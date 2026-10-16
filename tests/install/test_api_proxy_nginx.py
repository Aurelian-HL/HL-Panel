#!/usr/bin/env python3
"""Keep the API reverse proxy deadline aligned with the control API server."""
from pathlib import Path
import re
import unittest


ROOT = Path(__file__).resolve().parents[2]


class ApiProxyNginxTests(unittest.TestCase):
    def test_api_proxy_waits_for_the_control_api_write_deadline(self):
        snippet = (ROOT / 'deploy/nginx/snippets/hl-panel-api-proxy.conf').read_text()
        for directive in ('proxy_read_timeout', 'proxy_send_timeout'):
            matches = re.findall(rf'(?m)^\s*{directive}\s+([^;]+);', snippet)
            self.assertEqual(matches, ['120s'], directive)

    def test_migration_proxy_keeps_the_same_deadline(self):
        snippet = (ROOT / 'deploy/nginx/snippets/hl-panel-migration-proxy.conf').read_text()
        for directive in ('proxy_read_timeout', 'proxy_send_timeout'):
            matches = re.findall(rf'(?m)^\s*{directive}\s+([^;]+);', snippet)
            self.assertEqual(matches, ['120s'], directive)


if __name__ == '__main__':
    unittest.main()
