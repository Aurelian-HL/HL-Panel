<script setup lang="ts">
import { computed, onMounted, onUnmounted, ref, watch } from 'vue'
import { Download, Fingerprint, Play, RefreshCw, Undo2 } from '@lucide/vue'
import { automaticStatus, continueAutomaticMigration, downloadMigration, probeMigration, recoveryMigration, rollbackAutomaticMigration, startAutomaticMigration, type AutomaticInput, type AutomaticStatus } from '@/api/panelMigration'
import { displayError } from '@/lib/displayFormatters'

const status = ref<AutomaticStatus | null>(null)
const host = ref(''), port = ref(22), sourceURL = ref(''), fingerprint = ref('')
const authentication = ref<'password' | 'key'>('password')
const sshPassword = ref(''), privateKey = ref(''), passphrase = ref('')
const adminPassword = ref(''), backupPassword = ref(''), backupConfirmation = ref('')
const trusted = ref(false), fresh = ref(false), cutover = ref(false), rollbackConfirmed = ref(false)
const busy = ref(false), error = ref(''), pollingError = ref(''), loading = ref(true)
let timer: ReturnType<typeof setTimeout> | undefined
let disposed = false
const task = computed(() => status.value?.task)
const activeTask = computed(() => !!task.value && !['cancelled', 'completed'].includes(task.value.state))
const needsConnection = computed(() => !status.value?.running && (!activeTask.value || status.value?.credentials_required || task.value?.state === 'failed'))
const labels: Record<string, string> = { running: '执行中', ready: '等待正式切换', waiting_dns: '等待域名解析', failed: '待重新连接', completed: '迁移完成', cancelled: '已撤销', prepare: '准备新机', restore: '恢复数据', certificate: '域名与证书', rollback: '撤销切换' }
watch([host, port], () => { fingerprint.value = ''; trusted.value = false }, { flush: 'sync' })
watch(authentication, () => { sshPassword.value = ''; privateKey.value = ''; passphrase.value = '' })

function apply(next: AutomaticStatus): void {
  status.value = next
  sourceURL.value = next.task?.source_url || next.source_url || ''
  if (next.task && !['completed', 'cancelled'].includes(next.task.state)) {
    host.value = next.task.target.host; port.value = next.task.target.port
    fingerprint.value = next.task.target.fingerprint
  }
}
async function refresh(): Promise<void> {
  try { const next = await automaticStatus(); if (!disposed) { apply(next); pollingError.value = '' } }
  catch (cause) { if (!disposed) pollingError.value = displayError(cause) }
  finally {
    loading.value = false
    if (!disposed) timer = setTimeout(() => { void refresh() }, 5000)
  }
}
onMounted(() => { void refresh() })
onUnmounted(() => { disposed = true; if (timer) clearTimeout(timer); clearSecrets() })
function clearSecrets(): void { sshPassword.value = ''; privateKey.value = ''; passphrase.value = ''; adminPassword.value = ''; backupPassword.value = ''; backupConfirmation.value = '' }
async function action(operation: () => Promise<void>): Promise<void> {
  if (busy.value) return
  busy.value = true; error.value = ''
  try { await operation() } catch (cause) { error.value = displayError(cause) } finally { busy.value = false }
}
function input(confirm: string): AutomaticInput {
  return { id: activeTask.value ? task.value!.id : crypto.randomUUID(), target: { host: host.value.trim(), port: Number(port.value), fingerprint: fingerprint.value }, source_url: sourceURL.value,
    administrator_password: adminPassword.value, password: backupPassword.value, confirm,
    ssh_password: authentication.value === 'password' ? sshPassword.value : '', ssh_private_key: authentication.value === 'key' ? privateKey.value : '', ssh_key_passphrase: authentication.value === 'key' ? passphrase.value : '' }
}
async function probe(): Promise<void> {
  await action(async () => {
    const target = { host: host.value.trim(), port: Number(port.value), fingerprint: '' }
    const result = await probeMigration(target, adminPassword.value)
    fingerprint.value = result.fingerprint; trusted.value = false
  })
}
async function prepare(): Promise<void> {
  await action(async () => {
    if (!trusted.value || !fingerprint.value) throw new Error('请先获取并确认 SSH 指纹')
    if (!activeTask.value && !fresh.value) throw new Error('请确认目标服务器是全新服务器')
    if (backupPassword.value !== backupConfirmation.value) throw new Error('两次备份密码不一致')
    apply(await startAutomaticMigration(input('PREPARE')))
    clearSecrets()
  })
}
async function proceed(): Promise<void> {
  await action(async () => {
    if (!cutover.value) throw new Error('请确认正式切换')
    apply(await continueAutomaticMigration(task.value!.id, adminPassword.value))
    adminPassword.value = ''; cutover.value = false
  })
}
async function rollback(): Promise<void> {
  await action(async () => {
    if (!rollbackConfirmed.value) throw new Error('请确认域名已指回旧机')
    const value = input('ROLLBACK'); value.id = task.value!.id
    apply(await rollbackAutomaticMigration(value)); clearSecrets()
  })
}
async function download(): Promise<void> {
  await action(async () => { downloadMigration(await recoveryMigration(task.value!.backup_id), 'HL-Panel-final-migration.hlbackup') })
}
</script>

<template>
  <div class="automatic-migration">
    <p v-if="loading" role="status">正在读取迁移状态…</p>
    <p v-if="error" class="form-error" role="alert">{{ error }}</p>
    <p v-if="pollingError" class="form-error" role="alert">{{ pollingError }}</p>
    <p v-if="status && !status.available" class="migration-notice">{{ status.message }}</p>
    <template v-if="status">
      <section v-if="task" class="migration-progress" aria-label="迁移进度" aria-live="polite">
        <h3>{{ labels[task.state] || task.state }} <small>{{ labels[task.phase] || task.phase }}</small></h3>
        <p>{{ task.message }}</p>
        <dl><div><dt>新服务器</dt><dd>{{ task.target.host }}</dd></div><div><dt>保留域名</dt><dd>{{ task.domain }}</dd></div><div><dt>安装版本</dt><dd>{{ task.version }}</dd></div></dl>
        <p v-if="task.state === 'waiting_dns'">将 {{ task.domain }} 的 A 记录改为 {{ task.target.host }}，移除未使用的 AAAA 记录。使用 DNS 直连模式；新机需开放 80 和 443 端口。</p>
        <p v-if="status.frozen">旧面板已暂停业务写入。现有节点保留已生效规则，探针和管理操作会在新面板启用后恢复。</p>
        <div class="migration-links">
          <a v-if="status.source_ip_url" :href="`${status.source_ip_url}/migration`" target="_blank" rel="noopener noreferrer">旧机迁移入口</a>
          <a v-if="task.state === 'completed'" :href="`${task.source_url}/login?redirect=/overview`" target="_blank" rel="noopener noreferrer">登录新面板</a>
          <button v-if="task.backup_id" type="button" class="button button--secondary" :disabled="busy" @click="download"><Download :size="16" />下载最终备份</button>
        </div>
      </section>
      <form v-if="needsConnection && status.available" @submit.prevent="prepare">
        <fieldset :disabled="busy" class="migration-fields">
          <label>新服务器公网 IPv4<input v-model="host" aria-label="新服务器 IP" :readonly="activeTask" required autocomplete="off" /></label>
          <label>SSH 端口<input v-model.number="port" aria-label="SSH 端口" type="number" min="1" max="65535" :readonly="activeTask" required /></label>
          <label>原面板 HTTPS 地址<input :value="sourceURL" readonly aria-label="原面板地址" /></label>
          <label>当前管理员密码<input v-model="adminPassword" aria-label="自动迁移管理员密码" type="password" autocomplete="current-password" required /></label>
          <div class="fingerprint-field"><button type="button" class="button button--secondary" :disabled="!host || !adminPassword || busy" @click="probe"><Fingerprint :size="16" />获取 SSH 指纹</button><code v-if="fingerprint">{{ fingerprint }}</code></div>
          <label class="check full"><input v-model="trusted" type="checkbox" :disabled="!fingerprint" />已通过服务器控制台等可信渠道核对 SSH 指纹</label>
          <label>root 登录方式<select v-model="authentication"><option value="password">密码</option><option value="key">SSH 私钥</option></select></label>
          <label v-if="authentication === 'password'">root SSH 密码<input v-model="sshPassword" aria-label="SSH 密码" type="password" autocomplete="off" required /></label>
          <template v-else><label class="full">SSH 私钥<textarea v-model="privateKey" aria-label="SSH 私钥" rows="5" autocomplete="off" required spellcheck="false" /></label><label>私钥口令（可留空）<input v-model="passphrase" type="password" autocomplete="off" /></label></template>
          <label>{{ activeTask ? '本次迁移的备份密码' : '设置备份密码' }}<input v-model="backupPassword" aria-label="自动迁移备份密码" type="password" minlength="10" maxlength="256" autocomplete="off" required /></label>
          <label>再次输入备份密码<input v-model="backupConfirmation" aria-label="自动迁移确认密码" type="password" autocomplete="off" required /></label>
          <label v-if="!activeTask" class="check full"><input v-model="fresh" type="checkbox" />确认新机为全新 Debian/Ubuntu amd64 服务器，允许 root SSH，且没有需要保留的面板业务</label>
        </fieldset>
        <button class="button button--primary" type="submit" :disabled="busy || !trusted"><RefreshCw v-if="activeTask" :size="16" /><Play v-else :size="16" />{{ activeTask ? '重新连接并继续' : '准备新服务器' }}</button>
      </form>
      <form v-if="task?.state === 'ready' && !status.running && !status.credentials_required" @submit.prevent="proceed">
        <label class="standalone">当前管理员密码<input v-model="adminPassword" aria-label="切换管理员密码" type="password" autocomplete="current-password" required /></label>
        <label class="check"><input v-model="cutover" type="checkbox" :disabled="busy" />确认暂停旧面板写入并恢复到新机。我会在恢复完成后切换域名解析，并保管最终备份密码</label>
        <button type="submit" class="button button--primary" :disabled="busy || !cutover"><Play :size="16" />正式切换</button>
      </form>
      <details v-if="activeTask && !status.running" class="rollback-section">
        <summary>撤销未完成的迁移</summary>
        <form @submit.prevent="rollback">
          <label class="standalone">当前管理员密码<input v-model="adminPassword" type="password" autocomplete="current-password" required /></label>
          <p v-if="status.credentials_required">请在上方填写新机 SSH 凭据。系统会先停止新机，再恢复旧机写入；已经启用的新机不能直接撤销。</p>
          <label class="check"><input v-model="rollbackConfirmed" type="checkbox" />域名已指回旧机，我确认撤销切换并保留两端数据</label>
          <button class="button button--danger" :disabled="busy || !rollbackConfirmed"><Undo2 :size="16" />停止新机并恢复旧机</button>
        </form>
      </details>
    </template>
  </div>
</template>

<style scoped>
.automatic-migration { color: #34475e; line-height: 1.6; min-width: 0; }
.migration-fields { display: grid; grid-template-columns: minmax(0, 1fr) minmax(0, 1fr); gap: 16px; margin: 16px 0; border: 0; padding: 0; min-width: 0; }
label { display: flex; flex-direction: column; gap: 6px; font-size: 13px; min-width: 0; }
input:not([type=checkbox]), select, textarea { width: 100%; min-width: 0; box-sizing: border-box; border: 1px solid #dbe3eb; border-radius: 6px; padding: 10px 12px; background: white; color: inherit; font: inherit; }
input:read-only { background: #f5f7fa; }
.full, .fingerprint-field { grid-column: 1 / -1; }
.fingerprint-field { display: flex; flex-wrap: wrap; gap: 12px; align-items: center; }
code, dd { overflow-wrap: anywhere; }
.check { flex-direction: row; align-items: flex-start; gap: 8px; margin: 12px 0; }
.check input { flex-shrink: 0; margin-top: 5px; }
.migration-progress { border-bottom: 1px solid #dbe3eb; padding-bottom: 16px; margin-bottom: 16px; }
h3 { margin: 0; font-size: 16px; }
small, dt { color: #768397; font-size: 12px; }
dl { display: grid; grid-template-columns: repeat(3, minmax(0, 1fr)); gap: 12px; }
dd { margin: 0; font-size: 13px; }
p { font-size: 13px; }
.migration-links { display: flex; flex-wrap: wrap; gap: 16px; align-items: center; }
a { color: #217e6b; }
.migration-notice { padding: 12px; background: #fff5e7; color: #906230; border-radius: 6px; }
.standalone { max-width: 360px; margin: 16px 0; }
.rollback-section { border-top: 1px solid #dbe3eb; margin-top: 24px; padding-top: 16px; }
summary { cursor: pointer; font-size: 13px; }
@media (max-width: 600px) { .migration-fields, dl { grid-template-columns: minmax(0, 1fr); } }
</style>
