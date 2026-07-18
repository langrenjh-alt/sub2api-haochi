package repository

import (
	"context"
	"math"
	"net/http"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/resourcepressure"
)

const (
	defaultHTTPUpstreamReclaimInterval    = 5 * time.Second
	defaultHTTPUpstreamRecentWindow       = 300 * time.Second
	defaultHTTPUpstreamHighEnterRatio     = 0.85
	defaultHTTPUpstreamHighExitRatio      = 0.72
	defaultHTTPUpstreamCriticalEnterRatio = 0.92
	defaultHTTPUpstreamCriticalExitRatio  = 0.84
	defaultHTTPUpstreamNormalSpareMinimum = 512
	defaultHTTPUpstreamNormalSpareRatio   = 1.00
	defaultHTTPUpstreamHighSpareMinimum   = 128
	defaultHTTPUpstreamHighSpareRatio     = 0.25
)

type httpUpstreamPressureLevel uint8

const (
	httpUpstreamPressureNormal httpUpstreamPressureLevel = iota
	httpUpstreamPressureHigh
	httpUpstreamPressureCritical
)

type httpUpstreamPressureSample struct {
	ratio float64
	valid bool
}

type httpUpstreamPressureSource interface {
	Sample(context.Context) httpUpstreamPressureSample
}

type httpUpstreamPressureSourceFunc func(context.Context) httpUpstreamPressureSample

func (f httpUpstreamPressureSourceFunc) Sample(ctx context.Context) httpUpstreamPressureSample {
	return f(ctx)
}

type resourcePressureSampler interface {
	Sample(context.Context) resourcepressure.Snapshot
}

type httpUpstreamResourcePressureSource struct {
	sampler resourcePressureSampler
}

func (s httpUpstreamResourcePressureSource) Sample(ctx context.Context) httpUpstreamPressureSample {
	if s.sampler == nil {
		return httpUpstreamPressureSample{}
	}
	snapshot := s.sampler.Sample(ctx)
	return httpUpstreamPressureSample{
		ratio: snapshot.Pressure.Value,
		valid: snapshot.Pressure.Valid,
	}
}

type httpUpstreamReclaimerOptions struct {
	interval           time.Duration
	recentWindow       time.Duration
	highEnterRatio     float64
	highExitRatio      float64
	criticalEnterRatio float64
	criticalExitRatio  float64
	normalSpareMinimum int
	normalSpareRatio   float64
	highSpareMinimum   int
	highSpareRatio     float64
	now                func() time.Time
}

func defaultHTTPUpstreamReclaimerOptions() httpUpstreamReclaimerOptions {
	return httpUpstreamReclaimerOptions{
		interval:           defaultHTTPUpstreamReclaimInterval,
		recentWindow:       defaultHTTPUpstreamRecentWindow,
		highEnterRatio:     defaultHTTPUpstreamHighEnterRatio,
		highExitRatio:      defaultHTTPUpstreamHighExitRatio,
		criticalEnterRatio: defaultHTTPUpstreamCriticalEnterRatio,
		criticalExitRatio:  defaultHTTPUpstreamCriticalExitRatio,
		normalSpareMinimum: defaultHTTPUpstreamNormalSpareMinimum,
		normalSpareRatio:   defaultHTTPUpstreamNormalSpareRatio,
		highSpareMinimum:   defaultHTTPUpstreamHighSpareMinimum,
		highSpareRatio:     defaultHTTPUpstreamHighSpareRatio,
		now:                time.Now,
	}
}

func normalizeHTTPUpstreamReclaimerOptions(opts httpUpstreamReclaimerOptions) httpUpstreamReclaimerOptions {
	defaults := defaultHTTPUpstreamReclaimerOptions()
	if opts.interval <= 0 {
		opts.interval = defaults.interval
	}
	if opts.recentWindow <= 0 {
		opts.recentWindow = defaults.recentWindow
	}
	if opts.highEnterRatio <= 0 || opts.highEnterRatio >= 1 {
		opts.highEnterRatio = defaults.highEnterRatio
	}
	if opts.highExitRatio <= 0 || opts.highExitRatio >= opts.highEnterRatio {
		opts.highExitRatio = defaults.highExitRatio
	}
	if opts.criticalEnterRatio <= opts.highEnterRatio || opts.criticalEnterRatio > 1 {
		opts.criticalEnterRatio = defaults.criticalEnterRatio
	}
	if opts.criticalExitRatio <= opts.highExitRatio || opts.criticalExitRatio >= opts.criticalEnterRatio {
		opts.criticalExitRatio = defaults.criticalExitRatio
	}
	if opts.normalSpareMinimum < 0 {
		opts.normalSpareMinimum = defaults.normalSpareMinimum
	}
	if opts.normalSpareRatio < 0 || opts.normalSpareRatio > 5 {
		opts.normalSpareRatio = defaults.normalSpareRatio
	}
	if opts.highSpareMinimum < 0 {
		opts.highSpareMinimum = defaults.highSpareMinimum
	}
	if opts.highSpareRatio < 0 || opts.highSpareRatio > 5 {
		opts.highSpareRatio = defaults.highSpareRatio
	}
	if opts.now == nil {
		opts.now = defaults.now
	}
	return opts
}

type httpUpstreamPressureClassifier struct {
	mu      sync.Mutex
	level   httpUpstreamPressureLevel
	options httpUpstreamReclaimerOptions
}

func (c *httpUpstreamPressureClassifier) update(sample httpUpstreamPressureSample) httpUpstreamPressureLevel {
	c.mu.Lock()
	defer c.mu.Unlock()

	if !sample.valid || math.IsNaN(sample.ratio) || math.IsInf(sample.ratio, 0) {
		return c.level
	}
	ratio := min(max(sample.ratio, 0), 1)
	switch c.level {
	case httpUpstreamPressureCritical:
		if ratio < c.options.criticalExitRatio {
			// Recover through high first so a transient sample cannot jump from
			// critical straight back to normal.
			c.level = httpUpstreamPressureHigh
		}
	case httpUpstreamPressureHigh:
		if ratio >= c.options.criticalEnterRatio {
			c.level = httpUpstreamPressureCritical
		} else if ratio < c.options.highExitRatio {
			c.level = httpUpstreamPressureNormal
		}
	default:
		if ratio >= c.options.criticalEnterRatio {
			c.level = httpUpstreamPressureCritical
		} else if ratio >= c.options.highEnterRatio {
			c.level = httpUpstreamPressureHigh
		}
	}
	return c.level
}

type httpUpstreamReclaimCandidate struct {
	key      string
	entry    *upstreamClientEntry
	lastUsed int64
}

type httpUpstreamReclaimPlan struct {
	level        httpUpstreamPressureLevel
	recentCutoff int64
	candidates   []httpUpstreamReclaimCandidate
}

type httpUpstreamReclaimer struct {
	target     *httpUpstreamService
	source     httpUpstreamPressureSource
	options    httpUpstreamReclaimerOptions
	classifier httpUpstreamPressureClassifier

	ctx    context.Context
	cancel context.CancelFunc
	done   chan struct{}

	lifecycleMu sync.Mutex
	startOnce   sync.Once
	started     bool
	stopped     bool
}

func newHTTPUpstreamReclaimer(target *httpUpstreamService, source httpUpstreamPressureSource, opts httpUpstreamReclaimerOptions) *httpUpstreamReclaimer {
	opts = normalizeHTTPUpstreamReclaimerOptions(opts)
	ctx, cancel := context.WithCancel(context.Background())
	r := &httpUpstreamReclaimer{
		target:  target,
		source:  source,
		options: opts,
		ctx:     ctx,
		cancel:  cancel,
		done:    make(chan struct{}),
	}
	r.classifier.options = opts
	return r
}

func newConfiguredHTTPUpstreamReclaimer(target *httpUpstreamService, cfg *config.Config) *httpUpstreamReclaimer {
	opts, enabled := httpUpstreamReclaimerOptionsFromConfig(cfg)
	if !enabled {
		return nil
	}
	return newHTTPUpstreamReclaimer(target, httpUpstreamResourcePressureSource{
		sampler: resourcepressure.NewSampler(),
	}, opts)
}

func httpUpstreamReclaimerOptionsFromConfig(cfg *config.Config) (httpUpstreamReclaimerOptions, bool) {
	if cfg == nil || !cfg.Gateway.DynamicReadyPool.Enabled {
		return httpUpstreamReclaimerOptions{}, false
	}
	ready := cfg.Gateway.DynamicReadyPool
	opts := defaultHTTPUpstreamReclaimerOptions()
	if ready.SampleIntervalSeconds > 0 {
		opts.interval = time.Duration(ready.SampleIntervalSeconds) * time.Second
	}
	if ready.RecentWindowSeconds > 0 {
		opts.recentWindow = time.Duration(ready.RecentWindowSeconds) * time.Second
	}
	// Zero is a valid way to disable reserves under a custom policy.
	opts.normalSpareRatio = ready.ReserveRatio
	opts.normalSpareMinimum = ready.MinReserve
	opts.highSpareRatio = ready.HighReserveRatio
	opts.highSpareMinimum = ready.HighMinReserve
	if ready.HighWatermark > 0 {
		opts.highEnterRatio = ready.HighWatermark
	}
	if ready.HighExitWatermark > 0 {
		opts.highExitRatio = ready.HighExitWatermark
	}
	if ready.CriticalWatermark > 0 {
		opts.criticalEnterRatio = ready.CriticalWatermark
	}
	if ready.CriticalExitWatermark > 0 {
		opts.criticalExitRatio = ready.CriticalExitWatermark
	}
	return normalizeHTTPUpstreamReclaimerOptions(opts), true
}

func (r *httpUpstreamReclaimer) Start() {
	if r == nil {
		return
	}
	r.startOnce.Do(func() {
		r.lifecycleMu.Lock()
		if r.stopped {
			r.lifecycleMu.Unlock()
			return
		}
		r.started = true
		r.lifecycleMu.Unlock()
		go r.run()
	})
}

func (r *httpUpstreamReclaimer) Stop() {
	if r == nil {
		return
	}
	r.lifecycleMu.Lock()
	if !r.stopped {
		r.stopped = true
		if r.started {
			r.cancel()
		} else {
			close(r.done)
		}
	}
	done := r.done
	r.lifecycleMu.Unlock()
	<-done
}

func (r *httpUpstreamReclaimer) run() {
	defer close(r.done)
	ticker := time.NewTicker(r.options.interval)
	defer ticker.Stop()
	for {
		select {
		case <-r.ctx.Done():
			return
		case <-ticker.C:
			r.reclaimOnce(r.ctx)
		}
	}
}

func (r *httpUpstreamReclaimer) reclaimOnce(ctx context.Context) int {
	if r == nil || r.target == nil {
		return 0
	}
	sample := httpUpstreamPressureSample{}
	if r.source != nil {
		sample = r.source.Sample(ctx)
	}
	level := r.classifier.update(sample)
	plan := r.buildPlan(r.options.now(), level)
	return r.applyPlan(plan)
}

func (r *httpUpstreamReclaimer) buildPlan(now time.Time, level httpUpstreamPressureLevel) httpUpstreamReclaimPlan {
	plan := httpUpstreamReclaimPlan{level: level}
	if r == nil || r.target == nil {
		return plan
	}
	plan.recentCutoff = now.Add(-r.options.recentWindow).UnixNano()

	// Keep the map lock only while copying stable entry identities. Sorting a
	// large account pool must not block request-path cache lookups.
	r.target.mu.RLock()
	plan.candidates = make([]httpUpstreamReclaimCandidate, 0, len(r.target.clients))
	total := len(r.target.clients)
	recent := 0
	active := 0
	for key, entry := range r.target.clients {
		if entry == nil {
			continue
		}
		lastUsed := atomic.LoadInt64(&entry.lastUsed)
		inFlight := atomic.LoadInt64(&entry.inFlight)
		if inFlight != 0 {
			active++
			recent++
			continue
		}
		if lastUsed >= plan.recentCutoff {
			recent++
			if level == httpUpstreamPressureNormal {
				continue
			}
		}
		plan.candidates = append(plan.candidates, httpUpstreamReclaimCandidate{
			key:      key,
			entry:    entry,
			lastUsed: lastUsed,
		})
	}
	r.target.mu.RUnlock()

	sort.Slice(plan.candidates, func(i, j int) bool {
		if plan.candidates[i].lastUsed == plan.candidates[j].lastUsed {
			return plan.candidates[i].key < plan.candidates[j].key
		}
		return plan.candidates[i].lastUsed < plan.candidates[j].lastUsed
	})

	limit := 0
	switch level {
	case httpUpstreamPressureNormal:
		spare := max(r.options.normalSpareMinimum, int(math.Ceil(float64(recent)*r.options.normalSpareRatio)))
		dynamicLimit := max(0, total-(recent+spare))
		limit = min(dynamicLimit, len(plan.candidates))
	case httpUpstreamPressureHigh:
		spare := max(r.options.highSpareMinimum, int(math.Ceil(float64(active)*r.options.highSpareRatio)))
		limit = min(max(0, total-(active+spare)), len(plan.candidates))
	case httpUpstreamPressureCritical:
		limit = len(plan.candidates)
	}
	plan.candidates = plan.candidates[:limit]
	return plan
}

func (r *httpUpstreamReclaimer) applyPlan(plan httpUpstreamReclaimPlan) int {
	if r == nil || r.target == nil || len(plan.candidates) == 0 {
		return 0
	}
	clientsToClose := make([]*http.Client, 0, len(plan.candidates))
	evicted := 0
	r.target.mu.Lock()
	for _, candidate := range plan.candidates {
		entry, ok := r.target.clients[candidate.key]
		if !ok || entry != candidate.entry {
			continue
		}
		// The request fast path increments inFlight while holding RLock. Once
		// this writer lock is held, zero cannot race with a new acquisition.
		if atomic.LoadInt64(&entry.inFlight) != 0 {
			continue
		}
		if atomic.LoadInt64(&entry.lastUsed) != candidate.lastUsed {
			continue
		}
		delete(r.target.clients, candidate.key)
		evicted++
		if entry.client != nil {
			clientsToClose = append(clientsToClose, entry.client)
		}
	}
	r.target.mu.Unlock()

	// CloseIdleConnections may call transport-specific code. Never run it
	// while holding the shared client-map lock.
	for _, client := range clientsToClose {
		client.CloseIdleConnections()
	}
	return evicted
}
