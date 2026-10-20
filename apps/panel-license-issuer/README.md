# 独立 HL 授权服务 v2

仅本地开发和隔离验收，尚未发布。此程序仅访问独立 PostgreSQL 的 `hl_license_v2` schema，不连接 HT、HL 客户数据库或转发服务。

构建：`go build ./apps/panel-license-issuer`。HTTP 只监听 loopback，由独立 HTTPS 反向代理暴露；不得复用客户数据库凭据。运行前设置以下环境变量，其中秘密只传文件路径：

| 变量 | 内容 |
| --- | --- |
| HL_LICENSE_ISSUER_DATABASE_URL_FILE | 独立数据库 DSN 文件 |
| HL_LICENSE_ISSUER_ADMIN_TOKEN_FILE | 32–4096 个可打印 ASCII 字符、无空白的随机管理 token 文件 |
| HL_LICENSE_ISSUER_SIGNING_KEY_FILE | raw base64url Ed25519 32 字节种子或 64 字节私钥文件 |
| HL_LICENSE_ISSUER_ID | 对外稳定 HTTPS issuer 标识，与 HL 信任配置一致 |
| HL_LICENSE_ISSUER_KEY_ID | 签名公钥标识，与 HL 信任配置一致 |
| HL_LICENSE_ISSUER_LISTEN | 可选，默认 127.0.0.1:8712 |

秘密文件应仅服务账户可读；管理 token 仅供可信 HT 后端持有。代理只公开冻结契约所需路径，保留 TLS 校验。管理 body 的 actor 必须由 HT 登录会话生成。

迁移在启动时于独立 schema 内事务执行。授权码只保存 SHA256 和短前缀，幂等回放永不重新返回明码；未保存的码需显式换码。域名历史永久保留，不能靠换绑制造第二份同域许可。相对期限首次激活起算；月末按上海日历夹取。撤销不能通过延长或换绑恢复。

本地数据库验收：设置 `HL_LICENSE_TEST_DATABASE_URL` 指向**隔离测试 PostgreSQL 的 loopback 维护库 `postgres`**，账号需有建库权限，再执行 `go test -count=1 ./internal/panellicenseissuer`。测试只接受 `127.0.0.1` 或 `::1`，自行创建、使用并删除独立随机测试库；未设置 DSN 会跳过数据库集成测试。完整临时数据库及 HTTPS 验收使用 `verify_local.py`。生产上线需另行授权，使用当时最新线上文件和备份，按哈希打代码补丁；不得覆盖旧客户数据或配置。回滚仅撤销代码补丁，不恢复旧客户库。
