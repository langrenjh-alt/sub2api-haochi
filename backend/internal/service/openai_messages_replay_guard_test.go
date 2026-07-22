package service

import (
	"encoding/json"
	"fmt"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/pkg/apicompat"
	"github.com/stretchr/testify/require"
)

func TestApplyAnthropicCompatFullReplayGuard_TrimsOldMessages(t *testing.T) {
	t.Parallel()

	req := &apicompat.AnthropicRequest{Messages: make([]apicompat.AnthropicMessage, 0, openAICompatAnthropicReplayMaxTailMessages+3)}
	for i := 0; i < openAICompatAnthropicReplayMaxTailMessages+3; i++ {
		req.Messages = append(req.Messages, apicompat.AnthropicMessage{
			Role:    "user",
			Content: json.RawMessage(fmt.Sprintf(`"message-%02d"`, i)),
		})
	}

	trimmed := applyAnthropicCompatFullReplayGuard(req)

	require.True(t, trimmed)
	require.Len(t, req.Messages, openAICompatAnthropicReplayMaxTailMessages)
	require.JSONEq(t, `"message-03"`, string(req.Messages[0].Content))
	require.JSONEq(t, `"message-14"`, string(req.Messages[len(req.Messages)-1].Content))
}

func TestApplyAnthropicCompatFullReplayGuard_KeepsToolBoundaryIntact(t *testing.T) {
	t.Parallel()

	req := &apicompat.AnthropicRequest{Messages: make([]apicompat.AnthropicMessage, 0, openAICompatAnthropicReplayMaxTailMessages+3)}
	for i := 0; i < openAICompatAnthropicReplayMaxTailMessages+3; i++ {
		role := "user"
		content := json.RawMessage(fmt.Sprintf(`"message-%02d"`, i))
		if i == 1 {
			role = "assistant"
			content = json.RawMessage(`[{"type":"tool_use","id":"toolu_keep","name":"Read","input":{"file_path":"main.go"}}]`)
		}
		if i == 3 {
			content = json.RawMessage(`[{"type":"tool_result","tool_use_id":"toolu_keep","content":"ok"}]`)
		}
		req.Messages = append(req.Messages, apicompat.AnthropicMessage{
			Role:    role,
			Content: content,
		})
	}

	trimmed := applyAnthropicCompatFullReplayGuard(req)

	require.True(t, trimmed)
	require.Len(t, req.Messages, openAICompatAnthropicReplayMaxTailMessages+2)
	require.Equal(t, "assistant", req.Messages[0].Role)
	require.Contains(t, string(req.Messages[0].Content), `"toolu_keep"`)
	require.Contains(t, string(req.Messages[2].Content), `"tool_result"`)
}

func TestExpandAnthropicCompatTrimBoundary_DeepDependencyChain(t *testing.T) {
	t.Parallel()

	const messageCount = 4096
	messages := buildDeepAnthropicToolDependencyMessages(0, messageCount)
	start := len(messages) - openAICompatAnthropicReplayMaxTailMessages

	result := expandAnthropicCompatTrimBoundary(messages, start)

	require.Zero(t, result)
}

func TestApplyAnthropicCompatFullReplayGuard_DeepDependencyStopsAtRoot(t *testing.T) {
	t.Parallel()

	const (
		unrelatedPrefix = 128
		chainLength     = 2048
	)
	messages := make([]apicompat.AnthropicMessage, unrelatedPrefix)
	for i := range messages {
		messages[i] = apicompat.AnthropicMessage{
			Role:    "user",
			Content: json.RawMessage(fmt.Sprintf(`"unrelated-%04d"`, i)),
		}
	}
	messages = append(messages, buildDeepAnthropicToolDependencyMessages(unrelatedPrefix, chainLength)...)
	req := &apicompat.AnthropicRequest{Messages: messages}

	trimmed := applyAnthropicCompatFullReplayGuard(req)

	require.True(t, trimmed)
	require.Len(t, req.Messages, chainLength)
	require.Contains(t, string(req.Messages[0].Content), fmt.Sprintf(`"tool-%d"`, unrelatedPrefix))
}

func buildDeepAnthropicToolDependencyMessages(firstID, count int) []apicompat.AnthropicMessage {
	messages := make([]apicompat.AnthropicMessage, 0, count)
	for offset := 0; offset < count; offset++ {
		id := firstID + offset
		content := fmt.Sprintf(`[{"type":"tool_use","id":"tool-%d","name":"Read","input":{}}]`, id)
		if offset > 0 {
			content = fmt.Sprintf(
				`[{"type":"tool_use","id":"tool-%d","name":"Read","input":{}},{"type":"tool_result","tool_use_id":"tool-%d","content":"ok"}]`,
				id,
				id-1,
			)
		}
		messages = append(messages, apicompat.AnthropicMessage{
			Role:    "assistant",
			Content: json.RawMessage(content),
		})
	}
	return messages
}
