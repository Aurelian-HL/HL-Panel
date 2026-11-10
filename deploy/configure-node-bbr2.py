#!/usr/bin/env python3
"""Enable the kernel's TCP BBR2 default without restarting networking or engines."""
import argparse
import fnmatch
import importlib.util
import json
import os
from pathlib import Path
import re
import shutil
import subprocess
import sys
import tempfile

KEY = 'net.ipv4.tcp_congestion_control'
AVAILABLE = 'net.ipv4.tcp_available_congestion_control'
ALGORITHM = 'bbr2'
KERNEL_MODULE = 'tcp_bbr2'
CONFIG = Path('/etc/sysctl.d/90-hl-panel-node-bbr2.conf')
BACKUPS = Path('/var/backups/hl-panel-node')
DIRECTORIES = tuple(map(Path, ('/etc/sysctl.d', '/run/sysctl.d',
    '/usr/local/lib/sysctl.d', '/usr/lib/sysctl.d', '/lib/sysctl.d')))
LEGACY = Path('/etc/sysctl.conf')
CONTENT = b'# Managed by HL-panel node installer; TCP BBR2 only.\nnet.ipv4.tcp_congestion_control = bbr2\n'


class BBRError(Exception):
    pass


def require(condition, message):
    if not condition:
        raise BBRError(message)


def command(args):
    try:
        result = subprocess.run(args, capture_output=True, text=True, timeout=15)
    except (OSError, subprocess.TimeoutExpired):
        raise BBRError('无法执行内核参数检查或设置') from None
    require(result.returncode == 0, '内核拒绝参数检查或设置（容器可能无权限）')
    return result.stdout.strip()


def read(key):
    return command(['sysctl', '-n', key])


def write(value):
    require(re.fullmatch(r'[a-zA-Z0-9_-]+', value), '拥塞控制算法名称无效')
    command(['sysctl', '-q', '-w', KEY + '=' + value])
    require(read(KEY) == value, '内核参数读回值与请求值不一致')


def trusted(path, private=False):
    stat = path.stat()
    require(not path.is_symlink() and stat.st_uid == 0 and
            not stat.st_mode & (0o077 if private else 0o022), '文件或目录权限不安全：' + str(path))


def atomic(path, data, mode=0o644, uid=0, gid=0):
    fd, name = tempfile.mkstemp(prefix='.hl-bbr2-', dir=path.parent)
    try:
        with os.fdopen(fd, 'wb') as stream:
            stream.write(data)
            stream.flush()
            os.fsync(stream.fileno())
        os.chmod(name, mode)
        os.chown(name, uid, gid)
        os.replace(name, path)
    finally:
        if Path(name).exists():
            Path(name).unlink()


def setting(path, wildcards=False):
    for line in path.read_text().splitlines():
        line = line.strip()
        if not line or line.startswith(('#', ';')):
            continue
        match = re.match(r'-?([^\s=]+)\s*=\s*(\S+)', line)
        if match:
            key, value = match.groups()
            key = key.replace('/', '.')
            pattern = any(char in key for char in '*?[')
            if wildcards and pattern and fnmatch.fnmatchcase(KEY, key):
                yield value
            elif not wildcards and key == KEY:
                yield value


def conflict():
    # sysctl.d selects the first occurrence of a basename by directory priority,
    # then applies files in lexical order. procps may apply sysctl.conf last.
    effective = {}
    for directory in DIRECTORIES:
        if directory.is_dir():
            for path in directory.glob('*.conf'):
                effective.setdefault(path.name, path)
    for name, path in sorted(effective.items()):
        if name >= CONFIG.name and path != CONFIG and path.is_file():
            values = list(setting(path))
            # Conservatively reject conflicting globs: procps and systemd have
            # different exclusion/precedence semantics for wildcard settings.
            patterns = list(setting(path, wildcards=True))
            if (values and values[-1] != ALGORITHM) or any(v != ALGORITHM for v in patterns):
                return path
    if LEGACY.is_file():
        values = list(setting(LEGACY))
        patterns = list(setting(LEGACY, wildcards=True))
        if (values and values[-1] != ALGORITHM) or any(v != ALGORITHM for v in patterns):
            return LEGACY
    return None


def snapshot(previous, old):
    require(not BACKUPS.is_symlink(), '备份目录是链接')
    BACKUPS.mkdir(parents=True, exist_ok=True, mode=0o700)
    trusted(BACKUPS, private=True)
    directory = Path(tempfile.mkdtemp(prefix='network-bbr2-', dir=BACKUPS))
    info = {'algorithm': previous, 'existed': old is not None}
    if old is not None:
        stat = CONFIG.stat()
        info.update(mode=stat.st_mode & 0o777, uid=stat.st_uid, gid=stat.st_gid)
        atomic(directory / 'previous.conf', old, 0o600)
    atomic(directory / 'state.json', (json.dumps(info) + '\n').encode(), 0o600)
    atomic(directory / 'configure-node-bbr2.py', Path(__file__).read_bytes(), 0o600)
    return directory, info


def restore(directory, info):
    failures = []
    try:
        if info['existed']:
            atomic(CONFIG, (directory / 'previous.conf').read_bytes(), info['mode'], info['uid'], info['gid'])
        elif CONFIG.exists():
            CONFIG.unlink()
    except Exception:
        failures.append('持久配置')
    try:
        if read(KEY) != info['algorithm']:
            write(info['algorithm'])
    except Exception:
        failures.append('运行算法')
    require(not failures, '未能恢复' + '、'.join(failures) + '；备份：' + str(directory))


def load_bbr_fallback():
    spec = importlib.util.spec_from_file_location(
        'hl_node_bbr_fallback', Path(__file__).with_name('configure-node-bbr.py'))
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


def enable():
    previous = read(KEY)
    require(re.fullmatch(r'[a-zA-Z0-9_-]+', previous), '当前拥塞控制算法名称无效')
    # bbr is the previous default and may be upgraded to bbr2. Preserve
    # unknown BBR variants (for example bbr3) rather than silently replacing
    # an operator-selected algorithm.
    if previous.startswith('bbr') and previous not in ('bbr', ALGORITHM):
        print('[HL-panel 节点] 警告：正在使用其他 BBR 算法 ' + previous + '，保留现有配置，未切换为 bbr2。', file=sys.stderr)
        return None
    if ALGORITHM not in read(AVAILABLE).split():
        if shutil.which('modprobe'):
            try:
                command(['modprobe', KERNEL_MODULE])
            except BBRError:
                pass
        if ALGORITHM not in read(AVAILABLE).split():
            print('[HL-panel 节点] 当前内核不提供 TCP BBR2，尝试普通 TCP BBR；未更换内核或重启系统。', file=sys.stderr)
            fallback = load_bbr_fallback()
            try:
                return fallback.enable()
            except fallback.BBRError as error:
                raise BBRError('普通 TCP BBR 回退未完成：' + str(error)) from None
    require(CONFIG.parent.is_dir(), '未找到 sysctl.d 目录')
    trusted(CONFIG.parent)
    require(not CONFIG.is_symlink(), 'BBR2 配置路径是链接，拒绝覆盖')
    old = None
    if CONFIG.exists():
        require(CONFIG.is_file(), 'BBR2 配置路径不是普通文件')
        trusted(CONFIG)
        old = CONFIG.read_bytes()
        require(old == CONTENT, 'BBR2 受管路径存在自定义配置，未覆盖：' + str(CONFIG))
    other = conflict()
    require(other is None, '已有配置会覆盖 BBR2，保留原设置，请人工核对：' + str(other))
    if previous == ALGORITHM and old == CONTENT:
        print('[HL-panel 节点] TCP BBR2 默认值已启用，受管持久配置已存在。')
        return None
    directory, info = snapshot(previous, old)
    try:
        atomic(CONFIG, CONTENT)
        write(ALGORITHM)
    except Exception:
        try:
            restore(directory, info)
        except Exception:
            raise BBRError('BBR2 设置失败且恢复未完成，请核对备份：' + str(directory)) from None
        raise BBRError('BBR2 设置失败，已恢复原算法及配置；备份：' + str(directory)) from None
    print('[HL-panel 节点] TCP BBR2 默认值已启用并读回验证；未重启系统、网络或转发引擎。')
    print('BBR2 仅影响后续按默认值创建的 TCP socket；现有连接和监听不强制切换，UDP 不受此项控制。')
    print('BBR2 备份：' + str(directory))
    print('BBR2 回滚：python3 ' + str(directory / 'configure-node-bbr2.py') + ' --rollback ' + str(directory))
    return directory


def rollback(directory):
    require(directory.is_absolute() and directory.parent == BACKUPS and
            directory.name.startswith('network-bbr2-'), '仅接受本机 BBR2 备份目录')
    trusted(BACKUPS, private=True)
    trusted(directory, private=True)
    state = directory / 'state.json'
    trusted(state, private=True)
    info = json.loads(state.read_text())
    require(type(info.get('existed')) is bool and
            isinstance(info.get('algorithm'), str) and
            re.fullmatch(r'[a-zA-Z0-9_-]+', info['algorithm']), '备份状态无效')
    old = None
    if info['existed']:
        trusted(directory / 'previous.conf', private=True)
        old = (directory / 'previous.conf').read_bytes()
    trusted(CONFIG.parent)
    require(not CONFIG.is_symlink(), 'BBR2 配置路径是链接，拒绝回滚')
    current = CONFIG.read_bytes() if CONFIG.exists() else None
    require(current in (CONTENT, old), 'BBR2 文件已被其他操作修改，未覆盖')
    require(read(KEY) in (ALGORITHM, info['algorithm']), '当前算法已被其他操作修改，未覆盖')
    restore(directory, info)
    print('[HL-panel 节点] 已恢复 BBR2 变更前的默认算法和受管配置；未重启任何服务。')


def main():
    parser = argparse.ArgumentParser(description='安全设置节点 TCP BBR2 默认值')
    parser.add_argument('--rollback', type=Path)
    args = parser.parse_args()
    require(sys.platform == 'linux' and os.geteuid() == 0, '请在 Linux 节点以 root 执行')
    import fcntl
    # Share the ordinary BBR helper's lock so fallback and direct invocations
    # cannot change the same kernel default concurrently.
    descriptor = os.open('/run/hl-panel-node-bbr.lock', os.O_WRONLY | os.O_CREAT | os.O_NOFOLLOW, 0o600)
    with os.fdopen(descriptor, 'a') as lock:
        try:
            fcntl.flock(lock.fileno(), fcntl.LOCK_EX | fcntl.LOCK_NB)
        except BlockingIOError:
            raise BBRError('另一个 BBR2 设置或回滚正在执行') from None
        if args.rollback:
            rollback(args.rollback)
        else:
            enable()


if __name__ == '__main__':
    try:
        main()
    except (BBRError, OSError, ValueError, KeyError) as error:
        print('[HL-panel 节点] 警告：TCP BBR2 未配置完成：' + str(error), file=sys.stderr)
        sys.exit(1)
