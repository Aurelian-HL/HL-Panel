#!/usr/bin/env python3
"""Real node installation and loopback forwarding on a disposable systemd VM.

Only GitHub download transport is redirected to the candidate. Enrollment,
TLS, systemd, probes, engine processes and client traffic are real.
"""
import argparse
import base64
import hashlib
import importlib.util
import http.client
import uuid
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
import json
import os
from pathlib import Path
import shutil
import socket
import ssl
import subprocess
import sys
import tarfile
import tempfile
import threading
import time
import urllib.error
import urllib.request
from unittest.mock import patch

parser = argparse.ArgumentParser()
parser.add_argument('--archive-directory', type=Path, required=True)
parser.add_argument('--version', required=True)
parser.add_argument('--gost-sha256')
parser.add_argument('--gost-version')
args = parser.parse_args()
if bool(args.gost_sha256) != bool(args.gost_version):
    parser.error('--gost-sha256 and --gost-version must be supplied together')
if args.gost_sha256 and (len(args.gost_sha256) != 64 or
                        any(c not in '0123456789abcdef' for c in args.gost_sha256)):
    parser.error('--gost-sha256 must be a lowercase SHA256 digest')
assert os.geteuid() == 0 and os.environ.get('HL_PANEL_DISPOSABLE_INSTALL_TEST') == 'true'
assert not Path('/opt/hl-panel').exists(), 'Requires an empty disposable node VM'
repo = Path(__file__).resolve().parents[2]
secrets = []
transcript = []
checks = []
acceptance_status = 'failed'
worker_fixture = None


def run(command, **options):
    # Never include command arguments or environment in errors: they contain tokens.
    result = subprocess.run(command, capture_output=True, text=True, **options)
    safe = result.stdout + result.stderr
    for secret in secrets:
        safe = safe.replace(secret, '[hidden]')
    transcript.append(safe)
    return result


def wait_for(check, timeout=90):
    deadline = time.monotonic() + timeout
    while time.monotonic() < deadline:
        try:
            result = check()
            if result:
                return result
        except (OSError, AssertionError, KeyError, IndexError):
            pass
        time.sleep(1)
    raise RuntimeError('Isolated node acceptance condition timed out')


def unused_port():
    with socket.socket() as s:
        s.bind(('127.0.0.1', 0))
        return s.getsockname()[1]


def verify_expected_gost(path, stage):
    if args.gost_sha256 is None:
        return
    assert hashlib.sha256(path.read_bytes()).hexdigest() == args.gost_sha256, stage + ' GOST binary differs from accepted candidate'
    result = run([str(path), '-V'], timeout=15)
    assert result.returncode == 0 and result.stdout.strip() == args.gost_version, stage + ' GOST identity differs from accepted candidate'
    checks.append(stage + ' GOST matches accepted binary SHA256 and exact runtime identity')


try:
    with tempfile.TemporaryDirectory(prefix='hl-node-acceptance-') as temporary:
        work = Path(temporary)
        release = work / 'release'
        release.mkdir()
        with tarfile.open(args.archive_directory / 'hl-panel-linux-amd64.tar.gz') as archive:
            archive.extractall(release, filter='data')
        verify_expected_gost(release / 'bin/gost', 'packaged')
        cert, key = work / 'ca.pem', work / 'key.pem'
        assert run(['openssl', 'req', '-x509', '-newkey', 'rsa:2048', '-nodes',
                    '-keyout', str(key), '-out', str(cert), '-days', '1',
                    '-subj', '/CN=HL isolated node test', '-addext', 'subjectAltName=IP:127.0.0.1']).returncode == 0
        port = unused_port()
        control_origin = f'https://127.0.0.1:{port}'
        context = ssl.create_default_context(cafile=str(cert))
        fault_state = {'enabled': False, 'enroll_calls': 0, 'lost_response': False,
                       'attempts': [], 'panel_versions': [], 'reenroll_fault': False, 'reenroll_calls': 0,
                       'update_task': None, 'update_results': []}

        class NodeProxy(BaseHTTPRequestHandler):
            def log_message(self, *_):
                pass

            def forward(self):
                body = self.rfile.read(int(self.headers.get('Content-Length', '0')))
                # Disposable VM only: offer a fixed task through the real
                # Agent HTTP loop without publishing this candidate first.
                task = fault_state['update_task']
                if task and self.path in ('/api/v1/agent/update-task/claim', '/api/v1/agent/update-task/result'):
                    if self.path.endswith('/claim'):
                        data = json.dumps(task).encode()
                        self.send_response(200)
                        self.send_header('Content-Type', 'application/json')
                        self.send_header('Content-Length', str(len(data)))
                        self.end_headers(); self.wfile.write(data)
                    else:
                        result = json.loads(body)
                        assert result['id'] == task['id'] and result['lease'] == task['lease']
                        fault_state['update_results'].append(result)
                        self.send_response(204); self.end_headers()
                    return
                enroll = self.path == '/api/v1/agent/enroll' and fault_state['enabled']
                reenroll = self.path == '/api/v1/agent/reenroll' and fault_state['reenroll_fault']
                if reenroll:
                    fault_state['reenroll_calls'] += 1
                    if fault_state['reenroll_calls'] == 1:
                        self.send_response(503)
                        self.end_headers()
                        return
                if enroll:
                    recovery = json.loads(body)['enrollment_secret']
                    secrets.append(recovery)
                    fault_state['attempts'].append(recovery)
                    fault_state['enroll_calls'] += 1
                    if fault_state['enroll_calls'] <= 2:
                        self.send_response(503)
                        self.end_headers()
                        return
                headers = {name: self.headers[name] for name in ('Content-Type', 'Authorization') if name in self.headers}
                req = urllib.request.Request(control_origin + self.path,
                    body if body else None, headers, method=self.command)
                try:
                    response = urllib.request.urlopen(req, context=context, timeout=10)
                except urllib.error.HTTPError as error:
                    response = error
                with response:
                    data = response.read()
                    if self.path == '/api/v1/public/site-info':
                        fault_state['panel_versions'].append(json.loads(data)['platform_version'])
                    if enroll and fault_state['enroll_calls'] == 3:
                        assert response.code == 201, 'Fault injection did not consume the real token'
                        fault_state['lost_response'] = True
                        self.close_connection = True
                        self.connection.shutdown(socket.SHUT_RDWR)
                        return
                    if reenroll and fault_state['reenroll_calls'] == 2:
                        assert response.code == 200, 'Reinstall response loss did not follow a committed group switch'
                        self.close_connection = True
                        self.connection.shutdown(socket.SHUT_RDWR)
                        return
                    self.send_response(response.code)
                    self.send_header('Content-Type', 'application/json')
                    self.send_header('Content-Length', str(len(data)))
                    self.end_headers()
                    self.wfile.write(data)

            do_GET = forward
            do_POST = forward

        proxy = ThreadingHTTPServer(('127.0.0.1', 0), NodeProxy)
        proxy_tls = ssl.SSLContext(ssl.PROTOCOL_TLS_SERVER)
        proxy_tls.load_cert_chain(str(cert), str(key))
        proxy.socket = proxy_tls.wrap_socket(proxy.socket, server_side=True)
        threading.Thread(target=proxy.serve_forever, daemon=True).start()
        origin = f'https://127.0.0.1:{proxy.server_port}'
        password = 'isolated-' + os.urandom(24).hex()
        secrets.append(password)
        hash_result = run([str(release / 'bin/control-api'), 'hash-password'], input=password + '\n')
        assert hash_result.returncode == 0
        password_hash = hash_result.stdout.strip()
        secrets.append(password_hash)
        # The captured hash is not a deliverable.
        transcript.pop()
        from license_fixture import seed_license
        environment = dict(os.environ, **seed_license('node-ci.invalid', work/'license'), CONTROL_LISTEN_ADDRESS=f'127.0.0.1:{port}',
                           CONTROL_TLS_CERT_FILE=str(cert), CONTROL_TLS_KEY_FILE=str(key),
                           CONTROL_ALLOW_VOLATILE_STORE='true', CONTROL_BOOTSTRAP_ADMIN_USERNAME='node.acceptance',
                           CONTROL_BOOTSTRAP_ADMIN_PASSWORD_HASH=password_hash,
                           CONTROL_CUSTOMER_PASSWORD_FINGERPRINT_KEY_B64=base64.b64encode(os.urandom(32)).decode())
        control_log = (work / 'control.log').open('w')
        control = subprocess.Popen([str(release / 'bin/control-api')], env=environment,
                                   stdout=control_log, stderr=subprocess.STDOUT)
        admin_token = None

        def request(method, route, data=None, expected=200):
            headers = {'Content-Type': 'application/json'}
            if admin_token:
                headers['Authorization'] = 'Bearer ' + admin_token
            req = urllib.request.Request(control_origin + '/api/v1' + route,
                                         json.dumps(data).encode() if data is not None else None,
                                         headers, method=method)
            try:
                response = urllib.request.urlopen(req, context=context, timeout=10)
            except urllib.error.HTTPError as error:
                response = error
            assert response.code == expected, f'{method} {route}: {response.code}, expected {expected}'
            body = response.read()
            return json.loads(body) if body else None

        try:
            session = wait_for(lambda: request('POST', '/auth/login', {'username':'node.acceptance', 'password':password}))
            admin_token = session['access_token']
            secrets.append(admin_token)
            group = request('POST', '/device-groups', {'name':'isolated node', 'kind':'ENTRY',
                            'selection_policy':'weighted_least_connections', 'description':'loopback acceptance'}, 201)['group']

            def issue(ttl=900, group_id=None):
                token = request('POST', '/enrollment-tokens', {'name':'isolated node',
                                'group_id':group_id or group['id'], 'expires_in_seconds':ttl}, 201)['token']
                secrets.append(token)
                return token

            metadata = work / 'release.json'
            metadata.write_text(json.dumps({'tag_name':args.version}))
            shim = work / 'curl'
            shim.write_text('''#!/usr/bin/env python3
import os, shutil, sys
from pathlib import Path
tag=os.environ['HL_PANEL_CANDIDATE_TAG']; repo=Path(os.environ['HL_PANEL_CANDIDATE_REPO'])
base=Path(os.environ['HL_PANEL_CANDIDATE_DIR'])
mapping={'https://api.github.com/repos/Aurelian-HL/HL-Panel/releases/latest':Path(os.environ['HL_PANEL_CANDIDATE_META']),
 f'https://raw.githubusercontent.com/Aurelian-HL/HL-Panel/{tag}/deploy/install-node.sh':repo/'deploy/install-node.sh'}
for name in ['hl-panel-linux-amd64.tar.gz','hl-panel-linux-amd64.tar.gz.sha256']:
 mapping[f'https://github.com/Aurelian-HL/HL-Panel/releases/download/{tag}/{name}']=base/name
values=sys.argv[1:]; mapped=[mapping[x] for x in values if x in mapping]
if mapped:
 assert len(mapped)==1
 if '--output' in values: shutil.copyfile(mapped[0],values[values.index('--output')+1])
 else: sys.stdout.buffer.write(mapped[0].read_bytes())
else: os.execv(os.environ['HL_PANEL_REAL_CURL'],['curl',*values])
''')
            shim.chmod(0o755)
            install_env = dict(os.environ, PATH=str(work)+':'+os.environ['PATH'],
                               HL_PANEL_CANDIDATE_TAG=args.version, HL_PANEL_CANDIDATE_REPO=str(repo),
                               HL_PANEL_CANDIDATE_DIR=str(args.archive_directory.resolve()),
                               HL_PANEL_CANDIDATE_META=str(metadata), HL_PANEL_REAL_CURL=shutil.which('curl'))

            def install(token, ca=cert):
                # The root bootstrap intentionally resolves the latest published
                # release.  This candidate is not published until every job has
                # passed, so using that wrapper here would silently test the
                # previous public release instead of the archive under test.
                return run(['bash', str(repo / 'deploy/install-node.sh'), '--version', args.version,
                            '--panel-url', origin, '--token', token, '--ca-file', str(ca),
                            '--node-address', '127.0.0.1'], env=install_env, timeout=420)

            expired = issue(1)
            bbr_configs = {algorithm: Path('/etc/sysctl.d/90-hl-panel-node-' + algorithm + '.conf')
                           for algorithm in ('bbr2', 'bbr')}
            bbr_backups = Path('/var/backups/hl-panel-node')

            def bbr_backup_paths():
                return set(bbr_backups.glob('network-bbr2-*')) | set(bbr_backups.glob('network-bbr-*'))

            def transaction_paths():
                program = Path('/opt/hl-panel')
                return (set(program.glob('.node-update-v*')) | set(program.glob('.node-reinstall-*')) |
                        set(bbr_backups.glob('update-v*')))

            def bbr_state():
                current = run(['sysctl', '-n', 'net.ipv4.tcp_congestion_control'])
                assert current.returncode == 0, 'Cannot read the current TCP congestion algorithm'
                return (current.stdout.strip(),
                        {algorithm: path.read_bytes() if path.exists() else None
                         for algorithm, path in bbr_configs.items()})

            def rejected_install_preserves_bbr(token, ca=cert):
                before = bbr_state()
                assert install(token, ca).returncode != 0
                assert bbr_state() == before, 'Rejected installation changed TCP BBR runtime or persistent configuration'

            assert not any(path.exists() for path in bbr_configs.values()), 'requires empty disposable BBR configurations'
            original_bbr_state = bbr_state()
            # Probe support independently: an installer failure must never be
            # accepted as an unsupported-kernel skip. Loading a module does not
            # change the active TCP congestion-control default.
            if shutil.which('modprobe'):
                for module in ('tcp_bbr2', 'tcp_bbr'):
                    run(['modprobe', module], timeout=20)
            support = run(['sysctl', '-n', 'net.ipv4.tcp_available_congestion_control'])
            assert support.returncode == 0, 'Cannot inspect available TCP congestion algorithms'
            bbr_available = set(support.stdout.split())
            expected_bbr = next((name for name in ('bbr2', 'bbr') if name in bbr_available), None)
            preserve_variant = original_bbr_state[0].startswith('bbr') and original_bbr_state[0] not in bbr_configs
            backups_before_install = bbr_backup_paths()
            time.sleep(2)
            # Invalid CA fails before installing files.
            rejected_install_preserves_bbr(expired, work/'missing.pem')
            assert not Path('/opt/hl-panel/edge-agent').exists()
            rejected_install_preserves_bbr(expired)
            assert not Path('/etc/hl-panel/edge-agent-enrollment.env').exists()
            assert not Path('/var/lib/hl-panel-edge/credentials.json').exists()
            assert not any(path.exists() for path in bbr_configs.values()), 'failed enrollment must not change BBR settings'
            assert run(['systemctl','is-active','--quiet','hl-panel-edge-agent']).returncode != 0
            checks.append('expired token stops service and removes one-use secret')
            token = issue()
            fault_state['enabled'] = True
            assert install(token).returncode == 0, 'Complete one-command node installation failed'
            verify_expected_gost(Path('/opt/hl-panel/node-engines/gost'), 'installed')
            installed_bbr_state = bbr_state()
            installed_bbr_algorithm = installed_bbr_state[0]
            bbr_enabled = installed_bbr_algorithm in bbr_configs
            if expected_bbr and not preserve_variant:
                assert installed_bbr_algorithm == expected_bbr, 'Installer failed to enable the preferred supported TCP BBR algorithm'
            if bbr_enabled:
                assert bbr_configs[installed_bbr_algorithm].is_file()
                persisted = bbr_configs[installed_bbr_algorithm].read_text()
                assert ('net.ipv4.tcp_congestion_control = ' + installed_bbr_algorithm) in persisted.splitlines()
                assert all(not path.exists() for algorithm, path in bbr_configs.items()
                           if algorithm != installed_bbr_algorithm)
            else:
                assert preserve_variant or expected_bbr is None, 'Supported BBR must not be accepted as an unsupported-kernel skip'
                assert installed_bbr_state == original_bbr_state, 'Unsupported BBR algorithms changed runtime or persistence'
                assert not any(path.exists() for path in bbr_configs.values())
            assert bbr_backup_paths() == backups_before_install, 'Fresh installation retained a network archive'
            assert not transaction_paths(), 'Fresh installation retained a completed transaction'
            checks.append('successful token installation enables and persists kernel TCP ' + installed_bbr_algorithm if bbr_enabled else
                          'unsupported kernel preserves the original TCP algorithm and remains installable')
            assert fault_state['enroll_calls'] == 4 and fault_state['lost_response']
            assert len(set(fault_state['attempts'])) == 1
            # The installer receives the explicit release tag and deliberately
            # does not call the panel's site-info endpoint.  The bootstrap
            # unit test covers that contract; this integration test verifies
            # the resulting package and agent version instead.
            assert fault_state['panel_versions'] == []
            assert not Path('/var/lib/hl-panel-edge/enrollment-attempt.json').exists()
            checks.append('panel version selects installer and package from the same release tag')
            checks.append('automatic retry recovers two real HTTP 503 failures and a consumed-token response loss without duplicate nodes')
            credential_path = Path('/var/lib/hl-panel-edge/credentials.json')
            credential = json.loads(credential_path.read_text())
            secrets.append(credential['node_credential'])
            identity_digest = hashlib.sha256(credential_path.read_bytes()).hexdigest()
            assert credential_path.stat().st_mode & 0o077 == 0
            assert not Path('/etc/hl-panel/edge-agent-enrollment.env').exists()
            assert run(['systemctl','is-enabled','--quiet','hl-panel-edge-agent']).returncode == 0
            assert run(['systemctl','is-active','--quiet','hl-panel-edge-agent']).returncode == 0
            config = json.loads(Path('/etc/hl-panel/edge-agent.json').read_text())
            assert config['engine_mode'] == 'mixed' and config['xray_auto_start'] and config['gost_auto_start']
            routes = json.loads(run(['ip', '-j', '-4', 'route', 'get', '1.1.1.1']).stdout)
            egress_interface = routes[0]['dev']
            assert config['traffic_control_interface'] == egress_interface
            assert Path(config['traffic_control_binary']).resolve() == Path(shutil.which('tc')).resolve()
            agent_pid = run(['systemctl', 'show', 'hl-panel-edge-agent', '--property=MainPID', '--value']).stdout.strip()
            proc_status = dict(line.split(':', 1) for line in Path('/proc', agent_pid, 'status').read_text().splitlines() if ':' in line)
            assert int(proc_status['CapEff'].strip(), 16) & (1 << 12), 'Agent lacks real CAP_NET_ADMIN for limited rules'
            assert not Path('/etc/systemd/system/hl-panel-control-api.service').exists()
            assert not Path('/etc/nginx/sites-available/hl-panel.conf').exists()
            members = request('GET', f"/device-groups/{group['id']}/members")['items']
            assert len(members) == 1 and members[0]['node_id'] == credential['node_id']
            checks.append('one command installs both engines, registers into selected group and enables systemd')

            def node_sample():
                nodes = request('GET','/nodes')['items']
                assert len(nodes) == 1
                node = nodes[0]
                assert 'linux-tc-speed-limit-v1' in node['capabilities']
                host = node['resources']['host']
                assert node['agent_version'] == args.version
                assert host['memory_total_bytes'] > 0 and host['disk_total_bytes'] > 0
                assert host['ip_collection_status'] == 'collected', 'agent sandbox must allow interface collection'
                assert 0 <= host['cpu_percent'] <= 100
                assert host['net_in_speed_bytes_per_second'] >= 0
                return node

            wait_for(node_sample)
            checks.append('fresh token installation configures real TC interface, effective NET_ADMIN and reported speed-limit capability')
            for route in ['/monitoring/nezha/servers', f"/device-groups/{group['id']}/monitoring/nezha"]:
                probe = request('GET',route)
                assert probe['upstream_status'] == 'disabled'
                item = probe['items'][0]
                assert item['source'] == 'hl' and item['link_status'] == 'native' and item['online']
                assert item['memory_total_bytes'] > 0
            checks.append('native host probe works without Nezha in inventory and group')
            assert install(token).returncode == 0, 'Same-command reinstall failed'
            assert hashlib.sha256(credential_path.read_bytes()).hexdigest() == identity_digest
            request('POST','/agent/enroll', {'token':token,'hostname':'reused',
                    'platform':'linux','architecture':'amd64','agent_version':args.version}, 401)
            checks.append('repeat install succeeds with the same identity; token alone cannot enroll a second machine')

            marker = b'hl-installed-engines-real-traffic'
            listener = socket.socket()
            listener.bind(('127.0.0.1', 0)); listener.listen()
            target_port = listener.getsockname()[1]

            def echo(connection):
                try:
                    with connection:
                        connection.sendall(marker)
                        while True:
                            block = connection.recv(4096)
                            if not block:
                                return
                            connection.sendall(block)
                except OSError:
                    return

            def serve():
                while True:
                    try:
                        connection, _ = listener.accept()
                        threading.Thread(target=echo, args=(connection,), daemon=True).start()
                    except OSError: return

            threading.Thread(target=serve, daemon=True).start()
            gost_port, socks_port = unused_port(), unused_port()
            gost_config = {'services':[{'name':'isolated-forward','addr':f'127.0.0.1:{gost_port}',
                'handler':{'type':'tcp'},'listener':{'type':'tcp'},
                'metadata':{'enableStats':True},
                'forwarder':{'nodes':[{'name':'marker','addr':f'127.0.0.1:{target_port}'}],
                             'selector':{'strategy':'round','maxFails':1,'failTimeout':30000000000}}}]}
            request('POST', f"/device-groups/{group['id']}/generations",
                    {'engine':'gost','config':gost_config,'idempotency_key':'installed-gost'}, 201)
            second = request('POST','/device-groups',{'name':'isolated Xray','kind':'ENTRY',
                             'selection_policy':'weighted_least_connections','description':'loopback'},201)['group']
            request('POST',f"/device-groups/{second['id']}/members",{'node_id':credential['node_id'],'weight':100,'priority':0})
            xray_config = {'log':{'loglevel':'warning','access':'none'},
                'inbounds':[{'tag':'loopback-socks','listen':'127.0.0.1','port':socks_port,'protocol':'socks',
                             'settings':{'auth':'noauth','udp':False}}],
                'outbounds':[{'tag':'direct','protocol':'freedom'}],
                # Match panel-generated routing: live handler removal must not
                # depend on Xray's implicit first-outbound ordering.
                'routing':{'rules':[{'type':'field','inboundTag':['loopback-socks'],
                                     'outboundTag':'direct'}]}}
            request('POST',f"/device-groups/{second['id']}/generations",
                    {'engine':'xray','config':xray_config,'idempotency_key':'installed-xray'},201)

            def desired_applied():
                node = request('GET','/nodes')['items'][0]
                generation = node['desired_generation']
                if node['last_apply_status'] == 'failed' and node['last_apply_generation'] == generation:
                    raise RuntimeError('Node rejected the current forwarding generation; inspect sanitized service diagnostics')
                assert generation > 0 and node['applied_generation'] == generation
                return True

            wait_for(desired_applied)
            checks.append('both engine fragments pass validation and the desired generation is acknowledged')

            def receive_exact(connection, size):
                result = b''
                while len(result) < size:
                    block = connection.recv(size - len(result))
                    if not block:
                        raise OSError('Loopback client connection ended before complete response')
                    result += block
                return result

            def real_clients():
                with socket.create_connection(('127.0.0.1',gost_port),timeout=3) as s:
                    assert receive_exact(s, len(marker)) == marker
                with socket.create_connection(('127.0.0.1',socks_port),timeout=3) as s:
                    s.sendall(b'\x05\x01\x00'); assert receive_exact(s, 2) == b'\x05\x00'
                    s.sendall(b'\x05\x01\x00\x01\x7f\x00\x00\x01'+target_port.to_bytes(2,'big'))
                    reply = receive_exact(s, 10); assert reply[:2] == b'\x05\x00'
                    assert receive_exact(s, len(marker)) == marker
                return True

            wait_for(real_clients)
            checks.append('installed GOST and Xray forward real loopback client traffic simultaneously')
            # Run the real updater on this disposable VM; redirect only official
            # release downloads to the fully verified candidate archive.
            sys.path.insert(0, str(repo / 'deploy'))
            spec = importlib.util.spec_from_file_location('node_updater', repo / 'deploy/update-node.py')
            node_updater = importlib.util.module_from_spec(spec)
            spec.loader.exec_module(node_updater)
            # The real worker deliberately hides /home and has a private /tmp.
            # Keep only this disposable VM's download fixture in shared /var/lib;
            # do not relax the production service's filesystem protections.
            worker_fixture = tempfile.TemporaryDirectory(prefix='hl-node-update-ci-', dir='/var/lib')
            candidate = Path(worker_fixture.name)
            for name in ('hl-panel-linux-amd64.tar.gz', 'hl-panel-linux-amd64.tar.gz.sha256'):
                shutil.copy2(args.archive_directory / name, candidate / name)
            metadata = {'tag_name':args.version, 'draft':False, 'prerelease':False, 'published_at':'2026-01-01T00:00:00Z'}
            def candidate_request(url, limit=2*1024*1024):
                if '/releases/tags/' in url: return json.dumps(metadata).encode()
                return (candidate / url.rsplit('/',1)[1]).read_bytes()
            node_updater.request = candidate_request
            saved_args = sys.argv
            service_unit = Path('/etc/systemd/system/hl-panel-edge-agent.service')
            original_unit = service_unit.read_text()
            agent_config = Path('/etc/hl-panel/edge-agent.json')
            original_config = agent_config.read_bytes()
            # Dependency repair must keep both existing data-plane processes,
            # the service identity and active client connections untouched.
            def running_pids():
                result = {}
                for path in Path('/proc').iterdir():
                    if not path.name.isdigit():
                        continue
                    try:
                        executable = (path / 'exe').resolve(strict=True)
                    except (FileNotFoundError, PermissionError, ProcessLookupError):
                        continue
                    if str(executable) in ('/opt/hl-panel/edge-agent',
                            '/opt/hl-panel/node-engines/xray', '/opt/hl-panel/node-engines/gost'):
                        result[int(path.name)] = str(executable)
                assert len(set(result.values())) == 3, 'Both real engines and Agent must be running'
                return result

            before_repair = running_pids()
            persistent_clients = []
            resource_root = Path('/opt/hl-panel/node-engines')
            retained_resources = {name: (resource_root / name).read_bytes() for name in ('geoip.dat', 'geosite.dat')}
            try:
                for port in (gost_port, socks_port):
                    connection = socket.create_connection(('127.0.0.1', port), timeout=3)
                    persistent_clients.append(connection)
                    if port == socks_port:
                        connection.sendall(b'\x05\x01\x00')
                        assert receive_exact(connection, 2) == b'\x05\x00'
                        connection.sendall(b'\x05\x01\x00\x01\x7f\x00\x00\x01' + target_port.to_bytes(2, 'big'))
                        assert receive_exact(connection, 10)[:2] == b'\x05\x00'
                    assert receive_exact(connection, len(marker)) == marker
                    connection.sendall(b'before-repair')
                    assert receive_exact(connection, 13) == b'before-repair'
                for name in retained_resources:
                    (resource_root / name).unlink()
                sys.argv = ['update-node.py', '--version', args.version, '--repair']
                node_updater.main()
                for name in retained_resources:
                    assert (resource_root / name).stat().st_size > 0
                repaired_stamps = {name: (resource_root / name).stat().st_mtime_ns for name in retained_resources}
                node_updater.main()
                assert repaired_stamps == {name: (resource_root / name).stat().st_mtime_ns for name in retained_resources}
                assert running_pids() == before_repair, 'Dependency-only repair restarted Agent or an engine'
                assert agent_config.read_bytes() == original_config
                assert hashlib.sha256(credential_path.read_bytes()).hexdigest() == identity_digest
                for connection in persistent_clients:
                    connection.sendall(b'after-repair')
                    assert receive_exact(connection, 12) == b'after-repair'
                wait_for(real_clients)
            finally:
                for connection in persistent_clients:
                    connection.close()
                sys.argv = saved_args
                for name, content in retained_resources.items():
                    (resource_root / name).write_bytes(content)
            checks.append('dependency-only repair restores missing GeoIP/GeoSite, repeats without writes, preserves all PIDs and real clients')
            tc_dropin = service_unit.parent / 'hl-panel-edge-agent.service.d/30-traffic-control.conf'
            original_tc_dropin = tc_dropin.read_bytes()
            legacy_config = {key: value for key, value in json.loads(original_config).items()
                             if not key.startswith('traffic_control_')}
            agent_config.write_text(json.dumps(legacy_config) + '\n')
            tc_dropin.unlink()
            service_unit.write_text(original_unit.replace(' AF_NETLINK', ''))
            run(['systemctl', 'daemon-reload'], check=True)
            try:
                sys.argv = ['update-node.py','--version',args.version]
                before_transactions = transaction_paths()
                original_active = node_updater.active
                attempts = []
                def fail_candidate_start():
                    attempts.append(True)
                    if len(attempts) == 1:
                        raise node_updater.UpdateError('disposable candidate start failure')
                    return original_active()
                try:
                    with patch.object(node_updater, 'active', side_effect=fail_candidate_start):
                        node_updater.main()
                except node_updater.UpdateError:
                    pass
                else:
                    raise AssertionError('Candidate-start fault was not exercised')
                assert len(attempts) == 2, 'Failed candidate did not attempt to restore the old service'
                assert transaction_paths() == before_transactions, 'Recovered update retained its temporary files'
                assert not node_updater.METRICS_OVERRIDE.exists()
                assert not tc_dropin.exists(), 'Recovery retained new TC service defaults'
                assert json.loads(agent_config.read_text()) == legacy_config
                assert service_unit.read_text() == original_unit.replace(' AF_NETLINK', '')
                wait_for(real_clients)
                assert hashlib.sha256(credential_path.read_bytes()).hexdigest() == identity_digest
                node_updater.main()
                wait_for(real_clients)
                wait_for(node_sample)
                assert node_updater.METRICS_OVERRIDE.is_file()
                assert not any(key.startswith('traffic_control_') for key in json.loads(agent_config.read_text())), 'Legacy updater did not exercise saved-JSON overwrite'
                assert 'HL_PANEL_TC_INTERFACE=' + egress_interface in tc_dropin.read_text()
                assert 'HL_PANEL_TC_BINARY=' in tc_dropin.read_text()
                agent_pid = run(['systemctl', 'show', 'hl-panel-edge-agent', '-p', 'MainPID', '--value']).stdout.strip()
                proc_status = dict(line.split(':', 1) for line in Path('/proc', agent_pid, 'status').read_text().splitlines() if ':' in line)
                assert int(proc_status['CapEff'].strip(), 16) & (1 << 12), 'Upgraded legacy agent lacks effective CAP_NET_ADMIN'
                assert hashlib.sha256(credential_path.read_bytes()).hexdigest() == identity_digest
                assert transaction_paths() == before_transactions, 'Successful CLI update retained its temporary files'
            finally:
                sys.argv = saved_args
                service_unit.write_text(original_unit)
                agent_config.write_bytes(original_config)
                tc_dropin.parent.mkdir(mode=0o755, exist_ok=True)
                tc_dropin.write_bytes(original_tc_dropin)
                run(['systemctl', 'daemon-reload'], check=True)
                run(['systemctl', 'restart', 'hl-panel-edge-agent'], check=True)
            checks.append('legacy node start failure restores custom unit, absent metrics/TC defaults, identity and real clients without a retained archive')
            checks.append('legacy JSON without TC upgrades to effective NET_ADMIN and reported speed-limit capability; successful CLI update cleans its transaction')
            limits = run(['systemctl','show','hl-panel-edge-agent',
                          '--property=StartLimitIntervalUSec,StartLimitBurst']).stdout
            assert 'StartLimitIntervalUSec=1min' in limits and 'StartLimitBurst=5' in limits
            checks.append('controlled updates retain systemd automatic crash rate limits')

            # The actual root systemd worker survives the agent it updates.
            # Redirect ONLY official download transport on this disposable VM.
            update_helper = Path('/opt/hl-panel/update.py')
            def map_worker_downloads():
                original = update_helper.read_text()
                update_helper.write_text(original + '\nimport json as _ci_json\nfrom pathlib import Path as _CIPath\n_original_ci_request = request\ndef request(url, limit=2*1024*1024):\n' +
                    '    if url == ' + repr('https://api.github.com/repos/Aurelian-HL/HL-Panel/releases/tags/'+args.version) + ': return ' + repr(json.dumps(metadata).encode()) + '\n' +
                    '    if url.startswith(' + repr('https://github.com/Aurelian-HL/HL-Panel/releases/download/'+args.version+'/') + '): return (_CIPath(' + repr(str(candidate)) + ')/url.rsplit("/",1)[1]).read_bytes()\n' +
                    '    return _original_ci_request(url, limit)\n')
            class LocalUpdateConnection(http.client.HTTPConnection):
                def connect(self):
                    self.sock = socket.socket(socket.AF_UNIX, socket.SOCK_STREAM)
                    self.sock.settimeout(10)
                    self.sock.connect('/run/hl-panel-node-update.sock')
            def maintenance(path='/status', data=None, expected=200):
                connection = LocalUpdateConnection('localhost', timeout=10)
                connection.request('GET' if data is None else 'POST', path, json.dumps(data) if data is not None else None, {'Content-Type':'application/json'})
                response=connection.getresponse();body=response.read();connection.close()
                assert response.status == expected, 'local update endpoint unexpected HTTP status'
                return json.loads(body)
            def worker_verifying():
                task = maintenance().get('task') or {}
                if task.get('state') in ('failed', 'rolled_back', 'uncertain'):
                    raise RuntimeError('Isolated worker update failed: ' + task['state'] + '/' + task.get('phase', ''))
                return task.get('state') == 'verifying'
            assert maintenance()['capability'] == 'node-update-v1'
            maintenance('/start', {'command':'id'}, 400)
            archive_sha=hashlib.sha256((candidate/'hl-panel-linux-amd64.tar.gz').read_bytes()).hexdigest()
            for recovery in (False, True):
                map_worker_downloads()
                before_pid=run(['systemctl','show','hl-panel-edge-agent','-p','MainPID','--value'],check=True).stdout.strip()
                worker_pid=run(['systemctl','show','hl-panel-node-update','-p','MainPID','--value'],check=True).stdout.strip()
                task={'id':str(uuid.uuid4()),'version':args.version,'archive_sha256':archive_sha}
                wait_for(lambda: maintenance('/start',task))
                maintenance('/start',task)  # Replaying a task must not execute twice.
                wait_for(worker_verifying,timeout=120)
                pending = maintenance()['task']
                pending_directory = Path(pending['backup_directory'])
                assert pending_directory.is_dir(), 'Unverified update discarded recovery materials'
                assert run(['systemctl','show','hl-panel-node-update','-p','MainPID','--value'],check=True).stdout.strip()==worker_pid
                after_pid=run(['systemctl','show','hl-panel-edge-agent','-p','MainPID','--value'],check=True).stdout.strip()
                assert after_pid!=before_pid
                wait_for(real_clients)
                if recovery:
                    run(['systemctl','restart','hl-panel-node-update'],check=True)
                    assert maintenance()['task']['state']=='uncertain'
                    maintenance('/rollback',{'id':task['id']})
                    wait_for(lambda: maintenance()['task']['state']=='rolled_back',timeout=120)
                    wait_for(real_clients)
                    # Rollback restores its exact helper; remove this test's
                    # download shim before the following installer scenarios.
                    update_helper.write_bytes((release/'deploy/update.py').read_bytes())
                else:
                    # No pending desired exists after normal acknowledgement.
                    # The unprivileged Agent must verify the local good bundle,
                    # both real engine processes and their owned sockets.
                    desired_request = urllib.request.Request(control_origin + '/api/v1/agent/desired',
                        headers={'Authorization': 'Bearer ' + credential['node_credential']})
                    with urllib.request.urlopen(desired_request, context=context, timeout=10) as response:
                        assert response.status == 204 and response.read() == b''
                    fault_state['update_task'] = dict(task, lease='a'*64)
                    wait_for(lambda: any(x['state']=='succeeded' for x in fault_state['update_results']),timeout=120)
                    proof = next(x for x in fault_state['update_results'] if x['state']=='succeeded')
                    ledger = json.loads(Path('/var/lib/hl-panel-edge/state.json').read_text())
                    assert proof['verified_generation']==ledger['applied']['generation']
                    assert proof['verified_sha256']==ledger['applied']['config_sha256']
                    fault_state['update_task'] = None
                    assert maintenance()['task']['state']=='succeeded'
                    old_pid=after_pid
                    maintenance('/start',task)
                    assert run(['systemctl','show','hl-panel-edge-agent','-p','MainPID','--value'],check=True).stdout.strip()==old_pid
                wait_for(lambda: not pending_directory.exists())
                assert maintenance()['task']['backup_directory'] == '', 'Completed task retained a stale transaction path'
                assert hashlib.sha256(credential_path.read_bytes()).hexdigest()==identity_digest
            checks.append('real root socket update preserves pending recovery, cleans success/recovery, survives Agent replacement; task replay, crash lock and real forwarding verified')

            # Two real same-port group switches; both old group memberships
            # must be removed, not merely masked in the UI.
            changed_marker = b'hl-reinstalled-new-group-traffic'
            changed_listener = socket.socket()
            changed_listener.bind(('127.0.0.1',0)); changed_listener.listen()
            changed_port = changed_listener.getsockname()[1]
            def serve_changed():
                while True:
                    try:
                        connection,_ = changed_listener.accept()
                        with connection: connection.sendall(changed_marker)
                    except OSError: return
            threading.Thread(target=serve_changed,daemon=True).start()
            switched = request('POST','/device-groups',{'name':'reinstalled GOST','kind':'ENTRY',
                'selection_policy':'weighted_least_connections'},201)['group']
            switched_config = json.loads(json.dumps(gost_config))
            switched_config['services'][0]['forwarder']['nodes'][0]['addr'] = f'127.0.0.1:{changed_port}'
            request('POST',f"/device-groups/{switched['id']}/generations",{'engine':'gost',
                'config':switched_config,'idempotency_key':'switch-gost'},201)
            switch_token = issue(group_id=switched['id'])
            # Only this explicitly disposable VM resets its own fixture. Node
            # installation no longer exposes an archived network rollback tool.
            def reset_bbr_fixture():
                if not bbr_enabled:
                    assert bbr_state() == original_bbr_state
                    return
                for algorithm, path in bbr_configs.items():
                    if path.exists():
                        assert path.read_bytes() == installed_bbr_state[1][algorithm], 'Refusing to remove an unrelated sysctl file'
                        path.unlink()
                default = original_bbr_state[0]
                if default == installed_bbr_algorithm:
                    available = run(['sysctl', '-n', 'net.ipv4.tcp_available_congestion_control'])
                    assert available.returncode == 0 and 'cubic' in available.stdout.split()
                    default = 'cubic'
                assert run(['sysctl', '-q', '-w', 'net.ipv4.tcp_congestion_control='+default]).returncode == 0
                assert bbr_state()[0] != installed_bbr_algorithm and not any(path.exists() for path in bbr_configs.values())
            reset_bbr_fixture()
            # Exercise an actual legacy upgrade with BBR absent, rather than
            # passing because fresh installation already enabled it.
            backups_before_upgrade_bbr = bbr_backup_paths()
            before_transactions = transaction_paths()
            try:
                sys.argv = ['update-node.py','--version',args.version]
                node_updater.main()
            finally:
                sys.argv = saved_args
            wait_for(real_clients)
            assert bbr_state() == installed_bbr_state, 'Legacy upgrade did not configure the supported BBR algorithm'
            assert bbr_backup_paths() == backups_before_upgrade_bbr, 'Program upgrade retained a network archive'
            assert transaction_paths() == before_transactions, 'Program upgrade retained a completed transaction'
            reset_bbr_fixture()
            assert hashlib.sha256(credential_path.read_bytes()).hexdigest() == identity_digest
            checks.append('legacy program upgrade configures supported BBR, preserves node identity and leaves no completed archives')
            backups_before_reinstall = bbr_backup_paths()
            # A legacy machine may also be migrated using its token command.
            # Remove only this disposable VM's TC configuration fixture.
            legacy_config = {key: value for key, value in json.loads(agent_config.read_text()).items()
                             if not key.startswith('traffic_control_')}
            agent_config.write_text(json.dumps(legacy_config) + '\n')
            tc_dropin.unlink()
            run(['systemctl', 'daemon-reload'], check=True)
            fault_state['reenroll_fault'] = True
            assert install(switch_token).returncode == 0, 'Full command did not switch existing node'
            wait_for(node_sample)
            assert tc_dropin.is_file()
            assert 'HL_PANEL_TC_INTERFACE=' + egress_interface in tc_dropin.read_text()
            checks.append('token-command reinstall provisions TC on an existing legacy node without changing its identity')
            assert bbr_state() == installed_bbr_state, 'Reinstall changed the TCP algorithm or BBR persistence unexpectedly'
            assert {algorithm: path.is_file() for algorithm, path in bbr_configs.items()} == {
                algorithm: bbr_enabled and algorithm == installed_bbr_algorithm for algorithm in bbr_configs}
            reinstall_backups = bbr_backup_paths()
            assert reinstall_backups == backups_before_reinstall, 'Group-switch reinstall retained a network archive'
            assert transaction_paths() == before_transactions, 'Group-switch reinstall retained a completed transaction'
            checks.append('group-switch reinstall enables ' + installed_bbr_algorithm + ' without a retained archive' if bbr_enabled else
                          'group-switch reinstall preserves unsupported-kernel TCP settings')
            assert fault_state['reenroll_calls'] == 3
            fault_state['reenroll_fault'] = False
            registered_groups = [group,second,switched]
            def assert_group(active_id):
                for g in registered_groups:
                    members=request('GET',f"/device-groups/{g['id']}/members")['items']
                    active=[m for m in members if not m.get('retired_at')]
                    assert len(active) == (1 if g['id']==active_id else 0)
                assert hashlib.sha256(credential_path.read_bytes()).hexdigest() == identity_digest
                assert len(request('GET','/nodes')['items']) == 1
            def gost_after_switch():
                desired_applied()
                with socket.create_connection(('127.0.0.1',gost_port),timeout=3) as s:
                    assert receive_exact(s,len(changed_marker)) == changed_marker
                return True
            wait_for(gost_after_switch)
            assert_group(switched['id'])
            assert install(switch_token).returncode == 0
            assert bbr_state() == installed_bbr_state
            assert bbr_backup_paths() == reinstall_backups, 'Repeated reinstall changed already configured BBR'
            rejected_install_preserves_bbr(token)
            assert_group(switched['id'])
            expired_switch = issue(1,group_id=group['id'])
            time.sleep(2)
            rejected_install_preserves_bbr(expired_switch)
            checks.append('superseded and expired reinstall tokens preserve TCP algorithm and exact BBR configuration')
            wait_for(gost_after_switch)
            checks.append('full reinstall recovers HTTP 503 and lost confirmation, replaces GOST at the same port, retires both old groups and preserves identity')
            xray_group = request('POST','/device-groups',{'name':'reinstalled Xray','kind':'ENTRY',
                'selection_policy':'weighted_least_connections'},201)['group']
            registered_groups.append(xray_group)
            request('POST',f"/device-groups/{xray_group['id']}/generations",{'engine':'xray',
                'config':xray_config,'idempotency_key':'switch-xray'},201)
            assert install(issue(group_id=xray_group['id'])).returncode == 0
            assert_group(xray_group['id'])
            def xray_after_switch():
                desired_applied()
                with socket.create_connection(('127.0.0.1',socks_port),timeout=3) as s:
                    s.sendall(b'\x05\x01\x00'); assert receive_exact(s,2)==b'\x05\x00'
                    s.sendall(b'\x05\x01\x00\x01\x7f\x00\x00\x01'+changed_port.to_bytes(2,'big'))
                    assert receive_exact(s,10)[:2]==b'\x05\x00'
                    assert receive_exact(s,len(changed_marker))==changed_marker
                assert run(['ss','-H','-ltn','sport','=',str(gost_port)]).stdout.strip()==''
                return True
            wait_for(xray_after_switch)
            rejected_install_preserves_bbr(switch_token)
            wait_for(xray_after_switch)
            checks.append('second full reinstall activates Xray at the same port and removes old GOST listener; superseded commands cannot restore old groups')
            # Return to the original dual-engine fixture before offline restart.
            assert install(issue()).returncode == 0
            request('POST',f"/device-groups/{second['id']}/members",{'node_id':credential['node_id'],'weight':100,'priority':0})
            wait_for(real_clients)
            assert hashlib.sha256(credential_path.read_bytes()).hexdigest() == identity_digest
            changed_listener.close()
            # Separate operator restart scenarios from the expired-token fault
            # and repeated install/update/rollback starts in the same minute.
            assert run(['systemctl','reset-failed','hl-panel-edge-agent']).returncode == 0
            assert run(['systemctl','restart','hl-panel-edge-agent']).returncode == 0
            wait_for(real_clients)
            assert hashlib.sha256(credential_path.read_bytes()).hexdigest() == identity_digest
            control.terminate(); control.wait(timeout=15)
            assert run(['systemctl','reset-failed','hl-panel-edge-agent']).returncode == 0
            assert run(['systemctl','restart','hl-panel-edge-agent']).returncode == 0
            wait_for(real_clients)
            checks.append('restart keeps node identity and restores forwarding while panel is unavailable')
            listener.close()
            journal = run(['journalctl','-u','hl-panel-edge-agent','--no-pager']).stdout
            assert all(secret not in journal for secret in secrets), 'Secret leaked in node journal'
            checks.append('service journal contains no enrollment or persistent credentials')
            verify_expected_gost(Path('/opt/hl-panel/node-engines/gost'), 'after update rollback and reinstall')
            acceptance_status = 'passed'
        finally:
            proxy.shutdown()
            proxy.server_close()
            if control.poll() is None:
                control.terminate(); control.wait(timeout=15)
            control_log.close()
finally:
    # Include sanitized service diagnostics even when installation fails early.
    credential_file = Path('/var/lib/hl-panel-edge/credentials.json')
    if credential_file.is_file():
        try:
            value = json.loads(credential_file.read_text()).get('node_credential')
            if value:
                secrets.append(value)
        except (OSError, ValueError):
            pass
    run(['systemctl', 'show', 'hl-panel-edge-agent', '--property=ActiveState,SubState,Result,NRestarts'])
    run(['journalctl', '-u', 'hl-panel-edge-agent', '-n', '100', '--no-pager'])
    Path('node-install.log').write_text('\n'.join(transcript))
    Path('node-install-result.json').write_text(json.dumps({'status':acceptance_status,'checks':checks},indent=2)+'\n')
    if worker_fixture is not None:
        worker_fixture.cleanup()
print(json.dumps({'status':'passed','checks':checks},indent=2))
