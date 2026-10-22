# 下发优化积累批次：2026-10-22

本报告记录性能优化的本地实现、隔离验收和各轮历史状态。2026-10-23 经用户
授权，本批纳入 v0.1.137 的仓库提交与新发行包核查；生产及节点升级不在本轮
范围内。下文各轮“未提交、未发布”是当时的状态，最终发行以 GitHub Release
及发布流水线结果为准。基线为
`ae7018812801b1c813dc5fa9ce85b089bb69d851`，分支
`fix/snapshot-lock-window-20261022`。已发布 v0.1.136 的既有成果记录在
`control-dispatch-performance-20261022.md`，不计作本批收益。

## 实际改动

1. 配置读不再装载无关的实时流量、协议健康、节点遥测 overlay；控制命令、
   节点绑定、总览使用适合各自数据的独立读视图。仍检查 PostgreSQL 格式、
   revision 与 xmin；故障、同 revision 的修复、删除和撤销不能落入旧缓存。
   规则部署判定与公开就绪状态仍读取必要 overlay。
2. 每个节点的一次编译建立临时规则身份与本机材料索引，避免对每条规则扫描
   全部材料、重复生成公开就绪状态和对候选身份做完整排序。临时索引不跨写入
   留存，仍验证用户生命周期、额度、撤销、归属、成员、端点、密钥和节点能力。
3. 分别规范化引擎 JSON 后编码小外壳，避免对整个大 bundle 再解码一遍。
   字节 oracle、协议字段契约及 fuzz 检查保证规范字节和 SHA-256 保持一致。
   编译器修订仍为 4，未因实现优化触发所有节点重新编译。
4. 新增 `POST /api/v1/agent/apply-results/batch`：仅将同一次已成功本地应用的
   commit 与 verify 两条回执合为一个 HTTP 请求、一次 PostgreSQL 快照事务。
   两条证据、两条审计仍保留；整批失败回滚，持久化后才通知。
5. 节点对精确回执队首一次落盘确认；重启可补报，不混合不同代次和 attempt。
   老面板仅在返回 404/405/501 时退回逐条接口；鉴权、冲突、网络和服务失败
   均保留回执。旧接口部分成功后重放，依靠相同 phase 的幂等语义。
6. 终态回执网络等待位于引擎生命周期锁外。后台补报占用锁时，即时补报跳过，
   不再拖住新 Apply 的返回和调用者流量采集锁；即时补报整个积压队列共享
   750 ms 总预算。没有移除回执，也没有新增无界 goroutine；后台循环继续补报。
7. 协议探测的连接证据缓存绑定可信部署回执中的规则配置指纹，无关规则导致
   bundle generation 变化不必让原规则失效；配置指纹变化必须重新做网络挑战。
   静态配置验证缓存独立保留。命中时返回原挑战时间，不把读取缓存当成新验证。
8. 不可变读取视图中的节点 bundle 改为首次使用时才校验和解析；只读取配置时，
   不再预先处理全部节点的部署证据。每个 bundle 通过 sync.Once 合并并发首次
   访问；仍先验证该视图自己的完整 SHA-256，才能使用共享结构索引。缓存预热
   后的损坏字节、未知代次仍拒绝，成员、撤销、attempt 与回执仍逐次判断。
   使用连续数组承载每个 bundle 的延迟状态，减少逐节点小对象分配。
9. 终态即时补报遇忙锁、读取失败、网络错误，或本地恢复生成新回执时，用一个
   容量为 1 的通知唤醒已有后台 worker。队列仍先持久化；启动立即补报、正常
   5 秒周期兜底继续保留。通知只缩短正常空等，失败退避期间不会因通知提前
   重试；成功同步补报不再额外通知，也没有每次下发新建 goroutine。
10. desired 与 applied 相同时，读取视图对配置、回执和 attempt 各只复制一次。
    两者不同时仍保留两代独立证据，不影响下发期间旧配置的就绪判断。
11. 读取视图在源状态读锁内生成独立的权限/来源索引，不再复制和保留全部业务
    幂等响应 JSON。原始幂等记录继续持久化，写路径继续原有重放和权限检查。
    索引仍校验完整来源关系；损坏、删除或归属变化会影响新视图，已有视图与
    原始响应字节隔离，请求继续验证权威数据库版本。
12. UsageReadView 对管理员导入规则的来源关系改为一次遍历，避免逐规则重复
    扫描全部端点和绑定。与转发权限投影共用完整来源检查，并检查规则/端点的
    持久身份与引用一致；管理员存在性、归属和主体长度仍校验。仅出现候选
    管理员导入规则时构建临时管理员索引，普通客户/未分配规则不构建该索引。
    历史配置和回执仍防御性复制，未改变可变写路径的权威权限检查。
13. 协议健康读取视图按需建立规则到端点池的结构索引，overlay 共享同一私有
    不可变池结构和 sync.Once 索引。批量恢复及续租验证不再为每条结果遍历全部
    池；多池冲突仍拒绝，错误类型和消息保持一致。可变权威 Store 继续实时扫描，
    没有把索引用于事务写入，也没有缓存协议就绪结果或延长健康租期。
14. 健康续租校验不再做第二次全量 overlay 复制：先用同源完整证据校验，通过后
    才复制两个外层 map 和涉及的端点池；同池多条结果仅复制一次。节点、规则、
    无关池保持只读，空批次和全拒绝批次不复制运行时 map。批内顺序、旧证明替换
    检查、取消语义、首次激活的审计事务、原视图及并发请求隔离保持不变。
    PostgreSQL 持锁后的第一份 overlay 仍保留，本项只去掉校验阶段的重复复制。
15. PostgreSQL 健康续租结果通过逐条验证后，改为每次最多 128 个唯一规则/节点
    的参数化多行写入。同一规则/节点只保存最后一次已接受的证明；后续拒绝结果
    不覆盖它。逐条返回结果、首次激活审计、原有业务行锁及快照 revision 一次递增
    保持不变。所有分批写入继续处于同一事务，任一批失败整体回滚。缓存失效
    revision 仍由事务内触发器生成，作为失效标记使用，没有改成按结果条数计数。

## 测量结果

相同合成 fixture、每条规则独立身份与密钥，Windows AMD Ryzen Z1 Extreme。
基线从上述提交导出，仅复制同一 benchmark 测试文件。两树顺序运行，准备数据
不计时，`-benchtime=1x -count=5`；下表使用五次样本中位数。

| 范围 | 基线 | 本批 | 降幅 |
| --- | ---: | ---: | ---: |
| 100 条规则、1 节点，编译耗时 | 14.8727 ms | 10.3617 ms | 30.33% |
| 500 条规则、3 节点，编译耗时 | 462.7745 ms | 143.0574 ms | 69.09% |
| 500 条规则、3 节点，分配字节 | 74,097,928 | 62,292,976 | 15.93% |
| 本地 PG：终态两条回执事务耗时 | 2.8858 ms | 1.5284 ms | 47.04% |
| 本地 PG：20 次终态上报的快照写入数 | 40 | 20 | 50% |

PG 每种方式运行 20 个独立 attempt，日志中位数采用排序后第 `len/2` 项。
正常成功下发原有 prepare、validate、commit、verify 四次写入，本批变为三次，
不能将“终态两条写入减半”表述为“整个下发写入减半”。

100 次本地热配置读取的平均耗时：包含 overlay 为 176.680 µs，去除无关
overlay 为 103.242 µs；两条路径均保留权威版本检查。

以上为本地局部耗时及分配量，不是生产端到端、吞吐量、节点 CPU 百分比或
客户零中断保证。五次短样本仍有波动，不能据此承诺所有机器相同倍数提速。

不可变部署证据视图另作同树修改前后对比：24 个不同节点，各 100 个 fragment、
每个 fragment 约 1 KiB 填充，共享结构索引预热；准备数据不计时。
`BenchmarkEvaluatorPublication` 使用 `-benchtime=200ms -count=3`，取三次中位数。

| 发布视图后实际读取的节点数 | 原先耗时 | 本批耗时 | 原先/本批分配次数 |
| --- | ---: | ---: | ---: |
| 0 / 24 | 1.429919 ms | 0.002846 ms | 53 / 6 |
| 1 / 24 | 1.427135 ms | 0.062098 ms | 53 / 8 |
| 24 / 24 | 1.415927 ms | 1.422457 ms | 53 / 54 |

只访问一个节点时，该局部耗时下降 95.65%。访问全部节点时，仍必须计算全部
SHA-256，耗时基本持平（本轮约增加 0.46%），分配字节从 4,960 增至 6,752 B/op
（增加 1,792 B/op，约 36.13%）；只访问一个节点则降至 3,808 B/op。收益来自
避免未使用的工作，不能将这一数字描述为整个下发过程提速 96%。

继续优化时，以当时本地候选树为基线，运行现有
`BenchmarkForwardingRulesReadView/publish_projection`（200 条规则、1 个节点、
desired=applied、每个 bundle fragment 约 1 KiB 填充），
`-benchtime=300ms -count=5 -benchmem`，五次样本中位数：

| 视图构建局部指标 | 继续优化前 | 两项复制优化后 | 降幅 |
| --- | ---: | ---: | ---: |
| 耗时 | 1.691548 ms | 1.494275 ms | 11.66% |
| 分配字节 | 1,108,877 B/op | 670,992 B/op | 39.49% |
| 分配次数 | 2,233 /op | 2,016 /op | 9.72% |

这里的基线不是 HEAD，也不能与前文编译器对比叠加。短样本存在波动；仅去掉
同代复制的中间版本耗时中位数为 1.327283 ms，快于最终版，故不将逐项收益
相加，也不据此承诺生产端到端延迟或 CPU 下降比例。明确收益是减少无用复制
及分配。两代配置的正确性另有测试，不属于这个性能样本。

管理员导入来源优化使用同进程、同合成 fixture 的冻结前算法与最终算法对照，
基线是当时本地候选树的构造函数，不是 HEAD。仅填充结构来源证据，不包含
decode、真实引擎配置复制负载、PG 或引擎应用。运行
`BenchmarkUsageImportPublicationPaired -benchtime=300ms -count=5 -benchmem`，
12 组各五次样本，以下为中位数：

| 归属类型 | 规则数 | 前算法 | 最终算法 | 耗时变化 |
| --- | ---: | ---: | ---: | ---: |
| 管理员导入 | 100 | 0.320155 ms | 0.042361 ms | -86.77% |
| 管理员导入 | 500 | 6.923096 ms | 0.264203 ms | -96.18% |
| 管理员导入 | 2000 | 119.448333 ms | 1.115532 ms | -99.07% |
| 未分配导入 | 100 | 0.041213 ms | 0.029919 ms | -27.40% |
| 未分配导入 | 500 | 0.219792 ms | 0.231600 ms | +5.37% |
| 未分配导入 | 2000 | 0.828746 ms | 0.810333 ms | -2.22% |

收益集中在管理员导入规则消除平方扫描；普通未分配规则没有一致提升，500 条
样本有小幅退步。分配次数不变，中位分配字节基本相同，不宣称内存下降。
短样本受调度与 GC 影响，也不单独归因临时索引 helper 拆分的收益。该视图
构造位于 PG 提交后、节点通知前；这里只量化其中一个环节，不能与前文收益
叠加，也不能据此承诺生产端到端或节点 CPU 降幅。

协议健康合并另以同进程合成 fixture 对照，保留完整 WithOverlays 克隆与合并
在计时范围内。基线关闭结构投影以执行原端点池扫描，其他输入相同；数据准备、
视图发布不计时。首次查找沿用扫描，第二次才按需建立共享索引，避免单条结果
承担整表索引分配。hot 预热两次，cold 每次分配新投影。每组运行
`-benchtime=300ms -count=5 -benchmem`，以下为中位数：

| 池数 / 合并结果数 | 原扫描 | 索引已建立 | 包含首次建索引 |
| --- | ---: | ---: | ---: |
| 100 / 100 | 0.179820 ms | 0.098161 ms | 0.099277 ms |
| 500 / 500 | 2.426243 ms | 0.548277 ms | 0.726927 ms |
| 2000 / 2000 | 39.042744 ms | 2.024467 ms | 2.484399 ms |
| 2000 / 47 | 1.619476 ms | 0.996737 ms | 1.201189 ms |
| 2000 / 1 | 1.132215 ms | 1.011331 ms | 1.120206 ms |

2000 条批量结果的局部耗时分别下降 94.81% / 93.64%；47 条分别下降
38.45% / 25.83%。中间版本发现单条首次建索引有退步，已增加首次扫描保护，
最终单条 cold 约持平（-1.06%），不能宣称单条稳定加速。
单条的整份 overlay 克隆仍占主要开销，后续可研究精确复制，但必须保留
请求隔离，不得共享会被健康写入修改的成员和证据 map。

hot 的分配次数与扫描基本相同；cold 的额外分配字节在 100 / 500 / 2000 池
分别约为 5,512 / 49,289 / 196,871 B/op，2000 池多 11 次分配；单条不建整表
索引，只增加约 46 B/op、1 次分配（benchmark 中新投影的成本）。索引未使用时
不会建立；一次建立后随该不可变结构共享，结构重新发布后重新建立。这里是
局部分配量，未测进程保留内存、生产 CPU、数据库锁时间或端到端提速。
这些短样本含 GC/调度波动，不与前述其他批次收益叠加。

健康续租复制采用同进程冻结前算法与最终算法对照，两者使用同一完整证据
校验，且同样预热 evaluator 与池索引。42 条规则 × 28 个节点的 fixture 包含
每个成员已持久化的旧健康结果；构造 fixture、第一次 overlay 和预热不计时，
只计 CheckProtocolHealthRenewal 校验阶段。运行
`BenchmarkProtocolHealthRenewalCopy -benchtime=300ms -count=5 -benchmem`，
14 组各五次样本，以下均为中位数：

| 场景 | 前算法 | 最终算法 | 耗时变化 | 分配字节：前 / 后 |
| --- | ---: | ---: | ---: | ---: |
| 空批次 | 0.291525 ms | 0.000357 ms | -99.88% | 511,881 / 576 |
| 单条续租 | 0.467395 ms | 0.017024 ms | -96.36% | 513,658 / 17,296 |
| 同池 28 条 | 0.358792 ms | 0.088517 ms | -75.33% | 561,609 / 65,248 |
| 跨两池 32 条 | 0.378487 ms | 0.106913 ms | -71.75% | 568,714 / 83,280 |
| 全部 1176 条 | 3.925767 ms | 3.768289 ms | -4.01% | 2,600,723 / 2,555,613 |
| 32 条全部拒绝 | 0.413336 ms | 0.069962 ms | -83.07% | 553,610 / 42,304 |
| 500 池、每池一节点，单条续租 | 0.569501 ms | 0.043272 ms | -92.40% | 885,158 / 58,144 |

单条续租分配字节下降 96.63%，分配次数从 2801 降到 100；32 条批次分配字节
下降 85.36%，分配次数从 3545 降到 908。全部成员都需要续租时仍必须逐条验证
并复制所有涉及的池，因此该场景耗时仅下降 4.01%，分配字节仅下降 1.73%。
不能将稀疏批次收益推广到全量批次。PG 的第一份 overlay、数据库操作、真实
协议挑战和引擎应用均不在本测量内；没有生产 CPU、保留内存或端到端保证。
样本含 GC 与调度波动，各项优化百分比不能相加。

健康结果数据库批量写入使用本机 loopback 隔离 PostgreSQL，与测试中冻结的
原逐条写入算法交替对照。每种规模各 20 对样本，剔除三对预热；每个样本是
32 次事务（单条）或 8 次事务（其余规模）的平均耗时，再对 20 个样本取中位数。
两种算法使用同一 schema、相同已存在的证明行，量度范围为 BeginTx、取得
快照行锁、写证明、更新快照 revision、Commit；不含证据校验或快照装载。

| 健康结果条数 | 原逐条事务 | 批量事务 | 局部耗时降幅 | 证明写入 SQL：前 / 后 |
| --- | ---: | ---: | ---: | ---: |
| 1 | 0.356600 ms | 0.355079 ms | 0.43% | 1 / 1 |
| 47 | 3.700756 ms | 0.776212 ms | 79.03% | 47 / 1 |
| 128 | 9.854962 ms | 1.435587 ms | 85.43% | 128 / 1 |
| 257 | 21.237400 ms | 2.704444 ms | 87.27% | 257 / 3 |

单条没有可确认的实质加速，保留单条常量 SQL，避免为它构建多行占位符。
Windows 极短计时可见约半毫秒粒度，原单次事务测量含零值，已不采用；最终
160 个样本使用上述组内平均。记录中的 locked_section 是客户端收到行锁查询
返回至收到 Commit 返回的时间，不能当作服务器精确持锁时长。这里没有证明
生产 CPU 降低或整个下发流程同幅加速，也不与前 14 项的百分比叠加。

## 已执行验收

- `go test ./... -count=1 -timeout=180s`：通过，设置隔离 PostgreSQL 测试库；
  未提供真实引擎环境变量的可选集成项会跳过，不算真实引擎验收。
- `go vet ./...`、`git diff --check`：通过。
- 新增路径并发检查：controlclient、reconciler、runtime、state、generations、
  protocolprobe、gatewaymembership 通过；memoryrepo、postgressnapshot 回执
  测试及 HTTP 批量认证/证据测试的定向 race 检查通过。
- PostgreSQL 提交阶段注入失败：两条回执、revision、审计、通知均不泄漏；
  冷重开后成功批次的两条证据仍在。依赖 UDP 编译失败也整批回滚，可修复后重试。
- 编译器回归：撤销、暂停、额度、用户到期、密钥轮换、SNI 修改、成员退出、
  不相干损坏材料、节点能力与限速限制均覆盖。
- 网络阻塞回归：后台批量上报卡住时，本地恢复与下一代 Apply 仍能返回，
  新一代已落盘；取消网络上报不会丢失两代四条回执。
- 真实 Xray 26.7.28、全 loopback：
  `TestRuleActivationNotificationsWithRealAgentAndRealityTwice` 通过。
  两轮从保存到节点验证、协议激活、成员路由、客户流量通过分别为
  1.0658623 s 和 1.0032491 s；fallback 设置 1 小时以验证即时通知。
  此 fixture 使用内存仓库，不等同于生产 PostgreSQL 链路。
- 真实 Xray 的 `TestProtocolProbeRealRealityVisionEchoLoopback` 通过：相同规则
  第二次探测未增加挑战，保留同一 verifiedAt；配置指纹改变后增加一次真实
  挑战；错误 UUID 被拒绝。未进行苹果真机或公网客户端兼容性验收。

上述整树测试之后增加的回执锁优化已补跑 reconciler/runtime 普通及 race
测试；新增上下文早退已补跑 generations/HTTP 相关测试。真实探测缓存断言也已
单独重新执行，不以改动之前的整树结果替代新增代码验证。

后续按需校验及事件唤醒的本轮复验：

- 新增测试通过：32 并发首次校验、共享缓存已预热仍拒绝损坏数据、同键坏记录
  不覆盖有效证据；补报启动恢复、忙锁通知、失败通知合并、恢复通知、同步
  成功无额外通知、flush 期间的新通知、无重叠补报、失败退避与取消、周期兜底。
- `go test ./... -count=1 -timeout=180s` 再次通过，使用上述隔离 PostgreSQL。
  此轮之后仅调整延迟状态的数组分配；该调整已补跑 evaluator 全测试和
  benchmark、evaluator/ForwardingReadView 定向 race、PG 配置读取和批量回执
  持久化/提交失败回滚测试。
- deploymentreceipts、reconciler、runtime 三包 race 通过；
  `go vet ./...`、三个入口的 `go build`、`git diff --check` 通过。
- 真实 Xray 26.7.28 全 loopback 两项测试再次通过。保存至客户流量通过两轮为
  1.1528943 s / 2.2593169 s；运行时同时有整树和 race 检查，不能与前轮耗时
  直接当性能对照。协议挑战缓存仍保留原 verifiedAt，身份改变后必须新挑战。
- 未提交、推送、发布或操作生产；未做公网和苹果真机验收。

上述两项复制优化之后的补充验收：

- memoryrepo 全包测试通过；ForwardingReadView、部署输入、权限索引、连接
  权限与 UsageReadView 定向 race 通过。原始来源 JSON 被损坏或删除时，新视图
  拒绝授权，旧视图及 overlay 仍与源字节隔离；已有旧代配置回退测试继续通过。
- 隔离 PostgreSQL 定向测试通过：同代批量回执持久化/提交失败回滚、配置读取
  不载入无关 overlay、数据库修复/删除/失败、提交后通知、12 并发写入不丢失、
  未分配主体权威校验、连接与配置来源、成功写入填充缓存、失败写入不发布缓存。
- memoryrepo/postgressnapshot 的 `go vet` 与 `git diff --check` 通过。
- 本次仍未提交、发布或升级；未重新进行公网及苹果真机验收。

管理员导入来源优化的最终代码补充验收（含 helper 拆分及三条验证路径）：

- memoryrepo 全包通过（29.815 s）；UsageImported、UsageReadView、
  ForwardingProjection、ForwardingReadView 定向 race 通过（6.059 s）。
- 三路对照：权威 Store、UsageReadView、ForwardingReadView 的主体校验均覆盖
  管理员删除、缺失/损坏来源、归属错误、端点/绑定身份及组/端口不匹配、
  批次/行号不匹配、过长主体、客户导入和合法未分配规则；新视图撤销授权不影响
  已发布旧视图的隔离性。
- 隔离 PG 八个父测试实际通过（3.221 s），包括同代回执整批持久化/提交失败、
  缓存随 overlay 与数据库修复更新、提交后通知、主体权威与管理员来源校验、
  成功提交发布缓存、操作和延迟提交失败不发布缓存；不是跳过测试的空通过。
- memoryrepo/postgressnapshot 的 `go vet`、`git diff --check` 通过。
- 最终代码未改生产数据、协议身份或订阅；无提交、推送、发布、升级操作。

协议健康端点索引补充验收：

- 新增对照覆盖：空规则、缺失池、唯一池、三个重复池、按 map key 解析、
  新发布视图随删除/重绑定/冲突修复更新、旧视图及 overlay 隔离。
- 32 并发首次查找、重复池合并/续租拒绝、有效证据接受、旧规则修订、错代次、
  错 hash、错地址、超长租期、过期证据拒绝；只读验证不修改原视图。
- memoryrepo 全包通过（29.275 s）；新索引、重复池、ForwardingReadView 与
  ForwardingProjection 定向 race 通过（4.876 s）。
- 隔离 PostgreSQL 八个父测试实际通过（2.669 s）：健康续租不重写快照、
  租期丢失后续租、并发业务写入和回滚、编辑拒绝旧证明、跨实例缓存失效、
  overlay/修复/数据库故障、提交后缓存以及失败操作/延迟提交不发布缓存。
- memoryrepo/postgressnapshot 的 go vet 与 git diff --check 通过；benchmark
  每组检查所有结果均合并、原视图及未采样池没有被修改。
- 新增测试与 benchmark 源码真实保存在
  `C:/Users/hf/Documents/Codex/2026-10-16/codex-threads-01a10f3e-8cf5-7312-b6c9/work/hl-usage-throughput-20261021/internal/control/memoryrepo/endpoint_pool_projection_test.go`；
  新索引实现为同目录 `endpoint_pool_projection.go`，接入文件为
  `forwarding_projection.go` 与 `gateway_membership_repository.go`。
- 原始 75 个样本及计算后 JSON 真实保存在
  `C:/Users/hf/Documents/Codex/2026-10-16/codex-threads-01a10f3e-8cf5-7312-b6c9/outputs/hl-usage-throughput-20261022/endpoint-pool-merge-optimization-20261022.json`；
  同目录最终原始输出为 `endpoint-pool-merge-final-paired-20261022.txt`；
  其他两个 paired 输出为中间版本，不计入最终统计。解析脚本为
  `summarize_endpoint_pool_merge.py`。脚本已实际执行，JSON 已重新解析并校验
  15 组、每组 5 个样本。三份 verification 输出也在同一目录。
- 未提交、推送、发布或升级；未改生产，无需生产回滚。新索引只涉及上述四个
  Go 文件；撤回本项时需保留 forwarding_projection.go 的前序未提交优化。

健康续租按池复制的最终代码验收：

- memoryrepo 全包通过（29.387 s）；ProtocolHealthRenewal、端点池索引、
  ForwardingReadView、ForwardingProjection、PublishProtocolHealth 定向 race
  通过（5.123 s）。
- 冻结前算法逐条错误对照，覆盖空批次、同池多节点、重复/过旧/更新结果、
  错修订、缺失规则、全拒绝、重复池、首次激活；原视图成员、健康证明、规则和
  revision 不变。通过首条后取消不泄漏，16 并发 overlay 互不写入。
- 隔离 PostgreSQL 八个父测试实际通过（3.021 s），不是跳过：续租/租期恢复
  不重写快照、并发业务写入与回滚、编辑拒绝旧证明、跨实例缓存失效、
  overlay/修复/数据库故障、提交后缓存、操作及延迟提交失败不发布缓存。
- memoryrepo/postgressnapshot 的 `go vet` 与 `git diff --check` 通过。
- 最终候选又执行 `go test ./... -count=1 -timeout=180s`：70 个有测试的包
  通过，使用隔离 PostgreSQL。memoryrepo 为 30.263 s，postgressnapshot 为
  37.747 s。可选真实引擎环境变量未提供，不能把该轮的集成包通过当成真实
  Xray/GOST 验收；未做公网或苹果真机测试。
- 70 个 benchmark 原始样本、三份专项测试日志及静态检查记录已解析校验，
  `renewal-copy-optimization-20261022.json` 已重新打开并断言 14 组各 5 个样本。
- 未提交、推送、发布、升级、重启节点或修改生产数据；不改变订阅、协议身份
  或 Xray 版本。本轮没有生产回滚需求。

健康结果批量写入的最终代码验收：

- 专项隔离 PostgreSQL 六个父测试通过（3.351 s），覆盖 0/1/47/128/129/257 条、
  SQL 次数上限、完整字段持久化、重复接受/拒绝顺序、含特殊字符的独立复合键。
- 第二批 SQL 失败、第二批 JSON 编码失败均撤回第一批及投影 revision。真实续租
  入口的延迟 Commit 失败不改变快照、证明、审计或对外读取；原实例、另一实例
  及冷重开均保持旧状态。成功续租仅 revision 加一，快照正文及激活审计不变。
- 129 条结果跨批写入后，新事务看到全部最新证明，旧 RepeatableRead 事务保持
  旧视图，旧事务读取不能把新缓存覆盖为旧数据。
- 定向 `go test -race ./internal/control/postgressnapshot` 通过（22.232 s），
  包含上述正确性测试、并发业务编辑/回滚、旧部署证明拒绝、跨实例投影缓存。
  性能测试在该轮按设计跳过，不将 race 的计时用于性能结论。
- 最终 `go test ./... -count=1 -timeout=180s` 使用隔离 PG，70 个有测试的包
  全部通过；memoryrepo 29.763 s，postgressnapshot 37.696 s。该轮没有进行
  公网、苹果真机或真实引擎全链路验收。
- `go vet ./internal/control/postgressnapshot ./internal/control/memoryrepo` 与
  `git diff --check` 通过。没有提交、推送、发布、升级或生产操作。

## 第 16 项：运行状态覆盖层按需复制

`ForwardingReadView.WithOverlays` 原来无条件复制所有节点、规则，以及所有池的
成员与健康证明双层 map。现在空覆盖层直接共享私有不可变输入；有流量/心跳
时只复制对应 map；健康恢复只有通过原有顺序检查、准备写入时才复制对应池。
仍保留 traffic → health → telemetry 的合并次序，过期租约不会延长，也不把
已提交证明的恢复误作新证据校验。公开 DTO 继续防御性复制。

同进程、同合成 fixture，每场景先执行完整复制 5 次，再执行选择复制 5 次；
共 110 个原始样本，不是严格交替成对计时。Windows amd64，AMD Ryzen Z1 Extreme，
`-benchtime=150ms -count=5 -benchmem`，中位数如下：

| 覆盖层场景 | 原阶段耗时 ms | 新阶段耗时 ms | 原分配 B/op | 新分配 B/op |
| --- | ---: | ---: | ---: | ---: |
| 空覆盖层 | 0.316810 | 0.000391 | 511881 | 584 |
| 流量 | 0.344548 | 0.026489 | 511880 | 34720 |
| 单条健康 | 0.418903 | 0.011373 | 512129 | 15776 |
| 同池 28 条健康 | 0.225232 | 0.021183 | 518824 | 22472 |
| 32 条健康 | 0.323395 | 0.025907 | 519817 | 34392 |
| 全部 1176 条健康 | 0.817976 | 0.796070 | 803530 | 758433 |
| 32 条被忽略的健康结果 | 0.239064 | 0.011098 | 519049 | 7752 |
| 节点遥测 | 0.224759 | 0.007261 | 513930 | 15924 |
| 少量混合结果 | 0.237889 | 0.052240 | 521884 | 84091 |
| 全量混合结果 | 0.783662 | 0.798008 | 806143 | 809632 |
| 500 池、单条健康 | 0.300369 | 0.015473 | 883627 | 56624 |

只测覆盖层恢复/复制阶段，不含数据库、证据校验、网络或引擎。少量结果时减少
无关复制；全部健康结果仅快约 2.7%，全量混合结果反而慢约 1.8%、分配增加
约 0.4%，属于该测量范围内的小幅退步，不能隐去。生产 PostgreSQL 读取可能
包含全部健康覆盖层，不能把 32 条的约 92% 阶段降幅换算为全链路提速或生产
CPU 降幅。没有为降低 CPU 增加探测频率，也没有改协议、订阅、节点身份或引擎版本。

本项实际验收：

- 冻结旧完整算法的独立 oracle 对照 27 类边界，涵盖流量修订/期间/配额、健康
  恢复顺序/重复池/缺失成员/过期证明、节点凭证/心跳/删除状态和混合结果。
- 连续 overlay、兄弟视图、原视图不变、输入和公开返回值的嵌套指针隔离，
  以及 16 并发 sibling/chain 检查通过。修改前的续租复制基准使用独立完整
  复制 helper，避免新共享行为污染冻结对照。
- 定向普通测试通过；定向 race 通过（3.443 s）。最终 `go test ./... -count=1
  -timeout=180s` 使用隔离 PostgreSQL，70 个有测试的包全部通过，memoryrepo
  32.266 s，postgressnapshot 41.764 s。可选真实引擎环境未配置，未进行公网或
  苹果真机验收。
- 隔离 PostgreSQL 八个父测试实际运行并通过（2.565 s），覆盖续租/租期恢复、
  并发业务写入和回滚、旧证明拒绝、跨实例缓存、数据库修复/故障、成功提交
  发布缓存、操作和延迟 Commit 失败不发布缓存；无跳过。
- memoryrepo/postgressnapshot 的 `go vet`、`git diff --check` 通过。110 个原始
  样本已解析，JSON 重新打开并断言 22 组各 5 条。

真实文件位置：

- 源码目录：`C:/Users/hf/Documents/Codex/2026-10-16/codex-threads-01a10f3e-8cf5-7312-b6c9/work/hl-usage-throughput-20261021/internal/control/memoryrepo/`。
  修改 `forwarding_read_view.go`、`protocol_health_overlay.go`、
  `protocol_health_renewal_copy_test.go`，新增 `forwarding_overlay_copy_test.go`。
- 产物目录：`C:/Users/hf/Documents/Codex/2026-10-16/codex-threads-01a10f3e-8cf5-7312-b6c9/outputs/hl-usage-throughput-20261022/`。
  原始样本 `overlay-selective-paired-20261022.txt`，计算结果
  `overlay-selective-optimization-20261022.json`，解析脚本
  `summarize_overlay_selective.py`，验证日志为 `overlay-selective-` 开头的
  `race/all/pg/static-verification-20261022.txt`。脚本已执行，报告已文本复核。
- 本项没有生产变更，没有提交、推送、发布或升级。回撤仅恢复上述三个已存在
  文件在 `overlay-selective-before/` 的本项修改前备份，删除新增测试；不要整树
  重置或整文件恢复 HEAD，否则会丢失第 1–15 项优化。无 schema/数据格式变化。

## 17. 合并流量与健康版本读取，减少持锁期间数据库往返

同一次 forwarding 读取、协议健康续租、完整快照读取和业务写入，原本分别
查询 `rule_traffic`、`protocol_health` 失效版本。本项在同一事务内用一个
SQL 同时读取两个版本，仍按各自版本独立复用/失效缓存。缓存命中时版本查询
由两次降为一次；缓存失效后仍需正常读取对应投影表，没有跳过必要数据。
完整快照写入原本在拿到业务行锁后做两次版本查询，现在也减少一次持锁往返。
只需要流量的 usage 读取保留单独查询；配置读取继续不加载无关遥测投影。

一致性与异常边界：

- 版本读取和数据读取继续使用调用方事务；读取维持 Repeatable Read，写入
  维持已有业务行锁。没有去锁、改变隔离级别、加轮询或扩大工作并发。
- 慢旧事务只能返回自己的数据库视图，不能覆盖更高版本缓存；独立连接更新
  流量或健康时，只失效对应缓存；返回切片仍与共享缓存分离。
- marker 缺失即失败，已有缓存也不能兜底；健康 JSON 损坏时不返回部分结果。
  完整读取/写入遇到该异常也拒绝操作，权威快照及 revision 不变。
- 未提交写入回滚后，缓存继续对应原已提交状态；不改变格式、schema、探测
  周期、客户协议、规则、订阅、UUID、URL 或 Xray 版本。

实测使用独立 loopback PostgreSQL，预热后交替运行先旧后新、先新后旧；
每模式 20 个样本，每样本 32 次请求。fixture、预热不计入样本，原始输出
共 120 条。下表为缓存投影读取阶段的中位数，单位 ms：

| 每类投影条目 | 分别查版本 | 合并查版本 | 本阶段减少 |
| --- | ---: | ---: | ---: |
| 0 | 0.080348 | 0.040134 | 50.0% |
| 269 | 0.112761 | 0.080056 | 29.0% |
| 2000 | 0.306875 | 0.226309 | 26.3% |

同次微型事务包含 BEGIN/COMMIT 的中位数，分别为 0.144694→0.112304、
0.178126→0.145987、0.388158→0.307559 ms，减少约 22.4%、18.0%、20.8%。
这不是完整业务事务或端到端下发时间，也没有测生产 CPU；不能据此宣称
客户开通、全链路下发或整个面板同幅度提速。减少一次 SQL 往返是确定改变，
不同数据库距离、缓存命中率和其他阶段占比会影响最终收益。

必要验证已实际执行：

- 真实隔离 PG：新旧事务视图、缓存独立失效、返回值污染、回滚、缺 marker、
  错误 JSON、完整快照拒绝异常，以及既有配置读取/健康批处理回归。
- 上述专项 race 检查、`go test ./... -count=1 -timeout=180s`、
  `go vet ./...`、`git diff --check`。具体退出状态和日志以本项产物为准。
- 测量脚本已执行，JSON 已重新打开校验 6 组各 20 条，无失败或跳过样本。

真实文件位置：

- 源码目录：`C:/Users/hf/Documents/Codex/2026-10-16/codex-threads-01a10f3e-8cf5-7312-b6c9/work/hl-usage-throughput-20261021/internal/control/postgressnapshot/`。
  修改 `projection_cache.go`、`forwarding_read_repository.go`、
  `protocol_health_overlay.go`、`store.go`；新增
  `forwarding_overlays_integration_test.go`、`forwarding_overlays_performance_test.go`。
- 产物目录：`C:/Users/hf/Documents/Codex/2026-10-16/codex-threads-01a10f3e-8cf5-7312-b6c9/outputs/hl-usage-throughput-20261022/`。
  原始样本 `projection-revision-final-paired-20261022.txt`，汇总
  `projection-revision-optimization-20261022.json`，解析脚本
  `summarize_projection_revision.py`；验证日志为
  `projection-revision-pg/race/all/static-verification-20261022.txt`。
- 本项之前的四个源码备份真实保存在该产物目录的
  `projection-revision-before/`。若只撤回本项，恢复这四个备份，删除本项新增
  两个测试文件；不要整树 reset 或整文件恢复 HEAD，以免丢失前 16 项优化。
- 本项没有提交、推送、发布、升级或生产数据变更。独立 service-owner 删除
  候选包在另一工作区构建，不包含本性能批次。

## 18. 投影缓存刷新少复制一次，缩短共享锁范围

继续检查第 17 项调用链后，发现 traffic/health 缓存失效时，原实现先把 SQL
返回的私有切片复制进缓存，再把缓存复制回来。现在保留 SQL 返回值给当前
调用方，只构造一份缓存副本；缓存命中时，在读锁内取得切片 header，释放锁
后再复制。发布的缓存切片保持不可变、只做整体替换，不允许调用方修改它。
没有直接返回共享切片、移除版本校验、改变事务隔离或降低探测要求。

一致性边界：

- 旧事务仍读自己的 PostgreSQL 视图，不能覆盖更高版本缓存；当初始检查已经
  看见更新缓存时，旧事务直接返回其 SQL 私有结果，不为不能发布的数据复制。
- 相同版本并发 miss 各自返回私有 SQL 数据；版本推进后的发布仍在写锁内
  重新比较，不能用慢查询覆盖新缓存。流量/健康仍独立失效。
- 空表仍发布非 nil 的空切片，以区别未加载缓存；错误查询不发布缓存。
- 缓存返回值可被调用者修改而不污染其他读者；未改变规则、订阅、配置
  generation、UUID、Reality、URL、Xray 版本或数据库 schema。

对照测试固定本项修改前的两个方法，仅测试代码包含旧实现。独立 loopback
PostgreSQL 使用 0/269/2000 条合成 traffic 及同量 health；预热后交替先旧后新、
先新后旧，每模式 20 个样本。cold 每样本 8 次，hot 每样本 128 次，共 240
条原始样本。cold 在每次读取前清空进程缓存，事务数据保持不变，测量包含
投影 SQL、解码和复制，不包含 fixture、版本查询或 BEGIN/COMMIT。

缓存失效重载阶段的中位数：

| 每类条目 | 原耗时 ms | 优化后 ms | 耗时减少 | 原分配字节/次 | 优化后字节/次 | 分配减少 |
| --- | ---: | ---: | ---: | ---: | ---: | ---: |
| 269 | 0.903300 | 0.839144 | 7.1% | 748565 | 633413 | 15.4% |
| 2000 | 7.439475 | 6.461844 | 13.1% | 6268329 | 5505071 | 12.2% |

空表 cold 基本持平。hot 的分配量没有下降；269 条耗时约增加 0.34%，2000 条
约减少 2.45%，不据此宣称缓存命中普遍提速。空表 hot 的计时低于本机时钟
可测分辨率，原始记录为 0，JSON 已标记，不代表实际零耗时。内存数据来自
进程级 TotalAlloc/Mallocs 差值，包含驱动和后台开销；不是常驻内存或生产 CPU。
锁内复制已移出是代码事实，本项没有单独量化生产锁等待或端到端下发时间。

实际验证：

- 两项新增真实 PG 测试：6 个并发读者保持不同新旧事务视图，另一个连接
  连续提交 12 批更新；逐条验证全部 128 条数据，并修改每次返回切片；验证
  后续读取、新版本缓存及旧事务均不被污染。另测空缓存可复用。
- 新增测试初次正常执行通过；11 项 PG 专项及其 race 通过（23.670 秒），
  覆盖缓存跨实例失效、事务回滚、缺 marker、坏 JSON、配置/探测读取、健康
  批写提交失败。race 命令内另有 2 个 opt-in 性能测试按开关跳过；本项
  性能测试另行显式开启运行并通过，不把跳过算作性能验收。
- `go test ./... -count=1 -timeout=180s`、`go vet ./...`、
  `git diff --check` 均返回 0。整树普通测试不配置外部引擎/数据库环境，相关
  opt-in 集成项按各自条件跳过；不能据此声称真实引擎或生产全链路验收。
- 统计脚本实际执行，JSON 重新打开检查 12 组各 20 条完整样本。

真实文件位置：

- 源码目录：`C:/Users/hf/Documents/Codex/2026-10-16/codex-threads-01a10f3e-8cf5-7312-b6c9/work/hl-usage-throughput-20261021/internal/control/postgressnapshot/`。
  修改 `projection_cache.go`，新增 `projection_cache_copy_integration_test.go`、
  `projection_cache_copy_performance_test.go`。均实际写入、读取、gofmt 并测试。
- 产物目录：`C:/Users/hf/Documents/Codex/2026-10-16/codex-threads-01a10f3e-8cf5-7312-b6c9/outputs/hl-usage-throughput-20261022/`。
  原始样本 `cache-copy-final-paired-20261022.txt`，统计
  `cache-copy-optimization-20261022.json`，脚本 `summarize_cache_copy.py`、
  `build_cache_copy_performance.py`；验证日志 `cache-copy-all-verification-20261022.txt`、
  `cache-copy-pg-race-verification-20261022.txt`、`cache-copy-static-verification-20261022.txt`。
- 回撤本项：只恢复 `cache-copy-before/projection_cache.go` 备份，删除上述两个
  新测试；该备份包含前 17 项优化。不可整树 reset 或恢复 HEAD。

本项无提交、推送、发布、部署、生产访问或生产数据变更。剩余结构性大头仍是
全快照写放大和单写锁；本项只减少刷新时的复制与共享锁范围，不把单写锁问题
记为已解决。

## 真实慢请求的边界

只读日志中有一次约 60 秒的开通案例，耗时分布在规则持久化、端点和身份建立、
两个节点的应用回执、协议激活及外部调用完成。对应日志窗口出现一次 47 条
协议健康批处理的 `snapshot lock failed`，可确认存在锁竞争，不能把全部
60 秒都归因于这一条错误，或断言本批已经消除这一生产案例。

## 剩余瓶颈与下一批边界

- 权威全快照写放大与单写锁仍在；本批减少了重复计算和一次终态写入，没有
  把现有持久化架构改成多写并发。进一步拆写需要核对事务、历史回执、崩溃恢复
  和迁移/回滚，不能简单去掉锁或把并发调到 50。
- prepare/validate 的成功上报仍各有 750 ms 预算，涉及 attempt 失效边界；
  本批未删掉或延后该证据。失败、回滚和引擎切换仍有自身开销。
- 即时补报跳过忙锁后用合并通知唤醒后台循环，避免正常等待下一次约 5 秒
  周期；网络错误、已有请求等待和失败退避仍有延迟，未宣称故障条件下立即激活。
- desired watch 的约 8 秒等待是跨进程兜底，本进程有即时通知，不是每次固定
  额外等待 8 秒。没有通过更频繁轮询或增加探测 worker 来制造 CPU 压力。
- 本批没有改引擎版本、客户协议参数或订阅数据。正式发布前仍需完整发布流水线、
  安装/升级/回滚检查及真实环境分阶段验收；本地通过不代表线上完成。

原始数字和计算脚本位于当前任务 outputs/hl-usage-throughput-20261022 下的
`dispatch-optimization-batch-20261022.json` 与 `summarize_dispatch_batch.py`。
脚本只汇总已记录结果，不访问生产、不重跑 benchmark。
按需校验的三组原始样本和中位数在 `evaluator-publication-20261022.json`，
当次候选 benchmark 标准输出在 `evaluator-publication-candidate-20261022.txt`，
PG 定向复验标准输出在 `dispatch-final-pg-verification-20261022.txt`。
复制优化的原始样本、阶段对比和限制在 `projection-copy-optimization-20261022.json`，
候选 benchmark 标准输出在 `projection-copy-candidate-20261022.txt`，缓存及连接
读取的 PG 复验输出在 `projection-copy-pg-verification-20261022.txt`。
管理员导入来源优化的最终 60 个原始样本、中位数、变化百分比及限制在
`usage-import-optimization-20261022.json`，原始标准输出为
`usage-import-scoped-final-paired-20261022.txt`；解析脚本为
`summarize_usage_import.py`，只读取已保存输出并校验组数、样本数和 PASS。

本轮真实修改及生成位置（2026-10-22，管理员导入来源优化）：

- 生产源码：`C:/Users/hf/Documents/Codex/2026-10-16/codex-threads-01a10f3e-8cf5-7312-b6c9/work/hl-usage-throughput-20261021/internal/control/memoryrepo/usage_read_view.go`
- 共享来源投影：`C:/Users/hf/Documents/Codex/2026-10-16/codex-threads-01a10f3e-8cf5-7312-b6c9/work/hl-usage-throughput-20261021/internal/control/memoryrepo/forwarding_projection.go`
- 合成基准与权限回归：`C:/Users/hf/Documents/Codex/2026-10-16/codex-threads-01a10f3e-8cf5-7312-b6c9/work/hl-usage-throughput-20261021/internal/control/memoryrepo/usage_import_projection_test.go`
- 本报告：`C:/Users/hf/Documents/Codex/2026-10-16/codex-threads-01a10f3e-8cf5-7312-b6c9/work/hl-usage-throughput-20261021/docs/control-dispatch-optimization-batch-20261022.md`
- 实测 JSON：`C:/Users/hf/Documents/Codex/2026-10-16/codex-threads-01a10f3e-8cf5-7312-b6c9/outputs/hl-usage-throughput-20261022/usage-import-optimization-20261022.json`
- 解析脚本：`C:/Users/hf/Documents/Codex/2026-10-16/codex-threads-01a10f3e-8cf5-7312-b6c9/outputs/hl-usage-throughput-20261022/summarize_usage_import.py`

源码已读取并通过上述测试，脚本已实际运行，JSON 已重新解析并断言 60 个样本；
报告为 Markdown 文本复核。本轮无生产变更，无需生产回滚。

健康续租复制优化的真实文件位置（2026-10-22）：

- 源码文件夹：`C:/Users/hf/Documents/Codex/2026-10-16/codex-threads-01a10f3e-8cf5-7312-b6c9/work/hl-usage-throughput-20261021/internal/control/memoryrepo/`。
  修改 `forwarding_read_view.go`、`gateway_membership_repository.go`，参数化
  `protocol_health_scale_test.go`，新增 `protocol_health_renewal_copy_test.go`。
- 产物文件夹：`C:/Users/hf/Documents/Codex/2026-10-16/codex-threads-01a10f3e-8cf5-7312-b6c9/outputs/hl-usage-throughput-20261022/`。
  原始输出为 `renewal-copy-paired-20261022.txt`；计算产物为
  `renewal-copy-optimization-20261022.json`，解析脚本为
  `summarize_renewal_copy.py`。三份专项日志名称为
  `renewal-copy-memoryrepo-verification-20261022.txt`、
  `renewal-copy-race-verification-20261022.txt`、
  `renewal-copy-pg-verification-20261022.txt`；静态检查记录为
  `renewal-copy-static-verification-20261022.txt`；最终整树日志为
  `optimization-final-all-verification-20261022.txt`。
- 报告已文本复核，脚本已执行，JSON 已重新解析。撤回本项时只撤销续租按池
  复制和 validate/record 拆分、删除本项测试与恢复 fixture 包装；必须保留
  上述文件中第 1–13 项已存在的未提交优化，不能直接整文件恢复 HEAD。

健康结果批量写入的真实文件位置（2026-10-22）：

- 源码文件夹：`C:/Users/hf/Documents/Codex/2026-10-16/codex-threads-01a10f3e-8cf5-7312-b6c9/work/hl-usage-throughput-20261021/internal/control/postgressnapshot/`。
  新增 `protocol_health_batch_write.go`，在 `protocol_health_overlay.go` 替换原
  逐条写入循环。新增测试为 `protocol_health_batch_write_test.go`、
  `protocol_health_batch_integration_test.go`、`protocol_health_batch_performance_test.go`。
  上述文件均真实生成或修改，已经读取、gofmt、测试和静态检查。
- 产物文件夹：`C:/Users/hf/Documents/Codex/2026-10-16/codex-threads-01a10f3e-8cf5-7312-b6c9/outputs/hl-usage-throughput-20261022/`。
  最终原始样本为 `health-batch-final-paired-20261022.txt`，计算产物为
  `health-batch-optimization-20261022.json`，解析脚本为 `summarize_health_batch.py`。
  脚本已实际执行，JSON 已重新解析并断言 8 组各 20 个样本。中间单事务计时
  `health-batch-paired-20261022.txt` 不计入最终统计。
- 实际检查输出分别保存在 `health-batch-initial-verification-20261022.txt`、
  `health-batch-race-verification-20261022.txt`、`health-batch-all-verification-20261022.txt`、
  `health-batch-static-verification-20261022.txt`。报告为 Markdown 文本复核。
- 本轮无生产变更，无需生产回滚。源码回撤只需将 `protocol_health_overlay.go`
  该调用恢复为原逐条参数化写入循环，再删除上述四个新增 Go 文件；不得撤回
  第 1–14 项优化或整树重置。该变更没有新增 schema 或持久数据格式。

## 19. 同一批多节点编译复用 Reality 密钥比较（2026-10-23）

本轮先对第 1–18 项后的工作区做真实 CPU/分配剖析。500 条独立身份规则、
3 台节点的合成编译中，`RealityKeyMatchesPublicKey` 累积约占 CPU 样本
35.71%，`CanonicalJSON` 约占 24.80%。这是多核采样占比，不能解释为生产
CPU 占用率。每节点重复验证同一规则的密钥对，存在同批次可消除的计算。

修改只复用完整私钥/公钥字符串对的纯密码学布尔比较结果；结构、权限、
客户归属、规则与绑定、撤销状态、成员关系、节点能力、原生配置检查仍逐条
执行。memo 仅在 `recompileForwardingGroupsLocked` 的同步持锁调用中创建，
不存入 Store、快照、读视图、日志或返回值，也不跨修改或回滚。仅一台节点时
不创建 memo，继续直接调用原密码学检查。没有修改密钥、UUID、Reality 参数、
订阅、URL、规则、协议、Xray 版本、数据库 schema 或配置编译器修订号。

使用本项修改前/后的两个真实测试程序交替运行，8 轮，每项基准 500 ms，
每条规则独立的合成私钥/公钥和 UUID，共 32 条原始样本。fixture 是现有
SOCKS5 落地的 VLESS Reality 规则，无 usage capability；不使用大量规则
共享一个密钥的有利样本。中位数如下：

| 规模 | 修改前 ms/次 | 修改后 ms/次 | 耗时变化 | 分配字节减少 | 分配次数减少 |
| --- | ---: | ---: | ---: | ---: | ---: |
| 100 条规则 / 1 节点 | 11.186665 | 11.333499 | 增加 1.31% | 增加约 0.004% | 约 0.002% |
| 500 条规则 / 3 节点 | 148.663013 | 103.597487 | 减少 30.31% | 0.65% | 1.82% |

单节点没有预期复用收益，不宣称该场景提速；分配数据不是常驻内存。上述
数字只覆盖内存仓库完整批次编译，不包含数据库锁、快照持久化、网络传输、
排队、节点引擎应用或生产环境，不能宣称完整下发快 30% 或生产 CPU 降 30%。

实际验证：

- 新增五项测试，覆盖完整密钥对的命中/失败、不同公钥不能复用成功、不同
  私钥不能复用成功、原有合法空白/填充行为、预热后撤销/归属/成员变更、
  自动密钥回退、DIRECT、输出字节等价、多节点配置哈希和 generation 稳定、
  下一批次失效、批次中途失败后的全量回滚，以及原生配置损坏仍被拒绝。
- `go test ./... -count=1 -timeout=180s` 返回 0；外部数据库/引擎 opt-in
  项按条件执行或跳过，不视为生产或真实引擎验收。
- 定向 `go test -race ./internal/control/memoryrepo` 返回 0（40.613 秒），
  覆盖本项前四项测试及既有绑定轮换、撤销、原生导入、编译输入权威校验；
  后补的原生配置损坏测试单独普通执行、race 执行均返回 0。
- 隔离 loopback PostgreSQL 四项真实测试通过，无跳过：业务重启/幂等/权限
  回滚、下发配置不依赖运行态 overlay、探测读取修复/删除/数据库失败、
  仅已提交 generation 可唤醒通知。测试使用独立 schema 自动清理。
- `go vet ./...`、`git diff --check` 返回 0。统计脚本实际运行，并重新
  打开 JSON 验证 32 条样本、每组 8 轮与 16 次 PASS。

实际源码位置：

- 文件夹 `C:/Users/hf/Documents/Codex/2026-10-16/codex-threads-01a10f3e-8cf5-7312-b6c9/work/hl-usage-throughput-20261021/internal/control/memoryrepo/`。
- 修改 `node_bundle_compiler.go`、`vless_runtime_repository.go`；新增
  `compilation_reality_keys.go` 和 `compilation_reality_keys_test.go`。

实际产物文件夹：
`C:/Users/hf/Documents/Codex/2026-10-16/codex-threads-01a10f3e-8cf5-7312-b6c9/outputs/hl-usage-throughput-20261023/`。
其中 `reality-batch-paired-20261023.txt` 保存原始样本，
`reality-batch-optimization-20261023.json` 保存样本、中位数、限制和两程序
SHA256，`summarize_reality_batch.py` 为已执行的纯本地解析脚本。
`dispatch-compile-before-{cpu,memory}.pprof`、对应 `*-top.txt` 是实际剖析。
验证日志是 `reality-batch-{all,race,native-race,pg,static}-verification-20261023.txt`。
源码经过读取/gofmt/编译/测试，JSON 重新打开解析，报告经过文本复核。

只撤回本项时，用产物目录 `reality-batch-before/` 的两个文件恢复上述两份
生产源码，再删除本项新增两个 Go 文件；这两个备份已经包含第 1–18 项。
不得恢复 HEAD 或整树 reset。本轮未提交、推送、发布、升级或修改生产。

后续仍需测量 JSON 重复编解码与全快照写放大。不能无限承诺每轮都能提速：
收益不足或风险高于收益的改动应舍弃，不以降低校验、延迟回执、增加探测频率
或改变客户协议参数制造性能数字。
