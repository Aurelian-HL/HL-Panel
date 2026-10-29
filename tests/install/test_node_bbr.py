#!/usr/bin/env python3
"""BBR policy tests: fake kernel, temporary files, no real network mutations."""
import contextlib
import importlib.util
import io
from types import SimpleNamespace
from pathlib import Path
import tempfile
import unittest
from unittest.mock import patch

REPO = Path(__file__).resolve().parents[2]
spec = importlib.util.spec_from_file_location('node_bbr', REPO/'deploy/configure-node-bbr.py')
bbr = importlib.util.module_from_spec(spec)
spec.loader.exec_module(bbr)


class BBRTests(unittest.TestCase):
    def setUp(self):
        self.stack = contextlib.ExitStack()
        self.addCleanup(self.stack.close)
        self.root = Path(self.stack.enter_context(tempfile.TemporaryDirectory()))
        self.config_dir = self.root/'sysctl.d'
        self.config_dir.mkdir()
        self.config = self.config_dir/bbr.CONFIG.name
        self.backups = self.root/'backups'
        for name, value in dict(CONFIG=self.config,
            DIRECTORIES=(self.config_dir,), LEGACY=self.root/'sysctl.conf').items():
            self.stack.enter_context(patch.object(bbr, name, value))
        # Windows cannot emulate Linux ownership. Permission validation itself is
        # separately exercised with synthetic stat values below.
        self.stack.enter_context(patch.object(bbr, 'trusted'))
        self.stack.enter_context(patch.object(bbr.os, 'chown', create=True))
        self.output = self.stack.enter_context(contextlib.redirect_stdout(io.StringIO()))
        self.errors = self.stack.enter_context(contextlib.redirect_stderr(io.StringIO()))
        self.kernel = 'cubic'
        self.available = 'reno cubic bbr'
        self.calls = []
        self.failure = ''
        self.stack.enter_context(patch.object(bbr, 'command', self.command))

    def command(self, args):
        self.calls.append(args)
        if args == ['sysctl', '-n', bbr.KEY]:
            return self.kernel
        if args == ['sysctl', '-n', bbr.AVAILABLE]:
            return self.available
        if args == ['modprobe', 'tcp_bbr']:
            if self.failure == 'module':
                raise bbr.BBRError('fake module unavailable')
            self.available += ' bbr'
            return ''
        self.assertEqual(args[:3], ['sysctl', '-q', '-w'])
        self.assertTrue(args[3].startswith(bbr.KEY+'='))
        value = args[3].split('=', 1)[1]
        if self.failure == 'denied':
            raise bbr.BBRError('fake read-only kernel')
        if self.failure == 'mismatch' and value == 'bbr':
            return ''
        if self.failure == 'restore' and value == 'cubic':
            raise bbr.BBRError('fake rollback failure')
        self.kernel = value
        if self.failure in ('partial', 'restore') and value == 'bbr':
            raise bbr.BBRError('fake write changed kernel before failure')
        return ''

    def test_enable_readback_persist_idempotence_without_archive(self):
        self.assertEqual(bbr.enable(), 'bbr')
        self.assertEqual(self.kernel, 'bbr')
        self.assertEqual(self.config.read_bytes(), bbr.CONTENT)
        before = sorted(p.relative_to(self.root) for p in self.root.rglob('*'))
        self.calls.clear()
        self.assertIsNone(bbr.enable())
        self.assertFalse(any('-w' in args for args in self.calls))
        self.assertEqual(sorted(p.relative_to(self.root) for p in self.root.rglob('*')), before)
        self.assertFalse(self.backups.exists())

    def test_builtin_bbr_does_not_require_modprobe(self):
        with patch.object(bbr.shutil, 'which', return_value=None):
            bbr.enable()
        self.assertNotIn(['modprobe', 'tcp_bbr'], self.calls)

    def test_load_module_then_recheck(self):
        self.available = 'reno cubic'
        with patch.object(bbr.shutil, 'which', return_value='/sbin/modprobe'):
            bbr.enable()
        self.assertIn(['modprobe', 'tcp_bbr'], self.calls)
        self.assertEqual(self.kernel, 'bbr')

    def test_unsupported_kernel_and_missing_module_leave_no_files(self):
        self.available = 'reno cubic'
        for module in (None, '/sbin/modprobe'):
            with self.subTest(module=module):
                self.failure = 'module'
                with patch.object(bbr.shutil, 'which', return_value=module):
                    self.assertIsNone(bbr.enable())
                self.assertFalse(self.config.exists())
                self.assertFalse(self.backups.exists())
                self.assertEqual(self.kernel, 'cubic')

    def test_bbr_variant_is_not_downgraded(self):
        self.kernel = 'bbr3'
        bbr.enable()
        self.assertFalse(self.config.exists())
        self.assertEqual(self.calls, [['sysctl', '-n', bbr.KEY]])

    def test_existing_bbr_is_persisted_without_archive(self):
        self.kernel = 'bbr'
        self.assertEqual(bbr.enable(), 'bbr')
        self.assertEqual(self.kernel, 'bbr')
        self.assertEqual(self.config.read_bytes(), bbr.CONTENT)
        self.assertFalse(self.backups.exists())

    def test_existing_managed_file_is_preserved_on_failure(self):
        self.config.write_bytes(bbr.CONTENT)
        self.config.chmod(0o600)
        self.failure = 'partial'
        with self.assertRaisesRegex(bbr.BBRError, '已恢复'):
            bbr.enable()
        self.assertEqual(self.config.read_bytes(), bbr.CONTENT)
        self.assertEqual(self.kernel, 'cubic')
        if __import__('sys').platform != 'win32':
            self.assertEqual(self.config.stat().st_mode & 0o777, 0o600)

    def test_custom_file_is_not_overwritten(self):
        self.config.write_text('custom operator config')
        with self.assertRaises(bbr.BBRError):
            bbr.enable()
        self.assertEqual(self.config.read_text(), 'custom operator config')
        self.assertEqual(self.kernel, 'cubic')

    def test_later_sysctl_and_legacy_conflicts_are_not_overridden(self):
        for name in ('99-custom.conf', '../sysctl.conf'):
            other = self.config_dir/name
            other.write_text('net/ipv4/tcp_congestion_control = cubic\n')
            with self.subTest(name=name), self.assertRaises(bbr.BBRError):
                bbr.enable()
            self.assertFalse(self.config.exists())
            self.assertEqual(self.kernel, 'cubic')
            other.unlink()

    def test_earlier_setting_and_last_value_bbr_do_not_conflict(self):
        (self.config_dir/'10-default.conf').write_text(bbr.KEY+'=cubic\n')
        (self.config_dir/'99-custom.conf').write_text(bbr.KEY+'=cubic\n'+bbr.KEY+'=bbr\n')
        bbr.enable()
        self.assertEqual(self.kernel, 'bbr')

    def test_shadowed_vendor_setting_does_not_conflict(self):
        vendor = self.root/'vendor'
        vendor.mkdir()
        (self.config_dir/'99-vendor.conf').write_text('# disabled vendor setting\n')
        (vendor/'99-vendor.conf').write_text(bbr.KEY+'=cubic\n')
        with patch.object(bbr, 'DIRECTORIES', (self.config_dir, vendor)):
            bbr.enable()
        self.assertEqual(self.kernel, 'bbr')

    def test_conflicting_wildcards_require_operator_review(self):
        other = self.config_dir/'99-custom.conf'
        for key in ('net.ipv4.tcp_*control', '-net/ipv4/tcp_?ongestion_control',
                    'net.ipv4.tcp_[c]ongestion_control'):
            with self.subTest(key=key):
                other.write_text(key+' = cubic\n')
                with self.assertRaises(bbr.BBRError):
                    bbr.enable()
                self.assertFalse(self.config.exists())
                self.assertEqual(self.kernel, 'cubic')

    def test_matching_bbr_and_unrelated_wildcards_are_safe(self):
        (self.config_dir/'99-custom.conf').write_text(
            'net.ipv4.tcp_*control = bbr\nnet.ipv6.* = 0\n')
        bbr.enable()
        self.assertEqual(self.kernel, 'bbr')

    def test_failed_kernel_write_and_bad_readback_restore_files_and_runtime(self):
        for failure in ('partial', 'mismatch'):
            self.failure = failure
            with self.subTest(failure=failure), self.assertRaisesRegex(bbr.BBRError, '已恢复'):
                bbr.enable()
            self.assertFalse(self.config.exists())
            self.assertEqual(self.kernel, 'cubic')

    def test_readonly_kernel_restores_file_without_unnecessary_kernel_write(self):
        self.failure = 'denied'
        with self.assertRaisesRegex(bbr.BBRError, '已恢复'):
            bbr.enable()
        self.assertFalse(self.config.exists())
        self.assertEqual(self.kernel, 'cubic')

    def test_partial_failure_restore_failure_reports_algorithm_check(self):
        self.failure = 'restore'
        with self.assertRaisesRegex(bbr.BBRError, '恢复未完成.*检查 sysctl 配置与当前算法'):
            bbr.enable()
        self.assertFalse(self.config.exists())
        self.assertEqual(self.kernel, 'bbr')
        self.assertFalse(self.backups.exists())

    def test_persistence_failure_does_not_change_kernel(self):
        original = bbr.atomic
        def failing(path, *args, **kwargs):
            if path == self.config:
                raise OSError('fake disk failure')
            return original(path, *args, **kwargs)
        with patch.object(bbr, 'atomic', side_effect=failing), self.assertRaises(bbr.BBRError):
            bbr.enable()
        self.assertEqual(self.kernel, 'cubic')
        self.assertFalse(self.config.exists())

    def test_file_restore_failure_still_attempts_kernel_restore(self):
        self.config.write_bytes(bbr.CONTENT)
        original = bbr.atomic
        calls = []
        def failing(path, *args, **kwargs):
            if path == self.config:
                calls.append(path)
                if len(calls) == 2:
                    raise OSError('fake file restore failure')
            return original(path, *args, **kwargs)
        self.failure = 'partial'
        with patch.object(bbr, 'atomic', side_effect=failing), self.assertRaisesRegex(bbr.BBRError, '恢复未完成'):
            bbr.enable()
        self.assertEqual(self.kernel, 'cubic')

    def test_repeated_enable_preserves_later_admin_changes(self):
        bbr.enable()
        self.config.write_text('operator change')
        with self.assertRaises(bbr.BBRError):
            bbr.enable()
        self.assertEqual(self.config.read_text(), 'operator change')
        self.assertEqual(self.kernel, 'bbr')

    @unittest.skipIf(__import__('sys').platform == 'win32', 'Windows symlink privilege differs')
    def test_symlink_config_is_not_followed(self):
        target = self.root/'target'
        target.write_text('preserve')
        self.config.symlink_to(target)
        with self.assertRaises(bbr.BBRError):
            bbr.enable()
        self.assertEqual(target.read_text(), 'preserve')

    def test_installer_covers_both_success_paths_without_fatal_bbr_failure(self):
        script = (REPO/'deploy/install-node.sh').read_text(encoding='utf-8')
        self.assertEqual(script.count('\n  configure_tcp_bbr\n'), 1)
        self.assertIn('install_ready=1\nconfigure_tcp_bbr', script)
        self.assertIn('if ! python3 "$release/deploy/configure-node-bbr2.py"; then', script)


class BoundaryTests(unittest.TestCase):
    def test_trusted_rejects_nonroot_writable_and_symlink_paths(self):
        # Synthetic POSIX stat values exercise ownership even on Windows.
        for uid, mode, symlink, private, allowed in (
                (0, 0o100644, False, False, True),
                (1000, 0o100644, False, False, False),
                (0, 0o100664, False, False, False),
                (0, 0o100646, False, False, False),
                (0, 0o100600, True, False, False),
                (0, 0o40700, False, True, True),
                (0, 0o40750, False, True, False)):
            path = unittest.mock.Mock()
            path.stat.return_value = SimpleNamespace(st_uid=uid, st_mode=mode)
            path.is_symlink.return_value = symlink
            with self.subTest(uid=uid, mode=mode, symlink=symlink, private=private):
                if allowed:
                    bbr.trusted(path, private)
                else:
                    with self.assertRaises(bbr.BBRError):
                        bbr.trusted(path, private)

    def test_command_fails_closed_for_error_timeout_and_missing_executable(self):
        for error in (OSError('missing'), bbr.subprocess.TimeoutExpired(['sysctl'], 15)):
            with self.subTest(error=type(error).__name__), \
                    patch.object(bbr.subprocess, 'run', side_effect=error), \
                    self.assertRaises(bbr.BBRError):
                bbr.command(['sysctl', '-n', bbr.KEY])
        with patch.object(bbr.subprocess, 'run', return_value=SimpleNamespace(returncode=1)), \
                self.assertRaises(bbr.BBRError):
            bbr.command(['sysctl', '-n', bbr.KEY])


if __name__ == '__main__':
    unittest.main()
