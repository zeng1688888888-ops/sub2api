import { describe, expect, it } from 'vitest'
import { applyIntelligenceSnapshot } from '../accountIntelligence'
import type { Account, OpenAICodexStateProbeSnapshot } from '@/types'

describe('intelligence scheduling updates', () => {
  const account = { id: 1, schedulable: true, extra: { intelligence_scheduling_generation: 'current', intelligence_auto_pause_enabled: true } } as Account
  it('shows the manual-enable state immediately after the server pauses scheduling', () => {
    const snapshot = { scheduling_generation: 'current', verdict: 'degraded' } as OpenAICodexStateProbeSnapshot
    const updated = applyIntelligenceSnapshot(account, snapshot, true)
    expect(updated.schedulable).toBe(false)
    expect(updated.extra?.intelligence_auto_paused).toBe(true)
    expect(account.schedulable).toBe(true)
  })
  it('preserves a newer manual choice when a probe response arrives late', () => {
    expect(applyIntelligenceSnapshot(account, { scheduling_generation: 'old' }, true)).toBe(account)
  })
  it('records healthy results without enabling a paused account', () => {
    const paused = { ...account, schedulable: false }
    const snapshot = { scheduling_generation: 'current', verdict: 'healthy' } as OpenAICodexStateProbeSnapshot
    expect(applyIntelligenceSnapshot(paused, snapshot).schedulable).toBe(false)
  })
})
