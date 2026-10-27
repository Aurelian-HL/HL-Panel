import { computed, onMounted, onUnmounted, ref, watch, type Ref } from 'vue'

import type { ProbeMemberMove } from './memberOrder'

export function useProbeMemberSorting(items: () => readonly { node_id: string }[], enabled: () => boolean, move: (value: ProbeMemberMove) => void, inventory: Ref<HTMLElement | null>) {
  const draggingId = ref('')
  const targetId = ref('')
  const targetBeforeSource = computed(() => items().findIndex(item => item.node_id === draggingId.value) > items().findIndex(item => item.node_id === targetId.value))
  let pointer: { id: number; nodeId: string; x: number; y: number; handle: HTMLElement } | null = null

  function cancel(): void {
    const previous = pointer
    pointer = null
    draggingId.value = ''
    targetId.value = ''
    if (previous?.handle.hasPointerCapture?.(previous.id)) previous.handle.releasePointerCapture(previous.id)
  }

  function start(memberId: string, event: PointerEvent): void {
    if (!enabled() || event.button !== 0 || !event.isPrimary || items().length < 2) return
    cancel()
    const handle = event.currentTarget as HTMLElement
    handle.focus({ preventScroll: true })
    pointer = { id: event.pointerId, nodeId: memberId, x: event.clientX, y: event.clientY, handle }
    handle.setPointerCapture(event.pointerId)
    event.preventDefault()
  }

  function locate(event: PointerEvent): void {
    const row = document.elementFromPoint(event.clientX, event.clientY)?.closest<HTMLElement>('[data-probe-sort-id]')
    const id = row && inventory.value?.contains(row) ? row.dataset.probeSortId : ''
    targetId.value = id && id !== pointer?.nodeId && items().some(item => item.node_id === id) ? id : ''
  }

  function track(event: PointerEvent): void {
    if (!pointer || pointer.id !== event.pointerId) return
    if (!draggingId.value && Math.hypot(event.clientX - pointer.x, event.clientY - pointer.y) < 6) return
    draggingId.value = pointer.nodeId
    locate(event)
    if (event.clientY < 56) window.scrollBy({ top: -18 })
    else if (event.clientY > window.innerHeight - 56) window.scrollBy({ top: 18 })
    event.preventDefault()
  }

  function finish(event: PointerEvent): void {
    if (!pointer || pointer.id !== event.pointerId) return
    if (draggingId.value) locate(event)
    const result = draggingId.value && targetId.value ? { nodeId: draggingId.value, targetId: targetId.value } : null
    cancel()
    if (enabled() && result) move(result)
  }

  function keyboard(memberId: string, event: KeyboardEvent): void {
    if (event.key === 'Escape') { cancel(); return }
    if (!enabled() || !['ArrowUp', 'ArrowDown', 'Home', 'End'].includes(event.key)) return
    event.preventDefault()
    const rows = items()
    const index = rows.findIndex(item => item.node_id === memberId)
    if (index < 0) return
    const next = event.key === 'Home' ? 0 : event.key === 'End' ? rows.length - 1 : index + (event.key === 'ArrowUp' ? -1 : 1)
    const target = rows[next]
    if (target && target.node_id !== memberId) move({ nodeId: memberId, targetId: target.node_id })
  }

  function classes(memberId: string): Record<string, boolean> {
    return {
      'probe-sort-dragging': draggingId.value === memberId,
      'probe-sort-target--before': targetId.value === memberId && targetBeforeSource.value,
      'probe-sort-target--after': targetId.value === memberId && !targetBeforeSource.value,
    }
  }

  function escape(event: KeyboardEvent): void { if (event.key === 'Escape') cancel() }
  watch(() => items().map(item => item.node_id), ids => {
    if (pointer && (!ids.includes(pointer.nodeId) || (targetId.value && !ids.includes(targetId.value)))) cancel()
  })
  watch(enabled, value => { if (!value) cancel() })
  onMounted(() => {
    window.addEventListener('blur', cancel)
    window.addEventListener('keydown', escape)
  })
  onUnmounted(() => {
    cancel()
    window.removeEventListener('blur', cancel)
    window.removeEventListener('keydown', escape)
  })
  return { draggingId, classes, start, track, finish, cancel, keyboard }
}
