# Local Changes Against Official sub2api main (2026-09-10)

This fork is based on the official `main` commit
`98d86915becae9fe9491a91ffc6defd5235c8d2b` (source version `0.2.4`).

- Fork source version: `backend/cmd/server/VERSION` is `0.2.4`.
- Upgrade date: 2026-09-10.
- Upgrade policy: retain the documented fork behavior while adopting official
  fixes, API contracts, cancellation checks, and generated dependency wiring.

## v0.2.4 Upstream Merge Decisions

- Official MiniMax, plugin artifacts, Codex manifest pinned accounts, group
  model allowlist, force/free OpenAI Fast, per-model time pricing, and Grok 4.6
  defaults are retained.
- `groups.models_list_config` is superseded by official `model_allowlist`
  (migration 235/236). The JSON shape is unchanged, but the allowlist now also
  gates request admission, which is strictly stronger than the fork-only list
  filter.
- Burst-mode group fields stay on local migrations `221_group_burst_mode.sql`
  and `222_group_burst_retry_and_high_usage.sql`. Official later migrations keep
  their original filenames; git rename detection mapped official model-pricing
  `221` onto the existing local `223_group_model_pricing.sql`.
- Same-account retries use official delay/deadline/`RetryableOnSameAccount`
  machinery, wrapped by `burstSameAccountRetry` so burst 429 still uses the
  group retry limit and sticky reserve account.
- Official `newOpenAIAccountFailoverError(shouldDisable, retryable)` signature
  is kept. Transient OpenAI HTML 403 remains same-account retryable.
- Grok Chat-to-Responses still converts every losslessly compatible request,
  including API-key accounts and requests without a cache identity. Official
  narrowed that path to OAuth plus cache identity; the fork keeps the broader
  bridge. Requests with `stop`, `developer`, `metadata`, unknown fields, or
  other non-preservable shapes still fall back to raw Chat Completions.
- Composer image-input rewriting stays on the raw Chat path when `metadata`
  (or any other unknown field) makes the Responses bridge ineligible, matching
  official metadata preservation.
- Scheduler short-TTL account-reference cache, public capacity pool, inactive
  workspace 403 classification, and empty-stream retry without temp-unschedule
  remain.
- API-key auth snapshots are version `25` so v22 fork snapshots and v24
  official snapshots are both evicted.

## Public Group Capacity Pool

The user channel-status page exposes a shared capacity view for public standard
groups through `GET /api/v1/channel-monitors/capacity-pool`.

Primary files:

- `backend/internal/service/group_capacity_service.go`
- `backend/internal/handler/channel_monitor_user_handler.go`
- `frontend/src/components/user/monitor/ChannelCapacityPoolCard.vue`
- `frontend/src/views/user/ChannelStatusV1View.vue`

## OpenAI 403 Classification

1. Inactive-workspace credential-owner errors mark the owner as error first.
2. HTML 403 does not increment the persistent 403 counter and stays retryable
   on the same account.
3. Other OpenAI 403 responses keep official cooldown/error counting.

## Large OpenAI-Compatible Pool Scheduling Mitigations

The short-TTL shared account-reference cache remains the OpenAI hot path.
`selectBestAccount` continues to take `[]*Account` request-local copies.

## Grok Free Prompt Cache Routing

Losslessly compatible Grok Chat Completions requests are converted to xAI
Responses, including API-key accounts and requests without a cache identity.
Known-Free OAuth requests with client function tools keep native
`web_search`/`x_search` route markers from commit `649048eac`.

## Group Burst Mode

- `burst_mode_enabled`
- `burst_mode_threshold_percent` (default 90)
- `burst_mode_429_retry_count` (default 10)
- `burst_mode_high_usage_enabled` (OpenAI groups only)

Primary files:

- `backend/migrations/221_group_burst_mode.sql`
- `backend/migrations/222_group_burst_retry_and_high_usage.sql`
- `backend/internal/service/burst_mode.go`
- `backend/internal/handler/burst_mode.go`
- `frontend/src/views/admin/GroupsView.vue`

## Upgrade Verification

```bash
cd backend
go test -tags=unit ./internal/handler
go test -tags=unit ./internal/service

cd ../frontend
pnpm install --frozen-lockfile
pnpm run typecheck
pnpm run build
```
