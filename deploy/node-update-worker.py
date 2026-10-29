#!/usr/bin/env python3
"""Root-only fixed-purpose node updater, reachable only through a local socket."""
import http.server
import fcntl
import json
import os
from pathlib import Path
import pwd
import re
import shutil
import socket
import struct
import subprocess
import sys
import threading
import time

ROOT = Path('/opt/hl-panel')
STATE = Path('/var/lib/hl-panel-node-update')
UUID = re.compile(r'[a-f0-9]{8}-(?:[a-f0-9]{4}-){3}[a-f0-9]{12}')
TAG = re.compile(r'v(?:0|[1-9][0-9]*)\.(?:0|[1-9][0-9]*)\.(?:0|[1-9][0-9]*)')
SHA = re.compile(r'[a-f0-9]{64}')
# Accept the old receipt during an upgrade from an already-running worker.
# New installers stage replacements privately next to the installed programs.
BACKUP = re.compile(r'(?:/var/backups/hl-panel-node/update-v|/opt/hl-panel/\.node-update-v)[0-9]+\.[0-9]+\.[0-9]+-[A-Za-z0-9_-]+')
LOCK = threading.RLock()
ACTIVE = {'running', 'verifying', 'uncertain'}
EXECUTING = False


def save(task):
    temporary = STATE / 'task.tmp'
    fd = os.open(temporary, os.O_WRONLY | os.O_CREAT | os.O_TRUNC | os.O_NOFOLLOW, 0o600)
    with os.fdopen(fd, 'w') as out:
        json.dump(task, out)
        out.flush()
        os.fsync(out.fileno())
    os.replace(temporary, STATE / 'task.json')
    fd = os.open(STATE, os.O_RDONLY | os.O_DIRECTORY)
    try:
        os.fsync(fd)
    finally:
        os.close(fd)


def load():
    path = STATE / 'task.json'
    if not path.exists():
        return None
    if path.is_symlink() or path.stat().st_uid != 0 or path.stat().st_mode & 0o077:
        raise ValueError('unsafe state')
    return json.loads(path.read_text())


def valid_request(value):
    return (isinstance(value, dict) and set(value) == {'id', 'version', 'archive_sha256'} and
            all(isinstance(v, str) for v in value.values()) and UUID.fullmatch(value['id']) and
            TAG.fullmatch(value['version']) and SHA.fullmatch(value['archive_sha256']))


def installation_completed(task):
    # Same-version repair cannot be proved by a matching old binary alone.
    report = STATE / task['id'] / 'result.json'
    if not report.is_file() or report.is_symlink() or report.stat().st_uid != 0 or report.stat().st_mode & 0o077:
        return False
    value = json.loads(report.read_text())
    return (value.get('state') == 'verifying' and BACKUP.fullmatch(task.get('backup_directory', '')) and
            value.get('backup_directory') == task['backup_directory'])


def transition(task, state, phase, backup=''):
    with LOCK:
        task.update(state=state, phase=phase, backup_directory=backup or task.get('backup_directory', ''))
        save(task)


def cleanup_completed(task):
    if task.get('state') not in ('succeeded', 'rolled_back'):
        return
    value = task.get('backup_directory', '')
    if not value:
        return
    if not BACKUP.fullmatch(value):
        raise ValueError('invalid transaction')
    path = Path(value)
    if path.is_symlink():
        raise ValueError('unsafe transaction')
    if path.exists():
        for directory in (path.parent, path):
            info = directory.stat()
            if directory.is_symlink() or info.st_uid != 0 or info.st_mode & 0o022:
                raise ValueError('unsafe transaction')
        if path.stat().st_mode & 0o077:
            raise ValueError('unsafe transaction')
        # A restarted worker may receive /verified while the surviving updater
        # still reads release assets. Never delete an in-use transaction.
        fd = os.open(ROOT/'.node-update.lock', os.O_WRONLY|os.O_CREAT|os.O_NOFOLLOW, 0o600)
        with os.fdopen(fd, 'a') as lock:
            fcntl.flock(lock, fcntl.LOCK_EX|fcntl.LOCK_NB)
            shutil.rmtree(path)
    task['backup_directory'] = ''  # Kept only as a legacy wire field.
    save(task)


def finish(task, state, phase, backup=''):
    transition(task, state, phase, backup)
    try:
        cleanup_completed(task)
    except (OSError, ValueError):
        # Cleanup cannot turn a proven successful upgrade into an uncertain one,
        # or make a malformed directory eligible for recursive deletion.
        print('Node update completed; temporary-file cleanup deferred.', file=sys.stderr)


def retry_cleanup(task):
    # /status is already polled by the Agent. Retry a deferred cleanup there,
    # without a new background poller or losing the durable transaction path.
    if task and task.get('state') in ('succeeded', 'rolled_back') and task.get('backup_directory'):
        try:
            cleanup_completed(task)
        except (OSError, ValueError):
            pass


def execute(task):
    global EXECUTING
    # This thread and root service are independent of the edge-agent process.
    job = STATE / task['id']
    result = job / 'result.json'
    try:
        job.mkdir(mode=0o700, exist_ok=False)
        with (job / 'private.log').open('wb') as log:
            process = subprocess.run([sys.executable, str(ROOT / 'update-node.py'),
                '--version', task['version'], '--expected-sha256', task['archive_sha256'],
                '--result-file', str(result)], stdout=log, stderr=log, timeout=1800,
                env=dict(os.environ, HL_NODE_TRANSIENT_UPDATE='1'))
        report = json.loads(result.read_text()) if result.is_file() else {}
        backup = report.get('backup_directory', '')
        if backup and not BACKUP.fullmatch(backup):
            raise ValueError('invalid backup')
        if process.returncode:
            state = report.get('state', 'uncertain')
            if state not in ('failed', 'rolled_back', 'uncertain'):
                state = 'uncertain'
            finish(task, state, 'update_failed', backup)
            return
        if report.get('state') != 'verifying' or not backup:
            raise ValueError('missing update result')
        transition(task, 'verifying', 'verify', backup)
        # A new agent must prove its version, current configuration and listeners.
        deadline = time.monotonic() + 180
        while time.monotonic() < deadline:
            with LOCK:
                if load().get('state') == 'succeeded':
                    return
            time.sleep(1)
        with LOCK:
            if load().get('state') == 'succeeded':
                return
            transition(task, 'running', 'rollback', backup)
        with (job / 'private.log').open('ab') as log:
            restored = subprocess.run([sys.executable, str(Path(backup) / 'rollback-runner.py'),
                '--rollback', backup], stdout=log, stderr=log, timeout=180)
        finish(task, 'rolled_back' if restored.returncode == 0 else 'uncertain', 'rollback', backup)
    except Exception:
        # A timeout can occur during switching. Never assume the old program won.
        transition(task, 'uncertain', 'interrupted')
    finally:
        with LOCK:
            EXECUTING = False


def recover(task):
    global EXECUTING
    try:
        backup = task['backup_directory']
        if not BACKUP.fullmatch(backup):
            raise ValueError('missing backup')
        with (STATE / task['id'] / 'private.log').open('ab') as log:
            restored = subprocess.run([sys.executable, str(Path(backup) / 'rollback-runner.py'),
                '--rollback', backup], stdout=log, stderr=log, timeout=180)
        finish(task, 'rolled_back' if restored.returncode == 0 else 'uncertain', 'rollback', backup)
    except Exception:
        transition(task, 'uncertain', 'interrupted')
    finally:
        with LOCK:
            EXECUTING = False


class Handler(http.server.BaseHTTPRequestHandler):
    def setup(self):
        self.request.settimeout(5)
        super().setup()

    def log_message(self, *_):
        pass

    def respond(self, code, body):
        data = json.dumps(body).encode()
        self.send_response(code)
        self.send_header('Content-Type', 'application/json')
        self.send_header('Content-Length', str(len(data)))
        self.end_headers()
        self.wfile.write(data)

    def do_GET(self):
        self.handle_request(None)

    def do_POST(self):
        try:
            size = int(self.headers.get('Content-Length', '0'))
            if size <= 0 or size > 2048:
                raise ValueError()
            value = json.loads(self.rfile.read(size))
        except Exception:
            self.respond(400, {'error': 'invalid request'})
            return
        self.handle_request(value)

    def handle_request(self, value):
        global EXECUTING
        try:
            _, uid, _ = struct.unpack('3i', self.connection.getsockopt(socket.SOL_SOCKET, socket.SO_PEERCRED, 12))
            if uid not in (0, pwd.getpwnam('hl-edge').pw_uid):
                self.respond(403, {})
                return
            with LOCK:
                task = load()
                retry_cleanup(task)
                if self.command == 'GET' and self.path == '/status':
                    self.respond(200, {'capability': 'node-update-v1', 'task': task})
                elif self.command == 'POST' and self.path == '/start' and valid_request(value):
                    if task and task['id'] == value['id']:
                        if any(task[k] != value[k] for k in value):
                            self.respond(409, {})
                            return
                    elif (task and task['state'] in ACTIVE) or EXECUTING:
                        self.respond(409, {})
                        return
                    else:
                        # A compromised unprivileged process cannot install an arbitrary binary.
                        current = subprocess.run([str(ROOT / 'edge-agent'), '--version'], capture_output=True, timeout=5)
                        installed = current.stdout.decode().strip()
                        if current.returncode or not TAG.fullmatch(installed) or tuple(map(int, value['version'][1:].split('.'))) < tuple(map(int, installed[1:].split('.'))):
                            self.respond(409, {})
                            return
                        task = dict(value, state='running', phase='download', backup_directory='')
                        save(task)
                        EXECUTING = True
                        threading.Thread(target=execute, args=(dict(task),), daemon=False).start()
                    self.respond(200, {'task': task})
                elif self.command == 'POST' and self.path == '/verified' and isinstance(value, dict) and set(value) == {'id'}:
                    if task and task['id'] == value['id'] and task['state'] == 'succeeded':
                        self.respond(200, {'task': task})
                        return
                    if not task or task['id'] != value['id'] or task['state'] not in ('verifying', 'uncertain', 'succeeded') or task['phase'] == 'rollback' or not installation_completed(task):
                        self.respond(409, {})
                        return
                    version = subprocess.run([str(ROOT / 'edge-agent'), '--version'], capture_output=True, timeout=5)
                    alive = subprocess.run(['systemctl', 'is-active', '--quiet', 'hl-panel-edge-agent.service'], capture_output=True, timeout=5)
                    if version.returncode or task['version'] not in version.stdout.decode().split() or alive.returncode:
                        self.respond(409, {})
                        return
                    finish(task, 'succeeded', 'verified')
                    self.respond(200, {'task': task})
                elif self.command == 'POST' and self.path == '/rollback' and uid == 0 and isinstance(value, dict) and set(value) == {'id'}:
                    if EXECUTING or not task or task['id'] != value['id'] or task['state'] != 'uncertain' or not BACKUP.fullmatch(task.get('backup_directory', '')):
                        self.respond(409, {})
                        return
                    transition(task, 'uncertain', 'rollback')
                    EXECUTING = True
                    threading.Thread(target=recover, args=(dict(task),), daemon=False).start()
                    self.respond(200, {'task': task})
                else:
                    self.respond(400, {})
        except Exception:
            self.respond(503, {})


class Server(http.server.HTTPServer):
    address_family = socket.AF_UNIX


def main():
    if os.geteuid() != 0 or os.environ.get('LISTEN_FDS') != '1' or os.environ.get('LISTEN_PID') != str(os.getpid()):
        raise SystemExit('socket activation required')
    STATE.mkdir(mode=0o700, exist_ok=True)
    if STATE.is_symlink() or STATE.stat().st_uid != 0 or STATE.stat().st_mode & 0o077:
        raise SystemExit('unsafe directory')
    with LOCK:
        old = load()
        if old and old['state'] in ('succeeded', 'rolled_back'):
            try:
                cleanup_completed(old)
            except (OSError, ValueError):
                print('Temporary-file cleanup deferred; check node update logs.', file=sys.stderr)
        if old and old['state'] in ACTIVE:
            # Keep the child's durable backup reference if the worker died
            # before its in-memory task reached the verification stage.
            report = STATE / old['id'] / 'result.json'
            if report.is_file() and not report.is_symlink() and report.stat().st_uid == 0:
                backup = json.loads(report.read_text()).get('backup_directory', '')
                if BACKUP.fullmatch(backup):
                    old['backup_directory'] = backup
            transition(old, 'uncertain', 'rollback' if old.get('phase') == 'rollback' else 'interrupted')
    server = Server('/run/hl-panel-node-update.sock', Handler, bind_and_activate=False)
    server.socket.close()
    server.socket = socket.socket(fileno=3)
    server.serve_forever()


if __name__ == '__main__':
    main()
