#!/usr/bin/env python3
"""Real TCP BBR2-preferred/BBR-fallback acceptance on a disposable VM."""
import importlib.util
import json
import os
from pathlib import Path
import shutil
import socket
import subprocess
import sys

if sys.flags.optimize:
    raise RuntimeError('refusing optimized Python: acceptance assertions must remain active')
if sys.platform != 'linux' or os.geteuid() != 0:
    raise RuntimeError('requires root on an isolated Linux VM')
if (os.environ.get('HL_PANEL_DISPOSABLE_INSTALL_TEST') != 'true' or
        os.environ.get('RUNNER_ENVIRONMENT') != 'github-hosted'):
    raise RuntimeError('requires explicit disposable GitHub-hosted runner authorization')
import fcntl
repo = Path(__file__).resolve().parents[2]
spec = importlib.util.spec_from_file_location('node_bbr2', repo/'deploy/configure-node-bbr2.py')
bbr2 = importlib.util.module_from_spec(spec)
spec.loader.exec_module(bbr2)
assert not bbr2.CONFIG.exists(), 'requires a new isolated VM'
bbr = bbr2.load_bbr_fallback()
assert not bbr.CONFIG.exists(), 'requires an empty fallback configuration'


def run(args):
    return subprocess.run(args, capture_output=True, text=True, check=True, timeout=20).stdout.strip()


def receive(stream, length):
    data = b''
    while len(data) < length:
        part = stream.recv(length-len(data))
        if not part:
            raise RuntimeError('existing TCP connection closed unexpectedly')
        data += part
    return data


checks = []
original = bbr2.read(bbr2.KEY)
queues = run(['tc', 'qdisc', 'show'])
available = bbr2.read(bbr2.AVAILABLE).split()
if bbr2.ALGORITHM not in available and shutil.which('modprobe'):
    try:
        bbr2.command(['modprobe', bbr2.KERNEL_MODULE])
    except bbr2.BBRError:
        pass
    available = bbr2.read(bbr2.AVAILABLE).split()
# Exercise the public CLI's lock before any sysctl mutation.
with open('/run/hl-panel-node-bbr.lock', 'a') as lock:
    fcntl.flock(lock.fileno(), fcntl.LOCK_EX | fcntl.LOCK_NB)
    busy = subprocess.run([sys.executable, str(repo/'deploy/configure-node-bbr2.py')],
                          capture_output=True, text=True, timeout=20)
    assert busy.returncode != 0 and '另一个 BBR2 设置或回滚正在执行' in busy.stderr
    assert bbr2.read(bbr2.KEY) == original and not bbr2.CONFIG.exists() and not bbr.CONFIG.exists()
checks.append('CLI rejects concurrent configuration without changing runtime or persistence')
listener = socket.socket()
listener.bind(('127.0.0.1', 0))
listener.listen()
client = socket.create_connection(listener.getsockname(), timeout=5)
server, _ = listener.accept()
server.settimeout(5)
algorithm_option = getattr(socket, 'TCP_CONGESTION', 13)
before = server.getsockopt(socket.IPPROTO_TCP, algorithm_option, 32)
backup = None
selected = None
try:
    client.sendall(b'before')
    assert receive(server, 6) == b'before'
    backup = bbr2.enable()
    # Pick the rollback helper before a failed verification can interrupt us.
    if backup is not None:
        selected = bbr2 if backup.name.startswith('network-bbr2-') else bbr
    algorithm = bbr2.read(bbr2.KEY)
    if backup is None:
        assert algorithm == original and not bbr2.CONFIG.exists() and not bbr.CONFIG.exists()
        available = bbr2.read(bbr2.AVAILABLE).split()
        assert (original.startswith('bbr') and original not in ('bbr', 'bbr2')) or not {'bbr2', 'bbr'}.intersection(available)
        print(json.dumps({'status': 'skipped', 'algorithm': algorithm, 'reason': 'no supported BBR algorithm or operator-selected variant preserved', 'checks': checks}, indent=2))
        raise RuntimeError('BBR acceptance incomplete: isolated runner must enable a supported BBR algorithm')
    assert algorithm in ('bbr2', 'bbr'), 'configuration must enable a supported BBR algorithm'
    assert selected is (bbr2 if algorithm == 'bbr2' else bbr)
    other = bbr if algorithm == 'bbr2' else bbr2
    assert selected.CONFIG.read_bytes() == selected.CONTENT and not other.CONFIG.exists()
    checks.append('real kernel ' + algorithm + ' default written and read back')
    with socket.socket() as fresh:
        assert fresh.getsockopt(socket.IPPROTO_TCP, algorithm_option, 32).rstrip(b'\0') == algorithm.encode()
    assert server.getsockopt(socket.IPPROTO_TCP, algorithm_option, 32) == before
    client.sendall(b'after')
    assert receive(server, 5) == b'after'
    server.sendall(b'still-connected')
    assert receive(client, 15) == b'still-connected'
    assert run(['tc', 'qdisc', 'show']) == queues
    checks.append('new TCP socket uses ' + algorithm + '; existing bidirectional connection and qdisc remain intact')
    assert bbr2.enable() is None
    checks.append('repeated enable is a no-op')
    # Emulate just this managed file being loaded by boot-time sysctl, without
    # replaying any other machine configuration or restarting a service.
    bbr2.write(original)
    run(['sysctl', '-p', str(selected.CONFIG)])
    assert bbr2.read(bbr2.KEY) == algorithm
    checks.append('managed persistence file reloads ' + algorithm + ' through real sysctl')
finally:
    try:
        if backup is not None:
            selected.rollback(backup)
    finally:
        listener.close()
        client.close()
        server.close()
assert bbr2.read(bbr2.KEY) == original
assert not bbr2.CONFIG.exists()
assert not bbr.CONFIG.exists()
assert run(['tc', 'qdisc', 'show']) == queues
checks.append('rollback restores original runtime default and removes new managed config')
print(json.dumps({'status':'passed', 'algorithm': algorithm, 'bbr2_verified': algorithm == 'bbr2', 'checks':checks}, indent=2))
