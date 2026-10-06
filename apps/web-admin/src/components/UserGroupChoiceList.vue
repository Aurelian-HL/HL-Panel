<script setup lang="ts">
import { computed, ref } from 'vue'
import { Check } from '@lucide/vue'

import type { UserGroup } from '@/api/business'

const props = defineProps<{ modelValue: string[]; groups: UserGroup[]; label: string }>()
const emit = defineEmits<{ 'update:modelValue': [ids: string[]] }>()
const query = ref('')
const visible = computed(() => {
  const keyword = query.value.trim().toLowerCase()
  return props.groups.filter((group) => !keyword || `${group.name} ${group.description}`.toLowerCase().includes(keyword))
})
const missing = computed(() => props.modelValue.filter((id) => !props.groups.some((group) => group.id === id)))

function toggle(id: string): void {
  emit('update:modelValue', props.modelValue.includes(id) ? props.modelValue.filter((value) => value !== id) : [...props.modelValue, id])
}
</script>

<template>
  <fieldset class="choice-list fieldset-reset">
    <legend>{{ label }} <small>已选 {{ modelValue.length }} 项</small></legend>
    <input v-if="groups.length > 5" v-model="query" class="choice-search" :aria-label="`搜索${label}`" placeholder="搜索用户组" />
    <div class="choice-list__options">
      <button v-for="group in visible" :key="group.id" type="button" :aria-pressed="modelValue.includes(group.id)" :class="{ selected: modelValue.includes(group.id) }" @click="toggle(group.id)"><span>{{ group.name }}</span><Check v-if="modelValue.includes(group.id)" :size="15" /></button>
      <button v-for="id in missing" :key="id" type="button" class="selected choice-missing" aria-pressed="true" @click="toggle(id)">已不可用：{{ id }} · 移除</button>
    </div>
    <p v-if="!groups.length" class="field-help">还没有可选用户组，请先创建用户组。</p>
    <p v-else-if="!visible.length" class="field-help">没有匹配的用户组。</p>
    <p class="field-help">不选择表示允许全部用户组；选择后仅允许选中的用户组。</p>
  </fieldset>
</template>
