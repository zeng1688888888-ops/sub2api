package service

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	pkghttputil "github.com/Wei-Shaw/sub2api/internal/pkg/httputil"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

const AccountSVGProbeExtraKey = "account_svg_probe"
const accountSVGProbeSettingsKey = "account_svg_probe_settings"
const accountSVGProbeMaxBytes = 1 << 20
const accountSVGProbeTimeout = 5 * time.Minute

type AccountSVGProbeSettings struct {
	Model         string   `json:"model"`
	Prompts       []string `json:"prompts"`
	UseEgressPool bool     `json:"use_egress_pool"`
}
type AccountSVGProbeResult struct {
	ID          string    `json:"id"`
	Status      string    `json:"status"`
	Model       string    `json:"model"`
	Prompt      string    `json:"prompt"`
	PromptIndex int       `json:"prompt_index"`
	StartedAt   time.Time `json:"started_at"`
	FinishedAt  time.Time `json:"finished_at"`
	LatencyMS   int64     `json:"latency_ms"`
	Failure     string    `json:"failure,omitempty"`
	Message     string    `json:"message"`
	HasSVG      bool      `json:"has_svg"`
	MIMEType    string    `json:"mime_type,omitempty"`
	Route       string    `json:"route,omitempty"`
	SourceSaved bool      `json:"source_saved,omitempty"`
}

func defaultAccountSVGProbeSettings() *AccountSVGProbeSettings {
	return &AccountSVGProbeSettings{Model: "gpt-6.1-sol", Prompts: []string{
		"创建一个 HTML，内容是 SVG 绘制一个鹈鹕骑自行车的 2D 动画，你不需要任何测试，不要读取本地环境和缓存",
		"创建一个 HTML，内容是 SVG 绘制一个秦始皇开坦克的 2D 动画，你不需要任何测试，不要读取本地环境和缓存",
	}}
}
func validateAccountSVGProbeSettings(v *AccountSVGProbeSettings) error {
	v.Model = strings.TrimSpace(v.Model)
	if !regexp.MustCompile("^gpt-[A-Za-z0-9][A-Za-z0-9._-]{0,95}$").MatchString(v.Model) || isOpenAIImageModel(v.Model) {
		return infraerrors.BadRequest("INVALID_SVG_PROBE_MODEL", "请输入 GPT 文本模型名称，例如 gpt-6.1-sol")
	}
	if len(v.Prompts) < 1 || len(v.Prompts) > 30 {
		return infraerrors.BadRequest("INVALID_SVG_PROBE_PROMPTS", "请输入 1–30 条提示词")
	}
	for i, p := range v.Prompts {
		p = strings.TrimSpace(p)
		if p == "" || utf8.RuneCountInString(p) > 4000 {
			return infraerrors.BadRequest("INVALID_SVG_PROBE_PROMPTS", "每条提示词须为 1–4000 个字符")
		}
		v.Prompts[i] = p
	}
	return nil
}
func (s *AccountTestService) GetAccountSVGProbeSettings(ctx context.Context) (*AccountSVGProbeSettings, error) {
	if s == nil || s.settingService == nil || s.settingService.settingRepo == nil {
		return nil, errors.New("SVG probe settings unavailable")
	}
	raw, err := s.settingService.settingRepo.GetValue(ctx, accountSVGProbeSettingsKey)
	if errors.Is(err, ErrSettingNotFound) || err == nil && strings.TrimSpace(raw) == "" {
		return defaultAccountSVGProbeSettings(), nil
	}
	if err != nil {
		return nil, err
	}
	var v AccountSVGProbeSettings
	if err = json.Unmarshal([]byte(raw), &v); err != nil {
		return nil, err
	}
	if err = validateAccountSVGProbeSettings(&v); err != nil {
		return nil, err
	}
	return &v, nil
}
func (s *AccountTestService) SaveAccountSVGProbeSettings(ctx context.Context, v *AccountSVGProbeSettings) error {
	if err := validateAccountSVGProbeSettings(v); err != nil {
		return err
	}
	if s == nil || s.settingService == nil || s.settingService.settingRepo == nil {
		return errors.New("SVG probe settings unavailable")
	}
	data, err := json.Marshal(v)
	if err != nil {
		return err
	}
	return s.settingService.settingRepo.Set(ctx, accountSVGProbeSettingsKey, string(data))
}
func AccountSVGProbeFromAccount(a *Account) *AccountSVGProbeResult {
	if a == nil {
		return nil
	}
	raw, ok := a.Extra[AccountSVGProbeExtraKey]
	if !ok {
		return nil
	}
	data, err := json.Marshal(raw)
	if err != nil {
		return nil
	}
	var v AccountSVGProbeResult
	if json.Unmarshal(data, &v) != nil || v.ID == "" {
		return nil
	}
	return &v
}
func (s *AccountTestService) AccountSVGProbeRunning(id int64) bool {
	if s == nil {
		return false
	}
	_, ok := s.svgProbeAccounts.Load(id)
	return ok
}
func accountSVGProbeFile(accountID int64, id string, mime ...string) (string, error) {
	if accountID <= 0 {
		return "", errors.New("invalid account")
	}
	if _, err := uuid.Parse(id); err != nil {
		return "", err
	}
	base := os.Getenv("DATA_DIR")
	if base == "" {
		base = "."
	}
	extension := ".svg"
	if len(mime) > 0 && mime[0] == "text/html" {
		extension = ".html"
	}
	if len(mime) > 0 && mime[0] == "text/plain" {
		extension = ".txt"
	}
	return filepath.Join(base, "account-svg-probes", fmt.Sprintf("%d", accountID), id+extension), nil
}
func svgProbeFailure(err error) (string, string) {
	if strings.HasPrefix(err.Error(), "invalid_svg:") {
		return "invalid_svg", truncateString(err.Error(), 300)
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return "timeout", "鹈鹕测试超过5分钟，未返回完整动画；请稍后重试"
	}
	if errors.Is(err, context.Canceled) {
		return "cancelled", "SVG测试已取消"
	}
	if errors.Is(err, io.ErrUnexpectedEOF) || strings.Contains(strings.ToLower(err.Error()), "unexpected eof") || strings.Contains(strings.ToLower(err.Error()), "stream read error") {
		return "stream_interrupted", "上游响应未完成就断开连接，请检查测试使用的网络或代理后重试；这不是动画格式错误"
	}
	var upstream *UpstreamFailoverError
	if errors.As(err, &upstream) {
		if upstream.StatusCode == 429 {
			return "rate_limited", "上游限流，请稍后重试"
		}
		if upstream.StatusCode == 401 || upstream.StatusCode == 403 {
			return "access_denied", "上游认证或访问被拒绝"
		}
		if upstream.ClientMessage != "" {
			return "upstream_error", SanitizeUpstreamErrorMessage(upstream.ClientMessage)
		}
	}
	return "upstream_error", SanitizeUpstreamErrorMessage(truncateString(err.Error(), 300))
}
func (s *AccountTestService) ProbeAccountSVG(ctx context.Context, id int64) (*AccountSVGProbeResult, error) {
	return s.probeAccountSVG(ctx, id, false)
}

func (s *AccountTestService) probeAccountSVG(ctx context.Context, id int64, onlyDue bool) (*AccountSVGProbeResult, error) {
	if s == nil || s.accountRepo == nil || s.openaiGatewayService == nil {
		return nil, errors.New("SVG probe service unavailable")
	}
	a, err := s.accountRepo.GetByID(ctx, id)
	if err != nil {
		return nil, err
	}
	if a == nil {
		return nil, ErrAccountNotFound
	}
	if a.Platform != PlatformOpenAI || !a.IsOpenAIOAuthLike() || a.IsShadow() || a.IsSyntheticUITest() {
		return nil, infraerrors.BadRequest("SVG_PROBE_UNSUPPORTED", "SVG测试支持非影子的 OpenAI OAuth 账号")
	}
	if _, busy := s.svgProbeAccounts.LoadOrStore(id, struct{}{}); busy {
		return nil, infraerrors.Conflict("SVG_PROBE_BUSY", "该账号正在进行SVG测试")
	}
	defer s.svgProbeAccounts.Delete(id)
	if onlyDue {
		// Re-read after acquiring the per-account guard: a manual probe may just have finished.
		a, err = s.accountRepo.GetByID(ctx, id)
		if err != nil {
			return nil, err
		}
		if !accountSVGProbeDue(a, time.Now()) {
			return nil, nil
		}
	}
	s.svgProbeSlotsOnce.Do(func() { s.svgProbeSlots = make(chan struct{}, 2) })
	select {
	case s.svgProbeSlots <- struct{}{}:
		defer func() { <-s.svgProbeSlots }()
	default:
		return nil, infraerrors.Conflict("SVG_PROBE_BUSY", "已有两个SVG测试在运行，请稍后再试")
	}
	settings, err := s.GetAccountSVGProbeSettings(ctx)
	if err != nil {
		return nil, err
	}
	selected, err := rand.Int(rand.Reader, big.NewInt(int64(len(settings.Prompts))))
	if err != nil {
		return nil, err
	}
	index := int(selected.Int64())
	previous := AccountSVGProbeFromAccount(a)
	result := &AccountSVGProbeResult{ID: uuid.NewString(), Status: "failed", Model: settings.Model, Prompt: settings.Prompts[index], PromptIndex: index, StartedAt: time.Now().UTC()}
	probeCtx, cancel := context.WithTimeout(ctx, accountSVGProbeTimeout)
	defer cancel()
	result.Route = "account_default"
	if settings.UseEgressPool && a.HasEgressPool() {
		result.Route = "egress_pool"
	}
	data, mime, runErr := s.runAccountSVGProbe(probeCtx, a, settings.Model, result.Prompt, settings.UseEgressPool)
	if runErr == nil {
		path, pathErr := accountSVGProbeFile(id, result.ID, mime)
		if pathErr == nil {
			pathErr = os.MkdirAll(filepath.Dir(path), 0700)
		}
		if pathErr == nil {
			pathErr = os.WriteFile(path, data, 0600)
		}
		if pathErr != nil {
			runErr = errors.New("无法保存测试SVG")
		} else {
			result.Status = "generated"
			result.HasSVG = true
			result.MIMEType = mime
			result.Message = "鹈鹕测试动画已生成，请查看动作、构图和细节；未自动判定模型是否降智"
		}
	}
	if runErr != nil {
		result.Failure, result.Message = svgProbeFailure(runErr)
		if result.Failure == "invalid_svg" && len(data) > 0 && len(data) <= accountSVGProbeMaxBytes {
			if sourcePath, e := accountSVGProbeFile(id, result.ID, "text/plain"); e == nil {
				if e = os.MkdirAll(filepath.Dir(sourcePath), 0700); e == nil {
					result.SourceSaved = os.WriteFile(sourcePath, data, 0600) == nil
				}
			}
		}
	}
	result.FinishedAt = time.Now().UTC()
	result.LatencyMS = result.FinishedAt.Sub(result.StartedAt).Milliseconds()
	saveCtx, saveCancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer saveCancel()
	if err = s.accountRepo.UpdateExtra(saveCtx, id, map[string]any{AccountSVGProbeExtraKey: result}); err != nil {
		if result.HasSVG {
			if path, e := accountSVGProbeFile(id, result.ID, result.MIMEType); e == nil {
				_ = os.Remove(path)
			}
		}
		if result.SourceSaved {
			if sourcePath, e := accountSVGProbeFile(id, result.ID, "text/plain"); e == nil {
				_ = os.Remove(sourcePath)
			}
		}
		return nil, err
	}
	if previous != nil && previous.SourceSaved {
		if sourcePath, e := accountSVGProbeFile(id, previous.ID, "text/plain"); e == nil {
			_ = os.Remove(sourcePath)
		}
	}
	if prev := previous; prev != nil && prev.HasSVG && prev.ID != result.ID {
		if path, e := accountSVGProbeFile(id, prev.ID, prev.MIMEType); e == nil {
			_ = os.Remove(path)
		}
	}
	return result, nil
}
func (s *AccountTestService) runAccountSVGProbe(ctx context.Context, a *Account, model, prompt string, useEgressPool bool) ([]byte, string, error) {
	// Probe routing is explicit and request-local: ordinary account networking by default.
	// Never mutate the configured concurrency pool used by production requests.
	requestAccount := *a
	requestAccount.Extra = make(map[string]any, len(a.Extra)+1)
	for key, value := range a.Extra {
		requestAccount.Extra[key] = value
	}
	if !useEgressPool {
		delete(requestAccount.Extra, AccountEgressPoolExtraKey)
	}
	requestAccount.Extra["openai_ws_force_http"] = true
	a = &requestAccount
	gateway := s.openaiGatewayService
	if gateway.concurrencyService == nil {
		return nil, "", errors.New("并发服务不可用")
	}
	slot, err := gateway.concurrencyService.AcquireAccountSlot(ctx, a.ID, a.Concurrency)
	if err != nil {
		return nil, "", err
	}
	if !slot.Acquired {
		return nil, "", errors.New("账号并发已满，请稍后重试")
	}
	defer slot.ReleaseFunc()
	instructions := "请按用户要求直接编写一份完整的 HTML 文档，内嵌 SVG 绘制 2D 动画。只返回 HTML 源码，不要解释，不要 Markdown 围栏。用 SVG SMIL 或内嵌 CSS @keyframes 实现循环动画，不要 JavaScript、事件处理属性、foreignObject、iframe、外部图片、字体、链接或任何网络资源。SVG 设置 viewBox，画面自适应窗口；主体须有明显运动，不能只是静态插图。不要调用工具、执行测试、读取本地环境或缓存。\n\n用户要求：\n"
	body, err := json.Marshal(map[string]any{"model": model, "stream": true, "store": false, "reasoning": map[string]any{"effort": "high"}, "input": []any{map[string]any{"role": "user", "content": []any{map[string]any{"type": "input_text", "text": instructions + prompt}}}}})
	if err != nil {
		return nil, "", err
	}
	recorder := &boundedSVGProbeRecorder{ResponseRecorder: httptest.NewRecorder()}
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewReader(body)).WithContext(ctx)
	c.Request.Header.Set("Content-Type", "application/json")
	c.Request.Header.Set("Session-Id", "account-svg-probe-"+uuid.NewString())
	forwarded, err := gateway.Forward(pkghttputil.WithUpstreamCancellation(ctx), c, a, body)
	if err != nil {
		if ctx.Err() != nil {
			return nil, "", ctx.Err()
		}
		return nil, "", err
	}
	if recorder.overflow {
		return nil, "", errors.New("SVG 响应超出测试大小限制")
	}
	if forwarded == nil || forwarded.ClientDisconnect || ctx.Err() != nil {
		if ctx.Err() != nil {
			return nil, "", ctx.Err()
		}
		return nil, "", errors.New("SVG 响应中断")
	}
	if recorder.Code >= 400 {
		return nil, "", &UpstreamFailoverError{StatusCode: recorder.Code}
	}
	text, err := svgProbeResponseText(recorder.Body.Bytes())
	if err != nil {
		return nil, "", err
	}
	safe, err := sanitizeProbeHTML(text)
	if err != nil {
		return []byte(text), "text/plain", fmt.Errorf("invalid_svg: %w", err)
	}
	return safe, "text/html", nil
}
func svgProbeResponseText(body []byte) (string, error) {
	var delta, final strings.Builder
	completed := false
	for _, payload := range openAICodexStateStreamEvents(body) {
		var event struct {
			Type     string `json:"type"`
			Delta    string `json:"delta"`
			Response struct {
				Status string `json:"status"`
				Output []struct {
					Type    string `json:"type"`
					Content []struct {
						Type string `json:"type"`
						Text string `json:"text"`
					} `json:"content"`
				} `json:"output"`
			} `json:"response"`
		}
		if json.Unmarshal(payload, &event) != nil {
			continue
		}
		switch event.Type {
		case "response.output_text.delta":
			_, _ = delta.WriteString(event.Delta)
		case "response.completed", "response.done":
			if event.Response.Status != "" && event.Response.Status != "completed" {
				return "", errors.New("SVG 响应未完整完成")
			}
			completed = true
			final.Reset()
			for _, item := range event.Response.Output {
				if item.Type == "message" {
					for _, c := range item.Content {
						if c.Type == "output_text" {
							_, _ = final.WriteString(c.Text)
						}
					}
				}
			}
		case "response.failed", "response.incomplete", "error":
			return "", errors.New("SVG 响应在完成前失败")
		}
	}
	if !completed {
		return "", errors.New("SVG 响应缺少完成事件")
	}
	if final.Len() > 0 {
		return final.String(), nil
	}
	if delta.Len() > 0 {
		return delta.String(), nil
	}
	return "", errors.New("模型返回了空内容")
}

type boundedSVGProbeRecorder struct {
	*httptest.ResponseRecorder
	overflow bool
}

func (w *boundedSVGProbeRecorder) Write(b []byte) (int, error) {
	if w.overflow || w.Body.Len()+len(b) > 4<<20 {
		w.overflow = true
		return 0, io.ErrShortWrite
	}
	return w.ResponseRecorder.Write(b)
}
func (w *boundedSVGProbeRecorder) WriteString(s string) (int, error) { return w.Write([]byte(s)) }

func (s *AccountTestService) ReadAccountSVGProbe(ctx context.Context, id int64, resultID string) ([]byte, string, error) {
	a, err := s.accountRepo.GetByID(ctx, id)
	if err != nil {
		return nil, "", err
	}
	v := AccountSVGProbeFromAccount(a)
	if v == nil || !v.HasSVG || v.ID != resultID {
		return nil, "", infraerrors.NotFound("SVG_PROBE_NOT_FOUND", "SVG不存在或已被更新")
	}
	path, err := accountSVGProbeFile(id, v.ID, v.MIMEType)
	if err != nil {
		return nil, "", err
	}
	f, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, "", infraerrors.NotFound("SVG_PROBE_NOT_FOUND", "SVG文件不存在")
	}
	if err != nil {
		return nil, "", err
	}
	defer func() { _ = f.Close() }()
	data, err := io.ReadAll(io.LimitReader(f, accountSVGProbeMaxBytes+1))
	if err != nil {
		return nil, "", err
	}
	if len(data) > accountSVGProbeMaxBytes {
		return nil, "", errors.New("SVG文件过大")
	}
	return data, v.MIMEType, nil
}
