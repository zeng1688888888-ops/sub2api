package service

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestAccountAutomaticProbeAvailability(t *testing.T) {
	now := time.Now().UTC()
	future, past := now.Add(time.Hour), now.Add(-time.Hour)
	cases := []struct {
		name   string
		change func(*Account)
		want   bool
	}{
		{"healthy", func(a *Account) {}, true},
		{"manual_pause", func(a *Account) { a.Schedulable = false }, true},
		{"intelligence_pause", func(a *Account) { a.Schedulable = false; a.Extra[IntelligenceAutoPauseExtraKey] = true }, true},
		{"inactive", func(a *Account) { a.Status = "inactive" }, false},
		{"error", func(a *Account) { a.Status = StatusError }, false},
		{"429", func(a *Account) { a.RateLimitResetAt = &future }, false},
		{"429_expired", func(a *Account) { a.RateLimitResetAt = &past }, true},
		{"overload", func(a *Account) { a.OverloadUntil = &future }, false},
		{"cooldown", func(a *Account) { a.TempUnschedulableUntil = &future }, false},
		{"expired_account", func(a *Account) { a.AutoPauseOnExpired = true; a.ExpiresAt = &past }, false},
		{"expiry_opt_out", func(a *Account) { a.AutoPauseOnExpired = false; a.ExpiresAt = &past }, true},
		{"5h_exhausted", func(a *Account) {
			a.Extra["codex_5h_used_percent"] = 100.0
			a.Extra["codex_5h_reset_at"] = future.Format(time.RFC3339Nano)
		}, false},
		{"7d_exhausted", func(a *Account) {
			a.Extra["codex_7d_used_percent"] = "100"
			a.Extra["codex_7d_reset_at"] = future.Format(time.RFC3339Nano)
		}, false},
		{"still_has_quota", func(a *Account) { a.Extra["codex_5h_used_percent"] = 99.9; a.Extra["codex_7d_used_percent"] = 99.9 }, true},
		{"exhausted_without_reset", func(a *Account) { a.Extra["codex_5h_used_percent"] = 100 }, false},
		{"stale_exhaustion_without_reset", func(a *Account) {
			a.Extra["codex_7d_used_percent"] = 100
			a.Extra["codex_usage_updated_at"] = now.Add(-24 * time.Hour).Format(time.RFC3339)
		}, false},
		{"reset_has_passed", func(a *Account) {
			a.Extra["codex_5h_used_percent"] = 100
			a.Extra["codex_5h_reset_at"] = past.Format(time.RFC3339)
		}, true},
		{"relative_reset_pending", func(a *Account) {
			a.Extra["codex_5h_used_percent"] = 100
			a.Extra["codex_usage_updated_at"] = now.Format(time.RFC3339)
			a.Extra["codex_5h_reset_after_seconds"] = 3600
		}, false},
		{"relative_reset_passed", func(a *Account) {
			a.Extra["codex_5h_used_percent"] = 100
			a.Extra["codex_usage_updated_at"] = past.Format(time.RFC3339)
			a.Extra["codex_5h_reset_after_seconds"] = 10
		}, true},
		{"7d_still_exhausted_after_5h_reset", func(a *Account) {
			a.Extra["codex_5h_used_percent"] = 100
			a.Extra["codex_5h_reset_at"] = past.Format(time.RFC3339)
			a.Extra["codex_7d_used_percent"] = 100
			a.Extra["codex_7d_reset_at"] = future.Format(time.RFC3339)
		}, false},
		{"ignores_scheduling_threshold_opt_out", func(a *Account) {
			a.Extra["codex_5h_used_percent"] = 100
			a.Extra["auto_pause_5h_disabled"] = true
			a.Extra[IntelligenceAutoPauseEnabledExtraKey] = false
		}, false},
		{"canonical_overrides_legacy", func(a *Account) {
			a.Extra["codex_5h_used_percent"] = 20
			a.Extra["codex_7d_used_percent"] = 30
			a.Extra["codex_primary_used_percent"] = 100
			a.Extra["codex_secondary_used_percent"] = 100
		}, true},
		{"legacy_short_primary", func(a *Account) {
			a.Extra["codex_primary_used_percent"] = 100
			a.Extra["codex_primary_window_minutes"] = 300
			a.Extra["codex_primary_reset_at"] = future.Format(time.RFC3339)
		}, false},
		{"legacy_reset_passed", func(a *Account) {
			a.Extra["codex_secondary_used_percent"] = 100
			a.Extra["codex_secondary_reset_at"] = past.Format(time.RFC3339)
		}, true},
		{"identity_changed", func(a *Account) {
			a.Credentials["chatgpt_account_id"] = "new"
			a.Extra["chatgpt_account_id"] = "old"
			a.Extra["codex_5h_used_percent"] = 100
		}, true},
		{"api_key", func(a *Account) { a.Type = AccountTypeAPIKey }, false},
		{"shadow", func(a *Account) { id := int64(1); a.ParentAccountID = &id }, false},
		{"synthetic", func(a *Account) { a.Extra["synthetic_ui_test"] = true }, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			a := excelAccount()
			a.Schedulable = true
			tc.change(a)
			require.Equal(t, tc.want, AccountAutomaticProbeAvailable(a, now))
			require.Equal(t, tc.want, accountSVGProbeDue(a, now))
		})
	}
	require.False(t, AccountAutomaticProbeAvailable(nil, now))
}

func TestAccountAutomaticProbesResumeAtResetOrRefreshedQuota(t *testing.T) {
	now := time.Now().UTC()
	reset := now.Add(time.Minute)
	a := excelAccount()
	a.Schedulable = false
	a.Extra["codex_5h_used_percent"] = 100
	a.Extra["codex_5h_reset_at"] = reset.Format(time.RFC3339Nano)
	require.False(t, AccountAutomaticProbeAvailable(a, reset.Add(-time.Nanosecond)))
	require.True(t, AccountAutomaticProbeAvailable(a, reset))
	require.False(t, a.Schedulable)
	a.Extra["codex_5h_used_percent"] = 0
	require.True(t, AccountAutomaticProbeAvailable(a, now))
}

type unavailableProbeRepo struct {
	*intelligenceProbeRepo
	before *Account
	reads  int
	writes int
}

func (r *unavailableProbeRepo) GetByID(context.Context, int64) (*Account, error) {
	r.reads++
	if r.reads == 1 && r.before != nil {
		return r.before, nil
	}
	return r.account, nil
}
func (r *unavailableProbeRepo) UpdateExtra(context.Context, int64, map[string]any) error {
	r.writes++
	return nil
}

func TestUnavailableAccountSkipsBothProbesWithoutChangingResults(t *testing.T) {
	for _, kind := range []string{"quota", "429", "error", "cooldown"} {
		for _, automatic := range []bool{false, true} {
			for _, probe := range []string{"intelligence", "pelican"} {
				t.Run(kind+"/"+probe+map[bool]string{true: "/automatic", false: "/manual"}[automatic], func(t *testing.T) {
					before, a := excelAccount(), excelAccount()
					future := time.Now().Add(time.Hour)
					switch kind {
					case "quota":
						a.Extra["codex_7d_used_percent"] = 100
					case "429":
						a.RateLimitResetAt = &future
					case "error":
						a.Status = StatusError
					case "cooldown":
						a.TempUnschedulableUntil = &future
					}
					old := &OpenAICodexStateProbeSnapshot{Method: "candy", Route: "bps", Model: "gpt-6-astra", ConsecutiveDegraded: 1}
					a.Extra[OpenAICodexStateProbeSnapshotExtraKey] = old
					repo := &unavailableProbeRepo{intelligenceProbeRepo: &intelligenceProbeRepo{account: a}, before: before}
					svc := &AccountTestService{accountRepo: repo, openaiGatewayService: &OpenAIGatewayService{}}
					var err error
					if probe == "intelligence" {
						r, e := svc.probeOpenAIAccountIntelligence(context.Background(), a.ID, "gpt-6-astra", automatic)
						err = e
						require.Nil(t, r)
					} else {
						r, e := svc.probeAccountSVG(context.Background(), a.ID, automatic)
						err = e
						require.Nil(t, r)
					}
					if automatic {
						require.NoError(t, err)
					} else {
						require.ErrorContains(t, err, "测试")
					}
					require.Equal(t, 2, repo.reads, "availability must be rechecked after acquiring the probe guard")
					require.Zero(t, repo.writes)
					require.Equal(t, 1, old.ConsecutiveDegraded)
					require.False(t, svc.AccountSVGProbeRunning(a.ID))
					// Both test paths would fail (or panic) with this bare gateway if they sent upstream traffic.
				})
			}
		}
	}
}

func TestAutomaticProbeSelectionSkipsUnavailableKeepsPaused(t *testing.T) {
	now := time.Now()
	blocked, paused, healthy := excelAccount(), excelAccount(), excelAccount()
	blocked.ID = 1
	blocked.Extra["codex_5h_used_percent"] = 100
	paused.ID = 2
	paused.Schedulable = false
	healthy.ID = 3
	repo := &intelligenceMonitorRepo{accounts: []Account{*blocked, *paused, *healthy}}
	monitor := &UpstreamBillingProbeService{accountRepo: repo}
	for _, list := range []func(context.Context, time.Time) ([]Account, error){monitor.listDueOpenAICodexStateProbeAccounts, monitor.listDueAccountSVGProbeAccounts} {
		due, err := list(context.Background(), now)
		require.NoError(t, err)
		require.Len(t, due, 2)
		require.Equal(t, int64(2), due[0].ID)
		require.Equal(t, int64(3), due[1].ID)
	}
}
