"""Isolated regressions for the pinned executable upgrade incident."""
import ast
import importlib.util
import os
from pathlib import Path
import subprocess
import sys
import types
import unittest
from unittest.mock import patch

SOURCE = Path(__file__).resolve().parents[2]/'deploy/update.py'
spec = importlib.util.spec_from_file_location('panel_service_launch', SOURCE)
update = importlib.util.module_from_spec(spec)
with patch.dict(sys.modules, {name: types.ModuleType(name) for name in ('fcntl','grp') if os.name == 'nt'}):
    spec.loader.exec_module(update)


class ServiceLaunchTests(unittest.TestCase):
    def result(self, executable=None, reload='no', code=0):
        executable = executable or str(update.ROOT/'current/bin/control-api')
        # Representative systemctl show output, including optional arguments.
        text = 'NeedDaemonReload='+reload+'\nExecStart={ path='+executable+' ; argv[]='+executable+' -custom-option ; ignore_errors=no ; pid=17 ; }\n'
        return types.SimpleNamespace(returncode=code, stdout=text.encode(), stderr=b'')

    def test_current_path_with_arguments_and_other_property_order_is_accepted(self):
        with patch.object(update.subprocess, 'run', return_value=self.result()) as run:
            update.check_service_launch()
        self.assertEqual(run.call_count, 1)
        self.assertNotIn('stop', run.call_args.args[0])
        self.assertEqual(run.call_args.kwargs['timeout'], 10)

    def test_pinned_wrapper_missing_multiple_commands_and_prefix_collisions_fail_closed(self):
        for executable in ('/opt/hl-panel/releases/v0.1.127/bin/control-api',
                           '/usr/local/bin/custom-wrapper', '/opt/hl-panel/current/bin/control-api-old'):
            with self.subTest(executable=executable), patch.object(update.subprocess, 'run', return_value=self.result(executable)):
                with self.assertRaisesRegex(update.UpdateError, '当前服务未停止'):
                    update.check_service_launch()
        result = self.result()
        multiple = result.stdout.rstrip()+b' { path=/other/program ; argv[]=/other/program ; }\n'
        for text in (b'NeedDaemonReload=no\nExecStart=\n', multiple):
            with self.subTest(text=text), patch.object(update.subprocess, 'run', return_value=types.SimpleNamespace(returncode=0, stdout=text)):
                with self.assertRaises(update.UpdateError): update.check_service_launch()

    def test_pending_daemon_reload_and_systemctl_failure_do_not_restart_service(self):
        for result in (self.result(reload='yes'), self.result(reload=''), self.result(code=1)):
            with self.subTest(result=result), patch.object(update.subprocess, 'run', return_value=result) as run:
                with self.assertRaisesRegex(update.UpdateError, '当前服务未停止'): update.check_service_launch()
                self.assertEqual(run.call_count, 1)
        with patch.object(update.subprocess, 'run', side_effect=subprocess.TimeoutExpired('systemctl', 10)):
            with self.assertRaisesRegex(update.UpdateError, '当前服务未停止'): update.check_service_launch()

    def test_preflight_is_before_backup_creation_and_repeated_after_candidate_check(self):
        tree = ast.parse(SOURCE.read_text(encoding='utf-8'))
        main = next(n for n in tree.body if isinstance(n, ast.FunctionDef) and n.name == 'main')
        calls = [n for n in ast.walk(main) if isinstance(n, ast.Call)]
        checks = sorted(n.lineno for n in calls if isinstance(n.func, ast.Name) and n.func.id == 'check_service_launch')
        backup = next(n.lineno for n in calls if isinstance(n.func, ast.Attribute) and n.func.attr == 'mkdir' and isinstance(n.func.value, ast.Name) and n.func.value.id == 'backup')
        candidate = next(n.lineno for n in calls if isinstance(n.func, ast.Name) and n.func.id == 'check_candidate_license')
        stop = next(n.lineno for n in calls if isinstance(n.func, ast.Name) and n.func.id == 'run' and n.args and isinstance(n.args[0], ast.List) and any(isinstance(e, ast.Constant) and e.value == 'stop' for e in n.args[0].elts))
        self.assertEqual(len(checks), 2)
        self.assertLess(checks[0], backup)
        self.assertLess(candidate, checks[1])
        self.assertLess(checks[1], stop)


class RunningBinaryTests(unittest.TestCase):
    def test_same_inode_is_accepted_and_old_release_with_same_version_is_rejected(self):
        with patch.object(update, 'run', return_value=b'123\n'), patch.object(update.Path, 'samefile', return_value=True) as same:
            update.check_running_binary()
        self.assertEqual(same.call_args.args[0], update.ROOT/'current/bin/control-api')
        with patch.object(update, 'run', return_value=b'123\n'), patch.object(update.Path, 'samefile', return_value=False):
            with self.assertRaisesRegex(update.UpdateError, '当前发行包不一致'): update.check_running_binary()

    def test_absent_pid_or_exited_process_is_rejected(self):
        for pid in (b'0\n', b'', b'-1', b'12\n13'):
            with self.subTest(pid=pid), patch.object(update, 'run', return_value=pid):
                with self.assertRaises(update.UpdateError): update.check_running_binary()
        with patch.object(update, 'run', return_value=b'123'), patch.object(update.Path, 'samefile', side_effect=FileNotFoundError):
            with self.assertRaises(update.UpdateError): update.check_running_binary()

    def test_http_version_and_active_state_alone_cannot_pass_health(self):
        with patch.object(update, 'current_info', return_value={'platform_version':'v0.1.131'}), \
             patch.object(update, 'run', return_value=b''), patch.object(update.time, 'sleep'), \
             patch.object(update, 'check_running_binary', side_effect=update.UpdateError('wrong binary')) as check:
            with self.assertRaisesRegex(update.UpdateError, '运行程序核验失败'): update.healthy('http://127.0.0.1:1', 'v0.1.131')
        self.assertEqual(check.call_count, 30)


if __name__ == '__main__': unittest.main()
