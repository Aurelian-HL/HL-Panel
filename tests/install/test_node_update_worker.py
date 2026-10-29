"""Fixed-purpose worker and maintenance rollback, on isolated temporary paths."""
import importlib.util
import contextlib
import io
import json
import os
from pathlib import Path
import tempfile
import re
import struct
import subprocess
import sys
import time
import types
import unittest
from unittest.mock import patch
from unittest.mock import Mock

DEPLOY=Path(__file__).resolve().parents[2]/'deploy'
def module(name,file):
    spec=importlib.util.spec_from_file_location(name,DEPLOY/file)
    value=importlib.util.module_from_spec(spec);spec.loader.exec_module(value);return value
worker=module('node_worker','node-update-worker.py')
installer=module('node_maintenance','install-node-update.py')

class Worker(unittest.TestCase):
    @unittest.skipUnless(getattr(os,'geteuid',lambda:-1)()==0,'root-owned receipts')
    def test_same_version_requires_completed_installation_receipt(self):
        with tempfile.TemporaryDirectory() as tmp, patch.object(worker,'STATE',Path(tmp)):
            task={'id':'aaaaaaaa-aaaa-4aaa-aaaa-aaaaaaaaaaaa','backup_directory':'/var/backups/hl-panel-node/update-v0.1.123-test'}
            self.assertFalse(worker.installation_completed(task))
            job=Path(tmp)/task['id'];job.mkdir();report=job/'result.json'
            report.write_text(json.dumps({'state':'uncertain','backup_directory':task['backup_directory']}));report.chmod(0o600)
            self.assertFalse(worker.installation_completed(task))
            report.write_text(json.dumps({'state':'verifying','backup_directory':task['backup_directory']}))
            self.assertTrue(worker.installation_completed(task))
    def test_only_fixed_values_are_accepted(self):
        good={'id':'aaaaaaaa-aaaa-4aaa-aaaa-aaaaaaaaaaaa','version':'v0.1.123','archive_sha256':'a'*64}
        self.assertTrue(worker.valid_request(good))
        for field,value in [('command','id'),('version','latest'),('version','../../x'),('archive_sha256','x')]:
            bad=dict(good);bad[field]=value
            self.assertFalse(worker.valid_request(bad))
    def test_job_creation_failure_stays_locked(self):
        with tempfile.TemporaryDirectory() as tmp, patch.object(worker,'STATE',Path(tmp)):
            task={'id':'aaaaaaaa-aaaa-4aaa-aaaa-aaaaaaaaaaaa','version':'v0.1.123','archive_sha256':'a'*64,'state':'running'}
            (Path(tmp)/task['id']).mkdir()
            worker.save(task);worker.execute(task)
            self.assertEqual(worker.load()['state'],'uncertain')
            self.assertFalse(worker.EXECUTING)
    def test_failed_child_cannot_claim_success(self):
        with tempfile.TemporaryDirectory() as tmp, patch.object(worker,'STATE',Path(tmp)), patch.object(worker.subprocess,'run',return_value=types.SimpleNamespace(returncode=1)):
            task={'id':'aaaaaaaa-aaaa-4aaa-aaaa-aaaaaaaaaaaa','version':'v0.1.123','archive_sha256':'a'*64,'state':'running'}
            worker.save(task);worker.execute(task)
            self.assertEqual(worker.load()['state'],'uncertain')

@unittest.skipUnless(getattr(os,'geteuid',lambda:-1)()==0,'root-owned maintenance files')
class Maintenance(unittest.TestCase):
    def release(self, root):
        release=root/'release';(release/'deploy/systemd').mkdir(parents=True)
        (root/'program/node-engines').mkdir(exist_ok=True)
        (release/'bin').mkdir()
        for name in installer.ASSET_NAMES:
            (release/'bin'/name).write_text('packaged resource')
        (release/'bin/xray').write_text('fixed core fixture')
        for name in installer.NAMES:(release/'deploy'/name).write_text('new')
        for name in installer.UNIT_NAMES:(release/'deploy/systemd'/name).write_text('new unit')
        (release/'deploy/configure-node-traffic-control.py').write_text('# isolated helper fixture')
        return release

    def test_restores_files_and_absence_after_failure(self):
        with tempfile.TemporaryDirectory() as tmp:
            root=Path(tmp);program=root/'program';units=root/'units';program.mkdir();units.mkdir()
            (program/'update.py').write_text('old verifier');(program/'update.py').chmod(0o700)
            release=self.release(root)
            def run(args,**kwargs):
                if '--service-defaults' in args:
                    dropin=units/'hl-panel-edge-agent.service.d'/installer.TC_NAME
                    dropin.parent.mkdir();dropin.write_text('new TC defaults')
                if args[:3]==['systemctl','enable','--now']:raise OSError('failure after files replaced')
                return types.SimpleNamespace(returncode=1 if 'is-active' in args or 'is-enabled' in args else 0, stdout=b'Xray 26.7.28 (fixture)')
            with patch.object(installer,'ROOT',program),patch.object(installer,'UNITS',units),patch.object(installer.subprocess,'run',side_effect=run):
                with self.assertRaises(OSError):installer.install(release)
                self.assertEqual((program/'update.py').read_text(),'old verifier')
                self.assertEqual((program/'update.py').stat().st_mode & 0o777,0o700)
                self.assertFalse((program/'node-update-worker.py').exists())
                self.assertEqual(list(units.iterdir()),[])

    def test_successful_install_snapshot_restores_original_dropin_and_mode(self):
        with tempfile.TemporaryDirectory() as tmp:
            root=Path(tmp);program=root/'program';units=root/'units';program.mkdir();units.mkdir()
            dropin=units/'hl-panel-edge-agent.service.d'/installer.TC_NAME
            dropin.parent.mkdir();dropin.write_text('previous managed defaults');dropin.chmod(0o600)
            sibling=dropin.parent/'10-custom.conf';sibling.write_text('custom operator settings')
            release=self.release(root)
            def run(args,**kwargs):
                if '--service-defaults' in args:
                    dropin.write_text('new TC defaults');dropin.chmod(0o644)
                return types.SimpleNamespace(returncode=0, stdout=b'Xray 26.7.28 (fixture)')
            with patch.object(installer,'ROOT',program),patch.object(installer,'UNITS',units),patch.object(installer.subprocess,'run',side_effect=run):
                backup=root/'backup';installer.snapshot(backup)
                installer.install(release)
                self.assertEqual(dropin.read_text(),'new TC defaults')
                installer.restore(backup)
                self.assertEqual(dropin.read_text(),'previous managed defaults')
                self.assertEqual(dropin.stat().st_mode & 0o777,0o600)
                self.assertEqual(sibling.read_text(),'custom operator settings')
                self.assertFalse((program/'node-update-worker.py').exists())

    def test_helper_failure_restores_custom_override_and_other_files(self):
        with tempfile.TemporaryDirectory() as tmp:
            root=Path(tmp);program=root/'program';units=root/'units';program.mkdir();units.mkdir()
            dropin=units/'hl-panel-edge-agent.service.d'/installer.TC_NAME
            dropin.parent.mkdir();dropin.write_text('custom operator override')
            release=self.release(root)
            def run(args,**kwargs):
                if '--service-defaults' in args:raise OSError('conflicting custom drop-in')
                return types.SimpleNamespace(returncode=1 if 'is-active' in args or 'is-enabled' in args else 0, stdout=b'Xray 26.7.28 (fixture)')
            with patch.object(installer,'ROOT',program),patch.object(installer,'UNITS',units),patch.object(installer.subprocess,'run',side_effect=run):
                with self.assertRaises(OSError):installer.install(release)
                self.assertEqual(dropin.read_text(),'custom operator override')
                self.assertEqual(list(program.iterdir()),[program/'node-engines'])
                self.assertEqual(list((program/'node-engines').iterdir()),[])
                self.assertEqual(list(units.iterdir()),[dropin.parent])


@unittest.skipUnless(getattr(os, 'geteuid', lambda: -1)() == 0, 'root-owned cleanup paths')
class CompletedTransactions(unittest.TestCase):
    def setUp(self):
        self.stack = contextlib.ExitStack()
        self.addCleanup(self.stack.close)
        self.root = Path(self.stack.enter_context(tempfile.TemporaryDirectory()))
        self.program = self.root/'program'
        self.program.mkdir()
        self.state = self.root/'state'
        self.state.mkdir()
        self.stage = self.program/'.node-update-v0.1.999-fixture'
        self.stage.mkdir(mode=0o700)
        (self.stage/'program').write_text('saved program')
        self.stack.enter_context(patch.object(worker, 'ROOT', self.program))
        self.stack.enter_context(patch.object(worker, 'STATE', self.state))
        self.stack.enter_context(patch.object(worker, 'BACKUP', re.compile(re.escape(str(self.stage)))))
        self.task = {'id': 'aaaaaaaa-aaaa-4aaa-aaaa-aaaaaaaaaaaa', 'state': 'verifying',
                     'phase': 'verify', 'backup_directory': str(self.stage)}

    def test_success_and_recovery_remove_stage_and_keep_terminal_receipt(self):
        for state in ('succeeded', 'rolled_back'):
            if not self.stage.exists():
                self.stage.mkdir(mode=0o700)
            self.task['backup_directory'] = str(self.stage)
            worker.finish(self.task, state, 'verified')
            self.assertFalse(self.stage.exists())
            self.assertEqual(worker.load()['state'], state)
            self.assertEqual(worker.load()['backup_directory'], '')

    def test_uncertain_never_removes_stage(self):
        worker.finish(self.task, 'uncertain', 'interrupted')
        self.assertTrue(self.stage.exists())
        self.assertEqual(worker.load()['backup_directory'], str(self.stage))

    def test_cleanup_failure_cannot_change_success(self):
        with patch.object(worker.shutil, 'rmtree', side_effect=OSError('fixture disk busy')), contextlib.redirect_stderr(io.StringIO()):
            worker.finish(self.task, 'succeeded', 'verified')
        self.assertTrue(self.stage.exists())
        self.assertEqual(worker.load()['state'], 'succeeded')
        worker.cleanup_completed(worker.load())
        self.assertFalse(self.stage.exists())

    def test_symlink_or_shared_directory_is_not_deleted(self):
        self.stage.chmod(0o755)
        with contextlib.redirect_stderr(io.StringIO()):
            worker.finish(self.task, 'succeeded', 'verified')
        self.assertTrue((self.stage/'program').exists())
        self.stage.chmod(0o700)
        moved = self.program/'retained'
        self.stage.rename(moved)
        self.stage.symlink_to(moved, target_is_directory=True)
        with contextlib.redirect_stderr(io.StringIO()):
            worker.finish(self.task, 'succeeded', 'verified')
        self.assertTrue((moved/'program').exists())

    def test_active_updater_lock_defers_cleanup(self):
        import fcntl
        with (self.program/'.node-update.lock').open('w') as lock:
            fcntl.flock(lock, fcntl.LOCK_EX|fcntl.LOCK_NB)
            with contextlib.redirect_stderr(io.StringIO()):
                worker.finish(self.task, 'succeeded', 'verified')
            self.assertTrue(self.stage.exists())
        worker.cleanup_completed(worker.load())
        self.assertFalse(self.stage.exists())

    def test_verified_retry_is_idempotent_after_cleanup(self):
        worker.finish(self.task, 'succeeded', 'verified')
        handler = worker.Handler.__new__(worker.Handler)
        handler.connection = Mock()
        handler.connection.getsockopt.return_value = struct.pack('3i', 1, 0, 0)
        handler.command, handler.path = 'POST', '/verified'
        handler.respond = Mock()
        with patch.object(worker.subprocess, 'run') as command:
            handler.handle_request({'id': self.task['id']})
        self.assertEqual(handler.respond.call_args.args[0], 200)
        command.assert_not_called()

    def test_status_retries_cleanup_after_updater_releases_lock(self):
        import fcntl
        with (self.program/'.node-update.lock').open('w') as lock:
            fcntl.flock(lock, fcntl.LOCK_EX|fcntl.LOCK_NB)
            with contextlib.redirect_stderr(io.StringIO()):
                worker.finish(self.task, 'succeeded', 'verified')
        handler = worker.Handler.__new__(worker.Handler)
        handler.connection = Mock()
        handler.connection.getsockopt.return_value = struct.pack('3i', 1, 0, 0)
        handler.command, handler.path = 'GET', '/status'
        handler.respond = Mock()
        handler.handle_request(None)
        self.assertEqual(handler.respond.call_args.args[0], 200)
        self.assertFalse(self.stage.exists())
        self.assertEqual(worker.load()['backup_directory'], '')


@unittest.skipUnless(getattr(os, 'geteuid', lambda: -1)() == 0, 'root-owned legacy fixtures')
class LegacyBridge(unittest.TestCase):
    def test_real_legacy_parent_and_finalizer_obey_update_lock(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            program = root/'program'
            program.mkdir()
            legacy = root/'old-archives'
            legacy.mkdir(mode=0o755)
            stage = legacy/'update-v0.1.999-fixture'
            stage.mkdir(mode=0o700)
            release = stage/'release'
            (release/'deploy').mkdir(parents=True)
            state = root/'tasks'
            state.mkdir()
            task = 'aaaaaaaa-aaaa-4aaa-aaaa-aaaaaaaaaaaa'
            (state/'task.json').write_text(json.dumps({'id': task, 'state': 'succeeded',
                'backup_directory': str(stage)}))
            (state/'task.json').chmod(0o600)
            # This shim only redirects managed paths into this disposable fixture;
            # parent detection, flock, the subprocess and finalizer are real.
            load = ('import importlib.util,sys,os\nfrom pathlib import Path\n'
                    f'sys.path.insert(0, {str(DEPLOY)!r})\n')
            finalizer = release/'deploy/update-node.py'
            finalizer.write_text(load +
                f'spec=importlib.util.spec_from_file_location("fixture_updater", {str(DEPLOY/"update-node.py")!r})\n'
                'module=importlib.util.module_from_spec(spec);spec.loader.exec_module(module)\n' +
                f'module.ROOT=Path({str(program)!r});module.LEGACY_STAGING=Path({str(legacy)!r});module.UPDATE_STATE=Path({str(state)!r})\n' +
                f'Path({str(root/"finalizer.pid")!r}).write_text(str(os.getpid()))\n' +
                'module.main()\n' +
                f'Path({str(root/"finished")!r}).touch()\n')
            bridge = release/'deploy/install-node-update.py'
            bridge.write_text(load +
                f'spec=importlib.util.spec_from_file_location("fixture_installer", {str(DEPLOY/"install-node-update.py")!r})\n'
                'module=importlib.util.module_from_spec(spec);spec.loader.exec_module(module)\n' +
                f'module.ROOT=Path({str(program)!r});module.LEGACY_STAGING=Path({str(legacy)!r});module.UPDATE_STATE=Path({str(state)!r})\n' +
                f'module.bridge_legacy_updater(Path({str(release)!r}))\n')
            parent_script = program/'update-node.py'
            parent_script.write_text('import fcntl,subprocess,sys,time\nfrom pathlib import Path\n' +
                f'with Path({str(program/".node-update.lock")!r}).open("w") as lock:\n'
                ' fcntl.flock(lock,fcntl.LOCK_EX)\n' +
                f' subprocess.run([sys.executable,{str(bridge)!r}],check=True)\n' +
                f' while not Path({str(root/"release-lock")!r}).exists(): time.sleep(0.01)\n')
            parent = subprocess.Popen([sys.executable, str(parent_script), '--result-file',
                str(state/task/'result.json')], stdout=subprocess.DEVNULL, stderr=subprocess.PIPE)
            def wait_for(path):
                deadline = time.monotonic()+8
                while not path.exists() and time.monotonic() < deadline:
                    time.sleep(0.02)
                self.assertTrue(path.exists(), str(path))
            try:
                wait_for(root/'finalizer.pid')
                # Even terminal state cannot delete a release the old process uses.
                time.sleep(0.1)
                self.assertTrue(stage.is_dir())
                (root/'release-lock').touch()
                _, error = parent.communicate(timeout=8)
                self.assertEqual(parent.returncode, 0, error.decode())
                wait_for(root/'finished')
                self.assertFalse(stage.exists())
                self.assertFalse(legacy.exists())
            finally:
                if parent.poll() is None:
                    parent.kill()
                    parent.communicate(timeout=5)
                if not (root/'finished').exists() and (root/'finalizer.pid').exists():
                    try:
                        os.kill(int((root/'finalizer.pid').read_text()), 15)
                    except ProcessLookupError:
                        pass

    def test_first_old_updater_starts_finalizer_for_its_own_task(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            legacy = root/'old-archives'
            stage = legacy/'update-v0.1.999-fixture'
            release = stage/'release'
            legacy.mkdir(mode=0o755)
            stage.mkdir(mode=0o700)
            release.mkdir()
            task = 'aaaaaaaa-aaaa-4aaa-aaaa-aaaaaaaaaaaa'
            state = root/'tasks'
            parent = ('python3\0/opt/hl-panel/update-node.py\0--version\0v0.1.999\0'
                      '--result-file\0'+str(state/task/'result.json')+'\0').encode()
            with patch.object(installer, 'LEGACY_STAGING', legacy), patch.object(installer, 'UPDATE_STATE', state), patch.object(Path, 'read_bytes', return_value=parent), patch.object(installer.subprocess, 'Popen') as spawn:
                installer.bridge_legacy_updater(release)
                self.assertIn(task, spawn.call_args.args[0])
                self.assertIn(str(stage), spawn.call_args.args[0])
                # New updater already started its own finalizer before switching.
                spawn.reset_mock()
                (stage/'.switching').touch()
                installer.bridge_legacy_updater(release)
                spawn.assert_not_called()

    def test_unmanaged_release_does_not_spawn_cleanup(self):
        with patch.object(installer.subprocess, 'Popen') as spawn:
            installer.bridge_legacy_updater(Path('/tmp/unrelated/release'))
        spawn.assert_not_called()

if __name__=='__main__':unittest.main()
