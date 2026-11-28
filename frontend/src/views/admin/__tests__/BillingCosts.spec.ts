import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'
import type { Component } from 'vue'
import AccountBillingView from '../AccountBillingView.vue'
import BillingCatalogView from '../BillingCatalogView.vue'
import type { BillingAccountList, BillingCatalog } from '@/api/accountBilling'

const mocks = vi.hoisted(() => ({
  getCatalog: vi.fn(),
  listBillingAccounts: vi.fn(),
  updateBillingAccount: vi.fn(),
  updateBillingHistory: vi.fn(),
  createPlan: vi.fn(),
  updatePlan: vi.fn(),
  showSuccess: vi.fn(),
  showError: vi.fn(),
}))

vi.mock('@/api/accountBilling', () => ({
  ...mocks,
  syncBillingAccounts: vi.fn(),
  deletePlan: vi.fn(),
  createPayer: vi.fn(),
  updatePayer: vi.fn(),
  deletePayer: vi.fn(),
}))
vi.mock('@/stores/app', () => ({
  useAppStore: () => ({ showSuccess: mocks.showSuccess, showError: mocks.showError }),
}))

function mountView(component: Component) {
  return mount(component, {
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
}

let wrapper: ReturnType<typeof mountView> | undefined
let catalog: BillingCatalog
let ledger: BillingAccountList

beforeEach(() => {
  vi.resetAllMocks()
  catalog = {
    plans: [{ id: 5, platform: 'OpenAI', name: 'Plus', price: 150, cost: 75.5 }],
    payers: [{ id: 7, name: '代付甲' }],
  }
  ledger = {
    month: '2026-11',
    items: [{
      account_id: 42,
      account_name: '测试账号',
      platform: 'OpenAI',
      renewal_date: '2026-11-30',
      plan_id: null,
      plan_name: null,
      price: 100,
      cost: 60,
      payer_id: 7,
      payer_name: '代付甲',
      payment_status: 'paid',
      history: [
        { month: '2026-11', status: 'paid', amount: 90, cost: 65 },
        { month: '2026-10', status: 'paid', amount: 80, cost: 50 },
      ],
    }],
    summary: {
      paid_count: 1,
      paid_total_amount: 90,
      paid_total_cost: 65,
      paid_profit: 25,
      unpaid_count: 0,
      due_soon_count: 0,
      payers: [{ payer_id: 7, payer_name: '代付甲', total: 65 }],
    },
  }
  mocks.getCatalog.mockResolvedValue(catalog)
  mocks.listBillingAccounts.mockResolvedValue(ledger)
  mocks.updateBillingAccount.mockResolvedValue(ledger.items[0])
  mocks.updateBillingHistory.mockResolvedValue(ledger.items[0])
})

afterEach(() => {
  wrapper?.unmount()
  wrapper = undefined
})

async function click(text: string) {
  const button = wrapper!.findAll('button').find(button => button.text() === text)
  expect(button).toBeDefined()
  await button!.trigger('click')
}

describe('billing costs', () => {
  it('shows monthly paid snapshots and payer costs independently of table filters', async () => {
    wrapper = mountView(AccountBillingView)
    await flushPromises()
    const summary = wrapper.get('[data-testid="paid-summary"]')
    expect(summary.findAll('dd').map(value => value.text()))
      .toEqual(['1', '¥90.00', '¥65.00', '¥25.00'])
    expect(wrapper.text()).toContain('代付成本')
    expect(wrapper.get('[data-testid="payer-cost-summary"]').text()).toContain('¥65.00')
    await wrapper.get('[aria-label="按支付状态筛选"]').setValue('unpaid')
    expect(wrapper.get('tbody').text()).toContain('没有匹配的账号')
    expect(summary.findAll('dd').map(value => value.text()))
      .toEqual(['1', '¥90.00', '¥65.00', '¥25.00'])
    expect(wrapper.text()).not.toContain('同步仅添加尚未登记的账号')
    expect(wrapper.text()).not.toContain('API 按实际有效截止日期判断')
  })

  it('shows negative profit and zero totals without clamping them', async () => {
    Object.assign(ledger.summary, { paid_total_amount: 0, paid_total_cost: 65, paid_profit: -65 })
    wrapper = mountView(AccountBillingView)
    await flushPromises()
    const values = wrapper.get('[data-testid="paid-summary"]').findAll('dd')
    expect(values[1]!.text()).toBe('¥0.00')
    expect(values[3]!.text()).toBe('¥-65.00')
    expect(values[3]!.classes()).toContain('text-red-600')
  })

  it('copies both plan prices into an account and saves the chosen cost', async () => {
    wrapper = mountView(AccountBillingView)
    await flushPromises()
    await click('编辑')
    await wrapper.get('#account-billing-edit select').setValue(5)
    const inputs = wrapper.findAll('#account-billing-edit input[type="number"]')
    expect((inputs[0]!.element as HTMLInputElement).value).toBe('150')
    expect((inputs[1]!.element as HTMLInputElement).value).toBe('75.5')
    await inputs[1]!.setValue('70.25')
    await wrapper.get('#account-billing-edit').trigger('submit')
    await flushPromises()
    expect(mocks.updateBillingAccount).toHaveBeenCalledWith(42, expect.objectContaining({
      plan_id: 5, price: 150, cost: 70.25,
    }))
  })

  it('allows a zero account cost and rejects a negative cost', async () => {
    wrapper = mountView(AccountBillingView)
    await flushPromises()
    await click('编辑')
    const cost = wrapper.findAll('#account-billing-edit input[type="number"]')[1]!
    await cost.setValue('-1')
    await wrapper.get('#account-billing-edit').trigger('submit')
    expect(mocks.updateBillingAccount).not.toHaveBeenCalled()
    await cost.setValue('0')
    await wrapper.get('#account-billing-edit').trigger('submit')
    await flushPromises()
    expect(mocks.updateBillingAccount).toHaveBeenCalledWith(42, expect.objectContaining({ cost: 0 }))
  })

  it('edits historical amounts and costs using their month snapshots', async () => {
    wrapper = mountView(AccountBillingView)
    await flushPromises()
    await click('支付记录')
    const historyForm = wrapper.get('form')
    let inputs = historyForm.findAll('input[type="number"]')
    expect((inputs[0]!.element as HTMLInputElement).value).toBe('90')
    expect((inputs[1]!.element as HTMLInputElement).value).toBe('65')
    await historyForm.get('input[type="month"]').setValue('2026-10')
    inputs = historyForm.findAll('input[type="number"]')
    expect((inputs[0]!.element as HTMLInputElement).value).toBe('80')
    expect((inputs[1]!.element as HTMLInputElement).value).toBe('50')
    await inputs[1]!.setValue('52.25')
    await historyForm.trigger('submit')
    await flushPromises()
    expect(mocks.updateBillingHistory).toHaveBeenCalledWith(42, '2026-10', {
      status: 'paid', amount: 80, cost: 52.25,
    })
  })

  it('creates plans with a separate CNY cost', async () => {
    wrapper = mountView(BillingCatalogView)
    await flushPromises()
    expect(wrapper.get('tbody').text()).toContain('¥75.50')
    await click('新增套餐')
    const form = wrapper.get('#billing-catalog-form')
    const inputs = form.findAll('input')
    await inputs[0]!.setValue('Claude')
    await inputs[1]!.setValue('Pro')
    await inputs[2]!.setValue('150')
    await inputs[3]!.setValue('98.25')
    await form.trigger('submit')
    await flushPromises()
    expect(mocks.createPlan).toHaveBeenCalledWith({ platform: 'Claude', name: 'Pro', price: 150, cost: 98.25 })
  })

  it('preserves an existing plan cost when editing and permits a zero cost', async () => {
    wrapper = mountView(BillingCatalogView)
    await flushPromises()
    await click('编辑')
    const form = wrapper.get('#billing-catalog-form')
    const cost = form.findAll('input[type="number"]')[1]!
    expect((cost.element as HTMLInputElement).value).toBe('75.5')
    await cost.setValue('-1')
    await form.trigger('submit')
    expect(mocks.updatePlan).not.toHaveBeenCalled()
    await cost.setValue('0')
    await form.trigger('submit')
    await flushPromises()
    expect(mocks.updatePlan).toHaveBeenCalledWith(5, expect.objectContaining({ cost: 0, price: 150 }))
  })
})
