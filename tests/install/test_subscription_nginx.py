#!/usr/bin/env python3
"""Bounded config migration: custom routes preserved, validation failure reverted."""
import importlib.util
from pathlib import Path
import tempfile
import unittest
from unittest.mock import patch

ROOT = Path(__file__).resolve().parents[2]
spec = importlib.util.spec_from_file_location('subscription_updater', ROOT/'deploy/update.py')
update = importlib.util.module_from_spec(spec)
spec.loader.exec_module(update)


class SubscriptionNginxTests(unittest.TestCase):
    def test_migration_port_timeout_idempotency_and_failure_rollback(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            target = root/'release'
            snippet = target/'deploy/nginx/snippets/hl-panel-app-locations.conf'
            snippet.parent.mkdir(parents=True)
            snippet.write_text(update.SUBSCRIPTION_LOCATION+'\n'+update.MIGRATION_LOCATION)
            backup = root/'backup'; backup.mkdir()
            update.APP_LOCATIONS = root/'app.conf'
            update.API_PROXY = root/'api.conf'
            update.MIGRATION_PROXY = root/'migration.conf'
            original = 'location /custom { return 200; }\n'
            update.APP_LOCATIONS.write_text(original)
            update.API_PROXY.write_text('proxy_pass http://127.0.0.1:18245;\nproxy_read_timeout 30s;\nproxy_send_timeout 30s;\n')
            update.private_regular = lambda path: None
            calls=[];update.run=lambda args:calls.append(args)
            # A backup-copy failure must not create a new proxy or alter routes.
            with patch.object(update.shutil,'copyfile',side_effect=OSError('isolated disk failure')):
                with self.assertRaises(OSError):update.subscription_nginx_update(target,backup)
            self.assertFalse(update.MIGRATION_PROXY.exists())
            self.assertEqual(update.APP_LOCATIONS.read_text(),original)
            update.subscription_nginx_update(target,backup)
            proxy=update.MIGRATION_PROXY.read_text()
            self.assertIn('127.0.0.1:18245',proxy)
            self.assertEqual(proxy.count('proxy_read_timeout'),1)
            self.assertEqual(proxy.count('proxy_send_timeout'),1)
            self.assertEqual(proxy.count('120s'),2)
            update.subscription_nginx_update(target,backup)
            self.assertEqual(len(calls),2)
            self.assertEqual(update.APP_LOCATIONS.read_text().count(update.MIGRATION_LOCATION),1)
            update.restore_subscription_nginx(backup)
            self.assertFalse(update.MIGRATION_PROXY.exists())
            self.assertEqual(update.APP_LOCATIONS.read_text(),original)
            failed=False
            def reject_once(args):
                nonlocal failed
                if not failed:failed=True;raise update.UpdateError('isolated nginx rejection')
            update.run=reject_once
            with self.assertRaises(update.UpdateError):update.subscription_nginx_update(target,backup)
            self.assertEqual(update.APP_LOCATIONS.read_text(),original)
            self.assertFalse(update.MIGRATION_PROXY.exists())

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

    def test_upgrades_legacy_metrics_dropin_and_restores_original(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            target = root/'release'
            unit = target/'deploy/systemd/hl-panel-control-api.service'
            unit.parent.mkdir(parents=True)
            unit.write_text('[Service]\nProcSubset=all\n')
            backup = root/'backup'
            backup.mkdir()
            update.PROC_OVERRIDE = root/'unit.d/20-host-metrics.conf'
            update.PROC_OVERRIDE.parent.mkdir()
            update.PROC_OVERRIDE.write_text(update.LEGACY_PROC_OVERRIDE_CONTENT)
            update.private_regular = lambda path: None
            update.run = lambda args: None
            update.host_metrics_update(target, backup)
            self.assertIn('AF_NETLINK', update.PROC_OVERRIDE.read_text())
            self.assertEqual((backup/'host-metrics-previous.conf').read_text(), update.LEGACY_PROC_OVERRIDE_CONTENT)
            update.host_metrics_update(target, backup)
            update.restore_host_metrics(backup)
            self.assertEqual(update.PROC_OVERRIDE.read_text(), update.LEGACY_PROC_OVERRIDE_CONTENT)

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
