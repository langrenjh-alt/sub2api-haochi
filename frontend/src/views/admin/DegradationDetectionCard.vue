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
          按间隔用固定题目探测本组账号；答案不等于标准答案即判定降智，暂停调度并在恢复后自动重新启用。
          账号管理器手动停用的账号不参与探测，也不会被自动恢复。
        </p>
      </div>
      <button
        type="button"
        role="switch"
        :aria-checked="config.enabled"
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
          <p class="input-hint">糖果题正确答案为 21；答案不同即判定降智。</p>
        </div>
        <div>
          <label class="input-label">降智后暂停调度（分钟）</label>
          <input v-model.number="config.suspend_minutes" type="number" min="1" max="1440" step="1" class="input" />
          <p class="input-hint">暂停期间继续探测，一旦答对立即恢复调度。</p>
        </div>
        <div>
          <label class="input-label">单次探测超时（秒）</label>
          <input v-model.number="config.timeout_seconds" type="number" min="30" max="600" step="1" class="input" />
          <p class="input-hint">范围 30-600 秒。</p>
        </div>
      </div>

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

    <!-- 公开页是运营可管理的展台：这里能看能删，但只动画作本身。 -->
    <div class="space-y-3 rounded-lg bg-gray-50 p-3 dark:bg-dark-800/60">
      <div class="flex flex-wrap items-start justify-between gap-3">
        <div>
          <p class="text-xs font-medium text-gray-700 dark:text-gray-300">公开页作品管理</p>
          <p class="input-hint">
            当前展示 {{ worksTotal }} 幅。删除只影响公开页展示，不会动探测记录；运行中的画作不会被清掉。
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
            v-if="work.has_image"
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
      <button type="button" class="btn btn-secondary" :disabled="saving" @click="save">
        {{ saving ? '保存中…' : '保存降智检测' }}
      </button>
      <button
        v-if="config.enabled"
        type="button"
        class="btn btn-secondary"
        :disabled="running"
        @click="runNow"
      >
        {{ running ? '已入队' : '立即探测本组' }}
      </button>
      <span v-if="message" class="text-xs" :class="failed ? 'text-red-600' : 'text-emerald-600'">{{ message }}</span>
    </div>
  </div>
</template>

<script setup lang="ts">
import { onMounted, ref, watch } from 'vue'
import {
	DEFAULT_DEGRADATION_CONFIG,
	deleteWork,
	listGroups,
	listWorks,
	publicImageURL,
	purgeWorks,
	runNow as runDegradationNow,
	updateGroup,
	type DegradationDetectionConfig,
	type DegradationPublicWork,
} from '@/api/degradation'

const props = defineProps<{ groupId: number }>()

const EFFORT_OPTIONS = ['minimal', 'low', 'medium', 'high', 'xhigh', 'max'] as const

const config = ref<DegradationDetectionConfig>({ ...DEFAULT_DEGRADATION_CONFIG })
const loading = ref(false)
const saving = ref(false)
const running = ref(false)
const message = ref('')
const failed = ref(false)

const works = ref<DegradationPublicWork[]>([])
const worksTotal = ref(0)
const worksLoading = ref(false)
const worksMessage = ref('')
const worksFailed = ref(false)

async function load() {
  if (!props.groupId) {
    return
  }
  loading.value = true
  try {
    const groups = await listGroups()
    const match = groups.find((item) => item.group_id === props.groupId)
    config.value = match ? { ...match.config } : { ...DEFAULT_DEGRADATION_CONFIG }
    message.value = ''
    failed.value = false
  } catch {
    failed.value = true
    message.value = '读取降智检测配置失败'
  } finally {
    loading.value = false
  }
}

async function save() {
  saving.value = true
  message.value = ''
  try {
    const result = await updateGroup(props.groupId, config.value)
    config.value = { ...result.config }
    failed.value = false
    message.value = '已保存（分组保存后生效）'
  } catch (error) {
    failed.value = true
    message.value = extractMessage(error) || '保存失败'
  } finally {
    saving.value = false
  }
}

async function runNow() {
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
	return publicImageURL(id)
}

function formatWorkClock(raw: string | null): string {
	if (!raw) return '--'
	const date = new Date(raw)
	if (Number.isNaN(date.getTime())) return '--'
	const pad = (value: number) => value.toString().padStart(2, '0')
	return `${pad(date.getMonth() + 1)}-${pad(date.getDate())} ${pad(date.getHours())}:${pad(date.getMinutes())}`
}

async function loadWorks() {
	worksLoading.value = true
	try {
		const page = await listWorks(1, 12)
		works.value = page.items ?? []
		worksTotal.value = page.total ?? 0
		worksMessage.value = ''
		worksFailed.value = false
	} catch (error) {
		worksFailed.value = true
		worksMessage.value = extractMessage(error) || '读取公开页作品失败'
	} finally {
		worksLoading.value = false
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
