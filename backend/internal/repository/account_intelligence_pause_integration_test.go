//go:build integration

package repository

import (
	"context"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestIntelligenceSnapshotPausesWithoutReenabling(t *testing.T) {
	ctx := context.Background()
	tx := testEntTx(t)
	client := tx.Client()
	repo := newAccountRepositoryWithSQL(client, tx, nil)
	a := mustCreateAccount(t, client, &service.Account{Name: "intelligence-pause", Platform: service.PlatformOpenAI, Type: service.AccountTypeOAuth, Schedulable: true, Extra: map[string]any{"keep": "untouched"}})
	s := &service.OpenAICodexStateProbeSnapshot{Method: "candy", Route: "codex", Model: "gpt-6-astra", Verdict: service.OpenAICodexStateDegraded, ConsecutiveDegraded: 2, Reason: "two failed answers"}
	transition, err := repo.UpdateIntelligenceProbeSnapshot(ctx, a.ID, s, true, false)
	require.NoError(t, err)
	require.True(t, transition.Paused)
	got, err := repo.GetByID(ctx, a.ID)
	require.NoError(t, err)
	require.False(t, got.Schedulable)
	require.Equal(t, "untouched", got.Extra["keep"])
	snapshot := service.OpenAICodexStateProbeSnapshotFromAccount(got)
	require.NotNil(t, snapshot)
	require.Equal(t, 2, snapshot.ConsecutiveDegraded)
	s.Verdict = service.OpenAICodexStateHealthy
	s.ConsecutiveDegraded = 0
	_, err = repo.UpdateIntelligenceProbeSnapshot(ctx, a.ID, s, false, false)
	require.NoError(t, err)
	got, err = repo.GetByID(ctx, a.ID)
	require.NoError(t, err)
	require.False(t, got.Schedulable)
	transition, err = repo.UpdateIntelligenceProbeSnapshot(ctx, a.ID, s, false, true)
	require.NoError(t, err)
	require.True(t, transition.Resumed)
	got, err = repo.GetByID(ctx, a.ID)
	require.NoError(t, err)
	require.True(t, got.Schedulable)
	require.NotContains(t, got.Extra, service.IntelligenceAutoPauseExtraKey)
	// The old snapshot cannot undo a manual scheduling change made while a probe runs.
	_, err = repo.UpdateIntelligenceProbeSnapshot(ctx, a.ID, s, true, false)
	require.NoError(t, err)
	require.NoError(t, repo.SetSchedulable(ctx, a.ID, false))
	transition, err = repo.UpdateIntelligenceProbeSnapshot(ctx, a.ID, s, false, true)
	require.NoError(t, err)
	require.False(t, transition.Resumed)
	got, err = repo.GetByID(ctx, a.ID)
	require.NoError(t, err)
	require.False(t, got.Schedulable)
	// Bulk scheduling also revokes ownership, even when it writes extra fields.
	require.NoError(t, repo.SetSchedulable(ctx, a.ID, true))
	_, err = repo.UpdateIntelligenceProbeSnapshot(ctx, a.ID, s, true, false)
	require.NoError(t, err)
	disabled := false
	_, err = repo.BulkUpdate(ctx, []int64{a.ID}, service.AccountBulkUpdate{Schedulable: &disabled, Extra: map[string]any{"edited": true}})
	require.NoError(t, err)
	transition, err = repo.UpdateIntelligenceProbeSnapshot(ctx, a.ID, s, false, true)
	require.NoError(t, err)
	require.False(t, transition.Resumed)
	got, err = repo.GetByID(ctx, a.ID)
	require.NoError(t, err)
	require.False(t, got.Schedulable)
}
