# 已发布 Agent 兼容门禁验证

## 代码与环境

- 日期：2026-09-30。
- 基点：`4839d22dc170d84494a5a80567ce02b4cfed436b`，分支 `feat/agent-observability` 的未提交工作树。
- 工作目录：`/Users/xjetry/work/vibe/probe`；环境：`Darwin 25.3.0 arm64`。
- 所有测试直接检查执行工具返回的原始退出码，未通过管道截断输出。
- 隔离下载及故障注入目录：`.compat-evidence.6SC0GW`；完成后删除，不作为交付文件。

对应文件 SHA256：

```text
94df8497f54536f09cf8b82ba394759d3ae0c1ec49ebf95ac02175a7ed71a167  .github/workflows/ci.yml
06fdeb13fd8de10b260c33ead1390b15aae21cd05886a7dc0a54531d56fac845  .github/workflows/release.yml
bf287969f4338c30ba11a06cc991bac8d0ee316af6bfebddbfa13cc3d26142a6  scripts/compat-agent.json
a2d27cc56b5d28b151d14af20202d84e0eac0d3951f47234fb5ded29fd67679f  scripts/compat-download.sh
937c75059c32e5700d673056ce273804fa5f19180dc9df1a3c7b77a8ba1d22d0  scripts/compat-download-test.sh
8ed61261f71b8cd97d23a112325e2b04af6b5d79831b5f41e6be90eafbb740cf  scripts/compat-e2e.sh
```

## 发布基线

选择正式发布 `v0.3.5`，以当前 Hub 检查旧次版本 Agent，不使用当前源码替代旧发布产物。

- 官方元数据：<https://api.github.com/repos/xjetry/heron-probe/releases/tags/v0.3.5>，`draft: false`、`prerelease: false`，发布时间 `2026-09-29T18:20:14Z`。
- 发布页：<https://github.com/xjetry/heron-probe/releases/tag/v0.3.5>。
- 官方摘要清单：<https://github.com/xjetry/heron-probe/releases/download/v0.3.5/SHA256SUMS>。
- 从正式发布地址下载两个归档并执行 `sha256sum`，实算值与 GitHub 资产 digest、`SHA256SUMS` 的对应行一致。仓库固定实算值，后续 CI 不信任下载时取得的摘要。

| 资产 | SHA256 |
| --- | --- |
| `heron-agent_linux_amd64.tar.gz` | `90cdd2185aeef30a939d13b19f0ef518654236717fe22b723bb021e18d0f8a7b` |
| `heron-agent_linux_arm64.tar.gz` | `d528af56fc84e4973139b8bdc3a8dee5968a328f6c31dd7d65e6f69a17a68198` |

## 检查与观察

| 命令 | 退出码 | 实际观察 |
| --- | --- | --- |
| `scripts/compat-download.sh /Users/xjetry/work/vibe/probe/.compat-evidence.6SC0GW/agents` | 0 | 打印 `v0.3.5 (stable)`；两个真实资产摘要匹配，固定名称的 Agent 解包成功。未执行发布二进制。 |
| `scripts/compat-download-test.sh` | 0 | 缺 pin、显式 null tag、缺架构、错误渠道、可变 tag、摘要错误均拒绝；下载错误保留退出码 22；正常及恢复后的双架构替身均可执行。 |
| `shellcheck -s sh scripts/compat-download.sh scripts/compat-e2e.sh scripts/compat-download-test.sh` | 0 | 三个脚本无诊断。先用未引用变量的 stdin 样本观察到退出码 1 和 `SC2154` / `SC2086`，随后重新运行此命令。 |
| `make -n compat-e2e` | 0 | 配方构建当前 Hub，随后执行 `scripts/compat-e2e.sh debian:bookworm-slim=Debian alpine:3.21=Alpine`。这是配方检查，不是运行兼容证据。 |
| 使用 Ruby `YAML.load_file` 读取两个工作流并打印 `make compat-e2e` 步骤 | 0 | CI 的 `ci` job 和发布的 `release` job 均有独立步骤及 `E2E_LISTEN_HOST: 0.0.0.0`，均无 `if` 或 `continue-on-error`；发布门禁位于镜像推送及 GitHub Release 创建之前。 |

## 缺陷注入

注入仅发生在私有目录中的脚本副本；每次通过 `sed` 回读确认落地，再执行同一命令 `.compat-evidence.6SC0GW/mutation/scripts/compat-download-test.sh`。

| 注入 | 退出码 | 实际报错 |
| --- | --- | --- |
| 将 null tag 拒绝分支改成 `exit 0` | 1 | `FAIL: no Heron baseline unexpectedly succeeded` |
| null 样本构造改为保留 `v9.8.7-rc.1` | 1 | `FAIL: absent baseline fault was not injected` |
| null tag 拒绝前调用 curl 替身下载有效样本 | 1 | 先观察 null tag 按预期拒绝，再报 `FAIL: the absent baseline attempted a download` |

还原每项注入后，仓库入口 `scripts/compat-download-test.sh` 再次退出 0，并打印 `compatibility download checks OK`。

## 验证边界

下载、替身、ShellCheck、YAML 及 dry-run 本身不能证明运行兼容性。主任务随后在 Go 1.27.1、Docker 29.4.0 / OrbStack aarch64 环境执行 `make e2e compat-e2e`，原始退出码为 0：当前源码与 v0.3.5 均完成 Debian / Alpine × amd64 / arm64 真实通信。日志 `build/agent-observability/agent-e2e.log` 包含两架构摘要匹配、四次双架构 `E2E OK` 和 `published agent compatibility OK: v0.3.5`。

这只代表本地容器矩阵，不代表远端 GitHub Actions 已执行；也没有将兼容基线扩大为所有历史版本。
