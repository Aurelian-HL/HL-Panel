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
DIAGNOSTIC_LOG_BYTES = 32768
DIAGNOSTIC_STAGES = frozenset(('fixture_setup', 'server_startup', 'client_startup',
                              'authenticated_readiness', 'authorization_checks',
                              'bulk_transfer', 'transfer_validation',
                              'final_frame_close_check'))
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


def diagnose_failure(directory, stage, exc_type, _error, _traceback,
                     proxy=None, target=None):
    """Emit fixed classifications only; never expose log text or exception data."""
    if exc_type is None:
        return False
    safe_stage = stage if stage in DIAGNOSTIC_STAGES else 'unknown_stage'
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
        self.seen_bytes = {'out': 0, 'back': 0}
        self.dropped_bytes = {'out': 0, 'back': 0}
        self.forwarded_bytes = {'out': 0, 'back': 0}
        self.errors = []
        self.thread = threading.Thread(target=self.run, daemon=True)
        self.thread.start()

    def enable_loss(self, every):
        with self.lock:
            self.seen = {'out': 0, 'back': 0}
            self.dropped = {'out': 0, 'back': 0}
            self.forwarded = {'out': 0, 'back': 0}
            self.seen_bytes = {'out': 0, 'back': 0}
            self.dropped_bytes = {'out': 0, 'back': 0}
            self.forwarded_bytes = {'out': 0, 'back': 0}
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
            require(send(packet) == len(packet), 'UDP payload was only partly sent')
            self.forwarded[direction] += 1
            self.forwarded_bytes[direction] += len(packet)

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
                            # The engine may still be starting on its reserved port.
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
                        self.forwarded[direction], 'UDP datagram counters differ')
                require(self.seen_bytes[direction] == self.dropped_bytes[direction] +
                        self.forwarded_bytes[direction], 'UDP payload byte counters differ')
            return {'datagrams_seen': dict(self.seen),
                    'datagrams_dropped': dict(self.dropped),
                    'datagrams_forwarded': dict(self.forwarded),
                    'udp_payload_bytes_seen': dict(self.seen_bytes),
                    'udp_payload_bytes_dropped': dict(self.dropped_bytes),
                    'udp_payload_bytes_forwarded': dict(self.forwarded_bytes),
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
        config_path = directory / (name + '.json')
        config_path.write_text(json.dumps(configuration), encoding='utf-8')
        config_path.chmod(0o600)
        self.log = (directory / (name + '.log')).open('wb')
        try:
            self.process = subprocess.Popen([str(binary), '-C', str(config_path)],
                                            cwd=directory, stdin=subprocess.DEVNULL,
                                            stdout=self.log, stderr=subprocess.STDOUT)
        except Exception:
            self.log.close()
            raise

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
        try:
            if self.process.poll() is None:
                self.process.terminate()
                try:
                    self.process.wait(timeout=5)
                except subprocess.TimeoutExpired:
                    self.process.kill()
                    self.process.wait(timeout=5)
        finally:
            self.log.close()


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
            'scope': 'independent immediate-target-close probes after bulk; '
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


def scenario(binary, transport, lossy):
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
            target = Target()
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
            chains, services, ports = [], [], []
            for name in ('authorized', 'missing-auth', 'wrong-auth'):
                port = unused_port(socket.SOCK_STREAM)
                require(port not in ports, 'loopback port reservation collided; rerun test')
                ports.append(port)
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
            # Arm deterministic loss only after readiness and auth checks.
            clock_ticks = os.sysconf('SC_CLK_TCK')
            require(clock_ticks > 0, 'invalid Linux CPU clock tick rate')
            sampled_at = time.monotonic()
            before_cpu = {'server': server.cpu_ticks(), 'client': client.cpu_ticks()}
            proxy.enable_loss(23 if lossy else 0)
            stage[0] = 'bulk_transfer'
            result = bulk(ports[0], target)
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
            'immediate-target-close lost application replies; stable-stream '
            'results do not establish full reliable delivery')
    print('PASS: authenticated QUIC/KCP delivered bounded bidirectional payloads '
          'on loopback, including deterministic injected UDP loss', flush=True)


if __name__ == '__main__':
    main()
