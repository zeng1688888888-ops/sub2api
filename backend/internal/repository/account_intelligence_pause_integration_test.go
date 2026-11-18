//go:build integration

package repository

import (
	"context"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestIntelligenceSchedulingOptInLifecycle(t *testing.T) {
	ctx := context.Background()
	tx := testEntTx(t)
	repo := newAccountRepositoryWithSQL(tx.Client(), tx, nil)
	a := mustCreateAccount(t, tx.Client(), &service.Account{Name: "intelligence-opt-in", Platform: service.PlatformOpenAI, Type: service.AccountTypeOAuth, Schedulable: true, Extra: map[string]any{"keep": "untouched"}})
	s := &service.OpenAICodexStateProbeSnapshot{Method: "candy", Route: "codex", Model: "gpt-6-astra", Verdict: service.OpenAICodexStateDegraded, ConsecutiveDegraded: 3, History: []service.OpenAICodexStateProbeVerdict{service.OpenAICodexStateProbeHistoryDegraded}}
	transition, err := repo.UpdateIntelligenceProbeSnapshot(ctx, a.ID, s, true)
	require.NoError(t, err)
	require.False(t, transition.Paused, "missing setting defaults to testing only")
	got, err := repo.GetByID(ctx, a.ID)
	require.NoError(t, err)
	require.True(t, got.Schedulable)
	enabled := true
	require.NoError(t, repo.UpdateWithAccountBillingSettings(ctx, got, nil, nil, nil, &enabled))
	got, err = repo.GetByID(ctx, a.ID)
	require.NoError(t, err)
	require.True(t, got.IntelligenceAutoPauseEnabled())
	current := service.OpenAICodexStateProbeSnapshotFromAccount(got)
	require.Zero(t, current.ConsecutiveDegraded)
	require.Len(t, current.History, 1)
	// Turning the setting on cannot act on tests that began while it was off.
	transition, err = repo.UpdateIntelligenceProbeSnapshot(ctx, a.ID, s, true)
	require.NoError(t, err)
	require.False(t, transition.Paused)
	require.Zero(t, s.ConsecutiveDegraded)
	for i := 1; i <= 2; i++ {
		s.ConsecutiveDegraded = i
		transition, err = repo.UpdateIntelligenceProbeSnapshot(ctx, a.ID, s, i >= 2)
		require.NoError(t, err)
		require.Equal(t, i == 2, transition.Paused)
	}
	got, err = repo.GetByID(ctx, a.ID)
	require.NoError(t, err)
	require.False(t, got.Schedulable)
	s.Verdict = service.OpenAICodexStateHealthy
	s.ConsecutiveHealthy = 3
	transition, err = repo.UpdateIntelligenceProbeSnapshot(ctx, a.ID, s, false)
	require.NoError(t, err)
	require.False(t, transition.Resumed)
	got, err = repo.GetByID(ctx, a.ID)
	require.NoError(t, err)
	require.False(t, got.Schedulable)

	// Manual enable preserves the opt-in and starts a new streak.
	require.NoError(t, repo.SetSchedulable(ctx, a.ID, true))
	got, err = repo.GetByID(ctx, a.ID)
	require.NoError(t, err)
	require.True(t, got.IntelligenceAutoPauseEnabled())
	require.True(t, got.Schedulable)
	require.Zero(t, service.OpenAICodexStateProbeSnapshotFromAccount(got).ConsecutiveDegraded)
	s.Verdict = service.OpenAICodexStateDegraded
	s.ConsecutiveDegraded = 4
	transition, err = repo.UpdateIntelligenceProbeSnapshot(ctx, a.ID, s, true)
	require.NoError(t, err)
	require.False(t, transition.Paused, "in-flight tests cannot undo manual enable")
	s.ConsecutiveDegraded = 2
	transition, err = repo.UpdateIntelligenceProbeSnapshot(ctx, a.ID, s, true)
	require.NoError(t, err)
	require.True(t, transition.Paused)

	// Turning off the setting does not enable a paused account.
	got, err = repo.GetByID(ctx, a.ID)
	require.NoError(t, err)
	disabled := false
	require.NoError(t, repo.UpdateWithAccountBillingSettings(ctx, got, nil, nil, nil, &disabled))
	got, err = repo.GetByID(ctx, a.ID)
	require.NoError(t, err)
	require.False(t, got.Schedulable)
	require.False(t, got.IntelligenceAutoPauseEnabled())
	require.NoError(t, repo.SetSchedulable(ctx, a.ID, true))
	got, err = repo.GetByID(ctx, a.ID)
	require.NoError(t, err)
	require.False(t, got.IntelligenceAutoPauseEnabled(), "manual enable must never enable the opt-in")
	s = service.OpenAICodexStateProbeSnapshotFromAccount(got)
	s.ConsecutiveDegraded = 10
	transition, err = repo.UpdateIntelligenceProbeSnapshot(ctx, a.ID, s, true)
	require.NoError(t, err)
	require.False(t, transition.Paused)
	got, err = repo.GetByID(ctx, a.ID)
	require.NoError(t, err)
	require.True(t, got.Schedulable)
	require.Equal(t, "untouched", got.Extra["keep"])

	// Bulk enable also preserves the per-account choice.
	_, err = repo.BulkUpdate(ctx, []int64{a.ID}, service.AccountBulkUpdate{Schedulable: &enabled})
	require.NoError(t, err)
	got, err = repo.GetByID(ctx, a.ID)
	require.NoError(t, err)
	require.False(t, got.IntelligenceAutoPauseEnabled())
	require.NoError(t, repo.UpdateWithAccountBillingSettings(ctx, got, nil, nil, nil, &enabled))
	_, err = repo.BulkUpdate(ctx, []int64{a.ID}, service.AccountBulkUpdate{Schedulable: &enabled})
	require.NoError(t, err)
	got, err = repo.GetByID(ctx, a.ID)
	require.NoError(t, err)
	require.True(t, got.IntelligenceAutoPauseEnabled())
}

func TestIntelligenceOptInSurvivesUnrelatedStaleEdit(t *testing.T) {
	ctx := context.Background()
	tx := testEntTx(t)
	repo := newAccountRepositoryWithSQL(tx.Client(), tx, nil)
	a := mustCreateAccount(t, tx.Client(), &service.Account{Name: "stale-edit", Platform: service.PlatformOpenAI, Type: service.AccountTypeOAuth, Schedulable: true})
	stale, err := repo.GetByID(ctx, a.ID)
	require.NoError(t, err)
	current, err := repo.GetByID(ctx, a.ID)
	require.NoError(t, err)
	enabled := true
	require.NoError(t, repo.UpdateWithAccountBillingSettings(ctx, current, nil, nil, nil, &enabled))
	stale.Name = "renamed"
	stale.Extra = map[string]any{"note": "keep"}
	require.NoError(t, repo.UpdateWithAccountBillingSettings(ctx, stale, nil, nil, nil, nil))
	got, err := repo.GetByID(ctx, a.ID)
	require.NoError(t, err)
	require.True(t, got.IntelligenceAutoPauseEnabled())
	require.Equal(t, "keep", got.Extra["note"])
	disabled := false
	require.NoError(t, repo.UpdateWithAccountBillingSettings(ctx, got, nil, nil, nil, &disabled))
	require.NoError(t, repo.Update(ctx, stale))
	got, err = repo.GetByID(ctx, a.ID)
	require.NoError(t, err)
	require.False(t, got.IntelligenceAutoPauseEnabled(), "stale form must not re-enable the setting")
}

func TestIntelligenceSettingEditCannotReopenConcurrentAutoPause(t *testing.T) {
	ctx := context.Background()
	tx := testEntTx(t)
	repo := newAccountRepositoryWithSQL(tx.Client(), tx, nil)
	a := mustCreateAccount(t, tx.Client(), &service.Account{Name: "concurrent-pause", Platform: service.PlatformOpenAI, Type: service.AccountTypeOAuth, Schedulable: true, Extra: map[string]any{service.IntelligenceAutoPauseEnabledExtraKey: true}})
	stale, err := repo.GetByID(ctx, a.ID)
	require.NoError(t, err)
	snapshot := &service.OpenAICodexStateProbeSnapshot{Method: "candy", Route: "codex", Model: "gpt-6-astra", Verdict: service.OpenAICodexStateDegraded, ConsecutiveDegraded: 2}
	transition, err := repo.UpdateIntelligenceProbeSnapshot(ctx, a.ID, snapshot, true)
	require.NoError(t, err)
	require.True(t, transition.Paused)
	disabled := false
	require.NoError(t, repo.UpdateWithAccountBillingSettings(ctx, stale, nil, nil, nil, &disabled))
	got, err := repo.GetByID(ctx, a.ID)
	require.NoError(t, err)
	require.False(t, got.IntelligenceAutoPauseEnabled())
	require.False(t, got.Schedulable, "saving the policy must not undo the concurrent pause")
}
