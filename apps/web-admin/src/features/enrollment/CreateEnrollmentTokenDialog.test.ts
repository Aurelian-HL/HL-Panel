import { flushPromises, mount } from '@vue/test-utils'
import { describe, expect, it, vi } from 'vitest'

const apiMocks = vi.hoisted(() => ({ createEnrollmentToken: vi.fn() }))

vi.mock('@/api', () => ({ api: apiMocks }))

import CreateEnrollmentTokenDialog from './CreateEnrollmentTokenDialog.vue'

describe('CreateEnrollmentTokenDialog', () => {
  it('reveals the raw token only after a successful create request', async () => {
    apiMocks.createEnrollmentToken.mockResolvedValue({
      id: 'token-1', name: 'gz-edge-03', token: 'enroll_once_only', expires_at: '2026-10-02T08:00:00Z',
    })
    const wrapper = mount(CreateEnrollmentTokenDialog, { global: { stubs: { Teleport: true } } })

    expect(wrapper.text()).not.toContain('enroll_once_only')
    await wrapper.get('input').setValue('gz-edge-03')
    await wrapper.get('form').trigger('submit')
    await flushPromises()

    expect(apiMocks.createEnrollmentToken).toHaveBeenCalledWith({ name: 'gz-edge-03', expires_in_seconds: 900 })
    expect(wrapper.text()).toContain('enroll_once_only')
    expect(wrapper.text()).toContain('关闭后无法再次查看')

    await wrapper.get('.modal__footer .button').trigger('click')
    expect(wrapper.emitted('close')).toHaveLength(1)
  })
})
