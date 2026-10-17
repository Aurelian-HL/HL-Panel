#!/usr/bin/env python3
"""Run pinned KCP timeout contracts in private source/module/build trees.

The optional smux source is copied and inherited mappings are retained. Only
the real KCP Init/config paths are exercised; process replacement, loopback
delivery and WAN/performance acceptance remain separate.
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

from udp_timeout_overlay import build as timeout_overlay, CANDIDATE_TEST_PATTERN


PACKAGES = ('github.com/go-gost/x/listener/kcp', 'github.com/go-gost/x/dialer/kcp')


def sha256(path: Path) -> str:
    return hashlib.sha256(path.read_bytes()).hexdigest()


def manifest(root: Path) -> dict:
    return {str(path.relative_to(root)): sha256(path)
            for path in sorted(root.rglob('*')) if path.is_file()}


def expected_tests() -> set[str]:
    expected = {'TestKCPTimeoutEffectiveConfig', 'TestKCPTimeoutInvalidRejectedByInit',
                'TestKCPTimeoutInstanceIsolationAndReset'}
    expected.update('TestKCPTimeoutEffectiveConfig/' + name for name in (
        'default', 'explicit', 'integer', 'string_integer', 'equal', 'fractional',
        'timeout_absent', 'negative_interval', 'zero_interval', 'upper_case'))
    expected.update('TestKCPTimeoutInvalidRejectedByInit/' + name for name in (
        'nil', 'empty', 'malformed', 'zero', 'negative', 'bool', 'float', 'int64',
        'duration_overflow', 'seconds_overflow', 'integer_parse_overflow',
        'below_explicit_interval', 'below_default_interval', 'below_negative_fallback',
        'nonpositive_string', 'integer_seconds_overflow'))
    return expected


def validate_events(log: Path, count: int) -> int:
    events = [json.loads(line) for line in log.read_text(encoding='utf-8-sig').splitlines()
              if line.startswith('{')]
    wanted = Counter({(package, name): count for package in PACKAGES for name in expected_tests()})
    passed = Counter((event['Package'], event['Test']) for event in events
                     if event.get('Action') == 'pass' and event.get('Test'))
    ran = Counter((event['Package'], event['Test']) for event in events
                  if event.get('Action') == 'run' and event.get('Test'))
    package_passes = Counter(event.get('Package') for event in events
                             if event.get('Action') == 'pass' and not event.get('Test'))
    if (passed != wanted or ran != wanted or package_passes != Counter(PACKAGES)
            or any(event.get('Package') not in PACKAGES for event in events)
            or any(event.get('Action') in {'skip', 'fail'} for event in events)):
        raise RuntimeError('timeout contract test set/count/package mismatch, skip, or failure; see ' + str(log))
    return sum(passed.values())


def run_contracts(output: Path, local_x: Path, parent: dict, *, local_smux: Path | None = None,
                  go: str = 'go', count: int = 3, race: bool = True,
                  env: dict | None = None, seed_cache: Path | None = None) -> dict:
    if not 1 <= count <= 30:
        raise ValueError('count must be between 1 and 30')
    output, local_x = Path(output).resolve(), Path(local_x).resolve()
    local_smux = Path(local_smux).resolve() if local_smux else None
    inputs = [local_x] + ([local_smux] if local_smux else [])
    if any(output == source or source in output.parents for source in inputs):
        raise ValueError('output must be outside the read-only source trees')
    if output.exists() and (not output.is_dir() or any(output.iterdir())):
        raise ValueError('output must be absent or empty')
    if (local_x / 'go.mod').read_text().splitlines()[0] != 'module github.com/go-gost/x':
        raise ValueError('unexpected x source module')
    if local_smux and (local_smux / 'go.mod').read_text().splitlines()[0] != 'module github.com/xtaci/smux':
        raise ValueError('unexpected smux source module')
    source_manifests = {str(source): manifest(source) for source in inputs}
    mappings = parent['overlay']['Replace']
    if any(Path(name).name.endswith('_test.go') for name in mappings):
        raise ValueError('parent must be the binary overlay without contract mappings')
    output.mkdir(parents=True, exist_ok=True)
    copies = {local_x: output / 'x-source'}
    if local_smux:
        copies[local_smux] = output / 'smux-source'
    for source, destination in copies.items():
        shutil.copytree(source, destination)
        for path in destination.rglob('*'):
            if path.is_file():
                path.chmod(0o644)
    inherited, parent_inputs = {}, []
    parent_dir = output / 'parent-inputs'
    parent_dir.mkdir()
    for index, (original, replacement) in enumerate(sorted(mappings.items())):
        original_path = Path(original).resolve()
        destinations = [destination / original_path.relative_to(source)
                        for source, destination in copies.items() if original_path.is_relative_to(source)]
        if len(destinations) != 1:
            raise ValueError('parent mapping must belong to the private x or smux source')
        if not replacement:
            inherited[str(destinations[0])] = ''
            continue
        effective = Path(replacement).resolve()
        copied = parent_dir / (str(index) + '-' + effective.name)
        shutil.copyfile(effective, copied)
        parent_inputs.append({'source': str(effective), 'sha256': sha256(effective),
                              'copy': str(copied)})
        inherited[str(destinations[0])] = str(copied)
    source = copies[local_x]
    candidate = timeout_overlay(output / 'timeout-patch', source,
                                {'overlay': {'Replace': inherited}}, include_tests=True)
    # Freeze contract inputs as well; no live repository test path is executed.
    contract_dir = output / 'contracts'
    contract_dir.mkdir()
    for contract in candidate['contracts']:
        copied = contract_dir / Path(contract['source']).name
        shutil.copyfile(contract['source'], copied)
        candidate['overlay']['Replace'][contract['target']] = str(copied)
        contract['execution_copy'] = str(copied)
    overlay_path = output / 'timeout-patch/overlay.json'
    overlay_path.write_text(json.dumps(candidate['overlay'], indent=2) + '\n', encoding='utf-8')
    (output / 'timeout-patch/provenance.json').write_text(json.dumps(candidate, indent=2) + '\n', encoding='utf-8')
    modfile = output / 'contracts.mod'
    shutil.copyfile(source / 'go.mod', modfile)
    shutil.copyfile(source / 'go.sum', modfile.with_suffix('.sum'))
    environment = dict(os.environ if env is None else env)
    for key in ('GOFLAGS', 'GOWORK', 'GOOS', 'GOARCH', 'GOEXPERIMENT',
                'GOTMPDIR', 'GOENV', 'GOPATH', 'GOMODCACHE', 'GOCACHE'):
        environment.pop(key, None)
    if seed_cache:
        shutil.copytree(Path(seed_cache) / 'cache/download', output / 'module-cache/cache/download')
    environment.update({'GOMODCACHE': str(output / 'module-cache'),
                        'GOCACHE': str(output / 'build-cache'), 'GOPATH': str(output / 'go-path'),
                        'GOENV': 'off', 'GOWORK': 'off', 'GOTELEMETRY': 'off'})
    commands = []

    def execute(command: list[str], log: str, timeout: int = 900):
        started, timeout_error = time.monotonic(), None
        try:
            completed = subprocess.run(command, cwd=source, env=environment, stdout=subprocess.PIPE,
                                       stderr=subprocess.STDOUT, check=False, timeout=timeout)
            stdout, code = completed.stdout, completed.returncode
        except subprocess.TimeoutExpired as error:
            stdout, code, timeout_error = error.output or b'', None, error
        (output / log).write_bytes(stdout)
        commands.append({'arguments': command, 'cwd': str(source), 'exit_code': code,
                         'timed_out': timeout_error is not None, 'timeout_seconds': timeout,
                         'elapsed_seconds': round(time.monotonic() - started, 3), 'log': log})
        (output / 'commands.json').write_text(json.dumps(commands, indent=2) + '\n', encoding='utf-8')
        if timeout_error is not None:
            raise timeout_error
        if code:
            raise RuntimeError('command failed: see ' + str(output / log))

    if local_smux:
        execute([go, 'mod', 'edit', '-modfile=' + str(modfile),
                 '-replace=github.com/xtaci/smux=' + str(copies[local_smux])], 'mod-edit.log')
    mod_hash, sum_hash = sha256(modfile), sha256(modfile.with_suffix('.sum'))
    command = [go, 'test', '-mod=readonly', '-modfile=' + str(modfile),
               '-overlay=' + str(overlay_path), '-run', CANDIDATE_TEST_PATTERN,
               '-count=' + str(count), '-timeout=120s', '-json']
    if race:
        command.append('-race')
    command.extend(['./listener/kcp', './dialer/kcp'])
    try:
        execute(command, 'timeout-results.jsonl')
        passes = validate_events(output / 'timeout-results.jsonl', count)
        if sha256(modfile) != mod_hash or sha256(modfile.with_suffix('.sum')) != sum_hash:
            raise RuntimeError('temporary module graph changed during test')
        if any(manifest(source) != source_manifests[str(source)] for source in inputs):
            raise RuntimeError('read-only source input changed during test')
        if any(sha256(Path(row['source'])) != row['sha256'] for row in parent_inputs):
            raise RuntimeError('read-only parent input changed during test')
    except Exception as error:
        (output / 'failure.json').write_text(json.dumps({'error_type': type(error).__name__,
            'commands': str(output / 'commands.json'), 'log': str(output / 'timeout-results.jsonl')},
            indent=2) + '\n', encoding='utf-8')
        raise
    result = {'candidate': 'timeout', 'parent_candidate': 'dial', 'count': count, 'race': race,
              'passed_test_events': passes, 'expected_packages': list(PACKAGES),
              'expected_test_names': sorted(expected_tests()), 'commands': commands,
              'source_manifests': source_manifests, 'parent_inputs': parent_inputs,
              'timeout_provenance': candidate, 'modfile_sha256': mod_hash, 'sumfile_sha256': sum_hash,
              'runner_sha256': sha256(Path(__file__)),
              'helper_sha256': sha256(Path(__file__).with_name('udp_timeout_overlay.py')),
              'scope': 'real KCP Init and session config; inherited mappings retained; '
                       'not GOST process replacement, Linux delivery, WAN or performance acceptance'}
    (output / 'result.json').write_text(json.dumps(result, indent=2) + '\n', encoding='utf-8')
    return result


if __name__ == '__main__':
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--source', type=Path, required=True)
    parser.add_argument('--smux-source', type=Path)
    parser.add_argument('--parent', type=Path, required=True, help='dial binary provenance JSON')
    parser.add_argument('--output', type=Path, required=True)
    parser.add_argument('--go', default='go')
    parser.add_argument('--count', type=int, default=3)
    parser.add_argument('--no-race', action='store_true')
    parser.add_argument('--seed-cache', type=Path)
    args = parser.parse_args()
    print(json.dumps(run_contracts(args.output, args.source,
        json.loads(args.parent.read_text(encoding='utf-8-sig')), local_smux=args.smux_source,
        go=args.go, count=args.count, race=not args.no_race, seed_cache=args.seed_cache), indent=2))
