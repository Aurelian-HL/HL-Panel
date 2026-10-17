"""Offline contract-result gates; no Go processes or network are started."""
from contextlib import ExitStack
import importlib.util
import json
from pathlib import Path
import subprocess
import sys
import tempfile
import unittest
from unittest.mock import patch

ROOT = Path(__file__).resolve().parent
sys.path.insert(0, str(ROOT))
spec = importlib.util.spec_from_file_location(
    'result_gate_runner', ROOT / 'run_abort_contracts.py')
runner = importlib.util.module_from_spec(spec)
spec.loader.exec_module(runner)


class ContractResultGates(unittest.TestCase):
    count = 3
    package = runner.EXPECTED_PACKAGE

    def events(self):
        events = []
        for name in sorted(runner.expected_tests()):
            for _ in range(self.count):
                events.extend([{'Action': 'run', 'Test': name, 'Package': self.package},
                               {'Action': 'pass', 'Test': name, 'Package': self.package}])
        return events + [{'Action': 'pass', 'Package': self.package}]

    def invoke(self, output, events, *, malformed=False, failed_stage=None):
        source = output.parent / 'source'
        source.mkdir()
        (source / 'go.mod').write_text(
            'module github.com/go-gost/x\n\n'
            'replace github.com/ugorji/go v1.1.4 => '
            'github.com/ugorji/go/codec v1.2.12\n', encoding='utf-8')
        (source / 'go.sum').write_text('', encoding='utf-8')
        contracts = output.parent / 'inputs'
        contracts.mkdir()
        (contracts / 'contract_test.go').write_text('package review\n', encoding='utf-8')
        calls = []

        def overlay(directory, *_args):
            directory.mkdir()
            (directory / 'overlay.json').write_text('{"Replace": {}}', encoding='utf-8')
            return {'overlay': {'Replace': {}}}

        def command(arguments, **kwargs):
            calls.append(arguments)
            self.assertEqual(kwargs['stderr'], subprocess.STDOUT)
            stage = 'tidy' if arguments[1:3] == ['mod', 'tidy'] else 'test'
            payload = (b'offline-tidy-ok' if stage == 'tidy' else
                       ''.join(json.dumps(event) + '\n' for event in events).encode())
            if malformed and stage == 'test':
                payload += b'{invalid-json\n'
            return subprocess.CompletedProcess(
                arguments, 7 if stage == failed_stage else 0, stdout=payload)

        with ExitStack() as stack:
            stack.enter_context(patch.object(runner, 'abort_overlay', side_effect=overlay))
            stack.enter_context(patch.object(runner, 'contract_overlay', side_effect=overlay))
            stack.enter_context(patch.object(runner.subprocess, 'run', side_effect=command))
            result = runner.run_contracts(
                output, source, {'overlay': {'Replace': {}}},
                contracts=contracts, count=self.count)
        self.assertEqual(len(calls), 2)
        return result

    def test_complete_event_set_is_accepted(self):
        with tempfile.TemporaryDirectory(prefix='udp-result-pass-') as temporary:
            output = Path(temporary) / 'output'
            result = self.invoke(output, self.events())
            self.assertEqual(result['passed_test_events'],
                             len(runner.expected_tests()) * self.count)
            self.assertEqual(result['count'], self.count)
            self.assertEqual(json.loads((output / 'result.json').read_text(encoding='utf-8')), result)
            self.assertTrue(all(not item['timed_out'] for item in result['commands']))

    def test_incomplete_or_invalid_events_never_produce_result(self):
        leaf = next(name for name in sorted(runner.expected_tests()) if '/' in name)
        mutations = {
            'missing_leaf': lambda events: [e for e in events if e.get('Test') != leaf],
            'duplicate_pass': lambda events: events + [{'Action': 'pass', 'Test': leaf}],
            'skip_with_full_passes': lambda events: events + [{'Action': 'skip', 'Test': leaf}],
            'unexpected_test': lambda events: events + [
                {'Action': 'run', 'Test': 'TestActualUnexpected'},
                {'Action': 'pass', 'Test': 'TestActualUnexpected'}],
            'missing_package': lambda events: events[:-1],
            'extra_package': lambda events: events + [{'Action': 'pass'}],
            'missing_run': lambda events: [e for e in events if not
                (e.get('Action') == 'run' and e.get('Test') == leaf)],
            'wrong_repeat_count': lambda events: events[2:],
            'explicit_failure': lambda events: events + [{'Action': 'fail', 'Test': leaf}],
            'wrong_package': lambda events: [dict(event, Package='example.invalid/wrong')
                                             for event in events],
            'malformed_json': lambda events: events,
        }
        for case, mutate in mutations.items():
            with self.subTest(case=case), tempfile.TemporaryDirectory(prefix='udp-result-reject-') as temporary:
                output = Path(temporary) / 'output'
                with self.assertRaises((RuntimeError, json.JSONDecodeError)):
                    self.invoke(output, mutate(self.events()), malformed=case == 'malformed_json')
                self.assertFalse((output / 'result.json').exists())
                self.assertTrue((output / 'race-results.jsonl').is_file())
                commands = json.loads((output / 'commands.json').read_text(encoding='utf-8'))
                self.assertEqual(len(commands), 2)

    def test_nonzero_command_never_produces_result(self):
        for stage in ('tidy', 'test'):
            with self.subTest(stage=stage), tempfile.TemporaryDirectory(prefix='udp-command-failure-') as temporary:
                output = Path(temporary) / 'output'
                with self.assertRaisesRegex(RuntimeError, 'command failed'):
                    self.invoke(output, self.events(), failed_stage=stage)
                self.assertFalse((output / 'result.json').exists())
                commands = json.loads((output / 'commands.json').read_text(encoding='utf-8'))
                self.assertEqual(len(commands), 1 if stage == 'tidy' else 2)
                self.assertEqual(commands[-1]['exit_code'], 7)
                self.assertFalse(commands[-1]['timed_out'])
                self.assertTrue((output / commands[-1]['log']).is_file())


if __name__ == '__main__':
    unittest.main(verbosity=2)
