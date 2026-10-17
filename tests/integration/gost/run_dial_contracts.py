#!/usr/bin/env python3
"""Run stacked abort and Dial contracts in private sources and caches.

Only loopback traffic is generated. Linux engine and WAN acceptance are separate.
The aggregate result is written only after both real contract suites succeed.
"""
from __future__ import annotations

import argparse
from collections import Counter
import hashlib
import json
import os
from pathlib import Path
import shutil
import subprocess
import time

from run_abort_contracts import run_contracts as run_abort_contracts
from udp_dial_overlay import dial_overlay, CANDIDATE_TEST_PATTERN


def sha256(path: Path) -> str:
    return hashlib.sha256(path.read_bytes()).hexdigest()


def expected_tests() -> set[str]:
    return {
        'TestDialCancellationPreservesSharedSession',
        'TestDialCancellationPreservesSharedSession/cancel',
        'TestDialCancellationPreservesSharedSession/deadline',
        'TestDialCancellationPreservesSharedSession/already-canceled',
        'TestDialDeadSharedSessionReconnects',
        'TestDialFailedOldSessionCannotEvictReplacement',
    }


def validate_events(log: Path, count: int) -> int:
    events = [json.loads(line) for line in log.read_text(encoding='utf-8-sig').splitlines()
              if line.startswith('{')]
    failed = [e for e in events if e.get('Action') in {'fail', 'skip'}]
    passed = Counter(e['Test'] for e in events if e.get('Action') == 'pass' and e.get('Test'))
    ran = Counter(e['Test'] for e in events if e.get('Action') == 'run' and e.get('Test'))
    packages = [e for e in events if e.get('Action') == 'pass' and not e.get('Test')]
    wanted = Counter({name: count for name in expected_tests()})
    if (failed or passed != wanted or ran != wanted or len(packages) != 1
            or any(e.get('Package') != 'github.com/go-gost/x/dialer/quic' for e in events)):
        raise RuntimeError('Dial contract test set/count/package mismatch, skip, or failure; see ' + str(log))
    return sum(passed.values())


def run_contracts(output: Path, local_x: Path, parent: dict, *, go: str = 'go',
                  count: int = 3, race: bool = True, env: dict | None = None,
                  seed_cache: Path | None = None) -> dict:
    if not 1 <= count <= 30:
        raise ValueError('count must be between 1 and 30')
    output, local_x = output.resolve(), local_x.resolve()
    if output.exists() and any(output.iterdir()):
        raise RuntimeError('output must be absent or empty')
    output.mkdir(parents=True, exist_ok=True)
    # Dial changes only dialer.go. Applying that independent mapping first lets
    # the existing abort runner retain it when retargeting its private x copy.
    dial = dial_overlay(output / 'dial-parent', local_x, parent)
    stacked = output / 'stacked-abort'
    try:
        abort_result = run_abort_contracts(stacked, local_x, dial, go=go, count=count,
                                         race=race, env=env, seed_cache=seed_cache)
    except Exception as error:
        # The unchanged parent runner writes commands and partial output before
        # raising. Point at that evidence without creating a success result.
        failure = {'phase': 'abort', 'error_type': type(error).__name__,
                   'error': str(error), 'commands': str(stacked / 'commands.json'),
                   'logs': [str(p) for p in sorted(stacked.glob('*.log')) +
                            sorted(stacked.glob('*.jsonl'))]}
        (output / 'failure.json').write_text(json.dumps(failure, indent=2) + '\n', encoding='utf-8')
        raise
    source = stacked / 'x-source'
    overlay = json.loads((stacked / 'contract-overlay/overlay.json').read_text(encoding='utf-8'))
    test_input = Path(__file__).resolve().parent / 'dial_contracts/dialer_cancel_contract_test.go'
    test_copy = output / 'dialer_cancel_contract_test.go'
    shutil.copyfile(test_input, test_copy)
    overlay['Replace'][str(source / 'dialer/quic/dialer_cancel_test.go')] = str(test_copy)
    overlay_path = output / 'dial-contract-overlay.json'
    overlay_path.write_text(json.dumps(overlay, indent=2) + '\n', encoding='utf-8', newline='\n')
    run_env = dict(os.environ if env is None else env)
    for key in ('GOFLAGS', 'GOWORK', 'GOOS', 'GOARCH', 'GOEXPERIMENT',
                'GOTMPDIR', 'GOENV', 'GOPATH', 'GOMODCACHE', 'GOCACHE'):
        run_env.pop(key, None)
    run_env.update({'GOMODCACHE': str(stacked / 'module-cache'),
                    'GOCACHE': str(stacked / 'build-cache'), 'GOPATH': str(stacked / 'go-path'),
                    'GOENV': 'off', 'GOWORK': 'off', 'GOTELEMETRY': 'off'})
    command = [go, 'test', '-mod=readonly', '-overlay', str(overlay_path),
               '-run', CANDIDATE_TEST_PATTERN, '-count=' + str(count), '-timeout=120s', '-json']
    if race:
        command.append('-race')
    command.append('./dialer/quic')
    started = time.monotonic()
    timeout_error = None
    try:
        completed = subprocess.run(command, cwd=source, env=run_env, stdout=subprocess.PIPE,
                                   stderr=subprocess.STDOUT, check=False, timeout=900)
        stdout, exit_code = completed.stdout, completed.returncode
    except subprocess.TimeoutExpired as error:
        stdout, exit_code, timeout_error = error.output or b'', None, error
    log = output / 'dial-results.jsonl'
    log.write_bytes(stdout)
    commands = [dict(row, suite='abort', cwd=str(stacked / 'contracts'),
                     log=str(stacked / row['log'])) for row in abort_result['commands']]
    commands.append({'arguments': command, 'cwd': str(source), 'exit_code': exit_code,
                     'timed_out': timeout_error is not None, 'timeout_seconds': 900,
                     'elapsed_seconds': round(time.monotonic() - started, 3),
                     'log': str(log), 'suite': 'dial'})
    (output / 'commands.json').write_text(json.dumps(commands, indent=2) + '\n', encoding='utf-8')
    if timeout_error is not None:
        raise timeout_error
    if exit_code:
        raise RuntimeError('command failed: see ' + str(log))
    passes = validate_events(log, count)
    result = {'candidate': 'dial', 'parent_candidate': 'abort', 'count': count, 'race': race,
              'abort': {'result': str(stacked / 'result.json'),
                        'passed_test_events': abort_result['passed_test_events']},
              'dial': {'expected_test_names': sorted(expected_tests()), 'passed_test_events': passes},
              'passed_test_events': abort_result['passed_test_events'] + passes,
              'dial_provenance': dial,
              'commands': commands, 'overlay': abort_result['overlay'],
              'candidate_overlay_entities': abort_result['candidate_overlay_entities'],
              'candidate_overlay_sha256': abort_result['candidate_overlay_sha256'],
              'dial_contract_overlay': str(overlay_path),
              'source_go_mod_sha256': sha256(local_x / 'go.mod'),
              'sources': [{'path': p.name, 'sha256': sha256(p)} for p in [
                  Path(__file__), Path(__file__).with_name('run_abort_contracts.py'),
                  Path(__file__).with_name('udp_dial_overlay.py'),
                  Path(__file__).with_name('udp_abort_overlay.py'), test_input]],
              'scope': 'stacked abort and Dial cancellation over loopback QUIC/TCP; inherited '
                       'smux mapping retained but not exercised; Linux GOST pair and WAN remain separate'}
    (output / 'result.json').write_text(json.dumps(result, indent=2) + '\n', encoding='utf-8')
    return result


if __name__ == '__main__':
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--source', type=Path, required=True)
    parser.add_argument('--parent', type=Path, required=True, help='drain provenance containing overlay.Replace')
    parser.add_argument('--output', type=Path, required=True)
    parser.add_argument('--go', default='go')
    parser.add_argument('--count', type=int, default=3)
    parser.add_argument('--no-race', action='store_true')
    parser.add_argument('--seed-cache', type=Path)
    args = parser.parse_args()
    if not 1 <= args.count <= 30:
        parser.error('count must be between 1 and 30')
    print(json.dumps(run_contracts(args.output, args.source,
        json.loads(args.parent.read_text(encoding='utf-8')), go=args.go, count=args.count,
        race=not args.no_race, seed_cache=args.seed_cache), indent=2))
