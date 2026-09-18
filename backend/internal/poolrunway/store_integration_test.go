package poolrunway

import (
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	_ "github.com/lib/pq"
)

// Uses a dedicated throwaway local database, never the production account database.
func TestPostgresCollector(t *testing.T) {
	dsn := os.Getenv("POOL_RUNWAY_TEST_DSN")
	if dsn == "" {
		t.Skip("set POOL_RUNWAY_TEST_DSN to isolated local test DB")
	}
	db, err := sql.Open("postgres", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	var name string
	if err = db.QueryRow("SELECT current_database()").Scan(&name); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(name, "pool_runway_test") {
		t.Fatal("test database name must start pool_runway_test")
	}
	_, err = db.Exec(`DROP SCHEMA IF EXISTS runway_test CASCADE;CREATE SCHEMA runway_test;
CREATE TABLE runway_test.groups(id bigint primary key,name text,platform text,deleted_at timestamptz);
CREATE TABLE runway_test.proxies(id bigint primary key,deleted_at timestamptz,status text,expires_at timestamptz);
CREATE TABLE runway_test.accounts(id bigint primary key,credentials jsonb,extra jsonb,platform text,type text,status text,parent_account_id bigint,schedulable bool,
auto_pause_on_expired bool,created_at timestamptz,last_used_at timestamptz,expires_at timestamptz,rate_limit_reset_at timestamptz,overload_until timestamptz,
temp_unschedulable_until timestamptz,proxy_id bigint,deleted_at timestamptz);
CREATE TABLE runway_test.account_groups(account_id bigint,group_id bigint);`)
	if err != nil {
		t.Fatal(err)
	}
	// search_path is a connection startup parameter, so every pooled transaction uses isolation schema.
	db.Close()
	db, err = sql.Open("postgres", dsn+" search_path=runway_test")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	migration, err := os.ReadFile("../../migrations/246_pool_runway.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(string(migration)); err != nil {
		t.Fatal(err)
	}
	settingsMigration, err := os.ReadFile("../../migrations/247_pool_runway_settings.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(string(settingsMigration)); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(`INSERT INTO groups VALUES(89,'正常组','openai',NULL);
INSERT INTO accounts(id,credentials,extra,platform,type,status,schedulable,auto_pause_on_expired,created_at)
VALUES(1,'{"email":"test@example.invalid","chatgpt_account_id":"workspace-full","access_token":"NEVER_EXPORT_SENTINEL"}','{}','openai','oauth','active',true,true,NOW()-interval '1 day');
INSERT INTO account_groups VALUES(1,89)`); err != nil {
		t.Fatal(err)
	}
	t.Setenv("POOL_RUNWAY_GROUP_ID", "")
	t.Setenv("POOL_RUNWAY_GROUP_NAME", "")
	w := New(db)
	end := time.Now().UTC().Truncate(Interval)
	for i := 0; i < 5; i++ {
		at := end.Add(time.Duration(i-4) * Interval)
		r := record(at, float64(10+i*5))
		r.Cache["codex_7d_reset_at"] = end.Add(time.Hour).Format(time.RFC3339)
		raw, _ := json.Marshal(r.Cache)
		if _, err = db.Exec(`UPDATE accounts SET extra=$1`, string(raw)); err != nil {
			t.Fatal(err)
		}
		if err = w.Collect(ctx, at); err != nil {
			t.Fatal(err)
		}
	}
	o, err := w.Overview(ctx, DefaultPolicy)
	if err != nil {
		t.Fatal(err)
	}
	if o.Group.ID != 89 || o.Stale || o.Data == nil || o.Data.Candidates != 1 || len(o.History) != 5 {
		t.Fatalf("%+v", o)
	}
	closeTo(t, o.Data.Burn.Rate, .6)
	// Page settings persist across worker restarts and never mix group histories.
	cfg, err := w.Config(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.GroupID != 0 || cfg.Revision != 0 || cfg.Source != "default" || cfg.EffectiveGroup.ID != 89 || len(cfg.Groups) != 1 {
		t.Fatal(cfg)
	}
	if _, err = db.Exec(`INSERT INTO groups VALUES(90,'另一个组','openai',NULL),(91,'其他平台','grok',NULL),(92,'已删除','openai',NOW())`); err != nil {
		t.Fatal(err)
	}
	for _, badID := range []int64{0, -1, 91, 92, 999} {
		if err = w.Configure(ctx, badID, 0); err != ErrInvalidGroup {
			t.Fatalf("bad group %d: %v", badID, err)
		}
	}
	lockSettings, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = lockSettings.Exec(`SELECT pg_advisory_xact_lock(871356246)`); err != nil {
		t.Fatal(err)
	}
	if err = w.Configure(ctx, 90, 0); err != ErrCollectorBusy {
		t.Fatal("configuration raced collector", err)
	}
	lockSettings.Rollback()
	if err = w.Configure(ctx, 90, 0); err != nil {
		t.Fatal(err)
	}
	restarted := New(db)
	cfg, err = restarted.Config(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.GroupID != 90 || cfg.Revision != 1 || cfg.Source != "page" || cfg.EffectiveGroup.ID != 90 || len(cfg.Groups) != 2 {
		t.Fatal(cfg)
	}
	other, err := restarted.Overview(ctx, DefaultPolicy)
	if err != nil {
		t.Fatal(err)
	}
	if other.Group.ID != 90 || other.Data != nil || len(other.History) != 0 {
		t.Fatal("old group inventory leaked")
	}
	if err = w.Configure(ctx, 89, 0); err != ErrConfigConflict {
		t.Fatal("stale page overwrote setting", err)
	}
	// Page choice wins over the old environment fallback.
	t.Setenv("POOL_RUNWAY_GROUP_ID", "89")
	cfg, err = restarted.Config(ctx)
	if err != nil || cfg.EffectiveGroup.ID != 90 {
		t.Fatal("environment overrode page", err)
	}
	t.Setenv("POOL_RUNWAY_GROUP_ID", "")
	if _, err = db.Exec(`UPDATE groups SET deleted_at=NOW() WHERE id=90`); err != nil {
		t.Fatal(err)
	}
	cfg, err = restarted.Config(ctx)
	if err != nil || cfg.GroupID != 90 || cfg.EffectiveGroup.ID != 0 {
		t.Fatal("deleted saved group silently fell back", err)
	}
	if err = w.Configure(ctx, 89, 1); err != nil {
		t.Fatal(err)
	}
	resumed, err := w.Overview(ctx, DefaultPolicy)
	if err != nil || resumed.Data == nil || resumed.Group.ID != 89 {
		t.Fatal("history not retained", err)
	}
	// Saving performs no sampling, no collection, no business writes.
	var sampleCount int
	if err = db.QueryRow(`SELECT count(*) FROM pool_runway_samples`).Scan(&sampleCount); err != nil || sampleCount != 5 {
		t.Fatal("save generated snapshots", err)
	}
	t.Log("CONFIG: persistent selection, valid OpenAI-only choices, revision conflict, collector lock, env precedence, deleted group fail-closed, isolated histories, no sampling on save PASS")
	if err = w.Collect(ctx, end); err != nil {
		t.Fatal(err)
	}
	var count int
	_ = db.QueryRow(`SELECT count(*) FROM pool_runway_samples`).Scan(&count)
	if count != 5 {
		t.Fatal("duplicate bucket replaced", count)
	}
	lockTx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = lockTx.Exec(`SELECT pg_advisory_xact_lock(871356246)`); err != nil {
		t.Fatal(err)
	}
	if err = w.Collect(ctx, end.Add(Interval)); err != nil {
		t.Fatal(err)
	}
	lockTx.Rollback()
	_ = db.QueryRow(`SELECT count(*) FROM pool_runway_samples`).Scan(&count)
	if count != 5 {
		t.Fatal("collector bypassed another instance's lock")
	}
	// Re-import creation time must not push forward durable batch attribution.
	if _, err = db.Exec(`UPDATE accounts SET created_at=NOW()`); err != nil {
		t.Fatal(err)
	}
	before := o.Data.Batches[0].At
	if err = w.Collect(ctx, end.Add(Interval)); err != nil {
		t.Fatal(err)
	}
	var earliest time.Time
	if err = db.QueryRow(`SELECT first_created_at FROM pool_runway_batches`).Scan(&earliest); err != nil {
		t.Fatal(err)
	}
	if !earliest.Truncate(time.Minute).Equal(before) {
		t.Fatal("batch moved")
	}
	// All public payloads must remain aggregate and free of the sentinel credential.
	raw, err := PublicJSON(o)
	if err != nil || strings.Contains(string(raw), "NEVER_EXPORT_SENTINEL") || strings.Contains(string(raw), "example.invalid") {
		t.Fatal("output disclosure", err)
	}
	// Real PostgreSQL read-only transaction rejects a business write.
	tx, err := db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	_, err = tx.Exec(`UPDATE accounts SET status='error'`)
	tx.Rollback()
	if err == nil {
		t.Fatal("readonly transaction allowed write")
	}
	var status, token string
	if err = db.QueryRow(`SELECT status,credentials->>'access_token' FROM accounts`).Scan(&status, &token); err != nil {
		t.Fatal(err)
	}
	if status != "active" || token != "NEVER_EXPORT_SENTINEL" {
		t.Fatal("business state changed")
	}
	// Failure does not replace stored inventory with zero.
	if _, err = db.Exec(`ALTER TABLE accounts RENAME TO hidden_accounts`); err != nil {
		t.Fatal(err)
	}
	if err = w.Collect(ctx, end.Add(2*Interval)); err == nil {
		t.Fatal("expected read failure")
	}
	o, err = w.Overview(ctx, DefaultPolicy)
	if err != nil || !o.Stale || o.Data == nil || o.Data.Candidates != 1 {
		t.Fatal("failure state not retained", err)
	}
	// Old snapshots are historical, not a current ETA, even after process restart.
	old, _ := json.Marshal(time.Now().UTC().Add(-13 * time.Minute))
	if _, err = db.Exec(`UPDATE pool_runway_history SET payload=jsonb_set(payload,'{generated_at}',$1::jsonb)`, string(old)); err != nil {
		t.Fatal(err)
	}
	o, err = New(db).Overview(ctx, DefaultPolicy)
	if err != nil || !o.Stale || o.Available || o.Data == nil {
		t.Fatal("old snapshot accepted as current", err)
	}
	// Malformed stored time is also rejected.
	if _, err = db.Exec(`UPDATE pool_runway_history SET payload=jsonb_set(payload,'{generated_at}',to_jsonb((NOW()-interval '13 minutes')::text))`); err != nil {
		t.Fatal(err)
	}
	// PostgreSQL text timestamps are deliberately not RFC3339; malformed persisted payload must fail closed.
	if _, err = w.Overview(ctx, DefaultPolicy); err == nil {
		t.Fatal("malformed timestamp accepted")
	}
	t.Log("POSTGRES: readonly business transaction, aligned collection, 5 snapshots, R=0.6, duplicate lock, stable batches, secret filtering, failure retention PASS")
}
