#!/usr/bin/env python3
"""Move an existing managed node to a restored panel without re-enrollment."""
import argparse
import fcntl
import hashlib
import json
import os
from pathlib import Path
import shutil
import ssl
import subprocess
import sys
import tempfile
import time
import urllib.parse
import urllib.request

CONFIG = Path('/etc/hl-panel/edge-agent.json')
STATE = Path('/var/lib/hl-panel-edge')
BACKUPS = Path('/var/backups/hl-panel-node')
SERVICE = 'hl-panel-edge-agent.service'

class MoveError(Exception):
    pass

class NoRedirect(urllib.request.HTTPRedirectHandler):
    def redirect_request(self, *args):
        return None

def require(value, message):
    if not value:
        raise MoveError(message)

def origin(value):
    url = urllib.parse.urlsplit(value)
    require(url.scheme == 'https' and url.hostname and not url.username and not url.password and not url.query and not url.fragment and url.path in ('', '/'), '请输入有效的 HTTPS 面板地址，例如 https://panel.example.com')
    require(not any(c.isspace() for c in value), '面板地址不能包含空白字符')
    return value.rstrip('/')

def run(args):
    result = subprocess.run(args, capture_output=True, timeout=30)
    require(result.returncode == 0, '节点服务操作失败，未输出私有配置')

def atomic_config(data, stat):
    fd, name = tempfile.mkstemp(dir=CONFIG.parent, prefix='.panel-address-')
    try:
        with os.fdopen(fd, 'wb') as f:
            f.write(data); f.flush(); os.fsync(f.fileno())
        os.chmod(name, stat.st_mode & 0o777); os.chown(name, stat.st_uid, stat.st_gid)
        os.replace(name, CONFIG)
    finally:
        if Path(name).exists(): Path(name).unlink()

def move(url, ca_file=''):
    require(CONFIG.is_file() and not CONFIG.is_symlink(), '未找到受管节点配置，请先安装节点')
    stat = CONFIG.stat()
    require(stat.st_uid == 0 and not stat.st_mode & 0o022, '节点配置权限不安全')
    old = CONFIG.read_bytes()
    config = json.loads(old)
    require(Path(config.get('data_dir', '')).resolve() == STATE, '不支持非受管节点数据目录')
    credential_file = STATE/'credentials.json'
    require(credential_file.is_file() and not credential_file.is_symlink(), '未找到现有节点身份')
    credential_raw = credential_file.read_bytes()
    credential = json.loads(credential_raw)
    require(credential.get('node_id') and credential.get('node_credential'), '节点身份文件不完整')
    context = ssl.create_default_context(cafile=ca_file or None)
    opener = urllib.request.build_opener(urllib.request.ProxyHandler({}), urllib.request.HTTPSHandler(context=context), NoRedirect())
    # The new panel must have the restored credential before changing the node.
    req = urllib.request.Request(url+'/api/v1/agent/desired', headers={'Authorization': 'Bearer '+credential['node_credential'], 'User-Agent': 'HL-Panel-node-migration'})
    try:
        with opener.open(req, timeout=20) as response:
            require(response.status in (200, 204), '新面板未接受现有节点身份，请先导入备份')
    except Exception:
        raise MoveError('新面板连接、证书或节点身份校验失败；现有节点配置未改变。请先导入备份，核对地址和证书。') from None
    config['control_plane_url'] = url
    config['ca_file'] = ca_file
    require(not BACKUPS.is_symlink(), '节点备份目录不安全')
    BACKUPS.mkdir(mode=0o700, parents=True, exist_ok=True)
    require(not BACKUPS.stat().st_mode & 0o077, '节点备份目录须为私有目录')
    backup = Path(tempfile.mkdtemp(prefix='panel-address-', dir=BACKUPS))
    saved = backup/'edge-agent.json'
    saved.write_bytes(old); saved.chmod(0o600)
    with saved.open('rb') as f: os.fsync(f.fileno())
    try:
        atomic_config((json.dumps(config, ensure_ascii=False, indent=2)+'\n').encode(), stat)
        run(['systemctl', 'restart', SERVICE]); time.sleep(2)
        run(['systemctl', 'is-active', '--quiet', SERVICE])
        require(hashlib.sha256(credential_file.read_bytes()).digest() == hashlib.sha256(credential_raw).digest(), '节点身份发生变化')
    except Exception:
        atomic_config(old, stat)
        try:
            run(['systemctl', 'restart', SERVICE])
        except Exception:
            raise MoveError('连接配置已恢复，但旧节点服务重启失败，请检查 hl-panel-edge-agent.service') from None
        raise MoveError('节点切换失败，已恢复旧面板连接配置') from None
    print('节点面板地址已切换；节点身份、规则配置及流量游标保留。')
    print('连接配置备份：'+str(saved))
    print('请在新面板核对节点在线状态及真实转发。')

def main():
    parser = argparse.ArgumentParser(description='保留节点身份，更换面板地址')
    parser.add_argument('--panel-url', required=True)
    parser.add_argument('--ca-file', default='', help='仅自签证书需要，填写节点本机 CA 文件绝对路径')
    args = parser.parse_args()
    require(os.geteuid() == 0, '请以 root 执行')
    if args.ca_file:
        ca = Path(args.ca_file)
        require(ca.is_absolute() and ca.is_file() and not ca.is_symlink(), 'CA 文件必须是本机普通文件的绝对路径')
    with open('/run/hl-panel-node-update.lock', 'a') as lock:
        fcntl.flock(lock.fileno(), fcntl.LOCK_EX | fcntl.LOCK_NB)
        move(origin(args.panel_url), args.ca_file)

if __name__ == '__main__':
    try: main()
    except MoveError as error:
        print('错误：'+str(error), file=sys.stderr)
        sys.exit(1)
    except (OSError, ValueError):
        print('错误：节点迁移未完成。请核对新面板地址、证书和备份导入结果；现有身份与引擎数据未删除。', file=sys.stderr)
        sys.exit(1)
