<script setup lang="ts">
import { reactive, ref } from 'vue'
import { LoaderCircle } from '@lucide/vue'
import { businessApi, type RuleGroup, type RuleGroupInput } from '@/api/business'
import BaseModal from '@/components/BaseModal.vue'
import { useMutationKey } from '@/composables/useMutationKey'
import { displayError } from '@/lib/displayFormatters'

const props = defineProps<{ group: RuleGroup | null }>()
const emit = defineEmits<{ close: []; saved: [group: RuleGroup] }>()
const form = reactive<RuleGroupInput>({ name: props.group?.name ?? '', description: props.group?.description ?? '', revision: props.group?.revision ?? 0 })
const busy = ref(false)
const error = ref('')
const mutationKey = useMutationKey()

async function save(): Promise<void> {
  if (busy.value) return
  error.value = ''
  const input = { ...form, name: form.name.trim(), description: form.description.trim() }
  if (!input.name) { error.value = '请填写规则分组名称'; return }
  busy.value = true
  try { emit('saved', await businessApi.saveRuleGroup(input, props.group?.id ?? null, mutationKey(input))) }
  catch (cause) { error.value = displayError(cause) } finally { busy.value = false }
}
</script>

<template>
  <BaseModal :title="group ? '编辑规则分组' : '添加规则分组'" width="small" :close-disabled="busy" @close="emit('close')">
    <form id="rule-group-editor" class="form-stack ny-compact-form" @submit.prevent="save">
      <label class="field"><span>名称</span><input v-model="form.name" maxlength="128" required /></label>
      <label class="field"><span>备注</span><textarea v-model="form.description" maxlength="1024" rows="3" /></label>
      <p v-if="error" class="form-error" role="alert">{{ error }}</p>
    </form>
    <template #footer><button class="button button--secondary" :disabled="busy" @click="emit('close')">取消</button><button class="button button--primary" type="submit" form="rule-group-editor" :disabled="busy"><LoaderCircle v-if="busy" :size="14" class="spin" />{{ busy ? '保存中' : '确定' }}</button></template>
  </BaseModal>
</template>
