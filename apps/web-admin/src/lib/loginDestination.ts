function internalURL(value: unknown): URL | null {
  if (typeof value !== 'string' || !value.startsWith('/') || value.startsWith('//') || /[\\\u0000-\u0020]/.test(value)) return null
  try {
    const url = new URL(value, window.location.origin)
    return url.origin === window.location.origin ? url : null
  } catch {
    return null
  }
}

export function accountLoginDestination(value: unknown): string | null {
  const url = internalURL(value)
  if (!url || !['/account/', '/account/userinfo', '/account/forward_rules', '/account/forward-rules'].includes(url.pathname)) return null
  return url.pathname + url.search + url.hash
}

export function administratorLoginDestination(value: unknown): string {
  const url = internalURL(value)
  if (!url || url.pathname === '/login' || url.pathname === '/account' || url.pathname.startsWith('/account/')) return '/overview'
  return url.pathname + url.search + url.hash
}
