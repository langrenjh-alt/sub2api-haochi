# Local Changes Against Official sub2api v0.1.165

This fork is based on official release tag `v0.1.165` at commit
`e9a58c1cb8b5ef626a75c93b4d953fde5e67aa29`.

- Fork source version: `backend/cmd/server/VERSION` is `0.1.165`.
- Upgrade date: 2026-07-26.
- Upgrade policy: keep the official tree intact and retain only the documented
  capacity-pool and OpenAI 403 behavior below.

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
- `frontend/src/views/user/ChannelStatusView.vue`

## OpenAI 403 Classification

OpenAI 403 responses are classified before account state is mutated:

1. `biscuit_baker_service_auth_credential_error_status`, or the equivalent
   inactive-workspace-member message, immediately marks the credential owner as
   error. This takes precedence over broad custom temporary-unschedulable rules.
2. OpenAI's branded transient HTML 403 shell does not increment the persistent
   403 counter and does not write a new error or temporary-unschedulable state.
   HTTP and WebSocket paths classify it as retryable on the same account before
   normal account failover.
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

Primary files:

- `backend/internal/repository/account_repo.go`
- `backend/internal/service/concurrency_service.go`
- `backend/internal/service/openai_gateway_scheduling.go`
- `backend/internal/service/scheduler_snapshot_service.go`

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
