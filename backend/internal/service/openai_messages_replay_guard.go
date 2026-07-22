package service

import (
	"encoding/json"

	"github.com/Wei-Shaw/sub2api/internal/pkg/apicompat"
)

const openAICompatAnthropicReplayMaxTailMessages = 12

func applyAnthropicCompatFullReplayGuard(req *apicompat.AnthropicRequest) bool {
	if req == nil || len(req.Messages) <= openAICompatAnthropicReplayMaxTailMessages {
		return false
	}

	start := len(req.Messages) - openAICompatAnthropicReplayMaxTailMessages
	start = expandAnthropicCompatTrimBoundary(req.Messages, start)
	if start <= 0 {
		return false
	}

	req.Messages = append([]apicompat.AnthropicMessage(nil), req.Messages[start:]...)
	return true
}

func expandAnthropicCompatTrimBoundary(messages []apicompat.AnthropicMessage, start int) int {
	if start <= 0 || start >= len(messages) {
		return start
	}

	refs := make([]anthropicCompatMessageToolRefs, len(messages))
	toolUseIndex := make(map[string]int)
	toolResultIndex := make(map[string]int)
	for i, msg := range messages {
		uses, results := anthropicCompatMessageToolIDs(msg)
		refs[i] = anthropicCompatMessageToolRefs{uses: uses, results: results}
		for _, id := range uses {
			if _, exists := toolUseIndex[id]; !exists {
				toolUseIndex[id] = i
			}
		}
		for _, id := range results {
			if _, exists := toolResultIndex[id]; !exists {
				toolResultIndex[id] = i
			}
		}
	}

	// Walk backwards so that lowering start naturally extends the same scan into
	// the newly included prefix. Each message is visited at most once.
	for i := len(messages) - 1; i >= start; i-- {
		for _, id := range refs[i].results {
			if useIdx, ok := toolUseIndex[id]; ok && useIdx < start {
				start = useIdx
			}
		}
		for _, id := range refs[i].uses {
			if resultIdx, ok := toolResultIndex[id]; ok && resultIdx < start {
				start = resultIdx
			}
		}
	}
	return start
}

type anthropicCompatMessageToolRefs struct {
	uses    []string
	results []string
}

func anthropicCompatMessageToolIDs(msg apicompat.AnthropicMessage) ([]string, []string) {
	var blocks []apicompat.AnthropicContentBlock
	if err := json.Unmarshal(msg.Content, &blocks); err != nil {
		return nil, nil
	}

	var uses []string
	var results []string
	for _, block := range blocks {
		switch block.Type {
		case "tool_use":
			if block.ID != "" {
				uses = append(uses, block.ID)
			}
		case "tool_result":
			if block.ToolUseID != "" {
				results = append(results, block.ToolUseID)
			}
		}
	}
	return uses, results
}
