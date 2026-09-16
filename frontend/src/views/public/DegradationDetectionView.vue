<template>
  <div class="min-h-screen bg-white text-gray-900 dark:bg-dark-950 dark:text-white">
    <div class="mx-auto w-full max-w-[1400px] px-5 py-8 sm:px-8 lg:px-10 lg:py-10">
      <!-- Header -->
      <header class="flex flex-wrap items-start justify-between gap-6">
        <div class="min-w-0">
          <p class="text-[11px] font-semibold uppercase tracking-[0.18em] text-gray-400 dark:text-dark-400">
            VISUAL BENCHMARK
          </p>
          <h1 class="mt-2 text-3xl font-bold tracking-tight text-gray-950 sm:text-4xl dark:text-white">
            {{ page?.headline || '鹈鹕骑行' }}
          </h1>
          <div class="mt-3 flex flex-wrap items-center gap-2.5">
            <span class="rounded-full bg-gray-100 px-2.5 py-1 text-xs font-medium text-gray-500 dark:bg-dark-800 dark:text-dark-300">
              {{ page?.reasoning_effort || 'low' }}
            </span>
            <span class="text-sm text-gray-400 dark:text-dark-400">HTML / SVG · 独立生成</span>
          </div>
        </div>

        <div class="flex flex-shrink-0 items-center gap-3">
          <span
            class="inline-flex items-center gap-2 rounded-full px-4 py-2 text-sm font-medium"
            :class="statusPillClass"
          >
            <span class="h-1.5 w-1.5 rounded-full" :class="statusDotClass" aria-hidden="true"></span>
            {{ statusPillText }}
          </span>
          <button
            type="button"
            class="inline-flex h-10 w-10 items-center justify-center rounded-xl border border-gray-200 bg-white text-gray-500 transition hover:border-gray-300 hover:text-gray-900 disabled:cursor-not-allowed disabled:opacity-60 dark:border-dark-700 dark:bg-dark-900 dark:text-dark-300 dark:hover:text-white"
            :disabled="loading"
            aria-label="立即刷新"
            @click="reload()"
          >
            <svg viewBox="0 0 20 20" fill="none" class="h-4 w-4" :class="{ 'animate-spin': loading }" aria-hidden="true">
              <path d="M7 5.5 13.5 10 7 14.5V5.5Z" fill="currentColor" />
            </svg>
          </button>
        </div>
      </header>

      <!-- Error -->
      <div
        v-if="errorMessage"
        class="mt-6 rounded-xl border border-red-200 bg-red-50 px-5 py-4 text-sm text-red-700 dark:border-red-500/30 dark:bg-red-500/10 dark:text-red-200"
      >
        {{ errorMessage }}
      </div>

      <!-- Timeline -->
      <div class="mt-7">
        <DegradationTimelineChart v-model:range="rangeHours" :timeline="timeline" />
      </div>

      <!-- Gallery -->
      <section class="mt-9">
        <div class="mb-4 flex flex-wrap items-end justify-between gap-3">
          <div>
            <h2 class="text-base font-semibold text-gray-950 dark:text-white">最新作品</h2>
            <p class="mt-1 text-xs text-gray-500 dark:text-dark-400">
              每隔 {{ intervalMinutes }} 分钟随机抽一个启用中的账号生成，点击缩略图看大图。
            </p>
          </div>
          <span class="font-mono text-xs text-gray-400 dark:text-dark-500">共 {{ total }} 幅</span>
        </div>

        <div v-if="loading && works.length === 0" class="grid gap-x-5 gap-y-6 sm:grid-cols-3 lg:grid-cols-4 xl:grid-cols-5">
          <div v-for="index in placeholderCount" :key="index" class="animate-pulse">
            <div class="h-40 w-full rounded-xl bg-gray-100 dark:bg-dark-800"></div>
            <div class="mt-2.5 h-3 w-20 rounded bg-gray-100 dark:bg-dark-800"></div>
            <div class="mt-2 h-3 w-28 rounded bg-gray-100 dark:bg-dark-800"></div>
          </div>
        </div>

        <div
          v-else-if="works.length === 0"
          class="rounded-xl border border-dashed border-gray-200 px-6 py-14 text-center dark:border-dark-700"
        >
          <p class="text-sm font-medium text-gray-600 dark:text-dark-200">尚无作品</p>
          <p class="mt-2 text-sm text-gray-400 dark:text-dark-400">
            检测正在排队执行，完成一幅画作后会自动出现在这里。
          </p>
        </div>

        <div v-else class="grid gap-x-5 gap-y-6 sm:grid-cols-3 lg:grid-cols-4 xl:grid-cols-5">
          <article v-for="work in works" :key="work.id" class="group">
            <button
              type="button"
              class="relative block h-40 w-full overflow-hidden rounded-xl border border-gray-200 bg-gray-50 transition hover:border-gray-300 dark:border-dark-700 dark:bg-dark-900 dark:hover:border-dark-600"
              :aria-label="`查看第 ${work.id} 幅作品的大图`"
              @click="openLightbox(work.id)"
            >
              <!--
                Images are served as SVG from the backend with a sandbox CSP and
                loaded through <img>, so the browser never executes markup that
                came from a model.
              -->
              <img
                v-if="work.has_image"
                :src="imageURL(work.id)"
                :alt="`${page?.headline || '鹈鹕骑行'} #${work.id}`"
                loading="lazy"
                class="h-full w-full object-contain p-1.5"
              />
              <div v-else class="flex h-full w-full items-center justify-center text-xs text-gray-400">
                生成失败
              </div>
              <span
                v-if="work.has_image"
                class="pointer-events-none absolute inset-x-0 bottom-0 bg-gradient-to-t from-black/55 to-transparent px-2.5 py-1.5 text-[11px] font-medium text-white opacity-0 transition group-hover:opacity-100"
              >
                查看大图
              </span>
            </button>

            <div class="mt-2.5 flex items-center justify-between gap-2">
              <span class="truncate font-mono text-xs text-gray-500 dark:text-dark-400">{{ work.model }}</span>
              <span class="flex-shrink-0 rounded-md bg-gray-100 px-1.5 py-0.5 text-[10px] font-semibold uppercase tracking-wide text-gray-500 dark:bg-dark-800 dark:text-dark-300">
                SVG
              </span>
            </div>

            <div class="mt-1.5 flex items-center justify-between gap-2 font-mono text-[11px] text-blue-600 dark:text-blue-400">
              <span class="truncate">{{ formatClock(work.created_at) }}</span>
              <span class="flex-shrink-0 text-gray-400 dark:text-dark-500">{{ formatDuration(work.duration_ms) }}</span>
            </div>
          </article>
        </div>
      </section>

      <!-- Footer -->
      <footer class="mt-12 border-t border-gray-200 pt-5 dark:border-dark-800">
        <div class="flex flex-wrap items-center justify-between gap-3">
          <p class="text-sm text-gray-500 dark:text-dark-400">
            显示 {{ works.length }} / {{ total }} 幅作品
            <span v-if="timeline?.last_probe_at"> · 最近一次探测 {{ formatClock(timeline.last_probe_at) }}</span>
          </p>
          <a
            class="inline-flex items-center gap-1 text-sm font-medium text-emerald-600 transition hover:text-emerald-700 dark:text-emerald-400"
            :href="imageURL(latestReadyId) || '#'"
            :class="{ 'pointer-events-none opacity-50': !latestReadyId }"
            target="_blank"
            rel="noopener noreferrer"
          >
            最近检测
            <span aria-hidden="true">↗</span>
          </a>
        </div>
      </footer>
    </div>

    <!-- Lightbox: the tile stays small on purpose, the full artwork remains reachable. -->
    <div
      v-if="lightboxId"
      class="fixed inset-0 z-50 flex items-center justify-center bg-black/70 p-4"
      role="dialog"
      aria-modal="true"
      @click.self="closeLightbox"
    >
      <div class="relative max-h-full w-full max-w-2xl overflow-auto rounded-2xl bg-white p-3 shadow-2xl dark:bg-dark-900">
        <img :src="imageURL(lightboxId)" :alt="`作品 #${lightboxId}`" class="mx-auto max-h-[75vh] w-auto" />
        <div class="mt-2 flex items-center justify-between px-1 pb-1">
          <span class="font-mono text-xs text-gray-500 dark:text-dark-400">#{{ lightboxId }}</span>
          <button
            type="button"
            class="rounded-lg px-3 py-1.5 text-sm font-medium text-gray-600 transition hover:bg-gray-100 dark:text-dark-200 dark:hover:bg-dark-800"
            @click="closeLightbox"
          >
            关闭
          </button>
        </div>
      </div>
    </div>
  </div>
</template>

<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import DegradationTimelineChart from './DegradationTimeline.vue'
import {
  publicImageURL,
  publicPage,
  publicTimeline,
  type DegradationPublicPage,
  type DegradationTimeline,
} from '@/api/degradation'

const page = ref<DegradationPublicPage | null>(null)
const timeline = ref<DegradationTimeline | null>(null)
const works = computed(() => page.value?.items ?? [])
const loading = ref(false)
const errorMessage = ref('')
const rangeHours = ref(24)
const lightboxId = ref(0)
const placeholderCount = 10

let timer: ReturnType<typeof setInterval> | null = null
let controller: AbortController | null = null

const total = computed(() => page.value?.total ?? 0)

const intervalMinutes = computed(() => {
  const seconds = page.value?.interval_seconds ?? 600
  return Math.max(Math.round(seconds / 60), 1)
})

const latestReadyId = computed(() => {
  const first = works.value.find((item) => item.has_image)
  return first?.id ?? 0
})

const statusPillText = computed(() => {
  const status = page.value?.last_status ?? ''
  const suffix = status === 'completed' ? '最近生成成功' : status ? '最近生成失败' : '等待首次生成'
  return `${total.value} 幅作品 · ${suffix}`
})

const statusPillClass = computed(() => {
  const status = page.value?.last_status ?? ''
  if (status === 'completed') {
    return 'bg-emerald-50 text-emerald-700 dark:bg-emerald-500/10 dark:text-emerald-300'
  }
  return 'bg-amber-100 text-amber-800 dark:bg-amber-500/15 dark:text-amber-300'
})

const statusDotClass = computed(() => {
  const status = page.value?.last_status ?? ''
  return status === 'completed' ? 'bg-emerald-500' : 'bg-amber-500'
})

function imageURL(id: number | undefined | null): string {
  if (!id) return ''
  return publicImageURL(id)
}

function pad(value: number): string {
  return value.toString().padStart(2, '0')
}

function formatClock(raw: string | null): string {
  if (!raw) return '--'
  const date = new Date(raw)
  if (Number.isNaN(date.getTime())) return '--'
  return `${pad(date.getMonth() + 1)}-${pad(date.getDate())} ${pad(date.getHours())}:${pad(date.getMinutes())}`
}

function formatDuration(milliseconds: number): string {
  if (!milliseconds || milliseconds < 0) return '--'
  return `${(milliseconds / 1000).toFixed(1)} 秒`
}

function openLightbox(id: number) {
  lightboxId.value = id
}

function closeLightbox() {
  lightboxId.value = 0
}

async function loadTimeline(silent = false) {
  try {
    timeline.value = await publicTimeline(rangeHours.value, controller?.signal)
  } catch (error) {
    if ((error as { name?: string })?.name === 'CanceledError') return
    if (!silent) errorMessage.value = '时间轴加载失败，请稍后重试。'
  }
}

async function reload(silent = false) {
  if (controller) {
    controller.abort()
  }
  controller = new AbortController()
  if (!silent) {
    loading.value = true
  }
  try {
    const [pageData] = await Promise.all([
      publicPage(1, 20, controller.signal),
      loadTimeline(true),
    ])
    page.value = pageData
    errorMessage.value = ''
  } catch (error) {
    if ((error as { name?: string })?.name === 'CanceledError') {
      return
    }
    errorMessage.value = '作品加载失败，请稍后重试。'
  } finally {
    loading.value = false
  }
}

function scheduleRefresh() {
  if (timer) {
    clearInterval(timer)
  }
  const seconds = page.value?.interval_seconds && page.value.interval_seconds >= 30
    ? page.value.interval_seconds
    : 600
  timer = setInterval(() => {
    if (!document.hidden) {
      void reload(true)
    }
  }, seconds * 1000)
}

watch(rangeHours, () => {
  void loadTimeline()
})

onMounted(async () => {
  await reload()
  scheduleRefresh()
})

onBeforeUnmount(() => {
  if (timer) {
    clearInterval(timer)
  }
  controller?.abort()
})
</script>
