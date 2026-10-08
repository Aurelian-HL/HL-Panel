#!/usr/bin/env python3
"""Real node installation and loopback forwarding on a disposable systemd VM.

Only GitHub download transport is redirected to the candidate. Enrollment,
TLS, systemd, probes, engine processes and client traffic are real.
"""
import argparse
import base64
import hashlib
import importlib.util
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
                       'attempts': [], 'panel_versions': [], 'reenroll_fault': False, 'reenroll_calls': 0}

        class NodeProxy(BaseHTTPRequestHandler):
            def log_message(self, *_):
                pass

            def forward(self):
                body = self.rfile.read(int(self.headers.get('Content-Length', '0')))
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
                return run(['bash', str(repo / 'install-node.sh'), '--panel-url', origin,
                            '--token', token, '--ca-file', str(ca), '--node-address', '127.0.0.1'], env=install_env, timeout=420)

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
                assert host['ip_collection_status'] == 'collected', 'agent sandbox must allow interface collection'
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
            assert install(token).returncode == 0, 'Same-command reinstall failed'
            assert hashlib.sha256(credential_path.read_bytes()).hexdigest() == identity_digest
            request('POST','/agent/enroll', {'token':token,'hostname':'reused',
                    'platform':'linux','architecture':'amd64','agent_version':args.version}, 401)
            checks.append('repeat install succeeds with the same identity; token alone cannot enroll a second machine')

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
            # Run the real updater on this disposable VM; redirect only official
            # release downloads to the fully verified candidate archive.
            sys.path.insert(0, str(repo / 'deploy'))
            spec = importlib.util.spec_from_file_location('node_updater', repo / 'deploy/update-node.py')
            node_updater = importlib.util.module_from_spec(spec)
            spec.loader.exec_module(node_updater)
            candidate = args.archive_directory.resolve()
            metadata = {'tag_name':args.version, 'draft':False, 'prerelease':False, 'published_at':'2026-01-01T00:00:00Z'}
            def candidate_request(url, limit=2*1024*1024):
                if '/releases/tags/' in url: return json.dumps(metadata).encode()
                return (candidate / url.rsplit('/',1)[1]).read_bytes()
            node_updater.request = candidate_request
            saved_args = sys.argv
            service_unit = Path('/etc/systemd/system/hl-panel-edge-agent.service')
            original_unit = service_unit.read_text()
            service_unit.write_text(original_unit.replace(' AF_NETLINK', ''))
            run(['systemctl', 'daemon-reload'], check=True)
            try:
                sys.argv = ['update-node.py','--version',args.version]
                node_updater.main()
                wait_for(real_clients)
                wait_for(node_sample)
                assert node_updater.METRICS_OVERRIDE.is_file()
                assert hashlib.sha256(credential_path.read_bytes()).hexdigest() == identity_digest
                backups = sorted(Path('/var/backups/hl-panel-node').glob('update-*'))
                assert backups
                run(['bash', str(backups[-1]/'rollback.sh')], check=True)
                assert not node_updater.METRICS_OVERRIDE.exists()
                assert service_unit.read_text() == original_unit.replace(' AF_NETLINK', '')
                wait_for(real_clients)
                assert hashlib.sha256(credential_path.read_bytes()).hexdigest() == identity_digest
            finally:
                sys.argv = saved_args
                service_unit.write_text(original_unit)
                run(['systemctl', 'daemon-reload'], check=True)
                run(['systemctl', 'restart', 'hl-panel-edge-agent'], check=True)
            checks.append('verified legacy node metrics upgrade and rollback launcher preserve custom unit, identity and real forwarding')
            limits = run(['systemctl','show','hl-panel-edge-agent',
                          '--property=StartLimitIntervalUSec,StartLimitBurst']).stdout
            assert 'StartLimitIntervalUSec=1min' in limits and 'StartLimitBurst=5' in limits
            checks.append('controlled updates retain systemd automatic crash rate limits')
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
            fault_state['reenroll_fault'] = True
            assert install(switch_token).returncode == 0, 'Full command did not switch existing node'
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
            assert install(token).returncode != 0
            assert_group(switched['id'])
            expired_switch = issue(1,group_id=group['id'])
            time.sleep(2)
            assert install(expired_switch).returncode != 0
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
            assert install(switch_token).returncode != 0
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
