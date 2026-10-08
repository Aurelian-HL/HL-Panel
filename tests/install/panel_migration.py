#!/usr/bin/env python3
"""Restore two isolated PostgreSQL panels, move a real agent, and use real GOST.

Requires root, PostgreSQL, systemd and candidate binaries. All listeners and
traffic are loopback. Uses only uniquely named databases, directories and units;
never reads the installed panel or node's configuration or business data.
"""
import argparse
import base64
import hashlib
import importlib.util
import json
import os
from pathlib import Path
import pwd
import secrets
import shutil
import signal
import socket
import ssl
import subprocess
import tempfile
import threading
import time
import urllib.error
import urllib.request


def command(args, **options):
    result = subprocess.run(args, capture_output=True, **options)
    if result.returncode:
        raise RuntimeError('Isolated migration command failed: ' + Path(args[0]).name)
    return result.stdout


def wait_for(check, timeout=45):
    deadline = time.monotonic() + timeout
    last_error = None
    while time.monotonic() < deadline:
        try:
            value = check()
            if value:
                return value
        except (OSError, AssertionError, KeyError) as error:
            last_error = error
        time.sleep(0.2)
    raise RuntimeError('Isolated migration acceptance timed out') from last_error


def port():
    with socket.socket() as sock:
        sock.bind(('127.0.0.1', 0))
        return sock.getsockname()[1]


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument('--binaries', type=Path, required=True)
    parser.add_argument('--version', required=True)
    args = parser.parse_args()
    assert os.geteuid() == 0 and os.environ.get('HL_PANEL_DISPOSABLE_MIGRATION_TEST') == 'true'
    binaries = args.binaries.resolve()
    for name in ['control-api', 'usage-migrate', 'edge-agent', 'gost']:
        assert (binaries / name).is_file()
    postgres = pwd.getpwnam('postgres')
    suffix = secrets.token_hex(6)
    root = Path(tempfile.mkdtemp(prefix='hl-migration-system-', dir='/var/tmp'))
    root.chmod(0o755)
    unit = 'hl-migration-test-' + suffix
    processes, databases, servers = [], [], []
    checks, sensitive = [], []
    try:
        cert, key = root/'ca.pem', root/'key.pem'
        command(['openssl', 'req', '-x509', '-newkey', 'rsa:2048', '-nodes', '-keyout', str(key),
                 '-out', str(cert), '-days', '1', '-subj', '/CN=HL isolated migration',
                 '-addext', 'subjectAltName=IP:127.0.0.1'])
        os.chown(key, postgres.pw_uid, postgres.pw_gid)
        key.chmod(0o600)
        cert.chmod(0o644)
        context = ssl.create_default_context(cafile=str(cert))
        opener = urllib.request.build_opener(urllib.request.ProxyHandler({}), urllib.request.HTTPSHandler(context=context))
        panels = []
        for index in range(2):
            directory = root/('panel-' + str(index))
            directory.mkdir(mode=0o700)
            os.chown(directory, postgres.pw_uid, postgres.pw_gid)
            db = 'nyvp_migration_system_' + suffix + '_' + str(index) + '_test'
            command(['runuser', '-u', 'postgres', '--', 'createdb', db])
            databases.append(db)
            database_url = f'postgres://postgres@/{db}?host=/var/run/postgresql&sslmode=disable'
            dsn = directory/'database-url'
            dsn.write_text(database_url); dsn.chmod(0o600)
            os.chown(dsn, postgres.pw_uid, postgres.pw_gid)
            for action in ['apply', 'verify']:
                command(['runuser', '-u', 'postgres', '--', str(binaries/'usage-migrate'), action, '-dsn-file', str(dsn)])
            password = secrets.token_urlsafe(24)
            password_hash = command([str(binaries/'control-api'), 'hash-password'], input=(password+'\n').encode()).decode().strip()
            sensitive.extend([password, password_hash])
            listen = port()
            fingerprint = base64.b64encode(os.urandom(32)).decode()
            sensitive.append(fingerprint)
            env = dict(os.environ, CONTROL_LISTEN_ADDRESS=f'127.0.0.1:{listen}',
                       CONTROL_TLS_CERT_FILE=str(cert), CONTROL_TLS_KEY_FILE=str(key),
                       CONTROL_DATABASE_URL=database_url,
                       CONTROL_BOOTSTRAP_ADMIN_USERNAME='migration' + str(index),
                       CONTROL_BOOTSTRAP_ADMIN_PASSWORD_HASH=password_hash,
                       CONTROL_CUSTOMER_PASSWORD_FINGERPRINT_KEY_B64=fingerprint,
                       CONTROL_PANEL_LOG_FILE=str(directory/'panel.log'),
                       CONTROL_MIGRATION_BACKUP_DIR=str(directory/'backups'))
            log = (root/('process-' + str(index) + '.log')).open('wb')
            try:
                process = subprocess.Popen(['runuser', '-u', 'postgres', '--', str(binaries/'control-api')],
                                           env=env, cwd=directory, stdout=log, stderr=subprocess.STDOUT, start_new_session=True)
            finally:
                log.close()
            processes.append(process)
            panels.append({'url': f'https://127.0.0.1:{listen}', 'password': password,
                           'username': 'migration' + str(index), 'db': db, 'fingerprint': fingerprint, 'directory': directory})

        def request(panel, method, route, body=None, token=None, expected=200, raw=False, headers=None):
            h = dict(headers or {})
            if isinstance(body, dict):
                body = json.dumps(body).encode(); h['Content-Type'] = 'application/json'
            if token:
                h['Authorization'] = 'Bearer ' + token
            req = urllib.request.Request(panel['url']+'/api/v1'+route, body, h, method=method)
            try:
                response = opener.open(req, timeout=15)
            except urllib.error.HTTPError as error:
                response = error
            with response:
                value = response.read()
                if response.code != expected:
                    detail = value.decode(errors='replace')[:1000]
                    for secret in sensitive:
                        detail = detail.replace(secret, '[hidden]')
                    raise AssertionError(f'{method} {route}: {response.code}, expected {expected}; {detail}')
                return value if raw else json.loads(value) if value else None

        def login(panel):
            return request(panel, 'POST', '/auth/login', {'username':panel['username'], 'password':panel['password']})['access_token']

        source, target = panels
        source_token = wait_for(lambda: login(source))
        target_token = wait_for(lambda: login(target))
        for panel in panels:
            assert request(panel, 'GET', '/public/site-info')['platform_version'] == args.version
        group = request(source, 'POST', '/device-groups', {'name':'migration loopback', 'kind':'ENTRY',
                        'selection_policy':'weighted_round_robin'}, source_token, 201)['group']
        token = request(source, 'POST', '/enrollment-tokens', {'name':'migration agent', 'group_id':group['id'],
                        'expires_in_seconds':900}, source_token, 201)['token']
        sensitive.append(token)
        state = root/'node-state'; state.mkdir(mode=0o700)
        config_path = root/'edge-agent.json'
        config = {'control_plane_url':source['url'], 'data_dir':str(state), 'ca_file':str(cert),
                  'hostname':'migration-loopback', 'dial_host':'127.0.0.1', 'engine_mode':'gost',
                  'gost_binary_path':str((binaries/'gost').resolve()), 'gost_auto_start':True,
                  'gost_stats_api_address':f'127.0.0.1:{port()}', 'heartbeat_interval':'1s',
                  'desired_poll_interval':'1s', 'usage_report_interval':'1s',
                  'protocol_probe_echo_port':port(), 'request_timeout':'5s'}
        config_path.write_text(json.dumps(config)); config_path.chmod(0o600)
        command(['systemd-run', '--unit='+unit, '--collect', '--setenv=NYVP_ENROLLMENT_TOKEN='+token,
                 str(binaries/'edge-agent'), '-config', str(config_path)])

        def node(panel, admin):
            items = request(panel, 'GET', '/nodes', token=admin)['items']
            assert len(items) == 1 and items[0]['status'] == 'online'
            return items[0]

        registered = wait_for(lambda: node(source, source_token))
        request(source, 'PUT', f"/group-networks/{group['id']}",
                {'connect_host':'127.0.0.1', 'port_ranges':[{'start':1024,'end':65535}],
                 'direct_policy':'FORCED', 'allowed_exit_group_ids':[], 'traffic_multiplier':1,
                 'revision':0}, source_token,
                headers={'Idempotency-Key':'migration-fixture-network'})
        rule = request(source, 'POST', '/forwarding-rules', {'name':'迁移规则', 'entry_group_id':group['id'],
                       'egress_mode':'DIRECT', 'protocol':'tcp', 'listen_port':port(),
                       'targets':[{'host':'127.0.0.1','port':port()}], 'selection_policy':'round_robin',
                       'traffic_limit_bytes':1000000, 'paused':True}, source_token, 201,
                       headers={'Idempotency-Key':'migration-fixture-rule'})['rule']
        credentials = state/'credentials.json'
        identity = hashlib.sha256(credentials.read_bytes()).hexdigest()
        target_socket = socket.socket(); target_socket.bind(('127.0.0.1', 0)); target_socket.listen()
        servers.append(target_socket)
        marker = b'HL-migration-real-forwarding-ok'
        def serve():
            while True:
                try:
                    connection, _ = target_socket.accept()
                    with connection: connection.sendall(marker)
                except OSError:
                    return
        threading.Thread(target=serve, daemon=True).start()
        forwarding_port = port()
        gost = {'services':[{'name':'migration-loopback', 'addr':f'127.0.0.1:{forwarding_port}',
                'handler':{'type':'tcp'}, 'listener':{'type':'tcp'},
                'metadata':{'enableStats':True},
                'forwarder':{'nodes':[{'name':'marker','addr':f'127.0.0.1:{target_socket.getsockname()[1]}'}],
                             'selector':{'strategy':'round','maxFails':1,'failTimeout':30000000000}}}]}
        request(source, 'POST', f"/device-groups/{group['id']}/generations",
                {'engine':'gost', 'config':gost, 'idempotency_key':'migration-real-engine'}, source_token, 201)
        def applied(panel, admin):
            n = node(panel, admin)
            assert n['desired_generation'] > 0 and n['applied_generation'] == n['desired_generation'] and n['last_apply_status'] == 'succeeded'
            return n
        original = wait_for(lambda: applied(source, source_token))
        def client():
            with socket.create_connection(('127.0.0.1', forwarding_port), timeout=3) as sock:
                value = b''
                while len(value) < len(marker):
                    part = sock.recv(len(marker)-len(value))
                    assert part, 'Loopback forwarding closed before receiving the marker'
                    value += part
                assert value == marker
            return True
        wait_for(client)
        backup_password = secrets.token_urlsafe(24)
        archive = request(source, 'POST', '/panel/migration/export',
                          {'administrator_password':source['password'], 'password':backup_password, 'source_url':source['url']},
                          source_token, raw=True)
        assert archive.startswith(b'HL-PANEL-BACKUP')
        checks.append('real authenticated encrypted export')
        def upload(route, admin, password, extra=None, expected=200):
            boundary = 'hlmigration' + secrets.token_hex(12)
            fields = dict(administrator_password=password, password=backup_password, **(extra or {}))
            data = bytearray()
            for name, value in fields.items():
                data.extend(f'--{boundary}\r\nContent-Disposition: form-data; name="{name}"\r\n\r\n{value}\r\n'.encode())
            data.extend(f'--{boundary}\r\nContent-Disposition: form-data; name="file"; filename="panel.hlbackup"\r\nContent-Type: application/octet-stream\r\n\r\n'.encode())
            data.extend(archive); data.extend(f'\r\n--{boundary}--\r\n'.encode())
            return request(target, 'POST', '/panel/migration/'+route, bytes(data), admin, expected,
                           headers={'Content-Type':'multipart/form-data; boundary='+boundary, 'Idempotency-Key':'migration-system-import'})
        preview = upload('preview', target_token, target['password'])
        assert preview['counts']['rules'] == 1 and preview['counts']['nodes'] == 1
        assert request(target, 'GET', '/nodes', token=target_token)['items'] == []
        checks.append('preview validates counts without modifying target')
        processes[0].terminate(); processes[0].wait(timeout=15)
        receipt = upload('import', target_token, target['password'],
                         {'digest':preview['digest'], 'confirm':'RESTORE', 'target_url':target['url']})
        assert receipt['recovery_id'].startswith('mbk_')
        target['username'], target['password'] = source['username'], source['password']
        restored_token = wait_for(lambda: login(target))
        assert processes[1].poll() is None, 'Process must reload imported dependencies without exiting'
        request(target, 'GET', '/nodes', token=target_token, expected=401)
        request(target, 'GET', '/nodes', token=source_token, expected=401)
        rules = request(target, 'GET', '/forwarding-rules', token=restored_token)['items']
        assert len(rules) == 1 and rules[0]['id'] == rule['id'] and rules[0]['name'] == '迁移规则' and rules[0]['traffic_limit_bytes'] == 1000000
        recovery = request(target, 'GET', '/panel/migration/recovery/'+receipt['recovery_id'], token=restored_token, raw=True)
        assert recovery.startswith(b'HL-PANEL-BACKUP')
        checks.append('actual HTTP import reloads service, replaces administrator, revokes sessions and retains rule quota')
        spec = importlib.util.spec_from_file_location('node_move', Path(__file__).resolve().parents[2]/'deploy/node-control-url.py')
        move = importlib.util.module_from_spec(spec); spec.loader.exec_module(move)
        # Substitute only the fixture paths and unit; TLS, API, config writes and
        # systemctl restart are real. Production paths are never inspected.
        move.CONFIG, move.STATE, move.BACKUPS, move.SERVICE = config_path, state, root/'node-backups', unit+'.service'
        move.move(target['url'], str(cert))
        migrated = wait_for(lambda: applied(target, restored_token))
        assert migrated['id'] == registered['id'] and migrated['desired_generation'] == original['desired_generation']
        assert hashlib.sha256(credentials.read_bytes()).hexdigest() == identity
        assert json.loads(config_path.read_text())['control_plane_url'] == target['url']
        wait_for(client)
        checks.append('real node URL switch retains credential and generation, reports heartbeats and forwards real traffic')
        command(['runuser','-u','postgres','--','psql','-X','-d',target['db'],'-At','-v','ON_ERROR_STOP=1','-c',
                 "SELECT count(*) FROM hl_panel_migration_runtime WHERE singleton=true"])
        checks.append('migration runtime secrets persist in target database')
        print(json.dumps({'version':args.version, 'checks':checks, 'all_traffic_loopback':True}, ensure_ascii=False))
    except BaseException:
        for name in ['credentials.json', 'enrollment-attempt.json']:
            candidate = root/'node-state'/name
            if candidate.is_file():
                values = json.loads(candidate.read_bytes())
                sensitive.extend(str(v) for k, v in values.items() if 'credential' in k or 'secret' in k)
        for index in range(len(processes)):
            diagnostic = (root/('process-' + str(index) + '.log')).read_text(errors='replace')[-5000:]
            for secret in sensitive:
                diagnostic = diagnostic.replace(secret, '[hidden]')
            print('Isolated panel ' + str(index) + ' diagnostics: ' + diagnostic, flush=True)
        diagnostic = subprocess.run(['journalctl', '-u', unit+'.service', '-n', '25', '--no-pager'], capture_output=True, text=True).stdout
        for secret in sensitive:
            diagnostic = diagnostic.replace(secret, '[hidden]')
        print('Isolated node diagnostics: ' + diagnostic, flush=True)
        raise
    finally:
        subprocess.run(['systemctl','stop',unit+'.service'], capture_output=True)
        for sock in servers: sock.close()
        for process in processes:
            if process.poll() is None:
                os.killpg(process.pid, signal.SIGTERM)
                try: process.wait(timeout=15)
                except subprocess.TimeoutExpired:
                    os.killpg(process.pid, signal.SIGKILL); process.wait()
        for database in databases:
            assert database.startswith('nyvp_migration_system_'+suffix+'_') and database.endswith('_test')
            command(['runuser','-u','postgres','--','dropdb','--force',database])
        assert root.resolve().parent == Path('/var/tmp') and root.name.startswith('hl-migration-system-')
        shutil.rmtree(root)


if __name__ == '__main__':
    main()
