/**
 * Degradation detection API.
 *
 * `admin*` helpers back the group-editor card and the detector overview; the
 * remaining helpers feed the anonymous public page at /jiangzhijiance/ and only
 * ever receive sanitized SVG plus display metadata.
 */

import { apiClient } from './client'
import { buildApiUrl } from './url'

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
  move_on_degraded: boolean
  move_target_group_id: number
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
	/** True when the row has a renderable artwork; the SVG body is fetched per tile. */
	has_image: boolean
	duration_ms: number
  created_at: string
  finished_at: string | null
}

export interface DegradationPublicPage {
  enabled: boolean
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

export interface DegradationSample {
 id: number
 status: string
 state: string
 duration_ms: number
 output_tokens: number | null
 model: string
 created_at: string
 finished_at: string | null
}
export interface DegradationTimelineBucket {
 sample?: DegradationSample
 state?: string
	start: string
	total: number
	correct: number
	degraded: number
	undetermined: number
}

export interface DegradationTimeline {
 mode?: string
 interval_minutes?: number
 next_probe_at?: string | null
 latest_sample?: DegradationSample | null
 running?: boolean
  reset_at?: string | null
	range_hours: number
	bucket_minutes: number
	generated_at: string
	total: number
	correct: number
	degraded: number
	undetermined: number
	healthy_ratio: number
	current_state: 'healthy' | 'degraded' | 'unknown' | string
	suspended_accounts: number
	last_probe_at: string | null
	buckets: DegradationTimelineBucket[]
}

export async function resetPublicStats(): Promise<{ reset_at: string }> {
  const { data } = await apiClient.post<{ reset_at: string }>('/admin/degradation-detection/stats/reset', { confirm: true })
  return data
}

/** Admin view of the artworks the public page renders. */
export interface DegradationWorkPage {
	total: number
	page: number
	page_size: number
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
  move_on_degraded: false,
  move_target_group_id: 0,
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

/** Admin: the artworks currently on the public page. */
export async function listWorks(page = 1, pageSize = 24): Promise<DegradationWorkPage> {
	const { data } = await apiClient.get<DegradationWorkPage>('/admin/degradation-detection/works', {
		params: { page, page_size: pageSize },
	})
	return data
}

/** Admin: remove one artwork from the public page. */
export async function deleteWork(id: number): Promise<{ deleted: number }> {
	const { data } = await apiClient.delete<{ deleted: number }>(`/admin/degradation-detection/works/${id}`)
	return data
}

/** Admin: clear the whole public feed. */
export async function purgeWorks(): Promise<{ deleted: number }> {
	const { data } = await apiClient.post<{ deleted: number }>('/admin/degradation-detection/works/purge')
	return data
}

export async function publicPage(page = 1, pageSize = 6, signal?: AbortSignal): Promise<DegradationPublicPage> {
  const { data } = await apiClient.get<DegradationPublicPage>('/jiangzhijiance', {
    params: { page, page_size: pageSize },
    signal,
  })
  return data
}

/** Public verdict history behind the timeline chart. */
export async function publicTimeline(hours = 24, signal?: AbortSignal): Promise<DegradationTimeline> {
	const { data } = await apiClient.get<DegradationTimeline>('/jiangzhijiance/timeline', {
		params: { hours },
		signal,
	})
	return data
}

/** Admin thumbnails stay available for archived/unpublished works via authenticated fetch. */
export async function adminWorkImage(id: number, signal?: AbortSignal): Promise<Blob> {
  const { data } = await apiClient.get<Blob>(`/admin/intelligent-tests/records/${id}/image`, { responseType: 'blob', signal })
  return data
}

/** Public SVG endpoint. Kept as a URL so the browser can cache the artwork. */
export function publicImageURL(id: number): string {
  return buildApiUrl(`/jiangzhijiance/records/${id}/image`)
}

export async function publicAnimation(id: number, signal?: AbortSignal): Promise<{ document: string; animated: boolean }> {
  const { data } = await apiClient.get<{ document: string; animated: boolean }>(`/jiangzhijiance/records/${id}/animation`, { signal })
  return data
}

export default {
	listWorks,
	deleteWork,
	purgeWorks,
	listGroups,
  updateGroup,
  overview,
  runNow,
  publicPage,
  publicImageURL,
}
