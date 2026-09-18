<template>
  <AppLayout>
    <div class="wish-page">
      <header class="flex flex-wrap items-start justify-between gap-5">
        <div>
          <div class="mb-2 flex items-center gap-2 text-xs font-semibold uppercase tracking-[.16em] text-primary-600 dark:text-primary-400">
            <Icon name="shield" size="sm" /> 管理员工作台 · 美东账号池
          </div>
          <h1 class="text-3xl font-semibold tracking-tight text-gray-950 dark:text-white">WishTeam<span class="text-primary-600 dark:text-primary-400">5X</span></h1>
          <p class="mt-2 text-sm text-gray-500 dark:text-gray-400">定时巡查子号，复活失效凭据，完整保留你的账号配置。</p>
        </div>
        <div class="flex items-center gap-3">
          <span class="wish-badge" :class="overview?.config.enabled ? 'wish-good' : 'wish-neutral'">
            <span class="h-1.5 w-1.5 rounded-full bg-current" />
            {{ overview?.config.enabled ? '自动巡查已开启' : '自动巡查已暂停' }}
          </span>
          <button class="btn btn-secondary min-h-11" :disabled="loading || busy" @click="refresh(true)">
            <Icon name="refresh" size="sm" class="mr-2" />刷新
          </button>
        </div>
      </header>

      <div v-if="error" role="alert" class="rounded-xl border border-red-200 bg-red-50 p-4 text-sm text-red-800 dark:border-red-900 dark:bg-red-950/30 dark:text-red-300">
        {{ error }} <span v-if="updatedAt">显示的是 {{ formatTime(updatedAt) }} 的数据。</span>
      </div>
      <div v-if="notice" role="status" class="rounded-xl border border-emerald-200 bg-emerald-50 p-4 text-sm text-emerald-800 dark:border-emerald-900 dark:bg-emerald-950/30 dark:text-emerald-300">{{ notice }}</div>
      <div v-if="!overview && loading" class="wish-panel py-16 text-center text-gray-500" role="status">正在读取监控配置与任务记录…</div>

      <template v-if="overview">
        <div class="grid grid-cols-2 gap-3 xl:grid-cols-4">
          <div v-for="metric in metrics" :key="metric.label" class="wish-panel">
            <p class="text-xs font-medium text-gray-500 dark:text-gray-400">{{ metric.label }}</p>
            <div class="mt-3 flex items-baseline gap-2">
              <strong class="text-3xl font-semibold tabular-nums" :class="metric.color">{{ metric.value }}</strong>
              <span class="text-xs text-gray-500 dark:text-gray-400">{{ metric.hint }}</span>
            </div>
          </div>
        </div>

        <div class="grid items-start gap-5 xl:grid-cols-[minmax(290px,360px)_minmax(0,1fr)]">
          <section class="wish-panel" aria-labelledby="wish-config-title">
            <div class="mb-6 flex items-center justify-between">
              <h2 id="wish-config-title" class="font-semibold">监控分组设置</h2>
              <span v-if="dirty" class="text-xs text-amber-700 dark:text-amber-400">有未保存修改</span>
              <Icon v-else name="cog" size="sm" class="text-gray-400" />
            </div>
            <form @submit.prevent="requestSave">
              <label for="wish-group" class="wish-label">监控分组</label>
              <select id="wish-group" v-model.number="draft.group_id" class="input min-h-11" :disabled="busy">
                <option :value="0" disabled>选择一个 OpenAI 分组</option>
                <option v-for="g in overview.groups" :key="g.id" :value="g.id">{{ g.name }} · {{ g.count }} 个子号</option>
              </select>
              <p class="mt-2 text-xs leading-5 text-gray-500 dark:text-gray-400">巡查该分组所有 OpenAI OAuth 主账号。重名邮箱会跳过，Spark 影子号不重复提交。</p>
              <label for="wish-interval" class="wish-label mt-5">巡查间隔（分钟）</label>
              <input id="wish-interval" v-model.number="draft.interval_minutes" type="number" min="1" max="1440" step="1" required class="input min-h-11" :disabled="busy" />
              <div class="mt-5 flex items-start gap-3 rounded-xl bg-gray-50 p-3 dark:bg-dark-900">
                <input id="wish-enabled" v-model="draft.enabled" type="checkbox" class="mt-1 h-4 w-4 accent-primary-600" :disabled="busy" />
                <label for="wish-enabled" class="text-sm leading-6">
                  <span class="block font-medium">开启自动巡查与复活</span>
                  <span class="text-xs text-gray-500 dark:text-gray-400">暂停只停止新轮次，正在执行的任务会继续完成。</span>
                </label>
              </div>
              <p v-if="validation" class="mt-3 text-sm text-red-600 dark:text-red-400" role="alert">{{ validation }}</p>
              <button type="submit" class="btn btn-primary mt-5 min-h-11 w-full" :disabled="busy || !dirty">
                {{ busy ? '正在保存…' : '保存监控设置' }}
              </button>
            </form>
            <dl class="mt-6 space-y-3 border-t border-gray-100 pt-5 text-xs dark:border-dark-700">
              <div class="flex justify-between gap-2"><dt class="text-gray-500 dark:text-gray-400">下轮巡查</dt><dd class="text-right">{{ overview.config.enabled ? formatTime(overview.config.next_run_at) : '未启用' }}</dd></div>
              <div class="flex justify-between gap-2"><dt class="text-gray-500 dark:text-gray-400">上游单批上限</dt><dd>200 个 · 自动分批</dd></div>
              <div class="flex justify-between gap-2"><dt class="text-gray-500 dark:text-gray-400">任务刷新频率</dt><dd>执行中每 1.5 秒</dd></div>
            </dl>
          </section>

          <section class="wish-panel min-w-0" aria-labelledby="wish-progress-title">
            <div class="flex flex-wrap items-center justify-between gap-3">
              <div>
                <h2 id="wish-progress-title" class="font-semibold">任务进度</h2>
                <p class="mt-1 text-xs text-gray-500 dark:text-gray-400">{{ selected ? `任务 #${selected.id} · 分组 #${selected.group_id} · ${formatTime(selected.created_at)}` : '保存分组后，即可开始第一轮巡查' }}</p>
              </div>
              <button class="btn btn-secondary min-h-11" :disabled="busy || running || dirty || !overview.config.group_id" @click="confirmation = 'run'">
                <Icon name="play" size="sm" class="mr-2" />立即巡查
              </button>
            </div>
            <div v-if="selected" class="mt-6">
              <div class="mb-3 flex items-center justify-between gap-2 text-sm">
                <span class="wish-badge" :class="selected.status === 'running' ? 'wish-blue' : 'wish-good'">{{ selected.status === 'running' ? '正在巡查' : '巡查结束' }}</span>
                <span class="tabular-nums text-gray-500 dark:text-gray-400">{{ selected.done }} / {{ selected.total }} <b class="ml-2 text-gray-900 dark:text-white">{{ progress }}%</b></span>
              </div>
              <div class="h-2 overflow-hidden rounded-full bg-gray-100 dark:bg-dark-700" role="progressbar" :aria-valuenow="progress" aria-valuemin="0" aria-valuemax="100" aria-label="本轮巡查进度">
                <div class="h-full rounded-full bg-primary-500 transition-[width] duration-300 motion-reduce:transition-none" :style="{ width: `${progress}%` }" />
              </div>
              <div class="mt-4 grid grid-cols-2 gap-3 text-sm sm:grid-cols-4">
                <div>原件可用 <b class="ml-1 tabular-nums">{{ selected.alive }}</b></div>
                <div>复活成功 <b class="ml-1 tabular-nums text-emerald-700 dark:text-emerald-400">{{ selected.revived }}</b></div>
                <div>新版替换 <b class="ml-1 tabular-nums text-primary-600 dark:text-primary-400">{{ selected.replaced }}</b></div>
                <div>失败 / 跳过 <b class="ml-1 tabular-nums">{{ selected.failed }} / {{ selected.skipped }}</b></div>
              </div>
              <p class="mt-3 text-xs text-gray-500 dark:text-gray-400">{{ selected.message || '检测与复活在服务器后台运行，关闭或刷新页面不会终止任务。' }}<span v-if="selected.workspace_dead"> · 炸车 {{ selected.workspace_dead }} 个（已计入失败）</span></p>
            </div>
            <div v-else class="my-8 rounded-xl border border-dashed border-gray-200 px-5 py-8 text-center dark:border-dark-600">
              <Icon name="shield" size="xl" class="mx-auto mb-3 text-gray-300 dark:text-dark-400" />
              <p class="text-sm font-medium">尚未开始巡查</p>
              <p class="mt-2 text-xs leading-6 text-gray-500 dark:text-gray-400">默认不选分组、不自动复活。请先在左侧保存你的监控设置。</p>
            </div>
            <div class="mt-7 border-t border-gray-100 pt-5 dark:border-dark-700">
              <h3 class="text-xs font-semibold uppercase tracking-wide text-gray-500 dark:text-gray-400">配置保全流程</h3>
              <ol class="mt-4 grid gap-3 text-xs sm:grid-cols-4">
                <li v-for="(step, n) in preservation" :key="step.title" class="rounded-xl bg-gray-50 p-3 dark:bg-dark-900">
                  <span class="mb-2 inline-flex h-6 w-6 items-center justify-center rounded-full border border-gray-200 text-[10px] font-semibold text-gray-500 dark:border-dark-600 dark:text-gray-400">0{{ n + 1 }}</span>
                  <b class="block text-sm">{{ step.title }}</b><p class="mt-1 leading-5 text-gray-500 dark:text-gray-400">{{ step.text }}</p>
                </li>
              </ol>
              <p class="mt-4 text-xs leading-6 text-gray-500 dark:text-gray-400">保留模型白名单、代理、并发数、优先级、WS 模式、Codex 指纹收敛、全部分组及其他账号设置。导入或校验失败时，事务撤销，旧号仍在。</p>
            </div>
          </section>
        </div>

        <section class="wish-panel min-w-0" aria-labelledby="wish-accounts-title">
          <div class="mb-5 flex flex-wrap items-center justify-between gap-3">
            <div><h2 id="wish-accounts-title" class="font-semibold">子号状态巡查</h2><p class="mt-1 text-xs text-gray-500 dark:text-gray-400">仅明确 401 / Free 才尝试复活；网络异常、套餐或空间不符保留旧号。</p></div>
            <div class="flex items-center gap-2">
              <label for="wish-history" class="text-xs text-gray-500 dark:text-gray-400">历史任务</label>
              <select id="wish-history" v-model.number="selectedID" class="input min-h-11 w-auto max-w-[230px]" :disabled="!overview.runs.length" @change="page = 1; refreshItems()">
                <option :value="0">最新任务</option>
                <option v-for="run in overview.runs" :key="run.id" :value="run.id">#{{ run.id }} · {{ formatTime(run.created_at) }}</option>
              </select>
            </div>
          </div>
          <div class="overflow-x-auto rounded-xl border border-gray-100 dark:border-dark-700">
            <table class="w-full min-w-[790px] text-left text-sm">
              <thead class="bg-gray-50 text-xs text-gray-500 dark:bg-dark-900 dark:text-gray-400"><tr><th class="wish-th">子号 / 账号 ID</th><th class="wish-th">初检证据</th><th class="wish-th">状态 / 阶段</th><th class="wish-th">处理结果</th><th class="wish-th">配置存档</th></tr></thead>
              <tbody class="divide-y divide-gray-100 dark:divide-dark-700">
                <tr v-for="item in items" :key="item.id">
                  <td class="wish-td"><span class="block max-w-[250px] truncate font-medium" :title="item.email">{{ item.email || '缺少邮箱' }}</span><span class="mt-1 block font-mono text-xs text-gray-500">#{{ item.account_id }}<template v-if="item.new_account_id"> → #{{ item.new_account_id }}</template></span></td>
                  <td class="wish-td"><span v-if="item.probe.http_status" class="block text-xs">HTTP {{ item.probe.http_status }}</span><span class="text-xs text-gray-500 dark:text-gray-400">{{ item.probe.plan_type || item.probe.reason || (item.stage === 'done' ? '—' : '等待检测') }}</span></td>
                  <td class="wish-td"><span class="wish-badge" :class="badge(item.status)">{{ statusLabel(item.status) }}</span><span v-if="item.stage !== 'done'" class="mt-1 block text-xs text-gray-500">{{ stageLabel(item.stage) }}</span></td>
                  <td class="wish-td max-w-[320px] text-xs leading-5 text-gray-500 dark:text-gray-400">{{ item.message || stageLabel(item.stage) }}<span v-if="item.retry_after" class="block">建议至少等待 {{ item.retry_after }} 秒</span></td>
                  <td class="wish-td"><button v-if="item.archived" class="min-h-11 text-xs font-medium text-primary-700 hover:underline dark:text-primary-400" @click="showArchive(item.id)">已还原 · 查看</button><span v-else class="text-xs text-gray-400">未替换</span></td>
                </tr>
                <tr v-if="!items.length"><td colspan="5" class="px-5 py-12 text-center text-sm text-gray-500 dark:text-gray-400">{{ selected ? '本任务暂时没有子号记录' : '完成分组设置后，巡查结果会显示在这里' }}</td></tr>
              </tbody>
            </table>
          </div>
          <div class="mt-4 flex flex-wrap items-center justify-between gap-3 text-xs text-gray-500 dark:text-gray-400">
            <p>最近同步 {{ updatedAt ? formatTime(updatedAt) : '—' }} · 共 {{ total }} 条</p>
            <div class="flex items-center gap-3"><button class="btn btn-secondary min-h-11" :disabled="page <= 1" @click="page--; refreshItems()">上一页</button><span>{{ page }} / {{ Math.max(1, Math.ceil(total / 50)) }}</span><button class="btn btn-secondary min-h-11" :disabled="page * 50 >= total" @click="page++; refreshItems()">下一页</button></div>
          </div>
        </section>
        <p class="text-xs leading-6 text-gray-500 dark:text-gray-400">检测服务：team5x.wishtoapp.com · 仅支持该站产出的子号。检测需要向该服务发送所选子号的 AT / RT 与工作区标识；本页不展示令牌或上游任务密钥。</p>
      </template>
    </div>

    <BaseDialog :show="!!confirmation" :title="confirmation === 'run' ? '开始本轮巡查？' : '启用自动巡查与复活？'" @close="!busy && (confirmation = '')">
      <p class="text-sm leading-7">将向 WishTeam5X 发送已选分组的账号凭据，检测并修复明确的 401 / Free 账号。复活成功或返回新版凭据后，会存档原配置、删除旧号、导入新号并校验还原；账号 ID 将改变。同一上游身份与工作区的有效绑定票据、采集冷却会继承，票据到期时间不延长；分组随后按状态池规则重新核对。</p>
      <p class="mt-3 text-sm text-gray-500">原件可用、检测失败或非本站账号不会删除。自动任务在后台运行。</p>
      <template #footer><button class="btn btn-secondary min-h-11" :disabled="busy" @click="confirmation = ''">取消</button><button class="btn btn-primary min-h-11" :disabled="busy" @click="confirmed">{{ busy ? '处理中…' : '确认执行' }}</button></template>
    </BaseDialog>
    <BaseDialog :show="archiveOpen" title="账号配置存档" @close="archiveOpen = false">
      <p v-if="!archive" role="status">正在读取存档摘要…</p>
      <template v-else>
        <div class="wish-badge wish-good mb-4">全部账号字段及分组关系已校验</div>
        <p class="mb-3 text-sm">旧号 #{{ archive.old_account_id }} → 新号 #{{ archive.new_account_id }} · {{ formatTime(archive.created_at) }}</p>
        <pre class="max-h-96 overflow-auto rounded-xl bg-gray-50 p-4 text-xs leading-6 dark:bg-dark-900">{{ JSON.stringify({ ...archive.settings, groups: archive.groups }, null, 2) }}</pre>
        <p class="mt-4 text-xs leading-6 text-gray-500">完整原账号、凭据内配置（模型白名单等）、extra 配置（WS / 指纹等）、分组及计划均已在服务器存档。此处仅显示不含凭据的摘要。</p>
      </template>
    </BaseDialog>
  </AppLayout>
</template>

<script setup lang="ts">
import { computed, onMounted, onUnmounted, reactive, ref } from 'vue'
import AppLayout from '@/components/layout/AppLayout.vue'
import BaseDialog from '@/components/common/BaseDialog.vue'
import Icon from '@/components/icons/Icon.vue'
import { wishteam, type WishArchive, type WishConfig, type WishItem, type WishOverview } from '@/api/wishteam'

const overview = ref<WishOverview | null>(null)
const draft = reactive<WishConfig>({ enabled: false, group_id: 0, interval_minutes: 10, next_run_at: '' })
const items = ref<WishItem[]>([])
const selectedID = ref(0)
const selected = computed(() => overview.value?.runs.find(r => r.id === selectedID.value) ?? overview.value?.runs[0])
const running = computed(() => overview.value?.runs.some(r => r.status === 'running') ?? false)
const progress = computed(() => selected.value?.total ? Math.min(100, Math.round(selected.value.done / selected.value.total * 100)) : selected.value?.status === 'done' ? 100 : 0)
const page = ref(1)
const total = ref(0)
const loading = ref(false)
const busy = ref(false)
const error = ref('')
const notice = ref('')
const validation = ref('')
const updatedAt = ref('')
const confirmation = ref<'' | 'save' | 'run'>('')
const archive = ref<WishArchive | null>(null)
const archiveOpen = ref(false)
let initialized = false
let disposed = false
let timer: ReturnType<typeof setTimeout> | undefined
let abort: AbortController | undefined
let itemVersion = 0
const dirty = computed(() => !overview.value || draft.enabled !== overview.value.config.enabled || draft.group_id !== overview.value.config.group_id || draft.interval_minutes !== overview.value.config.interval_minutes)
const metrics = computed(() => [
  { label: '所选任务 · 巡查子号', value: selected.value?.total ?? 0, hint: '个', color: '' },
  { label: '复活成功 + 新版替换', value: (selected.value?.revived ?? 0) + (selected.value?.replaced ?? 0), hint: '已还原配置', color: 'text-emerald-700 dark:text-emerald-400' },
  { label: '保留原凭据', value: selected.value?.alive ?? 0, hint: '原件可用', color: '' },
  { label: '需要关注', value: (selected.value?.failed ?? 0) + (selected.value?.skipped ?? 0), hint: '失败 / 跳过', color: 'text-amber-700 dark:text-amber-400' }
])
const preservation = [
  { title: '完整存档', text: '保留账号全部配置' }, { title: '删除旧号', text: '按项目软删除语义' },
  { title: '导入新号', text: '仅换已验证的凭据' }, { title: '还原校验', text: '逐字段比对与提交' }
]
function formatTime(value: string) { return new Date(value).toLocaleString('zh-CN', { month: '2-digit', day: '2-digit', hour: '2-digit', minute: '2-digit', second: '2-digit', hour12: false }) }
function statusLabel(v: string) { return ({ queued: '等待中', checking: '检测中', alive: '原件可用', replaced: '新版替换', revived: '已复活', failed: '失败', skipped: '已跳过' } as Record<string, string>)[v] || v }
function stageLabel(v: string) { return ({ queued: '等待提交', checking: '检测原文件', checking_current: '检测最新成品', loading_source: '读取原料', checking_seat: '验证席位', refreshing: '刷新 / 重登', verifying: '复查成品', done: '已完成' } as Record<string, string>)[v] || '等待进度' }
function badge(v: string) { return ['alive', 'revived', 'replaced'].includes(v) ? 'wish-good' : v === 'failed' ? 'wish-bad' : v === 'checking' ? 'wish-blue' : 'wish-neutral' }
function message(e: unknown) { return (e as { message?: string })?.message || '请求失败，请稍后重试' }

async function refreshItems(signal?: AbortSignal) {
  const version = ++itemVersion
  const id = selected.value?.id
  if (!id) { items.value = []; total.value = 0; return }
  try {
    const result = await wishteam.items(id, page.value, signal)
    if (disposed || version !== itemVersion || id !== selected.value?.id) return
    items.value = result.items
    total.value = result.total
  } catch (e) { if (!signal?.aborted && !disposed) error.value = message(e) }
}
async function refresh(force = false) {
  if (disposed || loading.value || (!force && document.hidden)) { schedule(); return }
  loading.value = true
  abort = new AbortController()
  try {
    const data = await wishteam.overview(abort.signal)
    if (disposed) return
    overview.value = data
    if (!initialized) { Object.assign(draft, data.config); initialized = true }
    error.value = ''
    await refreshItems(abort.signal)
    updatedAt.value = new Date().toISOString()
  } catch (e) { if (!abort.signal.aborted && !disposed) error.value = message(e) }
  finally { loading.value = false; schedule() }
}
function schedule() {
  clearTimeout(timer)
  if (!disposed) timer = setTimeout(() => refresh(), running.value ? 1500 : 10000)
}
function requestSave() {
  validation.value = ''
  if (!draft.group_id || !Number.isInteger(draft.interval_minutes) || draft.interval_minutes < 1 || draft.interval_minutes > 1440) {
    validation.value = '请选择分组，巡查间隔为 1–1440 的整数。'; return
  }
  if (draft.enabled) confirmation.value = 'save'
  else void save()
}
async function save() {
  busy.value = true; notice.value = ''; error.value = ''
  try {
    const data = await wishteam.save({ ...draft })
    overview.value = data; Object.assign(draft, data.config)
    confirmation.value = ''; notice.value = data.config.enabled ? '设置已保存，自动巡查已启用。' : '设置已保存。新轮次已暂停，正在执行的任务会继续完成。'
  } catch (e) { error.value = message(e) }
  finally { busy.value = false; schedule() }
}
async function confirmed() {
  if (confirmation.value === 'save') { await save(); return }
  busy.value = true; notice.value = ''; error.value = ''
  try {
    const result = await wishteam.run()
    selectedID.value = result.run_id; page.value = 1; confirmation.value = ''
    notice.value = `任务 #${result.run_id} 已创建，正在后台巡查。`
    await refresh(true)
  } catch (e) { error.value = message(e) }
  finally { busy.value = false }
}
async function showArchive(id: number) {
  archive.value = null; archiveOpen.value = true
  try { archive.value = await wishteam.archive(id) }
  catch (e) { archiveOpen.value = false; error.value = message(e) }
}
function visible() { if (!document.hidden) void refresh(true) }
onMounted(() => { void refresh(true); document.addEventListener('visibilitychange', visible) })
onUnmounted(() => { disposed = true; itemVersion++; clearTimeout(timer); abort?.abort(); document.removeEventListener('visibilitychange', visible) })
</script>

<style scoped>
.wish-page { @apply mx-auto flex w-full max-w-[1500px] flex-col gap-6 px-4 py-6 text-gray-800 dark:text-gray-100 sm:px-6 lg:px-8; }
.wish-panel { @apply rounded-2xl border border-gray-200 bg-white p-5 shadow-sm dark:border-dark-700 dark:bg-dark-800; }
.wish-label { @apply mb-2 block text-sm font-medium; }
.wish-badge { @apply inline-flex items-center gap-2 whitespace-nowrap rounded-full px-2.5 py-1 text-xs font-medium; }
.wish-good { @apply bg-emerald-50 text-emerald-700 dark:bg-emerald-950/50 dark:text-emerald-300; }
.wish-neutral { @apply bg-gray-100 text-gray-600 dark:bg-dark-700 dark:text-gray-300; }
.wish-blue { @apply bg-blue-50 text-blue-700 dark:bg-blue-950/50 dark:text-blue-300; }
.wish-bad { @apply bg-red-50 text-red-700 dark:bg-red-950/50 dark:text-red-300; }
.wish-th { @apply whitespace-nowrap px-4 py-3 font-medium; }
.wish-td { @apply px-4 py-3 align-middle; }
</style>
