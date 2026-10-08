#!/usr/bin/env python3
"""Bounded config migration: custom routes preserved, validation failure reverted."""
import importlib.util
from pathlib import Path
import tempfile
import unittest

ROOT = Path(__file__).resolve().parents[2]
spec = importlib.util.spec_from_file_location('subscription_updater', ROOT/'deploy/update.py')
update = importlib.util.module_from_spec(spec)
spec.loader.exec_module(update)


class SubscriptionNginxTests(unittest.TestCase):
    def test_host_metrics_dropin_preserves_custom_unit_and_rolls_back(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            target = root/'release'
            unit = target/'deploy/systemd/hl-panel-control-api.service'
            unit.parent.mkdir(parents=True)
            unit.write_text('[Service]\nProtectProc=invisible\nProcSubset=all\n')
            backup = root/'backup'
            backup.mkdir()
            update.PROC_OVERRIDE = root/'unit.d/20-host-metrics.conf'
            update.private_regular = lambda path: None
            calls = []
            update.run = lambda args: calls.append(args)
            update.host_metrics_update(target, backup)
            self.assertEqual(update.PROC_OVERRIDE.read_text(), update.PROC_OVERRIDE_CONTENT)
            self.assertEqual(calls, [['systemctl','daemon-reload']])
            update.host_metrics_update(target, backup)
            self.assertEqual(len(calls), 1)
            update.restore_host_metrics(backup)
            self.assertFalse(update.PROC_OVERRIDE.exists())
            update.PROC_OVERRIDE.write_text('[Service]\n# custom config\n')
            with self.assertRaises(update.UpdateError):
                update.host_metrics_update(target, backup)
            self.assertIn('custom config', update.PROC_OVERRIDE.read_text())

    def test_preserves_routes_and_restores_on_failure(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            target = root/'release'
            snippet = target/'deploy/nginx/snippets/hl-panel-app-locations.conf'
            snippet.parent.mkdir(parents=True)
            snippet.write_text(update.SUBSCRIPTION_LOCATION)
            backup = root/'backup'
            backup.mkdir()
            update.APP_LOCATIONS = root/'existing.conf'
            original = 'location /custom { return 200; }\n# Existing domain-specific routes\n'
            update.APP_LOCATIONS.write_text(original)
            update.private_regular = lambda path: None
            calls = []
            update.run = lambda args: calls.append(args)
            update.subscription_nginx_update(target, backup)
            self.assertIn(original.strip(), update.APP_LOCATIONS.read_text())
            self.assertEqual(update.APP_LOCATIONS.read_text().count(update.SUBSCRIPTION_LOCATION), 1)
            update.subscription_nginx_update(target, backup)
            self.assertEqual(update.APP_LOCATIONS.read_text().count(update.SUBSCRIPTION_LOCATION), 1)
            update.restore_subscription_nginx(backup)
            self.assertEqual(update.APP_LOCATIONS.read_text(), original)
            failed = False

            def reject_once(args):
                nonlocal failed
                if not failed:
                    failed = True
                    raise update.UpdateError('isolated invalid Nginx config')
                calls.append(args)

            update.run = reject_once
            with self.assertRaises(update.UpdateError):
                update.subscription_nginx_update(target, backup)
            self.assertEqual(update.APP_LOCATIONS.read_text(), original)


if __name__ == '__main__':
    unittest.main()
