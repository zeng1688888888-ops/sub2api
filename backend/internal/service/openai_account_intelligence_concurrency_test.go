//go:build unit

package service

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestOpenAIIntelligenceSharesAccountConcurrencySlot(t *testing.T) {
	for _, tc := range []struct {
		name          string
		acquireResult bool
		wantRequests  int
		wantReleases  int
	}{
		{name: "slot available", acquireResult: true, wantRequests: 1, wantReleases: 1},
		{name: "account full", acquireResult: false, wantRequests: 0, wantReleases: 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			account := excelAccount()
			upstream := &httpUpstreamRecorder{resp: &http.Response{
				StatusCode: http.StatusOK,
				Header:     http.Header{"Content-Type": {"text/event-stream"}},
				Body:       io.NopCloser(strings.NewReader(intelligenceAnswerStream("21"))),
			}}
			slots := &stubConcurrencyCacheForTest{acquireResult: tc.acquireResult}
			gateway := openAIClientToolsTestService(upstream)
			gateway.concurrencyService = NewConcurrencyService(slots)
			svc := &AccountTestService{accountRepo: &intelligenceProbeRepo{account: account}, openaiGatewayService: gateway}

			result, err := svc.ProbeOpenAIAccountIntelligence(context.Background(), account.ID, "")
			require.NoError(t, err)
			require.Equal(t, tc.wantRequests, len(upstream.requests))
			require.Equal(t, tc.wantReleases, len(slots.releasedAccountIDs))
			if tc.acquireResult {
				require.Equal(t, OpenAICodexStateHealthy, result.Verdict)
			} else {
				require.Equal(t, OpenAICodexStateInconclusive, result.Verdict)
				require.Empty(t, result.Snapshot.SampleCount)
			}
		})
	}
}
