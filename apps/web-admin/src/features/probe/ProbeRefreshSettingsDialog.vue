<script setup lang="ts">
import { ref } from 'vue'

import BaseModal from '@/components/BaseModal.vue'
import { type ProbeRefreshIntervals, validRefreshInterval } from './refreshPreferences'

const props = defineProps<{ intervals: ProbeRefreshIntervals }>()
const emit = defineEmits<{ close: []; save: [intervals: ProbeRefreshIntervals] }>()
const foreground = ref(props.intervals.foreground)
const background = ref(props.intervals.background)
const error = ref('')

function save(): void {
  const next = { foreground: Number(foreground.value), background: Number(background.value) }
  if (!validRefreshInterval(next.foreground) || !validRefreshInterval(next.background)) {
    error.value = '刷新间隔须为 1000 至 300000 毫秒的整数。'
    return
  }
  emit('save', next)
}
</script>

<template>
  <BaseModal title="刷新间隔设置" description="单位为毫秒，保存后立即生效。" width="small" @close="emit('close')">
    <form class="form-stack" @submit.prevent="save">
      <label class="field"><span>前台刷新间隔（毫秒）</span><input v-model.number="foreground" type="number" min="1000" max="300000" step="1000" required /></label>
      <label class="field"><span>后台刷新间隔（毫秒）</span><input v-model.number="background" type="number" min="1000" max="300000" step="1000" required /></label>
      <p v-if="error" class="form-error" role="alert">{{ error }}</p>
      <div class="probe-refresh-dialog__actions"><button class="button button--secondary" type="button" @click="emit('close')">取消</button><button class="button button--primary" type="submit">保存</button></div>
    </form>
  </BaseModal>
</template>
