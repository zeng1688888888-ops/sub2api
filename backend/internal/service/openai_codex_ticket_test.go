package service

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func fakeCodexTicketState(n int) string {
	if n < len(openAICodexTicketStatePrefix) {
		return strings.Repeat("A", n)
	}
	return openAICodexTicketStatePrefix + strings.Repeat("B", n-len(openAICodexTicketStatePrefix))
}

func ticketTestAccount(id int64) *Account {
	return &Account{
		ID:          id,
		Platform:    PlatformOpenAI,
		Type:        AccountTypeOAuth,
		Credentials: map[string]any{"access_token": "tok", "chatgpt_account_id": "acc-1"},
	}
}

func ticketTestPlanToken(plan, accountID string) string {
	header := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"none"}`))
	payload, _ := json.Marshal(map[string]any{
		"https://api.openai.com/auth": map[string]string{
			"chatgpt_plan_type":  plan,
			"chatgpt_account_id": accountID,
		},
	})
	return header + "." + base64.RawURLEncoding.EncodeToString(payload) + ".sig"
}

func ticketTestService(t *testing.T, cfg config.OpenAICodexTicketConfig, upstream HTTPUpstream) *OpenAIGatewayService {
	t.Helper()
	return &OpenAIGatewayService{
		cfg: &config.Config{
			Gateway: config.GatewayConfig{OpenAICodexTicket: cfg},
		},
		httpUpstream: upstream,
	}
}

func TestApplyOpenAICodexTicket_ReplacesHeader(t *testing.T) {
	state := fakeCodexTicketState(292)
	svc := ticketTestService(t, config.OpenAICodexTicketConfig{
		Enabled:      true,
		TargetLength: 292,
		TTLSeconds:   3600,
		FailClosed:   true,
	}, nil)
	account := ticketTestAccount(41)
	svc.storeOpenAICodexTicket(context.Background(), account, &openAICodexTicket{
		AccountID:  41,
		Model:      "gpt-6-astra",
		State:      state,
		Length:     292,
		CapturedAt: time.Now(),
		ExpiresAt:  time.Now().Add(time.Hour),
	})

	h := http.Header{}
	h.Set(openAICodexTurnStateHeader, fakeCodexTicketState(312))
	err := svc.applyOpenAICodexTicket(context.Background(), account, "gpt-6-astra", h)
	require.NoError(t, err)
	require.Equal(t, state, h.Get(openAICodexTurnStateHeader))
	require.Equal(t, 292, len(h.Get(openAICodexTurnStateHeader)))
}

func TestApplyOpenAICodexTicket_DoesNotReuseOtherModelOrAccount(t *testing.T) {
	svc := ticketTestService(t, config.OpenAICodexTicketConfig{
		Enabled:         true,
		TargetLength:    292,
		TTLSeconds:      3600,
		FailClosed:      true,
		HarvestProxyURL: "socks5h://harvest",
	}, &httpUpstreamRecorder{err: io.EOF})
	a := ticketTestAccount(41)
	b := ticketTestAccount(42)
	astra := fakeCodexTicketState(292)
	svc.storeOpenAICodexTicket(context.Background(), a, &openAICodexTicket{
		AccountID:  41,
		Model:      "gpt-6-astra",
		State:      astra,
		Length:     292,
		CapturedAt: time.Now(),
		ExpiresAt:  time.Now().Add(time.Hour),
	})

	h := http.Header{}
	h.Set(openAICodexTurnStateHeader, "keep-ungated")
	err := svc.applyOpenAICodexTicket(context.Background(), a, "gpt-5.5", h)
	require.NoError(t, err)
	require.Equal(t, "keep-ungated", h.Get(openAICodexTurnStateHeader))
	require.False(t, svc.isOpenAIAccountRequestRuntimeBlocked(a, "gpt-5.5", false))
	require.False(t, svc.isOpenAIAccountRequestRuntimeBlocked(b, "gpt-6-astra", false))
	require.False(t, svc.isOpenAIAccountRequestRuntimeBlocked(a, "gpt-6-astra", false))

	h = http.Header{}
	err = svc.applyOpenAICodexTicket(context.Background(), b, "gpt-6-astra", h)
	require.NoError(t, err)
	require.Empty(t, h.Get(openAICodexTurnStateHeader))
}

func TestLookupOpenAICodexTicket_PrefersNewerExtra(t *testing.T) {
	svc := ticketTestService(t, config.OpenAICodexTicketConfig{Enabled: true, TargetLength: 292, TTLSeconds: 3600}, nil)
	account := ticketTestAccount(41)
	oldState := fakeCodexTicketState(292)
	newState := openAICodexTicketStatePrefix + strings.Repeat("C", 286)
	svc.storeOpenAICodexTicket(context.Background(), account, &openAICodexTicket{
		AccountID:  41,
		Model:      "gpt-6-astra",
		State:      oldState,
		Length:     292,
		CapturedAt: time.Now().Add(-30 * time.Minute),
		ExpiresAt:  time.Now().Add(-time.Minute),
	})
	account.Extra = map[string]any{openAICodexTicketExtraKey("gpt-6-astra"): &openAICodexTicket{
		Model:      "gpt-6-astra",
		State:      newState,
		Length:     292,
		CapturedAt: time.Now(),
		ExpiresAt:  time.Now().Add(time.Hour),
	},
	}
	got := svc.lookupOpenAICodexTicket(account, "gpt-6-astra")
	require.NotNil(t, got)
	require.Equal(t, newState, got.State)
	require.True(t, got.valid(time.Now(), 292))
}

func TestApplyOpenAICodexTicket_ExpiredNotInjected(t *testing.T) {
	svc := ticketTestService(t, config.OpenAICodexTicketConfig{
		Enabled:         true,
		TargetLength:    292,
		TTLSeconds:      3600,
		FailClosed:      true,
		HarvestProxyURL: "socks5h://harvest",
	}, &httpUpstreamRecorder{err: io.EOF})
	account := ticketTestAccount(41)
	svc.storeOpenAICodexTicket(context.Background(), account, &openAICodexTicket{
		AccountID:  41,
		Model:      "gpt-6-astra",
		State:      fakeCodexTicketState(292),
		Length:     292,
		CapturedAt: time.Now().Add(-2 * time.Hour),
		ExpiresAt:  time.Now().Add(-time.Minute),
	})
	h := http.Header{}
	err := svc.applyOpenAICodexTicket(context.Background(), account, "gpt-6-astra", h)
	require.NoError(t, err)
	require.Empty(t, h.Get(openAICodexTurnStateHeader))
}

func TestApplyOpenAICodexTicket_WrongLengthNotInjected(t *testing.T) {
	svc := ticketTestService(t, config.OpenAICodexTicketConfig{
		Enabled:      true,
		TargetLength: 292,
		TTLSeconds:   3600,
		FailClosed:   true,
	}, nil)
	account := ticketTestAccount(41)
	svc.storeOpenAICodexTicket(context.Background(), account, &openAICodexTicket{
		AccountID:  41,
		Model:      "gpt-6-astra",
		State:      fakeCodexTicketState(312),
		Length:     312,
		CapturedAt: time.Now(),
		ExpiresAt:  time.Now().Add(time.Hour),
	})
	h := http.Header{}
	err := svc.applyOpenAICodexTicket(context.Background(), account, "gpt-6-astra", h)
	require.NoError(t, err)
	require.Empty(t, h.Get(openAICodexTurnStateHeader))
}

func TestApplyOpenAICodexTicket_FailOpenSkipsInject(t *testing.T) {
	svc := ticketTestService(t, config.OpenAICodexTicketConfig{
		Enabled:    true,
		FailClosed: false,
	}, nil)
	h := http.Header{}
	h.Set(openAICodexTurnStateHeader, "client-state")
	err := svc.applyOpenAICodexTicket(context.Background(), ticketTestAccount(41), "gpt-6-astra", h)
	require.NoError(t, err)
	require.Equal(t, "client-state", h.Get(openAICodexTurnStateHeader))
	require.False(t, svc.isOpenAIAccountRequestRuntimeBlocked(ticketTestAccount(41), "gpt-6-astra", false))
}

func TestOpenAICodexTicket_FailOpenPreservesSchedulingAndForwarding(t *testing.T) {
	gin.SetMode(gin.TestMode)
	now := time.Now()
	cases := []struct {
		name   string
		ticket *openAICodexTicket
	}{
		{name: "missing"},
		{name: "expired", ticket: &openAICodexTicket{State: fakeCodexTicketState(292), Length: 292, ExpiresAt: now.Add(-time.Minute)}},
		{name: "wrong_length", ticket: &openAICodexTicket{State: fakeCodexTicketState(312), Length: 312, ExpiresAt: now.Add(time.Hour)}},
		{name: "wrong_prefix", ticket: &openAICodexTicket{State: strings.Repeat("X", 292), Length: 292, ExpiresAt: now.Add(time.Hour)}},
		{name: "missing_expiry", ticket: &openAICodexTicket{State: fakeCodexTicketState(292), Length: 292}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			for _, model := range []string{openAICodexTicketDefaultModel, openAICodexTicketDefaultSolModel} {
				t.Run(model, func(t *testing.T) {
					upstream := &httpUpstreamRecorder{err: io.EOF}
					// Existing deployments may still set fail_closed=true. It must no longer
					// make optional tickets a prerequisite for normal account traffic.
					svc := ticketTestService(t, config.OpenAICodexTicketConfig{Enabled: true, FailClosed: true}, upstream)
					account := ticketTestAccount(41)
					if tc.ticket != nil {
						account.Extra = map[string]any{openAICodexTicketExtraKey(model): tc.ticket}
					}
					require.False(t, svc.isOpenAIAccountRequestRuntimeBlocked(account, model, false))
					require.False(t, svc.isOpenAIAccountRequestRuntimeBlocked(account, model, true))
					for _, status := range OpenAICodexTicketStatuses(account, svc.openAICodexTicketConfig(), now) {
						require.False(t, status.Ready)
						require.False(t, status.Blocked)
					}
					for _, transport := range []string{"http", "passthrough", "websocket"} {
						t.Run(transport, func(t *testing.T) {
							body := []byte(`{"model":` + jsonString(model) + `,"stream":true}`)
							c, _ := gin.CreateTestContext(httptest.NewRecorder())
							c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
							c.Request.Header.Set(openAICodexTurnStateHeader, "client-state")
							c.Request.Header.Set("x-codex-beta-features", "client-feature")
							var headers http.Header
							var err error
							if transport == "websocket" {
								headers, _, err = svc.buildOpenAIWSHeaders(context.Background(), c, account, "test-token",
									OpenAIWSProtocolDecision{Transport: OpenAIUpstreamTransportResponsesWebsocketV2},
									true, "client-state", "", "", model, "")
							} else {
								var req *http.Request
								if transport == "passthrough" {
									req, err = svc.buildUpstreamRequestOpenAIPassthrough(context.Background(), c, account, body, "test-token")
								} else {
									req, err = svc.buildUpstreamRequest(context.Background(), c, account, body, "test-token", true, "", true)
								}
								if req != nil {
									headers = req.Header
								}
							}
							require.NoError(t, err)
							require.Equal(t, "client-state", headers.Get(openAICodexTurnStateHeader))
							require.Equal(t, "client-feature", headers.Get("x-codex-beta-features"))
							require.Equal(t, "Bearer test-token", headers.Get("Authorization"))
						})
					}
					require.Empty(t, upstream.requests, "ordinary forwarding must not wait for a harvest probe")
				})
			}
		})
	}
}

func TestOpenAICodexTicket_FailOpenAfterFailedProbe(t *testing.T) {
	cases := []struct {
		name   string
		err    error
		status int
		state  string
	}{
		{name: "proxy_failure", err: io.EOF},
		{name: "timeout", err: context.DeadlineExceeded},
		{name: "bad_request", status: http.StatusBadRequest},
		{name: "unauthorized", status: http.StatusUnauthorized},
		{name: "rate_limited", status: http.StatusTooManyRequests},
		{name: "unavailable", status: http.StatusServiceUnavailable},
		{name: "invalid_ticket", status: http.StatusOK, state: fakeCodexTicketState(312)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			headers := http.Header{}
			headers.Set(openAICodexTurnStateHeader, tc.state)
			upstream := &httpUpstreamRecorder{err: tc.err, resp: &http.Response{
				StatusCode: tc.status, Header: headers,
				Body: io.NopCloser(strings.NewReader("data: {\"type\":\"response.completed\"}\n\n")),
			}}
			svc := ticketTestService(t, config.OpenAICodexTicketConfig{
				Enabled: true, FailClosed: true, HarvestProxyURL: "http://proxy.example.com:8080",
			}, upstream)
			account := ticketTestAccount(41)
			account.Status = StatusActive
			account.Schedulable = true
			account.Extra = map[string]any{"existing": true}
			repo := &codexTicketRefreshRepo{}
			svc.accountRepo = repo
			svc.probeOnceOpenAICodexTicket(context.Background(), account, openAICodexTicketDefaultModel)
			require.Len(t, upstream.requests, 1)
			require.Empty(t, repo.updates)
			require.Equal(t, StatusActive, account.Status)
			require.True(t, account.Schedulable)
			require.Equal(t, map[string]any{"existing": true}, account.Extra)
			require.Nil(t, svc.lookupOpenAICodexTicket(account, openAICodexTicketDefaultModel))
			require.False(t, svc.isOpenAIAccountRequestRuntimeBlocked(account, openAICodexTicketDefaultModel, false))
			outboundHeaders := http.Header{}
			outboundHeaders.Set(openAICodexTurnStateHeader, "client-state")
			require.NoError(t, svc.applyOpenAICodexTicket(context.Background(), account, openAICodexTicketDefaultModel, outboundHeaders))
			require.Equal(t, "client-state", outboundHeaders.Get(openAICodexTurnStateHeader))
			require.Len(t, upstream.requests, 1, "a failed harvest must not trigger a probe on the request path")
		})
	}
}

func TestApplyOpenAICodexTicket_DisabledNoop(t *testing.T) {
	svc := ticketTestService(t, config.OpenAICodexTicketConfig{Enabled: false, FailClosed: true}, nil)
	h := http.Header{}
	h.Set(openAICodexTurnStateHeader, "client-state")
	err := svc.applyOpenAICodexTicket(context.Background(), ticketTestAccount(41), "gpt-6-astra", h)
	require.NoError(t, err)
	require.Equal(t, "client-state", h.Get(openAICodexTurnStateHeader))
}

func TestHarvestOpenAICodexTicket_StopsAt292AndUsesHarvestProxy(t *testing.T) {
	state312 := fakeCodexTicketState(312)
	state292 := fakeCodexTicketState(292)
	header312 := http.Header{}
	header312.Set(openAICodexTurnStateHeader, state312)
	header292 := http.Header{}
	header292.Set(openAICodexTurnStateHeader, state292)
	upstream := &httpUpstreamRecorder{
		responses: []*http.Response{
			{
				StatusCode: http.StatusOK,
				Header:     header312,
				Body:       io.NopCloser(strings.NewReader("data: {\"type\":\"response.completed\"}\n\n")),
			},
			{
				StatusCode: http.StatusOK,
				Header:     header292,
				Body:       io.NopCloser(strings.NewReader("data: {\"type\":\"response.completed\"}\n\n")),
			},
		},
	}
	svc := ticketTestService(t, config.OpenAICodexTicketConfig{
		Enabled:                      true,
		TargetLength:                 292,
		TTLSeconds:                   3600,
		HarvestProxyURL:              "socks5h://user:pass@harvest.example:31",
		HarvestAttemptTimeoutSeconds: 5,
		FailClosed:                   true,
	}, upstream)
	account := ticketTestAccount(41)

	svc.probeOnceOpenAICodexTicket(context.Background(), account, "gpt-6-astra")
	require.Nil(t, svc.lookupOpenAICodexTicket(account, "gpt-6-astra"))
	svc.probeOnceOpenAICodexTicket(context.Background(), account, "gpt-6-astra")
	ticket := svc.lookupOpenAICodexTicket(account, "gpt-6-astra")
	require.NotNil(t, ticket)
	require.Equal(t, state292, ticket.State)
	h := http.Header{}
	h.Set(openAICodexTurnStateHeader, "stale")
	require.NoError(t, svc.applyOpenAICodexTicket(context.Background(), account, "gpt-6-astra", h))
	require.Equal(t, state292, h.Get(openAICodexTurnStateHeader))
	require.Equal(t, "socks5h://user:pass@harvest.example:31", upstream.lastProxyURL)
	require.Len(t, upstream.requests, 2)
	require.Empty(t, upstream.requests[0].Header.Get(openAICodexTurnStateHeader))
	require.Equal(t, openAICodexAstraMinVersion, upstream.requests[0].Header.Get("version"))
	require.Equal(t, HTTPUpstreamProfileOpenAIHarvest, HTTPUpstreamProfileFromContext(upstream.requests[0].Context()))
	require.True(t, upstream.requests[0].Close)
	var probeBody struct {
		ParallelToolCalls bool     `json:"parallel_tool_calls"`
		Include           []string `json:"include"`
	}
	require.NoError(t, json.NewDecoder(upstream.requests[0].Body).Decode(&probeBody))
	require.True(t, probeBody.ParallelToolCalls)
	require.Equal(t, []string{"reasoning.encrypted_content"}, probeBody.Include)
}

func TestHarvestOpenAICodexTicket_HTTP503DoesNotAbortHunt(t *testing.T) {
	state292 := fakeCodexTicketState(292)
	header503 := http.Header{}
	header292 := http.Header{}
	header292.Set(openAICodexTurnStateHeader, state292)
	responses := make([]*http.Response, 0, 3)
	responses = append(responses, &http.Response{
		StatusCode: http.StatusServiceUnavailable,
		Header:     header503,
		Body:       io.NopCloser(strings.NewReader(`{"error":"overloaded"}`)),
	})
	responses = append(responses, &http.Response{
		StatusCode: http.StatusOK,
		Header:     header292,
		Body:       io.NopCloser(strings.NewReader("data: {\"type\":\"response.completed\"}\n\n")),
	})
	upstream := &httpUpstreamRecorder{responses: responses}
	svc := ticketTestService(t, config.OpenAICodexTicketConfig{
		Enabled:                      true,
		TargetLength:                 292,
		TTLSeconds:                   3600,
		HarvestProxyURL:              "socks5h://harvest.example:31",
		HarvestAttemptTimeoutSeconds: 5,
		FailClosed:                   true,
	}, upstream)
	account := ticketTestAccount(41)
	svc.probeOnceOpenAICodexTicket(context.Background(), account, "gpt-6-astra")
	require.Nil(t, svc.lookupOpenAICodexTicket(account, "gpt-6-astra"))
	svc.probeOnceOpenAICodexTicket(context.Background(), account, "gpt-6-astra")
	ticket := svc.lookupOpenAICodexTicket(account, "gpt-6-astra")
	require.NotNil(t, ticket)
	require.Equal(t, state292, ticket.State)
	require.Len(t, upstream.requests, 2)
}

func TestLookupOpenAICodexTicket_HydratesFromExtra(t *testing.T) {
	svc := ticketTestService(t, config.OpenAICodexTicketConfig{Enabled: true, TargetLength: 292, TTLSeconds: 3600}, nil)
	state := fakeCodexTicketState(292)
	account := ticketTestAccount(9)
	account.Extra = map[string]any{
		openAICodexTicketExtraKey("gpt-6-astra"): map[string]any{
			"state":       state,
			"length":      292,
			"model":       "gpt-6-astra",
			"captured_at": time.Now().Add(-time.Minute),
			"expires_at":  time.Now().Add(time.Hour),
		},
	}
	got := svc.lookupOpenAICodexTicket(account, "gpt-6-astra")
	require.NotNil(t, got)
	require.Equal(t, state, got.State)
	require.True(t, got.valid(time.Now(), 292))
}

func TestOpenAICodexTicketStatuses_ReportsRemainingTTL(t *testing.T) {
	account := ticketTestAccount(41)
	account.Extra = map[string]any{
		openAICodexTicketExtraKey("gpt-6-astra"): map[string]any{
			"state":       fakeCodexTicketState(292),
			"length":      292,
			"model":       "gpt-6-astra",
			"captured_at": time.Now().Add(-10 * time.Minute),
			"expires_at":  time.Now().Add(50 * time.Minute),
		},
	}
	now := time.Now()
	got := OpenAICodexTicketStatuses(account, config.OpenAICodexTicketConfig{Enabled: true, FailClosed: true}, now)
	require.Len(t, got, 2)
	require.Equal(t, "gpt-6-astra", got[0].Model)
	require.True(t, got[0].Ready)
	require.Greater(t, got[0].RemainingSeconds, int64(40*60))
	require.LessOrEqual(t, got[0].RemainingSeconds, int64(50*60))
	require.Equal(t, "gpt-5.6-sol", got[1].Model)
	require.False(t, got[1].Ready)
}

func TestExtractOpenAICodexTicketModel(t *testing.T) {
	require.Equal(t, "gpt-6-astra", extractOpenAICodexTicketModel([]byte(`{"model":"gpt-6-astra"}`)))
	require.Empty(t, extractOpenAICodexTicketModel([]byte(`{}`)))
}

// These stubs exercise the real continuous refresh path with both default models
// completing together. Run under -race to catch writes to the shared account maps.
type codexTicketRefreshRepo struct {
	AccountRepository
	accounts []Account
	mu       sync.Mutex
	updates  map[string]any
}

func (r *codexTicketRefreshRepo) ListByPlatform(context.Context, string) ([]Account, error) {
	return r.accounts, nil
}
func (r *codexTicketRefreshRepo) UpdateExtra(_ context.Context, _ int64, updates map[string]any) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.updates == nil {
		r.updates = make(map[string]any)
	}
	for k, v := range updates {
		r.updates[k] = v
	}
	return nil
}

type codexTicketConcurrentUpstream struct {
	HTTPUpstream
	started atomic.Int64
	ready   chan struct{}
}

func (u *codexTicketConcurrentUpstream) Do(req *http.Request, _ string, _ int64, _ int) (*http.Response, error) {
	if u.started.Add(1) == 2 {
		close(u.ready)
	}
	select {
	case <-u.ready:
	case <-req.Context().Done():
		return nil, req.Context().Err()
	}
	h := http.Header{}
	h.Set(openAICodexTurnStateHeader, fakeCodexTicketState(292))
	return &http.Response{StatusCode: 200, Header: h, Body: io.NopCloser(strings.NewReader("data: {\"type\":\"response.completed\"}\n\n"))}, nil
}
func TestRefreshOpenAICodexTickets_ConcurrentModelsPreserveAccountSnapshot(t *testing.T) {
	account := ticketTestAccount(41)
	account.Status = StatusActive
	account.Extra = map[string]any{"existing": true}
	repo := &codexTicketRefreshRepo{accounts: []Account{*account}}
	upstream := &codexTicketConcurrentUpstream{ready: make(chan struct{})}
	svc := ticketTestService(t, config.OpenAICodexTicketConfig{Enabled: true, HarvestProxyURL: "socks5h://proxy.example.com:1080"}, upstream)
	svc.accountRepo = repo
	svc.refreshOpenAICodexTickets(context.Background())
	require.Equal(t, int64(2), upstream.started.Load())
	require.Equal(t, map[string]any{"existing": true}, account.Extra)
	require.Len(t, repo.updates, 2)
	for _, model := range []string{openAICodexTicketDefaultModel, openAICodexTicketDefaultSolModel} {
		ticket := svc.lookupOpenAICodexTicket(account, model)
		require.NotNil(t, ticket)
		require.True(t, ticket.valid(time.Now(), 292))
	}
	// Valid tickets do not produce another probe on the next cycle.
	svc.refreshOpenAICodexTickets(context.Background())
	require.Equal(t, int64(2), upstream.started.Load())
}
func TestOpenAICodexTicketStatuses_RespectRuntimeConfiguration(t *testing.T) {
	account := ticketTestAccount(41)
	require.Empty(t, OpenAICodexTicketStatuses(account, config.OpenAICodexTicketConfig{}, time.Now()))
	cfg := config.OpenAICodexTicketConfig{Enabled: true, Models: []string{"custom-model"}}
	status := OpenAICodexTicketStatuses(account, cfg, time.Now())
	require.Len(t, status, 1)
	require.Equal(t, "custom-model", status[0].Model)
	require.False(t, status[0].Blocked)
	cfg.FailClosed = true
	require.False(t, OpenAICodexTicketStatuses(account, cfg, time.Now())[0].Blocked)
}
func TestProbeOpenAICodexTicket_RejectsInvalidState(t *testing.T) {
	for _, state := range []string{fakeCodexTicketState(312), strings.Repeat("X", 292), ""} {
		h := http.Header{}
		h.Set(openAICodexTurnStateHeader, state)
		upstream := &httpUpstreamRecorder{responses: []*http.Response{{StatusCode: 200, Header: h, Body: io.NopCloser(strings.NewReader("data: {\"type\":\"response.completed\"}\n\n"))}}}
		svc := ticketTestService(t, config.OpenAICodexTicketConfig{Enabled: true, HarvestProxyURL: "http://proxy.example.com:8080"}, upstream)
		account := ticketTestAccount(41)
		svc.probeOnceOpenAICodexTicket(context.Background(), account, "gpt-6-astra")
		require.Nil(t, svc.lookupOpenAICodexTicket(account, "gpt-6-astra"))
	}
}

func TestOpenAICodexTicketProbeCompleted(t *testing.T) {
	require.True(t, openAICodexTicketProbeCompleted([]byte("data: {\"type\":\"response.completed\"}\n\n")))
	require.True(t, openAICodexTicketProbeCompleted([]byte("event: response.completed\ndata: {}\n\n")))
	require.True(t, openAICodexTicketProbeCompleted([]byte("event: response.completed\r\ndata: {\"type\":\"response.completed\",\r\ndata: \"response\":{\"status\":\"completed\"}}\r\n\r\n")))
	require.False(t, openAICodexTicketProbeCompleted([]byte("data: {\"type\":\"response.output_text.delta\"}\n\n")))
	require.False(t, openAICodexTicketProbeCompleted([]byte("data: {}\n\n")))
	for _, stream := range []string{
		"data: {\"type\":\"response.completed\"\n\n",
		"data: {\"type\":\"response.completed\"}\n",
		"event: response.completed\ndata: {\n\n",
		"event: response.completed\ndata: {\"type\":\"response.failed\"}\n\n",
		"event: response.failed\ndata: {\"type\":\"response.completed\"}\n\n",
		"data: {\"type\":\"response.completed\",\"response\":{\"status\":\"incomplete\"}}\n\n",
		"event: response.completed\ndata: [DONE]\n\n",
		"data: {\"type\":\"error\"}\n\n",
	} {
		require.False(t, openAICodexTicketProbeCompleted([]byte(stream)), "invalid completion: %q", stream)
	}
}

func TestFireOpenAICodexTicketProbeRequiresCompletedStream(t *testing.T) {
	state := fakeCodexTicketState(292)
	h := http.Header{}
	h.Set(openAICodexTurnStateHeader, state)
	upstream := &httpUpstreamRecorder{responses: []*http.Response{{
		StatusCode: http.StatusOK,
		Header:     h,
		Body:       io.NopCloser(strings.NewReader("data: {}\n\n")),
	}}}
	svc := ticketTestService(t, config.OpenAICodexTicketConfig{}, upstream)
	_, status, err := svc.fireOpenAICodexTicketProbe(context.Background(), ticketTestAccount(41), "tok", "gpt-6-astra", "http://proxy.example.com:8080", time.Second)
	require.Equal(t, http.StatusOK, status)
	require.Error(t, err)
}
func TestOpenAICodexTicket_RequiresActualLengthAndExpiry(t *testing.T) {
	ticket := &openAICodexTicket{State: fakeCodexTicketState(312), Length: 292, ExpiresAt: time.Now().Add(time.Hour)}
	require.False(t, ticket.valid(time.Now(), 292))
	ticket.State = fakeCodexTicketState(292)
	ticket.ExpiresAt = time.Time{}
	require.False(t, ticket.valid(time.Now(), 292))
}

func TestOpenAICodexTicketTargetLength_FollowsPlan(t *testing.T) {
	for _, tc := range []struct {
		name string
		plan string
		want int
	}{
		{name: "free", plan: "free", want: openAICodexTicketPersonalLength},
		{name: "personal", plan: "plus", want: openAICodexTicketPersonalLength},
		{name: "pro", plan: "pro", want: openAICodexTicketPersonalLength},
		{name: "team", plan: "team", want: openAICodexTicketTeamLength},
		{name: "business", plan: "business", want: openAICodexTicketTeamLength},
		{name: "business_prolite", plan: "self_serve_business_prolite", want: openAICodexTicketTeamLength},
		{name: "unknown", plan: "", want: openAICodexTicketPersonalLength},
	} {
		t.Run(tc.name, func(t *testing.T) {
			account := ticketTestAccount(41)
			account.Credentials["access_token"] = ticketTestPlanToken(tc.plan, "acc-1")
			require.Equal(t, tc.want, openAICodexTicketTargetLength(account, config.OpenAICodexTicketConfig{}))
		})
	}
}

func TestOpenAICodexTicketTargetLength_ExplicitOverride(t *testing.T) {
	account := ticketTestAccount(41)
	account.Credentials["access_token"] = ticketTestPlanToken("team", "acc-1")
	require.Equal(t, 300, openAICodexTicketTargetLength(account, config.OpenAICodexTicketConfig{TargetLength: 300}))
}

func TestOpenAICodexTicketBusinessProliteAccountMismatchIgnoresPlan(t *testing.T) {
	account := ticketTestAccount(41)
	account.Credentials["access_token"] = ticketTestPlanToken("self_serve_business_prolite", "other-account")
	require.Empty(t, openAICodexTicketPlan(account))
	require.Equal(t, openAICodexTicketPersonalLength, openAICodexTicketTargetLength(account, config.OpenAICodexTicketConfig{}))
}

func TestOpenAICodexTicketBusinessProliteAccepts332Rejects292(t *testing.T) {
	for _, length := range []int{openAICodexTicketTeamLength, openAICodexTicketPersonalLength} {
		t.Run(fmt.Sprint(length), func(t *testing.T) {
			account := ticketTestAccount(41)
			account.Credentials["access_token"] = ticketTestPlanToken("self_serve_business_prolite", "acc-1")
			svc := ticketTestService(t, config.OpenAICodexTicketConfig{Enabled: true, FailClosed: true}, nil)
			state := fakeCodexTicketState(length)
			svc.storeOpenAICodexTicket(context.Background(), account, &openAICodexTicket{
				AccountID: account.ID, Model: openAICodexTicketDefaultModel, State: state,
				Length: length, CapturedAt: time.Now(), ExpiresAt: time.Now().Add(time.Hour),
			})
			h := http.Header{}
			err := svc.applyOpenAICodexTicket(context.Background(), account, openAICodexTicketDefaultModel, h)
			if length == openAICodexTicketTeamLength {
				require.NoError(t, err)
				require.Equal(t, state, h.Get(openAICodexTurnStateHeader))
			} else {
				require.NoError(t, err)
				require.Empty(t, h.Get(openAICodexTurnStateHeader))
			}
		})
	}
}

func TestOpenAICodexTicketTeam332IsInjectedAndValid(t *testing.T) {
	account := ticketTestAccount(41)
	account.Credentials["access_token"] = ticketTestPlanToken("team", "acc-1")
	svc := ticketTestService(t, config.OpenAICodexTicketConfig{Enabled: true, FailClosed: true}, nil)
	state := fakeCodexTicketState(openAICodexTicketTeamLength)
	svc.storeOpenAICodexTicket(context.Background(), account, &openAICodexTicket{
		AccountID: account.ID, Model: openAICodexTicketDefaultModel, State: state,
		Length: len(state), CapturedAt: time.Now(), ExpiresAt: time.Now().Add(time.Hour),
	})
	h := http.Header{}
	require.NoError(t, svc.applyOpenAICodexTicket(context.Background(), account, openAICodexTicketDefaultModel, h))
	require.Equal(t, state, h.Get(openAICodexTurnStateHeader))
	require.Equal(t, openAICodexTicketTeamLength, len(h.Get(openAICodexTurnStateHeader)))
}

func TestOpenAICodexTicketTeamRejectsPersonal292(t *testing.T) {
	account := ticketTestAccount(41)
	account.Credentials["access_token"] = ticketTestPlanToken("team", "acc-1")
	svc := ticketTestService(t, config.OpenAICodexTicketConfig{Enabled: true, FailClosed: true}, nil)
	state := fakeCodexTicketState(openAICodexTicketPersonalLength)
	svc.storeOpenAICodexTicket(context.Background(), account, &openAICodexTicket{
		AccountID: account.ID, Model: openAICodexTicketDefaultModel, State: state,
		Length: len(state), CapturedAt: time.Now(), ExpiresAt: time.Now().Add(time.Hour),
	})
	h := http.Header{}
	err := svc.applyOpenAICodexTicket(context.Background(), account, openAICodexTicketDefaultModel, h)
	require.NoError(t, err)
	require.Empty(t, h.Get(openAICodexTurnStateHeader))
}

func TestOpenAICodexTicketAPIKeyIsPassthrough(t *testing.T) {
	account := &Account{
		ID:          42,
		Platform:    PlatformOpenAI,
		Type:        AccountTypeAPIKey,
		Credentials: map[string]any{"api_key": "api-key"},
	}
	svc := ticketTestService(t, config.OpenAICodexTicketConfig{Enabled: true, FailClosed: true}, nil)
	h := http.Header{}
	h.Set(openAICodexTurnStateHeader, "client-state")
	require.NoError(t, svc.applyOpenAICodexTicket(context.Background(), account, openAICodexTicketDefaultModel, h))
	require.Equal(t, "client-state", h.Get(openAICodexTurnStateHeader))
	require.Empty(t, OpenAICodexTicketStatuses(account, svc.openAICodexTicketConfig(), time.Now()))
}

func TestHarvestOpenAICodexTicket332PersistsAndRestoresByPlan(t *testing.T) {
	for _, plan := range []string{"team", "business", "self_serve_business_prolite"} {
		t.Run(plan, func(t *testing.T) {
			account := ticketTestAccount(41)
			account.Status = StatusActive
			account.Credentials["access_token"] = ticketTestPlanToken(plan, "acc-1")
			state := fakeCodexTicketState(332)
			h := http.Header{}
			h.Set(openAICodexTurnStateHeader, state)
			upstream := &httpUpstreamRecorder{responses: []*http.Response{{
				StatusCode: http.StatusOK, Header: h,
				Body: io.NopCloser(strings.NewReader("data: {\"type\":\"response.completed\"}\n\n")),
			}}}
			cfg := config.OpenAICodexTicketConfig{
				Enabled: true, FailClosed: true, Models: []string{openAICodexTicketDefaultModel},
				HarvestProxyURL: "http://proxy.example.com:8080",
			}
			repo := &codexTicketRefreshRepo{accounts: []Account{*account}}
			svc := ticketTestService(t, cfg, upstream)
			svc.accountRepo = repo
			svc.probeOnceOpenAICodexTicket(context.Background(), account, openAICodexTicketDefaultModel)
			require.Len(t, repo.updates, 1)
			account.Extra = repo.updates
			// A fresh service must recover the persisted ticket without a new probe.
			restored := ticketTestService(t, cfg, upstream)
			restored.accountRepo = &codexTicketRefreshRepo{accounts: []Account{*account}}
			statuses := OpenAICodexTicketStatuses(account, cfg, time.Now())
			require.Len(t, statuses, 1)
			require.True(t, statuses[0].Ready)
			require.False(t, statuses[0].Blocked)
			require.False(t, restored.isOpenAIAccountRequestRuntimeBlocked(account, openAICodexTicketDefaultModel, false))
			out := http.Header{}
			require.NoError(t, restored.applyOpenAICodexTicket(context.Background(), account, openAICodexTicketDefaultModel, out))
			require.Equal(t, state, out.Get(openAICodexTurnStateHeader))
			restored.refreshOpenAICodexTickets(context.Background())
			require.Len(t, upstream.requests, 1)
		})
	}
}

// Missing tickets must not affect scheduling even when compact rewrites the model.
func TestOpenAICodexTicket_MissingTicketAllowsRegularAndCompactRequests(t *testing.T) {
	svc := &OpenAIGatewayService{cfg: &config.Config{Gateway: config.GatewayConfig{
		OpenAICompactModel: "gpt-5.5",
		OpenAICodexTicket: config.OpenAICodexTicketConfig{
			Enabled:      true,
			TargetLength: 292,
			TTLSeconds:   3600,
			FailClosed:   true,
			Models:       []string{"gpt-6-astra"},
		},
	}}}
	account := ticketTestAccount(41) // 无票

	require.False(t, svc.isOpenAIAccountRequestRuntimeBlocked(account, "gpt-6-astra", false))
	require.False(t, svc.isOpenAIAccountRequestRuntimeBlocked(account, "gpt-6-astra", true))
}
