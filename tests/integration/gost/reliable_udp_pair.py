#!/usr/bin/env python3
"""Linux-only source-pair experiment; never changes product engine selection."""
import argparse
import hashlib
import importlib.util
import json
import os
from pathlib import Path
import sys


def load_module(name, path):
    spec = importlib.util.spec_from_file_location(name, path)
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument('provenance', type=Path)
    parser.add_argument('results', type=Path)
    parser.add_argument('--lifecycle-focus', action='store_true')
    parser.add_argument('--halfclose-focus', action='store_true')
    parser.add_argument('--drain-focus', action='store_true')
    parser.add_argument('--lossy-close', action='store_true')
    args = parser.parse_args()
    if sys.platform != 'linux' or os.environ.get('HL_PANEL_ISOLATED_RELIABLE_UDP_TEST') != 'true':
        raise RuntimeError('requires explicitly isolated Linux acceptance')
    root = Path(__file__).resolve().parent
    module = load_module('reliable_udp_fixture', root / 'reliable_udp_test.py')
    close = load_module('udp_close_checks', root / 'udp_close_checks.py')
    provenance = json.loads(args.provenance.read_text(encoding='utf-8'))
    variants = provenance['variants']
    if set(variants) not in ({'official', 'original', 'fixed'},
                             {'official', 'original', 'fixed', 'lifecycle'},
                             {'official', 'original', 'fixed', 'lifecycle', 'halfclose'},
                             {'official', 'original', 'fixed', 'lifecycle', 'halfclose', 'drain'}):
        raise RuntimeError('source pair requires the complete recorded provenance entries')
    # A repeated round reverses build order; this is a correctness experiment,
    # not enough repetitions or load to claim any performance ranking.
    if sum((args.lifecycle_focus, args.halfclose_focus, args.drain_focus)) > 1:
        raise RuntimeError('choose exactly one candidate focus')
    if args.drain_focus:
        if 'drain' not in variants:
            raise RuntimeError('drain focus requires its separately recorded candidate')
        order = ('halfclose', 'drain', 'drain', 'halfclose')
    elif args.halfclose_focus:
        if 'halfclose' not in variants:
            raise RuntimeError('half-close focus requires its separately recorded candidate')
        order = ('lifecycle', 'halfclose', 'halfclose', 'lifecycle')
    elif args.lifecycle_focus:
        if 'lifecycle' not in variants:
            raise RuntimeError('lifecycle focus requires its separately recorded candidate')
        order = ('fixed', 'lifecycle', 'lifecycle', 'fixed')
    else:
        order = ('official', 'original', 'fixed', 'fixed', 'original')
    results = []
    args.results.parent.mkdir(parents=True, exist_ok=True)
    args.results.write_text('', encoding='utf-8')
    for ordinal, variant in enumerate(order):
        item = variants[variant]
        binary = Path(item['binary'])
        if not binary.is_absolute() or not binary.is_file():
            raise RuntimeError('provenance binary must be an absolute existing file')
        if hashlib.sha256(binary.read_bytes()).hexdigest() != item['sha256']:
            raise RuntimeError('binary changed since build provenance was recorded')
        for transport in ('quic', 'kcp'):
            for lossy in (False, True):
                result = {'variant': variant, 'ordinal': ordinal, 'transport': transport,
                          'lossy': lossy, 'binary_sha256': item['sha256']}
                try:
                    measured = module.scenario(binary, transport, lossy,
                        target_factory=close.target_factory(module),
                        closing_check=lambda port: close.closing_checks(module, port),
                        lossy_closing_check=(lambda port, proxy: close.lossy_closing_checks(
                            module, port, proxy)) if args.lossy_close else None)
                    result['measurement'] = measured
                    result['stable_bulk_passed'] = True
                    result['legacy_final_frame_passed'] = measured['final_frame_close_check']['all_complete']
                    result['extended_close_passed'] = measured['extended_close_checks']['extended_all_complete']
                    if args.lossy_close:
                        result['lossy_close_passed'] = measured['lossy_close_checks']['all_checks_passed']
                except Exception as error:
                    # Error text, logs and temporary credentials are never surfaced.
                    result['stable_bulk_passed'] = False
                    result['failure_type'] = type(error).__name__
                results.append(result)
                line = json.dumps(result, sort_keys=True)
                with args.results.open('a', encoding='utf-8') as output:
                    output.write(line + '\n')
                print('UDP_PAIR_RESULT ' + line, flush=True)
    candidate = ('drain' if args.drain_focus else ('halfclose' if args.halfclose_focus
                 else ('lifecycle' if args.lifecycle_focus else 'fixed')))
    fixed = [item for item in results if item['variant'] == candidate]
    full = all(item.get('stable_bulk_passed') and item.get('legacy_final_frame_passed')
               and item.get('extended_close_passed')
               and (not args.lossy_close or item.get('lossy_close_passed')) for item in fixed)
    per_transport = {transport: all(
        item.get('stable_bulk_passed') and item.get('legacy_final_frame_passed')
        and item.get('extended_close_passed')
        and (not args.lossy_close or item.get('lossy_close_passed'))
        for item in fixed if item['transport'] == transport) for transport in ('quic', 'kcp')}
    summary = {'candidate_variant': candidate, 'candidate_all_checks_passed': full,
               'candidate_scenarios': len(fixed), 'candidate_per_transport': per_transport,
               'total_scenarios': len(results), 'source_commit': provenance['source_commit'],
               'lossy_close_required': args.lossy_close,
               'scope': 'isolated loopback correctness experiment; not production or performance acceptance'}
    print('UDP_PAIR_SUMMARY ' + json.dumps(summary, sort_keys=True), flush=True)
    if not full:
        raise RuntimeError(candidate + ' candidate failed at least one original or extended delivery check')


if __name__ == '__main__':
    main()
