#!/usr/bin/env python3
"""Install the candidate on a disposable Linux CI VM, never a live VPS.

Only GitHub release transport is redirected to the just-built candidate.
apt, PostgreSQL, systemd, nginx, TLS, password changes and restart durability
are real. Run as root in the dedicated fresh-install GitHub Actions job.
"""
import argparse
import codecs
import errno
import json
import os
from pathlib import Path
import pty
import select
import shlex
import shutil
import ssl
import subprocess
import sys
import tempfile
import time
import urllib.error
import urllib.request

parser = argparse.ArgumentParser()
parser.add_argument('--archive-directory', required=True)
parser.add_argument('--version', required=True)
parser.add_argument('--credentials', choices=['default', 'custom'], required=True)
args = parser.parse_args()
assert os.geteuid() == 0, 'Requires disposable root CI VM'
assert os.environ.get('HL_PANEL_DISPOSABLE_INSTALL_TEST') == 'true', 'Explicit CI isolation required'
assert not Path('/opt/hl-panel').exists(), 'Requires an empty installation'
repository = Path(__file__).resolve().parents[2]
archive_directory = Path(args.archive_directory).resolve()
custom = args.credentials == 'custom'
username = 'ci.operator' if custom else 'admin'
# Test-only generated credential, never a real user's password.
password = 'test-' + os.urandom(24).hex() if custom else '123456'
replacement = 'test-' + os.urandom(24).hex()

with tempfile.TemporaryDirectory(prefix='hl-install-smoke-') as temporary:
    work = Path(temporary)
    (work / 'release.json').write_text(json.dumps({'tag_name': args.version}))
    real_curl = shutil.which('curl')
    assert real_curl
    shim = work / 'curl'
    shim.write_text('''#!/usr/bin/env python3
import os, sys
from pathlib import Path
base = Path(os.environ['HL_PANEL_CANDIDATE_DIR'])
repo = Path(os.environ['HL_PANEL_CANDIDATE_REPO'])
tag = os.environ['HL_PANEL_CANDIDATE_TAG']
mapping = {
 'https://api.github.com/repos/Aurelian-HL/HL-Panel/releases/latest': Path(os.environ['HL_PANEL_CANDIDATE_META']),
 f'https://raw.githubusercontent.com/Aurelian-HL/HL-Panel/{tag}/deploy/install.sh': repo / 'deploy/install.sh',
}
for name in ['hl-panel-linux-amd64.tar.gz', 'hl-panel-linux-amd64.tar.gz.sha256']:
 mapping[f'https://github.com/Aurelian-HL/HL-Panel/releases/download/{tag}/{name}'] = base / name
values = [('file://' + str(mapping[x])) if x in mapping else x for x in sys.argv[1:]]
os.execv(os.environ['HL_PANEL_REAL_CURL'], ['curl', *values])
''')
    shim.chmod(0o755)
    environment = dict(os.environ, PATH=str(work) + ':' + os.environ['PATH'],
                       HL_PANEL_CANDIDATE_DIR=str(archive_directory),
                       HL_PANEL_CANDIDATE_REPO=str(repository),
                       HL_PANEL_CANDIDATE_TAG=args.version,
                       HL_PANEL_CANDIDATE_META=str(work / 'release.json'),
                       HL_PANEL_REAL_CURL=real_curl)
    command = ['bash', str(repository / 'install.sh'),
               '--public-ip', '8.8.8.8', '--api-port', '19991', '--ip-https-port', '19443']
    if custom:
        command.extend(['--domain', 'hl-panel-smoke.invalid'])
    pid, terminal = pty.fork()
    if pid == 0:
        os.execve('/bin/bash', command, environment)
    transcript = ''
    prompt_buffer = ''
    answered = set()
    prompts = [('设置面板域名 [回车使用 IP 访问]：', ''),
               ('设置 HL-panel 管理员账号 [admin]：', username if custom else ''),
               ('设置 HL-panel 管理员密码 [回车使用 123456]：', password if custom else ''),
               ('再次输入管理员密码：', password)]
    deadline = time.monotonic() + 900
    decoder = codecs.getincrementaldecoder('utf-8')(errors='replace')
    while time.monotonic() < deadline:
        ready, _, _ = select.select([terminal], [], [], 2)
        if ready:
            try:
                chunk = os.read(terminal, 65536)
            except OSError as error:
                if error.errno == errno.EIO:
                    break
                raise
            if not chunk:
                break
            text = decoder.decode(chunk)
            transcript += text
            prompt_buffer = (prompt_buffer + text)[-4000:]
            for prompt, answer in prompts:
                if prompt not in answered and prompt in prompt_buffer:
                    os.write(terminal, (answer + '\n').encode())
                    answered.add(prompt)
        finished, status = os.waitpid(pid, os.WNOHANG)
        if finished:
            break
    else:
        os.kill(pid, 15)
        raise RuntimeError('Installer timed out')
    try:
        _, status = os.waitpid(pid, 0)
    except ChildProcessError:
        pass
    os.close(terminal)
    safe_transcript = transcript.replace(password, '[hidden]') if custom else transcript
    Path('fresh-install.log').write_text(safe_transcript)
    if os.waitstatus_to_exitcode(status) != 0:
        print(safe_transcript[-20000:])
        raise RuntimeError('Fresh installer failed')
    assert username in transcript and args.version in transcript
    assert 'https://8.8.8.8:19443/' in transcript
    assert '123456' in transcript if not custom else password not in transcript
    if os.environ.get('HL_PANEL_EXPECT_NGINX_ADAPTATION') == 'true':
        assert 'Nginx 自动适配完成' in transcript, 'Existing nginx-light was not automatically adapted'
    # Repeat install must reject its own existing state, before prompting.
    repeated = subprocess.run(command, env=environment, input='', capture_output=True, text=True)
    assert repeated.returncode != 0, 'Installer accepted overwrite'

base = 'http://127.0.0.1:19991/api/v1'

def request(method, route, data=None, token=None, expected=200):
    headers = {'Content-Type': 'application/json'}
    if token:
        headers['Authorization'] = 'Bearer ' + token
    req = urllib.request.Request(base + route, json.dumps(data).encode() if data is not None else None, headers, method=method)
    try:
        response = urllib.request.urlopen(req, timeout=15)
    except urllib.error.HTTPError as error:
        response = error
    assert response.code == expected, f'{method} {route}: {response.code}, expected {expected}'
    body = response.read()
    return json.loads(body) if body else None

def login(secret):
    return request('POST', '/auth/login', {'username': username, 'password': secret})

session = login(password)
token = session['access_token']
assert session['user']['must_change_password'] is (not custom)
request('GET', '/auth/me', token=token)
request('GET', '/overview', token=token, expected=200 if custom else 403)
request('GET', '/usage', token=token, expected=200 if custom else 403)
if not custom:
    request('POST', '/device-groups', {}, token, expected=403)
    request('POST', '/usage/enforcement/test/revoke', {}, token, expected=403)
    request('PUT', '/auth/password', {'current_password': password, 'new_password':'short'}, token, expected=400)
subprocess.run(['systemctl', 'restart', 'hl-panel-control-api.service'], check=True)
for _ in range(30):
    try:
        state = request('GET', '/auth/me', token=token)
        break
    except (OSError, AssertionError):
        time.sleep(1)
else:
    raise RuntimeError('API did not restart')
assert state['user']['must_change_password'] is (not custom)
request('PUT', '/auth/password', {'current_password': password, 'new_password':replacement}, token, expected=204)
request('GET', '/auth/me', token=token, expected=401)
request('POST', '/auth/login', {'username':username, 'password':password}, expected=401)
session = login(replacement)
assert session['user']['must_change_password'] is False
overview = request('GET', '/overview', token=session['access_token'])
assert overview['nodes'] == []
assert overview['node_count'] == 0
assert overview['group_count'] == 0
assert overview['online_node_count'] == 0
assert overview['syncing_node_count'] == 0
assert overview['failed_apply_count'] == 0
assert overview['panel']['status'] == 'online'
assert overview['panel']['version'] == args.version
assert overview['panel']['resources']['ip_collection_status'] == 'collected', 'systemd sandbox must allow interface collection'
import time
time.sleep(0.25)
resources = request('GET', '/panel/runtime', token=session['access_token'])['resources']
for metric in ('cpu_percent', 'memory_used_bytes', 'memory_total_bytes', 'swap_used_bytes',
               'swap_total_bytes', 'load_average_1', 'uptime_seconds'):
    assert metric in resources, 'Host metric hidden by service sandbox: ' + metric
assert resources['memory_total_bytes'] > 0
assert 0 <= resources['cpu_percent'] <= 100
assert request('GET', '/device-groups', token=session['access_token'])['items'] == []
web_update = request('GET', '/panel/update', token=session['access_token'])
assert web_update['available'] is True and web_update['task'] is None
request('GET', '/panel/update', expected=401)
request('POST', '/panel/update', {}, expected=401)
subprocess.run(['nginx', '-t'], check=True)
with urllib.request.urlopen('https://127.0.0.1:19443/', context=ssl._create_unverified_context(), timeout=15) as page:
    assert b'<html' in page.read()
env = dict(os.environ)
for line in Path('/etc/hl-panel/control-api.env').read_text().splitlines():
    if line and not line.startswith('#'):
        key, value = line.split('=', 1)
        env[key] = shlex.split(value)[0]
subprocess.run(['/opt/hl-panel/current/bin/control-api','reset-admin-password', username], env=env, input='123456\n', capture_output=True, text=True, check=True)
request('GET', '/auth/me', token=session['access_token'], expected=401)
assert login('123456')['user']['must_change_password'] is True
Path('fresh-install-result.json').write_text(json.dumps({
    'version': args.version, 'credentials': args.credentials, 'fresh_install': True,
    'real_postgresql_systemd_nginx': True, 'password_change_and_session_revocation': True,
    'restart_durability': True, 'repeat_install_rejected': True, 'local_password_reset': True,
    'unbound_domain_ip_https': True,
    'panel_host_metrics': True,
    'web_update_socket_and_admin_authorization': True,
    'nginx_automatic_adaptation': 'Nginx 自动适配完成' in transcript,
}, indent=2))
print('FRESH_INSTALL_ACCEPTED: ' + args.credentials)
