<script setup lang="ts">
import { computed, onMounted, onUnmounted, ref } from 'vue'
import BaseModal from '@/components/BaseModal.vue'
import { nodeDownloadPrerequisites } from '@/lib/nodeCommand'
import { nodeUpdateStatus, startNodeUpdate, cancelNodeUpdate, type NodeUpdateStatus } from '@/api/nodeUpdates'

const props = defineProps<{ nodeId: string }>()
defineEmits<{ close: [] }>()
const status = ref<NodeUpdateStatus | null>(null)
const version = ref('')
const error = ref('')
const readError = ref('')
const loading = ref(false)
const submitting = ref(false)
let disposed = false
let timer: ReturnType<typeof setTimeout> | undefined
const active = computed(() => !!status.value?.task && !['succeeded', 'failed', 'rolled_back', 'cancelled'].includes(status.value.task.state))
const pendingKey = `hl-node-update:${props.nodeId}`
const command = computed(() => {
  const tag = status.value?.panel_version ?? ''
  if (!/^v\d+\.\d+\.\d+$/.test(tag)) return ''
  return `(set +x; set -Eeuo pipefail; ${nodeDownloadPrerequisites}; f=$(mktemp); trap 'rm -f -- "$f"' EXIT; curl --proto '=https' --proto-redir '=https' -fsSL https://raw.githubusercontent.com/Aurelian-HL/HL-Panel/${tag}/update-node.sh -o "$f"; bash "$f" --version ${tag})`
})
const labels: Record<string, string> = { queued: '等待领取', running: '正在更新', verifying: '验证配置与监听', succeeded: '更新成功', failed: '更新失败', rolled_back: '已回滚', cancelled: '已取消', uncertain: '结果待核实' }
async function refresh() {
  if (loading.value || disposed) return
  loading.value = true
  try {
    const next = await nodeUpdateStatus(props.nodeId)
    if (disposed) return
    status.value = next
    readError.value = ''
    if (!next.versions.includes(version.value)) version.value = next.versions[0] ?? ''
    const pending = sessionStorage.getItem(pendingKey)
    if (pending && next.task?.id === pending.split('|')[0]) {
      sessionStorage.removeItem(pendingKey)
      error.value = ''
    }
  } catch (e) { if (!disposed) readError.value = e instanceof Error ? e.message : '读取更新状态失败' }
  finally {
    loading.value = false
    if (!disposed) timer = setTimeout(refresh, active.value ? 3000 : 15000)
  }
}
async function start() {
  if (!version.value || submitting.value || active.value) return
  submitting.value = true
  error.value = ''
  const pending = sessionStorage.getItem(pendingKey)?.split('|')
  const key = pending?.[1] === version.value ? pending[0]! : crypto.randomUUID()
  sessionStorage.setItem(pendingKey, `${key}|${version.value}`)
  try {
    const task = await startNodeUpdate(props.nodeId, version.value, key)
    if (!disposed && status.value) status.value.task = task
    sessionStorage.removeItem(pendingKey)
  } catch (e) { error.value = `${e instanceof Error ? e.message : '提交失败'}。状态会继续刷新，重试使用同一任务标识。` }
  finally { submitting.value = false; clearTimeout(timer); await refresh() }
}
async function cancel() {
  if (!status.value?.task || submitting.value) return
  submitting.value = true
  error.value = ''
  try { status.value.task = await cancelNodeUpdate(props.nodeId, status.value.task.id) }
  catch (e) { error.value = e instanceof Error ? e.message : '取消失败' }
  finally { submitting.value = false }
}
function manualRefresh() { clearTimeout(timer); error.value = ''; void refresh() }
onMounted(refresh)
onUnmounted(() => { disposed = true; clearTimeout(timer) })
</script>

<template>
  <BaseModal title="节点更新" description="固定正式版本更新 · 保留身份、规则、订阅与流量账本" @close="$emit('close')">
    <div class="node-update-content">
      <p v-if="!status && loading" role="status">正在读取节点和正式版本…</p>
      <p v-if="error" class="text-danger" role="alert">{{ error }}</p>
      <p v-if="readError" class="text-danger" role="alert">{{ readError }}</p>
      <template v-if="status">
        <dl><div><dt>节点版本</dt><dd>{{ status.current_version || '未上报' }}</dd></div><div><dt>面板版本</dt><dd>{{ status.panel_version }}</dd></div></dl>
        <p>{{ status.message }}</p>
        <section v-if="status.task" class="node-update-task" aria-live="polite">
          <strong>{{ labels[status.task.state] || status.task.state }} · {{ status.task.target_version }}</strong>
          <p>{{ status.task.message }}</p>
          <small>任务 {{ status.task.id }}</small>
          <p v-if="status.task.backup_directory">节点备份：<code>{{ status.task.backup_directory }}</code></p>
          <p v-if="status.task.state === 'uncertain'">任务保持锁定。请在节点检查更新服务日志和备份，核实后恢复；系统不会自动重复安装。</p>
          <button v-if="status.task.state === 'queued'" type="button" class="button button--secondary" :disabled="submitting" @click="cancel">取消未领取任务</button>
        </section>
        <template v-if="status.available">
          <label v-if="status.versions.length" class="node-update-version">目标正式版本<select v-model="version" :disabled="active || submitting"><option v-for="tag in status.versions" :key="tag" :value="tag">{{ tag }}</option></select></label>
          <p v-else>当前没有适用的正式版本。请先从系统更新升级面板。</p>
          <p v-if="version && version === status.current_version">所选版本与节点相同，将执行修复安装并重新验收。</p>
          <p class="text-warning">程序切换会中断此节点现有连接，客户端需重连。全局同时只执行一台节点更新。</p>
        </template>
        <section v-else-if="command">
          <strong>旧节点一次命令接入</strong>
          <p>在节点服务器以 root 执行下方保身份升级命令。完成后刷新，即可从本面板更新。新节点在“节点令牌”处复制安装命令即可自动接入。</p>
          <pre tabindex="0">{{ command }}</pre>
        </section>
      </template>
    </div>
    <template #footer><button type="button" class="button button--secondary" :disabled="loading" @click="manualRefresh">刷新</button><button v-if="status?.available" type="button" class="button button--primary" :disabled="!version || active || submitting" @click="start">{{ submitting ? '正在提交…' : active ? '任务执行中' : version === status.current_version ? '重新安装此版本' : '开始更新此节点' }}</button></template>
  </BaseModal>
</template>

<style scoped>
.node-update-content { display: grid; gap: 16px; min-width: 0; }
.node-update-content p { margin: 0; line-height: 1.7; }
.node-update-content dl { display: flex; gap: 28px; margin: 0; flex-wrap: wrap; }
.node-update-content dt { color: var(--text-muted, #64748b); font-size: 12px; }
.node-update-content dd { margin: 4px 0 0; font-weight: 600; }
.node-update-task { padding: 16px; border-radius: 10px; background: var(--surface-muted, #f1f5f9); display: grid; gap: 8px; overflow-wrap: anywhere; }
.node-update-content pre { padding: 12px; border-radius: 8px; background: #f1f5f9; color: #172b4d; white-space: pre-wrap; overflow-wrap: anywhere; font-size: 12px; }
.node-update-content code { overflow-wrap: anywhere; }
.node-update-version { display: grid; gap: 8px; }
.node-update-version select { width: 100%; padding: 10px; border: 1px solid #cbd5e1; border-radius: 8px; background: var(--surface, white); color: inherit; }
</style>
