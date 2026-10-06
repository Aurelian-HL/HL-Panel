import { beforeEach, describe, expect, it } from 'vitest'

import { defaultProbeRefreshIntervals, readProbeRefreshIntervals, saveProbeRefreshIntervals, validRefreshInterval } from './refreshPreferences'

beforeEach(() => localStorage.removeItem('hl_probe_refresh_intervals_v1'))

describe('probe refresh preferences', () => {
  it('defaults to 2 seconds in foreground and 10 seconds in background', () => {
    expect(readProbeRefreshIntervals()).toEqual(defaultProbeRefreshIntervals)
  })

  it('persists valid intervals and rejects corrupt storage', () => {
    saveProbeRefreshIntervals({ foreground: 3000, background: 12000 })
    expect(readProbeRefreshIntervals()).toEqual({ foreground: 3000, background: 12000 })
    localStorage.setItem('hl_probe_refresh_intervals_v1', '{bad json')
    expect(readProbeRefreshIntervals()).toEqual(defaultProbeRefreshIntervals)
  })

  it('accepts only bounded integer millisecond intervals', () => {
    expect(validRefreshInterval(1000)).toBe(true)
    expect(validRefreshInterval(300000)).toBe(true)
    expect(validRefreshInterval(999)).toBe(false)
    expect(validRefreshInterval(1000.5)).toBe(false)
    expect(validRefreshInterval(Infinity)).toBe(false)
  })
})
