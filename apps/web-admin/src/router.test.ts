import { describe, expect, it } from 'vitest'

import { router } from './router'

describe('admin routes', () => {
  it('opens the probe in its own workspace outside the panel shell', () => {
    expect(router.resolve('/probe').matched.map((record) => record.path)).toEqual(['/probe', '/probe'])
    expect(router.resolve('/overview').matched[0]?.path).toBe('/')
  })
  it('redirects the obsolete endpoint page to the rule workflow', () => {
    expect(router.resolve('/endpoint-pools').matched.at(-1)?.redirect).toBe('/forward-rules')
  })
})
