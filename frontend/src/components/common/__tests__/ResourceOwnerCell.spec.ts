import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'
import ResourceOwnerCell from '../ResourceOwnerCell.vue'
import Select from '../Select.vue'
import type { ResourceOwner } from '@/api/resourceOwnership'

const { listUsers, updateResourceOwner } = vi.hoisted(() => ({ listUsers: vi.fn(), updateResourceOwner: vi.fn() }))
vi.mock('@/api/admin/users', () => ({ list: listUsers }))
vi.mock('@/api/resourceOwnership', () => ({ updateResourceOwner }))
vi.mock('vue-i18n', async () => ({
  ...await vi.importActual<typeof import('vue-i18n')>('vue-i18n'),
  useI18n: () => ({ t: (key: string) => key })
}))

const alice: ResourceOwner = { resource_id: 42, user_id: 7, email: 'alice@example.test', username: 'Alice' }
const bob: ResourceOwner = { resource_id: 42, user_id: 9, email: 'bob@example.test', username: 'Bob' }
const mounted: ReturnType<typeof mount>[] = []

function mountCell(props: Partial<InstanceType<typeof ResourceOwnerCell>['$props']> = {}) {
  const wrapper = mount(ResourceOwnerCell, {
    props: { kind: 'accounts', resourceId: 42, owner: alice, ...props },
    global: {
      stubs: {
        BaseDialog: { props: ['show'], template: '<div v-if="show" role="dialog"><slot /><slot name="footer" /></div>' },
        Select: { props: { modelValue: [String, Number, Boolean], options: Array, remote: Boolean, loading: Boolean }, emits: ['update:modelValue', 'search', 'change'], template: '<div data-test="user-select" />' },
        Icon: true
      }
    }
  })
  mounted.push(wrapper)
  return wrapper
}

async function open(wrapper: ReturnType<typeof mountCell>) {
  await wrapper.get('[aria-label="common.resourceOwnership.edit"]').trigger('click')
  await flushPromises()
}

async function selectUser(wrapper: ReturnType<typeof mountCell>, id: number | null) {
  const select = wrapper.getComponent(Select)
  select.vm.$emit('update:modelValue', id)
  select.vm.$emit('change', id, id === null ? null : { value: id, label: `Selected #${id}` })
  await flushPromises()
}

describe('resource ownership display and transfer', () => {
  beforeEach(() => {
    listUsers.mockReset().mockResolvedValue({ items: [{ id: 9, email: bob.email, username: bob.username }], total: 1 })
    updateResourceOwner.mockReset().mockResolvedValue(bob)
  })
  afterEach(() => mounted.splice(0).forEach(wrapper => wrapper.unmount()))

  it('shows the owner and never opens search or transfer controls for ordinary users', async () => {
    const wrapper = mountCell()
    expect(wrapper.text()).toContain('Alice')
    expect(wrapper.text()).toContain('alice@example.test')
    expect(wrapper.text()).toContain('#7')
    expect(wrapper.find('button').exists()).toBe(false)
    expect(wrapper.find('[role="dialog"]').exists()).toBe(false)
    expect(listUsers).not.toHaveBeenCalled()
    expect(updateResourceOwner).not.toHaveBeenCalled()
  })

  it('distinguishes unassigned resources from loading and failed lookups', () => {
    expect(mountCell({ owner: { ...alice, user_id: null } }).text()).toContain('common.resourceOwnership.unassigned')
    expect(mountCell({ owner: undefined, loading: true }).text()).toContain('common.loading')
    const failed = mountCell({ owner: undefined, error: true })
    expect(failed.text()).toContain('common.resourceOwnership.loadFailed')
    expect(failed.text()).not.toContain('common.resourceOwnership.unassigned')
    expect(failed.find('button').exists()).toBe(false)
  })

  it('searches active users remotely in bounded pages and transfers ownership', async () => {
    const wrapper = mountCell({ editable: true })
    expect(listUsers).not.toHaveBeenCalled()
    await open(wrapper)
    const select = wrapper.getComponent(Select)
    expect(select.props('remote')).toBe(true)
    expect(wrapper.text()).toContain('common.resourceOwnership.transferHint')
    select.vm.$emit('search', ' bob ')
    await flushPromises()
    expect(listUsers).toHaveBeenLastCalledWith(1, 20, {
      status: 'active', search: 'bob', include_subscriptions: false
    }, { signal: expect.any(AbortSignal) })

    await selectUser(wrapper, 9)
    await wrapper.get('.btn-primary').trigger('click')
    await flushPromises()

    expect(updateResourceOwner).toHaveBeenCalledWith('accounts', 42, 9)
    expect(wrapper.emitted('updated')).toEqual([[bob]])
    expect(wrapper.find('[role="dialog"]').exists()).toBe(false)
  })

  it('lets admins clear ownership explicitly', async () => {
    const unassigned = { ...alice, user_id: null, username: '', email: '' }
    updateResourceOwner.mockResolvedValueOnce(unassigned)
    const wrapper = mountCell({ editable: true, kind: 'groups' })
    await open(wrapper)
    await selectUser(wrapper, null)
    await wrapper.get('.btn-primary').trigger('click')
    await flushPromises()

    expect(updateResourceOwner).toHaveBeenCalledWith('groups', 42, null)
    expect(wrapper.emitted('updated')).toEqual([[unassigned]])
  })

  it('retains the selected user label when a later remote search has different results', async () => {
    const wrapper = mountCell({ editable: true })
    await open(wrapper)
    await selectUser(wrapper, 9)
    listUsers.mockResolvedValueOnce({ items: [{ id: 10, email: 'charlie@example.test', username: 'Charlie' }], total: 1 })
    wrapper.getComponent(Select).vm.$emit('search', 'charlie')
    await flushPromises()

    expect(wrapper.getComponent(Select).props('options')).toEqual(expect.arrayContaining([
      expect.objectContaining({ value: 9, label: 'Selected #9' }),
      expect.objectContaining({ value: 10 }),
    ]))
  })

  it('keeps the dialog open with the actionable backend message when transfer conflicts', async () => {
    updateResourceOwner.mockRejectedValueOnce({ status: 409, message: 'Remove the fallback group reference before transferring.' })
    const wrapper = mountCell({ editable: true })
    await open(wrapper)
    await selectUser(wrapper, 9)
    await wrapper.get('.btn-primary').trigger('click')
    await flushPromises()

    expect(wrapper.get('[role="dialog"]').text()).toContain('Remove the fallback group reference before transferring.')
    expect(wrapper.emitted('updated')).toBeUndefined()
    expect(wrapper.get('.btn-primary').attributes('disabled')).toBeUndefined()
  })

  it('shows search failures and cancels an older query when a new search starts', async () => {
    const wrapper = mountCell({ editable: true })
    await open(wrapper)
    const initialSignal = listUsers.mock.calls[0]![3].signal as AbortSignal
    listUsers.mockRejectedValueOnce(new Error('Search unavailable'))
    wrapper.getComponent(Select).vm.$emit('search', 'bob')
    await flushPromises()

    expect(initialSignal.aborted).toBe(true)
    expect(wrapper.get('[role="alert"]').text()).toContain('Search unavailable')
    expect(updateResourceOwner).not.toHaveBeenCalled()
  })

  it('closes a previously opened editor if admin permissions are removed', async () => {
    const wrapper = mountCell({ editable: true })
    await open(wrapper)
    const signal = listUsers.mock.calls[0]![3].signal as AbortSignal
    await wrapper.setProps({ editable: false })

    expect(wrapper.find('button').exists()).toBe(false)
    expect(wrapper.find('[role="dialog"]').exists()).toBe(false)
    expect(signal.aborted).toBe(true)
    expect(updateResourceOwner).not.toHaveBeenCalled()
  })
})
