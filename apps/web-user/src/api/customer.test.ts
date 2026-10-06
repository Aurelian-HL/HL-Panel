import { beforeEach, describe, expect, it, vi } from 'vitest'

import { customerApi, TOKEN_KEY } from './index'

function jsonResponse(body: unknown): Response {
  return new Response(JSON.stringify(body), {
    status: 200,
    headers: { 'content-type': 'application/json' },
  })
}

describe('customerApi', () => {
  beforeEach(() => {
    sessionStorage.setItem(TOKEN_KEY, 'customer-access-token')
  })

  it('sends the customer bearer token for self-service reads', async () => {
    const fetchMock = vi.fn().mockResolvedValue(jsonResponse({ user: { id: 'cus-1' } }))
    vi.stubGlobal('fetch', fetchMock)

    await customerApi.me()

    expect(fetchMock).toHaveBeenCalledOnce()
    const [url, init] = fetchMock.mock.calls[0] as [string, RequestInit]
    expect(url).toBe('/api/v1/customer/me')
    expect(new Headers(init.headers).get('Authorization')).toBe('Bearer customer-access-token')
  })

  it('adds a non-empty idempotency key when changing a password', async () => {
    const fetchMock = vi.fn().mockResolvedValue(jsonResponse({ replayed: false }))
    vi.stubGlobal('fetch', fetchMock)

    await customerApi.changePassword('old-password', 'new-password')

    const [url, init] = fetchMock.mock.calls[0] as [string, RequestInit]
    const headers = new Headers(init.headers)
    expect(url).toBe('/api/v1/customer/password')
    expect(init.method).toBe('PUT')
    expect(headers.get('Authorization')).toBe('Bearer customer-access-token')
    expect(headers.get('Idempotency-Key')).toBeTruthy()
    expect(JSON.parse(init.body as string)).toEqual({ current_password: 'old-password', new_password: 'new-password' })
  })
})
