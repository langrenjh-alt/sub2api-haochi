package repository

import "context"

func (r *codexTurnStateRepository) ClearHistory(ctx context.Context, cacheIDs []int64) (int64, int64, error) {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, 0, err
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, `
WITH revoked AS (
  SELECT account_id, MAX(id) AS id FROM codex_turn_states WHERE status='revoked' GROUP BY account_id
), live AS (
  SELECT DISTINCT ON (s.account_id) s.account_id, s.id
  FROM codex_turn_states s LEFT JOIN revoked r ON r.account_id=s.account_id
  WHERE s.status='active' AND length(s.state) IN (292,332) AND s.expires_at>NOW()
    AND s.id>COALESCE(r.id,0)
  ORDER BY s.account_id,s.id DESC
), keep AS (
  SELECT id FROM live
  UNION SELECT r.id FROM revoked r WHERE NOT EXISTS(SELECT 1 FROM live l WHERE l.account_id=r.account_id)
  UNION SELECT id FROM codex_turn_states WHERE id=ANY($1) AND status='active'
    AND length(state) IN (292,332) AND expires_at>NOW()
)
DELETE FROM codex_turn_states WHERE id NOT IN(SELECT id FROM keep)`, int64Array(cacheIDs))
	if err != nil {
		return 0, 0, err
	}
	deleted, err := result.RowsAffected()
	if err != nil {
		return 0, 0, err
	}
	var kept int64
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM codex_turn_states`).Scan(&kept); err != nil {
		return 0, 0, err
	}
	if err := tx.Commit(); err != nil {
		return 0, 0, err
	}
	return deleted, kept, nil
}
