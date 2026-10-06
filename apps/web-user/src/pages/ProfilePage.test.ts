import { flushPromises, mount } from '@vue/test-utils'
import { describe, expect, it, vi } from 'vitest'

const mocked = vi.hoisted(() => ({
  me: vi.fn(),
  usage: vi.fn(),
  changePassword: vi.fn(),
  updateUser: vi.fn(),
}))

vi.mock('@/api', () => ({
  customerApi: {
    me: mocked.me,
    usage: mocked.usage,
    changePassword: mocked.changePassword,
  },
}))
vi.mock('@/stores/auth', () => ({ customerAuth: { updateUser: mocked.updateUser } }))

import ProfilePage from './ProfilePage.vue'

describe('ProfilePage', () => {
	function resolveProfile(): void {
		mocked.me.mockResolvedValue({
			id: 'cus-1', username: 'member', display_name: '测试用户', user_type: 'customer', user_group_name: '默认组',
			expires_at: '2027-03-03T13:52:20Z', traffic_used_bytes: 1024, traffic_limit_bytes: 4096,
			max_rules: 10, speed_limit_mbps: 200, ip_limit: 3, connection_limit: 50, effective_status: 'active',
		})
		mocked.usage.mockResolvedValue({ traffic_used_bytes: 1024, traffic_limit_bytes: 4096, remaining_bytes: 3072, updated_at: '2026-10-03T00:00:00Z' })
	}

  it('shows the NY account limits that remain in the user self-service scope', async () => {
		resolveProfile()

    const wrapper = mount(ProfilePage)
    await flushPromises()

    const text = wrapper.text()
    for (const label of ['UID', '用户名', '用户类型', '用户组', '到期时间', '流量', '最大规则数', '速率限制', 'IP 数限制', '连接数限制', '修改密码']) {
      expect(text).toContain(label)
    }
    expect(text).toContain('member')
    expect(text).toContain('默认组')
    expect(mocked.updateUser).toHaveBeenCalledOnce()
    wrapper.unmount()
  })

	it('submits a non-empty short password without imposing a browser-only minimum', async () => {
		resolveProfile()
		mocked.changePassword.mockResolvedValue({ replayed: false })
		const wrapper = mount(ProfilePage)
		await flushPromises()
		const openButton = wrapper.findAll('button').find((button) => button.text().includes('修改密码'))
		expect(openButton).toBeDefined()
		await openButton?.trigger('click')
		const inputs = wrapper.findAll('.modal input')
		await inputs[0]?.setValue('customer-password')
		await inputs[1]?.setValue('1')
		await inputs[2]?.setValue('1')
		await wrapper.get('#password-form').trigger('submit')
		await flushPromises()

		expect(mocked.changePassword).toHaveBeenCalledWith('customer-password', '1')
		expect(wrapper.text()).toContain('密码已修改')
		wrapper.unmount()
	})
})
