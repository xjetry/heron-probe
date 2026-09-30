# 同域主题与 Passkey 验证记录

## 对象与环境

2026-09-29，工作目录 `/Users/xjetry/work/vibe/probe`，基点 `3c75f18` 上的未提交工作树。未提交、推送或部署新二进制。另经用户授权修正线上可信代理配置，见下文。Go 1.27.1，macOS arm64；Playwright 1.63.0，Chromium 153.0.8010.12、Firefox 155.0、WebKit 26.6。

浏览器运行正式构建的 `bin/heron-hub`，使用临时数据库、回环 TLS 反向代理、真实转发协议头和 `--trusted-proxies 127.0.0.1/32`，未设置 `--admin-origin`。测试证书为临时自签证书，浏览器测试配置忽略证书信任错误；没有验证操作系统证书安装。Passkey 使用 Chromium 虚拟认证器，不是物理安全密钥。服务器重启保留同一数据库。

## 已观察结果

- `make gen lint build web-test script-test` 退出 0（`checks-final.log`）。协议生成、buf lint、Go 多平台 vet/build 与脚本检查通过；前端 60 个文件、684 项测试通过。Vite 有大 chunk 提示，jsdom 有非失败 CSS 解析提示。
- `buf breaking --against '.git#ref=3c75f18'` 退出 0。
- `go test -count=1 ./...` 退出 0，包括 deploy 包 202.576 秒、auth、store、backup、API 和主题运行包；没有跳过测试。
- 最后补齐缓存边界断言后，`make test` 退出 0（`make-test-final-tree.log`），其中 deploy 158.920 秒；不是用单包结果替代全量。
- `make web-e2e e2e` 退出 0。三个浏览器主题用例通过；Chromium Passkey 通过，另两种浏览器跳过虚拟认证器用例。Docker 中 Debian bookworm-slim、Alpine 3.21 的 amd64 和 arm64 agent 均完成注册、上报、探测、告警、重启及主题验收，两次输出 `E2E OK`。
- 最终生产实现的 `make web-e2e e2e` 再次退出 0（`e2e-current.log`）：4 个浏览器用例通过、2 个虚拟认证器用例按浏览器能力跳过，两个容器环境都输出 `E2E OK`。
- 补充后的 `make web-e2e` 退出 0：HTTPS 首次注册、重启后无密码登录、新域名 `other.localhost` 密码登录后重新绑定、仅保留新凭据并再次无密码登录。日志观察到 hub 停止和重新监听，而非仅创建第二个认证对象。
- 浏览器从管理页上传 ZIP 后，安装不启用；交互预览可加载 ES module、动态 import、CSS 和 SDK 公开数据。预览路由不离开短期能力路径，正式页面深链接、后退和 375 px 宽度工作。
- 管理员会话存在时，主题不能读取父 DOM、cookie、localStorage，不能直接 fetch 管理接口、注册 service worker 或发起 WebAuthn。直接打开主题 HTML 与 SVG 仍被沙箱隔离；管理节点列表没有出现恶意脚本尝试创建的节点。
- 下载原包的实际磁盘字节与上传 ZIP 一致。管理页桌面和手机截图已检查；手机下拉框原先撑到 430 px，修复后页面宽度为 375 px，表格在自己的容器内横向滚动。
- 节点列表增加来源 IP，查看与编辑状态都显示；地区沿用共享 emoji 国旗加国家码。线上空值经 SSH 确认由可信代理网段不匹配引起，修正后又定位并补齐 Cloudflare 到 Caddy 的来源解析。数据库来源与节点独立查询的公网出口一致，country 与 country_ip 成对更新；真实公开页显示日本国旗、JP 和在线状态，已登录管理页也显示国旗及正确的查得地址。没有为此修改地区查询算法或直接写数据库。Caddy 重启后配置哈希与运行配置回读一致，节点重试恢复在线。运维详情存于未跟踪的 `build/validation/production-proxy.md`。

## 缺陷注入

每次使用 apply_patch 注入并确认工具成功，读取原始命令退出码；注入已恢复。日志保存在忽略目录 `build/validation/`，不把日志文本搜索的退出码当作测试结果。

| 注入 | 观察到的正确失败 |
| --- | --- |
| 管理来源检查总放行 | `Origin: null` 创建节点返回 200 而非 403 |
| 忽略 public-dir 冲突 | 启用托管主题返回成功而非 FailedPrecondition |
| 原包响应置空 | SDK 0 归档与正常原包的字节一致性断言失败 |
| 忽略预览 TTL | 到期能力仍可用 |
| 忽略主题代数 | 切换后旧预览仍可用 |
| 忽略管理员会话撤销 | 登出后旧预览仍可用 |
| 移除文档响应 sandbox | 直接 HTML 的响应头安全断言失败 |
| 绕过公开静态总闸 | 关闭公开页后根路径仍输出主题容器 |
| 忽略已发布准入 | 未公开版本的资源返回 200 而非 404 |
| 忽略 SDK 准入 | SDK 0 文件直接执行入口返回 200 |
| 忽略缓存代数 | 删除主题后旧资源与选择仍被缓存返回 |
| 缩略图恢复整包读取 | 加入无关 8 MiB 文件后每次读取分配从约 15 KiB 增至约 16 MiB，比较断言失败 |
| 浏览器下载 RPC 替换为空响应 | 三浏览器实际下载文件与原 ZIP 不一致 |
| SDK 恢复丢失 RPC error.code | 三浏览器因缺少 not_found 失败；修正前的零时间范围夹具触发 invalid_argument，不算有效验红 |
| 主题资源读取错误仍返回 404 | disk failure 资源断言期望带 sandbox 的 500，实际 404 |
| 主题变更不清理旧预览链接 | 启用、删除版本、上传、GitHub 安装四个入口均仍显示已撤销链接 |
| HTTP 资源仍读取整个主题包 | 真实数据库中小资源冷读/三摘要轮换每请求约 16 MiB；改为单文件读取后约 20 KiB/9 KiB，单摘要对照约 9 KiB |
| 禁用资源缓存淘汰 | 3 个 16 MiB 资源导致缓存 48 MiB，257 个空文件导致 257 条，分别突破 32 MiB/256 条限制 |
| 摘要资源入口接受空主题 id | 不完整资源 URL 返回 200 而非 404 |
| 浏览器公开静态总闸旁路 | Chromium 关闸后仍返回真实主题 HTML，不含关闭说明；其他浏览器因共享关闭状态先失败于预览，不算这次有效验红 |

以上 Go 边界断言恢复后，API、web、cmd/hub 的同一测试集合退出 0；随后整仓命令再次通过。认证核心、GitHub 下载校验、版本/备份恢复的注入由对应实施单元执行并恢复，详见 `docs/implementation-status.md` 及本次会话的单元交付记录。缩略图分配测试最初夹具用 nil 内容触发非空约束，这次失败不算验红；修正夹具后重新注入整包读取才得到上述有效失败。

## 验证边界

未部署新版本、未安装真实第三方 GitHub Release 的 SDK 1 主题、未做物理认证器操作。GitHub 测试覆盖受控 TLS、DNS/拨号固定、逐跳主机/IP限制、资产归属、体积和错误路径，但不能据此声称已验证真实 GitHub 服务当前可达。新增 CI 步骤已接入，尚未在远端 GitHub Actions 执行。

审查提出 BFCache 恢复时消息通道关闭的候选风险。独立验证在三个浏览器实际跨文档返回时均观察到 persisted=false 和新文档，SDK 请求和导航正常，但这只能证明重载路径；未获得 persisted=true 的复现，不把该候选列为已确认缺陷，也不声称缓存恢复已通过。未为未复现的路径增加重连机制。

## 关键文件指纹

以下指纹对应上述集成验收时的生产代码；后续修改应补充新凭据。

```text
908ca77bf7338bf780a02d4351dbe2e21995887f5fec97c4600ebfbfd0ad9210  internal/hub/web/sandbox.go
3ebf369fa89f6f0a483b0de9039904faaae0a1c35066709eaaf48153917f45bd  internal/hub/web/theme.go
987398c7f572d88754b12f87be4931fe471518fe3cbacb6e2b1f7d4ce718e677  internal/hub/api/service.go
b10a391858721d86910562354f6964065f2c1e71d62a11414fcdf867065d65af  internal/hub/api/themes.go
52c7f1e20b3a74ed6c44245dd6e4fed3731bb97791ed49098650010bdd76b78f  internal/hub/auth/security.go
311806dc5fcc6e596d3420c5030db413cce14da09072abd1614a6df31ad558e1  internal/hub/auth/passkey_origin.go
5d8ae3de60c7b5fe002b7a4e092fcc13e5d40c3982c56d37b471546fa2b74a71  internal/hub/store/theme.go
0474b3cd6e3208c42a70f2712904442ea69e903dacb2c51c07af169913ddd357  internal/hub/store/restore_themes.go
```
