//go:build unit

package service

import (
	"encoding/json"
	"math"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/xai"
	"github.com/stretchr/testify/require"
)

type grokQuotaSnapshotFallbackMap map[string]any

var (
	benchmarkGrokQuotaPausedSink   bool
	benchmarkGrokQuotaDecisionSink openAIQuotaAutoPauseDecision
)

func TestGrokQuotaSchedulingMapFastPathMatchesJSONRoundTrip(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, time.July, 26, 12, 0, 0, 0, time.UTC)
	fresh := now.Add(-time.Minute).Format(time.RFC3339)
	stale := now.Add(-3 * time.Hour).Format(time.RFC3339)
	futureReset := float64(now.Add(time.Hour).Unix())
	pastReset := float64(now.Add(-time.Hour).Unix())

	tests := []struct {
		name string
		raw  map[string]any
	}{
		{
			name: "requests exhausted with full persisted shape",
			raw: map[string]any{
				"requests":               map[string]any{"limit": float64(100), "remaining": float64(0), "reset_unix": futureReset, "reset_at": now.Add(time.Hour).Format(time.RFC3339)},
				"tokens":                 map[string]any{"limit": float64(1_000_000), "remaining": float64(900_000), "reset_unix": futureReset},
				"subscription_tier":      "free",
				"entitlement_status":     "active",
				"status_code":            float64(200),
				"headers":                map[string]any{"x-ratelimit-limit-requests": "100", "retry-after": nil},
				"headers_observed":       true,
				"observation_source":     "upstream_response",
				"last_probe_at":          nil,
				"last_headers_seen_at":   fresh,
				"updated_at":             fresh,
				"future_extension_field": map[string]any{"nested": []any{true, "value", float64(1)}},
			},
		},
		{
			name: "tokens exhausted",
			raw: map[string]any{
				"tokens":     map[string]any{"limit": float64(1000), "remaining": float64(-1), "reset_unix": futureReset},
				"updated_at": fresh,
			},
		},
		{
			name: "active retry after",
			raw: map[string]any{
				"retry_after_seconds": float64(90),
				"updated_at":          now.Add(-time.Minute).Format(time.RFC3339),
			},
		},
		{
			name: "expired retry after",
			raw: map[string]any{
				"retry_after_seconds": float64(30),
				"updated_at":          now.Add(-time.Minute).Format(time.RFC3339),
			},
		},
		{
			name: "retry after without observation time",
			raw: map[string]any{
				"retry_after_seconds": float64(1),
				"updated_at":          nil,
			},
		},
		{
			name: "retry after with invalid observation time",
			raw: map[string]any{
				"retry_after_seconds": float64(1),
				"updated_at":          "not-a-time",
			},
		},
		{
			name: "stale exhaustion ignored",
			raw: map[string]any{
				"requests":   map[string]any{"limit": float64(10), "remaining": float64(0), "reset_unix": futureReset},
				"updated_at": stale,
			},
		},
		{
			name: "expired reset ignored",
			raw: map[string]any{
				"requests":   map[string]any{"limit": float64(10), "remaining": float64(0), "reset_unix": pastReset},
				"updated_at": fresh,
			},
		},
		{
			name: "incomplete and null windows ignored",
			raw: map[string]any{
				"requests":   map[string]any{"limit": float64(10), "remaining": nil, "reset_unix": nil, "reset_at": nil},
				"tokens":     nil,
				"updated_at": fresh,
			},
		},
		{
			name: "available quota",
			raw: map[string]any{
				"requests":   map[string]any{"limit": float64(10), "remaining": float64(1), "reset_unix": futureReset},
				"tokens":     map[string]any{"limit": float64(10), "remaining": float64(10)},
				"updated_at": fresh,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			fastSnapshot, ok := grokQuotaSchedulingSnapshotFromMap(tt.raw)
			require.True(t, ok)
			fastPaused, fastDecision := shouldAutoPauseGrokSchedulingSnapshot(fastSnapshot, now)

			legacySnapshot, err := grokQuotaSnapshotFromExtra(map[string]any{
				grokQuotaSnapshotExtraKey: grokQuotaSnapshotFallbackMap(tt.raw),
			})
			require.NoError(t, err)
			require.NotNil(t, legacySnapshot)
			legacyPaused, legacyDecision := shouldAutoPauseGrokQuotaSnapshot(legacySnapshot, now)

			require.Equal(t, legacyPaused, fastPaused)
			require.Equal(t, legacyDecision, fastDecision)
		})
	}
}

func TestGrokQuotaSchedulingMapFastPathReadsEncodingJSONShape(t *testing.T) {
	t.Parallel()

	var extra map[string]any
	require.NoError(t, json.Unmarshal([]byte(`{
		"grok_usage_snapshot": {
			"requests": {"limit": 100, "remaining": 0, "reset_unix": 4102444800},
			"tokens": {"limit": 1000000, "remaining": 900000, "reset_unix": 4102444800},
			"retry_after_seconds": null,
			"headers": {"x-ratelimit-limit-requests": "100"},
			"headers_observed": true,
			"status_code": 200,
			"updated_at": "2099-01-01T00:00:00Z"
		}
	}`), &extra))

	raw, ok := extra[grokQuotaSnapshotExtraKey].(map[string]any)
	require.True(t, ok)
	requests, ok := raw["requests"].(map[string]any)
	require.True(t, ok)
	require.IsType(t, float64(0), requests["limit"])

	_, fastPathOK := grokQuotaSchedulingSnapshotFromMap(raw)
	require.True(t, fastPathOK)
	paused, decision := shouldAutoPauseGrokAccountByQuota(&Account{
		Platform: PlatformGrok,
		Type:     AccountTypeOAuth,
		Extra:    extra,
	})
	require.True(t, paused)
	require.Equal(t, "requests", decision.window)
}

func TestShouldAutoPauseGrokAccountByQuotaPreservesFallbacks(t *testing.T) {
	t.Parallel()

	fresh := time.Now().UTC().Format(time.RFC3339)
	futureReset := time.Now().Add(time.Hour).Unix()
	exhaustedWindow := map[string]any{
		"limit":      float64(10),
		"remaining":  float64(0),
		"reset_unix": float64(futureReset),
	}
	typedLimit := int64(10)
	typedRemaining := int64(0)

	tests := []struct {
		name         string
		raw          any
		wantFastPath bool
		wantPaused   bool
	}{
		{
			name: "canonical decoded map",
			raw: map[string]any{
				"requests":   exhaustedWindow,
				"updated_at": fresh,
			},
			wantFastPath: true,
			wantPaused:   true,
		},
		{
			name: "typed pointer snapshot",
			raw: &xai.QuotaSnapshot{
				Requests:  &xai.QuotaWindow{Limit: &typedLimit, Remaining: &typedRemaining, ResetUnix: &futureReset},
				UpdatedAt: fresh,
			},
			wantPaused: true,
		},
		{
			name: "typed value snapshot",
			raw: xai.QuotaSnapshot{
				Requests:  &xai.QuotaWindow{Limit: &typedLimit, Remaining: &typedRemaining, ResetUnix: &futureReset},
				UpdatedAt: fresh,
			},
			wantPaused: true,
		},
		{
			name: "named map uses generic fallback",
			raw: grokQuotaSnapshotFallbackMap{
				"requests":   exhaustedWindow,
				"updated_at": fresh,
			},
			wantPaused: true,
		},
		{
			name: "nested typed window uses generic fallback",
			raw: map[string]any{
				"requests":   xai.QuotaWindow{Limit: &typedLimit, Remaining: &typedRemaining, ResetUnix: &futureReset},
				"updated_at": fresh,
			},
			wantPaused: true,
		},
		{
			name: "integer map values use generic fallback",
			raw: map[string]any{
				"requests": map[string]any{
					"limit":      int64(10),
					"remaining":  int64(0),
					"reset_unix": futureReset,
				},
				"updated_at": fresh,
			},
			wantPaused: true,
		},
		{
			name:       "raw JSON uses generic fallback",
			raw:        json.RawMessage(`{"requests":{"limit":10,"remaining":0,"reset_unix":4102444800},"updated_at":"2099-01-01T00:00:00Z"}`),
			wantPaused: true,
		},
		{
			name: "case folded JSON fields use generic fallback",
			raw: map[string]any{
				"REQUESTS":   exhaustedWindow,
				"UPDATED_AT": fresh,
			},
			wantPaused: true,
		},
		{
			name: "malformed scheduling number retains decode failure",
			raw: map[string]any{
				"requests":   map[string]any{"limit": float64(10.5), "remaining": float64(0)},
				"updated_at": fresh,
			},
			wantPaused: false,
		},
		{
			name: "out of range scheduling number retains decode failure",
			raw: map[string]any{
				"requests":   map[string]any{"limit": float64(1 << 63), "remaining": float64(0)},
				"updated_at": fresh,
			},
			wantPaused: false,
		},
		{
			name: "large lossless JSON integer uses generic fallback",
			raw: map[string]any{
				"requests":   map[string]any{"limit": float64(9_007_199_254_740_994), "remaining": float64(0)},
				"updated_at": fresh,
			},
			wantPaused: true,
		},
		{
			name: "non finite number retains marshal failure",
			raw: map[string]any{
				"requests":   map[string]any{"limit": float64(10), "remaining": float64(0)},
				"updated_at": fresh,
				"extension":  math.Inf(1),
			},
			wantPaused: false,
		},
		{
			name: "malformed unused known field retains decode failure",
			raw: map[string]any{
				"requests":    exhaustedWindow,
				"updated_at":  fresh,
				"status_code": "200",
			},
			wantPaused: false,
		},
		{
			name: "unsupported unknown field retains marshal failure",
			raw: map[string]any{
				"requests":   exhaustedWindow,
				"updated_at": fresh,
				"extension":  make(chan int),
			},
			wantPaused: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			if rawMap, ok := tt.raw.(map[string]any); ok {
				_, fastPathOK := grokQuotaSchedulingSnapshotFromMap(rawMap)
				require.Equal(t, tt.wantFastPath, fastPathOK)
			}
			account := &Account{
				Platform: PlatformGrok,
				Type:     AccountTypeOAuth,
				Extra:    map[string]any{grokQuotaSnapshotExtraKey: tt.raw},
			}
			paused, _ := shouldAutoPauseGrokAccountByQuota(account)
			require.Equal(t, tt.wantPaused, paused)
		})
	}
}

func TestShouldAutoPauseGrokAccountByQuotaDecodedMapAllocations(t *testing.T) {
	futureReset := time.Now().Add(time.Hour).Unix()
	account := &Account{
		Platform: PlatformGrok,
		Type:     AccountTypeOAuth,
		Extra: map[string]any{
			grokQuotaSnapshotExtraKey: map[string]any{
				"requests": map[string]any{
					"limit":      float64(10),
					"remaining":  float64(0),
					"reset_unix": float64(futureReset),
				},
				"updated_at": time.Now().UTC().Format(time.RFC3339),
			},
		},
	}

	allocs := testing.AllocsPerRun(1000, func() {
		benchmarkGrokQuotaPausedSink, benchmarkGrokQuotaDecisionSink = shouldAutoPauseGrokAccountByQuota(account)
	})
	require.Zero(t, allocs)
}

func BenchmarkShouldAutoPauseGrokAccountByQuota(b *testing.B) {
	futureReset := float64(time.Date(2100, time.January, 1, 0, 0, 0, 0, time.UTC).Unix())
	decodedMap := map[string]any{
		"requests": map[string]any{
			"limit":      float64(100),
			"remaining":  float64(0),
			"reset_unix": futureReset,
			"reset_at":   "2100-01-01T00:00:00Z",
		},
		"tokens": map[string]any{
			"limit":      float64(1_000_000),
			"remaining":  float64(900_000),
			"reset_unix": futureReset,
		},
		"subscription_tier":    "free",
		"entitlement_status":   "active",
		"status_code":          float64(200),
		"headers":              map[string]any{"x-ratelimit-limit-requests": "100", "x-ratelimit-remaining-requests": "0"},
		"headers_observed":     true,
		"observation_source":   "upstream_response",
		"last_headers_seen_at": "2099-12-31T23:59:00Z",
		"updated_at":           "2099-12-31T23:59:00Z",
	}
	fastAccount := &Account{
		Platform: PlatformGrok,
		Type:     AccountTypeOAuth,
		Extra:    map[string]any{grokQuotaSnapshotExtraKey: decodedMap},
	}
	fallbackAccount := &Account{
		Platform: PlatformGrok,
		Type:     AccountTypeOAuth,
		Extra:    map[string]any{grokQuotaSnapshotExtraKey: grokQuotaSnapshotFallbackMap(decodedMap)},
	}
	limit := int64(100)
	remaining := int64(0)
	resetUnix := int64(futureReset)
	typedAccount := &Account{
		Platform: PlatformGrok,
		Type:     AccountTypeOAuth,
		Extra: map[string]any{
			grokQuotaSnapshotExtraKey: &xai.QuotaSnapshot{
				Requests:  &xai.QuotaWindow{Limit: &limit, Remaining: &remaining, ResetUnix: &resetUnix},
				UpdatedAt: "2099-12-31T23:59:00Z",
			},
		},
	}

	bench := func(b *testing.B, account *Account) {
		b.Helper()
		b.ReportAllocs()
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			benchmarkGrokQuotaPausedSink, benchmarkGrokQuotaDecisionSink = shouldAutoPauseGrokAccountByQuota(account)
		}
	}
	b.Run("decoded_map_fast_path", func(b *testing.B) { bench(b, fastAccount) })
	b.Run("typed_snapshot", func(b *testing.B) { bench(b, typedAccount) })
	b.Run("legacy_json_round_trip", func(b *testing.B) { bench(b, fallbackAccount) })
}
