# 节点传输可靠性研究（2026-10-17）

用户关注的是：规则下发快、CPU 占用低、客户使用稳定，以及节点间发生丢包时尽量不影响业务。本报告区分协议提供的能力、本轮实验可以证明的范围，以及尚未验证的产品能力。

## 一、先区分三个目标

| 目标 | 含义 | 本轮结论 |
| --- | --- | --- |
| 底层 0 丢包 | 网络中的每个包都到达对端 | UDP、TCP、QUIC、KCP 都不能对任意网络作此保证；本轮故意丢弃部分 UDP 包 |
| 应用数据完整交付 | 丢失的内容通过重传或冗余恢复，接收应用拿到正确的数据 | TCP、QUIC 可靠流、KCP 均有相应机制；需要结合超时与实验条件判断 |
| 客户不断链、无感 | 已建立的业务连接持续，延迟和停顿仍在业务可接受范围 | 除传输机制外，还取决于进程状态、更新方式、网络故障长度和应用超时；本轮未验收 |

因此，“想让数据完整送达，只能靠 UDP”不准确。UDP 本身不提供可靠送达，可靠 UDP 隧道是在 UDP 之上增加恢复机制。发生底层丢包后，恢复还可能带来等待、额外流量和 CPU 开销；应用最终收到全部数据，也不等于过程完全无感。

来源：[RFC 768](https://www.rfc-editor.org/rfc/rfc768)；[RFC 9293 §2.2](https://www.rfc-editor.org/rfc/rfc9293.html#section-2.2)；[RFC 9000 §2.2](https://www.rfc-editor.org/rfc/rfc9000.html#section-2.2)。

## 二、不同传输分别解决什么

### TCP

TCP 向应用提供可靠、有序的字节流，包含检测丢失、重传、流量控制和拥塞控制等机制。网络持续不可用或超过连接及应用的超时后，仍可能失败。使用 TCP 不需要额外套一层 UDP 才能保证字节正确交付。

来源：[RFC 9293](https://www.rfc-editor.org/rfc/rfc9293)，尤其 §2.2。

### QUIC

QUIC 运行在 UDP 上，可靠 STREAM 提供有序字节流。丢失的 STREAM 数据可以在新的 QUIC 包中再次发送，配合丢失检测与拥塞控制完成恢复。不能把所有 QUIC 使用方式都称为可靠传输：QUIC DATAGRAM 扩展不为丢失的应用数据报提供重传。

本轮实验研究的是经 GOST relay/QUIC 承载的 TCP 应用数据，不是 QUIC DATAGRAM。QUIC 使用 UDP，也不表示它天然使用 BBR。

来源：[RFC 9000 §13.3](https://www.rfc-editor.org/rfc/rfc9000.html#section-13.3)；[RFC 9002 §6、§7](https://www.rfc-editor.org/rfc/rfc9002)；[RFC 9221 §5.2](https://www.rfc-editor.org/rfc/rfc9221.html#section-5.2)。

### KCP 与 FEC

KCP 通过 ARQ 等机制实现可靠交付，具体拥塞、窗口、定时器和多路复用实现会影响效果。部分 KCP 实现还可使用 FEC：发送额外编码分片，在冗余能力范围内恢复部分丢失。KCP 协议本身与某个实现附带的 FEC 应分开描述。

FEC 不是无限恢复。以 Reed-Solomon 编码为例，恢复需要收到足够数量的编码分片；丢失超过可恢复范围，仍需其他恢复机制或最终失败。冗余与编码也有带宽和计算成本。KCP 作者给出的性能或带宽描述不是本项目测量，不能直接作为节点选型结论。

来源：[KCP 作者仓库](https://github.com/skywind3000/kcp)；[kcp-go 实现说明](https://github.com/xtaci/kcp-go)；[RFC 5510 §8.3.1、§8.4](https://www.rfc-editor.org/rfc/rfc5510)。

## 三、BBR 是什么，本轮默认开启到哪里

BBR 是拥塞控制算法。Linux TCP BBR 根据模型调整发送节奏及在途数据量，目标包括吞吐与排队延迟；可以理解为影响 TCP 传输表现的一种算法，不能等同于任何线路上的必然加速。它既不是 UDP 重传开关，也不承诺 0 丢包或不断链。

Linux 的 `net.ipv4.tcp_congestion_control` 控制新连接的默认选择。入站连接会继承监听 socket 的拥塞控制选择；应用也可能显式设置自己的算法。更改系统默认值不会让所有既有连接和既有监听立即切换。即使 sysctl 读回为 `bbr`，也不能据此宣称每条当前客户连接都使用 BBR。

本轮源码在令牌新装注册或已注册节点重装成功后，默认尝试执行受管 BBR helper：

- 先检查内核能力及配置冲突，保留已有 BBR 变体；不支持或失败时警告，不撤销成功注册。
- 只管理自身的 sysctl 配置，保存备份并提供回滚；不安装内核、不重启系统或网络，不改 qdisc、防火墙或路由。
- helper 在 Agent 安装启动之后执行，因此本次安装中此前已经创建的监听，也不能算作已被即时切换。
- 该变更不为 QUIC/KCP 配置 UDP 隧道，也不控制其用户态拥塞控制实现。

隔离 Linux 验收已在 Ubuntu 22.04 与 Ubuntu 24.04 中通过，覆盖真实默认值、新 TCP socket、既有双向 TCP 连接、qdisc 不变、受管文件重载与回滚；令牌安装验收也通过。具体 CI 记录见第六节。这只证明已覆盖的隔离安装与运行场景：单文件 sysctl 重载验证不等于完整开机配置优先级验收，本轮没有整机重启验收，也没有因此修改生产节点。

来源：[Linux ip-sysctl：tcp_congestion_control](https://docs.kernel.org/networking/ip-sysctl.html#tcp-variables)；[Google BBR](https://github.com/google/bbr)；[BBR v3 实现](https://github.com/google/bbr/blob/v3/net/ipv4/tcp_bbr.c)。本项目实现见 `deploy/configure-node-bbr.py`、`deploy/install-node.sh`、`tests/install/node_bbr_linux.py`。

## 四、为什么可靠 UDP 也不能单独解决更新断链

可靠传输需要保留连接状态，例如密钥、序列号、窗口、缓冲与重传状态。引擎进程退出后，新进程不会仅因使用了 UDP 就自动接管这些状态。QUIC 的连接迁移能力也不等于跨进程重启自动恢复。

因此，GOST/Xray 进程替换、完整重启或两个引擎非原子切换造成的连接风险，需要在生命周期和更新机制中解决，并以既有业务连接验收。更换节点间传输协议不能替代这部分工作。持续断网或超时也可能使可靠连接终止。

来源：[RFC 9000 §10.1、§10.3](https://www.rfc-editor.org/rfc/rfc9000#section-10.1)。

## 五、本轮可靠 UDP 实验设计

脚本：`tests/integration/gost/reliable_udp_test.py`。仅在明确授权的隔离 Linux 环境运行，所有 TCP/UDP 端点均为 `127.0.0.1`，不使用生产节点，不修改 tc、sysctl、防火墙或路由。

| 项目 | 实际设计 |
| --- | --- |
| 引擎 | 从固定版本下载流程获取 GOST，检查对应 manifest 和版本 |
| 四个场景 | QUIC 无主动丢包；QUIC 注入丢包；KCP 无主动丢包；KCP 注入丢包 |
| 丢包方式 | 就绪和鉴权检查之后，代理在两个方向分别每观察到 23 个 UDP 数据报丢弃第 23 个 |
| 负载 | 单条应用连接同时双向传输，每方向 2 MiB 独立随机数据；B 操作返回回执后等待客户端关闭 |
| 稳定流关闭方式 | E 操作返回 echo 后、B 操作返回 bulk 回执后，都等待客户端关闭，以分别观察稳定传输与关闭边界 |
| 独立立即关闭检查 | F 操作每次发送 64 字节随机数据，目标回复后立即关闭；每场景新建 20 条应用 TCP 连接，底层 QUIC/KCP 隧道可能复用 |
| F 检查故障条件 | 在 bulk 与其 CPU、UDP 计数窗口结束后关闭主动丢包，再执行 F；不是在注入丢包状态下验收立即关闭 |
| 期限 | 每次 bulk 操作设定 45 秒有界交付期限 |
| 正确性 | 固定接收长度；上传由接收端计算长度与 SHA256 返回核对；下载与目标原始随机数据的 SHA256 核对 |
| 有效丢包 | 丢包场景必须两个方向均实际丢包；无主动丢包场景检查代理未主动丢弃数据报 |
| 鉴权 | 随机 relay 凭据，缺少或错误凭据须被拒绝；超时不作为成功拒绝 |
| QUIC TLS | 临时证书、CA 与 serverName 校验 |
| KCP 参数 | 隔离实验使用随机 AES 密钥、normal 模式、MTU 1200、发送和接收窗口 128；不是产品默认参数 |
| 清理 | 退出时停止临时进程并清理 socket、配置、证书与密钥；失败诊断只输出固定分类 |

第三轮四场景的稳定流 bulk 子项均通过长度及 SHA256 核对，但 F 立即关闭检查没有全部通过，可靠 UDP 作业总体仍然失败。稳定流子项只能证明：在本轮条件与期限内，指定应用数据完整到达。SHA256 和字节数一致不证明底层没有丢包，也不能单独证明恢复一定来自重传；恢复可能涉及实现提供的冗余机制，本轮没有协议级重传或 FEC 统计。E/B 等待客户端关闭的设计也没有单独证明完整 EOF、半关闭和退出语义。

### CPU、耗时与字节统计的限制

- CPU 从两个 GOST 进程的 `/proc/<pid>/stat` 读取 user/system tick，检查 starttime，换算 CPU 秒；不包括 Python 代理、目标服务或全机其他开销。样本存在 tick 粒度，短窗口不能支持细微性能排序。
- CPU 采样包围 bulk 调用，两个进程采样先后进行；包含本次流建立与退出的工作。`elapsed_seconds` 从应用头发送之后开始，隧道已预热，因此不是冷启动或首次握手耗时。
- UDP 统计在代理每观察到一个数据报时计一次，区分 seen、dropped、forwarded，并校验计数平衡；字节数不含 IP/UDP 头，包含协议开销、重传或冗余。
- UDP snapshot 在 bulk 返回后获取，可能不包括最后确认及后续控制报文；它不是整个会话的完整网络费用。额外字节也不能全部记为重传字节。
- 每 23 包丢 1 包是按数据报个数的周期性注入，不等于精确按字节丢失 1/23，也不代表公网随机或突发丢包。无主动丢包场景不宣称网络绝无其他丢失。
- 只有单连接、单次、小数据量、本机路径；没有跨区域 RTT、抖动、限速、多并发和长时间稳定性测量，不能由此宣称“更快”“CPU 更低”或“客户无感”。

## 六、2026-10-17 的实际 CI 结果

本报告于 2026-10-17 更新。以下是隔离 Linux CI 记录，不是生产节点验收：

| 轮次 | 提交与运行记录 | BBR / 令牌安装 | 可靠 UDP 结果 |
| --- | --- | --- | --- |
| 第一轮 | `4704507`；[run 39006057063](https://github.com/Aurelian-HL/HL-Panel/actions/runs/39006057063) | Ubuntu 22.04、24.04 BBR 与 token-install 最终均 success | QUIC 就绪检查失败，未进入鉴权和 bulk |
| 第二轮 | `3e3adc5`；[run 39006830039](https://github.com/Aurelian-HL/HL-Panel/actions/runs/39006830039) | Ubuntu 22.04、24.04 BBR 与 token-install 最终均 success | 进入鉴权后的 echo 检查时收到 EOF，尚无通过的 bulk 结果 |
| 第三轮 | `d13dd2f2caf3f6ee287a60d22c1e8ef07f916dd5`；[run 39007022981](https://github.com/Aurelian-HL/HL-Panel/actions/runs/39007022981) | Ubuntu 22.04、24.04 BBR 与 token-install 最终均 success | 四场景稳定流 bulk 通过，但 QUIC 的 F 立即关闭检查失败；作业总体 failure，工作流仍红 |

第二轮目标服务记录到请求且发送调用没有报错，不能证明客户端完整收到回复，不能以此把该轮改判为通过。

第三轮稳定流每个场景均双向各传输 2 MiB，接收长度与 SHA256 一致。下面的丢包计数、耗时与 CPU 秒属于 bulk 窗口；F 检查在其后关闭主动丢包再单独执行：

| 场景 | 稳定流 bulk 长度 / SHA256 | 主动丢包 out / back | bulk 耗时（秒） | client / server 进程 CPU 秒 | F 完整回复 |
| --- | --- | --- | --- | --- | --- |
| QUIC 无主动丢包 | 通过，双向各 2 MiB | 0 / 0 | 0.057 | 0.05 / 0.04 | 0 / 20 |
| QUIC 每 23 包丢 1 包 | 通过，双向各 2 MiB | 86 / 86 | 0.069 | 0.05 / 0.06 | 0 / 20 |
| KCP 无主动丢包 | 通过，双向各 2 MiB | 0 / 0 | 0.234 | 0.06 / 0.05 | 20 / 20 |
| KCP 每 23 包丢 1 包 | 通过，双向各 2 MiB | 100 / 100 | 0.196 | 0.06 / 0.06 | 20 / 20 |

这些结果同时说明了两个不同范围：指定丢包下的稳定流载荷交付已通过；立即关闭边界尚有未解决失败。F 的失败计数可能包含连接失败、超时、EOF 或回复内容不符，不能把全部失败都定性为尾包丢失，也不能泛化为 QUIC 协议不可靠。KCP 的 20/20 只代表本轮这个有界样本，不是所有关闭条件都已验收。

这里的 CPU 数值是进程 CPU 秒，不是 CPU 占用百分比。计时短、隧道已预热、只有单轮 loopback 样本，而且没有 TCP+BBR 对照，因此不能用表格给 QUIC/KCP 排名，也不能宣称任一方案更快、CPU 更低或客户无感。UDP 字节统计只代表观测窗口，不代表完整会话费用；不能把观测字节减应用字节全部解释为重传。

## 七、原版与 Pipe 修补版的真实网络对照

固定 GOST tag `v3.3.1-nightly.20260922` 的源码依赖检查显示：`go.mod` 引用 `github.com/go-gost/x v0.17.2`、`github.com/quic-go/quic-go v0.60.0`，未见 `replace` 覆盖。对其中 `x` 的 `Pipe` 源码复核发现两项值得独立验证的风险：读取循环先处理 `err`、后处理 `n`，可能丢弃一次合法的 `n > 0` 与 EOF 或其他错误共同返回的数据；写入路径未完整处理短写。

2026-10-17 在临时目录 `work/pipe-regression-20261017-133916-5367a8` 对提取的原函数和调整版函数做了 6 例实跑对照：原函数测试退出码为 1；调整版先写入本次读取的数据，再处理读取错误，并对无错误但未写满的情况返回 `io.ErrShortWrite`，测试退出码为 0。原函数在 `data + EOF`、`data + 非 EOF 错误` 两例中均交付 `0 / 12` 字节；短写模拟 `Write` 返回 `(4, nil)` 时，原函数错误结果仍为 nil。调整版在这 6 例中均通过。

随后已完成隔离 Linux 原/修对照：[run 39008802484](https://github.com/Aurelian-HL/HL-Panel/actions/runs/39008802484)，研究分支提交 `c54778cc7c759f7ee15c4337f1825c4eb3137138`。构建与产物上传 success；网络门禁 failure，原因是仍有半关闭检查失败。

来源核验：固定 tag 是 annotated tag，tag object 为 `6ea0a0c2154c4e62718295bc047d21de84283af0`，解引用后的源码 commit 为 `e9fab9587288b82ce8a1a9b8f07326f41b155935`。官方发行二进制的实际 buildinfo 包含 x `v0.17.2`、quic-go `v0.60.0`、kcp-go `v5.6.72`、smux `v1.5.57`，无 replace；其 Go 版本为 go1.26.8。原版与修补版均使用 go1.27.0、同一源码、相同依赖图、相同临时 modfile 与 x 源码副本、相同构建参数；修补版只覆盖一个 Pipe 文件。官方二进制与源码构建的 Go 版本不同，因此因果比较以同条件的原/修构建为准，官方结果仅作为发行版复现及来源佐证，不能称三份二进制完全同源同构建。

构建器检查上游模块和源码 SHA256、go.sum、Go module verify、buildinfo，以及构建前后源码未改变。官方 archive SHA256 固定为 `7ce4abf8856fc45926b86d92c770f00c8d7cfbc353d410b9e2886f8ce0a2fe0f`。原版与修补版 binary SHA256 分别为 `dfe2c996db066a8420afd8bbb5098fd508f21b92744a056344ca5b70378e26b1`、`1d8c7baa17a698b5cd2af72581f0b410e8a8fcbb66c35549864fd7c4395dff87`。完整来源记录在该次 CI artifact 的 `provenance.json`。

顺序是 official、original、fixed、fixed、original。每次运行 QUIC/KCP × 无主动丢包/每 23 包丢 1 包，共 20 场景。20 场景的随机鉴权、错误及缺少鉴权拒绝、双向各 2 MiB 长度与 SHA256 均通过；主动丢包场景确实观察到双向丢弃数据报。关闭边界在 bulk 后关闭主动丢包再测，不能据此声称在丢包条件下的关闭语义也已通过。

| 版本 / 协议 | F 立即关闭完整回复 | F + 收到 EOF | H 客户先 FIN，目标收到 FIN 后才回复 | S 目标先 FIN，客户收到 EOF 后的 ACK 必须到达目标应用 |
| --- | --- | --- | --- | --- |
| 官方发行版 QUIC | 0/20 | 0/5 | 0/3 | 0/3 |
| 源码原版 QUIC | 0/20 | 0/5 | 0/3 | 0/3 |
| Pipe 修补版 QUIC | 20/20 | 5/5 | 3/3 | 0/3 |
| 上述三种构建的 KCP | 20/20 | 5/5 | 0/3 | 0/3 |

表格是每个场景的计数，重复轮次及两种 bulk 丢包条件下的关闭结果一致。原版 QUIC 的扩展检查均归类为 `data_incomplete`；修补版 QUIC 的 S 为 `target_ack_not_observed`。KCP 的 H 为 `data_incomplete`，S 为 `target_ack_not_observed`。S 通过需要目标应用实际观察到完整 ACK 和客户 FIN，不能只凭客户端 sendall 成功算通过。

这次真实网络对照支持：Pipe 对 `n > 0` 与 readErr 的处理顺序确实影响该实验中的 QUIC F/EOF/H 交付。它没有解决 S，不是所有失败的唯一原因，也不构成产品 QUIC/KCP 交付。额外独立执行的 legacy F 结果保存在每场景的 `extended_close_checks.legacy_f`，原有 F 门禁没有被删掉或放宽。

原/修提取函数的双向 TCP、取消与错误诊断也已执行：真实 localhost TCP 的正常双向半关闭通过，但模拟 FIN-only Close 的连接在 ctx 取消后仍可能保留阻塞 Read；首个非 EOF 错误也没有主动取消另一半。函数诊断揭示清理边界，不能以测试进程最终退出替代资源释放验收。

上游源码依据：[GOST 固定 tag 的 go.mod](https://github.com/go-gost/gost/blob/v3.3.1-nightly.20260922/go.mod)；[x v0.17.2 的 Pipe](https://github.com/go-gost/x/blob/v0.17.2/internal/net/pipe.go)；[quic-go v0.60.0 的 receive_stream.go](https://github.com/quic-go/quic-go/blob/v0.60.0/receive_stream.go)。后者的读取实现允许本次读取同时返回数据和 EOF；不能把这种合法的读取结果误判为对端没有发送数据。

## 八、半关闭生命周期研究

源码检查显示 QUIC listener 的 `quicConn` 直接嵌入 `*quic.Stream`。服务把可用的 `conn.Context()` 作为 handler 的上下文，而 quic-go 文档明确 Stream.Context 在发送方向关闭时就取消。正常发送 FIN 因此可能误伤仍需继续的反向业务。

本地真实 TCP 加上下文绑定模型已复现这一风险：目标先发送回复并 FIN，客户收到 EOF 后发出的反向 ACK 为 0/3 字节；原/Pipe 修补版均复现。该模型不是 live QUIC。另加的 Linux 候选保留 Pipe 修补，仅将 listener adapter 的 Context 改为 QUIC connection context，独立对照只修 Pipe 的版本。它用于验证假说，不是最终清理方案；连接级 context 不能直接代表每条 stream 的完整生命周期。

该候选已完成真实 Linux 对照：[run 39009329900](https://github.com/Aurelian-HL/HL-Panel/actions/runs/39009329900)，提交 `af770d59ab806e2981151a109156b30ec9b42014`。顺序 fixed、lifecycle、lifecycle、fixed，每轮两协议、两种 bulk 丢包条件，共 16 场景。16 场景的鉴权和双向 bulk 均通过；原/修控制二进制 SHA256 与前轮完全一致，候选仅在相同 Pipe overlay 上增加 listener 的两个文件，源码、依赖图、临时模块与构建参数相同。候选 binary SHA256 为 `3fc07bb5ac70f45dcadd14b306e00d3776103118fbf1b48d7dadb3b32db90f48`。

| 版本 / 协议 | F | F + EOF | H | S |
| --- | --- | --- | --- | --- |
| 只修 Pipe / QUIC | 20/20 | 5/5 | 3/3 | 0/3 |
| Pipe + listener 生命周期 / QUIC | 20/20 | 5/5 | 3/3 | 3/3 |
| 上述两种构建 / KCP | 20/20 | 5/5 | 0/3 | 0/3 |

计数仍为每个场景的结果，两次重复与两种 bulk 丢包条件一致；关闭检查本身没有主动丢包。QUIC 候选四个场景的 S 合计 12/12，对照组为 0/12，支持本次条件下 listener 上下文误取消影响反向传输的判断。整体作业仍 failure，因为 KCP 半关闭未通过，门禁没有放宽。不能把 QUIC 这组有界检查全部通过扩大为取消/异常/资源回收已通过，更不能据此宣称公网性能和生产客户无感。

KCP 也不能仅凭失败就归因于协议不支持半关闭。实际依赖 smux `v1.5.57` 提供 CloseWrite；但 relay connector 的 tcpConn 只嵌入 net.Conn，未暴露底层 CloseWrite。Pipe 不支持半关闭时会回退 Close，这会让 smux 的接收方向也关闭。本地 smux/net.Pipe 契约实验共 12 例：v1/v2 直接使用或转发 CloseWrite 都能在 FIN 后收到 9/9 回复字节；隐藏方法并回退 Close 的 H 模型均为 0/9。这个实验只测 smux 和封装方法集，不是 KCP 网络或完整 GOST 根因证明。S 的剩余失败也不能仅凭此模型解释完毕。

源码依据：[QUIC listener conn](https://github.com/go-gost/x/blob/v0.17.2/listener/quic/conn.go)、[service Context](https://github.com/go-gost/x/blob/v0.17.2/service/service.go)、[relay connector conn](https://github.com/go-gost/x/blob/v0.17.2/connector/relay/conn.go)、[quic-go stream](https://github.com/quic-go/quic-go/blob/v0.60.0/stream.go)、[smux stream](https://github.com/xtaci/smux/blob/v1.5.57/stream.go)。

## 九、relay 半关闭透传与异常清理的新增证据

2026-10-17 已完成独立 halfclose 候选的 Linux 配对运行：run `39010364861`，研究分支提交 `1051cb19a88040dca3e9c7f9529c665951db7e95`。候选继承 lifecycle，仅给 relay connector/handler 的 buffered TCP adapter 增加 CloseWrite 透传；发送 FIN 前先刷新缓存协议头，与正常 Write 共用锁。真实 Linux 构建 success，网络门禁 failure，结果产物上传并已下载核对。halfclose 二进制 SHA256 为 `4df30c82245bd8300be8c172484aa6147f185b6ba3210f366bc9e529135d40be`。

顺序 lifecycle、halfclose、halfclose、lifecycle，共 16 场景。所有场景的鉴权、双向各 2 MiB 长度/SHA256、legacy F 与 F+EOF 均通过。下面汇总每个版本/协议的四个场景；关闭检查仍在关闭主动丢包后进行：

| 版本 / 协议 | F | F + EOF | H | S |
| --- | --- | --- | --- | --- |
| lifecycle / QUIC | 80/80 | 20/20 | 12/12 | 12/12 |
| halfclose / QUIC | 80/80 | 20/20 | 12/12 | 12/12 |
| lifecycle / KCP | 80/80 | 20/20 | 0/12 | 0/12 |
| halfclose / KCP | 80/80 | 20/20 | 0/12 | 2/12 |

KCP H 全部归类为 data_incomplete；S 剩余失败为 target_ack_not_observed。此结果否定了“仅给两个 relay adapter 透传 CloseWrite 就足以解决完整 KCP 半关闭”的判断。仍需检查整个封装链及 smux 双 FIN 后缓冲回收，不可把局部 adapter 契约通过代替完整引擎验收，也不通过放宽期限或删除门禁改变失败结论。

另在 Windows localhost 使用真实 quic-go v0.60.0 完成 10 项测试、每项 race 模式重复 3 次，共 30 次测试执行通过，无 race 报告。其中 Pipe 两阶段 abort 模型在异常/取消时先设置立即 deadline、等两个 worker 退出后再 Close，验证了阻塞 IO 收敛、正常 FIN 后延迟反向回复和同 session 兄弟流正常。它尚不是完整 GOST overlay 的验收。

资源额度实验提供了明确反证：QUIC 限制 8 个 incoming stream 时，重复 deadline(now)+原生 Close 8 次后，第 9 次开流在 150ms 内超时；对保留的 stream 调用 CancelRead 后能恢复开流。逐流显式 CancelRead+CancelWrite 连续处理 24 流通过。因此 deadline+Close 只解除应用 IO，不代表 QUIC 接收方向资源已经回收。正常成功路径不可无条件 CancelWrite，它可能放弃尚未可靠交付的数据；异常终止必须限定到当前 stream，不能关闭共享 session。

独立实验与原始结果保存于本次对话的 work/quic-cleanup-review；Linux 来源与逐场景结果保存于 work/udp-pair-ci-39010364861。上述样本不证明公网低 CPU、断网恢复、长时间稳定或生产客户无感。

## 十、smux 双 FIN 缓冲回收的确定性复现与独立候选

本地使用真实 smux v1.5.57 建立了确定性顺序：第一流发送 21000 字节并完成双 FIN，再通过同 session 的第二流 marker 确认接收循环已处理 FIN，之后才读取第一流。原版在 v1/v2、ReadAll/WriteTo 四条路径都稳定读到 0 字节。普通模式重复 30 次、race 模式重复 10 次均复现；没有 race warning 不代表语义正确。根因是 tryHalfCloseCleanup 在双 FIN 后立即调用 session.streamClosed，后者 recycleTokens 将未读 bufferRing 清空。

独立 drain 候选继承 halfclose，只增加 smux 的一个 stream.go overlay：缓冲非空时延后双 FIN 清理；Read/WriteTo 消费后重试；有序远端 FIN 的排空返回 EOF。显式 Close 仍可丢弃剩余数据并归还 tokens。smux 原 stream.go 的固定 SHA256 为 d3f54098259b2fe3f47831bc93f7895a75647e81d41df6b5d62e5e0c578adccf。构建器为所有源码候选使用同一份字节不变的 smux 源码副本和相同 replace，独立 overlay 只用于 drain；检查依赖版本、来源、完整源码树和构建后未改动。它没有修改宿主 module cache。

真实 smux 契约的八个子场景普通重复 30 次共 240 次通过，race 重复 10 次共 80 次通过：双 FIN 后完整读取、84 KiB 排队只读 137 字节后显式 Close、640 KiB 跨窗口反向传输、流数量收敛、tokens 精确归还、同 session 兄弟流可用均已检查。与 relay 两端 adapter 组合后，普通 200 次、race 60 次顶层测试执行通过；其中 TestExistingWriteHidesShortNil 是对既有短写缺陷的复现，不能计为该缺陷已修复。原始日志与结构化结果在本次对话 work/relay-halfclose-review。

Python 关闭夹具的直连与正确透传 TCP forwarder 已各对 F+EOF/H/S 重复 100 次，共 600/600 通过；吞 FIN 或提前全关的负控制均被拒绝。本轮还加入单独的 lossy_close_checks：每项 64 KiB、每 23 个 UDP 数据报丢一个，保留原两秒期限和全部无主动丢包门禁。组窗口必须实际观察到 out/back 双向 drop > 0，计数使用累计差值、不重置；逐次结果也记录丢包覆盖。UDP 计数包含复用隧道控制包，不能声称精确丢掉了 FIN。Linux 真引擎结果需另行验收，不能用这些本地测试代替。

## 十一、drain 候选的真实 Linux 丢包关闭验收

2026-10-17 已完成 run `39011820132`，研究提交 `f616336985ea68fcf180bfb53d7417db3892bee0`。Linux 构建、网络门禁及产物上传均 success；原始 provenance、逐场景 JSONL 与日志已下载并解析到本次对话 `work/udp-pair-ci-39011820132`，另生成 `verified-summary.json`。固定 GOST 源提交仍为 `e9fab9587288b82ce8a1a9b8f07326f41b155935`。

顺序 halfclose、drain、drain、halfclose，共 16 场景；源码构建使用相同依赖图、x/smux 原始副本及 replace，drain 仅额外叠加 smux stream.go。所有场景的缺失/错误鉴权拒绝和双向各 2 MiB 长度/SHA256 均通过。下面每行汇总四个场景；原关闭门禁未开启主动丢包，新增关闭门禁单独开启每 23 个 UDP 数据报丢一个，单次期限仍为 2 秒：

| 版本 / 协议 | 原 F | 原 F+EOF | 原 H | 原 S | 丢包 F+EOF | 丢包 H | 丢包 S |
| --- | --- | --- | --- | --- | --- | --- | --- |
| halfclose / QUIC | 80/80 | 20/20 | 12/12 | 12/12 | 20/20 | 12/12 | 12/12 |
| drain / QUIC | 80/80 | 20/20 | 12/12 | 12/12 | 20/20 | 12/12 | 12/12 |
| halfclose / KCP | 80/80 | 20/20 | 3/12 | 3/12 | 20/20 | 6/12 | 3/12 |
| drain / KCP | 80/80 | 20/20 | 12/12 | 12/12 | 20/20 | 12/12 | 12/12 |

新增丢包关闭检查的每个操作组均实际观察到 out/back 双向 drop > 0。drain 四个 KCP 场景的 H 每组 out/back 各 9 个；S 每组 out 为 10、back 为 10、10、11、10 个。计数包含协议及复用隧道控制包，不能声称精确丢掉了 FIN 或每个应用数据报。旧 halfclose 的 KCP H 仍为 data_incomplete，S 仍为 target_ack_not_observed；这与确定性 smux 负对照相互支持。本轮证明的是指定版本、loopback 和有界样本下，drain 候选通过全部既有与新增门禁，不是任意网络下绝不丢数据。

本轮 halfclose 二进制 SHA256 为 `ef1b1f13593660718ff6bfde646058e56503a7f887f6ac5c394714cb63926729`；drain 为 `fdd9b7e5a22d31888d6231e5ed2e4ee50bc6eea41d1e8b900c01fac2ab91c6da`。与上轮二进制哈希不同不能单独推断行为变化；本轮结论以相同构建环境内的配对对照及来源记录为准。

smux 本地回归另新增阻塞 WriteTo 已取出缓冲时发生双 FIN 的边界：v1/v2 均完整交付且 tokens 未提前归还。最终因果契约普通 300/300 子场景、race 100/100 通过；relay 组合普通 200/200、race 60/60 通过；上游两个流测试文件普通 63/63、race 21/21 通过，无 race warning。原 module cache 未修改，独立补丁 git apply --check 通过。

## 十二、当前 abort/Dial 合约与重启门禁状态

### abort 与 Dial 合约证据

旧的隔离 Linux run `39027350359` 中，独立的 stacked abort 与 Dial 合约步骤已完成：abort 以 `-race -count=3` 通过 129 个测试事件，Dial 以 `-race -count=3` 通过 18 个测试事件，合计 147 个通过事件。abort 覆盖已知 wrapper、listener/dialer、半关闭宽限 deadline 不覆盖立即 abort、首个错误终止另一半、Cancel 释放 stream credit 等；Dial 覆盖请求取消保持共享 session、死亡 session 重连，以及旧失败 session 不能移除替代 session。其范围是 localhost QUIC/TCP 合约和 request-local 清理，不覆盖生产传输所有权、0-RTT、WAN 或完整 GOST 对照。

### 旧 run 的真实失败性质

同一旧 run 的可靠 UDP pair 共启动了 16 个场景；15 个场景进入真实引擎并完成其记录的 bulk、关闭和丢包关闭子检查，唯一失败发生在 `dial` 候选、QUIC、lossy 场景的 `server_startup` 前置夹具阶段。该失败的诊断记录为：server 日志没有已知错误标记、client 日志尚不可用、UDP 计数为 0、目标服务没有收到请求；关闭检查没有开始。因此不能把它描述成 QUIC 关闭语义失败，也不能仅凭这份记录断言根因一定是临时 TCP 端口复用。当前修复保留端口 socket 到 client 启动前，并增加 `client_fixture_setup` 的固定安全失败分类；是否解决真实 Linux 场景，必须以新 CI 结果为准。

### 当前提交与重启恢复门禁

较早提交 `e9993b3504d023b60046440510f7477a5c4b74fd` 的真实 Linux workflow [run `39028607048`](https://github.com/Aurelian-HL/HL-Panel/actions/runs/39028607048) 中，build/race 步骤通过，但 pair 对照的 16 个场景全部在 `engine_reconnect` 阶段以 `RuntimeError` 失败，没有成功的 measurement/reconnect 记录。该轮诊断不足以区分子阶段，不能据此归因为协议、认证或资源清理问题。

随后包含细分诊断的准确提交 `eb3031e4467aa592ca801b86da1d104edc851bf9` 已完成真实 Linux [run `39031964493`](https://github.com/Aurelian-HL/HL-Panel/actions/runs/39031964493)，job `117394071680`。完整 1709 行 job log 的 checkout 记录已核对该提交；结果中的 `source_commit=e9fab9587288b82ce8a1a9b8f07326f41b155935` 指固定 GOST 源码，不是本仓库 CI head。构建、abort/Dial 合约的 147 个通过事件、旧 timeout 直接 metadata 合约的 174 个通过事件及产物上传均完成，但真实 pair 的 16 场全部失败，timeout 候选 8 场无一通过：

| ordinal / 版本 | 协议 | 两种 bulk 丢包标签合计 | 实际失败子阶段 | 关键诊断 |
| --- | --- | --- | --- | --- |
| 0、3 / dial 对照 | QUIC | 4 场 | `old_stream_exit` | 旧流未在 15 秒内退出，`restart_old_stream_exit_failed` |
| 0、3 / dial 对照 | KCP | 4 场 | `old_stream_exit` | 旧流未在 15 秒内退出，`restart_old_stream_exit_failed` |
| 1、2 / timeout 候选 | QUIC | 4 场 | `replacement_readiness` | 旧流退出已过，新连接握手超时；均记录 `proxy_close` 清理失败与 1 次 `out_send` ICMP refusal |
| 1、2 / timeout 候选 | KCP | 4 场 | `authenticated_readiness` | `RuntimeError`，目标请求数及两个方向 UDP seen/forward/drop 均为 0 |

每组覆盖 `lossy=false/true` 两种场景标签；重启恢复本身仍关闭主动丢包。KCP 四场不是已证实的 server/client startup 失败，诊断明确发生在鉴权就绪阶段。QUIC 的认证拒绝标记可能来自预期负控制，不等同于本次重连认证失败。失败行的 `stable_bulk_passed=false` 也不能独立证明已有 bulk 数据损坏；本轮整体场景未完成且没有通过的 reconnect 记录。完整逐场矩阵和 16 条诊断已解析到本次对话 `work/udp-repair-20261017-v2/evidence/full-job-results.json`，完整日志为同目录 `job-priority.log`。

`engine_reconnect` 门禁保持 15 秒旧应用连接失败界限：先保持客户 GOST 进程，终止服务端 GOST，要求旧应用连接在 15 秒内 EOF/RST/套接字错误；随后以相同配置和端口启动 replacement，要求服务端 PID 改变、客户 GOST PID 及 `/proc/<pid>/stat` starttime 不变，再建立新应用连接并完成双向 2 MiB 长度和 SHA256 校验。该检查明确 `injected_datagram_loss=false`，只证明旧流终止和新流恢复，不证明旧连接跨进程延续，也不证明丢包条件下的重启恢复。15 秒是本实验的硬界限，不是已交付的用户服务承诺。

提交 `eb3031e` 已包含停机前旧流存活检查及提前 FIN 的拒绝用例；聚合器要求全部 PID、布尔值、字节数、SHA256、计时和实验范围证据，拒绝缺失或畸形记录。固定白名单诊断把重启失败分为 `pre_stop_stream`、`server_stop`、`old_stream_exit`、`replacement_start`、`identity`、`replacement_readiness`、`replacement_bulk`、`target_stream_closed`，不输出异常正文、日志或秘密。代理夹具将 connected UDP 的 `send()` 拒绝和正常主动丢包分开计数，仅出站 `ConnectionRefusedError` 可记录后继续，其他发送错误仍使场景失败。细分诊断帮助定位了失败，未使这 16 场通过。

### 本轮定位的两个独立问题

KCP 配置加载问题已在真实 `Config.Read` 路径复现：JSON 经 Viper 将 `metadata.kcp.keepalive=2` 解为 `float64`，上游 `mdutil.GetInt` 只接受 int/string/bool，得到 0 后回退 smux 默认 10 秒心跳，继而拒绝显式 5 秒 `keepaliveTimeout`。修前 listener/dialer 的真实 Go 复现均失败，记录位于 `work/udp-repair-20261017-v2/loaded-json-before/results.jsonl` 和 `commands.json`。原 174 个通过事件使用直接构造的 metadata，没有覆盖这一文件加载类型差异，不能据此宣称真实 JSON 配置已生效。局部修复已补齐这两个配置项的有限正整数 float64 兼容及 JSON/YAML、边界、精确事件名单合约；修后真实 Go 完整单轮的 168 个通过事件已逐项核对，记录位于 `work/udp-repair-20261017-v2/full-timeout-after/results.jsonl` 和 `commands.json`。冻结源码的隔离 runner 已完成普通 `count=3`：504 个精确测试通过事件、exit 0、19.406 秒；实际 `-race -count=3` 使用 MSYS2 GCC 完成 504 个精确测试通过事件、exit 0、30.578 秒，两者各包 252 个通过事件、无失败或跳过。原始日志、命令及来源分别位于 `work/udp-repair-20261017-v2/frozen-runner-normal/` 和 `frozen-runner-race/`；这仍是本地 KCP 初始化及配置合约，不是 Linux pair 验收。其他 KCP 数值项的 JSON 类型兼容不在本轮修复范围，不能声称 MTU、window 等已验证按配置生效。

UDP 代理夹具的阻塞读问题也已定位：front/upstream 原为 blocking socket，同一 selector 批次中，front 处理的 upstream send 可能消耗 pending ICMP，随后 stale upstream readiness 对应的 recv 已无数据却无限阻塞；stop Event 无法唤醒，从而出现 `proxy_close` 等待失败，并可能阻挡 replacement QUIC 握手。夹具已将两个 socket 改为 nonblocking，两条接收路径捕获 `BlockingIOError`；未改计数平衡、15 秒旧流期限、F/EOF/H/S 或 lossy-close 门禁。真实 loopback UDP socket 加刻意过期 selector readiness 的两场负控在修前均失败，修后同两场重复 10 轮共 20/20 通过、0 error。原始对照见 `work/udp-repair-20261017-v2/evidence/proxy-stale-readiness-comparison.json` 及对应 baseline/fixed 日志。这是本地可控复现，不能说已完整重现 Linux ICMP 触发链。

当前 Windows 离线门禁最终实际执行 56 项，54 项通过、2 项 Linux connected-UDP ICMP 专用检查跳过；其中新增的检查覆盖 ICMP 被 send 消耗后同批 stale recv 的精确顺序，定向 `git diff --check` 通过。修复后的真实 Linux 16 场尚待执行并审核，不能把夹具局部通过解释为当前所有重启失败已解决，也不能把 dial 对照的旧流退出失败隐藏或放宽。

## 十三、交付边界与下一步

- BBR helper、令牌安装调用和原始实验代码已同步到 Git 仓库 `main` 的 `832188a5fe403e5ce583f9f9c0d01c7d64417ba2`；新增成对引擎及生命周期实验位于 `research/reliable-udp-pipe-20261017`，没有发布本轮 tag 或 Release，也没有部署到生产节点。
- 尚未把 QUIC/KCP 接入产品的完整规则流程，未默认启用节点间可靠 UDP 隧道。
- 本轮没有修改生产节点配置，没有把生产节点切换到 QUIC/KCP，也没有据此变更客户线路。
- drain 候选在 run `39011820132` 的全部可靠 UDP 门禁通过，是此前提交 `f616336985ea68fcf180bfb53d7417db3892bee0` 的历史、有界隔离实验结论；它不能替代 `e9993b3`、`eb3031e` 的新增重启门禁验收，也不能覆盖最新已完成 Linux run `39031964493` 的 16 场失败结果。
- 本轮 KCP 文件配置加载兼容与 UDP 代理非阻塞修复仍处于本地复核阶段；没有本轮修复后的 Linux 全场通过证据，不能宣称已实现生产可靠 UDP、零丢包、低 CPU 或客户无感。

KCP 文件加载路径及边界合约的本地普通 count=3 与实际 race 隔离运行已验收；下一步由主审核提交已冻结的 KCP、代理及证据补丁到隔离研究分支，对准确提交重跑并逐场核验 Linux 16 场；保留 dial 对照，必须完整审阅 timeout 候选的 8 场，不能只挑一个通过样本。工作流保留 timeout 结果、命令、模块图、overlay/provenance、替换代码与 parent 输入证据，排除庞大源码及编译缓存以缩短取证等待。不能放宽 15 秒界限或盲目重跑求绿；已实施的 abort/Dial 独立合约不等于完整引擎重启恢复成功。继续保留 F/EOF/H/S 和实际丢包关闭门禁，随后仍需断网与恢复、长时间负载，不能先默认开启生产 UDP。

之后在同一故障注入方式、机器资源、版本、MTU、带宽与负载下比较 TCP+BBR、QUIC 可靠流、KCP，以及独立比较的 KCP+FEC；若要衡量 BBR 本身的收益，还需保留 TCP 原拥塞算法对照。覆盖真实 RTT、抖动、随机及突发丢包、多连接并发、持续流量与短请求，至少多次重复并随机化顺序，记录应用完整性、失败率、吞吐、冷建链、请求尾延迟和最长停顿、双端单位业务流量 CPU 秒及完整窗口字节开销，之后再开展已授权公网环境的性能验证。现有实验不足以支持性能优劣结论。

规则下发速度与连接保持需要单独验收：观察规则提交、通知、节点应用的实际时延，同时让既有客户连接持续传输，分别执行新增、修改、删除规则及回滚，检查更新窗口中的中断、停顿和恢复。只有真实结果满足已明确的业务目标，才可以把“快、低 CPU、稳定、无感”作为本项目交付结论；任何具体停顿或时延阈值应先确认为验收目标，不能事后把候选数值当作已满足的服务承诺。
