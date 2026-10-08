<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { Copy, KeyRound, Plus, RefreshCw, RotateCcw, ShieldOff } from '@lucide/vue'
import { api, type EndpointPool } from '@/api'
import { businessApi, type Customer, type ForwardRule, type VlessIdentity } from '@/api/business'
import StatePanel from '@/components/StatePanel.vue'
import { toast } from '@/composables/toast'
import { useMutationKey } from '@/composables/useMutationKey'
import { displayError } from '@/lib/displayFormatters'

const identities = ref<VlessIdentity[]>([])
const rules = ref<ForwardRule[]>([])
const pools = ref<EndpointPool[]>([])
const customers = ref<Customer[]>([])
const loading = ref(true)
const error = ref('')
const actionError = ref('')
const formOpen = ref(false)
const ruleID = ref('')
const poolID = ref('')
const busyID = ref('')
const mutationKey = useMutationKey()

const ruleByID = computed(() => new Map(rules.value.map((rule) => [rule.id, rule])))
const poolByID = computed(() => new Map(pools.value.map((pool) => [pool.id, pool])))
const customerByID = computed(() => new Map(customers.value.map((customer) => [customer.id, customer])))
const selectedRule = computed(() => ruleByID.value.get(ruleID.value) ?? null)
const availableRules = computed(() => rules.value.filter((rule) => rule.ingress_protocol === 'vless_reality' && rule.protocol === 'tcp' && rule.vless_flow === 'xtls-rprx-vision' && !rule.paused && rule.ingress_status !== 'pending_reality_parameters' && rule.status === 'active' && rule.deployed === true))
const availablePools = computed(() => pools.value.filter((pool) => pool.protocol === 'vless' && !!selectedRule.value && pool.rule_id === selectedRule.value.id))
const rows = computed(() => identities.value.map((identity) => ({
  identity,
  rule: ruleByID.value.get(identity.forwarding_rule_id),
  pool: poolByID.value.get(identity.endpoint_pool_id),
  customer: customerByID.value.get(identity.customer_id),
})))

async function load(): Promise<void> {
  loading.value = true; error.value = ''; actionError.value = ''
  const results = await Promise.allSettled([businessApi.vlessIdentities(), businessApi.rules(), api.getEndpointPools(), businessApi.customers()])
  const [identityResult, ruleResult, poolResult, customerResult] = results
  if (identityResult.status === 'fulfilled') identities.value = identityResult.value.items
  if (ruleResult.status === 'fulfilled') rules.value = ruleResult.value.items
  if (poolResult.status === 'fulfilled') pools.value = poolResult.value.items
  if (customerResult.status === 'fulfilled') customers.value = customerResult.value.items
  const failed = results.slice(0, 3).find((result) => result.status === 'rejected')
  if (failed?.status === 'rejected') error.value = displayError(failed.reason)
  loading.value = false
}

function openCreate(): void {
  actionError.value = ''; ruleID.value = ''; poolID.value = ''; formOpen.value = true
}
function selectRule(): void {
  if (!availablePools.value.some((pool) => pool.id === poolID.value)) poolID.value = availablePools.value[0]?.id ?? ''
}
function closeCreate(): void { formOpen.value = false }
async function create(): Promise<void> {
  const rule = selectedRule.value
  if (!rule || !poolID.value) { actionError.value = '请选择可用的 VLESS 规则和服务端点'; return }
  busyID.value = 'create'; actionError.value = ''
  try {
    await businessApi.provisionVlessIdentity({ customer_id: rule.customer_id, forwarding_rule_id: rule.id, endpoint_pool_id: poolID.value }, mutationKey({ operation: 'provision', rule_id: rule.id, pool_id: poolID.value }))
    toast.success('VLESS 凭据已创建', rule.name)
    closeCreate(); await load()
  } catch (cause) { actionError.value = displayError(cause) } finally { busyID.value = '' }
}
async function copy(row: (typeof rows.value)[number]): Promise<void> {
  if (!row.rule || busyID.value) return
  busyID.value = row.identity.id; actionError.value = ''
  try {
    const connection = await businessApi.vlessIdentityConnection(row.identity.id)
    await navigator.clipboard.writeText(connection.uri)
    toast.success('VLESS 凭据链接已复制', connection.endpoint)
  } catch (cause) { actionError.value = displayError(cause) } finally { busyID.value = '' }
}
async function rotate(identity: VlessIdentity): Promise<void> {
  if (busyID.value || !window.confirm('轮换后旧VLESS 凭据链接会立即失效，确定继续吗？')) return
  busyID.value = identity.id; actionError.value = ''
  try { await businessApi.rotateVlessIdentity(identity.id, identity.revision, mutationKey({ operation: 'rotate', id: identity.id, revision: identity.revision })); toast.success('VLESS 凭据已轮换'); await load() }
  catch (cause) { actionError.value = displayError(cause) } finally { busyID.value = '' }
}
async function revoke(identity: VlessIdentity): Promise<void> {
  if (busyID.value || !window.confirm('撤销后此VLESS 凭据将不能恢复，确定继续吗？')) return
  busyID.value = identity.id; actionError.value = ''
  try { await businessApi.revokeVlessIdentity(identity.id, identity.revision, mutationKey({ operation: 'revoke', id: identity.id, revision: identity.revision })); toast.success('VLESS 凭据已撤销'); await load() }
  catch (cause) { actionError.value = displayError(cause) } finally { busyID.value = '' }
}
function stateLabel(state: string): string { return state === 'active' ? '有效' : state === 'revoked' ? '已撤销' : state }
onMounted(load)
</script>

<template>
  <div class="page-stack subscription-page">
    <header class="page-heading"><div><h2>VLESS 凭据管理</h2><p>为 VLESS 规则签发、复制、轮换和撤销连接链接</p></div><div class="page-heading__actions"><button class="button button--secondary" type="button" :disabled="loading || !!busyID" @click="load"><RefreshCw :size="16" :class="{ spin: loading }" />刷新</button><button class="button button--primary" type="button" :disabled="loading || !!busyID || !availableRules.length" @click="openCreate"><Plus :size="16" />创建VLESS 凭据</button></div></header>
    <p v-if="actionError" class="form-error" role="alert">{{ actionError }}</p>
    <StatePanel v-if="loading" state="loading" title="正在读取VLESS 凭据" />
    <StatePanel v-else-if="error && !identities.length" state="error" title="VLESS 凭据加载失败" :message="error" @retry="load" />
    <StatePanel v-else-if="!identities.length" state="empty" title="还没有VLESS 凭据" message="先创建一条已配置端点的 VLESS 规则，再签发VLESS 凭据。" />
    <div v-else class="business-inventory subscription-inventory"><div class="table-wrap"><table class="data-table business-table"><thead><tr><th>VLESS 凭据</th><th>关联规则</th><th>服务端点</th><th>状态</th><th>更新时间</th><th>操作</th></tr></thead><tbody><tr v-for="row in rows" :key="row.identity.id"><td><strong class="table-primary"><KeyRound :size="14" />{{ row.rule?.name ?? row.identity.id }}</strong><small class="table-secondary">{{ row.customer?.display_name || row.customer?.username || row.identity.customer_id }}</small></td><td>{{ row.rule?.name || row.identity.forwarding_rule_id }}</td><td>{{ row.pool ? `${row.pool.hostname}:${row.pool.port}` : row.identity.endpoint_pool_id }}</td><td><span class="business-status" :class="{ 'business-status--muted': row.identity.state !== 'active' }">{{ stateLabel(row.identity.state) }}</span></td><td>{{ new Date(row.identity.updated_at).toLocaleString() }}</td><td><div class="subscription-actions"><button class="button button--quiet" type="button" :disabled="row.identity.state !== 'active' || !!busyID || !row.rule" title="复制VLESS 凭据链接" @click="copy(row)"><Copy :size="14" />复制</button><button class="button button--quiet" type="button" :disabled="row.identity.state !== 'active' || !!busyID" title="轮换VLESS 凭据" @click="rotate(row.identity)"><RotateCcw :size="14" />轮换</button><button class="button button--quiet subscription-danger" type="button" :disabled="row.identity.state !== 'active' || !!busyID" title="撤销VLESS 凭据" @click="revoke(row.identity)"><ShieldOff :size="14" />撤销</button></div></td></tr></tbody></table></div></div>
    <p v-if="error && identities.length" class="inline-warning">刷新失败，保留上次数据：{{ error }}</p>
    <div v-if="formOpen" class="modal-backdrop" @click.self="closeCreate"><section class="modal" role="dialog" aria-modal="true" aria-labelledby="subscription-create-title"><header class="modal__header"><div><h2 id="subscription-create-title">创建VLESS 凭据</h2><p>只可为已具备 Reality 参数的 VLESS 规则签发。</p></div><button class="icon-button" type="button" aria-label="关闭" @click="closeCreate">×</button></header><form class="modal__body form-stack" @submit.prevent="create"><label class="field"><span>VLESS 规则</span><select v-model="ruleID" required @change="selectRule"><option value="" disabled>请选择规则</option><option v-for="rule in availableRules" :key="rule.id" :value="rule.id">{{ rule.name }} · {{ rule.listen_port }}</option></select></label><label class="field"><span>服务端点</span><select v-model="poolID" required><option value="" disabled>请选择端点</option><option v-for="pool in availablePools" :key="pool.id" :value="pool.id">{{ pool.name }} · {{ pool.hostname }}:{{ pool.port }}</option></select></label><p v-if="!availableRules.length" class="form-note">当前没有可签发的 VLESS 规则。请先在转发规则中完成 Reality 参数和端点配置。</p><div class="modal__footer"><button class="button button--secondary" type="button" :disabled="!!busyID" @click="closeCreate">取消</button><button class="button button--primary" type="submit" :disabled="!!busyID || !ruleID || !poolID">{{ busyID === 'create' ? '创建中' : '创建VLESS 凭据' }}</button></div></form></section></div>
  </div>
</template>

<style scoped>
.subscription-inventory .data-table { min-width: 980px; }
.subscription-inventory .table-primary { display: flex; align-items: center; gap: 7px; }
.subscription-actions { display: flex; flex-wrap: wrap; gap: 4px; }
.subscription-actions .button { min-height: 29px; padding: 4px 8px; }
.subscription-danger { color: var(--red-700); }
</style>
