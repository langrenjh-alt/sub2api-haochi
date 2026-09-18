package wishteam

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/lib/pq"
)

const lockID = int64(871356205)

type Store struct{ db *sql.DB }

func (s *Store) Config(ctx context.Context) (Config, error) {
	var c Config
	err := s.db.QueryRowContext(ctx, `SELECT enabled,COALESCE(group_id,0),interval_minutes,next_run_at FROM wishteam_settings WHERE id=1`).
		Scan(&c.Enabled, &c.GroupID, &c.IntervalMinutes, &c.NextRunAt)
	return c, err
}

func (s *Store) Configure(ctx context.Context, c Config) error {
	if c.IntervalMinutes < 1 || c.IntervalMinutes > 1440 || c.GroupID <= 0 {
		return errors.New("请选择分组；巡查间隔须为 1–1440 分钟")
	}
	result, err := s.db.ExecContext(ctx, `
UPDATE wishteam_settings SET enabled=$1,group_id=$2,interval_minutes=$3,
next_run_at=CASE WHEN NOT enabled AND $1 THEN NOW() ELSE NOW()+make_interval(mins=>$3) END,updated_at=NOW()
WHERE id=1 AND EXISTS(SELECT 1 FROM groups WHERE id=$2 AND platform='openai' AND deleted_at IS NULL)`,
		c.Enabled, c.GroupID, c.IntervalMinutes)
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return errors.New("所选 OpenAI 分组不存在或已删除")
	}
	return nil
}

func (s *Store) Groups(ctx context.Context) ([]Group, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT g.id,g.name,count(a.id)
FROM groups g LEFT JOIN account_groups ag ON ag.group_id=g.id
LEFT JOIN accounts a ON a.id=ag.account_id AND a.deleted_at IS NULL AND a.platform='openai'
 AND a.type='oauth' AND a.parent_account_id IS NULL
WHERE g.deleted_at IS NULL AND g.platform='openai' GROUP BY g.id,g.name ORDER BY g.id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Group{}
	for rows.Next() {
		var g Group
		if err := rows.Scan(&g.ID, &g.Name, &g.Count); err != nil {
			return nil, err
		}
		out = append(out, g)
	}
	return out, rows.Err()
}

func (s *Store) StartRun(ctx context.Context, actor int64, scheduled bool) (int64, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	// Worker holds the session lock on a different connection. Its caller uses
	// startRunLocked; manual requests take the same advisory key transactionally.
	var locked bool
	if err = tx.QueryRowContext(ctx, `SELECT pg_try_advisory_xact_lock($1)`, lockID).Scan(&locked); err != nil {
		return 0, err
	}
	if !locked {
		return 0, ErrBusy
	}
	return s.startRunTx(ctx, tx, actor, scheduled)
}

func (s *Store) startRunLocked(ctx context.Context) (int64, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	return s.startRunTx(ctx, tx, 0, true)
}

func (s *Store) startRunTx(ctx context.Context, tx *sql.Tx, actor int64, scheduled bool) (int64, error) {
	var groupID int64
	var enabled bool
	var due bool
	if err := tx.QueryRowContext(ctx, `SELECT COALESCE(group_id,0),enabled,next_run_at<=NOW() FROM wishteam_settings WHERE id=1 FOR UPDATE`).
		Scan(&groupID, &enabled, &due); err != nil {
		return 0, err
	}
	if scheduled && (!enabled || !due) {
		return 0, nil
	}
	var active bool
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM wishteam_runs WHERE status='running')`).Scan(&active); err != nil {
		return 0, err
	}
	if active {
		return 0, ErrBusy
	}
	var valid bool
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM groups WHERE id=$1 AND platform='openai' AND deleted_at IS NULL)`, groupID).Scan(&valid); err != nil {
		return 0, err
	}
	if !valid {
		return 0, errors.New("请先保存有效的 OpenAI 监控分组")
	}
	var runID int64
	if err := tx.QueryRowContext(ctx, `INSERT INTO wishteam_runs(group_id,requested_by) VALUES($1,NULLIF($2,0)) RETURNING id`, groupID, actor).Scan(&runID); err != nil {
		return 0, err
	}
	rows, err := tx.QueryContext(ctx, `SELECT a.id,to_jsonb(a) FROM accounts a JOIN account_groups ag ON ag.account_id=a.id
WHERE ag.group_id=$1 AND a.deleted_at IS NULL AND a.platform='openai' AND a.type='oauth'
AND a.parent_account_id IS NULL ORDER BY a.id`, groupID)
	if err != nil {
		return 0, err
	}
	type entry struct {
		id    int64
		raw   []byte
		email string
	}
	entries := []entry{}
	seen := map[string]int{}
	for rows.Next() {
		var e entry
		var a map[string]any
		if err := rows.Scan(&e.id, &e.raw); err != nil {
			rows.Close()
			return 0, err
		}
		if err := json.Unmarshal(e.raw, &a); err != nil {
			rows.Close()
			return 0, err
		}
		e.email = emailOf(a)
		seen[e.email]++
		entries = append(entries, e)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return 0, err
	}
	coolingEmails := map[string]bool{}
	coolingRows, err := tx.QueryContext(ctx, `SELECT DISTINCT email FROM wishteam_items
WHERE retry_after>0 AND updated_at+make_interval(secs=>retry_after)>NOW()`)
	if err != nil {
		return 0, err
	}
	for coolingRows.Next() {
		var email string
		if err = coolingRows.Scan(&email); err != nil {
			coolingRows.Close()
			return 0, err
		}
		coolingEmails[email] = true
	}
	err = coolingRows.Err()
	coolingRows.Close()
	if err != nil {
		return 0, err
	}
	// COPY buffers large groups instead of making one database roundtrip per
	// account; the surrounding transaction still makes run creation all-or-none.
	copyRows, err := tx.PrepareContext(ctx, pq.CopyIn("wishteam_items", "run_id", "account_id", "email", "status", "message", "snapshot"))
	if err != nil {
		return 0, err
	}
	defer copyRows.Close()
	for _, e := range entries {
		status, message := "queued", ""
		if e.email == "" {
			status, message = "skipped", "缺少有效邮箱，未提交"
		}
		if e.email != "" && seen[e.email] > 1 {
			status, message = "skipped", "同一分组存在重复邮箱，请先整理账号；未提交任何副本"
		}
		if coolingEmails[e.email] {
			status, message = "skipped", "上次结果要求等待 retry_after，本轮暂不提交"
		}
		if _, err = copyRows.ExecContext(ctx,
			runID, e.id, e.email, status, message, string(e.raw)); err != nil {
			return 0, err
		}
	}
	if _, err = copyRows.ExecContext(ctx); err != nil {
		return 0, err
	}
	if err = copyRows.Close(); err != nil {
		return 0, err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE wishteam_runs SET total=$2 WHERE id=$1`, runID, len(entries)); err != nil {
		return 0, err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE wishteam_settings SET next_run_at=NOW()+make_interval(mins=>interval_minutes) WHERE id=1`); err != nil {
		return 0, err
	}
	return runID, tx.Commit()
}

func (s *Store) Runs(ctx context.Context) ([]Run, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT r.id,r.group_id,r.status,r.total,r.message,r.created_at,r.finished_at,
count(i.id) FILTER(WHERE i.status IN('alive','replaced','revived','failed','skipped')),
count(i.id) FILTER(WHERE i.status='alive'),count(i.id) FILTER(WHERE i.status='replaced'),
count(i.id) FILTER(WHERE i.status='revived'),count(i.id) FILTER(WHERE i.status='failed'),
count(i.id) FILTER(WHERE i.status='skipped'),count(i.id) FILTER(WHERE i.error_code='workspace_deactivated')
FROM (SELECT * FROM wishteam_runs ORDER BY id DESC LIMIT 20) r
LEFT JOIN wishteam_items i ON i.run_id=r.id GROUP BY r.id,r.group_id,r.status,r.total,r.message,r.created_at,r.finished_at ORDER BY r.id DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Run{}
	for rows.Next() {
		var r Run
		if err = rows.Scan(&r.ID, &r.GroupID, &r.Status, &r.Total, &r.Message, &r.CreatedAt, &r.FinishedAt,
			&r.Done, &r.Alive, &r.Replaced, &r.Revived, &r.Failed, &r.Skipped, &r.Dead); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func (s *Store) Items(ctx context.Context, runID int64, page int) ([]Item, int, error) {
	var total int
	if err := s.db.QueryRowContext(ctx, `SELECT count(*) FROM wishteam_items WHERE run_id=$1`, runID).Scan(&total); err != nil {
		return nil, 0, err
	}
	rows, err := s.db.QueryContext(ctx, `SELECT i.id,i.account_id,i.new_account_id,i.email,i.status,i.stage,i.message,i.error_code,i.probe,i.retry_after,i.updated_at,
EXISTS(SELECT 1 FROM wishteam_archives a WHERE a.item_id=i.id)
FROM wishteam_items i WHERE i.run_id=$1 ORDER BY i.id LIMIT 50 OFFSET $2`, runID, (page-1)*50)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	out := []Item{}
	for rows.Next() {
		var i Item
		var raw []byte
		if err = rows.Scan(&i.ID, &i.AccountID, &i.NewAccountID, &i.Email, &i.Status, &i.Stage, &i.Message, &i.ErrorCode, &raw, &i.RetryAfter, &i.UpdatedAt, &i.Archived); err != nil {
			return nil, 0, err
		}
		if err = json.Unmarshal(raw, &i.Probe); err != nil {
			return nil, 0, err
		}
		out = append(out, i)
	}
	return out, total, rows.Err()
}

// replace performs archive -> delete -> import -> restore -> verify in exactly
// that order. No network requests occur inside this transaction.
func (s *Store) replace(ctx context.Context, itemID int64, fresh map[string]any, verdict RemoteItem, sub2 Document) error {
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelSerializable})
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var accountID, groupID int64
	var status string
	var submitted, raw []byte
	err = tx.QueryRowContext(ctx, `SELECT i.account_id,i.status,i.snapshot,r.group_id FROM wishteam_items i
JOIN wishteam_runs r ON r.id=i.run_id WHERE i.id=$1 FOR UPDATE OF i`, itemID).Scan(&accountID, &status, &submitted, &groupID)
	if err != nil {
		return err
	}
	if status == "revived" || status == "replaced" {
		return nil
	} // restart/idempotency
	err = tx.QueryRowContext(ctx, `SELECT to_jsonb(a) FROM accounts a WHERE a.id=$1 AND a.deleted_at IS NULL
AND EXISTS(SELECT 1 FROM account_groups ag JOIN groups g ON g.id=ag.group_id
 WHERE ag.account_id=a.id AND ag.group_id=$2 AND g.deleted_at IS NULL) FOR UPDATE`, accountID, groupID).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return errors.New("原账号已删除或已移出监控分组，未替换")
	}
	if err != nil {
		return err
	}
	var old, input map[string]any
	if err = json.Unmarshal(raw, &old); err != nil {
		return err
	}
	if err = json.Unmarshal(submitted, &input); err != nil {
		return err
	}
	// Settings may be edited during a remote job: preserve the newest settings,
	// but never overwrite credentials that changed since the submitted probe.
	for _, k := range authFields {
		a, _ := json.Marshal(object(old["credentials"])[k])
		b, _ := json.Marshal(object(input["credentials"])[k])
		if string(a) != string(b) {
			return errors.New("巡查期间凭据已被更新，保留当前账号，留待下轮检查")
		}
	}
	merged, err := replacement(old, fresh, verdict)
	if err != nil {
		return err
	}
	var groups, plans []byte
	if err = tx.QueryRowContext(ctx, `SELECT COALESCE(jsonb_agg(to_jsonb(ag)),'[]') FROM account_groups ag WHERE account_id=$1`, accountID).Scan(&groups); err != nil {
		return err
	}
	if err = tx.QueryRowContext(ctx, `SELECT COALESCE(jsonb_agg(to_jsonb(p)),'[]') FROM scheduled_test_plans p WHERE account_id=$1`, accountID).Scan(&plans); err != nil {
		return err
	}
	doc, _ := json.Marshal(sub2)
	if _, err = tx.ExecContext(ctx, `INSERT INTO wishteam_archives(item_id,old_account_id,account,groups,plans,sub2)
VALUES($1,$2,$3::jsonb,$4::jsonb,$5::jsonb,$6::jsonb)`, itemID, accountID, string(raw), string(groups), string(plans), string(doc)); err != nil {
		return err
	}
	// Same soft-delete semantics as the application's account repository.
	if _, err = tx.ExecContext(ctx, `DELETE FROM account_groups WHERE account_id=$1`, accountID); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE accounts SET deleted_at=NOW(),updated_at=NOW() WHERE id=$1`, accountID); err != nil {
		return err
	}
	columns, err := accountColumns(ctx, tx)
	if err != nil {
		return err
	}
	mergedRaw, _ := json.Marshal(merged)
	var newID int64
	query := `INSERT INTO accounts (` + strings.Join(columns, ",") + `) SELECT ` + strings.Join(columns, ",") + ` FROM jsonb_populate_record(NULL::accounts,$1::jsonb) RETURNING id`
	if err = tx.QueryRowContext(ctx, query, string(mergedRaw)).Scan(&newID); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO account_groups(account_id,group_id,priority,created_at)
SELECT $1,group_id,priority,created_at FROM jsonb_populate_recordset(NULL::account_groups,$2::jsonb)`, newID, string(groups)); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE scheduled_test_plans SET account_id=$1 WHERE account_id=$2`, newID, accountID); err != nil {
		return err
	}
	if err = inheritTurnState(ctx, tx, accountID, newID, old, fresh); err != nil {
		return err
	}
	// Spark shadows follow the new parent, preserving all their own settings.
	if _, err = tx.ExecContext(ctx, `UPDATE accounts SET parent_account_id=$1 WHERE parent_account_id=$2`, newID, accountID); err != nil {
		return err
	}
	var actual []byte
	if err = tx.QueryRowContext(ctx, `SELECT to_jsonb(a) FROM accounts a WHERE id=$1`, newID).Scan(&actual); err != nil {
		return err
	}
	var saved map[string]any
	if err = json.Unmarshal(actual, &saved); err != nil {
		return err
	}
	for _, column := range columns {
		key := strings.Trim(column, `"`)
		want, _ := json.Marshal(merged[key])
		got, _ := json.Marshal(saved[key])
		if string(want) != string(got) {
			return fmt.Errorf("恢复后字段 %s 校验失败，事务已撤销", key)
		}
	}
	var membershipsOK bool
	if err = tx.QueryRowContext(ctx, `SELECT
 (SELECT COALESCE(jsonb_agg(to_jsonb(ag)-'account_id' ORDER BY group_id),'[]') FROM account_groups ag WHERE account_id=$1)
 =
 (SELECT COALESCE(jsonb_agg(x-'account_id' ORDER BY (x->>'group_id')::bigint),'[]') FROM jsonb_array_elements($2::jsonb) x)`,
		newID, string(groups)).Scan(&membershipsOK); err != nil {
		return err
	}
	if !membershipsOK {
		return errors.New("分组关系恢复校验失败，事务已撤销")
	}
	var groupRows []map[string]any
	if err = json.Unmarshal(groups, &groupRows); err != nil {
		return err
	}
	ids := []int64{}
	for _, g := range groupRows {
		ids = append(ids, int64(g["group_id"].(float64)))
	}
	payload, _ := json.Marshal(map[string]any{"group_ids": ids})
	// Outbox is transactional, so the scheduler sees both removal and import,
	// including old group memberships needed for cache invalidation.
	if _, err = tx.ExecContext(ctx, `INSERT INTO scheduler_outbox(event_type,account_id,payload)
SELECT 'account_changed',id,$3::jsonb FROM accounts WHERE id IN($1,$2) OR parent_account_id=$2`, accountID, newID, string(payload)); err != nil {
		return err
	}
	localStatus := "revived"
	if verdict.Status == "alive" {
		localStatus = "replaced"
	}
	if _, err = tx.ExecContext(ctx, `UPDATE wishteam_items SET new_account_id=$2,status=$3,stage='done',message=$4,updated_at=NOW() WHERE id=$1`,
		itemID, newID, localStatus, verdictMessage(verdict)); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE wishteam_archives SET new_account_id=$2 WHERE item_id=$1`, itemID, newID); err != nil {
		return err
	}
	return tx.Commit()
}

func accountColumns(ctx context.Context, tx *sql.Tx) ([]string, error) {
	rows, err := tx.QueryContext(ctx, `SELECT attname FROM pg_attribute WHERE attrelid='accounts'::regclass
AND attnum>0 AND NOT attisdropped AND attgenerated='' AND attname NOT IN('id','created_at','updated_at','deleted_at') ORDER BY attnum`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []string{}
	for rows.Next() {
		var name string
		if err = rows.Scan(&name); err != nil {
			return nil, err
		}
		out = append(out, pq.QuoteIdentifier(name))
	}
	return out, rows.Err()
}

// No token-bearing JSON is exposed by any API handler.
func (s *Store) Archive(ctx context.Context, itemID int64) (map[string]any, error) {
	var raw, groups []byte
	var id, oldID, newID int64
	var created time.Time
	err := s.db.QueryRowContext(ctx, `SELECT id,old_account_id,new_account_id,account,groups,created_at FROM wishteam_archives WHERE item_id=$1`, itemID).
		Scan(&id, &oldID, &newID, &raw, &groups, &created)
	if err != nil {
		return nil, err
	}
	var a map[string]any
	var gs []map[string]any
	if err = json.Unmarshal(raw, &a); err != nil {
		return nil, err
	}
	if err = json.Unmarshal(groups, &gs); err != nil {
		return nil, err
	}
	settings := map[string]any{}
	for _, k := range []string{"name", "notes", "proxy_id", "proxy_fallback_origin_id", "concurrency", "priority", "rate_multiplier", "load_factor", "schedulable", "expires_at", "auto_pause_on_expired"} {
		settings[k] = a[k]
	}
	return map[string]any{"id": id, "old_account_id": oldID, "new_account_id": newID, "created_at": created,
		"settings": settings, "groups": gs, "all_fields_verified": true,
		"credentials_config_preserved": true, "extra_config_preserved": true}, nil
}
