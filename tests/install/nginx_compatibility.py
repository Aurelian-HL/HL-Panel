#!/usr/bin/env python3
"""Exercise real Ubuntu 22.04 nginx variants on a disposable CI VM only."""
import hashlib
import json
import os
from pathlib import Path
import re
import subprocess
import urllib.request


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
             for folder in ['sites-available', 'sites-enabled']
             for path in (Path('/etc/nginx') / folder).rglob('*') if path.is_file()}
    files['/etc/nginx/nginx.conf'] = hashlib.sha256(Path('/etc/nginx/nginx.conf').read_bytes()).hexdigest()
    active = subprocess.run(['systemctl', 'is-active', 'nginx'], capture_output=True, text=True).stdout
    return files, active


def original_site_works():
    with urllib.request.urlopen('http://127.0.0.1:19996/', timeout=10) as response:
        assert response.status == 200 and response.read() == b'original-site-ok\n'


environment = dict(os.environ, DEBIAN_FRONTEND='noninteractive')
run(['apt-get', 'update'], env=environment).check_returncode()
run(['apt-get', 'install', '-y', '--no-install-recommends', 'nginx-light'], env=environment).check_returncode()
site = Path('/etc/nginx/sites-available/hl-nginx-compat.conf')
site.write_text('server { listen 127.0.0.1:19996; location / { return 200 "original-site-ok\\n"; } }\n')
Path('/etc/nginx/sites-enabled/hl-nginx-compat.conf').symlink_to(site)
run(['nginx', '-t']).check_returncode()
run(['systemctl', 'restart', 'nginx']).check_returncode()
original_site_works()
before = nginx_state()

source = installer.read_text()
functions = re.search(r'^check_nginx_capabilities\(\) \{.*?(?=^resolve_domain_records)',
                      source, re.MULTILINE | re.DOTALL)
restore = re.search(r'^restore_unit_state\(\) \{.*?(?=^nginx_server_name_matches)',
                    source, re.MULTILINE | re.DOTALL)
assert functions and restore, 'Installer Nginx functions not found'
script = ('log() { printf "%s\\n" "$*"; }; '
          'fail() { printf "%s\\n" "$*" >&2; exit 1; };\n'
          + restore.group() + functions.group())
failed = run(['bash', '-euc', script + '\nif check_nginx_capabilities; then exit 0; '
              'else printf "%s\\n" "$NGINX_CHECK_OUTPUT" >&2; exit 1; fi\n'])
assert failed.returncode != 0
assert 'unknown directive "limit_req_zone"' in failed.stderr, failed.stderr
assert nginx_state() == before, 'Existing Nginx configuration or service state changed'
assert not list(Path('/tmp').glob('hl-panel-nginx-check.*')), 'Compatibility check leaked temporary files'
original_site_works()

adapted = run(['bash', '-euc', script + '\nensure_nginx_compatibility\n'], env=environment)
adapted.check_returncode()
assert 'Nginx 自动适配完成' in adapted.stdout, adapted.stdout
assert nginx_state() == before, 'Automatic adaptation changed existing sites'
original_site_works()
backup = Path(re.search(r'Nginx 适配备份：(.+)', adapted.stdout).group(1).strip())
assert (backup / 'nginx-config.tar').is_file()
assert (backup.stat().st_mode & 0o777) == 0o700

# Restore the light variant, then inject a failure after the real package switch.
run(['bash', str(backup / 'rollback.sh')], env=environment).check_returncode()
assert nginx_state() == before, 'Rollback changed existing sites'
original_site_works()
injected_script = script.replace('check_nginx_capabilities() {', 'real_check_nginx_capabilities() {', 1)
injected_script += '''
probe_count=0
check_nginx_capabilities() {
  probe_count=$((probe_count + 1))
  if ((probe_count == 2)); then NGINX_CHECK_OUTPUT='injected compatibility failure'; return 1; fi
  real_check_nginx_capabilities
}
ensure_nginx_compatibility
'''
recovered = run(['bash', '-euc', injected_script], env=environment)
assert recovered.returncode != 0 and '已恢复原站点' in recovered.stderr, recovered.stderr
assert nginx_state() == before, 'Failed adaptation did not restore existing sites'
original_site_works()

assert subprocess.run(['id', 'hlpanel'], capture_output=True).returncode != 0
for path in ['/opt/hl-panel', '/etc/hl-panel', '/var/lib/hl-panel', '/var/lib/hl-panel-acme',
             '/etc/nginx/conf.d/hl-panel-rate-limit.conf',
             '/etc/systemd/system/hl-panel-control-api.service']:
    assert not Path(path).exists(), 'Compatibility check created ' + path

run(['nginx', '-t']).check_returncode()
Path('nginx-compatibility-result.json').write_text(json.dumps({
    'ubuntu': '22.04', 'real_nginx_light_failure_reproduced': True,
    'automatic_nginx_core_adaptation': True,
    'existing_nginx_configuration_and_service_preserved': True,
    'original_http_site_accessible_after_adaptation': True,
    'failed_adaptation_automatically_rolled_back': True,
    'root_only_configuration_backup': True,
}, indent=2))
print('NGINX_COMPATIBILITY_ACCEPTED')
