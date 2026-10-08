let anchor: { server: number; monotonic: number } | undefined

export function observeServerTime(value: string | null): void {
  const server = value ? Date.parse(value) : NaN
  if (Number.isFinite(server)) anchor = { server, monotonic: performance.now() }
}

// Elapsed time stays valid even if the browser's wall clock is changed.
export function serverNow(): number {
  return anchor ? anchor.server + Math.max(0, performance.now() - anchor.monotonic) : Date.now()
}
