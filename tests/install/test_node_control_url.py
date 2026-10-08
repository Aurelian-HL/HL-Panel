#!/usr/bin/env python3
"""Node address changes preserve identity and engine state; failures roll back."""
import importlib.util
import json
from pathlib import Path
import tempfile
import unittest
from unittest.mock import patch

spec = importlib.util.spec_from_file_location('node_move', Path(__file__).resolve().parents[2]/'deploy/node-control-url.py')
move = importlib.util.module_from_spec(spec)
spec.loader.exec_module(move)


class Response:
    status = 204
    def __enter__(self): return self
    def __exit__(self, *args): pass


class NodeMoveTests(unittest.TestCase):
    def test_address_requires_https_origin(self):
        for value in ['http://panel.example.com', 'https://u:p@panel.example.com', 'https://panel.example.com/path', 'https://panel.example.com?q=1', 'https://panel.example.com/#a']:
            with self.subTest(value=value), self.assertRaises(move.MoveError): move.origin(value)
        self.assertEqual(move.origin('https://panel.example.com:8443/'), 'https://panel.example.com:8443')

    def exercise(self, failure='', rollback_failure=False):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            state = root/'state'; state.mkdir()
            config = root/'edge-agent.json'
            original = {'control_plane_url':'https://old.example.com', 'ca_file':'old-ca.pem', 'data_dir':str(state), 'engine_mode':'apply'}
            config.write_text(json.dumps(original)); config.chmod(0o600)
            identity = state/'credentials.json'; identity.write_text(json.dumps({'node_id':'isolated-node', 'node_credential':'isolated-credential'}))
            engine = state/'last-known-good.json'; engine.write_text('isolated engine config')
            cursor = state/'usage-cursor.json'; cursor.write_text('isolated cursor')
            before = {p.name:p.read_bytes() for p in state.iterdir()}
            calls = []
            def run(args):
                calls.append(args)
                if failure == 'restart' and (len(calls) == 1 or rollback_failure): raise move.MoveError('isolated service failure')
            class Opener:
                def open(self, req, timeout):
                    if failure == 'connection': raise OSError('isolated network rejection')
                    self_url = req.full_url
                    if self_url != 'https://new.example.com/api/v1/agent/desired': raise AssertionError('wrong validation route')
                    return Response()
            with patch.object(move, 'CONFIG', config), patch.object(move, 'STATE', state), patch.object(move, 'BACKUPS', root/'backups'), patch.object(move, 'run', run), patch.object(move.time, 'sleep'), patch.object(move.urllib.request, 'build_opener', return_value=Opener()):
                if failure:
                    with self.assertRaises(move.MoveError) as captured: move.move('https://new.example.com')
                    self.assertEqual(json.loads(config.read_text()), original)
                    if rollback_failure: self.assertIn('旧节点服务重启失败', str(captured.exception))
                else:
                    move.move('https://new.example.com')
                    expected = dict(original, control_plane_url='https://new.example.com', ca_file='')
                    self.assertEqual(json.loads(config.read_text()), expected)
                    self.assertEqual(calls, [['systemctl','restart',move.SERVICE], ['systemctl','is-active','--quiet',move.SERVICE]])
                self.assertEqual({p.name:p.read_bytes() for p in state.iterdir()}, before)
                backups = list((root/'backups').glob('*/edge-agent.json'))
                self.assertEqual(len(backups), 0 if failure == 'connection' else 1)
                if backups:
                    self.assertEqual(json.loads(backups[0].read_text()), original)
                    self.assertEqual(backups[0].stat().st_mode & 0o777, 0o600)

    def test_success_preserves_identity_engines_and_cursors(self): self.exercise()
    def test_connection_rejection_does_not_write_config(self): self.exercise('connection')
    def test_failed_restart_restores_old_config(self): self.exercise('restart')
    def test_rollback_restart_failure_reported(self): self.exercise('restart', True)


if __name__ == '__main__': unittest.main()
