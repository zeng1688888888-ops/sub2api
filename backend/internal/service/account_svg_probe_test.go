//go:build unit

package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

type svgProbeSettingsRepo struct {
	SettingRepository
	values map[string]string
	err    error
}

func (r *svgProbeSettingsRepo) GetValue(_ context.Context, k string) (string, error) {
	if r.err != nil {
		return "", r.err
	}
	v, ok := r.values[k]
	if !ok {
		return "", ErrSettingNotFound
	}
	return v, nil
}
func (r *svgProbeSettingsRepo) Set(_ context.Context, k, v string) error { r.values[k] = v; return nil }
func TestSVGProbeSettingsPersistAndValidate(t *testing.T) {
	repo := &svgProbeSettingsRepo{values: map[string]string{}}
	svc := &AccountTestService{settingService: NewSettingService(repo, nil)}
	defaults, err := svc.GetAccountSVGProbeSettings(context.Background())
	require.NoError(t, err)
	require.Equal(t, "gpt-6.1-sol", defaults.Model)
	require.Empty(t, defaults.ReasoningEffort)
	require.False(t, defaults.UseEgressPool)
	require.Len(t, defaults.Prompts, 2)
	require.Contains(t, defaults.Prompts[0], "鹈鹕骑自行车")
	require.Contains(t, defaults.Prompts[1], "秦始皇开坦克")
	cfg := &AccountSVGProbeSettings{Model: " gpt-6-astra ", ReasoningEffort: " MAX ", Prompts: []string{" 绘制小鸡\n骑摩托车 ", "画猫"}}
	require.NoError(t, svc.SaveAccountSVGProbeSettings(context.Background(), cfg))
	got, err := svc.GetAccountSVGProbeSettings(context.Background())
	require.NoError(t, err)
	require.Equal(t, "绘制小鸡\n骑摩托车", got.Prompts[0])
	require.Equal(t, "gpt-6-astra", got.Model)
	require.Equal(t, "max", got.ReasoningEffort)
	for _, invalid := range []string{"", "auto", "minimal", "unknown"} {
		cfg.ReasoningEffort = invalid
		require.Error(t, svc.SaveAccountSVGProbeSettings(context.Background(), cfg))
	}
	cfg.Model, cfg.ReasoningEffort = "gpt-5.5", "ultra"
	require.Error(t, svc.SaveAccountSVGProbeSettings(context.Background(), cfg))
	got, err = svc.GetAccountSVGProbeSettings(context.Background())
	require.NoError(t, err)
	require.Equal(t, "max", got.ReasoningEffort)
	for _, cfg := range []*AccountSVGProbeSettings{{Model: "gpt-image-2", Prompts: []string{"x"}}, {Model: "gpt-6-astra", Prompts: nil}, {Model: "gpt-6-astra", Prompts: []string{" "}}, {Model: "gpt-6-astra", Prompts: []string{strings.Repeat("文", 4001)}}} {
		require.Error(t, validateAccountSVGProbeSettings(cfg))
	}
	repo.err = errors.New("database unavailable")
	_, err = svc.GetAccountSVGProbeSettings(context.Background())
	require.Error(t, err)
}

func TestSVGProbeLegacySettingsWaitForExplicitEffort(t *testing.T) {
	a := excelAccount()
	settings := &svgProbeSettingsRepo{values: map[string]string{
		accountSVGProbeSettingsKey: `{"model":"gpt-6.2-Astra","prompts":["保存的提示词"],"use_egress_pool":true}`,
	}}
	svc := &AccountTestService{accountRepo: &intelligenceProbeRepo{account: a}, settingService: NewSettingService(settings, nil), openaiGatewayService: &OpenAIGatewayService{}}
	config, err := svc.GetAccountSVGProbeSettings(context.Background())
	require.NoError(t, err)
	require.Empty(t, config.ReasoningEffort)
	require.Equal(t, []string{"保存的提示词"}, config.Prompts)
	require.True(t, config.UseEgressPool)
	_, err = svc.ProbeAccountSVG(context.Background(), a.ID)
	require.ErrorIs(t, err, errSVGProbeEffortRequired)
	result, err := svc.probeAccountSVG(context.Background(), a.ID, true)
	require.NoError(t, err)
	require.Nil(t, result)
	require.NotContains(t, a.Extra, AccountSVGProbeExtraKey)
}

func TestSVGProbeModelAndRouteEffortLimits(t *testing.T) {
	for model, want := range map[string][]string{
		"gpt-6.2-Astra-2026-11-17": {"low", "medium", "high", "xhigh", "max", "ultra"},
		"gpt-6.1-sol":              {"low", "medium", "high", "xhigh", "max"},
		"gpt-5.5":                  {"low", "medium", "high", "xhigh"},
		"gpt-4o":                   {"none"},
	} {
		got, err := AccountSVGProbeReasoningEfforts(model)
		require.NoError(t, err)
		require.Equal(t, want, got, model)
	}
	a := excelAccount()
	cfg := &AccountSVGProbeSettings{Model: "gpt-6.2-astra", ReasoningEffort: "max"}
	// An incompatible account must fail before spending quota at another effort.
	svc := &AccountTestService{}
	_, _, err := svc.runAccountSVGProbe(context.Background(), a, cfg, "draw")
	require.ErrorContains(t, err, "Excel BPS")
	a.Extra["openai_excel_bps"] = false
	a.Credentials["model_mapping"] = map[string]any{cfg.Model: "gpt-5.5"}
	_, _, err = svc.runAccountSVGProbe(context.Background(), a, cfg, "draw")
	require.ErrorContains(t, err, "gpt-5.5")
}

func svgProbeStream(t *testing.T, text string) string {
	t.Helper()
	event := map[string]any{"type": "response.completed", "response": map[string]any{"id": "resp_svg", "status": "completed", "model": "gpt-6.1-sol", "output": []any{map[string]any{"type": "message", "role": "assistant", "content": []any{map[string]any{"type": "output_text", "text": text}}}}}}
	data, err := json.Marshal(event)
	require.NoError(t, err)
	return "data: " + string(data) + "\n\n"
}
func TestSVGProbeUsesTextGatewayAndSavesLatest(t *testing.T) {
	t.Setenv("DATA_DIR", t.TempDir())
	account := excelAccount()
	account.Extra["openai_excel_bps"] = false
	repo := &intelligenceProbeRepo{account: account}
	upstream := &httpUpstreamRecorder{}
	gateway := openAIClientToolsTestService(upstream)
	slots := &stubConcurrencyCacheForTest{acquireResult: true}
	gateway.concurrencyService = NewConcurrencyService(slots)
	settings := &svgProbeSettingsRepo{values: map[string]string{}}
	svc := &AccountTestService{accountRepo: repo, openaiGatewayService: gateway, settingService: NewSettingService(settings, nil)}
	const svg = "<svg xmlns=\"http://www.w3.org/2000/svg\" viewBox=\"0 0 960 640\"><rect width=\"960\" height=\"640\" fill=\"#fff\"/><text x=\"20\" y=\"40\">鸡不可失，马上出发</text></svg>"
	cfg := defaultAccountSVGProbeSettings()
	cfg.Model = "gpt-6.2-Astra"
	levels, err := AccountSVGProbeReasoningEfforts(cfg.Model)
	require.NoError(t, err)
	require.Equal(t, []string{"low", "medium", "high", "xhigh", "max", "ultra"}, levels)
	var previousPath string
	for i, effort := range levels {
		cfg.ReasoningEffort = effort
		require.NoError(t, svc.SaveAccountSVGProbeSettings(context.Background(), cfg))
		upstream.resp = &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{"text/event-stream"}}, Body: io.NopCloser(strings.NewReader(svgProbeStream(t, svg)))}
		result, err := svc.probeAccountSVG(context.Background(), account.ID, i == 0)
		require.NoError(t, err)
		require.Equal(t, "generated", result.Status)
		require.True(t, result.HasSVG)
		require.Equal(t, cfg.Model, result.Model)
		require.Equal(t, effort, result.ReasoningEffort)
		require.Equal(t, effort, AccountSVGProbeFromAccount(account).ReasoningEffort)
		require.Equal(t, cfg.Prompts[result.PromptIndex], result.Prompt)
		require.Contains(t, string(upstream.lastBody), result.Prompt)
		require.Contains(t, string(upstream.lastBody), "SVG")
		require.Equal(t, strings.ToLower(cfg.Model), strings.ToLower(gjson.GetBytes(upstream.lastBody, "model").String()))
		require.Contains(t, upstream.lastReq.URL.Path, "responses")
		wireEffort := gjson.GetBytes(upstream.lastBody, "reasoning.effort").String()
		if effort == "ultra" {
			require.Equal(t, "xhigh", wireEffort)
			require.True(t, gjson.GetBytes(upstream.lastBody, "multi_agent.enabled").Bool())
			require.Contains(t, upstream.lastReq.Header.Get("OpenAI-Beta"), "responses-multi-agent=v1")
		} else {
			require.Equal(t, effort, wireEffort)
			require.False(t, gjson.GetBytes(upstream.lastBody, "multi_agent").Exists())
		}
		data, mime, err := svc.ReadAccountSVGProbe(context.Background(), account.ID, result.ID)
		require.NoError(t, err)
		require.Equal(t, "text/html", mime)
		require.Contains(t, string(data), "Content-Security-Policy")
		require.Contains(t, string(data), "鸡不可失")
		if previousPath != "" {
			_, err = os.Stat(previousPath)
			require.True(t, os.IsNotExist(err))
		}
		previousPath, err = accountSVGProbeFile(account.ID, result.ID, result.MIMEType)
		require.NoError(t, err)
		_, _, err = svc.ReadAccountSVGProbe(context.Background(), account.ID, "../../outside")
		require.Error(t, err)
	}
	require.Len(t, slots.releasedAccountIDs, len(levels))
	require.False(t, svc.AccountSVGProbeRunning(account.ID))
}
func TestSVGProbeFailureClassificationAndIncompleteStream(t *testing.T) {
	for _, tc := range []struct {
		err  error
		code string
	}{{context.DeadlineExceeded, "timeout"}, {context.Canceled, "cancelled"}, {&UpstreamFailoverError{StatusCode: 429}, "rate_limited"}, {&UpstreamFailoverError{StatusCode: 403}, "access_denied"}, {errors.New("invalid_svg: invalid XML"), "invalid_svg"}, {fmt.Errorf("stream read error: %w", io.ErrUnexpectedEOF), "stream_interrupted"}} {
		code, _ := svgProbeFailure(tc.err)
		require.Equal(t, tc.code, code)
	}
	_, err := svgProbeResponseText([]byte("data: {\"type\":\"response.output_text.delta\",\"delta\":\"<svg/>\"}\n\n"), false)
	require.Error(t, err)
	_, err = svgProbeResponseText([]byte("data: {\"type\":\"response.incomplete\"}\n\n"), false)
	require.Error(t, err)
}

func TestSVGProbeUltraSelectsRootFinalArtwork(t *testing.T) {
	const deltas = "data: {\"type\":\"response.output_text.delta\",\"delta\":\"child draft\"}\n\n"
	const done = `data: {"type":"response.completed","response":{"status":"completed","output":[{"type":"message","agent":{"agent_name":"/root/artist"},"content":[{"type":"output_text","text":"child draft"}]},{"type":"message","phase":"commentary","agent":{"agent_name":"/root"},"content":[{"type":"output_text","text":"working"}]},{"type":"message","phase":"final_answer","agent":{"agent_name":"/root"},"content":[{"type":"output_text","text":"<svg/>"}]}]}}`
	text, err := svgProbeResponseText([]byte(deltas+done+"\n\n"), true)
	require.NoError(t, err)
	require.Equal(t, "<svg/>", text)
	_, err = svgProbeResponseText([]byte(deltas+"data: {\"type\":\"response.completed\",\"response\":{\"output\":[]}}\n\n"), true)
	require.Error(t, err)
}
func TestSVGProbePerAccountLock(t *testing.T) {
	a := excelAccount()
	svc := &AccountTestService{accountRepo: &intelligenceProbeRepo{account: a}, openaiGatewayService: &OpenAIGatewayService{}}
	svc.svgProbeAccounts.Store(a.ID, struct{}{})
	require.True(t, svc.AccountSVGProbeRunning(a.ID))
	_, err := svc.ProbeAccountSVG(context.Background(), a.ID)
	require.Error(t, err)
	require.Contains(t, err.Error(), "正在进行")
}
func TestPelicanHTMLPreservesCSSAndSVGAnimation(t *testing.T) {
	raw := "<!doctype html><html><head><meta charset=\"utf-8\"><style>@keyframes ride { to { transform: translateX(20px); } } .bike { animation: ride 2s infinite; }</style></head><body><svg viewBox=\"0 0 960 640\"><g class=\"bike\"><circle r=\"10\"><animateTransform attributeName=\"transform\" type=\"rotate\" from=\"0\" to=\"360\" dur=\"2s\" repeatCount=\"indefinite\"/></circle></g></svg></body></html>"
	data, err := sanitizeProbeHTML(raw)
	require.NoError(t, err)
	require.Contains(t, string(data), "@keyframes ride")
	require.Contains(t, string(data), "animateTransform")
	require.Contains(t, string(data), "repeatCount=\"indefinite\"")
	require.Contains(t, string(data), "attributeName=\"transform\"")
	require.Contains(t, string(data), "Content-Security-Policy")
	require.Contains(t, string(data), "script-src")
}
func TestPelicanHTMLRejectsExecutableOrExternalContent(t *testing.T) {
	for _, body := range []string{
		"<svg><script>alert(1)</script></svg>",
		"<svg onload=\"alert(1)\"></svg>",
		"<iframe src=\"https://example.com\"></iframe><svg></svg>",
		"<svg><foreignObject><div>bad</div></foreignObject></svg>",
		"<svg><use href=\"https://example.com/x.svg#x\"></use></svg>",
		"<svg><animate attributeName=\"href\" values=\"javascript:alert(1)\"></animate></svg>",
		"<style>@import 'https://example.com/x.css';</style><svg></svg>",
		"<style>.x{background:url(https://example.com)}</style><svg></svg>",
		"<p>No SVG</p>",
	} {
		_, err := sanitizeProbeHTML("<!doctype html><html><head></head><body>" + body + "</body></html>")
		require.Error(t, err, body)
	}
}

func TestSVGProbeRoutesDoNotMutateAccountPool(t *testing.T) {
	for _, tc := range []struct {
		name   string
		pool   bool
		single bool
		want   string
	}{
		{"ordinary_direct", false, false, ""}, {"ordinary_single_proxy", false, true, "http://ordinary.example:8080"}, {"explicit_pool", true, false, "http://pool.example:8080"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			account := excelAccount()
			account.Extra["openai_excel_bps"] = false
			account.Extra[AccountEgressPoolExtraKey] = []AccountEgressLane{{ProxyID: 2, Concurrency: 20}}
			if tc.single {
				id := int64(9)
				account.ProxyID = &id
				account.Proxy = &Proxy{ID: id, Protocol: "http", Host: "ordinary.example", Port: 8080}
			}
			upstream := &httpUpstreamRecorder{resp: &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{"text/event-stream"}}, Body: io.NopCloser(strings.NewReader(svgProbeStream(t, "<svg viewBox=\"0 0 100 100\"><circle r=\"10\"/></svg>")))}}
			gateway := openAIClientToolsTestService(upstream)
			slots := &stubConcurrencyCacheForTest{acquireResult: true}
			cache := &egressCacheStub{ConcurrencyCache: slots, selected: 2}
			gateway.concurrencyService = NewConcurrencyService(cache)
			gateway.concurrencyService.egressProxyRepo = &egressProxyRepoStub{proxies: []Proxy{{ID: 2, Protocol: "http", Host: "pool.example", Port: 8080, Status: StatusActive}}}
			service := &AccountTestService{openaiGatewayService: gateway}
			cfg := &AccountSVGProbeSettings{Model: "gpt-6.1-sol", ReasoningEffort: "medium", UseEgressPool: tc.pool}
			_, mime, err := service.runAccountSVGProbe(context.Background(), account, cfg, "画动画")
			require.NoError(t, err)
			require.Equal(t, "text/html", mime)
			require.Equal(t, tc.want, upstream.lastProxyURL)
			require.True(t, account.HasEgressPool())
			require.NotContains(t, account.Extra, "openai_ws_force_http")
			if tc.pool {
				require.Equal(t, 1, cache.acquireCalls)
				require.Equal(t, 1, cache.releaseCalls)
			} else {
				require.Zero(t, cache.acquireCalls)
			}
			require.Len(t, slots.releasedAccountIDs, 1)
		})
	}
}

func TestPelicanVisibilityAnimationIsAllowed(t *testing.T) {
	data, err := sanitizeProbeHTML("<html><body><svg><g><animate attributeName=\"visibility\" values=\"visible;hidden;visible\" dur=\"2s\" repeatCount=\"indefinite\"/></g></svg></body></html>")
	require.NoError(t, err)
	require.Contains(t, string(data), "attributeName=\"visibility\"")
	_, err = sanitizeProbeHTML("<svg><animate attributeName=\"href\" values=\"#x;#y\"/></svg>")
	require.Error(t, err)
}

func TestPelicanNormalizesMetadataWithoutDiscardingArtwork(t *testing.T) {
	raw := "<!doctype html><html><head><meta charset=\"gbk\"><meta name=\"description\" content=\"pelican\"><meta name=\"author\" content=\"model\"><meta name=\"color-scheme\" content=\"light dark\"><meta property=\"og:title\" content=\"animation\"><meta http-equiv=\"refresh\" content=\"0;url=https://example.com\"><meta http-equiv=\"Content-Security-Policy\" content=\"script-src *\"></head><body><svg viewBox=\"0 0 100 100\"><text>鹈鹕</text><circle r=\"10\"><animate attributeName=\"r\" values=\"10;20;10\" dur=\"2s\" repeatCount=\"indefinite\"/></circle></svg></body></html>"
	data, err := sanitizeProbeHTML(raw)
	require.NoError(t, err)
	clean := string(data)
	require.Contains(t, clean, "鹈鹕")
	require.Contains(t, clean, "<animate")
	require.Contains(t, clean, "charset=\"utf-8\"")
	require.Contains(t, clean, "width=device-width, initial-scale=1")
	require.Contains(t, clean, "Content-Security-Policy")
	require.NotContains(t, clean, "script-src *")
	require.NotContains(t, clean, "refresh")
	require.NotContains(t, clean, "https://example.com")
	require.NotContains(t, clean, "gbk")
	require.Equal(t, 3, strings.Count(clean, "<meta "))
}

func TestPelicanSupportsAllDrawingAnimationTargets(t *testing.T) {
	raw := "<html><head><meta name=\"color-scheme\" content=\"light\"></head><body><svg><defs><pattern id=\"road\" width=\"20\" height=\"20\" patternUnits=\"userSpaceOnUse\"><animateTransform attributeName=\"patternTransform\" type=\"translate\" from=\"0 0\" to=\"20 0\" dur=\"1s\" repeatCount=\"indefinite\"/><path d=\"M0 0L20 20\"/></pattern><linearGradient id=\"light\"><animateTransform attributeName=\"gradientTransform\" type=\"rotate\" from=\"0\" to=\"360\" dur=\"2s\"/></linearGradient></defs><rect width=\"100\" height=\"100\" fill=\"url(#road)\"><animate attributeName=\"fill\" values=\"url(#road);url(#light)\" dur=\"2s\"/></rect></svg></body></html>"
	clean, err := sanitizeProbeHTML(raw)
	require.NoError(t, err)
	require.Contains(t, string(clean), "patternTransform")
	require.Contains(t, string(clean), "gradientTransform")
	for _, attr := range []string{"href", "xlink:href", "onclick", "style", "id"} {
		_, err := sanitizeProbeHTML("<svg><animate attributeName=\"" + attr + "\" values=\"x;y\"/></svg>")
		require.Error(t, err, attr)
	}
}

func TestPelicanFailedSourceIsPrivateAndReplaced(t *testing.T) {
	t.Setenv("DATA_DIR", t.TempDir())
	account := excelAccount()
	account.Extra["openai_excel_bps"] = false
	upstream := &httpUpstreamRecorder{}
	gateway := openAIClientToolsTestService(upstream)
	gateway.concurrencyService = NewConcurrencyService(&stubConcurrencyCacheForTest{acquireResult: true})
	svc := &AccountTestService{accountRepo: &intelligenceProbeRepo{account: account}, openaiGatewayService: gateway, settingService: NewSettingService(&svgProbeSettingsRepo{values: map[string]string{}}, nil)}
	cfg := defaultAccountSVGProbeSettings()
	cfg.ReasoningEffort = "high"
	require.NoError(t, svc.SaveAccountSVGProbeSettings(context.Background(), cfg))
	setResponse := func(text string) {
		upstream.resp = &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{"text/event-stream"}}, Body: io.NopCloser(strings.NewReader(svgProbeStream(t, text)))}
	}
	setResponse("<svg><script>alert(1)</script></svg>")
	failed, err := svc.ProbeAccountSVG(context.Background(), account.ID)
	require.NoError(t, err)
	require.True(t, failed.SourceSaved)
	require.False(t, failed.HasSVG)
	sourcePath, err := accountSVGProbeFile(account.ID, failed.ID, "text/plain")
	require.NoError(t, err)
	data, err := os.ReadFile(sourcePath)
	require.NoError(t, err)
	require.Contains(t, string(data), "<script>")
	_, _, err = svc.ReadAccountSVGProbe(context.Background(), account.ID, failed.ID)
	require.Error(t, err)
	setResponse("<svg><circle r=\"5\"/></svg>")
	success, err := svc.ProbeAccountSVG(context.Background(), account.ID)
	require.NoError(t, err)
	require.True(t, success.HasSVG)
	_, err = os.Stat(sourcePath)
	require.True(t, os.IsNotExist(err))
}
