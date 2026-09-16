# Local Changes Against Official sub2api main (2026-09-16)

This fork is based on the official `main` commit
`881f3202694c6bc932446931a30c27d9675178b9` (source version `0.2.5`).

- Fork source version: `backend/cmd/server/VERSION` is `0.2.5`.
- Upgrade date: 2026-09-16.
- Upgrade policy: retain the documented fork behavior while adopting official
  fixes, API contracts, cancellation checks, and generated dependency wiring.

## v0.2.5 Upstream Merge Decisions

Official `main` advanced 197 commits from `98d86915b` (v0.2.4) to `881f32026`
(v0.2.5): 447 files, +23038/-1538. Git auto-merged all but three files; the
remaining hunks and the contradicting expectations were adjudicated as follows.

Adopted from official v0.2.5:

- OpenCode Go platform (native protocol dispatch, billing, session handling) and
  its migration `238_opencode_go_platform.sql`.
- `openai_gateway_chat_completions.go`: API-key `/responses` unsupported
  (404/405) fallback to raw Chat Completions, plus upstream endpoint stamping
  used by usage logs and routing hints.
- OpenAI image route rework (native Codex Images, image cache pricing fields),
  Responses Lite namespace tool preservation, `sequence_number` for grok-build,
  apicompat leading-system-message merge, antigravity OAuth token cache
  isolation and Gemini SSE separator fix, subscription bulk actions, monitor
  auto-refresh interval fix, Codex quota window parsing, WS pool queue-waiter
  reselection and the new context-pool capacity math (ctx_pool per-account
  connection factor default 5.0).
- Migrations `237_add_minimax_platform.sql`,
  `238_purge_unlimited_user_platform_quotas.sql`.
- Generated code: `go generate ./ent` and `go generate ./cmd/server` were
  re-run after the merge and produce no diff.

Fork behavior kept through the merge:

- Group burst mode (threshold/latency/429 retry/high-usage) and its sticky
  reserve account: `burstSameAccountRetry`, `WithBurstModeRetryAccount`,
  `burstModeMaxSwitches`, `shouldStopOpenAI429FailoverInMode`, plus migrations
  `221_group_burst_mode.sql` / `222_group_burst_retry_and_high_usage.sql` and
  the renamed `223_group_model_pricing.sql`.
- Public group capacity pool (`group_capacity_service.go`, user monitor card).
- OpenAI HTML-403 policy (transient HTML 403 stays same-account retryable,
  inactive-workspace 403 disables the credential owner) and the
  `transient_html_403` WS dial classification.
- Grok Free prompt-cache routing, the chat-to-Responses bridge (including
  API-key accounts on custom xAI-compatible endpoints), native
  `web_search`/`x_search` route markers, media/video ownership binding.
- Grok API-key pool-mode accounts never persist temporary-unschedulable state.
- Scheduler short-TTL account-reference cache and `[]*Account` request-local
  copies; API-key auth snapshots stay version `25`.
- The WS 二开 removal from commit `3255adfe1` (HTTP ingress stays HTTP/SSE,
  upstream WS only for the WebSocket ingress, pool idle recycle at official 90s).

Conflict adjudication (official fix vs fork behavior):

- `backend/internal/handler/grok_media.go` — union: official bound video-lookup
  ownership (`SelectGrokMediaVideoRequestAccount`), slot acquisition/release
  refactor and `not_found_error` short-circuit, plus the fork's burst-retry
  account threaded through the selection context (`WithBurstModeRetryAccount`).
- `frontend/src/views/user/ChannelStatusV1View.vue` — official
  `autoRefresh.resetCountdown()` (honors the selected interval) plus the fork's
  capacity-pool loading reset.
- `README_CN.md` — both documents kept (fork nginx/SSE note and official
  Codex Fast/Flex policy).
- Grok chat → Responses bridge: official keeps API-key accounts on raw Chat
  Completions and upstream's regression test asserts that. The fork deliberately
  bridges API-key accounts as well, including accounts that target a custom
  xAI-compatible endpoint which serves `/responses` (covered by
  `TestForwardAsChatCompletionsForGrokAPIKeyUsesConfiguredResponsesEndpoint` and
  `TestBuildGrokResponsesRequestAllowsPublicAPIKeyBaseURLByDefault`), so the fork
  behavior is kept. The official inline-image regression
  (`TestForwardGrokRawChatDropsRedundantViewImage`) now forces the raw path with
  a request shape the bridge cannot preserve (`seed`), keeping its coverage of
  the raw Chat Completions image/tool adaptation.
- Grok Free mixed-cache route: the fork deliberately stops rewriting a
  client-declared `web_search`/`x_search` function into a native tool (that
  changes the tool-call protocol and breaks tool-output correlation) and only
  injects the missing companion native marker. The fork contract is kept, and
  the official expectation in `openai_ws_http_bridge_test.go` was aligned to it
  (`tools[1]` stays `function`/`web_search`, `tools[3]` is `x_search`).
- Fork test expectations updated to the current defaults: the Grok model alias
  resolves to `grok-4.6` (`TestForwardIncompatibleGrokChatUsesRawFallback`).

Upgrade verification:

- `backend`: `go build ./...`, `go test -tags=unit ./...`. Remaining failures are
  environment/upstream owned: the `internal/repository` PgDumper tests need a
  POSIX `sh`, and `TestOllamaProbeCallback_StaleLongDoesNotOverrideNewShort`
  fails on pristine official `main` too (verified in a clean v0.2.5 worktree).
- `frontend`: `pnpm install --frozen-lockfile`, `pnpm run typecheck`,
  `pnpm run build` (includes the i18n completeness check).

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

## OpenAI WS 二开 Removal (2026-09-16)

Fork commits `f6b4e8caa` (HTTP ingress multiplexed onto the upstream WS pool) and
`f4e71a62a` (15s idle WS recycle) are removed; official v0.2.4 behaviour is
restored for the OpenAI WS path:

- `resolveOpenAIWSDecisionByClientTransport` again returns
  `openAIWSHTTPDecision("client_protocol_http")` for HTTP ingress, so a
  `/v1/responses` HTTP/SSE request never multiplexes onto the upstream WS pool.
  Upstream WS is used only by the WebSocket ingress.
- `openAIWSConnIdleRecycleAfter` is back to the official `90s`; the fork's 15s
  idle recycle and its keepalive-timeout comment are gone.
- `openai_gateway_forward.go` keeps only its unrelated fork hunk (the OpenAI
  HTML-403 retry policy documented below); the WS comment is official again.

`openai_client_transport.go`, `openai_client_transport_test.go`,
`openai_ws_pool.go`, `openai_ws_protocol_forward_test.go` and
`openai_ws_forwarder_v2_test.go` are byte-identical to official `main`
(`98d86915b`) again. The fork's `transient_html_403` WS dial classification
  stays, because it belongs to the fork's OpenAI 403 policy that also applies on
  the HTTP path.

## Third-Party 防降智 / 防并发 Fork Merge (2026-09-16)

Source: `E:\sub2新站`, imported as snapshot commit
`f8dc19fbdb0e1391b06e2de5539afdcfb3b45dc4` (theirs) and merged into fork commit
`f3217fa0b`. The snapshot labels itself `0.2.4`, but its real base is official
`98d86915b` (v0.2.4, May 2026) plus a large independent feature set, so the
merge is theirs-into-ours: 312 files, +28654/-900. Backup of the pre-merge
tree: branch `backup/pre-thirdparty-anti-degrade-20260916`; evidence:
`E:\号池sub2api\thirdparty-merge-20260916\`.

Adopted from the third-party snapshot:

- Anti-degradation (防降智) account protection: `account_anti_degrade.go`,
  `account_mode1_protection.go`, `account_protection*.go`, mode-1 semantics,
  per-account TLS fingerprint transports (`internal/pkg/tlsfingerprint` builtin
  profiles) and the protection runtime/transition/validation state.
- Anti-concurrency-limit (防并发限制) behaviour: mode-1 effective concurrency
  (`Account.Mode1EffectiveConcurrency()`) drives slot acquisition
  (`tryAcquireAccountSlot`, `AccountWaitPlan.MaxConcurrency`, full-account
  detection) in `openai_account_scheduler.go`, `openai_gateway_scheduling.go`,
  `openai_plugin_transport.go`, `account_test_admission.go` and
  `account_traffic_policy.go`. For accounts without anti-degradation the helper
  returns `Concurrency`, so fork behaviour is unchanged.
- Intelligent test / degrade detection, security policy keywords, support
  tickets, user cleanup guards, spend guard, tiered routing, margin and global
  model pricing, billing export, account traffic policy, legacy API keys and the
  user-hierarchy guard (`middleware.UserHierarchyGuard`, which adds the
  `userService` parameter to `routes.RegisterAdminRoutes`).
- `backend/migrations/239_thirdparty_protection_schema.sql` was reconstructed by
  hand: the snapshot ships the new entities, handlers and queries but its
  `migrations/` directory stops at 134, so the DDL for the `groups`
  security-policy columns, `api_keys.key_hash`/`key_prefix`,
  `security_policy_keywords`, `global_model_pricing`, `test_settings`,
  `account_tests`, `intelligent_test_requests`, `support_tickets`,
  `support_ticket_replies` and the `user_cleanup_*` tables was derived field by
  field from the snapshot's ent schemas and SQL queries.
- `backend/migrations/240_thirdparty_protection_schema_fixup.sql` completes that
  reconstruction: applying 239 on the live server and then parsing every SQL
  statement in the snapshot's repositories (`Prepare` against the migrated schema)
  showed `account_tests.lease_until` was still missing, which made the intelligent
  test worker log `column "lease_until" does not exist` every minute. 240 adds the
  column and its index. 239 is left untouched on purpose: the migration runner
  pins each applied file by SHA256 checksum and refuses to start when an applied
  file changes.

Conflict adjudications (fork choice wins wherever the third party regressed fork
capacity work):

- `openAIWSConnPool.effectiveMaxConnsByAccount`: the snapshot replaced the fork's
  ModeRouterV2 factor expansion with a plain
  `min(Mode1EffectiveConcurrency(), hardCap)`, which collapsed
  `concurrency=1, factor=5` to `1`. The merge keeps *both*: the mode-1
  protection cap still tightens `hardCap`, while the account-type factor
  expansion (`OAuthMaxConnsFactor` / `APIKeyMaxConnsFactor`) and the
  `<= 0 -> 0` rule stay. Third-party
  `TestMode1WSRuntimeConcurrencyCapCannotBeBypassed` and the fork's
  `TestOpenAIWSConnPool_EffectiveMaxConnsByAccount_ModeRouterV2` /
  `TestOpenAIWSConnPool_AcquireRetainedSessionsUsesScaledCapacity` all pass on
  the result.
- REVERTED 2026-09-16 (commit `fd8be6430`): the third-party super-admin hierarchy
  guard — `middleware.UserHierarchyGuard`, `requireSystemSuperAdmin`, the
  `service`/`repository` hierarchy enforcement, the `userService` parameter of
  `routes.RegisterAdminRoutes` and their tests — is removed, so administrator
  permissions behave exactly like official v0.2.5 again. Reason: the guard needs
  `role=super_admin` for writes on `/api/v1/admin/settings`, `/system`,
  `/plugins`, `/backups` and `/data-management`, but the fork frontend derives
  admin access from `role === 'admin'` (`frontend/src/stores/auth.ts`), so an
  account promoted to `super_admin` can log in and still not enter the panel.
  The production administrator is back to `role=admin`; everything else from the
  third-party merge (security policy, intelligent test, tickets, global pricing,
  spend guard, tiered routing, margins, legacy API keys, anti-degradation) stays.
- The official `AdminComplianceGuard` is untouched and still applies: an admin
  without a `settings` row `admin_compliance_acknowledgement:<user_id>` gets 423
  `ADMIN_COMPLIANCE_ACK_REQUIRED` until `POST /api/v1/admin/compliance/accept`.
  The production administrator already had the acknowledgement, so nothing
  changes for that account.
- The WS pool stays official for normal accounts: the extra
  `conversationID`/`transportKey` handshake-compatibility keys and the
  per-account TLS profile applied on dial are gated behind
  `Account.AntiDegradationEnabled()`, so the WS 二开 removal documented above still
  holds for every account without the protection enabled.
- `service/account.go` conflict hunks and the v0.2.5 scheduler/queue-wait
  behaviour (`DisableStickyEscape`, `queueWait`, `rewoken`) come from the fork;
  `api_key_auth_cache` entry version bumped 25 -> 26 for the security-policy
  fields. Generated code was regenerated after the merge (`go generate ./ent`,
  `go generate ./cmd/server`) and produces no diff.
- The snapshot's compiled admin UI (`backend/internal/web/dist`, 195 files) was
  not taken: the snapshot ships no `frontend/` source, so the fork's own frontend
  build is kept and the new admin surfaces are API-only until that source is
  available.

Merge verification (baseline vs merged, build/test/rollback) is recorded in
`E:\号池sub2api\thirdparty-merge-20260916\merge_verification.txt`.

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
go test -tags=unit ./internal/service -run 'TestResolveOpenAIWSDecisionByClientTransport|TestOpenAIGatewayService_Forward_HTTPIngressStaysHTTPWhenWSEnabled'
#   official gate: HTTP ingress must stay HTTP/SSE when account/global WS is on

cd ../frontend
pnpm install --frozen-lockfile
```

Third-party merge verification (2026-09-16):

```bash
cd backend
go build ./...
go test -tags=unit ./...            # same failure set as the pristine f3217fa0b baseline
go test -tags=unit ./internal/service -run 'TestOpenAIWSConnPool_EffectiveMaxConnsByAccount_ModeRouterV2|TestOpenAIWSConnPool_AcquireRetainedSessionsUsesScaledCapacity|TestMode1WSRuntimeConcurrencyCapCannotBeBypassed'
#   conflict adjudication gate: fork factor expansion + third-party mode-1 cap coexist

# migration replay against a real PostgreSQL (scratch database): the helpers used are
# kept in ../thirdparty-merge-20260916/verification_tools/*.go.txt
go run ./cmd/migcheck "host=127.0.0.1 port=5432 user=sub2api password=… dbname=<scratch> sslmode=disable"
pnpm run typecheck
pnpm run build
```

## Degradation Detection + Public Artwork Page (2026-09-16)

Fork-added feature. It is built on the intelligent-test plumbing that the
third-party merge already brought in, and it adds no new tables.

### Scheduling model

- Per-group switch and parameters live on `groups`
  (`degradation_detection_enabled`, `degradation_detection_config`,
  `degradation_preview_enabled`, migration `241_degradation_detection.sql`).
- The probe reuses `test_settings` (`degradation_probe`) and `account_tests`.
  `test_settings.enabled` tracks whether *any* group has the detector on, which
  is what keeps the claim worker from cancelling queued probes.
- The scheduler (`internal/service/degradation_service.go`) ticks every minute,
  sweeps the stalest accounts first, and stops enqueueing once its own pending
  backlog reaches 200, so a large group cannot flood the shared queue (the
  runner drains four tests at a time).

### Verdict -> scheduling, and why manual disable stays manual

- The detector suspends an account through the **official**
  `temp_unschedulable_until` / `temp_unschedulable_reason` window, and records
  the same deadline in `accounts.degradation_suspended_until` with the note
  prefix `降智检测`.
- Only `incorrect` suspends and only `correct` recovers. An undetermined
  verdict changes nothing, because a verbose-but-unparseable answer is not
  evidence of degradation.
- Recovery clears the official window **only while its reason still carries our
  prefix**, then always clears our own marker. An unrelated pause (manual
  disable, overload, rate limit) survives verbatim. Accounts with
  `schedulable = false` are excluded from the scheduler entirely, which is what
  makes "手动停用不参与探测也不自动启用" hold by construction.
- Install order matters: `wire_gen.go` constructs the detector before
  `ProvideIntelligentTestService` so the outcome hook is registered before the
  worker goroutines start.

### Probing during suspension

`accountTestCooldown` treats `temp_unschedulable_until` as a cooldown, which
would otherwise defer the detector's own re-check. Degradation test types carry
a context flag that skips **only** that entry; rate-limit and overload cooldowns
still apply. Covered by `TestAccountTestCooldownSkipsOnlyDetectorSuspension`.

### Reasoning effort

`IntelligentTestConfig` gained `reasoning_effort`. `applyIntelligentPayloadPrompt`
(the single hook every protocol adapter calls) now also pins the effort onto
whichever request shape was built: `reasoning.effort` for Responses,
`reasoning_effort` for Chat Completions, `output_config.effort` for Anthropic.

### Public page

- `GET /api/v1/jiangzhijiance` (page + interval + model/effort metadata) and
  `GET /api/v1/jiangzhijiance/records/:id/image` (sanitized SVG, CSP sandbox).
  No JWT: this is the requested public page, so it is deliberately *not* behind
  `BackendModeUserGuard` (backend mode is enabled on the account-pool instance).
- Every SVG is re-encoded through `PrepareIntelligentSVGPreview`, and the page
  loads it through `<img>`, so stored markup is never executed.
- Frontend: `/jiangzhijiance/` (`views/public/DegradationDetectionView.vue`,
  added to `BACKEND_MODE_ALLOWED_PATHS`) and a self-saving card in the group
  editor (`views/admin/DegradationDetectionCard.vue`).

### Verification

```bash
cd backend
go build ./...
go test -tags=unit -count=1 ./internal/service/ ./internal/handler/admin/ ./internal/server/routes/
#   failure set must equal the pristine baseline:
#   TestGroupHandlerSimpleModeSanitizesCommercialFields and
#   TestOllamaProbeCallback_StaleLongDoesNotOverrideNewShort
go test -tags=unit -count=1 -run 'Degradation|IntelligentPayload|AccountTestCooldown|HandleIntelligentTestOutcome|RunNowSkips|SafePublicSVG' ./internal/service/

cd ../frontend
pnpm run build
```

The migration and the detector SQL were replayed against a throwaway database
cloned from the production schema (`../jiangzhi-20260916/sandbox_validate.sh`).
---

## 2026-09-16 — 公开页时间轴、作品管理与「标准答案」真正生效（`eaf1dcf2b`）

线上验收暴露了三个只能在真机复现的问题，都已修在这个提交里，并在美东独服上
用 `jiangzhi-20260916/deploy4.sh` 验证。

### 1. 标准答案字段此前不影响判定（真 bug）

`degradationRepository.EnqueueDegradationTest` 把 `ExpectedAnswer` 写死成
`DegradationExpectedAnswer`（"21"），而暂停原因文本却读分组配置里的
`expected_answer`。后果：把配置改成 29 后，探测仍然按 21 判分，于是
`temp_unschedulable_reason` 出现自相矛盾的 `降智检测：答案 29（应为 29）`；
也就是说 UI 上那个输入框根本管不到判分。

修法：
- `EnqueueDegradationTest` 增加 `expectedAnswer` 参数，分组配置里的值随队列行
  写入 `config_snapshot`，runner 按快照判分；
- 暂停原因改为引用**本次判分用的**那个值（`evaluation->>'expected_answer'`，
  回退到快照、再回退到当前分组配置），保证「应为 X」永不和判定矛盾；
- 只有 artwork 任务仍然不带答案（它按 svg_structure 评分）。

回归测试：`TestEnqueuedTestsCarryTheGroupConfiguration`（配置 29 → 入队 29、
artwork 不带答案）、`TestSuspendNoteQuotesTheGradedExpectation`（快照 29 而当前
配置 21 时，文本必须写 29）。

> 顺带记一个事实：`gpt-6-astra` 对这道糖果题稳定答 **29**，而 29 才是正确答案
> （最多可取出 28 颗仍不满足条件：只取圆苹果 7 + 圆桃子 9 + 两种西瓜 12，
> 故 29 才保证成立）。因此线上把分组 42 的 `expected_answer` 设为 29，否则
> 会把健康账号全部判成降智。

### 2. 公开页时间轴 + 缩略图

- 新增 `GET /api/v1/jiangzhijiance/timeline?hours=24|72|168`：按桶聚合探测结论，
  固定 24 个点（24h→60min/桶，3d→180min，7d→420min），只返回计数与
  `current_state`，不含账号标识。`current_state` 取**最近一次**判定（新出现的
  红点必须立刻可见），另附 `suspended_accounts`。
- 列表接口不再内联 SVG：`DegradationPublicWork` 增加 `has_image`，原图仍走
  `/records/:id/image`，一次页面加载从「6 张图 × 数 KB」降到几十字节。
- 前端：`views/public/DegradationTimeline.vue`（inline SVG 柱状图 + 24h/3d/7d
  切换 + 图例），`DegradationDetectionView.vue` 改为 3/4/5 列小图 + 点击灯箱看大图。

### 3. 公开页可由运营管理（删除测试图）

- `GET /api/v1/admin/degradation-detection/works`（分页列出现有作品）、
  `DELETE /api/v1/admin/degradation-detection/works/:id`、
  `POST /api/v1/admin/degradation-detection/works/purge`。
- 删除语句只匹配 `test_type='degradation_preview'` 的行，时间轴背后的探测记录
  无法通过这几个接口被删除（验证时 `probe_history` 计数保持不变）。
- 分组编辑卡片里新增「公开页作品管理」：缩略图列表 + 单删 + 清空全部。

### 验证

```bash
# 仓库侧
gofmt -l internal/service internal/repository      # 无输出
go vet ./internal/service/ ./internal/repository/ ./internal/handler/... ./internal/server/routes/
go test -tags=unit -count=1 -run 'Degradation|PublicWorkCuration|EnqueuedTestsCarry|SuspendNoteQuotes' ./internal/service/
cd ../frontend && pnpm run typecheck && pnpm run build

# 线上侧（美东独服）
bash /root/scp/deploy4.sh
bash ROLLBACK.sh status
```

线上实测（`/root/scp/deploy4.log`）：`expected_answer_saved=29` →
`probe id=8 verdict=correct expected=29 answer=29` → `detector_marker=cleared
official_pause=lifted`；时间轴 `buckets=24 current_state=healthy`；作品单删后
`image_after_delete=404`、`probe_history_untouched=7`；`claim_errors=0`。
