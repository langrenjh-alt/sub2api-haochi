package service

import (
	"fmt"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

const (
	grokConversationIDHeader        = "X-Grok-Conv-Id"
	grokFreeCacheNativeToolsJSON    = `[{"type":"web_search"},{"type":"x_search"}]`
	grokFreeCacheWebSearchToolJSON  = `{"type":"web_search"}`
	grokFreeCacheXSearchToolJSON    = `{"type":"x_search"}`
	grokFreeCacheDisabledToolChoice = "none"
)

// resolveGrokCacheIdentity derives one stable, tenant-isolated routing identity
// for xAI's server-side prompt cache. The returned value is safe to expose to
// the upstream: it never contains the client's raw session identifier.
//
// A valid downstream API key is required. This intentionally fails closed on
// internal probes and incomplete request contexts instead of creating a cache
// identity that could be shared by unrelated tenants.
func resolveGrokCacheIdentity(c *gin.Context, body []byte, explicitKey, upstreamModel string) string {
	apiKeyID := getAPIKeyIDFromContext(c)
	if apiKeyID <= 0 {
		return ""
	}
	// /responses/compact rejects tool_choice and does not represent a normal
	// conversation turn. Keep both cache identity and Free-tier routing
	// augmentation out of this path.
	if isOpenAIResponsesCompactPath(c) {
		return ""
	}

	model := strings.ToLower(strings.TrimSpace(upstreamModel))
	if model == "" {
		return ""
	}

	seed := explicitGrokCacheSeed(c, body, explicitKey)
	if seed == "" {
		seed = deriveOpenAIStablePrefixSessionSeed(body)
		if seed == "" {
			// A model alone is too broad for cache routing. Preserve the
			// existing first-user-derived identity when no reusable prefix is
			// available so unrelated prompts do not share one tenant-wide key.
			seed = deriveOpenAIAnchoredContentSessionSeed(body)
		}
	}
	if seed == "" {
		return ""
	}

	// generateSessionUUID hashes the whole seed before formatting it as a UUID.
	// Include a versioned namespace so this identity cannot collide with other
	// upstream session identifiers derived by sub2api.
	isolatedSeed := fmt.Sprintf("grok-prompt-cache:v1:%d:%s:%s", apiKeyID, model, seed)
	return generateSessionUUID(isolatedSeed)
}

func explicitGrokCacheSeed(c *gin.Context, body []byte, explicitKey string) string {
	seed := ""
	if c != nil {
		seed = strings.TrimSpace(c.GetHeader("session_id"))
		if seed == "" {
			seed = strings.TrimSpace(c.GetHeader("conversation_id"))
		}
		if seed == "" {
			// OpenAI-compatible agent clients commonly send the stable session in
			// hyphenated headers rather than the underscore-style Codex headers.
			for _, header := range []string{"x-opencode-session", "X-Session-Id", "x-session-affinity"} {
				seed = strings.TrimSpace(c.GetHeader(header))
				if seed != "" {
					break
				}
			}
		}
		if seed == "" {
			seed = strings.TrimSpace(c.GetHeader(grokConversationIDHeader))
		}
	}
	if seed == "" && len(body) > 0 {
		seed = strings.TrimSpace(gjson.GetBytes(body, "prompt_cache_key").String())
	}
	if seed == "" {
		seed = strings.TrimSpace(explicitKey)
	}
	return seed
}

func isGrokRequestContext(c *gin.Context) bool {
	if c == nil {
		return false
	}
	v, exists := c.Get("api_key")
	if !exists {
		return false
	}
	apiKey, ok := v.(*APIKey)
	return ok && apiKey != nil && apiKey.Group != nil && apiKey.Group.Platform == PlatformGrok
}

// applyGrokResponsesCacheIdentity writes the cache routing identity into an
// xAI Responses request. Existing client values are deliberately replaced by
// the tenant-isolated value to prevent collisions on shared OAuth accounts.
//
// Free OAuth requests without native search tools are routed by xAI to the
// non-cacheable build-free model. Add any missing native search tools to
// supported client tool sets so agent requests can reach the cache-capable
// tier. Existing client tools and tool_choice are preserved; consequently an
// auto/required choice may select a native search tool. Tool-free requests keep
// tool_choice=none so the routing augmentation cannot trigger a search.
func applyGrokResponsesCacheIdentity(body, intentSourceBody []byte, identity string, injectFreeTierTools bool) ([]byte, error) {
	identity = strings.TrimSpace(identity)
	if identity == "" {
		if gjson.GetBytes(body, "prompt_cache_key").Exists() {
			return sjson.DeleteBytes(body, "prompt_cache_key")
		}
		return body, nil
	}
	out, err := sjson.SetBytes(body, "prompt_cache_key", identity)
	if err != nil {
		return nil, err
	}
	if !injectFreeTierTools {
		return out, nil
	}
	return applyGrokFreeCacheNativeTools(out, intentSourceBody)
}

func applyGrokFreeCacheNativeTools(body, intentSourceBody []byte) ([]byte, error) {
	tools := gjson.GetBytes(body, "tools")
	if tools.Exists() && tools.IsArray() {
		items := tools.Array()
		if len(items) > 0 {
			hasSupportedTool := false
			hasWebSearch := false
			hasXSearch := false
			for _, tool := range items {
				toolType := strings.TrimSpace(tool.Get("type").String())
				if _, ok := grokResponsesSupportedToolTypes[toolType]; ok {
					hasSupportedTool = true
				}
				switch toolType {
				case "web_search":
					hasWebSearch = true
				case "x_search":
					hasXSearch = true
				}
			}
			// apply normally receives a sanitized body. Keep this guard for
			// malformed or future unsupported-only tool arrays so they do not
			// silently turn into a search-only request.
			if !hasSupportedTool {
				return body, nil
			}
			if hasWebSearch && hasXSearch {
				return body, nil
			}

			// Rewrite the array once rather than calling sjson once per missing
			// tool. Agent requests can carry a large replay history, so avoiding
			// the second whole-body copy keeps this hot path inexpensive.
			rawTools := strings.TrimSpace(tools.Raw)
			if len(rawTools) < 2 || rawTools[0] != '[' || rawTools[len(rawTools)-1] != ']' {
				return body, nil
			}
			merged := make([]byte, 0, len(rawTools)+len(grokFreeCacheWebSearchToolJSON)+len(grokFreeCacheXSearchToolJSON)+2)
			merged = append(merged, rawTools[:len(rawTools)-1]...)
			if !hasWebSearch {
				merged = append(merged, ',')
				merged = append(merged, grokFreeCacheWebSearchToolJSON...)
			}
			if !hasXSearch {
				merged = append(merged, ',')
				merged = append(merged, grokFreeCacheXSearchToolJSON...)
			}
			merged = append(merged, ']')
			return sjson.SetRawBytes(body, "tools", merged)
		}
	}

	// Preserve malformed non-array tool values so upstream validation remains
	// visible. Missing, null, and empty tools remain eligible for the disabled
	// native-tool route; sanitizer-removed explicit intent is handled below.
	if tools.Exists() && tools.Type != gjson.Null && !tools.IsArray() {
		return body, nil
	}
	// If sanitization removed a non-empty unsupported tool declaration (or a
	// standalone active tool_choice), retain the prior fail-closed behavior
	// rather than silently replacing that rejected intent with search tools.
	if hasGrokExplicitToolIntent(intentSourceBody) {
		return body, nil
	}
	out, err := sjson.SetRawBytes(body, "tools", []byte(grokFreeCacheNativeToolsJSON))
	if err != nil {
		return nil, err
	}
	return sjson.SetBytes(out, "tool_choice", grokFreeCacheDisabledToolChoice)
}

func hasGrokExplicitToolIntent(body []byte) bool {
	tools := gjson.GetBytes(body, "tools")
	if tools.Exists() && tools.Type != gjson.Null {
		if !tools.IsArray() || len(tools.Array()) > 0 {
			return true
		}
	}

	choice := gjson.GetBytes(body, "tool_choice")
	if !choice.Exists() || choice.Type == gjson.Null {
		return false
	}
	return choice.Type != gjson.String || !strings.EqualFold(strings.TrimSpace(choice.String()), grokFreeCacheDisabledToolChoice)
}

// applyGrokCacheHeaders applies the documented Chat Completions conversation
// routing header. The request is built from a fresh header map, so client
// supplied x-grok headers cannot override this server-derived value.
func applyGrokCacheHeaders(headers http.Header, identity string) {
	if headers == nil {
		return
	}
	identity = strings.TrimSpace(identity)
	if identity == "" {
		headers.Del(grokConversationIDHeader)
		return
	}
	headers.Set(grokConversationIDHeader, identity)
}

// stripGrokChatPromptCacheKey removes the Responses-only body field after it
// has been used as an identity seed. Chat Completions routes cache by header.
func stripGrokChatPromptCacheKey(body []byte) ([]byte, error) {
	if !gjson.GetBytes(body, "prompt_cache_key").Exists() {
		return body, nil
	}
	return sjson.DeleteBytes(body, "prompt_cache_key")
}
