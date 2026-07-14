//go:build unit

package service

import (
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func newGrokCacheTestContext(apiKeyID int64) *gin.Context {
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	if apiKeyID > 0 {
		c.Set("api_key", &APIKey{ID: apiKeyID, Group: &Group{Platform: PlatformGrok}})
	}
	return c
}

func TestResolveGrokCacheIdentityStableAcrossAppendOnlyTurns(t *testing.T) {
	gin.SetMode(gin.TestMode)
	c := newGrokCacheTestContext(101)
	round1 := []byte(`{"model":"grok","instructions":"be concise","tools":[{"type":"function","name":"lookup","parameters":{"type":"object"}}],"input":[{"role":"user","content":"first question"}]}`)
	round2 := []byte(`{"model":"grok","instructions":"be concise","tools":[{"type":"function","name":"lookup","parameters":{"type":"object"}}],"input":[{"role":"user","content":"first question"},{"role":"assistant","content":"first answer"},{"role":"user","content":"second question"}]}`)

	first := resolveGrokCacheIdentity(c, round1, "", "grok-4.5")
	second := resolveGrokCacheIdentity(c, round2, "", "grok-4.5")

	require.NotEmpty(t, first)
	require.Len(t, first, 36)
	require.Equal(t, first, second)
}

func TestResolveGrokCacheIdentityStableAcrossIndependentPromptsWithSamePrefix(t *testing.T) {
	gin.SetMode(gin.TestMode)
	c := newGrokCacheTestContext(102)
	firstBody := []byte(`{"model":"grok","instructions":"be concise","tools":[{"type":"function","name":"lookup"}],"input":[{"role":"user","content":"Question A"}]}`)
	secondBody := []byte(`{"model":"grok","instructions":"be concise","tools":[{"type":"function","name":"lookup"}],"input":[{"role":"user","content":"Question B"}]}`)

	first := resolveGrokCacheIdentity(c, firstBody, "", "grok-4.5")
	second := resolveGrokCacheIdentity(c, secondBody, "", "grok-4.5")

	require.NotEmpty(t, first)
	require.Equal(t, first, second)
}

func TestResolveGrokCacheIdentityStablePrefixIsolation(t *testing.T) {
	gin.SetMode(gin.TestMode)
	baseBody := []byte(`{"model":"grok","instructions":"be concise","tools":[{"type":"function","name":"lookup"}],"input":[{"role":"system","content":"System A"},{"role":"user","content":"Question A"}]}`)
	differentInstructions := []byte(`{"model":"grok","instructions":"be detailed","tools":[{"type":"function","name":"lookup"}],"input":[{"role":"system","content":"System A"},{"role":"user","content":"Question B"}]}`)
	differentSystem := []byte(`{"model":"grok","instructions":"be concise","tools":[{"type":"function","name":"lookup"}],"input":[{"role":"system","content":"System B"},{"role":"user","content":"Question B"}]}`)
	differentTools := []byte(`{"model":"grok","instructions":"be concise","tools":[{"type":"function","name":"search"}],"input":[{"role":"system","content":"System A"},{"role":"user","content":"Question B"}]}`)

	base := resolveGrokCacheIdentity(newGrokCacheTestContext(103), baseBody, "", "grok-4.5")
	require.NotEqual(t, base, resolveGrokCacheIdentity(newGrokCacheTestContext(104), baseBody, "", "grok-4.5"))
	require.NotEqual(t, base, resolveGrokCacheIdentity(newGrokCacheTestContext(103), baseBody, "", "grok-4.3"))
	require.NotEqual(t, base, resolveGrokCacheIdentity(newGrokCacheTestContext(103), differentInstructions, "", "grok-4.5"))
	require.NotEqual(t, base, resolveGrokCacheIdentity(newGrokCacheTestContext(103), differentSystem, "", "grok-4.5"))
	require.NotEqual(t, base, resolveGrokCacheIdentity(newGrokCacheTestContext(103), differentTools, "", "grok-4.5"))
}

func TestResolveGrokCacheIdentityFallsBackWhenStablePrefixIsEmpty(t *testing.T) {
	gin.SetMode(gin.TestMode)
	c := newGrokCacheTestContext(105)
	firstBody := []byte(`{"model":"grok","tools":[],"input":"Question A"}`)
	secondBody := []byte(`{"model":"grok","tools":[],"input":"Question B"}`)

	first := resolveGrokCacheIdentity(c, firstBody, "", "grok-4.5")
	second := resolveGrokCacheIdentity(c, secondBody, "", "grok-4.5")

	require.NotEmpty(t, first)
	require.NotEmpty(t, second)
	require.NotEqual(t, first, second)
}

func TestResolveGrokCacheIdentitySkipsUnanchoredFallback(t *testing.T) {
	gin.SetMode(gin.TestMode)
	c := newGrokCacheTestContext(106)
	tests := [][]byte{
		[]byte(`{"model":"grok"}`),
		[]byte(`{"model":"grok","messages":[{"role":"assistant","content":"answer"}]}`),
		[]byte(`{"model":"grok","messages":[{"role":"user","content":""}]}`),
		[]byte(`{"model":"grok","input":"  "}`),
	}

	for _, body := range tests {
		require.Empty(t, resolveGrokCacheIdentity(c, body, "", "grok-4.5"))
	}
}

func TestResolveGrokCacheIdentityIsolatesAPIKeyAndMappedModel(t *testing.T) {
	gin.SetMode(gin.TestMode)
	body := []byte(`{"model":"grok","input":"same prompt"}`)

	base := resolveGrokCacheIdentity(newGrokCacheTestContext(201), body, "", "grok-4.5")
	otherTenant := resolveGrokCacheIdentity(newGrokCacheTestContext(202), body, "", "grok-4.5")
	otherModel := resolveGrokCacheIdentity(newGrokCacheTestContext(201), body, "", "grok-4.3")

	require.NotEmpty(t, base)
	require.NotEqual(t, base, otherTenant)
	require.NotEqual(t, base, otherModel)
}

func TestResolveGrokCacheIdentityUsesAndIsolatesNativeConversationHeader(t *testing.T) {
	gin.SetMode(gin.TestMode)
	c := newGrokCacheTestContext(301)
	c.Request.Header.Set(grokConversationIDHeader, "raw-native-conversation")
	body1 := []byte(`{"model":"grok","input":"one"}`)
	body2 := []byte(`{"model":"grok","input":"different body that must not replace the explicit session"}`)

	first := resolveGrokCacheIdentity(c, body1, "body-cache-key", "grok-4.5")
	second := resolveGrokCacheIdentity(c, body2, "another-body-cache-key", "grok-4.5")

	require.Equal(t, "raw-native-conversation", (&OpenAIGatewayService{}).ExtractSessionID(c, body1))
	require.Equal(t, first, second)
	require.NotEqual(t, "raw-native-conversation", first)
	require.NotContains(t, first, "raw-native-conversation")
}

func TestResolveGrokCacheIdentityExplicitHeaderPriority(t *testing.T) {
	gin.SetMode(gin.TestMode)
	body := []byte(`{"model":"grok","prompt_cache_key":"body-key","input":"hi"}`)
	c := newGrokCacheTestContext(401)
	c.Request.Header.Set(grokConversationIDHeader, "grok-key")
	c.Request.Header.Set("conversation_id", "conversation-key")
	c.Request.Header.Set("session_id", "session-key")

	got := resolveGrokCacheIdentity(c, body, "explicit-argument", "grok-4.5")
	onlySession := newGrokCacheTestContext(401)
	onlySession.Request.Header.Set("session_id", "session-key")
	want := resolveGrokCacheIdentity(onlySession, []byte(`{"model":"grok","input":"unrelated"}`), "", "grok-4.5")

	require.Equal(t, want, got)
}

func TestResolveGrokCacheIdentityFailsClosedWithoutAPIKeyContext(t *testing.T) {
	gin.SetMode(gin.TestMode)
	c := newGrokCacheTestContext(0)
	c.Request.Header.Set(grokConversationIDHeader, "native-session")

	require.Empty(t, resolveGrokCacheIdentity(c, []byte(`{"model":"grok","input":"hi"}`), "", "grok-4.5"))
	require.Empty(t, resolveGrokCacheIdentity(nil, []byte(`{"model":"grok","prompt_cache_key":"key"}`), "key", "grok-4.5"))
}

func TestGrokConversationHeaderIsScopedToGrokRequestScheduling(t *testing.T) {
	gin.SetMode(gin.TestMode)
	body := []byte(`{"model":"grok","prompt_cache_key":"body-session","input":"hi"}`)

	grokContext := newGrokCacheTestContext(601)
	grokContext.Request.Header.Set(grokConversationIDHeader, "native-grok-session")
	require.Equal(t, "native-grok-session", (&OpenAIGatewayService{}).ExtractSessionID(grokContext, body))

	openAIContext := newGrokCacheTestContext(601)
	openAIContext.Set("api_key", &APIKey{ID: 601, Group: &Group{Platform: PlatformOpenAI}})
	openAIContext.Request.Header.Set(grokConversationIDHeader, "must-be-ignored")
	require.Equal(t, "body-session", (&OpenAIGatewayService{}).ExtractSessionID(openAIContext, body))

	withoutGrokHeader := newGrokCacheTestContext(601)
	withoutGrokHeader.Set("api_key", &APIKey{ID: 601, Group: &Group{Platform: PlatformOpenAI}})
	require.Equal(t,
		(&OpenAIGatewayService{}).GenerateSessionHash(withoutGrokHeader, body),
		(&OpenAIGatewayService{}).GenerateSessionHash(openAIContext, body),
	)
}

func TestApplyGrokCacheIdentityWritesResponsesBodyAndHeader(t *testing.T) {
	sourceBody := []byte(`{"model":"grok-4.5","prompt_cache_key":"raw-client-key"}`)
	body, err := applyGrokResponsesCacheIdentity(sourceBody, sourceBody, "isolated-id", true)
	require.NoError(t, err)
	require.Equal(t, "isolated-id", gjson.GetBytes(body, "prompt_cache_key").String())
	require.Equal(t, "web_search", gjson.GetBytes(body, "tools.0.type").String())
	require.Equal(t, "x_search", gjson.GetBytes(body, "tools.1.type").String())
	require.Equal(t, grokFreeCacheDisabledToolChoice, gjson.GetBytes(body, "tool_choice").String())

	headers := make(http.Header)
	headers.Set(grokConversationIDHeader, "spoofed-client-value")
	applyGrokCacheHeaders(headers, "isolated-id")
	require.Equal(t, "isolated-id", headers.Get(grokConversationIDHeader))
	applyGrokCacheHeaders(headers, "")
	require.Empty(t, headers.Get(grokConversationIDHeader))

	chatBody, err := stripGrokChatPromptCacheKey(body)
	require.NoError(t, err)
	require.False(t, gjson.GetBytes(chatBody, "prompt_cache_key").Exists())

	unscopedSourceBody := []byte(`{"model":"grok","prompt_cache_key":"raw-client-key"}`)
	unscopedBody, err := applyGrokResponsesCacheIdentity(unscopedSourceBody, unscopedSourceBody, "", true)
	require.NoError(t, err)
	require.False(t, gjson.GetBytes(unscopedBody, "prompt_cache_key").Exists())
	require.False(t, gjson.GetBytes(unscopedBody, "tools").Exists())
	require.False(t, gjson.GetBytes(unscopedBody, "tool_choice").Exists())
}

func TestApplyGrokCacheIdentityAppendsNativeToolsToSupportedClientTools(t *testing.T) {
	tests := []struct {
		name string
		body string
	}{
		{
			name: "function without tool choice",
			body: `{"model":"grok","tools":[{"type":"function","name":"lookup","parameters":{"type":"object"}}]}`,
		},
		{
			name: "function with auto tool choice",
			body: `{"model":"grok","tools":[{"type":"function","name":"lookup","parameters":{"type":"object"}}],"tool_choice":"auto"}`,
		},
		{
			name: "function with required tool choice",
			body: `{"model":"grok","tools":[{"type":"function","name":"lookup","parameters":{"type":"object"}}],"tool_choice":"required"}`,
		},
		{
			name: "function with none tool choice",
			body: `{"model":"grok","tools":[{"type":"function","name":"lookup","parameters":{"type":"object"}}],"tool_choice":"none"}`,
		},
		{
			name: "function with object tool choice",
			body: `{"model":"grok","tools":[{"type":"function","name":"lookup","parameters":{"type":"object"}}],"tool_choice":{"type":"function","name":"lookup"}}`,
		},
		{
			name: "code execution tool",
			body: `{"model":"grok","tools":[{"type":"code_execution"}]}`,
		},
		{
			name: "code interpreter tool",
			body: `{"model":"grok","tools":[{"type":"code_interpreter"}]}`,
		},
		{
			name: "collections search tool",
			body: `{"model":"grok","tools":[{"type":"collections_search"}]}`,
		},
		{
			name: "file search tool",
			body: `{"model":"grok","tools":[{"type":"file_search"}]}`,
		},
		{
			name: "mcp tool",
			body: `{"model":"grok","tools":[{"type":"mcp","server_label":"fixture"}]}`,
		},
		{
			name: "shell tool",
			body: `{"model":"grok","tools":[{"type":"shell"}]}`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			beforeTool := gjson.Get(tt.body, "tools.0")
			beforeChoice := gjson.Get(tt.body, "tool_choice")
			body, err := applyGrokResponsesCacheIdentity([]byte(tt.body), []byte(tt.body), "isolated-id", true)

			require.NoError(t, err)
			require.Equal(t, "isolated-id", gjson.GetBytes(body, "prompt_cache_key").String())
			tools := gjson.GetBytes(body, "tools").Array()
			require.Len(t, tools, 3)
			require.JSONEq(t, beforeTool.Raw, tools[0].Raw)
			require.Equal(t, 1, countGrokCacheTestTools(tools, "web_search"))
			require.Equal(t, 1, countGrokCacheTestTools(tools, "x_search"))
			require.Equal(t, beforeChoice.Exists(), gjson.GetBytes(body, "tool_choice").Exists())
			require.Equal(t, beforeChoice.Raw, gjson.GetBytes(body, "tool_choice").Raw)
		})
	}
}

func TestApplyGrokCacheIdentityDoesNotDuplicateNativeTools(t *testing.T) {
	tests := []struct {
		name      string
		body      string
		toolCount int
	}{
		{
			name:      "web search already present",
			body:      `{"model":"grok","tools":[{"type":"function","name":"lookup"},{"type":"web_search"}],"tool_choice":"auto"}`,
			toolCount: 3,
		},
		{
			name:      "x search already present",
			body:      `{"model":"grok","tools":[{"type":"function","name":"lookup"},{"type":"x_search"}],"tool_choice":"required"}`,
			toolCount: 3,
		},
		{
			name:      "both native tools already present",
			body:      `{"model":"grok","tools":[{"type":"function","name":"lookup"},{"type":"web_search"},{"type":"x_search"}],"tool_choice":{"type":"function","name":"lookup"}}`,
			toolCount: 3,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			beforeChoice := gjson.Get(tt.body, "tool_choice")
			body, err := applyGrokResponsesCacheIdentity([]byte(tt.body), []byte(tt.body), "isolated-id", true)

			require.NoError(t, err)
			tools := gjson.GetBytes(body, "tools").Array()
			require.Len(t, tools, tt.toolCount)
			require.Equal(t, "function", tools[0].Get("type").String())
			require.Equal(t, 1, countGrokCacheTestTools(tools, "web_search"))
			require.Equal(t, 1, countGrokCacheTestTools(tools, "x_search"))
			require.Equal(t, beforeChoice.Raw, gjson.GetBytes(body, "tool_choice").Raw)
		})
	}
}

func TestApplyGrokCacheIdentityNativeToolMergeIsIdempotent(t *testing.T) {
	source := []byte(`{"model":"grok","input":"hello","tools":[{"type":"function","name":"lookup","parameters":{"type":"object"}}],"tool_choice":"auto"}`)

	first, err := applyGrokResponsesCacheIdentity(source, source, "isolated-id", true)
	require.NoError(t, err)
	second, err := applyGrokResponsesCacheIdentity(first, first, "isolated-id", true)
	require.NoError(t, err)

	require.Equal(t, string(first), string(second))
	require.JSONEq(t, string(first), string(second))
	firstTools := gjson.GetBytes(first, "tools").Array()
	secondTools := gjson.GetBytes(second, "tools").Array()
	require.Len(t, firstTools, 3)
	require.Len(t, secondTools, 3)
	for i := range firstTools {
		require.Equal(t, firstTools[i].Raw, secondTools[i].Raw)
	}
	require.Equal(t, "function", secondTools[0].Get("type").String())
	require.Equal(t, "web_search", secondTools[1].Get("type").String())
	require.Equal(t, "x_search", secondTools[2].Get("type").String())
	require.Equal(t, gjson.GetBytes(first, "tool_choice").Raw, gjson.GetBytes(second, "tool_choice").Raw)
}

func TestApplyGrokCacheIdentityPreservesClientNativeToolDuplicates(t *testing.T) {
	source := []byte(`{"model":"grok","tools":[{"type":"function","name":"lookup"},{"type":"web_search","allowed_domains":["docs.example"]},{"type":"web_search","allowed_domains":["status.example"]}],"tool_choice":"auto"}`)
	originalTools := gjson.GetBytes(source, "tools").Array()

	body, err := applyGrokResponsesCacheIdentity(source, source, "isolated-id", true)

	require.NoError(t, err)
	tools := gjson.GetBytes(body, "tools").Array()
	require.Len(t, tools, 4)
	for i := range originalTools {
		require.Equal(t, originalTools[i].Raw, tools[i].Raw)
	}
	require.Equal(t, 2, countGrokCacheTestTools(tools, "web_search"))
	require.Equal(t, 1, countGrokCacheTestTools(tools, "x_search"))
	require.Equal(t, "x_search", tools[3].Get("type").String())
	require.Equal(t, gjson.GetBytes(source, "tool_choice").Raw, gjson.GetBytes(body, "tool_choice").Raw)
}

func TestApplyGrokCacheIdentityTreatsEmptyToolFieldsAsToolFree(t *testing.T) {
	tests := []struct {
		name string
		body string
	}{
		{name: "empty tools without choice", body: `{"model":"grok","tools":[]}`},
		{name: "null tools without choice", body: `{"model":"grok","tools":null}`},
		{name: "empty tools with null choice", body: `{"model":"grok","tools":[],"tool_choice":null}`},
		{name: "null tools with null choice", body: `{"model":"grok","tools":null,"tool_choice":null}`},
		{name: "empty tools with none choice", body: `{"model":"grok","tools":[],"tool_choice":"none"}`},
		{name: "null tools with none choice", body: `{"model":"grok","tools":null,"tool_choice":"none"}`},
		{name: "missing tools with null choice", body: `{"model":"grok","tool_choice":null}`},
		{name: "missing tools with none choice", body: `{"model":"grok","tool_choice":"none"}`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			body, err := applyGrokResponsesCacheIdentity([]byte(tt.body), []byte(tt.body), "isolated-id", true)

			require.NoError(t, err)
			require.Equal(t, "isolated-id", gjson.GetBytes(body, "prompt_cache_key").String())
			tools := gjson.GetBytes(body, "tools").Array()
			require.Len(t, tools, 2)
			require.Equal(t, "web_search", tools[0].Get("type").String())
			require.Equal(t, "x_search", tools[1].Get("type").String())
			require.Equal(t, grokFreeCacheDisabledToolChoice, gjson.GetBytes(body, "tool_choice").String())
		})
	}
}

func TestApplyGrokCacheIdentityPreservesUnsupportedToolIntent(t *testing.T) {
	tests := []struct {
		name string
		body string
	}{
		{
			name: "unsupported tool",
			body: `{"model":"grok","tools":[{"type":"namespace","name":"client_tools"}]}`,
		},
		{
			name: "tool choice without tools",
			body: `{"model":"grok","tool_choice":{"type":"function","name":"lookup"}}`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			beforeTools := gjson.Get(tt.body, "tools")
			beforeChoice := gjson.Get(tt.body, "tool_choice")
			body, err := applyGrokResponsesCacheIdentity([]byte(tt.body), []byte(tt.body), "isolated-id", true)

			require.NoError(t, err)
			require.Equal(t, "isolated-id", gjson.GetBytes(body, "prompt_cache_key").String())
			require.Equal(t, beforeTools.Exists(), gjson.GetBytes(body, "tools").Exists())
			require.Equal(t, beforeTools.Raw, gjson.GetBytes(body, "tools").Raw)
			require.Equal(t, beforeChoice.Exists(), gjson.GetBytes(body, "tool_choice").Exists())
			require.Equal(t, beforeChoice.Raw, gjson.GetBytes(body, "tool_choice").Raw)
		})
	}
}

func countGrokCacheTestTools(tools []gjson.Result, toolType string) int {
	count := 0
	for _, tool := range tools {
		if tool.Get("type").String() == toolType {
			count++
		}
	}
	return count
}

func TestApplyGrokCacheIdentityUsesPreSanitizationToolIntent(t *testing.T) {
	tests := []struct {
		name       string
		intentBody string
	}{
		{
			name:       "unsupported tools removed by sanitizer",
			intentBody: `{"model":"grok","tools":[{"type":"namespace","name":"client_tools"}]}`,
		},
		{
			name:       "tool choice removed with unsupported tool",
			intentBody: `{"model":"grok","tools":[{"type":"namespace","name":"client_tools"}],"tool_choice":{"type":"namespace","name":"client_tools"}}`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// This is the shape apply receives after patchGrokResponsesBody has
			// removed unsupported tools and their associated tool_choice.
			patchedBody := []byte(`{"model":"grok-4.5","input":"hello"}`)
			body, err := applyGrokResponsesCacheIdentity(patchedBody, []byte(tt.intentBody), "isolated-id", true)

			require.NoError(t, err)
			require.Equal(t, "isolated-id", gjson.GetBytes(body, "prompt_cache_key").String())
			require.False(t, gjson.GetBytes(body, "tools").Exists())
			require.False(t, gjson.GetBytes(body, "tool_choice").Exists())
		})
	}
}

func TestApplyGrokCacheIdentityWithoutFreeTierRoutingOnlyWritesIdentity(t *testing.T) {
	sourceBody := []byte(`{"model":"grok-4.5","input":"hello"}`)
	body, err := applyGrokResponsesCacheIdentity(sourceBody, sourceBody, "isolated-id", false)

	require.NoError(t, err)
	require.Equal(t, "isolated-id", gjson.GetBytes(body, "prompt_cache_key").String())
	require.False(t, gjson.GetBytes(body, "tools").Exists())
	require.False(t, gjson.GetBytes(body, "tool_choice").Exists())
}

func TestGrokCompactRequestSkipsCacheIdentityAndNativeTools(t *testing.T) {
	gin.SetMode(gin.TestMode)
	c := newGrokCacheTestContext(701)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses/compact", nil)
	body := []byte(`{"model":"grok","input":"compact this","prompt_cache_key":"raw-client-key"}`)

	identity := resolveGrokCacheIdentity(c, body, "", "grok-4.5")
	patched, err := applyGrokResponsesCacheIdentity(body, body, identity, true)

	require.NoError(t, err)
	require.Empty(t, identity)
	require.False(t, gjson.GetBytes(patched, "prompt_cache_key").Exists())
	require.False(t, gjson.GetBytes(patched, "tools").Exists())
	require.False(t, gjson.GetBytes(patched, "tool_choice").Exists())
}

func TestResolveGrokCacheIdentityConcurrentDeterminism(t *testing.T) {
	gin.SetMode(gin.TestMode)
	const workers = 50
	body := []byte(`{"model":"grok","messages":[{"role":"system","content":"stable"},{"role":"user","content":"hello"}]}`)
	identities := make(chan string, workers)
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			identities <- resolveGrokCacheIdentity(newGrokCacheTestContext(501), body, "", "grok-4.5")
		}()
	}
	wg.Wait()
	close(identities)

	var first string
	for identity := range identities {
		if first == "" {
			first = identity
		}
		require.Equal(t, first, identity)
	}
	require.NotEmpty(t, first)
}
