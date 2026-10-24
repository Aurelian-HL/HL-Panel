"""Fixed-purpose worker and maintenance rollback, on isolated temporary paths."""
import importlib.util
import json
import os
from pathlib import Path
import tempfile
import types
import unittest
from unittest.mock import patch

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
                return types.SimpleNamespace(returncode=1 if 'is-active' in args or 'is-enabled' in args else 0)
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
                return types.SimpleNamespace(returncode=0)
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
                return types.SimpleNamespace(returncode=1 if 'is-active' in args or 'is-enabled' in args else 0)
            with patch.object(installer,'ROOT',program),patch.object(installer,'UNITS',units),patch.object(installer.subprocess,'run',side_effect=run):
                with self.assertRaises(OSError):installer.install(release)
                self.assertEqual(dropin.read_text(),'custom operator override')
                self.assertEqual(list(program.iterdir()),[])
                self.assertEqual(list(units.iterdir()),[dropin.parent])

if __name__=='__main__':unittest.main()
