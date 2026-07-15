package service

import (
	"context"
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/config"
)

func (s *OpenAIGatewayService) openAILatencyMode(ctx context.Context) string {
	if mode, ok := OpenAILatencyModeFromContext(ctx); ok {
		return mode
	}
	if s != nil && s.settingService != nil {
		return s.settingService.GetOpenAILatencyMode()
	}
	if s == nil {
		return config.OpenAILatencyModeCompatible
	}
	return openAILatencyModeConfigDefault(s.cfg)
}

func (s *OpenAIGatewayService) openAILowLatencyMode(ctx context.Context) bool {
	return s.openAILatencyMode(ctx) == config.OpenAILatencyModeLowLatency
}

// useEmptyOpenAIMissingInstructions selects the CLIProxy-compatible low-latency
// behavior. Keeping this behind a policy preserves the existing Codex prompt
// semantics while allowing callers that already supply their own system prompt
// to avoid pre-filling several thousand injected tokens.
func (s *OpenAIGatewayService) useEmptyOpenAIMissingInstructions(ctx context.Context) bool {
	if s.openAILowLatencyMode(ctx) {
		return true
	}
	if s == nil || s.cfg == nil {
		return false
	}
	return strings.EqualFold(
		strings.TrimSpace(s.cfg.Gateway.OpenAIMissingInstructionsPolicy),
		config.OpenAIMissingInstructionsPolicyEmpty,
	)
}

func (s *OpenAIGatewayService) shouldFlushOpenAIStreamPreamble(ctx context.Context) bool {
	if s.openAILowLatencyMode(ctx) {
		return true
	}
	return s != nil && s.cfg != nil && s.cfg.Gateway.OpenAIStreamFlushPreamble
}

func (s *OpenAIGatewayService) openAIMissingInstructions(ctx context.Context, model string) string {
	if s.useEmptyOpenAIMissingInstructions(ctx) {
		return ""
	}
	return defaultCodexSynthInstructions(model)
}
