import { afterEach, vi } from 'vitest'

afterEach(() => {
  document.body.innerHTML = ''
  sessionStorage.clear()
  vi.restoreAllMocks()
})
