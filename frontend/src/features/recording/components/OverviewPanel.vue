<template>
  <section aria-labelledby="recording-overview-title" class="py-6">
    <div class="flex flex-wrap items-start justify-between gap-3">
      <div>
        <h2 id="recording-overview-title" class="text-base font-semibold text-gray-950 dark:text-white">
          {{ t('admin.recording.overview.title') }}
        </h2>
        <p v-if="status?.root" class="mt-1 break-all font-mono text-xs text-gray-500 dark:text-dark-400" data-test="recording-root">
          {{ status.root }}
        </p>
      </div>
      <button type="button" class="btn btn-secondary btn-sm" :disabled="loading" data-test="refresh" @click="$emit('refresh')">
        {{ t('admin.recording.actions.refresh') }}
      </button>
    </div>

    <div v-if="error" role="alert" class="mt-5 rounded-lg bg-red-50 px-4 py-3 text-sm text-red-700 dark:bg-red-950/30 dark:text-red-300">
      {{ error }}
    </div>

    <ul v-if="alerts.length" class="mt-5 space-y-2" data-test="alerts">
      <li
        v-for="alert in alerts"
        :key="alert"
        role="status"
        class="flex flex-wrap items-center justify-between gap-3 rounded-xl border px-4 py-3 text-sm"
        :class="alertClass(alert)"
        :data-test="`alert-${alert}`"
      >
        <span>{{ t(`admin.recording.alerts.${alert}`, { reason: status?.degraded_reason ?? '', count: alertCount(alert) }) }}</span>
        <button v-if="alert === 'globalOff' || alert === 'noTargets'" type="button" class="btn btn-secondary btn-sm" @click="$emit('open-scopes')">
          {{ t('admin.recording.actions.openScopes') }}
        </button>
      </li>
    </ul>

    <div v-if="loading && !status" class="mt-5 grid grid-cols-2 gap-3 md:grid-cols-3 xl:grid-cols-6" aria-busy="true">
      <div v-for="index in 6" :key="index" class="h-16 animate-pulse rounded-xl bg-gray-100 dark:bg-dark-800" />
    </div>
    <dl v-else-if="status?.available" class="mt-5 grid grid-cols-2 gap-3 md:grid-cols-3 xl:grid-cols-6" data-test="stats">
      <div v-for="item in statItems" :key="item.label" class="rounded-xl border border-gray-100 bg-gray-50/80 px-3 py-3 dark:border-dark-700/60 dark:bg-dark-900/40">
        <dt class="text-xs text-gray-500 dark:text-dark-400">{{ item.label }}</dt>
        <dd class="mt-1.5 flex items-center gap-2 text-sm font-semibold tabular-nums text-gray-900 dark:text-white">
          <span v-if="item.dot" class="h-2 w-2 shrink-0 rounded-full" :class="item.dot" />
          <span class="min-w-0 truncate" :title="item.title ?? item.value">{{ item.value }}</span>
        </dd>
      </div>
    </dl>

    <div v-if="status?.available" class="mt-3 flex flex-wrap items-center gap-3 text-xs text-gray-500 dark:text-dark-400" data-test="index-status">
      <template v-if="status.index">
        <span>{{ t('admin.recording.index.summary', { indexed: status.index.indexed, queue: status.index.queue, failed: status.index.failed, dropped: status.index.dropped }) }}</span>
        <span v-if="status.index.rebuilding" class="text-amber-600 dark:text-amber-300">{{ t('admin.recording.index.rebuilding') }}</span>
        <button type="button" class="text-primary-600 hover:underline dark:text-primary-400" :disabled="status.index.rebuilding" data-test="reindex" @click="$emit('reindex')">
          {{ t('admin.recording.index.rebuild') }}
        </button>
      </template>
      <span v-else>{{ t('admin.recording.index.unavailable') }}</span>
    </div>

    <div class="mt-8 flex flex-wrap items-end justify-between gap-2">
      <div>
        <h3 class="text-sm font-semibold text-gray-950 dark:text-white">{{ t('admin.recording.exchanges.title') }}</h3>
        <p class="mt-1 text-xs text-gray-500 dark:text-dark-400">{{ t('admin.recording.exchanges.description') }}</p>
      </div>
    </div>
    <div class="mt-3 overflow-x-auto rounded-xl border border-gray-100 dark:border-dark-700/60">
      <table class="min-w-full divide-y divide-gray-100 text-sm dark:divide-dark-700/60" :aria-label="t('admin.recording.exchanges.title')">
        <thead class="bg-gray-50 text-left text-xs font-medium text-gray-500 dark:bg-dark-900/40 dark:text-dark-400">
          <tr>
            <th class="px-3 py-2">{{ t('admin.recording.exchanges.columns.time') }}</th>
            <th class="px-3 py-2">{{ t('admin.recording.exchanges.columns.client') }}</th>
            <th class="px-3 py-2">{{ t('admin.recording.exchanges.columns.model') }}</th>
            <th class="px-3 py-2">{{ t('admin.recording.exchanges.columns.protocol') }}</th>
            <th class="px-3 py-2">{{ t('admin.recording.exchanges.columns.status') }}</th>
            <th class="px-3 py-2 text-right">{{ t('admin.recording.exchanges.columns.tokens') }}</th>
            <th class="px-3 py-2 text-right">{{ t('admin.recording.exchanges.columns.duration') }}</th>
            <th class="px-3 py-2">{{ t('admin.recording.exchanges.columns.session') }}</th>
            <th class="px-3 py-2">{{ t('admin.recording.exchanges.columns.path') }}</th>
          </tr>
        </thead>
        <tbody class="divide-y divide-gray-100 dark:divide-dark-700/60">
          <tr v-if="!exchanges.length">
            <td colspan="9" class="px-3 py-8 text-center text-sm text-gray-500 dark:text-dark-400" data-test="exchanges-empty">
              {{ t('admin.recording.exchanges.empty') }}
            </td>
          </tr>
          <tr v-for="ex in exchanges" :key="ex.exchange_id" class="align-top text-gray-700 dark:text-dark-200" data-test="exchange-row">
            <td class="whitespace-nowrap px-3 py-2 tabular-nums">{{ formatDateTime(ex.started_at) }}</td>
            <td class="whitespace-nowrap px-3 py-2">{{ ex.detected_client || '—' }}</td>
            <td class="whitespace-nowrap px-3 py-2 font-mono text-xs">{{ ex.model || '—' }}</td>
            <td class="whitespace-nowrap px-3 py-2 font-mono text-xs">
              {{ ex.client_protocol }}<template v-if="ex.upstream_protocol && ex.upstream_protocol !== ex.client_protocol"> → {{ ex.upstream_protocol }}</template>
            </td>
            <td class="whitespace-nowrap px-3 py-2">
              <span class="rounded-md px-2 py-0.5 text-xs font-medium" :class="statusClass(ex.status)">{{ statusLabel(ex.status) }}</span>
              <span v-if="ex.attempts > 1" class="ml-1 text-xs text-gray-500 dark:text-dark-400">×{{ ex.attempts }}</span>
            </td>
            <td class="whitespace-nowrap px-3 py-2 text-right tabular-nums" :title="usageTitle(ex)">
              <template v-if="ex.usage">{{ fmtNum(ex.usage.input) }} / {{ fmtNum(ex.usage.output) }}</template>
              <template v-else>—</template>
            </td>
            <td class="whitespace-nowrap px-3 py-2 text-right tabular-nums">{{ fmtDuration(durationMs(ex)) }}</td>
            <td class="whitespace-nowrap px-3 py-2 font-mono text-xs" :title="ex.session_hint">{{ shortId(ex.session_hint) || '—' }}</td>
            <td class="px-3 py-2">
              <button
                type="button"
                class="max-w-[16rem] truncate text-left font-mono text-xs text-primary-600 hover:underline dark:text-primary-400"
                :title="t('admin.recording.exchanges.copyPath')"
                :aria-label="t('admin.recording.exchanges.copyPath')"
                @click="$emit('copy-path', ex.path)"
              >
                {{ ex.path }}
              </button>
            </td>
          </tr>
        </tbody>
      </table>
    </div>
    <p v-if="hasMore" class="mt-2 text-xs text-gray-500 dark:text-dark-400">{{ t('admin.recording.exchanges.hasMore', { count: exchanges.length }) }}</p>
  </section>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import { formatBytes, formatDateTime } from '@/utils/format'
import type { ExchangeEntry, RecordingStatus, ScopesView } from '../types'
import { durationMs, recordingAlerts, recordingCounts, shortId, type RecordingAlert } from '../viewModel'

const props = defineProps<{
  status: RecordingStatus | null
  scopes: ScopesView | null
  exchanges: ExchangeEntry[]
  hasMore: boolean
  loading: boolean
  error: string
}>()
defineEmits<{ (e: 'refresh'): void; (e: 'open-scopes'): void; (e: 'copy-path', path: string): void; (e: 'reindex'): void }>()

const { t } = useI18n()

const alerts = computed(() => recordingAlerts(props.status, props.scopes))

const fmtNum = (n: number | undefined) => (n ?? 0).toLocaleString()

const statItems = computed(() => {
  const s = props.status
  if (!s) return []
  const counts = recordingCounts(props.scopes)
  const state = s.degraded ? 'degraded' : s.global_enabled ? 'on' : 'off'
  const dot = { degraded: 'bg-red-500', on: 'bg-emerald-500', off: 'bg-gray-400' }[state]
  return [
    { label: t('admin.recording.stats.state'), value: t(`admin.recording.state.${state}`), dot },
    { label: t('admin.recording.stats.targets'), value: t('admin.recording.stats.targetsValue', counts) },
    { label: t('admin.recording.stats.exchanges'), value: fmtNum(s.exchanges), title: t('admin.recording.stats.daysTitle', { days: s.days }) },
    { label: t('admin.recording.stats.disk'), value: formatBytes(s.disk_bytes, 1) },
    { label: t('admin.recording.stats.last'), value: s.last_exchange_at ? formatDateTime(s.last_exchange_at) : t('admin.recording.common.never') },
    {
      label: t('admin.recording.stats.orphans'),
      value: fmtNum(s.orphan_calls),
      dot: s.orphan_calls > 0 ? 'bg-amber-500' : 'bg-emerald-500',
      title: t('admin.recording.stats.orphansTitle', { background: s.background_calls, late: s.late_attempts, writeErrors: s.write_errors }),
    },
  ] as { label: string; value: string; dot?: string; title?: string }[]
})

function alertCount(alert: RecordingAlert): number {
  if (alert === 'orphans') return props.status?.orphan_calls ?? 0
  if (alert === 'spool') return props.status?.spool_pending ?? 0
  return 0
}

function alertClass(alert: RecordingAlert): string {
  if (alert === 'unavailable' || alert === 'degraded') return 'border-red-200 bg-red-50 text-red-800 dark:border-red-900 dark:bg-red-950/30 dark:text-red-200'
  return 'border-amber-200 bg-amber-50 text-amber-900 dark:border-amber-900/70 dark:bg-amber-950/30 dark:text-amber-200'
}

function statusClass(status: string): string {
  if (status === 'complete') return 'bg-emerald-50 text-emerald-700 dark:bg-emerald-950/40 dark:text-emerald-300'
  if (status === 'incomplete') return 'bg-amber-50 text-amber-700 dark:bg-amber-950/40 dark:text-amber-300'
  return 'bg-red-50 text-red-700 dark:bg-red-950/40 dark:text-red-300'
}

function statusLabel(status: string): string {
  return ['complete', 'incomplete', 'error'].includes(status) ? t(`admin.recording.exchangeStatus.${status}`) : status
}

function usageTitle(ex: ExchangeEntry): string {
  const u = ex.usage
  if (!u) return ''
  return t('admin.recording.exchanges.usageTitle', {
    input: fmtNum(u.input), output: fmtNum(u.output), cacheRead: fmtNum(u.cache_read), cacheWrite: fmtNum(u.cache_write), reasoning: fmtNum(u.reasoning),
  })
}

function fmtDuration(ms: number | null): string {
  if (ms === null) return '—'
  return ms < 1000 ? `${ms} ms` : `${(ms / 1000).toFixed(1)} s`
}
</script>
