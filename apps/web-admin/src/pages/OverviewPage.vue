<script setup lang="ts">
import { onMounted, ref } from 'vue'
import { Activity, CircleAlert, CircleCheck, Cpu, HardDrive, RefreshCw, Server, Wifi } from '@lucide/vue'

import { api, type EdgeNode, type OverviewResponse } from '@/api'
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

function statusLabel(status: EdgeNode['status']): string {
  return { online: '在线', syncing: '同步中', failed: '配置失败', offline: '离线', retired: '已退役', unknown: '未采集' }[status]
}

function statusClass(status: EdgeNode['status']): string { return `node-status node-status--${status}` }

function resource(node: EdgeNode): Record<string, unknown> {
  const nested = node.resources.host
  if (nested && typeof nested === 'object' && !Array.isArray(nested)) return nested as Record<string, unknown>
  return node.resources
}

function number(node: EdgeNode, key: string): number | null {
  const value = resource(node)[key]
  return typeof value === 'number' && Number.isFinite(value) ? value : null
}

function percent(node: EdgeNode, key: string, usedKey?: string, totalKey?: string): string {
  const direct = number(node, key)
  if (direct !== null) return `${Math.round(direct)}%`
  const used = usedKey ? number(node, usedKey) : null
  const total = totalKey ? number(node, totalKey) : null
  return used !== null && total !== null && total > 0 ? `${Math.round((used / total) * 100)}%` : '未采集'
}

function bytesPerSecond(value: number | null): string {
  if (value === null) return '未采集'
  if (value >= 1024 * 1024) return `${(value / 1024 / 1024).toFixed(1)} MB/s`
  if (value >= 1024) return `${(value / 1024).toFixed(1)} KB/s`
  return `${Math.round(value)} B/s`
}

function count(value: number | null): string { return value === null ? '未采集' : String(Math.round(value)) }

function uptime(node: EdgeNode): string {
  const seconds = number(node, 'uptime_seconds')
  if (seconds === null) return '未采集'
  const days = Math.floor(seconds / 86400)
  const hours = Math.floor((seconds % 86400) / 3600)
  return days ? `${days}天 ${hours}小时` : `${hours}小时`
}

function heartbeat(node: EdgeNode): string {
  return node.last_heartbeat_at ? new Date(node.last_heartbeat_at).toLocaleString('zh-CN', { hour12: false }) : '未采集'
}

function engines(node: EdgeNode): string {
  const entries = Object.entries(node.engine_versions)
  return entries.length ? entries.map(([name, version]) => `${name} ${version}`).join(' / ') : '未采集'
}

onMounted(load)
</script>

<template>
  <div class="page-stack overview-page">
    <header class="page-heading">
      <div><h2>系统状态</h2><p>节点、探针和转发引擎的实时运行概况</p></div>
      <button class="button button--secondary" type="button" :disabled="loading" @click="load"><RefreshCw :class="{ spin: loading }" :size="16" />刷新</button>
    </header>
    <StatePanel v-if="loading && !overview" state="loading" title="正在读取运行状态" />
    <StatePanel v-else-if="errorMessage && !overview" state="error" title="总览加载失败" :message="errorMessage" @retry="load" />
    <template v-else-if="overview">
      <section class="metric-grid overview-metrics" aria-label="系统统计">
        <article class="metric-card metric-card--blue"><Server :size="20" /><div><span>节点总数</span><strong>{{ overview.node_count }}</strong></div></article>
        <article class="metric-card metric-card--green"><CircleCheck :size="20" /><div><span>在线节点</span><strong>{{ overview.online_node_count }}</strong></div></article>
        <article class="metric-card metric-card--gold"><Activity :size="20" /><div><span>同步中</span><strong>{{ overview.syncing_node_count }}</strong></div></article>
        <article class="metric-card metric-card--red"><CircleAlert :size="20" /><div><span>配置失败</span><strong>{{ overview.failed_apply_count }}</strong></div></article>
        <article class="metric-card metric-card--blue"><HardDrive :size="20" /><div><span>设备组</span><strong>{{ overview.group_count }}</strong></div></article>
      </section>
      <section class="overview-section">
        <header class="overview-section__heading"><div><h3>节点系统状态</h3><p>资源数据由节点 Agent 心跳采集，无数据时显示“未采集”。</p></div><span class="overview-section__count">{{ overview.nodes.length }} 台</span></header>
        <div v-if="!overview.nodes.length" class="overview-empty">当前没有已注册节点。</div>
        <div v-else class="system-node-grid">
          <article v-for="node in overview.nodes" :key="node.id" class="system-node-card">
            <header class="system-node-card__header">
              <div class="system-node-card__identity"><span class="system-node-card__icon"><Server :size="18" /></span><div><h4>{{ node.name || node.hostname || node.id }}</h4><p>{{ node.hostname || node.id }} · {{ node.platform || '未知平台' }} {{ node.architecture }}</p></div></div>
              <span :class="statusClass(node.status)">{{ statusLabel(node.status) }}</span>
            </header>
            <div class="system-node-card__resources">
              <div><span><Cpu :size="14" /> CPU</span><strong>{{ percent(node, 'cpu_percent') }}</strong></div>
              <div><span><Activity :size="14" /> 内存</span><strong>{{ percent(node, 'memory_percent', 'memory_used_bytes', 'memory_total_bytes') }}</strong></div>
              <div><span><HardDrive :size="14" /> 磁盘</span><strong>{{ percent(node, 'disk_percent', 'disk_used_bytes', 'disk_total_bytes') }}</strong></div>
              <div><span><Wifi :size="14" /> TCP / UDP</span><strong>{{ count(number(node, 'tcp_conn_count')) }} / {{ count(number(node, 'udp_conn_count')) }}</strong></div>
            </div>
            <dl class="system-node-card__details">
              <div><dt>运行时间</dt><dd>{{ uptime(node) }}</dd></div>
              <div><dt>下行 / 上行</dt><dd>{{ bytesPerSecond(number(node, 'net_in_speed_bytes_per_second')) }} / {{ bytesPerSecond(number(node, 'net_out_speed_bytes_per_second')) }}</dd></div>
              <div><dt>公网地址</dt><dd>{{ (resource(node).ipv4 as string) || '未采集' }}<span v-if="resource(node).ipv6"> / {{ resource(node).ipv6 }}</span></dd></div>
              <div><dt>Agent / 引擎</dt><dd>{{ node.agent_version || '未采集' }} · {{ engines(node) }}</dd></div>
              <div><dt>最近心跳</dt><dd>{{ heartbeat(node) }}</dd></div>
            </dl>
          </article>
        </div>
      </section>
      <section v-if="siteStore.info.value?.announcements.length" class="site-announcements" aria-label="站点公告">
        <h3>站点公告</h3>
        <article v-for="announcement in siteStore.info.value.announcements" :key="announcement.id" :class="`site-announcement site-announcement--${announcement.level}`"><strong>{{ announcement.title }}</strong><p>{{ announcement.content }}</p></article>
      </section>
    </template>
  </div>
</template>
