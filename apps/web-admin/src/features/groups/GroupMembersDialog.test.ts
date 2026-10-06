import { flushPromises, mount } from '@vue/test-utils'
import { beforeEach, describe, expect, it, vi } from 'vitest'

import type { DeviceGroup, EdgeNode } from '@/api'

const mocked = vi.hoisted(() => ({ getDeviceGroupMembers: vi.fn(), retireDeviceGroupMember: vi.fn(), updateDeviceGroupMemberWeight: vi.fn() }))
vi.mock('@/api', () => ({ api: mocked }))

import GroupMembersDialog from './GroupMembersDialog.vue'

const group: DeviceGroup = { id: 'group-1', name: '广州入口', kind: 'ENTRY', selection_policy: 'weighted_round_robin', description: '', member_count: 1, current_generation: 0, created_at: '', updated_at: '' }
const nodes: EdgeNode[] = [{ id: 'node-1', name: '入口一号', hostname: 'edge-1', platform: 'linux', architecture: 'amd64', agent_version: '', boot_id: '', engine_versions: {}, resources: {}, status: 'online', capabilities: [], desired_generation: 0, applied_generation: 0, last_apply_status: '', last_apply_message: '', last_heartbeat_at: null, created_at: '' }]
const member = { group_id: 'group-1', node_id: 'node-1', dial_host: 'edge.example.test', weight: 100, priority: 0, retired_at: null, created_at: '', updated_at: '' }

beforeEach(() => {
  mocked.getDeviceGroupMembers.mockReset().mockResolvedValue({ items: [member] })
  mocked.retireDeviceGroupMember.mockReset().mockResolvedValue({ member: { ...member, retired_at: '2026-10-03T00:00:00Z' }, assignments: [], replayed: false })
  mocked.updateDeviceGroupMemberWeight.mockReset().mockResolvedValue({ member: { ...member, weight: 0 }, assignments: [], replayed: false })
})

describe('GroupMembersDialog', () => {
  it('loads members and requires explicit confirmation before retirement', async () => {
    const wrapper = mount(GroupMembersDialog, { props: { group, nodes }, global: { stubs: { Teleport: true } } })
    await flushPromises()
    expect(wrapper.text()).toContain('入口一号')
    expect(wrapper.text()).toContain('edge.example.test')
    await wrapper.get('button[aria-label="退役 入口一号"]').trigger('click')
    expect(mocked.retireDeviceGroupMember).not.toHaveBeenCalled()
    await wrapper.findAll('button').find((button) => button.text().includes('确认退役'))?.trigger('click')
    await flushPromises()
    expect(mocked.retireDeviceGroupMember).toHaveBeenCalledWith('group-1', 'node-1', expect.any(String))
    expect(wrapper.text()).toContain('已退役')
    expect(wrapper.emitted('changed')?.[0]).toEqual([0])
    wrapper.unmount()
  })

  it('keeps confirmation open and shows an error when retirement fails', async () => {
    mocked.retireDeviceGroupMember.mockRejectedValue(new Error('操作失败'))
    const wrapper = mount(GroupMembersDialog, { props: { group, nodes }, global: { stubs: { Teleport: true } } })
    await flushPromises()
    await wrapper.get('button[aria-label="退役 入口一号"]').trigger('click')
    await wrapper.findAll('button').find((button) => button.text().includes('确认退役'))?.trigger('click')
    await flushPromises()
    expect(wrapper.get('[role="alert"]').text()).toContain('操作失败')
    expect(wrapper.text()).toContain('确认退役')
    wrapper.unmount()
  })

  it('changes member weight to zero without retiring the member', async () => {
    const wrapper = mount(GroupMembersDialog, { props: { group, nodes }, global: { stubs: { Teleport: true } } })
    await flushPromises()
    await wrapper.get('button[aria-label="更改 入口一号 权重"]').trigger('click')
    const input = wrapper.get('input[type="number"]')
    await input.setValue('0')
    await wrapper.get('form').trigger('submit')
    await flushPromises()
    expect(mocked.updateDeviceGroupMemberWeight).toHaveBeenCalledWith('group-1', 'node-1', 0, member.updated_at, expect.any(String))
    expect(wrapper.text()).toContain('权重 0')
    expect(mocked.retireDeviceGroupMember).not.toHaveBeenCalled()
    wrapper.unmount()
  })
})
