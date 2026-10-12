import { apiClient } from '@/api/client'
import type {
  ExchangeDetail,
  ExchangeList,
  RawChunk,
  RecordingStatus,
  ScopesView,
  ScopeUpdate,
  SessionDetail,
  SessionFilters,
  SessionPage,
} from './types'

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

export async function listSessions(filters: SessionFilters, page: number, pageSize: number): Promise<SessionPage> {
  const params: Record<string, string | number> = { page, page_size: pageSize }
  if (filters.q.trim()) params.q = filters.q.trim()
  if (filters.client) params.client = filters.client
  if (filters.model.trim()) params.model = filters.model.trim()
  const { data } = await apiClient.get<SessionPage>(`${basePath}/sessions`, { params })
  return data
}

export async function getSession(id: string): Promise<SessionDetail> {
  const { data } = await apiClient.get<SessionDetail>(`${basePath}/sessions/${encodeURIComponent(id)}`)
  return data
}

export async function getExchange(id: string, messagesFrom = 0, includeSystem = false): Promise<ExchangeDetail> {
  const params: Record<string, string | number> = { messages_from: messagesFrom }
  if (includeSystem) params.include_system = 1
  const { data } = await apiClient.get<ExchangeDetail>(`${basePath}/exchanges/${encodeURIComponent(id)}`, { params })
  return data
}

/** 分段读取原始文件；以文本返回（不做 JSON 解析）。 */
export async function getRaw(id: string, file: string, offset = 0, limit = 256 * 1024): Promise<RawChunk> {
  const res = await apiClient.get<string>(`${basePath}/exchanges/${encodeURIComponent(id)}/raw`, {
    params: { file, offset, limit },
    responseType: 'text',
    transformResponse: [(d: string) => d],
  })
  return {
    text: res.data,
    offset: Number(res.headers['x-offset'] ?? offset),
    total: Number(res.headers['x-total-size'] ?? res.data.length),
  }
}

export async function downloadSession(id: string): Promise<Blob> {
  const res = await apiClient.get(`${basePath}/sessions/${encodeURIComponent(id)}/download`, { responseType: 'blob' })
  return res.data as Blob
}

/** 设置会话自定义名称；空串清除。 */
export async function setSessionLabel(id: string, label: string): Promise<{ session_id: string; label: string }> {
  const { data } = await apiClient.put<{ session_id: string; label: string }>(`${basePath}/sessions/${encodeURIComponent(id)}/label`, { label })
  return data
}

export async function reindex(): Promise<{ started: boolean }> {
  const { data } = await apiClient.post<{ started: boolean }>(`${basePath}/reindex`)
  return data
}

export default {
  getStatus,
  getScopes,
  updateScope,
  listExchanges,
  listSessions,
  getSession,
  getExchange,
  getRaw,
  downloadSession,
  setSessionLabel,
  reindex,
}
