import { apiClient } from './client'

export type ResourceKind = 'groups' | 'accounts'

export interface ResourceOwner {
  resource_id: number
  user_id: number | null
  email: string
  username: string
}

export async function listResourceOwners(
  kind: ResourceKind,
  ids: number[],
  signal?: AbortSignal
): Promise<ResourceOwner[]> {
  const { data } = await apiClient.get<ResourceOwner[]>(`/admin/resource-ownership/${kind}`, {
    params: { ids: ids.join(',') },
    signal
  })
  return data
}

export async function updateResourceOwner(
  kind: ResourceKind,
  id: number,
  userId: number | null
): Promise<ResourceOwner> {
  const { data } = await apiClient.put<ResourceOwner>(`/admin/resource-ownership/${kind}/${id}`, {
    user_id: userId
  })
  return data
}
