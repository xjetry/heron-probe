# 功能扩展实施与验证记录

## 范围

登录及认证变更审计、备份成功与恢复审计、持续资源告警、快照版本准入与不可变主题、标签批量选择与动态选择器、TOTP 和无密码 Passkey；同域名主题 SDK、版本安装与后台切换、Passkey 访问域名自动绑定；同步 README 与架构文档。

## 实现状态

2026-09-29，当前未发布工作树的数据库为 schema 23。主题身份、不可变产物和全站选择已分离，快照格式为 3；Go/TS 协议、同域主题容器和 Passkey 绑定接口同步演进中。双栈出口由 agent 异步探测并随 Facts 对账，保存在 node_facts.network；只进入管理读面，国家查询仍以可信上报来源为准。下列为当前实现口径，不表示全部浏览器与部署验收已经完成。后面的早期验收与补验章节是各自所列代码版本的历史记录，不能直接作为当前工作树通过凭据。

- Passkey 首次注册成功时绑定当前可信 HTTPS Origin 与主机名 RP ID，无需新装参数；后续域名不随请求自动变化。`--admin-origin` 仅为旧凭据无持久绑定时导入原配置，已有绑定后以数据库为准。TOTP 约束密码登录，恢复码单次消费。
- 主机 CLI `passwd` 重设密码，`security-reset --yes` 清除 TOTP、恢复码及 Passkey。两者撤销会话，不需要旧认证证明；重设密码不隐式清除认证器。
- 主题通过同域可信容器的沙箱运行，SDK 1 提供固定公开数据与路由桥接。安装公开 GitHub Release ZIP 或本地 ZIP 不自动启用；最多 20 个主题、每主题 3 个版本，当前与回滚版本受单版本清理保护。整主题卸载可回落内置页。
- 配置快照先上传全部保留版本的摘要命名原包，再发布元数据及当前、回滚引用。恢复接受 schema 17–22，在私有副本迁移，显式保留序列；源文件不改写。旧 SDK 包归档后仍可备份、恢复，但不可预览或启用。
- 固定标签批量选择与动态标签交集分开；任务和规则共享覆盖谓词。资源告警使用完整连续窗口与触发/恢复双阈值。

## 主题存储与安装器局部验证

2026-09-29 17:37，目录 `/Users/xjetry/work/vibe/probe` 的 schema 22 未提交工作树，执行 `go test ./internal/hub/store ./internal/hub/backup -count=1`：退出 0，store 8.255 秒、backup 7.878 秒。观察包括同 ID 两版共存、安装不激活、回滚引用保护、并发主题/版本上限、真实 ZIP 的旧库与两种历史快照迁移、所有保留版本及选择恢复、SDK 0 归档仍拒绝执行、缺原包重传修复。

存储缺陷注入均先回读确认落地：去掉主题数限制得到 30 次成功而非 20；去掉版本数限制得到 10 次新增成功而非 2；去掉选择保护导致当前版删除返回成功；旧包 SDK 强制设为 1 导致它自动启用；快照仅写当前版导致清单只有 1 包而非 3；切换不写 previous 导致回滚引用丢失。对应测试均退出 1，恢复实现后执行上述两包完整命令通过。最初的双版本断言在改实现前退出 1，实际只读到后上传的一版。

安装器的旧参数用例在改脚本前退出 1，观察到 `--theme-origin` 被保留且报告安装成功。移除参数表项并加迁移提示后，`go test ./deploy -run 'TestHubRemovedThemeOriginIsExplained|TestHubExecStartForms|TestHubFlagTableAgreesWithServe|TestHubCommandLineRefusesFlagsOutsideTheTable' -count=1` 退出 0，33.675 秒。测试实际执行 POSIX shell 脚本，主机目录、systemctl 与下载器为隔离替身；这不是 Linux 真机 systemd 启动验收。

2026-09-29 17:52 补验：恢复预检中跳过主题身份与版本清单对应检查、跳过历史 metrics 快照格式检查，先回读确认注入后，`TestThemeRestoreRejectsUnmatchedIdentityAndVersion` 与 `TestLegacyMetricsSnapshotRejectsUnknownFormat` 均退出 1，分别观察到孤儿主题、孤儿版本、空清单及格式 99 被错误接受。恢复实现后，`go test ./internal/hub/store ./internal/hub/backup -count=1` 退出 0，store 17.120 秒、backup 13.538 秒；`go vet ./internal/hub/store ./internal/hub/backup ./deploy`、`sh -n deploy/install-hub.sh` 均退出 0。完整 `go test ./deploy -count=1` 退出 0，387.844 秒，无跳过参数。

本次局部验证对象 SHA-256：

```text
d8fb52f2c7824755b3a70a15a7e2e3ed2d5dab81d0cc4894451779d63784d0f2  internal/hub/store/theme.go
4010467202118d6057c1136aa0bbeeff48b330e27aaf81164759b0d3cc15fe2a  internal/hub/store/theme_migration.go
0474b3cd6e3208c42a70f2712904442ea69e903dacb2c51c07af169913ddd357  internal/hub/store/restore_themes.go
685497f7dc046e94f55e1b689356589cc9298baee49b5cb949e67a52e045d181  internal/hub/store/restore.go
c1cc34ca1e2508bd8fc62dca8d85830e2c3a31c2a380c5d8beecfbb38cfaabb8  internal/hub/store/snapshot.go
3b40ef712159082252fd5edb28fd0b08c99107b960d2403bc505e62f191c22b5  deploy/install-hub.sh
```

这些局部结果不覆盖当前主题 UI、真实浏览器权限隔离、硬件 Passkey、真实 GitHub 出网或 Linux 服务启动，须由当前集成验收补齐。后续修改存储、恢复或安装器后必须重跑对应命令，不能沿用这里的时效。

## 早期受限环境验收

以下命令在最后的选择器与认证动作重构后执行。工作目录 `/Users/xjetry/work/vibe/probe`，前端命令在 `web` 子目录。Go 使用 `GOCACHE=/private/tmp/probe-go-cache GOPROXY=off GOSUMDB=off`，系统为 macOS，Go 1.27.1。判定直接取命令退出码，不以日志过滤命令的状态代替。

| 命令 | 实际观察 |
| --- | --- |
| `pnpm test` | 退出 0，56 文件、607 测试通过 |
| `pnpm build` | 退出 0，管理端与公开端均生成；有大于 500 kB 的 chunk 提示 |
| `go test -count=1 ./internal/hub/auth ./internal/hub/backup ./internal/hub/metric ./internal/hub/probe` | 退出 0，四包全量通过 |
| `go test -count=1 ./internal/hub/store -skip TestIngestForgetWaitsForRegistryOutsideIngestLocks` | 退出 0；仅跳过需要本地监听的该用例，不代表 store 无跳过全量通过 |
| `go test -count=1 ./cmd/hub -run 'TestSecurityReset\|TestRestore' -skip TestRestoreStartsHubWithRecoveredConfig` | 退出 0；CLI 安全重置及不需监听的恢复用例通过 |
| `go test -count=1 ./internal/hub/alert -run 'TestResourceContinuousWindowsAndMissingReadings\|TestDeletedScope'` | 退出 0 |
| `go vet ./...` | 退出 0 |
| `GOOS=linux GOARCH=amd64 go vet ./...` | 退出 0，仅跨平台静态校验 |
| `go mod tidy -diff` | 退出 0，无依赖差异；此前缓存缺依赖的阻塞已解除 |
| `buf lint` | 退出 0；协议生成此前已执行成功 |
| `shellcheck deploy/install-hub.sh` | 退出 0 |
| `go test -count=1 ./...` | 退出 1，未全量通过。网络测试遭 `bind: operation not permitted`，macOS 采集测试遭 sysctl/系统权限限制；deploy 包在本次命令内通过，耗时 121 秒 |

本次日志：`/private/tmp/probe-web-final.log`、`probe-core-refactor-final.log`、`probe-store-final.log`、`probe-cli-final.log`、`probe-alert-final.log`、`probe-vet-final.log`、`probe-vet-linux-final.log`、`probe-all-final.log`。日志是本机临时凭据，关键结果已记在本文件。

## 缺陷注入与回读

认证与资源/选择器的注入细节分别见 [认证验证](validation-admin-security.md) 与 [资源选择器验证](validation-resource-selectors.md)。这些记录的文件哈希早于最终重构，以本文件重跑结果补充，不把早期网络通过结果冒充最终树全量通过。

- 登录无通知渠道时跳过审计、备份成功时跳过审计、CLI 重置允许创建空库、CLI 重置保留认证器的注入均触发对应失败，撤回后定向测试通过。
- 迁移注入将 `admin_security` 初始 generation 改为 1，回读确认 SQL 已改变后执行 `go test -count=1 ./internal/hub/store -run 'TestMigrationFromV19PreservesMetricsAndStartsWithoutFactors|TestFrozenSchemasFollowMigrations'`。退出 1，明确失败于读回 `Generation:1`；恢复默认初始值后，同一命令退出 0。日志 `probe-migration-{green,red,restored}.log`。
- 快照实现者验证了摘要错误、遗漏旧迁移、遗漏配置恢复、先发布配置后上传主题等注入；并曾在可监听环境运行恢复 CLI、启动 hub、从 GetSite 读到恢复标题。该运行早于总验收，不替代最终树服务启动验收。
- 所有故意注入均已撤回，没有为沙箱监听限制修改产品逻辑或放宽测试。

最终关键生产文件 SHA-256：

```text
6fc30f68e3537033078bc778de24e90320ec958bb4f6fd28fbcc4d8171ad2a41  internal/hub/auth/security.go
11bac12255543fe1b04b3d1b815ee952a79a6b80dd2f9cfb4f0bb66b2d33ecfb  internal/hub/api/security.go
6383b31e9a8e1a7fc1d7c37bfc014a89f573007319e094f1bdebfab22bfce38a  internal/hub/store/selector.go
31696b26a88f2e1328cd81f278f98b82312cd46baf8e300622f64d58ce3fcfbf  internal/hub/store/migrations.go
720c6d5e9122bbf1b71abe6db7c354f30cb53e3f8a46ff44a9a1532b9e2293b1  internal/hub/store/restore.go
116792db6355f7b6354f2a8f24ae8f7dd430fb914da4e549729ae142de7eed8f  internal/hub/backup/themes.go
```

## 提交前复核

2026-09-29，在同一 macOS 工作目录解除监听与文件系统限制后复核。Go 环境仍为 `GOCACHE=/private/tmp/probe-go-cache GOPROXY=off GOSUMDB=off`。前端再次执行 `pnpm test` 与 `pnpm build`，均退出 0，仍为 56 文件、607 测试；构建仍有大 chunk 提示。

第一次 `go test -count=1 ./...` 退出 1，不再出现权限阻塞：`cmd/hub` 与 `internal/hub/api` 的旧测试把事件列表等同于通知列表，新增审计事件使总数假设失效；离线旧库夹具还固定在 schema 19。其余包通过，包括系统采集、store 和 deploy。修正事件分类断言、保留期过滤与 schema 20 回退夹具，不改变产品行为。

修正后的定向命令退出 0：

```sh
go test -count=1 ./cmd/hub ./internal/hub/api -run 'TestServeBackupUploadsAndDeliversRecovery|TestServeDeliversOfflineAlerts|TestServeRequeuesPendingNotifications|TestServeEvaluatesProbeAlerts|TestServePrunesAlertEvents|TestOfflineCommandsRejectV8|TestLoginNotify'
```

对新增及调整的断言注入缺陷，每次先回读差异确认注入落地，再执行对应测试并检查原始退出码：

| 注入 | 测试与实际失败 |
| --- | --- |
| 无通知渠道时不写成功登录审计 | `TestLoginNotifySuccessDeliversAndUsesTrustedSource`、`TestLoginNotifyTokenReadsAndCleanup`、`TestLoginNotifyOnlyUpdatePreservesAppearance` 均因审计条数缺失失败 |
| 普通登录失败也创建通知投递 | `TestLoginNotifyLockThresholdOnlyOnce`、`TestLoginNotifySummaryCarriesZonedTime` 明确报普通失败不应带投递 |
| 反转事件保留期比较符 | `TestServePrunesAlertEvents` 两种保留期均读回错误的旧事件 ID，未放宽为只核对条数 |
| API token 读取设置也创建登录事件 | `TestLoginNotifyTokenReadsAndCleanup` 报读前后事件不一致 |
| 将规则 firing 事件错误保存为 recovered | `TestServeRequeuesPendingNotifications` 报 firing 未送达，回读可见 recovered 已成功投递 |
| 登录摘要忽略 hub 时区，固定 UTC | `TestLoginNotifySummaryCarriesZonedTime` 的失败审计、通知摘要及 Telegram 正文均不匹配 |
| v8 夹具遗漏回退一个 schema 20 指标列 | `TestOfflineCommandsRejectV8` 五个子命令均在真实迁移时报重复列 `disk_used_pct_n` |
| 不写普通失败审计 | `TestLoginNotifyLockThresholdOnlyOnce` 在第一次失败后报 `failed=0`，预期为 1 |

上述注入命令均退出 1。撤回后，`internal/hub/store/alert.go`、`internal/hub/auth/admin.go`、`internal/hub/auth/security.go`、`internal/hub/store/migrations.go`、`internal/hub/api/settings.go` 的 SHA-256 均与注入前一致；额外变更只有三份测试文件。`go vet ./...` 与 `GOOS=linux GOARCH=amd64 go vet ./...` 重新执行均退出 0。

撤回注入后，使用与首次失败相同的 `go test -count=1 ./...` 命令重新验收，退出 0，所有包通过，无跳过参数。`cmd/hub` 耗时 26.025 秒、`internal/hub/api` 耗时 17.991 秒、deploy 耗时 123.393 秒。恢复后启动 hub、绑定本地端口并通过 `GetSite` 回读标题的 `TestRestoreStartsHubWithRecoveredConfig` 也包含在该次通过结果中，不再只依赖早期受限环境前的证据。

日志：`/private/tmp/probe-unrestricted-all.log`、`probe-commit-targeted.log`、`probe-commit-red-{audit,notify,boundaries,envelope,failures}.log`、`probe-commit-all.log`。

本次测试文件 SHA-256：

```text
970cbf617bbec7d0d2a3d8721e6460a6c2005a70e0e451001617e9757b5c32ab  cmd/hub/serve_alert_test.go
67cf6d257320803a27b38030fe5c4d3cac61cf1b1182d513642b8814e91c0309  cmd/hub/stats_test.go
27e5e5fa079ee5584fdc0e8377675b8f9f0b8f5ce4a9c678fc1383dd5f95208a  internal/hub/api/login_notify_test.go
```

## 待验收与限制

- 真实浏览器/系统认证器的 Passkey 注册、无密码登录、桌面与移动端交互未验收；现有证据是正式 WebAuthn 库的软件签名校验及 jsdom 组件测试。
- S3 使用本地替身测试，未操作真实云端 bucket。
- 未取得完整独立代码审阅回执。已做简化审查和手工检查；此前 `.git` 不可写导致未暂存新文件无法纳入该审查，现已能完整暂存，但未补做独立审阅，不能声称已取得完整审阅覆盖。
- 简化审查指出节点编辑时覆盖全量回读、资源规则重复读取节点窗口、选择器标签逐项查询的潜在成本；本轮未追加性能改造，也没有性能测量结果。

## systemd 卸载清理验证

2026-09-29，在上述提交前工作树上补齐 agent 与 hub 的本地 drop-in 清理。普通卸载保留，purge 删除 `/etc/systemd/system/<服务名>.service.d` 与 `/run/systemd/system/<服务名>.service.d`，不依赖主单元存在；不跟随目录符号链接，不清理共享配置、发行版目录或系统 journal。README、架构与两处旧注释同步。

使用同一离线 Go 环境，在临时根目录内执行真实安装 shell 脚本与文件删除；systemctl 和账户查询使用替身，不触碰宿主机服务。`TestSystemdUninstallDropInScope` 的 16 个组合覆盖两服务、普通卸载/purge、主单元存在/缺失、真实目录/符号链接。

- 实现前同一测试命令退出 1，8 个 purge 组合均报 `purge retained drop-in`；实现后退出 0。
- 去掉两处 purge 条件，回读确认后测试退出 1，8 个普通卸载组合均报 `uninstall removed drop-in`。
- 在 purge 中注入越界删除，回读确认后测试退出 1，journal、共享/type-prefix drop-in、其他服务、发行版配置及符号链接目标的内容保护断言均报缺失。
- 撤回全部注入后，`go test -count=1 ./deploy -run TestSystemdUninstallDropInScope` 与 `go test -count=1 ./deploy` 均退出 0；`go vet ./deploy`、Makefile 指定的完整 ShellCheck 文件集合及 `git diff --check` 均退出 0。
- `scripts/install-accept.sh` 仅更新一处过时注释，`sh -n` 通过。额外对该验收脚本执行 ShellCheck 报 SC2016（远端 `sh -c` 的单引号命令段）；该文件不在 Makefile 的 ShellCheck 集合中，本轮未修改这些命令段，也未将这次检查记为通过。

日志保存在 `/private/tmp/probe-dropin-{red,green,red-uninstall,red-boundary,restored,deploy-final}.log`。本轮未运行 OrbStack 的 Linux 真机安装验收，当前证据不证明真实 systemd 的运行时状态；未发布。

验证对象 SHA-256：

```text
b332b1acaa7a285f5496e6f6b2f379419203b37e3d308b6ef82ff6f5b70c09e2  deploy/install.sh
afb8599f34056a3097052b572576676bd0ace66a8b19cb2ee7c6d6d0450666f2  deploy/install-hub.sh
f227f6e7946ad03894e7e4a48c84aa7c0efc5a13e6e2435a600d215e84cd69fc  deploy/installpurge_test.go
```
