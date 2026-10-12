import type { APIKeyScope, Block, ExchangeEntry, ExchangeRow, KeyOverride, RecordingStatus, ScopesView, ScopeUpdate } from './types'

export function overrideOf(key: Pick<APIKeyScope, 'override'>): KeyOverride {
  if (key.override === null || key.override === undefined) return 'inherit'
  return key.override ? 'on' : 'off'
}

export function keyOverrideUpdate(id: number, value: KeyOverride): ScopeUpdate {
  return { type: 'api_key', id, enabled: value === 'inherit' ? null : value === 'on' }
}

export type RecordingAlert = 'unavailable' | 'degraded' | 'globalOff' | 'noTargets' | 'orphans' | 'spool'

/** 概览页顶部提示，按严重程度排序。 */
export function recordingAlerts(status: RecordingStatus | null, scopes: ScopesView | null): RecordingAlert[] {
  if (!status) return []
  if (!status.available) return ['unavailable']
  const alerts: RecordingAlert[] = []
  if (status.degraded) alerts.push('degraded')
  if (!status.global_enabled) alerts.push('globalOff')
  else if (scopes && !scopes.groups.some((g) => g.enabled) && !scopes.api_keys.some((k) => k.effective)) alerts.push('noTargets')
  if (status.orphan_calls > 0) alerts.push('orphans')
  if (status.spool_pending > 0) alerts.push('spool')
  return alerts
}

/** 当前有多少分组 / key 在录制（全局关闭时为 0）。 */
export function recordingCounts(scopes: ScopesView | null): { groups: number; keys: number } {
  if (!scopes || !scopes.global) return { groups: 0, keys: 0 }
  return {
    groups: scopes.groups.filter((g) => g.enabled).length,
    keys: scopes.api_keys.filter((k) => k.effective).length,
  }
}

export function totalTokens(entry: ExchangeEntry): number {
  const u = entry.usage
  if (!u) return 0
  return (u.input ?? 0) + (u.output ?? 0)
}

export function shortId(id: string | undefined, n = 8): string {
  if (!id) return ''
  return id.length <= n ? id : `…${id.slice(-n)}`
}

export function durationMs(entry: ExchangeEntry): number | null {
  if (!entry.finished_at) return null
  const ms = Date.parse(entry.finished_at) - Date.parse(entry.started_at)
  return Number.isFinite(ms) ? ms : null
}

// ---- 回放 ----

/**
 * 主干：从最后一个 main 类 exchange 沿 parent 回溯到根，按时间顺序返回。
 * retry 与被重试的请求是同一轮，只保留最后一次（跳过被重试的父节点）。
 */
export function trunkOf(rows: ExchangeRow[]): ExchangeRow[] {
  const byId = new Map(rows.map((r) => [r.exchange_id, r]))
  // 主会话取最后一个 main；子 agent 会话里全是 subagent，取最后一个 subagent。
  const mains = rows.filter((r) => r.kind === 'main')
  const subs = rows.filter((r) => r.kind === 'subagent')
  const last = mains[mains.length - 1] ?? subs[subs.length - 1] ?? rows[rows.length - 1]
  const out: ExchangeRow[] = []
  const seen = new Set<string>()
  let cur: ExchangeRow | undefined = last
  while (cur && !seen.has(cur.exchange_id)) {
    seen.add(cur.exchange_id)
    out.push(cur)
    let next: ExchangeRow | undefined = cur.parent_id ? byId.get(cur.parent_id) : undefined
    let r: ExchangeRow = cur
    while (next && r.edge === 'retry' && !seen.has(next.exchange_id)) {
      seen.add(next.exchange_id)
      r = next
      next = r.parent_id ? byId.get(r.parent_id) : undefined
    }
    cur = next
  }
  return out.reverse()
}

/** 在给定的可见集合中，这一轮该从哪条消息开始显示：被隐藏的 retry 父节点按其自身的起点算。 */
export function displayFrom(row: ExchangeRow, byId: Map<string, ExchangeRow>, visible: Set<string>): number {
  let r = row
  const seen = new Set<string>()
  while (r.edge === 'retry' && r.parent_id && !visible.has(r.parent_id) && !seen.has(r.parent_id)) {
    seen.add(r.parent_id)
    const p = byId.get(r.parent_id)
    if (!p) break
    r = p
  }
  return messagesFrom(r)
}

/** 按时间顺序编号（#1 起）。 */
export function numberRows(rows: ExchangeRow[]): Map<string, number> {
  return new Map(rows.map((r, i) => [r.exchange_id, i + 1]))
}

/** 请求中与父节点共享的消息比例（本地前缀复用）。 */
export function prefixRatio(row: ExchangeRow): number {
  if (!row.req_messages) return 0
  return Math.min(1, row.prefix_messages / row.req_messages)
}

/** 输入 token 总数：Anthropic 的 input_tokens 不含缓存部分，其余协议含。 */
export function totalInput(row: Pick<ExchangeRow, 'upstream_protocol' | 'input_tokens' | 'cache_read_tokens' | 'cache_write_tokens'>): number {
  if (row.upstream_protocol === 'anthropic_messages') return row.input_tokens + row.cache_read_tokens + row.cache_write_tokens
  return row.input_tokens
}

/** 上游 cache 命中比例。 */
export function cacheRatio(row: ExchangeRow): number {
  const total = totalInput(row)
  return total > 0 ? Math.min(1, row.cache_read_tokens / total) : 0
}

/** 这轮请求需要显示的起点：根 / compaction 新根从 0（含 system）开始，其余从共享前缀之后开始。 */
export function messagesFrom(row: ExchangeRow): number {
  if (!row.parent_id || row.edge === 'compaction') return 0
  return row.prefix_messages
}

export function prettyJSON(v: unknown): string {
  if (typeof v === 'string') {
    try {
      return JSON.stringify(JSON.parse(v), null, 2)
    } catch {
      return v
    }
  }
  return JSON.stringify(v, null, 2) ?? ''
}

/** 块的单行预览（折叠时显示）。 */
export function blockPreview(b: Block, n = 120): string {
  let s = ''
  if (b.type === 'tool_result') s = (b.content ?? []).map((c) => c.text ?? '').join(' ')
  else if (b.type === 'tool_call') s = b.arguments_raw ?? JSON.stringify(b.arguments ?? {})
  else s = b.text ?? ''
  s = s.replace(/\s+/g, ' ').trim()
  return s.length > n ? `${s.slice(0, n)}…` : s
}

export function formatCount(n: number | undefined): string {
  return (n ?? 0).toLocaleString()
}
