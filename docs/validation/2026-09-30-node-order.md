# 节点排序验收

## 实现范围

- 基点 `80c81c7`。节点页提供拖拽插入线、序号、上移/下移/置顶/置底菜单，以及手柄方向键、Home、End 操作；手机触控控件至少 44px。
- 共享 `useOrder` 维持完整列表准入、串行写入、乐观排列、失败阻断与权威回读。ProbeTasks 原有相邻移动保持可用。只有实际保存并成功回读才显示确认，无效操作不发请求。
- 搜索、标签筛选、陈旧列表、编辑弹窗期间不可排序；拖放以插入而非交换实现，保留其余节点相对顺序。

## 验证证据

- 完整 `make ci` 重跑退出 0，含 63 个前端文件、737 条测试，Go 测试、部署脚本检查、类型检查、生成物检查及五个平台构建。日志：`build/validation/node-order-ci-rerun.log`。
- 首次相同命令出现 Go SQLite `disk I/O error (4874)` 和 `database or disk is full (13)`。失败包无本轮修改，事后磁盘仍有空间，原因尚未确认，不称既有 flake；原日志保留为 `node-order-ci.log`。
- `make web-e2e` 为 16 通过、2 跳过。Chromium、Firefox、WebKit 均验证拖拽保存、连续键盘移动、焦点保留、手机菜单与筛选禁用；跳过仅 Firefox/WebKit 虚拟 Passkey。
- 已查看三浏览器桌面 1440px 与手机 375px 截图，手机无横向溢出。截图位于 `web/test-results/` 中节点排序用例目录。
- 删除页面 onDrop 提交后，拖放测试准确失败于 RPC 未调用；恢复后通过。无效操作确认提示断言先红后绿。
- 将共享算法的 after 条件改成 before，确认注入代码已落地，三条方向用例分别失败于发给 save 的节点排列错误，串行保存用例亦失败。恢复后相同 `pnpm --dir web test src/api/useOrder.test.tsx` 退出 0。日志：`node-order-direction-mutation.log`、`node-order-direction-restored.log`。
- `pnpm --dir web typecheck`、`git diff --check` 通过。

## 审查边界

- `ce-code-review`：`status: complete`、`Ready to merge`、无遗留发现，记录 `/tmp/compound-engineering-501/ce-code-review/node-order-20260930/review.json`。独立 Claude 对抗与正确性检查已完成；之后新增三条方向测试，未改变产品代码。
- 简化按复用、可维护性、效率串行完成，排序算法未复制到页面，未削弱错误恢复与准入。
- 未测真实屏幕阅读器；页面真实拖拽覆盖下半区 after，上半区 before 由共享算法测试覆盖，不宣称已有真实上半区拖放验收。
