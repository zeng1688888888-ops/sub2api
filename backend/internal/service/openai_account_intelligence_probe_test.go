package service

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

type intelligenceProbeRepo struct {
	AccountRepository
	account *Account
}

func (r *intelligenceProbeRepo) GetByID(context.Context, int64) (*Account, error) {
	return r.account, nil
}

func (r *intelligenceProbeRepo) UpdateExtra(_ context.Context, _ int64, extra map[string]any) error {
	mergeAccountExtra(r.account, extra)
	return nil
}

func intelligenceAnswerStream(answer string) string {
	return fmt.Sprintf("event: response.output_text.delta\ndata: {\"type\":\"response.output_text.delta\",\"delta\":%q}\n\nevent: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_iq\",\"status\":\"completed\",\"model\":\"gpt-6-astra\",\"output\":[{\"type\":\"message\",\"role\":\"assistant\",\"content\":[{\"type\":\"output_text\",\"text\":%q}]}],\"usage\":{\"input_tokens\":10,\"output_tokens\":2}}}\n\n", answer, answer)
}

func TestOpenAIIntelligenceUsesActualRouteAndAnswer(t *testing.T) {
	for _, route := range []string{"codex", "bps", "scoped-codex", "mapped-bps"} {
		for _, answer := range []string{"21", "29"} {
			t.Run(route+"/"+answer, func(t *testing.T) {
				account := excelAccount()
				model := "gpt-6-astra"
				wantRoute := "bps"
				switch route {
				case "codex":
					account.Extra["openai_excel_bps"] = false
					wantRoute = "codex"
				case "scoped-codex":
					account.Extra["openai_excel_bps_models"] = []string{"gpt-5.6-sol"}
					wantRoute = "codex"
				case "mapped-bps":
					model = "reasoning-alias"
					account.Credentials["model_mapping"] = map[string]any{"reasoning-alias": "gpt-6-astra"}
					account.Extra["openai_excel_bps_models"] = []string{"gpt-6-astra"}
				}
				upstream := &httpUpstreamRecorder{resp: &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": {"text/event-stream"}}, Body: io.NopCloser(strings.NewReader(intelligenceAnswerStream(answer)))}}
				repo := &intelligenceProbeRepo{account: account}
				svc := &AccountTestService{accountRepo: repo, httpUpstream: upstream, openaiGatewayService: openAIClientToolsTestService(upstream)}
				result, err := svc.ProbeOpenAIAccountIntelligence(context.Background(), account.ID, model)
				require.NoError(t, err)
				require.Equal(t, wantRoute, result.Route)
				require.Equal(t, "candy", result.Method)
				require.Equal(t, answer, result.Answer)
				require.NotNil(t, result.Snapshot)
				require.Equal(t, 1, result.Snapshot.SampleCount)
				wantVerdict := OpenAICodexStateHealthy
				if answer != "21" {
					wantVerdict = OpenAICodexStateDegraded
				}
				require.Equal(t, wantVerdict, result.Verdict)
				require.Equal(t, result.Verdict, OpenAICodexStateProbeSnapshotFromAccount(account).Verdict)
				require.Len(t, upstream.requests, 1, "one real question, not a ticket probe")
				require.Contains(t, string(upstream.lastBody), "糖果")
				if wantRoute == "bps" {
					require.Equal(t, "bps.openai.com", upstream.lastReq.URL.Host)
					require.Equal(t, "high", gjson.GetBytes(upstream.lastBody, "reasoning_effort").String())
					require.Empty(t, upstream.lastReq.Header.Get(openAICodexTurnStateHeader))
				} else {
					require.Equal(t, "chatgpt.com", upstream.lastReq.URL.Host)
					require.Equal(t, "high", gjson.GetBytes(upstream.lastBody, "reasoning.effort").String())
				}
			})
		}
	}
}

func TestOpenAIIntelligenceTransportFailureIsNotDegraded(t *testing.T) {
	for _, route := range []string{"codex", "bps"} {
		t.Run(route, func(t *testing.T) {
			account := excelAccount()
			account.Extra["openai_excel_bps"] = route == "bps"
			upstream := &httpUpstreamRecorder{resp: &http.Response{StatusCode: http.StatusServiceUnavailable, Header: http.Header{}, Body: io.NopCloser(strings.NewReader("unavailable"))}}
			repo := &intelligenceProbeRepo{account: account}
			svc := &AccountTestService{accountRepo: repo, httpUpstream: upstream, openaiGatewayService: openAIClientToolsTestService(upstream)}
			result, err := svc.ProbeOpenAIAccountIntelligence(context.Background(), account.ID, "")
			require.NoError(t, err)
			require.Equal(t, OpenAICodexStateInconclusive, result.Verdict)
			require.Zero(t, result.Snapshot.SampleCount)
			require.Nil(t, result.Snapshot.HealthRate)
		})
	}
}

func TestOpenAIIntelligenceSnapshotSeparatesRoutesAndIgnoresFailures(t *testing.T) {
	account := excelAccount()
	svc := &AccountTestService{accountRepo: &intelligenceProbeRepo{account: account}}
	result := &OpenAICodexStateProbeResult{Method: "candy", Route: "bps", Model: "gpt-6-astra", Verdict: OpenAICodexStateHealthy, FinishedAt: time.Now()}
	require.NoError(t, svc.persistOpenAICodexStateProbeResult(context.Background(), account, result))
	result.Verdict = OpenAICodexStateDegraded
	require.NoError(t, svc.persistOpenAICodexStateProbeResult(context.Background(), account, result))
	result.Verdict, result.Failure = OpenAICodexStateInconclusive, OpenAICodexStateFailureNetworkError
	require.NoError(t, svc.persistOpenAICodexStateProbeResult(context.Background(), account, result))
	require.Equal(t, 2, result.Snapshot.SampleCount)
	require.Equal(t, 50.0, *result.Snapshot.HealthRate)
	require.Zero(t, result.Snapshot.ConsecutiveDegraded)
	account.Extra["openai_excel_bps"] = false
	require.Nil(t, OpenAICodexStateProbeSnapshotFromAccount(account), "old route must not be presented as current")
	result.Route, result.Verdict, result.Failure = "codex", OpenAICodexStateHealthy, ""
	require.NoError(t, svc.persistOpenAICodexStateProbeResult(context.Background(), account, result))
	require.Equal(t, 1, result.Snapshot.SampleCount)
	require.Equal(t, 100.0, *result.Snapshot.HealthRate)
}

func TestOpenAIIntelligenceSharesAccountProbeLock(t *testing.T) {
	account := excelAccount()
	svc := &AccountTestService{accountRepo: &intelligenceProbeRepo{account: account}}
	release, ok := svc.beginOpenAICodexStateProbe(account.ID)
	require.True(t, ok)
	defer release()
	_, err := svc.ProbeOpenAIAccountIntelligence(context.Background(), account.ID, "")
	require.ErrorIs(t, err, ErrOpenAICodexStateProbeBusy)
}

func TestOpenAIIntelligenceReadsCompletedOnlyAnswers(t *testing.T) {
	for _, bps := range []bool{false, true} {
		t.Run(fmt.Sprint(bps), func(t *testing.T) {
			account := excelAccount()
			account.Extra["openai_excel_bps"] = bps
			stream := intelligenceAnswerStream("21")
			stream = stream[strings.Index(stream, "event: response.completed"):]
			upstream := &httpUpstreamRecorder{resp: &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": {"text/event-stream"}}, Body: io.NopCloser(strings.NewReader(stream))}}
			svc := &AccountTestService{accountRepo: &intelligenceProbeRepo{account: account}, openaiGatewayService: openAIClientToolsTestService(upstream)}
			result, err := svc.ProbeOpenAIAccountIntelligence(context.Background(), account.ID, "")
			require.NoError(t, err)
			require.Equal(t, OpenAICodexStateHealthy, result.Verdict, result.Detail)
			require.Equal(t, "21", result.Answer)
		})
	}
}

func TestOpenAIIntelligenceIncompleteAnswerIsNotGraded(t *testing.T) {
	account := excelAccount()
	stream := "event: response.output_text.delta\ndata: {\"type\":\"response.output_text.delta\",\"delta\":\"21\"}\n\n"
	upstream := &httpUpstreamRecorder{resp: &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": {"text/event-stream"}}, Body: io.NopCloser(strings.NewReader(stream))}}
	svc := &AccountTestService{accountRepo: &intelligenceProbeRepo{account: account}, openaiGatewayService: openAIClientToolsTestService(upstream)}
	result, err := svc.ProbeOpenAIAccountIntelligence(context.Background(), account.ID, "")
	require.NoError(t, err)
	require.Equal(t, OpenAICodexStateInconclusive, result.Verdict)
	require.Zero(t, result.Snapshot.SampleCount)
}
