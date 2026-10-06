<script setup lang="ts">
defineProps<{ status: string; paused?: boolean }>()
const labels: Record<string, string> = {
  active: '正常', disabled: '已停用', expired: '已到期', quota_exhausted: '流量已用完',
  pending_activation: '待激活', paused: '已暂停', customer_disabled: '用户已停用', customer_expired: '用户已到期',
}
</script>

<template><span class="status-pill" :class="`status-${paused ? 'paused' : status}`">{{ paused ? '已暂停' : labels[status] ?? status }}</span></template>
