#!/usr/bin/env python3
"""Real receiver installation and encrypted restore on a disposable CI VM.

Only release downloads use the candidate shim. PostgreSQL, the fixed migration
executor, stdin installation, nginx, systemd and restart isolation are real.
No production hosts, external DNS changes or ACME issuance are involved.
"""
import argparse
import base64
import hashlib
import http.client
import importlib.util
import json
import os
from pathlib import Path
import pwd
import secrets
import shutil
import socket
import ssl
import subprocess
import tempfile
import time
import urllib.error
import urllib.request
import uuid


def command(args, **options):
    result = subprocess.run(args, capture_output=True, timeout=180, **options)
    if result.returncode:
        raise RuntimeError('Disposable migration command failed: ' + Path(args[0]).name)
    return result.stdout


def request(base, route, body=None, token='', expected=200, headers=None, raw=False):
    h = dict(headers or {})
    if isinstance(body, dict):
        body = json.dumps(body).encode()
        h['Content-Type'] = 'application/json'
    if token:
        h['Authorization'] = 'Bearer ' + token
    req = urllib.request.Request(base + route, body, h)
    opener = urllib.request.build_opener(urllib.request.ProxyHandler({}))
    try:
        response = opener.open(req, timeout=110)
    except urllib.error.HTTPError as error:
        response = error
    with response:
        assert response.code == expected, f'{route}: {response.code}, expected {expected}'
        value = response.read()
        return value if raw else json.loads(value) if value else None


def verify_proxy_isolation(domain, login):
    context = ssl.create_default_context(cafile='/etc/hl-panel/tls/domain-current/fullchain.pem')
    connection = http.client.HTTPSConnection(domain, 443, context=context, timeout=30)
    connection.sock = context.wrap_socket(socket.create_connection(('127.0.0.1', 443), timeout=30),
                                          server_hostname=domain)
    try:
        connection.request('POST', '/api/v1/auth/login', json.dumps(login),
                           {'Content-Type': 'application/json'})
        response = connection.getresponse()
        assert response.status == 503, 'Nginx bypassed receiver isolation'
        response.read()
    finally:
        connection.close()


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument('--archive-directory', type=Path, required=True)
    parser.add_argument('--version', required=True)
    args = parser.parse_args()
    assert os.geteuid() == 0 and os.environ.get('HL_PANEL_DISPOSABLE_INSTALL_TEST') == 'true'
    assert not Path('/opt/hl-panel').exists(), 'Requires empty disposable VM'
    repository = Path(__file__).resolve().parents[2]
    spec = importlib.util.spec_from_file_location('executor', repository/'internal/control/panelmigration/remote.py')
    executor = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(executor)
    os.umask(0o077)
    executor.ROOT.mkdir(mode=0o700)
    task = {'id': str(uuid.uuid4()), 'domain': 'hl-migration-ci.invalid', 'version': args.version,
            'source_url': 'https://hl-migration-ci.invalid', 'public_ip': '8.8.8.8',
            'bootstrap_password': secrets.token_urlsafe(24), 'installer': (repository/'deploy/install.sh').read_text()}
    with tempfile.TemporaryDirectory(prefix='hl-auto-migration-ci-') as directory:
        work = Path(directory)
        shim = work/'curl'
        shim.write_text('''#!/usr/bin/env python3
import os, sys
from pathlib import Path
base = Path(os.environ['HL_AUTO_CANDIDATE'])
tag = os.environ['HL_AUTO_TAG']
mapping = {f'https://github.com/Aurelian-HL/HL-Panel/releases/download/{tag}/{name}': base/name
           for name in ('hl-panel-linux-amd64.tar.gz', 'hl-panel-linux-amd64.tar.gz.sha256')}
values = [('file://' + str(mapping[x])) if x in mapping else x for x in sys.argv[1:]]
os.execv(os.environ['HL_AUTO_CURL'], ['curl', *values])
''')
        shim.chmod(0o700)
        os.environ.update(HL_AUTO_CANDIDATE=str(args.archive_directory.resolve()), HL_AUTO_TAG=args.version,
                          HL_AUTO_CURL=shutil.which('curl'), PATH=str(work)+':'+os.environ['PATH'])
        assert executor.prepare(task)['state'] == 'prepared'
        assert executor.prepare(task)['state'] == 'prepared', 'Prepare retry changed task identity'
        health = request(executor.API, '/healthz')
        assert health['version'] == args.version and health['migration_isolated'] is True
        for route, body in [('/api/v1/overview', None), ('/api/v1/nodes', None),
                            ('/api/v1/panel/update', None), ('/api/v1/nodes/enroll', {}),
                            ('/api/v1/public/subscriptions/example', None)]:
            request(executor.API, route, body, expected=503)
        login = {'username': executor.BOOTSTRAP, 'password': task['bootstrap_password']}
        target_token = request(executor.API, '/api/v1/auth/login', login)['access_token']
        request(executor.API, '/api/v1/auth/login', login, expected=503, headers={'X-Forwarded-For': '127.0.0.1'})
        verify_proxy_isolation(task['domain'], login)
        public_config = command(['runuser', '-u', 'hlpanel', '--', 'cat', '/etc/hl-panel/domain.conf'])
        assert task['domain'].encode() in public_config
        assert Path('/etc/hl-panel/domain.conf').stat().st_mode & 0o777 == 0o640

        # A separate database/process supplies the final backup. Never read a live panel.
        database = 'nyvp_auto_migration_' + secrets.token_hex(6) + '_test'
        source = work/'source'
        source.mkdir(mode=0o700)
        postgres = pwd.getpwnam('postgres')
        work.chmod(0o755)
        os.chown(source, postgres.pw_uid, postgres.pw_gid)
        dsn = f'postgres://postgres@/{database}?host=/var/run/postgresql&sslmode=disable'
        dsn_file = source/'database-url'
        dsn_file.write_text(dsn); dsn_file.chmod(0o600)
        os.chown(dsn_file, postgres.pw_uid, postgres.pw_gid)
        command(['runuser', '-u', 'postgres', '--', 'createdb', database])
        binary = Path('/opt/hl-panel/current/bin/control-api')
        password = secrets.token_urlsafe(24)
        backup_password = secrets.token_urlsafe(32)
        process = None
        try:
            for action in ('apply', 'verify'):
                command(['runuser', '-u', 'postgres', '--', '/opt/hl-panel/current/bin/usage-migrate', action, '-dsn-file', str(dsn_file)])
            hashed = command([str(binary), 'hash-password'], input=(password+'\n').encode()).decode().strip()
            env = dict(os.environ, CONTROL_LISTEN_ADDRESS='127.0.0.1:19992', CONTROL_DATABASE_URL=dsn,
                       CONTROL_BOOTSTRAP_ADMIN_USERNAME='migration-source', CONTROL_BOOTSTRAP_ADMIN_PASSWORD_HASH=hashed,
                       CONTROL_CUSTOMER_PASSWORD_FINGERPRINT_KEY_B64=base64.b64encode(os.urandom(32)).decode(),
                       CONTROL_MIGRATION_BACKUP_DIR=str(source/'backups'), CONTROL_PANEL_LOG_FILE=str(source/'panel.log'))
            with (work/'source.log').open('wb') as output:
                process = subprocess.Popen(['runuser', '-u', 'postgres', '--', str(binary)], env=env, cwd=source,
                                           stdout=output, stderr=output)
            base = 'http://127.0.0.1:19992'
            for _ in range(60):
                try:
                    token = request(base, '/api/v1/auth/login', {'username': 'migration-source', 'password': password})['access_token']
                    break
                except OSError:
                    time.sleep(1)
            else:
                raise RuntimeError('Isolated source failed to start')
            request(base, '/api/v1/device-groups', {'name': 'preserved migration group', 'kind': 'ENTRY',
                    'selection_policy': 'weighted_round_robin'}, token, expected=201)
            raw = request(base, '/api/v1/panel/migration/export', {'administrator_password': password,
                          'password': backup_password, 'source_url': task['source_url']}, token, raw=True)
            form, headers = executor.migration_form(raw, password, backup_password)
            preview = request(base, '/api/v1/panel/migration/preview', form, token, headers=headers)
            assert preview['counts']['device_groups'] == 1
            restore = {**task, 'archive': base64.b64encode(raw).decode(), 'digest': hashlib.sha256(raw).hexdigest(),
                       'counts': preview['counts'], 'password': backup_password}
            result = executor.restore(restore)
            assert result['state'] == 'restored' and result['recovery_id']
            # Import reload, service restart and replay must all retain receiver isolation.
            command(['systemctl', 'restart', 'hl-panel-control-api.service'])
            executor.wait_health()
            assert request(executor.API, '/healthz')['migration_isolated'] is True
            request(executor.API, '/api/v1/overview', expected=503)
            assert executor.restore({**restore, 'bootstrap_password': 'intentionally-invalid'}) == result
            executor.verify_counts(preview['counts'])
            assert executor.activate(task)['state'] == 'waiting_dns'
            assert executor.FENCE.exists(), 'Unresolved DNS activated receiver'
            assert executor.rollback(task)['state'] == 'cancelled'
            assert executor.FENCE.exists(), 'Rollback must retain target fence'
            assert subprocess.run(['systemctl', 'is-active', '--quiet', 'hl-panel-control-api.service']).returncode != 0
        finally:
            if process is not None:
                process.terminate()
                try:
                    process.wait(timeout=20)
                except subprocess.TimeoutExpired:
                    process.kill(); process.wait()
            command(['runuser', '-u', 'postgres', '--', 'dropdb', '--force', database])
    result_file = Path('automatic-migration-result.json')
    result_file.write_text(json.dumps({'version': args.version,
        'real_stdin_installation': True, 'fresh_receiver_and_proxy_isolation': True,
        'real_encrypted_postgresql_restore': True, 'receipt_replay_without_overwrite': True,
        'restart_retains_isolation': True, 'wrong_dns_blocks_activation': True,
        'rollback_stops_target': True, 'real_domain_tls_cutover': False}, indent=2))
    # Only public acceptance flags are published; private executor files stay 0600.
    result_file.chmod(0o644)
    print('AUTOMATIC_MIGRATION_RECEIVER_ACCEPTED')


if __name__ == '__main__':
    main()
