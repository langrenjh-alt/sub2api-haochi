package repository

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestGrokObservationSnapshotsAreSchedulerNeutral(t *testing.T) {
	t.Parallel()

	for _, key := range []string{"grok_billing_snapshot", "grok_usage_snapshot"} {
		require.True(t, isSchedulerNeutralExtraKey(key))
		require.False(t, shouldEnqueueSchedulerOutboxForExtraUpdates(map[string]any{
			key: map[string]any{"status_code": 200},
		}))
	}

	// The neutral exemption is exact: similarly named provider settings must
	// continue through the durable scheduler-change path.
	require.False(t, isSchedulerNeutralExtraKey("grok_usage_snapshot_override"))
	require.True(t, shouldEnqueueSchedulerOutboxForExtraUpdates(map[string]any{
		"grok_usage_snapshot": map[string]any{"status_code": 200},
		"grok_media_eligible": false,
	}))
}
