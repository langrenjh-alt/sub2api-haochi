ALTER TABLE groups
    ADD COLUMN IF NOT EXISTS burst_mode_429_retry_count INTEGER NOT NULL DEFAULT 10,
    ADD COLUMN IF NOT EXISTS burst_mode_high_usage_enabled BOOLEAN NOT NULL DEFAULT FALSE;

UPDATE groups
SET burst_mode_429_retry_count = 10
WHERE burst_mode_429_retry_count < 1 OR burst_mode_429_retry_count > 100;

ALTER TABLE groups
    DROP CONSTRAINT IF EXISTS groups_burst_mode_429_retry_count_check;

ALTER TABLE groups
    ADD CONSTRAINT groups_burst_mode_429_retry_count_check
    CHECK (burst_mode_429_retry_count BETWEEN 1 AND 100);
