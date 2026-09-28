package service

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
)

const openAIIntelligenceProbeMethod = "candy"

func openAIIntelligenceProbeRoute(account *Account, model string) string {
	if account != nil && account.IsExcelBPSEnabledForModel(model) {
		return "bps"
	}
	return "codex"
}

// ProbeOpenAIAccountIntelligence grades the built-in reasoning question on the
// account's configured request route. Ticket rotation only describes native
// Codex and must never stand in for an answer from the BPS route.
func (s *AccountTestService) ProbeOpenAIAccountIntelligence(ctx context.Context, accountID int64, model string) (*OpenAICodexStateProbeResult, error) {
	if s == nil || s.accountRepo == nil {
		return nil, errors.New("account test service unavailable")
	}
	account, err := s.accountRepo.GetByID(ctx, accountID)
	if err != nil {
		return nil, err
	}
	if account == nil {
		return nil, ErrAccountNotFound
	}
	release, ok := s.beginOpenAICodexStateProbe(accountID)
	if !ok {
		return nil, ErrOpenAICodexStateProbeBusy
	}
	defer release()
	model = strings.TrimSpace(model)
	if model == "" {
		model = OpenAICodexStateProbeDefaultModel
	}
	result := &OpenAICodexStateProbeResult{
		AccountID: accountID, Model: model, Method: openAIIntelligenceProbeMethod,
		Route:   openAIIntelligenceProbeRoute(account, model),
		Verdict: OpenAICodexStateInconclusive, StartedAt: time.Now(),
	}
	if reason := openAIIntelligenceProbeUnsupportedReason(account); reason != "" {
		result.fail(OpenAICodexStateFailureUnsupported, reason, "")
	} else {
		probeCtx, cancel := context.WithTimeout(ctx, 90*time.Second)
		defer cancel()
		sample, sampleErr := s.runOpenAIIntelligenceQuestion(probeCtx, account, model)
		switch {
		case sampleErr != nil:
			result.fail(OpenAICodexStateFailureUpstreamError, "智力测试请求未完成，本次无法判断", openAICodexStateDetail([]byte(sampleErr.Error())))
		case sample == nil:
			result.fail(OpenAICodexStateFailureStreamError, "智力测试未返回结果，本次无法判断", "")
		case sample.Status == "success" && CandyAnswerCorrect(sample.ResponseText):
			result.Verdict = OpenAICodexStateHealthy
			result.Reason = "当前通道已正确回答内置糖果推理题，本次智力测试通过。单题结果仅供参考。"
		case strings.HasPrefix(sample.ErrorMessage, "answer_mismatch:"):
			result.Verdict = OpenAICodexStateDegraded
			result.Reason = "当前通道未正确回答内置糖果推理题，本次智力测试未通过；建议复测，不能据此确认模型被降级。"
		default:
			result.fail(OpenAICodexStateFailureUpstreamError, "智力测试请求未完成，本次无法判断，不计为降智", openAICodexStateDetail([]byte(sample.ErrorMessage)))
		}
		if probeCtx.Err() != nil {
			result.fail(OpenAICodexStateFailureCancelled, "智力测试被取消或超时，本次无法判断", "")
		}
		if sample != nil {
			result.Answer = truncateString(sample.ResponseText, openAICodexStateProbeMaxDetailByte)
		}
	}
	result.FinishedAt = time.Now()
	result.LatencyMs = result.FinishedAt.Sub(result.StartedAt).Milliseconds()
	if err := s.persistOpenAICodexStateProbeResult(ctx, account, result); err != nil {
		logger.LegacyPrintf("service.openai_account_intelligence_probe", "persist_failed: account_id=%d err=%v", accountID, err)
	}
	return result, nil
}
