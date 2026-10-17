"""Offline hard gates for real GOST process replacement evidence."""
import contextlib
import importlib.util
import io
import json
from pathlib import Path
import socket
import struct
import sys
import tempfile
import time
import unittest
from unittest import mock


ROOT = Path(__file__).resolve().parent
sys.path.insert(0, str(ROOT))
spec = importlib.util.spec_from_file_location(
    'reliable_udp_pair_restart_gate', ROOT / 'reliable_udp_pair.py')
pair = importlib.util.module_from_spec(spec)
spec.loader.exec_module(pair)
fixture = pair.load_module('reliable_udp_restart_fixture', ROOT / 'reliable_udp_test.py')
close = pair.load_module('reliable_udp_restart_close', ROOT / 'udp_close_checks.py')


class ClientPortFixtureGates(unittest.TestCase):
    def test_client_ports_are_distinct_and_reserved_until_release(self):
        with fixture.reserved_tcp_ports(3) as ports:
            self.assertEqual(len(set(ports)), 3)
            for port in ports:
                with socket.socket(socket.AF_INET, socket.SOCK_STREAM) as stream:
                    with self.assertRaises(OSError):
                        stream.bind((fixture.HOST, port))
        for port in ports:
            with socket.socket(socket.AF_INET, socket.SOCK_STREAM) as stream:
                stream.bind((fixture.HOST, port))

    def test_port_failure_has_safe_explicit_diagnostic_and_no_retry(self):
        secret = 'credential-must-not-appear'
        fake_socket = mock.Mock()
        fake_socket.bind.side_effect = OSError(secret)
        with mock.patch.object(fixture.socket, 'socket', return_value=fake_socket) as create:
            with self.assertRaises(fixture.FixturePortAllocationError) as raised:
                with fixture.reserved_tcp_ports(3):
                    self.fail('failed reservation must never publish ports')
            self.assertEqual(create.call_count, 1)
            fake_socket.close.assert_called_once()
        output = io.StringIO()
        with tempfile.TemporaryDirectory() as directory:
            with contextlib.redirect_stderr(output):
                fixture.diagnose_failure(Path(directory), 'client_fixture_setup',
                                         type(raised.exception), raised.exception, None)
        text = output.getvalue()
        self.assertNotIn(secret, text)
        diagnostic = json.loads(text.split(' ', 1)[1])
        self.assertEqual(diagnostic['stage'], 'client_fixture_setup')
        self.assertEqual(diagnostic['failure_category'], 'client_port_reservation_failed')


class PersistentStreamFixtureGates(unittest.TestCase):
    def factories(self):
        return (('original', fixture.Target),
                ('extended', close.target_factory(fixture)))

    def test_real_persistent_stream_stays_open_until_peer_fin(self):
        for name, factory in self.factories():
            with self.subTest(target=name):
                target = factory()
                self.addCleanup(target.close)
                payload = b'persistent-restart-flow-' * 3
                with socket.create_connection(target.server_address, timeout=2) as stream:
                    stream.sendall(b'P' + struct.pack('!I', len(payload)) + payload)
                    self.assertEqual(fixture.receive(stream, len(payload),
                                                    time.monotonic() + 2), payload)
                    self.assertTrue(target.persistent_ready.wait(2))
                    with target.lock:
                        self.assertEqual(target.persistent_records, [payload])
                    self.assertFalse(target.persistent_closed.is_set())
                    stream.settimeout(0.1)
                    with self.assertRaises(socket.timeout):
                        stream.recv(1)
                    stream.shutdown(socket.SHUT_WR)
                    stream.settimeout(2)
                    self.assertEqual(stream.recv(1), b'')
                    self.assertTrue(target.persistent_closed.wait(2))
                with target.lock:
                    self.assertEqual(target.errors, [])

    def test_invalid_persistent_length_is_rejected_before_readiness(self):
        for name, factory in self.factories():
            for length in (15, 65537):
                with self.subTest(target=name, length=length):
                    target = factory()
                    self.addCleanup(target.close)
                    with socket.create_connection(target.server_address, timeout=2) as stream:
                        stream.sendall(b'P' + struct.pack('!I', length))
                        stream.settimeout(2)
                        self.assertEqual(stream.recv(1), b'')
                    self.assertFalse(target.persistent_ready.is_set())
                    self.assertFalse(target.persistent_closed.is_set())
                    with target.lock:
                        self.assertEqual(target.persistent_records, [])
                        self.assertEqual(target.errors, ['RuntimeError'])


class RestartEvidenceGates(unittest.TestCase):
    def complete(self):
        result = {
            'stable_bulk_passed': True,
            'legacy_final_frame_passed': True,
            'extended_close_passed': True,
            'engine_reconnect_passed': True,
            'lossy_close_passed': True,
        }
        result['measurement'] = {'engine_reconnect': {
            'all_checks_passed': True,
            'server_pid_before': 101,
            'server_pid_after': 202,
            'client_pid_before': 303,
            'client_pid_after': 303,
            'client_identity_unchanged': True,
        }}
        return result

    def test_complete_restart_evidence_is_required(self):
        self.assertTrue(pair.candidate_result_passes(self.complete(), lossy_close=True))

    def test_missing_restart_evidence_cannot_pass(self):
        result = self.complete()
        del result['engine_reconnect_passed']
        self.assertFalse(pair.candidate_result_passes(result, lossy_close=True))

    def test_pid_identity_change_cannot_be_hidden_by_boolean(self):
        result = self.complete()
        result['measurement']['engine_reconnect']['server_pid_after'] = 101
        self.assertFalse(pair.candidate_result_passes(result, lossy_close=False))

        result = self.complete()
        result['measurement']['engine_reconnect']['client_pid_after'] = 404
        self.assertFalse(pair.candidate_result_passes(result, lossy_close=False))

    def test_restart_failure_cannot_pass_even_when_stream_checks_pass(self):
        result = self.complete()
        result['engine_reconnect_passed'] = False
        self.assertFalse(pair.candidate_result_passes(result, lossy_close=False))

    def test_lossy_close_remains_independent_gate(self):
        result = self.complete()
        result['lossy_close_passed'] = False
        self.assertFalse(pair.candidate_result_passes(result, lossy_close=True))
        self.assertTrue(pair.candidate_result_passes(result, lossy_close=False))


if __name__ == '__main__':
    unittest.main(verbosity=2)
