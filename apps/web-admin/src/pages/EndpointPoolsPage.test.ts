import { flushPromises, mount } from '@vue/test-utils'
import { beforeEach, describe, expect, it, vi } from 'vitest'

const apiMocks = vi.hoisted(() => ({ getEndpointPools: vi.fn(), getDeviceGroups: vi.fn(), deleteEndpointPool: vi.fn() }))
const businessMocks = vi.hoisted(() => ({ groupNetworks: vi.fn(), rules: vi.fn() }))
const toastMocks = vi.hoisted(() => ({ success: vi.fn() }))

vi.mock('@/api', () => ({ api: apiMocks }))
vi.mock('@/api/business', () => ({ businessApi: businessMocks }))
vi.mock('@/composables/toast', () => ({ toast: toastMocks }))

import EndpointPoolsPage from './EndpointPoolsPage.vue'

const pool = {
  id: 'pool-1', name: '广州入口', group_id: 'group-1', mode: 'SINGLE_SERVICE_ENDPOINT', protocol: 'vless',
  hostname: 'entry.example.test', port: 443, selection_policy: 'weighted_round_robin',
  member_count: 1, healthy_candidate_count: 0, created_at: '', updated_at: '',
}

describe('EndpointPoolsPage deletion', () => {
  beforeEach(() => {
    vi.clearAllMocks()
    apiMocks.getEndpointPools.mockResolvedValue({ items: [{ ...pool }] })
    apiMocks.getDeviceGroups.mockResolvedValue({ items: [{ id: 'group-1', name: '入口组' }] })
    businessMocks.groupNetworks.mockResolvedValue({ items: [] })
    businessMocks.rules.mockResolvedValue({ items: [] })
  })

  it('requires confirmation, then removes only the deleted pool', async () => {
    apiMocks.deleteEndpointPool.mockResolvedValue({ replayed: false })
    const wrapper = mount(EndpointPoolsPage, { global: { stubs: { Teleport: true } } })
    await flushPromises()

    await wrapper.get('[aria-label="删除服务端点 广州入口"]').trigger('click')
    expect(apiMocks.deleteEndpointPool).not.toHaveBeenCalled()
    expect(wrapper.get('[role="dialog"]').text()).toContain('规则与该端点的绑定会解除')
    await wrapper.get('[role="dialog"] .button--danger').trigger('click')
    await flushPromises()

    expect(apiMocks.deleteEndpointPool).toHaveBeenCalledOnce()
    expect(apiMocks.deleteEndpointPool.mock.calls[0]?.[0]).toBe(pool.id)
    expect(apiMocks.deleteEndpointPool.mock.calls[0]?.[1]).toEqual(expect.any(String))
    expect(wrapper.find('[role="dialog"]').exists()).toBe(false)
    expect(wrapper.text()).toContain('还没有服务端点')
    expect(toastMocks.success).toHaveBeenCalledOnce()
  })

  it('keeps the target and stable key after a rejected deletion so it can retry', async () => {
    apiMocks.deleteEndpointPool.mockRejectedValueOnce(new Error('客户绑定，无法删除')).mockResolvedValueOnce({ replayed: true })
    const wrapper = mount(EndpointPoolsPage, { global: { stubs: { Teleport: true } } })
    await flushPromises()

    await wrapper.get('[aria-label="删除服务端点 广州入口"]').trigger('click')
    await wrapper.get('[role="dialog"] .button--danger').trigger('click')
    await flushPromises()
    expect(wrapper.text()).toContain('客户绑定，无法删除')
    expect(wrapper.text()).toContain('广州入口')

    await wrapper.get('[role="dialog"] .button--danger').trigger('click')
    await flushPromises()
    expect(apiMocks.deleteEndpointPool).toHaveBeenCalledTimes(2)
    expect(apiMocks.deleteEndpointPool.mock.calls[1]?.[1]).toBe(apiMocks.deleteEndpointPool.mock.calls[0]?.[1])
    expect(wrapper.text()).toContain('还没有服务端点')
  })

  it('locks confirmation and closing while deletion is in flight', async () => {
    let resolveDeletion!: (value: { replayed: boolean }) => void
    apiMocks.deleteEndpointPool.mockReturnValue(new Promise((resolve) => { resolveDeletion = resolve }))
    const wrapper = mount(EndpointPoolsPage, { global: { stubs: { Teleport: true } } })
    await flushPromises()

    await wrapper.get('[aria-label="删除服务端点 广州入口"]').trigger('click')
    await wrapper.get('[role="dialog"] .button--danger').trigger('click')
    expect(wrapper.get('[role="dialog"] .button--danger').attributes('disabled')).toBeDefined()
    expect(wrapper.get('[role="dialog"] .button--secondary').attributes('disabled')).toBeDefined()
    await wrapper.get('[role="dialog"] .button--danger').trigger('click')
    expect(apiMocks.deleteEndpointPool).toHaveBeenCalledOnce()

    resolveDeletion({ replayed: false })
    await flushPromises()
    expect(wrapper.find('[role="dialog"]').exists()).toBe(false)
  })

  it('separates NY TCP passthrough from legacy SOCKS5 markers', async () => {
    apiMocks.getEndpointPools.mockResolvedValue({ items: [
      { ...pool, id: 'tcp-pool', name: 'NY 入口', protocol: 'tcp' },
      { ...pool, id: 'legacy-pool', name: '历史入口', protocol: 'socks5' },
      { ...pool, id: 'vless-pool', name: 'Reality 入口', protocol: 'vless' },
    ] })
    const wrapper = mount(EndpointPoolsPage, { global: { stubs: { Teleport: true } } })
    await flushPromises()

    const filters = wrapper.findAll('[aria-label="协议筛选"] button')
    expect(filters.map((button) => button.text())).toEqual(['全部', 'VLESS', 'NY TCP 透传', 'NY SOCKS5'])
    await filters[2]!.trigger('click')
    expect(wrapper.find('.endpoint-table').text()).toContain('NY 入口')
    expect(wrapper.find('.endpoint-table').text()).not.toContain('历史入口')
    expect(wrapper.find('.endpoint-table').text()).not.toContain('Reality 入口')

    await filters[3]!.trigger('click')
    expect(wrapper.find('.endpoint-table').text()).toContain('历史入口')
    expect(wrapper.find('.endpoint-table').text()).toContain('SOCKS5')
    expect(wrapper.find('.endpoint-table').text()).not.toContain('TCP 透传（旧记录）')
    expect(wrapper.find('.endpoint-table').text()).not.toContain('NY 入口')
  })
})
