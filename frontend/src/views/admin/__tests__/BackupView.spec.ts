import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, shallowMount } from '@vue/test-utils'
import BackupView from '../BackupView.vue'
import type { BackupRecord } from '@/api/admin/backup'

const backupAPI = vi.hoisted(() => ({
  getS3Config: vi.fn(),
  getSchedule: vi.fn(),
  listBackups: vi.fn(),
  getBackup: vi.fn(),
  updateS3Config: vi.fn(),
  testS3Connection: vi.fn(),
  updateSchedule: vi.fn(),
  createBackup: vi.fn(),
  getDownloadURL: vi.fn(),
  restoreBackup: vi.fn(),
  deleteBackup: vi.fn(),
}))
const showSuccess = vi.hoisted(() => vi.fn())
const showError = vi.hoisted(() => vi.fn())
const showWarning = vi.hoisted(() => vi.fn())

vi.mock('vue-i18n', async () => {
  const actual = await vi.importActual<typeof import('vue-i18n')>('vue-i18n')
  return {
    ...actual,
    useI18n: () => ({ t: (key: string) => key }),
  }
})

vi.mock('@/api', () => ({
  adminAPI: { backup: backupAPI },
}))

vi.mock('@/stores', () => ({
  useAppStore: () => ({ showSuccess, showError, showWarning }),
}))

function backupRecord(overrides: Partial<BackupRecord> = {}): BackupRecord {
  return {
    id: 'backup-1',
    status: 'running',
    backup_type: 'manual',
    file_name: 'backup.sql.gz',
    s3_key: 'backups/backup.sql.gz',
    size_bytes: 0,
    triggered_by: 'manual',
    started_at: '2026-07-15T00:00:00Z',
    ...overrides,
  }
}

describe('BackupView polling', () => {
  beforeEach(() => {
    vi.useFakeTimers()
    vi.spyOn(document, 'hidden', 'get').mockReturnValue(false)
    backupAPI.getS3Config.mockReset().mockResolvedValue({
      endpoint: '',
      region: 'auto',
      bucket: '',
      access_key_id: '',
      prefix: 'backups/',
      force_path_style: false,
    })
    backupAPI.getSchedule.mockReset().mockResolvedValue({
      enabled: false,
      cron_expr: '0 2 * * *',
      retain_days: 14,
      retain_count: 10,
    })
    backupAPI.listBackups.mockReset()
    backupAPI.getBackup.mockReset()
    showSuccess.mockReset()
    showError.mockReset()
    showWarning.mockReset()
  })

  afterEach(() => {
    vi.restoreAllMocks()
    vi.useRealTimers()
  })

  it('keeps one backup poll in flight and ignores its result after unmount', async () => {
    backupAPI.listBackups.mockResolvedValue({ items: [backupRecord()] })
    let resolvePoll!: (record: BackupRecord) => void
    backupAPI.getBackup.mockReturnValue(new Promise<BackupRecord>(resolve => {
      resolvePoll = resolve
    }))
    const wrapper = shallowMount(BackupView)
    await flushPromises()

    vi.advanceTimersByTime(8000)
    await Promise.resolve()
    expect(backupAPI.getBackup).toHaveBeenCalledTimes(1)

    wrapper.unmount()
    resolvePoll(backupRecord({ status: 'completed' }))
    await flushPromises()

    expect(showSuccess).not.toHaveBeenCalled()
    expect(backupAPI.listBackups).toHaveBeenCalledTimes(1)
  })

  it('keeps one restore poll in flight and ignores its result after unmount', async () => {
    backupAPI.listBackups.mockResolvedValue({
      items: [backupRecord({ status: 'completed', restore_status: 'running' })],
    })
    let resolvePoll!: (record: BackupRecord) => void
    backupAPI.getBackup.mockReturnValue(new Promise<BackupRecord>(resolve => {
      resolvePoll = resolve
    }))
    const wrapper = shallowMount(BackupView)
    await flushPromises()

    vi.advanceTimersByTime(8000)
    await Promise.resolve()
    expect(backupAPI.getBackup).toHaveBeenCalledTimes(1)

    wrapper.unmount()
    resolvePoll(backupRecord({ status: 'completed', restore_status: 'completed' }))
    await flushPromises()

    expect(showSuccess).not.toHaveBeenCalled()
    expect(backupAPI.listBackups).toHaveBeenCalledTimes(1)
  })
})
