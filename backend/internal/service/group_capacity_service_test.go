package service

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

type groupCapacityAccountRepoStub struct {
	AccountRepository
	rows            []GroupAccountCapacityRow
	publicRows      []PublicCapacityAccountRow
	requested       []int64
	publicRequested []int64
}

func (s *groupCapacityAccountRepoStub) ListSchedulableCapacityByGroupIDs(_ context.Context, groupIDs []int64) ([]GroupAccountCapacityRow, error) {
	s.requested = append([]int64(nil), groupIDs...)
	return append([]GroupAccountCapacityRow(nil), s.rows...), nil
}

func (s *groupCapacityAccountRepoStub) ListPublicCapacityPoolAccountsByGroupIDs(_ context.Context, groupIDs []int64) ([]PublicCapacityAccountRow, error) {
	s.publicRequested = append([]int64(nil), groupIDs...)
	return append([]PublicCapacityAccountRow(nil), s.publicRows...), nil
}

type groupCapacityGroupRepoStub struct {
	GroupRepository
	groupIDs  []int64
	groups    []Group
	listCalls int
}

func (s *groupCapacityGroupRepoStub) ListActiveIDs(context.Context) ([]int64, error) {
	s.listCalls++
	return append([]int64(nil), s.groupIDs...), nil
}

func (s *groupCapacityGroupRepoStub) ListActive(context.Context) ([]Group, error) {
	s.listCalls++
	return append([]Group(nil), s.groups...), nil
}

type groupCapacityConcurrencyCacheStub struct {
	ConcurrencyCache
	counts    map[int64]int
	requested []int64
}

func (s *groupCapacityConcurrencyCacheStub) GetAccountConcurrencyBatch(_ context.Context, accountIDs []int64) (map[int64]int, error) {
	s.requested = append([]int64(nil), accountIDs...)
	out := make(map[int64]int, len(accountIDs))
	for _, id := range accountIDs {
		out[id] = s.counts[id]
	}
	return out, nil
}

type groupCapacitySessionCacheStub struct {
	SessionLimitCache
	counts       map[int64]int
	requested    []int64
	idleTimeouts map[int64]time.Duration
}

func (s *groupCapacitySessionCacheStub) GetActiveSessionCountBatch(_ context.Context, accountIDs []int64, idleTimeouts map[int64]time.Duration) (map[int64]int, error) {
	s.requested = append([]int64(nil), accountIDs...)
	s.idleTimeouts = make(map[int64]time.Duration, len(idleTimeouts))
	for id, timeout := range idleTimeouts {
		s.idleTimeouts[id] = timeout
	}
	out := make(map[int64]int, len(accountIDs))
	for _, id := range accountIDs {
		out[id] = s.counts[id]
	}
	return out, nil
}

type groupCapacityRPMCacheStub struct {
	RPMCache
	counts    map[int64]int
	requested []int64
}

func (s *groupCapacityRPMCacheStub) GetRPMBatch(_ context.Context, accountIDs []int64) (map[int64]int, error) {
	s.requested = append([]int64(nil), accountIDs...)
	out := make(map[int64]int, len(accountIDs))
	for _, id := range accountIDs {
		out[id] = s.counts[id]
	}
	return out, nil
}

func TestGetAllGroupCapacityBatchAggregatesRuntimeAndLimits(t *testing.T) {
	accountRepo := &groupCapacityAccountRepoStub{
		rows: []GroupAccountCapacityRow{
			{
				GroupID:     10,
				AccountID:   1,
				Concurrency: 2,
				Extra: map[string]any{
					"max_sessions":                 3,
					"session_idle_timeout_minutes": 7,
					"base_rpm":                     11,
				},
			},
			{
				GroupID:     20,
				AccountID:   1,
				Concurrency: 2,
				Extra: map[string]any{
					"max_sessions":                 3,
					"session_idle_timeout_minutes": 7,
					"base_rpm":                     11,
				},
			},
			{
				GroupID:     20,
				AccountID:   2,
				Concurrency: 4,
				Extra: map[string]any{
					"max_sessions":                 1,
					"session_idle_timeout_minutes": 9,
					"base_rpm":                     13,
				},
			},
		},
	}
	groupRepo := &groupCapacityGroupRepoStub{groupIDs: []int64{10, 20}}
	concurrencyCache := &groupCapacityConcurrencyCacheStub{counts: map[int64]int{1: 1, 2: 2}}
	sessionCache := &groupCapacitySessionCacheStub{counts: map[int64]int{1: 2, 2: 1}}
	rpmCache := &groupCapacityRPMCacheStub{counts: map[int64]int{1: 5, 2: 7}}
	svc := NewGroupCapacityService(
		accountRepo,
		groupRepo,
		NewConcurrencyService(concurrencyCache),
		sessionCache,
		rpmCache,
	)

	results, err := svc.GetAllGroupCapacity(context.Background())
	require.NoError(t, err)

	require.Equal(t, 1, groupRepo.listCalls)
	require.Equal(t, []int64{10, 20}, accountRepo.requested)
	require.Equal(t, []int64{1, 2}, concurrencyCache.requested)
	require.ElementsMatch(t, []int64{1, 2}, sessionCache.requested)
	require.ElementsMatch(t, []int64{1, 2}, rpmCache.requested)
	require.Equal(t, 7*time.Minute, sessionCache.idleTimeouts[1])
	require.Equal(t, 9*time.Minute, sessionCache.idleTimeouts[2])

	require.Equal(t, []GroupCapacitySummary{
		{
			GroupID:         10,
			ConcurrencyUsed: 1,
			ConcurrencyMax:  2,
			SessionsUsed:    2,
			SessionsMax:     3,
			RPMUsed:         5,
			RPMMax:          11,
		},
		{
			GroupID:         20,
			ConcurrencyUsed: 3,
			ConcurrencyMax:  6,
			SessionsUsed:    3,
			SessionsMax:     4,
			RPMUsed:         12,
			RPMMax:          24,
		},
	}, results)
}

func TestGetAllGroupCapacityBatchKeepsEmptyGroupRows(t *testing.T) {
	accountRepo := &groupCapacityAccountRepoStub{
		rows: []GroupAccountCapacityRow{
			{GroupID: 20, AccountID: 2, Concurrency: 4},
		},
	}
	groupRepo := &groupCapacityGroupRepoStub{groupIDs: []int64{10, 20}}
	svc := NewGroupCapacityService(accountRepo, groupRepo, nil, nil, nil)

	results, err := svc.GetAllGroupCapacity(context.Background())
	require.NoError(t, err)

	require.Equal(t, []GroupCapacitySummary{
		{GroupID: 10},
		{GroupID: 20, ConcurrencyMax: 4},
	}, results)
}

func TestGetPublicCapacityPoolFiltersPublicStandardGroupsAndBucketsStatuses(t *testing.T) {
	now := time.Now().UTC()
	later := now.Add(time.Hour)
	past := now.Add(-time.Hour)

	accountRepo := &groupCapacityAccountRepoStub{
		publicRows: []PublicCapacityAccountRow{
			{
				GroupID:     10,
				AccountID:   1,
				Platform:    PlatformOpenAI,
				Type:        AccountTypeOAuth,
				Status:      StatusActive,
				Schedulable: true,
				Concurrency: 3,
				Extra: map[string]any{
					"codex_5h_used_percent": 25,
					"codex_7d_used_percent": 50,
				},
			},
			{
				GroupID:          10,
				AccountID:        2,
				Platform:         PlatformOpenAI,
				Type:             AccountTypeOAuth,
				Status:           StatusActive,
				Schedulable:      true,
				Concurrency:      2,
				RateLimitResetAt: &later,
			},
			{
				GroupID:                10,
				AccountID:              3,
				Platform:               PlatformOpenAI,
				Type:                   AccountTypeOAuth,
				Status:                 StatusActive,
				Schedulable:            true,
				Concurrency:            2,
				TempUnschedulableUntil: &later,
			},
			{
				GroupID:     10,
				AccountID:   4,
				Platform:    PlatformOpenAI,
				Type:        AccountTypeOAuth,
				Status:      StatusError,
				Schedulable: true,
				Concurrency: 2,
			},
			{
				GroupID:            10,
				AccountID:          5,
				Platform:           PlatformOpenAI,
				Type:               AccountTypeOAuth,
				Status:             StatusActive,
				Schedulable:        true,
				Concurrency:        2,
				ExpiresAt:          &past,
				AutoPauseOnExpired: true,
			},
			{
				GroupID:     40,
				AccountID:   6,
				Platform:    PlatformAnthropic,
				Type:        AccountTypeOAuth,
				Status:      StatusActive,
				Schedulable: true,
				Concurrency: 4,
				Extra: map[string]any{
					"max_sessions":                 2,
					"session_idle_timeout_minutes": 9,
					"base_rpm":                     10,
				},
			},
		},
	}
	groupRepo := &groupCapacityGroupRepoStub{
		groups: []Group{
			{ID: 10, Name: "Public Standard", Platform: PlatformOpenAI, Status: StatusActive, SubscriptionType: SubscriptionTypeStandard},
			{ID: 20, Name: "Exclusive", Platform: PlatformOpenAI, Status: StatusActive, SubscriptionType: SubscriptionTypeStandard, IsExclusive: true},
			{ID: 30, Name: "Subscription", Platform: PlatformOpenAI, Status: StatusActive, SubscriptionType: SubscriptionTypeSubscription},
			{ID: 40, Name: "Legacy Standard", Platform: PlatformAnthropic, Status: StatusActive},
		},
	}
	concurrencyCache := &groupCapacityConcurrencyCacheStub{counts: map[int64]int{
		1: 1,
		2: 1,
		3: 1,
		4: 1,
		5: 1,
		6: 2,
	}}
	sessionCache := &groupCapacitySessionCacheStub{counts: map[int64]int{6: 1}}
	rpmCache := &groupCapacityRPMCacheStub{counts: map[int64]int{6: 4}}
	svc := NewGroupCapacityService(accountRepo, groupRepo, NewConcurrencyService(concurrencyCache), sessionCache, rpmCache)

	pool, err := svc.GetPublicCapacityPool(context.Background())
	require.NoError(t, err)

	require.Equal(t, []int64{10, 40}, accountRepo.publicRequested)
	require.Equal(t, 2, pool.Summary.GroupTotal)
	require.Equal(t, 6, pool.Summary.AccountTotal)
	require.Equal(t, 2, pool.Summary.AvailableAccounts)
	require.Equal(t, 1, pool.Summary.RateLimitedAccounts)
	require.Equal(t, 1, pool.Summary.QuotaLimitedAccounts)
	require.Equal(t, 1, pool.Summary.ErrorAccounts)
	require.Equal(t, 1, pool.Summary.DisabledAccounts)
	require.Equal(t, 15, pool.Summary.Capacity.Concurrency.Max)
	require.Equal(t, 7, pool.Summary.Capacity.Concurrency.Used)
	require.Equal(t, 4, pool.Summary.Capacity.Concurrency.Available)

	require.Len(t, pool.Groups, 2)
	first := pool.Groups[0]
	require.Equal(t, int64(10), first.GroupID)
	require.Equal(t, "degraded", first.Status)
	require.Equal(t, PublicCapacityStatusCounts{
		Normal:       1,
		RateLimited:  1,
		QuotaLimited: 1,
		Error:        1,
		Disabled:     1,
	}, first.StatusCounts)
	require.Equal(t, 11, first.Capacity.Concurrency.Max)
	require.Equal(t, 5, first.Capacity.Concurrency.Used)
	require.Equal(t, 2, first.Capacity.Concurrency.Available)
	require.Equal(t, 1, first.Window5h.TrackedAccounts)
	require.InDelta(t, 25, first.Window5h.UsedPercent, 0.001)
	require.InDelta(t, 0.75, first.Window5h.RemainingCapacity, 0.001)

	second := pool.Groups[1]
	require.Equal(t, int64(40), second.GroupID)
	require.Equal(t, "normal", second.Status)
	require.Equal(t, 4, second.Capacity.Concurrency.Max)
	require.Equal(t, 2, second.Capacity.Concurrency.Used)
	require.Equal(t, 2, second.Capacity.Concurrency.Available)
	require.Equal(t, []int64{6}, sessionCache.requested)
	require.Equal(t, []int64{6}, rpmCache.requested)
}
