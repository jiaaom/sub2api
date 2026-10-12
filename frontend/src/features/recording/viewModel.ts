import type { APIKeyScope, ExchangeEntry, KeyOverride, RecordingStatus, ScopesView, ScopeUpdate } from './types'

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
