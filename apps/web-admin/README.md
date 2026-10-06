# HL-panel 管理端

这是独立控制面的 Vue 3 管理端，不依赖旧面板或其他运营后台。

## 产品语义

- 设备组是服务端维护的候选机器池，不是把多条节点链接暴露给客户。
- 客户只使用一个 SOCKS5 或 VLESS 稳定入口；设备组成员自动同步为服务端候选，健康检查、排空和节点选择在服务端完成。
- 节点心跳不等于协议健康；新候选在协议探测成功前显示为未健康。
- 新建设备组可选择 `weighted_least_connections`（默认）、`weighted_round_robin` 或 `rendezvous_hash`，字段名为 `selection_policy`。
- 旧的 `SUBSCRIPTION_POOL` 只允许作为兼容导入标记，不是新建或发布的默认模式。

## 本地运行

先启动本地控制 API，再启动管理端。开发服务器默认监听 `127.0.0.1:4175`，
将 `/api` 转发到 `http://127.0.0.1:8080`；需要其他地址时设置
`VITE_API_PROXY_TARGET`。所有页面都使用真实 API。

```powershell
npm install
npm run dev
```

控制 API 未启动或代理不可达时，登录及数据请求会报连接错误，不会显示演示数据。

## 验证

```powershell
npm test
npm run typecheck
npm run build
```

所有 HTTP DTO 使用 API 契约的 `snake_case`；运行时解析器会拒绝字段类型漂移。一次性节点注册令牌只在创建成功后的当前对话框显示，列表和日志不读取该值。
