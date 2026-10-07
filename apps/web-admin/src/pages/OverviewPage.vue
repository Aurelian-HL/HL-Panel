<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { Archive, Cpu, Database, FileText, HardDrive, MemoryStick, Network, Power, RefreshCw, RotateCw, Terminal, Wifi, XCircle, CheckCircle2 } from '@lucide/vue'

import { api, type EdgeNode, type NodeControlResult, type OverviewResponse } from '@/api'
import StatePanel from '@/components/StatePanel.vue'
import { displayError } from '@/lib/displayFormatters'
import { siteStore } from '@/stores/site'

const overview = ref<OverviewResponse | null>(null)
const loading = ref(true)
const errorMessage = ref('')
const selectedNodeId = ref('')
const controlBusy = ref<Record<string, boolean>>({})
const controlResults = ref<Record<string, NodeControlResult | null>>({})
const refreshTimer = ref<number | undefined>()

async function load(): Promise<void> {
  loading.value = true
  errorMessage.value = ''
  try {
    const [runtime] = await Promise.all([api.getOverview(), siteStore.load(true).catch(() => null)])
    overview.value = runtime
    if (!selectedNodeId.value || !runtime.nodes.some((node) => node.id === selectedNodeId.value)) {
      selectedNodeId.value = runtime.nodes.find((node) => node.status === 'online')?.id ?? runtime.nodes[0]?.id ?? ''
    }
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

function sum(key: string, onlyOnline = false): number | null {
  const values = (overview.value?.nodes ?? []).filter((node) => !onlyOnline || node.status === 'online').map((node) => number(node, key)).filter((value): value is number => value !== null)
  return values.length ? values.reduce((total, value) => total + value, 0) : null
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

function text(node: EdgeNode | null, key: string): string {
  if (!node) return '未采集'
  const nested = resource(node)
  const value = nested[key] ?? node.resources[key]
  return typeof value === 'string' && value.length > 0 ? value : '未采集'
}

function engineLabel(node: EdgeNode): string {
  const engines = Object.entries(node.engine_versions)
  if (engines.length) return engines.map(([name, version]) => `${engineTitle(name)} ${version.startsWith('v') ? version : `v${version}`}`).join(' / ')
  if (node.capabilities.length) return node.capabilities.join(' / ')
  return '转发引擎版本未采集'
}

function engineTitle(name: string): string {
  const normalized = name.trim().toLowerCase()
  if (normalized === 'xray') return 'Xray'
  if (normalized === 'gost') return 'gost'
  return normalized ? normalized.charAt(0).toUpperCase() + normalized.slice(1) : '引擎'
}

function engineName(node: EdgeNode): string {
  const names = Object.keys(node.engine_versions)
  if (names.length) return names.map(engineTitle).join(' / ')
  if (node.capabilities.length) return node.capabilities.join(' / ')
  return '转发引擎'
}

function loadLabel(node: EdgeNode | null): string {
  if (!node) return '未采集'
  const values = [1, 5, 15].map((minutes) => number(node, `load_average_${minutes}`))
  return values.every((value) => value !== null) ? values.map((value) => (value as number).toFixed(2)).join(' / ') : '未采集'
}

function nodeStatusLabel(status: EdgeNode['status']): string {
  return { online: 'Agent 在线', syncing: '同步中', failed: '配置失败', offline: '节点离线', retired: '已退役', unknown: '未采集' }[status]
}

function nodeControlResult(node: EdgeNode): NodeControlResult | null {
  return controlResults.value[node.id] ?? (node.control_command_id && node.control_command && node.control_command_status
    ? { node_id: node.id, command_id: node.control_command_id, command: node.control_command as NodeControlResult['command'], status: node.control_command_status, message: node.control_command_message ?? '', logs: node.control_command_logs ?? '', updated_at: node.control_command_updated_at ?? '' }
    : null)
}

function controlStatusLabel(status: string): string { return { pending: '处理中', succeeded: '已完成', failed: '失败' }[status] ?? status }

async function runControl(node: EdgeNode, command: NodeControlResult['command']): Promise<void> {
  controlBusy.value = { ...controlBusy.value, [node.id]: true }
  try {
    const requested = await api.controlNode(node.id, command)
    controlResults.value = { ...controlResults.value, [node.id]: requested }
    for (let attempt = 0; attempt < 12 && requested.status === 'pending'; attempt += 1) {
      await new Promise((resolve) => window.setTimeout(resolve, 1000))
      const current = await api.getNodeControl(node.id)
      if (current) controlResults.value = { ...controlResults.value, [node.id]: current }
      if (!current || current.status !== 'pending') break
    }
  } catch (error) {
    controlResults.value = { ...controlResults.value, [node.id]: { node_id: node.id, command_id: '', command, status: 'failed', message: displayError(error), logs: '', updated_at: new Date().toISOString() } }
  } finally {
    controlBusy.value = { ...controlBusy.value, [node.id]: false }
    await load()
  }
}

const selectedNode = computed(() => overview.value?.nodes.find((node) => node.id === selectedNodeId.value) ?? overview.value?.nodes[0] ?? null)
const systemCpu = computed(() => number(selectedNode.value, 'cpu_percent'))
const systemMemory = computed(() => percent(number(selectedNode.value, 'memory_used_bytes'), number(selectedNode.value, 'memory_total_bytes')))
const systemSwap = computed(() => percent(number(selectedNode.value, 'swap_used_bytes'), number(selectedNode.value, 'swap_total_bytes')))
const systemDisk = computed(() => percent(number(selectedNode.value, 'disk_used_bytes'), number(selectedNode.value, 'disk_total_bytes')))
const connectionCount = computed(() => `${sum('tcp_conn_count', true) === null ? '未采集' : Math.round(sum('tcp_conn_count', true) as number).toLocaleString('zh-CN')} / ${sum('udp_conn_count', true) === null ? '未采集' : Math.round(sum('udp_conn_count', true) as number).toLocaleString('zh-CN')}`)
const latestLog = computed(() => {
  for (const node of overview.value?.nodes ?? []) {
    const result = nodeControlResult(node)
    if (result?.logs || result?.message) return { node, result }
    if (node.last_apply_message) return { node, result: { message: node.last_apply_message, logs: '', status: node.last_apply_status || 'succeeded' } }
  }
  return null
})
watch(() => overview.value?.nodes.length, () => {
  if (!selectedNodeId.value && overview.value?.nodes[0]) selectedNodeId.value = overview.value.nodes[0].id
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
            <div class="xpanel-section__title"><h2>系统状态</h2><span>{{ selectedNode?.name || '未选择节点' }}</span></div>
            <div v-if="overview.nodes.length" class="xpanel-node-picker"><button v-for="node in overview.nodes" :key="node.id" type="button" :class="{ active: node.id === selectedNode?.id }" @click="selectedNodeId = node.id"><i :class="`dot dot--${node.status}`" />{{ node.name || node.hostname || node.id }}</button></div>
            <div class="xpanel-gauges">
              <article class="xpanel-gauge"><div class="gauge-ring" :style="gaugeStyle(systemCpu)"><b>{{ formatPercent(systemCpu) }}</b></div><div><strong><Cpu :size="13" /> CPU</strong><small>{{ selectedNode ? `${number(selectedNode, 'logical_cpus') ?? '未采集'} Cores` : '未采集' }}</small></div></article>
              <article class="xpanel-gauge"><div class="gauge-ring" :style="gaugeStyle(systemMemory)"><b>{{ formatPercent(systemMemory) }}</b></div><div><strong><MemoryStick :size="13" /> 内存</strong><small>{{ formatBytes(number(selectedNode, 'memory_used_bytes')) }} / {{ formatBytes(number(selectedNode, 'memory_total_bytes')) }}</small></div></article>
              <article class="xpanel-gauge"><div class="gauge-ring" :style="gaugeStyle(systemSwap)"><b>{{ formatPercent(systemSwap) }}</b></div><div><strong><Database :size="13" /> 交换分区</strong><small>{{ formatBytes(number(selectedNode, 'swap_used_bytes')) }} / {{ formatBytes(number(selectedNode, 'swap_total_bytes')) }}</small></div></article>
              <article class="xpanel-gauge"><div class="gauge-ring" :style="gaugeStyle(systemDisk)"><b>{{ formatPercent(systemDisk) }}</b></div><div><strong><HardDrive :size="13" /> 存储</strong><small>{{ formatBytes(number(selectedNode, 'disk_used_bytes')) }} / {{ formatBytes(number(selectedNode, 'disk_total_bytes')) }}</small></div></article>
            </div>
          </section>

          <section class="xpanel-section xpanel-runtime" aria-label="运行状态">
            <div class="xpanel-section__title"><h2>{{ selectedNode ? `${engineName(selectedNode)} 运行状态` : '运行状态' }}</h2><span :class="`xpanel-running xpanel-running--${selectedNode?.status || 'unknown'}`"><i />{{ selectedNode ? nodeStatusLabel(selectedNode.status) : '未采集' }}</span></div>
            <div v-if="selectedNode" class="xpanel-runtime__body">
              <div class="xpanel-version">{{ engineLabel(selectedNode) }}</div>
              <div class="xpanel-controls">
                <button type="button" :disabled="controlBusy[selectedNode.id]" @click="runControl(selectedNode, 'logs')"><FileText :size="14" />日志</button>
                <button type="button" :disabled="controlBusy[selectedNode.id]" @click="runControl(selectedNode, 'stop')"><Power :size="14" />停止</button>
                <button type="button" :disabled="controlBusy[selectedNode.id]" @click="runControl(selectedNode, 'restart')"><RotateCw :size="14" />重启</button>
                <button type="button" :disabled="controlBusy[selectedNode.id]" @click="runControl(selectedNode, 'version')"><Terminal :size="14" />版本</button>
              </div>
              <div v-if="nodeControlResult(selectedNode)" class="xpanel-result" :class="`xpanel-result--${nodeControlResult(selectedNode)?.status}`"><CheckCircle2 v-if="nodeControlResult(selectedNode)?.status === 'succeeded'" :size="14" /><XCircle v-else-if="nodeControlResult(selectedNode)?.status === 'failed'" :size="14" /><RefreshCw v-else :size="14" class="spin" />{{ controlStatusLabel(nodeControlResult(selectedNode)?.status || '') }}：{{ nodeControlResult(selectedNode)?.message || '等待节点返回' }}</div>
            </div>
            <div v-else class="xpanel-empty">当前没有已注册节点，请先创建安装令牌并安装节点端。</div>
          </section>

          <section class="xpanel-section xpanel-logs" aria-label="日志">
            <div class="xpanel-section__title"><h2>日志</h2><span>最近一次节点应用记录</span></div>
            <div v-if="latestLog" class="xpanel-log"><strong>{{ latestLog.node.name || latestLog.node.hostname }}</strong><span>{{ controlStatusLabel(latestLog.result.status || '') }}</span><p>{{ latestLog.result.message || '暂无配置应用记录' }}</p><pre v-if="latestLog.result.logs">{{ latestLog.result.logs }}</pre></div>
            <div v-else class="xpanel-empty">暂无运行日志</div>
          </section>

          <div class="xpanel-bottom-grid">
            <section class="xpanel-section xpanel-bottom-card"><h2>总数据</h2><div class="xpanel-dual"><div><span>已发送</span><strong><Archive :size="14" />{{ formatBytes(sum('net_out_transfer_bytes')) }}</strong></div><div><span>已接收</span><strong><Archive :size="14" />{{ formatBytes(sum('net_in_transfer_bytes')) }}</strong></div></div></section>
            <section class="xpanel-section xpanel-bottom-card"><h2>连接数</h2><div class="xpanel-dual"><div><span>TCP</span><strong><Network :size="14" />{{ sum('tcp_conn_count', true) === null ? '未采集' : Math.round(sum('tcp_conn_count', true) as number).toLocaleString('zh-CN') }}</strong></div><div><span>UDP</span><strong><Network :size="14" />{{ sum('udp_conn_count', true) === null ? '未采集' : Math.round(sum('udp_conn_count', true) as number).toLocaleString('zh-CN') }}</strong></div></div></section>
          </div>
        </main>

        <aside class="xpanel-side">
          <section class="xpanel-side-card"><h3>[HL-Panel 面板]</h3><div class="xpanel-tags"><span>{{ siteStore.info.value?.platform_version || '版本未采集' }}</span><span>节点 {{ overview.online_node_count }}/{{ overview.node_count }}</span><span>设备组 {{ overview.group_count }}</span></div></section>
          <section class="xpanel-side-card"><h3>系统正常运行时间</h3><div class="xpanel-tags"><span>Agent: {{ selectedNode ? nodeStatusLabel(selectedNode.status) : '未采集' }}</span><span>OS: {{ formatUptime(number(selectedNode, 'uptime_seconds')) }}</span></div></section>
          <section class="xpanel-side-card"><h3>系统负载</h3><div class="xpanel-tags"><span>{{ loadLabel(selectedNode) }}</span><span>{{ selectedNode?.status === 'online' ? '网络通畅' : '节点离线' }}</span></div></section>
          <section class="xpanel-side-card"><h3>使用情况</h3><div class="xpanel-tags"><span>内存: {{ formatBytes(number(selectedNode, 'memory_used_bytes')) }}</span><span>连接: {{ connectionCount }}</span></div></section>
          <section class="xpanel-side-card"><h3>整体速度</h3><div class="xpanel-speed"><div><span>上传</span><strong>↑ {{ formatSpeed(sum('net_out_speed_bytes_per_second', true)) }}</strong></div><div><span>下载</span><strong>↓ {{ formatSpeed(sum('net_in_speed_bytes_per_second', true)) }}</strong></div></div></section>
          <section class="xpanel-side-card"><h3>IP 地址 <Wifi :size="14" /></h3><div class="xpanel-ip"><div><span>IPv4</span><strong>{{ text(selectedNode, 'ipv4') !== '未采集' ? text(selectedNode, 'ipv4') : text(selectedNode, 'observed_ip') }}</strong></div><div><span>IPv6</span><strong>{{ text(selectedNode, 'ipv6') }}</strong></div></div></section>
        </aside>
      </div>
    </template>
  </div>
</template>
