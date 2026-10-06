<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref } from 'vue'
import BaseModal from './BaseModal.vue'
import { canDeferVersion, checkVersion, repositoryUrl, updateCommand, type VersionStatus } from '@/api/releases'

const status = ref<VersionStatus | null>(null)
const loading = ref(false)
const error = ref('')
const details = ref(false)
const deferred = ref(false)
const copied = ref(false)
let timer: ReturnType<typeof setInterval> | undefined
const key = 'hl-panel-version-reminder'
const pending = computed(() => status.value && ['update_available', 'attention_required'].includes(status.value.state))
const showNotice = computed(() => pending.value && (!deferred.value || status.value?.attention_required))
const command = computed(() => {
  const tag = status.value?.latest_version ?? ''
  return /^v\d+\.\d+\.\d+$/.test(tag) ? `${updateCommand} -s -- --version ${tag}` : updateCommand
})

async function load(open = false): Promise<void> {
  if (loading.value) return
  if (open) details.value = true
  loading.value = true
  error.value = ''
  try {
    status.value = await checkVersion()
    deferred.value = false
    if (canDeferVersion(status.value)) {
      try {
        const saved = JSON.parse(localStorage.getItem(key) ?? 'null') as { version?: string; until?: number } | null
        deferred.value = saved?.version === status.value.latest_version && typeof saved.until === 'number' && saved.until > Date.now()
      } catch { /* Storage is optional; never bypass an urgent reminder. */ }
    }
  } catch (cause) {
    error.value = cause instanceof Error ? cause.message : '版本检查失败，请稍后重试'
  } finally { loading.value = false }
}

function defer(): void {
  if (!status.value || !canDeferVersion(status.value)) return
  deferred.value = true
  details.value = false
  try { localStorage.setItem(key, JSON.stringify({ version: status.value.latest_version, until: Date.now() + 24 * 60 * 60 * 1000 })) } catch { /* No persistent storage available. */ }
}

async function copy(): Promise<void> {
  copied.value = false
  try { await navigator.clipboard.writeText(command.value); copied.value = true } catch { error.value = '无法自动复制，请选中命令手动复制。' }
}

function focus(): void { void load() }
onMounted(() => {
  void load()
  timer = setInterval(() => { void load() }, 15 * 60 * 1000)
  window.addEventListener('focus', focus)
})
onBeforeUnmount(() => { if (timer) clearInterval(timer); window.removeEventListener('focus', focus) })
</script>

<template>
  <div class="version-notice">
    <button class="version-check" type="button" @click="load(true)">{{ loading ? '检查版本中…' : `版本 ${status?.current_version ?? '检查更新'}` }}</button>
    <div v-if="showNotice" class="version-banner" :class="{ 'version-banner--urgent': status?.attention_required }" role="status">
      <div><strong>{{ status?.attention_required ? '请查看版本更新' : '发现新版本' }}</strong><p>{{ status?.current_version }} → {{ status?.latest_version }}，落后{{ status?.versions_behind_at_least ? '至少' : '' }} {{ status?.versions_behind }} 个正式版本。{{ status?.attention_required ? '请前往 GitHub 查看更新说明。' : '可保留数据更新，或稍后处理。' }}</p></div>
      <div class="version-actions"><button class="button button--primary" @click="details = true">保留数据更新</button><a class="button button--secondary" :href="repositoryUrl + '/releases'" target="_blank" rel="noopener noreferrer">前往 GitHub</a><button v-if="status && canDeferVersion(status)" class="button button--ghost" @click="defer">下次更新</button></div>
    </div>
    <p v-else-if="error || status?.state === 'check_failed' || status?.state === 'unpublished'" class="version-error" role="status">{{ error || status?.message }} <button class="version-check" @click="load(true)">查看版本</button></p>
    <BaseModal v-if="details" title="版本与保留数据更新" width="medium" @close="details = false">
      <p v-if="loading">正在核对 GitHub 正式版本…</p>
      <template v-else>
        <p v-if="error" role="alert">{{ error }}</p>
        <p v-if="status">当前版本 <strong>{{ status.current_version }}</strong> · GitHub 正式版本 <strong>{{ status.latest_version || '暂未核实' }}</strong></p>
        <p v-if="status">{{ status.message }}</p>
        <p>登录安装面板的 VPS，以 root 执行下面的更新命令。更新会短暂停止本面板，先备份数据库和配置，再安装官方发布包；管理员账号、已修改密码、规则、域名和证书全部保留。</p>
        <pre class="version-command"><code>{{ command }}</code></pre>
        <div class="version-actions"><button class="button button--secondary" @click="copy">{{ copied ? '已复制' : '复制更新命令' }}</button><a class="button button--secondary" :href="repositoryUrl + '/releases'" target="_blank" rel="noopener noreferrer">查看发布说明</a></div>
        <p>更新成功后刷新面板。失败时自动恢复升级前程序和数据库；终端也会显示备份位置与手动回滚命令。回滚会将数据恢复到备份时刻，请先阅读说明。</p>
        <p v-if="status?.attention_required">已落后超过 2 个正式版本，更新提醒会持续显示，不能通过“下次更新”隐藏。</p>
      </template>
      <template #footer><button v-if="status && canDeferVersion(status)" class="button button--secondary" @click="defer">下次更新</button><button class="button button--primary" @click="details = false">关闭说明</button></template>
    </BaseModal>
  </div>
</template>

<style scoped>
.version-notice { margin-bottom: 16px; }
.version-check { border: 0; background: transparent; padding: 4px 0; color: #536479; font: inherit; font-size: 12px; cursor: pointer; }
.version-banner { display: flex; align-items: center; justify-content: space-between; gap: 16px; margin-top: 8px; padding: 16px; border: 1px solid #d9e2ed; border-radius: 10px; background: #f5f8fc; color: #23364f; }
.version-banner--urgent { background: #fffbef; border-color: #e8d5a1; }
.version-banner p, .version-error { margin: 6px 0 0; font-size: 13px; line-height: 1.6; }
.version-actions { display: flex; flex-wrap: wrap; gap: 8px; flex-shrink: 0; }
.version-command { padding: 14px; border-radius: 8px; background: #eef2f7; white-space: pre-wrap; overflow-wrap: anywhere; font-size: 12px; }
@media (max-width: 1050px) { .version-banner { align-items: flex-start; flex-direction: column; } }
</style>
