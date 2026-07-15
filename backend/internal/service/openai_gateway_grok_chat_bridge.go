package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/apicompat"
	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

const (
	grokChatResponsesEndpoint = "/v1/responses"
	grokChatRawEndpoint       = "/v1/chat/completions"
)

var grokStandardChatResponsesBridgeTopLevelFields = map[string]struct{}{
	"model":                 {},
	"messages":              {},
	"stream":                {},
	"stream_options":        {},
	"max_tokens":            {},
	"max_completion_tokens": {},
	"temperature":           {},
	"top_p":                 {},
	"presence_penalty":      {},
	"presencePenalty":       {},
	"frequency_penalty":     {},
	"frequencyPenalty":      {},
	"prompt_cache_key":      {},
	"tools":                 {},
	"tool_choice":           {},
	"functions":             {},
	"function_call":         {},
	"parallel_tool_calls":   {},
	"reasoning_effort":      {},
}

// grokChatResponsesBridgeEligibility deliberately accepts only request shapes
// whose Chat Completions semantics are preserved by the Responses bridge.
// Everything else stays on raw Chat Completions rather than being silently
// dropped or rewritten.
func grokChatResponsesBridgeEligibility(body []byte) (bool, string) {
	var root map[string]json.RawMessage
	if err := json.Unmarshal(body, &root); err != nil || root == nil {
		return false, "invalid_json"
	}

	if _, exists := root["stop"]; exists {
		return false, "unsupported_stop"
	}
	if raw, exists := root["reasoning_effort"]; exists {
		var effort string
		if json.Unmarshal(raw, &effort) != nil {
			return false, "invalid_reasoning_effort"
		}
		switch strings.ToLower(strings.TrimSpace(effort)) {
		case "none", "minimal", "low", "medium", "high", "xhigh":
		default:
			return false, "invalid_reasoning_effort"
		}
	}
	if raw, exists := root["tools"]; exists {
		if ok, reason := grokStandardChatFunctionToolsEligible(raw); !ok {
			return false, reason
		}
	}
	if raw, exists := root["functions"]; exists && !grokChatNullOrEmptyArray(raw) {
		return false, "unsupported_functions"
	}
	if raw, exists := root["tool_choice"]; exists {
		if ok, reason := grokStandardChatToolChoiceEligible(raw); !ok {
			return false, reason
		}
		if grokStandardChatToolChoiceRequiresTools(raw) {
			tools, exists := root["tools"]
			if !exists || grokChatNullOrEmptyArray(tools) {
				return false, "unsupported_tool_choice"
			}
		}
	}
	if raw, exists := root["parallel_tool_calls"]; exists {
		var parallel *bool
		if json.Unmarshal(raw, &parallel) != nil || parallel == nil {
			return false, "invalid_parallel_tool_calls"
		}
	}
	if raw, exists := root["function_call"]; exists && !grokChatNullOrNone(raw) {
		return false, "unsupported_function_call"
	}
	for field := range root {
		if _, supported := grokStandardChatResponsesBridgeTopLevelFields[field]; !supported {
			return false, "unknown_field_" + field
		}
	}

	var model string
	if raw, ok := root["model"]; !ok || json.Unmarshal(raw, &model) != nil || strings.TrimSpace(model) == "" {
		return false, "invalid_model"
	}

	if raw, ok := root["stream"]; ok {
		var stream *bool
		if json.Unmarshal(raw, &stream) != nil || stream == nil {
			return false, "invalid_stream"
		}
	}
	if raw, ok := root["stream_options"]; ok {
		var options map[string]json.RawMessage
		if json.Unmarshal(raw, &options) != nil || options == nil {
			return false, "invalid_stream_options"
		}
		for field, value := range options {
			if field != "include_usage" {
				return false, "unknown_stream_option_" + field
			}
			var includeUsage *bool
			if json.Unmarshal(value, &includeUsage) != nil || includeUsage == nil {
				return false, "invalid_stream_include_usage"
			}
		}
	}

	for _, field := range []string{"max_tokens", "max_completion_tokens"} {
		if raw, ok := root[field]; ok {
			var value *int
			if json.Unmarshal(raw, &value) != nil || value == nil || *value < 128 {
				return false, "unsafe_" + field
			}
		}
	}
	if _, hasMaxTokens := root["max_tokens"]; hasMaxTokens {
		if _, hasMaxCompletionTokens := root["max_completion_tokens"]; hasMaxCompletionTokens {
			return false, "conflicting_max_tokens"
		}
	}
	for _, field := range []string{"temperature", "top_p"} {
		if raw, ok := root[field]; ok {
			var value *float64
			if json.Unmarshal(raw, &value) != nil || value == nil {
				return false, "invalid_" + field
			}
		}
	}
	if raw, ok := root["prompt_cache_key"]; ok {
		var key string
		if json.Unmarshal(raw, &key) != nil {
			return false, "invalid_prompt_cache_key"
		}
	}

	var messages []map[string]json.RawMessage
	rawMessages, ok := root["messages"]
	if !ok || json.Unmarshal(rawMessages, &messages) != nil || len(messages) == 0 {
		return false, "invalid_messages"
	}
	for _, message := range messages {
		if ok, reason := grokStandardChatMessageEligible(message); !ok {
			return false, reason
		}
	}

	return true, ""
}

func grokStandardChatFunctionToolsEligible(raw json.RawMessage) (bool, string) {
	if grokChatNullOrEmptyArray(raw) {
		return true, ""
	}
	var tools []map[string]json.RawMessage
	if json.Unmarshal(raw, &tools) != nil || len(tools) == 0 {
		return false, "invalid_tools"
	}
	for _, tool := range tools {
		for field := range tool {
			if field != "type" && field != "function" {
				return false, "unsafe_tool_field_" + field
			}
		}
		var toolType string
		if value, exists := tool["type"]; !exists || json.Unmarshal(value, &toolType) != nil || toolType != "function" {
			return false, "unsupported_tool_type"
		}
		var function map[string]json.RawMessage
		if value, exists := tool["function"]; !exists || json.Unmarshal(value, &function) != nil || function == nil {
			return false, "invalid_tool_function"
		}
		for field := range function {
			switch field {
			case "name", "description", "parameters", "strict":
			default:
				return false, "unsafe_tool_function_field_" + field
			}
		}
		var name string
		if value, exists := function["name"]; !exists || json.Unmarshal(value, &name) != nil || strings.TrimSpace(name) == "" {
			return false, "invalid_tool_function_name"
		}
		if value, exists := function["description"]; exists {
			var description string
			if json.Unmarshal(value, &description) != nil {
				return false, "invalid_tool_function_description"
			}
		}
		if value, exists := function["parameters"]; exists {
			var parameters map[string]any
			if json.Unmarshal(value, &parameters) != nil || parameters == nil {
				return false, "invalid_tool_function_parameters"
			}
		}
		if value, exists := function["strict"]; exists {
			var strict *bool
			if json.Unmarshal(value, &strict) != nil || strict == nil {
				return false, "invalid_tool_function_strict"
			}
		}
	}
	return true, ""
}

func grokStandardChatToolChoiceEligible(raw json.RawMessage) (bool, string) {
	if strings.TrimSpace(string(raw)) == "null" {
		return true, ""
	}
	var choice string
	if json.Unmarshal(raw, &choice) != nil {
		// Chat Completions and Responses use different object layouts for a
		// forced function. Keep that uncommon shape on the raw endpoint rather
		// than forwarding a subtly incompatible value.
		return false, "unsupported_tool_choice"
	}
	switch strings.ToLower(strings.TrimSpace(choice)) {
	case "auto", "none", "required":
		return true, ""
	default:
		return false, "unsupported_tool_choice"
	}
}

func grokStandardChatToolChoiceRequiresTools(raw json.RawMessage) bool {
	var choice string
	return json.Unmarshal(raw, &choice) == nil && strings.EqualFold(strings.TrimSpace(choice), "required")
}

func grokStandardChatMessageEligible(message map[string]json.RawMessage) (bool, string) {
	var role string
	if raw, exists := message["role"]; !exists || json.Unmarshal(raw, &role) != nil {
		return false, "invalid_message_role"
	}
	role = strings.TrimSpace(role)

	allowedFields := map[string]struct{}{"role": {}, "content": {}}
	switch role {
	case "system", "user":
	case "assistant":
		allowedFields["tool_calls"] = struct{}{}
		allowedFields["reasoning_content"] = struct{}{}
	case "tool":
		allowedFields["tool_call_id"] = struct{}{}
	default:
		return false, "unsupported_message_role_" + role
	}
	for field := range message {
		if _, ok := allowedFields[field]; !ok {
			return false, "unsafe_message_field_" + field
		}
	}

	switch role {
	case "system", "user":
		raw, exists := message["content"]
		if !exists {
			return false, "non_text_message_content"
		}
		if ok, reason := grokStandardChatContentEligible(raw, false, true); !ok {
			return false, reason
		}
	case "assistant":
		hasContent := false
		if raw, exists := message["content"]; exists {
			if ok, reason := grokStandardChatContentEligible(raw, true, true); !ok {
				return false, reason
			}
			hasContent = grokStandardChatTextContentNonEmpty(raw)
		}
		hasToolCalls := false
		if raw, exists := message["tool_calls"]; exists {
			var reason string
			hasToolCalls, reason = grokStandardChatToolCallsEligible(raw)
			if reason != "" {
				return false, reason
			}
		}
		hasReasoning := false
		if raw, exists := message["reasoning_content"]; exists {
			var reasoning string
			if json.Unmarshal(raw, &reasoning) != nil {
				return false, "invalid_reasoning_content"
			}
			hasReasoning = strings.TrimSpace(reasoning) != ""
		}
		if !hasContent && !hasToolCalls && !hasReasoning {
			return false, "empty_message_content"
		}
	case "tool":
		raw, exists := message["content"]
		if !exists {
			return false, "non_text_message_content"
		}
		if ok, reason := grokStandardChatContentEligible(raw, false, false); !ok {
			return false, reason
		}
		var callID string
		if raw, exists := message["tool_call_id"]; !exists || json.Unmarshal(raw, &callID) != nil || strings.TrimSpace(callID) == "" {
			return false, "invalid_tool_call_id"
		}
	}
	return true, ""
}

func grokStandardChatContentEligible(raw json.RawMessage, allowEmpty, allowImages bool) (bool, string) {
	trimmed := strings.TrimSpace(string(raw))
	if trimmed == "null" {
		if allowEmpty {
			return true, ""
		}
		return false, "non_text_message_content"
	}
	var text string
	if json.Unmarshal(raw, &text) == nil {
		if allowEmpty || strings.TrimSpace(text) != "" {
			return true, ""
		}
		return false, "empty_message_content"
	}
	if allowImages {
		return grokChatStructuredContentBridgeable(raw)
	}
	var parts []map[string]json.RawMessage
	if json.Unmarshal(raw, &parts) != nil || len(parts) == 0 {
		return false, "non_text_message_content"
	}
	for _, part := range parts {
		for field := range part {
			if field != "type" && field != "text" {
				return false, "non_text_message_content"
			}
		}
		var partType, partText string
		if value, exists := part["type"]; !exists || json.Unmarshal(value, &partType) != nil || partType != "text" {
			return false, "non_text_message_content"
		}
		if value, exists := part["text"]; !exists || json.Unmarshal(value, &partText) != nil {
			return false, "non_text_message_content"
		}
		if !allowEmpty && strings.TrimSpace(partText) == "" {
			return false, "empty_message_content"
		}
	}
	return true, ""
}

func grokStandardChatTextContentNonEmpty(raw json.RawMessage) bool {
	var text string
	if json.Unmarshal(raw, &text) == nil {
		return strings.TrimSpace(text) != ""
	}
	var parts []map[string]json.RawMessage
	if json.Unmarshal(raw, &parts) != nil {
		return false
	}
	for _, part := range parts {
		var partType string
		if value, exists := part["type"]; exists && json.Unmarshal(value, &partType) == nil {
			switch strings.TrimSpace(partType) {
			case "image_url", "input_image":
				return true
			}
		}
		var partText string
		if value, exists := part["text"]; exists && json.Unmarshal(value, &partText) == nil && strings.TrimSpace(partText) != "" {
			return true
		}
	}
	return false
}

func grokStandardChatToolCallsEligible(raw json.RawMessage) (bool, string) {
	var calls []map[string]json.RawMessage
	if json.Unmarshal(raw, &calls) != nil {
		return false, "invalid_tool_calls"
	}
	if len(calls) == 0 {
		return false, ""
	}
	for _, call := range calls {
		for field := range call {
			if field != "id" && field != "type" && field != "function" {
				return false, "unsafe_tool_call_field_" + field
			}
		}
		var id, callType string
		if value, exists := call["id"]; !exists || json.Unmarshal(value, &id) != nil || strings.TrimSpace(id) == "" {
			return false, "invalid_tool_call_id"
		}
		if value, exists := call["type"]; !exists || json.Unmarshal(value, &callType) != nil || callType != "function" {
			return false, "unsupported_tool_call_type"
		}
		var function map[string]json.RawMessage
		if value, exists := call["function"]; !exists || json.Unmarshal(value, &function) != nil || function == nil {
			return false, "invalid_tool_call_function"
		}
		for field := range function {
			if field != "name" && field != "arguments" {
				return false, "unsafe_tool_call_function_field_" + field
			}
		}
		var name, arguments string
		if value, exists := function["name"]; !exists || json.Unmarshal(value, &name) != nil || strings.TrimSpace(name) == "" {
			return false, "invalid_tool_call_function_name"
		}
		if value, exists := function["arguments"]; !exists || json.Unmarshal(value, &arguments) != nil {
			return false, "invalid_tool_call_arguments"
		}
	}
	return true, ""
}

func grokChatStructuredContentBridgeable(raw json.RawMessage) (bool, string) {
	var parts []map[string]json.RawMessage
	if err := json.Unmarshal(raw, &parts); err != nil {
		return false, "non_text_message_content"
	}
	if len(parts) == 0 {
		return false, "empty_message_content"
	}
	hasContent := false
	for _, part := range parts {
		var partType string
		rawType, ok := part["type"]
		if !ok || json.Unmarshal(rawType, &partType) != nil {
			return false, "non_text_message_content"
		}
		switch strings.TrimSpace(partType) {
		case "text":
			var text string
			if raw, ok := part["text"]; ok && json.Unmarshal(raw, &text) == nil {
				if strings.TrimSpace(text) != "" {
					hasContent = true
				}
			}
		case "image_url", "input_image":
			hasContent = true
		default:
			return false, "unsupported_content_part_" + strings.TrimSpace(partType)
		}
	}
	if !hasContent {
		return false, "empty_message_content"
	}
	return true, ""
}

func grokChatNullOrEmptyArray(raw json.RawMessage) bool {
	if strings.TrimSpace(string(raw)) == "null" {
		return true
	}
	var values []json.RawMessage
	return json.Unmarshal(raw, &values) == nil && len(values) == 0
}

func grokChatNullOrNone(raw json.RawMessage) bool {
	if strings.TrimSpace(string(raw)) == "null" {
		return true
	}
	var value string
	return json.Unmarshal(raw, &value) == nil && strings.EqualFold(strings.TrimSpace(value), "none")
}

func grokChatCacheIntentBody(body []byte) ([]byte, error) {
	var root map[string]json.RawMessage
	if err := json.Unmarshal(body, &root); err != nil {
		return nil, err
	}
	for _, field := range []string{"tools", "tool_choice", "functions", "function_call"} {
		delete(root, field)
	}
	return json.Marshal(root)
}

func grokChatResponsesRuntimeEligible(upstreamModel, cacheIdentity string) bool {
	return strings.TrimSpace(upstreamModel) == "grok-4.5" && strings.TrimSpace(cacheIdentity) != ""
}

// forwardGrokChatCompletionsViaResponses converts a strictly compatible Chat
// request into xAI Responses format and reuses the established Responses-to-
// Chat response translators. It intentionally does not run the Codex OAuth
// transform because Grok CLI is a separate upstream protocol.
func (s *OpenAIGatewayService) forwardGrokChatCompletionsViaResponses(
	ctx context.Context,
	c *gin.Context,
	account *Account,
	body []byte,
	promptCacheKey string,
	defaultMappedModel string,
) (*OpenAIForwardResult, error) {
	startTime := time.Now()

	var chatReq apicompat.ChatCompletionsRequest
	if err := json.Unmarshal(body, &chatReq); err != nil {
		return nil, fmt.Errorf("parse grok chat completions request: %w", err)
	}
	originalModel := chatReq.Model
	clientStream := chatReq.Stream
	billingModel := resolveOpenAIForwardModel(account, originalModel, defaultMappedModel)
	upstreamModel := normalizeOpenAIModelForUpstream(account, billingModel)
	cacheIdentity := resolveGrokCacheIdentity(c, body, promptCacheKey, upstreamModel)
	// Image inputs must go through the Responses bridge: the raw Chat
	// Completions path cannot forward image_url parts to Grok's native vision
	// for non-composer models, so they would be silently dropped. Route them to
	// Responses even when no prompt-cache identity is available.
	hasImageInput := openAIJSONValueMayContainImageInput(gjson.GetBytes(body, "messages"))
	if !grokChatResponsesRuntimeEligible(upstreamModel, cacheIdentity) && (!hasImageInput || strings.TrimSpace(upstreamModel) != "grok-4.5") {
		return s.forwardAsRawChatCompletions(ctx, c, account, body, defaultMappedModel)
	}

	responsesReq, err := apicompat.ChatCompletionsToResponses(&chatReq)
	if err != nil {
		return nil, fmt.Errorf("convert grok chat completions to responses: %w", err)
	}
	responsesReq.Model = upstreamModel
	responsesReq.Stream = true
	// xAI's Grok CLI Responses endpoint consumes the Chat-compatible top-level
	// reasoning_effort field. The generic OpenAI converter emits
	// reasoning.effort instead, which the endpoint accepts but does not apply.
	// Re-add the original field after marshaling and omit the nested variant.
	reasoningEffort := strings.ToLower(strings.TrimSpace(chatReq.ReasoningEffort))
	responsesReq.Reasoning = nil
	// These fields are useful to Codex but are not needed by the Grok CLI
	// protocol. Keep the bridge request as close as possible to native Grok.
	responsesReq.Include = nil
	responsesReq.Store = nil

	responsesBody, err := json.Marshal(responsesReq)
	if err != nil {
		return nil, fmt.Errorf("marshal grok responses bridge request: %w", err)
	}
	if reasoningEffort != "" {
		responsesBody, err = sjson.SetBytes(responsesBody, "reasoning_effort", reasoningEffort)
		if err != nil {
			return nil, fmt.Errorf("preserve grok chat reasoning effort: %w", err)
		}
	}
	responsesBody, err = patchGrokResponsesBody(responsesBody, upstreamModel)
	if err != nil {
		return nil, fmt.Errorf("patch grok responses bridge request: %w", err)
	}
	intentBody, err := grokChatCacheIntentBody(body)
	if err != nil {
		return nil, fmt.Errorf("normalize grok responses bridge tool intent: %w", err)
	}
	responsesBody, err = applyGrokResponsesCacheIdentity(responsesBody, intentBody, cacheIdentity, true)
	if err != nil {
		return nil, fmt.Errorf("apply grok responses bridge cache identity: %w", err)
	}
	responsesBody, err = applyGrokFreeMessagesFunctionToolCacheRoute(responsesBody, responsesBody, account, cacheIdentity)
	if err != nil {
		return nil, fmt.Errorf("apply grok responses bridge function-tool cache route: %w", err)
	}

	updatedBody, policyErr := s.applyOpenAIFastPolicyToBody(ctx, account, upstreamModel, responsesBody)
	if policyErr != nil {
		var blocked *OpenAIFastBlockedError
		if errors.As(policyErr, &blocked) {
			MarkOpsClientBusinessLimited(c, OpsClientBusinessLimitedReasonLocalPolicyDenied)
			writeChatCompletionsError(c, http.StatusForbidden, "permission_error", blocked.Message)
		}
		return nil, policyErr
	}
	responsesBody = updatedBody

	token, _, err := s.getRequestCredential(ctx, c, account)
	if err != nil {
		return nil, fmt.Errorf("get grok access token: %w", err)
	}
	upstreamCtx, releaseUpstreamCtx := detachUpstreamContext(ctx)
	upstreamReq, err := buildGrokResponsesRequest(upstreamCtx, c, account, responsesBody, token, cacheIdentity, s.cfg)
	releaseUpstreamCtx()
	if err != nil {
		return nil, fmt.Errorf("build grok responses bridge request: %w", err)
	}
	SetActualOpenAIUpstreamEndpoint(c, grokChatResponsesEndpoint)

	proxyURL := ""
	if account.ProxyID != nil && account.Proxy != nil {
		proxyURL = account.Proxy.URL()
	}
	resp, err := s.httpUpstream.Do(upstreamReq, proxyURL, account.ID, account.Concurrency)
	if err != nil {
		return nil, s.handleOpenAIUpstreamTransportError(ctx, c, account, err, false)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode >= http.StatusBadRequest {
		respBody, upstreamMsg := s.readOpenAIUpstreamError(resp)
		if upstreamMsg == "" {
			upstreamMsg = fmt.Sprintf("xAI upstream returned status %d", resp.StatusCode)
		}
		appendOpsUpstreamError(c, OpsUpstreamErrorEvent{
			Platform:           account.Platform,
			AccountID:          account.ID,
			AccountName:        account.Name,
			UpstreamStatusCode: resp.StatusCode,
			UpstreamRequestID:  firstNonEmpty(resp.Header.Get("x-request-id"), resp.Header.Get("xai-request-id")),
			Kind:               "failover",
			Message:            upstreamMsg,
		})
		s.handleGrokAccountUpstreamError(ctx, account, resp.StatusCode, resp.Header, respBody)
		if s.shouldFailoverUpstreamError(resp.StatusCode) {
			return nil, &UpstreamFailoverError{
				StatusCode:             resp.StatusCode,
				ResponseBody:           respBody,
				ResponseHeaders:        resp.Header.Clone(),
				RetryableOnSameAccount: account.IsPoolMode() && account.IsPoolModeRetryableStatus(resp.StatusCode),
			}
		}
		return s.handleChatCompletionsErrorResponse(resp, c, account, billingModel)
	}

	s.updateGrokUsageFromResponse(ctx, account, resp.Header, resp.StatusCode)

	var result *OpenAIForwardResult
	if clientStream {
		result, err = s.handleChatStreamingResponse(resp, c, account, originalModel, billingModel, upstreamModel, startTime, len(body))
	} else {
		result, err = s.handleChatBufferedStreamingResponse(resp, c, account, originalModel, billingModel, upstreamModel, startTime)
	}
	if result != nil {
		result.UpstreamEndpoint = grokChatResponsesEndpoint
		result.ResponseHeaders = resp.Header.Clone()
		if result.RequestID == "" {
			result.RequestID = firstNonEmpty(resp.Header.Get("x-request-id"), resp.Header.Get("xai-request-id"))
		}
		result.ReasoningEffort = extractOpenAIReasoningEffortFromBody(body, upstreamModel, billingModel, originalModel)
	}
	return result, err
}
