"""Exercise release selection with fake downloads; never install or register.

Run with python -m unittest discover -s tests/install -p test_node_bootstrap.py.
Only Bash and Python's standard library are required.
"""
import os
from pathlib import Path
import shutil
import subprocess
import tempfile
import unittest


ROOT = Path(__file__).resolve().parents[2]
BOOTSTRAP = ROOT / 'install-node.sh'
REPOSITORY = 'https://github.com/Aurelian-HL/HL-Panel'
TOKEN = 'enr_synthetic_test_only'
WRAPPER = r'''
curl() {
  local url='' output='' item
  printf '%s\n' "$*" >> "$FIX_LOG"
  while (($#)); do
    item="$1"; shift
    case "$item" in
      --output|-o) output="$1"; shift ;;
      https://*) url="$item" ;;
    esac
  done
  if [[ "$url" == */releases/latest ]]; then
    [[ "${FIX_NETWORK_FAIL:-0}" == 0 ]] || return 22
    printf '%s' "$FIX_RESOLVED_URL"
  elif [[ "$url" == https://raw.githubusercontent.com/Aurelian-HL/HL-Panel/*/deploy/install-node.sh ]]; then
    [[ "${FIX_DOWNLOAD_FAIL:-0}" == 0 ]] || return 22
    if [[ "${FIX_SYNTAX_FAIL:-0}" == 1 ]]; then
      printf 'if\n' > "$output"
    else
      printf '#!/usr/bin/env bash\nprintf "DELEGATE_ARG:%%s\\n" "$@"\n' > "$output"
    fi
  else
    printf 'unexpected download\n' >&2
    return 23
  fi
}
export -f curl
bash "$@"
'''


class NodeBootstrapTest(unittest.TestCase):
    def run_bootstrap(self, version=None, resolved='v0.1.90', **settings):
        bash = os.environ.get('BASH_BIN') or shutil.which('bash')
        self.assertIsNotNone(bash, 'Bash is required')
        with tempfile.TemporaryDirectory(prefix='hl-bootstrap-') as directory:
            log = Path(directory) / 'downloads.log'
            environment = os.environ.copy()
            environment.update(FIX_LOG=log.as_posix(),
                               FIX_RESOLVED_URL=f'{REPOSITORY}/releases/tag/{resolved}')
            environment.update(settings)
            arguments = ['--panel-url', 'https://panel.example.test', '--token', TOKEN]
            if version is not None:
                arguments += ['--version', version]
            result = subprocess.run([bash, '-c', WRAPPER, 'test-bootstrap',
                                     BOOTSTRAP.as_posix(), *arguments],
                                    env=environment, capture_output=True, text=True,
                                    encoding='utf-8', errors='replace', timeout=15)
            downloads = log.read_text(encoding='utf-8') if log.exists() else ''
            return result, downloads

    def test_default_resolves_latest_without_panel_build_metadata(self):
        result, downloads = self.run_bootstrap()
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertIn(f'{REPOSITORY}/releases/latest', downloads)
        self.assertIn('/v0.1.90/deploy/install-node.sh', downloads)
        self.assertIn('DELEGATE_ARG:v0.1.90', result.stdout)
        self.assertIn(f'DELEGATE_ARG:{TOKEN}', result.stdout)
        self.assertNotIn('site-info', downloads)
        self.assertNotIn(TOKEN, downloads)
        self.assertNotIn('panel.example.test', downloads)
        for request in downloads.splitlines():
            self.assertIn("--proto =https --proto-redir =https", request)

    def test_each_execution_resolves_a_new_release(self):
        for version in ('v0.1.90', 'v0.1.91'):
            with self.subTest(version=version):
                result, downloads = self.run_bootstrap(resolved=version)
                self.assertEqual(result.returncode, 0, result.stderr)
                self.assertIn(f'/{version}/deploy/install-node.sh', downloads)
                self.assertIn(f'DELEGATE_ARG:{version}', result.stdout)

    def test_explicit_latest_also_resolves(self):
        result, downloads = self.run_bootstrap(version='latest')
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertIn('/releases/latest', downloads)
        self.assertTrue(result.stdout.rstrip().endswith('DELEGATE_ARG:v0.1.90'))

    def test_explicit_pin_is_not_replaced_by_latest(self):
        result, downloads = self.run_bootstrap(version='v0.1.89')
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertNotIn('/releases/latest', downloads)
        self.assertIn('/v0.1.89/deploy/install-node.sh', downloads)

    def test_invalid_or_untrusted_redirect_never_executes_installer(self):
        for url in ('', f'{REPOSITORY}/releases',
                    'https://attacker.example/releases/tag/v0.1.90',
                    f'{REPOSITORY}/releases/tag/v0.1.90-rc.1',
                    f'{REPOSITORY}/releases/tag/v0.1.89+group-delete.20261015',
                    f'{REPOSITORY}/releases/tag/v0.1.90?token=oops'):
            with self.subTest(url=url):
                result, downloads = self.run_bootstrap(FIX_RESOLVED_URL=url)
                self.assertNotEqual(result.returncode, 0)
                self.assertNotIn('DELEGATE_ARG:', result.stdout)
                self.assertNotIn('raw.githubusercontent.com', downloads)

    def test_network_download_and_syntax_failures_never_register(self):
        for setting in ('FIX_NETWORK_FAIL', 'FIX_DOWNLOAD_FAIL', 'FIX_SYNTAX_FAIL'):
            with self.subTest(setting=setting):
                result, _ = self.run_bootstrap(**{setting: '1'})
                self.assertNotEqual(result.returncode, 0)
                self.assertNotIn('DELEGATE_ARG:', result.stdout)


if __name__ == '__main__':
    unittest.main()
