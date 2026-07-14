# Local Changes Against Official sub2api v0.1.153

This repository is based on official `Wei-Shaw/sub2api` release `v0.1.153`.

- Base commit: `a2bc1337` (`v0.1.153`)
- Working branch: `merge-user-changes-v0.1.153`
- Original local change bundle: `E:\号池sub2api\改动`
- Original local commits: `f1a65550`, `66701c1c`, `46781d3a`
- Last upstream merge: 2026-07-14 (`v0.1.151` -> `v0.1.153`)
- Fork build version: `backend/cmd/server/VERSION` is pinned to `0.1.153` because the upstream `v0.1.153` tag still contains `0.1.152` and the merge commit is not an exact release tag.
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

### 5. OpenAI 403 Retry Behavior

OpenAI 403 is customized to keep retrying without disabling the account unless the response identifies a permanent credential-owner failure.

Behavior before local change:

- OpenAI 403 used an internal consecutive counter.
- Early hits wrote `OpenAI 403 temporary cooldown (1/3): ...` to `temp_unschedulable_reason`.
- Threshold hit disabled the account with `consecutive_403=3/3`.

Local behavior:

- No `OpenAI 403 temporary cooldown (...)` reason is written.
- No temporary unschedulable state is set for OpenAI 403.
- Generic OpenAI 403 responses do not set account error/disabled state, even after repeated failures.
- `biscuit_baker_service_auth_credential_error_status` is treated as permanent: the credential owner is marked as error when the personal access token owner is no longer an active member of the selected workspace.
- The permanent credential error takes precedence over user-configured temporary-unschedulable rules.
- The function still returns the failover/retry signal, so existing upstream retry/failover logic can continue trying another attempt/account.
- Log event is now `openai_403_retry_without_account_disable`.

Affected files:

- `backend/internal/service/account_test_service.go`
- `backend/internal/service/account_test_service_openai_403_test.go`
- `backend/internal/service/ratelimit_service.go`
- `backend/internal/service/ratelimit_service_403_test.go`

Important note:

- This is not an infinite loop inside a single HTTP request. Existing gateway retry/failover limits still bound each request.
- The "infinite retry" behavior applies to generic OpenAI 403 responses; deterministic credential-owner failures are disabled immediately.

Upgrade notes:

- In `handleOpenAI403`, preserve the permanent credential-owner exception before the generic retry path.
- For generic OpenAI 403 responses, keep building the 403 message for logging, but do not:
  - increment/check the OpenAI 403 counter
  - call `SetTempUnschedulable`
  - call `handleAuthError`
  - append `consecutive_403=...`
- Keep returning `true` from `handleOpenAI403` so callers still enter failover/retry handling.

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
   - OpenAI 403 behavior
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
7. Manually test a generic OpenAI 403 response and confirm:
   - no `OpenAI 403 temporary cooldown` reason appears
   - account is not marked temporarily unschedulable
   - account is not disabled after repeated OpenAI 403 responses
8. Test `biscuit_baker_service_auth_credential_error_status` and confirm the credential owner is marked as error immediately.

## Recommended Git Preservation

After confirming the current working tree is correct, commit these changes locally so future upgrades can use `git cherry-pick` or `git format-patch`.

Example:

```powershell
cd E:\号池sub2api\sub2api
git add backend frontend LOCAL_CHANGES.md
git commit -m "Apply local Kiro balance and OpenAI 403 retry customizations"
```
