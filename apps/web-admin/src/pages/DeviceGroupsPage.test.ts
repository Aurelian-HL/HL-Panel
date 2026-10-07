import { flushPromises, mount } from '@vue/test-utils'
import { createMemoryHistory, createRouter } from 'vue-router'
import { describe, expect, it, vi } from 'vitest'

const mocked = vi.hoisted(() => ({
  getDeviceGroups: vi.fn(),
  getNodes: vi.fn(),
  groupNetworks: vi.fn(),
  userGroups: vi.fn(),
}))

vi.mock('@/api', () => ({ api: mocked }))
vi.mock('@/api/business', () => ({ businessApi: mocked }))

import DeviceGroupsPage from './DeviceGroupsPage.vue'

const groups = [
  { id: 'entry-1', name: '已授权入口', kind: 'ENTRY', user_group_id: 'users-1', selection_policy: 'weighted_round_robin', description: '', member_count: 1, current_generation: 1, metadata_revision: 1, created_at: '', updated_at: '' },
  { id: 'entry-2', name: '待分组入口', kind: 'ENTRY', user_group_id: '', selection_policy: 'weighted_round_robin', description: '', member_count: 0, current_generation: 0, metadata_revision: 1, created_at: '', updated_at: '' },
]

describe('DeviceGroupsPage', () => {
  it('filters ungrouped device groups and displays the live count', async () => {
    mocked.getDeviceGroups.mockResolvedValue({ items: groups })
    mocked.getNodes.mockResolvedValue({ items: [] })
    mocked.groupNetworks.mockResolvedValue({ items: [] })
    mocked.userGroups.mockResolvedValue({ items: [] })
    const router = createRouter({ history: createMemoryHistory(), routes: [{ path: '/device-groups', component: DeviceGroupsPage }, { path: '/user-groups', component: { template: '<div />' } }] })
    await router.push('/device-groups')
    await router.isReady()
    const wrapper = mount(DeviceGroupsPage, {
      global: {
        plugins: [router],
        stubs: {
          DeviceGroupInventory: { props: ['groups'], template: '<div data-test="inventory">{{ groups.length }}</div>' },
          CreateDeviceGroupDialog: true,
          GroupMembersDialog: true,
          AddGroupMemberDialog: true,
          GroupNetworkDialog: true,
          EditDeviceGroupDialog: true,
          GroupIntegrationDialog: true,
          DeleteDeviceGroupDialog: true,
        },
      },
    })
    await flushPromises()

    expect(wrapper.text()).toContain('全部')
    expect(wrapper.text()).not.toContain('全部 2')
    expect(wrapper.text()).toContain('未分组 (1)')
    expect(wrapper.get('button[title="清空流量暂未开放"]').attributes('disabled')).toBeDefined()
    expect(wrapper.get('a[href="/user-groups"]').text()).toContain('管理分组')
    expect(wrapper.get('[data-test="inventory"]').text()).toBe('2')

    await wrapper.get('[role="tablist"] [role="tab"]:nth-child(2)').trigger('click')
    expect(wrapper.get('[data-test="inventory"]').text()).toBe('1')
    wrapper.unmount()
  })
})
