<script setup lang="ts">
import { onMounted, watch } from 'vue'
import { ArrowLeft, Activity } from '@lucide/vue'

import { siteStore } from '@/stores/site'

onMounted(() => { void siteStore.load().catch(() => undefined) })
watch(() => siteStore.panelTitle.value, (title) => { document.title = `设备探针 - ${title}` }, { immediate: true })
</script>

<template>
  <div class="probe-workspace">
    <header class="probe-workspace__header">
      <RouterLink class="probe-workspace__back" to="/overview" title="返回面板"><ArrowLeft :size="18" /><span>返回面板</span></RouterLink>
      <div class="probe-workspace__identity"><Activity :size="18" /><strong>设备探针</strong><span>{{ siteStore.siteName.value }}</span></div>
    </header>
    <main class="probe-workspace__content"><RouterView /></main>
  </div>
</template>
