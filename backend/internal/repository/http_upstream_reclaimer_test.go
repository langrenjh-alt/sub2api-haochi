package repository

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
)

type reclaimerTestTransport struct {
	service       *httpUpstreamService
	roundTrips    atomic.Int64
	closes        atomic.Int64
	closedOutside atomic.Bool
}

func (t *reclaimerTestTransport) RoundTrip(*http.Request) (*http.Response, error) {
	t.roundTrips.Add(1)
	return nil, errors.New("unexpected RoundTrip")
}

func (t *reclaimerTestTransport) CloseIdleConnections() {
	t.closes.Add(1)
	if t.service == nil {
		t.closedOutside.Store(true)
		return
	}
	if t.service.mu.TryLock() {
		t.closedOutside.Store(true)
		t.service.mu.Unlock()
	}
}

type reclaimerTestPressureSource struct {
	mu     sync.RWMutex
	sample httpUpstreamPressureSample
	calls  atomic.Int64
}

func (s *reclaimerTestPressureSource) Sample(context.Context) httpUpstreamPressureSample {
	s.calls.Add(1)
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.sample
}

func (s *reclaimerTestPressureSource) set(sample httpUpstreamPressureSample) {
	s.mu.Lock()
	s.sample = sample
	s.mu.Unlock()
}

func newReclaimerTestService(ttl time.Duration) *httpUpstreamService {
	return &httpUpstreamService{
		cfg: &config.Config{Gateway: config.GatewayConfig{
			ClientIdleTTLSeconds: int(ttl / time.Second),
		}},
		clients: make(map[string]*upstreamClientEntry),
	}
}

func addReclaimerTestEntry(s *httpUpstreamService, key string, lastUsed time.Time, inFlight int64) (*upstreamClientEntry, *reclaimerTestTransport) {
	transport := &reclaimerTestTransport{service: s}
	entry := &upstreamClientEntry{client: &http.Client{Transport: transport}}
	atomic.StoreInt64(&entry.lastUsed, lastUsed.UnixNano())
	atomic.StoreInt64(&entry.inFlight, inFlight)
	s.clients[key] = entry
	return entry, transport
}

func TestHTTPUpstreamPressureClassifierHysteresis(t *testing.T) {
	opts := defaultHTTPUpstreamReclaimerOptions()
	classifier := httpUpstreamPressureClassifier{options: opts}

	require.Equal(t, httpUpstreamPressureHigh, classifier.update(httpUpstreamPressureSample{ratio: 0.86, valid: true}))
	require.Equal(t, httpUpstreamPressureHigh, classifier.update(httpUpstreamPressureSample{ratio: 0.82, valid: true}), "high pressure should remain latched above its exit threshold")
	require.Equal(t, httpUpstreamPressureNormal, classifier.update(httpUpstreamPressureSample{ratio: 0.71, valid: true}))
	require.Equal(t, httpUpstreamPressureCritical, classifier.update(httpUpstreamPressureSample{ratio: 0.95, valid: true}))
	require.Equal(t, httpUpstreamPressureHigh, classifier.update(httpUpstreamPressureSample{ratio: 0.71, valid: true}), "critical recovery must pass through high")
	require.Equal(t, httpUpstreamPressureNormal, classifier.update(httpUpstreamPressureSample{ratio: 0.71, valid: true}))
}

func TestHTTPUpstreamReclaimerOptionsFromConfig(t *testing.T) {
	_, enabled := httpUpstreamReclaimerOptionsFromConfig(&config.Config{})
	require.False(t, enabled)

	cfg := &config.Config{Gateway: config.GatewayConfig{
		DynamicReadyPool: config.GatewayDynamicReadyPoolConfig{
			Enabled:               true,
			SampleIntervalSeconds: 7,
			RecentWindowSeconds:   180,
			ReserveRatio:          0.75,
			MinReserve:            96,
			HighReserveRatio:      0.20,
			HighMinReserve:        24,
			HighWatermark:         0.86,
			HighExitWatermark:     0.74,
			CriticalWatermark:     0.94,
			CriticalExitWatermark: 0.88,
		},
	}}
	opts, enabled := httpUpstreamReclaimerOptionsFromConfig(cfg)
	require.True(t, enabled)
	require.Equal(t, 7*time.Second, opts.interval)
	require.Equal(t, 180*time.Second, opts.recentWindow)
	require.Equal(t, 0.75, opts.normalSpareRatio)
	require.Equal(t, 96, opts.normalSpareMinimum)
	require.Equal(t, 0.20, opts.highSpareRatio)
	require.Equal(t, 24, opts.highSpareMinimum)
	require.Equal(t, 0.86, opts.highEnterRatio)
	require.Equal(t, 0.74, opts.highExitRatio)
	require.Equal(t, 0.94, opts.criticalEnterRatio)
	require.Equal(t, 0.88, opts.criticalExitRatio)
}

func TestNewHTTPUpstreamCreatesReclaimerOnlyWhenEnabledAndStartsLazily(t *testing.T) {
	disabled := NewHTTPUpstream(&config.Config{}).(*httpUpstreamService)
	require.Nil(t, disabled.reclaimer)
	disabled.Stop()

	cfg := &config.Config{Gateway: config.GatewayConfig{
		DynamicReadyPool: config.GatewayDynamicReadyPoolConfig{
			Enabled:               true,
			SampleIntervalSeconds: 60,
			RecentWindowSeconds:   120,
			ReserveRatio:          0.50,
			MinReserve:            64,
			HighReserveRatio:      0.15,
			HighMinReserve:        16,
			HighWatermark:         0.85,
			HighExitWatermark:     0.72,
			CriticalWatermark:     0.92,
			CriticalExitWatermark: 0.84,
		},
	}}
	enabled := NewHTTPUpstream(cfg).(*httpUpstreamService)
	require.NotNil(t, enabled.reclaimer)
	require.False(t, enabled.reclaimer.started, "construction must not leak a worker when application wiring later fails")
	enabled.startReclaimer()
	require.True(t, enabled.reclaimer.started)
	enabled.Stop()
	enabled.Stop()
}

func TestHTTPUpstreamReclaimerNormalEvictsOnlyOutsideRecentTarget(t *testing.T) {
	now := time.Date(2026, 7, 18, 12, 0, 0, 0, time.UTC)
	svc := newReclaimerTestService(time.Minute)
	_, expiredTransport := addReclaimerTestEntry(svc, "expired", now.Add(-6*time.Minute), 0)
	recentEntry, recentTransport := addReclaimerTestEntry(svc, "recent", now.Add(-10*time.Second), 0)
	activeEntry, activeTransport := addReclaimerTestEntry(svc, "active", now.Add(-6*time.Minute), 1)

	opts := defaultHTTPUpstreamReclaimerOptions()
	opts.now = func() time.Time { return now }
	opts.normalSpareMinimum = 0
	opts.normalSpareRatio = 0
	r := newHTTPUpstreamReclaimer(svc, httpUpstreamPressureSourceFunc(func(context.Context) httpUpstreamPressureSample {
		return httpUpstreamPressureSample{ratio: 0.2, valid: true}
	}), opts)
	t.Cleanup(r.Stop)

	require.Equal(t, 1, r.reclaimOnce(t.Context()))
	require.NotContains(t, svc.clients, "expired")
	require.Same(t, recentEntry, svc.clients["recent"])
	require.Same(t, activeEntry, svc.clients["active"])
	require.Equal(t, int64(1), expiredTransport.closes.Load())
	require.True(t, expiredTransport.closedOutside.Load(), "CloseIdleConnections must run without the client-map lock")
	require.Zero(t, recentTransport.closes.Load())
	require.Zero(t, activeTransport.closes.Load())
	require.Zero(t, expiredTransport.roundTrips.Load(), "reclamation must never issue an application request")
}

func TestHTTPUpstreamReclaimerHighPressureShrinksToActiveSpareTarget(t *testing.T) {
	now := time.Date(2026, 7, 18, 12, 0, 0, 0, time.UTC)
	svc := newReclaimerTestService(time.Hour)
	for i := range 10 {
		addReclaimerTestEntry(svc, fmt.Sprintf("recent-%03d", i), now.Add(-time.Duration(i)*time.Second), 0)
	}
	for i := range 100 {
		addReclaimerTestEntry(svc, fmt.Sprintf("stale-%03d", i), now.Add(time.Duration(i-200)*time.Minute), 0)
	}

	opts := defaultHTTPUpstreamReclaimerOptions()
	opts.now = func() time.Time { return now }
	opts.highSpareMinimum = 16
	opts.highSpareRatio = 0.15
	r := newHTTPUpstreamReclaimer(svc, httpUpstreamPressureSourceFunc(func(context.Context) httpUpstreamPressureSample {
		return httpUpstreamPressureSample{ratio: 0.86, valid: true}
	}), opts)
	t.Cleanup(r.Stop)

	// There are no in-flight clients, so high pressure retains only the
	// minimum 16-client spare, including recently used idle entries.
	require.Equal(t, 94, r.reclaimOnce(t.Context()))
	require.Len(t, svc.clients, 16)
	require.NotContains(t, svc.clients, "stale-000")
	require.Contains(t, svc.clients, "recent-000")
}

func TestHTTPUpstreamReclaimerNormalUsesRecentDemandAndDynamicSpare(t *testing.T) {
	now := time.Date(2026, 7, 18, 12, 0, 0, 0, time.UTC)
	svc := newReclaimerTestService(time.Hour)
	for i := range 10 {
		addReclaimerTestEntry(svc, fmt.Sprintf("recent-%03d", i), now.Add(-time.Duration(i)*time.Second), 0)
	}
	for i := range 100 {
		addReclaimerTestEntry(svc, fmt.Sprintf("stale-%03d", i), now.Add(-time.Duration(i+121)*time.Second), 0)
	}

	opts := defaultHTTPUpstreamReclaimerOptions()
	opts.now = func() time.Time { return now }
	opts.recentWindow = 120 * time.Second
	opts.normalSpareMinimum = 64
	opts.normalSpareRatio = 0.50
	r := newHTTPUpstreamReclaimer(svc, httpUpstreamPressureSourceFunc(func(context.Context) httpUpstreamPressureSample {
		return httpUpstreamPressureSample{ratio: 0.2, valid: true}
	}), opts)
	t.Cleanup(r.Stop)

	// Target = 10 recent + max(64, ceil(10*0.5)) = 74.
	require.Equal(t, 36, r.reclaimOnce(t.Context()))
	require.Len(t, svc.clients, 74)
	for i := range 10 {
		require.Contains(t, svc.clients, fmt.Sprintf("recent-%03d", i))
	}
}

func TestHTTPUpstreamLegacyTTLEvictionDefersToDynamicReserve(t *testing.T) {
	now := time.Now()
	svc := newReclaimerTestService(time.Second)
	svc.cfg.Gateway.DynamicReadyPool.Enabled = true
	entry, _ := addReclaimerTestEntry(svc, "warm-floor", now.Add(-time.Hour), 0)

	svc.mu.Lock()
	svc.evictIdleLocked(now)
	svc.mu.Unlock()

	require.Same(t, entry, svc.clients["warm-floor"])
}

func TestHTTPUpstreamReclaimerCriticalRechecksCandidatesUnderWriteLock(t *testing.T) {
	now := time.Date(2026, 7, 18, 12, 0, 0, 0, time.UTC)
	svc := newReclaimerTestService(time.Hour)
	activeEntry, activeTransport := addReclaimerTestEntry(svc, "became-active", now.Add(-3*time.Minute), 0)
	refreshedEntry, refreshedTransport := addReclaimerTestEntry(svc, "refreshed", now.Add(-4*time.Minute), 0)
	_, evictedTransport := addReclaimerTestEntry(svc, "evicted", now.Add(-5*time.Minute), 0)

	r := newHTTPUpstreamReclaimer(svc, nil, defaultHTTPUpstreamReclaimerOptions())
	t.Cleanup(r.Stop)
	plan := r.buildPlan(now, httpUpstreamPressureCritical)
	atomic.StoreInt64(&activeEntry.inFlight, 1)
	atomic.StoreInt64(&refreshedEntry.lastUsed, now.UnixNano())

	require.Equal(t, 1, r.applyPlan(plan))
	require.Same(t, activeEntry, svc.clients["became-active"])
	require.Same(t, refreshedEntry, svc.clients["refreshed"])
	require.NotContains(t, svc.clients, "evicted")
	require.Zero(t, activeTransport.closes.Load(), "an active client must never be reclaimed")
	require.Zero(t, refreshedTransport.closes.Load(), "a client reused after the snapshot must not be reclaimed")
	require.Equal(t, int64(1), evictedTransport.closes.Load())
}

func TestHTTPUpstreamReclaimerBackgroundStopIsIdempotent(t *testing.T) {
	now := time.Now()
	svc := newReclaimerTestService(time.Hour)
	_, transport := addReclaimerTestEntry(svc, "idle", now.Add(-3*time.Minute), 0)
	source := &reclaimerTestPressureSource{sample: httpUpstreamPressureSample{ratio: 0.99, valid: true}}
	opts := defaultHTTPUpstreamReclaimerOptions()
	opts.interval = 2 * time.Millisecond
	r := newHTTPUpstreamReclaimer(svc, source, opts)
	r.Start()

	require.Eventually(t, func() bool {
		svc.mu.RLock()
		defer svc.mu.RUnlock()
		return len(svc.clients) == 0
	}, time.Second, time.Millisecond)
	r.Stop()
	r.Stop()
	callsAfterStop := source.calls.Load()
	time.Sleep(3 * opts.interval)
	require.Equal(t, callsAfterStop, source.calls.Load(), "the worker must not sample after Stop returns")
	require.Zero(t, transport.roundTrips.Load())
	require.Equal(t, int64(1), transport.closes.Load())
}

func TestHTTPUpstreamReclaimerConcurrentStartUsesSingleFastPath(t *testing.T) {
	r := newHTTPUpstreamReclaimer(newReclaimerTestService(time.Hour), nil, httpUpstreamReclaimerOptions{
		interval: time.Hour,
	})
	var wg sync.WaitGroup
	for range 500 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r.Start()
		}()
	}
	wg.Wait()
	r.lifecycleMu.Lock()
	require.True(t, r.started)
	r.lifecycleMu.Unlock()
	r.Stop()
}

func TestHTTPUpstreamReclaimerConcurrentAcquireNeverEvictsInFlight(t *testing.T) {
	svc := newReclaimerTestService(time.Hour)
	r := newHTTPUpstreamReclaimer(svc, httpUpstreamPressureSourceFunc(func(context.Context) httpUpstreamPressureSample {
		return httpUpstreamPressureSample{ratio: 1, valid: true}
	}), defaultHTTPUpstreamReclaimerOptions())
	t.Cleanup(r.Stop)

	var wg sync.WaitGroup
	for worker := range 8 {
		wg.Add(1)
		go func(accountID int64) {
			defer wg.Done()
			for range 100 {
				entry, err := svc.acquireClient("", accountID, 3)
				require.NoError(t, err)
				require.Positive(t, atomic.LoadInt64(&entry.inFlight))
				atomic.AddInt64(&entry.inFlight, -1)
				atomic.StoreInt64(&entry.lastUsed, time.Now().UnixNano())
			}
		}(int64(worker + 1))
	}
	for range 100 {
		r.reclaimOnce(t.Context())
	}
	wg.Wait()

	svc.mu.RLock()
	defer svc.mu.RUnlock()
	for _, entry := range svc.clients {
		require.Zero(t, atomic.LoadInt64(&entry.inFlight))
	}
}
