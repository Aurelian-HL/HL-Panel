#!/usr/bin/env python3
"""Local task journal, permission and Unix HTTP contract regressions."""
import http.client
import importlib.util
import json
import os
from pathlib import Path
import socket
import tempfile
import threading
import unittest

source = Path(__file__).resolve().parents[2]/'deploy/panel-update-worker.py'
spec = importlib.util.spec_from_file_location('panel_update_worker_test', source)
worker = importlib.util.module_from_spec(spec)
spec.loader.exec_module(worker)
ID = '3a6f4f9c-158d-406a-9c14-c293a1e5321b'
OTHER = '122ea040-9125-402f-923d-23623f1d22fd'


class Tests(unittest.TestCase):
    def test_rejects_invalid_input_and_journals_idempotent_task(self):
        with tempfile.TemporaryDirectory() as tmp:
            running = threading.Event()
            complete = threading.Event()
            calls = []
            def execute(identifier, version):
                calls.append((identifier, version))
                running.set()
                complete.wait(5)
            store = worker.TaskStore(Path(tmp), execute)
            for identifier, version in [(ID, 'latest'), (ID, 'v1.2.3;sh'), ('../task', 'v1.2.3'), (ID, 'v01.2.3'), (None, [])]:
                self.assertEqual(store.submit(identifier, version)[0], 400)
            self.assertEqual(store.submit(ID, 'v1.2.3')[0], 202)
            self.assertTrue(running.wait(2))
            self.assertEqual(store.submit(ID, 'v1.2.3')[0], 202)
            self.assertEqual(store.submit(ID, 'v1.2.4')[0], 409)
            self.assertEqual(store.submit(OTHER, 'v1.2.4')[0], 409)
            self.assertEqual(len(calls), 1)
            self.assertEqual(store.file.stat().st_mode & 0o077, 0)
            recovered = worker.TaskStore(Path(tmp), execute)
            self.assertEqual(recovered.status()['task']['phase'], 'interrupted')
            self.assertEqual(recovered.submit(ID, 'v1.2.3')[1]['task']['state'], 'failed')
            complete.set()

    def test_failed_task_does_not_disclose_exception_or_credentials(self):
        with tempfile.TemporaryDirectory() as tmp:
            done = threading.Event()
            def execute(*_):
                raise RuntimeError('private-admin-password')
            store = worker.TaskStore(Path(tmp), execute)
            original = store.change
            def change(*args, **values):
                original(*args, **values)
                done.set()
            store.change = change
            store.submit(ID, 'v1.2.3')
            self.assertTrue(done.wait(2))
            self.assertEqual(store.status()['task']['state'], 'failed')
            self.assertNotIn('private-admin-password', store.file.read_text())

    def test_rejects_unsafe_state_directory(self):
        with tempfile.TemporaryDirectory() as tmp:
            Path(tmp).chmod(0o755)
            with self.assertRaises(RuntimeError):
                worker.TaskStore(Path(tmp))

    def test_local_http_denies_peer_and_arbitrary_route(self):
        with tempfile.TemporaryDirectory() as tmp:
            class Denied(worker.Handler):
                def authorized(self):
                    return False
            path = str(Path(tmp)/'channel.sock')
            server = worker.Server(path, Denied)
            threading.Thread(target=server.serve_forever, daemon=True).start()
            try:
                conn = http.client.HTTPConnection('localhost')
                conn.sock = socket.socket(socket.AF_UNIX, socket.SOCK_STREAM)
                conn.sock.connect(path)
                conn.request('POST', '/start', json.dumps({'id': ID, 'version': 'v1.2.3'}))
                response = conn.getresponse()
                self.assertEqual(response.status, 403)
                conn.close()
            finally:
                server.shutdown()
                server.server_close()


if __name__ == '__main__':
    unittest.main()
