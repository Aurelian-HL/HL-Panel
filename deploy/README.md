# HL-panel 一键全新安装

此流程面向一台全新 Debian 12/Ubuntu x86_64 主机。授权版安装必须填写自己的管理域名、
提供 HT 发放的授权码并验证 DNS TXT；域名 A 记录可以稍后生效，期间可使用临时 IP 入口。
授权的发放、旧面板首次接入、续期与身份备份见[面板域名授权指南](panel-license.md)。
安装会创建新的 PostgreSQL 空数据库，不导入旧面板、
旧 NY 或任何备份，也不会修改 DNS、停用网络组件或清理其他应用。

## 发布与安装

1. 将源码推送到 `Aurelian-HL/HL-Panel`。
2. 推送 `v*` 标签。GitHub Actions 会运行 Go 与管理端测试，构建 Linux amd64
   程序，生成文件摘要；默认账号和自定义账号分别在 Ubuntu 22.04 / 24.04 全新临时虚拟机安装，
   验证真实 PostgreSQL/systemd/Nginx、改密、会话撤销、重启持久化和 IP HTTPS。
   全新安装及旧版本保数据升级、失败回滚测试通过后，才发布 `hl-panel-linux-amd64.tar.gz` 与 SHA256 文件。
3. 在新主机执行以下命令：

```sh
curl -fsSL https://raw.githubusercontent.com/Aurelian-HL/HL-Panel/main/install.sh \
  | sudo bash -s -- --domain panel.example.com
```

入口默认读取该仓库的最新正式发布，固定标签后下载同标签的安装器和归档，避免
main 脚本与旧安装包混用。也可以通过 `--repo OWNER/REPOSITORY` 切换仓库，
通过 `--version TAG` 固定版本。源码更新要推送 GitHub；安装行为更新必须经测试
后发布新标签，不覆盖已发布标签或归档。

未安装 Nginx 时使用 `nginx-core`，避免 Ubuntu 22.04 的 `nginx-light` 缺少登录限流模块。
已有 Nginx 会以隔离临时配置执行 `nginx -t`，检查登录限流、SSL、HTTP/2 和代理能力；
检测不监听测试端口。发行版 `nginx-light` 模块不足时，先备份 `/etc/nginx` 和软件包版本，
再自动安装 `nginx-core`，保留既有配置；检查通过后继续安装面板。原服务正在运行时会短暂重启，
以加载新程序；适配失败自动恢复原软件包类型、配置和服务状态。备份与恢复脚本保留在
`/var/backups/hl-panel/nginx-adapt.*`，仅 root 可读。恢复软件包类型使用当前软件源的版本。
自编译或第三方 Nginx 模块不足时会提示兼容要求，不强行覆盖。
发布流程在 Ubuntu 22.04 实际安装 `nginx-light`，验证自动适配、原站点仍可访问和完整安装。

安装会询问账号与密码，支持自定义账号（也可用 `--admin-username NAME`）。
直接回车采用 **admin / 123456**，安装结束明确显示默认凭据与改密提示。
默认密码账号首次登录只能读取个人资料和修改密码，其他管理员 API 返回 403；
页面自动转到个人中心。新密码至少 8 个字符，成功后撤销该管理员全部会话，
使用新密码重新登录。安装时设置非默认自定义密码则可正常进入面板；自定义密码
隐藏输入并要求确认，不显示、不写入日志。密码仅以 PBKDF2-SHA256 哈希存储。
初始配置只在创建空数据库时生效；后续重启不会覆盖已修改的管理员密码。

API 默认只监听 `127.0.0.1:8080`。如果主机已有服务占用 8080，请使用
`--api-port 8081` 等空闲端口；安装器会在创建文件或运行 apt 之前检查 IPv4/IPv6
TCP 监听端口，有冲突就拒绝安装，不会停止或修改原有服务。例如：

```sh
curl -fsSL https://raw.githubusercontent.com/Aurelian-HL/HL-Panel/main/install.sh \
  | sudo bash -s -- --domain panel.example.com --api-port 8081
```

服务配置、HL-panel 独立 Nginx 代理和本机健康检查会统一使用所选端口。
`--api-port` 仅允许 1 到 65535 的十进制整数，API 始终只绑定 loopback。
IP HTTPS 使用独立端口 `8443`，可用 `--ip-https-port 9443` 等空闲端口调整。
两项端口参数都会规范化十进制输入并预检 IPv4/IPv6 TCP 监听冲突。
IP HTTPS 端口不得为 80、443 或 API 端口；检测到监听冲突时不会接管已有端口。

仅 Debian/Ubuntu apt 系统和 Linux amd64 提供一键流程。安装器先确认 HL-panel
目录、服务、数据库角色及数据库名称没有被占用，再下载固定 release 并校验归档
摘要和归档内文件摘要。随后安装 Nginx、Certbot、PostgreSQL 与必要工具，创建
专用服务账号、独立目录和空数据库，执行 usage schema 初始化，配置 systemd 与
Nginx，启动 API 并检查本机健康接口。

## 域名与 IP 入口

安装器不会改 DNS。它检查所填写域名的 A 和 AAAA 记录：只有 A 记录
全部指向本机公网 IPv4 且没有 AAAA 记录时，才会申请正式证书。
解析按 IPv4/IPv6 地址族分别查询，不将 IPv4 映射地址误判为 AAAA。临时 DNS
故障或其他查询错误会阻止证书申请；只有明确无该地址族记录才按空记录处理。
安装器会安装 Python 3 作为该检查的运行依赖。

如果域名尚未解析到新主机，安装结束时会提醒需要修改的 A 记录，并提供
`https://<公网IPv4>:8443/` 入口（若指定 `--ip-https-port`，使用指定端口）。
IP HTTPS 只在这个独立端口设置默认 TLS 站点，可接收不发送 SNI 的 IP 直连，
不改变已有 443 默认站点。IP 的 HTTP 入口会跳转到对应 IP HTTPS 端口；
域名 HTTPS 仍使用 443。请在主机供应商安全组中允许所选 IP HTTPS TCP 端口；
安装器不会修改防火墙或安全组。IP 证书为本机生成的自签名证书，浏览器会显示证书
警告；它用于临时直连，不等同于受信任的域名证书。若无法自动确认公网 IPv4，
请在执行命令时传入 `--public-ip <公网IPv4>`。安装器不会把 `hostname -I` 的
私网地址误报为公网入口。

### 后续启用域名证书

授权版安装时已经保存管理域名。将该域名的 A 记录指向本机，DNS 生效后，
在主机运行以下命令申请证书；DNS TXT 授权证明与网站 A 记录是两个独立步骤。
旧 IP 面板首次接入授权或需要更换管理域名时，先按[授权指南](panel-license.md)处理，
不要只改 Nginx 域名或删除授权身份。

```sh
sudo hl-panel-enable-domain-tls
```

可选传入通知邮箱：`sudo hl-panel-enable-domain-tls --email admin@example.com`。
成功启用证书后会启动 `certbot.timer`；若系统未提供该定时器，会提示自行配置定期续期。可用 `systemctl status certbot.timer --no-pager` 检查，并用 `certbot renew --dry-run` 验证续期。DNS 后来生效不会自动触发第一次签发，请运行上述命令。

Certbot 续期后只替换 HL-panel 自己的证书，并在 Nginx 配置检查通过后 reload。
IP HTTPS 入口会继续保留，所选端口保存在 `/etc/hl-panel/domain.conf`，
TLS helper 会输出包含端口的实际 IP 地址。
ACME 挑战文件放在独立 `/var/lib/hl-panel-acme/`，由 root 管理且目录权限为
0755，供 Nginx 读取。服务私有状态 `/var/lib/hl-panel/` 仍由 `hlpanel` 管理、
权限为 0750；不会向 Nginx 开放私有状态，也不会将其加入 `hlpanel` 组。
安装器拒绝覆盖已有 ACME 路径，失败回滚仅移除本轮新建的受管目录。

## 节点一键安装

入口和出口节点使用独立的 `install-node.sh`。在面板设备组的“节点对接”弹窗生成并复制完整安装命令，在目标节点的 root 终端执行即可。命令包含所选组的一次性令牌及面板 HTTPS 地址，自动安装 Agent、HL 主机探针、Xray、GOST，注册入组并启用开机自启；没有第二次输入令牌或分开安装引擎的步骤。

当前源码构建的引擎基线为（已发布资产需单独核对）：Xray v26.7.28、GOST v3.3.1-nightly.20260922，来源与锁定摘要见包内 `licenses/node-engines.json`。混合模式只在面板下发有效配置后启动对应引擎。服务使用 hl-edge 账号，默认监听端口需不低于 1024，不调整其他服务、防火墙或系统转发规则。升级既有节点前，先升级面板并确认节点已应用兼容配置，再分批更新节点；具体顺序见[内核兼容性维护](../docs/xray-compatibility.md)。

程序在 `/opt/hl-panel/edge-agent`，引擎在 `/opt/hl-panel/node-engines/`，配置在 `/etc/hl-panel/edge-agent.json`，私有状态在 `/var/lib/hl-panel-edge/`。在线入口默认从面板 HTTPS 的公开版本接口选定标签，再下载同标签安装器和程序包；可用 `--version vX.Y.Z` 固定版本。入口、脚本与安装包下载均有有限重试和超时。

注册最多等待 90 秒，每 15 秒提示进度；网络中断、408、429 和服务端暂时错误会退避重试。令牌被拒绝、证书错误或私有状态损坏会停止。注册失败停止服务并移除一次性令牌环境文件，保留安装与私有恢复状态；检查原因后重跑原命令。只有令牌过期或失效才重新生成。若面板已升级，添加 `--version vX.Y.Z` 指定最初安装版本，避免覆盖未完成的安装。

注册请求发送前，Agent 将独立随机的私有恢复秘密及令牌摘要保存到 `enrollment-attempt.json`（0600），不保存原令牌。注册响应丢失时，原机器可在首次注册后 24 小时内恢复同一身份，不重复创建节点、入组或审计。其他机器或不同恢复秘密不能重放已消费令牌。注册成功后删除恢复文件与一次性令牌环境文件，后续使用持久节点凭据。

v0.1.42 起，已有受管节点也可重新运行完整安装命令：在目标组生成新令牌，直接在原机执行。面板同时校验本机持久身份和组令牌，在事务内撤下其他组成员、加入目标组并重新编译完整规则，保留节点 ID、私有凭据、流量账本和历史记录。同命令重复执行或响应丢失可安全重试；已被后续换组替代的旧令牌不能切回原组。不得删除 `credentials.json` 来换组。面板先升级，随后重新生成命令；跨不同面板迁移需另行处理。

重装确认前不停止现有服务；程序更新前备份到 `/var/backups/hl-panel-node/reinstall-*`，失败自动尝试恢复旧程序。成功后输出 `rollback.sh` 路径，程序回滚不会回退已确认的组或流量。节点与规则切换会短暂断开连接，新组无规则时停止旧组监听。离线包安装同样支持该重装流程。

### 节点 TCP BBR2 默认值

令牌新装、继续未完成注册及重装换组在成功后优先尝试 TCP BBR2；确认内核不提供 `bbr2` 时，才尝试普通 TCP BBR。只有实际算法允许写入且读回验证成功时才显示已启用，并明确显示 `bbr2` 或 `bbr`；需要时加载对应内核模块，不安装或更换内核。两者均不支持、容器无权限或已有后置 sysctl 配置冲突会明确警告，节点安装与注册仍保留。支持 BBR2 但设置失败时恢复原配置并报错，不静默降级。旧默认 `bbr` 在支持时升级到 `bbr2`；`bbr3` 等未知 BBR 变体保留，不会冒充 BBR2。

持久配置根据实际算法写 `/etc/sysctl.d/90-hl-panel-node-bbr2.conf` 或 `/etc/sysctl.d/90-hl-panel-node-bbr.conf`，不覆盖 `/etc/sysctl.conf`，不运行 `sysctl --system`，不修改网卡 qdisc、防火墙、路由或引擎规则，不重启系统、网络或转发服务。原默认算法与原受管文件先备份到 `/var/backups/hl-panel-node/network-bbr2-*` 或 `network-bbr-*`；失败尝试恢复两者，成功时输出对应 helper 的独立回滚命令。两种 helper 共用配置锁；重复执行已完成的设置不新增备份或重复写入。

BBR2 是 TCP 拥塞控制，不是 UDP 转发或 UDP 重传开关。设置默认值只影响后续按默认值创建的 TCP socket；已有连接和监听可能继承旧算法，不为切换 BBR2 强制中断客户。实际 CPU、延迟和吞吐取决于内核、排队规则和线路，不能只凭设置成功承诺提升。v0.1.114 起，受管节点版本升级成功后也会执行已校验发布包中的 `deploy/configure-node-bbr2.py`；设置失败会明确警告，不撤销已健康运行的程序升级。更早版本的升级器不会自动执行该步骤。

核对运行默认值：`sysctl net.ipv4.tcp_congestion_control`；核对可用算法：`sysctl net.ipv4.tcp_available_congestion_control`。如需回滚，使用成功输出的 `python3 /var/backups/hl-panel-node/network-bbr2-具体目录/configure-node-bbr2.py --rollback /var/backups/hl-panel-node/network-bbr2-具体目录`；后续人工修改过默认算法或受管文件时会拒绝覆盖，先人工核对。

节点心跳只有在运行默认值和内核可用列表都确认 `bbr2` 时才上报 `tcp-bbr2` 能力。

### 普通 UDP 直入直出

规则选择 UDP 入口和 DIRECT 时，由 GOST 原生 UDP 监听转发至 UDP 目标，无需出口设备组。入口节点必须上报 `gost-udp-direct` 能力并允许对应 UDP 端口；旧节点会显示等待升级，不会误报已生效。普通 UDP 不提供重传、可靠性或零丢包保证，TCP 规则也不会因此自动改走 UDP。

### 节点间可靠 UDP 与既有节点升级

v0.1.114 的正式发布流程使用固定的 `3.3.1-nightly.20260922-hl-udp.1` GOST 构建；引擎和源码锁定信息可在发布包 `licenses/node-engines.json` 核对。普通源码构建的默认上游引擎不自动取得 `gost-reliable-udp-v1` 能力，不应与通过可靠 UDP 验收的发布包混用。

在规则编辑器选择 TCP 入口、出口设备组，再选择 QUIC 或 KCP。客户仍连接原 TCP 入口，UDP 用于入口与出口节点之间。两端都必须运行支持该能力的 Agent 和固定引擎，出口侧必须允许面板分配的 UDP 端口；本版本不修改防火墙。出口配置 commit 和 verify 成功后才激活入口。旧规则没有隧道选项时保留原路径，不会因升级自动变为 UDP；VLESS 与 DIRECT 不会被自动迁移。

GOST 仅在旧服务、链及全局配置保持不变的纯新增规则场景尝试实时添加，并校验监听和回滚结果。该成功路径保持引擎 PID 与流量统计周期，已有连接可继续。编辑、删除、失败补偿及引擎程序升级可能重启，不能承诺无感或零丢包；跨主机性能与长期 CPU 需按实际线路另行验收。

既有受管节点可执行同标签根目录 `update-node.sh --version vX.Y.Z`，无需重新注册或换令牌。升级会保留节点身份、配置、流量状态及程序备份，但当前升级器会停止 Agent 和引擎，活跃长连接会断开。应先升级面板并验收，随后在确认允许维护的节点上逐台执行，逐台核对在线、版本、desired/applied 代次与真实转发；失败即停止后续节点。没有长连接接管能力时，不把滚动更新描述为完全无感。

面板机可作为经过 host key 校验的 SSH 管理通道，按上述同一命令逐台操作节点；这不等于网页已提供远程节点升级按钮。面板 `update.sh` 只更新面板。禁止为此增加任意远程 Shell 产品接口，节点登录秘密不得保存进业务快照或代码库。

### 节点离线安装

在可访问 GitHub 的机器，从 Releases 下载同版本 `hl-panel-linux-amd64.tar.gz` 和 `.sha256`，从同标签源码取得 `deploy/install-node.sh`，将三个文件放在节点同一目录。也可从已校验的正式归档提取该脚本。无需节点访问 GitHub，但节点必须能通过 HTTPS 连接面板，并预装 python3、CA 证书和 systemd。

在面板生成一次性令牌，替换以下三个占位值，执行一条命令：

```sh
bash install-node.sh --version vX.Y.Z --panel-url https://panel.example.com --token '替换为面板令牌' --archive ./hl-panel-linux-amd64.tar.gz --sha256 ./hl-panel-linux-amd64.tar.gz.sha256
```

命令包含秘密，勿分享或写入公开日志，执行后清除对应历史记录。程序包会进行归档 SHA256、完整文件清单及路径安全校验，失败时不会继续安装。

### 节点与面板证书

推荐使用已绑定域名、具有可信 HTTPS 证书的面板地址。IP 自签名入口必须通过受信任的渠道取得 **公开证书 / CA 文件**，核对来源后上传节点，将 `--ca-file /root/panel-ca.pem` 添加到完整安装命令中；地址必须与证书 SAN 匹配。安装器校验 CA 并复制为 hl-edge 可读的受管文件，不需要私钥。证书替换后需更新信任文件。不要使用 `curl -k` 或关闭 Agent 的证书校验。

主机 CPU、内存、根磁盘、运行时间、连接数和网络读数由 HL Agent 独立上报，不要求哪吒。CPU 和网络速率需要两次采样；未取得的读数显示未采集。主机网络计量包括非回环接口流量，不能当作客户计费流量；业务计量仍来自相应引擎。

主机公网 IP 缺失时，面板会使用心跳来源 IP，再使用注册时的公网 IP 补全。默认 Nginx 在 loopback 转发时覆盖 `X-Real-IP`；直接连接 API 的远端请求无法用该请求头伪造来源。属地由面板向 `https://ipwho.is/` 查询，只有公网 IP 会发送给服务商，不发送账号、凭据或配置。查询异步执行，最多 4 个并发、每次最多 3 秒，成功缓存 24 小时，失败 5 分钟后重试；服务不可用时不影响主机指标、心跳或转发。新安装及既有节点均自动生效，旧节点不需要重装。多网卡或代理环境下显示的主机地址不等同于线路出口地址。

## 面板安装位置与运维命令

```text
/opt/hl-panel/                 程序及不可变 release
/etc/hl-panel/                 数据库连接、服务配置和 TLS 文件
/var/lib/hl-panel/             私有服务状态（hlpanel:hlpanel，0750）
/var/lib/hl-panel-license/     独立授权身份与签名缓存（勿复制给其他实例）
/var/lib/hl-panel-acme/        公共 ACME webroot（root:root，0755）
hl-panel-control-api.service   控制面 API
```

```sh
systemctl status hl-panel-control-api.service
journalctl -u hl-panel-control-api.service -n 100 --no-pager
curl --fail --silent --show-error http://127.0.0.1:8080/healthz
```

以上健康检查使用默认端口；若安装时指定 `--api-port 8081`，应改为
`http://127.0.0.1:8081/healthz`，实际监听地址保存在
`/etc/hl-panel/control-api.env`。API 只监听所选 `127.0.0.1` 端口，PostgreSQL
仅供本机使用。登录请求由 Nginx 按
来源 IP 限速。管理员密码、数据库密码和 TLS 私钥不进入 release 包或 Git。

## 保留数据更新与回滚

**本节仅更新面板服务器。** 节点更新需 SSH 登录每台节点，使用 `update-node.sh`，不是在面板服务器执行。完整的角色区分、先面板后节点的升级顺序及误执行处理见[面板与节点更新指南](../docs/upgrade-guide.md)。面板网页更新也不会自动升级节点。

首次从旧面板升级到授权版，必须先使用已校验候选包中的 `deploy/setup-panel-license.sh`
接入域名授权，再执行系统更新，详见[授权指南](panel-license.md)。旧升级器不具备新版预检，
不能跳过此步骤。后续新版升级器在备份、停止服务前验证签名身份；已绑定但到期或撤销的
授权仍允许程序升级，不因此恢复管理权限。

首次安装建立空实例；不要用安装器覆盖已有实例。已有实例由 root 运行：

```sh
curl -fsSL https://raw.githubusercontent.com/Aurelian-HL/HL-Panel/main/update.sh | bash
```

新版安装也提供 `hl-panel-update`，支持 `--check` 或 `--version vX.Y.Z`。更新只接受该仓库的正式发布，拒绝降级与非标准数据库。更新前下载并校验归档，服务在下载期间继续运行；随后暂停本面板，使用 pg_dump 备份并验证清单，迁移、切换程序、启动并核验实际版本。配置、域名、证书和密码保持不变。

备份在 `/var/backups/hl-panel/` 的独立 root 私有目录；终端显示实际路径。包含数据库 dump、配置与状态归档、文件摘要和独立回滚工具。新版本失败会自动 pg_restore 数据库并启动上一版本。人工回滚执行该备份目录下的 `rollback.sh`；会恢复备份时刻的数据，先另行保存升级后的新增数据。更新工具只支持安装器创建的本机 PostgreSQL，不修改其他应用，不提供远程 root 执行接口。

面板自动检查 GitHub 正式版本。落后 1～2 个版本允许暂缓同版本 24 小时，落后 3 个及以上持续提醒并提供 GitHub 入口；网络错误显示未核实，不视为最新。

管理员密码遗失时，主机运维可在备份数据库后使用本地维护命令
`control-api reset-admin-password USERNAME`，从标准输入提供密码，并加载私有
服务配置。该命令要求持久化数据库，记录审计，原子修改密码并撤销全部管理员
会话；设回 `123456` 会重新要求首登改密，不提供远程免认证重置接口。
v0.1.5 写入快照格式 16，读取兼容旧格式；回退旧二进制必须同时恢复升级前
数据库备份，不能仅切换程序链接。
