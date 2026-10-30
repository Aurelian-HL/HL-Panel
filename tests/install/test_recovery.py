"""Fault injection for rollback; all files and service operations are isolated."""
import argparse
from contextlib import ExitStack
import importlib.util
import json
import os
from pathlib import Path
import sys
import tempfile
import types
import unittest
from unittest.mock import Mock, patch


def load(name, filename):
    source = Path(__file__).resolve().parents[2] / 'deploy' / filename
    spec = importlib.util.spec_from_file_location(name, source)
    module = importlib.util.module_from_spec(spec)
    # Windows runs the filesystem/ordering regressions with POSIX ownership and
    # service calls mocked. Linux CI imports and checks the real POSIX modules.
    missing = {key: types.ModuleType(key) for key in ('fcntl', 'pwd', 'grp')
               if os.name == 'nt'}
    with patch.dict(sys.modules, missing):
        spec.loader.exec_module(module)
    return module


update = load('panel_recovery', 'update.py')
node = load('node_recovery', 'reinstall-node.py')
move = load('node_address', 'node-control-url.py')


class VersionParsingTests(unittest.TestCase):
    def test_build_suffix_compares_by_numeric_version(self):
        self.assertEqual(update.version_tuple('v0.1.95-vless-business-20261016'), (0, 1, 95))
        self.assertGreater(update.version_tuple('v0.1.96'),
                           update.version_tuple('v0.1.95-vless-business-20261016'))


class IsolatedFiles(unittest.TestCase):
    def setUp(self):
        temporary = tempfile.TemporaryDirectory()
        self.addCleanup(temporary.cleanup)
        self.root = Path(temporary.name)
        self.stack = ExitStack()
        self.addCleanup(self.stack.close)
        self.replace(os, 'chown', lambda *_: None, create=True)

    def replace(self, target, attribute, value, **kwargs):
        return self.stack.enter_context(patch.object(target, attribute, value, **kwargs))


class PanelRecoveryTests(IsolatedFiles):
    def setUp(self):
        super().setUp()
        self.programs = self.root / 'programs'
        self.old = self.programs / 'releases/v1.0.0'
        (self.old / 'bin').mkdir(parents=True)
        (self.old / 'bin/control-api').write_text('old program')
        self.backups = self.root / 'backups'
        self.backup = self.backups / 'v1.0.1-test'
        self.backup.mkdir(parents=True, mode=0o700)
        dump = self.backup / 'database.dump'
        dump.write_bytes(b'isolated database dump')
        (self.backup / 'update-state.json').write_text(json.dumps({
            'previous_target': str(self.old), 'previous_version': 'v1.0.0',
            'database_sha256': update.digest(dump)}))
        self.calls = []
        self.replace(update, 'ROOT', self.programs)
        self.replace(update, 'BACKUPS', self.backups)
        self.replace(update, 'private_regular', lambda *_: None)
        original_require = update.require
        def require(condition, message):
            if message == '备份路径必须是 root 私有的 HL-panel 备份目录':
                self.assertEqual(self.backup.parent, self.backups)
                return
            original_require(condition, message)
        self.replace(update, 'require', require)
        self.replace(update, 'configuration', lambda: ('isolated', 'http://127.0.0.1:1'))
        self.replace(update.signal, 'signal', lambda *_: None)
        for name in ('restore_subscription_nginx', 'restore_host_metrics',
                     'restore_web_update_channel', 'restore_domain_access'):
            self.replace(update, name, lambda *_: None)
        self.replace(update, 'switch', lambda target: self.calls.append(['switch', str(target)]))
        self.replace(update, 'healthy', lambda *_: self.calls.append(['healthy']))
        self.replace(update, 'run_best_effort', lambda args: self.calls.append(args))

    def command(self, args, **kwargs):
        self.calls.append(args)
        if args[0] == 'pg_restore':
            return b'1; 1 1 TABLE public hl_panel_audit_journal postgres\n'
        return b''

    def test_success_starts_only_after_restore_and_switch(self):
        self.replace(update, 'run', self.command)
        update.rollback(self.backup)
        restore = next(args for args in self.calls if 'pg_restore' in args and args[0] == 'runuser')
        self.assertIn('--single-transaction', restore)
        start = ['systemctl', 'start', update.SERVICE]
        self.assertLess(self.calls.index(restore), self.calls.index(['switch', str(self.old)]))
        self.assertLess(self.calls.index(['switch', str(self.old)]), self.calls.index(start))
        self.assertEqual(self.calls.count(start), 1)

    def test_failed_restore_never_starts_inconsistent_program(self):
        for phase in ('nginx', 'units', 'database', 'switch', 'start', 'health'):
            with self.subTest(phase=phase), ExitStack() as patches:
                self.calls.clear()
                def fail(*_args, **_kwargs):
                    raise update.UpdateError('isolated ' + phase + ' failure')
                def command(args, **kwargs):
                    if phase == 'database' and 'pg_restore' in args:
                        fail()
                    if phase == 'start' and args == ['systemctl', 'start', update.SERVICE]:
                        self.calls.append(args)
                        fail()
                    return self.command(args, **kwargs)
                patches.enter_context(patch.object(update, 'run', command))
                if phase == 'nginx':
                    patches.enter_context(patch.object(update, 'restore_subscription_nginx', fail))
                if phase == 'units':
                    patches.enter_context(patch.object(update, 'restore_web_update_channel', fail))
                if phase == 'switch':
                    patches.enter_context(patch.object(update, 'switch', fail))
                if phase == 'health':
                    patches.enter_context(patch.object(update, 'healthy', fail))
                with self.assertRaises(update.UpdateError):
                    update.rollback(self.backup)
                starts = self.calls.count(['systemctl', 'start', update.SERVICE])
                self.assertEqual(starts, 1 if phase in ('start', 'health') else 0)
                self.assertEqual(self.calls[-1], ['systemctl', 'stop', update.SERVICE])

    def test_rollback_removes_only_objects_absent_from_backup(self):
        self.replace(update, 'run', self.command)
        manifest = '1; 1 1 TABLE public hl_panel_audit_journal hl_panel_app\n'
        update.remove_upgrade_only_usage_objects('isolated', manifest)
        sql = self.calls[-1][-1]
        self.assertNotIn('DROP TABLE IF EXISTS public.hl_panel_audit_journal', sql)
        self.assertIn('DROP TABLE IF EXISTS public.hl_panel_rule_traffic', sql)
        self.assertIn('DROP TABLE IF EXISTS public.usage_rule_totals', sql)
        self.assertIn('DROP FUNCTION IF EXISTS public.usage_rule_totals_added()', sql)
        self.assertIn('DROP TRIGGER IF EXISTS usage_rule_totals_insert ON public.usage_events', sql)
        self.assertIn('DROP TABLE IF EXISTS public.hl_panel_protocol_health', sql)
        self.assertIn('DROP TABLE IF EXISTS public.usage_query_totals', sql)
        for suffix in ('added', 'removed', 'cleared'):
            self.assertIn('DROP FUNCTION IF EXISTS public.usage_query_totals_'+suffix+'()', sql)
        for suffix in ('insert', 'delete', 'update_1_remove', 'update_2_add', 'truncate'):
            self.assertIn('DROP TRIGGER IF EXISTS usage_query_totals_'+suffix+' ON public.usage_events', sql)
        self.assertTrue(sql.startswith('BEGIN; ') and sql.endswith('; COMMIT;'))
        self.calls.clear()
        manifest += '2; 1 2 TABLE public hl_panel_rule_traffic hl_panel_app\n3; 1 3 TABLE public usage_rule_totals hl_panel_app\n'
        update.remove_upgrade_only_usage_objects('isolated', manifest)
        sql = self.calls[-1][-1]
        self.assertNotIn('DROP TABLE IF EXISTS public.usage_rule_totals', sql)
        self.assertNotIn('DROP FUNCTION IF EXISTS public.usage_rule_totals_added()', sql)
        self.assertIn('DROP TABLE IF EXISTS public.usage_query_totals', sql)
        self.assertIn('DROP TABLE IF EXISTS public.hl_panel_protocol_health', sql)
        self.calls.clear()
        manifest += '4; 1 4 TABLE public hl_panel_protocol_health hl_panel_app\n5; 1 5 TABLE public usage_query_totals hl_panel_app\n'
        update.remove_upgrade_only_usage_objects('isolated', manifest)
        self.assertEqual(self.calls, [], 'existing usage projections must survive rollback')


class NodeRecoveryTests(IsolatedFiles):
    def setUp(self):
        super().setUp()
        self.programs = self.root / 'programs'
        (self.programs / 'node-engines').mkdir(parents=True)
        self.config = self.root / 'config/edge-agent.json'
        self.config.parent.mkdir()
        self.ca = self.config.parent / 'edge-agent-ca.pem'
        self.state = self.root / 'state'
        self.state.mkdir(mode=0o700)
        self.unit = self.root / 'units/edge-agent.service'
        self.unit.parent.mkdir()
        self.old_config = {'control_plane_url': 'https://old.example.com',
            'dial_host': '192.0.2.1', 'ca_file': str(self.ca),
            'data_dir': str(self.state), 'engine_mode': 'mixed',
            'xray_binary_path': str(self.programs / 'node-engines/xray'),
            'gost_binary_path': str(self.programs / 'node-engines/gost')}
        self.config.write_text(json.dumps(self.old_config))
        self.config.chmod(0o640)
        self.ca.write_text('old ca'); self.ca.chmod(0o640)
        self.identity = self.state / 'credentials.json'
        self.identity.write_text('{"node_id":"isolated","node_credential":"isolated-secret"}')
        self.identity.chmod(0o600)
        (self.state / 'usage.json').write_text('usage cursor')
        self.before = {path.name: path.read_bytes() for path in self.state.iterdir()}
        self.release = self.root / 'release'
        for relative in ('bin/edge-agent', 'bin/xray', 'bin/gost', 'bin/geoip.dat', 'bin/geosite.dat',
                         'deploy/edge-agent/enroll.sh', 'deploy/reinstall-node.py',
                         'deploy/install-node-update.py',
                         'deploy/systemd/hl-panel-edge-agent.service'):
            path = self.release / relative
            path.parent.mkdir(parents=True, exist_ok=True)
            path.write_text('new ' + relative)
        for relative in ('edge-agent', 'node-engines/xray', 'node-engines/gost',
                         'enroll-node.sh', 'reinstall-node.py'):
            (self.programs / relative).write_text('old ' + relative)
        self.unit.write_text('old service unit')
        for key, path in (('ROOT', self.programs), ('CONFIG', self.config),
                          ('STATE', self.state), ('UNIT', self.unit), ('CA_FILE', self.ca)):
            self.replace(node, key, path)
        self.replace(node.pwd, 'getpwnam', lambda _: types.SimpleNamespace(pw_uid=0, pw_gid=0), create=True)
        # Ownership is an OS boundary; these tests target connection restoration.
        self.replace(node, 'private_file', lambda *_args, **_kwargs: None)
        original_require = node.require
        def require(condition, message):
            if os.name == 'nt' and message in ('节点状态目录不安全', '节点程序目录不安全',
                                               '临时目录权限不安全', '节点配置所有者或权限无效'):
                return
            original_require(condition, message)
        self.replace(node, 'require', require)
        def command(args):
            if args == [str(self.release / 'bin/xray'), 'version']:
                return b'Xray 26.7.28 (isolated candidate)\n'
            return b''
        self.replace(node, 'run', command)
        self.replace(node, 'request_reenrollment', lambda *_: None)
        self.context = self.replace(node.ssl, 'create_default_context', Mock())
        self.stack.enter_context(patch.dict(os.environ, {'HL_INSTALL_ENROLLMENT_TOKEN': 'isolated-token'}))

    def test_incompatible_core_keeps_service_registration_and_files_untouched(self):
        self.replace(node, 'run', lambda *_: b'Xray 26.10.10 (isolated incompatible candidate)\n')
        registration = self.replace(node, 'request_reenrollment', Mock())
        start = self.replace(node, 'start_service', Mock())
        args = argparse.Namespace(release=self.release, version='v1.0.1',
            panel_url='https://new.example.com', node_address='192.0.2.2', ca_file='')
        before = {str(p): p.read_bytes() for p in self.root.rglob('*') if p.is_file()}
        with self.assertRaisesRegex(node.ReinstallError, '26.7.28'):
            node.reinstall(args)
        registration.assert_not_called()
        start.assert_not_called()
        self.assertEqual({str(p): p.read_bytes() for p in self.root.rglob('*') if p.is_file()}, before)

    def test_failed_candidate_keeps_confirmed_address_ca_and_identity(self):
        new_ca = self.root / 'new-ca.pem'
        new_ca.write_text('new ca')
        attempts = []
        def start():
            attempts.append(1)
            if len(attempts) == 1:
                raise node.ReinstallError('isolated candidate failure')
        self.replace(node, 'start_service', start)
        args = argparse.Namespace(release=self.release, version='v1.0.1',
            panel_url='https://new.example.com', node_address='192.0.2.2', ca_file=str(new_ca))
        with self.assertRaises(node.ReinstallError):
            node.reinstall(args)
        current = json.loads(self.config.read_text())
        self.assertEqual(current['control_plane_url'], args.panel_url)
        self.assertEqual(current['dial_host'], args.node_address)
        self.assertEqual(self.ca.read_text(), 'new ca')
        self.assertEqual((self.programs / 'edge-agent').read_text(), 'old edge-agent')
        self.assertEqual({path.name: path.read_bytes() for path in self.state.iterdir()}, self.before)
        self.assertEqual(len(attempts), 2)

    def test_public_domain_reinstall_uses_system_trust_and_cleans_transaction(self):
        self.replace(node, 'start_service', lambda: None)
        args = argparse.Namespace(release=self.release, version='v1.0.1',
            panel_url='https://new.example.com', node_address='192.0.2.2', ca_file='')
        node.reinstall(args)
        self.context.assert_called_once_with(cafile=None)
        current = json.loads(self.config.read_text())
        self.assertEqual(current['ca_file'], '')
        self.assertFalse(list(self.programs.glob('.node-reinstall-*')))
        self.assertEqual({path.name: path.read_bytes() for path in self.state.iterdir()}, self.before)

    def test_unfinished_transaction_restore_keeps_latest_confirmed_connection(self):
        self.replace(node, 'start_service', lambda: None)
        args = argparse.Namespace(release=self.release, version='v1.0.1',
            panel_url='https://new.example.com', node_address='192.0.2.2', ca_file='')
        stage = Path(tempfile.mkdtemp(prefix='.node-reinstall-', dir=self.programs))
        node.reinstall_staged(args, stage)
        current = json.loads(self.config.read_text())
        current.update(control_plane_url='https://latest.example.com', dial_host='192.0.2.3')
        self.config.write_text(json.dumps(current))
        node.restore_programs(stage)
        self.assertEqual(json.loads(self.config.read_text()), current)
        self.assertEqual((self.programs / 'edge-agent').read_text(), 'old edge-agent')
        self.assertEqual({path.name: path.read_bytes() for path in self.state.iterdir()}, self.before)

    def test_write_failure_retries_confirmed_connection_before_old_program_start(self):
        self.replace(node, 'start_service', lambda: None)
        install = node.atomic_install
        failed = False
        def fail_once(source, destination, *args, **kwargs):
            nonlocal failed
            if destination == self.config and not failed:
                failed = True
                raise OSError('isolated config write failure')
            return install(source, destination, *args, **kwargs)
        self.replace(node, 'atomic_install', fail_once)
        args = argparse.Namespace(release=self.release, version='v1.0.1',
            panel_url='https://new.example.com', node_address='192.0.2.2', ca_file='')
        with self.assertRaises(node.ReinstallError):
            node.reinstall(args)
        current = json.loads(self.config.read_text())
        self.assertEqual(current['control_plane_url'], args.panel_url)
        self.assertEqual(current['dial_host'], args.node_address)
        self.assertEqual(current['ca_file'], '')
        self.assertEqual({path.name: path.read_bytes() for path in self.state.iterdir()}, self.before)

    def test_rejected_registration_keeps_original_connection_and_programs(self):
        def reject(*_):
            raise node.ReinstallError('isolated registration rejection')
        self.replace(node, 'request_reenrollment', reject)
        start = self.replace(node, 'start_service', Mock())
        args = argparse.Namespace(release=self.release, version='v1.0.1',
            panel_url='https://new.example.com', node_address='192.0.2.2', ca_file='')
        with self.assertRaises(node.ReinstallError):
            node.reinstall(args)
        start.assert_not_called()
        self.assertEqual(json.loads(self.config.read_text()), self.old_config)
        self.assertEqual(self.ca.read_text(), 'old ca')
        self.assertEqual((self.programs / 'edge-agent').read_text(), 'old edge-agent')
        self.assertEqual({path.name: path.read_bytes() for path in self.state.iterdir()}, self.before)


class NodeAddressLockTests(IsolatedFiles):
    def setUp(self):
        super().setUp()
        self.programs = self.root / 'programs'
        self.programs.mkdir(mode=0o755)
        self.replace(move, 'ROOT', self.programs)
        self.replace(os, 'geteuid', lambda: 0, create=True)
        self.move_call = self.replace(move, 'move', Mock())
        self.stack.enter_context(patch.object(sys, 'argv', ['node-control-url.py', '--panel-url', 'https://panel.example.com']))
        if os.name == 'nt':
            self.replace(os, 'O_NOFOLLOW', 0, create=True)
            original_require = move.require
            def require(condition, message):
                if message != '节点程序目录不安全':
                    original_require(condition, message)
            self.replace(move, 'require', require)
        self.replace(move.fcntl, 'LOCK_EX', 2, create=True)
        self.replace(move.fcntl, 'LOCK_NB', 4, create=True)

    def test_address_migration_uses_same_lock_as_reinstallation(self):
        flock = self.replace(move.fcntl, 'flock', Mock(), create=True)
        move.main()
        self.assertTrue((self.programs / '.node-update.lock').is_file())
        flock.assert_called_once()
        self.move_call.assert_called_once_with('https://panel.example.com', '')

    def test_busy_update_lock_rejects_migration_before_connection_changes(self):
        self.replace(move.fcntl, 'flock', Mock(side_effect=BlockingIOError), create=True)
        with self.assertRaises(move.MoveError):
            move.main()
        self.move_call.assert_not_called()

    @unittest.skipIf(os.name == 'nt', 'Real flock requires Linux CI')
    def test_real_update_lock_blocks_address_migration(self):
        with (self.programs / '.node-update.lock').open('a') as lock:
            move.fcntl.flock(lock.fileno(), move.fcntl.LOCK_EX | move.fcntl.LOCK_NB)
            with self.assertRaises(move.MoveError):
                move.main()
        self.move_call.assert_not_called()


class RecoveryPermissionTests(unittest.TestCase):
    def test_node_service_readable_configuration_is_accepted(self):
        for mode in (0o640, 0o660, 0o644):
            with self.subTest(mode=oct(mode)):
                path = Mock()
                path.is_file.return_value = True
                path.is_symlink.return_value = False
                path.stat.return_value = types.SimpleNamespace(st_uid=0, st_mode=mode)
                if mode == 0o640:
                    node.private_file(path, mask=0o027)
                else:
                    with self.assertRaises(node.ReinstallError):
                        node.private_file(path, mask=0o027)

    def test_unit_restore_errors_are_not_silently_ignored(self):
        for operation in ('unmask', 'enable', 'start'):
            with self.subTest(operation=operation):
                def command(args):
                    if args[1] == operation:
                        raise update.UpdateError('isolated systemd rejection')
                with patch.object(update, 'run', command), self.assertRaises(update.UpdateError):
                    update.restore_systemd_unit_state('hl-panel-update.socket', 'enabled', True)


class WebWorkerRecoveryTests(IsolatedFiles):
    def setUp(self):
        super().setUp()
        self.units = self.root / 'units'
        self.units.mkdir()
        self.backup = self.root / 'backup'
        self.backup.mkdir()
        self.changes = []
        for name in ('hl-panel-update.socket', 'hl-panel-update.service'):
            (self.units / name).write_text('new unit')
            (self.backup / name).write_text('old unit')
            self.changes.append({'name': name, 'added': False, 'active': True, 'enabled': 'enabled'})
        (self.backup / 'web-update-units.json').write_text(json.dumps(self.changes))
        self.calls = []
        self.replace(update, 'SYSTEMD_UNITS', self.units)
        self.replace(update, 'private_regular', lambda *_: None)
        self.replace(update, 'run', lambda args: self.calls.append(args))

    def test_web_rollback_does_not_stop_its_own_cgroup_or_socket(self):
        self.replace(update, 'inside_update_worker', lambda: True)
        update.restore_web_update_channel(self.backup)
        self.assertIn(['systemctl', 'daemon-reload'], self.calls)
        self.assertFalse(any('--now' in args or args[1] in ('stop', 'start', 'restart') for args in self.calls))
        for item in self.changes:
            self.assertEqual((self.units / item['name']).read_text(), 'old unit')

    def test_cli_rollback_restores_recorded_runtime_state(self):
        self.replace(update, 'inside_update_worker', lambda: False)
        update.restore_web_update_channel(self.backup)
        self.assertIn(['systemctl', 'stop', 'hl-panel-update.service'], self.calls)
        self.assertIn(['systemctl', 'start', 'hl-panel-update.service'], self.calls)
        self.assertIn(['systemctl', 'start', 'hl-panel-update.socket'], self.calls)

    def prepare_release(self, changed=()):
        release = self.root / 'release'
        directory = release / 'deploy/systemd'
        directory.mkdir(parents=True)
        (self.backup / 'web-update-units.json').unlink()
        for item in self.changes:
            name = item['name']
            content = '# Managed by HL-panel: old ' + name + '\n'
            (self.units / name).write_text(content)
            (directory / name).write_text(content + ('# new\n' if name in changed else ''))
        self.replace(update, 'systemd_enabled_state', lambda _: 'enabled')
        self.replace(update, 'systemd_active_state', lambda _: True)
        self.replace(update, 'inside_update_worker', lambda: False)
        return release

    def test_service_only_update_restores_unchanged_socket_runtime(self):
        release = self.prepare_release(changed=('hl-panel-update.service',))
        update.web_update_channel(release, self.backup)
        update.restore_web_update_channel(self.backup)
        self.assertIn(['systemctl', 'start', 'hl-panel-update.socket'], self.calls)
        self.assertIn(['systemctl', 'start', 'hl-panel-update.service'], self.calls)

    def test_unchanged_units_still_record_state_before_update_activates_socket(self):
        release = self.prepare_release()
        update.web_update_channel(release, self.backup)
        self.assertTrue((self.backup / 'web-update-units.json').is_file())
        update.restore_web_update_channel(self.backup)
        self.assertIn(['systemctl', 'start', 'hl-panel-update.socket'], self.calls)

    def test_second_backup_failure_never_changes_live_units(self):
        release = self.prepare_release(changed=tuple(item['name'] for item in self.changes))
        previous = {path.name: path.read_bytes() for path in self.units.iterdir()}
        copy = update.shutil.copyfile
        def fail(source, destination):
            if Path(destination) == self.backup / 'hl-panel-update.service':
                raise OSError('isolated second backup failure')
            return copy(source, destination)
        with patch.object(update.shutil, 'copyfile', fail), self.assertRaises(OSError):
            update.web_update_channel(release, self.backup)
        self.assertEqual({path.name: path.read_bytes() for path in self.units.iterdir()}, previous)
        self.assertFalse((self.backup / 'web-update-units.json').exists())

    def test_partial_new_unit_write_can_roll_back_without_stopping_absent_unit(self):
        release = self.prepare_release()
        for path in self.units.iterdir():
            path.unlink()
        copy = update.shutil.copyfile
        def fail(source, destination):
            if Path(source).name == 'hl-panel-update.service':
                raise OSError('isolated service write failure')
            return copy(source, destination)
        with patch.object(update.shutil, 'copyfile', fail), self.assertRaises(OSError):
            update.web_update_channel(release, self.backup)
        update.restore_web_update_channel(self.backup)
        self.assertNotIn(['systemctl', 'stop', 'hl-panel-update.service'], self.calls)
        self.assertEqual(list(self.units.iterdir()), [])

    def test_detects_worker_on_cgroup_v1_and_v2(self):
        for content, expected in (('0::/system.slice/hl-panel-update.service\n', True),
                                  ('1:name=systemd:/system.slice/hl-panel-update.service\n', True),
                                  ('0::/user.slice/unrelated.service\n', False)):
            with self.subTest(content=content), patch.object(Path, 'read_text', return_value=content):
                self.assertEqual(update.inside_update_worker(), expected)


if __name__ == '__main__':
    unittest.main()
