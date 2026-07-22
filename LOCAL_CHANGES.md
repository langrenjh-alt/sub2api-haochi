# Local Changes Against Official sub2api v0.1.162

This repository includes official `Wei-Shaw/sub2api` through release tag
`v0.1.162` at commit `34b7a5ad7`.

- Upstream release commit: `34b7a5ad7` (`v0.1.162`)
- Upstream merge target: `v0.1.162^{}` (`34b7a5ad7`)
- Working branch: `merge-user-changes-v0.1.161`
- Original local change bundle: `E:\号池sub2api\改动`
- Original local commits: `f1a65550`, `66701c1c`, `46781d3a`
- Last upstream merge: 2026-07-22 (`v0.1.162@34b7a5ad7`)
- Fork build version: `backend/cmd/server/VERSION` is `0.1.162`.
- Purpose: preserve local behavior when upgrading to a newer official release.

## Kiro and Compatibility File List

Modified files:

- `backend/internal/handler/admin/account_handler.go`
- `backend/internal/handler/dto/credentials_redact_test.go`
- `backend/internal/repository/account_repo.go`
- `backend/internal/repository/account_repo_integration_test.go`
- `backend/internal/service/account_credentials_redact.go`
- `backend/internal/service/account_credentials_redact_test.go`
- `backend/internal/service/account_test_service.go`
- `backend/internal/service/openai_images.go`
- `backend/internal/service/ratelimit_service.go`
- `backend/internal/service/ratelimit_service_403_test.go`
- `backend/internal/service/ratelimit_service_openai_test.go`
- `frontend/src/components/account/AccountCapacityCell.vue`
- `frontend/src/types/index.ts`
- `frontend/src/views/admin/AccountsView.vue`

Added files:

- `backend/internal/handler/admin/kiro_balance.go`
- `backend/internal/handler/admin/kiro_balance_test.go`

## Functional Changes

### 0. Cascaded Grok Stream Keepalive

- Chat and Messages bridges schedule heartbeats from the last downstream write,
  not the last upstream line. Upstream SSE comments are commonly consumed by an
  intermediate gateway and must not suppress its client-facing heartbeat.
- Large Chat requests (64 KiB and above) continue sending SSE comment
  heartbeats while semantic chunks are staged by the silent-refusal detector.
- Raw Chat passthrough forwards upstream SSE comments immediately instead of
  staging them with semantic output.
- Silent-refusal failover remains allowed after heartbeat-only writes.
- Claude Code thinking signatures remain available in client responses, but
  historical signatures are removed before rebuilding Grok input because xAI
  encrypted reasoning is not portable across accounts/cache identities.

This prevents cascaded `sub2api -> sub2api -> Cloudflare` requests from idling
past Cloudflare's 120-second proxy read timeout during long Grok generations.

Affected files:

- `backend/internal/service/openai_gateway_chat_completions.go`
- `backend/internal/service/openai_gateway_chat_completions_raw.go`
- `backend/internal/service/openai_gateway_messages.go`
- `backend/internal/service/openai_silent_refusal.go`
- `backend/internal/service/openai_gateway_cascade_keepalive_test.go`

### 1. Kiro-rs Balance Display

Backend:

- Added `KiroRSBalanceInfo` and balance fetching helpers in `backend/internal/handler/admin/kiro_balance.go`.
- Reads Kiro-rs connection data from account credentials:
  - `kiro_rs_base_url`, `kiro_base_url`, or `base_url`
  - `kiro_rs_admin_key`, `kiro_admin_key`, or `admin_key`
  - `kiro_rs_credential_id`, `kiro_credential_id`, or `credential_id`
  - optional enable markers: `kiro_rs_balance_enabled`, `kiro_balance_enabled`
- Calls Kiro-rs admin endpoint:
  - `GET /api/admin/credentials/{credential_id}/balance`
  - header: `x-api-key: <admin_key>`
- Uses short TTL cache:
  - success: 60 seconds
  - error: 15 seconds
- Adds `kiro_balance` to admin account responses through `AccountWithConcurrency`.
- In account list API, Kiro balance is fetched only when `lite` mode is not enabled.

Frontend:

- Adds `KiroBalanceInfo` and `Account.kiro_balance` type fields.
- Adds a Kiro capacity badge to `AccountCapacityCell.vue`.
- Badge shows remaining / limit and tooltip with used, remaining, percentage, subscription title, or error.
- `AccountsView.vue` no longer sends `lite=1` on first load when the capacity column is visible, so the backend can return Kiro balance data.

Upgrade notes:

- Reapply `kiro_balance.go` and `kiro_balance_test.go`.
- Reapply the `KiroBalance` field in `AccountWithConcurrency`.
- Reapply frontend `KiroBalanceInfo`, `account.kiro_balance`, capacity badge UI, and `lite` condition.

### 2. Sensitive Credential Redaction

Added these Kiro admin key names to the sensitive credential whitelist:

- `kiro_rs_admin_key`
- `kiro_admin_key`
- `admin_key`

Affected files:

- `backend/internal/service/account_credentials_redact.go`
- `backend/internal/service/account_credentials_redact_test.go`
- `backend/internal/handler/dto/credentials_redact_test.go`

Purpose:

- Prevent Kiro-rs admin keys from being returned to the frontend through account credentials.

Upgrade notes:

- Preserve these keys in `SensitiveCredentialKeys`.
- Preserve matching tests in service and DTO redaction tests.

### 3. Account Test and Image API Compatibility

Gemini account tests:

- Image-generation model tests now use non-streaming `generateContent` instead of `streamGenerateContent`.
- This is meant to avoid upstreams that fail reCAPTCHA or compatibility checks on streaming image calls but succeed on non-streaming calls.
- Added parser for non-streaming Gemini responses, including:
  - direct Gemini response shape
  - Gemini CLI wrapper shape: `{ "response": { ... } }`
  - text parts
  - `inlineData` image parts converted to `data:<mime>;base64,...`

OpenAI-compatible image endpoint:

- `openai_images.go` now allows account-level model aliases for upstream image models.
- If request model maps to a different upstream model, the mapped model does not have to match OpenAI's built-in `gpt-image-*` naming.
- Still validates the inbound/public request model when no mapping changes it.
- Empty mapped upstream model remains an error.

Affected files:

- `backend/internal/service/account_test_service.go`
- `backend/internal/service/openai_images.go`

Upgrade notes:

- Preserve the extra `stream bool` parameter on Gemini request builders.
- Preserve `processGeminiGenerateContentResponse`.
- Preserve the alias-friendly image model validation logic in `openai_images.go`.

### 4. Rate Limit Scheduling Behavior

`SetRateLimited` now also marks the account temporarily unschedulable until the rate limit reset time.

Behavior:

- Sets `rate_limited_at`.
- Sets `rate_limit_reset_at`.
- Sets `temp_unschedulable_until` to `resetAt` only if no longer temporary block already exists.
- Sets `temp_unschedulable_reason` with a JSON reason containing status code `429`.
- Does not shorten an existing longer temporary unschedulable window.
- Returns `service.ErrAccountNotFound` if no active account row is updated.
- Still enqueues scheduler outbox change after update.

Affected files:

- `backend/internal/repository/account_repo.go`
- `backend/internal/repository/account_repo_integration_test.go`

Upgrade notes:

- Preserve the SQL `UPDATE accounts ... CASE WHEN temp_unschedulable_until IS NULL OR temp_unschedulable_until < $2 THEN ...`.
- Preserve integration tests:
  - `TestSetRateLimited`
  - `TestSetRateLimitedDoesNotShortenExistingTempUnschedulable`

### 5. OpenAI 403 Credential-owner Handling

Generic OpenAI 403 handling follows official sub2api behavior:

- The first two failures write a 10-minute `OpenAI 403 temporary cooldown (...)` and remove the account from scheduling.
- The third failure within the 180-minute counter window marks the account as error with `consecutive_403=3/3`.
- A missing or failed counter backend marks the account as error instead of leaving it schedulable.

The remaining local extension is limited to deterministic credential-owner failures:

- `biscuit_baker_service_auth_credential_error_status` immediately marks the credential owner as error when the personal access token owner is no longer an active member of the selected workspace.
- This deterministic credential error takes precedence over user-configured temporary-unschedulable rules.

Affected files:

- `backend/internal/service/account_test_service.go`
- `backend/internal/service/account_test_service_openai_403_test.go`
- `backend/internal/service/ratelimit_service.go`
- `backend/internal/service/ratelimit_service_403_test.go`

Upgrade notes:

- Keep generic OpenAI 403 counter, cooldown, and threshold behavior aligned with official sub2api.
- Preserve only the deterministic credential-owner exception before the generic official path.

### 6. Public Capacity Pool on Channel Status

Adds a user-facing shared capacity view for public standard groups.

Behavior:

- Exposes `GET /api/v1/channel-monitors/capacity-pool` when channel monitoring is enabled.
- Aggregates schedulable account state, concurrency, session/RPM limits, and 5h/7d windows per public group.
- Keeps each group independent so one unhealthy group does not hide the capacity of another.
- Displays the result on the user channel status page with English and Chinese translations.

Affected files include:

- `backend/internal/service/group_capacity_service.go`
- `backend/internal/repository/account_repo.go`
- `backend/internal/handler/channel_monitor_user_handler.go`
- `backend/internal/server/routes/user.go`
- `backend/cmd/server/wire_gen.go`
- `frontend/src/api/channelMonitor.ts`
- `frontend/src/components/user/monitor/ChannelCapacityPoolCard.vue`
- `frontend/src/i18n/locales/en/dashboard.ts`
- `frontend/src/i18n/locales/zh/dashboard.ts`
- `frontend/src/views/user/ChannelStatusView.vue`

Upgrade notes:

- Preserve `NewGroupCapacityService` in Wire output.
- If upstream reorganizes locale files, migrate `channelStatus.capacityPool` into the new dashboard locale module.
- Preserve both repository paths: SQL-backed batch loading and the repository fallback used by tests.

### 7. Empty Stream Retry Without Persistent Cooldown

Empty stream or empty response failures remain retryable, but must not temporarily unschedule the account after same-account retries are exhausted.

Behavior:

- `502 Bad Gateway` retryable failures return from `TempUnscheduleRetryableError` without writing account state.
- `400` Google project configuration failures keep their existing temporary cooldown.
- The Antigravity `tempUnscheduleEmptyResponse` helper remains removed (in `antigravity_gateway_retry.go` as of `v0.1.150`).

Affected files:

- `backend/internal/handler/failover_loop.go`
- `backend/internal/service/antigravity_gateway_retry.go`
- `backend/internal/service/gateway_service.go`
- `backend/internal/service/gateway_temp_unschedule_test.go`
- `backend/internal/service/gateway_multiplatform_test.go`

Upgrade notes:

- Official `v0.1.149` split Antigravity code across multiple files; this structure remains in `v0.1.150`, so preserve the behavior in the new retry file rather than restoring the old monolithic service file.
- Keep `RetryableOnSameAccount` enabled so the bounded in-request retry/failover loop still runs.

### 8. Grok OAuth Refresh Scaling

The background OAuth refresh service is sized and coordinated for Grok pools up
to 60,000 accounts.

Behavior:

- Elects one refresh scanner per cycle with the shared Redis owner-token leader
  lock and PostgreSQL advisory-lock fallback.
- Non-leader instances skip before querying candidate accounts, preventing
  duplicate full-pool scans and account-lock contention.
- Uses Grok-specific defaults of 32 concurrent refreshes and 25 QPS while other
  OAuth providers retain the global defaults of 4 concurrent refreshes and 2 QPS.
- Loads up to 1,000 candidates per cursor page and allows a 3,600-second cycle.
- Adds a deterministic 0-60 minute Grok refresh-window offset per account to
  spread synchronized expirations consistently across restarts and leader
  handoffs.
- Logs the effective high-capacity settings at service startup and cycle
  duration on completion.

Affected files:

- `backend/internal/config/config.go`
- `backend/internal/service/grok_token_refresher.go`
- `backend/internal/service/token_refresh_service.go`
- `backend/internal/service/wire.go`
- `backend/cmd/server/wire_gen.go`
- `deploy/config.example.yaml`
- `GROK_TOKEN_REFRESH.md`

Upgrade notes:

- Preserve the `LeaderLockCache`/DB injection in `ProvideTokenRefreshService`.
- Preserve the Grok-specific QPS/concurrency overrides so scaling Grok does not
  increase refresh traffic for every OAuth provider.
- Re-run Wire generation after changing the provider signature.

## v0.1.162 Merge Decisions

- Adopted official forwarded-client-IP/trusted-proxy hardening, image storage
  settings, admin backup integration, and the remaining release changes.
- Combined official Anthropic `stop_reason: null`, encrypted reasoning
  `signature_delta`, and non-streaming JSON response fixes with the local
  missing-`response.created`, multi-part reasoning, late tool-event, and
  internal web-search lifecycle guards.
- Kept every Grok Chat Completions ingress request on `/v1/responses`, including
  API-key accounts and requests without a cache identity; retained the local
  top-level `reasoning_effort` compatibility path.
- Adopted official Trae fields, service-tier normalization, stricter function
  schemas/tool history checks, converted Responses cache intent, Codex Lite
  `additional_tools` promotion, and unified request-level cache routing.
- Preserved client function declarations and added only non-conflicting native
  `web_search`/`x_search` route markers for known-Free OAuth accounts. This
  retains the local cache-capable Free model routing without rewriting client
  tool semantics.
- Preserved the local Grok token-refresh capacity defaults, deterministic OpenAI
  credential-owner handling, empty-response retry behavior, Kiro balance/redaction,
  public capacity pools, and frontend lifecycle/batch-processing protections.
- Advanced `backend/cmd/server/VERSION` to `0.1.162` because the official tag
  still contains the previous source fallback version.

## v0.1.161 Merge Decisions

- Adopted the official ingress-rejection aggregation, distributed API-key auth
  cache invalidation, protected Grok video proxying, account model mapping,
  encrypted-content recovery, OpenAI WebSocket turn lifecycle, transient 503
  classification, subscription renewal, billing probe, and related migrations/UI.
- Preserved the local dynamic HTTP upstream readiness pool and combined its
  shutdown lifecycle with the new official ops/auth-cache background workers.
- Combined official model-scoped temporary cooldowns with official generic OpenAI
  403 counter/cooldown behavior. Deterministic inactive-workspace credential
  failures still bypass temporary rules and disable the owner immediately.
- Preserved the local Grok Chat Completions-to-Responses route and client
  function names. Restored the `v0.1.160` cache routing behavior: known-Free
  OAuth requests with pure client function tools retain those functions and gain
  non-conflicting native `web_search`/`x_search` route markers. This intentionally
  overrides the official `v0.1.161` change from #4486 because omitting both native
  markers routes agent requests to Grok's non-cacheable Free model.
- Migrated the official Grok video sticky-wait scheduler test to the local
  user/API-key-scoped video request hash, retaining request-owner isolation.
- Advanced `backend/cmd/server/VERSION` to `0.1.161` because the official release
  tag did not update the source fallback version.

## v0.1.160 Merge Decisions

- Adopted the official Grok account scheduler, snapshot cache, sticky escape,
  endpoint selection, CLI `403 Access denied` fallback, and media eligibility
  routing.
- Preserved the local Grok Responses cache route and Claude Code function-tool
  names, including the local conflict policy for `web_search` and `x_search`.
- Removed the local `openai_latency_mode` feature and restored the official
  OpenAI transport, connection-pool, preamble, and instruction behavior.
- Preserved the remaining local Kiro, capacity, billing, moderation, Grok quota,
  frontend batching, and compatibility changes.

## v0.1.156 Merge Decisions

- Kept the official complete-JSON scanner, first-output timeout, error sanitization, and event-boundary flushing fixes in the OpenAI forwarding paths.
- The local low-latency preamble flush retained at this stage was removed by the
  later `v0.1.160` merge.
- Combined official Read-tool argument sanitization with the local closed-block and duplicate-event guards.
- Combined official adaptive Grok 429 backoff and recovery clearing with the local 24-hour Free quota detection and immediate scheduler block.
- Limited mixed function/native cache routing to known-Free, lossless Messages-style bridges (Anthropic Messages and the local Chat tool-history bridge); tool-free Free requests retain the disabled-native-tool cache route.
- Kept local Chat Completions tool/reasoning bridge support and added the official image content bridge.
- Adopted the official content-moderation runtime snapshot and keyword matcher while retaining the local worker/config cache behavior.
- Made the official runtime snapshot expiry test deterministic on Windows by explicitly expiring its fixture timestamp.

## Verification Commands Used

Backend:

```powershell
cd E:\号池sub2api\sub2api\backend
go test ./internal/handler/admin ./internal/handler/dto ./internal/handler ./internal/service ./internal/repository ./internal/server ./cmd/server
go test -tags=unit ./...
go test -tags=unit ./internal/service -run 403 -count=1
```

Frontend:

```powershell
cd E:\号池sub2api\sub2api\frontend
pnpm install --frozen-lockfile
pnpm run typecheck
pnpm test:run
pnpm run build
```

Known warnings:

- `git diff --check` passes after the `v0.1.150` merge.
- `pnpm install` may warn that some dependency build scripts are ignored depending on local pnpm settings.
- `pnpm run build` may emit existing Vite chunk-size/dynamic-import warnings.
- Integration tests still require PostgreSQL and Redis and are not part of the local no-service verification above.

## Suggested Upgrade Workflow

When a newer official version is released:

1. Clone or checkout the new official release in a clean directory.
2. Copy or cherry-pick these local changes by feature, not by blind file overwrite.
3. Start with backend changes:
   - Kiro balance files and `AccountWithConcurrency`
   - credential redaction keys
   - deterministic OpenAI 403 credential-owner handling
   - account test/image compatibility
   - `SetRateLimited` scheduling behavior
   - public group capacity aggregation and route wiring
   - empty-response retry without a persistent cooldown
4. Then apply frontend changes:
   - `KiroBalanceInfo`
   - `Account.kiro_balance`
   - `AccountCapacityCell.vue` Kiro badge
   - `AccountsView.vue` first-load `lite` condition
   - channel capacity card and `channelStatus.capacityPool` locale keys
5. Run the verification commands above.
6. Manually test the admin account list with the capacity column visible.
7. Manually test generic OpenAI 403 responses and confirm:
   - the first two failures create an `OpenAI 403 temporary cooldown` reason
   - the account is temporarily removed from scheduling during cooldown
   - the third failure in the counter window marks the account as error
8. Test `biscuit_baker_service_auth_credential_error_status` and confirm the credential owner is marked as error immediately.

## Recommended Git Preservation

After confirming the current working tree is correct, commit these changes locally so future upgrades can use `git cherry-pick` or `git format-patch`.

Example:

```powershell
cd E:\号池sub2api\sub2api
git add backend frontend LOCAL_CHANGES.md
git commit -m "Apply local Kiro balance and OpenAI credential-owner handling"
```
