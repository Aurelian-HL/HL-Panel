#!/usr/bin/env python3
"""Fixed-purpose privileged updater. Only a local, permissioned Unix socket."""
import importlib.util
import json
import os
from pathlib import Path
import re
import socket
import socketserver
import struct
import subprocess
import sys
import threading
from datetime import datetime, timezone
from http.server import BaseHTTPRequestHandler
import pwd

SOCKET = '/run/hl-panel-update.sock'
DIRECTORY = Path('/var/lib/hl-panel-update')
TASK_ID = re.compile(r'^[a-f0-9]{8}-[a-f0-9]{4}-[a-f0-9]{4}-[a-f0-9]{4}-[a-f0-9]{12}$')
TAG = re.compile(r'^v(0|[1-9][0-9]{0,8})\.(0|[1-9][0-9]{0,8})\.(0|[1-9][0-9]{0,8})$')
RAW = 'https://raw.githubusercontent.com/Aurelian-HL/HL-Panel/'
TERMINAL = {'succeeded', 'failed'}


def utc():
    return datetime.now(timezone.utc).isoformat()


def load_updater():
    source = Path('/opt/hl-panel/current/deploy/update.py')
    if source.is_symlink() or source.stat().st_uid != 0 or source.stat().st_mode & 0o022:
        raise RuntimeError('Untrusted updater')
    spec = importlib.util.spec_from_file_location('hl_panel_official_updater', source)
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


class TaskStore:
    def __init__(self, directory=DIRECTORY, execute=None):
        self.directory = directory
        directory.mkdir(mode=0o700, parents=True, exist_ok=True)
        info = directory.stat()
        if directory.is_symlink() or info.st_uid != os.geteuid() or info.st_mode & 0o077:
            raise RuntimeError('Task directory must be private')
        self.file = directory/'tasks.json'
        self.lock = threading.RLock()
        self.execute = execute or self.perform
        self.tasks = {}
        if self.file.exists():
            if self.file.is_symlink() or self.file.stat().st_uid != os.geteuid() or self.file.stat().st_mode & 0o077:
                raise RuntimeError('Untrusted task file')
            self.tasks = json.loads(self.file.read_text())
        for task in self.tasks.values():
            if task['state'] not in TERMINAL:
                task.update(state='failed', phase='interrupted', message='更新进程曾中断；请核对当前版本和备份后重试。', updated_at=utc())
        self.save()

    def save(self):
        pending = self.file.with_suffix('.pending')
        with os.fdopen(os.open(pending, os.O_WRONLY | os.O_CREAT | os.O_TRUNC | os.O_NOFOLLOW, 0o600), 'w') as stream:
            json.dump(self.tasks, stream, ensure_ascii=False)
            stream.flush()
            os.fsync(stream.fileno())
        pending.replace(self.file)
        descriptor = os.open(self.directory, os.O_RDONLY | os.O_DIRECTORY)
        try:
            os.fsync(descriptor)
        finally:
            os.close(descriptor)

    def status(self):
        with self.lock:
            current = next(reversed(self.tasks.values()), None)
            return {'available': True, 'task': dict(current) if current else None}

    def submit(self, identifier, version):
        if not isinstance(identifier, str) or not TASK_ID.fullmatch(identifier) or not isinstance(version, str) or not TAG.fullmatch(version):
            return 400, {'message': '更新版本或任务标识无效'}
        with self.lock:
            if identifier in self.tasks:
                if self.tasks[identifier]['target_version'] != version:
                    return 409, {'message': '同一任务标识不能用于其他版本'}
                return 202, {'available': True, 'task': dict(self.tasks[identifier])}
            if any(task['state'] not in TERMINAL for task in self.tasks.values()):
                return 409, {'message': '已有更新任务运行，请查看当前进度'}
            if len(self.tasks) >= 100:
                del self.tasks[next(iter(self.tasks))]
            self.tasks[identifier] = {'id': identifier, 'target_version': version, 'state': 'running', 'phase': 'queued',
                                      'message': '更新任务已提交', 'created_at': utc(), 'updated_at': utc()}
            self.save()
            result = {'available': True, 'task': dict(self.tasks[identifier])}
            threading.Thread(target=self.run, args=(identifier, version), daemon=True).start()
            return 202, result

    def change(self, identifier, **values):
        with self.lock:
            self.tasks[identifier].update(values, updated_at=utc())
            self.save()

    def run(self, identifier, version):
        try:
            self.execute(identifier, version)
        except Exception:
            self.change(identifier, state='failed', phase='failed', message='更新未完成；请查看当前版本。服务未恢复时使用本机备份中的回滚命令。')

    def perform(self, identifier, version):
        updater = load_updater()
        self.change(identifier, phase='download', message='正在验证正式版本并下载更新程序；面板继续运行')
        metadata = json.loads(updater.request(updater.API+'/releases/tags/'+version))
        updater.require(metadata.get('tag_name') == version and metadata.get('published_at') and not metadata.get('draft') and not metadata.get('prerelease'), 'Not an official release')
        source = updater.request(RAW+version+'/deploy/update.py', 512*1024)
        script = self.directory/(identifier+'.py')
        with os.fdopen(os.open(script, os.O_WRONLY | os.O_CREAT | os.O_EXCL | os.O_NOFOLLOW, 0o600), 'wb') as stream:
            stream.write(source)
        log = self.directory/(identifier+'.log')
        environment = {'PATH': '/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin', 'LANG': 'C.UTF-8', 'PYTHONUNBUFFERED': '1'}
        # The child survives API restarts. It receives no password, URL or shell text.
        with log.open('xb') as output:
            process = subprocess.Popen(['/usr/bin/python3', '-I', str(script), '--version', version], stdout=subprocess.PIPE,
                                       stderr=subprocess.STDOUT, env=environment, cwd='/opt/hl-panel')
            deadline = threading.Timer(1200, process.terminate)
            deadline.daemon = True
            deadline.start()
            try:
                for line in iter(process.stdout.readline, b''):
                    output.write(line)
                    output.flush()
                    text = line.decode('utf-8', errors='replace')
                    if '本次备份目录：' in text:
                        backup = text.split('本次备份目录：', 1)[1].strip()
                        if re.fullmatch(r'/var/backups/hl-panel/v[0-9.]+-[A-Za-z0-9-]+', backup):
                            self.change(identifier, backup_directory=backup)
                    if '下载同标签' in text:
                        self.change(identifier, phase='download', message='正在下载并校验官方发布包')
                    elif '暂停本面板' in text:
                        self.change(identifier, phase='install', message='正在备份数据库并安装；页面会短暂断开，请等待自动恢复')
                    elif '正在恢复升级前' in text:
                        self.change(identifier, phase='rollback', message='新版本检查未通过，正在恢复原程序和数据库')
                code = process.wait(timeout=30)
            finally:
                deadline.cancel()
                if process.poll() is None:
                    process.terminate()
                    try:
                        process.wait(timeout=15)
                    except subprocess.TimeoutExpired:
                        process.kill()
                        process.wait()
                process.stdout.close()
        if code:
            raise RuntimeError('Update failed')
        _, origin = updater.configuration()
        updater.healthy(origin, version)
        self.change(identifier, state='succeeded', phase='complete', message='更新成功，数据库、规则、账号和证书已保留；请刷新页面')


class Handler(BaseHTTPRequestHandler):
    def log_message(self, *_):
        pass  # Never log request bodies or privileged child output to the public journal.

    def setup(self):
        super().setup()
        self.connection.settimeout(10)

    def respond(self, code, data):
        body = json.dumps(data, ensure_ascii=False).encode()
        self.send_response(code)
        self.send_header('Content-Type', 'application/json')
        self.send_header('Cache-Control', 'no-store')
        self.send_header('Content-Length', str(len(body)))
        self.end_headers()
        self.wfile.write(body)

    def authorized(self):
        _, uid, _ = struct.unpack('3i', self.connection.getsockopt(socket.SOL_SOCKET, socket.SO_PEERCRED, 12))
        return uid == 0 or uid == pwd.getpwnam('hlpanel').pw_uid

    def do_GET(self):
        if not self.authorized():
            self.respond(403, {'message': 'Forbidden'})
        elif self.path == '/status':
            self.respond(200, self.server.store.status())
        else:
            self.respond(404, {'message': 'Not found'})

    def do_POST(self):
        if not self.authorized():
            self.respond(403, {'message': 'Forbidden'})
            return
        if self.path != '/start':
            self.respond(404, {'message': 'Not found'})
            return
        try:
            size = int(self.headers.get('Content-Length', '0'))
            if not 0 < size <= 256 or self.headers.get('Transfer-Encoding'):
                raise ValueError()
            body = json.loads(self.rfile.read(size))
            if not isinstance(body, dict) or set(body) != {'id', 'version'}:
                raise ValueError()
            code, data = self.server.store.submit(body['id'], body['version'])
        except (ValueError, TypeError, KeyError):
            code, data = 400, {'message': '无效更新请求'}
        self.respond(code, data)


class Server(socketserver.ThreadingMixIn, socketserver.UnixStreamServer):
    daemon_threads = True


def main():
    if os.geteuid() != 0 or os.environ.get('LISTEN_PID') != str(os.getpid()) or os.environ.get('LISTEN_FDS') != '1':
        raise RuntimeError('Must be started by the managed systemd Unix socket')
    os.umask(0o077)
    listener = socket.fromfd(3, socket.AF_UNIX, socket.SOCK_STREAM)
    if listener.getsockname() != SOCKET:
        raise RuntimeError('Unexpected listener')
    server = Server(SOCKET, Handler, bind_and_activate=False)
    server.socket.close()
    server.socket = listener
    server.store = TaskStore()
    server.serve_forever()


if __name__ == '__main__':
    main()
