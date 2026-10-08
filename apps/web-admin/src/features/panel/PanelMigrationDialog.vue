<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { Download, Upload, ShieldCheck } from '@lucide/vue'
import BaseModal from '@/components/BaseModal.vue'
import { downloadMigration, exportMigration, importMigration, previewMigration, recoveryMigration, type MigrationPreview } from '@/api/panelMigration'
import { displayError } from '@/lib/displayFormatters'

defineEmits<{ close: [] }>()
const mode = ref<'export' | 'import'>('export')
const administratorPassword = ref('')
const password = ref('')
const confirmation = ref('')
const file = ref<File | null>(null)
const preview = ref<MigrationPreview | null>(null)
const acknowledged = ref(false)
const busy = ref(false)
const error = ref('')
const message = ref('')
const restored = ref(false)
const operationKey = ref('')
const recoveryID = ref(sessionStorage.getItem('hl_panel_migration_recovery') || '')
const labels: Record<string, string> = { administrators: '管理员', customers: '用户', device_groups: '设备组', nodes: '节点', rules: '转发规则', subscriptions: '订阅', rule_groups: '规则组', user_groups: '用户组', usage_ingest_cursors: '采集游标', usage_events: '流量记录', usage_customer_totals: '流量累计', usage_enforcement_decisions: '限额记录', usage_enforcement_results: '执行结果' }
const counts = computed(() => Object.entries(preview.value?.counts || {}).map(([key, count]) => ({ label: labels[key] || key, count })))
const addressChanged = computed(() => preview.value && preview.value.manifest.source_url.replace(/\/$/, '') !== window.location.origin)
watch([mode, file, password, administratorPassword], () => { preview.value = null; acknowledged.value = false; operationKey.value = '' })
watch(mode, () => { error.value = ''; message.value = '' })
function choose(event: Event): void {
  file.value = (event.target as HTMLInputElement).files?.[0] || null
  if (file.value && (!file.value.name.endsWith('.hlbackup') || file.value.size > 64 * 1024 * 1024 || !file.value.size)) {
    file.value = null; error.value = '请选择不超过 64 MB 的 .hlbackup 文件'
    ;(event.target as HTMLInputElement).value = ''
  }
}
function validate(): boolean {
  error.value = ''; message.value = ''
  if (!administratorPassword.value) { error.value = '请填写当前面板的管理员密码'; return false }
  if (password.value.length < 10 || password.value.length > 256) { error.value = '备份密码需为 10 至 256 个字符'; return false }
  if (mode.value === 'export' && password.value !== confirmation.value) { error.value = '两次备份密码不一致'; return false }
  if (mode.value === 'import' && !file.value) { error.value = '请选择迁移备份包'; return false }
  return true
}
async function act(): Promise<void> {
  if (busy.value || !validate()) return
  busy.value = true
  try {
    if (mode.value === 'export') {
      const blob = await exportMigration(administratorPassword.value, password.value)
      downloadMigration(blob, `HL-Panel-${new Date().toISOString().slice(0, 10)}.hlbackup`)
      message.value = '备份已导出。请保管好文件与备份密码，忘记密码无法恢复。'
      administratorPassword.value = ''; password.value = ''; confirmation.value = ''
    } else {
      preview.value = await previewMigration(file.value!, administratorPassword.value, password.value)
      operationKey.value = crypto.randomUUID()
    }
  } catch (cause) { error.value = displayError(cause) } finally { busy.value = false }
}
async function restore(): Promise<void> {
  if (busy.value || !acknowledged.value || !preview.value || !file.value) return
  busy.value = true; error.value = ''
  try {
    const result = await importMigration(file.value, administratorPassword.value, password.value, preview.value.digest, operationKey.value)
    recoveryID.value = result.recovery_id
    sessionStorage.setItem('hl_panel_migration_recovery', result.recovery_id)
    sessionStorage.removeItem('ny_admin_access_token')
    restored.value = true
    administratorPassword.value = ''; password.value = ''
  } catch (cause) { error.value = displayError(cause) } finally { busy.value = false }
}
async function recovery(): Promise<void> {
  busy.value = true; error.value = ''
  try { downloadMigration(await recoveryMigration(recoveryID.value), 'HL-Panel-before-import.hlbackup') }
  catch (cause) { error.value = displayError(cause) } finally { busy.value = false }
}
function login(): void { window.location.assign('/login?redirect=/overview') }
</script>

<template>
  <BaseModal title="迁移备份" width="large" :close-disabled="busy || restored" @close="$emit('close')">
    <div class="migration-panel">
      <template v-if="!restored">
        <nav class="migration-tabs" aria-label="迁移方式">
          <button type="button" :class="{ selected: mode === 'export' }" :disabled="busy" @click="mode = 'export'"><Download :size="17" />导出备份</button>
          <button type="button" :class="{ selected: mode === 'import' }" :disabled="busy" @click="mode = 'import'"><Upload :size="17" />导入恢复</button>
        </nav>
        <p class="migration-summary">打包账号、规则、设备组、节点身份、订阅、限额、流量账本、站点设置及面板日志。新机器的证书、端口和安装环境由新面板安装器配置。</p>
        <form @submit.prevent="act">
          <fieldset :disabled="busy" class="migration-form">
            <label v-if="mode === 'import'" class="migration-file">迁移备份包<input type="file" accept=".hlbackup" aria-label="迁移备份包" @change="choose" /><small>单个 .hlbackup 文件，最大 64 MB</small></label>
            <label>当前管理员密码<input v-model="administratorPassword" type="password" autocomplete="current-password" maxlength="4096" aria-label="当前管理员密码" placeholder="当前登录面板的管理员密码" /></label>
            <label>{{ mode === 'export' ? '设置备份密码' : '备份包密码' }}<input v-model="password" type="password" :autocomplete="mode === 'export' ? 'new-password' : 'off'" maxlength="256" aria-label="备份密码" placeholder="至少 10 个字符" /></label>
            <label v-if="mode === 'export'">再次输入备份密码<input v-model="confirmation" type="password" autocomplete="new-password" maxlength="256" aria-label="确认备份密码" /></label>
          </fieldset>
          <p v-if="error" class="form-error" role="alert">{{ error }}</p>
          <p v-if="message" class="migration-success" role="status">{{ message }}</p>
          <button type="submit" class="button button--primary" :disabled="busy">{{ busy ? '正在处理，请勿关闭…' : mode === 'export' ? '导出加密备份' : '校验并预览' }}</button>
        </form>
        <section v-if="preview" class="migration-preview" aria-label="备份预览">
          <h3><ShieldCheck :size="18" />备份校验通过</h3>
          <p>版本 {{ preview.manifest.version }} · {{ new Date(preview.manifest.created_at).toLocaleString() }}</p>
          <p class="migration-origin">原面板：{{ preview.manifest.source_url }}</p>
          <dl><div v-for="item in counts" :key="item.label"><dt>{{ item.label }}</dt><dd>{{ item.count }}</dd></div></dl>
          <p v-if="addressChanged" class="migration-warning">面板地址已改变。导入后在每台节点上执行文档中的“更换面板地址”命令；在完成前节点无法连接新面板。订阅访问地址使用新面板域名。</p>
          <p v-else class="migration-summary">使用相同域名迁移时，完成恢复后将域名解析指向新面板机器。不要让两台面板同时接收节点数据。</p>
          <label class="migration-confirm"><input v-model="acknowledged" type="checkbox" :disabled="busy" />我确认用备份覆盖当前面板业务数据。系统会先创建恢复备份；完成后使用原面板的管理员账号密码重新登录。</label>
          <button type="button" class="button button--danger" :disabled="busy || !acknowledged" @click="restore">{{ busy ? '正在备份并恢复…' : '确认导入并恢复' }}</button>
        </section>
        <div v-if="recoveryID" class="migration-recovery"><span>上次导入前的恢复备份（使用当次备份密码）</span><button class="button button--secondary" :disabled="busy" @click="recovery">下载恢复备份</button></div>
        <p class="migration-help">新面板请先完成一键安装、修改默认管理员密码，并更新到不低于备份的版本。节点凭据和规则密钥会保留，无需重新注册。<a href="https://github.com/Aurelian-HL/HL-Panel/blob/main/deploy/backup-and-restore.md" target="_blank" rel="noopener noreferrer">查看迁移操作说明 ↗</a></p>
      </template>
      <section v-else class="migration-finished" role="status"><ShieldCheck :size="38" /><h3>导入成功</h3><p>面板正在重新载入，数秒后可登录。请使用原面板的管理员账号和密码。</p><p>导入前的恢复备份已保留。重新登录后在“迁移备份”中下载。</p><button class="button button--primary" @click="login">重新登录</button></section>
    </div>
  </BaseModal>
</template>

<style scoped>
.migration-panel { color: #34475e; line-height: 1.65; }
.migration-tabs { display: flex; gap: 8px; margin-bottom: 16px; }
.migration-tabs button { display: flex; align-items: center; gap: 8px; border: 1px solid #dbe3eb; background: #f5f7fa; color: #526177; padding: 10px 18px; border-radius: 8px; cursor: pointer; }
.migration-tabs .selected { color: #126857; background: #ecf7f3; border-color: #98cdbd; }
.migration-summary, .migration-help { color: #768397; font-size: 13px; }
.migration-form { display: grid; grid-template-columns: 1fr 1fr; gap: 16px; border: 0; margin: 18px 0; padding: 0; min-width: 0; }
.migration-form label { display: flex; flex-direction: column; gap: 6px; font-size: 13px; min-width: 0; }
.migration-form input { border: 1px solid #dbe3eb; border-radius: 8px; min-width: 0; width: 100%; box-sizing: border-box; padding: 10px 12px; background: #fff; }
.migration-form input:focus { outline: 2px solid #9dcebf; outline-offset: 1px; }
.migration-file { grid-column: 1 / -1; }
.migration-form small { color: #8793a4; }
.migration-preview { margin-top: 20px; border: 1px solid #dce7e2; padding: 16px; border-radius: 10px; background: #f8fbfa; }
.migration-preview h3 { display: flex; align-items: center; gap: 8px; color: #227a64; margin: 0; font-size: 16px; }
.migration-preview p { font-size: 13px; }
.migration-origin { overflow-wrap: anywhere; }
.migration-preview dl { display: grid; grid-template-columns: repeat(4, 1fr); gap: 10px; }
.migration-preview dl div { padding: 9px; border-radius: 7px; background: white; }
.migration-preview dt { font-size: 12px; color: #768397; }
.migration-preview dd { margin: 0; font-size: 18px; font-weight: 600; }
.migration-warning { padding: 10px; color: #906230; background: #fff5e7; border-radius: 8px; }
.migration-confirm { display: flex; align-items: flex-start; gap: 10px; margin: 16px 0; font-size: 13px; }
.migration-confirm input { margin-top: 6px; flex-shrink: 0; accent-color: #177763; }
.migration-success { color: #16705c; }
.migration-recovery { margin-top: 18px; display: flex; align-items: center; gap: 12px; flex-wrap: wrap; font-size: 13px; }
.migration-help a { color: #217e6b; white-space: normal; }
.migration-finished { padding: 30px 0; text-align: center; color: #217e6b; }
.migration-finished p { color: #627285; }
@media (max-width: 600px) { .migration-form { grid-template-columns: 1fr; } .migration-preview dl { grid-template-columns: repeat(2, 1fr); } .migration-tabs button { flex: 1; justify-content: center; padding: 10px 12px; } }
</style>
