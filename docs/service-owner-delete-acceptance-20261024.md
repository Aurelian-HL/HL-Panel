# 删除联动修复与验收（2026-10-24）

## 故障与范围

事故记录显示，单条规则删除触发旧 Agent 的完整 Xray 重启；HT 随后调用
`DELETE /api/v1/service-owners/{id}` 收到 404，因为运行版本未实现该接口。
规则已删除与身份清理未完成是两个独立步骤，不能将后者失败误判为批量误删。

本轮只修改 HL，HT/issuer 生产保持只读，未重试历史客户删除。测试全部使用
本机回环或隔离 CI，不能用它们代替生产升级与公网验收。

## 实际修改

- 节点按 tag/name 热更 Xray/GOST；无法安全应用就拒绝新配置并保留活进程，
  不再自动退回完整重启。Mixed 预检、失败补偿、不确定状态与死进程恢复见
  [节点验收记录](rule-live-update-acceptance-20261024.md)。
- 注册管理员专用 `DELETE /api/v1/service-owners/{id}`；必需 revision、
  Idempotency-Key，记录 `service_owner.delete` 审计。
- 服务身份仍有面板登录、规则、订阅或未撤销 VLESS identity/runtime 时返回
  409。无引用时仅删除该身份及残留会话，不删其他对象，不触发节点重新编译。
- PostgreSQL 中删除、审计、重放记录在同一事务提交；事务提交失败不发布缓存。
- 明确 `/customers/{id}` 仅撤销面板登录，`/service-owners/{id}` 才删除无引用
  客户服务身份；二者均不代替 VLESS identity 撤销。

生产逻辑文件位于 `internal/control/customers/service_owner_deletion.go`、
`internal/control/httpapi/service_owner_handlers.go`、
`internal/control/httpapi/business_routes.go`、
`internal/control/memoryrepo/service_owner_deletion.go`、
`internal/control/postgressnapshot/service_owner_deletion.go`。

## 已实际通过的本地验证

2026-10-24，Windows、Go 1.27.0，Xray 固定 26.7.28：

1. `go test ./...`：通过。最终日志 `full-test-final.log`；真实 Xray 集成
   61.143 秒。该命令未配置 GOST/PostgreSQL 外部夹具，条件跳过项另行专项验收。
2. `go test ./internal/control/httpapi -run TestServiceOwnerDeletion -count=1 -v`：
   两项通过。覆盖 401/400/404/409、幂等重试，以及真实 HTTP 创建两条目标规则、
   分批删除再删身份；另一客户规则、已发布订阅（含 token）及 VLESS 凭据完整比较不变。
3. `go test ./internal/control/memoryrepo -run TestServiceOwnerDeletion -count=1 -v`：
   三项及引用拒绝子场景通过。覆盖同组其他客户、两类规则/订阅引用、登录保护、
   未撤销身份保护、快照恢复、持久重放；规则撤回仅增加一次 generation，后续身份
   删除及重放不增加 generation，也不改变其他业务快照字段。
4. 本轮新建 PostgreSQL 16.15，数据目录为工作空间 `work/service-owner-pg-20261024`，
   仅 `127.0.0.1:55484`，数据库 `nyvp_panel_quota_test`。
   `go test ./internal/control/postgressnapshot -run 'TestPostgreSQL(ServiceOwner|CustomerDeletion)' -count=1 -v`：
   三项通过，1.944 秒。延迟提交故障注入后快照/版本/缓存不变；同请求重试、重开库
   后删除保持、会话保持、持久幂等通过。快照格式仍为 25。
5. 控制面新增删除专项竞态检测通过：`go test -race` 覆盖 customers、memoryrepo、
   httpapi、postgressnapshot（未为此竞态命令配置 PostgreSQL，真实数据库专项见第 4 项）。
6. `go vet ./...`、`GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build ./...`、
   Go 格式检查和 `git diff --check` 通过。交叉编译不等于 Linux 运行验收。

日志在仓库本地 `artifacts/live-update-20261024/`，由 Git 忽略。没有把日志中的
条件 SKIP 当作功能通过；Windows 可靠 UDP chain 三个子用例仍需 Linux 补验。

## Linux 验收门槛与升级边界

`.github/workflows/rule-deletion-live.yml` 使用隔离 Ubuntu、临时 PostgreSQL、
固定 Xray 和正式 recipe 的 GOST，检查真实旧 socket、PID、CounterEpoch、
可靠 UDP chain success/DELETE 后失败/POST 后失败、身份删除事务。要求这些
测试实际 PASS，任何 SKIP 或必需测试缺失都判为失败。该流程无发布/部署步骤。

面板新接口上线只解决缺失路由；节点要升级新 Agent 才能消除旧重启路径。
不修改客户 UUID/订阅地址，不升级 Xray 核心。Agent 升级自身仍有进程切换窗口，
不能承诺这次升级动作保留现有长连接。后续日常安全热更保留无关连接；被删除或
替换的规则自身、共享依赖的规则连接可能中断，Mixed 也不是跨进程原子事务。

## 历史半完成删除与回滚

不得自动重试生产客户删除。新版部署后先读 exact owner ID、kind、revision 和
剩余引用，确认目标无误后只继续未完成的身份步骤；不重复删除其他规则。
若 owner 已不存在，应核对审计/同请求重放，不能将所有 404 都当成功。

本轮未变更生产，无生产数据回滚动作。代码回滚可还原本轮 Git 提交；正式升级
前应保留一份上一版程序与配置备份。Agent 回滚到旧版会重新引入日常重启风险，
不应把旧版的“下发成功”当作连接稳定性验收。
