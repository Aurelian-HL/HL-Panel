import { flushPromises, mount } from '@vue/test-utils'
import { beforeEach, describe, expect, it, vi } from 'vitest'
const subs = vi.hoisted(() => ({ list: vi.fn(), detail: vi.fn(), save: vi.fn(), action: vi.fn(), package: vi.fn(), qrcode: vi.fn() }))
const business = vi.hoisted(() => ({ customers: vi.fn(), vlessIdentities: vi.fn(), rules: vi.fn() }))
vi.mock('@/api/subscriptions', () => ({ subscriptionApi: subs }))
vi.mock('@/api/business', () => ({ businessApi: business }))
import SubscriptionsPage from './SubscriptionsPage.vue'
import type { Subscription, SubscriptionDetail } from '@/api/subscriptions'
const item: Subscription = { id: 'sub-1', name: '客户固定订阅', customer_id: 'cus-1', state: 'active', revision: 3, published_revision: 2, pending_update: true, line_count: 2, published_line_count: 1, created_at: '2026-01-01T00:00:00Z', updated_at: '2026-01-01T00:00:00Z' }
const detail: SubscriptionDetail = { subscription: item, lines: [{ name: '香港', uri: 'socks5://user:pass@hk.example.test:1080' }, { name: '本面板', binding_id: 'binding-1' }], preview: [{ name: '香港', uri: 'socks5://user:pass@hk.example.test:1080' }], published_preview: [{ name: '香港', uri: 'socks5://old:pass@hk.example.test:1080' }], links: { txt: '/api/v1/public/subscriptions/secret.txt', yaml: '/api/v1/public/subscriptions/secret.yaml' }, warning: '' }
beforeEach(() => {
  Object.values(subs).forEach(m => m.mockReset()); Object.values(business).forEach(m => m.mockReset())
  subs.list.mockResolvedValue({ items: [item] }); subs.detail.mockResolvedValue(detail); subs.save.mockResolvedValue({ subscription: item }); subs.action.mockResolvedValue({ subscription: item })
  subs.qrcode.mockResolvedValue({ png_base64: 'cG5n' })
  business.customers.mockResolvedValue({ items: [{ id: 'cus-1', display_name: '测试客户' }] }); business.vlessIdentities.mockResolvedValue({ items: [{ id: 'binding-1', customer_id: 'cus-1', forwarding_rule_id: 'rule-1', state: 'active' }] }); business.rules.mockResolvedValue({ items: [{ id: 'rule-1', name: '原生线路' }] })
  vi.stubGlobal('confirm', vi.fn(() => true))
})
const options = { global: { stubs: { VlessCredentials: true } } }
describe('SubscriptionsPage', () => {
  it('keeps mobile subscriptions readable and routes their actions to the same API', async () => {
    const stopped = { ...item, id: 'sub-stopped', name: '已停用订阅', state: 'revoked' as const }
    subs.list.mockResolvedValue({ items: [item, stopped] })
    const wrapper = mount(SubscriptionsPage, options); await flushPromises()
    const mobile = wrapper.get('[aria-label="订阅卡片"]')
    expect(mobile.findAll('article')).toHaveLength(2)
    expect(mobile.text()).toContain('客户固定订阅'); expect(mobile.text()).toContain('测试客户')
    expect(mobile.text()).not.toContain('socks5://'); expect(mobile.text()).not.toContain('secret.txt')
    const active = mobile.findAll('article')[0]!
    await active.findAll('button').find(b => b.text() === '编辑')!.trigger('click'); await flushPromises()
    expect(wrapper.get('[role="dialog"]').text()).toContain('编辑订阅草稿')
    await wrapper.findAll('button').find(b => b.text() === '取消')!.trigger('click')
    await active.findAll('button').find(b => b.text() === '发布更新')!.trigger('click'); await flushPromises()
    expect(subs.action).toHaveBeenCalledWith(item, 'publish', expect.any(String))
    await wrapper.get('[aria-label="订阅卡片"]').findAll('article')[1]!.findAll('button').find(b => b.text() === '恢复')!.trigger('click'); await flushPromises()
    expect(subs.action).toHaveBeenCalledWith(stopped, 'restore', expect.any(String)); wrapper.unmount()
  })
  it('creates a two-line draft, does not publish implicitly', async () => {
    const wrapper = mount(SubscriptionsPage, options); await flushPromises()
    await wrapper.findAll('button').find(b => b.text() === '创建订阅')!.trigger('click')
    const form = wrapper.get('form'); await form.findAll('input')[0]!.setValue('多线路订阅'); await form.findAll('input')[1]!.setValue('香港')
    await form.get('textarea').setValue('socks5://user:pass@hk.example.test:1080')
    await form.findAll('button').find(b => b.text() === '添加线路')!.trigger('click')
    await form.findAll('input')[2]!.setValue('本面板'); await form.findAll('select')[2]!.setValue('native'); await form.findAll('select')[3]!.setValue('binding-1')
    await form.trigger('submit'); await flushPromises()
    expect(subs.save).toHaveBeenCalledWith(null, { name: '多线路订阅', customer_id: 'cus-1', revision: 0, lines: [{ name: '香港', uri: 'socks5://user:pass@hk.example.test:1080' }, { name: '本面板', binding_id: 'binding-1' }] }, expect.any(String))
    expect(subs.action).not.toHaveBeenCalled(); expect(wrapper.find('form').exists()).toBe(false); wrapper.unmount()
  })
  it('edits the current draft, keeps the customer fixed and copies both published addresses', async () => {
    const wrapper = mount(SubscriptionsPage, options); await flushPromises()
    await wrapper.findAll('button').find(b => b.text() === '编辑')!.trigger('click'); await flushPromises()
    expect(wrapper.get('form select').attributes('disabled')).toBeDefined(); expect((wrapper.get('form textarea').element as HTMLTextAreaElement).value).toContain('hk.example.test')
    await wrapper.findAll('button').find(b => b.text() === '取消')!.trigger('click')
    await wrapper.findAll('button').find(b => b.text() === '详情')!.trigger('click'); await flushPromises()
    expect(wrapper.text()).toContain('有未发布的修改'); expect(wrapper.text()).toContain('已发布线路')
    const writeText = vi.fn().mockResolvedValue(undefined); Object.defineProperty(navigator, 'clipboard', { configurable: true, value: { writeText } })
    const copies = wrapper.findAll('button').filter(b => b.text() === '复制'); await copies[0]!.trigger('click'); await copies[1]!.trigger('click'); await flushPromises()
    expect(writeText).toHaveBeenCalledWith(new URL(detail.links.txt, location.origin).href); expect(writeText).toHaveBeenCalledWith(new URL(detail.links.yaml, location.origin).href)
    await wrapper.findAll('button').find(b => b.text() === 'YAML 二维码')!.trigger('click'); await flushPromises()
    expect(subs.qrcode).toHaveBeenCalledWith(item.id, 'yaml'); expect(wrapper.get('img[alt="YAML 订阅二维码"]').attributes('src')).toBe('data:image/png;base64,cG5n')
    await wrapper.findAll('button').find(b => b.text() === '更换订阅地址')!.trigger('click'); await flushPromises()
    expect(subs.action).toHaveBeenCalledWith(item, 'rotate', expect.any(String)); wrapper.unmount()
  })
  it('paginates and filters without showing private credentials in the list', async () => {
    subs.list.mockResolvedValue({ items: Array.from({ length: 23 }, (_, i) => ({ ...item, id: `sub-${i}`, name: `订阅 ${i}` })) })
    const wrapper = mount(SubscriptionsPage, options); await flushPromises(); expect(wrapper.findAll('tbody tr')).toHaveLength(20)
    expect(wrapper.text()).not.toContain('socks5://'); expect(wrapper.text()).not.toContain('secret.txt')
    await wrapper.get('select[aria-label="每页订阅数量"]').setValue('10'); await flushPromises(); expect(wrapper.findAll('tbody tr')).toHaveLength(10)
    await wrapper.findAll('button').find(b => b.text() === '下一页')!.trigger('click'); expect(wrapper.text()).toContain('2 / 3')
    await wrapper.get('input[placeholder="名称或客户"]').setValue('订阅 22'); expect(wrapper.findAll('tbody tr')).toHaveLength(1); expect(wrapper.text()).toContain('1 / 1'); wrapper.unmount()
  })
  it('shows server failures and remains retryable', async () => {
    subs.action.mockRejectedValue(new Error('revision conflict')); const wrapper = mount(SubscriptionsPage, options); await flushPromises()
    await wrapper.findAll('button').find(b => b.text() === '发布更新')!.trigger('click'); await flushPromises(); expect(wrapper.get('[role="alert"]').text()).toContain('数据已被其他操作更新')
    expect(wrapper.findAll('button').find(b => b.text() === '发布更新')!.attributes('disabled')).toBeUndefined(); wrapper.unmount()
    subs.list.mockRejectedValue(new Error('offline')); const failed = mount(SubscriptionsPage, options); await flushPromises(); expect(failed.text()).toContain('订阅加载失败'); failed.unmount()
  })
})
