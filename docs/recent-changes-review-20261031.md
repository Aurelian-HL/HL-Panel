# HL Panel 近期更新说明与复查记录

检查日期：2026-10-31（北京时间）。范围：最近的安装器、规则持久化与批量创建、设备组配置、流量归属、侧边栏商店及一次性兑换码改动。

## 本轮结论

在下述源码检查和隔离回归覆盖范围内，未发现新的阻断性失败。发现一处打包基线遗漏：商店开发目录尚未带入已经有正式发布记录的 v0.1.154 创建规则持久化预算补丁。本轮已补齐四个 Go 文件和发行说明，并验证四个源码文件与 v154 提交逐字节一致，发行说明仅换行符不同。

这次是补齐开发目录，避免下一次打包丢失已修复的问题；没有将它描述为新发现的生产故障。商店和兑换码仍是未提交、未发布的本地改动。

本轮未访问生产数据库或生产面板，未创建生产规则、改动订阅、升级节点或重启转发引擎。测试结果不能替代真实客户端、真实支付或 Linux 节点的完整验收。

## 重点更新说明

### 1. 缩短规则操作等待，修复持续遥测下创建规则超时的风险

- 规则准备计算放到独立快照上，缩短占用统一写入阶段的时间，资料保存不必一直等待规则准备完成。
- 当流量或健康状态持续更新，使准备结果失效时，按已耗时和剩余期限提前进入串行提交，为最后提交保留预算，减少把期限耗在重复计算上的情况。
- 保留版本、一致性和并发修改检查；提交结果不明确时，不自动重放业务写入，防止重复创建。
- 失败日志补充脱敏的准备次数和阶段耗时，便于判断慢在准备、校验还是提交。日志不包含请求正文、凭据或客户标识。

数据库回归实际验证了：并发流量和健康上报仍被保留，创建只产生一次审计，提交失败不发布新配置或重复业务操作。

### 2. 增加批量创建，减少重复编译和重复提交

- 管理员可一次提交 1–500 条 VLESS + Reality、SOCKS5 出站规则，每行填写“昵称＋SK5”；省略昵称时默认使用上游 IP 或主机名。
- 自动分配端口、身份和必要材料；同一批操作使用幂等键，重试返回已有结果，避免重复建规则。
- 整批成功后统一提交；端口不足、额度不足、无效设备组或编译失败时，不留下半批规则和新增私密材料。
- 每批对每个受影响节点编译一次，减少逐条规则反复编译整份配置。500 条批量场景已有回归覆盖。
- 浏览器预览和错误提示不回显 SK5 密码；桌面与窄手机视口均覆盖输入、校验、失败重试和成功返回。

本轮未测量生产环境端到端下发耗时，不给出未经实测的毫秒数或提速倍数。

### 3. 安装入口简化，自动补依赖，修复新旧更新器衔接

- 面板统一保留自动探测线路入口，移除海外安装、离线部署和待发布提示，生成一条安装命令。
- 安装前检查缺少的工具、证书、系统服务和引擎资源；已有依赖跳过，缺少时补齐。下载、摘要或语法检查失败时，不继续注册。
- 未注册的旧程序、失败安装残留可自动替换；已经注册的身份、用量文件和无关服务保留。停服务瞬间若完成注册，停止清理并恢复服务。
- 更新不再依赖长期备份归档，使用临时事务材料处理切换和失败恢复；完成后清理。恢复结果不明确的材料保留供恢复处理。
- 修复旧节点维护工作器路径、恢复文件权限与系统 umask 的兼容问题；root 更新请求在查询节点账号前完成权限判断，兼容尚未创建账号的机器。
- Xray 按兼容版本固定为 26.7.28。普通依赖修复不主动重启 Agent 或转发引擎；节点版本升级仍有进程切换流程。

本轮实际运行了安装引导和残留安装处理的可移植回归；Linux 文件锁、真实 systemd、所有权和完整失败恢复仍需 Linux 环境验收。

### 4. 缩小设备组配置变更范围，补齐删除规则后的流量归属

- 设备入组、退组或成员材料变化时，重新编译受影响节点，以及依赖该出口的可靠 UDP 入口，减少对独立同组节点的无关重编译。
- 删除最后一条规则后，仍可按可信历史配置代数接收其尾部用量，减少删除瞬间流量漏记或被错误拒收。
- 用量网络投递移出引擎生命周期锁，避免控制面响应变慢时占住配置应用过程。
- 健康探测复用有上限的共享客户端，减少重复连接和无界并发。

相关控制面与 Agent 包回归通过。本轮没有在生产节点执行删除、退组或重启故障注入，也未重新验收真实 UDP 引擎链路。

### 5. 侧边栏商店和一次性兑换码：本地实现，尚未上线

- 侧边栏独立“商店”页面提供月、季、半年、年和永久方案，以及兑换和恢复兑换结果入口。
- 兑换码首次核销绑定域名、面板实例和实例公钥；通过公网 HTTPS 挑战自动证明域名，无需用户手工添加 TXT。
- 同码并发核销只成功变更一次；有期授权累加期限，永久授权不额外消耗续期码。
- 库存仅存码的 hash 与短前缀；临时导出内容加密，确认导出或过期后清除导出密文。
- 提交核销前先持久化签名收据，超时或重启后可恢复当前结果，避免重复延长，也不能通过旧收据恢复已撤销权限。
- 修复旧查询覆盖新兑换状态、旧会话响应污染新会话的问题；解析错误使用固定文案，避免回显夹带兑换码的字段内容。
- 授权操作独立于规则、订阅和节点配置，不调用转发引擎重启。

用户尚未搭建发卡商城，页面如实显示价格和购买入口待开放。真实付款、发货、HT 库存管理 UI、生产代理及完整购买闭环尚未交付，不能把本地兑换通过称为购买功能已上线。

## 实际检查结果

所有数据库回归使用 127.0.0.1:55441 的隔离 PostgreSQL；浏览器 API 使用本机 synthetic 数据，未连接真实支付或生产规则接口。

| 检查 | 本轮结果 |
| --- | --- |
| 11 个相关 Go 包的回归 | 10 个包通过，1 个包无测试文件；720 个顶层测试通过，包含子测试共 1,618 条通过记录；9 项条件测试跳过 |
| PostgreSQL 真实回归 | 持久化预算、并发流量保留、批量原子提交、并发端口预留、提交失败不发布，以及库存核销/恢复测试实际执行通过 |
| go vet ./... | 通过 |
| npm.cmd test -- --run | 70 个测试文件、402 项测试通过 |
| npm.cmd run build | TypeScript 和 Vite 构建通过 |
| 商店 Chrome 检查 | 1440、1024、390 三种宽度，共 42 项检查、42 张截图；无非预期控制台错误 |
| 批量创建 Chrome 检查 | 1440、1024、390、320 四种宽度，共 29 项检查、28 张截图；无非预期控制台错误 |
| 安装引导回归 | 9 项通过，模拟下载和依赖安装，不执行真实安装注册 |
| 未注册残留处理回归 | 10 项通过、1 项 POSIX 符号链接测试跳过；Windows 分支不代表真实 Linux 所有权验收 |
| 安装脚本语法 | 20 个 Python 文件编译检查、13 个 Bash 文件 bash -n 通过 |
| Go 格式 | 26 个已修改或新增 Go 文件全部符合 gofmt |
| git diff --check | 通过 |
| v154 基线一致性及撤销检查 | 四个 Go 文件字节一致，发行说明换行归一后相同；反向补丁仅检查通过，未执行撤销 |

桌面和手机的商店默认目录、批量创建预览截图已实际打开目视检查。浏览器检查包含空数据、加载、输入错误、提交失败、恢复和成功状态；预期注入的 HTTP 错误在浏览器报告中单独记录。

Go 回归实际命令：

~~~text
go test -json ./internal/panellicense ./internal/panellicenseissuer ./internal/control/httpapi ./internal/control/postgressnapshot ./internal/control/memoryrepo ./internal/control/forwarding ./internal/control/vlessidentity ./internal/control/usage ./internal/control/deploymentreceipts ./internal/agent/runtime ./internal/xrayconfig -count=1 -timeout=6m
go vet ./...
~~~

前端命令在 apps/web-admin 目录执行。浏览器命令为 node tests/browser/license-shop-smoke.cjs 与 node tests/browser/forwarding-batch-create-smoke.cjs。Python 回归命令为 python -m unittest discover -s tests/install -p test_node_bootstrap.py -v 及相应的 test_unenrolled_install.py。

### 明确未验证的内容

- 本轮未指定真实 Xray/GOST 测试二进制：两项 Xray 恢复/控制重放测试、普通 UDP 和可靠 UDP 的真实引擎测试跳过。
- Linux /proc 套接字归属检查跳过；Linux 节点更新事务、文件锁及真实 systemd 故障恢复未在本机运行。
- 四项独立性能测量未开启。本轮不声称 CPU 降低百分比、生产下发速度或真实网络零丢包。
- 真实商城、HT 库存管理、生产域名证明和生产购买链路未验收。
- 本轮未重新访问线上面板，不把旧发布验收记录当作当前线上健康证明。

## 修改范围与状态

工作目录：

~~~text
C:/Users/hf/Documents/Codex/2026-10-16/codex-threads-01a10f3e-8cf5-7312-b6c9/work/hl-usage-tail-release150
~~~

分支为 release/hl-performance-batch-20261031，HEAD 为 1eb486481c9ee7f4d49a7e4b9de41a3890e846d1。本轮基线补齐来自独立 v154 目录的提交 dffb08e5faa98eca5d5f5cce929e571044f5ed00，尚未在本开发分支提交。

本轮实际补齐或新增的源码/说明：

- internal/control/postgressnapshot/prepared_transaction.go
- internal/control/postgressnapshot/prepared_transaction_integration_test.go
- internal/control/postgressnapshot/transaction_failure.go
- internal/control/postgressnapshot/transaction_failure_test.go
- docs/release-v0.1.154.md
- docs/recent-changes-review-20261031.md（本报告）

现有商店、授权签发服务及前端改动保留，未通过全仓库 reset 覆盖。临时数据库 bootstrap helper 和检查产物仅放在 artifacts 下，不加入发行文件。

按本地 outputs/HL-Panel-v0.1.154/验收报告.md 的历史记录，v154 曾在 2026-10-31 正式发布并升级 hlpanel.hongle.cc，且未升级节点；本轮未实时复验线上版本或 GitHub。商店不在该正式发布范围内。

## 证据、清理和撤销方法

本报告已实际写入，测试日志、报告和截图真实生成并读取。证据目录：

~~~text
C:/Users/hf/Documents/Codex/2026-10-16/codex-threads-01a10f3e-8cf5-7312-b6c9/work/hl-usage-tail-release150/artifacts/recent-review-20261031
~~~

主要产物为 go-test.jsonl、go-test-summary.json、go-vet.log、node-bootstrap-tests.log、unenrolled-install-tests.log、installer-syntax.json、gofmt-check.json、v154-baseline-check.json，以及 shop-browser/report.json、batch-browser/report.json 和对应截图。前端构建目录为同一仓库 apps/web-admin/dist。

本轮启动的隔离 PostgreSQL 和 Vite preview 已停止，127.0.0.1:55441 与 127.0.0.1:4197 已核验不再监听，记录见 local-services-cleanup.json。

如需撤销本轮五文件基线补齐，在本工作目录对 artifacts/recent-review-20261031/v154-baseline.patch 反向应用即可；本轮已执行 git apply --reverse --check 验证可撤销，未执行实际撤销。报告和 artifacts 可按本轮精确路径单独处理，不能删除已有商店开发文件或回放生产数据库。未发生生产变更，因此本轮没有生产回滚操作。
