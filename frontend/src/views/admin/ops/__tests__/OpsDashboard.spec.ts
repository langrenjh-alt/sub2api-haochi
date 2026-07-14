import { flushPromises, shallowMount } from '@vue/test-utils'
import { beforeEach, describe, expect, it, vi } from 'vitest'

import OpsDashboard from '../OpsDashboard.vue'

const mocks = vi.hoisted(() => ({
  adminSettingsStore: {
    opsMonitoringEnabled: true,
    opsQueryModeDefault: 'auto',
    fetch: vi.fn()
  },
  appStore: {
    showError: vi.fn()
  },
  route: {
    query: {} as Record<string, string>
  },
  router: {
    replace: vi.fn()
  },
  pauseCountdown: vi.fn(),
  resumeCountdown: vi.fn(),
  getAdvancedSettings: vi.fn(),
  getMetricThresholds: vi.fn(),
  getDashboardSnapshotV2: vi.fn(),
  getThroughputTrend: vi.fn(),
  getDashboardOverview: vi.fn(),
  getErrorTrend: vi.fn(),
  getLatencyHistogram: vi.fn(),
  getErrorDistribution: vi.fn()
}))

vi.mock('@/stores', () => ({
  useAdminSettingsStore: () => mocks.adminSettingsStore,
  useAppStore: () => mocks.appStore
}))

vi.mock('@/api/admin/ops', () => {
  const opsAPI = {
    getAdvancedSettings: mocks.getAdvancedSettings,
    getMetricThresholds: mocks.getMetricThresholds,
    getDashboardSnapshotV2: mocks.getDashboardSnapshotV2,
    getThroughputTrend: mocks.getThroughputTrend,
    getDashboardOverview: mocks.getDashboardOverview,
    getErrorTrend: mocks.getErrorTrend,
    getLatencyHistogram: mocks.getLatencyHistogram,
    getErrorDistribution: mocks.getErrorDistribution
  }

  return { opsAPI, default: opsAPI }
})

vi.mock('vue-router', () => ({
  useRoute: () => mocks.route,
  useRouter: () => mocks.router
}))

vi.mock('vue-i18n', async () => {
  const actual = await vi.importActual<typeof import('vue-i18n')>('vue-i18n')
  return {
    ...actual,
    useI18n: () => ({
      t: (key: string) => key
    })
  }
})

vi.mock('@vueuse/core', async () => {
  const actual = await vi.importActual<typeof import('@vueuse/core')>('@vueuse/core')
  return {
    ...actual,
    useIntervalFn: () => ({
      pause: mocks.pauseCountdown,
      resume: mocks.resumeCountdown
    })
  }
})

const advancedSettings = {
  display_alert_events: true,
  display_openai_token_stats: false,
  auto_refresh_enabled: true,
  auto_refresh_interval_seconds: 30
}

function deferred<T>() {
  let resolve!: (value: T) => void
  const promise = new Promise<T>((resolvePromise) => {
    resolve = resolvePromise
  })
  return { promise, resolve }
}

describe('OpsDashboard lifecycle', () => {
  beforeEach(() => {
    vi.clearAllMocks()
    mocks.adminSettingsStore.opsMonitoringEnabled = true
    mocks.adminSettingsStore.opsQueryModeDefault = 'auto'
    mocks.route.query = {}

    mocks.adminSettingsStore.fetch.mockResolvedValue(undefined)
    mocks.getAdvancedSettings.mockResolvedValue(advancedSettings)
    mocks.getMetricThresholds.mockResolvedValue(null)
    mocks.getDashboardSnapshotV2.mockResolvedValue({
      overview: {},
      throughput_trend: { points: [] },
      error_trend: { points: [] }
    })
    mocks.getThroughputTrend.mockResolvedValue({ points: [] })
    mocks.getLatencyHistogram.mockResolvedValue({ buckets: [] })
    mocks.getErrorDistribution.mockResolvedValue({ items: [] })
  })

  it('stops mounted initialization when settings loading finishes after unmount', async () => {
    let resolveSettingsFetch!: () => void
    mocks.adminSettingsStore.fetch.mockImplementation(
      () => new Promise<void>((resolve) => {
        resolveSettingsFetch = resolve
      })
    )

    const wrapper = shallowMount(OpsDashboard)
    wrapper.unmount()
    resolveSettingsFetch()
    await flushPromises()

    expect(mocks.getAdvancedSettings).not.toHaveBeenCalled()
    expect(mocks.getDashboardSnapshotV2).not.toHaveBeenCalled()
    expect(mocks.resumeCountdown).not.toHaveBeenCalled()
    expect(mocks.pauseCountdown).toHaveBeenCalledTimes(1)
  })

  it('does not resume the countdown when the initial data fetch finishes after unmount', async () => {
    let resolveSnapshot!: (value: unknown) => void
    let resolveSwitchTrend!: (value: unknown) => void
    mocks.getDashboardSnapshotV2.mockImplementation(
      () => new Promise((resolve) => {
        resolveSnapshot = resolve
      })
    )
    mocks.getThroughputTrend.mockImplementation(
      () => new Promise((resolve) => {
        resolveSwitchTrend = resolve
      })
    )

    const wrapper = shallowMount(OpsDashboard)
    await vi.waitFor(() => {
      expect(mocks.getDashboardSnapshotV2).toHaveBeenCalledTimes(1)
      expect(mocks.getThroughputTrend).toHaveBeenCalledTimes(1)
    })
    await flushPromises()

    const resumeCallsBeforeUnmount = mocks.resumeCountdown.mock.calls.length
    const setupState = (wrapper.vm as any).$?.setupState
    wrapper.unmount()

    resolveSnapshot({
      overview: {},
      throughput_trend: { points: [] },
      error_trend: { points: [] }
    })
    resolveSwitchTrend({ points: [] })
    await flushPromises()

    expect(mocks.resumeCountdown).toHaveBeenCalledTimes(resumeCallsBeforeUnmount)
    expect(mocks.pauseCountdown).toHaveBeenCalledTimes(1)
    expect(setupState.overview).toBeNull()
    expect(setupState.dashboardRefreshToken).toBe(0)
  })

  it('ignores advanced settings that finish after unmount', async () => {
    const settingsRequest = deferred<typeof advancedSettings>()
    mocks.getAdvancedSettings.mockReturnValueOnce(settingsRequest.promise)

    const wrapper = shallowMount(OpsDashboard)
    await vi.waitFor(() => {
      expect(mocks.getAdvancedSettings).toHaveBeenCalledTimes(1)
    })
    const setupState = (wrapper.vm as any).$?.setupState

    wrapper.unmount()
    settingsRequest.resolve(advancedSettings)
    await flushPromises()

    expect(setupState.autoRefreshEnabled).toBe(false)
    expect(setupState.autoRefreshCountdown).toBe(0)
    expect(mocks.getDashboardSnapshotV2).not.toHaveBeenCalled()
    expect(mocks.resumeCountdown).not.toHaveBeenCalled()
    expect(mocks.pauseCountdown).toHaveBeenCalledTimes(1)
  })

  it('keeps the newest advanced settings when an older request finishes later', async () => {
    const olderRequest = deferred<typeof advancedSettings>()
    const newerRequest = deferred<typeof advancedSettings>()
    mocks.getAdvancedSettings
      .mockReturnValueOnce(olderRequest.promise)
      .mockReturnValueOnce(newerRequest.promise)

    const wrapper = shallowMount(OpsDashboard)
    await vi.waitFor(() => {
      expect(mocks.getAdvancedSettings).toHaveBeenCalledTimes(1)
    })
    const setupState = (wrapper.vm as any).$?.setupState

    const newerLoad = setupState.loadDashboardAdvancedSettings()
    expect(mocks.getAdvancedSettings).toHaveBeenCalledTimes(2)
    newerRequest.resolve({
      ...advancedSettings,
      auto_refresh_enabled: false,
      auto_refresh_interval_seconds: 45,
    })
    await newerLoad

    olderRequest.resolve(advancedSettings)
    await flushPromises()

    expect(setupState.autoRefreshEnabled).toBe(false)
    expect(setupState.autoRefreshIntervalMs).toBe(45_000)
    expect(setupState.autoRefreshCountdown).toBe(45)
    expect(mocks.getDashboardSnapshotV2).not.toHaveBeenCalled()
    expect(mocks.resumeCountdown).not.toHaveBeenCalled()
  })
})
