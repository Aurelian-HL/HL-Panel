<script setup lang="ts">
import { LoaderCircle, RotateCcw } from '@lucide/vue'

import type { EnforcementDecision } from '@/api/usage'
import BaseModal from '@/components/BaseModal.vue'

defineProps<{ decision: EnforcementDecision; busy: boolean }>()
defineEmits<{ close: []; confirm: [] }>()
</script>

<template>
  <BaseModal title="撤销访问停用" description="恢复该用户在对应规则上的访问" width="small" :close-disabled="busy" @close="$emit('close')">
    <div class="revoke-summary">
      <p>Agent 将收到一条启用命令。只有节点执行成功并回执后，状态才会变为“已撤销”。</p>
      <dl>
        <div><dt>用户</dt><dd>{{ decision.customer_id }}</dd></div>
        <div><dt>规则</dt><dd>{{ decision.rule_id }}</dd></div>
        <div><dt>协议</dt><dd>{{ decision.protocol.toUpperCase() }}</dd></div>
      </dl>
    </div>
    <template #footer>
      <button class="button button--secondary" type="button" :disabled="busy" @click="$emit('close')">取消</button>
      <button class="button button--primary" type="button" :disabled="busy" @click="$emit('confirm')">
        <LoaderCircle v-if="busy" :size="14" class="spin" /><RotateCcw v-else :size="14" />{{ busy ? '提交中' : '确认撤销' }}
      </button>
    </template>
  </BaseModal>
</template>

<style scoped>
.revoke-summary { display: grid; gap: 14px; color: var(--gray-700); font-size: 12px; line-height: 1.65; }
.revoke-summary dl { display: grid; gap: 1px; overflow: hidden; border: 1px solid var(--ny-border); border-radius: 3px; background: var(--ny-border); }
.revoke-summary dl div { display: grid; grid-template-columns: 72px minmax(0, 1fr); gap: 10px; padding: 8px 10px; background: #fff; }
.revoke-summary dt { color: var(--ny-muted); }
.revoke-summary dd { min-width: 0; overflow-wrap: anywhere; color: var(--gray-900); font-weight: 600; }
</style>
