<script setup lang="ts">
import { AlertTriangle, Inbox, LoaderCircle, RefreshCw } from '@lucide/vue'

withDefaults(defineProps<{ state: 'loading' | 'error' | 'empty'; title?: string; message?: string }>(), { title: '', message: '' })
defineEmits<{ retry: [] }>()
</script>

<template>
  <section class="state-panel" role="status">
    <LoaderCircle v-if="state === 'loading'" class="spin" :size="26" />
    <AlertTriangle v-else-if="state === 'error'" :size="26" />
    <Inbox v-else :size="26" />
    <h2>{{ title || (state === 'loading' ? '正在加载' : state === 'error' ? '加载失败' : '暂无数据') }}</h2>
    <p v-if="message">{{ message }}</p>
    <button v-if="state === 'error'" class="button button-secondary" @click="$emit('retry')"><RefreshCw :size="15" />重新加载</button>
  </section>
</template>
