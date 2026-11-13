package service

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestAutomaticPelicanEightMinuteBoundary(t *testing.T) {
	now := time.Now().UTC()
	a := excelAccount()
	a.Extra["openai_excel_bps"] = false
	require.True(t, accountSVGProbeDue(a, now))
	for _, status := range []string{"generated", "failed"} {
		a.Extra[AccountSVGProbeExtraKey] = &AccountSVGProbeResult{ID: "result", Status: status, FinishedAt: now.Add(-8*time.Minute + time.Second)}
		require.False(t, accountSVGProbeDue(a, now))
		a.Extra[AccountSVGProbeExtraKey] = &AccountSVGProbeResult{ID: "result", Status: status, FinishedAt: now.Add(-8 * time.Minute)}
		require.True(t, accountSVGProbeDue(a, now))
	}
	a.Status = "inactive"
	require.False(t, accountSVGProbeDue(a, now))
	a.Status = StatusActive
	a.Extra["synthetic_ui_test"] = true
	require.False(t, accountSVGProbeDue(a, now))
	delete(a.Extra, "synthetic_ui_test")
	parent := int64(9)
	a.ParentAccountID = &parent
	require.False(t, accountSVGProbeDue(a, now))
}

func TestAutomaticPelicanSkipsBusyAndRecentAccounts(t *testing.T) {
	now := time.Now().UTC()
	repo := &intelligenceMonitorRepo{}
	for i := 0; i < 4; i++ {
		a := excelAccount()
		a.ID = int64(i + 1)
		a.Extra[AccountSVGProbeExtraKey] = &AccountSVGProbeResult{ID: "result", FinishedAt: now.Add(-time.Duration(20-i) * time.Minute)}
		repo.accounts = append(repo.accounts, *a)
	}
	service := &AccountTestService{}
	service.svgProbeAccounts.Store(int64(1), struct{}{})
	repo.accounts[3].Extra[AccountSVGProbeExtraKey] = &AccountSVGProbeResult{ID: "recent", FinishedAt: now}
	monitor := &UpstreamBillingProbeService{accountRepo: repo, accountTestService: service}
	due, err := monitor.listDueAccountSVGProbeAccounts(context.Background(), now)
	require.NoError(t, err)
	require.Len(t, due, 2)
	require.Equal(t, int64(2), due[0].ID)
	require.Equal(t, int64(3), due[1].ID)
}

func TestAutomaticProbesDoNotRepeatJustFinishedManualProbe(t *testing.T) {
	a := excelAccount()
	a.Extra["openai_excel_bps"] = false
	now := time.Now().UTC()
	a.Extra[AccountSVGProbeExtraKey] = &AccountSVGProbeResult{ID: "recent", FinishedAt: now}
	a.Extra[OpenAICodexStateProbeSnapshotExtraKey] = &OpenAICodexStateProbeSnapshot{Method: "candy", Route: "codex", Model: "gpt-6-astra", LastProbeAt: now}
	service := &AccountTestService{accountRepo: &intelligenceProbeRepo{account: a}, openaiGatewayService: &OpenAIGatewayService{}}
	svg, err := service.probeAccountSVG(context.Background(), a.ID, true)
	require.NoError(t, err)
	require.Nil(t, svg)
	intelligence, err := service.probeOpenAIAccountIntelligence(context.Background(), a.ID, "gpt-6-astra", true)
	require.NoError(t, err)
	require.Nil(t, intelligence)
	require.False(t, service.AccountSVGProbeRunning(a.ID))
}

func TestIntelligenceNextProbeIsEightMinutesForEveryOutcome(t *testing.T) {
	now := time.Now().UTC()
	for _, verdict := range []OpenAICodexStateVerdict{OpenAICodexStateHealthy, OpenAICodexStateDegraded, OpenAICodexStateInconclusive} {
		a := excelAccount()
		svc := &AccountTestService{accountRepo: &intelligenceProbeRepo{account: a}}
		result := &OpenAICodexStateProbeResult{Method: "candy", Route: "bps", Model: "gpt-6-astra", Verdict: verdict, Failure: OpenAICodexStateFailureUnsupported, FinishedAt: now}
		require.NoError(t, svc.persistOpenAICodexStateProbeResult(context.Background(), a, result))
		require.Equal(t, now.Add(8*time.Minute), result.Snapshot.NextProbeAt)
	}
}

type deniedAutoProbeLeader struct{ LeaderLockCache }

func (*deniedAutoProbeLeader) TryAcquireLeaderLock(context.Context, string, string, time.Duration) (bool, error) {
	return false, nil
}
func TestAutomaticPelicanDoesNotRunWithoutLeaderLease(t *testing.T) {
	monitor := &UpstreamBillingProbeService{accountRepo: &intelligenceMonitorRepo{}, accountTestService: &AccountTestService{}, lockCache: &deniedAutoProbeLeader{}}
	require.NoError(t, monitor.RunAccountSVGProbeDue(context.Background()))
}
