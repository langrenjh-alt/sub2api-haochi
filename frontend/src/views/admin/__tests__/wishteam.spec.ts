import { mount, flushPromises } from '@vue/test-utils'
import { beforeEach, afterEach, describe, it, expect, vi } from 'vitest'
import Page from '../WishTeamView.vue'
import { wishteam, type WishOverview } from '@/api/wishteam'

vi.mock('@/api/wishteam', () => ({
  wishteam: { overview: vi.fn(), items: vi.fn(), save: vi.fn(), run: vi.fn(), archive: vi.fn() }
}))
const fixture = (): WishOverview => ({
  config: { enabled: false, group_id: 1, interval_minutes: 10, next_run_at: '2026-09-17T01:00:00Z' },
  groups: [{ id: 1, name: '测试分组', count: 3 }],
  provider: 'https://team5x.wishtoapp.com',
  runs: []
})
const wrappers: ReturnType<typeof mount>[] = []
function render() {
  const w = mount(Page, { global: { stubs: {
    AppLayout: { template: '<div><slot /></div>' },
    BaseDialog: { props: ['show', 'title'], template: '<div v-if="show" role="dialog"><h2>{{title}}</h2><slot /><slot name="footer" /></div>' },
    Icon: true
  } } })
  wrappers.push(w); return w
}
function button(w: ReturnType<typeof mount>, label: string) { return w.findAll('button').find(b => b.text().includes(label))! }
beforeEach(() => {
  vi.useFakeTimers(); vi.clearAllMocks()
  vi.mocked(wishteam.overview).mockResolvedValue(fixture())
  vi.mocked(wishteam.items).mockResolvedValue({ items: [], total: 0 })
  vi.mocked(wishteam.save).mockImplementation(async config => ({ ...fixture(), config }))
  vi.mocked(wishteam.run).mockResolvedValue({ run_id: 99 })
  vi.spyOn(document, 'hidden', 'get').mockReturnValue(false)
})
afterEach(() => { wrappers.splice(0).forEach(w => w.unmount()); vi.useRealTimers(); vi.restoreAllMocks() })

describe('WishTeam5X admin page', () => {
  it('loads disabled configuration without starting a task', async () => {
    const w = render(); await flushPromises()
    expect(w.text()).toContain('监控分组设置')
    expect(w.text()).toContain('子号状态巡查')
    expect(w.text()).toContain('任务进度')
    expect(w.text()).toContain('自动巡查已暂停')
    expect(wishteam.run).not.toHaveBeenCalled()
  })
  it('does not overwrite an unsaved interval while polling', async () => {
    const w = render(); await flushPromises()
    await w.get('#wish-interval').setValue('20')
    await vi.advanceTimersByTimeAsync(10000); await flushPromises()
    expect((w.get('#wish-interval').element as HTMLInputElement).value).toBe('20')
    expect(w.text()).toContain('有未保存修改')
    expect(button(w, '立即巡查').attributes('disabled')).toBeDefined()
  })
  it('confirms enabling and persists user interval and group', async () => {
    const w = render(); await flushPromises()
    await w.get('#wish-enabled').setValue(true)
    await w.get('#wish-interval').setValue('15')
    await w.get('form').trigger('submit.prevent')
    expect(wishteam.save).not.toHaveBeenCalled()
    expect(w.get('[role="dialog"]').text()).toContain('发送已选分组的账号凭据')
    await button(w, '确认执行').trigger('click'); await flushPromises()
    expect(wishteam.save).toHaveBeenCalledWith(expect.objectContaining({ enabled: true, group_id: 1, interval_minutes: 15 }))
  })
  it('requires confirmation before a manual run', async () => {
    const w = render(); await flushPromises()
    await button(w, '立即巡查').trigger('click')
    expect(wishteam.run).not.toHaveBeenCalled()
    await button(w, '确认执行').trigger('click'); await flushPromises()
    expect(wishteam.run).toHaveBeenCalledTimes(1)
  })
  it('retains form values and displays failed save', async () => {
    vi.mocked(wishteam.save).mockRejectedValue(new Error('保存失败'))
    const w = render(); await flushPromises()
    await w.get('#wish-interval').setValue('20')
    await w.get('form').trigger('submit.prevent'); await flushPromises()
    expect(w.text()).toContain('保存失败')
    expect((w.get('#wish-interval').element as HTMLInputElement).value).toBe('20')
  })
  it('stops polling in hidden documents and after unmount', async () => {
    const w = render(); await flushPromises()
    vi.spyOn(document, 'hidden', 'get').mockReturnValue(true)
    await vi.advanceTimersByTimeAsync(30000)
    expect(wishteam.overview).toHaveBeenCalledTimes(1)
    w.unmount()
    await vi.advanceTimersByTimeAsync(30000)
    expect(wishteam.overview).toHaveBeenCalledTimes(1)
  })
  it('loads persisted task progress and replacement archive link', async () => {
    const data = fixture()
    data.runs = [{ id: 8, group_id: 1, total: 3, done: 3, status: 'done', alive: 1, revived: 1, replaced: 1, failed: 0, skipped: 0, workspace_dead: 0, message: '本轮巡查完成', created_at: '2026-09-17T00:00:00Z', finished_at: '2026-09-17T00:01:00Z' }]
    vi.mocked(wishteam.overview).mockResolvedValue(data)
    vi.mocked(wishteam.items).mockResolvedValue({ total: 1, items: [{ id: 80, account_id: 10, new_account_id: 11, email: 'child@example.com', status: 'revived', stage: 'done', message: '配置已还原', error_code: '', retry_after: 0, archived: true, probe: { http_status: 401 }, updated_at: '2026-09-17T00:01:00Z' }] })
    const w = render(); await flushPromises()
    expect(w.text()).toContain('100%')
    expect(w.text()).toContain('HTTP 401')
    expect(w.text()).toContain('已还原 · 查看')
    expect(wishteam.items).toHaveBeenCalledWith(8, 1, expect.any(AbortSignal))
  })
})
