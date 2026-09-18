<template>
  <AppLayout>
    <div class="cts-page">
      <header class="flex flex-wrap items-start justify-between gap-4">
        <div>
          <p class="mb-2 text-xs font-semibold tracking-widest text-primary-600 dark:text-primary-400">管理员专属 · Codex 状态池</p>
          <h1 class="text-3xl font-semibold tracking-tight">Codex 状态池</h1>
          <p class="mt-2 text-sm text-gray-500 dark:text-gray-400">走动态 IP 代理探测上游 x-codex-turn-state，按分组注入到 Codex 账号请求；指纹仅用于定位，绝不是原始 token。</p>
        </div>
        <div class="flex flex-wrap items-center gap-3">
          <div class="cts-panel flex items-center gap-3 px-4 py-3">
            <Toggle id="cts-enabled" :model-value="draft.enabled" :disabled="loading || saving || !config" @update:model-value="toggleEnabled" />
            <span class="text-sm font-medium" aria-live="polite">{{ draft.enabled ? '已启用' : '已停用' }}</span>
          </div>
          <button class="btn btn-secondary min-h-11" :disabled="loading || saving" @click="refresh()">
            <Icon name="refresh" size="sm" class="mr-2" />{{ loading ? '读取中…' : '刷新' }}
          </button>
        </div>
      </header>

      <p v-if="error" role="alert" class="cts-alert">{{ error }}</p>
      <p v-if="overview?.last_error" role="alert" class="cts-alert">最近一次探测错误：{{ overview.last_error }}</p>
      <p v-if="notice" role="status" class="cts-notice">{{ notice }}</p>
      <p v-if="optionsError" role="alert" class="cts-alert">{{ optionsError }}</p>

      <section class="grid gap-3 text-sm sm:grid-cols-2 xl:grid-cols-4" aria-label="状态池概览">
        <div class="cts-panel"><p class="cts-label">入池账号</p><p class="mt-2 text-2xl font-semibold tabular-nums">{{ overview ? overview.enrolled_accounts : '—' }}</p></div>
        <div class="cts-panel"><p class="cts-label">生效状态</p><p class="mt-2 text-2xl font-semibold tabular-nums">{{ overview ? overview.active_states : '—' }}</p></div>
        <div class="cts-panel"><p class="cts-label">即将到期</p><p class="mt-2 text-2xl font-semibold tabular-nums">{{ overview ? overview.expiring_soon : '—' }}</p></div>
        <div class="cts-panel"><p class="cts-label">已撤销 / 失败</p><p class="mt-2 text-2xl font-semibold tabular-nums">{{ overview ? `${overview.revoked_states} / ${overview.failed_states}` : '—' }}</p></div>
        <div class="cts-panel"><p class="cts-label">今日探测</p><p class="mt-2 text-2xl font-semibold tabular-nums">{{ overview ? overview.probes_today : '—' }}</p></div>
        <div class="cts-panel"><p class="cts-label">今日采集</p><p class="mt-2 text-2xl font-semibold tabular-nums">{{ overview ? overview.harvests_today : '—' }}</p></div>
        <div class="cts-panel">
          <p class="cts-label">最近探测时间</p>
          <p class="mt-2 tabular-nums">{{ time(overview?.last_probe_at) }}</p>
          <p class="mt-1 text-xs text-gray-500 dark:text-gray-400">状态 {{ overview?.last_probe_status || '—' }}</p>
        </div>
        <div class="cts-panel">
          <p class="cts-label">下次轮询</p>
          <p class="mt-2 tabular-nums" aria-live="polite">{{ countdown(secondsUntil(overview?.next_tick_at), '即将执行') }}</p>
        </div>
      </section>

      <form class="cts-panel" @submit.prevent="saveConfig">
        <div class="flex flex-wrap items-start justify-between gap-3">
          <div>
            <h2 class="font-semibold">状态池配置</h2>
            <p class="mt-1 text-xs text-gray-500 dark:text-gray-400">采集使用动态 IP 代理；双向移组需同时开启状态池与注入，不改写账号凭据。</p>
          </div>
          <div class="flex items-center gap-3">
            <span v-if="draftDirty" class="text-xs text-amber-700 dark:text-amber-300">有未保存修改</span>
            <button type="submit" class="btn btn-primary min-h-11" :disabled="!draftDirty || !draftValid || saving || loading || !config">
              {{ saving ? '正在保存…' : '保存配置' }}
            </button>
          </div>
        </div>

        <p v-if="validation.length" role="alert" class="cts-alert mt-4">{{ validation.join('；') }}</p>

        <div class="mt-5 grid gap-5 lg:grid-cols-2">
          <fieldset class="min-w-0 rounded-xl border border-gray-200 p-4 dark:border-dark-700 lg:col-span-2" :disabled="loading || saving">
            <legend class="px-2 font-semibold">票据双向移组</legend>
            <label class="flex min-h-11 items-center gap-3">
              <input id="cts-transfer-enabled" v-model="draft.transfer_enabled" type="checkbox" class="h-4 w-4" />
              按有效票据在 A ↔ B 之间自动移组
            </label>
            <p class="mt-2 text-xs text-gray-500 dark:text-gray-400">A、B 全部账号持续检测；有效票据留在 A，缺失、过期或撤销后转入 B，恢复后回 A。保留其他分组；关闭本规则不撤销已完成的移组。需同时开启状态池和注入。</p>
            <div v-if="draft.transfer_enabled" class="mt-3 grid gap-4 md:grid-cols-3">
              <div>
                <label for="cts-ready-group" class="cts-label">A · 可用分组</label>
                <select id="cts-ready-group" v-model.number="draft.transfer_ready_group_id" class="input mt-2 min-h-11 w-full">
                  <option :value="0">选择分组</option>
                  <option v-for="group in groups" :key="group.id" :value="group.id">{{ groupLabel(group.id) }}</option>
                </select>
              </div>
              <div>
                <label for="cts-recovery-group" class="cts-label">B · 待恢复分组</label>
                <select id="cts-recovery-group" v-model.number="draft.transfer_recovery_group_id" class="input mt-2 min-h-11 w-full">
                  <option :value="0">选择分组</option>
                  <option v-for="group in groups" :key="group.id" :value="group.id">{{ groupLabel(group.id) }}</option>
                </select>
              </div>
              <div>
                <label for="cts-cycle-cooldown" class="cts-label">达到上限后的冷却秒数</label>
                <input id="cts-cycle-cooldown" v-model.number="draft.cycle_cooldown_seconds" type="number" min="1" max="86400" class="input mt-2 min-h-11 w-full" />
              </div>
            </div>
            <p v-if="draft.transfer_enabled && (!draft.enabled || !draft.inject_enabled)" role="status" class="mt-2 text-sm text-amber-700 dark:text-amber-300">规则暂未运行：请同时开启状态池与注入。</p>
          </fieldset>
          <div class="min-w-0 lg:col-span-2">
            <label for="cts-harvest-transport" class="cts-label">主动采集通道</label>
            <select id="cts-harvest-transport" v-model="draft.harvest_transport" class="input mt-2 min-h-11 w-full" :disabled="loading || saving">
              <option value="account">原账号通道（插件 / TLS，保持现有行为）</option>
              <option value="independent">独立通道（指定代理 / HTTP 1.1 / 不复用连接）</option>
            </select>
            <p class="mt-2 text-xs text-gray-500 dark:text-gray-400">只影响后续主动采集，不改变业务通道，也不清除有效票据。独立通道失败不会改走业务代理；新建连接不保证每次更换出口 IP。</p>
          </div>
          <div class="min-w-0">
            <label for="cts-proxy" class="cts-label">动态IP代理</label>
            <select id="cts-proxy" v-model.number="draft.proxy_id" class="input mt-2 min-h-11 w-full" :disabled="loading || saving">
              <option :value="0">使用账号自身代理（未设则直连）</option>
              <option v-if="missingProxy" :value="draft.proxy_id" disabled>已删除或不可用的代理 #{{ draft.proxy_id }}，请重新选择</option>
              <option v-for="proxy in proxies" :key="proxy.id" :value="proxy.id">{{ proxyOptionLabel(proxy) }}</option>
            </select>
            <p class="mt-2 text-xs text-gray-500 dark:text-gray-400">当前：{{ proxyName || '未配置代理' }}<span v-if="overview?.proxy_configured" class="ml-2">（已配置）</span></p>
          </div>

          <div class="min-w-0">
            <label for="cts-account-ids" class="cts-label">指定账号（可选）</label>
            <input id="cts-account-ids" v-model="draft.account_ids" type="text" class="input mt-2 min-h-11 w-full" placeholder="留空 = 按分组全部账号，例如 12, 34 56" :disabled="loading || saving" />
            <p class="mt-2 text-xs text-gray-500 dark:text-gray-400">逗号或空格分隔的账号 ID，仅填正整数；留空时按上方分组圈定账号。</p>
          </div>

          <div class="min-w-0 lg:col-span-2">
            <span class="cts-label">检测分组</span>
            <div class="mt-2 max-h-56 overflow-auto rounded-xl border border-gray-200 p-3 dark:border-dark-700">
              <p v-if="!groups.length" class="text-sm text-gray-500 dark:text-gray-400">{{ optionsLoading ? '正在读取分组…' : '暂无可用分组' }}</p>
              <label v-for="group in groups" :key="group.id" class="flex cursor-pointer items-center gap-3 rounded-lg px-2 py-2 hover:bg-gray-50 dark:hover:bg-dark-900">
                <input v-model="draft.group_ids" type="checkbox" :value="group.id" class="h-4 w-4" :disabled="loading || saving" />
                <span class="truncate text-sm">{{ group.name }} · #{{ group.id }}</span>
                <span class="ml-auto shrink-0 text-xs text-gray-500 dark:text-gray-400">{{ group.platform }}</span>
              </label>
            </div>
          </div>

          <div class="min-w-0">
            <label for="cts-model" class="cts-label">模型</label>
            <input id="cts-model" v-model="draft.model" type="text" class="input mt-2 min-h-11 w-full" placeholder="例如 gpt-5-codex" :disabled="loading || saving" />
          </div>

          <div class="min-w-0">
            <label for="cts-inject-header" class="cts-label">注入请求头名称</label>
            <input id="cts-inject-header" v-model="draft.inject_header" type="text" class="input mt-2 min-h-11 w-full" placeholder="x-codex-turn-state" :disabled="loading || saving" />
          </div>

          <div class="min-w-0 lg:col-span-2">
            <label for="cts-prompt" class="cts-label">提示词</label>
            <textarea id="cts-prompt" v-model="draft.prompt" rows="3" class="input mt-2 w-full" placeholder="采集时发送给模型的探测提示词" :disabled="loading || saving" />
          </div>

          <div class="min-w-0">
            <label for="cts-ttl" class="cts-label">TTL 秒（60–86400）</label>
            <input id="cts-ttl" v-model.number="draft.ttl_seconds" type="number" min="60" max="86400" step="1" class="input mt-2 min-h-11 w-full" :disabled="loading || saving" />
          </div>

          <div class="min-w-0">
            <label for="cts-renew" class="cts-label">提前续期秒（0–3600，且小于 TTL）</label>
            <input id="cts-renew" v-model.number="draft.renew_before_seconds" type="number" min="0" max="3600" step="1" class="input mt-2 min-h-11 w-full" :disabled="loading || saving" />
          </div>

          <div class="min-w-0">
            <label for="cts-poll" class="cts-label">入池/续期扫描秒（5–3600）</label>
            <input id="cts-poll" v-model.number="draft.poll_seconds" type="number" min="5" max="3600" step="1" class="input mt-2 min-h-11 w-full" :disabled="loading || saving" />
          </div>

          <div class="min-w-0">
            <label for="cts-timeout" class="cts-label">探测超时秒（10–600）</label>
            <input id="cts-timeout" v-model.number="draft.probe_timeout_seconds" type="number" min="10" max="600" step="1" class="input mt-2 min-h-11 w-full" :disabled="loading || saving" />
          </div>

          <div class="min-w-0">
            <label for="cts-max-accounts" class="cts-label">每次扫描入队账号数（1–200）</label>
            <input id="cts-max-accounts" v-model.number="draft.max_accounts_per_tick" type="number" min="1" max="200" step="1" class="input mt-2 min-h-11 w-full" :disabled="loading || saving" />
          </div>
          <div class="min-w-0">
            <label for="cts-concurrency" class="cts-label">并发采集账号数（1–64）</label>
            <input id="cts-concurrency" v-model.number="draft.concurrent_accounts" type="number" min="1" max="64" class="input mt-2 min-h-11 w-full" :disabled="loading || saving" />
            <p class="mt-2 text-xs text-gray-500">同时处理多个账号，每个账号最多一条采集请求；已入队账号完成后立即补位。限流响应遵守 Retry-After，错误响应退避。</p>
          </div>
          <div class="min-w-0">
            <label for="cts-retry-ms" class="cts-label">连续采集间隔毫秒（50–60000）</label>
            <input id="cts-retry-ms" v-model.number="draft.retry_interval_ms" type="number" min="50" max="60000" class="input mt-2 min-h-11 w-full" :disabled="loading || saving" />
            <p class="mt-2 text-xs text-gray-500">上次请求结束后到下次请求的等待时间；与入池扫描间隔分开。默认 200 毫秒。</p>
          </div>
          <div class="min-w-0">
            <label for="cts-target-length" class="cts-label">命中目标长度</label>
            <select id="cts-target-length" v-model.number="draft.target_state_length" class="input mt-2 min-h-11 w-full" :disabled="loading || saving">
              <option :value="0">自动：个人 292 / Team 332</option>
              <option :value="292">仅个人账号：292</option>
              <option :value="332">仅 Team 账号：332</option>
            </select>
            <p class="mt-2 text-xs text-gray-500">命中后锁定当前状态至续期窗口；其他响应不会提前替换。续期失败时旧状态保留至实际到期。</p>
          </div>
          <div class="min-w-0">
            <label for="cts-max-attempts" class="cts-label">每账号连续采集上限（1–10000）</label>
            <input id="cts-max-attempts" v-model.number="draft.max_attempts_per_cycle" type="number" min="1" max="10000" class="input mt-2 min-h-11 w-full" :disabled="loading || saving" />
            <p class="mt-2 text-xs text-gray-500">默认 50 次，入队后连续采集，不等下一轮扫描；多账号并发、同账号串行，命中立即停止并锁定。达到上限暂停，点击该账号“立即采集”可重新开始；重复点击不叠加任务，次数跨重启保留。</p>
          </div>

          <div class="min-w-0">
            <label for="cts-revocation" class="cts-label">撤销状态码（逗号分隔，100–599）</label>
            <input id="cts-revocation" v-model="draft.revocation_statuses" type="text" class="input mt-2 min-h-11 w-full" placeholder="留空：不按状态码撤销" :disabled="loading || saving" />
            <p class="mt-1 text-xs text-gray-500 dark:text-gray-400">
              命中即立刻丢弃状态并重新采集。留空是有意的：写法里的「312」实测是状态长度而不是响应码。
            </p>
          </div>

          <div class="min-w-0">
            <label class="cts-label" for="cts-issuance">下发状态码（留空=任意成功响应）</label>
            <input id="cts-issuance" v-model="draft.issuance_statuses" type="text" class="input mt-2 min-h-11 w-full" placeholder="留空：接受任意成功响应携带的状态" :disabled="loading || saving" />
            <p class="mt-1 text-xs text-gray-500 dark:text-gray-400">
              状态按账号绑定，无法跨账号共用：把 A 的状态给 B，上游会拒绝并为 B 补发新的。
              写法里的「292」同样是长度（个人号正常形态），所以正常情况应留空。
            </p>
          </div>

          <div class="min-w-0">
            <span class="cts-label">注入开关</span>
            <div class="mt-2 flex items-center gap-3">
              <Toggle id="cts-inject" :model-value="draft.inject_enabled" :disabled="loading || saving" @update:model-value="draft.inject_enabled = $event" />
              <span class="text-sm">{{ draft.inject_enabled ? '注入已开启' : '仅采集不注入' }}</span>
            </div>
          </div>

          <div class="min-w-0">
            <span class="cts-label">注入模式</span>
            <select id="cts-inject-mode" v-model="draft.inject_mode" class="input mt-2 min-h-11 w-full" :disabled="loading || saving">
              <option value="fill_empty">只补空白头（保留客户端自带状态）</option>
              <option value="force">覆盖客户端状态（能把降智轮次拉回正常路由）</option>
            </select>
            <p class="mt-1 text-xs text-gray-500 dark:text-gray-400">
              真实 Codex 客户端每轮都会回带自己的状态，而账号降智时那个状态恰好就是降智标记（356 字符 / 13 块）。
              只有「覆盖」才能把它换成池子里抓到的正常状态（332 字符 / 12 块）；实测回带正常状态 12/12 次答对，冷请求只有约 9%。
            </p>
          </div>

          <div class="min-w-0">
            <span class="cts-label">严格长度校验</span>
            <p class="mt-2 text-sm">仅锁定个人 292 / Team 332；312、356、376 及其他未识别形态只记历史，不注入。</p>
          </div>

          <div class="min-w-0">
            <label class="cts-label" for="cts-inject-models">注入模型白名单（逗号分隔，留空=只用采集模型）</label>
            <input id="cts-inject-models" v-model="draft.inject_models" type="text" class="input mt-2 min-h-11 w-full" placeholder="留空：只用采集时那个模型；填 * 表示所有模型" :disabled="loading || saving" />
            <p class="mt-1 text-xs text-gray-500 dark:text-gray-400">
              状态是绑模型的：实测把 gpt-6-astra 采集到的 332 用在 gpt-5.5 / gpt-5.6-sol 请求上，
              上游 6/6 次直接拒掉并补发新状态（同一状态在 gpt-6-astra 上 15/15 次被接受），
              所以默认只对采集模型注入。结尾加 * 做前缀匹配；请求里读不出模型名时跳过注入。
            </p>
          </div>
        </div>
      </form>

      <section class="cts-panel">
        <div class="flex flex-wrap items-start justify-between gap-3">
          <div>
            <h2 class="font-semibold">入池账号</h2>
            <p class="mt-1 text-xs text-gray-500 dark:text-gray-400">共 {{ overview?.accounts.length || 0 }} 个账号；状态来自最近一次采集或探测。</p>
            <p class="mt-1 text-xs text-gray-500 dark:text-gray-400">正在采集 {{ overview?.collecting_accounts || 0 }} / {{ config?.concurrent_accounts || 16 }} 个；等待队列 {{ overview?.queued_accounts || 0 }} 个。入队后连续采集，不等待轮询。</p>
          </div>
          <button class="btn btn-secondary min-h-11" :disabled="collecting || loading" @click="collect(0)">
            {{ collecting && collectingId === 0 ? '正在排队…' : '全部立即采集' }}
          </button>
        </div>
        <div class="mt-4 overflow-x-auto">
          <table class="cts-table">
            <thead>
              <tr>
                <th>账号 ID</th><th>账号名</th><th>分组</th><th>状态</th><th>指纹</th><th>长度</th><th>剩余时间</th><th>连续采集</th><th>来源</th><th>HTTP</th><th>耗时</th><th>最近记录时间</th><th>操作</th>
              </tr>
            </thead>
            <tbody>
              <tr v-for="row in overview?.accounts || []" :key="row.account_id">
                <td>{{ row.account_id }}</td>
                <td class="max-w-[14rem] truncate">{{ row.account_name || '—' }}</td>
                <td>{{ row.current_group_ids?.length ? row.current_group_ids.map(groupLabel).join('、') : groupLabel(row.group_id) }}<p class="mt-1 text-xs">{{ row.transfer_status }}</p><p v-if="row.transfer_error" class="mt-1 text-xs text-red-600">{{ row.transfer_error }}</p></td>
                <td><span :class="statusClass(rowStatus(row))">{{ statusLabel(rowStatus(row)) }}</span><p class="mt-1 text-xs">{{ collectionLabel(row.collection_status) }}</p><p v-if="config?.transfer_enabled" class="mt-1 text-xs">{{ row.ticket_qualified ? '票据合格' : '暂无合格绑定票据' }}</p></td>
                <td class="font-mono text-xs">{{ row.state_fingerprint || '—' }}</td>
                <td class="tabular-nums">{{ row.state_length || '—' }}</td>
                <td class="tabular-nums">{{ remainingLabel(row) }}</td>
                <td class="tabular-nums">{{ row.probe_attempts || 0 }} / {{ config?.max_attempts_per_cycle || 50 }}<p v-if="row.next_retry_at" class="text-xs text-amber-700">{{ retrySeconds(row.next_retry_at) }} 秒后可重试</p><p v-else-if="row.attempts_limit_reached" class="text-xs text-amber-700">达到上限，已暂停</p></td>
                <td>{{ sourceLabel(row.source) }}</td>
                <td class="tabular-nums">{{ row.http_status || '—' }}</td>
                <td class="tabular-nums">{{ row.latency_ms ? `${row.latency_ms} ms` : '—' }}</td>
                <td class="tabular-nums">{{ time(row.recorded_at) }}</td>
                <td>
                  <div class="flex flex-wrap items-center gap-2">
                    <button class="btn btn-secondary min-h-9 px-3 text-xs" :disabled="collecting" @click="collect(row.account_id)">
                      {{ collecting && collectingId === row.account_id ? '排队中…' : '立即采集' }}
                    </button>
                    <template v-if="confirmingId === row.account_id">
                      <button class="btn btn-danger min-h-9 px-3 text-xs" :disabled="!!invalidatingId" @click="invalidate(row)">
                        {{ invalidatingId === row.account_id ? '作废中…' : '确认作废' }}
                      </button>
                      <button class="btn btn-ghost min-h-9 px-3 text-xs" @click="confirmingId = 0">取消</button>
                    </template>
                    <button v-else class="btn btn-ghost min-h-9 px-3 text-xs" :disabled="!!invalidatingId" @click="confirmingId = row.account_id">作废</button>
                  </div>
                </td>
              </tr>
              <tr v-if="!(overview?.accounts || []).length">
                <td colspan="13" class="text-center text-gray-500 dark:text-gray-400">{{ loading ? '正在读取入池账号…' : '暂无入池账号' }}</td>
              </tr>
            </tbody>
          </table>
        </div>
      </section>

      <section v-if="overview?.transfers?.length" class="cts-panel" aria-label="移组记录">
        <h2 class="font-semibold">最近移组记录</h2>
        <ul class="mt-3 space-y-3 text-sm">
          <li v-for="move in overview.transfers" :key="move.id">
            <span class="tabular-nums">{{ time(move.created_at) }}</span>
            · 账号 #{{ move.account_id }} · {{ groupLabel(move.from_group_id) }} → {{ groupLabel(move.to_group_id) }}
            <p class="mt-1 text-xs text-gray-500 dark:text-gray-400">{{ move.reason }} · 票据记录 #{{ move.ticket_record_id || '—' }}</p>
          </li>
        </ul>
      </section>

      <section class="cts-panel">
        <div class="flex flex-wrap items-start justify-between gap-3">
          <div>
            <h2 class="font-semibold">采集历史</h2>
            <p class="mt-1 text-xs text-gray-500 dark:text-gray-400">共 {{ history?.total || 0 }} 条记录，每页 20 条。真实流量回喂与主动采集分别显示；清空后新记录仍会继续产生。</p>
          </div>
          <div class="flex items-center gap-3 text-sm">
            <template v-if="confirmHistoryClear">
              <button class="btn btn-danger min-h-11" :disabled="clearingHistory" @click="clearHistory">{{ clearingHistory ? '正在清空…' : '确认清空历史' }}</button>
              <button class="btn btn-ghost min-h-11" :disabled="clearingHistory" @click="confirmHistoryClear = false">取消</button>
            </template>
            <button v-else class="btn btn-secondary min-h-11" :disabled="historyLoading || clearingHistory" @click="confirmHistoryClear = true">清空历史</button>
            <button class="btn btn-secondary min-h-11" :disabled="historyPage <= 1 || historyLoading" @click="changeHistoryPage(-1)">上一页</button>
            <span class="tabular-nums">{{ historyPage }} / {{ historyPages }}</span>
            <button class="btn btn-secondary min-h-11" :disabled="historyPage >= historyPages || historyLoading" @click="changeHistoryPage(1)">下一页</button>
          </div>
        </div>
        <p v-if="historyError" role="alert" class="cts-alert mt-4">{{ historyError }}</p>
        <p v-if="confirmHistoryClear" role="alert" class="cts-alert mt-4">确认删除历史记录？当前锁定状态、必要的作废标记及连续采集次数会保留，此操作不会重新采集账号。</p>
        <div class="mt-4 overflow-x-auto">
          <table class="cts-table">
            <thead>
              <tr><th>账号</th><th>状态</th><th>来源</th><th>HTTP</th><th>模型</th><th>耗时</th><th>指纹</th><th>长度</th><th>创建时间</th><th>错误</th></tr>
            </thead>
            <tbody>
              <tr v-for="row in history?.items || []" :key="row.id">
                <td class="max-w-[12rem] truncate">{{ row.account_name || `#${row.account_id}` }}</td>
                <td><span :class="statusClass(row.status)">{{ statusLabel(row.status) }}</span></td>
                <td>{{ sourceLabel(row.source) }}</td>
                <td class="tabular-nums">{{ row.http_status || '—' }}</td>
                <td class="max-w-[12rem] truncate">{{ row.model || '—' }}</td>
                <td class="tabular-nums">{{ row.latency_ms ? `${row.latency_ms} ms` : '—' }}</td>
                <td class="font-mono text-xs">{{ row.state_fingerprint || '—' }}</td>
                <td class="tabular-nums">{{ row.state_length || '—' }}</td>
                <td class="tabular-nums">{{ time(row.created_at) }}</td>
                <td class="max-w-[16rem] truncate text-red-700 dark:text-red-300">{{ row.error || '—' }}</td>
              </tr>
              <tr v-if="!(history?.items || []).length">
                <td colspan="10" class="text-center text-gray-500 dark:text-gray-400">{{ historyLoading ? '正在读取历史记录…' : '暂无历史记录' }}</td>
              </tr>
            </tbody>
          </table>
        </div>
      </section>
    </div>
  </AppLayout>
</template>

<script setup lang="ts">
import { computed, onMounted, onUnmounted, ref } from 'vue'
import AppLayout from '@/components/layout/AppLayout.vue'
import Icon from '@/components/icons/Icon.vue'
import Toggle from '@/components/common/Toggle.vue'
import {
  collectCodexTurnState,
  getCodexTurnState,
  getCodexTurnStateHistory,
  clearCodexTurnStateHistory,
  invalidateCodexTurnState,
  saveCodexTurnStateConfig,
  type CodexTurnStateAccountRow,
  type CodexTurnStateConfig,
  type CodexTurnStateHistoryPage,
  type CodexTurnStateOverview
} from '@/api/codexTurnState'
import { getAll as getAllProxies } from '@/api/admin/proxies'
import { getAll as getAllGroups } from '@/api/admin/groups'
import type { AdminGroup, Proxy } from '@/types'

/** 表单草稿：数字字段允许临时为空字符串，保存前统一校验并转换。 */
interface ConfigDraft {
  transfer_enabled: boolean
  transfer_ready_group_id: number
  transfer_recovery_group_id: number
  cycle_cooldown_seconds: number | string
  harvest_transport: 'account' | 'independent'
  enabled: boolean
  inject_enabled: boolean
  inject_mode: string
  allow_degraded_shapes: boolean
  inject_models: string
  inject_header: string
  proxy_id: number
  group_ids: number[]
  account_ids: string
  model: string
  prompt: string
  ttl_seconds: number | string
  renew_before_seconds: number | string
  poll_seconds: number | string
  probe_timeout_seconds: number | string
  max_accounts_per_tick: number | string
  target_state_length: number
  max_attempts_per_cycle: number | string
  concurrent_accounts: number | string
  retry_interval_ms: number | string
  revocation_statuses: string
  issuance_statuses: string
}

const HISTORY_PAGE_SIZE = 20
const HEADER_TOKEN = /^[A-Za-z0-9-]+$/
const STATUS_LABELS: Record<string, string> = {
  active: '生效中',
  expiring_soon: '即将到期',
  expired: '已过期',
  revoked: '已撤销',
  failed: '失败',
  degraded: '未命中（降智形态）',
  standby: '候选（保留锁定状态）',
  rejected: '未命中目标',
  none: '未获取'
}
const STATUS_CLASSES: Record<string, string> = {
  active: 'cts-badge cts-badge-green',
  expiring_soon: 'cts-badge cts-badge-amber',
  expired: 'cts-badge cts-badge-red',
  revoked: 'cts-badge cts-badge-red',
  failed: 'cts-badge cts-badge-red',
  none: 'cts-badge cts-badge-gray'
}

const overview = ref<CodexTurnStateOverview | null>(null)
const config = ref<CodexTurnStateConfig | null>(null)
const history = ref<CodexTurnStateHistoryPage | null>(null)
const proxies = ref<Proxy[]>([])
const groups = ref<AdminGroup[]>([])
const loading = ref(false)
const saving = ref(false)
const error = ref('')
const notice = ref('')
const optionsLoading = ref(false)
const optionsError = ref('')
const historyLoading = ref(false)
const historyError = ref('')
const historyPage = ref(1)
const confirmHistoryClear = ref(false)
const clearingHistory = ref(false)
const collecting = ref(false)
const collectingId = ref(0)
const invalidatingId = ref(0)
const confirmingId = ref(0)
const now = ref(Date.now())
const loadedAt = ref(Date.now())

const draft = ref<ConfigDraft>({
  transfer_enabled: false,
  transfer_ready_group_id: 0,
  transfer_recovery_group_id: 0,
  cycle_cooldown_seconds: 300,
  harvest_transport: 'account',
  enabled: false,
  inject_enabled: true,
  inject_mode: 'fill_empty',
  inject_models: '',
  allow_degraded_shapes: false,
  inject_header: 'x-codex-turn-state',
  proxy_id: 0,
  group_ids: [],
  account_ids: '',
  model: '',
  prompt: '',
  ttl_seconds: 3600,
  renew_before_seconds: 300,
  poll_seconds: 45,
  probe_timeout_seconds: 120,
  max_accounts_per_tick: 8,
  target_state_length: 0,
  max_attempts_per_cycle: 50,
  concurrent_accounts: 16,
  retry_interval_ms: 200,
  revocation_statuses: '',
  issuance_statuses: ''
})

function toInt(value: number | string): number {
  if (typeof value === 'number') return Number.isFinite(value) ? Math.trunc(value) : NaN
  const text = value.trim()
  if (!/^-?\d+$/.test(text)) return NaN
  return Number.parseInt(text, 10)
}

/** 逗号 / 空格 / 顿号分隔的 ID 列表；返回合法集合与被拒绝的原始片段。 */
function parseIDs(raw: string): { ids: number[]; invalid: string[] } {
  const ids: number[] = []
  const invalid: string[] = []
  for (const token of raw.split(/[\s,，、;；]+/).filter(Boolean)) {
    const value = toInt(token)
    if (!Number.isFinite(value) || value <= 0) invalid.push(token)
    else ids.push(value)
  }
  return { ids, invalid }
}

function inRange(value: number, min: number, max: number): boolean {
  return Number.isFinite(value) && value >= min && value <= max
}

/** 注入模型白名单：小写、去重；结尾 * 由后端做前缀匹配。 */
function parseModelList(raw: string): string[] {
  const seen = new Set<string>()
  for (const token of raw.split(/[\s,，、;；]+/).filter(Boolean)) {
    seen.add(token.trim().toLowerCase())
  }
  return [...seen]
}

function normalizeConfig(cfg: CodexTurnStateConfig): CodexTurnStateConfig {
  return {
    transfer_enabled: !!cfg.transfer_enabled,
    transfer_ready_group_id: cfg.transfer_ready_group_id ?? 0,
    transfer_recovery_group_id: cfg.transfer_recovery_group_id ?? 0,
    cycle_cooldown_seconds: cfg.cycle_cooldown_seconds ?? 300,
    harvest_transport: cfg.harvest_transport === 'independent' ? 'independent' : 'account',
    enabled: !!cfg.enabled,
    inject_enabled: !!cfg.inject_enabled,
    inject_mode: cfg.inject_mode === 'force' ? 'force' : 'fill_empty',
    inject_models: [...new Set(cfg.inject_models || [])].map(m => m.toLowerCase()).sort(),
    allow_degraded_shapes: false,
    inject_header: (cfg.inject_header || '').trim(),
    proxy_id: Number.isFinite(cfg.proxy_id) ? cfg.proxy_id : 0,
    group_ids: [...new Set(cfg.group_ids || [])].sort((a, b) => a - b),
    account_ids: [...new Set(cfg.account_ids || [])].sort((a, b) => a - b),
    model: (cfg.model || '').trim(),
    prompt: cfg.prompt || '',
    ttl_seconds: cfg.ttl_seconds,
    renew_before_seconds: cfg.renew_before_seconds,
    poll_seconds: cfg.poll_seconds,
    probe_timeout_seconds: cfg.probe_timeout_seconds,
    max_accounts_per_tick: cfg.max_accounts_per_tick,
    target_state_length: cfg.target_state_length ?? 0,
    max_attempts_per_cycle: cfg.max_attempts_per_cycle ?? 50,
    concurrent_accounts: cfg.concurrent_accounts ?? 16,
    retry_interval_ms: cfg.retry_interval_ms ?? 200,
    revocation_statuses: [...new Set(cfg.revocation_statuses || [])].sort((a, b) => a - b),
    issuance_statuses: [...new Set(cfg.issuance_statuses || [])].sort((a, b) => a - b)
  }
}

function pickConfig(source: CodexTurnStateConfig): CodexTurnStateConfig {
  return normalizeConfig({
    transfer_enabled: source.transfer_enabled,
    transfer_ready_group_id: source.transfer_ready_group_id,
    transfer_recovery_group_id: source.transfer_recovery_group_id,
    cycle_cooldown_seconds: source.cycle_cooldown_seconds,
    harvest_transport: source.harvest_transport,
    enabled: source.enabled,
    inject_enabled: source.inject_enabled,
    inject_mode: source.inject_mode,
    inject_models: source.inject_models,
    allow_degraded_shapes: source.allow_degraded_shapes,
    inject_header: source.inject_header,
    proxy_id: source.proxy_id,
    group_ids: source.group_ids,
    account_ids: source.account_ids,
    model: source.model,
    prompt: source.prompt,
    ttl_seconds: source.ttl_seconds,
    renew_before_seconds: source.renew_before_seconds,
    poll_seconds: source.poll_seconds,
    probe_timeout_seconds: source.probe_timeout_seconds,
    max_accounts_per_tick: source.max_accounts_per_tick,
    target_state_length: source.target_state_length,
    max_attempts_per_cycle: source.max_attempts_per_cycle,
    concurrent_accounts: source.concurrent_accounts,
    retry_interval_ms: source.retry_interval_ms,
    revocation_statuses: source.revocation_statuses,
    issuance_statuses: source.issuance_statuses
  })
}

function mergeConfig(base: CodexTurnStateConfig, patch?: Partial<CodexTurnStateConfig> | null): CodexTurnStateConfig {
  return normalizeConfig({
    transfer_enabled: patch?.transfer_enabled ?? base.transfer_enabled,
    transfer_ready_group_id: patch?.transfer_ready_group_id ?? base.transfer_ready_group_id,
    transfer_recovery_group_id: patch?.transfer_recovery_group_id ?? base.transfer_recovery_group_id,
    cycle_cooldown_seconds: patch?.cycle_cooldown_seconds ?? base.cycle_cooldown_seconds,
    harvest_transport: patch?.harvest_transport ?? base.harvest_transport,
    enabled: patch?.enabled ?? base.enabled,
    inject_enabled: patch?.inject_enabled ?? base.inject_enabled,
    inject_mode: patch?.inject_mode ?? base.inject_mode,
    inject_models: patch?.inject_models ?? base.inject_models,
    allow_degraded_shapes: patch?.allow_degraded_shapes ?? base.allow_degraded_shapes,
    inject_header: patch?.inject_header ?? base.inject_header,
    proxy_id: patch?.proxy_id ?? base.proxy_id,
    group_ids: patch?.group_ids ?? base.group_ids,
    account_ids: patch?.account_ids ?? base.account_ids,
    model: patch?.model ?? base.model,
    prompt: patch?.prompt ?? base.prompt,
    ttl_seconds: patch?.ttl_seconds ?? base.ttl_seconds,
    renew_before_seconds: patch?.renew_before_seconds ?? base.renew_before_seconds,
    poll_seconds: patch?.poll_seconds ?? base.poll_seconds,
    probe_timeout_seconds: patch?.probe_timeout_seconds ?? base.probe_timeout_seconds,
    max_accounts_per_tick: patch?.max_accounts_per_tick ?? base.max_accounts_per_tick,
    target_state_length: patch?.target_state_length ?? base.target_state_length,
    max_attempts_per_cycle: patch?.max_attempts_per_cycle ?? base.max_attempts_per_cycle,
    concurrent_accounts: patch?.concurrent_accounts ?? base.concurrent_accounts,
    retry_interval_ms: patch?.retry_interval_ms ?? base.retry_interval_ms,
    revocation_statuses: patch?.revocation_statuses ?? base.revocation_statuses,
    issuance_statuses: patch?.issuance_statuses ?? base.issuance_statuses
  })
}

function toDraft(cfg: CodexTurnStateConfig): ConfigDraft {
  return {
    transfer_enabled: cfg.transfer_enabled ?? false,
    transfer_ready_group_id: cfg.transfer_ready_group_id ?? 0,
    transfer_recovery_group_id: cfg.transfer_recovery_group_id ?? 0,
    cycle_cooldown_seconds: cfg.cycle_cooldown_seconds ?? 300,
    harvest_transport: cfg.harvest_transport ?? 'account',
    enabled: cfg.enabled,
    inject_enabled: cfg.inject_enabled,
    // 旧配置里没有这两个字段（未部署前保存过的 JSON），这里按默认值补齐，
    // 否则表单会拿到 undefined。
    inject_mode: cfg.inject_mode === 'force' ? 'force' : 'fill_empty',
    inject_models: (cfg.inject_models || []).join(', '),
    allow_degraded_shapes: false,
    inject_header: cfg.inject_header,
    proxy_id: cfg.proxy_id,
    group_ids: [...(cfg.group_ids || [])],
    account_ids: (cfg.account_ids || []).join(', '),
    model: cfg.model,
    prompt: cfg.prompt,
    ttl_seconds: cfg.ttl_seconds,
    renew_before_seconds: cfg.renew_before_seconds,
    poll_seconds: cfg.poll_seconds,
    probe_timeout_seconds: cfg.probe_timeout_seconds,
    max_accounts_per_tick: cfg.max_accounts_per_tick,
    target_state_length: cfg.target_state_length ?? 0,
    max_attempts_per_cycle: cfg.max_attempts_per_cycle ?? 50,
    concurrent_accounts: cfg.concurrent_accounts ?? 16,
    retry_interval_ms: cfg.retry_interval_ms ?? 200,
    revocation_statuses: (cfg.revocation_statuses || []).join(', '),
    issuance_statuses: (cfg.issuance_statuses || []).join(', ')
  }
}

const parsedConfig = computed<CodexTurnStateConfig>(() => normalizeConfig({
  transfer_enabled: draft.value.transfer_enabled,
  transfer_ready_group_id: draft.value.transfer_ready_group_id,
  transfer_recovery_group_id: draft.value.transfer_recovery_group_id,
  cycle_cooldown_seconds: toInt(draft.value.cycle_cooldown_seconds),
  harvest_transport: draft.value.harvest_transport,
  enabled: draft.value.enabled,
  inject_enabled: draft.value.inject_enabled,
  inject_mode: draft.value.inject_mode,
  inject_models: parseModelList(draft.value.inject_models),
  allow_degraded_shapes: draft.value.allow_degraded_shapes,
  inject_header: draft.value.inject_header,
  proxy_id: draft.value.proxy_id,
  group_ids: [...draft.value.group_ids],
  account_ids: parseIDs(draft.value.account_ids).ids,
  model: draft.value.model,
  prompt: draft.value.prompt,
  ttl_seconds: toInt(draft.value.ttl_seconds),
  renew_before_seconds: toInt(draft.value.renew_before_seconds),
  poll_seconds: toInt(draft.value.poll_seconds),
  probe_timeout_seconds: toInt(draft.value.probe_timeout_seconds),
  max_accounts_per_tick: toInt(draft.value.max_accounts_per_tick),
  target_state_length: draft.value.target_state_length,
  max_attempts_per_cycle: toInt(draft.value.max_attempts_per_cycle),
  concurrent_accounts: toInt(draft.value.concurrent_accounts),
  retry_interval_ms: toInt(draft.value.retry_interval_ms),
  revocation_statuses: parseIDs(draft.value.revocation_statuses).ids,
  issuance_statuses: parseIDs(draft.value.issuance_statuses).ids
}))

const validation = computed<string[]>(() => {
  const cfg = parsedConfig.value
  const problems: string[] = []
  if (cfg.enabled && missingProxy.value) problems.push(`动态IP代理 #${cfg.proxy_id} 已删除或不可用，请重新选择代理后保存；也可先停用状态池`)
  if (cfg.enabled && !cfg.group_ids.length && !cfg.account_ids.length && !(cfg.transfer_enabled && cfg.inject_enabled)) problems.push('启用后必须至少选择一个检测分组或指定账号')
  if (cfg.transfer_enabled && (!cfg.transfer_ready_group_id || !cfg.transfer_recovery_group_id || cfg.transfer_ready_group_id === cfg.transfer_recovery_group_id)) problems.push('请选择不同的可用分组 A 和待恢复分组 B')
  if (!inRange(cfg.cycle_cooldown_seconds ?? 300, 1, 86400)) problems.push('轮次冷却需为 1–86400 秒')
  if (cfg.enabled && cfg.harvest_transport === 'independent' && !cfg.proxy_id) problems.push('独立采集通道必须选择采集代理')
  if (!inRange(cfg.ttl_seconds, 60, 86400)) problems.push('TTL 秒需为 60–86400 之间的整数')
  if (!inRange(cfg.renew_before_seconds, 0, 3600)) problems.push('提前续期秒需为 0–3600 之间的整数')
  else if (Number.isFinite(cfg.ttl_seconds) && cfg.renew_before_seconds >= cfg.ttl_seconds) problems.push('提前续期秒必须小于 TTL 秒')
  if (!inRange(cfg.poll_seconds, 5, 3600)) problems.push('轮询秒需为 5–3600 之间的整数')
  if (!inRange(cfg.probe_timeout_seconds, 10, 600)) problems.push('探测超时秒需为 10–600 之间的整数')
  if (!inRange(cfg.max_accounts_per_tick, 1, 200)) problems.push('每轮最多账号数需为 1–200 之间的整数')
  if (!inRange(cfg.concurrent_accounts ?? 16, 1, 64)) problems.push('并发采集账号数需为 1–64 之间的整数')
  if (!inRange(cfg.retry_interval_ms ?? 200, 50, 60000)) problems.push('连续采集间隔需为 50–60000 毫秒之间的整数')
  if (!inRange(cfg.max_attempts_per_cycle ?? 50, 1, 10000)) problems.push('每账号连续采集上限需为 1–10000 之间的整数')
  if (!HEADER_TOKEN.test(cfg.inject_header)) problems.push('注入请求头名称只能是字母、数字或连字符')
  const badAccountTokens = parseIDs(draft.value.account_ids).invalid
  if (badAccountTokens.length) problems.push(`指定账号需为正整数：${badAccountTokens.join('、')}`)
  const badCodeTokens = parseIDs(draft.value.revocation_statuses).invalid
  if (badCodeTokens.length) problems.push(`撤销状态码需为正整数：${badCodeTokens.join('、')}`)
  else if (cfg.revocation_statuses.some(code => !inRange(code, 100, 599))) problems.push('撤销状态码需为 100–599 之间的整数')
  const badIssuanceTokens = parseIDs(draft.value.issuance_statuses).invalid
  if (badIssuanceTokens.length) problems.push(`下发状态码需为正整数：${badIssuanceTokens.join('、')}`)
  else if (cfg.issuance_statuses.some(code => !inRange(code, 100, 599))) problems.push('下发状态码需为 100–599 之间的整数')
  return problems
})

const draftValid = computed(() => validation.value.length === 0)
const draftDirty = computed(() => {
  if (!config.value) return false
  return JSON.stringify(parsedConfig.value) !== JSON.stringify(normalizeConfig(config.value))
})

const proxyName = computed(() => overview.value?.proxy_name || proxies.value.find(p => p.id === draft.value.proxy_id)?.name || '')
const missingProxy = computed(() => !optionsLoading.value && !optionsError.value && draft.value.proxy_id > 0 && !proxies.value.some(p => p.id === draft.value.proxy_id))
const historyPages = computed(() => Math.max(1, Math.ceil((history.value?.total || 0) / (history.value?.page_size || HISTORY_PAGE_SIZE))))
const elapsedSeconds = computed(() => Math.max(0, Math.floor((now.value - loadedAt.value) / 1000)))

const proxyOptionLabel = (proxy: Proxy) => `${proxy.name} (${proxy.protocol}://${proxy.host}:${proxy.port})`
const groupLabel = (id: number) => {
  if (!id) return '—'
  const group = groups.value.find(g => g.id === id)
  return group ? `${group.name} · #${id}` : `#${id}`
}
const sourceLabel = (source: string) => ({ harvest: '真实流量回喂', probe: '主动采集', manual: '手动采集', revival: '复活继承（原到期时间）' } as Record<string, string>)[source] || '—'
const statusLabel = (status: string) => STATUS_LABELS[status] || status || '未知'
const statusClass = (status: string) => STATUS_CLASSES[status] || 'cts-badge cts-badge-amber'
const collectionLabel = (status?: string) => ({ locked: '已锁定', collecting: '连续采集中', renewing: '续期采集中', limit_reached: '达到上限', waiting: '等待采集', cooldown: '轮次冷却中' } as Record<string, string>)[status || ''] || ''
const retrySeconds = (at?: string | null) => at ? Math.max(0, Math.ceil((new Date(at).getTime() - now.value) / 1000)) : 0

function rowStatus(row: CodexTurnStateAccountRow): string {
  if (row.state_status === 'active' && config.value && row.remaining_seconds > 0 && row.remaining_seconds <= config.value.renew_before_seconds) return 'expiring_soon'
  return row.state_status || 'none'
}

function countdown(seconds: number | null | undefined, zeroLabel = '已到期'): string {
  if (seconds === null || seconds === undefined || !Number.isFinite(seconds)) return '—'
  if (seconds <= 0) return zeroLabel
  const total = Math.floor(seconds)
  const days = Math.floor(total / 86400)
  const clock = `${String(Math.floor((total % 86400) / 3600)).padStart(2, '0')}:${String(Math.floor((total % 3600) / 60)).padStart(2, '0')}:${String(total % 60).padStart(2, '0')}`
  return days > 0 ? `${days} 天 ${clock}` : clock
}

function remainingLabel(row: CodexTurnStateAccountRow): string {
  if (!row.has_state || row.state_status === 'none') return '—'
  return countdown(Math.max(0, row.remaining_seconds - elapsedSeconds.value))
}

function secondsUntil(iso?: string | null): number | null {
  if (!iso) return null
  const target = Date.parse(iso)
  if (!Number.isFinite(target)) return null
  return (target - now.value) / 1000
}

function time(value?: string | null): string {
  if (!value || value.startsWith('0001') || !Number.isFinite(Date.parse(value))) return '—'
  return new Intl.DateTimeFormat('zh-CN', { month: '2-digit', day: '2-digit', hour: '2-digit', minute: '2-digit', second: '2-digit', hour12: false }).format(new Date(value))
}

async function loadOptions() {
  optionsLoading.value = true
  const [proxyResult, groupResult] = await Promise.allSettled([getAllProxies(), getAllGroups()])
  if (disposed) return
  const failures: string[] = []
  if (proxyResult.status === 'fulfilled') proxies.value = proxyResult.value
  else failures.push('代理列表')
  if (groupResult.status === 'fulfilled') groups.value = groupResult.value
  else failures.push('分组列表')
  optionsError.value = failures.length ? `读取${failures.join(' / ')}失败，可点击刷新重试；保存不受影响。` : ''
  optionsLoading.value = false
}

async function loadHistory(page = historyPage.value) {
  historyLoading.value = true
  try {
    const result = await getCodexTurnStateHistory(page, HISTORY_PAGE_SIZE, 0)
    if (disposed) return
    history.value = result
    historyPage.value = result.page || page
    historyError.value = ''
  } catch { if (!disposed) historyError.value = '读取采集历史失败，可点击刷新重试。' }
  finally { if (!disposed) historyLoading.value = false }
}

async function clearHistory() {
  if (!confirmHistoryClear.value || clearingHistory.value) return
  clearingHistory.value = true
  historyError.value = ''
  try {
    const result = await clearCodexTurnStateHistory()
    if (disposed) return
    notice.value = `已清理 ${result.deleted} 条历史，保留 ${result.retained} 条当前状态/作废标记；采集次数不变。`
    confirmHistoryClear.value = false
    await Promise.allSettled([loadHistory(1), refresh()])
  } catch {
    if (!disposed) historyError.value = '清空历史失败；当前状态和表格仍保留，可刷新核对。'
  } finally {
    if (!disposed) clearingHistory.value = false
  }
}

async function refresh() {
  if (loading.value || saving.value) return
  loading.value = true
  try {
    const result = await getCodexTurnState()
    if (disposed) return
    const keepDraft = config.value !== null && draftDirty.value
    overview.value = result
    config.value = pickConfig(result)
    if (!keepDraft) draft.value = toDraft(result)
    loadedAt.value = Date.now()
    now.value = Date.now()
    error.value = ''
  } catch { if (!disposed) error.value = '读取状态池概览失败，保留上次结果；请稍后重试。' }
  finally { if (!disposed) loading.value = false }
}

async function reloadAll() {
  await Promise.allSettled([refresh(), loadHistory()])
}

function saveFailureMessage(reason: unknown, fallback: string): string {
  if (reason && typeof reason === 'object') {
    const failure = reason as { status?: number; message?: string; response?: { status?: number; data?: { message?: string } } }
    const status = failure.status ?? failure.response?.status
    const message = failure.response?.data?.message ?? failure.message
    if (status && message) return `保存失败：${message}`
  }
  return fallback
}

async function saveConfig() {
  if (saving.value || loading.value || !config.value || !draftValid.value || !draftDirty.value) return
  saving.value = true
  notice.value = ''
  error.value = ''
  let savedOK = false
  try {
    const saved = await saveCodexTurnStateConfig(parsedConfig.value)
    if (disposed) return
    config.value = mergeConfig(parsedConfig.value, saved)
    draft.value = toDraft(config.value)
    notice.value = '状态池配置已保存；下一轮采集按新配置执行。'
    savedOK = true
  } catch (reason) {
    if (!disposed) error.value = saveFailureMessage(reason, '未收到保存确认，请刷新核对配置；你的修改仍保留在表单中。')
  }
  finally { if (!disposed) saving.value = false }
  // refresh() 在 saving 期间会主动让路，因此保存结束后再重新读取概览。
  if (savedOK && !disposed) await Promise.allSettled([refresh(), loadHistory()])
}

async function toggleEnabled(next: boolean) {
  if (!config.value || saving.value || loading.value) return
  if (next && missingProxy.value) {
    error.value = `动态IP代理 #${draft.value.proxy_id} 已删除或不可用，请重新选择代理后保存。`
    return
  }
  const previous = config.value.enabled
  draft.value.enabled = next
  saving.value = true
  notice.value = ''
  error.value = ''
  try {
    const saved = await saveCodexTurnStateConfig(mergeConfig(config.value, { enabled: next }))
    if (disposed) return
    config.value = mergeConfig(config.value, { ...saved, enabled: next })
    draft.value.enabled = config.value.enabled
    notice.value = next ? '已启用 292 状态注入。' : '已停用 292 状态注入，不再采集或注入。'
  } catch (reason) {
    if (disposed) return
    draft.value.enabled = previous
    error.value = saveFailureMessage(reason, '未收到切换确认，已还原开关显示；请刷新核对配置。')
  } finally { if (!disposed) saving.value = false }
}

async function collect(accountId: number) {
  if (collecting.value) return
  collecting.value = true
  collectingId.value = accountId
  notice.value = ''
  error.value = ''
  try {
    const result = await collectCodexTurnState(accountId)
    if (disposed) return
    notice.value = result.queued > 0
      ? `已排队 ${result.queued} 个账号的状态采集任务。`
      : accountId > 0 ? '该账号已排队或正在连续采集，不会重复启动。' : '账号已排队或正在连续采集，无需重复启动。'
    await Promise.allSettled([refresh(), loadHistory()])
  } catch { if (!disposed) error.value = '排队采集失败，未产生采集任务；请稍后重试。' }
  finally { if (!disposed) { collecting.value = false; collectingId.value = 0 } }
}

async function invalidate(row: CodexTurnStateAccountRow) {
  if (invalidatingId.value) return
  invalidatingId.value = row.account_id
  notice.value = ''
  error.value = ''
  try {
    await invalidateCodexTurnState(row.account_id)
    if (disposed) return
    confirmingId.value = 0
    notice.value = `已作废 ${row.account_name || `账号 #${row.account_id}`} 的缓存状态。`
    await Promise.allSettled([refresh(), loadHistory()])
  } catch { if (!disposed) error.value = '作废失败，缓存状态可能仍然生效；请刷新核对。' }
  finally { if (!disposed) invalidatingId.value = 0 }
}

function changeHistoryPage(delta: number) {
  const next = historyPage.value + delta
  if (next < 1 || next > historyPages.value) return
  void loadHistory(next)
}

let disposed = false
let ticker: ReturnType<typeof setInterval> | undefined
let poll: ReturnType<typeof setInterval> | undefined
onMounted(() => {
  void reloadAll()
  void loadOptions()
  ticker = setInterval(() => { now.value = Date.now() }, 1000)
  poll = setInterval(() => { if (!document.hidden) void refresh() }, 30000)
})
onUnmounted(() => { disposed = true; clearInterval(ticker); clearInterval(poll) })
</script>

<style scoped>
.cts-page { @apply mx-auto flex max-w-7xl flex-col gap-5 pb-8 text-gray-900 dark:text-gray-100; }
.cts-panel { @apply min-w-0 rounded-2xl border border-gray-200 bg-white p-5 shadow-sm dark:border-dark-700 dark:bg-dark-800; }
.cts-label { @apply block text-xs font-medium text-gray-500 dark:text-gray-400; }
.cts-alert { @apply rounded-xl border border-amber-200 bg-amber-50 p-4 text-sm leading-6 text-amber-900 dark:border-amber-900 dark:bg-amber-950/40 dark:text-amber-200; }
.cts-notice { @apply rounded-xl border border-emerald-200 bg-emerald-50 p-4 text-sm leading-6 text-emerald-900 dark:border-emerald-900 dark:bg-emerald-950/40 dark:text-emerald-200; }
.cts-table { @apply w-full whitespace-nowrap text-left text-sm; }
.cts-table th { @apply border-b border-gray-200 px-3 py-3 text-xs font-medium text-gray-500 dark:border-dark-700 dark:text-gray-400; }
.cts-table td { @apply border-b border-gray-100 px-3 py-3 align-middle dark:border-dark-700; }
.cts-badge { @apply inline-flex items-center rounded-full px-2.5 py-1 text-xs font-medium; }
.cts-badge-green { @apply bg-emerald-50 text-emerald-700 dark:bg-emerald-950/50 dark:text-emerald-300; }
.cts-badge-amber { @apply bg-amber-50 text-amber-800 dark:bg-amber-950/50 dark:text-amber-300; }
.cts-badge-red { @apply bg-red-50 text-red-700 dark:bg-red-950/50 dark:text-red-300; }
.cts-badge-gray { @apply bg-gray-100 text-gray-600 dark:bg-dark-700 dark:text-gray-300; }
</style>
