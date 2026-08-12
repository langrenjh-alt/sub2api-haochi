package service

import (
	"context"
	"net/http"

	"github.com/Wei-Shaw/sub2api/internal/pkg/ctxkey"
)

const BurstModeSameAccount429Retries = 5

type burstModeRetryAccountContextKey struct{}

func WithBurstModeRetryAccount(ctx context.Context, accountID int64) context.Context {
	if ctx == nil || accountID <= 0 || !BurstModeEnabled(ctx) {
		return ctx
	}
	return context.WithValue(ctx, burstModeRetryAccountContextKey{}, accountID)
}

func burstModeRetryAccountID(ctx context.Context) int64 {
	if ctx == nil || !BurstModeEnabled(ctx) {
		return 0
	}
	accountID, _ := ctx.Value(burstModeRetryAccountContextKey{}).(int64)
	return accountID
}

func BurstModeGroupFromContext(ctx context.Context) *Group {
	if ctx == nil {
		return nil
	}
	group, _ := ctx.Value(ctxkey.Group).(*Group)
	if !IsGroupContextValid(group) || !group.BurstModeEnabled {
		return nil
	}
	return group
}

func BurstModeEnabled(ctx context.Context) bool {
	return BurstModeGroupFromContext(ctx) != nil
}

func BurstModeHandles429(ctx context.Context, statusCode int) bool {
	return statusCode == http.StatusTooManyRequests && BurstModeEnabled(ctx)
}

// burstModeAcquireLimit turns the inclusive pre-acquire threshold into the
// exclusive Redis acquire limit.
func burstModeAcquireLimit(accountConcurrency, thresholdPercent int) int {
	if accountConcurrency <= 0 {
		return accountConcurrency
	}
	if thresholdPercent < 1 || thresholdPercent > 100 {
		thresholdPercent = DefaultBurstModeThresholdPercent
	}
	limit := accountConcurrency*thresholdPercent/100 + 1
	if limit < 1 {
		limit = 1
	}
	return limit
}
