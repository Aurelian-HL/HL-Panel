"""License upgrade failures use only temporary files and mocked OS boundaries."""
import ast
import base64
import importlib.util
import json
import os
import re
from pathlib import Path
import subprocess
import sys
import tempfile
import types
import unittest
from unittest.mock import patch

ROOT = Path(__file__).resolve().parents[2]

def load(name, source):
    spec = importlib.util.spec_from_file_location(name, ROOT/'deploy'/source)
    module = importlib.util.module_from_spec(spec)
    missing = {key:types.ModuleType(key) for key in ('fcntl','grp') if os.name=='nt'}
    with patch.dict(sys.modules, missing): spec.loader.exec_module(module)
    return module

check = load('license_check','check-panel-license.py')
update = load('license_update','update.py')
trust = load('license_build_trust','build-license-trust.py')

class ReleaseTrustTests(unittest.TestCase):
    def environment(self, keys=None):
        keys = keys or {'release-1': base64.urlsafe_b64encode(bytes(range(32))).decode().rstrip('=')}
        return {'HL_PANEL_LICENSE_ISSUER_URL':'https://issuer.example.test',
                'HL_PANEL_LICENSE_ISSUER_ID':'https://issuer.example.test',
                'HL_PANEL_LICENSE_PUBLIC_KEYS_B64':base64.urlsafe_b64encode(json.dumps(keys).encode()).decode().rstrip('=')}

    def test_build_requires_complete_https_trust_and_real_key_bytes(self):
        self.assertIn('-X main.panelLicensePublicKeysB64=', trust.linker_flags(self.environment()))
        for env in ({}, {**self.environment(),'HL_PANEL_LICENSE_ISSUER_URL':'http://issuer.example.test'},
                    {**self.environment(),'HL_PANEL_LICENSE_ISSUER_ID':'https://issuer.example.test -X main.disable=true'},
                    self.environment({'release-1':'short'}), self.environment({'disposable-ci-only':'A'*43})):
            with self.subTest(env=list(env)), self.assertRaises(ValueError): trust.linker_flags(env)

class ActivationIdentityTests(unittest.TestCase):
    def test_existing_identity_cannot_be_overwritten_by_wrong_install_arguments(self):
        source = (ROOT/'deploy/activate-panel-license.sh').read_text(encoding='utf-8')
        script = re.search(r'# Preserve an existing identity.*?\n[^\n]*<<\'PY\'[^\n]*\n(.*?)\nPY', source, re.S).group(1)
        with tempfile.TemporaryDirectory() as temporary:
            directory = Path(temporary)
            identity = directory/'identity.json'
            instance = directory/'instance-id'
            instance.write_text('hlp-original-instance', encoding='ascii')
            original = b'{"domain":"panel.example.test","instance_id":"hlp-original-instance"}'
            for contents, requested, expected in (
                (original, 'panel.example.test', 0),
                (original, 'wrong.example.test', 1),
                (b'not json', 'panel.example.test', 1),
                (original.replace(b'hlp-original-instance', b'hlp-other-instance'), 'panel.example.test', 1)):
                identity.write_bytes(contents)
                result = subprocess.run([sys.executable, '-c', script, str(directory), requested], capture_output=True)
                self.assertEqual(result.returncode, expected)
                self.assertEqual(identity.read_bytes(), contents)
                self.assertEqual(instance.read_text(encoding='ascii'), 'hlp-original-instance')

class LicensePreflightTests(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory(); self.addCleanup(self.tmp.cleanup)
        self.root = Path(self.tmp.name)
        self.directory = self.root/'identity'; self.directory.mkdir()
        self.env = self.root/'control-api.env'; self.env.write_text('CONTROL_LICENSE_DOMAIN=panel.example.test\n')
        self.candidate = self.root/'candidate'; (self.candidate/'deploy').mkdir(parents=True)
        (self.candidate/'deploy/check-panel-license.py').write_text('isolated helper')
        for name in ('instance-key','identity.json','state.json'): (self.directory/name).write_bytes(b'isolated')
        # Redirect the one canonical directory to the temporary test directory.
        actual_path = Path
        self.paths = patch.object(check,'Path',lambda value: self.directory if str(value)=='/var/lib/hl-panel-license' else actual_path(value))
        self.paths.start(); self.addCleanup(self.paths.stop)

    def test_missing_identity_never_executes_candidate(self):
        (self.directory/'identity.json').unlink()
        with patch.object(check.subprocess,'run') as run, self.assertRaises(ValueError):
            check.check(self.candidate/'bin/control-api', self.env)
        run.assert_not_called()

    def test_signed_expired_identity_allows_security_upgrade_without_granting_management(self):
        status={'enabled':True,'license_id':'lic-existing','status':'expired','management_allowed':False}
        with patch.object(check.subprocess,'run',return_value=types.SimpleNamespace(returncode=0,stdout=json.dumps(status).encode())) as run:
            self.assertEqual(check.check(self.candidate/'bin/control-api',self.env),status)
        self.assertEqual(run.call_args.args[0][:4],['runuser','-u','hlpanel','--'])
        self.assertFalse(any(key.startswith('CONTROL_LICENSE_') for key in run.call_args.kwargs['env']))

    def test_invalid_signature_permission_failure_and_unactivated_are_rejected(self):
        for result in (types.SimpleNamespace(returncode=1,stdout=b''),types.SimpleNamespace(returncode=0,stdout=b'{"enabled":true}')):
            with self.subTest(result=result),patch.object(check.subprocess,'run',return_value=result),self.assertRaises(ValueError):
                check.check(self.candidate/'bin/control-api',self.env)

    def test_initialized_free_identity_can_upgrade_to_restricted_registration_ui(self):
        status = {'enabled':True,'status':'unactivated','management_allowed':False}
        (self.directory/'state.json').unlink()
        with patch.object(check.subprocess,'run',return_value=types.SimpleNamespace(returncode=0,stdout=json.dumps(status).encode())):
            self.assertFalse(check.check(self.candidate/'bin/control-api',self.env)['management_allowed'])
        status['last_error']='授权缓存校验失败'
        with patch.object(check.subprocess,'run',return_value=types.SimpleNamespace(returncode=0,stdout=json.dumps(status).encode())),self.assertRaises(ValueError):
            check.check(self.candidate/'bin/control-api',self.env)

    def test_upgrade_failure_is_explicit_and_runs_no_service_command(self):
        with patch.object(update.subprocess,'run',return_value=types.SimpleNamespace(returncode=1)) as run:
            with self.assertRaisesRegex(update.UpdateError,'当前服务未停止'):
                update.check_candidate_license(self.candidate)
        self.assertEqual(run.call_count,1)
        self.assertNotIn('systemctl',run.call_args.args[0])

    def test_real_upgrade_sequence_preflights_before_backup_and_stop(self):
        tree=ast.parse((ROOT/'deploy/update.py').read_text(encoding='utf-8'))
        main=next(node for node in tree.body if isinstance(node,ast.FunctionDef) and node.name=='main')
        calls=[node for node in ast.walk(main) if isinstance(node,ast.Call)]
        preflight=next(node.lineno for node in calls if isinstance(node.func,ast.Name) and node.func.id=='check_candidate_license')
        stops=[node.lineno for node in calls if isinstance(node.func,ast.Name) and node.func.id=='run' and node.args and isinstance(node.args[0],ast.List) and len(node.args[0].elts)>1 and isinstance(node.args[0].elts[1],ast.Constant) and node.args[0].elts[1].value=='stop']
        self.assertTrue(stops)
        self.assertLess(preflight,min(stops))

if __name__=='__main__': unittest.main()
