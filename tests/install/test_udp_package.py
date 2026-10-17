#!/usr/bin/env python3
"""Candidate option errors must fail before creating a release or build stage."""
from pathlib import Path
import shutil
import subprocess
import tempfile
import unittest

REPO = Path(__file__).resolve().parents[2]
POWERSHELL = shutil.which('pwsh') or shutil.which('powershell')


@unittest.skipUnless(POWERSHELL, 'PowerShell is required for release builder acceptance')
class CandidateOptionsTests(unittest.TestCase):
    def test_rejected_options_do_not_create_output(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            bundle = root / 'bundle'
            bundle.mkdir()
            cases = [
                (['-GostBundleDirectory', str(bundle)], 'must be supplied together'),
                (['-GostManifestSha256', 'a' * 64], 'must be supplied together'),
                (['-GostBundleDirectory', str(bundle), '-GostManifestSha256', 'wrong'], '64 lowercase'),
                (['-GostBundleDirectory', str(bundle), '-GostManifestSha256', 'A' * 64], '64 lowercase'),
                (['-GostBundleDirectory', str(root / 'absent'), '-GostManifestSha256', 'a' * 64], 'does not exist'),
            ]
            for options, expected in cases:
                with self.subTest(options=options):
                    def literal(value):
                        return "'" + str(value).replace("'", "''") + "'"
                    parameters = {'ReleaseDirectory': str(root / 'release')}
                    parameters.update({options[i].lstrip('-'): options[i + 1]
                                       for i in range(0, len(options), 2)})
                    assignments = '; '.join(key + ' = ' + literal(value)
                                            for key, value in parameters.items())
                    # PowerShell's default formatter wraps and colors errors on
                    # Linux. Catch the actual exception for a stable assertion.
                    script = (
                        '$options = @{' + assignments + '}; try { & '
                        + literal(REPO / 'deploy/build-release.ps1')
                        + ' @options; exit 0 } catch { [Console]::Error.WriteLine($_.Exception.Message); exit 1 }'
                    )
                    result = subprocess.run([POWERSHELL, '-NoProfile', '-NonInteractive',
                        '-Command', script], capture_output=True, text=True, timeout=30)
                    self.assertNotEqual(result.returncode, 0)
                    self.assertIn(expected, result.stdout + result.stderr)
                    self.assertEqual(list(root.iterdir()), [bundle])


if __name__ == '__main__':
    unittest.main()
