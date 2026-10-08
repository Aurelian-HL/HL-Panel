import { afterEach, describe, expect, it, vi } from 'vitest'

describe('server clock', () => {
  afterEach(() => vi.restoreAllMocks())

  it('uses server time and elapsed monotonic time despite browser clock changes', async () => {
    vi.resetModules()
    const { observeServerTime, serverNow } = await import('./serverClock')
    const monotonic = vi.spyOn(performance, 'now').mockReturnValue(100)
    vi.spyOn(Date, 'now').mockReturnValue(Date.parse('2030-01-01T00:00:00Z'))
    observeServerTime('2026-10-08T19:00:00Z')
    expect(serverNow()).toBe(Date.parse('2026-10-08T19:00:00Z'))
    monotonic.mockReturnValue(3100)
    observeServerTime('invalid')
    expect(serverNow()).toBe(Date.parse('2026-10-08T19:00:03Z'))
  })
})
