<template>
  <article ref="root" class="rounded-xl border border-gray-200 bg-white dark:border-dark-700 dark:bg-dark-800/60" :data-test="`exchange-${row.exchange_id}`">
    <div v-if="row.edge === 'compaction'" class="flex items-center gap-3 rounded-t-xl border-b border-amber-200 bg-amber-50 px-4 py-2 text-xs text-amber-900 dark:border-amber-900/60 dark:bg-amber-950/30 dark:text-amber-200" data-test="compaction-divider">
      <span class="font-semibold">{{ t('admin.recording.replay.compactionDivider') }}</span>
      <span>{{ t('admin.recording.replay.compactionFrom', { n: parentNumber ?? '?' }) }}</span>
    </div>

    <header class="flex flex-wrap items-center gap-x-3 gap-y-1.5 px-4 py-3 text-xs text-gray-600 dark:text-dark-300">
      <span class="font-mono text-sm font-semibold text-gray-900 dark:text-white">#{{ number }}</span>
      <span class="tabular-nums">{{ formatDateTime(row.started_at) }}</span>
      <span class="font-mono">{{ row.model || '—' }}</span>
      <span class="rounded-md px-1.5 py-0.5 font-medium" :class="edgeClass" :title="row.match_level ? t('admin.recording.replay.matchLevel', { level: row.match_level }) : ''" data-test="edge-badge">
        {{ edgeText }}
      </span>
      <span v-if="row.kind !== 'main'" class="rounded-md bg-amber-50 px-1.5 py-0.5 font-medium text-amber-700 dark:bg-amber-950/40 dark:text-amber-300">{{ kindText }}</span>
      <span v-if="row.status !== 'complete'" class="rounded-md bg-red-50 px-1.5 py-0.5 font-medium text-red-700 dark:bg-red-950/40 dark:text-red-300">{{ row.status }}</span>
      <span class="ml-auto flex flex-wrap items-center gap-3">
        <span class="tabular-nums" :title="t('admin.recording.replay.tokensTitle')">
          {{ formatCount(totalInput(row)) }} → {{ formatCount(row.output_tokens) }}
          <span v-if="row.reasoning_tokens" class="text-violet-600 dark:text-violet-300">({{ t('admin.recording.replay.reasoningShort', { n: formatCount(row.reasoning_tokens) }) }})</span>
        </span>
        <button type="button" class="text-primary-600 hover:underline dark:text-primary-400" :data-test="`raw-${row.exchange_id}`" @click="$emit('raw', row.exchange_id)">
          {{ t('admin.recording.replay.raw') }}
        </button>
      </span>
    </header>

    <div class="grid gap-1.5 px-4 pb-3 sm:grid-cols-2" data-test="reuse-bars">
      <div :title="t('admin.recording.replay.prefixTitle', { shared: row.prefix_messages, total: row.req_messages })">
        <div class="flex justify-between text-[11px] text-gray-500 dark:text-dark-400">
          <span>{{ t('admin.recording.replay.prefix') }}</span>
          <span class="tabular-nums">{{ row.prefix_messages }} / {{ row.req_messages }}</span>
        </div>
        <div class="mt-0.5 h-1.5 overflow-hidden rounded-full bg-gray-100 dark:bg-dark-700">
          <div class="h-full rounded-full bg-primary-500" :style="{ width: `${prefixRatio(row) * 100}%` }" />
        </div>
      </div>
      <div :title="t('admin.recording.replay.cacheTitle', { read: formatCount(row.cache_read_tokens), total: formatCount(totalInput(row)) })">
        <div class="flex justify-between text-[11px] text-gray-500 dark:text-dark-400">
          <span>{{ t('admin.recording.replay.cache') }}</span>
          <span class="tabular-nums">{{ Math.round(cacheRatio(row) * 100) }}%</span>
        </div>
        <div class="mt-0.5 h-1.5 overflow-hidden rounded-full bg-gray-100 dark:bg-dark-700">
          <div class="h-full rounded-full bg-emerald-500" :style="{ width: `${cacheRatio(row) * 100}%` }" />
        </div>
      </div>
    </div>

    <div class="border-t border-gray-100 px-4 py-4 dark:border-dark-700/60">
      <div v-if="error" role="alert" class="rounded-lg bg-red-50 px-3 py-2 text-sm text-red-700 dark:bg-red-950/30 dark:text-red-300">
        {{ error }}
        <button type="button" class="ml-2 underline" @click="load">{{ t('admin.recording.actions.retry') }}</button>
      </div>
      <div v-else-if="!detail" class="h-16 animate-pulse rounded-lg bg-gray-50 dark:bg-dark-900/40" aria-busy="true" />
      <template v-else>
        <p v-if="detail.parse_error" class="mb-3 text-xs text-red-600 dark:text-red-300">{{ detail.parse_error }}</p>
        <div v-if="detail.request" class="space-y-4">
          <template v-if="detail.messages_from === 0">
            <details v-if="detail.request.system?.length" class="rounded-lg border border-amber-200 bg-amber-50/40 px-3 py-2 dark:border-amber-900/50 dark:bg-amber-950/10" data-test="system-prompt">
              <summary class="cursor-pointer select-none text-xs font-semibold text-amber-800 dark:text-amber-200">
                {{ t('admin.recording.replay.system', { chars: systemChars }) }}
              </summary>
              <div class="mt-2 space-y-2">
                <BlockView v-for="(b, i) in detail.request.system" :key="i" :block="b" />
              </div>
            </details>
            <details v-if="detail.request.tools?.length" class="rounded-lg border border-gray-200 px-3 py-2 dark:border-dark-700" data-test="tools">
              <summary class="cursor-pointer select-none text-xs font-semibold text-gray-700 dark:text-dark-200">
                {{ t('admin.recording.replay.tools', { n: detail.request.tools.length }) }}
                <span class="ml-2 font-mono font-normal text-gray-500">{{ toolNames }}</span>
              </summary>
              <pre class="mt-2 max-h-[32rem] overflow-auto font-mono text-xs leading-5 text-gray-700 dark:text-dark-200">{{ prettyJSON(detail.request.tools) }}</pre>
            </details>
          </template>
          <p v-else class="text-xs text-gray-400 dark:text-dark-500" data-test="prefix-skipped">
            {{ t('admin.recording.replay.prefixSkipped', { n: detail.messages_from, parent: parentNumber ?? '?' }) }}
          </p>
          <MessageView v-for="(m, i) in detail.request.messages" :key="`q${i}`" :message="m" />
        </div>
        <div v-if="detail.response" class="mt-4 border-t border-dashed border-gray-200 pt-4 dark:border-dark-700">
          <p class="mb-3 text-[11px] font-semibold uppercase tracking-wide text-gray-400">
            {{ t('admin.recording.replay.response') }}
            <span v-if="detail.response.stop_reason" class="ml-2 font-mono normal-case tracking-normal">{{ detail.response.stop_reason }}</span>
            <span v-if="!detail.response.complete" class="ml-2 normal-case tracking-normal text-red-500">{{ t('admin.recording.replay.incompleteResponse') }}</span>
          </p>
          <div class="space-y-4">
            <MessageView v-for="(m, i) in detail.response.messages" :key="`r${i}`" :message="m" :open-thinking="openThinking" />
          </div>
          <pre v-if="detail.response.error" class="mt-3 overflow-auto rounded-lg bg-red-50 p-2 text-xs text-red-700 dark:bg-red-950/30 dark:text-red-300">{{ prettyJSON(detail.response.error) }}</pre>
        </div>
      </template>
      <div v-if="subagents?.length" class="mt-4 rounded-lg border border-teal-200 bg-teal-50/50 px-3 py-2 dark:border-teal-900/60 dark:bg-teal-950/20" data-test="subagents">
        <p class="text-xs font-semibold text-teal-800 dark:text-teal-200">{{ t('admin.recording.replay.subagents.title', { n: subagents.length }) }}</p>
        <ul class="mt-2 space-y-1.5">
          <li v-for="s in subagents" :key="s.exchange_id" class="flex flex-wrap items-center gap-2 text-xs text-teal-900 dark:text-teal-100">
            <span class="font-mono text-teal-600/80 dark:text-teal-400/80">{{ s.parent_call_id }}</span>
            <span class="min-w-0 max-w-xl truncate">{{ s.title || s.session_id }}</span>
            <span class="tabular-nums text-teal-700/70 dark:text-teal-300/70">{{ t('admin.recording.replay.subagents.turns', { n: s.exchanges }) }}</span>
            <button v-if="s.session_id !== row.session_id" type="button" class="font-medium text-primary-600 hover:underline dark:text-primary-400" :data-test="`open-subagent-${s.session_id}`" @click="$emit('open-session', s.session_id)">
              {{ t('admin.recording.replay.subagents.open') }} →
            </button>
          </li>
        </ul>
      </div>
    </div>
  </article>
</template>

<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { formatDateTime } from '@/utils/format'
import { extractApiErrorMessage } from '@/utils/apiError'
import recordingAPI from '../api'
import type { ExchangeDetail, ExchangeRow, SubagentLink } from '../types'
import { cacheRatio, formatCount, messagesFrom, prefixRatio, prettyJSON, totalInput } from '../viewModel'
import BlockView from './BlockView.vue'
import MessageView from './MessageView.vue'

const props = defineProps<{ row: ExchangeRow; number: number; parentNumber?: number; openThinking?: boolean; from?: number; subagents?: SubagentLink[] }>()
defineEmits<{ (e: 'raw', id: string): void; (e: 'open-session', id: string): void }>()

const { t } = useI18n()
const root = ref<HTMLElement | null>(null)
const detail = ref<ExchangeDetail | null>(null)
const error = ref('')
let observer: IntersectionObserver | null = null
let loading = false

async function load() {
  if (loading) return
  loading = true
  error.value = ''
  try {
    detail.value = await recordingAPI.getExchange(props.row.exchange_id, props.from ?? messagesFrom(props.row))
  } catch (err) {
    error.value = extractApiErrorMessage(err, t('admin.recording.errors.loadExchange'))
  } finally {
    loading = false
  }
}

// 进入视口时才加载，避免长 session 一次性发出上百个请求。
onMounted(() => {
  if (typeof IntersectionObserver === 'undefined' || !root.value) {
    void load()
    return
  }
  observer = new IntersectionObserver((entries) => {
    if (entries.some((e) => e.isIntersecting)) {
      observer?.disconnect()
      observer = null
      void load()
    }
  }, { rootMargin: '600px 0px' })
  observer.observe(root.value)
})
onBeforeUnmount(() => observer?.disconnect())
// 主干 / 全部切换会改变起点（被隐藏的 retry 父节点）；已加载过的卡片按新起点重载。
watch(() => props.from, () => {
  if (detail.value) void load()
})

const edgeText = computed(() => {
  const p = props.parentNumber
  switch (props.row.edge) {
    case 'root':
      return t('admin.recording.replay.edge.root')
    case 'compaction':
      return t('admin.recording.replay.edge.compaction', { n: p ?? '?' })
    case 'subagent':
      return p ? t('admin.recording.replay.edge.subagent', { n: p }) : t('admin.recording.replay.edge.subagentExternal')
    default:
      return t(`admin.recording.replay.edge.${props.row.edge}`, { n: p ?? '?' })
  }
})

const edgeClass = computed(() => {
  switch (props.row.edge) {
    case 'continue':
    case 'pointer':
      return 'bg-primary-50 text-primary-700 dark:bg-primary-950/40 dark:text-primary-300'
    case 'branch':
      return 'bg-fuchsia-50 text-fuchsia-700 dark:bg-fuchsia-950/40 dark:text-fuchsia-300'
    case 'retry':
      return 'bg-orange-50 text-orange-700 dark:bg-orange-950/40 dark:text-orange-300'
    case 'compaction':
    case 'summarize':
      return 'bg-amber-50 text-amber-700 dark:bg-amber-950/40 dark:text-amber-300'
    case 'subagent':
      return 'bg-teal-50 text-teal-700 dark:bg-teal-950/40 dark:text-teal-300'
    default:
      return 'bg-gray-100 text-gray-600 dark:bg-dark-700 dark:text-dark-300'
  }
})

const kindText = computed(() => {
  const key = `admin.recording.replay.kind.${props.row.kind}`
  return t(key)
})

const systemChars = computed(() => (detail.value?.request?.system ?? []).reduce((n, b) => n + (b.text?.length ?? 0), 0).toLocaleString())
const toolNames = computed(() => (detail.value?.request?.tools ?? []).map((tool) => tool.name || tool.type).join(', '))
</script>
