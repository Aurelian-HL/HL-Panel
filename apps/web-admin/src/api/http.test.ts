import { describe, expect, it, vi } from 'vitest'

import { HttpClient, onUnauthorized } from './http'

describe('HttpClient', () => {
  it('preserves the real Go API conflict reason instead of a generic status error', async () => {
    vi.stubGlobal('fetch', vi.fn().mockResolvedValue(new Response(JSON.stringify({
      error: { code: 'conflict', message: 'stable endpoint is already allocated' },
    }), { status: 409, headers: { 'content-type': 'application/json' } })))
    const client = new HttpClient('/api/v1', () => 'access-token')
    await expect(client.request('/endpoint-pools', { method: 'POST', body: '{}' })).rejects.toMatchObject({
      status: 409, code: 'conflict', message: 'stable endpoint is already allocated',
    })
  })

  it('reports an unavailable API proxy clearly without hiding a structured server error', async () => {
    vi.stubGlobal('fetch', vi.fn().mockResolvedValue(new Response('Bad Gateway', {
      status: 502, headers: { 'content-type': 'text/plain' },
    })))
    const client = new HttpClient('/api/v1', () => null)
    await expect(client.request('/public/site-info')).rejects.toMatchObject({
      status: 502, code: 'service_unavailable', message: '无法连接控制服务，请检查 API 服务和代理配置',
    })

    vi.stubGlobal('fetch', vi.fn().mockResolvedValue(new Response(JSON.stringify({
      error: { code: 'maintenance', message: '服务维护中' },
    }), { status: 503, headers: { 'content-type': 'application/json' } })))
    await expect(client.request('/public/site-info')).rejects.toMatchObject({
      status: 503, code: 'maintenance', message: '服务维护中',
    })
  })

  it('sends bearer authentication and JSON exactly once', async () => {
    const fetchMock = vi.fn().mockResolvedValue(new Response(JSON.stringify({ ok: true }), {
      status: 200,
      headers: { 'content-type': 'application/json' },
    }))
    vi.stubGlobal('fetch', fetchMock)
    const client = new HttpClient('/api/v1', () => 'access-token')

    await client.request('/device-groups', { method: 'POST', body: JSON.stringify({ name: 'edge' }) })

    expect(fetchMock).toHaveBeenCalledOnce()
    const [url, init] = fetchMock.mock.calls[0] as [string, RequestInit]
    expect(url).toBe('/api/v1/device-groups')
    expect(init.method).toBe('POST')
    expect(new Headers(init.headers).get('Authorization')).toBe('Bearer access-token')
    expect(new Headers(init.headers).get('Content-Type')).toBe('application/json')
  })

  it('clears authentication through the unauthorized hook', async () => {
    const handler = vi.fn()
    onUnauthorized(handler)
    vi.stubGlobal('fetch', vi.fn().mockResolvedValue(new Response(JSON.stringify({ code: 'expired', message: '登录已过期' }), {
      status: 401,
      headers: { 'content-type': 'application/json' },
    })))
    const client = new HttpClient('/api/v1', () => 'expired-token')

    await expect(client.request('/nodes')).rejects.toMatchObject({ status: 401, code: 'expired' })
    expect(handler).toHaveBeenCalledOnce()
  })
})
