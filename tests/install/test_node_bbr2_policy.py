"""BBR2 policy tests: fake kernel and temporary files only."""
import contextlib
import importlib.util
import io
from pathlib import Path
import tempfile
import unittest
from unittest.mock import patch

REPO = Path(__file__).resolve().parents[2]
spec = importlib.util.spec_from_file_location(
    'node_bbr2', REPO / 'deploy' / 'configure-node-bbr2.py')
bbr2 = importlib.util.module_from_spec(spec)
spec.loader.exec_module(bbr2)


class BBR2Tests(unittest.TestCase):
    def setUp(self):
        self.stack = contextlib.ExitStack()
        self.addCleanup(self.stack.close)
        self.root = Path(self.stack.enter_context(tempfile.TemporaryDirectory()))
        self.config_dir = self.root / 'sysctl.d'
        self.config_dir.mkdir()
        self.config = self.config_dir / bbr2.CONFIG.name
        self.backups = self.root / 'backups'
        self.fallback = bbr2.load_bbr_fallback()
        self.fallback_config = self.config_dir / self.fallback.CONFIG.name
        for name, value in dict(
                CONFIG=self.fallback_config,
                DIRECTORIES=(self.config_dir,),
                LEGACY=self.root / 'sysctl.conf').items():
            self.stack.enter_context(patch.object(self.fallback, name, value))
        self.stack.enter_context(patch.object(self.fallback, 'trusted'))
        self.stack.enter_context(patch.object(self.fallback, 'command', self.command))
        self.stack.enter_context(patch.object(bbr2, 'load_bbr_fallback', return_value=self.fallback))
        # Do not depend on the host having modprobe, and never reach its sysctl.
        self.stack.enter_context(patch.object(bbr2.shutil, 'which', return_value=None))
        for name, value in dict(
                CONFIG=self.config,
                DIRECTORIES=(self.config_dir,),
                LEGACY=self.root / 'sysctl.conf').items():
            self.stack.enter_context(patch.object(bbr2, name, value))
        self.stack.enter_context(patch.object(bbr2, 'trusted'))
        self.stack.enter_context(patch.object(bbr2.os, 'chown', create=True))
        self.stack.enter_context(contextlib.redirect_stdout(io.StringIO()))
        self.stack.enter_context(contextlib.redirect_stderr(io.StringIO()))
        self.kernel = 'cubic'
        self.available = 'reno cubic bbr2'
        self.calls = []
        self.stack.enter_context(patch.object(bbr2, 'command', self.command))

    def command(self, args):
        self.calls.append(args)
        if args == ['sysctl', '-n', bbr2.KEY]:
            return self.kernel
        if args == ['sysctl', '-n', bbr2.AVAILABLE]:
            return self.available
        if args == ['modprobe', bbr2.KERNEL_MODULE]:
            self.available += ' bbr2'
            return ''
        self.assertEqual(args[:3], ['sysctl', '-q', '-w'])
        value = args[3].split('=', 1)[1]
        self.kernel = value
        return ''

    def test_enable_writes_and_reads_back_bbr2(self):
        self.assertEqual(bbr2.enable(), 'bbr2')
        self.assertEqual(self.kernel, 'bbr2')
        self.assertEqual(self.config.read_bytes(), bbr2.CONTENT)
        self.assertFalse(self.backups.exists())
        self.assertIn(['sysctl', '-q', '-w', bbr2.KEY + '=bbr2'], self.calls)

    def test_existing_bbr_is_upgraded_to_bbr2_without_archive(self):
        self.kernel = 'bbr'
        self.assertEqual(bbr2.enable(), 'bbr2')
        self.assertEqual(self.kernel, 'bbr2')
        self.assertIsNone(bbr2.enable())
        self.assertFalse(self.backups.exists())

    def test_unknown_bbr_variant_is_preserved(self):
        self.kernel = 'bbr3'
        self.assertIsNone(bbr2.enable())
        self.assertEqual(self.kernel, 'bbr3')
        self.assertFalse(self.config.exists())

    def test_bbr2_unavailable_does_not_claim_or_change_algorithm(self):
        self.available = 'reno cubic'
        self.assertIsNone(bbr2.enable())
        self.assertEqual(self.kernel, 'cubic')
        self.assertFalse(self.config.exists())
        self.assertNotIn(['modprobe', 'tcp_bbr'], self.calls)
        self.assertFalse(self.fallback_config.exists())

    def test_bbr2_unavailable_enables_real_bbr_helper_without_archive(self):
        self.available = 'reno cubic bbr'
        self.assertEqual(bbr2.enable(), 'bbr')
        self.assertEqual(self.kernel, 'bbr')
        self.assertFalse(self.config.exists())
        self.assertEqual(self.fallback_config.read_bytes(), self.fallback.CONTENT)
        self.assertIsNone(bbr2.enable())
        self.assertFalse(self.backups.exists())

    def test_fallback_preserves_operator_conflict(self):
        self.available = 'reno cubic bbr'
        operator = self.config_dir / '99-operator.conf'
        operator.write_text(bbr2.KEY + '=cubic\n')
        with self.assertRaisesRegex(bbr2.BBRError, '普通 TCP BBR 回退未完成'):
            bbr2.enable()
        self.assertEqual(self.kernel, 'cubic')
        self.assertFalse(self.config.exists())
        self.assertFalse(self.fallback_config.exists())

    def test_supported_bbr2_failure_does_not_silently_fallback(self):
        with patch.object(bbr2, 'write', side_effect=bbr2.BBRError('denied')):
            with self.assertRaisesRegex(bbr2.BBRError, '已恢复原算法及配置'):
                bbr2.enable()
        self.assertEqual(self.kernel, 'cubic')
        self.assertFalse(self.config.exists())
        self.assertFalse(self.fallback_config.exists())
        bbr2.load_bbr_fallback.assert_not_called()

    def test_installer_and_release_require_bbr2_asset(self):
        installer = (REPO / 'deploy' / 'install-node.sh').read_text(encoding='utf-8')
        self.assertIn('configure-node-bbr2.py', installer)
        self.assertNotIn('configure-node-bbr.py"', installer)
        build = (REPO / 'deploy' / 'build-release.ps1').read_text(encoding='utf-8')
        verify = (REPO / 'deploy' / 'verify-release.ps1').read_text(encoding='utf-8')
        self.assertIn("'configure-node-bbr2.py'", build)
        self.assertIn("'deploy/configure-node-bbr2.py'", verify)
        self.assertIn("'deploy/configure-node-bbr.py'", installer)
        self.assertIn("'configure-node-bbr.py'", build)
        self.assertIn("'deploy/configure-node-bbr.py'", verify)
        updater = (REPO / 'deploy' / 'update-node.py').read_text(encoding='utf-8')
        self.assertIn('def configure_tcp_bbr(release):', updater)
        self.assertIn('configure_tcp_bbr(release)', updater)
        workflow = (REPO / '.github' / 'workflows' / 'release.yml').read_text(encoding='utf-8')
        self.assertIn('python3 -m py_compile deploy/configure-node-bbr2.py', workflow)
        self.assertIn('deploy/configure-node-bbr2.py', workflow)
        acceptance = (REPO / 'tests' / 'install' / 'node_bbr_linux.py').read_text(encoding='utf-8')
        self.assertIn("repo/'deploy/configure-node-bbr2.py'", acceptance)
        self.assertIn("== 'bbr2'", acceptance)


if __name__ == '__main__':
    unittest.main()
