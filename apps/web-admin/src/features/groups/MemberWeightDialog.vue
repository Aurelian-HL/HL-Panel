<script setup lang="ts">
import { ref } from 'vue'
import { LoaderCircle } from '@lucide/vue'

import { api, type DeviceGroupMember } from '@/api'
import BaseModal from '@/components/BaseModal.vue'
import { displayError } from '@/lib/displayFormatters'

const props = defineProps<{ member: DeviceGroupMember; label: string }>()
const emit = defineEmits<{ close: []; saved: [member: DeviceGroupMember] }>()
const weight = ref(props.member.weight)
const saving = ref(false)
const errorMessage = ref('')
let operationKey: string | null = null

async function save(): Promise<void> {
  if (saving.value) return
  if (!Number.isSafeInteger(weight.value) || weight.value < 0 || weight.value > 1000) {
    errorMessage.value = '权重请输入 0 到 1000 的整数。'
    return
  }
  if (weight.value === props.member.weight) {
    emit('close')
    return
  }
  operationKey ??= crypto.randomUUID()
  saving.value = true
  errorMessage.value = ''
  try {
    const result = await api.updateDeviceGroupMemberWeight(props.member.group_id, props.member.node_id, weight.value, props.member.updated_at, operationKey)
    emit('saved', result.member)
  } catch (error) {
    errorMessage.value = displayError(error)
  } finally {
    saving.value = false
  }
}

function changeWeight(event: Event): void {
  weight.value = (event.target as HTMLInputElement).valueAsNumber
  operationKey = null
  errorMessage.value = ''
}
</script>

<template>
  <BaseModal title="更改权重" :description="label" :close-disabled="saving" @close="emit('close')">
    <form class="form-stack" @submit.prevent="save">
      <label class="form-field"><span>转发权重</span><input :value="weight" type="number" min="0" max="1000" step="1" required :disabled="saving" autofocus @input="changeWeight" /></label>
      <p class="form-note">当前权重 {{ member.weight }}。设为 0 后，这台机器不再参与该设备组的新连接调度；已有连接不受影响。</p>
      <p v-if="errorMessage" class="form-error" role="alert">{{ errorMessage }}</p>
      <div class="member-weight-actions"><button class="button button--secondary" type="button" :disabled="saving" @click="emit('close')">取消</button><button class="button button--primary" type="submit" :disabled="saving"><LoaderCircle v-if="saving" class="spin" :size="15" />保存</button></div>
    </form>
  </BaseModal>
</template>

<style scoped>
.member-weight-actions { display: flex; justify-content: flex-end; gap: 8px; }
</style>
