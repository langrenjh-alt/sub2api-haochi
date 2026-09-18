package wishteam

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"log/slog"
	"regexp"
	"strings"
	"sync"
	"time"
)

type Worker struct {
	*Store
	client *client
	mu     sync.Mutex
	cancel context.CancelFunc
	wg     sync.WaitGroup
}

func New(db *sql.DB) *Worker { return &Worker{Store: &Store{db}, client: newClient()} }

func (w *Worker) Start() {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.cancel != nil {
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	w.cancel = cancel
	w.wg.Add(1)
	go func() {
		defer w.wg.Done()
		ticker := time.NewTicker(1500 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if err := w.step(ctx); err != nil && ctx.Err() == nil {
					// Never log upstream response bodies, credentials or secret job URLs.
					slog.Warn("WishTeam5X worker step failed; durable task retained", "error_type", errorType(err))
				}
			}
		}
	}()
}

func errorType(err error) string {
	if errors.Is(err, ErrBusy) {
		return "busy"
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return "timeout"
	}
	return "storage_or_upstream"
}

func (w *Worker) Stop() {
	w.mu.Lock()
	if w.cancel != nil {
		w.cancel()
	}
	w.mu.Unlock()
	w.wg.Wait()
}

type batch struct {
	ID, RunID        int64
	Status, RemoteID string
	Result           []byte
	Errors           int
}

func (w *Worker) step(parent context.Context) error {
	ctx, cancel := context.WithTimeout(parent, 90*time.Second)
	defer cancel()
	conn, err := w.db.Conn(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	var acquired bool
	if err = conn.QueryRowContext(ctx, `SELECT pg_try_advisory_lock($1)`, lockID).Scan(&acquired); err != nil {
		return err
	}
	if !acquired {
		return nil
	}
	defer func() {
		release, done := context.WithTimeout(context.Background(), 3*time.Second)
		defer done()
		_, _ = conn.ExecContext(release, `SELECT pg_advisory_unlock($1)`, lockID)
	}()
	var b batch
	err = w.db.QueryRowContext(ctx, `SELECT id,run_id,status,remote_id,result,errors FROM wishteam_batches
WHERE status NOT IN('done','failed') AND next_poll_at<=NOW() ORDER BY id LIMIT 1`).
		Scan(&b.ID, &b.RunID, &b.Status, &b.RemoteID, &b.Result, &b.Errors)
	if err == nil {
		switch b.Status {
		case "submitting":
			// A process stopped after reserving a request but before storing its ID.
			// Never replay a potentially accepted request.
			return w.failBatch(ctx, b.ID, "上次提交结果不确定，未重复提交；旧号保留，请稍后手动巡查")
		case "queued":
			return w.submit(ctx, b)
		case "polling":
			return w.poll(ctx, b)
		case "applying":
			return w.apply(ctx, b)
		}
		return errors.New("unknown durable batch state")
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	var runID int64
	err = w.db.QueryRowContext(ctx, `SELECT id FROM wishteam_runs WHERE status='running' LIMIT 1`).Scan(&runID)
	if errors.Is(err, sql.ErrNoRows) {
		_, err = w.startRunLocked(ctx)
		return err
	}
	if err != nil {
		return err
	}
	var outstanding bool
	if err = w.db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM wishteam_batches WHERE run_id=$1 AND status NOT IN('done','failed'))`, runID).Scan(&outstanding); err != nil {
		return err
	}
	if outstanding {
		return nil
	}
	return w.prepareBatch(ctx, runID)
}

func (w *Worker) prepareBatch(ctx context.Context, runID int64) error {
	tx, err := w.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	// Refresh the submitted file at the moment this batch is prepared; preserve
	// the full fresh row as concurrency evidence, even after a long preceding batch.
	if _, err = tx.ExecContext(ctx, `UPDATE wishteam_items i SET status='skipped',stage='done',
message='账号已删除或移出监控分组，未提交',updated_at=NOW() FROM wishteam_runs r
WHERE i.run_id=r.id AND r.id=$1 AND i.status='queued' AND NOT EXISTS(
SELECT 1 FROM accounts a JOIN account_groups ag ON ag.account_id=a.id JOIN groups g ON g.id=ag.group_id
WHERE a.id=i.account_id AND a.deleted_at IS NULL AND ag.group_id=r.group_id AND g.deleted_at IS NULL)`, runID); err != nil {
		return err
	}
	var ids []int64
	rows, err := tx.QueryContext(ctx, `SELECT id FROM wishteam_items WHERE run_id=$1 AND status='queued' AND batch_id IS NULL ORDER BY id LIMIT 200`, runID)
	if err != nil {
		return err
	}
	for rows.Next() {
		var id int64
		if err = rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		ids = append(ids, id)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	if len(ids) == 0 {
		if _, err = tx.ExecContext(ctx, `UPDATE wishteam_runs SET status='done',finished_at=NOW(),
message=CASE WHEN total=0 THEN '所选分组没有可巡查的 OpenAI OAuth 主账号' ELSE '本轮巡查完成' END WHERE id=$1`, runID); err != nil {
			return err
		}
		if _, err = tx.ExecContext(ctx, `UPDATE wishteam_settings SET next_run_at=GREATEST(
NOW()+make_interval(mins=>interval_minutes),
NOW()+make_interval(secs=>COALESCE((SELECT max(retry_after) FROM wishteam_items WHERE run_id=$1),0))) WHERE id=1`, runID); err != nil {
			return err
		}
		return tx.Commit()
	}
	var batchID int64
	if err = tx.QueryRowContext(ctx, `INSERT INTO wishteam_batches(run_id) VALUES($1) RETURNING id`, runID).Scan(&batchID); err != nil {
		return err
	}
	for _, id := range ids {
		if _, err = tx.ExecContext(ctx, `UPDATE wishteam_items i SET batch_id=$2,snapshot=to_jsonb(a)
FROM accounts a WHERE i.id=$1 AND a.id=i.account_id`, id, batchID); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (w *Worker) submit(ctx context.Context, b batch) error {
	var allowed bool
	if err := w.db.QueryRowContext(ctx, `SELECT submit_after<=NOW() FROM wishteam_settings WHERE id=1`).Scan(&allowed); err != nil {
		return err
	}
	if !allowed {
		return nil
	}
	rows, err := w.db.QueryContext(ctx, `SELECT snapshot,email FROM wishteam_items WHERE batch_id=$1 ORDER BY id`, b.ID)
	if err != nil {
		return err
	}
	accounts := []map[string]any{}
	for rows.Next() {
		var raw []byte
		var email string
		var a map[string]any
		if err = rows.Scan(&raw, &email); err != nil {
			rows.Close()
			return err
		}
		if err = json.Unmarshal(raw, &a); err != nil {
			rows.Close()
			return err
		}
		if emailOf(a) != email {
			rows.Close()
			return w.failBatch(ctx, b.ID, "批次准备期间邮箱发生变化，旧号保留")
		}
		accounts = append(accounts, outbound(a))
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	if len(accounts) == 0 || len(accounts) > 200 {
		return w.failBatch(ctx, b.ID, "批次数量无效，未提交")
	}
	tx, err := w.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, `UPDATE wishteam_settings SET submit_after=NOW()+INTERVAL '31 seconds' WHERE id=1`); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE wishteam_batches SET status='submitting' WHERE id=$1`, b.ID); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE wishteam_items SET status='checking',stage='checking',updated_at=NOW() WHERE batch_id=$1`, b.ID); err != nil {
		return err
	}
	if err = tx.Commit(); err != nil {
		return err
	}
	reply, err := w.client.submit(ctx, document(accounts))
	if err != nil {
		var remote *remoteError
		if errors.As(err, &remote) && remote.retry > 0 {
			_, err = w.db.ExecContext(ctx, `UPDATE wishteam_settings SET submit_after=NOW()+make_interval(secs=>$1) WHERE id=1`, remote.retry)
			if err != nil {
				return err
			}
			_, err = w.db.ExecContext(ctx, `UPDATE wishteam_batches SET status='queued',next_poll_at=NOW()+make_interval(secs=>$2) WHERE id=$1`, b.ID, remote.retry)
			if err != nil {
				return err
			}
			_, err = w.db.ExecContext(ctx, `UPDATE wishteam_items SET stage='queued',message='等待上游限频窗口',retry_after=$2 WHERE batch_id=$1`, b.ID, remote.retry)
			return err
		}
		return w.failBatch(ctx, b.ID, "上游提交结果不确定，未重复提交；旧号保留，请稍后巡查")
	}
	if !regexp.MustCompile(`^[A-Za-z0-9_-]{8,256}$`).MatchString(reply.TaskID) {
		return w.failBatch(ctx, b.ID, "上游未返回有效任务编号，未重复提交；旧号保留")
	}
	_, err = w.db.ExecContext(ctx, `UPDATE wishteam_batches SET status='polling',remote_id=$2,next_poll_at=NOW()+INTERVAL '1.5 seconds' WHERE id=$1`, b.ID, reply.TaskID)
	return err
}

var stages = map[string]bool{"queued": true, "checking": true, "checking_current": true, "loading_source": true, "checking_seat": true, "refreshing": true, "verifying": true, "done": true}
var codes = map[string]bool{"workspace_deactivated": true, "probe_unavailable": true, "usage_incomplete": true, "identity_mismatch": true, "plan_mismatch": true, "workspace_mismatch": true}

func (w *Worker) progress(ctx context.Context, batchID int64, items []RemoteItem) error {
	for _, item := range items {
		stage := item.Stage
		if !stages[stage] {
			stage = "checking"
		}
		code := item.ErrorCode
		if !codes[code] {
			code = ""
		}
		probe, _ := json.Marshal(safeProbe(item.Probe))
		retry := item.RetryAfter
		if retry < 0 {
			retry = 0
		}
		if retry > 86400 {
			retry = 86400
		}
		if _, err := w.db.ExecContext(ctx, `UPDATE wishteam_items SET stage=$3,error_code=$4,probe=$5::jsonb,retry_after=$6,updated_at=NOW()
WHERE batch_id=$1 AND email=$2 AND status IN('queued','checking')`, batchID, strings.ToLower(strings.TrimSpace(item.Email)), stage, code, string(probe), retry); err != nil {
			return err
		}
	}
	return nil
}

func (w *Worker) poll(ctx context.Context, b batch) error {
	reply, err := w.client.poll(ctx, b.RemoteID, false)
	if err != nil {
		return w.pollError(ctx, b, err)
	}
	if err = w.progress(ctx, b.ID, reply.Task.Items); err != nil {
		return err
	}
	if reply.Task.Status == "done" || reply.Task.Status == "interrupted" {
		result, err := w.client.poll(ctx, b.RemoteID, true)
		if err != nil {
			return w.pollError(ctx, b, err)
		}
		// Partial downloads never mean that unfinished accounts failed.
		// Keep the task polling; never submit it again.
		if result.Partial {
			return w.pollError(ctx, b, errors.New("上游结果尚未完成"))
		}
		if result.OutputMode != "" && result.OutputMode != "repaired_only" {
			return w.failBatch(ctx, b.ID, "上游批量结果模式异常，旧号保留")
		}
		raw, err := json.Marshal(result)
		if err != nil {
			return err
		}
		// Persist the complete sub2 result before making any local deletion.
		_, err = w.db.ExecContext(ctx, `UPDATE wishteam_batches SET status='applying',result=$2::jsonb,errors=0,next_poll_at=NOW() WHERE id=$1`, b.ID, string(raw))
		return err
	}
	if reply.Task.Status != "queued" && reply.Task.Status != "running" {
		return w.failBatch(ctx, b.ID, "上游任务状态异常，旧号保留")
	}
	_, err = w.db.ExecContext(ctx, `UPDATE wishteam_batches SET errors=0,next_poll_at=NOW()+INTERVAL '1.5 seconds' WHERE id=$1`, b.ID)
	return err
}

func (w *Worker) pollError(ctx context.Context, b batch, cause error) error {
	if b.Errors >= 119 {
		return w.failBatch(ctx, b.ID, "上游进度长时间不可查询；旧号保留，未重提任务")
	}
	delay := 30
	var remote *remoteError
	if errors.As(cause, &remote) && remote.retry > delay {
		delay = remote.retry
	}
	_, err := w.db.ExecContext(ctx, `UPDATE wishteam_batches SET errors=errors+1,next_poll_at=NOW()+make_interval(secs=>$2) WHERE id=$1`, b.ID, delay)
	return err
}

func (w *Worker) failBatch(ctx context.Context, batchID int64, message string) error {
	tx, err := w.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, `UPDATE wishteam_items SET status='failed',stage='done',message=$2,updated_at=NOW()
WHERE batch_id=$1 AND status IN('queued','checking')`, batchID, message); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE wishteam_batches SET status='failed' WHERE id=$1`, batchID); err != nil {
		return err
	}
	return tx.Commit()
}

func (w *Worker) apply(ctx context.Context, b batch) error {
	var reply RemoteReply
	if err := json.Unmarshal(b.Result, &reply); err != nil {
		return err
	}
	if !reply.Success {
		return w.failBatch(ctx, b.ID, "上游任务没有成功结果，旧号保留")
	}
	if reply.Partial || (reply.OutputMode != "" && reply.OutputMode != "repaired_only") {
		return w.failBatch(ctx, b.ID, "上游批量结果不完整或模式异常，旧号保留")
	}
	if err := w.progress(ctx, b.ID, reply.Results); err != nil {
		return err
	}
	verdicts := map[string][]RemoteItem{}
	accounts := map[string][]map[string]any{}
	for _, r := range reply.Results {
		e := strings.ToLower(strings.TrimSpace(r.Email))
		verdicts[e] = append(verdicts[e], r)
	}
	for _, a := range reply.Sub2.Accounts {
		e := emailOf(a)
		accounts[e] = append(accounts[e], a)
	}
	rows, err := w.db.QueryContext(ctx, `SELECT id,email FROM wishteam_items WHERE batch_id=$1 AND status IN('queued','checking') ORDER BY id LIMIT 20`, b.ID)
	if err != nil {
		return err
	}
	type pending struct {
		id    int64
		email string
	}
	items := []pending{}
	for rows.Next() {
		var i pending
		if err = rows.Scan(&i.id, &i.email); err != nil {
			rows.Close()
			return err
		}
		items = append(items, i)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, i := range items {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		status, message := "failed", "上游未返回唯一逐号结果，旧号保留"
		if vs := verdicts[i.email]; len(vs) == 1 {
			v := vs[0]
			message = verdictMessage(v)
			if v.Status == "alive" && !v.ReusedCurrent {
				// Healthy original files are never deleted or re-imported.
				status = "alive"
			} else if v.Status == "revived" || (v.Status == "alive" && v.ReusedCurrent) {
				if reply.Sub2.Type != "sub2api-data" || reply.Sub2.Version != 1 {
					message = "返回 Sub2 文档类型或版本无效，旧号保留"
				} else if len(accounts[i.email]) != 1 {
					message = "返回成品缺失或邮箱重复，旧号保留"
				} else if err := w.replace(ctx, i.id, accounts[i.email][0], v, document(accounts[i.email])); err == nil {
					continue
				} else {
					message = "本地导入、并发凭据检查或配置校验未通过；删除已撤销，旧号保留"
				}
			}
		}
		if _, err = w.db.ExecContext(ctx, `UPDATE wishteam_items SET status=$2,stage='done',message=$3,updated_at=NOW() WHERE id=$1`, i.id, status, message); err != nil {
			return err
		}
	}
	_, err = w.db.ExecContext(ctx, `UPDATE wishteam_batches SET status='done' WHERE id=$1 AND NOT EXISTS(
SELECT 1 FROM wishteam_items WHERE batch_id=$1 AND status IN('queued','checking'))`, b.ID)
	return err
}
