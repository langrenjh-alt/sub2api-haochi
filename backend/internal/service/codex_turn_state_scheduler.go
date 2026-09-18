package service

import (
	"context"
	"net/http"
	"sync"
	"time"
)

type codexTurnStateJob struct {
	id    int64
	force bool
}

// dispatchCycles only admits work; it never waits for upstream requests.
// Completion wakes the keeper to refill vacant slots from the EXISTING queue,
// not to scan unrelated accounts after a single-account manual request.
func (s *CodexTurnStateService) dispatchCycles(ctx context.Context, cfg CodexTurnStateConfig, ids []int64, priorityOnly, wait bool) {
	s.jobsMu.Lock()
	known := make(map[int64]bool, len(s.pending)+len(s.inFlight))
	for _, job := range s.pending {
		known[job.id] = true
	}
	for id := range s.inFlight {
		known[id] = true
	}
	var priority []codexTurnStateJob
drain:
	for n := 0; n < cfg.MaxAccountsPerTick; n++ {
		select {
		case id := <-s.refresh:
			delete(s.manualPending, id)
			if !s.isEnrolled(id) {
				continue
			}
			if _, running := s.inFlight[id]; running {
				continue
			}
			// Promote already queued automatic work rather than duplicating it.
			for i, job := range s.pending {
				if job.id == id {
					s.pending = append(s.pending[:i], s.pending[i+1:]...)
					known[id] = false
					break
				}
			}
			if !known[id] {
				priority = append(priority, codexTurnStateJob{id: id, force: true})
				known[id] = true
			}
		default:
			break drain
		}
	}
	s.pending = append(priority, s.pending...)
	if !priorityOnly {
		skip := make(map[int64]struct{}, len(known))
		for id := range known {
			skip[id] = struct{}{}
		}
		// Limit admission, not attempts. Already admitted accounts retry
		// independently of PollSeconds.
		for n, id := range s.dueAccounts(ids, cfg, s.now(), skip) {
			if n >= cfg.MaxAccountsPerTick {
				break
			}
			s.pending = append(s.pending, codexTurnStateJob{id: id})
		}
	}
	limit := cfg.ConcurrentAccounts
	if limit <= 0 {
		limit = 16
	}
	if limit > 64 {
		limit = 64
	}
	var selected []codexTurnStateJob
	remaining := s.pending[:0]
	for _, job := range s.pending {
		if !s.isEnrolled(job.id) {
			continue
		}
		if !job.force && !s.needsRefresh(job.id, cfg, s.now()) {
			continue
		}
		if s.attemptCount(job.id) >= cfg.MaxAttemptsPerCycle {
			continue
		}
		if len(s.inFlight) >= limit || s.now().Before(s.retryAfter[job.id]) {
			remaining = append(remaining, job)
			continue
		}
		s.inFlight[job.id] = struct{}{}
		selected = append(selected, job)
	}
	s.pending = remaining
	s.jobsMu.Unlock()
	var batch sync.WaitGroup
	for _, job := range selected {
		batch.Add(1)
		s.wg.Add(1)
		go func(job codexTurnStateJob) {
			defer batch.Done()
			defer s.wg.Done()
			defer func() {
				s.jobsMu.Lock()
				delete(s.inFlight, job.id)
				s.jobsMu.Unlock()
				s.wakeKeeper()
			}()
			s.collectCycle(ctx, job)
		}(job)
	}
	if wait {
		batch.Wait()
	}
}

func (s *CodexTurnStateService) collectCycle(ctx context.Context, job codexTurnStateJob) {
	failures := 0
	for first := true; ctx.Err() == nil; first = false {
		cfg := s.cachedConfig()
		if !cfg.Enabled || !s.isEnrolled(job.id) {
			return
		}
		if !(first && job.force) && !s.needsRefresh(job.id, cfg, s.now()) {
			return
		}
		if s.attemptCount(job.id) >= cfg.MaxAttemptsPerCycle {
			return
		}
		result := s.probeOne(ctx, job.id, cfg)
		if !result.Attempted || ctx.Err() != nil {
			return
		}
		if result.HTTPStatus == http.StatusTooManyRequests || result.HTTPStatus == http.StatusUnauthorized || result.HTTPStatus == http.StatusForbidden {
			delay := result.RetryAfter
			minimum := 30 * time.Second
			if result.HTTPStatus != http.StatusTooManyRequests {
				minimum = 5 * time.Minute
			}
			if delay < minimum {
				delay = minimum
			}
			s.jobsMu.Lock()
			s.retryAfter[job.id] = s.now().Add(delay)
			s.jobsMu.Unlock()
			s.persistUpstreamRetry(ctx, job.id, s.now().Add(delay))
			return
		}
		delay := time.Duration(cfg.RetryIntervalMS) * time.Millisecond
		if delay <= 0 {
			delay = 200 * time.Millisecond
		}
		if result.Err != "" || result.HTTPStatus >= 500 {
			failures++
			backoff := time.Second * time.Duration(1<<min(failures, 5))
			if delay < backoff {
				delay = backoff
			}
		} else {
			failures = 0
		}
		if delay < result.RetryAfter {
			delay = result.RetryAfter
		}
		if result.RetryAfter > 0 || result.Err != "" || result.HTTPStatus >= 500 {
			at := s.now().Add(delay)
			s.jobsMu.Lock()
			s.retryAfter[job.id] = at
			s.jobsMu.Unlock()
			s.persistUpstreamRetry(ctx, job.id, at)
		}
		// Even a manual probe made while an old pin remains valid must retain
		// upstream backoff. Only decide whether to stop after saving that delay.
		if !s.needsRefresh(job.id, s.cachedConfig(), s.now()) || s.attemptCount(job.id) >= cfg.MaxAttemptsPerCycle {
			return
		}
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
	}
}

// Repeated clicks join a pending/running cycle. They do not reset its counter or
// create parallel requests for the same account.
func (s *CodexTurnStateService) queueManualCycle(ctx context.Context, id int64) (int, error) {
	s.jobsMu.Lock()
	defer s.jobsMu.Unlock()
	if _, ok := s.inFlight[id]; ok {
		return 0, nil
	}
	if _, ok := s.manualPending[id]; ok {
		return 0, nil
	}
	if err := s.resetAttempts(ctx, id); err != nil {
		return 0, err
	}
	select {
	case s.refresh <- id:
		s.manualPending[id] = struct{}{}
		s.wakeKeeper()
		return 1, nil
	default:
		return 0, nil
	}
}
