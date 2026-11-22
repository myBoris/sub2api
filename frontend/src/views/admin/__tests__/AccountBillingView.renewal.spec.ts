import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'
import { nextTick, reactive } from 'vue'
import AccountBillingView from '../AccountBillingView.vue'
import type { BillingAccountList, BillingAccountUpdate } from '@/api/accountBilling'

const mocks = vi.hoisted(() => ({
  getCatalog: vi.fn(),
  listBillingAccounts: vi.fn(),
  syncBillingAccounts: vi.fn(),
  updateBillingAccount: vi.fn(),
  updateBillingHistory: vi.fn(),
  showSuccess: vi.fn(),
  showError: vi.fn(),
}))

vi.mock('@/api/accountBilling', () => ({
  getCatalog: mocks.getCatalog,
  listBillingAccounts: mocks.listBillingAccounts,
  syncBillingAccounts: mocks.syncBillingAccounts,
  updateBillingAccount: mocks.updateBillingAccount,
  updateBillingHistory: mocks.updateBillingHistory,
}))
vi.mock('@/stores/app', () => ({
  useAppStore: () => ({ showSuccess: mocks.showSuccess, showError: mocks.showError }),
}))

const mountView = () => mount(AccountBillingView, {
  global: {
    stubs: {
      AppLayout: { template: '<main><slot /></main>' },
      BaseDialog: {
        props: ['show'],
        template: '<section v-if="show"><slot /><slot name="footer" /></section>',
      },
    },
  },
})

let wrapper: ReturnType<typeof mountView> | undefined
let ledger: BillingAccountList

beforeEach(() => {
  vi.clearAllMocks()
  // This account retains a January 30 anchor; February temporarily uses its
  // last day. The backend may roll the same reactive row while editing is open.
  ledger = reactive<BillingAccountList>({
    month: '2027-02',
    items: [{
      account_id: 42,
      account_name: 'Monthly renewal account',
      platform: 'openai',
      renewal_date: '2027-02-28',
      plan_id: null,
      plan_name: null,
      price: 100,
      payer_id: null,
      payer_name: null,
      payment_status: 'unpaid',
      history: [],
    }],
    summary: { paid_count: 0, unpaid_count: 1, due_soon_count: 0, payers: [] },
  })
  mocks.getCatalog.mockResolvedValue({ plans: [], payers: [] })
  mocks.listBillingAccounts.mockResolvedValue(ledger)
  mocks.updateBillingAccount.mockImplementation(async () => ledger.items[0])
})

afterEach(() => {
  wrapper?.unmount()
  wrapper = undefined
})

async function openEditorThenRollMonth() {
  wrapper = mountView()
  await flushPromises()
  const edit = wrapper.findAll('button').find(button => button.text() === '编辑')
  expect(edit).toBeDefined()
  await edit!.trigger('click')
  expect(wrapper.get<HTMLInputElement>('#account-billing-edit input[type="date"]').element.value)
    .toBe('2027-02-28')

  ledger.month = '2027-03'
  ledger.items[0]!.renewal_date = '2027-03-30'
  await nextTick()
  expect(wrapper.get('tbody').text()).toContain('2027-03-30')
  // The open form must retain the date the administrator actually saw, even
  // though its source row has advanced to the next month's original anchor.
  expect(wrapper.get<HTMLInputElement>('#account-billing-edit input[type="date"]').element.value)
    .toBe('2027-02-28')
  return wrapper
}

async function submit(): Promise<BillingAccountUpdate> {
  await wrapper!.get('#account-billing-edit').trigger('submit')
  await flushPromises()
  expect(mocks.updateBillingAccount).toHaveBeenCalledTimes(1)
  expect(mocks.updateBillingAccount.mock.calls[0]![0]).toBe(42)
  expect(mocks.showError).not.toHaveBeenCalled()
  expect(mocks.showSuccess).toHaveBeenCalledWith('账号支付信息已保存')
  return mocks.updateBillingAccount.mock.calls[0]![1] as BillingAccountUpdate
}

describe('account billing renewal edits across a month rollover', () => {
  it('shows the actual expired API deadline even when the displayed renewal month has advanced', async () => {
    ledger.items[0]!.renewal_date = '2027-03-30'
    ledger.items[0]!.valid_through = '2027-02-28'
    ledger.items[0]!.expired = true
    wrapper = mountView()
    await flushPromises()
    expect(wrapper.get('tbody').text()).toContain('2027-03-30')
    expect(wrapper.get('[data-testid="billing-api-validity"]').text()).toContain('API已到期 · 有效期至 2027-02-28')
  })

  it('keeps the current API deadline available through its full day', async () => {
    ledger.items[0]!.valid_through = '2027-02-28'
    ledger.items[0]!.expired = false
    wrapper = mountView()
    await flushPromises()
    expect(wrapper.get('[data-testid="billing-api-validity"]').text()).toContain('API未到期 · 有效期至 2027-02-28')
  })

  it('keeps legacy records without validity metadata compatible', async () => {
    wrapper = mountView()
    await flushPromises()
    expect(wrapper.find('[data-testid="billing-api-validity"]').exists()).toBe(false)
  })

  it('preserves the existing renewal schedule when only the price changes in an old form', async () => {
    const view = await openEditorThenRollMonth()
    await view.get('#account-billing-edit input[type="number"]').setValue('125.50')

    expect(await submit()).toMatchObject({
      price: 125.5,
      renewal_date: '2027-02-28',
      renewal_date_changed: false,
    })
  })

  it('marks a manually changed renewal date as an explicit schedule change', async () => {
    const view = await openEditorThenRollMonth()
    await view.get('#account-billing-edit input[type="date"]').setValue('2027-03-15')

    expect(await submit()).toMatchObject({
      renewal_date: '2027-03-15',
      renewal_date_changed: true,
    })
  })

  it('sends an explicit null renewal date when the administrator clears it', async () => {
    const view = await openEditorThenRollMonth()
    await view.get('#account-billing-edit input[type="date"]').setValue('')

    expect(await submit()).toMatchObject({
      renewal_date: null,
      renewal_date_changed: true,
    })
  })
})
