#!/usr/bin/env python3
"""Exercise real Ubuntu 22.04 nginx variants on a disposable CI VM only."""
import hashlib
import json
import os
from pathlib import Path
import re
import subprocess
import tempfile


assert os.geteuid() == 0, 'Requires disposable root CI VM'
assert os.environ.get('HL_PANEL_DISPOSABLE_INSTALL_TEST') == 'true', 'Explicit CI isolation required'
assert 'VERSION_ID="22.04"' in Path('/etc/os-release').read_text(), 'Requires Ubuntu 22.04'
assert not Path('/opt/hl-panel').exists(), 'Requires an empty installation'
repository = Path(__file__).resolve().parents[2]
installer = repository / 'deploy/install.sh'
transcript = []


def run(command, **kwargs):
    result = subprocess.run(command, capture_output=True, text=True, timeout=240, **kwargs)
    transcript.append(result.stdout + result.stderr)
    Path('nginx-compatibility.log').write_text('\n'.join(transcript))
    return result


def nginx_state():
    files = {str(path): hashlib.sha256(path.read_bytes()).hexdigest()
             for path in Path('/etc/nginx').rglob('*') if path.is_file()}
    active = subprocess.run(['systemctl', 'is-active', 'nginx'], capture_output=True, text=True).stdout
    return files, active


environment = dict(os.environ, DEBIAN_FRONTEND='noninteractive')
run(['apt-get', 'update'], env=environment).check_returncode()
run(['apt-get', 'install', '-y', '--no-install-recommends', 'nginx-light'], env=environment).check_returncode()
before = nginx_state()
with tempfile.TemporaryDirectory(prefix='hl-nginx-regression-') as temporary:
    # Any package mutation before the compatibility rejection is a test failure.
    apt = Path(temporary) / 'apt-get'
    apt.write_text('#!/bin/sh\necho UNEXPECTED_APT_MUTATION >&2\nexit 99\n')
    apt.chmod(0o755)
    failed = run(['bash', str(installer), '--domain', 'hl-panel-smoke.invalid',
                  '--public-ip', '8.8.8.8', '--api-port', '19991', '--ip-https-port', '19443'],
                 env=dict(os.environ, PATH=temporary + ':' + os.environ['PATH']))
assert failed.returncode != 0
assert 'unknown directive "limit_req_zone"' in failed.stderr, failed.stderr
assert '尚未创建面板账号、文件或数据库' in failed.stderr, failed.stderr
assert 'UNEXPECTED_APT_MUTATION' not in failed.stderr
assert nginx_state() == before, 'Existing Nginx configuration or service state changed'
assert not list(Path('/tmp').glob('hl-panel-nginx-check.*')), 'Compatibility check leaked temporary files'
assert subprocess.run(['id', 'hlpanel'], capture_output=True).returncode != 0
for path in ['/opt/hl-panel', '/etc/hl-panel', '/var/lib/hl-panel', '/var/lib/hl-panel-acme',
             '/etc/nginx/conf.d/hl-panel-rate-limit.conf',
             '/etc/systemd/system/hl-panel-control-api.service']:
    assert not Path(path).exists(), 'Failed preflight created ' + path

# Switching packages is authorized only on this disposable compatibility VM.
run(['apt-get', 'install', '-y', '--no-install-recommends', 'nginx-core'], env=environment).check_returncode()
source = installer.read_text()
function = re.search(r'^check_nginx_capabilities\(\) \{.*?^\}\n(?=\nresolve_domain_records)',
                     source, re.MULTILINE | re.DOTALL)
assert function, 'Installer compatibility function not found'
verified = run(['bash', '-euc', 'log() { printf "%s\\n" "$*"; }; '
                'fail() { printf "%s\\n" "$*" >&2; exit 1; };\n'
                + function.group() + '\ncheck_nginx_capabilities\n'])
verified.check_returncode()
assert 'Nginx 模块检查通过' in verified.stdout
run(['nginx', '-t']).check_returncode()
Path('nginx-compatibility-result.json').write_text(json.dumps({
    'ubuntu': '22.04', 'real_nginx_light_failure_reproduced': True,
    'installer_rejected_before_resource_creation': True,
    'existing_nginx_configuration_and_service_preserved': True,
    'real_nginx_core_capabilities_verified': True,
}, indent=2))
print('NGINX_COMPATIBILITY_ACCEPTED')
