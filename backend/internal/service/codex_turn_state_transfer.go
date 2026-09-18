package service

import (
	"context"
	"fmt"
	"strings"
	"time"
)

func (cfg CodexTurnStateConfig) TransferActive() bool {
	return cfg.Enabled && cfg.InjectEnabled && cfg.TransferEnabled
}

// Ticket qualification is shared by injection and group reconciliation. A
// renewal window is not an expiration: an old valid pin continues to qualify.
func (cfg CodexTurnStateConfig) TicketQualified(state, model string, expires time.Time, revoked bool, now time.Time) bool {
	return !revoked && now.Before(expires) && cfg.AcceptsStateToken(state) &&
		model != "" && strings.EqualFold(model, cfg.Model)
}

type CodexTurnStateTransfer struct {
	ID             int64     `json:"id"`
	AccountID      int64     `json:"account_id"`
	FromGroupID    int64     `json:"from_group_id"`
	ToGroupID      int64     `json:"to_group_id"`
	TicketRecordID int64     `json:"ticket_record_id"`
	Reason         string    `json:"reason"`
	CreatedAt      time.Time `json:"created_at"`
}

type CodexTurnStateTransferRepository interface {
	TransferMembers(context.Context, CodexTurnStateConfig) (map[int64][]int64, error)
	ReconcileTransfer(context.Context, CodexTurnStateConfig, int64, int64, bool) (*CodexTurnStateTransfer, error)
	RecentTransfers(context.Context) ([]CodexTurnStateTransfer, error)
}

func (s *CodexTurnStateService) reconcileTransfers(ctx context.Context, cfg CodexTurnStateConfig) {
	if !cfg.TransferActive() {
		return
	}
	r, ok := s.repo.(CodexTurnStateTransferRepository)
	if !ok {
		return
	}
	members, err := r.TransferMembers(ctx, cfg)
	if err != nil {
		s.noteError("读取移组成员失败：" + err.Error())
		return
	}
	ids := make([]int64, 0, len(members))
	for id := range members {
		ids = append(ids, id)
	}
	// A failed read is not an empty pool. Do not demote anything on SQL errors.
	if err := s.preloadFromStore(ctx, ids); err != nil {
		return
	}
	for _, id := range ids {
		if ctx.Err() != nil {
			return
		}
		entry, exists := s.cachedState(id)
		qualified := exists && cfg.TicketQualified(entry.state, entry.model, entry.expiresAt, entry.revoked, s.now())
		move, moveErr := r.ReconcileTransfer(ctx, cfg, id, entry.recordID, qualified)
		if moveErr == nil && move != nil && s.syncTransfer != nil {
			moveErr = s.syncTransfer(ctx, id, []int64{cfg.TransferReadyGroupID, cfg.TransferRecoveryGroupID})
		}
		s.mu.Lock()
		if moveErr != nil {
			s.transferErrors[id] = moveErr.Error()
		} else {
			delete(s.transferErrors, id)
		}
		s.mu.Unlock()
		if moveErr != nil {
			s.noteError(fmt.Sprintf("账号 %d 移组/同步失败：%v", id, moveErr))
		}
	}
}

func (s *CodexTurnStateService) enrichTransferOverview(ctx context.Context, cfg CodexTurnStateConfig, rows []CodexTurnStateAccountRow, out *CodexTurnStateOverview) {
	if r, ok := s.repo.(CodexTurnStateTransferRepository); ok {
		var err error
		out.Transfers, err = r.RecentTransfers(ctx)
		if err != nil {
			s.noteError("读取移组记录失败：" + err.Error())
		}
	}
	s.jobsMu.Lock()
	defer s.jobsMu.Unlock()
	s.mu.RLock()
	defer s.mu.RUnlock()
	for i := range rows {
		row := &rows[i]
		entry, ok := s.cache[row.AccountID]
		row.TicketQualified = ok && cfg.TicketQualified(entry.state, entry.model, entry.expiresAt, entry.revoked, s.now())
		row.CurrentGroupIDs = s.transferMembers[row.AccountID]
		row.TransferError = s.transferErrors[row.AccountID]
		if len(row.CurrentGroupIDs) > 0 && cfg.TransferActive() {
			row.TransferStatus = "待恢复"
			for _, groupID := range row.CurrentGroupIDs {
				if groupID == cfg.TransferReadyGroupID {
					row.TransferStatus = "可用组"
				}
			}
		}
		at := s.cycleRetryAt[row.AccountID]
		if upstream := s.retryAfter[row.AccountID]; upstream.After(at) {
			at = upstream
		}
		if at.After(s.now()) {
			row.NextRetryAt = &at
			if row.CollectionStatus != "locked" {
				row.CollectionStatus = "cooldown"
			}
		}
	}
}
