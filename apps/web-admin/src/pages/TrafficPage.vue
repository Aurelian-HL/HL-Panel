<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { Activity, AlertTriangle, Database, Gauge, RefreshCw, RotateCcw, Search, ShieldAlert } from '@lucide/vue'

import { usageApi, type EnforcementDecision, type EnforcementReason, type EnforcementStatus, type UsageQuery, type UsageRecord, type UsageResponse, type UsageScope } from '@/api/usage'
import StatePanel from '@/components/StatePanel.vue'
import { toast } from '@/composables/toast'
import { useMutationKey } from '@/composables/useMutationKey'
import RevokeEnforcementDialog from '@/features/usage/RevokeEnforcementDialog.vue'
import { displayError, formatDateTime } from '@/lib/displayFormatters'

const PAGE_SIZE = 25
const emptyResponse = (): UsageResponse => ({
  items: [], totals: { rule_actual_bytes: 0, customer_actual_bytes: 0, charged_bytes: 0 }, total: 0, page: 1, page_size: PAGE_SIZE,
})

const result = ref<UsageResponse>(emptyResponse())
const loading = ref(true)
const errorMessage = ref('')
const actionError = ref('')
const filterMessage = ref('')
const scope = ref<UsageScope>('site')
const scopeID = ref('')
const fromLocal = ref('')
const toLocal = ref('')
const revokeCandidate = ref<EnforcementDecision | null>(null)
const revokeBusy = ref(false)
const mutationKey = useMutationKey()

const totalPages = computed(() => Math.max(1, Math.ceil(result.value.total / result.value.page_size)))
const pendingCount = computed(() => result.value.items.filter((item) => ['pending', 'revoke_pending'].includes(item.enforcement_decision?.status ?? '')).length)
const scopePlaceholder = computed(() => ({
  customer: '输入用户 ID', rule: '输入规则 ID', device_group: '输入设备组 ID', site: '',
})[scope.value])

function isoFromLocal(value: string): string | undefined {
  if (!value) return undefined
  const parsed = new Date(value)
  return Number.isNaN(parsed.getTime()) ? undefined : parsed.toISOString()
}

function buildQuery(page: number): UsageQuery | null {
  filterMessage.value = ''
  const id = scopeID.value.trim()
  if (scope.value !== 'site' && !id) {
    filterMessage.value = '请输入对应的查询 ID'
    return null
  }
  const from = isoFromLocal(fromLocal.value)
  const to = isoFromLocal(toLocal.value)
  if ((fromLocal.value && !from) || (toLocal.value && !to)) {
    filterMessage.value = '时间格式无效'
    return null
  }
  if (from && to && new Date(to).getTime() <= new Date(from).getTime()) {
    filterMessage.value = '结束时间必须晚于开始时间'
    return null
  }
  return { scope: scope.value, scope_id: id || undefined, from, to, page, page_size: PAGE_SIZE }
}

async function load(page = 1): Promise<void> {
  const query = buildQuery(page)
  if (!query) return
  loading.value = true
  errorMessage.value = ''
  try {
    result.value = await usageApi.query(query)
  } catch (error) {
    errorMessage.value = displayError(error)
  } finally {
    loading.value = false
  }
}

function changeScope(): void {
  scopeID.value = ''
  filterMessage.value = ''
}

function resetFilters(): void {
  scope.value = 'site'
  scopeID.value = ''
  fromLocal.value = ''
  toLocal.value = ''
  void load(1)
}

function formatBytes(value: number): string {
  if (!Number.isFinite(value) || value < 0) return '数据异常'
  const units = ['B', 'KiB', 'MiB', 'GiB', 'TiB', 'PiB']
  let amount = value
  let unit = 0
  while (amount >= 1024 && unit < units.length - 1) {
    amount /= 1024
    unit += 1
  }
  return `${amount.toLocaleString('zh-CN', { maximumFractionDigits: unit === 0 ? 0 : 2 })} ${units[unit]}`
}

function multiplier(value: number): string {
  return `×${(value / 1_000_000).toLocaleString('zh-CN', { maximumFractionDigits: 6 })}`
}

function reasonLabel(reason: EnforcementReason): string {
  return { customer_disabled: '用户已停用', customer_expired: '用户已到期', quota_exhausted: '流量已达限额' }[reason]
}

function statusLabel(status: EnforcementStatus): string {
  return {
    pending: '等待停用', applied: '停用已执行', apply_failed: '停用失败', revoke_pending: '等待恢复',
    revoked: '已恢复', revoke_failed: '恢复失败',
  }[status]
}

function statusClass(status: EnforcementStatus): string {
  if (status === 'applied') return 'decision-badge--applied'
  if (status === 'revoked') return 'decision-badge--revoked'
  if (status === 'apply_failed' || status === 'revoke_failed') return 'decision-badge--failed'
  return 'decision-badge--pending'
}

async function confirmRevoke(): Promise<void> {
  const decision = revokeCandidate.value
  if (!decision || revokeBusy.value) return
  revokeBusy.value = true
  actionError.value = ''
  try {
    const response = await usageApi.revoke(decision.id, mutationKey({ decision_id: decision.id, revision: decision.revision }))
    result.value = {
      ...result.value,
      items: result.value.items.map((item) => item.enforcement_decision?.id === response.decision.id
        ? { ...item, enforcement_decision: response.decision }
        : item),
    }
    revokeCandidate.value = null
    toast.success(response.replayed ? '撤销请求已存在' : '撤销指令已提交', '等待节点执行启用命令并回执')
  } catch (error) {
    actionError.value = displayError(error)
  } finally {
    revokeBusy.value = false
  }
}

function routeLabel(item: UsageRecord): string {
  return item.exit_group_id ? `${item.entry_group_id} → ${item.exit_group_id}` : `${item.entry_group_id} → 直出`
}

onMounted(() => load())
</script>

<template>
  <div class="page-stack traffic-page">
    <header class="page-heading">
      <div>
        <h2>流量统计</h2>
        <p>规则用量、用户计费用量与限额建议</p>
      </div>
      <button class="button button--secondary" type="button" :disabled="loading" @click="load(result.page)">
        <RefreshCw :class="{ spin: loading }" :size="16" />刷新
      </button>
    </header>

    <section class="toolbar traffic-filters" aria-label="流量筛选">
      <label class="traffic-filter">
        <span>统计范围</span>
        <select v-model="scope" @change="changeScope">
          <option value="site">全站</option>
          <option value="customer">指定用户</option>
          <option value="rule">指定规则</option>
          <option value="device_group">指定设备组</option>
        </select>
      </label>
      <label v-if="scope !== 'site'" class="traffic-filter traffic-filter--id">
        <span>对象 ID</span>
        <input v-model="scopeID" :placeholder="scopePlaceholder" @keyup.enter="load(1)" />
      </label>
      <label class="traffic-filter">
        <span>开始时间</span>
        <input v-model="fromLocal" type="datetime-local" />
      </label>
      <label class="traffic-filter">
        <span>结束时间</span>
        <input v-model="toLocal" type="datetime-local" />
      </label>
      <div class="traffic-filter-actions">
        <button class="button button--primary" type="button" :disabled="loading" @click="load(1)"><Search :size="15" />查询</button>
        <button class="button button--quiet" type="button" :disabled="loading" @click="resetFilters"><RotateCcw :size="15" />重置</button>
      </div>
    </section>
    <p v-if="filterMessage" class="inline-warning" role="alert">{{ filterMessage }}</p>
    <p v-if="actionError" class="form-error" role="alert">撤销失败：{{ actionError }}</p>

    <section class="metric-grid traffic-metrics" aria-label="流量汇总">
      <article class="metric-card metric-card--blue"><span class="metric-card__icon"><Activity :size="20" /></span><div><span>规则实际流量</span><strong>{{ formatBytes(result.totals.rule_actual_bytes) }}</strong></div></article>
      <article class="metric-card metric-card--navy"><span class="metric-card__icon"><Database :size="20" /></span><div><span>用户实际流量</span><strong>{{ formatBytes(result.totals.customer_actual_bytes) }}</strong></div></article>
      <article class="metric-card metric-card--gold"><span class="metric-card__icon"><Gauge :size="20" /></span><div><span>计费流量</span><strong>{{ formatBytes(result.totals.charged_bytes) }}</strong></div></article>
      <article class="metric-card" :class="pendingCount ? 'metric-card--red' : 'metric-card--green'"><span class="metric-card__icon"><ShieldAlert :size="20" /></span><div><span>本页待处理指令</span><strong>{{ pendingCount }}</strong></div></article>
    </section>

    <StatePanel v-if="loading && !result.items.length" state="loading" title="正在读取流量记录" />
    <StatePanel v-else-if="errorMessage && !result.items.length" state="error" title="流量记录加载失败" :message="errorMessage" @retry="load(result.page)" />
    <StatePanel v-else-if="!result.items.length" state="empty" title="暂无流量记录" message="当前筛选范围内没有已入账的流量。" />

    <template v-else>
      <section class="table-wrap traffic-table-wrap">
        <table class="data-table traffic-table">
          <thead><tr><th>发生时间</th><th>用户 / 规则</th><th>转发路径</th><th>规则实际</th><th>用户实际</th><th>倍率快照</th><th>计费流量</th><th>限额状态</th></tr></thead>
          <tbody>
            <tr v-for="item in result.items" :key="`${item.node_id}:${item.boot_id}:${item.sequence}`">
              <td><strong class="table-primary">{{ formatDateTime(item.occurred_at) }}</strong><small class="table-secondary">序号 {{ item.sequence }}</small></td>
              <td><strong class="table-primary">{{ item.customer_id }}</strong><small class="table-secondary">{{ item.rule_id }}</small></td>
              <td><strong class="route-text">{{ routeLabel(item) }}</strong><small class="table-secondary">节点 {{ item.node_id }}</small></td>
              <td>{{ formatBytes(item.rule_actual_bytes) }}</td>
              <td>{{ formatBytes(item.customer_actual_bytes) }}</td>
              <td><span class="multiplier-chip">入口 {{ multiplier(item.entry_multiplier_micros) }}</span><span class="multiplier-chip">出口 {{ multiplier(item.exit_multiplier_micros) }}</span></td>
              <td><strong class="charged-value">{{ formatBytes(item.charged_bytes) }}</strong></td>
              <td>
                <template v-if="item.enforcement_decision">
                  <span class="decision-badge" :class="statusClass(item.enforcement_decision.status)"><AlertTriangle :size="13" />{{ statusLabel(item.enforcement_decision.status) }} · {{ reasonLabel(item.enforcement_decision.reason) }}</span>
                  <small v-if="item.enforcement_decision.last_error" class="decision-error" :title="item.enforcement_decision.last_error">{{ item.enforcement_decision.last_error }}</small>
                  <button v-if="item.enforcement_decision.status === 'applied'" class="button button--quiet decision-action" type="button" @click="revokeCandidate = item.enforcement_decision">撤销停用</button>
                </template>
                <span v-else class="status-badge status-badge--success">正常</span>
              </td>
            </tr>
          </tbody>
        </table>
      </section>

      <section class="mobile-resource-list traffic-mobile-list" aria-label="移动端流量记录">
        <article v-for="item in result.items" :key="`mobile:${item.node_id}:${item.boot_id}:${item.sequence}`" class="mobile-resource-card">
          <header><div class="resource-name"><strong>{{ item.customer_id }}</strong><small>{{ formatDateTime(item.occurred_at) }}</small></div><strong class="charged-value">{{ formatBytes(item.charged_bytes) }}</strong></header>
          <dl>
            <div><dt>规则</dt><dd>{{ item.rule_id }}</dd></div><div><dt>路径</dt><dd>{{ routeLabel(item) }}</dd></div>
            <div><dt>规则实际</dt><dd>{{ formatBytes(item.rule_actual_bytes) }}</dd></div><div><dt>用户实际</dt><dd>{{ formatBytes(item.customer_actual_bytes) }}</dd></div>
            <div><dt>倍率</dt><dd>{{ multiplier(item.entry_multiplier_micros) }} / {{ multiplier(item.exit_multiplier_micros) }}</dd></div><div><dt>节点序号</dt><dd>{{ item.node_id }} · {{ item.sequence }}</dd></div>
          </dl>
          <div v-if="item.enforcement_decision" class="decision-note">
            <span><AlertTriangle :size="14" />{{ statusLabel(item.enforcement_decision.status) }}：{{ reasonLabel(item.enforcement_decision.reason) }}</span>
            <small v-if="item.enforcement_decision.last_error">{{ item.enforcement_decision.last_error }}</small>
            <button v-if="item.enforcement_decision.status === 'applied'" class="button button--secondary" type="button" @click="revokeCandidate = item.enforcement_decision">撤销停用</button>
          </div>
        </article>
      </section>

      <footer class="traffic-pagination">
        <span>共 {{ result.total.toLocaleString('zh-CN') }} 条，第 {{ result.page }} / {{ totalPages }} 页</span>
        <div><button class="button button--secondary" :disabled="loading || result.page <= 1" @click="load(result.page - 1)">上一页</button><button class="button button--secondary" :disabled="loading || result.page >= totalPages" @click="load(result.page + 1)">下一页</button></div>
      </footer>
    </template>
    <p v-if="errorMessage && result.items.length" class="inline-warning">刷新失败，保留上次数据：{{ errorMessage }}</p>
    <RevokeEnforcementDialog v-if="revokeCandidate" :decision="revokeCandidate" :busy="revokeBusy" @close="revokeCandidate = null" @confirm="confirmRevoke" />
  </div>
</template>

<style scoped>
.traffic-filters { display: grid; grid-template-columns: 150px minmax(190px, 1fr) repeat(2, minmax(190px, .75fr)) auto; align-items: end; }
.traffic-filter { display: flex; min-width: 0; flex-direction: column; gap: 5px; color: var(--gray-600); font-size: 11px; font-weight: 600; }
.traffic-filter input, .traffic-filter select { width: 100%; min-height: 32px; padding: 5px 8px; border: 1px solid var(--ny-border-dark); border-radius: 3px; background: #fff; color: var(--gray-800); font-size: 12px; }
.traffic-filter--id { min-width: 210px; }
.traffic-filter-actions { display: flex; gap: 5px; }
.traffic-metrics { grid-template-columns: repeat(4, minmax(0, 1fr)); }
.traffic-metrics strong { font-size: 20px; }
.traffic-table-wrap { overflow-x: auto; }
.traffic-table { min-width: 1180px; }
.traffic-table td { white-space: nowrap; }
.traffic-table td:first-child, .traffic-table td:nth-child(2), .traffic-table td:nth-child(3) { white-space: normal; }
.traffic-table td strong, .traffic-table td small { display: block; }
.route-text { max-width: 230px; overflow: hidden; color: var(--gray-800); font-size: 12px; text-overflow: ellipsis; white-space: nowrap; }
.multiplier-chip { display: block; color: var(--gray-600); font-size: 11px; }
.charged-value { color: var(--navy-700); font-variant-numeric: tabular-nums; }
.decision-badge { display: inline-flex; min-height: 24px; align-items: center; gap: 4px; padding: 3px 7px; border: 1px solid; border-radius: 3px; font-size: 11px; font-weight: 600; }
.decision-badge--pending { border-color: #ead9ad; background: #fff9e9; color: #765a13; }
.decision-badge--applied { border-color: #f2c6bd; background: #fff3f0; color: #a63d2b; }
.decision-badge--failed { border-color: #e1b9b9; background: #fff1f1; color: #9f2f2f; }
.decision-badge--revoked { border-color: #b9ddcc; background: #f0faf5; color: #27664b; }
.decision-error { display: block; max-width: 220px; margin-top: 4px; overflow: hidden; color: #9f2f2f; text-overflow: ellipsis; }
.decision-action { min-height: 26px; margin-top: 4px; padding: 3px 7px; }
.traffic-mobile-list { display: none; }
.decision-note { display: grid; gap: 7px; margin-top: 12px; padding: 8px; border-radius: 3px; background: #fff3f0; color: #a63d2b; font-size: 11px; font-weight: 600; }
.decision-note > span { display: flex; align-items: center; gap: 5px; }
.decision-note small { font-weight: 500; overflow-wrap: anywhere; }
.decision-note .button { justify-self: start; }
.traffic-pagination { display: flex; align-items: center; justify-content: space-between; gap: 12px; color: var(--gray-500); font-size: 12px; }
.traffic-pagination > div { display: flex; gap: 6px; }
@media (max-width: 1180px) {
  .traffic-filters { grid-template-columns: repeat(2, minmax(0, 1fr)); }
  .traffic-filter-actions { justify-content: flex-end; }
  .traffic-metrics { grid-template-columns: repeat(2, minmax(0, 1fr)); }
}
@media (max-width: 760px) {
  .traffic-filters { grid-template-columns: 1fr; }
  .traffic-filter-actions { display: grid; grid-template-columns: 1fr 1fr; }
  .traffic-table-wrap { display: none; }
  .traffic-mobile-list { display: flex; }
  .traffic-pagination { align-items: stretch; flex-direction: column; }
  .traffic-pagination > div { display: grid; grid-template-columns: 1fr 1fr; }
}
@media (max-width: 420px) {
  .traffic-metrics { grid-template-columns: 1fr; }
}
</style>
