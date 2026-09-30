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

发布前，radonet-kddi 实际 IPv4 回显为 `106.178.162.144`；强制 IPv6 回显连接失败。独立接口/路由检查显示 eth0 只有 `10.10.10.13/28` 和链路本地 IPv6，IPv6 路由只有 `fe80::/64`，访问公网 IPv6 返回 Network is unreachable。以下正式部署回读与该观察一致。

生产数据库一致性副本、旧二进制和单元文件保存于 spartan-seattle 的 `/var/backups/heron/v0.2.0-preflight-20260929`，schema 21、integrity ok、1 节点。在独立网络命名空间中以副本启动候选 hub，GetSite 返回 200、进程在监听、schema 升至 23、integrity ok、1 节点保留、旧 network 为 `{}`。首次演练误与尚在进行的 scp 重叠，Text file busy；等待传输退出 0 并校验 SHA 后使用相同命令重跑通过，没有触碰线上数据库。该候选哈希 `ade193b1e305ebd490d639f324bb96bd6b88692df6763270d076ead468c7726e`，早于后补 checked_at 上限；正式部署必须使用最终 Release 产物并重做回读。

完整边界修复后的本地 `make release` 产物再次演练通过，二进制 SHA-256 为 `e364bb49dcab0b33c2e0fe1583e3d100f4f8432754be6925570cba68f7bcbd7a`，本地与服务器回读一致。独立网络命名空间中 GetSite 200，schema 23、integrity ok、1 节点保留、旧 network `{}`；数据库为备份目录内 `final-rehearsal.db`，线上原库未变。

### 正式发布

代码提交 `1898a7db8f55a4f9c403e2a1afc8fea52d104e90` 已快进推送至 `heron/main`。对应 [CI 36567943170](https://github.com/xjetry/heron-probe/actions/runs/36567943170) 的 Linux、Intel macOS 与 Apple Silicon macOS 三个作业全部成功，`gh run watch --exit-status` 退出 0。前端 697 项通过，浏览器 7 通过、2 明确跳过，Debian/Alpine 各自打印 `E2E OK`，Docker 构建与冒烟通过。

`v0.2.0` 注解标签回读指向上述提交。[发布流水线 36569056112](https://github.com/xjetry/heron-probe/actions/runs/36569056112) 成功，watch 退出 0；[GitHub Release](https://github.com/xjetry/heron-probe/releases/tag/v0.2.0) 于 2026-09-29 12:44:27 UTC 发布，非草稿、非预发布，GitHub latest 回读为 v0.2.0。发布说明已上传并回读。

下载全部官方资产后执行 `sha256sum -c SHA256SUMS`，9 个归档和 3 个安装脚本全部 OK。`go version -m` 确认正式 hub 为 v0.2.0、VCS revision 与标签相同、`vcs.modified=false`。镜像按摘要逐架构回读通过；本地再次查询确认 `ghcr.io/xjetry/heron-hub:v0.2.0` 与 `latest` 均为 `sha256:cac302cc6a23dc5b41621318390a0d1b2dceab832e9c0417cf4ab912e22135ed`。

### 正式部署

2026-09-29 20:47 至 20:53（Asia/Taipei），spartan-seattle hub 与 radonet-kddi agent 均已从官方 Release 升级至 v0.2.0。安装文件与 `/proc/<MainPID>/exe` 回读 SHA-256 一致：

| 服务 | SHA-256 |
| --- | --- |
| hub | `460d3c64ed0d9a2a4a6e5f93a54b66fe895df0c6d056c039af83a84d6809c954` |
| agent | `bbc0883015a39bce675d250ae0cd9d101596b232681010a19aa8481a8197651e` |

hub 停服后的一致性旧库、旧二进制及单元文件保存在 `/var/backups/heron/v0.2.0-deploy-20260929`。agent 最终升级前的二进制、配置及单元在 `/var/backups/heron/v0.2.0-deploy-20260929-confirmed`，第一次尝试的备份目录也保留。两项最终部署命令均退出 0。Caddy 未改动或重启。

部署时实际观察到两项环境时序问题，均未修改产品代码：

- hub 初次预检在停服前退出 1：`/run` 为 `noexec`。将两台机器的候选路径改为 `/usr/local/bin/*.stage`，保留挂载安全配置，重新执行部署成功。
- agent 首次启动检查退出 1 并自动恢复 v0.1.0。独立临时 systemd 服务实测，Type=simple 的 start 返回后 `/proc/<pid>/exe` 仍为 `systemd-executor`，0.2 秒后才是目标程序。部署脚本改为有上限地等待目标可执行文件后再核对 SHA；重试日志捕获同一过渡，随后成功。旧版恢复后的文件哈希与备份一致，节点继续在线。

正式回读观察：

- `systemctl is-active` 两项均为 active，版本均为 v0.2.0；hub 数据库 schema 23、integrity ok、1 个节点和 1 条 facts。
- 管理 GetSnapshot、ListNodes、GetSecurity、ListThemes 均为 HTTP 200，hub 与 agent 版本均为 v0.2.0，radonet-kddi 在线。
- IPv4 为 AVAILABLE、地址 `106.178.162.144`、checked_at `1790686205`；IPv6 为 UNSUPPORTED、无地址、checked_at `1790686204`。数据库持久值、管理 API、刷新后的节点页及编辑弹窗一致，地区 JP 显示 emoji 国旗。
- 主题页显示 GitHub Release 安装与本地 ZIP 上传，不再报缺少 theme origin；未替用户安装或启用第三方主题。
- GetSecurity 返回 `passkeyAvailable=true`、`currentOrigin=https://heron.o1.pw`，尚未绑定来源。真实 Passkey 留给用户在 `/admin/security/credentials` 注册与登录验收。
- 以 `credentials: omit` 请求公开 GetSnapshot 返回 200、节点在线及 JP；逐字段回读确认不含源 IP、network 或 checkedAt，正文不含该 IPv4。

本地证据：`build/validation/github-ci-full.log`、`github-release-full.log`、`deploy-hub-result.log`、`deploy-agent-result.log`；官方资产位于 `build/validation/github-release-v0.2.0/`。线上节点页与编辑弹窗截图 `production-nodes.png`、`production-editor.png` 已实际查看。
