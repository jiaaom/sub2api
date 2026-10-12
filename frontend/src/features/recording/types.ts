// 与 backend/internal/trajectory/admin.go 的 JSON 结构一一对应。

export type ScopeType = 'global' | 'group' | 'api_key'

export interface RecordingStatus {
  available: boolean
  root: string
  format_version: number
  global_enabled: boolean
  degraded: boolean
  degraded_reason?: string
  exchanges_started: number
  exchanges_finalized: number
  exchanges_recovered: number
  attempts: number
  orphan_calls: number
  background_calls: number
  late_attempts: number
  write_errors: number
  disk_bytes: number
  spool_pending: number
  days: number
  exchanges: number
  last_exchange_at?: string
  scanned_at: string
}

export interface Scope {
  type: ScopeType
  id: number
  enabled: boolean
  updated_at?: string
}

export interface GroupScope {
  id: number
  name: string
  platform: string
  status: string
  enabled: boolean
}

export interface APIKeyScope {
  id: number
  name: string
  status: string
  user_id: number
  user_email: string
  group_id: number | null
  group_name?: string
  /** null = 继承分组 */
  override: boolean | null
  effective: boolean
}

export interface ScopesView {
  global: boolean
  groups: GroupScope[]
  api_keys: APIKeyScope[]
  scopes: Scope[]
  stale?: Scope[]
}

export interface ScopeUpdate {
  type: ScopeType
  id: number
  /** null 删除该配置（api_key 恢复继承） */
  enabled: boolean | null
}

export interface Usage {
  input?: number
  output?: number
  cache_read?: number
  cache_write?: number
  reasoning?: number
}

export interface ExchangeEntry {
  exchange_id: string
  path: string
  status: 'complete' | 'incomplete' | 'error' | string
  started_at: string
  finished_at?: string
  user_id: number
  api_key_id: number
  group_id: number
  client_path: string
  client_protocol: string
  upstream_protocol?: string
  detected_client: string
  session_hint?: string
  model?: string
  status_code?: number
  attempts: number
  usage?: Usage
  response_id?: string
}

export interface ExchangeList {
  items: ExchangeEntry[]
  has_more: boolean
}

export type KeyOverride = 'inherit' | 'on' | 'off'
