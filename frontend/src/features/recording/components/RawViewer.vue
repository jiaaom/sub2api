<template>
  <BaseDialog :show="!!exchangeId" :title="t('admin.recording.raw.title')" width="full" @close="$emit('close')">
    <div v-if="error" role="alert" class="mb-3 rounded-lg bg-red-50 px-3 py-2 text-sm text-red-700 dark:bg-red-950/30 dark:text-red-300">{{ error }}</div>
    <div class="flex min-h-[60vh] flex-col gap-4 lg:flex-row">
      <nav class="shrink-0 lg:w-72" :aria-label="t('admin.recording.raw.files')">
        <p class="mb-2 text-xs text-gray-500 dark:text-dark-400">{{ t('admin.recording.raw.hint') }}</p>
        <ul class="space-y-1">
          <li v-for="f in files" :key="f.path">
            <button
              type="button"
              class="flex w-full items-center justify-between gap-2 rounded-lg px-2 py-1.5 text-left text-xs"
              :class="f.path === selected ? 'bg-primary-50 text-primary-700 dark:bg-primary-950/40 dark:text-primary-300' : 'text-gray-700 hover:bg-gray-50 dark:text-dark-200 dark:hover:bg-dark-700'"
              :data-test="`raw-file-${f.path}`"
              @click="open(f.path)"
            >
              <span class="min-w-0">
                <span class="block truncate font-mono">{{ f.path }}</span>
                <span class="block text-[10px] text-gray-400">{{ fileLabel(f.path) }}</span>
              </span>
              <span class="shrink-0 tabular-nums text-gray-400">{{ formatBytes(f.bytes, 1) }}</span>
            </button>
          </li>
        </ul>
      </nav>
      <section class="min-w-0 flex-1">
        <div v-if="selected" class="mb-2 flex flex-wrap items-center gap-3 text-xs text-gray-500 dark:text-dark-400">
          <span class="font-mono">{{ selected }}</span>
          <span class="tabular-nums">{{ t('admin.recording.raw.loaded', { loaded: formatBytes(text.length, 1), total: formatBytes(total, 1) }) }}</span>
          <label v-if="canPretty" class="flex items-center gap-1">
            <input v-model="pretty" type="checkbox" class="rounded" />
            {{ t('admin.recording.raw.pretty') }}
          </label>
          <button v-if="loadedBytes < total" type="button" class="btn btn-secondary btn-sm" :disabled="loading" data-test="raw-more" @click="more">
            {{ t('admin.recording.raw.more') }}
          </button>
        </div>
        <pre class="max-h-[70vh] min-h-[50vh] overflow-auto rounded-lg bg-gray-50 p-3 font-mono text-xs leading-5 text-gray-800 dark:bg-dark-900 dark:text-dark-100" data-test="raw-content">{{ shown }}</pre>
      </section>
    </div>
  </BaseDialog>
</template>

<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import BaseDialog from '@/components/common/BaseDialog.vue'
import { formatBytes } from '@/utils/format'
import { extractApiErrorMessage } from '@/utils/apiError'
import recordingAPI from '../api'
import type { RawFile } from '../types'

const CHUNK = 256 * 1024

const props = defineProps<{ exchangeId: string | null }>()
defineEmits<{ (e: 'close'): void }>()
const { t } = useI18n()

const files = ref<RawFile[]>([])
const selected = ref('')
const text = ref('')
const loadedBytes = ref(0)
const total = ref(0)
const loading = ref(false)
const error = ref('')
const pretty = ref(true)

function fileLabel(path: string): string {
  if (path.startsWith('client.req')) return t('admin.recording.raw.labels.a')
  if (path.startsWith('client.resp')) return t('admin.recording.raw.labels.d')
  if (/^attempts\/\d+\/upstream\.req/.test(path)) return t('admin.recording.raw.labels.b')
  if (/^attempts\/\d+\/upstream\.resp/.test(path)) return t('admin.recording.raw.labels.c')
  if (path === 'meta.json') return t('admin.recording.raw.labels.meta')
  if (path.endsWith('timing.ndjson')) return t('admin.recording.raw.labels.timing')
  return ''
}

const canPretty = computed(() => selected.value.endsWith('.json') && loadedBytes.value >= total.value)
const shown = computed(() => {
  if (!(pretty.value && canPretty.value)) return text.value
  try {
    return JSON.stringify(JSON.parse(text.value), null, 2)
  } catch {
    return text.value
  }
})

async function fetchChunk(path: string, offset: number) {
  if (!props.exchangeId) return
  loading.value = true
  error.value = ''
  try {
    const chunk = await recordingAPI.getRaw(props.exchangeId, path, offset, CHUNK)
    if (path !== selected.value) return
    text.value = offset === 0 ? chunk.text : text.value + chunk.text
    total.value = chunk.total
    loadedBytes.value = Math.min(chunk.total, offset + CHUNK)
  } catch (err) {
    error.value = extractApiErrorMessage(err, t('admin.recording.errors.loadRaw'))
  } finally {
    loading.value = false
  }
}

function open(path: string) {
  selected.value = path
  text.value = ''
  loadedBytes.value = 0
  total.value = 0
  void fetchChunk(path, 0)
}

function more() {
  void fetchChunk(selected.value, loadedBytes.value)
}

watch(
  () => props.exchangeId,
  async (id) => {
    files.value = []
    selected.value = ''
    text.value = ''
    if (!id) return
    try {
      // 只要文件列表：messages_from 取一个足够大的值，不返回消息
      const d = await recordingAPI.getExchange(id, Number.MAX_SAFE_INTEGER)
      if (id !== props.exchangeId) return
      files.value = d.files
      const preferred = d.files.find((f) => /^attempts\/\d+\/upstream\.req/.test(f.path)) ?? d.files[0]
      if (preferred) open(preferred.path)
    } catch (err) {
      error.value = extractApiErrorMessage(err, t('admin.recording.errors.loadRaw'))
    }
  },
  { immediate: true },
)
</script>
