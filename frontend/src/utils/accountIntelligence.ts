import type { Account, OpenAICodexStateProbeSnapshot } from '@/types'

export function supportsIntelligenceScheduling(account: Account): boolean {
  return account.platform === 'openai'
    && ['oauth', 'setup-token'].includes(account.type)
    && account.parent_account_id == null
    && account.extra?.synthetic_ui_test !== true
    && !(account.type === 'oauth' && String(account.credentials?.auth_mode ?? '').trim().toLowerCase() === 'agentidentity')
}

// A delayed probe response must not overwrite a newer manual scheduling choice.
export function isCurrentIntelligenceSnapshot(account: Account, snapshot: OpenAICodexStateProbeSnapshot): boolean {
  return (snapshot.scheduling_generation ?? '') === (account.extra?.intelligence_scheduling_generation ?? '')
}

export function applyIntelligenceSnapshot(account: Account, snapshot: OpenAICodexStateProbeSnapshot, paused = false): Account {
  if (!isCurrentIntelligenceSnapshot(account, snapshot)) return account
  return {
    ...account,
    openai_codex_state_probe: snapshot,
    ...(paused ? { schedulable: false, extra: { ...account.extra, intelligence_auto_paused: true } } : {}),
  }
}
