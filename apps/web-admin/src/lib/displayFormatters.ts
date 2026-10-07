export function formatDateTime(value: string | null | undefined): string {
  if (!value) return '暂无记录'
  const date = new Date(value)
  if (Number.isNaN(date.getTime())) return '时间无效'
  return new Intl.DateTimeFormat('zh-CN', {
    year: 'numeric', month: '2-digit', day: '2-digit', hour: '2-digit', minute: '2-digit', second: '2-digit', hour12: false,
  }).format(date)
}

export function formatRelativeTime(value: string | null | undefined): string {
  if (!value) return '从未上报'
  const timestamp = Date.parse(value)
  if (Number.isNaN(timestamp)) return '时间无效'
  const seconds = Math.round((timestamp - Date.now()) / 1000)
  const absolute = Math.abs(seconds)
  const formatter = new Intl.RelativeTimeFormat('zh-CN', { numeric: 'auto' })
  if (absolute < 60) return formatter.format(seconds, 'second')
  if (absolute < 3600) return formatter.format(Math.round(seconds / 60), 'minute')
  if (absolute < 86400) return formatter.format(Math.round(seconds / 3600), 'hour')
  return formatter.format(Math.round(seconds / 86400), 'day')
}

export function displayError(error: unknown): string {
  const message = error instanceof Error ? error.message : ''
  const translations: Array<[RegExp, string]> = [
    [/endpoint pool is referenced by a customer identity/i, '该规则的接入端点仍绑定客户身份，请先解除身份绑定'],
    [/endpoint pool has active runtime material/i, '该规则的接入端点仍有生效中的运行配置，暂不能删除'],
    [/endpoint pool has active connections/i, '该规则的接入端点仍有活动连接，请等待连接结束后重试'],
    [/device group has active members/i, '设备组仍有在线成员，请先退役或移除成员后再删除'],
    [/device group has a network policy/i, '设备组仍配置了网络策略，请先删除该网络策略后再删除设备组'],
    [/device group is referenced by a network policy/i, '设备组仍被其他网络策略引用，请先解除网络策略引用后再删除'],
    [/device group is referenced by a user group/i, '设备组仍被用户组授权引用，请先从用户组授权中移除后再删除'],
    [/device group is referenced by a forwarding rule/i, '设备组仍被转发规则引用，请先删除或修改相关转发规则后再删除'],
    [/device group is referenced by an endpoint pool/i, '设备组仍被端点池引用，请先删除或修改相关端点池后再删除'],
    [/device group has published revisions/i, '设备组仍有已发布配置版本，请先清理配置版本后再删除'],
    [/revision|version.*conflict|stale|changed; reload/i, '数据已被其他操作更新，请刷新后重试'],
    [/direct.*authoriz/i, '直出需要用户组与入口组同时授权'],
    [/exit.*authoriz/i, '出口组需要用户组与入口组同时授权'],
    [/entry.*authoriz|authoriz.*entry/i, '该客户没有使用此入口组的权限'],
    [/unauthori[sz]|forbidden|not allowed|permission/i, '当前账号没有执行此操作的权限'],
    [/customer.*(disabled|expired)|user.*(disabled|expired)|not active/i, '客户当前不可用，请检查账号状态和有效期'],
    [/quota|limit.*rule|max.*rule|traffic.*exhaust/i, '客户额度或规则数量已达到限制'],
    [/network.*(required|missing)|entry.*network/i, '请先配置入口设备组的网络参数'],
    [/connect[_ ]host|host must be one DNS name|连接地址/i, '连接地址格式无效，请填写单个域名或 IP（不含协议、端口或路径）'],
    [/network change would invalidate a bound service endpoint/i, '端口范围不能移除已绑定的服务端点，请保留端口或先解除端点绑定'],
    [/network policy would invalidate an existing forwarding rule|exit policy would invalidate an existing forwarding rule/i, '当前网络策略会影响已有转发规则，请保留规则所需端口和授权'],
    [/port.*(range|allocated|conflict|use)|listen.*port/i, '监听端口不可用，请更换端口或使用自动分配'],
    [/target.*invalid|invalid.*host|hostname/i, '目标地址格式无效，请填写 IP:端口或域名:端口'],
    [/reality.*(server|sni)|reality_server_name/i, 'Reality SNI/服务器名称格式无效，请填写域名或 IP'],
    [/reality.*public|reality_public_key/i, 'Reality 公钥格式无效，请检查 Base64URL 公钥'],
    [/reality.*short|reality_short_id/i, 'Reality Short ID 格式无效，请填写偶数长度十六进制字符'],
    [/vless.*flow|xtls-rprx-vision/i, 'VLESS 流控必须使用 xtls-rprx-vision'],
    [/vless.*(direct|tcp)|ingress.*protocol/i, 'VLESS + Reality 当前仅支持 TCP 入口直出'],
    [/duplicate target/i, '目标地址重复，请移除重复项'],
    [/invalid credentials|invalid.*password|username or password/i, '用户名或密码错误'],
    [/username.*(exists|taken)|duplicate.*username/i, '该用户名已存在'],
    [/not found|does not exist/i, '记录不存在，请刷新后重试'],
    [/network|fetch|timeout|connection/i, '连接失败，请检查网络后重试'],
  ]
  if (/[\u3400-\u9fff]/.test(message)) return message
  return translations.find(([pattern]) => pattern.test(message))?.[1] ?? '操作失败，请检查填写内容并重试'
}

export function engineSummary(versions: Record<string, string>): string {
  const entries = Object.entries(versions)
  return entries.length ? entries.map(([name, version]) => `${name} ${version}`).join(' · ') : '未上报'
}
