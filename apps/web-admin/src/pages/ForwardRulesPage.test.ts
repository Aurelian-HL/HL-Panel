import { config, flushPromises, mount } from '@vue/test-utils'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { createMemoryHistory, createRouter } from 'vue-router'

import type { Customer, ForwardRule, GroupNetwork, UserGroup } from '@/api/business'
import type { DeviceGroup } from '@/api'
import { ApiError } from '@/api/http'
import { toast } from '@/composables/toast'

const mocked = vi.hoisted(() => ({
  rules: vi.fn(),
  customers: vi.fn(),
  userGroups: vi.fn(),
  deviceGroups: vi.fn(),
  groupNetworks: vi.fn(),
  ruleGroups: vi.fn(),
  saveRule: vi.fn(),
  batchRules: vi.fn(),
  previewRuleImport: vi.fn(),
  importRules: vi.fn(),
  exportRules: vi.fn(),
}))
vi.mock('@/api/business', () => ({ businessApi: mocked }))
const mockedUsage = vi.hoisted(() => ({ query: vi.fn() }))
vi.mock('@/api/usage', () => ({ usageApi: mockedUsage }))

const mockedSubscription = vi.hoisted(() => ({ generateRule: vi.fn(), package: vi.fn() }))
vi.mock('@/api/subscriptions', () => ({ subscriptionApi: mockedSubscription }))
const mockedDownload = vi.hoisted(() => ({ downloadSubscriptionPackage: vi.fn() }))
vi.mock('@/lib/subscriptionDownload', () => mockedDownload)
import ForwardRulesPage from './ForwardRulesPage.vue'

const customer: Customer = { id: 'c-1', username: 'user01', display_name: '示例客户', user_group_id: 'ug-1', disabled: false, expires_at: null, traffic_limit_bytes: 0, traffic_used_bytes: 0, max_rules: 0, speed_limit_mbps: 0, ip_limit: 0, connection_limit: 0, revision: 1, effective_status: 'active', created_at: '', updated_at: '' }
const userGroup: UserGroup = { id: 'ug-1', name: '用户组', description: '', allowed_entry_group_ids: ['entry-1'], allowed_exit_group_ids: [], allow_direct: true, revision: 1 }
const device: DeviceGroup = { id: 'entry-1', name: '入口一', kind: 'ENTRY', description: '', member_count: 0, current_generation: 0, selection_policy: 'weighted_round_robin', created_at: '', updated_at: '' }
const network: GroupNetwork = { group_id: 'entry-1', connect_host: 'entry.example.test', port_start: 10000, port_end: 20000, allow_direct: true, allowed_exit_group_ids: [], traffic_multiplier: 1, revision: 1 }

beforeEach(() => {
	config.global.plugins = [createRouter({ history: createMemoryHistory(), routes: [{ path: '/:pathMatch(.*)*', component: { template: '<div />' } }] })]
	for (const method of Object.values(mocked)) method.mockReset()
  mockedSubscription.generateRule.mockReset()
  mockedSubscription.package.mockReset()
  mockedDownload.downloadSubscriptionPackage.mockReset()
  mockedUsage.query.mockReset()
  mockedUsage.query.mockResolvedValue({ items: [], totals: { rule_actual_bytes: 0, customer_actual_bytes: 0, charged_bytes: 0 }, total: 0, page: 1, page_size: 1 })
  mocked.rules.mockResolvedValue({ items: [] })
  mocked.customers.mockResolvedValue({ items: [customer] })
  mocked.userGroups.mockResolvedValue({ items: [userGroup] })
  mocked.deviceGroups.mockResolvedValue({ items: [device] })
  mocked.groupNetworks.mockResolvedValue({ items: [network] })
  mocked.ruleGroups.mockResolvedValue({ items: [] })
  mocked.exportRules.mockResolvedValue({ schema_version: 'nyvp.forwarding-rules/v1', exported_at: '2026-10-04T00:00:00Z', rules: [] })
})
afterEach(() => { config.global.plugins = [] })

describe('ForwardRulesPage', () => {
  it('lets the admin create a rule without selecting or submitting a customer', async () => {
    const created: ForwardRule = { id: 'rule-new', name: '新规则', customer_id: '', rule_group_id: '', entry_group_id: device.id, exit_group_id: '', egress_mode: 'DIRECT', protocol: 'tcp', listen_port: 10001, targets: [{ host: '192.0.2.10', port: 443 }], selection_policy: 'round_robin', paused: false, description: '', revision: 1, status: 'pending_activation' }
    mocked.saveRule.mockResolvedValue(created)
    const wrapper = mount(ForwardRulesPage, { global: { stubs: { Teleport: true } } })
    await flushPromises()

    const add = wrapper.findAll('button').find((item) => item.text().includes('添加规则'))
    expect(add).toBeDefined()
    await add!.trigger('click')
    expect(wrapper.find('#select-rule-customer').exists()).toBe(false)
    expect(wrapper.find('#forward-rule-editor').exists()).toBe(true)
    await wrapper.get('#forward-rule-editor input[maxlength="120"]').setValue('新规则')
    await wrapper.get('#forward-rule-editor select[required]').setValue(device.id)
    await wrapper.get('#forward-rule-editor textarea[required]').setValue('192.0.2.10:443')
    await wrapper.get('#forward-rule-editor').trigger('submit')
    await flushPromises()
    expect(mocked.saveRule).toHaveBeenCalledTimes(1)
    expect(mocked.saveRule.mock.calls[0]?.[0]).toMatchObject({ name: '新规则', entry_group_id: device.id, targets: [{ host: '192.0.2.10', port: 443 }] })
    expect(mocked.saveRule.mock.calls[0]?.[0]).not.toHaveProperty('customer_id')
    expect(mocked.customers).not.toHaveBeenCalled()
    expect(mocked.userGroups).not.toHaveBeenCalled()
    wrapper.unmount()
  })

  it('states explicitly when a saved rule cannot be deployed', async () => {
    const success = vi.spyOn(toast, 'success').mockImplementation(() => {})
    mocked.saveRule.mockResolvedValue({ id: 'rule-new', name: '出口规则', customer_id: '', rule_group_id: '', entry_group_id: device.id, exit_group_id: '', egress_mode: 'DIRECT', protocol: 'tcp', listen_port: 10001, targets: [{ host: '192.0.2.10', port: 443 }], selection_policy: 'round_robin', paused: false, description: '', revision: 1, status: 'pending_activation', activation_reason: 'exit_group_engine_unsupported' } satisfies ForwardRule)
    const wrapper = mount(ForwardRulesPage, { global: { stubs: { Teleport: true } } })
    await flushPromises()
    await wrapper.findAll('button').find((item) => item.text().includes('添加规则'))!.trigger('click')
    await wrapper.get('#forward-rule-editor input[maxlength="120"]').setValue('出口规则')
    await wrapper.get('#forward-rule-editor select[required]').setValue(device.id)
    await wrapper.get('#forward-rule-editor textarea[required]').setValue('192.0.2.10:443')
    await wrapper.get('#forward-rule-editor').trigger('submit')
    await flushPromises()
    expect(success).toHaveBeenCalledWith('规则已保存', '经出口组暂不支持下发')
    wrapper.unmount()
    success.mockRestore()
  })

  it('does not depend on loading customers to open the rule editor', async () => {
    mocked.customers.mockRejectedValue(new Error('客户接口不可用'))
    const wrapper = mount(ForwardRulesPage, { global: { stubs: { Teleport: true } } })
    await flushPromises()
    await wrapper.findAll('button').find((item) => item.text().includes('添加规则'))!.trigger('click')
    expect(wrapper.find('#forward-rule-editor').exists()).toBe(true)
    expect(mocked.customers).not.toHaveBeenCalled()
    wrapper.unmount()
  })

  it('does not show a customer selector on the rules page', async () => {
    const wrapper = mount(ForwardRulesPage, { global: { stubs: { Teleport: true } } })
    await flushPromises()
    expect(wrapper.text()).not.toContain('归属客户')
    expect(wrapper.find('#select-rule-customer').exists()).toBe(false)
    wrapper.unmount()
  })

  it('clears previously loaded rules and selection when the own-rule request fails', async () => {
    const rule: ForwardRule = { id: 'rule-old', name: '旧规则', customer_id: '', rule_group_id: '', entry_group_id: device.id, exit_group_id: '', egress_mode: 'DIRECT', protocol: 'tcp', listen_port: 10001, targets: [{ host: '192.0.2.10', port: 443 }], selection_policy: 'round_robin', paused: false, description: '', revision: 1, status: 'pending_activation' }
    mocked.rules.mockResolvedValueOnce({ items: [rule] }).mockRejectedValueOnce(new Error('本人规则接口不可用'))
    const wrapper = mount(ForwardRulesPage, { global: { stubs: { Teleport: true } } })
    await flushPromises()
    await wrapper.get('input[aria-label="选择规则 旧规则"]').setValue(true)
    await wrapper.findAll('.page-heading button').find((button) => button.text().includes('刷新'))!.trigger('click')
    await flushPromises()
    expect(wrapper.find('input[aria-label="选择规则 旧规则"]').exists()).toBe(false)
    expect(wrapper.text()).toContain('本人规则接口不可用')
    expect(wrapper.find('.rule-batch-toolbar').exists()).toBe(false)
    wrapper.unmount()
  })

  it('does not show rules if an auxiliary request expires the session', async () => {
    const rule: ForwardRule = { id: 'rule-old', name: '旧规则', customer_id: '', rule_group_id: '', entry_group_id: device.id, exit_group_id: '', egress_mode: 'DIRECT', protocol: 'tcp', listen_port: 10001, targets: [{ host: '192.0.2.10', port: 443 }], selection_policy: 'round_robin', paused: false, description: '', revision: 1, status: 'pending_activation' }
    mocked.rules.mockResolvedValue({ items: [rule] })
    mocked.ruleGroups.mockRejectedValue(new ApiError('expired', 401, 'expired'))
    const wrapper = mount(ForwardRulesPage, { global: { stubs: { Teleport: true } } })
    await flushPromises()
    expect(wrapper.text()).toContain('登录已失效')
    expect(wrapper.text()).not.toContain('旧规则')
    expect(wrapper.find('.rule-batch-toolbar').exists()).toBe(false)
    wrapper.unmount()
  })

  it('hides old rules and disables actions while refresh is pending', async () => {
    const rule: ForwardRule = { id: 'rule-old', name: '旧规则', customer_id: '', rule_group_id: '', entry_group_id: device.id, exit_group_id: '', egress_mode: 'DIRECT', protocol: 'tcp', listen_port: 10001, targets: [{ host: '192.0.2.10', port: 443 }], selection_policy: 'round_robin', paused: false, description: '', revision: 1, status: 'pending_activation' }
    let finish!: (value: { items: ForwardRule[] }) => void
    mocked.rules.mockResolvedValueOnce({ items: [rule] }).mockReturnValueOnce(new Promise((resolve) => { finish = resolve }))
    const wrapper = mount(ForwardRulesPage, { global: { stubs: { Teleport: true } } })
    await flushPromises()
    await wrapper.findAll('.page-heading button').find((button) => button.text().includes('刷新'))!.trigger('click')
    expect(wrapper.text()).not.toContain('旧规则')
    expect(wrapper.find('.rule-batch-toolbar').exists()).toBe(false)
    expect(wrapper.findAll('.page-heading button').every((button) => (button.element as HTMLButtonElement).disabled)).toBe(true)
    finish({ items: [] })
    await flushPromises()
    expect(wrapper.text()).toContain('暂无转发规则')
    wrapper.unmount()
  })

  it('distinguishes an auxiliary resource failure from an empty rule list', async () => {
    mocked.ruleGroups.mockRejectedValue(new Error('network down'))
    const wrapper = mount(ForwardRulesPage, { global: { stubs: { Teleport: true } } })
    await flushPromises()
    expect(wrapper.text()).toContain('暂无转发规则')
    expect(wrapper.text()).toContain('规则分组加载失败')
    expect(wrapper.text()).not.toContain('规则加载失败')
    wrapper.unmount()
  })

  it('exports the server-scoped rule document without requesting customers', async () => {
    const createObjectURL = vi.fn(() => 'blob:own-rules')
    const revokeObjectURL = vi.fn()
    vi.stubGlobal('URL', { createObjectURL, revokeObjectURL })
    const click = vi.spyOn(HTMLAnchorElement.prototype, 'click').mockImplementation(() => {})
    const wrapper = mount(ForwardRulesPage, { global: { stubs: { Teleport: true } } })
    await flushPromises()
    await wrapper.findAll('.page-heading button').find((button) => button.text().includes('导出'))!.trigger('click')
    await flushPromises()
    expect(mocked.exportRules).toHaveBeenCalledOnce()
    expect(createObjectURL).toHaveBeenCalledOnce()
    expect(click).toHaveBeenCalledOnce()
    expect(revokeObjectURL).toHaveBeenCalledWith('blob:own-rules')
    expect(mocked.customers).not.toHaveBeenCalled()
    wrapper.unmount()
    vi.unstubAllGlobals()
    click.mockRestore()
  })

  it('previews and imports rules without choosing or submitting another customer', async () => {
    mocked.previewRuleImport.mockResolvedValue({ format: 'ny_text', total: 1, valid: 1, invalid: 0, global_issues: [], rows: [{ line: 1, operation: 'create', action: 'create', request: { name: '导入规则', rule_group_id: '', entry_group_id: device.id, exit_group_id: '', egress_mode: 'DIRECT', protocol: 'tcp', listen_port: 10001, targets: [{ host: '192.0.2.10', port: 443 }], selection_policy: 'round_robin', paused: false, description: '', revision: 0 }, resolved_listen_port: 10001, issues: [] }] })
    mocked.importRules.mockResolvedValue({ created: 1, updated: 0, items: [] })
    const wrapper = mount(ForwardRulesPage, { global: { stubs: { Teleport: true } } })
    await flushPromises()
    await wrapper.findAll('.page-heading button').find((button) => button.text().includes('导入'))!.trigger('click')
    const form = wrapper.get('#forward-rule-import')
    await form.get('textarea').setValue('导入规则#10001#192.0.2.10#443')
    await form.findAll('select')[2]!.setValue(device.id)
    await wrapper.findAll('button').find((button) => button.text().includes('预览并检查'))!.trigger('click')
    await flushPromises()
    expect(mocked.previewRuleImport.mock.calls[0]?.[0].mapping.customer_id).toBe('')
    await form.trigger('submit')
    await flushPromises()
    expect(mocked.importRules).toHaveBeenCalledOnce()
    expect(mocked.importRules.mock.calls[0]?.[0].mapping.customer_id).toBe('')
    expect(mocked.rules).toHaveBeenCalledTimes(2)
    expect(mocked.customers).not.toHaveBeenCalled()
    wrapper.unmount()
  })

  it('searches rules by target and entry or exit device group name', async () => {
    const exit = { ...device, id: 'exit-1', name: '香港出口', kind: 'EXIT' as const }
    const other = { ...device, id: 'entry-2', name: '备用入口' }
    const first: ForwardRule = { id: 'rule-a', name: '甲线路', customer_id: customer.id, rule_group_id: '', entry_group_id: device.id, exit_group_id: exit.id, egress_mode: 'EXIT_GROUP', protocol: 'tcp', listen_port: 10001, targets: [{ host: 'target.example.test', port: 443 }], selection_policy: 'round_robin', paused: false, description: '', revision: 1, status: 'pending_activation' }
    const second: ForwardRule = { ...first, id: 'rule-b', name: '乙线路', entry_group_id: other.id, exit_group_id: '', egress_mode: 'DIRECT', listen_port: 10002, targets: [{ host: 'other.example.test', port: 80 }] }
    mocked.rules.mockResolvedValue({ items: [first, second] })
    mocked.deviceGroups.mockResolvedValue({ items: [device, exit, other] })
    const wrapper = mount(ForwardRulesPage, { global: { stubs: { Teleport: true } } })
    await flushPromises()
    for (const term of ['target.example.test', '入口一', '香港出口']) {
      await wrapper.get('input[placeholder="搜索规则、目标或设备组"]').setValue(term)
      expect(wrapper.find('input[aria-label="选择规则 甲线路"]').exists()).toBe(true)
      expect(wrapper.find('input[aria-label="选择规则 乙线路"]').exists()).toBe(false)
    }
    wrapper.unmount()
  })

  it('searches VLESS rules by the displayed upstream endpoint', async () => {
    const rule: ForwardRule = {
      id: 'vless-search', name: 'Reality 规则', customer_id: '', rule_group_id: '', entry_group_id: device.id,
      exit_group_id: '', egress_mode: 'DIRECT', ingress_protocol: 'vless_reality', protocol: 'tcp', listen_port: 10003,
      targets: [], selection_policy: 'round_robin', paused: false, description: '', revision: 1, status: 'pending_activation',
      vless_socks5_host: '192.217.9.52', vless_socks5_port: 35556,
    }
    mocked.rules.mockResolvedValue({ items: [rule] })
    const wrapper = mount(ForwardRulesPage, { global: { stubs: { Teleport: true } } })
    await flushPromises()
    await wrapper.get('input[placeholder="搜索规则、目标或设备组"]').setValue('192.217.9.52:35556')
    expect(wrapper.find('input[aria-label="选择规则 Reality 规则"]').exists()).toBe(true)
    wrapper.unmount()
  })

  it('paginates rules and resets to the first page when filters or page size change', async () => {
    const rules = Array.from({ length: 21 }, (_, index): ForwardRule => ({
      id: `rule-${index + 1}`,
      name: `分页规则 ${index + 1}`,
      customer_id: '', rule_group_id: '', entry_group_id: device.id, exit_group_id: '',
      egress_mode: 'DIRECT', protocol: 'tcp', listen_port: 10000 + index,
      targets: [{ host: `192.0.2.${index + 1}`, port: 443 }], selection_policy: 'round_robin',
      paused: index === 20, description: '', revision: 1, status: 'pending_activation',
    }))
    mocked.rules.mockResolvedValue({ items: rules })
    const wrapper = mount(ForwardRulesPage, { global: { stubs: { Teleport: true } } })
    await flushPromises()

    expect(wrapper.get('.rule-pagination__summary').text()).toContain('显示 1-20，共 21 条')
    expect(mockedUsage.query).toHaveBeenCalledTimes(20)
    expect(mockedUsage.query.mock.calls.every(([request]) => request.scope_id !== 'rule-21')).toBe(true)
    mockedUsage.query.mockClear()
    expect(wrapper.findAll('input[aria-label="选择规则 分页规则 21"]').length).toBe(0)
    await wrapper.get('.rule-pagination__buttons button:last-child').trigger('click')
    await flushPromises()
    expect(mockedUsage.query.mock.calls.map(([request]) => request.scope_id)).toEqual(['rule-21'])
    expect(wrapper.get('.rule-pagination__summary').text()).toContain('显示 21-21，共 21 条')
    expect(wrapper.findAll('input[aria-label="选择规则 分页规则 21"]').length).toBeGreaterThan(0)

    await wrapper.get('select[aria-label="每页规则数量"]').setValue('10')
    await flushPromises()
    expect(wrapper.get('.rule-pagination__summary').text()).toContain('显示 1-10，共 21 条')
    await wrapper.get('.rule-pagination__buttons button:last-child').trigger('click')
    await wrapper.get('input[placeholder="搜索规则、目标或设备组"]').setValue('分页规则 21')
    await flushPromises()
    expect(wrapper.get('.rule-pagination__summary').text()).toContain('显示 1-1，共 1 条')
    expect(wrapper.findAll('input[aria-label="选择规则 分页规则 21"]').length).toBeGreaterThan(0)
    wrapper.unmount()
  })

  it('coalesces rapid page changes without starting obsolete traffic queues', async () => {
    const rules = Array.from({ length: 41 }, (_, index): ForwardRule => ({
      id: `rule-${index + 1}`, name: `Rule ${index + 1}`, customer_id: '', rule_group_id: '',
      entry_group_id: device.id, exit_group_id: '', egress_mode: 'DIRECT', protocol: 'tcp',
      listen_port: 10000 + index, targets: [{ host: '192.0.2.1', port: 443 }],
      selection_policy: 'round_robin', paused: false, description: '', revision: 1, status: 'active',
    }))
    mocked.rules.mockResolvedValue({ items: rules })
    const pending: (() => void)[] = []
    mockedUsage.query.mockImplementation(() => new Promise((resolve) => {
      pending.push(() => resolve({ items: [], totals: { rule_actual_bytes: 0 }, total: 0 }))
    }))
    const wrapper = mount(ForwardRulesPage, { global: { stubs: { Teleport: true } } })
    await flushPromises()
    expect(mockedUsage.query).toHaveBeenCalledTimes(4)
    await wrapper.get('.rule-pagination__buttons button:last-child').trigger('click')
    await wrapper.get('.rule-pagination__buttons button:last-child').trigger('click')
    expect(mockedUsage.query).toHaveBeenCalledTimes(4)
    pending.splice(0).forEach((resolve) => resolve())
    await flushPromises()
    expect(mockedUsage.query.mock.calls.map(([request]) => request.scope_id)).toEqual([
      'rule-1', 'rule-2', 'rule-3', 'rule-4', 'rule-41',
    ])
    wrapper.unmount()
    pending.splice(0).forEach((resolve) => resolve())
    await flushPromises()
    expect(mockedUsage.query).toHaveBeenCalledTimes(5)
  })

  it('does not schedule polling when the initial load resolves after unmount', async () => {
    let resolveRules!: (value: { items: ForwardRule[] }) => void
    mocked.rules.mockReturnValue(new Promise((resolve) => { resolveRules = resolve }))
    const wrapper = mount(ForwardRulesPage, { global: { stubs: { Teleport: true } } })
    wrapper.unmount()
    const timer = vi.spyOn(window, 'setTimeout')
    try {
      resolveRules({ items: [] })
      await flushPromises()
      expect(timer).not.toHaveBeenCalled()
      expect(mockedUsage.query).not.toHaveBeenCalled()
    } finally { timer.mockRestore() }
  })

  it('keeps the existing edit and copy actions', async () => {
    const rule: ForwardRule = { id: 'rule-edit', name: '原规则', customer_id: customer.id, rule_group_id: '', entry_group_id: device.id, exit_group_id: '', egress_mode: 'DIRECT', protocol: 'tcp', listen_port: 10001, targets: [{ host: '192.0.2.10', port: 443 }], selection_policy: 'round_robin', paused: false, description: '', revision: 1, status: 'pending_activation' }
    mocked.rules.mockResolvedValue({ items: [rule] })
    const wrapper = mount(ForwardRulesPage, { global: { stubs: { Teleport: true } } })
    await flushPromises()
    await wrapper.get('.business-desktop .rule-actions button[title="编辑规则"]').trigger('click')
    expect(wrapper.text()).toContain('编辑规则')
    expect((wrapper.get('#forward-rule-editor input[maxlength="120"]').element as HTMLInputElement).value).toBe('原规则')
    await wrapper.findAll('button').find((item) => item.text() === '取消')!.trigger('click')
    await wrapper.get('.business-desktop .rule-actions button[title="复制规则"]').trigger('click')
    expect(wrapper.text()).toContain('复制规则')
    expect((wrapper.get('#forward-rule-editor input[maxlength="120"]').element as HTMLInputElement).value).toBe('原规则 - 副本')
    wrapper.unmount()
  })

  it('sends selected rule revisions in one transactional batch request', async () => {
    const rule: ForwardRule = { id: 'rule-1', name: '批量规则', customer_id: customer.id, rule_group_id: '', entry_group_id: device.id, exit_group_id: '', egress_mode: 'DIRECT', protocol: 'tcp', listen_port: 10001, targets: [{ host: '192.0.2.10', port: 443 }], selection_policy: 'round_robin', paused: false, description: '', revision: 7, status: 'pending_activation' }
    mocked.rules.mockResolvedValue({ items: [rule] })
    mocked.batchRules.mockResolvedValue({ operation: 'pause', rule_group_id: '', items: [{ ...rule, paused: true, revision: 8, status: 'paused' }] })
    const wrapper = mount(ForwardRulesPage, { global: { stubs: { Teleport: true } } })
    await flushPromises()
    await wrapper.get('input[aria-label="选择规则 批量规则"]').setValue(true)
    const pause = wrapper.findAll('.rule-batch-toolbar button').find((item) => item.text().includes('暂停'))
    expect(pause).toBeDefined()
    await pause!.trigger('click'); await flushPromises()
    expect(mocked.batchRules).toHaveBeenCalledTimes(1)
    expect(mocked.batchRules.mock.calls[0]?.[0]).toEqual({ operation: 'pause', rule_ids: [rule.id], rule_group_id: '', expected_revisions: { [rule.id]: 7 } })
    expect(wrapper.text()).toContain('已暂停')
    wrapper.unmount()
  })

  it('moves selected rules to a group with revisions and locks other actions while pending', async () => {
    const rule: ForwardRule = { id: 'rule-move', name: '待移动规则', customer_id: '', rule_group_id: '', entry_group_id: device.id, exit_group_id: '', egress_mode: 'DIRECT', protocol: 'tcp', listen_port: 10001, targets: [{ host: '192.0.2.10', port: 443 }], selection_policy: 'round_robin', paused: false, description: '', revision: 3, status: 'pending_activation' }
    mocked.rules.mockResolvedValue({ items: [rule] })
    mocked.ruleGroups.mockResolvedValue({ items: [{ id: 'group-1', name: '我的分组', description: '', revision: 1, created_at: '', updated_at: '' }] })
    let resolveBatch!: (value: unknown) => void
    mocked.batchRules.mockReturnValue(new Promise((resolve) => { resolveBatch = resolve }))
    const wrapper = mount(ForwardRulesPage, { global: { stubs: { Teleport: true } } })
    await flushPromises()
    await wrapper.get('input[aria-label="选择规则 待移动规则"]').setValue(true)
    await wrapper.get('.rule-batch-move select').setValue('group-1')
    await wrapper.findAll('.rule-batch-move button').find((button) => button.text().includes('移动'))!.trigger('click')
    expect(mocked.batchRules.mock.calls[0]?.[0]).toEqual({ operation: 'move_group', rule_ids: ['rule-move'], rule_group_id: 'group-1', expected_revisions: { 'rule-move': 3 } })
    expect((wrapper.get('input[aria-label="选择规则 待移动规则"]').element as HTMLInputElement).disabled).toBe(true)
    expect(wrapper.findAll('.page-heading button').every((button) => (button.element as HTMLButtonElement).disabled)).toBe(true)
    resolveBatch({ operation: 'move_group', rule_group_id: 'group-1', items: [{ ...rule, rule_group_id: 'group-1', revision: 4 }] })
    await flushPromises()
    expect(wrapper.text()).toContain('我的分组')
    wrapper.unmount()
  })

  it('requires a modal confirmation and deletes one rule with its revision', async () => {
    const rule: ForwardRule = { id: 'rule-delete', name: '待删除规则', customer_id: customer.id, rule_group_id: '', entry_group_id: device.id, exit_group_id: '', egress_mode: 'DIRECT', protocol: 'tcp', listen_port: 10002, targets: [{ host: '192.0.2.20', port: 443 }], selection_policy: 'round_robin', paused: false, description: '', revision: 4, status: 'pending_activation' }
    mocked.rules.mockResolvedValue({ items: [rule] })
    mocked.batchRules.mockResolvedValue({ operation: 'delete', rule_group_id: '', items: [rule] })
    const wrapper = mount(ForwardRulesPage, { global: { stubs: { Teleport: true } } })
    await flushPromises()
    await wrapper.get('button[aria-label="删除规则 待删除规则"]').trigger('click')
    expect(wrapper.text()).toContain('将永久删除 1 条规则')
    expect(mocked.batchRules).not.toHaveBeenCalled()
    const confirm = wrapper.findAll('button').find((item) => item.text().includes('确认删除'))
    expect(confirm).toBeDefined()
    await confirm!.trigger('click'); await flushPromises()
    expect(mocked.batchRules.mock.calls[0]?.[0]).toEqual({ operation: 'delete', rule_ids: [rule.id], rule_group_id: '', expected_revisions: { [rule.id]: 4 } })
    expect(wrapper.text()).not.toContain('待删除规则')
    wrapper.unmount()
  })

  it('rejects a deletion confirmation after the displayed rule revision changes', async () => {
    const rule: ForwardRule = { id: 'rule-delete', name: '待删除规则', customer_id: '', rule_group_id: '', entry_group_id: device.id, exit_group_id: '', egress_mode: 'DIRECT', protocol: 'tcp', listen_port: 10002, targets: [{ host: '192.0.2.20', port: 443 }], selection_policy: 'round_robin', paused: false, description: '', revision: 4, status: 'pending_activation' }
    mocked.rules.mockResolvedValue({ items: [rule] })
    mocked.saveRule.mockResolvedValue({ ...rule, paused: true, revision: 5, status: 'paused' })
    const wrapper = mount(ForwardRulesPage, { global: { stubs: { Teleport: true } } })
    await flushPromises()
    await wrapper.get('button[aria-label="删除规则 待删除规则"]').trigger('click')
    await wrapper.get('.business-desktop .rule-actions button[title="暂停规则"]').trigger('click')
    await flushPromises()
    await wrapper.findAll('button').find((item) => item.text().includes('确认删除'))!.trigger('click')
    await flushPromises()
    expect(mocked.batchRules).not.toHaveBeenCalled()
    expect(wrapper.text()).toContain('规则已更新，请刷新后重试')
    wrapper.unmount()
  })

  it.each(['batch', 'toggle', 'export'] as const)('hides old rules after a %s request returns 401', async (action) => {
    const rule: ForwardRule = { id: 'rule-expired', name: '旧账号规则', customer_id: '', rule_group_id: '', entry_group_id: device.id, exit_group_id: '', egress_mode: 'DIRECT', protocol: 'tcp', listen_port: 10002, targets: [{ host: '192.0.2.20', port: 443 }], selection_policy: 'round_robin', paused: false, description: '', revision: 4, status: 'pending_activation' }
    mocked.rules.mockResolvedValue({ items: [rule] })
    const expired = new ApiError('expired', 401, 'expired')
    mocked.batchRules.mockRejectedValue(expired)
    mocked.saveRule.mockRejectedValue(expired)
    mocked.exportRules.mockRejectedValue(expired)
    const wrapper = mount(ForwardRulesPage, { global: { stubs: { Teleport: true } } })
    await flushPromises()
    if (action === 'batch') {
      await wrapper.get('input[aria-label="选择规则 旧账号规则"]').setValue(true)
      await wrapper.findAll('.rule-batch-toolbar button').find((button) => button.text().includes('暂停'))!.trigger('click')
    } else if (action === 'toggle') {
      await wrapper.get('.business-desktop .rule-actions button[title="暂停规则"]').trigger('click')
    } else {
      await wrapper.findAll('.page-heading button').find((button) => button.text().includes('导出'))!.trigger('click')
    }
    await flushPromises()
    expect(wrapper.text()).toContain('登录已失效')
    expect(wrapper.text()).not.toContain('旧账号规则')
    expect(wrapper.find('.rule-batch-toolbar').exists()).toBe(false)
    wrapper.unmount()
  })

  it('closes the editor and hides old rules when saving returns 401', async () => {
    const rule: ForwardRule = { id: 'rule-expired', name: '旧账号规则', customer_id: '', rule_group_id: '', entry_group_id: device.id, exit_group_id: '', egress_mode: 'DIRECT', protocol: 'tcp', listen_port: 10002, targets: [{ host: '192.0.2.20', port: 443 }], selection_policy: 'round_robin', paused: false, description: '', revision: 4, status: 'pending_activation' }
    mocked.rules.mockResolvedValue({ items: [rule] })
    mocked.saveRule.mockRejectedValue(new ApiError('expired', 401, 'expired'))
    const wrapper = mount(ForwardRulesPage, { global: { stubs: { Teleport: true } } })
    await flushPromises()
    await wrapper.get('.business-desktop .rule-actions button[title="编辑规则"]').trigger('click')
    await wrapper.get('#forward-rule-editor').trigger('submit')
    await flushPromises()
    expect(wrapper.text()).toContain('登录已失效')
    expect(wrapper.text()).not.toContain('旧账号规则')
    expect(wrapper.find('#forward-rule-editor').exists()).toBe(false)
    wrapper.unmount()
  })
  it('generates the native rule subscription then downloads its ZIP on one click', async () => {
    const rule: ForwardRule = { id: 'rule-auto', name: '自动订阅', customer_id: '', rule_group_id: '', entry_group_id: device.id, exit_group_id: '', egress_mode: 'DIRECT', protocol: 'tcp', ingress_protocol: 'vless_reality', listen_port: 10002, targets: [], selection_policy: 'round_robin', paused: false, description: '', revision: 4, status: 'active' }
    const pack = { filename: 'auto.zip', data_base64: 'UEs=' }
    mocked.rules.mockResolvedValue({ items: [rule] })
    mockedSubscription.generateRule.mockResolvedValue({ subscription: { id: 'sub-auto' }, replayed: false })
    mockedSubscription.package.mockResolvedValue(pack)
    const wrapper = mount(ForwardRulesPage, { global: { stubs: { Teleport: true } } })
    await flushPromises()
    await wrapper.get('.business-desktop button[title="生成订阅并下载导入包"]').trigger('click')
    await flushPromises()
    expect(mockedSubscription.generateRule).toHaveBeenCalledWith(rule.id, expect.any(String))
    expect(mockedSubscription.package).toHaveBeenCalledWith('sub-auto')
    expect(mockedDownload.downloadSubscriptionPackage).toHaveBeenCalledWith(pack)
    mockedSubscription.package.mockRejectedValue(new Error('下载失败，请重试'))
    await wrapper.get('.business-desktop button[title="生成订阅并下载导入包"]').trigger('click')
    await flushPromises()
    expect(wrapper.text()).toContain('下载失败，请重试')
    expect(mockedDownload.downloadSubscriptionPackage).toHaveBeenCalledTimes(1)
    expect(mockedSubscription.generateRule.mock.calls[1]?.[1]).toBe(mockedSubscription.generateRule.mock.calls[0]?.[1])
    wrapper.unmount()
  })

})
