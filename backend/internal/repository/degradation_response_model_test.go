package repository

import (
	"context"
	"fmt"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func responseHistory(t *testing.T, r *degradationRepository, account int64, models [][2]string) {
	t.Helper()
	for _, pair := range models {
		_, err := r.db.Exec(`INSERT INTO usage_logs(account_id,requested_model,model,upstream_response_model) VALUES($1,$2,$2,$3)`, account, pair[0], pair[1])
		require.NoError(t, err)
	}
}

func responseRows(hits, total int) [][2]string {
	rows := make([][2]string, total)
	for i := range rows {
		rows[i] = [2]string{"gpt-6-astra", "gpt-6-astra"}
		if i < hits {
			rows[i][1] = "gpt-5.6-luna"
		}
	}
	return rows
}

func TestDegradationResponsePostgresWindowAndThreshold(t *testing.T) {
	for _, tc := range []struct {
		hits, total int
		want        int
	}{{4, 10, 0}, {5, 10, 1}, {6, 10, 1}, {5, 5, 1}, {4, 4, 0}, {0, 0, 0}} {
		t.Run(fmt.Sprintf("%d_of_%d", tc.hits, tc.total), func(t *testing.T) {
			db := degradationTestDB(t)
			r := &degradationRepository{db: db}
			responseHistory(t, r, 10, responseRows(tc.hits, tc.total))
			n, err := r.ApplyResponseModelDegradation(context.Background(), 300)
			require.NoError(t, err)
			require.Equal(t, tc.want, n)
			if n == 1 {
				var note string
				require.NoError(t, db.QueryRow(`SELECT degradation_suspend_note FROM accounts WHERE id=10`).Scan(&note))
				require.Contains(t, note, "gpt-6-astra响应luna")
				n, err = r.ApplyResponseModelDegradation(context.Background(), 300)
				require.NoError(t, err)
				require.Zero(t, n, "active pause must not be continuously extended")
			}
		})
	}
}

func TestDegradationResponsePostgresUsesWholeWindowAndDeclaredModel(t *testing.T) {
	db := degradationTestDB(t)
	r := &degradationRepository{db: db}
	// Old mismatches must drop out; other requested models occupy window slots.
	responseHistory(t, r, 10, responseRows(5, 5))
	other := make([][2]string, 10)
	for i := range other {
		other[i] = [2]string{"gpt-5.6-sol", "gpt-5.6-luna"}
	}
	responseHistory(t, r, 10, other)
	n, err := r.ApplyResponseModelDegradation(context.Background(), 300)
	require.NoError(t, err)
	require.Zero(t, n)
	_, err = db.Exec(`DELETE FROM usage_logs`)
	require.NoError(t, err)
	responseHistory(t, r, 10, [][2]string{
		{"gpt-6-astra", ""}, {"gpt-6-astra", "not-luna"}, {"gpt-6-astra", "gpt-5.6-luna-fake"},
		{"gpt-5.6-luna", "gpt-5.6-luna"}, {"gpt-6-astra", "gpt-6-astra"},
	})
	n, err = r.ApplyResponseModelDegradation(context.Background(), 300)
	require.NoError(t, err)
	require.Zero(t, n, "unknown or substring lookalikes must not count")
	_, err = db.Exec(`DELETE FROM usage_logs`)
	require.NoError(t, err)
	responseHistory(t, r, 10, responseRows(5, 5))
	_, err = db.Exec(`UPDATE usage_logs SET requested_model=NULL,upstream_response_model=' LUNA '`)
	require.NoError(t, err)
	n, err = r.ApplyResponseModelDegradation(context.Background(), 300)
	require.NoError(t, err)
	require.Equal(t, 1, n, "historical requested model fallback and exact alias")
}

func TestDegradationResponsePostgresORAndRecovery(t *testing.T) {
	db := degradationTestDB(t)
	r := &degradationRepository{db: db}
	responseHistory(t, r, 40, responseRows(5, 10))
	// Even a correct candy outcome applies the independent response-model OR.
	released, err := r.ApplyProbeOutcome(context.Background(), 40, 1, false, 0, "")
	require.NoError(t, err)
	require.False(t, released)
	var paused bool
	require.NoError(t, db.QueryRow(`SELECT degradation_suspended_until>NOW() FROM accounts WHERE id=40`).Scan(&paused))
	require.True(t, paused)
	// Once new non-mismatches shift the window below five, a correct probe releases.
	responseHistory(t, r, 40, responseRows(0, 10))
	released, err = r.ApplyProbeOutcome(context.Background(), 40, 1, false, 0, "")
	require.NoError(t, err)
	require.True(t, released)
	// The original OR branch remains sufficient on its own.
	changed, err := r.ApplyProbeOutcome(context.Background(), 40, 1, true, 30, "降智检测[分组1]：糖果题错误")
	require.NoError(t, err)
	require.True(t, changed)
}

func TestDegradationResponsePostgresMoveAndEligibility(t *testing.T) {
	db := degradationTestDB(t)
	r := &degradationRepository{db: db}
	setMoveConfig(t, db, 1, true, 2)
	responseHistory(t, r, 40, responseRows(5, 10))
	responseHistory(t, r, 20, responseRows(5, 10)) // detector disabled
	responseHistory(t, r, 30, responseRows(5, 10)) // manual disable
	_, err := db.Exec(`UPDATE accounts SET schedulable=false WHERE id=30`)
	require.NoError(t, err)
	n, err := r.ApplyResponseModelDegradation(context.Background(), 300)
	require.NoError(t, err)
	require.Equal(t, 1, n)
	require.Equal(t, []int64{2}, moveGroups(t, db, 40))
	var clean bool
	require.NoError(t, db.QueryRow(`SELECT temp_unschedulable_until IS NULL AND degradation_suspended_until IS NULL FROM accounts WHERE id=40`).Scan(&clean))
	require.True(t, clean)
	require.Equal(t, []int64{2}, moveGroups(t, db, 20))
	require.Equal(t, []int64{3}, moveGroups(t, db, 30))
}

func TestDegradationResponsePostgresRechecksBeforeWrite(t *testing.T) {
	db := degradationTestDB(t)
	r := &degradationRepository{db: db}
	setMoveConfig(t, db, 1, true, 2)
	responseHistory(t, r, 40, responseRows(4, 10))
	changed, err := r.applyDegradedOutcome(context.Background(), 40, 1, 30, "", true)
	require.NoError(t, err)
	require.False(t, changed, "stale candidate must not move after its window changes")
	require.Equal(t, []int64{1, 3}, moveGroups(t, db, 40))
	responseHistory(t, r, 40, responseRows(5, 10))
	_, err = db.Exec(`UPDATE groups SET degradation_detection_enabled=false WHERE id=1`)
	require.NoError(t, err)
	changed, err = r.applyDegradedOutcome(context.Background(), 40, 1, 30, "", true)
	require.NoError(t, err)
	require.False(t, changed)
}

func TestDegradationResponsePostgresPeriodicTick(t *testing.T) {
	db := degradationTestDB(t)
	r := &degradationRepository{db: db}
	setMoveConfig(t, db, 1, true, 2)
	responseHistory(t, r, 40, responseRows(5, 10))
	service.NewDegradationService(r).Tick(context.Background())
	require.Equal(t, []int64{2}, moveGroups(t, db, 40))
}

func TestDegradationResponsePostgresMoveClearsOnlyDetectorCooldown(t *testing.T) {
	for _, reason := range []string{"降智检测[分组1]：旧题目", "upstream overload"} {
		t.Run(reason, func(t *testing.T) {
			db := degradationTestDB(t)
			r := &degradationRepository{db: db}
			setMoveConfig(t, db, 1, true, 2)
			responseHistory(t, r, 40, responseRows(5, 10))
			_, err := db.Exec(`UPDATE accounts SET temp_unschedulable_until=NOW()+INTERVAL '30 minutes',temp_unschedulable_reason=$1 WHERE id=40`, reason)
			require.NoError(t, err)
			n, err := r.ApplyResponseModelDegradation(context.Background(), 300)
			require.NoError(t, err)
			require.Equal(t, 1, n)
			require.Equal(t, []int64{2}, moveGroups(t, db, 40))
			var clear bool
			require.NoError(t, db.QueryRow(`SELECT temp_unschedulable_until IS NULL FROM accounts WHERE id=40`).Scan(&clear))
			require.Equal(t, reason != "upstream overload", clear)
		})
	}
}

func TestDegradationResponsePostgresFailureRollsBack(t *testing.T) {
	db := degradationTestDB(t)
	r := &degradationRepository{db: db}
	setMoveConfig(t, db, 1, true, 2)
	responseHistory(t, r, 40, responseRows(5, 10))
	_, err := db.Exec(`ALTER TABLE scheduler_outbox ADD CONSTRAINT fail_write CHECK (account_id <> 40)`)
	require.NoError(t, err)
	n, err := r.ApplyResponseModelDegradation(context.Background(), 300)
	require.Error(t, err)
	require.Zero(t, n)
	require.Equal(t, []int64{1, 3}, moveGroups(t, db, 40))
	var clean bool
	require.NoError(t, db.QueryRow(`SELECT temp_unschedulable_until IS NULL FROM accounts WHERE id=40`).Scan(&clean))
	require.True(t, clean)
}

func TestDegradationResponsePostgresPreviousPresetEnqueuesCandy(t *testing.T) {
	db := degradationTestDB(t)
	r := &degradationRepository{db: db}
	_, err := db.Exec(`UPDATE groups SET degradation_detection_config=$1::jsonb WHERE id=1`,
		`{"prompt":"计算 960 ÷ 2 × 10 ÷ 5，只回答数字。","expected_answer":"960","model":"gpt-5.6-sol","reasoning_effort":"max"}`)
	require.NoError(t, err)
	_, created, err := r.EnqueueDegradationTest(context.Background(), 1, 10, service.DegradationTestTypeProbe, "stale prompt", "stale model", "low", "960", 300)
	require.NoError(t, err)
	require.True(t, created)
	var prompt, answer, model, effort string
	require.NoError(t, db.QueryRow(`SELECT input,config_snapshot->>'expected_answer',model,config_snapshot->>'reasoning_effort' FROM account_tests WHERE account_id=10`).Scan(&prompt, &answer, &model, &effort))
	require.Equal(t, service.DegradationCandyPrompt, prompt)
	require.Equal(t, "21", answer)
	require.Equal(t, "gpt-6-astra", model)
	require.Equal(t, "medium", effort)
}
