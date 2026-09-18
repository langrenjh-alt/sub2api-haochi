ALTER TABLE groups
    ADD COLUMN IF NOT EXISTS anti_degrade_preset VARCHAR(32) NOT NULL DEFAULT '';

COMMENT ON COLUMN groups.anti_degrade_preset IS
    'Group-wide anti-degradation strategy preset (strategy registry ID, e.g. legacy/low_concurrency). Non-empty makes the reconciler continuously align every member account to that preset, overriding per-account concurrency/fingerprint settings; empty leaves each account on its own settings.';
