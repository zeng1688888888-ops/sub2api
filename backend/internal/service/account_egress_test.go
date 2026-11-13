package service

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type egressProxyRepoStub struct {
	ProxyRepository
	proxies []Proxy
}

func (r *egressProxyRepoStub) ListByIDs(_ context.Context, ids []int64) ([]Proxy, error) {
	var result []Proxy
	for _, p := range r.proxies {
		for _, id := range ids {
			if p.ID == id {
				result = append(result, p)
			}
		}
	}
	return result, nil
}

type egressCacheStub struct {
	ConcurrencyCache
	lanes        []AccountEgressLane
	acquireCalls int
	releaseCalls int
	selected     int64
	failure      error
}

func (c *egressCacheStub) AcquireAccountEgress(_ context.Context, _ int64, lanes []AccountEgressLane, _ string, _ time.Duration) (int64, error) {
	c.acquireCalls++
	c.lanes = lanes
	return c.selected, c.failure
}
func (c *egressCacheStub) RefreshAccountEgress(context.Context, int64, int64, string, time.Duration) (bool, error) {
	return true, nil
}
func (c *egressCacheStub) ReleaseAccountEgress(context.Context, int64, int64, string) error {
	c.releaseCalls++
	return nil
}
func (c *egressCacheStub) GetAccountEgressCounts(context.Context, map[int64][]AccountEgressLane) (map[int64]map[int64]int, error) {
	return nil, errors.New("redis unavailable")
}
func egressTestAccount() *Account {
	return &Account{ID: 42, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Concurrency: 100, Extra: map[string]any{AccountEgressPoolExtraKey: []AccountEgressLane{{ProxyID: 1, Concurrency: 5}, {ProxyID: 2, Concurrency: 5}, {ProxyID: 3, Concurrency: 5}}}}
}
func TestAccountEgressRequestCopyNestedCallAndRelease(t *testing.T) {
	cache := &egressCacheStub{selected: 2}
	expired := time.Now().Add(-time.Hour)
	concurrency := NewConcurrencyService(cache)
	concurrency.egressProxyRepo = &egressProxyRepoStub{proxies: []Proxy{{ID: 1, Status: "inactive"}, {ID: 2, Name: "exit two", Status: StatusActive}, {ID: 3, Status: StatusActive, ExpiresAt: &expired}}}
	gateway := &OpenAIGatewayService{concurrencyService: concurrency}
	account := egressTestAccount()
	ctx, selected, release, err := gateway.beginAccountEgress(context.Background(), account)
	require.NoError(t, err)
	defer release()
	require.Nil(t, account.ProxyID)
	require.Nil(t, account.Proxy)
	require.EqualValues(t, 2, *selected.ProxyID)
	require.Equal(t, "exit two", selected.Proxy.Name)
	require.Equal(t, account.ID, selected.ID)
	require.Equal(t, []AccountEgressLane{{ProxyID: 2, Concurrency: 5}}, cache.lanes)
	_, again, nestedRelease, err := gateway.beginAccountEgress(ctx, account)
	require.NoError(t, err)
	require.Equal(t, selected.ProxyID, again.ProxyID)
	nestedRelease()
	require.Equal(t, 1, cache.acquireCalls)
	require.Zero(t, cache.releaseCalls)
	release()
	release()
	require.Equal(t, 1, cache.releaseCalls)
}
func TestAccountEgressFullAndFailureNeverFallBack(t *testing.T) {
	for _, failure := range []error{nil, errors.New("redis unavailable")} {
		t.Run("admission", func(t *testing.T) {
			cache := &egressCacheStub{failure: failure}
			concurrency := NewConcurrencyService(cache)
			concurrency.egressProxyRepo = &egressProxyRepoStub{proxies: []Proxy{{ID: 1, Status: StatusActive}}}
			gateway := &OpenAIGatewayService{concurrencyService: concurrency}
			_, _, release, err := gateway.beginAccountEgress(context.Background(), egressTestAccount())
			require.Error(t, err)
			release()
			require.Zero(t, cache.releaseCalls)
			if failure == nil {
				var full *UpstreamFailoverError
				require.ErrorAs(t, err, &full)
				require.True(t, full.RequestScopedTransient)
				require.Equal(t, 503, full.StatusCode)
			}
		})
	}
}
func TestAccountEgressValidation(t *testing.T) {
	for _, raw := range []any{"not-array", []any{map[string]any{"proxy_id": 1, "concurrency": 1.5}}, []AccountEgressLane{{ProxyID: 0, Concurrency: 5}}, []AccountEgressLane{{ProxyID: 1, Concurrency: 0}}, []AccountEgressLane{{ProxyID: 1, Concurrency: 2001}}, []AccountEgressLane{{ProxyID: 1, Concurrency: 5}, {ProxyID: 1, Concurrency: 5}}} {
		_, err := ParseAccountEgressPool(map[string]any{AccountEgressPoolExtraKey: raw})
		require.Error(t, err)
	}
	ctx := context.Background()
	repo := &egressProxyRepoStub{proxies: []Proxy{{ID: 1, Status: StatusActive}}}
	extra := map[string]any{AccountEgressPoolExtraKey: []AccountEgressLane{{ProxyID: 1, Concurrency: 5}}}
	require.NoError(t, validateAccountEgressPool(ctx, repo, PlatformOpenAI, false, extra))
	require.Error(t, validateAccountEgressPool(ctx, repo, PlatformAnthropic, false, extra))
	require.Error(t, validateAccountEgressPool(ctx, repo, PlatformOpenAI, true, extra))
	require.Error(t, validateAccountEgressPool(ctx, repo, PlatformOpenAI, false, egressTestAccount().Extra))
	require.NoError(t, validateAccountEgressPool(ctx, nil, PlatformOpenAI, false, map[string]any{AccountEgressPoolExtraKey: []AccountEgressLane{}}))
}
func TestAccountEgressStatusUnknownAndNoSecrets(t *testing.T) {
	svc := NewConcurrencyService(&egressCacheStub{})
	svc.egressProxyRepo = &egressProxyRepoStub{proxies: []Proxy{{ID: 1, Name: "exit", Password: "secret", Status: StatusActive}}}
	statuses := svc.GetAccountEgressStatuses(context.Background(), []Account{*egressTestAccount()})[42]
	require.Len(t, statuses, 3)
	require.Nil(t, statuses[0].CurrentConcurrency)
	require.True(t, statuses[0].Available)
	require.False(t, statuses[1].Available)
}
func TestAccountEgressForcesHTTPAndAllowsBridge(t *testing.T) {
	a := egressTestAccount()
	require.True(t, a.IsOpenAIWSForceHTTPEnabled())
	cfg := &config.Config{}
	cfg.Gateway.OpenAIWS.Enabled = true
	cfg.Gateway.OpenAIWS.ModeRouterV2Enabled = true
	cfg.Gateway.OpenAIWS.IngressModeDefault = OpenAIWSIngressModeCtxPool
	s := &OpenAIGatewayService{cfg: cfg}
	require.True(t, s.isOpenAIAccountTransportCompatible(a, OpenAIUpstreamTransportResponsesWebsocketV2Ingress))
	require.Equal(t, OpenAIUpstreamTransportHTTPSSE, NewOpenAIWSProtocolResolver(cfg).Resolve(a).Transport)
}

func TestAccountEgressForwardUsesSelectedProxyAndReleases(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, route := range []string{"codex", "bps", "ws_bridge"} {
		t.Run(route, func(t *testing.T) {
			wire := `data: {"type":"response.completed","response":{"id":"resp_egress","status":"completed","model":"gpt-6-astra","output":[],"usage":{"input_tokens":1,"output_tokens":1}}}` + "\n\n"
			upstream := &httpUpstreamRecorder{resp: &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{"text/event-stream"}}, Body: io.NopCloser(strings.NewReader(wire))}}
			svc := openAIClientToolsTestService(upstream)
			cache := &egressCacheStub{selected: 2}
			svc.concurrencyService = NewConcurrencyService(cache)
			svc.concurrencyService.egressProxyRepo = &egressProxyRepoStub{proxies: []Proxy{{ID: 2, Name: "selected", Protocol: "http", Host: "egress.example", Port: 8080, Status: StatusActive}}}
			account := excelAccount()
			account.Extra[AccountEgressPoolExtraKey] = []AccountEgressLane{{ProxyID: 2, Concurrency: 5}}
			account.Extra["openai_excel_bps"] = route == "bps"
			body := []byte(`{"model":"gpt-6-astra","stream":true,"input":[{"role":"user","content":"hello"}]}`)
			rec := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(rec)
			c.Request = httptest.NewRequest("POST", "/v1/responses", bytes.NewReader(body))
			var result *OpenAIForwardResult
			var err error
			if route == "ws_bridge" {
				result, err = svc.proxyOpenAIWSHTTPBridgeTurn(context.Background(), c, account, "test-token", body, len(body), "gpt-6-astra", "", "", "", "", 1, func([]byte) error { return nil })
			} else {
				result, err = svc.Forward(context.Background(), c, account, body)
			}
			require.NoError(t, err)
			require.NotNil(t, result)
			require.Equal(t, "http://egress.example:8080", upstream.lastProxyURL)
			require.Equal(t, 1, cache.acquireCalls)
			require.Equal(t, 1, cache.releaseCalls)
			require.Nil(t, account.ProxyID)
		})
	}
}

// Optional egress pools must not change ordinary direct or single-proxy requests.
func TestAccountEgressUnconfiguredPreservesOrdinaryForwarding(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, emptyPool := range []bool{false, true} {
		for _, singleProxy := range []bool{false, true} {
			name := "direct"
			if singleProxy {
				name = "single_proxy"
			}
			if emptyPool {
				name += "_empty_pool"
			} else {
				name += "_absent_pool"
			}
			t.Run(name, func(t *testing.T) {
				wire := `data: {"type":"response.completed","response":{"id":"resp_direct","status":"completed","model":"gpt-6-astra","output":[],"usage":{"input_tokens":1,"output_tokens":1}}}

`
				upstream := &httpUpstreamRecorder{resp: &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{"text/event-stream"}}, Body: io.NopCloser(strings.NewReader(wire))}}
				gateway := openAIClientToolsTestService(upstream)
				cache := &egressCacheStub{failure: errors.New("egress unavailable")}
				gateway.concurrencyService = NewConcurrencyService(cache)
				account := excelAccount()
				account.Extra["openai_excel_bps"] = false
				delete(account.Extra, AccountEgressPoolExtraKey)
				if emptyPool {
					account.Extra[AccountEgressPoolExtraKey] = []AccountEgressLane{}
				}
				expectedProxy := ""
				if singleProxy {
					id := int64(9)
					account.ProxyID = &id
					account.Proxy = &Proxy{ID: id, Protocol: "http", Host: "ordinary.example", Port: 8080, Status: StatusActive}
					expectedProxy = "http://ordinary.example:8080"
				}
				body := []byte(`{"model":"gpt-6-astra","stream":true,"input":[{"role":"user","content":"hello"}]}`)
				c, _ := gin.CreateTestContext(httptest.NewRecorder())
				c.Request = httptest.NewRequest("POST", "/v1/responses", bytes.NewReader(body))
				result, err := gateway.Forward(context.Background(), c, account, body)
				require.NoError(t, err)
				require.NotNil(t, result)
				require.Equal(t, expectedProxy, upstream.lastProxyURL)
				require.Zero(t, cache.acquireCalls)
				require.Zero(t, cache.releaseCalls)
			})
		}
	}
}
