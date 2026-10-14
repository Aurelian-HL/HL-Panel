# Xray 内核与 Reality 兼容性维护

HL-panel 使用官方 Xray-core 作为 VLESS 数据平面。控制面、节点代理和订阅由本项目独立实现，不依赖其他面板运行。兼容性应以实际发行二进制、有效配置和真实转发为准。

## 固定版本

构建脚本 `deploy/fetch-node-engines.py` 当前固定 **v26.7.28**，不追踪 `latest`。Linux amd64 官方归档为 [Xray-linux-64.zip](https://github.com/XTLS/Xray-core/releases/download/v26.7.28/Xray-linux-64.zip)，大小 21,164,807 字节，SHA-256：

```text
8195d909f1109b8f3d99eefe401a3c451d7bf4af71f24d3815420f77e5dd2a40
```

安装、重装和更新均在停止服务前检查候选核心必须为 `Xray 26.7.28`，下载或检查失败时保留现有服务。修改 main 不会自动替换已经发布的资产，也不代表既有节点已更新；发行包、安装包和实际运行版本必须分别确认。

## 旧客户端握手兼容性

[3x-ui issue #6543](https://github.com/MHSanaei/3x-ui/issues/6543) 描述 Reality 升级后的客户端兼容问题。`26.9.8` 及之后的相关实现要求客户端携带 `X25519MLKEM768` key share，部分旧指纹无法满足，即使 TCP 可达也不能完成认证。

隔离测试和真实转发对照中，`26.10.10` 对 Chrome100、Chrome120、Safari16 指纹失败；`26.7.28` 对这三种指纹、Chrome131 和默认 Chrome 均通过。这不等于 iOS Shadowrocket 真机全部行为已经验收，也不能证明长期超时全部消失。

`26.7.28` 中省略或设置空 `minClientVer` 会继承核心最低版本限制。第三方客户端版本字段可能与官方 Xray 不一致，HL 默认明确生成 `minClientVer: "0.0.0"`。Agent 恢复旧配置时也补齐此默认，保留管理员明确非空的限制；补齐只影响运行配置投影，不改原包摘要、配置代次、UUID、密钥、短 ID 或账本。不能只手改活动 JSON，否则可能被 Agent 恢复覆盖。

`maxTimediff` 默认不限制 Reality 时间差；手机 HTTPS 证书时间检查是另外的问题，服务端不能替客户端跳过。

## 已知 Vision 收尾缺陷

[Xray PR #6834](https://github.com/XTLS/Xray-core/pull/6834) 修复了 Vision direct copy 后把外层 TLS/Reality `close_notify` 写入内层流的问题。**26.7.28 不包含该修复**。

本项目使用本地 TLS 源站直接关闭 TCP 的有界重复测试，确认：

| 链路 | 26.3.27 / 26.7.28 | 26.10.10 |
| --- | --- | --- |
| 直连 TLS 1.3 | 完整响应，正常 EOF | 完整响应，正常 EOF |
| Vision 内层 TLS 1.2 | 完整响应，正常 EOF | 完整响应，正常 EOF |
| Vision 内层 TLS 1.3，含 Reality 外层 | 部分连接响应尾部多 19 字节加密 alert，bad record MAC | 完整响应，正常 EOF，无额外 alert |

19 字节记录体加 5 字节 TLS 头共 24 字节。缺陷并非每次触发，正常源站收尾也可能掩盖错误，普通网页成功不能证明没有缺陷。`vision_close_test.go` 与 `reality_close_test.go` 对旧核心运行八轮，要求至少一次捕捉到已知缺陷，其他轮次必须完整传输且干净 EOF；未知错误一律失败。在 26.7.28 下 PASS 表示**捕捉到了已知缺陷**，不表示修复。

此次版本选择优先恢复旧客户端握手兼容性，同时保留收尾缺陷这一明确限制。后续升级必须同时通过旧指纹与严格收尾验收，不能只因版本较新就全量升级。

## 入站与目标边界

- 组连接域名、面板域名和 Reality 目标是独立概念；更改组域名不得重置 SNI、目标、UUID、密钥和短 ID。
- tcp/raw、dest/target 是相应核心接受的别名，字段外观不同不代表连接行为不同。
- VLESS 默认 flow 为 xtls-rprx-vision；客户端 flow、SNI、公钥和短 ID 必须匹配有效入站。
- fingerprint 属于客户端握手，往服务端写同名字段不能补齐客户端 key share。
- 默认不启用 sniffing 或 PROXY protocol，不为模仿其他面板 JSON 强加行为。
- SOCKS 入站 UDP 修复不能直接解释 VLESS 入站加 SOCKS 出站的 TCP 故障。

## 历史新内核私网限制

26.10.10 的 Freedom 出站对代理入站默认限制私网目标。HL 为探针和管理员固定 DIRECT 目标保留专用、有限的 finalRules：探针只到相应回环 Echo 地址和端口；固定 DIRECT 通过 redirect 限定目标。例外不能扩展到通用 Freedom 或 SOCKS 出站。

服务端保存编译器修订号，兼容更新只有内容变化时产生新代次，失败保留最后可用配置。此次兼容 Agent 补齐旧配置默认，无需重新注册或重置客户数据。

## 更新和回滚

1. 面板服务端命令仅在**面板机**运行；节点更新命令仅在**节点机**运行。
2. 校验官方包摘要和版本，在服务账户下预检既有配置。
3. 保留旧 Agent、核心和私有配置备份，以及可执行回滚脚本。
4. 先一台后逐台，核对实际进程、身份、应用代次和真实认证转发。
5. 失败时恢复程序和最后可用配置，不用旧快照覆盖更新期间流量账本。

进程 active、TCP 可达和配置验证均不能代替真实转发。隔离测试使用临时凭据与 loopback；线上仅执行授权的链路验收。客户端真机与长期运行仍需验证，不能把此次恢复描述为所有间歇故障已经消失。
