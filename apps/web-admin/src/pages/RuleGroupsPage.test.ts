import { flushPromises, mount } from '@vue/test-utils'
import { describe, expect, it, vi } from 'vitest'
import { createMemoryHistory, createRouter } from 'vue-router'

const mocked = vi.hoisted(() => ({ ruleGroups: vi.fn(), rules: vi.fn(), saveRuleGroup: vi.fn() }))
vi.mock('@/api/business', () => ({ businessApi: mocked }))

import RuleGroupsPage from './RuleGroupsPage.vue'

describe('RuleGroupsPage', () => {
  it('shows persisted groups with rule counts and opens the CAS editor', async () => {
    mocked.ruleGroups.mockResolvedValue({ items: [{ id: 'rgrp-1', name: '核心线路', description: '主用', revision: 3, created_at: '', updated_at: '' }] })
    mocked.rules.mockResolvedValue({ items: [{ id: 'rule-1', name: '规则一', rule_group_id: 'rgrp-1' }, { id: 'rule-2', name: '规则二', rule_group_id: '' }] })
    const router = createRouter({ history: createMemoryHistory(), routes: [{ path: '/:pathMatch(.*)*', component: { template: '<div />' } }] })
    const wrapper = mount(RuleGroupsPage, { global: { plugins: [router], stubs: { Teleport: true } } })
    await flushPromises()
    expect(wrapper.text()).toContain('核心线路')
    expect(wrapper.text()).toContain('主用')
    expect(wrapper.get('.rule-group-table').text()).toContain('1')
    await wrapper.get('.rule-group-table button').trigger('click')
    expect(wrapper.find('#rule-group-editor').exists()).toBe(true)
    expect((wrapper.get('#rule-group-editor input').element as HTMLInputElement).value).toBe('核心线路')
    wrapper.unmount()
  })
})
