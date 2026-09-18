package repository

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
)

func (r *codexTurnStateRepository) TransferMembers(ctx context.Context, cfg service.CodexTurnStateConfig) (map[int64][]int64, error) {
	rows, err := r.db.QueryContext(ctx, `
SELECT ag.account_id,ag.group_id FROM account_groups ag
JOIN accounts a ON a.id=ag.account_id
WHERE a.deleted_at IS NULL AND a.platform='openai'
AND EXISTS(SELECT 1 FROM account_groups p WHERE p.account_id=a.id AND p.group_id=ANY($1))
ORDER BY ag.account_id,ag.group_id`, int64Array([]int64{cfg.TransferReadyGroupID, cfg.TransferRecoveryGroupID}))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[int64][]int64{}
	for rows.Next() {
		var id, group int64
		if err := rows.Scan(&id, &group); err != nil {
			return nil, err
		}
		out[id] = append(out[id], group)
	}
	return out, rows.Err()
}

// All settings writers that can enable either transfer policy use the same
// advisory lock. Rows are rechecked in the transaction, never from a queued job.
func validateTicketTransferGroups(ctx context.Context, tx *sql.Tx, cfg service.CodexTurnStateConfig) error {
	if !cfg.TransferActive() {
		return nil
	}
	for _, id := range []int64{cfg.TransferReadyGroupID, cfg.TransferRecoveryGroupID} {
		var platform, status string
		var enabled bool
		var raw []byte
		err := tx.QueryRowContext(ctx, `SELECT platform,status,degradation_detection_enabled,degradation_detection_config
FROM groups WHERE id=$1 AND deleted_at IS NULL FOR SHARE`, id).Scan(&platform, &status, &enabled, &raw)
		if err != nil {
			return fmt.Errorf("移组分组 #%d 不存在或读取失败: %w", id, err)
		}
		if platform != "openai" || status != service.StatusActive {
			return fmt.Errorf("分组 #%d 必须为启用的 OpenAI 分组", id)
		}
		degrade := decodeDegradationConfig(raw)
		if enabled && degrade.MoveOnDegraded {
			return fmt.Errorf("分组 #%d 已启用降智自动移组，请先关闭该移组规则", id)
		}
	}
	return nil
}

func (r *codexTurnStateRepository) ReconcileTransfer(ctx context.Context, expected service.CodexTurnStateConfig, accountID, expectedRecordID int64, expectedQualified bool) (*service.CodexTurnStateTransfer, error) {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(247000)`); err != nil {
		return nil, err
	}
	var locked int64
	err = tx.QueryRowContext(ctx, `SELECT id FROM accounts WHERE id=$1 AND deleted_at IS NULL AND platform='openai' FOR UPDATE`, accountID).Scan(&locked)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var raw []byte
	if err = tx.QueryRowContext(ctx, `SELECT value FROM settings WHERE key=$1 FOR SHARE`, service.SettingKeyCodexTurnStateConfig).Scan(&raw); err != nil {
		return nil, err
	}
	var cfg service.CodexTurnStateConfig
	if err = json.Unmarshal(raw, &cfg); err != nil {
		return nil, err
	}
	cfg = service.NormalizeCodexTurnStateConfig(cfg)
	if !cfg.TransferActive() || !reflect.DeepEqual(cfg, expected) {
		return nil, nil
	}
	if err = validateTicketTransferGroups(ctx, tx, cfg); err != nil {
		return nil, err
	}
	rows, err := tx.QueryContext(ctx, `SELECT group_id,priority FROM account_groups WHERE account_id=$1 ORDER BY group_id FOR UPDATE`, accountID)
	if err != nil {
		return nil, err
	}
	var groups []int64
	priorities := map[int64]int{}
	for rows.Next() {
		var group int64
		var priority int
		if err = rows.Scan(&group, &priority); err != nil {
			rows.Close()
			return nil, err
		}
		groups = append(groups, group)
		priorities[group] = priority
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	if !slices.Contains(groups, cfg.TransferReadyGroupID) && !slices.Contains(groups, cfg.TransferRecoveryGroupID) {
		return nil, nil
	}

	// SQL rechecks both the bound pin and any later revocation. A successful
	// concurrent write not yet loaded into memory delays the move, never demotes.
	var id int64
	var state, model string
	var expires time.Time
	err = tx.QueryRowContext(ctx, `SELECT id,state,model,expires_at FROM codex_turn_states s
WHERE account_id=$1 AND status='active' AND length(state) IN(292,332) AND expires_at>NOW()
AND id>COALESCE((SELECT MAX(id) FROM codex_turn_states WHERE account_id=$1 AND status='revoked'),0)
ORDER BY id DESC LIMIT 1`, accountID).Scan(&id, &state, &model, &expires)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}
	qualified := err == nil && cfg.TicketQualified(state, model, expires, false, time.Now())
	if qualified && (!expectedQualified || expectedRecordID != id) {
		return nil, nil
	}
	to, from, reason := cfg.TransferRecoveryGroupID, cfg.TransferReadyGroupID, "没有符合要求的有效绑定票据"
	if qualified {
		to, from, reason = cfg.TransferReadyGroupID, cfg.TransferRecoveryGroupID, "有效票据已持久化并绑定"
	}
	if slices.Contains(groups, to) && !slices.Contains(groups, from) {
		return nil, nil
	}
	priority, ok := priorities[to]
	if !ok {
		priority = priorities[from]
	}
	if _, err = tx.ExecContext(ctx, `DELETE FROM account_groups WHERE account_id=$1 AND group_id=$2`, accountID, from); err != nil {
		return nil, err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO account_groups(account_id,group_id,priority,created_at) VALUES($1,$2,$3,NOW()) ON CONFLICT(account_id,group_id) DO NOTHING`, accountID, to, priority); err != nil {
		return nil, err
	}
	move := &service.CodexTurnStateTransfer{AccountID: accountID, FromGroupID: from, ToGroupID: to, Reason: reason}
	if qualified {
		move.TicketRecordID = id
	}
	if err = tx.QueryRowContext(ctx, `INSERT INTO codex_turn_state_transfers(account_id,from_group_id,to_group_id,ticket_record_id,reason) VALUES($1,$2,$3,$4,$5) RETURNING id,created_at`,
		accountID, from, to, move.TicketRecordID, reason).Scan(&move.ID, &move.CreatedAt); err != nil {
		return nil, err
	}
	if err = enqueueSchedulerOutbox(ctx, tx, service.SchedulerOutboxEventAccountGroupsChanged, &accountID, nil, buildSchedulerGroupPayload(mergeGroupIDs(groups, []int64{to}))); err != nil {
		return nil, err
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	return move, nil
}

func (r *codexTurnStateRepository) RecentTransfers(ctx context.Context) ([]service.CodexTurnStateTransfer, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT id,account_id,from_group_id,to_group_id,ticket_record_id,reason,created_at FROM codex_turn_state_transfers ORDER BY id DESC LIMIT 50`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []service.CodexTurnStateTransfer{}
	for rows.Next() {
		var item service.CodexTurnStateTransfer
		if err := rows.Scan(&item.ID, &item.AccountID, &item.FromGroupID, &item.ToGroupID, &item.TicketRecordID, &item.Reason, &item.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

func guardDegradationTicketTransfer(ctx context.Context, tx *sql.Tx, groupID int64, cfg service.DegradationDetectionConfig) error {
	if !cfg.Enabled || !cfg.MoveOnDegraded {
		return nil
	}
	var raw []byte
	err := tx.QueryRowContext(ctx, `SELECT value FROM settings WHERE key=$1 FOR SHARE`, service.SettingKeyCodexTurnStateConfig).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	var ticket service.CodexTurnStateConfig
	if err = json.Unmarshal(raw, &ticket); err != nil {
		return err
	}
	if ticket.TransferActive() && (groupID == ticket.TransferReadyGroupID || groupID == ticket.TransferRecoveryGroupID) {
		return fmt.Errorf("该分组已启用票据双向移组，请先停用票据移组")
	}
	return nil
}
