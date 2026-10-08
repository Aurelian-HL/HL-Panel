import { flushPromises, mount } from '@vue/test-utils'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import * as api from '@/api/panelMigration'
import PanelAutomaticMigration from './PanelAutomaticMigration.vue'

vi.mock('@/api/panelMigration', () => ({ automaticStatus: vi.fn(), continueAutomaticMigration: vi.fn(), downloadMigration: vi.fn(), probeMigration: vi.fn(), recoveryMigration: vi.fn(), rollbackAutomaticMigration: vi.fn(), startAutomaticMigration: vi.fn() }))
let wrapper: ReturnType<typeof mount>
const fingerprint = 'SHA256:' + 'a'.repeat(43)
const empty: api.AutomaticStatus = { available: true, source_url: 'https://panel.example.com', source_ip_url: 'https://1.1.1.1:443', frozen: false, running: false, credentials_required: true, task: null }
const ready: api.AutomaticStatus = { ...empty, credentials_required: false, task: { id: '12345678-1234-1234-1234-123456789abc', target: { host: '8.8.8.8', port: 22, fingerprint }, source_url: empty.source_url!, domain: 'panel.example.com', version: 'v0.1.50', state: 'ready', phase: 'prepare', message: '新机已准备', backup_id: '', updated_at: '' } }
const button = (text: string) => wrapper.findAll('button').find(item => item.text().includes(text))!
const field = (name: string) => wrapper.get(`[aria-label="${name}"]`)
beforeEach(() => { vi.resetAllMocks(); vi.useFakeTimers(); sessionStorage.clear(); localStorage.clear(); vi.mocked(api.automaticStatus).mockResolvedValue(empty) })
afterEach(() => { wrapper?.unmount(); vi.useRealTimers() })

describe('automatic panel migration', () => {
 it('requires verified SSH fingerprint and fresh target confirmation; clears secrets after submission', async () => {
  vi.mocked(api.probeMigration).mockResolvedValue({ fingerprint })
  vi.mocked(api.startAutomaticMigration).mockResolvedValue(ready)
  wrapper = mount(PanelAutomaticMigration); await flushPromises()
  await field('新服务器 IP').setValue('8.8.8.8'); await field('自动迁移管理员密码').setValue('isolated-admin')
  await button('获取 SSH 指纹').trigger('click'); await flushPromises()
  expect(wrapper.text()).toContain(fingerprint)
  expect(button('准备新服务器').attributes('disabled')).toBeDefined()
  await wrapper.findAll('input[type=checkbox]')[0]!.setValue(true)
  await field('SSH 密码').setValue('isolated-ssh-secret')
  await field('自动迁移备份密码').setValue('isolated-backup'); await field('自动迁移确认密码').setValue('isolated-backup')
  await wrapper.get('form').trigger('submit'); await flushPromises()
  expect(api.startAutomaticMigration).not.toHaveBeenCalled()
  expect(wrapper.text()).toContain('请确认目标服务器是全新服务器')
  await wrapper.findAll('input[type=checkbox]')[1]!.setValue(true)
  await wrapper.get('form').trigger('submit'); await flushPromises()
  expect(api.startAutomaticMigration).toHaveBeenCalledWith(expect.objectContaining({ target: { host: '8.8.8.8', port: 22, fingerprint }, ssh_password: 'isolated-ssh-secret', confirm: 'PREPARE' }))
  expect(field('切换管理员密码').element).toHaveProperty('value', '')
  expect(sessionStorage.length).toBe(0); expect(localStorage.length).toBe(0)
  expect(wrapper.text()).toContain('等待正式切换')
 })
 it('retains form errors across status polls and invalidates trust when target changes', async () => {
  vi.mocked(api.probeMigration).mockRejectedValueOnce(new Error('认证失败')).mockResolvedValue({ fingerprint })
  wrapper = mount(PanelAutomaticMigration); await flushPromises()
  await field('新服务器 IP').setValue('8.8.8.8'); await field('自动迁移管理员密码').setValue('isolated-admin')
  await button('获取 SSH 指纹').trigger('click'); await flushPromises()
  await vi.advanceTimersByTimeAsync(5000); await flushPromises()
  expect(wrapper.text()).toContain('认证失败')
  await button('获取 SSH 指纹').trigger('click'); await flushPromises()
  await wrapper.findAll('input[type=checkbox]')[0]!.setValue(true)
  await field('新服务器 IP').setValue('8.8.4.4')
  expect(wrapper.text()).not.toContain(fingerprint)
  expect(button('准备新服务器').attributes('disabled')).toBeDefined()
 })
 it('requires new credentials after restart and retains original task identity and fingerprint', async () => {
  const failed = { ...ready, credentials_required: true, frozen: true, task: { ...ready.task!, state: 'failed' as const, phase: 'restore', backup_id: 'mbk_' + 'a'.repeat(32) } }
  vi.mocked(api.automaticStatus).mockResolvedValue(failed)
  wrapper = mount(PanelAutomaticMigration); await flushPromises()
  expect(field('新服务器 IP').element).toHaveProperty('value', '8.8.8.8')
  expect(wrapper.text()).toContain(fingerprint)
  expect(wrapper.text()).toContain('下载最终备份')
  expect(button('重新连接并继续').attributes('disabled')).toBeDefined()
  wrapper.unmount(); const calls = vi.mocked(api.automaticStatus).mock.calls.length
  await vi.advanceTimersByTimeAsync(15000)
  expect(api.automaticStatus).toHaveBeenCalledTimes(calls)
 })
 it('requires formal cutover confirmation and sends administrator verification', async () => {
  vi.mocked(api.automaticStatus).mockResolvedValue(ready)
  vi.mocked(api.continueAutomaticMigration).mockResolvedValue({ ...ready, running: true, frozen: true, task: { ...ready.task!, state: 'waiting_dns', phase: 'certificate' } })
  wrapper = mount(PanelAutomaticMigration); await flushPromises()
  await field('切换管理员密码').setValue('isolated-admin')
  await wrapper.findAll('form')[0]!.trigger('submit'); await flushPromises()
  expect(api.continueAutomaticMigration).not.toHaveBeenCalled()
  await wrapper.findAll('input[type=checkbox]')[0]!.setValue(true)
  await wrapper.findAll('form')[0]!.trigger('submit'); await flushPromises()
  expect(api.continueAutomaticMigration).toHaveBeenCalledWith(ready.task!.id, 'isolated-admin')
  expect(wrapper.text()).toContain('A 记录改为 8.8.8.8')
 })
})
