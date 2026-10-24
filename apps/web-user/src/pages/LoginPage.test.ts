import { mount } from '@vue/test-utils'
import { describe, expect, it, vi } from 'vitest'

const mocked = vi.hoisted(() => ({ replace: vi.fn() }))
vi.mock('vue-router', () => ({ useRoute: () => ({ query: { redirect: '/forward-rules', reason: 'expired' } }) }))

import LoginPage from './LoginPage.vue'

describe('legacy account login', () => {
  it('returns to the shared login with the account destination and no second form', () => {
    vi.stubGlobal('location', { origin: 'http://localhost:3000', replace: mocked.replace })
    const wrapper = mount(LoginPage)
    expect(mocked.replace).toHaveBeenCalledWith('/login?redirect=%2Faccount%2Fforward-rules&reason=expired')
    expect(wrapper.find('form').exists()).toBe(false)
    wrapper.unmount()
  })
})
