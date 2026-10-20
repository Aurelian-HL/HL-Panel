<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { panelLicenseApi, type LicenseChallenge } from '@/api/panelLicense'
import { panelLicenseStore as store } from '@/stores/panelLicense'

const busy = ref(false)
const message = ref('')
const code = ref('')
const challenge = ref<LicenseChallenge | null>(null)
const showActivation = ref(false)
const status = computed(() => store.status.value)
const labels: Record<string, string> = { active: '授权有效', expired: '授权已到期', revoked: '授权已撤销', stale: '授权校验待恢复', unactivated: '等待激活', unconfigured: '授权配置待接入' }
const date = (value?: string | null) => value ? new Date(value).toLocaleString('zh-CN', { timeZone: 'Asia/Shanghai' }) : '—'
async function action(kind: 'sync' | 'prepare' | 'commit'): Promise<void> {
  if (busy.value) return
  const generation = store.generation()
  busy.value = true; message.value = ''
  try {
    if (kind === 'prepare') { const result = await panelLicenseApi.prepare(code.value); if (generation !== store.generation()) return; challenge.value = result; code.value = '' }
    else { const result = await panelLicenseApi[kind](); if (!store.apply(result, generation)) return; message.value = kind === 'commit' ? '授权已激活' : '授权状态已同步'; if (kind === 'commit') { challenge.value = null; showActivation.value = false } }
  } catch (cause) { if (generation === store.generation()) message.value = cause instanceof Error ? cause.message : '操作未完成，请重试' }
  finally { busy.value = false }
}
onMounted(() => { void store.refresh() })
</script>

<template>
  <section v-if="status?.enabled || store.error.value" class="panel-license-card" aria-label="面板域名授权">
    <template v-if="status?.enabled">
      <header><div><small>面板域名授权</small><h2>{{ labels[status.status] || status.status }}</h2></div><span class="panel-license-badge" :class="{ restricted: !status.management_allowed }">{{ status.management_allowed ? '可正常管理' : '新增、修改和删除受限' }}</span></header>
      <dl><div><dt>授权域名</dt><dd>{{ status.domain || '待配置' }}</dd></div><div><dt>到期时间（北京时间）</dt><dd>{{ ['unactivated', 'unconfigured'].includes(status.status) ? '激活后显示' : status.expires_at ? date(status.expires_at) : '永久授权' }}</dd></div><div><dt>最近同步</dt><dd>{{ date(status.checked_at) }}</dd></div></dl>
      <p v-if="!status.management_allowed">现有转发、订阅和节点运行继续保留。{{ status.status === 'unconfigured' ? '请先由服务器管理员接入授权配置。' : status.status === 'unactivated' ? '请激活 HT 授权码以开启管理。' : '续期或恢复授权校验后，点击同步即可恢复管理。' }}</p>
      <p v-if="status.last_error" role="status">{{ status.last_error }}</p>
      <p v-if="store.error.value" role="alert">{{ store.error.value }}；当前显示最近一次读取的状态。</p>
      <footer><a class="button button--primary" href="https://ht.hongle.work/" target="_blank" rel="noopener noreferrer">立即支持 / 续期</a><button class="button button--secondary" :disabled="busy || status.status === 'unconfigured'" @click="action('sync')">{{ busy ? '处理中…' : '同步授权' }}</button><button class="button button--quiet" :disabled="busy || status.status === 'unconfigured'" @click="showActivation = !showActivation">{{ showActivation ? '收起激活' : '激活授权码' }}</button></footer>
      <form v-if="showActivation" class="panel-license-activation" @submit.prevent="action('prepare')"><label class="field"><span>HT 提供的授权码</span><input v-model="code" type="password" autocomplete="off" maxlength="512" required /></label><button class="button button--secondary" :disabled="busy || !code.trim()">获取域名证明</button></form>
      <div v-if="challenge && showActivation" class="panel-license-proof"><p>在域名 DNS 中添加以下 TXT 记录，解析生效后点击验证。证明有效至 {{ date(challenge.expires_at) }}。</p><dl><div><dt>记录名</dt><dd><code>{{ challenge.dns_name }}</code></dd></div><div><dt>TXT 值</dt><dd><code>{{ challenge.dns_value }}</code></dd></div></dl><button type="button" class="button button--primary" :disabled="busy" @click="action('commit')">验证 DNS 并激活</button></div>
      <p v-if="message" role="status">{{ message }}</p>
      <details><summary>个人使用与授权说明</summary><p>仅供个人合法用途，实验性功能请勿直接用于关键生产环境。一码仅授权一个管理域名及实例，禁止转售、分享授权码或复制实例密钥。面板会在本机保存运行所需的账号、规则和统计；授权服务仅处理授权域名、实例证明、期限和必要审计，不接收客户转发内容。持有服务器最高权限的人仍可修改程序，授权不能替代访问控制。</p></details>
    </template>
    <template v-else><h2>授权状态读取失败</h2><p role="alert">{{ store.error.value }}</p><button class="button button--secondary" @click="store.refresh">重试</button></template>
  </section>
</template>

<style scoped>
.panel-license-card { padding: 22px; border: 1px solid #dce3ed; border-radius: 14px; background: #fff; color: #24364e; display: grid; gap: 16px; }
.panel-license-card header, .panel-license-card footer { display: flex; align-items: center; justify-content: space-between; gap: 12px; flex-wrap: wrap; }
.panel-license-card h2 { margin: 4px 0 0; font-size: 21px; }.panel-license-card small, dt { color: #65748a; }
.panel-license-card dl { display: flex; flex-wrap: wrap; gap: 18px 40px; margin: 0; }.panel-license-card dd { margin: 5px 0 0; font-weight: 600; overflow-wrap: anywhere; }
.panel-license-card p { margin: 0; line-height: 1.7; font-size: 13px; }.panel-license-card footer { justify-content: flex-start; }
.panel-license-badge { color: #16724d; background: #e8f5ee; padding: 6px 10px; border-radius: 20px; font-size: 12px; }.panel-license-badge.restricted { color: #895b19; background: #fff1d9; }
.panel-license-activation, .panel-license-proof { display: grid; gap: 12px; padding: 16px; background: #f5f7fa; border-radius: 10px; }.panel-license-activation button, .panel-license-proof button { justify-self: start; }details { font-size: 12px; color: #65748a; }summary { cursor: pointer; }details p { padding-top: 10px; }
@media(max-width:640px) { .panel-license-card { padding: 16px; }.panel-license-card dl { display: grid; gap: 14px; }.panel-license-card footer { display: grid; grid-template-columns: 1fr 1fr; }.panel-license-card footer a { grid-column: 1 / -1; justify-content: center; } }
</style>
