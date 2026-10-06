<script setup lang="ts">
import { Eye, EyeOff, LoaderCircle, LockKeyhole, Network, UserRound } from '@lucide/vue'
import { computed, onMounted, ref } from 'vue'
import { useRoute, useRouter } from 'vue-router'

import { customerAuth } from '@/stores/auth'
import { customerSite } from '@/stores/site'

const route = useRoute()
const router = useRouter()
const username = ref('')
const password = ref('')
const reveal = ref(false)
const busy = ref(false)
const error = ref('')
const siteName = computed(() => customerSite.info.value?.site_name || 'HL-panel')
onMounted(() => { void customerSite.load().catch(() => undefined) })

async function submit(): Promise<void> {
  if (busy.value) return
  busy.value = true
  error.value = ''
  try {
    await customerAuth.login(username.value.trim(), password.value)
    const redirect = typeof route.query.redirect === 'string' && route.query.redirect.startsWith('/') ? route.query.redirect : '/'
    await router.replace(redirect)
  } catch (cause) {
    error.value = cause instanceof Error ? cause.message : '登录失败'
  } finally {
    busy.value = false
  }
}
</script>

<template>
  <main class="login-page">
    <section class="login-box">
      <header><span class="login-mark"><Network :size="23" /></span><div><strong>{{ siteName }}</strong><small>用户中心</small></div></header>
      <form @submit.prevent="submit">
        <label><span>用户名</span><div class="input-icon"><UserRound :size="16" /><input v-model="username" autocomplete="username" maxlength="128" autofocus required /></div></label>
        <label><span>密码</span><div class="input-icon input-action"><LockKeyhole :size="16" /><input v-model="password" :type="reveal ? 'text' : 'password'" autocomplete="current-password" maxlength="4096" required /><button type="button" :aria-label="reveal ? '隐藏密码' : '显示密码'" :title="reveal ? '隐藏密码' : '显示密码'" @click="reveal = !reveal"><EyeOff v-if="reveal" :size="16" /><Eye v-else :size="16" /></button></div></label>
        <p v-if="error" class="form-error" role="alert">{{ error }}</p>
        <button class="button button-primary login-submit" :disabled="busy"><LoaderCircle v-if="busy" class="spin" :size="16" />{{ busy ? '登录中' : '登录' }}</button>
      </form>
    </section>
  </main>
</template>
