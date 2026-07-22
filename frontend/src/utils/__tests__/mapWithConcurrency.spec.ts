import { describe, expect, it } from 'vitest'
import { mapWithConcurrency } from '@/utils/mapWithConcurrency'

describe('mapWithConcurrency', () => {
  it('limits active work and preserves input order', async () => {
    let active = 0
    let maxActive = 0
    const releases: Array<() => void> = []

    const resultPromise = mapWithConcurrency([0, 1, 2, 3, 4], 2, async (value) => {
      active += 1
      maxActive = Math.max(maxActive, active)
      await new Promise<void>(resolve => releases.push(resolve))
      active -= 1
      return value * 10
    })

    await Promise.resolve()
    expect(active).toBe(2)
    releases.shift()?.()
    await Promise.resolve()
    await Promise.resolve()
    expect(active).toBe(2)

    while (releases.length > 0) {
      releases.shift()?.()
      await Promise.resolve()
      await Promise.resolve()
    }

    await expect(resultPromise).resolves.toEqual([0, 10, 20, 30, 40])
    expect(maxActive).toBe(2)
  })

  it('stops scheduling queued work after cancellation', async () => {
    const controller = new AbortController()
    let started = 0
    let release!: () => void

    const resultPromise = mapWithConcurrency([0, 1, 2], 1, async (value) => {
      started += 1
      await new Promise<void>(resolve => { release = resolve })
      return value
    }, controller.signal)

    await Promise.resolve()
    controller.abort(new DOMException('Superseded', 'AbortError'))
    release()

    await expect(resultPromise).rejects.toMatchObject({ name: 'AbortError' })
    expect(started).toBe(1)
  })
})
