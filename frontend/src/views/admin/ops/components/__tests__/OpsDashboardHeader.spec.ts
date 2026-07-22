import { flushPromises, shallowMount } from '@vue/test-utils'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import OpsDashboardHeader from '../OpsDashboardHeader.vue'

const { getGroups, getRealtimeTrafficSummary, setOpsRealtimeMonitoringEnabledLocal, adminSettingsStore } = vi.hoisted(() => ({
  getGroups: vi.fn(),
  getRealtimeTrafficSummary: vi.fn(),
  setOpsRealtimeMonitoringEnabledLocal: vi.fn(),
  adminSettingsStore: {
    opsRealtimeMonitoringEnabled: true,
  },
}))

vi.mock('@/api', () => ({
  adminAPI: {
    groups: {
      getAll: getGroups,
    },
  },
}))

vi.mock('@/api/admin/ops', () => ({
  opsAPI: {
    getRealtimeTrafficSummary,
  },
}))

vi.mock('@/stores', () => ({
  useAdminSettingsStore: () => ({
    ...adminSettingsStore,
    setOpsRealtimeMonitoringEnabledLocal,
  }),
}))

vi.mock('vue-i18n', async (importOriginal) => {
  const actual = await importOriginal<typeof import('vue-i18n')>()
  return {
    ...actual,
    useI18n: () => ({
      t: (key: string) => key,
    }),
  }
})

function deferred<T>() {
  let resolve!: (value: T) => void
  const promise = new Promise<T>((resolvePromise) => {
    resolve = resolvePromise
  })
  return { promise, resolve }
}

function realtimeResponse(platform: string, groupId: number, currentQps: number) {
  return {
    enabled: true,
    summary: {
      window: '1min',
      start_time: '2026-07-14T00:00:00Z',
      end_time: '2026-07-14T00:01:00Z',
      platform,
      group_id: groupId,
      qps: { current: currentQps, peak: currentQps, avg: currentQps },
      tps: { current: 0, peak: 0, avg: 0 },
    },
  }
}

describe('OpsDashboardHeader realtime traffic', () => {
  beforeEach(() => {
    getGroups.mockReset()
    getRealtimeTrafficSummary.mockReset()
    setOpsRealtimeMonitoringEnabledLocal.mockReset()
    adminSettingsStore.opsRealtimeMonitoringEnabled = true
    getGroups.mockResolvedValue([])
    getRealtimeTrafficSummary.mockResolvedValue(realtimeResponse('openai', 1, 10))
  })

  it('waits for the parent refresh token after toolbar filters change', async () => {
    const firstRequest = deferred<ReturnType<typeof realtimeResponse>>()
    getRealtimeTrafficSummary
      .mockReturnValueOnce(firstRequest.promise)
      .mockResolvedValueOnce(realtimeResponse('anthropic', 2, 2222.2))

    const wrapper = shallowMount(OpsDashboardHeader, {
      props: {
        platform: 'openai',
        groupId: 1,
        timeRange: '1h',
        queryMode: 'auto',
        loading: false,
        lastUpdated: null,
        refreshToken: 0,
      },
    })

    expect(getRealtimeTrafficSummary).toHaveBeenCalledTimes(1)
    expect(getRealtimeTrafficSummary).toHaveBeenNthCalledWith(1, '1min', 'openai', 1)

    await wrapper.setProps({ platform: 'anthropic', groupId: 2 })

    expect(getRealtimeTrafficSummary).toHaveBeenCalledTimes(1)

    const setupState = (wrapper.vm as any).$?.setupState
    expect(setupState.realtimeTrafficLoading).toBe(false)

    firstRequest.resolve(realtimeResponse('openai', 1, 1111.1))
    await flushPromises()

    expect(setupState.realtimeTrafficSummary).toBeNull()

    await wrapper.setProps({ refreshToken: 1 })
    await flushPromises()

    expect(getRealtimeTrafficSummary).toHaveBeenCalledTimes(2)
    expect(getRealtimeTrafficSummary).toHaveBeenNthCalledWith(2, '1min', 'anthropic', 2)
    expect(setupState.realtimeTrafficSummary.qps.current).toBe(2222.2)
  })

  it('loads directly for a realtime window change but not for a toolbar-driven reset', async () => {
    const wrapper = shallowMount(OpsDashboardHeader, {
      props: {
        platform: 'openai',
        groupId: 1,
        timeRange: '1h',
        queryMode: 'auto',
        loading: false,
        lastUpdated: null,
        refreshToken: 0,
      },
    })
    await flushPromises()

    const setupState = (wrapper.vm as any).$?.setupState
    setupState.realtimeWindow = '5min'
    await flushPromises()

    expect(getRealtimeTrafficSummary).toHaveBeenCalledTimes(2)
    expect(getRealtimeTrafficSummary).toHaveBeenNthCalledWith(2, '5min', 'openai', 1)

    await wrapper.setProps({ timeRange: '5m', refreshToken: 1 })
    await flushPromises()

    expect(setupState.realtimeWindow).toBe('1min')
    expect(getRealtimeTrafficSummary).toHaveBeenCalledTimes(3)
    expect(getRealtimeTrafficSummary).toHaveBeenNthCalledWith(3, '1min', 'openai', 1)
  })

  it('defers a manual toolbar refresh to the parent refresh token', async () => {
    const wrapper = shallowMount(OpsDashboardHeader, {
      props: {
        platform: 'openai',
        groupId: 1,
        timeRange: '1h',
        queryMode: 'auto',
        loading: false,
        lastUpdated: null,
        refreshToken: 0,
      },
    })
    await flushPromises()

    const setupState = (wrapper.vm as any).$?.setupState
    setupState.handleToolbarRefresh()
    await flushPromises()

    expect(wrapper.emitted('refresh')).toHaveLength(1)
    expect(getRealtimeTrafficSummary).toHaveBeenCalledTimes(1)

    await wrapper.setProps({ refreshToken: 1 })
    await flushPromises()

    expect(getRealtimeTrafficSummary).toHaveBeenCalledTimes(2)
  })

  it('coalesces a parent refresh token with the same in-flight realtime query', async () => {
    const request = deferred<ReturnType<typeof realtimeResponse>>()
    getRealtimeTrafficSummary.mockReturnValueOnce(request.promise)

    const wrapper = shallowMount(OpsDashboardHeader, {
      props: {
        platform: 'openai',
        groupId: 1,
        timeRange: '1h',
        queryMode: 'auto',
        loading: false,
        lastUpdated: null,
        refreshToken: 0,
      },
    })
    expect(getRealtimeTrafficSummary).toHaveBeenCalledTimes(1)

    await wrapper.setProps({ refreshToken: 1 })
    expect(getRealtimeTrafficSummary).toHaveBeenCalledTimes(1)

    request.resolve(realtimeResponse('openai', 1, 10))
    await flushPromises()
    const setupState = (wrapper.vm as any).$?.setupState
    expect(setupState.realtimeTrafficLoading).toBe(false)
    expect(setupState.realtimeTrafficSummary.qps.current).toBe(10)
  })

  it('shares the original keyed request when the realtime window changes A to B to A', async () => {
    const oneMinuteRequest = deferred<ReturnType<typeof realtimeResponse>>()
    const fiveMinuteRequest = deferred<ReturnType<typeof realtimeResponse>>()
    getRealtimeTrafficSummary
      .mockReturnValueOnce(oneMinuteRequest.promise)
      .mockReturnValueOnce(fiveMinuteRequest.promise)

    const wrapper = shallowMount(OpsDashboardHeader, {
      props: {
        platform: 'openai',
        groupId: 1,
        timeRange: '1h',
        queryMode: 'auto',
        loading: false,
        lastUpdated: null,
        refreshToken: 0,
      },
    })
    const setupState = (wrapper.vm as any).$?.setupState

    setupState.realtimeWindow = '5min'
    setupState.realtimeWindow = '1min'
    await flushPromises()
    expect(getRealtimeTrafficSummary).toHaveBeenCalledTimes(2)

    oneMinuteRequest.resolve(realtimeResponse('openai', 1, 11))
    fiveMinuteRequest.resolve(realtimeResponse('openai', 1, 55))
    await flushPromises()
    expect(setupState.realtimeTrafficSummary.qps.current).toBe(11)
  })

  it('clears a completed summary as soon as toolbar filters invalidate it', async () => {
    const wrapper = shallowMount(OpsDashboardHeader, {
      props: {
        platform: 'openai',
        groupId: 1,
        timeRange: '1h',
        queryMode: 'auto',
        loading: false,
        lastUpdated: null,
        refreshToken: 0,
      },
    })
    await flushPromises()
    const setupState = (wrapper.vm as any).$?.setupState
    expect(setupState.realtimeTrafficSummary).not.toBeNull()

    await wrapper.setProps({ platform: 'anthropic', groupId: 2 })
    expect(setupState.realtimeTrafficSummary).toBeNull()
  })

  it('invalidates a realtime response that finishes after unmount', async () => {
    const request = deferred<ReturnType<typeof realtimeResponse>>()
    getRealtimeTrafficSummary.mockReturnValueOnce(request.promise)

    const wrapper = shallowMount(OpsDashboardHeader, {
      props: {
        platform: 'openai',
        groupId: 1,
        timeRange: '1h',
        queryMode: 'auto',
        loading: false,
        lastUpdated: null,
        refreshToken: 0,
      },
    })
    const setupState = (wrapper.vm as any).$?.setupState

    wrapper.unmount()
    request.resolve({
      ...realtimeResponse('openai', 1, 1111.1),
      enabled: false,
    })
    await flushPromises()

    expect(setupState.realtimeTrafficLoading).toBe(false)
    expect(setupState.realtimeTrafficSummary).toBeNull()
    expect(setOpsRealtimeMonitoringEnabledLocal).not.toHaveBeenCalled()
  })
})
