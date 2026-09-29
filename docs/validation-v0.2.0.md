# v0.2.0 发布验收

## 范围与环境

2026-09-29，工作区 `/Users/xjetry/work/vibe/probe`，基点 `3c75f18`，分支 `feat/public-admin-link`。用户授权提交、发版与部署，确认版本为 `v0.2.0`。本次包含同域沙箱主题、GitHub Release 导入、主题版本管理、首次成功注册绑定 HTTPS 域名的 Passkey、后台改版和节点双栈探测。

数据库升级至 schema 23；公开接口不投影双栈地址，国家查询仍使用可信来源。线上目标为 spartan-seattle 的 hub 与 radonet-kddi 的 agent，升级前均为 v0.1.0。升级必须保留一致的旧数据库、二进制与服务配置。

## 本地凭据

- `make test` 退出 0，日志 `build/validation/release-tests.log`。
- `make web-e2e e2e` 退出 0，Debian 与 Alpine 的 amd64/arm64 agent 经真实 hub 完成验收，分别打印 `E2E OK`，日志 `build/validation/release-e2e.log`。
- `make gen lint build web-test script-test` 退出 0，前端 60 个文件、697 个测试通过，日志 `build/validation/release-checks.log`。
- `buf breaking --against '.git#branch=heron/main'` 与 `git diff --check` 退出 0。
- `make release VERSION=v0.2.0` 退出 0；9 个多架构归档和 3 个安装脚本的 `sha256sum -c SHA256SUMS` 全部为 OK。正式发布仍以 GitHub Actions 产物为准。
- 主代理复跑双栈整组 Go 测试退出 0，日志 `build/validation/dual-stack-root.log`；独立实现单元的并发检测和完整验红记录见 `validation-dual-stack.md`。
- Chromium、Firefox、WebKit 后台编辑、独立计费、双栈四态、明暗切换、Escape、焦点恢复、Tab 背景隔离及移动导航通过。375px 下总览、节点和其余 11 个管理路由没有整页横向溢出。桌面、手机、弹窗截图位于 `web/test-results/`，已实际查看节点、编辑、总览和主题页。
- 同域主题隔离在三个浏览器通过；Passkey HTTPS 注册、重新登录与换域使用 Chromium 虚拟认证器通过，Firefox/WebKit 两项没有对应虚拟认证器接口而明确跳过。浏览器合计 7 通过、2 跳过。

一次构建检查与前端构建并发，嵌入目录被替换导致 Go build 报资源不存在；该次退出 2 不计通过。之后使用相同检查命令串行重跑退出 0。没有据此修改产品或降低断言。

## 缺陷与验红

- 旧 UI 面对新增行为断言为 9 失败、102 通过，日志 `admin-ui-red.log`。
- 更新节点成功但回读失败时，弹窗曾被关闭。新增一般编辑和计费两项断言先失败（`admin-readback-red.log`），使用会传播错误的回读后同命令 89 项通过（`admin-readback-green.log`）。提示明确为已经保存、回读失败，草稿保留。
- 主代理故意移除总览在线过滤后，CPU 从应有 30% 变为 53%，流量也混入离线值，测试退出 1（`admin-summary-red.log`），随后恢复并通过全量前端测试。
- 主代理故意移除共享出口地址族检查，探测器把 IPv4 作为 IPv6 可用，hub 也接受了错误族；两个跨面断言均失败（`dual-stack-root-red.log`）。恢复后相同命令退出 0（`dual-stack-root-green.log`）。
- 三浏览器验收发现 WebKit 鼠标按钮不必获得焦点，关闭弹窗无法恢复触发器。统一 Modal 改为要求显式 opener，并同步节点编辑、创建与移动导航；原失败断言在三浏览器通过，不按浏览器名称特判。
- 主代理与独立审阅者均定位到极大 checked_at 导致详情日期格式化异常。新上报、直接入库和快照恢复断言先在旧实现失败（`network-time-red.log`），共享准入约束为 1..253402300799，并让读取与恢复共用同一解码校验；同命令退出 0（`network-time-green.log`）。协议注释、生成物和最大载荷夹具同步更新。

## 审查与边界

简化审阅覆盖复用、质量、效率；受代理线程总数限制，后两项串行完成。采纳一项主题状态标签的可读性改进。列表最多 60 版本，重复 findIndex 的低收益建议未采纳；GitHub SSRF 白名单与出口公网口径不等价，未合并安全检查。

此前主题与 Passkey 审查完成，无确认的未修代码缺陷，receipt 为 `/tmp/compound-engineering-501/ce-code-review/20260929-180255-97a0f6fb/review.json`。它不覆盖新增 UI/双栈。

新增 UI/双栈的审查已完成，receipt 为 `/tmp/compound-engineering-501/ce-code-review/20260929-194239-0d5a7b2e/review.json`，`status=complete`、`verdict=Ready to merge`、待修发现为 0。审查覆盖共享时间上限修复后的 127 文件工作树。受线程限制，多个视角由两个本地线程串行执行，不算多个独立专家；跨模型审查因 max_turns 没有可用结果，改由本地对抗视角审阅，不能声称跨模型验证成功。

真实第三方 SDK 1 GitHub Release 安装、物理/系统 Passkey 尚无验收凭据。用户已明确选择部署后亲自验证真实 Passkey。主题跨文档返回在三个浏览器均重建后工作正常，没有触发 `pageshow.persisted=true`，不能声称 BFCache 恢复已通过，也不能据未触发直接认定缺陷。

## 发布与线上回读

尚未发布、尚未升级。radonet-kddi 实际 IPv4 回显为 `106.178.162.144`；强制 IPv6 回显连接失败。独立接口/路由检查显示 eth0 只有 `10.10.10.13/28` 和链路本地 IPv6，IPv6 路由只有 `fe80::/64`，访问公网 IPv6 返回 Network is unreachable。最终状态须以新 agent 上报回读确认。

生产数据库一致性副本、旧二进制和单元文件保存于 spartan-seattle 的 `/var/backups/heron/v0.2.0-preflight-20260929`，schema 21、integrity ok、1 节点。在独立网络命名空间中以副本启动候选 hub，GetSite 返回 200、进程在监听、schema 升至 23、integrity ok、1 节点保留、旧 network 为 `{}`。首次演练误与尚在进行的 scp 重叠，Text file busy；等待传输退出 0 并校验 SHA 后使用相同命令重跑通过，没有触碰线上数据库。该候选哈希 `ade193b1e305ebd490d639f324bb96bd6b88692df6763270d076ead468c7726e`，早于后补 checked_at 上限；正式部署必须使用最终 Release 产物并重做回读。

完整边界修复后的本地 `make release` 产物再次演练通过，二进制 SHA-256 为 `e364bb49dcab0b33c2e0fe1583e3d100f4f8432754be6925570cba68f7bcbd7a`，本地与服务器回读一致。独立网络命名空间中 GetSite 200，schema 23、integrity ok、1 节点保留、旧 network `{}`；数据库为备份目录内 `final-rehearsal.db`，线上原库未变。
