# HL-panel 一键全新安装

此流程面向一台全新 Debian 12/Ubuntu x86_64 主机。安装时填写自己的域名，
或直接回车使用 IP 入口。安装会创建新的 PostgreSQL 空数据库，不导入旧面板、
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

DNS 生效后，在主机运行：

### 后续绑定域名

若首次安装时未填写域名，先由 root 在 `/etc/hl-panel/domain.conf` 中将
`DOMAIN` 改为自己的域名，并同步将 `/etc/nginx/sites-available/hl-panel.conf`
里的 `hl-panel.invalid` 替换为该域名；运行 `nginx -t` 通过后 reload Nginx。
安装时已填写域名则无需此步骤。

小白操作：运行 `nano /etc/hl-panel/domain.conf`，只修改 `DOMAIN` 的值，保留引号及其他行；按 Ctrl+O、回车保存，Ctrl+X 退出。再运行 `nano /etc/nginx/sites-available/hl-panel.conf`，将其中 `hl-panel.invalid` 改为同一个域名，保存退出。执行 `nginx -t && systemctl reload nginx`，通过后再申请证书。不要修改 IP 端口、私钥路径或其他站点。修改前请复制这两个文件留作备份。

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

引擎随正式安装包分发：Xray v26.3.27、GOST v3.3.1-nightly.20260922，来源与锁定摘要见包内 `licenses/node-engines.json`。混合模式只在面板下发有效配置后启动对应引擎。服务使用 hl-edge 账号，默认监听端口需不低于 1024，不调整其他服务、防火墙或系统转发规则。

程序在 `/opt/hl-panel/edge-agent`，引擎在 `/opt/hl-panel/node-engines/`，配置在 `/etc/hl-panel/edge-agent.json`，私有状态在 `/var/lib/hl-panel-edge/`。在线入口默认从面板 HTTPS 的公开版本接口选定标签，再下载同标签安装器和程序包；可用 `--version vX.Y.Z` 固定版本。入口、脚本与安装包下载均有有限重试和超时。

注册最多等待 90 秒，每 15 秒提示进度；网络中断、408、429 和服务端暂时错误会退避重试。令牌被拒绝、证书错误或私有状态损坏会停止。注册失败停止服务并移除一次性令牌环境文件，保留安装与私有恢复状态；检查原因后重跑原命令。只有令牌过期或失效才重新生成。若面板已升级，添加 `--version vX.Y.Z` 指定最初安装版本，避免覆盖未完成的安装。

注册请求发送前，Agent 将独立随机的私有恢复秘密及令牌摘要保存到 `enrollment-attempt.json`（0600），不保存原令牌。注册响应丢失时，原机器可在首次注册后 24 小时内恢复同一身份，不重复创建节点、入组或审计。其他机器或不同恢复秘密不能重放已消费令牌。注册成功后删除恢复文件与一次性令牌环境文件，后续使用持久节点凭据。

v0.1.42 起，已有受管节点也可重新运行完整安装命令：在目标组生成新令牌，直接在原机执行。面板同时校验本机持久身份和组令牌，在事务内撤下其他组成员、加入目标组并重新编译完整规则，保留节点 ID、私有凭据、流量账本和历史记录。同命令重复执行或响应丢失可安全重试；已被后续换组替代的旧令牌不能切回原组。不得删除 `credentials.json` 来换组。面板先升级，随后重新生成命令；跨不同面板迁移需另行处理。

重装确认前不停止现有服务；程序更新前备份到 `/var/backups/hl-panel-node/reinstall-*`，失败自动尝试恢复旧程序。成功后输出 `rollback.sh` 路径，程序回滚不会回退已确认的组或流量。节点与规则切换会短暂断开连接，新组无规则时停止旧组监听。离线包安装同样支持该重装流程。

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
