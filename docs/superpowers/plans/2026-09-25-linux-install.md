# Linux 发布与安装 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 把 agent（五个 Linux 架构）与 hub（两个 Linux 架构）经 `make release` 发到 GitHub Releases，给出一条命令装好并纳入 systemd/OpenRC 管理的 `deploy/install.sh`，并在真实启动的机器上完成分发行版验收。

**Architecture:** 构建逻辑只在 Makefile 一处：`make release VERSION=vX.Y.Z` 产出 `dist/`（tar 包 + SHA256SUMS + install.sh），CI 推 tag 调同一目标；`deploy/` 下的服务定义与安装脚本是真实文件，打包原样收入。安装脚本以 root 按"检测 init →（卸载分支不看架构）→ 检测架构 → 建同名组与用户 → 确保 CA → 下载校验 → register → 服务"的顺序工作，重跑即升级。验收脚本 `scripts/install-accept.sh` 在 OrbStack 真机上按发行版×架构矩阵逐格断言；每格首次安装用面板命令的管道形态。

**Tech Stack:** POSIX sh（busybox 兼容）、GNU Make、GitHub Actions、debug/elf 静态门禁（已有 `scripts/checkstatic`）、OrbStack 机器、proto/connect + React（面板命令）。

**Spec:** docs/superpowers/specs/2026-09-17-probe-architecture-design.md（§14、§12、§10、§4.7）

**实验依据:** .superpowers/drafts/linux-distro-spike-report.md（工具矩阵、busybox 参数、OpenRC 日志坑、ping_group_range 与 CA/时区结论）；动态链接判定依据 `scripts/checkstatic` 既有实现与其反例。实验结论已写入 spec §13 第 2 项与 §14。代码与注释只引用 spec 章节，不引用本段、不引用 `.superpowers/` 下的文件——那些不在仓库里，读代码的人追溯不到。

## Global Constraints

- 全部 `CGO_ENABLED=0`；与 libc 无关由静态链接承载：产物不得带动态解释器，构建产出二进制时就检查（§14）。
- 检查是三条独立的交付约束：没有 `PT_INTERP`、没有 `DT_NEEDED`、构建设置显式为 `CGO_ENABLED=0`；它们不互相等价，缺一条就拒绝。检查器是 `scripts/checkstatic`，发布流水线必须把全部 Linux 产物（agent 与 hub）交给它，而不是复制一份当前的文件清单（§14）。
- 构建逻辑只在 Makefile 一处，本地验收与线上发布用的是同一套产物；资产名不带版本号；服务定义在仓库 `deploy/` 下是真实文件，打包时原样放入，不在脚本里以字符串另存一份；仓库必须公开（§14）。
- Linux 安装脚本（install.sh，以 root 运行）的安装路径顺序：检测 init 系统与架构 → 创建固定的系统用户 `probe-agent`（主组同名）→ 确保 CA 证书 → 下载 tar 包与 `SHA256SUMS` 并校验 → `probe-agent register`，再把配置交给该用户（目录 0700、文件 0600；register 以 root 写入，不改属主服务就读不到）→ 安装并启动检测到的 init 对应的服务（§14）。`--uninstall` 在架构检测之前返回：卸载不需要架构，不支持的架构不能挡住卸载。
- 建用户排在下载与注册之前：它若失败，注册窗口的名额尚未消耗、旧服务尚未停止（§14）。先建同名组、再以它为主组建用户，建完回查 `id -gn` 是同名组，不信退出码。busybox 的 `adduser` 不指定组时会把用户放进 `nogroup`，而 OpenRC 的 `command_user` 与配置的 chown 都写 `probe-agent:probe-agent`。Debian 的 perl 版 adduser 收到 busybox 风格参数时打印用法、不建用户，却返回 0（§14）。
- 重跑即升级：已有配置时跳过注册；重新注册会多消耗一个窗口名额，并在 hub 上多出一个节点。二进制先写同目录临时文件再 `mv` 替换；服务定义每次覆盖（§14）。换一个版本重跑后节点数不变、版本为新版本（§12）。
- init 系统支持 systemd 与 OpenRC。判定：`/run/systemd/system` 存在为 systemd；否则 `/sbin/openrc-run` 存在为 OpenRC（`/run/openrc` 表示已启动）；两者都不是时安装脚本报错并列出支持的 init，不静默降级（§14）。
- OpenRC 的 `output_log` 与 `error_log` 的文件必须在启动前建好并交给运行用户，由服务脚本的 `start_pre` 建立并改属主，每次启动都成立而不只在安装时建一次：supervise-daemon 降权后才打开它们，打不开时子进程秒退、反复拉起，而 `rc-service status` 仍显示 started（§14）。`depend()` 用 `after net` 而不是 `need net`：上报循环自带退避重试（§4.7），`need net` 会让没有服务提供 net 的主机拒绝启动它。
- 安装脚本用 POSIX sh，兼容 busybox，按实际存在的工具分支，不假设任何单一工具（§14）。
- 面板命令是 `curl … | sh -s -- 参数`（wget 同形）。此时脚本来自 stdin，脚本里任何读 stdin 的命令都会吞掉余下部分，安装在中途无声结束（§12）。每个可能读 stdin 的外部命令（包管理器、建用户与组、`probe-agent register`、`systemctl`、`rc-service`、`rc-update`）都显式 `</dev/null`。不能用 `exec </dev/null`：那会切断脚本自己的来源。
- 能力授予交给 init（systemd `AmbientCapabilities=`、OpenRC `capabilities="^cap_net_raw"`），不依赖 setcap（§14）。验收直接读服务进程的 `/proc/<pid>/status`：`Uid` 第一列不为 0，`CapEff` 含 bit 13（`CAP_NET_RAW`，与 `0x2000` 按位与非零）（§12）。
- CA 是否存在按文件探测而不是按发行版名判断；缺失时先装 `ca-certificates`。下载地址与 hub 地址分别判断，任一为 https 就装：默认下载地址就是 https，只看 hub 会先死在 TLS 上；hub 为 https 而下载地址为 http 时，register 与上报同样要 CA（§14）。不能把两个地址拼成一个串再看前缀。
- `--purge` 删配置、日志目录、用户与同名组，并回查；删不掉就非零退出，不打印 warning 后 exit 0。各发行版删除工具对组的处理不一致，不能信退出码，也不能半成功还报成功（§12 要求卸载后用户与组都不存在）。
- 安装验收在真实启动的机器上跑（本地 OrbStack；验收脚本只创建与删除带自己前缀的机器）；这类验收依赖真实 init，不进 CI；`install.sh` 的 shellcheck（POSIX 模式）进 CI，systemd 单元的 `systemd-analyze verify` 在验收机器上跑（§12）。每格首次安装用面板命令的管道形态；重跑与卸载用下载到机器上的文件。
- `install.sh` 的一切行为测试都在容器里跑，不在宿主 macOS 上执行 install.sh，更不能 `sudo`。宿主上只跑 shellcheck 与 `sh -n`。容器镜像用 `debian:bookworm-slim`（与 e2e 同一镜像，本地已有缓存），不用 `debian:12-slim`。
- 需要 root 的 `orb -m <机器> …` 一律 `orb -m <机器> -u root …`。OrbStack 默认以宿主同名普通用户执行，install.sh 会报 must run as root，也写不了 `/root`。以 `-u root` 为准，不混用 sudo。
- 不用 `sed -i ''`（macOS 形态）。删行用 `grep -v … > tmp || [ "$?" = 1 ]` 再 `mv tmp 原文件`（grep 无保留行时退出码为 1 属正常，只放过 1，其余错误照常失败）；替换用 `sed 's///' 文件 > tmp && mv tmp 文件`。
- 每条新断言做一次缺陷注入，确认它红且红在正确的原因上；声称"只有 X 会让它红"的断言，把非 X 的原因也注入一遍（§12）。
- 判成败的命令不接管道：`cmd > log 2>&1; echo $?`；Go 测试一律 `go test -count=1`（项目规则）。
- 代码注释与提交信息不写过程信息（任务/轮次编号、方案代号、审阅引用、"按上一轮"）；注释写 WHY 与不变式，并指明前提由谁保证（项目规则）。注释不引用实验草稿路径。
- 不打补丁：根因在哪层就在哪层修，不在调用方加特判（项目规则）。
- OrbStack 安全：只创建、修改、删除 `pia-` 前缀的机器；已有的 `agent1`、`debian12-amd64`、`debian12-arm64` 绝对不能停止、修改或删除（项目规则）。
- 实现者不改 docs/，不派子代理（项目规则）。
- 打包 tar 前设 `COPYFILE_DISABLE=1`：macOS 的 bsdtar 会把扩展属性打成 `._*` 条目，busybox 解包会带出多余文件。
- 验收 hub 用 18085、发行目录 HTTP 用 18086，与 e2e 的 18080/18081 错开，两者可同时跑。

## Review Focus

1. **用户照面板给的命令 `curl … | sh -s -- 参数` 运行**：此时脚本本身来自 stdin，脚本里任何读 stdin 的命令都会吞掉脚本余下部分，表现为安装在中途无声结束。测试落在 Task 6：每格首次安装用管道形态，与面板命令逐字同形。
2. **adduser 的两种不兼容实现**（busybox 与 Debian perl 版，perl 版错参数时 rc=0 却不建用户）：install.sh 必须按实现分支且建完回查主组。测试落在 Task 2 Step 6（busybox 分支容器用例，`id -gn probe-agent` 为 `probe-agent`）与 Step 7（注入强制错误参数，红在 `failed to create system user probe-agent with primary group probe-agent`）。
3. **OpenRC 日志路径每次启动都要存在且属于运行用户**（supervise-daemon 降权后才打开日志，否则秒退循环而 status 显示 started）：靠服务脚本 `start_pre` 而不是安装时建一次。测试落在 Task 6 的 Alpine 格"删日志目录后重启仍健康"断言与 Step 5 注入（去掉 `start_pre` 后红在"服务状态 started 但节点不上报"）。
4. **https 遇上无 CA 的极简镜像**（Debian/Ubuntu 基础容器不带 CA bundle）：下载地址与 hub 地址分别判断，任一为 https 就装 CA。只看 hub 会让 https 的默认下载地址先失败；只看下载地址会让 hub 为 https、下载为 http 时 register 与上报没有 CA。测试落在 Task 2 Step 8（两条容器用例都断言 CA 被装上）与注入（gate 只看 `BASE_URL` 后，hub https + 下载 http 那条红）。
5. **重跑不得重新注册**（窗口名额有限、重复注册多出幽灵节点）：已有配置时即使给了 `--key` 也只提示"沿用现有注册"。测试落在 Task 6 的重跑断言（节点数不变 + 输出含提示；版本从 A 变为 B 是同一重跑块里的另一条断言）与 Step 6 注入（强制每次都 register，红在节点数 +1）。

无下载器的极简系统（Debian/Ubuntu 基础容器形态：curl 与 wget 都没有）不再列在 Review Focus。install.sh 仍须在建用户之后、发起任何网络请求之前报出指明依赖的错误并非零退出。测试保留在 Task 2 Step 5。

---

### Task 1: deploy/ 下的服务定义（systemd 单元与 OpenRC 脚本）

**Files:**
- Create: `deploy/systemd/probe-agent.service`
- Create: `deploy/openrc/probe-agent`

**Interfaces:**
- Produces：`deploy/systemd/probe-agent.service`（systemd 单元，服务名 probe-agent）；`deploy/openrc/probe-agent`（openrc-run 服务脚本，`depend()` 为 `after net`）。两者被 Task 4 的 `make release` 原样打进每个 agent tar 包，被 Task 2 的 install.sh 安装到 `/etc/systemd/system/probe-agent.service` 与 `/etc/init.d/probe-agent`。

单元的运行时证据是 Task 6 的 `systemd-analyze verify`、服务实际运行、进程 Uid/CapEff 断言。不在本任务 grep 刚写进去的指令文字：那证明不了任何运行时性质。

- [ ] **Step 1: 写 systemd 单元**

`deploy/systemd/probe-agent.service`：

```ini
[Unit]
Description=probe monitoring agent
# 采集只读 /proc、/sys 与配置；加固项若遮蔽了采集路径不会以启动失败显形，
# 由真机验收的"服务与 root 手动运行指标对照"承载（spec §12、§14）。
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
User=probe-agent
Group=probe-agent
ExecStart=/usr/local/bin/probe-agent run --config /etc/probe-agent/config.json
Restart=on-failure
# 裸机 Debian/Ubuntu/Alpine 的 ping_group_range 默认关闭，没有 CAP_NET_RAW 时
# ICMP 探测只能回报 error；Rocky 9 放开是发行版默认值，不作依赖（§13 第 2 项）。
AmbientCapabilities=CAP_NET_RAW
CapabilityBoundingSet=CAP_NET_RAW
NoNewPrivileges=yes
ProtectSystem=strict
ProtectHome=yes
PrivateTmp=yes
# 不用 DynamicUser=：未开 nesting 的 LXC 容器建不了挂载命名空间时，
# systemd 对静态 User= 会跳过挂载类隔离照常启动，对 DynamicUser= 拒绝启动
# （226/NAMESPACE）；其隐含的 RestrictSUIDSGID= 在此明写补回（spec §14）。
RestrictSUIDSGID=yes

[Install]
WantedBy=multi-user.target
```

- [ ] **Step 2: 写 OpenRC 服务脚本**

实测过的 OpenRC 脚本没有 depend 块。这里加 `after net` 而不是 `need net`：agent 的上报循环自带退避重试（§4.7），不需要硬依赖网络服务；`need net` 会让没有服务提供 net 的主机拒绝启动它。这一改动由 Task 6 的 Alpine 格与重启自启断言验证。下面代码块里的注释是要写进仓库的文字，不含本段的验证说明。

`deploy/openrc/probe-agent`：

```sh
#!/sbin/openrc-run
# shellcheck disable=SC2034 # 以下变量由 openrc-run 框架读取，脚本内不引用。

name="probe-agent"
description="probe monitoring agent"
supervisor=supervise-daemon
command="/usr/local/bin/probe-agent"
command_args="run --config /etc/probe-agent/config.json"
command_user="probe-agent:probe-agent"
# 与 systemd 的 AmbientCapabilities 等价：ping_group_range 关闭时靠它拿到
# CAP_NET_RAW（§14，OpenRC 0.55.1 实测）。
capabilities="^cap_net_raw"
output_log="/var/log/probe-agent/probe-agent.log"
error_log="/var/log/probe-agent/probe-agent.err"

depend() {
	# 只要求排在网络之后启动，不硬依赖某个网络服务：上报循环连不上 hub 时自带退避重试（§4.7），
	# 网络晚于本服务就绪不影响正确性。
	after net
}

start_pre() {
	# supervise-daemon 降权后才打开 output_log/error_log：目录与文件必须事先属于
	# 运行用户，否则子进程打不开日志秒退、被反复拉起，而 status 仍显示 started（§14）。
	# 每次启动都建，不靠安装时建一次。
	mkdir -p /var/log/probe-agent
	touch /var/log/probe-agent/probe-agent.log /var/log/probe-agent/probe-agent.err
	chown -R probe-agent:probe-agent /var/log/probe-agent
}
```

- [ ] **Step 3: 语法检查与注入**

宿主上只做语法检查，不执行安装脚本：

```sh
cd /Users/xjetry/work/vibe/probe-install && sh -n deploy/openrc/probe-agent > /tmp/p1-openrc-syntax.log 2>&1; echo $?
```

Expected: 0。注入：删 `start_pre` 的 `}`。`git add deploy/openrc/probe-agent` 建基准；`git diff --quiet; echo $?` 期望 1；复跑 `sh -n` 期望非 0 且日志含 syntax error；`git checkout -- deploy/openrc/probe-agent` 还原，复跑期望 0。

- [ ] **Step 4: 提交**

```bash
cd /Users/xjetry/work/vibe/probe-install && git add deploy/systemd deploy/openrc && git commit -m "deploy: systemd 单元与 OpenRC 服务脚本，能力授予与日志属主由 init 承载"
```

---

### Task 2: deploy/install.sh（安装/升级/卸载脚本）

**Files:**
- Create: `deploy/install.sh`

**Interfaces:**
- Consumes：`deploy/systemd/probe-agent.service`、`deploy/openrc/probe-agent`（tar 包内文件，由 Task 4 打包）；agent 的 `register`/`run` 子命令与 `SaveConfig` 的 0700/0600 权限不变式（`cmd/agent/main.go`、`internal/agent/client/config.go`，不改）。
- Produces：`deploy/install.sh`，参数 `--hub URL --key KEY [--name N] [--version vX.Y.Z] [--base-url URL]` 与 `--uninstall [--purge]`。`--purge` 删 `$CFG_DIR`、`$LOG_DIR`、用户与同名组，删不掉非零退出。Task 4 把它复制进 `dist/install.sh`；Task 6 与面板命令（Task 8）以同一参数集调用它。以管道形态运行时，包管理器、建用户与组、`probe-agent register`、`systemctl`、`rc-service`、`rc-update` 都显式 `</dev/null`。

前置：本机已装 shellcheck（`brew install` 由控制端处理）。本任务的行为测试全部在容器里跑；宿主只跑 Step 9 的 shellcheck。

- [ ] **Step 1: 写完整脚本**

`deploy/install.sh`：

```sh
#!/bin/sh
# probe-agent 安装脚本：下载、校验、注册并安装服务；重跑即升级。
# 与 libc 无关由静态链接产物承载（发布流水线的静态门禁保证），本脚本不处理。
# 以 curl … | sh -s -- 运行时，脚本本身来自 stdin。脚本里任何读 stdin 的命令都会吞掉
# 脚本余下部分，安装在中途无声结束。所以每个可能读 stdin 的外部命令都显式 </dev/null。
# 不能用 exec </dev/null：那会切断脚本自己的来源。
set -eu

BIN=/usr/local/bin/probe-agent
CFG_DIR=/etc/probe-agent
CFG=$CFG_DIR/config.json
LOG_DIR=/var/log/probe-agent
SVC_USER=probe-agent
REPO=https://github.com/xjetry/probe

usage() {
  echo "usage: install.sh --hub URL --key KEY [--name N] [--version vX.Y.Z] [--base-url URL]" >&2
  echo "       install.sh --uninstall [--purge]" >&2
  exit 2
}

HUB=""; KEY=""; NAME=""; VERSION=""; BASE_URL=""; UNINSTALL=0; PURGE=0
while [ $# -gt 0 ]; do
  case "$1" in
    --hub) HUB=$2; shift 2;;
    --key) KEY=$2; shift 2;;
    --name) NAME=$2; shift 2;;
    --version) VERSION=$2; shift 2;;
    --base-url) BASE_URL=$2; shift 2;;
    --uninstall) UNINSTALL=1; shift;;
    --purge) PURGE=1; shift;;
    *) usage;;
  esac
done

[ "$(id -u)" = 0 ] || { echo "install.sh must run as root" >&2; exit 1; }

# init 判定只认两个真实标记：都没有时是容器等无 init 环境，报错而不静默降级。
if [ -d /run/systemd/system ]; then INIT=systemd
elif [ -x /sbin/openrc-run ]; then INIT=openrc
else echo "unsupported init: expected systemd (/run/systemd/system) or OpenRC (/sbin/openrc-run)" >&2; exit 1; fi

stop_service() {
  case "$INIT" in
    systemd) systemctl stop probe-agent </dev/null 2>/dev/null || true;;
    openrc) rc-service probe-agent stop </dev/null 2>/dev/null || true;;
  esac
}

# 用户与同名组都删并回查：各发行版删除工具对组的处理不一致，不能信退出码，也不能半成功还报成功。
delete_account() {
  if id "$SVC_USER" >/dev/null 2>&1; then
    if command -v userdel >/dev/null 2>&1; then userdel "$SVC_USER" </dev/null
    elif command -v deluser >/dev/null 2>&1; then deluser "$SVC_USER" </dev/null
    fi
  fi
  if grep -q "^$SVC_USER:" /etc/group; then
    if command -v groupdel >/dev/null 2>&1; then groupdel "$SVC_USER" </dev/null
    elif command -v delgroup >/dev/null 2>&1; then delgroup "$SVC_USER" </dev/null
    fi
  fi
  if id "$SVC_USER" >/dev/null 2>&1 || grep -q "^$SVC_USER:" /etc/group; then
    echo "failed to delete user or group $SVC_USER" >&2; exit 1
  fi
}

if [ "$UNINSTALL" = 1 ]; then
  stop_service
  case "$INIT" in
    systemd)
      systemctl disable probe-agent </dev/null 2>/dev/null || true
      rm -f /etc/systemd/system/probe-agent.service
      systemctl daemon-reload </dev/null;;
    openrc)
      rc-update del probe-agent default </dev/null 2>/dev/null || true
      rm -f /etc/init.d/probe-agent;;
  esac
  rm -f "$BIN"
  if [ "$PURGE" = 1 ]; then
    rm -rf "$CFG_DIR" "$LOG_DIR"
    delete_account
  fi
  echo "probe-agent uninstalled"
  exit 0
fi

# 卸载不需要架构：不支持的架构不应让人连卸载都做不了。
case "$(uname -m)" in
  x86_64) ARCH=amd64;; aarch64) ARCH=arm64;; armv7l) ARCH=armv7;;
  i386|i686) ARCH=386;; riscv64) ARCH=riscv64;;
  *) echo "unsupported architecture: $(uname -m)" >&2; exit 1;;
esac

# 首次安装必须有注册凭据；升级沿用现有配置，不需要也不接受重新注册。
if [ ! -f "$CFG" ] && { [ -z "$HUB" ] || [ -z "$KEY" ]; }; then
  echo "--hub and --key are required for the first install" >&2
  exit 2
fi

# 建用户排在下载与注册之前：它若失败，注册窗口的名额尚未消耗、旧服务尚未停止。
# 主组必须是同名组：OpenRC 的 command_user 与配置文件属主都写 probe-agent:probe-agent，
# 而 busybox 的 adduser 不指定组时会把用户放进 nogroup。先建组、再以它为主组建用户，建完回查。
create_account() {
  nologin=/sbin/nologin
  [ -x /usr/sbin/nologin ] && nologin=/usr/sbin/nologin
  if ! grep -q "^$SVC_USER:" /etc/group; then
    if command -v groupadd >/dev/null 2>&1; then
      groupadd --system "$SVC_USER" </dev/null
    elif command -v addgroup >/dev/null 2>&1; then
      if addgroup --help 2>&1 | grep -qi busybox; then addgroup -S "$SVC_USER" </dev/null
      else addgroup --system "$SVC_USER" </dev/null; fi
    else
      echo "no groupadd or addgroup available to create the system group" >&2; exit 1
    fi
  fi
  if ! id "$SVC_USER" >/dev/null 2>&1; then
    if command -v useradd >/dev/null 2>&1; then
      useradd --system -M -d /nonexistent -s "$nologin" -g "$SVC_USER" "$SVC_USER" </dev/null
    elif command -v adduser >/dev/null 2>&1; then
      # adduser 有两种不兼容的实现（busybox 与 Debian perl 版），按实现分支；
      # 建完必须回查——perl 版收到 busybox 风格参数会打印用法、不建用户，却返回 0。
      if adduser --help 2>&1 | grep -qi busybox; then
        adduser -S -D -H -s "$nologin" -G "$SVC_USER" "$SVC_USER" </dev/null
      else
        adduser --system --no-create-home --shell "$nologin" --ingroup "$SVC_USER" "$SVC_USER" </dev/null
      fi
    else
      echo "no useradd or adduser available to create the system user" >&2; exit 1
    fi
  fi
  [ "$(id -gn "$SVC_USER" 2>/dev/null)" = "$SVC_USER" ] || {
    echo "failed to create system user $SVC_USER with primary group $SVC_USER" >&2; exit 1; }
}
create_account

[ -n "$BASE_URL" ] || {
  if [ -n "$VERSION" ]; then BASE_URL="$REPO/releases/download/$VERSION"
  else BASE_URL="$REPO/releases/latest/download"; fi
}

ca_bundle_present() {
  for f in /etc/ssl/certs/ca-certificates.crt /etc/pki/tls/certs/ca-bundle.crt /etc/ssl/cert.pem /etc/pki/ca-trust/extracted/pem/tls-ca-bundle.pem; do
    [ -f "$f" ] && return 0
  done
  return 1
}
is_https() { case "$1" in https://*) return 0;; esac; return 1; }
# 下载地址与 hub 地址分别判断：默认下载地址就是 https；hub 用 https 时 register 与上报也要 CA。
if is_https "$BASE_URL" || is_https "$HUB"; then
  if ! ca_bundle_present; then
    if command -v apt-get >/dev/null 2>&1; then
      apt-get update </dev/null && DEBIAN_FRONTEND=noninteractive apt-get install -y ca-certificates </dev/null
    elif command -v apk >/dev/null 2>&1; then
      apk add --no-cache ca-certificates </dev/null
    elif command -v dnf >/dev/null 2>&1; then
      dnf install -y ca-certificates </dev/null
    elif command -v yum >/dev/null 2>&1; then
      yum install -y ca-certificates </dev/null
    else
      echo "no CA bundle found and no supported package manager to install ca-certificates" >&2; exit 1
    fi
    ca_bundle_present || { echo "ca-certificates installation did not produce a CA bundle" >&2; exit 1; }
  fi
fi

PKG="probe-agent_linux_$ARCH.tar.gz"
if command -v curl >/dev/null 2>&1; then FETCH=curl
elif command -v wget >/dev/null 2>&1; then FETCH=wget
else echo "neither curl nor wget is available to download $PKG" >&2; exit 1; fi
command -v sha256sum >/dev/null 2>&1 || { echo "sha256sum is required to verify downloads" >&2; exit 1; }

work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT

dl() {
  if [ "$FETCH" = curl ]; then curl -fsSL -o "$2" "$1"; else wget -q -O "$2" "$1"; fi
}
dl "$BASE_URL/$PKG" "$work/$PKG"
dl "$BASE_URL/SHA256SUMS" "$work/SHA256SUMS"
# SHA256SUMS 含全部资产，只核对本包那一行；busybox 的 sha256sum 没有 --ignore-missing。
# 行格式是 64 位十六进制、两个空格、文件名；awk 按第二字段取行，带 * 前缀或单空格会静默取不到。
(cd "$work" && awk -v p="$PKG" '$2 == p' SHA256SUMS > verify.txt && [ -s verify.txt ] && sha256sum -c verify.txt)

# 升级先停服务；二进制写同目录临时文件再 mv，运行中的进程不会读到写了一半的文件。
[ -f "$CFG" ] && stop_service
tar -xzf "$work/$PKG" -C "$work"
install -m 0755 "$work/probe-agent" "$BIN.tmp.$$"
mv -f "$BIN.tmp.$$" "$BIN"

if [ ! -f "$CFG" ]; then
  # 注册消耗一个窗口名额，排在所有可能失败的步骤之后。
  set -- register --hub "$HUB" --key "$KEY" --config "$CFG"
  if [ -n "$NAME" ]; then set -- "$@" --name "$NAME"; fi
  "$BIN" "$@" </dev/null
  # register 以 root 写入配置（SaveConfig 定 0700/0600）；不改属主，服务用户读不到。
  chown "$SVC_USER:$SVC_USER" "$CFG_DIR" "$CFG"
else
  if [ -n "$KEY" ]; then echo "existing config found; keeping the current registration (--key ignored)"; fi
fi

# 服务定义每次覆盖，单元的改动随升级下发。
# 此前已 stop；首装时本就未运行。用 start，不依赖 restart 对已停服务等价于 start。
case "$INIT" in
  systemd)
    install -m 0644 "$work/probe-agent.service" /etc/systemd/system/probe-agent.service
    systemctl daemon-reload </dev/null
    systemctl enable probe-agent </dev/null
    systemctl start probe-agent </dev/null;;
  openrc)
    install -m 0755 "$work/probe-agent.openrc" /etc/init.d/probe-agent
    # 重跑时它已在 default runlevel 里；只在不在时才加，不依赖 rc-update 对重复 add 的退出码。
    [ -e /etc/runlevels/default/probe-agent ] || rc-update add probe-agent default </dev/null
    rc-service probe-agent start </dev/null;;
esac
echo "probe-agent installed and started ($INIT, $ARCH, $PKG)"
```

- [ ] **Step 2: 参数与非 root（容器）**

不在宿主执行 install.sh。`--bogus` 在参数循环里就 exit 2，不依赖 root；非 root 用 `--user 65534`。注入同样在容器里验证。

```sh
cd /Users/xjetry/work/vibe/probe-install && docker run --rm --platform linux/arm64 -v "$PWD/deploy:/deploy:ro" debian:bookworm-slim sh /deploy/install.sh --bogus > /tmp/p2-bogus.log 2>&1; echo $?
cd /Users/xjetry/work/vibe/probe-install && docker run --rm --platform linux/arm64 --user 65534 -v "$PWD/deploy:/deploy:ro" debian:bookworm-slim sh /deploy/install.sh --hub http://x --key y > /tmp/p2-nonroot.log 2>&1; echo $?
```

Expected: 第一条 2（usage），第二条 1 且日志含 `install.sh must run as root`。注入：删掉 root 检查行（可移植删行，不用 `sed -i ''`）：

```sh
cd /Users/xjetry/work/vibe/probe-install && git add deploy/install.sh && grep -v 'install.sh must run as root' deploy/install.sh > /tmp/p2-install.inj && mv /tmp/p2-install.inj deploy/install.sh && git diff --quiet; echo $?
```

期望 1。非 root 容器命令复跑，期望不再出现 root 报错而落到后续分支（无 init 的 `unsupported init`，红在检查缺失）。`git checkout -- deploy/install.sh` 还原。

- [ ] **Step 3: 无 init 环境的报错（容器）**

容器里两个 init 标记都不存在，进程是 root。不在宿主 `sudo`。

```sh
cd /Users/xjetry/work/vibe/probe-install && docker run --rm --platform linux/arm64 -v "$PWD/deploy:/deploy:ro" debian:bookworm-slim sh /deploy/install.sh --hub http://x --key y > /tmp/p2-noinit.log 2>&1; echo $?
```

Expected: 1 且日志含 `unsupported init: expected systemd (/run/systemd/system) or OpenRC (/sbin/openrc-run)`。

- [ ] **Step 4: 首次安装缺凭据的报错（容器）**

```sh
cd /Users/xjetry/work/vibe/probe-install && docker run --rm --platform linux/arm64 -v "$PWD/deploy:/deploy:ro" debian:bookworm-slim sh -c 'mkdir -p /run/systemd/system && sh /deploy/install.sh' > /tmp/p2-nokey.log 2>&1; echo $?
```

Expected: 2 且日志含 `--hub and --key are required for the first install`。注入：把缺凭据检查删掉。`git add deploy/install.sh` 建基准；`git diff --quiet; echo $?` 期望 1；复跑期望不再报凭据错误而进入下载阶段失败（红在检查缺失、失败位置后移）；`git checkout -- deploy/install.sh` 还原。

- [ ] **Step 5: 无下载器时的明确报错**

下载地址是 http，用不到 CA，不要在容器里预装 `ca-certificates`。

```sh
cd /Users/xjetry/work/vibe/probe-install && docker run --rm --platform linux/arm64 -v "$PWD/deploy:/deploy:ro" debian:bookworm-slim sh -c '
  mkdir -p /run/systemd/system &&
  sh /deploy/install.sh --hub http://192.0.2.1:9 --key dummy --base-url http://192.0.2.1:9/' > /tmp/p2-nodl.log 2>&1; echo $?
```

Expected: 1 且日志含 `neither curl nor wget is available`（bookworm-slim 两者都没有；install.sh 在建用户之后、任何网络请求之前报出）。注入：把下载工具探测替换为直接调用 curl。建基准；`git diff --quiet; echo $?` 期望 1；复跑期望失败原因退化成命令找不到（不含 `neither curl nor wget`）；还原复跑期望恢复。

- [ ] **Step 6: busybox 建用户分支（Review Focus 2 正例）**

断言主组，不只断言用户存在。

```sh
cd /Users/xjetry/work/vibe/probe-install && docker run --rm --platform linux/arm64 -v "$PWD/deploy:/deploy:ro" alpine:3.21 sh -c '
  printf "#!/bin/sh\n" > /sbin/openrc-run && chmod +x /sbin/openrc-run &&
  sh /deploy/install.sh --hub http://192.0.2.1:9 --key dummy --base-url http://192.0.2.1:9/ > /tmp/out.log 2>&1 || true
  id -gn probe-agent' > /tmp/p2-busybox-user.log 2>&1; echo $?
```

Expected: 0 且日志末行是 `probe-agent`（alpine 容器没有 useradd，走 busybox addgroup/adduser 分支；脚本随后会死于下载，但用户已以同名组为主组建好）。

- [ ] **Step 7: adduser 错参数的缺陷注入（Review Focus 2 红）**

先核实前提，再注入。bookworm-slim 不一定带 Debian 的 perl 版 adduser 包：容器里先 `command -v adduser`，没有就 `apt-get update && DEBIAN_FRONTEND=noninteractive apt-get install -y adduser`（该包同时提供 `addgroup`）。强制走 addgroup/adduser 分支的办法：删掉 `useradd` 与 `groupadd`（来自 passwd 包），脚本才会落到 addgroup/adduser。不要只删 useradd——groupadd 还在时脚本走 groupadd/useradd，测不到 perl adduser。

正例（不改脚本）：宿主注入之前先跑下面这条。挂载 `:ro`，脚本只在宿主上改。

```sh
cd /Users/xjetry/work/vibe/probe-install && docker run --rm --platform linux/arm64 -v "$PWD/deploy:/deploy:ro" debian:bookworm-slim sh -c '
  command -v adduser >/dev/null 2>&1 || { apt-get update && DEBIAN_FRONTEND=noninteractive apt-get install -y adduser; }
  rm -f /usr/sbin/useradd /usr/sbin/groupadd &&
  mkdir -p /run/systemd/system &&
  sh /deploy/install.sh --hub http://192.0.2.1:9 --key dummy --base-url http://192.0.2.1:9/ > /run/out.log 2>&1 || true
  id -gn probe-agent' > /tmp/p2-adduser-ok.log 2>&1; echo $?
```

Expected: 0，日志末行 `probe-agent`，`/run/out.log` 的失败在下载而不是建用户。前提不成立（没有 adduser，或删掉 useradd/groupadd 后仍走 useradd）就先修环境，不要把注入的红当成 perl 分支的证据。

注入：把 busybox 判别改成恒真，让 perl adduser 收到 busybox 参数。不用 `sed -i ''`。注入写在宿主文件上，容器仍以 `:ro` 读这份已注入的文件：

```sh
cd /Users/xjetry/work/vibe/probe-install && git add deploy/install.sh && sed 's/      if adduser --help 2>&1 | grep -qi busybox; then/      if true; then/' deploy/install.sh > /tmp/p2-install.inj && mv /tmp/p2-install.inj deploy/install.sh && git diff --quiet; echo $?
cd /Users/xjetry/work/vibe/probe-install && docker run --rm --platform linux/arm64 -v "$PWD/deploy:/deploy:ro" debian:bookworm-slim sh -c '
  command -v adduser >/dev/null 2>&1 || { apt-get update && DEBIAN_FRONTEND=noninteractive apt-get install -y adduser; }
  rm -f /usr/sbin/useradd /usr/sbin/groupadd &&
  mkdir -p /run/systemd/system &&
  sh /deploy/install.sh --hub http://192.0.2.1:9 --key dummy --base-url http://192.0.2.1:9/ > /run/out.log 2>&1; echo "exit=$?"; tail -2 /run/out.log' > /tmp/p2-adduser-inject.log 2>&1; echo $?
```

Expected: 注入落地（第一条 1）；容器内 `exit=1` 且日志尾部含 `failed to create system user probe-agent with primary group probe-agent`——红在主组回查，而不是后续下载。`git checkout -- deploy/install.sh` 还原；同容器命令复跑（仍先删 useradd/groupadd），期望走出 perl adduser 分支、`id -gn` 为 `probe-agent`、失败点后移到下载。

- [ ] **Step 8: https 触发 CA 安装（Review Focus 4，两个方向）**

两条都要装上 CA。第二条是反方向：hub 为 https、下载地址为 http。

```sh
cd /Users/xjetry/work/vibe/probe-install && docker run --rm --platform linux/arm64 -v "$PWD/deploy:/deploy:ro" debian:bookworm-slim sh -c '
  mkdir -p /run/systemd/system &&
  sh /deploy/install.sh --hub http://192.0.2.1:9 --key dummy --base-url https://definitely.invalid/ > /run/out.log 2>&1 || true
  ls -l /etc/ssl/certs/ca-certificates.crt' > /tmp/p2-ca-base.log 2>&1; echo $?
cd /Users/xjetry/work/vibe/probe-install && docker run --rm --platform linux/arm64 -v "$PWD/deploy:/deploy:ro" debian:bookworm-slim sh -c '
  mkdir -p /run/systemd/system &&
  sh /deploy/install.sh --hub https://definitely.invalid --key dummy --base-url http://192.0.2.1:9/ > /run/out.log 2>&1 || true
  ls -l /etc/ssl/certs/ca-certificates.crt' > /tmp/p2-ca-hub.log 2>&1; echo $?
```

Expected: 两条都是 0，且日志含 `-rw-r--r-- … /etc/ssl/certs/ca-certificates.crt`（bookworm-slim 原本没有任何 CA 文件；下载随后因 DNS 或无下载器失败属预期，CA 已先装上）。注入：把 `if is_https "$BASE_URL" || is_https "$HUB"; then` 改成 `if is_https "$BASE_URL"; then`（只看下载地址）。建基准；`git diff --quiet; echo $?` 期望 1；复跑第二条（hub https、下载 http）期望 `ls` 失败（CA 未装），红在 gate 条件。第一条（下载 https）在这个注入下仍会装 CA，不能当红因。还原后两条复跑期望恢复。

- [ ] **Step 9: shellcheck**

宿主上只跑 shellcheck，不执行 install.sh：

```sh
cd /Users/xjetry/work/vibe/probe-install && shellcheck -s sh deploy/install.sh deploy/openrc/probe-agent > /tmp/p2-shellcheck.log 2>&1; echo $?
```

Expected: 0。注入：把脚本里某处 `"$2"` 改成 `$2`（SC2086）。建基准；`git diff --quiet; echo $?` 期望 1；复跑 shellcheck 期望非 0 且输出含 SC2086；还原复跑期望 0。

- [ ] **Step 10: 提交**

```bash
cd /Users/xjetry/work/vibe/probe-install && git add deploy/install.sh && git commit -m "deploy: 安装脚本，按工具探测分支，建用户与下载先失败于注册之前"
```

---

### Task 3: shellcheck 进 lint 与 CI

**Files:**
- Modify: `Makefile`
- Modify: `.github/workflows/ci.yml`

**Interfaces:**
- Consumes：`deploy/install.sh`、`deploy/openrc/probe-agent`（Task 1/2 产物）。
- Produces：`make lint` 含 shellcheck。CI 不安装 shellcheck：ubuntu 运行器镜像自带；workflow 只跑 `shellcheck --version`，镜像若哪天不带它，CI 在这一步明确失败。

前置：本机已装 shellcheck。

- [ ] **Step 1: Makefile 与 CI 修改**

Makefile 的 `lint` 目标追加一行：

```make
lint:
	go mod tidy -diff
	buf lint
	shellcheck -s sh deploy/install.sh deploy/openrc/probe-agent
	go vet ./...
	GOOS=linux go vet ./...
	GOOS=darwin go vet ./...
```

`.github/workflows/ci.yml` 在 `bufbuild/buf-action` 步骤后、`make ci` 前加：

```yaml
      - run: shellcheck --version
```

不要 `sudo apt-get install -y shellcheck`：没有 `apt-get update` 时可能失败，且镜像自带。

- [ ] **Step 2: 验证与注入**

```sh
cd /Users/xjetry/work/vibe/probe-install && make lint > /tmp/p3-lint.log 2>&1; echo $?
```

Expected: 0。注入：给 install.sh 引入一处 SC2086。`git add deploy/install.sh Makefile .github/workflows/ci.yml` 建基准；`git diff --quiet; echo $?` 期望 1；复跑 `make lint` 期望非 0 且日志含 SC2086；`git checkout -- deploy/install.sh` 还原（Makefile 与 ci.yml 保留），复跑期望 0。

- [ ] **Step 3: 提交**

```bash
cd /Users/xjetry/work/vibe/probe-install && git add Makefile .github/workflows/ci.yml && git commit -m "ci: install.sh 与 OpenRC 脚本过 POSIX shellcheck"
```

---

### Task 4: make release（产物矩阵、版本注入、静态门禁、打包）

**Files:**
- Modify: `Makefile`
- Modify: `.gitignore`（加 `dist/`）

**Interfaces:**
- Consumes：`deploy/install.sh`、`deploy/systemd/probe-agent.service`、`deploy/openrc/probe-agent`（Task 1/2）；`scripts/checkstatic`（既有，`go run ./scripts/checkstatic <file>...`）。
- Produces：`make release VERSION=vX.Y.Z` → `dist/`：`probe-agent_linux_<arch>.tar.gz`（arch ∈ amd64 arm64 armv7 386 riscv64；包内 `probe-agent`、`probe-agent.service`、`probe-agent.openrc`）、`probe-hub_linux_<arch>.tar.gz`（amd64 arm64；包内 `probe-hub`）、`SHA256SUMS`、`install.sh`。`SHA256SUMS` 每行是 `^[0-9a-f]{64}  probe-.*\.tar\.gz$`（两个空格，没有二进制模式的 `*` 前缀）——install.sh 用 `awk '$2 == p'` 取行，格式漂移会让校验静默取不到行。`.github/workflows/release.yml`（Task 5）与 `scripts/install-accept.sh`（Task 6）调用同一目标。

- [ ] **Step 1: Makefile 修改**

`.PHONY` 行补 `release`；追加：

```make
# 发布产物矩阵：agent 五个 Linux 架构，hub 两个。架构集合只在这两个变量维护，
# 静态门禁与打包清单都由它们展开，不存在第二份文件清单。
AGENT_LINUX_ARCHES := amd64 arm64 armv7 386 riscv64
HUB_LINUX_ARCHES := amd64 arm64

# 本地验收与线上发布走同一目标，产物与版本注入完全一致（release.yml 只调用它）。
# macOS 的 bsdtar 会把扩展属性打成 ._* 条目，busybox 解包会带出多余文件；
# 打包的 tar 前设 COPYFILE_DISABLE=1。这行是 makefile 注释：写进 recipe 会被 make 吃掉。
release: web
	@if [ -z "$(VERSION)" ]; then echo "VERSION is required, e.g. make release VERSION=v0.1.0" >&2; exit 1; fi
	rm -rf dist/build dist/*.tar.gz dist/SHA256SUMS dist/install.sh
	mkdir -p dist/build
	@set -e; for arch in $(AGENT_LINUX_ARCHES); do \
	  case $$arch in armv7) gflags="GOARCH=arm GOARM=7" ;; *) gflags="GOARCH=$$arch" ;; esac; \
	  env GOOS=linux CGO_ENABLED=0 $$gflags go build -ldflags "-X main.version=$(VERSION)" -o "dist/build/probe-agent-linux-$$arch" ./cmd/agent; \
	done; \
	for arch in $(HUB_LINUX_ARCHES); do \
	  env GOOS=linux GOARCH=$$arch CGO_ENABLED=0 go build -ldflags "-X main.version=$(VERSION)" -o "dist/build/probe-hub-linux-$$arch" ./cmd/hub; \
	done
	go run ./scripts/checkstatic $(addprefix dist/build/probe-agent-linux-,$(AGENT_LINUX_ARCHES)) $(addprefix dist/build/probe-hub-linux-,$(HUB_LINUX_ARCHES))
	@set -e; for arch in $(AGENT_LINUX_ARCHES); do \
	  pkg="dist/pkg-$$arch"; mkdir -p "$$pkg"; \
	  cp "dist/build/probe-agent-linux-$$arch" "$$pkg/probe-agent"; \
	  cp deploy/systemd/probe-agent.service "$$pkg/probe-agent.service"; \
	  cp deploy/openrc/probe-agent "$$pkg/probe-agent.openrc"; \
	  COPYFILE_DISABLE=1 tar -C "$$pkg" -czf "dist/probe-agent_linux_$$arch.tar.gz" probe-agent probe-agent.service probe-agent.openrc; \
	  rm -rf "$$pkg"; \
	done; \
	for arch in $(HUB_LINUX_ARCHES); do \
	  pkg="dist/pkg-hub-$$arch"; mkdir -p "$$pkg"; \
	  cp "dist/build/probe-hub-linux-$$arch" "$$pkg/probe-hub"; \
	  COPYFILE_DISABLE=1 tar -C "$$pkg" -czf "dist/probe-hub_linux_$$arch.tar.gz" probe-hub; \
	  rm -rf "$$pkg"; \
	done; \
	rm -rf dist/build
	cp deploy/install.sh dist/install.sh
	cd dist && sha256sum probe-*.tar.gz > SHA256SUMS
```

`.gitignore` 追加一行 `dist/`。`COPYFILE_DISABLE=1` 必须出现在两条 `tar` 命令前。

- [ ] **Step 2: 无 VERSION 的失败路径**

```sh
cd /Users/xjetry/work/vibe/probe-install && make release > /tmp/p4-nover.log 2>&1; echo $?
```

Expected: 非 0 且日志含 `VERSION is required, e.g. make release VERSION=v0.1.0`。

- [ ] **Step 3: 产出与校验**

```sh
cd /Users/xjetry/work/vibe/probe-install && make release VERSION=v0.0.0-rc1 > /tmp/p4-release.log 2>&1; echo $?
cd /Users/xjetry/work/vibe/probe-install && ls dist > /tmp/p4-dist.log 2>&1; echo $?
cd /Users/xjetry/work/vibe/probe-install/dist && sha256sum -c SHA256SUMS > /tmp/p4-sums.log 2>&1; echo $?
cd /Users/xjetry/work/vibe/probe-install && tar -tzf dist/probe-agent_linux_arm64.tar.gz > /tmp/p4-tar.log 2>&1; echo $?
cd /Users/xjetry/work/vibe/probe-install && grep -Evc '^[0-9a-f]{64}  probe-.*\.tar\.gz$' dist/SHA256SUMS > /tmp/p4-sums-fmt.log 2>&1; echo $?
```

Expected: 前四条 0。`/tmp/p4-dist.log` 含 5 个 `probe-agent_linux_*.tar.gz`、2 个 `probe-hub_linux_*.tar.gz`、`SHA256SUMS`、`install.sh`；`/tmp/p4-sums.log` 每行 `: OK`；`/tmp/p4-tar.log` 恰为 `probe-agent`、`probe-agent.service`、`probe-agent.openrc` 三项（没有 `._*`——这就是 `COPYFILE_DISABLE=1` 的检查）。第五条的日志内容为 `0`（不匹配的行数）。grep 在没有不匹配行时退出码为 1，这里以计数为准，不把该退出码当失败。

- [ ] **Step 4: 静态门禁接线的缺陷注入**

注入：在 Makefile 的 hub 构建循环行后加一行 `echo not-a-binary > dist/build/probe-hub-linux-amd64`（模拟"产物没被检查到"的缺陷形态）。`git add Makefile .gitignore` 建基准；`git diff --quiet; echo $?` 期望 1；复跑 `make release VERSION=v0.0.0-rc2 > /tmp/p4-inject.log 2>&1; echo $?` 期望非 0 且日志含 `dist/build/probe-hub-linux-amd64: not an ELF file`——证明 7 个产物无一漏检（若检查清单是手抄的旧两份，这条红不会触发，那正是 spec 禁止的形态）；`git checkout -- Makefile` 还原，复跑 `make release VERSION=v0.0.0-rc3` 期望 0。

- [ ] **Step 5: 提交**

```bash
cd /Users/xjetry/work/vibe/probe-install && git add Makefile .gitignore && git commit -m "build: make release 产出全矩阵 tar 包与校验和，版本经 ldflags 注入"
```

---

### Task 5: release workflow（推 tag 自动发布）

**Files:**
- Create: `.github/workflows/release.yml`

**Interfaces:**
- Consumes：`make ci`、`make release VERSION=`（Task 4）。
- Produces：推 `v*` tag 时，先 `make ci`（没过测试的 tag 不出 release），再 `make release`，用运行器自带的 `gh` 创建 GitHub Release，资产为 `dist/*`（资产名不带版本号，`releases/latest/download/<名>` 与 `releases/download/<tag>/<名>` 都能拼出）。不用第三方 action 上传：带 `contents: write` 的第三方代码是供应链面。

- [ ] **Step 1: 写 workflow**

`.github/workflows/release.yml`：

```yaml
name: release
on:
  push:
    tags: ["v*"]
permissions:
  contents: write
jobs:
  release:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
      - uses: actions/setup-go@v5
        with:
          go-version-file: go.mod
      - uses: pnpm/action-setup@v4
        with:
          version: 12
      - uses: actions/setup-node@v4
        with:
          node-version: 26
          cache: pnpm
          cache-dependency-path: web/pnpm-lock.yaml
      - uses: bufbuild/buf-action@v1
        with:
          setup_only: true
      - run: make ci
      # 与本地验收同一构建入口；版本注入与静态门禁都在 make release 里。
      - run: make release VERSION="$GITHUB_REF_NAME"
      - run: gh release create "$GITHUB_REF_NAME" dist/* --verify-tag --title "$GITHUB_REF_NAME"
        env:
          GH_TOKEN: ${{ github.token }}
```

- [ ] **Step 2: actionlint**

```sh
cd /Users/xjetry/work/vibe/probe-install && go run github.com/rhysd/actionlint/cmd/actionlint@v1.7.7 .github/workflows/release.yml .github/workflows/ci.yml > /tmp/p5-actionlint.log 2>&1; echo $?
```

Expected: 0。注入：在 `make ci` 步骤下加一处未定义的步骤键 `not_a_step_key: true`。`git add .github/workflows/release.yml` 建基准；`git diff --quiet; echo $?` 期望 1；复跑 actionlint 期望非 0；`git checkout -- .github/workflows/release.yml` 还原，复跑期望 0。

workflow 的核心命令与 Task 4 实测过的 `make release VERSION=…` 是同一调用；Actions 运行时需要仓库公开（spec §14）。`https://github.com/xjetry/probe` 尚未创建，workflow 的首次真实运行由控制端推 tag 验收——本计划不创建仓库、不推送。

- [ ] **Step 3: 提交**

```bash
cd /Users/xjetry/work/vibe/probe-install && git add .github/workflows/release.yml && git commit -m "ci: 推 v* tag 经 make release 建 GitHub Release"
```

---

### Task 6: scripts/install-accept.sh（真机安装验收）

**Files:**
- Create: `scripts/install-accept.sh`

**Interfaces:**
- Consumes：`make release VERSION=`（Task 4，打两次：版本 A 与版本 B）；`bin/probe-hub` 的 `window open`/`serve`/`passwd` 与 AdminService（同 scripts/e2e.sh 的用法）；OrbStack CLI。镜像写法以 Step 1 实测为准，不写死。
- Produces：`scripts/install-accept.sh [--tier2]`：一级矩阵 Debian 12、Alpine 3.21 × amd64、arm64 每次改动安装脚本或服务定义时跑；`--tier2` 加 Ubuntu 24.04、Rocky 9（发版前）。不进 CI。每格首次安装是 `curl -fsSL <url> | sh -s -- …`（无 curl 时 `wget -qO-`），与面板命令逐字同形；重跑与卸载用下载到机器上的文件。hub 18085、HTTP 18086。

前置条件（宿主）：`orb`、`curl`、`jq`、`python3`；`make release` 与 `make binaries` 由 harness 自己做。需要 root 的机器命令用 `orb -m <机器> -u root`，不混用 sudo。

- [ ] **Step 1: 先行实验——镜像写法、下载器、宿主回环地址**

不凭记忆写镜像名。先看帮助，再实际创建确认。候选（不是结论）：`debian:bookworm`、`debian:12`、`alpine:3.21`、`ubuntu:noble`、`ubuntu:24.04`、`rocky:9`、`rockylinux:9`。四个发行版各用帮助所文档化的写法创建一台 `pia-` 机器；创建失败就换候选再试。成功的那条写入实现记录，并成为 harness 的 `IMG_*`。同时在每台机器上记录 `command -v curl` 与 `command -v wget` 的结果（Alpine 若无 curl，首次安装用 `wget -qO-`，以这次实测为准）。

实验依据记录过 `host.orb.internal` 可达宿主 127.0.0.1，但是一次性观察。用上面创建成功的 Alpine 机器做可达性实验，结果写进验收日志，不把那次观察当结论：

```sh
cd /Users/xjetry/work/vibe/probe-install && orb create --help > /tmp/pia-create-help.log 2>&1; echo $?
cd /tmp && python3 -m http.server 18086 --bind 127.0.0.1 > /tmp/pia-netcheck-httpd.log 2>&1 & echo $! > /tmp/pia-netcheck-httpd.pid
orb -m pia-netcheck wget -q -O - http://host.orb.internal:18086/ > /tmp/pia-netcheck.log 2>&1; echo $?
kill "$(cat /tmp/pia-netcheck-httpd.pid)"; orb delete -f pia-netcheck > /dev/null 2>&1; echo $?
```

`pia-netcheck` 用本步实测成功的 Alpine 镜像创建（上面第三条假定已创建且名为 pia-netcheck；实现时把创建插在 HTTP 服务起来之后、wget 之前）。wget 不需要 root，这条不用 `-u root`。Expected: help 与 wget、delete 均为 0，`/tmp/pia-netcheck.log` 为 python http.server 的目录列表 HTML。若 wget 非 0：fallback 是在机器里 `ip route | awk '/default/ {print $3}'` 取桥接地址，hub 与 HTTP 服务改绑该地址，harness 的 `BIND` 变量承载两种形态。四个发行版的镜像字符串与 curl/wget 结论写入本任务的实现记录。实验完 `orb list` 无 `pia-` 残留。

- [ ] **Step 2: 写验收脚本**

`IMG_*` 的默认值填 Step 1 实测值后再跑（脚本里不得留下 `<…>` 占位；提交前 `grep -n '<Step 1' scripts/install-accept.sh` 必须无输出）。计划不预设镜像代号。

`scripts/install-accept.sh`：

```sh
#!/bin/sh
# install.sh 与服务单元的真机验收：只在 OrbStack 真实启动的机器上跑，不进 CI。
# 机器名 pia- 前缀是隔离边界；只删除本 run 创建的机器（逐台登记）。
# 端口 18085/18086 与 e2e 的 18080/18081 错开，两者可同时跑。
set -eu
cd "$(cd "$(dirname "$0")/.." && pwd)"

TIER2=0
for a in "$@"; do case "$a" in --tier2) TIER2=1;; *) echo "usage: install-accept.sh [--tier2]" >&2; exit 2;; esac; done

# OrbStack 的镜像写法以 Step 1 实测为准：实现时把 <…> 换成实测值，作为默认值写死在脚本里，
# 运行时仍可用同名环境变量覆盖。二级镜像只在 --tier2 时使用。
IMG_DEBIAN=${IMG_DEBIAN:-<Step 1 实测的 Debian 12 写法>}
IMG_ALPINE=${IMG_ALPINE:-<Step 1 实测的 Alpine 3.21 写法>}
IMG_UBUNTU=${IMG_UBUNTU:-<Step 1 实测的 Ubuntu 24.04 写法>}
IMG_ROCKY=${IMG_ROCKY:-<Step 1 实测的 Rocky 9 写法>}

VERSION_A=${VERSION_A:-v0.0.0-accept-a}
VERSION_B=${VERSION_B:-v0.0.0-accept-b}
HUB_PORT=18085
DIST_PORT=18086
HOST=host.orb.internal
work=$(mktemp -d)
: > "$work/machines"
hub=""; httpd=""
admin_pw="accept admin password 2026"

cleanup() {
  if [ -n "$httpd" ]; then kill "$httpd" 2>/dev/null || true; wait "$httpd" 2>/dev/null || true; fi
  if [ -n "$hub" ]; then kill "$hub" 2>/dev/null || true; wait "$hub" 2>/dev/null || true; fi
  while read -r m; do orb delete -f "$m" > /dev/null 2>&1 || true; done < "$work/machines"
}
trap cleanup EXIT

# 两个版本各打一包再复制走：第二次 make release 会清空 dist/，重跑必须能证出版本从 A 变成 B。
make release VERSION="$VERSION_A" > "$work/release-a.log" 2>&1 || { echo "FAIL: make release A"; tail -20 "$work/release-a.log"; exit 1; }
mkdir -p "$work/dist/a"
cp dist/probe-*.tar.gz dist/SHA256SUMS dist/install.sh "$work/dist/a/"
make release VERSION="$VERSION_B" > "$work/release-b.log" 2>&1 || { echo "FAIL: make release B"; tail -20 "$work/release-b.log"; exit 1; }
mkdir -p "$work/dist/b"
cp dist/probe-*.tar.gz dist/SHA256SUMS dist/install.sh "$work/dist/b/"
make binaries > "$work/binaries.log" 2>&1 || { echo "FAIL: make binaries"; tail -20 "$work/binaries.log"; exit 1; }

# 一级 4 格 × 每格 2 节点（服务 + root 对照），tier2 再加 4 格。
max_nodes=8
[ "$TIER2" = 1 ] && max_nodes=16
bin/probe-hub window open --db "$work/accept.db" --ttl 90m --max "$max_nodes" > "$work/window.txt" 2>&1
key=$(sed -n 's/^key: //p' "$work/window.txt")
[ -n "$key" ] || { echo "FAIL: no window key"; exit 1; }

PROBE_OFFLINE_AFTER=3m bin/probe-hub serve --db "$work/accept.db" --listen "127.0.0.1:$HUB_PORT" --timezone UTC > "$work/hub.log" 2>&1 &
hub=$!
attempt=0
while [ "$attempt" -lt 50 ]; do
  [ "$(curl -s -o /dev/null -w '%{http_code}' "http://127.0.0.1:$HUB_PORT/")" = 302 ] && break
  attempt=$((attempt + 1)); sleep 0.2
done
printf '%s\n' "$admin_pw" | bin/probe-hub passwd --db "$work/accept.db" > "$work/passwd.log" 2>&1

# HTTP 以 $work/dist 为根，/a 与 /b 是两个版本目录。
(cd "$work/dist" && python3 -m http.server "$DIST_PORT" --bind 127.0.0.1) > "$work/httpd.log" 2>&1 &
httpd=$!

: > "$work/jar"
base="http://127.0.0.1:$HUB_PORT"
rpc() {
  name=$1; body=$2
  curl -sS -o "$work/$name.json" -w '%{http_code}' -H 'Content-Type: application/json' \
    -b "$work/jar" -c "$work/jar" --data "$body" "$base/probe.v1.AdminService/$name"
}
[ "$(rpc Login "$(jq -nc --arg password "$admin_pw" '{password: $password}')")" = 200 ] || { echo "FAIL: login"; exit 1; }

node_field() { # node_field <node-name> <jq-expr> — 轮询直到 expr 为真（60 次 × 1s）
  i=0
  until [ "$(rpc GetSnapshot '{}')" = 200 ] && jq -e --arg n "$1" ".nodes[] | select(.name == \$n) | $2" "$work/GetSnapshot.json" > /dev/null 2>&1; do
    i=$((i + 1)); [ "$i" -lt 60 ] || return 1
    sleep 1
  done
}
list_nodes() { [ "$(rpc ListNodes '{}')" = 200 ]; }

# 服务进程身份：Uid 第一列不为 0，CapEff 含 bit 13（CAP_NET_RAW = 0x2000）。
# ping_group_range 写进日志：Debian/Alpine 预期关闭，ICMP 可用应来自能力而不是组范围。
# 调用必须在 root 对照进程启动之前，否则 pidof 会看到两个进程。
assert_service_identity() {
  cell=$1
  orb -m "$cell" -u root sh -c '
    pid=$(pidof probe-agent) || { echo "no probe-agent pid"; exit 1; }
    case "$pid" in *" "*) echo "multiple probe-agent pids: $pid"; exit 1;; esac
    uid=$(awk "/^Uid:/ {print \$2; exit}" "/proc/$pid/status")
    cap=$(awk "/^CapEff:/ {print \$2; exit}" "/proc/$pid/status")
    echo "pid=$pid uid=$uid CapEff=$cap ping_group_range=$(cat /proc/sys/net/ipv4/ping_group_range)"
    [ "$uid" != 0 ] || exit 1
    [ $((0x$cap & 0x2000)) -ne 0 ] || exit 1
  ' > "$work/ident-$cell.log" 2>&1 || { echo "FAIL($cell): service identity"; cat "$work/ident-$cell.log"; exit 1; }
  cat "$work/ident-$cell.log"
}

run_cell() {
  img=$1; distro=$2; arch=$3
  name="pia-$distro-$arch"
  echo "== cell $name =="
  orb create -a "$arch" "$img" "$name" > "$work/create-$name.log" 2>&1 || { echo "FAIL($name): orb create"; exit 1; }
  echo "$name" >> "$work/machines"

  # 首次安装用面板命令的管道形态。无 curl 时用 wget -qO-（Step 1 实测；运行时再探一次，不把探测结果写死）。
  if orb -m "$name" -u root command -v curl >/dev/null 2>&1; then
    fetch="curl -fsSL http://$HOST:$DIST_PORT/a/install.sh"
  else
    fetch="wget -qO- http://$HOST:$DIST_PORT/a/install.sh"
  fi
  orb -m "$name" -u root sh -c "$fetch | sh -s -- --hub http://$HOST:$HUB_PORT --key $key --base-url http://$HOST:$DIST_PORT/a" \
    > "$work/install-$name.log" 2>&1 || { echo "FAIL($name): install"; tail -20 "$work/install-$name.log"; exit 1; }

  node_field "$name" '.online == true and .metrics.cpuPct != null' || { echo "FAIL($name): node did not come online"; exit 1; }
  assert_service_identity "$name"

  list_nodes || { echo "FAIL($name): ListNodes"; exit 1; }
  jq -e --arg n "$name" --arg v "$VERSION_A" '.nodes[] | select(.name == $n) | .facts.icmpAvailable == true and .facts.agentVersion == $v' "$work/ListNodes.json" > /dev/null \
    || { echo "FAIL($name): icmpAvailable or agentVersion A"; jq -r --arg n "$name" '.nodes[] | select(.name == $n) | .facts' "$work/ListNodes.json"; exit 1; }

  # ICMP 任务下发并等到有结果：能力由 init 授予，非 root 服务必须有可用 ICMP。
  node_id=$(jq -r --arg n "$name" '.nodes[] | select(.name == $n) | .id' "$work/ListNodes.json")
  [ "$(rpc SaveProbeTask "$(jq -nc --arg id "$node_id" '{task: {kind: "PROBE_KIND_ICMP", target: "127.0.0.1", intervalS: 5, timeoutMs: 1000}, nodeIds: [$id]}')")" = 200 ] \
    || { echo "FAIL($name): SaveProbeTask"; cat "$work/SaveProbeTask.json"; exit 1; }
  task_id=$(jq -r '.task.task.id' "$work/SaveProbeTask.json")
  now=$(date +%s)
  qbody=$(jq -nc --arg id "$node_id" --argjson from "$((now - 600))" --argjson to "$((now + 120))" '{nodeId: $id, from: $from, to: $to, maxPoints: 20}')
  i=0
  until [ "$(rpc QueryProbes "$qbody")" = 200 ] && jq -e --arg t "$task_id" 'any(.series[]; .taskId == $t and any(.samples[]; .sent > 0 and (.errors // 0) == 0))' "$work/QueryProbes.json" > /dev/null; do
    i=$((i + 1)); [ "$i" -lt 45 ] || { echo "FAIL($name): no ICMP results"; cat "$work/QueryProbes.json"; exit 1; }
    sleep 2
  done

  # 加固不得让采集缩水。网卡只以合计计数器上报，逐网卡集合经接口观测不到，不作断言（§12）。
  # 对照进程在身份断言之后才启动。
  orb -m "$name" -u root /usr/local/bin/probe-agent register --hub "http://$HOST:$HUB_PORT" --key "$key" \
    --config /root/root-agent.json --name "$name-root" > "$work/regroot-$name.log" 2>&1 || { echo "FAIL($name): root register"; exit 1; }
  orb -m "$name" -u root timeout 35 /usr/local/bin/probe-agent run --config /root/root-agent.json > "$work/runroot-$name.log" 2>&1 &
  rootrun=$!
  node_field "$name-root" '.online == true and .metrics.cpuPct != null' || { echo "FAIL($name): root node did not come online"; exit 1; }
  jq -e --arg a "$name" --arg b "$name-root" '
    ([.nodes[] | select(.name == $a) | .metrics][0]) as $ma |
    ([.nodes[] | select(.name == $b) | .metrics][0]) as $mb |
    ($ma | keys | sort) == ($mb | keys | sort) and
    $ma.diskTotal == $mb.diskTotal and $ma.memTotal == $mb.memTotal and $ma.bootId == $mb.bootId' \
    "$work/GetSnapshot.json" > /dev/null || { echo "FAIL($name): service/root metrics mismatch"; cat "$work/GetSnapshot.json"; exit 1; }
  wait "$rootrun" || true

  # 重跑即升级：文件形态，base-url 指向版本 B。沿用注册；版本必须变成 B，节点数不变。
  list_nodes || { echo "FAIL($name): ListNodes before rerun"; exit 1; }
  before=$(jq '[.nodes[] | select(.name | startswith("'"$name"'"))] | length' "$work/ListNodes.json")
  orb -m "$name" -u root sh -c "curl -fsSL -o /root/install.sh http://$HOST:$DIST_PORT/b/install.sh || wget -q -O /root/install.sh http://$HOST:$DIST_PORT/b/install.sh" \
    > "$work/fetchb-$name.log" 2>&1 || { echo "FAIL($name): fetch rerun install.sh"; exit 1; }
  orb -m "$name" -u root sh /root/install.sh --hub "http://$HOST:$HUB_PORT" --key "$key" --base-url "http://$HOST:$DIST_PORT/b" \
    > "$work/rerun-$name.log" 2>&1 || { echo "FAIL($name): rerun"; tail -20 "$work/rerun-$name.log"; exit 1; }
  grep -q 'keeping the current registration' "$work/rerun-$name.log" || { echo "FAIL($name): rerun did not keep registration"; exit 1; }
  list_nodes || { echo "FAIL($name): ListNodes after rerun"; exit 1; }
  after=$(jq '[.nodes[] | select(.name | startswith("'"$name"'"))] | length' "$work/ListNodes.json")
  [ "$before" = "$after" ] || { echo "FAIL($name): node count changed on rerun ($before -> $after)"; exit 1; }
  jq -e --arg n "$name" --arg v "$VERSION_B" '.nodes[] | select(.name == $n) | .facts.agentVersion == $v' "$work/ListNodes.json" > /dev/null \
    || { echo "FAIL($name): agentVersion after rerun is not B"; exit 1; }

  case "$distro" in
    debian|ubuntu|rocky)
      orb -m "$name" -u root systemd-analyze verify /etc/systemd/system/probe-agent.service > "$work/verify-$name.log" 2>&1 \
        || { echo "FAIL($name): systemd-analyze verify"; cat "$work/verify-$name.log"; exit 1; };;
    alpine)
      # 删日志目录后重启仍须健康：start_pre 每次启动都建，不靠安装时建一次。
      orb -m "$name" -u root rm -rf /var/log/probe-agent
      orb -m "$name" -u root rc-service probe-agent restart > "$work/logrestart-$name.log" 2>&1 \
        || { echo "FAIL($name): restart after log dir removed"; cat "$work/logrestart-$name.log"; exit 1; }
      node_field "$name" '.online == true and .metrics.cpuPct != null' || { echo "FAIL($name): not healthy after log restart"; exit 1; }
      # 重启机器后自启。last_seen_at 在 JSON 里是 lastSeenAt，int64 编码为字符串（§5.6）。
      # 同时验证 depend() 的 after net 在真机开机顺序下能自启。
      seen=$(jq -r --arg n "$name" '.nodes[] | select(.name == $n) | .lastSeenAt' "$work/GetSnapshot.json")
      orb stop "$name"
      orb start "$name"
      i=0
      until [ "$(rpc GetSnapshot '{}')" = 200 ] && jq -e --arg n "$name" --arg s "$seen" '.nodes[] | select(.name == $n) | .online == true and (.lastSeenAt | tonumber) > ($s | tonumber)' "$work/GetSnapshot.json" > /dev/null 2>&1; do
        i=$((i + 1)); [ "$i" -lt 120 ] || { echo "FAIL($name): did not come back after reboot"; exit 1; }
        sleep 1
      done
      assert_service_identity "$name";;
  esac

  orb -m "$name" -u root sh /root/install.sh --uninstall --purge > "$work/uninstall-$name.log" 2>&1 || { echo "FAIL($name): uninstall"; tail -20 "$work/uninstall-$name.log"; exit 1; }
  case "$distro" in
    debian|ubuntu|rocky)
      orb -m "$name" -u root sh -c 'test ! -f /etc/systemd/system/probe-agent.service && ! systemctl is-enabled probe-agent >/dev/null 2>&1' \
        || { echo "FAIL($name): systemd service still present after uninstall"; exit 1; };;
    alpine)
      orb -m "$name" -u root sh -c 'test ! -f /etc/init.d/probe-agent && ! rc-update show default 2>/dev/null | grep -q probe-agent' \
        || { echo "FAIL($name): openrc service still present after uninstall"; exit 1; };;
  esac
  orb -m "$name" -u root sh -c 'test -z "$(pidof probe-agent)" && test ! -e /usr/local/bin/probe-agent && ! id probe-agent >/dev/null 2>&1 && ! grep -q "^probe-agent:" /etc/group && test ! -e /etc/probe-agent' \
    || { echo "FAIL($name): purge left process, binary, user, group, or config"; exit 1; }

  orb delete -f "$name" > /dev/null 2>&1
  # grep 把名单滤成空时退出码为 1；set -eu 会把"只剩这一台"当成失败。|| true 只消化这个退出码。
  # 名单里只剩本格时 grep 无保留行、退出码为 1，属正常；只放过 1，读不到文件等错误（2）照常失败。
  grep -v "^$name\$" "$work/machines" > "$work/machines.tmp" || [ "$?" = 1 ]
  mv "$work/machines.tmp" "$work/machines"
  echo "== cell $name OK =="
}

run_cell "$IMG_DEBIAN" debian amd64
run_cell "$IMG_DEBIAN" debian arm64
run_cell "$IMG_ALPINE" alpine amd64
run_cell "$IMG_ALPINE" alpine arm64
if [ "$TIER2" = 1 ]; then
  run_cell "$IMG_UBUNTU" ubuntu amd64
  run_cell "$IMG_UBUNTU" ubuntu arm64
  run_cell "$IMG_ROCKY" rocky amd64
  run_cell "$IMG_ROCKY" rocky arm64
fi
echo "INSTALL ACCEPT OK"
```

节点名沿用机器名（OrbStack 把机器名设为 hostname，register 默认用 hostname）。不另加 `--name`，以免偏离面板命令的参数形态；若 Step 1 的机器 hostname 不是机器名，在实现记录里改 harness 传 `--name "$name"`，并说明与面板命令的这一处差异。

- [ ] **Step 3: 跑一级矩阵**

```sh
cd /Users/xjetry/work/vibe/probe-install && scripts/install-accept.sh > /tmp/p6-accept.log 2>&1; echo $?
```

Expected: 0，日志含 4 行 `== cell pia-<distro>-<arch> OK ==`、每格的 `ping_group_range=` 行，与末尾 `INSTALL ACCEPT OK`；`orb list` 中无 `pia-` 机器残留。跑之前先 `docker info > /tmp/p6-docker.log 2>&1; echo $?` 确认 Docker 可用（本 harness 不用 docker，但 OrbStack 守护进程共享状态）；任一格失败时按日志定位，机器已登记删除。

- [ ] **Step 4: 跑二级矩阵（发版前形态）**

```sh
cd /Users/xjetry/work/vibe/probe-install && scripts/install-accept.sh --tier2 > /tmp/p6-accept-tier2.log 2>&1; echo $?
```

Expected: 0，8 格全 OK。

- [ ] **Step 5: OpenRC start_pre 的缺陷注入（Review Focus 3）**

注入：删掉 `deploy/openrc/probe-agent` 的 `start_pre` 函数。`git add deploy/openrc/probe-agent scripts/install-accept.sh` 建基准；`git diff --quiet; echo $?` 期望 1。Step 3 的 harness 退出时 trap 已回收 hub 与 HTTP 服务。本步按 Step 2 的对应命令重新起 hub（`window open` 取新 key）与 dist HTTP 服务（单版本 `make release`，根目录即 `dist/`，端口仍是 18086/18085）。镜像用 Step 1 实测的 Alpine 写法。安装用管道形态，需要 root 的命令用 `-u root`：

```sh
cd /Users/xjetry/work/vibe/probe-install && make release VERSION=v0.0.0-inject > /dev/null 2>&1
orb create "$IMG_ALPINE" pia-inject > /dev/null 2>&1
orb -m pia-inject -u root sh -c "curl -fsSL http://host.orb.internal:18086/install.sh | sh -s -- --hub http://host.orb.internal:18085 --key $key --base-url http://host.orb.internal:18086" > /tmp/p6-inject-install.log 2>&1; echo $?
orb -m pia-inject -u root rm -rf /var/log/probe-agent
orb -m pia-inject -u root rc-service probe-agent restart > /tmp/p6-inject-restart.log 2>&1; echo $?
orb -m pia-inject -u root sh -c 'rc-service probe-agent status; pidof probe-agent' > /tmp/p6-inject-status.log 2>&1; echo $?
```

Alpine 若无 curl，管道左侧改成 `wget -qO-`，与 Step 1 实测一致。Expected: restart 显示 ok 而 `pidof probe-agent` 无输出（status 显示 started 但进程不在，§14 所述的失效形态）——红在正确原因。`git checkout -- deploy/openrc/probe-agent` 还原后同序列复跑，`pidof` 有输出。实验完 `orb delete -f pia-inject`。harness 里对应的长效断言是 Alpine 格"删日志目录后重启仍健康"加上"安装后节点上线 + icmpAvailable + ICMP 结果"。

- [ ] **Step 6: 重复注册的缺陷注入（Review Focus 5）**

注入：把 install.sh 的 `if [ ! -f "$CFG" ]` 注册分支改成无条件注册。`git add deploy/install.sh` 建基准；`git diff --quiet; echo $?` 期望 1。在 Step 5 重建的 hub/HTTP 服务环境里，任选一机（pia-inject 可复用，先 `--uninstall --purge` 再装）以 `-u root` 连跑两次 install（第一次管道形态，第二次文件形态）。hub 侧 `ListNodes` 中该机器名节点数变 2 → harness 的 `node count changed on rerun` 断言即红；本注入以手动序列确认两次 register 都在 hub 日志出现 `node registered`。`git checkout -- deploy/install.sh` 还原，机器删除，hub 与 HTTP 服务停止。

- [ ] **Step 7: 提交**

```bash
cd /Users/xjetry/work/vibe/probe-install && git add scripts/install-accept.sh && git commit -m "test: 真机安装验收矩阵，一级 Debian/Alpine 双架构，二级发版前"
```

---

### Task 7: LXC 未开 nesting 下的 DynamicUser 对照实验

**Files:**
- Create: `.superpowers/drafts/lxc-nesting-experiment.md`（实验报告，控制端据以把结论写进 spec；不进仓库，代码与注释不引用它）

**Interfaces:**
- Consumes：`deploy/systemd/probe-agent.service` 的加固形态（实验用最小替身单元复现其 User=/DynamicUser= 差异，不装 agent）。Debian 镜像写法用 Task 6 Step 1 的实测，不另猜 `debian:12`。
- Produces：实验报告（spec §14 的"单元定稿前在未开 nesting 的 LXC 容器里实测一次"）。

机器上的命令一律 `orb -m pia-lxc -u root`，不混用 sudo。

- [ ] **Step 1: 准备实验机**

```sh
cd /Users/xjetry/work/vibe/probe-install && orb create "$IMG_DEBIAN" pia-lxc > /tmp/p7-create.log 2>&1; echo $?
```

`$IMG_DEBIAN` 是 Task 6 Step 1 实测的 Debian 写法。若本任务单独跑，先按 Task 6 Step 1 的办法只确认 Debian 这一条，不凭记忆。

装 incus 的首选路径是 bookworm-backports。默认 sources 往往没有这个套件；先写入源再执行 `apt-get install -y -t bookworm-backports incus`，仍失败才算这条路径不行，然后 zabbly，再不行 lxc。不要因为源未配置就把首选路径判死。

```sh
orb -m pia-lxc -u root sh -c 'echo "deb http://deb.debian.org/debian bookworm-backports main" > /etc/apt/sources.list.d/backports.list && apt-get update && apt-get install -y -t bookworm-backports incus' > /tmp/p7-incus.log 2>&1; echo $?
```

Expected: 0 则用 incus。非 0：按 incus 官方文档加 zabbly 仓库（`curl -fsSL https://pkgs.zabbly.com/key.asc` 导入 key 后加源再装），仍 `orb -m pia-lxc -u root`。再不行 `orb -m pia-lxc -u root apt-get install -y lxc`。三条都走不通就把该项如实记为未验证（写清卡点：backports 的失败输出、zabbly 的失败输出、lxc 的失败输出），报告照常交付。

- [ ] **Step 2: 未开 nesting 的容器与两个对照单元**

装了 incus 时：

```sh
orb -m pia-lxc -u root incus admin init --auto > /tmp/p7-init.log 2>&1; echo $?
orb -m pia-lxc -u root incus launch images:debian/12 c1 -c security.nesting=false > /tmp/p7-launch.log 2>&1; echo $?
```

容器内造两个单元（ExecStart 用 /bin/sleep，实验对象是 systemd 对 User= 形态的启动判定，不是 agent）：

```sh
orb -m pia-lxc -u root incus exec c1 -- sh -c '
  useradd --system -M -s /usr/sbin/nologin probe-agent
  printf "[Service]\nType=simple\nUser=probe-agent\nGroup=probe-agent\nExecStart=/bin/sleep infinity\n" > /etc/systemd/system/t-static.service
  printf "[Service]\nType=simple\nDynamicUser=yes\nExecStart=/bin/sleep infinity\n" > /etc/systemd/system/t-dynamic.service
  systemctl daemon-reload' > /tmp/p7-units.log 2>&1; echo $?
```

降到 lxc 时，用对应的 lxc 命令创建未开 nesting 的容器并在容器内执行同样的单元文本；报告写明用的是哪条路径。

- [ ] **Step 3: 断言两种形态的实际行为**

```sh
orb -m pia-lxc -u root incus exec c1 -- systemctl start t-static > /tmp/p7-static.log 2>&1; echo $?
orb -m pia-lxc -u root incus exec c1 -- systemctl is-active t-static > /tmp/p7-static-active.log 2>&1; echo $?
orb -m pia-lxc -u root incus exec c1 -- systemctl start t-dynamic > /tmp/p7-dynamic.log 2>&1; echo $?
orb -m pia-lxc -u root incus exec c1 -- sh -c 'systemctl status t-dynamic 2>&1 | grep -o "226/NAMESPACE"' > /tmp/p7-dynamic-code.log 2>&1; echo $?
```

Expected（spec §14 引用的记录若成立）：static 两条 0（active）；dynamic 启动非 0 且 `226/NAMESPACE` 可检出。**若 dynamic 也能启动，说明该记录在此环境不成立，如实写入报告（这正是实验要回答的问题），结论由控制端写进 spec。** 环境：记录容器内 `systemctl --version` 与 nesting 状态入报告。

- [ ] **Step 4: 写报告并清理**

报告 `/Users/xjetry/work/vibe/probe-install/.superpowers/drafts/lxc-nesting-experiment.md`：环境、每条命令与退出码、关键输出片段、结论或未验证原因。清理：`orb -m pia-lxc -u root incus delete -f c1`（装了 incus 时）；`orb delete -f pia-lxc`。`orb list` 确认无 pia- 残留。本任务无仓库文件改动，不提交。

---

### Task 8: 面板安装命令与落后节点标记（hub_version 贯通）

**Files:**
- Modify: `proto/probe/v1/admin.proto`（`GetSnapshotResponse` 加字段）
- Modify: `gen/probe/v1/*.go`、`web/src/gen/**`（`make gen` 产物，不手改）
- Modify: `internal/hub/api/service.go`（`Config` 加 `HubVersion string`）
- Modify: `internal/hub/api/data.go`（`GetSnapshot` 回填）
- Modify: `cmd/hub/serve.go`（装配时传 `version`）
- Modify: `web/src/pages/RegisterWindow.tsx`、`web/src/pages/RegisterWindow.test.tsx`
- Modify: `web/src/pages/Nodes.tsx`、`web/src/pages/Nodes.test.tsx`

**Interfaces:**
- Consumes：hub 的 `var version`（`cmd/hub/main.go`，release 经 ldflags 注入）；`ListNodes` 的 `facts.agentVersion`（既有）；`web/src/api/queryGate.tsx` 的 `queryGate` / `errorBanner`（区域未就绪时 `loading` 为占位或 null，`errors` 交给 `errorBanner`）。
- Produces：`GetSnapshotResponse.hub_version`（string，字段号 4）。面板在注册窗口 key 旁给出 curl 与 wget 两条安装命令，旁注"以 root 运行"。hub 为正式版本（`hubVersion.startsWith("v")`，与 spec §10 的开发构建对照；dev 不以 v 开头）时，install.sh 的地址是 `https://github.com/xjetry/probe/releases/download/${hubVersion}/install.sh` 且命令带 `--version`；非正式版本用 `https://github.com/xjetry/probe/releases/latest/download/install.sh`，不带 `--version`，并提示将安装最新 release。节点列表用版本号比较标出落后于 hub 的节点，不用字符串不等。`getSnapshot` 不另设 `refetchInterval`：hub 版本在进程生命周期内不变。

**前置：本任务须在 agent-access 分支合入 main 后变基再执行**（那条线也改 admin.proto 与生成物；生成物冲突不手解，变基后重跑 `make gen`）。

- [ ] **Step 1: proto 字段与生成**

`GetSnapshotResponse` 加：

```proto
  // hub 构建版本（release 经 ldflags 注入，未注入为 dev）：面板据此生成与 hub
  // 同版本的安装命令，并标出 agent 版本落后的节点。
  string hub_version = 4;
```

```sh
cd /Users/xjetry/work/vibe/probe-install && make gen > /tmp/p8-gen.log 2>&1; echo $?
cd /Users/xjetry/work/vibe/probe-install && buf lint > /tmp/p8-buflint.log 2>&1; echo $?
```

Expected: 均 0。buf breaking（WIRE_JSON）只拦破坏性变更，加字段不触发。

- [ ] **Step 2: Go 侧先红后绿**

`internal/hub/api/api_test.go` 的 `newHarness` 装配 `New(Config{...})` 字面量补 `HubVersion: "test-hub-version"`（唯一构造点，不动签名）；在 `TestSnapshotReflectsLiveState` 旁加用例：

```go
func TestGetSnapshotReportsHubVersion(t *testing.T) {
	h := newHarness(t, "")
	h.login(t)
	resp, err := h.admin.GetSnapshot(context.Background(), connect.NewRequest(&probev1.GetSnapshotRequest{}))
	if err != nil {
		t.Fatalf("GetSnapshot: %v", err)
	}
	if resp.Msg.HubVersion != "test-hub-version" {
		t.Fatalf("want hub_version test-hub-version, got %q", resp.Msg.HubVersion)
	}
}
```

红：`go test -count=1 ./internal/hub/api > /tmp/p8-red.log 2>&1; echo $?` 期望 1，原因 `want hub_version test-hub-version, got ""`（data.go 未回填时）。实现：`Config` 加 `HubVersion string`；`GetSnapshot` 构造响应时 `HubVersion: s.cfg.HubVersion`；`cmd/hub/serve.go` 的 `api.New(api.Config{...})` 字面量补 `HubVersion: version`。绿：复跑 0。注入：删掉 data.go 的回填。`git add internal/hub/api cmd/hub/serve.go proto gen web/src/gen` 建基准；`git diff --quiet; echo $?` 期望 1；`go test -count=1 ./internal/hub/api` 期望红且红在上述原因行；`git checkout -- internal/hub/api` 还原，复跑 0。

- [ ] **Step 3: RegisterWindow 的安装命令**

`useQuery(AdminService.method.getSnapshot, {})` 取 hubVersion，不设 `refetchInterval`。命令区域用 queryGate 的区域写法：页面外壳（key、开窗表单）不依赖 snapshot；snapshot 未就绪时区域只显示 `snap.loading`（加载中）或 `errorBanner(...snap.errors)`，不渲染命令。不能用 `snapshot.data?.hubVersion ?? ""` 在未就绪时拼出 latest 命令再闪变。

既有 `RegisterWindow.test.tsx` 里断言 `probe-agent register` 的用例改为新命令形态，并给 `getSnapshot` 一个成功桩。不桩的话，未实现的方法会在命令区域打出另一条错误，干扰"失败时展示错误正文"对唯一 alert 的断言。

```tsx
const snapshot = useQuery(AdminService.method.getSnapshot, {});
const snap = queryGate(snapshot);
const scriptUrl = (hubVersion: string) =>
  hubVersion.startsWith("v")
    ? `https://github.com/xjetry/probe/releases/download/${hubVersion}/install.sh`
    : "https://github.com/xjetry/probe/releases/latest/download/install.sh";
// key 区块内，只有 snap.ready 才计算 URL 与参数：
// <p>在被监控的机器上以 root 执行（agent 若经其他地址访问 hub，把命令里的地址换掉）：</p>
// {snap.ready ? (
//   <>
//     <pre className="secret">{`curl -fsSL ${scriptUrl(snap.data.hubVersion)} | sh -s -- ${args}`}</pre>
//     <pre className="secret">{`wget -qO- ${scriptUrl(snap.data.hubVersion)} | sh -s -- ${args}`}</pre>
//     {!snap.data.hubVersion.startsWith("v") && <p className="muted">hub 不是正式版本（{snap.data.hubVersion || "未知"}），命令不带 --version，将安装最新 release。</p>}
//   </>
// ) : (
//   snap.loading ?? errorBanner(...snap.errors)
// )}
// args = `--hub ${window.location.origin} --key ${key}`，正式版本再加 ` --version ${hubVersion}`。
```

vitest（扩 `RegisterWindow.test.tsx`，用 `renderWithAdmin`（`web/src/test/harness`）按方法 mock；开窗表单提交后 key 区块出现）：

```tsx
const renderOpen = (hubVersion: string) =>
  renderWithAdmin({
    getRegisterWindow: async () => ({ open: true, expiresAt: 4_000_000_000n, remaining: 3 }),
    openRegisterWindow: async () => ({ key: "k1", expiresAt: 4_000_000_000n, maxNodes: 3 }),
    getSnapshot: async () => ({ now: 1n, reportIntervalMs: 4000, nodes: [], hubVersion }),
  }, [{ path: "/register", Component: RegisterWindow }], "/register");

it("install commands use the hub release URL and --version", async () => {
  renderOpen("v1.2.3");
  fireEvent.click(await screen.findByRole("button", { name: "开启新窗口" }));
  const pres = await screen.findAllByText(/install\.sh \| sh -s --/);
  expect(pres).toHaveLength(2);
  expect(pres[0].textContent).toContain("curl -fsSL");
  expect(pres[1].textContent).toContain("wget -qO-");
  for (const p of pres) {
    expect(p.textContent).toContain("https://github.com/xjetry/probe/releases/download/v1.2.3/install.sh");
    expect(p.textContent).toContain("--version v1.2.3");
    expect(p.textContent).toContain("--key k1");
  }
  expect(screen.getByText(/以 root 执行/)).toBeInTheDocument();
});

it("dev hub uses latest/download and shows the latest-release hint", async () => {
  renderOpen("dev");
  fireEvent.click(await screen.findByRole("button", { name: "开启新窗口" }));
  for (const p of await screen.findAllByText(/install\.sh \| sh -s --/)) {
    expect(p.textContent).toContain("https://github.com/xjetry/probe/releases/latest/download/install.sh");
    expect(p.textContent).not.toContain("--version");
    expect(p.textContent).not.toContain("/download/dev/");
  }
  expect(await screen.findByText(/将安装最新 release/)).toBeInTheDocument();
});

it("does not render install commands while the snapshot is pending", async () => {
  renderWithAdmin({
    getRegisterWindow: async () => ({ open: true, expiresAt: 4_000_000_000n, remaining: 3 }),
    openRegisterWindow: async () => ({ key: "k1", expiresAt: 4_000_000_000n, maxNodes: 3 }),
    getSnapshot: () => new Promise(() => {}),
  }, [{ path: "/register", Component: RegisterWindow }], "/register");
  fireEvent.click(await screen.findByRole("button", { name: "开启新窗口" }));
  expect(await screen.findByText("加载中…")).toBeInTheDocument();
  expect(screen.queryByText(/install\.sh/)).toBeNull();
});

it("shows only the snapshot error when it fails before commands exist", async () => {
  renderWithAdmin({
    getRegisterWindow: async () => ({ open: true, expiresAt: 4_000_000_000n, remaining: 3 }),
    openRegisterWindow: async () => ({ key: "k1", expiresAt: 4_000_000_000n, maxNodes: 3 }),
    getSnapshot: async () => { throw new ConnectError("snapshot unavailable", Code.Unavailable); },
  }, [{ path: "/register", Component: RegisterWindow }], "/register");
  fireEvent.click(await screen.findByRole("button", { name: "开启新窗口" }));
  expect(await screen.findByRole("alert")).toHaveTextContent("snapshot unavailable");
  expect(screen.queryByText(/install\.sh/)).toBeNull();
});
```

红：先写测试，`pnpm --dir web exec vitest run RegisterWindow > /tmp/p8-web-red.log 2>&1; echo $?` 期望非 0（页面还没有这两条命令，挂起时还会渲染旧命令或什么都不显示）。实现后复跑 0。

注入一：把 `scriptUrl` 改成恒为 `releases/latest/download/install.sh`。`git add web/src/pages/RegisterWindow.tsx web/src/pages/RegisterWindow.test.tsx` 建基准；`git diff --quiet; echo $?` 期望 1；正式版本用例红（找不到 `releases/download/v1.2.3/install.sh`）。还原。

注入二：去掉区域门控，未就绪时用 `snapshot.data?.hubVersion ?? ""` 渲染命令。挂起用例红（出现 `install.sh`，或找不到"加载中…"）。还原后复跑 0。

- [ ] **Step 4: 节点列表的落后标记**

`Nodes.tsx` 每行已渲染 `node.facts`（ListNodes 响应）。加 `useQuery(AdminService.method.getSnapshot, {})`，不设 `refetchInterval`。snapshot 是标记用的可选查询：不进页面门控，失败不卸载节点列表，未就绪时不标。落后用版本号比较，不用不等：agent 比 hub 新不是落后。解析 `vMAJOR.MINOR.PATCH`（`^v(\d+)\.(\d+)\.(\d+)(?:-|$)`，忽略 `-` 之后的预发布后缀）；任一方解析失败就不标。

```tsx
function releaseTriple(v: string): [number, number, number] | null {
  const m = /^v(\d+)\.(\d+)\.(\d+)(?:-|$)/.exec(v);
  return m ? [Number(m[1]), Number(m[2]), Number(m[3])] : null;
}
function lagsHub(agent: string | undefined, hub: string): boolean {
  const a = agent ? releaseTriple(agent) : null;
  const h = releaseTriple(hub);
  if (!a || !h) return false;
  for (let i = 0; i < 3; i++) if (a[i] !== h[i]) return a[i] < h[i];
  return false;
}
// JSX 名称单元格：<Link …>{node.name}</Link>{snap.ready && lagsHub(node.facts?.agentVersion, snap.data.hubVersion) && <span className="warn">落后于 hub</span>}
```

vitest（扩 `Nodes.test.tsx`，沿用 `renderWithAdmin` 与文件内 `two` 夹具，给节点补 `facts`）：

```tsx
const withVersion = [
  { ...two[0], facts: { hostname: "a", os: "", kernel: "", arch: "", virtualization: "", cpuModel: "", cpuCores: 0, agentVersion: "v1.0.0", icmpAvailable: true } },
];

it("marks nodes whose agent version lags the hub", async () => {
  renderWithAdmin({
    listNodes: async () => ({ nodes: withVersion }),
    getSnapshot: async () => ({ now: 1n, reportIntervalMs: 4000, nodes: [], hubVersion: "v1.1.0" }),
  }, [{ path: "/nodes", Component: Nodes }], "/nodes");
  expect(await screen.findByText("落后于 hub")).toBeInTheDocument();
});

it.each(["v1.1.0", "dev"])("no lagging marker when versions match or hub is %s", async (hubVersion) => {
  const nodes = [{ ...withVersion[0], facts: { ...withVersion[0].facts, agentVersion: "v1.1.0" } }];
  renderWithAdmin({
    listNodes: async () => ({ nodes }),
    getSnapshot: async () => ({ now: 1n, reportIntervalMs: 4000, nodes: [], hubVersion }),
  }, [{ path: "/nodes", Component: Nodes }], "/nodes");
  await screen.findByRole("link", { name: "a" });
  expect(screen.queryByText("落后于 hub")).toBeNull();
});

it("does not mark a node newer than the hub", async () => {
  const nodes = [{ ...withVersion[0], facts: { ...withVersion[0].facts, agentVersion: "v1.2.0" } }];
  renderWithAdmin({
    listNodes: async () => ({ nodes }),
    getSnapshot: async () => ({ now: 1n, reportIntervalMs: 4000, nodes: [], hubVersion: "v1.1.0" }),
  }, [{ path: "/nodes", Component: Nodes }], "/nodes");
  await screen.findByRole("link", { name: "a" });
  expect(screen.queryByText("落后于 hub")).toBeNull();
});

it("does not mark a dev agent", async () => {
  const nodes = [{ ...withVersion[0], facts: { ...withVersion[0].facts, agentVersion: "dev" } }];
  renderWithAdmin({
    listNodes: async () => ({ nodes }),
    getSnapshot: async () => ({ now: 1n, reportIntervalMs: 4000, nodes: [], hubVersion: "v1.1.0" }),
  }, [{ path: "/nodes", Component: Nodes }], "/nodes");
  await screen.findByRole("link", { name: "a" });
  expect(screen.queryByText("落后于 hub")).toBeNull();
});
```

（facts 字段类型以生成的 `FactsSchema` 为准，`create(FactsSchema, …)` 亦可。）红：先写测试，`pnpm --dir web exec vitest run Nodes > /tmp/p8-nodes-red.log 2>&1; echo $?` 期望非 0。实现后 0。注入：把 `lagsHub` 改成字符串不等（`agent !== hub`，解析失败也标）。建基准；`git diff --quiet; echo $?` 期望 1；"比 hub 新"与"agent 为 dev"两条红（出现"落后于 hub"）。`git checkout -- web/src/pages/Nodes.tsx` 还原，复跑 0。既有 Nodes 用例不桩 `getSnapshot` 也应保持绿：标记查询失败不卸载列表、不额外渲染会撞断言的 alert。

- [ ] **Step 5: 全量检查与提交**

```sh
cd /Users/xjetry/work/vibe/probe-install && make ci > /tmp/p8-ci.log 2>&1; echo $?
```

Expected: 0（含生成物一致性检查）。

```bash
cd /Users/xjetry/work/vibe/probe-install && git add proto internal cmd gen web && git commit -m "panel: 注册窗口给出与 hub 同版本的安装命令，节点列表标出版本落后的节点"
```

---

## 自查记录

1. spec 覆盖：§14 全节 → Task 1（服务定义，`after net`）、Task 2（install.sh 全条款，含管道 stdin、分别判断的 CA、主组回查、purge 回查）、Task 3（shellcheck 进 CI）、Task 4（构建矩阵/静态门禁/资产/版本注入/`COPYFILE_DISABLE`）、Task 5（tag 发布，`make ci` 后 `gh release create`）、Task 6（验收矩阵）、Task 7（LXC 实测）；darwin 构建、macOS launchd、hub Docker 镜像属 M6，本计划不含。§12 安装验收条 → Task 6 全部断言（管道形态、Uid/CapEff、指标对照、版本 A→B、OpenRC 重启自启、`--purge`）+ Task 3（shellcheck 进 CI）。§10 安装命令条 → Task 8（正式版本的脚本 URL 与 `--version` 同判；命令区域在 snapshot 未就绪时不渲染）。§13 第 2 项 → 结论已在 spec，体现在 Task 1 的能力授予与注释（引用 §13 第 2 项、§14，不引用实验草稿）。§4.7 → OpenRC `after net` 的注释。§1 非目标"hub 托管 agent 二进制" → 二进制只从 GitHub Releases 下载，hub 不出 install.sh 也不出二进制（面板命令指向 Releases）。
2. Review Focus 五条落点：1 → Task 6 每格首次安装的管道形态；2 → Task 2 Step 6/7；3 → Task 6 Alpine 格删日志目录后重启 + Step 5 注入；4 → Task 2 Step 8 两个方向；5 → Task 6 重跑断言 + Step 6 注入。无下载器报错在 Task 2 Step 5，不在 Review Focus。

## 执行修正

执行中相对上文的偏离与补充，按主题列出；每条的理由与实测记录在提交信息与 spec §14 里。上文 Task 1、Task 2 的代码块是初稿，现行行为以 `deploy/install.sh`、`deploy/openrc/probe-agent`、`deploy/systemd/probe-agent.service` 为准。

- **停服务**：上文 `[ -f "$CFG" ] && stop_service` 与吞掉错误的 stop 改为：解包、包内三项检查、写临时二进制都在停服务之前做完（准备失败时正在运行的旧服务不受影响）；服务定义已安装才发 stop，stop 失败即失败；之后无条件扫描 `/proc/[0-9]*/status`，按服务用户的有效 uid 确认没有进程在跑，有上限地轮询（0.5 秒 × 20 次），超过上限仍在即报错。stop 的退出码与 status 都不作判据：OpenRC 0.55.1（Alpine 3.21）实测 stop 在所有测过的状态都返回 0，supervise-daemon 被杀而子进程留存时，stop 之后 status 为 stopped、子进程仍在运行。卸载时停不下来即非零退出；单元文件存在时 `systemctl disable` 必须成功。
- **链接判断**：runlevel 链接的 add 与 del 守卫都用 `-L`（上文是 `-e`：init 脚本被删后链接悬空，`-e` 判为不存在而留下它）；systemd 卸载时单元文件已被手删的，同样按 `-L` 删掉 `multi-user.target.wants` 下的悬空链接。
- **属主**：上文"配置目录 0700"与 OpenRC `start_pre` 的 `chown -R` 改为：配置目录 root:probe-agent 0750、配置文件 probe-agent 0600；OpenRC 的日志目录 root:root 0755，只把两个日志文件（probe-agent 0640）交给运行用户，由 `checkpath` 每次启动确认，每条带 `|| return 1`（实测不带时 checkpath 失败被 start_pre 吞掉、服务照常启动）。服务用户能增删目录项时，root 按路径操作的对象可被替换，所以 root 要操作的目录都保持 root 属主。
- **账户**：先建组再以其为主组建用户（Debian 的 perl 版 adduser 用 `--ingroup`，busybox 用 `-G`）；用户已存在而主组不符时报错并写出实际主组，主组丢失时写出 gid。
- **参数与错误**：`--purge` 不带 `--uninstall` 是用法错误（退出 2），不再去安装；SHA256SUMS 里没有本包那一行时报 `SHA256SUMS has no entry for <包名>`；两处 `--help` 探测也显式 `</dev/null`；dash 与 busybox ash 被信号终止时不执行 EXIT trap，所以把 INT、TERM、HUP 转成 `exit 1`，Ctrl-C 或 SSH 断开时也会清掉临时文件。
- **Task 2 Step 7 的环境**：bookworm 上无法按上文删掉 useradd 与 groupadd（perl 版 addgroup、adduser 内部调用它们），改为把 adduser、groupadd 复制进 `/usr/bin` 并设 `PATH=/usr/bin:/bin` 隔离；"只有 perl addgroup、没有 groupadd"的组合在 bookworm 上不存在，addgroup 分支的真实覆盖是 Alpine busybox（Step 6）。
- **打包**：`.gitignore` 用 `/dist/`（不锚定的 `dist/` 会连带匹配 `internal/hub/web/dist/`，破坏那里的 `.gitkeep` 例外）。两处 tar 加 `--no-xattrs`：`COPYFILE_DISABLE=1` 只去掉 `._*` 条目，macOS 的 bsdtar 仍把 `com.apple.provenance` 写成 pax 扩展头，GNU tar 解包时逐条告警；加上后在 debian:bookworm-slim 用 GNU tar 解包没有告警。
- **OrbStack 镜像与连通**（Task 6 Step 1，arm64 实测）：`alpine:3.21`、`debian:12`、`ubuntu:24.04` 可建；Rocky 的写法是 `rocky:9`（`rockylinux:9` 不合法），实测时三次创建都因镜像 CDN 超时失败；`host.orb.internal` 可达宿主的 127.0.0.1，不需要备用地址。
- **LXC 对照实验**（Task 7）：bookworm-backports 的 Incus、容器内 systemd 252、`security.nesting=false` 下，静态 `User=` 与 `DynamicUser=` 都能启动，没有出现 226/NAMESPACE。spec §14 与 systemd 单元的注释不再引用那条未复现的记录，单元注释只写可核实的理由。
- **验收脚本（Task 6）**：
  - 服务进程按服务用户的有效 uid 扫描 `/proc` 认定，断言恰好一个，与 install.sh 停服务后的确认同一判据；上文的 `pidof` 在 BusyBox 1.37.0（Alpine 3.21）上会把它自己的进程也打印出来（把 `pidof` 停住实测：comm 为 `pidof`），不能用来认进程。卸载后的残留检查按 `comm` 精确等于 `probe-agent`。
  - 上文"根分区总量与 root 对照相等"改为：服务进程的 `/proc/<pid>/root/` 与 `/` 设备号相同（加固没有让 agent 统计到另一个根分区）；指标比较保留字段集合、内存总量与 bootId。OrbStack 机器的根是 btrfs，实测同一台机器相隔 5 秒的两次上报总量相差约 2.4 GiB 而已用量相同，数值不可比；同一时刻在带同样加固的 systemd-run 与普通环境里 statfs 结果相同。
  - 每格断言配置目录 root:probe-agent 0750、配置文件 probe-agent 0600；OpenRC 格另断言日志目录 root:root 0755、两个日志文件 probe-agent 0640，删目录重启后再断言一次。
  - hub 用默认 30 秒 TTL（不设 `PROBE_OFFLINE_AFTER=3m`：3 分钟 TTL 把上报间隔抬到 1 分钟，root 对照 35 秒凑不齐两次 CPU 采样）；QueryProbes 取样本写 `.series[]?`；重跑后最多等 60 秒到版本变为 B。
  - HTTP 服务用 `exec` 拉起，开跑前核对应答的是本轮 `mktemp` 目录名写成的 run-id（孤儿 `http.server` 曾占着端口让注入假绿；install.sh 在 deploy/ 未改时各轮逐字相同，比对它区分不了新旧服务）；hub 必须在本进程日志里打出监听行。
  - 新增 `--only DISTRO-ARCH`（可重复，默认行为不变），单格注入与分段运行用；`--only` 时每格多留 1 个窗口名额，无条件注册的注入才能走到节点数断言。
  - 矩阵结果（分支变基到 main 之前跑，install.sh 已含停服务确认与属主修复）：一级四格、二级四格全部通过；Rocky amd64 首次创建报 "machine didn't start in 30s"，立即重试通过。变基到含 API token、测试等待与投递类别的 main 后复验 Debian arm64、Alpine arm64 两格通过。
- **面板安装命令与落后标记（Task 8）**：
  - 上文只测到 api 层回填 `hub_version`；删掉 serve.go 的传值时那条测试仍绿，所以真实 serve 的集成用例也断言快照里的版本。
  - 节点页取不到快照时不再静默：列表照常、不标记，并显示横幅；从未取到时写落后标记不可用，取到过而刷新失败时写按上次取得的版本判断。节点页测试用默认返回成功快照的共享渲染辅助。
  - 落后判定按 semver 2.0 优先级（`web/src/lib/version.ts`）：三段数值、正式版高于预发布、预发布按点分标识符逐段比、构建元数据不参与、任一方不合 semver（含前导 0）不标。上文的正则忽略预发布后缀，而且解析不了带构建元数据的版本。
  - `hubVersion` 为空串（装配时没传）与 `dev` 同样按非正式版本处理，两页各有用例。
  - spec §5.4 的 `--site-url` 已删：安装命令的 hub 地址由面板取浏览器 origin（§10），hub 不生成对外地址。
- **打包的口径**：提交 d678086 的标题说"产物不含宿主元数据"说过了：`--no-xattrs` 只去掉扩展属性的 pax 头，归档里的属主（uid、用户名）与 mtime 仍是构建机的。install.sh 用 `install` 复制文件，不继承归档属主，功能不受影响。二进制里的构建机路径由 `-trimpath` 去掉。
