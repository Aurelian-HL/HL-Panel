"""Offline gates for fixture cleanup failure ordering and safe diagnostics."""
import contextlib
import importlib.util
import io
import json
from pathlib import Path
import tempfile
import threading
import unittest
from unittest import mock


ROOT = Path(__file__).resolve().parent
spec = importlib.util.spec_from_file_location(
    'reliable_udp_cleanup_fixture', ROOT / 'reliable_udp_test.py')
fixture = importlib.util.module_from_spec(spec)
spec.loader.exec_module(fixture)


class CleanupGates(unittest.TestCase):
    def run_cleanup(self, *, failing=(), primary=None, phases=None):
        order, failures, observations = [], [], []
        output = io.StringIO()
        phases = phases or ('target_close', 'proxy_close', 'server_close', 'client_close')

        def close(phase):
            order.append(phase)
            if phase in failing:
                raise RuntimeError('secret-cleanup-' + str(phase))

        propagated = None
        with tempfile.TemporaryDirectory() as directory:
            def diagnostic(exc_type, error, traceback):
                observations.append((list(order), error))
                return fixture.diagnose_failure(
                    Path(directory), 'engine_reconnect', exc_type, error, traceback,
                    cleanup_failures=failures)

            with contextlib.redirect_stderr(output):
                try:
                    with contextlib.ExitStack() as cleanup:
                        cleanup.push(diagnostic)
                        for phase in phases:
                            fixture.register_cleanup(
                                cleanup, phase, lambda phase=phase: close(phase), failures)
                        if primary is not None:
                            raise primary
                except BaseException as error:
                    propagated = error
        return order, failures, observations, output.getvalue(), propagated

    def diagnostics(self, output):
        parsed = {}
        for line in output.splitlines():
            prefix, body = line.split(' ', 1)
            parsed[prefix] = json.loads(body)
        return parsed

    def test_successful_cleanup_is_reverse_order_and_emits_nothing(self):
        order, failures, observed, output, error = self.run_cleanup()
        self.assertEqual(order, ['client_close', 'server_close', 'proxy_close', 'target_close'])
        self.assertEqual(failures, [])
        self.assertIsNone(error)
        self.assertEqual(observed, [(order, None)])
        self.assertEqual(output, '')

    def test_restart_error_identity_and_phase_survive_multiple_cleanup_errors(self):
        primary = fixture.RestartRecoveryError('old_stream_exit')
        order, failures, observed, output, error = self.run_cleanup(
            failing=('target_close', 'client_close', 'proxy_close'), primary=primary)
        self.assertIs(error, primary)
        self.assertEqual(observed, [(order, primary)])
        self.assertEqual(failures, ['client_close', 'proxy_close', 'target_close'])
        parsed = self.diagnostics(output)
        cleanup = parsed['RELIABLE_UDP_CLEANUP_DIAGNOSTIC']
        self.assertEqual(cleanup['failure_phases'], failures)
        self.assertEqual(cleanup['failure_count'], 3)
        self.assertIs(cleanup['scenario_failure_preserved'], True)
        diagnostic = parsed['RELIABLE_UDP_DIAGNOSTIC']
        self.assertEqual(diagnostic['stage'], 'engine_reconnect_old_stream_exit')
        self.assertEqual(diagnostic['failure_category'], 'restart_old_stream_exit_failed')
        self.assertEqual(diagnostic['cleanup_failure_phases'], failures)
        self.assertNotIn('secret-cleanup-', output)

    def test_successful_body_with_multiple_cleanup_errors_still_fails(self):
        order, failures, observed, output, error = self.run_cleanup(
            failing=('client_close', 'server_close', 'target_close'))
        self.assertIsInstance(error, fixture.FixtureCleanupError)
        self.assertEqual(error.phase, 'client_close')
        self.assertEqual(str(error), 'fixture_cleanup_client_close_failed')
        self.assertEqual(failures, ['client_close', 'server_close', 'target_close'])
        self.assertEqual(observed, [(order, error)])
        parsed = self.diagnostics(output)
        self.assertIs(parsed['RELIABLE_UDP_CLEANUP_DIAGNOSTIC']['scenario_failure_preserved'], False)
        self.assertEqual(parsed['RELIABLE_UDP_DIAGNOSTIC']['stage'], 'fixture_cleanup')
        self.assertEqual(parsed['RELIABLE_UDP_DIAGNOSTIC']['failure_category'],
                         'fixture_cleanup_client_close_failed')
        self.assertNotIn('secret-cleanup-', output)

    def test_unknown_cleanup_phase_cannot_export_arbitrary_text(self):
        secret = 'secret-phase-must-not-appear'
        _, failures, _, output, error = self.run_cleanup(
            failing=(secret,), phases=(secret,))
        self.assertEqual(failures, ['unknown_cleanup_phase'])
        self.assertIsInstance(error, fixture.FixtureCleanupError)
        self.assertEqual(error.phase, 'unknown_cleanup_phase')
        self.assertNotIn(secret, str(error))
        self.assertNotIn(secret, output)

    def test_cleanup_diagnostic_survives_broken_snapshot(self):
        primary = fixture.RestartRecoveryError('old_stream_exit')
        proxy = mock.Mock()
        proxy.snapshot.side_effect = RuntimeError('secret-snapshot-error')
        output = io.StringIO()
        with tempfile.TemporaryDirectory() as directory:
            with contextlib.redirect_stderr(output):
                self.assertIs(fixture.diagnose_failure(
                    Path(directory), 'engine_reconnect', type(primary), primary, None,
                    proxy=proxy, cleanup_failures=['target_close']), False)
        parsed = self.diagnostics(output.getvalue())
        self.assertEqual(parsed['RELIABLE_UDP_CLEANUP_DIAGNOSTIC']['failure_phases'],
                         ['target_close'])
        self.assertEqual(parsed['RELIABLE_UDP_DIAGNOSTIC']['categories'],
                         ['diagnostic_unavailable'])
        self.assertEqual(parsed['RELIABLE_UDP_DIAGNOSTIC']['cleanup_failure_phases'],
                         ['target_close'])
        self.assertNotIn('secret-snapshot-error', output.getvalue())

    def test_diagnostic_write_failure_does_not_replace_primary_error(self):
        primary = fixture.RestartRecoveryError('old_stream_exit')
        with mock.patch('builtins.print', side_effect=OSError('secret-output-error')):
            _, failures, observed, _, error = self.run_cleanup(
                failing=('target_close',), primary=primary)
        self.assertIs(error, primary)
        self.assertEqual(failures, ['target_close'])
        self.assertIs(observed[0][1], primary)

    def test_scenario_registers_all_resources_and_diagnoses_after_every_close(self):
        order = []
        primary = fixture.RestartRecoveryError('old_stream_exit')

        class Target:
            server_address = (fixture.HOST, 12345)
            records = []
            errors = []
            lock = threading.Lock()

            def close(self):
                order.append('target_close')
                raise RuntimeError('secret-target-worker-failure')

        class Proxy:
            address = (fixture.HOST, 12346)
            errors = []

            def __init__(self, _port):
                pass

            def close(self):
                order.append('proxy_close')
                raise RuntimeError('secret-proxy-cleanup-failure')

            def snapshot(self):
                self.snapshot_order = list(order)
                return {}

        class Engine:
            def __init__(self, _binary, _directory, name, _config):
                self.name = name

            def close(self):
                order.append(self.name + '_close')

        target, proxy = Target(), Proxy(0)
        output = io.StringIO()
        with contextlib.ExitStack() as patches:
            patches.enter_context(mock.patch.object(fixture, 'LossProxy', return_value=proxy))
            patches.enter_context(mock.patch.object(fixture, 'Engine', Engine))
            patches.enter_context(mock.patch.object(fixture, 'ready'))
            patches.enter_context(mock.patch.object(fixture, 'rejected'))
            patches.enter_context(mock.patch.object(fixture, 'echo'))
            patches.enter_context(mock.patch.object(fixture, 'unused_port', return_value=12347))
            patches.enter_context(mock.patch.object(fixture, 'reserved_tcp_ports',
                return_value=contextlib.nullcontext([12348, 12349, 12350])))
            patches.enter_context(mock.patch.object(fixture, 'restart_recovery_check',
                side_effect=primary))
            patches.enter_context(contextlib.redirect_stderr(output))
            with self.assertRaises(fixture.RestartRecoveryError) as raised:
                fixture.scenario(Path('unused-test-binary'), 'kcp', False,
                                 target_factory=lambda: target)
        self.assertIs(raised.exception, primary)
        self.assertEqual(order, ['client_close', 'server_close', 'proxy_close', 'target_close'])
        self.assertEqual(proxy.snapshot_order, order)
        parsed = self.diagnostics(output.getvalue())
        self.assertEqual(parsed['RELIABLE_UDP_CLEANUP_DIAGNOSTIC']['failure_phases'],
                         ['proxy_close', 'target_close'])
        self.assertEqual(parsed['RELIABLE_UDP_DIAGNOSTIC']['stage'],
                         'engine_reconnect_old_stream_exit')
        self.assertNotIn('secret-', output.getvalue())


if __name__ == '__main__':
    unittest.main()
