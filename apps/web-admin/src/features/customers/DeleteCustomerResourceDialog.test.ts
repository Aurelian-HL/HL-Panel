import { flushPromises, mount } from '@vue/test-utils'
import { beforeEach, describe, expect, it, vi } from 'vitest'
const mocked = vi.hoisted(() => ({ deleteCustomer: vi.fn(), deleteUserGroup: vi.fn() }))
vi.mock('@/api/business', () => ({ businessApi: mocked }))
import DeleteCustomerResourceDialog from './DeleteCustomerResourceDialog.vue'

beforeEach(() => { mocked.deleteCustomer.mockReset(); mocked.deleteUserGroup.mockReset() })
describe('customer deletion dialog', () => {
  it.each(['customer', 'user-group'] as const)('submits the %s revision once while pending', async (kind) => {
    let finish!: () => void
    const api = kind === 'customer' ? mocked.deleteCustomer : mocked.deleteUserGroup
    api.mockReturnValue(new Promise<void>((resolve) => { finish = resolve }))
    const wrapper = mount(DeleteCustomerResourceDialog, { props: { kind, item: { id: 'resource', revision: 3 }, name: '测试资源' }, global: { stubs: { Teleport: true } } })
    const button = wrapper.get('button.button--danger')
    await button.trigger('click'); await button.trigger('click')
    expect(api).toHaveBeenCalledOnce()
    expect(api.mock.calls[0]?.slice(0, 2)).toEqual(['resource', 3])
    expect(wrapper.get('button.button--danger').attributes('disabled')).toBeDefined()
    expect(wrapper.emitted('deleted')).toBeUndefined()
    finish(); await flushPromises()
    expect(wrapper.emitted('deleted')).toHaveLength(1)
    wrapper.unmount()
  })
  it('keeps the dialog open on a reference conflict and reuses the key for retry', async () => {
    mocked.deleteCustomer.mockRejectedValueOnce(new Error('customer has forwarding rules')).mockResolvedValueOnce({ replayed: false })
    const wrapper = mount(DeleteCustomerResourceDialog, { props: { kind: 'customer', item: { id: 'c-1', revision: 4 }, name: '测试客户' }, global: { stubs: { Teleport: true } } })
    await wrapper.get('button.button--danger').trigger('click'); await flushPromises()
    expect(wrapper.get('[role=alert]').text()).toContain('请先删除或转移规则')
    expect(wrapper.emitted('deleted')).toBeUndefined()
    await wrapper.get('button.button--danger').trigger('click'); await flushPromises()
    expect(mocked.deleteCustomer.mock.calls[0]?.[2]).toBe(mocked.deleteCustomer.mock.calls[1]?.[2])
    expect(wrapper.emitted('deleted')).toHaveLength(1)
    wrapper.unmount()
  })
})
