<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { Copy, Download, Plus, QrCode, RefreshCw, RotateCcw, Send } from '@lucide/vue'
import { businessApi, type Customer, type ForwardRule, type VlessIdentity } from '@/api/business'
import { subscriptionApi, type Subscription, type SubscriptionDetail, type SubscriptionInput } from '@/api/subscriptions'
import VlessCredentials from '@/components/VlessCredentials.vue'
import SubscriptionInventory from '@/features/subscriptions/SubscriptionInventory.vue'
import StatePanel from '@/components/StatePanel.vue'
import { toast } from '@/composables/toast'
import { useMutationKey } from '@/composables/useMutationKey'
import { displayError } from '@/lib/displayFormatters'

const tab = ref('subscriptions')
const items = ref<Subscription[]>([]), customers = ref<Customer[]>([]), identities = ref<VlessIdentity[]>([]), rules = ref<ForwardRule[]>([])
const loading = ref(true), error = ref(''), actionError = ref(''), busy = ref('')
const query = ref(''), status = ref(''), page = ref(1), pageSize = ref(20)
const formOpen = ref(false), detail = ref<SubscriptionDetail | null>(null), editingID = ref<string | null>(null)
const form = ref<SubscriptionInput>({ name: '', customer_id: '', revision: 0, lines: [] })
const mutationKey = useMutationKey()
const qrImage = ref(''), qrFormat = ref<'txt' | 'yaml'>('txt')
const customerNames = computed(() => new Map(customers.value.map(c => [c.id, c.display_name || c.username])))
const ruleNames = computed(() => new Map(rules.value.map(r => [r.id, r.name])))
const availableBindings = computed(() => identities.value.filter(i => i.state === 'active'))
const filtered = computed(() => items.value.filter(i => (!status.value || i.state === status.value) && `${i.name} ${customerNames.value.get(i.customer_id) ?? ''}`.toLowerCase().includes(query.value.trim().toLowerCase())))
const pages = computed(() => Math.max(1, Math.ceil(filtered.value.length / pageSize.value)))
const visible = computed(() => filtered.value.slice((page.value - 1) * pageSize.value, page.value * pageSize.value))
const published = computed(() => !!detail.value?.subscription.published_revision && detail.value.subscription.state === 'active')
async function load() {
  loading.value = true; error.value = ''
  const result = await Promise.allSettled([subscriptionApi.list(), businessApi.customers(), businessApi.vlessIdentities(), businessApi.rules()])
  if (result[0].status === 'fulfilled') { items.value = result[0].value.items; page.value = Math.min(page.value, pages.value) }
  if (result[1].status === 'fulfilled') customers.value = result[1].value.items
  if (result[2].status === 'fulfilled') identities.value = result[2].value.items
  if (result[3].status === 'fulfilled') rules.value = result[3].value.items
  const failed = result.find(r => r.status === 'rejected')
  if (failed?.status === 'rejected') error.value = displayError(failed.reason)
  loading.value = false
}
function addLine() { form.value.lines.push({ name: '', uri: '' }) }
function setKind(index: number, event: Event) { const name = form.value.lines[index]!.name; form.value.lines[index] = (event.target as HTMLSelectElement).value === 'native' ? { name, binding_id: '' } : { name, uri: '' } }
function openCreate() { editingID.value = null; form.value = { name: '', customer_id: customers.value[0]?.id ?? '', revision: 0, lines: [{ name: '', uri: '' }] }; actionError.value = ''; formOpen.value = true }
async function inspect(item: Subscription, edit = false) {
  busy.value = item.id; actionError.value = ''; qrImage.value = ''
  try { const value = await subscriptionApi.detail(item.id); if (edit) { editingID.value = item.id; form.value = { name: value.subscription.name, customer_id: value.subscription.customer_id, revision: value.subscription.revision, lines: value.lines.map(l => ({ ...l })) }; formOpen.value = true; detail.value = null } else { detail.value = value } }
  catch (cause) { actionError.value = displayError(cause) } finally { busy.value = '' }
}
async function save() {
  busy.value = 'save'; actionError.value = ''
  try { await subscriptionApi.save(editingID.value, form.value, mutationKey({ id: editingID.value, ...form.value })); formOpen.value = false; toast.success('草稿已保存', '点击“发布更新”后客户端才会获取新线路，订阅地址保持不变'); await load() }
  catch (cause) { actionError.value = displayError(cause) } finally { busy.value = '' }
}
async function action(item: Subscription, operation: 'publish' | 'rotate' | 'revoke' | 'restore') {
  const warning = operation === 'rotate' ? '更换订阅地址后旧地址立即失效，客户需要重新导入。是否继续？' : operation === 'revoke' ? '停用后订阅地址将不可访问，已导入的节点配置不会自动从客户端删除。可随时恢复。是否继续？' : ''
  if (warning && !window.confirm(warning)) return
  busy.value = item.id; actionError.value = ''; qrImage.value = ''
  try { await subscriptionApi.action(item, operation, mutationKey({ id: item.id, revision: item.revision, operation })); toast.success(({ publish: '订阅已发布，地址保持不变', rotate: '订阅地址已更换', revoke: '订阅已停用', restore: '订阅已恢复' })[operation]); await load(); if (detail.value?.subscription.id === item.id) detail.value = await subscriptionApi.detail(item.id) }
  catch (cause) { actionError.value = displayError(cause) } finally { busy.value = '' }
}
function fullURL(path: string) { return new URL(path, location.origin).href }
async function copyLink(kind: 'txt' | 'yaml') { if (!detail.value) return; try { await navigator.clipboard.writeText(fullURL(detail.value.links[kind])); toast.success('订阅地址已复制') } catch (cause) { actionError.value = displayError(cause) } }
async function showQR(kind: 'txt' | 'yaml') {
  if (!detail.value) return
  busy.value = 'qr'; actionError.value = ''; qrImage.value = ''
  try { const result = await subscriptionApi.qrcode(detail.value.subscription.id, kind); qrFormat.value = kind; qrImage.value = `data:image/png;base64,${result.png_base64}` }
  catch (cause) { actionError.value = displayError(cause) } finally { busy.value = '' }
}
async function download(item: Subscription) {
  busy.value = item.id; actionError.value = ''; qrImage.value = ''
  try { const pack = await subscriptionApi.package(item.id); const bytes = Uint8Array.from(atob(pack.data_base64), c => c.charCodeAt(0)); const url = URL.createObjectURL(new Blob([bytes], { type: 'application/zip' })); const a = document.createElement('a'); a.href = url; a.download = pack.filename; a.click(); URL.revokeObjectURL(url); toast.success('导入包已下载', '包含 YAML、订阅地址和二维码') }
  catch (cause) { actionError.value = displayError(cause) } finally { busy.value = '' }
}
onMounted(load)
</script>

<template>
  <div class="page-stack subscription-page">
    <header class="page-heading"><div><h2>订阅管理</h2><p>固定订阅地址 · 多线路 · TXT / YAML · 导入包与二维码</p></div><div class="page-heading__actions"><button class="button button--secondary" :disabled="loading || !!busy" @click="load"><RefreshCw :size="16" />刷新</button><button v-if="tab === 'subscriptions'" class="button button--primary" :disabled="loading || !!busy || !customers.length" @click="openCreate"><Plus :size="16" />创建订阅</button></div></header>
    <nav class="subscription-tabs" aria-label="订阅分类"><button :class="{ active: tab === 'subscriptions' }" @click="tab = 'subscriptions'">订阅地址</button><button :class="{ active: tab === 'credentials' }" @click="tab = 'credentials'">VLESS 凭据</button></nav>
    <VlessCredentials v-if="tab === 'credentials'" />
    <template v-else>
      <p v-if="actionError" class="form-error" role="alert">{{ actionError }}</p>
      <p v-if="error && items.length" class="inline-warning">{{ error }}</p>
      <div class="subscription-filters"><label class="field"><span>搜索订阅 / 客户</span><input v-model="query" placeholder="名称或客户" @input="page = 1" /></label><label class="field"><span>状态</span><select v-model="status" @change="page = 1"><option value="">全部状态</option><option value="active">启用</option><option value="revoked">已停用</option></select></label></div>
      <StatePanel v-if="loading" state="loading" title="正在读取订阅" />
      <StatePanel v-else-if="error && !items.length" state="error" title="订阅加载失败" :message="error" @retry="load" />
      <StatePanel v-else-if="!items.length" state="empty" title="还没有订阅" message="选择客户，添加 VLESS 凭据或 SOCKS5 / VLESS 链接，保存后发布。没有客户时请先创建客户。" />
      <StatePanel v-else-if="!filtered.length" state="empty" title="没有匹配的订阅" />
      <SubscriptionInventory v-else :items="visible" :customer-names="customerNames" :busy="!!busy" @inspect="inspect" @publish="action($event, 'publish')" @download="download" @toggle="action($event, $event.state === 'revoked' ? 'restore' : 'revoke')" />
      <div class="subscription-pagination"><span>共 {{ filtered.length }} 个订阅</span><label>每页 <select v-model.number="pageSize" aria-label="每页订阅数量" @change="page = 1"><option v-for="size in [10, 20, 50, 100]" :key="size" :value="size">{{ size }}</option></select></label><button class="button button--secondary" :disabled="page <= 1" @click="page--">上一页</button><span>{{ page }} / {{ pages }}</span><button class="button button--secondary" :disabled="page >= pages" @click="page++">下一页</button></div>
    </template>
    <div v-if="formOpen" class="modal-backdrop" @click.self="!busy && (formOpen = false)"><section class="modal subscription-modal" role="dialog" aria-modal="true" aria-labelledby="subscription-form-title"><header class="modal__header"><div><h2 id="subscription-form-title">{{ editingID ? '编辑订阅草稿' : '创建订阅' }}</h2><p>保存后点击“发布更新”，原订阅地址保持不变。</p></div><button class="icon-button" aria-label="关闭" :disabled="!!busy" @click="formOpen = false">×</button></header><form class="modal__body form-stack" @submit.prevent="save"><p v-if="actionError" class="form-error" role="alert">{{ actionError }}</p><label class="field"><span>订阅名称</span><input v-model="form.name" required maxlength="120" /></label><label class="field"><span>所属客户</span><select v-model="form.customer_id" required :disabled="!!editingID" @change="form.lines = [{ name: '', uri: '' }]"><option value="" disabled>选择客户</option><option v-for="customer in customers" :key="customer.id" :value="customer.id">{{ customer.display_name || customer.username }}</option></select></label><div v-for="(line, index) in form.lines" :key="index" class="subscription-line"><div class="subscription-line-heading"><strong>线路 {{ index + 1 }}</strong><button class="button button--quiet" type="button" :disabled="form.lines.length <= 1 || !!busy" @click="form.lines.splice(index, 1)">移除</button></div><label class="field"><span>线路名称</span><input v-model="line.name" required maxlength="100" placeholder="例如：香港线路" /></label><label class="field"><span>线路来源</span><select :value="line.binding_id !== undefined ? 'native' : 'manual'" @change="setKind(index, $event)"><option value="manual">粘贴 SOCKS5 / VLESS 链接</option><option value="native">本面板 VLESS 凭据</option></select></label><label v-if="line.binding_id !== undefined" class="field"><span>VLESS 凭据</span><select v-model="line.binding_id" required><option value="" disabled>请选择当前管理员的有效凭据</option><option v-for="identity in availableBindings" :key="identity.id" :value="identity.id">{{ ruleNames.get(identity.forwarding_rule_id) || identity.forwarding_rule_id }} · {{ identity.id }}</option></select></label><label v-else class="field"><span>连接链接</span><textarea v-model="line.uri" required rows="3" spellcheck="false" placeholder="socks5://用户名:密码@域名:端口 或 vless://…" /></label></div><button class="button button--secondary" type="button" :disabled="form.lines.length >= 100 || !!busy" @click="addLine"><Plus :size="16" />添加线路</button><p class="form-note">支持 VLESS TCP（Reality / TLS / 无 TLS）和 SOCKS5。原生 VLESS 凭据会跟随轮换；多位客户引用同一凭据时共享线路账号。手工链接需自行维护。客户到期或停用后，订阅停止下发。</p><div class="modal__footer"><button class="button button--secondary" type="button" :disabled="!!busy" @click="formOpen = false">取消</button><button class="button button--primary" type="submit" :disabled="!!busy">{{ busy === 'save' ? '保存中' : '保存草稿' }}</button></div></form></section></div>
    <div v-if="detail" class="modal-backdrop" @click.self="detail = null"><section class="modal subscription-modal" role="dialog" aria-modal="true" aria-labelledby="subscription-detail-title"><header class="modal__header"><div><h2 id="subscription-detail-title">{{ detail.subscription.name }}</h2><p>{{ detail.subscription.pending_update ? '有未发布的修改，客户端仍使用上次发布内容。' : '客户端通过固定地址拉取已发布线路。' }}</p></div><button class="icon-button" aria-label="关闭" @click="detail = null">×</button></header><div class="modal__body form-stack"><p v-if="actionError" class="form-error" role="alert">{{ actionError }}</p><p v-if="detail.warning" class="inline-warning">{{ detail.warning }}</p><p v-if="!published" class="inline-warning">{{ detail.subscription.state === 'revoked' ? '订阅已停用，恢复后才能拉取。' : '订阅尚未发布，请先发布更新。' }}</p><label v-for="kind in (['txt', 'yaml'] as const)" :key="kind" class="field"><span>{{ kind === 'txt' ? 'v2rayN / Shadowrocket · TXT' : 'Clash / Mihomo · YAML' }}</span><div class="subscription-link"><input :value="fullURL(detail.links[kind])" readonly :aria-label="`${kind.toUpperCase()} 订阅地址`" /><button class="button button--secondary" :disabled="!published" @click="copyLink(kind)"><Copy :size="14" />复制</button></div></label><p class="form-note">订阅地址是访问凭据，请勿公开。域名切换后需将新地址重新导入客户端；日常编辑和发布不会改变地址中的令牌。</p><div class="subscription-actions"><button v-for="kind in (['txt', 'yaml'] as const)" :key="kind" class="button button--secondary" :disabled="!!busy || !published" @click="showQR(kind)"><QrCode :size="14" />{{ kind.toUpperCase() }} 二维码</button></div><figure v-if="qrImage" class="subscription-qr"><img :src="qrImage" :alt="`${qrFormat.toUpperCase()} 订阅二维码`" width="256" height="256" /><figcaption>{{ qrFormat === 'txt' ? 'v2rayN / Shadowrocket' : 'Clash / Mihomo' }} 订阅地址 · 请勿公开</figcaption></figure><h3>已发布线路</h3><div v-if="!detail.published_preview?.length" class="form-note">暂无已发布线路</div><div v-for="line in detail.published_preview" :key="line.name" class="subscription-preview"><strong>{{ line.name }}</strong><span v-if="line.error" class="form-error">{{ line.error }}</span><details v-else><summary>查看连接链接</summary><code>{{ line.uri }}</code></details></div><h3>草稿线路</h3><div v-for="line in detail.preview" :key="line.name" class="subscription-preview"><strong>{{ line.name }}</strong><span>{{ line.error || '参数就绪' }}</span></div><div class="subscription-actions"><button class="button button--primary" :disabled="!!busy || !detail.subscription.pending_update || detail.subscription.state !== 'active'" @click="action(detail.subscription, 'publish')"><Send :size="14" />发布更新</button><button class="button button--secondary" :disabled="!!busy || !published" @click="download(detail.subscription)"><Download :size="14" />导入包（含二维码）</button><button class="button button--secondary" :disabled="!!busy || detail.subscription.state !== 'active'" @click="action(detail.subscription, 'rotate')"><RotateCcw :size="14" />更换订阅地址</button></div></div></section></div>
  </div>
</template>

<style scoped>
.subscription-tabs { display: flex; gap: 6px; border-bottom: 1px solid var(--line, #e2e8f0); }
.subscription-tabs button { padding: 12px 18px; border: 0; border-bottom: 2px solid transparent; background: transparent; color: var(--text-secondary, #64748b); cursor: pointer; }
.subscription-tabs button.active { border-bottom-color: var(--blue-700, #1e40af); color: var(--blue-700, #1e40af); }
.subscription-filters { display: flex; gap: 16px; align-items: end; }
.subscription-filters .field:first-child { flex: 1; max-width: 450px; }
.subscription-actions { display: flex; flex-wrap: wrap; gap: 5px; }
.subscription-actions .button { min-height: 30px; padding: 5px 8px; white-space: nowrap; }
.subscription-pagination { display: flex; align-items: center; gap: 12px; flex-wrap: wrap; justify-content: flex-end; }
.subscription-pagination select { border: 1px solid #cbd5e1; border-radius: 6px; padding: 5px; background: white; }
.subscription-modal { width: min(760px, calc(100vw - 28px)); }
.subscription-line { padding: 16px; border: 1px solid #e2e8f0; border-radius: 10px; display: grid; gap: 12px; background: #f8fafc; }
.subscription-line-heading { display: flex; align-items: center; justify-content: space-between; }
.subscription-link { display: flex; gap: 8px; }
.subscription-link input { flex: 1; min-width: 0; }
.subscription-qr { margin: 0; text-align: center; }
.subscription-qr img { width: min(256px, 100%); height: auto; border: 1px solid #e2e8f0; border-radius: 8px; }
.subscription-qr figcaption { font-size: 13px; color: #64748b; margin-top: 6px; }
.subscription-preview { display: grid; gap: 8px; padding: 12px; border: 1px solid #e2e8f0; border-radius: 8px; }
.subscription-preview code { display: block; overflow-wrap: anywhere; white-space: pre-wrap; margin-top: 8px; }
@media (max-width: 640px) { .subscription-filters { flex-direction: column; align-items: stretch; } .subscription-filters .field:first-child { max-width: none; } .subscription-pagination { justify-content: flex-start; gap: 8px; } .subscription-link { flex-wrap: wrap; } .subscription-link input { flex-basis: 100%; } }
</style>
