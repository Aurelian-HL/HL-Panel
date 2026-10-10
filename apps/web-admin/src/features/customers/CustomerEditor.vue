<script setup lang="ts">
import { onBeforeUnmount, reactive, ref } from 'vue'
import { LoaderCircle } from '@lucide/vue'
import { businessApi, type Customer, type CustomerInput, type UserGroup } from '@/api/business'
import BaseModal from '@/components/BaseModal.vue'
import { useMutationKey } from '@/composables/useMutationKey'
import { localDateTime } from '@/lib/businessFormatters'
import { displayError } from '@/lib/displayFormatters'

const props = defineProps<{ customer: Customer | null; groups: UserGroup[] }>()
const emit = defineEmits<{ close: []; saved: [customer: Customer] }>()
const originalExpiry = localDateTime(props.customer?.expires_at ?? null)
const form = reactive({
  username: props.customer?.username ?? '', display_name: props.customer?.display_name ?? '',
  user_group_id: props.customer?.user_group_id ?? '', password: '', disabled: props.customer?.disabled ?? false,
  expires_at: originalExpiry, traffic_gib: (props.customer?.traffic_limit_bytes ?? 0) / 1024 ** 3,
  max_rules: props.customer?.max_rules ?? 0, speed_limit_mbps: props.customer?.speed_limit_mbps ?? 0,
  ip_limit: props.customer?.ip_limit ?? 0, connection_limit: props.customer?.connection_limit ?? 0,
})
const busy = ref(false)
const error = ref('')
const mutationKey = useMutationKey()
onBeforeUnmount(() => { form.password = '' })

async function save(): Promise<void> {
  if (busy.value) return
  error.value = ''
  if (!/^[a-zA-Z0-9._@-]{1,64}$/.test(form.username.trim())) { error.value = '账号需为 1 至 64 位字母、数字或 . _ @ -'; return }
  if (!props.customer && !form.password) { error.value = '新建用户必须填写初始密码'; return }
  const numeric = [form.max_rules, form.speed_limit_mbps, form.ip_limit, form.connection_limit]
  if (numeric.some((value) => !Number.isSafeInteger(value) || value < 0) || !Number.isFinite(form.traffic_gib) || form.traffic_gib < 0 || !Number.isFinite(form.speed_limit_mbps) || form.speed_limit_mbps < 0) { error.value = '限额需为非负数，规则数、IP 数和连接数必须是整数'; return }
  const expiry = form.expires_at ? new Date(form.expires_at) : null
  if (expiry && Number.isNaN(expiry.getTime())) { error.value = '到期时间无效'; return }
  const trafficBytes = Math.round(form.traffic_gib * 1024 ** 3)
  if (!Number.isSafeInteger(trafficBytes)) { error.value = '流量额度过大'; return }
  const input: CustomerInput = {
    username: form.username.trim(), display_name: form.display_name.trim(), user_group_id: form.user_group_id,
    disabled: form.disabled, expires_at: props.customer && form.expires_at === originalExpiry ? props.customer.expires_at : expiry?.toISOString() ?? null,
    traffic_limit_bytes: trafficBytes, max_rules: form.max_rules, speed_limit_mbps: form.speed_limit_mbps,
    ip_limit: form.ip_limit, connection_limit: form.connection_limit, revision: props.customer?.revision ?? 0,
  }
  if (form.password) input.password = form.password
  busy.value = true
  try { const result = await businessApi.saveCustomer(input, props.customer?.id ?? null, mutationKey(input)); form.password = ''; emit('saved', result) }
  catch (cause) { error.value = displayError(cause) }
  finally { busy.value = false }
}
</script>

<template>
  <BaseModal :title="customer ? '编辑用户' : '添加用户'" width="small" :close-disabled="busy" @close="emit('close')">
    <form id="customer-editor" class="form-stack ny-compact-form" @submit.prevent="save">
      <label class="field"><span>用户名</span><input v-model="form.username" maxlength="64" required autocomplete="off" /></label>
      <label class="field"><span>{{ customer ? '重置密码' : '初始密码' }}</span><input v-model="form.password" type="password" autocomplete="new-password" :required="!customer" :placeholder="customer ? '留空不修改' : ''" /></label>
      <label class="field"><span>用户组</span><select v-model="form.user_group_id"><option value="">暂不分组（无线路权限）</option><option v-for="group in groups" :key="group.id" :value="group.id">{{ group.name }}</option></select></label>
      <details class="ny-advanced"><summary>高级选项</summary><div class="form-stack">
        <label class="field"><span>备注名称</span><input v-model="form.display_name" maxlength="120" /></label>
        <label class="field"><span>过期时间</span><input v-model="form.expires_at" type="datetime-local" /><small class="field-help">本地时间，留空不限。</small></label>
        <label class="field"><span>状态</span><select v-model="form.disabled"><option :value="false">启用</option><option :value="true">停用</option></select></label>
        <label class="field"><span>流量 · GB</span><input v-model.number="form.traffic_gib" type="number" min="0" step="any" required /></label>
        <label class="field"><span>最大规则数</span><input v-model.number="form.max_rules" type="number" min="0" max="1000000" step="1" required /></label>
        <label class="field"><span>限速 · Mbps</span><input v-model.number="form.speed_limit_mbps" type="number" min="0" max="10000000" step="1" required /></label>
        <label class="field"><span>IP 数限制</span><input v-model.number="form.ip_limit" type="number" min="0" max="10000000" step="1" required /></label>
        <label class="field"><span>连接数限制</span><input v-model.number="form.connection_limit" type="number" min="0" max="100000000" step="1" required /></label>
        <small class="field-help">0 表示不限。实时流量与限速尚未启用。</small>
      </div></details>
      <p v-if="error" class="form-error" role="alert">{{ error }}</p>
    </form>
    <template #footer><button class="button button--secondary" :disabled="busy" @click="emit('close')">取消</button><button class="button button--primary" form="customer-editor" type="submit" :disabled="busy"><LoaderCircle v-if="busy" :size="14" class="spin" />{{ busy ? '保存中' : '确定' }}</button></template>
  </BaseModal>
</template>
