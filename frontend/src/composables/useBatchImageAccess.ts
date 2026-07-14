import { computed, ref } from 'vue'
import { keysAPI } from '@/api/keys'
import { useAuthStore } from '@/stores/auth'
import type { ApiKey } from '@/types'

const loaded = ref(false)
const loading = ref(false)
const hasAllowedBatchImageKey = ref(false)
const cachedUserId = ref<number | null>(null)
const cachedSessionGeneration = ref<number | null>(null)
let loadGeneration = 0
let pendingLoad: {
  userId: number
  sessionGeneration: number
  generation: number
  promise: Promise<boolean>
} | null = null
const pageSize = 100
const maxPagesToScan = 1000

function keyAllowsBatchImage(key: ApiKey): boolean {
  return (
    key.status === 'active' &&
    key.group?.platform === 'gemini' &&
    key.group?.allow_batch_image_generation === true
  )
}

async function loadBatchImageAccess(force = false): Promise<boolean> {
  const authStore = useAuthStore()
  const userId = authStore.user?.id
  const sessionGeneration = authStore.sessionGeneration
  if (!authStore.isAuthenticated || typeof userId !== 'number' || !Number.isSafeInteger(userId)) {
    if (cachedUserId.value !== null || cachedSessionGeneration.value !== null) {
      cachedUserId.value = null
      cachedSessionGeneration.value = null
      loadGeneration += 1
    }
    pendingLoad = null
    loading.value = false
    loaded.value = true
    hasAllowedBatchImageKey.value = false
    return false
  }

  if (cachedUserId.value !== userId || cachedSessionGeneration.value !== sessionGeneration) {
    cachedUserId.value = userId
    cachedSessionGeneration.value = sessionGeneration
    loadGeneration += 1
    loaded.value = false
    hasAllowedBatchImageKey.value = false
  }

  if (loaded.value && !force) {
    return hasAllowedBatchImageKey.value
  }

  if (
    pendingLoad?.userId === userId &&
    pendingLoad.sessionGeneration === sessionGeneration &&
    pendingLoad.generation === loadGeneration
  ) {
    return pendingLoad.promise
  }

  loading.value = true
  const generation = loadGeneration
  const isCurrentLoad = () => (
    cachedUserId.value === userId &&
    cachedSessionGeneration.value === sessionGeneration &&
    loadGeneration === generation &&
    authStore.isAuthenticated &&
    authStore.user?.id === userId &&
    authStore.sessionGeneration === sessionGeneration
  )
  const request = (async () => {
    let page = 1
    let totalPages = 1
    while (page <= totalPages && page <= maxPagesToScan) {
      if (!isCurrentLoad()) return false
      const response = await keysAPI.list(page, pageSize, {
        status: 'active',
        sort_by: 'created_at',
        sort_order: 'desc'
      })
      if (!isCurrentLoad()) return false

      if (page === 1) {
        const reportedPages = Number(response.pages)
        totalPages = Number.isSafeInteger(reportedPages) && reportedPages > 0
          ? Math.min(reportedPages, maxPagesToScan)
          : 1
      }

      if ((response.items || []).some(keyAllowsBatchImage)) {
        if (isCurrentLoad()) {
          hasAllowedBatchImageKey.value = true
          loaded.value = true
        }
        return true
      }

      if (page >= totalPages || (response.items || []).length === 0) {
        if (isCurrentLoad()) {
          hasAllowedBatchImageKey.value = false
          loaded.value = true
        }
        return false
      }

      page += 1
    }

    if (isCurrentLoad()) {
      hasAllowedBatchImageKey.value = false
      loaded.value = true
    }
    return false
  })()
    .catch(() => {
      if (isCurrentLoad()) {
        hasAllowedBatchImageKey.value = false
        loaded.value = true
      }
      return false
    })
  const trackedLoad = { userId, sessionGeneration, generation, promise: request }
  pendingLoad = trackedLoad
  void request.finally(() => {
    if (pendingLoad === trackedLoad) {
      loading.value = false
      pendingLoad = null
    }
  })

  return request
}

export function useBatchImageAccess() {
  const authStore = useAuthStore()
  const sessionMatchesCache = computed(() => (
    authStore.isAuthenticated &&
    typeof authStore.user?.id === 'number' &&
    cachedUserId.value === authStore.user.id &&
    cachedSessionGeneration.value === authStore.sessionGeneration
  ))
  const canUseBatchImage = computed(() => sessionMatchesCache.value && hasAllowedBatchImageKey.value)

  return {
    canUseBatchImage,
    batchImageAccessLoaded: computed(() => !authStore.isAuthenticated || (sessionMatchesCache.value && loaded.value)),
    batchImageAccessLoading: computed(() => sessionMatchesCache.value && loading.value),
    refreshBatchImageAccess: loadBatchImageAccess,
  }
}
