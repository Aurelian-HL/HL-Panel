import { flushPromises, mount } from '@vue/test-utils'
import { describe, expect, it, vi } from 'vitest'

const apiMocks = vi.hoisted(() => ({ createEndpointPool: vi.fn() }))

vi.mock('@/api', () => ({ api: apiMocks }))

import CreateEndpointPoolDialog from './CreateEndpointPoolDialog.vue'

const groups = [{
  id: 'group-1', name: '广州入口', kind: 'ENTRY' as const, selection_policy: 'weighted_round_robin' as const,
  description: '', member_count: 10, current_generation: 3, created_at: '', updated_at: '',
}]
const networks = [{ group_id: 'group-1', connect_host: 'entry.example.com', port_start: 10000, port_end: 20000, port_ranges: [{ start: 10000, end: 20000 }], direct_policy: 'FORCED' as const, allow_direct: true, allowed_exit_group_ids: [], traffic_multiplier: 1, revision: 1 }]
const rules = [{ id: 'rule-1', name: '广州主线路', entry_group_id: 'group-1', listen_port: 12000, protocol: 'tcp' as const, ingress_protocol: 'vless_reality' as const }]

describe('CreateEndpointPoolDialog', () => {
  it('creates exactly one stable endpoint and never submits member URIs', async () => {
    apiMocks.createEndpointPool.mockResolvedValue({
      pool: {
        id: 'pool-1', name: '广州统一入口', group_id: 'group-1', mode: 'SINGLE_SERVICE_ENDPOINT', protocol: 'vless', hostname: 'entry.example.com', port: 443,
        selection_policy: 'weighted_round_robin', member_count: 10, healthy_candidate_count: 0, created_at: '', updated_at: '',
      }, replayed: false,
    })
    const wrapper = mount(CreateEndpointPoolDialog, { props: { groups, networks, rules: rules as never }, global: { stubs: { Teleport: true } } })
    await wrapper.get('select').setValue('rule-1')
    await wrapper.get('form').trigger('submit')
    await flushPromises()

    expect(apiMocks.createEndpointPool).toHaveBeenCalledOnce()
    const [request] = apiMocks.createEndpointPool.mock.calls[0] as [Record<string, unknown>]
    expect(request).toMatchObject({
      name: '广州主线路', group_id: 'group-1', rule_id: 'rule-1', mode: 'SINGLE_SERVICE_ENDPOINT', protocol: 'vless', hostname: 'entry.example.com', port: 12000,
      selection_policy: 'weighted_round_robin',
    })
    expect(request).not.toHaveProperty('member_uris')
    expect(wrapper.emitted('created')).toHaveLength(1)
    expect(wrapper.text()).toContain('10 台成员')
    expect(wrapper.text()).toContain('VLESS + Reality')
    expect(wrapper.text()).toContain('需完成规则发布和连通验证')
    expect(wrapper.text()).not.toContain('订阅')
  })

  it('requires a rule whose listener belongs to the configured entry network', async () => {
    apiMocks.createEndpointPool.mockClear()
    const wrapper = mount(CreateEndpointPoolDialog, { props: { groups, networks, rules: [{ ...rules[0], listen_port: 30000 }] as never }, global: { stubs: { Teleport: true } } })
    await wrapper.get('form').trigger('submit')

    expect(apiMocks.createEndpointPool).not.toHaveBeenCalled()
    expect(wrapper.text()).toContain('暂无可绑定的 NY TCP、NY SOCKS5 或 VLESS 规则')
  })

  it('offers plain TCP rules as TCP pass-through endpoints', async () => {
    apiMocks.createEndpointPool.mockClear()
    apiMocks.createEndpointPool.mockResolvedValue({ pool: { id: 'tcp-pool' }, replayed: false })
    const wrapper = mount(CreateEndpointPoolDialog, { props: { groups, networks, rules: [
      { ...rules[0], id: 'plain-tcp', name: '普通 TCP', ingress_protocol: 'tcp' },
      { ...rules[0], id: 'legacy-tcp', name: '旧规则', ingress_protocol: undefined },
    ] as never }, global: { stubs: { Teleport: true } } })

    expect(wrapper.get('select').findAll('option')).toHaveLength(3)
    await wrapper.get('select').setValue('plain-tcp')
    expect(wrapper.text()).toContain('NY TCP')
    expect(wrapper.text()).not.toContain('SOCKS5')
    await wrapper.get('form').trigger('submit')
    expect(apiMocks.createEndpointPool).toHaveBeenCalledWith(expect.objectContaining({ protocol: 'tcp', rule_id: 'plain-tcp' }))
  })

  it('maps a SOCKS5 rule to a SOCKS5 endpoint without relabeling it as TCP', async () => {
    apiMocks.createEndpointPool.mockClear()
    apiMocks.createEndpointPool.mockResolvedValue({ pool: { id: 'socks5-pool' }, replayed: false })
    const wrapper = mount(CreateEndpointPoolDialog, { props: { groups, networks, rules: [{ ...rules[0], id: 'socks5-rule', name: 'SOCKS5 入口', ingress_protocol: 'socks5' }] as never }, global: { stubs: { Teleport: true } } })
    await wrapper.get('select').setValue('socks5-rule')
    expect(wrapper.text()).toContain('NY SOCKS5')
    await wrapper.get('form').trigger('submit')
    expect(apiMocks.createEndpointPool).toHaveBeenCalledWith(expect.objectContaining({ protocol: 'socks5', rule_id: 'socks5-rule' }))
  })
})
