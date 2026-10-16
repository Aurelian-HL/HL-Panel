# 导入原生规则的 SOCKS5 凭据维护

`apps/native-credentials-maintenance` 是服务器本地维护工具，用于更新
`KL_NATIVE` 规则中已有 SOCKS5 出口的账号和密码。它不是远程命令接口，
不会更换客户 UUID、Reality 密钥、监听端口或出口地址。

普通规则编辑继续禁止修改导入原生配置的传输参数。维护工具只允许精确
匹配一个 SOCKS5 server 和一个用户，且必须提供真实管理员 ID、规则当前
revision 及全部 active material 的 ID/revision。版本冲突会拒绝操作。

## 执行步骤

1. 在服务器私有目录备份当前控制平面快照，权限设为目录 `0700`、文件
   `0600`。凭据和快照不得提交仓库或写入普通日志。
2. 构建工具：`go build -o native-credentials-maintenance ./apps/native-credentials-maintenance`。
3. 从标准输入传入 JSON，先执行
   `native-credentials-maintenance -mode rehearse -snapshot /private/snapshot.json`。
4. 演练通过后，以同一输入执行
   `native-credentials-maintenance -mode apply -dsn-file /private/database-url`。
   数据库连接文件必须无 group/other 权限。默认模式为演练。
5. 确认各节点已应用新配置，再通过实际客户端检查转发。现有出口修改可能
   走引擎完整重新加载路径，导致该节点连接短暂重连；工具不承诺无中断。
6. 如果另一个订阅系统保存了 SOCKS5 账号密码，也须通过该系统正常的维护
   流程同步并重新生成订阅。此工具只维护 HL-panel 的原生规则。

输入字段：`administrator_id`、`rule_id`、`expected_revision`、
`material_revisions`（ID 到 revision 的映射）、`host`、`port`、`username`、
`password`、`idempotency_key`。不要将秘密放入命令参数、shell 历史或
公共临时文件；使用受控进程的标准输入。

## 事务、重试与恢复

写入使用正常 PostgreSQL 快照事务，保留数据库锁、配置历史、审计和缓存
版本更新，不直接覆盖快照 SQL。同一幂等键和相同输入可安全重放；同键
不同输入会拒绝。输出仅包含规则 ID、revision、material 数量和重放状态。

回滚应使用备份中的旧凭据、最新 revision 和新的幂等键重新执行相同流程。
不要恢复整个旧快照来覆盖维护期间其他用户的正常变更。
