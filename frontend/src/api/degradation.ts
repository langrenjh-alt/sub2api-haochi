/**
 * Degradation detection API.
 *
 * `admin*` helpers back the group-editor card and the detector overview; the
 * remaining helpers feed the anonymous public page at /jiangzhijiance/ and only
 * ever receive sanitized SVG plus display metadata.
 */

import { apiClient } from './client'

export type ReasoningEffort = 'minimal' | 'low' | 'medium' | 'high' | 'xhigh' | 'max'

export interface DegradationDetectionConfig {
  enabled: boolean
  interval_minutes: number
  model: string
  reasoning_effort: string
  expected_answer?: string
  prompt?: string
  timeout_seconds: number
  suspend_minutes: number
  preview_enabled: boolean
  preview_interval_minutes: number
  preview_model: string
  preview_reasoning_effort: string
}

export interface DegradationGroup {
  group_id: number
  group_name: string
  platform: string
  account_count: number
  config: DegradationDetectionConfig
}

export interface DegradationAccountState {
  account_id: number
  account_name: string
  group_id: number
  schedulable: boolean
  suspended_until: string | null
  suspended_at: string | null
  suspend_note: string
  last_probe_at: string | null
  last_probe_status: string
  last_probe_answer: string
  last_probe_correct: boolean
}

export interface DegradationOverview {
  groups_enabled: number
  accounts_watched: number
  probes_today: number
  degraded_accounts: number
  suspended_accounts: number
  manual_disabled: number
  recovered_today: number
  preview_enabled: boolean
  preview_interval_minutes: number
  preview_works: number
  last_preview_status: string
  interval_minutes: number
  model: string
  reasoning_effort: string
  expected_answer: string
  suspend_minutes: number
  accounts?: DegradationAccountState[]
}

export interface DegradationPublicWork {
  id: number
  account_id: number
  status: string
  model: string
  reasoning_effort: string
  image?: string
  duration_ms: number
  created_at: string
  finished_at: string | null
}

export interface DegradationPublicPage {
  headline: string
  model: string
  reasoning_effort: string
  interval_seconds: number
  total: number
  page: number
  page_size: number
  last_status: string
  last_finished_at: string | null
  items: DegradationPublicWork[]
}

export const DEFAULT_DEGRADATION_CONFIG: DegradationDetectionConfig = {
  enabled: false,
  interval_minutes: 10,
  model: 'gpt-6-astra',
  reasoning_effort: 'medium',
  expected_answer: '21',
  timeout_seconds: 300,
  suspend_minutes: 30,
  preview_enabled: false,
  preview_interval_minutes: 10,
  preview_model: 'gpt-6-astra',
  preview_reasoning_effort: 'low',
}

export async function listGroups(): Promise<DegradationGroup[]> {
  const { data } = await apiClient.get<{ items: DegradationGroup[] }>('/admin/degradation-detection/groups')
  return data.items ?? []
}

export async function updateGroup(groupId: number, config: DegradationDetectionConfig) {
  const { data } = await apiClient.put<{ group_id: number; config: DegradationDetectionConfig }>(
    `/admin/degradation-detection/groups/${groupId}`,
    config
  )
  return data
}

export async function overview(): Promise<DegradationOverview> {
  const { data } = await apiClient.get<DegradationOverview>('/admin/degradation-detection/overview')
  return data
}

export async function runNow(groupId?: number): Promise<{ queued: number }> {
  const { data } = await apiClient.post<{ queued: number }>(
    '/admin/degradation-detection/run',
    null,
    groupId ? { params: { group_id: groupId } } : undefined
  )
  return data
}

export async function publicPage(page = 1, pageSize = 6, signal?: AbortSignal): Promise<DegradationPublicPage> {
  const { data } = await apiClient.get<DegradationPublicPage>('/jiangzhijiance', {
    params: { page, page_size: pageSize },
    signal,
  })
  return data
}

/** Public SVG endpoint. Kept as a URL so the browser can cache the artwork. */
export function publicImageURL(id: number): string {
  return `/api/v1/jiangzhijiance/records/${id}/image`
}

export default {
  listGroups,
  updateGroup,
  overview,
  runNow,
  publicPage,
  publicImageURL,
}
