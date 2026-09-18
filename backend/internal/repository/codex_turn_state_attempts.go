package repository

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
)

func (r *codexTurnStateRepository) AttemptCounts(ctx context.Context, ids []int64) (map[int64]int, error) {
	result := make(map[int64]int)
	if len(ids) == 0 {
		return result, nil
	}
	rows, err := r.db.QueryContext(ctx, `SELECT account_id, attempts FROM codex_turn_state_attempts WHERE account_id=ANY($1)`, int64Array(ids))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id int64
		var count int
		if err := rows.Scan(&id, &count); err != nil {
			return nil, err
		}
		result[id] = count
	}
	return result, rows.Err()
}

func (r *codexTurnStateRepository) ReserveAttempt(ctx context.Context, id int64, limit int) (int, error) {
	var count int
	err := r.db.QueryRowContext(ctx, `
INSERT INTO codex_turn_state_attempts(account_id, attempts) VALUES ($1, 1)
ON CONFLICT (account_id) DO UPDATE
SET attempts=codex_turn_state_attempts.attempts+1, updated_at=NOW()
WHERE codex_turn_state_attempts.attempts < $2
RETURNING attempts`, id, limit).Scan(&count)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	return count, err
}

func (r *codexTurnStateRepository) ResetAttempts(ctx context.Context, id int64) error {
	_, err := r.db.ExecContext(ctx, `UPDATE codex_turn_state_attempts SET attempts=0,next_retry_at=NULL,updated_at=NOW() WHERE account_id=$1`, id)
	return err
}

func (r *codexTurnStateRepository) RefreshRetryWindows(ctx context.Context, ids []int64, limit, cooldown int, now time.Time) (map[int64]service.CodexTurnStateRetry, error) {
	out := map[int64]service.CodexTurnStateRetry{}
	if len(ids) == 0 {
		return out, nil
	}
	// Clearing an expired window and reserving attempts are atomic SQL updates,
	// so process restarts cannot silently reset a still-active window.
	resetRows, err := r.db.QueryContext(ctx, `UPDATE codex_turn_state_attempts SET attempts=0,next_retry_at=NULL,updated_at=$2
WHERE account_id=ANY($1) AND next_retry_at IS NOT NULL AND next_retry_at<=$2 RETURNING account_id`, int64Array(ids), now)
	if err != nil {
		return nil, err
	}
	restarted := map[int64]bool{}
	for resetRows.Next() {
		var id int64
		if err := resetRows.Scan(&id); err != nil {
			resetRows.Close()
			return nil, err
		}
		restarted[id] = true
	}
	err = resetRows.Err()
	resetRows.Close()
	if err != nil {
		return nil, err
	}
	_, err = r.db.ExecContext(ctx, `UPDATE codex_turn_state_attempts SET next_retry_at=$2::timestamptz+make_interval(secs=>$4)
WHERE account_id=ANY($1) AND attempts >= $3 AND next_retry_at IS NULL`, int64Array(ids), now, limit, cooldown)
	if err != nil {
		return nil, err
	}
	rows, err := r.db.QueryContext(ctx, `SELECT account_id,next_retry_at,upstream_retry_at FROM codex_turn_state_attempts WHERE account_id=ANY($1)`, int64Array(ids))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id int64
		var cycle, upstream sql.NullTime
		if err := rows.Scan(&id, &cycle, &upstream); err != nil {
			return nil, err
		}
		out[id] = service.CodexTurnStateRetry{CycleAt: cycle.Time, UpstreamAt: upstream.Time, Restarted: restarted[id]}
	}
	return out, rows.Err()
}

func (r *codexTurnStateRepository) SaveUpstreamRetry(ctx context.Context, id int64, at time.Time) error {
	_, err := r.db.ExecContext(ctx, `INSERT INTO codex_turn_state_attempts(account_id,upstream_retry_at) VALUES($1,$2)
ON CONFLICT(account_id) DO UPDATE SET upstream_retry_at=GREATEST(codex_turn_state_attempts.upstream_retry_at,EXCLUDED.upstream_retry_at)`, id, at)
	return err
}
