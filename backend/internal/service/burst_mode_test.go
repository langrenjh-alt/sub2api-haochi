package service

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/ctxkey"
	"github.com/stretchr/testify/require"
)

func TestBurstModeAcquireLimitReservesCapacityForStickyRequests(t *testing.T) {
	require.Equal(t, 90, burstModeAcquireLimit(100, 90, false))
	require.Equal(t, 90, burstModeAcquireLimit(101, 90, false))
	require.Equal(t, 1, burstModeAcquireLimit(1, 1, false))
	require.Equal(t, 100, burstModeAcquireLimit(100, 100, false))
	require.Equal(t, 100, burstModeAcquireLimit(100, 90, true))
	require.Equal(t, 0, burstModeAcquireLimit(0, 90, true))
}

func TestBurstModeHandles429OnlyForEnabledHydratedGroup(t *testing.T) {
	group := &Group{
		ID:                        7,
		Platform:                  PlatformOpenAI,
		Status:                    StatusActive,
		Hydrated:                  true,
		BurstModeEnabled:          true,
		BurstModeThresholdPercent: 75,
	}
	ctx := context.WithValue(context.Background(), ctxkey.Group, group)
	require.True(t, BurstModeHandles429(ctx, http.StatusTooManyRequests))
	require.False(t, BurstModeHandles429(ctx, http.StatusBadGateway))
	group.BurstModeEnabled = false
	require.False(t, BurstModeHandles429(ctx, http.StatusTooManyRequests))
}

func TestValidateBurstModeThresholdPercent(t *testing.T) {
	require.NoError(t, ValidateBurstModeThresholdPercent(1))
	require.NoError(t, ValidateBurstModeThresholdPercent(100))
	require.Error(t, ValidateBurstModeThresholdPercent(0))
	require.Error(t, ValidateBurstModeThresholdPercent(101))
}

func TestBurstMode429RetryLimitUsesGroupSettingAndDefaultsToTen(t *testing.T) {
	group := &Group{ID: 7, Platform: PlatformOpenAI, Status: StatusActive, Hydrated: true, BurstModeEnabled: true}
	ctx := context.WithValue(context.Background(), ctxkey.Group, group)
	require.Equal(t, DefaultBurstMode429RetryCount, BurstMode429RetryLimit(ctx))
	group.BurstMode429RetryCount = 17
	require.Equal(t, 17, BurstMode429RetryLimit(ctx))
	group.BurstMode429RetryCount = 101
	require.Equal(t, 100, BurstMode429RetryLimit(ctx))
}

func TestBurstModeHighUsageAccountUsesOnlyOpenAIUsageSignals(t *testing.T) {
	openAI := &Account{Platform: PlatformOpenAI, Extra: map[string]any{
		"codex_5h_used_percent": 96.0,
		"codex_5h_reset_at":     time.Now().Add(time.Hour).Format(time.RFC3339),
	}}
	require.Equal(t, 96.0, BurstModeAccountUsagePercent(openAI))
	require.True(t, BurstModeHighUsageAccount(openAI))

	anthropic := &Account{Platform: PlatformAnthropic, Extra: map[string]any{
		"session_window_utilization": 0.95,
	}, SessionWindowEnd: func() *time.Time { value := time.Now().Add(time.Hour); return &value }()}
	require.Zero(t, BurstModeAccountUsagePercent(anthropic))
	require.False(t, BurstModeHighUsageAccount(anthropic))

	group := &Group{Platform: PlatformAnthropic, BurstModeHighUsageEnabled: true}
	require.False(t, burstModeHighUsageEnabledForGroup(group))
	group.Platform = PlatformOpenAI
	require.True(t, burstModeHighUsageEnabledForGroup(group))
}

func TestAntigravityBurstMode429BypassesInternalRetry(t *testing.T) {
	ctx := context.WithValue(context.Background(), ctxkey.Group, &Group{
		ID:                        7,
		Platform:                  PlatformAntigravity,
		Status:                    StatusActive,
		Hydrated:                  true,
		BurstModeEnabled:          true,
		BurstModeThresholdPercent: 90,
	})
	resp := &http.Response{
		StatusCode: http.StatusTooManyRequests,
		Header:     http.Header{"Retry-After": []string{"60"}},
	}
	body := []byte(`{"error":{"code":429,"message":"quota exhausted"}}`)

	result := (&AntigravityGatewayService{}).handleSmartRetry(
		antigravityRetryLoopParams{ctx: ctx}, resp, body, "https://example.invalid", 0, nil,
	)

	require.Equal(t, smartRetryActionBreakWithResp, result.action)
	require.Nil(t, result.switchError)
	require.NotNil(t, result.resp)
	require.Equal(t, http.StatusTooManyRequests, result.resp.StatusCode)
	returnedBody, err := io.ReadAll(result.resp.Body)
	require.NoError(t, err)
	require.JSONEq(t, string(body), strings.TrimSpace(string(returnedBody)))
}

func TestOpenAIBurstModeScheduling(t *testing.T) {
	groupID := int64(81)
	accounts := []Account{
		{ID: 30, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Status: StatusActive, Schedulable: true, Concurrency: 100, Priority: 5},
		{ID: 10, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Status: StatusActive, Schedulable: true, Concurrency: 100, Priority: 10},
		{ID: 20, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Status: StatusActive, Schedulable: true, Concurrency: 100, Priority: 0},
	}
	group := &Group{
		ID:                        groupID,
		Platform:                  PlatformOpenAI,
		Status:                    StatusActive,
		Hydrated:                  true,
		BurstModeEnabled:          true,
		BurstModeThresholdPercent: 73,
	}
	ctx := context.WithValue(context.Background(), ctxkey.Group, group)

	newService := func(cache *schedulerTestGatewayCache, concurrencyCache schedulerTestConcurrencyCache) *OpenAIGatewayService {
		cfg := &config.Config{}
		cfg.Gateway.Scheduling.LoadBatchEnabled = false
		return &OpenAIGatewayService{
			accountRepo:        schedulerTestOpenAIAccountRepo{accounts: accounts},
			cache:              cache,
			cfg:                cfg,
			concurrencyService: NewConcurrencyService(concurrencyCache),
		}
	}
	selectAccount := func(t *testing.T, selectCtx context.Context, svc *OpenAIGatewayService, sessionHash string) (*AccountSelectionResult, OpenAIAccountScheduleDecision) {
		t.Helper()
		selection, decision, err := svc.SelectAccountWithSchedulerForCapability(
			selectCtx, &groupID, "", sessionHash, "", nil,
			OpenAIUpstreamTransportHTTPSSE, OpenAIEndpointCapabilityChatCompletions,
			false, false, true, PlatformOpenAI,
		)
		require.NoError(t, err)
		require.NotNil(t, selection)
		require.NotNil(t, selection.Account)
		t.Cleanup(func() {
			if selection.ReleaseFunc != nil {
				selection.ReleaseFunc()
			}
		})
		return selection, decision
	}

	t.Run("sticky account remains first", func(t *testing.T) {
		var acquiredIDs []int64
		var acquireLimits []int
		svc := newService(
			&schedulerTestGatewayCache{sessionBindings: map[string]int64{"openai:sticky": 30}},
			schedulerTestConcurrencyCache{
				acquiredIDs:   &acquiredIDs,
				acquireLimits: &acquireLimits,
				currentCounts: map[int64]int{30: 99},
			},
		)
		selection, decision := selectAccount(t, ctx, svc, "sticky")
		require.Equal(t, int64(30), selection.Account.ID)
		require.Equal(t, openAIAccountScheduleLayerSessionSticky, decision.Layer)
		require.Equal(t, []int64{30}, acquiredIDs)
		require.Equal(t, []int{100}, acquireLimits)
	})

	t.Run("new requests fill one account only to the custom threshold", func(t *testing.T) {
		var acquiredIDs []int64
		var acquireLimits []int
		svc := newService(
			&schedulerTestGatewayCache{},
			schedulerTestConcurrencyCache{
				acquiredIDs:   &acquiredIDs,
				acquireLimits: &acquireLimits,
				currentCounts: map[int64]int{10: 73, 20: 72},
			},
		)
		selection, decision := selectAccount(t, ctx, svc, "")
		require.Equal(t, int64(20), selection.Account.ID)
		require.Equal(t, openAIAccountScheduleLayerBurstMode, decision.Layer)
		require.Equal(t, []int64{10, 20}, acquiredIDs)
		require.Equal(t, []int{73, 73}, acquireLimits)
	})

	t.Run("same-account retry cannot consume sticky reserve", func(t *testing.T) {
		var acquiredIDs []int64
		var acquireLimits []int
		svc := newService(
			&schedulerTestGatewayCache{},
			schedulerTestConcurrencyCache{
				acquiredIDs:   &acquiredIDs,
				acquireLimits: &acquireLimits,
				currentCounts: map[int64]int{20: 73},
			},
		)
		retryCtx := WithBurstModeRetryAccount(ctx, 20)
		selection, decision := selectAccount(t, retryCtx, svc, "")
		require.Equal(t, int64(10), selection.Account.ID)
		require.Equal(t, openAIAccountScheduleLayerBurstMode, decision.Layer)
		require.Equal(t, []int64{20, 10}, acquiredIDs)
		require.Equal(t, []int{73, 73}, acquireLimits)
	})

	t.Run("same-account retry outranks existing sticky binding", func(t *testing.T) {
		var acquiredIDs []int64
		svc := newService(
			&schedulerTestGatewayCache{sessionBindings: map[string]int64{"openai:sticky": 30}},
			schedulerTestConcurrencyCache{acquiredIDs: &acquiredIDs},
		)
		retryCtx := WithBurstModeRetryAccount(ctx, 20)
		selection, decision := selectAccount(t, retryCtx, svc, "sticky")
		require.Equal(t, int64(20), selection.Account.ID)
		require.Equal(t, openAIAccountScheduleLayerBurstMode, decision.Layer)
		require.Equal(t, []int64{20}, acquiredIDs)
	})

	t.Run("high usage mode ignores sticky binding and prioritizes 95 percent accounts", func(t *testing.T) {
		group.BurstModeHighUsageEnabled = true
		t.Cleanup(func() { group.BurstModeHighUsageEnabled = false })
		accounts[1].Extra = map[string]any{
			"codex_5h_used_percent": 96.0,
			"codex_5h_reset_at":     time.Now().Add(time.Hour).Format(time.RFC3339),
		}
		var acquiredIDs []int64
		svc := newService(
			&schedulerTestGatewayCache{sessionBindings: map[string]int64{"openai:sticky": 30}},
			schedulerTestConcurrencyCache{acquiredIDs: &acquiredIDs},
		)
		selection, decision := selectAccount(t, ctx, svc, "sticky")
		require.Equal(t, int64(10), selection.Account.ID)
		require.Equal(t, openAIAccountScheduleLayerBurstMode, decision.Layer)
		require.Equal(t, []int64{10}, acquiredIDs)
		accounts[1].Extra = nil
	})

	t.Run("disabled mode keeps the default priority scheduler", func(t *testing.T) {
		group.BurstModeEnabled = false
		t.Cleanup(func() { group.BurstModeEnabled = true })
		svc := newService(&schedulerTestGatewayCache{}, schedulerTestConcurrencyCache{})
		selection, decision := selectAccount(t, ctx, svc, "")
		require.Equal(t, int64(20), selection.Account.ID)
		require.Equal(t, openAIAccountScheduleLayerLoadBalance, decision.Layer)
	})
}
