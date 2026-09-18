package service

import (
	"context"
	"time"
)

type CodexTurnStateRetry struct {
	CycleAt    time.Time
	UpstreamAt time.Time
	Restarted  bool
}
type CodexTurnStateRetryRepository interface {
	RefreshRetryWindows(context.Context, []int64, int, int, time.Time) (map[int64]CodexTurnStateRetry, error)
	SaveUpstreamRetry(context.Context, int64, time.Time) error
}

func (s *CodexTurnStateService) refreshCycleCooldowns(ctx context.Context, cfg CodexTurnStateConfig) error {
	r, ok := s.repo.(CodexTurnStateRetryRepository)
	if !ok {
		return nil
	}
	// Do not start/reset a round while its final probe is still in flight.
	// Same lock order as manual queue admission: jobs -> attempts -> state.
	s.jobsMu.Lock()
	defer s.jobsMu.Unlock()
	s.attemptMu.Lock()
	defer s.attemptMu.Unlock()
	s.mu.RLock()
	ids := []int64{}
	for id := range s.transferMembers {
		if _, running := s.inFlight[id]; cfg.TransferActive() && !running {
			ids = append(ids, id)
		}
	}
	s.mu.RUnlock()
	windows, err := r.RefreshRetryWindows(ctx, ids, cfg.MaxAttemptsPerCycle, cfg.CycleCooldownSeconds, s.now())
	if err != nil {
		return err
	}
	counts := map[int64]int{}
	if attempts, ok := s.repo.(CodexTurnStateAttemptRepository); ok {
		counts, err = attempts.AttemptCounts(ctx, ids)
		if err != nil {
			return err
		}
	}
	s.mu.Lock()
	for id, window := range windows {
		s.cycleRetryAt[id] = window.CycleAt
		if window.UpstreamAt.After(s.retryAfter[id]) {
			s.retryAfter[id] = window.UpstreamAt
		}
		if count, exists := counts[id]; exists {
			if window.Restarted || (count == 0 && s.attempts[id] >= cfg.MaxAttemptsPerCycle) {
				delete(s.lastProbe, id)
			}
			s.attempts[id] = count
		}
	}
	s.mu.Unlock()
	return nil
}

func (s *CodexTurnStateService) persistUpstreamRetry(ctx context.Context, id int64, at time.Time) {
	if r, ok := s.repo.(CodexTurnStateRetryRepository); ok {
		if err := r.SaveUpstreamRetry(ctx, id, at); err != nil {
			s.noteError("保存限流冷却失败：" + err.Error())
		}
	}
}
