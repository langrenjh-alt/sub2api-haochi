package repository

import (
	"context"
	"database/sql"
	"encoding/json"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"time"
)

// SchedulePublicSamples treats a finished group interval cohort as a completed round.
// A persisted source_round_id makes the post-sweep sample restart-safe and unique.
// Samples never extend bulk coverage timestamps or apply account move/suspend policy.
func (r *degradationRepository) SchedulePublicSamples(ctx context.Context) error {
	rows, err := r.db.QueryContext(ctx, `
 SELECT g.id, (SELECT max(t.id) FROM account_tests t WHERE t.test_type='degradation_probe'
 AND t.config_snapshot->>'degradation_group_id'=g.id::text
 AND COALESCE(t.config_snapshot->>'public_sample','false')<>'true'
 GROUP BY floor(extract(epoch FROM t.created_at)/(GREATEST(1,COALESCE((g.degradation_detection_config->>'interval_minutes')::int,10))*60))
 HAVING count(*) FILTER(WHERE t.status IN ('queued','running'))=0
 AND count(*) FILTER(WHERE t.status<>'cancelled')>0
 ORDER BY max(t.id) DESC LIMIT 1) AS round_id,
 (SELECT a.id FROM accounts a JOIN account_groups ag ON ag.account_id=a.id
 WHERE ag.group_id=g.id AND a.deleted_at IS NULL AND a.schedulable
 AND (a.degradation_suspended_until IS NULL OR a.degradation_suspended_until<=NOW())
 ORDER BY random() LIMIT 1)
 FROM groups g WHERE g.deleted_at IS NULL AND g.degradation_detection_enabled`)
	if err != nil {
		return err
	}
	type candidate struct{ group, round, account int64 }
	var candidates []candidate
	for rows.Next() {
		var group int64
		var round, account sql.NullInt64
		if err = rows.Scan(&group, &round, &account); err != nil {
			rows.Close()
			return err
		}
		if round.Valid && account.Valid {
			candidates = append(candidates, candidate{group, round.Int64, account.Int64})
		}
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, c := range candidates {
		if _, _, err = r.enqueueDegradationTest(ctx, c.group, c.account, service.DegradationTestTypeProbe, "", "", "", "", 0, c.round); err != nil {
			return err
		}
	}
	return nil
}

// SampleTimeline deliberately excludes historical fleet aggregates. Empty slots
// stay empty until a real post-sweep request finishes; no historical results are invented.
func (r *degradationRepository) SampleTimeline(ctx context.Context, hours int) (*service.DegradationTimeline, error) {
	if hours != 24 && hours != 72 && hours != 168 {
		hours = 24
	}
	tx, err := r.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	var now time.Time
	var reset sql.NullTime
	var interval int
	err = tx.QueryRowContext(ctx, `SELECT transaction_timestamp(),(SELECT value::timestamptz FROM settings WHERE key=$1),COALESCE((SELECT min(GREATEST(1,COALESCE((degradation_detection_config->>'interval_minutes')::int,10))) FROM groups WHERE deleted_at IS NULL AND degradation_detection_enabled),10)`, degradationPublicResetKey).Scan(&now, &reset, &interval)
	if err != nil {
		return nil, err
	}
	minutes := hours * 60 / 144
	span := time.Duration(minutes) * time.Minute
	// Exactly 144 slots ending with the current wall-clock slot.
	newest := now.Truncate(span)
	start := newest.Add(-143 * span)
	out := &service.DegradationTimeline{Mode: "round_sample", RangeHours: hours, BucketMinute: minutes, IntervalMinutes: interval, GeneratedAt: now, CurrentState: "unknown", Buckets: make([]service.DegradationTimelineBucket, 144)}
	if reset.Valid {
		out.ResetAt = &reset.Time
	}
	for i := range out.Buckets {
		out.Buckets[i].Start = start.Add(time.Duration(i) * span)
		out.Buckets[i].State = "empty"
	}
	rows, err := tx.QueryContext(ctx, `SELECT id,status,duration_ms,model,created_at,finished_at,COALESCE(evaluation->>'answer_verdict',''),config_snapshot
 FROM account_tests WHERE test_type='degradation_probe' AND config_snapshot->>'public_sample'='true'
 AND status<>'cancelled' AND ($1::timestamptz IS NULL OR (created_at >= $1 AND COALESCE(started_at,created_at)>=$1))
 AND COALESCE(finished_at,created_at)>=$2 ORDER BY COALESCE(finished_at,created_at),id`, out.ResetAt, start)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		v := &service.DegradationSample{}
		var finished sql.NullTime
		var verdict string
		var cfgRaw []byte
		if err = rows.Scan(&v.ID, &v.Status, &v.DurationMS, &v.Model, &v.CreatedAt, &finished, &verdict, &cfgRaw); err != nil {
			rows.Close()
			return nil, err
		}
		var cfg service.IntelligentTestConfig
		_ = json.Unmarshal(cfgRaw, &cfg)
		v.OutputTokens = cfg.OutputTokens
		at := v.CreatedAt
		v.State = "unknown"
		if finished.Valid {
			v.FinishedAt = &finished.Time
			at = finished.Time
		}
		pending := v.Status == "queued" || v.Status == "running"
		if pending {
			v.State = "running"
			out.Running = true
		} else if v.Status == "completed" && verdict == "correct" {
			v.State = "healthy"
		} else if v.Status == "completed" && verdict == "incorrect" {
			v.State = "degraded"
		}
		pos := int(at.Sub(start) / span)
		if pos < 0 || pos >= len(out.Buckets) {
			continue
		}
		bucket := &out.Buckets[pos]
		bucket.Sample = v
		bucket.State = v.State
		if !pending {
			bucket.Total++
			out.Total++
			switch v.State {
			case "healthy":
				bucket.Correct++
				out.Correct++
			case "degraded":
				bucket.Degraded++
				out.Degraded++
			default:
				bucket.Undetermined++
				out.Undetermined++
			}
			out.LatestSample = v
			out.LastProbeAt = &at
			out.CurrentState = v.State
		}
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	if out.Total > 0 {
		out.HealthyRatio = float64(out.Correct) / float64(out.Total)
	}
	// Next time is an estimate only: a new sample waits for the next sweep to drain.
	if out.LatestSample != nil {
		at := out.LatestSample.CreatedAt.Add(time.Duration(interval) * time.Minute)
		out.NextProbeAt = &at
	}
	return out, tx.Commit()
}
