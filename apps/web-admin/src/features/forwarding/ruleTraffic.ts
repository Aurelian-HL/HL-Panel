import { ApiError } from '@/api/http'
import { usageApi } from '@/api/usage'

interface TrafficCallbacks {
  isCurrent: () => boolean
  onValue: (ruleId: string, bytes: number) => void
  onUnauthorized: () => void
}

/** Read ledger totals without blocking the rules or flooding the API. */
export async function loadRuleTraffic(ruleIds: string[], callbacks: TrafficCallbacks): Promise<void> {
  const pending = [...new Set(ruleIds)]
  let sessionExpired = false
  const current = () => !sessionExpired && callbacks.isCurrent()
  async function worker(): Promise<void> {
    while (pending.length && current()) {
      const id = pending.shift()!
      try {
        const result = await usageApi.query({ scope: 'rule', scope_id: id, page: 1, page_size: 1 })
        const bytes = result.totals.charged_bytes
        if (current() && Number.isFinite(bytes) && bytes >= 0) callbacks.onValue(id, bytes)
      } catch (cause) {
        if (current() && cause instanceof ApiError && cause.status === 401) {
          sessionExpired = true
          callbacks.onUnauthorized()
        }
        // An unavailable total stays absent, so the inventory displays —.
      }
    }
  }
  await Promise.all(Array.from({ length: Math.min(4, pending.length) }, worker))
}
