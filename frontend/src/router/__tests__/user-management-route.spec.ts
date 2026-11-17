import { afterEach, describe, expect, it, vi } from 'vitest'

vi.mock('@/stores/auth', () => ({ useAuthStore: () => ({ checkAuth: vi.fn(), isAuthenticated: false, isAdmin: false, isSimpleMode: false }) }))
vi.mock('@/stores/app', () => ({ useAppStore: () => ({ siteName: 'Sub2API', backendModeEnabled: false, cachedPublicSettings: null }) }))
vi.mock('@/stores/adminSettings', () => ({ useAdminSettingsStore: () => ({ customMenuItems: [] }) }))
vi.mock('@/stores/adminCompliance', () => ({ useAdminComplianceStore: () => ({}) }))
vi.mock('@/composables/useNavigationLoading', () => ({ useNavigationLoadingState: () => ({ startNavigation: vi.fn(), endNavigation: vi.fn() }) }))
vi.mock('@/composables/useRoutePrefetch', () => ({ useRoutePrefetch: () => ({ triggerPrefetch: vi.fn() }) }))
vi.mock('@/views/auth/LoginView.vue', () => ({ default: { template: '<div>Login</div>' } }))
vi.mock('@/router/title', () => ({ resolveRouteDocumentTitle: () => 'Management' }))

afterEach(() => { vi.restoreAllMocks() })

describe('user resource management routes', () => {
  it.each(['/groups', '/accounts'])('requires login without requiring the admin role for %s', async path => {
    vi.spyOn(window, 'scrollTo').mockImplementation(() => {})
    const { default: router } = await import('@/router')
    const record = router.getRoutes().find(route => route.path === path)
    expect(record?.meta.requiresAuth).toBe(true)
    expect(record?.meta.requiresAdmin).toBe(false)

    await router.push(path)
    expect(router.currentRoute.value.path).toBe('/login')
    expect(router.currentRoute.value.query.redirect).toBe(path)
  })

  it('retains the administrator requirement on the original management routes', async () => {
    const { default: router } = await import('@/router')
    for (const path of ['/admin/groups', '/admin/accounts']) {
      expect(router.getRoutes().find(route => route.path === path)?.meta.requiresAdmin).toBe(true)
    }
  })
})
