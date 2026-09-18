package service

import (
	"context"
	"github.com/stretchr/testify/require"
	"testing"
)

func TestDegradationSampleTokens(t *testing.T) {
	for _, raw := range []string{`{"usage":{"completion_tokens":166}}`, `data: {"type":"response.completed","response":{"usage":{"output_tokens":166}}}`} {
		v := intelligentOutputTokens(raw)
		require.NotNil(t, v)
		require.EqualValues(t, 166, *v)
	}
	require.Nil(t, intelligentOutputTokens(`data: {"text":"no usage"}`))
}
func TestDegradationSampleDoesNotMoveOrSuspend(t *testing.T) {
	repo := &fakeDegradationRepo{cfg: NormalizeDegradationConfig(DegradationDetectionConfig{Enabled: true})}
	r := probeRecord(5, "incorrect", nil)
	r.ConfigSnapshot.PublicSample = true
	NewDegradationService(repo).HandleIntelligentTestOutcome(context.Background(), r)
	require.Empty(t, repo.applied)
}
