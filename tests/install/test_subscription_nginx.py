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
