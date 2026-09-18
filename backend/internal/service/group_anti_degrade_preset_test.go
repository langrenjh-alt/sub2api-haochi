package service

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
)

func TestNormalizeGroupAntiDegradePreset(t *testing.T) {
	t.Run("empty means off", func(t *testing.T) {
		got, err := NormalizeGroupAntiDegradePreset("")
		require.NoError(t, err)
		require.Equal(t, AntiDegradePresetOff, got)
		require.False(t, IsGroupAntiDegradePresetActive(got))
	})

	t.Run("whitespace is treated as off", func(t *testing.T) {
		got, err := NormalizeGroupAntiDegradePreset("   ")
		require.NoError(t, err)
		require.Equal(t, AntiDegradePresetOff, got)
	})

	t.Run("registered apply-supported preset passes", func(t *testing.T) {
		got, err := NormalizeGroupAntiDegradePreset("low_concurrency")
		require.NoError(t, err)
		require.Equal(t, "low_concurrency", got)
		require.True(t, IsGroupAntiDegradePresetActive(got))

		got, err = NormalizeGroupAntiDegradePreset(" legacy ")
		require.NoError(t, err)
		require.Equal(t, "legacy", got)
	})

	t.Run("native baseline is rejected in favour of off", func(t *testing.T) {
		_, err := NormalizeGroupAntiDegradePreset(string(AntiDegradeModeNative))
		require.Error(t, err)
		require.Equal(t, "INVALID_ANTI_DEGRADE_PRESET", infraerrors.Reason(err))
	})

	t.Run("unknown preset is rejected", func(t *testing.T) {
		_, err := NormalizeGroupAntiDegradePreset("does_not_exist")
		require.Error(t, err)
		require.Equal(t, "INVALID_ANTI_DEGRADE_PRESET", infraerrors.Reason(err))
	})
}

func TestIsGroupPresetSkipReason(t *testing.T) {
	require.False(t, isGroupPresetSkipReason(nil))

	notEligible := infraerrors.New(400, "PROTECTION_NOT_ELIGIBLE", "模式一仅支持 OpenAI OAuth / Setup Token 账号")
	require.True(t, isGroupPresetSkipReason(notEligible))

	invalidPreset := infraerrors.New(400, "INVALID_ANTI_DEGRADE_PRESET", "unknown anti-degrade preset: nope")
	require.True(t, isGroupPresetSkipReason(invalidPreset))

	// 真实故障不能被当成"跳过"，否则状态面板永远看不到问题。
	other := infraerrors.New(500, "PROTECTION_CONFLICT", "account was modified concurrently")
	require.False(t, isGroupPresetSkipReason(other))
}

func TestAntiDegradeMarkerSource(t *testing.T) {
	t.Run("nil account", func(t *testing.T) {
		kind, groupID, preset, fromGroup := AntiDegradeMarkerSource(nil)
		require.Equal(t, "", kind)
		require.Zero(t, groupID)
		require.Equal(t, "", preset)
		require.False(t, fromGroup)
	})

	t.Run("manual apply has no group provenance", func(t *testing.T) {
		a := &Account{Extra: map[string]any{
			AntiDegradeMarkerExtraKey: map[string]any{"enabled": true, "mode": "legacy"},
		}}
		kind, groupID, _, fromGroup := AntiDegradeMarkerSource(a)
		require.Equal(t, "", kind)
		require.Zero(t, groupID)
		require.False(t, fromGroup)
	})

	t.Run("group-sourced marker round-trips", func(t *testing.T) {
		ctx := WithAntiDegradeApplySource(nil, AntiDegradeApplySource{}) // nil ctx / empty source is a no-op
		require.Nil(t, ctx)

		a := &Account{Extra: map[string]any{
			AntiDegradeMarkerExtraKey: map[string]any{
				"enabled":                        true,
				"mode":                           "low_concurrency",
				antiDegradeMarkerSourceKey:       AntiDegradeSourceGroup,
				antiDegradeMarkerSourceGroupID:   43,
				antiDegradeMarkerSourcePresetKey: "low_concurrency",
			},
		}}
		kind, groupID, preset, fromGroup := AntiDegradeMarkerSource(a)
		require.Equal(t, AntiDegradeSourceGroup, kind)
		require.Equal(t, int64(43), groupID)
		require.Equal(t, "low_concurrency", preset)
		require.True(t, fromGroup)
	})
}

func TestGroupMode1PresetPreservesSourceForRollback(t *testing.T) {
	store := &stubAntiDegradeStore{account: &Account{ID: 9, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Concurrency: 4}}
	svc := NewAntiDegradeService(store)
	ctx := WithAntiDegradeApplySource(context.Background(), AntiDegradeApplySource{Kind: AntiDegradeSourceGroup, GroupID: 43})
	_, err := svc.ApplyAntiDegradeMode(ctx, 9, AntiDegradeMode1)
	require.NoError(t, err)
	require.NotNil(t, store.updated)
	account := &Account{Extra: store.updated.Extra}
	kind, groupID, preset, fromGroup := AntiDegradeMarkerSource(account)
	require.True(t, fromGroup)
	require.Equal(t, AntiDegradeSourceGroup, kind)
	require.Equal(t, int64(43), groupID)
	require.Equal(t, "mode1", preset)
	store.account.Extra = store.updated.Extra
	_, err = svc.RevertAntiDegrade(context.Background(), 9)
	require.NoError(t, err)
	require.NotContains(t, store.updated.Extra, AntiDegradeMarkerExtraKey)
	require.NotContains(t, store.updated.Extra, "codex_fingerprint_mode")
}

type presetRegressionGroups struct {
	GroupRepository
	candidates func() ([]int64, error)
}

func (g *presetRegressionGroups) GetAccountCount(context.Context, int64) (int64, int64, error) {
	return 0, 0, nil
}
func (g *presetRegressionGroups) ListActive(context.Context) ([]Group, error) {
	return []Group{{ID: 1, AntiDegradePreset: "legacy"}}, nil
}
func (g *presetRegressionGroups) ListAntiDegradePresetCandidates(context.Context, int64, string) ([]int64, error) {
	return g.candidates()
}

func TestGroupPresetSyncReturnsIndependentSnapshots(t *testing.T) {
	groups := &presetRegressionGroups{candidates: func() ([]int64, error) { return nil, nil }}
	r := NewGroupAntiDegradeReconciler(groups, NewAntiDegradeService(nil))
	result := r.ReconcileGroup(context.Background(), 1, "legacy")
	require.False(t, result.Running)
	result.Applied = 999
	require.Zero(t, r.GroupStatus(1).Applied, "callers must not mutate the published worker state")
}

func TestGroupPresetSyncDoesNotOverlap(t *testing.T) {
	entered, release, done := make(chan struct{}), make(chan struct{}), make(chan struct{})
	var calls atomic.Int32
	groups := &presetRegressionGroups{candidates: func() ([]int64, error) {
		if calls.Add(1) == 1 {
			close(entered)
			<-release
		}
		return nil, nil
	}}
	r := NewGroupAntiDegradeReconciler(groups, NewAntiDegradeService(nil))
	go func() { defer close(done); r.ReconcileGroup(context.Background(), 1, "legacy") }()
	<-entered
	running := r.ReconcileGroup(context.Background(), 1, "legacy")
	close(release)
	<-done
	require.True(t, running.Running)
	require.Equal(t, int32(1), calls.Load())
	require.False(t, r.GroupStatus(1).Running)
}

func TestGroupPresetStartsWithoutWaitingForInterval(t *testing.T) {
	ran := make(chan struct{}, 1)
	groups := &presetRegressionGroups{candidates: func() ([]int64, error) { ran <- struct{}{}; return nil, nil }}
	r := NewGroupAntiDegradeReconciler(groups, NewAntiDegradeService(nil))
	r.SetInterval(time.Hour)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	r.Start(ctx)
	select {
	case <-ran:
	case <-time.After(time.Second):
		t.Fatal("first reconciliation waited for the hourly ticker")
	}
}
