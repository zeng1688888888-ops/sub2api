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
	require.NoError(t, repo.UpdateIntelligenceProbeSnapshot(ctx, a.ID, s, true))
	got, err := repo.GetByID(ctx, a.ID)
	require.NoError(t, err)
	require.False(t, got.Schedulable)
	require.Equal(t, "untouched", got.Extra["keep"])
	snapshot := service.OpenAICodexStateProbeSnapshotFromAccount(got)
	require.NotNil(t, snapshot)
	require.Equal(t, 2, snapshot.ConsecutiveDegraded)
	s.Verdict = service.OpenAICodexStateHealthy
	s.ConsecutiveDegraded = 0
	require.NoError(t, repo.UpdateIntelligenceProbeSnapshot(ctx, a.ID, s, false))
	got, err = repo.GetByID(ctx, a.ID)
	require.NoError(t, err)
	require.False(t, got.Schedulable)
}
