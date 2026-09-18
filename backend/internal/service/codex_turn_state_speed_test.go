package service

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"
)

type speedAccountRepo struct{ AccountRepository }

func (r *speedAccountRepo) GetByID(_ context.Context, id int64) (*Account, error) {
	a := codexOAuthAccount()
	a.ID = id
	a.Schedulable = true
	return a, nil
}

type speedUpstream struct {
	HTTPUpstream
	mu           sync.Mutex
	calls        map[int64]int
	active, peak int
	overlap      bool
	perAccount   map[int64]int
	hitAt        int
	status       int
	retryAfter   string
	delay        time.Duration
}

func (u *speedUpstream) Do(r *http.Request, _ string, id int64, _ int) (*http.Response, error) {
	u.mu.Lock()
	if u.calls == nil {
		u.calls = map[int64]int{}
		u.perAccount = map[int64]int{}
	}
	u.calls[id]++
	n := u.calls[id]
	u.active++
	u.perAccount[id]++
	if u.perAccount[id] > 1 {
		u.overlap = true
	}
	if u.active > u.peak {
		u.peak = u.active
	}
	u.mu.Unlock()
	defer func() { u.mu.Lock(); u.active--; u.perAccount[id]--; u.mu.Unlock() }()
	select {
	case <-time.After(u.delay):
	case <-r.Context().Done():
		return nil, r.Context().Err()
	}
	blocks := 13
	if u.hitAt > 0 && n >= u.hitAt {
		blocks = 12
	}
	status := u.status
	if status == 0 {
		status = 200
	}
	h := http.Header{}
	h.Set(CodexTurnStateDefaultHeader, synthTurnState(blocks, time.Now()))
	if u.retryAfter != "" {
		h.Set("Retry-After", u.retryAfter)
	}
	return &http.Response{StatusCode: status, Header: h, Body: io.NopCloser(strings.NewReader(""))}, nil
}

func speedService(t *testing.T, u *speedUpstream) *CodexTurnStateService {
	t.Helper()
	r := newFakeTurnStateRepo()
	// JSON also permits running these regressions against the old binary's
	// configuration schema: unrecognised scheduler fields were silently ignored.
	if err := json.Unmarshal([]byte(`{"enabled":true,"inject_enabled":true,"account_ids":[1,2,3,4],"poll_seconds":3600,"max_accounts_per_tick":4,"max_attempts_per_cycle":3,"concurrent_accounts":2,"retry_interval_ms":1}`), &r.cfg); err != nil {
		t.Fatal(err)
	}
	return NewCodexTurnStateService(r, &speedAccountRepo{}, nil, u)
}

func TestTurnStateSpeedRetriesWithoutWaitingForPoll(t *testing.T) {
	u := &speedUpstream{hitAt: 3, delay: 5 * time.Millisecond}
	s := speedService(t, u)
	if _, err := s.CollectNow(context.Background(), 1); err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	s.tick(context.Background(), true)
	u.mu.Lock()
	n, others := u.calls[1], len(u.calls)
	u.mu.Unlock()
	if n != 3 || others != 1 {
		t.Fatalf("calls=%d accounts=%d; want 3 attempts on only account 1 without another poll", n, others)
	}
	if time.Since(start) > time.Second {
		t.Fatal("retry waited for polling interval")
	}
	if _, _, ok := s.InjectionHeader(1, s.cfg.Model); !ok {
		t.Fatal("hit was not persisted before cycle completion")
	}
	t.Logf("FAST_SINGLE_ACCOUNT calls=%d other_accounts=%d elapsed=%s", n, others-1, time.Since(start))
}

func TestTurnStateSpeedAccountsRunConcurrently(t *testing.T) {
	u := &speedUpstream{hitAt: 1, delay: 40 * time.Millisecond}
	s := speedService(t, u)
	s.Tick(context.Background())
	u.mu.Lock()
	defer u.mu.Unlock()
	if u.peak != 2 || u.overlap {
		t.Fatalf("peak=%d same_account_overlap=%v; want bounded concurrency 2", u.peak, u.overlap)
	}
	t.Logf("PEAK_CONCURRENCY=%d SAME_ACCOUNT_OVERLAP=%v", u.peak, u.overlap)
}

func awaitSpeed(t *testing.T, predicate func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if predicate() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("scheduler did not complete within 3 seconds")
}

func TestTurnStateSpeedRefillsSlotsWithoutAnotherPoll(t *testing.T) {
	u := &speedUpstream{hitAt: 3, delay: 20 * time.Millisecond}
	s := speedService(t, u)
	s.Start()
	defer s.Stop()
	if n, err := s.CollectNow(context.Background(), 0); err != nil || n != 4 {
		t.Fatalf("queue=%d err=%v", n, err)
	}
	awaitSpeed(t, func() bool {
		for _, id := range []int64{1, 2, 3, 4} {
			if _, _, ok := s.InjectionHeader(id, s.cfg.Model); !ok {
				return false
			}
		}
		return true
	})
	u.mu.Lock()
	defer u.mu.Unlock()
	if len(u.calls) != 4 || u.peak != 2 || u.overlap {
		t.Fatalf("calls=%v peak=%d overlap=%v", u.calls, u.peak, u.overlap)
	}
	for id, n := range u.calls {
		if n != 3 {
			t.Fatalf("account %d got %d attempts", id, n)
		}
	}
	t.Log("REFILL=OK ACCOUNTS=4 ATTEMPTS_EACH=3 PEAK=2")
}

func TestTurnStateSpeedDuplicateClicksDoNotResetRunningBudget(t *testing.T) {
	u := &speedUpstream{delay: 40 * time.Millisecond}
	s := speedService(t, u)
	s.Start()
	defer s.Stop()
	if _, err := s.CollectNow(context.Background(), 1); err != nil {
		t.Fatal(err)
	}
	awaitSpeed(t, func() bool { u.mu.Lock(); defer u.mu.Unlock(); return u.calls[1] > 0 })
	for i := 0; i < 10; i++ {
		if n, err := s.CollectNow(context.Background(), 1); n != 0 || err != nil {
			t.Fatalf("duplicate queued=%d err=%v", n, err)
		}
	}
	awaitSpeed(t, func() bool { s.jobsMu.Lock(); defer s.jobsMu.Unlock(); return len(s.inFlight) == 0 })
	u.mu.Lock()
	defer u.mu.Unlock()
	if u.calls[1] != 3 || len(u.calls) != 1 || u.overlap || s.attemptCount(1) != 3 {
		t.Fatalf("calls=%v overlap=%v count=%d", u.calls, u.overlap, s.attemptCount(1))
	}
}

func TestTurnStateSpeedRateLimitRetainsRetryAfter(t *testing.T) {
	u := &speedUpstream{status: 429, retryAfter: "120"}
	s := speedService(t, u)
	s.CollectNow(context.Background(), 1)
	s.tick(context.Background(), true)
	s.jobsMu.Lock()
	deadline := s.retryAfter[1]
	s.jobsMu.Unlock()
	if time.Until(deadline) < 119*time.Second {
		t.Fatalf("Retry-After ignored: %s", deadline)
	}
	s.CollectNow(context.Background(), 1)
	s.tick(context.Background(), true)
	u.mu.Lock()
	defer u.mu.Unlock()
	if u.calls[1] != 1 {
		t.Fatalf("429 retried too soon: %d calls", u.calls[1])
	}
}

func TestTurnStateSpeedStopCancelsInflightRequests(t *testing.T) {
	u := &speedUpstream{delay: time.Minute}
	s := speedService(t, u)
	s.Start()
	s.CollectNow(context.Background(), 1)
	awaitSpeed(t, func() bool { u.mu.Lock(); defer u.mu.Unlock(); return u.calls[1] > 0 })
	start := time.Now()
	s.Stop()
	if time.Since(start) > time.Second {
		t.Fatal("shutdown waited for the upstream timeout")
	}
}

func TestTurnStateSpeedConfigBounds(t *testing.T) {
	for _, cfg := range []CodexTurnStateConfig{
		{ConcurrentAccounts: -1}, {ConcurrentAccounts: 65}, {RetryIntervalMS: -1}, {RetryIntervalMS: 49}, {RetryIntervalMS: 60001},
	} {
		if ValidateCodexTurnStateConfig(cfg) == nil {
			t.Fatalf("accepted invalid scheduler config %+v", cfg)
		}
	}
	cfg := NormalizeCodexTurnStateConfig(CodexTurnStateConfig{})
	if cfg.ConcurrentAccounts != 16 || cfg.RetryIntervalMS != 200 {
		t.Fatalf("unexpected defaults %+v", cfg)
	}
}
