import { computed, readonly, ref } from 'vue'

import { customerApi, onUnauthorized, TOKEN_KEY, type CustomerProfile } from '@/api'

const USER_KEY = 'ny_customer_user'
const EXPIRY_KEY = 'ny_customer_expires_at'

function storedUser(): CustomerProfile | null {
  try {
    const value = sessionStorage.getItem(USER_KEY)
    return value ? JSON.parse(value) as CustomerProfile : null
  } catch {
    return null
  }
}

const user = ref<CustomerProfile | null>(storedUser())
const token = ref(sessionStorage.getItem(TOKEN_KEY))
const expiresAt = ref(sessionStorage.getItem(EXPIRY_KEY))

function clear(): void {
  user.value = null
  token.value = null
  expiresAt.value = null
  sessionStorage.removeItem(USER_KEY)
  sessionStorage.removeItem(TOKEN_KEY)
  sessionStorage.removeItem(EXPIRY_KEY)
}

if (expiresAt.value && Date.parse(expiresAt.value) <= Date.now()) clear()

async function login(username: string, password: string): Promise<void> {
  const result = await customerApi.login(username, password)
  user.value = result.user
  token.value = result.access_token
  expiresAt.value = result.expires_at
  sessionStorage.setItem(USER_KEY, JSON.stringify(result.user))
  sessionStorage.setItem(TOKEN_KEY, result.access_token)
  sessionStorage.setItem(EXPIRY_KEY, result.expires_at)
}

function updateUser(value: CustomerProfile): void {
  user.value = value
  sessionStorage.setItem(USER_KEY, JSON.stringify(value))
}

async function logout(): Promise<void> {
  try { await customerApi.logout() } finally { clear() }
}

onUnauthorized(clear)

export const customerAuth = {
  user: readonly(user),
  isAuthenticated: computed(() => Boolean(user.value && token.value)),
  login,
  logout,
  clear,
  updateUser,
}
