/** Keep the shared management views on the user's ownership-scoped API. */
export function resolveUserManagementURL(url: string, role: string | undefined): string {
  if (role !== 'user') return url
  return url.replace(
    /^((?:\/api\/v1)?)\/admin\/(groups|accounts|resource-ownership|openai|gemini|antigravity|grok|cn-providers)(?=\/|\?|$)/,
    '$1/user/managed/$2'
  )
}

export function isUserManagementSession(): boolean {
  try {
    return JSON.parse(localStorage.getItem('auth_user') || 'null')?.role === 'user'
  } catch {
    return false
  }
}
