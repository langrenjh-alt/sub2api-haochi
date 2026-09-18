/**
 * 292 状态注入（Codex 状态池）API。
 *
 * 后端通过动态 IP 代理探测上游 `x-codex-turn-state`，并把命中的状态注入到
 * Codex 账号请求；本模块只暴露管理端读写接口，指纹字段永远不是原始 token。
 */

import { apiClient } from './client'

/** 单个入池账号的状态快照。 */
export interface CodexTurnStateAccountRow {
  ticket_qualified?: boolean
  transfer_status?: string
  transfer_error?: string
  next_retry_at?: string | null
  current_group_ids?: number[]
  account_id: number
  account_name: string
  group_id: number
  has_state: boolean
  state_status: string // 'active' | 'expired' | 'revoked' | 'failed' | 'none'
  state_fingerprint: string // short display fingerprint, never the raw token
  state_length: number
  issued_at: string | null
  expires_at: string | null
  remaining_seconds: number
  source: string // 'harvest' | 'probe' | 'manual' | ''
  http_status: number
  latency_ms: number
  recorded_at: string | null
  error: string
  probe_attempts?: number
  attempts_limit_reached?: boolean
  collection_status?: string
}

/** 可保存的状态池配置。 */
export interface CodexTurnStateConfig {
  transfer_enabled?: boolean
  transfer_ready_group_id?: number
  transfer_recovery_group_id?: number
  cycle_cooldown_seconds?: number
  harvest_transport?: 'account' | 'independent'
  enabled: boolean
  inject_enabled: boolean
  /** fill_empty = 只补空白头；force = 覆盖客户端自带的状态（唯一能把降智轮次拉回正常路由的模式）。 */
  inject_mode: string
  /** 旧客户端兼容字段；服务端固定为 false，只接受正常目标长度。 */
  allow_degraded_shapes: boolean
  /** 注入的模型白名单；留空 = 只用采集模型（状态绑模型，换模型会被上游拒），填 * = 所有模型。 */
  inject_models: string[]
  inject_header: string
  proxy_id: number
  group_ids: number[]
  account_ids: number[]
  model: string
  prompt: string
  ttl_seconds: number
  renew_before_seconds: number
  poll_seconds: number
  probe_timeout_seconds: number
  max_accounts_per_tick: number
  target_state_length?: number
  max_attempts_per_cycle?: number
  concurrent_accounts?: number
  retry_interval_ms?: number
  /** 会撤销当前状态的上游状态码；留空 = 不按状态码撤销（写法里的 312 实测是长度，不是状态码）。 */
  revocation_statuses: number[]
  /** 允许下发状态的上游状态码；留空 = 任意成功响应（实测 200 即下发）。 */
  issuance_statuses: number[]
}

/** 配置 + 运行态统计 + 入池账号列表。 */
export interface CodexTurnStateOverview extends CodexTurnStateConfig {
  transfers?: CodexTurnStateTransfer[]
  proxy_name: string
  proxy_configured: boolean
  enrolled_accounts: number
  collecting_accounts?: number
  queued_accounts?: number
  active_states: number
  expiring_soon: number
  revoked_states: number
  failed_states: number
  probes_today: number
  harvests_today: number
  last_probe_at: string | null
  last_probe_status: string
  last_error: string
  next_tick_at: string | null
  accounts: CodexTurnStateAccountRow[]
}

export interface CodexTurnStateTransfer {
  id: number
  account_id: number
  from_group_id: number
  to_group_id: number
  ticket_record_id: number
  reason: string
  created_at: string
}

/** 一次采集/探测/作废的历史记录。 */
export interface CodexTurnStateHistoryRow {
  id: number
  account_id: number
  account_name: string
  status: string
  source: string
  http_status: number
  model: string
  latency_ms: number
  state_fingerprint: string
  state_length: number
  issued_at: string | null
  expires_at: string | null
  error: string
  created_at: string
}

export interface CodexTurnStateHistoryPage {
  total: number
  page: number
  page_size: number
  items: CodexTurnStateHistoryRow[]
}

export async function getCodexTurnState(): Promise<CodexTurnStateOverview> {
  const { data } = await apiClient.get<CodexTurnStateOverview>('/admin/codex-turn-state')
  return data
}

/**
 * 保存配置。后端可能直接返回配置，也可能包一层 `{ config: ... }`；
 * 两种形态都要兼容，避免调用方拿到空对象。
 */
export async function saveCodexTurnStateConfig(cfg: CodexTurnStateConfig): Promise<CodexTurnStateConfig> {
  const { data } = await apiClient.put<CodexTurnStateConfig | { config: CodexTurnStateConfig }>(
    '/admin/codex-turn-state/config',
    cfg
  )
  if (data && typeof data === 'object' && 'config' in data && data.config) return data.config
  return data as CodexTurnStateConfig
}

/** accountId = 0 表示按配置里的分组/账号整批排队采集。 */
export async function collectCodexTurnState(accountId = 0): Promise<{ queued: number }> {
  const { data } = await apiClient.post<{ queued: number }>('/admin/codex-turn-state/collect', {
    account_id: accountId
  })
  return data
}

export async function invalidateCodexTurnState(accountId: number): Promise<void> {
  await apiClient.post(`/admin/codex-turn-state/accounts/${accountId}/invalidate`)
}

export async function getCodexTurnStateHistory(
  page = 1,
  pageSize = 20,
  accountId = 0
): Promise<CodexTurnStateHistoryPage> {
  const { data } = await apiClient.get<CodexTurnStateHistoryPage>('/admin/codex-turn-state/history', {
    params: { page, page_size: pageSize, account_id: accountId }
  })
  return data
}

export async function clearCodexTurnStateHistory(): Promise<{ deleted: number; retained: number }> {
  const { data } = await apiClient.delete<{ deleted: number; retained: number }>('/admin/codex-turn-state/history', {
    data: { confirm: true }
  })
  return data
}

export default {
  getCodexTurnState,
  saveCodexTurnStateConfig,
  collectCodexTurnState,
  invalidateCodexTurnState,
  getCodexTurnStateHistory,
  clearCodexTurnStateHistory
}
