# 个人中心布局调整与验收（2026-10-24）

## 修改范围

移除左边两张卡、右边一张卡的布局，改成三个等宽、上下排列的区域：

1. 账号概览：头像、账号名、身份和登录状态；宽屏横向展示创建时间与会话有效期。
2. 域名授权：统一标题图标、状态标记和操作栏；宽屏横向展示域名、期限及同步时间，帮助入口靠右。
3. 账号安全：宽屏并排展示三个密码字段，底部统一说明、显示密码及保存操作。

页面最大宽度 1180px 并居中，使用冷灰、白色、海军蓝及少量金色。手机上改成逐行信息、纵向表单，帮助按钮单独占一行。授权激活、DNS 证明、免费额度、到期提醒和密码请求流程保持原有 API 合约。

实际修改的业务界面文件：

- `apps/web-admin/src/pages/ProfilePage.vue`
- `apps/web-admin/src/components/PanelLicenseCard.vue`
- `apps/web-admin/src/styles/site.css`

新增 `tests/browser/profile-layout-smoke.cjs`，用于真实 Chromium 页面验收，仅允许本地回环地址和合成 API 数据。

## 已执行验收

- `npm test -- src/pages/ProfilePage.test.ts src/components/PanelLicense.test.ts`：2 个文件、18 项测试通过。
- `npm run build`：TypeScript 检查及 Vite 生产构建通过。
- `node tests/browser/profile-layout-smoke.cjs`：1920、1440、1024、768、390、320 六种宽度，54 项检查通过，生成 48 张截图，无意外控制台错误。检查默认界面、长账号名与长域名、首次登录、资料读取等待、资料读取失败与重试、密码不一致、服务端密码拒绝、保存中按钮禁用、成功后退出登录；验证卡片无重叠、整页无横向溢出、控件无裁切。
- `node tests/browser/panel-license-smoke.cjs`：1440、1024、390 三种宽度，51 项检查通过，生成 51 张截图，无意外控制台错误。覆盖免费、正式、过期授权、激活、DNS 失败重试、用量异常、状态错误恢复、帮助占位及提醒弹窗键盘操作。
- `git diff --check`：通过。

浏览器验收使用合成数据，密码修改请求没有发送到生产。服务端权限、真实域名授权及生产用户登录不属于本轮验收。本轮未发布、未部署，未操作节点、规则、订阅或内核。

## 产物与回退

产物根目录：

`C:/Users/hf/Documents/Codex/2026-10-16/codex-threads-01a10f3e-8cf5-7312-b6c9/outputs/profile-redesign-20261024`

- `profile-states/report.json`、`profile-states/*-default.png`：个人中心验收报告与预览。
- `license-states/report.json`：授权状态验收报告和截图。
- `ProfilePage.before.vue`、`PanelLicenseCard.before.vue`、`site.before.css`：修改前的三个文件备份。

需要回退时，仅将这三个备份复制回对应源码位置，再重新构建前端；不要回退同一工作区内的引擎连续性修复。
