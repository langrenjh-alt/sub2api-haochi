package repository

import (
	"context"
	"database/sql"
	"errors"
	"slices"

	"github.com/Wei-Shaw/sub2api/internal/service"
)

// Both policies share the account lock, so concurrent verdicts from different
// source groups recheck membership after an earlier move has committed. A late
// result from a group the account has left cannot move or pause it again.
func (r *degradationRepository) applyDegradedProbeOutcome(ctx context.Context, accountID, groupID int64, suspendMinutes int, note string) (bool, error) {
	return r.applyDegradedOutcome(ctx, accountID, groupID, suspendMinutes, note, false)
}

func (r *degradationRepository) applyDegradedOutcome(ctx context.Context, accountID, groupID int64, suspendMinutes int, note string, requireResponseMatch bool) (bool, error) {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer func() { _ = tx.Rollback() }()

	var lockedID int64
	err = tx.QueryRowContext(ctx, `SELECT id FROM accounts WHERE id=$1 AND deleted_at IS NULL AND schedulable=TRUE FOR UPDATE`, accountID).Scan(&lockedID)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}

	// Keep the detector switch stable until commit. Do not use the earlier
	// service lookup or the queued snapshot to authorize a destructive move.
	var enabled bool
	var raw []byte
	err = tx.QueryRowContext(ctx, `SELECT degradation_detection_enabled, degradation_detection_config FROM groups WHERE id=$1 AND deleted_at IS NULL FOR SHARE`, groupID).Scan(&enabled, &raw)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if !enabled {
		return false, nil
	}
	cfg := service.NormalizeDegradationConfig(decodeDegradationConfig(raw))
	if requireResponseMatch {
		hits, err := degradationResponseHits(ctx, tx, accountID)
		if err != nil {
			return false, err
		}
		if hits < 5 {
			return false, nil
		}
		suspendMinutes = cfg.SuspendMinute
		note = degradationResponseNote(groupID, hits, suspendMinutes)
	}
	if cfg.MoveOnDegraded {
		if cfg.MoveTargetGroupID <= 0 || cfg.MoveTargetGroupID == groupID {
			return false, service.ErrDegradationMoveTargetInvalid
		}
		// Lock the destination before join rows, matching group deletion's
		// group-then-membership order even when the target is an old member.
		if err := lockLiveGroups(ctx, tx, []int64{cfg.MoveTargetGroupID}); err != nil {
			return false, err
		}
	}

	rows, err := tx.QueryContext(ctx, `SELECT group_id FROM account_groups WHERE account_id=$1 ORDER BY group_id FOR UPDATE`, accountID)
	if err != nil {
		return false, err
	}
	var previousGroupIDs []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			_ = rows.Close()
			return false, err
		}
		previousGroupIDs = append(previousGroupIDs, id)
	}
	readErr := rows.Err()
	closeErr := rows.Close()
	if readErr != nil {
		return false, readErr
	}
	if closeErr != nil {
		return false, closeErr
	}
	if !slices.Contains(previousGroupIDs, groupID) {
		return false, nil
	}

	if cfg.MoveOnDegraded {
		target := cfg.MoveTargetGroupID
		if _, err := tx.ExecContext(ctx, `DELETE FROM account_groups WHERE account_id=$1`, accountID); err != nil {
			return false, err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO account_groups(account_id,group_id,priority,created_at) VALUES($1,$2,1,NOW())`, accountID, target); err != nil {
			return false, err
		}
		// Move-only mode clears old detector pauses but never clears another
		// subsystem's cooldown, rate limit, error, or manual disable.
		if _, err := tx.ExecContext(ctx, `
UPDATE accounts SET
 temp_unschedulable_until=CASE WHEN temp_unschedulable_reason LIKE $2 THEN NULL ELSE temp_unschedulable_until END,
 temp_unschedulable_reason=CASE WHEN temp_unschedulable_reason LIKE $2 THEN '' ELSE temp_unschedulable_reason END,
 degradation_suspended_until=NULL, degradation_suspended_at=NULL, degradation_suspend_note='',
 updated_at=NOW()
WHERE id=$1`, accountID, service.DegradationSuspendReasonPrefix+"%"); err != nil {
			return false, err
		}
		payload := buildSchedulerGroupPayload(mergeGroupIDs(previousGroupIDs, []int64{target}))
		if err := enqueueSchedulerOutbox(ctx, tx, service.SchedulerOutboxEventAccountGroupsChanged, &accountID, nil, payload); err != nil {
			return false, err
		}
		if err := tx.Commit(); err != nil {
			return false, err
		}
		return true, nil
	}

	// Feature disabled: retain the existing pause policy verbatim.
	result, err := tx.ExecContext(ctx, `
UPDATE accounts SET temp_unschedulable_until=NOW()+make_interval(mins=>$2::int),
 temp_unschedulable_reason=$3, degradation_suspended_until=NOW()+make_interval(mins=>$2::int),
 degradation_suspended_at=NOW(), degradation_suspend_note=$3, updated_at=NOW()
WHERE id=$1 AND (temp_unschedulable_until IS NULL OR temp_unschedulable_until<=NOW() OR temp_unschedulable_reason LIKE $4)`,
		accountID, suspendMinutes, note, service.DegradationSuspendReasonPrefix+"%")
	if err != nil {
		return false, err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return false, err
	}
	if affected > 0 {
		if err := enqueueSchedulerOutbox(ctx, tx, service.SchedulerOutboxEventAccountChanged, &accountID, nil, nil); err != nil {
			return false, err
		}
	}
	if err := tx.Commit(); err != nil {
		return false, err
	}
	return affected == 1, nil
}
