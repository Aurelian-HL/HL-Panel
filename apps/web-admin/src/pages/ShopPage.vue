<script setup lang="ts">
import { computed, onMounted, onUnmounted, ref } from 'vue'
import { ArrowUpRight, Check, Gift, KeyRound, LoaderCircle, RefreshCw, ShieldCheck, ShoppingBag } from '@lucide/vue'
import { panelLicenseApi, type LicenseShopProduct } from '@/api/panelLicense'
import { ApiError } from '@/api/http'
import { panelLicenseStore } from '@/stores/panelLicense'

const products = ref<LicenseShopProduct[]>([])
const loading = ref(true)
const catalogError = ref('')
const catalogEnabled = ref(false)
const catalogMessage = ref('')
const code = ref('')
const working = ref(false)
const formError = ref('')
const success = ref('')
const recoverySuggested = ref(false)
const status = panelLicenseStore.status
const statusError = panelLicenseStore.error
const pending = computed(() => Boolean(status.value?.redemption_pending) || recoverySuggested.value)
const canRecover = computed(() => Boolean(status.value?.enabled) && status.value?.status !== 'unconfigured')
const canRedeem = computed(() => canRecover.value && catalogEnabled.value && !loading.value)
let mounted = true
const current = (generation: number) => mounted && generation === panelLicenseStore.generation()

const statusLabel = computed(() => {
  const value = status.value
  if (!value) return statusError.value ? '授权状态暂未读到' : '正在读取授权'
  if (!value.enabled || value.status === 'unconfigured') return '授权服务未配置'
  const labels: Record<string, string> = { expired: '授权已到期', revoked: '授权已停用', stale: '授权待同步', unactivated: '等待激活', upgrade_required: '授权待同步' }
  return labels[value.status] ?? (value.tier === 'free' ? '免费版' : '正式授权')
})
const expiryLabel = computed(() => {
  const value = status.value
  if (!value?.license_id) return '激活后生效'
  if (!value.expires_at) return value.term?.kind === 'perpetual' ? '长期有效' : '等待同步'
  const date = new Date(value.expires_at)
  return Number.isNaN(date.valueOf()) ? '等待同步' : new Intl.DateTimeFormat('zh-CN', { dateStyle: 'medium', timeStyle: 'short', timeZone: 'Asia/Shanghai' }).format(date)
})
function termLabel(product: LicenseShopProduct): string {
  if (product.term.kind === 'perpetual') return '一次激活 · 长期有效'
  if (product.term.kind === 'calendar_months') return product.term.months === 12 ? '12 个自然月' : product.term.months + ' 个自然月'
  if (product.term.kind === 'days') return product.term.days + ' 天'
  return '以商品说明为准'
}
function purchaseURL(value?: string): string | undefined {
  if (!value || /[\r\n\t]/.test(value)) return undefined
  try {
    const url = new URL(value)
    if (url.protocol === 'https:' && !url.username && !url.password) return url.href
  } catch { /* Unusable purchase links stay disabled. */ }
  return undefined
}
function friendlyError(error: unknown): string {
  if (error instanceof ApiError) {
    if (['mutation_status_unknown', 'invalid_response', 'service_unavailable', 'network_error', 'request_timeout'].includes(error.code)) {
      return '兑换结果暂未确认，请点击“恢复兑换结果”核对，无需重复填写兑换码。'
    }
    return error.message
  }
  return '兑换结果暂未确认，请稍后恢复兑换结果。'
}
async function loadCatalog(): Promise<void> {
  if (!mounted) return
  const generation = panelLicenseStore.generation()
  loading.value = true
  catalogError.value = ''
  catalogMessage.value = ''
  catalogEnabled.value = false
  try {
    const result = await panelLicenseApi.shop()
    if (current(generation)) {
      products.value = result.items
      catalogEnabled.value = result.redemption_enabled === true
      catalogMessage.value = result.redemption_enabled ? '' : '商店尚未开通，暂不支持购买或兑换。'
    }
  } catch {
    if (current(generation)) catalogError.value = '商品暂时无法读取，请稍后重试。已有待确认的兑换仍可恢复结果。'
  } finally {
    if (current(generation)) loading.value = false
  }
}
async function redeem(resume = false): Promise<void> {
  if (working.value || !(resume ? canRecover.value : canRedeem.value)) return
  formError.value = ''; success.value = ''
  const submitted = code.value.trim()
  if (!resume && !/^hlr1_[A-Za-z0-9_-]{32}$/.test(submitted)) {
    formError.value = '请填写购买获得的完整兑换码（hlr1_ 开头）。'
    return
  }
  const generation = panelLicenseStore.generation()
  working.value = true
  code.value = ''
  try {
    const result = resume ? await panelLicenseApi.resumeRedemption() : await panelLicenseApi.redeem(submitted)
    if (current(generation) && panelLicenseStore.apply(result, generation)) {
      recoverySuggested.value = Boolean(result.redemption_pending)
      success.value = resume ? '兑换结果已同步，请查看上方授权状态。' : '兑换成功，授权已生效。'
    }
  } catch (error) {
    if (current(generation)) {
      formError.value = friendlyError(error)
      recoverySuggested.value = !(error instanceof ApiError) || ['mutation_status_unknown', 'invalid_response', 'service_unavailable', 'network_error', 'request_timeout', 'license_service_unavailable', 'license_shop_unavailable', 'redemption_result_pending', 'redemption_pending', 'https_not_verified'].includes(error.code)
      await panelLicenseStore.refresh()
      if (current(generation) && status.value?.redemption_pending) recoverySuggested.value = true
    }
  } finally {
    if (current(generation)) working.value = false
  }
}
onMounted(() => { void loadCatalog(); void panelLicenseStore.refresh() })
onUnmounted(() => { mounted = false; code.value = '' })
</script>

<template>
  <div class="page-stack shop-page">
    <header class="page-heading shop-heading">
      <div><h2>商店</h2><p>选择授权方案，购买兑换码后在这里激活</p></div>
      <span class="shop-heading-mark"><ShoppingBag :size="18" />面板授权</span>
    </header>

    <section class="shop-status" aria-label="当前授权">
      <div class="shop-status-identity"><span class="shop-icon"><ShieldCheck :size="24" :stroke-width="1.6" /></span><div><span class="shop-eyebrow">当前授权</span><h3>{{ statusLabel }}</h3></div></div>
      <dl><div><dt>绑定域名</dt><dd>{{ status?.domain || '等待读取' }}</dd></div><div><dt>有效期 · 北京时间</dt><dd>{{ expiryLabel }}</dd></div></dl>
      <button class="button button--secondary shop-refresh" type="button" :disabled="working" @click="panelLicenseStore.refresh()"><RefreshCw :size="14" />刷新状态</button>
      <p v-if="statusError" class="shop-status-error" role="alert">授权状态暂未读到，请刷新状态后重试。</p>
    </section>

    <section class="shop-offers" aria-label="授权方案" :aria-busy="loading">
      <header class="shop-section-heading"><div><span class="shop-eyebrow">选择适合你的周期</span><h3>授权方案</h3></div><p>有效授权续期时，剩余时长会保留</p></header>
      <div v-if="loading" class="shop-empty" role="status"><LoaderCircle class="shop-spin" :size="22" /><p>正在读取授权方案</p></div>
      <div v-else-if="catalogError" class="shop-empty" role="alert"><p>{{ catalogError }}</p><button type="button" class="button button--secondary" @click="loadCatalog">重新读取商品</button></div>
      <div v-else-if="!products.length" class="shop-empty"><ShoppingBag :size="26" /><h4>商店正在准备中</h4><p>已有兑换码可直接在下方激活。</p></div>
      <div v-else class="shop-product-grid">
        <article v-for="product in products" :key="product.id" class="shop-product" :class="{ 'shop-product--perpetual': product.term.kind === 'perpetual' }">
          <span class="shop-product-badge">{{ product.term.kind === 'perpetual' ? '长期使用' : '灵活选择' }}</span>
          <h4>{{ product.name }}</h4><p class="shop-product-term">{{ termLabel(product) }}</p>
          <p class="shop-price">{{ product.price_label || '价格待公布' }}</p>
          <ul><li><Check :size="14" />正式版管理权限</li><li><Check :size="14" />兑换后绑定本面板</li></ul>
          <a v-if="purchaseURL(product.purchase_url)" class="button shop-buy" :href="purchaseURL(product.purchase_url)" target="_blank" rel="noopener noreferrer" :aria-label="'购买' + product.name">前往购买<ArrowUpRight :size="15" /></a>
          <button v-else class="button button--secondary shop-buy" type="button" disabled>购买入口待开放</button>
        </article>
      </div>
      <p class="shop-purchase-note">购买将在发卡商城完成。请保管好获得的兑换码，每个码只能激活一次。</p>
    </section>

    <section class="shop-redemption" aria-labelledby="shop-redemption-title">
      <div class="shop-redemption-copy"><span class="shop-icon shop-icon--gold"><Gift :size="23" :stroke-width="1.6" /></span><div><h3 id="shop-redemption-title">已有兑换码？</h3><p>在此激活或续期，自动验证当前域名。</p></div></div>
      <div class="shop-redemption-body">
        <p v-if="pending" class="shop-pending" role="status">有一笔兑换结果待确认，恢复即可，无需再次输入兑换码。</p>
        <p v-if="status && !canRecover" class="shop-pending">授权服务尚未配置，请联系面板管理员。</p>
        <p v-else-if="catalogMessage" class="shop-pending" role="status">{{ catalogMessage }}</p>
        <form v-if="!pending" class="shop-redeem-form" @submit.prevent="redeem()">
          <label for="shop-redemption-code">兑换码</label>
          <div class="shop-code-row"><div class="shop-code-input"><KeyRound :size="17" /><input id="shop-redemption-code" v-model="code" type="password" placeholder="输入 hlr1_ 开头的兑换码" autocomplete="off" autocapitalize="off" :spellcheck="false" maxlength="80" :disabled="working || !canRedeem" aria-describedby="shop-code-help" /></div><button class="button shop-redeem-submit" type="submit" :disabled="working || !canRedeem || !code.trim()"><LoaderCircle v-if="working" class="shop-spin" :size="15" />{{ working ? '正在兑换…' : '激活兑换码' }}</button></div>
        </form>
        <button v-else class="button shop-recover" type="button" :disabled="working || !canRecover" @click="redeem(true)"><LoaderCircle v-if="working" class="shop-spin" :size="15" /><RefreshCw v-else :size="15" />{{ working ? '正在恢复…' : '恢复兑换结果' }}</button>
        <p id="shop-code-help" class="shop-code-help">兑换码首次使用后绑定此域名及面板实例。到期后也可续期，已有转发保持运行。</p>
        <p v-if="formError" class="shop-feedback shop-feedback--error" role="alert">{{ formError }}</p>
        <p v-if="success" class="shop-feedback shop-feedback--success" role="status"><Check :size="16" />{{ success }}</p>
      </div>
    </section>
  </div>
</template>

<style scoped>
.shop-page { --shop-navy: #203b58; --shop-gold: #9b7735; gap: 26px; max-width: 1520px; margin: 0 auto; padding-bottom: 24px; }
.shop-heading { align-items: center; }
.shop-heading-mark { display: inline-flex; gap: 8px; align-items: center; font-size: 12px; color: #66758a; }
.shop-status { display: flex; flex-wrap: wrap; align-items: center; gap: 22px 34px; padding: 24px 28px; border: 1px solid #e2e7ee; border-radius: 12px; background: #fff; }
.shop-status-identity { display: flex; align-items: center; gap: 13px; min-width: 170px; }
.shop-icon { display: inline-flex; flex: 0 0 48px; align-items: center; justify-content: center; width: 48px; height: 48px; border-radius: 13px; color: var(--shop-navy); background: #eef3f8; }
.shop-eyebrow { display: block; color: #7b8795; font-size: 11px; letter-spacing: .06em; margin-bottom: 6px; }
.shop-status h3 { margin: 0; font-size: 18px; color: #26384c; font-weight: 600; }
.shop-status dl { display: flex; flex: 1; flex-wrap: wrap; gap: 15px 44px; margin: 0; min-width: 0; }
.shop-status dl > div { min-width: 0; }
.shop-status dt { font-size: 11px; color: #85909d; margin-bottom: 7px; }
.shop-status dd { margin: 0; color: #35465b; font-size: 13px; font-variant-numeric: tabular-nums; overflow-wrap: anywhere; }
.shop-refresh { color: #52657b; font-size: 12px; }
.shop-status-error { flex-basis: 100%; color: #aa4d40; margin: 0; font-size: 12px; }
.shop-section-heading { display: flex; justify-content: space-between; align-items: flex-end; gap: 12px; margin: 0 0 18px; }
.shop-section-heading h3 { margin: 0; font-size: 18px; font-weight: 600; color: #293b50; }
.shop-section-heading p { margin: 0; color: #85909c; font-size: 12px; }
.shop-product-grid { display: grid; grid-template-columns: repeat(5, minmax(0, 1fr)); gap: 14px; }
.shop-product { display: flex; flex-direction: column; align-items: flex-start; min-width: 0; padding: 25px 21px 20px; border: 1px solid #e1e6ec; border-radius: 12px; background: #fff; }
.shop-product--perpetual { border-color: #d9cbb0; background: linear-gradient(155deg, #fffefb, #fff); }
.shop-product-badge { display: inline-flex; border-radius: 5px; padding: 4px 7px; font-size: 10px; line-height: 1.4; color: #6d7f92; background: #f0f4f8; }
.shop-product--perpetual .shop-product-badge { color: var(--shop-gold); background: #f7f1e5; }
.shop-product h4 { font-size: 18px; font-weight: 600; color: #2b3c51; margin: 17px 0 6px; overflow-wrap: anywhere; }
.shop-product-term { font-size: 11px; color: #86909b; margin: 0; }
.shop-price { display: flex; align-items: center; min-height: 48px; margin: 21px 0 13px; color: var(--shop-navy); font-size: 20px; font-weight: 600; overflow-wrap: anywhere; }
.shop-product ul { display: grid; list-style: none; padding: 0; margin: 0 0 27px; gap: 11px; color: #758295; font-size: 11px; }
.shop-product li { display: flex; gap: 7px; align-items: center; }
.shop-product li svg { color: #8e9caa; flex-shrink: 0; }
.shop-buy { width: 100%; margin-top: auto; justify-content: center; font-size: 12px; white-space: normal; text-align: center; }
.shop-buy:disabled { color: #7b8695; background: #f4f6f8; border-color: #e8ecf1; opacity: 1; }
.shop-purchase-note { margin: 14px 0 0; color: #8893a0; font-size: 11px; line-height: 1.8; }
.shop-redemption { display: grid; grid-template-columns: minmax(240px, .8fr) minmax(0, 1.7fr); gap: 30px; align-items: start; padding: 30px 28px; background: #fff; border: 1px solid #e1e6ec; border-radius: 12px; }
.shop-redemption-copy { display: flex; gap: 13px; align-items: center; }
.shop-icon--gold { color: var(--shop-gold); background: #f8f3e9; }
.shop-redemption-copy h3 { margin: 0 0 8px; font-size: 18px; font-weight: 600; color: #2c3f53; }
.shop-redemption-copy p { margin: 0; font-size: 11px; color: #8993a0; line-height: 1.8; }
.shop-redemption-body { min-width: 0; }
.shop-redeem-form label { display: block; color: #596c81; font-size: 12px; margin-bottom: 9px; }
.shop-code-row { display: flex; gap: 10px; }
.shop-code-input { display: flex; flex: 1; align-items: center; gap: 9px; min-width: 0; border: 1px solid #dce3ec; border-radius: 7px; padding: 0 13px; color: #93a0af; background: #fafbfd; }
.shop-code-input:focus-within { outline: 2px solid #b9cada; outline-offset: 1px; }
.shop-code-input input { width: 100%; min-width: 0; height: 41px; border: 0; padding: 0; background: transparent; color: #33475e; outline: none; font-size: 12px; box-shadow: none; }
.shop-code-input input::placeholder { color: #9aa5b1; }
.shop-redeem-submit, .shop-recover { background: var(--shop-navy); border-color: var(--shop-navy); justify-content: center; min-height: 41px; font-size: 12px; }
.shop-code-help { margin: 11px 0 0; color: #8b96a2; line-height: 1.8; font-size: 11px; }
.shop-pending { margin: 0 0 13px; color: #8b6d31; background: #fbf7ef; border-radius: 7px; padding: 12px 14px; line-height: 1.8; font-size: 12px; }
.shop-feedback { margin: 13px 0 0; padding: 11px 13px; border-radius: 7px; font-size: 12px; line-height: 1.8; overflow-wrap: anywhere; }
.shop-feedback--error { color: #a4483d; background: #fcf1ef; }
.shop-feedback--success { display: flex; gap: 7px; align-items: flex-start; color: #3d755a; background: #edf6f0; }
.shop-feedback--success svg { flex-shrink: 0; margin-top: 3px; }
.shop-empty { display: flex; min-height: 225px; flex-direction: column; align-items: center; justify-content: center; gap: 12px; padding: 24px; border: 1px dashed #dce3eb; border-radius: 12px; background: #fbfcfd; color: #8190a1; text-align: center; font-size: 13px; }
.shop-empty p, .shop-empty h4 { margin: 0; line-height: 1.8; }
.shop-spin { animation: shop-spin 1s linear infinite; }
@keyframes shop-spin { to { transform: rotate(360deg); } }
@media (max-width: 1250px) { .shop-product-grid { grid-template-columns: repeat(3, minmax(0, 1fr)); } .shop-redemption { grid-template-columns: 1fr; gap: 24px; } }
@media (max-width: 850px) { .shop-product-grid { grid-template-columns: repeat(2, minmax(0, 1fr)); } .shop-status { gap: 20px; padding: 22px; } .shop-status dl { flex-basis: 65%; gap: 18px; } .shop-status dl > div { flex: 1; } }
@media (max-width: 560px) {
  .shop-page { gap: 23px; }
  .shop-heading-mark { display: none; }
  .shop-heading h2 { font-size: 22px; }
  .shop-heading p { font-size: 12px; line-height: 1.8; max-width: 22em; }
  .shop-status { padding: 20px; gap: 20px 12px; }
  .shop-status-identity { flex: 1; min-width: 0; }
  .shop-status dl { flex-basis: 100%; order: 3; flex-direction: column; gap: 17px; padding-top: 17px; border-top: 1px solid #edf0f4; }
  .shop-refresh { padding: 6px 8px; font-size: 10px; }
  .shop-status-identity .shop-icon { flex-basis: 37px; width: 37px; height: 40px; }
  .shop-status h3 { font-size: 16px; }
  .shop-status-error { order: 4; }
  .shop-section-heading { display: block; }
  .shop-section-heading p { margin-top: 10px; line-height: 1.8; }
  .shop-product-grid { grid-template-columns: 1fr; gap: 12px; }
  .shop-product { padding: 22px; }
  .shop-product h4 { margin-top: 13px; }
  .shop-price { margin-top: 14px; }
  .shop-product ul { margin-bottom: 21px; }
  .shop-redemption { padding: 22px 18px; gap: 23px; }
  .shop-redemption-copy h3 { font-size: 17px; }
  .shop-code-row { flex-direction: column; }
  .shop-redeem-submit { width: 100%; }
  .shop-code-input { flex: auto; }
  .shop-recover { width: 100%; white-space: normal; }
}
@media (prefers-reduced-motion: reduce) { .shop-spin { animation: none; } }
</style>
