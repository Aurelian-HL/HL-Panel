import { flushPromises, mount } from '@vue/test-utils'
import { describe, expect, it, vi } from 'vitest'

const apiMocks = vi.hoisted(() => ({ createGroupRevision: vi.fn() }))

vi.mock('@/api', () => ({ api: apiMocks }))

import PublishGroupRevisionDialog from './PublishGroupRevisionDialog.vue'

const group = {
  id: 'group-1', name: '广州边缘', kind: 'EDGE' as const, description: '', member_count: 2,
  current_generation: 4, selection_policy: 'weighted_least_connections' as const, created_at: '2026-10-01T00:00:00Z', updated_at: '2026-10-02T00:00:00Z',
}

describe('PublishGroupRevisionDialog', () => {
  it('blocks invalid JSON before sending a group revision', async () => {
    const wrapper = mount(PublishGroupRevisionDialog, { props: { group }, global: { stubs: { Teleport: true } } })
    await wrapper.get('textarea').setValue('{bad json')
    await wrapper.get('form').trigger('submit')

    expect(apiMocks.createGroupRevision).not.toHaveBeenCalled()
    expect(wrapper.text()).toContain('配置内容不是有效的 JSON')
  })

  it('publishes an idempotent GroupRevision and shows node assignments', async () => {
    apiMocks.createGroupRevision.mockResolvedValue({
      generation: {
        id: 'revision-5', group_id: 'group-1', generation: 5, engine: 'xray',
        config: { schema_version: 1, services: [] }, config_hash: 'a'.repeat(64), created_by: 'admin-1', created_at: '2026-10-02T00:00:00Z',
      },
      assignments: [
        { id: 'a-1', node_id: 'n-1', generation: 8, engine: 'node-bundle', config: {}, config_hash: 'b'.repeat(64), created_at: '' },
        { id: 'a-2', node_id: 'n-2', generation: 8, engine: 'node-bundle', config: {}, config_hash: 'b'.repeat(64), created_at: '' },
      ],
      replayed: false,
    })
    const wrapper = mount(PublishGroupRevisionDialog, { props: { group }, global: { stubs: { Teleport: true } } })
    await wrapper.get('form').trigger('submit')
    await flushPromises()

    expect(apiMocks.createGroupRevision).toHaveBeenCalledOnce()
    const [groupId, request] = apiMocks.createGroupRevision.mock.calls[0] as [string, Record<string, unknown>]
    expect(groupId).toBe('group-1')
    expect(request).toMatchObject({ engine: 'xray', config: { schema_version: 1, services: [] } })
    expect(request.idempotency_key).toEqual(expect.any(String))
    expect(wrapper.text()).toContain('组修订 5 已发布')
    expect(wrapper.text()).toContain('已为 2 个节点生成完整配置')
  })
})
