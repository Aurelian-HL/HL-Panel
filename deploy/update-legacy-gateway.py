#!/usr/bin/env python3
"""Replace only an existing managed legacy gateway, with durable rollback.

No enrollment, config generation, NAT edits, or arbitrary service selection.
An absent gateway is a no-op; an inactive gateway stays inactive.
"""
import argparse
import json
import os
from pathlib import Path
import pwd
import re
import shutil
import stat
import subprocess
import sys
import tempfile
import time

from update import UpdateError, digest, private_regular, require

BINARY = Path('/usr/local/lib/hl-panel/connection-gateway')
CONFIG = Path('/etc/hl-connection-gateway')
SERVICE = 'hl-legacy-gateway.service'


def command(args):
    try:
        result = subprocess.run(args, capture_output=True, timeout=30)
    except (OSError, subprocess.TimeoutExpired):
        raise UpdateError('旧网关服务操作超时或不可用') from None
    require(result.returncode == 0, '旧网关服务操作失败；未输出私有配置')
    return result.stdout.decode('utf-8').strip()


def config_owners():
    owners = {0}
    try:
        owners.add(pwd.getpwnam('hlgateway').pw_uid)
    except KeyError:
        pass
    return owners


def secure_path(path, directory=False, private=False, config_file=False):
    require(path.is_absolute(), '旧网关路径必须为绝对路径')
    for parent in (*reversed(path.parents), path):
        require(not parent.is_symlink(), '旧网关受管路径不能包含链接')
        info = parent.stat()
        owners = config_owners() if parent == path and config_file else {0}
        require(info.st_uid in owners and not info.st_mode & 0o022, '旧网关路径所有权或权限不安全')
    require(path.is_dir() if directory else path.is_file(), '旧网关受管文件缺失')
    if private:
        require(not path.stat().st_mode & 0o077, '旧网关回滚目录必须私有')


def service_state():
    text = command(['systemctl', 'show', SERVICE, '-p', 'LoadState', '-p', 'ActiveState',
                    '-p', 'MainPID', '-p', 'FragmentPath', '-p', 'DropInPaths', '-p', 'ExecStart'])
    values = dict(line.split('=', 1) for line in text.splitlines() if '=' in line)
    if values.get('LoadState') == 'not-found':
        return None
    require(values.get('LoadState') == 'loaded', '旧网关不是受管服务；当前程序未替换')
    paths = re.findall(r'path=([^ ;}]+)', values.get('ExecStart', ''))
    require(paths == [str(BINARY)], '旧网关使用自定义程序路径，拒绝自动替换')
    require(re.fullmatch(r'[0-9]+', values.get('MainPID', '')) is not None, '旧网关进程信息无效')
    return values


def fingerprints(state):
    secure_path(CONFIG, directory=True)
    files = list(CONFIG.rglob('*'))
    require(len(files) <= 512, '旧网关配置目录过大，未修改')
    paths = [Path(state['FragmentPath']), *(Path(p) for p in state.get('DropInPaths', '').split())]
    for path in files:
        secure_path(path, directory=path.is_dir(), config_file=path.is_file())
        if path.is_file():
            paths.append(path)
    result = {}
    for path in paths:
        secure_path(path, config_file=path.is_relative_to(CONFIG))
        require(path.stat().st_size <= 8 * 1024 * 1024, '旧网关配置文件过大，未修改')
        result[str(path)] = digest(path)
    require(str(CONFIG/'bundle.json') in result, '旧网关缺少受管配置')
    return result


def candidate(release):
    path = release/'bin/connection-gateway'
    private_regular(path)
    version = command([str(path), '-version'])
    require(re.fullmatch(r'v[0-9]+\.[0-9]+\.[0-9]+', version) is not None,
            '旧网关候选程序版本无效')
    return path, version


def preflight(release):
    state = service_state()
    if state is None:
        return None
    require(state.get('ActiveState') in ('active', 'inactive'),
            '旧网关不是稳定状态；当前程序未替换')
    secure_path(BINARY)
    source, version = candidate(release)
    return dict(state=state, files=fingerprints(state), version=version,
                new_sha256=digest(source), old_sha256=digest(BINARY))


def prepare(release, directory):
    value = preflight(release)
    secure_path(directory.parent, directory=True, private=True)
    require(not directory.exists() and not directory.is_symlink(), '旧网关回滚目录已存在')
    directory.mkdir(mode=0o700)
    if value is None:
        value = dict(absent=True)
    else:
        info = BINARY.stat()
        value.update(mode=stat.S_IMODE(info.st_mode), uid=info.st_uid, gid=info.st_gid,
                     was_active=value['state']['ActiveState'] == 'active')
        shutil.copy2(BINARY, directory/'connection-gateway')
        (directory/'connection-gateway').chmod(0o600)
        require(digest(directory/'connection-gateway') == value['old_sha256'], '旧网关备份摘要不符')
    with (directory/'manifest.json').open('x', encoding='utf-8') as stream:
        json.dump(value, stream)
        stream.flush()
        os.fsync(stream.fileno())
    (directory/'manifest.json').chmod(0o600)


def snapshot(directory):
    secure_path(directory, directory=True, private=True)
    private_regular(directory/'manifest.json')
    return json.loads((directory/'manifest.json').read_text(encoding='utf-8'))


def unchanged(value):
    state = service_state()
    require(state is not None and fingerprints(state) == value['files'],
            '旧网关配置在更新期间发生变化，未覆盖配置')
    secure_path(BINARY)
    return state


def replace(source, value):
    fd, name = tempfile.mkstemp(prefix='.connection-gateway-', dir=BINARY.parent)
    try:
        with os.fdopen(fd, 'wb') as stream, source.open('rb') as incoming:
            shutil.copyfileobj(incoming, stream)
            os.fchmod(stream.fileno(), value['mode'])
            os.fchown(stream.fileno(), value['uid'], value['gid'])
            stream.flush()
            os.fsync(stream.fileno())
        os.replace(name, BINARY)
        fd = os.open(BINARY.parent, os.O_RDONLY | os.O_DIRECTORY)
        try:
            os.fsync(fd)
        finally:
            os.close(fd)
    finally:
        if os.path.exists(name):
            os.unlink(name)


def verify_running(value, expected):
    for _ in range(15):
        state = unchanged(value)
        if not value['was_active']:
            require(state['ActiveState'] == 'inactive', '原来停用的旧网关被意外启动')
            return
        pid = int(state['MainPID'])
        if state['ActiveState'] == 'active' and pid > 0:
            try:
                if digest(Path('/proc')/str(pid)/'exe') == expected:
                    return
            except (OSError, ProcessLookupError):
                pass
        time.sleep(1)
    raise UpdateError('旧网关运行程序摘要未追平')


def restore(directory):
    value = snapshot(directory)
    if value.get('absent'):
        return
    unchanged(value)
    old = directory/'connection-gateway'
    private_regular(old)
    require(digest(old) == value['old_sha256'], '旧网关回滚文件摘要不符')
    current = digest(BINARY)
    require(current in (value['old_sha256'], value['new_sha256']), '旧网关程序已被其他操作修改')
    if current != value['old_sha256']:
        replace(old, value)
    if value['was_active']:
        command(['systemctl', 'restart', SERVICE])
    verify_running(value, value['old_sha256'])


def apply(release, directory):
    value = snapshot(directory)
    if value.get('absent'):
        return
    state = unchanged(value)
    require((state['ActiveState'] == 'active') == value['was_active'], '旧网关启停状态已变化')
    source, version = candidate(release)
    require(version == value['version'] and digest(source) == value['new_sha256'], '旧网关候选程序被修改')
    current = digest(BINARY)
    require(current in (value['old_sha256'], value['new_sha256']), '旧网关程序已被其他操作修改')
    if current == value['new_sha256']:
        verify_running(value, current)
        return
    try:
        replace(source, value)
        if value['was_active']:
            command(['systemctl', 'restart', SERVICE])
        verify_running(value, value['new_sha256'])
    except Exception:
        try:
            restore(directory)
        except Exception:
            raise UpdateError('旧网关更新失败且回滚未完成，保留回滚目录：' + str(directory)) from None
        raise UpdateError('旧网关更新失败，已恢复原程序和启停状态') from None


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    actions = parser.add_mutually_exclusive_group(required=True)
    actions.add_argument('--preflight', action='store_true')
    actions.add_argument('--prepare', type=Path)
    actions.add_argument('--apply', type=Path)
    actions.add_argument('--restore', type=Path)
    parser.add_argument('--release', type=Path)
    args = parser.parse_args()
    require(sys.platform == 'linux' and os.geteuid() == 0, '请在 Linux 节点机使用 root 运行')
    if args.restore:
        restore(args.restore)
    else:
        require(args.release is not None and args.release.is_absolute(), '缺少已验证的发行目录')
        if args.preflight:
            preflight(args.release)
        elif args.prepare:
            prepare(args.release, args.prepare)
        else:
            apply(args.release, args.apply)


if __name__ == '__main__':
    try:
        main()
    except Exception as error:
        print('[HL-panel 旧网关更新] ' + (str(error) if isinstance(error, UpdateError) else '操作失败，未输出私有数据'), file=sys.stderr)
        sys.exit(1)
