package handler

import (
	"context"
	"net/http"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/pkg/ctxkey"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func burstModeTestContext() context.Context {
	return context.WithValue(context.Background(), ctxkey.Group, &service.Group{
		ID:                        9,
		Platform:                  service.PlatformOpenAI,
		Status:                    service.StatusActive,
		Hydrated:                  true,
		BurstModeEnabled:          true,
		BurstModeThresholdPercent: 90,
	})
}

func TestBurstModeRetryPolicyUsesConfigured429Retries(t *testing.T) {
	ctx := burstModeTestContext()
	retryable, limit := burstModeRetryPolicy(burstModeTestContext(), &service.UpstreamFailoverError{
		StatusCode: http.StatusTooManyRequests,
	}, 0)
	require.True(t, retryable)
	require.Equal(t, service.BurstModeSameAccount429Retries, limit)
	group := ctx.Value(ctxkey.Group).(*service.Group)
	group.BurstMode429RetryCount = 17
	retryable, limit = burstModeRetryPolicy(ctx, &service.UpstreamFailoverError{StatusCode: http.StatusTooManyRequests}, 0)
	require.True(t, retryable)
	require.Equal(t, 17, limit)
}

func TestBurstModeRetryPolicyKeepsDefaultForOtherErrors(t *testing.T) {
	retryable, limit := burstModeRetryPolicy(burstModeTestContext(), &service.UpstreamFailoverError{
		StatusCode:             http.StatusBadGateway,
		RetryableOnSameAccount: true,
	}, 3)
	require.True(t, retryable)
	require.Equal(t, 3, limit)
}

func TestBurstModeFailoverUsesConfigured429RetriesBeforeSwitching(t *testing.T) {
	ctx := burstModeTestContext()
	group := ctx.Value(ctxkey.Group).(*service.Group)
	group.BurstMode429RetryCount = 2
	state := NewFailoverState(0, false)
	unscheduler := &mockTempUnscheduler{}
	accountID := int64(41)

	for retry := 1; retry <= group.BurstMode429RetryCount; retry++ {
		action := state.HandleFailoverError(ctx, unscheduler, accountID, service.PlatformOpenAI, 0, &service.UpstreamFailoverError{
			StatusCode: http.StatusTooManyRequests,
		})
		require.Equal(t, FailoverContinue, action)
		require.Equal(t, retry, state.SameAccountRetryCount[accountID])
		require.Equal(t, accountID, state.BurstRetryAccountID)
		require.NotContains(t, state.FailedAccountIDs, accountID)
		require.Zero(t, state.SwitchCount)
	}

	action := state.HandleFailoverError(ctx, unscheduler, accountID, service.PlatformOpenAI, 0, &service.UpstreamFailoverError{
		StatusCode: http.StatusTooManyRequests,
	})
	require.Equal(t, FailoverContinue, action)
	require.Contains(t, state.FailedAccountIDs, accountID)
	require.Equal(t, 1, state.SwitchCount)
	require.Zero(t, state.BurstRetryAccountID)
	require.Empty(t, unscheduler.calls)
}
