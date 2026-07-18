package service

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestAppendMissingGrokFreeCacheNativeTools_PureClientFunctionNoInject(t *testing.T) {
	body := []byte(`{
		"model": "grok-4.5",
		"tools": [
			{"type":"function","name":"view_image","description":"View image","parameters":{"type":"object","properties":{"path":{"type":"string"}},"required":["path"]}}
		],
		"tool_choice": "auto"
	}`)

	result, err := appendMissingGrokFreeCacheNativeTools(body)
	require.NoError(t, err)

	tools := gjson.GetBytes(result, "tools").Array()
	for _, tool := range tools {
		toolType := tool.Get("type").String()
		assert.NotEqual(t, "web_search", toolType, "should not inject web_search for pure client functions")
		assert.NotEqual(t, "x_search", toolType, "should not inject x_search for pure client functions")
	}
}

func TestAppendMissingGrokFreeCacheNativeTools_FunctionWebSearchIsPreservedAndComplemented(t *testing.T) {
	body := []byte(`{
		"model": "grok-4.5",
		"tools": [
			{"type":"function","name":"view_image","description":"View","parameters":{"type":"object","properties":{"path":{"type":"string"}},"required":["path"]}},
			{"type":"function","name":"web_search","description":"Search","parameters":{"type":"object","properties":{"query":{"type":"string"}},"required":["query"]}}
		]
	}`)

	result, err := appendMissingGrokFreeCacheNativeTools(body)
	require.NoError(t, err)

	tools := gjson.GetBytes(result, "tools").Array()
	require.Len(t, tools, 3)
	assert.Equal(t, "function", tools[1].Get("type").String())
	assert.Equal(t, "web_search", tools[1].Get("name").String())
	types := make(map[string]bool)
	for _, tool := range tools {
		types[tool.Get("type").String()] = true
	}
	assert.False(t, types["web_search"], "client web_search must not be rewritten as a native tool")
	assert.True(t, types["x_search"], "x_search should complement the client web_search function")
}

func TestAppendMissingGrokFreeCacheNativeTools_NativeSearchAlreadyPresent(t *testing.T) {
	body := []byte(`{
		"model": "grok-4.5",
		"tools": [
			{"type":"function","name":"view_image","description":"View","parameters":{"type":"object","properties":{"path":{"type":"string"}},"required":["path"]}},
			{"type":"web_search"}
		]
	}`)

	result, err := appendMissingGrokFreeCacheNativeTools(body)
	require.NoError(t, err)

	tools := gjson.GetBytes(result, "tools").Array()
	types := make(map[string]bool)
	for _, tool := range tools {
		types[tool.Get("type").String()] = true
	}
	assert.True(t, types["web_search"])
	assert.True(t, types["x_search"], "x_search should be injected when web_search is already present")
}

func TestAppendMissingGrokFreeCacheNativeTools_MultipleFunctionsNoSearch(t *testing.T) {
	body := []byte(`{
		"model": "grok-4.5",
		"tools": [
			{"type":"function","name":"view_image","description":"View","parameters":{"type":"object","properties":{"path":{"type":"string"}},"required":["path"]}},
			{"type":"function","name":"read_file","description":"Read","parameters":{"type":"object","properties":{"path":{"type":"string"}},"required":["path"]}}
		]
	}`)

	result, err := appendMissingGrokFreeCacheNativeTools(body)
	require.NoError(t, err)

	tools := gjson.GetBytes(result, "tools").Array()
	require.Len(t, tools, 2, "no tools should be injected for pure client functions")
}
