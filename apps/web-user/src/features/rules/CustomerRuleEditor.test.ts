import { flushPromises, mount, type VueWrapper } from '@vue/test-utils'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import type { CustomerRuleDetail, CustomerRuleOptions } from '@/api/types'

const mocked = vi.hoisted(() => ({ saveRule: vi.fn() }))
vi.mock('@/api', () => ({ customerApi: mocked }))
import CustomerRuleEditor from './CustomerRuleEditor.vue'

const options: CustomerRuleOptions = {
  entry_groups: [{ id: 'entry-1', name: '美国入口', connect_host: 'entry.example.test', port_ranges: [{ start: 10000, end: 20000 }], allow_direct: true, allowed_exit_group_ids: [] }],
  exit_groups: [], rule_groups: [],
}
const rule: CustomerRuleDetail = {
  id: 'rule-1', customer_id: 'customer-1', name: '原规则', rule_group_id: '',
  entry_group_id: 'entry-1', exit_group_id: '', egress_mode: 'DIRECT',
  ingress_protocol: 'vless_reality', protocol: 'tcp', vless_flow: 'xtls-rprx-vision',
  vless_flow_explicit: false, vless_outbound_mode: 'DIRECT',
  reality_server_name: 'www.example.com', reality_public_key: 'public-key',
  reality_short_id: '0123456789abcdef', reality_destination: 'www.example.com:443',
  listen_port: 10001, targets: [{ host: '192.0.2.10', port: 443 }],
  selection_policy: 'round_robin', accept_proxy_protocol: false,
  send_proxy_protocol: 0, speed_limit_mbps: 0, ip_limit: 0, connection_limit: 0,
  paused: false, description: '', revision: 5,
}

function mountEditor(existing: CustomerRuleDetail | null = null, copy = false) {
  return mount(CustomerRuleEditor, { props: { options, rule: existing, copy }, global: { stubs: { Teleport: true } } })
}

function field(wrapper: VueWrapper, label: string) {
  const match = wrapper.findAll('label').find((item) => item.find('span').text() === label)
  if (!match) throw new Error(`${label} field missing`)
  return match
}

async function submit(wrapper: VueWrapper) {
  await wrapper.get('form').trigger('submit')
  await flushPromises()
}

beforeEach(() => { mocked.saveRule.mockReset(); mocked.saveRule.mockResolvedValue(rule) })

describe('CustomerRuleEditor VLESS Reality', () => {
  it.each(['tcp', 'udp'] as const)('creates an explicit DIRECT %s rule with one fixed target', async (protocol) => {
    const wrapper = mountEditor()
    await field(wrapper, '名称').get('input').setValue(`新 ${protocol} 规则`)
    await field(wrapper, '入口组').get('select').setValue('entry-1')
    await wrapper.findAll('button').find((item) => item.text() === 'VLESS + Reality')!.trigger('click')
    if (protocol === 'udp') await wrapper.findAll('button').find((item) => item.text() === 'UDP' && item.classes().includes('active') === false)!.trigger('click')
    await field(wrapper, '目标地址').get('textarea').setValue('192.0.2.50:5300')
    await submit(wrapper)
    expect(mocked.saveRule).toHaveBeenCalledOnce()
    expect(mocked.saveRule.mock.calls[0]?.[0]).toMatchObject({
      ingress_protocol: 'vless_reality', protocol, vless_outbound_mode: 'DIRECT',
      vless_flow: 'xtls-rprx-vision', targets: [{ host: '192.0.2.50', port: 5300 }],
    })
    expect(mocked.saveRule.mock.calls[0]?.[1]).toBeNull()
    wrapper.unmount()
  })

  it.each([
    { protocol: 'tcp' as const, flow: 'xtls-rprx-vision', explicit: false },
    { protocol: 'udp' as const, flow: 'xtls-rprx-vision', explicit: true },
    { protocol: 'udp' as const, flow: '', explicit: true },
  ])('keeps an existing DIRECT $protocol Flow "$flow" and Reality identity on name edit', async ({ protocol, flow, explicit }) => {
    const issued = { ...rule, protocol, vless_flow: flow, vless_flow_explicit: explicit }
    const wrapper = mountEditor(issued)
    await field(wrapper, '名称').get('input').setValue('仅改名称')
    await submit(wrapper)
    expect(mocked.saveRule.mock.calls[0]?.[0]).toMatchObject({
      name: '仅改名称', protocol, vless_outbound_mode: 'DIRECT',
      vless_flow: flow, vless_flow_explicit: explicit,
      targets: issued.targets, reality_server_name: issued.reality_server_name,
      reality_public_key: issued.reality_public_key,
      reality_short_id: issued.reality_short_id,
      reality_destination: issued.reality_destination,
    })
    expect(mocked.saveRule.mock.calls[0]?.[1]).toBe(issued.id)
    wrapper.unmount()
  })

  it('preserves Vision when changing a DIRECT fixed target from TCP to UDP', async () => {
    const wrapper = mountEditor(rule)
    await wrapper.findAll('button').find((item) => item.text() === 'UDP' && !item.classes().includes('active'))!.trigger('click')
    await submit(wrapper)
    expect(mocked.saveRule.mock.calls[0]?.[0]).toMatchObject({ protocol: 'udp', vless_flow: 'xtls-rprx-vision', vless_outbound_mode: 'DIRECT' })
    wrapper.unmount()
  })

  it('keeps a pre-existing SOCKS5 landing on an unrelated edit', async () => {
    const issued: CustomerRuleDetail = {
      ...rule, vless_outbound_mode: 'SOCKS5', targets: [],
      vless_socks5_host: 'landing.example.test', vless_socks5_port: 1080,
      vless_socks5_username: 'landing-user',
    }
    const wrapper = mountEditor(issued)
    await field(wrapper, '名称').get('input').setValue('动态规则更名')
    expect(wrapper.text()).toContain('SOCKS5 出站由平台管理')
    await submit(wrapper)
    expect(mocked.saveRule.mock.calls[0]?.[0]).toMatchObject({
      vless_outbound_mode: 'SOCKS5', targets: [],
      vless_socks5_host: 'landing.example.test', vless_socks5_port: 1080,
      vless_socks5_username: 'landing-user', vless_socks5_password: '',
    })
    wrapper.unmount()
  })

  it('rejects a direct copy of a SOCKS5-backed VLESS rule without its write-only password', async () => {
    const issued: CustomerRuleDetail = {
      ...rule, vless_outbound_mode: 'SOCKS5', targets: [],
      vless_socks5_host: 'landing.example.test', vless_socks5_port: 1080,
      vless_socks5_username: 'landing-user',
    }
    const wrapper = mountEditor(issued, true)
    await submit(wrapper)
    expect(mocked.saveRule).not.toHaveBeenCalled()
    expect(wrapper.get('[role="alert"]').text()).toContain('SOCKS5 上游密码不可读取')
    wrapper.unmount()
  })
})
