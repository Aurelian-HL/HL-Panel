<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { RefreshCw } from '@lucide/vue'
import BaseModal from '@/components/BaseModal.vue'
import { checkVersion, repositoryUrl, updateCommand, type VersionStatus } from '@/api/releases'
import { displayError } from '@/lib/displayFormatters'

const props = defineProps<{ current: string }>()
defineEmits<{ close: [] }>()
const status = ref<VersionStatus | null>(null)
const busy = ref(false)
const error = ref('')
const selected = ref(props.current)
const copied = ref(false)
const versions = computed(() => status.value?.versions ?? [])
const target = computed(() => versions.value.find(v => v.tag === selected.value))
const command = computed(() => !busy.value && !error.value && target.value?.can_update && /^v(0|[1-9]\d*)\.(0|[1-9]\d*)\.(0|[1-9]\d*)$/.test(selected.value) ? `${updateCommand} -s -- --version ${selected.value}` : '')
async function load(): Promise<void> {
  if (busy.value) return
  busy.value = true; error.value = ''
  try { status.value = await checkVersion(); selected.value = status.value.current_version; copied.value = false } catch (cause) { error.value = displayError(cause) } finally { busy.value = false }
}
async function copy(): Promise<void> { try { await navigator.clipboard.writeText(command.value); copied.value = true } catch { error.value = '复制失败，请选中命令手动复制' } }
onMounted(load)
</script>

<template>
  <BaseModal title="版本" width="small" dialog-class="panel-version-modal" @close="$emit('close')">
    <div class="version-panel-heading"><strong>HL-Panel</strong><button class="icon-button" type="button" aria-label="刷新版本列表" :disabled="busy" @click="load"><RefreshCw :size="15" :class="{ spin: busy }" /></button></div>
    <div class="version-warning">更新前请查看发布说明。保数据更新会先备份账号、规则与配置。</div>
    <p v-if="error" class="form-error" role="alert">{{ error }} <button @click="load">重试</button></p>
    <p v-if="busy && !status" class="version-loading">正在读取 GitHub 正式版本…</p>
    <p v-else-if="status?.state === 'check_failed'" class="form-error" role="alert">{{ status.message }}</p>
    <div v-if="versions.length" class="panel-version-list" role="radiogroup" aria-label="HL-Panel 正式版本">
      <label v-for="(version, index) in versions" :key="version.tag" class="panel-version-row" :class="{ 'is-current': version.current, 'is-disabled': !version.current && !version.can_update }">
        <span class="version-tag" :class="{ alternate: index % 2 === 0 }">{{ version.tag }}</span><input v-model="selected" type="radio" name="panel-version" :value="version.tag" :disabled="!version.current && !version.can_update" :aria-label="version.tag + (version.current ? ' 当前版本' : '')" @change="copied = false" /><span v-if="version.current" class="version-row-meta">当前</span><span v-else-if="index === 0" class="version-row-meta">最新</span>
      </label>
    </div>
    <p v-if="!busy && !versions.some(v => v.current)" class="version-current">当前版本：{{ status?.current_version || current }}</p>
    <div v-if="command" class="selected-update"><strong>保数据更新至 {{ selected }}</strong><p>在面板机终端以 root 执行：</p><pre aria-label="保数据更新命令">{{ command }}</pre><button type="button" class="button button--primary" @click="copy">{{ copied ? '已复制' : '复制更新命令' }}</button><p>更新会短暂停止本面板；终端会显示备份目录及回滚命令。完成后刷新页面。</p></div>
    <p class="version-footnote">当前版本已标记。旧版本仅供查看，自动更新不支持降级。</p>
    <a :href="target?.release_url || repositoryUrl + '/releases'" target="_blank" rel="noopener noreferrer" class="version-release-link">查看 GitHub 发布说明 ↗</a>
  </BaseModal>
</template>

<style>
.panel-version-modal { width: min(440px, calc(100vw - 40px)); border-radius: 12px; }
@media (max-width: 600px) { .panel-version-modal { width: 100%; border-radius: 12px 12px 0 0; } }
</style>
<style scoped>
.version-panel-heading { display: flex; align-items: center; justify-content: space-between; padding: 0 0 12px; }
.version-warning { padding: 10px 12px; margin-bottom: 12px; background: #fff6ed; border: 1px solid #ffbf88; border-radius: 8px; color: #925c31; font-size: 12px; line-height: 1.6; }
.panel-version-list { max-height: 48dvh; overflow-y: auto; border: 1px solid #d8dfe5; border-radius: 10px; }
.panel-version-row { display: flex; align-items: center; gap: 10px; min-height: 38px; padding: 8px 12px; border-bottom: 1px solid #e5eaf0; cursor: pointer; }
.panel-version-row:last-child { border-bottom: 0; }
.panel-version-row input { accent-color: #008e79; }
.version-tag { border: 1px solid #8cd3c0; background: #f0fbf7; color: #21846e; border-radius: 20px; padding: 2px 9px; font-size: 12px; }
.version-tag.alternate { border-color: #d3b7d7; color: #93639c; background: #fbf4fc; }
.is-current { background: #f3fbf8; }
.is-disabled { cursor: default; }
.version-row-meta { margin-left: auto; color: #82948f; font-size: 12px; }
.version-footnote, .version-current, .version-loading { color: #7b8897; font-size: 12px; line-height: 1.7; }
.version-release-link { color: #21846e; font-size: 13px; }
.selected-update { margin-top: 14px; padding: 12px; background: #f5f8fa; border-radius: 8px; font-size: 12px; line-height: 1.7; }
.selected-update p { margin: 6px 0; color: #667688; }
.selected-update pre { white-space: pre-wrap; overflow-wrap: anywhere; font-size: 11px; color: #344c65; }
</style>
