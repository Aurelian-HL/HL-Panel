# 结构化转发业务与 GOST 本地验证

验证日期：2026-10-02。范围为隔离的本地测试，不包含线上节点部署或生产转发验收。

## 本次落盘模块

- `internal/control/forwarding/`：转发规则、请求校验、创建幂等摘要、替换 CAS 元数据、审计事件和客户可用状态投影。
- `internal/control/groupconfig/`：设备组连接地址、端口范围、直连权限、出口授权和流量倍率；初次配置 revision=0，后续替换带当前 revision。
- `internal/control/forwarding/gostconfig/`：将已分配端口的 TCP DIRECT 规则编译为 GOST v3 JSON；支持轮询、随机和源 IP 固定选择。
- `internal/control/httpapi/business_handlers_test.go`：实际 HTTP handler、认证和内存仓储共同参与的业务流程测试。
- `tests/integration/gost/forwarding_test.go`：实际 GOST 进程、真实 TCP 连接和两个本地目标。

规则保存成功仅代表控制面数据已保存，`deployed=false`。启用的有效客户规则仍显示 `pending_activation`，不会以配置生成或本地引擎测试代替节点应用确认。

## 真实引擎来源

在已下载的官方 `github.com/go-gost/gost` 源码目录本地构建：

- 源码 commit：`e9fab9587288b82ce8a1a9b8f07326f41b155935`。
- 构建模块版本：`v0.0.0-20260922140655-e9fab9587288`，不是声称下载了同名发行资产。
- `gost -V`：`gost 3.3.1 (go1.27.1 windows/amd64)`。
- `github.com/go-gost/x`：`v0.17.2`。
- 二进制：`C:/tools/gost/gost.exe`。
- SHA256：`76046FB3987A058BBE147F64338D6C9C77E5E49F1497822C707305AD478DF750`。

默认 Go 模块代理连接超时后，仅对本次构建进程设置 `GOPROXY=https://goproxy.cn`。保留 Go 模块校验，没有修改全局 Go 配置或项目依赖。

## 实际执行结果

```powershell
$env:NYVP_GOST_BINARY='C:/tools/gost/gost.exe'
go test ./internal/control/forwarding/... ./internal/control/groupconfig ./internal/control/httpapi ./tests/integration/gost -count=1 -v
```

以上五个包通过。HTTP 新增五个业务测试，覆盖客户组、客户、网络配置、自动分配端口、保存、编辑、暂停、恢复、401、幂等重复提交、CAS、端口冲突、规则额度、客户禁用/到期、双重出口授权、密码不回显及留空不重置、失败修改不产生部分写入。

每个真实 GOST 用例将一条结构化规则编译为 JSON，启动独立进程，以一个 `127.0.0.1` 监听入口连接两个本地目标，执行 32 次独立 TCP 连接：

| 策略 | 目标 A | 目标 B | 验证结论 |
| --- | ---: | ---: | --- |
| round_robin | 16 | 16 | 轮询均衡 |
| random | 19 | 13 | 两目标均实际收到连接；不要求固定比例 |
| ip_hash | 32 | 0 | 相同源 IP 在不同源端口连接中保持同一目标 |

`ip_hash` 的支持依据为该版本 `go-gost/x` service 层将来源 IP 写入 hash context；旧版 Flux 内嵌 GOST 代码不能作为这项行为的依据。

所有测试进程在结束后退出，检查未发现遗留 `gost-v3.3.1` 进程。测试仅监听及访问回环地址。

## 尚未验证或接通

- 此编译器明确拒绝 UDP 和 EXIT_GROUP，避免把未实现的路径默认为直连。
- 尚未从业务规则自动发布节点配置、启动正式节点 GOST、接收真实应用确认。
- 此验证只覆盖同一入口进程对多个目标的选择，不等同于跨多台入口机器的设备组调度验收。
- 真实 NY 数据导入、流量采集/扣量、限速和正式客户订阅不在此测试范围。
- 不将以上本地结果表述为 `xzf.hongle.cc` 已完成上线验收。

未连接或修改三个受保护站点 `zf.hongle.work`、`kl.hongle.cc`、`ht.hongle.work`。
