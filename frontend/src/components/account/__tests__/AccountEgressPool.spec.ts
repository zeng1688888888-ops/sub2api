import { describe, expect, it, vi } from 'vitest'
import { mount } from '@vue/test-utils'
import type { Account, Proxy } from '@/types'
import Editor from '../AccountEgressPoolEditor.vue'
import Capacity from '../AccountCapacityCell.vue'

vi.mock('vue-i18n', async () => ({ ...await vi.importActual<typeof import('vue-i18n')>('vue-i18n'), useI18n: () => ({ t: (key: string, values?: Record<string, unknown>) => key + (values ? JSON.stringify(values) : '') }) }))
const proxies = [
  { id: 1, name: 'Exit A', status: 'active', expires_at: null },
  { id: 2, name: 'Exit B', status: 'active', expires_at: null },
  { id: 3, name: 'Expired', status: 'active', expires_at: '2000-01-01T00:00:00Z' }
] as Proxy[]

describe('Account egress editor', () => {
  it('enables with an active proxy and adds a different proxy', async () => {
    const wrapper = mount(Editor, { props: { modelValue: [], proxies, accountConcurrency: 100 } })
    await wrapper.get('input[type="checkbox"]').setValue(true)
    expect(wrapper.emitted('update:modelValue')?.[0]).toEqual([[{ proxy_id: 1, concurrency: 5 }]])
    await wrapper.setProps({ modelValue: [{ proxy_id: 1, concurrency: 5 }] })
    const add = wrapper.findAll('button').find(b => b.text().includes('egressPool.add'))!
    await add.trigger('click')
    expect(wrapper.emitted('update:modelValue')?.at(-1)).toEqual([[{ proxy_id: 1, concurrency: 5 }, { proxy_id: 2, concurrency: 5 }]])
    expect(wrapper.get('option[value="3"]').attributes('disabled')).toBeDefined()
  })
  it('edits a lane without mutating the input and respects the total account cap', async () => {
    const lanes = [{ proxy_id: 1, concurrency: 5 }, { proxy_id: 2, concurrency: 5 }]
    const wrapper = mount(Editor, { props: { modelValue: lanes, proxies, accountConcurrency: 7 } })
    expect(wrapper.text()).toContain('"count":7')
    await wrapper.findAll('input[type="number"]')[0].setValue(8)
    expect(lanes[0].concurrency).toBe(5)
    expect(wrapper.emitted('update:modelValue')?.at(-1)).toEqual([[{ proxy_id: 1, concurrency: 8 }, { proxy_id: 2, concurrency: 5 }]])
    await wrapper.get('input[type="checkbox"]').setValue(false)
    expect(wrapper.emitted('update:modelValue')?.at(-1)).toEqual([[]])
  })
})

describe('Account capacity with egresses', () => {
  it('renders total plus 5/5, 4/5, 4/5 and updates lane counts independently', async () => {
    const account = { platform: 'openai', type: 'oauth', concurrency: 100, current_concurrency: 13,
      egress_statuses: [5,4,4].map((n,i) => ({ proxy_id: i+1, name: 'Exit '+(i+1), concurrency: 5, current_concurrency: n, available: true })) } as Account
    const wrapper = mount(Capacity, { props: { account } })
    const badges = wrapper.findAll('[data-testid="egress-capacity"]')
    expect(badges).toHaveLength(3)
    expect(wrapper.text().replaceAll(/\s/g, '')).toContain('13/100')
    expect(badges[0].text().replaceAll(/\s/g, '')).toContain('5/5')
    expect(badges[0].classes()).toContain('text-red-700')
    expect(badges[1].classes()).toContain('text-yellow-700')
    expect(badges[0].attributes('title')).toContain('Exit 1')
    await wrapper.setProps({ account: { ...account, egress_statuses: [{...account.egress_statuses![0], current_concurrency: null, available: false}] } })
    expect(wrapper.get('[data-testid="egress-capacity"]').text()).toContain('?')
    expect(wrapper.get('[data-testid="egress-capacity"]').classes()).toContain('text-gray-600')
  })
})
