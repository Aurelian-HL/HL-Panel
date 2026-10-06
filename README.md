# HL-panel

本项目的目标是构建独立的 NY 网络转发控制面板：支持 VLESS + Reality + Vision / TCP
入口、设备组转发和故障切换。项目由 `Aurelian-HL/HL-Panel` 持续维护，欢迎反馈问题。

独立重构中的网络控制平台。保留 NY 的非商业用户、授权、设备组和规则工作流，新增 VLESS + Reality + Vision 入口；Go 控制面与节点 Agent、Vue 3 管理端/客户界面、独立连接网关分模块部署，不依赖现有运营后台或旧 NY 的运行接口。客户业务交付仍由现有运营后台负责，不作为 HL-panel 模块。

**当前试运行版本尚未达到 NY 完整业务重构目标。** 用户/用户组、入口出口设备组、管理端和客户登录、规则控制面流程已接入；真实 Agent 自动下发、设备组网关动态配置、完整 VLESS 真实流量、NY 正式导入和跨机转发仍未闭环。后续按 [NY 业务验收基线](docs/ny-rebuild-acceptance-20261002.md) 推进；历史缺口见 [源码核查](docs/implementation-gap-20261002.md)。


## 已冻结的设备组规则

客户最终只获得一个稳定地址和一条协议链接。管理员只维护入口组、出口组及其机器成员；稳定接入地址与候选投影属于规则发布的内部实现，不作为单独的日常业务页面。后加机器和组权重调整应自动同步，不重复维护候选清单。

新建设备组只接受 `ENTRY` 和 `EXIT`。早期原型中的 `EDGE`、`HYBRID` 仅用于读取历史数据，不能继续创建、授权或用于新规则。

网关对每个新建 TCP 连接从健康候选中选择一台机器，支持加权轮询、加权最少连接和 Rendezvous Hash。排空、离线或健康租约过期的机器不接新连接。现有长连接固定在原机器；机器宕机后的旧连接需要客户端重连。连接复用不会按每个网站请求重新轮询。

每条规则都必须选择入口/转发组。`DIRECT` 表示该组接入后由组内所选机器直接访问目标，不再经过独立出口组；`EXIT_GROUP` 表示入口组接入后还要经过一个出口组。二者描述的是规则出站路径，不代表底层承载一定是公网或专线。当前 L4 代理方案包含一个数据面网关跳数，网关不在面板进程中。DNS 多 A 记录不提供严格逐连接权重；生产仍需要网关高可用。

## 当前实现边界

| 模块 | 已实现 | 尚待完成 |
|---|---|---|
| 控制面 | 管理员登录、一次性注册、心跳、设备组、候选自动投影、配置代数与应用确认；PostgreSQL 事务快照持久化 | 完整关系仓储与迁移、快照增长治理、完整 RBAC/MFA、生产注册限流与证书体系 |
| 管理端 | 主页、转发规则、用户、用户组、节点和设备组；管理员代客户创建规则时先独立选择客户，规则表单内不切换归属 | 计量展示闭环、正式导入和完整运维操作；套餐与订单明确排除 |
| Agent | 独立心跳和配置循环、校验、原子文件、回读、回滚、last-known-good；可显式选择 Xray 或 GOST 3.3.1 独立进程适配 | 默认仍为 dry-run；Xray/GOST 混合配置尚无复合进程管理，真实引擎激活和协议探测尚未完成多机验收，mTLS 待完成 |
| 连接网关 | 单 TCP 入口、共享调度器、连接预留释放、半关闭、健康租约及可选 TCP 自动探测摘除恢复 | 控制面动态配置接入、完整协议/出口探测、双活和生产压测 |
| VLESS | TLS/Vision 与 Reality 的配置编译、DIRECT 或认证 SOCKS5 落地实验；真实 Xray 回环验证 | 完整身份和秘密分发、规则开通后单 URI 交付、全组撤销及真实多机验收 |
| 离线预检 | 本地 JSON/ZIP 结构检查、快照哈希、脱敏报告、证据适配器门槛；不写源数据 | 真实 NY 原生 schema 适配器、正式迁移与对账 |

历史端点占位接口已从 HTTP 表面移除。单条 VLESS URI 应是规则激活后的连接信息，经授权的客户连接接口交付；编译后的配置与 URI 包含凭据，不可放入普通列表、日志或审计详情。

节点心跳与协议可用性分开。当前控制面尚无协议健康写入通道，因此新候选的 `healthy_candidate_count` 为 0；候选数量增加不等于业务已连通。网关的可选 TCP 探测只对已获授权且租约有效的候选做额外否决，连续恢复成功后才解除否决，不延长租约、不把排空/离线成员升为可用。真实 Xray 测试已验证进程停止后的自动摘除，但完整 VLESS 认证和目标出口探测仍未实现。

控制面已接入 PostgreSQL transactional snapshot（事务快照）适配器，用于单实例试运行。设置 `CONTROL_DATABASE_URL_FILE` 或 `CONTROL_DATABASE_URL` 之一即可启用；公网部署保持 `CONTROL_ALLOW_VOLATILE_STORE=false`。每次操作读取完整状态，写操作通过行锁事务写回，快照上限为 16 MiB。这提供重启后的持久化，但尚不是完整关系仓储或高吞吐数据库方案；`migrations/` 中的关系模式仍未接入，当前快照部署不要执行这些迁移。未配置数据库时只有显式设置 `CONTROL_ALLOW_VOLATILE_STORE=true` 才允许本地易失内存模式。

自动创建 VLESS + Reality + Vision 规则时，控制面需预先配置 `CONTROL_REALITY_SERVER_NAME` 和 `CONTROL_REALITY_DESTINATION`（`host:port`）。目标须由运维在实际入口节点核验；未配置时自动创建返回校验错误，已有人工参数规则仍可使用。每条规则的 X25519 密钥对和 Short ID 由服务端生成，私钥仅保存在受保护的事务快照并下发给对应节点，不进入规则 API 或审计。

独立新机的 HTTPS、真实管理员登录、重启后会话保持、隔离 PostgreSQL 测试及备份恢复已验证；当前控制台按六个正常业务页面做桌面、窄屏和手机浏览器验收。这些不代表客户转发能力已开通。具体步骤和边界见 [部署手册](deploy/README.md)、[备份恢复手册](deploy/backup-and-restore.md)；按阶段区分的验证证据见 [验证记录](docs/verification-20261002.md)。

## 本地验证

工具链：Go 1.27 或更高版本、兼容 Vite 8 的 Node.js。

```text
go test ./...
go vet ./...
cd apps/web-admin
npm ci
npm run typecheck
npm test
npm run build
```

普通 Go 测试不下载引擎、不访问生产。真实 Xray 测试默认跳过；设置 `NYVP_XRAY_BINARY` 为已校验的本机绝对路径后运行：

```text
go test ./tests/integration/xray -run TestSingleVLESSURI -v -count=1
```

集成测试会在回环地址启动两个真实 Xray TLS 后端、一个 TCP 网关和一个 Xray SOCKS 客户端，使用临时证书验证单 URI 的请求分配与摘除后连通，退出时关闭进程。覆盖 DIRECT 普通模式、DIRECT Vision、认证 SOCKS5 落地三条路径；错误证书/SNI 和错误落地凭据被拒绝。测试不关闭 TLS 证书验证，使用临时 CA 作为可信根；不打印凭据或引擎配置。

真实 GOST 3.3.1 测试同样默认跳过；设置 `NYVP_GOST_BINARY` 为已校验的本机绝对路径后运行：

```text
go test ./tests/integration/gost -v -count=1
```

测试只在本机回环启动 GOST 和临时 TCP 目标，验证结构化 DIRECT 规则的轮询/随机/IP 哈希分配，以及 Agent 进程适配器的启动、转发、存活检查和停止。GOST 模式与 Xray 模式目前分别运行，混合配置会被拒绝；这不代表现网 NY 转发或跨机设备组流程已接通。

已选验证内核：上游 XTLS/Xray-core `v26.3.27`。Windows x64 官方 ZIP 的 SHA-256：

```text
d004c39288ce9ada487c6f398c7c545f7d749e44bdfdd59dbc9f865afba4e1ad
```

资产与摘要来源：[官方固定版本](https://github.com/XTLS/Xray-core/releases/tag/v26.3.27)。其他平台必须核对对应资产摘要，不能复用本摘要。

启动配置见 [控制面](apps/control-api/README.md)、[Agent](apps/edge-agent/README.md)、[管理端](apps/web-admin/README.md)。接口语义见 [API 契约](docs/api-contract.md)，代码分层见 [模块边界](docs/module-boundaries.md)，NY 本地快照规则见 [离线导入预检契约](docs/ny-import-contract.md)。本轮实际验证与限制见 [验证记录](docs/verification-20261002.md)。

## 后续接通顺序

1. 在已接入的 PostgreSQL 事务快照基础上完成关系仓储、迁移兼容与回滚、快照增长治理。
2. 客户身份、秘密存储、规则连接信息的单 URI 签发与全组撤销；不实现独立订阅业务。
3. 原生配置编译接入节点完整配置，Agent 受控启动/更新 Xray。
4. 协议与实际出口探测、控制面版本化网关发布；在现有 TCP 自动摘除基础上补齐业务可用性判断。
5. GOST 传统转发、NY 只读导入、计量限额和完整管理流程。
6. 隔离环境多机验收、网关高可用，再安排有备份和回滚包的生产灰度。

当前产物尚不能替换生产 NY。新机部署与三个受保护旧服务隔离，未改动旧面板及其生产账号、服务器配置、DNS 或线路。
