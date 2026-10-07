<script setup lang="ts">
import { onMounted, reactive, ref } from 'vue'
import { Megaphone, Pencil, Plus, RefreshCw, Save, Settings2 } from '@lucide/vue'
import StatePanel from '@/components/StatePanel.vue'
import VersionNotice from '@/components/VersionNotice.vue'
import AnnouncementEditor from '@/features/announcements/AnnouncementEditor.vue'
import { siteApi } from '@/api/site'
import type { Announcement, SiteSettings, SiteSettingsInput } from '@/api/siteTypes'
import { useMutationKey } from '@/composables/useMutationKey'
import { toast } from '@/composables/toast'
import { siteStore } from '@/stores/site'

const settings = ref<SiteSettings | null>(null)
const announcements = ref<Announcement[]>([])
const loading = ref(true)
const saving = ref(false)
const error = ref('')
const editor = ref<Announcement | null | undefined>(undefined)
const mutationKey = useMutationKey()
const form = reactive<SiteSettingsInput>({ site_name: '', panel_title: '', public_description: '', support_url: '', theme: 'classic', background_image_url: '', revision: 0 })

function setForm(value: SiteSettings): void {
  Object.assign(form, {
    site_name: value.site_name, panel_title: value.panel_title, public_description: value.public_description,
    support_url: value.support_url, theme: value.theme, background_image_url: value.background_image_url, revision: value.revision,
  })
}

async function load(): Promise<void> {
  loading.value = true; error.value = ''
  try {
    const [siteSettings, items] = await Promise.all([siteApi.settings(), siteApi.announcements()])
    settings.value = siteSettings; announcements.value = items; setForm(siteSettings)
  } catch (cause) { error.value = cause instanceof Error ? cause.message : '系统设置加载失败' }
  finally { loading.value = false }
}

async function saveSettings(): Promise<void> {
  if (!form.site_name.trim() || !form.panel_title.trim()) { toast.error('请填写站点名称和面板标题'); return }
  saving.value = true
  try {
    const input = { ...form, site_name: form.site_name.trim(), panel_title: form.panel_title.trim(), public_description: form.public_description.trim(), support_url: form.support_url.trim(), background_image_url: form.background_image_url.trim() }
    const saved = await siteApi.saveSettings(input, mutationKey(input))
    settings.value = saved; setForm(saved); await siteStore.refresh(); toast.success('系统设置已保存')
  } catch (cause) { toast.error('系统设置保存失败', cause instanceof Error ? cause.message : undefined) }
  finally { saving.value = false }
}

function savedAnnouncement(value: Announcement): void {
  const index = announcements.value.findIndex((item) => item.id === value.id)
  if (index >= 0) announcements.value[index] = value; else announcements.value.push(value)
  editor.value = undefined; toast.success('公告已保存', value.title)
}

onMounted(load)
</script>

<template>
  <div class="page-stack system-settings-page">
    <header class="page-heading"><div><h2>系统设置</h2><p>管理站点公开信息、主题和首页公告</p></div><div class="page-heading__actions"><button class="button button--secondary" :disabled="loading" @click="load"><RefreshCw :size="16" :class="{ spin: loading }" />刷新</button></div></header>
    <VersionNotice />
    <StatePanel v-if="loading && !settings" state="loading" title="正在读取系统设置" />
    <StatePanel v-else-if="error && !settings" state="error" title="系统设置加载失败" :message="error" @retry="load" />
    <template v-else-if="settings">
      <section class="settings-section">
        <header><span class="settings-section__icon"><Settings2 :size="18" /></span><div><h3>站点信息</h3><p>用于登录页、首页和用户信息页，不改变数据面协议。</p></div></header>
        <form class="settings-form" @submit.prevent="saveSettings">
          <div class="field-grid"><label class="field"><span>站点名称</span><input v-model="form.site_name" maxlength="80" required /></label><label class="field"><span>面板标题</span><input v-model="form.panel_title" maxlength="120" required /></label></div>
          <label class="field"><span>公开说明</span><textarea v-model="form.public_description" maxlength="2000" rows="4" /></label>
          <div class="field-grid"><label class="field"><span>帮助地址</span><input v-model="form.support_url" type="url" maxlength="2048" placeholder="https://" /></label><label class="field"><span>界面主题</span><select v-model="form.theme"><option value="classic">经典主题</option><option value="transparent">透明主题</option></select></label></div>
          <label class="field"><span>背景图片地址</span><input v-model="form.background_image_url" type="url" maxlength="2048" placeholder="https://" /><small class="field-help">透明主题使用；只接受 HTTPS 图片地址。</small></label>
          <footer><button class="button button--primary" type="submit" :disabled="saving"><Save :size="15" />{{ saving ? '保存中…' : '保存设置' }}</button></footer>
        </form>
      </section>

      <section class="settings-section announcement-management">
        <header><span class="settings-section__icon"><Megaphone :size="18" /></span><div><h3>站点公告</h3><p>支持生效时间、结束时间和维护级别。</p></div><button class="button button--primary" @click="editor = null"><Plus :size="15" />发布公告</button></header>
        <StatePanel v-if="!announcements.length" state="empty" title="暂无公告" message="发布后，启用且在有效期内的公告会显示在首页。" />
        <div v-else class="table-wrap"><table class="data-table announcement-table"><thead><tr><th>公告</th><th>类型</th><th>有效期</th><th>状态</th><th>操作</th></tr></thead><tbody><tr v-for="item in announcements" :key="item.id"><td><strong class="table-primary">{{ item.title }}</strong><span class="table-secondary announcement-preview">{{ item.content }}</span></td><td>{{ item.level === 'maintenance' ? '维护' : item.level === 'warning' ? '重要' : '普通' }}</td><td><span class="table-secondary">{{ item.starts_at ? new Date(item.starts_at).toLocaleString() : '立即' }}<br />至 {{ item.ends_at ? new Date(item.ends_at).toLocaleString() : '长期' }}</span></td><td><span class="business-status" :class="{ 'business-status--muted': !item.enabled }">{{ item.enabled ? '已启用' : '已停用' }}</span></td><td><button class="button button--quiet" @click="editor = item"><Pencil :size="14" />编辑</button></td></tr></tbody></table></div>
      </section>
    </template>
    <AnnouncementEditor v-if="editor !== undefined" :announcement="editor" @close="editor = undefined" @saved="savedAnnouncement" />
  </div>
</template>
