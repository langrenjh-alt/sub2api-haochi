-- Monitoring-only configuration. NULL preserves the existing env/name fallback.
CREATE TABLE IF NOT EXISTS pool_runway_settings (
    id SMALLINT PRIMARY KEY CHECK (id = 1),
    group_id BIGINT CHECK (group_id > 0),
    revision BIGINT NOT NULL DEFAULT 0,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
INSERT INTO pool_runway_settings(id) VALUES(1) ON CONFLICT(id) DO NOTHING;
