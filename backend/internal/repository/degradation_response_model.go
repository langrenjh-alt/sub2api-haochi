package repository

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/Wei-Shaw/sub2api/internal/service"
)

// Take the account's whole last-ten window BEFORE filtering models. Missing
// response metadata and requests for other models occupy slots but never match.
// Read the upstream's declared model, not a client-facing rewritten model.
const degradationResponseWindowSQL = `
SELECT COUNT(*) FILTER (
 WHERE LOWER(BTRIM(COALESCE(NULLIF(BTRIM(requested_model), ''), model))) = 'gpt-6-astra'
 AND LOWER(BTRIM(COALESCE(upstream_response_model, ''))) IN ('luna', 'gpt-5.6-luna'))
FROM (
 SELECT requested_model, model, upstream_response_model
 FROM usage_logs WHERE account_id=$1
 ORDER BY created_at DESC, id DESC LIMIT 10
) recent`

func degradationResponseHits(ctx context.Context, tx *sql.Tx, accountID int64) (int, error) {
	var hits int
	err := tx.QueryRowContext(ctx, degradationResponseWindowSQL, accountID).Scan(&hits)
	return hits, err
}

// Sweep independently of the active probe queue. Existing account/time indexes
// bound each lateral lookup to ten records. A paused account is not repeatedly
// extended on each tick; after its cooldown expires the current window is checked
// again. Membership/configuration and the window are rechecked under account lock.
func (r *degradationRepository) ApplyResponseModelDegradation(ctx context.Context, limit int) (int, error) {
	if limit <= 0 {
		return 0, nil
	}
	rows, err := r.db.QueryContext(ctx, `
SELECT a.id, source.id
FROM accounts a
JOIN LATERAL (
 SELECT g.id, COALESCE(g.degradation_detection_config->>'move_on_degraded','false')='true' AS move_enabled
 FROM account_groups ag JOIN groups g ON g.id=ag.group_id
 WHERE ag.account_id=a.id AND g.deleted_at IS NULL AND g.degradation_detection_enabled
 ORDER BY g.id LIMIT 1
) source ON TRUE
JOIN LATERAL (
 SELECT COUNT(*) FILTER (
  WHERE LOWER(BTRIM(COALESCE(NULLIF(BTRIM(requested_model), ''), model)))='gpt-6-astra'
  AND LOWER(BTRIM(COALESCE(upstream_response_model, ''))) IN ('luna','gpt-5.6-luna')) AS hits
 FROM (
  SELECT requested_model, model, upstream_response_model FROM usage_logs
  WHERE account_id=a.id ORDER BY created_at DESC,id DESC LIMIT 10
 ) recent
) signal ON TRUE
WHERE a.deleted_at IS NULL AND a.schedulable=TRUE
 AND (a.temp_unschedulable_until IS NULL OR a.temp_unschedulable_until<=NOW() OR source.move_enabled)
 AND signal.hits>=5
ORDER BY a.id LIMIT $1`, limit)
	if err != nil {
		return 0, err
	}
	type candidate struct{ account, group int64 }
	var candidates []candidate
	for rows.Next() {
		var row candidate
		if err := rows.Scan(&row.account, &row.group); err != nil {
			_ = rows.Close()
			return 0, err
		}
		candidates = append(candidates, row)
	}
	readErr := rows.Err()
	closeErr := rows.Close()
	if readErr != nil {
		return 0, readErr
	}
	if closeErr != nil {
		return 0, closeErr
	}
	applied := 0
	var failures []error
	for _, row := range candidates {
		changed, err := r.applyDegradedOutcome(ctx, row.account, row.group, 0, "", true)
		if err != nil {
			failures = append(failures, fmt.Errorf("account %d group %d: %w", row.account, row.group, err))
			continue
		}
		if changed {
			applied++
		}
	}
	return applied, errors.Join(failures...)
}

func degradationResponseNote(groupID int64, hits, minutes int) string {
	return fmt.Sprintf("%s[分组%d]：最近10次已记录请求中%d次请求gpt-6-astra响应luna，暂停调度%d分钟",
		service.DegradationSuspendReasonPrefix, groupID, hits, minutes)
}
