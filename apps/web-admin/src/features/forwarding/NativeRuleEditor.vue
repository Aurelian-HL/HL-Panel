<script setup lang="ts">
import { reactive, ref } from 'vue'
import { businessApi, type ForwardRule } from '@/api/business'
import { ApiError } from '@/api/http'
import BaseModal from '@/components/BaseModal.vue'
import { useMutationKey } from '@/composables/useMutationKey'
import { BYTES_PER_GB, ruleInput } from '@/lib/businessFormatters'
import { displayError } from '@/lib/displayFormatters'

const props = defineProps<{ rule: ForwardRule }>()
const emit = defineEmits<{ close: []; saved: [rule: ForwardRule]; unauthorized: [] }>()
const form = reactive({ name: props.rule.name, description: props.rule.description, paused: props.rule.paused,
  trafficGB: (props.rule.traffic_limit_bytes ?? 0) / BYTES_PER_GB, monthly: props.rule.traffic_quota_monthly ?? false })
const busy = ref(false)
const error = ref('')
const mutationKey = useMutationKey()
async function save(): Promise<void> {
  if (busy.value) return
  error.value = ''
  if (!form.name.trim() || !Number.isFinite(form.trafficGB) || form.trafficGB < 0) { error.value = '请填写名称和有效的流量额度'; return }
  const payload = { ...ruleInput(props.rule), name: form.name.trim(), description: form.description,
    paused: form.paused, traffic_limit_bytes: Math.round(form.trafficGB * BYTES_PER_GB), traffic_quota_monthly: form.monthly }
  busy.value = true
  try {
    emit('saved', await businessApi.saveRule(payload, props.rule.id, mutationKey(payload)))
  } catch (cause) {
    if (cause instanceof ApiError && cause.status === 401) emit('unauthorized')
    else error.value = displayError(cause)
  } finally { busy.value = false }
}
</script>

<template>
  <BaseModal title="编辑迁入线路" description="原入口、端口和连接参数保持不变，继续使用已有订阅。" width="small" :close-disabled="busy" @close="emit('close')">
    <form id="native-rule-form" class="native-form" @submit.prevent="save">
      <label>规则名称<input v-model="form.name" required maxlength="160" :disabled="busy"></label>
      <label>流量额度（GB，0 为不限）<input v-model.number="form.trafficGB" type="number" min="0" step="any" :disabled="busy"></label>
      <label class="native-check"><input v-model="form.monthly" type="checkbox" :disabled="busy">每月重置额度</label>
      <label class="native-check"><input v-model="form.paused" type="checkbox" :disabled="busy">暂停线路</label>
      <label>备注<textarea v-model="form.description" rows="3" :disabled="busy" /></label>
      <p v-if="error" role="alert" class="form-error">{{ error }}</p>
    </form>
    <template #footer><button class="button button--secondary" :disabled="busy" @click="emit('close')">取消</button><button class="button button--primary" form="native-rule-form" type="submit" :disabled="busy">{{ busy ? '保存中…' : '保存' }}</button></template>
  </BaseModal>
</template>

<style scoped>
.native-form { display: grid; gap: 16px; }
.native-form label { display: grid; gap: 7px; color: var(--gray-700); }
.native-form input:not([type=checkbox]), .native-form textarea { width: 100%; padding: 9px 11px; border: 1px solid var(--gray-300); border-radius: 5px; font: inherit; }
.native-form .native-check { display: flex; align-items: center; gap: 8px; }
</style>
