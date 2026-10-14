import { nextTick, onBeforeUnmount, onMounted, type Ref } from 'vue'

const dialogs: HTMLElement[] = []
let wasScrollLocked = false

function focusable(dialog: HTMLElement): HTMLElement[] {
  return [...dialog.querySelectorAll<HTMLElement>('button, input, select, textarea, a[href], [tabindex]')]
    .filter(element => !element.matches(':disabled, [tabindex="-1"]') && !element.closest('[hidden], [inert]'))
    .filter(element => {
      for (let parent: HTMLElement | null = element; parent; parent = parent.parentElement) {
        const style = getComputedStyle(parent)
        if (style.display === 'none' || style.visibility === 'hidden') return false
      }
      return true
    })
}

export function useDialogFocus(dialogRef: Ref<HTMLElement | null>, close: () => void): void {
  let dialog: HTMLElement | null = null
  let previous: HTMLElement | null = null
  const isTop = () => dialog !== null && dialogs.at(-1) === dialog
  const focus = () => (dialog && (focusable(dialog)[0] ?? dialog))?.focus({ preventScroll: true })
  function onFocus(event: FocusEvent): void {
    if (isTop() && !dialog?.contains(event.target as Node)) focus()
  }
  function onKeydown(event: KeyboardEvent): void {
    if (!isTop() || event.defaultPrevented) return
    if (event.key === 'Escape') {
      event.preventDefault()
      close()
    } else if (event.key === 'Tab' && dialog) {
      const items = focusable(dialog)
      const first = items[0], last = items.at(-1)
      if (!first || !last) { event.preventDefault(); dialog.focus(); return }
      const active = document.activeElement
      if (!dialog.contains(active) || (event.shiftKey ? active === first || active === dialog : active === last || active === dialog)) {
        event.preventDefault()
        ;(event.shiftKey ? last : first).focus()
      }
    }
  }
  onMounted(() => {
    dialog = dialogRef.value
    if (!dialog) return
    previous = document.activeElement instanceof HTMLElement ? document.activeElement : null
    if (!dialogs.length) wasScrollLocked = document.body.classList.contains('modal-open')
    dialogs.push(dialog)
    document.body.classList.add('modal-open')
    window.addEventListener('keydown', onKeydown)
    document.addEventListener('focusin', onFocus)
    void nextTick(() => { if (isTop()) focus() })
  })
  onBeforeUnmount(() => {
    const wasTop = isTop()
    if (dialog) {
      const index = dialogs.indexOf(dialog)
      if (index >= 0) dialogs.splice(index, 1)
    }
    window.removeEventListener('keydown', onKeydown)
    document.removeEventListener('focusin', onFocus)
    if (!dialogs.length && !wasScrollLocked) document.body.classList.remove('modal-open')
    if (wasTop) {
      const top = dialogs.at(-1)
      if (previous?.isConnected && (!top || top.contains(previous))) previous.focus({ preventScroll: true })
      else if (top) (focusable(top)[0] ?? top).focus({ preventScroll: true })
    }
    dialog = null
  })
}
