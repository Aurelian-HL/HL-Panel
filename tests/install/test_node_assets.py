"""Dependency/resource repair must preserve running engines and custom data."""
import contextlib
import importlib.util
import io
import os
from pathlib import Path
import shutil
import subprocess
import tempfile
import types
import unittest
from unittest.mock import patch
REAL_RUN = subprocess.run

DEPLOY = Path(__file__).resolve().parents[2] / 'deploy'
spec = importlib.util.spec_from_file_location('node_asset_installer', DEPLOY / 'install-node-update.py')
installer = importlib.util.module_from_spec(spec)
spec.loader.exec_module(installer)


class AssetRepair(unittest.TestCase):
    def setUp(self):
        self.stack = contextlib.ExitStack()
        self.addCleanup(self.stack.close)
        self.directory = Path(self.stack.enter_context(tempfile.TemporaryDirectory()))
        self.root = self.directory / 'program'
        (self.root / 'node-engines').mkdir(parents=True)
        self.release = self.directory / 'release'
        (self.release / 'bin').mkdir(parents=True)
        (self.release / 'deploy').mkdir()
        for name in installer.ASSET_NAMES:
            (self.release / 'bin' / name).write_bytes(b'official resource ' + name.encode())
        (self.release / 'bin/xray').write_text('fixture')
        (self.release / 'deploy/configure-node-traffic-control.py').write_text('fixture')
        self.stack.enter_context(patch.object(installer, 'ROOT', self.root))
        self.stack.enter_context(patch.object(installer, 'safe'))
        self.commands = self.stack.enter_context(patch.object(installer.subprocess, 'run',
            return_value=types.SimpleNamespace(stdout=b'Xray 26.7.28 (fixture)')))
        self.stack.enter_context(contextlib.redirect_stdout(io.StringIO()))

    def test_repairs_only_missing_or_empty_files_and_is_idempotent(self):
        existing = self.root / 'node-engines/geosite.dat'
        existing.write_bytes(b'custom resource must survive')
        before = existing.stat().st_mtime_ns
        empty = self.root / 'node-engines/geoip.dat'
        empty.touch()
        installer.repair_assets(self.release)
        self.assertEqual(empty.read_bytes(), (self.release / 'bin/geoip.dat').read_bytes())
        self.assertEqual(existing.read_bytes(), b'custom resource must survive')
        stamps = (empty.stat().st_mtime_ns, existing.stat().st_mtime_ns)
        installer.repair_assets(self.release)
        self.assertEqual((empty.stat().st_mtime_ns, existing.stat().st_mtime_ns), stamps)
        self.assertEqual(existing.stat().st_mtime_ns, before)
        self.assertFalse(any(call.args[0][0] == 'systemctl' for call in self.commands.call_args_list))

    def test_preflight_validates_resources_without_writing_installation(self):
        plan = installer.preflight(self.release)
        self.assertEqual(set(plan), set(installer.ASSET_NAMES))
        self.assertEqual(list((self.root / 'node-engines').iterdir()), [])
        calls = [call.args[0] for call in self.commands.call_args_list]
        self.assertTrue(any('-test' in call for call in calls))
        self.assertTrue(any('--check' in call for call in calls))
        self.assertFalse(any('--install-missing' in call for call in calls))

    def test_second_write_failure_restores_first_resource(self):
        empty = self.root / 'node-engines/geoip.dat'
        empty.touch()
        actual_replace = installer.replace
        def replace(source, dest, mode):
            if dest.name == 'geosite.dat':
                raise OSError('simulated full disk')
            actual_replace(source, dest, mode)
        with patch.object(installer, 'replace', side_effect=replace):
            with self.assertRaises(OSError):
                installer.repair_assets(self.release)
        self.assertEqual(empty.read_bytes(), b'')
        self.assertFalse((self.root / 'node-engines/geosite.dat').exists())

    def test_corrupt_retained_resource_rejects_without_overwriting_it(self):
        existing = self.root / 'node-engines/geoip.dat'
        existing.write_bytes(b'corrupt')
        def command(argv, **kwargs):
            if '-test' in argv:
                raise subprocess.CalledProcessError(1, argv)
            return types.SimpleNamespace(stdout=b'Xray 26.7.28 (fixture)')
        self.commands.side_effect = command
        with self.assertRaisesRegex(ValueError, 'resource validation failed'):
            installer.repair_assets(self.release)
        self.assertEqual(existing.read_bytes(), b'corrupt')
        self.assertFalse((self.root / 'node-engines/geosite.dat').exists())

    def test_missing_release_resource_fails_before_package_install(self):
        (self.release / 'bin/geosite.dat').unlink()
        with patch.object(installer, 'safe', side_effect=lambda path: None):
            with self.assertRaisesRegex(ValueError, 'missing nonempty'):
                installer.preflight(self.release, install_missing=True)
        self.assertFalse(any('--install-missing' in call.args[0] for call in self.commands.call_args_list))

    @unittest.skipUnless(os.environ.get('HL_TEST_XRAY_ASSET_DIRECTORY'), 'real pinned Xray fixture not configured')
    def test_real_xray_accepts_repaired_assets_and_rejects_corruption(self):
        source = Path(os.environ['HL_TEST_XRAY_ASSET_DIRECTORY'])
        for name in ('xray', *installer.ASSET_NAMES):
            shutil.copy2(source / name, self.release / 'bin' / name)
        # Real Xray, isolated routing config, no listeners or engine restarts.
        def run(argv, **kwargs):
            if argv[0] == str(self.release / 'bin/xray'):
                return REAL_RUN(argv, **kwargs)
            return types.SimpleNamespace(stdout=b'')
        # subprocess.run is already mocked; unwrap it for the subprocess below.
        with patch.object(installer.subprocess, 'run', side_effect=run):
            installer.repair_assets(self.release)
            (self.root / 'node-engines/geoip.dat').write_bytes(b'truncated')
            with self.assertRaisesRegex(ValueError, 'resource validation failed'):
                installer.preflight(self.release)


@unittest.skipUnless(getattr(os, 'geteuid', lambda: -1)() == 0, 'root-owned Linux resources')
class AssetPathSafety(unittest.TestCase):
    def test_linked_or_writable_resources_and_parent_are_rejected(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            resource = root / 'data.dat'
            resource.write_bytes(b'fixture')
            resource.chmod(0o666)
            with self.assertRaises(ValueError):
                installer.safe(resource)
            resource.chmod(0o644)
            link = root / 'linked.dat'
            link.symlink_to(resource)
            with self.assertRaises(ValueError):
                installer.safe(link)
            linked_parent = root / 'node-engines'
            linked_parent.symlink_to(root, target_is_directory=True)
            with self.assertRaises(ValueError):
                installer.safe_parent(linked_parent / 'missing.dat')


if __name__ == '__main__':
    unittest.main()
