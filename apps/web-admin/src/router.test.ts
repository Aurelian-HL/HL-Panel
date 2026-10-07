import { describe, expect, it } from 'vitest'

import { router } from './router'
import { authStore } from './stores/auth'
import { parseLoginResponse } from './api/validators'

describe('admin routes', () => {
  it('preserves the password change flag from login and prevents navigation bypass', async () => {
    const response = parseLoginResponse({ access_token: 'test', expires_at: '2030-01-01T00:00:00Z', user: { id: 'adm-1', username: 'admin', must_change_password: true } })
    expect(response.user.must_change_password).toBe(true)
    sessionStorage.setItem('ny_admin_access_token', 'test')
    // The singleton store cannot be changed from readonly references; login
    // goes through the real parser with a local mocked HTTP response.
    const { vi } = await import('vitest')
    vi.stubGlobal('fetch', vi.fn().mockResolvedValue(new Response(JSON.stringify({ ...response, user: { ...response.user, must_change_password: true } }), { headers: { 'content-type': 'application/json' } })))
    await authStore.login({ username: 'admin', password: '123456' })
    await router.push('/nodes')
    expect(router.currentRoute.value.name).toBe('userinfo')
    await router.push('/login')
    expect(router.currentRoute.value.name).toBe('userinfo')
    authStore.logout()
    vi.unstubAllGlobals()
  })
  it('opens the probe in its own workspace outside the panel shell', () => {
    expect(router.resolve('/probe').matched.map((record) => record.path)).toEqual(['/probe', '/probe'])
    expect(router.resolve('/overview').matched[0]?.path).toBe('/')
  })
  it('redirects the obsolete endpoint page to the rule workflow', () => {
    expect(router.resolve('/endpoint-pools').matched.at(-1)?.redirect).toBe('/forward-rules')
  })
  it('registers the subscription management page inside the authenticated shell', () => {
    expect(router.resolve('/subscriptions').matched.map((record) => record.path)).toEqual(['/', '/subscriptions'])
    expect(router.resolve('/subscriptions').name).toBe('subscriptions')
  })
  it('does not expose removed traffic statistics or LookingGlass pages', () => {
    expect(router.resolve('/traffic').matched.at(-1)?.redirect).toBe('/overview')
    expect(router.resolve('/lookingglass').matched.at(-1)?.redirect).toBe('/overview')
    expect(router.getRoutes().some((route) => route.name === 'traffic' || route.name === 'lookingglass')).toBe(false)
  })
})
