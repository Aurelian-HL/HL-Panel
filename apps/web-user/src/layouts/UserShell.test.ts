import { flushPromises, mount } from '@vue/test-utils'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { ref } from 'vue'

const mocked = vi.hoisted(() => ({ me: vi.fn(), replace: vi.fn(), updateUser: vi.fn() }))
const authenticated = ref(true)
vi.mock('@/api', () => ({ customerApi: { me: mocked.me } }))
vi.mock('@/stores/auth', () => ({ customerAuth: {
  get isAuthenticated() { return authenticated }, user: { value: { username: 'member' } },
  updateUser: mocked.updateUser, logout: vi.fn(),
} }))
vi.mock('@/stores/site', () => ({ customerSite: {
  info: { value: { site_name: '测试站点' } }, load: vi.fn().mockResolvedValue(undefined),
} }))
vi.mock('vue-router', () => ({
  useRoute: () => ({ fullPath: '/userinfo' }), useRouter: () => ({ replace: mocked.replace }),
}))

import UserShell from './UserShell.vue'

beforeEach(() => { authenticated.value = true; vi.clearAllMocks() })

describe('panel login lifecycle', () => {
  it('checks the server session on entering the account workspace', async () => {
    const user = { username: 'member' }
    mocked.me.mockResolvedValue(user)
    const wrapper = mount(UserShell, { global: { stubs: { RouterLink: true, RouterView: true } } })
    await flushPromises()
    expect(mocked.me).toHaveBeenCalledOnce()
    expect(mocked.updateUser).toHaveBeenCalledWith(user)
    expect(mocked.replace).not.toHaveBeenCalled()
    wrapper.unmount()
  })

  it('returns a revoked login to the login screen', async () => {
    mocked.me.mockImplementation(async () => { authenticated.value = false; throw new Error('revoked session') })
    const wrapper = mount(UserShell, { global: { stubs: { RouterLink: true, RouterView: true } } })
    await flushPromises()
    expect(mocked.replace).toHaveBeenCalledWith({ name: 'login', query: { reason: 'expired', redirect: '/userinfo' } })
    expect(mocked.updateUser).not.toHaveBeenCalled()
    wrapper.unmount()
  })
})
