import { computed, readonly, ref } from 'vue'

import { api, onUnauthorized, type Administrator, type LoginRequest } from '@/api'

const TOKEN_KEY = 'ny_admin_access_token'
const USER_KEY = 'ny_admin_user'
const EXPIRY_KEY = 'ny_admin_expires_at'

function readUser(): Administrator | null {
  try {
    const value = sessionStorage.getItem(USER_KEY)
    return value ? JSON.parse(value) as Administrator : null
  } catch {
    return null
  }
}

const user = ref<Administrator | null>(readUser())
const token = ref<string | null>(sessionStorage.getItem(TOKEN_KEY))
const expiresAt = ref<string | null>(sessionStorage.getItem(EXPIRY_KEY))

function clearSession(): void {
  token.value = null
  user.value = null
  expiresAt.value = null
  sessionStorage.removeItem(TOKEN_KEY)
  sessionStorage.removeItem(USER_KEY)
  sessionStorage.removeItem(EXPIRY_KEY)
}

if (expiresAt.value && Date.parse(expiresAt.value) <= Date.now()) clearSession()

async function login(request: LoginRequest): Promise<void> {
  const response = await api.login(request)
  token.value = response.access_token
  user.value = response.user
  expiresAt.value = response.expires_at
  sessionStorage.setItem(TOKEN_KEY, response.access_token)
  sessionStorage.setItem(USER_KEY, JSON.stringify(response.user))
  sessionStorage.setItem(EXPIRY_KEY, response.expires_at)
}

onUnauthorized(clearSession)

export const authStore = {
  user: readonly(user),
  expiresAt: readonly(expiresAt),
  isAuthenticated: computed(() => Boolean(token.value && user.value)),
  login,
  logout: clearSession,
}
