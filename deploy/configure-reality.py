#!/usr/bin/env python3
"""Verify a TLS 1.3 target, then configure only the managed HL API."""
import argparse
import fcntl
import os
from pathlib import Path
import re
import shutil
import subprocess
import tempfile
import time
import urllib.request

CONFIG = Path('/etc/hl-panel/control-api.env')
SERVICE = 'hl-panel-control-api.service'


def require(value, message):
    if not value:
        raise RuntimeError(message)


def replace(data, info):
    fd, name = tempfile.mkstemp(dir=CONFIG.parent)
    try:
        with os.fdopen(fd, 'wb') as stream:
            stream.write(data)
            stream.flush()
            os.fsync(stream.fileno())
        os.chown(name, info.st_uid, info.st_gid)
        os.chmod(name, info.st_mode & 0o777)
        os.replace(name, CONFIG)
    finally:
        if os.path.exists(name):
            os.unlink(name)


def healthy(origin):
    opener = urllib.request.build_opener(urllib.request.ProxyHandler({}))
    for _ in range(30):
        try:
            with opener.open(origin + '/healthz', timeout=2) as response:
                if response.status == 200:
                    return
        except OSError:
            pass
        time.sleep(1)
    raise RuntimeError('HL API 未恢复健康')


def main():
    parser = argparse.ArgumentParser(description='配置可验证的 Reality TLS 目标')
    parser.add_argument('domain')
    parser.add_argument('--port', type=int, default=443)
    args = parser.parse_args()
    domain = args.domain.lower().rstrip('.')
    require(os.geteuid() == 0, '请使用 root 运行')
    require(1 <= args.port <= 65535 and len(domain) <= 253 and '.' in domain and all(
        re.fullmatch(r'[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?', label) for label in domain.split('.')), '域名或端口无效')
    require(CONFIG.is_file() and not CONFIG.is_symlink(), '未找到受管面板配置')
    info = CONFIG.stat()
    require(info.st_uid == 0 and not info.st_mode & 0o022, '面板配置权限不安全')
    fd = os.open('/opt/hl-panel/.update.lock', os.O_WRONLY | os.O_CREAT | os.O_NOFOLLOW, 0o600)
    with os.fdopen(fd, 'a') as lock:
        fcntl.flock(lock, fcntl.LOCK_EX | fcntl.LOCK_NB)
        result = subprocess.run(['openssl', 's_client', '-connect', f'{domain}:{args.port}',
            '-servername', domain, '-tls1_3', '-groups', 'X25519', '-verify_hostname', domain,
            '-verify_return_error'], input=b'', capture_output=True, timeout=20)
        require(result.returncode == 0 and b'Verify return code: 0' in result.stdout,
            '目标未通过 TLS 1.3 / X25519 / 域名证书核验；未修改配置')
        old = CONFIG.read_bytes()
        text = old.decode()
        listen = re.search(r'^CONTROL_LISTEN_ADDRESS=(127\.0\.0\.1:[0-9]{1,5})$', text, re.M)
        require(listen, '不支持非受管 API 监听地址')
        Path('/var/backups/hl-panel').mkdir(parents=True, exist_ok=True, mode=0o700)
        backup = Path(tempfile.mkdtemp(prefix='reality-', dir='/var/backups/hl-panel'))
        backup.chmod(0o700)
        shutil.copy2(CONFIG, backup / 'control-api.env')
        values = {'CONTROL_REALITY_SERVER_NAME': domain, 'CONTROL_REALITY_DESTINATION': f'{domain}:{args.port}'}
        lines = [line for line in text.splitlines() if line.split('=', 1)[0] not in values]
        lines.extend(key + '=' + value for key, value in values.items())
        try:
            replace(('\n'.join(lines) + '\n').encode(), info)
            subprocess.run(['systemctl', 'restart', SERVICE], check=True, capture_output=True)
            healthy('http://' + listen[1])
        except Exception:
            replace(old, info)
            subprocess.run(['systemctl', 'restart', SERVICE], check=True, capture_output=True)
            raise RuntimeError('配置未通过健康检查，已恢复原配置') from None
        print('[HL-panel Reality] TLS 目标已配置：' + domain)
        print('配置备份：' + str(backup))


if __name__ == '__main__':
    try:
        main()
    except Exception as error:
        print('[HL-panel Reality] 失败：' + (str(error) if isinstance(error, RuntimeError) else '操作失败，未输出私有配置'))
        raise SystemExit(1)
