package handler

import (
	"context"

	"github.com/Wei-Shaw/sub2api/internal/service"
)

func burstModeRetryPolicy(ctx context.Context, failoverErr *service.UpstreamFailoverError, configuredLimit int) (retryable bool, retryLimit int) {
	if failoverErr == nil {
		return false, configuredLimit
	}
	if service.BurstModeHandles429(ctx, failoverErr.StatusCode) {
		return true, service.BurstMode429RetryLimit(ctx)
	}
	return failoverErr.RetryableOnSameAccount, configuredLimit
}

func burstModeMaxSwitches(ctx context.Context, configured int) int {
	if !service.BurstModeEnabled(ctx) {
		return configured
	}
	// The burst selector is finite and excludes each failed account. This high
	// request-local ceiling lets it traverse large groups without changing defaults.
	return int(^uint(0) >> 1)
}

func shouldStopOpenAI429FailoverInMode(ctx context.Context) bool {
	return !service.BurstModeEnabled(ctx)
}
