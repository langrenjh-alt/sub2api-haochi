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
)

func openAITransportHeaderTestConfig() *config.Config {
	return &config.Config{Security: config.SecurityConfig{
		URLAllowlist: config.URLAllowlistConfig{Enabled: false},
	}}
}

func TestBuildUpstreamRequestStreamingUsesIdentityEncoding(t *testing.T) {
	gin.SetMode(gin.TestMode)
	svc := &OpenAIGatewayService{cfg: openAITransportHeaderTestConfig()}
	account := &Account{ID: 1, Platform: PlatformOpenAI, Type: AccountTypeAPIKey}

	newContext := func(body string) *gin.Context {
		rec := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(rec)
		c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(body))
		return c
	}

	streamContext := newContext(`{"model":"gpt-5","stream":true}`)
	streamReq, err := svc.buildUpstreamRequest(
		streamContext.Request.Context(), streamContext, account,
		[]byte(`{"model":"gpt-5","stream":true}`), "token", true, "", false,
	)
	require.NoError(t, err)
	require.Equal(t, "identity", streamReq.Header.Get("Accept-Encoding"))

	nonStreamContext := newContext(`{"model":"gpt-5","stream":false}`)
	nonStreamReq, err := svc.buildUpstreamRequest(
		nonStreamContext.Request.Context(), nonStreamContext, account,
		[]byte(`{"model":"gpt-5","stream":false}`), "token", false, "", false,
	)
	require.NoError(t, err)
	require.Empty(t, nonStreamReq.Header.Get("Accept-Encoding"))
}

func TestBuildUpstreamRequestPassthroughStreamingUsesIdentityEncoding(t *testing.T) {
	gin.SetMode(gin.TestMode)
	svc := &OpenAIGatewayService{cfg: openAITransportHeaderTestConfig()}
	account := &Account{ID: 1, Platform: PlatformOpenAI, Type: AccountTypeAPIKey}
	body := []byte(`{"model":"gpt-5","stream":true}`)

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(string(body)))
	c.Request.Header.Set("Accept-Encoding", "gzip")

	req, err := svc.buildUpstreamRequestOpenAIPassthrough(context.Background(), c, account, body, "token")
	require.NoError(t, err)
	require.Equal(t, "identity", req.Header.Get("Accept-Encoding"))
}

func TestSendCCUpstreamRequestStreamingUsesIdentityEncoding(t *testing.T) {
	gin.SetMode(gin.TestMode)
	upstream := &httpUpstreamRecorder{resp: &http.Response{
		StatusCode: http.StatusOK,
		Header:     make(http.Header),
		Body:       io.NopCloser(strings.NewReader("")),
	}}
	svc := &OpenAIGatewayService{httpUpstream: upstream}
	account := &Account{ID: 1, Platform: PlatformOpenAI, Type: AccountTypeAPIKey}

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"stream":true}`))

	resp, err := svc.sendCCUpstreamRequest(
		context.Background(), c, account, "https://api.openai.com/v1/chat/completions",
		[]byte(`{"stream":true}`), true, "token", "", "",
	)
	require.NoError(t, err)
	require.NotNil(t, resp)
	require.Equal(t, "identity", upstream.lastReq.Header.Get("Accept-Encoding"))
}
