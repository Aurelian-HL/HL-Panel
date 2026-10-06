# 现有 NY 只读参考记录

检查日期：2026-10-02，接口结构提取时间为 12:47 UTC。仅记录结构及聚合数量，不保存用户记录、落地目标、密码、连接令牌或完整配置。

## 访问与边界

- 浏览器实际访问 `https://zf.hongle.work`，跳转 `/#/login`，页面标题 WORK；没有浏览器登录态。本次没有登录后台逐页截图，不将官方文档推断表述成实际界面观察。
- 从已存在的本地连接资料读取凭据至临时内存，用已知登录接口登录其对应 NY 主机。业务请求仅为以下三个 GET；未点击/调用创建、编辑、启停、安装、删除、同步或重启。
- 未连接 `ht.hongle.work` 或 `kl.hongle.cc`；只读取本地历史后台源码中的连接契约。新平台运行时没有引入对它们的依赖。
- 登录本身可能产生 NY 的正常会话/访问日志；“只读”指未改业务数据和服务配置，不把登录说成零状态变化。

| 接口 | HTTP / NY code | 本次可见结果 |
| --- | --- | --- |
| `/api/v1/user/info` | 200 / 0 | 当前参考账号 `admin=true`；用户有用户组、套餐、到期、流量、限速、IP/连接数、规则数量、余额、自带设备、自动续费等字段 |
| `/api/v1/user/devicegroup` | 200 / 0 | 8 个可见组：5 个入口组，3 个 `DeviceGroupType_OutboundBySite` 出口组 |
| `/api/v1/user/forward` | 200 / 0 | 648 条可见规则；都有 `ForwardRuleStatus_Normal`，其中 2 条另有 `paused=true` |

这些是当前账号的接口返回范围，不声称已经列出全站每个账号的数据，也不作为未来不变数量。

## 真实字段

用户对象包括：`group_id/group_name`、`plan_id/plan_name`、`expire`、`banned`、`max_rules/used_rules`、`traffic_enable/traffic_used`、`speed_limit`、`ip_limit`、`connection_limit`、`allow_device`、`auto_renew`、`balance` 等。本记录仅提取字段名，不保存值。

设备组包括：`type`、`connect_host`、`port_range`、`ratio`、`allowed_in`、`config`、`display_protocol`、`traffic_used` 等。入口组样本的 config 中可见 `direct_policy`；只读检查当前前端资源确认其三态为 `0=禁止直出`、`1=规则可选直出或出口组`、`2=强制直出`。组类型与策略必须一起解释，不能只给组贴 ENTRY/EXIT 标签。

转发规则包括：`device_group_in`、`device_group_out`、`listen_port`、`config`、`paused`、`status`、`traffic_used`、`uid` 等。当前可见配置结构有 `dest: array`，部分规则另有 `speed_limit: number` 或 `ip_limit: number`。没有读取输出 dest 中的实际客户目标或凭据。

`status` 和 `paused` 必须分开迁移及显示：本次就存在状态正常但已暂停的规则。看到 Normal 不能自动恢复暂停规则。用户到期、禁用与额度也应独立参与有效状态计算。

## 对重构的直接要求

1. 转发规则必须成为主业务模块，包含用户、入口、出口/直出、落地目标、端口、协议、限制和独立生命周期状态。
2. 入口/出口组必须提供各自的地址、端口池、策略和授权配置，不是目前通用设备组类型枚举就算实现。
3. 用户与授权、用量/倍率、到期和规则额度必须贯穿创建规则及实际执行。
4. VLESS 加入同一业务规则流程，客户一条稳定链接；组内机器由服务端按新连接选择。
5. NY 导入必须保留用户/组/规则关系及暂停状态；这些 API 字段只构成参考证据，不代表已经掌握原生导出 schema 或具备导入能力。
6. `device_group_in` 始终是规则接入/转发组；`device_group_out=0` 表示入口组直出，非零表示再经过出口组。直出与专线不是同一维度，不能将所有出口组路径都命名成专线。

参照来源：现场以上三个接口，官方功能说明及本地代码差距报告。生产面板的后台逐页布局、所有管理字段、数据库/导出版本和自定义功能仍需进一步只读核验，不能宣称已完整复制。
