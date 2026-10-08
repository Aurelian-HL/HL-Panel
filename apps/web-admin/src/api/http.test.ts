import { afterEach, describe, expect, it, vi } from 'vitest'

import { HttpClient, onUnauthorized } from './http'

afterEach(() => { vi.useRealTimers(); vi.unstubAllGlobals() })

describe('HttpClient', () => {
  it('bounds a stalled GET and preserves the login without retrying', async () => {
    vi.useFakeTimers()
    const handler = vi.fn()
    onUnauthorized(handler)
    const fetchMock = vi.fn((_url, init: RequestInit) => new Promise<Response>((_resolve, reject) => {
      init.signal!.addEventListener('abort', () => reject(new DOMException('aborted', 'AbortError')))
    }))
    vi.stubGlobal('fetch', fetchMock)
    const result = expect(new HttpClient('/api/v1', () => 'token').request('/nodes')).rejects.toMatchObject({ code: 'request_timeout' })
    await vi.advanceTimersByTimeAsync(30_000)
    await result
    expect(fetchMock).toHaveBeenCalledOnce()
    expect(handler).not.toHaveBeenCalled()
    expect(vi.getTimerCount()).toBe(0)
  })

  it('keeps the deadline until the response body is received', async () => {
    vi.useFakeTimers()
    vi.stubGlobal('fetch', vi.fn(async (_url, init: RequestInit) => ({
      status: 200, ok: true, headers: new Headers({ 'content-type': 'application/json' }),
      json: () => new Promise((_resolve, reject) => init.signal!.addEventListener('abort', () => reject(new DOMException('aborted', 'AbortError')))),
    })))
    const result = expect(new HttpClient('/api/v1', () => null).request('/nodes', { timeoutMs: 100 })).rejects.toMatchObject({ code: 'request_timeout' })
    await vi.advanceTimersByTimeAsync(100)
    await result
  })

  it('honors caller cancellation and removes the listener', async () => {
    const controller = new AbortController()
    const remove = vi.spyOn(controller.signal, 'removeEventListener')
    vi.stubGlobal('fetch', vi.fn((_url, init: RequestInit) => new Promise<Response>((_resolve, reject) => {
      init.signal!.addEventListener('abort', () => reject(new DOMException('aborted', 'AbortError')))
    })))
    const result = expect(new HttpClient('/api/v1', () => null).request('/nodes', { signal: controller.signal })).rejects.toMatchObject({ code: 'request_cancelled' })
    controller.abort()
    await result
    expect(remove).toHaveBeenCalledWith('abort', expect.any(Function))
  })

  it('reports truncated JSON and preserves a broken proxy status', async () => {
    const client = new HttpClient('/api/v1', () => null)
    vi.stubGlobal('fetch', vi.fn().mockResolvedValue(new Response('{', { headers: { 'content-type': 'application/json' } })))
    await expect(client.request('/nodes')).rejects.toMatchObject({ code: 'invalid_response', status: 200 })
    vi.stubGlobal('fetch', vi.fn().mockResolvedValue(new Response('{', { status: 502, headers: { 'content-type': 'application/json' } })))
    await expect(client.request('/nodes')).rejects.toMatchObject({ code: 'service_unavailable', status: 502 })
  })

  it('does not log out a newer login when an old request returns unauthorized', async () => {
    let token = 'old-token'
    const handler = vi.fn()
    onUnauthorized(handler)
    let finish!: (response: Response) => void
    vi.stubGlobal('fetch', vi.fn(() => new Promise<Response>((resolve) => { finish = resolve })))
    const request = new HttpClient('/api/v1', () => token).request('/nodes')
    token = 'new-token'
    finish(new Response('{}', { status: 401 }))
    await expect(request).rejects.toMatchObject({ status: 401 })
    expect(handler).not.toHaveBeenCalled()
  })

  it('keeps sessions on server failure and gives an actionable service error', async () => {
    const handler = vi.fn()
    onUnauthorized(handler)
    vi.stubGlobal('fetch', vi.fn().mockResolvedValue(new Response(JSON.stringify({ error: { code: 'internal_error', message: 'internal server error' } }), {
      status: 500, headers: { 'content-type': 'application/json' },
    })))
    await expect(new HttpClient('/api/v1', () => 'token').request('/device-groups')).rejects.toMatchObject({
      status: 500, code: 'internal_error', message: expect.stringContaining('面板服务暂时异常'),
    })
    expect(handler).not.toHaveBeenCalled()
  })

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

    await expect(client.request('/nodes')).rejects.toMatchObject({ status: 401, code: 'session_expired', message: '登录已过期，请重新登录后继续' })
    expect(handler).toHaveBeenCalledOnce()
  })
})
