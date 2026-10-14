import { flushPromises, mount } from '@vue/test-utils'
import { afterEach, describe, expect, it } from 'vitest'
import BaseModal from './BaseModal.vue'

const wrappers: ReturnType<typeof mount>[] = []
function open(title = '测试', closeDisabled = false) {
  const wrapper = mount(BaseModal, { props: { title, closeDisabled }, attachTo: document.body,
    slots: { default: '<input aria-label="内容" /><button disabled>不可用</button><button style="display:none">隐藏</button>', footer: '<button>保存</button>' } })
  wrappers.push(wrapper)
  return wrapper
}
function key(value: string, shiftKey = false) {
  window.dispatchEvent(new KeyboardEvent('keydown', { key: value, shiftKey, cancelable: true }))
}
afterEach(() => { wrappers.splice(0).reverse().forEach(wrapper => wrapper.unmount()); document.body.innerHTML = ''; document.body.classList.remove('modal-open') })

describe('BaseModal keyboard lifecycle', () => {
  it('contains focus, wraps Tab in both directions and restores the opener', async () => {
    const opener = document.createElement('button'); document.body.append(opener); opener.focus()
    const wrapper = open(); await flushPromises()
    const dialog = document.querySelector<HTMLElement>('[role="dialog"]')!
    const first = dialog.querySelector<HTMLButtonElement>('button')!
    const last = dialog.querySelector<HTMLButtonElement>('footer button')!
    expect(document.activeElement).toBe(first)
    key('Tab', true); expect(document.activeElement).toBe(last)
    key('Tab'); expect(document.activeElement).toBe(first)
    opener.focus(); expect(dialog.contains(document.activeElement)).toBe(true)
    wrapper.unmount(); wrappers.pop()
    expect(document.activeElement).toBe(opener)
    expect(document.body.classList.contains('modal-open')).toBe(false)
  })
  it('only closes the top dialog and retains the scroll lock for the remaining one', async () => {
    const lower = open('底层'); await flushPromises()
    const top = open('顶层'); await flushPromises()
    key('Escape')
    expect(top.emitted('close')).toHaveLength(1)
    expect(lower.emitted('close')).toBeUndefined()
    top.unmount(); wrappers.pop()
    expect(document.body.classList.contains('modal-open')).toBe(true)
    expect(document.querySelector('[role="dialog"]')!.contains(document.activeElement)).toBe(true)
    key('Escape'); expect(lower.emitted('close')).toHaveLength(1)
  })
  it('keeps a submitting dialog open and focuses its available controls', async () => {
    const wrapper = open('提交', true); await flushPromises()
    expect(document.activeElement?.tagName).toBe('INPUT')
    key('Escape'); expect(wrapper.emitted('close')).toBeUndefined()
    await wrapper.setProps({ closeDisabled: false })
    key('Escape'); expect(wrapper.emitted('close')).toHaveLength(1)
  })
  it('preserves an existing scroll lock owned by another overlay', async () => {
    document.body.classList.add('modal-open')
    const wrapper = open(); await flushPromises(); wrapper.unmount(); wrappers.pop()
    expect(document.body.classList.contains('modal-open')).toBe(true)
  })
})
