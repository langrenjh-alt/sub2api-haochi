package repository

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/lib/pq"
)

type degradationRepository struct{ db *sql.DB }

// NewDegradationRepository returns the SQL implementation of the detector's
// storage. It shares the account_tests / test_settings tables with the generic
// intelligent-test plumbing and adds no new tables.
func NewDegradationRepository(db *sql.DB) service.DegradationRepository {
	return &degradationRepository{db: db}
}

const degradationGroupSQL = `
SELECT g.id, g.name, COALESCE(g.platform, ''), g.degradation_detection_enabled,
       g.degradation_detection_config, g.degradation_preview_enabled,
       (SELECT COUNT(*) FROM account_groups ag JOIN accounts a ON a.id = ag.account_id AND a.deleted_at IS NULL WHERE ag.group_id = g.id)
FROM groups g
WHERE g.deleted_at IS NULL`

func decodeDegradationConfig(raw []byte) service.DegradationDetectionConfig {
	cfg := service.DegradationDetectionConfig{}
	if len(raw) > 0 {
		_ = json.Unmarshal(raw, &cfg)
	}
	return cfg
}

func (r *degradationRepository) Groups(ctx context.Context) ([]service.DegradationGroup, error) {
	rows, err := r.db.QueryContext(ctx, degradationGroupSQL+" ORDER BY g.id")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []service.DegradationGroup{}
	for rows.Next() {
		var item service.DegradationGroup
		var enabled, previewEnabled bool
		var raw []byte
		if err := rows.Scan(&item.GroupID, &item.GroupName, &item.Platform, &enabled, &raw, &previewEnabled, &item.AccountCount); err != nil {
			return nil, err
		}
		cfg := service.NormalizeDegradationConfig(decodeDegradationConfig(raw))
		cfg.Enabled = enabled
		cfg.PreviewEnabled = previewEnabled
		item.Config = cfg
		out = append(out, item)
	}
	return out, rows.Err()
}

func (r *degradationRepository) GroupConfig(ctx context.Context, groupID int64) (service.DegradationDetectionConfig, error) {
	var enabled, previewEnabled bool
	var raw []byte
	err := r.db.QueryRowContext(ctx, `SELECT degradation_detection_enabled, degradation_detection_config, degradation_preview_enabled FROM groups WHERE id=$1 AND deleted_at IS NULL`, groupID).
		Scan(&enabled, &raw, &previewEnabled)
	if errors.Is(err, sql.ErrNoRows) {
		return service.DegradationDetectionConfig{}, service.ErrDegradationGroupNotFound
	}
	if err != nil {
		return service.DegradationDetectionConfig{}, err
	}
	cfg := service.NormalizeDegradationConfig(decodeDegradationConfig(raw))
	cfg.Enabled = enabled
	cfg.PreviewEnabled = previewEnabled
	return cfg, nil
}

// AccountConfig resolves the effective detector configuration for an account
// from its most recently configured degradation-enabled group. An account in no
// such group gets the built-in defaults.
func (r *degradationRepository) AccountConfig(ctx context.Context, accountID int64) (int64, service.DegradationDetectionConfig, error) {
	var groupID int64
	var raw []byte
	err := r.db.QueryRowContext(ctx, `
SELECT g.id, g.degradation_detection_config
FROM accounts a
JOIN account_groups ag ON ag.account_id = a.id
JOIN groups g ON g.id = ag.group_id
WHERE a.id = $1 AND a.deleted_at IS NULL AND g.deleted_at IS NULL AND g.degradation_detection_enabled
ORDER BY g.id LIMIT 1`, accountID).Scan(&groupID, &raw)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, service.NormalizeDegradationConfig(service.DegradationDetectionConfig{}), nil
	}
	if err != nil {
		return 0, service.DegradationDetectionConfig{}, err
	}
	return groupID, service.NormalizeDegradationConfig(decodeDegradationConfig(raw)), nil
}

func (r *degradationRepository) UpdateGroupConfig(ctx context.Context, actor, groupID int64, cfg service.DegradationDetectionConfig) error {
	encoded, err := json.Marshal(cfg)
	if err != nil {
		return err
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	// Serialize switches with enqueue and other group updates, so the global
	// worker setting reflects all committed group switches.
	if _, err := tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(247000)`); err != nil {
		return err
	}
	if err := guardDegradationTicketTransfer(ctx, tx, groupID, cfg); err != nil {
		return err
	}
	if cfg.Enabled && cfg.MoveOnDegraded {
		if cfg.MoveTargetGroupID <= 0 || cfg.MoveTargetGroupID == groupID {
			return service.ErrDegradationMoveTargetInvalid
		}
		if err := lockLiveGroups(ctx, tx, []int64{cfg.MoveTargetGroupID}); err != nil {
			if errors.Is(err, service.ErrGroupNotFound) {
				return service.ErrDegradationMoveTargetInvalid
			}
			return err
		}
	}
	result, err := tx.ExecContext(ctx, `UPDATE groups SET degradation_detection_enabled=$2, degradation_detection_config=$3::jsonb, degradation_preview_enabled=$4, updated_at=NOW() WHERE id=$1 AND deleted_at IS NULL`, groupID, cfg.Enabled, string(encoded), cfg.PreviewEnabled)
	if err != nil {
		return err
	}
	if affected, err := result.RowsAffected(); err != nil {
		return err
	} else if affected != 1 {
		return service.ErrDegradationGroupNotFound
	}
	// A queued test is cancelled by the claim worker when its setting is off, so
	// the settings rows must track the group switches exactly: probe settings
	// follow any enabled group, preview settings follow the public page switch.
	var probeGroups, previewGroups int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FILTER (WHERE degradation_detection_enabled), COUNT(*) FILTER (WHERE degradation_detection_enabled AND degradation_preview_enabled) FROM groups WHERE deleted_at IS NULL`).Scan(&probeGroups, &previewGroups); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO test_settings(test_type, enabled, user_visible) VALUES ($1,$2,FALSE), ($3,$4,TRUE) ON CONFLICT (test_type) DO UPDATE SET enabled=EXCLUDED.enabled, user_visible=EXCLUDED.user_visible, updated_at=NOW()`,
		service.DegradationTestTypeProbe, probeGroups > 0,
		service.DegradationTestTypePreview, previewGroups > 0); err != nil {
		return err
	}
	if err := insertIntelligentAudit(ctx, tx, actor, "admin.degradation_detection.settings", map[string]any{"group_id": groupID, "config": cfg}); err != nil {
		return err
	}
	return tx.Commit()
}

func (r *degradationRepository) SetTestSettingEnabled(ctx context.Context, testType string, enabled, userVisible bool) error {
	_, err := r.db.ExecContext(ctx, `INSERT INTO test_settings(test_type, enabled, user_visible) VALUES ($1,$2,$3) ON CONFLICT (test_type) DO UPDATE SET enabled=EXCLUDED.enabled, user_visible=EXCLUDED.user_visible, updated_at=NOW()`, testType, enabled, userVisible)
	return err
}

// PendingQueueDepth counts probes the worker has not drained yet. The scheduler
// uses it as a hard ceiling so a large group cannot flood the shared test queue,
// which is drained by at most four concurrent runs.
func (r *degradationRepository) PendingQueueDepth(ctx context.Context) (int, error) {
	var depth int
	err := r.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM account_tests WHERE status IN ('queued','running') AND test_type = ANY($1)`,
		pq.Array([]string{service.DegradationTestTypeProbe, service.DegradationTestTypePreview})).Scan(&depth)
	return depth, err
}

// DueProbeAccountIDs returns schedulable group members whose last finished probe
// is older than the interval. Manual disables (schedulable = false) are excluded
// here, which is what keeps them out of probing and out of auto-recovery.
func (r *degradationRepository) DueProbeAccountIDs(ctx context.Context, groupID int64, intervalMinutes, limit int) ([]int64, error) {
	rows, err := r.db.QueryContext(ctx, `
WITH member AS (
    SELECT a.id,
           COALESCE((SELECT MAX(t.created_at) FROM account_tests t
                     WHERE t.account_id = a.id AND t.test_type = $2 AND COALESCE(t.config_snapshot->>'public_sample','false') <> 'true'
                       AND t.config_snapshot->>'degradation_group_id' = $1::bigint::text
                       AND t.status NOT IN ('queued','running','cancelled')), '-infinity'::timestamptz) AS last_at
    FROM accounts a
    JOIN account_groups ag ON ag.account_id = a.id AND ag.group_id = $1
    WHERE a.deleted_at IS NULL AND a.schedulable = TRUE
      AND NOT EXISTS (SELECT 1 FROM account_tests q
                      WHERE q.account_id = a.id AND q.test_type = $2 AND q.config_snapshot->>'degradation_group_id' = $1::bigint::text AND q.status IN ('queued','running'))
)
SELECT id FROM member WHERE last_at < NOW() - make_interval(mins => $3::int) ORDER BY last_at, id LIMIT $4`,
		groupID, service.DegradationTestTypeProbe, intervalMinutes, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []int64{}
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

// DuePreviewAccountIDs picks random schedulable members of the preview-enabled
// groups. Accounts currently suspended by a degradation verdict are skipped, so
// the artwork is never produced by an account the detector flagged.
func (r *degradationRepository) DuePreviewAccountIDs(ctx context.Context, groupIDs []int64, interval int, limit int) ([]int64, error) {
	rows, err := r.db.QueryContext(ctx, `
SELECT a.id
FROM accounts a
JOIN account_groups ag ON ag.account_id = a.id
JOIN groups g ON g.id = ag.group_id
WHERE g.deleted_at IS NULL AND g.degradation_detection_enabled AND g.degradation_preview_enabled
  AND g.id = ANY($2)
  AND a.deleted_at IS NULL AND a.schedulable = TRUE
  AND (a.degradation_suspended_until IS NULL OR a.degradation_suspended_until <= NOW())
  AND NOT EXISTS (SELECT 1 FROM account_tests q WHERE q.test_type = $1 AND q.config_snapshot->>'degradation_group_id' = g.id::text AND q.status IN ('queued','running'))
  AND COALESCE((SELECT MAX(COALESCE(p.finished_at,p.created_at) + make_interval(mins => CASE WHEN p.status='completed' THEN $4::int ELSE 1 END)) FROM account_tests p WHERE p.test_type=$1 AND p.config_snapshot->>'degradation_group_id'=g.id::text AND p.status NOT IN ('queued','running','cancelled')), '-infinity'::timestamptz) <= NOW()
ORDER BY (SELECT COUNT(*) FROM account_tests p
          WHERE p.account_id = a.id AND p.test_type = $1
            AND p.status NOT IN ('queued','running','cancelled')
            AND p.status <> 'completed'
            AND p.created_at > NOW() - INTERVAL '30 minutes') ASC,
         random()
LIMIT $3`,
		service.DegradationTestTypePreview, pq.Array(groupIDs), limit, interval)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []int64{}
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

func (r *degradationRepository) PreviewGroups(ctx context.Context) ([]int64, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT id FROM groups WHERE deleted_at IS NULL AND degradation_detection_enabled AND degradation_preview_enabled ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []int64{}
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

// EnqueueDegradationTest queues one probe. It is idempotent against an already
// queued or running record of the same type and never uses the admin request
// path, so the scheduler needs no actor.
func (r *degradationRepository) EnqueueDegradationTest(ctx context.Context, groupID, accountID int64, testType, prompt, model, reasoningEffort, expectedAnswer string, timeoutSeconds int) (int64, bool, error) {
	return r.enqueueDegradationTest(ctx, groupID, accountID, testType, prompt, model, reasoningEffort, expectedAnswer, timeoutSeconds, 0)
}

func (r *degradationRepository) enqueueDegradationTest(ctx context.Context, groupID, accountID int64, testType, prompt, model, reasoningEffort, expectedAnswer string, timeoutSeconds int, roundID int64) (int64, bool, error) {
	if !service.IsDegradationTestType(testType) {
		return 0, false, errors.New("unknown degradation test type")
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, false, err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(247000)`); err != nil {
		return 0, false, err
	}
	// Re-read under the enqueue/settings lock: a scheduler scan made before a
	// settings edit must not enqueue stale answers or models after the save.
	var raw []byte
	var enabled, previewEnabled bool
	err = tx.QueryRowContext(ctx, `SELECT degradation_detection_enabled,degradation_preview_enabled,degradation_detection_config FROM groups WHERE id=$1 AND deleted_at IS NULL`, groupID).Scan(&enabled, &previewEnabled, &raw)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, false, nil
	}
	if err != nil {
		return 0, false, err
	}
	if !enabled || (testType == service.DegradationTestTypePreview && !previewEnabled) {
		return 0, false, nil
	}
	live := service.NormalizeDegradationConfig(decodeDegradationConfig(raw))
	timeoutSeconds = live.TimeoutSeconds
	if testType == service.DegradationTestTypePreview {
		prompt, model, reasoningEffort, expectedAnswer = service.DegradationPelicanPrompt, live.PreviewModel, live.PreviewReasoningEffort, ""
	} else {
		prompt, model, reasoningEffort, expectedAnswer = live.Prompt, live.Model, live.ReasoningEffort, live.ExpectedAnswer
		if prompt == "" {
			prompt = service.DegradationCandyPrompt
		}
	}
	// The runner resolves the verdict from this snapshot, so the group's
	// configured answer has to travel with the row: reading the live group
	// config at verdict time would grade the answer against a value that may
	// have changed after the request was sent.
	expected := strings.TrimSpace(expectedAnswer)
	if expected == "" {
		expected = service.DegradationExpectedAnswer
	}
	cfg := service.IntelligentTestConfig{
		PublicSample: roundID > 0, SourceRoundID: roundID,
		DegradationGroupID: groupID,
		Prompt:             prompt,
		Model:              model,
		ReasoningEffort:    reasoningEffort,
		Evaluator:          degradationEvaluatorFor(testType),
		ExpectedAnswer:     expected,
		TimeoutSeconds:     timeoutSeconds,
		AnswerType:         "number",
		AnswerFormat:       "free_text",
		AnswerUnitMode:     "none",
	}
	if testType == service.DegradationTestTypePreview {
		cfg.AnswerType = "text"
		cfg.ExpectedAnswer = ""
	}
	encoded, err := json.Marshal(cfg)
	if err != nil {
		return 0, false, err
	}
	var id int64
	err = tx.QueryRowContext(ctx, `
INSERT INTO account_tests(account_id, test_type, input, model, anti_degradation, config_snapshot, requested_by)
SELECT $1, $2, $3, $4,
       COALESCE((a.extra->'anti_degradation')='true'::jsonb, (a.extra#>'{anti_degrade,enabled}')='true'::jsonb, false),
       $5::jsonb, NULL
FROM accounts a
WHERE a.id = $1 AND a.deleted_at IS NULL AND a.schedulable = TRUE
  AND EXISTS (SELECT 1 FROM account_groups ag JOIN groups g ON g.id=ag.group_id WHERE ag.account_id=a.id AND g.id=$6 AND g.deleted_at IS NULL AND g.degradation_detection_enabled AND ($2 <> 'degradation_preview' OR g.degradation_preview_enabled))
  AND (SELECT COUNT(*) FROM account_tests WHERE status IN ('queued','running') AND test_type IN ('degradation_probe','degradation_preview')) < 200
  AND (SELECT COUNT(*) FROM account_tests WHERE status IN ('queued','running')) < 2000
  AND NOT EXISTS (SELECT 1 FROM account_tests q WHERE q.account_id = $1 AND q.test_type = $2 AND q.config_snapshot->>'degradation_group_id'=$6::bigint::text AND q.status IN ('queued','running'))
  AND ($2 <> 'degradation_preview' OR NOT EXISTS (SELECT 1 FROM account_tests q WHERE q.test_type=$2 AND q.config_snapshot->>'degradation_group_id'=$6::bigint::text AND q.status IN ('queued','running')))
  AND ($7::bigint = 0 OR (
    NOT EXISTS (SELECT 1 FROM account_tests q WHERE q.config_snapshot->>'public_sample'='true' AND q.config_snapshot->>'degradation_group_id'=$6::bigint::text AND q.created_at > NOW()-make_interval(mins=>GREATEST(1,$8::int)))
    AND NOT EXISTS (SELECT 1 FROM account_tests q JOIN account_tests source ON source.id=$7
      WHERE q.test_type='degradation_probe' AND q.config_snapshot->>'degradation_group_id'=$6::bigint::text
      AND COALESCE(q.config_snapshot->>'public_sample','false')<>'true' AND q.status IN ('queued','running')
      AND floor(extract(epoch FROM q.created_at)/(GREATEST(1,$8::int)*60))=floor(extract(epoch FROM source.created_at)/(GREATEST(1,$8::int)*60)))
    AND NOT EXISTS (SELECT 1 FROM account_tests q WHERE q.config_snapshot->>'public_sample'='true' AND q.config_snapshot->>'degradation_group_id'=$6::bigint::text AND q.status IN ('queued','running'))
    AND NOT EXISTS (SELECT 1 FROM account_tests q WHERE q.config_snapshot->>'public_sample'='true' AND q.config_snapshot->>'degradation_group_id'=$6::bigint::text AND (q.config_snapshot->>'source_round_id')::bigint >= $7)
  ))
RETURNING id`, accountID, testType, prompt, model, string(encoded), groupID, roundID, live.IntervalMinute).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, false, nil
	}
	if err != nil {
		return 0, false, err
	}
	return id, true, tx.Commit()
}

func degradationEvaluatorFor(testType string) string {
	if testType == service.DegradationTestTypePreview {
		return "svg_structure"
	}
	return "exact_answer"
}

// ApplyProbeOutcome translates a persisted verdict into scheduling state.
//
//   - degraded: either move exclusively to the configured group without a
//     detector cooldown, or apply the original suspension policy. The current
//     switch and memberships are rechecked transactionally.
//   - recovered: drop our window only while its reason still carries our marker,
//     and always clear our own columns. An unrelated pause survives verbatim.
func (r *degradationRepository) ApplyProbeOutcome(ctx context.Context, accountID, groupID int64, degraded bool, suspendMinutes int, note string) (bool, error) {
	if degraded {
		return r.applyDegradedProbeOutcome(ctx, accountID, groupID, suspendMinutes, note)
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer func() { _ = tx.Rollback() }()
	// OR recovery: a correct candy answer must not clear the response-model
	// condition. Serialize with policy writes before examining the same window.
	var lockedID int64
	err = tx.QueryRowContext(ctx, `SELECT id FROM accounts WHERE id=$1 AND deleted_at IS NULL AND schedulable=TRUE FOR UPDATE`, accountID).Scan(&lockedID)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	hits, err := degradationResponseHits(ctx, tx, accountID)
	if err != nil {
		return false, err
	}
	if hits >= 5 {
		// Release this transaction before reusing the common move/pause path,
		// which rechecks the window, membership and switch under its own lock.
		if err := tx.Rollback(); err != nil {
			return false, err
		}
		_, err := r.applyDegradedOutcome(ctx, accountID, groupID, 0, "", true)
		return false, err
	}
	if _, err := tx.ExecContext(ctx, `
UPDATE accounts SET temp_unschedulable_until = NULL, temp_unschedulable_reason = '', updated_at = NOW()
WHERE id = $1 AND deleted_at IS NULL AND schedulable=TRUE AND degradation_suspended_until IS NOT NULL AND temp_unschedulable_reason LIKE $2
 AND EXISTS (SELECT 1 FROM account_groups ag JOIN groups g ON g.id=ag.group_id WHERE ag.account_id=accounts.id AND g.id=$3 AND g.deleted_at IS NULL AND g.degradation_detection_enabled)`, accountID, fmt.Sprintf("%s[分组%d]%%", service.DegradationSuspendReasonPrefix, groupID), groupID); err != nil {
		return false, err
	}
	result, err := tx.ExecContext(ctx, `
UPDATE accounts SET degradation_suspended_until = NULL, degradation_suspended_at = NULL, degradation_suspend_note = '', updated_at = NOW()
WHERE id = $1 AND deleted_at IS NULL AND schedulable=TRUE AND degradation_suspended_until IS NOT NULL AND degradation_suspend_note LIKE $3
 AND EXISTS (SELECT 1 FROM account_groups ag JOIN groups g ON g.id=ag.group_id WHERE ag.account_id=accounts.id AND g.id=$2 AND g.deleted_at IS NULL AND g.degradation_detection_enabled)`, accountID, groupID, fmt.Sprintf("%s[分组%d]%%", service.DegradationSuspendReasonPrefix, groupID))
	if err != nil {
		return false, err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return false, err
	}
	if affected > 0 {
		if err := enqueueSchedulerOutbox(ctx, tx, service.SchedulerOutboxEventAccountChanged, &accountID, nil, nil); err != nil {
			return false, err
		}
	}
	return affected == 1, tx.Commit()
}

func (r *degradationRepository) Overview(ctx context.Context, accountLimit int) (*service.DegradationOverview, error) {
	out := &service.DegradationOverview{
		Model:           service.DegradationDefaultModel,
		ReasoningEffort: service.DegradationProbeDefaultEffort,
		ExpectedAnswer:  service.DegradationExpectedAnswer,
		IntervalMinute:  service.DegradationDefaultIntervalMinute,
		SuspendMinute:   service.DegradationDefaultSuspendMinute,
		PreviewInterval: service.DegradationDefaultIntervalMinute,
		Accounts:        []service.DegradationAccountState{},
	}
	var raw []byte
	var enabled, previewEnabled bool
	err := r.db.QueryRowContext(ctx, `
SELECT degradation_detection_enabled, degradation_detection_config, degradation_preview_enabled
FROM groups WHERE deleted_at IS NULL AND degradation_detection_enabled
ORDER BY degradation_preview_enabled DESC, id LIMIT 1`).Scan(&enabled, &raw, &previewEnabled)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}
	if err == nil {
		cfg := service.NormalizeDegradationConfig(decodeDegradationConfig(raw))
		out.Model = cfg.Model
		out.ReasoningEffort = cfg.ReasoningEffort
		out.ExpectedAnswer = cfg.ExpectedAnswer
		out.IntervalMinute = cfg.IntervalMinute
		out.SuspendMinute = cfg.SuspendMinute
		out.PreviewInterval = cfg.PreviewIntervalMinute
	}
	out.PreviewEnabled = previewEnabled

	if err := r.db.QueryRowContext(ctx, `
SELECT
  (SELECT COUNT(*) FROM groups WHERE deleted_at IS NULL AND degradation_detection_enabled),
  (SELECT COUNT(DISTINCT a.id) FROM accounts a JOIN account_groups ag ON ag.account_id = a.id
     JOIN groups g ON g.id = ag.group_id
    WHERE g.deleted_at IS NULL AND g.degradation_detection_enabled AND a.deleted_at IS NULL AND a.schedulable = TRUE),
  (SELECT COUNT(*) FROM account_tests WHERE test_type = $1 AND finished_at >= date_trunc('day', NOW())),
  (SELECT COUNT(*) FROM accounts WHERE degradation_suspended_until > NOW()),
  (SELECT COUNT(*) FROM accounts a JOIN account_groups ag ON ag.account_id = a.id
     JOIN groups g ON g.id = ag.group_id
    WHERE g.deleted_at IS NULL AND g.degradation_detection_enabled AND a.deleted_at IS NULL AND a.schedulable = FALSE),
  (SELECT COUNT(*) FROM account_tests WHERE test_type = $2 AND status NOT IN ('queued','running','cancelled') AND result_image <> '')`,
		service.DegradationTestTypeProbe, service.DegradationTestTypePreview).
		Scan(&out.GroupsEnabled, &out.AccountsWatched, &out.ProbesToday, &out.SuspendedAccounts, &out.ManualDisabled, &out.PreviewWorks); err != nil {
		return nil, err
	}

	if err := r.db.QueryRowContext(ctx, `
WITH ordered AS (
    SELECT t.finished_at, t.evaluation->>'answer_verdict' AS verdict,
           LAG(t.evaluation->>'answer_verdict') OVER (PARTITION BY t.account_id ORDER BY t.id) AS prev
    FROM account_tests t
    JOIN accounts a ON a.id = t.account_id AND a.deleted_at IS NULL
    WHERE t.test_type = $1 AND t.status NOT IN ('queued','running','cancelled')
)
SELECT COUNT(*) FROM ordered
WHERE verdict = 'correct' AND prev = 'incorrect' AND finished_at >= date_trunc('day', NOW())`,
		service.DegradationTestTypeProbe).Scan(&out.RecoveredToday); err != nil {
		return nil, err
	}

	if err := r.db.QueryRowContext(ctx, `
WITH latest AS (
    SELECT DISTINCT ON (t.account_id) t.evaluation->>'answer_verdict' AS verdict
    FROM account_tests t
    JOIN accounts a ON a.id = t.account_id AND a.deleted_at IS NULL
    WHERE t.test_type = $1 AND t.status NOT IN ('queued','running','cancelled')
    ORDER BY t.account_id, t.id DESC)
SELECT COUNT(*) FROM latest WHERE verdict = 'incorrect'`, service.DegradationTestTypeProbe).Scan(&out.DegradedAccounts); err != nil {
		return nil, err
	}

	if err := r.db.QueryRowContext(ctx, `SELECT COALESCE((SELECT status FROM account_tests WHERE test_type=$1 AND status NOT IN ('queued','running') ORDER BY id DESC LIMIT 1), '')`, service.DegradationTestTypePreview).Scan(&out.LastPreviewStatus); err != nil {
		return nil, err
	}

	if accountLimit > 0 {
		accounts, err := r.degradationAccountStates(ctx, accountLimit)
		if err != nil {
			return nil, err
		}
		out.Accounts = accounts
	}
	return out, nil
}

func (r *degradationRepository) degradationAccountStates(ctx context.Context, limit int) ([]service.DegradationAccountState, error) {
	rows, err := r.db.QueryContext(ctx, `
SELECT a.id, a.name, COALESCE(MIN(ag.group_id), 0), a.schedulable,
       a.degradation_suspended_until, a.degradation_suspended_at, a.degradation_suspend_note,
       latest.finished_at, COALESCE(latest.status, ''), COALESCE(latest.verdict, '')
FROM accounts a
JOIN account_groups ag ON ag.account_id = a.id
JOIN groups g ON g.id = ag.group_id AND g.deleted_at IS NULL AND g.degradation_detection_enabled
LEFT JOIN LATERAL (
    SELECT t.finished_at, t.status, t.evaluation->>'answer_verdict' AS verdict
    FROM account_tests t
    WHERE t.account_id = a.id AND t.test_type = $1 AND t.status NOT IN ('queued','running','cancelled')
    ORDER BY t.id DESC LIMIT 1
) latest ON TRUE
WHERE a.deleted_at IS NULL
GROUP BY a.id, a.name, a.schedulable, a.degradation_suspended_until, a.degradation_suspended_at,
         a.degradation_suspend_note, latest.finished_at, latest.status, latest.verdict
ORDER BY a.degradation_suspended_until DESC NULLS LAST, a.id
LIMIT $2`, service.DegradationTestTypeProbe, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []service.DegradationAccountState{}
	for rows.Next() {
		var item service.DegradationAccountState
		var suspendedUntil, suspendedAt, lastProbeAt sql.NullTime
		if err := rows.Scan(&item.AccountID, &item.AccountName, &item.GroupID, &item.Schedulable,
			&suspendedUntil, &suspendedAt, &item.SuspendNote,
			&lastProbeAt, &item.LastProbeStatus, &item.LastProbeAnswer); err != nil {
			return nil, err
		}
		if suspendedUntil.Valid {
			item.SuspendedUntil = &suspendedUntil.Time
		}
		if suspendedAt.Valid {
			item.SuspendedAt = &suspendedAt.Time
		}
		if lastProbeAt.Valid {
			item.LastProbeAt = &lastProbeAt.Time
		}
		item.LastProbeCorrect = item.LastProbeAnswer == "correct"
		out = append(out, item)
	}
	return out, rows.Err()
}

const degradationPublicScope = ` AND EXISTS (
 SELECT 1 FROM account_groups ag JOIN groups g ON g.id=ag.group_id JOIN accounts a ON a.id=ag.account_id
 WHERE ag.account_id=t.account_id AND a.deleted_at IS NULL AND g.deleted_at IS NULL
 AND g.degradation_detection_enabled AND g.degradation_preview_enabled
 AND (COALESCE(t.config_snapshot->>'degradation_group_id','')='' OR t.config_snapshot->>'degradation_group_id'=g.id::text))`

const degradationPublicColumns = `t.id,t.account_id,t.status,t.model,COALESCE(t.config_snapshot->>'reasoning_effort',''),t.result_image,t.duration_ms,t.created_at,t.finished_at`

func scanDegradationPublicWork(scan func(...any) error) (*service.DegradationPublicWork, error) {
	item := &service.DegradationPublicWork{}
	var finishedAt sql.NullTime
	if err := scan(&item.ID, &item.AccountID, &item.Status, &item.Model, &item.ReasoningEffort,
		&item.Image, &item.DurationMS, &item.CreatedAt, &finishedAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, service.ErrDegradationWorkNotFound
		}
		return nil, err
	}
	if finishedAt.Valid {
		item.FinishedAt = &finishedAt.Time
	}
	return item, nil
}

func (r *degradationRepository) PublicPage(ctx context.Context, page, pageSize int) (*service.DegradationPublicPage, error) {
	out := &service.DegradationPublicPage{Page: page, PageSize: pageSize, Items: []service.DegradationPublicWork{}}
	cfg, previewGroups, err := r.previewConfiguration(ctx)
	if err != nil {
		return nil, err
	}
	out.Enabled = previewGroups > 0
	out.Model = cfg.PreviewModel
	out.ReasoningEffort = cfg.PreviewReasoningEffort
	out.IntervalSeconds = cfg.PreviewIntervalMinute * 60
	if previewGroups == 0 {
		return out, nil
	}
	if err := r.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM account_tests t WHERE test_type=$1 AND status NOT IN ('queued','running','cancelled') AND result_image <> ''`+degradationPublicScope, service.DegradationTestTypePreview).Scan(&out.Total); err != nil {
		return nil, err
	}
	var lastAt sql.NullTime
	if err := r.db.QueryRowContext(ctx, `SELECT status, finished_at FROM account_tests t WHERE test_type=$1 AND status NOT IN ('queued','running')`+degradationPublicScope+` ORDER BY id DESC LIMIT 1`, service.DegradationTestTypePreview).Scan(&out.LastStatus, &lastAt); err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}
	if lastAt.Valid {
		out.LastFinishedAt = &lastAt.Time
	}
	// Artwork metadata only: the SVG body is fetched per tile from the image
	// endpoint, so a page load never ships megabytes of markup.
	rows, err := r.db.QueryContext(ctx, `SELECT `+degradationWorkColumns+` FROM account_tests t WHERE t.test_type=$1 AND t.status NOT IN ('queued','running','cancelled') AND t.result_image <> ''`+degradationPublicScope+` ORDER BY t.id DESC LIMIT $2 OFFSET $3`,
		service.DegradationTestTypePreview, pageSize, (page-1)*pageSize)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		item, err := scanDegradationWorkRow(rows.Scan)
		if err != nil {
			return nil, err
		}
		out.Items = append(out.Items, *item)
	}
	return out, rows.Err()
}

func (r *degradationRepository) previewConfiguration(ctx context.Context) (service.DegradationDetectionConfig, int, error) {
	var raw []byte
	var previewEnabled bool
	var count int
	err := r.db.QueryRowContext(ctx, `SELECT degradation_detection_config, degradation_preview_enabled, (SELECT COUNT(*) FROM groups WHERE deleted_at IS NULL AND degradation_detection_enabled AND degradation_preview_enabled) FROM groups WHERE deleted_at IS NULL AND degradation_detection_enabled ORDER BY degradation_preview_enabled DESC, id LIMIT 1`).
		Scan(&raw, &previewEnabled, &count)
	if errors.Is(err, sql.ErrNoRows) {
		return service.NormalizeDegradationConfig(service.DegradationDetectionConfig{}), 0, nil
	}
	if err != nil {
		return service.DegradationDetectionConfig{}, 0, err
	}
	return service.NormalizeDegradationConfig(decodeDegradationConfig(raw)), count, nil
}

func (r *degradationRepository) PublicWork(ctx context.Context, id int64) (*service.DegradationPublicWork, error) {
	return scanDegradationPublicWork(r.db.QueryRowContext(ctx, `SELECT `+degradationPublicColumns+` FROM account_tests t WHERE t.id=$1 AND t.test_type=$2 AND t.status NOT IN ('queued','running','cancelled') AND t.result_image <> ''`+degradationPublicScope,
		id, service.DegradationTestTypePreview).Scan)
}

func (r *degradationRepository) PublicAnimationSource(ctx context.Context, id int64) (string, error) {
	var output string
	err := r.db.QueryRowContext(ctx, `SELECT COALESCE(NULLIF(t.result,''),t.result_image) FROM account_tests t WHERE t.id=$1 AND t.test_type=$2 AND t.status NOT IN ('queued','running','cancelled') AND t.result_image <> ''`+degradationPublicScope,
		id, service.DegradationTestTypePreview).Scan(&output)
	if errors.Is(err, sql.ErrNoRows) {
		return "", service.ErrDegradationWorkNotFound
	}
	return output, err
}

// degradationTimelineBuckets is the nominal resolution. Include one extra
// partial edge bucket to cover the complete rolling time window.
const degradationTimelineBuckets = 72

// degradationWorkColumns lists artwork metadata only. The SVG body never travels
// in a list: it stays behind /records/:id/image, which matters because the public
// page re-reads this list every few minutes.
const degradationWorkColumns = `t.id,t.account_id,t.status,t.model,COALESCE(t.config_snapshot->>'reasoning_effort',''),t.duration_ms,t.created_at,t.finished_at,(COALESCE(t.result_image,'') <> '')`

func scanDegradationWorkRow(scan func(...any) error) (*service.DegradationPublicWork, error) {
	item := &service.DegradationPublicWork{}
	var finishedAt sql.NullTime
	if err := scan(&item.ID, &item.AccountID, &item.Status, &item.Model, &item.ReasoningEffort,
		&item.DurationMS, &item.CreatedAt, &finishedAt, &item.HasImage); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, service.ErrDegradationWorkNotFound
		}
		return nil, err
	}
	if finishedAt.Valid {
		item.FinishedAt = &finishedAt.Time
	}
	return item, nil
}

// Works lists the same rows the public page renders, for the admin panel. The
// public page is a curated surface, so its rows have to be inspectable.
func (r *degradationRepository) Works(ctx context.Context, page, pageSize int) (*service.DegradationWorkPage, error) {
	out := &service.DegradationWorkPage{Page: page, PageSize: pageSize, Items: []service.DegradationPublicWork{}}
	if err := r.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM account_tests WHERE test_type=$1 AND status NOT IN ('queued','running')`,
		service.DegradationTestTypePreview).Scan(&out.Total); err != nil {
		return nil, err
	}
	rows, err := r.db.QueryContext(ctx, `SELECT `+degradationWorkColumns+` FROM account_tests t WHERE t.test_type=$1 AND t.status NOT IN ('queued','running') ORDER BY t.id DESC LIMIT $2 OFFSET $3`,
		service.DegradationTestTypePreview, pageSize, (page-1)*pageSize)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		item, err := scanDegradationWorkRow(rows.Scan)
		if err != nil {
			return nil, err
		}
		out.Items = append(out.Items, *item)
	}
	return out, rows.Err()
}

// DeleteWork removes one artwork. Only artwork rows match, so the probe history
// behind the public timeline can never be deleted through this call.
func (r *degradationRepository) DeleteWork(ctx context.Context, id int64) (bool, error) {
	result, err := r.db.ExecContext(ctx, `DELETE FROM account_tests WHERE id=$1 AND test_type=$2`,
		id, service.DegradationTestTypePreview)
	if err != nil {
		return false, err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return false, err
	}
	return affected > 0, nil
}

// PurgeWorks clears the whole feed. A running artwork is left alone: it is still
// writing its own row.
func (r *degradationRepository) PurgeWorks(ctx context.Context) (int64, error) {
	result, err := r.db.ExecContext(ctx, `DELETE FROM account_tests WHERE test_type=$1 AND status NOT IN ('queued','running')`,
		service.DegradationTestTypePreview)
	if err != nil {
		return 0, err
	}
	return result.RowsAffected()
}

// Timeline aggregates probe verdicts into fixed buckets. It is the public view
// of detector history, so it returns counts and never rows.
const degradationPublicResetKey = "degradation_public_stats_reset_at"

func (r *degradationRepository) ResetPublicStats(ctx context.Context, actor int64) (time.Time, error) {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return time.Time{}, err
	}
	defer tx.Rollback()
	// Serialize concurrent resets with detector enqueue/configuration changes.
	if _, err = tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(247000)`); err != nil {
		return time.Time{}, err
	}
	var at time.Time
	err = tx.QueryRowContext(ctx, `INSERT INTO settings(key,value,updated_at)
VALUES ($1,clock_timestamp()::text,clock_timestamp())
ON CONFLICT (key) DO UPDATE SET value=EXCLUDED.value,updated_at=EXCLUDED.updated_at
RETURNING value::timestamptz`, degradationPublicResetKey).Scan(&at)
	if err != nil {
		return time.Time{}, err
	}
	if err = insertIntelligentAudit(ctx, tx, actor, "admin.degradation_detection.stats_reset", map[string]any{"reset_at": at, "scope": "public_global"}); err != nil {
		return time.Time{}, err
	}
	return at, tx.Commit()
}

func (r *degradationRepository) Timeline(ctx context.Context, hours int) (*service.DegradationTimeline, error) {
	// A single database snapshot prevents a reset/result racing bucket totals
	// and latest-state reads from producing contradictory output.
	tx, err := r.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	var resetAt sql.NullTime
	var now time.Time
	if err = tx.QueryRowContext(ctx, `SELECT (SELECT value::timestamptz FROM settings WHERE key=$1),transaction_timestamp()`, degradationPublicResetKey).Scan(&resetAt, &now); err != nil {
		return nil, err
	}
	if hours <= 0 {
		hours = 24
	}
	if hours > 24*30 {
		hours = 24 * 30
	}
	bucketMinutes := hours * 60 / degradationTimelineBuckets
	if bucketMinutes < 1 {
		bucketMinutes = 1
	}
	bucketSpan := time.Duration(bucketMinutes) * time.Minute
	newest := time.Unix(now.Unix()/int64(bucketSpan/time.Second)*int64(bucketSpan/time.Second), 0)
	out := &service.DegradationTimeline{
		RangeHours:   hours,
		BucketMinute: bucketMinutes,
		GeneratedAt:  now,
		CurrentState: "unknown",
		Buckets:      make([]service.DegradationTimelineBucket, 0, degradationTimelineBuckets),
	}
	if resetAt.Valid {
		out.ResetAt = &resetAt.Time
	}
	index := make(map[int64]int, degradationTimelineBuckets)
	for i := degradationTimelineBuckets; i >= 0; i-- {
		start := newest.Add(-time.Duration(i) * bucketSpan)
		index[start.Unix()] = len(out.Buckets)
		out.Buckets = append(out.Buckets, service.DegradationTimelineBucket{Start: start})
	}
	rows, err := tx.QueryContext(ctx, `
SELECT (floor(extract(epoch FROM COALESCE(t.finished_at,t.created_at)) / ($1::int * 60)) * $1::int * 60)::bigint AS bucket_epoch,
       COUNT(*),
       COUNT(*) FILTER (WHERE t.evaluation->>'answer_verdict' = 'correct'),
       COUNT(*) FILTER (WHERE t.evaluation->>'answer_verdict' = 'incorrect'),
       COUNT(*) FILTER (WHERE COALESCE(t.evaluation->>'answer_verdict','') NOT IN ('correct','incorrect'))
FROM account_tests t
WHERE t.test_type = $2 AND t.status NOT IN ('queued','running','cancelled')
  AND ($5::timestamptz IS NULL OR (t.created_at >= $5 AND COALESCE(t.started_at,t.created_at) >= $5))
  AND COALESCE(t.finished_at,t.created_at) >= $4::timestamptz - make_interval(hours => $3::int) AND COALESCE(t.finished_at,t.created_at) <= $4::timestamptz
GROUP BY 1
ORDER BY 1`,
		bucketMinutes, service.DegradationTestTypeProbe, hours, now, out.ResetAt)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var epoch, total, correct, degraded, undetermined int64
		if err := rows.Scan(&epoch, &total, &correct, &degraded, &undetermined); err != nil {
			return nil, err
		}
		position, ok := index[epoch]
		if !ok {
			continue
		}
		bucket := &out.Buckets[position]
		bucket.Total += total
		bucket.Correct += correct
		bucket.Degraded += degraded
		bucket.Undetermined += undetermined
		out.Total += total
		out.Correct += correct
		out.Degraded += degraded
		out.Undetermined += undetermined
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	rows.Close()
	// The headline state is the newest verdict, not the ratio: a single degraded
	// probe in the most recent bucket is exactly what a visitor must see.
	var lastAt sql.NullTime
	var lastVerdict string
	err = tx.QueryRowContext(ctx, `SELECT COALESCE(finished_at,created_at), COALESCE(evaluation->>'answer_verdict','') FROM account_tests WHERE test_type=$1 AND status NOT IN ('queued','running','cancelled') AND ($2::timestamptz IS NULL OR (created_at >= $2 AND COALESCE(started_at,created_at) >= $2)) ORDER BY COALESCE(finished_at,created_at) DESC, id DESC LIMIT 1`,
		service.DegradationTestTypeProbe, out.ResetAt).Scan(&lastAt, &lastVerdict)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}
	if lastAt.Valid {
		stamp := lastAt.Time
		out.LastProbeAt = &stamp
		switch lastVerdict {
		case "correct":
			out.CurrentState = "healthy"
		case "incorrect":
			out.CurrentState = "degraded"
		}
	}
	if out.Total > 0 {
		out.HealthyRatio = float64(out.Correct) / float64(out.Total)
	}
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM accounts WHERE deleted_at IS NULL AND degradation_suspended_until IS NOT NULL AND degradation_suspended_until > NOW()`).Scan(&out.Suspended); err != nil {
		return nil, err
	}
	return out, tx.Commit()
}
