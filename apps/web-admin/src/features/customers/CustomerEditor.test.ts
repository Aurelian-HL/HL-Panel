import { flushPromises, mount } from '@vue/test-utils'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import type { Customer, UserGroup } from '@/api/business'
const mocked = vi.hoisted(() => ({ saveCustomer: vi.fn() }))
vi.mock('@/api/business', () => ({ businessApi: mocked }))
import CustomerEditor from './CustomerEditor.vue'
const groups: UserGroup[] = [{ id: 'ug-1', name: 'users', description: '', allowed_entry_group_ids: [], allowed_exit_group_ids: [], allow_direct: false, revision: 1 }]
const customer: Customer = { id: 'c-1', username: 'customer01', display_name: '客户', user_group_id: 'ug-1', disabled: false, expires_at: '2027-01-01T00:00:31Z', traffic_limit_bytes: 1024 ** 3, traffic_used_bytes: 0, max_rules: 2, speed_limit_mbps: 10, ip_limit: 2, connection_limit: 10, revision: 3, effective_status: 'active', created_at: '', updated_at: '' }
beforeEach(() => mocked.saveCustomer.mockReset())
describe('CustomerEditor', () => {
  it('creates an account without pre-creating a user group', async () => {
    mocked.saveCustomer.mockResolvedValue({ ...customer, user_group_id: '' })
    const wrapper = mount(CustomerEditor, { props: { customer: null, groups: [] }, global: { stubs: { Teleport: true } } })
    await wrapper.get('input[maxlength="64"]').setValue('customer02')
    await wrapper.get('input[type=password]').setValue('initial-password')
    await wrapper.get('form').trigger('submit'); await flushPromises()
    expect(mocked.saveCustomer).toHaveBeenCalledOnce()
    expect(mocked.saveCustomer.mock.calls[0]?.[0]).toMatchObject({ username: 'customer02', user_group_id: '' })
    expect(wrapper.emitted('saved')).toHaveLength(1)
    wrapper.unmount()
  })

  it('does not resend or prefill a password when editing, and preserves exact expiry', async () => {
    mocked.saveCustomer.mockResolvedValue(customer)
    const wrapper = mount(CustomerEditor, { props: { customer, groups }, global: { stubs: { Teleport: true } } })
    expect((wrapper.get('input[type=password]').element as HTMLInputElement).value).toBe('')
    await wrapper.get('form').trigger('submit'); await flushPromises()
    const [input, id] = mocked.saveCustomer.mock.calls[0] as [Record<string, unknown>, string]
    expect(input).not.toHaveProperty('password')
    expect(input.expires_at).toBe(customer.expires_at)
    expect(input.revision).toBe(3)
    expect(id).toBe(customer.id)
    wrapper.unmount()
  })

  it('retries a failed save with the same idempotency key', async () => {
    mocked.saveCustomer.mockRejectedValueOnce(new Error('网络连接失败')).mockResolvedValueOnce(customer)
    const wrapper = mount(CustomerEditor, { props: { customer, groups }, global: { stubs: { Teleport: true } } })
    await wrapper.get('form').trigger('submit'); await flushPromises()
    expect(wrapper.text()).toContain('网络连接失败')
    await wrapper.get('form').trigger('submit'); await flushPromises()
    expect(mocked.saveCustomer.mock.calls[1]?.[2]).toBe(mocked.saveCustomer.mock.calls[0]?.[2])
    expect(wrapper.emitted('saved')).toHaveLength(1)
    wrapper.unmount()
  })
})
