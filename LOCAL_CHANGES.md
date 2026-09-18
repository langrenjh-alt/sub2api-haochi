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

---

## 2026-09-16 追加 — 运营拍板：标准答案 = 21（线上实测结论）

周总确认题面标准答案就是 **21**，因此分组 42 的 `expected_answer` 已由 29 改回 21
（分组 43 本来就是 21）。改完立刻跑了一次探测：

```
probe id=16 verdict=incorrect expected=21 answer=29 ms=6156
acct=123019 detector_until=2026-09-16 17:58:14+08
note='降智检测：答案 29（应为 21），暂停调度 30 分钟'
```

注意这里判分与文案已经完全一致（`expected=21` ↔ 「应为 21」），也就是本次提交修的
那个 bug 正常工作了：**配置什么就按什么判**。

### 21 是否可达——四个模型的对照结果

用一个**非降智类型**的一次性测试类型（`candy21`，同样的题面、expected=21、跑完即删）
在同一账号 123019 上各跑一次，避免影响调度钩子：

| 模型 | 答案 | 判定 |
|---|---|---|
| gpt-6-astra | 29 | incorrect |
| gpt-5.6-sol | 29 | incorrect |
| gpt-5.6 | 29 | incorrect |
| gpt-5.5 | 36 | incorrect |

结论：**这批模型没有一个答 21**；三个答 29（也正是本题的数学答案：最多能取出 28 颗
仍不满足条件——只取圆苹果 7 + 圆桃子 9 + 圆西瓜 8 + 五角星西瓜 4，所以 29 才保证成立），
gpt-5.5 答 36（安全但非最少）。

### 运营含义（按当前配置）

`expected_answer=21` + 检测开启时，任何走到这个检测的账号都会在每次探测后被判降智并
暂停 30 分钟，而 10 分钟的探测节奏会不断续期，因此该账号会长期留在暂停态、不会被自动
恢复。要保留「21 才算正常」的判据又不希望全线暂停，三个可选动作：

1. 关掉该分组的降智检测开关（`enabled=false`），先只保留公开页；
2. 把探测模型换成确实会答 21 的模型（当前池内尚未找到）；
3. 把 `expected_answer` 改为 29，恢复「答对即放行」的行为（`bash ROLLBACK.sh config`
   的反向操作：面板里改回 29 即可）。

### 更正（同日 17:40，实测）

上一条里「29 才是正确答案、21 会把健康账号全部判成降智」的判断只对**纯数学**成立，对**这个池子的运营事实**不成立，按实测更正：

- 分组 43「不降智」的 7 个账号（`team-*@gmail.com-ws-*`）在 17:30 之后共 15 次探测，**全部回答 21 → verdict=correct**，没有任何一个被暂停；
- 分组 42「微软测试」的 `123019`（微软e3）回答 **29**，被判 `incorrect` 并暂停 30 分钟，之后每 10 分钟的探测会续期；
- 在同一账号上做的模型对照（一次性测试类型 `candy21`，非降智类型）：`gpt-6-astra` / `gpt-5.6-sol` / `gpt-5.6` → 29，`gpt-5.5` → 36。

也就是说：21 是这个池子的**主流答案**，21 作为判据能把 `123019` 从其余 7 个账号里区分出来——
作为「防降智」的判据它是有效的、可复现的。至于这道题本身的最优保证值，
用穷举可以确认是 29（最多 28 颗仍可不满足条件：圆苹果 7 + 圆桃子 9 + 圆西瓜 8 +
五角星西瓜 4），但那是数学题面的事，与「本池账号是否表现异常」是两件事。

因此线上保持 **`expected_answer=21`**（分组 42 / 43 均已确认），判据=池内主流答案，
偏离者即被暂停并持续复检；`123019` 目前处于该策略下的暂停态。

---

## 2026-09-16 追加 — 作品一直生成失败的根因：原始日志 1 MiB 上限（不是超时）

现象：分组 43（`不降智`，7 个 Codex 账号）开启「降智检测 + HTML/SVG 生成」之后，
`degradation_preview` 记录清一色 `failed`，公开页 `total=0`。

排查（`/root/scp/pelican_experiment.sh`：把同样的鹈鹕提示词塞进一次性测试类型
`pelican600`，timeout=600s，跑完自动删除，不影响调度）：

```
id=47 acct=123036 status=failed ms=278372 raw_truncated=t raw_len=1048003 svg=0
        err=upstream did not return a complete, nonempty test result
```

278 秒就结束了，远没到 300 秒的旧超时 —— **不是超时**。真正的原因在
`internal/service/intelligent_test_runner.go` 的原始日志采集上限：

- `intelligentCapture.Write` 写满 `1<<20` 就置 `truncated`；
- 该标志直接进了失败判据（`... || recorder.truncated || capture.truncated`），
  同时 `intelligentRawComplete(capture.body)` 因为日志被腰斩而看不到 `response.completed`
  → `complete=false` → 整单判 failed；
- 而 `result` 里其实已经是完整答案 + 完整 `<svg>…</svg>`（id=44 的 result 22,108 字符，
  `</svg>` 落在第 22,099 字符）。

线上数据把这根链条钉死：失败的 26/44/48/54 全部 `raw_truncated=t`、`raw_len≈1_048_0xx`
（正好是 1 MiB 上限），而修复后的 62 号记录 `raw_truncated=f`、`raw_len=1,912,421`
（1.82 MiB）—— 作品流本来就有将近 2 MiB，旧上限必然腰斩。

修复（提交 `7eaa712e9`）：

1. 上限放宽：原始日志 1 MiB → 8 MiB、客户端流 2 MiB → 16 MiB、上游 body 读取 4 MiB → 16 MiB；
2. 判据与日志解耦：抽出 `intelligentObservationFailed(...)`，`capture.truncated` 只写进
   `raw_truncated`（日志保真度标记），不再让整单失败；`intelligentRawComplete` 只在日志
   **没有被截断**时才有否决权；
3. 测试：`TestIntelligentCaptureBounded` 改为按常量取上限，新增
   `TestIntelligentClippedRawLogStillCompletes`（6 条判据）；
4. 线上配置：分组 42/43 的 `timeout_seconds` 300 → 600（作品实测 178–300s，留余量）。

部署（`/root/scp/deploy6.sh`，退出码 0，末行 `DEPLOY6 OK`）：

```
sha    cb9dfa6f… -> 8f986156…
版本   0.2.5-fork-artwork (commit 7eaa712e9)
id=62 acct=123038 status=completed ms=218010 raw_truncated=false
       raw_len=1912421 result_len=18050 img_len=20099
公开页 /jiangzhijiance/ = 200；total=1；records/62/image = 200（20,432 bytes）
探测   123032/123036/123037/123038 answer=21 verdict=correct
```

回退：`bash ROLLBACK.sh {repo|server|config}`（repo → b39345569、server → cb9dfa6f…、
config → timeout 300）；已在 git worktree 副本上验证回退点（旧代码确实同时具备
`1<<20` 上限和 `capture.truncated` 判失败）。

### 顺带排除「gpt-6-astra 是不是降智的」

同一批分组 43 账号对糖果题（短回答）每次都答 21 且判 correct，只有长输出的作品任务失败；
差异只在响应体积，不在账号可用性 —— 既不是账号降智，也不是模型不可用，而是服务端
自己的采集上限。

---

## 2026-09-16 追加 — 删除第三方二开的「全站账号健康分服务」

周总问：`health:auto err_rate=94.0%` 这个「全站账号健康分服务」是官方功能吗？不是就删掉。

**结论：不是官方功能，已删除并上线（提交 `4792d9589`）。**

出处判定：

```
$ git cat-file -e f3217fa0b:backend/internal/service/account_health.go
fatal: path ... exists on disk, but not in 'f3217fa0b'        # 官方 v0.2.5 基线没有
$ git log --oneline --diff-filter=A -- backend/internal/service/account_health.go
f8dc19fbd thirdparty snapshot: E:\sub2新站 ...                 # 来自第三方二开快照
```

它做了什么：每 60 秒扫描一次，`ok = usage_logs`、`err = ops_error_logs(status>=400 且非
is_business_limited)`，10 分钟窗口内样本 ≥10 且 `err/total >= 50%` 就写
`temp_unschedulable` 30 分钟，reason = `health:auto err_rate=X%`（也就是账号页里那个
「临时不可调度状态 / health:auto err_rate=94.0%」）。恢复阈值 `<= 20%`。

删除内容：服务本体、handler、`/api/v1/admin/account-health*` 路由、wire/wire_gen 装配、
`Handlers.AccountHealth` 字段；被删测试桩改为 margin/spend-guard/tiered-routing 共用
`stubSharedSettingRepo`。**官方与「账号健康」有关的两处保留未动**：
`ObserveOpenAIAccountHealthFailure`（OpenAI 调度）与运维邮件里的「账号健康报告」
（`GET /api/v1/admin/ops/email-notification/config` 部署后仍 200）。

线上（美东独服 `deploy7.sh`，exit 0）：

```
sha     8f986156… -> c85f5f41…
version 0.2.5-fork-nohealth (commit 4792d9589)   health=200 after 3s
GET /api/v1/admin/account-health           -> 404   （已删除，不是 401/403）
GET /api/v1/admin/accounts                 -> 200
GET /api/v1/admin/ops/email-notification/config -> 200
UPDATE accounts ... WHERE reason LIKE 'health:%'   -> UPDATE 22（历史残留清零，剩余 0）
```

部署后 5 分钟观察（cpagrok 窗口 1547–1661 ok / 109–123 err）：`health_isolated_now=0` 全程、
`any_health_reason_left=0`、`cpagrok_isolated=false`、账号恢复成功流量。
同一时刻按旧规则本应被隔离的账号（`id=123119 ok=0 err=16 err_pct=100%`）**没有再被隔离**。

回退：`bash ROLLBACK.sh repo`（回到 `cad12be73`）、`bash ROLLBACK.sh server`
（装回 `sub2api.bak-20260916-192630-v0.2.5-fork-artwork` = `8f986156…`）。

## 2026-09-18 — 292 状态注入 / Codex 状态池（新增功能，非上游）

新增一套 fork 专有功能：把上游下发的 Codex 本轮状态令牌（文章所称 `current_turn_state`）
按账号池化并注入到 `chatgpt.com` 的 Codex 请求上。管理页 `/admin/codex-turn-state`，
详细设计、实测证据与运维步骤见 [docs/CODEX_TURN_STATE.md](docs/CODEX_TURN_STATE.md)。

需要跨升级保留的点：

- **上游形态与文章不同，实现按实测**：真实 upstream 在 **HTTP 200 的响应头
  `x-codex-turn-state`** 上返回状态（文章描述的是 292 + 响应体字段）。实现同时支持
  响应头与 292 响应体的 `current_turn_state`/`turn_state` 字段；312 撤销状态码是配置项
  （默认 `312`），线上尚未观测到该信号。
- **状态是账号级的**，因此注入只作用于令牌所属账号，绝不跨账号复用。
  实测（同父账号下两个子账号互相换用状态）：上游**拒绝并补发新状态**，与发送损坏状态表现一致；
  文章本身也写明 state is account-bound。故「所有账号共用一个状态头」不可行。
- `issuance_statuses` 是严格模式开关：留空接受任意携带状态的成功响应（本环境实测就是 200 头），
  填 `292` 则只信任 292 下发的状态、200 的状态会被完全忽略（不落库不注入）。
- **注入点收口在 `doOpenAIUpstream`**（`openai_plugin_transport.go`），它是所有 OpenAI
  路径的唯一漏斗；`injectCodexTurnState`/`observeCodexTurnState` 只对 `chatgpt.com` 生效，
  `api.openai.com` 不受影响。池未安装时两个钩子都是空操作。
- **网关新增字段** `OpenAIGatewayService.codexTurnState`（`CodexTurnStateGateway` 接口），
  经 `SetCodexTurnState` 安装；不引入结构体构造依赖，故 wire 图不变。
- **`cmd/server/wire_gen.go` 手工装配段**：`ProvideCodexTurnStateService` /
  `Start()` / `adminHandlers.CodexTurnState` 三处与降智检测的手工段同理，
  **重跑 `go generate ./cmd/server` 会整块删除，必须补回**（否则管理页 handler 为 nil）。
- **采集复用账号测试传输链路**（`AccountTestService.DoOpenAIProbe`）并补齐 Codex
  引擎指纹头与 `client_metadata`；缺指纹会被上游按非 Codex 客户端拒绝或不下发状态。
- **原始令牌不进管理端响应**：列表/历史只有 SHA-256 指纹、长度、倒计时，符合本 fork
  的凭据脱敏约定；令牌原文只存库并用于注入。
- 迁移 `248_codex_turn_state.sql` 只新建 `codex_turn_states`，不改任何业务表。

## 2026-09-18 追加 — 实测：state 长度就是降智标记（332 = 未降智，356 = 降智）

动态住宅 IP 反复撞测 + 糖果题判据，完整数据见 `docs/CODEX_STATE_LENGTH_RESULT.md`。

- **两种 state 长度**：`332`（答 21，未降智）与 `356`（答 28/29/36，降智）。
  60 个样本 Fernet 结构自洽（0x80 / 时间戳 / IV / 密文 / HMAC），**密文正好相差一个 AES 块**（192 vs 208 字节）。
- **完全对应**：n=26 有效判定，332 → 3/3 答 21，356 → 0/23 答 21（单侧 Fisher p≈3.8e-4）。
- **类别跟账号、不跟 IP**：`codex特惠` 15 个可用账号逐账号稳定（`123904` 两次不同 IP 都是 332）；
  用户新给账号（business、配额 0%）用 14 个不同美国住宅 IP 撞测**全是 356**。
  本轮**没有出现长度 292/312**（方向一致，绝对值为 332/356）。
- **回带两类 state 都被上游接受**（不再补发），但回带 356 不能让降智账号变好 ——
  **state 是标记不是解药**；好账号不注入也是 21，跨账号共享仍被拒。
- 建议后续：给 `codex_turn_state` 加「长度类别」闸门（只持久化/注入 332 类），
  账号筛选改成「一次冷请求读 state 长度」（本轮命中率 2/15 ≈ 13%）。
- 本轮只发外部请求：**未改线上配置、未重启服务、未写业务表**；临时令牌文件用完即删。

## 2026-09-18 追加 — 形态闸门 + 注入模式（照实测与 gpt-load 补齐）

参考公开 fork `DesuwaDev/gpt-load` 的 `X-Codex-Turn-State` 相关提交（`2b219e0d7` 观测与强制注入、
`56031a2c3` 按模型限定注入、`a4f9f360` 332 字符 team 形态属正常、`1f8f54e11` 按密文块数标疑似降智）。

- **修正理解**：292 / 312 是**状态长度**（个人号正常/降智），team 号是 332 / 356；
  与响应码无关。`revocation_statuses` 默认值因此**改为空**（312 不再当撤销码），
  `issuance_statuses` 保留但正常应留空。
- `internal/service/codex_turn_state.go`：新增形态表与 `ClassifyCodexTurnState`
  （Fernet 封装 → 密文块数 → individual/team × 正常/降智），
  新增配置 `inject_mode`(fill_empty|force) 与 `allow_degraded_shapes`(默认 false)，
  新增状态 `degraded`。
- `codex_turn_state_service.go`：降智形态只落库不写缓存（不会顶掉好状态）、
  注入前再校验形态；`ForceInject()` 暴露给网关。
- `openai_plugin_transport.go`：网关 hook 支持 `force` 覆盖客户端回带的降智状态
  （只补空白头等于永不注入，因为真实客户端每轮都自带状态）。
- 前端：配置表单加「注入模式」下拉与「降智形态」开关，`toDraft` 对旧配置缺字段做兜底；
  撤销/下发状态码的说明改成正确表述。
- 测试：后端新增 `codex_turn_state_shape_test.go`（形态表、降智不入池、不顶替好状态、
  allow_degraded_shapes 逃生门、force 覆盖），并更新受默认值影响的旧用例；前端新增 1 条表单用例。
  后端 `internal/service` 全绿，前端 typecheck + 该页 7 条用例全绿。
- 文档：`docs/CODEX_STATE_LENGTH_RESULT.md` 补第 7/8 节（模型对应关系、12/12 保路实测、
  292/312 归属），`docs/CODEX_TURN_STATE.md` 补形态闸门与注入模式一节。
- **未部署**：以上均未上美东独服，线上仍是旧行为。

## 2026-09-18 追加 — 注入模型白名单（`inject_models`）+ 换模型注入实测

- **实测结论**：状态**绑模型**。同一份 332（在 `gpt-6-astra` 下采集）回带到 `gpt-5.5` / `gpt-5.6-sol`
  请求上，上游 **6/6 次拒绝并补发新状态**；同一状态在 `gpt-6-astra` 上 **15/15 次被接受**。
  `gpt-5.3-codex` 直接 400（ChatGPT 账号不支持该模型）。详见 `docs/CODEX_TURN_STATE.md` 第 5 节。
- **后端**：`CodexTurnStateConfig.InjectModels`
  - 留空 = **只对采集模型注入**（安全默认，依据上面的实测）；`*` = 所有模型；结尾 `*` 前缀匹配；
    模型名读不出来时照样注入；单条最长 96 字符、最多 20 条，字符集受限（字母数字与 `- . _ /`）。
  - `InjectionHeader(accountID, requestedModel)` 增加模型参数；新增 `ModelScoped()` 供网关判断是否需要
    读模型。网关钩子用 `requestedModelOf`（`request.GetBody()` 读副本 + 256 KiB 上限，不消耗原 body）
    只在配了白名单时回放请求体。
- **前端**：配置页新增「注入模型白名单」输入框（逗号/空格分隔，大小写归一、去重、排序），
  `toDraft` / `pickConfig` / `mergeConfig` / `parsedConfig` 同步；说明文案标注"状态绑模型"的实测依据。
- **测试**：后端新增 `TestInjectionMatchesModel`、`TestValidateCodexTurnStateConfigRejectsBadModelEntries`、
  `TestInjectCodexTurnStateReadsTheRequestedModel`（含 body 未被消耗的断言），并同步 `InjectionHeader`
  新签名的既有用例；前端表单用例补 `inject_models` 断言。后端 `internal/service` 全绿，前端 typecheck + 7 条用例全绿。
- **仍未部署**。
