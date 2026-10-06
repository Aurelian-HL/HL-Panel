<script setup lang="ts">
import { AlertTriangle, Inbox, LoaderCircle, RefreshCw } from '@lucide/vue'

withDefaults(defineProps<{
  state: 'loading' | 'error' | 'empty'
  title?: string
  message?: string
}>(), { title: '', message: '' })

defineEmits<{ retry: [] }>()
</script>

<template>
  <div class="state-panel" :class="`state-panel--${state}`" role="status">
    <LoaderCircle v-if="state === 'loading'" class="spin" :size="28" aria-hidden="true" />
    <AlertTriangle v-else-if="state === 'error'" :size="28" aria-hidden="true" />
    <Inbox v-else :size="28" aria-hidden="true" />
    <h3>{{ title || (state === 'loading' ? '正在加载' : state === 'error' ? '加载失败' : '暂无数据') }}</h3>
    <p v-if="message">{{ message }}</p>
    <button v-if="state === 'error'" class="button button--secondary" type="button" @click="$emit('retry')">
      <RefreshCw :size="16" />
      重新加载
    </button>
  </div>
</template>
