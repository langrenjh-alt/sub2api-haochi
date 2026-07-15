package service

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/config"
)

type openAILatencyModeSnapshot struct {
	mode string
}

func normalizeOpenAILatencyMode(value string) (string, bool) {
	mode := strings.ToLower(strings.TrimSpace(value))
	switch mode {
	case config.OpenAILatencyModeCompatible, config.OpenAILatencyModeLowLatency:
		return mode, true
	default:
		return "", false
	}
}

func normalizeOpenAILatencyModeOrDefault(value, fallback string) string {
	if mode, ok := normalizeOpenAILatencyMode(value); ok {
		return mode
	}
	if mode, ok := normalizeOpenAILatencyMode(fallback); ok {
		return mode
	}
	return config.OpenAILatencyModeCompatible
}

func openAILatencyModeConfigDefault(cfg *config.Config) string {
	if cfg == nil {
		return config.OpenAILatencyModeCompatible
	}
	return normalizeOpenAILatencyModeOrDefault(
		cfg.Gateway.OpenAILatencyMode,
		config.OpenAILatencyModeCompatible,
	)
}

func (s *SettingService) storeOpenAILatencyMode(mode string) {
	if s == nil {
		return
	}
	s.openAILatencyModeCache.Store(&openAILatencyModeSnapshot{
		mode: normalizeOpenAILatencyModeOrDefault(mode, openAILatencyModeConfigDefault(s.cfg)),
	})
}

// GetOpenAILatencyMode is a lock-free hot-path read and never accesses the DB.
func (s *SettingService) GetOpenAILatencyMode() string {
	if s == nil {
		return config.OpenAILatencyModeCompatible
	}
	if snapshot, ok := s.openAILatencyModeCache.Load().(*openAILatencyModeSnapshot); ok && snapshot != nil {
		return snapshot.mode
	}
	return openAILatencyModeConfigDefault(s.cfg)
}

// LoadOpenAILatencyModeSetting synchronously warms the runtime snapshot during startup.
// A missing row keeps the config-file default; malformed persisted values are ignored.
func (s *SettingService) LoadOpenAILatencyModeSetting(ctx context.Context) error {
	if s == nil {
		return nil
	}
	fallback := openAILatencyModeConfigDefault(s.cfg)
	if s.settingRepo == nil {
		s.storeOpenAILatencyMode(fallback)
		return nil
	}
	raw, err := s.settingRepo.GetValue(ctx, SettingKeyOpenAILatencyMode)
	if err != nil {
		if errors.Is(err, ErrSettingNotFound) {
			s.storeOpenAILatencyMode(fallback)
			return nil
		}
		return fmt.Errorf("get OpenAI latency mode setting: %w", err)
	}
	mode, ok := normalizeOpenAILatencyMode(raw)
	if !ok {
		return fmt.Errorf("invalid persisted OpenAI latency mode %q", strings.TrimSpace(raw))
	}
	s.storeOpenAILatencyMode(mode)
	return nil
}
