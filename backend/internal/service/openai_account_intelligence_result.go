package service

import (
	"bufio"
	"bytes"
	"strings"
	"sync"
	"time"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
)

// Keep the verified V3 result and snapshot format while testing actual answers.
type OpenAICodexStateVerdict string

const (
	// OpenAICodexStateHealthy：当前通道正确回答推理题。
	OpenAICodexStateHealthy OpenAICodexStateVerdict = "healthy"
	// OpenAICodexStateDegraded：当前通道的完整答案未通过校验。
	OpenAICodexStateDegraded OpenAICodexStateVerdict = "degraded"
	// OpenAICodexStateInconclusive：请求没有完整跑通，本次无法判断。
	OpenAICodexStateInconclusive OpenAICodexStateVerdict = "inconclusive"
)

// 探针没跑通时的原因分类（OpenAICodexStateProbeResult.Failure）。
const (
	OpenAICodexStateFailureUnsupported      = "unsupported"
	OpenAICodexStateFailureAccountError     = "account_error"
	OpenAICodexStateFailureRateLimited      = "rate_limited"
	OpenAICodexStateFailureModelUnsupported = "model_unsupported"
	OpenAICodexStateFailureUpstreamError    = "upstream_error"
	OpenAICodexStateFailureNetworkError     = "network_error"
	OpenAICodexStateFailureStreamError      = "stream_error"
	OpenAICodexStateFailureNoTicket         = "no_ticket"
	OpenAICodexStateFailureCancelled        = "cancelled"
)

const (
	OpenAICodexStateProbeDefaultModel = "gpt-6-astra"

	openAICodexStateProbeMaxBody       = 1 << 20
	openAICodexStateProbeMaxDetailByte = 300
)

var ErrOpenAICodexStateProbeBusy = infraerrors.Conflict("STATE_PROBE_BUSY", "该账号已有一次探针在进行中，请稍后再试")

// OpenAICodexStateProbeResult 是一次实际通道智力检测的完整结果，可直接作为接口 JSON 返回。
type OpenAICodexStateProbeResult struct {
	Method    string                         `json:"method,omitempty"`
	Route     string                         `json:"route,omitempty"`
	Answer    string                         `json:"answer,omitempty"`
	Snapshot  *OpenAICodexStateProbeSnapshot `json:"snapshot,omitempty"`
	AccountID int64                          `json:"account_id"`
	Model     string                         `json:"model"`
	Verdict   OpenAICodexStateVerdict        `json:"verdict"`
	Reason    string                         `json:"reason"`
	Failure   string                         `json:"failure,omitempty"`
	// Detail 是脱敏截断后的上游报错原文，便于看出「模型不支持」之类的具体原因。
	Detail string `json:"detail,omitempty"`

	MintStatus     int  `json:"mint_status"`
	ContinueStatus int  `json:"continue_status"`
	Minted         bool `json:"minted"`
	NewTicket      bool `json:"new_ticket"`
	// Legacy diagnostic fields keep the V3 response shape; answer probes do not mint tickets.
	TicketLength         int    `json:"ticket_length"`
	ContinueTicketLength int    `json:"continue_ticket_length"`
	ReportedModel        string `json:"reported_model,omitempty"`

	LatencyMs  int64     `json:"latency_ms"`
	StartedAt  time.Time `json:"started_at"`
	FinishedAt time.Time `json:"finished_at"`
}

func (r *OpenAICodexStateProbeResult) fail(kind, reason, detail string) {
	r.Verdict = OpenAICodexStateInconclusive
	r.Failure = kind
	r.Reason = reason
	r.Detail = detail
}

func openAICodexStateDetail(body []byte) string {
	msg := strings.TrimSpace(extractUpstreamErrorMessage(body))
	if msg == "" {
		msg = strings.TrimSpace(string(body))
	}
	return SanitizeUpstreamErrorMessage(truncateString(msg, openAICodexStateProbeMaxDetailByte))
}

// openAICodexStateStreamEvents 按 SSE 事件切出每个 data 负载（跳过 [DONE]）。
func openAICodexStateStreamEvents(data []byte) [][]byte {
	var events [][]byte
	scanner := bufio.NewScanner(bytes.NewReader(data))
	scanner.Buffer(make([]byte, 4096), openAICodexStateProbeMaxBody)
	var lines []string
	flush := func() {
		if len(lines) == 0 {
			return
		}
		payload := strings.TrimSpace(strings.Join(lines, "\n"))
		lines = nil
		if payload != "" && payload != "[DONE]" {
			events = append(events, []byte(payload))
		}
	}
	for scanner.Scan() {
		line := strings.TrimSuffix(scanner.Text(), "\r")
		if line == "" {
			flush()
			continue
		}
		if strings.HasPrefix(line, "data:") {
			lines = append(lines, strings.TrimPrefix(strings.TrimPrefix(line, "data:"), " "))
		}
	}
	flush()
	return events
}

func (s *AccountTestService) beginOpenAICodexStateProbe(accountID int64) (func(), bool) {
	if _, loaded := s.stateProbeAccounts.LoadOrStore(accountID, struct{}{}); loaded {
		return nil, false
	}
	var once sync.Once
	return func() { once.Do(func() { s.stateProbeAccounts.Delete(accountID) }) }, true
}
