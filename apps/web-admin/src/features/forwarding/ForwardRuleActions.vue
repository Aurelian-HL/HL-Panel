<script setup lang="ts">
import { computed } from 'vue'
import { Copy, Link, Pause, Pencil, Play, Trash2 } from '@lucide/vue'
import type { ForwardRule } from '@/api/business'
import { effectiveIngressProtocol } from '@/lib/businessFormatters'

const props = defineProps<{ rule: ForwardRule; busyId: string; compact?: boolean }>()
const quotaExhausted = computed(() => (props.rule.traffic_limit_bytes ?? 0) > 0 && (props.rule.traffic_used_bytes ?? 0) >= props.rule.traffic_limit_bytes!)
defineEmits<{ edit: []; copy: []; connection: []; toggle: []; delete: [] }>()
</script>

<template>
  <div class="rule-actions" :class="{ 'rule-actions--compact': compact }">
    <button type="button" class="rule-action" :disabled="!!busyId" title="编辑规则" :aria-label="`编辑规则 ${rule.name}`" @click="$emit('edit')"><Pencil :size="15" /><span v-if="!compact">编辑</span></button>
    <button type="button" class="rule-action" :disabled="!!busyId" title="复制规则" :aria-label="`复制规则 ${rule.name}`" @click="$emit('copy')"><Copy :size="15" /><span v-if="!compact">复制</span></button>
    <button v-if="effectiveIngressProtocol(rule) === 'vless_reality'" type="button" class="rule-action" :disabled="!!busyId" title="生成订阅并下载导入包" :aria-label="`生成订阅并下载导入包 ${rule.name}`" @click="$emit('connection')"><Link :size="15" /><span v-if="!compact">订阅包</span></button>
    <button v-if="quotaExhausted" type="button" class="rule-action" :disabled="!!busyId" title="调整流量额度" :aria-label="`调整流量额度 ${rule.name}`" @click="$emit('edit')"><Pencil :size="15" /><span v-if="!compact">调整额度</span></button>
    <button v-else type="button" class="rule-action" :disabled="!!busyId" :title="rule.paused ? '恢复规则' : '暂停规则'" :aria-label="`${rule.paused ? '恢复规则' : '暂停规则'} ${rule.name}`" @click="$emit('toggle')"><Play v-if="rule.paused" :size="15" /><Pause v-else :size="15" /><span v-if="!compact">{{ rule.paused ? '恢复' : '暂停' }}</span></button>
    <button type="button" class="rule-action rule-action--delete" :disabled="!!busyId" title="删除规则" :aria-label="`删除规则 ${rule.name}`" @click="$emit('delete')"><Trash2 :size="15" /><span v-if="!compact">删除</span></button>
  </div>
</template>

<style scoped>
.rule-actions { display: flex; flex-wrap: wrap; align-items: center; gap: 5px; }
.rule-action { display: inline-flex; min-height: 32px; align-items: center; justify-content: center; gap: 4px; padding: 5px 9px; border: 1px solid var(--gray-300); border-radius: 4px; background: #fff; color: var(--gray-700); font: inherit; font-size: 12px; cursor: pointer; }
.rule-action:hover:not(:disabled) { border-color: var(--navy-700); background: var(--navy-100); color: var(--navy-700); }
.rule-action:focus-visible { outline: 2px solid var(--navy-700); outline-offset: 2px; }
.rule-action:disabled { cursor: wait; opacity: .5; }
.rule-action--delete:hover:not(:disabled) { border-color: var(--red-700); background: var(--red-100); color: var(--red-700); }
.rule-actions--compact { flex-wrap: nowrap; }
.rule-actions--compact .rule-action { flex: 0 0 30px; width: 30px; height: 30px; min-height: 30px; padding: 0; }
@media (max-width: 760px) {
  .rule-actions { display: grid; grid-template-columns: repeat(3, minmax(0, 1fr)); gap: 7px; }
  .rule-action { min-height: 36px; padding: 5px; }
}
</style>
