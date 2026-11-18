package service

import (
	"encoding/json"
	"os"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/apicompat"
	"github.com/Wei-Shaw/sub2api/internal/pkg/openai"
	"github.com/stretchr/testify/require"
)

func TestGPT62PreservesModelIdentityAndPredecessorCapabilities(t *testing.T) {
	for _, tc := range []struct{ model, previous string }{
		{"gpt-6.2-sol", "gpt-6.1-sol"}, {"gpt-6.2-astra", "gpt-6-astra"},
	} {
		t.Run(tc.model, func(t *testing.T) {
			require.Contains(t, openai.DefaultModelIDs(), tc.model)
			for _, spelling := range []string{tc.model, "openai/" + tc.model + "-max", tc.model + "-openai-compact"} {
				require.Equal(t, tc.model, normalizeCodexModel(spelling))
				require.Equal(t, tc.model, normalizeKnownOpenAICodexModel(spelling))
				require.Equal(t, tc.model, normalizeOpenAIModelForUpstream(&Account{Platform: PlatformOpenAI, Type: AccountTypeOAuth}, spelling))
				require.Equal(t, "max", normalizeOpenAIReasoningEffortForModel("max", spelling))
				require.True(t, shouldAutoInjectPromptCacheKeyForCompat(spelling))
			}
			require.Equal(t, openai.CodexBaseInstructionsForModel(tc.previous), openai.CodexBaseInstructionsForModel(tc.model))
			require.Equal(t, openai.RequiresResponsesForToolCalls(tc.previous), openai.RequiresResponsesForToolCalls(tc.model))
			decode := func(id string) map[string]any {
				body, err := BuildCodexModelsManifest([]string{id})
				require.NoError(t, err)
				var manifest struct {
					Models []map[string]any `json:"models"`
				}
				require.NoError(t, json.Unmarshal(body, &manifest))
				require.Len(t, manifest.Models, 1)
				return manifest.Models[0]
			}
			current, previous := decode(tc.model), decode(tc.previous)
			require.Equal(t, tc.model, current["slug"])
			require.Contains(t, current["display_name"], "6.2")
			delete(current, "slug")
			delete(previous, "slug")
			delete(current, "display_name")
			delete(previous, "display_name")
			require.Equal(t, previous, current, "only model identity should change in the inherited manifest")
			for _, accountType := range []string{AccountTypeAPIKey, AccountTypeOAuth} {
				base := "https://api.openai.com"
				if accountType == AccountTypeOAuth {
					base = "https://chatgpt.com"
				}
				account := &Account{Platform: PlatformOpenAI, Type: accountType, Credentials: map[string]any{"base_url": base}}
				require.Equal(t, accountCodexToolCapabilities(account, tc.previous), accountCodexToolCapabilities(account, tc.model))
				require.Equal(t, openAISubscriptionLongContextExempt(account, &OpenAIForwardResult{UpstreamModel: tc.previous}), openAISubscriptionLongContextExempt(account, &OpenAIForwardResult{UpstreamModel: tc.model}))
			}
		})
	}
	for _, model := range []string{"gpt-6.2", "gpt-6.2-solitude", "gpt-6.2-sol-preview", "gpt-6.2-astra-preview"} {
		require.False(t, isOpenAIGPT6Model(model), model)
	}
	require.Equal(t, "gpt-6-astra", normalizeCodexModel("gpt-6"))
}

func TestGPT62SolCompatPreservesMaxAndRejectsDisabledReasoning(t *testing.T) {
	req := &apicompat.AnthropicRequest{Model: "openai/gpt-6.2-sol-max"}
	applyOpenAICompatModelNormalization(req)
	require.Equal(t, "gpt-6.2-sol", req.Model)
	require.Equal(t, "max", req.OutputConfig.Effort)
	converted, err := apicompat.AnthropicToResponses(req)
	require.NoError(t, err)
	require.Equal(t, "gpt-6.2-sol", converted.Model)
	require.Equal(t, "max", converted.Reasoning.Effort)
	for _, effort := range []string{"none", "minimal"} {
		_, err := apicompat.ChatCompletionsToResponses(&apicompat.ChatCompletionsRequest{Model: "gpt-6.2-sol", ReasoningEffort: effort})
		require.ErrorContains(t, err, "gpt-6.2-sol")
		require.Error(t, validateGPT61RawEffort([]byte(`{"reasoning":{"effort":"`+effort+`"}}`), "gpt-6.2-sol"))
	}
	body, changed, err := normalizeGPT6ResponsesSampling([]byte(`{"model":"gpt-6.2-sol","reasoning":{"effort":"max"},"temperature":0.7,"top_p":0.9}`), "gpt-6.2-sol")
	require.NoError(t, err)
	require.True(t, changed)
	require.NotContains(t, string(body), "temperature")
	require.NotContains(t, string(body), "top_p")
	require.Contains(t, string(body), "gpt-6.2-sol")
}

func TestGPT62APIKeyCatalogUsesFullResponses(t *testing.T) {
	account := &Account{Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Credentials: map[string]any{"base_url": "https://api.openai.com"}}
	for _, model := range []string{"gpt-6.2-sol", "gpt-6.2-astra"} {
		body, err := completeAPIKeyCodexModelsManifestMetadata([]byte(`{"models":[{"slug":"`+model+`","use_responses_lite":true}]}`), true, account)
		require.NoError(t, err)
		body, err = adjustAPIKeyCodexModelsManifest(body, account)
		require.NoError(t, err)
		var manifest struct {
			Models []map[string]any `json:"models"`
		}
		require.NoError(t, json.Unmarshal(body, &manifest))
		require.Equal(t, model, manifest.Models[0]["slug"])
		require.Equal(t, false, manifest.Models[0]["use_responses_lite"])
		require.NotEmpty(t, manifest.Models[0]["supported_reasoning_levels"])
	}
}

func TestGPT62PricingCatalogAndFallbacks(t *testing.T) {
	catalog, err := os.ReadFile("../../resources/model-pricing/model_prices_and_context_window.json")
	require.NoError(t, err)
	services := map[string]*BillingService{
		"catalog":          NewBillingService(&config.Config{}, newStubPricingServiceFromJSON(t, string(catalog))),
		"stale_remote":     NewBillingService(&config.Config{}, newStubPricingServiceFromJSON(t, `{"gpt-5.4":{"input_cost_per_token":1,"output_cost_per_token":2},"gpt-6-astra":{"input_cost_per_token":0.00001,"output_cost_per_token":0.00005}}`)),
		"billing_fallback": NewBillingService(&config.Config{}, nil),
	}
	for name, svc := range services {
		for _, tc := range []struct {
			model                      string
			input, output, read, write float64
		}{
			{"gpt-6.2-sol", 2e-6, 10e-6, 0.1e-6, 2.5e-6},
			{"gpt-6.2-astra", 8e-6, 40e-6, 0.4e-6, 10e-6},
		} {
			t.Run(name+"/"+tc.model, func(t *testing.T) {
				for _, model := range []string{tc.model, "openai/" + tc.model + "-max", tc.model + "-openai-compact"} {
					p, err := svc.GetModelPricing(model)
					require.NoError(t, err)
					require.InDelta(t, tc.input, p.InputPricePerToken, 1e-12)
					require.InDelta(t, tc.output, p.OutputPricePerToken, 1e-12)
					require.InDelta(t, tc.read, p.CacheReadPricePerToken, 1e-12)
					require.InDelta(t, tc.write, p.CacheCreationPricePerToken, 1e-12)
					require.InDelta(t, 2*tc.input, p.InputPricePerTokenPriority, 1e-12)
					require.InDelta(t, 2*tc.read, p.CacheReadPricePerTokenPriority, 1e-12)
				}
				tiers := map[string]float64{"": 1, "priority": 2, "flex": 0.5}
				if tc.model == "gpt-6.2-astra" {
					tiers["ultrafast"] = 6
				}
				for tier, scale := range tiers {
					// Cached input contributes to the 272k threshold, including the exact boundary.
					for _, cacheRead := range []int{72000, 73000} {
						tokens := UsageTokens{InputTokens: 100000, CacheCreationTokens: 100000, CacheReadTokens: cacheRead, OutputTokens: 1000}
						cost, err := svc.CalculateCostWithServiceTier(tc.model, tokens, 1, tier)
						require.NoError(t, err)
						long := cacheRead == 73000
						require.Equal(t, long, cost.LongContextBillingApplied)
						inputScale, outputScale := scale, scale
						if long {
							inputScale *= 2
							outputScale *= 1.5
						}
						require.InDelta(t, 100000*tc.input*inputScale, cost.InputCost, 1e-10)
						require.InDelta(t, 1000*tc.output*outputScale, cost.OutputCost, 1e-10)
						require.InDelta(t, float64(cacheRead)*tc.read*inputScale, cost.CacheReadCost, 1e-10)
						require.InDelta(t, 100000*tc.write*inputScale, cost.CacheCreationCost, 1e-10)
					}
				}
			})
		}
	}
}
