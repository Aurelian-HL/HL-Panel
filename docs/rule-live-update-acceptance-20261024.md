# 日常规则操作不重启活引擎：实现与验收（2026-10-24）

## 目标与原问题

日常新增、删除、暂停、恢复、编辑规则以及失败补偿，不得通过停止、替换仍在运行的 Xray/GOST 来应用配置。旧实现主要支持纯新增热更，其他变更可能退回完整 Stop/Start，因此操作一条规则会影响同一引擎的其他客户。

修复提交 `15ba5fa8e38426f2e6926cc0b4c407b5d8e1a1bf` 已推送 `fix/rule-deletion-live-20261024`。本地与 Linux 隔离专项均通过；本记录编写时尚未发布或升级生产。生产节点须升级 Agent 后才获得新行为。

## 已实现策略

- Xray 保留纯新增快速路径，增加按 tag 热更 inbound、outbound 和 routing。要求全局设置兼容、控制 API 可用、路由影响范围可确定；按需替换变化的 handler。
- GOST 按 name 热更 service、chain；chain 变化更新依赖它的 service，保留无关 service。
- 活进程不满足安全热更条件时，返回 `ErrLiveUpdateRequiresMaintenance` 并保留进程。普通提交和非空目标回滚不再以 Stop/Start 兜底。
- 对确定失败尝试局部补偿；RPC 结果不确定、补偿失败或文件恢复失败时，锁定 `liveStateUncertain` 并返回 `ErrLiveStateUncertain`。保留仍在运行的进程，拒绝继续提交、非空目标回滚和成功校验，避免错误宣告新配置生效。
- Reconciler 在下发与恢复入口检查锁定状态；反复下发不会反复准备、校验或干预已经锁定的活引擎。
- Prepare/Validate 失败时只清理未提交准备状态并报失败，不调用引擎回滚。覆盖有/无最后可用配置，避免尚未应用就误停正在服务的进程。
- Mixed 在首个引擎变更前预检两端；运行状态包含仅一个引擎幸存的情形，恢复失败不能以 `Rollback(nil)` 停掉幸存引擎。
- Mixed 删除某个引擎最后一个配置分片时，热清客户监听并保留空闲进程；回滚需要时补齐空闲组件的准备配置，不停止该组件。
- Xray 空闲投影保留原有 log、policy、routing 全局设置，清空客户 inbound/outbound/routing rules，由编译器重建自己的 API 单例。内部 `local-idle-xray` 分片仅服务本地编译、校验、回滚，控制面原始 Desired、canonicalConfig、ConfigSHA256 身份不变。发现活动文件漂移时拒绝热更。
- Close 仅在 Stop 成功后释放进程引用；错误包装保留维护/不确定错误身份。

真正已经退出的引擎允许从最后可用配置恢复，成功启动后清除不确定状态。首次启动失败的新进程仍需清理。正式 Agent 更新、管理员主动停止/重启、进程自身崩溃恢复属于独立生命周期操作，保留原有语义。

## 修改文件

生产逻辑：

- `internal/agent/engine/xray_live_update.go`、`gost_live_update.go`：差异规划、热更和局部补偿。
- `internal/agent/engine/live_policy.go`：保留活进程的错误约定、存活状态、Mixed 预检。
- `internal/agent/engine/xray_idle.go`：最后分片删除后的空闲投影。
- `internal/agent/engine/xray_adapter.go`、`gost_adapter.go`、`mixed_adapter.go`：提交、校验、回滚、生命周期保护。
- `internal/agent/engine/xray_live_add.go`、`gost_live_add.go`：既有快速路径接入及补偿。
- `internal/agent/reconciler/reconciler.go`：准备/校验失败不回滚，保护不确定状态与幸存引擎。
- `internal/agent/runtime/engine_recovery.go`：恢复失败保留幸存引擎。

相应测试位于上述包中的 `*_test.go`，另包括 `tests/integration/gost/mixed_stability_test.go`、`tests/integration/xray/live_add_test.go`、`tests/integration/xray/live_update_test.go`。

## 环境与方法

Windows 本机回环及 Ubuntu 24.04 隔离 CI，Go 1.27.0；未向生产注入故障或发送测试配置。

- Xray 固定 **26.7.28**，未升级核心。
- GOST 为固定源码 `e9fab9587288b82ce8a1a9b8f07326f41b155935` 的 Windows 3.3.1，依赖 go-gost/x 0.17.2；不具备正式 Linux 可靠 UDP 补丁版能力。
- 真实测试检查 PID、CounterEpoch，持续使用同一个 TCP socket 收发，不能用重新连接成功代替旧连接保留验收。
- 日志保存在仓库本地 `artifacts/live-update-20261024/`，由 Git 忽略，不属于发行产物。

## 已完成的本地验证

1. `go test ./...` 通过，日志 `full-test.log`。设置真实 Xray 二进制，移除 GOST 外部二进制变量，需要它的测试按条件跳过。最终 engine 包使用缓存，reconciler 3.270 秒、runtime 16.201 秒、Xray 集成 60.604 秒。
2. 真实 Xray/GOST/Mixed 专项重新执行通过，日志 `live-engines.log`。三个包分别为 6.310、3.506、8.858 秒；可靠 UDP chain 三个子用例明确跳过，未计为通过。
3. Xray 覆盖删、停、恢复、编辑、回滚、最后规则删除、部分 API 成功后失败、绑定冲突、缺 API 拒绝、结果不确定、补偿失败。正常变更保留的同一 TCP 会话完成 377 次收发；部分失败场景完成 158–215 次；不确定/补偿失败场景完成 52/62 次。
4. GOST 覆盖普通 service 删除、暂停、恢复、修改、回滚、最后规则删除、部分失败、端口冲突。旧 TCP 会话单场景完成 32–96 次收发，不确定/补偿失败场景完成 41/52 次。
5. Mixed 专项 3.49 秒通过：覆盖两端最后客户监听清空、重复空配置、恢复和原回滚场景，另一引擎原 socket 持续通信，PID、CounterEpoch 不变。空闲 Xray 场景带 log、policy、domainStrategy，验证全局设置保留。
6. `go test -race ./internal/agent/engine ./internal/agent/reconciler ./internal/agent/runtime` 通过，日志 `race.log`，分别为 53.281/6.684/6.946 秒。最后准备/校验失败修复后再运行 reconciler/runtime 竞态检测通过，日志 `race-final.log`，分别为 4.529/5.469 秒；engine 代码在这两轮之间未再修改。
7. `go vet ./...` 通过，日志 `vet.log`。
8. `GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build ./...` 通过，日志 `linux-build.log`。这是交叉编译，不是 Linux 运行验收。
9. Go 文件格式及 `git diff --check` 通过。

真实专项命令（设置两个真实二进制变量后）：

```text
go test -p 1 ./internal/agent/engine ./tests/integration/gost ./tests/integration/xray -run 'TestGOSTRealLive|TestMixedEngineUpdatePreservesExistingOtherEngineConnection|TestXrayLive' -count=1 -v
```

## 失败尝试与未验收项

- 首次双二进制全仓测试中，可靠 UDP API 的 quic/kcp 场景因本机 GOST 能力不足失败，未伪造版本或降低能力校验，不计为已通过。
- Windows 可靠 UDP chain 三个子用例因夹具能力不足跳过，随后在 Linux 正式 recipe 补丁版 GOST 上全部实际通过，见下节。
- 首次跨包并行运行 GOST 测试争用 `127.0.0.1:18090`，导致启动失败；使用 `-p 1` 串行后通过。
- 开发期间夹具缺显式回退路由/全局设置、路由故障注入未实际改变路由、旧重启路径断言不适用等问题，均修正后重新实跑。准备阶段失败用例曾错误断言 Validate，已按实际 Prepare 阶段纠正并完成全仓及竞态重测。
- 尚未进行本次新版生产升级或生产客户公网验收。

## 已完成的 Linux 隔离验证

GitHub Actions [40009597595](https://github.com/Aurelian-HL/HL-Panel/actions/runs/40009597595) 对修复提交执行成功。Ubuntu 24.04、PostgreSQL 17.11、固定 Xray 26.7.28 和正式 recipe 补丁版 GOST；专项 80 个 pass 事件、0 fail、0 skip，工作流强制检查必需测试不得跳过或缺失。

可靠 UDP chain success、DELETE chain 后失败、POST chain 后失败全部通过；Xray 删除/暂停/编辑/回滚与补偿失败、Mixed 保留另一引擎原连接、身份删除与事务提交失败回滚全部通过。真实旧 TCP socket 持续收发，PID 和 CounterEpoch 保持不变。engine/reconciler/runtime 与控制面竞态检测、恢复专项、vet 均通过。

下载证据压缩包 SHA256 为 `beb2f7fbb8dd989f6f5465e3fe605bb4de833f02e76948cde300045bb9f3da9a`；已核验并解析到本地 `artifacts/live-update-20261024/linux/`，包含测试 JSONL、race 日志、引擎版本和 `summary.json`。

## 影响边界与交付

不重启引擎不等于所有连接绝对不受影响：被删除或替换的规则自身、共享出站/chain 的依赖规则，其连接可能中断。Mixed 两个引擎热更不是跨进程原子事务；预检和补偿降低失败影响，不能描述为原子切换。

全局设置变化、旧配置缺 API/明确路由、文件漂移、结果不确定时，保留活进程并明确失败/需要维护，不自动重启解决。不确定状态锁定后，新变更会等待维护，不能标记已生效。

本轮未改变客户 UUID、订阅 URI、持久数据模型、核心版本。代码已提交推送，本记录编写时尚未发布、升级生产或执行生产回滚。新行为需要新 Agent 升级到节点后才会生效，单独更新面板不能改变旧 Agent。

Linux 与可靠 UDP 验收已完成；后续交付生成独立新版本，保留上一版 Agent 和配置备份，再按授权升级，不覆盖旧 tag 或旧发行包。
