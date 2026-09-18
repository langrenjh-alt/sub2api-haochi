-- Counters are independent of pruned observation history so restarting or
-- pruning cannot silently bypass a per-account collection limit.
CREATE TABLE IF NOT EXISTS codex_turn_state_attempts (
  account_id BIGINT PRIMARY KEY,
  attempts INTEGER NOT NULL DEFAULT 0 CHECK (attempts >= 0),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
