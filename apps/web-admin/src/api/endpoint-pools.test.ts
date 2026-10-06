import { describe, expect, it, vi } from 'vitest'

import { api } from './index'
import { ContractError, parseDeleteEndpointPoolResponse } from './validators'

describe('endpoint pool deletion API', () => {
  it('sends an encoded pool id and operation key, and validates the response', async () => {
    const fetchMock = vi.fn().mockResolvedValue(new Response(JSON.stringify({ replayed: false }), { headers: { 'content-type': 'application/json' } }))
    vi.stubGlobal('fetch', fetchMock)
    sessionStorage.setItem('ny_admin_access_token', 'local-test-token')

    expect(await api.deleteEndpointPool('pool/1', 'delete-operation')).toEqual({ replayed: false })
    const [path, options] = fetchMock.mock.calls[0] as [string, RequestInit]
    expect(path).toBe('/api/v1/endpoint-pools/pool%2F1')
    expect(options.method).toBe('DELETE')
    expect(new Headers(options.headers).get('Idempotency-Key')).toBe('delete-operation')
    expect(new Headers(options.headers).get('Authorization')).toBe('Bearer local-test-token')
  })

  it('rejects ambiguous success responses', () => {
    expect(() => parseDeleteEndpointPoolResponse({})).toThrow(ContractError)
    expect(() => parseDeleteEndpointPoolResponse({ replayed: 'false' })).toThrow(ContractError)
  })
})
