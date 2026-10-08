<script setup lang="ts">
import { computed, onMounted, onUnmounted, ref, watch } from 'vue'
import { Download, FolderInput, FolderTree, Pause, Play, Plus, RefreshCw, Search, Trash2, Upload } from '@lucide/vue'
import { RouterLink } from 'vue-router'
import type { DeviceGroup } from '@/api'
import { businessApi, type ForwardRule, type GroupNetwork, type RuleBatchOperation, type RuleGroup } from '@/api/business'
import { subscriptionApi } from '@/api/subscriptions'
import { downloadSubscriptionPackage } from '@/lib/subscriptionDownload'
import { ApiError } from '@/api/http'
import StatePanel from '@/components/StatePanel.vue'
import ForwardRuleEditor from '@/features/forwarding/ForwardRuleEditor.vue'
import ForwardRuleInventory from '@/features/forwarding/ForwardRuleInventory.vue'
import ConfirmRuleDeletion from '@/features/forwarding/ConfirmRuleDeletion.vue'
import ForwardRuleTransferDialog from '@/features/forwarding/ForwardRuleTransferDialog.vue'
import { loadRuleTraffic } from '@/features/forwarding/ruleTraffic'
import { toast } from '@/composables/toast'
import { useMutationKey } from '@/composables/useMutationKey'
import { ruleInput } from '@/lib/businessFormatters'
import { displayError } from '@/lib/displayFormatters'
import { ruleActivationNotice } from '@/lib/ruleActivation'
import { readProbeRefreshIntervals } from '@/features/probe/refreshPreferences'
const rules = ref<ForwardRule[]>([])
const devices = ref<DeviceGroup[]>([])
const networks = ref<GroupNetwork[]>([])
const ruleGroups = ref<RuleGroup[]>([])
const loading = ref(true)
const error = ref('')
const resourceWarning = ref('')
const actionError = ref('')
const query = ref('')
const filter = ref('all')
const groupFilter = ref('all')
const selectedIds = ref<string[]>([])
const page = ref(1)
const pageSize = ref(20)
const pageSizeOptions = [10, 20, 50, 100, 200, 500, 1000]
const moveGroupId = ref('')
const batchBusy = ref(false)
const deletionCandidates = ref<ForwardRule[]>([])
const editor = ref(false)
const selected = ref<ForwardRule | null>(null)
const copying = ref(false)
const busyId = ref('')
const transferDialog = ref(false)
const exportBusy = ref(false)
const trafficByRule = ref<Record<string, number>>({})
const mutationKey = useMutationKey()
let loadVersion = 0
let trafficTimer: number | undefined
let trafficRequestInFlight = false
const trafficRefreshIntervals = readProbeRefreshIntervals()
const ready = computed(() => !loading.value && !error.value)
function isUnauthorized(cause: unknown): boolean { return cause instanceof ApiError && cause.status === 401 }
function invalidateSession(): void {
  loadVersion++
  loading.value = false
  error.value = '登录已失效，请重新登录'
  resourceWarning.value = ''; actionError.value = ''
  rules.value = []; devices.value = []; networks.value = []; ruleGroups.value = []
  trafficByRule.value = {}
  selectedIds.value = []; deletionCandidates.value = []
  closeEditor(); transferDialog.value = false
}
const filtered = computed(() => rules.value.filter((item) => {
  const entry = devices.value.find((group) => group.id === item.entry_group_id)
  const exit = devices.value.find((group) => group.id === item.exit_group_id)
  const vlessUpstream = item.vless_socks5_host && item.vless_socks5_port
    ? `${item.vless_socks5_host}:${item.vless_socks5_port}`
    : ''
  const searchable = [item.name, item.listen_port, entry?.name, exit?.name, vlessUpstream, ...item.targets.map((target) => `${target.host}:${target.port}`)].join(' ').toLowerCase()
  return searchable.includes(query.value.trim().toLowerCase()) && (filter.value === 'all' || (filter.value === 'paused' ? item.paused || item.status === 'quota_exhausted' : !item.paused && item.status !== 'quota_exhausted')) && (groupFilter.value === 'all' || (groupFilter.value === 'ungrouped' ? !item.rule_group_id : item.rule_group_id === groupFilter.value))
}))
const totalPages = computed(() => Math.max(1, Math.ceil(filtered.value.length / pageSize.value)))
const pagedRules = computed(() => filtered.value.slice((page.value - 1) * pageSize.value, page.value * pageSize.value))
const pageStart = computed(() => filtered.value.length ? (page.value - 1) * pageSize.value + 1 : 0)
const pageEnd = computed(() => Math.min(page.value * pageSize.value, filtered.value.length))
const selectedRules = computed(() => rules.value.filter((item) => selectedIds.value.includes(item.id)))
const allFilteredSelected = computed(() => filtered.value.length > 0 && filtered.value.every((item) => selectedIds.value.includes(item.id)))
async function load(): Promise<void> {
  const version = ++loadVersion
  loading.value = true; error.value = ''; resourceWarning.value = ''
  rules.value = []; devices.value = []; networks.value = []; ruleGroups.value = []
  trafficByRule.value = {}
  selectedIds.value = []; deletionCandidates.value = []
  page.value = 1
  closeEditor(); transferDialog.value = false
  const results = await Promise.allSettled([businessApi.rules(), businessApi.deviceGroups(), businessApi.groupNetworks(), businessApi.ruleGroups()])
  if (version !== loadVersion) return
  const [r, d, n, g] = results
  if (results.some((result) => result.status === 'rejected' && isUnauthorized(result.reason))) { invalidateSession(); return }
  if (r.status === 'fulfilled') rules.value = r.value.items
  else error.value = displayError(r.reason)
  if (d.status === 'fulfilled') devices.value = d.value.items
  if (n.status === 'fulfilled') networks.value = n.value.items
  if (g.status === 'fulfilled') ruleGroups.value = g.value.items
  const failedResources = ([['设备组', d], ['入口网络', n], ['规则分组', g]] as const)
    .flatMap(([name, result]) => result.status === 'rejected' ? [{ name, reason: result.reason }] : [])
  if (failedResources.length) resourceWarning.value = `${failedResources.map(({ name }) => name).join('、')}加载失败，相关选项暂不可用；请刷新重试。${displayError(failedResources[0]!.reason)}`
  loading.value = false
  if (r.status === 'fulfilled') void loadRuleTraffic(r.value.items.map((rule) => rule.id), {
    isCurrent: () => version === loadVersion,
    onValue: (id, bytes) => { trafficByRule.value[id] = bytes },
    onUnauthorized: invalidateSession,
  })
}
watch([query, filter, groupFilter, pageSize], () => { page.value = 1 })
watch(totalPages, (value) => { if (page.value > value) page.value = value })
async function refreshTraffic(): Promise<void> {
  if (trafficRequestInFlight || loading.value || error.value || busyId.value || batchBusy.value || !rules.value.length) return
  trafficRequestInFlight = true
  const version = loadVersion
  try {
    const result = await businessApi.rules()
    if (version !== loadVersion || busyId.value || batchBusy.value) return
    rules.value = result.items
    selectedIds.value = selectedIds.value.filter(id => result.items.some(rule => rule.id === id))
    await loadRuleTraffic(rules.value.map((rule) => rule.id), {
      isCurrent: () => version === loadVersion,
      onValue: (id, bytes) => { trafficByRule.value[id] = bytes },
      onUnauthorized: invalidateSession,
    })
  } catch (cause) {
    if (isUnauthorized(cause)) invalidateSession()
  } finally {
    trafficRequestInFlight = false
  }
}
function scheduleTrafficRefresh(): void {
  if (trafficTimer !== undefined) window.clearTimeout(trafficTimer)
  const interval = document.visibilityState === 'visible' ? trafficRefreshIntervals.foreground : trafficRefreshIntervals.background
  trafficTimer = window.setTimeout(async () => {
    await refreshTraffic()
    scheduleTrafficRefresh()
  }, interval)
}
function handleTrafficVisibilityChange(): void { scheduleTrafficRefresh() }
function startCreate(): void {
  if (!ready.value || batchBusy.value || busyId.value) return
  actionError.value = ''
  selected.value = null
  copying.value = false
  editor.value = true
}
function openExisting(rule: ForwardRule, copy = false): void {
  if (!ready.value || batchBusy.value || busyId.value) return
  const current = rules.value.find((item) => item.id === rule.id)
  if (!current || current.revision !== rule.revision) return
  selected.value = current; copying.value = copy; editor.value = true
}
function closeEditor(): void { editor.value = false; selected.value = null; copying.value = false }
async function saved(rule: ForwardRule): Promise<void> {
  closeEditor(); actionError.value = ''
  toast.success('规则已保存', ruleActivationNotice(rule))
  await load()
}
async function toggle(rule: ForwardRule): Promise<void> {
  if (!ready.value || busyId.value || batchBusy.value) return
  const version = loadVersion
  busyId.value = rule.id; actionError.value = ''
  const input = { ...ruleInput(rule), paused: !rule.paused }
  try { const updated = await businessApi.saveRule(input, rule.id, mutationKey({ id: rule.id, ...input })); if (version !== loadVersion) return; const index = rules.value.findIndex((item) => item.id === updated.id); if (index >= 0) rules.value[index] = updated; toast.success(`规则${ruleActivationNotice(updated)}`) }
  catch (cause) { if (isUnauthorized(cause)) invalidateSession(); else if (version === loadVersion) actionError.value = displayError(cause) } finally { busyId.value = '' }
}
function selectRule(rule: ForwardRule, selected: boolean): void {
  if (!ready.value || batchBusy.value || busyId.value) return
  selectedIds.value = selected ? [...new Set([...selectedIds.value, rule.id])] : selectedIds.value.filter((id) => id !== rule.id)
}
function selectFiltered(selected: boolean): void {
  if (!ready.value || batchBusy.value || busyId.value) return
  const visible = new Set(filtered.value.map((item) => item.id))
  selectedIds.value = selected ? [...new Set([...selectedIds.value, ...visible])] : selectedIds.value.filter((id) => !visible.has(id))
}
async function executeBatch(operation: RuleBatchOperation, candidates: ForwardRule[]): Promise<void> {
  if (!ready.value || batchBusy.value || busyId.value || !candidates.length) return
  if (candidates.some((candidate) => rules.value.find((rule) => rule.id === candidate.id)?.revision !== candidate.revision)) {
    deletionCandidates.value = []
    actionError.value = '规则已更新，请刷新后重试'
    return
  }
  const version = loadVersion
  batchBusy.value = true; actionError.value = ''
  const input = { operation, rule_ids: candidates.map((item) => item.id), rule_group_id: operation === 'move_group' ? moveGroupId.value : '', expected_revisions: Object.fromEntries(candidates.map((item) => [item.id, item.revision])) }
  try {
    const result = await businessApi.batchRules(input, mutationKey(input))
    if (version !== loadVersion) return
    if (operation === 'delete') {
      const deleted = new Set(result.items.map((item) => item.id))
      rules.value = rules.value.filter((item) => !deleted.has(item.id))
      deletionCandidates.value = []
    } else {
      const updated = new Map(result.items.map((item) => [item.id, item]))
      rules.value = rules.value.map((item) => updated.get(item.id) ?? item)
    }
    selectedIds.value = []
    toast.success(operation === 'pause' ? '已批量暂停' : operation === 'resume' ? '已批量恢复' : operation === 'move_group' ? '已批量移动分组' : '规则已删除', operation === 'resume' && result.items.some((item) => ruleActivationNotice(item).includes('暂不支持下发')) ? `${result.items.length} 条规则，部分规则暂不支持下发` : `${result.items.length} 条规则`)
  } catch (cause) { if (isUnauthorized(cause)) invalidateSession(); else if (version === loadVersion) actionError.value = displayError(cause) } finally { batchBusy.value = false }
}
async function batch(operation: RuleBatchOperation): Promise<void> { await executeBatch(operation, selectedRules.value) }
async function confirmDelete(): Promise<void> { await executeBatch('delete', deletionCandidates.value) }
function requestDelete(candidates: ForwardRule[]): void {
  if (!ready.value || batchBusy.value || busyId.value) return
  deletionCandidates.value = candidates.filter((candidate) => rules.value.some((rule) => rule.id === candidate.id && rule.revision === candidate.revision))
}
async function imported(count: number): Promise<void> {
  transferDialog.value = false
  toast.success('规则导入完成', `${count} 条规则已保存并等待激活`)
  await load()
}
async function copyConnection(rule: ForwardRule): Promise<void> {
  if (!ready.value || busyId.value || batchBusy.value) return
  busyId.value = rule.id; actionError.value = ''
  try {
    const generated = await subscriptionApi.generateRule(rule.id, mutationKey({ operation: 'generate-subscription', rule_id: rule.id, revision: rule.revision }))
    const pack = await subscriptionApi.package(generated.subscription.id)
    downloadSubscriptionPackage(pack)
    toast.success('订阅已生成，导入包已下载', rule.status === 'active' ? '可在订阅管理查看固定地址和二维码，重复点击复用同一订阅' : '规则暂停、额度用完或尚未就绪时暂不下发线路；恢复后更新客户端订阅')
  } catch (cause) {
    if (isUnauthorized(cause)) invalidateSession(); else actionError.value = displayError(cause)
  } finally { busyId.value = '' }
}
async function exportRules(): Promise<void> {
  if (!ready.value || exportBusy.value || batchBusy.value || busyId.value) return
  exportBusy.value = true; actionError.value = ''
  try {
    const document = await businessApi.exportRules()
    const blob = new Blob([`${JSON.stringify(document, null, 2)}\n`], { type: 'application/json;charset=utf-8' })
    const url = URL.createObjectURL(blob)
    const anchor = window.document.createElement('a')
    anchor.href = url; anchor.download = `forwarding-rules-${new Date().toISOString().slice(0, 10).replaceAll('-', '')}.json`; anchor.hidden = true; window.document.body.append(anchor); anchor.click(); anchor.remove()
    URL.revokeObjectURL(url)
    toast.success('规则已导出', `${document.rules.length} 条规则`)
  } catch (cause) { if (isUnauthorized(cause)) invalidateSession(); else actionError.value = displayError(cause) }
  finally { exportBusy.value = false }
}
onMounted(async () => {
  document.addEventListener('visibilitychange', handleTrafficVisibilityChange)
  await load()
  scheduleTrafficRefresh()
})
onUnmounted(() => {
  loadVersion++
  document.removeEventListener('visibilitychange', handleTrafficVisibilityChange)
  if (trafficTimer !== undefined) window.clearTimeout(trafficTimer)
})
</script>
<template>
  <div class="page-stack">
    <header class="page-heading"><h2>转发规则</h2><div class="page-heading__actions"><RouterLink class="button button--secondary" to="/rule-groups"><FolderTree :size="15" />规则分组</RouterLink><button class="button button--secondary" :disabled="loading || !!busyId || batchBusy || exportBusy" @click="exportRules"><Download :size="15" />{{ exportBusy ? '导出中' : '导出' }}</button><button class="button button--secondary" :disabled="loading || !!busyId || batchBusy || exportBusy" @click="transferDialog = true"><Upload :size="15" />导入</button><button class="button button--secondary" :disabled="loading || !!busyId || batchBusy || exportBusy" @click="load"><RefreshCw :size="16" :class="{ spin: loading }" />刷新</button><button class="button button--primary" :disabled="loading || !!busyId || batchBusy || exportBusy" @click="startCreate"><Plus :size="16" />添加规则</button></div></header>
    <section class="toolbar business-toolbar"><label class="search-field"><Search :size="17" /><input v-model="query" placeholder="搜索规则、目标或设备组" /></label><label class="toolbar-select"><span>规则分组</span><select v-model="groupFilter"><option value="all">全部分组</option><option value="ungrouped">未分组</option><option v-for="group in ruleGroups" :key="group.id" :value="group.id">{{ group.name }}</option></select></label><div class="segmented-control" aria-label="规则开关筛选"><button :class="{ active: filter === 'all' }" @click="filter = 'all'">全部 {{ rules.length }}</button><button :class="{ active: filter === 'enabled' }" @click="filter = 'enabled'">已启用</button><button :class="{ active: filter === 'paused' }" @click="filter = 'paused'">已暂停</button></div></section>
    <section v-if="ready && rules.length" class="rule-batch-toolbar" aria-label="规则批量操作"><label><input type="checkbox" :checked="allFilteredSelected" :disabled="batchBusy || !!busyId" @change="selectFiltered(($event.target as HTMLInputElement).checked)" />选择当前结果</label><span>{{ selectedIds.length }} 条已选择</span><button class="button button--secondary" :disabled="!selectedIds.length || batchBusy || !!busyId" @click="batch('pause')"><Pause :size="14" />暂停</button><button class="button button--secondary" :disabled="!selectedIds.length || batchBusy || !!busyId" @click="batch('resume')"><Play :size="14" />恢复</button><label class="rule-batch-move"><select v-model="moveGroupId" :disabled="batchBusy || !!busyId"><option value="">未分组</option><option v-for="group in ruleGroups" :key="group.id" :value="group.id">{{ group.name }}</option></select><button class="button button--secondary" :disabled="!selectedIds.length || batchBusy || !!busyId" @click="batch('move_group')"><FolderInput :size="14" />移动</button></label><button class="button button--danger" :disabled="!selectedIds.length || batchBusy || !!busyId" @click="requestDelete([...selectedRules])"><Trash2 :size="14" />删除</button></section>
    <p v-if="actionError" class="form-error" role="alert">{{ actionError }}</p>
    <p v-if="resourceWarning && ready" class="inline-warning" role="status">{{ resourceWarning }}</p>
    <StatePanel v-if="loading" state="loading" title="正在读取转发规则" />
    <StatePanel v-else-if="error" state="error" title="规则加载失败" :message="error" @retry="load" />
    <StatePanel v-else-if="!rules.length" state="empty" title="暂无转发规则" message="点击添加规则创建。" />
    <StatePanel v-else-if="!filtered.length" state="empty" title="没有匹配的规则" message="调整搜索词或开关筛选。" />
    <ForwardRuleInventory v-else :rules="pagedRules" :devices="devices" :networks="networks" :traffic-by-rule="trafficByRule" :rule-groups="ruleGroups" :selected-ids="selectedIds" :busy-id="busyId || (batchBusy ? 'batch' : '')" @select="selectRule" @edit="openExisting($event)" @copy="openExisting($event, true)" @connection="copyConnection" @toggle="toggle" @delete="requestDelete([$event])" />
    <nav v-if="ready && filtered.length" class="rule-pagination" aria-label="转发规则分页">
      <span class="rule-pagination__summary">显示 {{ pageStart }}-{{ pageEnd }}，共 {{ filtered.length }} 条</span>
      <label class="rule-pagination__size"><span>每页</span><select v-model.number="pageSize" aria-label="每页规则数量"><option v-for="size in pageSizeOptions" :key="size" :value="size">{{ size }}</option></select><span>条</span></label>
      <div class="rule-pagination__buttons"><button class="button button--secondary" type="button" :disabled="page <= 1" @click="page--">上一页</button><span>第 {{ page }} / {{ totalPages }} 页</span><button class="button button--secondary" type="button" :disabled="page >= totalPages" @click="page++">下一页</button></div>
    </nav>
    <ForwardRuleEditor v-if="editor" :key="`${selected?.id ?? 'new'}:${copying ? 'copy' : 'edit'}`" :rule="selected" :copy="copying" :devices="devices" :networks="networks" :rule-groups="ruleGroups" @close="closeEditor" @saved="saved" @unauthorized="invalidateSession" />
    <ConfirmRuleDeletion v-if="deletionCandidates.length" :rules="deletionCandidates" :busy="batchBusy" @close="deletionCandidates = []" @confirm="confirmDelete" />
    <ForwardRuleTransferDialog v-if="transferDialog" :devices="devices" :networks="networks" :rule-groups="ruleGroups" @close="transferDialog = false" @imported="imported" @unauthorized="invalidateSession" />
  </div>
</template>

<style scoped>
.rule-pagination { display: flex; min-height: 48px; align-items: center; justify-content: space-between; gap: 14px; padding: 8px 12px; border: 1px solid var(--ny-border); border-radius: 4px; background: #fff; color: var(--ny-muted); font-size: 12px; }
.rule-pagination__summary { white-space: nowrap; }
.rule-pagination__size, .rule-pagination__buttons { display: inline-flex; align-items: center; gap: 7px; }
.rule-pagination__size select { min-height: 30px; padding: 4px 25px 4px 8px; border: 1px solid var(--ny-border-dark); border-radius: 3px; background: #fff; color: var(--ny-text); }
.rule-pagination__buttons .button { min-height: 30px; padding: 4px 9px; }
@media (max-width: 640px) {
  .rule-pagination { align-items: stretch; flex-direction: column; }
  .rule-pagination__summary, .rule-pagination__size, .rule-pagination__buttons { justify-content: space-between; }
  .rule-pagination__buttons .button { flex: 1; }
}
</style>
