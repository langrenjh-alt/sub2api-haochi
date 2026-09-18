package service

import (
	"context"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestTurnStateRegressionFairnessAcrossPollIntervals(t *testing.T) {
	s, _, now := newShapeTestService(t, CodexTurnStateConfig{})
	s.markProbed(11)
	due := s.dueAccounts([]int64{11, 12, 13}, s.cfg, now.Add(time.Minute), nil)
	if len(due) != 3 || due[0] != 12 || due[2] != 11 {
		t.Fatalf("starvation: due=%v, want [12 13 11]", due)
	}
}

func TestTurnStateRegressionBudgetPreservesPriority(t *testing.T) {
	r := newFakeTurnStateRepo()
	r.cfg = CodexTurnStateConfig{Enabled: true, AccountIDs: []int64{1, 2, 3}, MaxAccountsPerTick: 1, MaxAttemptsPerCycle: 1}
	a := codexOAuthAccount()
	a.Schedulable = true
	s := NewCodexTurnStateService(r, &staticAccountRepo{account: a}, nil, &captureUpstream{status: 200})
	s.refresh <- 1
	s.refresh <- 2
	s.refresh <- 3
	s.Tick(context.Background())
	if len(s.refresh) != 2 {
		t.Fatalf("remaining priority=%d, want 2", len(s.refresh))
	}
}

func TestTurnStateRegressionRejectsLookalikeHosts(t *testing.T) {
	for _, h := range []string{"evilchatgpt.com", "chatgpt.com:443.evil.test", "chatgpt.com:443@evil.test"} {
		if isCodexTurnStateUpstreamHost(h) {
			t.Errorf("accepted lookalike %q", h)
		}
	}
}

func TestTurnStateRegressionErrorStateCannotEnterPool(t *testing.T) {
	s, r, now := newShapeTestService(t, CodexTurnStateConfig{})
	for _, code := range []int{301, 401, 429, 500} {
		s.persist(context.Background(), codexTurnStateEvent{model: CodexTurnStateDefaultModel, accountID: 7, state: synthTurnState(12, now), statusCode: code})
	}
	if len(r.snapshot()) != 0 {
		t.Fatalf("error responses entered pool: %d", len(r.snapshot()))
	}
}

func TestTurnStateRegressionExpiredCannotReplaceLive(t *testing.T) {
	s, _, now := newShapeTestService(t, CodexTurnStateConfig{})
	good := synthTurnState(12, now)
	s.persist(context.Background(), codexTurnStateEvent{model: CodexTurnStateDefaultModel, accountID: 7, state: good, statusCode: 200})
	s.persist(context.Background(), codexTurnStateEvent{model: CodexTurnStateDefaultModel, accountID: 7, state: synthTurnState(12, now.Add(-2*time.Hour)), statusCode: 200})
	_, token, ok := s.InjectionHeader(7, s.cfg.Model)
	if !ok || token != good {
		t.Fatal("expired observation replaced valid cached state")
	}
}

func TestTurnStateRegressionColdProbeStripsOverride(t *testing.T) {
	a := codexOAuthAccount()
	a.Credentials["header_overrides"] = map[string]any{"x-codex-turn-state": "stale-route"}
	u := &captureUpstream{status: 200}
	s := probeService(u, a, CodexTurnStateConfig{})
	s.probeCodexState(context.Background(), a, s.cfg, "")
	if got := u.lastRequest.Header.Get(CodexTurnStateDefaultHeader); got != "" {
		t.Fatalf("cold probe carried old state: %q", got)
	}
}

type canceledTurnStateUpstream struct {
	captureUpstream
	canceled bool
}

func (u *canceledTurnStateUpstream) Do(r *http.Request, proxy string, id int64, concurrency int) (*http.Response, error) {
	u.canceled = r.Context().Err() != nil
	return u.captureUpstream.Do(r, proxy, id, concurrency)
}

func TestTurnStateRegressionProbeHonorsCancellation(t *testing.T) {
	a := codexOAuthAccount()
	u := &canceledTurnStateUpstream{captureUpstream: captureUpstream{status: 200}}
	s := probeService(u, a, CodexTurnStateConfig{})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	s.probeCodexState(ctx, a, s.cfg, "")
	if !u.canceled {
		t.Fatal("probe detached from canceled keeper context")
	}
}

func TestTurnStateRegressionRejectsProxyOnlyEnrollment(t *testing.T) {
	if ValidateCodexTurnStateConfig(CodexTurnStateConfig{Enabled: true, ProxyID: 1}) == nil {
		t.Fatal("proxy alone incorrectly counts as enrolled account")
	}
}

func TestTurnStateRegressionDegradedCacheMustRefreshWhenDisabled(t *testing.T) {
	s, _, now := newShapeTestService(t, CodexTurnStateConfig{})
	s.storeCache(7, cachedTurnState{model: CodexTurnStateDefaultModel, state: synthTurnState(13, now), expiresAt: now.Add(time.Hour)})
	if !s.needsRefresh(7, s.cfg, now) {
		t.Fatal("disallowed degraded cache suppressed renewal")
	}
}

func TestTurnStateRegressionDisabledWriterCannotRepopulate(t *testing.T) {
	s, r, now := newShapeTestService(t, CodexTurnStateConfig{})
	s.cfg.Enabled = false
	s.persist(context.Background(), codexTurnStateEvent{model: CodexTurnStateDefaultModel, accountID: 7, state: synthTurnState(12, now), statusCode: 200})
	if len(r.snapshot()) != 0 {
		t.Fatal("queued event repopulated disabled pool")
	}
}

func TestTurnStateRegressionQueuedObservationCannotUndoRevoke(t *testing.T) {
	s, _, now := newShapeTestService(t, CodexTurnStateConfig{})
	s.enqueue(codexTurnStateEvent{model: CodexTurnStateDefaultModel, accountID: 7, state: synthTurnState(12, now), statusCode: 200})
	s.Revoke(7, 401, "revoked")
	s.persist(context.Background(), <-s.events)
	if _, _, ok := s.InjectionHeader(7, s.cfg.Model); ok {
		t.Fatal("observation queued before revocation resurrected the state")
	}
}

type notifyTurnStateUpstream struct {
	captureUpstream
	called chan struct{}
}

func (u *notifyTurnStateUpstream) Do(r *http.Request, p string, id int64, n int) (*http.Response, error) {
	select {
	case u.called <- struct{}{}:
	default:
	}
	return u.captureUpstream.Do(r, p, id, n)
}

func TestTurnStateRegressionCollectWakesKeeper(t *testing.T) {
	r := newFakeTurnStateRepo()
	r.cfg = CodexTurnStateConfig{Enabled: true, AccountIDs: []int64{4242}, PollSeconds: 3600}
	u := &notifyTurnStateUpstream{captureUpstream: captureUpstream{status: 200}, called: make(chan struct{}, 1)}
	s := NewCodexTurnStateService(r, &staticAccountRepo{account: codexOAuthAccount()}, nil, u)
	s.Start()
	defer s.Stop()
	if n, err := s.CollectNow(context.Background(), 4242); n != 1 || err != nil {
		t.Fatalf("collect: %d %v", n, err)
	}
	select {
	case <-u.called:
	case <-time.After(time.Second):
		t.Fatal("manual collect did not wake the sleeping keeper")
	}
}

func TestTurnStateRegressionHarvestRespectsEnrollment(t *testing.T) {
	r := newFakeTurnStateRepo()
	r.cfg = CodexTurnStateConfig{Enabled: true, AccountIDs: []int64{7}, PollSeconds: 3600}
	s := NewCodexTurnStateService(r, nil, nil, nil)
	s.Start()
	defer s.Stop()
	s.ObserveResponse(999, http.Header{"X-Codex-Turn-State": []string{"foreign"}}, 200, "gpt-6-astra")
	s.persist(context.Background(), codexTurnStateEvent{model: CodexTurnStateDefaultModel, accountID: 999, state: "foreign", statusCode: 200})
	if len(r.snapshot()) != 0 {
		t.Fatal("unenrolled traffic polluted the state pool")
	}
}

func TestTurnStateRegressionMissingProxyFailsClosed(t *testing.T) {
	r := newFakeTurnStateRepo()
	s := NewCodexTurnStateService(r, &staticAccountRepo{account: codexOAuthAccount()}, &stubProxyRepo{}, &captureUpstream{status: 200})
	cfg := NormalizeCodexTurnStateConfig(CodexTurnStateConfig{ProxyID: 99})
	s.probeOne(context.Background(), 4242, cfg)
	if s.upstream.(*captureUpstream).lastRequest != nil {
		t.Fatal("configured proxy missing but collector silently used direct egress")
	}
}

type countedTurnStateRepo struct {
	*fakeTurnStateRepo
	loads atomic.Int64
}

func (r *countedTurnStateRepo) Config(ctx context.Context) (CodexTurnStateConfig, error) {
	r.loads.Add(1)
	return r.fakeTurnStateRepo.Config(ctx)
}

func TestTurnStateRegressionInjectionNeverQueriesDatabase(t *testing.T) {
	r := &countedTurnStateRepo{fakeTurnStateRepo: newFakeTurnStateRepo()}
	s := newTestTurnStateService(r, CodexTurnStateConfig{Enabled: true, InjectEnabled: true})
	s.cfgAt = time.Now().Add(-time.Hour)
	s.InjectionHeader(7, "gpt-6-astra")
	if r.loads.Load() != 0 {
		t.Fatal("request hot path queried SQL after config TTL elapsed")
	}
}

func TestTurnStateRegressionFailureKeepsHTTPStatus(t *testing.T) {
	s, r, _ := newShapeTestService(t, CodexTurnStateConfig{})
	s.recordProbe(7, s.cfg, 2, codexTurnStateProbeResult{HTTPStatus: 429, Err: "rate limited"})
	if r.snapshot()[0].HTTPStatus != 429 {
		t.Fatal("failure history discarded upstream HTTP status")
	}
}

func TestTurnStateRegressionDeletedProxyCanDisable(t *testing.T) {
	r := newFakeTurnStateRepo()
	s := NewCodexTurnStateService(r, nil, &stubProxyRepo{}, nil)
	cfg := CodexTurnStateConfig{Enabled: false, ProxyID: 2, AccountIDs: []int64{7}}
	saved, err := s.UpdateConfig(context.Background(), 1, cfg)
	if err != nil || saved.Enabled || saved.ProxyID != 2 {
		t.Fatalf("disable with deleted proxy: saved=%+v err=%v", saved, err)
	}
	cfg.Enabled = true
	_, err = s.UpdateConfig(context.Background(), 1, cfg)
	if err == nil || !strings.Contains(err.Error(), "#2") {
		t.Fatalf("enabled stale proxy must identify the reference: %v", err)
	}
	current, _ := r.Config(context.Background())
	if current.Enabled {
		t.Fatal("failed validation partially saved enabled config")
	}
}
