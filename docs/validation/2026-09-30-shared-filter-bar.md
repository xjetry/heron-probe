# 公开筛选栏交互验收

## 范围

- 基点 `deb48ed67273b35f3bc3d36504649a40867e7dca`，只修改内置公开页，无数据库迁移。
- 地区和标签共用 FilterBar，各占一行，都提供“全部”；移除地区标题和说明。普通点击单选，再点唯一已选项取消；Shift 点击增删选择；全部只清空所在栏。
- 地区之间 OR，与标签 AND；未知空字符串是独立选项，空数组才表示全部。标签选项先在 Overview 按当前快照规范化，节点匹配仍保留大小写折叠。
- 删除旧 TagBar 与 nextSelection 及对应函数级测试，将交互覆盖迁移至共享组件和公开页面测试；没有保留重复实现或兼容包装。

## 本地验证

- 修改测试后旧实现准确失败于缺少“全部”和旧地区交互，日志 `build/validation/region-bar-red.log`。
- 注入 `event.shiftKey && value` 后地区参数用例准确失败于 HK + 未知仅留下未知，标签用例仍通过；恢复产品代码后四文件 42 条测试通过。日志 `region-bar-unknown-red.log`、`region-bar-restored.log`。
- `make web-e2e` 退出 0，三个浏览器共 19 通过、2 跳过（Firefox/WebKit 虚拟 Passkey）。地区用例覆盖家宽 + HK/JP、全部清空地区仍保留家宽、未知，日志 `region-bar-e2e.log`。
- 已查看 Chromium 1440px 和 375px 截图：两栏各自独立一行，样式一致，无地区标题说明或横向溢出；未实测粗指针设备和屏幕阅读器。
- `make ci` 退出 0，Go 全量测试、安装部署回归、脚本检查、前端测试、类型检查与五个平台编译通过，日志 `region-bar-ci.log`。构建仍提示公开页压缩后单块超过 500kB，非构建失败。
- `git diff --check HEAD` 退出 0。独立 ce-code-review 完成，无发现，receipt `shared-filter-bar-20260930`，路径 `/tmp/compound-engineering-501/ce-code-review/shared-filter-bar-20260930`。
- 未直接测试快照把标签 db 改为 DB 后再取消的场景；源码检查确认 effective 在传给组件前取当前标签写法。现有页面测试覆盖节点标签折叠匹配。

## 发布

- 计划发布 v0.3.5，仅在线升级 Hub；Agent 无代码变更，不为前端调整重启节点。
