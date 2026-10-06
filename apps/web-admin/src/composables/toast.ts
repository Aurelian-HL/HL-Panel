import { readonly, ref } from 'vue'

export interface ToastMessage {
  id: number
  type: 'success' | 'error' | 'info'
  title: string
  message?: string
}

const messages = ref<ToastMessage[]>([])
let sequence = 0

function remove(id: number): void {
  messages.value = messages.value.filter((message) => message.id !== id)
}

function push(type: ToastMessage['type'], title: string, message?: string): void {
  const id = ++sequence
  messages.value.push({ id, type, title, message })
  window.setTimeout(() => remove(id), type === 'error' ? 6500 : 4200)
}

export const toast = {
  messages: readonly(messages),
  success: (title: string, message?: string) => push('success', title, message),
  error: (title: string, message?: string) => push('error', title, message),
  info: (title: string, message?: string) => push('info', title, message),
  remove,
}
