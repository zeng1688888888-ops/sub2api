package service

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/tidwall/gjson"
)

type openAIIntelligenceSample struct {
	Status       string
	ResponseText string
	ErrorMessage string
}

func openAIIntelligenceProbeUnsupportedReason(account *Account) string {
	switch {
	case account == nil:
		return "账号不存在"
	case !account.IsOpenAIOAuthLike():
		return "智力检测只适用于 OpenAI ChatGPT 订阅（OAuth）账号"
	case account.IsSyntheticUITest():
		return "测试数据账号不向上游发真实请求"
	case account.IsOpenAIAgentIdentity():
		return "当前智力检测不支持 Agent Identity 账号"
	}
	return ""
}

// The target repository has no scheduled question framework. Capture the
// production gateway response directly, retaining the verified V3 question,
// reasoning effort, routing, completion checks and answer grading.
func (s *AccountTestService) runOpenAIIntelligenceQuestion(ctx context.Context, account *Account, model string) (*openAIIntelligenceSample, error) {
	if s.openaiGatewayService == nil {
		return nil, errors.New("OpenAI gateway unavailable")
	}
	body, err := json.Marshal(map[string]any{
		"model": model, "stream": true, "store": false,
		"input": []any{map[string]any{"type": "message", "role": "user", "content": []any{
			map[string]any{"type": "input_text", "text": CandyPrompt + "\n\n只输出最终整数，不要解释。"},
		}}},
		"reasoning": map[string]any{"effort": "high"},
	})
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	recorder := &openAIIntelligenceRecorder{ResponseRecorder: httptest.NewRecorder(), cancel: cancel}
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewReader(body)).WithContext(ctx)
	c.Request.Header.Set("Content-Type", "application/json")
	c.Request.Header.Set("Session-Id", "account-intelligence-"+uuid.NewString())
	forwarded, err := s.openaiGatewayService.Forward(ctx, c, account, body)
	if err != nil {
		return nil, err
	}
	if recorder.overflow {
		return nil, errors.New("intelligence response exceeds 4 MiB capture limit")
	}
	if forwarded == nil || forwarded.ClientDisconnect || ctx.Err() != nil {
		return nil, errors.New("intelligence response was interrupted")
	}

	var answer, completedAnswer strings.Builder
	completed := false
	for _, payload := range openAICodexStateStreamEvents(recorder.Body.Bytes()) {
		switch gjson.GetBytes(payload, "type").String() {
		case "response.output_text.delta":
			_, _ = answer.WriteString(gjson.GetBytes(payload, "delta").String())
		case "response.completed", "response.done":
			status := gjson.GetBytes(payload, "response.status").String()
			if status != "" && status != "completed" {
				return nil, errors.New("intelligence response did not complete successfully")
			}
			completed = true
			completedAnswer.Reset()
			for _, item := range gjson.GetBytes(payload, "response.output").Array() {
				if item.Get("type").String() != "message" {
					continue
				}
				for _, content := range item.Get("content").Array() {
					if content.Get("type").String() == "output_text" {
						_, _ = completedAnswer.WriteString(content.Get("text").String())
					}
				}
			}
		case "response.failed", "response.incomplete", "error":
			return nil, errors.New("intelligence response failed before completion")
		}
	}
	if !completed {
		return nil, errors.New("intelligence response ended before completion")
	}
	output := answer.String()
	if completedAnswer.Len() > 0 {
		output = completedAnswer.String()
	}
	if strings.TrimSpace(output) == "" {
		return nil, errors.New("intelligence response returned empty output")
	}
	result := &openAIIntelligenceSample{Status: "success", ResponseText: output}
	if !CandyAnswerCorrect(output) {
		result.Status = "failed"
		result.ErrorMessage = "answer_mismatch: expected 21"
	}
	return result, nil
}

type openAIIntelligenceRecorder struct {
	*httptest.ResponseRecorder
	cancel   context.CancelFunc
	overflow bool
}

func (w *openAIIntelligenceRecorder) Write(data []byte) (int, error) {
	if w.overflow || w.Body.Len()+len(data) > 4<<20 {
		w.overflow = true
		w.cancel()
		return 0, io.ErrShortWrite
	}
	return w.ResponseRecorder.Write(data)
}

func (w *openAIIntelligenceRecorder) WriteString(data string) (int, error) {
	return w.Write([]byte(data))
}
