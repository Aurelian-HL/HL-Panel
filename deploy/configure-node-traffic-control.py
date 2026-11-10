#!/usr/bin/env python3
"""Provision Linux per-rule speed limits without replacing the root qdisc.

Only installation/configuration is performed here. The agent owns its clsact
filters and applies them transactionally with the desired rule bundle.
Existing interface settings are explicit operator choices and are preserved.
"""
import json
import os
from pathlib import Path
import re
import shutil
import subprocess
import tempfile

CONFIG = Path('/etc/hl-panel/edge-agent.json')
DROPIN = Path('/etc/systemd/system/hl-panel-edge-agent.service.d/30-traffic-control.conf')
UNIT = '[Service]\nCapabilityBoundingSet=CAP_NET_ADMIN\nAmbientCapabilities=CAP_NET_ADMIN\n'


def safe(path):
    if path.is_symlink():
        raise ValueError('traffic-control path is a symbolic link')
    stat = path.stat()
    if stat.st_uid != 0 or stat.st_mode & 0o022:
        raise ValueError('traffic-control path has unsafe ownership or permissions')


def replace(path, content, mode, owner):
    fd, name = tempfile.mkstemp(dir=path.parent)
    try:
        with os.fdopen(fd, 'wb') as out:
            out.write(content)
            out.flush()
            os.fsync(out.fileno())
        os.chmod(name, mode)
        os.chown(name, *owner)
        os.replace(name, path)
    finally:
        if os.path.exists(name):
            os.unlink(name)


def configure(config=CONFIG, dropin=DROPIN):
    safe(config.parent)
    safe(config)
    original = config.read_bytes()
    value = json.loads(original)
    if value.get('traffic_control_interface'):
        return {'configured': False, 'preserved_existing_interface': True}
    # Empty custom fields may intentionally disable this feature. Do not
    # silently replace an administrator's explicit configuration.
    if any(key.startswith('traffic_control_') for key in value):
        return {'configured': False, 'preserved_existing_interface': True}
    binaries = {name: shutil.which(name) for name in ('ip', 'tc')}
    if not all(binaries.values()):
        raise ValueError('iproute2 (ip and tc) is required for rule speed limits')
    for binary in binaries.values():
        # Distribution binaries may themselves be links; validate their target.
        safe(Path(binary).resolve(strict=True))
    route = subprocess.run([binaries['ip'], '-j', '-4', 'route', 'get', '1.1.1.1'],
                           check=True, capture_output=True, text=True, timeout=10)
    routes = json.loads(route.stdout)
    if len(routes) != 1 or not re.fullmatch(r'[A-Za-z0-9_.:-]{1,15}', routes[0].get('dev', '')):
        raise ValueError('cannot uniquely determine the IPv4 egress interface')
    interface = routes[0]['dev']
    if interface == 'lo':
        raise ValueError('loopback is not a node egress interface')
    safe(dropin.parent.parent)
    created_directory = not dropin.parent.exists()
    if created_directory:
        dropin.parent.mkdir(mode=0o755)
    safe(dropin.parent)
    previous = None
    if dropin.exists() or dropin.is_symlink():
        safe(dropin)
        previous = dropin.read_bytes()
        if previous != UNIT.encode():
            raise ValueError('custom traffic-control systemd drop-in already exists')
    stat = config.stat()
    value.update(traffic_control_interface=interface, traffic_control_binary=binaries['tc'])
    try:
        replace(dropin, UNIT.encode(), 0o644, (0, 0))
        replace(config, (json.dumps(value) + '\n').encode(), stat.st_mode & 0o777,
                (stat.st_uid, stat.st_gid))
        subprocess.run(['systemctl', 'daemon-reload'], check=True, capture_output=True, timeout=30)
    except Exception:
        replace(config, original, stat.st_mode & 0o777, (stat.st_uid, stat.st_gid))
        if previous is None:
            dropin.unlink(missing_ok=True)
        else:
            replace(dropin, previous, 0o644, (0, 0))
        if created_directory:
            dropin.parent.rmdir()
        subprocess.run(['systemctl', 'daemon-reload'], capture_output=True, timeout=30)
        raise
    return {'configured': True, 'interface': interface, 'root_qdisc_unchanged': True}


if __name__ == '__main__':
    if os.geteuid() != 0:
        raise SystemExit('root required')
    print(json.dumps(configure()))
