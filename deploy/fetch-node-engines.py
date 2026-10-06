#!/usr/bin/env python3
"""Build-time fetch of pinned official engines. No runtime latest downloads."""
import argparse
import hashlib
import io
import json
from pathlib import Path
import tarfile
import urllib.request
import zipfile

ENGINES = [
    {'name': 'xray', 'version': 'v26.3.27', 'format': 'zip',
     'url': 'https://github.com/XTLS/Xray-core/releases/download/v26.3.27/Xray-linux-64.zip',
     'sha256': '23cd9af937744d97776ee35ecad4972cf4b2109d1e0fe6be9930467608f7c8ae'},
    {'name': 'gost', 'version': 'v3.3.1-nightly.20260922', 'format': 'tar.gz',
     'url': 'https://github.com/go-gost/gost/releases/download/v3.3.1-nightly.20260922/gost_3.3.1-nightly.20260922_linux_amd64.tar.gz',
     'sha256': '7ce4abf8856fc45926b86d92c770f00c8d7cfbc353d410b9e2886f8ce0a2fe0f'},
]

def main():
    parser = argparse.ArgumentParser()
    parser.add_argument('release_directory', type=Path)
    args = parser.parse_args()
    binary = args.release_directory / 'bin'
    licenses = args.release_directory / 'licenses'
    binary.mkdir(parents=True, exist_ok=True)
    licenses.mkdir(parents=True, exist_ok=True)
    for engine in ENGINES:
        request = urllib.request.Request(engine['url'], headers={'User-Agent': 'HL-panel-release'})
        with urllib.request.urlopen(request, timeout=120) as response:
            data = response.read(128*1024*1024+1)
        assert len(data) <= 128*1024*1024 and hashlib.sha256(data).hexdigest() == engine['sha256'], 'Official engine archive checksum mismatch'
        files = {}
        if engine['format'] == 'zip':
            with zipfile.ZipFile(io.BytesIO(data)) as source:
                for entry in source.infolist():
                    if entry.filename in (engine['name'], 'LICENSE'):
                        assert entry.file_size <= 128*1024*1024 and entry.filename not in files
                        files[entry.filename] = source.read(entry)
        else:
            with tarfile.open(fileobj=io.BytesIO(data), mode='r:gz') as source:
                for entry in source.getmembers():
                    name = entry.name.removeprefix('./')
                    if name in (engine['name'], 'LICENSE'):
                        assert entry.isfile() and entry.size <= 128*1024*1024 and name not in files
                        files[name] = source.extractfile(entry).read()
        assert engine['name'] in files and 'LICENSE' in files, 'Engine binary/license missing'
        path = binary / engine['name']
        path.write_bytes(files[engine['name']]); path.chmod(0o755)
        (licenses / (engine['name'] + '-LICENSE')).write_bytes(files['LICENSE'])
    (licenses / 'node-engines.json').write_text(json.dumps(ENGINES, indent=2) + '\n')

if __name__ == '__main__':
    main()
