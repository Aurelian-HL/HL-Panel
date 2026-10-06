<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { useRouter } from 'vue-router'
import { Eye, EyeOff, KeyRound, UserRound } from '@lucide/vue'

import { profileApi, type CurrentAdministrator } from '@/api/profile'
import StatePanel from '@/components/StatePanel.vue'
import { toast } from '@/composables/toast'
import { displayError } from '@/lib/displayFormatters'
import { authStore } from '@/stores/auth'

const router = useRouter()
const profile = ref<CurrentAdministrator | null>(null)
const loading = ref(true)
const loadError = ref('')
const currentPassword = ref('')
const newPassword = ref('')
const confirmPassword = ref('')
const showPasswords = ref(false)
const saving = ref(false)
const formError = ref('')
const account = computed(() => profile.value ?? authStore.user.value)

function displayDate(value: string | undefined): string {
  if (!value) return '—'
  const timestamp = Date.parse(value)
  return Number.isNaN(timestamp) ? '—' : new Intl.DateTimeFormat('zh-CN', { dateStyle: 'medium', timeStyle: 'short' }).format(timestamp)
}

async function load(): Promise<void> {
  loading.value = true
  loadError.value = ''
  try { profile.value = await profileApi.current() }
  catch (error) { loadError.value = displayError(error) }
  finally { loading.value = false }
}

async function savePassword(): Promise<void> {
  formError.value = ''
  if (!currentPassword.value || !newPassword.value) { formError.value = '请填写当前密码和新密码'; return }
  if (newPassword.value !== confirmPassword.value) { formError.value = '两次输入的新密码不一致'; return }
  if (currentPassword.value === newPassword.value) { formError.value = '新密码不能与当前密码相同'; return }
  saving.value = true
  try {
    await profileApi.changePassword(currentPassword.value, newPassword.value)
    currentPassword.value = ''
    newPassword.value = ''
    confirmPassword.value = ''
    authStore.logout()
    toast.success('密码已修改', '请使用新密码重新登录')
    await router.replace('/login')
  } catch (error) { formError.value = displayError(error) }
  finally { saving.value = false }
}

onMounted(load)
</script>

<template>
  <div class="page-stack profile-page">
    <header class="page-heading"><div><h2>个人中心</h2></div></header>
    <StatePanel v-if="loading && !account" state="loading" title="正在读取账号资料" />
    <StatePanel v-if="loadError" state="error" title="账号资料加载失败" :message="loadError" @retry="load" />

    <section v-if="account" class="profile-section" aria-label="账号资料">
      <header><UserRound :size="18" /><h3>账号资料</h3></header>
      <dl class="profile-details">
        <div><dt>登录账号</dt><dd>{{ account.username }}</dd></div>
        <div><dt>账号类型</dt><dd>管理员</dd></div>
        <div><dt>创建时间</dt><dd>{{ displayDate(account.created_at) }}</dd></div>
        <div><dt>当前会话有效期</dt><dd>{{ displayDate(authStore.expiresAt.value ?? undefined) }}</dd></div>
      </dl>
    </section>

    <section class="profile-section" aria-label="修改密码">
      <header><KeyRound :size="18" /><h3>修改密码</h3></header>
      <form class="profile-password-form" @submit.prevent="savePassword">
        <label class="field"><span>当前密码</span><input v-model="currentPassword" :type="showPasswords ? 'text' : 'password'" autocomplete="current-password" required /></label>
        <label class="field"><span>新密码</span><input v-model="newPassword" :type="showPasswords ? 'text' : 'password'" autocomplete="new-password" required /></label>
        <label class="field"><span>确认新密码</span><input v-model="confirmPassword" :type="showPasswords ? 'text' : 'password'" autocomplete="new-password" required /></label>
        <button class="profile-visibility" type="button" :aria-label="showPasswords ? '隐藏密码' : '显示密码'" @click="showPasswords = !showPasswords"><EyeOff v-if="showPasswords" :size="16" /><Eye v-else :size="16" />{{ showPasswords ? '隐藏密码' : '显示密码' }}</button>
        <p v-if="formError" class="form-error" role="alert">{{ formError }}</p>
        <footer><button class="button button--primary" type="submit" :disabled="saving">{{ saving ? '保存中' : '修改密码' }}</button></footer>
      </form>
    </section>
  </div>
</template>
