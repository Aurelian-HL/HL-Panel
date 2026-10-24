"""Isolated installation tests; never invoke host networking or systemd."""
import contextlib
import importlib.util
import json
from pathlib import Path
import subprocess
import tempfile
import unittest
from unittest.mock import patch

spec = importlib.util.spec_from_file_location('node_tc', Path(__file__).resolve().parents[2] / 'deploy/configure-node-traffic-control.py')
tc = importlib.util.module_from_spec(spec)
spec.loader.exec_module(tc)


class TrafficControlInstallTest(unittest.TestCase):
    def setUp(self):
        self.stack = contextlib.ExitStack()
        self.addCleanup(self.stack.close)
        self.root = Path(self.stack.enter_context(tempfile.TemporaryDirectory()))
        self.config = self.root / 'agent.json'
        self.config.write_text(json.dumps({'engine_mode': 'mixed', 'dial_host': '192.0.2.10'}))
        self.config.chmod(0o640)
        self.dropin = self.root / 'agent.service.d' / '30-traffic-control.conf'
        self.stack.enter_context(patch.object(tc, 'safe'))
        self.stack.enter_context(patch.object(tc.os, 'chown', create=True))
        self.stack.enter_context(patch.object(tc.shutil, 'which', side_effect=lambda name: str(self.config)))
        self.calls = []
        self.routes = [{'dev': 'ens3'}]
        self.fail_reload = False
        self.stack.enter_context(patch.object(tc.subprocess, 'run', self.run_command))

    def run_command(self, argv, **options):
        self.calls.append(argv)
        if argv[0] == 'systemctl':
            if self.fail_reload and options.get('check'):
                raise subprocess.CalledProcessError(1, argv)
            return subprocess.CompletedProcess(argv, 0)
        return subprocess.CompletedProcess(argv, 0, json.dumps(self.routes))

    def test_fresh_node_gets_real_interface_and_capability_without_qdisc_mutation(self):
        result = tc.configure(self.config, self.dropin)
        self.assertTrue(result['configured'])
        config = json.loads(self.config.read_text())
        self.assertEqual(config['traffic_control_interface'], 'ens3')
        self.assertEqual(config['dial_host'], '192.0.2.10')
        self.assertEqual(self.dropin.read_text(), tc.UNIT)
        self.assertFalse(any('qdisc' in call or 'filter' in call for call in self.calls))
        calls = len(self.calls)
        self.assertFalse(tc.configure(self.config, self.dropin)['configured'])
        self.assertEqual(len(self.calls), calls)

    def test_existing_custom_or_disabled_settings_are_preserved(self):
        for value in ['custom0', '']:
            self.config.write_text(json.dumps({'traffic_control_interface': value}))
            original = self.config.read_bytes()
            self.assertFalse(tc.configure(self.config, self.dropin)['configured'])
            self.assertEqual(self.config.read_bytes(), original)
            self.assertFalse(self.calls)
            self.assertFalse(self.dropin.exists())

    def test_missing_tools_and_ambiguous_route_do_not_write_configuration(self):
        original = self.config.read_bytes()
        with patch.object(tc.shutil, 'which', return_value=None):
            with self.assertRaises(ValueError):
                tc.configure(self.config, self.dropin)
        for routes in [[], [{'dev': 'eth0'}, {'dev': 'eth1'}], [{'dev': 'lo'}], [{'dev': 'eth0;bad'}]]:
            self.routes = routes
            with self.assertRaises(ValueError):
                tc.configure(self.config, self.dropin)
            self.assertEqual(self.config.read_bytes(), original)
            self.assertFalse(self.dropin.exists())

    def test_failed_reload_restores_both_files(self):
        original = self.config.read_bytes()
        self.fail_reload = True
        with self.assertRaises(subprocess.CalledProcessError):
            tc.configure(self.config, self.dropin)
        self.assertEqual(self.config.read_bytes(), original)
        self.assertFalse(self.dropin.parent.exists())

    def test_conflicting_unit_override_is_preserved(self):
        original = self.config.read_bytes()
        self.dropin.parent.mkdir()
        self.dropin.write_text('custom override')
        with self.assertRaises(ValueError):
            tc.configure(self.config, self.dropin)
        self.assertEqual(self.config.read_bytes(), original)
        self.assertEqual(self.dropin.read_text(), 'custom override')


if __name__ == '__main__':
    unittest.main()
