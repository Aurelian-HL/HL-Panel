<script setup lang="ts">
import { computed, ref } from 'vue'
import { LoaderCircle, Trash2 } from '@lucide/vue'
import { businessApi } from '@/api/business'
import BaseModal from '@/components/BaseModal.vue'
import { useMutationKey } from '@/composables/useMutationKey'
import { displayError } from '@/lib/displayFormatters'

const props = defineProps<{ kind: 'customer' | 'user-group'; item: { id: string; revision: number }; name: string }>()
const emit = defineEmits<{ close: []; deleted: [] }>()
const busy = ref(false)
const error = ref('')
const label = computed(() => props.kind === 'customer' ? '用户' : '用户组')
const mutationKey = useMutationKey()
async function remove(): Promise<void> {
  if (busy.value) return
  busy.value = true
  error.value = ''
  try {
    const key = mutationKey({ kind: props.kind, id: props.item.id, revision: props.item.revision })
    if (props.kind === 'customer') await businessApi.deleteCustomer(props.item.id, props.item.revision, key)
    else await businessApi.deleteUserGroup(props.item.id, props.item.revision, key)
    emit('deleted')
  } catch (cause) { error.value = displayError(cause) }
  finally { busy.value = false }
}
</script>
<template>
  <BaseModal :title="`删除${label}`" :description="name" width="small" :close-disabled="busy" @close="emit('close')">
    <div class="delete-confirmation">
      <p>确认删除“{{ name }}”？此操作无法在页面中撤销。</p>
      <p v-if="kind === 'customer'">删除后，该子账号将立即退出并无法登录。现有规则、订阅地址和线路配置继续保留并运行。</p>
      <p v-else>请先移出组内用户，并解除设备组、网络授权对该用户组的引用。</p>
      <p v-if="error" class="form-error" role="alert">{{ error }}</p>
    </div>
    <template #footer>
      <button class="button button--secondary" type="button" :disabled="busy" @click="emit('close')">取消</button>
      <button class="button button--danger" type="button" :disabled="busy" @click="remove"><LoaderCircle v-if="busy" class="spin" :size="15" /><Trash2 v-else :size="15" />{{ busy ? '删除中' : '确认删除' }}</button>
    </template>
  </BaseModal>
</template>
