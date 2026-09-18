package poolrunway

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"strconv"
	"sync"
	"time"
)

type Group struct {
	ID   int64  `json:"id"`
	Name string `json:"name"`
}
type Snapshot struct {
	Schema          string     `json:"schema_version"`
	Generated       *time.Time `json:"generated_at"`
	Next            time.Time  `json:"next_calculation_at"`
	Age             float64    `json:"age_seconds"`
	Stale           bool       `json:"stale"`
	Available       bool       `json:"available"`
	Interval        int        `json:"interval_seconds"`
	Group           Group      `json:"group"`
	Policy          string     `json:"forecast_policy"`
	Method          string     `json:"method_version"`
	CollectionError string     `json:"collection_error"`
	Data            *Result    `json:"data"`
	History         []Result   `json:"history"`
}
type Worker struct {
	db     *sql.DB
	mu     sync.RWMutex
	failed bool
	cancel context.CancelFunc
	done   chan struct{}
}

func New(db *sql.DB) *Worker { return &Worker{db: db, done: make(chan struct{})} }
func (w *Worker) Start() {
	ctx, cancel := context.WithCancel(context.Background())
	w.cancel = cancel
	go func() {
		defer close(w.done)
		// A startup within bucket tolerance may capture this boundary, never fill past gaps.
		if time.Since(time.Now().UTC().Truncate(Interval)) <= 30*time.Second {
			_ = w.Collect(ctx, time.Now().UTC())
		}
		for {
			next := time.Now().UTC().Truncate(Interval).Add(Interval)
			timer := time.NewTimer(time.Until(next))
			select {
			case <-ctx.Done():
				timer.Stop()
				return
			case <-timer.C:
				_ = w.Collect(ctx, time.Now().UTC())
			}
		}
	}()
}
func (w *Worker) Stop() {
	if w.cancel != nil {
		w.cancel()
		<-w.done
	}
}
func (w *Worker) group(ctx context.Context, q interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}) (Group, error) {
	id, _ := strconv.ParseInt(os.Getenv("POOL_RUNWAY_GROUP_ID"), 10, 64)
	name := os.Getenv("POOL_RUNWAY_GROUP_NAME")
	if name == "" {
		name = "正常组"
	}
	rows, err := q.QueryContext(ctx, `WITH selected AS (
SELECT COALESCE((SELECT group_id FROM pool_runway_settings WHERE id=1),$1::bigint) AS id)
SELECT g.id,g.name FROM groups g,selected s
WHERE g.deleted_at IS NULL AND g.platform='openai'
AND ((s.id>0 AND g.id=s.id) OR (s.id=0 AND g.name=$2))`, id, name)
	if err != nil {
		return Group{}, err
	}
	defer rows.Close()
	g := Group{}
	n := 0
	for rows.Next() {
		if err = rows.Scan(&g.ID, &g.Name); err != nil {
			return Group{}, err
		}
		n++
	}
	if err = rows.Err(); err != nil {
		return Group{}, err
	}
	if n != 1 {
		return Group{}, errGroupMissing
	}
	return g, nil
}

// Column whitelist: token presence is computed in SQL; token values never leave DB.
// chatgpt_account_id is the full routing workspace identity for this project's OAuth adapter.
const accountSQL = `
SELECT a.id,
 COALESCE(a.credentials->>'email',''),COALESCE(a.extra->>'email',a.extra->>'email_address',''),
 COALESCE(a.credentials->>'chatgpt_account_id',''),COALESCE(a.extra->>'chatgpt_account_id',a.extra->>'account_id',''),
 a.platform,a.type,a.status,COALESCE(a.credentials->>'plan_type',a.extra->>'plan_type','unknown'),
 a.parent_account_id IS NOT NULL,a.schedulable,
 COALESCE(jsonb_typeof(a.credentials->'access_token')='string' AND length(trim(a.credentials->>'access_token'))>0,false),
 CASE WHEN a.extra->>'proxy_mode'='random' THEN EXISTS(SELECT 1 FROM proxies rp WHERE rp.deleted_at IS NULL AND rp.status='active' AND (rp.expires_at IS NULL OR rp.expires_at>$2))
 ELSE a.proxy_id IS NULL OR (p.id IS NOT NULL AND p.deleted_at IS NULL AND p.status='active' AND (p.expires_at IS NULL OR p.expires_at>$2)) END,
 a.auto_pause_on_expired,a.created_at,a.last_used_at,a.expires_at,a.rate_limit_reset_at,a.overload_until,a.temp_unschedulable_until,
 jsonb_build_object(
 'codex_usage_updated_at',a.extra->'codex_usage_updated_at',
 'codex_7d_used_percent',a.extra->'codex_7d_used_percent',
 'codex_7d_window_minutes',a.extra->'codex_7d_window_minutes',
 'codex_7d_reset_at',a.extra->'codex_7d_reset_at',
 'codex_5h_used_percent',a.extra->'codex_5h_used_percent',
 'codex_5h_window_minutes',a.extra->'codex_5h_window_minutes',
 'codex_5h_reset_at',a.extra->'codex_5h_reset_at'),
 EXISTS(SELECT 1 FROM (VALUES('workspace_id'),('chatgpt_workspace_id'),('organization_id'),('org_id')) AS f(k)
 WHERE NULLIF(trim(a.credentials->>f.k),'') IS NOT NULL AND NULLIF(trim(a.extra->>f.k),'') IS NOT NULL
 AND trim(a.credentials->>f.k)<>trim(a.extra->>f.k))
FROM accounts a LEFT JOIN proxies p ON p.id=a.proxy_id
WHERE a.deleted_at IS NULL AND EXISTS(SELECT 1 FROM account_groups ag WHERE ag.account_id=a.id AND ag.group_id=$1)`

func (w *Worker) read(ctx context.Context, now time.Time) (Group, Sample, error) {
	tx, err := w.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true, Isolation: sql.LevelRepeatableRead})
	if err != nil {
		return Group{}, Sample{}, err
	}
	defer tx.Rollback()
	g, err := w.group(ctx, tx)
	if err != nil {
		return g, Sample{}, err
	}
	rows, err := tx.QueryContext(ctx, accountSQL, g.ID, now)
	if err != nil {
		return g, Sample{}, err
	}
	rs := []Record{}
	for rows.Next() {
		var r Record
		var raw []byte
		var used, expires, rate, overload, temp sql.NullTime
		if err = rows.Scan(&r.ID, &r.Email, &r.ExtraEmail, &r.Workspace, &r.ExtraWorkspace, &r.Platform, &r.Type, &r.Status, &r.Plan, &r.Shadow, &r.Schedulable, &r.CredentialPresent, &r.ProxyOK, &r.AutoPause, &r.Created, &used, &expires, &rate, &overload, &temp, &raw, &r.IdentityConflict); err != nil {
			rows.Close()
			return g, Sample{}, err
		}
		r.LastUsed = used.Time
		r.Expires = expires.Time
		r.RateLimit = rate.Time
		r.Overload = overload.Time
		r.Temporary = temp.Time
		if err = json.Unmarshal(raw, &r.Cache); err != nil {
			rows.Close()
			return g, Sample{}, err
		}
		rs = append(rs, r)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return g, Sample{}, err
	}
	if err = tx.Commit(); err != nil {
		return g, Sample{}, err
	}
	return g, Normalize(rs, now), nil
}

func (w *Worker) Collect(parent context.Context, now time.Time) (err error) {
	started := time.Now()
	defer func() { w.mu.Lock(); w.failed = err != nil; w.mu.Unlock() }()
	now = now.UTC()
	bucket := now.Truncate(Interval)
	if now.Sub(bucket) > 30*time.Second {
		return errors.New("outside_bucket_tolerance")
	}
	ctx, cancel := context.WithTimeout(parent, 25*time.Second)
	defer cancel()
	// Cross-process transaction lock: workers in multiple replicas cannot overlap.
	tx, err := w.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var locked bool
	if err = tx.QueryRowContext(ctx, `SELECT pg_try_advisory_xact_lock($1)`, collectorLockID).Scan(&locked); err != nil {
		return err
	}
	if !locked {
		return nil
	}
	g, s, err := w.read(ctx, now)
	if err != nil {
		return err
	}
	if time.Since(started)+now.Sub(bucket) > 30*time.Second {
		return errors.New("collection_exceeded_bucket_tolerance")
	}
	// Persist the earliest seen trustworthy creation time; later reimports cannot move a batch.
	for key, a := range s.Accounts {
		if a.Created.IsZero() {
			continue
		}
		if err = tx.QueryRowContext(ctx, `INSERT INTO pool_runway_batches(group_id,identity_hash,first_created_at) VALUES($1,$2,$3)
ON CONFLICT(group_id,identity_hash) DO UPDATE SET first_created_at=LEAST(pool_runway_batches.first_created_at,EXCLUDED.first_created_at)
RETURNING first_created_at`, g.ID, key, a.Created).Scan(&a.Created); err != nil {
			return err
		}
		s.Accounts[key] = a
	}
	raw, err := json.Marshal(s)
	if err != nil {
		return err
	}
	res, err := tx.ExecContext(ctx, `INSERT INTO pool_runway_samples(group_id,bucket,sampled_at,payload) VALUES($1,$2,$3,$4)
ON CONFLICT(group_id,bucket) DO NOTHING`, g.ID, bucket, s.At, string(raw))
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return nil
	}
	rows, err := tx.QueryContext(ctx, `SELECT payload FROM pool_runway_samples WHERE group_id=$1 AND bucket >= $2 ORDER BY bucket`, g.ID, bucket.Add(-2*time.Hour))
	if err != nil {
		return err
	}
	samples := []Sample{}
	for rows.Next() {
		var b []byte
		var p Sample
		if err = rows.Scan(&b); err != nil {
			break
		}
		if err = json.Unmarshal(b, &p); err != nil {
			break
		}
		samples = append(samples, p)
	}
	rowErr := rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	if rowErr != nil {
		return rowErr
	}
	for _, policy := range []string{DefaultPolicy, "known_only"} {
		result := Calculate(s, samples, policy)
		data, e := PublicJSON(result)
		if e != nil {
			return e
		}
		if _, err = tx.ExecContext(ctx, `INSERT INTO pool_runway_history(group_id,bucket,policy,method,payload) VALUES($1,$2,$3,$4,$5) ON CONFLICT DO NOTHING`, g.ID, bucket, policy, Method, string(data)); err != nil {
			return err
		}
	}
	if _, err = tx.ExecContext(ctx, `DELETE FROM pool_runway_samples WHERE bucket < $1`, bucket.Add(-2*time.Hour)); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `DELETE FROM pool_runway_history WHERE bucket <= $1`, bucket.Add(-24*time.Hour)); err != nil {
		return err
	}
	return tx.Commit()
}
func (w *Worker) Overview(parent context.Context, policy string) (Snapshot, error) {
	if policy != "known_only" {
		policy = DefaultPolicy
	}
	now := time.Now().UTC()
	o := Snapshot{Schema: "1", Next: now.Truncate(Interval).Add(Interval), Interval: 300, Stale: true, Policy: policy, Method: Method, History: []Result{}}
	ctx, cancel := context.WithTimeout(parent, 8*time.Second)
	defer cancel()
	g, err := w.group(ctx, w.db)
	if err != nil {
		o.CollectionError = "监控分组未找到、重名或读取失败；请在页面选择分组并保存"
		return o, nil
	}
	o.Group = g
	rows, err := w.db.QueryContext(ctx, `SELECT payload FROM pool_runway_history WHERE group_id=$1 AND policy=$2 AND bucket > $3 ORDER BY bucket`, g.ID, policy, now.Add(-24*time.Hour))
	if err != nil {
		return o, err
	}
	defer rows.Close()
	for rows.Next() {
		var raw []byte
		var r Result
		if err = rows.Scan(&raw); err != nil {
			return o, err
		}
		if err = json.Unmarshal(raw, &r); err != nil {
			return o, err
		}
		o.History = append(o.History, r)
	}
	if err = rows.Err(); err != nil {
		return o, err
	}
	rows.Close()
	// A prolonged outage must not erase the last valid snapshot just because
	// the chart's 24-hour range has elapsed.
	var last *Result
	if len(o.History) == 0 {
		var raw []byte
		err = w.db.QueryRowContext(ctx, `SELECT payload FROM pool_runway_history WHERE group_id=$1 AND policy=$2 ORDER BY bucket DESC LIMIT 1`, g.ID, policy).Scan(&raw)
		if err != nil && err != sql.ErrNoRows {
			return o, err
		}
		if err == nil {
			var r Result
			if err = json.Unmarshal(raw, &r); err != nil {
				return o, err
			}
			last = &r
		}
	} else {
		r := o.History[len(o.History)-1]
		last = &r
	}
	w.mu.RLock()
	failed := w.failed
	w.mu.RUnlock()
	if failed {
		o.CollectionError = "采集失败，保留最后成功记录；等待下一时间桶"
	}
	if last != nil {
		r := *last
		o.Data = &r
		o.Generated = &r.Generated
		o.Age = now.Sub(r.Generated).Seconds()
		o.Stale = failed || o.Age > 720 || o.Age < -60 || r.Method != Method
		o.Available = !o.Stale
	}
	return o, nil
}
