<template>
  <AppLayout>
    <div class="runway-page">
      <header class="flex flex-wrap items-start justify-between gap-4">
        <div>
          <p class="mb-2 text-xs font-semibold tracking-widest text-primary-600 dark:text-primary-400">管理员专属 · 账号数据只读</p>
          <h1 class="text-3xl font-semibold tracking-tight">号池续航</h1>
          <p class="mt-2 text-sm text-gray-500 dark:text-gray-400">实测余量与规划假设分开，了解当前库存还能维持多久。</p>
        </div>
        <button class="btn btn-secondary min-h-11" :disabled="loading || saving" @click="refresh()">
          <Icon name="refresh" size="sm" class="mr-2" />{{ loading ? '读取中…' : '刷新快照' }}
        </button>
      </header>
      <div class="runway-panel flex flex-wrap items-end gap-5">
        <form class="mr-auto w-full min-w-0 lg:w-auto lg:max-w-lg" @submit.prevent="saveGroup">
          <label for="runway-group" class="runway-label">监控分组</label>
          <div class="mt-2 flex flex-wrap gap-2">
            <select id="runway-group" v-model.number="draftGroup" class="input min-h-11 min-w-0 flex-1" :disabled="loading || saving || !config" @change="notice = ''">
              <option :value="0" disabled>{{ configLoading ? '正在读取分组…' : '请选择 OpenAI 分组' }}</option>
              <option v-if="draftGroup > 0 && !config?.groups.some(g => g.id === draftGroup)" :value="draftGroup" disabled>分组 #{{ draftGroup }} 已不可用，请重新选择</option>
              <option v-for="group in config?.groups || []" :key="group.id" :value="group.id">{{ group.name }} · #{{ group.id }}</option>
            </select>
            <button type="submit" class="btn btn-primary min-h-11 shrink-0" :disabled="loading || saving || !validGroup || !groupDirty">
              {{ saving ? '正在保存…' : '保存分组' }}
            </button>
          </div>
          <p class="mt-2 text-xs leading-5 text-gray-500 dark:text-gray-400">当前监控：{{ snapshot?.group.name || config?.effective_group.name || '尚未配置' }}<span v-if="groupDirty" class="ml-2 text-amber-700 dark:text-amber-300">有未保存修改</span></p>
          <p class="mt-1 text-xs leading-5 text-gray-500 dark:text-gray-400">只切换监控对象，不移动账号。保存后下一个 5 分钟时间桶采样，无需重启；各组历史独立。</p>
        </form>
        <div>
          <label for="runway-policy" class="runway-label">估算策略</label>
          <select id="runway-policy" v-model="policy" class="input mt-2 min-h-11" :disabled="loading || saving" @change="changePolicy">
            <option value="unknown_full_v1">未知按满额 · 规划口径</option>
            <option value="known_only">仅计算已知部分</option>
          </select>
        </div>
        <div>
          <label for="runway-zone" class="runway-label">显示时区</label>
          <select id="runway-zone" v-model="zone" class="input mt-2 min-h-11">
            <option value="Asia/Shanghai">Asia/Shanghai</option><option value="UTC">UTC</option><option value="America/New_York">America/New_York</option>
          </select>
        </div>
      </div>
      <p v-if="configError" role="alert" class="runway-alert">{{ configError }}</p>
      <p v-if="notice" role="status" class="rounded-xl border border-emerald-200 bg-emerald-50 p-4 text-sm text-emerald-900 dark:border-emerald-900 dark:bg-emerald-950/40 dark:text-emerald-200">{{ notice }}</p>
      <p v-if="error" role="alert" class="runway-alert">{{ error }}，保留最后成功快照；历史估计 / 等待更新。</p>
      <p v-if="snapshot?.collection_error" role="alert" class="runway-alert">{{ snapshot.collection_error }}</p>
      <div v-if="!snapshot" class="runway-panel py-16 text-center text-gray-500" role="status">{{ loading ? '正在读取监控快照…' : '暂无监控数据' }}</div>
      <template v-else>
        <section class="grid gap-3 text-sm sm:grid-cols-2 xl:grid-cols-4" aria-label="采集状态">
          <div class="runway-panel"><p class="runway-label">计算频率</p><p class="mt-2">每 5 分钟 · 墙钟对齐</p></div>
          <div class="runway-panel"><p class="runway-label">最近成功</p><p class="mt-2 tabular-nums">{{ time(snapshot.generated_at) }}</p></div>
          <div class="runway-panel"><p class="runway-label">下次预计</p><p class="mt-2 tabular-nums">{{ time(snapshot.next_calculation_at) }}</p></div>
          <div class="runway-panel"><p class="runway-label">快照年龄 / 状态</p><p class="mt-2" aria-live="polite">{{ snapshot.generated_at ? `${Math.max(0, Math.floor(age / 60))} 分钟` : '尚未采样' }} · {{ stale ? '历史估计 / 等待更新' : stateLabel }}</p></div>
        </section>
        <div v-if="!data" class="runway-panel py-16 text-center">
          <h2 class="text-xl font-semibold">{{ snapshot.group.id ? '积累样本中' : '请选择监控分组' }}</h2>
          <p class="mt-3 text-sm text-gray-500">{{ snapshot.group.id ? '定时任务需要 5 个采样点形成完整 20 分钟区间。刷新页面不会触发采集或上游查询。' : '在上方选择一个 OpenAI 分组并保存，采样将在下一个时间桶开始。' }}</p>
        </div>
        <template v-else>
          <section class="runway-panel relative overflow-hidden" aria-labelledby="runway-eta">
            <div class="absolute inset-y-0 left-0 w-1 bg-primary-500" />
            <div class="flex flex-wrap justify-between gap-5">
              <div>
                <h2 id="runway-eta" class="text-sm font-medium text-gray-600 dark:text-gray-300">{{ data.forecast_policy === 'unknown_full_v1' ? '预计可用至（未知按满额估算）' : '预计可用至（仅计算已知部分）' }}</h2>
                <p class="mt-4 text-3xl font-semibold tracking-tight sm:text-4xl">{{ etaLabel }}</p>
                <p class="mt-3 text-sm text-gray-500 dark:text-gray-400">剩余时长 <b class="ml-2 text-gray-900 dark:text-white">{{ !stale && data.forecast.status === 'estimated' ? duration(data.forecast.remaining_hours) : '—' }}</b></p>
              </div>
              <div class="max-w-md">
                <span class="inline-block rounded-full bg-amber-50 px-3 py-1.5 text-xs font-semibold text-amber-800 dark:bg-amber-950 dark:text-amber-300">{{ data.forecast.confidence === 'low' ? '低置信度' : '规划参考' }}</span>
                <p class="mt-3 text-xs leading-6 text-gray-500 dark:text-gray-400">最近完整 20 分钟<br>{{ time(data.burn.window_start_at) }} → {{ time(data.burn.window_end_at) }}</p>
              </div>
            </div>
            <p class="mt-5 border-t border-gray-100 pt-4 text-xs leading-6 text-gray-500 dark:border-dark-700 dark:text-gray-400">
              {{ data.forecast_policy === 'unknown_full_v1' ? 'S = K + A；剩余小时 = S ÷ R。未知满额是规划假设，不是实测额度。' : '剩余小时 = K ÷ R；仅估算已知部分，不代表整个号池耗尽时间。' }}
              账号当量，按当前套餐组合估计；不计未来补号、重置回补或恢复，不保证未来可用性。
            </p>
          </section>
          <p v-if="data.forecast.confidence === 'low'" class="runway-alert">实测覆盖不足，预测可能偏乐观。已知额度覆盖 {{ num(data.week.coverage_percent, 1) }}%，当前配对覆盖 {{ num(data.burn.coverage_percent, 1) }}%；两种口径并非置信区间。</p>
          <div class="grid grid-cols-2 gap-3 xl:grid-cols-3">
            <div v-for="metric in metrics" :key="metric.label" class="runway-panel">
              <p class="runway-label">{{ metric.label }}</p>
              <p class="mt-3 text-3xl font-semibold tabular-nums">{{ metric.value }}<span class="ml-1 text-xs font-normal text-gray-500">{{ metric.unit }}</span></p>
              <p class="mt-2 text-xs text-gray-500 dark:text-gray-400">{{ metric.note }}</p>
            </div>
          </div>
          <section class="runway-panel">
            <div class="flex flex-wrap items-start justify-between gap-3">
              <div><h2 class="font-semibold">真实消耗 · 四段累加</h2><p class="mt-2 text-sm text-gray-500">20 分钟消耗 {{ num(data.burn.delta_equivalents) }} 当量 × 3 = {{ num(data.burn.equivalents_per_hour) }} 当量 / 小时</p></div>
              <span class="text-sm">{{ data.burn.complete ? '完整 20 分钟' : '缺少采样点，不生成预测' }}</span>
            </div>
            <div class="mt-5 grid grid-cols-2 gap-3 lg:grid-cols-4">
              <div v-for="(segment, index) in data.burn.segments" :key="segment.start_at" class="rounded-xl bg-gray-50 p-4 dark:bg-dark-900">
                <p class="runway-label">区间 {{ index + 1 }} · {{ clock(segment.start_at) }} – {{ clock(segment.end_at) }}</p>
                <p class="mt-2 text-xl font-semibold tabular-nums">{{ num(segment.delta_equivalents, 4) }} <span class="text-xs font-normal">当量</span></p>
                <p class="mt-2 text-xs text-gray-500">配对 {{ segment.paired_accounts }} · 排除重置 {{ segment.reset_pairs_excluded }}</p>
              </div>
            </div>
            <p class="mt-5 text-xs leading-6 text-gray-500 dark:text-gray-400">去重配对 {{ data.burn.paired_accounts }} · 当前已知候选配对 {{ data.burn.current_paired_accounts }} · 发生消耗 {{ data.burn.consuming_accounts }} · 观测更新 {{ data.burn.updated_accounts }} · 排除重置区间 {{ data.burn.reset_pairs_excluded }} · 时间戳异常 {{ data.burn.anomalous_pairs }}。重置或缺失区间的消费未覆盖，不外推。</p>
          </section>
          <div class="grid gap-5 xl:grid-cols-2">
            <section class="runway-panel">
              <h2 class="font-semibold">额度质量与去重</h2>
              <dl class="mt-4 space-y-3 text-sm">
                <div class="flex justify-between"><dt>7 天已知 / 未知（身份数）</dt><dd>{{ data.week.known_accounts }} / {{ data.week.unknown_accounts }}</dd></div>
                <div class="flex justify-between"><dt>空闲旧缓存</dt><dd>{{ data.week.idle_cached_accounts }}</dd></div>
                <div class="flex justify-between"><dt>重复记录折叠</dt><dd>{{ data.duplicate_records_collapsed }}</dd></div>
                <div v-for="(count, reason) in data.week.unknown_reasons" :key="reason" class="flex justify-between"><dt>{{ reasonLabel(String(reason)) }}</dt><dd>{{ count }}</dd></div>
              </dl>
              <p class="mt-4 text-xs leading-6 text-gray-500">空闲缓存基于本地最后使用记录，不证明外部没有消费。身份缺失或冲突不合并、不推算库存。</p>
            </section>
            <section class="runway-panel">
              <h2 class="font-semibold">候选排除原因</h2>
              <dl class="mt-4 grid grid-cols-2 gap-x-6 gap-y-3 text-sm">
                <div v-for="(count, reason) in data.excluded_records" :key="reason" class="flex justify-between gap-2"><dt>{{ reasonLabel(String(reason)) }}</dt><dd>{{ count }}</dd></div>
              </dl>
              <p v-if="!Object.keys(data.excluded_records).length" class="mt-4 text-sm text-gray-500">未发现排除记录</p>
              <p class="mt-4 text-xs leading-6 text-gray-500">按数据库记录数计数；身份异常可能与静态排除原因重叠。候选不代表请求已验证成功，瞬时满并发不扣库存。</p>
            </section>
          </div>
          <section class="runway-panel flex flex-wrap justify-between gap-5">
            <div><h2 class="font-semibold">5 小时辅助额度</h2><p class="mt-2 text-sm text-gray-500">独立展示，不与周额度相加，未知不按满额。</p></div>
            <p class="text-sm leading-7">已知余量 <b>{{ num(data.five_hour.known_remaining_equivalents) }}</b> 当量<br>已知 {{ data.five_hour.known_accounts }} / 未知 {{ data.five_hour.unknown_accounts }}</p>
            <p class="max-w-sm text-xs leading-6 text-gray-500">{{ data.mixed_plans ? '检测到混合套餐。' : '' }}不同套餐的 100% 不代表相同请求量、Token 或金额。当前统一权重为 1，没有套餐换算。</p>
          </section>
          <section class="runway-panel">
            <h2 class="font-semibold">最近 24 小时</h2>
            <p class="mt-2 text-xs text-gray-500">口径 {{ data.forecast_policy }} · {{ data.method_version }}；缺测与版本变化断线，不跨口径连接。</p>
            <div class="mt-5 grid gap-6 lg:grid-cols-3">
              <div v-for="chart in charts" :key="chart.title" class="min-w-0">
                <h3 class="text-sm font-medium">{{ chart.title }}</h3>
                <svg viewBox="0 0 320 120" class="mt-3 w-full text-primary-600 dark:text-primary-400" role="img" :aria-label="chart.title">
                  <path d="M12 10V105H308" fill="none" stroke="currentColor" opacity=".2" />
                  <polyline v-for="(line, index) in chart.lines" :key="index" :points="line" fill="none" stroke="currentColor" stroke-width="2" vector-effect="non-scaling-stroke" />
                  <circle v-for="(dot,index) in chart.dots" :key="`dot-${index}`" :cx="dot.x" :cy="dot.y" r="2" fill="currentColor"><title>{{ dot.label }}</title></circle>
                  <text x="16" y="16" fill="currentColor" font-size="9">{{ num(chart.max) }}</text>
                  <text x="16" y="102" fill="currentColor" font-size="9">0</text>
                </svg>
                <p class="flex justify-between text-xs text-gray-500"><span>24 小时前</span><span>最近计算</span></p>
              </div>
            </div>
            <details class="mt-5 text-sm">
              <summary class="cursor-pointer py-3">查看历史数值（{{ snapshot.history.length }} 条）</summary>
              <div class="max-h-80 overflow-auto">
                <table class="runway-table"><thead><tr><th>时间</th><th>库存当量</th><th>当量 / 小时</th><th>续航</th><th>口径 / 版本</th></tr></thead>
                  <tbody><tr v-for="row in [...snapshot.history].reverse()" :key="row.generated_at + row.method_version"><td>{{ time(row.generated_at) }}</td><td>{{ num(row.week.remaining_equivalents) }}</td><td>{{ row.burn.complete ? num(row.burn.equivalents_per_hour) : '—' }}</td><td>{{ duration(row.forecast.remaining_hours) }}</td><td>{{ row.forecast_policy }} / {{ row.method_version }}</td></tr></tbody>
                </table>
              </div>
            </details>
          </section>
          <section class="runway-panel">
            <div class="flex flex-wrap items-center justify-between gap-4">
              <div><h2 class="font-semibold">导入批次</h2><p class="mt-2 text-xs text-gray-500">同身份最早可信创建分钟；保留监控期间已见最早归属，重导不后移。</p></div>
              <div class="flex items-center gap-3 text-sm"><button class="btn btn-secondary min-h-11" :disabled="page <= 1" @click="page--">上一页</button><span>{{ page }} / {{ pages }}</span><button class="btn btn-secondary min-h-11" :disabled="page >= pages" @click="page++">下一页</button></div>
            </div>
            <div class="mt-4 overflow-x-auto">
              <table class="runway-table"><thead><tr><th>批次时间</th><th>候选账号</th><th>已知</th><th>未知</th><th>库存当量</th></tr></thead><tbody>
                <tr v-for="batch in batches" :key="batch.at"><td>{{ time(batch.at) }}</td><td>{{ batch.candidates }}</td><td>{{ batch.known }}</td><td>{{ batch.unknown }}</td><td>{{ num(batch.remaining_equivalents) }}</td></tr>
                <tr v-if="!batches.length"><td colspan="5" class="text-center text-gray-500">暂无候选批次</td></tr>
              </tbody></table>
            </div>
          </section>
        </template>
      </template>
    </div>
  </AppLayout>
</template>

<script setup lang="ts">
import { computed, onMounted, onUnmounted, ref } from 'vue'
import AppLayout from '@/components/layout/AppLayout.vue'
import Icon from '@/components/icons/Icon.vue'
import { getPoolRunway, getPoolRunwayConfig, savePoolRunwayConfig, type RunwayConfig, type RunwayPolicy, type RunwayResult, type RunwaySnapshot } from '@/api/poolRunway'

const snapshot = ref<RunwaySnapshot | null>(null)
const policy = ref<RunwayPolicy>('unknown_full_v1')
const zone = ref('Asia/Shanghai')
const loading = ref(false), error = ref(''), page = ref(1), now = ref(Date.now())
const config = ref<RunwayConfig | null>(null), draftGroup = ref(0)
const configLoading = ref(false), configError = ref(''), saving = ref(false), notice = ref('')
const savedGroup = computed(() => config.value?.group_id || config.value?.effective_group.id || 0)
const groupDirty = computed(() => draftGroup.value !== savedGroup.value)
const validGroup = computed(() => !!config.value?.groups.some(g => g.id === draftGroup.value))
const data = computed(() => snapshot.value?.data)
const age = computed(() => snapshot.value?.generated_at ? (now.value - Date.parse(snapshot.value.generated_at)) / 1000 : 0)
const stale = computed(() => !!error.value || !snapshot.value?.available || snapshot.value.stale || age.value > 720 || age.value < -60 || !Number.isFinite(age.value))
const states: Record<string, string> = { empty_pool: '没有调度候选', unknown_quota: '额度未知', warming: '积累样本中', insufficient_samples: '有效配对不足', no_observed_burn: '未观测到正消耗', exhausted: '已知额度耗尽', estimated: '规划估计' }
const stateLabel = computed(() => states[data.value?.forecast.status || 'warming'] || '等待更新')
const etaLabel = computed(() => stale.value ? '历史估计 / 等待更新' : data.value?.forecast.status !== 'estimated' ? stateLabel.value : data.value.forecast.over_seven_days ? '超过 7 天' : time(data.value.forecast.available_until))
const num = (v: number, digits = 2) => Number.isFinite(v) ? v.toLocaleString('zh-CN', { maximumFractionDigits: digits }) : '—'
function time(v?: string | null) { if (!v || v.startsWith('0001') || !Number.isFinite(Date.parse(v))) return '—'; return new Intl.DateTimeFormat('zh-CN', { timeZone: zone.value, month: '2-digit', day: '2-digit', hour: '2-digit', minute: '2-digit', hour12: false }).format(new Date(v)) }
function clock(v: string) { return new Intl.DateTimeFormat('zh-CN', { timeZone: zone.value, hour: '2-digit', minute: '2-digit', hour12: false }).format(new Date(v)) }
function duration(h: number | null) { if (h === null || !Number.isFinite(h)) return '—'; if (h > 168) return '超过 7 天'; const m = Math.round(h * 60); return `${Math.floor(m / 60)} 小时 ${m % 60} 分钟` }
const reasons: Record<string, string> = { missing: '缺少额度', invalid_percent: '使用率异常', wrong_window: '额度窗口不匹配', invalid_updated_at: '更新时间异常', reset_unknown_or_elapsed: '重置未知或已到期', stale: '使用后缓存过期', conflicting_observation: '同时间观测冲突', unreliable_identity: '身份缺失或冲突', other_platform: '其他平台', key_or_other_type: 'Key / 其他账号类型', shadow: '影子账号', disabled: '停用', error: '错误 / 401', not_schedulable: '关闭调度', missing_credential: '缺少必要凭据', expired: '到期自动暂停', rate_limit: '限流冷却', overload: '过载冷却', temporary: '临时不可调度', proxy_unavailable: '代理不可用' }
const reasonLabel = (r: string) => reasons[r] || r
const metrics = computed(() => {
  const d = data.value; if (!d) return []
  return [
    { label: '调度候选', value: num(d.candidates, 0), unit: '个', note: '按逻辑身份去重' },
    { label: '已知库存 K', value: num(d.week.known_remaining_equivalents), unit: '当量', note: `${d.week.known_accounts} 个已知账号` },
    { label: '假设库存 A', value: num(d.week.assumed_remaining_equivalents), unit: '当量', note: `${d.week.unknown_accounts} 个未知账号 · 非实测` },
    { label: '采用库存 S', value: num(d.week.remaining_equivalents), unit: '当量', note: '实测 + 当前策略的假设库存' },
    { label: '每小时消耗 R', value: d.burn.complete ? num(d.burn.equivalents_per_hour) : '—', unit: '当量 / h', note: '仅来自最近 20 分钟真实配对' },
    { label: '额度已知覆盖率', value: num(d.week.coverage_percent, 1), unit: '%', note: `当前配对覆盖 ${num(d.burn.coverage_percent, 1)}%` }
  ]
})
const pages = computed(() => Math.max(1, Math.ceil((data.value?.batches.length || 0) / 20)))
const batches = computed(() => data.value?.batches.slice((page.value - 1) * 20, page.value * 20) || [])
const charts = computed(() => {
  const history = snapshot.value?.history || []
  const end = Date.parse(snapshot.value?.generated_at || '') || now.value
  return [
    { title: '库存 · 账号当量', value: (r: RunwayResult) => r.week.remaining_equivalents },
    { title: '消耗速度 · 当量 / 小时', value: (r: RunwayResult) => r.burn.complete ? r.burn.equivalents_per_hour : null },
    { title: '预测续航 · 小时（上限 168）', value: (r: RunwayResult) => r.forecast.remaining_hours === null ? null : Math.min(168, r.forecast.remaining_hours) }
  ].map(c => {
    const max = Math.max(1, ...history.map(r => c.value(r) || 0))
    const lines: string[] = [], dots: { x: number; y: number; label: string }[] = []
    let line: string[] = [], previous: RunwayResult | undefined
    for (const r of history) {
      const value = c.value(r)
      if (value === null || r.method_version !== data.value?.method_version || r.forecast_policy !== data.value?.forecast_policy) { if (line.length) lines.push(line.join(' ')); line = []; previous = undefined; continue }
      if (previous && Date.parse(r.generated_at) - Date.parse(previous.generated_at) > 360000) { lines.push(line.join(' ')); line = [] }
      const x = 12 + Math.max(0, (Date.parse(r.generated_at) - (end - 86400000)) / 86400000) * 296, y = 105 - value / max * 85
      line.push(`${x},${y}`); dots.push({ x, y, label: `${time(r.generated_at)} · ${num(value)}` }); previous = r
    }
    if (line.length) lines.push(line.join(' '))
    return { title: c.title, lines, dots, max }
  })
})
let disposed = false, ticker: ReturnType<typeof setInterval> | undefined, poll: ReturnType<typeof setInterval> | undefined
async function loadConfig() {
  configLoading.value = true
  try {
    const result = await getPoolRunwayConfig()
    if (!disposed) {
      const keepDraft = groupDirty.value
      config.value = result
      if (!keepDraft) draftGroup.value = savedGroup.value
      configError.value = ''
    }
  } catch { if (!disposed) configError.value = '读取分组配置失败，请点击刷新重试；未更改监控对象。' }
  finally { if (!disposed) configLoading.value = false }
}
async function refresh(afterSave = false) {
  if (loading.value || (saving.value && !afterSave)) return
  loading.value = true
  try {
    const [read] = await Promise.allSettled([getPoolRunway(policy.value), loadConfig()])
    if (read.status === 'rejected') throw read.reason
    const result = read.value
    if (!disposed) { snapshot.value = result; error.value = ''; now.value = Date.now(); page.value = Math.min(page.value, pages.value) }
  }
  catch { if (!disposed) error.value = '读取失败' }
  finally { if (!disposed) loading.value = false }
}
async function saveGroup() {
  if (saving.value || loading.value || !config.value || !validGroup.value || !groupDirty.value) return
  saving.value = true; configError.value = ''; notice.value = ''
  try {
    const result = await savePoolRunwayConfig(draftGroup.value, config.value.revision)
    if (disposed) return
    config.value = { ...config.value, group_id: result.group_id, revision: result.revision, source: 'page', effective_group: config.value.groups.find(g => g.id === result.group_id)! }
    draftGroup.value = result.group_id
    // Never relabel an old group's inventory as the newly selected group's.
    snapshot.value = null; page.value = 1
    notice.value = '监控分组已保存，下一个 5 分钟时间桶开始采样；未修改账号或复活配置。'
    await refresh(true)
  } catch (e) {
    if (!disposed) {
      const status = (e as { status?: number; response?: { status?: number } }).status || (e as { response?: { status?: number } }).response?.status
      configError.value = status === 409 ? '配置已变更或正在采样，请点击刷新后重新确认并保存。' : '保存未确认，请点击刷新核对当前监控分组后重试。'
      // An interrupted HTTP response does not prove the transaction did not commit.
      snapshot.value = null
    }
  } finally { if (!disposed) saving.value = false }
}
function changePolicy() { page.value = 1; void refresh() }
onMounted(() => { void refresh(); ticker = setInterval(() => { now.value = Date.now() }, 10000); poll = setInterval(() => { if (!document.hidden) void refresh() }, 60000) })
onUnmounted(() => { disposed = true; clearInterval(ticker); clearInterval(poll) })
</script>

<style scoped>
.runway-page { @apply mx-auto flex max-w-7xl flex-col gap-5 pb-8 text-gray-900 dark:text-gray-100; }
.runway-panel { @apply min-w-0 rounded-2xl border border-gray-200 bg-white p-5 shadow-sm dark:border-dark-700 dark:bg-dark-800; }
.runway-label { @apply block text-xs font-medium text-gray-500 dark:text-gray-400; }
.runway-alert { @apply rounded-xl border border-amber-200 bg-amber-50 p-4 text-sm leading-6 text-amber-900 dark:border-amber-900 dark:bg-amber-950/40 dark:text-amber-200; }
.runway-table { @apply w-full whitespace-nowrap text-left text-sm; }
.runway-table th { @apply border-b border-gray-200 px-3 py-3 text-xs font-medium text-gray-500 dark:border-dark-700 dark:text-gray-400; }
.runway-table td { @apply border-b border-gray-100 px-3 py-3 tabular-nums dark:border-dark-700; }
</style>
