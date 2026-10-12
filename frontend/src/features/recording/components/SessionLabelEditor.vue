<template>
  <span class="inline-flex min-w-0 max-w-full items-center gap-1.5" @click.stop @keydown.stop>
    <template v-if="editing">
      <input
        ref="input"
        v-model="draft"
        type="text"
        maxlength="200"
        class="input h-8 min-w-0 flex-1 py-1 text-sm"
        :placeholder="title || t('admin.recording.sessions.untitled')"
        :aria-label="t('admin.recording.label.input')"
        data-test="label-input"
        @keydown.enter.prevent="save"
        @keydown.esc.prevent="cancel"
      />
      <button type="button" class="btn btn-primary btn-sm" :disabled="saving" data-test="label-save" @click="save">{{ t('common.save') }}</button>
      <button type="button" class="btn btn-secondary btn-sm" :disabled="saving" @click="cancel">{{ t('common.cancel') }}</button>
    </template>
    <template v-else>
      <span class="min-w-0 truncate" :class="label ? '' : 'text-gray-500 dark:text-dark-300'" data-test="label-text">{{ label || title || t('admin.recording.sessions.untitled') }}</span>
      <button
        type="button"
        class="shrink-0 rounded p-0.5 text-gray-400 hover:bg-gray-100 hover:text-gray-700 dark:hover:bg-dark-700 dark:hover:text-dark-100"
        :title="t('admin.recording.label.rename')"
        :aria-label="t('admin.recording.label.rename')"
        data-test="label-edit"
        @click="start"
      >
        <svg class="h-3.5 w-3.5" fill="none" viewBox="0 0 24 24" stroke="currentColor" stroke-width="1.8" aria-hidden="true">
          <path stroke-linecap="round" stroke-linejoin="round" d="M16.862 4.487l1.687-1.688a1.875 1.875 0 112.652 2.652L10.582 16.07a4.5 4.5 0 01-1.897 1.13L6 18l.8-2.685a4.5 4.5 0 011.13-1.897l8.932-8.931z" />
        </svg>
      </button>
    </template>
  </span>
</template>

<script setup lang="ts">
import { nextTick, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { useAppStore } from '@/stores/app'
import { extractApiErrorMessage } from '@/utils/apiError'
import recordingAPI from '../api'

// label 为用户自定义名称；为空时显示自动标题（第一条用户消息）。
const props = defineProps<{ sessionId: string; label: string; title: string }>()
const emit = defineEmits<{ (e: 'saved', label: string): void }>()
const { t } = useI18n()
const appStore = useAppStore()

const editing = ref(false)
const saving = ref(false)
const draft = ref('')
const input = ref<HTMLInputElement | null>(null)

async function start() {
  draft.value = props.label
  editing.value = true
  await nextTick()
  input.value?.focus()
  input.value?.select()
}

function cancel() {
  editing.value = false
}

async function save() {
  const label = draft.value.trim()
  if (label === props.label) {
    editing.value = false
    return
  }
  saving.value = true
  try {
    const res = await recordingAPI.setSessionLabel(props.sessionId, label)
    emit('saved', res.label)
    editing.value = false
  } catch (err) {
    appStore.showError(extractApiErrorMessage(err, t('admin.recording.errors.saveLabel')))
  } finally {
    saving.value = false
  }
}
</script>
