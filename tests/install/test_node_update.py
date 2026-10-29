"""Private agent state uses the service account, never a shared writable file."""
import importlib.util
import contextlib
import io
import json
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


class CoreCompatibility(unittest.TestCase):
    def test_compatible_core_is_accepted(self):
        with patch.object(updater, 'run', return_value=b'Xray 26.7.28 (Xray)\n'):
            updater.verify_xray_version(Path('/candidate/xray'))

    def test_new_handshake_and_unexpected_versions_are_rejected(self):
        for version in ('26.10.10', '26.9.8', '26.3.27', '26.7.280'):
            with self.subTest(version=version), patch.object(updater, 'run', return_value=('Xray '+version+' (Xray)\n').encode()):
                with self.assertRaises(updater.UpdateError):
                    updater.verify_xray_version(Path('/candidate/xray'))


class UpgradeBBR(unittest.TestCase):
    def setUp(self):
        self.temporary = tempfile.TemporaryDirectory()
        self.addCleanup(self.temporary.cleanup)
        self.release = Path(self.temporary.name)
        (self.release/'deploy').mkdir()
        self.helper = self.release/'deploy/configure-node-bbr2.py'
        self.fallback = self.release/'deploy/configure-node-bbr.py'
        self.helper.write_text('# verified release helper\n')
        self.fallback.write_text('# verified fallback\n')
        self.stack = contextlib.ExitStack()
        self.addCleanup(self.stack.close)
        self.stack.enter_context(contextlib.redirect_stdout(io.StringIO()))
        self.stack.enter_context(contextlib.redirect_stderr(io.StringIO()))

    def test_uses_only_verified_release_helper(self):
        result = types.SimpleNamespace(returncode=0, stdout=b'BBR configured\n', stderr=b'')
        with patch.object(updater.subprocess, 'run', return_value=result) as run:
            self.assertTrue(updater.configure_tcp_bbr(self.release))
        run.assert_called_once_with([sys.executable, str(self.helper)], capture_output=True, timeout=120)

    def test_missing_or_linked_helpers_do_not_run(self):
        for path in (self.helper, self.fallback):
            with self.subTest(path=path.name), patch.object(updater.subprocess, 'run') as run:
                content = path.read_bytes()
                path.unlink()
                self.assertFalse(updater.configure_tcp_bbr(self.release))
                target = self.release/'target.py'
                target.write_bytes(content)
                path.symlink_to(target)
                self.assertFalse(updater.configure_tcp_bbr(self.release))
                run.assert_not_called()
                path.unlink()
                path.write_bytes(content)

    def test_failure_does_not_undo_healthy_program_update(self):
        failures = (OSError('unavailable'), updater.subprocess.TimeoutExpired('helper', 120))
        for error in failures:
            with self.subTest(error=type(error).__name__), patch.object(updater.subprocess, 'run', side_effect=error):
                self.assertFalse(updater.configure_tcp_bbr(self.release))
        with patch.object(updater.subprocess, 'run', return_value=types.SimpleNamespace(returncode=1, stdout=b'', stderr=b'failed')):
            self.assertFalse(updater.configure_tcp_bbr(self.release))


class TelemetryConfiguration(unittest.TestCase):
    def test_atomic_config_install_and_restore_preserve_owner_permissions(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            config = root/'edge-agent.json'
            original = {'control_plane_url':'https://panel.example', 'heartbeat_interval':'20s', 'engine_mode':'mixed'}
            config.write_text(json.dumps(original))
            config.chmod(0o640)
            before = config.stat()
            backup = root/'saved.json'
            backup.write_bytes(config.read_bytes())
            candidate = root/'candidate.json'
            updated = dict(original, heartbeat_interval='1s')
            candidate.write_text(json.dumps(updated))
            with patch.object(updater, 'CONFIG', config):
                updater.install_config(candidate)
                self.assertEqual(json.loads(config.read_text()), updated)
                after = config.stat()
                self.assertEqual((after.st_uid, after.st_gid, after.st_mode & 0o777),
                                 (before.st_uid, before.st_gid, before.st_mode & 0o777))
                updater.install_config(backup)
                self.assertEqual(json.loads(config.read_text()), original)


class DependencyOnlyRepair(unittest.TestCase):
    def test_repair_and_failure_rollback_never_stop_or_replace_core(self):
        for fail in (False, True):
            with self.subTest(failure=fail), tempfile.TemporaryDirectory() as directory:
                root = Path(directory)
                release = root/'release'
                (release/'deploy').mkdir(parents=True)
                (release/'deploy/install-node-update.py').write_text('# candidate helper')
                backup = root/'backup'
                backup.mkdir()
                commands = []
                def run(argv):
                    commands.append(argv)
                    if fail and '--repair-assets' in argv:
                        raise updater.UpdateError('synthetic repair failure')
                with patch.object(updater, 'run', side_effect=run), contextlib.redirect_stdout(io.StringIO()):
                    if fail:
                        with self.assertRaises(updater.UpdateError):
                            updater.repair_dependencies(release, backup)
                    else:
                        updater.repair_dependencies(release, backup)
                self.assertFalse((backup/'rollback.sh').exists())
                self.assertFalse(any(argv[0] == 'systemctl' for argv in commands))
                self.assertFalse(any(str(path) in argv for argv in commands for path in ('xray', 'gost', 'edge-agent')))
                self.assertEqual(any('--restore' in argv for argv in commands), fail)


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
