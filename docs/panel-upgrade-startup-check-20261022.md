# 面板升级启动检查（2026-10-22）

## 故障与修复范围

一次 v0.1.130 升级已校验并切换发行目录，但 systemd 的本地覆盖配置将
`ExecStart` 固定到旧候选程序。服务启动后仍报告旧版本，升级器因此恢复数据库
和原发行目录。本次修复只涉及面板升级器，不升级 Agent、Xray 或 GOST。

自动升级现在在创建备份目录前检查有效 `ExecStart` 和 `NeedDaemonReload`，
下载及授权预检结束后、停服前再次检查。启动程序必须使用
`/opt/hl-panel/current/bin/control-api`，允许保留原有参数；固定旧发行目录、
自定义启动器或尚未 daemon-reload 的配置会提前拒绝，服务保持运行。
检查只读，不替用户覆盖自定义 systemd 配置。

启动后的验收同时要求 HTTP 版本正确、服务 active，以及服务 MainPID 的
`/proc/PID/exe` 与 current 中的程序是同一文件。仅返回相同版本号不能通过。
这项检查同样用于自动回滚后的恢复验收。

## 旧版如何获得检查

面板的固定用途更新 worker 先核对目标正式发行，再下载对应 tag 的
`deploy/update.py` 执行。因此已有网页更新通道可在升级此次版本时获得停服前
检查。旧版命令行 wrapper 直接执行本机 current 内的脚本，第一次升级时仍可能
使用旧检查；需要由管理员先核对有效启动配置。不要把新包中的代码自动套用为
所有旧命令行入口的行为。

配置冲突时，检查 `systemctl cat hl-panel-control-api.service` 及
`systemctl show hl-panel-control-api.service -p ExecStart -p NeedDaemonReload`。
备份冲突的 unit/drop-in，保留环境、权限和启动参数，将程序路径改为受管 current
路径，执行 daemon-reload 后重试。不要反复执行包含数据库恢复的旧 rollback.sh，
以免覆盖故障后新增的数据。

## 回归验收

- 隔离单元测试覆盖旧程序、自定义启动器、缺失/多个命令、待重载状态、超时、
  无有效 PID、退出进程及“HTTP 版本正确但程序错误”。
- 专用 Linux CI 的真实 PostgreSQL/systemd/Nginx 升级验收新增固定旧程序覆盖：
  必须提前拒绝、PID 不变、current 不变、数据库 schema 不变、未新增备份。
- 保留真实候选启动后注入失败、数据库/程序回滚、再次成功升级和两轮网页升级。
- 发布和生产验收结果另存操作报告；单元测试通过不能替代真实 Linux 和公网验收。

生产升级前仍需新鲜备份，升级后核对实际程序摘要、授权、节点采集与真实客户请求。
本修复不保证控制面更新期间页面不中断；节点引擎和已有客户身份不因此改变。
