import { onUnmounted, ref, shallowRef } from 'vue'
import { listResourceOwners, type ResourceKind, type ResourceOwner } from '@/api/resourceOwnership'

/** One ownership lookup for each visible page, independent of the upstream list payloads. */
export function useResourceOwners(kind: ResourceKind) {
  const owners = shallowRef(new Map<number, ResourceOwner>())
  const loading = ref(false)
  const error = ref(false)
  let controller: AbortController | undefined

  async function load(ids: number[]) {
    controller?.abort()
    const current = new AbortController()
    controller = current
    const uniqueIds = [...new Set(ids)]
    owners.value = new Map(uniqueIds.flatMap(id => {
      const owner = owners.value.get(id)
      return owner ? [[id, owner] as const] : []
    }))
    error.value = false
    loading.value = uniqueIds.length > 0
    if (uniqueIds.length === 0) return

    try {
      const result = await listResourceOwners(kind, uniqueIds, current.signal)
      if (!current.signal.aborted) {
        owners.value = new Map(result.map(owner => [owner.resource_id, owner]))
      }
    } catch {
      if (!current.signal.aborted) error.value = true
    } finally {
      if (controller === current) loading.value = false
    }
  }

  function setOwner(owner: ResourceOwner) {
    owners.value = new Map(owners.value).set(owner.resource_id, owner)
  }

  onUnmounted(() => controller?.abort())
  return { owners, loading, error, load, setOwner }
}
