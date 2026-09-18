package repository

import (
	"context"
	"database/sql"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
)

type turnStateTestProxyRepo struct{ service.ProxyRepository }

func (*turnStateTestProxyRepo) GetByID(_ context.Context, id int64) (*service.Proxy, error) {
	if id == 3 {
		return &service.Proxy{ID: 3, Status: service.StatusActive, Protocol: "http", Host: "fixture.invalid", Port: 8080}, nil
	}
	return nil, service.ErrProxyNotFound
}

// This opt-in test creates only connection-local TEMP tables. It never writes
// application tables or advances application sequences.
func TestCodexTurnStatePostgresRegression(t *testing.T) {
	dsn := os.Getenv("CODEX_STATE_TEST_DSN")
	if dsn == "" {
		t.Skip("CODEX_STATE_TEST_DSN not set")
	}
	db, err := sql.Open("postgres", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	ctx := context.Background()
	_, err = db.ExecContext(ctx, `
CREATE TEMP TABLE codex_turn_states (
 id BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY, account_id BIGINT NOT NULL,
 state TEXT NOT NULL DEFAULT '', state_fingerprint TEXT NOT NULL DEFAULT '',
 status TEXT NOT NULL DEFAULT 'active', source TEXT NOT NULL DEFAULT 'harvest',
 http_status INT NOT NULL DEFAULT 0, model TEXT NOT NULL DEFAULT '', proxy_id BIGINT,
 latency_ms BIGINT NOT NULL DEFAULT 0, issued_at TIMESTAMPTZ, expires_at TIMESTAMPTZ,
 error TEXT NOT NULL DEFAULT '', created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE TEMP TABLE accounts (id BIGINT PRIMARY KEY, name TEXT, platform TEXT, type TEXT,
 status TEXT, schedulable BOOLEAN, deleted_at TIMESTAMPTZ, credentials JSONB);
CREATE TEMP TABLE account_groups (account_id BIGINT, group_id BIGINT);
CREATE TEMP TABLE settings (key TEXT PRIMARY KEY, value TEXT, updated_at TIMESTAMPTZ DEFAULT NOW());
CREATE TEMP TABLE codex_turn_state_attempts (account_id BIGINT PRIMARY KEY, attempts INT NOT NULL DEFAULT 0, updated_at TIMESTAMPTZ DEFAULT NOW(), next_retry_at TIMESTAMPTZ, upstream_retry_at TIMESTAMPTZ);
INSERT INTO accounts VALUES
 (7,'good','openai','oauth','active',true,NULL,'{"access_token":"fixture"}'),
 (8,'other','grok','oauth','active',true,NULL,'{"access_token":"fixture"}'),
 (9,'empty','openai','oauth','active',true,NULL,'{}');
INSERT INTO account_groups VALUES (7,24),(8,24),(9,24);`)
	if err != nil {
		t.Fatal(err)
	}
	r := &codexTurnStateRepository{db: db}
	t.Run("attempt_limit_survives_repository_restart", func(t *testing.T) {
		for i := 1; i <= 50; i++ {
			if n, err := r.ReserveAttempt(ctx, 7, 50); err != nil || n != i {
				t.Fatalf("reserve %d: count=%d err=%v", i, n, err)
			}
		}
		restarted := &codexTurnStateRepository{db: db}
		if n, err := restarted.ReserveAttempt(ctx, 7, 50); err != nil || n != 0 {
			t.Fatalf("limit bypassed after restart: %d %v", n, err)
		}
		counts, err := restarted.AttemptCounts(ctx, []int64{7})
		if err != nil || counts[7] != 50 {
			t.Fatalf("persisted count=%v err=%v", counts, err)
		}
		if err := restarted.ResetAttempts(ctx, 7); err != nil {
			t.Fatal(err)
		}
		if n, err := restarted.ReserveAttempt(ctx, 7, 50); err != nil || n != 1 {
			t.Fatalf("manual reset: %d %v", n, err)
		}
	})
	t.Run("save_readback_with_deleted_and_valid_proxies", func(t *testing.T) {
		svc := service.NewCodexTurnStateService(r, nil, &turnStateTestProxyRepo{}, nil)
		cfg := service.CodexTurnStateConfig{Enabled: false, ProxyID: 2, AccountIDs: []int64{7}}
		if _, err := svc.UpdateConfig(ctx, 1, cfg); err != nil {
			t.Fatalf("disable with deleted proxy must save: %v", err)
		}
		cfg.Enabled = true
		if _, err := svc.UpdateConfig(ctx, 1, cfg); err == nil {
			t.Fatal("enabled config accepted deleted proxy")
		}
		readback, err := r.Config(ctx)
		if err != nil || readback.Enabled {
			t.Fatalf("failed save modified settings: %v", err)
		}
		cfg.ProxyID = 3
		saved, err := svc.UpdateConfig(ctx, 1, cfg)
		if err != nil {
			t.Fatal(err)
		}
		readback, err = r.Config(ctx)
		if err != nil || !readback.Enabled || readback.ProxyID != 3 || readback.Model != saved.Model {
			t.Fatalf("successful save/readback mismatch: %+v %v", readback, err)
		}
		t.Log("DISABLE_MISSING_PROXY=OK VALID_PROXY_SAVE_READBACK=OK FAILED_SAVE_UNCHANGED=OK")
	})
	now, expiry := time.Now().UTC(), time.Now().UTC().Add(time.Hour)
	good := &service.CodexTurnStateRecord{AccountID: 7, State: strings.Repeat("a", 332),
		StateFingerprint: "fixture", Status: "active", Source: "probe", HTTPStatus: 200,
		Model: "gpt-6-astra", IssuedAt: &now, ExpiresAt: &expiry}
	insert := func(v *service.CodexTurnStateRecord) {
		t.Helper()
		if _, err := r.Insert(ctx, v); err != nil {
			t.Fatal(err)
		}
	}
	insert(good)
	insert(&service.CodexTurnStateRecord{AccountID: 7, State: strings.Repeat("b", 356), Status: "degraded", ExpiresAt: &expiry})
	insert(&service.CodexTurnStateRecord{AccountID: 7, Status: "failed"})
	t.Run("restart_preserves_good_state_and_length", func(t *testing.T) {
		latest, err := r.LatestPerAccount(ctx, []int64{7})
		if err != nil {
			t.Fatal(err)
		}
		if latest[7].Status != "active" || latest[7].StateLength != 332 {
			t.Fatalf("status=%s length=%d, want active/332", latest[7].Status, latest[7].StateLength)
		}
		h, err := r.History(ctx, 7, 1, 20)
		if err != nil || len(h.Items) != 3 {
			t.Fatalf("history: %v", err)
		}
		if h.Items[1].StateLength != 356 {
			t.Errorf("history length=%d, want 356", h.Items[1].StateLength)
		}
	})
	t.Run("enrollment_filters_credentials_and_platform", func(t *testing.T) {
		ids, err := r.EnrolledAccounts(ctx, []int64{24})
		if err != nil || len(ids) != 1 || ids[7] != 24 {
			t.Fatalf("enrolled=%v err=%v, want only 7", ids, err)
		}
	})
	if err := r.Invalidate(ctx, 7, "fixture revocation"); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 65; i++ {
		insert(&service.CodexTurnStateRecord{AccountID: 7, Status: "failed"})
	}
	t.Run("retention_and_revocation_barrier", func(t *testing.T) {
		if _, err := r.Prune(ctx, 50, now.Add(-7*24*time.Hour)); err != nil {
			t.Fatal(err)
		}
		var count, markers int
		if err := db.QueryRow(`SELECT COUNT(*), COUNT(*) FILTER (WHERE status='revoked') FROM codex_turn_states`).Scan(&count, &markers); err != nil {
			t.Fatal(err)
		}
		if count > 52 || markers != 1 {
			t.Errorf("rows=%d markers=%d; want <=52 and 1 revocation barrier", count, markers)
		}
		latest, err := r.LatestPerAccount(ctx, []int64{7})
		if err != nil {
			t.Fatal(err)
		}
		if latest[7].Status == "active" {
			t.Fatal("revoked old active state resurrected after pruning")
		}
	})
	insert(good)
	t.Run("new_state_after_revocation_is_usable", func(t *testing.T) {
		latest, err := r.LatestPerAccount(ctx, []int64{7})
		if err != nil || latest[7].Status != "active" {
			t.Fatalf("latest status=%s err=%v", latest[7].Status, err)
		}
	})
	t.Run("clear_history_preserves_live_pin_revocation_and_budget", func(t *testing.T) {
		insert(&service.CodexTurnStateRecord{AccountID: 8, State: strings.Repeat("c", 332), Status: "active", ExpiresAt: &expiry})
		if err := r.Invalidate(ctx, 8, "revoked fixture"); err != nil {
			t.Fatal(err)
		}
		before, err := r.AttemptCounts(ctx, []int64{7})
		if err != nil {
			t.Fatal(err)
		}
		deleted, kept, err := r.ClearHistory(ctx, []int64{good.ID})
		if err != nil || deleted == 0 || kept != 2 {
			t.Fatalf("clear: deleted=%d retained=%d err=%v", deleted, kept, err)
		}
		latest, err := r.LatestPerAccount(ctx, []int64{7, 8})
		if err != nil || latest[7].Status != "active" || latest[8].Status != "revoked" {
			t.Fatalf("clear lost pin or revocation: %v %v", latest, err)
		}
		after, err := r.AttemptCounts(ctx, []int64{7})
		if err != nil || after[7] != before[7] {
			t.Fatalf("clear reset attempt budget: before=%v after=%v err=%v", before, after, err)
		}
	})
}
