<template>
  <section class="rounded-2xl border border-gray-200 bg-white p-5 sm:p-6 dark:border-dark-800 dark:bg-dark-900/50">
    <header class="flex flex-wrap items-start justify-between gap-4">
      <div class="min-w-0">
        <h2 class="text-base font-semibold text-gray-950 dark:text-white">降智时间轴</h2>
        <p class="mt-1 text-xs text-gray-500 dark:text-dark-400">
          每 {{ timeline?.bucket_minutes || 60 }} 分钟一个刻度，绿柱是答对的探测，红柱是判定降智的探测。
        </p>
      </div>

      <div class="flex flex-wrap items-center gap-2">
        <div class="flex rounded-lg bg-gray-100 p-0.5 dark:bg-dark-800">
          <button
            v-for="option in RANGES"
            :key="option.hours"
            type="button"
            class="rounded-md px-3 py-1 text-xs font-medium transition"
            :class="
              option.hours === range
                ? 'bg-white text-gray-900 shadow-sm dark:bg-dark-700 dark:text-white'
                : 'text-gray-500 hover:text-gray-900 dark:text-dark-300 dark:hover:text-white'
            "
            @click="emit('update:range', option.hours)"
          >
            {{ option.label }}
          </button>
        </div>
        <span class="inline-flex items-center gap-2 rounded-full px-3 py-1.5 text-xs font-medium" :class="statePillClass">
          <span class="h-1.5 w-1.5 rounded-full" :class="stateDotClass" aria-hidden="true"></span>
          {{ stateText }}
        </span>
      </div>
    </header>

    <div class="mt-5 flex items-stretch gap-3">
      <div class="flex flex-col justify-between py-0.5 text-right font-mono text-[10px] leading-none text-gray-400 dark:text-dark-500">
        <span>{{ peak }}</span>
        <span>{{ Math.round(peak / 2) }}</span>
        <span>0</span>
      </div>

      <div class="min-w-0 flex-1">
        <svg
          :viewBox="`0 0 ${CHART_WIDTH} ${CHART_HEIGHT}`"
          preserveAspectRatio="none"
          class="h-28 w-full sm:h-32"
          role="img"
          aria-label="降智检测时间轴"
        >
          <line
            v-for="line in 3"
            :key="`grid-${line}`"
            x1="0"
            :x2="CHART_WIDTH"
            :y1="((line - 1) * CHART_HEIGHT) / 2"
            :y2="((line - 1) * CHART_HEIGHT) / 2"
            class="stroke-gray-200 dark:stroke-dark-700"
            stroke-width="1"
            stroke-dasharray="4 6"
          />
          <g v-for="(bar, index) in bars" :key="index">
            <title>{{ bar.tooltip }}</title>
            <rect :x="bar.x" :y="bar.y" :width="bar.width" :height="bar.height" :fill="bar.fill" rx="2" />
          </g>
        </svg>

        <div class="mt-2 flex justify-between font-mono text-[10px] text-gray-400 dark:text-dark-500">
          <span>{{ axisLabel(0) }}</span>
          <span>{{ axisLabel(Math.floor(barCount / 2)) }}</span>
          <span>{{ axisLabel(barCount - 1) }}</span>
        </div>
      </div>
    </div>

    <footer class="mt-4 flex flex-wrap items-center justify-between gap-3 border-t border-gray-100 pt-3 dark:border-dark-800">
      <div class="flex flex-wrap items-center gap-4 text-xs text-gray-500 dark:text-dark-400">
        <span class="inline-flex items-center gap-1.5"><span class="h-2 w-2 rounded-sm bg-emerald-500"></span>正常 {{ total.correct }}</span>
        <span class="inline-flex items-center gap-1.5"><span class="h-2 w-2 rounded-sm bg-red-500"></span>降智 {{ total.degraded }}</span>
        <span class="inline-flex items-center gap-1.5"><span class="h-2 w-2 rounded-sm bg-gray-300 dark:bg-dark-600"></span>未知 {{ total.undetermined }}</span>
      </div>
      <p class="text-xs text-gray-500 dark:text-dark-400">
        窗口内 {{ total.probes }} 次探测 · 正常率 {{ healthyPercent }}%
        <span v-if="timeline?.suspended_accounts"> · {{ timeline.suspended_accounts }} 个账号暂停调度中</span>
      </p>
    </footer>
  </section>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import type { DegradationTimeline } from '@/api/degradation'

const props = defineProps<{
  timeline: DegradationTimeline | null
  /** Selected window in hours; the parent drives it with v-model:range. */
  range: number
  loading?: boolean
}>()

const emit = defineEmits<{ (event: 'update:range', value: number): void }>()

const RANGES = [
  { hours: 24, label: '24 小时' },
  { hours: 72, label: '3 天' },
  { hours: 168, label: '7 天' },
] as const

const CHART_WIDTH = 720
const CHART_HEIGHT = 120
const GAP = 4

const buckets = computed(() => props.timeline?.buckets ?? [])
const barCount = computed(() => buckets.value.length)

const total = computed(() => {
  const source = props.timeline
  return {
    probes: source?.total ?? 0,
    correct: source?.correct ?? 0,
    degraded: source?.degraded ?? 0,
    undetermined: source?.undetermined ?? 0,
  }
})

const peak = computed(() => {
  const highest = buckets.value.reduce((max, bucket) => Math.max(max, bucket.total), 0)
  return Math.max(highest, 1)
})

const healthyPercent = computed(() => {
  if (!total.value.probes) return 0
  return Math.round((total.value.correct / total.value.probes) * 100)
})

type Bar = { x: number; y: number; width: number; height: number; fill: string; tooltip: string }

const barWidth = computed(() => {
  const count = Math.max(barCount.value, 1)
  return Math.max((CHART_WIDTH - GAP * (count - 1)) / count, 1)
})

const colorFor = (bucket: { correct: number; degraded: number; undetermined: number }) => {
  if (bucket.degraded > 0) return '#ef4444'
  if (bucket.correct > 0) return '#10b981'
  return '#d1d5db'
}

const bars = computed<Bar[]>(() => {
  const width = barWidth.value
  const unit = CHART_HEIGHT / peak.value
  return buckets.value.map((bucket, index) => {
    const x = index * (width + GAP)
    const height = bucket.total > 0 ? Math.max(bucket.total * unit, 2) : 2
    const tooltip = `${axisLabel(index)} · 探测 ${bucket.total}（正常 ${bucket.correct} / 降智 ${bucket.degraded} / 未知 ${bucket.undetermined}）`
    return {
      x,
      y: CHART_HEIGHT - height,
      width,
      height,
      fill: bucket.total > 0 ? colorFor(bucket) : '#e5e7eb',
      tooltip,
    }
  })
})

const stateText = computed(() => {
  switch (props.timeline?.current_state) {
    case 'healthy':
      return '最近探测正常'
    case 'degraded':
      return '最近探测判定降智'
    default:
      return '等待探测数据'
  }
})

const statePillClass = computed(() => {
  switch (props.timeline?.current_state) {
    case 'healthy':
      return 'bg-emerald-50 text-emerald-700 dark:bg-emerald-500/10 dark:text-emerald-300'
    case 'degraded':
      return 'bg-red-50 text-red-700 dark:bg-red-500/10 dark:text-red-300'
    default:
      return 'bg-gray-100 text-gray-600 dark:bg-dark-800 dark:text-dark-300'
  }
})

const stateDotClass = computed(() => {
  switch (props.timeline?.current_state) {
    case 'healthy':
      return 'bg-emerald-500'
    case 'degraded':
      return 'bg-red-500'
    default:
      return 'bg-gray-400'
  }
})

function pad(value: number): string {
  return value.toString().padStart(2, '0')
}

function axisLabel(index: number): string {
  const bucket = buckets.value[index]
  if (!bucket) return '--'
  const date = new Date(bucket.start)
  if (Number.isNaN(date.getTime())) return '--'
  const clock = `${pad(date.getHours())}:${pad(date.getMinutes())}`
  if (props.range > 24) {
    return `${pad(date.getMonth() + 1)}-${pad(date.getDate())} ${clock}`
  }
  return clock
}
</script>
