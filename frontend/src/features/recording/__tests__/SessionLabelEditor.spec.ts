import { beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'
import SessionLabelEditor from '../components/SessionLabelEditor.vue'

const mocks = vi.hoisted(() => ({ setSessionLabel: vi.fn(), showError: vi.fn() }))
vi.mock('../api', () => ({ default: mocks }))
vi.mock('@/stores/app', () => ({ useAppStore: () => ({ showError: mocks.showError }) }))
vi.mock('vue-i18n', async () => {
  const actual = await vi.importActual<typeof import('vue-i18n')>('vue-i18n')
  return { ...actual, useI18n: () => ({ t: (key: string) => key }) }
})

describe('SessionLabelEditor', () => {
  beforeEach(() => vi.clearAllMocks())

  it('falls back to the auto title and saves a trimmed label on Enter', async () => {
    mocks.setSessionLabel.mockResolvedValue({ session_id: 's1', label: 'parser refactor' })
    const parentClick = vi.fn()
    const wrapper = mount({
      components: { SessionLabelEditor },
      template: '<div @click="parentClick"><SessionLabelEditor session-id="s1" label="" title="你好" @saved="saved = $event" /></div>',
      data: () => ({ saved: null as string | null }),
      methods: { parentClick },
    })
    expect(wrapper.find('[data-test="label-text"]').text()).toBe('你好')
    await wrapper.find('[data-test="label-edit"]').trigger('click')
    expect(parentClick).not.toHaveBeenCalled()
    const input = wrapper.find('[data-test="label-input"]')
    await input.setValue('  parser refactor  ')
    await input.trigger('keydown', { key: 'Enter' })
    await flushPromises()
    expect(mocks.setSessionLabel).toHaveBeenCalledWith('s1', 'parser refactor')
    expect((wrapper.vm as unknown as { saved: string }).saved).toBe('parser refactor')
    expect(wrapper.find('[data-test="label-input"]').exists()).toBe(false)
  })

  it('cancels on Escape without saving and skips unchanged labels', async () => {
    const wrapper = mount(SessionLabelEditor, { props: { sessionId: 's1', label: 'keep', title: 't' } })
    await wrapper.find('[data-test="label-edit"]').trigger('click')
    await wrapper.find('[data-test="label-input"]').setValue('changed')
    await wrapper.find('[data-test="label-input"]').trigger('keydown', { key: 'Escape' })
    expect(mocks.setSessionLabel).not.toHaveBeenCalled()
    expect(wrapper.find('[data-test="label-text"]').text()).toBe('keep')

    await wrapper.find('[data-test="label-edit"]').trigger('click')
    await wrapper.find('[data-test="label-save"]').trigger('click')
    expect(mocks.setSessionLabel).not.toHaveBeenCalled()
  })

  it('shows an error and stays in edit mode when saving fails', async () => {
    mocks.setSessionLabel.mockRejectedValue({ message: 'boom' })
    const wrapper = mount(SessionLabelEditor, { props: { sessionId: 's1', label: '', title: 't' } })
    await wrapper.find('[data-test="label-edit"]').trigger('click')
    await wrapper.find('[data-test="label-input"]').setValue('x')
    await wrapper.find('[data-test="label-save"]').trigger('click')
    await flushPromises()
    expect(mocks.showError).toHaveBeenCalledWith('boom')
    expect(wrapper.find('[data-test="label-input"]').exists()).toBe(true)
  })
})
