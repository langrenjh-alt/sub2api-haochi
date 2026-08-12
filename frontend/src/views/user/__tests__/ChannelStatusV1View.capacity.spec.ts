import { describe, expect, it, vi } from 'vitest'
import { defineComponent, h } from 'vue'
import { flushPromises, mount } from '@vue/test-utils'

const { list, capacityPool } = vi.hoisted(() => ({
  list: vi.fn(async () => ({ items: [] })),
  capacityPool: vi.fn(async () => ({
    updated_at: '2026-08-12T00:00:00Z',
    groups: [{ group_id: 1, group_name: 'Public' }],
  })),
}))

vi.mock('@/api/channelMonitor', () => ({
  list,
  status: vi.fn(),
  capacityPool,
}))

vi.mock('vue-i18n', () => ({
  useI18n: () => ({ t: (key: string) => key }),
}))

vi.mock('@/stores/app', () => ({
  useAppStore: () => ({
    cachedPublicSettings: { channel_monitor_enabled: false },
    showError: vi.fn(),
  }),
}))

vi.mock('@/composables/useAutoRefresh', () => ({
  useAutoRefresh: () => ({
    countdown: { value: 30 },
    enabled: { value: false },
    setEnabled: vi.fn(),
    start: vi.fn(),
    stop: vi.fn(),
  }),
}))

vi.mock('@/components/layout/AppLayout.vue', () => ({
  default: defineComponent({
    setup(_, { slots }) {
      return () => h('main', slots.default?.())
    },
  }),
}))

vi.mock('@/components/user/monitor/MonitorHero.vue', () => ({
  default: defineComponent({ setup: () => () => h('div') }),
}))
vi.mock('@/components/user/monitor/MonitorCardGrid.vue', () => ({
  default: defineComponent({ setup: () => () => h('div') }),
}))
vi.mock('@/components/user/MonitorDetailDialog.vue', () => ({
  default: defineComponent({ setup: () => () => h('div') }),
}))
vi.mock('@/components/user/monitor/ChannelCapacityPoolCard.vue', () => ({
  default: defineComponent({
    props: {
      pool: { type: Object, default: null },
      loading: { type: Boolean, default: false },
    },
    setup(props) {
      return () => h('div', {
        'data-testid': 'capacity-pool',
        'data-loading': String(props.loading),
        'data-groups': String((props.pool as { groups?: unknown[] } | null)?.groups?.length ?? 0),
      })
    },
  }),
}))

import ChannelStatusV1View from '../ChannelStatusV1View.vue'

describe('ChannelStatusV1View capacity pool', () => {
  it('loads and renders the fork capacity pool alongside V1 monitors', async () => {
    const wrapper = mount(ChannelStatusV1View)
    await flushPromises()

    expect(list).toHaveBeenCalledOnce()
    expect(capacityPool).toHaveBeenCalledOnce()
    expect(wrapper.get('[data-testid="capacity-pool"]').attributes('data-groups')).toBe('1')
    expect(wrapper.get('[data-testid="capacity-pool"]').attributes('data-loading')).toBe('false')

    wrapper.unmount()
  })
})
