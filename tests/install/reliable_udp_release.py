#!/usr/bin/env python3
"""Accept the exact locked release engine on disposable Linux loopback only."""
import argparse
import importlib.util
import json
import os
from pathlib import Path
import subprocess
import sys

ROOT = Path(__file__).resolve().parents[2]
sys.path.insert(0, str(ROOT / 'deploy'))
from reliable_udp_engine import load_recipe, sha256, validate_buildinfo, verify_bundle


def module(name, path):
    spec = importlib.util.spec_from_file_location(name, path)
    value = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(value)
    return value


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--bundle', required=True, type=Path)
    parser.add_argument('--manifest-sha256', required=True)
    parser.add_argument('--results', required=True, type=Path)
    args = parser.parse_args()
    if sys.platform != 'linux' or os.environ.get('HL_PANEL_ISOLATED_RELIABLE_UDP_TEST') != 'true':
        raise RuntimeError('requires explicitly isolated Linux acceptance')
    bundle = args.bundle.resolve()
    manifest = verify_bundle(bundle, args.manifest_sha256)
    binary = bundle / 'gost'
    version = subprocess.run([str(binary), '-V'], check=True, capture_output=True,
                             text=True, timeout=10).stdout.strip()
    if manifest['version'].removeprefix('v') not in version:
        raise RuntimeError('running engine version does not match release manifest')
    info = subprocess.run(['go', 'version', '-m', str(binary)], check=True,
                          capture_output=True, text=True, timeout=15).stdout
    validate_buildinfo(info, load_recipe())
    lines = info.splitlines()
    lines[0] = 'gost: ' + lines[0].rsplit(': ', 1)[1]
    if '\n'.join(lines) + '\n' != (bundle / 'buildinfo.txt').read_text(encoding='utf-8'):
        raise RuntimeError('actual executable build information differs from bundle')
    fixture = module('release_udp_fixture', ROOT / 'tests/integration/gost/reliable_udp_test.py')
    close = module('release_udp_close', ROOT / 'tests/integration/gost/udp_close_checks.py')
    pair = module('release_udp_gates', ROOT / 'tests/integration/gost/reliable_udp_pair.py')
    report = {'status': 'running', 'manifest_sha256': args.manifest_sha256,
              'binary_sha256': sha256(binary), 'version': version, 'scenarios': [],
              'scope': 'isolated Linux loopback; no WAN, product integration or production acceptance'}
    args.results.parent.mkdir(parents=True, exist_ok=True)

    def save():
        args.results.write_text(json.dumps(report, indent=2) + '\n', encoding='utf-8')

    save()
    try:
        for ordinal in range(2):
            for transport in ('quic', 'kcp'):
                for lossy in (False, True):
                    if sha256(binary) != report['binary_sha256']:
                        raise RuntimeError('release executable changed during acceptance')
                    profile = pair.timeout_profile(fixture, transport)
                    item = {'variant': 'timeout', 'ordinal': ordinal, 'transport': transport,
                            'lossy': lossy, 'binary_sha256': report['binary_sha256'],
                            'profile': profile}
                    report['scenarios'].append(item)
                    save()
                    measured = fixture.scenario(binary, transport, lossy,
                        target_factory=close.target_factory(fixture),
                        closing_check=lambda port: close.closing_checks(fixture, port),
                        lossy_closing_check=lambda port, proxy: close.lossy_closing_checks(
                            fixture, port, proxy), transport_profile='timeout')
                    item.update({'measurement': measured, 'stable_bulk_passed': True,
                        'legacy_final_frame_passed': measured['final_frame_close_check']['all_complete'],
                        'extended_close_passed': measured['extended_close_checks']['extended_all_complete'],
                        'engine_reconnect_passed': measured['engine_reconnect']['all_checks_passed'],
                        'lossy_close_passed': measured['lossy_close_checks']['all_checks_passed']})
                    if not pair.candidate_result_passes(item, lossy_close=True):
                        raise RuntimeError('release engine failed the existing UDP hard gates')
                    save()
                    print(json.dumps({'ordinal': ordinal, 'transport': transport,
                          'lossy': lossy, 'status': 'passed',
                          'binary_sha256': report['binary_sha256']}), flush=True)
        report['status'] = 'passed'
        save()
    except BaseException as error:
        report['status'] = 'failed'
        report['failure_type'] = type(error).__name__
        save()
        raise
    print(json.dumps({'status': report['status'], 'scenarios': len(report['scenarios']),
          'binary_sha256': report['binary_sha256'], 'scope': report['scope']}), flush=True)


if __name__ == '__main__':
    main()
