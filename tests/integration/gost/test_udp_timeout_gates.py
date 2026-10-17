"""Portable offline gates for the independently pinned KCP timeout overlay.

These verify overlay boundaries and rejection behavior. The separate Go
contracts exercise the real upstream Init/parseMetadata and session config.
"""
from contextlib import ExitStack
import copy
import json
from pathlib import Path
import sys
import tempfile
import unittest
from unittest.mock import patch

ROOT = Path(__file__).resolve().parent
sys.path.insert(0, str(ROOT))
import udp_timeout_overlay as helper


class TimeoutOverlayGates(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory(prefix='udp-timeout-gates-')
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name)
        self.source = self.root / 'source'
        self.targets = {}
        pins = {}
        # Small synthetic sources use explicitly test-scoped digests; the
        # immutable production pin declarations are checked independently.
        for relative in helper.PINNED_ORIGINAL_SHA256:
            anchors = []
            for old, _new in helper.edits(relative):
                if old not in anchors:
                    anchors.append(old)
            target = self.source / relative
            target.parent.mkdir(parents=True, exist_ok=True)
            target.write_text('package kcp\n' + '\n'.join(anchors), encoding='utf-8', newline='\n')
            self.targets[relative] = target
            pins[relative] = helper.sha(target)
        self.pins = pins
        self.stack = ExitStack()
        self.addCleanup(self.stack.close)
        self.stack.enter_context(patch.object(helper, 'PINNED_ORIGINAL_SHA256', pins))
        self.stack.enter_context(patch.object(helper, 'ALLOWED_EFFECTIVE_SHA256',
                                             {name: frozenset({digest}) for name, digest in pins.items()}))

    def assert_unwritten(self, output):
        self.assertTrue(not output.exists() or not list(output.iterdir()))

    def test_fixed_pin_declarations_and_real_contract_files(self):
        body = (ROOT / 'udp_timeout_overlay.py').read_text(encoding='utf-8')
        for digest in (
            'ff9d9cf2386d77cca012b3efb48747cac31ae90854b2d6819f7a0f2ded59db6e',
            '597a52e6a3bfd8c8cf82d40586ee03097ea128ab05fcb8b40af704199e185bdd',
            '2cdd5a6faf9f675b5b1d8aef73dc050eef0415d70d72f161cbdc3ebea066f353',
            '00c2c3b62ed8c751bf1e05911dd6e50e92e4266a2a713db24f95816d336cb319',
        ):
            self.assertIn(digest, body)
        for name in helper.CONTRACTS.values():
            self.assertTrue((ROOT / 'timeout_contracts' / name).is_file())
        self.assertEqual(helper.CANDIDATE_TEST_PATTERN, '^TestKCPTimeout')

    def test_inherits_parent_and_changes_only_four_production_mappings(self):
        replacements = {}
        for relative, target in self.targets.items():
            replacement = self.root / ('parent-' + relative.replace('/', '__'))
            replacement.write_bytes(target.read_bytes())
            replacements[str(target)] = str(replacement)
        foreign = str(self.root / 'smux/session.go')
        deleted = str(self.source / 'unused.go')
        replacements.update({foreign: 'parent-smux.go', deleted: ''})
        parent = {'overlay': {'Replace': replacements}}
        before = copy.deepcopy(parent)
        result = helper.timeout_overlay(self.root / 'out', self.source, parent)
        mappings = result['overlay']['Replace']
        self.assertEqual(parent, before)
        self.assertEqual(len(mappings), 6)
        self.assertEqual(mappings[foreign], 'parent-smux.go')
        self.assertEqual(mappings[deleted], '')
        self.assertFalse(any(key.endswith('_test.go') for key in mappings))
        self.assertEqual(result['contracts'], [])
        for record in result['patches']:
            name = record['path']
            self.assertEqual(record['original_sha256'], self.pins[name])
            self.assertEqual(record['effective_input_sha256'], self.pins[name])
            self.assertEqual(record['candidate_sha256'], helper.sha(Path(record['candidate'])))
            self.assertEqual(helper.sha(self.targets[name]), self.pins[name])
            self.assertEqual(helper.sha(Path(record['effective_input'])), self.pins[name])
        self.assertEqual(json.loads((self.root / 'out/overlay.json').read_text()), result['overlay'])
        self.assertEqual(json.loads((self.root / 'out/provenance.json').read_text()), result)

    def test_opt_in_contracts_add_only_two_test_mappings(self):
        result = helper.build(self.root / 'contracts', self.source, include_tests=True)
        self.assertEqual(len(result['overlay']['Replace']), 6)
        self.assertEqual(len(result['contracts']), 2)
        for contract in result['contracts']:
            self.assertEqual(contract['sha256'], helper.sha(Path(contract['source'])))
            self.assertEqual(result['overlay']['Replace'][contract['target']], contract['source'])

    def test_original_and_parent_drift_deletion_and_repatch_are_rejected_before_writes(self):
        # Use the last target so an implementation that writes while validating
        # earlier targets would leave partial output and fail this gate.
        relative, target = list(self.targets.items())[-1]
        original = target.read_bytes()
        for case in ('original-drift', 'parent-drift', 'parent-delete', 'already-patched'):
            with self.subTest(case=case):
                replacement = self.root / (case + '.go')
                replacement.write_bytes(original)
                if case == 'original-drift':
                    target.write_bytes(original + b'// drift\n')
                elif case == 'parent-drift':
                    replacement.write_bytes(original + b'// drift\n')
                elif case == 'already-patched':
                    body = original.decode('utf-8')
                    for old, new in helper.edits(relative):
                        body = body.replace(old, new, 1)
                    replacement.write_text(body, encoding='utf-8', newline='\n')
                output = self.root / ('out-' + case)
                parent = {'Replace': {str(target): '' if case == 'parent-delete' else str(replacement)}}
                with self.assertRaises(ValueError):
                    helper.build(output, self.source, parent)
                self.assert_unwritten(output)
                target.write_bytes(original)

    def test_missing_and_duplicate_anchor_are_rejected_even_with_authorized_effective_hash(self):
        relative, target = list(self.targets.items())[-1]
        original = target.read_text()
        old, _new = helper.edits(relative)[0]
        for case, modified in (('missing', original.replace(old, '', 1)),
                               ('duplicate', original + old)):
            with self.subTest(case=case):
                replacement = self.root / (case + '.go')
                replacement.write_text(modified, encoding='utf-8', newline='\n')
                allowed = dict(helper.ALLOWED_EFFECTIVE_SHA256)
                allowed[relative] = frozenset({helper.sha(replacement)})
                output = self.root / ('anchor-' + case)
                with patch.object(helper, 'ALLOWED_EFFECTIVE_SHA256', allowed), self.assertRaises(ValueError):
                    helper.build(output, self.source, {'Replace': {str(target): str(replacement)}})
                self.assert_unwritten(output)

    def test_normalized_alias_replaces_once_and_duplicate_alias_is_rejected(self):
        target = next(iter(self.targets.values()))
        alias = str(target.parent / '..' / 'kcp' / target.name)
        result = helper.build(self.root / 'alias', self.source, {'Replace': {alias: str(target)}})
        self.assertNotIn(alias, result['overlay']['Replace'])
        self.assertIn(str(target), result['overlay']['Replace'])
        with self.assertRaises(ValueError):
            helper.build(self.root / 'duplicate-alias', self.source,
                         {'Replace': {str(target): str(target), alias: str(target)}})
        self.assert_unwritten(self.root / 'duplicate-alias')

    def test_unsafe_output_parent_shape_and_contract_collision_are_rejected(self):
        output = self.root / 'nonempty'
        output.mkdir()
        sentinel = output / 'keep'
        sentinel.write_text('unchanged')
        for path in (self.source, self.source / 'inside', output):
            with self.subTest(path=path), self.assertRaises(ValueError):
                helper.build(path, self.source)
        self.assertEqual(sentinel.read_text(), 'unchanged')
        for index, parent in enumerate(({'unexpected': True}, {'Replace': []}, {'Replace': {1: 'bad'}},
                                        {'Replace': {'bad': None}})):
            with self.subTest(parent=parent), self.assertRaises(ValueError):
                helper.build(self.root / ('bad-parent-' + str(index)), self.source, parent)
        contract = self.source / next(iter(helper.CONTRACTS))
        with self.assertRaises(ValueError):
            helper.build(self.root / 'collision', self.source,
                         {'Replace': {str(contract): ''}}, include_tests=True)
        self.assert_unwritten(self.root / 'collision')
        contract.write_text('package kcp\n')
        with self.assertRaises(ValueError):
            helper.build(self.root / 'source-collision', self.source, include_tests=True)
        self.assert_unwritten(self.root / 'source-collision')


if __name__ == '__main__':
    unittest.main(verbosity=2)
