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
        class="mt-8 rounded-xl border border-red-200 bg-red-50 px-5 py-4 text-sm text-red-700 dark:border-red-500/30 dark:bg-red-500/10 dark:text-red-200"
      >
        {{ errorMessage }}
      </div>

      <!-- Grid -->
      <section class="mt-9">
        <div v-if="loading && works.length === 0" class="grid gap-x-6 gap-y-9 sm:grid-cols-2 lg:grid-cols-3">
          <div v-for="index in placeholderCount" :key="index" class="animate-pulse">
            <div class="aspect-[4/3] w-full rounded-xl bg-gray-100 dark:bg-dark-800"></div>
            <div class="mt-3 h-3 w-24 rounded bg-gray-100 dark:bg-dark-800"></div>
            <div class="mt-2 h-3 w-32 rounded bg-gray-100 dark:bg-dark-800"></div>
          </div>
        </div>

        <div
          v-else-if="works.length === 0"
          class="rounded-xl border border-dashed border-gray-200 px-6 py-16 text-center dark:border-dark-700"
        >
          <p class="text-sm font-medium text-gray-600 dark:text-dark-200">尚无作品</p>
          <p class="mt-2 text-sm text-gray-400 dark:text-dark-400">
            检测正在排队执行，完成一幅画作后会自动出现在这里。
          </p>
        </div>

        <div v-else class="grid gap-x-6 gap-y-9 sm:grid-cols-2 lg:grid-cols-3">
          <article v-for="work in works" :key="work.id" class="group">
            <div class="overflow-hidden rounded-xl border border-gray-200 bg-gray-50 dark:border-dark-700 dark:bg-dark-900">
              <div class="aspect-[4/3] w-full">
                <!--
                  Images are served as SVG from the backend with a sandbox CSP and
                  loaded through <img>, so the browser never executes markup that
                  came from a model.
                -->
                <img
                  v-if="work.image"
                  :src="imageURL(work.id)"
                  :alt="`${page?.headline || '鹈鹕骑行'} #${work.id}`"
                  loading="lazy"
                  class="h-full w-full object-contain"
                />
                <div v-else class="flex h-full w-full items-center justify-center text-xs text-gray-400">
                  生成失败
                </div>
              </div>
            </div>

            <div class="mt-3 flex items-center justify-between gap-3">
              <span class="truncate text-sm font-medium text-gray-900 dark:text-white">
                {{ page?.headline || '鹈鹕骑行' }}
              </span>
              <span class="flex-shrink-0 rounded-md bg-gray-100 px-2 py-0.5 text-[11px] font-semibold uppercase tracking-wide text-gray-500 dark:bg-dark-800 dark:text-dark-300">
                SVG
              </span>
            </div>

            <div class="mt-1.5 flex items-center justify-between gap-3">
              <span class="truncate font-mono text-xs text-gray-400 dark:text-dark-400">{{ work.model }}</span>
              <span class="flex-shrink-0 font-mono text-xs text-blue-600 dark:text-blue-400">
                {{ formatClock(work.created_at) }}
              </span>
            </div>

            <div class="mt-1 flex items-center justify-between gap-3">
              <span class="flex-shrink-0 font-mono text-xs text-blue-600 dark:text-blue-400">
                {{ formatClock(work.finished_at || work.created_at) }}
              </span>
              <span class="flex-shrink-0 text-xs text-gray-400 dark:text-dark-400">
                {{ formatDuration(work.duration_ms) }}
              </span>
            </div>
          </article>
        </div>
      </section>

      <!-- Footer -->
      <footer class="mt-12 border-t border-gray-200 pt-5 dark:border-dark-800">
        <div class="flex flex-wrap items-center justify-between gap-3">
          <p class="text-sm text-gray-500 dark:text-dark-400">
            显示 {{ works.length }} / {{ total }} 幅作品
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
  </div>
</template>

<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref } from 'vue'
import { publicImageURL, publicPage, type DegradationPublicPage } from '@/api/degradation'

const page = ref<DegradationPublicPage | null>(null)
const works = computed(() => page.value?.items ?? [])
const loading = ref(false)
const errorMessage = ref('')
const placeholderCount = 6

let timer: ReturnType<typeof setInterval> | null = null
let controller: AbortController | null = null

const total = computed(() => page.value?.total ?? 0)

const latestReadyId = computed(() => {
  const first = works.value.find((item) => item.image)
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

async function reload(silent = false) {
  if (controller) {
    controller.abort()
  }
  controller = new AbortController()
  if (!silent) {
    loading.value = true
  }
  try {
    page.value = await publicPage(1, 6, controller.signal)
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
