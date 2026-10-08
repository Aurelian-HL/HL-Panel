<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref } from 'vue'
import { panelUpdateStatus, startPanelUpdate, type PanelUpdateStatus } from '@/api/panelUpdate'
import { displayError } from '@/lib/displayFormatters'

const props = defineProps<{ version: string }>()
const status = ref<PanelUpdateStatus | null>(null)
const password = ref('')
const error = ref('')
const disconnected = ref(false)
const sending = ref(false)
const pending = ref(false)
const busy = computed(() => sending.value || status.value?.task?.state === 'running')
let requestId = ''
let requestVersion = ''
let timer: ReturnType<typeof setTimeout> | undefined
let disposed = false
let polling = false

async function poll(): Promise<void> {
  if (polling || disposed) return
  polling = true
  try {
    status.value = await panelUpdateStatus()
    disconnected.value = false
    pending.value = false
  } catch (cause) {
    disconnected.value = true
    if (!busy.value && !pending.value) error.value = displayError(cause)
  } finally {
    polling = false
    if (!disposed && (busy.value || pending.value)) timer = setTimeout(() => { void poll() }, 2000)
  }
}

async function submit(): Promise<void> {
  if (!props.version || !password.value || busy.value) return
  if (!requestId || requestVersion !== props.version) {
    requestId = crypto.randomUUID(); requestVersion = props.version
  }
  sending.value = true; pending.value = true; error.value = ''
  const confirmation = password.value
  password.value = ''
  try {
    status.value = await startPanelUpdate(props.version, confirmation, requestId)
    pending.value = false
  } catch (cause) { error.value = displayError(cause) } finally {
    sending.value = false
    if (timer) clearTimeout(timer)
    void poll()
  }
}
function refreshPage(): void { window.location.reload() }
onMounted(() => { void poll() })
onBeforeUnmount(() => { disposed = true; if (timer) clearTimeout(timer); password.value = '' })
</script>

<template>
  <section class="panel-update-form" aria-label="网页保数据更新">
    <p v-if="!status && !error">正在连接更新服务…</p>
    <p v-if="status && !status.available" role="status">{{ status.message }}</p>
    <template v-if="status?.task">
      <p role="status"><strong>{{ status.task.target_version }}</strong> · {{ status.task.message }}</p>
      <p v-if="status.task.backup_directory" class="update-backup">备份目录：{{ status.task.backup_directory }}</p>
      <p v-if="status.task.state === 'running'">任务在后台继续执行，关闭弹窗不会取消更新。</p>
      <button v-if="status.task.state === 'succeeded'" class="button button--primary" @click="refreshPage">刷新面板</button>
    </template>
    <p v-if="disconnected && (busy || pending)" role="status">面板暂时断开，正在等待服务恢复并核对更新结果…</p>
    <p v-if="error" class="form-error" role="alert">{{ error }}</p>
    <form v-if="version && status?.available && !busy && !(status.task?.state === 'succeeded' && status.task.target_version === version)" @submit.prevent="submit">
      <label for="panel-update-password">当前管理员密码</label>
      <input id="panel-update-password" v-model="password" type="password" autocomplete="current-password" required maxlength="4096" />
      <p>确认后更新至 {{ version }}。先备份再升级；面板会短暂断开，失败时自动恢复。</p>
      <button type="submit" class="button button--primary" :disabled="!password">确认保数据更新</button>
    </form>
  </section>
</template>

<style scoped>
.panel-update-form { margin: 12px 0; font-size: 13px; line-height: 1.7; }
.panel-update-form p { margin: 7px 0; }
.panel-update-form label { display: block; margin-bottom: 5px; }
.panel-update-form input { width: 100%; min-height: 38px; border: 1px solid #ced7e1; border-radius: 6px; padding: 7px 10px; box-sizing: border-box; }
.update-backup { color: #667688; overflow-wrap: anywhere; font-size: 12px; }
</style>
