package service

import "time"

// AccountAutomaticProbeAvailable ignores the manual scheduling switch: a healthy
// paused account may still be monitored, but a known unavailable account may not.
func AccountAutomaticProbeAvailable(account *Account, now time.Time) bool {
	return account.SupportsIntelligenceScheduling() && accountProbeUnavailableReason(account, now) == ""
}

// A skipped probe must not overwrite results or count as an intelligence failure.
// Use the latest persisted quota windows, including legacy primary/secondary
// snapshots. Unknown quota permits initial detection; known exhaustion without
// a reset remains blocked until usage is refreshed. Snapshot age alone is not
// evidence that quota has recovered.
func accountProbeUnavailableReason(account *Account, now time.Time) string {
	if account == nil || !account.IsActive() {
		return "账号不可用，已跳过测试"
	}
	if account.AutoPauseOnExpired && account.ExpiresAt != nil && !now.Before(*account.ExpiresAt) {
		return "账号已过期，已跳过测试"
	}
	if account.RateLimitResetAt != nil && now.Before(*account.RateLimitResetAt) {
		return "账号正在限流或等待额度恢复，已跳过测试"
	}
	if account.OverloadUntil != nil && now.Before(*account.OverloadUntil) {
		return "账号处于过载冷却期，已跳过测试"
	}
	if account.TempUnschedulableUntil != nil && now.Before(*account.TempUnschedulableUntil) {
		return "账号处于临时冷却期，已跳过测试"
	}
	if account.IsOpenAIOAuthLike() && openAICodexSnapshotIdentityTrusted(account) {
		window5h, window7d := openAICanonicalQuotaWindows(account.Extra, now)
		if window5h.hasUsed && !window5h.reset && window5h.usedPercent >= 100 {
			return "账号5小时额度已耗尽，恢复后再测试"
		}
		if window7d.hasUsed && !window7d.reset && window7d.usedPercent >= 100 {
			return "账号7天额度已耗尽，恢复后再测试"
		}
	}
	return ""
}
