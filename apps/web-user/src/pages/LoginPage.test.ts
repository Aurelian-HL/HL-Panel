import { flushPromises, mount } from '@vue/test-utils'
import { describe, expect, it, vi } from 'vitest'

const mocked = vi.hoisted(() => ({ login: vi.fn(), replace: vi.fn() }))
vi.mock('@/stores/auth', () => ({ customerAuth: { login: mocked.login } }))
vi.mock('@/stores/site', () => ({ customerSite: { info: { value: { site_name: '测试站点' } }, load: vi.fn().mockResolvedValue(undefined) } }))
vi.mock('vue-router', () => ({
  useRoute: () => ({ query: { redirect: '/forward-rules' } }),
  useRouter: () => ({ replace: mocked.replace }),
}))

import LoginPage from './LoginPage.vue'

describe('LoginPage', () => {
  it('logs in with the customer account and honors an internal redirect', async () => {
    mocked.login.mockResolvedValue(undefined)
    mocked.replace.mockResolvedValue(undefined)
    const wrapper = mount(LoginPage)
    expect(wrapper.text()).toContain('测试站点')
    const inputs = wrapper.findAll('input')
    await inputs[0]?.setValue('  member  ')
    await inputs[1]?.setValue('customer-password')

    await wrapper.get('form').trigger('submit')
    await flushPromises()

    expect(mocked.login).toHaveBeenCalledWith('member', 'customer-password')
    expect(mocked.replace).toHaveBeenCalledWith('/forward-rules')
    wrapper.unmount()
  })
})
