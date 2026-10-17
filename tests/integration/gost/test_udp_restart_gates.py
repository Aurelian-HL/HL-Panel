"""Offline hard gates for real GOST process replacement evidence."""
import contextlib
import importlib.util
import io
import json
from pathlib import Path
import select
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

    def test_early_fin_cannot_be_counted_as_restart_failure(self):
        for reported_closed in (False, True):
            with self.subTest(target_reported_closed=reported_closed):
                def early_fin(handler):
                    stream, target = handler.request, handler.server
                    header = fixture.receive(stream, 5, time.monotonic() + 2)
                    length = struct.unpack('!I', header[1:])[0]
                    payload = fixture.receive(stream, length, time.monotonic() + 2)
                    stream.sendall(payload)
                    stream.shutdown(socket.SHUT_WR)
                    if reported_closed:
                        target.persistent_closed.set()
                    target.persistent_ready.set()

                with mock.patch.object(fixture.TargetHandler, 'handle', early_fin):
                    target = fixture.Target()
                    self.addCleanup(target.close)
                    server, client = mock.Mock(), mock.Mock()
                    with self.assertRaises(RuntimeError):
                        fixture.restart_recovery_check(server, client,
                                                       target.server_address[1], target)
                    server.stop.assert_not_called()
                    server.restart.assert_not_called()

    def test_live_persistent_stream_reaches_server_stop(self):
        for name, factory in self.factories():
            with self.subTest(target=name):
                target = factory()
                self.addCleanup(target.close)
                server, client = mock.Mock(), mock.Mock()
                server.process.pid, client.process.pid = 101, 303
                client.cpu_ticks.return_value = {'starttime': 1234}
                server.stop.side_effect = RuntimeError('observed-server-stop')
                with self.assertRaisesRegex(fixture.RestartRecoveryError, '^restart_server_stop_failed$'):
                    fixture.restart_recovery_check(server, client,
                                                   target.server_address[1], target)
                server.stop.assert_called_once()
                server.restart.assert_not_called()


class RestartDiagnosticGates(unittest.TestCase):
    def test_each_restart_phase_has_fixed_safe_failure_diagnostic(self):
        for phase in sorted(fixture.RESTART_PHASES) + ['secret-invalid-phase']:
            with self.subTest(phase=phase):
                with self.assertRaises(fixture.RestartRecoveryError) as raised:
                    with fixture.restart_phase(phase):
                        raise RuntimeError('secret-origin-error')
                output = io.StringIO()
                with tempfile.TemporaryDirectory() as directory:
                    with contextlib.redirect_stderr(output):
                        fixture.diagnose_failure(Path(directory), 'engine_reconnect',
                                                 type(raised.exception), raised.exception, None)
                text = output.getvalue()
                self.assertNotIn('secret', text)
                diagnostic = json.loads(text.split(' ', 1)[1])
                safe = phase if phase in fixture.RESTART_PHASES else 'unknown_restart_phase'
                self.assertEqual(diagnostic['stage'], 'engine_reconnect_' + safe)
                self.assertEqual(diagnostic['failure_category'], 'restart_' + safe + '_failed')


class ProxyRestartFixtureGates(unittest.TestCase):
    def proxy(self):
        proxy = fixture.LossProxy(fixture.unused_port(socket.SOCK_DGRAM))
        self.addCleanup(proxy.close)
        return proxy

    def test_outbound_refusal_is_counted_and_next_datagram_can_forward(self):
        proxy = self.proxy()
        payload = b'refused-datagram'
        send = mock.Mock(side_effect=[ConnectionRefusedError('secret-refusal'), len(payload)])
        self.assertFalse(proxy.forward('out', payload, send))
        self.assertTrue(proxy.forward('out', payload, send))
        counters = proxy.snapshot()
        self.assertEqual(counters['datagrams_seen']['out'], 2)
        self.assertEqual(counters['datagrams_send_failed']['out'], 1)
        self.assertEqual(counters['datagrams_forwarded']['out'], 1)
        self.assertEqual(counters['datagrams_dropped']['out'], 0)
        self.assertEqual(counters['connection_refused_events']['out_send'], 1)
        self.assertEqual(counters['udp_payload_bytes_send_failed']['out'], len(payload))
        self.assertTrue(proxy.thread.is_alive())
        proxy.enable_loss(0)
        counters = proxy.snapshot()
        self.assertEqual(counters['datagrams_send_failed']['out'], 0)
        self.assertEqual(counters['connection_refused_events']['out_send'], 0)

    def test_other_send_errors_remain_fatal_with_balanced_counters(self):
        for direction, error in (('out', PermissionError('secret-permission')),
                                 ('back', ConnectionRefusedError('secret-back-refusal')),
                                 ('out', OSError('secret-other-error'))):
            with self.subTest(direction=direction, error=type(error).__name__):
                proxy = self.proxy()
                with self.assertRaises(type(error)):
                    proxy.forward(direction, b'x', mock.Mock(side_effect=error))
                counters = proxy.snapshot()
                self.assertEqual(counters['datagrams_send_failed'][direction], 1)
                self.assertEqual(counters['datagrams_forwarded'][direction], 0)
                self.assertEqual(counters['datagrams_dropped'][direction], 0)
                self.assertEqual(counters['connection_refused_events']['out_send'], 0)

    @unittest.skipUnless(sys.platform == 'linux', 'requires Linux connected-UDP ICMP semantics')
    def test_real_closed_udp_port_send_refusal_and_same_port_recovery(self):
        proxy = self.proxy()
        with socket.socket(socket.AF_INET, socket.SOCK_DGRAM) as receiver:
            receiver.bind((fixture.HOST, 0))
            address = receiver.getsockname()
        with socket.socket(socket.AF_INET, socket.SOCK_DGRAM) as upstream:
            upstream.connect(address)
            self.assertTrue(proxy.forward('out', b'closed-port', upstream.send))
            self.assertEqual(select.select([upstream], [], [], 2)[0], [upstream],
                             'closed loopback port did not produce a pending ICMP error')
            self.assertFalse(proxy.forward('out', b'pending-error', upstream.send))
            with socket.socket(socket.AF_INET, socket.SOCK_DGRAM) as replacement:
                replacement.bind(address)
                replacement.settimeout(2)
                self.assertTrue(proxy.forward('out', b'replacement-ready', upstream.send))
                self.assertEqual(replacement.recvfrom(1024)[0], b'replacement-ready')
        counters = proxy.snapshot()
        self.assertEqual(counters['datagrams_send_failed']['out'], 1)
        self.assertEqual(counters['connection_refused_events']['out_send'], 1)
        self.assertEqual(counters['datagrams_dropped']['out'], 0)


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
            'server_stopped': True,
            'client_pid_before': 303,
            'client_pid_after': 303,
            'client_identity_unchanged': True,
            'old_connection_open_before_stop': True,
            'pre_stop_probe_elapsed_seconds': 0.1,
            'old_connection_failed': True,
            'old_connection_failure': 'eof',
            'old_connection_failure_timeout_seconds': 15,
            'old_connection_failure_elapsed_seconds': 0.2,
            'replacement_readiness_elapsed_seconds': 0.3,
            'replacement_bulk_passed': True,
            'replacement_upload_bytes': 2097152,
            'replacement_download_bytes': 2097152,
            'replacement_upload_sha256': 'a' * 64,
            'replacement_download_sha256': 'b' * 64,
            'injected_datagram_loss': False,
            'scope': pair.RESTART_SCOPE,
        }}
        return result

    def test_complete_restart_evidence_is_required(self):
        self.assertTrue(pair.candidate_result_passes(self.complete(), lossy_close=True))

    def test_missing_restart_evidence_cannot_pass(self):
        result = self.complete()
        del result['engine_reconnect_passed']
        self.assertFalse(pair.candidate_result_passes(result, lossy_close=True))

        for key in self.complete()['measurement']['engine_reconnect']:
            with self.subTest(missing=key):
                result = self.complete()
                del result['measurement']['engine_reconnect'][key]
                self.assertFalse(pair.candidate_result_passes(result, lossy_close=True))

    def test_wrong_restart_fields_cannot_pass(self):
        cases = {
            'all_checks_passed': False,
            'server_stopped': False,
            'server_pid_before': True,
            'server_pid_after': 0,
            'client_pid_before': -1,
            'client_pid_after': '303',
            'client_identity_unchanged': False,
            'old_connection_open_before_stop': False,
            'old_connection_failed': False,
            'old_connection_failure': 'timeout',
            'old_connection_failure_timeout_seconds': 16,
            'pre_stop_probe_elapsed_seconds': -0.1,
            'old_connection_failure_elapsed_seconds': 15.001,
            'replacement_readiness_elapsed_seconds': float('nan'),
            'replacement_bulk_passed': False,
            'replacement_upload_bytes': 2097151,
            'replacement_download_bytes': 2097153,
            'injected_datagram_loss': True,
            'scope': 'production or loss-injected restart',
        }
        for key, value in cases.items():
            with self.subTest(field=key, value=value):
                result = self.complete()
                result['measurement']['engine_reconnect'][key] = value
                self.assertFalse(pair.candidate_result_passes(result, lossy_close=False))

    def test_invalid_restart_hashes_cannot_pass(self):
        for key in ('replacement_upload_sha256', 'replacement_download_sha256'):
            for value in (None, 64, 'a' * 63, 'a' * 65, 'z' * 64, 'a' * 63 + '\n'):
                with self.subTest(field=key, value=value):
                    result = self.complete()
                    result['measurement']['engine_reconnect'][key] = value
                    self.assertFalse(pair.candidate_result_passes(result, lossy_close=False))

    def test_malformed_measurement_cannot_pass(self):
        for value in (None, [], 'invalid', {'engine_reconnect': None}):
            with self.subTest(measurement=value):
                result = self.complete()
                result['measurement'] = value
                self.assertFalse(pair.candidate_result_passes(result, lossy_close=False))

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
