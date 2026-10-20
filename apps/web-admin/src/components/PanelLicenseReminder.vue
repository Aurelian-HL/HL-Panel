<script setup lang="ts">
import { computed, nextTick, onBeforeUnmount, ref, watch } from 'vue'
import { authStore } from '@/stores/auth'
import { panelLicenseStore as store } from '@/stores/panelLicense'
import { panelLicenseApi } from '@/api/panelLicense'
const busy = ref(false)
const error = ref('')
const dialog = ref<HTMLElement | null>(null)
let previousFocus: HTMLElement | null = null
let timer: ReturnType<typeof setInterval> | undefined
const due = computed(() => authStore.isAuthenticated.value && store.status.value?.enabled && store.status.value.reminder_due)
async function dismiss(): Promise<void> {
  if (busy.value) return
  const generation = store.generation()
  busy.value = true; error.value = ''
  try { store.apply(await panelLicenseApi.dismiss(), generation) }
  catch { if (generation === store.generation()) error.value = '提醒设置未保存，请重试' }
  finally { busy.value = false }
}
function keyboard(event: KeyboardEvent): void {
  if (event.key === 'Escape') { event.preventDefault(); void dismiss(); return }
  if (event.key !== 'Tab') return
  const elements = dialog.value?.querySelectorAll<HTMLElement>('a[href],button:not(:disabled)')
  if (!elements?.length) return
  const first = elements[0]!, last = elements[elements.length - 1]!
  if (event.shiftKey && document.activeElement === first) { event.preventDefault(); last.focus() }
  else if (!event.shiftKey && document.activeElement === last) { event.preventDefault(); first.focus() }
}
watch(due, async (visible) => {
  if (visible) { previousFocus = document.activeElement as HTMLElement; await nextTick(); dialog.value?.querySelector<HTMLElement>('button')?.focus() }
  else { previousFocus?.focus(); previousFocus = null }
})
watch(() => authStore.user.value?.id, (administrator) => {
  if (timer) clearInterval(timer)
  store.clear()
  if (administrator) { void store.refresh(); timer = setInterval(() => { if (!document.hidden) void store.refresh() }, 60_000) }
}, { immediate: true })
onBeforeUnmount(() => { if (timer) clearInterval(timer); previousFocus?.focus() })
</script>
<template>
  <div v-if="due" class="license-reminder-backdrop">
    <section ref="dialog" role="alertdialog" aria-modal="true" aria-labelledby="license-reminder-title" aria-describedby="license-reminder-description" class="license-reminder" @keydown="keyboard">
      <small>HL-PANEL · 域名授权</small><h2 id="license-reminder-title">{{ store.status.value?.status === 'expired' ? '授权已到期' : '授权即将到期' }}</h2>
      <p id="license-reminder-description">到期时间：{{ new Date(store.status.value?.expires_at || '').toLocaleString('zh-CN', { timeZone: 'Asia/Shanghai' }) }}（北京时间）。感谢您的支持！如觉得对您有帮助，还请继续支持，诚挚感谢。</p>
      <p>到期将限制新增、修改和删除，现有转发、订阅及节点运行继续保留。</p>
      <p v-if="error" role="alert">{{ error }}</p>
      <footer><a href="https://ht.hongle.work/" target="_blank" rel="noopener noreferrer" class="button button--primary">立即支持</a><button autofocus class="button button--secondary" :disabled="busy" @click="dismiss">{{ busy ? '保存中…' : '今日不再提醒' }}</button></footer>
    </section>
  </div>
</template>
<style scoped>
.license-reminder-backdrop { position: fixed; inset: 0; z-index: 1200; background: #172c4859; display: grid; place-items: center; padding: 20px; }.license-reminder { width: min(100%, 490px); box-sizing: border-box; background: white; border-radius: 18px; padding: 28px; box-shadow: 0 22px 70px #12253d30; color: #24364e; }.license-reminder small { color: #8d6e3c; letter-spacing: 1px; }.license-reminder h2 { margin: 12px 0; }.license-reminder p { font-size: 14px; line-height: 1.8; }.license-reminder footer { display: flex; flex-wrap: wrap; gap: 12px; padding-top: 12px; }@media(max-width:480px) { .license-reminder { padding: 22px; }.license-reminder footer { display: grid; } }
</style>
