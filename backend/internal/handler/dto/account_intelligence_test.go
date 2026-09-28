package dto

import (
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestAccountIntelligenceSnapshotFullAndLite(t *testing.T) {
	for _, route := range []string{"codex", "bps"} {
		t.Run(route, func(t *testing.T) {
			rate := 100.0
			account := &service.Account{ID: 7, Platform: service.PlatformOpenAI, Type: service.AccountTypeOAuth,
				Extra: map[string]any{
					"openai_excel_bps": route == "bps",
					service.OpenAICodexStateProbeSnapshotExtraKey: &service.OpenAICodexStateProbeSnapshot{
						Method: "candy", Route: route, Model: "gpt-6-astra", Verdict: service.OpenAICodexStateHealthy,
						HealthRate: &rate, SampleCount: 1,
					},
				},
			}
			full := AccountFromServiceShallow(account)
			require.NotNil(t, full.OpenAICodexStateProbe)
			require.Equal(t, route, full.OpenAICodexStateProbe.Route)
			lite := AccountListItemFromAccount(full)
			require.Equal(t, full.OpenAICodexStateProbe, lite.OpenAICodexStateProbe)
			account.Extra["openai_excel_bps"] = route != "bps"
			require.Nil(t, AccountFromServiceShallow(account).OpenAICodexStateProbe)
		})
	}
}
