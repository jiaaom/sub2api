import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'
import type { ExchangeDetail, ExchangeRow, SessionDetail } from '../types'
import { blockPreview, cacheRatio, displayFrom, messagesFrom, prefixRatio, totalInput, trunkOf } from '../viewModel'
import SessionReplay from '../components/SessionReplay.vue'

const mocks = vi.hoisted(() => ({ getSession: vi.fn(), getExchange: vi.fn(), getRaw: vi.fn(), downloadSession: vi.fn(), showError: vi.fn(), showSuccess: vi.fn() }))
vi.mock('../api', () => ({ default: mocks }))
vi.mock('@/stores/app', () => ({ useAppStore: () => ({ showSuccess: mocks.showSuccess, showError: mocks.showError }) }))
vi.mock('vue-i18n', async () => {
  const actual = await vi.importActual<typeof import('vue-i18n')>('vue-i18n')
  return {
    ...actual,
    useI18n: () => ({ t: (key: string, p?: Record<string, unknown>) => key.replace(/$/, p ? JSON.stringify(p) : ''), te: () => false }),
  }
})

function row(id: string, patch: Partial<ExchangeRow> = {}): ExchangeRow {
  return {
    exchange_id: id, raw_path: `raw/2026-10-12/u1/${id}`, started_at: '2026-10-12T03:07:42Z', user_id: 1, api_key_id: 2, group_id: 2,
    status: 'complete', status_code: 200, client_path: '/v1/responses', client_protocol: 'openai_responses', upstream_protocol: 'openai_responses',
    detected_client: 'oh-my-pi', model: 'm', session_id: 's', session_hint: 'h', input_tokens: 1000, output_tokens: 10, cache_read_tokens: 0,
    cache_write_tokens: 0, reasoning_tokens: 0, req_messages: 1, resp_messages: 1, tools: 1, parent_id: null, edge: 'root', prefix_messages: 0, kind: 'main',
    ...patch,
  }
}

describe('replay view model', () => {
  const rows = [
    row('a'),
    row('title', { session_id: 'aux' }),
    row('b', { parent_id: 'a', edge: 'continue', prefix_messages: 2, req_messages: 3 }),
    row('b-retry', { parent_id: 'b', edge: 'retry', prefix_messages: 3, req_messages: 3 }),
    row('c', { parent_id: 'b', edge: 'continue', prefix_messages: 4, req_messages: 5 }),
    row('cnt', { kind: 'count_tokens' }),
  ]

  it('walks the main path back from the newest main exchange', () => {
    expect(trunkOf(rows).map((r) => r.exchange_id)).toEqual(['a', 'b', 'c'])
    // 最新一轮是重试：保留重试、跳过被重试的那次，起点沿用被重试者的
    const retried = rows.slice(0, 4)
    const trunk = trunkOf(retried)
    expect(trunk.map((r) => r.exchange_id)).toEqual(['a', 'b-retry'])
    const byId = new Map(retried.map((r) => [r.exchange_id, r]))
    expect(displayFrom(retried[3], byId, new Set(trunk.map((r) => r.exchange_id)))).toBe(2)
    expect(displayFrom(retried[3], byId, new Set(retried.map((r) => r.exchange_id)))).toBe(3)
    expect(trunkOf([])).toEqual([])
  })

  it('computes reuse ratios per protocol', () => {
    expect(prefixRatio(rows[2])).toBeCloseTo(2 / 3)
    expect(prefixRatio(row('x', { req_messages: 0 }))).toBe(0)
    const anthropic = row('x', { upstream_protocol: 'anthropic_messages', input_tokens: 10, cache_read_tokens: 3000, cache_write_tokens: 40 })
    expect(totalInput(anthropic)).toBe(3050)
    expect(cacheRatio(anthropic)).toBeCloseTo(3000 / 3050)
    expect(cacheRatio(row('y', { input_tokens: 1000, cache_read_tokens: 800 }))).toBeCloseTo(0.8)
  })

  it('starts roots and compaction roots from the system prompt', () => {
    expect(messagesFrom(rows[0])).toBe(0)
    expect(messagesFrom(rows[2])).toBe(2)
    expect(messagesFrom(row('z', { parent_id: 'a', edge: 'compaction', prefix_messages: 0 }))).toBe(0)
  })

  it('previews blocks on one line', () => {
    expect(blockPreview({ type: 'tool_result', content: [{ type: 'text', text: 'a\n  b' }] })).toBe('a b')
    expect(blockPreview({ type: 'text', text: 'x'.repeat(200) }, 10)).toBe(`${'x'.repeat(10)}…`)
  })
})

describe('SessionReplay', () => {
  const detail = (from: number): ExchangeDetail => ({
    meta: { exchange_id: 'b' }, dir: '/d', files: [], messages_from: from, total_messages: 3,
    request: {
      protocol: 'openai_responses',
      system: from === 0 ? [{ type: 'text', text: 'You are a coding agent.' }] : undefined,
      tools: from === 0 ? [{ type: 'function', name: 'read' }] : undefined,
      messages: [{ role: 'user', blocks: [{ type: 'text', text: 'list folders' }] }],
    },
    response: {
      protocol: 'openai_responses', complete: true, stop_reason: 'completed',
      messages: [
        { role: 'assistant', item_type: 'reasoning', blocks: [{ type: 'thinking', text: 'look at cwd' }] },
        { role: 'assistant', item_type: 'function_call', blocks: [{ type: 'tool_call', id: 'call_1', name: 'read', arguments: { path: '.' } }] },
      ],
    },
  })
  const session: SessionDetail = {
    session: { session_id: 's', user_id: 1, hint_source: '', hint: 'h', detected_client: 'oh-my-pi', first_at: '2026-10-12T03:07:42Z', last_at: '2026-10-12T03:11:59Z', exchanges: 3, input_tokens: 1, output_tokens: 1, cache_read_tokens: 0, last_model: 'm', max_messages: 5, title: 'hi', label: '' },
    exchanges: [
      row('a'),
      row('b', { parent_id: 'a', edge: 'continue', match_level: 'no_thinking', prefix_messages: 2, req_messages: 3 }),
      row('b2', { parent_id: 'b', edge: 'retry', prefix_messages: 3, req_messages: 3 }),
    ],
    subagents: [{ parent_exchange_id: 'a', parent_call_id: 'call_task', exchange_id: 'sub1', session_id: 'sub', title: 'Complete assignment thoroughly', exchanges: 2 }],
  }

  beforeEach(() => {
    vi.clearAllMocks()
    // 所有卡片立即“进入视口”
    vi.stubGlobal('IntersectionObserver', class {
      constructor(private cb: (entries: { isIntersecting: boolean }[]) => void) {}
      observe() { this.cb([{ isIntersecting: true }]) }
      disconnect() {}
    })
    mocks.getSession.mockResolvedValue(session)
    mocks.getExchange.mockImplementation(async (_id: string, from: number) => detail(from))
  })

  afterEach(() => vi.unstubAllGlobals())

  it('shows the main path, fetches only each turn\'s new messages and renders blocks', async () => {
    const wrapper = mount(SessionReplay, { props: { sessionId: 's' } })
    await flushPromises()
    expect(mocks.getExchange).toHaveBeenCalledTimes(2)
    expect(mocks.getExchange).toHaveBeenCalledWith('a', 0)
    expect(mocks.getExchange).toHaveBeenCalledWith('b2', 2)
    expect(wrapper.find('[data-test="trunk-hint"]').exists()).toBe(true)
    expect(wrapper.find('[data-test="exchange-b"]').exists()).toBe(false)

    await wrapper.find('[data-test="mode-all"]').trigger('click')
    await flushPromises()
    expect(mocks.getExchange).toHaveBeenCalledWith('b', 2)
    expect(mocks.getExchange).toHaveBeenCalledWith('b2', 3)
    const card = wrapper.find('[data-test="exchange-b"]')
    expect(card.find('[data-test="edge-badge"]').text()).toContain('admin.recording.replay.edge.continue')
    expect(card.find('[data-test="prefix-skipped"]').exists()).toBe(true)
    expect(card.find('[data-test="system-prompt"]').exists()).toBe(false)
    expect(card.find('[data-test="block-thinking"]').text()).toContain('look at cwd')
    expect(card.find('[data-test="block-tool-call"]').text()).toContain('"path": "."')
    expect(wrapper.find('[data-test="exchange-a"] [data-test="system-prompt"]').exists()).toBe(true)
    expect(wrapper.find('[data-test="exchange-b2"] [data-test="edge-badge"]').text()).toContain('admin.recording.replay.edge.retry')

    // 派出子 agent 的那一轮显示子 agent，点击打开子会话
    const box = wrapper.find('[data-test="exchange-a"] [data-test="subagents"]')
    expect(box.text()).toContain('call_task')
    expect(wrapper.find('[data-test="exchange-b"] [data-test="subagents"]').exists()).toBe(false)
    await box.find('[data-test="open-subagent-sub"]').trigger('click')
    expect(wrapper.emitted('open-session')?.[0]).toEqual(['sub'])
  })
})

describe('SessionReplay for a subagent session', () => {
  beforeEach(() => {
    vi.clearAllMocks()
    vi.stubGlobal('IntersectionObserver', class {
      constructor(private cb: (entries: { isIntersecting: boolean }[]) => void) {}
      observe() { this.cb([{ isIntersecting: true }]) }
      disconnect() {}
    })
  })
  afterEach(() => vi.unstubAllGlobals())

  it('shows the parent session breadcrumb and builds the trunk from subagent rows', async () => {
    const base = { user_id: 1, hint_source: '', hint: '', detected_client: 'oh-my-pi', first_at: '2026-10-12T03:07:42Z', last_at: '2026-10-12T03:11:59Z', exchanges: 2, input_tokens: 1, output_tokens: 1, cache_read_tokens: 0, last_model: 'm', max_messages: 3, label: '' }
    mocks.getSession.mockResolvedValue({
      session: { ...base, session_id: 'sub', title: 'Complete assignment', parent_session_id: 'main', parent_exchange_id: 'a', parent_call_id: 'call_task' },
      exchanges: [
        row('sub1', { session_id: 'sub', kind: 'subagent', parent_id: 'a', edge: 'subagent', match_level: 'task_args' }),
        row('sub2', { session_id: 'sub', kind: 'subagent', parent_id: 'sub1', edge: 'continue', prefix_messages: 2, req_messages: 3 }),
      ],
      subagents: [],
      parent: { ...base, session_id: 'main', title: 'main task', label: 'my label' },
    })
    mocks.getExchange.mockResolvedValue({ meta: { exchange_id: 'x' }, dir: '/d', files: [], messages_from: 0, total_messages: 1, request: { protocol: 'openai_responses', messages: [] } })
    const wrapper = mount(SessionReplay, { props: { sessionId: 'sub' } })
    await flushPromises()
    const crumb = wrapper.find('[data-test="parent-session"]')
    expect(crumb.text()).toContain('my label')
    expect(crumb.text()).toContain('call_task')
    await crumb.find('button').trigger('click')
    expect(wrapper.emitted('open-session')?.[0]).toEqual(['main'])
    expect(wrapper.find('[data-test="exchange-sub1"] [data-test="edge-badge"]').text()).toContain('admin.recording.replay.edge.subagentExternal')
    expect(wrapper.find('[data-test="exchange-sub2"]').exists()).toBe(true)
    expect(wrapper.find('[data-test="trunk-hint"]').exists()).toBe(false)
  })
})
