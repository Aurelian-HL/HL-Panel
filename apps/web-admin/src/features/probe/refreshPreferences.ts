export interface ProbeRefreshIntervals {
  foreground: number
  background: number
}

const storageKey = 'hl_probe_refresh_intervals_v1'
export const defaultProbeRefreshIntervals: ProbeRefreshIntervals = { foreground: 2_000, background: 10_000 }

export function validRefreshInterval(value: number): boolean {
  return Number.isSafeInteger(value) && value >= 1_000 && value <= 300_000
}

export function readProbeRefreshIntervals(): ProbeRefreshIntervals {
  try {
    const raw = localStorage.getItem(storageKey)
    if (!raw) return { ...defaultProbeRefreshIntervals }
    const parsed: unknown = JSON.parse(raw)
    if (!parsed || typeof parsed !== 'object') return { ...defaultProbeRefreshIntervals }
    const { foreground, background } = parsed as Record<string, unknown>
    if (typeof foreground !== 'number' || typeof background !== 'number' || !validRefreshInterval(foreground) || !validRefreshInterval(background)) return { ...defaultProbeRefreshIntervals }
    return { foreground, background }
  } catch {
    return { ...defaultProbeRefreshIntervals }
  }
}

export function saveProbeRefreshIntervals(intervals: ProbeRefreshIntervals): void {
  localStorage.setItem(storageKey, JSON.stringify(intervals))
}
