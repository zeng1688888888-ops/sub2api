package service

import (
	"context"
	"fmt"
	"sort"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
	"golang.org/x/sync/errgroup"
)

// Each account becomes due eight minutes after its latest completed probe.
const AccountAutomaticProbeInterval = 8 * time.Minute
const accountAutomaticProbePollInterval = 15 * time.Second
const accountSVGProbeMonitorLeaderKey = "openai:pelican-probe:leader"
const accountSVGProbeMonitorLeaderTTL = 7 * time.Minute
const accountSVGProbeMonitorMaxPerCycle = 2

type accountSVGProbeDueLister interface {
	ListDueAccountSVGProbeAccounts(context.Context, time.Time, int) ([]Account, error)
}

// Independent loops prevent a long animation from delaying intelligence/billing probes.
func (s *UpstreamBillingProbeService) runAccountProbeLoop(name string, run func(context.Context) error) {
	defer s.wg.Done()
	execute := func() {
		if s.parentCtx.Err() != nil {
			return
		}
		if err := run(s.parentCtx); err != nil && s.parentCtx.Err() == nil {
			logger.LegacyPrintf("service.account_auto_probe", "%s_cycle_failed: %v", name, err)
		}
	}
	execute()
	ticker := time.NewTicker(accountAutomaticProbePollInterval)
	defer ticker.Stop()
	for {
		select {
		case <-s.parentCtx.Done():
			return
		case <-ticker.C:
			execute()
		}
	}
}

func accountSVGProbeDue(a *Account, now time.Time) bool {
	if a == nil || a.Platform != PlatformOpenAI || !a.IsOpenAIOAuthLike() || !a.IsActive() || a.IsShadow() || a.IsSyntheticUITest() {
		return false
	}
	result := AccountSVGProbeFromAccount(a)
	return result == nil || result.FinishedAt.IsZero() || !now.Before(result.FinishedAt.Add(AccountAutomaticProbeInterval))
}

func (s *UpstreamBillingProbeService) listDueAccountSVGProbeAccounts(ctx context.Context, now time.Time) ([]Account, error) {
	var accounts []Account
	var err error
	if lister, ok := s.accountRepo.(accountSVGProbeDueLister); ok {
		accounts, err = lister.ListDueAccountSVGProbeAccounts(ctx, now, accountSVGProbeMonitorMaxPerCycle)
	} else {
		accounts, err = s.accountRepo.ListByPlatform(ctx, PlatformOpenAI)
	}
	if err != nil {
		return nil, err
	}
	due := make([]Account, 0, len(accounts))
	for i := range accounts {
		a := &accounts[i]
		if accountSVGProbeDue(a, now) && (s.accountTestService == nil || !s.accountTestService.AccountSVGProbeRunning(a.ID)) {
			due = append(due, *a)
		}
	}
	sort.SliceStable(due, func(i, j int) bool {
		left, right := AccountSVGProbeFromAccount(&due[i]), AccountSVGProbeFromAccount(&due[j])
		if left == nil || left.FinishedAt.IsZero() {
			return right != nil && !right.FinishedAt.IsZero()
		}
		if right == nil || right.FinishedAt.IsZero() {
			return false
		}
		return left.FinishedAt.Before(right.FinishedAt)
	})
	if len(due) > accountSVGProbeMonitorMaxPerCycle {
		due = due[:accountSVGProbeMonitorMaxPerCycle]
	}
	return due, nil
}

func (s *UpstreamBillingProbeService) RunAccountSVGProbeDue(ctx context.Context) error {
	if s == nil || s.accountRepo == nil || s.accountTestService == nil {
		return nil
	}
	s.svgProbeCycleMu.Lock()
	defer s.svgProbeCycleMu.Unlock()
	release, acquired, err := s.tryAcquireSVGProbeLeaderLock(ctx)
	if err != nil {
		return fmt.Errorf("acquire pelican probe leader lock: %w", err)
	}
	if !acquired {
		return nil
	}
	defer release()
	ctx, cancel := context.WithTimeout(ctx, accountSVGProbeMonitorLeaderTTL-time.Minute)
	defer cancel()
	accounts, err := s.listDueAccountSVGProbeAccounts(ctx, time.Now().UTC())
	if err != nil {
		return err
	}
	var group errgroup.Group
	group.SetLimit(2)
	for _, a := range accounts {
		id := a.ID
		if ctx.Err() != nil {
			break
		}
		group.Go(func() error {
			if ctx.Err() != nil {
				return nil
			}
			if _, err := s.accountTestService.probeAccountSVG(ctx, id, true); err != nil {
				logger.LegacyPrintf("service.account_auto_probe", "pelican_probe_failed: account_id=%d err=%v", id, err)
			}
			return nil
		})
	}
	return group.Wait()
}

func (s *UpstreamBillingProbeService) tryAcquireSVGProbeLeaderLock(ctx context.Context) (func(), bool, error) {
	lockCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	if s.lockCache != nil {
		acquired, err := s.lockCache.TryAcquireLeaderLock(lockCtx, accountSVGProbeMonitorLeaderKey, s.instanceID, accountSVGProbeMonitorLeaderTTL)
		if err != nil || !acquired {
			return func() {}, acquired, err
		}
		return func() {
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			_ = s.lockCache.ReleaseLeaderLock(ctx, accountSVGProbeMonitorLeaderKey, s.instanceID)
		}, true, nil
	}
	if s.db != nil {
		return tryAcquireDBAdvisoryLockWithError(lockCtx, s.db, hashAdvisoryLockID(accountSVGProbeMonitorLeaderKey))
	}
	return func() {}, true, nil
}
