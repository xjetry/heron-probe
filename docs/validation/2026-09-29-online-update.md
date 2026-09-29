# 在线更新验收记录

## 范围与版本

- 工作树基点：`bac9c75`，本记录与在线更新实现一同提交；尚未发布或部署。
- 支持 Linux systemd 的 Hub 与 Agent，仅固定官方正式 Release。更新器和服务定义仍由 root 安装器更新。
- schema 24 保存节点更新授权；快照不包含授权，恢复不会重放旧更新任务。
- 以下隔离测试使用测试专用版本 `v0.3.0` 至 `v0.6.0`，不代表这些版本已正式发布。

## 实际运行观察

环境为专用 OrbStack Debian 12 arm64 机器 `pia-update-20260929`，未在生产执行故障注入。

- Hub 从旧版更新成功，实际主进程、运行文件摘要与任务结果匹配。
- 候选立即退出时回滚到原程序。候选写坏数据库再退出时，停服后逐字节比较数据库与程序备份均一致，恢复后 `GetSite` 成功。
- 在事务 `verifying` 阶段向更新器发送 SIGKILL，systemd 重启更新器后状态变为 `rolled_back`，数据库、程序与备份逐字节一致。
- OrbStack 默认的全局 drop-in 关闭部分加固；验收显式恢复 `ProtectSystem=strict`、`ProtectHome=yes`、`PrivateTmp=yes`、`NoNewPrivileges=yes` 和固定 `ReadWritePaths`，回读生效后重跑上述中断恢复，通过。
- 最终候选 Hub 与 Agent 均在上述加固下更新到测试版 `v0.6.0`。Agent 任务由真实 `StartUpdate` RPC 创建，经 Report 下发，新进程上报后回读 `succeeded`；两角色运行文件与候选逐字节一致。
- 真实 socket 拒绝 root 调服务用户接口、服务用户调维护接口、无权用户连接；`Verify` 拒绝非 MainPID 和错误程序摘要，接受实际 MainPID 与匹配摘要。
- 从固定官方 API 读到最新版本 `v0.2.0`，真实下载并校验该版 Hub、Agent arm64 资产。Hub 程序 SHA-256 为 `136d6a9c9bedca8b1f7aa8a1f7ce1dffeed866fb9df3ac1c8e7378ab716de1ca`，Agent 为 `11fab4f90db11b25196d34bcfc08f6b7bec6437041b828fd9c1772f15a4a358d`。隔离机原 DNS 返回 `198.18.0.0/15` fake-IP，被策略拒绝；仅在隔离机 hosts 中固定当次公共 DoH 查询的真实地址后通过，没有放宽产品地址检查。
- `scripts/install-accept.sh --only hub-debian-arm64 --only debian-arm64` 返回 0；两套 systemd 安装、升级、卸载及更新器账户、socket、状态权限检查通过。

原始证据保存在本地忽略目录 `build/validation/`：`update-db-accept.log`、`update-hardening-accept.log`、`update-final-accept.log`、`update-socket-accept.log`、`update-official-network.log`、`update-install-accept.log`。

## 自动检查与缺陷注入

- Go 全量 `make test`、`make lint`、`make build`、`make script-test` 已通过；构建覆盖 Linux/darwin amd64/arm64，发行构建另覆盖五个 Linux Agent/更新器架构。
- 更新器、节点状态管理与 Agent 协调器的 `go test -count=1 -race` 通过。
- 前端完整测试为 62 文件、728 测试通过；三浏览器端到端检查为 13 通过、2 跳过。跳过的是 Firefox/WebKit 虚拟 Passkey，在线更新页面三浏览器均执行，检查了桌面、375px 布局、能力说明和禁用按钮；已查看截图。
- 回滚中断后的恢复意图持久化为 `rolling_back`，恢复失败禁止 ready、提交新任务及安装维护。旧实现可错误确认候选，新断言先红，修复后同命令绿。
- 回滚文件恢复完成但旧进程启动失败，以及启动门清理失败，均不能接受新工作；注入后断言分别红在支持状态、维护握手、新提交，修复后绿。
- 已消费任务跨重启不可重放；恢复结果落盘后只重试启动，不再次恢复备份覆盖后续数据。
- 下发后超时归为 `unconfirmed`，不推断成功或失败；匹配 ID、目标版本与实际新进程版本的迟到终态可校正。五阶段丢结果测试先红后绿。
- 人工升级到同版或高版而缺少对应本机记录时归为 `unconfirmed`，不阻塞后续更新；不相干任务不得冒称本任务成功。更新了原有跨 ID/目标/进程版本的对照断言。
- 移除实际文件摘要检查后，隔离机断言红在 `accepted different executable digest`；恢复代码后通过。
- 移除 Hub 更新按钮的能力限制后，三浏览器均红在不支持平台的按钮仍可用；恢复后重跑同一 `make web-e2e` 命令，13 通过、2 跳过。
- 初次检查不可用的旧原因不得污染后续成功提交；删除原因清理后断言红在 `successful submit retained stale unavailable reason`，恢复后绿。
- 官方源、安装摘要、安装互斥、卸载、快照恢复和后台权限的缺陷注入记录见本地 `update-backend-result.md` 及相关 mutation 日志。

## 审查与限制

- 简化检查按复用、可维护性、执行效率三个视角进行；线程容量受限时由主代理串行完成。复用共享 GitHub 传输与 API 客户端、统一事务日志类型，保留安全边界，不为缩短代码删除守卫。Report 不增加数据库或 socket 阻塞，重复状态不重复落库。
- 独立审查发现的恢复失败确认、状态原因残留、下载预算耦合、超时丢终态、人工安装占用问题已修复并增加回归。
- `ce-code-review` receipt：`status: complete`，`Ready to merge`，run ID `online-update-20260930`，无遗留可执行发现。独立正确性、安全及 Claude 对抗审查已执行；其余视角受线程限制由审查调度上下文完成，最终修复由本地复核，不宣称跨模型审过最终代码。
- `git diff --check` 对 buf 新生成的 `update_pb.ts` 报文件末尾空行；保持生成器原样输出，不手改生成物。
- SHA256SUMS 与资产共享官方发布信任根，不是独立签名。真实触控/安全密钥 Passkey 验收仍由用户本人完成。
- 真实完整更新验收为 arm64；amd64 经构建、静态检查及后续生产版本回读，不将编译等同于完整故障注入。

## systemd 257 权限兼容

- 基点 `de56b6f`，`v0.3.1` 正式发布后部署到 Debian 13 Hub，主服务可用，但更新器 `/status` 返回 `supported:false`、版本查询 `fork/exec ... operation not permitted`。Agent 部署因此暂停，没有将服务启动视为在线更新通过。
- 生产与隔离 Debian 13 / systemd `257.13-1~deb13u1` 均用最小 systemd 服务复现：显式 `User=root`、`RestrictSUIDSGID=yes` 与 `NoNewPrivileges=yes` 下 `CapEff` 缺少 `CAP_SETUID`，`runuser` 同样失败。仅关闭 `PrivateTmp` 仍失败；关闭 `RestrictSUIDSGID` 成功。最终保留所有原加固并显式声明 `AmbientCapabilities=CAP_SETUID`，相同操作成功。
- Hub 与 Agent 更新器单元同步修正。`scripts/update-credentials-accept.sh` 加载仓库真实单元，显式恢复 OrbStack 全局覆盖掉的加固；`TestSystemCredentialDrop` 使用产品共享的 `credentials()` 启动子进程，验证非 root UID/GID、清空附加组、无 permitted/effective/ambient capability、继承 `NoNewPrivileges`，且不能切回 root。
- 相同脚本在 systemd 252（`pia-update-20260929`）与 257（`pia-update257-20260929`）两种角色均退出 0。旧单元先准确失败于降权执行；分别注入 root UID/GID、附加组 0、子进程 ambient `CAP_SETUID`，测试准确失败于对应身份、组、可用 capability 断言。恢复后同命令均通过。
- 原始日志位于 `build/validation/update-credentials-*`。一次隔离测试出现 `NAMESPACE` 目录不存在；另一次共享文件刚修改后读取到不完整脚本，后续语法检查及重跑通过。这些失败保留，不声称已证明为既有 flake，也不据此修改生产加固。
- `v0.3.0` tag 的发行流水线因安装器 ShellCheck 规则失败，未创建 Release；保留该 tag，不改写历史。`v0.3.1` 发行与主干 CI 均成功，GitHub latest 与 GHCR latest 已回读；本节权限修复尚待新版本发布与生产在线更新回读。
- 本轮 `make ci` 完整运行两次均退出 0。审查后仅加强测试完成握手：子进程完成所有断言后输出标记，父测试检查后输出另一个标记，脚本检查本次独立日志，避免 Go 无匹配测试仍退出 0 的假绿。旧测试程序与错误子测试名分别验红，恢复后 systemd 252/257 两角色通过。
- 本轮审查已实际运行独立 Claude 只读检查与本地安全/正确性复核；无产品修复缺陷，测试假绿发现已关闭。收尾代理受线程限额阻断，receipt 为 `failed`，不称完整审查通过。`Code review: skipped (ce-code-review unavailable)`：技能顶层已终止且无法产生完整 receipt，按不可用路径补人工全 diff 检查。审查记录：`/tmp/compound-engineering-501/ce-code-review/systemd257-20260930`。
