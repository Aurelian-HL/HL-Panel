import { flushPromises, mount } from '@vue/test-utils'
import { beforeEach, describe, expect, it, vi } from 'vitest'

import type { DeviceGroup, EdgeNode } from '@/api'

const mocked = vi.hoisted(() => ({ getDeviceGroupMembers: vi.fn(), retireDeviceGroupMember: vi.fn(), deleteRetiredDeviceGroupMember: vi.fn(), updateDeviceGroupMemberWeight: vi.fn() }))
vi.mock('@/api', () => ({ api: mocked }))

import GroupMembersDialog from './GroupMembersDialog.vue'

const group: DeviceGroup = { id: 'group-1', name: '广州入口', kind: 'ENTRY', selection_policy: 'weighted_round_robin', description: '', member_count: 1, current_generation: 0, created_at: '', updated_at: '' }
const nodes: EdgeNode[] = [{ id: 'node-1', name: '入口一号', hostname: 'edge-1', platform: 'linux', architecture: 'amd64', agent_version: '', boot_id: '', engine_versions: {}, resources: {}, status: 'online', capabilities: [], desired_generation: 0, applied_generation: 0, last_apply_status: '', last_apply_message: '', last_heartbeat_at: null, created_at: '' }]
const member = { group_id: 'group-1', node_id: 'node-1', dial_host: 'edge.example.test', weight: 100, priority: 0, retired_at: null, created_at: '', updated_at: '' }

beforeEach(() => {
  mocked.getDeviceGroupMembers.mockReset().mockResolvedValue({ items: [member] })
  mocked.retireDeviceGroupMember.mockReset().mockResolvedValue({ member: { ...member, retired_at: '2026-10-03T00:00:00Z' }, assignments: [], replayed: false })
  mocked.updateDeviceGroupMemberWeight.mockReset().mockResolvedValue({ member: { ...member, weight: 0 }, assignments: [], replayed: false })
  mocked.deleteRetiredDeviceGroupMember.mockReset().mockResolvedValue({ group_id: group.id, node_id: member.node_id, deleted: true, replayed: false })
})

describe('GroupMembersDialog', () => {
  it('deletes only a confirmed retired membership and removes it from the list', async () => {
    const retired = { ...member, retired_at: '2026-10-12T00:00:00Z', updated_at: '2026-10-12T00:00:00Z' }
    mocked.getDeviceGroupMembers.mockResolvedValue({ items: [retired, { ...member, node_id: 'active-node' }] })
    const wrapper = mount(GroupMembersDialog, { props: { group, nodes }, global: { stubs: { Teleport: true } } })
    await flushPromises()
    expect(wrapper.findAll('button[aria-label^="删除退役记录"]')).toHaveLength(1)
    await wrapper.get('button[aria-label="删除退役记录 入口一号"]').trigger('click')
    expect(mocked.deleteRetiredDeviceGroupMember).not.toHaveBeenCalled()
    expect(wrapper.text()).toContain('保留这台机器在其他组的成员关系')
    await wrapper.findAll('button').find((button) => button.text().includes('确认删除'))!.trigger('click')
    await flushPromises()
    expect(mocked.deleteRetiredDeviceGroupMember).toHaveBeenCalledWith(group.id, 'node-1', retired.updated_at, expect.any(String))
    expect(wrapper.text()).not.toContain('已退役')
    expect(wrapper.text()).toContain('active-node')
    expect(wrapper.emitted('changed')?.[0]).toEqual([0])
    wrapper.unmount()
  })

  it('keeps a failed deletion visible and uses the same key for retry', async () => {
    mocked.getDeviceGroupMembers.mockResolvedValue({ items: [{ ...member, retired_at: '2026-10-12T00:00:00Z' }] })
    mocked.deleteRetiredDeviceGroupMember.mockRejectedValueOnce(new Error('成员已重新入组，请刷新'))
    const wrapper = mount(GroupMembersDialog, { props: { group, nodes }, global: { stubs: { Teleport: true } } })
    await flushPromises()
    await wrapper.get('button[aria-label="删除退役记录 入口一号"]').trigger('click')
    const confirm = () => wrapper.findAll('button').find((button) => button.text().includes('确认删除'))!
    await confirm().trigger('click')
    await flushPromises()
    expect(wrapper.get('[role="alert"]').text()).toContain('成员已重新入组')
    expect(wrapper.text()).toContain('已退役')
    const key = mocked.deleteRetiredDeviceGroupMember.mock.calls[0]![3]
    await confirm().trigger('click')
    await flushPromises()
    expect(mocked.deleteRetiredDeviceGroupMember.mock.calls[1]![3]).toBe(key)
    wrapper.unmount()
  })

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
