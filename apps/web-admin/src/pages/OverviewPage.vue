<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref } from 'vue'
import { Archive, Cpu, Database, FileText, HardDrive, MemoryStick, Network, Power, RefreshCw, RotateCw, Terminal, Wifi, XCircle, CheckCircle2 } from '@lucide/vue'

import { api, type EdgeNode, type PanelControlResult, type OverviewResponse } from '@/api'
import StatePanel from '@/components/StatePanel.vue'
import { displayError } from '@/lib/displayFormatters'
import { siteStore } from '@/stores/site'

const overview = ref<OverviewResponse | null>(null)
const loading = ref(true)
const errorMessage = ref('')
const controlBusy = ref(false)
const controlResult = ref<PanelControlResult | null>(null)
const refreshTimer = ref<number | undefined>()

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

function resource(node: EdgeNode): Record<string, unknown> {
  const nested = node.resources.host
  return nested && typeof nested === 'object' && !Array.isArray(nested) ? nested as Record<string, unknown> : node.resources
}

function number(node: EdgeNode | null, key: string): number | null {
  if (!node) return null
  const nested = resource(node)
  const value = nested[key] ?? node.resources[key]
  return typeof value === 'number' && Number.isFinite(value) ? value : null
}

function sum(key: string): number | null {
  const values = (overview.value?.nodes ?? []).map((node) => number(node, key)).filter((value): value is number => value !== null)
  return values.length ? values.reduce((total, value) => total + value, 0) : null
}

const nodeTotalTraffic = computed(() => {
  const inbound = sum('net_in_transfer_bytes')
  const outbound = sum('net_out_transfer_bytes')
  return inbound === null && outbound === null ? null : (inbound ?? 0) + (outbound ?? 0)
})

function panelResource(): Record<string, unknown> {
  const value = overview.value?.panel?.resources
  return value && typeof value === 'object' && !Array.isArray(value) ? value : {}
}

function panelNumber(key: string): number | null {
  const value = panelResource()[key]
  return typeof value === 'number' && Number.isFinite(value) ? value : null
}

function panelText(key: string): string {
  const value = panelResource()[key]
  return typeof value === 'string' && value.length > 0 ? value : '未采集'
}

function formatBytes(value: number | null): string {
  if (value === null) return '未采集'
  if (value >= 1024 ** 4) return `${(value / 1024 ** 4).toFixed(2)} TB`
  if (value >= 1024 ** 3) return `${(value / 1024 ** 3).toFixed(2)} GB`
  if (value >= 1024 ** 2) return `${(value / 1024 ** 2).toFixed(1)} MB`
  if (value >= 1024) return `${(value / 1024).toFixed(1)} KB`
  return `${Math.round(value)} B`
}

function formatSpeed(value: number | null): string {
  return value === null ? '未采集' : `${formatBytes(value)}/s`
}

function formatPercent(value: number | null): string {
  if (value === null) return '未采集'
  const normalized = Math.max(0, Math.round(value * 100) / 100)
  return `${Number.isInteger(normalized) ? normalized : normalized.toFixed(2)}%`
}

function percent(used: number | null, total: number | null): number | null {
  return used === null || total === null || total <= 0 ? null : Math.min(100, Math.max(0, used / total * 100))
}

function gaugeStyle(value: number | null): string {
  const degrees = value === null ? 0 : Math.min(270, Math.max(0, value) * 2.7)
  return `background: conic-gradient(from 135deg, #168b76 0deg ${degrees}deg, #e8edf2 ${degrees}deg 270deg, transparent 270deg 360deg)`
}

function formatUptime(seconds: number | null): string {
  if (seconds === null) return '未采集'
  const days = Math.floor(seconds / 86400)
  const hours = Math.floor((seconds % 86400) / 3600)
  const minutes = Math.floor((seconds % 3600) / 60)
  if (days > 0) return `${days}天 ${hours}小时`
  if (hours > 0) return `${hours}小时 ${minutes}分钟`
  return `${minutes}分钟`
}

function loadLabel(): string {
  const values = [1, 5, 15].map((minutes) => panelNumber(`load_average_${minutes}`))
  return values.every((value) => value !== null) ? values.map((value) => (value as number).toFixed(2)).join(' / ') : '未采集'
}

function panelStatusLabel(status: string | undefined): string {
  return { online: '面板在线', unavailable: '面板不可用' }[status ?? ''] ?? '未采集'
}

function controlStatusLabel(status: string): string { return { pending: '处理中', succeeded: '已完成', failed: '失败' }[status] ?? status }

async function runControl(command: PanelControlResult['command']): Promise<void> {
  controlBusy.value = true
  try {
    const requested = await api.controlPanel(command)
    controlResult.value = requested
    for (let attempt = 0; attempt < 12 && requested.status === 'pending'; attempt += 1) {
      await new Promise((resolve) => window.setTimeout(resolve, 1000))
      const current = await api.getPanelControl()
      if (current) controlResult.value = current
      if (!current || current.status !== 'pending') break
    }
  } catch (error) {
    controlResult.value = { command_id: '', command, status: 'failed', message: displayError(error), logs: '', updated_at: new Date().toISOString() }
  } finally {
    controlBusy.value = false
    await load()
  }
}

const panelRuntime = computed(() => overview.value?.panel ?? null)
const systemCpu = computed(() => panelNumber('cpu_percent'))
const systemMemory = computed(() => percent(panelNumber('memory_used_bytes'), panelNumber('memory_total_bytes')))
const systemSwap = computed(() => panelNumber('swap_total_bytes') === 0 ? 0 : percent(panelNumber('swap_used_bytes'), panelNumber('swap_total_bytes')))
const systemDisk = computed(() => percent(panelNumber('disk_used_bytes'), panelNumber('disk_total_bytes')))
const connectionCount = computed(() => `${sum('tcp_conn_count') === null ? '未采集' : Math.round(sum('tcp_conn_count') as number).toLocaleString('zh-CN')} / ${sum('udp_conn_count') === null ? '未采集' : Math.round(sum('udp_conn_count') as number).toLocaleString('zh-CN')}`)
const latestLog = computed(() => {
  if (controlResult.value?.logs || controlResult.value?.message) return controlResult.value
  return panelRuntime.value?.log ? { status: 'succeeded', message: panelRuntime.value.log, logs: '' } : null
})

onMounted(() => {
  void load()
  refreshTimer.value = window.setInterval(() => { if (!loading.value) void load() }, 15000)
})

onBeforeUnmount(() => {
  if (refreshTimer.value !== undefined) window.clearInterval(refreshTimer.value)
})
</script>

<template>
  <div class="page-stack overview-page xpanel-page">
    <StatePanel v-if="loading && !overview" state="loading" title="正在读取系统状态" />
    <StatePanel v-else-if="errorMessage && !overview" state="error" title="系统状态读取失败" :message="errorMessage" @retry="load" />
    <template v-else-if="overview">
      <div class="xpanel-layout">
        <main class="xpanel-main">
          <header class="xpanel-header">
            <div class="xpanel-brand"><span>HL-PANEL</span><h1>HL-Panel</h1><p>系统运行面板</p></div>
            <button class="xpanel-refresh" type="button" :disabled="loading" aria-label="刷新状态" title="刷新状态" @click="load"><RefreshCw :class="{ spin: loading }" :size="15" /></button>
          </header>

          <section class="xpanel-section xpanel-system" aria-label="系统状态">
            <div class="xpanel-section__title"><h2>系统状态</h2><span>面板机</span></div>
            <div class="xpanel-gauges">
              <article class="xpanel-gauge"><div class="gauge-ring" :style="gaugeStyle(systemCpu)"><b>{{ formatPercent(systemCpu) }}</b></div><div><strong><Cpu :size="13" /> CPU</strong><small>面板机资源</small></div></article>
              <article class="xpanel-gauge"><div class="gauge-ring" :style="gaugeStyle(systemMemory)"><b>{{ formatPercent(systemMemory) }}</b></div><div><strong><MemoryStick :size="13" /> 内存</strong><small>{{ formatBytes(panelNumber('memory_used_bytes')) }} / {{ formatBytes(panelNumber('memory_total_bytes')) }}</small></div></article>
              <article class="xpanel-gauge"><div class="gauge-ring" :style="gaugeStyle(systemSwap)"><b>{{ panelNumber('swap_total_bytes') === 0 ? '未启用' : formatPercent(systemSwap) }}</b></div><div><strong><Database :size="13" /> 交换分区</strong><small>{{ formatBytes(panelNumber('swap_used_bytes')) }} / {{ formatBytes(panelNumber('swap_total_bytes')) }}</small></div></article>
              <article class="xpanel-gauge"><div class="gauge-ring" :style="gaugeStyle(systemDisk)"><b>{{ formatPercent(systemDisk) }}</b></div><div><strong><HardDrive :size="13" /> 存储</strong><small>{{ formatBytes(panelNumber('disk_used_bytes')) }} / {{ formatBytes(panelNumber('disk_total_bytes')) }}</small></div></article>
            </div>
          </section>

          <section class="xpanel-section xpanel-runtime" aria-label="运行状态">
            <div class="xpanel-section__title"><h2>HL-Panel 运行状态</h2><span :class="`xpanel-running xpanel-running--${panelRuntime?.status || 'unknown'}`"><i />{{ panelStatusLabel(panelRuntime?.status) }}</span></div>
            <div v-if="panelRuntime" class="xpanel-runtime__body">
              <div class="xpanel-version">{{ panelRuntime.version || '版本未采集' }}</div>
              <div class="xpanel-controls">
                <button type="button" :disabled="controlBusy" @click="runControl('logs')"><FileText :size="14" />日志</button>
                <button type="button" :disabled="controlBusy" @click="runControl('stop')"><Power :size="14" />停止</button>
                <button type="button" :disabled="controlBusy" @click="runControl('restart')"><RotateCw :size="14" />重启</button>
                <button type="button" :disabled="controlBusy" @click="runControl('version')"><Terminal :size="14" />版本</button>
              </div>
              <div v-if="controlResult" class="xpanel-result" :class="`xpanel-result--${controlResult.status}`"><CheckCircle2 v-if="controlResult.status === 'succeeded'" :size="14" /><XCircle v-else-if="controlResult.status === 'failed'" :size="14" /><RefreshCw v-else :size="14" class="spin" />{{ controlStatusLabel(controlResult.status) }}：{{ controlResult.message || '等待面板返回' }}</div>
            </div>
            <div v-else class="xpanel-empty">面板运行状态暂未采集。</div>
          </section>

          <section class="xpanel-section xpanel-logs" aria-label="日志">
            <div class="xpanel-section__title"><h2>日志</h2><span>最近一次面板运行记录</span></div>
            <div v-if="latestLog" class="xpanel-log"><strong>HL-Panel</strong><span>{{ controlStatusLabel(latestLog.status || '') }}</span><p>{{ latestLog.message || '暂无面板运行记录' }}</p><pre v-if="latestLog.logs">{{ latestLog.logs }}</pre></div>
            <div v-else class="xpanel-empty">暂无面板运行日志</div>
          </section>

          <div class="xpanel-bottom-grid">
            <section class="xpanel-section xpanel-bottom-card"><h2>总数据</h2><div class="xpanel-dual"><div><span>已发送（面板机）</span><strong><Archive :size="14" />{{ formatBytes(panelNumber('net_out_transfer_bytes')) }}</strong></div><div><span>已接收（面板机）</span><strong><Archive :size="14" />{{ formatBytes(panelNumber('net_in_transfer_bytes')) }}</strong></div></div></section>
            <section class="xpanel-section xpanel-bottom-card"><h2>连接数</h2><div class="xpanel-dual"><div><span>TCP（节点合计）</span><strong><Network :size="14" />{{ sum('tcp_conn_count') === null ? '未采集' : Math.round(sum('tcp_conn_count') as number).toLocaleString('zh-CN') }}</strong></div><div><span>UDP（节点合计）</span><strong><Network :size="14" />{{ sum('udp_conn_count') === null ? '未采集' : Math.round(sum('udp_conn_count') as number).toLocaleString('zh-CN') }}</strong></div></div></section>
            <section class="xpanel-section xpanel-bottom-card"><h2>总流量</h2><div class="xpanel-dual"><div><span>所有节点收发合计</span><strong><Archive :size="14" />{{ formatBytes(nodeTotalTraffic) }}</strong></div><div><span>统计范围</span><strong><Network :size="14" />{{ overview.node_count }} 台节点</strong></div></div></section>
          </div>
        </main>

        <aside class="xpanel-side">
          <section class="xpanel-side-card"><h3>[HL-Panel 面板]</h3><div class="xpanel-tags"><span>{{ siteStore.info.value?.platform_version || '版本未采集' }}</span></div></section>
          <section class="xpanel-side-card"><h3>系统正常运行时间</h3><div class="xpanel-tags"><span>面板: {{ panelStatusLabel(panelRuntime?.status) }}</span><span>OS: {{ formatUptime(panelNumber('uptime_seconds')) }}</span></div></section>
          <section class="xpanel-side-card"><h3>系统负载</h3><div class="xpanel-tags"><span>{{ loadLabel() }}</span><span>{{ panelRuntime?.status === 'online' ? '面板正常' : '面板不可用' }}</span></div></section>
          <section class="xpanel-side-card"><h3>使用情况</h3><div class="xpanel-tags"><span>面板内存: {{ formatBytes(panelNumber('memory_used_bytes')) }}</span><span>节点连接: {{ connectionCount }}</span><span>节点总流量: {{ formatBytes(nodeTotalTraffic) }}</span></div></section>
          <section class="xpanel-side-card"><h3>整体速度</h3><div class="xpanel-speed"><div><span>上传（节点合计）</span><strong>↑ {{ formatSpeed(sum('net_out_speed_bytes_per_second')) }}</strong></div><div><span>下载（节点合计）</span><strong>↓ {{ formatSpeed(sum('net_in_speed_bytes_per_second')) }}</strong></div></div></section>
          <section class="xpanel-side-card"><h3>IP 地址 <Wifi :size="14" /></h3><div class="xpanel-ip"><div><span>面板 IPv4</span><strong>{{ panelText('ipv4') }}</strong></div><div><span>面板 IPv6</span><strong>{{ panelText('ipv6') }}</strong></div></div></section>
        </aside>
      </div>
    </template>
  </div>
</template>
