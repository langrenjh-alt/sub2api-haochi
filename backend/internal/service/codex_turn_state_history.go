package service

import (
	"context"
	"fmt"
)

type CodexTurnStateHistoryClearer interface {
	ClearHistory(context.Context, []int64) (int64, int64, error)
}

func (s *CodexTurnStateService) ClearHistory(ctx context.Context) (int64, int64, error) {
	r, ok := s.repo.(CodexTurnStateHistoryClearer)
	if !ok {
		return 0, 0, fmt.Errorf("采集历史清理接口未就绪")
	}
	s.runMu.Lock()
	defer s.runMu.Unlock()
	cfg := s.cachedConfig()
	now := s.now()
	s.mu.RLock()
	keep := make([]int64, 0, len(s.cache))
	for _, entry := range s.cache {
		if !entry.revoked && now.Before(entry.expiresAt) && cfg.AcceptsStateToken(entry.state) && entry.recordID > 0 {
			keep = append(keep, entry.recordID)
		}
	}
	s.mu.RUnlock()
	// Independent attempt counters are deliberately untouched.
	return r.ClearHistory(ctx, keep)
}
