import { flushPromises, mount } from '@vue/test-utils'
import { beforeEach, describe, expect, it, vi } from 'vitest'

import type { DeviceGroup } from '@/api'
import type { GroupNetwork, UserGroup } from '@/api/business'

const mocked = vi.hoisted(() => ({ saveGroupNetwork: vi.fn() }))
vi.mock('@/api/business', () => ({ businessApi: mocked }))

import GroupNetworkDialog from './GroupNetworkDialog.vue'

const entry: DeviceGroup = { id: 'entry-1', name: '广州入口', kind: 'ENTRY', description: '', member_count: 2, current_generation: 0, selection_policy: 'weighted_round_robin', created_at: '', updated_at: '' }
const exit: DeviceGroup = { id: 'exit-1', name: '香港出口', kind: 'EXIT', description: '', member_count: 2, current_generation: 0, selection_policy: 'weighted_round_robin', created_at: '', updated_at: '' }
const userGroup: UserGroup = { id: 'users-1', name: '普通用户', description: '', allowed_entry_group_ids: [entry.id], allowed_exit_group_ids: [exit.id], allow_direct: true, revision: 1 }
const legacyNetwork: GroupNetwork = { group_id: entry.id, connect_host: 'entry.example.test', port_start: 10000, port_end: 20000, allow_direct: true, allowed_exit_group_ids: [exit.id], traffic_multiplier: 1, revision: 2 }

beforeEach(() => {
  mocked.saveGroupNetwork.mockReset()
  mocked.saveGroupNetwork.mockImplementation(async (input: object, groupId: string) => ({ group_id: groupId, ...input }))
})

describe('GroupNetworkDialog', () => {
  it('maps a legacy direct flag to OPTIONAL and clears exits when direct becomes forced', async () => {
    const wrapper = mount(GroupNetworkDialog, { props: { group: entry, groups: [entry, exit], userGroups: [userGroup], network: legacyNetwork }, global: { stubs: { Teleport: true } } })
    const policyField = wrapper.findAll('label.field').find((item) => item.text().includes('入口直出策略'))
    if (!policyField) throw new Error('direct policy field missing')
    expect((policyField.get('select').element as HTMLSelectElement).value).toBe('OPTIONAL')
    expect(wrapper.text()).toContain('可用落地出口组')

    await policyField.get('select').setValue('FORCED')
    expect(wrapper.text()).toContain('强制直出时不配置落地出口组')
    expect(wrapper.text()).not.toContain('可用落地出口组')
    await wrapper.get('#group-network-editor').trigger('submit')
    await flushPromises()

    const [input, groupId] = mocked.saveGroupNetwork.mock.calls[0] as [Record<string, unknown>, string]
    expect(groupId).toBe(entry.id)
    expect(input).toMatchObject({ direct_policy: 'FORCED', allow_direct: true, allowed_exit_group_ids: [], fallback_exit_group_id: '', port_ranges: [{ start: 10000, end: 20000 }] })
    wrapper.unmount()
  })

  it('hides entry-only fields and normalizes an exit group to its allowed zero values', async () => {
    const staleExitNetwork: GroupNetwork = { ...legacyNetwork, group_id: exit.id, direct_policy: 'OPTIONAL' }
    const wrapper = mount(GroupNetworkDialog, { props: { group: exit, groups: [entry, exit], userGroups: [userGroup], network: staleExitNetwork }, global: { stubs: { Teleport: true } } })
    expect(wrapper.text()).not.toContain('客户连接地址')
    expect(wrapper.text()).not.toContain('入口直出策略')
    await wrapper.get('#group-network-editor').trigger('submit')
    await flushPromises()

    const [input, groupId] = mocked.saveGroupNetwork.mock.calls[0] as [Record<string, unknown>, string]
    expect(groupId).toBe(exit.id)
    expect(input).toMatchObject({ connect_host: '', port_start: 0, port_end: 0, port_ranges: [], direct_policy: 'DISABLED', allow_direct: false, allowed_exit_group_ids: [] })
    wrapper.unmount()
  })

  it('submits canonical disjoint ranges, user authorization, reverse entry authorization and fallback', async () => {
    const secondExit: DeviceGroup = { ...exit, id: 'exit-2', name: '日本出口' }
    const wrapper = mount(GroupNetworkDialog, { props: { group: entry, groups: [entry, exit, secondExit], userGroups: [userGroup], network: legacyNetwork }, global: { stubs: { Teleport: true } } })
    await wrapper.find('textarea').setValue('10000-10002, 12000, 12001-12002')
    const userButton = wrapper.findAll('button').find((item) => item.text().includes(userGroup.name))
    if (!userButton) throw new Error('user group option missing')
    await userButton.trigger('click')
    const fallbackField = wrapper.findAll('label.field').find((item) => item.text().includes('备用出口组'))
    if (!fallbackField) throw new Error('fallback field missing')
    await fallbackField.get('select').setValue(exit.id)
    await wrapper.get('#group-network-editor').trigger('submit')
    await flushPromises()

    expect(mocked.saveGroupNetwork.mock.calls[0]?.[0]).toMatchObject({
      port_start: 10000,
      port_end: 12002,
      port_ranges: [{ start: 10000, end: 10002 }, { start: 12000, end: 12002 }],
      allowed_user_group_ids: [userGroup.id],
      fallback_exit_group_id: exit.id,
    })
    wrapper.unmount()
  })
})
