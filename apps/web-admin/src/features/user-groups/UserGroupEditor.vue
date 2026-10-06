<script setup lang="ts">
import { computed, reactive, ref } from 'vue'
import { LoaderCircle } from '@lucide/vue'
import type { DeviceGroup } from '@/api'
import { businessApi, type UserGroup, type UserGroupInput } from '@/api/business'
import BaseModal from '@/components/BaseModal.vue'
import GroupChoiceList from '@/components/GroupChoiceList.vue'
import { useMutationKey } from '@/composables/useMutationKey'
import { displayError } from '@/lib/displayFormatters'
const props = defineProps<{ group: UserGroup | null; devices: DeviceGroup[] }>()
const emit = defineEmits<{ close: []; saved: [group: UserGroup] }>()
const form = reactive<UserGroupInput>({ name: props.group?.name ?? '', description: props.group?.description ?? '', allowed_entry_group_ids: [...(props.group?.allowed_entry_group_ids ?? [])], allowed_exit_group_ids: [...(props.group?.allowed_exit_group_ids ?? [])], allow_direct: props.group?.allow_direct ?? false, revision: props.group?.revision ?? 0 })
const entries = computed(() => props.devices.filter((item) => item.kind === 'ENTRY'))
const exits = computed(() => props.devices.filter((item) => item.kind === 'EXIT'))
const busy = ref(false)
const error = ref('')
const mutationKey = useMutationKey()
async function save(): Promise<void> {
  if (busy.value) return
  error.value = ''
  if (!form.name.trim()) { error.value = '请填写用户组名称'; return }
  const input = { ...form, name: form.name.trim(), description: form.description.trim() }
  busy.value = true
  try { emit('saved', await businessApi.saveUserGroup(input, props.group?.id ?? null, mutationKey(input))) }
  catch (cause) { error.value = displayError(cause) } finally { busy.value = false }
}
</script>
<template>
  <BaseModal :title="group ? '编辑用户组' : '添加用户组'" width="small" :close-disabled="busy" @close="emit('close')">
    <form id="user-group-editor" class="form-stack ny-compact-form" @submit.prevent="save">
      <label class="field"><span>名称</span><input v-model="form.name" maxlength="120" required /></label>
      <details class="ny-advanced" :open="!!group"><summary>设备组授权</summary><div class="form-stack">
      <GroupChoiceList v-model="form.allowed_entry_group_ids" :groups="entries" label="可用转发组 / 前置组" />
      <GroupChoiceList v-model="form.allowed_exit_group_ids" :groups="exits" label="可用落地出口组" />
      <label class="field"><span>允许入口直出</span><select v-model="form.allow_direct"><option :value="false">否</option><option :value="true">是</option></select></label>
      </div></details>
      <label class="field"><span>备注</span><textarea v-model="form.description" rows="2" maxlength="500" /></label>
      <p v-if="error" class="form-error" role="alert">{{ error }}</p>
    </form>
    <template #footer><button class="button button--secondary" :disabled="busy" @click="emit('close')">取消</button><button class="button button--primary" form="user-group-editor" type="submit" :disabled="busy"><LoaderCircle v-if="busy" :size="14" class="spin" />{{ busy ? '保存中' : '确定' }}</button></template>
  </BaseModal>
</template>
