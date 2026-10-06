<script setup lang="ts">
import { LoaderCircle, Trash2 } from '@lucide/vue'
import type { ForwardRule } from '@/api/business'
import BaseModal from '@/components/BaseModal.vue'

defineProps<{ rules: ForwardRule[]; busy: boolean }>()
defineEmits<{ close: []; confirm: [] }>()
</script>

<template>
  <BaseModal title="删除转发规则" :description="`将永久删除 ${rules.length} 条规则`" width="small" :close-disabled="busy" @close="$emit('close')">
    <div class="delete-confirmation">
      <p>删除后将撤销相关监听端口和无依赖的绑定端点；不会删除客户或设备组。有客户身份或活动连接的端点不能删除。</p>
      <ul><li v-for="rule in rules.slice(0, 8)" :key="rule.id">{{ rule.name }} · {{ rule.protocol.toUpperCase() }} :{{ rule.listen_port }}</li></ul>
      <p v-if="rules.length > 8" class="table-secondary">另有 {{ rules.length - 8 }} 条规则</p>
    </div>
    <template #footer>
      <button class="button button--secondary" type="button" :disabled="busy" @click="$emit('close')">取消</button>
      <button class="button button--danger" type="button" :disabled="busy" @click="$emit('confirm')"><LoaderCircle v-if="busy" :size="14" class="spin" /><Trash2 v-else :size="14" />{{ busy ? '删除中' : '确认删除' }}</button>
    </template>
  </BaseModal>
</template>
