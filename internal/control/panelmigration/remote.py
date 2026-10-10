"""Fixed root migration executor. Secrets arrive only on stdin; never print them."""
import base64
import fcntl
import hashlib
import http.client
import ipaddress
import json
import os
from pathlib import Path
import re
import secrets
import shlex
import signal
import socket
import ssl
import subprocess
import sys
import tempfile
import time
import urllib.request

ROOT = Path('/var/lib/hl-panel-remote-migration')
MARKER = ROOT / 'task.json'
STATE = Path('/var/lib/hl-panel/automatic-migration')
FENCE = STATE / 'fence.json'
API = 'http://127.0.0.1:8080'
BOOTSTRAP = 'hl-migration-bootstrap'
TABLES = ('usage_ingest_cursors', 'usage_events', 'usage_customer_totals',
          'usage_enforcement_decisions', 'usage_enforcement_results')
COLLECTIONS = {'administrators': 'Administrators', 'customers': 'Customers',
               'device_groups': 'DeviceGroups', 'nodes': 'Nodes', 'rules': 'ForwardRules',
               'subscriptions': 'Subscriptions', 'rule_groups': 'RuleGroups', 'user_groups': 'UserGroups'}


class Failure(Exception):
    pass


def require(condition, code):
    if not condition:
        raise Failure(code)


def save(path, value):
    require(not path.is_symlink(), 'marker')
    with tempfile.NamedTemporaryFile(dir=path.parent, prefix='.migration-', delete=False) as stream:
        name = stream.name
        try:
            os.chmod(name, 0o600)
            stream.write(json.dumps(value).encode())
            stream.flush()
            os.fsync(stream.fileno())
            os.replace(name, path)
            descriptor = os.open(path.parent, os.O_RDONLY | os.O_DIRECTORY)
            try:
                os.fsync(descriptor)
            finally:
                os.close(descriptor)
        finally:
            if os.path.exists(name):
                os.unlink(name)


def load_marker(task_id):
    require(MARKER.is_file() and not MARKER.is_symlink(), 'marker')
    marker = json.loads(MARKER.read_text())
    require(marker.get('id') == task_id, 'marker')
    return marker


def command(args, code='internal', **options):
    result = subprocess.run(args, capture_output=True, timeout=180, **options)
    require(result.returncode == 0, code)
    return result.stdout


def wait_health():
    for _ in range(60):
        try:
            local('/healthz')
            return
        except Exception:
            time.sleep(1)
    raise Failure('health')


def local(path, data=None, token='', headers=None):
    if data is not None and not isinstance(data, bytes):
        data = json.dumps(data).encode()
    merged = {'Content-Type': 'application/json'}
    if token:
        merged['Authorization'] = 'Bearer ' + token
    merged.update(headers or {})
    request = urllib.request.Request(API + path, data=data, headers=merged)
    opener = urllib.request.build_opener(urllib.request.ProxyHandler({}))
    with opener.open(request, timeout=110) as response:
        return json.loads(response.read(65536))


def migration_form(raw, administrator_password, password, digest='', task_id='', target_url=''):
    boundary = 'hlmigration' + secrets.token_hex(16)
    values = {'administrator_password': administrator_password, 'password': password}
    if digest:
        values.update(digest=digest, confirm='RESTORE', target_url=target_url)
    parts = []
    for name, value in values.items():
        parts.append((f'--{boundary}\r\nContent-Disposition: form-data; name="{name}"\r\n\r\n').encode() + value.encode() + b'\r\n')
    parts.append((f'--{boundary}\r\nContent-Disposition: form-data; name="file"; filename="migration.hlbackup"\r\nContent-Type: application/octet-stream\r\n\r\n').encode() + raw + b'\r\n')
    parts.append(f'--{boundary}--\r\n'.encode())
    return b''.join(parts), {'Content-Type': 'multipart/form-data; boundary=' + boundary, 'Idempotency-Key': task_id}


def sql(statement):
    return command(['runuser', '-u', 'postgres', '--', 'psql', '-XAt', '-v', 'ON_ERROR_STOP=1',
                    '-d', 'hl_panel_control', '-c', statement], code='restore').decode().strip()


def receipt(task_id):
    # task_id is a validated UUID; SQL identifiers and statements are fixed.
    row = sql("SELECT digest || '|' || recovery_id FROM hl_panel_migration_receipts WHERE operation_key='" + task_id + "'")
    return row.split('|') if row else None


def verify_counts(expected):
    snapshot = json.loads(sql("SELECT convert_from(payload, 'UTF8') FROM nyvp_control_snapshots WHERE singleton=true"))
    for label, name in COLLECTIONS.items():
        require(len(snapshot.get(name) or []) == expected.get(label, -1), 'counts')
    for table in TABLES:
        require(int(sql('SELECT count(*) FROM ' + table)) == expected.get(table, -1), 'counts')


def reset_bootstrap(password):
    env = os.environ.copy()
    for line in Path('/etc/hl-panel/control-api.env').read_text().splitlines():
        if line and not line.startswith('#') and '=' in line:
            name, value = line.split('=', 1)
            values = shlex.split(value)
            env[name] = values[0] if values else ''
    command(['/opt/hl-panel/current/bin/control-api', 'reset-admin-password', BOOTSTRAP],
            code='restore', input=(password + '\n').encode(), env=env, cwd='/var/lib/hl-panel')
    command(['systemctl', 'restart', 'hl-panel-control-api.service'], code='restore')
    wait_health()


def prepare(data):
    if MARKER.exists():
        marker = load_marker(data['id'])
        require(marker.get('domain') == data['domain'] and marker.get('version') == data['version'] and
                marker.get('public_ip') == data['public_ip'] and marker.get('source_url') == data['source_url'], 'marker')
        require(marker.get('state') in ('preparing', 'prepared'), 'marker')
    else:
        require(not any(Path(path).exists() for path in ('/opt/hl-panel', '/etc/hl-panel', '/var/lib/hl-panel')), 'occupied')
        marker = {key: data[key] for key in ('id', 'domain', 'version', 'source_url', 'public_ip')}
        marker['state'] = 'preparing'
        save(MARKER, marker)
    if not Path('/opt/hl-panel/current/bin/control-api').is_file():
        with tempfile.TemporaryDirectory(prefix='hl-panel-migrate-') as directory:
            script = Path(directory) / 'install.sh'
            script.write_text(data['installer'])
            script.chmod(0o700)
            log = ROOT / ('install-' + data['id'] + '.log')
            with log.open('ab') as output:
                os.chmod(log, 0o600)
                process = subprocess.Popen(['bash', str(script), '--version', data['version'], '--domain', data['domain'],
                                         '--public-ip', data['public_ip'], '--admin-username', BOOTSTRAP,
                                         '--password-stdin', '--migration-receiver', data['id']],
                                        stdin=subprocess.PIPE, stdout=output, stderr=output, start_new_session=True)
                try:
                    process.communicate((data['bootstrap_password'] + '\n').encode(), timeout=1800)
                finally:
                    if process.poll() is None:
                        os.killpg(process.pid, signal.SIGTERM)
                        try:
                            process.wait(timeout=15)
                        except subprocess.TimeoutExpired:
                            os.killpg(process.pid, signal.SIGKILL)
                            process.wait()
            require(process.returncode == 0, 'install')
    require(FENCE.is_file() and not FENCE.is_symlink(), 'marker')
    fence = json.loads(FENCE.read_text())
    require(fence == {'role': 'receiver', 'id': data['id']}, 'marker')
    wait_health()
    health = local('/healthz')
    require(health.get('version') == data['version'], 'version')
    require(health.get('migration_isolated') is True, 'marker')
    if marker['state'] == 'preparing':
        marker['state'] = 'prepared'
        save(MARKER, marker)
    return {'state': 'prepared'}


def restore(data):
    marker = load_marker(data['id'])
    require(marker.get('state') in ('prepared', 'restored'), 'marker')
    require(FENCE.is_file(), 'marker')
    require(json.loads(FENCE.read_text()) == {'role': 'receiver', 'id': data['id']}, 'marker')
    previous = receipt(data['id'])
    if previous:
        require(previous[0] == data['digest'], 'marker')
        recovery_id = previous[1]
    else:
        reset_bootstrap(data['bootstrap_password'])
        token = local('/api/v1/auth/login', {'username': BOOTSTRAP, 'password': data['bootstrap_password']})['access_token']
        raw = base64.b64decode(data['archive'], validate=True)
        require(len(raw) <= 64 << 20 and hashlib.sha256(raw).hexdigest() == data['digest'], 'invalid')
        form, headers = migration_form(raw, data['bootstrap_password'], data['password'])
        preview = local('/api/v1/panel/migration/preview', form, token, headers)
        require(preview['digest'] == data['digest'] and preview['counts'] == data['counts'], 'counts')
        archive = ROOT / ('final-' + data['id'] + '.hlbackup')
        with archive.open('wb') as output:
            os.chmod(archive, 0o600)
            output.write(raw)
            output.flush()
            os.fsync(output.fileno())
        form, headers = migration_form(raw, data['bootstrap_password'], data['password'], data['digest'], data['id'], data['source_url'])
        try:
            result = local('/api/v1/panel/migration/import', form, token, headers)
            recovery_id = result['recovery_id']
        except Exception:
            # The import may have committed before the connection was lost.
            previous = receipt(data['id'])
            require(previous and previous[0] == data['digest'], 'restore')
            recovery_id = previous[1]
    verify_counts(data['counts'])
    marker.update(state='restored', digest=data['digest'], recovery_id=recovery_id)
    save(MARKER, marker)
    wait_health()
    return {'state': 'restored', 'recovery_id': recovery_id}


def dns_ready(domain, ip):
    try:
        addresses = {entry[4][0] for entry in socket.getaddrinfo(domain, 443, 0, socket.SOCK_STREAM)}
        return addresses == {ip}
    except OSError:
        return False


def public_health(domain, ip):
    # Dial the target explicitly while still validating the original hostname.
    connection = http.client.HTTPSConnection(domain, timeout=15, context=ssl.create_default_context())
    connection.sock = ssl.create_default_context().wrap_socket(socket.create_connection((ip, 443), timeout=15), server_hostname=domain)
    try:
        connection.request('GET', '/healthz')
        response = connection.getresponse()
        require(response.status == 200, 'health')
        result = json.loads(response.read(65536))
        require(result.get('status') == 'ok', 'health')
        return result
    finally:
        connection.close()


def activate(data):
    marker = load_marker(data['id'])
    require(marker['state'] in ('restored', 'active'), 'marker')
    if not dns_ready(marker['domain'], marker['public_ip']):
        return {'state': 'waiting_dns'}
    command(['/usr/local/sbin/hl-panel-enable-domain-tls'], code='tls')
    public_health(marker['domain'], marker['public_ip'])
    if FENCE.exists():
        require(json.loads(FENCE.read_text()) == {'role': 'receiver', 'id': data['id']}, 'marker')
        # Commit is recorded before lifting the fence: retries never restore again.
        marker['state'] = 'active'
        save(MARKER, marker)
        FENCE.unlink()
        descriptor = os.open(STATE, os.O_RDONLY | os.O_DIRECTORY)
        try:
            os.fsync(descriptor)
        finally:
            os.close(descriptor)
    else:
        require(marker['state'] == 'active', 'marker')
    # A prior restart can have failed after removing the durable fence.
    command(['systemctl', 'restart', 'hl-panel-control-api.service'], code='health')
    wait_health()
    health = public_health(marker['domain'], marker['public_ip'])
    require(health.get('version') == marker['version'] and health.get('migration_isolated') is False, 'health')
    import pwd
    user = pwd.getpwnam('hlpanel')
    save(STATE / 'received.json', {'id': data['id'], 'state': 'completed', 'phase': 'completed',
                                 'message': '迁移恢复与 HTTPS 切换完成，旧机保持隔离。',
                                 'source_url': marker['source_url'], 'target': {'host': marker['public_ip'], 'port': 22, 'fingerprint': ''},
                                 'version': marker['version'], 'backup_id': '', 'domain': marker['domain']})
    os.chown(STATE / 'received.json', user.pw_uid, user.pw_gid)
    marker['state'] = 'active'
    save(MARKER, marker)
    return {'state': 'completed'}


def rollback(data):
    marker = load_marker(data['id'])
    require(marker['state'] != 'active', 'marker')
    if Path('/etc/systemd/system/hl-panel-control-api.service').exists():
        # A restored target must still be fenced until it is explicitly active.
        # Treat a missing or mismatched fence as an unsafe inconsistent state;
        # silently cancelling here could leave the receiver serving traffic.
        require(FENCE.is_file() and not FENCE.is_symlink() and json.loads(FENCE.read_text()) == {'role': 'receiver', 'id': data['id']}, 'marker')
        command(['systemctl', 'stop', 'hl-panel-control-api.service'])
        check = subprocess.run(['systemctl', 'is-active', '--quiet', 'hl-panel-control-api.service'])
        require(check.returncode != 0, 'health')
    marker['state'] = 'cancelled'
    save(MARKER, marker)
    return {'state': 'cancelled'}


def main():
    os.umask(0o077)
    data = json.load(sys.stdin)
    require(os.geteuid() == 0, 'unsupported')
    require(re.fullmatch(r'[a-f0-9]{8}(-[a-f0-9]{4}){3}-[a-f0-9]{12}', data.get('id', '')), 'invalid')
    require(data.get('action') in ('prepare', 'restore', 'activate', 'rollback'), 'invalid')
    if data['action'] == 'prepare':
        require(re.fullmatch(r'v[0-9]+\.[0-9]+\.[0-9]+', data.get('version', '')), 'version')
        require(re.fullmatch(r'([a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?\.)+[a-z]{2,63}', data.get('domain', '')), 'invalid')
        require(ipaddress.ip_address(data['public_ip']).version == 4 and ipaddress.ip_address(data['public_ip']).is_global, 'invalid')
    require(not ROOT.is_symlink(), 'marker')
    ROOT.mkdir(mode=0o700, exist_ok=True)
    lock = ROOT / 'operation.lock'
    require(not lock.is_symlink(), 'marker')
    with lock.open('a') as handle:
        try:
            fcntl.flock(handle, fcntl.LOCK_EX | fcntl.LOCK_NB)
        except BlockingIOError:
            raise Failure('busy')
        return {'prepare': prepare, 'restore': restore, 'activate': activate, 'rollback': rollback}[data['action']](data)


if __name__ == '__main__':
    def interrupted(_signum, _frame):
        raise Failure('internal')
    signal.signal(signal.SIGHUP, interrupted)
    signal.signal(signal.SIGTERM, interrupted)
    try:
        print(json.dumps(main()))
    except Failure as error:
        print(json.dumps({'code': str(error)}))
        sys.exit(1)
    except Exception:
        print(json.dumps({'code': 'internal'}))
        sys.exit(1)
