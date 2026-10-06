<script setup lang="ts">
import { computed, onMounted, onUnmounted, ref } from 'vue'
import { KeyRound, RefreshCw, Search } from '@lucide/vue'

import { api, type EdgeNode } from '@/api'
import StatePanel from '@/components/StatePanel.vue'
import CreateEnrollmentTokenDialog from '@/features/enrollment/CreateEnrollmentTokenDialog.vue'
import NodeDetailsDialog from '@/features/nodes/NodeDetailsDialog.vue'
import NodeInventory from '@/features/nodes/NodeInventory.vue'
import { displayError } from '@/lib/displayFormatters'

const nodes = ref<EdgeNode[]>([])
const loading = ref(true)
const errorMessage = ref('')
const query = ref('')
type NodeFilter = 'all' | 'online' | 'syncing' | 'failed' | 'offline'
const statusFilter = ref<NodeFilter>('all')
const showEnrollmentDialog = ref(false)
const selectedNodeId = ref<string | null>(null)
const selectedNode = computed(() => nodes.value.find((node) => node.id === selectedNodeId.value) ?? null)

const counts = computed<Record<NodeFilter | 'heartbeat', number>>(() => ({
  all: nodes.value.length,
  heartbeat: nodes.value.filter((node) => ['online', 'syncing', 'failed'].includes(node.status)).length,
  online: nodes.value.filter((node) => node.status === 'online').length,
  syncing: nodes.value.filter((node) => node.status === 'syncing').length,
  failed: nodes.value.filter((node) => node.status === 'failed').length,
  offline: nodes.value.filter((node) => node.status === 'offline').length,
}))

const filters: Array<{ value: NodeFilter; label: string }> = [
  { value: 'all', label: '全部' }, { value: 'online', label: '配置正常' }, { value: 'syncing', label: '同步中' },
  { value: 'failed', label: '失败' }, { value: 'offline', label: '离线' },
]

const filteredNodes = computed(() => {
  const keyword = query.value.trim().toLowerCase()
  return nodes.value.filter((node) => {
    const matchesStatus = statusFilter.value === 'all' || node.status === statusFilter.value
    const matchesQuery = !keyword || [node.name, node.hostname, node.platform, node.architecture, node.agent_version].some((value) => value.toLowerCase().includes(keyword))
    return matchesStatus && matchesQuery
  })
})

let inFlight = false
async function load(): Promise<void> {
  if (inFlight) return
  inFlight = true
  loading.value = true
  errorMessage.value = ''
  try {
    nodes.value = (await api.getNodes()).items
  } catch (error) {
    errorMessage.value = displayError(error)
  } finally {
    inFlight = false
    loading.value = false
  }
}

let refreshTimer: ReturnType<typeof setInterval> | undefined
onMounted(() => {
  void load()
  refreshTimer = setInterval(() => {
    if (document.visibilityState === 'visible') void load()
  }, 30_000)
})
onUnmounted(() => {
  if (refreshTimer) clearInterval(refreshTimer)
})
</script>

<template>
  <div class="page-stack">
    <header class="page-heading">
      <div><h2>节点清单</h2><p>查看心跳、引擎版本和节点配置应用进度</p></div>
      <div class="page-heading__actions">
        <button class="button button--secondary" type="button" :disabled="loading" @click="load"><RefreshCw :class="{ spin: loading }" :size="16" />刷新</button>
        <button class="button button--primary" type="button" @click="showEnrollmentDialog = true"><KeyRound :size="16" />注册节点</button>
      </div>
    </header>

    <div v-if="nodes.length" class="node-status-summary" aria-label="节点状态统计">
      <span>总计 <strong>{{ counts.all }}</strong></span><span>心跳在线 <strong>{{ counts.heartbeat }}</strong></span><span>配置正常 <strong>{{ counts.online }}</strong></span><span>同步中 <strong>{{ counts.syncing }}</strong></span><span>应用失败 <strong>{{ counts.failed }}</strong></span><span>离线 <strong>{{ counts.offline }}</strong></span>
    </div>

    <section class="toolbar" aria-label="节点筛选">
      <label class="search-field"><Search :size="17" /><input v-model="query" placeholder="搜索节点名称或主机名" /></label>
      <div class="segmented-control" role="group" aria-label="状态筛选">
        <button v-for="filter in filters" :key="filter.value" type="button" :class="{ active: statusFilter === filter.value }" @click="statusFilter = filter.value">{{ filter.label }} {{ counts[filter.value] }}</button>
      </div>
    </section>

    <StatePanel v-if="loading && !nodes.length" state="loading" title="正在读取节点" />
    <StatePanel v-else-if="errorMessage && !nodes.length" state="error" title="节点加载失败" :message="errorMessage" @retry="load" />
    <StatePanel v-else-if="!nodes.length" state="empty" title="还没有节点" message="生成一次性注册令牌后，可在目标机器完成节点注册。" />
    <StatePanel v-else-if="!filteredNodes.length" state="empty" title="没有符合条件的节点" message="调整搜索词或状态筛选后重试。" />
    <NodeInventory v-else :nodes="filteredNodes" @inspect="selectedNodeId = $event.id" />

    <p v-if="errorMessage && nodes.length" class="inline-warning">刷新失败，当前显示上一次成功读取的数据：{{ errorMessage }}</p>
    <CreateEnrollmentTokenDialog v-if="showEnrollmentDialog" @close="showEnrollmentDialog = false" />
    <NodeDetailsDialog v-if="selectedNode" :node="selectedNode" @close="selectedNodeId = null" />
  </div>
</template>
