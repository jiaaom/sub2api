import { apiClient } from '@/api/client'
import type { ExchangeList, RecordingStatus, ScopesView, ScopeUpdate } from './types'

const basePath = '/admin/recording'

export async function getStatus(): Promise<RecordingStatus> {
  const { data } = await apiClient.get<RecordingStatus>(`${basePath}/status`)
  return data
}

export async function getScopes(): Promise<ScopesView> {
  const { data } = await apiClient.get<ScopesView>(`${basePath}/scopes`)
  return data
}

export async function updateScope(update: ScopeUpdate): Promise<ScopesView> {
  const { data } = await apiClient.put<ScopesView>(`${basePath}/scopes`, update)
  return data
}

export async function listExchanges(limit = 50): Promise<ExchangeList> {
  const { data } = await apiClient.get<ExchangeList>(`${basePath}/exchanges`, { params: { limit } })
  return data
}

export default { getStatus, getScopes, updateScope, listExchanges }
