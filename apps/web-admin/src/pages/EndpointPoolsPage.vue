<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { Plus, RefreshCw, Search } from '@lucide/vue'

import { api, type DeviceGroup, type EndpointPool, type EndpointPoolProtocol } from '@/api'
import { businessApi, type ForwardRule, type GroupNetwork } from '@/api/business'
import StatePanel from '@/components/StatePanel.vue'
import CreateEndpointPoolDialog from '@/features/endpoint-pools/CreateEndpointPoolDialog.vue'
import DeleteEndpointPoolDialog from '@/features/endpoint-pools/DeleteEndpointPoolDialog.vue'
import EndpointPoolInventory from '@/features/endpoint-pools/EndpointPoolInventory.vue'
import { useMutationKey } from '@/composables/useMutationKey'
import { toast } from '@/composables/toast'
import { displayError } from '@/lib/displayFormatters'

const pools = ref<EndpointPool[]>([])
const groups = ref<DeviceGroup[]>([])
const networks = ref<GroupNetwork[]>([])
const rules = ref<ForwardRule[]>([])
const loading = ref(true)
const errorMessage = ref('')
const query = ref('')
const protocolFilter = ref<'ALL' | EndpointPoolProtocol>('ALL')
const showCreateDialog = ref(false)
const deletingPool = ref<EndpointPool | null>(null)
const deleting = ref(false)
const deleteError = ref('')
const deleteMutationKey = useMutationKey()

const filters: Array<{ value: 'ALL' | EndpointPoolProtocol; label: string }> = [
  { value: 'ALL', label: '全部' },
  { value: 'vless', label: 'VLESS' },
  { value: 'tcp', label: 'NY TCP 透传' },
  { value: 'socks5', label: 'NY SOCKS5' },
]

const filteredPools = computed(() => {
  const keyword = query.value.trim().toLowerCase()
  return pools.value.filter((pool) => {
    const matchesProtocol = protocolFilter.value === 'ALL' || pool.protocol === protocolFilter.value
    const group = groups.value.find((item) => item.id === pool.group_id)
    const matchesQuery = !keyword || `${pool.name} ${pool.hostname} ${group?.name ?? ''}`.toLowerCase().includes(keyword)
    return matchesProtocol && matchesQuery
  })
})

async function load(): Promise<void> {
  loading.value = true
  errorMessage.value = ''
  try {
    const [poolResponse, groupResponse, networkResponse, ruleResponse] = await Promise.all([api.getEndpointPools(), api.getDeviceGroups(), businessApi.groupNetworks(), businessApi.rules()])
    pools.value = poolResponse.items
    groups.value = groupResponse.items
    networks.value = networkResponse.items
    rules.value = ruleResponse.items
  } catch (error) {
    errorMessage.value = displayError(error)
  } finally {
    loading.value = false
  }
}

function onCreated(pool: EndpointPool, replayed: boolean): void {
  showCreateDialog.value = false
  const existingIndex = pools.value.findIndex((item) => item.id === pool.id)
  if (existingIndex >= 0) pools.value.splice(existingIndex, 1, pool)
  else pools.value.unshift(pool)
  toast.success(replayed ? '服务端点已幂等复用' : '服务端点已创建', `${pool.hostname}:${pool.port}`)
}

function requestDelete(pool: EndpointPool): void {
  deletingPool.value = pool
  deleteError.value = ''
}

async function confirmDelete(): Promise<void> {
  const pool = deletingPool.value
  if (!pool || deleting.value) return
  deleting.value = true
  deleteError.value = ''
  try {
    const result = await api.deleteEndpointPool(pool.id, deleteMutationKey(pool.id))
    pools.value = pools.value.filter((item) => item.id !== pool.id)
    deletingPool.value = null
    toast.success(result.replayed ? '服务端点已删除' : '服务端点删除成功', pool.name)
  } catch (error) {
    deleteError.value = displayError(error)
  } finally {
    deleting.value = false
  }
}

onMounted(load)
</script>

<template>
  <div class="page-stack">
    <header class="page-heading">
      <div><h2>服务端点</h2><p>配置单一稳定入口的设备组调度；NY TCP、NY UDP、NY SOCKS5 与 VLESS 入口分别管理</p></div>
      <div class="page-heading__actions"><button class="button button--secondary" type="button" :disabled="loading" @click="load"><RefreshCw :class="{ spin: loading }" :size="16" />刷新</button><button class="button button--primary" type="button" @click="showCreateDialog = true"><Plus :size="16" />创建服务端点</button></div>
    </header>

    <section class="toolbar" aria-label="服务端点筛选">
      <label class="search-field"><Search :size="17" /><input v-model="query" placeholder="搜索名称、主机或设备组" /></label>
      <div class="segmented-control" role="group" aria-label="协议筛选"><button v-for="filter in filters" :key="filter.value" type="button" :class="{ active: protocolFilter === filter.value }" @click="protocolFilter = filter.value">{{ filter.label }}</button></div>
    </section>

    <StatePanel v-if="loading && !pools.length" state="loading" title="正在读取服务端点" />
    <StatePanel v-else-if="errorMessage && !pools.length" state="error" title="服务端点加载失败" :message="errorMessage" @retry="load" />
    <StatePanel v-else-if="!pools.length" state="empty" title="还没有服务端点" message="此处只保存设备组调度模型。创建端点不会签发 VLESS 链接或立即提供转发。" />
    <StatePanel v-else-if="!filteredPools.length" state="empty" title="没有符合条件的服务端点" message="调整搜索词或协议筛选后重试。" />
    <EndpointPoolInventory v-else :pools="filteredPools" :groups="groups" @delete="requestDelete" />
    <p v-if="errorMessage && pools.length" class="inline-warning">刷新失败，当前显示上一次成功读取的数据：{{ errorMessage }}</p>

    <CreateEndpointPoolDialog v-if="showCreateDialog" :groups="groups" :networks="networks" :rules="rules" @close="showCreateDialog = false" @created="onCreated" />
    <DeleteEndpointPoolDialog v-if="deletingPool" :pool="deletingPool" :busy="deleting" :error-message="deleteError" @close="deletingPool = null" @confirm="confirmDelete" />
  </div>
</template>
