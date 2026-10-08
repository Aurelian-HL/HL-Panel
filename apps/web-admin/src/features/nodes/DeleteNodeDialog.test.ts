import { flushPromises, mount } from '@vue/test-utils'
import { beforeEach, describe, expect, it, vi } from 'vitest'

const mocked = vi.hoisted(() => ({ deleteNode: vi.fn() }))
vi.mock('@/api', () => ({ api: { deleteNode: mocked.deleteNode } }))
import DeleteNodeDialog from './DeleteNodeDialog.vue'

beforeEach(() => { mocked.deleteNode.mockReset() })
const confirm = () => document.body.querySelector<HTMLButtonElement>('.modal .button--danger')!

describe('DeleteNodeDialog', () => {
  it('keeps the same idempotency key after an error and emits only after success', async () => {
    mocked.deleteNode.mockRejectedValueOnce(new Error('节点仍在线')).mockResolvedValueOnce({ node_id: 'node-43', replayed: false })
    const wrapper = mount(DeleteNodeDialog, { props: { nodeId: 'node-43', label: '离线节点' } })
    expect(document.body.textContent).toContain('不会卸载远程机器上的服务')
    confirm().click()
    await flushPromises()
    expect(document.body.querySelector('[role="alert"]')?.textContent).toContain('节点仍在线')
    expect(wrapper.emitted('deleted')).toBeUndefined()
    const key = mocked.deleteNode.mock.calls[0]![1]
    confirm().click()
    await flushPromises()
    expect(mocked.deleteNode).toHaveBeenLastCalledWith('node-43', key)
    expect(wrapper.emitted('deleted')).toEqual([['node-43']])
    wrapper.unmount()
  })

  it('prevents repeated submission and closing while the delete is pending', async () => {
    let resolve!: (value: { node_id: string; replayed: boolean }) => void
    mocked.deleteNode.mockImplementation(() => new Promise((done) => { resolve = done }))
    const wrapper = mount(DeleteNodeDialog, { props: { nodeId: 'node-43', label: '离线节点' } })
    confirm().click()
    await flushPromises()
    expect(confirm().disabled).toBe(true)
    confirm().click()
    expect(mocked.deleteNode).toHaveBeenCalledTimes(1)
    expect(document.body.querySelector<HTMLButtonElement>('.modal .button--secondary')!.disabled).toBe(true)
    resolve({ node_id: 'node-43', replayed: false })
    await flushPromises()
    expect(wrapper.emitted('deleted')).toEqual([['node-43']])
    wrapper.unmount()
  })
})
