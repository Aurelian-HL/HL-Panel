#!/usr/bin/env python3
"""Linux-only source-pair experiment; never changes product engine selection."""
import argparse
import hashlib
import importlib.util
import json
import math
import os
from pathlib import Path
import re
import sys


RESTART_SCOPE = ('local isolated GOST server process replacement; client process retained; '
                 'old application stream fails and a new stream recovers; no injected loss')


def load_module(name, path):
    spec = importlib.util.spec_from_file_location(name, path)
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


def candidate_order(variants, *, lifecycle=False, halfclose=False, drain=False,
                    abort=False, dial=False, timeout=False):
    base = {'official', 'original', 'fixed'}
    stages = ('lifecycle', 'halfclose', 'drain', 'abort', 'dial', 'timeout')
    allowed = [base | set(stages[:count]) for count in range(len(stages) + 1)]
    if set(variants) not in allowed:
        raise RuntimeError('source pair requires the complete recorded provenance entries')
    selected = [name for name, enabled in zip(stages, (lifecycle, halfclose, drain, abort, dial, timeout))
                if enabled]
    if len(selected) > 1:
        raise RuntimeError('choose exactly one candidate focus')
    if selected:
        candidate = selected[0]
        if candidate not in variants:
            raise RuntimeError(candidate + ' focus requires its separately recorded candidate')
        previous = ('fixed',) + stages[:-1]
        parent = previous[stages.index(candidate)]
        return (parent, candidate, candidate, parent), candidate
    return ('official', 'original', 'fixed', 'fixed', 'original'), 'fixed'


def candidate_result_passes(item, *, lossy_close):
    """Keep process replacement as a first-class candidate acceptance gate."""
    if not isinstance(item, dict) or not isinstance(item.get('measurement'), dict):
        return False
    restart = item['measurement'].get('engine_reconnect', {})
    restart_identity = (
        isinstance(restart, dict) and
        restart.get('all_checks_passed') is True and
        restart.get('server_stopped') is True and
        type(restart.get('server_pid_before')) is int and restart['server_pid_before'] > 0 and
        type(restart.get('server_pid_after')) is int and restart['server_pid_after'] > 0 and
        restart['server_pid_before'] != restart['server_pid_after'] and
        type(restart.get('client_pid_before')) is int and restart['client_pid_before'] > 0 and
        type(restart.get('client_pid_after')) is int and restart['client_pid_after'] > 0 and
        restart['client_pid_before'] == restart['client_pid_after'] and
        restart.get('client_identity_unchanged') is True and
        restart.get('old_connection_open_before_stop') is True and
        restart.get('old_connection_failed') is True and
        restart.get('old_connection_failure') in ('eof', 'socket_error') and
        type(restart.get('old_connection_failure_timeout_seconds')) is int and
        restart['old_connection_failure_timeout_seconds'] == 15 and
        all(type(restart.get(key)) in (int, float) and math.isfinite(restart[key]) and
            restart[key] >= 0 for key in ('pre_stop_probe_elapsed_seconds',
                'old_connection_failure_elapsed_seconds', 'replacement_readiness_elapsed_seconds')) and
        restart['old_connection_failure_elapsed_seconds'] <= 15 and
        restart.get('replacement_bulk_passed') is True and
        all(type(restart.get(key)) is int and restart[key] == 2 * 1024 * 1024
            for key in ('replacement_upload_bytes', 'replacement_download_bytes')) and
        all(isinstance(restart.get(key), str) and
            re.fullmatch(r'[0-9a-f]{64}', restart[key]) is not None
            for key in ('replacement_upload_sha256', 'replacement_download_sha256')) and
        restart.get('injected_datagram_loss') is False and
        restart.get('scope') == RESTART_SCOPE)
    profile_ok = True
    if item.get('variant') == 'timeout':
        profile = item.get('profile')
        profile_ok = (isinstance(profile, dict) and profile.get('profile_name') == 'timeout' and
                      profile.get('transport') == item.get('transport') and
                      profile.get('comparison') == 'binary_plus_profile' and
                      isinstance(profile.get('effective'), dict))
    return (profile_ok and item.get('stable_bulk_passed') is True and
            item.get('legacy_final_frame_passed') is True and
            item.get('extended_close_passed') is True and
            item.get('engine_reconnect_passed') is True and restart_identity and
            (not lossy_close or item.get('lossy_close_passed') is True))


def timeout_profile(module, transport):
    """Require the fixture's single allowlisted profile constructor.

    A timeout candidate must never silently fall back to the fixture defaults.
    The constructor is supplied by reliable_udp_test.py after its profile
    integration is reviewed by the parent task.
    """
    constructor = getattr(module, 'transport_profile', None)
    if not callable(constructor):
        raise RuntimeError('timeout profile fixture is not integrated')
    profile = constructor(transport, 'timeout')
    if not isinstance(profile, dict) or profile.get('profile_name') != 'timeout' or \
            profile.get('transport') != transport or \
            profile.get('comparison') != 'binary_plus_profile' or \
            not isinstance(profile.get('effective'), dict):
        raise RuntimeError('timeout profile fixture returned invalid safe profile')
    return profile


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument('provenance', type=Path)
    parser.add_argument('results', type=Path)
    parser.add_argument('--lifecycle-focus', action='store_true')
    parser.add_argument('--halfclose-focus', action='store_true')
    parser.add_argument('--drain-focus', action='store_true')
    parser.add_argument('--abort-focus', action='store_true')
    parser.add_argument('--dial-focus', action='store_true')
    parser.add_argument('--timeout-focus', action='store_true')
    parser.add_argument('--lossy-close', action='store_true')
    args = parser.parse_args()
    if sys.platform != 'linux' or os.environ.get('HL_PANEL_ISOLATED_RELIABLE_UDP_TEST') != 'true':
        raise RuntimeError('requires explicitly isolated Linux acceptance')
    root = Path(__file__).resolve().parent
    module = load_module('reliable_udp_fixture', root / 'reliable_udp_test.py')
    close = load_module('udp_close_checks', root / 'udp_close_checks.py')
    provenance = json.loads(args.provenance.read_text(encoding='utf-8'))
    variants = provenance['variants']
    # A repeated round reverses build order; this is a correctness experiment,
    # not enough repetitions or load to claim any performance ranking.
    order, candidate = candidate_order(variants, lifecycle=args.lifecycle_focus,
        halfclose=args.halfclose_focus, drain=args.drain_focus, abort=args.abort_focus,
        dial=args.dial_focus, timeout=args.timeout_focus)
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
                    profile = None
                    scenario_kwargs = {}
                    if variant == 'timeout':
                        profile = timeout_profile(module, transport)
                        result['profile'] = {'profile_name': profile['profile_name'],
                                             'transport': profile['transport'],
                                             'comparison': profile['comparison'],
                                             'effective': profile['effective']}
                        scenario_kwargs['transport_profile'] = 'timeout'
                    measured = module.scenario(binary, transport, lossy,
                        target_factory=close.target_factory(module),
                        closing_check=lambda port: close.closing_checks(module, port),
                        lossy_closing_check=(lambda port, proxy: close.lossy_closing_checks(
                            module, port, proxy)) if args.lossy_close else None,
                        **scenario_kwargs)
                    result['measurement'] = measured
                    result['stable_bulk_passed'] = True
                    result['legacy_final_frame_passed'] = measured['final_frame_close_check']['all_complete']
                    result['extended_close_passed'] = measured['extended_close_checks']['extended_all_complete']
                    result['engine_reconnect_passed'] = measured['engine_reconnect']['all_checks_passed']
                    if args.lossy_close:
                        result['lossy_close_passed'] = measured['lossy_close_checks']['all_checks_passed']
                except Exception as error:
                    # Error text, logs and temporary credentials are never surfaced.
                    result['stable_bulk_passed'] = False
                    result['failure_type'] = type(error).__name__
                    if isinstance(error, module.RestartRecoveryError):
                        result['restart_failure_category'] = 'restart_' + error.phase + '_failed'
                results.append(result)
                line = json.dumps(result, sort_keys=True)
                with args.results.open('a', encoding='utf-8') as output:
                    output.write(line + '\n')
                print('UDP_PAIR_RESULT ' + line, flush=True)
    fixed = [item for item in results if item['variant'] == candidate]
    expected_candidate_scenarios = 8  # 4 rounds x QUIC/KCP x loss/no-loss
    full = len(fixed) == expected_candidate_scenarios and all(
        candidate_result_passes(item, lossy_close=args.lossy_close) for item in fixed)
    per_transport = {transport: len([item for item in fixed if item['transport'] == transport]) == 4 and all(
        candidate_result_passes(item, lossy_close=args.lossy_close)
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
