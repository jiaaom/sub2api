<template>
  <section aria-labelledby="recording-sessions-title" class="py-6">
    <div class="flex flex-wrap items-start justify-between gap-3">
      <div>
        <h2 id="recording-sessions-title" class="text-base font-semibold text-gray-950 dark:text-white">{{ t('admin.recording.sessions.title') }}</h2>
        <p class="mt-1 max-w-3xl text-sm text-gray-500 dark:text-dark-300">{{ t('admin.recording.sessions.description') }}</p>
      </div>
    </div>

    <form class="mt-5 grid gap-3 sm:grid-cols-2 lg:grid-cols-4" @submit.prevent="search">
      <input v-model="filters.q" type="search" class="input" :placeholder="t('admin.recording.sessions.searchPlaceholder')" :aria-label="t('admin.recording.sessions.searchPlaceholder')" data-test="sessions-q" />
      <input v-model="filters.client" type="text" class="input" :placeholder="t('admin.recording.sessions.clientPlaceholder')" :aria-label="t('admin.recording.sessions.clientPlaceholder')" />
      <input v-model="filters.model" type="text" class="input" :placeholder="t('admin.recording.sessions.modelPlaceholder')" :aria-label="t('admin.recording.sessions.modelPlaceholder')" />
      <div class="flex gap-2">
        <button type="submit" class="btn btn-primary" :disabled="loading" data-test="sessions-search">{{ t('admin.recording.actions.search') }}</button>
        <button type="button" class="btn btn-secondary" :disabled="loading" @click="reset">{{ t('common.reset') }}</button>
      </div>
    </form>

    <div v-if="error" role="alert" class="mt-5 rounded-lg bg-red-50 px-4 py-3 text-sm text-red-700 dark:bg-red-950/30 dark:text-red-300">{{ error }}</div>

    <div class="mt-4 overflow-x-auto rounded-xl border border-gray-100 dark:border-dark-700/60">
      <table class="min-w-full divide-y divide-gray-100 text-sm dark:divide-dark-700/60" :aria-label="t('admin.recording.sessions.title')">
        <thead class="bg-gray-50 text-left text-xs font-medium text-gray-500 dark:bg-dark-900/40 dark:text-dark-400">
          <tr>
            <th class="px-3 py-2">{{ t('admin.recording.sessions.columns.title') }}</th>
            <th class="px-3 py-2">{{ t('admin.recording.sessions.columns.client') }}</th>
            <th class="px-3 py-2">{{ t('admin.recording.sessions.columns.model') }}</th>
            <th class="px-3 py-2 text-right">{{ t('admin.recording.sessions.columns.exchanges') }}</th>
            <th class="px-3 py-2 text-right">{{ t('admin.recording.sessions.columns.messages') }}</th>
            <th class="px-3 py-2 text-right">{{ t('admin.recording.sessions.columns.tokens') }}</th>
            <th class="px-3 py-2">{{ t('admin.recording.sessions.columns.last') }}</th>
          </tr>
        </thead>
        <tbody class="divide-y divide-gray-100 dark:divide-dark-700/60">
          <tr v-if="loading && !page.items.length">
            <td colspan="7" class="px-3 py-8"><div class="h-6 animate-pulse rounded bg-gray-100 dark:bg-dark-800" /></td>
          </tr>
          <tr v-else-if="!page.items.length">
            <td colspan="7" class="px-3 py-8 text-center text-gray-500 dark:text-dark-400" data-test="sessions-empty">{{ t('admin.recording.sessions.empty') }}</td>
          </tr>
          <tr
            v-for="s in page.items"
            :key="s.session_id"
            class="cursor-pointer text-gray-700 hover:bg-gray-50 dark:text-dark-200 dark:hover:bg-dark-800"
            tabindex="0"
            data-test="session-row"
            @click="$emit('open', s.session_id)"
            @keydown.enter="$emit('open', s.session_id)"
          >
            <td class="max-w-md px-3 py-2">
              <SessionLabelEditor class="font-medium text-gray-900 dark:text-white" :session-id="s.session_id" :label="s.label" :title="s.title" @saved="s.label = $event" />
              <span v-if="s.label && s.title" class="block truncate text-xs text-gray-500 dark:text-dark-400">{{ s.title }}</span>
              <span class="block truncate font-mono text-[11px] text-gray-400" :title="s.hint">{{ s.hint || s.session_id }}</span>
            </td>
            <td class="whitespace-nowrap px-3 py-2">{{ s.detected_client || '—' }}</td>
            <td class="whitespace-nowrap px-3 py-2 font-mono text-xs">{{ s.last_model || '—' }}</td>
            <td class="whitespace-nowrap px-3 py-2 text-right tabular-nums">{{ s.exchanges }}</td>
            <td class="whitespace-nowrap px-3 py-2 text-right tabular-nums">{{ s.max_messages }}</td>
            <td class="whitespace-nowrap px-3 py-2 text-right tabular-nums" :title="t('admin.recording.sessions.tokensTitle', { cache: formatCount(s.cache_read_tokens) })">
              {{ formatCount(s.input_tokens) }} / {{ formatCount(s.output_tokens) }}
            </td>
            <td class="whitespace-nowrap px-3 py-2 tabular-nums">{{ formatDateTime(s.last_at) }}</td>
          </tr>
        </tbody>
      </table>
    </div>

    <div v-if="page.pages > 1" class="mt-3 flex items-center justify-end gap-3 text-sm text-gray-600 dark:text-dark-300">
      <button type="button" class="btn btn-secondary btn-sm" :disabled="loading || page.page <= 1" @click="load(page.page - 1)">{{ t('admin.recording.actions.prev') }}</button>
      <span class="tabular-nums">{{ page.page }} / {{ page.pages }}</span>
      <button type="button" class="btn btn-secondary btn-sm" :disabled="loading || page.page >= page.pages" @click="load(page.page + 1)">{{ t('admin.recording.actions.next') }}</button>
    </div>
  </section>
</template>

<script setup lang="ts">
import { onMounted, reactive, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { formatDateTime } from '@/utils/format'
import { extractApiErrorMessage } from '@/utils/apiError'
import recordingAPI from '../api'
import type { SessionFilters, SessionPage } from '../types'
import { formatCount } from '../viewModel'
import SessionLabelEditor from './SessionLabelEditor.vue'

defineEmits<{ (e: 'open', id: string): void }>()
const { t } = useI18n()

const PAGE_SIZE = 30
const filters = reactive<SessionFilters>({ q: '', client: '', model: '' })
const page = ref<SessionPage>({ items: [], total: 0, page: 1, page_size: PAGE_SIZE, pages: 0 })
const loading = ref(false)
const error = ref('')

async function load(n = 1) {
  loading.value = true
  error.value = ''
  try {
    page.value = await recordingAPI.listSessions(filters, n, PAGE_SIZE)
  } catch (err) {
    error.value = extractApiErrorMessage(err, t('admin.recording.errors.loadSessions'))
  } finally {
    loading.value = false
  }
}

function search() {
  void load(1)
}

function reset() {
  Object.assign(filters, { q: '', client: '', model: '' })
  void load(1)
}

onMounted(() => load(1))
defineExpose({ load })
</script>
