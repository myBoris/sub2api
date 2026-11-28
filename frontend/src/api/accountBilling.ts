import { apiClient } from './client'

export type BillingPaymentStatus = 'paid' | 'unpaid'

export interface BillingPlan {
  id: number
  platform: string
  name: string
  price: number
  cost: number
}

export interface BillingPayer {
  id: number
  name: string
}

export interface BillingHistory {
  month: string
  status: BillingPaymentStatus
  amount: number
  cost: number
}

export interface BillingAccount {
  account_id: number
  account_name: string
  platform: string
  renewal_date: string | null
  valid_through?: string | null
  expired?: boolean
  plan_id: number | null
  plan_name: string | null
  price: number
  cost: number
  payer_id: number | null
  payer_name: string | null
  payment_status: BillingPaymentStatus
  history: BillingHistory[]
}

export interface BillingCatalog {
  plans: BillingPlan[]
  payers: BillingPayer[]
}

export interface BillingAccountList {
  items: BillingAccount[]
  month: string
  summary: {
    paid_count: number
    paid_total_amount: number
    paid_total_cost: number
    paid_profit: number
    unpaid_count: number
    due_soon_count: number
    payers: Array<{
      payer_id: number | null
      payer_name: string
      total: number
    }>
  }
}

export interface BillingAccountFilters {
  search?: string
  payer_id?: number | string
  payment_status?: BillingPaymentStatus | ''
  month?: string
}

export interface BillingAccountUpdate {
  renewal_date: string | null
  renewal_date_changed?: boolean
  plan_id: number | null
  price: number
  cost?: number
  payer_id: number | null
  payment_status: BillingPaymentStatus
}

export interface UserBillingSummary {
  month: string
  amount_due: number
  total_amount: number
  accounts: Array<
    Pick<BillingAccount, 'account_id' | 'renewal_date' | 'valid_through' | 'expired' | 'price' | 'payment_status'>
  >
}

const base = '/admin/account-billing'

export async function getCatalog(): Promise<BillingCatalog> {
  return (await apiClient.get<BillingCatalog>(`${base}/catalog`)).data
}

export async function createPlan(
  plan: Omit<BillingPlan, 'id'>,
): Promise<BillingPlan> {
  return (await apiClient.post<BillingPlan>(`${base}/plans`, plan)).data
}

export async function updatePlan(
  id: number,
  plan: Omit<BillingPlan, 'id'>,
): Promise<BillingPlan> {
  return (await apiClient.put<BillingPlan>(`${base}/plans/${id}`, plan)).data
}

export async function deletePlan(id: number): Promise<void> {
  await apiClient.delete(`${base}/plans/${id}`)
}

export async function createPayer(
  payer: Omit<BillingPayer, 'id'>,
): Promise<BillingPayer> {
  return (await apiClient.post<BillingPayer>(`${base}/payers`, payer)).data
}

export async function updatePayer(
  id: number,
  payer: Omit<BillingPayer, 'id'>,
): Promise<BillingPayer> {
  return (await apiClient.put<BillingPayer>(`${base}/payers/${id}`, payer)).data
}

export async function deletePayer(id: number): Promise<void> {
  await apiClient.delete(`${base}/payers/${id}`)
}

export async function listBillingAccounts(
  filters?: BillingAccountFilters,
): Promise<BillingAccountList> {
  return (
    await apiClient.get<BillingAccountList>(`${base}/accounts`, {
      params: filters,
    })
  ).data
}

export async function syncBillingAccounts(): Promise<{
  synced?: number
  created?: number
  message?: string
}> {
  return (await apiClient.post(`${base}/accounts/sync`)).data
}

export async function updateBillingAccount(
  id: number,
  account: BillingAccountUpdate,
): Promise<BillingAccount> {
  return (
    await apiClient.put<BillingAccount>(`${base}/accounts/${id}`, account)
  ).data
}

export async function updateBillingHistory(
  id: number,
  month: string,
  record: Pick<BillingHistory, 'status' | 'amount'> & Partial<Pick<BillingHistory, 'cost'>>,
): Promise<BillingAccount> {
  return (
    await apiClient.put<BillingAccount>(
      `${base}/accounts/${id}/history/${month}`,
      record,
    )
  ).data
}

export async function getUserBillingSummary(): Promise<UserBillingSummary> {
  return (await apiClient.get<UserBillingSummary>('/account-billing/summary')).data
}
