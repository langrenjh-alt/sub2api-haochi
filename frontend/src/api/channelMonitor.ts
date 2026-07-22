/**
 * User-facing Channel Monitor API endpoints
 * Read-only views for end users to inspect channel availability/status.
 */

import { apiClient } from './client'
import type { Provider, MonitorStatus } from './admin/channelMonitor'

export type { Provider, MonitorStatus } from './admin/channelMonitor'

export interface UserMonitorExtraModel {
  model: string
  status: MonitorStatus
  latency_ms: number | null
}

export interface MonitorTimelinePoint {
  status: MonitorStatus
  latency_ms: number | null
  ping_latency_ms: number | null
  checked_at: string
}

export interface UserMonitorView {
  id: number
  name: string
  provider: Provider
  group_name: string
  primary_model: string
  primary_status: MonitorStatus
  primary_latency_ms: number | null
  primary_ping_latency_ms: number | null
  availability_7d: number
  extra_models: UserMonitorExtraModel[]
  timeline: MonitorTimelinePoint[]
}

export interface UserMonitorListResponse {
  items: UserMonitorView[]
}

export interface UserMonitorModelDetail {
  model: string
  latest_status: MonitorStatus
  latest_latency_ms: number | null
  availability_7d: number
  availability_15d: number
  availability_30d: number
  avg_latency_7d_ms: number | null
}

export interface UserMonitorDetail {
  id: number
  name: string
  provider: Provider
  group_name: string
  models: UserMonitorModelDetail[]
}

export type CapacityPoolGroupStatus = 'normal' | 'degraded' | 'resource_tight' | 'unavailable'

export interface CapacityPoolStatusCounts {
  normal: number
  rate_limited: number
  quota_limited: number
  error: number
  disabled: number
}

export interface CapacityPoolHealthCounts {
  normal: number
  degraded: number
  resource_tight: number
  unavailable: number
}

export interface CapacityLimit {
  used: number
  max: number
  available: number
}

export interface CapacityLimitSnapshot {
  concurrency: CapacityLimit
  sessions: CapacityLimit
  rpm: CapacityLimit
}

export interface CapacityWindowSummary {
  label: string
  tracked_accounts: number
  available_accounts: number
  used_percent: number
  remaining_capacity: number
}

export interface CapacityPoolSummary {
  group_total: number
  account_total: number
  active_accounts: number
  available_accounts: number
  rate_limited_accounts: number
  quota_limited_accounts: number
  error_accounts: number
  disabled_accounts: number
  status_counts: CapacityPoolStatusCounts
  group_health_counts: CapacityPoolHealthCounts
  capacity: CapacityLimitSnapshot
}

export interface CapacityPoolGroup {
  group_id: number
  group_name: string
  platform: string
  status: CapacityPoolGroupStatus
  account_total: number
  active_accounts: number
  available_accounts: number
  status_counts: CapacityPoolStatusCounts
  capacity: CapacityLimitSnapshot
  window_5h: CapacityWindowSummary
  window_7d: CapacityWindowSummary
}

export interface CapacityPoolResponse {
  updated_at: string
  summary: CapacityPoolSummary
  groups: CapacityPoolGroup[]
}

/**
 * List all monitor views available to the current user.
 */
export async function list(options?: { signal?: AbortSignal }): Promise<UserMonitorListResponse> {
  const { data } = await apiClient.get<UserMonitorListResponse>('/channel-monitors', {
    signal: options?.signal,
  })
  return data
}

/**
 * Get detailed status (multi-window availability + latency) for a single monitor.
 */
export async function status(id: number): Promise<UserMonitorDetail> {
  const { data } = await apiClient.get<UserMonitorDetail>(`/channel-monitors/${id}/status`)
  return data
}

/**
 * Get public capacity pool status grouped by public standard groups.
 */
export async function capacityPool(options?: { signal?: AbortSignal }): Promise<CapacityPoolResponse> {
  const { data } = await apiClient.get<CapacityPoolResponse>('/channel-monitors/capacity-pool', {
    signal: options?.signal,
  })
  return data
}

export const channelMonitorUserAPI = {
  list,
  status,
  capacityPool,
}

export default channelMonitorUserAPI
