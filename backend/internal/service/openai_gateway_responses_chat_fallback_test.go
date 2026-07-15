//go:build unit

package service

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/pkg/openai_compat"
	"github.com/Wei-Shaw/sub2api/internal/pkg/xai"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestGrokResponsesRawChatEligibility(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		body string
		want bool
	}{
		{
			name: "plain responses stays native",
			body: `{"model":"grok","input":"hello"}`,
		},
		{
			name: "native search only stays native",
			body: `{"model":"grok","input":"hello","tools":[{"type":"web_search"},{"type":"x_search"}]}`,
		},
		{
			name: "function tool is eligible",
			body: `{"model":"grok","input":"hello","tools":[{"type":"function","name":"lookup","parameters":{"type":"object"}}]}`,
			want: true,
		},
		{
			name: "function plus proxy search tools is eligible",
			body: `{"model":"grok","input":"hello","tools":[{"type":"function","name":"lookup","parameters":{"type":"object"}},{"type":"web_search"},{"type":"x_search"}]}`,
			want: true,
		},
		{
			name: "additional function tools are eligible",
			body: `{"model":"grok","input":[{"type":"additional_tools","tools":[{"type":"function","name":"lookup","parameters":{"type":"object"}}]},{"type":"message","role":"user","content":"hello"}]}`,
			want: true,
		},
		{
			name: "unsupported capability stays native",
			body: `{"model":"grok","input":"hello","tools":[{"type":"function","name":"lookup","parameters":{"type":"object"}},{"type":"file_search"}]}`,
		},
		{
			name: "malformed function stays native",
			body: `{"model":"grok","input":"hello","tools":[{"type":"function","parameters":{"type":"object"}}]}`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, grokResponsesRawChatEligible([]byte(tt.body)))
		})
	}
}

func TestForwardResponses_GrokWithoutClientToolsStillUsesResponsesEndpoint(t *testing.T) {
	gin.SetMode(gin.TestMode)

	body := []byte(`{"model":"grok","input":"hello","stream":false}`)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewReader(body))
	c.Set("api_key", &APIKey{ID: 8101})

	account := healthyGrokOAuthGatewayTestAccount(81, "access-token")
	repo := &grokQuotaAccountRepo{mockAccountRepoForPlatform: &mockAccountRepoForPlatform{
		accountsByID: map[int64]*Account{account.ID: account},
	}}
	upstream := &httpUpstreamRecorder{resp: &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body: io.NopCloser(strings.NewReader(
			`{"id":"resp_native_grok","object":"response","model":"grok-4.5","status":"completed","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"ok"}]}],"usage":{"input_tokens":3,"output_tokens":1,"total_tokens":4}}`,
		)),
	}}
	svc := &OpenAIGatewayService{
		httpUpstream:      upstream,
		grokTokenProvider: NewGrokTokenProvider(repo, nil),
		accountRepo:       repo,
	}

	result, err := svc.Forward(context.Background(), c, account, body)
	require.NoError(t, err)
	require.NotNil(t, result)
	require.Equal(t, xai.DefaultCLIBaseURL+"/responses", upstream.lastReq.URL.String())
	require.True(t, gjson.GetBytes(upstream.lastBody, "input").Exists())
	require.False(t, gjson.GetBytes(upstream.lastBody, "messages").Exists())
}

func TestForwardResponses_GrokFunctionToolsUseRawChatAndStableCache(t *testing.T) {
	gin.SetMode(gin.TestMode)

	firstBody := []byte(`{
		"model":"grok","stream":false,"prompt_cache_key":"claude-session-1",
		"input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"look up alpha"}]}],
		"tools":[
			{"type":"function","name":"lookup","description":"look up a key","parameters":{"type":"object","properties":{"key":{"type":"string"}},"required":["key"]}},
			{"type":"web_search"},{"type":"x_search"}
		],
		"tool_choice":"auto"
	}`)
	secondBody := []byte(`{
		"model":"grok","stream":false,"prompt_cache_key":"claude-session-1",
		"input":[
			{"type":"message","role":"user","content":[{"type":"input_text","text":"look up alpha"}]},
			{"type":"function_call","id":"fc_lookup","call_id":"call_lookup","name":"lookup","arguments":"{\"key\":\"alpha\"}"},
			{"type":"function_call_output","call_id":"call_lookup","output":"{\"value\":42}"},
			{"type":"message","role":"user","content":[{"type":"input_text","text":"summarize it"}]}
		],
		"tools":[
			{"type":"function","name":"lookup","description":"look up a key","parameters":{"type":"object","properties":{"key":{"type":"string"}},"required":["key"]}},
			{"type":"web_search"},{"type":"x_search"}
		],
		"tool_choice":"auto"
	}`)

	chatResponse := func(id, message string, cached int) *http.Response {
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"application/json"}, "x-request-id": []string{id}},
			Body:       io.NopCloser(strings.NewReader(`{"id":"` + id + `","object":"chat.completion","model":"grok-4.5","choices":[{"index":0,"message":{"role":"assistant","content":"` + message + `"},"finish_reason":"stop"}],"usage":{"prompt_tokens":7000,"completion_tokens":2,"total_tokens":7002,"prompt_tokens_details":{"cached_tokens":` + fmt.Sprint(cached) + `}}}`)),
		}
	}
	account := healthyGrokOAuthGatewayTestAccount(82, "access-token")
	repo := &grokQuotaAccountRepo{mockAccountRepoForPlatform: &mockAccountRepoForPlatform{
		accountsByID: map[int64]*Account{account.ID: account},
	}}
	upstream := &httpUpstreamRecorder{responses: []*http.Response{
		chatResponse("chatcmpl_first", "tool result accepted", 0),
		chatResponse("chatcmpl_second", "the value is 42", 6144),
	}}
	svc := &OpenAIGatewayService{
		httpUpstream:      upstream,
		grokTokenProvider: NewGrokTokenProvider(repo, nil),
		accountRepo:       repo,
	}
	forward := func(body []byte) (*OpenAIForwardResult, *httptest.ResponseRecorder) {
		rec := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(rec)
		c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewReader(body))
		c.Request.Header.Set("Content-Type", "application/json")
		c.Set("api_key", &APIKey{ID: 8201})
		result, err := svc.Forward(context.Background(), c, account, body)
		require.NoError(t, err)
		require.Equal(t, grokChatRawEndpoint, GetActualOpenAIUpstreamEndpoint(c))
		return result, rec
	}

	firstResult, _ := forward(firstBody)
	secondResult, secondRecorder := forward(secondBody)
	require.NotNil(t, firstResult)
	require.NotNil(t, secondResult)
	require.Len(t, upstream.requests, 2)
	require.Len(t, upstream.bodies, 2)

	firstIdentity := upstream.requests[0].Header.Get(grokConversationIDHeader)
	secondIdentity := upstream.requests[1].Header.Get(grokConversationIDHeader)
	require.NotEmpty(t, firstIdentity)
	require.Equal(t, firstIdentity, secondIdentity)
	for i := range upstream.requests {
		require.Equal(t, xai.DefaultCLIBaseURL+"/chat/completions", upstream.requests[i].URL.String())
		require.False(t, gjson.GetBytes(upstream.bodies[i], "prompt_cache_key").Exists())
		tools := gjson.GetBytes(upstream.bodies[i], "tools").Array()
		require.Len(t, tools, 1)
		require.Equal(t, "function", tools[0].Get("type").String())
		require.Equal(t, "lookup", tools[0].Get("function.name").String())
		require.NotContains(t, string(upstream.bodies[i]), "web_search")
		require.NotContains(t, string(upstream.bodies[i]), "x_search")
	}
	require.Equal(t, grokChatRawEndpoint, secondResult.UpstreamEndpoint)
	require.Equal(t, 6144, secondResult.Usage.CacheReadInputTokens)
	require.Equal(t, int64(6144), gjson.Get(secondRecorder.Body.String(), "usage.input_tokens_details.cached_tokens").Int())
}

func TestForwardResponses_GrokFunctionToolsStreamingPropagatesCachedUsage(t *testing.T) {
	gin.SetMode(gin.TestMode)

	body := []byte(`{
		"model":"grok","stream":true,"prompt_cache_key":"claude-stream-session",
		"input":[{"type":"message","role":"user","content":"look up alpha"}],
		"tools":[{"type":"function","name":"lookup","parameters":{"type":"object"}},{"type":"web_search"},{"type":"x_search"}],
		"tool_choice":"auto"
	}`)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewReader(body))
	c.Set("api_key", &APIKey{ID: 8301})

	upstreamBody := strings.Join([]string{
		`data: {"id":"chatcmpl_stream","object":"chat.completion.chunk","model":"grok-4.5","choices":[{"index":0,"delta":{"role":"assistant"}}]}`,
		"",
		`data: {"id":"chatcmpl_stream","object":"chat.completion.chunk","model":"grok-4.5","choices":[{"index":0,"delta":{"content":"cached ok"}}]}`,
		"",
		`data: {"id":"chatcmpl_stream","object":"chat.completion.chunk","model":"grok-4.5","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`,
		"",
		`data: {"id":"chatcmpl_stream","object":"chat.completion.chunk","model":"grok-4.5","choices":[],"usage":{"prompt_tokens":5000,"completion_tokens":3,"total_tokens":5003,"prompt_tokens_details":{"cached_tokens":4096}}}`,
		"",
		"data: [DONE]",
		"",
	}, "\n")
	account := healthyGrokOAuthGatewayTestAccount(83, "access-token")
	repo := &grokQuotaAccountRepo{mockAccountRepoForPlatform: &mockAccountRepoForPlatform{
		accountsByID: map[int64]*Account{account.ID: account},
	}}
	upstream := &httpUpstreamRecorder{resp: &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
		Body:       io.NopCloser(strings.NewReader(upstreamBody)),
	}}
	svc := &OpenAIGatewayService{
		httpUpstream:      upstream,
		grokTokenProvider: NewGrokTokenProvider(repo, nil),
		accountRepo:       repo,
	}

	result, err := svc.Forward(context.Background(), c, account, body)
	require.NoError(t, err)
	require.NotNil(t, result)
	require.True(t, result.Stream)
	require.Equal(t, 4096, result.Usage.CacheReadInputTokens)
	require.Equal(t, xai.DefaultCLIBaseURL+"/chat/completions", upstream.lastReq.URL.String())
	require.NotEmpty(t, upstream.lastReq.Header.Get(grokConversationIDHeader))
	require.Len(t, gjson.GetBytes(upstream.lastBody, "tools").Array(), 1)
	require.NotContains(t, string(upstream.lastBody), "web_search")
	require.NotContains(t, string(upstream.lastBody), "x_search")
	require.Contains(t, rec.Body.String(), "event: response.completed")
	require.Contains(t, rec.Body.String(), `"cached_tokens":4096`)
	require.Contains(t, rec.Body.String(), "data: [DONE]")
}

func TestForwardResponses_ForceChatCompletionsRoutesNonStreamingToChatCompletions(t *testing.T) {
	gin.SetMode(gin.TestMode)

	body := []byte(`{"model":"gpt-5.4","input":"hello","stream":false}`)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")

	upstream := &httpUpstreamRecorder{resp: &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"application/json"}, "x-request-id": []string{"rid_resp_chat_json"}},
		Body: io.NopCloser(strings.NewReader(
			`{"id":"chatcmpl_json","object":"chat.completion","model":"gpt-5.4","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":3,"completion_tokens":2,"total_tokens":5,"prompt_tokens_details":{"cached_tokens":1}}}`,
		)),
	}}
	svc := &OpenAIGatewayService{
		cfg:          rawChatCompletionsTestConfig(),
		httpUpstream: upstream,
	}

	result, err := svc.Forward(context.Background(), c, forceChatResponsesFallbackAccount(), body)
	require.NoError(t, err)
	require.NotNil(t, result)
	require.Equal(t, "http://upstream.example/v1/chat/completions", upstream.lastReq.URL.String())
	require.Equal(t, HTTPUpstreamProfileOpenAI, HTTPUpstreamProfileFromContext(upstream.lastReq.Context()))
	require.Equal(t, "hello", gjson.GetBytes(upstream.lastBody, "messages.0.content").String())
	require.False(t, gjson.GetBytes(upstream.lastBody, "input").Exists())
	require.Equal(t, "response", gjson.Get(rec.Body.String(), "object").String())
	require.Equal(t, "ok", gjson.Get(rec.Body.String(), "output.0.content.0.text").String())
	require.Equal(t, 3, result.Usage.InputTokens)
	require.Equal(t, 2, result.Usage.OutputTokens)
	require.Equal(t, 1, result.Usage.CacheReadInputTokens)
	require.False(t, result.Stream)
}

func TestForwardResponses_ForceChatCompletionsRoutesStreamingToChatCompletions(t *testing.T) {
	gin.SetMode(gin.TestMode)

	body := []byte(`{"model":"gpt-5.4","input":"hello","stream":true}`)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")

	upstreamBody := strings.Join([]string{
		`data: {"id":"chatcmpl_stream","object":"chat.completion.chunk","model":"gpt-5.4","choices":[{"index":0,"delta":{"role":"assistant"},"finish_reason":null}]}`,
		"",
		`data: {"id":"chatcmpl_stream","object":"chat.completion.chunk","model":"gpt-5.4","choices":[{"index":0,"delta":{"content":"he"},"finish_reason":null}]}`,
		"",
		`data: {"id":"chatcmpl_stream","object":"chat.completion.chunk","model":"gpt-5.4","choices":[{"index":0,"delta":{"content":"llo"},"finish_reason":null}]}`,
		"",
		`data: {"id":"chatcmpl_stream","object":"chat.completion.chunk","model":"gpt-5.4","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`,
		"",
		`data: {"id":"chatcmpl_stream","object":"chat.completion.chunk","model":"gpt-5.4","choices":[],"usage":{"prompt_tokens":4,"completion_tokens":3,"total_tokens":7}}`,
		"",
		"data: [DONE]",
		"",
	}, "\n")
	upstream := &httpUpstreamRecorder{resp: &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}, "x-request-id": []string{"rid_resp_chat_stream"}},
		Body:       io.NopCloser(strings.NewReader(upstreamBody)),
	}}
	svc := &OpenAIGatewayService{
		cfg:          rawChatCompletionsTestConfig(),
		httpUpstream: upstream,
	}

	result, err := svc.Forward(context.Background(), c, forceChatResponsesFallbackAccount(), body)
	require.NoError(t, err)
	require.NotNil(t, result)
	require.Equal(t, "http://upstream.example/v1/chat/completions", upstream.lastReq.URL.String())
	require.True(t, gjson.GetBytes(upstream.lastBody, "stream_options.include_usage").Bool())
	require.Contains(t, rec.Body.String(), "event: response.output_text.delta")
	require.Contains(t, rec.Body.String(), `"delta":"he"`)
	require.Contains(t, rec.Body.String(), "event: response.completed")
	require.Contains(t, rec.Body.String(), `"input_tokens":4`)
	require.Contains(t, rec.Body.String(), "data: [DONE]")
	require.Equal(t, 4, result.Usage.InputTokens)
	require.Equal(t, 3, result.Usage.OutputTokens)
	require.True(t, result.Stream)
	require.NotNil(t, result.FirstTokenMs)
}

func TestForwardResponses_DeepSeekReasoningOnlyStreamProducesVisibleText(t *testing.T) {
	gin.SetMode(gin.TestMode)

	body := []byte(`{"model":"deepseek-reasoner","input":"hello","stream":true}`)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")

	upstreamBody := strings.Join([]string{
		`data: {"id":"chatcmpl_reasoning","object":"chat.completion.chunk","model":"deepseek-reasoner","choices":[{"index":0,"delta":{"role":"assistant","content":null,"reasoning_content":""},"finish_reason":null}]}`,
		"",
		`data: {"id":"chatcmpl_reasoning","object":"chat.completion.chunk","model":"deepseek-reasoner","choices":[{"index":0,"delta":{"reasoning_content":"visible fallback"},"finish_reason":null}]}`,
		"",
		`data: {"id":"chatcmpl_reasoning","object":"chat.completion.chunk","model":"deepseek-reasoner","choices":[{"index":0,"delta":{"content":""},"finish_reason":"length"}],"usage":{"prompt_tokens":4,"completion_tokens":3,"total_tokens":7}}`,
		"",
		"data: [DONE]",
		"",
	}, "\n")
	upstream := &httpUpstreamRecorder{resp: &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}, "x-request-id": []string{"rid_deepseek_reasoning_responses_stream"}},
		Body:       io.NopCloser(strings.NewReader(upstreamBody)),
	}}
	svc := &OpenAIGatewayService{
		cfg:          rawChatCompletionsTestConfig(),
		httpUpstream: upstream,
	}

	result, err := svc.Forward(context.Background(), c, forceChatResponsesFallbackAccount(), body)
	require.NoError(t, err)
	require.NotNil(t, result)
	require.True(t, result.Stream)
	require.Contains(t, rec.Body.String(), "event: response.output_text.delta")
	require.Contains(t, rec.Body.String(), `"delta":"visible fallback"`)
	require.Contains(t, rec.Body.String(), `"status":"incomplete"`)
	require.Contains(t, rec.Body.String(), "data: [DONE]")
}

func TestForwardResponses_AutoSupportedAccountStillUsesResponsesEndpoint(t *testing.T) {
	gin.SetMode(gin.TestMode)

	body := []byte(`{"model":"gpt-5.4","input":"hello","stream":false}`)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")

	upstream := &httpUpstreamRecorder{resp: &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"application/json"}, "x-request-id": []string{"rid_resp_native"}},
		Body: io.NopCloser(strings.NewReader(
			`{"id":"resp_native","object":"response","model":"gpt-5.4","status":"completed","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"ok"}],"status":"completed"}],"usage":{"input_tokens":5,"output_tokens":2,"total_tokens":7}}`,
		)),
	}}
	svc := &OpenAIGatewayService{
		cfg:          rawChatCompletionsTestConfig(),
		httpUpstream: upstream,
	}
	account := rawChatCompletionsTestAccount()
	account.Extra = map[string]any{
		openai_compat.ExtraKeyResponsesMode:      string(openai_compat.ResponsesSupportModeAuto),
		openai_compat.ExtraKeyResponsesSupported: true,
	}

	result, err := svc.Forward(context.Background(), c, account, body)
	require.NoError(t, err)
	require.NotNil(t, result)
	require.Equal(t, "http://upstream.example/v1/responses", upstream.lastReq.URL.String())
	require.True(t, gjson.GetBytes(upstream.lastBody, "input").Exists())
	require.False(t, gjson.GetBytes(upstream.lastBody, "messages").Exists())
	require.Equal(t, "ok", gjson.Get(rec.Body.String(), "output.0.content.0.text").String())
}

func forceChatResponsesFallbackAccount() *Account {
	account := rawChatCompletionsTestAccount()
	account.Extra = map[string]any{
		openai_compat.ExtraKeyResponsesMode: string(openai_compat.ResponsesSupportModeForceChatCompletions),
	}
	return account
}
