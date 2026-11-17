import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { defineComponent } from 'vue'
import { mount } from '@vue/test-utils'
import { useResourceOwners } from '../useResourceOwners'
import type { ResourceOwner } from '@/api/resourceOwnership'

const { listResourceOwners } = vi.hoisted(() => ({ listResourceOwners: vi.fn() }))
vi.mock('@/api/resourceOwnership', () => ({ listResourceOwners }))

const alice: ResourceOwner = { resource_id: 1, user_id: 7, email: 'alice@example.test', username: 'Alice' }
const unassigned: ResourceOwner = { resource_id: 2, user_id: null, email: '', username: '' }
const mounted: ReturnType<typeof mount>[] = []

function setup() {
  let state!: ReturnType<typeof useResourceOwners>
  mounted.push(mount(defineComponent({
    setup() { state = useResourceOwners('groups'); return () => null }
  })))
  return state
}

describe('ownership batches for visible resources', () => {
  beforeEach(() => { listResourceOwners.mockReset() })
  afterEach(() => mounted.splice(0).forEach(wrapper => wrapper.unmount()))

  it('deduplicates the visible ids into one request and preserves explicit unassigned ownership', async () => {
    listResourceOwners.mockResolvedValue([alice, unassigned])
    const state = setup()
    await state.load([1, 2, 1])

    expect(listResourceOwners).toHaveBeenCalledTimes(1)
    expect(listResourceOwners).toHaveBeenCalledWith('groups', [1, 2], expect.any(AbortSignal))
    expect(state.owners.value.get(1)).toEqual(alice)
    expect(state.owners.value.get(2)?.user_id).toBeNull()
    expect(state.loading.value).toBe(false)
    expect(state.error.value).toBe(false)
  })

  it('clears an empty page without making a request', async () => {
    listResourceOwners.mockResolvedValue([alice])
    const state = setup()
    await state.load([1])
    await state.load([])

    expect(listResourceOwners).toHaveBeenCalledTimes(1)
    expect(state.owners.value.size).toBe(0)
    expect(state.loading.value).toBe(false)
  })

  it('aborts an older page and ignores its late response', async () => {
    let finishOld!: (owners: ResourceOwner[]) => void
    listResourceOwners.mockImplementationOnce(() => new Promise(resolve => { finishOld = resolve }))
    listResourceOwners.mockResolvedValueOnce([unassigned])
    const state = setup()
    const older = state.load([1])
    const oldSignal = listResourceOwners.mock.calls[0]![2] as AbortSignal
    await state.load([2])
    finishOld([alice])
    await older

    expect(oldSignal.aborted).toBe(true)
    expect([...state.owners.value.values()]).toEqual([unassigned])
  })

  it('exposes lookup failures and recovers on the next page load', async () => {
    listResourceOwners.mockRejectedValueOnce(new Error('lookup failed'))
    listResourceOwners.mockResolvedValueOnce([alice])
    const state = setup()
    await state.load([1])
    expect(state.error.value).toBe(true)
    expect(state.owners.value.has(1)).toBe(false)

    await state.load([1])
    expect(state.error.value).toBe(false)
    expect(state.owners.value.get(1)).toEqual(alice)
  })

  it('cancels pending lookups when leaving the page', async () => {
    listResourceOwners.mockReturnValue(new Promise(() => {}))
    const state = setup()
    void state.load([1])
    const signal = listResourceOwners.mock.calls[0]![2] as AbortSignal
    mounted[0]!.unmount()
    expect(signal.aborted).toBe(true)
  })
})
