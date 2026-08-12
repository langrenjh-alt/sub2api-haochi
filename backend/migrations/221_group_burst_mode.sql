ALTER TABLE groups
    ADD COLUMN IF NOT EXISTS burst_mode_enabled BOOLEAN NOT NULL DEFAULT FALSE,
    ADD COLUMN IF NOT EXISTS burst_mode_threshold_percent INTEGER NOT NULL DEFAULT 90;

UPDATE groups
SET burst_mode_threshold_percent = 90
WHERE burst_mode_threshold_percent < 1 OR burst_mode_threshold_percent > 100;

ALTER TABLE groups
    DROP CONSTRAINT IF EXISTS groups_burst_mode_threshold_percent_check;

ALTER TABLE groups
    ADD CONSTRAINT groups_burst_mode_threshold_percent_check
    CHECK (burst_mode_threshold_percent BETWEEN 1 AND 100);
