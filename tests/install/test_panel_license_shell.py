"""Run the real Bash activation helper with isolated binary/OS stubs.

No root ownership, live directories, credentials or issuer requests are used.
"""
import json
import os
from pathlib import Path
import shutil
import subprocess
import sys
import tempfile
import unittest

ROOT = Path(__file__).resolve().parents[2]
BASH = os.environ.get('BASH_PATH') or shutil.which('bash')


@unittest.skipUnless(BASH, 'Bash is required for installer helper verification')
class ActivationShellTests(unittest.TestCase):
    def run_helper(self, status, tier='free', automatic=False):
        with tempfile.TemporaryDirectory(prefix='hl-license-shell-') as temporary:
            root = Path(temporary)
            binary = root/'control-api-stub'
            binary.write_text('''#!/usr/bin/env bash
printf '%s\\n' "$2" >> "$CALLS_FILE"
case "$2" in
  trust) printf '{"issuer_url":"https://issuer.example.test"}' ;;
  sync) exit 1 ;;
  status) printf '%s' "$STATUS_JSON" ;;
  *) exit 99 ;;
esac
''', encoding='utf-8', newline='\n')
            binary.chmod(0o700)
            script = root/'verify.sh'
            script.write_text('''#!/usr/bin/env bash
set -euo pipefail
source "$HELPER_PATH"
fail() { printf '%s\\n' "$*" >&2; exit 41; }
log() { printf '%s\\n' "$*"; }
python3() { "$PYTHON_EXE" "$@"; }
export -f python3
license_state_permissions() { :; }
install() { mkdir -p "${@: -1}"; }
openssl() { printf '0123456789abcdef0123456789abcdef'; }
activate_panel_license "$BINARY_PATH" panel.example.test "$STATE_PATH" '' "$TIER" "$AUTOMATIC"
printf 'awaiting=%s\\n' "$LICENSE_AWAITING_REGISTRATION"
''', encoding='utf-8', newline='\n')
            env = dict(os.environ, HELPER_PATH=(ROOT/'deploy/activate-panel-license.sh').as_posix(), PYTHON_EXE=Path(sys.executable).as_posix(), BINARY_PATH=binary.as_posix(), STATE_PATH=(root/'identity').as_posix(), CALLS_FILE=(root/'calls').as_posix(), STATUS_JSON=json.dumps(status), TIER=tier, AUTOMATIC=str(automatic).lower())
            result = subprocess.run([BASH, script.as_posix()], env=env, capture_output=True, text=True, encoding='utf-8', errors='replace', timeout=30)
            calls = (root/'calls').read_text().splitlines() if (root/'calls').exists() else []
            identity = json.loads((root/'identity/identity.json').read_text())
            return result, calls, identity

    def test_free_install_initializes_identity_without_reading_code_or_preparing(self):
        result, calls, identity = self.run_helper({'enabled': True, 'status': 'unactivated', 'management_allowed': False})
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertIn('awaiting=true', result.stdout)
        self.assertEqual(calls, ['trust', 'sync', 'status'])
        self.assertEqual(identity['domain'], 'panel.example.test')
        self.assertTrue(identity['instance_id'].startswith('hlp-'))
        self.assertFalse(identity['auto_free'])

    def test_automatic_install_records_intent_without_code_or_early_activation(self):
        result, calls, identity = self.run_helper({'enabled': True, 'status': 'unactivated', 'management_allowed': False}, automatic=True)
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertTrue(identity['auto_free'])
        self.assertEqual(calls, ['trust', 'sync', 'status'])
        self.assertIn('无需授权码或 TXT', result.stdout)

    def test_existing_expired_licensed_binding_is_not_downgraded_to_free(self):
        result, calls, _ = self.run_helper({'enabled': True, 'status': 'expired', 'tier': 'licensed', 'license_id': 'lic-existing', 'management_allowed': False})
        self.assertEqual(result.returncode, 41)
        self.assertIn('HT', result.stderr)
        self.assertEqual(calls, ['trust', 'sync', 'status'])

    def test_bad_cache_cannot_be_hidden_by_free_install(self):
        result, calls, _ = self.run_helper({'enabled': True, 'status': 'unactivated', 'last_error': 'invalid cache', 'management_allowed': False})
        self.assertEqual(result.returncode, 41)
        self.assertEqual(calls, ['trust', 'sync', 'status'])

    def test_verified_existing_license_reused_without_new_registration(self):
        result, calls, _ = self.run_helper({'enabled': True, 'status': 'active', 'tier': 'licensed', 'license_id': 'lic-existing', 'management_allowed': True})
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertIn('awaiting=false', result.stdout)
        self.assertEqual(calls, ['trust', 'sync', 'status'])


if __name__ == '__main__':
    unittest.main()
