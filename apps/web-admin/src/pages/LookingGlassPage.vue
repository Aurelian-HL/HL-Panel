<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { Activity, RefreshCw } from '@lucide/vue'
import { diagnosticsApi, type DiagnosticResult, type DiagnosticTarget } from '@/api/diagnostics'
import StatePanel from '@/components/StatePanel.vue'
import { displayError, formatDateTime } from '@/lib/displayFormatters'

const targets = ref<DiagnosticTarget[]>([])
const selectedId = ref('')
const action = ref<'ping' | 'dns'>('ping')
const result = ref<DiagnosticResult | null>(null)
const loading = ref(true)
const running = ref(false)
const error = ref('')
const runError = ref('')
const selected = computed(() => targets.value.find((target) => target.id === selectedId.value))

async function load(): Promise<void> {
  loading.value = true
  error.value = ''
  try {
    targets.value = await diagnosticsApi.targets()
    if (!targets.value.some((target) => target.id === selectedId.value)) selectedId.value = targets.value[0]?.id ?? ''
  } catch (cause) {
    error.value = displayError(cause)
  } finally {
    loading.value = false
  }
}

async function run(): Promise<void> {
  if (!selected.value || running.value) return
  running.value = true
  runError.value = ''
  result.value = null
  try {
    result.value = await diagnosticsApi.run(selected.value.id, action.value)
  } catch (cause) {
    runError.value = displayError(cause)
  } finally {
    running.value = false
  }
}

function changeTarget(): void {
  result.value = null
  runError.value = ''
  if (selected.value?.kind === 'ip' && action.value === 'dns') action.value = 'ping'
}

onMounted(load)
</script>

<template>
  <div class="page-stack lookingglass-page">
    <header class="page-heading">
      <div><h2>LookingGlass</h2><p>控制面本机诊断</p></div>
      <div class="page-heading__actions"><button class="button button--secondary" type="button" :disabled="loading || running" @click="load"><RefreshCw :size="16" :class="{ spin: loading }" />刷新</button></div>
    </header>

    <StatePanel v-if="loading && !targets.length" state="loading" title="正在读取诊断目标" />
    <StatePanel v-else-if="error && !targets.length" state="error" title="诊断目标加载失败" :message="error" @retry="load" />
    <StatePanel v-else-if="!targets.length" state="empty" title="暂无诊断目标" message="管理员尚未配置允许的目标。" />
    <template v-else>
      <section class="toolbar" aria-label="诊断操作">
        <label class="field">目标
          <select v-model="selectedId" :disabled="running" @change="changeTarget"><option v-for="target in targets" :key="target.id" :value="target.id">{{ target.label }}</option></select>
        </label>
        <div class="segmented-control" role="group" aria-label="诊断类型">
          <button type="button" :class="{ active: action === 'ping' }" :disabled="running" @click="action = 'ping'; result = null">Ping</button>
          <button type="button" :class="{ active: action === 'dns' }" :disabled="running || selected?.kind === 'ip'" @click="action = 'dns'; result = null">DNS</button>
        </div>
        <button class="button button--primary" type="button" :disabled="running || !selected" @click="run"><Activity :size="16" />{{ running ? '执行中' : '执行诊断' }}</button>
      </section>
      <StatePanel v-if="running" state="loading" title="诊断执行中" />
      <p v-else-if="runError" class="inline-warning" role="alert">{{ runError }}</p>
      <div v-else-if="result" class="diagnostic-result" :class="{ 'diagnostic-result--failed': result.status === 'failed' }" role="status">
        <strong>{{ result.status === 'ok' ? '检测成功' : '检测失败' }}</strong>
        <span>{{ targets.find((target) => target.id === result?.target_id)?.label }} · {{ result.action.toUpperCase() }} · {{ formatDateTime(result.checked_at) }}</span>
      </div>
      <p v-if="error && targets.length" class="inline-warning">刷新失败，仍显示先前的目标：{{ error }}</p>
    </template>
  </div>
</template>
