package service

import (
	"encoding/json"
	"strings"
)

// Output tokens are upstream-reported, never inferred from characters or elapsed time.
func intelligentOutputTokens(raw string) *int64 {
	var result *int64
	read := func(s string) {
		var v map[string]any
		if json.Unmarshal([]byte(s), &v) != nil {
			return
		}
		if response, ok := v["response"].(map[string]any); ok {
			v = response
		}
		usage, ok := v["usage"].(map[string]any)
		if !ok {
			return
		}
		for _, key := range []string{"output_tokens", "completion_tokens"} {
			if n, ok := usage[key].(float64); ok && n >= 0 {
				value := int64(n)
				result = &value
				return
			}
		}
	}
	read(raw)
	for _, line := range strings.Split(raw, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "data:") {
			read(strings.TrimSpace(strings.TrimPrefix(line, "data:")))
		}
	}
	return result
}
