#!/usr/bin/env python3
"""Build isolated GOST original, Pipe-only and lifecycle research candidates.

Usage: python3 build_pair.py OUTPUT_DIRECTORY
Requires Linux amd64 and Go. It never starts GOST, changes host networking,
edits a product repository, or writes the existing Go module/build caches.
OUTPUT_DIRECTORY must be absent or empty; failed output is kept as evidence.
"""
from __future__ import annotations

import argparse
import hashlib
import json
import os
import platform
import re
import shutil
import subprocess
import sys
import tarfile
import time
import urllib.parse
import urllib.request
from pathlib import Path, PurePosixPath

from udp_abort_overlay import abort_overlay
from udp_dial_overlay import dial_overlay

TAG = 'v3.3.1-nightly.20260922'
REPOSITORY = 'go-gost/gost'
X_MODULE = 'github.com/go-gost/x'
X_VERSION = 'v0.17.2'
QUIC_MODULE = 'github.com/quic-go/quic-go'
QUIC_VERSION = 'v0.60.0'
SMUX_MODULE = 'github.com/xtaci/smux'
SMUX_VERSION = 'v1.5.57'
SMUX_STREAM_SHA256 = 'd3f54098259b2fe3f47831bc93f7895a75647e81d41df6b5d62e5e0c578adccf'
PIPE_SHA256 = '1d83f46e2699086be124b9f9db3b273b4b652f102b563e8b98fc70867590fba0'
OFFICIAL_SHA256 = '7ce4abf8856fc45926b86d92c770f00c8d7cfbc353d410b9e2886f8ce0a2fe0f'
OFFICIAL_URL = ('https://github.com/go-gost/gost/releases/download/' + TAG +
                '/gost_3.3.1-nightly.20260922_linux_amd64.tar.gz')
MAX_ARCHIVE = 96 * 1024 * 1024
MAX_UNPACKED = 384 * 1024 * 1024
MAX_FILE = 192 * 1024 * 1024
MAX_MEMBERS = 30000

# Exact, previously regression-tested fix; all other bytes remain unchanged.
PIPE_OLD = '''			// 读取数据
			nr, err := reader.Read(buf)
			if err != nil {
				if err != io.EOF {
					return err
				}
				return nil // 正常结束
			}

			// 写入数据
			_, err = dst.Write(buf[:nr])
			if err != nil {
				return err
			}
'''
PIPE_NEW = '''			// 读取数据
			nr, readErr := reader.Read(buf)
			if nr > 0 {
				nw, writeErr := dst.Write(buf[:nr])
				if writeErr != nil {
					return writeErr
				}
				if nw != nr {
					return io.ErrShortWrite
				}
			}
			if readErr != nil {
				if readErr != io.EOF {
					return readErr
				}
				return nil // 正常结束
			}
'''


def require(condition: bool, message: str) -> None:
    if not condition:
        raise RuntimeError(message)


def digest(path: Path) -> str:
    value = hashlib.sha256()
    with path.open('rb') as handle:
        for chunk in iter(lambda: handle.read(1024 * 1024), b''):
            value.update(chunk)
    return value.hexdigest()


def write_json(path: Path, value: object) -> None:
    path.write_text(json.dumps(value, indent=2, ensure_ascii=False) + '\n',
                    encoding='utf-8', newline='\n')


class SafeRedirect(urllib.request.HTTPRedirectHandler):
    def redirect_request(self, req, fp, code, msg, headers, newurl):
        require(urllib.parse.urlsplit(newurl).scheme == 'https',
                'download redirect attempted to leave HTTPS')
        redirected = super().redirect_request(req, fp, code, msg, headers, newurl)
        if redirected is not None:
            redirected.remove_header('Authorization')
        return redirected


def download(url: str, destination: Path, limit: int = MAX_ARCHIVE,
             api: bool = False) -> dict:
    require(urllib.parse.urlsplit(url).scheme == 'https', 'HTTPS is required')
    headers = {'User-Agent': 'hl-panel-isolated-pipe-research',
               'Accept': 'application/vnd.github+json' if api else '*/*'}
    token = os.environ.get('GITHUB_TOKEN', '')
    if api and token:
        require(urllib.parse.urlsplit(url).hostname == 'api.github.com',
                'credentials are limited to GitHub API metadata')
        headers['Authorization'] = 'Bearer ' + token
    opener = urllib.request.build_opener(SafeRedirect())
    started = time.monotonic()
    request = urllib.request.Request(url, headers=headers)
    received = 0
    with opener.open(request, timeout=30) as response, destination.open('xb') as handle:
        require(response.status == 200, 'download did not return HTTP 200')
        declared = response.headers.get('Content-Length')
        if declared:
            require(0 <= int(declared) <= limit, 'download Content-Length exceeds limit')
        while True:
            require(time.monotonic() - started < 240, 'download exceeded total time limit')
            chunk = response.read(1024 * 1024)
            if not chunk:
                break
            received += len(chunk)
            require(received <= limit, 'download exceeded byte limit')
            handle.write(chunk)
    require(received > 0, 'download was empty')
    return {'url': url, 'bytes': received, 'sha256': digest(destination)}


def api_json(url: str, destination: Path) -> dict:
    download(url, destination, limit=2 * 1024 * 1024, api=True)
    value = json.loads(destination.read_text(encoding='utf-8'))
    require(isinstance(value, dict), 'GitHub API returned non-object JSON')
    return value


def resolve_commit(output: Path) -> dict:
    base = 'https://api.github.com/repos/' + REPOSITORY + '/git/'
    value = api_json(base + 'ref/tags/' + urllib.parse.quote(TAG, safe=''),
                     output / 'tag-ref.json')
    require(value.get('ref') == 'refs/tags/' + TAG, 'tag reference mismatch')
    current = value.get('object', {})
    chain = []
    for depth in range(6):
        kind, sha = current.get('type'), current.get('sha')
        require(kind in {'tag', 'commit'} and bool(re.fullmatch('[0-9a-f]{40}', sha or '')),
                'invalid GitHub tag target')
        chain.append({'type': kind, 'sha': sha})
        if kind == 'commit':
            commit = api_json(base + 'commits/' + sha, output / 'source-commit.json')
            require(commit.get('sha') == sha, 'source commit identity mismatch')
            return {'tag': TAG, 'chain': chain, 'commit': sha,
                    'tree': commit.get('tree', {}).get('sha')}
        value = api_json(base + 'tags/' + sha, output / ('tag-object-' + str(depth) + '.json'))
        require(value.get('sha') == sha, 'annotated tag identity mismatch')
        current = value.get('object', {})
    raise RuntimeError('annotated tag chain exceeded six levels')


def safe_unpack(archive: Path, destination: Path) -> list[str]:
    destination.mkdir()
    names = []
    total = 0
    with tarfile.open(archive, 'r:gz') as source:
        seen = set()
        for member in source:
            require(len(seen) < MAX_MEMBERS, 'archive member count exceeds limit')
            name = member.name
            parts = PurePosixPath(name).parts
            require(name and '\\' not in name and not name.startswith('/')
                    and not PurePosixPath(name).is_absolute()
                    and '..' not in parts and ':' not in name, 'unsafe archive path')
            require(parts and parts[0] not in {'.', '..'}, 'unsafe archive root')
            require(member.isdir() or member.isfile(),
                    'archive contains a link or special file')
            require(name not in seen, 'archive repeats a member path')
            seen.add(name)
            require(0 <= member.size <= MAX_FILE, 'archive member exceeds byte limit')
            total += member.size
            require(total <= MAX_UNPACKED, 'archive exceeds unpacked byte limit')
            target = destination.joinpath(*parts)
            require(target.resolve().is_relative_to(destination.resolve()),
                    'archive escaped target directory')
            names.append(name)
            if member.isdir():
                target.mkdir(parents=True, exist_ok=True)
            else:
                target.parent.mkdir(parents=True, exist_ok=True)
                handle = source.extractfile(member)
                require(handle is not None, 'regular archive member could not be opened')
                with handle, target.open('xb') as output:
                    remaining = member.size
                    while remaining:
                        chunk = handle.read(min(1024 * 1024, remaining))
                        require(bool(chunk), 'archive member was truncated')
                        output.write(chunk)
                        remaining -= len(chunk)
                target.chmod(0o755 if member.mode & 0o111 else 0o644)
    return names


def tree_manifest(directory: Path) -> dict:
    files = []
    for item in sorted(directory.rglob('*')):
        require(not item.is_symlink(), 'source tree contains a symbolic link')
        if item.is_file():
            files.append({'path': item.relative_to(directory).as_posix(),
                          'bytes': item.stat().st_size, 'sha256': digest(item)})
        else:
            require(item.is_dir(), 'source tree contains a special file')
    payload = json.dumps(files, sort_keys=True, separators=(',', ':')).encode()
    return {'sha256': hashlib.sha256(payload).hexdigest(), 'files': files}


def run(arguments: list[str], cwd: Path, env: dict, log: Path, timeout: int = 900) -> str:
    try:
        completed = subprocess.run(arguments, cwd=cwd, env=env, stdout=subprocess.PIPE,
                                   stderr=subprocess.STDOUT, timeout=timeout, check=False)
    except subprocess.TimeoutExpired as error:
        # communicate() has already collected partial output before killing the
        # timed-out command. Preserve it without treating the build as complete.
        log.write_bytes(error.output or b'')
        raise
    log.write_bytes(completed.stdout)
    require(completed.returncode == 0,
            'command failed with exit ' + str(completed.returncode) + ': ' +
            ' '.join(arguments[:3]) + '; see ' + str(log))
    return completed.stdout.decode('utf-8', errors='strict')


def check_go_mod(document: dict) -> None:
    require(not document.get('Replace'), 'pinned source go.mod unexpectedly contains replace')
    dependencies = {item['Path']: item['Version'] for item in document.get('Require', [])}
    require(dependencies.get(X_MODULE) == X_VERSION, 'source x dependency does not match pin')
    require(dependencies.get(QUIC_MODULE) == QUIC_VERSION,
            'source quic-go dependency does not match pin')
    require(dependencies.get(SMUX_MODULE) == SMUX_VERSION,
            'source smux dependency does not match pin')


def build_info(text: str, local_x: Path | None, local_smux: Path | None = None) -> dict:
    dependencies = {}
    replacements = {}
    current = None
    for raw in text.splitlines():
        fields = raw.strip().split()
        if not fields:
            continue
        if fields[0] == 'dep':
            require(len(fields) >= 3, 'malformed binary dependency info')
            current = fields[1]
            dependencies[current] = fields[2]
        elif fields[0] == '=>':
            require(current is not None and len(fields) >= 2, 'malformed replacement info')
            replacements[current] = fields[1:]
        elif fields[0] not in {'mod'}:
            current = None
    require(dependencies.get(X_MODULE) == X_VERSION, 'binary x dependency mismatch')
    require(dependencies.get(QUIC_MODULE) == QUIC_VERSION, 'binary quic-go dependency mismatch')
    require(dependencies.get(SMUX_MODULE) == SMUX_VERSION, 'binary smux dependency mismatch')
    if local_x is None:
        require(not replacements, 'official binary unexpectedly contains module replacements')
    else:
        expected = {X_MODULE} | ({SMUX_MODULE} if local_smux is not None else set())
        require(set(replacements) == expected, 'research binary contains unexpected replacements')
        require(Path(replacements[X_MODULE][0]).resolve() == local_x.resolve(),
                'research binary x replacement is not the pinned source copy')
        if local_smux is not None:
            require(Path(replacements[SMUX_MODULE][0]).resolve() == local_smux.resolve(),
                    'research binary smux replacement is not the pinned source copy')
    return {'dependencies': dependencies, 'replacements': replacements}


def lifecycle_overlay(output: Path, local_x: Path, pipe: Path, fixed: Path) -> dict:
    """Test one additional hypothesis without changing the Pipe-only control.

    quic.Stream.Context ends when its send direction closes. The service uses
    Conn.Context as the parent of both pipe directions. Bind this test adapter
    to the QUIC connection context so a normal local FIN cannot cancel reverse
    application traffic. This does not implement cancellation/abort hardening.
    """
    changes = (
        ('listener/quic/conn.go', (
            ('import (\n', 'import (\n\t"context"\n'),
            ('\t*quic.Stream\n', '\t*quic.Stream\n\tsessionContext context.Context\n'),
            ('func (c *quicConn) LocalAddr()',
             'func (c *quicConn) Context() context.Context {\n'
             '\treturn c.sessionContext\n}\n\nfunc (c *quicConn) LocalAddr()'),
        )),
        ('listener/quic/listener.go', (
            ('\t\t\tStream: stream,\n',
             '\t\t\tStream: stream,\n\t\t\tsessionContext: session.Context(),\n'),
        )),
    )
    replacements = {str(pipe): str(fixed)}
    records = []
    for relative, edits in changes:
        source = local_x / relative
        before = source.read_bytes()
        patched = before
        for old, new in edits:
            old, new = old.encode(), new.encode()
            require(patched.count(old) == 1, 'lifecycle exact patch failed: ' + relative)
            patched = patched.replace(old, new, 1)
        destination = output / ('lifecycle-' + relative.replace('/', '-'))
        destination.write_bytes(patched)
        replacements[str(source)] = str(destination)
        records.append({'path': relative, 'original_sha256': hashlib.sha256(before).hexdigest(),
                        'candidate_sha256': digest(destination), 'exact_edits': len(edits)})
    return {'overlay': {'Replace': replacements}, 'patches': records,
            'hypothesis': 'normal QUIC send FIN must not cancel both forwarding directions',
            'scope': 'isolated candidate; does not establish abort or idle resource cleanup'}


def halfclose_overlay(output: Path, local_x: Path, lifecycle: dict) -> dict:
    """Expose smux half-close through both buffered relay TCP adapters.

    The relay header must reach the peer before FIN, including a connection
    closed before its first payload. Serialize that flush with normal writes.
    Unsupported transports retain Pipe's existing full-close fallback.
    """
    replacements = dict(lifecycle['overlay']['Replace'])
    records = []
    for side in ('connector', 'handler'):
        relative = side + '/relay/conn.go'
        source = local_x / relative
        before = source.read_bytes()
        edits = [('\t"net"\n', '\t"net"\n\txio "github.com/go-gost/x/internal/io"\n')]
        if side == 'handler':
            edits.extend([
                ('\t"net"\n', '\t"net"\n\t"sync"\n'),
                ('type tcpConn struct {\n\tnet.Conn\n\twbuf bytes.Buffer\n}',
                 'type tcpConn struct {\n\tnet.Conn\n\twbuf bytes.Buffer\n\tmu sync.Mutex\n}'),
                ('func (c *tcpConn) Write(b []byte) (n int, err error) {\n',
                 'func (c *tcpConn) Write(b []byte) (n int, err error) {\n'
                 '\tc.mu.Lock()\n\tdefer c.mu.Unlock()\n'),
            ])
        pending = 'c.wbuf != nil && c.wbuf.Len() > 0' if side == 'connector' else 'c.wbuf.Len() > 0'
        method = ('func (c *tcpConn) CloseWrite() error {\n'
                  '\tc.mu.Lock()\n\tdefer c.mu.Unlock()\n'
                  '\tif ' + pending + ' {\n'
                  '\t\tif _, err := c.wbuf.WriteTo(c.Conn); err != nil {\n'
                  '\t\t\treturn err\n\t\t}\n\t}\n'
                  '\tif cw, ok := c.Conn.(xio.CloseWrite); ok {\n'
                  '\t\treturn cw.CloseWrite()\n\t}\n'
                  '\treturn xio.ErrUnsupported\n}\n\n')
        edits.append(('type udpConn struct {', method + 'type udpConn struct {'))
        patched = before
        for old, new in edits:
            require(patched.count(old.encode()) == 1, 'half-close exact patch failed: ' + relative)
            patched = patched.replace(old.encode(), new.encode(), 1)
        destination = output / ('halfclose-' + relative.replace('/', '-'))
        destination.write_bytes(patched)
        replacements[str(source)] = str(destination)
        records.append({'path': relative, 'original_sha256': hashlib.sha256(before).hexdigest(),
                        'candidate_sha256': digest(destination), 'exact_edits': len(edits)})
    return {'overlay': {'Replace': replacements}, 'patches': records,
            'parent_candidate': 'lifecycle',
            'hypothesis': 'buffered relay TCP adapters must expose transport CloseWrite after header flush',
            'scope': 'isolated half-close candidate; cancellation and abort hardening remain separate'}


def drain_overlay(output: Path, local_smux: Path, halfclose: dict) -> dict:
    """Retain ordered payload after both FINs until the reader drains it.

    smux v1.5.57 immediately removes a doubly half-closed stream. Its session
    removal recycles unread buffers, turning a valid ordered FIN into data loss.
    Defer that removal while data remains; retry after successful consumption.
    Explicit Close retains the upstream discard-and-recycle behavior.
    """
    source = local_smux / 'stream.go'
    before = source.read_bytes()
    require(digest(source) == SMUX_STREAM_SHA256, 'smux stream SHA256 gate failed')
    edits = [
        ('\t// return tokens to session to allow more data to be received\n'
         '\tif n > 0 {\n\t\ts.sess.returnTokens(n)\n\t\treturn n, nil\n\t}',
         '\t// return tokens to session to allow more data to be received\n'
         '\tif n > 0 {\n\t\ts.sess.returnTokens(n)\n'
         '\t\ts.tryHalfCloseCleanup()\n\t\treturn n, nil\n\t}', 1),
        ('\t\t// send window update if necessary\n\t\tif notifyConsumed > 0 {\n'
         '\t\t\treturn n, s.sendWindowUpdate(notifyConsumed)\n\t\t}\n\t\treturn n, nil',
         '\t\t// send window update if necessary\n\t\tif notifyConsumed > 0 {\n'
         '\t\t\terr = s.sendWindowUpdate(notifyConsumed)\n\t\t}\n'
         '\t\ts.tryHalfCloseCleanup()\n\t\treturn n, err', 1),
        ('\t\t} else if ew := s.waitRead(); ew != nil {\n\t\t\treturn n, ew\n\t\t}',
         '\t\t\ts.tryHalfCloseCleanup()\n'
         '\t\t} else if ew := s.waitRead(); ew != nil {\n\t\t\treturn n, ew\n\t\t}', 2),
        ('\tcase <-s.die:\n\t\treturn io.ErrClosedPipe\n\t}\n\n}\n\n// checkWriteClosed',
         '\tcase <-s.die:\n\t\tselect {\n\t\tcase <-s.chFinEvent:\n'
         '\t\t\treturn io.EOF\n\t\tdefault:\n\t\t\treturn io.ErrClosedPipe\n'
         '\t\t}\n\t}\n\n}\n\n// checkWriteClosed', 1),
        ('\ts.dieOnce.Do(func() {\n\t\tclose(s.die)\n\t})\n'
         '\ts.sess.streamClosed(s.id)\n}\n\n// stopTimer',
         '\t// FIN is ordered behind payload: retain queued data until consumed.\n'
         '\ts.bufferLock.Lock()\n\tpending := s.bufferRing.len() > 0\n'
         '\ts.bufferLock.Unlock()\n\tif pending {\n\t\treturn\n\t}\n\n'
         '\ts.dieOnce.Do(func() {\n\t\tclose(s.die)\n\t})\n'
         '\ts.sess.streamClosed(s.id)\n}\n\n// stopTimer', 1),
    ]
    patched = before
    for old, new, expected in edits:
        require(patched.count(old.encode()) == expected, 'drain exact patch failed: stream.go')
        patched = patched.replace(old.encode(), new.encode())
    destination = output / 'drain-smux-stream.go'
    destination.write_bytes(patched)
    replacements = dict(halfclose['overlay']['Replace'])
    replacements[str(source)] = str(destination)
    return {'overlay': {'Replace': replacements}, 'parent_candidate': 'halfclose',
            'patches': [{'module': SMUX_MODULE, 'version': SMUX_VERSION, 'path': 'stream.go',
                         'original_sha256': hashlib.sha256(before).hexdigest(),
                         'candidate_sha256': digest(destination),
                         'exact_edits': sum(edit[2] for edit in edits)}],
            'hypothesis': 'ordered FIN must not recycle unread smux payload',
            'scope': 'isolated drain candidate; abort hardening remains separate'}


def main() -> None:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('output_directory', type=Path)
    args = parser.parse_args()
    require(sys.platform == 'linux' and platform.machine().lower() in {'x86_64', 'amd64'},
            'research build is restricted to Linux amd64')
    require(shutil.which('go') is not None, 'Go is required')
    output = args.output_directory.absolute()
    require(not output.is_symlink(), 'output must not be a symbolic link')
    require(not output.exists() or (output.is_dir() and not any(output.iterdir())),
            'output directory must be absent or empty')
    output.mkdir(parents=True, exist_ok=True)
    output = output.resolve()
    env = os.environ.copy()
    # Isolate all Go writes; both builds use the exact same local replacement.
    for variable in ('GOFLAGS', 'GOWORK', 'GOPATH', 'GOMODCACHE', 'GOCACHE', 'GOENV',
                     'GOOS', 'GOARCH', 'GOEXPERIMENT', 'GOTOOLCHAIN', 'GOTMPDIR',
                     'GONOPROXY', 'GONOSUMDB', 'GOPRIVATE', 'GOINSECURE'):
        env.pop(variable, None)
    env.update({'CGO_ENABLED': '0', 'GOOS': 'linux', 'GOARCH': 'amd64',
                'GO111MODULE': 'on', 'GOSUMDB': 'sum.golang.org',
                'GOPROXY': 'https://proxy.golang.org,direct',
                'GOWORK': 'off', 'GOENV': 'off', 'GOTOOLCHAIN': 'auto',
                'GOPATH': str(output / 'go'),
                'GOMODCACHE': str(output / 'go' / 'pkg' / 'mod'),
                'GOCACHE': str(output / 'go-build-cache')})
    provenance = {'schema': 1, 'purpose': 'isolated original, Pipe-only and QUIC lifecycle research',
                  'production_changes': False, 'engine_tests_run': False,
                  'tag': TAG, 'script_sha256': digest(Path(__file__).resolve()),
                  'environment': {key: env[key] for key in
                                  ('CGO_ENABLED', 'GOOS', 'GOARCH', 'GOWORK', 'GOENV',
                                   'GOTOOLCHAIN', 'GOPATH', 'GOMODCACHE', 'GOCACHE',
                                   'GO111MODULE', 'GOSUMDB', 'GOPROXY')}}
    try:
        provenance['source'] = resolve_commit(output)
        archive = output / 'gost-source.tar.gz'
        commit = provenance['source']['commit']
        provenance['source_commit'] = commit
        provenance['source']['archive'] = download(
            'https://codeload.github.com/' + REPOSITORY + '/tar.gz/' + commit, archive)
        source_container = output / 'source'
        safe_unpack(archive, source_container)
        roots = list(source_container.iterdir())
        require(len(roots) == 1 and roots[0].is_dir(), 'source archive must contain one root')
        source = roots[0]
        require((source / 'go.mod').is_file(), 'source archive is missing go.mod')
        provenance['source']['directory'] = str(source)
        provenance['go_version'] = run(['go', 'version'], source, env, output / 'go-version.txt')
        document = json.loads(run(['go', 'mod', 'edit', '-json'], source, env,
                                  output / 'source-go-mod.json'))
        check_go_mod(document)
        source_manifest = tree_manifest(source)
        write_json(output / 'source-manifest.json', source_manifest)
        provenance['source']['tree_sha256'] = source_manifest['sha256']
        provenance['source']['go_mod_sha256'] = digest(source / 'go.mod')
        provenance['source']['go_sum_sha256'] = digest(source / 'go.sum')

        # Authenticate the fixed upstream module in an isolated module cache.
        downloaded = json.loads(run(['go', 'mod', 'download', '-json', X_MODULE + '@' + X_VERSION],
                                    source, env, output / 'x-download.json'))
        require(downloaded.get('Path') == X_MODULE and downloaded.get('Version') == X_VERSION,
                'downloaded x module identity mismatch')
        require(all(downloaded.get(key) for key in ('Sum', 'GoModSum', 'Zip', 'GoMod', 'Dir')),
                'downloaded x provenance is incomplete')
        module_cache = Path(env['GOMODCACHE']).resolve()
        for key in ('Zip', 'GoMod', 'Dir'):
            require(Path(downloaded[key]).resolve().is_relative_to(module_cache),
                    'Go downloaded outside isolated module cache')
        provenance['x_module'] = {key: downloaded[key] for key in
                                 ('Path', 'Version', 'Sum', 'GoModSum')}
        provenance['x_module']['zip_sha256'] = digest(Path(downloaded['Zip']))
        provenance['x_module']['go_mod_sha256'] = digest(Path(downloaded['GoMod']))
        upstream_x = Path(downloaded['Dir'])
        upstream_manifest = tree_manifest(upstream_x)
        write_json(output / 'x-upstream-manifest.json', upstream_manifest)
        local_x = output / 'x-source'
        shutil.copytree(upstream_x, local_x, symlinks=False)
        local_manifest = tree_manifest(local_x)
        require(local_manifest == upstream_manifest, 'x source copy differs from authenticated module')
        write_json(output / 'x-source-manifest.json', local_manifest)
        provenance['x_module']['source_tree_sha256'] = local_manifest['sha256']
        downloaded_smux = json.loads(run(
            ['go', 'mod', 'download', '-json', SMUX_MODULE + '@' + SMUX_VERSION],
            source, env, output / 'smux-download.json'))
        require(downloaded_smux.get('Path') == SMUX_MODULE
                and downloaded_smux.get('Version') == SMUX_VERSION,
                'downloaded smux module identity mismatch')
        require(all(downloaded_smux.get(key) for key in ('Sum', 'GoModSum', 'Zip', 'GoMod', 'Dir')),
                'downloaded smux provenance is incomplete')
        for key in ('Zip', 'GoMod', 'Dir'):
            require(Path(downloaded_smux[key]).resolve().is_relative_to(module_cache),
                    'Go downloaded smux outside isolated module cache')
        provenance['smux_module'] = {key: downloaded_smux[key] for key in
                                    ('Path', 'Version', 'Sum', 'GoModSum')}
        provenance['smux_module']['zip_sha256'] = digest(Path(downloaded_smux['Zip']))
        provenance['smux_module']['go_mod_sha256'] = digest(Path(downloaded_smux['GoMod']))
        upstream_smux = Path(downloaded_smux['Dir'])
        smux_manifest = tree_manifest(upstream_smux)
        write_json(output / 'smux-upstream-manifest.json', smux_manifest)
        local_smux = output / 'smux-source'
        shutil.copytree(upstream_smux, local_smux, symlinks=False)
        require(tree_manifest(local_smux) == smux_manifest,
                'smux source copy differs from authenticated module')
        provenance['smux_module']['source_tree_sha256'] = smux_manifest['sha256']
        pipe = local_x / 'internal' / 'net' / 'pipe.go'
        require(digest(pipe) == PIPE_SHA256, 'original Pipe SHA256 gate failed')
        original = pipe.read_bytes()
        old, new = PIPE_OLD.encode('utf-8'), PIPE_NEW.encode('utf-8')
        require(original.count(old) == 1, 'exact verified Pipe patch did not match once')
        fixed = output / 'fixed-pipe.go'
        fixed.write_bytes(original.replace(old, new, 1))
        provenance['pipe'] = {'original_sha256': digest(pipe), 'fixed_sha256': digest(fixed),
                              'patch_matches': 1, 'overlay_target': str(pipe),
                              'overlay_source': str(fixed)}
        original_overlay = output / 'original-overlay.json'
        fixed_overlay = output / 'fixed-overlay.json'
        write_json(original_overlay, {'Replace': {}})
        write_json(fixed_overlay, {'Replace': {str(pipe): str(fixed)}})
        lifecycle = lifecycle_overlay(output, local_x, pipe, fixed)
        lifecycle_path = output / 'lifecycle-overlay.json'
        write_json(lifecycle_path, lifecycle['overlay'])
        provenance['lifecycle_candidate'] = lifecycle
        halfclose = halfclose_overlay(output, local_x, lifecycle)
        halfclose_path = output / 'halfclose-overlay.json'
        write_json(halfclose_path, halfclose['overlay'])
        provenance['halfclose_candidate'] = halfclose
        drain = drain_overlay(output, local_smux, halfclose)
        drain_path = output / 'drain-overlay.json'
        write_json(drain_path, drain['overlay'])
        provenance['drain_candidate'] = drain
        write_json(output / 'drain-candidate.json', drain)
        abort = abort_overlay(output / 'abort-patch', local_x, dict(drain, candidate='drain'))
        abort_path = output / 'abort-patch' / 'overlay.json'
        provenance['abort_candidate'] = abort
        provenance['abort_helper_sha256'] = digest(Path(__file__).with_name('udp_abort_overlay.py'))
        dial = dial_overlay(output / 'dial-patch', local_x, abort)
        dial_path = output / 'dial-patch' / 'overlay.json'
        provenance['dial_candidate'] = dial
        provenance['dial_helper_sha256'] = digest(Path(__file__).with_name('udp_dial_overlay.py'))
        write_json(output / 'dial-candidate.json', dial)
        require(not any(path.endswith('_test.go') for path in dial['overlay']['Replace']),
                'Dial binary overlay must not contain contract test mappings')

        # -overlay cannot replace files in GOMODCACHE. Both builds therefore
        # share a byte-identical x copy and the same temporary -modfile replace.
        modfile = output / 'research.mod'
        sumfile = output / 'research.sum'
        shutil.copyfile(source / 'go.mod', modfile)
        shutil.copyfile(source / 'go.sum', sumfile)
        run(['go', 'mod', 'edit', '-modfile=' + str(modfile),
             '-replace=' + X_MODULE + '=' + str(local_x),
             '-replace=' + SMUX_MODULE + '=' + str(local_smux)],
            source, env, output / 'mod-edit.txt')
        run(['go', 'mod', 'download', '-modfile=' + str(modfile), 'all'],
            source, env, output / 'mod-download-all.txt', 1200)
        run(['go', 'mod', 'verify'], source, env, output / 'mod-verify-before.txt', 600)
        # Resolve the complete research graph before compilation; -mod=readonly
        # makes either build fail if its temporary module files need changing.
        run(['go', 'list', '-mod=readonly', '-modfile=' + str(modfile), '-m', 'all'],
            source, env, output / 'research-module-graph.txt', 600)
        modfile_hash, sumfile_hash = digest(modfile), digest(sumfile)
        provenance['research_module'] = {'modfile': str(modfile), 'sha256': modfile_hash,
                                         'sumfile_sha256': sumfile_hash,
                                         'identical_replace_for_all': {X_MODULE: str(local_x),
                                                                       SMUX_MODULE: str(local_smux)}}
        provenance['builds'] = {}
        provenance['variants'] = {}
        for label, overlay in [('original', original_overlay), ('fixed', fixed_overlay),
                               ('lifecycle', lifecycle_path), ('halfclose', halfclose_path),
                               ('drain', drain_path), ('abort', abort_path), ('dial', dial_path)]:
            destination = output / label
            destination.mkdir()
            binary = destination / 'gost'
            command = ['go', 'build', '-mod=readonly', '-modfile=' + str(modfile),
                       '-trimpath', '-buildvcs=false', '-overlay=' + str(overlay),
                       '-o', str(binary), './cmd/gost']
            started = time.monotonic()
            run(command, source, env, destination / 'build.log', 1800)
            seconds = time.monotonic() - started
            information = run(['go', 'version', '-m', str(binary)], source, env,
                              destination / 'go-version-m.txt')
            parsed = build_info(information, local_x, local_smux)
            require(digest(modfile) == modfile_hash and digest(sumfile) == sumfile_hash,
                    'temporary module files changed during build')
            require(tree_manifest(local_x) == local_manifest, 'x source changed during build')
            require(tree_manifest(local_smux) == smux_manifest, 'smux source changed during build')
            require(tree_manifest(source) == source_manifest, 'GOST source changed during build')
            record = {'label': label, 'binary': str(binary), 'sha256': digest(binary),
                      'bytes': binary.stat().st_size, 'build_seconds': round(seconds, 3),
                      'command': command, 'buildinfo': parsed, 'overlay': json.loads(
                          overlay.read_text(encoding='utf-8')),
                      'source_tree_sha256': source_manifest['sha256'],
                      'x_source_tree_sha256': local_manifest['sha256'],
                      'smux_source_tree_sha256': smux_manifest['sha256'],
                      'modfile_sha256': modfile_hash, 'sumfile_sha256': sumfile_hash}
            write_json(destination / 'provenance.json', record)
            provenance['builds'][label] = record
            provenance['variants'][label] = {
                'binary': str(binary), 'sha256': record['sha256'],
                'buildinfo': information, 'buildinfo_parsed': parsed}
        require(all(record['buildinfo'] == provenance['builds']['original']['buildinfo']
                    for record in provenance['builds'].values()), 'build dependency graphs differ')
        run(['go', 'mod', 'verify'], source, env, output / 'mod-verify-after.txt', 600)
        require(tree_manifest(upstream_x) == upstream_manifest, 'module cache source was modified')
        require(tree_manifest(upstream_smux) == smux_manifest, 'smux module cache was modified')

        # Inspect the actual official distribution without executing its engine.
        official_archive = output / 'gost-official.tar.gz'
        official_download = download(OFFICIAL_URL, official_archive)
        require(official_download['sha256'] == OFFICIAL_SHA256,
                'official release archive SHA256 gate failed')
        official = output / 'official'
        safe_unpack(official_archive, official)
        candidates = [item for item in official.rglob('gost') if item.is_file()]
        require(len(candidates) == 1, 'official archive must contain exactly one gost executable')
        official_binary = candidates[0]
        info = run(['go', 'version', '-m', str(official_binary)], source, env,
                   official / 'go-version-m.txt')
        official_record = {'download': official_download, 'binary': str(official_binary),
                           'sha256': digest(official_binary),
                           'buildinfo': build_info(info, None)}
        write_json(official / 'provenance.json', official_record)
        provenance['official'] = official_record
        provenance['variants']['official'] = {
            'binary': str(official_binary), 'sha256': official_record['sha256'],
            'buildinfo': info, 'buildinfo_parsed': official_record['buildinfo']}
        provenance['status'] = 'complete'
        write_json(output / 'provenance.json', provenance)
        print(json.dumps({'status': 'complete', 'output_directory': str(output),
                          'commit': commit, 'binaries': {key: item['binary'] for key, item in
                          provenance['builds'].items()}, 'provenance': str(output / 'provenance.json')}))
    except Exception as exc:
        provenance['status'] = 'failed'
        provenance['error_type'] = type(exc).__name__
        # Error strings contain paths/commands only; no environment values or credentials.
        provenance['error'] = str(exc)
        write_json(output / 'provenance.json', provenance)
        raise


if __name__ == '__main__':
    main()
