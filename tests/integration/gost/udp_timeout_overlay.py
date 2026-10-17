"""Build an opt-in KCP session timeout overlay against pinned private x sources.

Only the four KCP adapter files are replaced. Parent mappings and all input
files remain read-only; this does not change smux defaults or establish a
15-second detection guarantee. Contract mappings are a separate opt-in.
"""
from __future__ import annotations

import argparse
import hashlib
import json
from pathlib import Path

PINNED_ORIGINAL_SHA256 = {
    'listener/kcp/metadata.go': 'ff9d9cf2386d77cca012b3efb48747cac31ae90854b2d6819f7a0f2ded59db6e',
    'listener/kcp/listener.go': '597a52e6a3bfd8c8cf82d40586ee03097ea128ab05fcb8b40af704199e185bdd',
    'dialer/kcp/metadata.go': '2cdd5a6faf9f675b5b1d8aef73dc050eef0415d70d72f161cbdc3ebea066f353',
    'dialer/kcp/dialer.go': '00c2c3b62ed8c751bf1e05911dd6e50e92e4266a2a713db24f95816d336cb319',
}
# Existing Pipe/abort/Dial overlays do not modify these four KCP files.
ALLOWED_EFFECTIVE_SHA256 = {
    name: frozenset({digest}) for name, digest in PINNED_ORIGINAL_SHA256.items()
}
CONTRACTS = {
    'listener/kcp/kcp_timeout_contract_test.go': 'listener_timeout_contract_test.go',
    'dialer/kcp/kcp_timeout_contract_test.go': 'dialer_timeout_contract_test.go',
}
CANDIDATE_TEST_PATTERN = '^TestKCPTimeout'


def sha(path: Path) -> str:
    return hashlib.sha256(path.read_bytes()).hexdigest()


def edits(relative: str) -> list[tuple[str, str]]:
    side = 'l' if relative.startswith('listener/') else 'd'
    if relative.endswith('/metadata.go'):
        changes = [
            ('\t"encoding/json"\n', '\t"encoding/json"\n\t"errors"\n\t"strconv"\n'),
            ('type metadata struct {\n', 'type metadata struct {\n\tkeepAliveTimeout time.Duration\n'),
            (f'\t{side}.md.config.NoComp = mdutil.GetBool(md, "kcp.nocomp")\n',
             f'\t{side}.md.config.NoComp = mdutil.GetBool(md, "kcp.nocomp")\n'
             f'\n\t// Absent metadata preserves the pinned smux timeout default.\n'
             f'\t{side}.md.keepAliveTimeout = 0\n'
             '\tif mdutil.IsExists(md, "kcp.keepaliveTimeout") {\n'
             '\t\tinvalid := errors.New("kcp.keepaliveTimeout must be a positive duration at least the effective keepalive interval")\n'
             '\t\tvar timeout time.Duration\n'
             '\t\tswitch value := md.Get("kcp.keepaliveTimeout").(type) {\n'
             '\t\tcase int:\n'
             '\t\t\tif value <= 0 || uint64(value) > uint64((1<<63-1)/time.Second) {\n'
             '\t\t\t\treturn invalid\n'
             '\t\t\t}\n'
             '\t\t\ttimeout = time.Duration(value) * time.Second\n'
             '\t\tcase string:\n'
             '\t\t\tvar err error\n'
             '\t\t\ttimeout, err = time.ParseDuration(value)\n'
             '\t\t\tif err != nil {\n'
             '\t\t\t\tseconds, parseErr := strconv.ParseInt(value, 10, 64)\n'
             '\t\t\t\tif parseErr != nil || seconds <= 0 || seconds > int64((1<<63-1)/time.Second) {\n'
             '\t\t\t\t\treturn invalid\n'
             '\t\t\t\t}\n'
             '\t\t\t\ttimeout = time.Duration(seconds) * time.Second\n'
             '\t\t\t}\n'
             '\t\tdefault:\n'
             '\t\t\treturn invalid\n'
             '\t\t}\n'
             f'\t\tinterval := {side}.md.config.KeepAlive\n'
             '\t\tif interval <= 0 {\n'
             '\t\t\tinterval = int(smux.DefaultConfig().KeepAliveInterval / time.Second)\n'
             '\t\t}\n'
             '\t\t// Divide the positive timeout to avoid overflowing a large interval.\n'
             '\t\tif timeout <= 0 || timeout/time.Second < time.Duration(interval) {\n'
             '\t\t\treturn invalid\n'
             '\t\t}\n'
             f'\t\t{side}.md.keepAliveTimeout = timeout\n'
             '\t}\n'),
            ('\n\tmdata "github.com/go-gost/core/metadata"',
             '\n\t"github.com/xtaci/smux"\n\n\tmdata "github.com/go-gost/core/metadata"'),
        ]
        if side == 'l':
            changes.insert(1, ('\t"encoding/json"\n', '\t"encoding/json"\n\t"time"\n'))
        return changes

    config = 'l.md.config' if side == 'l' else 'config'
    block = ('\tsmuxConfig := smux.DefaultConfig()\n'
             f'\tsmuxConfig.Version = {config}.SmuxVer\n'
             f'\tsmuxConfig.MaxReceiveBuffer = {config}.SmuxBuf\n'
             f'\tsmuxConfig.MaxStreamBuffer = {config}.StreamBuf\n'
             f'\tif {config}.KeepAlive > 0 {{\n'
             f'\t\tsmuxConfig.KeepAliveInterval = time.Duration({config}.KeepAlive) * time.Second\n'
             '\t}\n')
    helper = (f'\n// smuxConfig is used by the real session construction path and contracts.\n'
              f'func ({side} *kcp{"Listener" if side == "l" else "Dialer"}) smuxConfig() *smux.Config {{\n')
    if side == 'd':
        helper += '\tconfig := d.md.config\n'
    helper += (block + f'\tif {side}.md.keepAliveTimeout > 0 {{\n'
               f'\t\tsmuxConfig.KeepAliveTimeout = {side}.md.keepAliveTimeout\n'
               '\t}\n\treturn smuxConfig\n}\n')
    return [(block, f'\tsmuxConfig := {side}.smuxConfig()\n'), ('\nfunc init() {', helper + '\nfunc init() {')]


def build(output: Path, source: Path, parent: dict | None = None,
          *, include_tests: bool = False) -> dict:
    output, source = Path(output).resolve(), Path(source).resolve()
    if output == source or source in output.parents:
        raise ValueError('output must be outside the read-only source tree')
    if output.exists() and (not output.is_dir() or any(output.iterdir())):
        raise ValueError('output must be absent or empty')
    overlay = (parent or {}).get('overlay', parent or {'Replace': {}})
    if not isinstance(overlay, dict) or not isinstance(overlay.get('Replace'), dict):
        raise ValueError('parent must contain overlay.Replace or Replace')
    mappings = dict(overlay['Replace'])
    if any(not isinstance(k, str) or not isinstance(v, str) for k, v in mappings.items()):
        raise ValueError('parent mappings must contain string paths')
    prepared, records = [], []
    for relative, pinned in PINNED_ORIGINAL_SHA256.items():
        target = source / relative
        if sha(target) != pinned:
            raise ValueError('pinned original SHA256 mismatch: ' + relative)
        aliases = [key for key in mappings if Path(key).resolve() == target]
        if len(aliases) > 1:
            raise ValueError('duplicate parent aliases: ' + relative)
        effective = target
        if aliases:
            replacement = mappings[aliases[0]]
            if not replacement:
                raise ValueError('parent deletes KCP source: ' + relative)
            effective = Path(replacement).resolve()
        effective_sha = sha(effective)
        if effective_sha not in ALLOWED_EFFECTIVE_SHA256[relative]:
            raise ValueError('parent effective SHA256 mismatch: ' + relative)
        raw = effective.read_bytes()
        text = raw.decode('utf-8')
        operations = edits(relative)
        for old, new in operations:
            if text.count(old) != 1:
                raise ValueError('exact patch anchor count mismatch: ' + relative)
            text = text.replace(old, new, 1)
        candidate = output / relative.replace('/', '__')
        prepared.append((target, aliases, candidate, text.encode('utf-8')))
        records.append({'path': relative, 'original_sha256': pinned,
                        'effective_input': str(effective), 'effective_input_sha256': effective_sha,
                        'allowed_effective_sha256': sorted(ALLOWED_EFFECTIVE_SHA256[relative]),
                        'candidate': str(candidate), 'exact_edits': len(operations)})
    contracts = []
    if include_tests:
        for relative, name in CONTRACTS.items():
            target = source / relative
            if target.exists() or any(Path(key).resolve() == target for key in mappings):
                raise ValueError('contract target already exists in source or parent: ' + relative)
            contract = Path(__file__).resolve().parent / 'timeout_contracts' / name
            if not contract.is_file():
                raise ValueError('contract source missing: ' + name)
            contracts.append({'target': str(target), 'source': str(contract), 'sha256': sha(contract)})
    # Complete every input/hash/anchor check before writing any output.
    output.mkdir(parents=True, exist_ok=True)
    for (_target, aliases, candidate, data), record in zip(prepared, records):
        candidate.write_bytes(data)
        for alias in aliases:
            del mappings[alias]
        mappings[str(_target)] = str(candidate)
        record['candidate_sha256'] = sha(candidate)
    for contract in contracts:
        mappings[contract['target']] = contract['source']
    result = {'overlay': {'Replace': mappings}, 'source': str(source),
              'candidate': 'kcp-explicit-timeout', 'patches': records, 'contracts': contracts,
              'candidate_test_pattern': CANDIDATE_TEST_PATTERN,
              'scope': 'opt-in KCP session timeout; defaults retained; no smux change, '
                       'engine acceptance, production change or guaranteed detection bound'}
    for name, document in (('overlay.json', result['overlay']), ('provenance.json', result)):
        (output / name).write_text(json.dumps(document, indent=2) + '\n', encoding='utf-8')
    return result


def timeout_overlay(output: Path, local_x: Path, parent: dict | None = None) -> dict:
    """Layer the binary candidate without adding any contract test mappings."""
    return build(output, local_x, parent, include_tests=False)


if __name__ == '__main__':
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--source', type=Path, required=True)
    parser.add_argument('--output', type=Path, required=True)
    parser.add_argument('--parent', type=Path)
    parser.add_argument('--with-tests', action='store_true')
    args = parser.parse_args()
    parent = json.loads(args.parent.read_text(encoding='utf-8-sig')) if args.parent else None
    print(json.dumps(build(args.output, args.source, parent, include_tests=args.with_tests), indent=2))
