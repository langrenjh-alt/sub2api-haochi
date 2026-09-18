package repository

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/lib/pq"

	"github.com/Wei-Shaw/sub2api/internal/service"
)

// codexTurnStateRepository stores the Codex turn-state pool.
//
// The configuration lives in the generic settings table as a single JSON row,
// which is the established pattern for an admin-level JSON config. The state
// observations get their own table because they are an append-only timeline with
// an expiry index, not a key/value pair.
type codexTurnStateRepository struct {
	db *sql.DB
}

func NewCodexTurnStateRepository(db *sql.DB) service.CodexTurnStateRepository {
	return &codexTurnStateRepository{db: db}
}

const codexTurnStateColumns = `id, account_id, state, state_fingerprint, status, source,
       http_status, model, COALESCE(proxy_id, 0), latency_ms, issued_at, expires_at, error, created_at`

func (r *codexTurnStateRepository) Config(ctx context.Context) (service.CodexTurnStateConfig, error) {
	var raw string
	err := r.db.QueryRowContext(ctx, `SELECT value FROM settings WHERE key = $1`, service.SettingKeyCodexTurnStateConfig).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return service.NormalizeCodexTurnStateConfig(service.CodexTurnStateConfig{}), nil
	}
	if err != nil {
		return service.CodexTurnStateConfig{}, err
	}
	cfg := service.CodexTurnStateConfig{}
	if len(raw) > 0 {
		if err := json.Unmarshal([]byte(raw), &cfg); err != nil {
			// A corrupt row must not disable the feature silently; fall back to
			// defaults and let the next save overwrite it.
			return service.NormalizeCodexTurnStateConfig(service.CodexTurnStateConfig{}), nil
		}
	}
	return service.NormalizeCodexTurnStateConfig(cfg), nil
}

func (r *codexTurnStateRepository) SaveConfig(ctx context.Context, _ int64, cfg service.CodexTurnStateConfig) error {
	encoded, err := json.Marshal(cfg)
	if err != nil {
		return err
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(247000)`); err != nil {
		return err
	}
	if err = validateTicketTransferGroups(ctx, tx, cfg); err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `
INSERT INTO settings (key, value, updated_at) VALUES ($1, $2, NOW())
ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value, updated_at = NOW()`,
		service.SettingKeyCodexTurnStateConfig, string(encoded))
	if err != nil {
		return err
	}
	return tx.Commit()
}

func (r *codexTurnStateRepository) LatestPerAccount(ctx context.Context, accountIDs []int64) (map[int64]service.CodexTurnStateRecord, error) {
	out := map[int64]service.CodexTurnStateRecord{}
	if len(accountIDs) == 0 {
		return out, nil
	}
	rows, err := r.db.QueryContext(ctx, `
SELECT DISTINCT ON (account_id) `+codexTurnStateColumns+`
FROM codex_turn_states s
WHERE account_id = ANY($1)
ORDER BY account_id,
  CASE WHEN status = 'active' AND length(state) IN (292,332) AND expires_at > NOW()
    AND id > COALESCE((SELECT MAX(r.id) FROM codex_turn_states r
                      WHERE r.account_id = s.account_id AND r.status = 'revoked'), 0)
    THEN 0 ELSE 1 END,
  id DESC`, int64Array(accountIDs))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		record, err := scanCodexTurnState(rows)
		if err != nil {
			return nil, err
		}
		out[record.AccountID] = record
	}
	return out, rows.Err()
}

func (r *codexTurnStateRepository) Insert(ctx context.Context, record *service.CodexTurnStateRecord) (int64, error) {
	if record == nil {
		return 0, errors.New("nil codex turn state record")
	}
	var proxyID any
	if record.ProxyID > 0 {
		proxyID = record.ProxyID
	}
	var id int64
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	if err = lockTurnStateAccount(ctx, tx, record.AccountID); err != nil {
		return 0, err
	}
	err = tx.QueryRowContext(ctx, `
INSERT INTO codex_turn_states
  (account_id, state, state_fingerprint, status, source, http_status, model, proxy_id, latency_ms, issued_at, expires_at, error)
VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12)
RETURNING id`,
		record.AccountID, record.State, record.StateFingerprint, record.Status, record.Source,
		record.HTTPStatus, record.Model, proxyID, record.LatencyMS, record.IssuedAt, record.ExpiresAt, record.Error,
	).Scan(&id)
	if err != nil {
		return 0, err
	}
	if err = tx.Commit(); err != nil {
		return 0, err
	}
	record.ID = id
	return id, nil
}

func (r *codexTurnStateRepository) Touch(ctx context.Context, id int64, expiresAt time.Time) error {
	if id <= 0 {
		return nil
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var accountID int64
	if err = tx.QueryRowContext(ctx, `SELECT account_id FROM codex_turn_states WHERE id=$1`, id).Scan(&accountID); err != nil {
		return err
	}
	if err = lockTurnStateAccount(ctx, tx, accountID); err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `UPDATE codex_turn_states SET expires_at = $2, status = $3 WHERE id = $1`,
		id, expiresAt, service.CodexTurnStateStatusActive)
	if err != nil {
		return err
	}
	return tx.Commit()
}

// Invalidate appends a revoked marker instead of mutating history, so the
// operator-visible timeline keeps showing when and why a state was dropped.
func (r *codexTurnStateRepository) Invalidate(ctx context.Context, accountID int64, note string) error {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err = lockTurnStateAccount(ctx, tx, accountID); err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `
INSERT INTO codex_turn_states (account_id, state, state_fingerprint, status, source, error)
VALUES ($1, '', '', $2, $3, $4)`,
		accountID, service.CodexTurnStateStatusRevoked, service.CodexTurnStateSourceHarvest, note)
	if err != nil {
		return err
	}
	return tx.Commit()
}

// Shared with group reconciliation and WishTeam's account replacement lock.
// A probe finishing after revival cannot recreate a pin for the deleted ID.
func lockTurnStateAccount(ctx context.Context, tx *sql.Tx, id int64) error {
	var locked int64
	return tx.QueryRowContext(ctx, `SELECT id FROM accounts WHERE id=$1 AND deleted_at IS NULL FOR UPDATE`, id).Scan(&locked)
}

func (r *codexTurnStateRepository) ExpireStale(ctx context.Context, now time.Time) (int64, error) {
	result, err := r.db.ExecContext(ctx, `
UPDATE codex_turn_states SET status = $2
WHERE status = $1 AND expires_at IS NOT NULL AND expires_at <= $3`,
		service.CodexTurnStateStatusActive, service.CodexTurnStateStatusExpired, now)
	if err != nil {
		return 0, err
	}
	return result.RowsAffected()
}

func (r *codexTurnStateRepository) History(ctx context.Context, accountID int64, page, pageSize int) (*service.CodexTurnStateHistoryPage, error) {
	out := &service.CodexTurnStateHistoryPage{Page: page, PageSize: pageSize, Items: []service.CodexTurnStateRecord{}}
	filter := ""
	args := []any{}
	if accountID > 0 {
		filter = " WHERE s.account_id = $1"
		args = append(args, accountID)
	}
	if err := r.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM codex_turn_states s`+filter, args...).Scan(&out.Total); err != nil {
		return nil, err
	}
	query := fmt.Sprintf(`
SELECT s.id, s.account_id, COALESCE(a.name, ''), s.state, s.state_fingerprint, s.status, s.source,
       s.http_status, s.model, COALESCE(s.proxy_id, 0), s.latency_ms, s.issued_at, s.expires_at, s.error, s.created_at
FROM codex_turn_states s
LEFT JOIN accounts a ON a.id = s.account_id%s
ORDER BY s.id DESC
LIMIT $%d OFFSET $%d`, filter, len(args)+1, len(args)+2)
	args = append(args, pageSize, (page-1)*pageSize)
	rows, err := r.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		record, err := scanCodexTurnStateWithName(rows)
		if err != nil {
			return nil, err
		}
		out.Items = append(out.Items, record)
	}
	return out, rows.Err()
}

func (r *codexTurnStateRepository) CountersSince(ctx context.Context, since time.Time) (int64, int64, error) {
	var probes, harvests int64
	err := r.db.QueryRowContext(ctx, `
SELECT COUNT(*) FILTER (WHERE source IN ($3,$4)),
       COUNT(*) FILTER (WHERE source = $5 AND state_fingerprint <> '')
FROM codex_turn_states WHERE created_at >= $1 AND status <> $2`,
		since, service.CodexTurnStateStatusExpired, service.CodexTurnStateSourceProbe, service.CodexTurnStateSourceManual, service.CodexTurnStateSourceHarvest,
	).Scan(&probes, &harvests)
	return probes, harvests, err
}

// Prune trims the timeline: it keeps the newest N rows per account and drops
// anything older than the retention window, without ever touching the row the
// pool still injects.
func (r *codexTurnStateRepository) Prune(ctx context.Context, keepPerAccount int, olderThan time.Time) (int64, error) {
	if keepPerAccount < 1 {
		keepPerAccount = 1
	}
	result, err := r.db.ExecContext(ctx, `
DELETE FROM codex_turn_states
WHERE (created_at < $1
  OR id NOT IN (
    SELECT id FROM (
      SELECT id, ROW_NUMBER() OVER (PARTITION BY account_id ORDER BY id DESC) AS rn
      FROM codex_turn_states
    ) ranked WHERE ranked.rn <= $3
  ))
  AND status <> $2
  AND id NOT IN (
    SELECT MAX(id) FROM codex_turn_states WHERE status = 'revoked' GROUP BY account_id
  )`, olderThan, service.CodexTurnStateStatusActive, keepPerAccount)
	if err != nil {
		return 0, err
	}
	return result.RowsAffected()
}

// EnrolledAccounts resolves the selected groups to accounts that can serve
// traffic. Accounts that are deleted, paused or not schedulable are skipped:
// collecting a state for them would spend quota nobody can use.
func (r *codexTurnStateRepository) EnrolledAccounts(ctx context.Context, groupIDs []int64) (map[int64]int64, error) {
	out := map[int64]int64{}
	if len(groupIDs) == 0 {
		return out, nil
	}
	rows, err := r.db.QueryContext(ctx, `
SELECT ag.account_id, MIN(ag.group_id)
FROM account_groups ag
JOIN accounts a ON a.id = ag.account_id
WHERE ag.group_id = ANY($1)
  AND a.deleted_at IS NULL
  AND a.status = 'active'
  AND a.schedulable
  AND a.platform = 'openai'
  AND a.type IN ('oauth', 'setup-token')
  AND COALESCE(a.credentials->>'access_token', '') <> ''
GROUP BY ag.account_id`, int64Array(groupIDs))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var accountID, groupID int64
		if err := rows.Scan(&accountID, &groupID); err != nil {
			return nil, err
		}
		out[accountID] = groupID
	}
	return out, rows.Err()
}

// int64Array adapts IDs for lib/pq's ANY() handling.
func int64Array(values []int64) any {
	if len(values) == 0 {
		return pq.Array([]int64{})
	}
	sorted := make([]int64, len(values))
	copy(sorted, values)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })
	return pq.Array(sorted)
}

func scanCodexTurnState(rows *sql.Rows) (service.CodexTurnStateRecord, error) {
	var record service.CodexTurnStateRecord
	err := rows.Scan(
		&record.ID, &record.AccountID, &record.State, &record.StateFingerprint, &record.Status, &record.Source,
		&record.HTTPStatus, &record.Model, &record.ProxyID, &record.LatencyMS,
		&record.IssuedAt, &record.ExpiresAt, &record.Error, &record.CreatedAt,
	)
	record.StateLength = len(record.State)
	return record, err
}

func scanCodexTurnStateWithName(rows *sql.Rows) (service.CodexTurnStateRecord, error) {
	var record service.CodexTurnStateRecord
	err := rows.Scan(
		&record.ID, &record.AccountID, &record.AccountName, &record.State, &record.StateFingerprint, &record.Status, &record.Source,
		&record.HTTPStatus, &record.Model, &record.ProxyID, &record.LatencyMS,
		&record.IssuedAt, &record.ExpiresAt, &record.Error, &record.CreatedAt,
	)
	record.StateLength = len(record.State)
	return record, err
}
