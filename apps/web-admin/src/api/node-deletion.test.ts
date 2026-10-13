import { afterEach, expect, it, vi } from 'vitest'
import { api } from './index'

afterEach(() => vi.unstubAllGlobals())

it('uses the guarded scope for online orphan cleanup and preserves the legacy offline endpoint', async () => {
  const fetch = vi.fn().mockResolvedValue(new Response(JSON.stringify({ node_id: 'node/1', replayed: false }), { status: 200, headers: { 'Content-Type': 'application/json' } }))
  vi.stubGlobal('fetch', fetch)
  await api.deleteNode('node/1', 'cleanup-key', 'unassigned')
  expect(fetch.mock.calls[0]![0]).toBe('/api/v1/nodes/node%2F1?scope=unassigned')
  expect(fetch.mock.calls[0]![1]).toMatchObject({ method: 'DELETE' })
  fetch.mockResolvedValue(new Response(JSON.stringify({ node_id: 'node/1', replayed: true }), { status: 200, headers: { 'Content-Type': 'application/json' } }))
  await api.deleteNode('node/1', 'offline-key')
  expect(fetch.mock.calls[1]![0]).toBe('/api/v1/nodes/node%2F1')
})
