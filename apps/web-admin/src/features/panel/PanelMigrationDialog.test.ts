import { flushPromises, mount } from '@vue/test-utils'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import * as api from '@/api/panelMigration'
import PanelMigrationDialog from './PanelMigrationDialog.vue'

vi.mock('@/api/panelMigration', () => ({ exportMigration: vi.fn(), previewMigration: vi.fn(), importMigration: vi.fn(), recoveryMigration: vi.fn(), downloadMigration: vi.fn() }))
let wrapper: ReturnType<typeof mount>
const root = () => document.body.querySelector('[role="dialog"]')!
const input = async (name: string, value: string) => { const element=root().querySelector<HTMLInputElement>(`[aria-label="${name}"]`)!; element.value=value; element.dispatchEvent(new Event('input')); await flushPromises() }
const button = (text: string) => [...root().querySelectorAll<HTMLButtonElement>('button')].find(b=>b.textContent?.includes(text))!
beforeEach(() => { vi.resetAllMocks(); sessionStorage.clear() })
afterEach(() => { wrapper?.unmount() })
describe('migration backup dialog', () => {
 it('validates password confirmation and keeps export success visible after secrets are cleared', async () => {
  vi.mocked(api.exportMigration).mockResolvedValue(new Blob(['encrypted']))
  wrapper=mount(PanelMigrationDialog)
  await input('当前管理员密码','isolated-admin');await input('备份密码','isolated-backup');await input('确认备份密码','different')
  button('导出加密备份').click();await flushPromises()
  expect(api.exportMigration).not.toHaveBeenCalled();expect(root().textContent).toContain('两次备份密码不一致')
  await input('确认备份密码','isolated-backup');button('导出加密备份').click();await flushPromises()
  expect(api.exportMigration).toHaveBeenCalledWith('isolated-admin','isolated-backup')
  expect(api.downloadMigration).toHaveBeenCalled();expect(root().textContent).toContain('备份已导出')
  expect(root().querySelector<HTMLInputElement>('[aria-label="备份密码"]')!.value).toBe('')
 }),
 it('requires a preview and explicit overwrite before restore and revokes local login', async () => {
  const preview={manifest:{format:1,version:'v0.1.47',created_at:'2026-10-08T00:00:00Z',source_url:'https://source.example.com'},digest:'fixture-digest',counts:{nodes:2,rules:5}}
  vi.mocked(api.previewMigration).mockResolvedValue(preview)
  vi.mocked(api.importMigration).mockResolvedValue({recovery_id:'mbk_'+ 'a'.repeat(32),replayed:false,restored_at:''})
  sessionStorage.setItem('ny_admin_access_token','isolated-token')
  wrapper=mount(PanelMigrationDialog);button('导入恢复').click();await flushPromises()
  const file=new File(['encrypted'],'panel.hlbackup'); const element=root().querySelector<HTMLInputElement>('input[type="file"]')!
  Object.defineProperty(element,'files',{value:[file]});element.dispatchEvent(new Event('change'));await flushPromises()
  await input('当前管理员密码','isolated-admin');await input('备份密码','isolated-backup')
  button('校验并预览').click();await flushPromises()
  expect(root().textContent).toContain('备份校验通过');expect(root().textContent).toContain('面板地址已改变')
  expect(button('确认导入并恢复').disabled).toBe(true);expect(api.importMigration).not.toHaveBeenCalled()
  const ack=root().querySelector<HTMLInputElement>('input[type="checkbox"]')!;ack.checked=true;ack.dispatchEvent(new Event('change'));await flushPromises()
  button('确认导入并恢复').click();await flushPromises()
  expect(api.importMigration).toHaveBeenCalledWith(file,'isolated-admin','isolated-backup','fixture-digest',expect.any(String))
  expect(sessionStorage.getItem('ny_admin_access_token')).toBeNull();expect(sessionStorage.getItem('hl_panel_migration_recovery')).toBe('mbk_'+'a'.repeat(32))
  expect(root().textContent).toContain('导入成功');expect(root().querySelector<HTMLButtonElement>('[aria-label="关闭对话框"]')!.disabled).toBe(true)
 }),
 it('keeps network errors visible and requires a fresh preview when password changes', async () => {
  vi.mocked(api.previewMigration).mockRejectedValue(new Error('连接中断'))
  wrapper=mount(PanelMigrationDialog);button('导入恢复').click();await flushPromises()
  const file=root().querySelector<HTMLInputElement>('input[type="file"]')!;Object.defineProperty(file,'files',{value:[new File(['x'],'panel.hlbackup')]});file.dispatchEvent(new Event('change'))
  await input('当前管理员密码','isolated-admin');await input('备份密码','isolated-backup');button('校验并预览').click();await flushPromises()
  expect(root().querySelector('[role="alert"]')!.textContent).toContain('连接中断');expect(api.importMigration).not.toHaveBeenCalled()
 })
})
