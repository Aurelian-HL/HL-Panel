# Xray 本机协议和生命周期验收

设置 `NYVP_XRAY_BINARY` 为经过核对的本地 Xray 可执行文件的绝对路径，再运行：

```sh
go test ./tests/integration/xray -count=1
```

测试只创建 loopback 监听器和临时文件，未设置该变量时跳过真实引擎测试。
支持的固定版本以 `process_test.go` 中的版本校验为准。

当前生产基线为 `26.10.10`。仅做升级差异对照时，可以设置
`NYVP_XRAY_EXPECTED_VERSION=26.3.27` 并指向旧版二进制；该变量不会修改
生产安装脚本的版本。新旧测试分别运行，避免同一次测试混用两种二进制。

`production_route_test.go` 原样使用生产编译器生成的配置，不通过测试夹具
添加回环豁免。它验证固定 DIRECT 的 IP/域名目标以及独立协议探针路由：
客户请求其他目标仍只能到达管理员指定的固定目标，探针身份只到达对应的
回环 Echo 端口。这覆盖新内核私网限制与生产配置的实际兼容性。

`vision_close_test.go` 比较直连 TLS 1.3、Vision 内层 TLS 1.2 和 Vision
内层 TLS 1.3，各运行三次完整响应与连接收尾。在显式指定旧版基线时，
Vision/TLS 1.3 场景要求复现 `bad record MAC` 和额外外层 alert，因此旧版
对照显示 PASS 表示捕捉到已知缺陷；新版必须完整传输、干净 EOF 且没有
额外 alert。该测试不能作为旧内核正常的证明，详见
[`docs/xray-compatibility.md`](../../../docs/xray-compatibility.md)。

`reality_close_test.go` 使用临时 Reality 密钥、真实 VLESS Reality 入站和
本机 TLS 源站重复 TLS 1.3 收尾 A/B。它只验证 Reality 外层是否保留同一
Vision 关闭缺陷，不连接公网目标；旧版三次应复现 `bad record MAC`，新版
三次应完整响应并正常 EOF。

`live_add_test.go` 验证新增独立规则不会更换 Xray 进程或中断其它规则已有
TCP 会话；API 部分成功后也必须撤销新增监听器并恢复完整旧路由。

`live_remove_test.go` 记录删除 API 的边界：`rmi` 关闭指定入站监听器，
无关入站的既有 TCP 会话、进程 PID 和统计 epoch 保持不变；**被删除入站的
既有 TCP 会话也可能继续传输**。因此不能将 `rmi` 普遍代替暂停规则、流量
额度耗尽或权限撤销的完整应用流程，否则已连接客户可能继续使用。
当前这些操作仍保留完整应用和 last-known-good 回滚路径。

`version_test.go` 验证从真实可执行文件读取内核版本并缓存；重复读取版本
不会启动子进程。该值代表 Agent 启动时检查的安装二进制版本，外部替换
二进制后需重启 Agent 才会刷新，且版本字段本身不证明引擎正在运行。

这些隔离测试不替代客户网络或客户端长期运行验收，不能据此声称所有
间歇超时已经消失。
