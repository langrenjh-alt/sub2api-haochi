package service

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"
)

// Detection cadence. The worker ticks faster than the minimum interval so a
// one-minute interval is honoured, and the per-tick caps keep the shared test
// queue (running cap 4, queued cap 2000) out of reach.
const (
	degradationScanInterval = time.Minute
	degradationProbePerTick = 60
	degradationTotalPerTick = 300
	// The probe worker drains four tests at a time, so the scheduler stops
	// enqueueing once this many of its own tests are pending. The interval is a
	// target, not a guarantee: coverage sweeps the stalest accounts first so a
	// large group is still probed evenly, just slower than the nominal rate.
	degradationMaxQueued    = 200
	degradationOverviewRows = 200
)

// DegradationService owns the periodic probe, the public artwork schedule and
// the translation of a probe verdict into account scheduling.
//
// The answer a probe is graded against is read from the same group config the
// admin panel edits and is copied into the queued row, so the verdict and the
// operator-visible numbers can never disagree.
//
// It never runs a test itself: it enqueues rows that the shared
// IntelligentTestService worker executes, and it reacts only to records that
// were already persisted. That keeps the generic runner free of policy changes
// (the runner's repository wrapper still refuses every scheduling write).
type DegradationService struct {
	repo   DegradationRepository
	cancel context.CancelFunc
	mu     sync.Mutex
	wg     sync.WaitGroup
}

func NewDegradationService(repo DegradationRepository) *DegradationService {
	return &DegradationService{repo: repo}
}

// Start launches the scheduler. It is safe to call more than once.
func (s *DegradationService) Start() {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cancel != nil {
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	s.cancel = cancel
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		ticker := time.NewTicker(degradationScanInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				s.Tick(ctx)
			}
		}
	}()
}

func (s *DegradationService) Stop() {
	if s == nil {
		return
	}
	s.mu.Lock()
	if s.cancel != nil {
		s.cancel()
	}
	s.mu.Unlock()
	s.wg.Wait()
}

// Tick queues every probe and artwork job that is currently due. Exported so the
// admin panel can force a pass without waiting for the next minute.
func (s *DegradationService) Tick(ctx context.Context) {
	if s == nil || s.repo == nil {
		return
	}
	scanCtx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	groups, err := s.repo.Groups(scanCtx)
	if err != nil {
		slog.Error("degradation detection group scan failed", "error", err)
		return
	}
	depth, err := s.repo.PendingQueueDepth(scanCtx)
	if err != nil {
		slog.Error("degradation detection queue depth probe failed", "error", err)
	}
	// A full backlog pauses probing only. The single artwork job keeps its own
	// cadence so the public page never goes stale behind a busy probe queue.
	backlogFull := err == nil && depth >= degradationMaxQueued
	if backlogFull {
		slog.Warn("degradation detection backlog is full; skipping probes this pass", "pending", depth)
	}
	queued := 0
	for _, group := range groups {
		if backlogFull {
			break
		}
		if !group.Config.Enabled || ctx.Err() != nil || queued >= degradationTotalPerTick {
			continue
		}
		limit := degradationProbePerTick
		if remaining := degradationTotalPerTick - queued; remaining < limit {
			limit = remaining
		}
		ids, err := s.repo.DueProbeAccountIDs(scanCtx, group.GroupID, group.Config.IntervalMinute, limit)
		if err != nil {
			slog.Error("degradation detection due scan failed", "group_id", group.GroupID, "error", err)
			continue
		}
		for _, accountID := range ids {
			created, err := s.enqueueProbe(scanCtx, accountID, group.Config)
			if err != nil {
				slog.Error("degradation probe enqueue failed", "account_id", accountID, "error", err)
				continue
			}
			if created {
				queued++
			}
		}
	}
	if err := s.schedulePreview(scanCtx, groups); err != nil {
		slog.Error("degradation preview enqueue failed", "error", err)
	}
}

func (s *DegradationService) enqueueProbe(ctx context.Context, accountID int64, cfg DegradationDetectionConfig) (bool, error) {
	prompt := strings.TrimSpace(cfg.Prompt)
	if prompt == "" {
		prompt = DegradationCandyPrompt
	}
	_, created, err := s.repo.EnqueueDegradationTest(ctx, accountID, DegradationTestTypeProbe, prompt, cfg.Model, cfg.ReasoningEffort, cfg.ExpectedAnswer, cfg.TimeoutSeconds)
	return created, err
}

// schedulePreview keeps the public page fed at its own interval, independent of
// the probe interval and of any single account's verdict.
func (s *DegradationService) schedulePreview(ctx context.Context, groups []DegradationGroup) error {
	var cfg DegradationDetectionConfig
	found := false
	for _, group := range groups {
		if group.Config.Enabled && group.Config.PreviewEnabled {
			cfg = group.Config
			found = true
			break
		}
	}
	if !found {
		return nil
	}
	previewGroups, err := s.repo.PreviewGroups(ctx)
	if err != nil {
		return err
	}
	if len(previewGroups) == 0 {
		return nil
	}
	due, err := s.previewDue(ctx, cfg.PreviewIntervalMinute)
	if err != nil || !due {
		return err
	}
	ids, err := s.repo.DuePreviewAccountIDs(ctx, previewGroups, cfg.PreviewIntervalMinute, 1)
	if err != nil {
		return err
	}
	// The artwork prompt is fixed by the feature: the group config customizes
	// model, effort, interval and timeout, not the picture.
	prompt := DegradationPelicanPrompt
	for _, accountID := range ids {
		// The artwork is graded by shape, not by a number, so no answer is passed.
		_, _, err := s.repo.EnqueueDegradationTest(ctx, accountID, DegradationTestTypePreview, prompt, cfg.PreviewModel, cfg.PreviewReasoningEffort, "", cfg.TimeoutSeconds)
		return err
	}
	return nil
}

// previewDue reports whether the newest artwork is older than the interval. A
// queued or running job already counts as "not due" so the page never lags
// behind a long request.
func (s *DegradationService) previewDue(ctx context.Context, intervalMinutes int) (bool, error) {
	page, err := s.repo.PublicPage(ctx, 1, 1)
	if err != nil {
		return false, err
	}
	// PreviewPage returns LastFinishedAt for terminal records only; the running
	// guard is enforced by DuePreviewAccountIDs.
	if page.LastFinishedAt == nil {
		return true, nil
	}
	return time.Since(*page.LastFinishedAt) >= time.Duration(intervalMinutes)*time.Minute, nil
}

// HandleIntelligentTestOutcome is the outcome hook installed on the intelligent
// test service. Only a probe verdict that evaluators resolved to correct or
// incorrect changes scheduling; undetermined results never touch an account.
func (s *DegradationService) HandleIntelligentTestOutcome(ctx context.Context, record *IntelligentTestRecord) {
	if s == nil || record == nil || record.TestType != DegradationTestTypeProbe || record.Evaluation == nil {
		return
	}
	verdict, _ := record.Evaluation["answer_verdict"].(string)
	if verdict != "correct" && verdict != "incorrect" {
		return
	}
	handleCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 15*time.Second)
	defer cancel()
	_, cfg, err := s.repo.AccountConfig(handleCtx, record.AccountID)
	if err != nil {
		slog.Error("degradation outcome config lookup failed", "account_id", record.AccountID, "error", err)
		return
	}
	if verdict == "correct" {
		released, err := s.repo.ApplyProbeOutcome(handleCtx, record.AccountID, false, 0, "")
		if err != nil {
			slog.Error("degradation recovery failed", "account_id", record.AccountID, "error", err)
			return
		}
		if released {
			slog.Info("degradation detection recovered account", "account_id", record.AccountID, "record_id", record.ID)
		}
		return
	}
	answer := strings.TrimSpace(stringField(record.Evaluation, "normalized_answer"))
	if answer == "" {
		answer = strings.TrimSpace(stringField(record.Evaluation, "actual_answer"))
	}
	minutes := cfg.SuspendMinute
	if minutes <= 0 {
		minutes = DegradationDefaultSuspendMinute
	}
	// Report the number the verdict was actually produced against, not whatever
	// the group is configured with now: an edit between the request and the
	// verdict must not be able to print a self-contradicting note.
	expected := strings.TrimSpace(stringField(record.Evaluation, "expected_answer"))
	if expected == "" && record.ConfigSnapshot != nil {
		expected = strings.TrimSpace(record.ConfigSnapshot.ExpectedAnswer)
	}
	if expected == "" {
		expected = cfg.ExpectedAnswer
	}
	note := fmt.Sprintf("%s：答案 %s（应为 %s），暂停调度 %d 分钟", DegradationSuspendReasonPrefix, fallback(answer, "非整数"), fallback(expected, DegradationExpectedAnswer), minutes)
	suspended, err := s.repo.ApplyProbeOutcome(handleCtx, record.AccountID, true, minutes, note)
	if err != nil {
		slog.Error("degradation suspension failed", "account_id", record.AccountID, "error", err)
		return
	}
	if suspended {
		slog.Warn("degradation detection suspended account", "account_id", record.AccountID, "record_id", record.ID, "minutes", minutes)
	}
}

func stringField(values map[string]any, key string) string {
	value, _ := values[key].(string)
	return value
}

func fallback(value, replaced string) string {
	if strings.TrimSpace(value) == "" {
		return replaced
	}
	return value
}

// ---------------------------------------------------------------- admin API

func (s *DegradationService) Groups(ctx context.Context) ([]DegradationGroup, error) {
	return s.repo.Groups(ctx)
}

func (s *DegradationService) UpdateGroupConfig(ctx context.Context, actor, groupID int64, cfg DegradationDetectionConfig) (DegradationDetectionConfig, error) {
	// Reject malformed input before the normalizer can paper over it, then
	// validate the concrete values that will actually be persisted.
	if err := ValidateDegradationConfig(cfg); err != nil {
		return DegradationDetectionConfig{}, err
	}
	normalized := NormalizeDegradationConfig(cfg)
	if err := ValidateDegradationConfig(normalized); err != nil {
		return DegradationDetectionConfig{}, err
	}
	if err := s.repo.UpdateGroupConfig(ctx, actor, groupID, normalized); err != nil {
		return DegradationDetectionConfig{}, err
	}
	return normalized, nil
}

func (s *DegradationService) Overview(ctx context.Context) (*DegradationOverview, error) {
	return s.repo.Overview(ctx, degradationOverviewRows)
}

// RunNow queues probes for every group whose detector is on, ignoring the
// interval but not the manual-disable rule.
func (s *DegradationService) RunNow(ctx context.Context, groupID int64) (int, error) {
	groups, err := s.repo.Groups(ctx)
	if err != nil {
		return 0, err
	}
	queued := 0
	for _, group := range groups {
		if !group.Config.Enabled || (groupID > 0 && group.GroupID != groupID) {
			continue
		}
		ids, err := s.repo.DueProbeAccountIDs(ctx, group.GroupID, 0, degradationProbePerTick)
		if err != nil {
			return queued, err
		}
		for _, accountID := range ids {
			created, err := s.enqueueProbe(ctx, accountID, group.Config)
			if err != nil {
				return queued, err
			}
			if created {
				queued++
			}
		}
	}
	return queued, nil
}

// --------------------------------------------------------------- public API

// PublicPage renders the artwork list for /jiangzhijiance/. Every SVG is
// re-encoded through the same inert-preview pipeline the admin panel uses, so
// the public page can never serve stored script or external references.
func (s *DegradationService) PublicPage(ctx context.Context, page, pageSize int) (*DegradationPublicPage, error) {
	if page < 1 {
		page = 1
	}
	if pageSize < 1 {
		pageSize = 6
	}
	if pageSize > 24 {
		pageSize = 24
	}
	out, err := s.repo.PublicPage(ctx, page, pageSize)
	if err != nil {
		return nil, err
	}
	out.Headline = "鹈鹕骑行"
	for i := range out.Items {
		out.Items[i].Image = safePublicSVG(out.Items[i].Image)
	}
	return out, nil
}

// PublicWork returns one sanitized artwork.
func (s *DegradationService) PublicWork(ctx context.Context, id int64) (*DegradationPublicWork, error) {
	work, err := s.repo.PublicWork(ctx, id)
	if err != nil {
		return nil, err
	}
	work.Image = safePublicSVG(work.Image)
	return work, nil
}

func safePublicSVG(raw string) string {
	if strings.TrimSpace(raw) == "" {
		return ""
	}
	safe, _, err := PrepareIntelligentSVGPreview(raw)
	if err != nil {
		return ""
	}
	return safe
}

// Timeline returns the public health chart. Probe verdicts are the only public
// statement about degradation, and they are reduced to per-bucket counts.
func (s *DegradationService) Timeline(ctx context.Context, hours int) (*DegradationTimeline, error) {
	return s.repo.Timeline(ctx, hours)
}

// Works lists the artworks the public page renders. The portal is a curated
// surface, so an operator needs to see and remove what is on it.
func (s *DegradationService) Works(ctx context.Context, page, pageSize int) (*DegradationWorkPage, error) {
	if page < 1 {
		page = 1
	}
	if pageSize < 1 {
		pageSize = 24
	}
	if pageSize > 100 {
		pageSize = 100
	}
	return s.repo.Works(ctx, page, pageSize)
}

// DeleteWork removes one artwork from the public feed.
func (s *DegradationService) DeleteWork(ctx context.Context, id int64) (bool, error) {
	if id < 1 {
		return false, ErrDegradationWorkNotFound
	}
	return s.repo.DeleteWork(ctx, id)
}

// PurgeWorks empties the public feed and reports how many artworks went away.
func (s *DegradationService) PurgeWorks(ctx context.Context) (int64, error) {
	return s.repo.PurgeWorks(ctx)
}
