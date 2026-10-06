import { describe, expect, it } from 'vitest'

import { router } from './router'

describe('customer routes', () => {
  it('uses the NY forwarding path and keeps the prior path as an alias', () => {
    expect(router.resolve('/forward_rules').name).toBe('forward-rules')
    expect(router.resolve('/forward-rules').name).toBe('forward-rules')
    expect(router.resolve('/userinfo').name).toBe('userinfo')
    expect(router.resolve('/').name).toBe('home')
  })

  it('does not expose unfinished tunnel or LookingGlass placeholders', () => {
    expect(router.resolve('/device_group').matched[0]?.path).toBe('/:pathMatch(.*)*')
    expect(router.resolve('/looking_glass').matched[0]?.path).toBe('/:pathMatch(.*)*')
  })
})
