import { apiClient } from './client'

export type RunwayPolicy = 'unknown_full_v1' | 'known_only'
export interface RunwayInventory {
  known_remaining_equivalents: number
  assumed_remaining_equivalents: number
  remaining_equivalents: number
  known_accounts: number
  unknown_accounts: number
  coverage_percent: number
  idle_cached_accounts: number
  unknown_reasons: Record<string, number>
}
export interface RunwayResult {
  generated_at: string
  forecast_policy: RunwayPolicy
  method_version: string
  candidates: number
  week: RunwayInventory
  five_hour: RunwayInventory
  burn: {
    window_start_at: string; window_end_at: string; segment_count: number; complete: boolean
    delta_equivalents: number; equivalents_per_hour: number; paired_accounts: number
    current_paired_accounts: number; consuming_accounts: number; updated_accounts: number
    reset_pairs_excluded: number; anomalous_pairs: number; coverage_percent: number
    segments: { start_at: string; end_at: string; delta_equivalents: number; paired_accounts: number; reset_pairs_excluded: number }[]
  }
  forecast: {
    status: string; confidence: string; remaining_hours: number | null; known_hours: number | null
    available_until: string | null; over_seven_days: boolean
  }
  excluded_records: Record<string, number>
  duplicate_records_collapsed: number
  mixed_plans: boolean
  batches: { at: string; candidates: number; known: number; unknown: number; remaining_equivalents: number }[]
}
export interface RunwaySnapshot {
  schema_version: string
  generated_at: string | null
  next_calculation_at: string
  age_seconds: number
  stale: boolean
  available: boolean
  interval_seconds: number
  group: { id: number; name: string }
  forecast_policy: RunwayPolicy
  method_version: string
  collection_error: string
  data: RunwayResult | null
  history: RunwayResult[]
}
export async function getPoolRunway(policy: RunwayPolicy): Promise<RunwaySnapshot> {
  const { data } = await apiClient.get<RunwaySnapshot>('/admin/pool-runway', { params: { policy } })
  return data
}

export interface RunwayConfig {
  group_id: number
  revision: number
  source: 'page' | 'environment' | 'default'
  effective_group: { id: number; name: string }
  groups: { id: number; name: string }[]
}
export async function getPoolRunwayConfig(): Promise<RunwayConfig> {
  const { data } = await apiClient.get<RunwayConfig>('/admin/pool-runway/config')
  return data
}
export async function savePoolRunwayConfig(group_id: number, revision: number): Promise<{ group_id: number; revision: number }> {
  const { data } = await apiClient.put<{ group_id: number; revision: number }>('/admin/pool-runway/config', { group_id, revision })
  return data
}
