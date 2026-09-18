package service

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// CodexTurnStateService keeps one Codex turn state per enrolled account.
//
// It has three jobs, and they are deliberately kept apart:
//
//  1. Harvest — every successful Codex response already carries a state, so the
//     normal gateway traffic refreshes the pool for free. Harvesting runs off
//     the request path: the caller hands over a header and returns immediately.
//  2. Inject — the request path asks for the account's current state and sets it
//     as a request header. This is a lock-free map read plus a TTL check.
//  3. Keep — an account that receives no traffic never refreshes, so a keeper
//     pass probes accounts whose state is missing or close to expiry. The probe
//     goes out through the configured dynamic-IP proxy.
//
// Optional transfer reconciliation changes only the configured A/B memberships.
type CodexTurnStateService struct {
	repo            CodexTurnStateRepository
	accountRepo     AccountRepository
	proxyRepo       ProxyRepository
	upstream        HTTPUpstream
	transport       CodexTurnStateTransport
	syncTransfer    func(context.Context, int64, []int64) error
	transferMembers map[int64][]int64
	transferErrors  map[int64]string
	cycleRetryAt    map[int64]time.Time

	now func() time.Time

	mu      sync.RWMutex
	cache   map[int64]cachedTurnState
	cfg     CodexTurnStateConfig
	cfgAt   time.Time
	started bool
	// lastProbe remembers when each account was last probed, so a single tick's
	// budget is spent on accounts that have not just been tried. Without it a
	// pool of accounts that never produce an injectable token would burn every
	// tick on the same few IDs and never reach the rest of the pool.
	lastProbe map[int64]time.Time

	// events carries observations from the request path to the writer goroutine
	// so that no upstream response is ever delayed by a database round trip.
	events chan codexTurnStateEvent
	// refresh requests an immediate re-collection, used by revocation.
	refresh       chan int64
	wake          chan struct{}
	enrolled      map[int64]int64
	generation    map[int64]uint64
	attempts      map[int64]int
	attemptMu     sync.Mutex
	jobsMu        sync.Mutex
	pending       []codexTurnStateJob
	inFlight      map[int64]struct{}
	manualPending map[int64]struct{}
	retryAfter    map[int64]time.Time

	cancel context.CancelFunc
	wg     sync.WaitGroup

	ticking atomic.Bool

	// agentIdentityMu serializes Agent Identity task registration, matching the
	// account test path.
	agentIdentityMu sync.Mutex

	runMu         sync.Mutex
	lastProbeAt   time.Time
	lastProbeNote string
	lastError     string
	nextTickAt    time.Time
}

// cachedTurnState is one account's currently usable state.
type cachedTurnState struct {
	state     string
	expiresAt time.Time
	issuedAt  time.Time
	recordID  int64
	groupID   int64
	source    string
	model     string
	revoked   bool
}

type codexTurnStateEvent struct {
	accountID  int64
	state      string
	statusCode int
	model      string
	source     string
	errText    string
	latencyMS  int64
	generation uint64
	proxyID    int64
}

func NewCodexTurnStateService(
	repo CodexTurnStateRepository,
	accountRepo AccountRepository,
	proxyRepo ProxyRepository,
	upstream HTTPUpstream,
) *CodexTurnStateService {
	return &CodexTurnStateService{
		repo:            repo,
		accountRepo:     accountRepo,
		proxyRepo:       proxyRepo,
		upstream:        upstream,
		now:             time.Now,
		cache:           make(map[int64]cachedTurnState),
		lastProbe:       make(map[int64]time.Time),
		events:          make(chan codexTurnStateEvent, codexTurnStateEventQueueSize),
		refresh:         make(chan int64, codexTurnStateMaxEnrolledAccounts),
		wake:            make(chan struct{}, 1),
		generation:      make(map[int64]uint64),
		attempts:        make(map[int64]int),
		inFlight:        make(map[int64]struct{}),
		manualPending:   make(map[int64]struct{}),
		retryAfter:      make(map[int64]time.Time),
		transferMembers: make(map[int64][]int64),
		transferErrors:  make(map[int64]string),
		cycleRetryAt:    make(map[int64]time.Time),
	}
}

// CodexTurnStateTransport sends a probe with the account's real transport
// selection. The account test service implements it; a nil transport falls back
// to the raw upstream, which is enough for an unprotected account.
type CodexTurnStateTransport interface {
	DoOpenAIProbe(request *http.Request, proxyURL string, account *Account) (*http.Response, error)
}

// SetTransport installs the transport used for collection.
func (s *CodexTurnStateService) SetTransport(transport CodexTurnStateTransport) {
	if s == nil {
		return
	}
	s.transport = transport
}

// Start launches the keeper and the observation writer. Safe to call twice.
func (s *CodexTurnStateService) Start() {
	if s == nil || s.repo == nil {
		return
	}
	s.mu.Lock()
	if s.started {
		s.mu.Unlock()
		return
	}
	s.started = true
	ctx, cancel := context.WithCancel(context.Background())
	s.cancel = cancel
	s.mu.Unlock()

	cfg := s.reloadConfig(ctx)
	s.syncEnrollment(ctx, cfg)
	s.preloadFromStore(ctx, s.enrolledIDs())
	s.reconcileTransfers(ctx, cfg)
	s.wg.Add(2)
	go func() {
		defer s.wg.Done()
		s.runWriter(ctx)
	}()
	go func() {
		defer s.wg.Done()
		s.runKeeper(ctx)
	}()
}

func (s *CodexTurnStateService) Stop() {
	if s == nil {
		return
	}
	s.mu.Lock()
	cancel := s.cancel
	s.cancel = nil
	s.started = false
	s.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	s.wg.Wait()
}

// ---------------------------------------------------------------- request path

// InjectionHeader returns the header name and value to attach to this account's
// next Codex request, for a request whose model is requestedModel (empty when the
// caller could not read one). It is called on the hot path, so it holds only a
// read lock and never touches the database.
func (s *CodexTurnStateService) InjectionHeader(accountID int64, requestedModel string) (string, string, bool) {
	if s == nil || accountID <= 0 {
		return "", "", false
	}
	cfg := s.cachedConfig()
	if !cfg.Enabled || !cfg.InjectEnabled || !cfg.InjectionMatchesModel(requestedModel) {
		return "", "", false
	}
	s.mu.RLock()
	entry, ok := s.cache[accountID]
	_, member := s.enrolled[accountID]
	scoped := s.enrolled != nil
	s.mu.RUnlock()
	if scoped && !member {
		return "", "", false
	}
	if !ok || !cfg.TicketQualified(entry.state, entry.model, entry.expiresAt, entry.revoked, s.now()) {
		return "", "", false
	}
	if entry.model != "" && requestedModel != "" && !strings.EqualFold(entry.model, requestedModel) {
		return "", "", false
	}
	if !entry.expiresAt.IsZero() && !s.now().Before(entry.expiresAt) {
		// Expired states are withheld: presenting a dead token is worse than
		// presenting none, because upstream would have to replace it.
		return "", "", false
	}
	if !cfg.AcceptsStateToken(entry.state) {
		// A token from a downgraded turn is withheld for the same reason: it
		// re-applies the downgrade on every request that carries it.
		return "", "", false
	}
	return cfg.InjectHeader, entry.state, true
}

// ForceInject reports whether the pooled state should overwrite the state the
// caller presented rather than only fill a blank header. The gateway hook owns
// the request, so it asks this instead of reading the config itself.
func (s *CodexTurnStateService) ForceInject() bool {
	if s == nil {
		return false
	}
	return s.cachedConfig().InjectMode == CodexTurnStateInjectModeForce
}

// ModelScoped reports whether injection depends on the requested model, i.e.
// whether an allow-list is configured. The gateway uses it to skip replaying the
// request body on the hot path when nothing depends on the model.
func (s *CodexTurnStateService) ModelScoped() bool {
	if s == nil {
		return false
	}
	return len(s.cachedConfig().InjectModels) > 0
}

// ObserveResponse feeds an upstream response back into the pool. Called from the
// gateway for every Codex response; it must stay cheap, so work is queued.
func (s *CodexTurnStateService) ObserveResponse(accountID int64, header http.Header, statusCode int, model string) {
	if s == nil || accountID <= 0 {
		return
	}
	cfg := s.cachedConfig()
	if !cfg.Enabled {
		return
	}
	if !s.isEnrolled(accountID) {
		return
	}
	if isStatusIn(statusCode, cfg.RevocationStatuses) {
		s.Revoke(accountID, statusCode, "上游下发撤销状态")
		return
	}
	state := ""
	if header != nil {
		state = strings.TrimSpace(header.Get(cfg.InjectHeader))
	}
	if state == "" {
		return
	}
	if !cfg.AcceptsIssuanceStatus(statusCode) {
		return
	}
	s.enqueue(codexTurnStateEvent{
		accountID:  accountID,
		state:      state,
		statusCode: statusCode,
		model:      model,
		source:     CodexTurnStateSourceHarvest,
	})
}

// Revoke drops the account's state and asks the keeper to re-collect at once.
func (s *CodexTurnStateService) Revoke(accountID int64, statusCode int, note string) {
	if s == nil || accountID <= 0 {
		return
	}
	s.mu.Lock()
	s.generation[accountID]++
	s.cache[accountID] = cachedTurnState{revoked: true}
	s.mu.Unlock()

	s.enqueue(codexTurnStateEvent{
		accountID:  accountID,
		statusCode: statusCode,
		errText:    note,
		source:     CodexTurnStateSourceHarvest,
	})
	select {
	case s.refresh <- accountID:
	default:
	}
	if !s.ticking.Load() {
		s.wakeKeeper()
	}
}

func (s *CodexTurnStateService) enqueue(event codexTurnStateEvent) {
	s.mu.RLock()
	event.generation = s.generation[event.accountID]
	s.mu.RUnlock()
	select {
	case s.events <- event:
	default:
		// A full queue means the writer is behind; dropping a harvest is safe
		// because the next response carries a state again.
		slog.Warn("codex turn state observation dropped: writer backlog full", "account_id", event.accountID)
	}
}

// ---------------------------------------------------------------- writer

func (s *CodexTurnStateService) runWriter(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case event := <-s.events:
			s.persist(ctx, event)
		}
	}
}

func (s *CodexTurnStateService) persist(ctx context.Context, event codexTurnStateEvent) {
	if event.accountID <= 0 {
		return
	}
	s.runMu.Lock()
	defer s.runMu.Unlock()
	defer s.wakeKeeper()
	writeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 15*time.Second)
	defer cancel()

	cfg := s.cachedConfig()
	now := s.now().UTC()
	if !cfg.Enabled || !s.isEnrolled(event.accountID) {
		return
	}
	s.mu.RLock()
	generation := s.generation[event.accountID]
	s.mu.RUnlock()
	if event.generation != generation {
		return
	}

	if event.errText != "" && event.state == "" {
		if err := s.repo.Invalidate(writeCtx, event.accountID, event.errText); err != nil {
			slog.Error("codex turn state revocation persist failed", "account_id", event.accountID, "error", err)
		}
		s.noteError(fmt.Sprintf("账号 %d：%s", event.accountID, event.errText))
		return
	}
	if event.state == "" {
		return
	}
	if event.model == "" || !strings.EqualFold(event.model, cfg.Model) {
		return
	}
	// The acceptance gate is enforced here as well as at the call sites: this is
	// the single point where a token becomes injectable, so an excluded status
	// must not reach it on any path.
	if !cfg.AcceptsIssuanceStatus(event.statusCode) {
		return
	}

	issued := decodeFernetIssuedAt(event.state)
	if issued.IsZero() {
		issued = now
	}
	expires := issued.Add(time.Duration(cfg.TTLSeconds) * time.Second)
	previous, hadPrevious := s.cachedState(event.accountID)
	// Echoes do not extend a pin, even when an opaque token has no timestamp.
	if hadPrevious && previous.state == event.state && !previous.revoked {
		issued, expires = previous.issuedAt, previous.expiresAt
	}

	// A state that is already past its lifetime is recorded but never injected.
	status := CodexTurnStateStatusActive
	if !now.Before(expires) {
		status = CodexTurnStateStatusExpired
	}
	// Shape decides whether this token may ever be injected. A downgraded token
	// is recorded so the history shows what came back, but it is stored under its
	// own status and kept out of the cache, so a good state survives the arrival
	// of a bad one instead of being overwritten by it.
	shape := ClassifyCodexTurnState(event.state)
	degradedShape := shape.Shape != CodexTurnStateShapeUnknown && !shape.Normal
	if degradedShape {
		status = CodexTurnStateStatusDegraded
	}
	accepted := cfg.AcceptsStateToken(event.state)
	if !accepted && !degradedShape {
		status = "rejected"
	}
	if accepted && now.Before(expires) {
		status = CodexTurnStateStatusActive
	}
	locked := hadPrevious && cfg.TicketQualified(previous.state, previous.model, previous.expiresAt, previous.revoked, now) &&
		now.Add(time.Duration(cfg.RenewBeforeSeconds)*time.Second).Before(previous.expiresAt)
	// Record replacement candidates but never replace a pinned state before
	// its renewal window. Standby rows must not win restart preloading.
	if accepted && hadPrevious && previous.state != event.state &&
		(locked || previous.issuedAt.After(issued)) {
		status = "standby"
	}

	record := &CodexTurnStateRecord{
		AccountID:        event.accountID,
		State:            event.state,
		Status:           status,
		Source:           event.source,
		HTTPStatus:       event.statusCode,
		Model:            event.model,
		ProxyID:          event.proxyID,
		LatencyMS:        event.latencyMS,
		StateFingerprint: stateFingerprint(event.state),
		StateLength:      len(event.state),
		IssuedAt:         &issued,
		ExpiresAt:        &expires,
		Error:            event.errText,
	}

	// Re-observing the same token is the common case (we injected it and the
	// upstream echoed no replacement), so it refreshes the existing row instead
	// of appending a duplicate.
	usable := status == CodexTurnStateStatusActive || (accepted && now.Before(expires) && status == CodexTurnStateStatusDegraded)
	if entry, ok := s.cachedState(event.accountID); usable && ok && entry.state == event.state && entry.recordID > 0 && !entry.revoked {
		if err := s.repo.Touch(writeCtx, entry.recordID, expires); err != nil {
			slog.Error("codex turn state refresh failed", "account_id", event.accountID, "error", err)
			return
		}
		entry.expiresAt = expires
		entry.revoked = false
		s.storeObservedCache(event, entry)
		return
	}

	id, err := s.repo.Insert(writeCtx, record)
	if err != nil {
		slog.Error("codex turn state persist failed", "account_id", event.accountID, "error", err)
		s.noteError(fmt.Sprintf("写入状态失败：%s", err.Error()))
		return
	}
	record.ID = id
	record.State = event.state
	if usable {
		s.storeObservedCache(event, cachedTurnState{
			state:     event.state,
			expiresAt: expires,
			issuedAt:  issued,
			recordID:  id,
			source:    event.source,
			model:     event.model,
		})
		if !hadPrevious || previous.revoked || expires.After(previous.expiresAt) {
			if err := s.resetAttempts(writeCtx, event.accountID); err != nil {
				s.noteError("重置采集次数失败：" + err.Error())
			}
		}
	}
	if status == CodexTurnStateStatusActive {
		s.noteProbeSuccess(event.source)
	}
}

// Generation fencing keeps observations queued before a revocation from
// resurrecting the token, including a write already in flight.
func (s *CodexTurnStateService) storeObservedCache(event codexTurnStateEvent, entry cachedTurnState) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.cfg.Enabled || s.generation[event.accountID] != event.generation {
		return
	}
	if previous, ok := s.cache[event.accountID]; ok && !previous.revoked && previous.issuedAt.After(entry.issuedAt) {
		return
	}
	s.cache[event.accountID] = entry
}

func (s *CodexTurnStateService) cachedState(accountID int64) (cachedTurnState, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	entry, ok := s.cache[accountID]
	return entry, ok
}

func (s *CodexTurnStateService) storeCache(accountID int64, entry cachedTurnState) {
	s.mu.Lock()
	s.cache[accountID] = entry
	s.mu.Unlock()
}

func (s *CodexTurnStateService) cachedConfig() CodexTurnStateConfig {
	s.mu.RLock()
	defer s.mu.RUnlock()
	cfg := s.cfg
	// Only keeper/admin paths reload configuration. Never block traffic on SQL.
	return cfg
}

func (s *CodexTurnStateService) reloadConfig(ctx context.Context) CodexTurnStateConfig {
	s.mu.RLock()
	current := s.cfg
	s.mu.RUnlock()
	if s.repo == nil {
		return current
	}
	loadCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()
	cfg, err := s.repo.Config(loadCtx)
	if err != nil {
		slog.Error("codex turn state config load failed", "error", err)
		return current
	}
	s.mu.Lock()
	s.cfg = cfg
	s.cfgAt = s.now()
	s.mu.Unlock()
	return cfg
}

// ---------------------------------------------------------------- keeper

func (s *CodexTurnStateService) runKeeper(ctx context.Context) {
	prune := time.NewTicker(codexTurnStatePruneInterval)
	defer prune.Stop()
	// Short reconciliation interval bounds expiry recovery independently of the
	// operator's (possibly very long) harvesting poll interval.
	transferTick := time.NewTicker(5 * time.Second)
	defer transferTick.Stop()
	cfg := s.cachedConfig()
	wait := time.Duration(cfg.PollSeconds) * time.Second
	if wait <= 0 {
		wait = 45 * time.Second
	}
	timer := time.NewTimer(wait)
	defer timer.Stop()
	s.mu.Lock()
	s.nextTickAt = s.now().Add(wait)
	s.mu.Unlock()
	for {
		select {
		case <-ctx.Done():
			return
		case <-prune.C:
			s.prune(ctx)
		case <-transferTick.C:
			if s.cachedConfig().TransferActive() {
				s.dispatchTick(ctx, false, false)
			}
		case <-s.wake:
			s.dispatchTick(ctx, true, false)
			// Manual wakeups do not postpone the automatic scan indefinitely.
			cfg = s.cachedConfig()
			nextWait := time.Duration(cfg.PollSeconds) * time.Second
			if nextWait > 0 && nextWait != wait {
				wait = nextWait
				if !timer.Stop() {
					select {
					case <-timer.C:
					default:
					}
				}
				timer.Reset(wait)
				s.mu.Lock()
				s.nextTickAt = s.now().Add(wait)
				s.mu.Unlock()
			}
		case <-timer.C:
			s.dispatchTick(ctx, false, false)
			cfg = s.cachedConfig()
			wait = time.Duration(cfg.PollSeconds) * time.Second
			if wait <= 0 {
				wait = 45 * time.Second
			}
			timer.Reset(wait)
			s.mu.Lock()
			s.nextTickAt = s.now().Add(wait)
			s.mu.Unlock()
		}
	}
}

// Tick runs one keeper pass: it refreshes the accounts whose state is missing or
// within the renewal window, plus any account that asked for an immediate
// refresh. Exported so the admin panel can force a pass.
func (s *CodexTurnStateService) Tick(ctx context.Context) {
	s.tick(ctx, false)
}

func (s *CodexTurnStateService) tick(ctx context.Context, priorityOnly bool) {
	s.dispatchTick(ctx, priorityOnly, true)
}

func (s *CodexTurnStateService) dispatchTick(ctx context.Context, priorityOnly, wait bool) {
	if s == nil || s.repo == nil {
		return
	}
	if !s.ticking.CompareAndSwap(false, true) {
		return
	}
	defer s.ticking.Store(false)

	cfg := s.reloadConfig(ctx)
	if !cfg.Enabled {
		s.jobsMu.Lock()
		s.pending = nil
		s.jobsMu.Unlock()
		return
	}
	passCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	enrolled, err := s.enrolledAccounts(passCtx, cfg)
	if err != nil {
		slog.Error("codex turn state enrollment scan failed", "error", err)
		s.noteError(fmt.Sprintf("读取检测账号失败：%s", err.Error()))
		return
	}
	s.setEnrollment(enrolled)
	accountIDs := sortedCodexTurnStateAccountIDs(enrolled)
	s.preloadFromStore(passCtx, accountIDs)
	s.reconcileTransfers(passCtx, cfg)
	if err := s.refreshCycleCooldowns(passCtx, cfg); err != nil {
		s.noteError("读取采集冷却失败：" + err.Error())
		return
	}
	if err := s.loadAttempts(passCtx, accountIDs); err != nil {
		s.noteError("读取采集次数失败：" + err.Error())
		return
	}

	s.dispatchCycles(ctx, cfg, accountIDs, priorityOnly, wait)
	now := s.now()

	if sweeper, ok := s.repo.(interface {
		ExpireStale(context.Context, time.Time) (int64, error)
	}); ok {
		if _, err := sweeper.ExpireStale(passCtx, now.UTC()); err != nil {
			slog.Error("codex turn state expiry sweep failed", "error", err)
		}
	}
}

// dueAccounts lists the accounts a bounded pass may probe now, in a stable
// order: state missing or renewing, minus whatever was probed within the last
// poll interval, minus the accounts the caller already handled.
//
// The cooldown is what makes the pass sweep the pool instead of hammering it.
// An account whose upstream keeps answering with a degraded token never caches
// anything, so "needs refresh" stays true forever for it — without a cooldown a
// pass of budget N would probe the same N lowest-ID accounts on every tick and
// the rest of the pool would never be reached.
func (s *CodexTurnStateService) dueAccounts(accountIDs []int64, cfg CodexTurnStateConfig, now time.Time, skip map[int64]struct{}) []int64 {
	cooldown := time.Duration(cfg.PollSeconds) * time.Second

	s.mu.RLock()
	last := make(map[int64]time.Time, len(s.lastProbe))
	for id, at := range s.lastProbe {
		last[id] = at
	}
	s.mu.RUnlock()

	out := make([]int64, 0, len(accountIDs))
	for _, id := range accountIDs {
		if cfg.MaxAttemptsPerCycle > 0 && s.attemptCount(id) >= cfg.MaxAttemptsPerCycle {
			continue
		}
		if skip != nil {
			if _, forced := skip[id]; forced {
				continue
			}
		}
		if at, ok := last[id]; ok && cooldown > 0 && now.Sub(at) < cooldown {
			continue
		}
		if !s.needsRefresh(id, cfg, now) {
			continue
		}
		out = append(out, id)
	}
	sort.SliceStable(out, func(i, j int) bool {
		a, b := last[out[i]], last[out[j]]
		if a.Equal(b) {
			return out[i] < out[j]
		}
		return a.Before(b)
	})
	return out
}

// markProbed stamps the cooldown. It runs before the request so a probe that
// fails slowly cannot be picked again while it is still in flight.
func (s *CodexTurnStateService) markProbed(accountID int64) {
	s.mu.Lock()
	if s.lastProbe == nil {
		s.lastProbe = make(map[int64]time.Time)
	}
	s.lastProbe[accountID] = s.now()
	s.mu.Unlock()
}

// needsRefresh reports whether an account's state is missing or inside the
// renewal window.
func (s *CodexTurnStateService) needsRefresh(accountID int64, cfg CodexTurnStateConfig, now time.Time) bool {
	entry, ok := s.cachedState(accountID)
	if !ok || !cfg.TicketQualified(entry.state, entry.model, entry.expiresAt, entry.revoked, now) {
		return true
	}
	renewBefore := time.Duration(cfg.RenewBeforeSeconds) * time.Second
	return !now.Add(renewBefore).Before(entry.expiresAt)
}

// enrolledAccounts resolves which accounts are enrolled, as accountID -> groupID.
// Group membership is filtered to accounts that can actually serve traffic; an
// explicit AccountIDs entry is enrolled regardless, because the operator asked
// for that account by name.
func (s *CodexTurnStateService) enrolledAccounts(ctx context.Context, cfg CodexTurnStateConfig) (map[int64]int64, error) {
	out := make(map[int64]int64)
	groupIDs := append([]int64(nil), cfg.GroupIDs...)
	members := map[int64][]int64{}
	if cfg.TransferActive() {
		groupIDs = append(groupIDs, cfg.TransferReadyGroupID, cfg.TransferRecoveryGroupID)
		if r, ok := s.repo.(CodexTurnStateTransferRepository); ok {
			var err error
			members, err = r.TransferMembers(ctx, cfg)
			if err != nil {
				return nil, err
			}
		}
	}
	if len(groupIDs) > 0 {
		fromGroups, err := s.repo.EnrolledAccounts(ctx, normalizeInt64IDs(groupIDs))
		if err != nil {
			return nil, err
		}
		for accountID, groupID := range fromGroups {
			out[accountID] = groupID
		}
	}
	for _, accountID := range normalizeInt64IDs(cfg.AccountIDs) {
		if _, ok := out[accountID]; !ok {
			out[accountID] = 0
		}
	}
	s.mu.Lock()
	s.transferMembers = members
	s.mu.Unlock()
	return out, nil
}

func (s *CodexTurnStateService) isEnrolled(id int64) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	_, ok := s.enrolled[id]
	return s.enrolled == nil || ok
}

func (s *CodexTurnStateService) enrolledIDs() []int64 {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return sortedCodexTurnStateAccountIDs(s.enrolled)
}

func (s *CodexTurnStateService) setEnrollment(enrolled map[int64]int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.enrolled = enrolled
	for id := range s.cache {
		if _, ok := enrolled[id]; !ok {
			delete(s.cache, id)
			s.generation[id]++
		}
	}
	for id := range s.lastProbe {
		if _, ok := enrolled[id]; !ok {
			delete(s.lastProbe, id)
		}
	}
}

func (s *CodexTurnStateService) syncEnrollment(ctx context.Context, cfg CodexTurnStateConfig) {
	enrolled, err := s.enrolledAccounts(ctx, cfg)
	if err != nil {
		s.noteError("读取检测账号失败：" + err.Error())
		// Fail closed rather than harvesting every gateway account.
		enrolled = map[int64]int64{}
	}
	s.setEnrollment(enrolled)
}

func (s *CodexTurnStateService) wakeKeeper() {
	select {
	case s.wake <- struct{}{}:
	default:
	}
}

// sortedAccountIDs gives the keeper a deterministic order, so a bounded pass
// sweeps the pooled accounts evenly instead of whatever order the map yields.
func sortedCodexTurnStateAccountIDs(groups map[int64]int64) []int64 {
	ids := make([]int64, 0, len(groups))
	for id := range groups {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	return ids
}

// preloadFromStore fills the cache so a process restart does not have to probe
// everything again, and so a state collected before a restart keeps serving.
func (s *CodexTurnStateService) preloadFromStore(ctx context.Context, accountIDs []int64) error {
	// Serialize reload with the writer/revocation persistence.
	s.runMu.Lock()
	defer s.runMu.Unlock()
	latest, err := s.repo.LatestPerAccount(ctx, accountIDs)
	if err != nil {
		slog.Error("codex turn state preload failed", "error", err)
		return err
	}
	now := s.now()
	cfg := s.cachedConfig()
	for id, record := range latest {
		if record.Status != CodexTurnStateStatusActive || record.ExpiresAt == nil ||
			!cfg.TicketQualified(record.State, record.Model, *record.ExpiresAt, false, now) {
			continue
		}
		if entry, ok := s.cachedState(id); ok && (entry.revoked || entry.recordID == record.ID) {
			continue
		}
		entry := cachedTurnState{
			state:     record.State,
			expiresAt: *record.ExpiresAt,
			recordID:  record.ID,
			source:    record.Source,
			model:     record.Model,
		}
		if record.IssuedAt != nil {
			entry.issuedAt = *record.IssuedAt
		}
		s.mu.Lock()
		if previous, exists := s.cache[id]; !exists || (!previous.revoked && previous.recordID != record.ID) {
			s.cache[id] = entry
		}
		s.mu.Unlock()
	}
	return nil
}

// probeOne collects a state for one account and records the outcome.
func (s *CodexTurnStateService) probeOne(ctx context.Context, accountID int64, cfg CodexTurnStateConfig) codexTurnStateProbeResult {
	if s.accountRepo == nil {
		return codexTurnStateProbeResult{}
	}
	s.markProbed(accountID)
	account, err := s.accountRepo.GetByID(ctx, accountID)
	if err != nil || account == nil {
		s.recordFailure(accountID, cfg, 0, 0, fmt.Sprintf("读取账号失败：%v", err))
		return codexTurnStateProbeResult{}
	}
	if account.Platform != PlatformOpenAI || account.Status != StatusActive || !account.Schedulable {
		s.recordFailure(accountID, cfg, 0, 0, "账号不是启用且可调度的 OpenAI 账号")
		return codexTurnStateProbeResult{}
	}
	proxyURL, proxyID := s.resolveProxyURL(ctx, account, cfg)
	if cfg.HarvestTransport == "independent" && cfg.ProxyID <= 0 {
		s.recordFailure(accountID, cfg, 0, 0, "独立采集通道缺少指定代理")
		return codexTurnStateProbeResult{}
	}
	if cfg.ProxyID > 0 && (proxyID != cfg.ProxyID || proxyURL == "") {
		s.recordFailure(accountID, cfg, 0, 0, "指定动态IP代理不可用，未发送采集请求")
		return codexTurnStateProbeResult{}
	}
	if !s.reserveAttempt(ctx, accountID, cfg.MaxAttemptsPerCycle) {
		s.noteProbe("limit_reached")
		return codexTurnStateProbeResult{}
	}

	s.mu.RLock()
	generation := s.generation[accountID]
	s.mu.RUnlock()
	result := s.probeCodexState(ctx, account, cfg, proxyURL)
	s.recordProbe(accountID, cfg, proxyID, result, generation)
	return result
}

func (s *CodexTurnStateService) recordProbe(accountID int64, cfg CodexTurnStateConfig, proxyID int64, result codexTurnStateProbeResult, generations ...uint64) {
	s.mu.Lock()
	s.lastProbeAt = s.now()
	s.mu.Unlock()

	switch {
	case result.Err != "":
		s.recordFailure(accountID, cfg, proxyID, result.LatencyMS, result.Err, result.HTTPStatus)
	case result.Revoked:
		s.Revoke(accountID, result.HTTPStatus, fmt.Sprintf("采集时上游返回撤销状态 %d", result.HTTPStatus))
		s.noteProbe("revoked")
	case result.State == "":
		s.recordFailure(accountID, cfg, proxyID, result.LatencyMS, fmt.Sprintf("上游返回 %d 且未下发状态", result.HTTPStatus), result.HTTPStatus)
	default:
		// Commit synchronously through the same persistence gate as harvests.
		// Otherwise the next fast retry could outrun the writer and waste quota.
		s.mu.RLock()
		generation := s.generation[accountID]
		s.mu.RUnlock()
		if len(generations) > 0 {
			generation = generations[0]
		}
		s.persist(context.Background(), codexTurnStateEvent{
			generation: generation,
			accountID:  accountID,
			state:      result.State,
			statusCode: result.HTTPStatus,
			model:      cfg.Model,
			source:     CodexTurnStateSourceProbe,
			latencyMS:  result.LatencyMS,
			proxyID:    proxyID,
		})
		s.noteProbe("ok")
	}
}

func (s *CodexTurnStateService) recordFailure(accountID int64, cfg CodexTurnStateConfig, proxyID int64, latencyMS int64, message string, statuses ...int) {
	record := &CodexTurnStateRecord{
		AccountID:  accountID,
		Status:     CodexTurnStateStatusFailed,
		Source:     CodexTurnStateSourceProbe,
		Model:      cfg.Model,
		ProxyID:    proxyID,
		LatencyMS:  latencyMS,
		Error:      message,
		HTTPStatus: 0,
	}
	if len(statuses) > 0 {
		record.HTTPStatus = statuses[0]
	}
	writeCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if _, err := s.repo.Insert(writeCtx, record); err != nil {
		slog.Error("codex turn state failure persist failed", "account_id", accountID, "error", err)
	}
	s.noteError(fmt.Sprintf("账号 %d 采集失败：%s", accountID, message))
	s.noteProbe("failed")
}

func (s *CodexTurnStateService) resolveProxyURL(ctx context.Context, account *Account, cfg CodexTurnStateConfig) (string, int64) {
	// The configured dynamic-IP proxy wins: it is the whole point of the
	// collection path. Without one, the account's own proxy is used.
	if cfg.ProxyID > 0 && s.proxyRepo != nil {
		if proxy, err := s.proxyRepo.GetByID(ctx, cfg.ProxyID); err == nil && proxy != nil && (cfg.HarvestTransport != "independent" || (proxy.IsActive() && !proxy.IsExpired(s.now()))) {
			if url := proxy.URL(); strings.TrimSpace(url) != "" {
				return url, proxy.ID
			}
		} else if err != nil {
			slog.Warn("codex turn state dynamic proxy lookup failed", "proxy_id", cfg.ProxyID, "error", err)
		}
	}
	if account != nil && account.ProxyID != nil && account.Proxy != nil {
		return account.Proxy.URL(), *account.ProxyID
	}
	return "", 0
}

func (s *CodexTurnStateService) prune(ctx context.Context) {
	pruneCtx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	removed, err := s.repo.Prune(pruneCtx, codexTurnStateHistoryKeepPerAccount, s.now().Add(-codexTurnStateHistoryRetention))
	if err != nil {
		slog.Error("codex turn state history prune failed", "error", err)
		return
	}
	if removed > 0 {
		slog.Info("codex turn state history pruned", "rows", removed)
	}
	// Enrollment refresh removes departed accounts. Keep timestamps for members
	// even in large pools: forgetting them would reintroduce low-ID starvation.
}

// ---------------------------------------------------------------- runtime notes

func (s *CodexTurnStateService) noteProbe(status string) {
	s.mu.Lock()
	s.lastProbeNote = status
	s.mu.Unlock()
}

func (s *CodexTurnStateService) noteProbeSuccess(source string) {
	if source == CodexTurnStateSourceProbe || source == CodexTurnStateSourceManual {
		s.noteProbe("ok")
	}
}

func (s *CodexTurnStateService) noteError(message string) {
	s.mu.Lock()
	s.lastError = message
	s.mu.Unlock()
}

// ---------------------------------------------------------------- admin API

func (s *CodexTurnStateService) Config(ctx context.Context) (CodexTurnStateConfig, error) {
	return s.repo.Config(ctx)
}

func (s *CodexTurnStateService) UpdateConfig(ctx context.Context, actor int64, cfg CodexTurnStateConfig) (CodexTurnStateConfig, error) {
	if err := ValidateCodexTurnStateConfig(cfg); err != nil {
		return CodexTurnStateConfig{}, err
	}
	normalized := NormalizeCodexTurnStateConfig(cfg)
	if err := ValidateCodexTurnStateConfig(normalized); err != nil {
		return CodexTurnStateConfig{}, err
	}
	// Disabling must remain possible even if a referenced proxy was deleted.
	if normalized.Enabled && normalized.ProxyID > 0 && s.proxyRepo != nil {
		proxy, err := s.proxyRepo.GetByID(ctx, normalized.ProxyID)
		if err != nil || proxy == nil {
			return CodexTurnStateConfig{}, fmt.Errorf("动态IP代理 #%d 已删除或不存在，请重新选择代理后保存", normalized.ProxyID)
		}
		if !proxy.IsActive() || proxy.IsExpired(s.now()) {
			return CodexTurnStateConfig{}, fmt.Errorf("动态IP代理 #%d 已停用或过期，请重新选择代理后保存", normalized.ProxyID)
		}
	}
	if err := s.repo.SaveConfig(ctx, actor, normalized); err != nil {
		return CodexTurnStateConfig{}, err
	}
	// The change must be visible to the request path immediately.
	s.mu.Lock()
	collectionChanged := s.cfg.Model != normalized.Model || s.cfg.TargetStateLength != normalized.TargetStateLength
	s.cfg = normalized
	s.cfgAt = s.now()
	if !normalized.Enabled || collectionChanged {
		for id := range s.cache {
			s.generation[id]++
		}
		clear(s.cache)
	}
	s.mu.Unlock()
	s.syncEnrollment(ctx, normalized)
	s.wakeKeeper()
	return normalized, nil
}

func (s *CodexTurnStateService) Overview(ctx context.Context) (*CodexTurnStateOverview, error) {
	cfg := s.reloadConfig(ctx)
	out := &CodexTurnStateOverview{CodexTurnStateConfig: cfg}

	enrolled, err := s.enrolledAccounts(ctx, cfg)
	if err != nil {
		return nil, err
	}
	// Include manually disabled A/B members in the management view, without
	// making them eligible for probes or altering their scheduling flags.
	s.mu.RLock()
	for id, groups := range s.transferMembers {
		if _, exists := enrolled[id]; !exists && len(groups) > 0 {
			enrolled[id] = groups[0]
		}
	}
	s.mu.RUnlock()
	out.EnrolledAccounts = len(enrolled)

	if cfg.ProxyID > 0 && s.proxyRepo != nil {
		if proxy, err := s.proxyRepo.GetByID(ctx, cfg.ProxyID); err == nil && proxy != nil {
			out.ProxyName = proxy.Name
			out.ProxyConfigured = true
		}
	}

	accountIDs := sortedCodexTurnStateAccountIDs(enrolled)
	if err := s.loadAttempts(ctx, accountIDs); err != nil {
		return nil, err
	}
	names := make(map[int64]string, len(accountIDs))
	if s.accountRepo != nil && len(accountIDs) > 0 {
		if accounts, err := s.accountRepo.GetByIDs(ctx, accountIDs); err == nil {
			for _, account := range accounts {
				if account != nil {
					names[account.ID] = account.Name
				}
			}
		}
	}

	latest, err := s.repo.LatestPerAccount(ctx, accountIDs)
	if err != nil {
		return nil, err
	}
	now := s.now()
	rows := make([]CodexTurnStateAccountRow, 0, len(accountIDs))
	for _, id := range accountIDs {
		row := CodexTurnStateAccountRow{
			AccountID:        id,
			AccountName:      names[id],
			GroupID:          enrolled[id],
			StateStatus:      "none",
			ProbeAttempts:    s.attemptCount(id),
			CollectionStatus: "waiting",
		}
		row.AttemptsLimitReached = row.ProbeAttempts >= cfg.MaxAttemptsPerCycle
		if row.AttemptsLimitReached {
			row.CollectionStatus = "limit_reached"
		}
		record, ok := latest[id]
		if !ok {
			rows = append(rows, row)
			continue
		}
		if record.Status == CodexTurnStateStatusActive && (record.ExpiresAt == nil || !now.Before(*record.ExpiresAt)) {
			record.Status = CodexTurnStateStatusExpired
		}
		if record.Status == CodexTurnStateStatusActive && !cfg.AcceptsStateToken(record.State) {
			record.Status = "rejected"
		}
		row.StateStatus = record.Status
		row.StateFingerprint = record.StateFingerprint
		row.StateLength = record.StateLength
		row.IssuedAt = record.IssuedAt
		row.ExpiresAt = record.ExpiresAt
		row.Source = record.Source
		row.HTTPStatus = record.HTTPStatus
		row.LatencyMS = record.LatencyMS
		row.Error = record.Error
		if !record.CreatedAt.IsZero() {
			created := record.CreatedAt
			row.RecordedAt = &created
		}
		row.HasState = record.StateLength > 0
		if record.ExpiresAt != nil {
			remaining := int64(record.ExpiresAt.Sub(now).Seconds())
			if remaining < 0 {
				remaining = 0
			}
			row.RemainingSeconds = remaining
		}
		switch record.Status {
		case CodexTurnStateStatusActive:
			if entry, ok := s.cachedState(id); ok && entry.revoked {
				row.StateStatus = CodexTurnStateStatusRevoked
				out.RevokedStates++
				break
			}
			out.ActiveStates++
			row.CollectionStatus = "locked"
			if row.RemainingSeconds <= int64(cfg.RenewBeforeSeconds) {
				out.ExpiringSoon++
				if row.AttemptsLimitReached {
					row.CollectionStatus = "limit_reached"
				} else {
					row.CollectionStatus = "renewing"
				}
			}
		case CodexTurnStateStatusRevoked:
			out.RevokedStates++
		case CodexTurnStateStatusFailed:
			out.FailedStates++
		}
		rows = append(rows, row)
	}
	s.jobsMu.Lock()
	out.CollectingAccounts = len(s.inFlight)
	out.QueuedAccounts = len(s.pending) + len(s.refresh)
	for i := range rows {
		if _, running := s.inFlight[rows[i].AccountID]; running && rows[i].CollectionStatus != "locked" {
			rows[i].CollectionStatus = "collecting"
		}
	}
	s.jobsMu.Unlock()
	s.enrichTransferOverview(ctx, cfg, rows, out)
	out.Accounts = rows

	if counters, ok := s.repo.(interface {
		CountersSince(context.Context, time.Time) (int64, int64, error)
	}); ok {
		dayStart := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
		if probes, harvests, err := counters.CountersSince(ctx, dayStart); err == nil {
			out.ProbesToday = probes
			out.HarvestsToday = harvests
		}
	}

	s.mu.Lock()
	if !s.lastProbeAt.IsZero() {
		at := s.lastProbeAt
		out.LastProbeAt = &at
	}
	out.LastProbeStatus = s.lastProbeNote
	out.LastError = s.lastError
	if !s.nextTickAt.IsZero() {
		next := s.nextTickAt
		out.NextTickAt = &next
	}
	s.mu.Unlock()
	return out, nil
}

// CollectNow queues an immediate collection. accountID 0 collects every account
// that is due.
func (s *CodexTurnStateService) CollectNow(ctx context.Context, accountID int64) (int, error) {
	cfg := s.reloadConfig(ctx)
	if !cfg.Enabled {
		return 0, ErrCodexTurnStateDisabled
	}
	if accountID > 0 {
		enrolled, err := s.enrolledAccounts(ctx, cfg)
		if err != nil {
			return 0, err
		}
		if _, ok := enrolled[accountID]; !ok {
			return 0, ErrCodexTurnStateAccountMiss
		}
		if err := s.refreshCycleCooldowns(ctx, cfg); err != nil {
			return 0, err
		}
		return s.queueManualCycle(ctx, accountID)
	}
	enrolled, err := s.enrolledAccounts(ctx, cfg)
	if err != nil {
		return 0, err
	}
	if err := s.refreshCycleCooldowns(ctx, cfg); err != nil {
		return 0, err
	}
	queued := 0
	for _, id := range sortedCodexTurnStateAccountIDs(enrolled) {
		n, err := s.queueManualCycle(ctx, id)
		queued += n
		if err != nil {
			return queued, err
		}
	}
	s.wakeKeeper()
	return queued, nil
}

// Invalidate drops one account's state without waiting for expiry.
func (s *CodexTurnStateService) Invalidate(ctx context.Context, accountID int64) error {
	if accountID <= 0 {
		return ErrCodexTurnStateAccountMiss
	}
	s.mu.Lock()
	s.generation[accountID]++
	s.cache[accountID] = cachedTurnState{revoked: true}
	s.mu.Unlock()
	s.runMu.Lock()
	err := s.repo.Invalidate(ctx, accountID, "管理员手动作废")
	s.runMu.Unlock()
	if err != nil {
		return err
	}
	if err := s.resetAttempts(ctx, accountID); err != nil {
		return err
	}
	select {
	case s.refresh <- accountID:
	default:
	}
	s.wakeKeeper()
	return nil
}

func (s *CodexTurnStateService) History(ctx context.Context, accountID int64, page, pageSize int) (*CodexTurnStateHistoryPage, error) {
	if page < 1 {
		page = 1
	}
	if pageSize < 1 {
		pageSize = 20
	}
	if pageSize > 100 {
		pageSize = 100
	}
	return s.repo.History(ctx, accountID, page, pageSize)
}

func isStatusIn(statusCode int, statuses []int) bool {
	for _, code := range statuses {
		if code == statusCode {
			return true
		}
	}
	return false
}
