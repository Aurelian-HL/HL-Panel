#!/usr/bin/env python3
"""Build-time pinned engines; reviewed UDP bundles require an explicit hash."""
import argparse
import hashlib
import io
import json
from pathlib import Path
import tarfile
import urllib.request
import zipfile


class EngineBuildError(RuntimeError):
    pass


def verify_bundle(directory, manifest_sha256):
    # Only the release builder needs the source recipe. The shipped official
    # fetch helper remains standalone; node installations never compile GOST.
    from reliable_udp_engine import verify_bundle as verify
    return verify(directory, manifest_sha256)

ENGINES = [
    {'name': 'xray', 'version': 'v26.7.28', 'format': 'zip',
     'url': 'https://github.com/XTLS/Xray-core/releases/download/v26.7.28/Xray-linux-64.zip',
     'sha256': '8195d909f1109b8f3d99eefe401a3c451d7bf4af71f24d3815420f77e5dd2a40'},
    {'name': 'gost', 'version': 'v3.3.1-nightly.20260922', 'format': 'tar.gz',
     'url': 'https://github.com/go-gost/gost/releases/download/v3.3.1-nightly.20260922/gost_3.3.1-nightly.20260922_linux_amd64.tar.gz',
     'sha256': '7ce4abf8856fc45926b86d92c770f00c8d7cfbc353d410b9e2886f8ce0a2fe0f'},
]

def install_engines(release_directory, gost_bundle=None, gost_manifest_sha256=None):
    if (gost_bundle is None) != (gost_manifest_sha256 is None):
        raise EngineBuildError('GOST bundle and its reviewed manifest SHA256 must be supplied together')
    candidate = verify_bundle(gost_bundle, gost_manifest_sha256) if gost_bundle is not None else None
    binary = release_directory / 'bin'
    licenses = release_directory / 'licenses'
    binary.mkdir(parents=True, exist_ok=True)
    licenses.mkdir(parents=True, exist_ok=True)
    records = []
    for engine in ENGINES:
        if engine['name'] == 'gost' and candidate is not None:
            data = (gost_bundle / 'gost').read_bytes()
            license_data = (gost_bundle / 'LICENSE').read_bytes()
            # Recheck the bytes being installed, including concurrent source changes.
            if hashlib.sha256(data).hexdigest() != candidate['files']['gost']['sha256'] or \
                    hashlib.sha256(license_data).hexdigest() != candidate['files']['LICENSE']['sha256']:
                raise EngineBuildError('GOST bundle changed during installation')
            path = binary / 'gost'
            path.write_bytes(data)
            path.chmod(0o755)
            (licenses / 'gost-LICENSE').write_bytes(license_data)
            for module in ('x', 'smux'):
                (licenses / ('gost-' + module + '-LICENSE')).write_bytes(
                    (gost_bundle / (module + '-LICENSE')).read_bytes())
            (licenses / 'gost-buildinfo.txt').write_bytes((gost_bundle / 'buildinfo.txt').read_bytes())
            (licenses / 'gost-manifest.json').write_bytes((gost_bundle / 'manifest.json').read_bytes())
            records.append({'name': 'gost', 'version': candidate['version'],
                            'source': 'reviewed-local-bundle', 'target': candidate['target'],
                            'source_commit': candidate['source_commit'],
                            'recipe_sha256': candidate['recipe_sha256'],
                            'manifest_sha256': gost_manifest_sha256,
                            'binary_sha256': candidate['files']['gost']['sha256']})
            continue
        request = urllib.request.Request(engine['url'], headers={'User-Agent': 'HL-panel-release'})
        with urllib.request.urlopen(request, timeout=120) as response:
            data = response.read(128*1024*1024+1)
        if len(data) > 128*1024*1024 or hashlib.sha256(data).hexdigest() != engine['sha256']:
            raise EngineBuildError('Official engine archive checksum mismatch')
        files = {}
        if engine['format'] == 'zip':
            with zipfile.ZipFile(io.BytesIO(data)) as source:
                for entry in source.infolist():
                    selected = (engine['name'], 'LICENSE', 'geoip.dat', 'geosite.dat') if engine['name'] == 'xray' else (engine['name'], 'LICENSE')
                    if entry.filename in selected:
                        if entry.file_size > 128*1024*1024 or entry.filename in files:
                            raise EngineBuildError('Invalid official engine archive member')
                        files[entry.filename] = source.read(entry)
        else:
            with tarfile.open(fileobj=io.BytesIO(data), mode='r:gz') as source:
                for entry in source.getmembers():
                    name = entry.name.removeprefix('./')
                    if name in (engine['name'], 'LICENSE'):
                        if not entry.isfile() or entry.size > 128*1024*1024 or name in files:
                            raise EngineBuildError('Invalid official engine archive member')
                        files[name] = source.extractfile(entry).read()
        if engine['name'] not in files or 'LICENSE' not in files:
            raise EngineBuildError('Engine binary/license missing')
        if engine['name'] == 'xray':
            for name in ('geoip.dat', 'geosite.dat'):
                if not files.get(name):
                    raise EngineBuildError('Xray geodata missing from pinned archive')
                (binary / name).write_bytes(files[name])
                (binary / name).chmod(0o644)
        path = binary / engine['name']
        path.write_bytes(files[engine['name']]); path.chmod(0o755)
        (licenses / (engine['name'] + '-LICENSE')).write_bytes(files['LICENSE'])
        records.append(dict(engine, binary_sha256=hashlib.sha256(files[engine['name']]).hexdigest()))
    (licenses / 'node-engines.json').write_text(json.dumps(records, indent=2) + '\n', encoding='utf-8')


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument('release_directory', type=Path)
    parser.add_argument('--gost-bundle', type=Path)
    parser.add_argument('--gost-manifest-sha256')
    args = parser.parse_args()
    install_engines(args.release_directory, args.gost_bundle, args.gost_manifest_sha256)

if __name__ == '__main__':
    main()
