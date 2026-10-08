<script setup lang="ts">
import { ref } from 'vue'
import { LoaderCircle, Trash2 } from '@lucide/vue'
import { api } from '@/api'
import BaseModal from '@/components/BaseModal.vue'
import { useMutationKey } from '@/composables/useMutationKey'
import { displayError } from '@/lib/displayFormatters'

const props = defineProps<{ nodeId: string; label: string }>()
const emit = defineEmits<{ close: []; deleted: [nodeId: string] }>()
const busy = ref(false)
const error = ref('')
const mutationKey = useMutationKey()
async function remove(): Promise<void> {
  if (busy.value) return
  busy.value = true
  error.value = ''
  try {
    await api.deleteNode(props.nodeId, mutationKey({ node_id: props.nodeId }))
    emit('deleted', props.nodeId)
  } catch (cause) {
    error.value = displayError(cause)
  } finally {
    busy.value = false
  }
}
</script>

<template>
  <BaseModal title="删除离线节点" :description="label" width="small" :close-disabled="busy" @close="emit('close')">
    <div class="delete-confirmation">
      <p>确认清理这个离线节点？</p>
      <ul>
        <li>从节点清单、设备探针及设备组中移除，旧凭据将停止接收心跳。</li>
        <li>历史流量和操作日志保留，不会卸载远程机器上的服务。</li>
        <li>需要重新接入时，生成新令牌并执行一条命令安装即可。</li>
      </ul>
      <p v-if="error" class="form-error" role="alert">{{ error }}</p>
    </div>
    <template #footer>
      <button class="button button--secondary" type="button" :disabled="busy" @click="emit('close')">取消</button>
      <button class="button button--danger" type="button" :disabled="busy" @click="remove"><LoaderCircle v-if="busy" class="spin" :size="15" /><Trash2 v-else :size="15" />{{ busy ? '删除中' : '确认删除' }}</button>
    </template>
  </BaseModal>
</template>
