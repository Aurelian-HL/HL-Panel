import { flushPromises, mount, type VueWrapper } from '@vue/test-utils'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import type { DeviceGroup } from '@/api'
import type { ForwardRule, GroupNetwork } from '@/api/business'

const mocked = vi.hoisted(() => ({ saveRule: vi.fn(), realityTargets: vi.fn().mockResolvedValue({ items: [] }), probeReality: vi.fn() }))
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

beforeEach(() => {
  mocked.saveRule.mockReset(); mocked.saveRule.mockResolvedValue(rule)
  mocked.realityTargets.mockReset(); mocked.realityTargets.mockResolvedValue({ items: [] })
  mocked.probeReality.mockReset(); mocked.probeReality.mockImplementation(async (target) => ({ ...target, compatible: true, latency_ms: 30, tls_version: 'TLS 1.3', key_exchange: 'X25519', source: 'control_plane', checked_at: '2026-10-20T00:00:00Z' }))
})
afterEach(() => { vi.useRealTimers(); vi.restoreAllMocks() })

describe('ForwardRuleEditor', () => {
  it('pairs random Reality fields, offers another candidate and preserves them on edit', async () => {
    const pool = [
      { server_name: 'www.apple.com', destination: 'www.apple.com:443' },
      { server_name: 'www.cloudflare.com', destination: 'www.cloudflare.com:443' },
      { server_name: 'www.icloud.com', destination: 'www.icloud.com:443' },
      { server_name: 'www.amazon.com', destination: 'www.amazon.com:443' },
    ]
    mocked.realityTargets.mockResolvedValue({ items: pool })
    const wrapper = mountEditor()
    await flushPromises()
    expect(mocked.realityTargets).not.toHaveBeenCalled()
    await field(wrapper, '接入协议').get('select').setValue('vless_reality')
    await flushPromises()
    expect(mocked.probeReality).toHaveBeenCalledTimes(4)
    const initial = field(wrapper, 'Reality SNI').get('input').element.value
    expect(pool.map((item) => item.server_name)).toContain(initial)
    expect(field(wrapper, 'Reality Target').get('input').element.value).toBe(`${initial}:443`)
    await wrapper.findAll('button').find((item) => item.text().includes('随机更换 Reality 目标'))!.trigger('click')
    await flushPromises()
    expect(mocked.probeReality).toHaveBeenCalledTimes(8)
    const next = field(wrapper, 'Reality SNI').get('input').element.value
    expect(next).not.toBe(initial)
    expect(field(wrapper, 'Reality Target').get('input').element.value).toBe(`${next}:443`)
    wrapper.unmount()
    mocked.realityTargets.mockClear()
    const edit = mountEditor({ rule: { ...rule, ingress_protocol: 'vless_reality', reality_server_name: 'existing.example.com', reality_destination: 'existing.example.com:443' } })
    await flushPromises()
    expect(mocked.realityTargets).not.toHaveBeenCalled()
    expect(field(edit, 'Reality SNI').get('input').element.value).toBe('existing.example.com')
    edit.unmount()
  })
  it('debounces manual edits, ignores obsolete responses and permits a retry', async () => {
    vi.useFakeTimers()
    const wrapper = mountEditor({ rule: { ...rule, ingress_protocol: 'vless_reality', reality_server_name: 'old.example.test', reality_destination: 'old.example.test:443' } })
    let resolveOld: (value: unknown) => void = () => {}
    mocked.probeReality.mockImplementationOnce(() => new Promise((resolve) => { resolveOld = resolve }))
    await vi.advanceTimersByTimeAsync(500)
    expect(mocked.probeReality).toHaveBeenCalledOnce()
    await field(wrapper, 'Reality Target').get('input').setValue('www.apple.com:443')
    await field(wrapper, 'Reality SNI').get('input').setValue('www.apple.com')
    await vi.advanceTimersByTimeAsync(499)
    expect(mocked.probeReality).toHaveBeenCalledOnce()
    await vi.advanceTimersByTimeAsync(1)
    await flushPromises()
    expect(mocked.probeReality).toHaveBeenCalledTimes(2)
    expect(wrapper.text()).toContain('面板检测通过 · 30 ms')
    resolveOld({ compatible: false, message: '旧目标失败' })
    await flushPromises()
    expect(wrapper.text()).not.toContain('旧目标失败')
    mocked.probeReality.mockRejectedValueOnce(new Error('检测暂不可用'))
    await wrapper.findAll('button').find((item) => item.text() === '复测当前目标')!.trigger('click')
    await flushPromises()
    expect(wrapper.text()).toContain('检测暂不可用')
    await wrapper.findAll('button').find((item) => item.text() === '复测当前目标')!.trigger('click')
    await flushPromises()
    expect(wrapper.text()).toContain('面板检测通过 · 30 ms')
    expect(wrapper.text()).not.toContain('检测暂不可用')
    wrapper.unmount()
    await vi.advanceTimersByTimeAsync(1000)
    expect(mocked.probeReality).toHaveBeenCalledTimes(4)
  })
  it('chooses only a passing candidate and keeps manual input while the pool is pending', async () => {
    const pool = [{ server_name: 'www.apple.com', destination: 'www.apple.com:443' }, { server_name: 'www.amazon.com', destination: 'www.amazon.com:443' }]
    mocked.realityTargets.mockResolvedValue({ items: pool })
    mocked.probeReality.mockImplementation(async (target) => ({ ...target, compatible: target.server_name === 'www.apple.com', latency_ms: 20, message: '检测失败' }))
    const wrapper = mountEditor()
    await field(wrapper, '接入协议').get('select').setValue('vless_reality')
    await flushPromises()
    expect(field(wrapper, 'Reality SNI').get('input').element.value).toBe('www.apple.com')
    const finishes: Array<() => void> = []
    mocked.probeReality.mockImplementation((target) => new Promise((resolve) => { finishes.push(() => resolve({ ...target, compatible: true })) }))
    await wrapper.findAll('button').find((item) => item.text().includes('随机更换 Reality 目标'))!.trigger('click')
    await flushPromises()
    await field(wrapper, 'Reality SNI').get('input').setValue('manual.example.test')
    expect(finishes).toHaveLength(2)
    finishes.forEach((finish) => finish())
    await flushPromises()
    expect(field(wrapper, 'Reality SNI').get('input').element.value).toBe('manual.example.test')
    wrapper.unmount()
  })
  it('defaults a newly selected TCP exit to QUIC and saves KCP explicitly', async () => {
    const wrapper = mountEditor()
    await populate(wrapper)
    await field(wrapper, '出口').get('select').setValue('exit-1')
    expect(field(wrapper, '节点间隧道').get('select').element.value).toBe('quic')
    await field(wrapper, '节点间隧道').get('select').setValue('kcp')
    await submit(wrapper)
    expect(mocked.saveRule.mock.calls[0]?.[0]).toMatchObject({ egress_mode: 'EXIT_GROUP', tunnel_transport: 'kcp' })
    await field(wrapper, '出口').get('select').setValue('')
    await submit(wrapper)
    expect(mocked.saveRule.mock.calls[1]?.[0]).toMatchObject({ egress_mode: 'DIRECT', tunnel_transport: '' })
    wrapper.unmount()
  })

  it.each([false, true])('preserves a saved tunnel on edit and copy (copy=%s)', async (copy) => {
    const wrapper = mountEditor({ rule: { ...rule, egress_mode: 'EXIT_GROUP', exit_group_id: 'exit-1', tunnel_transport: 'kcp' }, copy })
    expect(field(wrapper, '节点间隧道').get('select').element.value).toBe('kcp')
    await submit(wrapper)
    expect(mocked.saveRule.mock.calls[0]?.[0]).toMatchObject({ tunnel_transport: 'kcp' })
    wrapper.unmount()
  })

  it('requires explicit transport selection for a legacy exit rule', async () => {
    const wrapper = mountEditor({ rule: { ...rule, egress_mode: 'EXIT_GROUP', exit_group_id: 'exit-1' } })
    expect(field(wrapper, '节点间隧道').get('select').element.value).toBe('')
    await submit(wrapper)
    expect(mocked.saveRule).not.toHaveBeenCalled()
    await field(wrapper, '节点间隧道').get('select').setValue('quic')
    await submit(wrapper)
    expect(mocked.saveRule.mock.calls[0]?.[0]).toMatchObject({ tunnel_transport: 'quic' })
    wrapper.unmount()
  })

  it('edits imported metadata without rewriting connection parameters', async () => {
    const issued: ForwardRule = { ...rule, ingress_protocol: 'vless_reality', targets: [], vless_outbound_mode: 'KL_NATIVE', vless_flow: '', vless_flow_explicit: true, reality_server_name: 'example.com', reality_public_key: 'public', reality_short_id: '', reality_destination: 'example.com:443' }
    const wrapper = mountEditor({ rule: issued })
    await wrapper.get('input[maxlength="160"]').setValue('迁入线路')
    await submit(wrapper)
    const payload = mocked.saveRule.mock.calls[0]?.[0]
    expect(payload).toMatchObject({ name: '迁入线路', vless_outbound_mode: 'KL_NATIVE', listen_port: issued.listen_port, vless_flow: '', vless_flow_explicit: true, reality_short_id: '', reality_public_key: 'public', targets: [] })
    expect(wrapper.text()).toContain('继续使用已有订阅')
    expect(wrapper.findAll('select')).toHaveLength(0)
    wrapper.unmount()
  })
  it.each([false, true])('preserves explicit empty flow on edit or copy (copy=%s)', async (copy) => {
    const issued: ForwardRule = { ...rule, ingress_protocol: 'vless_reality', vless_flow: '', vless_flow_explicit: true, vless_outbound_mode: 'SOCKS5', vless_socks5_host: '198.51.100.20', vless_socks5_port: 6019, reality_server_name: 'www.example.com', reality_public_key: 'public-key', reality_short_id: '0123456789abcdef', reality_destination: 'www.example.com:443' }
    const wrapper = mountEditor({ rule: issued, copy })
    if (copy) await fieldStartingWith(wrapper, '目标地址').get('textarea').setValue('198.51.100.20:6019:user:secret')
    expect(wrapper.text()).toContain('空（保留原配置）')
    await submit(wrapper)
    expect(mocked.saveRule).toHaveBeenCalledOnce()
    expect(mocked.saveRule.mock.calls[0]?.[0]).toMatchObject({ vless_flow: '', vless_flow_explicit: true })
    wrapper.unmount()
  })

  it('follows the NY field order without an endpoint-pool workflow', () => {
    const wrapper = mountEditor()
    expect(wrapper.findAll('form > label.field').map((item) => item.find('span').text())).toEqual(['名称', '接入协议', '入口', '监听端口', '出口', '目标地址', '规则流量额度（GB）', '每月清零额度'])
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

  it('shows VLESS SOCKS5 as TCP+UDP without changing an existing Vision URI', async () => {
    const wrapper = mountEditor()
    await populate(wrapper)
    await field(wrapper, '接入协议').get('select').setValue('vless_reality')
    expect(wrapper.findAll('label.field').some((item) => item.find('span').text() === '目标协议')).toBe(false)
    await fieldStartingWith(wrapper, '目标地址').get('textarea').setValue('192.0.2.10:443:landing-user:landing-secret')
    await submit(wrapper)

    expect(mocked.saveRule.mock.calls[0]?.[0]).toMatchObject({
      ingress_protocol: 'vless_reality', protocol: 'tcp', egress_mode: 'DIRECT',
      vless_flow: 'xtls-rprx-vision', vless_flow_explicit: false,
    })
    expect(wrapper.text()).toContain('VLESS + Reality / TCP')
    expect(wrapper.text()).toContain('UDP ASSOCIATE')
    wrapper.unmount()
  })

  it.each(['tcp', 'udp'] as const)('still edits an existing fixed %s target', async (protocol) => {
    const wrapper = mountEditor({ rule: { ...rule, ingress_protocol: 'vless_reality', vless_outbound_mode: 'DIRECT', vless_flow: 'xtls-rprx-vision' } })
    await field(wrapper, '目标协议').get('select').setValue(protocol)
    await field(wrapper, '目标地址').get('textarea').setValue('192.0.2.50:5300')
    await submit(wrapper)
    expect(mocked.saveRule).toHaveBeenCalledOnce()
    expect(mocked.saveRule.mock.calls[0]?.[0]).toMatchObject({
      ingress_protocol: 'vless_reality', protocol, egress_mode: 'DIRECT',
      vless_outbound_mode: 'DIRECT', vless_flow: 'xtls-rprx-vision',
      targets: [{ host: '192.0.2.50', port: 5300 }],
      vless_socks5_host: '', vless_socks5_port: 0,
      vless_socks5_username: '', vless_socks5_password: '',
    })
    wrapper.unmount()
  })

  it('preserves an issued DIRECT URI and mode on edit', async () => {
    const issued: ForwardRule = {
      ...rule, ingress_protocol: 'vless_reality', protocol: 'udp',
      vless_outbound_mode: 'DIRECT', vless_flow: '', vless_flow_explicit: true,
      reality_server_name: 'www.example.com', reality_destination: 'www.example.com:443',
      reality_public_key: 'public-key', reality_short_id: '0123456789abcdef',
    }
    const wrapper = mountEditor({ rule: issued })
    expect(field(wrapper, 'VLESS 出站').get('select').element.value).toBe('DIRECT')
    expect(field(wrapper, '目标协议').get('select').element.value).toBe('udp')
    expect(field(wrapper, '目标地址').get('textarea').element.value).toBe('192.0.2.10:443')
    await field(wrapper, '名称').get('input').setValue('仅改名称')
    await submit(wrapper)
    expect(mocked.saveRule).toHaveBeenCalledOnce()
    expect(mocked.saveRule.mock.calls[0]?.[0]).toMatchObject({
      vless_outbound_mode: 'DIRECT', protocol: 'udp', vless_flow: '', vless_flow_explicit: true,
      targets: issued.targets, reality_server_name: issued.reality_server_name,
      reality_destination: issued.reality_destination,
      reality_public_key: issued.reality_public_key,
      reality_short_id: issued.reality_short_id,
    })
    wrapper.unmount()
  })

  it('does not offer fixed targets when creating, copying or editing a SOCKS5 rule', async () => {
    const issued: ForwardRule = {
      ...rule, ingress_protocol: 'vless_reality', vless_outbound_mode: 'SOCKS5', targets: [],
      vless_socks5_host: '198.51.100.20', vless_socks5_port: 6019,
      vless_socks5_username: 'landing-user',
      reality_server_name: 'www.example.com', reality_destination: 'www.example.com:443',
      reality_public_key: 'public-key', reality_short_id: '0123456789abcdef',
    }
    for (const overrides of [{}, { rule: issued }, { rule: { ...issued, vless_outbound_mode: 'DIRECT' as const }, copy: true }]) {
      const wrapper = mountEditor(overrides)
      if (!overrides.rule) await field(wrapper, '接入协议').get('select').setValue('vless_reality')
      expect(field(wrapper, 'VLESS 出站').findAll('option').map((item) => item.attributes('value'))).toEqual(['SOCKS5'])
      wrapper.unmount()
    }
  })

  it('keeps the URI flow of a legacy UDP rule during an unrelated edit', async () => {
    const legacy = { ...rule, ingress_protocol: 'vless_reality' as const, protocol: 'udp' as const, vless_flow: '', vless_flow_explicit: false, vless_outbound_mode: 'SOCKS5' as const, vless_socks5_host: '198.51.100.20', vless_socks5_port: 6019 }
    const wrapper = mountEditor({ rule: legacy })
    await field(wrapper, '名称').get('input').setValue('旧规则更名')
    await submit(wrapper)
    expect(mocked.saveRule.mock.calls[0]?.[0]).toMatchObject({ protocol: 'udp', vless_flow: '', vless_flow_explicit: false })
    expect(wrapper.text()).toContain('Flow：空（保留原配置）')
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
    await flushPromises()
    await fieldStartingWith(copy, '目标地址').get('textarea').setValue('192.0.2.10:443:landing-user:landing-secret')
    await submit(copy)
    expect(mocked.saveRule.mock.calls[1]?.[0]).toMatchObject({ reality_public_key: '', reality_short_id: '', reality_server_name: '', reality_destination: '', listen_port: 0, paused: true })
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

  it('requires fresh SOCKS5 credentials when copying a VLESS rule', async () => {
    const issued: ForwardRule = {
      ...rule, ingress_protocol: 'vless_reality', vless_outbound_mode: 'SOCKS5', targets: [],
      vless_socks5_host: '198.51.100.20', vless_socks5_port: 6019,
      vless_socks5_username: 'landing-user', reality_server_name: 'www.example.com',
      reality_public_key: 'public-key', reality_short_id: '0123456789abcdef',
      reality_destination: 'www.example.com:443',
    }
    const copy = mountEditor({ rule: issued, copy: true })
    await flushPromises()
    expect(fieldStartingWith(copy, '目标地址').get('textarea').element.value).toBe('')
    expect(copy.text()).toContain('上游密码不可读取')
    await submit(copy)
    expect(mocked.saveRule).not.toHaveBeenCalled()
    expect(copy.get('[role="alert"]').text()).toContain('重新填写上游地址、账号和密码')
    await fieldStartingWith(copy, '目标地址').get('textarea').setValue('198.51.100.20:6019:landing-user:fresh-secret')
    await submit(copy)
    expect(mocked.saveRule).toHaveBeenCalledOnce()
    expect(mocked.saveRule.mock.calls[0]?.[0]).toMatchObject({
      vless_outbound_mode: 'SOCKS5', vless_socks5_password: 'fresh-secret',
      reality_public_key: '', reality_short_id: '', revision: 0, listen_port: 0,
    })
    copy.unmount()
  })

  it('saves a custom Reality target while leaving key generation automatic', async () => {
    const wrapper = mountEditor()
    await populate(wrapper)
    await field(wrapper, '接入协议').get('select').setValue('vless_reality')
    await field(wrapper, '目标地址').get('textarea').setValue('198.51.100.20:6019:user:secret')
    await field(wrapper, 'Reality SNI').get('input').setValue('www.example.com')
    await submit(wrapper)
    expect(mocked.saveRule.mock.calls[0]?.[0]).toMatchObject({ reality_server_name: 'www.example.com', reality_destination: 'www.example.com:443', reality_public_key: '', reality_short_id: '' })
    wrapper.unmount()
  })

  it('requires SNI for a custom Target and warns on an issued target change', async () => {
    const issued = { ...rule, ingress_protocol: 'vless_reality' as const, reality_server_name: 'www.example.com', reality_public_key: 'public-key', reality_short_id: '0123456789abcdef', reality_destination: 'www.example.com:443' }
    const edit = mountEditor({ rule: issued })
    await field(edit, '目标地址').get('textarea').setValue('198.51.100.20:6019:user:secret')
    await field(edit, 'Reality Target').get('input').setValue('other.example.com:443')
    await field(edit, 'Reality SNI').get('input').setValue('')
    await submit(edit)
    expect(mocked.saveRule).not.toHaveBeenCalled()
    expect(edit.text()).toContain('也需要填写对应的 SNI')
    await field(edit, 'Reality SNI').get('input').setValue('other.example.com')
    expect(edit.text()).toContain('客户需要刷新订阅或重新导入节点')
    await submit(edit)
    expect(mocked.saveRule.mock.calls[0]?.[0]).toMatchObject({ reality_public_key: 'public-key', reality_short_id: '0123456789abcdef', reality_server_name: 'other.example.com', reality_destination: 'other.example.com:443' })
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
  it('converts a binary GB quota and preserves it when editing', async () => {
    const wrapper = mountEditor({ rule: { ...rule, traffic_limit_bytes: 2.5 * 1024 ** 3, traffic_used_bytes: 1024 ** 3 } })
    expect(field(wrapper, '规则流量额度（GB）').get('input').element.value).toBe('2.5')
    expect(wrapper.text()).toContain('已累计 1.000 GB')
    await field(wrapper, '规则流量额度（GB）').get('input').setValue('0.125')
    await submit(wrapper)
    expect(mocked.saveRule.mock.calls[0]?.[0]).toMatchObject({ traffic_limit_bytes: 0.125 * 1024 ** 3 })
    expect(mocked.saveRule.mock.calls[0]?.[0]).not.toHaveProperty('traffic_used_bytes')
    await field(wrapper, '规则流量额度（GB）').get('input').setValue('0')
    await submit(wrapper)
    expect(mocked.saveRule.mock.calls[1]?.[0]).toMatchObject({ traffic_limit_bytes: 0 })
    wrapper.unmount()
  })
  it('submits the monthly quota mode and preserves it while editing', async () => {
    const wrapper = mountEditor({ rule: { ...rule, traffic_quota_monthly: true } })
    expect(field(wrapper, '每月清零额度').get('input').element.checked).toBe(true)
    await submit(wrapper)
    expect(mocked.saveRule.mock.calls[0]?.[0]).toMatchObject({ traffic_quota_monthly: true })
    wrapper.unmount()
  })
  it('keeps the 1024 GB to 1 TB boundary exact', async () => {
    const wrapper = mountEditor()
    await populate(wrapper)
    await field(wrapper, '规则流量额度（GB）').get('input').setValue('1024')
    await submit(wrapper)
    expect(mocked.saveRule.mock.calls[0]?.[0]).toMatchObject({ traffic_limit_bytes: 1024 ** 4 })
    wrapper.unmount()
  })
  it('rejects negative or unsafe quotas', async () => {
    const wrapper = mountEditor({ rule })
    for (const value of ['-1', '9007200']) {
      await field(wrapper, '规则流量额度（GB）').get('input').setValue(value)
      await submit(wrapper)
      expect(mocked.saveRule).not.toHaveBeenCalled()
      expect(wrapper.text()).toContain('请输入有效的流量额度')
    }
    wrapper.unmount()
  })

})
