import { flushPromises, mount } from '@vue/test-utils'
import { beforeEach, describe, expect, it, vi } from 'vitest'

import type { DeviceGroup } from '@/api'

const mocked = vi.hoisted(() => ({ createEnrollmentToken: vi.fn(), getDeviceGroupMembers: vi.fn(), getPendingGroupEnrollmentTokens: vi.fn(), revokeEnrollmentToken: vi.fn() }))
const mockedProbe = vi.hoisted(() => ({ getInventory: vi.fn() }))
vi.mock('@/api', () => ({ api: mocked }))
vi.mock('@/api/probe', () => ({ probeApi: mockedProbe }))

import GroupIntegrationDialog from './GroupIntegrationDialog.vue'

const group: DeviceGroup = {
  id: 'group-entry-1', name: '广州入口', kind: 'ENTRY', selection_policy: 'weighted_round_robin',
  description: '', member_count: 0, current_generation: 0, created_at: '', updated_at: '',
}

describe('GroupIntegrationDialog', () => {
  beforeEach(() => {
    mocked.createEnrollmentToken.mockReset().mockResolvedValue({
      id: 'token-1', name: '广州入口 节点注册', token: 'enroll_once', expires_at: '2026-10-04T08:00:00Z',
    })
    mocked.getDeviceGroupMembers.mockReset().mockResolvedValue({ items: [{ group_id: group.id, node_id: 'node-1', dial_host: 'edge-1.example.test', weight: 100, priority: 0, retired_at: null, created_at: '', updated_at: '' }] })
    mocked.getPendingGroupEnrollmentTokens.mockReset().mockResolvedValue({ items: [] })
    mocked.revokeEnrollmentToken.mockReset().mockImplementation(async (id: string) => ({ token: { id, revoked_at: '2026-10-04T08:00:00Z' }, replayed: false }))
    mockedProbe.getInventory.mockReset().mockResolvedValue({
      upstream_status: 'ok',
      items: [
        { node_id: 'nezha:1', nezha_server_id: 1, link_status: 'unmanaged', name: '香港一号', ipv4: '198.51.100.1', online: true },
        { node_id: 'nezha:2', nezha_server_id: 2, link_status: 'unmanaged', name: '香港二号', online: true },
        { node_id: 'hl-node-3', nezha_server_id: 3, link_status: 'linked', name: '已绑定', online: true },
      ],
    })
  })

  it('binds a newly issued enrollment token to the current device group', async () => {
    const wrapper = mount(GroupIntegrationDialog, { props: { group }, global: { stubs: { Teleport: true } } })

    await wrapper.get('button.button--primary').trigger('click')
    await flushPromises()

    expect(mocked.createEnrollmentToken).toHaveBeenCalledWith({
      name: '广州入口 节点注册', group_id: 'group-entry-1', expires_in_seconds: 900,
    })
    expect(wrapper.text()).toContain('已绑定 广州入口')
    wrapper.unmount()
  })

  it('shows member dialing records in the debug configuration mode', async () => {
    const wrapper = mount(GroupIntegrationDialog, { props: { group, mode: 'config' }, global: { stubs: { Teleport: true } } })

    await flushPromises()

    expect(mocked.getDeviceGroupMembers).toHaveBeenCalledWith(group.id)
    expect(wrapper.text()).toContain('edge-1.example.test')
    expect(wrapper.text()).toContain('控制面保存的成员')
    expect(wrapper.find('button.button--primary').exists()).toBe(false)
    wrapper.unmount()
  })

  it('binds each group token to a selected unlinked Nezha server and can issue another', async () => {
    const wrapper = mount(GroupIntegrationDialog, { props: { group }, global: { stubs: { Teleport: true } } })
    await flushPromises()

    const serverSelect = wrapper.findAll('select')[1]!
    expect(serverSelect.text()).toContain('香港一号')
    expect(serverSelect.text()).not.toContain('已绑定')
    await serverSelect.setValue('1')
    await wrapper.get('button.button--primary').trigger('click')
    await flushPromises()

    expect(mocked.createEnrollmentToken).toHaveBeenCalledWith({
      name: '广州入口 节点注册 · 香港一号', group_id: group.id, nezha_server_id: 1, expires_in_seconds: 900,
    })
    expect(wrapper.text()).toContain('哪吒 #1')
    expect(wrapper.text()).toContain('待注册')
    await wrapper.get('.integration-panel > .button--secondary').trigger('click')
    await flushPromises()
    expect(wrapper.text()).not.toContain('enroll_once')
    await wrapper.findAll('select')[1]!.setValue('2')
    await wrapper.get('button.button--primary').trigger('click')
    await flushPromises()
    expect(mocked.createEnrollmentToken).toHaveBeenLastCalledWith({
      name: '广州入口 节点注册 · 香港二号', group_id: group.id, nezha_server_id: 2, expires_in_seconds: 900,
    })
    wrapper.unmount()
  })

  it('keeps monitoring and forwarding registration separate if Nezha is unavailable', async () => {
    mockedProbe.getInventory.mockRejectedValue(new Error('monitor offline'))
    const wrapper = mount(GroupIntegrationDialog, { props: { group }, global: { stubs: { Teleport: true } } })
    await flushPromises()
    expect(wrapper.text()).toContain('哪吒机器清单读取失败')
    await wrapper.get('button.button--primary').trigger('click')
    await flushPromises()
    expect(mocked.createEnrollmentToken).toHaveBeenCalledWith({ name: '广州入口 节点注册', group_id: group.id, expires_in_seconds: 900 })
    wrapper.unmount()
  })

  it('lists only non-secret pending tokens and revokes after confirmation', async () => {
    mocked.getPendingGroupEnrollmentTokens.mockResolvedValue({ items: [{ id: 'token-1', name: '待装节点', group_id: group.id, created_at: '2026-10-04T07:00:00Z', expires_at: '2026-10-04T08:00:00Z' }] })
    const wrapper = mount(GroupIntegrationDialog, { props: { group, mode: 'offline' }, global: { stubs: { Teleport: true } } })
    await flushPromises()
    expect(mocked.getPendingGroupEnrollmentTokens).toHaveBeenCalledWith(group.id)
    expect(wrapper.text()).toContain('待装节点')
    expect(wrapper.text()).not.toContain('enroll_once')
    await wrapper.get('[aria-label="撤销 待装节点"]').trigger('click')
    expect(mocked.revokeEnrollmentToken).not.toHaveBeenCalled()
    await wrapper.get('.pending-token-list__actions .button--danger').trigger('click')
    await flushPromises()
    expect(mocked.revokeEnrollmentToken).toHaveBeenCalledWith('token-1', expect.any(String))
    expect(wrapper.text()).toContain('当前没有未使用的组注册令牌')
    wrapper.unmount()
  })
})
