# 已发布 Agent 兼容验收

## 基线与范围

- 工作基点：`f741ce15f2b8ec14b62a1126f030e11842a917a4`；开发目录为 `.worktrees/monitor-improvements`。
- 2026-09-29 读取 `https://api.github.com/repos/xjetry/probe/releases`：只有 `v0.1.0-rc.1`，`draft=false`、`prerelease=true`，不是稳定正式版。
- 本地同名 tag 指向 `ee8f9a387f582f9c20bfac2966cea2cb306f34d0`。
- 资产摘要以发布页资产及同一版本的 `SHA256SUMS` 为来源，固化在 `scripts/compat-agent.json`。实际下载的 amd64、arm64 包都与固定摘要相同。
- CI 与发布流程运行当前源码 E2E 和固定旧 agent E2E；二者共用完整断言，只替换 agent 二进制目录。没有基线、资产不可得、摘要不符都失败，不跳过。

## 已执行检查

环境：macOS、OrbStack Linux VM，默认 `/bin/sh`；本节只记录执行时的脚本修改，不代表其它并行功能或 GitHub runner 已验收。

| 命令/实验 | 原始退出码 | 实际观察 |
| --- | --- | --- |
| `scripts/compat-download.sh <私有新目录>` | 0 | 两个真实发布包摘要匹配，明确打印预发布渠道。 |
| `docker run --rm --platform linux/amd64 alpine:3.21 uname -m` | 0 | `x86_64`，不是只编译未运行。 |
| `docker run --rm --platform linux/arm64 alpine:3.21 uname -m` | 0 | `aarch64`。 |
| `make script-test` | 0 | 发布规则、镜像回读与兼容下载的脚本测试均完成。 |
| `shellcheck -s sh scripts/compat-download.sh scripts/compat-e2e.sh scripts/compat-download-test.sh` | 0 | 三个新脚本无诊断。 |
| `shellcheck -s sh scripts/e2e.sh` | 1 | SC2012/SC2015 info 级诊断；从基点提取的原脚本以相同参数检查也返回 1、同类诊断。两份脚本的 `-S warning` 检查均返回 0；没有将完整脚本 lint 声称为通过。 |
| `make -n e2e compat-e2e` | 0 | 当前 hub 只构建一次；源码 agent 与发布 agent 使用两个独立入口；这是配方检查，不是运行兼容凭据。 |

## 缺陷注入

1. 下载测试先用有效替身包确认正例通过，然后注入缺 pin、缺 arm64、把预发布渠道改成稳定、把 tag 改成 latest、摘要不符以及 curl 返回 22。分别因目标原因失败；失败下载目录均被移除。
2. 临时把真实下载脚本的摘要比较替换成 `: "$actual" "$expected"`，用 `rg` 确认替换已写入。原命令 `scripts/compat-download-test.sh` 返回 1，错误为 `checksum mismatch unexpectedly succeeded`。还原比较后相同命令返回 0。
3. 实际已下载的旧 amd64 二进制配 `E2E_AGENT_VERSION=v0.0.0-fault` 运行 `scripts/e2e.sh`，返回 1，错误为 `amd64 agent version v0.1.0-rc.1, expected v0.0.0-fault`；发生在启动 hub 之前。由该次运行创建的容器已回收。
4. `E2E_AGENT_BIN_DIR` 指向已确认没有 agent 文件的私有父目录，运行相同 E2E 入口，返回 1，错误为 `missing executable agent for amd64`，没有进入容器启动。
5. 真实运行固定旧 agent，CLI `version` 核对通过后，只在 HTTP 响应代理中把首个节点的 `facts.agentVersion` 从 `v0.1.0-rc.1` 改成 `v0.0.0-injected`。代理先回读 JSON 确认注入落地，再交给 E2E。脚本返回 1，目标错误为 `reported agent version differs from the compatibility baseline`。原响应与注入响应分别保存在私有验证目录的 `before.json`、`after.json`；代理只通过 PATH 注入，没有修改产品或测试源码。

## 完整运行发现的验收口径问题

实际命令：`env E2E_HUB_PORT=18190 E2E_HOOK_PORT=18191 scripts/compat-e2e.sh debian:bookworm-slim=Debian alpine:3.21=Alpine`。日志目录为 `/var/folders/2g/hlstnsjd36x_rh6w6yhb25jm0000gn/T/tmp.fVQ6sLfBwS`。

- `compat-e2e.log`：退出码 1，旧断言把所有事件数与 3 比较；实际为 3 个均已送达的规则事件、3 个登录审计和 1 个改密审计。两个节点的版本、架构、系统、注册上报、CPU、双任务历史、公开隔离、离线/恢复告警在失败前已经过实际断言。该运行跨过主任务重建 hub 的窗口，只用于定位断言，不作为最终产物兼容凭据。
- `compat-e2e-rerun.log`：退出码 1，规则事件分类修复后，陈旧的任务版本断言失败。`UpdateNodeTasks` 本来就会推进任务版本，而脚本曾在一串节点更新前记录版本，更新后拿它检查重启。版本从 `1790655030` 变为 `1790655126`，任务内容仍完整。没有为此修改产品版本规则。
- 规则事件断言改为先按 `ruleId` 存在分类，完整库表总数另与 API 全量事件数对照。用真实响应注入缺少恢复事件、重复规则事件、规则全部未送达，先分别回读出规则数 2、4、3（最后一份送达数 0），相同 jq 断言均返回 1；包含四个审计事件的正例返回 0。
- 重启检查改为紧邻重启保存完整 `{version,tasks}`，重启后在任何写调用前比较，包括顺序与覆盖范围。以真实任务响应生成版本加一、任务逆序、丢失一个任务三份夹具，先回读确认差异，`cmp -s` 均返回 1，原夹具对照返回 0。删除版本检查取删除前最新读数。

## Linux 监听路径实验

在 OrbStack Linux VM 的 host 网络命名空间内启动两个 HTTP 服务，分别监听 `0.0.0.0:19190` 和 `127.0.0.1:19191`，服务启动日志确认两者均已监听。另一个默认 bridge 容器经 `docker network inspect bridge` 读出的真实网关 `198.19.0.1` 请求：

- 全接口监听返回 HTTP 501，客户端断言通过，退出码 0。
- 回环监听连接被拒绝，客户端退出码 1，异常为 `ConnectionRefusedError`。
- 实验服务已回收。

OrbStack 的 `host-gateway` 实际映射到代理地址 `0.250.250.254`，代理能转接回环；所以没有把经该代理的成功请求当作原生 Linux 行为证据。GitHub Linux runner 显式设置 `E2E_LISTEN_HOST=0.0.0.0`；开发机默认保持回环监听。

## 最终完整运行

主任务冻结的 hub SHA256 为 `f63c54316215243fb25ee506d7df921ba8ef4eb407333058fc33728a277a01f6`；修复验收口径后的 E2E 脚本 SHA256 为 `63b2cca0ebf750fdbf67eaca95c39c3fd68fb8a000b20768aaa8432a6e5ff8d2`。

- 同一条兼容命令最终退出码 0，日志 `compat-e2e-final.log` 同时有 Debian `E2E OK`、Alpine `E2E OK`、`published agent compatibility OK: v0.1.0-rc.1`。
- 两个发行版都运行 amd64、arm64 发布二进制，两个节点的真实服务端 facts 都回读为 `v0.1.0-rc.1`。Debian 为 `Debian GNU/Linux 12 (bookworm)`，Alpine 为 `Alpine Linux v3.21`。
- Debian 完整原始产物目录：`/var/folders/2g/hlstnsjd36x_rh6w6yhb25jm0000gn/T/tmp.SClUfpNF8E`；Alpine：`/var/folders/2g/hlstnsjd36x_rh6w6yhb25jm0000gn/T/tmp.8iAXXvuZX9`。
- 最终 `make script-test`、三个兼容脚本 ShellCheck、E2E 的 warning 级 ShellCheck、`git diff --check` 均返回 0。

当前源码构建 agent 也完成同一 `scripts/e2e.sh` 入口的完整验收：

| 完整命令 | 原始退出码 | 日志与实际观察 |
| --- | --- | --- |
| `env E2E_HUB_PORT=18190 E2E_HOOK_PORT=18191 AGENT_IMAGE=debian:bookworm-slim EXPECT_OS=Debian scripts/e2e.sh` | 0 | `current-debian.log`，最后 `E2E OK`；完整产物目录 `tmp.K6giSQGJMe`。 |
| `env E2E_HUB_PORT=18190 E2E_HOOK_PORT=18191 AGENT_IMAGE=alpine:3.21 EXPECT_OS=Alpine scripts/e2e.sh` | 0 | `current-alpine.log`，最后 `E2E OK`；完整产物目录 `tmp.Z7R8imXP1P`。 |

两个完整产物目录均位于 `/var/folders/2g/hlstnsjd36x_rh6w6yhb25jm0000gn/T/`。执行前后重新计算 SHA256，hub 和脚本均与上面相同；当前 amd64 agent 为 `ef7b7ef46ecf2f3aca5b78fd8b9bd17ca3647f84ae2eaa09b658846e10010480`，arm64 agent 为 `fc3e40f501fbaf273f514a51c94a99314c2c762aaa1d8a00c82dc1290f7a9250`。

GitHub Actions runner 本身尚未执行，不能把本机 OrbStack 验收表述为线上 CI 已通过。当前固定基线仅证明已发布预发布版的兼容性，项目尚没有稳定正式版可作为旧版基线。
