export const probeMemberOrderStorageKey = 'hl_probe_member_order_v1'
export const unassignedProbeOrderKey = '__unassigned__'
export type ProbeMemberOrders = Record<string, string[]>
export type ProbeMemberMove = { nodeId: string; targetId: string }

export function loadProbeMemberOrders(): ProbeMemberOrders {
  const orders: ProbeMemberOrders = Object.create(null)
  try {
    const value: unknown = JSON.parse(localStorage.getItem(probeMemberOrderStorageKey) ?? '{}')
    if (!value || typeof value !== 'object' || Array.isArray(value)) return orders
    for (const [groupId, ids] of Object.entries(value)) {
      if (Array.isArray(ids)) orders[groupId] = [...new Set(ids.filter((id): id is string => typeof id === 'string' && Boolean(id.trim())))]
    }
  } catch { /* Local preferences must never block live monitoring. */ }
  return orders
}

export function orderProbeMembers<T extends { node_id: string }>(items: readonly T[], order: readonly string[] = []): T[] {
  const positions = new Map(order.map((id, index) => [id, index]))
  // Stable sorting keeps newly joined machines in their existing relative order.
  return [...items].sort((left, right) => (positions.get(left.node_id) ?? order.length) - (positions.get(right.node_id) ?? order.length))
}

export function saveProbeMemberMove(groupId: string, items: readonly { node_id: string }[], move: ProbeMemberMove): ProbeMemberOrders {
  const orders = loadProbeMemberOrders()
  const ids = orderProbeMembers(items, orders[groupId]).map(item => item.node_id)
  const source = ids.indexOf(move.nodeId)
  const target = ids.indexOf(move.targetId)
  if (!groupId || source < 0 || target < 0 || source === target) return orders
  ids.splice(source, 1)
  ids.splice(target, 0, move.nodeId)
  orders[groupId] = ids
  localStorage.setItem(probeMemberOrderStorageKey, JSON.stringify(orders))
  return orders
}
