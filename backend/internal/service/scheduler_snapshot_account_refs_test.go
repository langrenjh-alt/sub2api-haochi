//go:build unit

package service

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
)

type accountRefsTestCache struct {
	snapshotHydrationCache
	calls       atomic.Int64
	entered     chan struct{}
	release     chan struct{}
	finished    chan struct{}
	enteredOnce sync.Once
}

type accountRefsRetiredCache struct {
	snapshotHydrationCache
}

func (c *accountRefsRetiredCache) GetSnapshot(context.Context, SchedulerBucket) ([]*Account, bool, error) {
	return nil, false, nil
}

func (c *accountRefsRetiredCache) CaptureBucketWriteToken(context.Context, SchedulerBucket) (SchedulerBucketWriteToken, error) {
	return SchedulerBucketWriteToken{}, ErrSchedulerBucketRetired
}

type accountRefsFallbackRepo struct {
	stubOpenAIAccountRepo
	listCalls atomic.Int64
}

func (r *accountRefsFallbackRepo) ListSchedulableByGroupIDAndPlatform(ctx context.Context, groupID int64, platform string) ([]Account, error) {
	r.listCalls.Add(1)
	return r.stubOpenAIAccountRepo.ListSchedulableByGroupIDAndPlatform(ctx, groupID, platform)
}

func (c *accountRefsTestCache) GetSnapshot(ctx context.Context, _ SchedulerBucket) ([]*Account, bool, error) {
	c.calls.Add(1)
	if c.entered != nil {
		c.enteredOnce.Do(func() { close(c.entered) })
	}
	if c.release != nil {
		select {
		case <-c.release:
		case <-ctx.Done():
			return nil, false, ctx.Err()
		}
	}
	if c.finished != nil {
		select {
		case c.finished <- struct{}{}:
		default:
		}
	}
	return cloneAccountRefsTestSnapshot(c.snapshot), true, nil
}

func cloneAccountRefsTestSnapshot(accounts []*Account) []*Account {
	cloned := make([]*Account, len(accounts))
	for i, account := range accounts {
		if account != nil {
			copy := *account
			cloned[i] = &copy
		}
	}
	return cloned
}

func newAccountRefsTestService(cache SchedulerCache, ttl time.Duration) *SchedulerSnapshotService {
	return NewSchedulerSnapshotService(cache, nil, nil, nil, &config.Config{
		Gateway: config.GatewayConfig{
			Scheduling: config.GatewaySchedulingConfig{
				LoadBatchCacheTTLMS: int(ttl / time.Millisecond),
			},
		},
	})
}

func TestSchedulerSnapshotAccountRefsCoalescesConcurrentReads(t *testing.T) {
	cache := &accountRefsTestCache{
		snapshotHydrationCache: snapshotHydrationCache{snapshot: []*Account{{
			ID:       1,
			Platform: PlatformGrok,
			Credentials: map[string]any{
				"model_mapping": map[string]any{"grok-4": "grok-4"},
			},
		}}},
		entered: make(chan struct{}),
		release: make(chan struct{}),
	}
	svc := newAccountRefsTestService(cache, 200*time.Millisecond)
	groupID := int64(20)

	const callers = 16
	start := make(chan struct{})
	results := make([][]*Account, callers)
	errs := make([]error, callers)
	var wg sync.WaitGroup
	wg.Add(callers)
	for i := 0; i < callers; i++ {
		go func(index int) {
			defer wg.Done()
			<-start
			results[index], _, errs[index] = svc.ListSchedulableAccountRefs(context.Background(), &groupID, PlatformGrok, false)
		}(i)
	}
	close(start)
	<-cache.entered
	time.Sleep(10 * time.Millisecond)
	close(cache.release)
	wg.Wait()

	require.Equal(t, int64(1), cache.calls.Load())
	for i := range callers {
		require.NoError(t, errs[i])
		require.Len(t, results[i], 1)
		require.Same(t, results[0][0], results[i][0])
	}
	require.True(t, results[0][0].modelMappingCacheReady)
}

func TestSchedulerSnapshotAccountRefsExpiresAndReloads(t *testing.T) {
	cache := &accountRefsTestCache{snapshotHydrationCache: snapshotHydrationCache{
		snapshot: []*Account{{ID: 1, Platform: PlatformGrok}},
	}}
	svc := newAccountRefsTestService(cache, 20*time.Millisecond)
	groupID := int64(20)

	first, _, err := svc.ListSchedulableAccountRefs(context.Background(), &groupID, PlatformGrok, false)
	require.NoError(t, err)
	second, _, err := svc.ListSchedulableAccountRefs(context.Background(), &groupID, PlatformGrok, false)
	require.NoError(t, err)
	require.Equal(t, int64(1), cache.calls.Load())
	require.Same(t, first[0], second[0])

	require.Eventually(t, func() bool {
		third, _, loadErr := svc.ListSchedulableAccountRefs(context.Background(), &groupID, PlatformGrok, false)
		return loadErr == nil && third[0] != first[0]
	}, time.Second, 5*time.Millisecond)
	require.GreaterOrEqual(t, cache.calls.Load(), int64(2))
	require.Equal(t, 1, svc.accountRefsCount)
}

func TestSchedulerSnapshotAccountRefsCallerCancellationDoesNotCancelSharedRead(t *testing.T) {
	cache := &accountRefsTestCache{
		snapshotHydrationCache: snapshotHydrationCache{snapshot: []*Account{{ID: 1, Platform: PlatformGrok}}},
		entered:                make(chan struct{}),
		release:                make(chan struct{}),
		finished:               make(chan struct{}, 1),
	}
	svc := newAccountRefsTestService(cache, 200*time.Millisecond)
	groupID := int64(20)
	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() {
		_, _, err := svc.ListSchedulableAccountRefs(ctx, &groupID, PlatformGrok, false)
		result <- err
	}()

	<-cache.entered
	cancel()
	select {
	case err := <-result:
		require.ErrorIs(t, err, context.Canceled)
	case <-time.After(time.Second):
		t.Fatal("canceled caller remained blocked on shared snapshot read")
	}
	close(cache.release)
	select {
	case <-cache.finished:
	case <-time.After(time.Second):
		t.Fatal("shared snapshot read did not finish after caller cancellation")
	}
	_, _, err := svc.ListSchedulableAccountRefs(context.Background(), &groupID, PlatformGrok, false)
	require.NoError(t, err)
}

func TestSchedulerSnapshotAccountRefsCanceledContextCannotHitLocalCache(t *testing.T) {
	cache := &accountRefsTestCache{snapshotHydrationCache: snapshotHydrationCache{
		snapshot: []*Account{{ID: 1, Platform: PlatformGrok}},
	}}
	svc := newAccountRefsTestService(cache, time.Second)
	groupID := int64(20)
	_, _, err := svc.ListSchedulableAccountRefs(context.Background(), &groupID, PlatformGrok, false)
	require.NoError(t, err)
	require.Equal(t, int64(1), cache.calls.Load())

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, _, err = svc.ListSchedulableAccountRefs(ctx, &groupID, PlatformGrok, false)
	require.ErrorIs(t, err, context.Canceled)
	require.Equal(t, int64(1), cache.calls.Load())
}

func TestSchedulerSnapshotAccountRefsStopFencesAndWaitsForSharedRead(t *testing.T) {
	cache := &accountRefsTestCache{
		snapshotHydrationCache: snapshotHydrationCache{snapshot: []*Account{{ID: 1, Platform: PlatformGrok}}},
		entered:                make(chan struct{}),
		release:                make(chan struct{}),
	}
	svc := newAccountRefsTestService(cache, time.Second)
	groupID := int64(20)
	requestDone := make(chan error, 1)
	go func() {
		_, _, err := svc.ListSchedulableAccountRefs(context.Background(), &groupID, PlatformGrok, false)
		requestDone <- err
	}()
	<-cache.entered

	stopDone := make(chan struct{})
	go func() {
		svc.Stop()
		close(stopDone)
	}()
	select {
	case <-stopDone:
		t.Fatal("Stop returned before the active shared snapshot read finished")
	case <-time.After(20 * time.Millisecond):
	}
	close(cache.release)

	select {
	case err := <-requestDone:
		require.ErrorIs(t, err, ErrSchedulerCacheNotReady)
	case <-time.After(time.Second):
		t.Fatal("snapshot caller did not return after Stop")
	}
	select {
	case <-stopDone:
	case <-time.After(time.Second):
		t.Fatal("Stop did not wait for the active shared snapshot read")
	}
	require.Empty(t, svc.accountRefs)
	require.Empty(t, svc.accountRefsLoads)
}

func TestSchedulerSnapshotAccountRefsRetiredBucketDoesNotFallbackOrCache(t *testing.T) {
	cache := &accountRefsRetiredCache{}
	repo := &accountRefsFallbackRepo{stubOpenAIAccountRepo: stubOpenAIAccountRepo{
		accounts: []Account{{ID: 1, Platform: PlatformGrok}},
	}}
	svc := NewSchedulerSnapshotService(cache, nil, repo, nil, &config.Config{
		Gateway: config.GatewayConfig{Scheduling: config.GatewaySchedulingConfig{
			LoadBatchCacheTTLMS: 200,
			DbFallbackEnabled:   true,
		}},
	})
	groupID := int64(20)

	_, _, err := svc.ListSchedulableAccountRefs(context.Background(), &groupID, PlatformGrok, false)
	require.ErrorIs(t, err, ErrSchedulerBucketRetired)
	require.Zero(t, repo.listCalls.Load())
	require.Empty(t, svc.accountRefs)
	require.Empty(t, svc.accountRefsLoads)
}

func TestSchedulerSnapshotAccountRefsInvalidationRejectsStaleRefill(t *testing.T) {
	svc := newAccountRefsTestService(nil, 200*time.Millisecond)
	bucket := SchedulerBucket{GroupID: 20, Platform: PlatformGrok, Mode: SchedulerModeSingle}
	generation, started := svc.beginAccountRefsLoad(bucket)
	require.True(t, started)
	accounts := []*Account{{ID: 1}}
	svc.storeCachedAccountRefs(bucket, accounts, time.Now(), generation)

	got, ok := svc.getCachedAccountRefs(bucket, time.Now())
	require.True(t, ok)
	require.Same(t, accounts[0], got[0])

	svc.invalidateCachedAccountRefs(bucket)
	_, ok = svc.getCachedAccountRefs(bucket, time.Now())
	require.False(t, ok)

	svc.storeCachedAccountRefs(bucket, accounts, time.Now(), generation)
	_, ok = svc.getCachedAccountRefs(bucket, time.Now())
	require.False(t, ok, "an in-flight read from the old generation must not refill the cache")
	svc.finishAccountRefsLoad(bucket)
	require.Empty(t, svc.accountRefsLoads)
}

func TestSchedulerSnapshotAccountRefsCacheIsBounded(t *testing.T) {
	svc := newAccountRefsTestService(nil, time.Second)
	now := time.Now()
	for i := 0; i < maxSchedulerAccountRefsCacheEntries+10; i++ {
		bucket := SchedulerBucket{GroupID: int64(i + 1), Platform: PlatformGrok, Mode: SchedulerModeSingle}
		generation, started := svc.beginAccountRefsLoad(bucket)
		require.True(t, started)
		svc.storeCachedAccountRefs(bucket, []*Account{{ID: int64(i + 1)}}, now, generation)
		svc.finishAccountRefsLoad(bucket)
	}

	require.LessOrEqual(t, len(svc.accountRefs), maxSchedulerAccountRefsCacheEntries)
	require.Equal(t, len(svc.accountRefs), svc.accountRefsCount)

	for bucket := range svc.accountRefs {
		svc.accountRefs[bucket] = cachedSchedulerAccountRefs{accounts: svc.accountRefs[bucket].accounts, expiresAt: now.Add(-time.Second)}
	}
	svc.accountRefsMu.Lock()
	svc.pruneCachedAccountRefsLocked(now)
	svc.accountRefsMu.Unlock()
	require.Empty(t, svc.accountRefs)
	require.Zero(t, svc.accountRefsCount)
	require.Empty(t, svc.accountRefsLoads)
}

func TestSchedulerSnapshotAccountRefsDoesNotCacheOversizedBucket(t *testing.T) {
	svc := newAccountRefsTestService(nil, time.Second)
	bucket := SchedulerBucket{GroupID: 20, Platform: PlatformGrok, Mode: SchedulerModeSingle}
	generation, started := svc.beginAccountRefsLoad(bucket)
	require.True(t, started)
	svc.storeCachedAccountRefs(bucket, make([]*Account, maxSchedulerAccountRefsCacheAccounts+1), time.Now(), generation)
	svc.finishAccountRefsLoad(bucket)

	require.Empty(t, svc.accountRefs)
	require.Zero(t, svc.accountRefsCount)
}

func TestSchedulerSnapshotAccountRefsTTLIsCapped(t *testing.T) {
	svc := newAccountRefsTestService(nil, time.Hour)
	require.Equal(t, maxSchedulerAccountRefsCacheTTL, svc.accountRefsTTL())
}
