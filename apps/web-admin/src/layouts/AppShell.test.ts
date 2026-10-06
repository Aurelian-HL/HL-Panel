import { mount } from '@vue/test-utils'
import { ref } from 'vue'
import { createMemoryHistory, createRouter } from 'vue-router'
import { describe, expect, it, vi } from 'vitest'

vi.mock('@/stores/site', () => ({
  siteStore: {
    siteName: ref('HL-panel'),
    panelTitle: ref('HL-panel'),
    load: vi.fn().mockResolvedValue(undefined),
  },
}))

import AppShell from './AppShell.vue'

vi.mock('@/api/releases', async (original) => ({
  ...await original<typeof import('@/api/releases')>(),
  checkVersion: vi.fn().mockResolvedValue({ state: 'up_to_date', current_version: 'v0.1.5' }),
}))

describe('AppShell navigation', () => {
  it('opens only the probe in a new tab without changing the panel route', async () => {
    const router = createRouter({
      history: createMemoryHistory(),
      routes: [
        { path: '/overview', component: { template: '<div>Overview</div>' } },
        { path: '/probe', component: { template: '<div>Probe</div>' } },
      ],
    })
    await router.push('/overview')
    await router.isReady()
    const wrapper = mount(AppShell, { global: { plugins: [router] } })

    const probeLink = wrapper.get('a[href="/probe"]')
    expect(probeLink.attributes('target')).toBe('_blank')
    expect(probeLink.attributes('rel')).toBe('opener')
    expect(wrapper.get('a[href="/forward-rules"]').attributes('target')).toBeUndefined()

    await wrapper.get('.mobile-nav-toggle').trigger('click')
    expect(wrapper.get('.sidebar').classes()).toContain('sidebar--open')
    probeLink.element.addEventListener('click', (event) => event.preventDefault(), { once: true })
    await probeLink.trigger('click')
    expect(router.currentRoute.value.path).toBe('/overview')
    expect(wrapper.get('.sidebar').classes()).not.toContain('sidebar--open')
    wrapper.unmount()
  })
})
