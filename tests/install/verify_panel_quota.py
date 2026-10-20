"""Run Store quota acceptance against a fresh, synthetic loopback PG cluster.

Requires an explicit local PostgreSQL bin directory. Never reads production
configuration. Logs are retained; the cluster is stopped in finally.
"""
import argparse
import ctypes
import json
import os
from pathlib import Path
import secrets
import socket
import subprocess
import tempfile
import time


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument('--pg-bin', type=Path, required=True)
    parser.add_argument('--go', default='C:/Program Files/Go/bin/go.exe')
    args = parser.parse_args()
    repo = Path(__file__).resolve().parents[2]
    run = Path(tempfile.gettempdir()) / ('hl-panel-quota-' + secrets.token_hex(8))
    run.mkdir()
    cluster = run / 'cluster'
    pg_bin = args.pg_bin.resolve()
    env = {k: v for k, v in os.environ.items()
           if not k.upper().startswith('PG') and not k.startswith('NYVP_TEST_POSTGRES')}
    env.update({'LC_ALL': 'C', 'LANG': 'C', 'PGCLIENTENCODING': 'UTF8'})
    report = {'scope': 'new empty loopback PostgreSQL; synthetic data only',
              'directory': str(run), 'success': False, 'stages': []}
    no_window = getattr(subprocess, 'CREATE_NO_WINDOW', 0)
    alias = None
    started = False
    print('verification_directory:', run, flush=True)

    def execute(stage, command, timeout=60, check=True):
        start = time.monotonic()
        with (run / (stage + '.stdout.log')).open('wb') as out, (run / (stage + '.stderr.log')).open('wb') as err:
            result = subprocess.run(list(map(str, command)), cwd=repo, env=env,
                                    stdin=subprocess.DEVNULL, stdout=out, stderr=err,
                                    creationflags=no_window, timeout=timeout)
        report['stages'].append({'stage': stage, 'exit_code': result.returncode,
                                 'seconds': round(time.monotonic() - start, 3)})
        print(stage, 'exit_code:', result.returncode, flush=True)
        if check and result.returncode:
            raise RuntimeError(stage + ' failed; inspect local logs')
        return result.returncode

    try:
        if os.name == 'nt':
            buffer = ctypes.create_unicode_buffer(32768)
            if ctypes.windll.kernel32.GetShortPathNameW(str(pg_bin), buffer, len(buffer)):
                pg_bin = Path(buffer.value)
            if not str(pg_bin).isascii():
                drives = ctypes.windll.kernel32.GetLogicalDrives()
                letter = next((x for x in 'ZYXWVUTSRQPONMLKJIHGF' if not drives & (1 << (ord(x)-ord('A')))), None)
                if not letter:
                    raise RuntimeError('no unused temporary PG runtime alias')
                alias = letter + ':'
                execute('alias', ['subst.exe', alias, args.pg_bin.resolve().parent])
                pg_bin = Path(alias + '/bin')
        env['PATH'] = str(pg_bin) + os.pathsep + env.get('PATH', '')
        executable = '.exe' if os.name == 'nt' else ''
        execute('initdb', [pg_bin/('initdb'+executable), '-D', cluster, '-U', 'quota_test', '-A', 'trust', '--no-locale', '--encoding=UTF8'])
        with socket.socket() as sock:
            sock.bind(('127.0.0.1', 0))
            port = sock.getsockname()[1]
        with (cluster/'postgresql.conf').open('a', encoding='utf-8') as out:
            out.write("\nlisten_addresses = '127.0.0.1'\nport = %d\n" % port)
        started = True
        execute('start', [pg_bin/('pg_ctl'+executable), '-D', cluster, '-l', run/'postgres.log', '-w', '-t', '30', 'start'])
        execute('createdb', [pg_bin/('createdb'+executable), '-h', '127.0.0.1', '-p', port, '-U', 'quota_test', 'nyvp_panel_quota_test'])
        env['NYVP_TEST_POSTGRES_URL'] = 'postgres://quota_test@127.0.0.1:%d/nyvp_panel_quota_test?sslmode=disable' % port
        env['NYVP_TEST_POSTGRES_ALLOW_MUTATION'] = 'true'
        execute('quota-tests', [args.go, 'test', '-v', '-count=1', '-timeout=120s', '-run=PanelQuota', './internal/control/memoryrepo', './internal/control/postgressnapshot'], timeout=150)
        report['success'] = True
    except Exception as error:
        report['failure'] = str(error)
    finally:
        if started:
            try:
                if execute('stop', [pg_bin/('pg_ctl'+executable), '-D', cluster, '-m', 'fast', '-w', '-t', '30', 'stop'], check=False):
                    report['success'] = False
            except Exception as error:
                report['stop_failure'] = str(error)
                report['success'] = False
        if alias:
            execute('alias-remove', ['subst.exe', alias, '/D'], check=False)
        (run/'report.json').write_text(json.dumps(report, indent=2), encoding='utf-8')
    print('success:', report['success'], flush=True)
    return 0 if report['success'] else 1


if __name__ == '__main__':
    raise SystemExit(main())
