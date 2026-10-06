<script setup lang="ts">
import { computed, reactive, ref } from 'vue'
import BaseModal from '@/components/BaseModal.vue'
import type { Announcement, AnnouncementInput, AnnouncementLevel } from '@/api/siteTypes'
import { siteApi } from '@/api/site'
import { useMutationKey } from '@/composables/useMutationKey'

const props = defineProps<{ announcement: Announcement | null }>()
const emit = defineEmits<{ close: []; saved: [announcement: Announcement] }>()

const mutationKey = useMutationKey()
const busy = ref(false)
const error = ref('')
const levels: { value: AnnouncementLevel; label: string }[] = [
  { value: 'info', label: '普通公告' },
  { value: 'warning', label: '重要提醒' },
  { value: 'maintenance', label: '维护通知' },
]
function toLocalInput(value: string | null): string {
  if (!value) return ''
  const date = new Date(value)
  if (Number.isNaN(date.getTime())) return ''
  return new Date(date.getTime() - date.getTimezoneOffset() * 60_000).toISOString().slice(0, 16)
}
const form = reactive({
  title: props.announcement?.title ?? '',
  content: props.announcement?.content ?? '',
  level: props.announcement?.level ?? 'info' as AnnouncementLevel,
  enabled: props.announcement?.enabled ?? true,
  starts_at: toLocalInput(props.announcement?.starts_at ?? null),
  ends_at: toLocalInput(props.announcement?.ends_at ?? null),
  sort_order: props.announcement?.sort_order ?? 100,
})
const modalTitle = computed(() => props.announcement ? '编辑公告' : '发布公告')

function isoOrNull(value: string): string | null {
  if (!value) return null
  const date = new Date(value)
  return Number.isNaN(date.getTime()) ? null : date.toISOString()
}

async function submit(): Promise<void> {
  error.value = ''
  if (!form.title.trim()) { error.value = '请填写公告标题'; return }
  if (!form.content.trim()) { error.value = '请填写公告内容'; return }
  const startsAt = isoOrNull(form.starts_at)
  const endsAt = isoOrNull(form.ends_at)
  if (form.starts_at && !startsAt) { error.value = '开始时间格式不正确'; return }
  if (form.ends_at && !endsAt) { error.value = '结束时间格式不正确'; return }
  if (startsAt && endsAt && new Date(endsAt) <= new Date(startsAt)) { error.value = '结束时间必须晚于开始时间'; return }
  const input: AnnouncementInput = {
    title: form.title.trim(), content: form.content.trim(), level: form.level, enabled: form.enabled,
    starts_at: startsAt, ends_at: endsAt, sort_order: Number(form.sort_order), revision: props.announcement?.revision ?? 0,
  }
  busy.value = true
  try {
    emit('saved', await siteApi.saveAnnouncement(input, props.announcement?.id ?? null, mutationKey(input)))
  } catch (cause) {
    error.value = cause instanceof Error ? cause.message : '公告保存失败'
  } finally { busy.value = false }
}
</script>

<template>
  <BaseModal :title="modalTitle" description="公告会按生效时间显示在首页和用户信息页。" :close-disabled="busy" @close="emit('close')">
    <form class="form-stack ny-compact-form" @submit.prevent="submit">
      <label class="field"><span>标题</span><input v-model="form.title" maxlength="120" required /></label>
      <label class="field"><span>内容</span><textarea v-model="form.content" rows="6" maxlength="10000" required /></label>
      <div class="field-grid">
        <label class="field"><span>类型</span><select v-model="form.level"><option v-for="level in levels" :key="level.value" :value="level.value">{{ level.label }}</option></select></label>
        <label class="field"><span>排序</span><input v-model.number="form.sort_order" type="number" min="-100000" max="100000" /></label>
      </div>
      <div class="field-grid">
        <label class="field"><span>开始时间</span><input v-model="form.starts_at" type="datetime-local" /></label>
        <label class="field"><span>结束时间</span><input v-model="form.ends_at" type="datetime-local" /></label>
      </div>
      <label class="toggle-field"><input v-model="form.enabled" type="checkbox" /><span>立即启用</span></label>
      <p v-if="error" class="form-error" role="alert">{{ error }}</p>
    </form>
    <template #footer><button class="button button--secondary" type="button" :disabled="busy" @click="emit('close')">取消</button><button class="button button--primary" type="button" :disabled="busy" @click="submit">{{ busy ? '保存中…' : '保存公告' }}</button></template>
  </BaseModal>
</template>
