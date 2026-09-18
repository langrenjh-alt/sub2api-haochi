package service

import (
	"context"
	"testing"
	"time"
)

func TestTurnStateLifecyclePinsPersonalAndTeamUntilRenewal(t *testing.T) {
	for _, blocks := range []int{10, 12} {
		s, r, clock := newShapeTestService(t, CodexTurnStateConfig{})
		s.now = func() time.Time { return clock }
		original := synthTurnState(blocks, clock)
		s.persist(context.Background(), codexTurnStateEvent{model: CodexTurnStateDefaultModel, accountID: 7, state: original, statusCode: 200})
		first, _ := s.cachedState(7)
		clock = clock.Add(10 * time.Minute)
		s.persist(context.Background(), codexTurnStateEvent{model: CodexTurnStateDefaultModel, accountID: 7, state: synthTurnState(blocks, clock), statusCode: 200})
		current, _ := s.cachedState(7)
		if current.state != original || current.expiresAt != first.expiresAt || s.needsRefresh(7, s.cfg, clock) {
			t.Fatalf("%d blocks: pin changed before renewal", blocks)
		}
		if r.snapshot()[1].Status != "standby" {
			t.Fatal("replacement candidate could win restart preload")
		}
		clock = first.expiresAt.Add(-4 * time.Minute)
		if !s.needsRefresh(7, s.cfg, clock) {
			t.Fatal("renewal window did not start")
		}
		s.persist(context.Background(), codexTurnStateEvent{model: CodexTurnStateDefaultModel, accountID: 7, state: synthTurnState(blocks+1, clock), statusCode: 200})
		current, _ = s.cachedState(7)
		if current.state != original {
			t.Fatal("renewal miss evicted the still-valid pin")
		}
		s.reserveAttempt(context.Background(), 7, 50)
		replacement := synthTurnState(blocks, clock)
		s.persist(context.Background(), codexTurnStateEvent{model: CodexTurnStateDefaultModel, accountID: 7, state: replacement, statusCode: 200})
		current, _ = s.cachedState(7)
		if current.state != replacement || current.expiresAt != clock.Add(time.Hour) || s.attemptCount(7) != 0 {
			t.Fatal("renewal did not install a fresh one-hour pin/reset cycle")
		}
		clock = current.expiresAt
		if _, _, ok := s.InjectionHeader(7, s.cfg.Model); ok {
			t.Fatal("expired pin was injected")
		}
	}
}

func TestTurnStateLifecycleSameTokenNeverExtendsPin(t *testing.T) {
	s, _, clock := newShapeTestService(t, CodexTurnStateConfig{})
	s.now = func() time.Time { return clock }
	s.persist(context.Background(), codexTurnStateEvent{model: CodexTurnStateDefaultModel, accountID: 7, state: synthTurnState(12, clock), statusCode: 200})
	first, _ := s.cachedState(7)
	clock = clock.Add(20 * time.Minute)
	s.persist(context.Background(), codexTurnStateEvent{model: CodexTurnStateDefaultModel, accountID: 7, state: first.state, statusCode: 200})
	current, _ := s.cachedState(7)
	if !current.expiresAt.Equal(first.expiresAt) {
		t.Fatal("echo extended expiry rather than retaining the original pin")
	}
}

func TestTurnStateLifecycleStopsAt50AndManualResumesOnlyTarget(t *testing.T) {
	r := newFakeTurnStateRepo()
	r.cfg = CodexTurnStateConfig{Enabled: true, AccountIDs: []int64{4242, 4243}, MaxAccountsPerTick: 8, RetryIntervalMS: 1}
	s := NewCodexTurnStateService(r, &staticAccountRepo{account: codexOAuthAccount()}, nil, &captureUpstream{status: 200})
	cfg := s.reloadConfig(context.Background())
	for i := 0; i < 55; i++ {
		s.probeOne(context.Background(), 4242, cfg)
	}
	if s.attemptCount(4242) != 50 || len(r.snapshot()) != 50 {
		t.Fatalf("attempts=%d rows=%d, want exactly 50", s.attemptCount(4242), len(r.snapshot()))
	}
	if due := s.dueAccounts([]int64{4242}, cfg, time.Now().Add(time.Hour), nil); len(due) != 0 {
		t.Fatal("limited account automatically resumed")
	}
	if n, err := s.CollectNow(context.Background(), 4242); err != nil || n != 1 {
		t.Fatalf("manual resume: %d %v", n, err)
	}
	s.tick(context.Background(), true)
	if s.attemptCount(4242) != 50 || s.attemptCount(4243) != 0 || len(r.snapshot()) != 100 {
		t.Fatalf("manual tick collected other accounts: counts=%d/%d rows=%d",
			s.attemptCount(4242), s.attemptCount(4243), len(r.snapshot()))
	}
}

func TestTurnStateLifecycleTargetLengthDoesNotAccept312Or356(t *testing.T) {
	for _, length := range []int{0, 292, 332} {
		cfg := NormalizeCodexTurnStateConfig(CodexTurnStateConfig{TargetStateLength: length})
		for _, blocks := range []int{11, 13, 14} {
			if cfg.AcceptsStateToken(synthTurnState(blocks, time.Now())) {
				t.Fatalf("target=%d accepted degraded %d blocks", length, blocks)
			}
		}
	}
}
