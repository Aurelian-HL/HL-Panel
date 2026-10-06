import { mount } from '@vue/test-utils'
import { describe, expect, it } from 'vitest'

import type { DeviceGroup } from '@/api'
import type { GroupNetwork, UserGroup } from '@/api/business'

import GroupConfigPreviewDialog from './GroupConfigPreviewDialog.vue'

const entry: DeviceGroup = { id: 'entry-1', name: '广州入口', kind: 'ENTRY', user_group_id: 'users-1', selection_policy: 'weighted_round_robin', description: '', member_count: 1, current_generation: 2, created_at: '', updated_at: '' }
const exit: DeviceGroup = { ...entry, id: 'exit-1', name: '香港出口', kind: 'EXIT' }
const user: UserGroup = { id: 'users-1', name: '普通用户', description: '', allowed_entry_group_ids: [entry.id], allowed_exit_group_ids: [exit.id], allow_direct: true, revision: 1 }
const network: GroupNetwork = { group_id: entry.id, connect_host: 'entry.example.test', port_start: 10000, port_end: 20000, direct_policy: 'OPTIONAL', allow_direct: true, allowed_exit_group_ids: [exit.id], fallback_exit_group_id: exit.id, allowed_user_group_ids: [user.id], traffic_multiplier: 1, revision: 1 }

describe('GroupConfigPreviewDialog', () => {
  it('resolves user groups and device groups from their own inventories', () => {
    const wrapper = mount(GroupConfigPreviewDialog, { props: { group: entry, groups: [entry, exit], network, userGroups: [user] }, global: { stubs: { Teleport: true } } })
    expect(wrapper.text()).toContain('普通用户')
    expect(wrapper.text()).toContain('香港出口')
    expect(wrapper.text()).not.toContain('exit-1')
    expect(wrapper.text()).toContain('控制面保存的组策略')
    wrapper.unmount()
  })
})
