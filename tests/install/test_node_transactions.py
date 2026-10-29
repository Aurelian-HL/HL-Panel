"""Node replacement lifecycle on temporary paths; all services/network mocked."""
import contextlib
import hashlib
import importlib.util
import io
import json
import os
from pathlib import Path
import sys
import tempfile
import types
import unittest
from unittest.mock import patch

DEPLOY = Path(__file__).resolve().parents[2] / 'deploy'
sys.path.insert(0, str(DEPLOY))


def module(name, filename):
    spec = importlib.util.spec_from_file_location(name, DEPLOY/filename)
    value = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(value)
    return value


updater = module('transaction_updater', 'update-node.py')
reinstaller = module('transaction_reinstaller', 'reinstall-node.py')
TASK = 'aaaaaaaa-aaaa-4aaa-aaaa-aaaaaaaaaaaa'


@unittest.skipUnless(getattr(os, 'geteuid', lambda: -1)() == 0, 'POSIX root-owned fixtures')
class UpdateTransactions(unittest.TestCase):
    def setUp(self):
        self.stack = contextlib.ExitStack()
        self.addCleanup(self.stack.close)
        self.base = Path(self.stack.enter_context(tempfile.TemporaryDirectory()))
        self.root = self.base/'program'
        (self.root/'node-engines').mkdir(parents=True)
        self.state = self.base/'state'
        self.state.mkdir(mode=0o700)
        (self.state/'credentials.json').write_text('{"node_id":"fixture","node_credential":"private-fixture"}')
        (self.state/'credentials.json').chmod(0o600)
        (self.state/'usage.json').write_text('{"bytes":12345}')
        self.config = self.base/'edge-agent.json'
        self.original_config = {'engine_mode': 'mixed', 'data_dir': str(self.state),
            'xray_binary_path': str(self.root/'node-engines/xray'),
            'gost_binary_path': str(self.root/'node-engines/gost'),
            'heartbeat_interval': '20s', 'control_plane_url': 'https://panel.example'}
        self.config.write_text(json.dumps(self.original_config))
        self.config.chmod(0o640)
        self.original_files = {}
        for name in (*updater.FILES, 'update.py', 'update-node.py'):
            (self.root/name).write_text('old '+name)
            self.original_files[name] = (self.root/name).read_bytes()
        self.update_state = self.base/'tasks'
        (self.update_state/TASK).mkdir(parents=True, mode=0o700)
        self.legacy = self.base/'old-archives'
        self.legacy.mkdir(mode=0o755)
        (self.base/'units').mkdir()
        self.commands = []
        self.payload = b'verified-fixture-archive'
        self.report = None
        for name, value in dict(ROOT=self.root, CONFIG=self.config, STATE=self.state,
                UPDATE_STATE=self.update_state, LEGACY_STAGING=self.legacy,
                METRICS_OVERRIDE=self.base/'units/service.d/20-host-metrics.conf').items():
            self.stack.enter_context(patch.object(updater, name, value))
        self.stack.enter_context(patch.object(updater.pwd, 'getpwnam',
            return_value=types.SimpleNamespace(pw_uid=0, pw_gid=0)))
        self.stack.enter_context(patch.object(updater, 'run', side_effect=self.command))
        self.active = self.stack.enter_context(patch.object(updater, 'active'))
        self.bbr = self.stack.enter_context(patch.object(updater, 'configure_tcp_bbr', return_value=True))
        self.stack.enter_context(patch.object(updater, 'request', side_effect=self.download))
        self.stack.enter_context(patch.object(updater, 'extract_verified', side_effect=self.extract))
        self.popen = self.stack.enter_context(patch.object(updater.subprocess, 'Popen'))
        self.stack.enter_context(contextlib.redirect_stdout(io.StringIO()))
        self.stack.enter_context(patch.dict(os.environ, {'HL_NODE_TRANSIENT_UPDATE': '1'}))
        self.before_state = {p.name: p.read_bytes() for p in self.state.iterdir()}

    def command(self, argv):
        self.commands.append(argv)
        return b'Xray 26.7.28 (fixture)' if argv[-1] == 'version' else b''

    def download(self, url, *args):
        if '/releases/tags/' in url:
            return json.dumps({'tag_name': 'v0.1.999', 'published_at': 'fixture'}).encode()
        if url.endswith('.sha256'):
            return (hashlib.sha256(self.payload).hexdigest()+'  hl-panel-linux-amd64.tar.gz').encode()
        return self.payload

    def extract(self, archive, release):
        names = [*updater.FILES.values(), 'deploy/install-node-update.py', 'deploy/node-update-worker.py',
                 'deploy/update.py', 'deploy/update-node.py',
                 'deploy/systemd/hl-panel-node-update.socket', 'deploy/systemd/hl-panel-node-update.service']
        for name in names:
            path = release/name
            path.parent.mkdir(parents=True, exist_ok=True)
            path.write_text('new '+name)

    def invoke(self, worker=False):
        argv = ['update-node.py', '--version', 'v0.1.999']
        if worker:
            self.report = self.update_state/TASK/'result.json'
            argv += ['--result-file', str(self.report)]
        with patch.object(sys, 'argv', argv):
            updater.main()

    def stages(self):
        return list(self.root.glob('.node-update-*'))+list(self.legacy.glob('update-*'))

    def assert_state_unchanged(self):
        self.assertEqual({p.name: p.read_bytes() for p in self.state.iterdir()}, self.before_state)

    def test_cli_success_cleans_transaction_and_preserves_identity_usage(self):
        self.invoke()
        self.assertFalse(self.stages())
        self.assertEqual((self.root/'edge-agent').read_text(), 'new bin/edge-agent')
        self.assertEqual(json.loads(self.config.read_text())['heartbeat_interval'], '1s')
        self.assert_state_unchanged()
        self.assertFalse(any(p.name in ('config-state.tar.gz', 'rollback.sh') for p in self.base.rglob('*')))

    def test_cleanup_failure_does_not_reclassify_success_or_hide_start_failure(self):
        with patch.object(updater, 'cleanup_transaction', side_effect=OSError('fixture busy')), contextlib.redirect_stderr(io.StringIO()):
            self.invoke()
            self.active.side_effect = [updater.UpdateError('fixture failed start'), None]
            with self.assertRaisesRegex(updater.UpdateError, 'fixture failed start'):
                self.invoke(worker=True)
        self.assertEqual(json.loads(self.report.read_text())['state'], 'rolled_back')
        self.assertTrue(Path(json.loads(self.report.read_text())['backup_directory']).is_dir())
        self.assert_state_unchanged()

    def test_explicit_recovery_cleanup_failure_keeps_success_and_recoverable_path(self):
        self.invoke(worker=True)
        stage, = self.stages()
        with patch.object(sys, 'argv', ['update-node.py', '--rollback', str(stage)]), \
                patch.object(updater, 'cleanup_transaction', side_effect=OSError('fixture busy')), \
                contextlib.redirect_stderr(io.StringIO()):
            updater.main()
        self.assertTrue(stage.is_dir())
        for name, content in self.original_files.items():
            self.assertEqual((self.root/name).read_bytes(), content)
        self.assert_state_unchanged()

    def test_worker_success_keeps_only_pending_recovery_until_verified(self):
        self.invoke(worker=True)
        stages = self.stages()
        self.assertEqual(len(stages), 1)
        self.assertTrue((stages[0]/'.verifying').is_file())
        self.assertFalse((stages[0]/'.switching').exists())
        self.assertEqual(json.loads(self.report.read_text())['state'], 'verifying')
        self.assert_state_unchanged()

    def test_download_and_preflight_failures_never_stop_service_or_leave_archive(self):
        for failure in ('download', 'preflight'):
            self.commands.clear()
            def command(argv):
                if '--preflight' in argv:
                    raise updater.UpdateError('fixture invalid resource')
                return self.command(argv)
            with self.subTest(failure=failure):
                replacement = patch.object(updater, 'request', side_effect=OSError('fixture download')) if failure == 'download' else patch.object(updater, 'run', side_effect=command)
                with replacement, self.assertRaises((updater.UpdateError, OSError)):
                    self.invoke()
                self.assertFalse(self.stages())
                self.assertNotIn(['systemctl', 'stop', updater.SERVICE], self.commands)
                self.assertEqual((self.root/'edge-agent').read_bytes(), self.original_files['edge-agent'])

    def test_start_failure_restores_programs_and_cleans_transaction(self):
        self.active.side_effect = [updater.UpdateError('fixture failed start'), None]
        with self.assertRaises(updater.UpdateError):
            self.invoke(worker=True)
        self.assertFalse(self.stages())
        self.assertEqual(json.loads(self.report.read_text())['state'], 'rolled_back')
        for name, content in self.original_files.items():
            self.assertEqual((self.root/name).read_bytes(), content)
        self.assertEqual(json.loads(self.config.read_text()), self.original_config)
        self.assert_state_unchanged()

    def test_failed_recovery_keeps_transaction_and_uncertain_receipt(self):
        self.active.side_effect = updater.UpdateError('fixture cannot start')
        with self.assertRaises(updater.UpdateError):
            self.invoke(worker=True)
        stages = self.stages()
        self.assertEqual(len(stages), 1)
        self.assertTrue((stages[0]/'.switching').is_file())
        self.assertTrue((stages[0]/'manifest.json').is_file())
        self.assertEqual(json.loads(self.report.read_text())['state'], 'uncertain')
        self.assert_state_unchanged()

    def test_old_worker_new_updater_accepts_0755_parent(self):
        with patch.dict(os.environ, {'HL_NODE_TRANSIENT_UPDATE': ''}):
            self.invoke(worker=True)
        stage, = self.stages()
        self.assertEqual(stage.parent, self.legacy)
        self.assertEqual(self.legacy.stat().st_mode & 0o777, 0o755)
        self.assertEqual(stage.stat().st_mode & 0o777, 0o700)
        self.popen.assert_called_once()
        self.assert_state_unchanged()

    def test_legacy_finalizer_cleans_only_same_completed_task(self):
        self.invoke(worker=True)
        stage, = self.stages()
        taskfile = self.update_state/'task.json'
        taskfile.write_text(json.dumps({'id': TASK, 'state': 'succeeded', 'backup_directory': str(stage)}))
        taskfile.chmod(0o600)
        updater.cleanup_legacy_task(TASK, stage)
        self.assertFalse(stage.exists())

    def test_legacy_finalizer_preserves_interrupted_or_replaced_task(self):
        self.invoke(worker=True)
        stage, = self.stages()
        for task in ({'id': TASK, 'state': 'uncertain'}, {'id': 'bbbbbbbb-bbbb-4bbb-bbbb-bbbbbbbbbbbb', 'state': 'succeeded'}):
            (self.update_state/'task.json').write_text(json.dumps(dict(task, backup_directory=str(stage))))
            (self.update_state/'task.json').chmod(0o600)
            # One loop is enough to prove an uncertain task never reaches deletion.
            with patch.object(updater.time, 'sleep', side_effect=RuntimeError('end fixture wait')):
                if task['id'] == TASK:
                    with self.assertRaises(RuntimeError):
                        updater.cleanup_legacy_task(TASK, stage)
                else:
                    updater.cleanup_legacy_task(TASK, stage)
            self.assertTrue(stage.exists())

    def test_legacy_cli_proof_requires_complete_programs_config_and_live_service(self):
        self.invoke(worker=True)
        stage, = self.stages()
        with patch.object(updater.subprocess, 'run', return_value=types.SimpleNamespace(returncode=0)):
            self.assertTrue(updater.legacy_cli_completed(stage))
            (self.root/'edge-agent').write_text('partial replacement')
            self.assertFalse(updater.legacy_cli_completed(stage))
        self.assertTrue(stage.exists())


@unittest.skipUnless(getattr(os, 'geteuid', lambda: -1)() == 0, 'POSIX root-owned fixtures')
class ReinstallTransactions(unittest.TestCase):
    def setUp(self):
        self.stack = contextlib.ExitStack()
        self.addCleanup(self.stack.close)
        self.base = Path(self.stack.enter_context(tempfile.TemporaryDirectory()))
        self.root = self.base/'program'
        (self.root/'node-engines').mkdir(parents=True)
        self.state = self.base/'state'
        self.state.mkdir(mode=0o700)
        self.config = self.base/'config/edge-agent.json'
        self.config.parent.mkdir()
        self.unit = self.base/'units/hl-panel-edge-agent.service'
        self.unit.parent.mkdir()
        self.unit.write_text('old service')
        self.config.write_text(json.dumps({'data_dir': str(self.state), 'engine_mode': 'mixed',
            'xray_binary_path': str(self.root/'node-engines/xray'), 'gost_binary_path': str(self.root/'node-engines/gost'),
            'control_plane_url': 'https://old.example', 'dial_host': '192.0.2.1'}))
        self.config.chmod(0o640)
        (self.state/'credentials.json').write_text('{"node_id":"fixture","node_credential":"fixture-secret"}')
        (self.state/'credentials.json').chmod(0o600)
        (self.state/'usage.json').write_text('{"bytes":987654}')
        self.release = self.base/'release'
        self.old_files = {}
        for installed, packaged in (('edge-agent', 'bin/edge-agent'), ('node-engines/xray', 'bin/xray'),
                ('node-engines/gost', 'bin/gost'), ('enroll-node.sh', 'deploy/edge-agent/enroll.sh'),
                ('reinstall-node.py', 'deploy/reinstall-node.py')):
            (self.root/installed).write_text('old '+installed)
            self.old_files[installed] = (self.root/installed).read_bytes()
            path = self.release/packaged
            path.parent.mkdir(parents=True, exist_ok=True)
            path.write_text('new '+installed)
        for name in ('deploy/systemd/hl-panel-edge-agent.service', 'deploy/install-node-update.py'):
            path = self.release/name
            path.parent.mkdir(parents=True, exist_ok=True)
            path.write_text('new fixture')
        for name, value in dict(ROOT=self.root, STATE=self.state, CONFIG=self.config, UNIT=self.unit, CA_FILE=self.config.parent/'ca.pem').items():
            self.stack.enter_context(patch.object(reinstaller, name, value))
        self.stack.enter_context(patch.object(reinstaller.pwd, 'getpwnam', return_value=types.SimpleNamespace(pw_uid=0, pw_gid=0)))
        self.commands = []
        def run(argv):
            self.commands.append(argv)
            return b'Xray 26.7.28 (fixture)' if argv[-1] == 'version' else b''
        self.stack.enter_context(patch.object(reinstaller, 'run', side_effect=run))
        self.started = self.stack.enter_context(patch.object(reinstaller, 'start_service'))
        self.enroll = self.stack.enter_context(patch.object(reinstaller, 'request_reenrollment'))
        self.stack.enter_context(patch.dict(os.environ, {'HL_INSTALL_ENROLLMENT_TOKEN': 'fixture-enrollment-token'}))
        self.stack.enter_context(contextlib.redirect_stdout(io.StringIO()))
        self.before_state = {p.name: p.read_bytes() for p in self.state.iterdir()}
        self.args = types.SimpleNamespace(release=self.release, version='v0.1.999',
            panel_url='https://new.example', node_address='192.0.2.2', ca_file='')

    def test_rejected_registration_never_stops_service_and_cleans_stage(self):
        original_config = self.config.read_bytes()
        self.enroll.side_effect = reinstaller.ReinstallError('fixture rejected')
        with self.assertRaises(reinstaller.ReinstallError):
            reinstaller.reinstall(self.args)
        self.assertFalse(list(self.root.glob('.node-reinstall-*')))
        self.assertNotIn(['systemctl', 'stop', reinstaller.SERVICE], self.commands)
        self.assertEqual(self.config.read_bytes(), original_config)
        self.started.assert_not_called()

    def test_success_cleans_stage_and_preserves_identity_and_usage(self):
        reinstaller.reinstall(self.args)
        self.assertFalse(list(self.root.glob('.node-reinstall-*')))
        self.assertEqual(json.loads(self.config.read_text())['control_plane_url'], self.args.panel_url)
        self.assertEqual({p.name: p.read_bytes() for p in self.state.iterdir()}, self.before_state)

    def test_cleanup_failure_does_not_report_successful_reinstall_as_failed(self):
        with patch.object(reinstaller.shutil, 'rmtree', side_effect=OSError('fixture busy')), contextlib.redirect_stderr(io.StringIO()):
            reinstaller.reinstall(self.args)
        self.assertEqual(json.loads(self.config.read_text())['control_plane_url'], self.args.panel_url)
        self.assertEqual({p.name: p.read_bytes() for p in self.state.iterdir()}, self.before_state)

    def test_failed_new_program_restores_old_program_keeps_confirmed_registration(self):
        self.started.side_effect = [reinstaller.ReinstallError('fixture new start failed'), None]
        with self.assertRaisesRegex(reinstaller.ReinstallError, '已恢复旧程序'):
            reinstaller.reinstall(self.args)
        self.assertFalse(list(self.root.glob('.node-reinstall-*')))
        for name, content in self.old_files.items():
            self.assertEqual((self.root/name).read_bytes(), content)
        config = json.loads(self.config.read_text())
        self.assertEqual((config['control_plane_url'], config['dial_host']), (self.args.panel_url, self.args.node_address))
        self.assertEqual({p.name: p.read_bytes() for p in self.state.iterdir()}, self.before_state)

    def test_failed_recovery_preserves_unfinished_stage(self):
        self.started.side_effect = reinstaller.ReinstallError('fixture cannot start')
        with self.assertRaisesRegex(reinstaller.ReinstallError, '未完成事务'):
            reinstaller.reinstall(self.args)
        stage, = self.root.glob('.node-reinstall-*')
        self.assertTrue((stage/'.switching').is_file())
        self.assertTrue((stage/'manifest.json').is_file())
        self.assertEqual({p.name: p.read_bytes() for p in self.state.iterdir()}, self.before_state)


if __name__ == '__main__':
    unittest.main()
