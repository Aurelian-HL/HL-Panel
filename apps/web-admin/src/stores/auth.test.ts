import { afterEach, describe, expect, it, vi } from 'vitest'

vi.mock('@/api', () => ({ api: {}, onUnauthorized: vi.fn() }))

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
