-- Codex turn-state pooling: storage for the upstream-issued `x-codex-turn-state`
-- header (the article's `current_turn_state`) plus one row per observation.
--
-- Isolated storage. It never touches business tables: accounts, groups and
-- settings are only read. The newest `active` row per account is the state the
-- gateway injects; older rows are retained as the operator-visible timeline.
CREATE TABLE IF NOT EXISTS codex_turn_states (
  id                BIGSERIAL PRIMARY KEY,
  account_id        BIGINT NOT NULL,
  group_id          BIGINT NOT NULL DEFAULT 0,
  -- state: the opaque upstream token. Required for injection, so it is stored
  -- verbatim; the admin API only ever exposes state_fingerprint.
  state             TEXT NOT NULL DEFAULT '',
  state_fingerprint TEXT NOT NULL DEFAULT '',
  -- status: active | revoked | failed | expired
  status            TEXT NOT NULL DEFAULT 'active',
  -- source: harvest (taken from live traffic) | probe (keeper refresh) | manual
  source            TEXT NOT NULL DEFAULT 'harvest',
  http_status       INTEGER NOT NULL DEFAULT 0,
  model             TEXT NOT NULL DEFAULT '',
  proxy_id          BIGINT,
  latency_ms        BIGINT NOT NULL DEFAULT 0,
  -- issued_at is decoded from the token itself, so the countdown reflects the
  -- upstream clock rather than our first sighting of the value.
  issued_at         TIMESTAMPTZ,
  expires_at        TIMESTAMPTZ,
  error             TEXT NOT NULL DEFAULT '',
  created_at        TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_codex_turn_states_account
  ON codex_turn_states (account_id, id DESC);
CREATE INDEX IF NOT EXISTS idx_codex_turn_states_active
  ON codex_turn_states (status, expires_at);
CREATE INDEX IF NOT EXISTS idx_codex_turn_states_created
  ON codex_turn_states (created_at DESC);
