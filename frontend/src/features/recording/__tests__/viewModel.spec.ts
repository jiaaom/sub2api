import { describe, expect, it } from 'vitest'
import type { RecordingStatus, ScopesView } from '../types'
import { durationMs, keyOverrideUpdate, overrideOf, recordingAlerts, recordingCounts, shortId } from '../viewModel'

const status = (patch: Partial<RecordingStatus> = {}): RecordingStatus => ({
  available: true, root: '/data/trajectories', format_version: 1, global_enabled: true, degraded: false,
  exchanges_started: 0, exchanges_finalized: 0, exchanges_recovered: 0, attempts: 0, orphan_calls: 0, background_calls: 0,
  late_attempts: 0, write_errors: 0, disk_bytes: 0, spool_pending: 0, days: 0, exchanges: 0, scanned_at: '2026-10-12T00:00:00Z',
  ...patch,
})

const scopes = (patch: Partial<ScopesView> = {}): ScopesView => ({
  global: true,
  groups: [{ id: 2, name: 'OpenAI', platform: 'openai', status: 'active', enabled: false }],
  api_keys: [{ id: 2, name: 'key 1', status: 'active', user_id: 1, user_email: 'a@b.c', group_id: 2, group_name: 'OpenAI', override: null, effective: false }],
  scopes: [],
  ...patch,
})

describe('recording view model', () => {
  it('maps the key override tri-state both ways', () => {
    expect(overrideOf({ override: null })).toBe('inherit')
    expect(overrideOf({ override: true })).toBe('on')
    expect(overrideOf({ override: false })).toBe('off')
    expect(keyOverrideUpdate(7, 'inherit')).toEqual({ type: 'api_key', id: 7, enabled: null })
    expect(keyOverrideUpdate(7, 'on')).toEqual({ type: 'api_key', id: 7, enabled: true })
    expect(keyOverrideUpdate(7, 'off')).toEqual({ type: 'api_key', id: 7, enabled: false })
  })

  it('raises alerts in severity order', () => {
    expect(recordingAlerts(null, null)).toEqual([])
    expect(recordingAlerts(status({ available: false }), null)).toEqual(['unavailable'])
    expect(recordingAlerts(status({ global_enabled: false }), scopes())).toEqual(['globalOff'])
    expect(recordingAlerts(status(), scopes())).toEqual(['noTargets'])
    const on = scopes({ groups: [{ id: 2, name: 'OpenAI', platform: 'openai', status: 'active', enabled: true }] })
    expect(recordingAlerts(status(), on)).toEqual([])
    expect(recordingAlerts(status({ degraded: true, orphan_calls: 2, spool_pending: 1 }), on)).toEqual(['degraded', 'orphans', 'spool'])
  })

  it('counts recording targets only when global is on', () => {
    const s = scopes({
      groups: [{ id: 2, name: 'OpenAI', platform: 'openai', status: 'active', enabled: true }],
      api_keys: [{ ...scopes().api_keys[0], effective: true }],
    })
    expect(recordingCounts(s)).toEqual({ groups: 1, keys: 1 })
    expect(recordingCounts({ ...s, global: false })).toEqual({ groups: 0, keys: 0 })
    expect(recordingCounts(null)).toEqual({ groups: 0, keys: 0 })
  })

  it('formats helpers', () => {
    expect(shortId('01a12e13-45a8-72a2-ab93-4d88990cfe8f')).toBe('…990cfe8f')
    expect(shortId('abc')).toBe('abc')
    expect(shortId(undefined)).toBe('')
    const base = { exchange_id: 'x', path: 'p', status: 'complete', user_id: 1, api_key_id: 1, group_id: 1, client_path: '/v1/responses', client_protocol: 'openai_responses', detected_client: 'oh-my-pi', attempts: 1 }
    expect(durationMs({ ...base, started_at: '2026-10-12T03:07:42.000Z', finished_at: '2026-10-12T03:07:49.500Z' })).toBe(7500)
    expect(durationMs({ ...base, started_at: '2026-10-12T03:07:42.000Z' })).toBeNull()
  })
})
