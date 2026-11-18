import { describe, expect, it, vi } from 'vitest'
import { mount } from '@vue/test-utils'
import AccountSchedulingControl from '../AccountSchedulingControl.vue'
import type { Account } from '@/types'

vi.mock('vue-i18n', () => ({ useI18n: () => ({ t: (key: string) => key }) }))
const account = (overrides: Partial<Account> = {}) => ({ id: 7, platform: 'openai', type: 'oauth', schedulable: false, extra: { intelligence_auto_paused: true }, ...overrides } as Account)
const render = (value = account(), busy = false) => mount(AccountSchedulingControl, { props: { account: value, busy } })

describe('GPT scheduling controls', () => {
  it('shows the pause and a plain manual-enable action', async () => {
    const wrapper = render()
    expect(wrapper.get('[data-testid="intelligence-paused"]').text()).toContain('intelligenceScheduling.paused')
    await wrapper.get('[data-testid="enable-scheduling"]').trigger('click')
    expect(wrapper.emitted('change')).toEqual([[true]])
  })
  it.each([true, false])('never changes the opt-in setting (%s) when manually enabling', async enabled => {
    const wrapper = render(account({ extra: { intelligence_auto_pause_enabled: enabled } }))
    await wrapper.get('[role="switch"]').trigger('click')
    expect(wrapper.emitted('change')).toEqual([[true]])
    expect(wrapper.get('[data-testid="intelligence-auto-pause-status"]').text()).toContain(enabled ? '.enabled' : '.monitorOnly')
    expect(wrapper.find('[data-testid="intelligence-auto-pause-toggle"]').exists()).toBe(false)
  })
  it('shows tests-only for accounts without a setting', () => {
    expect(render(account({ extra: {} })).get('[data-testid="intelligence-auto-pause-status"]').text()).toContain('.monitorOnly')
  })
  it.each([
    { platform: 'anthropic' }, { type: 'apikey' }, { parent_account_id: 1 },
    { extra: { synthetic_ui_test: true } }, { credentials: { auth_mode: 'agentIdentity' } },
  ] as Partial<Account>[])('keeps ordinary scheduling for unsupported accounts: %j', (overrides) => {
    const wrapper = render(account(overrides))
    expect(wrapper.find('[role="switch"]').exists()).toBe(true)
    expect(wrapper.find('[data-testid="intelligence-auto-pause-status"]').exists()).toBe(false)
  })
  it('disables all actions while saving', () => {
    const wrapper = render(account({ type: 'setup-token' }), true)
    for (const button of wrapper.findAll('button')) expect(button.attributes('disabled')).toBeDefined()
  })
})
