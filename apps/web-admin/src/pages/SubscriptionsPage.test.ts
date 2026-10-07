import { flushPromises, mount } from '@vue/test-utils'
import { beforeEach, describe, expect, it, vi } from 'vitest'

const mockedBusiness = vi.hoisted(() => ({
  vlessIdentities: vi.fn(),
  rules: vi.fn(),
  customers: vi.fn(),
  provisionVlessIdentity: vi.fn(),
  vlessIdentityConnection: vi.fn(),
  rotateVlessIdentity: vi.fn(),
  revokeVlessIdentity: vi.fn(),
}))
const mockedAdminApi = vi.hoisted(() => ({ getEndpointPools: vi.fn() }))
vi.mock('@/api/business', () => ({ businessApi: mockedBusiness }))
vi.mock('@/api', () => ({ api: mockedAdminApi }))

import type { EndpointPool } from '@/api'
import type { Customer, ForwardRule, VlessIdentity } from '@/api/business'
import SubscriptionsPage from './SubscriptionsPage.vue'

const rule: ForwardRule = {
  id: 'rule-vless', name: 'Reality 入口', customer_id: 'customer-1', rule_group_id: '', entry_group_id: 'entry-1', exit_group_id: '',
  egress_mode: 'DIRECT', ingress_protocol: 'vless_reality', vless_flow: 'xtls-rprx-vision', reality_server_name: 'edge.example.test',
  reality_public_key: 'public-key', reality_short_id: 'short', reality_destination: 'edge.example.test:443', protocol: 'tcp', listen_port: 443,
  targets: [], selection_policy: 'round_robin', paused: false, description: '', revision: 3, status: 'active', ingress_status: 'ready', deployed: true,
}
const pool: EndpointPool = {
  id: 'pool-vless', name: '统一入口', group_id: 'entry-1', rule_id: rule.id, mode: 'SINGLE_SERVICE_ENDPOINT', protocol: 'vless', hostname: 'sub.example.test', port: 443,
  selection_policy: 'weighted_round_robin', member_count: 1, healthy_candidate_count: 1, created_at: '2026-01-01T00:00:00Z', updated_at: '2026-01-01T00:00:00Z',
}
const customer: Customer = {
  id: 'customer-1', username: 'alice', display_name: 'Alice', user_group_id: '', disabled: false, expires_at: null, traffic_limit_bytes: 0, traffic_used_bytes: 0,
  max_rules: 0, speed_limit_mbps: 0, ip_limit: 0, connection_limit: 0, revision: 1, effective_status: 'active', created_at: '', updated_at: '',
}
const identity: VlessIdentity = {
  id: 'binding-1', customer_id: customer.id, forwarding_rule_id: rule.id, endpoint_pool_id: pool.id, state: 'active', revision: 1,
  created_at: '2026-01-01T00:00:00Z', updated_at: '2026-01-01T00:00:00Z',
}

beforeEach(() => {
  for (const method of Object.values(mockedBusiness)) method.mockReset()
  mockedAdminApi.getEndpointPools.mockReset()
  mockedBusiness.vlessIdentities.mockResolvedValue({ items: [] })
  mockedBusiness.rules.mockResolvedValue({ items: [rule] })
  mockedBusiness.customers.mockResolvedValue({ items: [customer] })
  mockedAdminApi.getEndpointPools.mockResolvedValue({ items: [pool] })
})

describe('SubscriptionsPage', () => {
  it('loads identities, creates one with the rule-bound VLESS pool, and copies its connection URI', async () => {
    mockedBusiness.vlessIdentities.mockResolvedValueOnce({ items: [] }).mockResolvedValue({ items: [identity] })
    mockedBusiness.provisionVlessIdentity.mockResolvedValue({ identity, replayed: false })
    mockedBusiness.vlessIdentityConnection.mockResolvedValue({ uri: 'vless://secret', name: rule.name, endpoint: 'sub.example.test:443', status: 'ready' })
    const writeText = vi.fn().mockResolvedValue(undefined)
    Object.defineProperty(navigator, 'clipboard', { configurable: true, value: { writeText } })
    const wrapper = mount(SubscriptionsPage)
    await flushPromises()

    await wrapper.findAll('button').find((button) => button.text().includes('创建订阅'))!.trigger('click')
    const selects = wrapper.findAll('select')
    await selects[0]!.setValue(rule.id)
    await selects[1]!.setValue(pool.id)
    await wrapper.get('form').trigger('submit')
    await flushPromises()
    expect(mockedBusiness.provisionVlessIdentity).toHaveBeenCalledWith(expect.objectContaining({ customer_id: customer.id, forwarding_rule_id: rule.id, endpoint_pool_id: pool.id }), expect.any(String))
    expect(wrapper.text()).toContain('Reality 入口')

    await wrapper.get('button[title="复制订阅链接"]').trigger('click')
    await flushPromises()
    expect(mockedBusiness.vlessIdentityConnection).toHaveBeenCalledWith(identity.id)
    expect(writeText).toHaveBeenCalledWith('vless://secret')
    wrapper.unmount()
  })

  it('rotates and revokes an active identity with its current revision', async () => {
    mockedBusiness.vlessIdentities.mockResolvedValue({ items: [identity] })
    mockedBusiness.rotateVlessIdentity.mockResolvedValue({ identity: { ...identity, revision: 2 }, replayed: false })
    mockedBusiness.revokeVlessIdentity.mockResolvedValue({ identity: { ...identity, state: 'revoked', revision: 3 }, replayed: false })
    vi.stubGlobal('confirm', vi.fn(() => true))
    const wrapper = mount(SubscriptionsPage)
    await flushPromises()
    await wrapper.get('button[title="轮换订阅凭据"]').trigger('click')
    await flushPromises()
    expect(mockedBusiness.rotateVlessIdentity).toHaveBeenCalledWith(identity.id, identity.revision, expect.any(String))
    await wrapper.get('button[title="撤销订阅"]').trigger('click')
    await flushPromises()
    expect(mockedBusiness.revokeVlessIdentity).toHaveBeenCalledWith(identity.id, identity.revision, expect.any(String))
    wrapper.unmount()
  })

  it('keeps the page usable when the optional customer lookup fails', async () => {
    mockedBusiness.customers.mockRejectedValue(new Error('customers unavailable'))
    const wrapper = mount(SubscriptionsPage)
    await flushPromises()
    expect(wrapper.text()).toContain('还没有订阅')
    expect(wrapper.text()).not.toContain('订阅加载失败')
    wrapper.unmount()
  })
})
