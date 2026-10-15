import { flushPromises, mount } from '@vue/test-utils'
import { describe, expect, it, vi } from 'vitest'

const apiMocks = vi.hoisted(() => ({ createDeviceGroup: vi.fn() }))

vi.mock('@/api', () => ({ api: apiMocks }))

import CreateDeviceGroupDialog from './CreateDeviceGroupDialog.vue'

describe('CreateDeviceGroupDialog', () => {
  it('collects NY-style group fields and keeps load policy secondary', async () => {
    apiMocks.createDeviceGroup.mockResolvedValue({
      id: 'group-1', name: '广州入口', kind: 'ENTRY', selection_policy: 'weighted_least_connections', description: '',
      member_count: 0, current_generation: 0, created_at: '', updated_at: '',
    })
    const wrapper = mount(CreateDeviceGroupDialog, { props: { userGroups: [{ id: 'user-group-1', name: '普通用户', description: '', allowed_entry_group_ids: [], allowed_exit_group_ids: [], allow_direct: true, revision: 1 }] }, global: { stubs: { Teleport: true } } })
    const kindOptions = wrapper.findAll('select').at(1)?.findAll('option').map((option) => option.attributes('value'))
    expect(kindOptions).toEqual(['', 'ENTRY', 'EXIT'])

    await wrapper.get('input[maxlength="120"]').setValue('广州入口')
    await wrapper.findAll('select').at(0)?.setValue('user-group-1')
    await wrapper.findAll('select').at(1)?.setValue('ENTRY')
    await wrapper.get('input[type="checkbox"]').setValue(true)
    await wrapper.get('form').trigger('submit')
    await flushPromises()

    expect(apiMocks.createDeviceGroup).toHaveBeenCalledWith({
      name: '广州入口', kind: 'ENTRY', user_group_id: 'user-group-1', hide_in_probe: true, selection_policy: 'weighted_least_connections', description: '',
    }, expect.any(String))
    expect(wrapper.emitted('created')).toHaveLength(1)
  })
})
