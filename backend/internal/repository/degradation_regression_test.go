package repository

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

// Explicit opt-in: these tests create only a uniquely named disposable schema.
// No production migrations, credentials, or upstream model requests are used.
func degradationTestDB(t *testing.T) *sql.DB {
	t.Helper()
	dsn := os.Getenv("DEGRADATION_TEST_DSN")
	if dsn == "" {
		t.Skip("set DEGRADATION_TEST_DSN to a disposable PostgreSQL database")
	}
	db, err := sql.Open("postgres", dsn)
	require.NoError(t, err)
	db.SetMaxOpenConns(1)
	schema := fmt.Sprintf("degradation_test_%d", time.Now().UnixNano())
	_, err = db.Exec("CREATE SCHEMA " + schema)
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = db.Exec("DROP SCHEMA " + schema + " CASCADE")
		_ = db.Close()
	})
	_, err = db.Exec("SET search_path TO " + schema)
	require.NoError(t, err)
	_, err = db.Exec(`
CREATE TABLE groups(id bigint PRIMARY KEY, name text DEFAULT '', platform text DEFAULT 'openai',
 deleted_at timestamptz, updated_at timestamptz DEFAULT NOW(), degradation_detection_enabled boolean DEFAULT false,
 degradation_preview_enabled boolean DEFAULT false, degradation_detection_config jsonb DEFAULT '{}');
CREATE TABLE accounts(id bigint PRIMARY KEY, name text DEFAULT '', deleted_at timestamptz, schedulable boolean DEFAULT true,
 extra jsonb DEFAULT '{}', temp_unschedulable_until timestamptz, temp_unschedulable_reason text,
 degradation_suspended_until timestamptz, degradation_suspended_at timestamptz, degradation_suspend_note text DEFAULT '',
 updated_at timestamptz DEFAULT NOW());
CREATE TABLE account_groups(account_id bigint, group_id bigint, priority int DEFAULT 50, created_at timestamptz DEFAULT NOW(), PRIMARY KEY(account_id,group_id));
CREATE TABLE test_settings(test_type text PRIMARY KEY, enabled boolean DEFAULT false, user_visible boolean DEFAULT false, updated_at timestamptz DEFAULT NOW());
CREATE TABLE settings(key text PRIMARY KEY,value text NOT NULL,updated_at timestamptz NOT NULL DEFAULT NOW());
CREATE TABLE account_tests(id bigserial PRIMARY KEY, account_id bigint, test_type text, status text DEFAULT 'queued',
 score double precision, result text DEFAULT '', result_image text DEFAULT '', input text DEFAULT '',
 raw_response text DEFAULT '', raw_truncated boolean DEFAULT false, error_message text DEFAULT '', duration_ms bigint DEFAULT 0,
 model text DEFAULT '', anti_degradation boolean DEFAULT false, config_snapshot jsonb, evaluation jsonb DEFAULT '{}',
 requested_by bigint, lease_token text, lease_until timestamptz, queue_reason text DEFAULT '', available_at timestamptz,
 started_at timestamptz, finished_at timestamptz, created_at timestamptz DEFAULT NOW());
CREATE TABLE scheduler_outbox(id bigserial, event_type text, account_id bigint, group_id bigint, payload jsonb, dedup_key text);
CREATE TABLE usage_logs(id bigserial PRIMARY KEY, account_id bigint, requested_model text, model text,
 upstream_response_model text, created_at timestamptz DEFAULT NOW());
CREATE UNIQUE INDEX dedup ON scheduler_outbox(dedup_key) WHERE dedup_key IS NOT NULL;
CREATE TABLE users(id bigint, email text, role text);
CREATE TABLE audit_logs(actor_user_id bigint,actor_email text,actor_role text,action text,method text,path text,status_code int,extra jsonb);
INSERT INTO groups(id,degradation_detection_enabled,degradation_preview_enabled) VALUES (1,true,true),(2,false,true),(3,true,false);
INSERT INTO accounts(id) VALUES(10),(20),(30),(40);
INSERT INTO account_groups(account_id,group_id) VALUES(10,1),(20,2),(30,3),(40,1),(40,3);
INSERT INTO test_settings(test_type,enabled) VALUES('degradation_probe',true),('degradation_preview',true);
`)
	require.NoError(t, err)
	return db
}

func TestDegradationPostgresSavedAnswerAndEligibility(t *testing.T) {
	db := degradationTestDB(t)
	r := &degradationRepository{db: db}
	ctx := context.Background()
	cfg := service.NormalizeDegradationConfig(service.DegradationDetectionConfig{
		Enabled: true, PreviewEnabled: true, ExpectedAnswer: "29",
	})
	require.NoError(t, r.UpdateGroupConfig(ctx, 1, 1, cfg))
	saved, err := r.GroupConfig(ctx, 1)
	require.NoError(t, err)
	require.Equal(t, "29", saved.ExpectedAnswer)
	for _, tc := range []struct {
		group, account int64
		kind           string
		created        bool
	}{
		{2, 20, service.DegradationTestTypeProbe, false},   // disabled group
		{1, 20, service.DegradationTestTypeProbe, false},   // not a member
		{3, 30, service.DegradationTestTypePreview, false}, // preview disabled
		{1, 10, service.DegradationTestTypeProbe, true},
		{1, 10, service.DegradationTestTypeProbe, false}, // idempotent
		{1, 10, service.DegradationTestTypePreview, true},
		{1, 40, service.DegradationTestTypePreview, false}, // one artwork per group
	} {
		_, created, err := r.EnqueueDegradationTest(ctx, tc.group, tc.account, tc.kind, "prompt", "test", "medium", "21", 300)
		require.NoError(t, err)
		require.Equal(t, tc.created, created, "%+v", tc)
	}
	var answer, source string
	require.NoError(t, db.QueryRow(`SELECT config_snapshot->>'expected_answer',config_snapshot->>'degradation_group_id' FROM account_tests WHERE test_type='degradation_probe'`).Scan(&answer, &source))
	require.Equal(t, "29", answer)
	require.Equal(t, "1", source)
	_, err = db.Exec(`UPDATE account_tests SET status='completed',finished_at=NOW()`)
	require.NoError(t, err)
	ids, err := r.DueProbeAccountIDs(ctx, 1, 10, 60)
	require.NoError(t, err)
	require.NotContains(t, ids, int64(10))
	ids, err = r.DuePreviewAccountIDs(ctx, []int64{1}, 10, 1)
	require.NoError(t, err)
	require.Empty(t, ids)
	// A shared account retains separate task snapshots for each enabled group.
	for _, group := range []int64{1, 3} {
		_, created, err := r.EnqueueDegradationTest(ctx, group, 40, service.DegradationTestTypeProbe, "prompt", "test", "medium", fmt.Sprint(group+28), 300)
		require.NoError(t, err)
		require.True(t, created)
	}
	var count int
	require.NoError(t, db.QueryRow(`SELECT COUNT(DISTINCT config_snapshot->>'expected_answer') FROM account_tests WHERE account_id=40 AND test_type='degradation_probe'`).Scan(&count))
	require.Equal(t, 2, count)
	// A failed preview retries after one minute instead of its normal interval.
	_, err = db.Exec(`UPDATE account_tests SET status='failed',finished_at=NOW()-INTERVAL '2 minutes' WHERE test_type='degradation_preview'`)
	require.NoError(t, err)
	ids, err = r.DuePreviewAccountIDs(ctx, []int64{1}, 10, 1)
	require.NoError(t, err)
	require.Len(t, ids, 1)
	_, err = db.Exec(`INSERT INTO account_tests(account_id,test_type) SELECT 10,'degradation_probe' FROM generate_series(1,200)`)
	require.NoError(t, err)
	_, created, err := r.EnqueueDegradationTest(ctx, 3, 30, service.DegradationTestTypeProbe, "prompt", "test", "medium", "21", 300)
	require.NoError(t, err)
	require.False(t, created, "the SQL enqueue must enforce the hard queue limit")
}

func TestDegradationPostgresClaimRechecksGroupAndMembership(t *testing.T) {
	for _, change := range []string{
		`UPDATE groups SET degradation_detection_enabled=false WHERE id=1`,
		`DELETE FROM account_groups WHERE group_id=1`,
		`UPDATE accounts SET schedulable=false WHERE id=10`,
		`UPDATE groups SET degradation_preview_enabled=false WHERE id=1`,
	} {
		t.Run(change, func(t *testing.T) {
			db := degradationTestDB(t)
			r := &degradationRepository{db: db}
			ctx := context.Background()
			id, created, err := r.EnqueueDegradationTest(ctx, 1, 10, service.DegradationTestTypePreview, "prompt", "test", "low", "", 300)
			require.NoError(t, err)
			require.True(t, created)
			_, err = db.Exec(change)
			require.NoError(t, err)
			record, err := (&intelligentTestRepository{db: db}).Claim(ctx)
			require.NoError(t, err)
			require.Nil(t, record)
			var status string
			require.NoError(t, db.QueryRow(`SELECT status FROM account_tests WHERE id=$1`, id).Scan(&status))
			require.Equal(t, "cancelled", status)
		})
	}
}

func TestDegradationPostgresPauseOwnershipAndOutbox(t *testing.T) {
	db := degradationTestDB(t)
	r := &degradationRepository{db: db}
	ctx := context.Background()
	_, err := db.Exec(`UPDATE accounts SET temp_unschedulable_until=NOW()+INTERVAL '2 hours',temp_unschedulable_reason='other protection' WHERE id=10`)
	require.NoError(t, err)
	note := "降智检测[分组1]：测试"
	changed, err := r.ApplyProbeOutcome(ctx, 10, 1, true, 30, note)
	require.NoError(t, err)
	require.False(t, changed)
	var reason string
	require.NoError(t, db.QueryRow(`SELECT temp_unschedulable_reason FROM accounts WHERE id=10`).Scan(&reason))
	require.Equal(t, "other protection", reason)
	changed, err = r.ApplyProbeOutcome(ctx, 40, 1, true, 30, note)
	require.NoError(t, err)
	require.True(t, changed)
	var count int
	require.NoError(t, db.QueryRow(`SELECT COUNT(*) FROM scheduler_outbox`).Scan(&count))
	require.Equal(t, 1, count)
	changed, err = r.ApplyProbeOutcome(ctx, 40, 3, false, 0, "")
	require.NoError(t, err)
	require.False(t, changed, "a different group's success must not lift this pause")
	_, err = db.Exec(`TRUNCATE scheduler_outbox`)
	require.NoError(t, err)
	changed, err = r.ApplyProbeOutcome(ctx, 40, 1, false, 0, "")
	require.NoError(t, err)
	require.True(t, changed)
	require.NoError(t, db.QueryRow(`SELECT COUNT(*) FROM scheduler_outbox`).Scan(&count))
	require.Equal(t, 1, count, "recovery publishes an outbox event too")
	var until sql.NullTime
	require.NoError(t, db.QueryRow(`SELECT temp_unschedulable_until FROM accounts WHERE id=40`).Scan(&until))
	require.False(t, until.Valid)
	_, err = db.Exec(`UPDATE groups SET degradation_detection_enabled=false WHERE id=1`)
	require.NoError(t, err)
	changed, err = r.ApplyProbeOutcome(ctx, 40, 1, true, 30, note)
	require.NoError(t, err)
	require.False(t, changed)
}

func TestDegradationPostgresTimelineIncludesEveryWindow(t *testing.T) {
	db := degradationTestDB(t)
	r := &degradationRepository{db: db}
	for _, hours := range []int{24, 72, 168} {
		_, err := db.Exec(`TRUNCATE account_tests`)
		require.NoError(t, err)
		_, err = db.Exec(`INSERT INTO account_tests(account_id,test_type,status,evaluation,finished_at) VALUES
 (10,'degradation_probe','completed','{"answer_verdict":"correct"}',NOW()-make_interval(hours => $1::int)+INTERVAL '1 minute'),
 (10,'degradation_probe','completed','{"answer_verdict":"incorrect"}',NOW()-INTERVAL '1 minute')`, hours)
		require.NoError(t, err)
		timeline, err := r.Timeline(context.Background(), hours)
		require.NoError(t, err)
		require.Len(t, timeline.Buckets, 73)
		require.Equal(t, hours*60/72, timeline.BucketMinute)
		require.EqualValues(t, 2, timeline.Total, "range=%dh", hours)
		require.EqualValues(t, 1, timeline.Correct)
		require.EqualValues(t, 1, timeline.Degraded)
		require.InDelta(t, .5, timeline.HealthyRatio, .001)
	}
}

func TestDegradationPostgresPublicStatsResetKeepsHistoryAndExcludesInflight(t *testing.T) {
	db := degradationTestDB(t)
	r := &degradationRepository{db: db}
	ctx := context.Background()
	_, err := db.Exec(`INSERT INTO users VALUES(1,'admin@test','admin');
INSERT INTO account_tests(account_id,test_type,status,evaluation,created_at,finished_at) VALUES
(10,'degradation_probe','completed','{"answer_verdict":"incorrect"}',NOW()-INTERVAL '2 minutes',NOW()),
(10,'degradation_probe','running','{}',NOW()-INTERVAL '1 minute',NULL),
(10,'degradation_preview','completed','{}',NOW(),NOW());
UPDATE accounts SET degradation_suspended_until=NOW()+INTERVAL '1 hour' WHERE id=10`)
	require.NoError(t, err)
	before, err := r.Timeline(ctx, 24)
	require.NoError(t, err)
	require.EqualValues(t, 1, before.Total)
	at, err := r.ResetPublicStats(ctx, 1)
	require.NoError(t, err)
	_, err = db.Exec(`UPDATE account_tests SET status='completed',evaluation='{"answer_verdict":"incorrect"}',finished_at=NOW() WHERE status='running'`)
	require.NoError(t, err)
	for _, hours := range []int{24, 72, 168} {
		out, err := r.Timeline(ctx, hours)
		require.NoError(t, err)
		require.EqualValues(t, 0, out.Total)
		require.Equal(t, "unknown", out.CurrentState)
		require.Nil(t, out.LastProbeAt)
		require.NotNil(t, out.ResetAt)
		require.True(t, at.Equal(*out.ResetAt))
		require.EqualValues(t, 1, out.Suspended, "reset must not release accounts")
	}
	var count int
	require.NoError(t, db.QueryRow(`SELECT COUNT(*) FROM account_tests`).Scan(&count))
	require.Equal(t, 3, count, "all historical rows and artwork remain")
	require.NoError(t, db.QueryRow(`SELECT COUNT(*) FROM audit_logs WHERE action='admin.degradation_detection.stats_reset'`).Scan(&count))
	require.Equal(t, 1, count)
	_, err = db.Exec(`INSERT INTO account_tests(account_id,test_type,status,evaluation,created_at,finished_at) VALUES(10,'degradation_probe','completed','{"answer_verdict":"correct"}',clock_timestamp(),clock_timestamp())`)
	require.NoError(t, err)
	out, err := (&degradationRepository{db: db}).Timeline(ctx, 168)
	require.NoError(t, err)
	require.EqualValues(t, 1, out.Total)
	require.EqualValues(t, 1, out.Correct)
	require.Equal(t, "healthy", out.CurrentState)
	_, err = r.ResetPublicStats(ctx, 1)
	require.NoError(t, err)
	out, err = r.Timeline(ctx, 24)
	require.NoError(t, err)
	require.Zero(t, out.Total)
}

func TestDegradationPostgresPublicWorksRespectGroupSwitches(t *testing.T) {
	db := degradationTestDB(t)
	r := &degradationRepository{db: db}
	_, err := db.Exec(`INSERT INTO account_tests(id,account_id,test_type,status,result_image,config_snapshot,finished_at) VALUES
 (1,10,'degradation_preview','completed','<svg/>','{"degradation_group_id":1}',NOW()),
 (2,20,'degradation_preview','completed','<svg/>','{"degradation_group_id":2}',NOW()),
 (3,30,'degradation_preview','completed','<svg/>','{"degradation_group_id":3}',NOW())`)
	require.NoError(t, err)
	page, err := r.PublicPage(context.Background(), 1, 12)
	require.NoError(t, err)
	require.True(t, page.Enabled)
	source, err := r.PublicAnimationSource(context.Background(), 1)
	require.NoError(t, err)
	require.Equal(t, "<svg/>", source)
	for _, hiddenID := range []int64{2, 3, 999} {
		_, err = r.PublicAnimationSource(context.Background(), hiddenID)
		require.ErrorIs(t, err, service.ErrDegradationWorkNotFound)
	}
	require.EqualValues(t, 1, page.Total)
	require.Len(t, page.Items, 1)
	require.EqualValues(t, 1, page.Items[0].ID)
	_, err = r.PublicWork(context.Background(), 2)
	require.ErrorIs(t, err, service.ErrDegradationWorkNotFound)
	_, err = db.Exec(`UPDATE groups SET degradation_preview_enabled=false WHERE id=1`)
	require.NoError(t, err)
	page, err = r.PublicPage(context.Background(), 1, 12)
	require.NoError(t, err)
	require.False(t, page.Enabled)
	require.Empty(t, page.Items)
	_, err = r.PublicWork(context.Background(), 1)
	require.ErrorIs(t, err, service.ErrDegradationWorkNotFound)
	_, err = r.PublicAnimationSource(context.Background(), 1)
	require.ErrorIs(t, err, service.ErrDegradationWorkNotFound)
}
