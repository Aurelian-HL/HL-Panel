#!/usr/bin/env python3
"""One authenticated local request; only merge ip_local_reserved_ports."""
import fcntl
import json
import os
from pathlib import Path
import pwd
import re
import socket
import struct

RESERVED = Path('/proc/sys/net/ipv4/ip_local_reserved_ports')
LOCK = Path('/run/hl-panel-port-reservation/merge.lock')
MAX_REQUEST = 196608


def parse_ports(value):
    if not isinstance(value, str) or len(value) > MAX_REQUEST:
        raise ValueError('invalid ports')
    ports = set()
    if not value.strip():
        return ports
    for field in value.strip().split(','):
        match = re.fullmatch(r'([0-9]{1,5})(?:-([0-9]{1,5}))?', field.strip())
        if not match:
            raise ValueError('invalid ports')
        first, last = int(match[1]), int(match[2] or match[1])
        if not 1 <= first <= last <= 65535:
            raise ValueError('invalid ports')
        ports.update(range(first, last+1))
    return ports


def format_ports(ports):
    result = []
    values = sorted(ports)
    index = 0
    while index < len(values):
        first = last = values[index]
        index += 1
        while index < len(values) and values[index] == last+1:
            last = values[index]
            index += 1
        result.append(str(first) if first == last else f'{first}-{last}')
    return ','.join(result)


def reserve(ports):
    requested = parse_ports(ports)
    # This directory is root-owned and created by systemd RuntimeDirectory.
    with os.fdopen(os.open(LOCK, os.O_CREAT | os.O_RDWR | os.O_NOFOLLOW, 0o600), 'a') as lock:
        fcntl.flock(lock, fcntl.LOCK_EX)
        existing = parse_ports(RESERVED.read_text())
        merged = existing | requested
        if merged != existing:
            RESERVED.write_text(format_ports(merged)+'\n')
        if not merged <= parse_ports(RESERVED.read_text()):
            raise ValueError('concurrent reservation change')


def handle(connection):
    connection.settimeout(5)
    try:
        _, uid, _ = struct.unpack('3i', connection.getsockopt(socket.SOL_SOCKET, socket.SO_PEERCRED, 12))
        if uid != 0 and uid != pwd.getpwnam('hl-edge').pw_uid:
            raise ValueError('unauthorized peer')
        with connection.makefile('rb') as incoming:
            raw = incoming.readline(MAX_REQUEST+1)
        if len(raw) > MAX_REQUEST or not raw.endswith(b'\n'):
            raise ValueError('invalid request size')
        value = json.loads(raw)
        if not isinstance(value, dict) or set(value) != {'ports'}:
            raise ValueError('invalid request')
        reserve(value['ports'])
        connection.sendall(b'{"ok":true}\n')
    except Exception:
        # Do not include arbitrary input, filesystem details or diagnostics.
        connection.sendall(b'{"ok":false}\n')


if __name__ == '__main__':
    with socket.socket(fileno=0) as connection:
        handle(connection)
