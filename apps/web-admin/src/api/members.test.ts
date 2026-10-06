import { describe, expect, it, vi } from 'vitest'

import { api } from './index'

describe('device-group member API', () => {
  it('loads the member envelope and retires a member with an operation key', async () => {
    const member = { group_id: 'group/1', node_id: 'node/1', dial_host: 'edge.example.test', weight: 100, priority: 0, created_at: '', updated_at: '', retired_at: '2026-10-03T00:00:00Z' }
    const fetchMock = vi.fn()
      .mockResolvedValueOnce(new Response(JSON.stringify({ items: [member] }), { headers: { 'content-type': 'application/json' } }))
      .mockResolvedValueOnce(new Response(JSON.stringify({ member, assignments: [], replayed: false }), { headers: { 'content-type': 'application/json' } }))
    vi.stubGlobal('fetch', fetchMock)
    sessionStorage.setItem('ny_admin_access_token', 'local-test-token')

    expect((await api.getDeviceGroupMembers('group/1')).items).toHaveLength(1)
    expect((await api.retireDeviceGroupMember('group/1', 'node/1', 'retire-operation')).member.retired_at).toBe(member.retired_at)

    expect(fetchMock.mock.calls[0]?.[0]).toBe('/api/v1/device-groups/group%2F1/members')
    const [retirePath, retireOptions] = fetchMock.mock.calls[1] as [string, RequestInit]
    expect(retirePath).toBe('/api/v1/device-groups/group%2F1/members/node%2F1/retire')
    expect(retireOptions.method).toBe('POST')
    expect(new Headers(retireOptions.headers).get('Idempotency-Key')).toBe('retire-operation')
    expect(new Headers(retireOptions.headers).get('Authorization')).toBe('Bearer local-test-token')
  })

  it('updates zero weight with a concurrency timestamp and operation key', async () => {
    const member = { group_id: 'group/1', node_id: 'node/1', dial_host: 'edge.example.test', weight: 0, priority: 0, created_at: '', updated_at: '2026-10-04T00:00:01Z', retired_at: null }
    const fetchMock = vi.fn().mockResolvedValue(new Response(JSON.stringify({ member, assignments: [], replayed: false }), { headers: { 'content-type': 'application/json' } }))
    vi.stubGlobal('fetch', fetchMock)
    const result = await api.updateDeviceGroupMemberWeight('group/1', 'node/1', 0, '2026-10-04T00:00:00Z', 'weight-operation')
    expect(result.member.weight).toBe(0)
    const [path, options] = fetchMock.mock.calls[0] as [string, RequestInit]
    expect(path).toBe('/api/v1/device-groups/group%2F1/members/node%2F1/weight')
    expect(options.method).toBe('PUT')
    expect(new Headers(options.headers).get('Idempotency-Key')).toBe('weight-operation')
    expect(JSON.parse(options.body as string)).toEqual({ weight: 0, updated_at: '2026-10-04T00:00:00Z' })
  })
})
