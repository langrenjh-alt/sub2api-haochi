import { mount, flushPromises } from '@vue/test-utils'
import { beforeEach, afterEach, describe, it, expect, vi } from 'vitest'
import { readFileSync } from 'node:fs'
import Page from '../PoolRunwayView.vue'
import { getPoolRunway, getPoolRunwayConfig, savePoolRunwayConfig, type RunwaySnapshot, type RunwayConfig } from '@/api/poolRunway'

vi.mock('@/api/poolRunway', () => ({ getPoolRunway: vi.fn(), getPoolRunwayConfig: vi.fn(), savePoolRunwayConfig: vi.fn() }))
const configFixture = (): RunwayConfig => ({ group_id: 89, revision: 2, source: 'page', effective_group: { id: 89, name: '正常组' }, groups: [{ id: 89, name: '正常组' }, { id: 90, name: '另一个组' }] })
const now = '2026-09-18T00:20:00Z'
function fixture(): RunwaySnapshot {
  const inventory = { known_remaining_equivalents: 176.12, assumed_remaining_equivalents: 256, remaining_equivalents: 432.12, known_accounts: 315, unknown_accounts: 256, coverage_percent: 55.17, idle_cached_accounts: 0, unknown_reasons: { missing: 256 } }
  const data: NonNullable<RunwaySnapshot['data']> = {
    generated_at: now, forecast_policy: 'unknown_full_v1', method_version: 'adjacent20m-equivalent-v1', candidates: 571,
    week: inventory, five_hour: { ...inventory, assumed_remaining_equivalents: 0 },
    burn: { window_start_at: '2026-09-18T00:00:00Z', window_end_at: now, segment_count: 4, complete: true, delta_equivalents: 15.36, equivalents_per_hour: 46.08, paired_accounts: 315, current_paired_accounts: 315, consuming_accounts: 315, updated_accounts: 315, reset_pairs_excluded: 0, anomalous_pairs: 0, coverage_percent: 100, segments: [] },
    forecast: { status: 'estimated', confidence: 'low', remaining_hours: 9.3776, known_hours: 3.822, available_until: '2026-09-18T09:42:39Z', over_seven_days: false },
    excluded_records: { key_or_other_type: 2, shadow: 3 }, duplicate_records_collapsed: 5, mixed_plans: true,
    batches: Array.from({ length: 25 }, (_, i) => ({ at: `2026-09-17T00:${String(i).padStart(2, '0')}:00Z`, candidates: 1, known: 1, unknown: 0, remaining_equivalents: 0.5 }))
  }
  return { schema_version: '1', generated_at: now, next_calculation_at: '2026-09-18T00:25:00Z', age_seconds: 0, stale: false, available: true, interval_seconds: 300, group: { id: 89, name: '正常组' }, forecast_policy: 'unknown_full_v1', method_version: data.method_version, collection_error: '', data, history: [data] }
}
const wrappers: ReturnType<typeof mount>[] = []
function render() {
  const w = mount(Page, { global: { stubs: { AppLayout: { template: '<div><slot /></div>' }, Icon: true } } })
  wrappers.push(w); return w
}
beforeEach(() => {
  vi.useFakeTimers(); vi.setSystemTime(now); vi.resetAllMocks()
  vi.mocked(getPoolRunway).mockResolvedValue(fixture())
  vi.mocked(getPoolRunwayConfig).mockResolvedValue(configFixture())
  vi.mocked(savePoolRunwayConfig).mockResolvedValue({ group_id: 90, revision: 3 })
})
afterEach(() => { wrappers.splice(0).forEach(w => w.unmount()); vi.useRealTimers(); vi.restoreAllMocks() })

describe('号池续航管理员页面', () => {
  it('shows measured and assumed inventory separately with the exact policy title', async () => {
    const w = render(); await flushPromises()
    expect(w.text()).toContain('预计可用至（未知按满额估算）')
    expect(w.text()).toContain('9 小时 23 分钟')
    expect(w.text()).toContain('低置信度')
    for (const value of ['176.12', '256', '432.12', '46.08', '571']) expect(w.text()).toContain(value)
    expect(w.text()).toContain('检测到混合套餐')
    expect(getPoolRunway).toHaveBeenCalledTimes(1)
  })
  it('refresh only reads the snapshot', async () => {
    const w = render(); await flushPromises()
    await w.findAll('button').find(b => b.text() === '刷新快照')!.trigger('click'); await flushPromises()
    expect(getPoolRunway).toHaveBeenCalledTimes(2)
  })
  it('retains last successful inventory on failure but suppresses a current ETA', async () => {
    const w = render(); await flushPromises()
    vi.mocked(getPoolRunway).mockRejectedValue(new Error('network'))
    await w.findAll('button').find(b => b.text() === '刷新快照')!.trigger('click'); await flushPromises()
    expect(w.text()).toContain('432.12'); expect(w.text()).toContain('历史估计 / 等待更新')
    expect(w.get('[aria-labelledby="runway-eta"]').text()).not.toContain('9 小时 23 分钟')
  })
  it('ages out even when automatic refresh fails or the tab is hidden', async () => {
    const w = render(); await flushPromises()
    vi.spyOn(document, 'hidden', 'get').mockReturnValue(true)
    await vi.advanceTimersByTimeAsync(13 * 60 * 1000)
    expect(w.text()).toContain('历史估计 / 等待更新')
    expect(getPoolRunway).toHaveBeenCalledTimes(1)
  })
  it('changes policy without combining history series', async () => {
    const w = render(); await flushPromises()
    const next = fixture(); next.forecast_policy = 'known_only'; next.data!.forecast_policy = 'known_only'; next.data!.week.assumed_remaining_equivalents = 0
    vi.mocked(getPoolRunway).mockResolvedValue(next)
    await w.get('#runway-policy').setValue('known_only'); await flushPromises()
    expect(getPoolRunway).toHaveBeenLastCalledWith('known_only')
    expect(w.text()).toContain('预计可用至（仅计算已知部分）')
    expect(w.text()).not.toContain('预计可用至（未知按满额估算）')
  })
  it('paginates batches and stops polling on unmount', async () => {
    const w = render(); await flushPromises()
    expect(w.text()).toContain('1 / 2')
    await w.findAll('button').find(b => b.text() === '下一页')!.trigger('click')
    expect(w.text()).toContain('2 / 2')
    w.unmount(); await vi.advanceTimersByTimeAsync(120000)
    expect(getPoolRunway).toHaveBeenCalledTimes(1)
  })
  it('warming and no burn do not imply infinite inventory', async () => {
    const f = fixture(); f.data!.forecast.status = 'no_observed_burn'; f.data!.forecast.remaining_hours = null; f.data!.forecast.available_until = null
    vi.mocked(getPoolRunway).mockResolvedValue(f); const w = render(); await flushPromises()
    expect(w.text()).toContain('未观测到正消耗'); expect(w.text()).not.toContain('无限')
  })
  it('registers route guard and adds navigation only in admin items', () => {
    const router = readFileSync('src/router/index.ts', 'utf8')
    expect(router).toMatch(/path: '\/admin\/pool-runway'[\s\S]{0,250}requiresAuth: true, requiresAdmin: true/)
    const sidebar = readFileSync('src/components/layout/AppSidebar.vue', 'utf8')
    expect(sidebar.indexOf("path: '/admin/pool-runway'")).toBeGreaterThan(sidebar.indexOf('const adminNavItems'))
  })
  it('loads saved group and does not save merely on selection or polling', async () => {
    const w = render(); await flushPromises()
    expect((w.get('#runway-group').element as HTMLSelectElement).value).toBe('89')
    await w.get('#runway-group').setValue('90')
    await w.findAll('button').find(b => b.text() === '刷新快照')!.trigger('click'); await flushPromises()
    expect((w.get('#runway-group').element as HTMLSelectElement).value).toBe('90')
    expect(w.text()).toContain('有未保存修改')
    expect(w.text()).toContain('当前监控：正常组')
    expect(savePoolRunwayConfig).not.toHaveBeenCalled()
  })
  it('saves explicit selection with revision and clears the previous group inventory', async () => {
    const w = render(); await flushPromises()
    await w.get('#runway-group').setValue('90')
    vi.mocked(getPoolRunwayConfig).mockResolvedValue({ ...configFixture(), group_id: 90, revision: 3, effective_group: { id: 90, name: '另一个组' } })
    vi.mocked(getPoolRunway).mockResolvedValue({ ...fixture(), group: { id: 90, name: '另一个组' }, data: null, history: [], generated_at: null })
    await w.get('form').trigger('submit.prevent'); await flushPromises()
    expect(savePoolRunwayConfig).toHaveBeenCalledTimes(1)
    expect(savePoolRunwayConfig).toHaveBeenCalledWith(90, 2)
    expect(w.text()).toContain('监控分组已保存')
    expect(w.text()).toContain('当前监控：另一个组')
    expect(w.text()).toContain('积累样本中')
    expect(w.text()).not.toContain('432.12')
  })
  it('preserves draft and reports a conflicting save without showing an old ETA', async () => {
    const w = render(); await flushPromises()
    await w.get('#runway-group').setValue('90')
    vi.mocked(savePoolRunwayConfig).mockRejectedValue({ status: 409 })
    await w.get('form').trigger('submit.prevent'); await flushPromises()
    expect(w.text()).toContain('配置已变更或正在采样')
    expect((w.get('#runway-group').element as HTMLSelectElement).value).toBe('90')
    expect(w.text()).not.toContain('432.12')
    expect(w.text()).not.toContain('监控分组已保存')
  })
  it('supports an unconfigured pool without silently choosing the first group', async () => {
    vi.mocked(getPoolRunwayConfig).mockResolvedValue({ ...configFixture(), group_id: 0, revision: 0, source: 'default', effective_group: { id: 0, name: '' } })
    vi.mocked(getPoolRunway).mockResolvedValue({ ...fixture(), group: { id: 0, name: '' }, data: null, history: [], generated_at: null })
    const w = render(); await flushPromises()
    expect((w.get('#runway-group').element as HTMLSelectElement).value).toBe('0')
    expect(w.text()).toContain('请选择监控分组')
    expect(w.findAll('button').find(b => b.text() === '保存分组')!.attributes('disabled')).toBeDefined()
    expect(savePoolRunwayConfig).not.toHaveBeenCalled()
  })
  it('handles unavailable group-list and deleted selection', async () => {
    vi.mocked(getPoolRunwayConfig).mockRejectedValue(new Error('network'))
    const w = render(); await flushPromises()
    expect(w.text()).toContain('读取分组配置失败')
    expect(w.get('#runway-group').attributes('disabled')).toBeDefined()
    vi.mocked(getPoolRunwayConfig).mockResolvedValue({ ...configFixture(), group_id: 91, effective_group: { id: 0, name: '' } })
    await w.findAll('button').find(b => b.text() === '刷新快照')!.trigger('click'); await flushPromises()
    expect(w.text()).toContain('分组 #91 已不可用')
  })
})
