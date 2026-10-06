# HL-panel

HL-panel 是一个独立的网络转发管理面板，采用 Go、Vue 3 和 PostgreSQL，围绕用户授权、入口与出口设备组、转发规则和节点管理提供统一操作界面，并支持 VLESS / Reality / Vision 配置。

**项目目前处于持续开发阶段。** 管理界面与控制面已接入，完整的节点自动部署、多机转发和协议健康探测仍在完善。请先在独立 VPS 上试用；保存规则或生成配置不代表转发服务已经生效。

## 一键安装

适用于使用 systemd 的 Debian 12、Ubuntu 22.04 或更新版本的 **Linux amd64** VPS。首次安装以 root 执行：

```sh
curl -fsSL https://raw.githubusercontent.com/Aurelian-HL/HL-Panel/main/install.sh | bash
```

安装器会询问面板域名、管理员账号和密码。域名留空使用 IP 访问；账号和密码直接回车使用：

| 项目 | 默认值 |
| --- | --- |
| 管理员账号 | `admin` |
| 初始密码 | `123456` |

**使用初始密码登录后，必须到「个人中心 → 修改密码」设置新密码。** 修改前无法执行其他管理操作。也可以在安装时设置自定义账号与密码；自定义密码需为 8～256 个字符，隐藏输入并二次确认，不在安装输出中显示。

指定自己的域名和管理员账号：

```sh
curl -fsSL https://raw.githubusercontent.com/Aurelian-HL/HL-Panel/main/install.sh \
  | bash -s -- --domain panel.example.com --admin-username myadmin
```

安装结束会显示当前版本、实际访问地址、管理员账号和密码说明。域名未解析到本机时，会提示绑定 A 记录，并提供 `https://<公网IPv4>:8443/` 临时入口。IP 入口使用自签名证书，浏览器会提示证书警告；绑定域名后可启用受信任的 HTTPS 证书。

首次安装创建独立目录、服务账号和全新的 PostgreSQL 数据库。检测到已有 HL-panel 实例或端口冲突时会停止安装，不覆盖已有实例。**此命令用于全新安装，不用于升级或数据迁移。**

## 常用安装参数

| 参数 | 用途 |
| --- | --- |
| `--domain panel.example.com` | 指定自己的面板域名 |
| `--admin-username myadmin` | 指定初始管理员账号；密码在终端中输入 |
| `--public-ip <公网IPv4>` | 自动检测失败时手动指定公网 IPv4 |
| `--api-port 8081` | 调整仅监听本机的 API 端口，默认 `8080` |
| `--ip-https-port 9443` | 调整 IP HTTPS 入口端口，默认 `8443` |
| `--email admin@example.com` | 设置证书通知邮箱 |
| `--version <发布标签>` | 安装指定正式版本 |

安装器不会修改 DNS、防火墙或云安全组。请自行放行所需访问端口，并将域名解析到 VPS 的公网 IPv4。域名解析生效后，在 VPS 上执行：

```sh
hl-panel-enable-domain-tls
```

详细说明见 [安装与运维手册](deploy/README.md)，数据保护见 [备份与恢复](deploy/backup-and-restore.md)。

## 功能与当前边界

| 模块 | 当前能力 | 开发中的能力 |
| --- | --- | --- |
| 用户与授权 | 用户、用户组、有效期、额度与设备组授权 | 完整计量与限额闭环 |
| 设备组 | 入口组、出口组、成员与权重、候选自动投影 | 动态网关配置与高可用 |
| 转发规则 | 创建、编辑、启停、端口分配、直连与出口组选择 | 自动下发、跨机运行与真实业务连通验收 |
| 节点 Agent | 注册、心跳、配置校验、原子更新与回滚 | 完整引擎激活、混合进程管理与 mTLS |
| VLESS | TLS / Vision / Reality 配置编译与独立引擎适配 | 完整单 URI 交付、秘密分发与全组撤销 |
| 数据持久化 | PostgreSQL 事务快照、重启持久化、管理员会话与审计 | 关系仓储、迁移与大规模数据治理 |

设备组面向客户提供一个稳定接入地址，支持按连接调度。现有连接保持在原后端；后端故障后需要客户端重连。节点在线状态与协议可用状态分别判断，心跳正常不等于业务流量已经连通。

本项目聚焦网络控制与转发，不包含商城、订单、支付、钱包或推广返佣模块。

## 版本与发布

一键安装入口先选定最新正式 Release，再下载**同一标签**的安装脚本与程序包，并校验 SHA256。发布流程包含 Go 和前端检查，以及默认账号、自定义账号两种隔离的全新安装测试；通过后才发布安装包。

源码变更同步至 GitHub；影响安装行为的变更随新版本发布。正式标签与安装包不覆盖，避免安装器和程序版本不一致。提交 main 不等于发布了新的安装版本。

## 本地开发

需要 Go 1.27 或更新版本，以及兼容 Vite 8 的 Node.js。

```sh
go test ./...
go vet ./...
cd apps/web-admin
npm ci
npm run typecheck
npm test
npm run build
```

真实引擎集成测试需自行提供已校验的 Xray 或 GOST 程序；默认测试不会访问线上节点。启动配置见 [控制面](apps/control-api/README.md)、[节点 Agent](apps/edge-agent/README.md) 和 [Web 界面](apps/web-admin/README.md)。接口与架构见 [API 契约](docs/api-contract.md) 和 [模块边界](docs/module-boundaries.md)。

## 反馈与贡献

欢迎通过 [Issues](https://github.com/Aurelian-HL/HL-Panel/issues) 反馈问题或提交 Pull Request。反馈请附版本、操作步骤和脱敏后的错误信息；请勿上传管理员密码、数据库连接、私钥或用户连接凭据。
