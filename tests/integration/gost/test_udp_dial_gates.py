"""Portable offline Dial overlay, result, timeout, and pair selection gates."""
from contextlib import ExitStack
import copy
import hashlib
import json
from pathlib import Path
import subprocess
import sys
import tempfile
import unittest
from unittest.mock import patch

ROOT = Path(__file__).resolve().parent
sys.path.insert(0, str(ROOT))
import udp_dial_overlay as helper
import run_dial_contracts as runner
import reliable_udp_pair as pair


class DialOverlayGates(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory(prefix='udp-dial-helper-')
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name)
        self.source = self.root / 'x-source'
        self.target = self.source / 'dialer/quic/dialer.go'
        self.target.parent.mkdir(parents=True)
        # Gate behavior uses a small synthetic source with an explicitly scoped
        # test-only digest. Production digest and contract identity are checked
        # separately; no downloaded upstream source is required by these tests.
        self.target.write_text('package quic\n' + helper.OLD + '\n', encoding='utf-8', newline='\n')
        self.digest = helper.sha(self.target)
        self.stack = ExitStack()
        self.addCleanup(self.stack.close)
        self.stack.enter_context(patch.object(helper, 'PINNED_ORIGINAL_SHA256', self.digest))
        self.stack.enter_context(patch.object(helper, 'ALLOWED_EFFECTIVE_SHA256', frozenset({self.digest})))

    def test_source_pins_remain_fixed_and_contract_is_real(self):
        # Inspect immutable source declarations rather than the test-only values.
        text = (ROOT / 'udp_dial_overlay.py').read_text(encoding='utf-8')
        self.assertIn('c5da9a9c89340347deade8beb5d3c7c45c6233a0d7a255a0e2443623caf3f481', text)
        self.assertEqual(helper.sha(ROOT / 'dial_contracts/dialer_cancel_contract_test.go'),
                         '7044eb10ea47cc1307a900a65a48468493616cd727274c2877a901eac010938b')

    def test_default_changes_only_dial_and_inherits_all_other_mappings(self):
        replacement = self.root / 'parent-dialer.go'
        replacement.write_bytes(self.target.read_bytes())
        other = str(self.source / 'internal/net/pipe.go')
        deleted = str(self.source / 'unused.go')
        parent = {'overlay': {'Replace': {str(self.target): str(replacement),
                  other: 'parent-pipe.go', deleted: ''}}}
        before = copy.deepcopy(parent)
        result = helper.dial_overlay(self.root / 'out', self.source, parent)
        mappings = result['overlay']['Replace']
        self.assertEqual(parent, before)
        self.assertEqual(len(mappings), 3)
        self.assertEqual(mappings[other], 'parent-pipe.go')
        self.assertEqual(mappings[deleted], '')
        self.assertFalse(any(key.endswith('_test.go') for key in mappings))
        self.assertIsNone(result['contract'])
        self.assertEqual(result['original_input_sha256'], self.digest)
        self.assertEqual(result['effective_input_sha256'], self.digest)
        self.assertEqual(helper.sha(self.target), self.digest)
        self.assertEqual(helper.sha(replacement), self.digest)
        candidate = Path(result['candidate']).read_text()
        self.assertEqual(candidate.count(helper.NEW), 1)
        self.assertNotIn(helper.OLD, candidate)
        self.assertEqual(json.loads((self.root / 'out/overlay.json').read_text()), result['overlay'])

    def test_original_and_parent_drift_deletion_and_repeated_patch_are_rejected(self):
        for case in ('original-drift', 'parent-drift', 'parent-delete', 'already-fixed'):
            with self.subTest(case=case):
                original = self.target.read_bytes()
                replacement = self.root / (case + '.go')
                replacement.write_bytes(original)
                if case == 'original-drift':
                    self.target.write_bytes(original + b'// drift\n')
                elif case == 'parent-drift':
                    replacement.write_bytes(original + b'// drift\n')
                elif case == 'already-fixed':
                    replacement.write_text(helper.NEW, encoding='utf-8')
                parent = {'Replace': {str(self.target): '' if case == 'parent-delete' else str(replacement)}}
                with self.assertRaises(ValueError):
                    helper.dial_overlay(self.root / case, self.source, parent)
                self.assertFalse((self.root / case).exists())
                self.target.write_bytes(original)

    def test_nonempty_output_is_never_overwritten(self):
        output = self.root / 'out'
        output.mkdir()
        sentinel = output / 'keep'
        sentinel.write_text('unchanged')
        with self.assertRaises(ValueError):
            helper.dial_overlay(output, self.source)
        self.assertEqual(sentinel.read_text(), 'unchanged')


class DialResultGates(unittest.TestCase):
    count = 3
    package = 'github.com/go-gost/x/dialer/quic'

    def events(self):
        events = []
        for name in sorted(runner.expected_tests()):
            for _ in range(self.count):
                events.extend([{'Action': 'run', 'Test': name, 'Package': self.package},
                               {'Action': 'pass', 'Test': name, 'Package': self.package}])
        return events + [{'Action': 'pass', 'Package': self.package}]

    def test_complete_result_set_is_accepted(self):
        with tempfile.TemporaryDirectory(prefix='udp-dial-events-') as temporary:
            path = Path(temporary) / 'log.jsonl'
            path.write_text(''.join(json.dumps(e) + '\n' for e in self.events()), encoding='utf-8')
            self.assertEqual(runner.validate_events(path, self.count), 18)

    def test_partial_duplicate_skip_wrong_package_and_malformed_are_rejected(self):
        first = sorted(runner.expected_tests())[0]
        mutations = {
            'missing-test': lambda e: [x for x in e if x.get('Test') != first],
            'missing-run': lambda e: [x for x in e if not (x.get('Test') == first and x['Action'] == 'run')],
            'duplicate': lambda e: e + [e[1]],
            'skip': lambda e: e + [{'Action': 'skip', 'Package': self.package}],
            'fail': lambda e: e + [{'Action': 'fail', 'Package': self.package}],
            'extra-test': lambda e: e + [{'Action': 'pass', 'Test': 'TestUnknown', 'Package': self.package}],
            'missing-package': lambda e: e[:-1],
            'extra-package': lambda e: e + [e[-1]],
            'wrong-package': lambda e: [dict(x, Package='example.invalid/wrong') for x in e],
            'malformed': lambda e: e,
        }
        for case, mutate in mutations.items():
            with self.subTest(case=case), tempfile.TemporaryDirectory(prefix='udp-dial-reject-') as temporary:
                path = Path(temporary) / 'log.jsonl'
                body = ''.join(json.dumps(e) + '\n' for e in mutate(self.events()))
                path.write_text(body + ('{bad-json\n' if case == 'malformed' else ''), encoding='utf-8')
                with self.assertRaises((RuntimeError, json.JSONDecodeError)):
                    runner.validate_events(path, self.count)

    def test_failed_or_timed_out_dial_keeps_evidence_without_aggregate_result(self):
        for case in ('timeout', 'nonzero', 'incomplete'):
            with self.subTest(case=case), tempfile.TemporaryDirectory(prefix='udp-dial-command-') as temporary:
                root = Path(temporary)
                source = root / 'source'
                source.mkdir()
                (source / 'go.mod').write_text('module example.invalid/test\n')
                output = root / 'result'
                def abort_stub(stacked, *_args, **_kwargs):
                    (stacked / 'x-source').mkdir(parents=True)
                    (stacked / 'contract-overlay').mkdir()
                    (stacked / 'contract-overlay/overlay.json').write_text('{"Replace": {}}')
                    return {'commands': [], 'passed_test_events': 129}
                def command(arguments, **kwargs):
                    if case == 'timeout':
                        raise subprocess.TimeoutExpired(arguments, kwargs['timeout'], output=b'partial-output')
                    return subprocess.CompletedProcess(arguments, 7 if case == 'nonzero' else 0, stdout=b'')
                with patch.object(runner, 'dial_overlay', return_value={}), \
                     patch.object(runner, 'run_abort_contracts', side_effect=abort_stub), \
                     patch.object(runner.subprocess, 'run', side_effect=command):
                    with self.assertRaises((RuntimeError, subprocess.TimeoutExpired)):
                        runner.run_contracts(output, source, {}, count=self.count)
                self.assertFalse((output / 'result.json').exists())
                self.assertTrue((output / 'dial-results.jsonl').is_file())
                commands = json.loads((output / 'commands.json').read_text())
                self.assertEqual(len(commands), 1)
                self.assertEqual(commands[0]['suite'], 'dial')
                self.assertEqual(commands[0]['timed_out'], case == 'timeout')
                self.assertEqual(commands[0]['exit_code'], None if case == 'timeout' else (7 if case == 'nonzero' else 0))


    def test_failed_parent_keeps_commands_and_partial_logs_without_aggregate_result(self):
        for case in ('timeout', 'nonzero', 'incomplete'):
            with self.subTest(case=case), tempfile.TemporaryDirectory(prefix='udp-dial-parent-') as temporary:
                root = Path(temporary)
                source = root / 'source'
                source.mkdir()
                output = root / 'result'
                def abort_failure(stacked, *_args, **_kwargs):
                    stacked.mkdir()
                    (stacked / 'race-results.jsonl').write_bytes(b'partial-parent-output')
                    (stacked / 'commands.json').write_text(json.dumps([{'exit_code': None if case == 'timeout' else 7,
                        'timed_out': case == 'timeout', 'log': 'race-results.jsonl'}]))
                    if case == 'timeout':
                        raise subprocess.TimeoutExpired(['go', 'test'], 900, output=b'partial-parent-output')
                    raise RuntimeError('parent ' + case)
                with patch.object(runner, 'dial_overlay', return_value={}), \
                     patch.object(runner, 'run_abort_contracts', side_effect=abort_failure), \
                     patch.object(runner.subprocess, 'run') as dial_command:
                    with self.assertRaises((RuntimeError, subprocess.TimeoutExpired)):
                        runner.run_contracts(output, source, {}, count=self.count)
                    dial_command.assert_not_called()
                self.assertFalse((output / 'result.json').exists())
                failure = json.loads((output / 'failure.json').read_text())
                self.assertEqual(failure['phase'], 'abort')
                self.assertTrue(Path(failure['commands']).is_file())
                self.assertEqual(Path(failure['logs'][0]).read_bytes(), b'partial-parent-output')


class DialPairGates(unittest.TestCase):
    variants = {'official', 'original', 'fixed', 'lifecycle', 'halfclose', 'drain', 'abort', 'dial'}

    def test_dial_focus_is_abort_dial_dial_abort(self):
        self.assertEqual(pair.candidate_order(self.variants, dial=True),
                         (('abort', 'dial', 'dial', 'abort'), 'dial'))

    def test_missing_parent_candidate_and_multiple_focus_rejected(self):
        for variants, flags in ((self.variants - {'abort'}, {'dial': True}),
                                (self.variants - {'dial'}, {'dial': True}),
                                (self.variants, {'dial': True, 'abort': True})):
            with self.subTest(variants=variants, flags=flags), self.assertRaises(RuntimeError):
                pair.candidate_order(variants, **flags)


if __name__ == '__main__':
    unittest.main(verbosity=2)
