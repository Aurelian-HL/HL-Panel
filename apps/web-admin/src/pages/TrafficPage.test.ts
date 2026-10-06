import { flushPromises, mount } from '@vue/test-utils'
import { beforeEach, describe, expect, it, vi } from 'vitest'

const mocked = vi.hoisted(() => ({ query: vi.fn(), revoke: vi.fn() }))
vi.mock('@/api/usage', () => ({ usageApi: mocked }))

import TrafficPage from './TrafficPage.vue'

beforeEach(() => {
  mocked.query.mockResolvedValue({
    items: [{
      node_id: 'node-one', boot_id: 'boot-one', sequence: 7, customer_id: 'customer-one', rule_id: 'rule-one',
      entry_group_id: 'entry-one', exit_group_id: 'exit-one', protocol: 'tcp', occurred_at: '2026-10-03T01:00:00Z',
      period_started_at: '2026-10-03T00:59:00Z', period_ended_at: '2026-10-03T01:00:00Z',
      rule_actual_bytes: 1024, customer_actual_bytes: 1024, charged_bytes: 2048,
      entry_multiplier_micros: 2_000_000, exit_multiplier_micros: 1_000_000,
      payload_sha256: 'a'.repeat(64), received_at: '2026-10-03T01:00:01Z',
      enforcement_decision: { id: 'decision-one', customer_id: 'customer-one', rule_id: 'rule-one', protocol: 'tcp', reason: 'quota_exhausted', action: 'disable_customer_access', status: 'pending', trigger_node_id: 'node-one', trigger_boot_id: 'boot-one', trigger_sequence: 7, customer_charged_bytes: 2048, traffic_limit_bytes: 2000, created_at: '2026-10-03T01:00:01Z', updated_at: '2026-10-03T01:00:01Z', revision: 1, last_error: '' },
    }],
    totals: { rule_actual_bytes: 1024, customer_actual_bytes: 1024, charged_bytes: 2048 }, total: 1, page: 1, page_size: 25,
  })
})

describe('TrafficPage', () => {
  it('separates actual and charged bytes and labels enforcement as pending', async () => {
    const wrapper = mount(TrafficPage)
    await flushPromises()
    expect(wrapper.text()).toContain('规则实际流量')
    expect(wrapper.text()).toContain('用户实际流量')
    expect(wrapper.text()).toContain('计费流量')
    expect(wrapper.text()).toContain('入口 ×2')
    expect(wrapper.text()).toContain('等待停用 · 流量已达限额')
    expect(wrapper.text()).not.toContain('已执行')
    wrapper.unmount()
  })

  it('requires an id for scoped queries before calling the API', async () => {
    const wrapper = mount(TrafficPage)
    await flushPromises()
    const calls = mocked.query.mock.calls.length
    await wrapper.get('select').setValue('customer')
    const queryButton = wrapper.findAll('button').find((button) => button.text().includes('查询'))
    await queryButton!.trigger('click')
    expect(wrapper.text()).toContain('请输入对应的查询 ID')
    expect(mocked.query).toHaveBeenCalledTimes(calls)
    wrapper.unmount()
  })

  it('requires confirmation and replaces an applied decision with revoke pending', async () => {
    mocked.query.mockResolvedValueOnce({
      items: [{
        node_id: 'node-one', boot_id: 'boot-one', sequence: 7, customer_id: 'customer-one', rule_id: 'rule-one', protocol: 'tcp',
        entry_group_id: 'entry-one', exit_group_id: '', occurred_at: '2026-10-03T01:00:00Z', period_started_at: '2026-10-03T00:59:00Z', period_ended_at: '2026-10-03T01:00:00Z',
        rule_actual_bytes: 1024, customer_actual_bytes: 1024, charged_bytes: 1024, entry_multiplier_micros: 1_000_000, exit_multiplier_micros: 1_000_000,
        payload_sha256: 'a'.repeat(64), received_at: '2026-10-03T01:00:01Z',
        enforcement_decision: { id: 'decision-one', customer_id: 'customer-one', rule_id: 'rule-one', protocol: 'tcp', reason: 'quota_exhausted', action: 'disable_customer_access', status: 'applied', trigger_node_id: 'node-one', trigger_boot_id: 'boot-one', trigger_sequence: 7, customer_charged_bytes: 1024, traffic_limit_bytes: 1000, created_at: '2026-10-03T01:00:01Z', updated_at: '2026-10-03T01:00:02Z', revision: 2, last_error: '' },
      }],
      totals: { rule_actual_bytes: 1024, customer_actual_bytes: 1024, charged_bytes: 1024 }, total: 1, page: 1, page_size: 25,
    })
    mocked.revoke.mockResolvedValueOnce({
      replayed: false,
      decision: { id: 'decision-one', customer_id: 'customer-one', rule_id: 'rule-one', protocol: 'tcp', reason: 'quota_exhausted', action: 'enable_customer_access', status: 'revoke_pending', trigger_node_id: 'node-one', trigger_boot_id: 'boot-one', trigger_sequence: 7, customer_charged_bytes: 1024, traffic_limit_bytes: 1000, created_at: '2026-10-03T01:00:01Z', updated_at: '2026-10-03T01:02:00Z', revision: 3, last_error: '' },
    })
    const wrapper = mount(TrafficPage)
    await flushPromises()
    const revokeButton = wrapper.findAll('button').find((button) => button.text().includes('撤销停用'))
    await revokeButton!.trigger('click')
    expect(document.body.textContent).toContain('撤销访问停用')
    const confirm = [...document.body.querySelectorAll('button')].find((button) => button.textContent?.includes('确认撤销')) as HTMLButtonElement
    confirm.click()
    await flushPromises()
    expect(mocked.revoke).toHaveBeenCalledWith('decision-one', expect.any(String))
    expect(wrapper.text()).toContain('等待恢复 · 流量已达限额')
    wrapper.unmount()
  })
})
