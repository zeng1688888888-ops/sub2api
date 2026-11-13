package admin

import (
	"fmt"
	"maps"

	"github.com/Wei-Shaw/sub2api/internal/service"
)

type DataAccountEgress struct {
	ProxyKey    string `json:"proxy_key"`
	Concurrency int    `json:"concurrency"`
}

// Backups use portable proxy keys, never source-database proxy IDs.
func exportAccountEgress(extra map[string]any, proxyKeys map[int64]string, includeProxies bool) (map[string]any, []DataAccountEgress, error) {
	copy := maps.Clone(extra)
	delete(copy, service.AccountEgressPoolExtraKey)
	lanes, err := service.ParseAccountEgressPool(extra)
	if err != nil {
		return nil, nil, err
	}
	if !includeProxies {
		return copy, nil, nil
	}
	var exported []DataAccountEgress
	for _, lane := range lanes {
		key, ok := proxyKeys[lane.ProxyID]
		if !ok {
			return nil, nil, fmt.Errorf("egress proxy %d is unavailable for export", lane.ProxyID)
		}
		exported = append(exported, DataAccountEgress{ProxyKey: key, Concurrency: lane.Concurrency})
	}
	return copy, exported, nil
}
func importAccountEgress(extra map[string]any, lanes []DataAccountEgress, proxyIDs map[string]int64) (map[string]any, error) {
	copy := maps.Clone(extra)
	if raw, exists := copy[service.AccountEgressPoolExtraKey]; exists && raw != nil {
		old, err := service.ParseAccountEgressPool(copy)
		if err != nil || len(old) > 0 {
			return nil, fmt.Errorf("egress pool contains non-portable proxy IDs; export with proxies or configure egresses after import")
		}
	}
	delete(copy, service.AccountEgressPoolExtraKey)
	if len(lanes) == 0 {
		return copy, nil
	}
	if copy == nil {
		copy = make(map[string]any)
	}
	pool := make([]service.AccountEgressLane, 0, len(lanes))
	for _, lane := range lanes {
		id, ok := proxyIDs[lane.ProxyKey]
		if !ok {
			return nil, fmt.Errorf("egress proxy_key not found")
		}
		pool = append(pool, service.AccountEgressLane{ProxyID: id, Concurrency: lane.Concurrency})
	}
	copy[service.AccountEgressPoolExtraKey] = pool
	if _, err := service.ParseAccountEgressPool(copy); err != nil {
		return nil, err
	}
	return copy, nil
}
