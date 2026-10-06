import { flushPromises, mount, type VueWrapper } from '@vue/test-utils'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import type { DeviceGroup } from '@/api'
import type { ForwardRule, GroupNetwork } from '@/api/business'

const mocked = vi.hoisted(() => ({ saveRule: vi.fn() }))
vi.mock('@/api/business', () => ({ businessApi: mocked }))
import ForwardRuleEditor from './ForwardRuleEditor.vue'

const devices: DeviceGroup[] = [
  { id: 'entry-1', name: '入口一', kind: 'ENTRY', description: '', member_count: 1, current_generation: 0, selection_policy: 'weighted_round_robin', created_at: '', updated_at: '' },
  { id: 'exit-1', name: '出口一', kind: 'EXIT', description: '', member_count: 1, current_generation: 0, selection_policy: 'weighted_round_robin', created_at: '', updated_at: '' },
]
const network: GroupNetwork = { group_id: 'entry-1', connect_host: 'entry.example.test', port_start: 10000, port_end: 20000, direct_policy: 'OPTIONAL', allow_direct: true, allowed_exit_group_ids: ['exit-1'], traffic_multiplier: 1, revision: 1 }
const rule: ForwardRule = { id: 'rule-1', name: '原规则', customer_id: 'c-1', rule_group_id: '', entry_group_id: 'entry-1', exit_group_id: '', egress_mode: 'DIRECT', protocol: 'tcp', listen_port: 10001, targets: [{ host: '192.0.2.10', port: 443 }], selection_policy: 'round_robin', paused: false, description: '', revision: 5, status: 'pending_activation' }
const props = { rule: null as ForwardRule | null, copy: false, devices, networks: [network], ruleGroups: [] }

function mountEditor(overrides: Partial<typeof props> = {}) {
  return mount(ForwardRuleEditor, { props: { ...props, ...overrides }, global: { stubs: { Teleport: true } } })
}
function field(wrapper: VueWrapper, label: string) {
  const match = wrapper.findAll('label.field').find((item) => item.find('span').text() === label)
  if (!match) throw new Error(`${label} field missing`)
  return match
}
function fieldStartingWith(wrapper: VueWrapper, label: string) {
  const match = wrapper.findAll('label.field').find((item) => item.find('span').text().startsWith(label))
  if (!match) throw new Error(`${label} field missing`)
  return match
}
async function populate(wrapper: VueWrapper) {
  await field(wrapper, '名称').get('input').setValue('新规则')
  await field(wrapper, '入口').get('select').setValue('entry-1')
  await field(wrapper, '目标地址').get('textarea').setValue('192.0.2.10:443')
}
async function submit(wrapper: VueWrapper) {
  await wrapper.get('form').trigger('submit')
  await flushPromises()
}

beforeEach(() => { mocked.saveRule.mockReset(); mocked.saveRule.mockResolvedValue(rule) })

describe('ForwardRuleEditor', () => {
  it('follows the NY field order without an endpoint-pool workflow', () => {
    const wrapper = mountEditor()
    expect(wrapper.findAll('form > label.field').map((item) => item.find('span').text())).toEqual(['名称', '接入协议', '入口', '监听端口', '出口', '目标地址'])
    expect(wrapper.text()).not.toContain('服务端点')
    expect(wrapper.text()).not.toContain('转发路径')
    wrapper.unmount()
  })

  it('chooses direct by default and exit routing through the exit dropdown', async () => {
    const wrapper = mountEditor()
    await populate(wrapper)
    await submit(wrapper)
    expect(mocked.saveRule.mock.calls[0]?.[0]).toMatchObject({ egress_mode: 'DIRECT', exit_group_id: '' })
    await field(wrapper, '出口').get('select').setValue('exit-1')
    await submit(wrapper)
    expect(mocked.saveRule.mock.calls[1]?.[0]).toMatchObject({ egress_mode: 'EXIT_GROUP', exit_group_id: 'exit-1' })
    wrapper.unmount()
  })

  it('does not show target connectivity probing in the rule editor', () => {
    const wrapper = mountEditor()
    expect(wrapper.text()).not.toContain('测试目标连通性')
    wrapper.unmount()
  })

  it('requires an exit when the entry disallows direct routing', async () => {
    const wrapper = mountEditor({ networks: [{ ...network, direct_policy: 'DISABLED', allow_direct: false }] })
    await populate(wrapper)
    await submit(wrapper)
    expect(mocked.saveRule).not.toHaveBeenCalled()
    expect(wrapper.text()).toContain('请选择已授权的出口组')
    await field(wrapper, '出口').get('select').setValue('exit-1')
    await submit(wrapper)
    expect(mocked.saveRule).toHaveBeenCalledOnce()
    wrapper.unmount()
  })

  it('blocks an exit no longer authorized for the entry', async () => {
    const exitNetwork: GroupNetwork = { ...network, group_id: 'exit-1', allowed_entry_group_ids: ['another-entry'] }
    const oldRule = { ...rule, egress_mode: 'EXIT_GROUP' as const, exit_group_id: 'exit-1' }
    const wrapper = mountEditor({ rule: oldRule, networks: [network, exitNetwork] })
    await submit(wrapper)
    expect(mocked.saveRule).not.toHaveBeenCalled()
    expect(wrapper.text()).toContain('所选出口组已不可用')
    wrapper.unmount()
  })

  it('keeps SOCKS5 and UDP distinct from TCP', async () => {
    const wrapper = mountEditor()
    await populate(wrapper)
    await field(wrapper, '接入协议').get('select').setValue('socks5')
    await submit(wrapper)
    expect(mocked.saveRule.mock.calls[0]?.[0]).toMatchObject({ ingress_protocol: 'socks5', protocol: 'tcp' })
    await field(wrapper, '接入协议').get('select').setValue('udp')
    await submit(wrapper)
    expect(mocked.saveRule.mock.calls[1]?.[0]).toMatchObject({ ingress_protocol: 'udp', protocol: 'udp' })
    wrapper.unmount()
  })

  it('clears TCP-only limits when switching to UDP', async () => {
    const wrapper = mountEditor({ rule: { ...rule, speed_limit_mbps: 100, ip_limit: 3, connection_limit: 10, accept_proxy_protocol: true, send_proxy_protocol: 3 } })
    await field(wrapper, '接入协议').get('select').setValue('udp')
    await submit(wrapper)
    expect(mocked.saveRule.mock.calls[0]?.[0]).toMatchObject({ protocol: 'udp', speed_limit_mbps: 0, ip_limit: 0, connection_limit: 0, accept_proxy_protocol: false, send_proxy_protocol: 0 })
    wrapper.unmount()
  })

  it('generates VLESS material automatically and locks exit to direct', async () => {
    const wrapper = mountEditor()
    await populate(wrapper)
    await field(wrapper, '出口').get('select').setValue('exit-1')
    await field(wrapper, '接入协议').get('select').setValue('vless_reality')
    await fieldStartingWith(wrapper, '目标地址').get('textarea').setValue('192.0.2.10:443:landing-user:landing-secret')
    expect(field(wrapper, '出口').get('select').element.disabled).toBe(true)
    await submit(wrapper)
    expect(mocked.saveRule.mock.calls[0]?.[0]).toMatchObject({ ingress_protocol: 'vless_reality', egress_mode: 'DIRECT', exit_group_id: '', vless_flow: 'xtls-rprx-vision', reality_public_key: '', reality_short_id: '' })
    wrapper.unmount()
  })

  it('preserves VLESS identity on edit and clears it on copy', async () => {
    const issued = { ...rule, ingress_protocol: 'vless_reality' as const, reality_server_name: 'www.example.com', reality_public_key: 'public-key', reality_short_id: '0123456789abcdef', reality_destination: 'www.example.com:443' }
    const edit = mountEditor({ rule: issued })
    await fieldStartingWith(edit, '目标地址').get('textarea').setValue('192.0.2.10:443:landing-user:landing-secret')
    await submit(edit)
    expect(mocked.saveRule.mock.calls[0]?.[0]).toMatchObject({ reality_public_key: 'public-key', reality_short_id: '0123456789abcdef' })
    edit.unmount()
    const copy = mountEditor({ rule: issued, copy: true })
    await fieldStartingWith(copy, '目标地址').get('textarea').setValue('192.0.2.10:443:landing-user:landing-secret')
    await submit(copy)
    expect(mocked.saveRule.mock.calls[1]?.[0]).toMatchObject({ reality_public_key: '', reality_short_id: '', listen_port: 0, paused: true })
    expect(mocked.saveRule.mock.calls[1]?.[1]).toBeNull()
    copy.unmount()
  })

  it('requires the compact SOCKS5 target when editing a VLESS rule', async () => {
    const issued = { ...rule, ingress_protocol: 'vless_reality' as const, vless_outbound_mode: 'SOCKS5' as const, vless_socks5_host: '198.51.100.20', vless_socks5_port: 6019, vless_socks5_username: '', reality_server_name: 'www.example.com', reality_public_key: 'public-key', reality_short_id: '0123456789abcdef', reality_destination: 'www.example.com:443' }
    const edit = mountEditor({ rule: issued })
    await fieldStartingWith(edit, '目标地址').get('textarea').setValue('198.51.100.20:6019:landing-user:landing-secret')
    await submit(edit)
    expect(mocked.saveRule).toHaveBeenCalledOnce()
    expect(mocked.saveRule.mock.calls[0]?.[0]).toMatchObject({ vless_socks5_username: 'landing-user', vless_socks5_password: 'landing-secret', targets: [] })
    edit.unmount()
  })

  it('allows an existing VLESS rule to keep its stored upstream on edit', async () => {
    const issued = { ...rule, ingress_protocol: 'vless_reality' as const, vless_outbound_mode: 'SOCKS5' as const, vless_socks5_host: '198.51.100.20', vless_socks5_port: 6019, reality_server_name: 'www.example.com', reality_public_key: 'public-key', reality_short_id: '0123456789abcdef', reality_destination: 'www.example.com:443' }
    const edit = mountEditor({ rule: issued })
    expect(fieldStartingWith(edit, '目标地址').get('textarea').element.value).toBe('198.51.100.20:6019')
    await submit(edit)
    expect(mocked.saveRule).toHaveBeenCalledOnce()
    expect(mocked.saveRule.mock.calls[0]?.[0]).toMatchObject({ vless_socks5_host: '198.51.100.20', vless_socks5_port: 6019, vless_socks5_username: '', vless_socks5_password: '' })
    edit.unmount()
  })

  it('accepts a new VLESS rule from one compact SOCKS5 target', async () => {
    const wrapper = mountEditor()
    await field(wrapper, '名称').get('input').setValue('动态目标')
    await field(wrapper, '入口').get('select').setValue('entry-1')
    await field(wrapper, '接入协议').get('select').setValue('vless_reality')
    await fieldStartingWith(wrapper, '目标地址').get('textarea').setValue('198.51.100.20:6019:landing-user:landing-secret')

    await submit(wrapper)
    expect(mocked.saveRule).toHaveBeenCalledOnce()
    expect(mocked.saveRule.mock.calls[0]?.[0]).toMatchObject({
      ingress_protocol: 'vless_reality', vless_outbound_mode: 'SOCKS5', targets: [],
      vless_socks5_host: '198.51.100.20', vless_socks5_port: 6019,
      vless_socks5_username: 'landing-user', vless_socks5_password: 'landing-secret',
    })
    wrapper.unmount()
  })

  it('rejects malformed compact VLESS target rows', async () => {
    const wrapper = mountEditor()
    await field(wrapper, '名称').get('input').setValue('兼容目标')
    await field(wrapper, '入口').get('select').setValue('entry-1')
    await field(wrapper, '接入协议').get('select').setValue('vless_reality')
    await fieldStartingWith(wrapper, '目标地址').get('textarea').setValue('192.0.2.10:443\n192.0.2.11:443')
    await submit(wrapper)
    expect(mocked.saveRule).not.toHaveBeenCalled()
    expect(wrapper.text()).toContain('VLESS 目标地址只能填写一行')
    wrapper.unmount()
  })

  it('rejects multiple VLESS targets', async () => {
    const wrapper = mountEditor()
    await populate(wrapper)
    await field(wrapper, '接入协议').get('select').setValue('vless_reality')
    await field(wrapper, '目标地址').get('textarea').setValue('192.0.2.10:443\n192.0.2.11:443')
    await submit(wrapper)
    expect(mocked.saveRule).not.toHaveBeenCalled()
    expect(wrapper.text()).toContain('VLESS 目标地址只能填写一行')
    wrapper.unmount()
  })

  it('rejects listener ports outside the allowed ranges', async () => {
    const wrapper = mountEditor({ networks: [{ ...network, port_ranges: [{ start: 10000, end: 10010 }, { start: 11000, end: 12000 }] }] })
    await populate(wrapper)
    await field(wrapper, '监听端口').get('input').setValue('10500')
    await submit(wrapper)
    expect(mocked.saveRule).not.toHaveBeenCalled()
    expect(wrapper.text()).toContain('不在入口组允许的端口范围内')
    wrapper.unmount()
  })

  it('never submits the stored owner', async () => {
    const wrapper = mountEditor({ rule })
    await submit(wrapper)
    expect(mocked.saveRule.mock.calls[0]?.[0]).not.toHaveProperty('customer_id')
    wrapper.unmount()
  })
})
