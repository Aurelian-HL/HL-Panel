import { computed, readonly, ref } from 'vue'

import { api, ApiError, onUnauthorized, type Administrator, type LoginRequest } from '@/api'

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

// Only the server can expire a persisted session; browser time may be skewed.

async function login(request: LoginRequest): Promise<'administrator' | 'panel_account'> {
  let response
  try {
    response = await api.login(request)
  } catch (error) {
    // Only rejected credentials may try the other identity namespace. An
    // outage or invalid response must remain visible and preserve the session.
    if (!(error instanceof ApiError) || error.status !== 401) throw error
    const account = await api.loginPanelAccount(request)
    sessionStorage.setItem('ny_customer_user', JSON.stringify(account.user))
    sessionStorage.setItem('ny_customer_access_token', account.access_token)
    sessionStorage.setItem('ny_customer_expires_at', account.expires_at)
    clearSession()
    return 'panel_account'
  }
  token.value = response.access_token
  user.value = response.user
  expiresAt.value = response.expires_at
  sessionStorage.setItem(TOKEN_KEY, response.access_token)
  sessionStorage.setItem(USER_KEY, JSON.stringify(response.user))
  sessionStorage.setItem(EXPIRY_KEY, response.expires_at)
  sessionStorage.removeItem('ny_customer_user')
  sessionStorage.removeItem('ny_customer_access_token')
  sessionStorage.removeItem('ny_customer_expires_at')
  return 'administrator'
}

onUnauthorized(clearSession)

export const authStore = {
  user: readonly(user),
  expiresAt: readonly(expiresAt),
  isAuthenticated: computed(() => Boolean(token.value && user.value)),
  login,
  logout: clearSession,
}
