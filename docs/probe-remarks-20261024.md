# 设备探针本地备注

## 使用

点击设备 IP 旁的铅笔，填写备注并保存。列表主标题立即显示备注；清空后保存恢复 IP。监测详情与标题提示仍保留真实地址。

备注最多 80 字符，按节点 ID 保存在当前站点、当前浏览器的 localStorage（`hl_probe_remarks_v1`）。刷新、切换设备组后保留，同源标签页之间同步；其他浏览器或设备不共享，清理站点数据会清除备注。

这项功能不调用服务端写接口，不修改实际 IP、连接信息、节点配置、规则或订阅。存储失败时显示提示，不把未保存内容当作成功。

## 修改范围

- `apps/web-admin/src/pages/ProbePage.vue`：备注状态与编辑弹窗。
- `apps/web-admin/src/features/probe/ProbeMembers.vue`：桌面表格和手机卡片的备注展示、编辑入口。
- `apps/web-admin/src/features/probe/remarks.ts`：本地存储、数据校验和合并。
- `apps/web-admin/src/styles/probe.css`：备注、长文本和移动端布局。
- `apps/web-admin/src/pages/ProbePage.test.ts`：保存、清空、取消、持久化和存储拒绝验证。

## 验证结果

- `npm test -- src/pages/ProbePage.test.ts src/features/probe/ProbeMembers.test.ts`：2 个文件、30 项测试通过。
- `npm run build`：Vue TypeScript 检查与 Vite 生产构建通过。
- `git diff --check`：通过。
- 本地浏览器使用合成数据验证 1440、1024、390、320 像素视口：33 项检查通过，生成 12 张截图，无页面错误、无服务端写请求。包含刷新、切组、取消、清空、长备注、损坏存储和跨标签页同步。

验收产物目录：`C:/Users/hf/Documents/Codex/2026-10-16/codex-threads-01a10f3e-8cf5-7312-b6c9/outputs/probe-remarks-20261024`，其中 `report.json` 为结果，`verify.cjs` 为浏览器验收脚本。

修改前备份在同一目录：`ProbePage.before.vue`、`ProbeMembers.before.vue`、`probe.before.css`。回退时仅还原本轮变更，避免覆盖同一工作区已有的其他改动。

## 发布状态

本轮完成本地实现与验收，尚未提交、发布或升级生产；未对线上面板和节点执行变更。
