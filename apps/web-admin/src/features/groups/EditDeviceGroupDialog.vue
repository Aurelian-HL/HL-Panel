<script setup lang="ts">
import { computed, reactive, ref } from 'vue'
import { LoaderCircle, Network } from '@lucide/vue'
import { api, type DeviceGroup, type LoadBalancingStrategy, type UpdateDeviceGroupRequest } from '@/api'
import type { GroupNetwork, UserGroup } from '@/api/business'
import BaseModal from '@/components/BaseModal.vue'
import { useMutationKey } from '@/composables/useMutationKey'
import { displayError } from '@/lib/displayFormatters'

const props = defineProps<{ group: DeviceGroup; userGroups: UserGroup[]; network: GroupNetwork | null }>()
const emit = defineEmits<{ close: []; saved: [group: DeviceGroup]; configureNetwork: [] }>()
const form = reactive<UpdateDeviceGroupRequest>({
  name: props.group.name,
  user_group_id: props.group.user_group_id || '',
  hide_in_probe: props.group.hide_in_probe || false,
  selection_policy: props.group.selection_policy,
  description: props.group.description,
  revision: props.group.metadata_revision ?? 1,
})
const strategies: { value: LoadBalancingStrategy; label: string }[] = [
  { value: 'weighted_least_connections', label: '加权最少连接' },
  { value: 'weighted_round_robin', label: '加权轮询' },
  { value: 'rendezvous_hash', label: '稳定哈希' },
]
const busy = ref(false)
const error = ref('')
const mutationKey = useMutationKey()
const unchanged = computed(() => form.name === props.group.name
  && form.user_group_id === (props.group.user_group_id || '')
  && form.hide_in_probe === (props.group.hide_in_probe || false)
  && form.selection_policy === props.group.selection_policy
  && form.description === props.group.description)

async function save(): Promise<void> {
  if (busy.value) return
  error.value = ''
  const input = { ...form, name: form.name.trim(), description: form.description.trim() }
  if (!input.name || input.name.length > 128 || input.description.length > 1024) {
    error.value = '名称不能为空且不超过 128 字符；备注不超过 1024 字符'
    return
  }
  busy.value = true
  try { emit('saved', await api.updateDeviceGroup(props.group.id, input, mutationKey(input))) }
  catch (cause) { error.value = displayError(cause) }
  finally { busy.value = false }
}
</script>

<template>
  <BaseModal :title="`编辑设备组 ${group.name}`" width="small" :close-disabled="busy" @close="emit('close')">
    <form id="edit-device-group-form" class="form-stack ny-compact-form" @submit.prevent="save">
      <label class="field"><span>名称</span><input v-model="form.name" required maxlength="128" /></label>
      <label class="field"><span>用户组 ID</span><select v-model="form.user_group_id"><option value="">不指定</option><option v-for="item in userGroups" :key="item.id" :value="item.id">{{ item.name }} · {{ item.id }}</option></select></label>
      <label class="field"><span>类型</span><input :value="group.kind === 'ENTRY' ? '入口' : '出口'" disabled /></label>
      <label class="field"><span>分配方式</span><select v-model="form.selection_policy"><option v-for="item in strategies" :key="item.value" :value="item.value">{{ item.label }}</option></select></label>
      <label class="field field--checkbox"><input v-model="form.hide_in_probe" type="checkbox" /><span>在探针中隐藏</span></label>
      <label class="field"><span>备注</span><textarea v-model="form.description" rows="3" maxlength="1024" /></label>
      <div class="edit-group-network">
        <strong>网络与路径配置</strong>
        <p>{{ network ? '连接地址、端口范围、流量倍率和出口策略已单独保存。' : '尚未保存网络与路径配置。' }}</p>
        <button class="button button--secondary" type="button" :disabled="busy || !unchanged" @click="emit('configureNetwork')"><Network :size="15" />编辑网络配置</button>
        <small v-if="!unchanged" class="field-help">请先保存或取消当前修改，再编辑网络配置。</small>
      </div>
      <p v-if="error" class="form-error" role="alert">{{ error }}</p>
    </form>
    <template #footer><button class="button button--secondary" :disabled="busy" @click="emit('close')">取消</button><button class="button button--primary" type="submit" form="edit-device-group-form" :disabled="busy"><LoaderCircle v-if="busy" :size="14" class="spin" />确定</button></template>
  </BaseModal>
</template>

<style scoped>
.edit-group-network { display: grid; gap: 6px; padding-top: 12px; border-top: 1px solid var(--gray-200); }
.edit-group-network strong { color: var(--navy-800); font-size: 12px; }
.edit-group-network p { margin: 0; color: var(--gray-600); font-size: 12px; line-height: 1.5; }
.edit-group-network .button { justify-self: start; }
</style>
