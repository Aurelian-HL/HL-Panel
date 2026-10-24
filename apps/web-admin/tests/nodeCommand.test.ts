import { spawnSync } from 'node:child_process'
import { expect, it } from 'vitest'
import { nodeDownloadPrerequisites } from '../src/lib/nodeCommand'

// Execute the actual copyable shell prefix, with all host/package calls faked.
function execute(missing: boolean, fail = false, root = true) {
  const wrapper = `id() { echo ${root ? '0' : '1000'}; }
command() { if [ "$*" = '-v curl' ]; then return ${missing ? '1' : '0'}; fi; return 0; }
test() { if [ "$1" = '-s' ]; then return 0; fi; builtin test "$@"; }
apt-get() { echo "APT:$* MODE:$NEEDRESTART_MODE"; return ${fail ? '1' : '0'}; }
set -e; ${nodeDownloadPrerequisites}; echo READY`
  return spawnSync('bash', ['-c', wrapper], { encoding: 'utf8' })
}
it('installs a missing downloader once and skips packages when already present', () => {
  const missing = execute(true)
  expect(missing.status, missing.stderr).toBe(0)
  expect(missing.stdout.match(/APT:/g)).toHaveLength(2)
  expect(missing.stdout).toContain('--reinstall')
  expect(missing.stdout).toContain('install -y --no-install-recommends curl MODE:l')
  const ready = execute(false)
  expect(ready.status, ready.stderr).toBe(0)
  expect(ready.stdout).toBe('READY\n')
})
it('aborts before downloading or registration when packages fail or root is missing', () => {
  for (const result of [execute(true, true), execute(false, false, false)]) {
    expect(result.status).not.toBe(0)
    expect(result.stdout).not.toContain('READY')
  }
})
