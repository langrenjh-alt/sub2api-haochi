package repository

import (
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/Wei-Shaw/sub2api/migrations"
	"github.com/stretchr/testify/require"
)

func transferFixture(t *testing.T) (*sql.DB, *codexTurnStateRepository, service.CodexTurnStateConfig) {
	t.Helper()
	dsn := os.Getenv("CODEX_STATE_TEST_DSN")
	if dsn == "" {
		t.Skip("CODEX_STATE_TEST_DSN not set")
	}
	db, err := sql.Open("postgres", dsn)
	require.NoError(t, err)
	db.SetMaxOpenConns(1)
	schema := fmt.Sprintf("ticket_transfer_%d", time.Now().UnixNano())
	_, err = db.Exec(`CREATE SCHEMA ` + schema + `; SET search_path TO ` + schema)
	require.NoError(t, err)
	t.Cleanup(func() { _, _ = db.Exec(`DROP SCHEMA ` + schema + ` CASCADE`); db.Close() })
	_, err = db.Exec(`
CREATE TABLE accounts(id bigint PRIMARY KEY,platform text DEFAULT 'openai',status text DEFAULT 'active',type text DEFAULT 'oauth',schedulable boolean DEFAULT true,credentials jsonb DEFAULT '{"access_token":"fixture"}',deleted_at timestamptz);
CREATE TABLE groups(id bigint PRIMARY KEY,platform text DEFAULT 'openai',status text DEFAULT 'active',deleted_at timestamptz,degradation_detection_enabled boolean DEFAULT false,degradation_detection_config jsonb DEFAULT '{}');
CREATE TABLE account_groups(account_id bigint,group_id bigint,priority integer DEFAULT 50,created_at timestamptz DEFAULT NOW(),PRIMARY KEY(account_id,group_id));
CREATE TABLE settings(key text PRIMARY KEY,value text,updated_at timestamptz DEFAULT NOW());
CREATE TABLE scheduler_outbox(id bigserial,event_type text,account_id bigint,group_id bigint,payload jsonb,dedup_key text);
CREATE UNIQUE INDEX outbox_dedup ON scheduler_outbox(dedup_key) WHERE dedup_key IS NOT NULL;
INSERT INTO groups(id) VALUES(1),(2),(3);
INSERT INTO accounts(id) VALUES(7),(8);
INSERT INTO account_groups VALUES(7,1,17,NOW()),(7,3,91,NOW()),(8,2,23,NOW());`)
	require.NoError(t, err)
	for _, file := range []string{"248_codex_turn_state.sql", "249_codex_turn_state_attempts.sql", "251_codex_turn_state_transfer.sql"} {
		raw, err := migrations.FS.ReadFile(file)
		require.NoError(t, err)
		_, err = db.Exec(string(raw))
		require.NoError(t, err)
	}
	r := &codexTurnStateRepository{db: db}
	cfg := service.NormalizeCodexTurnStateConfig(service.CodexTurnStateConfig{Enabled: true, InjectEnabled: true, TransferEnabled: true, TransferReadyGroupID: 1, TransferRecoveryGroupID: 2, TargetStateLength: 332})
	require.NoError(t, r.SaveConfig(context.Background(), 1, cfg))
	return db, r, cfg
}

func transferToken(now time.Time) string {
	raw := make([]byte, 57+12*16)
	raw[0] = 0x80
	binary.BigEndian.PutUint64(raw[1:9], uint64(now.Unix()))
	return base64.URLEncoding.EncodeToString(raw)
}

func TestTurnStateTransferPostgresRoundTrip(t *testing.T) {
	db, r, cfg := transferFixture(t)
	ctx := context.Background()
	move, err := r.ReconcileTransfer(ctx, cfg, 7, 0, false)
	require.NoError(t, err)
	require.Equal(t, int64(2), move.ToGroupID)
	require.Equal(t, []int64{2, 3}, moveGroups(t, db, 7))
	var priority int
	require.NoError(t, db.QueryRow(`SELECT priority FROM account_groups WHERE account_id=7 AND group_id=2`).Scan(&priority))
	require.Equal(t, 17, priority)
	now := time.Now()
	expiry := now.Add(time.Hour)
	rec := &service.CodexTurnStateRecord{AccountID: 7, State: transferToken(now), Model: cfg.Model, Status: "active", IssuedAt: &now, ExpiresAt: &expiry}
	id, err := r.Insert(ctx, rec)
	require.NoError(t, err)
	move, err = r.ReconcileTransfer(ctx, cfg, 7, id, false)
	require.NoError(t, err)
	require.Nil(t, move, "persisted but not yet bound must not promote")
	move, err = r.ReconcileTransfer(ctx, cfg, 7, id, true)
	require.NoError(t, err)
	require.Equal(t, int64(1), move.ToGroupID)
	require.Equal(t, []int64{1, 3}, moveGroups(t, db, 7))
	_, err = r.Insert(ctx, &service.CodexTurnStateRecord{AccountID: 7, Status: "failed"})
	require.NoError(t, err)
	move, err = r.ReconcileTransfer(ctx, cfg, 7, id, true)
	require.NoError(t, err)
	require.Nil(t, move, "failure must keep old valid pin")
	require.NoError(t, r.Invalidate(ctx, 7, "fixture revoked"))
	move, err = r.ReconcileTransfer(ctx, cfg, 7, id, true)
	require.NoError(t, err)
	require.Equal(t, int64(2), move.ToGroupID)
	rec.AccountID = 8
	id, err = r.Insert(ctx, rec)
	require.NoError(t, err)
	move, err = r.ReconcileTransfer(ctx, cfg, 8, id, true)
	require.NoError(t, err)
	require.Equal(t, int64(1), move.ToGroupID, "preexisting B account can recover")
	_, err = db.Exec(`DELETE FROM account_groups WHERE account_id=8`)
	require.NoError(t, err)
	move, err = r.ReconcileTransfer(ctx, cfg, 8, id, true)
	require.NoError(t, err)
	require.Nil(t, move)
	logs, err := r.RecentTransfers(ctx)
	require.NoError(t, err)
	require.Len(t, logs, 4)
}

func TestTurnStateTransferPostgresRechecksAndConflicts(t *testing.T) {
	db, r, cfg := transferFixture(t)
	ctx := context.Background()
	changed := cfg
	changed.TransferEnabled = false
	require.NoError(t, r.SaveConfig(ctx, 1, changed))
	move, err := r.ReconcileTransfer(ctx, cfg, 7, 0, false)
	require.NoError(t, err)
	require.Nil(t, move)
	require.Equal(t, []int64{1, 3}, moveGroups(t, db, 7))
	_, err = db.Exec(`UPDATE groups SET degradation_detection_enabled=true,degradation_detection_config='{"move_on_degraded":true}' WHERE id=1`)
	require.NoError(t, err)
	require.Error(t, r.SaveConfig(ctx, 1, cfg))
	_, err = db.Exec(`UPDATE groups SET degradation_detection_enabled=false WHERE id=1`)
	require.NoError(t, err)
	require.NoError(t, r.SaveConfig(ctx, 1, cfg))
	tx, err := db.BeginTx(ctx, nil)
	require.NoError(t, err)
	require.Error(t, guardDegradationTicketTransfer(ctx, tx, 1, service.DegradationDetectionConfig{Enabled: true, MoveOnDegraded: true}))
	require.NoError(t, tx.Rollback())
	_, err = db.Exec(`INSERT INTO account_groups VALUES(7,2,42,NOW())`)
	require.NoError(t, err)
	move, err = r.ReconcileTransfer(ctx, cfg, 7, 0, false)
	require.NoError(t, err)
	require.NotNil(t, move)
	require.Equal(t, []int64{2, 3}, moveGroups(t, db, 7))
	var p int
	require.NoError(t, db.QueryRow(`SELECT priority FROM account_groups WHERE account_id=7 AND group_id=2`).Scan(&p))
	require.Equal(t, 42, p)
	_, err = db.Exec(`ALTER TABLE scheduler_outbox ADD CONSTRAINT reject_move CHECK(event_type <> 'account_groups_changed') NOT VALID`)
	require.NoError(t, err)
	_, err = db.Exec(`DELETE FROM account_groups WHERE account_id=7 AND group_id=2;INSERT INTO account_groups VALUES(7,1,17,NOW())`)
	require.NoError(t, err)
	_, err = r.ReconcileTransfer(ctx, cfg, 7, 0, false)
	require.Error(t, err)
	require.Equal(t, []int64{1, 3}, moveGroups(t, db, 7), "transaction failure preserves memberships")
}

func TestTurnStateTransferPostgresCooldown(t *testing.T) {
	db, r, _ := transferFixture(t)
	ctx := context.Background()
	now := time.Now()
	for i := 0; i < 3; i++ {
		_, err := r.ReserveAttempt(ctx, 7, 3)
		require.NoError(t, err)
	}
	w, err := r.RefreshRetryWindows(ctx, []int64{7}, 3, 300, now)
	require.NoError(t, err)
	require.WithinDuration(t, now.Add(300*time.Second), w[7].CycleAt, time.Millisecond)
	require.NoError(t, r.SaveUpstreamRetry(ctx, 7, now.Add(time.Hour)))
	restarted := &codexTurnStateRepository{db: db}
	w, err = restarted.RefreshRetryWindows(ctx, []int64{7}, 3, 300, now.Add(time.Minute))
	require.NoError(t, err)
	require.WithinDuration(t, now.Add(300*time.Second), w[7].CycleAt, time.Millisecond)
	_, _, err = r.ClearHistory(ctx, nil)
	require.NoError(t, err)
	require.NoError(t, r.ResetAttempts(ctx, 7))
	w, err = r.RefreshRetryWindows(ctx, []int64{7}, 3, 300, now.Add(time.Minute))
	require.NoError(t, err)
	require.True(t, w[7].CycleAt.IsZero())
	require.WithinDuration(t, now.Add(time.Hour), w[7].UpstreamAt, time.Millisecond, "manual reset must preserve upstream backoff")
	for i := 0; i < 3; i++ {
		_, err := r.ReserveAttempt(ctx, 7, 3)
		require.NoError(t, err)
	}
	_, err = r.RefreshRetryWindows(ctx, []int64{7}, 3, 300, now)
	require.NoError(t, err)
	_, err = r.RefreshRetryWindows(ctx, []int64{7}, 3, 300, now.Add(301*time.Second))
	require.NoError(t, err)
	counts, err := r.AttemptCounts(ctx, []int64{7})
	require.NoError(t, err)
	require.Zero(t, counts[7])
}

func TestTurnStateTransferPostgresConcurrentAndExpired(t *testing.T) {
	db, r, cfg := transferFixture(t)
	var schema string
	require.NoError(t, db.QueryRow(`SELECT current_schema()`).Scan(&schema))
	second, err := sql.Open("postgres", os.Getenv("CODEX_STATE_TEST_DSN")+" search_path="+schema)
	require.NoError(t, err)
	defer second.Close()
	other := &codexTurnStateRepository{db: second}
	start := make(chan struct{})
	var wg sync.WaitGroup
	errs := make(chan error, 2)
	var moved atomic.Int32
	for _, repo := range []*codexTurnStateRepository{r, other} {
		wg.Add(1)
		go func(repo *codexTurnStateRepository) {
			defer wg.Done()
			<-start
			move, err := repo.ReconcileTransfer(context.Background(), cfg, 7, 0, false)
			if move != nil {
				moved.Add(1)
			}
			errs <- err
		}(repo)
	}
	close(start)
	wg.Wait()
	close(errs)
	for err := range errs {
		require.NoError(t, err)
	}
	require.Equal(t, int32(1), moved.Load())
	ctx := context.Background()
	now := time.Now()
	expiry := now.Add(time.Hour)
	rec := &service.CodexTurnStateRecord{AccountID: 7, State: transferToken(now), Model: cfg.Model, Status: "active", ExpiresAt: &expiry}
	id, err := r.Insert(ctx, rec)
	require.NoError(t, err)
	_, err = r.ReconcileTransfer(ctx, cfg, 7, id, true)
	require.NoError(t, err)
	// Even a stale positive memory snapshot cannot promote/keep an expired pin.
	_, err = db.Exec(`UPDATE codex_turn_states SET expires_at=NOW()-INTERVAL '1 second' WHERE id=$1`, id)
	require.NoError(t, err)
	move, err := r.ReconcileTransfer(ctx, cfg, 7, id, true)
	require.NoError(t, err)
	require.Equal(t, int64(2), move.ToGroupID)
	// Deleted/revived IDs never accept late observations.
	_, err = db.Exec(`UPDATE accounts SET deleted_at=NOW() WHERE id=7`)
	require.NoError(t, err)
	_, err = r.Insert(ctx, rec)
	require.Error(t, err)
}

func TestTurnStateTransferPostgresBoundTicketAndRestart(t *testing.T) {
	db, r, cfg := transferFixture(t)
	now := time.Now()
	expiry := now.Add(time.Hour)
	rec := &service.CodexTurnStateRecord{AccountID: 8, State: transferToken(now), Model: cfg.Model, Status: "active", IssuedAt: &now, ExpiresAt: &expiry}
	_, err := r.Insert(context.Background(), rec)
	require.NoError(t, err)
	// Startup binds the persisted pin before moving a recovering member to A.
	svc := service.NewCodexTurnStateService(r, nil, nil, nil)
	svc.Start()
	defer svc.Stop()
	_, state, ok := svc.InjectionHeader(8, cfg.Model)
	require.True(t, ok)
	require.Equal(t, rec.State, state)
	require.Equal(t, []int64{1}, moveGroups(t, db, 8))
	require.Equal(t, []int64{2, 3}, moveGroups(t, db, 7))
	require.NoError(t, svc.Invalidate(context.Background(), 8))
	svc.Stop()
	// Explicit tick is deterministic and also exercises periodic reconciliation.
	svc.Tick(context.Background())
	require.Equal(t, []int64{2}, moveGroups(t, db, 8))
}

func TestTurnStateIndependentTransportFreshConnections(t *testing.T) {
	var connections atomic.Int32
	proxy := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(200); _, _ = w.Write([]byte("fixture")) }))
	proxy.Config.ConnState = func(_ net.Conn, s http.ConnState) {
		if s == http.StateNew {
			connections.Add(1)
		}
	}
	proxy.Start()
	defer proxy.Close()
	proxyURL, err := url.Parse(proxy.URL)
	require.NoError(t, err)
	transport, err := buildUpstreamTransport(poolSettings{}, proxyURL, upstreamProtocolModeOpenAIHarvest)
	require.NoError(t, err)
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport}
	for i := 0; i < 3; i++ {
		resp, err := client.Get("http://fixture.invalid/probe")
		require.NoError(t, err)
		_, _ = io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
	}
	require.Equal(t, int32(3), connections.Load())
	require.True(t, transport.DisableKeepAlives)
	require.False(t, transport.ForceAttemptHTTP2)
	normal, err := buildUpstreamTransport(poolSettings{}, proxyURL, upstreamProtocolModeOpenAIH1)
	require.NoError(t, err)
	defer normal.CloseIdleConnections()
	require.False(t, normal.DisableKeepAlives)
	cfg := service.NormalizeCodexTurnStateConfig(service.CodexTurnStateConfig{})
	raw, err := json.Marshal(cfg)
	require.NoError(t, err)
	require.Contains(t, string(raw), `"harvest_transport":"account"`)
}
