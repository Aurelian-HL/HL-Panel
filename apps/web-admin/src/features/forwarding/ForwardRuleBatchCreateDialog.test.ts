import { flushPromises, mount, type VueWrapper } from '@vue/test-utils'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import type { DeviceGroup } from '@/api'
import { ApiError } from '@/api/http'

const mocked = vi.hoisted(() => ({ batchCreateRules: vi.fn(), realityTargets: vi.fn(), probeReality: vi.fn() }))
vi.mock('@/api/business', () => ({ businessApi: mocked }))
import ForwardRuleBatchCreateDialog from './ForwardRuleBatchCreateDialog.vue'

const device: DeviceGroup = { id: 'entry-1', name: '美国组', kind: 'ENTRY', description: '', member_count: 2, current_generation: 0, selection_policy: 'weighted_round_robin', created_at: '', updated_at: '' }
const target = { server_name: 'www.apple.com', destination: 'www.apple.com:443' }
const wrappers: VueWrapper[] = []
function open() {
  const wrapper = mount(ForwardRuleBatchCreateDialog, { props: { devices: [device], networks: [], ruleGroups: [] }, global: { stubs: { Teleport: true } } })
  wrappers.push(wrapper)
  return wrapper
}
async function populate(wrapper: VueWrapper) {
  await flushPromises()
  await wrapper.get('select[required]').setValue(device.id)
  await wrapper.get('textarea').setValue('192.0.2.10:1080:user:private-password\n客户昵称 192.0.2.11:1080:user:private-password')
}
beforeEach(() => {
  mocked.batchCreateRules.mockReset().mockResolvedValue({ created: 2, items: [] })
  mocked.realityTargets.mockReset().mockResolvedValue({ items: [target] })
  mocked.probeReality.mockReset().mockImplementation(async value => ({ ...value, compatible: true, latency_ms: 12, source: 'control_plane', checked_at: '2026-10-30T00:00:00Z' }))
})
afterEach(() => { for (const wrapper of wrappers.splice(0)) wrapper.unmount(); vi.restoreAllMocks() })

describe('ForwardRuleBatchCreateDialog', () => {
  it('previews safe names and sends a single complete batch with automatic ports', async () => {
    const wrapper = open()
    await populate(wrapper)
    expect(wrapper.findAll('.batch-row strong').map(item => item.text())).toEqual(['192.0.2.10', '客户昵称'])
    expect(wrapper.get('.batch-preview').text()).not.toContain('private-password')
    expect(wrapper.get('button[type="submit"]').attributes('disabled')).toBeUndefined()
    await wrapper.get('form').trigger('submit')
    await flushPromises()
    expect(mocked.batchCreateRules).toHaveBeenCalledTimes(1)
    const [input, key] = mocked.batchCreateRules.mock.calls[0]!
    expect(key).toBeTruthy()
    expect(input.rules).toHaveLength(2)
    expect(input.rules[0]).toMatchObject({ name: '192.0.2.10', entry_group_id: device.id, protocol: 'tcp', ingress_protocol: 'vless_reality', vless_outbound_mode: 'SOCKS5', vless_socks5_password: 'private-password', listen_port: 0 })
    expect(input.rules[1].name).toBe('客户昵称')
    expect(input.rules[0]).not.toHaveProperty('customer_id')
    expect(wrapper.emitted('saved')).toHaveLength(1)
  })

  it('blocks every row when a line is invalid or Reality rechecking fails', async () => {
    const wrapper = open()
    await populate(wrapper)
    await wrapper.get('textarea').setValue('192.0.2.10:1080:user:private-password\ninvalid')
    await wrapper.get('form').trigger('submit')
    expect(wrapper.text()).toContain('第 2 行')
    expect(mocked.batchCreateRules).not.toHaveBeenCalled()
    await populate(wrapper)
    mocked.probeReality.mockRejectedValue(new Error('isolated unavailable'))
    await wrapper.findAll('button').find(item => item.text().includes('重新检测'))!.trigger('click')
    await flushPromises()
    expect(wrapper.get('button[type="submit"]').attributes('disabled')).toBeDefined()
    await wrapper.get('form').trigger('submit')
    expect(mocked.batchCreateRules).not.toHaveBeenCalled()
  })

  it('locks editing while pending and keeps the idempotency key for a safe retry', async () => {
    const wrapper = open()
    await populate(wrapper)
    let reject!: (reason: Error) => void
    mocked.batchCreateRules.mockImplementationOnce(() => new Promise((_resolve, fail) => { reject = fail }))
    await wrapper.get('form').trigger('submit')
    expect(wrapper.get('fieldset').attributes('disabled')).toBeDefined()
    expect(wrapper.findAll('button').find(item => item.text() === '取消')!.attributes('disabled')).toBeDefined()
    await wrapper.get('form').trigger('submit')
    expect(mocked.batchCreateRules).toHaveBeenCalledTimes(1)
    reject(new Error('isolated retryable failure'))
    await flushPromises()
    expect(wrapper.get('textarea').element.value).toContain('客户昵称')
    expect(wrapper.emitted('saved')).toBeUndefined()
    await wrapper.get('form').trigger('submit')
    await flushPromises()
    expect(mocked.batchCreateRules.mock.calls[1]![1]).toBe(mocked.batchCreateRules.mock.calls[0]![1])
    expect(wrapper.emitted('saved')).toHaveLength(1)
  })

  it('propagates expired sessions without pretending the batch succeeded', async () => {
    const wrapper = open()
    await populate(wrapper)
    mocked.batchCreateRules.mockRejectedValue(new ApiError('expired', 401))
    await wrapper.get('form').trigger('submit')
    await flushPromises()
    expect(wrapper.emitted('unauthorized')).toHaveLength(1)
    expect(wrapper.emitted('saved')).toBeUndefined()
  })
})
