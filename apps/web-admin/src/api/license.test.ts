import { describe, expect, it, vi } from 'vitest'

import { licenseApi } from './license'

describe('licenseApi', () => {
  it('sends an authenticated idempotent revoke request', async () => {
    sessionStorage.setItem('ny_admin_access_token', 'admin-token')
    const fetchMock = vi.fn().mockResolvedValue(new Response(JSON.stringify({
      result: { id: 'lic-1', status: 'revoked' }, replayed: false,
    }), { status: 200, headers: { 'content-type': 'application/json' } }))
    vi.stubGlobal('fetch', fetchMock)

    await licenseApi.revoke('license / one', 'license-revoke-one')

    const [url, init] = fetchMock.mock.calls[0] as [string, RequestInit]
    expect(url).toContain('/api/v1/license/codes/license%20%2F%20one/revoke')
    expect(init.method).toBe('POST')
    expect(new Headers(init.headers).get('Idempotency-Key')).toBe('license-revoke-one')
    expect(new Headers(init.headers).get('Authorization')).toBe('Bearer admin-token')
  })
})
