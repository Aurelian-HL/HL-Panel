# xzf 发布状态核对

日期：2026-10-03。仅对新站 `xzf.hongle.cc` 做未登录、只读公网检查；
未登录服务器、未切换 release、未修改数据库、DNS 或三个受保护旧站。

## 当次实际结果

- `GET https://xzf.hongle.cc/login` 返回 HTTP 200，页面引用
  `/assets/index-DYBlDuhv.js`，HTML 标题为“线路管理面板”。
- 仓库中 `artifacts/20261003-login-nyui-r5/web-admin/index.html` 引用同一
  `index-DYBlDuhv.js`。这是静态资源一致性证据，不能单凭它证明服务器
  当前 release 软链或 API 二进制版本。
- 本地 `apps/web-admin/dist/index.html` 引用
  `/assets/index-JtJqE-c-.js` 和 `/brand-bootstrap.js`，与公网 HTML 不同。
  当前本地最新管理端构建尚未在公网 HTML 中出现。
- `GET https://xzf.hongle.cc/api/v1/public/site-info` 返回 HTTP 200，
  `site_name` 为 `HLPanel`，`panel_title` 为“鸿乐的整合怪诞”，
  `platform_version` 为 `development`。
- `GET https://xzf.hongle.cc/healthz` 返回 HTTP 200。它只证明当次
  健康接口可达，不证明数据库、规则执行或设备组转发闭环。

## 发布前仍需确认

当前源代码与公网静态资源不一致；不得将本地修改描述为已部署。发布时先
备份数据库并记录当前 release 绝对路径和文件摘要，使用隔离副本验证快照
兼容，再执行原子切换。切换后应重新核对公网 HTML 资源摘要、公开站点设置、
真实管理员登录与规则流程，以及服务端和浏览器错误。所有未执行项必须明确
标记为未验证。
