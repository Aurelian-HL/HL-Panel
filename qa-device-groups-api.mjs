import { createServer } from 'node:http'

// Local-only UI fixture. It never connects to the control plane or production services.
const now = '2026-10-04T06:00:00Z'
const groups = [
  { id: 'qa-entry', name: '广州测试入口', kind: 'ENTRY', user_group_id: 'qa-users', hide_in_probe: false, selection_policy: 'weighted_round_robin', description: '仅用于本地界面验收', member_count: 1, current_generation: 2, metadata_revision: 1, created_at: now, updated_at: now },
  { id: 'qa-exit', name: '香港测试出口', kind: 'EXIT', user_group_id: 'qa-users', hide_in_probe: false, selection_policy: 'weighted_least_connections', description: '仅用于本地界面验收', member_count: 0, current_generation: 1, metadata_revision: 1, created_at: now, updated_at: now },
]
const networks = [
  { group_id: 'qa-entry', connect_host: 'entry.example.test', port_start: 10000, port_end: 10010, port_ranges: [{ start: 10000, end: 10010 }], direct_policy: 'OPTIONAL', allow_direct: true, allowed_user_group_ids: ['qa-users'], allowed_entry_group_ids: [], allowed_exit_group_ids: ['qa-exit'], fallback_exit_group_id: 'qa-exit', traffic_multiplier: 1, revision: 1 },
  { group_id: 'qa-exit', connect_host: '', port_start: 0, port_end: 0, port_ranges: [], direct_policy: 'DISABLED', allow_direct: false, allowed_user_group_ids: ['qa-users'], allowed_entry_group_ids: ['qa-entry'], allowed_exit_group_ids: [], fallback_exit_group_id: '', traffic_multiplier: 1, revision: 1 },
]
const tokens = [{ id: 'qa-token-1', name: '待安装测试节点', group_id: 'qa-entry', created_at: now, expires_at: '2026-12-31T23:59:59Z' }]
const userGroups = [{ id: 'qa-users', name: '测试用户组', description: '', allowed_entry_group_ids: ['qa-entry'], allowed_exit_group_ids: ['qa-exit'], allow_direct: true, revision: 1 }]

function send(response, status, body) {
  response.writeHead(status, { 'Content-Type': 'application/json; charset=utf-8', 'Cache-Control': 'no-store' })
  response.end(JSON.stringify(body))
}

const server = createServer(async (request, response) => {
  const path = new URL(request.url, 'http://127.0.0.1').pathname.replace(/^\/api\/v1/, '')
  const method = request.method
  let input = {}
  if (method === 'POST' || method === 'PUT') {
    const chunks = []
    for await (const chunk of request) chunks.push(chunk)
    try { input = JSON.parse(Buffer.concat(chunks).toString() || '{}') }
    catch { return send(response, 400, { error: { message: '无效的 JSON' } }) }
  }

  if (method === 'GET' && path === '/public/site-info') return send(response, 200, { settings: { id: 'qa-site', site_name: 'HL-panel 本地验收', panel_title: '设备组本地验收', public_description: '', support_url: '', theme: 'classic', background_image_url: '', revision: 1, updated_at: now }, announcements: [], platform_version: 'qa-only', build_time: null })
  if (method === 'POST' && path === '/auth/login') return send(response, 200, { access_token: 'local-ui-fixture', expires_at: '2026-12-31T23:59:59Z', user: { id: 'qa-admin', username: input.username || 'qa', display_name: '本地验收', role: 'administrator' } })
  if (method === 'GET' && path === '/device-groups') return send(response, 200, { items: groups })
  if (method === 'GET' && path === '/device-groups/qa-entry/monitoring/nezha') return send(response, 200, {
    upstream_status: 'ok',
    items: [{
      node_id: 'qa-node-1', link_status: 'linked', nezha_server_id: 17, online: true,
      name: '本地样例 · 广州测试入口', ipv4: '192.0.2.10', ipv6: '2001:db8::10', country_code: 'CN',
      uptime_seconds: 172800, cpu_percent: 32.5,
      memory_used_bytes: 2147483648, memory_total_bytes: 4294967296,
      disk_used_bytes: 10737418240, disk_total_bytes: 42949672960,
      net_in_speed_bytes_per_second: 1048576, net_out_speed_bytes_per_second: 524288,
      net_in_transfer_bytes: 1073741824, net_out_transfer_bytes: 536870912,
      sampled_at: new Date().toISOString(),
    }],
  })
  if (method === 'GET' && path === '/device-groups/qa-exit/monitoring/nezha') return send(response, 200, { upstream_status: 'disabled', items: [] })
  if (method === 'GET' && path === '/nodes') return send(response, 200, { items: [] })
  if (method === 'GET' && path === '/group-networks') return send(response, 200, { items: networks })
  if (method === 'GET' && path === '/user-groups') return send(response, 200, { items: userGroups })
  if (method === 'GET' && path === '/device-groups/qa-entry/members') return send(response, 200, { items: [{ group_id: 'qa-entry', node_id: 'qa-node-1', dial_host: 'node.example.test', weight: 100, priority: 0, retired_at: null, created_at: now, updated_at: now }] })
  if (method === 'GET' && path === '/device-groups/qa-exit/members') return send(response, 200, { items: [] })
  const pending = path.match(/^\/device-groups\/(qa-entry|qa-exit)\/enrollment-tokens$/)
  if (method === 'GET' && pending) return send(response, 200, { items: tokens.filter((item) => item.group_id === pending[1]) })
  if (method === 'POST' && path === '/enrollment-tokens') {
    const id = `qa-token-${Date.now()}`
    tokens.push({ id, name: input.name, group_id: input.group_id, created_at: now, expires_at: '2026-12-31T23:59:59Z' })
    return send(response, 200, { id, name: input.name, token: 'local-ui-fixture-token', expires_at: '2026-12-31T23:59:59Z' })
  }
  const revoke = path.match(/^\/enrollment-tokens\/(qa-token-[^/]+)\/revoke$/)
  if (method === 'POST' && revoke) {
    const index = tokens.findIndex((item) => item.id === revoke[1])
    if (index < 0) return send(response, 404, { error: { message: '测试令牌不存在' } })
    tokens.splice(index, 1)
    return send(response, 200, { token: { id: revoke[1], revoked_at: now }, replayed: false })
  }
  const groupUpdate = path.match(/^\/device-groups\/(qa-entry|qa-exit)$/)
  if (method === 'PUT' && groupUpdate) {
    const group = groups.find((item) => item.id === groupUpdate[1])
    Object.assign(group, input, { metadata_revision: group.metadata_revision + 1 })
    return send(response, 200, { group })
  }
  const networkUpdate = path.match(/^\/group-networks\/(qa-entry|qa-exit)$/)
  if (method === 'PUT' && networkUpdate) {
    const network = networks.find((item) => item.group_id === networkUpdate[1])
    Object.assign(network, input, { revision: network.revision + 1 })
    return send(response, 200, { network })
  }
  return send(response, 404, { error: { message: `本地测试 API 未提供 ${method} ${path}` } })
})

server.listen(8080, '127.0.0.1', () => console.log('Local-only device-group fixture: http://127.0.0.1:8080/api/v1'))
