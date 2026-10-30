#!/usr/bin/env python3
"""Transactional installation of the fixed-purpose, local node updater."""
import argparse
import hashlib
import json
import os
from pathlib import Path
import re
import shutil
import subprocess
import sys
import tempfile

ROOT = Path('/opt/hl-panel')
UNITS = Path('/etc/systemd/system')
NAMES = ('node-update-worker.py', 'update.py', 'update-node.py')
UNIT_NAMES = ('hl-panel-node-update.socket', 'hl-panel-node-update.service')
TC_NAME = '30-traffic-control.conf'
ASSET_NAMES = ('geoip.dat', 'geosite.dat')
LEGACY_STAGING = Path('/var/backups/hl-panel-node')
UPDATE_STATE = Path('/var/lib/hl-panel-node-update')


def bridge_legacy_updater(release):
    """An old updater calls this new installer but keeps its own code in memory."""
    stage = release.parent
    if release.name != 'release' or stage.parent != LEGACY_STAGING or (stage/'.switching').exists():
        return
    if not re.fullmatch(r'update-v[0-9]+\.[0-9]+\.[0-9]+-[A-Za-z0-9_-]+', stage.name):
        raise ValueError('invalid legacy update transaction')
    safe(LEGACY_STAGING)
    safe(stage)
    if stage.stat().st_mode & 0o077:
        raise ValueError('unsafe legacy update transaction')
    # Only the managed updater's direct child may start this transition.
    arguments = Path('/proc')/str(os.getppid())/'cmdline'
    parent = [arg.decode() for arg in arguments.read_bytes().split(b'\0') if arg]
    if str(ROOT/'update-node.py') not in parent or '--rollback' in parent:
        return
    task = []
    if '--result-file' in parent:
        report = Path(parent[parent.index('--result-file')+1])
        if (report.name != 'result.json' or report.parent.parent != UPDATE_STATE or
                not re.fullmatch(r'[a-f0-9]{8}-(?:[a-f0-9]{4}-){3}[a-f0-9]{12}', report.parent.name)):
            raise ValueError('unsafe legacy update receipt')
        task.append(report.parent.name)
    subprocess.Popen([sys.executable, str(release/'deploy/update-node.py'), '--cleanup-task',
                      *task, '--transaction', str(stage)], stdin=subprocess.DEVNULL,
                     stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL,
                     close_fds=True, start_new_session=True)


def destinations():
    return ([(name, ROOT / name, 0o755) for name in NAMES] +
            [(name, UNITS / name, 0o644) for name in UNIT_NAMES] +
            [(name, ROOT / 'node-engines' / name, 0o644) for name in ASSET_NAMES] +
            [(TC_NAME, UNITS / 'hl-panel-edge-agent.service.d' / TC_NAME, 0o644)])


def safe_parent(dest):
    if dest.name == TC_NAME and not dest.parent.exists() and not dest.parent.is_symlink():
        safe(dest.parent.parent)
    else:
        safe(dest.parent)


def safe(path):
    if path.is_symlink() or path.stat().st_uid != 0 or path.stat().st_mode & 0o022:
        raise ValueError('unsafe maintenance file')


def command(*args):
    subprocess.run(list(args), check=True, capture_output=True, timeout=30)


def replace(source, destination, mode):
    fd, name = tempfile.mkstemp(dir=destination.parent)
    try:
        with os.fdopen(fd, 'wb') as out:
            out.write(source.read_bytes())
            out.flush()
            os.fsync(out.fileno())
        os.chmod(name, mode)
        os.replace(name, destination)
    finally:
        if os.path.exists(name):
            os.unlink(name)


def snapshot(backup):
    backup.mkdir(mode=0o700, exist_ok=False)
    manifest = {'tc_directory_present': (UNITS / 'hl-panel-edge-agent.service.d').exists()}
    for name, dest, _ in destinations():
        safe_parent(dest)
        if dest.exists() or dest.is_symlink():
            safe(dest)
            shutil.copy2(dest, backup / name)
            manifest[name] = {'sha256': hashlib.sha256((backup / name).read_bytes()).hexdigest(), 'mode':dest.stat().st_mode & 0o777}
        else:
            manifest[name] = None
    for field, args in [('enabled', ['is-enabled', '--quiet']), ('active', ['is-active', '--quiet'])]:
        manifest[field] = subprocess.run(['systemctl', *args, 'hl-panel-node-update.socket'], capture_output=True, timeout=10).returncode == 0
    (backup / 'manifest.json').write_text(json.dumps(manifest))
    (backup / 'manifest.json').chmod(0o600)


def restore(backup):
    safe(backup)
    safe(backup / 'manifest.json')
    manifest = json.loads((backup / 'manifest.json').read_text())
    for name, dest, _ in destinations():
        if name not in manifest:  # Backups created before resource repair support.
            continue
        saved = manifest[name]
        safe_parent(dest)
        if dest.exists() or dest.is_symlink():
            safe(dest)
        if saved is not None:
            safe(backup / name)
            if hashlib.sha256((backup / name).read_bytes()).hexdigest() != saved['sha256']:
                raise ValueError('maintenance backup checksum mismatch')
    # Do not kill the worker performing this rollback. Its next activation
    # reads the restored files; the current interpreter completes the report.
    if not manifest['active']:
        command('systemctl', 'stop', 'hl-panel-node-update.socket')
    if not manifest['enabled']:
        command('systemctl', 'disable', 'hl-panel-node-update.socket')
    for name, dest, _ in destinations():
        if name not in manifest:
            continue
        saved = manifest[name]
        if saved is None:
            if dest.exists():
                dest.unlink()
        else:
            if name == TC_NAME and not dest.parent.exists():
                dest.parent.mkdir(mode=0o755)
            replace(backup / name, dest, saved['mode'])
    tc_directory = UNITS / 'hl-panel-edge-agent.service.d'
    if not manifest.get('tc_directory_present', True) and tc_directory.exists() and not any(tc_directory.iterdir()):
        tc_directory.rmdir()
    command('systemctl', 'daemon-reload')
    if manifest['enabled']:
        command('systemctl', 'enable', 'hl-panel-node-update.socket')
    if manifest['active']:
        command('systemctl', 'start', 'hl-panel-node-update.socket')


def asset_plan(release, candidate_only=False):
    """Only missing/empty resources are repaired; existing custom data is retained."""
    planned = {}
    for name in ASSET_NAMES:
        source = release / 'bin' / name
        if not source.is_file() or source.stat().st_size == 0:
            raise ValueError('release is missing nonempty Xray resource: ' + name)
        safe(source)
        if candidate_only:
            planned[name] = source
            continue
        dest = ROOT / 'node-engines' / name
        if dest.exists() or dest.is_symlink():
            safe_parent(dest)
            safe(dest)
            if not dest.is_file():
                raise ValueError('invalid Xray resource path: ' + name)
        planned[name] = source if not dest.exists() or dest.stat().st_size == 0 else dest
    return planned


def preflight(release, install_missing=False, candidate_only=False):
    """No service stops, enrollment changes, or interface/filter writes."""
    binary = release / 'bin/xray'
    safe(binary)
    identity = subprocess.run([str(binary), 'version'], check=True, capture_output=True, timeout=15)
    if not identity.stdout.startswith(b'Xray 26.7.28 '):
        raise ValueError('Xray must remain pinned to 26.7.28')
    planned = asset_plan(release, candidate_only=candidate_only)
    # Exercise the parser with the exact mix of retained and repaired data.
    # This detects truncation/corruption instead of treating file existence as health.
    with tempfile.TemporaryDirectory(prefix='hl-node-resource-check-') as directory:
        staging = Path(directory)
        for name, source in planned.items():
            shutil.copyfile(source, staging / name)
        config = staging / 'check.json'
        config.write_text(json.dumps({'log': {'loglevel': 'none'},
            'outbounds': [{'tag': 'direct', 'protocol': 'freedom'}],
            'routing': {'rules': [
                {'type': 'field', 'ip': ['geoip:private'], 'outboundTag': 'direct'},
                {'type': 'field', 'domain': ['geosite:cn'], 'outboundTag': 'direct'}]}}))
        try:
            subprocess.run([str(binary), 'run', '-test', '-config', str(config)],
                           check=True, capture_output=True, timeout=30,
                           env=dict(os.environ, XRAY_LOCATION_ASSET=str(staging)))
        except subprocess.CalledProcessError:
            raise ValueError('Xray resource validation failed; existing data preserved, node service not stopped') from None
    helper = release / 'deploy/configure-node-traffic-control.py'
    safe(helper)
    arguments = [sys.executable, '-I', str(helper), '--check']
    if install_missing:
        arguments.append('--install-missing')
    subprocess.run(arguments, check=True, capture_output=True, timeout=360)
    return planned


def repair_assets(release):
    planned = preflight(release, install_missing=True)
    changed = []
    with tempfile.TemporaryDirectory(prefix='hl-node-resource-backup-') as directory:
        backup = Path(directory)
        try:
            for name, source in planned.items():
                dest = ROOT / 'node-engines' / name
                if source == dest:
                    continue
                safe_parent(dest)
                previous = None
                if dest.exists():
                    previous = backup / name
                    shutil.copy2(dest, previous)
                # Record before replacement: a write/rename failure also rolls
                # back any earlier resource repaired in this transaction.
                changed.append((dest, previous))
                replace(source, dest, 0o644)
        except Exception:
            for dest, previous in reversed(changed):
                if previous is None:
                    dest.unlink(missing_ok=True)
                else:
                    replace(previous, dest, previous.stat().st_mode & 0o777)
            raise
    for dest, _ in changed:
        print('[HL-panel 节点] 已补齐 Xray 资源：' + dest.name)


def install(release):
    if os.geteuid() != 0:
        raise ValueError('root required')
    sources = {name: release / 'deploy' / name for name in NAMES}
    sources.update({name: release / 'deploy/systemd' / name for name in UNIT_NAMES})
    sources.update({name: release / 'bin' / name for name in ASSET_NAMES})
    helper = release / 'deploy/configure-node-traffic-control.py'
    safe(helper)
    if not helper.is_file():
        raise ValueError('missing traffic-control provisioner')
    for name, dest, _ in destinations():
        if name != TC_NAME:
            safe(sources[name])
            if not sources[name].is_file():
                raise ValueError('invalid release')
        safe_parent(dest)
        if dest.exists() or dest.is_symlink():
            safe(dest)
    planned = preflight(release, install_missing=True)
    command('systemd-analyze', 'verify', str(sources[UNIT_NAMES[0]]), str(sources[UNIT_NAMES[1]]))
    with tempfile.TemporaryDirectory(prefix='hl-node-maintenance-') as directory:
        backup = Path(directory) / 'backup'
        snapshot(backup)
        try:
            for name, dest, mode in destinations():
                if name != TC_NAME:
                    if name in ASSET_NAMES and planned[name] == dest:
                        continue
                    replace(sources[name], dest, mode)
            # This new-release installer is also invoked by old updaters.
            # Service defaults survive their later saved-JSON installation;
            # no root pre-start command or extra service restart is needed.
            subprocess.run([sys.executable, '-I', str(helper), '--service-defaults', '--install-missing'],
                           check=True, capture_output=True, timeout=360)
            command('systemctl', 'daemon-reload')
            command('systemctl', 'enable', '--now', 'hl-panel-node-update.socket')
            bridge_legacy_updater(release)
        except Exception:
            restore(backup)
            raise


if __name__ == '__main__':
    parser = argparse.ArgumentParser()
    parser.add_argument('release', nargs='?', type=Path)
    parser.add_argument('--snapshot', type=Path)
    parser.add_argument('--restore', type=Path)
    parser.add_argument('--preflight', action='store_true')
    parser.add_argument('--candidate-only', action='store_true')
    parser.add_argument('--repair-assets', action='store_true')
    parser.add_argument('--install-missing', action='store_true')
    args = parser.parse_args()
    if args.candidate_only and not args.preflight:
        parser.error('--candidate-only requires --preflight')
    if os.geteuid() != 0:
        raise SystemExit('root required')
    if args.snapshot:
        snapshot(args.snapshot)
    elif args.restore:
        restore(args.restore)
    elif args.release:
        if args.preflight:
            preflight(args.release, install_missing=args.install_missing, candidate_only=args.candidate_only)
            print('[HL-panel 节点] 依赖与 Xray 资源预检通过，未停止节点服务。')
        elif args.repair_assets:
            repair_assets(args.release)
        else:
            install(args.release)
    else:
        parser.error('release or backup required')
