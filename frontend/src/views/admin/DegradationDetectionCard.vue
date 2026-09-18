<template>
  <div class="mt-4 space-y-4 border-t border-gray-200 pt-4 dark:border-dark-400">
    <div class="flex flex-wrap items-start justify-between gap-4">
      <div class="min-w-0">
        <div class="flex items-center gap-2">
          <h4 class="text-sm font-medium text-gray-700 dark:text-gray-300">降智检测</h4>
          <span
            v-if="config.enabled"
            class="rounded-full bg-emerald-50 px-2 py-0.5 text-[11px] font-medium text-emerald-700 dark:bg-emerald-500/10 dark:text-emerald-300"
          >
            已开启
          </span>
        </div>
        <p class="mt-1 text-xs text-gray-500 dark:text-gray-400">
          按间隔用糖果题探测本组账号；答案不等于标准答案，或最近10次已记录请求中至少5次请求 gpt-6-astra、上游响应模型为 luna，即判定降智（OR）。按下方设置暂停调度或移入指定分组。
          响应模型规则每分钟巡检；不足10条时按已有记录计数，仍需至少5次命中。未知响应模型不算命中。
          账号管理器手动停用的账号不参与探测，也不会被自动恢复。
          跨组共享账号只要属于启用检测的分组就可能被探测；暂停会影响该账号在所有分组的调度。
        </p>
      </div>
      <button
        type="button"
        role="switch"
        :aria-checked="config.enabled"
        :disabled="loading || saving"
        class="relative inline-flex h-6 w-11 shrink-0 items-center rounded-full transition-colors"
        :class="config.enabled ? 'bg-primary-500' : 'bg-gray-300 dark:bg-dark-600'"
        @click="config.enabled = !config.enabled"
      >
        <span
          :class="[
            'inline-block h-4 w-4 transform rounded-full bg-white shadow transition-transform',
            config.enabled ? 'translate-x-6' : 'translate-x-1',
          ]"
        />
      </button>
    </div>

    <div v-if="loading" class="text-xs text-gray-400">正在读取配置…</div>

    <template v-else-if="config.enabled">
      <div class="grid gap-4 sm:grid-cols-2">
        <div>
          <label class="input-label">检测间隔（分钟）</label>
          <input v-model.number="config.interval_minutes" type="number" min="1" max="1440" step="1" class="input" />
          <p class="input-hint">到点的账号会排队探测；队列繁忙时优先补测最久未测的账号。</p>
        </div>
        <div>
          <label class="input-label">思考强度</label>
          <select v-model="config.reasoning_effort" class="input">
            <option v-for="effort in EFFORT_OPTIONS" :key="effort" :value="effort">{{ effort }}</option>
          </select>
          <p class="input-hint">探测题目默认使用 medium。</p>
        </div>
        <div>
          <label class="input-label">模型 ID</label>
          <input v-model.trim="config.model" type="text" class="input" placeholder="gpt-6-astra" />
          <p class="input-hint">默认 gpt-6-astra。</p>
        </div>
        <div>
          <label class="input-label">标准答案</label>
          <input v-model.trim="config.expected_answer" type="text" class="input" placeholder="21" />
          <p class="input-hint">默认 21，可自定义。保存后新任务按本组标准答案判定，已运行任务保留原答案。</p>
        </div>
        <div v-if="!config.move_on_degraded">
          <label class="input-label">降智后暂停调度（分钟）</label>
          <input v-model.number="config.suspend_minutes" type="number" min="1" max="1440" step="1" class="input" />
          <p class="input-hint">暂停期间继续探测；答对且响应模型规则未命中时提前恢复调度。</p>
        </div>
        <div>
          <label class="input-label">单次探测超时（秒）</label>
          <input v-model.number="config.timeout_seconds" type="number" min="30" max="600" step="1" class="input" />
          <p class="input-hint">范围 30-600 秒。</p>
        </div>
      </div>

      <section class="space-y-3 rounded-lg border border-gray-200 p-3 dark:border-dark-600" aria-label="降智后移组">
        <div class="flex items-start justify-between gap-4">
          <div class="min-w-0">
            <p class="text-xs font-medium text-gray-700 dark:text-gray-300">降智后移组</p>
            <p class="input-hint">
              开启后，明确判定降智的账号会清除原有全部分组绑定，只绑定目标分组；不再添加降智冷却，并清除已有的降智检测冷却。
              其他原因的停用、限流或冷却不变；超时和网络错误不触发移组，恢复后不自动移回。
            </p>
          </div>
          <button
            type="button"
            role="switch"
            aria-label="降智后移组"
            data-testid="degradation-move-switch"
            :aria-checked="config.move_on_degraded"
            :disabled="saving || loading"
            class="relative inline-flex h-6 w-11 shrink-0 items-center rounded-full transition-colors disabled:opacity-50"
            :class="config.move_on_degraded ? 'bg-primary-500' : 'bg-gray-300 dark:bg-dark-600'"
            @click="config.move_on_degraded = !config.move_on_degraded"
          >
            <span :class="['inline-block h-4 w-4 transform rounded-full bg-white shadow transition-transform', config.move_on_degraded ? 'translate-x-6' : 'translate-x-1']" />
          </button>
        </div>
        <div v-if="config.move_on_degraded">
          <label :for="`degradation-move-target-${groupId}`" class="input-label">目标分组</label>
          <select
            :id="`degradation-move-target-${groupId}`"
            v-model.number="config.move_target_group_id"
            data-testid="degradation-move-target"
            class="input"
            :disabled="saving || loading"
          >
            <option :value="0" disabled>请选择移入分组</option>
            <option v-if="config.move_target_group_id && !validMoveTarget" :value="config.move_target_group_id" disabled>
              目标分组 #{{ config.move_target_group_id }} 不可用，请重新选择
            </option>
            <option v-for="group in moveTargetGroups" :key="group.group_id" :value="group.group_id">
              {{ group.group_name }}（#{{ group.group_id }}）
            </option>
          </select>
          <p class="input-hint">保存后对新完成的降智判定生效，不批量处理历史记录。关闭移组后恢复使用暂停调度规则。</p>
        </div>
      </section>

      <div class="space-y-3 rounded-lg bg-gray-50 p-3 dark:bg-dark-800/60">
        <div class="flex items-center justify-between gap-4">
          <div>
            <p class="text-xs font-medium text-gray-700 dark:text-gray-300">在公开页展示作品</p>
            <p class="input-hint">
              开启后本组会随机抽一个正在启用的账号生成鹈鹕 SVG，展示在
              <a class="text-primary-600 hover:underline dark:text-primary-400" href="/jiangzhijiance/" target="_blank" rel="noopener noreferrer">
                /jiangzhijiance/
              </a>
            </p>
          </div>
          <button
            type="button"
            role="switch"
            :aria-checked="config.preview_enabled"
            class="relative inline-flex h-6 w-11 shrink-0 items-center rounded-full transition-colors"
            :class="config.preview_enabled ? 'bg-primary-500' : 'bg-gray-300 dark:bg-dark-600'"
            @click="config.preview_enabled = !config.preview_enabled"
          >
            <span
              :class="[
                'inline-block h-4 w-4 transform rounded-full bg-white shadow transition-transform',
                config.preview_enabled ? 'translate-x-6' : 'translate-x-1',
              ]"
            />
          </button>
        </div>
        <div v-if="config.preview_enabled" class="grid gap-4 sm:grid-cols-3">
          <div>
            <label class="input-label">公开页间隔（分钟）</label>
            <input v-model.number="config.preview_interval_minutes" type="number" min="1" max="1440" step="1" class="input" />
          </div>
          <div>
            <label class="input-label">公开页模型 ID</label>
            <input v-model.trim="config.preview_model" type="text" class="input" placeholder="gpt-6-astra" />
          </div>
          <div>
            <label class="input-label">公开页思考强度</label>
            <select v-model="config.preview_reasoning_effort" class="input">
              <option v-for="effort in EFFORT_OPTIONS" :key="effort" :value="effort">{{ effort }}</option>
            </select>
          </div>
        </div>
      </div>
    </template>

    <section class="space-y-3 rounded-lg border border-gray-200 p-3 dark:border-dark-600" aria-label="公开统计管理">
      <p class="text-xs font-medium text-gray-700 dark:text-gray-300">公开页统计管理（全站）</p>
      <p class="input-hint">重置所有分组汇总的时间轴、答对比例、答错次数及最近探测状态。历史探测、作品数量、分组设置和账号暂停状态保持不变；重置前入队的任务也不再计入。</p>
      <button type="button" class="btn btn-secondary text-xs" :disabled="resetting" @click="resetConfirmation = true">重置公开页统计</button>
      <div v-if="resetConfirmation" class="space-y-3 rounded-lg bg-amber-50 p-3 dark:bg-dark-800" role="group" aria-label="确认重置全站统计">
        <p class="text-xs text-gray-700 dark:text-gray-300">确认从现在开始重新累计全站公开统计？24 小时、3 天和 7 天窗口都会重置，并非仅影响当前分组。</p>
        <div class="flex gap-3">
          <button type="button" class="btn btn-primary text-xs" :disabled="resetting" @click="confirmStatsReset">{{ resetting ? '正在重置…' : '确认重置全站统计' }}</button>
          <button type="button" class="btn btn-secondary text-xs" :disabled="resetting" @click="resetConfirmation = false">取消</button>
        </div>
      </div>
      <p v-if="resetMessage" :role="resetFailed ? 'alert' : 'status'" class="text-xs" :class="resetFailed ? 'text-red-600' : 'text-emerald-700 dark:text-emerald-300'">{{ resetMessage }}</p>
    </section>

    <!-- 公开页是运营可管理的展台：这里能看能删，但只动画作本身。 -->
    <div class="space-y-3 rounded-lg bg-gray-50 p-3 dark:bg-dark-800/60">
      <div class="flex flex-wrap items-start justify-between gap-3">
        <div>
          <p class="text-xs font-medium text-gray-700 dark:text-gray-300">公开页作品管理</p>
          <p class="input-hint">
            全站作品管理，共 {{ worksTotal }} 条。删除只影响公开页展示，不会动探测记录；运行中的画作不会被清掉。
          </p>
        </div>
        <div class="flex flex-wrap items-center gap-2">
          <button type="button" class="btn btn-secondary" :disabled="worksLoading" @click="loadWorks">
            {{ worksLoading ? '读取中…' : '刷新作品列表' }}
          </button>
          <button
            type="button"
            class="btn btn-secondary"
            :disabled="worksLoading || worksTotal === 0"
            @click="purgeAllWorks"
          >
            清空全部作品
          </button>
        </div>
      </div>

      <div v-if="works.length" class="grid gap-3 sm:grid-cols-2">
        <div
          v-for="work in works"
          :key="work.id"
          class="flex items-center gap-3 rounded-lg border border-gray-200 bg-white p-2 dark:border-dark-700 dark:bg-dark-900"
        >
          <img
            v-if="workImages[work.id]"
            :src="workImageURL(work.id)"
            alt=""
            class="h-14 w-14 flex-shrink-0 rounded-md bg-gray-50 object-contain dark:bg-dark-800"
          />
          <div class="min-w-0 flex-1">
            <p class="truncate font-mono text-xs text-gray-600 dark:text-dark-200">#{{ work.id }} · {{ work.status }}</p>
            <p class="mt-0.5 truncate font-mono text-[11px] text-gray-400 dark:text-dark-500">
              {{ work.model }} · {{ formatWorkClock(work.created_at) }}
            </p>
          </div>
          <button type="button" class="btn btn-secondary" :disabled="worksLoading" @click="removeWork(work.id)">
            删除
          </button>
        </div>
      </div>
      <p v-else class="text-xs text-gray-400">公开页暂无作品。</p>
      <p v-if="worksMessage" class="text-xs" :class="worksFailed ? 'text-red-600' : 'text-emerald-600'">
        {{ worksMessage }}
      </p>
    </div>

    <div class="flex flex-wrap items-center gap-3">
      <button type="button" class="btn btn-secondary" :disabled="saving || loading || !loaded" @click="save">
        {{ saving ? '保存中…' : '保存降智检测' }}
      </button>
      <button
        v-if="config.enabled"
        type="button"
        class="btn btn-secondary"
        :disabled="running || saving || loading"
        @click="runNow"
      >
        {{ running ? '已入队' : '立即探测本组' }}
      </button>
      <span v-if="message" :role="failed ? 'alert' : 'status'" class="text-xs" :class="failed ? 'text-red-600' : 'text-emerald-600'">{{ message }}</span>
    </div>
  </div>
</template>

<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import {
	DEFAULT_DEGRADATION_CONFIG,
	deleteWork,
	listGroups,
	listWorks,
	adminWorkImage,
	purgeWorks,
	resetPublicStats,
	runNow as runDegradationNow,
	updateGroup,
	type DegradationDetectionConfig,
	type DegradationGroup,
	type DegradationPublicWork,
} from '@/api/degradation'

defineExpose({ save })

const props = defineProps<{ groupId: number }>()

const EFFORT_OPTIONS = ['minimal', 'low', 'medium', 'high', 'xhigh', 'max'] as const

const config = ref<DegradationDetectionConfig>({ ...DEFAULT_DEGRADATION_CONFIG })
const availableGroups = ref<DegradationGroup[]>([])
const moveTargetGroups = computed(() => availableGroups.value.filter(group => group.group_id !== props.groupId))
const validMoveTarget = computed(() =>
  Number.isSafeInteger(config.value.move_target_group_id) &&
  moveTargetGroups.value.some(group => group.group_id === config.value.move_target_group_id)
)
let configGeneration = 0
const loading = ref(false)
const loaded = ref(false)
const saving = ref(false)
const running = ref(false)
const message = ref('')
const failed = ref(false)
const resetConfirmation = ref(false)
const resetting = ref(false)
const resetMessage = ref('')
const resetFailed = ref(false)
async function confirmStatsReset() {
  if (resetting.value) return
  resetting.value = true
  resetMessage.value = ''
  try {
    const result = await resetPublicStats()
    resetConfirmation.value = false
    resetFailed.value = false
    resetMessage.value = `已重置全站公开统计，起点：${new Date(result.reset_at).toLocaleString()}。刷新公开页即可查看；历史记录及作品保留。`
  } catch (error) {
    resetFailed.value = true
    resetMessage.value = extractMessage(error) || '重置失败，请重试'
  } finally {
    resetting.value = false
  }
}

const works = ref<DegradationPublicWork[]>([])
const workImages = ref<Record<number, string>>({})
let imageController: AbortController | undefined
let workGeneration = 0
function clearWorkImages() {
  Object.values(workImages.value).forEach(url => URL.revokeObjectURL(url))
  workImages.value = {}
}
onBeforeUnmount(() => { configGeneration++; workGeneration++; imageController?.abort(); clearWorkImages() })
const worksTotal = ref(0)
const worksLoading = ref(false)
const worksMessage = ref('')
const worksFailed = ref(false)

async function load() {
  const generation = ++configGeneration
  const groupId = props.groupId
  if (!groupId) {
    return
  }
  loading.value = true
  loaded.value = false
  try {
    const groups = await listGroups()
    if (generation !== configGeneration) return
    const match = groups.find((item) => item.group_id === groupId)
    if (!match) throw new Error("分组配置不存在")
    availableGroups.value = groups
    config.value = { ...DEFAULT_DEGRADATION_CONFIG, ...match.config }
    loaded.value = true
    message.value = ''
    failed.value = false
  } catch {
    if (generation !== configGeneration) return
    failed.value = true
    message.value = '读取降智检测配置失败'
  } finally {
    if (generation === configGeneration) loading.value = false
  }
}

async function save() {
  if (!loaded.value || loading.value || saving.value) {
    // 配置没加载成功时并没有可写回的内容，返回 false 的含义是「未保存」而不是
    // 「保存失败」。不在这里说明，调用方只能统一报「保存失败，请重试」，
    // 而配置根本没读出来的情况下重试永远不会成功。
    if (!loaded.value && !loading.value) {
      failed.value = true
      message.value = '降智检测配置未加载成功，无法保存；请关闭弹窗重开'
    }
    return false
  }
  if (config.value.enabled && config.value.move_on_degraded && !validMoveTarget.value) {
    failed.value = true
    message.value = '请选择有效的目标分组，且不能选择当前分组'
    return false
  }
  const groupId = props.groupId
  const generation = configGeneration
  saving.value = true
  message.value = ''
  try {
    const result = await updateGroup(groupId, { ...config.value })
    if (generation !== configGeneration) return false
    config.value = { ...DEFAULT_DEGRADATION_CONFIG, ...result.config }
    failed.value = false
    message.value = '已保存并生效；新入队探测使用本组标准答案'
    return true
  } catch (error) {
    if (generation !== configGeneration) return false
    failed.value = true
    message.value = extractMessage(error) || '保存失败'
    return false
  } finally {
    saving.value = false
  }
}

async function runNow() {
  if (!(await save())) return
  running.value = true
  message.value = ''
  try {
    const result = await runDegradationNow(props.groupId)
    failed.value = false
    message.value = `已入队 ${result.queued} 个账号`
  } catch (error) {
    failed.value = true
    message.value = extractMessage(error) || '入队失败'
  } finally {
    running.value = false
  }
}

function workImageURL(id: number): string {
	return workImages.value[id] || ''
}

function formatWorkClock(raw: string | null): string {
	if (!raw) return '--'
	const date = new Date(raw)
	if (Number.isNaN(date.getTime())) return '--'
	const pad = (value: number) => value.toString().padStart(2, '0')
	return `${pad(date.getMonth() + 1)}-${pad(date.getDate())} ${pad(date.getHours())}:${pad(date.getMinutes())}`
}

async function loadWorks() {
    const generation = ++workGeneration
    imageController?.abort()
    imageController = new AbortController()
    const signal = imageController.signal
	worksLoading.value = true
	try {
		const page = await listWorks(1, 12)
        if (generation !== workGeneration) return
        clearWorkImages()
		works.value = page.items ?? []
		worksTotal.value = page.total ?? 0
		worksMessage.value = ''
		worksFailed.value = false
        await Promise.allSettled(works.value.filter(work => work.has_image).map(async work => {
            const blob = await adminWorkImage(work.id, signal)
            if (generation === workGeneration) workImages.value[work.id] = URL.createObjectURL(blob)
        }))
	} catch (error) {
		worksFailed.value = true
		if (generation !== workGeneration) return
        worksMessage.value = extractMessage(error) || '读取公开页作品失败'
	} finally {
		if (generation === workGeneration) worksLoading.value = false
	}
}

async function removeWork(id: number) {
	worksLoading.value = true
	try {
		await deleteWork(id)
		worksFailed.value = false
		worksMessage.value = `已删除作品 #${id}`
		await loadWorks()
	} catch (error) {
		worksFailed.value = true
		worksMessage.value = extractMessage(error) || '删除失败'
		worksLoading.value = false
	}
}

async function purgeAllWorks() {
	worksLoading.value = true
	try {
		const result = await purgeWorks()
		worksFailed.value = false
		worksMessage.value = `已清空 ${result.deleted} 幅作品`
		await loadWorks()
	} catch (error) {
		worksFailed.value = true
		worksMessage.value = extractMessage(error) || '清空失败'
		worksLoading.value = false
	}
}

function extractMessage(error: unknown): string {
  const candidate = error as { response?: { data?: { message?: string } }; message?: string }
  return candidate?.response?.data?.message || candidate?.message || ''
}

onMounted(load)
onMounted(loadWorks)
watch(() => props.groupId, () => {
	void load()
	void loadWorks()
})
</script>
