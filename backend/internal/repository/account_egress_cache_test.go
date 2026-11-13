package repository

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
)

func TestAccountEgressAtomicCapacityAndIsolation(t *testing.T) {
	r := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: r.Addr()})
	t.Cleanup(func() { _ = client.Close() })
	cache, ok := NewConcurrencyCache(client, 15, 900).(service.AccountEgressCache)
	require.True(t, ok)
	ctx := context.Background()
	lanes := []service.AccountEgressLane{{ProxyID: 1, Concurrency: 5}, {ProxyID: 2, Concurrency: 5}, {ProxyID: 3, Concurrency: 5}}
	var success atomic.Int64
	var failed atomic.Int64
	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			id, err := cache.AcquireAccountEgress(ctx, 42, lanes, fmt.Sprintf("request-%d", i), time.Minute)
			if err != nil {
				failed.Add(1)
			}
			if id > 0 {
				success.Add(1)
			}
		}(i)
	}
	wg.Wait()
	require.Zero(t, failed.Load())
	require.EqualValues(t, 15, success.Load())
	counts, err := cache.GetAccountEgressCounts(ctx, map[int64][]service.AccountEgressLane{42: lanes})
	require.NoError(t, err)
	require.Equal(t, map[int64]int{1: 5, 2: 5, 3: 5}, counts[42])
	id, err := cache.AcquireAccountEgress(ctx, 43, lanes, "other-account", time.Minute)
	require.NoError(t, err)
	require.NotZero(t, id)
	// The UI total is tracked separately by the existing account request slots.
	ordinary := NewConcurrencyCache(client, 15, 900)
	count, err := ordinary.GetAccountConcurrency(ctx, 42)
	require.NoError(t, err)
	require.Zero(t, count)
}
func TestAccountEgressReleaseExpiryAndRenewal(t *testing.T) {
	r := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: r.Addr()})
	t.Cleanup(func() { _ = client.Close() })
	cache, ok := NewConcurrencyCache(client, 15, 900).(service.AccountEgressCache)
	require.True(t, ok)
	ctx := context.Background()
	lanes := []service.AccountEgressLane{{ProxyID: 7, Concurrency: 1}}
	id, err := cache.AcquireAccountEgress(ctx, 1, lanes, "first", time.Minute)
	require.NoError(t, err)
	require.EqualValues(t, 7, id)
	r.FastForward(40 * time.Second)
	owned, err := cache.RefreshAccountEgress(ctx, 1, 7, "first", time.Minute)
	require.NoError(t, err)
	require.True(t, owned)
	r.FastForward(40 * time.Second)
	id, err = cache.AcquireAccountEgress(ctx, 1, lanes, "blocked", time.Minute)
	require.NoError(t, err)
	require.Zero(t, id)
	require.NoError(t, cache.ReleaseAccountEgress(ctx, 1, 7, "first"))
	require.NoError(t, cache.ReleaseAccountEgress(ctx, 1, 7, "first"))
	id, err = cache.AcquireAccountEgress(ctx, 1, lanes, "second", time.Minute)
	require.NoError(t, err)
	require.EqualValues(t, 7, id)
	r.FastForward(61 * time.Second)
	owned, err = cache.RefreshAccountEgress(ctx, 1, 7, "second", time.Minute)
	require.NoError(t, err)
	require.False(t, owned)
	id, err = cache.AcquireAccountEgress(ctx, 1, lanes, "after-crash", time.Minute)
	require.NoError(t, err)
	require.EqualValues(t, 7, id)
	require.NoError(t, cache.ReleaseAccountEgress(ctx, 1, 7, "second"))
	counts, err := cache.GetAccountEgressCounts(ctx, map[int64][]service.AccountEgressLane{1: lanes})
	require.NoError(t, err)
	require.Equal(t, 1, counts[1][7])
}
func TestAccountEgressBalancesByUtilization(t *testing.T) {
	r := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: r.Addr()})
	t.Cleanup(func() { _ = client.Close() })
	cache, ok := NewConcurrencyCache(client, 15, 900).(service.AccountEgressCache)
	require.True(t, ok)
	ctx := context.Background()
	lanes := []service.AccountEgressLane{{ProxyID: 1, Concurrency: 2}, {ProxyID: 2, Concurrency: 6}}
	for i := 0; i < 4; i++ {
		_, err := cache.AcquireAccountEgress(ctx, 1, lanes, fmt.Sprint(i), time.Minute)
		require.NoError(t, err)
	}
	counts, err := cache.GetAccountEgressCounts(ctx, map[int64][]service.AccountEgressLane{1: lanes})
	require.NoError(t, err)
	require.Equal(t, map[int64]int{1: 1, 2: 3}, counts[1])
}
func TestSchedulerKeepsAccountEgressPool(t *testing.T) {
	pool := []service.AccountEgressLane{{ProxyID: 1, Concurrency: 5}}
	require.Equal(t, pool, filterSchedulerExtra(map[string]any{service.AccountEgressPoolExtraKey: pool})[service.AccountEgressPoolExtraKey])
}
