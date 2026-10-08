import { flushPromises, mount } from '@vue/test-utils'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { getPanelLogs } from '@/api/panelLogs'
import PanelLogsDialog from './PanelLogsDialog.vue'

vi.mock('@/api/panelLogs', () => ({ getPanelLogs: vi.fn() }))
const getLogs = vi.mocked(getPanelLogs)
let wrapper: ReturnType<typeof mount>
const sample = { persistent: true, updated_at: '', items: [
 { time: '2026-10-08T00:00:00Z', level: 'INFO', message: '面板已启动' },
 { time: '2026-10-08T00:01:00Z', level: 'WARN', message: '连接异常' },
 { time: '2026-10-08T00:02:00Z', level: 'ERROR', message: '重启失败' },
] }
beforeEach(() => { getLogs.mockReset().mockResolvedValue(sample) })
afterEach(() => { wrapper?.unmount(); vi.restoreAllMocks() })
const root = () => document.body.querySelector('[role="dialog"]')!
describe('panel log dialog', () => {
 it('loads real records, searches, filters levels, changes count and refreshes', async () => {
  wrapper = mount(PanelLogsDialog)
  await flushPromises()
  expect(getLogs).toHaveBeenCalledWith(100)
  expect(root().textContent).toContain('面板已启动')
  const search = root().querySelector<HTMLInputElement>('[aria-label="筛选日志"]')!
  search.value='连接';search.dispatchEvent(new Event('input'))
  await flushPromises()
  expect(root().querySelector('pre')!.textContent).toContain('连接异常')
  expect(root().querySelector('pre')!.textContent).not.toContain('面板已启动')
  const warn = root().querySelector<HTMLInputElement>('input[value="WARN"]')!
  warn.checked=false;warn.dispatchEvent(new Event('change'))
  await flushPromises()
  expect(root().textContent).toContain('没有匹配')
  const count = root().querySelector<HTMLSelectElement>('select')!
  count.value='20';count.dispatchEvent(new Event('change'))
  await flushPromises()
  expect(getLogs).toHaveBeenLastCalledWith(20)
  root().querySelector<HTMLButtonElement>('[aria-label="刷新日志"]')!.click()
  await flushPromises()
  expect(getLogs).toHaveBeenCalledTimes(3)
 }),
 it('provides a UTF-8 downloadable log and closes without a control command', async () => {
  const create = vi.spyOn(URL,'createObjectURL').mockReturnValue('blob:logs')
  vi.spyOn(URL,'revokeObjectURL').mockImplementation(() => {})
  const click = vi.spyOn(HTMLAnchorElement.prototype,'click').mockImplementation(() => {})
  wrapper=mount(PanelLogsDialog);await flushPromises()
  root().querySelector<HTMLButtonElement>('[aria-label="下载日志"]')!.click()
  const blob=create.mock.calls[0]![0] as Blob
  expect(await blob.text()).toContain('面板已启动')
  expect(blob.type).toBe('text/plain;charset=utf-8')
  expect(click).toHaveBeenCalled()
  root().querySelector<HTMLButtonElement>('[aria-label="关闭对话框"]')!.click()
  await flushPromises()
  expect(wrapper.emitted('close')).toHaveLength(1)
 }),
 it('shows network failure and retries; supports an empty log', async () => {
  getLogs.mockRejectedValueOnce(new Error('网络中断'))
  wrapper=mount(PanelLogsDialog);await flushPromises()
  expect(root().querySelector('[role="alert"]')!.textContent).toContain('网络中断')
  getLogs.mockResolvedValue({ ...sample, items: [] })
  root().querySelector<HTMLButtonElement>('[role="alert"] button')!.click()
  await flushPromises()
  expect(root().textContent).toContain('暂无面板日志')
  expect(root().querySelector<HTMLButtonElement>('[aria-label="下载日志"]')!.disabled).toBe(true)
 })
})
