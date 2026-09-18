package service

import (
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
)

func (s *OpenAIGatewayService) SetPluginManager(manager *PluginManager) {
	s.pluginManager = manager
}

// SetCodexTurnState installs the turn-state pool. A nil pool disables both
// injection and harvesting, which is the default.
func (s *OpenAIGatewayService) SetCodexTurnState(pool CodexTurnStateGateway) {
	s.codexTurnState = pool
}

// CodexTurnStateGateway is the narrow surface the gateway needs from the pool:
// read one header before the request, hand back one header after it. Keeping it
// this small means the gateway has no opinion about how states are collected,
// stored or expired.
type CodexTurnStateGateway interface {
	// InjectionHeader returns the request header to attach for an account, or
	// nothing when an injection allow-list excludes this model.
	InjectionHeader(accountID int64, requestedModel string) (name string, value string, ok bool)
	// ForceInject reports whether that header may replace one the caller already
	// sent, or only fill a blank slot.
	ForceInject() bool
	// ModelScoped reports whether the injection depends on the requested model.
	ModelScoped() bool
	// ObserveResponse feeds an upstream response back into the pool.
	ObserveResponse(accountID int64, header http.Header, statusCode int, model string)
}

// requestedModelOf reads the model a request asks for without consuming its body.
// The gateway builds these requests from bytes.Reader, so GetBody is set and the
// body can be replayed safely; when it is not, the caller gets "" and the pool
// treats the model as unknown.
func requestedModelOf(request *http.Request) string {
	if request == nil || request.GetBody == nil {
		return ""
	}
	body, err := request.GetBody()
	if err != nil {
		return ""
	}
	defer func() { _ = body.Close() }()
	raw, err := io.ReadAll(io.LimitReader(body, codexTurnStateModelProbeBytes))
	if err != nil {
		return ""
	}
	model, _, _ := extractOpenAIRequestMetaFromBody(raw)
	return model
}

// doOpenAIUpstream 只在 OpenAI OAuth 能力绑定已启用时把真实请求交给插件。
// 插件返回标准 http.Response，响应解析、错误映射、SSE 和计费仍由现有核心链处理。
func (s *OpenAIGatewayService) doOpenAIUpstream(request *http.Request, proxyURL string, account *Account) (*http.Response, error) {
	request = WithAccountTrafficRequest(request, account)
	s.injectCodexTurnState(request, account)
	return accountTrafficController(s.httpUpstream).DoHTTP(request, func(controlled *http.Request) (*http.Response, error) {
		response, err := s.doOpenAIUpstreamWithoutTraffic(controlled, proxyURL, account)
		s.observeCodexTurnState(response, account, controlled)
		return response, err
	})
}

// injectCodexTurnState fills in the account's pooled turn state when the request
// does not already carry one.
//
// It never overwrites a state that is already present unless the pool is in
// force mode: a real Codex client captures the blob from its own response and
// echoes it on the remaining requests of the same turn, and that value is the
// one the upstream minted for exactly this session. Replacing it with a pooled
// token would recreate the very contradiction guardOpenAICodexTurnStateEcho
// exists to remove. Filling only an empty slot is also what makes this compose
// with that guard: a foreign-account echo is stripped upstream of here, and the
// freed slot is then filled with a token minted for THIS account.
//
// Force mode exists because fill-empty can never help the case this feature is
// for: the client's own token is precisely the downgraded one (356 chars /
// 13 blocks when the account is degraded), so leaving it in place keeps the
// downgrade. Overwriting it with a captured normal token re-applies the good
// route — measured 12/12 correct answers while replaying a normal state, versus
// ~9% for cold requests on the same pool.
//
// The state only exists on the ChatGPT Codex backend, so a request to any other
// host is left untouched rather than carrying a header that means nothing there.
func (s *OpenAIGatewayService) injectCodexTurnState(request *http.Request, account *Account) {
	if s.codexTurnState == nil || request == nil || request.URL == nil || account == nil {
		return
	}
	if !isCodexTurnStateUpstreamHost(request.URL.Host) || !strings.HasPrefix(request.URL.Path, "/backend-api/codex/") {
		return
	}
	if !s.codexTurnState.ForceInject() && strings.TrimSpace(request.Header.Get(openAICodexTurnStateHeader)) != "" {
		return
	}
	// The model is only read when something actually depends on it; the extra
	// body replay is skipped entirely when no allow-list is configured.
	model := ""
	if s.codexTurnState.ModelScoped() {
		model = requestedModelOf(request)
		if model == "" {
			return
		}
	}
	name, value, ok := s.codexTurnState.InjectionHeader(account.ID, model)
	if !ok {
		return
	}
	request.Header.Set(name, value)
}

// observeCodexTurnState hands the response back to the pool. It runs on every
// Codex response and only reads headers, so it adds no measurable latency.
func (s *OpenAIGatewayService) observeCodexTurnState(response *http.Response, account *Account, requests ...*http.Request) {
	if s.codexTurnState == nil || response == nil || account == nil {
		return
	}
	if len(requests) == 0 || requests[0] == nil || requests[0].URL == nil ||
		!isCodexTurnStateUpstreamHost(requests[0].URL.Host) || !strings.HasPrefix(requests[0].URL.Path, "/backend-api/codex/") {
		return
	}
	// Bind observations to the requested model, not the served-model header.
	model := requestedModelOf(requests[0])
	if model == "" {
		return
	}
	s.codexTurnState.ObserveResponse(account.ID, response.Header, response.StatusCode, model)
}

// isCodexTurnStateUpstreamHost reports whether this host serves the Codex
// backend that issues turn states.
func isCodexTurnStateUpstreamHost(host string) bool {
	host = strings.ToLower(strings.TrimSpace(host))
	if host == "" {
		return false
	}
	if strings.Contains(host, ":") {
		name, port, err := net.SplitHostPort(host)
		n, portErr := strconv.Atoi(port)
		if err != nil || portErr != nil || n < 1 || n > 65535 {
			return false
		}
		host = name
	}
	return host == "chatgpt.com"
}

// codexTurnStateObservedModel is best effort: the model is recorded for the
// operator's benefit, and reading it must never fail a request.
func codexTurnStateObservedModel(response *http.Response) string {
	if response == nil {
		return ""
	}
	return strings.TrimSpace(response.Header.Get("x-codex-model"))
}
func (s *OpenAIGatewayService) doOpenAIUpstreamWithoutTraffic(request *http.Request, proxyURL string, account *Account) (*http.Response, error) {
	profile, err := resolveMode1TLSProfile(account)
	if err != nil {
		return nil, err
	}
	if profile != nil && (s.cfg == nil || s.cfg.Gateway.TLSFingerprint.Enabled) && (s.pluginManager == nil || !s.pluginManager.ShouldRouteOpenAIOAuth(account)) {
		return s.httpUpstream.DoWithTLS(WithAccountTrafficRequest(request, account), proxyURL, account.ID, account.Mode1EffectiveConcurrency(), profile)
	}
	if s.pluginManager != nil {
		response, handled, err := s.pluginManager.RoundTripOpenAIOAuth(request.Context(), request, proxyURL, account)
		if handled {
			return captureIntelligentResponse(request, response), err
		}
	}
	return s.httpUpstream.Do(WithAccountTrafficRequest(request, account), proxyURL, account.ID, account.Mode1EffectiveConcurrency())
}

// doOpenAIAccountTestUpstream 让 OpenAI OAuth 账号测试与真实转发使用同一插件路径。
// API Key 和未命中插件的账号保持各自原有的 HTTPUpstream 行为。
func (s *AccountTestService) doOpenAIAccountTestUpstream(
	request *http.Request,
	proxyURL string,
	account *Account,
	useTLSFallback bool,
) (*http.Response, error) {
	request = WithAccountTrafficRequest(request, account)
	return accountTrafficController(s.httpUpstream).DoHTTP(request, func(controlled *http.Request) (*http.Response, error) {
		return s.doOpenAIAccountTestUpstreamWithoutTraffic(controlled, proxyURL, account, useTLSFallback)
	})
}
func (s *AccountTestService) doOpenAIAccountTestUpstreamWithoutTraffic(request *http.Request, proxyURL string, account *Account, useTLSFallback bool) (*http.Response, error) {
	profile, err := resolveMode1TLSProfile(account)
	if err != nil {
		return nil, err
	}
	if profile != nil && (s.cfg == nil || s.cfg.Gateway.TLSFingerprint.Enabled) && (s.pluginManager == nil || !s.pluginManager.ShouldRouteOpenAIOAuth(account)) {
		return s.httpUpstream.DoWithTLS(WithAccountTrafficRequest(request, account), proxyURL, account.ID, account.Mode1EffectiveConcurrency(), profile)
	}
	if s.pluginManager != nil {
		response, handled, err := s.pluginManager.RoundTripOpenAIOAuth(request.Context(), request, proxyURL, account)
		if handled {
			return captureIntelligentResponse(request, response), err
		}
	}
	if useTLSFallback && !isMode1ProtectionEnabled(account) {
		return s.httpUpstream.DoWithTLS(
			WithAccountTrafficRequest(request, account),
			proxyURL,
			account.ID,
			account.Concurrency,
			s.tlsFPProfileService.ResolveTLSProfile(account),
		)
	}
	return s.httpUpstream.Do(WithAccountTrafficRequest(request, account), proxyURL, account.ID, account.Mode1EffectiveConcurrency())
}

// DoOpenAIProbe implements CodexTurnStateTransport by reusing the account-test
// transport. Collecting a state through the same transport selection real
// traffic uses — TLS fingerprint profile, protection mode, plugin routing — is
// what makes the collected state meaningful for that account: a token earned
// over a different transport would be presented later by a client the upstream
// sees differently.
func (s *AccountTestService) DoOpenAIProbe(request *http.Request, proxyURL string, account *Account) (*http.Response, error) {
	return s.doOpenAIAccountTestUpstream(request, proxyURL, account, true)
}
