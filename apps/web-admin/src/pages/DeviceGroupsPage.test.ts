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
import { ApiError } from '@/api/http'

const groups = [
  { id: 'entry-1', name: '已授权入口', kind: 'ENTRY', user_group_id: 'users-1', selection_policy: 'weighted_round_robin', description: '', member_count: 1, current_generation: 1, metadata_revision: 1, created_at: '', updated_at: '' },
  { id: 'entry-2', name: '待分组入口', kind: 'ENTRY', user_group_id: '', selection_policy: 'weighted_round_robin', description: '', member_count: 0, current_generation: 0, metadata_revision: 1, created_at: '', updated_at: '' },
]

describe('DeviceGroupsPage', () => {
  it('keeps the group list visible when an auxiliary API fails and blocks edits until retry succeeds', async () => {
    mocked.getDeviceGroups.mockResolvedValue({ items: groups })
    mocked.getNodes.mockResolvedValue({ items: [] })
    mocked.groupNetworks.mockRejectedValue(new ApiError('面板服务暂时异常', 500, 'internal_error'))
    mocked.userGroups.mockResolvedValue({ items: [] })
    const router = createRouter({ history: createMemoryHistory(), routes: [{ path: '/device-groups', component: DeviceGroupsPage }, { path: '/user-groups', component: { template: '<div />' } }] })
    await router.push('/device-groups')
    await router.isReady()
    const wrapper = mount(DeviceGroupsPage, { global: { plugins: [router] } })
    await flushPromises()
    expect(wrapper.text()).toContain('已授权入口')
    expect(wrapper.text()).toContain('网络配置：面板服务暂时异常')
    expect(wrapper.text()).not.toContain('设备组加载失败')
    expect(wrapper.get('fieldset').attributes('disabled')).toBeDefined()
    mocked.groupNetworks.mockResolvedValue({ items: [] })
    const refresh = wrapper.findAll('button').find((button) => button.text() === '刷新')!
    await refresh.trigger('click')
    await flushPromises()
    expect(wrapper.get('fieldset').attributes('disabled')).toBeUndefined()
    expect(wrapper.text()).not.toContain('部分数据读取失败')
    wrapper.unmount()
  })

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
