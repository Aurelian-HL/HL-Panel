#!/usr/bin/env python3
"""Real node installation and loopback forwarding on a disposable systemd VM.

Only GitHub download transport is redirected to the candidate. Enrollment,
TLS, systemd, probes, engine processes and client traffic are real.
"""
import argparse
import base64
import hashlib
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
import json
import os
from pathlib import Path
import shutil
import socket
import ssl
import subprocess
import tarfile
import tempfile
import threading
import time
import urllib.error
import urllib.request

parser = argparse.ArgumentParser()
parser.add_argument('--archive-directory', type=Path, required=True)
parser.add_argument('--version', required=True)
args = parser.parse_args()
assert os.geteuid() == 0 and os.environ.get('HL_PANEL_DISPOSABLE_INSTALL_TEST') == 'true'
assert not Path('/opt/hl-panel').exists(), 'Requires an empty disposable node VM'
repo = Path(__file__).resolve().parents[2]
secrets = []
transcript = []
checks = []


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


try:
    with tempfile.TemporaryDirectory(prefix='hl-node-acceptance-') as temporary:
        work = Path(temporary)
        release = work / 'release'
        release.mkdir()
        with tarfile.open(args.archive_directory / 'hl-panel-linux-amd64.tar.gz') as archive:
            archive.extractall(release, filter='data')
        cert, key = work / 'ca.pem', work / 'key.pem'
        assert run(['openssl', 'req', '-x509', '-newkey', 'rsa:2048', '-nodes',
                    '-keyout', str(key), '-out', str(cert), '-days', '1',
                    '-subj', '/CN=HL isolated node test', '-addext', 'subjectAltName=IP:127.0.0.1']).returncode == 0
        port = unused_port()
        control_origin = f'https://127.0.0.1:{port}'
        context = ssl.create_default_context(cafile=str(cert))
        fault_state = {'enabled': False, 'enroll_calls': 0, 'lost_response': False,
                       'attempts': [], 'panel_versions': []}

        class NodeProxy(BaseHTTPRequestHandler):
            def log_message(self, *_):
                pass

            def forward(self):
                body = self.rfile.read(int(self.headers.get('Content-Length', '0')))
                enroll = self.path == '/api/v1/agent/enroll' and fault_state['enabled']
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
        environment = dict(os.environ, CONTROL_LISTEN_ADDRESS=f'127.0.0.1:{port}',
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

            def issue(ttl=900):
                token = request('POST', '/enrollment-tokens', {'name':'isolated node',
                                'group_id':group['id'], 'expires_in_seconds':ttl}, 201)['token']
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
                return run(['bash', str(repo / 'install-node.sh'), '--panel-url', origin,
                            '--token', token, '--ca-file', str(ca)], env=install_env, timeout=420)

            expired = issue(1)
            time.sleep(2)
            # Invalid CA fails before installing files.
            assert install(expired, work/'missing.pem').returncode != 0
            assert not Path('/opt/hl-panel/edge-agent').exists()
            assert install(expired).returncode != 0
            assert not Path('/etc/hl-panel/edge-agent-enrollment.env').exists()
            assert not Path('/var/lib/hl-panel-edge/credentials.json').exists()
            assert run(['systemctl','is-active','--quiet','hl-panel-edge-agent']).returncode != 0
            checks.append('expired token stops service and removes one-use secret')
            token = issue()
            fault_state['enabled'] = True
            assert install(token).returncode == 0, 'Complete one-command node installation failed'
            assert fault_state['enroll_calls'] == 4 and fault_state['lost_response']
            assert len(set(fault_state['attempts'])) == 1
            assert fault_state['panel_versions'] and set(fault_state['panel_versions']) == {args.version}
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
            assert not Path('/etc/systemd/system/hl-panel-control-api.service').exists()
            assert not Path('/etc/nginx/sites-available/hl-panel.conf').exists()
            members = request('GET', f"/device-groups/{group['id']}/members")['items']
            assert len(members) == 1 and members[0]['node_id'] == credential['node_id']
            checks.append('one command installs both engines, registers into selected group and enables systemd')

            def node_sample():
                nodes = request('GET','/nodes')['items']
                assert len(nodes) == 1
                node = nodes[0]
                host = node['resources']['host']
                assert node['agent_version'] == args.version
                assert host['memory_total_bytes'] > 0 and host['disk_total_bytes'] > 0
                assert 0 <= host['cpu_percent'] <= 100
                assert host['net_in_speed_bytes_per_second'] >= 0
                return node

            wait_for(node_sample)
            for route in ['/monitoring/nezha/servers', f"/device-groups/{group['id']}/monitoring/nezha"]:
                probe = request('GET',route)
                assert probe['upstream_status'] == 'disabled'
                item = probe['items'][0]
                assert item['source'] == 'hl' and item['link_status'] == 'native' and item['online']
                assert item['memory_total_bytes'] > 0
            checks.append('native host probe works without Nezha in inventory and group')
            assert install(token).returncode != 0
            assert hashlib.sha256(credential_path.read_bytes()).hexdigest() == identity_digest
            request('POST','/agent/enroll', {'token':token,'hostname':'reused',
                    'platform':'linux','architecture':'amd64','agent_version':args.version}, 401)
            checks.append('repeat install preserves identity and used token cannot enroll again')

            marker = b'hl-installed-engines-real-traffic'
            listener = socket.socket()
            listener.bind(('127.0.0.1', 0)); listener.listen()
            target_port = listener.getsockname()[1]

            def serve():
                while True:
                    try:
                        connection, _ = listener.accept()
                        with connection: connection.sendall(marker)
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
                'outbounds':[{'tag':'direct','protocol':'freedom'}]}
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
            assert run(['systemctl','restart','hl-panel-edge-agent']).returncode == 0
            wait_for(real_clients)
            assert hashlib.sha256(credential_path.read_bytes()).hexdigest() == identity_digest
            control.terminate(); control.wait(timeout=15)
            assert run(['systemctl','restart','hl-panel-edge-agent']).returncode == 0
            wait_for(real_clients)
            checks.append('restart keeps node identity and restores forwarding while panel is unavailable')
            listener.close()
            journal = run(['journalctl','-u','hl-panel-edge-agent','--no-pager']).stdout
            assert all(secret not in journal for secret in secrets), 'Secret leaked in node journal'
            checks.append('service journal contains no enrollment or persistent credentials')
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
    Path('node-install-result.json').write_text(json.dumps({'checks':checks},indent=2)+'\n')
print(json.dumps({'status':'passed','checks':checks},indent=2))
