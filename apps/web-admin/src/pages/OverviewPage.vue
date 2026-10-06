<script setup lang="ts">
import { onMounted, ref } from 'vue'
import { RefreshCw } from '@lucide/vue'

import { api, type OverviewResponse } from '@/api'
import StatePanel from '@/components/StatePanel.vue'
import { displayError } from '@/lib/displayFormatters'
import { siteStore } from '@/stores/site'

const overview = ref<OverviewResponse | null>(null)
const loading = ref(true)
const errorMessage = ref('')

async function load(): Promise<void> {
  loading.value = true
  errorMessage.value = ''
  try {
    const [runtime] = await Promise.all([api.getOverview(), siteStore.load(true).catch(() => null)])
    overview.value = runtime
  } catch (error) {
    errorMessage.value = displayError(error)
  } finally {
    loading.value = false
  }
}

onMounted(load)
</script>

<template>
  <div class="page-stack">
    <header class="page-heading">
      <div>
        <h2>运行概览</h2>
        <p>节点在线状态与配置应用进度</p>
      </div>
      <button class="button button--secondary" type="button" :disabled="loading" @click="load">
        <RefreshCw :class="{ spin: loading }" :size="16" />
        刷新
      </button>
    </header>

    <StatePanel v-if="loading && !overview" state="loading" title="正在读取运行状态" />
    <StatePanel v-else-if="errorMessage && !overview" state="error" title="总览加载失败" :message="errorMessage" @retry="load" />

    <template v-else-if="overview">
      <section v-if="siteStore.info.value?.announcements.length" class="site-announcements" aria-label="站点公告">
        <h3>站点公告</h3>
        <article v-for="announcement in siteStore.info.value.announcements" :key="announcement.id" :class="`site-announcement site-announcement--${announcement.level}`">
          <strong>{{ announcement.title }}</strong><p>{{ announcement.content }}</p>
        </article>
      </section>

      <p v-else class="overview-empty">当前没有站点公告。</p>
    </template>
  </div>
</template>
