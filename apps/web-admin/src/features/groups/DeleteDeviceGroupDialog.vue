<script setup lang="ts">
import { ref } from 'vue'
import { LoaderCircle, Trash2 } from '@lucide/vue'

import { api, type DeviceGroup } from '@/api'
import BaseModal from '@/components/BaseModal.vue'
import { useMutationKey } from '@/composables/useMutationKey'
import { displayError } from '@/lib/displayFormatters'

const props = defineProps<{ group: DeviceGroup }>()
const emit = defineEmits<{ close: []; deleted: [replayed: boolean] }>()
const busy = ref(false)
const error = ref('')
const mutationKey = useMutationKey()

async function remove(): Promise<void> {
  if (busy.value) return
  busy.value = true
  error.value = ''
  try {
    const result = await api.deleteDeviceGroup(props.group.id, mutationKey({ group_id: props.group.id }))
    emit('deleted', result.replayed)
  } catch (cause) {
    error.value = displayError(cause)
  } finally {
    busy.value = false
  }
}
</script>

<template>
  <BaseModal title="删除设备组" :description="group.name" width="small" :close-disabled="busy" @close="$emit('close')">
    <div class="delete-confirmation">
      <p>确认删除这个设备组？删除不会删除节点或客户数据。</p>
      <ul><li>仍被规则、端点池或授权策略引用时，服务端会拒绝删除。</li><li>请先退役成员并解除引用，再重试。</li></ul>
      <p v-if="error" class="form-error" role="alert">{{ error }}</p>
    </div>
    <template #footer><button class="button button--secondary" type="button" :disabled="busy" @click="$emit('close')">取消</button><button class="button button--danger" type="button" :disabled="busy" @click="remove"><LoaderCircle v-if="busy" class="spin" :size="15" /><Trash2 v-else :size="15" />{{ busy ? '删除中' : '确认删除' }}</button></template>
  </BaseModal>
</template>
