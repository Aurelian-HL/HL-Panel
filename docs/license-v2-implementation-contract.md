# HL / HT 授权 v2 实施契约（2026-10-20）

本轮免费域名登记和档位变更为本地实现及隔离验收范围，尚未发布；不得据此声称生产已生效。生产发布、升级、DNS 和线上授权修改仍暂停。恢复发布时必须基于当时最新线上文件备份并打代码补丁，不能上传旧 HT data 或回退新增客户数据。

## 协议与独立性

跨服务 DTO 以 `internal/panellicense/protocol.go` 为准，档位与配额定义见同包 `entitlements.go`。Ed25519 签名覆盖原始 base64url JSON 字节，无 padding；响应签名前缀为 `hl-panel-license-v2\n{key_id}\n{payload}`，实例请求前缀为 `hl-panel-instance-v2\n{operation}\n{payload}`。不同 operation 的签名不可互换。私钥和管理 token 不发浏览器。

独立 issuer 仅访问独立 PostgreSQL 的授权 schema，不读取或写入 HT、HL 客户数据库。HT 通过受保护的管理接口办理授权；HL 只调用此版本化授权协议。转发、订阅、节点和既有业务数据不依赖 HT 业务 API；授权身份不得混入客户 snapshot。

`InstanceRequest` 包含 `payload` 和 `signature`；响应 `Envelope` 包含 `payload`、`key_id` 和 `signature`。`Request` 公共身份字段为 version、product、audience、domain、instance_id、instance_public_key、随机 request_id 和 UTC issued_at。公钥是 raw base64url 32 字节，thumbprint 是其 SHA256 hex。域名规范化为 ASCII DNS（支持 punycode，不接受 IP、端口或 URL）。请求时间窗口为 ±5 分钟，客户端 request_id 至少使用 128 位随机值，每个新请求使用新编号。

## 公开接口

以下路径均以 `/api/v2/public/hl-license` 为前缀，仅使用 POST：

| 相对路径 | 签名 operation | 除公共身份外允许的字段 | 签名响应 |
| --- | --- | --- | --- |
| `/activation/prepare` | `prepare` | code | Challenge |
| `/activation/commit` | `commit` | reservation_id | Claims |
| `/sync` | `sync` | license_id | Claims |
| `/free/prepare` | `free-prepare` | 无 | Challenge |
| `/free/commit` | `free-commit` | challenge（完整 Envelope） | Claims |

`prepare/commit/sync` 拒绝 `challenge`；免费两步拒绝 code、license_id、reservation_id；`free-prepare` 不接受 challenge，`free-commit` 必须携带原始完整 challenge Envelope，不能只传解码的 Challenge 或其 reservation_id。同步使用 license_id，不需要原始授权码。

Challenge 携带 version、issuer、product、audience、request_id、domain、instance_id、instance_key_thumbprint、reservation_id、dns_name、dns_value、issued_at、expires_at。TXT 名固定为 `_hl-license.{domain}`。HL 验签并核对用途、身份、时效；新鲜签名响应的 request_id 必须对应本次请求。免费提交的外层 request_id 对应提交请求，内层 challenge.request_id 保留准备请求编号，两者无需相同。非签名错误、网络故障或服务不可用不能覆盖最后可信授权状态。

### 带码激活与管理员恢复

`activation/prepare` 根据已有授权码建立十分钟持久预留，Challenge 的 purpose 为空，TXT 值为 `hl-license-v2={随机挑战}`。相同 code/domain/instance/key 的有效预留可复用。`activation/commit` 验证 DNS TXT、相同实例签名与预留时效，持有授权行锁后再次读取预留，防止撤销、换码、换绑竞态。失败不永久占码；不同域名或实例不得覆盖已有绑定。

此带码流程用于正式授权，也保留供 HT 超级管理员显式换码、换绑后的免费授权恢复。恢复仍须完成 DNS 证明；签发的 Claims 以该记录实际 tier 和 limits 为准，带码不会把免费授权提升为正式授权。

### 免费自助登记

免费用户在 HL 内自助登记并验证域名；首次登记不签发免费授权码。`free-prepare` 返回 purpose=`free-registration` 的签名 Challenge，此步骤完全无状态，不访问数据库、不创建授权、不占用域名或持久预留。其 reservation_id 以 `free_` 开头，仅为签名证明标识；TXT 值为 `hl-license-v2={挑战值}`。挑战值依据完整实例签名请求和当前 issuer 签名密钥确定性生成，ID 与 DNS 使用分离的派生用途。同一密钥配置下，完全相同请求在重试或重启后得到相同响应；有效期固定为请求 issued_at 至 issued_at+10 分钟，重试不延长有效期。

`free-commit` 先验证完整 challenge 的 issuer 签名、purpose、协议身份、域名、实例、公钥指纹、TXT 名和值及十分钟时效，再查询 DNS。进入事务后，在域名锁及授权行锁下再次验证 challenge 时效和域名归属，才创建永久免费授权及审计。DNS 未通过时不创建授权、不占用域名。内部唯一 code_hash 列使用不可猜随机占位值，不向用户交付免费登记码。

域名已有同一实例、公钥和域名绑定的 active 免费授权时，可恢复同一授权，保留 license_id、revision、activated_at，并增加 `free-register-recover` 审计。正式授权无论待激活、有效、过期或撤销，均不能通过自助登记变为免费；已撤销的免费授权、不同实例或公钥、未完成管理换绑的待激活记录，以及历史换绑域名也不能重新登记。HL 已有授权声明时不自动重新登记或降档。正式过期、撤销或缓存异常不能作为获得免费权限的依据，档位变更必须走 HT 显式管理操作。

### 幂等与代理

除 `free-prepare` 外，公开操作的成功结果与操作摘要在事务中持久保存；完全相同请求可返回原签名结果，同 request_id 异内容拒绝。重放仍须通过请求签名与 ±5 分钟时间检查；返回的是原结果，不是重新执行激活或变更，也不保证代表重放时刻的最新授权状态。客户端不得接受 revision 或时间倒退。

`free-prepare` 是明确例外：它不保存防重放记录，不对同编号异内容建立持久冲突索引；相同完整签名请求的确定性响应与短时效提供有界重试，不构成域名所有权或授权凭证。

公开代理必须覆盖上述五个路径；已有不改写 URI 的 `/api/v2/public/hl-license/` 前缀代理可以覆盖新增免费路径。管理 API 继续限制可信来源与 Bearer token，不能因新增免费接口开放管理前缀。当前本地保存的代理源不代表现场配置已核验，恢复发布时须验证真实 HTTPS、路径转发及 TLS 校验。

## 档位与配额

Claims 和 LicenseRecord 必须显式输出 tier、limits；limits 包含 device_groups 和 rules：

| tier | limits | Term 与含义 |
| --- | --- | --- |
| `free` | `{"device_groups":1,"rules":10}` | 固定 perpetual，license_expires_at=null |
| `licensed` | `{"device_groups":0,"rules":0}` | 授权层不设面板总量上限，原业务配额继续执行 |

免费额度按整个面板计算，跨归属人、协议和启停状态；未删除的停用规则也计数。新增和导入均须在服务端原子检查，不能通过并发、切换入口或不同所有者绕过。降为免费不得自动删除既有数据或中断存量转发；超过额度时限制新增，不把已有数据恢复成旧快照。

未知或缺失 tier、与 tier 不匹配的 limits 不能由客户端默认为正式授权。旧记录兼容只发生在可信 issuer 存储边界：旧记录 tier 为空且 limits 为零时可投影为 licensed，随后签发显式字段；不得把此兼容用于用户输入或 HL 签名声明校验，也不批量改写历史幂等文档。HT GET 可兼容展示 tier 和 limits 都缺失的旧响应并标记 legacy_tier；写入响应必须使用显式合法字段。

HL 在验签后的原始 payload 边界核验 tier、limits 和两个整数配额字段均显式存在且名称大小写准确；null、空对象、只缺部分字段均拒绝。对于升级前保留的可信签名缓存，只有 tier 和 limits 两者完全缺失，且原身份、时间和 revision 合法，才进入 `upgrade_required`：保留原 license_id 仅供同步，管理保持关闭，不能自助登记免费。取回新的显式合法声明后才恢复对应管理权限；坏签名、错误绑定或回滚声明不能使用这一恢复路径。

## HT 管理接口与审计

管理 Bearer token 仅由可信 HT 后端持有。路径为 `/api/v2/license/codes` GET/POST、`/{id}/{extend,revoke,rotate-code,rebind,change-tier}` POST、`/{id}/audit` GET。列表返回 `{items:LicenseRecord[]}`，审计返回 `{items:AuditEvent[]}`，写入返回 MutationResult，错误统一 ErrorResponse。

POST 都需要 Idempotency-Key（16–128 字符），所有输入和可信 actor 一起摘要；同键异内容返回 409。首次签发使用 IssueRequest，已有授权的修改使用 MutationRequest 并要求 expected_revision；授权行锁下校验当前 revision，一次事务提交授权、幂等结果及审计。Actor 由 HT 当前超级管理员会话生成，issuer 只接受管理 token 信任边界内的 body actor，不接受浏览器自报 actor。校验 actor id/name、reason 长度；日志不包含 code、token 或私钥。

HT 和 issuer 不提供首次免费发码：issue 只签发 licensed，可信旧管理请求未带 tier 时兼容 licensed。免费首次登记必须使用 HL 自助流程。已登记免费授权的管理员换码、换绑恢复能力保留；其目标不是新建同域授权或改变档位。

`change-tier` 必须显式提交目标 tier、term、expected_revision 和原因，不能带 domain；其他管理动作不能夹带 tier。改档只变更 tier、limits、term 和相应到期，保留 license_id、域名、实例、公钥、授权码、激活记录和已有激活预留；revision 递增并记录可信 actor 审计。同档位返回 409，不修改授权或审计，正式期限调整使用 extend。已撤销授权不能改档。

从免费升级正式：已激活的相对期限从办理时刻起算，未激活的从首次激活起算；until 使用显式固定时间。改为免费必须 perpetual 和固定 1/10 配额，原到期置空。幂等重放返回原操作结果，不重复改档或更换身份。

域名占用及历史永久保留，不能靠换绑制造第二份同域授权。码只保存 hash 和短前缀；issue、rotate-code、rebind 的新码只在首次成功响应显示一次。幂等重试不重新返回原码（replayed=true 且 code 为空），未保存时须显式换码。HT 发码弹窗只在内存暂存，不能写日志或浏览器持久存储。

## 期限、换绑与签名状态

Term 使用同一对象，不另设套餐字符串：calendar_months 1–120（月、季、半年、年为 1、3、6、12）、days 1–36500、until RFC3339、perpetual，字段互斥。自然月按上海时区夹取月末，days 为 24 小时。首次带码激活时相对期限起算，until 固定；自助免费登记通过时即激活。

extend 从 max(now,旧到期) 加时；未激活只允许同种相对期限累加或更晚的 until；perpetual 为显式转换。不能缩短或隐式取消撤销。免费固定永久，不能通过 extend 改为有期或正式。rebind 为明确审计操作：可保留或更换域名，清空实例及公钥绑定，新码替代旧码，revision 递增，保留原商业到期和激活日；旧实例随后 sync 失败，新实例必须重新完成 DNS 证明。rotate-code 只替换凭证，不延长或换绑。撤销不会因续期、换码、换绑或改档恢复。

Claims status 为 active、expired 或 revoked；商业事实变化递增 revision，正常 sync 不变。issued_at 为签发时刻，默认 refresh_after=issued_at+30m、lease_valid_until=issued_at+24h；有期 active 租约夹取为 min(issued_at+24h, 商业到期)，refresh_after 保持在签发与租约到期之间。永久 license_expires_at=null 仍有有限离线租约。商业到期不靠离线窗口延长，active 自然到期变为 expired 不要求 revision 递增。

HL 拒绝 license_id 更换、revision 下降或 issued_at 倒退；同 revision 商业事实保持一致，仅允许自然到期状态变化。更高 revision 可在有效显式续期或改档后从 expired 恢复 active。issuer 继续为已到期或撤销的合法绑定实例签发状态。旧缓存可能在剩余租约内继续有效，不能承诺撤销或换绑立即使离线旧实例失效。

## HL 状态与管理边界

HL cache 使用独立受限状态目录，安装身份由 root 管理，仅必要服务账户可读写；不污染客户 snapshot。每次加载重新验签，并核对实例私钥、公钥、域名和授权身份，不能通过修改 JSON、删除身份或改配置获得新档位。更新、回滚须保留授权身份；不能回退签名状态来绕过 revision 检查。

授权门禁只限制管理写入，保留认证、授权恢复、存量订阅和 agent 的明确运行接口。所有豁免继续执行原身份验证，不能将整个 agent 前缀放行以接受新节点注册。故障或超额时保留存量运行；解除限制必须获得有效签名状态，不修改客户数据库或数据平面配置来伪造授权恢复。

## 恢复发布顺序

本轮保持生产暂停。后续恢复发布须先重新备份最新现场代码和授权状态，先部署能输出显式 tier/limits 的 issuer，再部署 HL 客户端和 HT 管理界面。新版 HT 写入响应会拒绝旧 issuer 的缺字段返回。升级 HL 时必须保留原实例私钥、身份目录和签名缓存；不能将本地测试数据库、临时信任密钥、旧 HT data 或测试二进制上传生产。按组件回滚代码时保留最新客户数据、授权身份和 revision，不用旧全量快照覆盖线上新增客户。
