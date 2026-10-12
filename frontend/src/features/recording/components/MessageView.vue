<template>
  <div class="flex gap-3" :data-test="`message-${message.role}`">
    <div class="w-20 shrink-0 pt-0.5 text-right">
      <span class="inline-block rounded-md px-1.5 py-0.5 text-[11px] font-semibold uppercase tracking-wide" :class="roleClass">{{ roleLabel }}</span>
      <span v-if="message.item_type && message.item_type !== 'message'" class="mt-1 block font-mono text-[10px] text-gray-400">{{ message.item_type }}</span>
    </div>
    <div class="min-w-0 flex-1 space-y-2">
      <BlockView v-for="(b, i) in message.blocks" :key="i" :block="b" :open-thinking="openThinking" :open-result="openResults" />
      <p v-if="!message.blocks.length" class="text-xs italic text-gray-400">{{ t('admin.recording.replay.emptyMessage') }}</p>
    </div>
  </div>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import type { Message } from '../types'
import BlockView from './BlockView.vue'

const props = defineProps<{ message: Message; openThinking?: boolean; openResults?: boolean }>()
const { t, te } = useI18n()

const roleLabel = computed(() => {
  const key = `admin.recording.replay.roles.${props.message.role}`
  return te(key) ? t(key) : props.message.role
})

const roleClass = computed(() => {
  switch (props.message.role) {
    case 'user':
      return 'bg-primary-50 text-primary-700 dark:bg-primary-950/40 dark:text-primary-300'
    case 'assistant':
      return 'bg-emerald-50 text-emerald-700 dark:bg-emerald-950/40 dark:text-emerald-300'
    case 'tool':
      return 'bg-gray-100 text-gray-600 dark:bg-dark-700 dark:text-dark-300'
    default:
      return 'bg-amber-50 text-amber-700 dark:bg-amber-950/40 dark:text-amber-300'
  }
})
</script>
