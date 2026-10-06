import { describe, expect, it, vi } from 'vitest'

import { usageApi } from './usage'

describe('usageApi', () => {
  it('serializes scope, time and pagination without leaking an empty site scope id', async () => {
    sessionStorage.setItem('ny_admin_access_token', 'admin-token')
    const fetchMock = vi.fn().mockResolvedValue(new Response(JSON.stringify({
      items: [], totals: { rule_actual_bytes: 0, customer_actual_bytes: 0, charged_bytes: 0 }, total: 0, page: 2, page_size: 25,
    }), { status: 200, headers: { 'content-type': 'application/json' } }))
    vi.stubGlobal('fetch', fetchMock)

    await usageApi.query({ scope: 'site', scope_id: 'ignored', from: '2026-10-01T00:00:00Z', page: 2, page_size: 25 })

    const [url, init] = fetchMock.mock.calls[0] as [string, RequestInit]
    expect(url).toContain('/api/v1/usage?')
    const parsed = new URL(url, 'https://console.example.test')
    expect(parsed.searchParams.get('scope')).toBe('site')
    expect(parsed.searchParams.has('scope_id')).toBe(false)
    expect(parsed.searchParams.get('from')).toBe('2026-10-01T00:00:00Z')
    expect(parsed.searchParams.get('page')).toBe('2')
    expect(new Headers(init.headers).get('Authorization')).toBe('Bearer admin-token')
  })

  it('submits an authenticated idempotent revoke request', async () => {
    sessionStorage.setItem('ny_admin_access_token', 'admin-token')
    const decision = {
      id: 'decision / one', customer_id: 'customer-one', rule_id: 'rule-one', protocol: 'tcp', reason: 'quota_exhausted',
      action: 'enable_customer_access', status: 'revoke_pending', trigger_node_id: 'node-one', trigger_boot_id: 'boot-one',
      trigger_sequence: 7, customer_charged_bytes: 2048, traffic_limit_bytes: 2000,
      created_at: '2026-10-03T01:00:01Z', updated_at: '2026-10-03T01:01:00Z', revision: 3, last_error: '',
    }
    const fetchMock = vi.fn().mockResolvedValue(new Response(JSON.stringify({ decision, replayed: false }), {
      status: 200, headers: { 'content-type': 'application/json' },
    }))
    vi.stubGlobal('fetch', fetchMock)

    await usageApi.revoke('decision / one', 'revoke-key-one')

    const [url, init] = fetchMock.mock.calls[0] as [string, RequestInit]
    expect(url).toContain('/usage/enforcement/decision%20%2F%20one/revoke')
    expect(init.method).toBe('POST')
    expect(new Headers(init.headers).get('Idempotency-Key')).toBe('revoke-key-one')
    expect(new Headers(init.headers).get('Authorization')).toBe('Bearer admin-token')
  })
})
