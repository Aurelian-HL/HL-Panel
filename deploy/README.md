# HL-panel 一键全新安装

此流程面向一台全新 Debian 12/Ubuntu x86_64 主机。安装时填写自己的域名，
或直接回车使用 IP 入口。安装会创建新的 PostgreSQL 空数据库，不导入旧面板、
旧 NY 或任何备份，也不会修改 DNS、停用网络组件或清理其他应用。

## 发布与安装

1. 将源码推送到 `Aurelian-HL/HL-Panel`。
2. 推送 `v*` 标签。GitHub Actions 会运行 Go 与管理端测试，构建 Linux amd64
   程序，生成文件摘要；默认账号和自定义账号分别在全新临时 Linux 虚拟机安装，
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

## 安装位置与运维命令

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
