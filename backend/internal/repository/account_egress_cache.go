package repository

import (
	"context"
	"fmt"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/redis/go-redis/v9"
)

// Account hash tags allow an atomic multi-lane reservation on Redis Cluster.
func accountEgressKey(accountID, proxyID int64) string {
	return fmt.Sprintf("concurrency:egress:{%d}:%d", accountID, proxyID)
}

var acquireEgressScript = redis.NewScript(`
local t = redis.call('TIME')
local now = tonumber(t[1]) * 1000 + math.floor(tonumber(t[2]) / 1000)
local selected = 0
local best = 2
local offset = tonumber(t[2]) % #KEYS
for step = 1, #KEYS do
 local i = ((step + offset - 1) % #KEYS) + 1
 redis.call('ZREMRANGEBYSCORE', KEYS[i], '-inf', now)
 local count = redis.call('ZCARD', KEYS[i])
 local limit = tonumber(ARGV[i+2])
 if limit > 0 and count < limit and count / limit < best then
  best = count / limit
  selected = i
 end
end
if selected == 0 then return 0 end
redis.call('ZADD', KEYS[selected], now + tonumber(ARGV[2]), ARGV[1])
redis.call('PEXPIRE', KEYS[selected], ARGV[2])
return selected
`)
var refreshEgressScript = redis.NewScript(`
local t = redis.call('TIME')
local now = tonumber(t[1]) * 1000 + math.floor(tonumber(t[2]) / 1000)
local expiry = redis.call('ZSCORE', KEYS[1], ARGV[1])
if not expiry or tonumber(expiry) <= now then return 0 end
redis.call('ZADD', KEYS[1], 'XX', now + tonumber(ARGV[2]), ARGV[1])
redis.call('PEXPIRE', KEYS[1], ARGV[2])
return 1
`)

const countEgressScript = `
local t = redis.call('TIME')
local now = tonumber(t[1]) * 1000 + math.floor(tonumber(t[2]) / 1000)
redis.call('ZREMRANGEBYSCORE', KEYS[1], '-inf', now)
return redis.call('ZCARD', KEYS[1])
`

func (c *concurrencyCache) AcquireAccountEgress(ctx context.Context, accountID int64, lanes []service.AccountEgressLane, requestID string, ttl time.Duration) (int64, error) {
	if len(lanes) == 0 {
		return 0, nil
	}
	keys := make([]string, len(lanes))
	args := []any{requestID, ttl.Milliseconds()}
	for i, l := range lanes {
		keys[i] = accountEgressKey(accountID, l.ProxyID)
		args = append(args, l.Concurrency)
	}
	selected, err := acquireEgressScript.Run(ctx, c.rdb, keys, args...).Int()
	if err != nil || selected == 0 {
		return 0, err
	}
	return lanes[selected-1].ProxyID, nil
}
func (c *concurrencyCache) RefreshAccountEgress(ctx context.Context, accountID, proxyID int64, requestID string, ttl time.Duration) (bool, error) {
	v, err := refreshEgressScript.Run(ctx, c.rdb, []string{accountEgressKey(accountID, proxyID)}, requestID, ttl.Milliseconds()).Int()
	return v == 1, err
}
func (c *concurrencyCache) ReleaseAccountEgress(ctx context.Context, accountID, proxyID int64, requestID string) error {
	return c.rdb.ZRem(ctx, accountEgressKey(accountID, proxyID), requestID).Err()
}
func (c *concurrencyCache) GetAccountEgressCounts(ctx context.Context, configs map[int64][]service.AccountEgressLane) (map[int64]map[int64]int, error) {
	pipe := c.rdb.Pipeline()
	pending := make(map[int64]map[int64]*redis.Cmd)
	for id, lanes := range configs {
		pending[id] = make(map[int64]*redis.Cmd)
		for _, l := range lanes {
			pending[id][l.ProxyID] = pipe.Eval(ctx, countEgressScript, []string{accountEgressKey(id, l.ProxyID)})
		}
	}
	if _, err := pipe.Exec(ctx); err != nil {
		return nil, err
	}
	result := make(map[int64]map[int64]int)
	for id, cmds := range pending {
		result[id] = make(map[int64]int)
		for proxyID, cmd := range cmds {
			v, err := cmd.Int()
			if err != nil {
				return nil, err
			}
			result[id][proxyID] = v
		}
	}
	return result, nil
}

var _ service.AccountEgressCache = (*concurrencyCache)(nil)
