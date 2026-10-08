"""Private agent state uses the service account, never a shared writable file."""
import importlib.util
import os
from pathlib import Path
import sys
import tempfile
import types
import unittest
from unittest.mock import patch

sys.path.insert(0, str(Path(__file__).resolve().parents[2] / 'deploy'))
spec = importlib.util.spec_from_file_location('node_updater', Path(sys.path[0]) / 'update-node.py')
updater = importlib.util.module_from_spec(spec)
spec.loader.exec_module(updater)


class CredentialPermissions(unittest.TestCase):
    def setUp(self):
        self.temporary = tempfile.TemporaryDirectory()
        self.addCleanup(self.temporary.cleanup)
        self.state = Path(self.temporary.name)
        self.state.chmod(0o700)
        self.credential = self.state / 'credentials.json'
        self.credential.write_text('{}')
        self.credential.chmod(0o600)
        self.owner = os.getuid()
        self.patches = [patch.object(updater, 'STATE', self.state),
                        patch.object(updater.pwd, 'getpwnam', return_value=types.SimpleNamespace(pw_uid=self.owner))]
        for replacement in self.patches:
            replacement.start()
            self.addCleanup(replacement.stop)

    def test_private_service_owned_credentials_are_valid(self):
        updater.private_credentials()

    def test_other_service_owner_is_rejected(self):
        with patch.object(updater.pwd, 'getpwnam', return_value=types.SimpleNamespace(pw_uid=self.owner + 1)):
            with self.assertRaises(updater.UpdateError):
                updater.private_credentials()

    def test_shared_credentials_are_rejected(self):
        self.credential.chmod(0o640)
        with self.assertRaises(updater.UpdateError):
            updater.private_credentials()

    def test_shared_state_directory_is_rejected(self):
        self.state.chmod(0o750)
        with self.assertRaises(updater.UpdateError):
            updater.private_credentials()

    def test_linked_credentials_are_rejected(self):
        target = self.state / 'other.json'
        self.credential.rename(target)
        self.credential.symlink_to(target)
        with self.assertRaises(updater.UpdateError):
            updater.private_credentials()

    def test_linked_state_directory_is_rejected(self):
        link = self.state / 'linked-state'
        link.symlink_to(self.state, target_is_directory=True)
        with patch.object(updater, 'STATE', link):
            with self.assertRaises(updater.UpdateError):
                updater.private_credentials()


class MetricsUpgrade(unittest.TestCase):
    def setUp(self):
        temporary = tempfile.TemporaryDirectory()
        self.addCleanup(temporary.cleanup)
        root = Path(temporary.name)
        self.override = root/'unit.d/20-host-metrics.conf'
        self.backup = root/'backup'
        self.backup.mkdir()
        self.calls = []
        for replacement in (patch.object(updater, 'METRICS_OVERRIDE', self.override),
                            patch.object(updater, 'private_regular', lambda path: None),
                            patch.object(updater, 'run', lambda args: self.calls.append(args))):
            replacement.start()
            self.addCleanup(replacement.stop)

    def test_adds_only_managed_metrics_and_rolls_back(self):
        unit = self.override.parent.parent/'hl-panel-edge-agent.service'
        unit.write_text('[Service]\n# custom limits remain\nMemoryMax=256M\n')
        updater.metrics_update(self.backup)
        self.assertEqual(self.override.read_text(), updater.METRICS_CONTENT)
        self.assertIn('MemoryMax=256M', unit.read_text())
        updater.metrics_update(self.backup)
        self.assertEqual(len(self.calls), 1)
        updater.restore_metrics(self.backup)
        self.assertFalse(self.override.exists())
        self.assertEqual(len(self.calls), 2)

    def test_existing_managed_configuration_survives_rollback(self):
        self.override.parent.mkdir()
        self.override.write_text(updater.METRICS_CONTENT)
        updater.metrics_update(self.backup)
        updater.restore_metrics(self.backup)
        self.assertTrue(self.override.exists())
        self.assertFalse(self.calls)

    def test_rejects_custom_file_and_changed_configuration(self):
        updater.metrics_update(self.backup)
        self.override.write_text('[Service]\n# custom configuration\n')
        with self.assertRaises(updater.UpdateError):
            updater.metrics_update(self.backup)
        with self.assertRaises(updater.UpdateError):
            updater.restore_metrics(self.backup)
        self.assertIn('custom configuration', self.override.read_text())

    def test_rejects_symlinked_configuration(self):
        target = self.backup/'custom.conf'
        target.write_text(updater.METRICS_CONTENT)
        self.override.parent.mkdir()
        self.override.symlink_to(target)
        with self.assertRaises(updater.UpdateError):
            updater.metrics_update(self.backup)


if __name__ == '__main__':
    unittest.main()
