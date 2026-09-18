package service

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/Wei-Shaw/sub2api/internal/pkg/openai"
)

// codexTurnStateProbeResult is the outcome of one collection request.
type codexTurnStateProbeResult struct {
	State      string
	HTTPStatus int
	LatencyMS  int64
	Revoked    bool
	Err        string
	RetryAfter time.Duration
	Attempted  bool
}

// codexTurnStateProbeBodyLimit bounds how much of a state-bearing status body is
// inspected. Such a body is a small JSON document, never a stream.
const codexTurnStateProbeBodyLimit = 64 << 10

// probeCodexState performs one collection call for an account.
//
// It reuses the exact request shape the gateway and the account test use, so the
// upstream sees an ordinary Codex client rather than a bespoke probe: the same
// canonical identity, the same account headers, the same account-level header
// overrides, and the account's own payload conventions.
//
// Only the response headers are consumed. The body is an SSE stream and closing
// it immediately after the headers is what keeps a collection cheap: upstream has
// already decided on and emitted the state by then.
func (s *CodexTurnStateService) probeCodexState(ctx context.Context, account *Account, cfg CodexTurnStateConfig, proxyURL string) codexTurnStateProbeResult {
	if s == nil || s.upstream == nil {
		return codexTurnStateProbeResult{Err: "上游通道不可用"}
	}
	if account == nil {
		return codexTurnStateProbeResult{Err: "账号不存在"}
	}

	// A credential shadow account signs with its母账号 credentials, exactly like
	// real traffic; without this the probe would 401 while the gateway works.
	credentialAccount := account
	if account.IsCredentialShadow() && s.accountRepo != nil {
		resolved, err := resolveCredentialAccount(ctx, s.accountRepo, account)
		if err != nil {
			return codexTurnStateProbeResult{Err: "解析凭据账号失败：" + err.Error()}
		}
		credentialAccount = resolved
	}
	if !credentialAccount.IsOAuth() && !credentialAccount.IsOpenAIAgentIdentity() {
		return codexTurnStateProbeResult{Err: "仅支持 OpenAI OAuth / Setup Token 账号"}
	}

	token := ""
	if !credentialAccount.IsOpenAIAgentIdentity() {
		token = credentialAccount.GetOpenAIAccessToken()
		if strings.TrimSpace(token) == "" {
			return codexTurnStateProbeResult{Err: "账号缺少 access_token"}
		}
	}

	model := normalizeOpenAIModelForUpstream(credentialAccount, resolveIntelligentTestModel(credentialAccount, cfg.Model))
	payload := buildCodexTurnStateProbePayload(model, cfg.Prompt)
	body, err := json.Marshal(payload)
	if err != nil {
		return codexTurnStateProbeResult{Err: "构造请求体失败：" + err.Error()}
	}

	timeout := time.Duration(cfg.ProbeTimeoutSeconds) * time.Second
	if timeout <= 0 {
		timeout = time.Duration(CodexTurnStateDefaultProbeTimeoutSeconds) * time.Second
	}
	reqCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	req, err := http.NewRequestWithContext(reqCtx, http.MethodPost, chatgptCodexURL, bytes.NewReader(body))
	if err != nil {
		return codexTurnStateProbeResult{Err: "构造请求失败：" + err.Error()}
	}
	req = req.WithContext(WithHTTPUpstreamProfile(req.Context(), HTTPUpstreamProfileOpenAI))
	req.Host = "chatgpt.com"
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("accept", "text/event-stream")
	req.Header.Set("OpenAI-Beta", "responses=experimental")

	if credentialAccount.IsOpenAIAgentIdentity() {
		authHeaders, authErr := buildAgentIdentityAuthenticationHeaders(reqCtx, s.accountRepo, nil, &s.agentIdentityMu, credentialAccount)
		if authErr != nil {
			return codexTurnStateProbeResult{Err: "构造 Agent Identity 认证失败：" + authErr.Error()}
		}
		for key, values := range authHeaders {
			for _, value := range values {
				req.Header.Add(key, value)
			}
		}
	} else {
		req.Header.Set("Authorization", "Bearer "+token)
	}

	// Identity: the same canonical triple the gateway sends, plus the
	// engine-fingerprint headers the deployment requires on this upstream.
	canonical := resolveCodexOutboundIdentity("")
	req.Header.Set("Originator", canonical.originator)
	req.Header.Set("version", canonical.version)
	if customUA := strings.TrimSpace(credentialAccount.GetOpenAIUserAgent()); customUA != "" {
		req.Header.Set("User-Agent", customUA)
	} else {
		req.Header.Set("User-Agent", canonical.userAgent)
	}
	applyOpenAICodexProbeHeaders(req.Header)
	enforceCodexIdentityHeadersWithUA(req.Header, credentialAccount.GetOpenAIUserAgent())
	setOpenAIChatGPTAccountHeaders(req.Header, credentialAccount)
	credentialAccount.ApplyHeaderOverrides(req.Header)
	// Collection must be cold even when account-level overrides carry a state.
	for name := range req.Header {
		if strings.EqualFold(name, cfg.InjectHeader) || strings.EqualFold(name, CodexTurnStateDefaultHeader) {
			delete(req.Header, name)
		}
	}

	started := time.Now()
	var resp *http.Response
	if cfg.HarvestTransport == "independent" {
		if cfg.ProxyID <= 0 || strings.TrimSpace(proxyURL) == "" {
			return codexTurnStateProbeResult{Err: "独立采集代理未配置"}
		}
		req.Close = true
		req = req.WithContext(WithHTTPUpstreamRedirectsDisabled(WithHTTPUpstreamProfile(req.Context(), HTTPUpstreamProfileOpenAIHarvest)))
		req = WithAccountTrafficRequest(req, account)
		resp, err = accountTrafficController(s.upstream).DoHTTP(req, func(controlled *http.Request) (*http.Response, error) {
			return s.upstream.Do(controlled, proxyURL, account.ID, account.Mode1EffectiveConcurrency())
		})
	} else {
		resp, err = s.sendProbe(req, proxyURL, account)
	}
	latency := time.Since(started).Milliseconds()
	if err != nil {
		return codexTurnStateProbeResult{Attempted: true, LatencyMS: latency, Err: "请求失败：" + err.Error()}
	}
	if resp == nil {
		return codexTurnStateProbeResult{Attempted: true, LatencyMS: latency, Err: "上游返回空响应"}
	}
	defer func() {
		if resp.Body != nil {
			_ = resp.Body.Close()
		}
	}()

	result := codexTurnStateProbeResult{HTTPStatus: resp.StatusCode, LatencyMS: latency, Attempted: true}
	if raw := strings.TrimSpace(resp.Header.Get("Retry-After")); raw != "" {
		if seconds, err := strconv.ParseInt(raw, 10, 32); err == nil && seconds > 0 {
			result.RetryAfter = time.Duration(seconds) * time.Second
		} else if deadline, err := http.ParseTime(raw); err == nil {
			result.RetryAfter = time.Until(deadline)
		}
	}

	// A revocation status invalidates whatever state the account holds.
	if isStatusIn(resp.StatusCode, cfg.RevocationStatuses) {
		result.Revoked = true
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
		return result
	}

	if state := strings.TrimSpace(resp.Header.Get(cfg.InjectHeader)); state != "" {
		// A status the operator excluded may not introduce a state, even though
		// it carries one: that is the strict "only 292 counts" mode.
		if !cfg.AcceptsIssuanceStatus(resp.StatusCode) {
			return result
		}
		result.State = state
		return result
	}

	// The write-up describes the state arriving inside the body of a
	// non-standard success status. That shape cannot be read off a normal SSE
	// stream, but the body of such a status is a small JSON document, so it is
	// safe to inspect here.
	if resp.StatusCode == CodexTurnStateDefaultStateStatus {
		raw, readErr := io.ReadAll(io.LimitReader(resp.Body, codexTurnStateProbeBodyLimit))
		if readErr != nil {
			result.Err = "读取响应体失败：" + readErr.Error()
			return result
		}
		if state := extractCodexTurnStateField(raw); state != "" {
			result.State = state
			return result
		}
		return result
	}

	if resp.StatusCode >= http.StatusBadRequest {
		snippet, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		message := strings.TrimSpace(string(snippet))
		if message == "" {
			message = http.StatusText(resp.StatusCode)
		}
		result.Err = "上游返回 " + strconv.Itoa(resp.StatusCode) + " " + httpStatusText(resp.StatusCode) + "：" + message
		return result
	}
	return result
}

// sendProbe dispatches the collection request. The account's real transport is
// preferred so the collected state is earned under the same client identity the
// traffic will later present; the raw upstream is the fallback.
func (s *CodexTurnStateService) sendProbe(req *http.Request, proxyURL string, account *Account) (*http.Response, error) {
	if s.transport != nil {
		return s.transport.DoOpenAIProbe(req, proxyURL, account)
	}
	if s.upstream == nil {
		return nil, errCodexTurnStateProbeUnavailable
	}
	return s.upstream.Do(req, proxyURL, account.ID, account.Mode1EffectiveConcurrency())
}

// buildCodexTurnStateProbePayload mirrors the account test payload: a Responses
// request with the account's instructions and a single short user turn.
func buildCodexTurnStateProbePayload(model string, prompt string) map[string]any {
	text := strings.TrimSpace(prompt)
	if text == "" {
		text = CodexTurnStateDefaultPrompt
	}
	windowID := uuid.NewString()
	installationID := uuid.NewString()
	return map[string]any{
		"model":        model,
		"instructions": openai.DefaultInstructions,
		"input": []map[string]any{
			{
				"role": "user",
				"content": []map[string]any{
					{"type": "input_text", "text": text},
				},
			},
		},
		"stream": true,
		"store":  false,
		// The deployment classifies a Codex client by these engine-fingerprint
		// signals; without them the upstream treats the request as non-Codex.
		"client_metadata": map[string]string{
			"x-codex-window-id":       windowID,
			"x-codex-installation-id": installationID,
		},
	}
}

// extractCodexTurnStateField reads the state out of a JSON body, accepting both
// the bare field and the nested shapes the write-up hints at.
func extractCodexTurnStateField(raw []byte) string {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 {
		return ""
	}
	var payload map[string]any
	if err := json.Unmarshal(raw, &payload); err != nil {
		return ""
	}
	return findCodexTurnState(payload, 0)
}

func findCodexTurnState(value any, depth int) string {
	if depth > 6 {
		return ""
	}
	switch typed := value.(type) {
	case map[string]any:
		for _, key := range []string{"current_turn_state", "turn_state", "currentTurnState"} {
			if candidate, ok := typed[key].(string); ok && strings.TrimSpace(candidate) != "" {
				return strings.TrimSpace(candidate)
			}
		}
		for _, nested := range typed {
			if found := findCodexTurnState(nested, depth+1); found != "" {
				return found
			}
		}
	case []any:
		for _, item := range typed {
			if found := findCodexTurnState(item, depth+1); found != "" {
				return found
			}
		}
	}
	return ""
}

func httpStatusText(code int) string {
	if text := strings.TrimSpace(http.StatusText(code)); text != "" {
		return text
	}
	return "HTTP"
}

var errCodexTurnStateProbeUnavailable = errors.New("292 状态采集通道不可用")
