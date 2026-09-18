-- WishTeam5X is opt-in. No existing account/group configuration is changed.
CREATE TABLE IF NOT EXISTS wishteam_settings (
  id integer PRIMARY KEY CHECK (id = 1),
  enabled boolean NOT NULL DEFAULT false,
  group_id bigint,
  interval_minutes integer NOT NULL DEFAULT 10 CHECK (interval_minutes BETWEEN 1 AND 1440),
  next_run_at timestamptz NOT NULL DEFAULT NOW(),
  submit_after timestamptz NOT NULL DEFAULT NOW(),
  updated_at timestamptz NOT NULL DEFAULT NOW()
);
INSERT INTO wishteam_settings(id) VALUES(1) ON CONFLICT DO NOTHING;

CREATE TABLE IF NOT EXISTS wishteam_runs (
  id bigserial PRIMARY KEY,
  group_id bigint NOT NULL,
  status text NOT NULL DEFAULT 'running',
  total integer NOT NULL DEFAULT 0,
  message text NOT NULL DEFAULT '',
  requested_by bigint,
  created_at timestamptz NOT NULL DEFAULT NOW(),
  finished_at timestamptz
);
CREATE UNIQUE INDEX IF NOT EXISTS wishteam_one_run ON wishteam_runs ((1)) WHERE status = 'running';
CREATE TABLE IF NOT EXISTS wishteam_batches (
  id bigserial PRIMARY KEY,
  run_id bigint NOT NULL REFERENCES wishteam_runs(id),
  status text NOT NULL DEFAULT 'queued',
  remote_id text NOT NULL DEFAULT '',
  next_poll_at timestamptz NOT NULL DEFAULT NOW(),
  errors integer NOT NULL DEFAULT 0,
  result jsonb,
  created_at timestamptz NOT NULL DEFAULT NOW()
);
CREATE TABLE IF NOT EXISTS wishteam_items (
  id bigserial PRIMARY KEY,
  run_id bigint NOT NULL REFERENCES wishteam_runs(id),
  batch_id bigint REFERENCES wishteam_batches(id),
  account_id bigint NOT NULL,
  new_account_id bigint,
  email text NOT NULL,
  status text NOT NULL DEFAULT 'queued',
  stage text NOT NULL DEFAULT 'queued',
  message text NOT NULL DEFAULT '',
  error_code text NOT NULL DEFAULT '',
  probe jsonb NOT NULL DEFAULT '{}',
  retry_after integer NOT NULL DEFAULT 0,
  snapshot jsonb NOT NULL,
  updated_at timestamptz NOT NULL DEFAULT NOW(),
  UNIQUE(run_id,account_id)
);
CREATE INDEX IF NOT EXISTS wishteam_items_run ON wishteam_items(run_id,id);
CREATE INDEX IF NOT EXISTS wishteam_items_batch ON wishteam_items(batch_id);
CREATE INDEX IF NOT EXISTS wishteam_items_retry ON wishteam_items(email,updated_at) WHERE retry_after>0;
-- Full secrets are kept server-side, never in progress responses.
-- A committed archive and replacement are one transaction. Failed imports roll back
-- the deletion; completed API results remain in wishteam_batches for diagnosis.
CREATE TABLE IF NOT EXISTS wishteam_archives (
  id bigserial PRIMARY KEY,
  item_id bigint NOT NULL UNIQUE REFERENCES wishteam_items(id),
  old_account_id bigint NOT NULL,
  new_account_id bigint,
  account jsonb NOT NULL,
  groups jsonb NOT NULL,
  plans jsonb NOT NULL,
  sub2 jsonb NOT NULL,
  created_at timestamptz NOT NULL DEFAULT NOW()
);
