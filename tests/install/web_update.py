#!/usr/bin/env python3
"""Two real HTTP-triggered upgrades of an isolated signed candidate on a CI VM.

The baseline uses this source with an older build version, installed using the
existing isolated installer transport. Only fixed GitHub metadata, tagged updater
and archive URLs are mapped to the isolated candidate. HTTP API, Unix root worker,
SQL, systemd, authorization preflight and health checks execute normally.
Never run this script against a user's panel.
"""
import argparse
import hashlib
import json
import os
from pathlib import Path
import shutil
import subprocess
import tarfile
import tempfile
import time
import urllib.error
import urllib.request
import uuid

parser = argparse.ArgumentParser()
parser.add_argument('--version', required=True)
parser.add_argument('--prepare', action='store_true')
parser.add_argument('--archive-directory', type=Path)
parser.add_argument('--baseline-binary', type=Path)
parser.add_argument('--output-directory', type=Path)
args = parser.parse_args()
BASELINE = 'v0.1.0'
repository = Path(__file__).resolve().parents[2]

if args.prepare:
    assert os.environ.get('RUNNER_ENVIRONMENT') == 'github-hosted'
    assert args.archive_directory and args.baseline_binary and args.output_directory
    archive = args.archive_directory/'hl-panel-linux-amd64.tar.gz'
    digest = hashlib.sha256(archive.read_bytes()).hexdigest()
    assert (args.archive_directory/'hl-panel-linux-amd64.tar.gz.sha256').read_text().split()[0] == digest
    with tempfile.TemporaryDirectory() as tmp:
        stage = Path(tmp)
        with tarfile.open(archive, 'r:gz') as source:
            source.extractall(stage, filter='data')
        shutil.copyfile(args.baseline_binary, stage/'bin/control-api')
        (stage/'bin/control-api').chmod(0o755)
        files = sorted(path for path in stage.rglob('*') if path.is_file() and path.name != 'SHA256SUMS')
        (stage/'SHA256SUMS').write_text(''.join(hashlib.sha256(path.read_bytes()).hexdigest()+'  '+str(path.relative_to(stage))+'\n' for path in files))
        args.output_directory.mkdir()
        target = args.output_directory/archive.name
        with tarfile.open(target, 'w:gz') as output:
            for path in stage.iterdir():
                output.add(path, arcname=path.name)
        (args.output_directory/'hl-panel-linux-amd64.tar.gz.sha256').write_text(hashlib.sha256(target.read_bytes()).hexdigest()+'  '+target.name+'\n')
    print('Prepared isolated older build; upgrade will use isolated signed assets')
    raise SystemExit(0)

assert os.geteuid() == 0 and os.environ.get('HL_PANEL_DISPOSABLE_INSTALL_TEST') == 'true'
assert os.environ.get('RUNNER_ENVIRONMENT') == 'github-hosted'
assert args.archive_directory and os.environ.get('HL_PANEL_LICENSE_FIXTURE_DIR')
candidate = args.archive_directory.resolve()
metadata = {'tag_name':args.version,'draft':False,'prerelease':False,'published_at':'2026-01-01T00:00:00Z',
            'assets':[{'name':name} for name in ('hl-panel-linux-amd64.tar.gz','hl-panel-linux-amd64.tar.gz.sha256')]}
# Inject transport into this disposable installation only, never the archives.
# Both root worker imports and its child updater receive the same bounded map.
tagged_script = candidate/'isolated-tagged-updater.py'
injection = f'''
_ci_metadata = {json.dumps(metadata).encode()!r}
_ci_files = {{
    'https://raw.githubusercontent.com/Aurelian-HL/HL-Panel/{args.version}/deploy/update.py': {str(tagged_script)!r},
    REPO + '/releases/download/{args.version}/hl-panel-linux-amd64.tar.gz': {str(candidate/'hl-panel-linux-amd64.tar.gz')!r},
    REPO + '/releases/download/{args.version}/hl-panel-linux-amd64.tar.gz.sha256': {str(candidate/'hl-panel-linux-amd64.tar.gz.sha256')!r},
}}
def request(url, limit=2*1024*1024):
    if url in (API + '/releases/latest', API + '/releases/tags/{args.version}'):
        return _ci_metadata
    if url not in _ci_files:
        raise UpdateError('Unexpected isolated release URL')
    data = Path(_ci_files[url]).read_bytes()
    if len(data) > limit:
        raise UpdateError('Isolated release response too large')
    return data

'''
source = (repository/'deploy/update.py').read_text()
guard = "if __name__=='__main__':"
assert source.count(guard) == 1
isolated_source = source.replace(guard, injection+guard)
tagged_script.write_text(isolated_source)
base = 'http://127.0.0.1:19991/api/v1'
opener = urllib.request.build_opener(urllib.request.ProxyHandler({}))

def request(method, route, data=None, token=None, expected=200, identifier=None):
    headers = {'Content-Type': 'application/json'}
    if token:
        headers['Authorization'] = 'Bearer '+token
    if identifier:
        headers['Idempotency-Key'] = identifier
    req = urllib.request.Request(base+route, json.dumps(data).encode() if data is not None else None, headers, method=method)
    try:
        response = opener.open(req, timeout=25)
    except urllib.error.HTTPError as error:
        response = error
    assert response.code == expected, f'{method} {route}: HTTP {response.code}, expected {expected}'
    body = response.read()
    return json.loads(body) if body else None

assert request('GET', '/public/site-info')['platform_version'] == BASELINE
session = request('POST', '/auth/login', {'username':'admin', 'password':'123456'})
token = session['access_token']
for method in ('GET', 'POST'):
    request(method, '/panel/update', expected=401)
    request(method, '/panel/update', {}, token, expected=403)
password = 'isolated-web-update-'+os.urandom(24).hex()
request('PUT', '/auth/password', {'current_password':'123456', 'new_password':password}, token, 204)
token = request('POST', '/auth/login', {'username':'admin', 'password':password})['access_token']
request('POST', '/device-groups', {'name':'web-upgrade-durable-group', 'kind':'ENTRY', 'selection_policy':'weighted_round_robin'}, token, 201)
groups = request('GET', '/device-groups', token=token)
request('POST', '/panel/update', {'version':args.version, 'administrator_password':'wrong-isolated-password'}, token, 400, str(uuid.uuid4()))
assert request('GET', '/panel/update', token=token)['task'] is None

def fingerprint():
    return {str(path):hashlib.sha256(path.read_bytes()).hexdigest() for directory in ('/etc/hl-panel', '/etc/nginx') for path in Path(directory).rglob('*') if path.is_file() and not path.is_symlink()}

from issuer_nginx_fixture import IssuerNginxFixture
issuer_nginx = IssuerNginxFixture()
protected = fingerprint()
results = []
for round_number in (1, 2):
    Path('/opt/hl-panel/current/deploy/update.py').write_text(isolated_source)
    identifier = str(uuid.uuid4())
    started = time.monotonic()
    payload = {'version':args.version, 'administrator_password':password}
    submitted = request('POST', '/panel/update', payload, token, 202, identifier)
    assert submitted['task']['id'] == identifier
    repeated = request('POST', '/panel/update', payload, token, 202, identifier)
    assert repeated['task']['id'] == identifier
    disconnected = 0
    deadline = time.monotonic()+900
    while time.monotonic() < deadline:
        try:
            status = request('GET', '/panel/update', token=token)
        except (OSError, AssertionError):
            disconnected += 1
            time.sleep(2)
            continue
        task = status['task']
        assert task['id'] == identifier
        if task['state'] == 'failed':
            raise AssertionError('Isolated web update failed: '+task['message'])
        if task['state'] == 'succeeded':
            break
        time.sleep(2)
    else:
        raise AssertionError('Web update timed out')
    assert request('GET', '/public/site-info')['platform_version'] == args.version
    assert request('GET', '/device-groups', token=token) == groups
    request('POST', '/auth/login', {'username':'admin','password':password})
    request('POST', '/auth/login', {'username':'admin','password':'123456'}, expected=401)
    assert fingerprint() == protected
    issuer_nginx.verify()
    backup = Path(task['backup_directory'])
    assert backup.parent == Path('/var/backups/hl-panel') and (backup/'rollback.sh').is_file()
    subprocess.run(['pg_restore', '--list', str(backup/'database.dump')], stdout=subprocess.DEVNULL, check=True)
    subprocess.run(['systemctl', 'restart', 'hl-panel-update.service'], check=True)
    assert request('GET', '/panel/update', token=token)['task'] == task
    results.append({'round':round_number,'seconds':round(time.monotonic()-started,2),'temporary_disconnects':disconnected,'version':args.version,'data_and_password_preserved':True,'task_survives_api_and_worker_restarts':True,'idempotent_submission':True})
    if round_number == 1:
        subprocess.run(['bash', str(backup/'rollback.sh')], check=True)
        assert request('GET', '/public/site-info')['platform_version'] == BASELINE
        assert request('GET', '/device-groups', token=token) == groups
        assert fingerprint() == protected
        issuer_nginx.verify()

report = Path('web-update-result.json')
report.write_text(json.dumps({'real_published_github_transport':False,'isolated_signed_candidate_transport':True,'real_http_api_unix_worker_postgresql_systemd':True,'baseline':'source build '+BASELINE,'target':args.version,'rounds':results,'manual_rollback_between_rounds':True,'independent_issuer_include_and_https_route_preserved':True}, indent=2)+'\n')
report.chmod(0o644)
print('WEB_UPDATE_ACCEPTED_TWO_ROUNDS')
