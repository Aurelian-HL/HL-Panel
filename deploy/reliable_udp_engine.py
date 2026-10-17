#!/usr/bin/env python3
"""Build-time only: pinned, auditable Linux amd64 GOST reliable UDP bundle.

The output needs real-engine acceptance before passing its manifest SHA256 to
fetch-node-engines.py. This tool never runs an engine or changes networking.
"""
from __future__ import annotations

import argparse
import hashlib
import json
import os
from pathlib import Path, PurePosixPath
import platform
import re
import shutil
import stat
import struct
import subprocess
import sys
import tarfile
import urllib.parse
import urllib.request

RECIPE = Path(__file__).with_name('reliable-udp-engine')
MAX_FILE = 192 * 1024 * 1024
MAX_SOURCE = 384 * 1024 * 1024


class EngineBuildError(RuntimeError):
    pass


def require(condition, message):
    if not condition:
        raise EngineBuildError(message)


def sha256(path):
    value = hashlib.sha256()
    with path.open('rb') as handle:
        for chunk in iter(lambda: handle.read(1024 * 1024), b''):
            value.update(chunk)
    return value.hexdigest()


def write_json(path, value):
    path.write_text(json.dumps(value, indent=2) + '\n', encoding='utf-8', newline='\n')


def tree_hash(directory):
    records = []
    for path in sorted(directory.rglob('*'), key=lambda item: item.relative_to(directory).as_posix()):
        require(not path.is_symlink(), 'symbolic link in source tree')
        if path.is_file():
            records.append({'path': path.relative_to(directory).as_posix(),
                            'bytes': path.stat().st_size, 'sha256': sha256(path)})
        else:
            require(path.is_dir(), 'special file in source tree')
    return hashlib.sha256(json.dumps(records, sort_keys=True, separators=(',', ':')).encode()).hexdigest()


def load_recipe(recipe=RECIPE):
    lock = json.loads((recipe / 'lock.json').read_text(encoding='utf-8'))
    require(lock.get('schema') == 1 and lock.get('target') == 'linux/amd64', 'unsupported recipe')
    require(isinstance(lock.get('version'), str) and
            re.fullmatch(r'v[0-9][A-Za-z0-9.+-]*', lock['version']), 'unsafe engine version')
    for name, key in [('candidate.mod', 'candidate_mod_sha256'),
                      ('candidate.sum', 'candidate_sum_sha256')]:
        require(sha256(recipe / name) == lock[key], 'recipe module file checksum mismatch')
    for key, module in lock['modules'].items():
        require(sha256(recipe / (key + '-LICENSE')) == module['license_sha256'],
                'module license checksum mismatch')
    seen = set()
    for item in lock['patches']:
        require(item['module'] in {'x', 'smux'}, 'unknown patched module')
        relative = PurePosixPath(item['path'])
        patch = PurePosixPath(item['patch'])
        require(not relative.is_absolute() and '..' not in relative.parts and
                not patch.is_absolute() and '..' not in patch.parts and
                patch.parts[0] == 'patches' and patch.name.endswith('.go.src') and
                not relative.name.endswith('_test.go'),
                'unsafe or test-only release patch path')
        require((item['module'], str(relative)) not in seen, 'duplicate patch target')
        seen.add((item['module'], str(relative)))
        path = recipe / str(patch)
        require(not path.is_symlink() and sha256(path) == item['sha256'], 'release patch checksum mismatch')
    return lock


class HTTPSRedirect(urllib.request.HTTPRedirectHandler):
    def redirect_request(self, req, fp, code, msg, headers, newurl):
        require(urllib.parse.urlsplit(newurl).scheme == 'https', 'download redirect left HTTPS')
        return super().redirect_request(req, fp, code, msg, headers, newurl)


def download(url, destination):
    require(urllib.parse.urlsplit(url).scheme == 'https', 'HTTPS source required')
    request = urllib.request.Request(url, headers={'User-Agent': 'HL-panel-engine-build'})
    with urllib.request.build_opener(HTTPSRedirect()).open(request, timeout=120) as response:
        data = response.read(96 * 1024 * 1024 + 1)
    require(0 < len(data) <= 96 * 1024 * 1024, 'source archive size invalid')
    destination.write_bytes(data)


def unpack_source(archive, destination):
    total = 0
    seen = set()
    with tarfile.open(archive, 'r:gz') as source:
        for count, entry in enumerate(source, 1):
            path = PurePosixPath(entry.name)
            require(count <= 30000 and not path.is_absolute() and '..' not in path.parts
                    and '\\' not in entry.name and entry.name not in seen, 'unsafe source archive path')
            seen.add(entry.name)
            require(entry.isdir() or entry.isfile(), 'source archive contains a special file')
            target = destination.joinpath(*path.parts)
            if entry.isdir():
                target.mkdir(parents=True, exist_ok=True)
                continue
            total += entry.size
            require(0 <= entry.size <= MAX_FILE and total <= MAX_SOURCE, 'source archive exceeds limits')
            target.parent.mkdir(parents=True, exist_ok=True)
            with source.extractfile(entry) as handle:
                target.write_bytes(handle.read())
    roots = list(destination.iterdir())
    require(len(roots) == 1 and roots[0].is_dir(), 'source archive root invalid')
    return roots[0]


def parse_buildinfo(document):
    dependencies, replacements, settings = {}, {}, {}
    current = None
    for raw in document.splitlines():
        fields = raw.strip().split()
        if not fields:
            continue
        if fields[0] == 'dep' and len(fields) >= 3:
            current = fields[1]
            dependencies[current] = fields[2]
        elif fields[0] == '=>' and current is not None:
            replacements[current] = fields[1:]
        elif fields[0] == 'build' and len(fields) == 2 and '=' in fields[1]:
            key, value = fields[1].split('=', 1)
            settings[key] = value
        else:
            current = None
    return {'dependencies': dependencies, 'replacements': replacements, 'settings': settings}


def validate_buildinfo(document, lock):
    parsed = parse_buildinfo(document)
    expected = {'github.com/go-gost/x': './hl-udp-x', 'github.com/xtaci/smux': './hl-udp-smux'}
    require(set(parsed['replacements']) == set(expected), 'unexpected engine module replacements')
    for name, relative in expected.items():
        require(parsed['replacements'][name] == [relative, '(devel)'], 'engine replacement is not reproducible')
    for module in lock['modules'].values():
        require(parsed['dependencies'].get(module['Path']) == module['Version'], 'engine dependency mismatch')
    require(parsed['dependencies'].get('github.com/quic-go/quic-go') == 'v0.60.0', 'QUIC dependency mismatch')
    require(all(parsed['settings'].get(key) == value for key, value in
                [('GOOS', 'linux'), ('GOARCH', 'amd64'), ('CGO_ENABLED', '0')]), 'engine target mismatch')
    return parsed


def verify_elf(path):
    with path.open('rb') as handle:
        header = handle.read(64)
    require(len(header) == 64 and header[:6] == b'\x7fELF\x02\x01' and
            struct.unpack_from('<H', header, 18)[0] == 62, 'engine is not Linux amd64 ELF')


def release_build_arguments(binary, lock):
    return ['go', 'build', '-mod=readonly', '-modfile=candidate.mod', '-trimpath',
            '-buildvcs=false', '-ldflags', '-X main.version=' + lock['version'].removeprefix('v'),
            '-o', str(binary), './cmd/gost']


def verify_runtime_version(document, lock):
    expected = 'gost ' + lock['version'].removeprefix('v') + ' (' + lock['go_version'] + ' linux/amd64)'
    require(document.strip() == expected, 'running engine version does not match release manifest')


def copy_module_for_patching(upstream, target):
    shutil.copytree(upstream, target)
    # Go's authenticated module cache is read-only on Linux. Only the private
    # copy may gain owner write permission, including directories for new files.
    for directory in [target, *(item for item in target.rglob('*') if item.is_dir())]:
        require(not directory.is_symlink(), 'symbolic link in module copy')
        directory.chmod(stat.S_IMODE(directory.stat().st_mode) | stat.S_IWUSR)


def verify_bundle(bundle, manifest_sha256, recipe=RECIPE):
    require(len(manifest_sha256) == 64 and all(c in '0123456789abcdef' for c in manifest_sha256),
            'bundle manifest SHA256 required')
    require(not bundle.is_symlink() and bundle.is_dir(), 'bundle directory invalid')
    manifest_path = bundle / 'manifest.json'
    require(not manifest_path.is_symlink() and sha256(manifest_path) == manifest_sha256,
            'bundle manifest checksum mismatch')
    manifest = json.loads(manifest_path.read_text(encoding='utf-8'))
    lock = load_recipe(recipe)
    require(manifest.get('schema') == 1 and manifest.get('target') == lock['target'] and
            manifest.get('version') == lock['version'] and
            manifest.get('recipe_sha256') == sha256(recipe / 'lock.json') and
            manifest.get('source_commit') == lock['source']['commit'], 'bundle provenance mismatch')
    require(set(manifest.get('files', {})) ==
            {'gost', 'LICENSE', 'x-LICENSE', 'smux-LICENSE', 'buildinfo.txt'}, 'bundle file inventory mismatch')
    for name, record in manifest['files'].items():
        path = bundle / name
        require(not path.is_symlink() and path.is_file() and 0 < path.stat().st_size <= MAX_FILE and
                path.stat().st_size == record['bytes'] and sha256(path) == record['sha256'],
                'bundle file checksum mismatch: ' + name)
    verify_elf(bundle / 'gost')
    for key, module in lock['modules'].items():
        require(sha256(bundle / (key + '-LICENSE')) == module['license_sha256'], 'bundle license mismatch')
    validate_buildinfo((bundle / 'buildinfo.txt').read_text(encoding='utf-8'), lock)
    return manifest


def build(output, recipe=RECIPE, module_proxy='https://proxy.golang.org'):
    parsed_proxy = urllib.parse.urlsplit(module_proxy)
    require(parsed_proxy.scheme == 'https' and parsed_proxy.hostname and not parsed_proxy.username
            and not parsed_proxy.password and not parsed_proxy.query and not parsed_proxy.fragment
            and parsed_proxy.path in {'', '/'}, 'module proxy must be one HTTPS origin')
    lock = load_recipe(recipe)
    require(not output.is_symlink() and (not output.exists() or
            (output.is_dir() and not any(output.iterdir()))), 'output must be absent or empty')
    output.mkdir(parents=True, exist_ok=True)
    output = output.resolve()
    env = {key: value for key, value in os.environ.items() if not key.startswith('GO')}
    env.update({'GOENV': 'off', 'GOWORK': 'off', 'GO111MODULE': 'on', 'GOTOOLCHAIN': 'local',
                'GOOS': 'linux', 'GOARCH': 'amd64', 'CGO_ENABLED': '0',
                'GOPROXY': module_proxy, 'GOSUMDB': 'sum.golang.org',
                'GOPATH': str(output / 'go'), 'GOMODCACHE': str(output / 'go/pkg/mod'),
                'GOCACHE': str(output / 'go-cache')})
    commands = []
    def run(arguments, cwd=output):
        result = subprocess.run(arguments, cwd=cwd, env=env, capture_output=True, text=True, timeout=1800)
        commands.append({'arguments': arguments, 'exit_code': result.returncode})
        (output / ('command-%02d.log' % len(commands))).write_text(result.stdout + result.stderr, encoding='utf-8')
        write_json(output / 'commands.json', commands)
        require(result.returncode == 0, 'engine build command failed; see command logs')
        return result.stdout
    require(run(['go', 'version']).strip().split()[:3] == ['go', 'version', lock['go_version']],
            'Go toolchain does not match accepted recipe')
    archive = output / 'source.tar.gz'
    download(lock['source']['archive']['url'], archive)
    require(sha256(archive) == lock['source']['archive']['sha256'], 'GOST source archive checksum mismatch')
    source = unpack_source(archive, output / 'source')
    require(tree_hash(source) == lock['source']['tree_sha256'] and
            sha256(source / 'go.mod') == lock['source']['go_mod_sha256'] and
            sha256(source / 'go.sum') == lock['source']['go_sum_sha256'], 'GOST source tree mismatch')
    module_roots = {}
    for key, module in lock['modules'].items():
        item = json.loads(run(['go', 'mod', 'download', '-json', module['Path'] + '@' + module['Version']], source))
        require(all(item.get(field) == module[field] for field in ['Path', 'Version', 'Sum', 'GoModSum']),
                'module authentication mismatch')
        require(sha256(Path(item['Zip'])) == module['zip_sha256'] and
                sha256(Path(item['GoMod'])) == module['go_mod_sha256'], 'module archive checksum mismatch')
        upstream = Path(item['Dir']).resolve()
        require(upstream.is_relative_to(output / 'go/pkg/mod') and
                tree_hash(upstream) == module['source_tree_sha256'], 'module source tree mismatch')
        target = source / ('hl-udp-' + key)
        copy_module_for_patching(upstream, target)
        module_roots[key] = target
    for item in lock['patches']:
        target = module_roots[item['module']] / item['path']
        require((not target.exists() if item['source_sha256'] is None else
                 target.is_file() and sha256(target) == item['source_sha256']), 'patch original checksum mismatch')
        target.parent.mkdir(parents=True, exist_ok=True)
        if target.exists():
            target.chmod(0o644)
        shutil.copyfile(recipe / item['patch'], target)
    shutil.copyfile(recipe / 'candidate.mod', source / 'candidate.mod')
    shutil.copyfile(recipe / 'candidate.sum', source / 'candidate.sum')
    run(['go', 'mod', 'download', '-modfile=candidate.mod', 'all'], source)
    run(['go', 'mod', 'verify', '-modfile=candidate.mod'], source)
    require(sha256(source / 'candidate.mod') == lock['candidate_mod_sha256'] and
            sha256(source / 'candidate.sum') == lock['candidate_sum_sha256'], 'locked dependency graph changed')
    bundle = output / 'bundle'
    bundle.mkdir()
    run(release_build_arguments(bundle / 'gost', lock), source)
    info = run(['go', 'version', '-m', str(bundle / 'gost')], source)
    validate_buildinfo(info, lock)
    verify_elf(bundle / 'gost')
    info_lines = info.splitlines()
    info_lines[0] = 'gost: ' + info_lines[0].rsplit(': ', 1)[1]
    (bundle / 'buildinfo.txt').write_text('\n'.join(info_lines) + '\n', encoding='utf-8', newline='\n')
    shutil.copyfile(source / 'LICENSE', bundle / 'LICENSE')
    for key in lock['modules']:
        shutil.copyfile(recipe / (key + '-LICENSE'), bundle / (key + '-LICENSE'))
    manifest = {'schema': 1, 'name': lock['name'], 'version': lock['version'], 'target': lock['target'],
                'source_commit': lock['source']['commit'], 'recipe_sha256': sha256(recipe / 'lock.json'),
                'go_version': lock['go_version'], 'engine_acceptance_run': False,
                'files': {name: {'bytes': (bundle / name).stat().st_size, 'sha256': sha256(bundle / name)}
                          for name in ['gost', 'LICENSE', 'x-LICENSE', 'smux-LICENSE', 'buildinfo.txt']}}
    write_json(bundle / 'manifest.json', manifest)
    manifest_sha = sha256(bundle / 'manifest.json')
    verify_bundle(bundle, manifest_sha, recipe)
    return {'bundle': str(bundle), 'manifest_sha256': manifest_sha,
            'binary_sha256': manifest['files']['gost']['sha256'], 'engine_acceptance_run': False}


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('output_directory', type=Path)
    parser.add_argument('--module-proxy', default='https://proxy.golang.org',
                        help='HTTPS module mirror; Go checksum database and pinned hashes stay mandatory')
    args = parser.parse_args()
    print(json.dumps(build(args.output_directory, module_proxy=args.module_proxy)))


if __name__ == '__main__':
    try:
        main()
    except (EngineBuildError, OSError, ValueError, subprocess.SubprocessError) as error:
        raise SystemExit(str(error))
