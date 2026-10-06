# PostgreSQL 快照备份与恢复

只适用于 `xzf.hongle.cc` 新机的单实例试运行库。本手册是待执行方法，不能
代替真实恢复结果。备份包含身份及配置秘密，目录使用 `0700`、文件 `0600`，
不得上传到 Git、公共对象存储或报告附件。离机备份必须加密，并单独保管密钥。

## 备份

使用 PostgreSQL 15 的 `pg_dump`，通过本机 `postgres` 系统用户和 Unix socket
认证，避免在命令参数或环境日志暴露应用数据库 URL。下面以专用库
`nyvp_control` 为例；执行前必须核对实际库名和本次备份路径。先确认目标路径
尚不存在，不覆盖任何已有备份。以 root 执行，示例时间目录需替换为本次值：

```sh
umask 077
install -d -m 0700 /opt/nyvp/backups/20261002T120000Z
runuser -u postgres -- pg_dump --format=custom --no-owner --no-privileges nyvp_control > /opt/nyvp/backups/20261002T120000Z/nyvp-control.dump
pg_restore --list /opt/nyvp/backups/20261002T120000Z/nyvp-control.dump
sha256sum /opt/nyvp/backups/20261002T120000Z/nyvp-control.dump
```

逐条核对返回码，失败的 dump 不能标记为有效备份。`pg_dump` 会获得一致性
快照，不需要停止正常 API；若要做时间点一致的正式切换，仍需安排写入冻结。
`pg_restore --list` 只检查归档目录，不证明能恢复；必须执行下一节的隔离演练。

同时记录原子软链目标、release 的 `SHA256SUMS`、快照版本/修订号/字节数、
PostgreSQL 主版本、nginx 和 systemd 配置。`/etc/nyvp`、数据库角色凭据及证书
资料另外备份至受保护目录，不能列出其内容。`--no-owner --no-privileges` 的
dump 不包括集群角色；恢复端须另建专用角色与权限，不能误认有 dump 就有全部
凭据。秘密文件和业务 dump 应采用一致的保密与离机加密策略。

不要复制运行中的 `/var/lib/postgresql` 目录充当可靠备份，也不要为了备份
关闭 PostgreSQL 的 `fsync`、`full_page_writes` 或 `synchronous_commit`。

## 隔离恢复演练

1. 选择全新的演练库名，例如 `nyvp_restore_20261002_test`，确认该名称不存在。
   创建独立的 `NOLOGIN` 角色作为库所有者，再创建新库；不要使用业务库名。
2. 用 `pg_restore --exit-on-error --single-transaction --no-owner --no-privileges`
   恢复到新库，并显式指定演练库所有者角色。备份目录无需放宽权限，可由 root
   打开 dump 再通过 stdin 交给 `runuser -u postgres -- pg_restore ...`。不得使用
   `--clean`、`--create` 或对原库执行 restore。
3. 比较源库和新库的 `format_version`、`revision`、`octet_length(payload)` 及
   快照 SHA256；只记录摘要，不输出 payload。源库继续有写入时，比较对象应是
   备份时记录的值或同一恢复过程中的 dump，不要求实时 revision 相同。
4. 为演练临时进程建立受保护的连接文件和环境文件，指向演练库，使用同一
   release；仅监听 `127.0.0.1:18080`，不接入 nginx、不对公网发布、不启动节点
   Agent/网关。为该进程配置受限的本地认证角色；不要重用业务连接文件。
5. `/healthz` 通过后，用真实管理员凭据通过安全 stdin 登录演练副本，验证
   受保护 API 和设备组、节点等可用记录，重启演练副本后再次读取。所有修改
   仅发生在演练库。不要接收或下发实际节点任务。
6. 保存退出码、检查时间、软件版本、数据库名和恢复结果；凭据、Bearer 令牌及
   登录响应不得落入普通日志。停止临时副本，保留演练库待明确清理授权，不能
   使用通配符删除数据库或目录。

## 正式恢复与回滚

正式恢复可能丢失备份之后的业务写入，属于独立维护操作，需要明确恢复点和
维护范围授权。先备份故障现状，再恢复到新库并完成上述演练；冻结原服务写入，
记录旧连接文件和旧 release 后，以受保护文件原子替换数据库连接并重启 API。
在真实公网重新验证登录、会话、设备组和节点记录。原库及原连接文件保留，
不要覆盖或删除，以便切回；恢复期间产生的新写入需单独核对，不能承诺零丢失。

应用 release 回滚不能代替数据库恢复，旧程序不能读取更新快照格式时必须
停止回滚尝试并依据兼容性计划处理。新库恢复成功也不代表原来发给节点的配置
或运行中的数据面进程已自动同步，应单独检查世代与 last-known-good 状态。
