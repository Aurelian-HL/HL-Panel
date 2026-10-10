import { flushPromises, mount } from '@vue/test-utils'
import { beforeEach, describe, expect, it, vi } from 'vitest'

import type { DeviceGroup, EdgeNode } from '@/api'

const mocked = vi.hoisted(() => ({ addDeviceGroupMember: vi.fn() }))
vi.mock('@/api', () => ({ api: mocked }))

import AddGroupMemberDialog from './AddGroupMemberDialog.vue'

const group: DeviceGroup = {
  id: 'group-1', name: '广州入口', kind: 'ENTRY', selection_policy: 'weighted_round_robin',
  description: '', member_count: 0, current_generation: 0, created_at: '', updated_at: '',
}
const nodes: EdgeNode[] = [
  { id: 'node-1', name: '入口一号', hostname: 'edge-1.example.test', platform: 'linux', architecture: 'amd64', agent_version: '', boot_id: '', engine_versions: {}, resources: {}, status: 'online', capabilities: [], desired_generation: 0, applied_generation: 0, last_apply_status: '', last_apply_message: '', last_heartbeat_at: null, created_at: '' },
  { id: 'node-2', name: '入口二号', hostname: 'edge-2.example.test', platform: 'linux', architecture: 'amd64', agent_version: '', boot_id: '', engine_versions: {}, resources: {}, status: 'online', capabilities: [], desired_generation: 0, applied_generation: 0, last_apply_status: '', last_apply_message: '', last_heartbeat_at: null, created_at: '' },
]

beforeEach(() => {
  mocked.addDeviceGroupMember.mockReset().mockResolvedValue({ assignments: [] })
})

describe('AddGroupMemberDialog', () => {
  it('uses the selected node hostname as the default dial address', async () => {
    const wrapper = mount(AddGroupMemberDialog, { props: { group, nodes }, global: { stubs: { Teleport: true } } })
    const nodeSelect = wrapper.get('select')

    await nodeSelect.setValue('node-1')
    await wrapper.get('form').trigger('submit')
    await flushPromises()
    expect(mocked.addDeviceGroupMember).toHaveBeenCalledWith('group-1', expect.objectContaining({ node_id: 'node-1', dial_host: 'edge-1.example.test' }), expect.any(String))
    wrapper.unmount()
  })

  it('keeps a manually overridden dial address when switching nodes', async () => {
    const wrapper = mount(AddGroupMemberDialog, { props: { group, nodes }, global: { stubs: { Teleport: true } } })
    const nodeSelect = wrapper.get('select')
    const dialHost = wrapper.get('input[maxlength="253"]')

    await nodeSelect.setValue('node-1')
    await dialHost.setValue('gateway.example.test')
    await nodeSelect.setValue('node-2')
    expect(dialHost.element).toHaveProperty('value', 'gateway.example.test')
    wrapper.unmount()
  })
})
