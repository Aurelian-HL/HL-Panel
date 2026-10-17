#!/usr/bin/env python3
"""Release recipe integrity and fail-closed import contracts; no network."""
import contextlib
import hashlib
import importlib.util
import io
import json
from pathlib import Path
import shutil
import struct
import sys
import tarfile
import tempfile
import unittest
from unittest.mock import patch
import zipfile

REPO = Path(__file__).resolve().parents[2]
sys.path.insert(0, str(REPO / 'deploy'))
import reliable_udp_engine as engine
spec = importlib.util.spec_from_file_location('fetch_engines', REPO / 'deploy/fetch-node-engines.py')
fetch = importlib.util.module_from_spec(spec)
spec.loader.exec_module(fetch)


class ReleaseEngineTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name)
        self.recipe = self.root / 'recipe'
        shutil.copytree(engine.RECIPE, self.recipe)
        self.lock = engine.load_recipe(self.recipe)

    def bundle(self):
        root = self.root / 'bundle'
        root.mkdir()
        binary = bytearray(64)
        binary[:6] = b'\x7fELF\x02\x01'
        struct.pack_into('<H', binary, 18, 62)
        (root / 'gost').write_bytes(binary)
        (root / 'LICENSE').write_bytes(b'test license')
        for key in ['x', 'smux']:
            shutil.copyfile(self.recipe / (key + '-LICENSE'), root / (key + '-LICENSE'))
        info = 'gost: go1.27.0\n'
        for key, relative in [('x', './hl-udp-x'), ('smux', './hl-udp-smux')]:
            module = self.lock['modules'][key]
            info += '\tdep\t' + module['Path'] + '\t' + module['Version'] + '\n'
            info += '\t=>\t' + relative + '\t(devel)\n'
        info += '\tdep\tgithub.com/quic-go/quic-go\tv0.60.0\n'
        info += '\tbuild\tGOOS=linux\n\tbuild\tGOARCH=amd64\n\tbuild\tCGO_ENABLED=0\n'
        (root / 'buildinfo.txt').write_text(info, encoding='utf-8')
        manifest = {'schema': 1, 'target': self.lock['target'], 'version': self.lock['version'],
                    'source_commit': self.lock['source']['commit'],
                    'recipe_sha256': engine.sha256(self.recipe / 'lock.json'),
                    'files': {name: {'sha256': engine.sha256(root / name), 'bytes': (root / name).stat().st_size}
                              for name in ['gost', 'LICENSE', 'x-LICENSE', 'smux-LICENSE', 'buildinfo.txt']}}
        engine.write_json(root / 'manifest.json', manifest)
        return root, engine.sha256(root / 'manifest.json')

    def test_recipe_exact_patch_inventory(self):
        self.assertEqual(len(self.lock['patches']), 13)
        self.assertFalse(any(item['path'].endswith('_test.go') for item in self.lock['patches']))
        self.assertTrue(all(item['patch'].endswith('.go.src') for item in self.lock['patches']))
        self.assertFalse(any((self.recipe / item['patch']).name.endswith('.go')
                             for item in self.lock['patches']))

    def test_modified_patch_is_rejected(self):
        (self.recipe / self.lock['patches'][0]['patch']).write_bytes(b'changed')
        with self.assertRaisesRegex(engine.EngineBuildError, 'patch checksum'):
            engine.load_recipe(self.recipe)

    def test_modified_module_file_is_rejected(self):
        (self.recipe / 'candidate.mod').write_bytes(b'changed')
        with self.assertRaisesRegex(engine.EngineBuildError, 'module file checksum'):
            engine.load_recipe(self.recipe)

    def test_recipe_rejects_traversal_and_tests(self):
        for path in ['../bad.go', 'dialer_test.go']:
            self.lock['patches'][0]['path'] = path
            engine.write_json(self.recipe / 'lock.json', self.lock)
            with self.assertRaisesRegex(engine.EngineBuildError, 'patch path'):
                engine.load_recipe(self.recipe)

    def test_verify_bundle(self):
        root, digest = self.bundle()
        self.assertEqual(engine.verify_bundle(root, digest, self.recipe)['version'], self.lock['version'])

    def test_bundle_content_tampering_rejected(self):
        root, digest = self.bundle()
        (root / 'gost').write_bytes(b'changed')
        with self.assertRaisesRegex(engine.EngineBuildError, 'checksum mismatch'):
            engine.verify_bundle(root, digest, self.recipe)

    def test_bundle_manifest_tampering_rejected(self):
        root, digest = self.bundle()
        (root / 'manifest.json').write_bytes(b'{}')
        with self.assertRaisesRegex(engine.EngineBuildError, 'manifest checksum'):
            engine.verify_bundle(root, digest, self.recipe)

    def test_bundle_different_target_rejected(self):
        root, _ = self.bundle()
        manifest = json.loads((root / 'manifest.json').read_text())
        manifest['target'] = 'linux/arm64'
        engine.write_json(root / 'manifest.json', manifest)
        with self.assertRaisesRegex(engine.EngineBuildError, 'provenance'):
            engine.verify_bundle(root, engine.sha256(root / 'manifest.json'), self.recipe)

    def test_wrong_elf_architecture_rejected(self):
        root, _ = self.bundle()
        raw = bytearray((root / 'gost').read_bytes())
        struct.pack_into('<H', raw, 18, 183)
        (root / 'gost').write_bytes(raw)
        with self.assertRaisesRegex(engine.EngineBuildError, 'Linux amd64'):
            engine.verify_elf(root / 'gost')

    def test_unexpected_dependency_replacement_rejected(self):
        root, _ = self.bundle()
        info = (root / 'buildinfo.txt').read_text() + '\tdep\tevil.example/module\tv1\n\t=>\t./evil\t(devel)\n'
        with self.assertRaisesRegex(engine.EngineBuildError, 'unexpected'):
            engine.validate_buildinfo(info, self.lock)

    def test_archive_traversal_rejected(self):
        archive = self.root / 'bad.tar.gz'
        with tarfile.open(archive, 'w:gz') as handle:
            member = tarfile.TarInfo('../escape')
            member.size = 1
            handle.addfile(member, io.BytesIO(b'x'))
        with self.assertRaisesRegex(engine.EngineBuildError, 'unsafe'):
            engine.unpack_source(archive, self.root / 'output')
        self.assertFalse((self.root / 'escape').exists())

    def test_hash_pair_required_before_network_or_output(self):
        with patch.object(fetch.urllib.request, 'urlopen') as request:
            for bundle, digest in [(self.root, None), (None, 'a' * 64)]:
                with self.assertRaisesRegex(fetch.EngineBuildError, 'together'):
                    fetch.install_engines(self.root / 'release', bundle, digest)
            request.assert_not_called()
        self.assertFalse((self.root / 'release').exists())

    def test_invalid_bundle_rejected_before_official_download(self):
        root, digest = self.bundle()
        (root / 'LICENSE').write_bytes(b'tampered')
        with patch.object(fetch.urllib.request, 'urlopen') as request:
            with self.assertRaises(engine.EngineBuildError):
                fetch.install_engines(self.root / 'release', root, digest)
            request.assert_not_called()

    def official_archive(self, name):
        data = io.BytesIO()
        with zipfile.ZipFile(data, 'w') as handle:
            handle.writestr(name, b'official-' + name.encode())
            handle.writestr('LICENSE', b'official-license')
        raw = data.getvalue()
        return {'name': name, 'version': 'test', 'format': 'zip', 'url': 'https://example.invalid/' + name,
                'sha256': hashlib.sha256(raw).hexdigest()}, raw

    def test_default_install_keeps_official_engines(self):
        xray, xray_zip = self.official_archive('xray')
        gost, gost_zip = self.official_archive('gost')
        with patch.object(fetch, 'ENGINES', [xray, gost]), \
                patch.object(fetch.urllib.request, 'urlopen', side_effect=[io.BytesIO(xray_zip), io.BytesIO(gost_zip)]):
            fetch.install_engines(self.root / 'release')
        self.assertEqual((self.root / 'release/bin/gost').read_bytes(), b'official-gost')

    def test_verified_candidate_does_not_download_official_gost(self):
        root, digest = self.bundle()
        xray, archive = self.official_archive('xray')
        gost, _ = self.official_archive('gost')
        with patch.object(fetch, 'ENGINES', [xray, gost]), \
                patch.object(fetch.urllib.request, 'urlopen', return_value=io.BytesIO(archive)) as request:
            fetch.install_engines(self.root / 'release', root, digest)
            self.assertEqual(request.call_count, 1)
        self.assertEqual((self.root / 'release/bin/gost').read_bytes(), (root / 'gost').read_bytes())
        metadata = json.loads((self.root / 'release/licenses/node-engines.json').read_text())
        self.assertEqual(metadata[1]['manifest_sha256'], digest)

    def test_checksum_validation_survives_python_optimization(self):
        archive, data = self.official_archive('xray')
        archive['sha256'] = '0' * 64
        with patch.object(fetch, 'ENGINES', [archive]), \
                patch.object(fetch.urllib.request, 'urlopen', return_value=io.BytesIO(data)):
            with self.assertRaisesRegex(fetch.EngineBuildError, 'checksum mismatch'):
                fetch.install_engines(self.root / 'release')


if __name__ == '__main__':
    unittest.main()
