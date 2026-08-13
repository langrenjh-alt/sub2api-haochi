# Local Changes Against Official sub2api main (2026-08-13)

This fork is based on the official `main` commit
`fbfdcef8184ae4b2e224d5cfc47cf1d0e3742710` (source version `0.1.176`).

- Fork source version: `backend/cmd/server/VERSION` is `0.1.176`.
- Upgrade date: 2026-08-13.
- Upgrade policy: retain the documented fork behavior while adopting official
  fixes, API contracts, cancellation checks, and generated dependency wiring.

## v0.1.176 Upstream Merge Decisions

- Official Grok 4.6 model routing, JWT subscription-tier detection, long-context
  handling, media fallbacks, native `/x_search`, and search billing are retained.
- The fork's Grok Chat-to-Responses bridge remains preferred for every request
  that can be converted without changing Chat Completions semantics, including
  API-key accounts and requests without a cache identity. Requests using `stop`,
  `developer`, `input_audio`, legacy `functions`, named `tool_choice`, or unknown
  fields fall back to raw Chat Completions so no client field is silently lost.
  The bridge retains composer image-input conversion, reasoning-effort
  preservation, original stream accounting, and Free Cache native-tool markers.
- Official per-model group pricing and long-context controls are merged with the
  fork's burst-mode fields. Ent generated code was regenerated from the combined
  schema so both field families remain available through admin APIs and UI.
- The official model-pricing migration originally numbered `221` was renamed to
  `223_group_model_pricing.sql`; local `221_group_burst_mode.sql` and
  `222_group_burst_retry_and_high_usage.sql` remain unchanged and ordered.
- Legacy groups with an unset burst threshold are normalized to the default before
  validation, preserving update compatibility for databases created before burst
  fields existed.
- Group duplication copies the official long-context and per-model pricing
  settings as well as the fork's burst-mode settings, with pricing structures
  deep-copied so edits to the duplicate cannot mutate the source group.
- API-key authentication snapshots include long-context and per-model pricing
  fields. Their version is `22`, which evicts older snapshots that cannot
  reconstruct the complete billing policy.

## v0.1.175 Merge Decisions

- Channel Monitor v2 and its V1/V2 mode switch come from official. The fork's
  capacity-pool card moved into `ChannelStatusV1View.vue`, so it remains visible
  only when the legacy monitor mode and its matching API are active.
- Official generic HTML 403 protection is retained. The fork's deterministic
  inactive-workspace credential-owner classification still runs first, and its
  branded transient HTML path remains retryable on the same account.
- Official scheduler cancellation checks and filter diagnostics are retained.
  The fork's short-TTL shared account-reference cache remains the OpenAI hot
  path; Grok's official quota gates operate on one request-local value copy.
- Official Grok CLI identity headers are retained. The fork converts losslessly
  compatible Grok Chat Completions ingress to Responses and accepts both JSON and
  SSE from that internally streaming upstream route; incompatible request shapes
  retain raw Chat semantics.
- Official request-scoped transient protection is retained for capacity-shed
  failover. The fork's empty-response policy still keeps unmarked 502 failures
  schedulable after same-account retries, while unmarked 400 configuration
  failures retain their cooldown behavior.

## Public Group Capacity Pool

The user channel-status page exposes a shared capacity view for public standard
groups through `GET /api/v1/channel-monitors/capacity-pool`.

- Capacity is evaluated independently per public group.
- Account status, concurrency, session/RPM limits, and 5h/7d windows are
  aggregated without allowing one unhealthy group to hide another group.
- Only normal, schedulable accounts contribute to available-account and
  available-concurrency totals.
- Both SQL-backed batch loading and the repository fallback used by tests are
  retained.

Primary files:

- `backend/internal/service/group_capacity_service.go`
- `backend/internal/repository/account_repo.go`
- `backend/internal/handler/channel_monitor_user_handler.go`
- `backend/internal/server/routes/user.go`
- `frontend/src/api/channelMonitor.ts`
- `frontend/src/components/user/monitor/ChannelCapacityPoolCard.vue`
- `frontend/src/views/user/ChannelStatusV1View.vue`
- `frontend/src/views/user/__tests__/ChannelStatusV1View.capacity.spec.ts`

## OpenAI 403 Classification

OpenAI 403 responses are classified before account state is mutated:

1. `biscuit_baker_service_auth_credential_error_status`, or the equivalent
   inactive-workspace-member message, immediately marks the credential owner as
   error. This takes precedence over broad custom temporary-unschedulable rules.
2. HTML 403 responses do not increment the persistent 403 counter and do not
   write a new error or temporary-unschedulable state. The fork's branded
   transient HTML classifier additionally marks the response retryable on the
   same account before normal account failover.
3. Other OpenAI 403 responses keep official behavior: the first two failures
   apply a 10-minute temporary cooldown, the third failure in the 180-minute
   counter window marks the account as error, and counter-backend failures fail
   closed by marking the account as error.

Account connection tests apply the same deterministic credential-owner rule,
while generic test-time 403 responses do not mutate account state.

Primary files:

- `backend/internal/service/ratelimit_service.go`
- `backend/internal/service/account_test_service.go`
- `backend/internal/service/openai_account_runtime_block_fastpath.go`
- `backend/internal/service/openai_gateway_forward.go`
- `backend/internal/service/openai_gateway_passthrough.go`
- `backend/internal/service/openai_ws_forwarder_support.go`
- `backend/internal/service/ratelimit_service_403_test.go`
- `backend/internal/service/account_test_service_openai_403_test.go`

## Large OpenAI-Compatible Pool Scheduling Mitigations

Large Grok and other OpenAI-compatible account pools avoid repeating the same
high-cost scheduler work for every overlapping request:

- observational `grok_usage_snapshot` updates do not enqueue scheduler rebuilds;
- same-bucket snapshot reads share one bounded load and reuse decoded account
  references for the configured short load-batch cache interval;
- overlapping fresh load queries for the same candidate set are coalesced; and
- Grok quota checks read canonical JSONB maps directly instead of performing a
  JSON marshal/unmarshal round trip for every candidate.

These changes reduce duplicate work but do not replace the scheduler's full-pool
load query. Very large pools still require a bounded candidate/index design for
strictly sublinear selection cost.

The scheduler now reuses the short-TTL account-reference cache in both the
advanced OpenAI load-balancer path and the legacy Anthropic/Gemini/Antigravity
gateway path. The legacy `[]Account` API still receives value copies, while the
advanced path uses shallow, request-local account copies and rechecks the final
selected account against the database. This prevents concurrent requests from
repeatedly decoding the same Redis snapshot without weakening freshness checks.

The per-request `sticky.scheduler_entry` diagnostic is emitted at debug level;
it is intentionally excluded from the default info log path because it runs for
every gateway request.

Primary files:

- `backend/internal/repository/account_repo.go`
- `backend/internal/service/concurrency_service.go`
- `backend/internal/service/openai_gateway_scheduling.go`
- `backend/internal/service/scheduler_snapshot_service.go`

## Grok Free Prompt Cache Routing

Losslessly compatible Grok Chat Completions requests are converted to xAI
Responses, including API-key accounts, mapped models, and requests without a
cache identity. The bridge always streams from xAI internally, but the
forwarding result and usage log retain the client's original `stream` value so
dashboard request types remain accurate. Request shapes the bridge cannot
preserve use raw Chat Completions instead.

Known-Free Grok OAuth requests with client function tools preserve the client
function declarations and gain non-conflicting native `web_search`/`x_search`
route markers. Pure client function tools use this mixed route by default.

This intentionally retains commit `649048eac` over the official behavior that
omits native markers for pure client tools. Without a native marker, xAI routes
the request to its non-cacheable Free model. A client function named
`web_search` or `x_search` is not rewritten because doing so changes the tool
protocol; only the non-conflicting native marker is appended.

Primary files:

- `backend/internal/service/openai_gateway_grok_cache.go`
- `backend/internal/service/openai_gateway_grok_cache_test.go`
- `backend/internal/service/openai_gateway_grok_cache_tool_test.go`
- `backend/internal/service/openai_gateway_chat_completions.go`
- `backend/internal/service/openai_gateway_grok_chat_bridge.go`
- `backend/internal/service/openai_gateway_grok_chat_bridge_test.go`

## Upgrade Verification

Before upgrading again, verify:

```bash
cd backend
go test -tags=unit ./internal/service
go test ./internal/service

cd ../frontend
pnpm install --frozen-lockfile
pnpm run typecheck
pnpm run build
```

For release binaries, build the frontend first so
`backend/internal/web/dist/` is populated, then build the backend with the
`embed` tag.
