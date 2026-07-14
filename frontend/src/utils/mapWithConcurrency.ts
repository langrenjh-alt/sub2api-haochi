function throwIfAborted(signal?: AbortSignal) {
  if (!signal?.aborted) return
  throw signal.reason ?? new DOMException('Operation aborted', 'AbortError')
}

export async function mapWithConcurrency<T, R>(
  values: readonly T[],
  concurrency: number,
  mapper: (value: T, index: number) => Promise<R>,
  signal?: AbortSignal,
): Promise<R[]> {
  if (values.length === 0) return []

  const results = new Array<R>(values.length)
  const workerCount = Math.min(Math.max(1, Math.floor(concurrency)), values.length)
  let nextIndex = 0

  const worker = async () => {
    while (nextIndex < values.length) {
      throwIfAborted(signal)
      const index = nextIndex
      nextIndex += 1
      results[index] = await mapper(values[index], index)
    }
  }

  await Promise.all(Array.from({ length: workerCount }, () => worker()))
  return results
}
