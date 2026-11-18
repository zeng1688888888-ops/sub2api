package service

import (
	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/google/uuid"
)

const (
	IntelligenceAutoPauseEnabledExtraKey     = "intelligence_auto_pause_enabled"
	IntelligenceSchedulingGenerationExtraKey = "intelligence_scheduling_generation"
)

func (a *Account) SupportsIntelligenceScheduling() bool {
	return a != nil && a.IsOpenAIOAuthLike() && !a.IsShadow() && !a.IsSyntheticUITest() && !a.IsOpenAIAgentIdentity()
}

// IntelligenceAutoPauseEnabled is opt-in. Missing flags, including legacy accounts,
// keep testing without letting intelligence results change scheduling.
func (a *Account) IntelligenceAutoPauseEnabled() bool {
	if !a.SupportsIntelligenceScheduling() {
		return false
	}
	enabled, _ := a.Extra[IntelligenceAutoPauseEnabledExtraKey].(bool)
	return enabled
}

func (a *Account) IntelligenceSchedulingGeneration() string {
	if a == nil {
		return ""
	}
	generation, _ := a.Extra[IntelligenceSchedulingGenerationExtraKey].(string)
	return generation
}

func requestedIntelligenceAutoPauseSetting(account *Account, extra map[string]any) (*bool, error) {
	raw, present := extra[IntelligenceAutoPauseEnabledExtraKey]
	if !present {
		return nil, nil
	}
	enabled, ok := raw.(bool)
	if !ok {
		return nil, infraerrors.BadRequest("INVALID_INTELLIGENCE_AUTO_PAUSE", "intelligence_auto_pause_enabled must be a boolean")
	}
	if enabled && !account.SupportsIntelligenceScheduling() {
		return nil, infraerrors.BadRequest("INVALID_INTELLIGENCE_AUTO_PAUSE", "intelligence auto-pause is only supported for GPT OAuth/setup-token accounts")
	}
	return &enabled, nil
}

// ApplyIntelligenceAutoPauseSetting changes only the policy, never schedulable.
// Callers updating an existing row must hold its lock and use the latest extra.
func ApplyIntelligenceAutoPauseSetting(account *Account, enabled bool) {
	if account.Extra == nil {
		account.Extra = make(map[string]any)
	}
	changed := account.IntelligenceAutoPauseEnabled() != enabled
	if !account.SupportsIntelligenceScheduling() {
		delete(account.Extra, IntelligenceAutoPauseEnabledExtraKey)
		return
	}
	account.Extra[IntelligenceAutoPauseEnabledExtraKey] = enabled
	if !changed {
		return
	}
	generation := uuid.NewString()
	account.Extra[IntelligenceSchedulingGenerationExtraKey] = generation
	if snapshot := OpenAICodexStateProbeSnapshotFromExtra(account.Extra); snapshot != nil {
		snapshot.ConsecutiveDegraded = 0
		snapshot.ConsecutiveHealthy = 0
		snapshot.SchedulingGeneration = generation
		account.Extra[OpenAICodexStateProbeSnapshotExtraKey] = snapshot
	}
}
