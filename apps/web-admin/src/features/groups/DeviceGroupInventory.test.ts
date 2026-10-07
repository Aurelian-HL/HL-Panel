import { flushPromises, mount } from '@vue/test-utils'
import { describe, expect, it, vi } from 'vitest'

import type { DeviceGroup } from '@/api'

import DeviceGroupInventory from './DeviceGroupInventory.vue'

const group: DeviceGroup = {
  id: 'group-entry-1', name: '广州入口', kind: 'ENTRY', selection_policy: 'weighted_round_robin',
  description: '', member_count: 2, current_generation: 1, created_at: '', updated_at: '',
}

describe('DeviceGroupInventory', () => {
  it('exposes the NY-style integration menu and reports the selected deployment mode', async () => {
    const wrapper = mount(DeviceGroupInventory, { attachTo: document.body, props: { groups: [group], networks: [] } })

    await wrapper.get('.integration-menu__toggle').trigger('click')
    await flushPromises()
    const menu = document.body.querySelector('[role="menu"]')!
    expect(wrapper.find('[role="menu"]').exists()).toBe(false)
    expect(menu.textContent).toContain('自动探测线路（安装器待发布）')
    expect(menu.textContent).toContain('查看节点记录（调试用）')

    ;(menu.querySelectorAll('[role="menuitem"]')[3] as HTMLButtonElement).click()
    await flushPromises()

    expect(wrapper.emitted('integrate')).toEqual([[group, 'config']])
    expect(document.body.querySelector('[role="menu"]')).toBeNull()
    wrapper.unmount()
  })

  it('places the shared menu above a trigger near the viewport bottom', async () => {
    const wrapper = mount(DeviceGroupInventory, { attachTo: document.body, props: { groups: [group], networks: [] } })
    const trigger = wrapper.get('.integration-menu__toggle').element as HTMLElement
    vi.spyOn(trigger, 'getBoundingClientRect').mockReturnValue({ left: 400, right: 480, top: window.innerHeight - 40, bottom: window.innerHeight - 12 } as DOMRect)

    await wrapper.get('.integration-menu__toggle').trigger('click')
    await flushPromises()

    const menu = document.body.querySelector('[role="menu"]') as HTMLElement
    expect(menu.style.top).toBe(`${window.innerHeight - 206}px`)
    expect(menu.style.visibility).not.toBe('hidden')
    expect(document.body.querySelectorAll('[role="menu"]')).toHaveLength(1)
    wrapper.unmount()
  })

  it('keeps edit and network configuration available in the mobile layout', async () => {
    const wrapper = mount(DeviceGroupInventory, { props: { groups: [group], networks: [] } })
    const mobile = wrapper.get('.group-mobile-card')
    await mobile.findAll('button').find((button) => button.text().includes('编辑'))!.trigger('click')
    await mobile.findAll('button').find((button) => button.text().includes('连接'))!.trigger('click')
    expect(wrapper.emitted('edit')).toEqual([[group]])
    expect(wrapper.emitted('configureNetwork')).toEqual([[group]])
    wrapper.unmount()
  })

  it('removes the read-only config action and keeps labeled delete actions in one desktop row', () => {
    const wrapper = mount(DeviceGroupInventory, { props: { groups: [group], networks: [] } })
    const actions = wrapper.get('.group-table .business-row-actions')
    expect(actions.text()).not.toContain('配置')
    expect(actions.get('button[title="编辑设备组"]').text()).toContain('编辑')
    expect(actions.get('button[title="删除设备组"]').text()).toContain('删除')
    expect(actions.classes()).toContain('business-row-actions')
    wrapper.unmount()
  })
})
