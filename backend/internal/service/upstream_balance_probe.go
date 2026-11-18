package service

import (
	"context"
	"encoding/json"
	"io"
	"math"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/tlsfingerprint"
)

// UpstreamBalanceProbeSnapshot describes the upstream wallet only. Key quotas
// and subscription allowances are deliberately not interchangeable with money
// held in the upstream user's wallet. This is display data, not billing input.
type UpstreamBalanceProbeSnapshot struct {
	Status        string     `json:"status"`
	Amount        *float64   `json:"amount,omitempty"`
	Currency      string     `json:"currency,omitempty"`
	ReceivedAt    *time.Time `json:"received_at,omitempty"`
	FreshUntil    *time.Time `json:"fresh_until,omitempty"`
	LastAttemptAt time.Time  `json:"last_attempt_at"`
	LastError     string     `json:"last_error,omitempty"`
}

func upstreamBalanceProbeFailure(previous *UpstreamBalanceProbeSnapshot, now time.Time, status, reason string) *UpstreamBalanceProbeSnapshot {
	snapshot := &UpstreamBalanceProbeSnapshot{Status: status, LastAttemptAt: now, LastError: reason}
	// Keep historical amounts on transient errors, with their original timestamps.
	// A successful response that no longer exposes a wallet revokes the old value.
	if previous != nil && status == UpstreamBillingProbeStatusFailed {
		snapshot.Amount = previous.Amount
		snapshot.Currency = previous.Currency
		snapshot.ReceivedAt = previous.ReceivedAt
		snapshot.FreshUntil = previous.FreshUntil
	}
	return snapshot
}

func (s *UpstreamBillingProbeService) probeUpstreamWalletBalance(ctx context.Context, account *Account, billingRequest *http.Request, normalizedBaseURL, proxyURL string, tlsProfile *tlsfingerprint.Profile, intervalMinutes int) *UpstreamBalanceProbeSnapshot {
	now := s.currentTime().UTC()
	var previous *UpstreamBalanceProbeSnapshot
	if snapshot := decodeUpstreamBillingProbeSnapshot(account.Extra); snapshot != nil {
		previous = snapshot.Balance
	}
	failed := func(reason string) *UpstreamBalanceProbeSnapshot {
		return upstreamBalanceProbeFailure(previous, now, UpstreamBillingProbeStatusFailed, reason)
	}
	unsupported := func(reason string) *UpstreamBalanceProbeSnapshot {
		return upstreamBalanceProbeFailure(nil, now, UpstreamBillingProbeStatusUnsupported, reason)
	}
	// Share the billing probe's ten-second budget; cap the extra read at five seconds.
	// Clone only the validated, authenticated request, never a URL from upstream JSON.
	balanceCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	req := billingRequest.Clone(WithHTTPUpstreamRedirectsDisabled(WithHTTPUpstreamProfile(balanceCtx, HTTPUpstreamProfileFromContext(billingRequest.Context()))))
	usageURL, err := url.Parse(buildOpenAIEndpointURL(normalizedBaseURL, "/v1/usage"))
	if err != nil {
		return failed("request_build_failed")
	}
	query := usageURL.Query()
	query.Set("days", "1")
	usageURL.RawQuery = query.Encode()
	req.URL = usageURL
	req.Host = ""
	resp, err := s.accountTestService.httpUpstream.DoWithTLS(req, proxyURL, account.ID, account.Concurrency, tlsProfile)
	if resp != nil && resp.Body != nil {
		defer func() { _ = resp.Body.Close() }()
	}
	if err != nil {
		return failed("request_failed")
	}
	if resp == nil || resp.Body == nil {
		return failed("empty_response")
	}
	if resp.StatusCode == http.StatusNotFound || resp.StatusCode == http.StatusMethodNotAllowed {
		return unsupported("unsupported")
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return failed("http_error")
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, upstreamBillingProbeMaxBodyBytes+1))
	if err != nil {
		return failed("response_read_failed")
	}
	if len(body) > upstreamBillingProbeMaxBodyBytes {
		return failed("response_too_large")
	}
	amount, currency, reason := parseUpstreamWalletBalance(body)
	if reason == "wallet_not_exposed" || reason == "key_quota_only" || reason == "subscription_only" {
		return unsupported(reason)
	}
	if reason != "" {
		return failed(reason)
	}
	receivedAt := s.currentTime().UTC()
	return &UpstreamBalanceProbeSnapshot{Status: UpstreamBillingProbeStatusOK, Amount: &amount, Currency: currency, ReceivedAt: probeTimePtr(receivedAt), FreshUntil: probeTimePtr(receivedAt.Add(2 * time.Duration(intervalMinutes) * time.Minute)), LastAttemptAt: now}
}

func parseUpstreamWalletBalance(body []byte) (float64, string, string) {
	var response struct {
		Mode         string          `json:"mode"`
		IsValid      *bool           `json:"isValid"`
		Balance      json.RawMessage `json:"balance"`
		Unit         string          `json:"unit"`
		Subscription json.RawMessage `json:"subscription"`
	}
	if err := json.Unmarshal(body, &response); err != nil {
		return 0, "", "invalid_response"
	}
	if response.IsValid == nil || !*response.IsValid {
		return 0, "", "invalid_response"
	}
	if response.Mode == "quota_limited" {
		return 0, "", "key_quota_only"
	}
	if response.Mode != "unrestricted" {
		return 0, "", "invalid_response"
	}
	if len(response.Subscription) > 0 && string(response.Subscription) != "null" {
		return 0, "", "subscription_only"
	}
	if len(response.Balance) == 0 || string(response.Balance) == "null" {
		return 0, "", "wallet_not_exposed"
	}
	var amount float64
	if err := json.Unmarshal(response.Balance, &amount); err != nil || math.IsNaN(amount) || math.IsInf(amount, 0) {
		return 0, "", "invalid_balance"
	}
	currency := strings.ToUpper(strings.TrimSpace(response.Unit))
	if len(currency) != 3 {
		return 0, "", "invalid_currency"
	}
	for _, c := range currency {
		if c < 'A' || c > 'Z' {
			return 0, "", "invalid_currency"
		}
	}
	return amount, currency, ""
}
