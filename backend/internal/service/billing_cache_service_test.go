package service

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
)

type blockingRateLimitResetLoader struct {
	calls   int64
	started chan struct{}
	release chan struct{}
	done    chan struct{}
}

type concurrentRateLimitResetLoader struct {
	calls   atomic.Int64
	release chan struct{}
}

func (l *concurrentRateLimitResetLoader) GetRateLimitData(context.Context, int64) (*APIKeyRateLimitData, error) {
	return nil, errors.New("not implemented")
}

func (l *concurrentRateLimitResetLoader) ResetRateLimitWindows(context.Context, int64) error {
	l.calls.Add(1)
	<-l.release
	return nil
}

func (l *blockingRateLimitResetLoader) GetRateLimitData(context.Context, int64) (*APIKeyRateLimitData, error) {
	return nil, errors.New("not implemented")
}

func (l *blockingRateLimitResetLoader) ResetRateLimitWindows(context.Context, int64) error {
	if atomic.AddInt64(&l.calls, 1) == 1 {
		close(l.started)
	}
	<-l.release
	close(l.done)
	return nil
}

type billingCacheWorkerStub struct {
	balanceUpdates      int64
	subscriptionUpdates int64
}

func (b *billingCacheWorkerStub) GetUserBalance(ctx context.Context, userID int64) (float64, error) {
	return 0, errors.New("not implemented")
}

func (b *billingCacheWorkerStub) SetUserBalance(ctx context.Context, userID int64, balance float64) error {
	atomic.AddInt64(&b.balanceUpdates, 1)
	return nil
}

func (b *billingCacheWorkerStub) DeductUserBalance(ctx context.Context, userID int64, amount float64) error {
	atomic.AddInt64(&b.balanceUpdates, 1)
	return nil
}

func (b *billingCacheWorkerStub) InvalidateUserBalance(ctx context.Context, userID int64) error {
	return nil
}

func (b *billingCacheWorkerStub) GetSubscriptionCache(ctx context.Context, userID, groupID int64) (*SubscriptionCacheData, error) {
	return nil, errors.New("not implemented")
}

func (b *billingCacheWorkerStub) SetSubscriptionCache(ctx context.Context, userID, groupID int64, data *SubscriptionCacheData) error {
	atomic.AddInt64(&b.subscriptionUpdates, 1)
	return nil
}

func (b *billingCacheWorkerStub) UpdateSubscriptionUsage(ctx context.Context, userID, groupID int64, cost float64) error {
	atomic.AddInt64(&b.subscriptionUpdates, 1)
	return nil
}

func (b *billingCacheWorkerStub) InvalidateSubscriptionCache(ctx context.Context, userID, groupID int64) error {
	return nil
}

func (b *billingCacheWorkerStub) GetAPIKeyRateLimit(ctx context.Context, keyID int64) (*APIKeyRateLimitCacheData, error) {
	return nil, errors.New("not implemented")
}

func (b *billingCacheWorkerStub) SetAPIKeyRateLimit(ctx context.Context, keyID int64, data *APIKeyRateLimitCacheData) error {
	return nil
}

func (b *billingCacheWorkerStub) UpdateAPIKeyRateLimitUsage(ctx context.Context, keyID int64, cost float64) error {
	return nil
}

func (b *billingCacheWorkerStub) InvalidateAPIKeyRateLimit(ctx context.Context, keyID int64) error {
	return nil
}

func (b *billingCacheWorkerStub) GetUserPlatformQuotaCache(ctx context.Context, userID int64, platform string) (*UserPlatformQuotaCacheEntry, bool, error) {
	return nil, false, nil
}

func (b *billingCacheWorkerStub) SetUserPlatformQuotaCache(ctx context.Context, userID int64, platform string, entry *UserPlatformQuotaCacheEntry, ttl time.Duration) error {
	return nil
}

func (b *billingCacheWorkerStub) DeleteUserPlatformQuotaCache(ctx context.Context, userID int64, platform string) error {
	return nil
}

func (b *billingCacheWorkerStub) IncrUserPlatformQuotaUsageCache(ctx context.Context, userID int64, platform string, cost float64, ttl time.Duration, markDirty bool) error {
	return nil
}

func (b *billingCacheWorkerStub) PopDirtyUserPlatformQuotaKeys(ctx context.Context, n int) ([]UserPlatformQuotaKey, error) {
	return nil, nil
}

func (b *billingCacheWorkerStub) ReaddDirtyUserPlatformQuotaKeys(ctx context.Context, keys []UserPlatformQuotaKey) error {
	return nil
}

func (b *billingCacheWorkerStub) BatchGetUserPlatformQuotaCache(ctx context.Context, keys []UserPlatformQuotaKey) ([]*UserPlatformQuotaCacheEntry, error) {
	return nil, nil
}

func TestBillingCacheServiceQueueHighLoad(t *testing.T) {
	cache := &billingCacheWorkerStub{}
	svc := NewBillingCacheService(cache, nil, nil, nil, nil, nil, &config.Config{}, nil)
	t.Cleanup(svc.Stop)

	start := time.Now()
	for i := 0; i < cacheWriteBufferSize*2; i++ {
		svc.QueueDeductBalance(1, 1)
	}
	require.Less(t, time.Since(start), 2*time.Second)

	svc.QueueUpdateSubscriptionUsage(1, 2, 1.5)

	require.Eventually(t, func() bool {
		return atomic.LoadInt64(&cache.balanceUpdates) > 0
	}, 2*time.Second, 10*time.Millisecond)

	require.Eventually(t, func() bool {
		return atomic.LoadInt64(&cache.subscriptionUpdates) > 0
	}, 2*time.Second, 10*time.Millisecond)
}

func TestBillingCacheServiceEnqueueAfterStopReturnsFalse(t *testing.T) {
	cache := &billingCacheWorkerStub{}
	svc := NewBillingCacheService(cache, nil, nil, nil, nil, nil, &config.Config{}, nil)
	svc.Stop()

	enqueued := svc.enqueueCacheWrite(cacheWriteTask{
		kind:   cacheWriteDeductBalance,
		userID: 1,
		amount: 1,
	})
	require.False(t, enqueued)
}

func TestBillingCacheServiceCoalescesExpiredWindowResets(t *testing.T) {
	loader := &blockingRateLimitResetLoader{
		started: make(chan struct{}),
		release: make(chan struct{}),
		done:    make(chan struct{}),
	}
	svc := &BillingCacheService{
		cache:                 &billingCacheWorkerStub{},
		apiKeyRateLimitLoader: loader,
	}
	expired := time.Now().Add(-RateLimitWindow5h - time.Minute)
	apiKey := &APIKey{
		ID:            42,
		RateLimit5h:   10,
		Window5hStart: &expired,
	}

	var wg sync.WaitGroup
	var evaluationErrors int64
	for i := 0; i < 200; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := svc.evaluateRateLimits(context.Background(), apiKey, 1, 0, 0, apiKey.Window5hStart, nil, nil); err != nil {
				atomic.AddInt64(&evaluationErrors, 1)
			}
		}()
	}
	wg.Wait()
	require.Zero(t, atomic.LoadInt64(&evaluationErrors))

	select {
	case <-loader.started:
	case <-time.After(time.Second):
		t.Fatal("rate limit reset did not start")
	}
	require.Equal(t, int64(1), atomic.LoadInt64(&loader.calls))

	for i := 0; i < 20; i++ {
		require.NoError(t, svc.evaluateRateLimits(context.Background(), apiKey, 1, 0, 0, apiKey.Window5hStart, nil, nil))
	}
	require.Equal(t, int64(1), atomic.LoadInt64(&loader.calls))

	close(loader.release)
	select {
	case <-loader.done:
	case <-time.After(time.Second):
		t.Fatal("rate limit reset did not finish")
	}

	for i := 0; i < 20; i++ {
		require.NoError(t, svc.evaluateRateLimits(context.Background(), apiKey, 1, 0, 0, apiKey.Window5hStart, nil, nil))
	}
	require.Equal(t, int64(1), atomic.LoadInt64(&loader.calls))
}

func TestBillingCacheServiceDoesNotResetNilRateLimitWindows(t *testing.T) {
	loader := &concurrentRateLimitResetLoader{release: make(chan struct{})}
	svc := &BillingCacheService{
		cache:                 &billingCacheWorkerStub{},
		apiKeyRateLimitLoader: loader,
	}
	apiKey := &APIKey{ID: 42, RateLimit5h: 10}

	for i := 0; i < 20; i++ {
		require.NoError(t, svc.evaluateRateLimits(context.Background(), apiKey, apiKey.RateLimit5h, 0, 0, nil, nil, nil))
	}
	require.Never(t, func() bool { return loader.calls.Load() > 0 }, 100*time.Millisecond, 10*time.Millisecond)
	close(loader.release)
}

func TestBillingCacheServiceBoundsDistinctRateLimitResets(t *testing.T) {
	loader := &concurrentRateLimitResetLoader{release: make(chan struct{})}
	svc := &BillingCacheService{
		cache:                 &billingCacheWorkerStub{},
		apiKeyRateLimitLoader: loader,
	}
	expired := time.Now().Add(-RateLimitWindow5h - time.Minute)

	for i := 1; i <= apiKeyRateLimitResetConcurrency*4; i++ {
		apiKey := &APIKey{ID: int64(i), RateLimit5h: 10}
		require.NoError(t, svc.evaluateRateLimits(context.Background(), apiKey, 1, 0, 0, &expired, nil, nil))
	}
	require.Eventually(t, func() bool {
		return loader.calls.Load() == int64(apiKeyRateLimitResetConcurrency)
	}, time.Second, 10*time.Millisecond)
	require.Equal(t, int64(apiKeyRateLimitResetConcurrency), loader.calls.Load())
	require.LessOrEqual(t, len(svc.apiKeyRateLimitResets), apiKeyRateLimitResetConcurrency)
	close(loader.release)
}
