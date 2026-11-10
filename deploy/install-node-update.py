#!/usr/bin/env python3
"""Transactional installation of the fixed-purpose, local node updater."""
import argparse
import hashlib
import json
import os
from pathlib import Path
import shutil
import subprocess
import tempfile

ROOT = Path('/opt/hl-panel')
UNITS = Path('/etc/systemd/system')
NAMES = ('node-update-worker.py', 'update.py', 'update-node.py')
UNIT_NAMES = ('hl-panel-node-update.socket', 'hl-panel-node-update.service')


def destinations():
    return [(name, ROOT / name, 0o755) for name in NAMES] + [(name, UNITS / name, 0o644) for name in UNIT_NAMES]


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
    manifest = {}
    for name, dest, _ in destinations():
        safe(dest.parent)
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
        saved = manifest[name]
        safe(dest.parent)
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
        saved = manifest[name]
        if saved is None:
            if dest.exists():
                dest.unlink()
        else:
            replace(backup / name, dest, saved['mode'])
    command('systemctl', 'daemon-reload')
    if manifest['enabled']:
        command('systemctl', 'enable', 'hl-panel-node-update.socket')
    if manifest['active']:
        command('systemctl', 'start', 'hl-panel-node-update.socket')


def install(release):
    if os.geteuid() != 0:
        raise ValueError('root required')
    sources = {name: release / 'deploy' / name for name in NAMES}
    sources.update({name: release / 'deploy/systemd' / name for name in UNIT_NAMES})
    for name, dest, _ in destinations():
        safe(sources[name])
        if not sources[name].is_file():
            raise ValueError('invalid release')
        safe(dest.parent)
        if dest.exists() or dest.is_symlink():
            safe(dest)
    command('systemd-analyze', 'verify', str(sources[UNIT_NAMES[0]]), str(sources[UNIT_NAMES[1]]))
    with tempfile.TemporaryDirectory(prefix='hl-node-maintenance-') as directory:
        backup = Path(directory) / 'backup'
        snapshot(backup)
        try:
            for name, dest, mode in destinations():
                replace(sources[name], dest, mode)
            command('systemctl', 'daemon-reload')
            command('systemctl', 'enable', '--now', 'hl-panel-node-update.socket')
        except Exception:
            restore(backup)
            raise


if __name__ == '__main__':
    parser = argparse.ArgumentParser()
    parser.add_argument('release', nargs='?', type=Path)
    parser.add_argument('--snapshot', type=Path)
    parser.add_argument('--restore', type=Path)
    args = parser.parse_args()
    if os.geteuid() != 0:
        raise SystemExit('root required')
    if args.snapshot:
        snapshot(args.snapshot)
    elif args.restore:
        restore(args.restore)
    elif args.release:
        install(args.release)
    else:
        parser.error('release or backup required')
