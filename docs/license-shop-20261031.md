# HL 侧边栏商店与一次性兑换码（2026-10-31）

## 本轮交付状态

本功能已完成本地实现和隔离验收，纳入 v0.1.155 发行候选。独立授权签发服务（issuer）具备商品目录、库存发码、导出、核销记录和兑换接口；HT 后台的库存管理页面及生产接入尚未实现。用户随后授权本次只发布并升级 HL 面板，不升级 HT 或节点。

旧 issuer 的商品目录接口明确返回 404/405/501 时，HL 返回无价格、无购买链接的默认方案，且 `redemption_enabled=false`；页面显示“商店尚未开通”，禁用新兑换。其他 HTTP 故障和签名错误不降级成可信商品；待确认收据仍可恢复，不改现有授权。新增兼容回归后，Chrome 三视口检查为 45 项，45 张截图，无非预期错误。

用户确认发卡商城尚未搭建。HL 侧边栏新增独立“商店”，地址为 `/shop`，默认提供月、季、半年、年、永久五种授权。尚未配置的价格显示“价格待公布”，尚未配置的购买链接显示禁用的“购买入口待开放”。没有模拟付款、订单成功或自动发货。个人中心原有授权状态卡保留兼容。

商店页面源码：`apps/web-admin/src/pages/ShopPage.vue`。路由与导航位于 `apps/web-admin/src/router.ts`、`apps/web-admin/src/layouts/AppShell.vue`。商店和兑换接口位于 `internal/control/httpapi/panel_license_handlers.go`，客户端位于 `internal/panellicense/shop_client.go`。独立 issuer 的库存和兑换实现位于 `internal/panellicenseissuer/stock*.go`、`redemption.go`。

## 用户操作

1. 管理员登录 HL，在侧边栏进入“商店”，查看当前域名、授权状态和北京时间到期日。
2. 商城开通并配置链接后，点击对应方案的“前往购买”，在外部发卡商城完成支付和取码。链接仅允许 HTTPS，并隔离新窗口权限。
3. 将 `hlr1_` 开头的完整兑换码粘贴到商店，点击“激活兑换码”。首次成功兑换绑定此域名、面板实例及实例公钥。自动验证当前域名，无需用户手动添加 TXT。
4. 有效的有期授权续期时，从现有到期与当前时间的较晚者累加；免费版升级保留原授权身份；永久正式授权不再消耗额外续期码。
5. 请求超时、结果未确认或客户端重启后，点击“恢复兑换结果”。恢复返回该授权的当前状态，不重复增加期限，也不能通过旧收据恢复已经撤销或更换绑定的权限。

兑换输入采用密码框，提交后清空，不写 URL、浏览器持久存储或日志。客户端提交核销前先保存 issuer 签名收据，收据不含明文兑换码。收据或结果保存失败时保留最后可信状态；仍有待确认兑换时保留恢复入口。旧查询响应不得覆盖新兑换结果或污染新管理员会话。

兑换与旧授权激活入口限制 JSON 请求体为 4096 字节，只接受单个 JSON 对象和已知字段。解析失败返回固定文案，不回显可能包含兑换码的未知字段名或原始解析诊断；回归测试已先复现泄露，再验证修复。

## issuer 配置

保留现有 issuer 的数据库、签名密钥、管理员 token、issuer ID 和 key ID 配置。本轮新增：

| 环境变量 | 内容 | 未设置时 |
| --- | --- | --- |
| `HL_LICENSE_ISSUER_EXPORT_KEY_FILE` | 指向密钥文件；文件内容为独立随机 32 字节密钥的无 padding base64url 编码 | 可以读取目录，但不能发码或导出库存 |
| `HL_LICENSE_ISSUER_PRODUCTS_FILE` | 指向严格 JSON 数组文件，最多 64 KiB | 返回默认五个商品，不带价格和购买链接 |

导出密钥与签名密钥分开管理，不放仓库、浏览器或普通日志。商品文件更改由 issuer 启动时加载；上线调整配置需要按独立 issuer 服务流程办理，不涉及转发引擎。

以下是可用的未开售商品配置：

```json
[
  {"id":"monthly","name":"月度授权","term":{"kind":"calendar_months","months":1}},
  {"id":"quarterly","name":"季度授权","term":{"kind":"calendar_months","months":3}},
  {"id":"half-year","name":"半年授权","term":{"kind":"calendar_months","months":6}},
  {"id":"yearly","name":"年度授权","term":{"kind":"calendar_months","months":12}},
  {"id":"perpetual","name":"永久授权","term":{"kind":"perpetual"}}
]
```

开售后为每个商品增加 `price_label` 和 `purchase_url`。前者是受控展示文案，实际支付价格和订单由商城处理；后者填写真实 HTTPS 商品页。商品最多 20 个，ID 唯一，禁止账号密码式 URL 和控制字符。传入空数组表示暂不展示商品。发码时固化商品期限，后续目录调整不会改变已经签发库存的期限。

## 发码、导出与商城接入

以下为独立 issuer 管理接口，只允许可信服务端持有的 Bearer token。HT 接入时，`actor` 必须来自已验证的管理会话，不能信任浏览器自报身份。

| 方法 | 路径 | 用途 |
| --- | --- | --- |
| GET | `/api/v2/license/redemption-batches?offset=0` | 每页最多 100 批，必要时返回 `next_offset` |
| POST | `/api/v2/license/redemption-batches` | 创建一批 1–1000 个一次性兑换码 |
| POST | `/api/v2/license/redemption-batches/{batch}/export` | 在有效期内导出这批库存，返回 `codes` 数组 |
| POST | `/api/v2/license/redemption-batches/{batch}/confirm-export` | 确认导入商城并清除导出密文 |
| POST | `/api/v2/license/redemption-batches/{batch}/disable` | 禁用该批未核销的码并清除导出密文 |
| GET | `/api/v2/license/redemption-batches/{batch}/redemptions` | 查询该批核销记录 |

创建批次请求示例（示例不含真实秘密）：

```json
{
  "product_id": "monthly",
  "count": 100,
  "actor": {"id": "trusted-admin-id", "name": "授权管理员"},
  "reason": "导入月度授权商品库存"
}
```

导出、确认导出和禁用请求包含 `actor`、`reason`。创建、确认导出、禁用须携带 `Idempotency-Key`（16–128 字符）；重试同一操作保持同一键和请求内容。导出可在 24 小时内重复读取，防止下载丢失导致重新发码；每次导出都有审计。

接入顺序：

1. 在 HT 可信后端调用创建批次，再调用导出。
2. 将返回的 `codes` 转为一行一个码，导入商城的对应授权商品；不得把兑换码写进公开文档、日志或 Git。
3. 确认商城库存导入成功后调用 `confirm-export`。此后 issuer 不再提供这批明文导出，商城负责保管交付库存。
4. 配置各商品真实购买链接和价格文案，重新加载独立 issuer 配置。
5. 以隔离测试商品验收真实支付、发货、兑换和 HT 核销查询，再开放销售。

数据库只保存码的 hash 与短前缀，临时导出材料以 AES-GCM 加密保存。确认导出、禁用或导出过期后清除密文；过期清理由启动及每小时任务处理。**导出过期只关闭导出，不使已交付的未核销码失效。** 禁用批次也不撤销已经形成的授权。

同码并发核销只有一次成功变更；核销记录、授权期限与审计在同一个 PostgreSQL 事务内提交。不同码的核销不使用进程全局锁。DNS/TLS 网络访问在数据库事务外进行，避免外部验证长期占用数据库锁。

## 协议和域名证明

HL 管理员接口：

```text
GET  /api/v1/license/shop
POST /api/v1/license/redemption          {"code":"完整兑换码"}
POST /api/v1/license/redemption/resume
```

这些接口在授权尚未激活或到期时仍可由已登录管理员使用；未登录或客户 token 不能调用。已有 `hl2_` 授权流程保持兼容，不将其当作 `hlr1_` 库存码提交。

issuer 的公开协议均采用 POST、实例签名请求及 issuer 签名响应：

| 路径 | operation |
| --- | --- |
| `/api/v2/public/hl-license/shop/catalog` | `shop-catalog` |
| `/api/v2/public/hl-license/redemption/prepare` | `redeem-prepare` |
| `/api/v2/public/hl-license/redemption/commit` | `redeem-commit` |

首次核销时 issuer 通过公网 HTTPS 访问 HL 的 `/.well-known/hl-panel-license/` 挑战路径。要求可信证书、TLS 1.2 或以上、正确 Host/SNI；只拨号到已经校验的公网 DNS 地址，不接受内网、重定向或环境代理。IPv6 失败会在总预算内尝试 IPv4，总验证预算为 5 秒。用户不用操作 DNS TXT，但当前面板的 HTTPS 443 与挑战路径必须可达。

代理源 `deploy/nginx/snippets/hl-panel-app-locations.conf` 已包含该挑战前缀；发布时仍须核验真实现场代理，不能以本地文件替代线上验收。公开代理须覆盖新增目录和兑换路径，管理路径继续受保护。

核销记录包含批次、码短前缀、商品、授权 ID、域名、实例、公钥、核销时间及成功验证的 DNS IP；IP 来源为 `https-verified-dns`。如果域名经过 CDN，记录可能是 CDN 公开地址，不能声称已取得真实源站 IP。完整码和管理 token 不在记录中返回。

授权操作只作用于独立授权状态和库存，不写规则、订阅或节点配置，也不调用转发引擎重启。转发、订阅和节点运行不依赖外部商城支付状态。

## 实际验收

全部使用 loopback、模拟目录或独立 PostgreSQL，不连接生产支付或生产数据库。

| 命令或检查 | 结果 |
| --- | --- |
| `go test ./internal/panellicense ./internal/panellicenseissuer ./internal/control/httpapi ./apps/panel-license-issuer -count=1` | 通过；该轮未设置数据库 DSN，数据库集成测试由下一项单独验收 |
| 设置本地 `HL_LICENSE_TEST_DATABASE_URL` 后执行 `go test ./internal/panellicenseissuer -count=1 -v` | 通过，实际运行 8 项库存 PostgreSQL 集成及 4 项 loopback TLS 测试，覆盖并发、核销持久化、撤销及客户端重启恢复 |
| `go test ./internal/control/httpapi -count=1` | 最终修改后全包通过，包含请求解析不泄露授权码、认证、故障文案及转发服务隔离 |
| `go vet ./internal/panellicense ./internal/panellicenseissuer ./internal/control/httpapi ./apps/panel-license-issuer` | 通过 |
| `npm test -- --run`（`apps/web-admin`） | 70 个测试文件、402 项测试通过 |
| `npm run build`（`apps/web-admin`） | TypeScript 和 Vite 构建通过 |
| `node tests/browser/license-shop-smoke.cjs` | Chrome 42 个状态/视口检查通过，42 张截图，无非预期控制台错误或横向溢出 |
| `git diff --check` | 通过 |

新增三项前端回归测试先复现了旧请求覆盖新兑换状态、旧失败污染错误状态和新读取被旧请求干扰的问题，修复后通过。浏览器检查使用 synthetic API 数据覆盖 1440、1024、390 三种宽度，各 14 个状态；故意注入的 409/502 作为预期错误单独记录，不能等同于真实支付已验收。桌面与手机默认目录截图已打开目视检查。

真实生成的产物目录：

```text
C:/Users/hf/Documents/Codex/2026-10-16/codex-threads-01a10f3e-8cf5-7312-b6c9/work/hl-usage-tail-release150/artifacts/license-shop-browser
```

内含 `report.json`、`1440-unactivated-default-catalog.png`、`390-unactivated-default-catalog.png` 等 42 张截图。本说明位于同一仓库的 `docs/license-shop-20261031.md`。前端实际构建产物位于 `apps/web-admin/dist`。

## 尚未交付及后续上线边界

- 发卡商城及真实商品尚未搭建，真实购买、支付、发货未验收。
- HT 的库存管理 UI、服务端对接和生产部署尚未完成；当前是独立 issuer API 实现。
- 生产域名 HTTPS 证明路径、真实代理和完整购买闭环尚未验收。
- v0.1.155 发行及 HL 面板升级按独立发行验收办理；真实版本和生产检查记录以对应发布回执为准。商店实现不写生产规则、订阅、客户数据或节点配置。

开放真实购买与兑换前，须先核验并升级独立 issuer 与代理，再接入 HT 和商城；仅升级 HL 时使用上述未开放状态。组件回滚应保留最新授权身份、实例私钥、核销记录和 revision；不能回放旧数据库或旧授权缓存来恢复权限。已有其他改动必须保留，不使用全仓库 reset 清除。
