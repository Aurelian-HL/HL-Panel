"""Automatic stale-install replacement; temporary files, mocked systemd only."""
import contextlib
import importlib.util
import os
from pathlib import Path
import stat
import tempfile
import types
import unittest
from unittest.mock import patch

DEPLOY = Path(__file__).resolve().parents[2] / 'deploy'
spec = importlib.util.spec_from_file_location('unenrolled_reset', DEPLOY / 'reset-unenrolled-node.py')
reset = importlib.util.module_from_spec(spec)
spec.loader.exec_module(reset)


class AutomaticReplacement(unittest.TestCase):
    def setUp(self):
        if os.name != 'nt' and os.geteuid() != 0:
            self.skipTest('POSIX installation ownership checks require root')
        self.stack = contextlib.ExitStack()
        self.addCleanup(self.stack.close)
        # Production checks every ancestor. Keep the real Linux fixtures under
        # a private root-owned parent instead of the world-writable /tmp.
        base = Path(self.stack.enter_context(tempfile.TemporaryDirectory(
            prefix='hl-install-fixture-', dir='/root' if os.name != 'nt' else None)))
        for name, path in dict(ROOT=base/'program', CONFIG=base/'config',
                               STATE=base/'state', UNITS=base/'units').items():
            path.mkdir(mode=0o700 if name == 'STATE' else 0o755)
            self.stack.enter_context(patch.object(reset, name, path))
        (reset.ROOT/'node-engines').mkdir()
        self.commands = []
        self.stack.enter_context(patch.object(reset, 'command', side_effect=self.command))
        self.stack.enter_context(contextlib.redirect_stdout(__import__('io').StringIO()))
        if os.name == 'nt':
            # Windows cannot enforce POSIX uid/mode. Path and lifecycle checks
            # still run; real ownership validation also runs in Linux release CI.
            self.stack.enter_context(patch.object(reset, 'safe_directory'))
        self.protected = {}
        for path in (reset.STATE/'usage.json', reset.STATE/'desired.json',
                     reset.ROOT/'control-api', reset.CONFIG/'control-api.env',
                     reset.ROOT/'node-engines/custom.dat', reset.UNITS/'x-ui.service'):
            path.write_bytes(b'preserved fixture')
            self.protected[path] = path.read_bytes()

    def command(self, *args, **kwargs):
        self.commands.append(args)
        return types.SimpleNamespace(stdout=b'loaded' if args[1] == 'show' else b'',
                                     returncode=3 if args[1] == 'is-active' else 0)

    def assert_preserved(self):
        for path, content in self.protected.items():
            self.assertEqual(path.read_bytes(), content)

    def test_different_version_invalid_config_and_stale_token_removed_automatically(self):
        for path in reset.managed_files():
            path.write_text('different-version or unfinished install')
        reset.reset_unenrolled()
        self.assertTrue(all(not path.exists() for path in reset.managed_files()))
        self.assertIn(('systemctl', 'stop', reset.SERVICE), self.commands)
        self.assertEqual(self.commands[-1], ('systemctl', 'daemon-reload'))
        self.assert_preserved()

    def test_partial_install_without_unit_is_cleaned(self):
        (reset.ROOT/'edge-agent').write_text('old binary')
        def command(*args, **kwargs):
            self.commands.append(args)
            return types.SimpleNamespace(stdout=b'not-found' if args[1] == 'show' else b'', returncode=4 if args[1] == 'show' else 0)
        with patch.object(reset, 'command', side_effect=command):
            reset.reset_unenrolled()
        self.assertFalse((reset.ROOT/'edge-agent').exists())
        self.assertFalse(any(args[1] == 'stop' for args in self.commands))
        self.assert_preserved()

    def test_engine_only_residue_does_not_block_reinstallation(self):
        (reset.ROOT/'node-engines/xray').write_text('old core')
        reset.reset_unenrolled()
        self.assertFalse((reset.ROOT/'node-engines/xray').exists())
        self.assert_preserved()

    def test_fresh_install_skips_service_operations(self):
        reset.reset_unenrolled()
        self.assertFalse(self.commands)
        self.assert_preserved()

    def test_registered_identity_refuses_cleanup_before_service_change(self):
        credential = reset.STATE/'credentials.json'
        credential.write_text('fixture private identity')
        (reset.ROOT/'edge-agent').write_text('old program')
        with self.assertRaisesRegex(ValueError, '注册身份'):
            reset.reset_unenrolled()
        self.assertFalse(self.commands)
        self.assertEqual(credential.read_text(), 'fixture private identity')
        self.assertTrue((reset.ROOT/'edge-agent').exists())
        self.assert_preserved()

    def test_identity_created_while_stopping_preserves_programs_and_restarts(self):
        (reset.ROOT/'edge-agent').write_text('existing program')
        def command(*args, **kwargs):
            result = self.command(*args, **kwargs)
            if args[1] == 'stop':
                (reset.STATE/'credentials.json').write_text('newly enrolled identity')
            return result
        with patch.object(reset, 'command', side_effect=command):
            with self.assertRaisesRegex(ValueError, '刚完成注册'):
                reset.reset_unenrolled()
        self.assertTrue((reset.ROOT/'edge-agent').exists())
        self.assertEqual(self.commands[-1], ('systemctl', 'start', reset.SERVICE))
        self.assert_preserved()

    def test_failed_service_inspection_does_not_delete_anything(self):
        path = reset.ROOT/'edge-agent'
        path.write_text('old program')
        with patch.object(reset, 'command', return_value=types.SimpleNamespace(stdout=b'', returncode=1)):
            with self.assertRaisesRegex(ValueError, '服务状态'):
                reset.reset_unenrolled()
        self.assertEqual(path.read_text(), 'old program')
        self.assert_preserved()

    def test_service_that_stays_active_prevents_deletion(self):
        path = reset.ROOT/'edge-agent'
        path.write_text('old program')
        with patch.object(reset, 'command', return_value=types.SimpleNamespace(stdout=b'loaded', returncode=0)):
            with self.assertRaisesRegex(ValueError, '尚未停止'):
                reset.reset_unenrolled()
        self.assertEqual(path.read_text(), 'old program')

    def test_directory_at_any_managed_file_aborts_before_any_deletion(self):
        (reset.ROOT/'edge-agent').write_text('old program')
        (reset.CONFIG/'edge-agent-enrollment.env').mkdir()
        with self.assertRaisesRegex(ValueError, '特殊文件'):
            reset.reset_unenrolled()
        self.assertFalse(self.commands)
        self.assertTrue((reset.ROOT/'edge-agent').exists())
        self.assert_preserved()

    @unittest.skipIf(os.name == 'nt', 'POSIX symbolic links')
    def test_symlink_to_other_program_or_identity_is_never_followed(self):
        for path in (reset.CONFIG/'edge-agent.json', reset.STATE/'credentials.json',
                     reset.ROOT/'node-engines/xray'):
            with self.subTest(path=path):
                path.symlink_to(reset.ROOT/'control-api')
                with self.assertRaises(ValueError):
                    reset.reset_unenrolled()
                path.unlink()
                self.assertFalse(self.commands)
                self.assert_preserved()


class PathSafety(unittest.TestCase):
    def test_directory_rejects_links_wrong_owner_or_writable_parent(self):
        directory = Path('/isolated-fixture/program')
        for mode, uid, linked in ((stat.S_IFDIR | 0o755, 0, True),
                                  (stat.S_IFDIR | 0o755, 123, False),
                                  (stat.S_IFDIR | 0o777, 0, False),
                                  (stat.S_IFREG | 0o755, 0, False)):
            with self.subTest(mode=mode, uid=uid, linked=linked), \
                    patch.object(Path, 'exists', return_value=True), \
                    patch.object(Path, 'is_symlink', return_value=linked), \
                    patch.object(Path, 'lstat', return_value=types.SimpleNamespace(st_uid=uid, st_mode=mode)):
                with self.assertRaises(ValueError):
                    reset.safe_directory(directory)


if __name__ == '__main__':
    unittest.main()
