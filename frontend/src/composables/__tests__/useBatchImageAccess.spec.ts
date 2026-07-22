import { beforeEach, describe, expect, it, vi } from 'vitest'

const mocks = vi.hoisted(() => ({
  list: vi.fn(),
  auth: {
    isAuthenticated: true,
    user: { id: 1 },
    sessionGeneration: 1,
  }
}))

vi.mock('@/api/keys', () => ({
  keysAPI: { list: mocks.list }
}))

vi.mock('@/stores/auth', () => ({
  useAuthStore: () => mocks.auth
}))

async function loadComposable() {
  const { useBatchImageAccess } = await import('../useBatchImageAccess')
  return useBatchImageAccess()
}

describe('useBatchImageAccess', () => {
  beforeEach(() => {
    vi.resetModules()
    mocks.list.mockReset()
    mocks.auth.isAuthenticated = true
    mocks.auth.user = { id: 1 }
    mocks.auth.sessionGeneration = 1
  })

  it('stops after the first page when the reported page count is invalid', async () => {
    mocks.list
      .mockResolvedValueOnce({
        items: [{ status: 'active', group: { platform: 'openai', allow_batch_image_generation: false } }],
        page: 1,
        page_size: 100,
        total: 1,
        pages: Infinity
      })
      .mockResolvedValueOnce({ items: [], page: 2, page_size: 100, total: 1, pages: Infinity })
    const access = await loadComposable()

    await expect(access.refreshBatchImageAccess(true)).resolves.toBe(false)
    expect(mocks.list).toHaveBeenCalledTimes(1)
  })

  it('becomes reactive when an allowed result loads after the initial false read', async () => {
    mocks.list.mockResolvedValueOnce({
      items: [{
        status: 'active',
        group: { platform: 'gemini', allow_batch_image_generation: true }
      }],
      page: 1,
      page_size: 100,
      total: 1,
      pages: 1
    })
    const access = await loadComposable()

    expect(access.canUseBatchImage.value).toBe(false)
    await expect(access.refreshBatchImageAccess(true)).resolves.toBe(true)
    expect(access.canUseBatchImage.value).toBe(true)
  })

  it('shares an in-flight load with a forced refresh', async () => {
    let resolveList!: (value: unknown) => void
    mocks.list.mockReturnValue(new Promise((resolve) => {
      resolveList = resolve
    }))
    const access = await loadComposable()

    const first = access.refreshBatchImageAccess()
    const forced = access.refreshBatchImageAccess(true)
    expect(mocks.list).toHaveBeenCalledTimes(1)

    resolveList({ items: [], page: 1, page_size: 100, total: 0, pages: 1 })
    await expect(Promise.all([first, forced])).resolves.toEqual([false, false])
    expect(mocks.list).toHaveBeenCalledTimes(1)
  })

  it('ignores a stale result after the authenticated user changes', async () => {
    const resolvers: Array<(value: unknown) => void> = []
    mocks.list.mockImplementation(() => new Promise((resolve) => {
      resolvers.push(resolve)
    }))
    const access = await loadComposable()

    const firstUserLoad = access.refreshBatchImageAccess(true)
    mocks.auth.user = { id: 2 }
    const secondUserLoad = access.refreshBatchImageAccess(true)
    expect(mocks.list).toHaveBeenCalledTimes(2)

    resolvers[1]({ items: [], page: 1, page_size: 100, total: 0, pages: 1 })
    await expect(secondUserLoad).resolves.toBe(false)
    resolvers[0]({
      items: [{
        status: 'active',
        group: { platform: 'openai', allow_batch_image_generation: false }
      }],
      page: 1,
      page_size: 100,
      total: 2,
      pages: 2
    })
    await expect(firstUserLoad).resolves.toBe(false)

    expect(mocks.list).toHaveBeenCalledTimes(2)
    expect(access.canUseBatchImage.value).toBe(false)
  })

  it('does not reuse cached access after the same user starts a new session', async () => {
    mocks.list
      .mockResolvedValueOnce({
        items: [{
          status: 'active',
          group: { platform: 'gemini', allow_batch_image_generation: true }
        }],
        page: 1,
        page_size: 100,
        total: 1,
        pages: 1
      })
      .mockResolvedValueOnce({ items: [], page: 1, page_size: 100, total: 0, pages: 1 })
    const access = await loadComposable()

    await expect(access.refreshBatchImageAccess(true)).resolves.toBe(true)
    mocks.auth.sessionGeneration = 2
    await expect(access.refreshBatchImageAccess()).resolves.toBe(false)

    expect(mocks.list).toHaveBeenCalledTimes(2)
    expect(access.canUseBatchImage.value).toBe(false)
  })
})
