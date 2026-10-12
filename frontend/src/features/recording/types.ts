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
  index?: { indexed: number; dropped: number; failed: number; queue: number; rebuilding: boolean }
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

// ---- Sessions / 回放（backend/internal/trajectory/index.go、admin_sessions.go、ir/ir.go） ----

export interface SessionRow {
  session_id: string
  user_id: number
  hint_source: string
  hint: string
  detected_client: string
  first_at: string
  last_at: string
  exchanges: number
  input_tokens: number
  output_tokens: number
  cache_read_tokens: number
  last_model: string
  max_messages: number
  title: string
  /** 用户自定义名称，空串表示未命名 */
  label: string
}

export interface SessionPage {
  items: SessionRow[]
  total: number
  page: number
  page_size: number
  pages: number
}

export interface SessionFilters {
  q: string
  client: string
  model: string
}

export type EdgeType = 'root' | 'continue' | 'branch' | 'retry' | 'pointer' | 'compaction'

export interface ExchangeRow {
  exchange_id: string
  raw_path: string
  started_at: string
  finished_at?: string
  user_id: number
  api_key_id: number
  group_id: number
  status: string
  status_code: number
  client_path: string
  client_protocol: string
  upstream_protocol: string
  detected_client: string
  model: string
  session_id: string
  session_hint: string
  input_tokens: number
  output_tokens: number
  cache_read_tokens: number
  cache_write_tokens: number
  reasoning_tokens: number
  response_id?: string
  previous_response_id?: string
  root_hash?: string
  req_messages: number
  resp_messages: number
  tools: number
  parent_id: string | null
  edge: EdgeType | string
  match_level?: string
  prefix_messages: number
  kind: string
  title?: string
  index_error?: string
}

export interface SessionDetail {
  session: SessionRow
  exchanges: ExchangeRow[]
}

export type BlockType =
  | 'text'
  | 'thinking'
  | 'redacted_thinking'
  | 'reasoning_encrypted'
  | 'tool_call'
  | 'tool_result'
  | 'image'
  | 'unknown'

export interface Block {
  type: BlockType | string
  text?: string
  signature?: string
  data?: string
  summary?: string[]
  id?: string
  name?: string
  arguments?: unknown
  arguments_raw?: string
  call_id?: string
  is_error?: boolean
  content?: Block[]
  media_type?: string
  object?: string
  bytes?: number
  kind?: string
  raw?: unknown
}

export interface Message {
  role: string
  blocks: Block[]
  item_type?: string
  item_id?: string
}

export interface Tool {
  type?: string
  name?: string
  description?: string
  schema?: unknown
  raw?: unknown
}

export interface IRRequest {
  protocol: string
  model?: string
  system?: Block[]
  tools?: Tool[]
  messages: Message[]
  stream?: boolean
  previous_response_id?: string
  params?: Record<string, unknown>
}

export interface IRResponse {
  protocol: string
  id?: string
  model?: string
  messages: Message[]
  stop_reason?: string
  usage?: Usage
  complete: boolean
  error?: unknown
}

export interface RawFile {
  path: string
  bytes: number
}

export interface ExchangeDetail {
  row?: ExchangeRow
  meta: Record<string, unknown> & { exchange_id: string; notes?: string[] }
  dir: string
  files: RawFile[]
  request?: IRRequest
  response?: IRResponse
  messages_from: number
  total_messages: number
  parse_error?: string
}

export interface RawChunk {
  text: string
  offset: number
  total: number
}
