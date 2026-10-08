<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { Download, RefreshCw } from '@lucide/vue'
import BaseModal from '@/components/BaseModal.vue'
import { getPanelLogs, type PanelLogs } from '@/api/panelLogs'
import { displayError } from '@/lib/displayFormatters'

defineEmits<{ close: [] }>()
const limit = ref(100)
const filter = ref('')
const levels = ref(['INFO', 'WARN', 'ERROR'])
const logs = ref<PanelLogs | null>(null)
const busy = ref(false)
const error = ref('')
const entries = computed(() => (logs.value?.items ?? []).filter(item => levels.value.includes(item.level) && `${item.time} ${item.level} ${item.message}`.toLocaleLowerCase().includes(filter.value.toLocaleLowerCase())))
const text = computed(() => entries.value.map(item => `${item.time} [${item.level}] ${item.message}`).join('\n'))
async function load(): Promise<void> {
  if (busy.value) return
  busy.value = true; error.value = ''
  try { logs.value = await getPanelLogs(limit.value) } catch (cause) { error.value = displayError(cause) } finally { busy.value = false }
}
function download(): void {
  const url = URL.createObjectURL(new Blob(['\uFEFF' + text.value + '\n'], { type: 'text/plain;charset=utf-8' }))
  const a = document.createElement('a'); a.href = url; a.download = `HL-Panel-logs-${new Date().toISOString().replace(/[:.]/g, '-')}.txt`; a.click()
  window.setTimeout(() => URL.revokeObjectURL(url), 1000)
}
onMounted(load)
</script>

<template>
  <BaseModal title="日志" width="large" dialog-class="panel-logs-modal" @close="$emit('close')">
    <div class="panel-log-toolbar">
      <button type="button" class="icon-button" aria-label="刷新日志" :disabled="busy" @click="load"><RefreshCw :size="17" :class="{ spin: busy }" /></button>
      <label class="log-limit"><span>条数</span><select v-model.number="limit" aria-label="日志条数" :disabled="busy" @change="load"><option v-for="n in [20, 50, 100, 200, 500, 1000, 2000]" :key="n" :value="n">{{ n }}</option></select></label>
      <label class="log-search"><span>筛选</span><input v-model="filter" aria-label="筛选日志" placeholder="搜索日志" /></label>
      <div class="log-levels"><label v-for="[value, label] in [['INFO', '信息'], ['WARN', '警告'], ['ERROR', '错误']]" :key="value"><input v-model="levels" type="checkbox" :value="value" />{{ label }}</label></div>
      <button class="log-download" type="button" aria-label="下载日志" :disabled="!entries.length || busy" @click="download"><Download :size="18" /></button>
    </div>
    <p v-if="error" role="alert" class="form-error">{{ error }} <button type="button" @click="load">重试</button></p>
    <pre class="panel-log-body" aria-label="面板日志正文" :aria-busy="busy">{{ busy && !logs ? '正在读取面板日志…' : text || (logs?.items.length ? '没有匹配的日志' : '暂无面板日志') }}</pre>
    <div class="log-meta"><span>{{ entries.length }} 条 / 最近 {{ logs?.items.length ?? 0 }} 条</span><span v-if="logs">{{ logs.persistent ? '面板运行日志' : '本次运行日志' }}</span></div>
  </BaseModal>
</template>

<style>
.panel-logs-modal { width: min(1400px, calc(100vw - 48px)); border-radius: 16px; }
@media (max-width: 600px) { .panel-logs-modal { width: 100%; border-radius: 14px 14px 0 0; } }
</style>
<style scoped>
.panel-log-toolbar { display: flex; align-items: center; flex-wrap: wrap; gap: 14px; margin-bottom: 14px; color: #566171; }
.panel-log-toolbar label { display: flex; align-items: center; gap: 8px; }
.panel-log-toolbar select, .panel-log-toolbar input:not([type="checkbox"]) { min-height: 32px; border: 1px solid #d8dfe6; border-radius: 18px; padding: 5px 12px; color: #46536a; background: #fff; min-width: 0; }
.log-search { flex: 1; max-width: 340px; }
.log-search input { width: 100%; }
.log-levels { display: flex; gap: 14px; }
.log-levels input { accent-color: #008e79; }
.log-download { margin-left: auto; width: 36px; height: 36px; border: 0; border-radius: 50%; color: white; background: #008e79; display: grid; place-items: center; cursor: pointer; }
.log-download:disabled { opacity: .45; cursor: default; }
.panel-log-body { height: min(52vh, 520px); min-height: 220px; margin: 0; padding: 12px; overflow: auto; border: 1px solid #95c7bd; border-radius: 15px; background: #f6faf9; color: #485f64; font: 13px/1.75 ui-monospace, Consolas, monospace; white-space: pre; }
.log-meta { display: flex; justify-content: space-between; margin-top: 10px; font-size: 12px; color: #85929e; }
@media (max-width: 600px) { .panel-log-toolbar { gap: 10px; } .log-search { max-width: none; flex-basis: calc(100% - 140px); } .log-levels { gap: 12px; } .panel-log-body { height: 50dvh; font-size: 12px; } }
</style>
