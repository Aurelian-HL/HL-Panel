import { onMounted, onUnmounted, shallowRef, watch, type Ref } from 'vue'
import type { ForwardRule } from '@/api/business'
import { effectiveIngressProtocol } from '@/lib/businessFormatters'
import { ruleConnectionPresentation, type RuleConnectionPresentation } from './ruleConnection'

export const ruleConnectionRefreshMS = 5 * 60 * 1000

// Traffic can refresh every second; connection badges, counts and filters all
// read this single five-minute observation instead of interpreting each poll.
export function useRuleConnectionSnapshot(rules: Ref<ForwardRule[]>) {
  const presentations = shallowRef<Record<string, RuleConnectionPresentation>>({})
  const received = new WeakMap<object, number>()
  let identities = new Map<string, string>()
  let timer: number | undefined
  let active = false
  const identity = (rule: ForwardRule) => JSON.stringify([
    rule.entry_group_id, rule.listen_port, effectiveIngressProtocol(rule),
    rule.paused, ['active', 'pending_activation'].includes(rule.status),
  ])
  const present = (rule: ForwardRule) => ruleConnectionPresentation(rule, rule.connection
    ? Math.max(0, performance.now() - (received.get(rule.connection) ?? -Infinity)) : Infinity)

  function schedule() {
    if (!active || timer !== undefined) return
    timer = window.setTimeout(() => {
      timer = undefined
      presentations.value = Object.fromEntries(rules.value.map(rule => [rule.id, present(rule)]))
      schedule()
    }, ruleConnectionRefreshMS)
  }

  watch(() => rules.value.map(rule => rule), current => {
    const now = performance.now()
    const next: Record<string, RuleConnectionPresentation> = {}
    const nextIdentities = new Map<string, string>()
    for (const rule of current) {
      if (rule.connection && !received.has(rule.connection)) received.set(rule.connection, now)
      const key = identity(rule)
      nextIdentities.set(rule.id, key)
      const previous = presentations.value[rule.id]
      // A new rule initializes only itself. Explicit pause/port/entry changes
      // invalidate only that rule's attribution, never the other observations.
      next[rule.id] = !previous ? present(rule) : identities.get(rule.id) === key ? previous
        : ruleConnectionPresentation({ ...rule, connection: undefined }, Infinity)
    }
    identities = nextIdentities
    presentations.value = next
    if (current.length) schedule()
  }, { immediate: true, flush: 'sync' })

  onMounted(() => { active = true; if (rules.value.length) schedule() })
  onUnmounted(() => { active = false; window.clearTimeout(timer) })
  return presentations
}
