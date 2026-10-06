<script setup lang="ts">
import { computed } from 'vue'

const props = defineProps<{ label: string; percent?: number }>()
const value = computed(() => props.percent !== undefined && props.percent >= 0 && props.percent <= 100 ? props.percent : undefined)
const tone = computed(() => value.value === undefined ? 'empty' : value.value >= 90 ? 'high' : value.value >= 70 ? 'medium' : 'normal')
</script>

<template>
  <div class="probe-meter" :class="[`probe-meter--${tone}`, `probe-meter--${label === 'CPU' ? 'cpu' : label === '内存' ? 'memory' : 'disk'}`, { 'probe-meter--filled': value !== undefined && value >= 35 }]">
    <div class="probe-meter__heading"><span>{{ label }}</span></div>
    <div class="probe-meter__track" :role="value === undefined ? undefined : 'progressbar'" :aria-label="value === undefined ? undefined : `${label} 使用率`" :aria-valuemin="value === undefined ? undefined : 0" :aria-valuemax="value === undefined ? undefined : 100" :aria-valuenow="value">
      <span :style="{ width: `${value ?? 0}%` }" />
      <strong>{{ value === undefined ? '未采集' : `${value}%` }}</strong>
    </div>
  </div>
</template>
