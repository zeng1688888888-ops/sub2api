package admin

import (
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestAccountEgressBackupRemapsProxyIDs(t *testing.T) {
	extra := map[string]any{"keep": "value", service.AccountEgressPoolExtraKey: []service.AccountEgressLane{{ProxyID: 1, Concurrency: 5}, {ProxyID: 2, Concurrency: 7}}}
	exported, lanes, err := exportAccountEgress(extra, map[int64]string{1: "proxy-a", 2: "proxy-b"}, true)
	require.NoError(t, err)
	require.NotContains(t, exported, service.AccountEgressPoolExtraKey)
	require.Contains(t, extra, service.AccountEgressPoolExtraKey)
	imported, err := importAccountEgress(exported, lanes, map[string]int64{"proxy-a": 100, "proxy-b": 200})
	require.NoError(t, err)
	pool, err := service.ParseAccountEgressPool(imported)
	require.NoError(t, err)
	require.Equal(t, []service.AccountEgressLane{{ProxyID: 100, Concurrency: 5}, {ProxyID: 200, Concurrency: 7}}, pool)
	require.Equal(t, "value", imported["keep"])
	_, err = importAccountEgress(exported, lanes, map[string]int64{"proxy-a": 100})
	require.Error(t, err)
	_, err = importAccountEgress(extra, nil, map[string]int64{})
	require.Error(t, err)
	without, lanes, err := exportAccountEgress(extra, nil, false)
	require.NoError(t, err)
	require.Empty(t, lanes)
	require.NotContains(t, without, service.AccountEgressPoolExtraKey)
}
