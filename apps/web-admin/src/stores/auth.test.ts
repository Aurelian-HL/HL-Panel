import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

const admin = { access_token: 'test-admin-session', expires_at: '2030-01-01T00:00:00Z', user: { id: 'adm-test', username: 'same-name' } }
const account = { access_token: 'test-account-session', expires_at: '2030-01-01T00:00:00Z', user: {
  id: 'cus-test', username: 'same-name', display_name: '测试子账号', user_type: 'normal', user_group_name: '授权组',
  expires_at: null, traffic_used_bytes: 10, traffic_limit_bytes: 100, max_rules: 2, speed_limit_mbps: 5,
  ip_limit: 1, connection_limit: 10, effective_status: 'active',
} }

function json(value: unknown, status = 200): Response {
  return new Response(JSON.stringify(value), { status, headers: { 'content-type': 'application/json' } })
}

beforeEach(() => vi.resetModules())
afterEach(() => vi.unstubAllGlobals())

describe('persisted administrator session', () => {
  afterEach(() => vi.restoreAllMocks())

  it('lets the server validate a session even when browser time is ahead', async () => {
    vi.resetModules()
    sessionStorage.setItem('ny_admin_access_token', 'existing-token')
    sessionStorage.setItem('ny_admin_user', JSON.stringify({ id: 'admin', username: 'admin' }))
    sessionStorage.setItem('ny_admin_expires_at', '2026-10-08T23:00:00Z')
    vi.spyOn(Date, 'now').mockReturnValue(Date.parse('2030-01-01T00:00:00Z'))
    const { authStore } = await import('./auth')
    expect(authStore.isAuthenticated.value).toBe(true)
    expect(sessionStorage.getItem('ny_admin_access_token')).toBe('existing-token')
  })
})

describe('shared login keeps identity sessions separate', () => {
  it('logs in the administrator with one request and clears an old account session', async () => {
    sessionStorage.setItem('ny_customer_access_token', 'old-account')
    sessionStorage.setItem('ny_customer_user', JSON.stringify(account.user))
    sessionStorage.setItem('ny_customer_expires_at', account.expires_at)
    const fetchMock = vi.fn().mockResolvedValue(json(admin))
    vi.stubGlobal('fetch', fetchMock)
    const { authStore } = await import('./auth')
    expect(await authStore.login({ username: 'same-name', password: 'admin-test-password' })).toBe('administrator')
    expect(fetchMock).toHaveBeenCalledOnce()
    expect(fetchMock.mock.calls[0]?.[0]).toBe('/api/v1/auth/login')
    expect(new Headers(fetchMock.mock.calls[0]?.[1].headers).has('Authorization')).toBe(false)
    expect(authStore.isAuthenticated.value).toBe(true)
    expect(sessionStorage.getItem('ny_admin_access_token')).toBe(admin.access_token)
    expect(sessionStorage.getItem('ny_customer_access_token')).toBeNull()
    expect(sessionStorage.getItem('ny_customer_user')).toBeNull()
    expect(sessionStorage.getItem('ny_customer_expires_at')).toBeNull()
  })

  it('tries the account API only after credential rejection and never creates an admin session', async () => {
    sessionStorage.setItem('ny_admin_access_token', 'old-admin')
    sessionStorage.setItem('ny_admin_user', JSON.stringify(admin.user))
    sessionStorage.setItem('ny_admin_expires_at', admin.expires_at)
    const fetchMock = vi.fn().mockResolvedValueOnce(json({ error: { code: 'unauthorized', message: 'authentication failed' } }, 401)).mockResolvedValueOnce(json(account))
    vi.stubGlobal('fetch', fetchMock)
    const { authStore } = await import('./auth')
    const credentials = { username: 'same-name', password: 'account-test-password' }
    expect(await authStore.login(credentials)).toBe('panel_account')
    expect(fetchMock.mock.calls.map(([url]) => url)).toEqual(['/api/v1/auth/login', '/api/v1/customer/auth/login'])
    for (const [, init] of fetchMock.mock.calls) {
      expect(JSON.parse(init.body)).toEqual(credentials)
      expect(new Headers(init.headers).has('Authorization')).toBe(false)
    }
    expect(authStore.isAuthenticated.value).toBe(false)
    expect(sessionStorage.getItem('ny_admin_access_token')).toBeNull()
    expect(sessionStorage.getItem('ny_admin_user')).toBeNull()
    expect(sessionStorage.getItem('ny_admin_expires_at')).toBeNull()
    expect(sessionStorage.getItem('ny_customer_access_token')).toBe(account.access_token)
    expect(JSON.parse(sessionStorage.getItem('ny_customer_user')!)).toEqual(account.user)
    expect(sessionStorage.getItem('ny_customer_expires_at')).toBe(account.expires_at)
  })

  it('rejects bad credentials in both namespaces without clearing an existing session', async () => {
    sessionStorage.setItem('ny_admin_access_token', 'existing-admin')
    sessionStorage.setItem('ny_admin_user', JSON.stringify(admin.user))
    const fetchMock = vi.fn().mockImplementation(() => Promise.resolve(json({ error: { code: 'unauthorized', message: 'authentication failed' } }, 401)))
    vi.stubGlobal('fetch', fetchMock)
    const { authStore } = await import('./auth')
    await expect(authStore.login({ username: 'same-name', password: 'wrong' })).rejects.toMatchObject({ status: 401 })
    expect(fetchMock).toHaveBeenCalledTimes(2)
    expect(sessionStorage.getItem('ny_admin_access_token')).toBe('existing-admin')
    expect(sessionStorage.getItem('ny_customer_access_token')).toBeNull()
  })

  it.each([403, 429, 500, 503])('does not retry or change identity on HTTP %s', async (status) => {
    const fetchMock = vi.fn().mockResolvedValue(json({ error: { code: 'test-failure', message: 'test failure' } }, status))
    vi.stubGlobal('fetch', fetchMock)
    const { authStore } = await import('./auth')
    await expect(authStore.login({ username: 'same-name', password: 'test-only' })).rejects.toMatchObject({ status })
    expect(fetchMock).toHaveBeenCalledOnce()
    expect(sessionStorage.getItem('ny_admin_access_token')).toBeNull()
    expect(sessionStorage.getItem('ny_customer_access_token')).toBeNull()
  })

  it('does not try another identity after a network failure', async () => {
    const fetchMock = vi.fn().mockRejectedValue(new TypeError('offline'))
    vi.stubGlobal('fetch', fetchMock)
    const { authStore } = await import('./auth')
    await expect(authStore.login({ username: 'same-name', password: 'test-only' })).rejects.toMatchObject({ status: 0 })
    expect(fetchMock).toHaveBeenCalledOnce()
  })

  it('does not store a malformed account response or turn it into an administrator', async () => {
    const fetchMock = vi.fn().mockResolvedValueOnce(json({ error: { code: 'unauthorized' } }, 401)).mockResolvedValueOnce(json(admin))
    vi.stubGlobal('fetch', fetchMock)
    const { authStore } = await import('./auth')
    await expect(authStore.login({ username: 'same-name', password: 'test-only' })).rejects.toThrow('API 响应不符合契约')
    expect(sessionStorage.getItem('ny_admin_access_token')).toBeNull()
    expect(sessionStorage.getItem('ny_customer_access_token')).toBeNull()
  })
})
