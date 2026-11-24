//go:build integration

package repository

import (
	"context"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestUpstreamBalanceSnapshotSurvivesEditAndClearsOnIdentityChange(t *testing.T) {
	ctx := context.Background()
	tx := testEntTx(t)
	repo := newAccountRepositoryWithSQL(tx.Client(), tx, nil)
	a := mustCreateAccount(t, tx.Client(), &service.Account{Name: "wallet-test", Platform: service.PlatformOpenAI, Type: service.AccountTypeAPIKey, Credentials: map[string]any{"api_key": "sk-test", "base_url": "https://relay.example"}})
	stale, err := repo.GetByID(ctx, a.ID)
	require.NoError(t, err)
	now := time.Now().UTC()
	amount := 8465.83
	snapshot := &service.UpstreamBillingProbeSnapshot{Status: "ok", LastAttemptAt: now, Balance: &service.UpstreamBalanceProbeSnapshot{Status: "ok", Amount: &amount, Currency: "USD", ReceivedAt: &now, FreshUntil: &now, LastAttemptAt: now}}
	require.NoError(t, repo.UpdateUpstreamBillingProbeSnapshot(ctx, stale, snapshot, nil, service.BillingGroupInfo{}))
	stale.Name = "renamed"
	require.NoError(t, repo.Update(ctx, stale))
	got, err := repo.GetByID(ctx, a.ID)
	require.NoError(t, err)
	items := service.BuildUpstreamBillingRateSnapshotItems([]service.Account{*got})
	require.Len(t, items, 1)
	require.NotNil(t, items[0].Snapshot.Balance)
	require.Equal(t, amount, *items[0].Snapshot.Balance.Amount)
	require.Equal(t, "USD", items[0].Snapshot.Balance.Currency)
	got.Credentials["api_key"] = "sk-new-owner"
	require.NoError(t, repo.Update(ctx, got))
	got, err = repo.GetByID(ctx, a.ID)
	require.NoError(t, err)
	require.NotContains(t, got.Extra, service.UpstreamBillingProbeExtraKey)
}
