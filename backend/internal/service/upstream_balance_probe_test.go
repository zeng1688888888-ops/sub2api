package service

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

const upstreamWalletTestBillingBody = `{"object":"sub2api.key_billing","schema_version":1,"billing_scope":"token","group_rate_multiplier":1.5,"resolved_rate_multiplier":1.5,"peak_rate_enabled":false,"effective_rate_multiplier":1.5,"observed_at":"2026-11-17T00:00:00Z"}`

func TestParseUpstreamWalletBalance(t *testing.T) {
	for _, tc := range []struct {
		name, body, reason string
		amount             float64
		currency           string
	}{
		{name: "wallet", body: `{"mode":"unrestricted","isValid":true,"balance":8465.83,"unit":"USD"}`, amount: 8465.83, currency: "USD"},
		{name: "zero", body: `{"mode":"unrestricted","isValid":true,"balance":0,"unit":"USD"}`, currency: "USD"},
		{name: "negative", body: `{"mode":"unrestricted","isValid":true,"balance":-2.5,"unit":"USD"}`, amount: -2.5, currency: "USD"},
		{name: "currency", body: `{"mode":"unrestricted","isValid":true,"balance":12.34,"unit":"cny"}`, amount: 12.34, currency: "CNY"},
		{name: "key_quota", body: `{"mode":"quota_limited","isValid":true,"remaining":100,"balance":1000,"unit":"USD"}`, reason: "key_quota_only"},
		{name: "subscription", body: `{"mode":"unrestricted","isValid":true,"remaining":100,"balance":1000,"subscription":{},"unit":"USD"}`, reason: "subscription_only"},
		{name: "unlimited", body: `{"mode":"unrestricted","isValid":true,"remaining":-1,"unit":"USD"}`, reason: "wallet_not_exposed"},
		{name: "null", body: `{"mode":"unrestricted","isValid":true,"balance":null,"unit":"USD"}`, reason: "wallet_not_exposed"},
		{name: "string", body: `{"mode":"unrestricted","isValid":true,"balance":"NaN","unit":"USD"}`, reason: "invalid_balance"},
		{name: "overflow", body: `{"mode":"unrestricted","isValid":true,"balance":1e999,"unit":"USD"}`, reason: "invalid_balance"},
		{name: "missing_currency", body: `{"mode":"unrestricted","isValid":true,"balance":10}`, reason: "invalid_currency"},
		{name: "unknown_schema", body: `{"balance":10,"unit":"USD"}`, reason: "invalid_response"},
		{name: "invalid_key", body: `{"mode":"unrestricted","isValid":false,"balance":10,"unit":"USD"}`, reason: "invalid_response"},
		{name: "html", body: "<html>login</html>", reason: "invalid_response"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			amount, currency, reason := parseUpstreamWalletBalance([]byte(tc.body))
			require.Equal(t, tc.reason, reason)
			require.Equal(t, tc.amount, amount)
			require.Equal(t, tc.currency, currency)
		})
	}
}

func TestUpstreamBillingProbeIncludesWalletAndKeepsRateOnBalanceFailure(t *testing.T) {
	for _, tc := range []struct {
		name, body, status, reason string
		httpStatus                 int
		amount                     *float64
	}{
		{name: "success", body: `{"mode":"unrestricted","isValid":true,"balance":8465.83,"unit":"USD","private_email":"do-not-save"}`, httpStatus: 200, status: "ok", amount: f64p(8465.83)},
		{name: "zero", body: `{"mode":"unrestricted","isValid":true,"balance":0,"unit":"USD"}`, httpStatus: 200, status: "ok", amount: f64p(0)},
		{name: "unsupported", body: "not found", httpStatus: 404, status: "unsupported", reason: "unsupported"},
		{name: "limited_key", body: `{"mode":"quota_limited","isValid":true,"quota":{"remaining":100},"unit":"USD"}`, httpStatus: 200, status: "unsupported", reason: "key_quota_only"},
		{name: "unauthorized", body: "private error", httpStatus: 401, status: "failed", reason: "http_error"},
		{name: "redirect", body: "", httpStatus: 302, status: "failed", reason: "http_error"},
		{name: "oversize", body: strings.Repeat("x", upstreamBillingProbeMaxBodyBytes+1), httpStatus: 200, status: "failed", reason: "response_too_large"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			now := time.Date(2026, 11, 17, 1, 0, 0, 0, time.UTC)
			previous := &UpstreamBalanceProbeSnapshot{Status: "ok", Amount: f64p(88), Currency: "USD", ReceivedAt: probeTimePtr(now.Add(-time.Hour)), FreshUntil: probeTimePtr(now.Add(time.Hour))}
			account := &Account{ID: 8, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Status: StatusActive, Concurrency: 2, Credentials: map[string]any{"api_key": "sk-balance", "base_url": "https://upstream.example/prefix/v1"}, Extra: map[string]any{UpstreamBillingRateSyncEnabledExtraKey: true, UpstreamBillingProbeEnabledExtraKey: true, UpstreamBillingProbeExtraKey: &UpstreamBillingProbeSnapshot{Status: "ok", Balance: previous}}}
			repo := &upstreamBillingProbeAccountRepo{accounts: map[int64]*Account{account.ID: account}}
			firstBody := &passthroughCloseTrackingReadCloser{Reader: strings.NewReader(upstreamWalletTestBillingBody)}
			balanceBody := &passthroughCloseTrackingReadCloser{Reader: strings.NewReader(tc.body)}
			upstream := &httpUpstreamRecorder{responses: []*http.Response{{StatusCode: 200, Header: http.Header{}, Body: firstBody}, {StatusCode: tc.httpStatus, Header: http.Header{}, Body: balanceBody}}}
			svc := newUpstreamBillingProbeTestService(repo, upstream, &upstreamBillingProbeSettingRepo{})
			svc.now = func() time.Time { return now }
			snapshot, err := svc.ProbeAccount(context.Background(), account.ID)
			require.NoError(t, err)
			require.Equal(t, "ok", snapshot.Status)
			require.Equal(t, 1.5, snapshot.Data["effective_rate_multiplier"])
			require.NotNil(t, snapshot.SyncedRateMultiplier)
			require.Equal(t, 1.5, *snapshot.SyncedRateMultiplier)
			require.NotNil(t, snapshot.Balance)
			require.Equal(t, tc.status, snapshot.Balance.Status)
			require.Equal(t, tc.reason, snapshot.Balance.LastError)
			require.Len(t, upstream.requests, 2)
			request := upstream.requests[1]
			require.Equal(t, "https://upstream.example/prefix/v1/usage?days=1", request.URL.String())
			require.Equal(t, http.MethodGet, request.Method)
			require.Equal(t, "Bearer sk-balance", request.Header.Get("Authorization"))
			require.True(t, HTTPUpstreamRedirectsDisabled(request.Context()))
			require.Equal(t, HTTPUpstreamProfileOpenAI, HTTPUpstreamProfileFromContext(request.Context()))
			deadline, ok := request.Context().Deadline()
			require.True(t, ok)
			require.LessOrEqual(t, time.Until(deadline), 5*time.Second)
			require.True(t, firstBody.closed)
			require.True(t, balanceBody.closed)
			if tc.amount != nil {
				require.Equal(t, *tc.amount, *snapshot.Balance.Amount)
				require.Equal(t, now, *snapshot.Balance.ReceivedAt)
			} else if tc.status == "failed" {
				require.Equal(t, float64(88), *snapshot.Balance.Amount)
				require.Equal(t, *previous.ReceivedAt, *snapshot.Balance.ReceivedAt)
			} else {
				require.Nil(t, snapshot.Balance.Amount)
			}
			stored := decodeUpstreamBillingProbeSnapshot(account.Extra)
			require.Equal(t, snapshot.Balance.Status, stored.Balance.Status)
			encoded, err := json.Marshal(stored)
			require.NoError(t, err)
			require.NotContains(t, string(encoded), "do-not-save")
			require.NotContains(t, string(encoded), "sk-balance")
			require.NotContains(t, string(encoded), "private error")
		})
	}
}

func TestUpstreamBalanceBecomesHistoricalWhenBillingRefreshFails(t *testing.T) {
	now := time.Now().UTC()
	oldTime := now.Add(-time.Hour)
	account := &Account{ID: 3, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Credentials: map[string]any{"api_key": "sk", "base_url": "https://upstream.example"}, Extra: map[string]any{UpstreamBillingProbeExtraKey: &UpstreamBillingProbeSnapshot{Status: "ok", Balance: &UpstreamBalanceProbeSnapshot{Status: "ok", Amount: f64p(19.5), Currency: "USD", ReceivedAt: &oldTime, FreshUntil: probeTimePtr(now.Add(time.Hour))}}}}
	repo := &upstreamBillingProbeAccountRepo{accounts: map[int64]*Account{account.ID: account}}
	upstream := &httpUpstreamRecorder{resp: &http.Response{StatusCode: 503, Header: http.Header{}, Body: io.NopCloser(strings.NewReader("unavailable"))}}
	svc := newUpstreamBillingProbeTestService(repo, upstream, &upstreamBillingProbeSettingRepo{})
	snapshot, err := svc.ProbeAccount(context.Background(), account.ID)
	require.NoError(t, err)
	require.Equal(t, "failed", snapshot.Balance.Status)
	require.Equal(t, "billing_probe_failed", snapshot.Balance.LastError)
	require.Equal(t, 19.5, *snapshot.Balance.Amount)
	require.Equal(t, oldTime, *snapshot.Balance.ReceivedAt)
	require.Len(t, upstream.requests, 1)
}

func TestUpstreamBalanceHonorsCancelledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	account := &Account{ID: 1, Platform: PlatformOpenAI, Type: AccountTypeAPIKey}
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, "https://upstream.example/v1/sub2api/billing", nil)
	upstream := &httpUpstreamRecorder{err: context.Canceled}
	svc := newUpstreamBillingProbeTestService(&upstreamBillingProbeAccountRepo{}, upstream, &upstreamBillingProbeSettingRepo{})
	snapshot := svc.probeUpstreamWalletBalance(ctx, account, req, "https://upstream.example", "", nil, 30)
	require.Equal(t, "failed", snapshot.Status)
	require.Nil(t, snapshot.Amount)
	require.ErrorIs(t, upstream.lastReq.Context().Err(), context.Canceled)
}
