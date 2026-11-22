import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { enableAutoUnmount, flushPromises, mount } from '@vue/test-utils'
import { nextTick, reactive } from 'vue'
import type { UserBillingSummary } from '@/api/accountBilling'
import zhCommon from '@/i18n/locales/zh/common'
import AccountBillingReminder from '../AccountBillingReminder.vue'

const mocks = vi.hoisted(() => ({
  getUserBillingSummary: vi.fn<() => Promise<UserBillingSummary>>(),
}))
const auth = reactive({
  isAuthenticated: true,
  isAdmin: false,
  user: { id: 1, role: 'user' } as { id: number; role: string } | null,
})
const route = reactive({ fullPath: '/dashboard' })

vi.mock('@/api/accountBilling', () => ({ getUserBillingSummary: mocks.getUserBillingSummary }))
vi.mock('@/stores/auth', () => ({ useAuthStore: () => auth }))
vi.mock('vue-router', async (importOriginal) => ({
  ...(await importOriginal<typeof import('vue-router')>()),
  useRoute: () => route,
}))
vi.mock('vue-i18n', () => ({
  useI18n: () => ({
    t: (key: string, values: Record<string, unknown> = {}) => {
      let message: unknown = zhCommon
      for (const part of key.split('.')) {
        message = (message as Record<string, unknown>)[part]
      }
      return String(message).replace(/\{(\w+)\}/g, (_, name: string) => String(values[name]))
    },
  }),
}))

enableAutoUnmount(afterEach)

type SummaryAccount = UserBillingSummary['accounts'][number]

function account(overrides: Partial<SummaryAccount> = {}): SummaryAccount {
  return {
    account_id: 1,
    renewal_date: '2031-02-25',
    price: 100,
    payment_status: 'unpaid',
    ...overrides,
  }
}

function billing(overrides: Partial<UserBillingSummary> = {}): UserBillingSummary {
  return {
    month: '2031-02',
    amount_due: 100,
    total_amount: 100,
    accounts: [account()],
    ...overrides,
  }
}

function deferredBilling() {
  let resolve!: (value: UserBillingSummary) => void
  const promise = new Promise<UserBillingSummary>((accept) => { resolve = accept })
  return { promise, resolve }
}

function renderReminder() {
  return mount(AccountBillingReminder, {
    global: { stubs: { Icon: true } },
  })
}

describe('AccountBillingReminder', () => {
  beforeEach(() => {
    mocks.getUserBillingSummary.mockReset().mockResolvedValue(billing())
    auth.user = { id: 1, role: 'user' }
    auth.isAuthenticated = true
    auth.isAdmin = false
    route.fullPath = '/dashboard'
  })

  it('warns only about accounts expired by the server using their actual validity date', async () => {
    mocks.getUserBillingSummary.mockResolvedValueOnce(billing({
      accounts: [
        account({ account_id: 1, renewal_date: '2031-03-25', valid_through: '2031-01-25', expired: true }),
        account({ account_id: 2, valid_through: '2031-02-28', expired: false }),
        account({ account_id: 3, valid_through: null, expired: false }),
      ],
    }))
    const wrapper = renderReminder()
    await flushPromises()
    expect(wrapper.get('[data-testid="account-billing-expired"]').text()).toContain('1 个账号API已到期')
    expect(wrapper.get('[data-testid="account-billing-expired"]').text()).toContain('2031-01-25')
    expect(wrapper.get('[data-testid="account-billing-expired"]').text()).toContain('其他未到期账号仍可用')
    expect(wrapper.get('[data-testid="account-billing-reminder"]').attributes('aria-label')).toContain('1 个账号API已到期')
  })

  it('does not infer expiry from dates on the client when the server reports accounts available', async () => {
    mocks.getUserBillingSummary.mockResolvedValueOnce(billing({ accounts: [
      account({ valid_through: '2020-01-01', expired: false }),
      account({ account_id: 2, valid_through: null, expired: false }),
    ] }))
    const wrapper = renderReminder()
    await flushPromises()
    expect(wrapper.find('[data-testid="account-billing-expired"]').exists()).toBe(false)
  })

  it('shows the summary month, amount and earliest unpaid priced account date as plain text', async () => {
    mocks.getUserBillingSummary.mockResolvedValueOnce(billing({
      amount_due: 88.88,
      total_amount: 999,
      accounts: [
        account({ account_id: 1, payment_status: 'paid', renewal_date: '2031-02-01' }),
        account({ account_id: 2, price: 0, renewal_date: '2031-02-02' }),
        account({ account_id: 3, renewal_date: '2031-02-26' }),
        // Converting this timestamp into UTC would incorrectly show February 24.
        account({ account_id: 4, renewal_date: '2031-02-25T00:30:00+14:00' }),
      ],
    }))
    const wrapper = renderReminder()
    await flushPromises()

    const reminder = wrapper.get('[data-testid="account-billing-reminder"]')
    expect(reminder.text()).toContain('本月待充值')
    expect(reminder.text()).toContain('¥88.88')
    expect(reminder.text()).toContain('2031-02 · 最早续费 2031-02-25')
    expect(reminder.text()).not.toContain('2031-02-24')
    expect(reminder.text()).not.toContain('¥999.00')
    expect(reminder.attributes('aria-label')).toContain('最早续费 2031-02-25')
    expect(reminder.element.tagName).toBe('DIV')
    expect(wrapper.find('a, button, [tabindex]').exists()).toBe(false)
    expect(reminder.attributes('href')).toBeUndefined()
    expect(mocks.getUserBillingSummary).toHaveBeenCalledTimes(1)
  })

  it('shows zero and no payment needed when the server reports nothing due', async () => {
    mocks.getUserBillingSummary.mockResolvedValueOnce(billing({ amount_due: 0 }))
    const wrapper = renderReminder()
    await flushPromises()

    expect(wrapper.text()).toContain('¥0.00')
    expect(wrapper.text()).toContain('本月无需充值')
    expect(wrapper.text()).not.toContain('最早续费')
  })

  it('keeps the amount visible and says the renewal date is unset when no valid date exists', async () => {
    mocks.getUserBillingSummary.mockResolvedValueOnce(billing({
      accounts: [
        account({ renewal_date: null }),
        account({ account_id: 2, renewal_date: 'not-a-date' }),
        account({ account_id: 3, payment_status: 'paid', renewal_date: '2031-02-01' }),
      ],
    }))
    const wrapper = renderReminder()
    await flushPromises()

    expect(wrapper.text()).toContain('¥100.00')
    expect(wrapper.text()).toContain('续费时间待设置')
    expect(wrapper.text()).not.toContain('本月无需充值')
  })

  it.each(['anonymous', 'admin', 'other role'])('does not request a summary for an %s session', async (session) => {
    if (session === 'anonymous') {
      auth.isAuthenticated = false
      auth.user = null
    } else if (session === 'admin') {
      auth.isAdmin = true
      auth.user = { id: 1, role: 'admin' }
    } else {
      auth.user = { id: 1, role: 'operator' }
    }
    const wrapper = renderReminder()
    await flushPromises()

    expect(mocks.getUserBillingSummary).not.toHaveBeenCalled()
    expect(wrapper.find('[data-testid="account-billing-reminder"]').exists()).toBe(false)
  })

  it('loads the summary when an anonymous session becomes an ordinary user', async () => {
    auth.isAuthenticated = false
    auth.user = null
    const wrapper = renderReminder()
    await flushPromises()
    expect(mocks.getUserBillingSummary).not.toHaveBeenCalled()

    auth.isAuthenticated = true
    auth.user = { id: 1, role: 'user' }
    await flushPromises()

    expect(mocks.getUserBillingSummary).toHaveBeenCalledTimes(1)
    expect(wrapper.text()).toContain('¥100.00')
  })

  it('hides a failed request instead of presenting it as a zero amount due', async () => {
    mocks.getUserBillingSummary.mockRejectedValueOnce(new Error('offline'))
    const wrapper = renderReminder()
    await flushPromises()

    expect(wrapper.find('[data-testid="account-billing-reminder"]').exists()).toBe(false)
    expect(wrapper.text()).not.toContain('¥0.00')
  })

  it('ignores a pending response after the user signs out', async () => {
    const pending = deferredBilling()
    mocks.getUserBillingSummary.mockReturnValueOnce(pending.promise)
    const wrapper = renderReminder()

    auth.isAuthenticated = false
    auth.user = null
    await nextTick()
    pending.resolve(billing({ amount_due: 999 }))
    await flushPromises()

    expect(wrapper.find('[data-testid="account-billing-reminder"]').exists()).toBe(false)
    expect(mocks.getUserBillingSummary).toHaveBeenCalledTimes(1)
  })

  it('removes a displayed amount if the same user is no longer an ordinary user', async () => {
    const wrapper = renderReminder()
    await flushPromises()
    expect(wrapper.text()).toContain('¥100.00')

    auth.user!.role = 'admin'
    await nextTick()

    expect(wrapper.find('[data-testid="account-billing-reminder"]').exists()).toBe(false)
    expect(mocks.getUserBillingSummary).toHaveBeenCalledTimes(1)
  })

  it('does not let a previous user response replace the new user amount', async () => {
    const oldRequest = deferredBilling()
    const newRequest = deferredBilling()
    mocks.getUserBillingSummary
      .mockReturnValueOnce(oldRequest.promise)
      .mockReturnValueOnce(newRequest.promise)
    const wrapper = renderReminder()

    auth.user = { id: 2, role: 'user' }
    await nextTick()
    expect(mocks.getUserBillingSummary).toHaveBeenCalledTimes(2)
    expect(wrapper.find('[data-testid="account-billing-reminder"]').exists()).toBe(false)

    newRequest.resolve(billing({ amount_due: 32.25 }))
    await flushPromises()
    expect(wrapper.text()).toContain('¥32.25')

    oldRequest.resolve(billing({ amount_due: 999 }))
    await flushPromises()
    expect(wrapper.text()).toContain('¥32.25')
    expect(wrapper.text()).not.toContain('¥999.00')
  })

  it('clears the previous user amount while the next user summary is loading', async () => {
    const wrapper = renderReminder()
    await flushPromises()
    expect(wrapper.text()).toContain('¥100.00')

    const pending = deferredBilling()
    mocks.getUserBillingSummary.mockReturnValueOnce(pending.promise)
    auth.user = { id: 2, role: 'user' }
    await nextTick()
    expect(wrapper.find('[data-testid="account-billing-reminder"]').exists()).toBe(false)

    pending.resolve(billing({ amount_due: 55.5 }))
    await flushPromises()
    expect(wrapper.text()).toContain('¥55.50')
    expect(wrapper.text()).not.toContain('¥100.00')
  })

  it('refreshes on navigation and focus, hiding stale amounts when refresh fails', async () => {
    const wrapper = renderReminder()
    await flushPromises()
    expect(wrapper.text()).toContain('¥100.00')

    mocks.getUserBillingSummary.mockResolvedValueOnce(billing({ amount_due: 25.5 }))
    route.fullPath = '/keys'
    await flushPromises()
    expect(wrapper.text()).toContain('¥25.50')

    mocks.getUserBillingSummary.mockRejectedValueOnce(new Error('offline'))
    window.dispatchEvent(new Event('focus'))
    await flushPromises()
    expect(wrapper.find('[data-testid="account-billing-reminder"]').exists()).toBe(false)
    expect(mocks.getUserBillingSummary).toHaveBeenCalledTimes(3)
  })

  it('ignores an old navigation response after a newer summary has loaded', async () => {
    const pending = deferredBilling()
    mocks.getUserBillingSummary.mockReturnValueOnce(pending.promise)
    const wrapper = renderReminder()

    mocks.getUserBillingSummary.mockResolvedValueOnce(billing({ amount_due: 8.88 }))
    route.fullPath = '/keys'
    await flushPromises()
    pending.resolve(billing({ amount_due: 999 }))
    await flushPromises()

    expect(wrapper.text()).toContain('¥8.88')
    expect(wrapper.text()).not.toContain('¥999.00')
  })

  it('stops refreshing on focus after the reminder is unmounted', async () => {
    const pending = deferredBilling()
    mocks.getUserBillingSummary.mockReturnValueOnce(pending.promise)
    const wrapper = renderReminder()
    wrapper.unmount()

    pending.resolve(billing())
    window.dispatchEvent(new Event('focus'))
    await flushPromises()

    expect(mocks.getUserBillingSummary).toHaveBeenCalledTimes(1)
  })
})
