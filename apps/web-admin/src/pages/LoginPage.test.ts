import { flushPromises, mount } from '@vue/test-utils'
import { afterEach, describe, expect, it, vi } from 'vitest'

const mocked = vi.hoisted(() => ({ login: vi.fn(), replace: vi.fn(), locationReplace: vi.fn(), query: {} as Record<string, unknown> }))
vi.mock('@/stores/auth', () => ({ authStore: { login: mocked.login } }))
vi.mock('@/stores/site', () => ({ siteStore: { siteName: { value: '测试面板' }, panelTitle: { value: '测试面板' }, load: vi.fn().mockResolvedValue(undefined) } }))
vi.mock('vue-router', () => ({ useRoute: () => ({ query: mocked.query }), useRouter: () => ({ replace: mocked.replace }) }))

import { ApiError } from '@/api'
import LoginPage from './LoginPage.vue'

afterEach(() => { mocked.query = {}; vi.unstubAllGlobals() })

async function fillAndSubmit() {
  const wrapper = mount(LoginPage)
  await wrapper.get('input[autocomplete="username"]').setValue('  test-account  ')
  await wrapper.get('input[autocomplete="current-password"]').setValue('test-only-password')
  await wrapper.get('form').trigger('submit')
  await flushPromises()
  return wrapper
}

describe('shared login form', () => {
  it('uses the existing form for administrators without an extra account entry', async () => {
    mocked.login.mockResolvedValue('administrator')
    mocked.query = { redirect: '/nodes' }
    const wrapper = await fillAndSubmit()
    expect(wrapper.findAll('form')).toHaveLength(1)
    expect(wrapper.text()).not.toContain('子账号登录')
    expect(wrapper.find('a[href="/account/login"]').exists()).toBe(false)
    expect(mocked.login).toHaveBeenCalledWith({ username: 'test-account', password: 'test-only-password' })
    expect(mocked.replace).toHaveBeenCalledWith('/nodes')
    wrapper.unmount()
  })

  it('enters the account application after account authentication', async () => {
    vi.stubGlobal('location', { origin: 'http://localhost:3000', replace: mocked.locationReplace })
    mocked.login.mockResolvedValue('panel_account')
    mocked.query = { redirect: '/account/userinfo' }
    const wrapper = await fillAndSubmit()
    expect(mocked.locationReplace).toHaveBeenCalledWith('/account/userinfo')
    expect(mocked.replace).not.toHaveBeenCalled()
    wrapper.unmount()
  })

  it('shows a credential error and keeps the user on the form', async () => {
    mocked.login.mockRejectedValue(new ApiError('authentication failed', 401))
    const wrapper = await fillAndSubmit()
    expect(wrapper.get('[role="alert"]').text()).toBe('用户名或密码错误')
    expect(wrapper.get('button[type="submit"]').attributes('disabled')).toBeUndefined()
    expect(mocked.replace).not.toHaveBeenCalled()
    wrapper.unmount()
  })

  it('allows only one pending login when the form is submitted repeatedly', async () => {
    let finish!: (identity: string) => void
    mocked.login.mockImplementation(() => new Promise<string>((resolve) => { finish = resolve }))
    const wrapper = await fillAndSubmit()
    await wrapper.get('form').trigger('submit')
    expect(mocked.login).toHaveBeenCalledOnce()
    finish('administrator')
    await flushPromises()
    wrapper.unmount()
  })
})
