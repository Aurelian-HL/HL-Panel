#!/usr/bin/env python3
"""Real TCP BBR acceptance on a disposable GitHub-hosted VM only."""
import importlib.util
import json
import os
from pathlib import Path
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
spec = importlib.util.spec_from_file_location('node_bbr', repo/'deploy/configure-node-bbr.py')
bbr = importlib.util.module_from_spec(spec)
spec.loader.exec_module(bbr)
assert not bbr.CONFIG.exists(), 'requires a new isolated VM'


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
original = bbr.read(bbr.KEY)
queues = run(['tc', 'qdisc', 'show'])
# Exercise the public CLI's lock before any sysctl mutation.
with open('/run/hl-panel-node-bbr.lock', 'a') as lock:
    fcntl.flock(lock.fileno(), fcntl.LOCK_EX | fcntl.LOCK_NB)
    busy = subprocess.run([sys.executable, str(repo/'deploy/configure-node-bbr.py')],
                          capture_output=True, text=True, timeout=20)
    assert busy.returncode != 0 and '另一个 BBR 设置或回滚正在执行' in busy.stderr
    assert bbr.read(bbr.KEY) == original and not bbr.CONFIG.exists()
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
try:
    client.sendall(b'before')
    assert receive(server, 6) == b'before'
    backup = bbr.enable()
    assert backup is not None, 'disposable Linux kernel must provide usable BBR'
    assert bbr.read(bbr.KEY) == 'bbr'
    assert bbr.CONFIG.read_bytes() == bbr.CONTENT
    checks.append('real kernel BBR default written and read back')
    with socket.socket() as fresh:
        assert fresh.getsockopt(socket.IPPROTO_TCP, algorithm_option, 32).rstrip(b'\0') == b'bbr'
    assert server.getsockopt(socket.IPPROTO_TCP, algorithm_option, 32) == before
    client.sendall(b'after')
    assert receive(server, 5) == b'after'
    server.sendall(b'still-connected')
    assert receive(client, 15) == b'still-connected'
    assert run(['tc', 'qdisc', 'show']) == queues
    checks.append('new TCP socket uses BBR; existing bidirectional connection and qdisc remain intact')
    assert bbr.enable() is None
    checks.append('repeated enable is a no-op')
    # Emulate just this managed file being loaded by boot-time sysctl, without
    # replaying any other machine configuration or restarting a service.
    bbr.write(original)
    run(['sysctl', '-p', str(bbr.CONFIG)])
    assert bbr.read(bbr.KEY) == 'bbr'
    checks.append('managed persistence file reloads BBR through real sysctl')
finally:
    if backup is not None:
        bbr.rollback(backup)
    listener.close()
    client.close()
    server.close()
assert bbr.read(bbr.KEY) == original
assert not bbr.CONFIG.exists()
assert run(['tc', 'qdisc', 'show']) == queues
checks.append('rollback restores original runtime default and removes new managed config')
print(json.dumps({'status':'passed', 'checks':checks}, indent=2))
