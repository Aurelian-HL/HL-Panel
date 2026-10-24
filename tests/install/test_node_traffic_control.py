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
        self.assertEqual(self.calls[calls:], [[str(self.config), '-Version']] * 2 + [[str(self.config), '--version']] * 3)

    def test_existing_custom_or_disabled_settings_are_preserved(self):
        for value in ['custom0', '']:
            self.config.write_text(json.dumps({'traffic_control_interface': value}))
            original = self.config.read_bytes()
            result = tc.configure(self.config, self.dropin)
            self.assertEqual(result['configured'], bool(value))
            self.assertEqual(self.config.read_bytes(), original)
            if value:
                self.assertEqual(self.dropin.read_text(), tc.UNIT)
                self.dropin.unlink()
            else:
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

    def test_upgrade_defaults_survive_legacy_json_replacement_and_are_idempotent(self):
        original = self.config.read_bytes()
        with patch.object(tc, 'discover', return_value=('ens3', '/usr/sbin/tc')):
            result = tc.configure(self.config, self.dropin, service_defaults=True)
            self.assertTrue(result['configured'])
            self.assertEqual(self.config.read_bytes(), original)
            content = self.dropin.read_bytes()
            self.assertTrue(tc.managed_defaults(content))
            self.assertIn(b'HL_PANEL_TC_INTERFACE=ens3', content)
            self.assertIn(b'HL_PANEL_TC_BINARY=/usr/sbin/tc', content)
            self.assertNotIn(b'ExecStart', content)
            calls = len(self.calls)
            # This is the ordering of the already installed, older updater.
            self.config.write_bytes(original)
            self.assertFalse(tc.configure(self.config, self.dropin, service_defaults=True)['configured'])
            self.assertEqual(self.calls[calls:], [[str(self.config), '-Version']] * 2 + [[str(self.config), '--version']] * 3)
            self.assertEqual(self.dropin.read_bytes(), content)
            self.fail_reload = True
            with patch.object(tc, 'discover', return_value=('ens4', '/usr/sbin/tc')):
                with self.assertRaises(subprocess.CalledProcessError):
                    tc.configure(self.config, self.dropin, service_defaults=True)
            self.assertEqual(self.config.read_bytes(), original)
            self.assertEqual(self.dropin.read_bytes(), content)

    def test_service_defaults_preserve_explicit_custom_settings_and_conflicting_override(self):
        for setting in [{'traffic_control_interface': ''}, {'traffic_control_binary': '/custom/tc'}, {'traffic_control_state_file': '/custom/state'}]:
            self.config.write_text(json.dumps(setting))
            original = self.config.read_bytes()
            self.assertFalse(tc.configure(self.config, self.dropin, service_defaults=True)['configured'])
            self.assertEqual(self.config.read_bytes(), original)
            self.assertFalse(self.dropin.exists())
        self.config.write_text('{}')
        self.dropin.parent.mkdir()
        self.dropin.write_text('custom')
        with patch.object(tc, 'discover', return_value=('ens3', '/usr/sbin/tc')):
            with self.assertRaises(ValueError):
                tc.configure(self.config, self.dropin, service_defaults=True)
        self.assertEqual(self.dropin.read_text(), 'custom')

    def test_missing_iproute2_can_be_installed_without_changing_qdisc(self):
        lookups = {name: [str(self.config)] * 2 for name in ('systemd-analyze', 'sysctl', 'modprobe')}
        lookups.update({'ip': [None, str(self.config)], 'tc': [None, str(self.config)], 'apt-get': [str(self.config)]})
        with patch.object(tc.shutil, 'which', side_effect=lambda name: lookups[name].pop(0)):
            interface, binary = tc.discover(install_missing=True)
        self.assertEqual(interface, 'ens3')
        self.assertEqual(binary, str(self.config))
        self.assertTrue(any(call[-4:] == ['install', '-y', '--no-install-recommends', 'iproute2'] for call in self.calls))
        self.assertTrue(any('--reinstall' in call for call in self.calls))
        self.assertFalse(any('qdisc' in call or 'filter' in call for call in self.calls))

    def test_existing_json_does_not_hide_missing_system_packages(self):
        self.config.write_text(json.dumps({'traffic_control_interface': 'custom0'}))
        lookups = {name: [str(self.config)] * 2 for name in ('systemd-analyze', 'sysctl', 'modprobe')}
        lookups.update({'ip': [None, str(self.config)], 'tc': [None, str(self.config)], 'apt-get': [str(self.config)]})
        with patch.object(tc.shutil, 'which', side_effect=lambda name: lookups[name].pop(0)):
            result = tc.configure(self.config, self.dropin, install_missing=True)
        self.assertTrue(result['preserved_existing_interface'])
        self.assertTrue(any('install' in call for call in self.calls))
        self.assertEqual(json.loads(self.config.read_text())['traffic_control_interface'], 'custom0')

    def test_package_install_suppresses_unrelated_service_restarts(self):
        lookups = {name: [str(self.config)] * 2 for name in ('systemd-analyze', 'sysctl', 'modprobe')}
        lookups.update({'ip': [None, str(self.config)], 'tc': [None, str(self.config)], 'apt-get': [str(self.config)]})
        with patch.object(tc.shutil, 'which', side_effect=lambda name: lookups[name].pop(0)), \
                patch.object(tc.subprocess, 'run', side_effect=self.run_command) as run:
            tc.ensure_tools(install_missing=True)
        installs = [call for call in run.call_args_list if 'install' in call.args[0]]
        self.assertEqual(len(installs), 1)
        self.assertEqual(installs[0].kwargs['env']['NEEDRESTART_MODE'], 'l')

    def test_missing_bbr_utilities_are_batched_and_existing_tools_skip_apt(self):
        with patch.object(tc.subprocess, 'run', side_effect=self.run_command) as run:
            tc.ensure_tools(install_missing=True)
            self.assertFalse(any('install' in call.args[0] or 'update' in call.args[0] for call in run.call_args_list))
        lookups = {name: [str(self.config)] * 2 for name in ('ip', 'tc', 'systemd-analyze')}
        lookups.update({'sysctl': [None, str(self.config)], 'modprobe': [None, str(self.config)], 'apt-get': [str(self.config)]})
        with patch.object(tc.shutil, 'which', side_effect=lambda name: lookups[name].pop(0)):
            tc.ensure_tools(install_missing=True)
        installs = [call for call in self.calls if 'install' in call]
        self.assertEqual(len(installs), 1)
        self.assertEqual(installs[0][-2:], ['kmod', 'procps'])

    def test_enabled_custom_interface_missing_dropin_repairs_permission_only(self):
        self.config.write_text(json.dumps({'traffic_control_interface': 'custom0', 'traffic_control_binary': '/custom/tc'}))
        original = self.config.read_bytes()
        result = tc.configure(self.config, self.dropin, service_defaults=True)
        self.assertTrue(result['configured'])
        self.assertEqual(self.config.read_bytes(), original)
        self.assertEqual(self.dropin.read_text(), tc.UNIT)
        self.assertFalse(any('route' in call or 'restart' in call for call in self.calls))

    def test_unusable_tools_reject_before_writing_config(self):
        original = self.config.read_bytes()
        with patch.object(tc.subprocess, 'run', side_effect=subprocess.CalledProcessError(1, ['tc'])):
            with self.assertRaises(subprocess.CalledProcessError):
                tc.configure(self.config, self.dropin)
        self.assertEqual(self.config.read_bytes(), original)
        self.assertFalse(self.dropin.exists())


if __name__ == '__main__':
    unittest.main()
