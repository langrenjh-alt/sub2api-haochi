-- Isolated monitoring storage. No UPDATE/ALTER to business accounts or settings.
CREATE TABLE IF NOT EXISTS pool_runway_samples (
  group_id BIGINT NOT NULL,
  bucket TIMESTAMPTZ NOT NULL,
  sampled_at TIMESTAMPTZ NOT NULL,
  payload JSONB NOT NULL,
  PRIMARY KEY(group_id,bucket)
);
CREATE TABLE IF NOT EXISTS pool_runway_history (
  group_id BIGINT NOT NULL,
  bucket TIMESTAMPTZ NOT NULL,
  policy TEXT NOT NULL,
  method TEXT NOT NULL,
  payload JSONB NOT NULL,
  PRIMARY KEY(group_id,bucket,policy,method)
);
CREATE TABLE IF NOT EXISTS pool_runway_batches (
  group_id BIGINT NOT NULL,
  identity_hash TEXT NOT NULL,
  first_created_at TIMESTAMPTZ NOT NULL,
  PRIMARY KEY(group_id,identity_hash)
);
