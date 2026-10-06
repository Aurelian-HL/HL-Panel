<script setup lang="ts">
import { ChevronRight, Info, Server } from '@lucide/vue'
import { onMounted } from 'vue'

import StatePanel from '@/components/StatePanel.vue'
import { customerSite } from '@/stores/site'

function load(force = false): Promise<unknown> { return customerSite.load(force) }
onMounted(() => { void load().catch(() => undefined) })
</script>

<template>
  <div class="page-stack home-page">
    <StatePanel v-if="customerSite.loading.value && !customerSite.info.value" state="loading" title="正在读取站点信息" />
    <StatePanel v-else-if="customerSite.error.value && !customerSite.info.value" state="error" title="站点信息加载失败" :message="customerSite.error.value" @retry="load(true)" />
    <template v-else-if="customerSite.info.value">
      <section class="home-panel">
        <header class="welcome-heading"><h1>欢迎使用</h1><p>{{ customerSite.info.value.site_name }} 面板版本: {{ customerSite.info.value.panel_version }}</p></header>
        <div class="announcement-section">
          <h2>站点公告</h2>
          <div class="announcement-content"><strong>{{ customerSite.info.value.announcement.title || '欢迎使用网络服务' }}</strong><p>{{ customerSite.info.value.announcement.content || '暂无公告' }}</p></div>
        </div>
      </section>
      <section class="info-accordions">
        <details><summary><Info :size="16" />站点信息<ChevronRight :size="15" /></summary><dl><div v-for="item in customerSite.info.value.site_info" :key="item.label"><dt>{{ item.label }}</dt><dd>{{ item.value }}</dd></div><div v-if="!customerSite.info.value.site_info.length"><dt>状态</dt><dd>正常</dd></div></dl></details>
        <details><summary><Server :size="16" />后端信息<ChevronRight :size="15" /></summary><dl><div v-for="item in customerSite.info.value.backend_info" :key="item.label"><dt>{{ item.label }}</dt><dd>{{ item.value }}</dd></div><div v-if="!customerSite.info.value.backend_info.length"><dt>服务</dt><dd>运行中</dd></div></dl></details>
      </section>
    </template>
  </div>
</template>
