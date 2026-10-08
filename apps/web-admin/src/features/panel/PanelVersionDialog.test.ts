import { flushPromises, mount } from '@vue/test-utils'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { checkVersion, type VersionStatus } from '@/api/releases'
import PanelVersionDialog from './PanelVersionDialog.vue'
vi.mock('@/api/releases', async original => ({ ...await original<typeof import('@/api/releases')>(), checkVersion: vi.fn() }))
const check = vi.mocked(checkVersion)
const sample: VersionStatus = {
 current_version:'v0.1.43', latest_version:'v0.1.44', versions_behind:1, versions_behind_at_least:false,
 state:'update_available',attention_required:false,can_defer:true,checked_at:'',message:'有更新',
 versions: [
  {tag:'v0.1.44',published_at:'',current:false,can_update:true,release_url:'https://github.com/Aurelian-HL/HL-Panel/releases/tag/v0.1.44'},
  {tag:'v0.1.43',published_at:'',current:true,can_update:false,release_url:'https://github.com/Aurelian-HL/HL-Panel/releases/tag/v0.1.43'},
  {tag:'v0.1.42',published_at:'',current:false,can_update:false,release_url:'https://github.com/Aurelian-HL/HL-Panel/releases/tag/v0.1.42'},
 ]
}
let wrapper: ReturnType<typeof mount>
const root = () => document.body.querySelector('[role="dialog"]')!
beforeEach(() => check.mockReset().mockResolvedValue(sample))
afterEach(() => { wrapper?.unmount(); vi.unstubAllGlobals(); vi.restoreAllMocks() })
describe('panel version dialog', () => {
 it('marks current, disables downgrade and creates a selected data-preserving update command', async () => {
  const write=vi.fn().mockResolvedValue(undefined)
  vi.stubGlobal('navigator',{ clipboard:{writeText:write} })
  wrapper=mount(PanelVersionDialog,{props:{current:'v0.1.43'}});await flushPromises()
  const current=root().querySelector<HTMLInputElement>('input[value="v0.1.43"]')!
  expect(current.checked).toBe(true)
  expect(root().querySelector<HTMLInputElement>('input[value="v0.1.42"]')!.disabled).toBe(true)
  expect(root().querySelector('pre')).toBeNull()
  root().querySelector<HTMLInputElement>('input[value="v0.1.44"]')!.click()
  await flushPromises()
  expect(root().querySelector('pre')!.textContent).toContain('update.sh | bash -s -- --version v0.1.44')
  root().querySelector<HTMLButtonElement>('.selected-update button')!.click()
  await flushPromises()
  expect(write).toHaveBeenCalledWith(expect.stringContaining('--version v0.1.44'))
  expect(root().textContent).toContain('已复制')
  expect(root().querySelector<HTMLAnchorElement>('a')!.href).toBe(sample.versions![0]!.release_url)
  vi.unstubAllGlobals()
 })
 it('shows a failed GitHub check and retries after a request failure', async () => {
  check.mockRejectedValueOnce(new Error('网络错误'))
  wrapper=mount(PanelVersionDialog,{props:{current:'v0.1.43'}});await flushPromises()
  expect(root().querySelector('[role="alert"]')!.textContent).toContain('网络错误')
  root().querySelector<HTMLButtonElement>('[role="alert"] button')!.click()
  await flushPromises()
  expect(root().querySelectorAll('input[type="radio"]')).toHaveLength(3)
 }),
 it('does not claim current when GitHub is unreachable', async () => {
  check.mockResolvedValue({...sample,state:'check_failed',versions:[],message:'版本尚未核实'})
  wrapper=mount(PanelVersionDialog,{props:{current:'v0.1.43'}});await flushPromises()
  expect(root().textContent).toContain('版本尚未核实')
  expect(root().textContent).toContain('当前版本：v0.1.43')
  expect(root().querySelector('pre')).toBeNull()
 })
})
