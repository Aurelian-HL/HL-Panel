import { mount } from '@vue/test-utils'
import { describe, expect, it, vi } from 'vitest'

import type { DeviceGroup } from '@/api'

vi.mock('@/api', () => ({ api: { updateDeviceGroup: vi.fn() } }))

import EditDeviceGroupDialog from './EditDeviceGroupDialog.vue'

const group: DeviceGroup = {
  id: 'entry-1', name: '广州入口', kind: 'ENTRY', user_group_id: 'users-1',
  hide_in_probe: false, selection_policy: 'weighted_round_robin', description: '',
  member_count: 1, current_generation: 0, metadata_revision: 1, created_at: '', updated_at: '',
}

describe('EditDeviceGroupDialog', () => {
  it('opens the separately persisted network editor without losing pending metadata changes', async () => {
    const wrapper = mount(EditDeviceGroupDialog, { props: { group, network: null, userGroups: [{ id: 'users-1', name: '普通用户', description: '', allowed_entry_group_ids: [], allowed_exit_group_ids: [], allow_direct: false, revision: 1 }] }, global: { stubs: { Teleport: true } } })
    const networkButton = wrapper.findAll('button').find((button) => button.text().includes('编辑网络配置'))!
    await networkButton.trigger('click')
    expect(wrapper.emitted('configureNetwork')).toHaveLength(1)
    await wrapper.get('input[maxlength="128"]').setValue('广州入口新版')
    expect(wrapper.findAll('button').find((button) => button.text().includes('编辑网络配置'))!.attributes('disabled')).toBeDefined()
    expect(wrapper.text()).toContain('请先保存或取消当前修改')
    wrapper.unmount()
  })
})
