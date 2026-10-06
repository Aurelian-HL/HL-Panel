import { describe, expect, it, vi } from 'vitest'
import { businessApi, type ForwardRuleInput, type GroupNetworkInput } from './business'
import { useMutationKey } from '@/composables/useMutationKey'

describe('business API contracts', () => {
  it('writes complete versioned rules, carries authentication and unwraps the server envelope', async () => {
    sessionStorage.setItem('ny_admin_access_token', 'test-token')
    const fetchMock = vi.fn().mockResolvedValue(new Response(JSON.stringify({ rule: { id: 'rule-1', revision: 2 }, replayed: false }), { headers: { 'content-type': 'application/json' } }))
    vi.stubGlobal('fetch', fetchMock)
    const input: ForwardRuleInput = { name: 'test', customer_id: 'c-1', rule_group_id: '', entry_group_id: 'entry-1', exit_group_id: '', egress_mode: 'DIRECT', protocol: 'tcp', listen_port: 12000, targets: [{ host: '192.0.2.10', port: 443 }], selection_policy: 'round_robin', paused: true, description: '', revision: 1 }
    expect(await businessApi.saveRule(input, 'rule-1', 'operation-1')).toEqual({ id: 'rule-1', revision: 2 })
    const [url, options] = fetchMock.mock.calls[0] as [string, RequestInit]
    expect(url).toBe('/api/v1/forwarding-rules/rule-1')
    expect(options.method).toBe('PUT')
    expect(new Headers(options.headers).get('Idempotency-Key')).toBe('operation-1')
    expect(new Headers(options.headers).get('Authorization')).toBe('Bearer test-token')
    expect(JSON.parse(options.body as string)).toEqual(input)
  })

  it('uses the network resource endpoint and accepts an initial revision of zero', async () => {
    const fetchMock = vi.fn().mockResolvedValue(new Response(JSON.stringify({ network: { group_id: 'edge-1', revision: 1 }, replayed: false }), { headers: { 'content-type': 'application/json' } }))
    vi.stubGlobal('fetch', fetchMock)
    const input: GroupNetworkInput = { connect_host: 'edge.example.test', port_start: 10000, port_end: 20000, direct_policy: 'OPTIONAL', allow_direct: true, allowed_exit_group_ids: [], traffic_multiplier: 1, revision: 0 }
    expect(await businessApi.saveGroupNetwork(input, 'edge-1', 'network-key')).toEqual({ group_id: 'edge-1', revision: 1 })
    expect(fetchMock.mock.calls[0]?.[0]).toBe('/api/v1/group-networks/edge-1')
    expect((fetchMock.mock.calls[0]?.[1] as RequestInit).method).toBe('PUT')
  })

  it('preserves VLESS Reality + Vision ingress fields in the rule payload', async () => {
    const fetchMock = vi.fn().mockResolvedValue(new Response(JSON.stringify({ rule: { id: 'vless-1', revision: 1 }, replayed: false }), { headers: { 'content-type': 'application/json' } }))
    vi.stubGlobal('fetch', fetchMock)
    const input: ForwardRuleInput = { name: 'Reality', customer_id: 'c-1', rule_group_id: '', entry_group_id: 'entry-1', exit_group_id: '', egress_mode: 'DIRECT', ingress_protocol: 'vless_reality', vless_flow: 'xtls-rprx-vision', reality_server_name: 'www.example.com', reality_public_key: 'AbCdEf0123456789AbCdEf0123456789AbCdEf01234', reality_short_id: '0123456789abcdef', protocol: 'tcp', listen_port: 12001, targets: [{ host: '192.0.2.10', port: 443 }], selection_policy: 'round_robin', paused: false, description: '', revision: 0 }
    await businessApi.saveRule(input, null, 'vless-operation')
    expect(JSON.parse((fetchMock.mock.calls[0]?.[1] as RequestInit).body as string)).toMatchObject({ ingress_protocol: 'vless_reality', vless_flow: 'xtls-rprx-vision', reality_server_name: 'www.example.com', reality_short_id: '0123456789abcdef' })
  })

  it('rejects a failed revision instead of turning it into a saved object', async () => {
    vi.stubGlobal('fetch', vi.fn().mockResolvedValue(new Response(JSON.stringify({ error: { code: 'conflict', message: 'revision changed' } }), { status: 409, headers: { 'content-type': 'application/json' } })))
    await expect(businessApi.saveUserGroup({ name: 'users', description: '', allowed_entry_group_ids: [], allowed_exit_group_ids: [], allow_direct: false, revision: 1 }, 'ug-1', 'group-key')).rejects.toMatchObject({ status: 409, code: 'conflict' })
  })

  it('posts one versioned batch request and unwraps all updated rules', async () => {
    const fetchMock = vi.fn().mockResolvedValue(new Response(JSON.stringify({ result: { operation: 'pause', rule_group_id: '', items: [{ id: 'rule-1', revision: 4 }] }, replayed: false }), { headers: { 'content-type': 'application/json' } }))
    vi.stubGlobal('fetch', fetchMock)
    const input = { operation: 'pause' as const, rule_ids: ['rule-1'], rule_group_id: '', expected_revisions: { 'rule-1': 3 } }
    expect(await businessApi.batchRules(input, 'batch-key')).toMatchObject({ operation: 'pause', items: [{ id: 'rule-1', revision: 4 }] })
    const [url, options] = fetchMock.mock.calls[0] as [string, RequestInit]
    expect(url).toBe('/api/v1/forwarding-rules/batch')
    expect(options.method).toBe('POST')
    expect(new Headers(options.headers).get('Idempotency-Key')).toBe('batch-key')
    expect(JSON.parse(options.body as string)).toEqual(input)
  })

  it('previews, commits and exports forwarding transfers through dedicated endpoints', async () => {
    const fetchMock = vi.fn()
      .mockResolvedValueOnce(new Response(JSON.stringify({ preview: { format: 'ny_text', total: 1, valid: 1, invalid: 0, rows: [], global_issues: [] } }), { headers: { 'content-type': 'application/json' } }))
      .mockResolvedValueOnce(new Response(JSON.stringify({ result: { created: 1, updated: 0, items: [] }, replayed: false }), { headers: { 'content-type': 'application/json' } }))
      .mockResolvedValueOnce(new Response(JSON.stringify({ schema_version: 'nyvp.forwarding-rules/v1', exported_at: '2026-10-03T00:00:00Z', rules: [] }), { headers: { 'content-type': 'application/json' } }))
    vi.stubGlobal('fetch', fetchMock)
    const input = { format: 'ny_text' as const, content: 'rule##target.example#443', mapping: { customer_id: 'c-1', rule_group_id: '', entry_group_id: 'entry-1', egress_mode: 'DIRECT' as const, exit_group_id: '', protocol: 'tcp' as const, selection_policy: 'round_robin' as const } }
    expect((await businessApi.previewRuleImport(input)).valid).toBe(1)
    expect((await businessApi.importRules(input, 'import-key')).created).toBe(1)
    expect((await businessApi.exportRules()).schema_version).toBe('nyvp.forwarding-rules/v1')
    expect(fetchMock.mock.calls.map((call) => call[0])).toEqual([
      '/api/v1/forwarding-rules/import/preview', '/api/v1/forwarding-rules/import', '/api/v1/forwarding-rules/export',
    ])
    expect(new Headers((fetchMock.mock.calls[1]?.[1] as RequestInit).headers).get('Idempotency-Key')).toBe('import-key')
  })

  it('retains an operation key for retry and rotates it only for a changed payload', () => {
    const keyFor = useMutationKey()
    const initial = keyFor({ revision: 3, paused: true })
    expect(keyFor({ revision: 3, paused: true })).toBe(initial)
    expect(keyFor({ revision: 3, paused: false })).not.toBe(initial)
  })
})
