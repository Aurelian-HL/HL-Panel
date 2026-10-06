import { flushPromises, mount } from '@vue/test-utils'
import { describe, expect, it, vi } from 'vitest'

const mocked = vi.hoisted(() => ({ rules: vi.fn(), ruleOptions: vi.fn(), rule: vi.fn(), saveRule: vi.fn(), batchRules: vi.fn() }))
vi.mock('@/api', () => ({ customerApi: mocked }))

import ServicesPage from './ServicesPage.vue'

const vlessOptions = {
  entry_groups: [{ id: 'entry-1', name: '入口', connect_host: 'entry.example.test', port_ranges: [], allow_direct: true, allowed_exit_group_ids: [] }],
  exit_groups: [], rule_groups: [],
}
const vlessRule = () => ({
  id: 'rule-vless', customer_id: 'customer-1', name: 'VLESS 规则', rule_group_id: '', entry_group_id: 'entry-1', exit_group_id: '', egress_mode: 'DIRECT',
  ingress_protocol: 'vless_reality', protocol: 'tcp', vless_flow: 'xtls-rprx-vision', reality_server_name: 'server.example.test', reality_public_key: 'existing-public-key',
  reality_short_id: 'aabbccdd', reality_destination: 'server.example.test:443', listen_port: 14443,
  targets: [{ host: 'target.example.test', port: 443 }], selection_policy: 'round_robin', accept_proxy_protocol: false,
  send_proxy_protocol: 0, speed_limit_mbps: 0, ip_limit: 0, connection_limit: 0, paused: false, description: '', revision: 3,
  connect_host: 'entry.example.test', route_description: '入口直出', status: 'active', deployed: true,
})

describe('ServicesPage', () => {
  it('creates a rule using authorized options without submitting a customer id', async () => {
    mocked.rules.mockResolvedValue([])
    mocked.ruleOptions.mockResolvedValue({ entry_groups: [{ id: 'entry-1', name: '广州入口', connect_host: 'entry.example.test', port_ranges: [{ start: 10000, end: 20000 }], allow_direct: true, allowed_exit_group_ids: [] }], exit_groups: [], rule_groups: [] })
    mocked.saveRule.mockResolvedValue({ id: 'rule-1' })
    const wrapper = mount(ServicesPage, { global: { stubs: { Teleport: true } } })
    await flushPromises()
    await wrapper.findAll('button').find((item) => item.text().includes('添加规则'))!.trigger('click')
    await flushPromises()
    const form = wrapper.get('#customer-rule-form')
    await form.get('input[maxlength="128"]').setValue('测试规则')
    await form.findAll('select')[0]!.setValue('entry-1')
    await form.get('.rule-targets textarea').setValue('example.test:443\n[2001:db8::1]:8443')
    await form.trigger('submit')
    await flushPromises()
    expect(mocked.saveRule).toHaveBeenCalledOnce()
    expect(mocked.saveRule.mock.calls[0]![0]).toMatchObject({ name: '测试规则', entry_group_id: 'entry-1', egress_mode: 'DIRECT', targets: [{ host: 'example.test', port: 443 }, { host: '2001:db8::1', port: 8443 }] })
    expect(mocked.saveRule.mock.calls[0]![0]).not.toHaveProperty('customer_id')
    wrapper.unmount()
  })

  it('selects an authorized exit group instead of direct routing', async () => {
    mocked.rules.mockResolvedValue([])
    mocked.ruleOptions.mockResolvedValue({
      entry_groups: [{ id: 'entry-2', name: '前置入口', connect_host: 'entry.example.test', port_ranges: [], allow_direct: false, allowed_exit_group_ids: ['exit-1'] }],
      exit_groups: [{ id: 'exit-1', name: '香港落地' }], rule_groups: [],
    })
    mocked.saveRule.mockResolvedValue({ id: 'rule-2' })
    const wrapper = mount(ServicesPage, { global: { stubs: { Teleport: true } } })
    await flushPromises()
    await wrapper.findAll('button').find((item) => item.text().includes('添加规则'))!.trigger('click')
    await flushPromises()
    await wrapper.get('#customer-rule-form input[maxlength="128"]').setValue('出口规则')
    await wrapper.get('#customer-rule-form').findAll('select')[0]!.setValue('entry-2')
    await flushPromises()
    expect((wrapper.get('#customer-rule-form').findAll('select')[1]!.element as HTMLSelectElement).disabled).toBe(false)
    await wrapper.get('#customer-rule-form .rule-targets textarea').setValue('landing.example.test:443')
    expect(wrapper.get('#customer-rule-form').text()).toContain('经 香港落地 转发')
    await wrapper.get('#customer-rule-form').findAll('select')[1]!.setValue('exit:exit-1')
    await wrapper.get('#customer-rule-form').trigger('submit')
    await flushPromises()
    expect(mocked.saveRule).toHaveBeenCalledWith(expect.objectContaining({ egress_mode: 'EXIT_GROUP', exit_group_id: 'exit-1' }), null)
    wrapper.unmount()
  })

  it('edits a rule without sending its customer id back to the self-service API', async () => {
    const rule = {
      id: 'rule-1', customer_id: 'customer-1', name: '旧规则', rule_group_id: '', entry_group_id: 'entry-1', exit_group_id: '', egress_mode: 'DIRECT',
      ingress_protocol: 'tcp', protocol: 'tcp', vless_flow: '', reality_server_name: '', reality_public_key: '', reality_short_id: '', reality_destination: '',
      listen_port: 10001, targets: [{ host: 'old.example.test', port: 443 }], selection_policy: 'round_robin', accept_proxy_protocol: false,
      send_proxy_protocol: 0, speed_limit_mbps: 0, ip_limit: 0, connection_limit: 0, paused: false, description: '', revision: 2,
      connect_host: 'entry.example.test', route_description: '入口直出', status: 'active', deployed: true,
    }
    mocked.rules.mockResolvedValue([rule])
    mocked.rule.mockResolvedValue(rule)
    mocked.ruleOptions.mockResolvedValue({ entry_groups: [{ id: 'entry-1', name: '入口', connect_host: 'entry.example.test', port_ranges: [], allow_direct: true, allowed_exit_group_ids: [] }], exit_groups: [], rule_groups: [] })
    mocked.saveRule.mockResolvedValue(rule)
    const wrapper = mount(ServicesPage, { global: { stubs: { Teleport: true } } })
    await flushPromises()
    await wrapper.get('button[aria-label="编辑规则"]').trigger('click')
    await flushPromises()
    const form = wrapper.get('#customer-rule-form')
    expect((form.get('.rule-targets textarea').element as HTMLTextAreaElement).value).toBe('old.example.test:443')
    await form.trigger('submit')
    await flushPromises()
    expect(mocked.saveRule).toHaveBeenCalledWith(expect.not.objectContaining({ customer_id: expect.anything() }), 'rule-1')
    expect(mocked.saveRule.mock.calls[0]![0]).not.toHaveProperty('id')
    expect(mocked.saveRule.mock.calls[0]![0]).not.toHaveProperty('connect_host')
    wrapper.unmount()
  })

  it('uses the server-side rule revision for a pause operation', async () => {
    const rule = { id: 'rule-1', name: '入口', protocol: 'tcp', connect_host: 'entry.example.test', listen_port: 10001, route_description: '入口直出', targets: [{ host: 'example.test', port: 443 }], paused: false, status: 'pending_activation', deployed: false, revision: 2 }
    mocked.rules.mockResolvedValue([rule])
    mocked.rule.mockResolvedValue({ ...rule, revision: 3 })
    mocked.batchRules.mockResolvedValue({ items: [] })
    const wrapper = mount(ServicesPage)
    await flushPromises()
    await wrapper.get('button[aria-label="暂停规则"]').trigger('click')
    await flushPromises()
    expect(mocked.batchRules).toHaveBeenCalledWith('pause', expect.objectContaining({ id: 'rule-1', revision: 3 }))
    wrapper.unmount()
  })

  it('renders the customer rules as a searchable NY-style table', async () => {
    mocked.rules.mockResolvedValue([
      { id: 'rule-1', name: '香港入口', protocol: 'tcp', connect_host: 'entry.example.com', listen_port: 443, route_description: '入口组直连', targets: [{ host: '10.0.0.8', port: 8443 }], paused: false, status: 'active', deployed: true },
      { id: 'rule-2', name: '广州入口', protocol: 'tcp', connect_host: 'gz.example.com', listen_port: 8443, route_description: '出口组转发', targets: [{ host: '10.0.0.9', port: 443 }], paused: false, status: 'active', deployed: false },
    ])
    const wrapper = mount(ServicesPage)
    await flushPromises()

    expect(wrapper.find('.data-table').exists()).toBe(true)
    expect(wrapper.text()).toContain('香港入口')
    expect(wrapper.text()).toContain('已同步')
    await wrapper.get('input[type="search"]').setValue('广州')
    expect(wrapper.text()).not.toContain('香港入口')
    expect(wrapper.text()).toContain('广州入口')
    expect(wrapper.text()).toContain('未同步')
    wrapper.unmount()
  })

  it('shows only customer rules without a separate subscription tab', async () => {
    mocked.rules.mockResolvedValue([])
    const wrapper = mount(ServicesPage)
    await flushPromises()
    expect(wrapper.text()).toContain('暂无转发规则')
    expect(wrapper.text()).not.toContain('订阅')
    expect(wrapper.find('[role="tablist"]').exists()).toBe(false)
    wrapper.unmount()
  })

  it('keeps NY SOCKS5 and VLESS Reality labels distinct from NY TCP', async () => {
    mocked.rules.mockResolvedValue([])
    mocked.ruleOptions.mockResolvedValue({ entry_groups: [{ id: 'entry-1', name: '入口', connect_host: 'entry.example.test', port_ranges: [], allow_direct: true, allowed_exit_group_ids: [] }], exit_groups: [], rule_groups: [] })
    const wrapper = mount(ServicesPage, { global: { stubs: { Teleport: true } } })
    await flushPromises()
    await wrapper.findAll('button').find((item) => item.text().includes('添加规则'))!.trigger('click')
    await flushPromises()
    const protocolChoices = wrapper.get('#customer-rule-form .rule-choice').text()
    expect(protocolChoices).toContain('NY TCP')
    expect(protocolChoices).toContain('NY SOCKS5')
    expect(protocolChoices).toContain('VLESS + Reality + Vision')
    wrapper.unmount()
  })

  it('lets the platform generate Reality material when creating a VLESS rule', async () => {
    mocked.rules.mockResolvedValue([])
    mocked.ruleOptions.mockResolvedValue(vlessOptions)
    mocked.saveRule.mockResolvedValue(vlessRule())
    const wrapper = mount(ServicesPage, { global: { stubs: { Teleport: true } } })
    await flushPromises()
    await wrapper.findAll('button').find((item) => item.text().includes('添加规则'))!.trigger('click')
    await flushPromises()
    const form = wrapper.get('#customer-rule-form')
    await form.findAll('select')[0]!.setValue('entry-1')
    const vlessChoice = form.findAll('.rule-choice button').find((item) => item.text().includes('VLESS'))!
    await vlessChoice.trigger('click')
    const updatedForm = wrapper.get('#customer-rule-form')
    expect(updatedForm.findAll('.rule-choice button').find((item) => item.text().includes('VLESS'))!.classes()).toContain('active')
    expect(updatedForm.text()).toContain('Reality 密钥、Short ID 与 Vision 参数由平台生成')
    expect(updatedForm.findAll('label').some((item) => item.text().includes('Reality 公钥'))).toBe(false)
    await form.get('input[maxlength="128"]').setValue('新 VLESS 规则')
    await form.get('.rule-targets textarea').setValue('target.example.test:443')
    await form.trigger('submit')
    await flushPromises()
    expect(mocked.saveRule).toHaveBeenCalledWith(expect.objectContaining({
      ingress_protocol: 'vless_reality', vless_flow: 'xtls-rprx-vision', egress_mode: 'DIRECT',
      reality_server_name: '', reality_public_key: '', reality_short_id: '', reality_destination: '',
    }), null)
    wrapper.unmount()
  })

  it('preserves generated Reality material when editing a VLESS rule', async () => {
    const rule = vlessRule()
    mocked.rules.mockResolvedValue([rule])
    mocked.rule.mockResolvedValue(rule)
    mocked.ruleOptions.mockResolvedValue(vlessOptions)
    mocked.saveRule.mockResolvedValue(rule)
    const wrapper = mount(ServicesPage, { global: { stubs: { Teleport: true } } })
    await flushPromises()
    await wrapper.get('button[aria-label="编辑规则"]').trigger('click')
    await flushPromises()
    await wrapper.get('#customer-rule-form').trigger('submit')
    await flushPromises()
    expect(mocked.saveRule).toHaveBeenCalledWith(expect.objectContaining({
      reality_server_name: rule.reality_server_name, reality_public_key: rule.reality_public_key,
      reality_short_id: rule.reality_short_id, reality_destination: rule.reality_destination,
    }), rule.id)
    wrapper.unmount()
  })

  it('copies a VLESS rule without reusing its port, revision, or Reality material', async () => {
    const rule = vlessRule()
    mocked.rules.mockResolvedValue([rule])
    mocked.rule.mockResolvedValue(rule)
    mocked.ruleOptions.mockResolvedValue(vlessOptions)
    mocked.saveRule.mockResolvedValue({ ...rule, id: 'copied-rule' })
    const wrapper = mount(ServicesPage, { global: { stubs: { Teleport: true } } })
    await flushPromises()
    await wrapper.get('button[aria-label="复制规则"]').trigger('click')
    await flushPromises()
    expect(wrapper.get('#customer-rule-title').text()).toBe('复制规则')
    expect((wrapper.get('#customer-rule-form input[type="number"]').element as HTMLInputElement).value).toBe('0')
    await wrapper.get('#customer-rule-form').trigger('submit')
    await flushPromises()
    expect(mocked.saveRule).toHaveBeenCalledWith(expect.objectContaining({
      name: 'VLESS 规则 - 副本', listen_port: 0, revision: 0, paused: true,
      reality_server_name: '', reality_public_key: '', reality_short_id: '', reality_destination: '',
    }), null)
    wrapper.unmount()
  })
})
