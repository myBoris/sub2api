import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { enableAutoUnmount, flushPromises, mount } from '@vue/test-utils'
import { reactive } from 'vue'
import SubscriptionRequestButton from '../SubscriptionRequestButton.vue'
import type { SubscriptionRequest } from '@/api/subscriptionRequests'

const mocks = vi.hoisted(() => ({
  getSubscriptionRequestPlans: vi.fn(), getSubscriptionRequests: vi.fn(),
  createSubscriptionRequest: vi.fn(), updateSubscriptionRequest: vi.fn(), showSuccess: vi.fn(),
}))
vi.mock('@/api/subscriptionRequests', () => mocks)
const auth = reactive({ isAuthenticated: true, isAdmin: false, user: { id: 1, role: 'user' } as { id: number; role: string } | null })
vi.mock('@/stores/auth', () => ({ useAuthStore: () => auth }))
vi.mock('@/stores/app', () => ({ useAppStore: () => ({ showSuccess: mocks.showSuccess }) }))
enableAutoUnmount(afterEach)

const plans = [{ id: 5, platform: 'OpenAI', name: 'Plus', price: 150 }, { id: 6, platform: 'Claude', name: 'Pro', price: 180 }]
function request(overrides: Partial<SubscriptionRequest> = {}): SubscriptionRequest {
  return { id: 9, plan_id: 5, platform: 'OpenAI', plan_name: 'Plus', price: 150, status: 'pending', created_at: '2026-11-22T10:00:00Z', updated_at: '2026-11-22T10:00:00Z', completed_at: null, ...overrides }
}
const renderButton = () => mount(SubscriptionRequestButton, { global: { stubs: {
  Icon: true,
  BaseDialog: { props: ['show'], emits: ['close'], template: '<section v-if="show" role="dialog"><slot /><slot name="footer" /></section>' },
} } })

beforeEach(() => {
  vi.resetAllMocks()
  auth.isAuthenticated = true
  auth.isAdmin = false
  auth.user = { id: 1, role: 'user' }
  mocks.getSubscriptionRequestPlans.mockResolvedValue({ plans })
  mocks.getSubscriptionRequests.mockResolvedValue({ items: [] })
  mocks.createSubscriptionRequest.mockResolvedValue(request())
})

describe('subscription account request button', () => {
  it('loads only when opened and submits the selected catalog plan', async () => {
    const wrapper = renderButton()
    expect(mocks.getSubscriptionRequestPlans).not.toHaveBeenCalled()
    expect(mocks.getSubscriptionRequests).not.toHaveBeenCalled()
    await wrapper.get('[data-testid="subscription-request-button"]').trigger('click')
    await flushPromises()
    expect(wrapper.get('select').text()).toContain('OpenAI / Plus · ¥150.00')
    expect(wrapper.text()).not.toContain('成本')
    await wrapper.get('#subscription-request-form').trigger('submit')
    await flushPromises()
    expect(mocks.createSubscriptionRequest).toHaveBeenCalledWith(5)
    expect(wrapper.get('[data-testid="current-subscription-request"]').text()).toContain('待处理')
    expect(wrapper.get('[data-testid="subscription-request-submit"]').attributes('disabled')).toBeDefined()
    expect(mocks.showSuccess).toHaveBeenCalledWith('申请已提交')
  })

  it('changes a pending application with PUT and keeps completed history read only', async () => {
    mocks.getSubscriptionRequests.mockResolvedValue({ items: [request(), request({ id: 8, status: 'completed', price: 120, completed_at: '2026-11-21T10:00:00Z' })] })
    mocks.updateSubscriptionRequest.mockResolvedValue(request({ plan_id: 6, platform: 'Claude', plan_name: 'Pro', price: 180 }))
    const wrapper = renderButton()
    await wrapper.get('[data-testid="subscription-request-button"]').trigger('click')
    await flushPromises()
    expect(wrapper.get('[data-testid="completed-subscription-request"]').text()).toContain('¥120.00')
    expect(wrapper.get('[data-testid="completed-subscription-request"]').find('button').exists()).toBe(false)
    await wrapper.get('select').setValue(6)
    await wrapper.get('#subscription-request-form').trigger('submit')
    await flushPromises()
    expect(mocks.updateSubscriptionRequest).toHaveBeenCalledWith(9, 6)
    expect(mocks.createSubscriptionRequest).not.toHaveBeenCalled()
    expect(wrapper.get('[data-testid="current-subscription-request"]').text()).toContain('Claude / Pro')
  })

  it('allows a new application when all previous requests are complete', async () => {
    mocks.getSubscriptionRequests.mockResolvedValue({ items: [request({ status: 'completed' })] })
    const wrapper = renderButton()
    await wrapper.get('[data-testid="subscription-request-button"]').trigger('click')
    await flushPromises()
    expect(wrapper.find('[data-testid="current-subscription-request"]').exists()).toBe(false)
    expect(wrapper.get('[data-testid="subscription-request-submit"]').text()).toBe('提交申请')
    await wrapper.get('#subscription-request-form').trigger('submit')
    await flushPromises()
    expect(mocks.createSubscriptionRequest).toHaveBeenCalledTimes(1)
  })

  it('reloads a concurrently completed request after a conflict and offers a new request', async () => {
    mocks.getSubscriptionRequests.mockResolvedValueOnce({ items: [request()] }).mockResolvedValue({ items: [request({ status: 'completed' })] })
    mocks.updateSubscriptionRequest.mockRejectedValue({ status: 409, message: '申请已经完成' })
    const wrapper = renderButton()
    await wrapper.get('[data-testid="subscription-request-button"]').trigger('click')
    await flushPromises()
    await wrapper.get('select').setValue(6)
    await wrapper.get('#subscription-request-form').trigger('submit')
    await flushPromises()
    expect(wrapper.get('[role="alert"]').text()).toBe('申请已经完成')
    expect(wrapper.find('[data-testid="current-subscription-request"]').exists()).toBe(false)
    expect(wrapper.get('[data-testid="subscription-request-submit"]').text()).toBe('提交申请')
  })

  it('keeps a failed submit editable and prevents duplicate in-flight submissions', async () => {
    let reject!: (reason: unknown) => void
    mocks.createSubscriptionRequest.mockReturnValue(new Promise((_resolve, fail) => { reject = fail }))
    const wrapper = renderButton()
    await wrapper.get('[data-testid="subscription-request-button"]').trigger('click')
    await flushPromises()
    await wrapper.get('#subscription-request-form').trigger('submit')
    await wrapper.get('#subscription-request-form').trigger('submit')
    expect(mocks.createSubscriptionRequest).toHaveBeenCalledTimes(1)
    reject({ message: '暂时不可用' })
    await flushPromises()
    expect(wrapper.get('[role="alert"]').text()).toBe('暂时不可用')
    expect(wrapper.get('[data-testid="subscription-request-submit"]').attributes('disabled')).toBeUndefined()
  })

  it('disables applications when no catalog plans are available', async () => {
    mocks.getSubscriptionRequestPlans.mockResolvedValue({ plans: [] })
    const wrapper = renderButton()
    await wrapper.get('[data-testid="subscription-request-button"]').trigger('click')
    await flushPromises()
    expect(wrapper.get('select').text()).toContain('暂无可申请套餐')
    expect(wrapper.get('[data-testid="subscription-request-submit"]').attributes('disabled')).toBeDefined()
  })

  it.each(['admin', 'guest'])('does not expose an application button to %s sessions', async role => {
    auth.user = role === 'admin' ? { id: 1, role: 'admin' } : null
    auth.isAdmin = role === 'admin'
    auth.isAuthenticated = role === 'admin'
    const wrapper = renderButton()
    await flushPromises()
    expect(wrapper.find('button').exists()).toBe(false)
    expect(mocks.getSubscriptionRequests).not.toHaveBeenCalled()
  })

  it('discards another user session response and clears the open dialog', async () => {
    let resolve!: (value: { items: SubscriptionRequest[] }) => void
    mocks.getSubscriptionRequests.mockReturnValue(new Promise(accept => { resolve = accept }))
    const wrapper = renderButton()
    await wrapper.get('[data-testid="subscription-request-button"]').trigger('click')
    auth.user = { id: 2, role: 'user' }
    await flushPromises()
    resolve({ items: [request()] })
    await flushPromises()
    expect(wrapper.find('[role="dialog"]').exists()).toBe(false)
    expect(wrapper.text()).not.toContain('OpenAI')
  })
})
