import { beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'
import AccountIntelligenceTestModal from '../AccountIntelligenceTestModal.vue'
import type { Account } from '@/types'

const { probe } = vi.hoisted(() => ({ probe: vi.fn() }))
vi.mock('@/api/admin/accounts', () => ({ probeOpenAIAccountIntelligence: probe }))
vi.mock('vue-i18n', async () => ({
  ...await vi.importActual<typeof import('vue-i18n')>('vue-i18n'),
  useI18n: () => ({ t: (key: string) => key }),
}))

const snapshot = { method: 'candy', route: 'bps', verdict: 'healthy', sample_count: 1, health_rate: 100 }
const result = {
  account_id: 7, model: 'gpt-6-astra', method: 'candy', route: 'bps', verdict: 'healthy',
  answer: '21', reason: 'correct answer', latency_ms: 2300,
  started_at: '2026-09-27T10:00:00Z', finished_at: '2026-09-27T10:00:02Z', snapshot,
}

function render() {
  return mount(AccountIntelligenceTestModal, {
    props: { show: true, account: { id: 7, name: 'BPS account', platform: 'openai', type: 'oauth' } as Account },
    global: { stubs: {
      BaseDialog: { template: '<div><slot /><slot name="footer" /></div>' },
      Input: true, Icon: true,
    } },
  })
}

describe('AccountIntelligenceTestModal', () => {
  beforeEach(() => { probe.mockReset() })

  it.each(['codex', 'bps'])('renders a real answer from %s and refreshes the list', async route => {
    probe.mockResolvedValue({ ...result, route })
    const wrapper = render()
    await wrapper.get('[data-testid="intelligence-start"]').trigger('click')
    await flushPromises()
    expect(probe).toHaveBeenCalledWith(7, 'gpt-6-astra', { signal: expect.any(AbortSignal) })
    expect(wrapper.get('[data-testid="intelligence-result-route"]').text()).toBe(route === 'bps' ? 'BPS' : 'Codex')
    expect(wrapper.get('[data-testid="intelligence-result-answer"]').text()).toBe('21')
    expect(wrapper.get('[data-testid="intelligence-result-verdict"]').text()).toContain('stateProbe.healthy')
    expect(wrapper.text()).toContain('100.0%')
    expect(wrapper.emitted('updated')?.[0]?.[0]).toMatchObject({ id: 7, openai_codex_state_probe: snapshot })
    wrapper.unmount()
  })

  it('shows inconclusive errors without a misleading healthy result', async () => {
    probe.mockResolvedValue({ ...result, verdict: 'inconclusive', answer: '', detail: 'upstream unavailable', snapshot: { ...snapshot, verdict: 'inconclusive', sample_count: 0, health_rate: undefined } })
    const wrapper = render()
    await wrapper.get('[data-testid="intelligence-start"]').trigger('click')
    await flushPromises()
    expect(wrapper.get('[data-testid="intelligence-result-verdict"]').text()).toContain('stateProbe.inconclusive')
    expect(wrapper.text()).toContain('upstream unavailable')
    expect(wrapper.text()).not.toContain('100.0%')
    wrapper.unmount()
  })

  it('shows request failures and enables retry', async () => {
    probe.mockRejectedValue(new Error('already testing'))
    const wrapper = render()
    await wrapper.get('[data-testid="intelligence-start"]').trigger('click')
    await flushPromises()
    expect(wrapper.get('[role="alert"]').text()).toBe('already testing')
    expect(wrapper.get('[data-testid="intelligence-start"]').attributes('disabled')).toBeUndefined()
    wrapper.unmount()
  })

  it('aborts on close and ignores a late response', async () => {
    let finish!: (value: unknown) => void
    probe.mockImplementation(() => new Promise(resolve => { finish = resolve }))
    const wrapper = render()
    await wrapper.get('[data-testid="intelligence-start"]').trigger('click')
    expect(wrapper.get('[data-testid="intelligence-start"]').attributes('disabled')).toBeDefined()
    await wrapper.setProps({ show: false })
    expect(probe.mock.calls[0]![2].signal.aborted).toBe(true)
    finish(result)
    await flushPromises()
    expect(wrapper.emitted('updated')).toBeUndefined()
    expect(wrapper.find('[data-testid="intelligence-result"]').exists()).toBe(false)
    wrapper.unmount()
  })
})
