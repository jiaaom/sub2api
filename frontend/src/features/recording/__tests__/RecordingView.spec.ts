import { beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'
import type { RecordingStatus, ScopesView } from '../types'
import RecordingView from '../RecordingView.vue'

const mocks = vi.hoisted(() => ({
  getStatus: vi.fn(), getScopes: vi.fn(), updateScope: vi.fn(), listExchanges: vi.fn(),
  listSessions: vi.fn(), getSession: vi.fn(), getExchange: vi.fn(), getRaw: vi.fn(), downloadSession: vi.fn(), reindex: vi.fn(),
  showSuccess: vi.fn(), showError: vi.fn(),
  route: { query: {} as Record<string, string> },
  push: vi.fn(), replace: vi.fn(), back: vi.fn(),
}))

vi.mock('vue-router', () => ({
  useRoute: () => mocks.route,
  useRouter: () => ({ push: mocks.push, replace: mocks.replace, back: mocks.back }),
}))

vi.mock('../api', () => ({ default: mocks }))
vi.mock('@/stores/app', () => ({ useAppStore: () => ({ showSuccess: mocks.showSuccess, showError: mocks.showError }) }))
vi.mock('vue-i18n', async () => {
  const actual = await vi.importActual<typeof import('vue-i18n')>('vue-i18n')
  return { ...actual, useI18n: () => ({ locale: { value: 'en' }, t: (key: string) => key, te: () => false }) }
})

const status = (): RecordingStatus => ({
  available: true, root: '/data/trajectories', format_version: 1, global_enabled: true, degraded: false,
  exchanges_started: 3, exchanges_finalized: 3, exchanges_recovered: 0, attempts: 3, orphan_calls: 0, background_calls: 0,
  late_attempts: 0, write_errors: 0, disk_bytes: 2048, spool_pending: 0, days: 1, exchanges: 3,
  last_exchange_at: '2026-10-12T03:07:49Z', scanned_at: '2026-10-12T03:08:00Z',
})

const scopes = (patch: Partial<ScopesView> = {}): ScopesView => ({
  global: true,
  groups: [{ id: 2, name: 'OpenAI', platform: 'openai', status: 'active', enabled: true }],
  api_keys: [{ id: 2, name: 'key 1', status: 'active', user_id: 1, user_email: 'a@b.c', group_id: 2, group_name: 'OpenAI', override: null, effective: true }],
  scopes: [],
  ...patch,
})

const AppLayoutStub = { template: '<div><slot /></div>' }

function mountView() {
  return mount(RecordingView, { global: { stubs: { AppLayout: AppLayoutStub } } })
}

describe('RecordingView', () => {
  beforeEach(() => {
    vi.clearAllMocks()
    mocks.route.query = {}
    mocks.listSessions.mockResolvedValue({ items: [], total: 0, page: 1, page_size: 30, pages: 0 })
    mocks.getStatus.mockResolvedValue(status())
    mocks.getScopes.mockResolvedValue(scopes())
    mocks.listExchanges.mockResolvedValue({
      items: [{
        exchange_id: '01a12e14-ac16', path: 'raw/2026-10-12/u1/01a12e14-ac16', status: 'complete', started_at: '2026-10-12T03:07:49Z',
        finished_at: '2026-10-12T03:07:56Z', user_id: 1, api_key_id: 2, group_id: 2, client_path: '/v1/responses',
        client_protocol: 'openai_responses', upstream_protocol: 'openai_responses', detected_client: 'oh-my-pi',
        session_hint: '01a12e13-45a8-72a2-ab93-4d88990cfe8f', model: 'deepseek-v4-flash', status_code: 200, attempts: 1,
        usage: { input: 18980, output: 334, reasoning: 53 },
      }],
      has_more: false,
    })
  })

  it('loads status, scopes and recent exchanges on mount', async () => {
    const wrapper = mountView()
    await flushPromises()
    expect(mocks.getStatus).toHaveBeenCalledOnce()
    expect(mocks.getScopes).toHaveBeenCalledOnce()
    expect(mocks.listExchanges).toHaveBeenCalledWith(50)
    expect(wrapper.find('[data-test="recording-root"]').text()).toBe('/data/trajectories')
    expect(wrapper.findAll('[data-test="exchange-row"]')).toHaveLength(1)
    expect(wrapper.text()).toContain('deepseek-v4-flash')
    expect(wrapper.find('[data-test="alerts"]').exists()).toBe(false)
  })

  it('shows the global-off alert and jumps to the toggles tab', async () => {
    mocks.getStatus.mockResolvedValue({ ...status(), global_enabled: false })
    mocks.getScopes.mockResolvedValue(scopes({ global: false }))
    const wrapper = mountView()
    await flushPromises()
    const alert = wrapper.find('[data-test="alert-globalOff"]')
    expect(alert.exists()).toBe(true)
    await alert.find('button').trigger('click')
    expect(wrapper.find('[data-test="tab-scopes"]').attributes('aria-selected')).toBe('true')
  })

  it('does not query scopes or exchanges when the recorder is unavailable', async () => {
    mocks.getStatus.mockResolvedValue({ ...status(), available: false })
    const wrapper = mountView()
    await flushPromises()
    expect(mocks.getScopes).not.toHaveBeenCalled()
    expect(mocks.listExchanges).not.toHaveBeenCalled()
    expect(wrapper.find('[data-test="alert-unavailable"]').exists()).toBe(true)
  })

  it('saves group toggles and key overrides through the API', async () => {
    mocks.updateScope.mockImplementation(async () => scopes({ groups: [{ id: 2, name: 'OpenAI', platform: 'openai', status: 'active', enabled: false }] }))
    const wrapper = mountView()
    await flushPromises()
    await wrapper.find('[data-test="tab-scopes"]').trigger('click')

    await wrapper.find('[data-test="group-toggle-2"]').trigger('click')
    await flushPromises()
    expect(mocks.updateScope).toHaveBeenLastCalledWith({ type: 'group', id: 2, enabled: false })
    expect(mocks.showSuccess).toHaveBeenCalled()

    await wrapper.find('[data-test="key-2-off"]').trigger('click')
    await flushPromises()
    expect(mocks.updateScope).toHaveBeenLastCalledWith({ type: 'api_key', id: 2, enabled: false })

    // 已选中的选项不重复提交
    mocks.updateScope.mockClear()
    await wrapper.find('[data-test="key-2-inherit"]').trigger('click')
    expect(mocks.updateScope).not.toHaveBeenCalled()

    await wrapper.find('[data-test="global-toggle"]').trigger('click')
    await flushPromises()
    expect(mocks.updateScope).toHaveBeenLastCalledWith({ type: 'global', id: 0, enabled: false })
  })

  it('reports save failures', async () => {
    mocks.updateScope.mockRejectedValue({ message: 'boom' })
    const wrapper = mountView()
    await flushPromises()
    await wrapper.find('[data-test="tab-scopes"]').trigger('click')
    await wrapper.find('[data-test="group-toggle-2"]').trigger('click')
    await flushPromises()
    expect(mocks.showError).toHaveBeenCalledWith('boom')
  })
})

describe('RecordingView sessions and replay', () => {
  beforeEach(() => {
    vi.clearAllMocks()
    mocks.route.query = {}
    mocks.getStatus.mockResolvedValue(status())
    mocks.getScopes.mockResolvedValue(scopes())
    mocks.listExchanges.mockResolvedValue({ items: [], has_more: false })
  })

  it('lists sessions lazily and opens one through the query string', async () => {
    mocks.listSessions.mockResolvedValue({
      items: [{ session_id: 'hf01', user_id: 1, hint_source: 'body:prompt_cache_key', hint: '01a1', detected_client: 'oh-my-pi', first_at: '2026-10-12T03:07:42Z', last_at: '2026-10-12T03:11:59Z', exchanges: 3, input_tokens: 56773, output_tokens: 499, cache_read_tokens: 0, last_model: 'deepseek-v4-flash', max_messages: 10, title: '你好' }],
      total: 1, page: 1, page_size: 30, pages: 1,
    })
    const wrapper = mountView()
    await flushPromises()
    expect(mocks.listSessions).not.toHaveBeenCalled()
    await wrapper.find('[data-test="tab-sessions"]').trigger('click')
    await flushPromises()
    expect(mocks.replace).toHaveBeenCalledWith({ query: { tab: 'sessions' } })
    expect(mocks.listSessions).toHaveBeenCalledWith({ q: '', client: '', model: '' }, 1, 30)
    const row = wrapper.find('[data-test="session-row"]')
    expect(row.text()).toContain('你好')
    await row.trigger('click')
    expect(mocks.push).toHaveBeenCalledWith({ query: { tab: 'sessions', session: 'hf01' } })
  })

  it('renders the replay page when ?session= is present', async () => {
    mocks.route.query = { tab: 'sessions', session: 'hf01' }
    mocks.getSession.mockResolvedValue({
      session: { session_id: 'hf01', user_id: 1, hint_source: '', hint: '', detected_client: 'oh-my-pi', first_at: '2026-10-12T03:07:42Z', last_at: '2026-10-12T03:11:59Z', exchanges: 2, input_tokens: 10, output_tokens: 5, cache_read_tokens: 0, last_model: 'm', max_messages: 5, title: 'hello' },
      exchanges: [],
    })
    const wrapper = mountView()
    await flushPromises()
    expect(mocks.getSession).toHaveBeenCalledWith('hf01')
    expect(wrapper.find('[data-test="session-replay"]').exists()).toBe(true)
    expect(wrapper.find('[data-test="tab-overview"]').exists()).toBe(false)
  })
})
