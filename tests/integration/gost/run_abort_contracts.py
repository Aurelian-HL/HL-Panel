#!/usr/bin/env python3
"""Run the actual GOST Pipe/QUIC cancellation contract in an isolated tree.

All outputs and Go caches stay under output. The source and parent overlay are
read-only; no production processes, repository files or host networking change.
Requires Go >= 1.26, Python >= 3.10, and a supported race detector toolchain.
"""
from __future__ import annotations

import argparse
from collections import Counter
import hashlib
import json
import os
import shutil
import subprocess
import time
from pathlib import Path

from udp_abort_overlay import abort_overlay, contract_overlay

EXPECTED_PACKAGE = 'github.com/go-gost/x/cleanupreview'


def expected_tests() -> set[str]:
    expected = {
        'TestActualOverlayContracts',
        'TestActualAbortThroughKnownWrapperAndComposite',
        'TestActualOpaqueWrapperHidesAbort',
        'TestActualAdapterGracefulCloseDoesNotResetAcceptedSend',
        'TestActualFinalReadBytesAndShortWrite',
        'TestActualAbortDeadlineCannotBeOverwrittenByHalfCloseGrace',
        'TestActualPipeCancelReleasesStreamCredit',
        'TestActualRelayWrapperCancelReleasesCredit',
        'TestActualCloseWriteErrorStopsOtherHalf',
        'TestActualAbortFailureFallbacks',
        'TestActualQUICCloseWriteErrorPropagates',
    }
    for adapter in ('listener', 'dialer', 'metrics-listener'):
        base = 'TestActualOverlayContracts/' + adapter
        expected.add(base)
        for name in ('external-cancel-blocked-read-write', 'first-error-stops-other-half',
                     'target-fin-delayed-reverse', 'client-fin-delayed-reply'):
            expected.add(base + '/' + name)
    for top in ('TestActualAbortThroughKnownWrapperAndComposite',
                'TestActualAdapterGracefulCloseDoesNotResetAcceptedSend',
                'TestActualPipeCancelReleasesStreamCredit'):
        for adapter in ('listener', 'dialer'):
            expected.add(top + '/' + adapter)
    for side in ('connector', 'handler'):
        for adapter in ('listener', 'dialer'):
            expected.add('TestActualRelayWrapperCancelReleasesCredit/' + side + '/' + adapter)
    for name in ('abort-error-deadline-ok', 'wrapped-unsupported-deadline-ok',
                 'deadline-error-close', 'abort-and-deadline-errors-close', 'close-error-retained'):
        expected.add('TestActualAbortFailureFallbacks/' + name)
    for adapter in ('listener', 'dialer'):
        expected.add('TestActualQUICCloseWriteErrorPropagates/' + adapter)
    return expected


def sha256(path: Path) -> str:
    return hashlib.sha256(path.read_bytes()).hexdigest()


def run_contracts(output: Path, local_x: Path, parent: dict, *,
                  go: str = 'go', count: int = 3, race: bool = True,
                  env: dict | None = None, seed_cache: Path | None = None,
                  contracts: Path | None = None) -> dict:
    output, local_x = output.resolve(), local_x.resolve()
    if output.exists() and any(output.iterdir()):
        raise RuntimeError('output must be absent or empty')
    output.mkdir(parents=True, exist_ok=True)
    source = output / 'x-source'
    shutil.copytree(local_x, source)
    for path in source.rglob('*'):
        if path.is_file():
            path.chmod(0o644)
    # Go forbids overlays replacing files in GOMODCACHE. Retarget only x paths
    # to the private source copy; preserve inherited smux/drain mappings.
    inherited = {}
    for original, replacement in parent['overlay']['Replace'].items():
        original_path = Path(original).resolve()
        try:
            original_path = source / original_path.relative_to(local_x)
        except ValueError:
            pass
        inherited[str(original_path)] = str(Path(replacement).resolve())
    parent = dict(parent, overlay={'Replace': inherited})
    candidate = abort_overlay(output / 'abort', source, parent)
    contract_overlay(output / 'contract-overlay', source, candidate)
    test_source = contracts or (Path(__file__).resolve().parent / 'abort_contracts')
    tests = output / 'contracts'
    tests.mkdir()
    files = sorted(test_source.glob('*.go'))
    if not files:
        raise RuntimeError('no Go contract sources found')
    for path in files:
        shutil.copyfile(path, tests / path.name)
    go_mod = (source / 'go.mod').read_text(encoding='utf-8')
    replacements = [line for line in go_mod.splitlines() if line.startswith('replace ')]
    allowed_replacement = 'replace github.com/ugorji/go v1.1.4 => github.com/ugorji/go/codec v1.2.12'
    if go_mod.count('module github.com/go-gost/x\n') != 1 or replacements != [allowed_replacement]:
        raise RuntimeError('unexpected pinned upstream go.mod')
    go_mod = go_mod.replace('module github.com/go-gost/x\n',
                            'module github.com/go-gost/x/cleanupreview\n', 1)
    go_mod += '\nrequire github.com/go-gost/x v0.17.2\n'
    go_mod += 'replace github.com/go-gost/x => ../x-source\n'
    (tests / 'go.mod').write_text(go_mod, encoding='utf-8', newline='\n')
    shutil.copyfile(source / 'go.sum', tests / 'go.sum')
    run_env = dict(os.environ if env is None else env)
    for key in ('GOFLAGS', 'GOWORK', 'GOOS', 'GOARCH', 'GOEXPERIMENT',
                'GOTMPDIR', 'GOENV', 'GOPATH', 'GOMODCACHE', 'GOCACHE'):
        run_env.pop(key, None)
    module_cache = output / 'module-cache'
    if seed_cache:
        # Seed verified module archives only. Extracted module trees can exceed
        # Windows path limits under a longer output path; Go handles extraction.
        shutil.copytree(seed_cache / 'cache' / 'download',
                        module_cache / 'cache' / 'download')
    run_env.update({'GOMODCACHE': str(module_cache),
                    'GOCACHE': str(output / 'build-cache'),
                    'GOPATH': str(output / 'go-path'),
                    'GOENV': 'off', 'GOWORK': 'off'})
    commands = []
    def execute(arguments, log, timeout=900):
        started = time.monotonic()
        timeout_error = None
        try:
            result = subprocess.run(arguments, cwd=tests, env=run_env,
                                    stdout=subprocess.PIPE, stderr=subprocess.STDOUT,
                                    check=False, timeout=timeout)
            stdout, exit_code = result.stdout, result.returncode
        except subprocess.TimeoutExpired as error:
            stdout, exit_code, timeout_error = error.output or b'', None, error
        (output / log).write_bytes(stdout)
        commands.append({'arguments': arguments, 'exit_code': exit_code,
                         'timed_out': timeout_error is not None, 'timeout_seconds': timeout,
                         'elapsed_seconds': round(time.monotonic()-started, 3),
                         'log': log})
        (output / 'commands.json').write_text(json.dumps(commands, indent=2)+'\n', encoding='utf-8')
        if timeout_error is not None:
            raise timeout_error
        if exit_code:
            raise RuntimeError('command failed: see ' + str(output / log))
    execute([go, 'mod', 'tidy'], 'go-mod-tidy.log')
    command = [go, 'test', '-mod=readonly', '-overlay',
               str(output / 'contract-overlay/overlay.json'),
               '-run', '^TestActual', '-count=' + str(count), '-timeout=120s', '-json']
    if race:
        command.append('-race')
    execute(command + ['./...'], 'race-results.jsonl')
    events = [json.loads(line) for line in (output / 'race-results.jsonl').read_text(encoding='utf-8-sig').splitlines()
              if line.startswith('{')]
    if any(event.get('Package') != EXPECTED_PACKAGE for event in events):
        raise RuntimeError('contract test package mismatch; see race-results.jsonl')
    failed = [event for event in events if event.get('Action') in {'fail', 'skip'}]
    passed = Counter(event['Test'] for event in events
                     if event.get('Action') == 'pass' and event.get('Test'))
    ran = Counter(event['Test'] for event in events
                  if event.get('Action') == 'run' and event.get('Test'))
    wanted = Counter({name: count for name in expected_tests()})
    package_passes = [event for event in events if event.get('Action') == 'pass' and not event.get('Test')]
    if failed or passed != wanted or ran != wanted or len(package_passes) != 1:
        raise RuntimeError('contract test set/count mismatch, skip, or failure; see race-results.jsonl')
    overlay_entities = []
    for original, effective in sorted(candidate['overlay']['Replace'].items()):
        overlay_entities.append({'original': original, 'effective': effective,
                                 'candidate_sha256': sha256(Path(effective))})
    result = {'candidate': 'abort', 'count': count, 'race': race,
              'expected_test_names': sorted(expected_tests()),
              'passed_test_events': sum(passed.values()), 'commands': commands,
              'overlay': str(output / 'abort/overlay.json'),
              'contract_overlay': str(output / 'contract-overlay/overlay.json'),
              'source_go_mod_sha256': sha256(local_x / 'go.mod'),
              'runner_sha256': sha256(Path(__file__)),
              'helper_sha256': sha256(Path(__file__).with_name('udp_abort_overlay.py')),
              'candidate_overlay_sha256': sha256(output / 'abort/overlay.json'),
              'candidate_overlay_entities': overlay_entities,
              'contract_sources': [{'path': p.name, 'sha256': hashlib.sha256(p.read_bytes()).hexdigest()} for p in files],
              'scope': 'actual upstream Pipe and QUIC adapters over localhost UDP/TCP; inherited smux mapping retained but not exercised by this QUIC-only contract; full Linux GOST pair remains separate'}
    (output / 'result.json').write_text(json.dumps(result, indent=2)+'\n', encoding='utf-8')
    return result


if __name__ == '__main__':
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--source', type=Path, required=True)
    parser.add_argument('--parent', type=Path, required=True, help='parent provenance JSON containing overlay.Replace')
    parser.add_argument('--output', type=Path, required=True)
    parser.add_argument('--go', default='go')
    parser.add_argument('--count', type=int, default=3)
    parser.add_argument('--no-race', action='store_true')
    parser.add_argument('--seed-cache', type=Path)
    parser.add_argument('--contracts', type=Path)
    args = parser.parse_args()
    if not 1 <= args.count <= 30:
        parser.error('count must be between 1 and 30')
    print(json.dumps(run_contracts(args.output, args.source,
        json.loads(args.parent.read_text(encoding='utf-8')), go=args.go,
        count=args.count, race=not args.no_race, seed_cache=args.seed_cache,
        contracts=args.contracts), indent=2))
