import { describe, expect, it } from 'vitest'

import { accountLoginDestination, administratorLoginDestination } from './loginDestination'

describe('login destinations stay inside the authenticated surface', () => {
  it('preserves the requested page only for the matching identity', () => {
    expect(administratorLoginDestination('/forward-rules?group=one#rules')).toBe('/forward-rules?group=one#rules')
    expect(accountLoginDestination('/account/userinfo?tab=password#details')).toBe('/account/userinfo?tab=password#details')
    expect(administratorLoginDestination('/account/userinfo')).toBe('/overview')
    expect(accountLoginDestination('/forward-rules')).toBeNull()
  })

  it.each(['//example.test', '/\\example.test', 'https://example.test', '/\n/example.test', '/login', '/account/login', '/account/../login', '/account/%2e%2e/login'])('rejects external URLs and login loops: %s', (redirect) => {
    expect(administratorLoginDestination(redirect)).toBe('/overview')
    expect(accountLoginDestination(redirect)).toBeNull()
  })
})
