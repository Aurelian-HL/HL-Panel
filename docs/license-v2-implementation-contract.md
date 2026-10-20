# HL / HT 授权 v2 实施契约（2026-10-20）

唯一 DTO 来源：`internal/panellicense/protocol.go`。签名是原始 base64url JSON 字节，无 padding；响应签名前缀 `hl-panel-license-v2\n{key_id}\n{payload}`，实例请求前缀 `hl-panel-instance-v2\n{operation}\n{payload}`。Ed25519，私钥和管理 token 不发浏览器。声明 Term 使用同一个对象，不使用另外的套餐字符串。

公共 POST 路径：`/api/v2/public/hl-license/activation/prepare`（operation=`prepare`）、`/activation/commit`（`commit`）、`/sync`（`sync`），请求 InstanceRequest，prepare 返回签名 Challenge，commit/sync 返回签名 Claims。Request 含版本、产品、audience、域名、实例 ID、公钥、随机 request_id、UTC issued_at。prepare 另带 code；commit 带 reservation_id；sync 带 license_id。公钥为 raw base64url 32 字节，thumbprint 为 SHA256 hex。同步不再需要原始授权码。

prepare 预留十分钟，Challenge 的 TXT 名固定 `_hl-license.{domain}`，值为 `hl-license-v2={随机挑战}`；commit 必须验证 DNS TXT 和相同实例签名才绑定，失败不永久占码。相同 code/domain/instance/key 重试幂等。不同域或实例不得覆盖。验证请求时间 ±5 分钟；request_id 至少 128 位随机；issuer 持久防重放，完全相同请求可返回缓存签名结果，异内容拒绝。签名响应 request_id 必须与本次请求一致。服务端不可用、非签名错误均不覆盖最后可信状态。

管理 Bearer token，仅 HT 服务持有，路径 `/api/v2/license/codes` GET/POST；`/{id}/{extend,revoke,rotate-code,rebind}` POST；`/{id}/audit` GET。列表 `{items:LicenseRecord[]}`，audit `{items:AuditEvent[]}`，写响应 MutationResult。POST 都需 Idempotency-Key（16–128 字符），所有输入、可信 actor 一起摘要；同键异内容 409。issue 用 IssueRequest，其余用 MutationRequest；每次修改要求 expected_revision，一次事务提交许可、幂等及 audit。Actor 由 HT 当前超级管理员会话生成，issuer 只接受管理 token 信任边界内的 body actor；不得接受浏览器自报 actor。校验 actor id/name、reason 长度，日志不含 code/token。错误统一 ErrorResponse。

域名规范化 ASCII DNS（支持 punycode，不接受 IP/端口/URL），数据库 domain 唯一永久保留；码只保存 hash+prefix，首次 issue/rotate 只显示一次；幂等重试不重新返回原码（Replayed=true 且 Code 空），HT 提示未保存需换码。正常发码结果可在 HT 发码弹窗暂存内存，绝不记录日志或浏览器持久存储。

Term: calendar_months 1–120（月/季/半年/年=1/3/6/12）、days 1–36500、until RFC3339、perpetual。互斥。自然月按上海时区夹取月末；days 为 24h。激活起算相对期限；until 固定。extend 从 max(now,旧到期) 加时；未激活只允许同种相对期限累加或 until 更晚；perpetual 显式转换。不能缩短或隐式取消 revoke。rebind 是明确审计操作：domain 可同域或新域，清空实例绑定、使旧实例 sync 失败、新码替代旧码，revision++，保留原商业到期。rotate-code 替换凭证，不延长不换绑。revoked 状态不得用 extend 恢复；如需恢复另行产品决策。

Claims status active/expired/revoked；商业事实变更递增 revision，正常 sync 不变。issued_at 为现在，默认 refresh_after=issued_at+30m、lease_valid_until=issued_at+24h；有期 active 的租约夹取为 min(issued_at+24h, 商业到期)，refresh_after 再夹取至 [issued_at, lease_valid_until]。商业到期不靠离线窗口延长。永久 expires=null 仍有限离线租约。active 到商业日期自然变 expired，不需 revision++。同 revision 商业事实必须完全一致，issued_at 只能前进；更高 revision 可从 expired 恢复 active，相同 ID 不永久封锁。issuer 必须继续为已到期或撤销的合法实例签名状态。

HL cache 独立 root-owned 状态目录，不污染客户 snapshot；每次加载重新验签并核对实例私钥、公钥和域名。缓存不能通过改 JSON 绕过签名。管理门禁仅限制写入，保留认证、授权恢复、存量订阅和 agent 的明确运行接口。所有豁免仍执行原身份验证，不能把整个 agent 前缀放行新节点注册。

本轮本地开发与隔离验收，不发版、不升级生产、不改 DNS 或客户数据。以后发布必须只打最新代码补丁，不能上传旧 HT data。
