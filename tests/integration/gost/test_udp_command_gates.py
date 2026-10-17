"""Offline timeout failure-gate tests; mutates only private temp directories."""
from pathlib import Path
import importlib.util
import json
import subprocess
import sys
import tempfile
import unittest
from unittest.mock import patch

ROOT = Path(__file__).resolve().parent
sys.path.insert(0, str(ROOT))

def module(name, path):
    spec = importlib.util.spec_from_file_location(name, path)
    result = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(result)
    return result

builder = module('review_timeout_builder', ROOT / 'udp_pair_builder.py')
runner = module('review_timeout_runner', ROOT / 'run_abort_contracts.py')

class TimeoutFailureGates(unittest.TestCase):
    def expired(self, arguments, **kwargs):
        raise subprocess.TimeoutExpired(arguments, kwargs['timeout'], output=b'partial-timeout-output')

    def test_builder_main_timeout_is_failed_provenance_and_keeps_log(self):
        with tempfile.TemporaryDirectory(prefix='udp-builder-timeout-') as temporary:
            output = Path(temporary) / 'output'
            def unpack(_archive, destination):
                source = destination / 'gost-source'
                source.mkdir(parents=True)
                (source / 'go.mod').write_text('module example.invalid/test\n', encoding='utf-8')
                (source / 'go.sum').write_text('', encoding='utf-8')
                return ['gost-source/go.mod', 'gost-source/go.sum']
            with patch.object(builder.sys, 'argv', ['builder', str(output)]), \
                 patch.object(builder.sys, 'platform', 'linux'), \
                 patch.object(builder.platform, 'machine', return_value='x86_64'), \
                 patch.object(builder.shutil, 'which', return_value='go'), \
                 patch.object(builder, 'resolve_commit', return_value={'commit': '0'*40}), \
                 patch.object(builder, 'download', return_value={'url': 'offline', 'bytes': 1, 'sha256': 'stub'}), \
                 patch.object(builder, 'safe_unpack', side_effect=unpack), \
                 patch.object(builder.subprocess, 'run', side_effect=self.expired):
                with self.assertRaises(subprocess.TimeoutExpired):
                    builder.main()
            provenance = json.loads((output / 'provenance.json').read_text(encoding='utf-8'))
            self.assertEqual(provenance['status'], 'failed')
            self.assertEqual(provenance['error_type'], 'TimeoutExpired')
            self.assertNotEqual(provenance['status'], 'complete')
            self.assertIn(b'partial-timeout-output', (output / 'go-version.txt').read_bytes())
            self.assertNotIn('builds', provenance)

    def test_contract_timeout_keeps_log_and_command_but_never_result(self):
        with tempfile.TemporaryDirectory(prefix='udp-contract-timeout-') as temporary:
            root = Path(temporary)
            source = root / 'upstream'
            source.mkdir()
            (source / 'go.mod').write_text('module github.com/go-gost/x\n\nreplace github.com/ugorji/go v1.1.4 => github.com/ugorji/go/codec v1.2.12\n', encoding='utf-8')
            (source / 'go.sum').write_text('', encoding='utf-8')
            contracts = root / 'inputs'
            contracts.mkdir()
            (contracts / 'contract_test.go').write_text('package review\n', encoding='utf-8')
            output = root / 'result'
            with patch.object(runner, 'abort_overlay', return_value={'overlay': {'Replace': {}}}), \
                 patch.object(runner, 'contract_overlay', return_value={'Replace': {}}), \
                 patch.object(runner.subprocess, 'run', side_effect=self.expired):
                with self.assertRaises(subprocess.TimeoutExpired):
                    runner.run_contracts(output, source, {'overlay': {'Replace': {}}}, contracts=contracts)
            self.assertFalse((output / 'result.json').exists(), 'timeout cannot produce accepted result')
            self.assertIn(b'partial-timeout-output', (output / 'go-mod-tidy.log').read_bytes())
            commands = json.loads((output / 'commands.json').read_text(encoding='utf-8'))
            self.assertEqual(len(commands), 1)
            self.assertTrue(commands[0]['timed_out'])
            self.assertGreater(commands[0]['timeout_seconds'], 0)
            self.assertIsNone(commands[0]['exit_code'])

    def test_go_test_timeout_keeps_partial_jsonl_and_never_result(self):
        with tempfile.TemporaryDirectory(prefix='udp-go-test-timeout-') as temporary:
            root = Path(temporary)
            source = root / 'upstream'
            source.mkdir()
            (source / 'go.mod').write_text('module github.com/go-gost/x\n\nreplace github.com/ugorji/go v1.1.4 => github.com/ugorji/go/codec v1.2.12\n', encoding='utf-8')
            (source / 'go.sum').write_text('', encoding='utf-8')
            contracts = root / 'inputs'
            contracts.mkdir()
            (contracts / 'contract_test.go').write_text('package review\n', encoding='utf-8')
            output = root / 'result'
            calls = []
            def fake_run(arguments, **kwargs):
                calls.append(arguments)
                if arguments[1:3] == ['mod', 'tidy']:
                    return subprocess.CompletedProcess(arguments, 0, stdout=b'offline-tidy-ok')
                self.assertEqual(arguments[1], 'test')
                raise subprocess.TimeoutExpired(arguments, kwargs['timeout'], output=b'{"Action":"run","Test":"TestActualPartial"}\n')
            with patch.object(runner, 'abort_overlay', return_value={'overlay': {'Replace': {}}}), \
                 patch.object(runner, 'contract_overlay', return_value={'Replace': {}}), \
                 patch.object(runner.subprocess, 'run', side_effect=fake_run):
                with self.assertRaises(subprocess.TimeoutExpired):
                    runner.run_contracts(output, source, {'overlay': {'Replace': {}}}, contracts=contracts)
            self.assertEqual(len(calls), 2)
            self.assertFalse((output / 'result.json').exists(), 'go test timeout cannot produce accepted result')
            self.assertIn(b'TestActualPartial', (output / 'race-results.jsonl').read_bytes())
            self.assertEqual((output / 'go-mod-tidy.log').read_bytes(), b'offline-tidy-ok')
            commands = json.loads((output / 'commands.json').read_text(encoding='utf-8'))
            self.assertEqual(len(commands), 2)
            self.assertEqual(commands[0]['exit_code'], 0)
            self.assertFalse(commands[0]['timed_out'])
            self.assertEqual(commands[0]['log'], 'go-mod-tidy.log')
            self.assertIsNone(commands[1]['exit_code'])
            self.assertTrue(commands[1]['timed_out'])
            self.assertEqual(commands[1]['log'], 'race-results.jsonl')
            self.assertGreater(commands[1]['timeout_seconds'], 0)

if __name__ == '__main__':
    unittest.main(verbosity=2)
