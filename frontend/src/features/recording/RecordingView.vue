<template>
  <AppLayout>
    <div class="mx-auto max-w-[1600px] pb-8">
      <header class="mb-6">
        <p class="text-xs font-semibold uppercase tracking-[0.16em] text-primary-600 dark:text-primary-400">{{ t('admin.recording.eyebrow') }}</p>
        <h1 class="mt-1 text-2xl font-semibold tracking-tight text-gray-950 dark:text-white">{{ t('admin.recording.title') }}</h1>
        <p class="mt-2 max-w-3xl text-sm text-gray-500 dark:text-dark-300">{{ t('admin.recording.description') }}</p>
      </header>

      <div class="mb-4" role="tablist" :aria-label="t('admin.recording.title')">
        <div class="tabs inline-flex">
          <button
            v-for="tab in pageTabs"
            :key="tab.id"
            type="button"
            role="tab"
            class="tab"
            :class="{ 'tab-active': activeTab === tab.id }"
            :aria-selected="activeTab === tab.id"
            :data-test="`tab-${tab.id}`"
            @click="activeTab = tab.id"
          >
            {{ tab.label }}
          </button>
        </div>
      </div>

      <main class="card px-4 sm:px-6 lg:px-8">
        <OverviewPanel
          v-show="activeTab === 'overview'"
          :status="status"
          :scopes="scopes"
          :exchanges="exchanges.items"
          :has-more="exchanges.has_more"
          :loading="loading.overview"
          :error="errors.overview"
          @refresh="loadOverview"
          @open-scopes="activeTab = 'scopes'"
          @copy-path="copyPath"
        />
        <ScopesPanel
          v-show="activeTab === 'scopes'"
          :scopes="scopes"
          :loading="loading.scopes"
          :error="errors.scopes"
          :saving="saving"
          @update="saveScope"
        />
      </main>
    </div>
  </AppLayout>
</template>

<script setup lang="ts">
import { computed, onMounted, reactive, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import AppLayout from '@/components/layout/AppLayout.vue'
import { useAppStore } from '@/stores/app'
import { extractApiErrorMessage } from '@/utils/apiError'
import OverviewPanel from './components/OverviewPanel.vue'
import ScopesPanel from './components/ScopesPanel.vue'
import recordingAPI from './api'
import type { ExchangeList, RecordingStatus, ScopesView, ScopeUpdate } from './types'

const { t } = useI18n()
const appStore = useAppStore()

type RecordingTab = 'overview' | 'scopes'
const activeTab = ref<RecordingTab>('overview')
const pageTabs = computed(() => [
  { id: 'overview' as const, label: t('admin.recording.tabs.overview') },
  { id: 'scopes' as const, label: t('admin.recording.tabs.scopes') },
])

const status = ref<RecordingStatus | null>(null)
const scopes = ref<ScopesView | null>(null)
const exchanges = ref<ExchangeList>({ items: [], has_more: false })
const saving = ref<string[]>([])
const loading = reactive({ overview: false, scopes: false })
const errors = reactive({ overview: '', scopes: '' })

function errorMessage(err: unknown, fallbackKey: string): string {
  return extractApiErrorMessage(err, t(fallbackKey))
}

async function loadScopes() {
  loading.scopes = true
  errors.scopes = ''
  try {
    scopes.value = await recordingAPI.getScopes()
  } catch (err) {
    errors.scopes = errorMessage(err, 'admin.recording.errors.loadScopes')
  } finally {
    loading.scopes = false
  }
}

async function loadOverview() {
  loading.overview = true
  errors.overview = ''
  try {
    status.value = await recordingAPI.getStatus()
    if (status.value.available) {
      const [list] = await Promise.all([recordingAPI.listExchanges(50), loadScopes()])
      exchanges.value = list
    }
  } catch (err) {
    errors.overview = errorMessage(err, 'admin.recording.errors.loadStatus')
  } finally {
    loading.overview = false
  }
}

async function saveScope(update: ScopeUpdate) {
  const key = `${update.type}:${update.id}`
  saving.value = [...saving.value, key]
  try {
    scopes.value = await recordingAPI.updateScope(update)
    if (status.value) status.value = { ...status.value, global_enabled: scopes.value.global }
    appStore.showSuccess(t('admin.recording.messages.saved'))
  } catch (err) {
    appStore.showError(errorMessage(err, 'admin.recording.errors.saveScope'))
  } finally {
    saving.value = saving.value.filter((k) => k !== key)
  }
}

async function copyPath(path: string) {
  const full = status.value?.root ? `${status.value.root}/${path}` : path
  try {
    await navigator.clipboard.writeText(full)
    appStore.showSuccess(t('admin.recording.messages.copied'))
  } catch {
    appStore.showError(full)
  }
}

onMounted(loadOverview)
</script>
