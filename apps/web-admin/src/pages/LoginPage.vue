<script setup lang="ts">
import { computed, onMounted, ref, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { Contrast, Eye, EyeOff, LoaderCircle, LockKeyhole, Moon, UserRound } from '@lucide/vue'

import { authStore } from '@/stores/auth'
import { siteStore } from '@/stores/site'
import { displayError } from '@/lib/displayFormatters'

const route = useRoute()
const router = useRouter()
const username = ref('')
const password = ref('')
const revealPassword = ref(false)
const submitting = ref(false)
const errorMessage = ref('')
const darkMode = ref(false)
const highContrast = ref(false)
onMounted(() => { void siteStore.load().catch(() => undefined) })
watch(() => siteStore.panelTitle.value, (title) => {
  document.title = title
}, { immediate: true })

const destination = computed(() => {
  const redirect = typeof route.query.redirect === 'string' ? route.query.redirect : '/overview'
  return redirect.startsWith('/') && !redirect.startsWith('//') ? redirect : '/overview'
})

async function submit(): Promise<void> {
  errorMessage.value = ''
  if (!username.value.trim() || !password.value) {
    errorMessage.value = '请输入管理员账号和密码'
    return
  }
  submitting.value = true
  try {
    await authStore.login({ username: username.value.trim(), password: password.value })
    await router.replace(destination.value)
  } catch (error) {
    errorMessage.value = displayError(error)
  } finally {
    submitting.value = false
  }
}
</script>

<template>
  <main class="login-page" :class="{ 'login-page--dark': darkMode, 'login-page--contrast': highContrast }">
    <header class="login-topbar">
      <div class="login-topbar__brand">
        {{ siteStore.siteName.value }}
      </div>
      <div class="login-topbar__actions">
        <span class="login-session-state"><span>未登录</span><UserRound :size="16" aria-hidden="true" /></span>
        <button class="login-topbar__icon" type="button" aria-label="切换深色模式" title="切换深色模式" :aria-pressed="darkMode" @click="darkMode = !darkMode"><Moon :size="17" /></button>
        <button class="login-topbar__icon" type="button" aria-label="切换高对比度" title="切换高对比度" :aria-pressed="highContrast" @click="highContrast = !highContrast"><Contrast :size="17" /></button>
      </div>
    </header>

    <section class="login-panel" aria-labelledby="login-title">
      <header class="login-panel__header"><h1 id="login-title">登录</h1></header>
      <form class="login-form" @submit.prevent="submit">
        <label class="ny-login-field">
          <span class="sr-only">管理员账号</span>
          <UserRound :size="16" aria-hidden="true" />
          <input v-model="username" autocomplete="username" inputmode="text" maxlength="128" autofocus placeholder="账号" />
        </label>
        <label class="ny-login-field">
          <span class="sr-only">密码</span>
          <LockKeyhole :size="16" aria-hidden="true" />
          <input v-model="password" :type="revealPassword ? 'text' : 'password'" autocomplete="current-password" maxlength="256" placeholder="密码" />
          <button type="button" class="input-action" :aria-label="revealPassword ? '隐藏密码' : '显示密码'" :title="revealPassword ? '隐藏密码' : '显示密码'" @click="revealPassword = !revealPassword">
            <EyeOff v-if="revealPassword" :size="15" />
            <Eye v-else :size="15" />
          </button>
        </label>
        <p v-if="errorMessage" class="form-error" role="alert">{{ errorMessage }}</p>
        <button class="ny-login-submit" type="submit" :disabled="submitting">
          <LoaderCircle v-if="submitting" class="spin" :size="15" />
          {{ submitting ? '登录中' : '登录' }}
        </button>
      </form>
    </section>
  </main>
</template>
