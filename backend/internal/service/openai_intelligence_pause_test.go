package service

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestIntelligenceConsecutiveFailuresPauseScheduling(t *testing.T) {
	a := excelAccount()
	a.Schedulable = true
	repo := &intelligenceProbeRepo{account: a}
	svc := &AccountTestService{accountRepo: repo}
	record := func(v OpenAICodexStateVerdict) *OpenAICodexStateProbeResult {
		r := &OpenAICodexStateProbeResult{Method: "candy", Route: "bps", Model: "gpt-6-astra", Verdict: v}
		require.NoError(t, svc.persistOpenAICodexStateProbeResult(context.Background(), a, r))
		return r
	}
	require.False(t, record(OpenAICodexStateDegraded).SchedulingPaused)
	require.True(t, a.Schedulable)
	second := record(OpenAICodexStateDegraded)
	require.True(t, second.SchedulingPaused)
	require.False(t, a.Schedulable)
	require.Contains(t, second.Snapshot.Reason, "已自动暂停")
	require.False(t, record(OpenAICodexStateHealthy).SchedulingResumed)
	require.False(t, a.Schedulable)
	require.True(t, record(OpenAICodexStateHealthy).SchedulingResumed)
	require.True(t, a.Schedulable)
}

func TestIntelligencePauseRequiresTwoConsecutiveSameRouteResults(t *testing.T) {
	for _, middle := range []OpenAICodexStateVerdict{OpenAICodexStateHealthy, OpenAICodexStateInconclusive} {
		a := excelAccount()
		a.Schedulable = true
		svc := &AccountTestService{accountRepo: &intelligenceProbeRepo{account: a}}
		for _, v := range []OpenAICodexStateVerdict{OpenAICodexStateDegraded, middle, OpenAICodexStateDegraded} {
			r := &OpenAICodexStateProbeResult{Method: "candy", Route: "bps", Model: "gpt-6-astra", Verdict: v}
			require.NoError(t, svc.persistOpenAICodexStateProbeResult(context.Background(), a, r))
			require.False(t, r.SchedulingPaused)
		}
		require.True(t, a.Schedulable)
	}
	for _, change := range []string{"model", "route", "legacy"} {
		a := excelAccount()
		a.Schedulable = true
		a.Extra[OpenAICodexStateProbeSnapshotExtraKey] = &OpenAICodexStateProbeSnapshot{Method: "candy", Route: "bps", Model: "gpt-6-astra", ConsecutiveDegraded: 1}
		svc := &AccountTestService{accountRepo: &intelligenceProbeRepo{account: a}}
		r := &OpenAICodexStateProbeResult{Method: "candy", Route: "bps", Model: "gpt-6-astra", Verdict: OpenAICodexStateDegraded}
		switch change {
		case "model":
			r.Model = "gpt-6.1-sol"
		case "route":
			r.Route = "codex"
		case "legacy":
			r.Method = "ticket"
		}
		require.NoError(t, svc.persistOpenAICodexStateProbeResult(context.Background(), a, r))
		require.False(t, r.SchedulingPaused)
		require.True(t, a.Schedulable)
	}
}

func TestIntelligencePauseDoesNotAffectOtherAccountTypes(t *testing.T) {
	for _, kind := range []string{"anthropic", "apikey", "shadow", "synthetic"} {
		a := excelAccount()
		a.Schedulable = true
		switch kind {
		case "anthropic":
			a.Platform = PlatformAnthropic
		case "apikey":
			a.Type = AccountTypeAPIKey
		case "shadow":
			id := int64(1)
			a.ParentAccountID = &id
		case "synthetic":
			a.Extra["synthetic_ui_test"] = true
		}
		svc := &AccountTestService{accountRepo: &intelligenceProbeRepo{account: a}}
		for i := 0; i < 2; i++ {
			r := &OpenAICodexStateProbeResult{Method: "candy", Route: "bps", Model: "gpt-6-astra", Verdict: OpenAICodexStateDegraded}
			require.NoError(t, svc.persistOpenAICodexStateProbeResult(context.Background(), a, r))
			require.False(t, r.SchedulingPaused)
		}
		require.True(t, a.Schedulable)
	}
}

type intelligenceAtomicFailureRepo struct{ *intelligenceProbeRepo }

func (*intelligenceAtomicFailureRepo) UpdateIntelligenceProbeSnapshot(context.Context, int64, *OpenAICodexStateProbeSnapshot, bool, bool) (IntelligenceSchedulingTransition, error) {
	return IntelligenceSchedulingTransition{}, errors.New("database write failed")
}
func TestIntelligencePauseDoesNotReportSuccessWhenPersistenceFails(t *testing.T) {
	a := excelAccount()
	a.Schedulable = true
	a.Extra[OpenAICodexStateProbeSnapshotExtraKey] = &OpenAICodexStateProbeSnapshot{Method: "candy", Route: "bps", Model: "gpt-6-astra", ConsecutiveDegraded: 1}
	svc := &AccountTestService{accountRepo: &intelligenceAtomicFailureRepo{&intelligenceProbeRepo{account: a}}}
	r := &OpenAICodexStateProbeResult{Method: "candy", Route: "bps", Model: "gpt-6-astra", Verdict: OpenAICodexStateDegraded}
	require.Error(t, svc.persistOpenAICodexStateProbeResult(context.Background(), a, r))
	require.False(t, r.SchedulingPaused)
	require.Nil(t, r.Snapshot)
	require.True(t, a.Schedulable)
}

func TestIntelligenceRecoveryRespectsManualPauseAndInterruptedStreak(t *testing.T) {
	for _, manual := range []bool{false, true} {
		a := excelAccount()
		a.Schedulable = false
		if !manual {
			a.Extra[IntelligenceAutoPauseExtraKey] = true
		}
		svc := &AccountTestService{accountRepo: &intelligenceProbeRepo{account: a}}
		outcomes := []OpenAICodexStateVerdict{OpenAICodexStateHealthy, OpenAICodexStateInconclusive, OpenAICodexStateHealthy, OpenAICodexStateHealthy}
		for i, v := range outcomes {
			r := &OpenAICodexStateProbeResult{Method: "candy", Route: "bps", Model: "gpt-6-astra", Verdict: v}
			require.NoError(t, svc.persistOpenAICodexStateProbeResult(context.Background(), a, r))
			require.Equal(t, !manual && i == 3, r.SchedulingResumed)
		}
		require.Equal(t, !manual, a.Schedulable)
	}
}
func TestIntelligenceDoesNotClaimAnAlreadyManuallyPausedAccount(t *testing.T) {
	a := excelAccount()
	a.Schedulable = false
	svc := &AccountTestService{accountRepo: &intelligenceProbeRepo{account: a}}
	for _, v := range []OpenAICodexStateVerdict{OpenAICodexStateDegraded, OpenAICodexStateDegraded, OpenAICodexStateHealthy, OpenAICodexStateHealthy} {
		r := &OpenAICodexStateProbeResult{Method: "candy", Route: "bps", Model: "gpt-6-astra", Verdict: v}
		require.NoError(t, svc.persistOpenAICodexStateProbeResult(context.Background(), a, r))
		require.False(t, r.SchedulingPaused)
		require.False(t, r.SchedulingResumed)
		require.False(t, a.Schedulable)
	}
}
