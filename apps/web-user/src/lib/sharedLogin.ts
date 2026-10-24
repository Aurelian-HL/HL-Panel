export function sharedLoginURL(query: Record<string, unknown>): string {
  let destination = '/account/'
  const redirect = query.redirect
  if (typeof redirect === 'string' && redirect.startsWith('/') && !redirect.startsWith('//') && !/[\\\u0000-\u0020]/.test(redirect)) {
    try {
      const url = new URL(redirect, window.location.origin)
      if (url.origin === window.location.origin && ['/', '/userinfo', '/forward_rules', '/forward-rules'].includes(url.pathname)) {
        destination = '/account' + url.pathname + url.search + url.hash
      }
    } catch { /* Invalid redirects return to the account home page. */ }
  }
  const params = new URLSearchParams({ redirect: destination })
  if (query.reason === 'expired') params.set('reason', 'expired')
  return '/login?' + params.toString()
}
