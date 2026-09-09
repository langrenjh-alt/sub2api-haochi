package service

import (
	"strings"

	"github.com/gin-gonic/gin"
)

// OpenAIClientTransport 表示客户端入站协议类型。
type OpenAIClientTransport string

const (
	OpenAIClientTransportUnknown OpenAIClientTransport = ""
	OpenAIClientTransportHTTP    OpenAIClientTransport = "http"
	OpenAIClientTransportWS      OpenAIClientTransport = "ws"
)

const openAIClientTransportContextKey = "openai_client_transport"

// SetOpenAIClientTransport 标记当前请求的客户端入站协议。
func SetOpenAIClientTransport(c *gin.Context, transport OpenAIClientTransport) {
	if c == nil {
		return
	}
	normalized := normalizeOpenAIClientTransport(transport)
	if normalized == OpenAIClientTransportUnknown {
		return
	}
	c.Set(openAIClientTransportContextKey, string(normalized))
}

// GetOpenAIClientTransport 读取当前请求的客户端入站协议。
func GetOpenAIClientTransport(c *gin.Context) OpenAIClientTransport {
	if c == nil {
		return OpenAIClientTransportUnknown
	}
	raw, ok := c.Get(openAIClientTransportContextKey)
	if !ok || raw == nil {
		return OpenAIClientTransportUnknown
	}

	switch v := raw.(type) {
	case OpenAIClientTransport:
		return normalizeOpenAIClientTransport(v)
	case string:
		return normalizeOpenAIClientTransport(OpenAIClientTransport(v))
	default:
		return OpenAIClientTransportUnknown
	}
}

func normalizeOpenAIClientTransport(transport OpenAIClientTransport) OpenAIClientTransport {
	switch strings.ToLower(strings.TrimSpace(string(transport))) {
	case string(OpenAIClientTransportHTTP), "http_sse", "sse":
		return OpenAIClientTransportHTTP
	case string(OpenAIClientTransportWS), "websocket":
		return OpenAIClientTransportWS
	default:
		return OpenAIClientTransportUnknown
	}
}

func resolveOpenAIWSDecisionByClientTransport(
	decision OpenAIWSProtocolDecision,
	clientTransport OpenAIClientTransport,
) OpenAIWSProtocolDecision {
	// HTTP 入站不再强制改走 HTTP 上游。
	// 全局 + 账号 WS 打开时，resolver 给出 ctx_pool/passthrough → 上游走 WS 池，
	// Forward/forwardOpenAIWSV2 仍把事件写成客户端 HTTP/SSE。
	// 账号 off、http_bridge、全局关闭时 resolver 已是 HTTP，这里保持原决策。
	if clientTransport == OpenAIClientTransportHTTP && openAIWSDecisionUsesUpstreamWS(decision) {
		if decision.Reason == "" {
			decision.Reason = "http_ingress"
		} else if !strings.HasSuffix(decision.Reason, "_http_ingress") {
			decision.Reason += "_http_ingress"
		}
	}
	return decision
}

func openAIWSDecisionUsesUpstreamWS(decision OpenAIWSProtocolDecision) bool {
	return decision.Transport == OpenAIUpstreamTransportResponsesWebsocketV2 ||
		decision.Transport == OpenAIUpstreamTransportResponsesWebsocket
}
