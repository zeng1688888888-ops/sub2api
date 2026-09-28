package service

import (
	"context"
	"fmt"
	"sort"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
	"golang.org/x/sync/errgroup"
)

const (
	openAICodexStateProbeMonitorLeaderKey   = "openai:codex:state-probe:leader"
	openAICodexStateProbeMonitorLeaderTTL   = 2 * time.Minute
	openAICodexStateProbeMonitorMaxPerCycle = 20
	openAICodexStateProbeMonitorConcurrency = 2
)

type openAICodexStateProbeDueLister interface {
	ListDueOpenAICodexStateProbeAccounts(context.Context, time.Time, int) ([]Account, error)
}

// RunOpenAICodexStateProbeDue executes one bounded account-level state probe
// batch. It is hosted by the existing process-wide probe runner so it shares
// the application's lifecycle and leader-lock behavior.
func (s *UpstreamBillingProbeService) RunOpenAICodexStateProbeDue(ctx context.Context) error {
	if s == nil || s.accountRepo == nil || s.accountTestService == nil {
		return nil
	}
	s.cycleMu.Lock()
	defer s.cycleMu.Unlock()

	release, acquired, err := s.tryAcquireStateProbeLeaderLock(ctx)
	if err != nil {
		return fmt.Errorf("acquire state probe leader lock: %w", err)
	}
	if !acquired {
		return nil
	}
	defer release()
	// Finish the batch before its distributed lease expires. A slow account
	// must not let another instance overlap this monitor's requests.
	ctx, cancel := context.WithTimeout(ctx, openAICodexStateProbeMonitorLeaderTTL-10*time.Second)
	defer cancel()

	now := time.Now().UTC()
	accounts, err := s.listDueOpenAICodexStateProbeAccounts(ctx, now)
	if err != nil {
		return fmt.Errorf("list due state probes: %w", err)
	}
	if len(accounts) == 0 {
		return nil
	}
	if len(accounts) > openAICodexStateProbeMonitorMaxPerCycle {
		accounts = accounts[:openAICodexStateProbeMonitorMaxPerCycle]
	}

	var group errgroup.Group
	group.SetLimit(openAICodexStateProbeMonitorConcurrency)
	for i := range accounts {
		if ctx.Err() != nil {
			break
		}
		accountID := accounts[i].ID
		group.Go(func() error {
			if ctx.Err() != nil {
				return nil
			}
			if _, probeErr := s.accountTestService.ProbeOpenAIAccountIntelligence(ctx, accountID, OpenAICodexStateProbeDefaultModel); probeErr != nil {
				logger.LegacyPrintf("service.openai_codex_state_probe", "probe_due_failed: account_id=%d err=%v", accountID, probeErr)
			}
			return nil
		})
	}
	return group.Wait()
}

func (s *UpstreamBillingProbeService) listDueOpenAICodexStateProbeAccounts(ctx context.Context, now time.Time) ([]Account, error) {
	if lister, ok := s.accountRepo.(openAICodexStateProbeDueLister); ok {
		return lister.ListDueOpenAICodexStateProbeAccounts(ctx, now, openAICodexStateProbeMonitorMaxPerCycle)
	}
	accounts, err := s.accountRepo.ListByPlatform(ctx, PlatformOpenAI)
	if err != nil {
		return nil, err
	}
	due := make([]Account, 0, len(accounts))
	for i := range accounts {
		account := accounts[i]
		if !account.IsOpenAIOAuthLike() || !account.IsActive() {
			continue
		}
		snapshot := OpenAICodexStateProbeSnapshotFromAccount(&account)
		if snapshot != nil && snapshot.Method == openAIIntelligenceProbeMethod && snapshot.Route == openAIIntelligenceProbeRoute(&account, snapshot.Model) && !snapshot.NextProbeAt.IsZero() && now.Before(snapshot.NextProbeAt) {
			continue
		}
		due = append(due, account)
	}
	sort.SliceStable(due, func(i, j int) bool {
		left := OpenAICodexStateProbeSnapshotFromAccount(&due[i])
		right := OpenAICodexStateProbeSnapshotFromAccount(&due[j])
		if left == nil || left.NextProbeAt.IsZero() {
			return right != nil && !right.NextProbeAt.IsZero()
		}
		if right == nil || right.NextProbeAt.IsZero() {
			return false
		}
		return left.NextProbeAt.Before(right.NextProbeAt)
	})
	return due, nil
}

func (s *UpstreamBillingProbeService) tryAcquireStateProbeLeaderLock(ctx context.Context) (func(), bool, error) {
	lockCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	if s.lockCache != nil {
		acquired, err := s.lockCache.TryAcquireLeaderLock(lockCtx, openAICodexStateProbeMonitorLeaderKey, s.instanceID, openAICodexStateProbeMonitorLeaderTTL)
		if err != nil || !acquired {
			return func() {}, acquired, err
		}
		return func() {
			releaseCtx, releaseCancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer releaseCancel()
			_ = s.lockCache.ReleaseLeaderLock(releaseCtx, openAICodexStateProbeMonitorLeaderKey, s.instanceID)
		}, true, nil
	}
	if s.db != nil {
		return tryAcquireDBAdvisoryLockWithError(lockCtx, s.db, hashAdvisoryLockID(openAICodexStateProbeMonitorLeaderKey))
	}
	return func() {}, true, nil
}
