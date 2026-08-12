package service

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/ctxkey"
	"github.com/stretchr/testify/require"
)

func TestBurstModeAcquireLimitInclusiveThreshold(t *testing.T) {
	require.Equal(t, 91, burstModeAcquireLimit(100, 90))
	require.Equal(t, 91, burstModeAcquireLimit(101, 90))
	require.Equal(t, 1, burstModeAcquireLimit(1, 1))
	require.Equal(t, 101, burstModeAcquireLimit(100, 100))
	require.Equal(t, 0, burstModeAcquireLimit(0, 90))
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
				currentCounts: map[int64]int{30: 73},
			},
		)
		selection, decision := selectAccount(t, ctx, svc, "sticky")
		require.Equal(t, int64(30), selection.Account.ID)
		require.Equal(t, openAIAccountScheduleLayerSessionSticky, decision.Layer)
		require.Equal(t, []int64{30}, acquiredIDs)
		require.Equal(t, []int{74}, acquireLimits)
	})

	t.Run("custom threshold allows equality and rejects values above it", func(t *testing.T) {
		var acquiredIDs []int64
		var acquireLimits []int
		svc := newService(
			&schedulerTestGatewayCache{},
			schedulerTestConcurrencyCache{
				acquiredIDs:   &acquiredIDs,
				acquireLimits: &acquireLimits,
				currentCounts: map[int64]int{10: 74, 20: 73},
			},
		)
		selection, decision := selectAccount(t, ctx, svc, "")
		require.Equal(t, int64(20), selection.Account.ID)
		require.Equal(t, openAIAccountScheduleLayerBurstMode, decision.Layer)
		require.Equal(t, []int64{10, 20}, acquiredIDs)
		require.Equal(t, []int{74, 74}, acquireLimits)
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

	t.Run("disabled mode keeps the default priority scheduler", func(t *testing.T) {
		group.BurstModeEnabled = false
		t.Cleanup(func() { group.BurstModeEnabled = true })
		svc := newService(&schedulerTestGatewayCache{}, schedulerTestConcurrencyCache{})
		selection, decision := selectAccount(t, ctx, svc, "")
		require.Equal(t, int64(20), selection.Account.ID)
		require.Equal(t, openAIAccountScheduleLayerLoadBalance, decision.Layer)
	})
}
