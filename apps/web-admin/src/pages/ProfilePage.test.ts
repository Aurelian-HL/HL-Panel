import { flushPromises, mount } from '@vue/test-utils'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { ref } from 'vue'

const mocked = vi.hoisted(() => ({
  current: vi.fn(), changePassword: vi.fn(), replace: vi.fn(), logout: vi.fn(), success: vi.fn(),
}))
vi.mock('@/api/profile', () => ({ profileApi: { current: mocked.current, changePassword: mocked.changePassword } }))
vi.mock('@/stores/auth', () => ({ authStore: {
  user: ref({ id: 'adm-1', username: 'admin', created_at: '2026-10-03T00:00:00Z' }),
  expiresAt: ref('2026-10-03T08:00:00Z'), logout: mocked.logout,
} }))
vi.mock('@/composables/toast', () => ({ toast: { success: mocked.success } }))
vi.mock('vue-router', () => ({ useRouter: () => ({ replace: mocked.replace }) }))

import ProfilePage from './ProfilePage.vue'

beforeEach(() => {
  mocked.current.mockReset().mockResolvedValue({ id: 'adm-1', username: 'admin', created_at: '2026-10-03T00:00:00Z' })
  mocked.changePassword.mockReset().mockResolvedValue(undefined)
  mocked.replace.mockReset().mockResolvedValue(undefined)
  mocked.logout.mockReset()
  mocked.success.mockReset()
})

describe('ProfilePage', () => {
  it('shows the authenticated server identity', async () => {
    const wrapper = mount(ProfilePage)
    await flushPromises()
    expect(mocked.current).toHaveBeenCalledOnce()
    expect(wrapper.text()).toContain('admin')
    expect(wrapper.text()).toContain('管理员')
    wrapper.unmount()
  })

  it('requires confirmation and only logs out after the server confirms a password change', async () => {
    const wrapper = mount(ProfilePage)
    await flushPromises()
    const inputs = wrapper.findAll('.profile-password-form input')
    await inputs[0]!.setValue('old-password')
    await inputs[1]!.setValue('new-password')
    await inputs[2]!.setValue('mismatch')
    await wrapper.get('.profile-password-form').trigger('submit')
    expect(mocked.changePassword).not.toHaveBeenCalled()
    expect(mocked.logout).not.toHaveBeenCalled()
    await inputs[2]!.setValue('new-password')
    await wrapper.get('.profile-password-form').trigger('submit')
    await flushPromises()
    expect(mocked.changePassword).toHaveBeenCalledWith('old-password', 'new-password')
    expect(mocked.logout).toHaveBeenCalledOnce()
    expect(mocked.replace).toHaveBeenCalledWith('/login')
    wrapper.unmount()
  })

  it('keeps the session when the server rejects the current password', async () => {
    mocked.changePassword.mockRejectedValueOnce(new Error('当前密码错误'))
    const wrapper = mount(ProfilePage)
    await flushPromises()
    const inputs = wrapper.findAll('.profile-password-form input')
    await inputs[0]!.setValue('wrong-password')
    await inputs[1]!.setValue('new-password')
    await inputs[2]!.setValue('new-password')
    await wrapper.get('.profile-password-form').trigger('submit')
    await flushPromises()
    expect(wrapper.get('[role="alert"]').text()).toContain('当前密码错误')
    expect(mocked.logout).not.toHaveBeenCalled()
    wrapper.unmount()
  })
})
