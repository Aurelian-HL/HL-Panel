"""Run installer address validation without installing or contacting a panel."""
from pathlib import Path
import os
import subprocess
import sys
import unittest


INSTALLER = (Path(__file__).resolve().parents[2] / 'deploy/install-node.sh').read_text(encoding='utf-8')


def validate(marker, *arguments):
    source = INSTALLER.split(marker + " <<'PY'\n", 1)[1].split('\nPY\n', 1)[0]
    environment = dict(os.environ, PYTHONIOENCODING='utf-8')
    return subprocess.run([sys.executable, '-c', source, *arguments], capture_output=True,
                          text=True, encoding='utf-8', env=environment)


class InstallationAddressTests(unittest.TestCase):
    def test_panel_rejects_invalid_hosts_and_ports(self):
        for address in ('https://bad..example.com', 'https://example.com..',
                        'https://-bad.example.com', 'https://bad-.example.com',
                        'https://' + 'a' * 64 + '.example.com', 'https://999.1.2.3',
                        'https://example.com:0', 'https://example.com:65536',
                        'https://user:password@example.com', 'https://example.com/path',
                        'http://example.com', 'https://example.com?x=1', 'https://example.com#x'):
            with self.subTest(address=address):
                self.assertNotEqual(validate('origin=$(python3 - "$origin" "$ca_file"', address, '').returncode, 0)

    def test_panel_accepts_domain_ipv4_and_normalizes(self):
        for address, expected in (('https://Panel.Example.com.:8443', 'https://panel.example.com:8443'),
                                  ('https://192.0.2.1', 'https://192.0.2.1')):
            with self.subTest(address=address):
                result = validate('origin=$(python3 - "$origin" "$ca_file"', address, '')
                self.assertEqual(result.returncode, 0, result.stderr)
                self.assertEqual(result.stdout.strip(), expected)

    def test_node_rejects_multiple_trailing_dots(self):
        for address in ('node.example.com..', '-node.example.com', 'node-.example.com', '999.1.2.3'):
            with self.subTest(address=address):
                self.assertNotEqual(validate('node_address=$(python3 - "$node_address"', address).returncode, 0)


if __name__ == '__main__':
    unittest.main()
