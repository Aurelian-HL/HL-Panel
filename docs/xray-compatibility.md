# Xray 内核与 Reality 兼容性维护

HL-panel 使用官方 Xray-core 作为 VLESS 数据平面，控制面和节点代理由本项目独立实现。参考其他面板时，应比较实际发行二进制、有效入站参数和配置应用方式；面板名称相同或 JSON 字段数量相同，不能证明连接行为一致。

## 版本基线

本次将构建脚本 `deploy/fetch-node-engines.py` 中的 Xray 从 `v26.3.27` 升级为 `v26.10.10`，与 [3x-ui v3.10.0 的发行工作流](https://github.com/MHSanaei/3x-ui/blob/v3.10.0/.github/workflows/release.yml#L118) 使用的内核一致。这里比较的是发行工作流下载的真实内核版本，不能以面板自身版本或 `go.mod` 的依赖版本代替。

Linux amd64 官方归档为 [Xray-linux-64.zip](https://github.com/XTLS/Xray-core/releases/download/v26.10.10/Xray-linux-64.zip)，大小为 22,168,131 字节，SHA-256 为：

```text
72a5fde819713a103c6fc26a87aa025c1518c918a34eba4bc38d64dc0728e5d9
```

Windows amd64 诊断归档为 [Xray-windows-64.zip](https://github.com/XTLS/Xray-core/releases/download/v26.10.10/Xray-windows-64.zip)，大小为 22,058,899 字节，SHA-256 为：

```text
bfefce8e9804e53b7d75b7c49d958b34e89890d9d5e672329089d1b3111647af
```

构建仍使用固定版本和固定 SHA-256，不在节点运行过程中自动下载 `latest`。修改脚本不等于已更新既有节点；发行产物、节点安装包和节点实际运行的 `xray version` 均需核对。

## 与断链有关的已知内核缺陷

Xray 上游 [PR #6834](https://github.com/XTLS/Xray-core/pull/6834) 于 2026-09-27 合并，修复 **Vision 切换到 direct copy 后，外层 TLS/REALITY 的 `close_notify` 被写入内层数据流**。对应提交为 [e5e85ca9dada936ae736197ad2b7a685972e8e0f](https://github.com/XTLS/Xray-core/commit/e5e85ca9dada936ae736197ad2b7a685972e8e0f)。`v26.9.30` 及本次选用的 `v26.10.10` 包含该修复，旧基线 `v26.3.27` 不包含。

Vision 识别内层 TLS 1.3 后会直接使用底层 TCP。旧实现随后关闭外层 TLS 对象时，可能额外写入 24 字节的加密 alert；客户端把它作为内层 TLS 记录处理，导致 `bad record MAC` 或 `ERR_SSL_PROTOCOL_ERROR`。源站自身的正常 TLS 收尾有时会掩盖该错误，因此可能表现为偶发失败、刷新恢复。

上游 [issue #6794](https://github.com/XTLS/Xray-core/issues/6794) 提供了确定性复现和字节对账，并报告 Linux `v26.3.27`、VLESS + Vision + Reality 路径也受影响。[issue #6124](https://github.com/XTLS/Xray-core/issues/6124) 描述了 Shadowrocket 下 iOS 下载中断，同样在该修复合并时关闭。[issue #4878](https://github.com/XTLS/Xray-core/issues/4878) 的症状是浏览网页偶发 TLS 错误、刷新恢复。

本项目通过 `tests/integration/xray/vision_close_test.go` 实际复现并验证了该差异。隔离源站在写完响应后直接关闭 TCP，不发送内层 TLS `close_notify`；测试透传记录 TLS 记录头，并比较完整响应和 EOF。另由 `reality_close_test.go` 使用临时 Reality 密钥和真实 VLESS Reality 入站重复同一 A/B，确认该收尾差异也存在于客户实际使用的 Reality 外层：

| 隔离链路 | 旧内核 `26.3.27` | 新内核 `26.10.10` |
| --- | --- | --- |
| 直连源站，TLS 1.3 | 3 次完整响应、正常 EOF | 3 次完整响应、正常 EOF |
| Vision，内层 TLS 1.2 | 3 次完整响应、正常 EOF | 3 次完整响应、正常 EOF |
| Vision，内层 TLS 1.3 | 3 次均在响应尾部多出 19 字节加密 alert，报 `bad record MAC` | 3 次完整响应、正常 EOF，无额外 alert |

记录体 19 字节，加上 TLS 记录头 5 字节即为 24 字节。两项测试均不使用客户配置、凭据或公网节点。Reality A/B 进一步确认客户入口路径；它仍是隔离回环验证，不能替代每台线上节点的长期运行验收。

这是升级的具体依据，不能据此推断每一次超时都来自同一个缺陷。客户到入口的路径、节点配置失败、进程重启、资源耗尽及配置应用切断既有连接仍需分别排查；单纯 TCP 连通不能代替 VLESS 认证和实际转发验证。

## 入站参数对照

下表以 3x-ui `v3.10.0` 的默认配置和 HL 的 VLESS Reality 编译器为基准。用户手工编辑过的入站必须另行读取实际运行配置。

| 参数 | HL 默认或生成方式 | 3x-ui 默认或生成方式 | 实际影响 |
| --- | --- | --- | --- |
| 协议 / flow | VLESS / `xtls-rprx-vision` | 入站中按客户端配置 | 需核对同一客户端 UUID、flow 和订阅下发值 |
| 传输 / 安全 | `tcp` / `reality` | 可选 TCP/RAW 和 Reality | TCP/RAW 是对应内核版本支持的别名；不能凭字段拼写认定断链 |
| `dest` / `target` | 写入 `dest` | 写入 `target`，兼容读取 `dest` | 内核接受二者为 Reality 目标别名 |
| `show` / `xver` | `false` / `0` | `false` / `0` | 默认一致 |
| `serverNames` | 选定 SNI | 用户选定或根据目标生成 | 必须与目标证书和客户端 `serverName` 配套；不能随组入口域名变更自动替换 |
| `shortIds` / 公钥 | 来自规则的 Reality 凭据 | 来自入站凭据 | 客户端需使用对应短 ID 和公钥；升级不应重置已有凭据 |
| sniffing | 未输出，核心默认关闭 | 默认 `enabled: false` | 默认语义一致；开启会影响路由，不能为了 JSON 外观一致强加 |
| TCP header / PROXY protocol | 未输出，核心使用默认 | 无 header、`acceptProxyProtocol: false` | 默认语义一致；对直接客户端启用 PROXY protocol 可能破坏连接 |
| `maxTimediff` | 未设置，默认不限制 | `0` | 默认无时间差限制；手机系统时间仍可能影响 HTTPS 证书校验 |
| `minClientVer` / `maxClientVer` | 未设置 | 空字符串 | 不能凭曾出现过的上游提交认定当前内核必然拒绝旧客户端 |
| fingerprint | 客户端下发 `chrome` | 客户端设置默认 `chrome` | 属于客户端握手参数，不是必须写入服务端 Reality 的参数 |

对照源码：[Reality schema](https://github.com/MHSanaei/3x-ui/blob/v3.10.0/frontend/src/schemas/protocols/security/reality.ts)、[sniffing schema](https://github.com/MHSanaei/3x-ui/blob/v3.10.0/frontend/src/schemas/primitives/sniffing.ts)、[TCP schema](https://github.com/MHSanaei/3x-ui/blob/v3.10.0/frontend/src/schemas/protocols/stream/tcp.ts)。

Xray 曾引入默认最低客户端版本限制，但 [47cfe9994a6b39b1f673ba35e62b091bcce15a71](https://github.com/XTLS/Xray-core/commit/47cfe9994a6b39b1f673ba35e62b091bcce15a71) 已注释掉该默认限制。判断兼容性应以最终发布源码和真实客户端测试为准。

## Reality 目标与入口地址

客户连接的组域名、面板域名和 Reality 伪装目标是三个独立概念。组域名用于客户连接入口；Reality 目标用于未通过认证的 TLS 握手转发，并需与选定 SNI 的证书相配。

3x-ui 的 [Reality 目标扫描](https://github.com/MHSanaei/3x-ui/blob/v3.10.0/internal/web/service/reality_scan.go#L238) 检查 TLS 1.3、ALPN `h2`、X25519/X25519MLKEM768、可信证书链与 SNI。其 `Feasible` 为四项检查同时通过。这是目标兼容性检查，不能证明客户完整转发链路健康。

HL 的编译阶段检查格式、密钥和参数合法性；这不等于从每台节点实测目标 TLS 能力。增加网络验证时，应在节点侧对实际目标和 SNI 做有超时、有频率上限的检查。既有规则升级时保留 UUID、私钥、公钥、短 ID、SNI 和目标，不能自动改成面板域名或随机新目标。

## 新内核的私网目标限制

`26.10.10` 的 Freedom 出站对 VLESS 等代理入站默认阻断私网目标；命中时会黑洞连接 30–90 秒。旧版可访问回环，并不能证明新核心升级后协议探针仍正常。上游实现见 [Freedom 出站](https://github.com/XTLS/Xray-core/blob/v26.10.10/proxy/freedom/freedom.go)。

HL 的协议探针使用独立路由，把挑战转发至节点回环 Echo 服务。生成配置为该专用出站设置 `finalRules`，仅允许 TCP、`127.0.0.1/32` 和 `::1/128`，端口精确限定为 Echo 端口。

用户明确配置的固定 DIRECT 目标也需保持旧版行为。该专用出站先用 `freedom.redirect` 固定目标和端口，再放行对应 TCP 端口；IP 目标同时限定精确 `/32` 或 `/128`。域名目标由核心正常解析，避免把某次 DNS 结果永久写入配置；客户端请求其他地址仍只能到达管理员配置的固定目标。此例外不加入通用 Freedom 或 SOCKS 出站，也不会把探针回环权限赋予客户。SOCKS 出口凭据和 Reality 凭据不变。

已有节点持久化的配置不会因为替换 Xray 二进制自动加入新规则。升级流程必须在恢复旧配置时处理此兼容性，或先重新下发并确认新配置，再切换核心。只替换内核可能让之前正常的节点停留在“待激活”。隔离测试的回环源站另行显式放行；真实 Agent 激活测试直接使用生产编译器，避免测试夹具掩盖此问题。

服务端为节点保存独立的配置编译器修订号。旧节点首次心跳会重新编译兼容配置；内容不变时只记录修订号，内容变化时才产生一个新配置代次。成功结果与历史配置一同持久化，面板重启后不重复下发。编译失败保留原代次并允许后续重试，不重新注册节点，也不更换客户或探针凭据。

节点启动恢复本地最后可用配置后，先执行一次最多 5 秒的心跳，再启动配置监听，避免监听先读取尚未完成兼容更新的旧配置。面板不可达时仍继续运行本地配置与重试循环。这个顺序不能消除“新版核心先恢复旧配置”的短暂窗口，升级必须按以下顺序进行：

1. 在面板机更新服务端，保持节点旧核心运行。
2. 等待旧节点心跳完成兼容配置下发，确认期望代次与已应用代次一致、协议探针通过。
3. 在节点机分批更新节点端，再核对真实 Xray 版本和双向转发；不要在面板机执行节点更新命令。
4. 若节点离线、配置应用失败或协议探针不通过，先修复该节点再更新核心。服务 active 不能代替实际转发验收；失败时使用节点更新备份中的回滚脚本。

并发心跳共用一次快照写入门控。只读遥测路径不编译、也不返回未持久化的新代次；兼容修订可绕过普通遥测节流，但仍受门控约束。普通心跳仍可能每 30 秒写入全量快照，本次减少重复并发写入，没有宣称彻底移除快照架构的 CPU 成本。

## 验证与维护边界

升级应先在隔离环境比较旧、新核心：相同凭据、相同有效入站、相同目标和出口，覆盖真实 VLESS/Reality 握手与转发、TLS 1.3 下载、连接关闭重建、长连接和配置应用。对 #6834 的回归可使用本地 TLS 源站的正常 `close_notify` 和直接 FIN 两种收尾，检查代理后没有额外外层 alert；TLS 1.2 可作为不进入 direct copy 的对照。

配置验证通过只说明内核接受 JSON；内核启动成功只说明进程已运行。验证完成需读取实际内核版本、确认协议认证和双向字节传输，并证明新增规则不会无必要地重启全量引擎而中断存量连接。线上升级需备份并保留上一版本二进制及配置，按节点分批进行。

本次对照不把 SOCKS **入站** UDP ASSOCIATE 修复当作 VLESS 入站 + SOCKS **出站** 的根因，也不把 Windows 专属接收修复、XHTTP 修复套用到 Linux TCP/Reality 节点。客户端系统时间错误导致的 HTTPS 证书验证失败，也不能靠服务端忽略 Reality 时间差来解决。
