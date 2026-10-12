<template>
  <section class="py-6" data-test="session-replay">
    <div class="flex flex-wrap items-start justify-between gap-3">
      <div class="min-w-0">
        <button type="button" class="text-sm text-primary-600 hover:underline dark:text-primary-400" data-test="replay-back" @click="$emit('back')">
          ← {{ t('admin.recording.replay.back') }}
        </button>
        <p v-if="detail?.parent" class="mt-2 text-xs text-teal-700 dark:text-teal-300" data-test="parent-session">
          {{ t('admin.recording.replay.subagentOf') }}
          <button type="button" class="font-medium underline" @click="$emit('open-session', detail.parent.session_id)">
            {{ detail.parent.label || detail.parent.title || detail.parent.session_id }}
          </button>
          <span v-if="detail.session.parent_call_id" class="ml-1 font-mono text-teal-600/70">({{ detail.session.parent_call_id }})</span>
        </p>
        <h2 class="mt-2 flex min-w-0 text-lg font-semibold text-gray-950 dark:text-white">
          <SessionLabelEditor v-if="detail" :session-id="detail.session.session_id" :label="detail.session.label" :title="detail.session.title" @saved="detail.session.label = $event" />
        </h2>
        <p v-if="detail?.session.label && detail.session.title" class="mt-0.5 truncate text-xs text-gray-500 dark:text-dark-400">{{ detail.session.title }}</p>
        <p v-if="detail" class="mt-1 flex flex-wrap gap-x-4 gap-y-1 text-xs text-gray-500 dark:text-dark-400">
          <span>{{ detail.session.detected_client || '—' }}</span>
          <span class="font-mono">{{ detail.session.last_model }}</span>
          <span class="tabular-nums">{{ formatDateTime(detail.session.first_at) }} – {{ formatDateTime(detail.session.last_at) }}</span>
          <span class="tabular-nums">{{ t('admin.recording.replay.summary', { exchanges: detail.session.exchanges, input: formatCount(detail.session.input_tokens), output: formatCount(detail.session.output_tokens) }) }}</span>
          <span class="font-mono" :title="detail.session.hint_source">{{ detail.session.hint || detail.session.session_id }}</span>
        </p>
      </div>
      <div class="flex flex-wrap items-center gap-3">
        <div class="tabs inline-flex" role="tablist" :aria-label="t('admin.recording.replay.viewMode')">
          <button type="button" role="tab" class="tab" :class="{ 'tab-active': mode === 'trunk' }" :aria-selected="mode === 'trunk'" data-test="mode-trunk" @click="mode = 'trunk'">
            {{ t('admin.recording.replay.trunk', { n: trunk.length }) }}
          </button>
          <button type="button" role="tab" class="tab" :class="{ 'tab-active': mode === 'all' }" :aria-selected="mode === 'all'" data-test="mode-all" @click="mode = 'all'">
            {{ t('admin.recording.replay.all', { n: detail?.exchanges.length ?? 0 }) }}
          </button>
        </div>
        <label class="flex items-center gap-1.5 text-xs text-gray-600 dark:text-dark-300">
          <input v-model="openThinking" type="checkbox" class="rounded" />
          {{ t('admin.recording.replay.expandThinking') }}
        </label>
        <button type="button" class="btn btn-secondary btn-sm" :disabled="downloading || !detail" data-test="download" @click="download">
          {{ downloading ? t('admin.recording.replay.downloading') : t('admin.recording.replay.download') }}
        </button>
      </div>
    </div>

    <div v-if="error" role="alert" class="mt-5 rounded-lg bg-red-50 px-4 py-3 text-sm text-red-700 dark:bg-red-950/30 dark:text-red-300">{{ error }}</div>
    <div v-else-if="!detail" class="mt-5 space-y-3" aria-busy="true">
      <div v-for="i in 3" :key="i" class="h-24 animate-pulse rounded-xl bg-gray-100 dark:bg-dark-800" />
    </div>

    <template v-else>
      <p v-if="mode === 'trunk' && hidden > 0" class="mt-4 text-xs text-gray-500 dark:text-dark-400" data-test="trunk-hint">
        {{ t('admin.recording.replay.trunkHint', { hidden }) }}
      </p>
      <div class="mt-4 space-y-4">
        <ExchangeCard
          v-for="row in visible"
          :key="row.exchange_id"
          :row="row"
          :number="numbers.get(row.exchange_id) ?? 0"
          :parent-number="row.parent_id ? numbers.get(row.parent_id) : undefined"
          :open-thinking="openThinking"
          :from="displayFrom(row, byId, visibleIds)"
          :subagents="subagentsOf.get(row.exchange_id)"
          @raw="rawId = $event"
          @open-session="$emit('open-session', $event)"
        />
      </div>
    </template>

    <RawViewer :exchange-id="rawId" @close="rawId = null" />
  </section>
</template>

<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { useAppStore } from '@/stores/app'
import { formatDateTime } from '@/utils/format'
import { extractApiErrorMessage } from '@/utils/apiError'
import recordingAPI from '../api'
import type { SessionDetail, SubagentLink } from '../types'
import { displayFrom, formatCount, numberRows, trunkOf } from '../viewModel'
import ExchangeCard from './ExchangeCard.vue'
import RawViewer from './RawViewer.vue'
import SessionLabelEditor from './SessionLabelEditor.vue'

const props = defineProps<{ sessionId: string }>()
defineEmits<{ (e: 'back'): void; (e: 'open-session', id: string): void }>()
const { t } = useI18n()
const appStore = useAppStore()

const detail = ref<SessionDetail | null>(null)
const error = ref('')
const mode = ref<'trunk' | 'all'>('trunk')
const openThinking = ref(false)
const rawId = ref<string | null>(null)
const downloading = ref(false)

const numbers = computed(() => numberRows(detail.value?.exchanges ?? []))
const trunk = computed(() => trunkOf(detail.value?.exchanges ?? []))
const visible = computed(() => (mode.value === 'trunk' ? trunk.value : detail.value?.exchanges ?? []))
const byId = computed(() => new Map((detail.value?.exchanges ?? []).map((r) => [r.exchange_id, r])))
const visibleIds = computed(() => new Set(visible.value.map((r) => r.exchange_id)))
const subagentsOf = computed(() => {
  const m = new Map<string, SubagentLink[]>()
  for (const s of detail.value?.subagents ?? []) m.set(s.parent_exchange_id, [...(m.get(s.parent_exchange_id) ?? []), s])
  return m
})
const hidden = computed(() => (detail.value?.exchanges.length ?? 0) - trunk.value.length)

async function load(id: string) {
  detail.value = null
  error.value = ''
  try {
    detail.value = await recordingAPI.getSession(id)
  } catch (err) {
    error.value = extractApiErrorMessage(err, t('admin.recording.errors.loadSession'))
  }
}

async function download() {
  if (!detail.value) return
  downloading.value = true
  try {
    const blob = await recordingAPI.downloadSession(detail.value.session.session_id)
    const url = window.URL.createObjectURL(blob)
    const link = document.createElement('a')
    link.href = url
    link.download = `session-${detail.value.session.session_id}.tar.zst`
    document.body.appendChild(link)
    link.click()
    document.body.removeChild(link)
    window.URL.revokeObjectURL(url)
  } catch (err) {
    appStore.showError(extractApiErrorMessage(err, t('admin.recording.errors.download')))
  } finally {
    downloading.value = false
  }
}

watch(() => props.sessionId, (id) => void load(id), { immediate: true })
</script>
