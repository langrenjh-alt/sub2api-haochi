package service

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

func moveConfig(t *testing.T, raw string) DegradationDetectionConfig {
	t.Helper()
	var cfg DegradationDetectionConfig
	require.NoError(t, json.Unmarshal([]byte(raw), &cfg))
	return NormalizeDegradationConfig(cfg)
}

func TestDegradationMoveConfigRoundTripAndValidation(t *testing.T) {
	cfg := moveConfig(t, `{"enabled":true,"move_on_degraded":true,"move_target_group_id":8}`)
	encoded, err := json.Marshal(cfg)
	require.NoError(t, err)
	require.Contains(t, string(encoded), `"move_on_degraded":true`)
	require.Contains(t, string(encoded), `"move_target_group_id":8`)
	require.NoError(t, ValidateDegradationConfig(cfg))
	for _, raw := range []string{`{"enabled":true,"move_on_degraded":true}`, `{"enabled":true,"move_on_degraded":true,"move_target_group_id":-1}`} {
		require.Error(t, ValidateDegradationConfig(moveConfig(t, raw)))
	}
	require.NoError(t, ValidateDegradationConfig(moveConfig(t, `{}`)))
	require.NoError(t, ValidateDegradationConfig(moveConfig(t, `{"enabled":false,"move_on_degraded":true}`)), "disabling detection must remain possible")
}

func TestDegradationMoveConfigRejectsSourceAsTarget(t *testing.T) {
	svc := NewDegradationService(&fakeDegradationRepo{})
	_, err := svc.UpdateGroupConfig(context.Background(), 1, 42, moveConfig(t, `{"enabled":true,"move_on_degraded":true,"move_target_group_id":42}`))
	require.Error(t, err)
}

func TestDegradationMoveIgnoresTransportErrorsAndNonVerdicts(t *testing.T) {
	repo := &fakeDegradationRepo{cfg: moveConfig(t, `{"enabled":true,"move_on_degraded":true,"move_target_group_id":8}`)}
	svc := NewDegradationService(repo)
	record := probeRecord(5, "incorrect", nil)
	record.ErrorMessage = "upstream timed out"
	svc.HandleIntelligentTestOutcome(context.Background(), record)
	svc.HandleIntelligentTestOutcome(context.Background(), probeRecord(5, "undetermined", nil))
	require.Empty(t, repo.applied)
	record.ErrorMessage = ""
	svc.HandleIntelligentTestOutcome(context.Background(), record)
	require.Len(t, repo.applied, 1)
	require.True(t, repo.applied[0].degraded)
}
