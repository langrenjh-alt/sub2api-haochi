import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { getBatchImageJob, listBatchImageJobs } from '@/api/batchImage'

vi.mock('@/api/client', () => ({
  buildGatewayUrl: (path: string) => `http://gateway.test${path}`,
}))

describe('getBatchImageJob', () => {
  beforeEach(() => {
    vi.useFakeTimers()
  })

  afterEach(() => {
    vi.clearAllTimers()
    vi.unstubAllGlobals()
    vi.useRealTimers()
  })

  it('coalesces concurrent status requests for the same batch', async () => {
    let resolveFetch!: (response: Response) => void
    const fetchMock = vi.fn().mockReturnValue(new Promise<Response>((resolve) => {
      resolveFetch = resolve
    }))
    vi.stubGlobal('fetch', fetchMock)

    const first = getBatchImageJob('api-key', 'batch-1')
    const second = getBatchImageJob('api-key', 'batch-1')

    expect(first).toBe(second)
    expect(fetchMock).toHaveBeenCalledTimes(1)

    resolveFetch(new Response(JSON.stringify({ id: 'batch-1', status: 'running' }), {
      status: 200,
      headers: { 'Content-Type': 'application/json' },
    }))
    await expect(first).resolves.toMatchObject({ id: 'batch-1', status: 'running' })
  })

  it('aborts a status request that stays pending', async () => {
    const fetchMock = vi.fn().mockImplementation((_url: string, init?: RequestInit) => (
      new Promise<Response>((_resolve, reject) => {
        init?.signal?.addEventListener('abort', () => reject(init.signal?.reason), { once: true })
      })
    ))
    vi.stubGlobal('fetch', fetchMock)

    const request = getBatchImageJob('api-key', 'batch-timeout')
    const rejection = expect(request).rejects.toMatchObject({ name: 'TimeoutError' })
    await vi.advanceTimersByTimeAsync(30_000)

    await rejection
    expect(fetchMock).toHaveBeenCalledTimes(1)
  })

  it('keeps the timeout active while the response body is pending', async () => {
    let requestSignal: AbortSignal | undefined
    const fetchMock = vi.fn()
      .mockImplementationOnce((_url: string, init?: RequestInit) => {
        requestSignal = init?.signal ?? undefined
        return Promise.resolve({
          ok: true,
          json: () => new Promise((_resolve, reject) => {
            requestSignal?.addEventListener('abort', () => reject(requestSignal?.reason), { once: true })
          }),
        } as Response)
      })
      .mockResolvedValueOnce(new Response(JSON.stringify({ id: 'batch-body', status: 'running' }), {
        status: 200,
        headers: { 'Content-Type': 'application/json' },
      }))
    vi.stubGlobal('fetch', fetchMock)

    const first = getBatchImageJob('api-key', 'batch-body')
    const rejection = expect(first).rejects.toMatchObject({ name: 'TimeoutError' })
    await vi.advanceTimersByTimeAsync(30_000)

    expect(requestSignal?.aborted).toBe(true)
    await rejection
    await expect(getBatchImageJob('api-key', 'batch-body')).resolves.toMatchObject({
      id: 'batch-body',
      status: 'running',
    })
    expect(fetchMock).toHaveBeenCalledTimes(2)
  })

  it('preserves timeout errors while parsing a non-success response body', async () => {
    let requestSignal: AbortSignal | undefined
    vi.stubGlobal('fetch', vi.fn().mockImplementation((_url: string, init?: RequestInit) => {
      requestSignal = init?.signal ?? undefined
      return Promise.resolve({
        ok: false,
        status: 500,
        statusText: 'Internal Server Error',
        headers: new Headers(),
        json: () => new Promise((_resolve, reject) => {
          requestSignal?.addEventListener('abort', () => reject(requestSignal?.reason), { once: true })
        }),
      } as Response)
    }))

    const request = getBatchImageJob('api-key', 'batch-error-body')
    const rejection = expect(request).rejects.toMatchObject({ name: 'TimeoutError' })
    await vi.advanceTimersByTimeAsync(30_000)

    expect(requestSignal?.aborted).toBe(true)
    await rejection
  })
})

describe('listBatchImageJobs', () => {
  afterEach(() => {
    vi.unstubAllGlobals()
  })

  it('forwards cancellation to the list request', async () => {
    let requestSignal: AbortSignal | undefined
    const fetchMock = vi.fn().mockImplementation((_url: string, init?: RequestInit) => (
      new Promise<Response>((_resolve, reject) => {
        requestSignal = init?.signal ?? undefined
        requestSignal?.addEventListener('abort', () => reject(requestSignal?.reason), { once: true })
      })
    ))
    vi.stubGlobal('fetch', fetchMock)
    const controller = new AbortController()

    const request = listBatchImageJobs('api-key', { limit: 20 }, { signal: controller.signal })
    controller.abort(new DOMException('Superseded', 'AbortError'))

    await expect(request).rejects.toMatchObject({ name: 'AbortError' })
    expect(requestSignal).toBe(controller.signal)
  })
})
