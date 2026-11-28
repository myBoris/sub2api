import { apiClient } from './client'

export interface SubscriptionRequestPlan {
  id: number
  platform: string
  name: string
  price: number
}

export type SubscriptionRequestStatus = 'pending' | 'completed'

export interface SubscriptionRequest {
  id: number
  plan_id: number
  platform: string
  plan_name: string
  price: number
  status: SubscriptionRequestStatus
  created_at: string
  updated_at: string
  completed_at: string | null
}

export interface AdminSubscriptionRequest extends SubscriptionRequest {
  user_id: number
  user_name: string
  user_email: string
  cost: number
  completed_by: number | null
}

const userBase = '/account-billing'
const adminBase = '/admin/account-billing/subscription-requests'

export async function getSubscriptionRequestPlans(): Promise<{ plans: SubscriptionRequestPlan[] }> {
  return (await apiClient.get<{ plans: SubscriptionRequestPlan[] }>(`${userBase}/subscription-plans`)).data
}

export async function getSubscriptionRequests(): Promise<{ items: SubscriptionRequest[] }> {
  return (await apiClient.get<{ items: SubscriptionRequest[] }>(`${userBase}/subscription-requests`)).data
}

export async function createSubscriptionRequest(planId: number): Promise<SubscriptionRequest> {
  return (await apiClient.post<SubscriptionRequest>(`${userBase}/subscription-requests`, { plan_id: planId })).data
}

export async function updateSubscriptionRequest(id: number, planId: number): Promise<SubscriptionRequest> {
  return (await apiClient.put<SubscriptionRequest>(`${userBase}/subscription-requests/${id}`, { plan_id: planId })).data
}

export async function listAdminSubscriptionRequests(filters?: { search?: string; status?: SubscriptionRequestStatus | '' }): Promise<{ items: AdminSubscriptionRequest[] }> {
  return (await apiClient.get<{ items: AdminSubscriptionRequest[] }>(adminBase, { params: filters })).data
}

export async function completeSubscriptionRequest(id: number, updatedAt: string): Promise<AdminSubscriptionRequest> {
  return (await apiClient.post<AdminSubscriptionRequest>(`${adminBase}/${id}/complete`, { updated_at: updatedAt })).data
}
