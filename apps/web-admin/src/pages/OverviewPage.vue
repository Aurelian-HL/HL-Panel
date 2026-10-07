<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { Activity, ArrowRight, CircleAlert, CircleCheck, Cpu, Database, HardDrive, Network, RefreshCw, Server, ServerCog, Settings, ShieldCheck, Users, Wifi } from '@lucide/vue'

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

const featuredNode = computed(() => overview.value?.nodes.find((node) => node.status === 'online') ?? overview.value?.nodes[0] ?? null)
const onlineRatio = computed(() => {
  if (!overview.value || overview.value.node_count <= 0) return '未采集'
  return `${Math.round((overview.value.online_node_count / overview.value.node_count) * 100)}%`
})
const healthLabel = computed(() => {
  if (!overview.value || overview.value.node_count === 0) return '等待节点接入'
  if (overview.value.failed_apply_count > 0) return '有配置需要处理'
  if (overview.value.syncing_node_count > 0) return '节点正在同步'
  if (overview.value.online_node_count === overview.value.node_count) return '运行正常'
  return '部分节点在线'
})
const healthClass = computed(() => {
  if (!overview.value || overview.value.node_count === 0) return 'is-muted'
  if (overview.value.failed_apply_count > 0) return 'is-danger'
  if (overview.value.syncing_node_count > 0) return 'is-warning'
  if (overview.value.online_node_count === overview.value.node_count) return 'is-success'
  return 'is-warning'
})

const quickActions = [
  { label: '节点管理', description: '查看节点、版本与心跳', to: '/nodes', icon: Server },
  { label: '设备组管理', description: '组织节点与发布配置', to: '/device-groups', icon: ServerCog },
  { label: '转发规则', description: '管理入口、端口与策略', to: '/forward-rules', icon: Network },
  { label: '设备探针', description: '查看成员采样与状态', to: '/probe', icon: Activity },
  { label: '用户管理', description: '管理客户账号与状态', to: '/customers', icon: Users },
  { label: '系统设置', description: '站点信息与公告设置', to: '/system-settings', icon: Settings },
]

onMounted(load)
</script>

<template>
  <div class="page-stack overview-page">
    <header class="overview-hero">
      <div class="overview-hero__copy">
        <span class="overview-eyebrow">{{ siteStore.siteName.value }}</span>
        <h2>系统总览</h2>
        <p>集中查看节点、探针、转发引擎和设备组的实时运行状态。</p>
        <div class="overview-hero__facts">
          <span><strong>{{ overview?.node_count ?? '—' }}</strong> 台节点</span>
          <span><strong>{{ overview?.group_count ?? '—' }}</strong> 个设备组</span>
          <span><strong>{{ onlineRatio }}</strong> 在线率</span>
        </div>
      </div>
      <div class="overview-hero__actions">
        <span class="overview-health" :class="healthClass"><i />{{ healthLabel }}</span>
        <button class="button button--secondary" type="button" :disabled="loading" @click="load"><RefreshCw :class="{ spin: loading }" :size="16" />刷新状态</button>
      </div>
    </header>
    <StatePanel v-if="loading && !overview" state="loading" title="正在读取运行状态" />
    <StatePanel v-else-if="errorMessage && !overview" state="error" title="总览加载失败" :message="errorMessage" @retry="load" />
    <template v-else-if="overview">
      <div class="overview-layout">
        <div class="overview-main">
          <section class="overview-section overview-section--metrics" aria-label="系统统计">
            <header class="overview-section__heading"><div><h3>运行概况</h3><p>面板从节点心跳和配置状态汇总实时数据。</p></div><ShieldCheck :size="19" /></header>
            <div class="overview-metrics">
              <article class="metric-card metric-card--blue"><Server :size="20" /><div><span>节点总数</span><strong>{{ overview.node_count }}</strong><small>已注册节点</small></div></article>
              <article class="metric-card metric-card--green"><CircleCheck :size="20" /><div><span>在线节点</span><strong>{{ overview.online_node_count }}</strong><small>当前可用</small></div></article>
              <article class="metric-card metric-card--gold"><Activity :size="20" /><div><span>同步中</span><strong>{{ overview.syncing_node_count }}</strong><small>等待配置生效</small></div></article>
              <article class="metric-card metric-card--red"><CircleAlert :size="20" /><div><span>配置失败</span><strong>{{ overview.failed_apply_count }}</strong><small>需要检查</small></div></article>
              <article class="metric-card metric-card--blue"><HardDrive :size="20" /><div><span>设备组</span><strong>{{ overview.group_count }}</strong><small>配置组织</small></div></article>
            </div>
          </section>

          <section class="overview-section overview-section--nodes">
            <header class="overview-section__heading"><div><h3>节点系统状态</h3><p>资源数据由节点 Agent 心跳采集，无数据时显示“未采集”。</p></div><span class="overview-section__count">{{ overview.nodes.length }} 台</span></header>
            <div v-if="!overview.nodes.length" class="overview-empty">当前没有已注册节点，请先在设备组中创建安装令牌。</div>
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

          <section class="overview-section overview-section--actions" aria-label="常用操作">
            <header class="overview-section__heading"><div><h3>常用操作</h3><p>从这里进入面板的完整管理功能。</p></div><ArrowRight :size="18" /></header>
            <div class="overview-action-grid">
              <RouterLink v-for="action in quickActions" :key="action.to" :to="action.to" class="overview-action">
                <span class="overview-action__icon"><component :is="action.icon" :size="18" /></span>
                <span class="overview-action__copy"><strong>{{ action.label }}</strong><small>{{ action.description }}</small></span>
                <ArrowRight :size="15" />
              </RouterLink>
            </div>
          </section>

          <section v-if="siteStore.info.value?.announcements.length" class="site-announcements" aria-label="站点公告">
            <h3>站点公告</h3>
            <article v-for="announcement in siteStore.info.value.announcements" :key="announcement.id" :class="`site-announcement site-announcement--${announcement.level}`"><strong>{{ announcement.title }}</strong><p>{{ announcement.content }}</p></article>
          </section>
        </div>

        <aside class="overview-aside" aria-label="系统摘要">
          <section class="overview-summary-card">
            <header><div><span class="overview-card-kicker">面板摘要</span><h3>{{ siteStore.siteName.value }}</h3></div><Database :size="18" /></header>
            <dl class="overview-summary-list">
              <div><dt>面板版本</dt><dd>{{ siteStore.info.value?.platform_version || '未采集' }}</dd></div>
              <div><dt>节点在线率</dt><dd>{{ onlineRatio }}</dd></div>
              <div><dt>设备组数量</dt><dd>{{ overview.group_count }}</dd></div>
              <div><dt>数据采样节点</dt><dd>{{ featuredNode?.name || '未采集' }}</dd></div>
            </dl>
          </section>
          <section class="overview-summary-card overview-summary-card--resource">
            <header><div><span class="overview-card-kicker">最近节点资源</span><h3>{{ featuredNode?.name || '暂无节点' }}</h3></div><Cpu :size="18" /></header>
            <div v-if="featuredNode" class="overview-resource-list">
              <div><span>CPU 使用率</span><strong>{{ percent(featuredNode, 'cpu_percent') }}</strong><b><i :style="{ width: percent(featuredNode, 'cpu_percent') === '未采集' ? '0%' : percent(featuredNode, 'cpu_percent') }" /></b></div>
              <div><span>内存使用率</span><strong>{{ percent(featuredNode, 'memory_percent', 'memory_used_bytes', 'memory_total_bytes') }}</strong><b><i :style="{ width: percent(featuredNode, 'memory_percent', 'memory_used_bytes', 'memory_total_bytes') === '未采集' ? '0%' : percent(featuredNode, 'memory_percent', 'memory_used_bytes', 'memory_total_bytes') }" /></b></div>
              <div><span>磁盘使用率</span><strong>{{ percent(featuredNode, 'disk_percent', 'disk_used_bytes', 'disk_total_bytes') }}</strong><b><i :style="{ width: percent(featuredNode, 'disk_percent', 'disk_used_bytes', 'disk_total_bytes') === '未采集' ? '0%' : percent(featuredNode, 'disk_percent', 'disk_used_bytes', 'disk_total_bytes') }" /></b></div>
              <div class="overview-resource-list__pair"><span>下行 / 上行</span><strong>{{ bytesPerSecond(number(featuredNode, 'net_in_speed_bytes_per_second')) }} / {{ bytesPerSecond(number(featuredNode, 'net_out_speed_bytes_per_second')) }}</strong></div>
            </div>
            <p v-else class="overview-aside-empty">节点上线后，这里会显示最新采样。</p>
          </section>
          <section class="overview-summary-card overview-summary-card--details">
            <header><div><span class="overview-card-kicker">运行信息</span><h3>服务状态</h3></div><Wifi :size="18" /></header>
            <dl class="overview-summary-list">
              <div><dt>最近心跳</dt><dd>{{ featuredNode ? heartbeat(featuredNode) : '未采集' }}</dd></div>
              <div><dt>Agent / 引擎</dt><dd>{{ featuredNode ? `${featuredNode.agent_version || '未采集'} · ${engines(featuredNode)}` : '未采集' }}</dd></div>
              <div><dt>公网地址</dt><dd>{{ featuredNode ? ((resource(featuredNode).ipv4 as string) || '未采集') : '未采集' }}</dd></div>
              <div><dt>更新时间</dt><dd>{{ siteStore.info.value?.build_time ? new Date(siteStore.info.value.build_time).toLocaleString('zh-CN', { hour12: false }) : '未采集' }}</dd></div>
            </dl>
          </section>
        </aside>
      </div>
    </template>
  </div>
</template>
