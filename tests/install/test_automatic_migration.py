"""Fixed migration executor fault tests; no SSH, network or installed panel access."""
import base64
import hashlib
import importlib.util
import json
import os
from pathlib import Path
import sys
import tempfile
import types
import unittest
from unittest.mock import patch

# Only the test import is portable; the production executor still requires Linux.
if os.name == 'nt':
    sys.modules.setdefault('fcntl', types.SimpleNamespace())
spec = importlib.util.spec_from_file_location('automatic_executor', Path(__file__).resolve().parents[2] / 'internal/control/panelmigration/remote.py')
executor = importlib.util.module_from_spec(spec)
spec.loader.exec_module(executor)


class ExecutorTests(unittest.TestCase):
    def setUp(self):
        self.temporary = tempfile.TemporaryDirectory()
        root = Path(self.temporary.name)
        self.state = root / 'state'
        self.state.mkdir()
        self.fence = self.state / 'fence.json'
        self.marker = {'id': '12345678-1234-1234-1234-123456789abc', 'state': 'restored', 'domain': 'panel.example.com', 'public_ip': '8.8.8.8', 'source_url': 'https://panel.example.com', 'version': 'v0.1.50'}
        self.data = {**self.marker, 'digest': hashlib.sha256(b'encrypted-test').hexdigest(), 'counts': {}, 'bootstrap_password': 'isolated-test-bootstrap', 'password': 'isolated-test-backup', 'archive': base64.b64encode(b'encrypted-test').decode()}
        self.fence.write_text(json.dumps({'role': 'receiver', 'id': self.marker['id']}))
        for name, value in [('ROOT', root), ('STATE', self.state), ('FENCE', self.fence)]:
            fixture = patch.object(executor, name, value)
            fixture.start()
            self.addCleanup(fixture.stop)
        self.addCleanup(self.temporary.cleanup)

    def test_existing_sql_receipt_never_reimports(self):
        with patch.object(executor, 'load_marker', return_value=self.marker), patch.object(executor, 'receipt', return_value=[self.data['digest'], 'recovery-id']), patch.object(executor, 'reset_bootstrap') as reset, patch.object(executor, 'local') as local, patch.object(executor, 'verify_counts') as counts, patch.object(executor, 'save'), patch.object(executor, 'wait_health'):
            result = executor.restore(self.data)
            self.assertEqual(result['recovery_id'], 'recovery-id')
            reset.assert_not_called()
            local.assert_not_called()
            counts.assert_called_once_with({})

    def test_receipt_digest_mismatch_blocks_restore(self):
        with patch.object(executor, 'load_marker', return_value=self.marker), patch.object(executor, 'receipt', return_value=['different', 'recovery-id']), patch.object(executor, 'reset_bootstrap') as reset:
            with self.assertRaisesRegex(executor.Failure, 'marker'):
                executor.restore(self.data)
            reset.assert_not_called()

    def test_activation_after_failed_restart_restarts_again(self):
        self.marker['state'] = 'active'
        self.fence.unlink()
        user = types.SimpleNamespace(pw_uid=0, pw_gid=0)
        mocked_pwd = types.SimpleNamespace(getpwnam=lambda name: user)
        with patch.object(executor, 'load_marker', return_value=self.marker), patch.object(executor, 'dns_ready', return_value=True), patch.object(executor, 'public_health', return_value={'status': 'ok', 'version': 'v0.1.50', 'migration_isolated': False}), patch.object(executor, 'command') as command, patch.object(executor, 'wait_health'), patch.object(executor, 'save') as save, patch.object(executor.os, 'chown', create=True), patch.dict(sys.modules, {'pwd': mocked_pwd}):
            self.assertEqual(executor.activate(self.data)['state'], 'completed')
            self.assertIn(unittest.mock.call(['systemctl', 'restart', 'hl-panel-control-api.service'], code='health'), command.call_args_list)
            self.assertTrue(any(call.args[0] == self.state / 'received.json' for call in save.call_args_list))

    def test_wrong_dns_does_not_change_fence_or_start_target(self):
        with patch.object(executor, 'load_marker', return_value=self.marker), patch.object(executor, 'dns_ready', return_value=False), patch.object(executor, 'command') as command:
            self.assertEqual(executor.activate(self.data)['state'], 'waiting_dns')
            command.assert_not_called()
            self.assertTrue(self.fence.exists())

    def test_active_target_cannot_be_rolled_back(self):
        self.marker['state'] = 'active'
        with patch.object(executor, 'load_marker', return_value=self.marker), patch.object(executor, 'command') as command:
            with self.assertRaisesRegex(executor.Failure, 'marker'):
                executor.rollback(self.data)
            command.assert_not_called()

    def test_counts_and_usage_ledger_are_verified(self):
        snapshot = {name: [] for name in executor.COLLECTIONS.values()}
        snapshot['Nodes'] = [{'ID': 'test-node'}]
        expected = {name: 0 for name in (*executor.COLLECTIONS.keys(), *executor.TABLES)}
        expected['nodes'] = 1
        def sql(statement):
            return json.dumps(snapshot) if 'convert_from' in statement else '0'
        with patch.object(executor, 'sql', side_effect=sql):
            executor.verify_counts(expected)
            expected['nodes'] = 0
            with self.assertRaisesRegex(executor.Failure, 'counts'):
                executor.verify_counts(expected)

    def test_import_response_loss_uses_transaction_receipt(self):
        self.marker['state'] = 'prepared'
        preview = {'digest': self.data['digest'], 'counts': {}}
        def local(path, *args):
            if path.endswith('/login'):
                return {'access_token': 'isolated-test-session'}
            if path.endswith('/preview'):
                return preview
            raise OSError('private network diagnostic')
        with patch.object(executor, 'load_marker', return_value=self.marker), patch.object(executor, 'receipt', side_effect=[None, [self.data['digest'], 'recovery-id']]), patch.object(executor, 'reset_bootstrap'), patch.object(executor, 'local', side_effect=local), patch.object(executor, 'verify_counts'), patch.object(executor, 'save'), patch.object(executor, 'wait_health'):
            self.assertEqual(executor.restore(self.data)['recovery_id'], 'recovery-id')


if __name__ == '__main__':
    unittest.main()
