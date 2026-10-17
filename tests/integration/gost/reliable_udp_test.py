#!/usr/bin/env python3
"""Isolated real-engine relay/QUIC and relay/KCP delivery acceptance.

Fetch the pinned engines with deploy/fetch-node-engines.py first, then set
NYVP_GOST_BINARY to its absolute bin/gost path and explicitly authorize this
test with HL_PANEL_ISOLATED_RELIABLE_UDP_TEST=true. Requires Linux and openssl.
All TCP/UDP endpoints are 127.0.0.1; packet loss is injected in this process,
never with tc, sysctl, firewall, interfaces, routes, or production nodes.
This proves bounded delivery cases, not zero underlying loss or CPU superiority.
UDP byte counts are payload bytes observed at the local proxy, excluding IP/UDP
headers and counting protocol overhead/retransmissions. CPU seconds are sampled
from each GOST process during the bulk window, excluding Python fixture CPU.
Temporary credentials/configuration/logs are never printed and are removed.
"""
import contextlib
import copy
import hashlib
import importlib.util
import json
import os
from pathlib import Path
import secrets
import selectors
import shutil
import socket
import socketserver
import struct
import subprocess
import sys
import tempfile
import threading
import time


HOST = '127.0.0.1'
BULK_BYTES = 2 * 1024 * 1024
DEADLINE_SECONDS = 45
ENGINE_RESTART_FAILURE_SECONDS = 15
DIAGNOSTIC_LOG_BYTES = 32768
RESTART_PHASES = frozenset(('pre_stop_stream', 'server_stop', 'old_stream_exit',
                            'replacement_start', 'identity', 'replacement_readiness',
                            'replacement_bulk', 'target_stream_closed'))
DIAGNOSTIC_STAGES = frozenset(('fixture_setup', 'server_startup', 'client_startup',
                              'client_fixture_setup',
                              'authenticated_readiness', 'authorization_checks',
                              'engine_reconnect', 'bulk_transfer', 'transfer_validation',
                              'final_frame_close_check', 'lossy_close_checks'))
DIAGNOSTIC_MARKERS = (
    ('unregistered_transport_observed',
     (b'unregistered listener', b'unregistered dialer', b'unknown listener',
      b'unknown dialer', b'listener not registered', b'dialer not registered')),
    ('address_in_use_observed', (b'address already in use',)),
    ('configuration_error_observed',
     (b'cannot unmarshal', b'invalid character', b'unknown field',
      b'failed to load config', b'failed to parse config')),
    ('tls_certificate_error_observed',
     (b'x509:', b'bad certificate', b'certificate verify failed')),
    ('authentication_rejection_observed',
     (b'authentication failed', b'authentication required', b'unauthorized')),
    ('handshake_timeout_observed',
     (b'handshake timeout', b'handshake did not complete',
      b'timeout: no recent network activity')),
    ('connection_refused_observed', (b'connection refused',)),
    ('permission_denied_observed', (b'permission denied',)),
)


class FixturePortAllocationError(RuntimeError):
    """A client fixture could not reserve its distinct loopback TCP ports."""


class RestartRecoveryError(RuntimeError):
    def __init__(self, phase):
        self.phase = phase if phase in RESTART_PHASES else 'unknown_restart_phase'
        super().__init__('restart_' + self.phase + '_failed')


@contextlib.contextmanager
def restart_phase(phase):
    try:
        yield
    except Exception as error:
        raise RestartRecoveryError(phase) from error


def diagnose_failure(directory, stage, exc_type, _error, _traceback,
                     proxy=None, target=None):
    """Emit fixed classifications only; never expose log text or exception data."""
    if exc_type is None:
        return False
    safe_stage = stage if stage in DIAGNOSTIC_STAGES else 'unknown_stage'
    if isinstance(_error, RestartRecoveryError):
        safe_stage = 'engine_reconnect_' + _error.phase
    try:
        engines = []
        for name in ('server', 'client'):
            try:
                with (directory / (name + '.log')).open('rb') as log:
                    log.seek(0, os.SEEK_END)
                    log.seek(max(0, log.tell() - DIAGNOSTIC_LOG_BYTES))
                    tail = log.read(DIAGNOSTIC_LOG_BYTES).lower()
                categories = [category for category, markers in DIAGNOSTIC_MARKERS
                              if any(marker in tail for marker in markers)]
                if not categories:
                    categories = ['no_known_error_marker']
            except OSError:
                categories = ['engine_log_unavailable']
            engines.append({'engine': name, 'categories': categories})
        diagnostic = {'stage': safe_stage, 'engines': engines,
                      'failure_category': ('client_port_reservation_failed'
                          if isinstance(_error, FixturePortAllocationError)
                          else 'restart_' + _error.phase + '_failed'
                          if isinstance(_error, RestartRecoveryError)
                          else 'unclassified_failure'),
                      'scope': 'known markers observed in bounded log tails; '
                               'expected authorization rejections may be included'}
        if proxy is not None:
            diagnostic['udp'] = proxy.snapshot()
            diagnostic['proxy_error_count'] = len(proxy.errors)
        if target is not None:
            with target.lock:
                diagnostic['target_record_count'] = len(target.records)
                diagnostic['target_error_count'] = len(target.errors)
    except Exception:
        diagnostic = {'stage': safe_stage, 'categories': ['diagnostic_unavailable']}
    # Even a diagnostic output failure must preserve the original exception.
    with contextlib.suppress(Exception):
        print('RELIABLE_UDP_DIAGNOSTIC ' + json.dumps(diagnostic, sort_keys=True),
              file=sys.stderr, flush=True)
    return False


def require(condition, message):
    # Explicit checks remain effective under python -O.
    if not condition:
        raise RuntimeError(message)


def receive(stream, length, deadline):
    chunks = []
    remaining = length
    while remaining:
        timeout = deadline - time.monotonic()
        require(timeout > 0, 'delivery exceeded its bounded deadline')
        stream.settimeout(timeout)
        part = stream.recv(min(remaining, 65536))
        require(bool(part), 'stream ended before all expected bytes arrived')
        chunks.append(part)
        remaining -= len(part)
    return b''.join(chunks)


def unused_port(kind):
    with socket.socket(socket.AF_INET, kind) as stream:
        stream.bind((HOST, 0))
        return stream.getsockname()[1]


@contextlib.contextmanager
def reserved_tcp_ports(count):
    """Keep distinct client ports bound until their configuration is complete."""
    with contextlib.ExitStack() as cleanup:
        ports = []
        for _ in range(count):
            try:
                stream = socket.socket(socket.AF_INET, socket.SOCK_STREAM)
                cleanup.callback(stream.close)
                stream.bind((HOST, 0))
                port = stream.getsockname()[1]
            except OSError as error:
                raise FixturePortAllocationError() from error
            if port in ports:
                raise FixturePortAllocationError()
            ports.append(port)
        yield ports


class Target(socketserver.ThreadingTCPServer):
    allow_reuse_address = False
    daemon_threads = True

    def __init__(self):
        super().__init__((HOST, 0), TargetHandler)
        self.download = os.urandom(BULK_BYTES)
        self.records = []
        self.errors = []
        self.connections = set()
        self.lock = threading.Lock()
        self.persistent_records = []
        self.persistent_ready = threading.Event()
        self.persistent_closed = threading.Event()
        self.thread = threading.Thread(target=self.serve_forever, daemon=True)
        self.thread.start()

    def close(self):
        self.shutdown()
        with self.lock:
            for connection in tuple(self.connections):
                with contextlib.suppress(OSError):
                    connection.shutdown(socket.SHUT_RDWR)
                connection.close()
        self.server_close()
        self.thread.join(5)
        require(not self.thread.is_alive(), 'target listener failed to stop')


class TargetHandler(socketserver.BaseRequestHandler):
    def handle(self):
        target = self.server
        stream = self.request
        with target.lock:
            target.connections.add(stream)
        sender = None
        send_errors = []
        try:
            deadline = time.monotonic() + DEADLINE_SECONDS
            operation = receive(stream, 1, deadline)
            if operation in (b'E', b'F'):
                length = struct.unpack('!I', receive(stream, 4, deadline))[0]
                require(length <= 65536, 'echo length exceeds fixture limit')
                payload = receive(stream, length, deadline)
                with target.lock:
                    target.records.append(payload)
                stream.sendall(payload)
                if operation == b'E':
                    # Keep the stable-stream probe independent of immediate
                    # server-close behavior. F exercises that boundary below.
                    stream.settimeout(5)
                    require(stream.recv(1) == b'', 'unexpected trailing echo data')
            elif operation == b'B':
                length = struct.unpack('!I', receive(stream, 4, deadline))[0]
                require(length == BULK_BYTES, 'bulk length differs from fixture')

                def send_download():
                    try:
                        stream.sendall(target.download)
                    except OSError as error:
                        send_errors.append(type(error).__name__)

                sender = threading.Thread(target=send_download, daemon=True)
                sender.start()
                upload = receive(stream, length, deadline)
                sender.join(max(0, deadline - time.monotonic()))
                require(not sender.is_alive() and not send_errors,
                        'target could not send its independent bulk payload')
                stream.sendall(struct.pack('!I', len(upload)) +
                               hashlib.sha256(upload).digest())
                stream.settimeout(5)
                require(stream.recv(1) == b'', 'unexpected trailing bulk data')
            elif operation == b'P':
                # Keep one application connection open while the server engine is
                # terminated.  This is deliberately separate from E/F/B so a
                # successful echo cannot be mistaken for a surviving tunnel.
                length = struct.unpack('!I', receive(stream, 4, deadline))[0]
                require(16 <= length <= 65536, 'persistent payload exceeds fixture limit')
                payload = receive(stream, length, deadline)
                with target.lock:
                    target.persistent_records.append(payload)
                stream.sendall(payload)
                target.persistent_ready.set()
                stream.settimeout(DEADLINE_SECONDS)
                while True:
                    if not stream.recv(4096):
                        target.persistent_closed.set()
                        break
            else:
                raise RuntimeError('unrecognized fixture operation')
        except Exception as error:
            with target.lock:
                target.errors.append(type(error).__name__)
        finally:
            with contextlib.suppress(OSError):
                stream.shutdown(socket.SHUT_RDWR)
            stream.close()
            if sender is not None:
                sender.join(2)
            with target.lock:
                target.connections.discard(stream)


class LossProxy:
    """NAT-like local UDP forwarder; one upstream socket per client."""

    def __init__(self, target_port):
        self.target = (HOST, target_port)
        self.front = socket.socket(socket.AF_INET, socket.SOCK_DGRAM)
        self.front.bind((HOST, 0))
        self.address = self.front.getsockname()
        self.selector = selectors.DefaultSelector()
        self.selector.register(self.front, selectors.EVENT_READ, None)
        self.upstreams = {}
        self.stop = threading.Event()
        self.lock = threading.Lock()
        self.drop_every = 0
        self.seen = {'out': 0, 'back': 0}
        self.dropped = {'out': 0, 'back': 0}
        self.forwarded = {'out': 0, 'back': 0}
        self.send_failed = {'out': 0, 'back': 0}
        self.seen_bytes = {'out': 0, 'back': 0}
        self.dropped_bytes = {'out': 0, 'back': 0}
        self.forwarded_bytes = {'out': 0, 'back': 0}
        self.send_failed_bytes = {'out': 0, 'back': 0}
        self.refused = {'out_send': 0, 'upstream_receive': 0}
        self.errors = []
        self.thread = threading.Thread(target=self.run, daemon=True)
        self.thread.start()

    def enable_loss(self, every):
        with self.lock:
            self.seen = {'out': 0, 'back': 0}
            self.dropped = {'out': 0, 'back': 0}
            self.forwarded = {'out': 0, 'back': 0}
            self.send_failed = {'out': 0, 'back': 0}
            self.seen_bytes = {'out': 0, 'back': 0}
            self.dropped_bytes = {'out': 0, 'back': 0}
            self.forwarded_bytes = {'out': 0, 'back': 0}
            self.send_failed_bytes = {'out': 0, 'back': 0}
            self.refused = {'out_send': 0, 'upstream_receive': 0}
            self.drop_every = every

    def set_loss_policy(self, every):
        """Change injection without erasing any already recorded counters."""
        require(isinstance(every, int) and every >= 0,
                'loss policy must be a non-negative datagram interval')
        with self.lock:
            self.drop_every = every

    def forward(self, direction, packet, send):
        # Count one observed datagram once, not both legs of the local proxy.
        # Keep reset, drop choice, successful send and counters under one lock.
        with self.lock:
            self.seen[direction] += 1
            self.seen_bytes[direction] += len(packet)
            drop = bool(self.drop_every and
                        self.seen[direction] % self.drop_every == 0)
            if drop:
                self.dropped[direction] += 1
                self.dropped_bytes[direction] += len(packet)
                return
            try:
                require(send(packet) == len(packet), 'UDP payload was only partly sent')
            except Exception as error:
                self.send_failed[direction] += 1
                self.send_failed_bytes[direction] += len(packet)
                if direction == 'out' and isinstance(error, ConnectionRefusedError):
                    # A connected UDP socket can surface a pending ICMP refusal
                    # at send as well as recv while the engine is unavailable.
                    # Record this loss separately from intentional packet drops.
                    self.refused['out_send'] += 1
                    return False
                raise
            self.forwarded[direction] += 1
            self.forwarded_bytes[direction] += len(packet)
            return True

    def run(self):
        try:
            while not self.stop.is_set():
                for key, _ in self.selector.select(0.1):
                    if key.fileobj is self.front:
                        packet, client = self.front.recvfrom(65535)
                        require(client[0] == HOST, 'non-loopback UDP sender')
                        if client not in self.upstreams:
                            upstream = socket.socket(socket.AF_INET, socket.SOCK_DGRAM)
                            upstream.bind((HOST, 0))
                            upstream.connect(self.target)
                            self.upstreams[client] = upstream
                            self.selector.register(upstream, selectors.EVENT_READ, client)
                        self.forward('out', packet, self.upstreams[client].send)
                    else:
                        try:
                            packet = key.fileobj.recv(65535)
                        except ConnectionRefusedError:
                            # The engine may be starting or deliberately stopped.
                            with self.lock:
                                self.refused['upstream_receive'] += 1
                            continue
                        self.forward('back', packet,
                                     lambda data: self.front.sendto(data, key.data))
        except Exception as error:
            self.errors.append(type(error).__name__)
        finally:
            for upstream in self.upstreams.values():
                upstream.close()
            self.selector.close()
            self.front.close()

    def snapshot(self):
        with self.lock:
            for direction in ('out', 'back'):
                require(self.seen[direction] == self.dropped[direction] +
                        self.forwarded[direction] + self.send_failed[direction],
                        'UDP datagram counters differ')
                require(self.seen_bytes[direction] == self.dropped_bytes[direction] +
                        self.forwarded_bytes[direction] + self.send_failed_bytes[direction],
                        'UDP payload byte counters differ')
            return {'datagrams_seen': dict(self.seen),
                    'datagrams_dropped': dict(self.dropped),
                    'datagrams_forwarded': dict(self.forwarded),
                    'datagrams_send_failed': dict(self.send_failed),
                    'udp_payload_bytes_seen': dict(self.seen_bytes),
                    'udp_payload_bytes_dropped': dict(self.dropped_bytes),
                    'udp_payload_bytes_forwarded': dict(self.forwarded_bytes),
                    'udp_payload_bytes_send_failed': dict(self.send_failed_bytes),
                    'connection_refused_events': dict(self.refused),
                    'udp_payload_byte_scope': 'one observation at local proxy; '
                        'excludes IP/UDP headers; includes protocol overhead and retransmissions',
                    'udp_directions': {'out': 'entry to exit', 'back': 'exit to entry'}}

    def close(self):
        self.stop.set()
        self.thread.join(5)
        require(not self.thread.is_alive(), 'local UDP forwarder failed to stop')
        require(not self.errors, 'local UDP forwarder failed')


class Engine:
    def __init__(self, binary, directory, name, configuration):
        self.binary = Path(binary)
        self.directory = Path(directory)
        self.name = name
        config_path = directory / (name + '.json')
        self.config_path = config_path
        config_path.write_text(json.dumps(configuration), encoding='utf-8')
        config_path.chmod(0o600)
        self.log_path = directory / (name + '.log')
        self.log = None
        self.process = None
        self._start()

    def _start(self):
        self.log = self.log_path.open('ab')
        try:
            self.process = subprocess.Popen([str(self.binary), '-C', str(self.config_path)],
                                            cwd=self.directory, stdin=subprocess.DEVNULL,
                                            stdout=self.log, stderr=subprocess.STDOUT)
        except Exception:
            self.log.close()
            self.log = None
            raise

    def stop(self):
        """Stop one real engine and wait for its process identity to end."""
        process = self.process
        if process is None:
            return
        try:
            if process.poll() is None:
                process.terminate()
                try:
                    process.wait(timeout=5)
                except subprocess.TimeoutExpired:
                    process.kill()
                    process.wait(timeout=5)
            require(process.poll() is not None, 'GOST process failed to stop')
        finally:
            if self.log is not None:
                self.log.close()
                self.log = None

    def restart(self):
        """Restart this engine from the same config, binary, and bound ports."""
        self.stop()
        self._start()
        self.alive()

    def alive(self):
        require(self.process.poll() is None, 'test GOST process exited unexpectedly')

    def cpu_ticks(self):
        self.alive()
        fields = (Path('/proc') / str(self.process.pid) / 'stat').read_text().rsplit(')', 1)
        require(len(fields) == 2, 'cannot parse GOST process CPU statistics')
        # comm may contain spaces or parentheses. Remaining fields start at 3
        # (state): indices 11/12 are utime/stime; 19 is starttime/PID identity.
        values = fields[1].split()
        require(len(values) > 19, 'incomplete GOST process CPU statistics')
        ticks = {'user': int(values[11]), 'system': int(values[12]),
                 'starttime': int(values[19])}
        self.alive()
        return ticks

    def close(self):
        self.stop()


def echo(port, payload, timeout=5, operation=b'E'):
    deadline = time.monotonic() + timeout
    with socket.create_connection((HOST, port), timeout=timeout) as stream:
        stream.sendall(operation + struct.pack('!I', len(payload)) + payload)
        require(receive(stream, len(payload), deadline) == payload, 'echo differs')


def final_frame_close_check(port, attempts=20):
    """Do not hide delivery failures when the target closes after its reply."""
    complete = 0
    for _ in range(attempts):
        try:
            echo(port, secrets.token_bytes(64), timeout=2, operation=b'F')
            complete += 1
        except (OSError, RuntimeError):
            pass
    return {'attempts': attempts, 'complete_replies': complete,
            'failed_replies': attempts - complete,
            'all_complete': complete == attempts,
            'scope': 'new application TCP connections after bulk, with no injected loss; '
                     'the underlying UDP tunnel may be reused; '
                     'outside reported CPU and UDP counter windows'}


def ready(port, engines):
    deadline = time.monotonic() + 15
    while time.monotonic() < deadline:
        for engine in engines:
            engine.alive()
        try:
            echo(port, b'reliable-udp-ready', timeout=2)
            return
        except (OSError, RuntimeError):
            time.sleep(0.1)
    raise RuntimeError('authenticated relay did not become ready')


def rejected(port, target):
    marker = b'denied-' + secrets.token_bytes(24)
    # A timeout is inconclusive and must fail instead of passing as rejection.
    with socket.create_connection((HOST, port), timeout=5) as stream:
        stream.sendall(b'E' + struct.pack('!I', len(marker)) + marker)
        try:
            data = stream.recv(1)
        except (ConnectionResetError, BrokenPipeError):
            data = b''
        require(data == b'', 'unauthorized relay forwarded application data')
    with target.lock:
        require(marker not in target.records, 'unauthorized request reached target')


def bulk(port, target):
    upload = os.urandom(BULK_BYTES)
    deadline = time.monotonic() + DEADLINE_SECONDS
    errors = []
    with socket.create_connection((HOST, port), timeout=5) as stream:
        stream.settimeout(DEADLINE_SECONDS)
        stream.sendall(b'B' + struct.pack('!I', len(upload)))

        def send_upload():
            try:
                stream.sendall(upload)
            except OSError as error:
                errors.append(type(error).__name__)

        sender = threading.Thread(target=send_upload, daemon=True)
        started = time.monotonic()
        sender.start()
        try:
            download = receive(stream, BULK_BYTES, deadline)
            acknowledgement = receive(stream, 36, deadline)
            sender.join(max(0, deadline - time.monotonic()))
            require(not sender.is_alive() and not errors, 'bulk upload failed')
            uploaded = struct.unpack('!I', acknowledgement[:4])[0]
            require(uploaded == len(upload), 'upload byte count differs')
            require(acknowledgement[4:] == hashlib.sha256(upload).digest(),
                    'upload SHA256 differs')
            require(hashlib.sha256(download).digest() ==
                    hashlib.sha256(target.download).digest(), 'download SHA256 differs')
            return {'upload_bytes': uploaded, 'download_bytes': len(download),
                    'upload_sha256': hashlib.sha256(upload).hexdigest(),
                    'download_sha256': hashlib.sha256(download).hexdigest(),
                    'elapsed_seconds': round(time.monotonic() - started, 3)}
        finally:
            with contextlib.suppress(OSError):
                stream.shutdown(socket.SHUT_RDWR)
            sender.join(2)


def restart_recovery_check(server, client, port, target):
    """Require real server process replacement and a fresh post-restart flow."""
    payload = secrets.token_bytes(64)
    with restart_phase('pre_stop_stream'):
        stream = socket.create_connection((HOST, port), timeout=5)
        stream.settimeout(5)
    try:
        with restart_phase('pre_stop_stream'):
            stream.sendall(b'P' + struct.pack('!I', len(payload)) + payload)
            require(receive(stream, len(payload), time.monotonic() + 8) == payload,
                    'persistent pre-restart echo differs')
            require(target.persistent_ready.wait(5),
                    'target did not observe persistent application stream')
            require(not target.persistent_closed.is_set(),
                    'persistent application stream closed before server stop')
            # Echo alone cannot establish that the application flow is open.
            pre_stop_probe_started = time.monotonic()
            stream.settimeout(0.1)
            try:
                stream.recv(1, socket.MSG_PEEK)
            except socket.timeout:
                pass
            else:
                raise RuntimeError('persistent stream was not idle and open before server stop')
            require(not target.persistent_closed.is_set(),
                    'persistent application stream closed during pre-stop probe')
            pre_stop_probe_seconds = time.monotonic() - pre_stop_probe_started
        with restart_phase('identity'):
            old_server_pid = server.process.pid
            client_pid = client.process.pid
            client_identity = client.cpu_ticks()['starttime']
        with restart_phase('server_stop'):
            server.stop()
            require(server.process.poll() is not None,
                    'server process did not terminate before replacement')
        with restart_phase('old_stream_exit'):
            old_failure = None
            old_failure_started = time.monotonic()
            deadline = old_failure_started + ENGINE_RESTART_FAILURE_SECONDS
            while time.monotonic() < deadline:
                stream.settimeout(min(0.5, max(0.001, deadline - time.monotonic())))
                try:
                    part = stream.recv(4096)
                    if not part:
                        old_failure = 'eof'
                        break
                except socket.timeout:
                    continue
                except (ConnectionResetError, BrokenPipeError, OSError):
                    old_failure = 'socket_error'
                    break
            old_failure_seconds = time.monotonic() - old_failure_started
            require(old_failure is not None and old_failure_seconds <= ENGINE_RESTART_FAILURE_SECONDS,
                    'old application connection survived server process termination')
    finally:
        with contextlib.suppress(OSError):
            stream.shutdown(socket.SHUT_RDWR)
        stream.close()
    with restart_phase('replacement_start'):
        server.restart()
    with restart_phase('identity'):
        new_server_pid = server.process.pid
        require(new_server_pid != old_server_pid,
                'replacement server reused the terminated process identity')
        require(client.process.pid == client_pid,
                'client GOST process changed during server replacement')
        require(client.cpu_ticks()['starttime'] == client_identity,
                'client GOST process identity changed during server replacement')
    with restart_phase('replacement_readiness'):
        readiness_started = time.monotonic()
        ready(port, (server, client))
        readiness_seconds = time.monotonic() - readiness_started
    with restart_phase('replacement_bulk'):
        replacement = bulk(port, target)
        require(replacement['upload_bytes'] == BULK_BYTES and
                replacement['download_bytes'] == BULK_BYTES,
                'replacement bulk byte count differs')
        require(len(replacement['upload_sha256']) == 64 and
                len(replacement['download_sha256']) == 64,
                'replacement bulk SHA256 was not recorded')
    with restart_phase('target_stream_closed'):
        require(target.persistent_closed.wait(5),
                'target did not observe closure of the pre-restart application stream')
    return {
        'server_pid_before': old_server_pid,
        'server_pid_after': new_server_pid,
        'server_stopped': True,
        'client_pid_before': client_pid,
        'client_pid_after': client.process.pid,
        'client_identity_unchanged': True,
        'old_connection_open_before_stop': True,
        'pre_stop_probe_elapsed_seconds': pre_stop_probe_seconds,
        'old_connection_failed': True,
        'old_connection_failure': old_failure,
        'old_connection_failure_timeout_seconds': ENGINE_RESTART_FAILURE_SECONDS,
        'old_connection_failure_elapsed_seconds': old_failure_seconds,
        'replacement_readiness_elapsed_seconds': readiness_seconds,
        'replacement_bulk_passed': True,
        'replacement_upload_bytes': replacement['upload_bytes'],
        'replacement_download_bytes': replacement['download_bytes'],
        'replacement_upload_sha256': replacement['upload_sha256'],
        'replacement_download_sha256': replacement['download_sha256'],
        'all_checks_passed': True,
        'injected_datagram_loss': False,
        'scope': 'local isolated GOST server process replacement; client process retained; '
                 'old application stream fails and a new stream recovers; no injected loss',
    }


def certificates(directory):
    cert, key = directory / 'cert.pem', directory / 'key.pem'
    subprocess.run(['openssl', 'req', '-x509', '-newkey', 'rsa:2048', '-nodes',
                    '-keyout', str(key), '-out', str(cert), '-days', '1',
                    '-subj', '/CN=localhost', '-addext', 'subjectAltName=DNS:localhost',
                    '-addext', 'basicConstraints=critical,CA:TRUE'],
                   check=True, stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL,
                   timeout=20)
    key.chmod(0o600)
    return cert, key


def scenario(binary, transport, lossy, *, target_factory=None, closing_check=None,
             lossy_closing_check=None):
    with tempfile.TemporaryDirectory(prefix='hl-reliable-udp-') as temporary:
        directory = Path(temporary)
        with contextlib.ExitStack() as cleanup:
            stage = ['fixture_setup']
            observation = {'proxy': None, 'target': None}
            # Register first to diagnose after logs close, before their directory
            # is removed; callbacks never suppress the original failure.
            cleanup.push(lambda exc_type, error, traceback:
                         diagnose_failure(directory, stage[0], exc_type, error,
                                          traceback, **observation))
            target = (target_factory or Target)()
            observation['target'] = target
            cleanup.callback(target.close)
            udp_port = unused_port(socket.SOCK_DGRAM)
            proxy = LossProxy(udp_port)
            observation['proxy'] = proxy
            cleanup.callback(proxy.close)
            auth = {'username': 'isolated-test', 'password': secrets.token_urlsafe(32)}
            listener = {'type': transport}
            dialer = {'type': transport}
            if transport == 'quic':
                cert, key = certificates(directory)
                listener['tls'] = {'certFile': str(cert), 'keyFile': str(key)}
                dialer['tls'] = {'caFile': str(cert), 'secure': True,
                                 'serverName': 'localhost'}
            else:
                # Loopback verification settings; these are not product defaults.
                metadata = {'kcp.key': secrets.token_urlsafe(32), 'kcp.crypt': 'aes',
                            'kcp.mode': 'normal', 'kcp.nocomp': True,
                            'kcp.mtu': 1200, 'kcp.sndwnd': 128, 'kcp.rcvwnd': 128,
                            'kcp.smuxver': 1, 'kcp.smuxbuf': 4194304,
                            'kcp.streambuf': 2097152, 'kcp.keepalive': 10}
                listener['metadata'] = copy.deepcopy(metadata)
                dialer['metadata'] = copy.deepcopy(metadata)
            server_config = {'services': [{'name': 'loopback-relay',
                             'addr': f'{HOST}:{udp_port}', 'listener': listener,
                             'handler': {'type': 'relay', 'auth': auth,
                                         'metadata': {'readTimeout': '5s'}}}]}
            stage[0] = 'server_startup'
            server = Engine(binary, directory, 'server', server_config)
            cleanup.callback(server.close)
            stage[0] = 'client_fixture_setup'
            chains, services = [], []
            names = ('authorized', 'missing-auth', 'wrong-auth')
            with reserved_tcp_ports(len(names)) as ports:
                for name, port in zip(names, ports):
                    connector = {'type': 'relay'}
                    if name == 'authorized':
                        connector['auth'] = auth
                    elif name == 'wrong-auth':
                        connector['auth'] = {'username': auth['username'],
                                             'password': secrets.token_urlsafe(32)}
                    chains.append({'name': name, 'hops': [{'name': name, 'nodes': [{
                        'name': name, 'addr': f'{HOST}:{proxy.address[1]}',
                        'connector': connector, 'dialer': copy.deepcopy(dialer)}]}]})
                    services.append({'name': name, 'addr': f'{HOST}:{port}',
                                     'listener': {'type': 'tcp'},
                                     'handler': {'type': 'tcp', 'chain': name},
                                     'forwarder': {'nodes': [{'name': 'fixture',
                                         'addr': f'{HOST}:{target.server_address[1]}'}]}})
            stage[0] = 'client_startup'
            client = Engine(binary, directory, 'client', {'services': services,
                                                        'chains': chains})
            cleanup.callback(client.close)
            engines = (server, client)
            stage[0] = 'authenticated_readiness'
            ready(ports[0], engines)
            stage[0] = 'authorization_checks'
            rejected(ports[1], target)
            rejected(ports[2], target)
            echo(ports[0], b'authenticated-after-rejections')
            stage[0] = 'engine_reconnect'
            result = {'engine_reconnect': restart_recovery_check(server, client,
                                                                  ports[0], target)}
            # Arm deterministic loss only after readiness and auth checks.
            clock_ticks = os.sysconf('SC_CLK_TCK')
            require(clock_ticks > 0, 'invalid Linux CPU clock tick rate')
            sampled_at = time.monotonic()
            before_cpu = {'server': server.cpu_ticks(), 'client': client.cpu_ticks()}
            proxy.enable_loss(23 if lossy else 0)
            stage[0] = 'bulk_transfer'
            result.update(bulk(ports[0], target))
            stage[0] = 'transfer_validation'
            after_cpu = {'server': server.cpu_ticks(), 'client': client.cpu_ticks()}
            sample_seconds = time.monotonic() - sampled_at
            cpu_seconds = {}
            for name in ('server', 'client'):
                require(before_cpu[name]['starttime'] == after_cpu[name]['starttime'],
                        'GOST process changed during CPU measurement')
                user_ticks = after_cpu[name]['user'] - before_cpu[name]['user']
                system_ticks = after_cpu[name]['system'] - before_cpu[name]['system']
                require(user_ticks >= 0 and system_ticks >= 0, 'CPU ticks decreased')
                cpu_seconds[name] = {'user_seconds': user_ticks / clock_ticks,
                                     'system_seconds': system_ticks / clock_ticks,
                                     'total_seconds': (user_ticks + system_ticks) / clock_ticks}
            result['gost_process_cpu_seconds'] = cpu_seconds
            result['cpu_measurement'] = {
                'source': '/proc/<pid>/stat utime/stime',
                'clock_ticks_per_second': clock_ticks,
                'sample_window_seconds': round(sample_seconds, 6),
                'scope': 'bulk call window including new stream setup and teardown; '
                         'excludes Python proxy/target CPU; per-process samples are sequential',
                'resolution_seconds': 1 / clock_ticks}
            for engine in engines:
                engine.alive()
            counters = proxy.snapshot()
            if lossy:
                require(all(count > 0 for count in
                            counters['datagrams_dropped'].values()),
                        'loss scenario did not drop packets in both directions')
            else:
                require(not any(counters['datagrams_dropped'].values()),
                        'baseline unexpectedly dropped a datagram')
            require(not proxy.errors, 'UDP forwarder failed during transfer')
            result.update(counters)
            result.update({'transport': transport,
                           'drop_every_nth_datagram': 23 if lossy else 0,
                           'missing_auth_rejected': True, 'wrong_auth_rejected': True})
            stage[0] = 'final_frame_close_check'
            proxy.enable_loss(0)
            result['final_frame_close_check'] = final_frame_close_check(ports[0])
            if closing_check is not None:
                result['extended_close_checks'] = closing_check(ports[0])
            if lossy_closing_check is not None:
                stage[0] = 'lossy_close_checks'
                result['lossy_close_checks'] = lossy_closing_check(ports[0], proxy)
            return result


def main():
    require(sys.platform == 'linux', 'requires an explicitly isolated Linux environment')
    require(os.environ.get('HL_PANEL_ISOLATED_RELIABLE_UDP_TEST') == 'true',
            'set HL_PANEL_ISOLATED_RELIABLE_UDP_TEST=true in an isolated environment')
    require(shutil.which('openssl') is not None, 'openssl is required for temporary TLS')
    supplied = os.environ.get('NYVP_GOST_BINARY', '')
    binary = Path(supplied)
    require(bool(supplied) and binary.is_absolute() and binary.is_file(),
            'NYVP_GOST_BINARY must identify an absolute pinned local GOST executable')
    repo = Path(__file__).resolve().parents[3]
    spec = importlib.util.spec_from_file_location('fetch_node_engines',
                                                  repo / 'deploy/fetch-node-engines.py')
    fetch = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(fetch)
    pinned = next(engine for engine in fetch.ENGINES if engine['name'] == 'gost')
    manifest = binary.parent.parent / 'licenses/node-engines.json'
    require(manifest.is_file(), 'fetch pinned engines with deploy/fetch-node-engines.py first')
    engines = json.loads(manifest.read_text(encoding='utf-8'))
    require(pinned in engines, 'engine manifest differs from pinned fetch configuration')
    version = subprocess.run([str(binary), '-V'], check=True, capture_output=True,
                             text=True, timeout=5).stdout
    require(pinned['version'].removeprefix('v') in version,
            'GOST version differs from the pinned fetch configuration')
    results = []
    for transport in ('quic', 'kcp'):
        for lossy in (False, True):
            result = scenario(binary, transport, lossy)
            results.append(result)
            print(json.dumps(result, sort_keys=True), flush=True)
    require(all(result['final_frame_close_check']['all_complete'] for result in results),
            'immediate-target-close replies were incomplete or failed; stable-stream '
            'results do not establish full reliable delivery')
    print('PASS: authenticated QUIC/KCP delivered bounded bidirectional payloads '
          'on loopback, including deterministic injected UDP loss', flush=True)


if __name__ == '__main__':
    main()
