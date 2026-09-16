package repository

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"

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
                     WHERE t.account_id = a.id AND t.test_type = $2
                       AND t.status NOT IN ('queued','running','cancelled')), '-infinity'::timestamptz) AS last_at
    FROM accounts a
    JOIN account_groups ag ON ag.account_id = a.id AND ag.group_id = $1
    WHERE a.deleted_at IS NULL AND a.schedulable = TRUE
      AND NOT EXISTS (SELECT 1 FROM account_tests q
                      WHERE q.account_id = a.id AND q.test_type = $2 AND q.status IN ('queued','running'))
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
func (r *degradationRepository) DuePreviewAccountIDs(ctx context.Context, groupIDs []int64, _ int, limit int) ([]int64, error) {
	rows, err := r.db.QueryContext(ctx, `
SELECT a.id
FROM accounts a
JOIN account_groups ag ON ag.account_id = a.id
JOIN groups g ON g.id = ag.group_id
WHERE g.deleted_at IS NULL AND g.degradation_detection_enabled AND g.degradation_preview_enabled
  AND g.id = ANY($2)
  AND a.deleted_at IS NULL AND a.schedulable = TRUE
  AND (a.degradation_suspended_until IS NULL OR a.degradation_suspended_until <= NOW())
  AND NOT EXISTS (SELECT 1 FROM account_tests q WHERE q.test_type = $1 AND q.status IN ('queued','running'))
ORDER BY random() LIMIT $3`,
		service.DegradationTestTypePreview, pq.Array(groupIDs), limit)
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
func (r *degradationRepository) EnqueueDegradationTest(ctx context.Context, accountID int64, testType, prompt, model, reasoningEffort string, timeoutSeconds int) (int64, bool, error) {
	cfg := service.IntelligentTestConfig{
		Prompt:          prompt,
		Model:           model,
		ReasoningEffort: reasoningEffort,
		Evaluator:       degradationEvaluatorFor(testType),
		ExpectedAnswer:  service.DegradationExpectedAnswer,
		TimeoutSeconds:  timeoutSeconds,
		AnswerType:      "number",
		AnswerFormat:    "free_text",
		AnswerUnitMode:  "none",
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
	err = r.db.QueryRowContext(ctx, `
INSERT INTO account_tests(account_id, test_type, input, model, anti_degradation, config_snapshot, requested_by)
SELECT $1, $2, $3, $4,
       COALESCE((a.extra->'anti_degradation')='true'::jsonb, (a.extra#>'{anti_degrade,enabled}')='true'::jsonb, false),
       $5::jsonb, NULL
FROM accounts a
WHERE a.id = $1 AND a.deleted_at IS NULL
  AND NOT EXISTS (SELECT 1 FROM account_tests q WHERE q.account_id = $1 AND q.test_type = $2 AND q.status IN ('queued','running'))
RETURNING id`, accountID, testType, prompt, model, string(encoded)).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, false, nil
	}
	if err != nil {
		return 0, false, err
	}
	return id, true, nil
}

func degradationEvaluatorFor(testType string) string {
	if testType == service.DegradationTestTypePreview {
		return "svg_structure"
	}
	return "exact_answer"
}

// ApplyProbeOutcome translates a persisted verdict into scheduling state.
//
//   - degraded: suspend through the official temporary-unschedulable window and
//     remember that the window is ours. Only schedulable accounts are touched,
//     so a manual disable is never rewritten.
//   - recovered: drop our window only while its reason still carries our marker,
//     and always clear our own columns. An unrelated pause survives verbatim.
func (r *degradationRepository) ApplyProbeOutcome(ctx context.Context, accountID int64, degraded bool, suspendMinutes int, note string) (bool, error) {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer func() { _ = tx.Rollback() }()
	if degraded {
		result, err := tx.ExecContext(ctx, `
UPDATE accounts SET temp_unschedulable_until = NOW() + make_interval(mins => $2::int),
                    temp_unschedulable_reason = $3,
                    degradation_suspended_until = NOW() + make_interval(mins => $2::int),
                    degradation_suspended_at = NOW(),
                    degradation_suspend_note = $3,
                    updated_at = NOW()
WHERE id = $1 AND deleted_at IS NULL AND schedulable = TRUE`, accountID, suspendMinutes, note)
		if err != nil {
			return false, err
		}
		affected, err := result.RowsAffected()
		if err != nil {
			return false, err
		}
		return affected == 1, tx.Commit()
	}
	if _, err := tx.ExecContext(ctx, `
UPDATE accounts SET temp_unschedulable_until = NULL, temp_unschedulable_reason = '', updated_at = NOW()
WHERE id = $1 AND degradation_suspended_until IS NOT NULL AND temp_unschedulable_reason LIKE $2`, accountID, service.DegradationSuspendReasonPrefix+"%"); err != nil {
		return false, err
	}
	result, err := tx.ExecContext(ctx, `
UPDATE accounts SET degradation_suspended_until = NULL, degradation_suspended_at = NULL, degradation_suspend_note = '', updated_at = NOW()
WHERE id = $1 AND degradation_suspended_until IS NOT NULL`, accountID)
	if err != nil {
		return false, err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return false, err
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
	out.Model = cfg.PreviewModel
	out.ReasoningEffort = cfg.PreviewReasoningEffort
	out.IntervalSeconds = cfg.PreviewIntervalMinute * 60
	if previewGroups == 0 {
		return out, nil
	}
	if err := r.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM account_tests WHERE test_type=$1 AND status NOT IN ('queued','running','cancelled') AND result_image <> ''`, service.DegradationTestTypePreview).Scan(&out.Total); err != nil {
		return nil, err
	}
	var lastAt sql.NullTime
	if err := r.db.QueryRowContext(ctx, `SELECT status, finished_at FROM account_tests WHERE test_type=$1 AND status NOT IN ('queued','running') ORDER BY id DESC LIMIT 1`, service.DegradationTestTypePreview).Scan(&out.LastStatus, &lastAt); err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}
	if lastAt.Valid {
		out.LastFinishedAt = &lastAt.Time
	}
	rows, err := r.db.QueryContext(ctx, `SELECT `+degradationPublicColumns+` FROM account_tests t WHERE t.test_type=$1 AND t.status NOT IN ('queued','running','cancelled') AND t.result_image <> '' ORDER BY t.id DESC LIMIT $2 OFFSET $3`,
		service.DegradationTestTypePreview, pageSize, (page-1)*pageSize)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		item, err := scanDegradationPublicWork(rows.Scan)
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
	return scanDegradationPublicWork(r.db.QueryRowContext(ctx, `SELECT `+degradationPublicColumns+` FROM account_tests t WHERE t.id=$1 AND t.test_type=$2 AND t.status NOT IN ('queued','running','cancelled') AND t.result_image <> ''`,
		id, service.DegradationTestTypePreview).Scan)
}
