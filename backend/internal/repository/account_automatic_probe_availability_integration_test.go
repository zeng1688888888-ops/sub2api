//go:build integration

package repository

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestAutomaticProbeQuotaFilterDoesNotStarveHealthyAccounts(t *testing.T) {
	ctx := context.Background()
	tx := testEntTx(t)
	repo := newAccountRepositoryWithSQL(tx.Client(), tx, nil)
	now := time.Now().UTC()
	var firstID int64
	for i := 0; i < 103; i++ {
		created := mustCreateAccount(t, tx.Client(), &service.Account{Name: fmt.Sprintf("exhausted-%d", i), Platform: service.PlatformOpenAI, Type: service.AccountTypeOAuth, Schedulable: true, Extra: map[string]any{"codex_5h_used_percent": 100, "codex_5h_reset_at": now.Add(time.Hour).Format(time.RFC3339Nano)}})
		if i == 0 {
			firstID = created.ID
		}
	}
	paused := mustCreateAccount(t, tx.Client(), &service.Account{Name: "manual-paused-available", Platform: service.PlatformOpenAI, Type: service.AccountTypeOAuth})
	require.NoError(t, repo.SetSchedulable(ctx, paused.ID, false))
	healthy := mustCreateAccount(t, tx.Client(), &service.Account{Name: "quota-reset", Platform: service.PlatformOpenAI, Type: service.AccountTypeOAuth, Schedulable: true, Extra: map[string]any{"codex_7d_used_percent": 100, "codex_7d_reset_at": now.Add(-time.Minute).Format(time.RFC3339Nano)}})
	for _, list := range []func(context.Context, time.Time, int) ([]service.Account, error){repo.ListDueOpenAICodexStateProbeAccounts, repo.ListDueAccountSVGProbeAccounts} {
		due, err := list(ctx, now, 2)
		require.NoError(t, err)
		require.Len(t, due, 2)
		require.Equal(t, paused.ID, due[0].ID)
		require.Equal(t, healthy.ID, due[1].ID)
		require.False(t, due[0].Schedulable)
	}
	// A new quota snapshot makes the formerly exhausted account eligible again.
	require.NoError(t, repo.UpdateExtra(ctx, firstID, map[string]any{"codex_5h_used_percent": 0}))
	due, err := repo.ListDueOpenAICodexStateProbeAccounts(ctx, now, 1)
	require.NoError(t, err)
	require.Len(t, due, 1)
	require.Equal(t, firstID, due[0].ID)
}

func TestAutomaticProbeSkipsCooldownAndExpiryUntilRecovery(t *testing.T) {
	ctx := context.Background()
	tx := testEntTx(t)
	repo := newAccountRepositoryWithSQL(tx.Client(), tx, nil)
	now := time.Now().UTC()
	future, past := now.Add(time.Hour), now.Add(-time.Hour)
	for _, kind := range []string{"rate", "overload", "temporary", "expired"} {
		a := mustCreateAccount(t, tx.Client(), &service.Account{Name: kind, Platform: service.PlatformOpenAI, Type: service.AccountTypeOAuth, Schedulable: true})
		switch kind {
		case "rate":
			require.NoError(t, repo.SetRateLimited(ctx, a.ID, future))
		case "overload":
			require.NoError(t, repo.SetOverloaded(ctx, a.ID, future))
		case "temporary":
			require.NoError(t, repo.SetTempUnschedulable(ctx, a.ID, future, "test"))
		case "expired":
			require.NoError(t, tx.Client().Account.UpdateOneID(a.ID).SetAutoPauseOnExpired(true).SetExpiresAt(past).Exec(ctx))
		}
	}
	for _, list := range []func(context.Context, time.Time, int) ([]service.Account, error){repo.ListDueOpenAICodexStateProbeAccounts, repo.ListDueAccountSVGProbeAccounts} {
		due, err := list(ctx, now, 20)
		require.NoError(t, err)
		require.Empty(t, due)
		due, err = list(ctx, future.Add(time.Second), 20)
		require.NoError(t, err)
		require.Len(t, due, 3, "expired account stays excluded, completed cooldowns are eligible again")
	}
}
