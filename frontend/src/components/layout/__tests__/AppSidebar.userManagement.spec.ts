import { defineComponent, ref } from 'vue'
import { shallowMount } from '@vue/test-utils'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import AppSidebar from '../AppSidebar.vue'

const { authState } = vi.hoisted(() => ({ authState: { isAdmin: false, isSimpleMode: false } }))

vi.mock('vue-i18n', async importOriginal => ({
  ...await importOriginal<typeof import('vue-i18n')>(),
  useI18n: () => ({ t: (key: string) => key })
}))
vi.mock('vue-router', () => ({ useRoute: () => ({ path: '/dashboard' }), useRouter: () => ({ push: vi.fn() }) }))
vi.mock('@/stores', () => ({
  useAuthStore: () => authState,
  useAppStore: () => ({ cachedPublicSettings: {}, sidebarScrollTop: 0, backendModeEnabled: false }),
  useAdminSettingsStore: () => ({ customMenuItems: [], fetch: vi.fn() }),
  useOnboardingStore: () => ({ isCurrentStep: () => false })
}))
vi.mock('@/utils/featureFlags', () => ({
  FeatureFlags: {}, makeSidebarFlag: () => () => false, resolveFeatureFlag: () => false
}))
vi.mock('@/composables/useBatchImageAccess', () => ({
  useBatchImageAccess: () => ({ canUseBatchImage: ref(false), refreshBatchImageAccess: vi.fn() })
}))

const RouterLinkStub = defineComponent({ props: ['to'], template: '<a :href="to"><slot /></a>' })
const mountSidebar = () => shallowMount(AppSidebar, { global: { stubs: { RouterLink: RouterLinkStub } } })

beforeEach(() => {
  authState.isAdmin = false
  authState.isSimpleMode = false
})

describe('ownership management navigation', () => {
  it.each([false, true])('shows user management pages with simple mode set to %s', simple => {
    authState.isSimpleMode = simple
    const wrapper = mountSidebar()
    expect(wrapper.get('a[href="/groups"]').text()).toBe('nav.groups')
    expect(wrapper.get('a[href="/accounts"]').text()).toBe('nav.accounts')
    expect(wrapper.find('a[href="/admin/users"]').exists()).toBe(false)
    expect(wrapper.find('a[href="/admin/accounts"]').exists()).toBe(false)
    wrapper.unmount()
  })

  it('keeps administrators on their existing management pages', () => {
    authState.isAdmin = true
    const wrapper = mountSidebar()
    expect(wrapper.find('a[href="/admin/groups"]').exists()).toBe(true)
    expect(wrapper.find('a[href="/admin/accounts"]').exists()).toBe(true)
    expect(wrapper.find('a[href="/groups"]').exists()).toBe(false)
    expect(wrapper.find('a[href="/accounts"]').exists()).toBe(false)
    wrapper.unmount()
  })
})
