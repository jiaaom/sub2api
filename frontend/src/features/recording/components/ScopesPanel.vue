<template>
  <section aria-labelledby="recording-scopes-title" class="py-6">
    <h2 id="recording-scopes-title" class="text-base font-semibold text-gray-950 dark:text-white">{{ t('admin.recording.scopes.title') }}</h2>
    <p class="mt-1 max-w-3xl text-sm text-gray-500 dark:text-dark-300">{{ t('admin.recording.scopes.description') }}</p>

    <div v-if="error" role="alert" class="mt-5 rounded-lg bg-red-50 px-4 py-3 text-sm text-red-700 dark:bg-red-950/30 dark:text-red-300">
      {{ error }}
    </div>
    <div v-else-if="loading && !scopes" class="mt-5 space-y-3" aria-busy="true">
      <div v-for="index in 3" :key="index" class="h-14 animate-pulse rounded-xl bg-gray-100 dark:bg-dark-800" />
    </div>

    <template v-else-if="scopes">
      <div class="mt-5 flex flex-wrap items-center justify-between gap-4 rounded-xl border border-gray-100 bg-gray-50/80 px-4 py-4 dark:border-dark-700/60 dark:bg-dark-900/40">
        <div>
          <p class="text-sm font-semibold text-gray-900 dark:text-white">{{ t('admin.recording.scopes.global') }}</p>
          <p class="mt-1 text-xs text-gray-500 dark:text-dark-400">{{ t('admin.recording.scopes.globalHint') }}</p>
        </div>
        <Toggle
          :model-value="scopes.global"
          :aria-label="t('admin.recording.scopes.global')"
          :disabled="isSaving('global', 0)"
          data-test="global-toggle"
          @update:model-value="emitUpdate({ type: 'global', id: 0, enabled: $event })"
        />
      </div>

      <h3 class="mt-8 text-sm font-semibold text-gray-950 dark:text-white">{{ t('admin.recording.scopes.groups') }}</h3>
      <p class="mt-1 text-xs text-gray-500 dark:text-dark-400">{{ t('admin.recording.scopes.groupsHint') }}</p>
      <div class="mt-3 overflow-x-auto rounded-xl border border-gray-100 dark:border-dark-700/60" :class="{ 'opacity-60': !scopes.global }">
        <table class="min-w-full divide-y divide-gray-100 text-sm dark:divide-dark-700/60" :aria-label="t('admin.recording.scopes.groups')">
          <thead class="bg-gray-50 text-left text-xs font-medium text-gray-500 dark:bg-dark-900/40 dark:text-dark-400">
            <tr>
              <th class="px-3 py-2">ID</th>
              <th class="px-3 py-2">{{ t('admin.recording.scopes.columns.name') }}</th>
              <th class="px-3 py-2">{{ t('admin.recording.scopes.columns.platform') }}</th>
              <th class="px-3 py-2 text-right">{{ t('admin.recording.scopes.columns.recording') }}</th>
            </tr>
          </thead>
          <tbody class="divide-y divide-gray-100 dark:divide-dark-700/60">
            <tr v-if="!scopes.groups.length">
              <td colspan="4" class="px-3 py-6 text-center text-gray-500 dark:text-dark-400">{{ t('admin.recording.scopes.noGroups') }}</td>
            </tr>
            <tr v-for="g in scopes.groups" :key="g.id" class="text-gray-700 dark:text-dark-200" data-test="group-row">
              <td class="px-3 py-2 tabular-nums">{{ g.id }}</td>
              <td class="px-3 py-2 font-medium text-gray-900 dark:text-white">
                {{ g.name }}
                <span v-if="g.status !== 'active'" class="ml-1 text-xs text-gray-400">({{ g.status }})</span>
              </td>
              <td class="px-3 py-2 text-xs">{{ g.platform }}</td>
              <td class="px-3 py-2">
                <div class="flex justify-end">
                  <Toggle
                    :model-value="g.enabled"
                    :aria-label="t('admin.recording.scopes.toggleGroup', { name: g.name })"
                    :disabled="isSaving('group', g.id)"
                    :data-test="`group-toggle-${g.id}`"
                    @update:model-value="emitUpdate({ type: 'group', id: g.id, enabled: $event })"
                  />
                </div>
              </td>
            </tr>
          </tbody>
        </table>
      </div>

      <h3 class="mt-8 text-sm font-semibold text-gray-950 dark:text-white">{{ t('admin.recording.scopes.apiKeys') }}</h3>
      <p class="mt-1 text-xs text-gray-500 dark:text-dark-400">{{ t('admin.recording.scopes.apiKeysHint') }}</p>
      <div class="mt-3 overflow-x-auto rounded-xl border border-gray-100 dark:border-dark-700/60" :class="{ 'opacity-60': !scopes.global }">
        <table class="min-w-full divide-y divide-gray-100 text-sm dark:divide-dark-700/60" :aria-label="t('admin.recording.scopes.apiKeys')">
          <thead class="bg-gray-50 text-left text-xs font-medium text-gray-500 dark:bg-dark-900/40 dark:text-dark-400">
            <tr>
              <th class="px-3 py-2">ID</th>
              <th class="px-3 py-2">{{ t('admin.recording.scopes.columns.name') }}</th>
              <th class="px-3 py-2">{{ t('admin.recording.scopes.columns.user') }}</th>
              <th class="px-3 py-2">{{ t('admin.recording.scopes.columns.group') }}</th>
              <th class="px-3 py-2">{{ t('admin.recording.scopes.columns.override') }}</th>
              <th class="px-3 py-2">{{ t('admin.recording.scopes.columns.effective') }}</th>
            </tr>
          </thead>
          <tbody class="divide-y divide-gray-100 dark:divide-dark-700/60">
            <tr v-if="!scopes.api_keys.length">
              <td colspan="6" class="px-3 py-6 text-center text-gray-500 dark:text-dark-400">{{ t('admin.recording.scopes.noKeys') }}</td>
            </tr>
            <tr v-for="k in scopes.api_keys" :key="k.id" class="text-gray-700 dark:text-dark-200" data-test="key-row">
              <td class="px-3 py-2 tabular-nums">{{ k.id }}</td>
              <td class="px-3 py-2 font-medium text-gray-900 dark:text-white">
                {{ k.name }}
                <span v-if="k.status !== 'active'" class="ml-1 text-xs text-gray-400">({{ k.status }})</span>
              </td>
              <td class="px-3 py-2 text-xs">{{ k.user_email || `#${k.user_id}` }}</td>
              <td class="px-3 py-2 text-xs">{{ k.group_id ? k.group_name || `#${k.group_id}` : t('admin.recording.scopes.noGroup') }}</td>
              <td class="px-3 py-2">
                <div class="inline-flex overflow-hidden rounded-lg border border-gray-200 dark:border-dark-600" role="radiogroup" :aria-label="t('admin.recording.scopes.overrideFor', { name: k.name })">
                  <button
                    v-for="opt in overrideOptions"
                    :key="opt"
                    type="button"
                    role="radio"
                    class="px-2.5 py-1 text-xs transition-colors"
                    :class="overrideOf(k) === opt ? 'bg-primary-600 text-white' : 'bg-white text-gray-600 hover:bg-gray-50 dark:bg-dark-800 dark:text-dark-300 dark:hover:bg-dark-700'"
                    :aria-checked="overrideOf(k) === opt"
                    :disabled="isSaving('api_key', k.id)"
                    :data-test="`key-${k.id}-${opt}`"
                    @click="overrideOf(k) !== opt && emitUpdate(keyOverrideUpdate(k.id, opt))"
                  >
                    {{ t(`admin.recording.scopes.override.${opt}`) }}
                  </button>
                </div>
              </td>
              <td class="px-3 py-2">
                <span
                  class="rounded-md px-2 py-0.5 text-xs font-medium"
                  :class="k.effective && scopes.global ? 'bg-emerald-50 text-emerald-700 dark:bg-emerald-950/40 dark:text-emerald-300' : 'bg-gray-100 text-gray-500 dark:bg-dark-700 dark:text-dark-300'"
                  :data-test="`key-${k.id}-effective`"
                >
                  {{ k.effective && scopes.global ? t('admin.recording.scopes.recording') : t('admin.recording.scopes.notRecording') }}
                </span>
              </td>
            </tr>
          </tbody>
        </table>
      </div>

      <div v-if="scopes.stale?.length" class="mt-6 rounded-xl border border-gray-200 px-4 py-3 text-xs text-gray-600 dark:border-dark-700 dark:text-dark-300" data-test="stale-scopes">
        <p class="font-medium">{{ t('admin.recording.scopes.stale') }}</p>
        <ul class="mt-2 flex flex-wrap gap-2">
          <li v-for="sc in scopes.stale" :key="`${sc.type}-${sc.id}`" class="flex items-center gap-2 rounded-md bg-gray-50 px-2 py-1 dark:bg-dark-900/60">
            <span class="font-mono">{{ sc.type }}#{{ sc.id }} = {{ sc.enabled ? 'on' : 'off' }}</span>
            <button type="button" class="text-red-600 hover:underline dark:text-red-400" @click="emitUpdate({ type: sc.type, id: sc.id, enabled: null })">
              {{ t('admin.recording.actions.remove') }}
            </button>
          </li>
        </ul>
      </div>
    </template>
  </section>
</template>

<script setup lang="ts">
import { useI18n } from 'vue-i18n'
import Toggle from '@/components/common/Toggle.vue'
import type { KeyOverride, ScopesView, ScopeType, ScopeUpdate } from '../types'
import { keyOverrideUpdate, overrideOf } from '../viewModel'

const props = defineProps<{ scopes: ScopesView | null; loading: boolean; error: string; saving: string[] }>()
const emit = defineEmits<{ (e: 'update', update: ScopeUpdate): void }>()

const { t } = useI18n()
const overrideOptions: KeyOverride[] = ['inherit', 'on', 'off']

function isSaving(type: ScopeType, id: number): boolean {
  return props.saving.includes(`${type}:${id}`)
}

function emitUpdate(update: ScopeUpdate) {
  if (!isSaving(update.type, update.id)) emit('update', update)
}
</script>
