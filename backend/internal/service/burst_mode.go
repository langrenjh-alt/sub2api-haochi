package service

import (
	"context"
	"net/http"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/ctxkey"
)

const BurstModeSameAccount429Retries = DefaultBurstMode429RetryCount

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

func BurstMode429RetryLimit(ctx context.Context) int {
	return burstMode429RetryCount(BurstModeGroupFromContext(ctx))
}

func burstMode429RetryCount(group *Group) int {
	if group == nil || group.BurstMode429RetryCount < 1 {
		return DefaultBurstMode429RetryCount
	}
	if group.BurstMode429RetryCount > 100 {
		return 100
	}
	return group.BurstMode429RetryCount
}

func BurstModeHighUsageEnabled(ctx context.Context) bool {
	return burstModeHighUsageEnabledForGroup(BurstModeGroupFromContext(ctx))
}

func burstModeHighUsageEnabledForGroup(group *Group) bool {
	return group != nil && group.Platform == PlatformOpenAI && group.BurstModeHighUsageEnabled
}

func BurstModeAccountUsagePercent(account *Account) float64 {
	if account == nil || account.Platform != PlatformOpenAI || len(account.Extra) == 0 {
		return 0
	}
	now := time.Now()
	maxPercent := 0.0
	consider := func(percent float64) {
		if percent > 100 {
			percent = 100
		}
		if percent > maxPercent {
			maxPercent = percent
		}
	}
	for _, window := range []string{"5h", "7d"} {
		if percent, ok := resolveOpenAIQuotaUtilization(account.Extra, window, now); ok {
			consider(percent * 100)
		}
	}
	return maxPercent
}

func BurstModeHighUsageAccount(account *Account) bool {
	return BurstModeAccountUsagePercent(account) >= BurstModeHighUsagePercent
}

func BurstModeHandles429(ctx context.Context, statusCode int) bool {
	return statusCode == http.StatusTooManyRequests && BurstModeEnabled(ctx)
}

// burstModeAcquireLimit reserves capacity above the burst threshold for
// requests that are already sticky to the account.
func burstModeAcquireLimit(accountConcurrency, thresholdPercent int, sticky bool) int {
	if accountConcurrency <= 0 {
		return accountConcurrency
	}
	if sticky {
		return accountConcurrency
	}
	if thresholdPercent < 1 || thresholdPercent > 100 {
		thresholdPercent = DefaultBurstModeThresholdPercent
	}
	limit := accountConcurrency * thresholdPercent / 100
	if limit < 1 {
		limit = 1
	}
	if limit > accountConcurrency {
		limit = accountConcurrency
	}
	return limit
}
