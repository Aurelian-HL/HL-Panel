<script setup lang="ts">
import { computed, ref } from 'vue'
import { LoaderCircle } from '@lucide/vue'

import { api, type DeviceGroup, type DeviceGroupKind, type LoadBalancingStrategy } from '@/api'
import type { UserGroup } from '@/api/business'
import BaseModal from '@/components/BaseModal.vue'
import { displayError } from '@/lib/displayFormatters'

defineProps<{ userGroups: UserGroup[] }>()
const emit = defineEmits<{ close: []; created: [group: DeviceGroup] }>()
const name = ref('')
const userGroupId = ref('')
const kind = ref<DeviceGroupKind | ''>('')
const hideInProbe = ref(false)
const selectionPolicy = ref<LoadBalancingStrategy>('weighted_least_connections')
const description = ref('')
const submitting = ref(false)
const errorMessage = ref('')

const kinds: Array<{ value: DeviceGroupKind; label: string; caption: string }> = [
  { value: 'ENTRY', label: '入口', caption: '承载入口直出，或作为经出口组路径的前置入口' },
  { value: 'EXIT', label: '出口', caption: '作为经出口组路径的落地出口' },
]

const selectionPolicies: Array<{ value: LoadBalancingStrategy; label: string; caption: string }> = [
  { value: 'weighted_least_connections', label: '加权最少连接', caption: '优先分配给当前连接少的健康节点' },
  { value: 'weighted_round_robin', label: '加权轮询', caption: '按权重依次分配新连接' },
  { value: 'rendezvous_hash', label: '稳定哈希', caption: '按连接来源稳定选择健康节点' },
]

const canSubmit = computed(() => name.value.trim().length >= 2 && name.value.trim().length <= 120 && !!kind.value && description.value.length <= 500)

async function submit(): Promise<void> {
  if (submitting.value) return
  errorMessage.value = ''
  if (!canSubmit.value) {
    errorMessage.value = '设备组名称需要 2 至 120 个字符，描述不能超过 500 个字符'
    return
  }
  submitting.value = true
  try {
    const group = await api.createDeviceGroup({ name: name.value.trim(), kind: kind.value as DeviceGroupKind, user_group_id: userGroupId.value, hide_in_probe: hideInProbe.value, selection_policy: selectionPolicy.value, description: description.value.trim() })
    emit('created', group)
  } catch (error) {
    errorMessage.value = displayError(error)
  } finally {
    submitting.value = false
  }
}
</script>

<template>
  <BaseModal title="添加设备组" width="small" :close-disabled="submitting" @close="$emit('close')">
    <form id="create-group-form" class="form-stack ny-compact-form" @submit.prevent="submit">
      <label class="field"><span>名称</span><input v-model="name" maxlength="120" autofocus /></label>
      <label class="field"><span>用户组 ID</span><select v-model="userGroupId"><option value="">不指定</option><option v-for="item in userGroups" :key="item.id" :value="item.id">{{ item.name }} · {{ item.id }}</option></select></label>
      <label class="field"><span>类型</span><select v-model="kind" required><option value="" disabled>请选择</option><option v-for="item in kinds" :key="item.value" :value="item.value">{{ item.label }}</option></select></label>
      <label class="field field--checkbox"><input v-model="hideInProbe" type="checkbox" /><span>在探针中隐藏</span></label>
      <label class="field"><span>备注</span><textarea v-model="description" rows="2" maxlength="500" /></label>
      <details class="ny-advanced"><summary>高级选项</summary><div class="form-stack">
        <label class="field"><span>负载均衡</span><select v-model="selectionPolicy"><option v-for="item in selectionPolicies" :key="item.value" :value="item.value">{{ item.label }}</option></select></label>
      </div></details>
      <p v-if="errorMessage" class="form-error" role="alert">{{ errorMessage }}</p>
    </form>
    <template #footer>
      <button class="button button--secondary" type="button" :disabled="submitting" @click="$emit('close')">取消</button>
      <button class="button button--primary" type="submit" form="create-group-form" :disabled="submitting || !canSubmit"><LoaderCircle v-if="submitting" class="spin" :size="14" />{{ submitting ? '保存中' : '确定' }}</button>
    </template>
  </BaseModal>
</template>
