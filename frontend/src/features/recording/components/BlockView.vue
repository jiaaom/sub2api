<template>
  <div v-if="block.type === 'text'" class="whitespace-pre-wrap break-words text-sm leading-6 text-gray-800 dark:text-dark-100" data-test="block-text">{{ block.text }}</div>

  <details v-else-if="block.type === 'thinking'" class="group rounded-lg border border-dashed border-violet-200 bg-violet-50/50 px-3 py-2 dark:border-violet-900/60 dark:bg-violet-950/20" :open="openThinking" data-test="block-thinking">
    <summary class="cursor-pointer select-none text-xs font-medium text-violet-700 dark:text-violet-300">
      {{ t('admin.recording.replay.block.thinking') }}
      <span class="ml-2 font-normal text-violet-500/80 group-open:hidden">{{ blockPreview(block) }}</span>
    </summary>
    <div class="mt-2 whitespace-pre-wrap break-words text-sm leading-6 text-violet-900/90 dark:text-violet-100/80">{{ block.text }}</div>
  </details>

  <div v-else-if="block.type === 'redacted_thinking' || block.type === 'reasoning_encrypted'" class="rounded-lg border border-dashed border-gray-200 px-3 py-2 text-xs text-gray-500 dark:border-dark-600 dark:text-dark-400" data-test="block-encrypted">
    {{ t('admin.recording.replay.block.encrypted', { bytes: (block.data ?? '').length }) }}
    <span v-if="block.summary?.length" class="mt-1 block whitespace-pre-wrap text-gray-600 dark:text-dark-300">{{ block.summary.join('\n\n') }}</span>
  </div>

  <div v-else-if="block.type === 'tool_call'" class="rounded-lg border border-sky-200 bg-sky-50/60 dark:border-sky-900/60 dark:bg-sky-950/20" data-test="block-tool-call">
    <div class="flex flex-wrap items-center gap-2 px-3 py-1.5 text-xs">
      <span class="font-semibold text-sky-800 dark:text-sky-200">{{ t('admin.recording.replay.block.toolCall') }}</span>
      <span class="font-mono font-semibold text-sky-900 dark:text-sky-100">{{ block.name }}</span>
      <span v-if="block.id" class="font-mono text-sky-600/70 dark:text-sky-400/70">{{ block.id }}</span>
    </div>
    <pre class="max-h-96 overflow-auto border-t border-sky-200/70 px-3 py-2 font-mono text-xs leading-5 text-sky-950 dark:border-sky-900/60 dark:text-sky-100">{{ args }}</pre>
  </div>

  <details v-else-if="block.type === 'tool_result'" class="group rounded-lg border bg-gray-50/80 dark:bg-dark-900/40" :class="block.is_error ? 'border-red-200 dark:border-red-900/60' : 'border-gray-200 dark:border-dark-700'" :open="openResult" data-test="block-tool-result">
    <summary class="cursor-pointer select-none px-3 py-1.5 text-xs">
      <span class="font-semibold" :class="block.is_error ? 'text-red-700 dark:text-red-300' : 'text-gray-700 dark:text-dark-200'">
        {{ block.is_error ? t('admin.recording.replay.block.toolError') : t('admin.recording.replay.block.toolResult') }}
      </span>
      <span v-if="block.call_id" class="ml-2 font-mono text-gray-400">{{ block.call_id }}</span>
      <span class="ml-2 text-gray-500 group-open:hidden dark:text-dark-400">{{ blockPreview(block) }}</span>
    </summary>
    <div class="space-y-2 border-t border-gray-200 px-3 py-2 dark:border-dark-700">
      <BlockView v-for="(child, i) in block.content ?? []" :key="i" :block="child" :open-thinking="openThinking" />
    </div>
  </details>

  <div v-else-if="block.type === 'image'" class="inline-flex items-center gap-2 rounded-lg border border-gray-200 px-3 py-1.5 text-xs text-gray-600 dark:border-dark-700 dark:text-dark-300" data-test="block-image">
    <span class="font-semibold">{{ t('admin.recording.replay.block.image') }}</span>
    <span>{{ block.media_type }}</span>
    <span v-if="block.bytes">{{ formatBytes(block.bytes, 1) }}</span>
    <span class="max-w-xs truncate font-mono text-gray-400" :title="block.object">{{ block.object }}</span>
  </div>

  <details v-else class="rounded-lg border border-gray-200 px-3 py-1.5 text-xs dark:border-dark-700" data-test="block-unknown">
    <summary class="cursor-pointer select-none text-gray-600 dark:text-dark-300">{{ block.kind || block.type }}</summary>
    <pre class="mt-2 max-h-96 overflow-auto font-mono leading-5 text-gray-700 dark:text-dark-200">{{ prettyJSON(block.raw) }}</pre>
  </details>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import { formatBytes } from '@/utils/format'
import type { Block } from '../types'
import { blockPreview, prettyJSON } from '../viewModel'

defineOptions({ name: 'BlockView' })

const props = withDefaults(defineProps<{ block: Block; openThinking?: boolean; openResult?: boolean }>(), {
  openThinking: false,
  openResult: false,
})
const { t } = useI18n()

const args = computed(() => (props.block.arguments_raw !== undefined && props.block.arguments === undefined ? props.block.arguments_raw : prettyJSON(props.block.arguments ?? {})))
</script>
