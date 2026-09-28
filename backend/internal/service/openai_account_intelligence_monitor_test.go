package service

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

type intelligenceMonitorRepo struct {
	AccountRepository
	accounts []Account
}

func (r *intelligenceMonitorRepo) ListByPlatform(context.Context, string) ([]Account, error) {
	return r.accounts, nil
}

func TestOpenAIIntelligenceMonitorSelectsBothRoutesAndDueResults(t *testing.T) {
	now := time.Now()
	native, bps, future, inactive, apiKey, legacy, switched := excelAccount(), excelAccount(), excelAccount(), excelAccount(), excelAccount(), excelAccount(), excelAccount()
	native.Extra["openai_excel_bps"] = false
	inactive.Status = "inactive"
	apiKey.Type = AccountTypeAPIKey
	future.Extra[OpenAICodexStateProbeSnapshotExtraKey] = &OpenAICodexStateProbeSnapshot{Method: "candy", Route: "bps", Model: "gpt-6-astra", NextProbeAt: now.Add(time.Hour)}
	legacy.Extra[OpenAICodexStateProbeSnapshotExtraKey] = &OpenAICodexStateProbeSnapshot{Verdict: OpenAICodexStateDegraded, NextProbeAt: now.Add(time.Hour)}
	switched.Extra[OpenAICodexStateProbeSnapshotExtraKey] = &OpenAICodexStateProbeSnapshot{Method: "candy", Route: "codex", Model: "gpt-6-astra", NextProbeAt: now.Add(time.Hour)}
	repo := &intelligenceMonitorRepo{}
	for i, account := range []*Account{native, bps, future, inactive, apiKey, legacy, switched} {
		account.ID = int64(i + 1)
		repo.accounts = append(repo.accounts, *account)
	}
	svc := &UpstreamBillingProbeService{accountRepo: repo}
	due, err := svc.listDueOpenAICodexStateProbeAccounts(context.Background(), now)
	require.NoError(t, err)
	var ids []int64
	for _, account := range due {
		ids = append(ids, account.ID)
	}
	require.Equal(t, []int64{1, 2, 6, 7}, ids)
}

func TestOpenAIIntelligenceSnapshotRetainsBoundedConclusiveHistory(t *testing.T) {
	account := excelAccount()
	history := make([]OpenAICodexStateProbeVerdict, openAICodexStateProbeHistoryLimit)
	for i := range history {
		history[i] = OpenAICodexStateProbeHistoryHealthy
	}
	account.Extra[OpenAICodexStateProbeSnapshotExtraKey] = &OpenAICodexStateProbeSnapshot{Method: "candy", Route: "bps", Model: "gpt-6-astra", History: history}
	svc := &AccountTestService{accountRepo: &intelligenceProbeRepo{account: account}}
	result := &OpenAICodexStateProbeResult{Method: "candy", Route: "bps", Model: "gpt-6-astra", Verdict: OpenAICodexStateDegraded}
	require.NoError(t, svc.persistOpenAICodexStateProbeResult(context.Background(), account, result))
	require.Len(t, result.Snapshot.History, 500)
	require.Equal(t, 499, result.Snapshot.HealthyCount)
	require.Equal(t, 1, result.Snapshot.DegradedCount)
	require.Equal(t, 99.8, *result.Snapshot.HealthRate)
	result.Verdict = OpenAICodexStateInconclusive
	require.NoError(t, svc.persistOpenAICodexStateProbeResult(context.Background(), account, result))
	require.Equal(t, 500, result.Snapshot.SampleCount)
	require.Equal(t, 99.8, *result.Snapshot.HealthRate)
}
