#!/usr/bin/env python3
"""Reinstall a managed node using its private identity and a new group token."""
import argparse
import fcntl
import hashlib
import json
import os
from pathlib import Path
import pwd
import re
import shutil
import socket
import ssl
import subprocess
import sys
import tarfile
import tempfile
import time
import urllib.error
import urllib.request

ROOT = Path('/opt/hl-panel')
CONFIG = Path('/etc/hl-panel/edge-agent.json')
STATE = Path('/var/lib/hl-panel-edge')
UNIT = Path('/etc/systemd/system/hl-panel-edge-agent.service')
SERVICE = 'hl-panel-edge-agent.service'
BACKUPS = Path('/var/backups/hl-panel-node')


class ReinstallError(Exception):
    pass


class NoRedirect(urllib.request.HTTPRedirectHandler):
    def redirect_request(self, req, fp, code, msg, headers, newurl):
        # Never send the private node identity or group token to a redirect.
        return None


def require(condition, message):
    if not condition:
        raise ReinstallError(message)


def digest(path):
    return hashlib.sha256(path.read_bytes()).hexdigest()


def run(arguments):
    result = subprocess.run(arguments, capture_output=True)
    require(result.returncode == 0, '节点服务或程序检查失败；未输出私有配置')
    return result.stdout


def private_file(path, owner=0, mask=0o022):
    require(path.is_file() and not path.is_symlink(), '缺少受管文件或文件是链接')
    info = path.stat()
    require(info.st_uid == owner and not info.st_mode & mask, '受管文件权限不安全')


def atomic_install(source, destination, mode, uid=0, gid=0):
    require(not destination.is_symlink(), '安装目标是链接')
    fd, name = tempfile.mkstemp(dir=destination.parent)
    os.close(fd)
    try:
        shutil.copyfile(source, name)
        os.chmod(name, mode)
        os.chown(name, uid, gid)
        os.replace(name, destination)
    finally:
        if os.path.exists(name):
            os.unlink(name)


def start_service():
    run(['systemctl', 'daemon-reload'])
    run(['systemctl', 'reset-failed', SERVICE])
    run(['systemctl', 'enable', SERVICE])
    run(['systemctl', 'start', SERVICE])
    for _ in range(15):
        time.sleep(1)
        if subprocess.run(['systemctl', 'is-active', '--quiet', SERVICE], capture_output=True).returncode == 0:
            return
    raise ReinstallError('节点服务未启动')


def request_reenrollment(origin, context, credential, payload):
    # TLS checks and existing credentials also allow an IP/domain change for
    # the same panel. Neither a shared IP nor a hostname can claim an identity.
    opener = urllib.request.build_opener(urllib.request.ProxyHandler({}), NoRedirect(),
                                         urllib.request.HTTPSHandler(context=context))
    messages = {400: '注册参数不兼容，请更新面板后重新生成命令',
                401: '节点身份或令牌无效，或旧命令已被后续注册替代；请在目标组生成新命令',
                404: '面板不支持重装换组或目标组不存在，请先更新面板',
                409: '令牌已撤销或新组规则配置有冲突；当前节点身份保留',
                410: '令牌已过期，请在目标组生成新命令'}
    for attempt in range(4):
        req = urllib.request.Request(origin + '/api/v1/agent/reenroll',
            data=json.dumps(payload).encode(),
            headers={'Content-Type': 'application/json', 'Authorization': 'Bearer ' + credential['node_credential']},
            method='POST')
        try:
            with opener.open(req, timeout=15) as response:
                result = json.loads(response.read(8192))
                require(response.status == 200 and result.get('node_id') == credential['node_id'], '面板返回的节点身份不符')
                return
        except urllib.error.HTTPError as error:
            if error.code < 500 and error.code != 429:
                raise ReinstallError(messages.get(error.code, '面板拒绝注册，当前安装保留')) from None
        except (urllib.error.URLError, TimeoutError, ConnectionError, OSError, ValueError):
            pass
        if attempt < 3:
            print('[HL-panel 节点] 等待面板确认，使用同一节点身份和令牌自动重试…', flush=True)
            time.sleep(2 ** attempt)
    raise ReinstallError('未收到面板确认，当前节点服务和身份保留；重新运行本次命令可安全恢复，勿改用旧组命令')


def restore_programs(backup):
    backup = backup.resolve()
    require(backup.parent == BACKUPS and backup.name.startswith('reinstall-'), '不是受管重装备份目录')
    require(not backup.stat().st_mode & 0o077 and backup.stat().st_uid == 0, '备份目录权限不安全')
    private_file(backup / 'manifest.json', mask=0o077)
    manifest = json.loads((backup / 'manifest.json').read_text())
    require(digest(STATE / 'credentials.json') == manifest['credential_sha256'], '节点身份不同，拒绝恢复')
    allowed = {str(ROOT / p) for p in ('edge-agent', 'node-engines/xray', 'node-engines/gost', 'enroll-node.sh', 'reinstall-node.py')}
    allowed.add(str(UNIT))
    for item in manifest['files']:
        require(item['destination'] in allowed and re.fullmatch(r'file-[0-9]+', item['source']), '备份文件路径无效')
        require(digest(backup / item['source']) == item['sha256'], '备份文件摘要不符')
    run(['systemctl', 'stop', SERVICE])
    for item in manifest['files']:
        atomic_install(backup / item['source'], Path(item['destination']), item['mode'])
    # Membership is the latest confirmed registration; never restore old usage
    # counters or credentials while rolling the executables back.
    start_service()


def reinstall(args):
    account = pwd.getpwnam('hl-edge')
    for directory in (ROOT, ROOT / 'node-engines', CONFIG.parent, STATE, UNIT.parent, BACKUPS):
        require(not directory.is_symlink(), '受管目录不能是链接')
    require(STATE.is_dir() and STATE.stat().st_uid == account.pw_uid and not STATE.stat().st_mode & 0o077, '节点状态目录不安全')
    private_file(CONFIG)
    private_file(STATE / 'credentials.json', account.pw_uid, 0o077)
    private_file(UNIT)
    config = json.loads(CONFIG.read_text())
    credential = json.loads((STATE / 'credentials.json').read_text())
    require(credential.get('node_id') and credential.get('node_credential'), '现有节点凭据无效')
    require(config.get('data_dir') == str(STATE) and config.get('engine_mode') == 'mixed', '不是受管 mixed 节点安装')
    require(config.get('xray_binary_path') == str(ROOT / 'node-engines/xray') and config.get('gost_binary_path') == str(ROOT / 'node-engines/gost'), '不是受管引擎路径')
    require(re.fullmatch(r'v[0-9]+\.[0-9]+\.[0-9]+', args.version), '版本无效')
    token = os.environ.pop('HL_INSTALL_ENROLLMENT_TOKEN', '')
    require(re.fullmatch(r'[A-Za-z0-9_-]{8,256}', token), '缺少有效注册令牌')
    context = ssl.create_default_context(cafile=args.ca_file or config.get('ca_file') or None)
    sources = [(ROOT / 'edge-agent', 'bin/edge-agent', 0o755),
               (ROOT / 'node-engines/xray', 'bin/xray', 0o755),
               (ROOT / 'node-engines/gost', 'bin/gost', 0o755),
               (ROOT / 'enroll-node.sh', 'deploy/edge-agent/enroll.sh', 0o700),
               (ROOT / 'reinstall-node.py', 'deploy/reinstall-node.py', 0o700),
               (UNIT, 'deploy/systemd/hl-panel-edge-agent.service', 0o644)]
    for destination, source, _ in sources:
        require((args.release / source).is_file(), '安装包缺少重装文件')
        if destination.exists():
            private_file(destination)
    run([str(args.release / 'bin/xray'), 'version'])
    run([str(args.release / 'bin/gost'), '-V'])
    run(['systemd-analyze', 'verify', str(args.release / 'deploy/systemd/hl-panel-edge-agent.service')])
    BACKUPS.mkdir(parents=True, exist_ok=True, mode=0o700)
    require(BACKUPS.stat().st_uid == 0 and not BACKUPS.stat().st_mode & 0o077, '备份目录权限不安全')
    backup = BACKUPS / ('reinstall-' + args.version + '-' + time.strftime('%Y%m%dT%H%M%SZ', time.gmtime()) + '-' + os.urandom(3).hex())
    backup.mkdir(mode=0o700)
    files = []
    for destination, _, _ in sources:
        if destination.exists():
            name = 'file-' + str(len(files))
            shutil.copy2(destination, backup / name)
            files.append({'source': name, 'destination': str(destination), 'sha256': digest(backup / name), 'mode': destination.stat().st_mode & 0o777})
    manifest = {'credential_sha256': digest(STATE / 'credentials.json'), 'files': files}
    (backup / 'manifest.json').write_text(json.dumps(manifest) + '\n')
    (backup / 'manifest.json').chmod(0o600)
    shutil.copy2(CONFIG, backup / 'edge-agent.json')
    shutil.copy2(args.release / 'deploy/reinstall-node.py', backup / 'reinstall-node.py')
    (backup / 'rollback.sh').write_text('#!/usr/bin/env bash\nset -Eeuo pipefail\nexec python3 ' + str(backup / 'reinstall-node.py') + ' --rollback ' + str(backup) + '\n')
    (backup / 'rollback.sh').chmod(0o700)
    print('[HL-panel 节点] 已备份现有程序，正在确认本次设备组注册…', flush=True)
    request_reenrollment(args.panel_url, context, credential, {'token': token, 'hostname': socket.gethostname(),
        'dial_host': args.node_address, 'platform': 'linux', 'architecture': 'amd64', 'agent_version': args.version, 'capabilities': []})
    token = ''
    config['control_plane_url'], config['dial_host'] = args.panel_url, args.node_address
    print('[HL-panel 节点] 注册已确认，保留节点身份，更新 Agent、探针及双引擎…', flush=True)
    try:
        if args.ca_file:
            atomic_install(Path(args.ca_file), CONFIG.parent / 'edge-agent-ca.pem', 0o640, gid=account.pw_gid)
            config['ca_file'] = str(CONFIG.parent / 'edge-agent-ca.pem')
        candidate = backup / 'new-config.json'
        candidate.write_text(json.dumps(config) + '\n')
        candidate.chmod(0o600)
        run(['systemctl', 'stop', SERVICE])
        with tarfile.open(backup / 'config-state.tar.gz', 'w:gz') as archive:
            archive.add(CONFIG, arcname='edge-agent.json')
            archive.add(STATE, arcname='state')
        for destination, source, mode in sources:
            atomic_install(args.release / source, destination, mode)
        atomic_install(candidate, CONFIG, 0o640, gid=account.pw_gid)
        start_service()
        require(digest(STATE / 'credentials.json') == manifest['credential_sha256'], '重装意外改变节点身份')
    except Exception:
        try:
            restore_programs(backup)
        except Exception:
            raise ReinstallError('设备组注册已确认，但程序更新及自动恢复失败；备份：' + str(backup) + '；运行 bash ' + str(backup / 'rollback.sh')) from None
        raise ReinstallError('新程序启动失败，已恢复旧程序；设备组仍采用本次已确认的注册，可重跑本次命令') from None
    print('备份：' + str(backup))
    print('程序回滚（不回退设备组或流量）：bash ' + str(backup / 'rollback.sh'))


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument('--release', type=Path)
    parser.add_argument('--panel-url')
    parser.add_argument('--node-address')
    parser.add_argument('--version')
    parser.add_argument('--ca-file', default='')
    parser.add_argument('--rollback', type=Path)
    args = parser.parse_args()
    require(os.geteuid() == 0, '请使用 root 执行')
    fd = os.open(ROOT / '.node-update.lock', os.O_WRONLY | os.O_CREAT | os.O_NOFOLLOW, 0o600)
    with os.fdopen(fd, 'a') as lock:
        try:
            fcntl.flock(lock, fcntl.LOCK_EX | fcntl.LOCK_NB)
        except BlockingIOError:
            raise ReinstallError('另一个节点更新或重装正在执行，请完成后再运行') from None
        if args.rollback:
            restore_programs(args.rollback)
        else:
            require(all((args.release, args.panel_url, args.node_address, args.version)), '缺少重装参数')
            reinstall(args)


if __name__ == '__main__':
    try:
        main()
    except Exception as error:
        print('[HL-panel 节点] 重装失败：' + (str(error) if isinstance(error, ReinstallError) else '操作失败，私有数据未输出；查看节点服务状态后重跑本次命令'), file=sys.stderr)
        sys.exit(1)
