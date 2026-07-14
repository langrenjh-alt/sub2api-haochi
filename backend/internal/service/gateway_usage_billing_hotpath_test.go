//go:build unit

package service

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

type deadlineCapturingPlatformQuotaRepo struct {
	called   bool
	deadline time.Time
}

type concurrentPlatformQuotaRepo struct {
	deadlineCapturingPlatformQuotaRepo
	active    atomic.Int64
	maxActive atomic.Int64
	calls     atomic.Int64
	entered   chan struct{}
	release   <-chan struct{}
}

func (r *concurrentPlatformQuotaRepo) IncrementUsageWithReset(ctx context.Context, _ int64, _ string, _ float64, _ time.Time) error {
	r.calls.Add(1)
	active := r.active.Add(1)
	defer r.active.Add(-1)
	for {
		maxActive := r.maxActive.Load()
		if active <= maxActive || r.maxActive.CompareAndSwap(maxActive, active) {
			break
		}
	}
	r.entered <- struct{}{}
	select {
	case <-r.release:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (r *deadlineCapturingPlatformQuotaRepo) GetByUserPlatform(context.Context, int64, string) (*UserPlatformQuotaRecord, error) {
	return nil, nil
}

func (r *deadlineCapturingPlatformQuotaRepo) BulkInsertInitial(context.Context, []UserPlatformQuotaRecord) error {
	return nil
}

func (r *deadlineCapturingPlatformQuotaRepo) IncrementUsageWithReset(ctx context.Context, _ int64, _ string, _ float64, _ time.Time) error {
	r.called = true
	r.deadline, _ = ctx.Deadline()
	return ctx.Err()
}

func TestPersistUserPlatformQuotaUsageHonorsParentCancellation(t *testing.T) {
	repo := &deadlineCapturingPlatformQuotaRepo{}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	require.ErrorIs(t, persistUserPlatformQuotaUsage(ctx, repo, 7, "openai", 1.25), context.Canceled)
}

func (r *deadlineCapturingPlatformQuotaRepo) ListByUser(context.Context, int64) ([]UserPlatformQuotaRecord, error) {
	return nil, nil
}

func (r *deadlineCapturingPlatformQuotaRepo) UpsertForUser(context.Context, int64, []UserPlatformQuotaRecord) error {
	return nil
}

func (r *deadlineCapturingPlatformQuotaRepo) ResetExpiredWindow(context.Context, int64, string, string, time.Time) error {
	return nil
}

func (r *deadlineCapturingPlatformQuotaRepo) BatchSnapshotUsage(context.Context, []UserPlatformQuotaSnapshot, time.Time) error {
	return nil
}

func TestPersistUserPlatformQuotaUsageRunsSynchronouslyWithDeadline(t *testing.T) {
	repo := &deadlineCapturingPlatformQuotaRepo{}
	startedAt := time.Now()

	require.NoError(t, persistUserPlatformQuotaUsage(context.Background(), repo, 7, "openai", 1.25))
	require.True(t, repo.called)
	require.False(t, repo.deadline.IsZero())
	require.WithinDuration(t, startedAt.Add(userPlatformQuotaPersistTimeout), repo.deadline, time.Second)
}

func TestPersistUserPlatformQuotaUsageEnforcesGlobalConcurrencyLimit(t *testing.T) {
	release := make(chan struct{})
	total := userPlatformQuotaPersistConcurrency + 5
	repo := &concurrentPlatformQuotaRepo{
		entered: make(chan struct{}, total),
		release: release,
	}
	start := make(chan struct{})
	errs := make(chan error, total)
	var wg sync.WaitGroup
	for i := 0; i < total; i++ {
		wg.Add(1)
		go func(id int64) {
			defer wg.Done()
			<-start
			errs <- persistUserPlatformQuotaUsage(context.Background(), repo, id, "openai", 1)
		}(int64(i + 1))
	}
	close(start)

	for i := 0; i < userPlatformQuotaPersistConcurrency; i++ {
		select {
		case <-repo.entered:
		case <-time.After(time.Second):
			t.Fatal("quota persistence did not fill all concurrency slots")
		}
	}
	select {
	case <-repo.entered:
		t.Fatal("quota persistence exceeded its global concurrency limit")
	case <-time.After(50 * time.Millisecond):
	}
	require.Equal(t, int64(userPlatformQuotaPersistConcurrency), repo.calls.Load())
	require.Equal(t, int64(userPlatformQuotaPersistConcurrency), repo.maxActive.Load())

	close(release)
	wg.Wait()
	close(errs)
	for err := range errs {
		require.NoError(t, err)
	}
	require.Equal(t, int64(total), repo.calls.Load())
	require.LessOrEqual(t, repo.maxActive.Load(), int64(userPlatformQuotaPersistConcurrency))
}

func TestPersistUserPlatformQuotaUsageSlotWaitUsesTotalTimeout(t *testing.T) {
	slots := make(chan struct{}, 1)
	slots <- struct{}{}
	repo := &deadlineCapturingPlatformQuotaRepo{}
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	startedAt := time.Now()
	err := persistUserPlatformQuotaUsageWithSlots(ctx, repo, 7, "openai", 1.25, slots)
	require.ErrorIs(t, err, context.DeadlineExceeded)
	require.False(t, repo.called)
	require.Less(t, time.Since(startedAt), 500*time.Millisecond)
}

func TestBillingNotificationContextsHaveIndependentDeadlines(t *testing.T) {
	first, cancelFirst := newBillingNotificationContext(context.Background())
	second, cancelSecond := newBillingNotificationContext(context.Background())
	defer cancelSecond()

	cancelFirst()
	require.ErrorIs(t, first.Err(), context.Canceled)
	require.NoError(t, second.Err())
	firstDeadline, firstHasDeadline := first.Deadline()
	secondDeadline, secondHasDeadline := second.Deadline()
	require.True(t, firstHasDeadline)
	require.True(t, secondHasDeadline)
	require.WithinDuration(t, firstDeadline, secondDeadline, time.Second)
}
