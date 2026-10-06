import { flushPromises, mount } from '@vue/test-utils'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import type { Announcement } from '@/api/siteTypes'

const mocked = vi.hoisted(() => ({ saveAnnouncement: vi.fn() }))
vi.mock('@/api/site', () => ({ siteApi: mocked }))
import AnnouncementEditor from './AnnouncementEditor.vue'

const existing: Announcement = {
  id: 'ann-1', title: '维护通知', content: '今晚进行线路维护', level: 'maintenance', enabled: true,
  starts_at: '2026-10-03T12:00:00Z', ends_at: '2026-10-03T14:00:00Z', sort_order: 10, revision: 2,
  created_at: '2026-10-01T00:00:00Z', updated_at: '2026-10-02T00:00:00Z',
}

beforeEach(() => mocked.saveAnnouncement.mockReset())

describe('AnnouncementEditor', () => {
  it('preserves scheduling and revision when editing', async () => {
    mocked.saveAnnouncement.mockResolvedValue(existing)
    const wrapper = mount(AnnouncementEditor, { props: { announcement: existing }, global: { stubs: { Teleport: true } } })
    await wrapper.get('form').trigger('submit')
    await flushPromises()
    const [input, id] = mocked.saveAnnouncement.mock.calls[0] as [Record<string, unknown>, string]
    expect(input.revision).toBe(2)
    expect(input.starts_at).toBe('2026-10-03T12:00:00.000Z')
    expect(input.ends_at).toBe('2026-10-03T14:00:00.000Z')
    expect(id).toBe(existing.id)
    expect(wrapper.emitted('saved')).toHaveLength(1)
    wrapper.unmount()
  })

  it('rejects an end time before the start time without sending a request', async () => {
    const wrapper = mount(AnnouncementEditor, { props: { announcement: null }, global: { stubs: { Teleport: true } } })
    await wrapper.get('input[maxlength="120"]').setValue('维护通知')
    await wrapper.get('textarea').setValue('维护说明')
    const times = wrapper.findAll('input[type="datetime-local"]')
    await times[0]!.setValue('2026-10-03T14:00')
    await times[1]!.setValue('2026-10-03T13:00')
    await wrapper.get('form').trigger('submit')
    expect(wrapper.text()).toContain('结束时间必须晚于开始时间')
    expect(mocked.saveAnnouncement).not.toHaveBeenCalled()
    wrapper.unmount()
  })
})
