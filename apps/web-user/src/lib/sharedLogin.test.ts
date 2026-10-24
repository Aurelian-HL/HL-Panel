import { describe, expect, it } from 'vitest'

import { sharedLoginURL } from './sharedLogin'

describe('shared login redirect', () => {
  it('preserves account pages and the expired-session message', () => {
    const result = new URL(sharedLoginURL({ redirect: '/userinfo?tab=password#details', reason: 'expired' }), window.location.origin)
    expect(result.pathname).toBe('/login')
    expect(result.searchParams.get('redirect')).toBe('/account/userinfo?tab=password#details')
    expect(result.searchParams.get('reason')).toBe('expired')
  })

  it.each(['https://example.test', '//example.test', '/\\example.test', '/login', '/account/login', '/../login', '/%2e%2e/login', '/overview', '/\n/example.test'])('rejects unsafe or unrelated destination %s', (redirect) => {
    const result = new URL(sharedLoginURL({ redirect }), window.location.origin)
    expect(result.searchParams.get('redirect')).toBe('/account/')
  })
})
