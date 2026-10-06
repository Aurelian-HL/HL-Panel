import { describe, expect, it } from 'vitest'
import { nodeInstallCommand } from './nodeInstall'

describe('node install command', () => {
  it('includes the panel origin and one-use token in one installation', () => {
    const command = nodeInstallCommand('https://panel.example.test:9443', 'enr_test_only_token')
    expect(command).toContain("--panel-url 'https://panel.example.test:9443'")
    expect(command).toContain("--token 'enr_test_only_token'")
    expect(command).toContain('HL-Panel/main/install-node.sh')
    expect(command).not.toContain('| bash')
    expect(command).not.toContain('-k ')
  })
  it('rejects shell injection and insecure or path-based origins', () => {
    for (const origin of ['http://panel.example.test', 'https://user:pass@panel.example.test', 'https://panel.example.test/extra', 'https://panel.example.test/?token=oops']) {
      expect(() => nodeInstallCommand(origin, 'enr_test_only_token')).toThrow()
    }
    expect(() => nodeInstallCommand('https://panel.example.test', "enr_'; touch /tmp/oops; '")).toThrow()
  })
})
