import { beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'
import AccountIntelligenceIndicator from '../AccountIntelligenceIndicator.vue'
import type { Account, OpenAICodexStateProbeSnapshot } from '@/types'

const { probe } = vi.hoisted(() => ({ probe: vi.fn() }))
vi.mock('@/api/admin/accounts', () => ({ probeOpenAIAccountIntelligence: probe }))
vi.mock('vue-i18n', async () => ({
  ...await vi.importActual<typeof import('vue-i18n')>('vue-i18n'),
  useI18n: () => ({ t: (key: string) => key }),
}))

const healthy: OpenAICodexStateProbeSnapshot = {
  method: 'candy', route: 'bps', model: 'gpt-6-astra', status: 'healthy', verdict: 'healthy',
  sample_count: 2, health_rate: 50, history: ['degraded', 'healthy'],
}
const account = (snapshot?: OpenAICodexStateProbeSnapshot) => ({
  id: 7, platform: 'openai', type: 'oauth', openai_codex_state_probe: snapshot,
} as Account)
const render = (value = account()) => mount(AccountIntelligenceIndicator, {
  props: { account: value }, global: { stubs: { Icon: true } },
})

describe('AccountIntelligenceIndicator', () => {
  beforeEach(() => vi.clearAllMocks())

  it.each(['codex', 'bps'] as const)('shows actual %s results with history and pass rate', (route) => {
    const wrapper = render(account({ ...healthy, route }))
    expect(wrapper.get('[data-testid="intelligence-verdict"]').text()).toContain('stateProbe.healthy')
    expect(wrapper.get('[data-testid="intelligence-rate"]').text()).toContain('50.0%')
    expect(wrapper.findAll('[data-verdict="healthy"]')).toHaveLength(1)
    expect(wrapper.findAll('[data-verdict="degraded"]')).toHaveLength(1)
    expect(wrapper.findAll('[data-verdict="unprobed"]')).toHaveLength(22)
    expect(wrapper.text()).toContain(route === 'bps' ? 'BPS' : 'Codex')
  })

  it('does not mistake old native ticket failures for actual intelligence results', () => {
    const wrapper = render(account({ verdict: 'degraded', health_rate: 0 }))
    expect(wrapper.text()).toContain('stateProbe.unprobed')
    expect(wrapper.find('[data-testid="intelligence-rate"]').exists()).toBe(false)
  })

  it('shows transport failures as inconclusive while retaining the historical pass rate', () => {
    const wrapper = render(account({ ...healthy, verdict: 'inconclusive', failure: 'network_error' }))
    expect(wrapper.get('[data-testid="intelligence-verdict"]').text()).toContain('stateProbe.inconclusive')
    expect(wrapper.get('[data-testid="intelligence-rate"]').text()).toContain('50.0%')
  })

  it('updates immediately after retesting and emits the persisted snapshot', async () => {
    probe.mockResolvedValue({ snapshot: healthy })
    const wrapper = render()
    await wrapper.get('button').trigger('click')
    await flushPromises()
    expect(probe).toHaveBeenCalledWith(7, undefined, { signal: expect.any(AbortSignal) })
    expect(wrapper.emitted('updated')).toEqual([[healthy]])
    expect(wrapper.text()).toContain('stateProbe.healthy')
    expect(wrapper.get('button').attributes('disabled')).toBeUndefined()
  })

  it('disables repeat clicks and discards results when the account changes', async () => {
    let finish!: (value: unknown) => void
    probe.mockImplementation(() => new Promise(resolve => { finish = resolve }))
    const wrapper = render()
    await wrapper.get('button').trigger('click')
    expect(wrapper.get('button').attributes('disabled')).toBeDefined()
    await wrapper.setProps({ account: { ...account(), id: 8 } })
    expect(probe.mock.calls[0]![2].signal.aborted).toBe(true)
    finish({ snapshot: healthy })
    await flushPromises()
    expect(wrapper.emitted('updated')).toBeUndefined()
    expect(wrapper.text()).toContain('stateProbe.unprobed')
  })

  it('keeps the last measurement when the retest request fails', async () => {
    probe.mockRejectedValue(new Error('network unavailable'))
    const wrapper = render(account(healthy))
    await wrapper.get('button').trigger('click')
    await flushPromises()
    expect(wrapper.get('[role="alert"]').text()).toBe('network unavailable')
    expect(wrapper.text()).toContain('stateProbe.healthy')
    expect(wrapper.emitted('updated')).toBeUndefined()
  })

  it('renders only the latest 24 real results and rejects invalid rates', () => {
    const wrapper = render(account({ ...healthy, health_rate: NaN, history: Array(40).fill('healthy') }))
    expect(wrapper.findAll('[data-verdict="healthy"]')).toHaveLength(24)
    expect(wrapper.find('[data-testid="intelligence-rate"]').exists()).toBe(false)
  })

  it('does not show an OpenAI intelligence panel for other platforms', () => {
    expect(render({ ...account(), platform: 'anthropic' }).find('[data-testid="account-intelligence"]').exists()).toBe(false)
  })
})
