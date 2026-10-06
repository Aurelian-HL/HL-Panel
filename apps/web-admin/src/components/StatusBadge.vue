<script setup lang="ts">
import { computed } from 'vue'

const props = defineProps<{ status: string }>()

const normalized = computed(() => props.status.toLowerCase())
const label = computed(() => ({
  online: '在线', offline: '离线', syncing: '同步中', failed: '应用失败', retired: '已退役',
  succeeded: '已成功', pending: '待处理', prepare: '准备中', unknown: '未知',
}[normalized.value] ?? (props.status || '未知')))
</script>

<template>
  <span class="status-badge" :class="`status-badge--${normalized}`"><i aria-hidden="true" />{{ label }}</span>
</template>
