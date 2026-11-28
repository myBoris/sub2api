import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { enableAutoUnmount, flushPromises, mount } from '@vue/test-utils'
import SubscriptionRequestsView from '../SubscriptionRequestsView.vue'
import type { AdminSubscriptionRequest } from '@/api/subscriptionRequests'

const mocks = vi.hoisted(() => ({ listAdminSubscriptionRequests: vi.fn(), completeSubscriptionRequest: vi.fn(), showSuccess: vi.fn() }))
vi.mock('@/api/subscriptionRequests', () => mocks)
vi.mock('@/stores/app', () => ({ useAppStore: () => ({ showSuccess: mocks.showSuccess }) }))
enableAutoUnmount(afterEach)
const item = (overrides: Partial<AdminSubscriptionRequest> = {}): AdminSubscriptionRequest => ({
  id: 9, user_id: 7, user_name: '申请甲', user_email: 'a@example.com', plan_id: 5, platform: 'OpenAI', plan_name: 'Plus', price: 150, cost: 75,
  status: 'pending', created_at: '2026-11-22T10:00:00Z', updated_at: '2026-11-22T10:00:00Z', completed_at: null, completed_by: null, ...overrides,
})
const renderView = () => mount(SubscriptionRequestsView, { global: { stubs: { AppLayout: { template: '<main><slot /></main>' } } } })

beforeEach(() => {
  vi.resetAllMocks()
  mocks.listAdminSubscriptionRequests.mockResolvedValue({ items: [item(), item({ id: 8, user_name: '申请乙', platform: 'Claude', status: 'completed', completed_at: '2026-11-21T10:00:00Z' })] })
  mocks.completeSubscriptionRequest.mockResolvedValue(item({ status: 'completed', completed_at: '2026-11-22T11:00:00Z', completed_by: 1 }))
})

describe('administrator subscription account requests', () => {
  it('searches users and plans, filters status, and shows only pending completion actions', async () => {
    const wrapper = renderView()
    await flushPromises()
    expect(wrapper.findAll('tbody tr')).toHaveLength(2)
    expect(wrapper.findAll('tbody button')).toHaveLength(1)
    await wrapper.get('[aria-label="搜索订阅账号申请"]').setValue('Claude')
    expect(wrapper.get('tbody').text()).toContain('申请乙')
    expect(wrapper.get('tbody').text()).not.toContain('申请甲')
    await wrapper.get('[aria-label="申请状态"]').setValue('pending')
    expect(wrapper.get('tbody').text()).toContain('没有匹配的申请')
    await wrapper.get('[aria-label="搜索订阅账号申请"]').setValue('a@example.com')
    expect(wrapper.get('tbody').text()).toContain('申请甲')
  })

  it('marks a pending request complete and immediately removes its completion action', async () => {
    const wrapper = renderView()
    await flushPromises()
    await wrapper.get('tbody button').trigger('click')
    await flushPromises()
    expect(mocks.completeSubscriptionRequest).toHaveBeenCalledWith(9, '2026-11-22T10:00:00Z')
    expect(wrapper.findAll('tbody button')).toHaveLength(0)
    expect(mocks.showSuccess).toHaveBeenCalledWith('申请已完成')
  })

  it('keeps failed requests pending and prevents duplicate completion calls', async () => {
    let reject!: (reason: unknown) => void
    mocks.completeSubscriptionRequest.mockReturnValue(new Promise((_resolve, fail) => { reject = fail }))
    const wrapper = renderView()
    await flushPromises()
    await wrapper.get('tbody button').trigger('click')
    await wrapper.get('tbody button').trigger('click')
    expect(mocks.completeSubscriptionRequest).toHaveBeenCalledTimes(1)
    reject({ message: '处理失败' })
    await flushPromises()
    expect(wrapper.get('[role="alert"]').text()).toBe('处理失败')
    expect(wrapper.get('tbody button').text()).toBe('完成申请')
  })

  it('shows an API failure and allows refreshing', async () => {
    mocks.listAdminSubscriptionRequests.mockRejectedValueOnce({ message: '暂时不可用' })
    const wrapper = renderView()
    await flushPromises()
    expect(wrapper.get('[role="alert"]').text()).toBe('暂时不可用')
    await wrapper.get('button').trigger('click')
    await flushPromises()
    expect(wrapper.find('[role="alert"]').exists()).toBe(false)
    expect(wrapper.findAll('tbody tr')).toHaveLength(2)
  })

  it('refreshes a concurrently changed application instead of completing a new plan from stale data', async () => {
    const wrapper = renderView()
    await flushPromises()
    mocks.completeSubscriptionRequest.mockRejectedValue({ status: 409, message: '申请已变化' })
    mocks.listAdminSubscriptionRequests.mockResolvedValue({ items: [item({ plan_id: 6, platform: 'Claude', plan_name: 'Pro', price: 180, updated_at: '2026-11-22T10:01:00Z' })] })
    await wrapper.get('tbody button').trigger('click')
    await flushPromises()
    expect(mocks.listAdminSubscriptionRequests).toHaveBeenCalledTimes(2)
    expect(wrapper.get('[role="alert"]').text()).toBe('申请已变化，请刷新后重试')
    expect(wrapper.get('tbody').text()).toContain('Claude / Pro')
    expect(wrapper.get('tbody button').text()).toBe('完成申请')
    expect(mocks.showSuccess).not.toHaveBeenCalled()
  })
})
