#!/usr/bin/env python3
"""Real upgrade/rollback on the dedicated disposable Linux CI VM only."""
import argparse
import hashlib
import importlib.util
import json
import os
from pathlib import Path
import subprocess
import sys
import urllib.error
import urllib.request

parser = argparse.ArgumentParser()
parser.add_argument('--archive-directory', required=True)
parser.add_argument('--version', required=True)
parser.add_argument('--previous-version', required=True)
args = parser.parse_args()
assert os.geteuid() == 0 and os.environ.get('HL_PANEL_DISPOSABLE_INSTALL_TEST') == 'true'
repository = Path(__file__).resolve().parents[2]
archive = Path(args.archive_directory).resolve()
base = 'http://127.0.0.1:19991/api/v1'
opener = urllib.request.build_opener(urllib.request.ProxyHandler({}))

def request(method, route, data=None, token=None, expected=200):
    headers = {'Content-Type': 'application/json'}
    if token:
        headers['Authorization'] = 'Bearer ' + token
    req = urllib.request.Request(base+route, json.dumps(data).encode() if data is not None else None, headers, method=method)
    try:
        response = opener.open(req, timeout=20)
    except urllib.error.HTTPError as error:
        response = error
    assert response.code == expected, f'{method} {route}: {response.code}, expected {expected}'
    body = response.read()
    return json.loads(body) if body else None

session = request('POST','/auth/login',{'username':'admin','password':'123456'})
secret = 'isolated-test-' + os.urandom(24).hex()
request('PUT','/auth/password',{'current_password':'123456','new_password':secret},session['access_token'],204)
token = request('POST','/auth/login',{'username':'admin','password':secret})['access_token']
request('POST','/device-groups',{'name':'upgrade-persistence','kind':'ENTRY','selection_policy':'weighted_round_robin','description':'isolated CI fixture'},token,201)
groups = request('GET','/device-groups',token=token)
assert len(groups['items']) == 1
previous = Path('/opt/hl-panel/current').resolve()

def fingerprint():
    values={}
    for directory in ('/etc/hl-panel','/etc/nginx','/etc/systemd/system/hl-panel-control-api.service'):
        root=Path(directory)
        for path in ([root] if root.is_file() else root.rglob('*')):
            if path.is_symlink():
                values[str(path)]='link:'+os.readlink(path)
            elif path.is_file():
                values[str(path)]=hashlib.sha256(path.read_bytes()).hexdigest()
    return values

protected = fingerprint()
spec = importlib.util.spec_from_file_location('hl_panel_test_updater',repository/'deploy/update.py')
update = importlib.util.module_from_spec(spec)
spec.loader.exec_module(update)
assert update.version_tuple(args.version)>update.version_tuple(args.previous_version)
metadata = {'tag_name':args.version,'draft':False,'prerelease':False,'published_at':'2026-01-01T00:00:00Z',
            'assets':[{'name':name} for name in ('hl-panel-linux-amd64.tar.gz','hl-panel-linux-amd64.tar.gz.sha256')]}
corrupt = False

def release_transport(url,limit=2*1024*1024):
    # Redirect only fixed GitHub release metadata/assets to the candidate.
    # Service, SQL backup/restore, migrations, users and files are not mocked.
    if url in (update.API+'/releases/latest',update.API+'/releases/tags/'+args.version):
        return json.dumps(metadata).encode()
    for name in ('hl-panel-linux-amd64.tar.gz','hl-panel-linux-amd64.tar.gz.sha256'):
        if url==update.REPO+'/releases/download/'+args.version+'/'+name:
            if corrupt and name.endswith('.sha256'):
                return ('0'*64+'  hl-panel-linux-amd64.tar.gz\n').encode()
            data=(archive/name).read_bytes()
            assert len(data)<=limit
            return data
    raise AssertionError('Unexpected release URL')

update.request=release_transport
def invoke(*options):
    sys.argv=['update.py',*options]
    update.main()

def verify(version):
    assert request('GET','/public/site-info')['platform_version']==version
    me=request('GET','/auth/me',token=token)
    assert me['user']['must_change_password'] is False
    request('POST','/auth/login',{'username':'admin','password':secret})
    request('POST','/auth/login',{'username':'admin','password':'123456'},expected=401)
    assert request('GET','/device-groups',token=token)==groups
    expected = dict(protected)
    locations = Path('/etc/nginx/snippets/hl-panel-app-locations.conf')
    if version == args.version:
        original = (previous/'deploy/nginx/snippets/hl-panel-app-locations.conf').read_text()
        candidate = (repository/'deploy/nginx/snippets/hl-panel-app-locations.conf').read_text()
        if update.SUBSCRIPTION_LOCATION in candidate and update.SUBSCRIPTION_LOCATION not in original:
            expected[str(locations)] = hashlib.sha256((original.rstrip()+'\n\n'+update.SUBSCRIPTION_LOCATION+'\n').encode()).hexdigest()
    assert fingerprint()==expected,'Configuration or certificates changed beyond the managed subscription location'
    subprocess.run(['nginx','-t'],check=True)

# Invalid SHA must fail without stopping the service or switching binaries.
corrupt=True
try:
    invoke('--version',args.version)
    raise AssertionError('Corrupt archive accepted')
except update.UpdateError:
    pass
assert Path('/opt/hl-panel/current').resolve()==previous
verify(args.previous_version)
corrupt=False

# The candidate really starts, then fault injection forces the production
# rollback path to restore PostgreSQL and the old binary.
real_healthy=update.healthy
def reject_candidate(origin,version):
    real_healthy(origin,version)
    if version==args.version:
        raise update.UpdateError('Isolated CI post-start failure')
update.healthy=reject_candidate
try:
    invoke('--version',args.version)
    raise AssertionError('Expected forced update failure')
except update.UpdateError:
    pass
assert Path('/opt/hl-panel/current').resolve()==previous
verify(args.previous_version)

update.healthy=real_healthy
invoke('--version',args.version)
verify(args.version)
invoke('--version',args.version,'--check')
invoke('--version',args.version) # A repeated same-version update is a no-op.
assert Path('/usr/local/sbin/hl-panel-update').stat().st_mode&0o777==0o755
assert Path('/opt/hl-panel/current').resolve().stat().st_mode&0o777==0o755
backups=[p for p in Path('/var/backups/hl-panel').iterdir() if (p/'database.dump').is_file()]
assert len(backups)==2
for backup in backups:
    assert backup.stat().st_mode&0o077==0
    subprocess.run(['pg_restore','--list',str(backup/'database.dump')],stdout=subprocess.DEVNULL,check=True)
    subprocess.run(['bash','-n',str(backup/'rollback.sh')],check=True)
report = Path('preserve-update-result.json')
report.write_text(json.dumps({
    'previous_version':args.previous_version,'version':args.version,
    'real_postgresql_systemd_nginx':True,'corrupt_archive_rejected_before_stop':True,
    'failed_upgrade_restores_database_and_binary':True,'successful_upgrade':True,
    'modified_password_and_session_preserved':True,'device_group_data_preserved':True,
    'configuration_and_certificates_preserved':True,'verified_database_backups':len(backups),
    'same_version_no_op':True,
},indent=2)+'\n')
# The root updater intentionally sets umask 077. This report contains only
# public test outcomes; allow the unprivileged Actions uploader to read it.
report.chmod(0o644)
print('PRESERVE_UPDATE_ACCEPTED')
