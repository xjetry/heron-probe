# Heron

Heron 是轻量自托管主机监控工具。关注状态，不接管系统。

## 名称

| 用途 | 名称 |
| --- | --- |
| 产品与中文文案 | Heron，不另设中文译名 |
| 仓库 | `xjetry/heron-probe` |
| Go module | `github.com/xjetry/heron-probe` |
| 命令与 Linux 服务 | `heron-hub`、`heron-agent` |
| hub 镜像 | `ghcr.io/xjetry/heron-hub` |
| RPC 命名空间 | `heron.v1` |
| hub 数据库 | `/var/lib/heron/heron.db`，容器内 `/data/heron.db` |
| agent 配置 | `/etc/heron-agent/config.json` |
| macOS LaunchDaemon | `xyz.heron.agent` |
| 环境变量 | `HERON_OFFLINE_AFTER`；API 示例使用 `HERON_HUB`、`HERON_TOKEN` |

Heron 不兼容更名前的命令、服务路径、环境变量、会话 cookie、API token 前缀及 `probe.v1` RPC 路径，不提供旧名称别名。安装器不会迁移旧 probe 服务或数据。首次使用 Heron 须按 Heron 的路径安装和配置。

`probe` 仍是网络探测的领域术语：探测任务、指标表、枚举及相关包名不随产品名变化。历史计划和验收记录保留当时的名称、命令、文件哈希与环境，不作为 Heron 版本已通过验收的凭据。

## 图形

鹭鸟侧影表现长颈、尖喙与细腿，使用青灰色，琥珀色眼点。SVG 源文件为 `web/src/assets/heron.svg`，由管理端、内置公开页、favicon 与 README 共用，不加载远程字体或图片。

管理端使用图形与 Heron 字标。公开页保留站点自定义标题和 logo，仅在未提供 logo 时显示默认鹭鸟。图形旁已有文字时使用空替代文本，避免读屏重复播报。

更名不改变界面布局、监控口径、存储表结构或权限范围。
