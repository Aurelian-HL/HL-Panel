<script setup lang="ts">
import { computed, ref } from 'vue'
import { Check } from '@lucide/vue'
import type { DeviceGroup } from '@/api'

const props = defineProps<{ modelValue: string[]; groups: DeviceGroup[]; label: string }>()
const emit = defineEmits<{ 'update:modelValue': [ids: string[]] }>()
const query = ref('')
const visible = computed(() => props.groups.filter((group) => group.name.toLowerCase().includes(query.value.trim().toLowerCase())))
const missing = computed(() => props.modelValue.filter((id) => !props.groups.some((group) => group.id === id)))
function toggle(id: string): void {
  emit('update:modelValue', props.modelValue.includes(id) ? props.modelValue.filter((value) => value !== id) : [...props.modelValue, id])
}
</script>

<template>
  <fieldset class="choice-list fieldset-reset">
    <legend>{{ label }} <small>已选 {{ modelValue.length }} 项</small></legend>
    <input v-if="groups.length > 5" v-model="query" class="choice-search" :aria-label="`搜索${label}`" placeholder="搜索设备组" />
    <div class="choice-list__options">
      <button v-for="group in visible" :key="group.id" type="button" :aria-pressed="modelValue.includes(group.id)" :class="{ selected: modelValue.includes(group.id) }" @click="toggle(group.id)"><span>{{ group.name }}</span><Check v-if="modelValue.includes(group.id)" :size="15" /></button>
      <button v-for="id in missing" :key="id" type="button" class="selected choice-missing" aria-pressed="true" @click="toggle(id)">已不可用：{{ id }} · 移除</button>
    </div>
    <p v-if="!groups.length" class="field-help">还没有可选设备组，请先在设备组页面创建。</p>
    <p v-else-if="!visible.length" class="field-help">没有匹配的设备组。</p>
    <p class="field-help">未选择表示不授予该类设备组权限。</p>
  </fieldset>
</template>
