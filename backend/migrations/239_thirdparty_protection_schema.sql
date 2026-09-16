-- 239_thirdparty_protection_schema.sql
-- 第三方二开（防降智 / 防并发限制 / 安全策略 / 智能测试 / 工单等）所需的表与列。
--
-- 该二开的源码附带的后端只含代码，未附带这些 DDL（其 migrations 目录被裁剪到
-- 134 号），因此在这里补齐；全部语句幂等，可重复执行。
--
-- 1) groups 安全策略字段（ent: security_policy_enabled / mode / email_enabled）
-- 2) api_keys 历史仅哈希 key 的兼容列（ent: key_hash / key_prefix）
-- 3) security_policy_keywords（ent 表：分组/全局安全策略关键词）
-- 4) global_model_pricing（全局模型定价）
-- 5) test_settings / account_tests / intelligent_test_requests（智能测试）
-- 6) support_tickets / support_ticket_replies（工单）
-- 7) user_cleanup_previews / user_cleanup_guards / user_cleanup_identity_archives（用户清理）

-- ---------------------------------------------------------------- groups
ALTER TABLE groups ADD COLUMN IF NOT EXISTS security_policy_enabled BOOLEAN NOT NULL DEFAULT FALSE;
ALTER TABLE groups ADD COLUMN IF NOT EXISTS security_policy_mode VARCHAR(20) NOT NULL DEFAULT 'block_session';
ALTER TABLE groups ADD COLUMN IF NOT EXISTS security_policy_email_enabled BOOLEAN NOT NULL DEFAULT TRUE;

-- ---------------------------------------------------------------- api_keys
ALTER TABLE api_keys ADD COLUMN IF NOT EXISTS key_hash VARCHAR(64);
ALTER TABLE api_keys ADD COLUMN IF NOT EXISTS key_prefix VARCHAR(16);

-- ------------------------------------------- security_policy_keywords (ent)
CREATE TABLE IF NOT EXISTS security_policy_keywords (
    id BIGSERIAL PRIMARY KEY,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    deleted_at TIMESTAMPTZ,
    group_id BIGINT REFERENCES groups(id) ON DELETE SET NULL,
    keyword VARCHAR(200) NOT NULL,
    category VARCHAR(50) NOT NULL DEFAULT '',
    enabled BOOLEAN NOT NULL DEFAULT TRUE
);

CREATE UNIQUE INDEX IF NOT EXISTS idx_secpol_kw_global_unique
    ON security_policy_keywords (keyword) WHERE group_id IS NULL AND deleted_at IS NULL;
CREATE UNIQUE INDEX IF NOT EXISTS idx_secpol_kw_group_unique
    ON security_policy_keywords (group_id, keyword) WHERE group_id IS NOT NULL AND deleted_at IS NULL;
CREATE INDEX IF NOT EXISTS security_policy_keywords_group_id ON security_policy_keywords (group_id);
CREATE INDEX IF NOT EXISTS security_policy_keywords_enabled ON security_policy_keywords (enabled);
CREATE INDEX IF NOT EXISTS security_policy_keywords_deleted_at ON security_policy_keywords (deleted_at);

-- --------------------------------------------------- global_model_pricing
CREATE TABLE IF NOT EXISTS global_model_pricing (
    id BIGSERIAL PRIMARY KEY,
    model_pattern TEXT NOT NULL,
    billing_mode TEXT NOT NULL DEFAULT '',
    input_price DOUBLE PRECISION,
    output_price DOUBLE PRECISION,
    cache_write_price DOUBLE PRECISION,
    cache_write_1h_price DOUBLE PRECISION,
    cache_read_price DOUBLE PRECISION,
    per_request_price DOUBLE PRECISION,
    enabled BOOLEAN NOT NULL DEFAULT TRUE,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX IF NOT EXISTS global_model_pricing_enabled ON global_model_pricing (enabled);

-- ------------------------------------------------------- intelligent tests
CREATE TABLE IF NOT EXISTS test_settings (
    test_type TEXT PRIMARY KEY,
    enabled BOOLEAN NOT NULL DEFAULT FALSE,
    user_visible BOOLEAN NOT NULL DEFAULT TRUE,
    config JSONB NOT NULL DEFAULT '{}'::jsonb,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE TABLE IF NOT EXISTS account_tests (
    id BIGSERIAL PRIMARY KEY,
    account_id BIGINT NOT NULL,
    test_type TEXT NOT NULL,
    status TEXT NOT NULL DEFAULT 'queued',
    score DOUBLE PRECISION,
    result TEXT NOT NULL DEFAULT '',
    result_image TEXT NOT NULL DEFAULT '',
    input TEXT NOT NULL DEFAULT '',
    raw_response TEXT NOT NULL DEFAULT '',
    raw_truncated BOOLEAN NOT NULL DEFAULT FALSE,
    error_message TEXT NOT NULL DEFAULT '',
    duration_ms BIGINT NOT NULL DEFAULT 0,
    model TEXT NOT NULL DEFAULT '',
    anti_degradation BOOLEAN NOT NULL DEFAULT FALSE,
    config_snapshot JSONB,
    evaluation JSONB,
    requested_by BIGINT,
    lease_token VARCHAR(64),
    queue_reason TEXT NOT NULL DEFAULT '',
    available_at TIMESTAMPTZ,
    started_at TIMESTAMPTZ,
    finished_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX IF NOT EXISTS account_tests_account_id ON account_tests (account_id);
CREATE INDEX IF NOT EXISTS account_tests_test_type ON account_tests (test_type);
CREATE INDEX IF NOT EXISTS account_tests_status ON account_tests (status);
CREATE INDEX IF NOT EXISTS account_tests_available_at ON account_tests (available_at);
CREATE INDEX IF NOT EXISTS account_tests_created_at ON account_tests (created_at);

CREATE TABLE IF NOT EXISTS intelligent_test_requests (
    id BIGSERIAL PRIMARY KEY,
    actor_id BIGINT NOT NULL,
    request_key TEXT NOT NULL,
    fingerprint TEXT NOT NULL DEFAULT '',
    record_ids BIGINT[] NOT NULL DEFAULT '{}',
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE UNIQUE INDEX IF NOT EXISTS intelligent_test_requests_actor_request_key
    ON intelligent_test_requests (actor_id, request_key);

-- ------------------------------------------------------------- support desk
CREATE TABLE IF NOT EXISTS support_tickets (
    id BIGSERIAL PRIMARY KEY,
    user_id BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    subject TEXT NOT NULL DEFAULT '',
    status VARCHAR(32) NOT NULL DEFAULT 'open',
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    closed_at TIMESTAMPTZ
);
CREATE INDEX IF NOT EXISTS support_tickets_user_id ON support_tickets (user_id);
CREATE INDEX IF NOT EXISTS support_tickets_status ON support_tickets (status);

CREATE TABLE IF NOT EXISTS support_ticket_replies (
    id BIGSERIAL PRIMARY KEY,
    ticket_id BIGINT NOT NULL REFERENCES support_tickets(id) ON DELETE CASCADE,
    author_type VARCHAR(32) NOT NULL DEFAULT 'user',
    author_id BIGINT,
    body TEXT NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX IF NOT EXISTS support_ticket_replies_ticket_id ON support_ticket_replies (ticket_id);

-- ------------------------------------------------------------ user cleanup
CREATE TABLE IF NOT EXISTS user_cleanup_previews (
    id UUID PRIMARY KEY,
    actor_user_id BIGINT NOT NULL,
    candidate_ids BIGINT[] NOT NULL DEFAULT '{}',
    candidate_snapshot JSONB NOT NULL DEFAULT '[]'::jsonb,
    result JSONB,
    expires_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    completed_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX IF NOT EXISTS user_cleanup_previews_actor_user_id ON user_cleanup_previews (actor_user_id);

CREATE TABLE IF NOT EXISTS user_cleanup_guards (
    user_id BIGINT PRIMARY KEY REFERENCES users(id) ON DELETE CASCADE,
    is_system BOOLEAN NOT NULL DEFAULT FALSE,
    is_protected BOOLEAN NOT NULL DEFAULT FALSE,
    updated_by BIGINT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE TABLE IF NOT EXISTS user_cleanup_identity_archives (
    id BIGSERIAL PRIMARY KEY,
    preview_id UUID NOT NULL,
    user_id BIGINT NOT NULL,
    identities JSONB NOT NULL DEFAULT '[]'::jsonb,
    channels JSONB NOT NULL DEFAULT '[]'::jsonb,
    adoption_decisions JSONB NOT NULL DEFAULT '[]'::jsonb,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX IF NOT EXISTS user_cleanup_identity_archives_preview_id ON user_cleanup_identity_archives (preview_id);
CREATE INDEX IF NOT EXISTS user_cleanup_identity_archives_user_id ON user_cleanup_identity_archives (user_id);
