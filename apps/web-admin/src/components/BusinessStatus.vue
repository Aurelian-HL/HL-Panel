<script setup lang="ts">
import { computed } from 'vue'
import { unsupportedActivationLabels } from '@/lib/ruleActivation'
const props = defineProps<{ status: string; paused?: boolean }>()
const labels: Record<string, string> = { active: '可用', disabled: '已停用', expired: '已到期', quota_exhausted: '额度已用完', pending_activation: '待激活', pending_reality_parameters: '待补 Reality 参数', vless_runtime_material_pending: '等待节点运行材料', ...unsupportedActivationLabels, paused: '已暂停', customer_disabled: '客户已停用', customer_expired: '客户已到期', blocked: '授权受限', pending: '待处理', failed: '激活失败', running: '运行中' }
const label = computed(() => props.paused ? '已暂停' : (labels[props.status] ?? props.status) || '状态未知')
</script>
<template><span class="business-status" :class="{ 'business-status--muted': paused || status === 'disabled', 'business-status--warning': !paused && (['pending_activation', 'pending_reality_parameters', 'vless_runtime_material_pending', 'expired', 'quota_exhausted', 'blocked', 'failed'].includes(status) || Boolean(unsupportedActivationLabels[status])) }">{{ label }}</span></template>
