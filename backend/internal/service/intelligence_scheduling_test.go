package service

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestIntelligenceAutoPauseCreateOptIn(t *testing.T) {
	for _, tc := range []struct {
		name    string
		extra   map[string]any
		want    bool
		invalid bool
	}{
		{name: "default"},
		{name: "disabled", extra: map[string]any{IntelligenceAutoPauseEnabledExtraKey: false}},
		{name: "enabled", extra: map[string]any{IntelligenceAutoPauseEnabledExtraKey: true}, want: true},
		{name: "invalid", extra: map[string]any{IntelligenceAutoPauseEnabledExtraKey: "true"}, invalid: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repo := &upstreamBillingProbeAccountRepo{}
			svc := &adminServiceImpl{accountRepo: repo}
			account, err := svc.CreateAccount(context.Background(), &CreateAccountInput{Name: "gpt", Platform: PlatformOpenAI, Type: AccountTypeOAuth, Credentials: map[string]any{"access_token": "test"}, Extra: tc.extra, SkipDefaultGroupBind: true})
			if tc.invalid {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			require.Equal(t, tc.want, account.IntelligenceAutoPauseEnabled())
			require.True(t, account.Schedulable)
		})
	}
}

func TestIntelligenceAutoPauseEditPreservesUnlessExplicit(t *testing.T) {
	a := excelAccount()
	a.ID = 7
	a.Extra["openai_excel_bps"] = false
	a.Schedulable = false
	a.Extra[OpenAICodexStateProbeSnapshotExtraKey] = &OpenAICodexStateProbeSnapshot{Method: "candy", Route: "codex", Model: "gpt-6-astra", ConsecutiveDegraded: 8, History: []OpenAICodexStateProbeVerdict{OpenAICodexStateProbeHistoryDegraded}}
	repo := &accountBillingSettingsAdminRepo{upstreamBillingProbeAccountRepo: &upstreamBillingProbeAccountRepo{accounts: map[int64]*Account{a.ID: a}}}
	svc := &adminServiceImpl{accountRepo: repo}
	updated, err := svc.UpdateAccount(context.Background(), a.ID, &UpdateAccountInput{Extra: map[string]any{IntelligenceAutoPauseEnabledExtraKey: true}})
	require.NoError(t, err)
	require.NotNil(t, repo.lastIntelligenceAutoPause)
	require.True(t, *repo.lastIntelligenceAutoPause)
	require.True(t, updated.IntelligenceAutoPauseEnabled())
	require.False(t, updated.Schedulable)
	require.NotNil(t, OpenAICodexStateProbeSnapshotFromAccount(updated))
	require.Zero(t, OpenAICodexStateProbeSnapshotFromAccount(updated).ConsecutiveDegraded)
	generation := updated.IntelligenceSchedulingGeneration()
	require.NotEmpty(t, generation)

	updated, err = svc.UpdateAccount(context.Background(), a.ID, &UpdateAccountInput{Name: "renamed", Extra: map[string]any{"custom": "kept"}})
	require.NoError(t, err)
	require.Nil(t, repo.lastIntelligenceAutoPause)
	require.True(t, updated.IntelligenceAutoPauseEnabled())
	require.Equal(t, generation, updated.IntelligenceSchedulingGeneration())

	updated, err = svc.UpdateAccount(context.Background(), a.ID, &UpdateAccountInput{Extra: map[string]any{IntelligenceAutoPauseEnabledExtraKey: false}})
	require.NoError(t, err)
	require.NotNil(t, repo.lastIntelligenceAutoPause)
	require.False(t, *repo.lastIntelligenceAutoPause)
	require.False(t, updated.IntelligenceAutoPauseEnabled())
	require.False(t, updated.Schedulable, "turning off protection must not reopen a paused account")
	require.NotEqual(t, generation, updated.IntelligenceSchedulingGeneration())
	_, err = svc.UpdateAccount(context.Background(), a.ID, &UpdateAccountInput{Extra: map[string]any{IntelligenceAutoPauseEnabledExtraKey: "true"}})
	require.Error(t, err)
}

func TestIntelligenceAutoPauseOnlySupportsGPTAccounts(t *testing.T) {
	for _, kind := range []string{"oauth", "setup-token", "apikey", "anthropic", "shadow", "synthetic", "agent_identity"} {
		t.Run(kind, func(t *testing.T) {
			a := excelAccount()
			switch kind {
			case "setup-token", "apikey":
				a.Type = kind
			case "anthropic":
				a.Platform = PlatformAnthropic
			case "shadow":
				id := int64(1)
				a.ParentAccountID = &id
			case "synthetic":
				a.Extra["synthetic_ui_test"] = true
			case "agent_identity":
				a.Credentials["auth_mode"] = OpenAIAuthModeAgentIdentity
			}
			enabled, err := requestedIntelligenceAutoPauseSetting(a, map[string]any{IntelligenceAutoPauseEnabledExtraKey: true})
			if kind == "oauth" || kind == "setup-token" {
				require.NoError(t, err)
				require.True(t, *enabled)
			} else {
				require.Error(t, err)
			}
		})
	}
}
