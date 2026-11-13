import { it, expect, vi, beforeEach } from 'vitest'
import { mount, flushPromises } from '@vue/test-utils'
import { defineComponent } from 'vue'
import type { Account } from '@/types'
import Cell from '../AccountSVGProbeCell.vue'
import Settings from '../../admin/account/AccountSVGProbeSettings.vue'
const mocks = vi.hoisted(() => ({ get: vi.fn(), save: vi.fn(), run: vi.fn(), image: vi.fn(), account: vi.fn(), list: vi.fn() }))
vi.mock('@/api/admin/accountSvgProbe', () => ({ getSVGProbeSettings: mocks.get, saveSVGProbeSettings: mocks.save, runSVGProbe: mocks.run, getSVGProbeImage: mocks.image }))
vi.mock('@/api/admin/accounts', () => ({ getById: mocks.account, list: mocks.list }))
vi.mock('vue-i18n', async () => ({ ...await vi.importActual<typeof import('vue-i18n')>('vue-i18n'), useI18n: () => ({ t: (key: string) => key }) }))
const Dialog = defineComponent({ props: ['show'], template: '<div v-if="show"><slot /><slot name="footer" /></div>' })
const account = { id: 2, platform: 'openai', type: 'oauth', name: 'Test GPT' } as Account
const result = { id: 'test-result', status: 'generated', model: 'gpt-6.1-sol', prompt: 'Draw a chicken', prompt_index: 0, started_at: '2026-11-13T00:00:00Z', finished_at: '2026-11-13T00:00:10Z', latency_ms: 10000, message: 'Review SVG', has_svg: true }
const global = { stubs: { BaseDialog: Dialog } }
beforeEach(() => {
  vi.clearAllMocks()
  mocks.get.mockResolvedValue({ model: 'gpt-6.1-sol', prompts: ['Prompt one', 'Prompt two'] })
  mocks.save.mockImplementation(async x => x)
  mocks.run.mockResolvedValue(result)
  mocks.image.mockResolvedValue(new Blob(['<svg xmlns="http://www.w3.org/2000/svg"/>'], { type: 'image/svg+xml' }))
  mocks.account.mockResolvedValue(account)
  URL.createObjectURL = vi.fn().mockReturnValue('blob:test-svg')
  URL.revokeObjectURL = vi.fn()
})
it('uses an authenticated image blob, shows prompt and downloads SVG without inserting model HTML', async () => {
  const wrapper = mount(Cell, { props: { account: { ...account, svg_probe: result as Account['svg_probe'] } }, global })
  await flushPromises()
  expect(mocks.image).toHaveBeenCalledWith(2, 'test-result', expect.any(AbortSignal))
  expect(wrapper.get('img').attributes('src')).toBe('blob:test-svg')
  expect(wrapper.find('iframe').exists()).toBe(false)
  await wrapper.get('button[aria-label="admin.accounts.svgProbe.preview"]').trigger('click')
  expect(wrapper.text()).toContain('Draw a chicken')
  expect(wrapper.get('a[download]').attributes('download')).toBe('account-2-test.svg')
  wrapper.unmount(); expect(URL.revokeObjectURL).toHaveBeenCalledWith('blob:test-svg')
})
it('retests a single account and emits its saved result', async () => {
  const wrapper = mount(Cell, { props: { account }, global })
  await wrapper.findAll('button').find(b => b.text() === 'admin.accounts.svgProbe.retest')!.trigger('click')
  await flushPromises()
  expect(mocks.run).toHaveBeenCalledWith(2, expect.any(AbortSignal))
  expect(wrapper.emitted('updated')?.[0]?.[0]).toMatchObject({ id: 2, svg_probe: result })
})
it('saves multiple independent prompts and sequentially tests the selected accounts', async () => {
  const wrapper = mount(Settings, { props: { show: false, accountIds: [2, 3] }, global })
  await wrapper.setProps({ show: true }); await flushPromises()
  expect(wrapper.findAll('textarea')).toHaveLength(2)
  await wrapper.findAll('textarea')[0].setValue('自定义第一题\n第二行')
  await wrapper.findAll('button').find(b => b.text() === 'admin.accounts.svgProbe.saveAndRun')!.trigger('click')
  await flushPromises()
  expect(mocks.save).toHaveBeenCalledWith({ model: 'gpt-6.1-sol', prompts: ['自定义第一题\n第二行', 'Prompt two'], use_egress_pool: false })
  expect(mocks.run.mock.calls.map(c => c[0])).toEqual([2, 3])
  expect(wrapper.emitted('updated')).toHaveLength(2)
})
it('does not save an image model or blank prompt', async () => {
  const wrapper = mount(Settings, { props: { show: false, accountIds: [2] }, global })
  await wrapper.setProps({ show: true }); await flushPromises()
  await wrapper.get('input').setValue('gpt-image-2')
  await wrapper.findAll('button').find(b => b.text() === 'admin.accounts.svgProbe.save')!.trigger('click')
  expect(mocks.save).not.toHaveBeenCalled(); expect(wrapper.get('[role="alert"]').text()).toContain('invalidSettings')
})

it('previews HTML animation in a sandbox without script or origin permissions', async () => {
  const html = '<!doctype html><html><body><svg><circle r="10" /></svg></body></html>'
  mocks.image.mockResolvedValue({ text: async () => html })
  const wrapper = mount(Cell, { props: { account: { ...account, svg_probe: { ...result, mime_type: 'text/html' } as Account['svg_probe'] } }, global })
  await flushPromises()
  expect(wrapper.get('iframe').attributes('sandbox')).toBe('')
  expect(wrapper.get('iframe').attributes('srcdoc')).toBe(html)
  expect(wrapper.find('svg').exists()).toBe(false)
  await wrapper.get('button[aria-label="admin.accounts.svgProbe.preview"]').trigger('click')
  expect(wrapper.get('a[download]').attributes('download')).toBe('account-2-pelican-test.html')
  wrapper.unmount()
})
it('loads settings when initially open and allows changing prompts again after saving', async () => {
  const wrapper = mount(Settings, { props: { show: true, accountIds: [2] }, global })
  await flushPromises()
  const save = () => wrapper.findAll('button').find(b => b.text() === 'admin.accounts.svgProbe.save')!
  await wrapper.findAll('textarea')[0].setValue('第一版')
  await save().trigger('click'); await flushPromises()
  await wrapper.findAll('textarea')[0].setValue('第二版')
  await save().trigger('click'); await flushPromises()
  expect(mocks.save.mock.calls[1][0].prompts[0]).toBe('第二版')
  await wrapper.findAll('button').find(b => b.text() === 'admin.accounts.svgProbe.restoreDefaults')!.trigger('click')
  expect(wrapper.findAll('textarea')).toHaveLength(2)
  expect((wrapper.findAll('textarea')[0].element as HTMLTextAreaElement).value).toContain('鹈鹕骑自行车')
  expect((wrapper.findAll('textarea')[1].element as HTMLTextAreaElement).value).toContain('秦始皇开坦克')
})
