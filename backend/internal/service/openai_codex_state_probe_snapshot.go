package service

import (
	"context"
	"encoding/json"
	"math"
	"strings"
	"time"
)

// OpenAICodexStateProbeSnapshotExtraKey is the account extra key used by the
// account list and the background monitor. The snapshot is intentionally kept
// separate from the ticket cache so a probe result can never be mistaken for
// a usable turn ticket.
const OpenAICodexStateProbeSnapshotExtraKey = "openai_codex_state_probe"

const (
	openAICodexStateProbeHistoryLimit        = 500
	openAICodexStateProbeInterval            = 30 * time.Minute
	openAICodexStateProbeUnsupportedInterval = 4 * time.Hour
)

// OpenAICodexStateProbeSnapshot is the compact account-level state projected
// into the admin account list. History stores only decisive verdicts and is
// bounded so a long-lived account cannot grow its JSONB value without limit.
type OpenAICodexStateProbeSnapshot struct {
	Method               string                         `json:"method,omitempty"`
	Route                string                         `json:"route,omitempty"`
	Answer               string                         `json:"answer,omitempty"`
	Status               string                         `json:"status"`
	Verdict              OpenAICodexStateVerdict        `json:"verdict,omitempty"`
	Model                string                         `json:"model,omitempty"`
	ReportedModel        string                         `json:"reported_model,omitempty"`
	HealthRate           *float64                       `json:"health_rate,omitempty"`
	HealthyCount         int                            `json:"healthy_count,omitempty"`
	DegradedCount        int                            `json:"degraded_count,omitempty"`
	SampleCount          int                            `json:"sample_count,omitempty"`
	ConsecutiveHealthy   int                            `json:"consecutive_healthy,omitempty"`
	ConsecutiveDegraded  int                            `json:"consecutive_degraded,omitempty"`
	LastProbeAt          time.Time                      `json:"last_probe_at,omitempty"`
	NextProbeAt          time.Time                      `json:"next_probe_at,omitempty"`
	Failure              string                         `json:"failure,omitempty"`
	Reason               string                         `json:"reason,omitempty"`
	Detail               string                         `json:"detail,omitempty"`
	TicketLength         int                            `json:"ticket_length,omitempty"`
	ContinueTicketLength int                            `json:"continue_ticket_length,omitempty"`
	NewTicket            bool                           `json:"new_ticket,omitempty"`
	History              []OpenAICodexStateProbeVerdict `json:"history,omitempty"`
}

// OpenAICodexStateProbeVerdict is kept as a small string so the history can
// be used as a rolling window without retaining response bodies or tickets.
type OpenAICodexStateProbeVerdict string

const (
	OpenAICodexStateProbeHistoryHealthy  OpenAICodexStateProbeVerdict = "healthy"
	OpenAICodexStateProbeHistoryDegraded OpenAICodexStateProbeVerdict = "degraded"
)

func OpenAICodexStateProbeSnapshotFromAccount(account *Account) *OpenAICodexStateProbeSnapshot {
	if account == nil || account.Extra == nil {
		return nil
	}
	snapshot := OpenAICodexStateProbeSnapshotFromExtra(account.Extra)
	if snapshot == nil || snapshot.Method != openAIIntelligenceProbeMethod || snapshot.Route != openAIIntelligenceProbeRoute(account, snapshot.Model) {
		return nil
	}
	return snapshot
}

func OpenAICodexStateProbeSnapshotFromExtra(extra map[string]any) *OpenAICodexStateProbeSnapshot {
	if extra == nil {
		return nil
	}
	raw, ok := extra[OpenAICodexStateProbeSnapshotExtraKey]
	if !ok || raw == nil {
		return nil
	}
	data, err := json.Marshal(raw)
	if err != nil {
		return nil
	}
	var snapshot OpenAICodexStateProbeSnapshot
	if err := json.Unmarshal(data, &snapshot); err != nil {
		return nil
	}
	return &snapshot
}

func (s *AccountTestService) persistOpenAICodexStateProbeResult(
	ctx context.Context,
	account *Account,
	result *OpenAICodexStateProbeResult,
) error {
	if s == nil || s.accountRepo == nil || account == nil || result == nil {
		return nil
	}
	now := result.FinishedAt
	if now.IsZero() {
		now = time.Now().UTC()
	}
	snapshot := OpenAICodexStateProbeSnapshotFromAccount(account)
	if snapshot == nil || snapshot.Method != result.Method || snapshot.Route != result.Route || snapshot.Model != result.Model {
		snapshot = &OpenAICodexStateProbeSnapshot{}
	}
	if snapshot.History == nil {
		snapshot.History = make([]OpenAICodexStateProbeVerdict, 0, openAICodexStateProbeHistoryLimit)
	}

	snapshot.Method = result.Method
	snapshot.Route = result.Route
	snapshot.Answer = result.Answer
	snapshot.Model = result.Model
	snapshot.ReportedModel = result.ReportedModel
	snapshot.Verdict = result.Verdict
	snapshot.LastProbeAt = now.UTC()
	snapshot.NextProbeAt = now.UTC().Add(openAICodexStateProbeInterval)
	snapshot.Failure = result.Failure
	snapshot.Reason = result.Reason
	snapshot.Detail = result.Detail
	snapshot.TicketLength = result.TicketLength
	snapshot.ContinueTicketLength = result.ContinueTicketLength
	snapshot.NewTicket = result.NewTicket

	switch result.Verdict {
	case OpenAICodexStateHealthy:
		snapshot.Status = string(OpenAICodexStateHealthy)
		snapshot.NextProbeAt = now.UTC().Add(openAICodexStateProbeInterval)
		snapshot.History = append(snapshot.History, OpenAICodexStateProbeHistoryHealthy)
		snapshot.ConsecutiveHealthy++
		snapshot.ConsecutiveDegraded = 0
	case OpenAICodexStateDegraded:
		snapshot.Status = string(OpenAICodexStateDegraded)
		snapshot.NextProbeAt = now.UTC().Add(openAICodexStateProbeInterval)
		snapshot.History = append(snapshot.History, OpenAICodexStateProbeHistoryDegraded)
		snapshot.ConsecutiveDegraded++
		snapshot.ConsecutiveHealthy = 0
	default:
		snapshot.ConsecutiveHealthy = 0
		snapshot.ConsecutiveDegraded = 0
		if result.Failure == OpenAICodexStateFailureUnsupported || result.Failure == OpenAICodexStateFailureModelUnsupported {
			snapshot.Status = OpenAICodexStateFailureUnsupported
			snapshot.NextProbeAt = now.UTC().Add(openAICodexStateProbeUnsupportedInterval)
		} else {
			snapshot.Status = string(OpenAICodexStateInconclusive)
			snapshot.NextProbeAt = now.UTC().Add(openAICodexStateProbeInterval)
		}
	}

	if len(snapshot.History) > openAICodexStateProbeHistoryLimit {
		snapshot.History = snapshot.History[len(snapshot.History)-openAICodexStateProbeHistoryLimit:]
	}
	snapshot.HealthyCount = 0
	snapshot.DegradedCount = 0
	for _, verdict := range snapshot.History {
		switch verdict {
		case OpenAICodexStateProbeHistoryHealthy:
			snapshot.HealthyCount++
		case OpenAICodexStateProbeHistoryDegraded:
			snapshot.DegradedCount++
		}
	}
	snapshot.SampleCount = snapshot.HealthyCount + snapshot.DegradedCount
	if snapshot.SampleCount > 0 {
		rate := float64(snapshot.HealthyCount) / float64(snapshot.SampleCount) * 100
		snapshot.HealthRate = openAICodexStateProbeFloat64Ptr(math.Round(rate*10) / 10)
	} else {
		snapshot.HealthRate = nil
	}

	result.Snapshot = snapshot
	return s.accountRepo.UpdateExtra(ctx, account.ID, map[string]any{
		OpenAICodexStateProbeSnapshotExtraKey: snapshot,
	})
}

func openAICodexStateProbeFloat64Ptr(value float64) *float64 {
	return &value
}

func OpenAICodexStateProbeSnapshotStatus(snapshot *OpenAICodexStateProbeSnapshot) string {
	if snapshot == nil {
		return "unprobed"
	}
	status := strings.TrimSpace(snapshot.Status)
	if status == "" {
		return string(snapshot.Verdict)
	}
	return status
}
