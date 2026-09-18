-- Additive only: leave previously applied state-pool migrations unchanged.
ALTER TABLE codex_turn_state_attempts ADD COLUMN IF NOT EXISTS next_retry_at TIMESTAMPTZ;
ALTER TABLE codex_turn_state_attempts ADD COLUMN IF NOT EXISTS upstream_retry_at TIMESTAMPTZ;

CREATE TABLE IF NOT EXISTS codex_turn_state_transfers (
    id BIGSERIAL PRIMARY KEY,
    account_id BIGINT NOT NULL,
    from_group_id BIGINT NOT NULL,
    to_group_id BIGINT NOT NULL,
    ticket_record_id BIGINT NOT NULL DEFAULT 0,
    reason TEXT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX IF NOT EXISTS idx_codex_turn_state_transfers_account
ON codex_turn_state_transfers(account_id,id DESC);

-- Revival remaps local IDs without renewing an upstream-issued ticket.
CREATE TABLE IF NOT EXISTS codex_turn_state_revivals (
    old_account_id BIGINT PRIMARY KEY,
    new_account_id BIGINT NOT NULL,
    ticket_record_id BIGINT NOT NULL DEFAULT 0,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
