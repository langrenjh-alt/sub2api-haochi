<template>
  <section class="mb-5">
    <div
      v-if="loading && !pool"
      class="rounded-2xl border border-gray-200/80 bg-white/75 p-4 shadow-sm animate-pulse dark:border-dark-700/70 dark:bg-dark-800/60"
    >
      <div class="flex items-center gap-3">
        <div class="h-9 w-9 rounded-xl bg-gray-200 dark:bg-dark-700"></div>
        <div class="flex-1 space-y-2">
          <div class="h-4 w-40 rounded bg-gray-200 dark:bg-dark-700"></div>
          <div class="h-3 w-72 max-w-full rounded bg-gray-100 dark:bg-dark-900/50"></div>
        </div>
      </div>
      <div class="mt-4 grid grid-cols-2 gap-3 md:grid-cols-5">
        <div v-for="i in 5" :key="i" class="h-16 rounded-xl bg-gray-100 dark:bg-dark-900/50"></div>
      </div>
    </div>

    <div
      v-else
      class="rounded-2xl border border-gray-200/80 bg-white/90 p-4 shadow-sm dark:border-dark-700/70 dark:bg-dark-800/80"
    >
      <div class="flex flex-col gap-3 md:flex-row md:items-start md:justify-between">
        <div class="flex items-start gap-3 min-w-0">
          <span class="inline-flex h-9 w-9 shrink-0 items-center justify-center rounded-xl bg-emerald-100 text-emerald-700 dark:bg-emerald-500/15 dark:text-emerald-300">
            <Icon name="database" size="md" />
          </span>
          <div class="min-w-0">
            <h2 class="text-base font-semibold text-gray-900 dark:text-white">
              {{ t('channelStatus.capacityPool.title') }}
            </h2>
            <p class="mt-0.5 text-xs text-gray-500 dark:text-gray-400">
              {{ t('channelStatus.capacityPool.subtitle') }}
            </p>
          </div>
        </div>
        <div class="text-xs text-gray-400 dark:text-gray-500">
          {{ t('channelStatus.capacityPool.updatedAt', { time: updatedAtLabel }) }}
        </div>
      </div>

      <div class="mt-4 grid grid-cols-2 gap-3 lg:grid-cols-5">
        <div
          v-for="stat in summaryStats"
          :key="stat.key"
          class="rounded-xl bg-gray-50 px-3 py-2.5 dark:bg-dark-900/45"
        >
          <div class="text-xs text-gray-500 dark:text-gray-400">
            {{ stat.label }}
          </div>
          <div class="mt-1 text-xl font-bold" :class="stat.className">
            {{ formatInteger(stat.value) }}
          </div>
        </div>
      </div>

      <div class="mt-4 rounded-xl border border-gray-200/80 p-3 dark:border-dark-700/70">
        <div class="flex flex-col gap-3 md:flex-row md:items-start md:justify-between">
          <div>
            <div class="text-sm font-semibold text-gray-800 dark:text-gray-100">
              {{ t('channelStatus.capacityPool.groupPanelTitle') }}
            </div>
            <div class="mt-0.5 text-xs text-gray-500 dark:text-gray-400">
              {{ t('channelStatus.capacityPool.groupPanelSubtitle') }}
            </div>
          </div>
          <div class="flex flex-wrap gap-2">
            <span
              v-for="chip in healthChips"
              :key="chip.key"
              class="inline-flex items-center rounded-lg px-2 py-1 text-xs font-semibold"
              :class="chip.className"
            >
              {{ chip.label }} {{ chip.value }}
            </span>
          </div>
        </div>

        <div
          v-if="groups.length === 0"
          class="mt-3 rounded-xl bg-gray-50 px-4 py-6 text-center text-sm text-gray-500 dark:bg-dark-900/45 dark:text-gray-400"
        >
          {{ t('channelStatus.capacityPool.empty') }}
        </div>

        <div v-else class="mt-3 grid grid-cols-1 gap-3 xl:grid-cols-2">
          <article
            v-for="group in groups"
            :key="group.group_id"
            class="rounded-xl border p-3 transition-colors"
            :class="groupCardClass(group.status)"
          >
            <div class="flex items-start justify-between gap-3">
              <div class="flex min-w-0 items-start gap-2">
                <span class="mt-0.5 inline-flex h-5 w-5 shrink-0 items-center justify-center text-gray-700 dark:text-gray-200">
                  <ProviderIcon :provider="group.platform" :size="18" />
                </span>
                <div class="min-w-0">
                  <h3 class="truncate text-sm font-semibold text-gray-900 dark:text-white" :title="group.group_name">
                    {{ group.group_name }}
                  </h3>
                  <p class="mt-0.5 text-xs text-gray-500 dark:text-gray-400">
                    {{ t('channelStatus.capacityPool.accountMeta', {
                      platform: formatPlatform(group.platform),
                      total: formatInteger(group.account_total),
                      active: formatInteger(group.active_accounts)
                    }) }}
                  </p>
                </div>
              </div>
              <span
                class="shrink-0 rounded-lg px-2 py-0.5 text-xs font-semibold"
                :class="statusChipClass(group.status)"
              >
                {{ groupStatusLabel(group.status) }}
              </span>
            </div>

            <div class="mt-2 space-y-0.5 text-sm font-semibold">
              <div class="text-emerald-700 dark:text-emerald-300">
                {{ t('channelStatus.capacityPool.schedulable') }} {{ formatInteger(group.available_accounts) }}
              </div>
              <div class="text-teal-700 dark:text-teal-300">
                {{ t('channelStatus.capacityPool.concurrencyAvailable') }}
                {{ formatInteger(group.capacity.concurrency.available) }} / {{ formatInteger(group.capacity.concurrency.max) }}
              </div>
            </div>

            <div class="mt-3">
              <div class="mb-1 flex items-center justify-between text-xs font-semibold text-gray-700 dark:text-gray-300">
                <span>{{ t('channelStatus.capacityPool.accountStatus') }}</span>
                <span>{{ t('channelStatus.capacityPool.totalShort') }} {{ formatInteger(group.account_total) }}</span>
              </div>
              <div class="flex h-2.5 overflow-hidden rounded-full bg-gray-200 dark:bg-dark-700">
                <div
                  v-for="segment in statusSegments(group)"
                  :key="segment.key"
                  class="h-full"
                  :class="segment.className"
                  :style="{ width: `${segment.width}%` }"
                ></div>
              </div>
              <div class="mt-2 grid grid-cols-2 gap-x-4 gap-y-1 text-xs md:grid-cols-5">
                <div
                  v-for="segment in visibleStatusSegments(group)"
                  :key="segment.key"
                  class="min-w-0"
                >
                  <div class="flex items-center gap-1.5 text-gray-600 dark:text-gray-300">
                    <span class="h-2 w-2 rounded-full" :class="segment.dotClass"></span>
                    <span class="truncate">{{ segment.label }}</span>
                  </div>
                  <div class="pl-3.5 font-semibold text-gray-800 dark:text-gray-100">
                    {{ formatInteger(segment.count) }}
                  </div>
                </div>
              </div>
            </div>

            <div
              v-if="hasCapacityRows(group)"
              class="mt-3 grid grid-cols-1 gap-2 md:grid-cols-2"
            >
              <CapacityBar
                v-if="group.window_5h.tracked_accounts > 0"
                :label="t('channelStatus.capacityPool.window5h')"
                :percent="group.window_5h.used_percent"
                :footer-left="t('channelStatus.capacityPool.windowSnapshot', {
                  available: formatInteger(group.window_5h.available_accounts),
                  tracked: formatInteger(group.window_5h.tracked_accounts)
                })"
                :footer-right="t('channelStatus.capacityPool.windowRemaining', {
                  value: formatDecimal(group.window_5h.remaining_capacity)
                })"
              />
              <CapacityBar
                v-if="group.window_7d.tracked_accounts > 0"
                :label="t('channelStatus.capacityPool.window7d')"
                :percent="group.window_7d.used_percent"
                :footer-left="t('channelStatus.capacityPool.windowSnapshot', {
                  available: formatInteger(group.window_7d.available_accounts),
                  tracked: formatInteger(group.window_7d.tracked_accounts)
                })"
                :footer-right="t('channelStatus.capacityPool.windowRemaining', {
                  value: formatDecimal(group.window_7d.remaining_capacity)
                })"
              />
              <CapacityBar
                v-if="group.capacity.sessions.max > 0"
                :label="t('channelStatus.capacityPool.sessions')"
                :percent="limitPercent(group.capacity.sessions)"
                :footer-left="`${formatInteger(group.capacity.sessions.used)} / ${formatInteger(group.capacity.sessions.max)}`"
                :footer-right="t('channelStatus.capacityPool.availableShort', {
                  value: formatInteger(group.capacity.sessions.available)
                })"
              />
              <CapacityBar
                v-if="group.capacity.rpm.max > 0"
                :label="t('channelStatus.capacityPool.rpm')"
                :percent="limitPercent(group.capacity.rpm)"
                :footer-left="`${formatInteger(group.capacity.rpm.used)} / ${formatInteger(group.capacity.rpm.max)}`"
                :footer-right="t('channelStatus.capacityPool.availableShort', {
                  value: formatInteger(group.capacity.rpm.available)
                })"
              />
            </div>
          </article>
        </div>
      </div>
    </div>
  </section>
</template>

<script setup lang="ts">
import { computed, defineComponent, h } from 'vue'
import { useI18n } from 'vue-i18n'
import type {
  CapacityLimit,
  CapacityPoolGroup,
  CapacityPoolGroupStatus,
  CapacityPoolResponse,
  CapacityPoolStatusCounts,
} from '@/api/channelMonitor'
import Icon from '@/components/icons/Icon.vue'
import ProviderIcon from './ProviderIcon.vue'

const props = defineProps<{
  pool: CapacityPoolResponse | null
  loading: boolean
}>()

const { t } = useI18n()

const groups = computed(() => props.pool?.groups ?? [])
const summary = computed(() => props.pool?.summary ?? null)

const updatedAtLabel = computed(() => {
  if (!props.pool?.updated_at) return '-'
  const date = new Date(props.pool.updated_at)
  if (Number.isNaN(date.getTime())) return '-'
  return date.toLocaleString()
})

const summaryStats = computed(() => {
  const s = summary.value
  const errorTotal = (s?.error_accounts ?? 0) + (s?.disabled_accounts ?? 0)
  return [
    {
      key: 'total',
      label: t('channelStatus.capacityPool.stats.totalAccounts'),
      value: s?.account_total ?? 0,
      className: 'text-gray-900 dark:text-white',
    },
    {
      key: 'available',
      label: t('channelStatus.capacityPool.stats.availableAccounts'),
      value: s?.available_accounts ?? 0,
      className: 'text-emerald-600 dark:text-emerald-300',
    },
    {
      key: 'rateLimited',
      label: t('channelStatus.capacityPool.stats.rateLimited'),
      value: s?.rate_limited_accounts ?? 0,
      className: 'text-amber-600 dark:text-amber-300',
    },
    {
      key: 'quotaLimited',
      label: t('channelStatus.capacityPool.stats.quotaLimited'),
      value: s?.quota_limited_accounts ?? 0,
      className: 'text-orange-600 dark:text-orange-300',
    },
    {
      key: 'error',
      label: t('channelStatus.capacityPool.stats.errorAccounts'),
      value: errorTotal,
      className: 'text-red-600 dark:text-red-300',
    },
  ]
})

const healthChips = computed(() => {
  const counts = summary.value?.group_health_counts
  return [
    {
      key: 'normal',
      label: t('channelStatus.capacityPool.groupStatus.normal'),
      value: counts?.normal ?? 0,
      className: 'bg-emerald-50 text-emerald-700 dark:bg-emerald-500/15 dark:text-emerald-300',
    },
    {
      key: 'degraded',
      label: t('channelStatus.capacityPool.groupStatus.degraded'),
      value: counts?.degraded ?? 0,
      className: 'bg-amber-50 text-amber-700 dark:bg-amber-500/15 dark:text-amber-300',
    },
    {
      key: 'resource_tight',
      label: t('channelStatus.capacityPool.groupStatus.resource_tight'),
      value: counts?.resource_tight ?? 0,
      className: 'bg-orange-50 text-orange-700 dark:bg-orange-500/15 dark:text-orange-300',
    },
    {
      key: 'unavailable',
      label: t('channelStatus.capacityPool.groupStatus.unavailable'),
      value: counts?.unavailable ?? 0,
      className: 'bg-red-50 text-red-700 dark:bg-red-500/15 dark:text-red-300',
    },
  ]
})

function statusSegments(group: CapacityPoolGroup) {
  const total = Math.max(group.account_total, 1)
  return statusEntries(group.status_counts)
    .filter(segment => segment.count > 0)
    .map(segment => ({
      ...segment,
      width: Math.max((segment.count / total) * 100, 1.5),
    }))
}

function visibleStatusSegments(group: CapacityPoolGroup) {
  return statusEntries(group.status_counts).filter(segment => segment.count > 0)
}

function statusEntries(counts: CapacityPoolStatusCounts) {
  return [
    {
      key: 'normal',
      label: t('channelStatus.capacityPool.accountState.normal'),
      count: counts.normal,
      className: 'bg-teal-400',
      dotClass: 'bg-teal-400',
    },
    {
      key: 'rate_limited',
      label: t('channelStatus.capacityPool.accountState.rateLimited'),
      count: counts.rate_limited,
      className: 'bg-amber-500',
      dotClass: 'bg-amber-500',
    },
    {
      key: 'quota_limited',
      label: t('channelStatus.capacityPool.accountState.quotaLimited'),
      count: counts.quota_limited,
      className: 'bg-yellow-400',
      dotClass: 'bg-yellow-400',
    },
    {
      key: 'error',
      label: t('channelStatus.capacityPool.accountState.error'),
      count: counts.error,
      className: 'bg-red-500',
      dotClass: 'bg-red-500',
    },
    {
      key: 'disabled',
      label: t('channelStatus.capacityPool.accountState.disabled'),
      count: counts.disabled,
      className: 'bg-slate-400',
      dotClass: 'bg-slate-400',
    },
  ]
}

function groupStatusLabel(status: CapacityPoolGroupStatus) {
  return t(`channelStatus.capacityPool.groupStatus.${status}`)
}

function groupCardClass(status: CapacityPoolGroupStatus) {
  switch (status) {
    case 'normal':
      return 'border-gray-200 bg-white dark:border-dark-700 dark:bg-dark-800'
    case 'resource_tight':
      return 'border-orange-200 bg-orange-50/35 dark:border-orange-500/35 dark:bg-orange-500/10'
    case 'degraded':
      return 'border-amber-200 bg-amber-50/35 dark:border-amber-500/35 dark:bg-amber-500/10'
    case 'unavailable':
    default:
      return 'border-red-200 bg-red-50/35 dark:border-red-500/35 dark:bg-red-500/10'
  }
}

function statusChipClass(status: CapacityPoolGroupStatus) {
  switch (status) {
    case 'normal':
      return 'bg-emerald-100 text-emerald-700 dark:bg-emerald-500/15 dark:text-emerald-300'
    case 'resource_tight':
      return 'bg-orange-100 text-orange-700 dark:bg-orange-500/15 dark:text-orange-300'
    case 'degraded':
      return 'bg-amber-100 text-amber-700 dark:bg-amber-500/15 dark:text-amber-300'
    case 'unavailable':
    default:
      return 'bg-red-100 text-red-700 dark:bg-red-500/15 dark:text-red-300'
  }
}

function limitPercent(limit: CapacityLimit) {
  if (!limit.max) return 0
  return Math.min(100, Math.max(0, (limit.used / limit.max) * 100))
}

function hasCapacityRows(group: CapacityPoolGroup) {
  return (
    group.window_5h.tracked_accounts > 0 ||
    group.window_7d.tracked_accounts > 0 ||
    group.capacity.sessions.max > 0 ||
    group.capacity.rpm.max > 0
  )
}

function formatInteger(value: number) {
  return Math.round(value || 0).toLocaleString()
}

function formatDecimal(value: number) {
  return (value || 0).toFixed(2)
}

function formatPlatform(platform: string) {
  if (!platform) return '-'
  const map: Record<string, string> = {
    openai: 'OpenAI',
    anthropic: 'Anthropic',
    gemini: 'Gemini',
    antigravity: 'Antigravity',
    grok: 'Grok',
  }
  return map[platform] ?? platform
}

const CapacityBar = defineComponent({
  name: 'CapacityBar',
  props: {
    label: { type: String, required: true },
    percent: { type: Number, required: true },
    footerLeft: { type: String, required: true },
    footerRight: { type: String, required: true },
  },
  setup(barProps) {
    return () => {
      const pct = Math.min(100, Math.max(0, barProps.percent || 0))
      return h('div', { class: 'rounded-lg bg-white/70 p-2 dark:bg-dark-900/35' }, [
        h('div', { class: 'mb-1 flex items-center justify-between text-xs font-semibold text-gray-700 dark:text-gray-200' }, [
          h('span', barProps.label),
          h('span', `${pct.toFixed(1)}%`),
        ]),
        h('div', { class: 'h-1.5 overflow-hidden rounded-full bg-gray-200 dark:bg-dark-700' }, [
          h('div', {
            class: 'h-full rounded-full bg-emerald-500',
            style: { width: `${pct}%` },
          }),
        ]),
        h('div', { class: 'mt-1 flex items-center justify-between gap-2 text-[11px] text-gray-500 dark:text-gray-400' }, [
          h('span', { class: 'truncate' }, barProps.footerLeft),
          h('span', { class: 'shrink-0' }, barProps.footerRight),
        ]),
      ])
    }
  },
})
</script>
