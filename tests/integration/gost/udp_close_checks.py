"""Bounded, isolated TCP close probes for the GOST source-pair experiment.

The legacy F result is independent from added F+EOF/H/S outcomes. No payload,
correlation digest, credentials, exception text, or engine logs are exported.
H waits for client FIN before replying. S sends FIN before accepting an ACK;
success requires the target application to observe that ACK and client FIN.
"""
import contextlib
import hashlib
import secrets
import socket
import socketserver
import statistics
import struct
import threading
import time
import weakref


TIMEOUT_SECONDS = 2.0
LOSS_PAYLOAD_BYTES = 65536
LOSS_DROP_INTERVAL = 23
_COUNTER_FIELDS = ('datagrams_seen', 'datagrams_dropped', 'datagrams_forwarded',
                   'udp_payload_bytes_seen', 'udp_payload_bytes_dropped',
                   'udp_payload_bytes_forwarded')
_TARGETS = weakref.WeakKeyDictionary()


class _ProbeFailure(Exception):
    pass


def _remaining(deadline):
    value = deadline - time.monotonic()
    if value <= 0:
        raise TimeoutError()
    return value


def _exact(stream, length, deadline):
    parts = []
    remaining = length
    while remaining:
        stream.settimeout(_remaining(deadline))
        part = stream.recv(min(remaining, 65536))
        if not part:
            raise _ProbeFailure('data_incomplete')
        parts.append(part)
        remaining -= len(part)
    return b''.join(parts)


def _eof(stream, deadline):
    # A loop, rather than a single opportunistic read, waits for an actual FIN.
    while True:
        stream.settimeout(_remaining(deadline))
        part = stream.recv(4096)
        if not part:
            return
        raise _ProbeFailure('unexpected_trailing_data')


def target_factory(module):
    """Return a Target subclass preserving the original E/F/B handler."""

    class ExtendedHandler(module.TargetHandler):
        def handle(self):
            target = self.server
            stream = self.request
            worker = threading.current_thread()
            with target.lock:
                target.connections.add(stream)
            with target.worker_condition:
                target.workers.add(worker)
            try:
                stream.settimeout(module.DEADLINE_SECONDS)
                operation = stream.recv(1, socket.MSG_PEEK)
                if operation not in (b'H', b'S'):
                    # MSG_PEEK leaves the exact original E/F/B byte stream.
                    return super().handle()
                deadline = time.monotonic() + TIMEOUT_SECONDS
                if _exact(stream, 1, deadline) != operation:
                    raise _ProbeFailure('operation_changed')
                length = struct.unpack('!I', _exact(stream, 4, deadline))[0]
                if not 16 <= length <= 65536:
                    raise _ProbeFailure('invalid_fixture_length')
                payload = _exact(stream, length, deadline)
                digest = hashlib.sha256(payload).digest()
                with target.lock:
                    target.records.append(payload)
                if operation == b'H':
                    _eof(stream, deadline)
                    # The response is produced only after the target sees FIN.
                    stream.settimeout(_remaining(deadline))
                    stream.sendall(payload)
                    stream.shutdown(socket.SHUT_WR)
                else:
                    with target.lock:
                        observation = target.ack_observations.get(digest)
                    if observation is None:
                        raise _ProbeFailure('ack_observation_missing')
                    stream.settimeout(_remaining(deadline))
                    stream.sendall(payload)
                    stream.shutdown(socket.SHUT_WR)
                    ack = _exact(stream, len(digest), deadline)
                    if ack != digest:
                        raise _ProbeFailure('ack_mismatch')
                    _eof(stream, deadline)
                    observation.set()
            except Exception as error:
                # Fixed type labels only; an exception can contain secrets.
                category = ('close_probe_contract_error'
                            if isinstance(error, _ProbeFailure)
                            else 'close_probe_timeout'
                            if isinstance(error, (TimeoutError, socket.timeout))
                            else 'close_probe_socket_error'
                            if isinstance(error, OSError)
                            else 'close_probe_internal_error')
                with target.lock:
                    target.errors.append(category)
            finally:
                with contextlib.suppress(OSError):
                    stream.shutdown(socket.SHUT_RDWR)
                stream.close()
                with target.lock:
                    target.connections.discard(stream)
                with target.worker_condition:
                    target.workers.discard(worker)
                    target.worker_condition.notify_all()

    class ExtendedTarget(module.Target):
        def __init__(self):
            # Initialize before publishing/starting the listener.
            socketserver.ThreadingTCPServer.__init__(
                self, (module.HOST, 0), ExtendedHandler)
            self.download = secrets.token_bytes(module.BULK_BYTES)
            self.records = []
            self.errors = []
            self.connections = set()
            self.lock = threading.Lock()
            self.ack_observations = {}
            self.worker_condition = threading.Condition()
            self.workers = set()
            _TARGETS[module] = weakref.ref(self)
            self.thread = threading.Thread(target=self.serve_forever, daemon=True)
            self.thread.start()

        def close(self):
            super().close()
            deadline = time.monotonic() + 5
            with self.worker_condition:
                while self.workers:
                    remaining = deadline - time.monotonic()
                    module.require(remaining > 0, 'close probe workers failed to stop')
                    self.worker_condition.wait(remaining)
            current = _TARGETS.get(module)
            if current is not None and current() is self:
                del _TARGETS[module]

    return ExtendedTarget


def _counter_delta(before, after):
    delta = {}
    for field in _COUNTER_FIELDS:
        delta[field] = {}
        for direction in ('out', 'back'):
            value = after[field][direction] - before[field][direction]
            if value < 0:
                raise _ProbeFailure('udp_counter_reset_during_probe')
            delta[field][direction] = value
    return delta


def _run_group(module, port, operation, attempts, target, *, payload_bytes=64,
               proxy=None):
    categories = {}
    latencies = []
    attempt_results = []
    complete = 0
    for ordinal in range(attempts):
        before = proxy.snapshot() if proxy is not None else None
        started = time.monotonic()
        deadline = started + TIMEOUT_SECONDS
        phase = 'connect'
        failure = None
        payload = secrets.token_bytes(payload_bytes)
        digest = hashlib.sha256(payload).digest()
        observation = threading.Event() if operation == b'S' else None
        if observation is not None:
            with target.lock:
                target.ack_observations[digest] = observation
        try:
            with socket.create_connection((module.HOST, port),
                                          timeout=_remaining(deadline)) as stream:
                phase = 'send'
                stream.settimeout(_remaining(deadline))
                stream.sendall(operation + struct.pack('!I', len(payload)) + payload)
                if operation == b'H':
                    phase = 'client_half_close'
                    stream.shutdown(socket.SHUT_WR)
                phase = 'reply'
                reply = _exact(stream, len(payload), deadline)
                if hashlib.sha256(reply).digest() != digest:
                    raise _ProbeFailure('reply_mismatch')
                phase = 'eof'
                _eof(stream, deadline)
                if operation == b'S':
                    phase = 'ack_send'
                    stream.settimeout(_remaining(deadline))
                    stream.sendall(digest)
                    stream.shutdown(socket.SHUT_WR)
                    phase = 'target_ack'
                    if not observation.wait(_remaining(deadline)):
                        raise _ProbeFailure('target_ack_not_observed')
                complete += 1
        except _ProbeFailure as error:
            failure = error.args[0]
        except (TimeoutError, socket.timeout):
            failure = phase + '_timeout'
        except ConnectionResetError:
            failure = 'connection_reset'
        except BrokenPipeError:
            failure = 'broken_pipe'
        except OSError:
            failure = 'socket_error'
        except Exception:
            failure = 'internal_error'
        finally:
            elapsed = time.monotonic() - started
            latencies.append(round(elapsed * 1000, 3))
            if failure is not None:
                categories[failure] = categories.get(failure, 0) + 1
            if observation is not None:
                with target.lock:
                    target.ack_observations.pop(digest, None)
            if proxy is not None:
                delta = _counter_delta(before, proxy.snapshot())
                attempt_results.append({
                    'ordinal': ordinal, 'complete': failure is None,
                    'failure_category': failure, 'last_phase': phase,
                    'sample_window_seconds': round(elapsed, 6),
                    'udp_counter_delta': delta,
                    'loss_observed_both_directions': all(
                        delta['datagrams_dropped'][direction] > 0
                        for direction in ('out', 'back')),
                })
    result = {
        'attempts': attempts,
        'complete': complete,
        'failed': attempts - complete,
        'all_complete': complete == attempts,
        'failure_categories': categories,
        'timeout_seconds_per_attempt': TIMEOUT_SECONDS,
        'latency_ms': {'minimum': min(latencies),
                       'median': round(statistics.median(latencies), 3),
                       'maximum': max(latencies)},
    }
    if proxy is not None:
        result['payload_bytes_per_attempt'] = payload_bytes
        result['attempt_results'] = attempt_results
    return result


def closing_checks(module, port):
    """Run independent legacy F and bounded F+EOF/H/S probes."""
    reference = _TARGETS.get(module)
    target = reference() if reference is not None else None

    def group(operation, attempts):
        if target is None:
            return {'attempts': attempts, 'complete': 0, 'failed': attempts,
                    'all_complete': False,
                    'failure_categories': {'fixture_unavailable': attempts}}
        try:
            return _run_group(module, port, operation, attempts, target)
        except Exception:
            return {'attempts': attempts, 'complete': 0, 'failed': attempts,
                    'all_complete': False,
                    'failure_categories': {'probe_internal_error': attempts}}

    try:
        legacy = module.final_frame_close_check(port, attempts=20)
    except Exception:
        legacy = {'attempts': 20, 'complete_replies': 0, 'failed_replies': 20,
                  'all_complete': False,
                  'failure_categories': {'legacy_probe_internal_error': 20}}
    result = {
        'legacy_f': legacy,
        'f_with_eof': group(b'F', 5),
        'client_half_close_h': group(b'H', 3),
        'server_half_close_s': group(b'S', 3),
        'scope': 'independent application TCP connections over the same tunnel; '
                 'no injected loss; outside bulk CPU and UDP counter windows',
        'h_contract': 'client FIN reaches target before response payload and FIN',
        's_contract': 'target FIN reaches client before client ACK; target application '
                      'must observe ACK and client FIN',
    }
    result['extended_all_complete'] = all(
        result[name]['all_complete']
        for name in ('f_with_eof', 'client_half_close_h', 'server_half_close_s'))
    return result


def lossy_closing_checks(module, port, proxy):
    """Add true-loss coverage without replacing any original close gate."""
    reference = _TARGETS.get(module)
    target = reference() if reference is not None else None
    groups = (('f_with_eof', b'F', 5),
              ('client_half_close_h', b'H', 3),
              ('server_half_close_s', b'S', 3))
    result = {
        'drop_every_nth_datagram': LOSS_DROP_INTERVAL,
        'payload_bytes_per_attempt': LOSS_PAYLOAD_BYTES,
        'timeout_seconds_per_attempt': TIMEOUT_SECONDS,
        'scope': 'separate 64 KiB application close probes over the same tunnel; '
                 'periodic UDP loss active in each recorded window; '
                 'outside bulk CPU and UDP counter windows',
        'counter_scope': 'proxy cumulative counter deltas; includes protocol, '
                         'retransmission and any residual shared-tunnel traffic; '
                         'does not identify a FIN-carrying datagram',
        'coverage_contract': 'every operation group must observe actual dropped '
                             'datagrams in both directions; enabling a policy '
                             'alone is insufficient',
        'policy_changes_reset_counters': False,
    }
    try:
        for name, operation, attempts in groups:
            # Let the previous target handler finish before changing policy.
            # This cannot prove that an engine has no residual control packets.
            idle = target is not None
            if target is not None:
                idle_deadline = time.monotonic() + 5
                with target.worker_condition:
                    while target.workers:
                        remaining = idle_deadline - time.monotonic()
                        if remaining <= 0:
                            idle = False
                            break
                        target.worker_condition.wait(remaining)
            proxy.set_loss_policy(LOSS_DROP_INTERVAL)
            before = proxy.snapshot()
            started = time.monotonic()
            try:
                if not idle:
                    group = {'attempts': attempts, 'complete': 0,
                             'failed': attempts, 'all_complete': False,
                             'failure_categories': {
                                 'fixture_unavailable' if target is None
                                 else 'fixture_workers_not_idle': attempts},
                             'attempt_results': []}
                else:
                    group = _run_group(module, port, operation, attempts, target,
                                       payload_bytes=LOSS_PAYLOAD_BYTES, proxy=proxy)
            except Exception:
                group = {'attempts': attempts, 'complete': 0, 'failed': attempts,
                         'all_complete': False, 'attempt_results': [],
                         'failure_categories': {'probe_internal_error': attempts}}
            after = proxy.snapshot()
            elapsed = time.monotonic() - started
            delta = _counter_delta(before, after)
            loss_coverage = all(delta['datagrams_dropped'][direction] > 0
                                for direction in ('out', 'back'))
            group.update({
                'payload_bytes_per_attempt': LOSS_PAYLOAD_BYTES,
                'timeout_seconds_per_attempt': TIMEOUT_SECONDS,
                'udp_window': {
                    'sample_window_seconds': round(elapsed, 6),
                    'before': before, 'after': after, 'counter_delta': delta,
                    'drop_every_nth_datagram': LOSS_DROP_INTERVAL,
                },
                'loss_observed_both_directions': loss_coverage,
                'all_checks_passed': group['all_complete'] and loss_coverage,
            })
            result[name] = group
            # Do not reset, otherwise the final suite counters and diagnostics
            # would silently lose a completed group's observations.
            proxy.set_loss_policy(0)
    finally:
        proxy.set_loss_policy(0)
    result['delivery_all_complete'] = all(
        result[name]['all_complete'] for name, _, _ in groups)
    result['loss_coverage_all_complete'] = all(
        result[name]['loss_observed_both_directions'] for name, _, _ in groups)
    result['all_checks_passed'] = (result['delivery_all_complete'] and
                                  result['loss_coverage_all_complete'])
    return result
