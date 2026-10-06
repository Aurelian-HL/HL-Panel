import { flushPromises, mount } from '@vue/test-utils'
import { beforeEach, describe, expect, it, vi } from 'vitest'

const apiMocks = vi.hoisted(() => ({ preview: vi.fn(), importRules: vi.fn() }))
vi.mock('@/api/business', async (load) => {
  const actual = await load<typeof import('@/api/business')>()
  return { ...actual, businessApi: { ...actual.businessApi, previewRuleImport: apiMocks.preview, importRules: apiMocks.importRules } }
})

import type { DeviceGroup } from '@/api'
import type { GroupNetwork } from '@/api/business'
import ForwardRuleTransferDialog from './ForwardRuleTransferDialog.vue'

const entry: DeviceGroup = { id: 'entry-1', name: '入口组', kind: 'ENTRY', selection_policy: 'weighted_round_robin', description: '', member_count: 1, current_generation: 1, created_at: '', updated_at: '' }
const network: GroupNetwork = { group_id: 'entry-1', connect_host: 'entry.example.test', port_start: 12000, port_end: 12010, direct_policy: 'OPTIONAL', allow_direct: true, allowed_exit_group_ids: [], traffic_multiplier: 1, revision: 1 }

describe('ForwardRuleTransferDialog', () => {
  beforeEach(() => {
    apiMocks.preview.mockReset()
    apiMocks.importRules.mockReset()
  })

  it('requires a clean preview before atomically submitting legacy NY text', async () => {
    apiMocks.preview.mockResolvedValue({ format: 'ny_text', total: 1, valid: 1, invalid: 0, global_issues: [], rows: [{ line: 1, operation: 'create', action: 'create', request: { name: '主线路', customer_id: 'c-1', rule_group_id: '', entry_group_id: 'entry-1', exit_group_id: '', egress_mode: 'DIRECT', protocol: 'tcp', listen_port: 0, targets: [{ host: 'target.example.test', port: 443 }], selection_policy: 'round_robin', paused: false, description: '', revision: 0 }, resolved_listen_port: 12000, issues: [] }] })
    apiMocks.importRules.mockResolvedValue({ created: 1, updated: 0, items: [] })
    const wrapper = mount(ForwardRuleTransferDialog, { props: { devices: [entry], networks: [network], ruleGroups: [] }, global: { stubs: { Teleport: true } } })
    const selects = wrapper.findAll('select')
    await selects[0]!.setValue('ny_text')
    await wrapper.get('.transfer-source').setValue('主线路##target.example.test#443')
    await wrapper.findAll('.transfer-mapping select')[1]!.setValue('entry-1')
    await wrapper.get('.transfer-preview-action button').trigger('click')
    await flushPromises()
    expect(apiMocks.preview).toHaveBeenCalledWith(expect.objectContaining({ mapping: expect.objectContaining({ customer_id: '', entry_group_id: 'entry-1', egress_mode: 'DIRECT', protocol: 'tcp', selection_policy: 'round_robin' }) }))
    expect(wrapper.text()).toContain('第 1 行 · 主线路')
    const submit = wrapper.get('button[type="submit"]')
    expect((submit.element as HTMLButtonElement).disabled).toBe(false)
    await wrapper.get('form').trigger('submit')
    await flushPromises()
    expect(apiMocks.importRules).toHaveBeenCalledOnce()
    expect(wrapper.emitted('imported')).toEqual([[1]])
    wrapper.unmount()
  })

  it('does not allow committing content changed while preview is in flight', async () => {
    let resolvePreview!: (value: unknown) => void
    apiMocks.preview.mockImplementation(() => new Promise((resolve) => { resolvePreview = resolve }))
    const wrapper = mount(ForwardRuleTransferDialog, { props: { devices: [entry], networks: [network], ruleGroups: [] }, global: { stubs: { Teleport: true } } })
    await wrapper.get('.transfer-source').setValue('first##target.example.test#443')
    await wrapper.findAll('.transfer-mapping select')[1]!.setValue('entry-1')
    await wrapper.get('.transfer-preview-action button').trigger('click')
    await wrapper.get('.transfer-source').setValue('second##target.example.test#443')
    resolvePreview({ format: 'ny_text', total: 1, valid: 1, invalid: 0, global_issues: [], rows: [] })
    await flushPromises()
    expect((wrapper.get('button[type="submit"]').element as HTMLButtonElement).disabled).toBe(true)
    expect(wrapper.text()).toContain('内容或映射已变化，请重新预览')
    await wrapper.get('form').trigger('submit')
    expect(apiMocks.importRules).not.toHaveBeenCalled()
    wrapper.unmount()
  })
})
