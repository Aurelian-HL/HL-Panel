#!/usr/bin/env python3
"""Replace only fixed HL program remnants that have no enrolled identity."""
import argparse
import os
from pathlib import Path
import stat
import subprocess
import sys

ROOT = Path('/opt/hl-panel')
CONFIG = Path('/etc/hl-panel')
STATE = Path('/var/lib/hl-panel-edge')
UNITS = Path('/etc/systemd/system')
SERVICE = 'hl-panel-edge-agent.service'


def require(condition, message):
    if not condition:
        raise ValueError(message)


def managed_files():
    return [ROOT / 'edge-agent', ROOT / 'enroll-node.sh',
            *(ROOT / 'node-engines' / name for name in ('xray', 'gost', 'geoip.dat', 'geosite.dat')),
            CONFIG / 'edge-agent.json', CONFIG / 'edge-agent-enrollment.env',
            CONFIG / 'edge-agent-ca.pem', UNITS / SERVICE]


def safe_directory(path, state=False):
    # Check ancestors too; an absent leaf under a linked parent is unsafe.
    for parent in (path, *path.parents):
        if not parent.exists() and not parent.is_symlink():
            continue
        info = parent.lstat()
        require(stat.S_ISDIR(info.st_mode) and not parent.is_symlink(), '安装目录是链接或非目录；未清理旧安装')
        # The agent owns only its private state directory, never program parents.
        if state and parent == STATE:
            require(not info.st_mode & 0o077, '节点状态目录权限不安全；未清理旧安装')
        else:
            require(info.st_uid == 0 and not info.st_mode & 0o022, '安装目录权限不安全；未清理旧安装')


def validate():
    for path in (ROOT, ROOT / 'node-engines', CONFIG, UNITS):
        safe_directory(path)
    safe_directory(STATE, state=True)
    require(not os.path.lexists(STATE / 'credentials.json'), '节点已有注册身份，保留现有数据；请重跑同一条安装命令自动重装')
    paths = managed_files()
    for path in paths:
        if os.path.lexists(path):
            require(not path.is_symlink() and stat.S_ISREG(path.lstat().st_mode), '节点安装目标是链接或特殊文件；未清理旧安装')
    lock = ROOT / '.node-update.lock'
    if os.path.lexists(lock):
        info = lock.lstat()
        require(not lock.is_symlink() and stat.S_ISREG(info.st_mode) and info.st_uid == 0 and
                info.st_nlink == 1 and not info.st_mode & 0o077, '节点安装锁不安全；未清理旧安装')
    return [path for path in paths if path.exists()]


def prepare_lock():
    validate()
    ROOT.mkdir(mode=0o755, parents=False, exist_ok=True)
    descriptor = os.open(ROOT / '.node-update.lock', os.O_CREAT | os.O_RDWR | os.O_NOFOLLOW, 0o600)
    os.close(descriptor)


def command(*args, check=True):
    return subprocess.run(list(args), capture_output=True, check=check, timeout=30)


def reset_unenrolled():
    # Caller holds the same lock used by upgrades/re-enrollment for the whole
    # installation. Validate every target before stopping or deleting anything.
    paths = validate()
    if not paths:
        return
    result = command('systemctl', 'show', '-p', 'LoadState', '--value', SERVICE, check=False)
    loaded = result.stdout.strip()
    require(result.returncode == 0 or loaded == b'not-found', '无法读取旧 HL 服务状态；未清理旧安装')
    if loaded and loaded != b'not-found':
        command('systemctl', 'stop', SERVICE)
        require(command('systemctl', 'is-active', '--quiet', SERVICE, check=False).returncode != 0,
                '旧 HL 节点服务尚未停止，未清理旧安装')
    # A previous enrollment might have finished while systemd was stopping it.
    # Preserve that identity and all programs; the next run takes reenrollment.
    if os.path.lexists(STATE / 'credentials.json'):
        if loaded and loaded != b'not-found':
            command('systemctl', 'start', SERVICE)
        raise ValueError('节点刚完成注册，已保留身份；重跑同一条命令即可自动重装')
    validate()
    for path in paths:
        path.unlink()
    command('systemctl', 'daemon-reload')
    print('[HL-panel 节点] 已自动清理未注册的旧 HL 程序，正在安装本次版本。')


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--prepare-lock', action='store_true')
    args = parser.parse_args()
    require(os.geteuid() == 0, '请以 root 执行节点安装')
    if args.prepare_lock:
        prepare_lock()
    else:
        reset_unenrolled()


if __name__ == '__main__':
    try:
        main()
    except (OSError, ValueError, subprocess.SubprocessError) as error:
        print('节点自动安装未完成：' + str(error), file=sys.stderr)
        sys.exit(1)
