# 当前实现与 NY + VLESS 目标的差距

检查日期：2026-10-02。范围：本地工作区源码、路由、表单、存储、Agent、网关和测试代码。没有访问或修改旧站、服务器、DNS、生产数据。本报告不以 README 的功能陈述或历史测试数量代替实际调用链。

结论：当前交付是控制面第一阶段原型，不是保留 NY 完整业务体系的面板。用户看到的四个业务页面就是现有范围；转发规则、用户授权、配额、可执行的节点安装、真实 NY 导入及客户 VLESS 开通链路均未完成。已有编译器、调度器和回环实验具有继续开发价值，但不能作为面板功能已交付的依据。

## 判定口径

- 不存在：当前运行入口没有对应领域、API 和页面；不等于技术不可实现。
- 模型、库或 CLI：存在局部实现或独立程序，尚未接入管理员完整操作流程。
- 页面及 API：可读写控制面对象，不代表实际转发生效。
- 数据面闭环：管理员操作能下发真实进程、客户连接能使用，并有故障与权限验证；本次仅静态读取，未重新执行远程或协议测试，因此不作新增线上通过声明。

## 功能矩阵

| 用户需要的能力 | 当前分类 | 实际已有内容 | 缺口与源码证据 |
| --- | --- | --- | --- |
| 管理员登录、节点/组总览 | 页面及 API | 登录、Bearer 会话、总览计数 | Vue 仅登录加四页，见 `apps/web-admin/src/router.ts:15`、`:21`；API 全量路由见 `internal/control/httpapi/router.go:39` |
| 添加机器、一键安装 | 注册 API 有；安装不存在 | 一次性令牌、Agent 自行注册与心跳 | “注册节点”仅打开令牌弹窗，没有安装命令、可下载 Agent 包、安装结果流程；`apps/web-admin/src/pages/NodesPage.vue:53`、`features/enrollment/CreateEnrollmentTokenDialog.vue:56`。`deploy/` 只有 control-api systemd，未提供 edge-agent 安装部署资产 |
| 设备组、入口/出口角色 | 页面及 API | ENTRY/EXIT/EDGE/HYBRID 类型、成员权重/优先级、组修订 | 角色字段并非入口→出口拓扑；没有线路/隧道连接对象。见 `internal/control/groups/model.go:14`、`:29` |
| 入口→出口转发规则 | 不存在；原始配置发布只属技术工具 | 组可发布任意 JSON 配置片段 | 无目标 IP/端口、协议、入口组、出口组、启停、复制、修改规则表单/API。发布弹窗要求操作者手写 `schema_version/services` JSON，`features/group-revisions/PublishGroupRevisionDialog.vue:12`、`:72` |
| SOCKS5 落地管理与格式导入 | 编译库局部存在 | VLESS egress 编译器能使用认证 SOCKS5 | 无落地资源 CRUD、导入粘贴框、凭据托管或线路绑定 API；`internal/provisioning/vless/compiler.go:15` 仅接受调用者传入的 egress |
| 用户、授权、到期、限速/流量/连接数限制 | 不存在 | 只有管理员身份与节点身份 | `internal/control/httpapi/router.go:39` 至 `:56` 为全量接口，没有客户域、配额域；`internal/control/auth/model.go` 管理员不是客户。当前不能给客户授权、到期停用或真实限额 |
| 组内加权轮询、故障摘除 | 模型及独立网关，有回环实验代码 | 调度器、TCP 代理、健康探测、版本化候选文件 | 网关入口只读本地成员 JSON，没有连接控制 API 的源；`apps/connection-gateway/main.go:38`。创建组和端点不会自动部署网关/监听端口 |
| 单一稳定 VLESS 链接 | 编译库；面板订阅为占位 | `vless.URI()` 生成一条 URI，编译 Xray TLS/Vision 配置 | 面板订阅接口明确返回 `identity_binding_required`，`internal/control/endpoints/service.go:119`。没有客户 UUID、证书、凭据签发和节点装载闭环 |
| VLESS 服务开通表单 | 页面及 API 仅元数据 | 名称、设备组、VLESS/SOCKS5 标签、域名端口、调度策略 | 表单没有客户、TLS、入站身份、出站选择，也未调用 VLESS 编译器；`features/endpoint-pools/CreateEndpointPoolDialog.vue:55`。端点列表无实际链接按钮，`features/endpoint-pools/EndpointPoolInventory.vue:23` |
| Agent 启动/更新 Xray 或 GOST | 只有文件模拟 | 拉配置、校验 JSON/hash、写文件、回报 generation | runtime 硬接 `DryRunFileAdapter`，`internal/agent/runtime/runtime.go:31`、`:52`；Commit/Verify 只落盘/比对文件，`internal/agent/engine/file_adapter.go:66`、`:76`。没有真实引擎校验、启动、重载、端口验收 |
| 协议健康回报和在线流量监控 | 部分模型；闭环不存在 | 节点心跳资源字典、候选健康字段、网关 TCP 探测 | 控制面 `LastHealthAt` 无生产写入入口；创建候选不填健康时间，`memoryrepo/endpoints_repository.go:47`，而资格判定要求非空，`endpoints/model.go:84`。心跳在线不等于协议健康 |
| NY 导入 | CLI 预检框架 | 本地 JSON/ZIP 格式与安全检查、脱敏报告 | CLI `ny.NewInspector(ny.Options{})` 未注册任何真实版本适配器，`internal/importers/ny/previewcmd/run.go:57`；缺 adapter 返回 unsupported，`preflight.go:220`。无 UI/API、映射预览、落库、冲突处理或回滚 |
| 持久化 | PostgreSQL 快照可用 | 单实例事务保存内存仓库快照 | 不是已落实的规范化生产仓储；`internal/control/postgressnapshot/store.go:1`、`:53`。迁移 SQL 存在并不表示应用按关系表读写 |
| 审计、系统管理 | 事件底座；产品功能不全 | 变更记录内部审计事件 | 全量路由没有审计查询、角色权限配置、系统设置、引擎更新、维护任务等 API；不能当作运营管理功能已交付 |

表中 `features/` 相对目录为 `apps/web-admin/src/features/`；`memoryrepo/`、`endpoints/` 相对目录为 `internal/control/`。

## 为什么页面创建成功仍不能转发

1. 设备组保存的是角色和成员关系，不包含入口与出口线路规则。
2. 服务端点保存的是域名、协议标签和候选投影；不会自动申请证书、创建客户身份或配置远端监听。
3. 组修订将人工 JSON 原样封装为节点 bundle，`internal/control/memoryrepo/node_bundle_compiler.go:25`，不会从业务线路编译可执行转发配置。
4. Agent 收到 bundle 只执行文件适配器；“已应用”最多证明文件已保存，不能证明端口或转发可用。
5. 独立网关读取另一个本地文件；面板端点成员、健康与网关之间没有同步闭环。
6. VLESS URI 与 Xray 配置库没有生产调用者；源码检索 `provisioning/vless` 的非测试调用未发现控制 API 接入。客户订阅接口甚至未暴露在前端 `AdminApi` 中（`apps/web-admin/src/api/index.ts:42`）。

## 已有协议实验能证明什么

`tests/integration/xray/single_endpoint_test.go:49` 包含真实 Xray 双后端、DIRECT/Vision/SOCKS5 落地路径测试；`:74` 直接在测试内构造 Profile，`:87` 直接调用编译器，`:93` 由测试启动 Xray，`:95` 直接构造成员与健康租约。它能验证选定协议与调度技术路径，却没有经过面板创建客户、线路发布、Agent 进程激活。这不是“NY 面板业务闭环测试”。本次未重新运行该测试，不更新历史测试成绩。

## 应调整的实现顺序与验收门槛

1. 先建立 NY 功能基线：以合法只读文档、实际界面和离线导出样本列出机器、设备组、入口/出口、线路规则、落地、用户授权、流量/期限、监控、导入、运维能力；记录明确差异与优先级。不能以页面名称相同视为功能兼容。
2. 第一完整业务闭环：机器安装→节点在线→入口/出口或直入直出组→结构化规则→真实 GOST/Xray 校验启动→实际连接→禁用/回滚。先让管理员能正常创建、修改、停用一条线路。
3. 第二闭环：客户→授权及限额→选择设备组与协议→凭据/证书分发→一个稳定 URI→网关自动成员同步与健康摘除。不可把多台候选展开为十条订阅。
4. NY 导入必须有真实版本样本、字段映射、预检、可回滚提交，以及导入后真实规则验证；没有版本适配器时应明确显示“尚不支持导入”。
5. 最终验收应覆盖真实管理页面和客户连接，含无数据、错误、修改、删除/停用、权限、限额、故障摘除、恢复。回环技术实验通过不能替代公网业务验收。

额外架构边界：当前方案在设备组前加入独立 L4 网关。它能提供稳定入口和每个新连接的服务端选择，但增加了一个流量经过的入口和单点。若要求客户直接连接候选机器且仍只使用一个域名，需要明确入口高可用/调度方式；多 A 记录本身不能承诺每连接精确加权轮询或即时故障摘除。

本次实际操作：读取上述源码与配置，检索所有路由/调用者/健康字段、检查 `git status --short`，新增本报告。未修改应用实现，未部署，未操作任一线上站点。
