package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
)

const AccountEgressPoolExtraKey = "openai_egress_pool"
const accountEgressLeaseTTL = 90 * time.Second
const accountEgressRefreshInterval = 20 * time.Second

type AccountEgressLane struct {
	ProxyID     int64 `json:"proxy_id"`
	Concurrency int   `json:"concurrency"`
}
type AccountEgressStatus struct {
	ProxyID            int64  `json:"proxy_id"`
	Name               string `json:"name"`
	Concurrency        int    `json:"concurrency"`
	CurrentConcurrency *int   `json:"current_concurrency"`
	Available          bool   `json:"available"`
}

// Separate keys keep each account's proxy lanes independent of account/user slots.
type AccountEgressCache interface {
	AcquireAccountEgress(context.Context, int64, []AccountEgressLane, string, time.Duration) (int64, error)
	RefreshAccountEgress(context.Context, int64, int64, string, time.Duration) (bool, error)
	ReleaseAccountEgress(context.Context, int64, int64, string) error
	GetAccountEgressCounts(context.Context, map[int64][]AccountEgressLane) (map[int64]map[int64]int, error)
}

func ParseAccountEgressPool(extra map[string]any) ([]AccountEgressLane, error) {
	raw, exists := extra[AccountEgressPoolExtraKey]
	if !exists || raw == nil {
		return nil, nil
	}
	data, err := json.Marshal(raw)
	if err != nil {
		return nil, err
	}
	var lanes []AccountEgressLane
	if err := json.Unmarshal(data, &lanes); err != nil {
		return nil, errors.New("egress pool must be an array of proxy_id and concurrency")
	}
	if len(lanes) > 32 {
		return nil, errors.New("egress pool supports at most 32 proxies")
	}
	seen := make(map[int64]bool)
	for _, lane := range lanes {
		if lane.ProxyID <= 0 || lane.Concurrency < 1 || lane.Concurrency > 2000 {
			return nil, errors.New("each egress needs a proxy and concurrency between 1 and 2000")
		}
		if seen[lane.ProxyID] {
			return nil, errors.New("duplicate proxy in egress pool")
		}
		seen[lane.ProxyID] = true
	}
	return lanes, nil
}
func (a *Account) HasEgressPool() bool {
	if a == nil || a.Platform != PlatformOpenAI || a.IsCredentialShadow() {
		return false
	}
	lanes, err := ParseAccountEgressPool(a.Extra)
	// Invalid persisted config must fail closed at admission, never fall back to direct.
	return err != nil || len(lanes) > 0
}
func validateAccountEgressPool(ctx context.Context, repo ProxyRepository, platform string, shadow bool, extra map[string]any) error {
	lanes, err := ParseAccountEgressPool(extra)
	if err != nil {
		return infraerrors.BadRequest("INVALID_EGRESS_POOL", err.Error())
	}
	if len(lanes) == 0 {
		return nil
	}
	if platform != PlatformOpenAI || shadow {
		return infraerrors.BadRequest("INVALID_EGRESS_POOL", "egress pools require a non-shadow OpenAI account")
	}
	if repo == nil {
		return errors.New("proxy repository unavailable")
	}
	ids := make([]int64, 0, len(lanes))
	for _, lane := range lanes {
		ids = append(ids, lane.ProxyID)
	}
	proxies, err := repo.ListByIDs(ctx, ids)
	if err != nil {
		return err
	}
	if len(proxies) != len(lanes) {
		return infraerrors.BadRequest("INVALID_EGRESS_POOL", "one or more proxies no longer exist")
	}
	for _, p := range proxies {
		if !p.IsActive() || p.IsExpired(time.Now()) {
			return infraerrors.BadRequest("INVALID_EGRESS_POOL", "egress proxies must be active and unexpired")
		}
	}
	return nil
}

type accountEgressContextKey struct{}
type accountEgressSelection struct {
	accountID int64
	account   *Account
}

// beginAccountEgress pins the entire logical request (including internal retries)
// to one proxy. Only the request-local account copy is modified.
func (s *OpenAIGatewayService) beginAccountEgress(ctx context.Context, account *Account) (context.Context, *Account, func(), error) {
	noop := func() {}
	if !account.HasEgressPool() {
		return ctx, account, noop, nil
	}
	if selected, ok := ctx.Value(accountEgressContextKey{}).(*accountEgressSelection); ok && selected.accountID == account.ID {
		if account == selected.account {
			return ctx, account, noop, nil
		}
		copy := *account
		copy.ProxyID, copy.Proxy = selected.account.ProxyID, selected.account.Proxy
		return ctx, &copy, noop, nil
	}
	if s == nil || s.concurrencyService == nil {
		return ctx, account, noop, errors.New("egress admission unavailable")
	}
	return s.concurrencyService.acquireAccountEgress(ctx, account)
}

func (s *ConcurrencyService) acquireAccountEgress(ctx context.Context, account *Account) (context.Context, *Account, func(), error) {
	noop := func() {}
	cache, ok := s.cache.(AccountEgressCache)
	if !ok || s.egressProxyRepo == nil {
		return ctx, account, noop, errors.New("egress admission unavailable")
	}
	lanes, err := ParseAccountEgressPool(account.Extra)
	if err != nil {
		return ctx, account, noop, err
	}
	ids := make([]int64, 0, len(lanes))
	for _, lane := range lanes {
		ids = append(ids, lane.ProxyID)
	}
	proxies, err := s.egressProxyRepo.ListByIDs(ctx, ids)
	if err != nil {
		return ctx, account, noop, err
	}
	byID := make(map[int64]*Proxy)
	for i := range proxies {
		p := &proxies[i]
		if p.IsActive() && !p.IsExpired(time.Now()) {
			byID[p.ID] = p
		}
	}
	eligible := make([]AccountEgressLane, 0, len(lanes))
	for _, lane := range lanes {
		if byID[lane.ProxyID] != nil {
			eligible = append(eligible, lane)
		}
	}
	requestID := generateRequestID()
	proxyID, err := cache.AcquireAccountEgress(ctx, account.ID, eligible, requestID, accountEgressLeaseTTL)
	if err != nil {
		return ctx, account, noop, err
	}
	if proxyID == 0 {
		return ctx, account, noop, &UpstreamFailoverError{
			StatusCode: 503, RequestScopedTransient: true, ClientStatusCode: 503,
			ClientMessage: "All configured account egresses are busy or unavailable",
			ResponseBody:  []byte(`{"error":{"type":"egress_capacity_exceeded","message":"All configured account egresses are busy or unavailable"}}`),
		}
	}
	selected := *account
	selected.ProxyID = &proxyID
	selected.Proxy = byID[proxyID]
	leaseCtx, cancel := context.WithCancel(ctx)
	leaseCtx = context.WithValue(leaseCtx, accountEgressContextKey{}, &accountEgressSelection{account.ID, &selected})
	stop := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		ticker := time.NewTicker(accountEgressRefreshInterval)
		defer ticker.Stop()
		for {
			select {
			case <-stop:
				return
			case <-ticker.C:
				refreshCtx, refreshCancel := context.WithTimeout(context.Background(), 3*time.Second)
				owned, refreshErr := cache.RefreshAccountEgress(refreshCtx, account.ID, proxyID, requestID, accountEgressLeaseTTL)
				refreshCancel()
				if refreshErr != nil || !owned {
					cancel()
					return
				}
			}
		}
	}()
	var once sync.Once
	release := func() {
		once.Do(func() {
			close(stop)
			<-done
			cancel()
			releaseCtx, releaseCancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer releaseCancel()
			_ = cache.ReleaseAccountEgress(releaseCtx, account.ID, proxyID, requestID)
		})
	}
	return leaseCtx, &selected, release, nil
}

// Egress status contains only proxy IDs/names, never proxy credentials or URLs.
// A nil count means the live counter could not be read, rather than zero load.
func (s *ConcurrencyService) GetAccountEgressStatuses(ctx context.Context, accounts []Account) map[int64][]AccountEgressStatus {
	result := make(map[int64][]AccountEgressStatus)
	configs := make(map[int64][]AccountEgressLane)
	ids := make(map[int64]bool)
	for i := range accounts {
		a := &accounts[i]
		if !a.HasEgressPool() {
			continue
		}
		lanes, err := ParseAccountEgressPool(a.Extra)
		if err != nil {
			continue
		}
		configs[a.ID] = lanes
		for _, l := range lanes {
			ids[l.ProxyID] = true
		}
	}
	if len(configs) == 0 {
		return result
	}
	var counts map[int64]map[int64]int
	if s != nil {
		if cache, ok := s.cache.(AccountEgressCache); ok {
			counts, _ = cache.GetAccountEgressCounts(ctx, configs)
		}
	}
	byID := make(map[int64]Proxy)
	if s != nil && s.egressProxyRepo != nil {
		keys := make([]int64, 0, len(ids))
		for id := range ids {
			keys = append(keys, id)
		}
		proxies, err := s.egressProxyRepo.ListByIDs(ctx, keys)
		if err == nil {
			for _, p := range proxies {
				byID[p.ID] = p
			}
		}
	}
	for accountID, lanes := range configs {
		for _, l := range lanes {
			status := AccountEgressStatus{ProxyID: l.ProxyID, Name: fmt.Sprintf("Proxy #%d", l.ProxyID), Concurrency: l.Concurrency}
			if p, ok := byID[l.ProxyID]; ok {
				status.Name = p.Name
				status.Available = p.IsActive() && !p.IsExpired(time.Now())
			}
			if v, ok := counts[accountID][l.ProxyID]; ok {
				status.CurrentConcurrency = &v
			}
			result[accountID] = append(result[accountID], status)
		}
	}
	return result
}
