package repository

import (
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/lib/pq"
	"github.com/stretchr/testify/require"
)

func setMoveConfig(t *testing.T, db *sql.DB, groupID int64, enabled bool, target int64) {
	t.Helper()
	raw, _ := json.Marshal(map[string]any{"move_on_degraded": enabled, "move_target_group_id": target})
	_, err := db.Exec(`UPDATE groups SET degradation_detection_config=$2::jsonb WHERE id=$1`, groupID, string(raw))
	require.NoError(t, err)
}
func moveGroups(t *testing.T, db *sql.DB, accountID int64) []int64 {
	t.Helper()
	var ids pq.Int64Array
	require.NoError(t, db.QueryRow(`SELECT COALESCE(array_agg(group_id ORDER BY group_id),'{}') FROM account_groups WHERE account_id=$1`, accountID).Scan(&ids))
	return []int64(ids)
}
func TestDegradationMovePostgresReplacesAllGroupsWithoutCooldown(t *testing.T) {
	for _, target := range []int64{2, 3} {
		t.Run(string(rune('0'+target)), func(t *testing.T) {
			db := degradationTestDB(t)
			r := &degradationRepository{db: db}
			ctx := context.Background()
			setMoveConfig(t, db, 1, true, target)
			// Clear only the detector-owned cooldown, including a pause from another old group.
			_, err := db.Exec(`UPDATE accounts SET temp_unschedulable_until=NOW()+INTERVAL '30 minutes',temp_unschedulable_reason='降智检测[分组3]：旧暂停',degradation_suspended_until=NOW()+INTERVAL '30 minutes',degradation_suspended_at=NOW(),degradation_suspend_note='降智检测[分组3]：旧暂停',extra='{"keep":"unchanged"}' WHERE id=40`)
			require.NoError(t, err)
			changed, err := r.ApplyProbeOutcome(ctx, 40, 1, true, 30, "降智检测[分组1]：错误答案")
			require.NoError(t, err)
			require.True(t, changed)
			require.Equal(t, []int64{target}, moveGroups(t, db, 40))
			var until, degradedUntil sql.NullTime
			var reason, extra string
			var schedulable bool
			require.NoError(t, db.QueryRow(`SELECT temp_unschedulable_until,degradation_suspended_until,temp_unschedulable_reason,extra::text,schedulable FROM accounts WHERE id=40`).Scan(&until, &degradedUntil, &reason, &extra, &schedulable))
			require.False(t, until.Valid)
			require.False(t, degradedUntil.Valid)
			require.Empty(t, reason)
			require.True(t, schedulable)
			require.JSONEq(t, `{"keep":"unchanged"}`, extra)
			var event string
			var payload []byte
			require.NoError(t, db.QueryRow(`SELECT event_type,payload FROM scheduler_outbox`).Scan(&event, &payload))
			require.Equal(t, service.SchedulerOutboxEventAccountGroupsChanged, event)
			var body struct {
				IDs []int64 `json:"group_ids"`
			}
			require.NoError(t, json.Unmarshal(payload, &body))
			require.ElementsMatch(t, uniqueMoveIDs([]int64{1, 3, target}), body.IDs)
			changed, err = r.ApplyProbeOutcome(ctx, 40, 1, true, 30, "late probe")
			require.NoError(t, err)
			require.False(t, changed)
			changed, err = r.ApplyProbeOutcome(ctx, 40, 1, false, 0, "")
			require.NoError(t, err)
			require.False(t, changed)
			require.Equal(t, []int64{target}, moveGroups(t, db, 40), "late results never restore old memberships")
		})
	}
}
func uniqueMoveIDs(ids []int64) []int64 {
	out := []int64{}
	seen := map[int64]bool{}
	for _, id := range ids {
		if !seen[id] {
			out = append(out, id)
			seen[id] = true
		}
	}
	return out
}

func TestDegradationMovePostgresDisabledUsesOldCooldown(t *testing.T) {
	db := degradationTestDB(t)
	r := &degradationRepository{db: db}
	setMoveConfig(t, db, 1, false, 2)
	changed, err := r.ApplyProbeOutcome(context.Background(), 40, 1, true, 30, "降智检测[分组1]：测试")
	require.NoError(t, err)
	require.True(t, changed)
	require.Equal(t, []int64{1, 3}, moveGroups(t, db, 40))
	var paused bool
	require.NoError(t, db.QueryRow(`SELECT temp_unschedulable_until>NOW() FROM accounts WHERE id=40`).Scan(&paused))
	require.True(t, paused)
}

func TestDegradationMovePostgresPreservesUnrelatedCooldown(t *testing.T) {
	db := degradationTestDB(t)
	r := &degradationRepository{db: db}
	setMoveConfig(t, db, 1, true, 2)
	_, err := db.Exec(`UPDATE accounts SET temp_unschedulable_until=NOW()+INTERVAL '2 hours',temp_unschedulable_reason='upstream overload' WHERE id=40`)
	require.NoError(t, err)
	var before, after string
	require.NoError(t, db.QueryRow(`SELECT temp_unschedulable_until::text FROM accounts WHERE id=40`).Scan(&before))
	changed, err := r.ApplyProbeOutcome(context.Background(), 40, 1, true, 30, "降智检测[分组1]：测试")
	require.NoError(t, err)
	require.True(t, changed)
	require.Equal(t, []int64{2}, moveGroups(t, db, 40))
	var reason string
	require.NoError(t, db.QueryRow(`SELECT temp_unschedulable_until::text,temp_unschedulable_reason FROM accounts WHERE id=40`).Scan(&after, &reason))
	require.Equal(t, before, after)
	require.Equal(t, "upstream overload", reason)
}

func TestDegradationMovePostgresRejectsInvalidTargetsOnSave(t *testing.T) {
	db := degradationTestDB(t)
	r := &degradationRepository{db: db}
	for _, target := range []int64{1, 999} {
		var cfg service.DegradationDetectionConfig
		raw, _ := json.Marshal(map[string]any{"enabled": true, "move_on_degraded": true, "move_target_group_id": target})
		require.NoError(t, json.Unmarshal(raw, &cfg))
		require.Error(t, r.UpdateGroupConfig(context.Background(), 1, 1, cfg))
	}
	var cfg service.DegradationDetectionConfig
	require.NoError(t, json.Unmarshal([]byte(`{"enabled":true,"move_on_degraded":true,"move_target_group_id":2}`), &cfg))
	require.NoError(t, r.UpdateGroupConfig(context.Background(), 1, 1, cfg))
	saved, err := r.GroupConfig(context.Background(), 1)
	require.NoError(t, err)
	raw, _ := json.Marshal(saved)
	require.Contains(t, string(raw), `"move_on_degraded":true`)
}

func TestDegradationMovePostgresSkipsStaleAndDisabledAccounts(t *testing.T) {
	for _, change := range []string{
		`UPDATE accounts SET schedulable=false WHERE id=40`,
		`UPDATE accounts SET deleted_at=NOW() WHERE id=40`,
		`UPDATE groups SET degradation_detection_enabled=false WHERE id=1`,
		`DELETE FROM account_groups WHERE account_id=40 AND group_id=1`,
	} {
		t.Run(change, func(t *testing.T) {
			db := degradationTestDB(t)
			r := &degradationRepository{db: db}
			setMoveConfig(t, db, 1, true, 2)
			_, err := db.Exec(change)
			require.NoError(t, err)
			before := moveGroups(t, db, 40)
			changed, err := r.ApplyProbeOutcome(context.Background(), 40, 1, true, 30, "降智检测[分组1]：测试")
			require.NoError(t, err)
			require.False(t, changed)
			require.Equal(t, before, moveGroups(t, db, 40))
		})
	}
}

func TestDegradationMovePostgresRollsBackOnFailure(t *testing.T) {
	for _, failure := range []string{"deleted target", "insert failure", "outbox failure"} {
		t.Run(failure, func(t *testing.T) {
			db := degradationTestDB(t)
			r := &degradationRepository{db: db}
			setMoveConfig(t, db, 1, true, 2)
			if failure == "deleted target" {
				_, err := db.Exec(`UPDATE groups SET deleted_at=NOW() WHERE id=2`)
				require.NoError(t, err)
			} else {
				table := "account_groups"
				if failure == "outbox failure" {
					table = "scheduler_outbox"
				}
				_, err := db.Exec(`CREATE FUNCTION fail_move_write() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'injected move failure'; END $$; CREATE TRIGGER fail_move BEFORE INSERT ON ` + table + ` FOR EACH ROW EXECUTE FUNCTION fail_move_write()`)
				require.NoError(t, err)
			}
			_, err := r.ApplyProbeOutcome(context.Background(), 40, 1, true, 30, "降智检测[分组1]：测试")
			require.Error(t, err)
			require.Equal(t, []int64{1, 3}, moveGroups(t, db, 40))
			var until sql.NullTime
			require.NoError(t, db.QueryRow(`SELECT temp_unschedulable_until FROM accounts WHERE id=40`).Scan(&until))
			require.False(t, until.Valid)
			var count int
			require.NoError(t, db.QueryRow(`SELECT COUNT(*) FROM scheduler_outbox`).Scan(&count))
			require.Zero(t, count)
		})
	}
}

func TestDegradationMovePostgresOutcomeHook(t *testing.T) {
	for _, tc := range []struct {
		name, verdict, runError, kind string
		enabled, wantMove             bool
	}{
		{"incorrect", "incorrect", "", service.DegradationTestTypeProbe, true, true},
		{"correct", "correct", "", service.DegradationTestTypeProbe, true, false},
		{"undetermined", "undetermined", "", service.DegradationTestTypeProbe, true, false},
		{"timeout", "incorrect", "upstream timeout", service.DegradationTestTypeProbe, true, false},
		{"artwork", "incorrect", "", service.DegradationTestTypePreview, true, false},
		{"detector off", "incorrect", "", service.DegradationTestTypeProbe, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db := degradationTestDB(t)
			repo := &degradationRepository{db: db}
			svc := service.NewDegradationService(repo)
			var cfg service.DegradationDetectionConfig
			raw, _ := json.Marshal(map[string]any{"enabled": tc.enabled, "move_on_degraded": true, "move_target_group_id": 2})
			require.NoError(t, json.Unmarshal(raw, &cfg))
			_, err := svc.UpdateGroupConfig(context.Background(), 1, 1, cfg)
			require.NoError(t, err)
			svc.HandleIntelligentTestOutcome(context.Background(), &service.IntelligentTestRecord{
				ID: 9, AccountID: 40, TestType: tc.kind, Status: "completed", ErrorMessage: tc.runError,
				ConfigSnapshot: &service.IntelligentTestConfig{DegradationGroupID: 1, ExpectedAnswer: "21"},
				Evaluation:     map[string]any{"answer_verdict": tc.verdict, "normalized_answer": "29"},
			})
			expected := []int64{1, 3}
			if tc.wantMove {
				expected = []int64{2}
			}
			require.Equal(t, expected, moveGroups(t, db, 40))
			var until sql.NullTime
			require.NoError(t, db.QueryRow(`SELECT temp_unschedulable_until FROM accounts WHERE id=40`).Scan(&until))
			require.False(t, until.Valid, "move-only must never add a cooldown")
		})
	}
}

func TestDegradationMovePostgresConcurrentVerdictsMoveOnlyOnce(t *testing.T) {
	db := degradationTestDB(t)
	var schema string
	require.NoError(t, db.QueryRow(`SELECT current_schema()`).Scan(&schema))
	other, err := sql.Open("postgres", os.Getenv("DEGRADATION_TEST_DSN"))
	require.NoError(t, err)
	other.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = other.Close() })
	_, err = other.Exec(`SET search_path TO ` + pq.QuoteIdentifier(schema))
	require.NoError(t, err)
	_, err = db.Exec(`INSERT INTO groups(id) VALUES(4)`)
	require.NoError(t, err)
	setMoveConfig(t, db, 1, true, 2)
	setMoveConfig(t, db, 3, true, 4)
	type outcome struct {
		changed bool
		err     error
	}
	done := make(chan outcome, 2)
	start := make(chan struct{})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	for i, pool := range []*sql.DB{db, other} {
		groupID := int64(1 + i*2)
		go func(pool *sql.DB, groupID int64) {
			<-start
			changed, err := (&degradationRepository{db: pool}).ApplyProbeOutcome(ctx, 40, groupID, true, 30, "降智检测：测试")
			done <- outcome{changed, err}
		}(pool, groupID)
	}
	close(start)
	writes := 0
	for range 2 {
		result := <-done
		require.NoError(t, result.err)
		if result.changed {
			writes++
		}
	}
	require.Equal(t, 1, writes)
	ids := moveGroups(t, db, 40)
	require.Len(t, ids, 1)
	require.Contains(t, []int64{2, 4}, ids[0])
	var events int
	require.NoError(t, db.QueryRow(`SELECT COUNT(*) FROM scheduler_outbox`).Scan(&events))
	require.Equal(t, 1, events)
}
