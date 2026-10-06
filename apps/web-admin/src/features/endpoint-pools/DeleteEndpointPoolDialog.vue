<script setup lang="ts">
import { LoaderCircle, Trash2 } from '@lucide/vue'

import type { EndpointPool } from '@/api'
import BaseModal from '@/components/BaseModal.vue'

defineProps<{ pool: EndpointPool; busy: boolean; errorMessage: string }>()
defineEmits<{ close: []; confirm: [] }>()
</script>

<template>
  <BaseModal title="删除服务端点" width="small" :close-disabled="busy" @close="$emit('close')">
    <div class="delete-confirmation">
      <p>将删除服务端点“{{ pool.name }}”（{{ pool.hostname }}:{{ pool.port }}）。转发规则和设备组不会删除，但规则与该端点的绑定会解除。</p>
      <p>如有客户绑定或活跃运行配置，服务端会拒绝删除。请先处理这些依赖。</p>
      <p v-if="errorMessage" class="form-error" role="alert">{{ errorMessage }}</p>
    </div>
    <template #footer>
      <button class="button button--secondary" type="button" :disabled="busy" @click="$emit('close')">取消</button>
      <button class="button button--danger" type="button" :disabled="busy" @click="$emit('confirm')"><LoaderCircle v-if="busy" :size="14" class="spin" /><Trash2 v-else :size="14" />{{ busy ? '删除中' : '确认删除' }}</button>
    </template>
  </BaseModal>
</template>
