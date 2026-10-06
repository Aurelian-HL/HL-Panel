import { mount } from '@vue/test-utils'
import { describe, expect, it } from 'vitest'

import type { Customer, UserGroup } from '@/api/business'
import CustomerInventory from './CustomerInventory.vue'

const customer: Customer = {
  id: 'customer-1', username: 'user01', display_name: '测试用户', user_group_id: 'group-1',
  disabled: false, expires_at: null, traffic_limit_bytes: 0, traffic_used_bytes: 0,
  max_rules: 5, speed_limit_mbps: 0, ip_limit: 0, connection_limit: 0, revision: 1,
  effective_status: 'active', created_at: '', updated_at: '',
}
const group: UserGroup = {
  id: 'group-1', name: '基础组', description: '', allowed_entry_group_ids: [],
  allowed_exit_group_ids: [], allow_direct: true, revision: 1,
}

describe('CustomerInventory', () => {
  it('offers user editing without a rule creation proxy action', async () => {
    const wrapper = mount(CustomerInventory, { props: { customers: [customer], groups: [group] } })
    expect(wrapper.text()).toContain('user01')
    expect(wrapper.text()).not.toContain('代建')
    expect(wrapper.text()).not.toContain('添加规则')
    await wrapper.get('.business-desktop button').trigger('click')
    expect(wrapper.emitted('edit')).toEqual([[customer]])
    wrapper.unmount()
  })
})
