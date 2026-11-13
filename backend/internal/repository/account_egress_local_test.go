//go:build egress_local

package repository

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
)

type localEgressProxyFixture struct{ service.ProxyRepository }

func (*localEgressProxyFixture) ListByIDs(_ context.Context, ids []int64) ([]service.Proxy, error) {
	proxies := make([]service.Proxy, 0, len(ids))
	for _, id := range ids {
		proxies = append(proxies, service.Proxy{ID: id, Name: fmt.Sprintf("Local proxy %d", id), Protocol: "http", Host: "127.0.0.1", Port: 18100 + int(id), Status: service.StatusActive})
	}
	return proxies, nil
}

type localEgressMetrics struct {
	TotalActive          int `json:"totalActive"`
	TotalPeak            int `json:"totalPeak"`
	CredentialIdentities int `json:"credentialIdentities"`
	Lanes                map[string]struct {
		Active int `json:"active"`
		Peak   int `json:"peak"`
		Total  int `json:"total"`
	} `json:"lanes"`
}

func readLocalMetrics(t *testing.T) localEgressMetrics {
	t.Helper()
	resp, err := http.Get("http://127.0.0.1:18100/admin/metrics")
	require.NoError(t, err)
	defer resp.Body.Close()
	var metrics localEgressMetrics
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&metrics))
	return metrics
}
func localEgressGateway(t *testing.T) (*service.OpenAIGatewayService, service.ConcurrencyCache) {
	t.Helper()
	addr := os.Getenv("EGRESS_TEST_REDIS_ADDR")
	if addr != "127.0.0.1:16379" {
		t.Skip("opt in with EGRESS_TEST_REDIS_ADDR=127.0.0.1:16379; uses isolated Redis DB 1")
	}
	client := redis.NewClient(&redis.Options{Addr: addr, DB: 1})
	t.Cleanup(func() { _ = client.Close() })
	require.NoError(t, client.Ping(context.Background()).Err())
	cache := NewConcurrencyCache(client, 15, 900)
	concurrency := service.ProvideConcurrencyService(cache, nil, nil, &localEgressProxyFixture{})
	cfg := &config.Config{Security: config.SecurityConfig{URLAllowlist: config.URLAllowlistConfig{Enabled: false, AllowPrivateHosts: true, AllowInsecureHTTP: true}}}
	upstream := NewHTTPUpstream(cfg)
	gateway := service.NewOpenAIGatewayService(nil, nil, nil, nil, nil, nil, nil, cfg, nil, concurrency, nil, nil, nil, upstream, nil, nil, nil, nil, nil, nil, nil, nil)
	t.Cleanup(gateway.CloseOpenAIWSPool)
	return gateway, cache
}
func TestLocalAccountEgress(t *testing.T) {
	gin.SetMode(gin.TestMode)
	gateway, cache := localEgressGateway(t)
	cases := []struct {
		Name                             string
		Limits                           []int
		AccountLimit, Requests, WantPeak int
	}{
		{"smoke", []int{5, 5, 5}, 300, 1, 1},
		{"three_exits_overflow", []int{5, 5, 5}, 300, 100, 15},
		{"account_total_limit", []int{5, 5, 5}, 7, 30, 7},
		{"two_hundred", []int{70, 70, 60}, 300, 200, 200},
	}
	results := make([]map[string]any, 0, len(cases))
	for testIndex, tc := range cases {
		t.Run(tc.Name, func(t *testing.T) {
			response, err := http.Get("http://127.0.0.1:18100/admin/reset?hold_ms=5000")
			require.NoError(t, err)
			require.Equal(t, 200, response.StatusCode)
			response.Body.Close()
			lanes := make([]service.AccountEgressLane, len(tc.Limits))
			for i, limit := range tc.Limits {
				lanes[i] = service.AccountEgressLane{ProxyID: int64(i + 1), Concurrency: limit}
			}
			account := &service.Account{ID: int64(90000 + testIndex), Name: "local-only-fixture", Platform: service.PlatformOpenAI, Type: service.AccountTypeAPIKey, Concurrency: tc.AccountLimit, Status: service.StatusActive, Schedulable: true, Credentials: map[string]any{"api_key": "local-egress-fixture-only", "base_url": "http://127.0.0.1:18100", "api_protocol": "responses"}, Extra: map[string]any{"openai_passthrough": true, service.AccountEgressPoolExtraKey: lanes}}
			start := make(chan struct{})
			var wg sync.WaitGroup
			var succeeded, limited, failed atomic.Int64
			var errorMu sync.Mutex
			firstError := ""
			startedAt := time.Now()
			for i := 0; i < tc.Requests; i++ {
				wg.Add(1)
				go func(i int) {
					defer wg.Done()
					<-start
					ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
					defer cancel()
					requestID := fmt.Sprintf("local-%d-%d", account.ID, i)
					acquired, slotErr := cache.AcquireAccountSlot(ctx, account.ID, account.Concurrency, requestID)
					if slotErr != nil {
						failed.Add(1)
						return
					}
					if !acquired {
						limited.Add(1)
						return
					}
					defer cache.ReleaseAccountSlot(context.Background(), account.ID, requestID)
					body := []byte(fmt.Sprintf("{\"model\":\"gpt-4o\",\"stream\":true,\"input\":\"Local test %d\"}", i))
					rec := httptest.NewRecorder()
					c, _ := gin.CreateTestContext(rec)
					c.Request = httptest.NewRequest("POST", "/v1/responses", bytes.NewReader(body)).WithContext(ctx)
					result, forwardErr := gateway.Forward(ctx, c, account, body)
					if forwardErr != nil {
						var full *service.UpstreamFailoverError
						if errors.As(forwardErr, &full) && full.ClientMessage == "All configured account egresses are busy or unavailable" {
							limited.Add(1)
							return
						}
						failed.Add(1)
						errorMu.Lock()
						if firstError == "" {
							firstError = forwardErr.Error()
						}
						errorMu.Unlock()
						return
					}
					if result == nil || rec.Code != 200 || !bytes.Contains(rec.Body.Bytes(), []byte("response.completed")) {
						failed.Add(1)
						return
					}
					succeeded.Add(1)
				}(i)
			}
			close(start)
			wg.Wait()
			metrics := readLocalMetrics(t)
			counts, countErr := cache.(service.AccountEgressCache).GetAccountEgressCounts(context.Background(), map[int64][]service.AccountEgressLane{account.ID: lanes})
			require.NoError(t, countErr)
			report := map[string]any{"scenario": tc.Name, "requests": tc.Requests, "account_limit": tc.AccountLimit, "lane_limits": tc.Limits, "completed": succeeded.Load(), "limited": limited.Load(), "failed": failed.Load(), "duration_ms": time.Since(startedAt).Milliseconds(), "metrics": metrics, "remaining_slots": counts[account.ID], "first_error": firstError}
			results = append(results, report)
			encoded, _ := json.Marshal(report)
			t.Log(string(encoded))
			require.Zero(t, failed.Load(), firstError)
			require.EqualValues(t, tc.WantPeak, succeeded.Load())
			require.Equal(t, tc.WantPeak, metrics.TotalPeak)
			require.Equal(t, 1, metrics.CredentialIdentities)
			require.Zero(t, metrics.TotalActive)
			for i, limit := range tc.Limits {
				require.LessOrEqual(t, metrics.Lanes[fmt.Sprint(18101+i)].Peak, limit)
				require.Zero(t, counts[account.ID][int64(i+1)])
			}
		})
	}
	if output := os.Getenv("EGRESS_TEST_REPORT_DIR"); output != "" {
		require.NoError(t, os.MkdirAll(output, 0700))
		encoded, err := json.MarshalIndent(results, "", "  ")
		require.NoError(t, err)
		require.NoError(t, os.WriteFile(filepath.Join(output, "results.json"), encoded, 0600))
	}
}
