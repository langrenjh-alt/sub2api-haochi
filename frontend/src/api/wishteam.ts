import { apiClient } from './client'

export interface WishConfig {
  enabled: boolean
  group_id: number
  interval_minutes: number
  next_run_at: string
}
export interface WishRun {
  id: number
  group_id: number
  status: string
  total: number
  done: number
  alive: number
  replaced: number
  revived: number
  failed: number
  skipped: number
  workspace_dead: number
  message: string
  created_at: string
  finished_at: string | null
}
export interface WishItem {
  id: number
  account_id: number
  new_account_id: number | null
  email: string
  status: string
  stage: string
  message: string
  error_code: string
  retry_after: number
  archived: boolean
  updated_at: string
  probe: { http_status?: number; plan_type?: string; state?: string; reason?: string }
}
export interface WishOverview {
  config: WishConfig
  groups: { id: number; name: string; count: number }[]
  runs: WishRun[]
  provider: string
}
export interface WishArchive {
  id: number
  old_account_id: number
  new_account_id: number
  created_at: string
  settings: Record<string, unknown>
  groups: { group_id: number; priority: number }[]
  all_fields_verified: boolean
}

const base = '/admin/wishteam5x'
export const wishteam = {
  async overview(signal?: AbortSignal) {
    return (await apiClient.get<WishOverview>(base, { signal })).data
  },
  async save(config: WishConfig) {
    return (await apiClient.put<WishOverview>(`${base}/config`, config)).data
  },
  async run() {
    return (await apiClient.post<{ run_id: number }>(`${base}/run`, { confirm: true })).data
  },
  async items(id: number, page: number, signal?: AbortSignal) {
    return (await apiClient.get<{ items: WishItem[]; total: number }>(`${base}/runs/${id}/items`, { params: { page }, signal })).data
  },
  async archive(id: number) {
    return (await apiClient.get<WishArchive>(`${base}/items/${id}/archive`)).data
  }
}
