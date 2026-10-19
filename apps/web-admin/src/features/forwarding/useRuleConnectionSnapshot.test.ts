import { defineComponent, ref } from 'vue'
import { flushPromises, mount } from '@vue/test-utils'
import { afterEach, expect, it, vi } from 'vitest'
import type { ForwardRule } from '@/api/business'
import { useRuleConnectionSnapshot } from './useRuleConnectionSnapshot'

const online: ForwardRule = {
  id: 'one', name: 'one', customer_id: 'customer', rule_group_id: '', entry_group_id: 'entry',
  exit_group_id: '', egress_mode: 'DIRECT', protocol: 'tcp', listen_port: 443,
  targets: [], selection_policy: 'round_robin', paused: false, description: '', revision: 1,
  status: 'active', connection: { status: 'online', connections: 2, reason: 'established', valid_for_ms: 330000 },
}
const offline = (): ForwardRule => ({ ...online,
  connection: { status: 'offline', connections: 0, reason: 'no_connections', valid_for_ms: 330000 },
})

afterEach(() => vi.useRealTimers())

function harness() {
  const rules = ref<ForwardRule[]>([online])
  let snapshot!: ReturnType<typeof useRuleConnectionSnapshot>
  const wrapper = mount(defineComponent({ setup() {
    snapshot = useRuleConnectionSnapshot(rules)
    return () => null
  } }))
  return { rules, snapshot, wrapper }
}

it('holds one observation through frequent polls and publishes all rules at five-minute boundaries', async () => {
  vi.useFakeTimers({ toFake: ['setTimeout', 'clearTimeout', 'performance'] })
  const { rules, snapshot, wrapper } = harness()
  try {
    const first = snapshot.value.one
    for (let second = 1; second < 300; second++) {
      await vi.advanceTimersByTimeAsync(1000)
      rules.value = [offline(), { ...offline(), id: 'two' }]
      expect(snapshot.value.one).toBe(first)
    }
    expect(snapshot.value.one!.label).toBe('在线')
    expect(snapshot.value.two!.label).toBe('不在线')
    await vi.advanceTimersByTimeAsync(1000)
    expect(snapshot.value.one!.label).toBe('不在线')
    // A new response cannot immediately change the just-published snapshot.
    rules.value = [{ ...online, connection: { ...online.connection! } }]
    expect(snapshot.value.one!.label).toBe('不在线')
    expect(snapshot.value.two).toBeUndefined()
    await vi.advanceTimersByTimeAsync(299999)
    expect(snapshot.value.one!.label).toBe('不在线')
    await vi.advanceTimersByTimeAsync(1)
    expect(snapshot.value.one!.label).toBe('在线')
  } finally { wrapper.unmount() }
})

it('expires old observations at a sampling boundary when polling stops, without renewing them', async () => {
  vi.useFakeTimers({ toFake: ['setTimeout', 'clearTimeout', 'performance'] })
  const { rules, snapshot, wrapper } = harness()
  try {
    await vi.advanceTimersByTimeAsync(300000)
    expect(snapshot.value.one!.label).toBe('在线')
    // Reading the same heartbeat again must not renew its age.
    rules.value = [...rules.value]
    await vi.advanceTimersByTimeAsync(300000)
    expect(snapshot.value.one!.label).toBe('不在线')
  } finally { wrapper.unmount() }
})

it('invalidates only a changed rule, preserves other rules on additions and cleans up its timer', async () => {
  vi.useFakeTimers({ toFake: ['setTimeout', 'clearTimeout', 'performance'] })
  const { rules, snapshot, wrapper } = harness()
  rules.value = [offline(), { ...online, id: 'two' }]
  expect(snapshot.value.one!.label).toBe('在线')
  rules.value[0] = { ...offline(), paused: true }
  expect(snapshot.value.one!.label).toBe('已暂停')
  expect(snapshot.value.two!.label).toBe('在线')
  rules.value[0] = offline()
  expect(snapshot.value.one!.label).toBe('不在线')
  rules.value[1] = { ...online, id: 'two', listen_port: 444 }
  expect(snapshot.value.two!.label).toBe('不在线')
  wrapper.unmount()
  await flushPromises()
  expect(vi.getTimerCount()).toBe(0)
})
