#!/usr/bin/env python3
"""Update bundled node binaries without re-enrolling or replacing state."""
import argparse
import fcntl
import json
import os
import pwd
from pathlib import Path
import shutil
import subprocess
import sys
import tarfile
import tempfile
import time

from update import API, REPO, UpdateError, digest, extract_verified, private_regular, request, require, version_tuple

ROOT = Path('/opt/hl-panel')
CONFIG = Path('/etc/hl-panel/edge-agent.json')
STATE = Path('/var/lib/hl-panel-edge')
SERVICE = 'hl-panel-edge-agent.service'
METRICS_OVERRIDE = Path('/etc/systemd/system/hl-panel-edge-agent.service.d/20-host-metrics.conf')
METRICS_CONTENT = '# Managed by HL-panel: allow interface metrics.\n[Service]\nRestrictAddressFamilies=AF_UNIX AF_INET AF_INET6 AF_NETLINK\n'
FILES = {'edge-agent':'bin/edge-agent', 'node-engines/xray':'bin/xray', 'node-engines/gost':'bin/gost'}


def private_credentials():
    # systemd runs the agent as hl-edge; its atomically written state belongs
    # to that account, while executable files and configuration remain root-owned.
    account = pwd.getpwnam('hl-edge')
    require(STATE.is_dir() and not STATE.is_symlink(), '节点状态目录不安全')
    info = STATE.stat()
    require(info.st_uid == account.pw_uid and not info.st_mode & 0o077, '节点状态目录必须归专用服务账号所有且保持私有')
    path = STATE/'credentials.json'
    require(path.is_file() and not path.is_symlink(), '缺少受管节点身份')
    info = path.stat()
    require(info.st_uid == account.pw_uid and not info.st_mode & 0o077, '节点身份必须归专用服务账号所有且保持私有')


def run(args):
    result = subprocess.run(args, capture_output=True)
    require(result.returncode == 0, '节点服务操作失败；未输出私有配置')
    return result.stdout


def install(source, destination):
    fd, name = tempfile.mkstemp(dir=destination.parent)
    os.close(fd)
    try:
        shutil.copyfile(source, name)
        os.chmod(name, 0o755)
        os.replace(name, destination)
    finally:
        if os.path.exists(name):
            os.unlink(name)


def active():
    for _ in range(15):
        time.sleep(1)
        if subprocess.run(['systemctl','is-active','--quiet',SERVICE]).returncode == 0:
            run(['systemctl','is-active','--quiet',SERVICE])
            return
    raise UpdateError('节点新版本未启动')


def start_service():
    # Enrollment retries and a failed candidate can exhaust systemd's burst.
    # Clear that history only for this explicit, controlled start; automatic
    # crash restarts still retain the unit's normal rate limit.
    run(['systemctl', 'reset-failed', SERVICE])
    run(['systemctl', 'start', SERVICE])


def metrics_update(backup):
    require(not METRICS_OVERRIDE.is_symlink(), '节点指标服务配置不能为链接')
    if METRICS_OVERRIDE.exists():
        private_regular(METRICS_OVERRIDE)
        require(METRICS_OVERRIDE.read_text() == METRICS_CONTENT, '节点指标配置路径被自定义文件占用，未覆盖')
        return
    METRICS_OVERRIDE.parent.mkdir(mode=0o755, exist_ok=True)
    info = METRICS_OVERRIDE.parent.stat()
    require(not METRICS_OVERRIDE.parent.is_symlink() and info.st_uid == os.geteuid() and not info.st_mode & 0o022,
            '节点服务配置目录必须归更新用户所有且不能被其他用户修改')
    (backup/'host-metrics-added').write_text('20-host-metrics.conf\n')
    METRICS_OVERRIDE.write_text(METRICS_CONTENT)
    METRICS_OVERRIDE.chmod(0o644)
    run(['systemctl', 'daemon-reload'])


def restore_metrics(backup):
    marker = backup/'host-metrics-added'
    if not marker.exists():
        return
    private_regular(marker)
    require(marker.read_text() == '20-host-metrics.conf\n', '节点指标备份标记无效')
    require(not METRICS_OVERRIDE.is_symlink(), '节点指标服务配置不能为链接')
    if METRICS_OVERRIDE.exists():
        private_regular(METRICS_OVERRIDE)
        require(METRICS_OVERRIDE.read_text() == METRICS_CONTENT, '节点指标配置已被修改，拒绝覆盖')
        METRICS_OVERRIDE.unlink()
    run(['systemctl', 'daemon-reload'])


def rollback(backup):
    private_credentials()
    backup = backup.resolve()
    require(backup.parent == Path('/var/backups/hl-panel-node') and backup.stat().st_uid == 0 and not backup.stat().st_mode & 0o077,
        '不是 root 私有的节点备份目录')
    private_regular(backup/'manifest.json')
    manifest=json.loads((backup/'manifest.json').read_text())
    require(manifest['credential_sha256'] == digest(STATE/'credentials.json'), '当前节点身份不同；拒绝回滚')
    for name in FILES:
        require(digest(backup/name) == manifest['files'][name], '备份文件摘要不符')
    restore_metrics(backup)
    run(['systemctl','stop',SERVICE])
    for name in FILES:
        install(backup/name,ROOT/name)
    install(backup/'update-node.py',ROOT/'update-node.py')
    install(backup/'update.py',ROOT/'update.py')
    start_service()
    active()
    print('[HL-panel 节点更新] 已恢复旧程序，身份、状态和流量账本保留。')


def main():
    parser=argparse.ArgumentParser(description='保留节点身份的正式版本升级')
    parser.add_argument('--version',default='latest')
    parser.add_argument('--rollback',type=Path)
    args=parser.parse_args()
    require(os.geteuid()==0 and sys.platform=='linux', '请在 Linux 节点机使用 root 运行')
    private_regular(CONFIG)
    private_credentials()
    require(ROOT.is_dir() and not ROOT.is_symlink() and ROOT.stat().st_uid==0 and not ROOT.stat().st_mode&0o022,'程序目录不安全')
    fd=os.open(ROOT/'.node-update.lock',os.O_WRONLY|os.O_CREAT|os.O_NOFOLLOW,0o600)
    with os.fdopen(fd,'a') as lock:
        fcntl.flock(lock,fcntl.LOCK_EX|fcntl.LOCK_NB)
        if args.rollback:
            rollback(args.rollback)
            return
        config=json.loads(CONFIG.read_text())
        require(config.get('engine_mode')=='mixed' and config.get('data_dir')==str(STATE), '不支持非受管节点配置')
        require(config.get('xray_binary_path')==str(ROOT/'node-engines/xray') and config.get('gost_binary_path')==str(ROOT/'node-engines/gost'), '引擎路径不是受管安装')
        target=args.version
        if target=='latest':
            target=json.loads(request(API+'/releases/latest'))['tag_name']
        version_tuple(target)
        metadata=json.loads(request(API+'/releases/tags/'+target))
        require(metadata.get('tag_name')==target and not metadata.get('draft') and not metadata.get('prerelease') and metadata.get('published_at'), '目标不是正式发布')
        stamp=time.strftime('%Y%m%dT%H%M%SZ',time.gmtime())+'-'+os.urandom(3).hex()
        backup=Path('/var/backups/hl-panel-node')/('update-'+target+'-'+stamp)
        backup.mkdir(parents=True,mode=0o700)
        archive=backup/'release.tar.gz'
        print('[HL-panel 节点更新] 下载并验证 '+target+'，当前转发继续运行。',flush=True)
        base=REPO+'/releases/download/'+target+'/'
        archive.write_bytes(request(base+'hl-panel-linux-amd64.tar.gz',256*1024*1024))
        checksum=request(base+'hl-panel-linux-amd64.tar.gz.sha256',4096).decode().strip().split()
        require(len(checksum)==2 and checksum[1]=='hl-panel-linux-amd64.tar.gz' and digest(archive)==checksum[0], '发布包摘要校验失败')
        release=backup/'release'
        release.mkdir()
        extract_verified(archive,release)
        require((release/'deploy/update-node.py').is_file(),'此版本未提供节点升级器')
        run([str(release/'bin/xray'),'version'])
        run([str(release/'bin/gost'),'-V'])
        credential=digest(STATE/'credentials.json')
        manifest={'credential_sha256':credential,'files':{}}
        for name in FILES:
            private_regular(ROOT/name)
            dest=backup/name
            dest.parent.mkdir(parents=True,exist_ok=True)
            shutil.copy2(ROOT/name,dest)
            manifest['files'][name]=digest(dest)
        # Stop only HL agent, then capture a coherent usage journal and LKG.
        run(['systemctl','stop',SERVICE])
        try:
            with tarfile.open(backup/'config-state.tar.gz','w:gz') as saved:
                saved.add(CONFIG,arcname='edge-agent.json')
                saved.add(STATE,arcname='state')
            for name in ('update-node.py','update.py'):
                source=ROOT/name if (ROOT/name).is_file() else release/'deploy'/name
                shutil.copy2(source,backup/name)
            # The old updater cannot restore service changes introduced here.
            shutil.copy2(Path(__file__), backup/'rollback-runner.py')
            (backup/'manifest.json').write_text(json.dumps(manifest)+'\n')
            (backup/'rollback.sh').write_text('#!/usr/bin/env bash\nset -Eeuo pipefail\nexec python3 '+str(backup/'rollback-runner.py')+' --rollback '+str(backup)+'\n')
            (backup/'rollback.sh').chmod(0o700)
            metrics_update(backup)
            for name,source in FILES.items():
                install(release/source,ROOT/name)
            for name in ('update-node.py','update.py'):
                install(release/'deploy'/name,ROOT/name)
            start_service()
            active()
            require(digest(STATE/'credentials.json')==credential,'节点身份发生变化')
        except Exception:
            if (backup/'manifest.json').is_file():
                rollback(backup)
            else:
                start_service()
            raise
        print('[HL-panel 节点更新] 程序已更新；请回面板核对在线、版本和真实转发。')
        print('备份：'+str(backup))
        print('回滚：bash '+str(backup/'rollback.sh'))


if __name__=='__main__':
    try:
        main()
    except Exception as error:
        print('[HL-panel 节点更新] 失败：'+(str(error) if isinstance(error,UpdateError) else '操作失败，未输出私有数据'),file=sys.stderr)
        sys.exit(1)
