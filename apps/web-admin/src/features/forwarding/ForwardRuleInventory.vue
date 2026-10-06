<script setup lang="ts">
import { computed } from 'vue'
import type { DeviceGroup } from '@/api'
import type { ForwardRule, GroupNetwork, RuleGroup } from '@/api/business'
import BusinessStatus from '@/components/BusinessStatus.vue'
import { byteAmount, effectiveIngressProtocol, ingressProtocolLabel, targetAddress } from '@/lib/businessFormatters'
import { ruleDisplayStatus } from '@/lib/ruleActivation'
import ForwardRuleActions from './ForwardRuleActions.vue'

const props = withDefaults(defineProps<{
  rules: ForwardRule[]
  devices: DeviceGroup[]
  ruleGroups: RuleGroup[]
  networks?: GroupNetwork[]
  trafficByRule?: Record<string, number>
  selectedIds: string[]
  busyId: string
}>(), { networks: () => [], trafficByRule: () => ({}) })
const emit = defineEmits<{ select: [rule: ForwardRule, selected: boolean]; edit: [rule: ForwardRule]; copy: [rule: ForwardRule]; connection: [rule: ForwardRule]; toggle: [rule: ForwardRule]; delete: [rule: ForwardRule] }>()

const allSelected = computed(() => props.rules.length > 0 && props.rules.every((rule) => props.selectedIds.includes(rule.id)))
const someSelected = computed(() => props.rules.some((rule) => props.selectedIds.includes(rule.id)) && !allSelected.value)
const deviceName = (id: string) => props.devices.find((item) => item.id === id)?.name ?? '设备组不可用'
const network = (id: string) => props.networks.find((item) => item.group_id === id)
const entryAddress = (rule: ForwardRule) => {
  const host = network(rule.entry_group_id)?.connect_host.trim()
  return host ? targetAddress(host, rule.listen_port) : `监听端口：${rule.listen_port}`
}
const exitName = (rule: ForwardRule) => rule.egress_mode === 'DIRECT' ? '组内直出' : deviceName(rule.exit_group_id)
const multiplier = (id: string) => {
  const value = network(id)?.traffic_multiplier
  return typeof value === 'number' && Number.isFinite(value) && value >= 0 ? value : null
}
// Target addresses stay separate from upstream authentication material.
const targetSummary = (rule: ForwardRule) => {
  if (effectiveIngressProtocol(rule) === 'vless_reality') {
    const host = rule.vless_socks5_host?.trim()
    const port = rule.vless_socks5_port ?? 0
    if (host && Number.isInteger(port) && port > 0) return targetAddress(host, port)
  }
  return rule.targets.map((target) => targetAddress(target.host, target.port)).join('、') || '—'
}
const protocolSummary = (rule: ForwardRule) => `${ingressProtocolLabel(effectiveIngressProtocol(rule))} / ${rule.protocol.toUpperCase()}`
const usedTraffic = (id: string) => {
  const value = props.trafficByRule[id]
  return typeof value === 'number' && Number.isFinite(value) && value >= 0 ? byteAmount(value) : '—'
}
function selectVisible(selected: boolean): void {
  if (props.busyId) return
  for (const rule of props.rules) emit('select', rule, selected)
}
</script>
<template>
  <div class="business-inventory rule-inventory">
    <div class="business-desktop table-wrap">
      <table class="data-table business-table rule-table">
        <colgroup><col class="rule-column-select" /><col class="rule-column-name" /><col class="rule-column-entry" /><col class="rule-column-exit" /><col class="rule-column-traffic" /><col class="rule-column-status" /><col class="rule-column-actions" /></colgroup>
        <thead><tr>
          <th class="rule-select-column"><input type="checkbox" :checked="allSelected" :indeterminate="someSelected" :disabled="!!busyId || !rules.length" aria-label="选择全部规则" @change="selectVisible(($event.target as HTMLInputElement).checked)" /></th>
          <th>规则名</th><th>入口</th><th>出口</th><th>已用流量</th><th>状态</th><th>操作</th>
        </tr></thead>
        <tbody><tr v-for="rule in rules" :key="rule.id">
          <td class="rule-select-column"><input type="checkbox" :checked="selectedIds.includes(rule.id)" :disabled="!!busyId" :aria-label="`选择规则 ${rule.name}`" @change="$emit('select', rule, ($event.target as HTMLInputElement).checked)" /></td>
          <td><strong class="table-primary">{{ rule.name }}</strong><small class="table-secondary rule-protocol">{{ protocolSummary(rule) }}</small><small v-if="rule.description" class="table-secondary">{{ rule.description }}</small></td>
          <td class="rule-entry-cell"><div class="rule-route-heading"><span>入口：{{ deviceName(rule.entry_group_id) }}</span><small v-if="multiplier(rule.entry_group_id) !== null" class="rule-multiplier">倍率 {{ multiplier(rule.entry_group_id) }}</small></div><span class="rule-address">{{ entryAddress(rule) }}</span></td>
          <td class="rule-exit-cell"><div class="rule-route-heading"><span>出口：{{ exitName(rule) }}</span><small v-if="rule.egress_mode !== 'DIRECT' && multiplier(rule.exit_group_id) !== null" class="rule-multiplier">倍率 {{ multiplier(rule.exit_group_id) }}</small></div><span class="rule-address rule-targets">{{ targetSummary(rule) }}</span></td>
          <td class="rule-used-traffic">{{ usedTraffic(rule.id) }}</td>
          <td><BusinessStatus :status="ruleDisplayStatus(rule)" :paused="rule.paused" /></td>
          <td><ForwardRuleActions :rule="rule" :busy-id="busyId" compact @edit="$emit('edit', rule)" @copy="$emit('copy', rule)" @connection="$emit('connection', rule)" @toggle="$emit('toggle', rule)" @delete="$emit('delete', rule)" /></td>
        </tr></tbody>
      </table>
    </div>
    <div class="business-mobile">
      <article v-for="rule in rules" :key="rule.id" class="business-card">
        <header><div class="rule-card-select"><input type="checkbox" :checked="selectedIds.includes(rule.id)" :disabled="!!busyId" :aria-label="`选择规则 ${rule.name}`" @change="$emit('select', rule, ($event.target as HTMLInputElement).checked)" /><div><h3>{{ rule.name }}</h3><p>{{ protocolSummary(rule) }}</p></div></div><BusinessStatus :status="ruleDisplayStatus(rule)" :paused="rule.paused" /></header>
        <dl>
          <div class="rule-card-route"><dt>入口</dt><dd><div class="rule-route-heading"><span>{{ deviceName(rule.entry_group_id) }}</span><small v-if="multiplier(rule.entry_group_id) !== null" class="rule-multiplier">倍率 {{ multiplier(rule.entry_group_id) }}</small></div><span class="rule-address">{{ entryAddress(rule) }}</span></dd></div>
          <div class="rule-card-route"><dt>出口</dt><dd><div class="rule-route-heading"><span>{{ exitName(rule) }}</span><small v-if="rule.egress_mode !== 'DIRECT' && multiplier(rule.exit_group_id) !== null" class="rule-multiplier">倍率 {{ multiplier(rule.exit_group_id) }}</small></div><span class="rule-address rule-targets">{{ targetSummary(rule) }}</span></dd></div>
          <div><dt>已用流量</dt><dd class="rule-used-traffic">{{ usedTraffic(rule.id) }}</dd></div>
          <div v-if="rule.description"><dt>备注</dt><dd>{{ rule.description }}</dd></div>
        </dl>
        <footer><ForwardRuleActions :rule="rule" :busy-id="busyId" @edit="$emit('edit', rule)" @copy="$emit('copy', rule)" @connection="$emit('connection', rule)" @toggle="$emit('toggle', rule)" @delete="$emit('delete', rule)" /></footer>
      </article>
    </div>
  </div>
</template>

<style scoped>
.rule-inventory .rule-table { width: 100%; min-width: 930px; table-layout: fixed; }
.rule-inventory .rule-table th { width: auto; }
.rule-column-select { width: 40px; }
.rule-column-name { width: calc(24% - 102.24px); }
.rule-column-entry { width: calc(40% - 170.4px); }
.rule-column-exit { width: calc(36% - 153.36px); }
.rule-column-traffic { width: 100px; }
.rule-column-status { width: 96px; }
.rule-column-actions { width: 190px; }
.rule-inventory .rule-table td, .rule-inventory .rule-table th { padding: 12px 9px; }
.rule-select-column input, .rule-card-select input { width: 15px; height: 15px; cursor: pointer; accent-color: var(--navy-700); }
.rule-route-heading { display: flex; flex-wrap: wrap; align-items: center; gap: 5px 7px; line-height: 1.6; }
.rule-route-heading > span { min-width: 0; overflow-wrap: anywhere; }
.rule-multiplier { flex: 0 0 auto; padding: 0 5px; border: 1px solid #b7dfad; border-radius: 3px; color: #45882f; font-size: 10px; font-weight: 400; line-height: 20px; white-space: nowrap; }
.rule-address { display: block; margin-top: 4px; color: var(--gray-800); overflow-wrap: anywhere; font-size: 12px; line-height: 1.6; }
.rule-protocol { font-size: 10px; }
.rule-used-traffic { font-variant-numeric: tabular-nums; white-space: nowrap; }
.rule-card-route { grid-column: 1 / -1; }
@media (max-width: 1100px) and (min-width: 761px) {
  .rule-inventory .business-desktop { display: none; }
  .rule-inventory .business-mobile { display: grid; grid-template-columns: repeat(2, minmax(0, 1fr)); gap: 12px; }
}
@media (max-width: 760px) {
  .rule-inventory .business-desktop { display: none; }
  .rule-inventory .business-mobile { display: flex; flex-direction: column; gap: 10px; }
  .rule-inventory .business-card footer { display: block; }
}
</style>
