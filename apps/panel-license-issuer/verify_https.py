"""Isolated HTTPS bridge used only by verify_local.py, never by the product."""
import base64
from datetime import datetime, timedelta, timezone
import http.client
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
import ipaddress
import os
from pathlib import Path
import secrets
import socket
import ssl
import subprocess
import sys
import threading
import time


def unused_port():
    with socket.socket() as sock:
        sock.bind(('127.0.0.1', 0))
        return sock.getsockname()[1]


def verify_ht(execute, run, env, go, repo, pg_bin, pg_port, script):
    from cryptography import x509
    from cryptography.hazmat.primitives import hashes, serialization
    from cryptography.hazmat.primitives.asymmetric import rsa
    from cryptography.x509.oid import NameOID

    # A one-hour localhost certificate is trusted only by the subprocess test.
    tls_key = rsa.generate_private_key(public_exponent=65537, key_size=2048)
    subject = x509.Name([x509.NameAttribute(NameOID.COMMON_NAME, 'HL local test CA')])
    now = datetime.now(timezone.utc)
    certificate = (x509.CertificateBuilder().subject_name(subject).issuer_name(subject)
                   .public_key(tls_key.public_key()).serial_number(x509.random_serial_number())
                   .not_valid_before(now - timedelta(minutes=1))
                   .not_valid_after(now + timedelta(hours=1))
                   .add_extension(x509.BasicConstraints(ca=True, path_length=0), critical=True)
                   .add_extension(x509.SubjectAlternativeName([
                       x509.IPAddress(ipaddress.ip_address('127.0.0.1'))]), critical=False)
                   .sign(tls_key, hashes.SHA256()))
    ca_file = run / 'test-ca.pem'
    key_file = run / 'test-tls-key.pem'
    ca_file.write_bytes(certificate.public_bytes(serialization.Encoding.PEM))
    key_file.write_bytes(tls_key.private_bytes(serialization.Encoding.PEM,
                                             serialization.PrivateFormat.PKCS8,
                                             serialization.NoEncryption()))
    secret_dir = run / 'synthetic-secrets'
    secret_dir.mkdir(mode=0o700)
    token_file = secret_dir / 'management-token'
    signing_file = secret_dir / 'signing-key'
    dsn_file = secret_dir / 'database-url'
    token_file.write_text(secrets.token_urlsafe(48), encoding='ascii')
    signing_file.write_text(base64.urlsafe_b64encode(secrets.token_bytes(32)).decode().rstrip('='),
                            encoding='ascii')
    dsn_file.write_text('postgres://license_test@127.0.0.1:%d/issuer_fixture?sslmode=disable' % pg_port,
                        encoding='ascii')
    execute('fixture-db', [pg_bin / 'psql.exe', '-X', '--no-password', '-h', '127.0.0.1',
                           '-p', pg_port, '-U', 'license_test', '-d', 'postgres',
                           '-v', 'ON_ERROR_STOP=1', '-c', 'CREATE DATABASE issuer_fixture'])
    binary = run / 'panel-license-issuer.exe'
    execute('issuer-build', [go, 'build', '-o', binary, './apps/panel-license-issuer'], timeout=120)
    issuer_port = unused_port()
    issuer_env = dict(env)
    issuer_env.update({
        'HL_LICENSE_ISSUER_DATABASE_URL_FILE': str(dsn_file),
        'HL_LICENSE_ISSUER_ADMIN_TOKEN_FILE': str(token_file),
        'HL_LICENSE_ISSUER_SIGNING_KEY_FILE': str(signing_file),
        'HL_LICENSE_ISSUER_ID': 'https://issuer.example.test',
        'HL_LICENSE_ISSUER_KEY_ID': 'local-test-key',
        'HL_LICENSE_ISSUER_LISTEN': '127.0.0.1:%d' % issuer_port,
    })
    server = None
    server_thread = None
    with (run / 'issuer.stdout.log').open('wb') as out, \
         (run / 'issuer.stderr.log').open('wb') as err:
        process = subprocess.Popen([str(binary)], cwd=repo, env=issuer_env,
                                   stdin=subprocess.DEVNULL, stdout=out, stderr=err,
                                   creationflags=getattr(subprocess, 'CREATE_NO_WINDOW', 0))
        try:
            ready = False
            for _ in range(100):
                if process.poll() is not None:
                    raise RuntimeError('isolated issuer process exited before readiness')
                try:
                    with socket.create_connection(('127.0.0.1', issuer_port), timeout=.2):
                        ready = True
                        break
                except OSError:
                    time.sleep(.05)
            if not ready:
                raise RuntimeError('isolated issuer readiness timed out')

            class Bridge(BaseHTTPRequestHandler):
                def log_message(self, *args):
                    pass

                def forward(self):
                    length = int(self.headers.get('Content-Length', '0'))
                    if length < 0 or length > 65536:
                        self.send_error(413)
                        return
                    body = self.rfile.read(length)
                    headers = {key: value for key, value in self.headers.items()
                               if key.lower() not in ('host', 'connection')}
                    upstream = http.client.HTTPConnection('127.0.0.1', issuer_port, timeout=12)
                    try:
                        upstream.request(self.command, self.path, body, headers)
                        response = upstream.getresponse()
                        content = response.read(1048577)
                        self.send_response(response.status)
                        for key, value in response.getheaders():
                            if key.lower() not in ('content-length', 'transfer-encoding', 'connection'):
                                self.send_header(key, value)
                        self.send_header('Content-Length', str(len(content)))
                        self.end_headers()
                        self.wfile.write(content)
                    finally:
                        upstream.close()

                do_GET = forward
                do_POST = forward

            server = ThreadingHTTPServer(('127.0.0.1', 0), Bridge)
            context = ssl.SSLContext(ssl.PROTOCOL_TLS_SERVER)
            context.load_cert_chain(ca_file, key_file)
            server.socket = context.wrap_socket(server.socket, server_side=True)
            server_thread = threading.Thread(target=server.serve_forever, daemon=True)
            server_thread.start()
            execute('ht-issuer-e2e', [sys.executable, Path(script).resolve(),
                                     '--endpoint', 'https://127.0.0.1:%d' % server.server_port,
                                     '--ca-file', ca_file, '--token-file', token_file], timeout=60)
            return {'transport': 'real HTTPS with temporary CA and SAN 127.0.0.1',
                    'issuer': 'built Go executable and fresh PostgreSQL fixture DB',
                    'passed': True}
        finally:
            if server:
                server.shutdown()
                server.server_close()
            if server_thread:
                server_thread.join(timeout=5)
            if process.poll() is None:
                process.terminate()
                process.wait(timeout=10)
