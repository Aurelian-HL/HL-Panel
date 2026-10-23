# 日常操作不断核心：本地修复与验收记录

日期：2026-10-24。基线：`0084823925673fc7b8936855be554a7a137a5f87`，分支：`fix/rule-deletion-live-20261024`。

本轮修改仍在本地工作区，未提交、未发布、未升级生产。未访问客户服务器进行故障注入。

## 结论与验收口径

目标是日常配置修改不重启正在转发的核心，并保护不相关客户连接。必须同时检查原 TCP socket 持续收发，以及变更期间新连接获得完整目标响应；只检查 PID、TCP 建连或配置文件不能证明不断流。

本轮修复了出口替换的先删后建空窗、失败补偿的相同问题、回滚记录缺失时误停已有核心的路径，并取消路由未变化时的补偿重载。后续排查又修复了过早失败时补偿尚未执行的配置，以及 GOST 查询缺少字段时误判删除成功。真实 Xray、GOST 回环测试通过相应覆盖；异常查询响应另由故障注入回归覆盖。

不能据此承诺“任何操作、任何时刻、所有连接绝对不中断”。固定核心的路由更新仍有原子性边界，GOST 关联服务替换仍有窗口，Agent 升级仍停止其所拥有的核心。删除或撤销某个客户、修改该客户监听端口、主动停机也不能同时承诺该客户继续使用原入口。

## 本地实现

### 出口先创建，再切换，再清理

受管 VLESS 编译器及受管导入编译器，为 freedom/socks/blackhole 叶子出口增加内容版本 tag。出口地址、凭据等内容变化时，新旧 tag 不同；入站、UUID、Reality 握手配置、监听端口、客户 URI 不因出口版本变化而改变。计量 tag 的 `--` 后元数据保持原值。

正向顺序：添加候选出口 → 切路由 → 清理旧出口。

确认失败后的补偿顺序：恢复旧出口 → 恢复旧路由 → 清理候选出口。路由完全未变时，正向与补偿均不调用全量路由替换。

不全局重写任意 Agent JSON。对链式出口、未知扩展、balancer、未知路由引用不进行猜测式版本化；Agent 拒绝同 tag 的出口替换并保留运行进程。

主要文件：

- `internal/xrayconfig/outbound_versions.go`
- `internal/control/forwarding/vlessconfig/compiler.go`
- `internal/control/importedconfig/native_runtime.go`
- `internal/agent/engine/xray_live_update.go`

### 缺少回滚记录时保护存活数据面

Reconciler 在激活前检查：没有 durable LKG，但适配器仍有存活进程时，拒绝修改并报告失败。不得通过 `Rollback(nil)` 清理原有转发进程，不得虚报新配置已应用。

Mixed 适配器再独立保护同一边界：新 wrapper 没有完整 `lastGood` 时，即使只有一侧存活，也先拒绝激活。否则另一侧启动失败可能通过空回滚目标停掉仍正常的一侧。

真正无存活进程的首次安装仍允许启动；启动后验证失败仍清理本次未通过验证的新进程。已有可信 LKG 的局部恢复仍允许。

主要文件：

- `internal/agent/reconciler/reconciler.go`
- `internal/agent/engine/mixed_adapter.go`

### 保持兼容与计量

八个历史 v0.1.115 golden hash 不改期望值：测试仅去掉新增出口 tag 版本及其路由引用，再按原 SHA256 比较，约束其余原配置不变。另有独立测试确认出口改动不改变客户与探测入站。

新增计量测试覆盖不同版本出口归入同一规则，以及有 inbound 计量时优先 inbound，避免双计费。这是测试覆盖，未进行生产账单核算。

## 实测证据

所有日志在工作区 `outputs/HL-Panel-continuity-20261024/`。实际二进制为固定 Xray 26.7.28 和 GOST 3.3.1 Windows 版本；没有升级 Xray 版本。

### 真实 Xray：原连接与新连接

`go-test-final-all.log` 中下列 Xray 用例全部通过。该整轮命令的整体结果为失败，原因见后文，不能将它描述为整仓库全绿。表中数字均取自这一轮，未拼接多轮数据。

| 测试组 | 成功路径 | 添加出口后报错 | 切路由后报错 | 删除旧出口后报错 |
| --- | ---: | ---: | ---: | ---: |
| 普通出口：RPC 检查点 | 18 | 18 | 24 | 36 |
| 普通出口：后台成功新会话 | 578 | 799 | 885 | 1073 |
| VLESS + Reality Vision：RPC 检查点 | 12 | 12 | 16 | 24 |
| VLESS + Reality Vision：后台成功新会话 | 185 | 241 | 281 | 351 |

每个 RPC 检查点还分别对修改规则与不相关规则新建连接，要求完整收发、无重试。各场景的两条原 socket 持续收发；Reality 客户端进程及 URI 全程不变。上述两组共有 4393 次后台新会话，另有 320 次检查点同步新会话。

旧版无版本 tag 配置迁移也覆盖成功和三个失败阶段；缺失 durable LKG 的场景覆盖连续三次拒绝、零 mutation RPC、原连接与新连接继续转发。

相关测试：

- `tests/integration/xray/outbound_continuity_test.go`
- `tests/integration/xray/reality_continuity_test.go`
- `tests/integration/xray/live_update_test.go`

### 真实 Mixed 与 GOST

`mixed-real-final.log`：Xray/GOST 两方向各连续三次拒绝无回滚记录的 wrapper 激活，原 socket 与每次新建的连接均实际收发；缺失组件没有被误启动。随后有可信记录的原适配器正常恢复缺失组件。

`gost-live-isolated-final.log`：GOST 删除、禁用、修改、回滚、部分失败、删除最后规则后恢复，以及不确定失败保护的适用用例通过。依赖指定补丁 Linux 二进制的可靠 UDP 链用例明确跳过。该测试证明不相关服务连接保留，不证明被替换服务自身没有监听空窗。

### 先失败，再修复

- `mixed-missing-rollback-before.log`：修复前四个 Mixed 用例复现误清理已有配置。
- `unchanged-routing-before.log`：修复前确认失败与回滚失败两场景复现无变化路由仍被重载。
- `guards-routing-final.log`：上述相关保护及补偿测试在修复后通过。

### 最终回归

| 检查 | 结果及日志 |
| --- | --- |
| `go test ./... -count=1`，只设置真实 Xray 路径 | 退出 0；`go-test-final-supported.log`，Xray 集成 75.522 秒 |
| 六个改动相关包 `go test -race ... -count=1` | 退出 0；`go-race-last.log` |
| `go vet ./...` | 退出 0；`go-vet-last.log` |
| `go build ./...` | 退出 0；`go-build-last.log` |
| `git diff --check` | 退出 0 |

race 包为 xrayconfig、control/forwarding/vlessconfig、control/importedconfig、agent/engine、agent/reconciler、agent/usage。该检测针对 HL Go 代码，不是对外部 Xray 二进制进行 race 检测。

有环境依赖的 PostgreSQL、授权运行时、旧客户端、可靠 UDP 等用例存在跳过。没有苹果真机验收，也没有生产升级后验收。

同轮 Reality/Vision 的 TLS 1.3 突然关闭测试仍复现固定核心已有的 `bad record MAC` 及额外加密 alert。相关测试通过只表示预期检查通过，不能解释成该核心行为已修复；本轮未修改核心，也未把这一现象认定为本次配置热更新导致的生产故障。

### 整轮测试失败的解释与复核

同时为全仓库设置 Windows GOST 路径的 `go-test-final-all.log` 整体退出 1：

1. engine 包 GOST 启动失败。跨包运行的真实 GOST 都使用编译器固定计量地址 `127.0.0.1:18090`，存在竞争。相应用例随后独立运行通过，见 `gost-isolated.log` 与 `gost-live-isolated-final.log`。未通过修改生产计量地址绕过。
2. HTTP 可靠 UDP 用例拒绝该 Windows 二进制，因为它不是要求的锁定补丁 Linux 版本。此项仍未验收。

因此最终采用整仓库适用环境回归与单独真实 GOST 验收分别记录。完整可靠 UDP 接入测试必须另用锁定补丁 Linux 二进制，并串行执行共享固定端口的真实引擎测试。

## 继续排查：补偿必须依据实际执行进度

### 已复现并修复的问题

1. Xray 在 `rmi` 或 `ado` 阶段就失败时，旧补偿仍会按整个计划重载路由；同 tag 入站尚未删除就失败时，还可能把仍运行的原入站当作候选拆掉。现在在每个非空 RPC 前记录已尝试阶段，仅查询和补偿这些阶段；尚未尝试的路由不重载。纯新增与已提交事务回滚使用相同补偿逻辑，但明确区分其实际执行进度。
2. GOST 同批修改 A/B 两个服务，A 的首个 DELETE 失败时，旧补偿仍会查询、删除、重建尚未操作的 B。现在在每次 DELETE/POST 前记录对象种类和名称，补偿只处理已尝试对象。保留在请求前记录的语义，覆盖服务端已执行但返回失败的情况。
3. GOST 纯新增失败，补偿 DELETE 又失败时，旧逻辑将查询响应 `{}`、`{"code":0}`、`null` 误当作对象已不存在。现在只接受明确的 `data:null`；缺字段、无效 JSON、对象仍存在一律视为恢复未确认，保留存活核心，阻止继续提交或回滚，不再虚报 LKG 验证成功。

此处“保留存活核心”不表示所有候选修改均已恢复。状态不确定时需要核实真实运行状态，不能自动重启健康数据面来掩盖问题。

### 本次新增验收

| 检查 | 结果及证据 |
| --- | --- |
| Xray 五个 mutation 阶段的执行前/后失败 | `TestXrayFailureCompensatesOnlyAttemptedStages` 通过；早期失败不再执行额外路由重载或移除原入站 |
| 真实 Xray 早期失败五场景 | `early-xray-compensation.log`，退出 0，2.157 秒；原 socket 持续完整收发，24 个新会话检查点完整回包，无重试 |
| 真实 GOST 同批多规则首步失败 | `gost-attempted-compensation.log`，适用测试退出 0，5.133 秒；尚未执行的 B 不被 DELETE/POST，两条原 socket 持续收发，10 个新会话检查点完整回包 |
| GOST 缺字段错误先复现 | `gost-absence-before.log`，退出 1；缺 data、成功外壳无 data、顶层 null 三场景复现误判 |
| GOST 修复后的七种查询响应 | `gost-absence-after.log`，退出 0；缺字段、非法 JSON、仍存在均冻结进一步变更；明确 `data:null` 才认可补偿完成 |
| 全仓库回归，固定真实 Xray | `go-test-attempted-compensation.log`，退出 0，Xray 集成 76.787 秒；该轮在最后一项缺字段修复前运行 |
| 六个相关包竞态检查 | `go-race-attempted-compensation.log`，退出 0；该轮在最后一项缺字段修复前运行 |
| 静态检查与构建 | `go-vet-attempted-compensation.log`、`go-build-attempted-compensation.log`，均退出 0 |
| 缺字段修复后的全仓库最终回归 | `go-test-final-absence-proof.log`，退出 0，固定真实 Xray 集成 76.280 秒 |
| 缺字段修复后的引擎包竞态检查 | `go-race-final-absence-proof.log`，退出 0，57.553 秒 |
| 缺字段修复后的真实 GOST 独立回归 | `gost-live-final-absence-proof.log`，退出 0，3.265 秒；可靠 UDP 的三个 Linux 补丁包场景仍明确跳过 |
| 最终静态检查与构建 | `go-vet-final-absence-proof.log`、`go-build-final-absence-proof.log`，均退出 0；`git diff --check` 及相关文件 gofmt 检查通过 |

主要代码为 `internal/agent/engine/{xray_live_update.go,xray_live_add.go,xray_adapter.go,gost_live_update.go,gost_live_add.go}`；相关单元与真实引擎测试已扩充。测试仅在本地回环运行，Xray 保持 26.7.28。Windows GOST 无法执行要求锁定补丁 Linux 二进制的可靠 UDP 用例，三项明确跳过。

## 尚未关闭的边界

### 操作范围不能只按是否重启分类

| 配置或生命周期变化 | 当前处理 | 连续转发边界 |
| --- | --- | --- |
| 运行配置字节完全不变 | 可信存活进程直接保留，不执行配置切换 | 文件一致还必须与适配器持有的运行配置 hash 一致，不能仅凭磁盘文件认定已应用 |
| 新增、删除或禁用独立规则 | 通过受管 handler 与路由 API 修改，不以全核心重启兜底 | 删除或禁用的目标不继续开放；真实测试覆盖不相关规则的连接，路由并发边界仍见下文 |
| 修改叶子出口地址或凭据 | 内容版本出口先创建，再切路由，最后清理旧出口 | 原 socket 与新会话已做回环验证；仍非核心内部原子事务 |
| 改变同一 tag 的入站配置，例如握手参数或监听设置 | 从变更集合中先移除旧入站，后添加候选入站 | 同一监听的新连接可能遇到窗口，不能把核心未重启解释为该规则无感；尚未实现保留监听的完整入站热替换 |
| 同 tag 出口替换、无法规划的全局配置变化 | 拒绝在线应用，保留存活进程并报告维护错误 | 旧配置保留，不得虚报候选配置已生效 |
| 没有可信回滚记录，但已有核心存活 | 在激活前拒绝，不触碰已有数据面 | Reconciler 与 Mixed 两层保护已有真实引擎测试 |
| 状态不确定、补偿失败 | 保留存活进程，阻止进一步不可信修改 | 不等于证明先前每一项变化都已恢复；需人工核实真实运行状态 |
| Agent 升级、管理员显式停止或重启核心 | 当前生命周期仍会停止核心 | 未实现持续持有原 socket 的接管，不能承诺不断流 |

这张表按引擎配置变化分类，不替代各个业务操作的端到端验收。同一项业务编辑可能同时产生入站、出口和路由变化，必须以最终编译差异判断，不能只按按钮名称判定安全。

### 固定 Xray 的路由及出口查找不是一次原子事务

已读固定官方 tag 源码，下载文件、原始 URL 与 SHA256 见输出目录 `source-provenance.json`：

- `xray-v26.7.28-router.go:123`：`ReloadRules` 加写锁，全量替换时先置空规则，再逐条构建追加。
- 同文件 `:245`：`pickRouteInternal` 读取规则时未取得同一把锁。
- `xray-v26.7.28-dispatcher.go:456`、`:458`：先选路，再查找出口；无路由分支可选默认出口。
- `xray-v26.7.28-outbound.go:131`：移除 tag 不调用该 handler 的 `Close`，但后续按 tag 查找已不可见。

这意味着先添加出口可以消除确定性的先删后建空窗，但不能证明并发路由读取严格原子；已取到旧 tag 的派发与旧 handler 移除也没有统一事务。以上是源码审阅推导，未在本轮重现为实际转发失败，不应冒称已发现同样的生产事故。

单纯加固定 sleep 或延迟清理只能缩小概率，不能构成严格证明。本轮没有修改、重新编译或升级核心。

### GOST 依赖服务替换

`internal/agent/engine/gost_live_update.go` 会为变化的 chain 重建依赖 service，顺序仍为删除相关 service/chain，再添加新 chain/service。不相关服务保留，但同一被替换监听的新连接可能遇到窗口。本轮未实现 GOST 原子切换。

### Agent 生命周期仍拥有核心

`deploy/update-node.py:173`、`:256` 停止 Agent；Agent 退出及显式 restart/stop 路径调用 `closeEngines`。现有 systemd 服务也将子进程置于 Agent 服务生命周期内。节点升级当前仍会影响连接。

不能只设置 `KillMode=process` 或跳过 Close：那会引入无可信接管的残留进程、端口争用、双写和计量失配。

## 下一阶段实现约束

1. 将数据面交给独立常驻 supervisor/systemd 单元，生命周期与 Agent 解耦；Agent 只提交有界配置操作。supervisor 不提供任意远程 shell。
2. 通过本机受权限保护的控制通道接管，核实进程启动身份、二进制 hash、配置 hash、generation、协议版本和单写者身份；不得只凭 PID 文件认领进程。
3. 日常修改保留当前进程。应用状态不确定时停止进一步变更并报告真实状态，保留存量转发，不自动升级为全核心重启。
4. 路由需具备完整预构建后原子发布的读快照，且旧出口回收需与持有旧快照的派发协调。若要改固定核心实现，先做相同版本兼容与苹果真实客户端回归，不能暗中替换核心。
5. Agent 交接保持计量 epoch 与持久游标，重连后补采累积计数，避免因管理进程重启重复计费或漏计。
6. 端口修改、撤销身份、核心升级及机器故障单独定义语义：保留无关连接；撤销不继续放行；端口迁移不宣称原地址不变；核心和机器被停止时，不承诺原 socket 永远存活。

这些是后续架构约束，尚未实现，不属于本轮已交付能力。

## 回退与发布范围

本轮没有修改生产数据、订阅、客户服务器、DNS，也未发布新包。代码回退应仅针对本轮差异，不能用整库 reset 覆盖其他工作；测试和源码下载均位于隔离本地目录。

发布前仍需使用正式 Linux 引擎包完成可靠 UDP 与安装/升级验收、旧客户端及苹果真机兼容验收，再逐项核对真实应用回执与不相关连接。当前记录不能作为“生产已无感升级”的证明。

## 继续排查：GOST mutation 回执误报成功

本轮在本地确认另一处独立缺陷：`ExecGOSTRunner.GOSTAPI` 将响应 `code` 解码到 `int`，使缺字段、顶层 `null` 或 `code:null` 都得到零值。因此，HTTP 200 的 `{}`、`null`、`{"code":null}`、`{"data":null}` 会被 POST/DELETE 当作成功，可能推进与实际状态不一致的配置记录。该结论来自本地故障响应复现，不代表已确认相同生产事故。

修复使用可区分空值的 `*int`，要求 mutation 有明确成功标志：`code:0`，或没有有效数字 code 时的 `msg:"OK"`。明确非零 code、非 200 仍判为失败；缺少成功标志则返回完成状态不确定。GET 保持仅含 data 的合法响应兼容，具体状态仍由调用方验证。

实际 GOST 3.3.1 的成功回执使用空/缺 code 加 `msg:"OK"`。本轮曾用只接受数字 code 的初版检查运行真实回归，发现不兼容并在本地修正；最终版本已通过实际 POST/DELETE、服务恢复及完整转发响应验证。

新增八个状态测试覆盖 POST/DELETE 各四种缺少成功标志的响应，模拟目标操作实际完成后才返回该响应。验证：无关服务仍存在，目标 mutation 确实执行；不推进 active 文件和运行配置 hash；Commit、Rollback、Verify 均报告状态不确定；不继续 mutation，不停止原进程。该测试不宣称目标变更已自动恢复。

| 本轮检查 | 结果与证据，均在输出目录 `outputs/HL-Panel-continuity-20261024` |
| --- | --- |
| 修复前针对性复现 | `gost-mutation-envelope-before.log`，退出 1，POST/DELETE 的四种缺少成功标志响应均复现误判 |
| 24 个 mutation 响应场景、GET 兼容、八个 applied-state 场景 | `gost-mutation-envelope-after.log`，退出 0 |
| 最终真实 GOST 独立回归及上述状态场景 | `gost-live-mutation-envelope-final.log`，退出 0，5.629 秒；同 socket 连续收发、变更及补偿检查通过 |
| 全仓库 `go test ./... -count=1` | `go-test-mutation-envelope.log`，退出 0；未设置真实引擎路径，相关真实引擎用例跳过；真实 GOST 另行验证如上 |
| 引擎包 `go test -race ./internal/agent/engine -count=1` | `go-race-mutation-envelope.log`，退出 0，61.664 秒；未设置真实引擎路径 |
| `go vet ./...` 与 `go build ./...` | `go-vet-mutation-envelope.log`、`go-build-mutation-envelope.log`，均退出 0 |
| 格式与差异 | 三个 Go 文件已 gofmt；`git diff --check` 通过 |

本轮额外修改的文件是 `internal/agent/engine/gost_live_add.go`、`gost_live_add_test.go`、`gost_live_update_test.go` 以及本报告，绝对位置均在下方清单对应仓库中。工作区仍保留之前未提交修改。本轮未提交、未发布、未升级生产，未改变 Xray 26.7.28。Linux 补丁版可靠 UDP 的三个真实场景仍跳过，苹果真机和生产尚未验收；前文关于非原子切换、目标监听窗口及 Agent 升级生命周期的边界没有因此关闭。

## 再次复核：补偿状态与核心退出后的恢复

本轮重新读取 Mixed 的预检查、提交、失败补偿及回滚，Xray/GOST 的在线增删、状态不确定保护，Reconciler 的 Apply、恢复和失败路径，以及 ConditionalSource 的计量开关。未确认新的独立缺陷；没有因候选推测修改生产逻辑。

Mixed 的生命周期由 Reconciler 串行调用；不能仅凭 Mixed 自身没有覆盖整个提交过程的互斥锁认定生产并发缺陷。回滚失败后不设置完整引擎健康标志；对新启动侧补出空配置后再回滚的处理，也避免了仅因旧 bundle 未包含该侧就停止核心。以上为本轮源码核对结论，不能扩大为任意调用者并发使用都安全的承诺。

补充 `TestGOSTRealLiveUncertainStateRecoversAfterProcessExit`：使用真实 GOST 3.3.1 和本地回环，先实际应用目标修改再注入不确定回执，验证无关 TCP 连接仍能完整收发，原进程及计量 epoch 不变，保护状态存在。随后显式停止该隔离测试进程以模拟核心退出，验证保护不再阻断死进程恢复；重新提交最后可用配置后启动一个新 epoch，两条旧规则均能收到正确目标完整响应，失败候选端口不再监听。

此测试覆盖实际引擎适配器的恢复边界，不是 Reconciler 定时自愈的端到端测试。核心真正退出时原 socket 不能保留，本测试没有宣称该阶段不断连接，也没有更改核心生命周期或版本。

| 本轮重新执行 | 结果与证据，位于 `outputs/HL-Panel-continuity-20261024` |
| --- | --- |
| 真实 GOST `go test ./internal/agent/engine -run '^TestGOSTRealLive' -count=1 -v` | `gost-recheck-exit-recovery.log`，退出 0，4.449 秒；Linux 补丁版可靠 UDP 三个场景明确跳过 |
| Reconciler 与 usage 两包 `go test ... -count=1` | `reconciler-usage-recheck.log`，退出 0；分别 3.311 秒、0.200 秒 |
| Mixed、Xray/GOST 失败边界、新增真实退出恢复用例的针对性 `go test -race` | `engine-recheck-race.log`，退出 0，12.304 秒 |
| 格式与差异 | 修改的测试文件已 gofmt；`git diff --check` 通过 |

本轮仅新增上述测试并更新报告，没有新增业务代码修复，没有提交、发布或升级生产。前文的路由并发边界、目标监听替换窗口、Agent 升级中断，以及 Linux 可靠 UDP、苹果真机和生产未验收项仍然保留。

## 本次继续排查的实际文件位置

以下源码与测试文件均已读取；本次变更通过上述构建、回归或针对性测试检查。报告已实际写入并重新读取核查。目录及文件均真实存在：

- `C:/Users/hf/Documents/Codex/2026-10-16/codex-threads-01a10f3e-8cf5-7312-b6c9/work/hl-usage-throughput-20261021/internal/agent/engine/xray_live_update.go`
- `C:/Users/hf/Documents/Codex/2026-10-16/codex-threads-01a10f3e-8cf5-7312-b6c9/work/hl-usage-throughput-20261021/internal/agent/engine/xray_live_add.go`
- `C:/Users/hf/Documents/Codex/2026-10-16/codex-threads-01a10f3e-8cf5-7312-b6c9/work/hl-usage-throughput-20261021/internal/agent/engine/xray_adapter.go`
- `C:/Users/hf/Documents/Codex/2026-10-16/codex-threads-01a10f3e-8cf5-7312-b6c9/work/hl-usage-throughput-20261021/internal/agent/engine/gost_live_update.go`
- `C:/Users/hf/Documents/Codex/2026-10-16/codex-threads-01a10f3e-8cf5-7312-b6c9/work/hl-usage-throughput-20261021/internal/agent/engine/gost_live_add.go`
- `C:/Users/hf/Documents/Codex/2026-10-16/codex-threads-01a10f3e-8cf5-7312-b6c9/work/hl-usage-throughput-20261021/internal/agent/engine/xray_live_update_test.go`
- `C:/Users/hf/Documents/Codex/2026-10-16/codex-threads-01a10f3e-8cf5-7312-b6c9/work/hl-usage-throughput-20261021/internal/agent/engine/xray_live_add_test.go`
- `C:/Users/hf/Documents/Codex/2026-10-16/codex-threads-01a10f3e-8cf5-7312-b6c9/work/hl-usage-throughput-20261021/internal/agent/engine/gost_live_recovery_test.go`
- `C:/Users/hf/Documents/Codex/2026-10-16/codex-threads-01a10f3e-8cf5-7312-b6c9/work/hl-usage-throughput-20261021/internal/agent/engine/gost_live_update_test.go`
- `C:/Users/hf/Documents/Codex/2026-10-16/codex-threads-01a10f3e-8cf5-7312-b6c9/work/hl-usage-throughput-20261021/internal/agent/engine/gost_live_add_test.go`
- `C:/Users/hf/Documents/Codex/2026-10-16/codex-threads-01a10f3e-8cf5-7312-b6c9/work/hl-usage-throughput-20261021/tests/integration/xray/live_update_test.go`
- `C:/Users/hf/Documents/Codex/2026-10-16/codex-threads-01a10f3e-8cf5-7312-b6c9/work/hl-usage-throughput-20261021/tests/integration/xray/outbound_continuity_test.go`
- `C:/Users/hf/Documents/Codex/2026-10-16/codex-threads-01a10f3e-8cf5-7312-b6c9/work/hl-usage-throughput-20261021/docs/xray-continuity-audit-20261024.md`
