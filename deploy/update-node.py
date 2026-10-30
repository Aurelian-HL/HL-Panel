#!/usr/bin/env python3
"""Update bundled node binaries without re-enrolling or replacing state."""
import argparse
import fcntl
import json
import os
import pwd
import re
from pathlib import Path
import shutil
import subprocess
import sys
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


RESULT_FILE = None
LEGACY_STAGING = Path('/var/backups/hl-panel-node')
UPDATE_STATE = Path('/var/lib/hl-panel-node-update')


def transaction_path(directory):
    # The old wire field is retained for workers already running in memory.
    # These are pending replacement files, never a retained release archive.
    require(not directory.is_symlink() and directory.is_dir(), '更新临时目录无效')
    parent = directory.parent
    require(not parent.is_symlink() and parent.stat().st_uid == 0 and
            not parent.stat().st_mode & 0o022, '更新临时目录父路径不安全')
    require(((parent == ROOT and re.fullmatch(r'\.node-update-v[0-9]+\.[0-9]+\.[0-9]+-[A-Za-z0-9_-]+', directory.name)) or
             (parent == LEGACY_STAGING and re.fullmatch(r'update-v[0-9]+\.[0-9]+\.[0-9]+-[A-Za-z0-9_-]+', directory.name))),
            '不是受管更新临时目录')
    require(directory.stat().st_uid == 0 and not directory.stat().st_mode & 0o077,
            '更新临时目录权限不安全')
    return directory


def cleanup_transaction(directory):
    transaction_path(directory)
    shutil.rmtree(directory)
    if directory.parent == LEGACY_STAGING:
        try:
            LEGACY_STAGING.rmdir()  # Only our now-empty compatibility directory.
        except OSError:
            pass


def cleanup_finished(directory):
    try:
        cleanup_transaction(directory)
        return True
    except (OSError, UpdateError):
        # Cleanup cannot change a proven update or recovery result.
        print('[HL-panel 节点更新] 临时文件清理未完成：' + str(directory), file=sys.stderr)
        return False


def legacy_cli_completed(directory):
    """Prove a pre-upgrade CLI finished its switch or restored all old programs."""
    private_credentials()
    private_regular(directory/'manifest.json')
    manifest = json.loads((directory/'manifest.json').read_text())
    if digest(STATE/'credentials.json') != manifest['credential_sha256']:
        return False
    private_regular(CONFIG)
    installed = {name: digest(ROOT/name) for name in FILES}
    for candidate, expected in (
            ('new-edge-agent.json', {name: digest(directory/'release'/source) for name, source in FILES.items()}),
            ('edge-agent.json', manifest['files'])):
        if installed == expected and CONFIG.read_bytes() == (directory/candidate).read_bytes():
            return subprocess.run(['systemctl', 'is-active', '--quiet', SERVICE],
                                  capture_output=True, timeout=10).returncode == 0
    return False


def cleanup_legacy_task(task_id, directory):
    """Old interpreters keep their recovery files until their operation finishes."""
    require(not task_id or re.fullmatch(r'[a-f0-9]{8}-(?:[a-f0-9]{4}-){3}[a-f0-9]{12}', task_id), '任务标识无效')
    for _ in range(2400):
        if not directory.exists():
            return
        transaction_path(directory)
        # The updater/rollback holds this lock while reading release assets.
        # Even an early /verified response cannot remove those files mid-use.
        fd = os.open(ROOT/'.node-update.lock', os.O_WRONLY|os.O_CREAT|os.O_NOFOLLOW, 0o600)
        with os.fdopen(fd, 'a') as lock:
            try:
                fcntl.flock(lock, fcntl.LOCK_EX|fcntl.LOCK_NB)
            except BlockingIOError:
                pass
            else:
                if task_id:
                    path = UPDATE_STATE/'task.json'
                    private_regular(path)
                    task = json.loads(path.read_text())
                    if task.get('id') != task_id:
                        return  # Do not infer a result from a later task.
                    if task.get('state') in ('succeeded', 'rolled_back') and task.get('backup_directory') == str(directory):
                        cleanup_transaction(directory)
                        return
                    if task.get('state') == 'failed':
                        return
                elif legacy_cli_completed(directory):
                    cleanup_transaction(directory)
                    return
        time.sleep(1)


def create_transaction(target):
    legacy = RESULT_FILE is not None and os.environ.get('HL_NODE_TRANSIENT_UPDATE') != '1'
    parent = LEGACY_STAGING if legacy else ROOT
    if legacy:
        require(not parent.is_symlink(), '更新临时目录不能是链接')
        parent.mkdir(mode=0o700, exist_ok=True)
        require(parent.stat().st_uid == 0 and not parent.stat().st_mode & 0o022, '更新临时目录权限不安全')
    prefix = ('update-' if legacy else '.node-update-') + target + '-'
    if legacy:
        # Old workers validate this path with an alphanumeric-only regex.
        directory = parent / (prefix + os.urandom(12).hex())
        directory.mkdir(mode=0o700)
        return directory, True
    return Path(tempfile.mkdtemp(prefix=prefix, dir=parent)), legacy

def result(state, backup=None):
    if RESULT_FILE:
        data = {'state':state,'backup_directory':str(backup) if backup else ''}
        fd, name = tempfile.mkstemp(dir=RESULT_FILE.parent)
        with os.fdopen(fd,'w') as out:
            json.dump(data,out);out.flush();os.fsync(out.fileno())
        os.replace(name,RESULT_FILE)


def verify_xray_version(binary):
    # Reject an untested handshake change before stopping the current service.
    require(run([str(binary), 'version']).startswith(b'Xray 26.7.28 '),
            'Xray 版本未通过旧客户端兼容验收；要求 26.7.28，当前节点未停止')


def configure_tcp_bbr(release):
    # Run only the checksum-verified release helper, after a successful update.
    # It restores failed sysctl writes in memory and never restarts an engine. A kernel
    # without BBR must not undo an otherwise healthy node-program upgrade.
    helper = release/'deploy/configure-node-bbr2.py'
    fallback = release/'deploy/configure-node-bbr.py'
    if not helper.is_file() or helper.is_symlink() or not fallback.is_file() or fallback.is_symlink():
        print('[HL-panel 节点更新] 警告：发布包缺少安全的 TCP BBR 配置器，未修改网络参数。', file=sys.stderr)
        return False
    try:
        result = subprocess.run([sys.executable, str(helper)], capture_output=True, timeout=120)
    except (OSError, subprocess.TimeoutExpired):
        print('[HL-panel 节点更新] 警告：TCP BBR 设置未完成；节点更新保留，请检查实际算法。', file=sys.stderr)
        return False
    for output, stream in ((result.stdout, sys.stdout), (result.stderr, sys.stderr)):
        if output:
            print(output.decode('utf-8', errors='replace').rstrip(), file=stream)
    if result.returncode != 0:
        print('[HL-panel 节点更新] 警告：TCP BBR 设置未完成；节点更新保留，请核对上方原因及当前 sysctl 配置。', file=sys.stderr)
    return result.returncode == 0


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


def install_config(source):
    info = CONFIG.stat()
    fd, name = tempfile.mkstemp(dir=CONFIG.parent)
    os.close(fd)
    try:
        shutil.copyfile(source, name)
        os.chown(name, info.st_uid, info.st_gid)
        os.chmod(name, info.st_mode & 0o777)
        os.replace(name, CONFIG)
    finally:
        if os.path.exists(name):
            os.unlink(name)


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
    (backup/'host-metrics-added').chmod(0o600)
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
    transaction_path(backup)
    private_regular(backup/'manifest.json')
    manifest=json.loads((backup/'manifest.json').read_text())
    require(manifest['credential_sha256'] == digest(STATE/'credentials.json'), '当前节点身份不同；拒绝回滚')
    for name in FILES:
        require(digest(backup/name) == manifest['files'][name], '备份文件摘要不符')
    if (backup/'maintenance-rollback.py').is_file():
        private_regular(backup/'maintenance-rollback.py')
        run([sys.executable,str(backup/'maintenance-rollback.py'),'--restore',str(backup/'maintenance')])
    restore_metrics(backup)
    run(['systemctl','stop',SERVICE])
    if (backup/'edge-agent.json').exists():
        private_regular(backup/'edge-agent.json')
        install_config(backup/'edge-agent.json')
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
    parser.add_argument('--rollback',type=Path, help=argparse.SUPPRESS)
    parser.add_argument('--cleanup-task', nargs='?', const='', help=argparse.SUPPRESS)
    parser.add_argument('--transaction', type=Path, help=argparse.SUPPRESS)
    parser.add_argument('--expected-sha256', default='')
    parser.add_argument('--result-file', type=Path)
    parser.add_argument('--repair', action='store_true', help='repair dependencies only, without replacing or restarting engines')
    args=parser.parse_args()
    require(os.geteuid()==0 and sys.platform=='linux', '请在 Linux 节点机使用 root 运行')
    if args.cleanup_task is not None:
        require(args.transaction is not None, '缺少临时目录')
        cleanup_legacy_task(args.cleanup_task, args.transaction)
        return
    global RESULT_FILE
    RESULT_FILE = args.result_file
    if RESULT_FILE:
        require(RESULT_FILE.parent.parent == UPDATE_STATE and not RESULT_FILE.parent.is_symlink() and RESULT_FILE.parent.stat().st_uid == 0 and not RESULT_FILE.parent.stat().st_mode & 0o077, '更新结果路径不安全')
    require(not args.expected_sha256 or re.fullmatch(r'[a-f0-9]{64}', args.expected_sha256), '预期摘要无效')
    result('failed')
    require(os.geteuid()==0 and sys.platform=='linux', '请在 Linux 节点机使用 root 运行')
    private_regular(CONFIG)
    private_credentials()
    require(ROOT.is_dir() and not ROOT.is_symlink() and ROOT.stat().st_uid==0 and not ROOT.stat().st_mode&0o022,'程序目录不安全')
    fd=os.open(ROOT/'.node-update.lock',os.O_WRONLY|os.O_CREAT|os.O_NOFOLLOW,0o600)
    with os.fdopen(fd,'a') as lock:
        fcntl.flock(lock,fcntl.LOCK_EX|fcntl.LOCK_NB)
        if args.rollback:
            rollback(args.rollback)
            cleanup_finished(args.rollback)
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
        backup, legacy = create_transaction(target)
        cleaned = False
        try:
            update_programs(args, config, target, backup, legacy)
        finally:
            if not (backup/'.switching').exists() and not (backup/'.verifying').exists():
                cleaned = cleanup_finished(backup)
        if not RESULT_FILE and cleaned:
            print('[HL-panel 节点更新] 临时文件已清理。')


def update_programs(args, config, target, backup, legacy):
    archive=backup/'release.tar.gz'
    print('[HL-panel 节点更新] 下载并验证 '+target+'，当前转发继续运行。',flush=True)
    base=REPO+'/releases/download/'+target+'/'
    archive.write_bytes(request(base+'hl-panel-linux-amd64.tar.gz',256*1024*1024))
    checksum=request(base+'hl-panel-linux-amd64.tar.gz.sha256',4096).decode().strip().split()
    require(not args.expected_sha256 or digest(archive)==args.expected_sha256, '发布包与面板任务摘要不符')
    require(len(checksum)==2 and checksum[1]=='hl-panel-linux-amd64.tar.gz' and digest(archive)==checksum[0], '发布包摘要校验失败')
    release=backup/'release'
    release.mkdir()
    extract_verified(archive,release)
    require((release/'deploy/update-node.py').is_file(),'此版本未提供节点升级器')
    for asset in ('deploy/install-node-update.py','deploy/node-update-worker.py',
                  'deploy/systemd/hl-panel-node-update.socket','deploy/systemd/hl-panel-node-update.service'):
        require((release/asset).is_file(), '发布包缺少节点更新服务资产；当前节点未停止')
    verify_xray_version(release/'bin/xray')
    run([str(release/'bin/gost'),'-V'])
    maintenance = release/'deploy/install-node-update.py'
    # Download/package failures, missing packages and invalid resources must
    # be resolved while the current forwarding processes are still running.
    try:
        run([sys.executable, str(maintenance), str(release), '--preflight', '--install-missing'])
    except UpdateError:
        raise UpdateError('节点依赖或 Xray 资源预检失败；请检查软件源、iproute2/systemd/procps/kmod 和 GeoIP/GeoSite 资源；当前转发服务未停止') from None
    run(['systemd-analyze', 'verify',
         str(release/'deploy/systemd/hl-panel-node-update.socket'),
         str(release/'deploy/systemd/hl-panel-node-update.service')])
    if args.repair:
        repair_dependencies(release, backup)
        return
    credential=digest(STATE/'credentials.json')
    shutil.copy2(CONFIG, backup/'edge-agent.json')
    config['heartbeat_interval'] = '1s'
    config.pop('agent_version', None)
    (backup/'new-edge-agent.json').write_text(json.dumps(config)+'\n')
    (backup/'new-edge-agent.json').chmod(0o600)
    manifest={'credential_sha256':credential,'files':{}}
    for name in FILES:
        private_regular(ROOT/name)
        dest=backup/name
        dest.parent.mkdir(parents=True,exist_ok=True)
        shutil.copy2(ROOT/name,dest)
        manifest['files'][name]=digest(dest)
    maintenance = release/'deploy/install-node-update.py'
    require(maintenance.is_file(), '发布包缺少节点更新服务安装器')
    shutil.copy2(maintenance, backup/'maintenance-rollback.py')
    run([sys.executable,str(maintenance),'--snapshot',str(backup/'maintenance')])
    # Prepare the fixed recovery runner completely before stopping anything.
    # Runtime identity, usage journals and LKG stay in their original locations.
    for name in ('update-node.py','update.py'):
        source=ROOT/name if (ROOT/name).is_file() else release/'deploy'/name
        shutil.copy2(source,backup/name)
    shutil.copy2(Path(__file__), backup/'rollback-runner.py')
    (backup/'manifest.json').write_text(json.dumps(manifest)+'\n')
    (backup/'manifest.json').chmod(0o600)
    if legacy:
        # If the compatibility finalizer cannot start, fail before the service
        # switch. An already-running old worker cannot clean completed files.
        subprocess.Popen([sys.executable, str(backup/'rollback-runner.py'),
            '--cleanup-task', RESULT_FILE.parent.name, '--transaction', str(backup)],
            stdin=subprocess.DEVNULL, stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL,
            close_fds=True, start_new_session=True)
    (backup/'.switching').touch(mode=0o600)
    result('uncertain', backup)
    try:
        run(['systemctl','stop',SERVICE])
        metrics_update(backup)
        for name,source in FILES.items():
            install(release/source,ROOT/name)
        for name in ('update-node.py','update.py'):
            install(release/'deploy'/name,ROOT/name)
        run([sys.executable,str(release/'deploy/install-node-update.py'),str(release)])
        install_config(backup/'new-edge-agent.json')
        start_service()
        active()
        require(digest(STATE/'credentials.json')==credential,'节点身份发生变化')
    except Exception:
        rollback(backup)
        result('rolled_back', backup)
        (backup/'.switching').unlink()
        raise
    (backup/'.switching').unlink()
    if RESULT_FILE:
        (backup/'.verifying').touch(mode=0o600)
        result('verifying',backup)
    configure_tcp_bbr(release)
    print('[HL-panel 节点更新] 程序已更新；请回面板核对在线、版本和真实转发。')


def repair_dependencies(release, backup):
    maintenance = release/'deploy/install-node-update.py'
    runner = backup/'maintenance-rollback.py'
    shutil.copy2(maintenance, runner)
    run([sys.executable, str(runner), '--snapshot', str(backup/'maintenance')])
    try:
        run([sys.executable, str(maintenance), str(release), '--repair-assets'])
        run([sys.executable, '-I', str(release/'deploy/configure-node-traffic-control.py'),
             '--service-defaults', '--install-missing'])
    except Exception:
        try:
            run([sys.executable, str(runner), '--restore', str(backup/'maintenance')])
        except Exception:
            (backup/'.switching').touch(mode=0o600)
            raise UpdateError('依赖修复失败，受管文件恢复未完成；未请求重启转发。请检查服务状态；未完成事务：' + str(backup)) from None
        raise UpdateError('依赖修复失败，已恢复受管文件；未请求重启转发。') from None
    result('succeeded')
    print('[HL-panel 节点] 缺失依赖已补齐，已有资源保留；未替换或重启 Agent、Xray、GOST。')
    print('如新补了 TC 服务权限，它在下次主动更新/启动时生效；本次不会为了权限重启转发。')


if __name__=='__main__':
    try:
        main()
    except Exception as error:
        print('[HL-panel 节点更新] 失败：'+(str(error) if isinstance(error,UpdateError) else '操作失败，未输出私有数据'),file=sys.stderr)
        sys.exit(1)
