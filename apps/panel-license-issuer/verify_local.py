"""Start a brand new empty loopback PG cluster and run synthetic issuer tests.

No production dumps, credentials, hosts, system service, or global PATH changes.
The cluster and logs are retained for review, and pg_ctl stops it in finally.
"""
import argparse
import ctypes
from datetime import datetime
import json
import os
from pathlib import Path
import socket
import secrets
import subprocess
import sys
import tempfile
import time


def short_path(path):
    buffer = ctypes.create_unicode_buffer(32768)
    length = ctypes.windll.kernel32.GetShortPathNameW(str(path), buffer, len(buffer))
    return buffer.value if length else str(path)


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument('--pg-bin', required=True, type=Path)
    parser.add_argument('--go', default='C:/Program Files/Go/bin/go.exe', type=Path)
    parser.add_argument('--race', action='store_true')
    parser.add_argument('--ht-verifier', type=Path)
    parser.add_argument('--panel-runtime', action='store_true',
                        help='also run actual control-api + HT handler lifecycle')
    parser.add_argument('--browser-marker', type=Path,
                        help='optional private ready/done marker for real runtime UI review')
    args = parser.parse_args()
    if args.panel_runtime and not args.ht_verifier:
        parser.error('--panel-runtime requires --ht-verifier from the delivered HT candidate')
    if args.browser_marker and (not args.panel_runtime or not args.browser_marker.is_absolute()
                                or args.browser_marker.exists()
                                or Path(str(args.browser_marker) + '.done').exists()):
        parser.error('browser marker must be a new absolute path with --panel-runtime')
    repo = Path(__file__).resolve().parents[2]
    # Python 3.13 mkdtemp's Windows OWNER RIGHTS ACL blocks PostgreSQL's
    # restricted child token. Inherit the ordinary temp-directory user ACL.
    run = Path(tempfile.gettempdir()) / ('hl-license-v2-' + secrets.token_hex(8))
    run.mkdir()
    cluster = run / 'cluster'
    pg_bin = Path(short_path(args.pg_bin.resolve()))
    env = {k: v for k, v in os.environ.items() if not k.upper().startswith('PG')
           and not k.startswith('HL_LICENSE_TEST_')}
    env.update({'LC_ALL': 'C', 'LANG': 'C', 'PGCLIENTENCODING': 'UTF8',
                'PATH': str(pg_bin) + os.pathsep + env.get('PATH', '')})
    no_window = getattr(subprocess, 'CREATE_NO_WINDOW', 0)
    report = {'started': datetime.now().astimezone().isoformat(), 'scope':
              'Fresh empty loopback PostgreSQL, synthetic data, fake DNS only',
              'directory': str(run), 'stages': [], 'success': False}
    print('verification_directory:', run, flush=True)

    def execute(stage, command, timeout=60, check=True):
        started = time.monotonic()
        with (run / (stage + '.stdout.log')).open('wb') as out, \
             (run / (stage + '.stderr.log')).open('wb') as err:
            process = subprocess.run(list(map(str, command)), cwd=repo, env=env,
                                     stdin=subprocess.DEVNULL, stdout=out, stderr=err,
                                     timeout=timeout, creationflags=no_window)
        report['stages'].append({'stage': stage, 'exit_code': process.returncode,
                                 'seconds': round(time.monotonic() - started, 3)})
        print(stage, 'exit_code:', process.returncode, flush=True)
        if process.returncode and check:
            raise RuntimeError(stage + ' failed; inspect local logs')
        return process.returncode

    start_attempted = False
    drive_alias = None
    try:
        if not str(pg_bin).isascii():
            # PostgreSQL on Windows passes its installation path through the
            # current ANSI code page during initdb. A temporary ASCII drive
            # alias fixes that without copying or changing the runtime.
            drives = ctypes.windll.kernel32.GetLogicalDrives()
            for letter in 'ZYXWVUTSRQPONMLKJIHGF':
                if not drives & (1 << (ord(letter) - ord('A'))):
                    drive_alias = letter + ':'
                    break
            if not drive_alias:
                raise RuntimeError('No unused drive letter for temporary PG runtime alias')
            execute('runtime-alias', ['subst.exe', drive_alias, args.pg_bin.resolve().parent])
            pg_bin = Path(drive_alias + '/bin')
            env['PATH'] = str(pg_bin) + os.pathsep + env['PATH']
        execute('initdb', [pg_bin / 'initdb.exe', '-D', cluster, '-U', 'license_test',
                           '-A', 'trust', '--no-locale', '--encoding=UTF8'])
        # No external bind or inherited PG service/connection configuration.
        with socket.socket() as sock:
            sock.bind(('127.0.0.1', 0))
            port = sock.getsockname()[1]
        with (cluster / 'postgresql.conf').open('a', encoding='utf-8') as config:
            config.write("\nlisten_addresses = '127.0.0.1'\nport = %d\n" % port)
        start_attempted = True
        execute('start', [pg_bin / 'pg_ctl.exe', '-D', cluster, '-l', run / 'postgres.log',
                          '-w', '-t', '30', 'start'])
        env['HL_LICENSE_TEST_DATABASE_URL'] = (
            'postgres://license_test@127.0.0.1:%d/postgres?sslmode=disable' % port)
        command = [args.go, 'test', '-count=1', '-v', '-timeout=120s']
        if args.race:
            command.append('-race')
        command.extend(['./internal/panellicenseissuer', './apps/panel-license-issuer'])
        execute('go-test', command, timeout=180)
        execute('go-vet', [args.go, 'vet', './internal/panellicenseissuer',
                           './apps/panel-license-issuer'], timeout=120)
        if args.ht_verifier:
            from verify_https import verify_ht
            report['ht_e2e'] = verify_ht(execute, run, env, args.go, repo,
                                          pg_bin, port, args.ht_verifier)
        if args.panel_runtime:
            env.update({'HL_LICENSE_PANEL_E2E': 'true',
                        'HL_LICENSE_PANEL_E2E_HT_ROOT': str(args.ht_verifier.resolve().parents[1]),
                        'HL_LICENSE_PANEL_E2E_PYTHON': str(Path(sys.executable).resolve())})
            if args.browser_marker:
                env['HL_LICENSE_PANEL_E2E_BROWSER_MARKER'] = str(args.browser_marker)
            execute('panel-runtime-e2e', [args.go, 'test', '-count=1', '-v', '-timeout=15m',
                                          '-run=^TestPanelLicenseRuntimeLifecycle$',
                                          './apps/control-api'], timeout=960)
            report['panel_runtime_e2e'] = {
                'passed': True, 'real_control_api_process': True,
                'real_postgresql': True, 'ht_handler_https': True,
                'synthetic_ht_session_and_dns': True,
                'production_access': False}
        report['success'] = True
    except Exception as error:
        report['failure'] = str(error)
    finally:
        if start_attempted:
            try:
                report['stop_exit_code'] = execute(
                    'stop', [pg_bin / 'pg_ctl.exe', '-D', cluster, '-m', 'fast',
                             '-w', '-t', '30', 'stop'], check=False)
                if report['stop_exit_code']:
                    report['success'] = False
            except Exception as error:
                report['stop_failure'] = str(error)
                report['success'] = False
        if drive_alias:
            try:
                report['alias_remove_exit_code'] = execute(
                    'runtime-alias-remove', ['subst.exe', drive_alias, '/D'], check=False)
            except Exception as error:
                report['alias_remove_failure'] = str(error)
                report['success'] = False
        report['finished'] = datetime.now().astimezone().isoformat()
        (run / 'report.json').write_text(json.dumps(report, indent=2, ensure_ascii=False),
                                         encoding='utf-8')
    print('success:', report['success'], flush=True)
    return 0 if report['success'] else 1


if __name__ == '__main__':
    raise SystemExit(main())
