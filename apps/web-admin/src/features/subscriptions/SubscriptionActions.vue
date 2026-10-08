<script setup lang="ts">
import { Download, Eye, Pencil, Send, ShieldOff } from '@lucide/vue'
import type { Subscription } from '@/api/subscriptions'
defineProps<{ item: Subscription; busy: boolean }>()
defineEmits<{ inspect: []; edit: []; publish: []; download: []; toggle: [] }>()
</script>

<template>
  <div class="subscription-actions">
    <button class="button button--quiet" :disabled="busy" @click="$emit('inspect')"><Eye :size="14" />详情</button>
    <button class="button button--quiet" :disabled="busy || item.state !== 'active'" @click="$emit('edit')"><Pencil :size="14" />编辑</button>
    <button class="button button--quiet" :disabled="busy || item.state !== 'active' || !item.pending_update" @click="$emit('publish')"><Send :size="14" />发布更新</button>
    <button class="button button--quiet" :disabled="busy || !item.published_revision || item.state !== 'active'" @click="$emit('download')"><Download :size="14" />导入包</button>
    <button class="button button--quiet" :disabled="busy" @click="$emit('toggle')"><ShieldOff :size="14" />{{ item.state === 'revoked' ? '恢复' : '停用' }}</button>
  </div>
</template>

<style scoped>
.subscription-actions { display: flex; flex-wrap: wrap; gap: 5px; }
.subscription-actions .button { min-height: 32px; padding: 5px 8px; white-space: nowrap; }
</style>
