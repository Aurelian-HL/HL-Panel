# HL-panel 一键全新安装

此流程面向一台全新 Debian 12/Ubuntu x86_64 主机。当前安装目标域名为
`hlpanel.hongle.cc`。安装会创建新的 PostgreSQL 空数据库，不导入旧面板、
旧 NY 或任何备份，也不会修改 DNS、停用网络组件或清理其他应用。

## 发布与安装

1. 将源码推送到 `Aurelian-HL/HL-Panel`。
2. 推送 `v*` 标签。GitHub Actions 会运行 Go 与管理端测试，构建 Linux amd64
   程序，生成文件摘要，再发布 `hl-panel-linux-amd64.tar.gz` 与 SHA256 文件。
3. 在新主机执行以下命令：

```sh
curl -fsSL https://raw.githubusercontent.com/Aurelian-HL/HL-Panel/main/deploy/install.sh \
  | sudo bash -s -- --domain hlpanel.hongle.cc
```

安装器默认读取该仓库的最新发布。也可以通过 `--repo OWNER/REPOSITORY` 切换仓库，
通过 `--version TAG` 固定版本。安装器提示时在终端输入并确认初始管理员密码。
输入不会显示在屏幕上，不会写入日志；密码哈希保存在 root 管理的服务配置文件中。

API 默认只监听 `127.0.0.1:8080`。如果主机已有服务占用 8080，请使用
`--api-port 8081` 等空闲端口；安装器会在创建文件或运行 apt 之前检查 IPv4/IPv6
TCP 监听端口，有冲突就拒绝安装，不会停止或修改原有服务。例如：

```sh
curl -fsSL https://raw.githubusercontent.com/Aurelian-HL/HL-Panel/main/deploy/install.sh \
  | sudo bash -s -- --domain hlpanel.hongle.cc --api-port 8081
```

服务配置、HL-panel 独立 Nginx 代理和本机健康检查会统一使用所选端口。
`--api-port` 仅允许 1 到 65535 的十进制整数，API 始终只绑定 loopback。

仅 Debian/Ubuntu apt 系统和 Linux amd64 提供一键流程。安装器先确认 HL-panel
目录、服务、数据库角色及数据库名称没有被占用，再下载固定 release 并校验归档
摘要和归档内文件摘要。随后安装 Nginx、Certbot、PostgreSQL 与必要工具，创建
专用服务账号、独立目录和空数据库，执行 usage schema 初始化，配置 systemd 与
Nginx，启动 API 并检查本机健康接口。

## 域名与 IP 入口

安装器不会改 DNS。它检查 `hlpanel.hongle.cc` 的 A 和 AAAA 记录：只有 A 记录
全部指向本机公网 IPv4 且没有 AAAA 记录时，才会申请正式证书。
解析按 IPv4/IPv6 地址族分别查询，不将 IPv4 映射地址误判为 AAAA。临时 DNS
故障或其他查询错误会阻止证书申请；只有明确无该地址族记录才按空记录处理。
安装器会安装 Python 3 作为该检查的运行依赖。

如果域名尚未解析到新主机，安装结束时会提醒需要修改的 A 记录，并提供
`https://<公网IPv4>/` 入口。IP 证书为本机生成的自签名证书，浏览器会显示证书
警告；它用于临时直连，不等同于受信任的域名证书。若无法自动确认公网 IPv4，
请在执行命令时传入 `--public-ip <公网IPv4>`。安装器不会把 `hostname -I` 的
私网地址误报为公网入口。

DNS 生效后，在主机运行：

```sh
sudo hl-panel-enable-domain-tls
```

可选传入通知邮箱：`sudo hl-panel-enable-domain-tls --email admin@example.com`。
Certbot 续期后只替换 HL-panel 自己的证书，并在 Nginx 配置检查通过后 reload。
IP HTTPS 入口会继续保留。
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

一键安装仅用于首次建立空实例，不用于升级或迁移。升级需要另行制作明确的
备份、校验、切换和回滚流程；不要重复运行首次安装器覆盖现有实例。
