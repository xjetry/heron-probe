# 公开节点卡片验收

## 范围

- 基点 `2834d920fd4f19364040b12902fd86d94455a742`；独立 worktree `.worktrees/public-node-cards`，分支 `feat/public-node-cards`。不修改主工作区，不合并或部署。
- 参考 Monitor 的双列指标布局，保留 Heron 的站点主色与明暗设置。名称、地区、在线状态组成页头，资源区展示核数、负载、CPU、内存、磁盘与周期流量总量；网络上下行独立；计费与最近上报收在底部。
- 在线状态只用 hub 的 online；uptime 明确写运行时长，不称连续在线。离线保留最后读数并标记，缺失读数不是零。没有配额字段，不展示无限配额或虚构流量占比。
- 提取独立 NodeCard，继续使用 Bar、Missing、CountryBadge 和计费/单位格式化工具；不改 API、公开字段范围或第三方主题。地区与标签组合筛选不变，宽布局只作用于总览，不改详情页尺寸。
- 整卡点击与键盘标题链接进入同一详情地址；长名称视觉省略但保留完整链接名和 title。尊重减少动态效果偏好。

## 验证

- `pnpm --dir web typecheck` 退出 0。
- 最终 `make ci` 退出 0，Go 全量（含安装部署）测试、脚本检查、749 条前端测试、类型检查和五个平台构建通过；日志 `build/validation/cards-ci.log`。`git diff --check HEAD` 退出 0。
- 新用例先因缺少核数和最后读数文案失败，日志 `build/validation/cards-red.log`。随后将 CPU 0 错误兜底为 undefined、倒置离线判断，两条新用例分别准确红在 0 值进度条和最后读数，恢复后完整单测通过；日志 `cards-mutation.log`。
- 完整 `pnpm --dir web test` 重跑退出 0，67 文件 749 测试通过，日志 `cards-all-unit-rerun.log`。旧卡片测试按新分区查询相同数值，没有删除核心语义断言。
- 独立审查发现资源条的定位层遮住整卡链接。增加真实鼠标点击资源条中心用例，修复前准确停留在总览，日志 `cards-link-red.log`；卡片独立层叠上下文与链接覆盖层明确层级后恢复正确导航。
- 最终完整 `make web-e2e` 退出 0，22 通过、2 跳过（Firefox/WebKit 虚拟 Passkey），26.9 秒，日志 `cards-e2e-awake.log`。三个浏览器均覆盖1600px四列、375/320px单列、明暗主题、长名称、缺失、真实0、离线最后读数、键盘与资源条鼠标导航。
- 已查看明暗桌面与移动截图。`build/validation/cards-dark.png`、`cards-light.png`、`cards-mobile.png` 为最终视觉证据；没有实测屏幕阅读器或物理触控设备。
- 中间完整运行出现单测和浏览器超时。系统日志中 03:09:37 起睡眠1076秒、03:28:42 起睡眠925秒，与运行停顿吻合；一次后台测试超时导致 finally 清理失败，后续节点计数被残留夹具污染。临时保活后未改产品、超时阈值或跳过项，原完整命令通过；不将这些失败声明为既有产品 flake。保活只作用于验收窗口。

## 审查

- ce-code-review：`status: complete`，`Ready to merge`，无剩余发现；receipt `/tmp/compound-engineering-501/ce-code-review/public-node-cards-20260930`。
- 简化检查因新建代理触及线程上限，由主代理按复用、质量、效率三个 rubric 检查：共用 Bar/格式化工具，内存与磁盘共用 CapacityResource，没有额外请求、状态、定时器或依赖；没有为简化改变行为。未宣称完成三个独立代理审查。
