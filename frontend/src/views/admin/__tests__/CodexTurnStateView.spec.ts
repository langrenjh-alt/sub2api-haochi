import { mount, flushPromises, type VueWrapper } from '@vue/test-utils'
import { beforeEach, afterEach, describe, it, expect, vi } from 'vitest'
import { readFileSync } from 'node:fs'
import Page from '../CodexTurnStateView.vue'
import {
  collectCodexTurnState,
  getCodexTurnState,
  getCodexTurnStateHistory,
  clearCodexTurnStateHistory,
  invalidateCodexTurnState,
  saveCodexTurnStateConfig,
  type CodexTurnStateConfig,
  type CodexTurnStateHistoryPage,
  type CodexTurnStateOverview
} from '@/api/codexTurnState'
import { getAll as getAllProxies } from '@/api/admin/proxies'
import { getAll as getAllGroups } from '@/api/admin/groups'
import type { AdminGroup, Proxy } from '@/types'

vi.mock('@/api/codexTurnState', () => ({
  getCodexTurnState: vi.fn(),
  saveCodexTurnStateConfig: vi.fn(),
  collectCodexTurnState: vi.fn(),
  invalidateCodexTurnState: vi.fn(),
  getCodexTurnStateHistory: vi.fn(),
  clearCodexTurnStateHistory: vi.fn()
}))
// 保留 default 导出，避免 api/admin/index.ts 的聚合导入在测试环境下解析失败。
vi.mock('@/api/admin/proxies', () => ({ getAll: vi.fn(), default: { getAll: vi.fn() } }))
vi.mock('@/api/admin/groups', () => ({ getAll: vi.fn(), default: { getAll: vi.fn() } }))

const now = '2026-09-18T00:20:00Z'
function configFixture(): CodexTurnStateConfig {
  return {
    enabled: true,
    inject_enabled: true,
    inject_header: 'x-codex-turn-state',
    proxy_id: 7,
    group_ids: [89],
    account_ids: [],
    model: 'gpt-5-codex',
    prompt: 'probe prompt',
    ttl_seconds: 1800,
    renew_before_seconds: 300,
    poll_seconds: 60,
    probe_timeout_seconds: 30,
    max_accounts_per_tick: 10,
    revocation_statuses: [401, 403]
  }
}
function fixture(): CodexTurnStateOverview {
  return {
    ...configFixture(),
    proxy_name: '动态IP-1',
    proxy_configured: true,
    enrolled_accounts: 3,
    active_states: 2,
    expiring_soon: 1,
    revoked_states: 1,
    failed_states: 0,
    probes_today: 12,
    harvests_today: 4,
    last_probe_at: now,
    last_probe_status: 'ok',
    last_error: '',
    next_tick_at: '2026-09-18T00:21:00Z',
    accounts: [
      {
        account_id: 101, account_name: 'codex-a', group_id: 89, has_state: true, state_status: 'active',
        state_fingerprint: 'ab12cd34', state_length: 512, issued_at: now, expires_at: '2026-09-18T01:20:00Z',
        remaining_seconds: 3600, source: 'harvest', http_status: 200, latency_ms: 1234, recorded_at: now, error: ''
      },
      {
        account_id: 102, account_name: 'codex-b', group_id: 90, has_state: true, state_status: 'revoked',
        state_fingerprint: 'ef56ab78', state_length: 512, issued_at: now, expires_at: '2026-09-18T00:30:00Z',
        remaining_seconds: 0, source: 'probe', http_status: 401, latency_ms: 800, recorded_at: now, error: 'upstream 401'
      }
    ]
  }
}
function historyFixture(): CodexTurnStateHistoryPage {
  return {
    total: 25,
    page: 1,
    page_size: 20,
    items: [
      {
        id: 1, account_id: 101, account_name: 'codex-a', status: 'active', source: 'harvest', http_status: 200,
        model: 'gpt-5-codex', latency_ms: 1234, state_fingerprint: 'ab12cd34', state_length: 512,
        issued_at: now, expires_at: '2026-09-18T01:20:00Z', error: '', created_at: now
      }
    ]
  }
}
const proxies = [
  { id: 7, name: '动态IP-1', protocol: 'http', host: '1.2.3.4', port: 8080 },
  { id: 9, name: '备用代理', protocol: 'socks5', host: '5.6.7.8', port: 1080 }
] as Proxy[]
const groups = [
  { id: 89, name: '正常组', platform: 'openai' },
  { id: 90, name: '备用组', platform: 'openai' }
] as AdminGroup[]
const wrappers: ReturnType<typeof mount>[] = []
function render() {
  const w = mount(Page, { global: { stubs: { AppLayout: { template: '<div><slot /></div>' }, Icon: true } } })
  wrappers.push(w)
  return w
}
function accountRows(w: VueWrapper) {
  const section = w.findAll('section').find(s => s.text().includes('入池账号') && s.find('table').exists())
  return section!.findAll('tbody tr')
}
function button(w: VueWrapper, text: string) {
  return w.findAll('button').find(b => b.text() === text)
}
beforeEach(() => {
  vi.useFakeTimers(); vi.setSystemTime(now); vi.resetAllMocks()
  vi.mocked(getCodexTurnState).mockResolvedValue(fixture())
  vi.mocked(getCodexTurnStateHistory).mockResolvedValue(historyFixture())
  vi.mocked(clearCodexTurnStateHistory).mockResolvedValue({ deleted: 123, retained: 2 })
  vi.mocked(saveCodexTurnStateConfig).mockResolvedValue(configFixture())
  vi.mocked(collectCodexTurnState).mockResolvedValue({ queued: 1 })
  vi.mocked(invalidateCodexTurnState).mockResolvedValue(undefined)
  vi.mocked(getAllProxies).mockResolvedValue(proxies)
  vi.mocked(getAllGroups).mockResolvedValue(groups)
})
afterEach(() => { wrappers.splice(0).forEach(w => w.unmount()); vi.useRealTimers(); vi.restoreAllMocks() })

describe('292 状态注入管理员页面', () => {
  it('keeps legacy transport and transfer disabled for old settings', async () => {
    const w = render(); await flushPromises()
    expect((w.get('#cts-transfer-enabled').element as HTMLInputElement).checked).toBe(false)
    expect((w.get('#cts-harvest-transport').element as HTMLSelectElement).value).toBe('account')
  })
  it('saves transfer pair, cooldown and independent transport', async () => {
    const w = render(); await flushPromises()
    await w.get('#cts-transfer-enabled').setValue(true)
    await w.get('#cts-ready-group').setValue('89')
    await w.get('#cts-recovery-group').setValue('90')
    await w.get('#cts-cycle-cooldown').setValue('600')
    await w.get('#cts-harvest-transport').setValue('independent')
    await w.get('form').trigger('submit.prevent'); await flushPromises()
    expect(saveCodexTurnStateConfig).toHaveBeenCalledWith(expect.objectContaining({
      transfer_enabled: true, transfer_ready_group_id: 89, transfer_recovery_group_id: 90,
      cycle_cooldown_seconds: 600, harvest_transport: 'independent'
    }))
  })
  it('rejects identical groups and independent transport without proxy', async () => {
    const w = render(); await flushPromises()
    await w.get('#cts-transfer-enabled').setValue(true)
    await w.get('#cts-ready-group').setValue('89')
    await w.get('#cts-recovery-group').setValue('89')
    expect(w.text()).toContain('请选择不同的可用分组 A 和待恢复分组 B')
    await w.get('#cts-recovery-group').setValue('90')
    await w.get('#cts-harvest-transport').setValue('independent')
    await w.get('#cts-proxy').setValue('0')
    expect(w.text()).toContain('独立采集通道必须选择采集代理')
    expect(button(w, '保存配置')!.attributes('disabled')).toBeDefined()
    expect(saveCodexTurnStateConfig).not.toHaveBeenCalled()
  })
  it('shows ticket qualification, transfer history and retry countdown', async () => {
    const data = fixture()
    data.transfer_enabled = true
    data.transfers = [{ id: 1, account_id: 101, from_group_id: 89, to_group_id: 90, ticket_record_id: 0, reason: '没有符合要求的有效绑定票据', created_at: now }]
    Object.assign(data.accounts[0], { ticket_qualified: false, current_group_ids: [90], transfer_status: '待恢复', collection_status: 'cooldown', next_retry_at: '2026-09-18T00:25:00Z' })
    vi.mocked(getCodexTurnState).mockResolvedValue(data)
    const w = render(); await flushPromises()
    expect(w.text()).toContain('300 秒后可重试')
    expect(w.text()).toContain('暂无合格绑定票据')
    expect(w.text()).toContain('最近移组记录')
    expect(w.text()).toContain('没有符合要求的有效绑定票据')
    await vi.advanceTimersByTimeAsync(2000)
    expect(w.text()).toContain('298 秒后可重试')
  })
  it('renders stats, proxy options and account rows from the mocked overview', async () => {
    const w = render(); await flushPromises()
    expect(w.text()).toContain('Codex 状态池')
    expect(w.text()).toContain('入池账号')
    expect(w.text()).toContain('今日探测')
    expect(w.text()).toContain('12')
    expect(w.text()).toContain('动态IP-1 (http://1.2.3.4:8080)')
    expect(w.text()).toContain('codex-a')
    expect(w.text()).toContain('ab12cd34')
    expect(w.text()).toContain('生效中')
    expect(w.text()).toContain('已撤销')
    expect(w.text()).toContain('01:00:00')
    expect(w.text()).toContain('正常组 · #89')
    expect(getCodexTurnState).toHaveBeenCalledTimes(1)
    expect(getCodexTurnStateHistory).toHaveBeenCalledWith(1, 20, 0)
    expect(accountRows(w)).toHaveLength(2)
  })
  it('saves the parsed config including group_ids and account_ids typed as text', async () => {
    const w = render(); await flushPromises()
    const boxes = w.findAll('input[type="checkbox"]:not(#cts-transfer-enabled)')
    await boxes[0].setValue(false)
    await boxes[1].setValue(true)
    await w.get('#cts-account-ids').setValue('300, 301 302')
    await w.get('#cts-ttl').setValue('900')
    await w.get('#cts-proxy').setValue('9')
    await w.get('form').trigger('submit.prevent'); await flushPromises()
    expect(saveCodexTurnStateConfig).toHaveBeenCalledTimes(1)
    const payload = vi.mocked(saveCodexTurnStateConfig).mock.calls[0][0]
    expect(payload.group_ids).toEqual([90])
    expect(payload.account_ids).toEqual([300, 301, 302])
    expect(payload.ttl_seconds).toBe(900)
    expect(payload.proxy_id).toBe(9)
    expect(w.text()).toContain('状态池配置已保存')
    // 保存结束后要重新读取概览，草稿不能残留旧的脏状态。
    expect(getCodexTurnState).toHaveBeenCalledTimes(2)
    expect(button(w, '保存配置')!.attributes('disabled')).toBeDefined()
  })
  it('carries injection mode but never enables degraded shapes', async () => {
    const w = render(); await flushPromises()
    await w.get('#cts-inject-mode').setValue('force')
    await w.get('#cts-inject-models').setValue('gpt-6-astra, GPT-5.*, gpt-6-astra')
    // Toggle 是 <button role="switch">，不是 checkbox（checkbox 是分组多选）。
    await w.get('form').trigger('submit.prevent'); await flushPromises()
    expect(saveCodexTurnStateConfig).toHaveBeenCalledTimes(1)
    const payload = vi.mocked(saveCodexTurnStateConfig).mock.calls[0][0]
    expect(payload.inject_mode).toBe('force')
    expect(payload.allow_degraded_shapes).toBe(false)
    // 去重 + 小写，顺序稳定。
    expect(payload.inject_models).toEqual(['gpt-5.*', 'gpt-6-astra'])
  })
  it('queues collection for one row, for all rows, and invalidates with a confirm guard', async () => {
    const w = render(); await flushPromises()
    await button(w, '立即采集')!.trigger('click'); await flushPromises()
    expect(collectCodexTurnState).toHaveBeenLastCalledWith(101)
    await button(w, '全部立即采集')!.trigger('click'); await flushPromises()
    expect(collectCodexTurnState).toHaveBeenLastCalledWith(0)
    const revokedRow = accountRows(w)[1]
    await revokedRow.findAll('button').find(b => b.text() === '作废')!.trigger('click')
    expect(invalidateCodexTurnState).not.toHaveBeenCalled()
    await revokedRow.findAll('button').find(b => b.text() === '确认作废')!.trigger('click'); await flushPromises()
    expect(invalidateCodexTurnState).toHaveBeenCalledWith(102)
  })
  it('disables save while the config is invalid and reports the reason', async () => {
    const w = render(); await flushPromises()
    await w.get('#cts-ttl').setValue('5')
    expect(w.text()).toContain('TTL 秒需为 60–86400 之间的整数')
    expect(button(w, '保存配置')!.attributes('disabled')).toBeDefined()
    await w.get('form').trigger('submit.prevent'); await flushPromises()
    expect(saveCodexTurnStateConfig).not.toHaveBeenCalled()
    await w.get('#cts-ttl').setValue('1800')
    await w.get('#cts-account-ids').setValue('abc')
    expect(w.text()).toContain('指定账号需为正整数')
    expect(button(w, '保存配置')!.attributes('disabled')).not.toBeUndefined()
    await w.get('#cts-inject-header').setValue('x codex')
    expect(w.text()).toContain('注入请求头名称只能是字母、数字或连字符')
    expect(saveCodexTurnStateConfig).not.toHaveBeenCalled()
  })
  it('pages the history table and stops polling on unmount', async () => {
    const w = render(); await flushPromises()
    expect(w.text()).toContain('1 / 2')
    await button(w, '下一页')!.trigger('click'); await flushPromises()
    expect(getCodexTurnStateHistory).toHaveBeenLastCalledWith(2, 20, 0)
    expect(getCodexTurnState).toHaveBeenCalledTimes(1)
    vi.spyOn(document, 'hidden', 'get').mockReturnValue(true)
    await vi.advanceTimersByTimeAsync(60000)
    expect(getCodexTurnState).toHaveBeenCalledTimes(1)
    w.unmount(); await vi.advanceTimersByTimeAsync(60000)
    expect(getCodexTurnState).toHaveBeenCalledTimes(1)
  })
  it('registers the route guard and adds navigation only in admin items', () => {
    const router = readFileSync('src/router/index.ts', 'utf8')
    expect(router).toMatch(/path: '\/admin\/codex-turn-state'[\s\S]{0,250}requiresAuth: true, requiresAdmin: true/)
    const sidebar = readFileSync('src/components/layout/AppSidebar.vue', 'utf8')
    expect(sidebar.indexOf("path: '/admin/codex-turn-state'")).toBeGreaterThan(sidebar.indexOf('const adminNavItems'))
  })
  it('shows the backend save rejection and keeps unsaved edits', async () => {
    vi.mocked(saveCodexTurnStateConfig).mockRejectedValue({ status: 400, message: '动态IP代理 #7 已删除或不存在，请重新选择代理后保存' })
    const w = render(); await flushPromises()
    await w.get('#cts-ttl').setValue('900')
    await w.get('form').trigger('submit.prevent'); await flushPromises()
    expect(w.text()).toContain('保存失败：动态IP代理 #7 已删除或不存在')
    expect(w.text()).not.toContain('状态池配置已保存')
    expect((w.get('#cts-ttl').element as HTMLInputElement).value).toBe('900')
    expect(getCodexTurnState).toHaveBeenCalledTimes(1)
  })
  it('requires confirmation to clear history and reloads the first page', async () => {
    const w = render(); await flushPromises()
    await button(w, '清空历史')!.trigger('click')
    expect(clearCodexTurnStateHistory).not.toHaveBeenCalled()
    expect(w.text()).toContain('连续采集次数会保留')
    await button(w, '确认清空历史')!.trigger('click'); await flushPromises()
    expect(clearCodexTurnStateHistory).toHaveBeenCalledTimes(1)
    expect(w.text()).toContain('已清理 123 条历史，保留 2 条当前状态/作废标记')
    expect(getCodexTurnStateHistory).toHaveBeenLastCalledWith(1, 20, 0)
    expect(collectCodexTurnState).not.toHaveBeenCalled()
  })
  it('saves the per-account target and attempt limit explicitly', async () => {
    const w = render(); await flushPromises()
    await w.get('#cts-target-length').setValue('332')
    await w.get('#cts-max-attempts').setValue('50')
    await w.get('form').trigger('submit.prevent'); await flushPromises()
    expect(saveCodexTurnStateConfig).toHaveBeenCalledWith(expect.objectContaining({ target_state_length: 332, max_attempts_per_cycle: 50 }))
  })
  it('saves concurrency and rapid retry independently of the scan interval', async () => {
    const w = render(); await flushPromises()
    await w.get('#cts-concurrency').setValue('32')
    await w.get('#cts-retry-ms').setValue('200')
    await w.get('form').trigger('submit.prevent'); await flushPromises()
    expect(saveCodexTurnStateConfig).toHaveBeenCalledWith(expect.objectContaining({ concurrent_accounts: 32, retry_interval_ms: 200, poll_seconds: 60 }))
    expect(w.text()).toContain('入队后连续采集，不等待轮询')
    expect(w.text()).not.toContain('按轮询节奏采集')
  })
  it('rejects excessive concurrency before saving', async () => {
    const w = render(); await flushPromises()
    await w.get('#cts-concurrency').setValue('65')
    await w.get('form').trigger('submit.prevent'); await flushPromises()
    expect(saveCodexTurnStateConfig).not.toHaveBeenCalled()
    expect(w.text()).toContain('并发采集账号数需为 1–64')
  })
  it('identifies a deleted proxy instead of presenting a blank selection', async () => {
    vi.mocked(getAllProxies).mockResolvedValue([proxies[1]])
    const w = render(); await flushPromises()
    expect(w.text()).toContain('已删除或不可用的代理 #7')
    await w.get('#cts-ttl').setValue('900')
    expect(button(w, '保存配置')!.attributes('disabled')).toBeDefined()
    await w.get('#cts-proxy').setValue('9')
    expect(button(w, '保存配置')!.attributes('disabled')).toBeUndefined()
  })
  it('allows disabling the pool with a deleted proxy', async () => {
    vi.mocked(getAllProxies).mockResolvedValue([proxies[1]])
    vi.mocked(saveCodexTurnStateConfig).mockResolvedValue({ ...configFixture(), enabled: false })
    const w = render(); await flushPromises()
    await w.get('#cts-enabled').trigger('click'); await flushPromises()
    expect(saveCodexTurnStateConfig).toHaveBeenCalledWith(expect.objectContaining({ enabled: false, proxy_id: 7 }))
    expect(w.text()).toContain('已停用 292 状态注入')
  })
  it('does not claim rejection or success when a save response is lost', async () => {
    vi.mocked(saveCodexTurnStateConfig).mockRejectedValue({ status: 0, message: 'Network error' })
    const w = render(); await flushPromises()
    await w.get('#cts-ttl').setValue('900')
    await w.get('form').trigger('submit.prevent'); await flushPromises()
    expect(w.text()).toContain('未收到保存确认')
    expect(w.text()).not.toContain('状态池配置已保存')
  })
})
