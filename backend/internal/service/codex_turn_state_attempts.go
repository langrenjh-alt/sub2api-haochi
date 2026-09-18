package service

import (
	"context"
	"time"
)

type CodexTurnStateAttemptRepository interface {
	AttemptCounts(context.Context, []int64) (map[int64]int, error)
	ReserveAttempt(context.Context, int64, int) (int, error)
	ResetAttempts(context.Context, int64) error
}

func (s *CodexTurnStateService) loadAttempts(ctx context.Context, ids []int64) error {
	r, ok := s.repo.(CodexTurnStateAttemptRepository)
	if !ok {
		return nil
	}
	s.attemptMu.Lock()
	defer s.attemptMu.Unlock()
	counts, err := r.AttemptCounts(ctx, ids)
	if err != nil {
		return err
	}
	s.mu.Lock()
	for _, id := range ids {
		s.attempts[id] = counts[id]
	}
	s.mu.Unlock()
	return nil
}

func (s *CodexTurnStateService) attemptCount(id int64) int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.attempts[id]
}

func (s *CodexTurnStateService) reserveAttempt(ctx context.Context, id int64, limit int) bool {
	s.attemptMu.Lock()
	defer s.attemptMu.Unlock()
	if limit <= 0 {
		limit = 50
	}
	count := s.attemptCount(id)
	if count >= limit {
		return false
	}
	if r, ok := s.repo.(CodexTurnStateAttemptRepository); ok {
		n, err := r.ReserveAttempt(ctx, id, limit)
		if err != nil {
			s.noteError("保存采集次数失败：" + err.Error())
			return false
		}
		if n == 0 {
			s.mu.Lock()
			s.attempts[id] = limit
			s.mu.Unlock()
			return false
		}
		count = n
	} else {
		count++
	}
	s.mu.Lock()
	s.attempts[id] = count
	s.mu.Unlock()
	return true
}

func (s *CodexTurnStateService) resetAttempts(ctx context.Context, id int64) error {
	s.attemptMu.Lock()
	defer s.attemptMu.Unlock()
	if r, ok := s.repo.(CodexTurnStateAttemptRepository); ok {
		writeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
		defer cancel()
		if err := r.ResetAttempts(writeCtx, id); err != nil {
			return err
		}
	}
	s.mu.Lock()
	s.attempts[id] = 0
	delete(s.cycleRetryAt, id)
	s.mu.Unlock()
	return nil
}
