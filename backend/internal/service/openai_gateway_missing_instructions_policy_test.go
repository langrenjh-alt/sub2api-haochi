package service

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestForwardEmptyMissingInstructionsPolicyAvoidsCodexBasePrompt(t *testing.T) {
	gin.SetMode(gin.TestMode)

	tests := []struct {
		name        string
		accountType string
		credentials map[string]any
	}{
		{
			name:        "api key",
			accountType: AccountTypeAPIKey,
			credentials: map[string]any{"api_key": "sk-test", "base_url": "https://example.com"},
		},
		{
			name:        "oauth",
			accountType: AccountTypeOAuth,
			credentials: map[string]any{"access_token": "oauth-test", "chatgpt_account_id": "acct-test"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			upstream := &httpUpstreamRecorder{resp: &http.Response{
				StatusCode: http.StatusOK,
				Header:     http.Header{"Content-Type": []string{"application/json"}},
				Body: io.NopCloser(strings.NewReader(
					`{"id":"resp_test","status":"completed","usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2}}`,
				)),
			}}
			cfg := &config.Config{}
			cfg.Security.URLAllowlist.Enabled = false
			cfg.Gateway.OpenAIMissingInstructionsPolicy = config.OpenAIMissingInstructionsPolicyEmpty
			svc := &OpenAIGatewayService{cfg: cfg, httpUpstream: upstream}
			account := &Account{
				ID:          1,
				Name:        tt.name,
				Platform:    PlatformOpenAI,
				Type:        tt.accountType,
				Concurrency: 1,
				Credentials: tt.credentials,
				Extra:       map[string]any{"use_responses_api": true},
			}
			body := []byte(`{"model":"gpt-5.4","stream":false,"input":[{"role":"user","content":"hi"}]}`)
			rec := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(rec)
			c.Request = httptest.NewRequest(http.MethodPost, "/openai/v1/responses", strings.NewReader(string(body)))
			SetOpenAIClientTransport(c, OpenAIClientTransportHTTP)

			result, err := svc.Forward(context.Background(), c, account, body)
			require.NoError(t, err)
			require.NotNil(t, result)
			require.True(t, gjson.GetBytes(upstream.lastBody, "instructions").Exists())
			require.Empty(t, gjson.GetBytes(upstream.lastBody, "instructions").String())
			require.NotContains(t, string(upstream.lastBody), "You are Codex")
			require.Less(t, len(upstream.lastBody), 1024)
		})
	}
}

func TestOpenAIMissingInstructionsPolicyKeepsLegacyDefault(t *testing.T) {
	cfg := &config.Config{}
	cfg.Gateway.OpenAIMissingInstructionsPolicy = config.OpenAIMissingInstructionsPolicyEmpty
	svc := &OpenAIGatewayService{cfg: cfg}
	require.Equal(t, "", svc.openAIMissingInstructions(context.Background(), "gpt-5.4"))
	require.NotEmpty(t, (&OpenAIGatewayService{}).openAIMissingInstructions(context.Background(), "gpt-5.4"))
}

func TestOpenAILowLatencyModeEnablesLatencyPolicies(t *testing.T) {
	cfg := &config.Config{}
	cfg.Gateway.OpenAILatencyMode = config.OpenAILatencyModeLowLatency
	cfg.Gateway.OpenAIStreamFlushPreamble = false
	cfg.Gateway.OpenAIMissingInstructionsPolicy = config.OpenAIMissingInstructionsPolicyCodex
	svc := &OpenAIGatewayService{cfg: cfg}

	ctx := context.Background()
	require.True(t, svc.openAILowLatencyMode(ctx))
	require.True(t, svc.shouldFlushOpenAIStreamPreamble(ctx))
	require.True(t, svc.useEmptyOpenAIMissingInstructions(ctx))
	require.Empty(t, svc.openAIMissingInstructions(ctx, "gpt-5.4"))
}

func TestOpenAICompatibleModeKeepsLatencyPoliciesOptIn(t *testing.T) {
	cfg := &config.Config{}
	cfg.Gateway.OpenAILatencyMode = config.OpenAILatencyModeCompatible
	cfg.Gateway.OpenAIStreamFlushPreamble = false
	cfg.Gateway.OpenAIMissingInstructionsPolicy = config.OpenAIMissingInstructionsPolicyCodex
	svc := &OpenAIGatewayService{cfg: cfg}

	ctx := context.Background()
	require.False(t, svc.openAILowLatencyMode(ctx))
	require.False(t, svc.shouldFlushOpenAIStreamPreamble(ctx))
	require.False(t, svc.useEmptyOpenAIMissingInstructions(ctx))
	require.NotEmpty(t, svc.openAIMissingInstructions(ctx, "gpt-5.4"))
}
