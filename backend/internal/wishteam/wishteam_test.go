package wishteam

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/migrations"
	"github.com/stretchr/testify/require"
)

func oldFixture() map[string]any {
	return map[string]any{
		"name": "original child", "platform": "openai", "type": "oauth", "status": "error", "schedulable": false,
		"credentials": map[string]any{
			"access_token": "old-at", "refresh_token": "old-rt", "chatgpt_account_id": "workspace-A",
			"model_mapping": map[string]any{"gpt-5": "gpt-5"}, "model_whitelist": []any{"gpt-5"},
			"future_config": map[string]any{"keep": true},
		},
		"extra":       map[string]any{"email": "child@example.com", "openai_ws_mode": "ctx_pool", "codex_fingerprint_enabled": true, "unrecognized_setting": "keep-me"},
		"concurrency": 7, "priority": 19, "rate_multiplier": 0.5, "proxy_id": 42,
	}
}
func freshFixture() map[string]any {
	return map[string]any{
		"name": "provider name", "platform": "openai", "type": "oauth",
		"credentials": map[string]any{"access_token": "new-at", "refresh_token": "new-rt", "chatgpt_account_id": "workspace-A", "model_whitelist": []any{"wrong-model"}},
		"extra":       map[string]any{"email": "child@example.com", "plan_type": "team", "openai_ws_mode": "disabled"},
		"concurrency": 1, "priority": 1, "proxy_id": nil,
	}
}
func revived() RemoteItem {
	return RemoteItem{Email: "child@example.com", Status: "revived", Probe: Probe{HTTPStatus: 401}}
}

func TestReplacementPreservesEveryConfig(t *testing.T) {
	old := oldFixture()
	out, err := replacement(old, freshFixture(), revived())
	require.NoError(t, err)
	for _, k := range []string{"name", "concurrency", "priority", "proxy_id", "rate_multiplier"} {
		want, _ := json.Marshal(old[k])
		got, _ := json.Marshal(out[k])
		require.JSONEq(t, string(want), string(got), k)
	}
	require.Equal(t, object(old["credentials"])["model_whitelist"], object(out["credentials"])["model_whitelist"])
	require.Equal(t, object(old["extra"])["openai_ws_mode"], object(out["extra"])["openai_ws_mode"])
	require.Equal(t, true, object(out["extra"])["codex_fingerprint_enabled"])
	require.Equal(t, "keep-me", object(out["extra"])["unrecognized_setting"])
	require.Equal(t, "new-at", object(out["credentials"])["access_token"])
	require.Equal(t, "old-at", object(old["credentials"])["access_token"])
	require.Equal(t, "active", out["status"])
	require.Equal(t, true, out["schedulable"])
}

func TestReplacementRejectsUncertainEvidenceAndWrongIdentity(t *testing.T) {
	for _, kind := range []string{"healthy", "no_evidence", "workspace", "email", "missing_token", "wrong_type"} {
		t.Run(kind, func(t *testing.T) {
			old, fresh, v := oldFixture(), freshFixture(), revived()
			switch kind {
			case "healthy":
				v.Status = "alive"
			case "no_evidence":
				v.Probe = Probe{Reason: "probe_unavailable"}
			case "workspace":
				object(fresh["credentials"])["chatgpt_account_id"] = "workspace-B"
			case "email":
				object(fresh["extra"])["email"] = "other@example.com"
			case "missing_token":
				delete(object(fresh["credentials"]), "access_token")
			case "wrong_type":
				fresh["platform"] = "grok"
			}
			_, err := replacement(old, fresh, v)
			require.Error(t, err)
		})
	}
	v := RemoteItem{Email: "child@example.com", Status: "alive", ReusedCurrent: true, Probe: Probe{PlanType: "free"}}
	_, err := replacement(oldFixture(), freshFixture(), v)
	require.NoError(t, err)
}

func TestOutboundExcludesLocalSettingsAndProxy(t *testing.T) {
	raw, err := json.Marshal(outbound(oldFixture()))
	require.NoError(t, err)
	require.Contains(t, string(raw), "old-at")
	for _, secret := range []string{"model_whitelist", "proxy_id", "openai_ws_mode", "future_config"} {
		require.NotContains(t, string(raw), secret)
	}
}

func TestClientHonorsRetryAndRejectsSuccessFalseAndRedirect(t *testing.T) {
	for _, tc := range []struct {
		name  string
		code  int
		body  string
		retry int
	}{
		{"rate_limit", 429, `{"success":false,"retry_after":60}`, 60},
		{"body_limit", 200, `{"success":false,"retry_after":31}`, 31},
		{"not_success", 200, `{"success":false,"error":"sensitive-token-must-not-leak"}`, 0},
		{"bad_json", 502, `<html>token</html>`, 0},
		{"redirect", 307, `{}`, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Location", "https://example.invalid/")
				w.WriteHeader(tc.code)
				fmt.Fprint(w, tc.body)
			}))
			defer server.Close()
			c := newClient()
			c.base = server.URL
			_, err := c.submit(context.Background(), document(nil))
			require.Error(t, err)
			require.NotContains(t, err.Error(), "sensitive-token")
			e := err.(*remoteError)
			require.Equal(t, tc.retry, e.retry)
		})
	}
}

func testDB(t *testing.T) *sql.DB {
	t.Helper()
	dsn := os.Getenv("WISHTEAM_TEST_DSN")
	if dsn == "" {
		t.Skip("WISHTEAM_TEST_DSN not set; use a disposable local PostgreSQL DB")
	}
	base, err := sql.Open("postgres", dsn)
	require.NoError(t, err)
	schema := fmt.Sprintf("wishteam_test_%d", time.Now().UnixNano())
	_, err = base.Exec("CREATE SCHEMA " + schema)
	require.NoError(t, err)
	db, err := sql.Open("postgres", dsn+" search_path="+schema)
	require.NoError(t, err)
	db.SetMaxOpenConns(6)
	t.Cleanup(func() { db.Close(); _, _ = base.Exec("DROP SCHEMA " + schema + " CASCADE"); base.Close() })
	_, err = db.Exec(`
CREATE TABLE groups(id bigserial PRIMARY KEY,name text,platform text DEFAULT 'openai',deleted_at timestamptz);
CREATE TABLE accounts(id bigserial PRIMARY KEY,name text,notes text,platform text,type text,
 credentials jsonb NOT NULL DEFAULT '{}',extra jsonb NOT NULL DEFAULT '{}',proxy_id bigint,proxy_fallback_origin_id bigint,
 concurrency integer DEFAULT 3,load_factor integer,priority integer DEFAULT 50,rate_multiplier numeric(10,4) DEFAULT 1,
 status text DEFAULT 'active',error_message text,schedulable boolean DEFAULT true,expires_at timestamptz,
 auto_pause_on_expired boolean DEFAULT true,parent_account_id bigint,quota_dimension text DEFAULT 'global',
 future_custom_config jsonb DEFAULT '{"preserve":true}',degradation_suspend_note text DEFAULT 'keep',
 deleted_at timestamptz,created_at timestamptz DEFAULT NOW(),updated_at timestamptz DEFAULT NOW());
CREATE TABLE account_groups(account_id bigint REFERENCES accounts(id),group_id bigint REFERENCES groups(id),priority integer DEFAULT 50,created_at timestamptz DEFAULT NOW(),PRIMARY KEY(account_id,group_id));
CREATE TABLE scheduled_test_plans(id bigserial PRIMARY KEY,account_id bigint REFERENCES accounts(id),configuration jsonb);
CREATE TABLE scheduler_outbox(id bigserial PRIMARY KEY,event_type text,account_id bigint,payload jsonb);
INSERT INTO groups(id,name) VALUES(1,'monitored'),(2,'other'),(3,'also assigned');
`)
	require.NoError(t, err)
	migration, err := migrations.FS.ReadFile("244_wishteam5x.sql")
	require.NoError(t, err)
	_, err = db.Exec(string(migration))
	require.NoError(t, err)
	return db
}
func seed(t *testing.T, db *sql.DB, email string, group int64) int64 {
	t.Helper()
	a := oldFixture()
	object(a["extra"])["email"] = email
	cred, _ := json.Marshal(a["credentials"])
	extra, _ := json.Marshal(a["extra"])
	var id int64
	err := db.QueryRow(`INSERT INTO accounts(name,platform,type,credentials,extra,proxy_id,concurrency,priority,rate_multiplier,status,error_message,schedulable)
VALUES('original child','openai','oauth',$1,$2,42,7,19,0.5,'error','401',false) RETURNING id`, string(cred), string(extra)).Scan(&id)
	require.NoError(t, err)
	_, err = db.Exec(`INSERT INTO account_groups(account_id,group_id,priority) VALUES($1,$2,17)`, id, group)
	require.NoError(t, err)
	return id
}
func startFixture(t *testing.T, db *sql.DB) (*Worker, int64, int64) {
	t.Helper()
	w := New(db)
	require.NoError(t, w.Configure(context.Background(), Config{GroupID: 1, IntervalMinutes: 10}))
	id, err := w.StartRun(context.Background(), 1, false)
	require.NoError(t, err)
	var item int64
	require.NoError(t, db.QueryRow(`SELECT id FROM wishteam_items WHERE run_id=$1 ORDER BY id LIMIT 1`, id).Scan(&item))
	return w, id, item
}

func TestPostgresAtomicArchiveDeleteImportRestore(t *testing.T) {
	db := testDB(t)
	oldID := seed(t, db, "child@example.com", 1)
	_, err := db.Exec(`INSERT INTO account_groups(account_id,group_id,priority) VALUES($1,3,91)`, oldID)
	require.NoError(t, err)
	_, err = db.Exec(`INSERT INTO scheduled_test_plans(account_id,configuration) VALUES($1,'{"model":"keep"}')`, oldID)
	require.NoError(t, err)
	w, _, item := startFixture(t, db)
	// Prove the actual SQL statement ordering, not just the final state.
	_, err = db.Exec(`CREATE FUNCTION assert_replacement_order() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF NEW.credentials->>'access_token'='new-at' THEN
  IF NOT EXISTS(SELECT 1 FROM accounts WHERE credentials->>'access_token'='old-at' AND deleted_at IS NOT NULL)
   OR NOT EXISTS(SELECT 1 FROM wishteam_archives) THEN
   RAISE EXCEPTION 'archive and delete must precede import';
  END IF;
 END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER verify_order BEFORE INSERT ON accounts FOR EACH ROW EXECUTE FUNCTION assert_replacement_order()`)
	require.NoError(t, err)
	require.NoError(t, w.replace(context.Background(), item, freshFixture(), revived(), document([]map[string]any{freshFixture()})))
	var newID int64
	var oldDeleted bool
	var groupCount, planCount, outboxCount int
	require.NoError(t, db.QueryRow(`SELECT new_account_id FROM wishteam_items WHERE id=$1`, item).Scan(&newID))
	require.NotEqual(t, oldID, newID)
	require.NoError(t, db.QueryRow(`SELECT deleted_at IS NOT NULL FROM accounts WHERE id=$1`, oldID).Scan(&oldDeleted))
	require.True(t, oldDeleted)
	require.NoError(t, db.QueryRow(`SELECT count(*) FROM account_groups WHERE account_id=$1`, newID).Scan(&groupCount))
	require.Equal(t, 2, groupCount)
	require.NoError(t, db.QueryRow(`SELECT count(*) FROM scheduled_test_plans WHERE account_id=$1`, newID).Scan(&planCount))
	require.Equal(t, 1, planCount)
	require.NoError(t, db.QueryRow(`SELECT count(*) FROM scheduler_outbox`).Scan(&outboxCount))
	require.Equal(t, 2, outboxCount)
	var proof bool
	require.NoError(t, db.QueryRow(`SELECT proxy_id=42 AND concurrency=7 AND priority=19 AND rate_multiplier=0.5 AND schedulable
AND credentials->>'access_token'='new-at' AND credentials->'model_whitelist'='["gpt-5"]'::jsonb
AND extra->>'openai_ws_mode'='ctx_pool' AND extra->>'codex_fingerprint_enabled'='true'
AND future_custom_config='{"preserve":true}'::jsonb AND degradation_suspend_note='keep'
FROM accounts WHERE id=$1`, newID).Scan(&proof))
	require.True(t, proof)
	// The second application after restart must not create another account.
	require.NoError(t, w.replace(context.Background(), item, freshFixture(), revived(), document(nil)))
	var archives int
	require.NoError(t, db.QueryRow(`SELECT count(*) FROM wishteam_archives`).Scan(&archives))
	require.Equal(t, 1, archives)
	summary, err := w.Archive(context.Background(), item)
	require.NoError(t, err)
	raw, _ := json.Marshal(summary)
	require.NotContains(t, string(raw), "old-at")
	require.NotContains(t, string(raw), "new-rt")
}

func TestPostgresFailedImportRollsBackOldAccountAndMemberships(t *testing.T) {
	db := testDB(t)
	oldID := seed(t, db, "child@example.com", 1)
	w, _, item := startFixture(t, db)
	_, err := db.Exec(`ALTER TABLE accounts ADD CONSTRAINT reject_new_token CHECK(credentials->>'access_token'<>'new-at')`)
	require.NoError(t, err)
	require.Error(t, w.replace(context.Background(), item, freshFixture(), revived(), document(nil)))
	var ok bool
	require.NoError(t, db.QueryRow(`SELECT deleted_at IS NULL AND credentials->>'access_token'='old-at' AND EXISTS(SELECT 1 FROM account_groups WHERE account_id=$1) FROM accounts WHERE id=$1`, oldID).Scan(&ok))
	require.True(t, ok)
	var count int
	require.NoError(t, db.QueryRow(`SELECT count(*) FROM wishteam_archives`).Scan(&count))
	require.Zero(t, count)
	require.NoError(t, db.QueryRow(`SELECT count(*) FROM accounts`).Scan(&count))
	require.Equal(t, 1, count)
}

func TestPostgresConcurrentCredentialChangeAndGroupRemoval(t *testing.T) {
	for _, change := range []string{"token", "group"} {
		t.Run(change, func(t *testing.T) {
			db := testDB(t)
			id := seed(t, db, "child@example.com", 1)
			w, _, item := startFixture(t, db)
			var err error
			if change == "token" {
				_, err = db.Exec(`UPDATE accounts SET credentials=jsonb_set(credentials,'{access_token}','"newer-local-token"') WHERE id=$1`, id)
			} else {
				_, err = db.Exec(`DELETE FROM account_groups WHERE account_id=$1`, id)
			}
			require.NoError(t, err)
			require.Error(t, w.replace(context.Background(), item, freshFixture(), revived(), document(nil)))
			var deleted bool
			require.NoError(t, db.QueryRow(`SELECT deleted_at IS NOT NULL FROM accounts WHERE id=$1`, id).Scan(&deleted))
			require.False(t, deleted)
		})
	}
}

func TestPostgresWorkerEndToEndResumePartialAndSecretFreeProgress(t *testing.T) {
	db := testDB(t)
	seed(t, db, "child@example.com", 1)
	seed(t, db, "healthy@example.com", 1)
	seed(t, db, "dead@example.com", 1)
	outside := seed(t, db, "outside@example.com", 2)
	w, runID, _ := startFixture(t, db)
	var posts atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(out http.ResponseWriter, r *http.Request) {
		out.Header().Set("Content-Type", "application/json")
		if r.Method == "POST" {
			posts.Add(1)
			var payload struct {
				Sub2 Document `json:"sub2"`
			}
			require.NoError(t, json.NewDecoder(r.Body).Decode(&payload))
			require.Len(t, payload.Sub2.Accounts, 3)
			for _, a := range payload.Sub2.Accounts {
				require.NotEqual(t, "outside@example.com", emailOf(a))
			}
			fmt.Fprint(out, `{"success":true,"task_id":"secret-task-12345","task":{"status":"queued"}}`)
			return
		}
		if strings.HasSuffix(r.URL.Path, "/result") {
			result := RemoteReply{Success: true, Results: []RemoteItem{revived(), {Email: "healthy@example.com", Status: "alive"}, {Email: "dead@example.com", Status: "failed", ErrorCode: "workspace_deactivated"}}, Sub2: document([]map[string]any{freshFixture()})}
			require.NoError(t, json.NewEncoder(out).Encode(result))
			return
		}
		fmt.Fprint(out, `{"success":true,"task":{"status":"done","items":[{"email":"child@example.com","stage":"verifying","probe":{"http_status":401}}]}}`)
	}))
	defer server.Close()
	w.client.base = server.URL
	require.NoError(t, w.step(context.Background())) // prepare
	require.NoError(t, w.step(context.Background())) // submit
	require.Equal(t, int32(1), posts.Load())
	// Fresh worker instance simulates a process restart after submit.
	w = New(db)
	w.client.base = server.URL
	_, err := db.Exec(`UPDATE wishteam_batches SET next_poll_at=NOW()`)
	require.NoError(t, err)
	for n := 0; n < 4; n++ {
		require.NoError(t, w.step(context.Background()))
	}
	runs, err := w.Runs(context.Background())
	require.NoError(t, err)
	require.Equal(t, "done", runs[0].Status)
	require.Equal(t, 1, runs[0].Revived)
	require.Equal(t, 1, runs[0].Alive)
	require.Equal(t, 1, runs[0].Dead)
	require.Equal(t, int32(1), posts.Load())
	items, _, err := w.Items(context.Background(), runID, 1)
	require.NoError(t, err)
	raw, _ := json.Marshal(items)
	for _, secret := range []string{"secret-task-12345", "old-at", "new-at", "refresh_token"} {
		require.NotContains(t, string(raw), secret)
	}
	var untouched bool
	require.NoError(t, db.QueryRow(`SELECT deleted_at IS NULL AND credentials->>'access_token'='old-at' FROM accounts WHERE id=$1`, outside).Scan(&untouched))
	require.True(t, untouched)
}

func TestPostgresDuplicateBusyBatchLimitAndAmbiguousSubmission(t *testing.T) {
	db := testDB(t)
	seed(t, db, "duplicate@example.com", 1)
	seed(t, db, "duplicate@example.com", 1)
	for i := 0; i < 201; i++ {
		seed(t, db, fmt.Sprintf("child%d@example.com", i), 1)
	}
	w, _, _ := startFixture(t, db)
	_, err := w.StartRun(context.Background(), 1, false)
	require.ErrorIs(t, err, ErrBusy)
	require.NoError(t, w.step(context.Background()))
	var count int
	require.NoError(t, db.QueryRow(`SELECT count(*) FROM wishteam_items WHERE batch_id IS NOT NULL`).Scan(&count))
	require.Equal(t, 200, count)
	require.NoError(t, db.QueryRow(`SELECT count(*) FROM wishteam_items WHERE status='skipped'`).Scan(&count))
	require.Equal(t, 2, count)
	_, err = db.Exec(`UPDATE wishteam_batches SET status='submitting';UPDATE wishteam_items SET status='checking' WHERE batch_id IS NOT NULL`)
	require.NoError(t, err)
	require.NoError(t, w.step(context.Background()))
	require.NoError(t, db.QueryRow(`SELECT count(*) FROM wishteam_items WHERE status='failed'`).Scan(&count))
	require.Equal(t, 200, count)
}

func TestPostgresRateLimitAndRetryAfterAreDurable(t *testing.T) {
	db := testDB(t)
	seed(t, db, "child@example.com", 1)
	w, _, _ := startFixture(t, db)
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(out http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		out.WriteHeader(429)
		fmt.Fprint(out, `{"success":false,"retry_after":60}`)
	}))
	defer server.Close()
	w.client.base = server.URL
	require.NoError(t, w.step(context.Background()))
	require.NoError(t, w.step(context.Background()))
	require.Equal(t, int32(1), calls.Load())
	// A fresh process still observes the same global submission cooldown.
	w = New(db)
	w.client.base = server.URL
	require.NoError(t, w.step(context.Background()))
	require.Equal(t, int32(1), calls.Load())
	var waits bool
	require.NoError(t, db.QueryRow(`SELECT submit_after>NOW()+INTERVAL '50 seconds' FROM wishteam_settings`).Scan(&waits))
	require.True(t, waits)
	var retry int
	require.NoError(t, db.QueryRow(`SELECT retry_after FROM wishteam_items LIMIT 1`).Scan(&retry))
	require.Equal(t, 60, retry)
}

func TestPostgresUsesLatestSettingsButPreservesDisabledStatus(t *testing.T) {
	db := testDB(t)
	old := seed(t, db, "child@example.com", 1)
	w, _, item := startFixture(t, db)
	_, err := db.Exec(`UPDATE accounts SET concurrency=12,priority=4,status='disabled',
extra=jsonb_set(extra,'{new_setting}','"retain"') WHERE id=$1`, old)
	require.NoError(t, err)
	require.NoError(t, w.replace(context.Background(), item, freshFixture(), revived(), document(nil)))
	var preserved bool
	require.NoError(t, db.QueryRow(`SELECT concurrency=12 AND priority=4 AND status='disabled'
AND extra->>'new_setting'='retain' FROM accounts WHERE id=(SELECT new_account_id FROM wishteam_items WHERE id=$1)`, item).Scan(&preserved))
	require.True(t, preserved)
}

// Regression for the production incident: revoked-token SetError also writes
// schedulable=false. A successful revive must not inherit that error side-effect.
func TestWishTeamReviveRestoresErrorScheduling(t *testing.T) {
	for _, tc := range []struct {
		name, status    string
		schedulable     bool
		wantStatus      string
		wantSchedulable bool
	}{
		{"401_error", "error", false, "active", true},
		{"error_already_schedulable", "error", true, "active", true},
		{"manual_stop", "active", false, "active", false},
		{"active", "active", true, "active", true},
		{"manual_disabled", "disabled", false, "disabled", false},
		{"manual_inactive", "inactive", false, "inactive", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			old := oldFixture()
			old["status"] = tc.status
			old["schedulable"] = tc.schedulable
			old["temp_unschedulable_until"] = "2026-10-01T00:00:00Z"
			old["degradation_suspended_until"] = "2026-10-02T00:00:00Z"
			result, err := replacement(old, freshFixture(), revived())
			require.NoError(t, err)
			require.Equal(t, tc.wantStatus, result["status"])
			require.Equal(t, tc.wantSchedulable, result["schedulable"])
			require.Equal(t, old["temp_unschedulable_until"], result["temp_unschedulable_until"])
			require.Equal(t, old["degradation_suspended_until"], result["degradation_suspended_until"])
		})
	}
}

func TestWishTeamReusedCurrentRestoresErrorScheduling(t *testing.T) {
	verdict := RemoteItem{
		Email: "child@example.com", Status: "alive", ReusedCurrent: true,
		Probe: Probe{PlanType: "free"},
	}
	out, err := replacement(oldFixture(), freshFixture(), verdict)
	require.NoError(t, err)
	require.Equal(t, "active", out["status"])
	require.Equal(t, true, out["schedulable"])
}
