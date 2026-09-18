package service

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

type responseMonitorRepo struct {
	fakeDegradationRepo
	calls int
}

func (r *responseMonitorRepo) ApplyResponseModelDegradation(_ context.Context, limit int) (int, error) {
	r.calls++
	return 0, nil
}

func TestDegradationResponseMonitorRunsWithFullProbeQueue(t *testing.T) {
	r := &responseMonitorRepo{}
	r.depth = degradationMaxQueued
	NewDegradationService(r).Tick(context.Background())
	require.Equal(t, 1, r.calls)
}

func TestDegradationCandyUpgradesOnlyPreviousArithmeticPreset(t *testing.T) {
	old := DegradationDetectionConfig{
		Prompt: "计算 960 ÷ 2 × 10 ÷ 5，只回答数字。", ExpectedAnswer: "960",
		Model: "gpt-5.6-sol", ReasoningEffort: "max",
		MoveOnDegraded: true, MoveTargetGroupID: 24,
	}
	cfg := NormalizeDegradationConfig(old)
	require.Equal(t, DegradationCandyPrompt, cfg.Prompt)
	require.Equal(t, "21", cfg.ExpectedAnswer)
	require.Equal(t, "gpt-6-astra", cfg.Model)
	require.Equal(t, "medium", cfg.ReasoningEffort)
	require.True(t, cfg.MoveOnDegraded)
	require.Equal(t, int64(24), cfg.MoveTargetGroupID)
	old.Prompt = DegradationCandyPrompt
	old.ExpectedAnswer = "21"
	require.Equal(t, "gpt-6-astra", NormalizeDegradationConfig(old).Model)
	require.Equal(t, "medium", NormalizeDegradationConfig(old).ReasoningEffort)
	old.ExpectedAnswer = "960"
	old.Prompt = ""
	require.Equal(t, "21", NormalizeDegradationConfig(old).ExpectedAnswer)
	require.Equal(t, DegradationCandyPrompt, NormalizeDegradationConfig(old).Prompt)
	old.Prompt = "custom arithmetic"
	require.Equal(t, "960", NormalizeDegradationConfig(old).ExpectedAnswer)
	require.Equal(t, "gpt-5.6-sol", NormalizeDegradationConfig(old).Model)
	require.Equal(t, "max", NormalizeDegradationConfig(old).ReasoningEffort)
	require.Equal(t, old.Prompt, NormalizeDegradationConfig(old).Prompt)
}
