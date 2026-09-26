# macOS agent Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 让 agent 在 macOS（Apple Silicon 与 Intel）上以 `CGO_ENABLED=0` 采全 §4.2 的每一项指标与 facts，随 `make release` 发布 `probe-agent_darwin_<arch>.tar.gz`，并给出以 root 运行、与 Linux 脚本同形的 `install-macos.sh`（LaunchDaemon + 专用账户），脚本逻辑有桩测试、本机有端到端验收、真机有核对清单。

**Architecture:** 采集层按"平台取原始读数（`Host`）/ 平台无关的差分、过滤、用量不变式（`Collector`）"分层：Linux 的 `/proc`、`/sys` 读法收进 `ProcFS`，darwin 的字节布局解析与组合收进不带 build tag 的 `darwinraw.go`（Linux 上可测），只有系统调用层 `platform_darwin.go` 带 darwin 约束并引用 purego。网卡、内存总量、负载、swap、连接数、启动标识走 `x/sys/unix` 的 sysctl；逐 CPU tick、VM 统计、页大小与进程数经 purego 调 libSystem。安装侧 `deploy/launchd/xyz.probe.agent.plist` 是真实文件，原样打进包；`deploy/install-macos.sh` 的步骤与 Linux 的 `install.sh` 逐条对齐，差异只在平台工具（dscl、shasum、launchctl、ps）。

**Tech Stack:** Go 1.27.1、`golang.org/x/sys` v0.48.0（sysctl、statfs、clock_gettime）、`github.com/ebitengine/purego` v0.11.1（仅 darwin 文件引用）、POSIX sh、launchd、GNU Make、GitHub Actions（macOS runner）。

**Spec:** `docs/superpowers/specs/2026-09-17-probe-architecture-design.md`（提交 c381a27）——§2（agent 平台、darwin 依赖隔离）、§4.2（`Metrics`/`Facts` 与 optional 语义）、§4.5、§4.7、§7（boot_id 与流量差分、默认网卡排除）、§12（darwin 文件的测试要求、Linux 上 `GOOS=darwin go vet`）、§13 第 1、2 项、§14（macOS agent 与 Linux 安装脚本各条）。计划从 spec 论证；spec 没写的由本计划定，理由写在"规划决定"一节。

**工作树:** `/Users/xjetry/work/vibe/probe-macos`，分支 `m5m6-macos`，自 main（c381a27）由控制端创建：`git -C /Users/xjetry/work/vibe/probe worktree add /Users/xjetry/work/vibe/probe-macos -b m5m6-macos main`。实现者是这台 Mac（Apple Silicon，macOS 26.3.1）上的席位：可以普通用户身份跑 Go 测试、agent、hub、`launchctl` 的用户域（`gui/<uid>`）；**不能 sudo，不能在本机执行安装脚本**（脚本只在 PATH 替身下以普通用户跑逻辑测试）。

## Global Constraints

- 全部 `CGO_ENABLED=0`（§14）。编译 agent 的 `go test`、`go build`、`go run` 命令都显式带 `CGO_ENABLED=0`，与发布产物同一模式（purego 在 cgo 关闭时走自带的 fakecgo 路径）；`make` 目标已由 Makefile 顶部的 `export CGO_ENABLED=0` 承载。
- agent 平台是 Linux、macOS；"darwin 侧以 build tag 隔离，其依赖不链入 Linux 二进制"（§2）。purego 只能在带 darwin 约束的文件里引用；由 `cmd/agent/deps_test.go` 按依赖图显式检查（Task 3）。
- `Metrics` 的每个字段是 `optional`：读不到只让对应字段缺失并记日志，不填 0（§4.2"`optional` 区分无读数与读数为 0"，§14"采集读不到某个文件时只让对应字段缺失并记日志，上报照常"）。
- `boot_id` 放在 `Metrics`；hub 侧"`boot_id` 变化、或计数器小于基线：只把基线重置为当前计数器，不入账"，"计数器缺失：不入账也不动基线"（§7）。darwin 的 `boot_id` 取法必须让同一次开机内 agent 重启不换值、机器重启换值。
- 不信任 agent 墙钟（§4.5）：运行时长不用"墙钟 − 启动时刻"。
- agent 上报失败在进程内退避、上限为 TTL，不退出（§4.7）。
- 产物 `probe-agent_darwin_<arch>.tar.gz`（二进制、launchd plist），arch 为 amd64、arm64；资产名不带版本号；服务定义在 `deploy/` 下是真实文件，打包时原样放入；构建逻辑只在 Makefile 一处，本地验收与线上发布同一套产物（§14）。
- `install-macos.sh` 以 root 运行：检测架构 → 用 `dscl` 建 `_probe-agent` 用户与组 → 下载并用 `shasum -a 256` 校验 → 停止已装的 LaunchDaemon 并确认进程退出 → 替换二进制 → 没有配置时 `probe-agent register` → 配置属主同 Linux（`/etc/probe-agent`，目录 root 属主、组 `_probe-agent`、0750，文件 0600）→ 写 `/Library/LaunchDaemons/xyz.probe.agent.plist`（`UserName` 为该用户、`KeepAlive`、日志在 `/Library/Logs/probe-agent/`）→ `launchctl bootstrap system` → 确认进程活着。重跑即升级，`--uninstall`、`--purge` 语义同 Linux（§14）。
- 与 Linux 脚本对齐的语义（§14）：参数 `--hub`、`--key`、`--name`、`--version`、`--base-url`；给了 `--base-url` 时 `--version` 不参与下载地址并提示；重跑沿用现有配置时 `--key`、`--name` 被忽略并提示；建用户排在下载与注册之前；建完回查主组、不信退出码；包完整（校验、解包、文件齐全）之后才停服务；停服务后无条件按有效 uid 确认没有服务进程，有上限地等待；二进制先写同目录临时文件再 `mv`；服务定义每次覆盖；启动后确认"出现以服务用户运行的进程，3 秒后同一个进程仍在"；`--purge` 必须与 `--uninstall` 同给，删配置、日志目录、用户与同名组并回查；以 `curl … | sh -s --` 运行时每个可能读 stdin 的外部命令显式 `</dev/null`，不用 `exec </dev/null`。
- "没有 macOS 虚拟机可用：脚本逻辑用桩测试，真机验收由人在 Mac 上执行，脚本随附检查清单。面板的安装命令只给 Linux 的两条，macOS 的写在 README。CI 含 macOS runner 跑 agent 的测试"（§14）。
- "darwin 采集文件带 build tag，Linux 上的验证循环照不到：CI 含 macOS runner 跑其测试；Linux 上至少执行 `GOOS=darwin go vet ./...`"（§12，`make lint` 已有这一行）。
- 静态门禁 `scripts/checkstatic` 只收 Linux 产物（agent 与 hub），不复制文件清单（§14）；darwin 产物不交给它，理由写在 Makefile 注释与"规划决定"。
- 每条新断言做一次缺陷注入，确认它红且红在正确的原因上（§12）。注入步骤统一写法：先 `git add` 被注入的文件，让暂存区成为基准（本任务新建或尚未提交的改动都在里面；不先 add 就 `git checkout --` 会把文件退回上一次提交，冲掉本任务的实现）→ 注入 → `git diff --stat` 非空 → 跑测试看红与原因 → `git checkout -- <文件>` 从暂存区还原 → `git diff --stat` 为空。
- 判成败的命令写成 `cd /Users/xjetry/work/vibe/probe-macos && cmd > /tmp/m6mac-<名>.log 2>&1; echo $?`，不接管道；注入步骤里行内写出的命令省略了前缀，同样在这个工作树下执行；Go 测试一律 `go test -count=1`；生成物只由 `make gen` 产生（本计划不改 proto）。
- 项目规则：注释与提交信息写 WHY 与不变式并指明保证方，不复述代码；不写任务/步骤编号、方案代号、审阅引用；注释里的因果只写实测过或能从代码推出的；不打补丁、不加兼容层；同形状的问题一次改齐；不变式靠显式检查承载。实现者不改 `docs/`、不派子代理、不 push、不 sudo。
- 端口：`scripts/macos-accept.sh` 用 18087（hub）与 18088（回环流量源），与 e2e 的 18080/18081、install-accept 的 18085/18086 错开。launchd 用户域作业的 Label 前缀 `xyz.probe.agent.accept.`，脚本只卸下自己建的那一个。

## Review Focus

1. **网卡字节计数的来源：32 位回绕与 1 KiB 取整。** 本机普通用户进程经 `NET_RT_IFLIST2` 或 `getifaddrs` 读到的字节数都截到 32 位并按 KiB 取整（实验结论第 6 条）；累计过 4 GiB 的网卡在 hub 上会表现为"计数器变小"而被只换基线、不入账（§7），流量静默丢失。期望：大流量网卡在 hub 上持续入账。钉住它的测试：Task 2 `TestDarwinMetricsFromSyscallLayout`（en0 取 2^32+1025，既超 32 位又不是 1024 的倍数），Task 3 `TestDarwinInterfaceCountersSandwichNetstat`（与 `netstat -ib` 前后夹逼，无容差）；注入 `& 0xFFFFFC00` 两者皆红。
2. **睡眠唤醒跨越采样间隔。** CPU 差分只含醒着的 tick，逐 CPU 的 `natural_t` 计数约 497 天回绕一次；运行时长必须含睡眠（Linux 的 `/proc/uptime` 含挂起时间）。期望：唤醒后第一份上报照常有 `cpu_pct`，回绕之后下一份仍有读数，运行时长与 `now − kern.boottime` 一致。测试：Task 2 `TestTickAccumulatorAcrossWrapAndCPUCountChange`；Task 3 `TestDarwinBootIDAndUptimeMatchSysctl`（把时钟换成 `CLOCK_UPTIME_RAW` 在睡过的机器上红，本机差 14 小时）；README 真机核对第 12 条。
3. **`kern.bootsessionuuid` 在 hub 侧流量差分里的语义。** 同一次开机内 agent 重启不换 `boot_id`，停机期间的流量要在重启后的首次上报里入账；机器重启换 `boot_id`，hub 只重置基线、不把开机以来的计数当增量；睡眠唤醒不换。测试：Task 3 `TestDarwinBootIDAndUptimeMatchSysctl`（与 `sysctl -n kern.bootsessionuuid` 相等）；Task 6 验收脚本"agent 停着时在回环上走 64 MiB，重启后总量至少涨 64 MiB"（注入"boot_id 随进程变化"即红）；README 真机核对第 12、13 条。
4. **Apple Silicon、Intel 与 Rosetta 的差异。** 页大小 16 KiB 与 4 KiB；Rosetta 转译的进程读 `hw.pagesize` 得 4096 而内核页是 16384；`hw.machine`、`uname -m` 在 Rosetta 下报 x86_64；聚合的 `HOST_CPU_LOAD_INFO` 在 Apple Silicon 上按秒取差不稳（实验结论第 4、5、11、13 条）。期望：两种硬件、原生或转译运行，内存已用量与 CPU 比例都对，安装脚本装对架构的包。测试：Task 3 的 darwin 测试在本机原生与 `GOARCH=amd64`（Rosetta）各跑一遍——`hw.pagesize` 注入只在 Rosetta 下红；Task 7 的 CI 矩阵含 `macos-26-intel`；Task 4 `TestArchitectureFromHardwareNotShell`；Task 6 在 Rosetta 下执行 amd64 产物。
5. **launchd `KeepAlive` 与 agent 自身退避的叠加。** hub 不可用时 agent 在进程内退避（上限 TTL）而不退出，launchd 不参与；进程崩溃时 launchd 每 5 秒拉起一次、不放弃；darwin 的 libSystem 函数取不到时 `NewPlatform` 不失败，否则 KeepAlive 会每 5 秒拉起一个立即退出的进程。测试：Task 6 验收脚本"hub 停 15 秒期间 pid 不变、恢复后重新在线"与"连续两次被杀，第二次拉起相隔约 5 秒"；Task 3 `TestDarwinWithoutLibSystemKeepsSysctlReadings`。

---

## 实验结论（spec §13 第 1 项；2026-09-26，本机 macOS 26.3.1 / Apple M4 Max 16 核 / Go 1.27.1）

实验程序在私有临时目录（`mktemp -d /tmp/probe-macos-spike.XXXXXX`，独立 module，依赖 purego v0.11.1、x/sys v0.48.0、x/net v0.58.0），不进仓库。计划里的代码已在 main（c381a27）的 `git archive` 副本上按本节路线实现并跑通：darwin 原生 `go test ./...` 全绿，Rosetta 下（`GOARCH=amd64`）`./internal/agent/collect ./cmd/agent` 全绿，`golang:1.27.1-bookworm` 容器里 `./internal/agent/... ./cmd/agent/... ./deploy/...` 全绿，`make lint`、`make build`、`make release` 为 0，`scripts/macos-accept.sh` 输出 `MACOS ACCEPT OK (arm64)`。

1. **构建与运行。** `CGO_ENABLED=0 GOOS=darwin GOARCH=arm64|amd64 go build` 退出 0；`file` 报 `Mach-O 64-bit executable arm64` / `x86_64`；`otool -L` 报 `/usr/lib/libSystem.B.dylib`、`/usr/lib/libresolv.9.dylib`（darwin 产物按平台约定动态链接，静态门禁不适用）。`codesign -dv` 报 `flags=0x20002(adhoc,linker-signed)`、`Signature=adhoc`。在 `golang:1.27.1-bookworm`（linux/arm64 容器）里交叉编译的 darwin/arm64 二进制同样是 `adhoc,linker-signed`，拷回本机直接运行、purego 调用正常——在 Linux 主机上交叉编译的产物能在 Apple Silicon 上启动（本机容器是 linux/arm64；CI 的 ubuntu 是 amd64 主机，签名由 Go 链接器按目标写入，这一点没有在 amd64 主机上单独实测）。amd64 产物经 `arch -x86_64` 在 Rosetta 下运行正常。
2. **启动标识（不需要 purego）。** `unix.Sysctl("kern.bootsessionuuid")` → `"DEF2AAD6-739B-4A47-AC3D-8D8AB38FE277"`。统一日志在开机时刻记有 `2026-09-23 20:37:55.957 … === system boot: DEF2AAD6-739B-4A47-AC3D-8D8AB38FE277`；其后本机多次睡眠唤醒（`pmset -g log` 最近一次 `Wake` 在 2026-09-26 03:52:53，累计睡眠约 14 小时，见下条），值未变。`kern.bootuuid` 与 `kern.apfsprebootuuid` 同为 `72E8075A-…`，是 APFS preboot 卷的标识而不是这次开机的，不能用。
3. **运行时长（不需要 purego）。** `kern.boottime` → `{ sec = 1790167075, usec = 972872 }`；同一时刻 `unix.ClockGettime(CLOCK_MONOTONIC)` = 245955 s，`now − kern.boottime` = 245956 s，`CLOCK_UPTIME_RAW` = 195164 s。macOS 的 `CLOCK_MONOTONIC` 自启动起算且含睡眠，与 Linux `/proc/uptime` 同口径，又不依赖墙钟；`CLOCK_UPTIME_RAW` 不含睡眠。
4. **CPU（需要 purego）。** `host_processor_info(mach_host_self(), PROCESSOR_CPU_LOAD_INFO)` 返回 `n=16 cnt=64`（每 CPU user/system/idle/nice 四个 `natural_t`），`vm_deallocate(mach_task_self(), …)` 返回 0；`kern.clockrate` 为 `{ hz = 100, … }`。连续 20 个 1 秒窗口：逐 CPU 之和每秒 1588–1603 个 tick（16 × hz），而聚合的 `host_statistics(HOST_CPU_LOAD_INFO)` 每秒 1057–2202 个，同一窗口算出的忙碌比例相差最多 12 个百分点（例：逐 CPU 78.6% 对聚合 66.6%；91.1% 对 100.0%），与 cgo 开关无关。→ 用逐 CPU 的 `host_processor_info`。
5. **内存（总量不需要 purego，已用需要）。** `unix.SysctlUint64("hw.memsize")` = 137438953472。`host_statistics64(HOST_VM_INFO64)` count=38、kr=0；`host_page_size` = 16384。已用 = (internal − purgeable + wire + compressor) × 页 = 87.23 GiB，同时刻 `vm_stat`（`page size of 16384 bytes`，Anonymous 5191703、purgeable 20394、wired 491012、occupied by compressor 78583）算出 87.6 GiB。Rosetta 下：`hw.pagesize` = **4096**，`vm.pagesize` = 16384，`host_page_size` = 16384——页计数的单位只能取 `host_page_size`。
6. **网卡计数器（不需要 purego）。** 三条路径对照 `netstat -ib -n`（它给出完整的 64 位字节数）：
   - `getifaddrs`（purego）的 `if_data.ifi_ibytes`：lo0 = 3809657856、en0 = 1047084032；同一时刻 `netstat` 为 16694559795、52586692460。两者恰是 `netstat` 值模 2^32 后向下取整到 1024 的倍数（16694559795 mod 2^32 = 3809657907 → 3809657856；52586692460 mod 2^32 = 1047084908 → 1047084032）。
   - `route.FetchRIB(0, NET_RT_IFLIST2, 0)` 解析 `if_msghdr2.ifm_data`（`if_data64`）：en0 的 `ifi_ibytes` 原始字节为 `00 24 cc 1b 00 00 00 00`——高 32 位为 0、低 10 位为 0，同样是截断并取整后的值；包计数 `ifi_ipackets` 则是完整值（63870143，稍后读的 `netstat` Ipkts 为 63870145）。
   - `unix.SysctlRaw("net.link.generic.ifdata", <index>, 1)`（IFDATA_GENERAL，返回 `struct ifmibdata`，`if_data64` 在偏移 52，`ifi_ibytes`/`ifi_obytes` 在其内 64/72）：同一时刻 lo0 = 16418378730、en0 = 52023553193 / 40164171549、awdl0 = 16645937、utun1024 = 170869852866，与 `netstat -ib` 逐字节相等；Rosetta 下相同。
   `unix.SysctlUint32("net.link.generic.system.ifcount")` = 35；索引 23、34、35 返回 ENOENT（`no such file or directory`）。`unix.SysctlRaw("net.route", 0, 0, 6, 0)` 按名字解析失败（ENOENT）。→ 用 `net.link.generic.ifdata`，逐索引读、ENOENT 跳过。
7. **负载、swap（不需要 purego）。** `vm.loadavg` 原始 24 字节 `b0980000fec20000fbc70000000000000008000000000000` → 19.09 24.37 25.00（`fscale` = 2048）。`vm.swapusage` 原始 32 字节 → total=0 avail=0 used=0 pagesize=16384 encrypted=1；`sysctl vm.swapusage` 同时报 `total = 0.00M  used = 0.00M  free = 0.00M  (encrypted)`（swap 文件按需建，0 是真实读数）。
8. **磁盘（不需要 purego）。** `unix.Statfs("/")`：bsize=4096 blocks=242837545 bfree=52130935 fstype=apfs → total 994662584320、used 781134274560；`/System/Volumes/Data` 的结果逐字段相同。`df -k /` 的 1024-blocks 971350180 与 total 一致，但 Used 列只有 19936012（系统卷自身），Available 205041228 是容器空闲。→ 沿用 Linux 的 `blocks − bfree` 口径，在 APFS 上即整个容器的占用。
9. **进程数（需要 purego）。** `proc_listallpids(NULL, 0)` = 1580（带余量的估计），带缓冲区 = 1561；`unix.SysctlKinfoProcSlice("kern.proc.all")` = 1561 项（每项 648 字节，约 1 MB）；`ps -A -o pid= | wc -l` = 1560。→ 用 `proc_listallpids`（缓冲区约 6 KB，libSystem 已为 CPU 与内存打开）。
10. **连接数（不需要 purego）。** `net.inet.tcp.pcbcount` = 924、`net.inet.udp.pcbcount` = 90；同一时刻 `netstat -an -p tcp` 列出 867 条 tcp、`-p udp` 列出 90 条 udp。PCB 计数是内核里的协议控制块数，与 Linux sockstat 的 `inuse` 同为内核计数口径，数值与 `netstat` 的行数不必相等。
11. **facts（不需要 purego）。** `kern.osproductversion` = `26.3.1`、`kern.osrelease` = `25.3.0`、`machdep.cpu.brand_string` = `Apple M4 Max`（Rosetta 下相同）、`hw.logicalcpu` = 16、`kern.hv_vmm_present` = 0、`kern.hostname` = `xjetrydeMacBook-Pro.local`；`hw.machine` 原生 `arm64`、Rosetta 下 `x86_64`；`sysctl.proc_translated` 原生 0、Rosetta 下 1。
12. **ICMP（§13 第 2 项已有结论）。** 非 root 数据报 socket 可用；本机验收脚本以普通用户对 127.0.0.1 的 ICMP 任务有 RTT 样本、无 error。
13. **launchd 与安装工具。** `launchctl print system/<不存在>` 退出 113（`Could not find service … in domain for system`）；`launchctl bootout gui/501/<不存在>` 退出 3（`Boot-out failed: 3: No such process`）。`launchd.plist(5)`：`StandardOutPath`/`StandardErrorPath` 不存在时"created with … ownership reflecting the user and/or group specified as the UserName and/or GroupName"；`KeepAlive` "implicitly implies RunAtLoad"；`ThrottleInterval` 默认 10 秒。用户域实测：包里的 plist 改 Label、路径并去掉 `UserName`/`GroupName` 后 `launchctl bootstrap gui/$(id -u)` 即启动，日志两个文件由 launchd 创建；进程被 `kill -9` 后立即拉起，拉起后立刻再杀，下一次拉起相隔 5 秒。`shasum -a 256 -c` 接受 `sha256sum` 格式的行，校验失败退出 1 并打印 `FAILED`；`ps -axo uid=,pid=` 的 uid 是有效 uid（`ps(1)`：`uid  effective user ID`）；`sysctl -in no.such.oid` 输出为空、退出 0；Rosetta 下的 shell 里 `uname -m` = `x86_64` 而 `sysctl -in hw.optional.arm64` = `1`；`/bin/sh` 是 bash 3.2 的 sh 模式。

**实现路线：** sysctl（x/sys/unix）取启动标识、内存总量、负载、swap、连接数、网卡计数器（`net.link.generic.ifdata`）与 facts；`statfs` 取磁盘；`clock_gettime(CLOCK_MONOTONIC)` 取运行时长；purego 调 libSystem 的 `host_processor_info`、`host_statistics64`、`host_page_size`、`vm_deallocate`、`proc_listallpids`。字节布局解析全部放进不带 build tag 的 `darwinraw.go`，Linux CI 上可测；带 darwin 约束的只有取数的系统调用层。

## 规划决定（spec 未写或只给候选，由本计划定）

- **采集分层。** 现有 `Collector` 直接读 `fs.FS`，是 Linux 形状。新增 `Host` 接口（方法不导出，实现只在本包），`Collector` 只做差分、过滤、`used ≤ total` 检查与"读不到即缺失"；Linux 与 darwin 各实现一次 `Host`。不在 darwin 文件里另写一份 `Metrics()`——那会复制 CPU 差分、速率与过滤逻辑。
- **`used ≤ total` 由 `Collector` 显式检查。** 两个平台的已用量都是差或和，计数不一致或无符号减法回绕都会得出大于总量的值；按读不到处理并记日志，不截断（截断会把错误计数伪装成满载）。Linux 的 `total − MemAvailable` 同样受益，同形状一次改齐。
- **`Facts.arch` 两个平台都取 `runtime.GOARCH`，不取 `hw.machine`。** `hw.machine` 用 `uname` 的词汇（`x86_64`），Linux 侧与发布资产名用 GOARCH 的词汇（`amd64`），同一字段不该两套词汇；Rosetta 下 `hw.machine` 也只报转译后的 `x86_64`（实验第 11 条），并不比 GOARCH 多给信息。`os` = `"macOS " + kern.osproductversion`，`kernel` = `kern.osrelease`，`cpu_model` = `machdep.cpu.brand_string`，`cpu_cores` = `hw.logicalcpu`（与 Linux 数 `processor` 行同为逻辑核），`virtualization` = `kern.hv_vmm_present == 1` 时为 `vm`（与 Linux 认不出具体类型时的取值相同）。
- **内存已用** = (匿名页 − 可清除页 + 联动页 + 压缩器占用页) × `host_page_size`：文件缓存与可清除内存不算占用，与 Linux 的 `total − MemAvailable` 同义。可清除页多于匿名页时按读不到处理。
- **运行时长用 `CLOCK_MONOTONIC`，不用 `now − kern.boottime`：** 值相同（实验第 3 条），但前者不经墙钟（§4.5）。
- **darwin 默认不计流量的网卡**（§7 只列了 Linux 的名字）：`lo* gif* stf* utun* ipsec* bridge* vmenet* awdl* llw* anpi* ap*`，与 Linux 列表同一原则——回环，以及字节同时计在物理网口上或不出本机的接口；以太网、Wi-Fi、雷雳网口都叫 `en*`。`--net-exclude` 的帮助文字改为按平台取默认列表。Linux 侧已知的同类缺口（`tun*`、`wg*` 未排除）不在本计划里改：spec §7 逐字列出了 Linux 的默认值。
- **darwin 网卡读取遇到 ENOENT 以外的错误时整个网络读数缺失**，不像 Linux `ProcFS` 那样跳过读不到的网卡：darwin 的枚举只有索引空间，ENOENT 是"该索引无网卡"的明确信号；其余错误意味着一块存在的网卡读失败，合计会先变小、恢复时把它的全部历史计数当增量加回去，而缺失的读数让 hub 保持基线（§7）。Linux 侧的跳过覆盖了 `/sys/class/net` 里的非网卡条目（如 `bonding_masters`），本计划不改。
- **安装脚本的"是否发 stop"看作业是否已载入 system 域（`launchctl print`），不看 plist 文件在不在。** launchd 按已载入的作业管进程：文件在而作业未载入时 `bootout` 以 3 失败（实验第 13 条），作业在而文件被删时进程照跑。这是 Linux 脚本"看服务定义是否已安装"在 launchd 上的等价物。
- **架构按 `sysctl -in hw.optional.arm64` 判定，其次 `uname -m`：** 在 Rosetta 转译的终端里 `uname -m` 报 x86_64（实验第 13 条），只看它会在 Apple Silicon 上装 amd64 包。
- **账户号取 300–499 中同时未被用作 UniqueID 与 PrimaryGroupID 的最小值**，组与用户同号；低于 500 的账户不出现在登录窗口，另设 `IsHidden 1`。登录 shell `/usr/bin/false`，家目录 `/var/empty`，密码 `*`。
- **`launchctl enable` 在 `bootstrap` 之前执行：** 清掉可能残留的禁用覆盖，与 Linux 的 `systemctl enable` 同位；卸载只 `bootout` 并删 plist，不写禁用覆盖。
- **不装 CA：** macOS 自带 curl 与系统信任库；Linux 脚本的 CA 分支对应的是不带 CA 的极简容器镜像，macOS 没有这种形态。
- **`ThrottleInterval` = 5：** 与 Linux 两种 init 的"无限次、每次 5 秒"一致（§14）；launchd 默认 10 秒。
- **脚本逻辑测试的替身接缝 `PROBE_INSTALL_ROOT`：** 普通用户无法写 `/usr/local/bin`、`/etc`、`/Library`，而重跑、升级、卸载各分支都要看这些路径上的文件。脚本把全部落盘路径挂在这个前缀下（生产为空）；它不改变 plist 里的真实路径，也不引入任何分支。
- **真机核对清单放在 README 的 macOS 一节，不放在计划里：** §14 要求"脚本随附检查清单"、由人在 Mac 上执行；清单要跟着 `install-macos.sh` 与 plist 一起演进，而计划是本里程碑的记录，之后改脚本的人不会来这里找；按指示也不在 `docs/` 之外新建文档目录。README 的清单写成命令加期望输出，普通用户装完也能照着核对。
- **CI 的 macOS 任务同时跑 `./deploy/...`：** 安装脚本在 macOS 上由 `/bin/sh`（bash 3.2 的 sh 模式）执行，ubuntu 上的 `make test` 跑的是 dash；两种 shell 都要过。矩阵含 `macos-latest`（Apple Silicon）与 `macos-26-intel`（x64，runner-images 的 README 与 actionlint v1.7.12 的标签表都列有它）。

## 文件结构

| 文件 | 职责 |
|---|---|
| `internal/agent/collect/host.go` | 包文档；`Host` 接口（方法不导出）与原始读数类型 `usage`、`ifaceCounters`、`hostFacts` |
| `internal/agent/collect/collect.go` | `Collector`：CPU 与网速差分、网卡过滤、`checkUsage`（`used ≤ total`）、facts 组装；平台无关 |
| `internal/agent/collect/procfs.go` | `ProcFS`：Linux 的 `/proc`、`/sys` 读法；默认排除列表 `linuxNetExclude` |
| `internal/agent/collect/parse.go` | `/proc` 文本的纯解析（`parseLoadavg` 改为同时返回进程数） |
| `internal/agent/collect/statfs_unix.go` | `statfs`（`linux \|\| darwin`），从 `platform_linux.go` 移来，两平台共用一份 |
| `internal/agent/collect/darwinraw.go` | darwin 的字节布局解析与组合：`darwinSource`、`darwinHost`、`tickAccumulator`、`darwinNetExclude`；不带 build tag |
| `internal/agent/collect/platform_linux.go` | `NewPlatform`、`DefaultNetExclude`（Linux） |
| `internal/agent/collect/platform_darwin.go` | `NewPlatform`、`DefaultNetExclude`（darwin）；`darwinSyscalls`：sysctl 与 purego 调 libSystem |
| `internal/agent/collect/platform_other.go` | 其余平台：`!linux && !darwin` |
| `internal/agent/collect/darwinraw_test.go` | 以构造字节替换系统调用层，Linux 上可跑 |
| `internal/agent/collect/platform_darwin_test.go` | 系统调用层与系统命令行工具的对照（darwin） |
| `cmd/agent/deps_test.go` | purego 只在 darwin 的依赖图里 |
| `cmd/agent/main.go` | `--net-exclude` 的帮助文字按平台取默认列表 |
| `deploy/launchd/xyz.probe.agent.plist` | LaunchDaemon 定义，原样打包 |
| `deploy/install-macos.sh` | macOS 安装、升级、卸载脚本 |
| `deploy/installmacos_test.go` | 以普通用户在 PATH 替身下跑脚本的逻辑测试 |
| `Makefile` | `lint` 加 shellcheck；`build` 加 darwin 两架构；`release` 加 darwin 产物与 `install-macos.sh` |
| `scripts/macos-accept.sh` | 本机端到端验收（普通用户、不 sudo） |
| `.github/workflows/ci.yml` | macOS runner 矩阵 |
| `README.md` | hub 运行、Linux 与 macOS 安装、macOS 真机核对清单 |

任务顺序：1 分层 → 2 darwin 解析 → 3 darwin 系统调用层 → 4 plist 与安装脚本 → 5 发布 → 6 本机验收 → 7 CI → 8 README。每个任务结束时 `make ci` 为 0。

---

### Task 1: 采集按 `Host` 分层（Linux 行为不变）

**Files:**
- Create: `internal/agent/collect/host.go`、`internal/agent/collect/procfs.go`、`internal/agent/collect/statfs_unix.go`
- Modify: `internal/agent/collect/collect.go`（整份替换）、`internal/agent/collect/parse.go`、`internal/agent/collect/platform_linux.go`（整份替换）、`internal/agent/collect/platform_other.go`（整份替换）、`cmd/agent/main.go:85`
- Test: `internal/agent/collect/collect_test.go`（整份替换）、`internal/agent/collect/parse_test.go`、`internal/agent/client/client_test.go:157,216`

**Interfaces:**
- Consumes：现有 `cpuTimes{idle, total uint64}`、`cpuPercent(prev, cur cpuTimes) (float64, bool)`、`parseStat`、`parseMeminfo`、`parseUptime`、`parseSockstat`、`parseCPUInfo`、`parseOSRelease`、`detectVirtualization`、`readTrim`、`netCounters{rx, tx uint64}`（`parse.go`，不改）。
- Produces（后续任务逐字使用）：
  - `type Host interface { bootID() (string, error); cpuTimes() (cpuTimes, error); memory() (usage, error); swap() (usage, error); disk() (usage, error); load() (loadAvg, error); procs() (uint32, error); uptime() (uint64, error); conns() (tcp, udp uint32, err error); ifaces(include func(name string) bool) ([]ifaceCounters, error); defaultNetExclude() []string; facts() hostFacts }`
  - `type usage struct{ total, used uint64 }`；`type ifaceCounters struct{ name string; rx, tx uint64 }`；`type hostFacts struct{ hostname, os, kernel, virtualization, cpuModel string; cpuCores uint32 }`；`type loadAvg struct{ l1, l5, l15 float64 }`
  - `type Collector struct { Host Host; Clock clock.Clock; NetInclude, NetExclude []string; Version string; IcmpAvailable bool }`，方法 `Metrics() (*probev1.Metrics, error)`、`Facts() *probev1.Facts`（签名不变）
  - `type ProcFS struct { FS fs.FS; DiskUsage func(path string) (total, used uint64, err error) }`
  - `func statfs(path string) (uint64, uint64, error)`（`linux || darwin`）；`func DefaultNetExclude() []string`（每个平台文件一份）；`func parseLoadavg(r io.Reader) (loadAvg, uint32, error)`

- [ ] **Step 1: 先把测试改到新接口**

`internal/agent/collect/collect_test.go` 整份替换为（新增 `TestUsageAboveTotalIsDroppedNotClamped` 与 `TestExcludedInterfacesAreNotSummed`，其余用例只改构造方式）：

```go
package collect

import (
	"os"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/xjetry/probe/internal/clock"
)

func fixture(t *testing.T) *Collector {
	t.Helper()
	return &Collector{
		Host:    &ProcFS{FS: os.DirFS("testdata/docker-debian"), DiskUsage: func(string) (uint64, uint64, error) { return 1000, 400, nil }},
		Clock:   clock.NewFake(time.Unix(0, 0)),
		Version: "test",
	}
}

func TestMetricsFromRealProcSnapshot(t *testing.T) {
	c := fixture(t)
	m, err := c.Metrics()
	if err != nil {
		t.Fatalf("unexpected read failures: %v", err)
	}
	if len(m.GetBootId()) != 36 {
		t.Fatalf("boot_id %q", m.GetBootId())
	}
	if m.CpuPct != nil {
		t.Fatal("first sample has no previous /proc/stat to diff against; cpu_pct must be absent")
	}
	if m.MemTotal == nil || m.GetMemTotal() == 0 || m.MemUsed == nil {
		t.Fatalf("mem: %+v", m)
	}
	if m.Load1 == nil || m.Load5 == nil || m.Load15 == nil {
		t.Fatal("load triple missing")
	}
	if m.Procs == nil || m.GetProcs() == 0 || m.UptimeS == nil || m.GetUptimeS() == 0 {
		t.Fatalf("procs/uptime: %+v", m)
	}
	if m.TcpConns == nil || m.UdpConns == nil {
		t.Fatal("conn counts missing")
	}
	if m.GetDiskTotal() != 1000 || m.GetDiskUsed() != 400 {
		t.Fatalf("disk: %+v", m)
	}
	if m.NetRxTotal == nil {
		t.Fatal("net counters missing (eth0 should be included, lo excluded)")
	}
}

func TestCPUPercentAppearsOnSecondSample(t *testing.T) {
	fsys := fstest.MapFS{
		"proc/stat": {Data: []byte("cpu  100 0 50 800 20 0 10 0 0 0\n")},
	}
	c := &Collector{Host: &ProcFS{FS: fsys, DiskUsage: func(string) (uint64, uint64, error) { return 0, 0, nil }}, Clock: clock.NewFake(time.Unix(0, 0))}
	if m, _ := c.Metrics(); m.CpuPct != nil {
		t.Fatal("first sample must not carry cpu_pct")
	}
	fsys["proc/stat"] = &fstest.MapFile{Data: []byte("cpu  150 0 50 850 20 0 10 0 0 0\n")} // +100 tick，其中 50 空闲
	m, _ := c.Metrics()
	if m.CpuPct == nil || m.GetCpuPct() != 50 {
		t.Fatalf("cpu_pct = %v", m.CpuPct)
	}
}

func TestNetRateNeedsTwoSamples(t *testing.T) {
	clk := clock.NewFake(time.Unix(0, 0))
	fsys := fstest.MapFS{
		"sys/class/net/eth0/statistics/rx_bytes": {Data: []byte("1000\n")},
		"sys/class/net/eth0/statistics/tx_bytes": {Data: []byte("2000\n")},
	}
	c := &Collector{Host: &ProcFS{FS: fsys, DiskUsage: func(string) (uint64, uint64, error) { return 0, 0, nil }}, Clock: clk}
	m, _ := c.Metrics()
	if m.GetNetRxTotal() != 1000 || m.NetRxBps != nil {
		t.Fatalf("first: %+v", m)
	}
	fsys["sys/class/net/eth0/statistics/rx_bytes"] = &fstest.MapFile{Data: []byte("3000\n")}
	clk.Advance(2 * time.Second)
	m, _ = c.Metrics()
	if m.GetNetRxBps() != 1000 {
		t.Fatalf("rx_bps = %d, want (3000-1000)/2s = 1000", m.GetNetRxBps())
	}
}

func TestMissingFilesYieldMissingReadingsNotZero(t *testing.T) {
	c := &Collector{Host: &ProcFS{FS: fstest.MapFS{}, DiskUsage: func(string) (uint64, uint64, error) { return 0, 0, os.ErrNotExist }}, Clock: clock.NewFake(time.Unix(0, 0))}
	m, err := c.Metrics()
	if err == nil {
		t.Fatal("read failures must be reported for logging")
	}
	if m.MemTotal != nil || m.Load1 != nil || m.DiskTotal != nil || m.Procs != nil || m.NetRxTotal != nil {
		t.Fatalf("missing inputs must leave readings unset, got %+v", m)
	}
}

func TestFactsFromRealProcSnapshot(t *testing.T) {
	f := fixture(t).Facts()
	if f.GetHostname() == "" || f.GetKernel() == "" || f.GetOs() == "" || f.GetCpuCores() == 0 || f.GetArch() == "" {
		t.Fatalf("%+v", f)
	}
	if f.GetAgentVersion() != "test" || f.GetIcmpAvailable() {
		t.Fatalf("%+v", f)
	}
}

// used > total 只能来自不一致的计数或回绕的减法：按读不到处理，不截断成满载。
func TestUsageAboveTotalIsDroppedNotClamped(t *testing.T) {
	fsys := fstest.MapFS{
		"proc/meminfo": {Data: []byte("MemTotal: 1000 kB\nMemAvailable: 2000 kB\nSwapTotal: 10 kB\nSwapFree: 4 kB\n")},
	}
	c := &Collector{Host: &ProcFS{FS: fsys, DiskUsage: func(string) (uint64, uint64, error) { return 100, 101, nil }}, Clock: clock.NewFake(time.Unix(0, 0))}
	m, err := c.Metrics()
	if m.MemTotal != nil || m.MemUsed != nil || m.DiskTotal != nil || m.DiskUsed != nil {
		t.Fatalf("used above total must leave both readings unset, got mem %d/%d disk %d/%d", m.GetMemUsed(), m.GetMemTotal(), m.GetDiskUsed(), m.GetDiskTotal())
	}
	if err == nil || !strings.Contains(err.Error(), "memory: used") || !strings.Contains(err.Error(), "disk: used 101 exceeds total 100") {
		t.Fatalf("err = %v, want both rejections named", err)
	}
	if m.GetSwapTotal() != 10*1024 || m.GetSwapUsed() != 6*1024 {
		t.Fatalf("swap from the same file must be unaffected: %v/%v", m.SwapTotal, m.SwapUsed)
	}
}

// 过滤在 Collector 里决定、由 Host 执行：被排除的网卡不进合计，未给 --net-exclude 时用平台默认列表。
func TestExcludedInterfacesAreNotSummed(t *testing.T) {
	fsys := fstest.MapFS{
		"sys/class/net/eth0/statistics/rx_bytes":    {Data: []byte("1000\n")},
		"sys/class/net/eth0/statistics/tx_bytes":    {Data: []byte("2000\n")},
		"sys/class/net/lo/statistics/rx_bytes":      {Data: []byte("50000\n")},
		"sys/class/net/lo/statistics/tx_bytes":      {Data: []byte("50000\n")},
		"sys/class/net/docker0/statistics/rx_bytes": {Data: []byte("70000\n")},
		"sys/class/net/docker0/statistics/tx_bytes": {Data: []byte("70000\n")},
	}
	c := &Collector{Host: &ProcFS{FS: fsys, DiskUsage: func(string) (uint64, uint64, error) { return 0, 0, nil }}, Clock: clock.NewFake(time.Unix(0, 0))}
	if m, _ := c.Metrics(); m.GetNetRxTotal() != 1000 || m.GetNetTxTotal() != 2000 {
		t.Fatalf("net = %d/%d, want eth0 only", m.GetNetRxTotal(), m.GetNetTxTotal())
	}
	c = &Collector{Host: c.Host, Clock: c.Clock, NetExclude: []string{"eth*"}}
	if m, _ := c.Metrics(); m.GetNetRxTotal() != 120000 {
		t.Fatalf("explicit exclude list replaces the default: rx = %d, want lo + docker0", m.GetNetRxTotal())
	}
}
```

`internal/agent/collect/parse_test.go`：

```diff
diff --git a/internal/agent/collect/parse_test.go b/internal/agent/collect/parse_test.go
--- a/internal/agent/collect/parse_test.go
+++ b/internal/agent/collect/parse_test.go
@@ -55,12 +55,12 @@ func TestParseMeminfoKBToBytes(t *testing.T) {
 }
 
 func TestParseLoadavg(t *testing.T) {
-	l, err := parseLoadavg(strings.NewReader("0.52 0.31 0.20 3/721 12345\n"))
+	l, procs, err := parseLoadavg(strings.NewReader("0.52 0.31 0.20 3/721 12345\n"))
 	if err != nil {
 		t.Fatal(err)
 	}
-	if l.l1 != 0.52 || l.l5 != 0.31 || l.l15 != 0.20 || l.procs != 721 {
-		t.Fatalf("%+v", l)
+	if l.l1 != 0.52 || l.l5 != 0.31 || l.l15 != 0.20 || procs != 721 {
+		t.Fatalf("%+v procs=%d", l, procs)
 	}
 }
 
@@ -91,7 +91,7 @@ func TestParseOSReleasePrettyName(t *testing.T) {
 }
 
 func TestInterfaceFilterDefaults(t *testing.T) {
-	c := &Collector{}
+	c := &Collector{Host: &ProcFS{}}
 	for _, n := range []string{"lo", "docker0", "veth1234", "br-abc", "virbr0"} {
 		if c.includeIface(n) {
 			t.Fatalf("%s must be excluded by default", n)
@@ -102,7 +102,7 @@ func TestInterfaceFilterDefaults(t *testing.T) {
 			t.Fatalf("%s must be included by default", n)
 		}
 	}
-	c = &Collector{NetInclude: []string{"eth*"}}
+	c = &Collector{Host: &ProcFS{}, NetInclude: []string{"eth*"}}
 	if c.includeIface("ens3") || !c.includeIface("eth1") {
 		t.Fatal("explicit include list must be exclusive")
 	}
```

`internal/agent/client/client_test.go`：

```diff
diff --git a/internal/agent/client/client_test.go b/internal/agent/client/client_test.go
--- a/internal/agent/client/client_test.go
+++ b/internal/agent/client/client_test.go
@@ -154,7 +154,7 @@ func newRunner(t *testing.T, hub *fakeHub) (*Runner, chan time.Duration) {
 	t.Cleanup(srv.Close)
 	sleeps := make(chan time.Duration, 100)
 	r := &Runner{
-		Collector: &collect.Collector{FS: fstest.MapFS{"proc/loadavg": {Data: []byte("0 0 0 1/2 3\n")}}, DiskUsage: func(string) (uint64, uint64, error) { return 1, 1, nil }, Clock: clock.NewFake(time.Unix(0, 0)), Version: "t"},
+		Collector: &collect.Collector{Host: &collect.ProcFS{FS: fstest.MapFS{"proc/loadavg": {Data: []byte("0 0 0 1/2 3\n")}}, DiskUsage: func(string) (uint64, uint64, error) { return 1, 1, nil }}, Clock: clock.NewFake(time.Unix(0, 0)), Version: "t"},
 		Client:    probev1connect.NewAgentServiceClient(srv.Client(), srv.URL),
 		Token:     "tok",
 		Clock:     clock.NewFake(time.Unix(0, 0)),
@@ -213,7 +213,7 @@ func TestFirstReportCarriesFactsThenOnlyOnRequest(t *testing.T) {
 func TestFactsChangeIsReportedWithoutRestart(t *testing.T) {
 	hub := &fakeHub{interval: 5000, reconcile: true}
 	r, _ := newRunner(t, hub)
-	fs := r.Collector.FS.(fstest.MapFS)
+	fs := r.Collector.Host.(*collect.ProcFS).FS.(fstest.MapFS)
 	fs["proc/sys/kernel/hostname"] = &fstest.MapFile{Data: []byte("old\n")}
 	round := 0
 	r.Sleep = func(context.Context, time.Duration) error {
```

- [ ] **Step 2: 跑红**

```sh
cd /Users/xjetry/work/vibe/probe-macos && CGO_ENABLED=0 go test -count=1 ./internal/agent/... > /tmp/m6mac-t1-red.log 2>&1; echo $?
```

Expected: `1`；日志含 `unknown field Host in struct literal` 与 `undefined: ProcFS`（编译失败即红：新接口还不存在）。

- [ ] **Step 3: 实现分层**

`internal/agent/collect/host.go`：

```go
// Package collect 把主机的原始读数变成一次上报。
//
// 平台差异只在 Host 的实现里：Linux 读 /proc 与 /sys（procfs.go），darwin 读 sysctl
// 与 Mach 接口（darwinraw.go 解析，platform_darwin.go 取数）。解析与组合都不带 build tag，
// 任何平台上都能用真机快照或按头文件布局构造的字节测试；带 build tag 的只有系统调用层。
package collect

// Host 是一个平台取原始读数的全部入口。每个方法独立失败，失败只让对应读数缺失。
//
// 方法名不导出，实现只能在本包内：Linux 的 ProcFS（procfs.go）与 darwin 的
// darwinHost（darwinraw.go）。两次采样的差分、网卡过滤、用量不变式与"读不到即缺失"
// 由此只在 Collector 里实现一次，平台只负责把各自的来源翻译成同一组原始读数。
type Host interface {
	bootID() (string, error)
	// cpuTimes 返回单调不减的累计 tick；Collector 只用两次读数之差，起点无意义。
	cpuTimes() (cpuTimes, error)
	memory() (usage, error)
	swap() (usage, error)
	disk() (usage, error)
	load() (loadAvg, error)
	procs() (uint32, error)
	uptime() (uint64, error)
	conns() (tcp, udp uint32, err error)
	// ifaces 只返回 include 接受的网卡：能按名字先过滤的平台不必读被排除网卡的计数器。
	ifaces(include func(name string) bool) ([]ifaceCounters, error)
	// defaultNetExclude 是未给 --net-exclude 时不计入流量的网卡；网卡命名随平台而异。
	defaultNetExclude() []string
	facts() hostFacts
}

type usage struct{ total, used uint64 }

type ifaceCounters struct {
	name   string
	rx, tx uint64
}

type hostFacts struct {
	hostname, os, kernel, virtualization, cpuModel string
	cpuCores                                       uint32
}
```

`internal/agent/collect/collect.go` 整份替换：

```go
package collect

import (
	"errors"
	"fmt"
	"path"
	"runtime"
	"time"

	probev1 "github.com/xjetry/probe/gen/probe/v1"
	"github.com/xjetry/probe/internal/clock"
	"google.golang.org/protobuf/proto"
)

// Collector 把 Host 的原始读数变成一次上报。
type Collector struct {
	Host  Host
	Clock clock.Clock
	// NetInclude 非空时只统计匹配的网卡；否则统计除 NetExclude 外的全部。
	NetInclude []string
	// NetExclude 为 nil 时用 Host.defaultNetExclude。
	NetExclude []string
	Version    string
	// IcmpAvailable 默认为 false；须在 Runner.Run 前赋值，运行期间只读。
	IcmpAvailable bool

	prevCPU  *cpuTimes
	prevNet  *netCounters
	prevNetT time.Duration
}

func (c *Collector) includeIface(name string) bool {
	if len(c.NetInclude) > 0 {
		return matchAny(c.NetInclude, name)
	}
	ex := c.NetExclude
	if ex == nil {
		ex = c.Host.defaultNetExclude()
	}
	return !matchAny(ex, name)
}

func matchAny(patterns []string, name string) bool {
	for _, p := range patterns {
		if ok, _ := path.Match(p, name); ok {
			return true
		}
	}
	return false
}

// checkUsage 承载 used ≤ total。平台的已用量都是差或和（Linux 的 total − available，
// darwin 的页计数之和），计数彼此不一致或无符号减法回绕都会得出超过总量的值；
// 这样的读数按读不到处理并记日志，不截到总量——截断会把错误的计数伪装成满载。
func checkUsage(u usage, err error) (usage, error) {
	if err != nil {
		return usage{}, err
	}
	if u.used > u.total {
		return usage{}, fmt.Errorf("used %d exceeds total %d", u.used, u.total)
	}
	return u, nil
}

// Metrics 生成一次上报。任何一个来源读不到只让对应读数缺失，其余照常；
// 返回的 error 汇总了这些失败，供调用方记日志，不阻止上报。
func (c *Collector) Metrics() (*probev1.Metrics, error) {
	m := &probev1.Metrics{}
	var errs []error
	fail := func(what string, err error) { errs = append(errs, fmt.Errorf("%s: %w", what, err)) }
	h := c.Host

	if id, err := h.bootID(); err == nil {
		m.BootId = id
	} else {
		fail("boot_id", err)
	}

	if cur, err := h.cpuTimes(); err == nil {
		if c.prevCPU != nil {
			if pct, ok := cpuPercent(*c.prevCPU, cur); ok {
				m.CpuPct = proto.Float64(pct)
			}
		}
		c.prevCPU = &cur
	} else {
		fail("cpu", err)
	}

	if u, err := checkUsage(h.memory()); err == nil {
		m.MemTotal, m.MemUsed = proto.Uint64(u.total), proto.Uint64(u.used)
	} else {
		fail("memory", err)
	}
	if u, err := checkUsage(h.swap()); err == nil {
		m.SwapTotal, m.SwapUsed = proto.Uint64(u.total), proto.Uint64(u.used)
	} else {
		fail("swap", err)
	}
	if u, err := checkUsage(h.disk()); err == nil {
		m.DiskTotal, m.DiskUsed = proto.Uint64(u.total), proto.Uint64(u.used)
	} else {
		fail("disk", err)
	}

	if l, err := h.load(); err == nil {
		m.Load1, m.Load5, m.Load15 = proto.Float64(l.l1), proto.Float64(l.l5), proto.Float64(l.l15)
	} else {
		fail("load", err)
	}
	if n, err := h.procs(); err == nil {
		m.Procs = proto.Uint32(n)
	} else {
		fail("procs", err)
	}
	if up, err := h.uptime(); err == nil {
		m.UptimeS = proto.Uint64(up)
	} else {
		fail("uptime", err)
	}
	if tcp, udp, err := h.conns(); err == nil {
		m.TcpConns, m.UdpConns = proto.Uint32(tcp), proto.Uint32(udp)
	} else {
		fail("conns", err)
	}

	if sum, err := c.netTotals(); err == nil {
		m.NetRxTotal, m.NetTxTotal = proto.Uint64(sum.rx), proto.Uint64(sum.tx)
		now := c.Clock.Mono()
		if c.prevNet != nil && now > c.prevNetT && sum.rx >= c.prevNet.rx && sum.tx >= c.prevNet.tx {
			secs := float64(now-c.prevNetT) / float64(time.Second)
			m.NetRxBps = proto.Uint64(uint64(float64(sum.rx-c.prevNet.rx) / secs))
			m.NetTxBps = proto.Uint64(uint64(float64(sum.tx-c.prevNet.tx) / secs))
		}
		c.prevNet, c.prevNetT = &sum, now
	} else {
		fail("net", err)
	}

	return m, errors.Join(errs...)
}

func (c *Collector) netTotals() (netCounters, error) {
	ifs, err := c.Host.ifaces(c.includeIface)
	if err != nil {
		return netCounters{}, err
	}
	if len(ifs) == 0 {
		return netCounters{}, errors.New("no interface with readable counters")
	}
	var sum netCounters
	for _, i := range ifs {
		sum.rx, sum.tx = sum.rx+i.rx, sum.tx+i.tx
	}
	return sum, nil
}

// Facts 收集静态信息；读不到的字段留空，由 hub 侧展示为未知。
// arch 取本二进制的 GOARCH：与发布资产名、安装脚本的架构名同一套词汇，两个平台一致。
func (c *Collector) Facts() *probev1.Facts {
	hf := c.Host.facts()
	f := &probev1.Facts{
		Hostname: hf.hostname, Os: hf.os, Kernel: hf.kernel, Arch: runtime.GOARCH,
		Virtualization: hf.virtualization, CpuModel: hf.cpuModel, CpuCores: hf.cpuCores,
		AgentVersion: c.Version, IcmpAvailable: c.IcmpAvailable,
	}
	if f.CpuCores == 0 {
		f.CpuCores = uint32(runtime.NumCPU())
	}
	return f
}
```

`internal/agent/collect/procfs.go`：

```go
package collect

import (
	"errors"
	"fmt"
	"io/fs"
)

// linuxNetExclude 是回环与常见的虚拟桥接口（spec §7）。
var linuxNetExclude = []string{"lo", "docker*", "veth*", "br-*", "virbr*"}

// ProcFS 从 Linux 的 /proc 与 /sys 取原始读数。
type ProcFS struct {
	// FS 是主机根文件系统；路径相对根，如 "proc/stat"。
	FS fs.FS
	// DiskUsage 取根分区的总量与已用量；statfs 是系统调用，由平台文件注入。
	DiskUsage func(path string) (total, used uint64, err error)
}

func (p *ProcFS) bootID() (string, error) { return readTrim(p.FS, "proc/sys/kernel/random/boot_id") }

func (p *ProcFS) cpuTimes() (cpuTimes, error) {
	f, err := p.FS.Open("proc/stat")
	if err != nil {
		return cpuTimes{}, err
	}
	defer f.Close()
	return parseStat(f)
}

func (p *ProcFS) meminfo() (memInfo, error) {
	f, err := p.FS.Open("proc/meminfo")
	if err != nil {
		return memInfo{}, err
	}
	defer f.Close()
	return parseMeminfo(f)
}

// memory 按 total − MemAvailable 计已用：页缓存与可回收的 slab 不算占用。
// MemAvailable 大于 MemTotal 时差值回绕，由 Collector 的 checkUsage 拒收。
func (p *ProcFS) memory() (usage, error) {
	mi, err := p.meminfo()
	if err != nil {
		return usage{}, err
	}
	return usage{total: mi.total, used: mi.total - mi.available}, nil
}

func (p *ProcFS) swap() (usage, error) {
	mi, err := p.meminfo()
	if err != nil {
		return usage{}, err
	}
	return usage{total: mi.swapTotal, used: mi.swapTotal - mi.swapFree}, nil
}

func (p *ProcFS) disk() (usage, error) {
	total, used, err := p.DiskUsage("/")
	return usage{total: total, used: used}, err
}

func (p *ProcFS) loadavg() (loadAvg, uint32, error) {
	f, err := p.FS.Open("proc/loadavg")
	if err != nil {
		return loadAvg{}, 0, err
	}
	defer f.Close()
	return parseLoadavg(f)
}

func (p *ProcFS) load() (loadAvg, error) {
	l, _, err := p.loadavg()
	return l, err
}

func (p *ProcFS) procs() (uint32, error) {
	_, n, err := p.loadavg()
	return n, err
}

func (p *ProcFS) uptime() (uint64, error) {
	f, err := p.FS.Open("proc/uptime")
	if err != nil {
		return 0, err
	}
	defer f.Close()
	return parseUptime(f)
}

// conns 合计 IPv4 与 IPv6 两张表；两张都读不到才算没有读数。
func (p *ProcFS) conns() (uint32, uint32, error) {
	var tcp, udp uint32
	var errs []error
	got := false
	for _, name := range []string{"proc/net/sockstat", "proc/net/sockstat6"} {
		f, err := p.FS.Open(name)
		if err != nil {
			continue
		}
		t, u, perr := parseSockstat(f)
		f.Close()
		if perr != nil {
			errs = append(errs, fmt.Errorf("%s: %w", name, perr))
			continue
		}
		tcp, udp, got = tcp+t, udp+u, true
	}
	if !got {
		return 0, 0, errors.Join(append(errs, fs.ErrNotExist)...)
	}
	return tcp, udp, nil
}

// ifaces 从 /sys/class/net/<if>/statistics 读计数器；每个网卡一对文件，
// 比 /proc/net/dev 少一次整表解析，且缺某块网卡时不影响其余。
func (p *ProcFS) ifaces(include func(string) bool) ([]ifaceCounters, error) {
	entries, err := fs.ReadDir(p.FS, "sys/class/net")
	if err != nil {
		return nil, err
	}
	var out []ifaceCounters
	for _, e := range entries {
		if !include(e.Name()) {
			continue
		}
		base := "sys/class/net/" + e.Name() + "/statistics/"
		rx, err1 := readUint(p.FS, base+"rx_bytes")
		tx, err2 := readUint(p.FS, base+"tx_bytes")
		if err1 != nil || err2 != nil {
			continue
		}
		out = append(out, ifaceCounters{name: e.Name(), rx: rx, tx: tx})
	}
	return out, nil
}

func (p *ProcFS) defaultNetExclude() []string { return linuxNetExclude }

func (p *ProcFS) facts() hostFacts {
	var f hostFacts
	f.hostname, _ = readTrim(p.FS, "proc/sys/kernel/hostname")
	f.kernel, _ = readTrim(p.FS, "proc/sys/kernel/osrelease")
	if r, err := p.FS.Open("etc/os-release"); err == nil {
		f.os = parseOSRelease(r)
		r.Close()
	}
	if r, err := p.FS.Open("proc/cpuinfo"); err == nil {
		ci := parseCPUInfo(r)
		r.Close()
		f.cpuModel, f.cpuCores = ci.model, ci.cores
	}
	f.virtualization = detectVirtualization(p.FS)
	return f
}

func readUint(fsys fs.FS, name string) (uint64, error) {
	s, err := readTrim(fsys, name)
	if err != nil {
		return 0, err
	}
	var v uint64
	_, err = fmt.Sscan(s, &v)
	return v, err
}
```

`internal/agent/collect/parse.go`（包文档移到 `host.go`；`loadAvg` 不再带进程数）：

```diff
diff --git a/internal/agent/collect/parse.go b/internal/agent/collect/parse.go
--- a/internal/agent/collect/parse.go
+++ b/internal/agent/collect/parse.go
@@ -1,7 +1,3 @@
-// Package collect 读取 Linux 的 /proc 与 /sys 生成一次上报。
-//
-// 解析全部是对 fs.FS 的纯函数，不带 build tag：它们在任何平台上都能用真机
-// 抓来的快照测试。只有取根文件系统与 statfs 的几行在 platform_linux.go 里。
 package collect
 
 import (
@@ -101,40 +97,37 @@ func parseMeminfo(r io.Reader) (memInfo, error) {
 	return m, nil
 }
 
-type loadAvg struct {
-	l1, l5, l15 float64
-	procs       uint32
-}
+type loadAvg struct{ l1, l5, l15 float64 }
 
-func parseLoadavg(r io.Reader) (loadAvg, error) {
+// parseLoadavg 同时给出负载三元组与第四列 running/total 里的进程总数。
+func parseLoadavg(r io.Reader) (loadAvg, uint32, error) {
 	b, err := io.ReadAll(r)
 	if err != nil {
-		return loadAvg{}, err
+		return loadAvg{}, 0, err
 	}
 	f := strings.Fields(string(b))
 	if len(f) < 4 {
-		return loadAvg{}, errors.New("/proc/loadavg: short")
+		return loadAvg{}, 0, errors.New("/proc/loadavg: short")
 	}
 	var l loadAvg
 	if l.l1, err = strconv.ParseFloat(f[0], 64); err != nil {
-		return l, err
+		return loadAvg{}, 0, err
 	}
 	if l.l5, err = strconv.ParseFloat(f[1], 64); err != nil {
-		return l, err
+		return loadAvg{}, 0, err
 	}
 	if l.l15, err = strconv.ParseFloat(f[2], 64); err != nil {
-		return l, err
+		return loadAvg{}, 0, err
 	}
 	_, total, ok := strings.Cut(f[3], "/")
 	if !ok {
-		return l, errors.New("/proc/loadavg: no running/total field")
+		return loadAvg{}, 0, errors.New("/proc/loadavg: no running/total field")
 	}
 	n, err := strconv.ParseUint(total, 10, 32)
 	if err != nil {
-		return l, err
+		return loadAvg{}, 0, err
 	}
-	l.procs = uint32(n)
-	return l, nil
+	return l, uint32(n), nil
 }
 
 func parseUptime(r io.Reader) (uint64, error) {
```

`internal/agent/collect/statfs_unix.go`（从 `platform_linux.go` 移来，darwin 用同一份）：

```go
//go:build linux || darwin

package collect

import "golang.org/x/sys/unix"

// statfs 按 df 的口径：total = 全部块，used = 全部块 − 空闲块（含 root 保留）。
// APFS 上同一容器内各卷的 statfs 给出的是容器的块数与空闲块，used 因此是整个容器的占用。
func statfs(path string) (uint64, uint64, error) {
	var st unix.Statfs_t
	if err := unix.Statfs(path, &st); err != nil {
		return 0, 0, err
	}
	bs := uint64(st.Bsize)
	return st.Blocks * bs, (st.Blocks - st.Bfree) * bs, nil
}
```

`internal/agent/collect/platform_linux.go` 整份替换：

```go
//go:build linux

package collect

import (
	"os"

	"github.com/xjetry/probe/internal/clock"
)

func NewPlatform(version string, clk clock.Clock, netInclude, netExclude []string) (*Collector, error) {
	return &Collector{Host: &ProcFS{FS: os.DirFS("/"), DiskUsage: statfs}, Clock: clk, NetInclude: netInclude, NetExclude: netExclude, Version: version}, nil
}

// DefaultNetExclude 是本平台未给 --net-exclude 时不计入流量的网卡。
func DefaultNetExclude() []string { return linuxNetExclude }
```

`internal/agent/collect/platform_other.go` 整份替换（build tag 仍是 `!linux`：darwin 的实现由 Task 3 加入，届时同时改成 `!linux && !darwin`；在那之前 darwin 上的 `NewPlatform` 照旧报未实现）：

```go
//go:build !linux

package collect

import (
	"errors"
	"runtime"

	"github.com/xjetry/probe/internal/clock"
)

func NewPlatform(string, clock.Clock, []string, []string) (*Collector, error) {
	return nil, errors.New("metrics collection is not implemented for " + runtime.GOOS)
}

// DefaultNetExclude 在不支持采集的平台上没有意义。
func DefaultNetExclude() []string { return nil }
```

`cmd/agent/main.go`：

```diff
diff --git a/cmd/agent/main.go b/cmd/agent/main.go
--- a/cmd/agent/main.go
+++ b/cmd/agent/main.go
@@ -82,7 +82,7 @@ func runRun(args []string) error {
 	fs := flag.NewFlagSet("run", flag.ContinueOnError)
 	cfgPath := fs.String("config", defaultConfig, "agent config path")
 	include := fs.String("net-include", "", "comma-separated interface globs to count (exclusive)")
-	exclude := fs.String("net-exclude", "", "comma-separated interface globs to skip (default: lo, docker*, veth*, br-*, virbr*)")
+	exclude := fs.String("net-exclude", "", "comma-separated interface globs to skip (default: "+strings.Join(collect.DefaultNetExclude(), ", ")+")")
 	if err := fs.Parse(args); err != nil {
 		return err
 	}
```

- [ ] **Step 4: 跑绿（本机、Linux 容器、两个 GOOS 的 vet）**

```sh
cd /Users/xjetry/work/vibe/probe-macos && CGO_ENABLED=0 go test -count=1 ./internal/agent/... ./cmd/agent/... > /tmp/m6mac-t1-green.log 2>&1; echo $?
cd /Users/xjetry/work/vibe/probe-macos && docker run --rm -v "$PWD:/src" -v "$(go env GOMODCACHE):/go/pkg/mod" -w /src -e CGO_ENABLED=0 -e GOFLAGS=-buildvcs=false golang:1.27.1-bookworm go test -count=1 ./internal/agent/... ./cmd/agent/... > /tmp/m6mac-t1-linux.log 2>&1; echo $?
cd /Users/xjetry/work/vibe/probe-macos && GOOS=linux go vet ./... > /tmp/m6mac-t1-vet-linux.log 2>&1; echo $?
cd /Users/xjetry/work/vibe/probe-macos && GOOS=darwin go vet ./... > /tmp/m6mac-t1-vet-darwin.log 2>&1; echo $?
```

Expected: 四个都是 `0`。容器一行在 Linux 上编译 `platform_linux.go` 并用 `/proc` 快照 fixture 跑全部既有用例，是"Linux 行为不变"的凭据；`golang:1.27.1-bookworm` 本地已有，没有时先 `docker pull golang:1.27.1-bookworm`。

- [ ] **Step 5: 缺陷注入**

1. 先 `git add internal/agent/collect` 建基准。`collect.go` 的 `checkUsage` 里把 `return usage{}, fmt.Errorf("used %d exceeds total %d", …)` 换成 `u.used = u.total`（截断而不拒收）。`git diff --stat` 非空；跑 `CGO_ENABLED=0 go test -count=1 -run TestUsageAboveTotalIsDroppedNotClamped ./internal/agent/collect > /tmp/m6mac-t1-inj1.log 2>&1; echo $?`，期望 `1`，日志含 `used above total must leave both readings unset`。`git checkout -- internal/agent/collect/collect.go`，`git diff --stat` 为空。
2. `procfs.go` 的 `ifaces` 删掉 `if !include(e.Name()) { continue }`（基准同上）。`git diff --stat` 非空；跑 `-run TestExcludedInterfacesAreNotSummed`，期望 `1`，日志含 `net = 121000/122000, want eth0 only`。`git checkout -- internal/agent/collect/procfs.go` 还原。

- [ ] **Step 6: make ci**

```sh
cd /Users/xjetry/work/vibe/probe-macos && make ci > /tmp/m6mac-t1-ci.log 2>&1; echo $?
```

Expected: `0`。

- [ ] **Step 7: 提交**

```sh
cd /Users/xjetry/work/vibe/probe-macos && git add internal/agent/collect cmd/agent/main.go internal/agent/client/client_test.go && git commit -m "agent: 采集按平台读数来源分层，差分、过滤与用量上限只在 Collector 实现一次"
```

---

### Task 2: darwin 读数的字节布局与组合（不带 build tag，Linux 上可测）

**Files:**
- Create: `internal/agent/collect/darwinraw.go`
- Test: `internal/agent/collect/darwinraw_test.go`

**Interfaces:**
- Consumes：Task 1 的 `Host`、`usage`、`ifaceCounters`、`hostFacts`、`loadAvg`、`cpuTimes`、`cpuPercent`、`Collector`、`checkUsage`。
- Produces（Task 3 实现并使用）：
  - `type darwinSource interface { sysctlString(name string) (string, error); sysctlUint32(name string) (uint32, error); sysctlUint64(name string) (uint64, error); sysctlRaw(name string, args ...int) ([]byte, error); statfs(path string) (total, used uint64, err error); monotonicSeconds() (uint64, error); processorTicks() ([]uint32, error); vmStatistics64() ([]byte, error); pageSize() (uint64, error); pidCount() (uint32, error) }`
  - `type darwinHost struct { src darwinSource; ticks tickAccumulator }`（实现 `Host`）；`var darwinNetExclude []string`
  - 常量 `cpuStateMax = 4`、`cpuStateIdle = 2`；`func (a *tickAccumulator) add(raw []uint32) (cpuTimes, error)`；`parseLoadavgSysctl`、`parseSwapUsage`、`vmUsed`、`parseIfmibData`
  - sysctl 名字（Task 3 的对照测试与之一致）：`kern.bootsessionuuid`、`hw.memsize`、`vm.swapusage`、`vm.loadavg`、`net.inet.tcp.pcbcount`、`net.inet.udp.pcbcount`、`net.link.generic.system.ifcount`、`net.link.generic.ifdata`（参数 `<index>, 1`）、`kern.hostname`、`kern.osproductversion`、`kern.osrelease`、`machdep.cpu.brand_string`、`hw.logicalcpu`、`kern.hv_vmm_present`

测试以按 SDK 头文件布局构造的字节替换系统调用层：`vmBytes` 把没读的字段填成各不相同的哨兵，偏移错一位就读到哨兵；`ifmibBytes` 把相邻的包计数写成别的值；en0 的字节数取 2^32+1025，32 位截断或 KiB 取整的来源都得不出它（Review Focus 1）。字节布局对真机内核的正确性由 Task 3 的对照测试承载。

- [ ] **Step 1: 写失败的测试**

`internal/agent/collect/darwinraw_test.go`：

```go
package collect

import (
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/xjetry/probe/internal/clock"
)

// fakeDarwin 按头文件布局构造字节；键是 sysctl 名，ifdata 以 "net.link.generic.ifdata/<idx>" 为键。
type fakeDarwin struct {
	strs  map[string]string
	u32   map[string]uint32
	u64   map[string]uint64
	raw   map[string][]byte
	errs  map[string]error
	ticks [][]uint32 // 每次 processorTicks 取下一份
	vm    []byte
	page  uint64
	pids  uint32
	disk  usage
	up    uint64
}

func (f *fakeDarwin) err(name string) error {
	if e, ok := f.errs[name]; ok {
		return e
	}
	return nil
}

func (f *fakeDarwin) sysctlString(n string) (string, error) {
	if e := f.err(n); e != nil {
		return "", e
	}
	v, ok := f.strs[n]
	if !ok {
		return "", syscall.ENOENT
	}
	return v, nil
}

func (f *fakeDarwin) sysctlUint32(n string) (uint32, error) {
	if e := f.err(n); e != nil {
		return 0, e
	}
	v, ok := f.u32[n]
	if !ok {
		return 0, syscall.ENOENT
	}
	return v, nil
}

func (f *fakeDarwin) sysctlUint64(n string) (uint64, error) {
	if e := f.err(n); e != nil {
		return 0, e
	}
	v, ok := f.u64[n]
	if !ok {
		return 0, syscall.ENOENT
	}
	return v, nil
}

func (f *fakeDarwin) sysctlRaw(n string, args ...int) ([]byte, error) {
	key := n
	if n == "net.link.generic.ifdata" {
		key = fmt.Sprintf("%s/%d", n, args[0])
	}
	if e := f.err(key); e != nil {
		return nil, e
	}
	v, ok := f.raw[key]
	if !ok {
		return nil, syscall.ENOENT
	}
	return v, nil
}

func (f *fakeDarwin) statfs(string) (uint64, uint64, error) { return f.disk.total, f.disk.used, nil }
func (f *fakeDarwin) monotonicSeconds() (uint64, error)     { return f.up, nil }
func (f *fakeDarwin) processorTicks() ([]uint32, error) {
	if len(f.ticks) == 0 {
		return nil, errors.New("no ticks")
	}
	t := f.ticks[0]
	f.ticks = f.ticks[1:]
	return t, nil
}
func (f *fakeDarwin) vmStatistics64() ([]byte, error) { return f.vm, nil }
func (f *fakeDarwin) pageSize() (uint64, error)       { return f.page, nil }
func (f *fakeDarwin) pidCount() (uint32, error)       { return f.pids, nil }

var le = binary.LittleEndian

func loadavgBytes(l1, l5, l15 uint32, fscale int64) []byte {
	b := make([]byte, sizeofLoadavg)
	le.PutUint32(b[0:], l1)
	le.PutUint32(b[4:], l5)
	le.PutUint32(b[8:], l15)
	le.PutUint64(b[16:], uint64(fscale))
	return b
}

func swapBytes(total, avail, used uint64) []byte {
	b := make([]byte, sizeofXswUsage)
	le.PutUint64(b[0:], total)
	le.PutUint64(b[8:], avail)
	le.PutUint64(b[16:], used)
	le.PutUint32(b[24:], 16384)
	le.PutUint32(b[28:], 1)
	return b
}

// vmBytes 只填 vmUsed 读的四个字段，其余字段写入不同的哨兵值：偏移错一位就读到哨兵。
func vmBytes(wire, purgeable, compressor, internal uint32) []byte {
	b := make([]byte, 152)
	for off := 0; off+4 <= len(b); off += 4 {
		le.PutUint32(b[off:], 0x5A5A0000|uint32(off))
	}
	le.PutUint32(b[12:], wire)
	le.PutUint32(b[88:], purgeable)
	le.PutUint32(b[128:], compressor)
	le.PutUint32(b[140:], internal)
	return b
}

// ifmibBytes 的 ifi_ipackets(24)、ifi_opackets(40) 与字节计数相邻，写成不同的值以抓偏移错误。
func ifmibBytes(name string, rx, tx uint64) []byte {
	b := make([]byte, 180)
	copy(b, name)
	d := b[ifmibDataOffset:]
	d[0] = 6 // IFT_ETHER
	le.PutUint64(d[24:], 11)
	le.PutUint64(d[40:], 22)
	le.PutUint64(d[64:], rx)
	le.PutUint64(d[72:], tx)
	le.PutUint64(d[80:], 33)
	return b
}

func newFakeDarwin() *fakeDarwin {
	return &fakeDarwin{
		strs: map[string]string{
			"kern.bootsessionuuid": "DEF2AAD6-739B-4A47-AC3D-8D8AB38FE277", "kern.hostname": "mac.local",
			"kern.osproductversion": "26.3.1", "kern.osrelease": "25.3.0", "machdep.cpu.brand_string": "Apple M4 Max",
		},
		u32: map[string]uint32{
			"net.inet.tcp.pcbcount": 614, "net.inet.udp.pcbcount": 94, "hw.logicalcpu": 16, "kern.hv_vmm_present": 0,
			"net.link.generic.system.ifcount": 4,
		},
		u64: map[string]uint64{"hw.memsize": 1 << 37},
		raw: map[string][]byte{
			"vm.loadavg":                loadavgBytes(3072, 2048, 1024, 2048),
			"vm.swapusage":              swapBytes(2<<30, 1<<30, 1<<30),
			"net.link.generic.ifdata/1": ifmibBytes("lo0", 9<<32, 9<<32),
			// 大于 2^32 且不是 1024 的倍数：32 位截断或 KiB 取整的来源都得不出这两个值。
			"net.link.generic.ifdata/2": ifmibBytes("en0", 1<<32+1025, 40_156_680_979),
			"net.link.generic.ifdata/4": ifmibBytes("utun0", 5, 5),
		},
		ticks: [][]uint32{
			{100, 50, 800, 0, 100, 50, 800, 0},
			{150, 50, 850, 0, 150, 50, 850, 0},
		},
		vm:   vmBytes(400, 30, 70, 5000),
		page: 16384,
		pids: 1566,
		disk: usage{total: 994662584320, used: 781134274560},
		up:   245955,
	}
}

func TestDarwinMetricsFromSyscallLayout(t *testing.T) {
	c := &Collector{Host: &darwinHost{src: newFakeDarwin()}, Clock: clock.NewFake(time.Unix(0, 0))}
	m, err := c.Metrics()
	if err != nil {
		t.Fatalf("unexpected read failures: %v", err)
	}
	if m.GetBootId() != "DEF2AAD6-739B-4A47-AC3D-8D8AB38FE277" || m.CpuPct != nil {
		t.Fatalf("boot_id %q cpu_pct %v", m.GetBootId(), m.CpuPct)
	}
	if m.GetMemTotal() != 1<<37 || m.GetMemUsed() != (5000-30+400+70)*16384 {
		t.Fatalf("mem = %d/%d, want total 2^37 and (internal-purgeable+wire+compressor)*page", m.GetMemUsed(), m.GetMemTotal())
	}
	if m.GetSwapTotal() != 2<<30 || m.GetSwapUsed() != 1<<30 {
		t.Fatalf("swap = %d/%d", m.GetSwapUsed(), m.GetSwapTotal())
	}
	if m.GetLoad1() != 1.5 || m.GetLoad5() != 1 || m.GetLoad15() != 0.5 {
		t.Fatalf("load = %v %v %v, want ldavg/fscale", m.GetLoad1(), m.GetLoad5(), m.GetLoad15())
	}
	if m.GetDiskTotal() != 994662584320 || m.GetDiskUsed() != 781134274560 || m.GetProcs() != 1566 || m.GetUptimeS() != 245955 {
		t.Fatalf("disk/procs/uptime: %+v", m)
	}
	if m.GetTcpConns() != 614 || m.GetUdpConns() != 94 {
		t.Fatalf("conns = %d/%d", m.GetTcpConns(), m.GetUdpConns())
	}
	// lo0 与 utun0 被默认排除，索引 3 缺号被跳过，只剩 en0。
	if m.GetNetRxTotal() != 1<<32+1025 || m.GetNetTxTotal() != 40_156_680_979 {
		t.Fatalf("net = %d/%d, want en0's 64-bit counters only", m.GetNetRxTotal(), m.GetNetTxTotal())
	}
	m, _ = c.Metrics()
	// 两个 CPU 各 +100 tick，其中各 50 空闲。
	if m.CpuPct == nil || m.GetCpuPct() != 50 {
		t.Fatalf("second cpu_pct = %v, want 50", m.CpuPct)
	}
}

func TestDarwinDefaultExcludeKeepsOnlyPhysicalInterfaces(t *testing.T) {
	c := &Collector{Host: &darwinHost{}}
	for _, n := range []string{"lo0", "gif0", "stf0", "utun0", "utun1024", "ipsec0", "bridge0", "bridge100", "vmenet0", "awdl0", "llw0", "anpi0", "ap1"} {
		if c.includeIface(n) {
			t.Errorf("%s must be excluded by default", n)
		}
	}
	for _, n := range []string{"en0", "en1", "en10"} {
		if !c.includeIface(n) {
			t.Errorf("%s must be included by default", n)
		}
	}
}

// 缺号之外的错误说明内核拒绝了一个存在的索引：合计会少一块网卡，读数整体缺失而不是少算。
func TestDarwinInterfaceReadErrorDropsTheWholeReading(t *testing.T) {
	f := newFakeDarwin()
	f.errs = map[string]error{"net.link.generic.ifdata/3": syscall.EPERM}
	c := &Collector{Host: &darwinHost{src: f}, Clock: clock.NewFake(time.Unix(0, 0))}
	m, err := c.Metrics()
	if m.NetRxTotal != nil || m.NetTxTotal != nil {
		t.Fatalf("partial interface set must not be reported: %v/%v", m.NetRxTotal, m.NetTxTotal)
	}
	if err == nil || !strings.Contains(err.Error(), "ifdata 3") {
		t.Fatalf("err = %v, want the failing index named", err)
	}
}

func TestTickAccumulatorAcrossWrapAndCPUCountChange(t *testing.T) {
	var a tickAccumulator
	first, err := a.add([]uint32{10, 0, math.MaxUint32 - 19, 0})
	if err != nil {
		t.Fatal(err)
	}
	// idle 从 2^32−20 回绕到 30：增量 50；user +50。
	cur, _ := a.add([]uint32{60, 0, 30, 0})
	if pct, ok := cpuPercent(first, cur); !ok || pct != 50 {
		t.Fatalf("pct across wrap = %v,%v, want 50 (50 busy of 100)", pct, ok)
	}
	// CPU 个数变化：累计值不动，没有读数。
	next, _ := a.add([]uint32{30, 0, 40, 0, 1, 1, 1, 1})
	if _, ok := cpuPercent(cur, next); ok {
		t.Fatal("cpu count change must yield no reading")
	}
	if _, err := a.add([]uint32{1, 2, 3}); err == nil {
		t.Fatal("ticks not a multiple of cpuStateMax must be an error")
	}
}

func TestVMUsedRejectsInconsistentCounts(t *testing.T) {
	if used, err := vmUsed(vmBytes(400, 900, 70, 500), 4096); err == nil {
		t.Fatalf("purgeable above internal must be an error, got %d", used)
	}
	if _, err := vmUsed(make([]byte, vmStatsMinLen-1), 4096); err == nil {
		t.Fatal("short vm_statistics64 must be an error")
	}
}

func TestDarwinLayoutsRejectWrongSizes(t *testing.T) {
	if _, err := parseLoadavgSysctl(loadavgBytes(1, 1, 1, 0)); err == nil {
		t.Fatal("fscale 0 must be an error, not +Inf")
	}
	if _, err := parseLoadavgSysctl(make([]byte, 20)); err == nil {
		t.Fatal("short loadavg must be an error")
	}
	if _, err := parseSwapUsage(make([]byte, 24)); err == nil {
		t.Fatal("short xsw_usage must be an error")
	}
	if _, err := parseIfmibData(make([]byte, ifmibMinLen)); err == nil {
		t.Fatal("empty interface name must be an error")
	}
}

func TestDarwinFacts(t *testing.T) {
	f := newFakeDarwin()
	f.u32["kern.hv_vmm_present"] = 1
	got := (&Collector{Host: &darwinHost{src: f}, Version: "v"}).Facts()
	if got.GetOs() != "macOS 26.3.1" || got.GetKernel() != "25.3.0" || got.GetCpuModel() != "Apple M4 Max" ||
		got.GetCpuCores() != 16 || got.GetHostname() != "mac.local" || got.GetVirtualization() != "vm" {
		t.Fatalf("%+v", got)
	}
	delete(f.u32, "kern.hv_vmm_present")
	if v := (&darwinHost{src: f}).facts().virtualization; v != "" {
		t.Fatalf("missing kern.hv_vmm_present must not claim a VM: %q", v)
	}
}
```

- [ ] **Step 2: 跑红**

```sh
cd /Users/xjetry/work/vibe/probe-macos && CGO_ENABLED=0 go test -count=1 ./internal/agent/collect > /tmp/m6mac-t2-red.log 2>&1; echo $?
```

Expected: `1`；日志含 `undefined: sizeofLoadavg` 与 `undefined: darwinHost`。

- [ ] **Step 3: 实现**

`internal/agent/collect/darwinraw.go`：

```go
package collect

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io/fs"
	"strings"
)

// darwinNetExclude 与 Linux 的默认列表同一原则：回环，以及流量同时计在物理网口上或不出本机的接口。
// 隧道 gif、stf、utun、ipsec（VPN 的字节也计在承载它的网口上），桥与虚拟机网卡 bridge、vmenet，
// Wi-Fi 上的点对点接口 awdl、llw，以及 anpi、ap。以太网、Wi-Fi 与雷雳网口都叫 en*，不在此列。
var darwinNetExclude = []string{"lo*", "gif*", "stf*", "utun*", "ipsec*", "bridge*", "vmenet*", "awdl*", "llw*", "anpi*", "ap*"}

// 下列布局取自 macOS SDK：<sys/sysctl.h> 的 loadavg 与 xsw_usage、<mach/vm_statistics.h> 的
// vm_statistics64、<net/if_mib.h> 的 ifmibdata 与 <net/if_var.h> 的 if_data64（#pragma pack(4)）。darwin 的两个目标
// amd64 与 arm64 都是小端，字段按小端解码。
const (
	sizeofLoadavg   = 24  // struct loadavg { fixpt_t ldavg[3]; long fscale; }
	sizeofXswUsage  = 32  // struct xsw_usage { u_int64_t total, avail, used; u_int32_t pagesize; boolean_t encrypted; }
	vmStatsMinLen   = 144 // vm_statistics64 读到 internal_page_count 为止
	ifmibDataOffset = 52  // struct ifmibdata 里 if_data64 的偏移
	ifmibMinLen     = ifmibDataOffset + 80
	ifNameSize      = 16 // IFNAMSIZ

	// PROCESSOR_CPU_LOAD_INFO 每个 CPU 一组 natural_t：CPU_STATE_USER、SYSTEM、IDLE、NICE。
	cpuStateMax  = 4
	cpuStateIdle = 2
)

// darwinSource 是 darwin 取数的系统调用层，由 platform_darwin.go 实现；
// 测试以按头文件布局构造的字节替换它。
type darwinSource interface {
	sysctlString(name string) (string, error)
	sysctlUint32(name string) (uint32, error)
	sysctlUint64(name string) (uint64, error)
	sysctlRaw(name string, args ...int) ([]byte, error)
	statfs(path string) (total, used uint64, err error)
	// monotonicSeconds 是自启动起的秒数，含睡眠（CLOCK_MONOTONIC），与 Linux /proc/uptime 同口径。
	monotonicSeconds() (uint64, error)
	// processorTicks 是 host_processor_info 的逐 CPU 计数，每 CPU cpuStateMax 个。
	processorTicks() ([]uint32, error)
	// vmStatistics64 是 host_statistics64(HOST_VM_INFO64) 的原始字节。
	vmStatistics64() ([]byte, error)
	// pageSize 是 vmStatistics64 页计数的单位（host_page_size）。
	pageSize() (uint64, error)
	pidCount() (uint32, error)
}

type darwinHost struct {
	src   darwinSource
	ticks tickAccumulator
}

// bootID 用 kern.bootsessionuuid：开机时生成（统一日志的 "system boot" 行记的就是它），睡眠唤醒不变。
// hub 靠它区分"同一次开机内 agent 重启"（接着差分）与"机器重启"（只换基线）（spec §7）。
// kern.bootuuid 与 kern.apfsprebootuuid 相同，是 preboot 卷的标识而不是这次开机的，不能用。
func (d *darwinHost) bootID() (string, error) {
	id, err := d.src.sysctlString("kern.bootsessionuuid")
	if err != nil {
		return "", err
	}
	if id == "" {
		return "", errors.New("kern.bootsessionuuid is empty")
	}
	return id, nil
}

func (d *darwinHost) cpuTimes() (cpuTimes, error) {
	raw, err := d.src.processorTicks()
	if err != nil {
		return cpuTimes{}, err
	}
	return d.ticks.add(raw)
}

// memory 的已用量 = 匿名页 − 可清除页 + 联动页 + 压缩器占用页。文件缓存与可清除内存随时可回收，
// 不算占用，与 Linux 的 total − MemAvailable 同义。
func (d *darwinHost) memory() (usage, error) {
	total, err := d.src.sysctlUint64("hw.memsize")
	if err != nil {
		return usage{}, err
	}
	raw, err := d.src.vmStatistics64()
	if err != nil {
		return usage{}, err
	}
	page, err := d.src.pageSize()
	if err != nil {
		return usage{}, err
	}
	used, err := vmUsed(raw, page)
	if err != nil {
		return usage{}, err
	}
	return usage{total: total, used: used}, nil
}

func (d *darwinHost) swap() (usage, error) {
	b, err := d.src.sysctlRaw("vm.swapusage")
	if err != nil {
		return usage{}, err
	}
	return parseSwapUsage(b)
}

func (d *darwinHost) disk() (usage, error) {
	total, used, err := d.src.statfs("/")
	return usage{total: total, used: used}, err
}

func (d *darwinHost) load() (loadAvg, error) {
	b, err := d.src.sysctlRaw("vm.loadavg")
	if err != nil {
		return loadAvg{}, err
	}
	return parseLoadavgSysctl(b)
}

func (d *darwinHost) procs() (uint32, error) { return d.src.pidCount() }

func (d *darwinHost) uptime() (uint64, error) { return d.src.monotonicSeconds() }

// conns 是内核里 TCP、UDP 协议控制块的个数，与 Linux sockstat 的 inuse 同为内核计数口径。
func (d *darwinHost) conns() (uint32, uint32, error) {
	tcp, err := d.src.sysctlUint32("net.inet.tcp.pcbcount")
	if err != nil {
		return 0, 0, err
	}
	udp, err := d.src.sysctlUint32("net.inet.udp.pcbcount")
	if err != nil {
		return 0, 0, err
	}
	return tcp, udp, nil
}

// ifaces 读 net.link.generic.ifdata.<index>.IFDATA_GENERAL：它给出 64 位、不取整的字节计数。
// 同一台机器上，普通用户进程经 NET_RT_IFLIST2 读到的 if_data64 字节数截到了 32 位并按 1 KiB 取整；
// 累计值过 4 GiB 就回绕，hub 把变小的计数当成重置、只换基线不入账（spec §7），流量就丢了。
// 索引空间有空洞（实测 ifcount 为 35 时 23、34、35 号不存在），缺号返回 ENOENT，跳过。
// 其余错误让整个读数缺失：少一块网卡的合计会先变小、恢复时再把那块网卡的全部历史计数当增量加回去；
// 缺失的读数则让 hub 保持基线不动（spec §7）。
func (d *darwinHost) ifaces(include func(string) bool) ([]ifaceCounters, error) {
	n, err := d.src.sysctlUint32("net.link.generic.system.ifcount")
	if err != nil {
		return nil, err
	}
	var out []ifaceCounters
	for idx := 1; idx <= int(n); idx++ {
		b, err := d.src.sysctlRaw("net.link.generic.ifdata", idx, 1) // IFDATA_GENERAL
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("ifdata %d: %w", idx, err)
		}
		ic, err := parseIfmibData(b)
		if err != nil {
			return nil, fmt.Errorf("ifdata %d: %w", idx, err)
		}
		if include(ic.name) {
			out = append(out, ic)
		}
	}
	return out, nil
}

func (d *darwinHost) defaultNetExclude() []string { return darwinNetExclude }

func (d *darwinHost) facts() hostFacts {
	var f hostFacts
	f.hostname, _ = d.src.sysctlString("kern.hostname")
	if v, err := d.src.sysctlString("kern.osproductversion"); err == nil && v != "" {
		f.os = "macOS " + v
	}
	f.kernel, _ = d.src.sysctlString("kern.osrelease")
	f.cpuModel, _ = d.src.sysctlString("machdep.cpu.brand_string")
	f.cpuCores, _ = d.src.sysctlUint32("hw.logicalcpu")
	if v, err := d.src.sysctlUint32("kern.hv_vmm_present"); err == nil && v == 1 {
		f.virtualization = "vm"
	}
	return f
}

// tickAccumulator 把逐 CPU 的 natural_t 计数折成单调不减的 64 位累计值。
// 单个计数是 uint32，每 CPU 每秒 hz（100）个 tick，约 497 天回绕一次；
// 按 uint32 相减得到的增量在一次回绕之内总是对的，再累加进 64 位和。
// CPU 个数变化时没有可对齐的上一份：只换基线、累计值不动，
// Collector 见 total 未增长就不给 cpu_pct。
type tickAccumulator struct {
	last []uint32
	sum  cpuTimes
}

func (a *tickAccumulator) add(raw []uint32) (cpuTimes, error) {
	if len(raw) == 0 || len(raw)%cpuStateMax != 0 {
		return cpuTimes{}, fmt.Errorf("processor ticks: %d values, want a positive multiple of %d", len(raw), cpuStateMax)
	}
	if len(a.last) == len(raw) {
		for i, v := range raw {
			d := uint64(v - a.last[i])
			a.sum.total += d
			if i%cpuStateMax == cpuStateIdle {
				a.sum.idle += d
			}
		}
	}
	a.last = append(a.last[:0], raw...)
	return a.sum, nil
}

func parseLoadavgSysctl(b []byte) (loadAvg, error) {
	if len(b) != sizeofLoadavg {
		return loadAvg{}, fmt.Errorf("vm.loadavg: %d bytes, want %d", len(b), sizeofLoadavg)
	}
	le := binary.LittleEndian
	scale := float64(int64(le.Uint64(b[16:])))
	if scale <= 0 {
		return loadAvg{}, fmt.Errorf("vm.loadavg: fscale %v", scale)
	}
	return loadAvg{
		l1:  float64(le.Uint32(b[0:])) / scale,
		l5:  float64(le.Uint32(b[4:])) / scale,
		l15: float64(le.Uint32(b[8:])) / scale,
	}, nil
}

func parseSwapUsage(b []byte) (usage, error) {
	if len(b) != sizeofXswUsage {
		return usage{}, fmt.Errorf("vm.swapusage: %d bytes, want %d", len(b), sizeofXswUsage)
	}
	return usage{total: binary.LittleEndian.Uint64(b[0:]), used: binary.LittleEndian.Uint64(b[16:])}, nil
}

// vmUsed 取 vm_statistics64 的 wire_count(12)、purgeable_count(88)、
// compressor_page_count(128)、internal_page_count(140)。可清除页是匿名页的子集；
// 读到前者大于后者说明这份计数不自洽，与 checkUsage 同一口径按读不到处理，不截成 0。
func vmUsed(b []byte, page uint64) (uint64, error) {
	if len(b) < vmStatsMinLen {
		return 0, fmt.Errorf("vm_statistics64: %d bytes, want at least %d", len(b), vmStatsMinLen)
	}
	if page == 0 {
		return 0, errors.New("vm_statistics64: page size is 0")
	}
	le := binary.LittleEndian
	wire := uint64(le.Uint32(b[12:]))
	purgeable := uint64(le.Uint32(b[88:]))
	compressor := uint64(le.Uint32(b[128:]))
	internal := uint64(le.Uint32(b[140:]))
	if purgeable > internal {
		return 0, fmt.Errorf("vm_statistics64: purgeable %d exceeds internal %d", purgeable, internal)
	}
	return (internal - purgeable + wire + compressor) * page, nil
}

// parseIfmibData 取 struct ifmibdata 的 ifmd_name 与 if_data64 的 ifi_ibytes(64)、ifi_obytes(72)。
func parseIfmibData(b []byte) (ifaceCounters, error) {
	if len(b) < ifmibMinLen {
		return ifaceCounters{}, fmt.Errorf("ifmibdata: %d bytes, want at least %d", len(b), ifmibMinLen)
	}
	name, _, _ := strings.Cut(string(b[:ifNameSize]), "\x00")
	if name == "" {
		return ifaceCounters{}, errors.New("ifmibdata: empty interface name")
	}
	d := b[ifmibDataOffset:]
	return ifaceCounters{name: name, rx: binary.LittleEndian.Uint64(d[64:]), tx: binary.LittleEndian.Uint64(d[72:])}, nil
}
```

- [ ] **Step 4: 跑绿（本机与 Linux 容器）**

```sh
cd /Users/xjetry/work/vibe/probe-macos && CGO_ENABLED=0 go test -count=1 ./internal/agent/collect > /tmp/m6mac-t2-green.log 2>&1; echo $?
cd /Users/xjetry/work/vibe/probe-macos && docker run --rm -v "$PWD:/src" -v "$(go env GOMODCACHE):/go/pkg/mod" -w /src -e CGO_ENABLED=0 -e GOFLAGS=-buildvcs=false golang:1.27.1-bookworm go test -count=1 -v -run 'Darwin|Tick|VMUsed' ./internal/agent/collect > /tmp/m6mac-t2-linux.log 2>&1; echo $?
```

Expected: 两个都是 `0`；容器日志里 `TestDarwinMetricsFromSyscallLayout`、`TestDarwinDefaultExcludeKeepsOnlyPhysicalInterfaces`、`TestDarwinInterfaceReadErrorDropsTheWholeReading`、`TestTickAccumulatorAcrossWrapAndCPUCountChange`、`TestVMUsedRejectsInconsistentCounts`、`TestDarwinLayoutsRejectWrongSizes`、`TestDarwinFacts` 七个都是 `--- PASS`（证明它们确实在 Linux 上跑了）。

- [ ] **Step 5: 缺陷注入**

每条先 `git add internal/agent/collect/darwinraw.go` 建基准，注入后 `git diff --stat` 非空，跑 `CGO_ENABLED=0 go test -count=1 -run '<用例>' ./internal/agent/collect > /tmp/m6mac-t2-injN.log 2>&1; echo $?` 期望 `1`，`git checkout -- internal/agent/collect/darwinraw.go` 还原：

1. `parseIfmibData` 的两个 `binary.LittleEndian.Uint64(…)` 各 `& 0xFFFFFC00`（模拟 32 位截断加 KiB 取整的来源）→ `TestDarwinMetricsFromSyscallLayout` 红，日志含 `net = 1024/`、`want en0's 64-bit counters only`。
2. `vmUsed` 的 `internal := uint64(le.Uint32(b[140:]))` 改成 `b[136:]`（错读成 external_page_count）→ `TestDarwinMetricsFromSyscallLayout` 红，日志含 `want total 2^37 and (internal-purgeable+wire+compressor)*page`。
3. `tickAccumulator.add` 里 `d := uint64(v - a.last[i])` 改成 `d := uint64(v) - uint64(a.last[i])`（不按 uint32 取差）→ `TestTickAccumulatorAcrossWrapAndCPUCountChange` 红，日志含 `pct across wrap`。
4. `darwinHost.ifaces` 里把 `if errors.Is(err, fs.ErrNotExist) {` 改成 `if err != nil {`（任何错误都跳过）→ `TestDarwinInterfaceReadErrorDropsTheWholeReading` 红，日志含 `partial interface set must not be reported`。
5. `darwinNetExclude` 删掉 `"utun*"` → `TestDarwinDefaultExcludeKeepsOnlyPhysicalInterfaces` 红，日志含 `utun0 must be excluded by default`。
6. `vmUsed` 里删掉 `purgeable > internal` 的拒收 → `TestVMUsedRejectsInconsistentCounts` 红，日志含 `purgeable above internal must be an error`。

- [ ] **Step 6: make ci**

```sh
cd /Users/xjetry/work/vibe/probe-macos && make ci > /tmp/m6mac-t2-ci.log 2>&1; echo $?
```

Expected: `0`。

- [ ] **Step 7: 提交**

```sh
cd /Users/xjetry/work/vibe/probe-macos && git add internal/agent/collect/darwinraw.go internal/agent/collect/darwinraw_test.go && git commit -m "agent: darwin 读数的字节布局解析与组合，Linux 上可测"
```

---

### Task 3: darwin 系统调用层（sysctl 与 purego）与依赖隔离

**Files:**
- Create: `internal/agent/collect/platform_darwin.go`、`internal/agent/collect/platform_darwin_test.go`、`cmd/agent/deps_test.go`
- Modify: `internal/agent/collect/platform_other.go:1`（build tag）、`go.mod`、`go.sum`、`Makefile`（`build`）

**Interfaces:**
- Consumes：Task 2 的 `darwinSource`（本任务的 `darwinSyscalls` 实现它）、`darwinHost`、`darwinNetExclude`、`cpuStateMax`；Task 1 的 `statfs`、`Collector`。
- Produces：`func NewPlatform(version string, clk clock.Clock, netInclude, netExclude []string) (*Collector, error)` 与 `func DefaultNetExclude() []string` 的 darwin 实现（`cmd/agent/main.go` 已在用）；`openDarwinSyscalls() *darwinSyscalls`（darwin 测试用）。依赖 `github.com/ebitengine/purego v0.11.1`，只被 `platform_darwin.go` 引用。

- [ ] **Step 1: 写失败的测试**

`internal/agent/collect/platform_darwin_test.go`（对照系统自带的命令行工具，容差只容得下 gauge 在两次读取之间的变化）：

```go
//go:build darwin

package collect

import (
	"bufio"
	"errors"
	"net"
	"os/exec"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/xjetry/probe/internal/clock"
)

// 本文件把系统调用层的读数与系统自带命令行工具的输出对照：命令行工具走的是另一条代码路径，
// 字节偏移、单位或来源选错时两边对不上。gauge 类读数在两次读取之间会变，容差按变化幅度给，
// 远小于偏移或单位错误造成的偏差。

func run(t *testing.T, name string, args ...string) string {
	t.Helper()
	out, err := exec.Command(name, args...).Output()
	if err != nil {
		t.Fatalf("%s %v: %v", name, args, err)
	}
	return strings.TrimSpace(string(out))
}

func cliUint(t *testing.T, name string, args ...string) uint64 {
	t.Helper()
	s := run(t, name, args...)
	v, err := strconv.ParseUint(s, 10, 64)
	if err != nil {
		t.Fatalf("%s %v printed %q: %v", name, args, s, err)
	}
	return v
}

func within(a, b, tol uint64) bool {
	if a > b {
		return a-b <= tol
	}
	return b-a <= tol
}

func realHost(t *testing.T) *darwinHost {
	t.Helper()
	s := openDarwinSyscalls()
	if s.machErr != nil {
		t.Fatalf("libSystem: %v", s.machErr)
	}
	return &darwinHost{src: s}
}

func TestDarwinEveryMetricPresent(t *testing.T) {
	c, err := NewPlatform("test", clock.Real(), nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.Metrics(); err != nil {
		t.Fatalf("first sample: %v", err)
	}
	time.Sleep(1100 * time.Millisecond)
	m, err := c.Metrics()
	if err != nil {
		t.Fatalf("second sample: %v", err)
	}
	if m.GetBootId() == "" || m.CpuPct == nil || m.MemTotal == nil || m.MemUsed == nil || m.SwapTotal == nil || m.SwapUsed == nil ||
		m.DiskTotal == nil || m.DiskUsed == nil || m.Load1 == nil || m.Procs == nil || m.UptimeS == nil ||
		m.TcpConns == nil || m.UdpConns == nil || m.NetRxTotal == nil || m.NetTxTotal == nil || m.NetRxBps == nil {
		t.Fatalf("missing readings: %+v", m)
	}
}

func TestDarwinBootIDAndUptimeMatchSysctl(t *testing.T) {
	h := realHost(t)
	id, err := h.bootID()
	if err != nil || id != run(t, "sysctl", "-n", "kern.bootsessionuuid") {
		t.Fatalf("boot id %q, %v", id, err)
	}
	// kern.boottime 是 "{ sec = N, usec = M } ..."；now − boottime 含睡眠时间，与 CLOCK_MONOTONIC 同口径。
	bt := run(t, "sysctl", "-n", "kern.boottime")
	sec, _, _ := strings.Cut(strings.TrimPrefix(bt, "{ sec = "), ",")
	boot, err := strconv.ParseInt(sec, 10, 64)
	if err != nil {
		t.Fatalf("kern.boottime %q: %v", bt, err)
	}
	up, err := h.uptime()
	if err != nil || !within(up, uint64(time.Now().Unix()-boot), 2) {
		t.Fatalf("uptime %d, %v; now − kern.boottime = %d", up, err, time.Now().Unix()-boot)
	}
}

// vm_stat 打印页大小与各计数；"Anonymous pages" 是 internal_page_count。
func TestDarwinMemoryMatchesVMStat(t *testing.T) {
	h := realHost(t)
	mem, err := h.memory()
	if err != nil {
		t.Fatal(err)
	}
	if mem.total != cliUint(t, "sysctl", "-n", "hw.memsize") {
		t.Fatalf("total %d != hw.memsize", mem.total)
	}
	out := run(t, "vm_stat")
	pages := map[string]uint64{}
	var page uint64
	sc := bufio.NewScanner(strings.NewReader(out))
	for sc.Scan() {
		line := sc.Text()
		if i := strings.Index(line, "page size of "); i >= 0 {
			page, _ = strconv.ParseUint(strings.Fields(line[i+len("page size of "):])[0], 10, 64)
			continue
		}
		k, v, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		n, err := strconv.ParseUint(strings.TrimSuffix(strings.TrimSpace(v), "."), 10, 64)
		if err == nil {
			pages[strings.Trim(k, `"`)] = n
		}
	}
	want := (pages["Anonymous pages"] - pages["Pages purgeable"] + pages["Pages wired down"] + pages["Pages occupied by compressor"]) * page
	if page == 0 || !within(mem.used, want, mem.total/20) {
		t.Fatalf("used %d, vm_stat gives %d (page %d); tolerance 5%% of %d", mem.used, want, page, mem.total)
	}
}

// netstat -ib 的 <Link#n> 行给出每块网卡的完整 64 位字节数。计数只增不减：
// 前后各读一次本实现，netstat 的值必须夹在两次之间，没有容差。
func TestDarwinInterfaceCountersSandwichNetstat(t *testing.T) {
	h := realHost(t)
	all := func(string) bool { return true }
	before, err := h.ifaces(all)
	if err != nil {
		t.Fatal(err)
	}
	out := run(t, "netstat", "-ib", "-n")
	after, err := h.ifaces(all)
	if err != nil {
		t.Fatal(err)
	}
	index := func(ifs []ifaceCounters) map[string]ifaceCounters {
		m := map[string]ifaceCounters{}
		for _, i := range ifs {
			m[i.name] = i
		}
		return m
	}
	b, a := index(before), index(after)
	checked := 0
	for _, line := range strings.Split(out, "\n") {
		f := strings.Fields(line)
		if len(f) < 7 || !strings.HasPrefix(f[2], "<Link#") {
			continue
		}
		// 有链路地址的行多一列 Address；字节列按从行尾倒数取：Ibytes 在倒数第五，Obytes 在倒数第二。
		rx, err1 := strconv.ParseUint(f[len(f)-5], 10, 64)
		tx, err2 := strconv.ParseUint(f[len(f)-2], 10, 64)
		if err1 != nil || err2 != nil {
			t.Fatalf("cannot parse netstat line %q", line)
		}
		x, ok1 := b[f[0]]
		y, ok2 := a[f[0]]
		if !ok1 || !ok2 {
			continue // 两次读取之间出现或消失的网卡
		}
		if x.rx > rx || rx > y.rx || x.tx > tx || tx > y.tx {
			t.Errorf("%s: netstat rx=%d tx=%d not within [%d,%d]/[%d,%d]", f[0], rx, tx, x.rx, y.rx, x.tx, y.tx)
		}
		checked++
	}
	if checked == 0 || b["lo0"].name == "" {
		t.Fatalf("no interface compared (lo0 present: %v)", b["lo0"].name != "")
	}
}

func TestDarwinSwapLoadProcsDiskMatchCLI(t *testing.T) {
	h := realHost(t)
	// "total = 0.00M  used = 0.00M  free = 0.00M  (encrypted)"，单位 MiB，两位小数。
	sw, err := h.swap()
	if err != nil {
		t.Fatal(err)
	}
	f := strings.Fields(run(t, "sysctl", "-n", "vm.swapusage"))
	mib := func(s string) uint64 {
		v, err := strconv.ParseFloat(strings.TrimSuffix(s, "M"), 64)
		if err != nil {
			t.Fatalf("vm.swapusage field %q: %v", s, err)
		}
		return uint64(v * (1 << 20))
	}
	if !within(sw.total, mib(f[2]), 1<<20) || !within(sw.used, mib(f[5]), 64<<20) {
		t.Fatalf("swap %d/%d vs vm.swapusage %v", sw.used, sw.total, f)
	}
	// "{ 1.23 4.56 7.89 }"：一分钟负载每 5 秒更新一次，两次读取之间变化不超过 1。
	l, err := h.load()
	if err != nil {
		t.Fatal(err)
	}
	lf := strings.Fields(strings.Trim(run(t, "sysctl", "-n", "vm.loadavg"), "{ }"))
	cli, _ := strconv.ParseFloat(lf[0], 64)
	if d := l.l1 - cli; d > 1 || d < -1 {
		t.Fatalf("load1 %v vs vm.loadavg %v", l.l1, lf)
	}
	n, err := h.procs()
	if err != nil {
		t.Fatal(err)
	}
	ps := uint64(len(strings.Split(run(t, "ps", "-A", "-o", "pid="), "\n")))
	if !within(uint64(n), ps, ps/20+20) {
		t.Fatalf("procs %d vs ps %d", n, ps)
	}
	d, err := h.disk()
	if err != nil {
		t.Fatal(err)
	}
	df := strings.Fields(strings.Split(run(t, "df", "-k", "/"), "\n")[1])
	if kb, _ := strconv.ParseUint(df[1], 10, 64); d.total != kb*1024 {
		t.Fatalf("disk total %d vs df %s KiB", d.total, df[1])
	}
}

func TestDarwinConnsCountOpenSockets(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	pc, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer pc.Close()
	tcp, udp, err := realHost(t).conns()
	if err != nil || tcp == 0 || udp == 0 {
		t.Fatalf("tcp=%d udp=%d err=%v with one listener and one udp socket open", tcp, udp, err)
	}
	cli := cliUint(t, "sysctl", "-n", "net.inet.tcp.pcbcount")
	if !within(uint64(tcp), cli, cli/10+16) {
		t.Fatalf("tcp %d vs sysctl %d", tcp, cli)
	}
}

// 每个 CPU 每秒走 hz 个 tick（kern.clockrate）；逐 CPU 之和除以 CPU 数与经过的秒数应接近 hz。
// 单位或字段错位会差出数量级。
func TestDarwinProcessorTicksAdvanceAtClockRate(t *testing.T) {
	s := openDarwinSyscalls()
	a, err := s.processorTicks()
	if err != nil {
		t.Fatal(err)
	}
	t0 := time.Now()
	time.Sleep(time.Second)
	b, err := s.processorTicks()
	if err != nil {
		t.Fatal(err)
	}
	dt := time.Since(t0).Seconds()
	var sum uint64
	for i := range a {
		sum += uint64(b[i] - a[i])
	}
	cr := run(t, "sysctl", "-n", "kern.clockrate")
	hzs, _, _ := strings.Cut(strings.TrimPrefix(cr, "{ hz = "), ",")
	hz, err := strconv.ParseFloat(hzs, 64)
	if err != nil {
		t.Fatalf("kern.clockrate %q", cr)
	}
	cpus := float64(len(a) / cpuStateMax)
	if rate := float64(sum) / cpus / dt; rate < hz*0.75 || rate > hz*1.25 {
		t.Fatalf("%.1f ticks per cpu per second, hz = %v", rate, hz)
	}
	if uint64(cpus) != cliUint(t, "sysctl", "-n", "hw.logicalcpu") {
		t.Fatalf("%v cpus vs hw.logicalcpu", cpus)
	}
}

func TestDarwinFactsMatchCLI(t *testing.T) {
	f := realHost(t).facts()
	if f.os != "macOS "+run(t, "sw_vers", "-productVersion") || f.kernel != run(t, "uname", "-r") ||
		f.cpuModel != run(t, "sysctl", "-n", "machdep.cpu.brand_string") || f.hostname != run(t, "sysctl", "-n", "kern.hostname") ||
		uint64(f.cpuCores) != cliUint(t, "sysctl", "-n", "hw.logicalcpu") {
		t.Fatalf("%+v", f)
	}
}

// libSystem 的函数取不到时只让 CPU、内存与进程数缺失：NewPlatform 不因此失败，agent 不退出，
// 否则 launchd 的 KeepAlive 会每 5 秒拉起一个立即退出的进程。
func TestDarwinWithoutLibSystemKeepsSysctlReadings(t *testing.T) {
	c := &Collector{Host: &darwinHost{src: &darwinSyscalls{machErr: errors.New("dlopen libSystem: test")}}, Clock: clock.Real()}
	m, err := c.Metrics()
	if err == nil || !strings.Contains(err.Error(), "dlopen libSystem: test") {
		t.Fatalf("err = %v, want the libSystem failure reported", err)
	}
	if m.CpuPct != nil || m.MemTotal != nil || m.Procs != nil {
		t.Fatalf("readings that need libSystem must be missing: %+v", m)
	}
	if m.GetBootId() == "" || m.Load1 == nil || m.SwapTotal == nil || m.DiskTotal == nil || m.TcpConns == nil || m.NetRxTotal == nil || m.UptimeS == nil {
		t.Fatalf("sysctl readings must survive: %+v", m)
	}
}
```

`cmd/agent/deps_test.go`：

```go
package main

import (
	"os"
	"os/exec"
	"slices"
	"strings"
	"testing"
)

const purego = "github.com/ebitengine/purego"

func deps(t *testing.T, goos, pkg string) []string {
	t.Helper()
	cmd := exec.Command("go", "list", "-deps", pkg)
	cmd.Env = append(os.Environ(), "GOOS="+goos, "GOARCH=arm64", "CGO_ENABLED=0")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("go list -deps %s (GOOS=%s): %v\n%s", pkg, goos, err, out)
	}
	return strings.Fields(string(out))
}

// darwin 的采集依赖只在带 darwin 约束的文件里引用，不链入 Linux 二进制（spec §2）。
// purego 在 Linux 上也能编译：不带 darwin 约束的文件（darwinraw.go 就是一个）一旦引用它，
// 各平台照样编译通过，Linux 产物却链入了它。这里按依赖图检查，缺陷在引入它的那次提交就红。
// darwin 一侧必须看得见 purego：否则这条检查对"列表为空"与"确实没有"分不出来。
func TestPuregoOnlyLinksIntoDarwin(t *testing.T) {
	if !slices.Contains(deps(t, "darwin", "github.com/xjetry/probe/cmd/agent"), purego) {
		t.Fatal("darwin agent does not depend on purego; the Linux check below would pass vacuously")
	}
	for _, pkg := range []string{"github.com/xjetry/probe/cmd/agent", "github.com/xjetry/probe/cmd/hub"} {
		if slices.Contains(deps(t, "linux", pkg), purego) {
			t.Fatalf("%s links %s on linux", pkg, purego)
		}
	}
}
```

- [ ] **Step 2: 跑红**

```sh
cd /Users/xjetry/work/vibe/probe-macos && CGO_ENABLED=0 go test -count=1 ./internal/agent/collect ./cmd/agent > /tmp/m6mac-t3-red.log 2>&1; echo $?
```

Expected: `1`；日志含 `undefined: openDarwinSyscalls`（collect 包测试编译失败）与 `darwin agent does not depend on purego; the Linux check below would pass vacuously`。

- [ ] **Step 3: 实现系统调用层**

`internal/agent/collect/platform_darwin.go`：

```go
//go:build darwin

package collect

import (
	"encoding/binary"
	"errors"
	"fmt"
	"slices"
	"unsafe"

	"github.com/ebitengine/purego"
	"golang.org/x/sys/unix"

	"github.com/xjetry/probe/internal/clock"
)

func NewPlatform(version string, clk clock.Clock, netInclude, netExclude []string) (*Collector, error) {
	return &Collector{Host: &darwinHost{src: openDarwinSyscalls()}, Clock: clk, NetInclude: netInclude, NetExclude: netExclude, Version: version}, nil
}

// DefaultNetExclude 是本平台未给 --net-exclude 时不计入流量的网卡。
func DefaultNetExclude() []string { return darwinNetExclude }

// Mach 常量取自 <mach/processor_info.h>、<mach/host_info.h>、<mach/vm_statistics.h>、<mach/kern_return.h>。
const (
	processorCPULoadInfo = 2  // PROCESSOR_CPU_LOAD_INFO
	hostVMInfo64         = 4  // HOST_VM_INFO64
	hostVMInfo64Count    = 38 // HOST_VM_INFO64_COUNT = sizeof(vm_statistics64_data_t) / sizeof(integer_t)
	kernSuccess          = 0
)

// darwinSyscalls 是 darwinSource 的真实实现：sysctl 走 x/sys/unix；逐 CPU 计数与 VM 统计只有 Mach 接口给得出，
// 进程数用 libproc 的 proc_listallpids（sysctl kern.proc.all 也能数，但每次要复制约 1 MB 的 kinfo_proc），
// 这几个经 purego 调 libSystem。
type darwinSyscalls struct {
	// machErr 非空时 libSystem 的函数不可用：依赖它们的读数缺失，sysctl 读数照常，agent 不因此退出。
	machErr error
	// mach_host_self 每次调用都给同一个端口名多加一个发送权引用，只在打开时取一次。
	host, task uint32

	hostProcessorInfo func(host uint32, flavor int32, n *uint32, info **uint32, cnt *uint32) int32
	hostStatistics64  func(host uint32, flavor int32, info *uint32, cnt *uint32) int32
	hostPageSize      func(host uint32, size *uintptr) int32
	vmDeallocate      func(task uint32, addr, size uintptr) int32
	procListallpids   func(buf *int32, size int32) int32
}

func openDarwinSyscalls() *darwinSyscalls {
	s := &darwinSyscalls{}
	lib, err := purego.Dlopen("/usr/lib/libSystem.B.dylib", purego.RTLD_NOW|purego.RTLD_GLOBAL)
	if err != nil {
		s.machErr = fmt.Errorf("dlopen libSystem: %w", err)
		return s
	}
	// 用 Dlsym 逐个取符号再注册：RegisterLibFunc 找不到符号时 panic，而缺一个函数只该让对应读数缺失。
	bind := func(fptr any, name string) {
		if s.machErr != nil {
			return
		}
		sym, err := purego.Dlsym(lib, name)
		if err != nil {
			s.machErr = fmt.Errorf("dlsym %s: %w", name, err)
			return
		}
		purego.RegisterFunc(fptr, sym)
	}
	var machHostSelf, machTaskSelf func() uint32
	bind(&machHostSelf, "mach_host_self")
	bind(&machTaskSelf, "mach_task_self")
	bind(&s.hostProcessorInfo, "host_processor_info")
	bind(&s.hostStatistics64, "host_statistics64")
	bind(&s.hostPageSize, "host_page_size")
	bind(&s.vmDeallocate, "vm_deallocate")
	bind(&s.procListallpids, "proc_listallpids")
	if s.machErr == nil {
		s.host, s.task = machHostSelf(), machTaskSelf()
	}
	return s
}

func (s *darwinSyscalls) sysctlString(name string) (string, error) { return unix.Sysctl(name) }
func (s *darwinSyscalls) sysctlUint32(name string) (uint32, error) { return unix.SysctlUint32(name) }
func (s *darwinSyscalls) sysctlUint64(name string) (uint64, error) { return unix.SysctlUint64(name) }
func (s *darwinSyscalls) sysctlRaw(name string, args ...int) ([]byte, error) {
	return unix.SysctlRaw(name, args...)
}
func (s *darwinSyscalls) statfs(path string) (uint64, uint64, error) { return statfs(path) }

// monotonicSeconds 用 CLOCK_MONOTONIC：macOS 上它自启动起算且含睡眠时间，与 now − kern.boottime 一致，
// 又不随墙钟调整。CLOCK_UPTIME_RAW 不含睡眠（实测一台睡过的机器上两者差 14 小时），不能当运行时长。
func (s *darwinSyscalls) monotonicSeconds() (uint64, error) {
	var ts unix.Timespec
	if err := unix.ClockGettime(unix.CLOCK_MONOTONIC, &ts); err != nil {
		return 0, err
	}
	return uint64(ts.Sec), nil
}

// processorTicks 用逐 CPU 的 host_processor_info 而不是聚合的 host_statistics(HOST_CPU_LOAD_INFO)：
// 实测一台 16 核 Apple Silicon：前者之和每秒稳定在约 1590（CPU 数 × kern.clockrate 的 hz 100），
// 后者按秒取差在 1057 到 2202 之间摆动，算出的忙碌比例偏差到 12 个百分点。
func (s *darwinSyscalls) processorTicks() ([]uint32, error) {
	if s.machErr != nil {
		return nil, s.machErr
	}
	var n, cnt uint32
	var info *uint32
	if kr := s.hostProcessorInfo(s.host, processorCPULoadInfo, &n, &info, &cnt); kr != kernSuccess {
		return nil, fmt.Errorf("host_processor_info: kern_return_t %d", kr)
	}
	// 数组由内核在本进程地址空间里 vm_allocate，复制出来后必须归还，否则每次采样都泄漏。
	out := slices.Clone(unsafe.Slice(info, cnt))
	if kr := s.vmDeallocate(s.task, uintptr(unsafe.Pointer(info)), uintptr(cnt)*4); kr != kernSuccess {
		return nil, fmt.Errorf("vm_deallocate: kern_return_t %d", kr)
	}
	if uint64(cnt) != uint64(n)*cpuStateMax {
		return nil, fmt.Errorf("host_processor_info: %d values for %d cpus", cnt, n)
	}
	return out, nil
}

func (s *darwinSyscalls) vmStatistics64() ([]byte, error) {
	if s.machErr != nil {
		return nil, s.machErr
	}
	var words [hostVMInfo64Count]uint32
	cnt := uint32(hostVMInfo64Count)
	if kr := s.hostStatistics64(s.host, hostVMInfo64, &words[0], &cnt); kr != kernSuccess {
		return nil, fmt.Errorf("host_statistics64: kern_return_t %d", kr)
	}
	// 按字重新编码成小端字节，交给与头文件布局对照的解析；darwin 的两个目标都是小端，逐字编码与内存布局相同。
	b := make([]byte, 4*int(cnt))
	for i := range int(cnt) {
		binary.LittleEndian.PutUint32(b[4*i:], words[i])
	}
	return b, nil
}

// pageSize 取 host_page_size：它是 host_statistics64 页计数的单位。hw.pagesize 对 Rosetta 转译的
// x86_64 进程报 4096，而 Apple Silicon 内核的页是 16384，用它会把已用内存少算四倍。
func (s *darwinSyscalls) pageSize() (uint64, error) {
	if s.machErr != nil {
		return 0, s.machErr
	}
	var size uintptr
	if kr := s.hostPageSize(s.host, &size); kr != kernSuccess {
		return 0, fmt.Errorf("host_page_size: kern_return_t %d", kr)
	}
	return uint64(size), nil
}

// pidCount 用 proc_listallpids：不带缓冲区时返回带余量的估计值，带缓冲区时返回实际写入的个数。
// 写满缓冲区说明进程表在两次调用之间长过了余量，放大再取。
func (s *darwinSyscalls) pidCount() (uint32, error) {
	if s.machErr != nil {
		return 0, s.machErr
	}
	est := s.procListallpids(nil, 0)
	if est <= 0 {
		return 0, errors.New("proc_listallpids: no estimate")
	}
	size := int(est) + 64
	for range 3 {
		buf := make([]int32, size)
		got := s.procListallpids(&buf[0], int32(4*len(buf)))
		if got <= 0 {
			return 0, fmt.Errorf("proc_listallpids: returned %d", got)
		}
		if int(got) < len(buf) {
			return uint32(got), nil
		}
		size *= 2
	}
	return 0, errors.New("proc_listallpids: process table kept outgrowing the buffer")
}
```

`internal/agent/collect/platform_other.go` 第 1 行改为：

```go
//go:build !linux && !darwin
```

依赖：

```sh
cd /Users/xjetry/work/vibe/probe-macos && go get github.com/ebitengine/purego@v0.11.1 > /tmp/m6mac-t3-get.log 2>&1; echo $?
cd /Users/xjetry/work/vibe/probe-macos && go mod tidy > /tmp/m6mac-t3-tidy.log 2>&1; echo $?
```

Expected: 两个都是 `0`；`go.mod` 的直接依赖里出现 `github.com/ebitengine/purego v0.11.1`（不带 `// indirect`）。

`Makefile` 的 `build`（Linux 上的 CI 只有在这里才编译得到 darwin 文件，两个架构都编）：

```diff
-# build 验证全部已有的包在本机以及 Linux amd64、arm64 上都能编译；
+# build 验证全部已有的包在本机、Linux 与 darwin 的 amd64、arm64 上都能编译：darwin 的采集文件带 build tag，
+# Linux 上的 CI 只有在这里才编译得到它们；lint 的 GOOS=darwin go vet 只覆盖本机架构，而 purego 按架构分文件实现。
 # 二进制产物由 binaries 生成，只有 e2e 需要它。
 build:
 	go build ./...
 	GOOS=linux GOARCH=amd64 go build ./...
 	GOOS=linux GOARCH=arm64 go build ./...
+	GOOS=darwin GOARCH=amd64 go build ./...
+	GOOS=darwin GOARCH=arm64 go build ./...
```

- [ ] **Step 4: 跑绿（原生、Rosetta、Linux 容器、lint 与 build）**

```sh
cd /Users/xjetry/work/vibe/probe-macos && CGO_ENABLED=0 go test -count=1 -v ./internal/agent/collect ./cmd/agent > /tmp/m6mac-t3-green.log 2>&1; echo $?
cd /Users/xjetry/work/vibe/probe-macos && CGO_ENABLED=0 GOARCH=amd64 go test -count=1 -v ./internal/agent/collect ./cmd/agent > /tmp/m6mac-t3-rosetta.log 2>&1; echo $?
cd /Users/xjetry/work/vibe/probe-macos && docker run --rm -v "$PWD:/src" -v "$(go env GOMODCACHE):/go/pkg/mod" -w /src -e CGO_ENABLED=0 -e GOFLAGS=-buildvcs=false golang:1.27.1-bookworm go test -count=1 ./internal/agent/... ./cmd/agent/... > /tmp/m6mac-t3-linux.log 2>&1; echo $?
cd /Users/xjetry/work/vibe/probe-macos && make lint > /tmp/m6mac-t3-lint.log 2>&1; echo $?
cd /Users/xjetry/work/vibe/probe-macos && make build > /tmp/m6mac-t3-build.log 2>&1; echo $?
cd /Users/xjetry/work/vibe/probe-macos && CGO_ENABLED=0 go run ./cmd/agent run -h > /tmp/m6mac-t3-help.log 2>&1; echo $?
```

Expected: 前五个 `0`。`GOARCH=amd64` 的测试二进制由 Rosetta 执行（`arch -x86_64 /usr/bin/true` 为 0 即已装）；它与原生日志里 `TestDarwin` 开头的 14 个用例与 `TestPuregoOnlyLinksIntoDarwin` 都是 `--- PASS`。最后一条退出码 `1`（`flag.ErrHelp` 走 `main` 的错误出口），日志含 `(default: lo*, gif*, stf*, utun*, ipsec*, bridge*, vmenet*, awdl*, llw*, anpi*, ap*)`。

- [ ] **Step 5: 缺陷注入**

每条先 `git add` 被注入的文件建基准，注入后 `git diff --stat` 非空，跑完 `git checkout -- <文件>` 还原并确认 `git diff --stat` 为空：

1. `pageSize` 改成 `v, err := unix.SysctlUint32("hw.pagesize"); return uint64(v), err`。跑 `CGO_ENABLED=0 GOARCH=amd64 go test -count=1 -run TestDarwinMemoryMatchesVMStat ./internal/agent/collect > /tmp/m6mac-t3-inj1.log 2>&1; echo $?`：期望 `1`，日志含 `vm_stat gives` 与 `tolerance 5%`（已用量只有四分之一）。同一注入原生跑期望 `0`——`hw.pagesize` 只在 Rosetta 下与内核页不同，这条用例在 Apple Silicon 上只有转译运行才抓得到（Review Focus 4），两个结果都记进报告。
2. `monotonicSeconds` 的 `unix.CLOCK_MONOTONIC` 改成 `unix.CLOCK_UPTIME_RAW`。跑 `-run TestDarwinBootIDAndUptimeMatchSysctl`：期望 `1`，日志含 `now − kern.boottime =`。前提是本机自开机以来睡眠过：先跑 `cd /Users/xjetry/work/vibe/probe-macos && pmset -g log > /tmp/m6mac-t3-pmset.log 2>&1; echo $?` 并在日志里找开机之后的 `Wake` 行；没有睡眠过时这条注入不会红，如实写进报告。
3. `darwinraw.go` 的 import 里加 `_ "github.com/ebitengine/purego"`（不带 darwin 约束的文件引用 purego）。跑 `CGO_ENABLED=0 go test -count=1 ./cmd/agent > /tmp/m6mac-t3-inj3.log 2>&1; echo $?`：期望 `1`，日志含 `links github.com/ebitengine/purego on linux`。
4. `deps_test.go` 的 `const purego` 改成 `"github.com/ebitengine/purego-typo"`：期望 `1`，日志含 `the Linux check below would pass vacuously`（阳性对照确实在起作用）。
5. `darwinSyscalls.sysctlString` 开头加 `if s.machErr != nil { return "", s.machErr }`（libSystem 的失败拖垮 sysctl 读数）。跑 `-run TestDarwinWithoutLibSystemKeepsSysctlReadings`：期望 `1`，日志含 `sysctl readings must survive`。
6. 重做 Task 2 的注入 1（`parseIfmibData` 的 `& 0xFFFFFC00`），跑 `-run TestDarwinInterfaceCountersSandwichNetstat`：期望 `1`，日志含 `netstat rx=` 与 `not within`（真机内核上也抓得到截断的来源）。

- [ ] **Step 6: make ci**

```sh
cd /Users/xjetry/work/vibe/probe-macos && make ci > /tmp/m6mac-t3-ci.log 2>&1; echo $?
```

Expected: `0`（本机是 darwin，`make test` 会原生跑上面的对照测试）。

- [ ] **Step 7: 提交**

```sh
cd /Users/xjetry/work/vibe/probe-macos && git add internal/agent/collect cmd/agent/deps_test.go go.mod go.sum Makefile && git commit -m "agent: darwin 采集经 sysctl 与 libSystem 取数，purego 不进 Linux 依赖图"
```

---

### Task 4: launchd plist 与 `install-macos.sh`

**Files:**
- Create: `deploy/launchd/xyz.probe.agent.plist`、`deploy/install-macos.sh`
- Test: `deploy/installmacos_test.go`（`package deploy`，只有测试文件）
- Modify: `Makefile:15`（`lint` 的 shellcheck）

**Interfaces:**
- Consumes：agent 的 `register`/`run` 子命令与 `version`（`cmd/agent/main.go`，不改）；`client.SaveConfig` 的 0600 权限（`internal/agent/client/config.go`，不改）。
- Produces：`deploy/launchd/xyz.probe.agent.plist`（Label `xyz.probe.agent`，`UserName`/`GroupName` `_probe-agent`，`KeepAlive` true，`ThrottleInterval` 5，日志 `/Library/Logs/probe-agent/probe-agent.{log,err}`），由 Task 5 原样打进 `probe-agent_darwin_<arch>.tar.gz`、由 Task 6 改成用户域作业；`deploy/install-macos.sh`，参数 `--hub URL --key KEY [--name N] [--version vX.Y.Z] [--base-url URL]` 与 `--uninstall [--purge]`，成功的最后一行 `probe-agent installed and started (launchd, <arch>, probe-agent_darwin_<arch>.tar.gz)`，由 Task 5 复制为 `dist/install-macos.sh`、由 README（Task 8）引用。环境变量 `PROBE_INSTALL_ROOT` 只给逻辑测试用。

脚本的行为测试只在 PATH 替身下以普通用户跑：`dscl`、`launchctl`、`ps`、`id`、`sysctl`、`uname`、`chown`、`sleep` 是替身，`curl`（经 `file://`）、`shasum`、`tar`、`install` 是真的。`dscl`、`launchctl` 与假 agent 读尽 stdin，脚本以 `sh -s --` 从 stdin 运行——漏写 `</dev/null` 的调用会吞掉脚本余下部分（Linux 计划 Review Focus 1 的同一陷阱）。真实 launchd、目录服务与 root 属主由 README 的真机核对验证。**不要在本机以任何方式执行不带 `PROBE_INSTALL_ROOT` 与替身 PATH 的 `install-macos.sh`。**

- [ ] **Step 1: 写失败的测试**

`deploy/installmacos_test.go`：

```go
// Package deploy 的测试以普通用户运行 install-macos.sh：落盘路径经 PROBE_INSTALL_ROOT 挂到临时目录，
// dscl、launchctl、ps、id、sysctl、uname、chown、sleep 由 PATH 上的替身接管，curl、shasum、tar 用真的。
// 真实 launchd、目录服务与 root 属主只在真机上验证（spec §14：没有 macOS 虚拟机可用）。
package deploy

import (
	"archive/tar"
	"compress/gzip"
	"crypto/sha256"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

const svcUser = "_probe-agent"

// 替身共用的状态目录：users/<名> 存 "uid gid"，groups/<名> 存 "gid"，procs 每行 "uid pid"，
// loaded 表示作业已载入，calls 按调用顺序记下每个替身收到的参数。
// dscl、launchctl 与假 agent 读尽 stdin：脚本以 sh -s 从 stdin 运行，
// 漏掉 </dev/null 的调用会吞掉脚本余下部分，安装在中途无声结束，测试看不到最后一行。
var stubs = map[string]string{
	"id": `#!/bin/sh
S=$STUB_STATE
if [ "$#" = 1 ] && [ "$1" = -u ]; then echo "${STUB_UID:-0}"; exit 0; fi
flag=""; [ "$#" = 2 ] && { flag=$1; shift; }
f="$S/users/$1"
[ -f "$f" ] || { echo "id: $1: no such user" >&2; exit 1; }
read -r uid gid < "$f"
case "$flag" in
  "") echo "uid=$uid($1) gid=$gid";;
  -u) echo "$uid";;
  -g) echo "$gid";;
  -gn)
    for g in "$S"/groups/*; do
      [ -f "$g" ] || continue
      if [ "$(cat "$g")" = "$gid" ]; then basename "$g"; exit 0; fi
    done
    echo "id: group $gid not found" >&2; exit 1;;
esac
`,
	"dscl": `#!/bin/sh
cat > /dev/null
S=$STUB_STATE
echo "dscl $*" >> "$S/calls"
[ "$1" = . ] || exit 64
op=$2; path=$3; key=${4-}; val=${5-}
kind=${path#/}; kind=${kind%%/*}; name=${path##*/}
case "$op" in
  -list)
    cat "$S/sys-$kind" 2>/dev/null
    for f in "$S/$(echo "$kind" | tr 'UG' 'ug')"/*; do
      [ -f "$f" ] || continue
      read -r a b < "$f"
      if [ "$key" = UniqueID ]; then echo "$(basename "$f") $a"; else echo "$(basename "$f") $a"; fi
    done;;
  -read)
    f="$S/$(echo "$kind" | tr 'UG' 'ug')/$name"
    [ -f "$f" ] || { echo "<dscl_cmd> DS Error: -14136 (eDSRecordNotFound)" >&2; exit 56; }
    read -r a b < "$f"; echo "$key: $a";;
  -create)
    case "$kind/$key" in
      Groups/PrimaryGroupID) echo "$val" > "$S/groups/$name";;
      Users/UniqueID) [ -n "${STUB_DSCL_DROP_USER-}" ] || echo "$val" > "$S/users/$name";;
      Users/PrimaryGroupID) [ -f "$S/users/$name" ] && { read -r u g < "$S/users/$name"; echo "$u $val" > "$S/users/$name"; };;
    esac;;
  -delete)
    [ -n "${STUB_DSCL_KEEP-}" ] || rm -f "$S/$(echo "$kind" | tr 'UG' 'ug')/$name";;
esac
exit 0
`,
	"launchctl": `#!/bin/sh
cat > /dev/null
S=$STUB_STATE
echo "launchctl $*" >> "$S/calls"
case "$1" in
  print) [ -f "$S/loaded" ] && exit 0; echo "Could not find service \"${2#*/}\" in domain for system" >&2; exit 113;;
  bootout)
    [ -f "$S/loaded" ] || { echo "Boot-out failed: 3: No such process" >&2; exit 3; }
    rm -f "$S/loaded"
    [ -n "${STUB_BOOTOUT_LEAVES_PROCESS-}" ] || : > "$S/procs"
    exit 0;;
  enable) exit 0;;
  bootstrap)
    [ -f "$3" ] || { echo "Bootstrap failed: 2: No such file or directory" >&2; exit 5; }
    cp "$3" "$S/bootstrapped.plist"
    : > "$S/loaded"
    [ -n "${STUB_START_FAILS-}" ] && exit 0
    read -r uid gid < "$S/users/_probe-agent"
    echo "$uid 4242" >> "$S/procs"
    exit 0;;
esac
exit 64
`,
	"ps": `#!/bin/sh
S=$STUB_STATE
[ "$*" = "-axo uid=,pid=" ] || { echo "unexpected ps $*" >&2; exit 64; }
echo "    0     1"
echo "  501   777"
if [ -n "${STUB_RESPAWN-}" ] && [ -s "$S/procs" ]; then
  awk '{ print $1, $2 + 1 }' "$S/procs" > "$S/procs.new" && mv "$S/procs.new" "$S/procs"
fi
cat "$S/procs" 2>/dev/null
exit 0
`,
	"sysctl": `#!/bin/sh
[ "$*" = "-in hw.optional.arm64" ] || { echo "unexpected sysctl $*" >&2; exit 64; }
printf '%s' "${STUB_ARM64-}"
[ -z "${STUB_ARM64-}" ] || echo
`,
	"uname": `#!/bin/sh
[ "$*" = -m ] || { echo "unexpected uname $*" >&2; exit 64; }
echo "${STUB_UNAME_M:-arm64}"
`,
	"chown": `#!/bin/sh
echo "chown $*" >> "$STUB_STATE/calls"
`,
	"sleep": `#!/bin/sh
echo "sleep $*" >> "$STUB_STATE/calls"
`,
}

// fakeAgent 是包里的 probe-agent：register 写出配置并记下参数，与真 agent 的 SaveConfig 同为 0600。
const fakeAgent = `#!/bin/sh
cat > /dev/null
echo "probe-agent $*" >> "$STUB_STATE/calls"
[ "$1" = register ] || exit 0
while [ $# -gt 0 ]; do [ "$1" = --config ] && cfg=$2; shift; done
mkdir -p "$(dirname "$cfg")"
echo '{"hub":"h","token":"t"}' > "$cfg"
chmod 0600 "$cfg"
echo "registered as node 1; config written to $cfg"
`

type env struct {
	t                *testing.T
	root, state, bin string
	dist             string
	vars             []string
}

func newEnv(t *testing.T) *env {
	t.Helper()
	d := t.TempDir()
	e := &env{t: t, root: filepath.Join(d, "root"), state: filepath.Join(d, "state"), bin: filepath.Join(d, "bin"), dist: filepath.Join(d, "dist")}
	for _, dir := range []string{e.root, e.bin, e.dist, filepath.Join(e.state, "users"), filepath.Join(e.state, "groups")} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	for name, body := range stubs {
		if err := os.WriteFile(filepath.Join(e.bin, name), []byte(body), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	// 300–302 已被占用，空闲号应是 303；系统账户不在替身状态里，只出现在 -list 的输出中。
	e.write("sys-Users", "_taken1 300\n_taken3 302\n")
	e.write("sys-Groups", "_taken2 301\n")
	e.write("calls", "")
	e.write("procs", "")
	e.release("arm64", "v1")
	e.release("amd64", "v1")
	return e
}

func (e *env) write(name, body string) {
	e.t.Helper()
	if err := os.WriteFile(filepath.Join(e.state, name), []byte(body), 0o644); err != nil {
		e.t.Fatal(err)
	}
}

// release 按 make release 的形状打包：包内是 probe-agent 与仓库里的 plist 原件。
func (e *env) release(arch, version string) {
	e.t.Helper()
	plist, err := os.ReadFile("launchd/xyz.probe.agent.plist")
	if err != nil {
		e.t.Fatal(err)
	}
	pkg := "probe-agent_darwin_" + arch + ".tar.gz"
	f, err := os.Create(filepath.Join(e.dist, pkg))
	if err != nil {
		e.t.Fatal(err)
	}
	gz := gzip.NewWriter(f)
	tw := tar.NewWriter(gz)
	for _, m := range []struct {
		name string
		body string
		mode int64
	}{{"probe-agent", fakeAgent + "# " + version + " " + arch + "\n", 0o755}, {"xyz.probe.agent.plist", string(plist), 0o644}} {
		if err := tw.WriteHeader(&tar.Header{Name: m.name, Mode: m.mode, Size: int64(len(m.body))}); err != nil {
			e.t.Fatal(err)
		}
		tw.Write([]byte(m.body))
	}
	tw.Close()
	gz.Close()
	f.Close()
	e.sums()
}

func (e *env) sums() {
	e.t.Helper()
	var b strings.Builder
	for _, arch := range []string{"amd64", "arm64"} {
		pkg := "probe-agent_darwin_" + arch + ".tar.gz"
		data, err := os.ReadFile(filepath.Join(e.dist, pkg))
		if err != nil {
			continue
		}
		fmt.Fprintf(&b, "%x  %s\n", sha256.Sum256(data), pkg)
	}
	if err := os.WriteFile(filepath.Join(e.dist, "SHA256SUMS"), []byte(b.String()), 0o644); err != nil {
		e.t.Fatal(err)
	}
}

// run 以面板命令的形态执行：脚本来自 stdin（sh -s --）。
func (e *env) run(args ...string) (string, int) {
	e.t.Helper()
	script, err := os.Open("install-macos.sh")
	if err != nil {
		e.t.Fatal(err)
	}
	defer script.Close()
	cmd := exec.Command("sh", append([]string{"-s", "--"}, args...)...)
	cmd.Stdin = script
	cmd.Env = append(os.Environ(), "PATH="+e.bin+string(os.PathListSeparator)+os.Getenv("PATH"),
		"PROBE_INSTALL_ROOT="+e.root, "STUB_STATE="+e.state)
	cmd.Env = append(cmd.Env, e.vars...)
	out, err := cmd.CombinedOutput()
	code := 0
	if ee, ok := err.(*exec.ExitError); ok {
		code = ee.ExitCode()
	} else if err != nil {
		e.t.Fatal(err)
	}
	return string(out), code
}

func (e *env) install(extra ...string) (string, int) {
	return e.run(append([]string{"--hub", "http://hub.test", "--key", "k", "--base-url", "file://" + e.dist}, extra...)...)
}

func (e *env) calls() []string {
	e.t.Helper()
	b, err := os.ReadFile(filepath.Join(e.state, "calls"))
	if err != nil {
		e.t.Fatal(err)
	}
	return strings.Split(strings.TrimSpace(string(b)), "\n")
}

func (e *env) resetCalls() { e.write("calls", "") }

// index 返回第一条以 prefix 开头的调用的位置，没有时为 -1。
func index(calls []string, prefix string) int {
	return slices.IndexFunc(calls, func(c string) bool { return strings.HasPrefix(c, prefix) })
}

func (e *env) file(rel string) string {
	e.t.Helper()
	b, err := os.ReadFile(filepath.Join(e.root, rel))
	if err != nil {
		e.t.Fatalf("%s: %v", rel, err)
	}
	return string(b)
}

func (e *env) exists(rel string) bool {
	_, err := os.Stat(filepath.Join(e.root, rel))
	return err == nil
}

const done = "probe-agent installed and started (launchd, arm64, probe-agent_darwin_arm64.tar.gz)"

func TestFreshInstallFromStdin(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	out, code := e.install("--name", "mac-1")
	if code != 0 || !strings.Contains(out, done) {
		t.Fatalf("exit %d, want the final line %q:\n%s", code, done, out)
	}
	c := e.calls()
	// 空闲号 303 同时给组与用户；组先于用户建。
	if g, u := index(c, "dscl . -create /Groups/_probe-agent PrimaryGroupID 303"), index(c, "dscl . -create /Users/_probe-agent UniqueID 303"); g < 0 || u < g {
		t.Fatalf("group then user with id 303, got %q", c)
	}
	reg := index(c, "probe-agent register --hub http://hub.test --key k --config "+e.root+"/etc/probe-agent/config.json --name mac-1")
	boot := index(c, "launchctl bootstrap system "+e.root+"/Library/LaunchDaemons/xyz.probe.agent.plist")
	if reg < 0 || boot < reg || index(c, "launchctl bootout") >= 0 {
		t.Fatalf("register before bootstrap and no bootout on a fresh host, got %q", c)
	}
	for _, want := range []string{
		"chown root:_probe-agent " + e.root + "/etc/probe-agent",
		"chown _probe-agent:_probe-agent " + e.root + "/etc/probe-agent/config.json",
		"chown root:wheel " + e.root + "/Library/LaunchDaemons/xyz.probe.agent.plist",
		"launchctl enable system/xyz.probe.agent",
	} {
		if i := index(c, want); i < 0 || i > boot {
			t.Errorf("missing %q before bootstrap in %q", want, c)
		}
	}
	if !strings.Contains(e.file("usr/local/bin/probe-agent"), "# v1 arm64") {
		t.Fatal("installed binary is not the arm64 package's")
	}
	st, err := os.Stat(filepath.Join(e.root, "etc/probe-agent"))
	if err != nil || st.Mode().Perm() != 0o750 {
		t.Fatalf("config dir mode %v, %v; want 0750", st.Mode(), err)
	}
}

// 包里的 plist 与脚本的常量是同一件事的两处写法：脚本按 Label 管作业、按 UserName 的 uid 认进程、
// 把二进制放到 ProgramArguments[0]、建 StandardErrorPath 所在的目录。
func TestPlistAgreesWithScript(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	if out, code := e.install(); code != 0 {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	p := e.file("Library/LaunchDaemons/xyz.probe.agent.plist")
	for _, want := range []string{
		"<key>Label</key>\n\t<string>xyz.probe.agent</string>",
		"<string>/usr/local/bin/probe-agent</string>",
		"<string>/etc/probe-agent/config.json</string>",
		"<key>UserName</key>\n\t<string>" + svcUser + "</string>",
		"<key>GroupName</key>\n\t<string>" + svcUser + "</string>",
		"<string>/Library/Logs/probe-agent/probe-agent.err</string>",
	} {
		if !strings.Contains(p, want) {
			t.Errorf("plist lacks %q", want)
		}
	}
	for _, rel := range []string{"usr/local/bin/probe-agent", "etc/probe-agent/config.json", "Library/Logs/probe-agent"} {
		if !e.exists(rel) {
			t.Errorf("script did not create %s, which the plist refers to", rel)
		}
	}
}

func TestRerunUpgradesWithoutRegistering(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	if out, code := e.install(); code != 0 {
		t.Fatalf("first install exit %d:\n%s", code, out)
	}
	e.release("arm64", "v2")
	e.resetCalls()
	out, code := e.install("--name", "ignored")
	if code != 0 || !strings.Contains(out, "keeping the current registration (--key ignored)") || !strings.Contains(out, "--name ignored") {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	c := e.calls()
	if index(c, "probe-agent register") >= 0 || index(c, "dscl . -create") >= 0 {
		t.Fatalf("rerun must neither register nor recreate the account: %q", c)
	}
	if out := index(c, "launchctl bootout system/xyz.probe.agent"); out < 0 || out > index(c, "launchctl bootstrap") {
		t.Fatalf("a loaded job must be booted out before the new one is bootstrapped: %q", c)
	}
	if !strings.Contains(e.file("usr/local/bin/probe-agent"), "# v2 arm64") {
		t.Fatal("binary was not replaced")
	}
}

func TestArchitectureFromHardwareNotShell(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		arm64, uname, pkg string
	}{
		{"1", "x86_64", "arm64"}, // Rosetta 转译的终端：uname -m 报 x86_64，硬件是 Apple Silicon
		{"", "x86_64", "amd64"},  // Intel：hw.optional.arm64 不存在
	} {
		e := newEnv(t)
		e.vars = []string{"STUB_ARM64=" + tc.arm64, "STUB_UNAME_M=" + tc.uname}
		out, code := e.install()
		if code != 0 || !strings.Contains(out, "probe-agent_darwin_"+tc.pkg+".tar.gz)") {
			t.Fatalf("arm64=%q uname=%q: exit %d, want the %s package:\n%s", tc.arm64, tc.uname, code, tc.pkg, out)
		}
	}
}

// 作业被手工 bootout 后 plist 还在：没有可停的作业，bootout 会以 3 失败，重跑不能因此失败。
func TestUnloadedJobIsNotBootedOut(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	if out, code := e.install(); code != 0 {
		t.Fatalf("first install exit %d:\n%s", code, out)
	}
	os.Remove(filepath.Join(e.state, "loaded"))
	e.write("procs", "")
	e.resetCalls()
	out, code := e.install()
	if code != 0 || !strings.Contains(out, done) {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	if index(e.calls(), "launchctl bootout") >= 0 {
		t.Fatalf("an unloaded job must not be booted out: %q", e.calls())
	}
}

func TestChecksumMismatchLeavesRunningServiceAlone(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	if out, code := e.install(); code != 0 {
		t.Fatalf("first install exit %d:\n%s", code, out)
	}
	sums := filepath.Join(e.dist, "SHA256SUMS")
	os.WriteFile(sums, []byte(strings.Repeat("0", 64)+"  probe-agent_darwin_arm64.tar.gz\n"), 0o644)
	e.resetCalls()
	out, code := e.install()
	if code == 0 || !strings.Contains(out, "FAILED") {
		t.Fatalf("exit %d, want shasum failure:\n%s", code, out)
	}
	if index(e.calls(), "launchctl bootout") >= 0 {
		t.Fatalf("a bad download must not stop the running service: %q", e.calls())
	}
	os.WriteFile(sums, []byte(strings.Repeat("0", 64)+"  probe-agent_darwin_amd64.tar.gz\n"), 0o644)
	if out, code := e.install(); code == 0 || !strings.Contains(out, "SHA256SUMS has no entry for probe-agent_darwin_arm64.tar.gz") {
		t.Fatalf("exit %d:\n%s", code, out)
	}
}

// dscl 退出码为 0 却没建出用户：回查挡住，下载与注册都还没发生。
func TestAccountCreationIsVerified(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	e.vars = []string{"STUB_DSCL_DROP_USER=1"}
	out, code := e.install()
	if code != 1 || !strings.Contains(out, "failed to create system user _probe-agent") {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	if index(e.calls(), "probe-agent register") >= 0 || e.exists("usr/local/bin/probe-agent") {
		t.Fatal("nothing may be downloaded or registered after the account check fails")
	}
}

func TestLingeringProcessBlocksReplacement(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	if out, code := e.install(); code != 0 {
		t.Fatalf("first install exit %d:\n%s", code, out)
	}
	e.release("arm64", "v2")
	e.vars = []string{"STUB_BOOTOUT_LEAVES_PROCESS=1"}
	out, code := e.install()
	if code != 1 || !strings.Contains(out, "probe-agent is still running: processes with uid 303 (_probe-agent): 4242") {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	if !strings.Contains(e.file("usr/local/bin/probe-agent"), "# v1 arm64") {
		t.Fatal("binary must not be replaced while the old process runs")
	}
}

func TestStartIsConfirmedBySamePID(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct{ knob, want string }{
		{"STUB_START_FAILS=1", "probe-agent did not start"},
		{"STUB_RESPAWN=1", "probe-agent did not stay running (pid 4243)"},
	} {
		e := newEnv(t)
		e.vars = []string{tc.knob}
		out, code := e.install()
		if code != 1 || !strings.Contains(out, tc.want) || !strings.Contains(out, "/Library/Logs/probe-agent/probe-agent.err") {
			t.Fatalf("%s: exit %d, want %q and the log path:\n%s", tc.knob, code, tc.want, out)
		}
	}
}

func TestUninstallAndPurge(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	if out, code := e.install(); code != 0 {
		t.Fatalf("install exit %d:\n%s", code, out)
	}
	if out, code := e.run("--purge"); code != 2 {
		t.Fatalf("--purge alone must be a usage error, exit %d:\n%s", code, out)
	}
	out, code := e.run("--uninstall")
	if code != 0 || e.exists("usr/local/bin/probe-agent") || e.exists("Library/LaunchDaemons/xyz.probe.agent.plist") {
		t.Fatalf("uninstall exit %d:\n%s", code, out)
	}
	if !e.exists("etc/probe-agent/config.json") || !e.exists("../state/users/_probe-agent") {
		t.Fatal("plain uninstall keeps config and account")
	}
	e.vars = []string{"STUB_DSCL_KEEP=1"}
	if out, code := e.run("--uninstall", "--purge"); code != 1 || !strings.Contains(out, "failed to delete user or group _probe-agent") {
		t.Fatalf("undeleted account must fail purge, exit %d:\n%s", code, out)
	}
	e.vars = nil
	if out, code := e.run("--uninstall", "--purge"); code != 0 || e.exists("etc/probe-agent") || e.exists("Library/Logs/probe-agent") || e.exists("../state/users/_probe-agent") || e.exists("../state/groups/_probe-agent") {
		t.Fatalf("purge exit %d:\n%s", code, out)
	}
}

func TestRefusals(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	e.vars = []string{"STUB_UID=501"}
	if out, code := e.install(); code != 1 || !strings.Contains(out, "must run as root") {
		t.Fatalf("non-root: exit %d:\n%s", code, out)
	}
	e.vars = nil
	if out, code := e.run("--base-url", "file://"+e.dist); code != 2 || !strings.Contains(out, "--hub and --key are required for the first install") {
		t.Fatalf("missing credentials: exit %d:\n%s", code, out)
	}
	if out, code := e.run("--hub"); code != 2 {
		t.Fatalf("flag without value: exit %d:\n%s", code, out)
	}
}
```

- [ ] **Step 2: 跑红**

```sh
cd /Users/xjetry/work/vibe/probe-macos && go test -count=1 ./deploy > /tmp/m6mac-t4-red.log 2>&1; echo $?
```

Expected: `1`；日志含 `open launchd/xyz.probe.agent.plist: no such file or directory`。

- [ ] **Step 3: 写 plist**

`deploy/launchd/xyz.probe.agent.plist`：

```xml
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>Label</key>
	<string>xyz.probe.agent</string>
	<key>ProgramArguments</key>
	<array>
		<string>/usr/local/bin/probe-agent</string>
		<string>run</string>
		<string>--config</string>
		<string>/etc/probe-agent/config.json</string>
	</array>
	<!-- 安装脚本按有效 uid 确认服务进程已停与已起，前提是本服务的每个进程都以这个专用账户运行。
	     改用别的身份运行任何进程时，deploy/install-macos.sh 的判据要一起改。 -->
	<key>UserName</key>
	<string>_probe-agent</string>
	<key>GroupName</key>
	<string>_probe-agent</string>
	<!-- 连不上 hub 时 agent 在进程内退避重试（上限为 TTL）而不退出；进程只在启动失败或崩溃时退出。
	     KeepAlive 无条件拉起，ThrottleInterval 把间隔定为 5 秒，与 Linux 两种 init 的无限次、每次 5 秒一致。 -->
	<key>KeepAlive</key>
	<true/>
	<key>ThrottleInterval</key>
	<integer>5</integer>
	<!-- 文件不存在时 launchd 以 UserName/GroupName 的属主创建（launchd.plist(5)）；目录由安装脚本建、属 root。 -->
	<key>StandardOutPath</key>
	<string>/Library/Logs/probe-agent/probe-agent.log</string>
	<key>StandardErrorPath</key>
	<string>/Library/Logs/probe-agent/probe-agent.err</string>
</dict>
</plist>
```

```sh
cd /Users/xjetry/work/vibe/probe-macos && plutil -lint deploy/launchd/xyz.probe.agent.plist > /tmp/m6mac-t4-plutil.log 2>&1; echo $?
```

Expected: `0`，日志 `deploy/launchd/xyz.probe.agent.plist: OK`。plist 的运行时凭据是 Task 6 的用户域作业与 README 真机核对，不在这里 grep 键值。

- [ ] **Step 4: 写安装脚本**

`deploy/install-macos.sh`（`chmod +x`）：

```sh
#!/bin/sh
# probe-agent 的 macOS 安装脚本：下载、校验、注册并装成 LaunchDaemon；重跑即升级。
# 参数、步骤顺序与提示与 Linux 的 install.sh 同形，差异只在平台工具：dscl 建账户、shasum 校验、
# launchctl 管服务、ps 扫进程（macOS 没有 /proc）。
# 以 curl … | sh -s -- 运行时，脚本本身来自 stdin。脚本里任何读 stdin 的命令都会吞掉
# 脚本余下部分，安装在中途无声结束。所以每个可能读 stdin 的外部命令都显式 </dev/null。
# 不能用 exec </dev/null：那会切断脚本自己的来源。
set -eu

# 落盘路径都挂在 PROBE_INSTALL_ROOT 下。生产运行时它为空，即真实根目录；脚本的逻辑测试以普通用户
# 把它指向临时目录，连同 PATH 上的 dscl、launchctl、ps 等替身一起运行，不触碰真实系统路径。
# 它只改变本脚本写文件的位置：plist 里的 ProgramArguments 与日志路径是固定的真实路径。
ROOT=${PROBE_INSTALL_ROOT-}
BIN=$ROOT/usr/local/bin/probe-agent
CFG_DIR=$ROOT/etc/probe-agent
CFG=$CFG_DIR/config.json
LOG_DIR=$ROOT/Library/Logs/probe-agent
LABEL=xyz.probe.agent
PLIST=$ROOT/Library/LaunchDaemons/$LABEL.plist
SVC_USER=_probe-agent
REPO=https://github.com/xjetry/probe

usage() {
  echo "usage: install-macos.sh --hub URL --key KEY [--name N] [--version vX.Y.Z] [--base-url URL]" >&2
  echo "       install-macos.sh --uninstall [--purge]" >&2
  exit 2
}

HUB=""; KEY=""; NAME=""; VERSION=""; BASE_URL=""; UNINSTALL=0; PURGE=0
need_value() { [ "$#" -ge 2 ] || usage; }
while [ $# -gt 0 ]; do
  case "$1" in
    --hub) need_value "$@"; HUB=$2; shift 2;;
    --key) need_value "$@"; KEY=$2; shift 2;;
    --name) need_value "$@"; NAME=$2; shift 2;;
    --version) need_value "$@"; VERSION=$2; shift 2;;
    --base-url) need_value "$@"; BASE_URL=$2; shift 2;;
    --uninstall) UNINSTALL=1; shift;;
    --purge) PURGE=1; shift;;
    *) usage;;
  esac
done
[ "$PURGE" = 0 ] || [ "$UNINSTALL" = 1 ] || usage

[ "$(id -u)" = 0 ] || { echo "install-macos.sh must run as root" >&2; exit 1; }

# 停服务后确认进程退出的轮询间隔（秒）与次数上限：sleep 合计最多 10 秒，外加每轮一次 ps。
STOP_POLL_INTERVAL=1
STOP_POLL_MAX=10
# 启动后等到进程出现的上限，同一口径。出现之后再固定等 3 秒，确认还是同一个 pid。
START_POLL_INTERVAL=1
START_POLL_MAX=10

# 列出有效 uid 为 $1 的进程 pid（空格分隔，写入 svc_pids）。BSD ps 的 uid 列是有效 uid（ps(1)）。
scan_uid_pids() {
  procs=$(ps -axo uid=,pid=) || { echo "cannot list processes" >&2; return 1; }
  svc_pids=$(printf '%s\n' "$procs" | awk -v u="$1" '$1 == u { printf " %s", $2 }')
}

# 确认没有以服务用户身份运行的进程。它不看 launchd 报告的状态，也不依赖 plist 是否还在：
# plist 被手工删掉而进程仍在时也只有这一步抓得到。
# 判据"有效 uid 等于服务用户的 uid"与"是本服务的进程"等价，两个方向各有担保：
# - 以服务用户运行的进程都属于本服务：$SVC_USER 是专供 agent 的账户（登录 shell /usr/bin/false），由 create_account 建立。
# - 本服务的每个进程都以服务用户运行：由 plist 的 UserName（deploy/launchd/xyz.probe.agent.plist）保证。
#   plist 若改以其他身份运行任何进程，这里会漏查，必须同步改判据。
# 用户不存在时没有可比对的 uid，直接通过；此时查不到仍在运行的旧进程，与 Linux 脚本相同。
confirm_service_stopped() {
  id "$SVC_USER" >/dev/null 2>&1 || return 0
  svc_uid=$(id -u "$SVC_USER")
  polls=0
  while :; do
    scan_uid_pids "$svc_uid" || return 1
    [ -n "$svc_pids" ] || return 0
    [ "$polls" -lt "$STOP_POLL_MAX" ] || break
    sleep "$STOP_POLL_INTERVAL"
    polls=$((polls + 1))
  done
  echo "probe-agent is still running: processes with uid $svc_uid ($SVC_USER):$svc_pids" >&2
  return 1
}

# 启动之后确认服务进程活着：bootstrap 返回 0 只说明作业已载入，证明不了子进程没有秒退。
# 先等到出现有效 uid 为服务用户的进程，记下 pid，3 秒后同一个 pid 仍在；
# 秒退再被 KeepAlive 拉起会换成新 pid，不能算起来了。判据与 confirm_service_stopped 同一处。
start_log_hint() { echo "see $LOG_DIR/probe-agent.err" >&2; }
confirm_service_started() {
  svc_uid=$(id -u "$SVC_USER") || { echo "no service user $SVC_USER" >&2; start_log_hint; return 1; }
  polls=0
  pid=""
  while :; do
    scan_uid_pids "$svc_uid" || return 1
    if [ -n "$svc_pids" ]; then
      pid=${svc_pids# }
      pid=${pid%% *}
      break
    fi
    [ "$polls" -lt "$START_POLL_MAX" ] || break
    sleep "$START_POLL_INTERVAL"
    polls=$((polls + 1))
  done
  if [ -z "$pid" ]; then
    echo "probe-agent did not start" >&2
    start_log_hint
    return 1
  fi
  sleep 3
  scan_uid_pids "$svc_uid" || return 1
  case " $svc_pids " in
    *" $pid "*) return 0;;
  esac
  echo "probe-agent did not stay running (pid $pid)" >&2
  start_log_hint
  return 1
}

# 是否发 bootout 看作业是否已载入 system 域，而不是 plist 文件在不在：launchd 按已载入的作业管进程，
# 文件在而作业未载入时没有可停的东西（bootout 会以 3 失败），作业已载入而文件被删时进程照样在跑。
# bootout 失败即失败；确认一步不依赖它，总是执行。
service_loaded() { launchctl print "system/$LABEL" >/dev/null 2>&1 </dev/null; }
stop_service() {
  if service_loaded; then
    launchctl bootout "system/$LABEL" </dev/null || { echo "failed to stop probe-agent" >&2; return 1; }
  fi
  confirm_service_stopped
}

group_exists() { dscl . -read "/Groups/$SVC_USER" PrimaryGroupID >/dev/null 2>&1 </dev/null; }

# 用户与同名组都删并回查：不信 dscl 的退出码，也不能半成功还报成功。
delete_account() {
  if id "$SVC_USER" >/dev/null 2>&1; then dscl . -delete "/Users/$SVC_USER" </dev/null; fi
  if group_exists; then dscl . -delete "/Groups/$SVC_USER" </dev/null; fi
  if id "$SVC_USER" >/dev/null 2>&1 || group_exists; then
    echo "failed to delete user or group $SVC_USER" >&2; exit 1
  fi
}

if [ "$UNINSTALL" = 1 ]; then
  stop_service
  # 作业已由 stop_service 卸下；plist 删掉后开机也不再载入。
  rm -f "$PLIST" "$BIN"
  if [ "$PURGE" = 1 ]; then
    rm -rf "$CFG_DIR" "$LOG_DIR"
    delete_account
  fi
  echo "probe-agent uninstalled"
  exit 0
fi

# 卸载不需要架构：不支持的架构不应让人连卸载都做不了。
# 在 Rosetta 转译的终端里 uname -m 报 x86_64，而 hw.optional.arm64 仍为 1：先看它，只看 uname 会在 Apple Silicon 上装 amd64 包。
# 它不存在或不为 1 时按 uname -m 判定；-i 让未知 OID 输出为空而不报错。
if [ "$(sysctl -in hw.optional.arm64 </dev/null)" = 1 ]; then ARCH=arm64
else
  case "$(uname -m)" in
    x86_64) ARCH=amd64;; arm64) ARCH=arm64;;
    *) echo "unsupported architecture: $(uname -m)" >&2; exit 1;;
  esac
fi

# 首次安装必须有注册凭据；升级沿用现有配置，不需要也不接受重新注册。
if [ ! -f "$CFG" ] && { [ -z "$HUB" ] || [ -z "$KEY" ]; }; then
  echo "--hub and --key are required for the first install" >&2
  exit 2
fi

# 300–499 中同时未被用作 UniqueID 与 PrimaryGroupID 的最小值：低于 500 的账户不出现在登录窗口，
# 两个命名空间都空闲的号可以让新建的组与用户同号。
free_id() {
  uids=$(dscl . -list /Users UniqueID </dev/null) || return 1
  gids=$(dscl . -list /Groups PrimaryGroupID </dev/null) || return 1
  printf '%s\n%s\n' "$uids" "$gids" | awk '{ used[$2] = 1 } END { for (i = 300; i < 500; i++) if (!(i in used)) { print i; exit 0 } exit 1 }'
}

# 建用户排在下载与注册之前：它若失败，注册窗口的名额尚未消耗、旧服务尚未停止。
# 主组必须是同名组：plist 的 GroupName 与配置目录的组都写 $SVC_USER。先建组、再以它为主组建用户，建完回查，
# 不信 dscl 的退出码。
create_account() {
  # 用户已在时先核对主组，再决定要不要建组。顺序反了会在主组不符退出时留下本次新建的同名组。
  if id "$SVC_USER" >/dev/null 2>&1; then
    actual_group=$(id -gn "$SVC_USER" 2>/dev/null) || {
      echo "user $SVC_USER exists but its primary group is missing (gid $(id -g "$SVC_USER"))" >&2; exit 1; }
    [ "$actual_group" = "$SVC_USER" ] || {
      echo "user $SVC_USER exists with primary group $actual_group; expected $SVC_USER" >&2; exit 1; }
    return 0
  fi
  if group_exists; then
    gid=$(dscl . -read "/Groups/$SVC_USER" PrimaryGroupID </dev/null | awk '{ print $2 }')
    uid=$(free_id) || { echo "no free id in 300-499 for user $SVC_USER" >&2; exit 1; }
  else
    gid=$(free_id) || { echo "no free id in 300-499 for group $SVC_USER" >&2; exit 1; }
    uid=$gid
    dscl . -create "/Groups/$SVC_USER" PrimaryGroupID "$gid" </dev/null
    dscl . -create "/Groups/$SVC_USER" RealName "probe agent" </dev/null
    dscl . -create "/Groups/$SVC_USER" Password '*' </dev/null
  fi
  dscl . -create "/Users/$SVC_USER" UniqueID "$uid" </dev/null
  dscl . -create "/Users/$SVC_USER" PrimaryGroupID "$gid" </dev/null
  dscl . -create "/Users/$SVC_USER" UserShell /usr/bin/false </dev/null
  dscl . -create "/Users/$SVC_USER" NFSHomeDirectory /var/empty </dev/null
  dscl . -create "/Users/$SVC_USER" RealName "probe agent" </dev/null
  dscl . -create "/Users/$SVC_USER" Password '*' </dev/null
  dscl . -create "/Users/$SVC_USER" IsHidden 1 </dev/null
  id "$SVC_USER" >/dev/null 2>&1 || { echo "failed to create system user $SVC_USER" >&2; exit 1; }
  actual_group=$(id -gn "$SVC_USER" 2>/dev/null) || {
    echo "user $SVC_USER exists but its primary group is missing (gid $(id -g "$SVC_USER"))" >&2; exit 1; }
  [ "$actual_group" = "$SVC_USER" ] || {
    echo "user $SVC_USER exists with primary group $actual_group; expected $SVC_USER" >&2; exit 1; }
}
create_account

# 给了 --base-url 时它就是下载目录，--version 不参与地址。与 --key 被忽略时一样说出来，
# 避免人以为钉住了版本。
if [ -n "$BASE_URL" ] && [ -n "$VERSION" ]; then
  echo "--base-url is the download directory (--version ignored)"
fi
[ -n "$BASE_URL" ] || {
  if [ -n "$VERSION" ]; then BASE_URL="$REPO/releases/download/$VERSION"
  else BASE_URL="$REPO/releases/latest/download"; fi
}

# 缺下载器或 shasum 时在任何网络操作之前退出。macOS 自带二者与系统 CA，不需要装 CA 的分支。
PKG="probe-agent_darwin_$ARCH.tar.gz"
command -v curl >/dev/null 2>&1 || { echo "curl is required to download $PKG" >&2; exit 1; }
command -v shasum >/dev/null 2>&1 || { echo "shasum is required to verify downloads" >&2; exit 1; }

work=$(mktemp -d)
BIN_TMP="$BIN.tmp.$$"
# EXIT trap 覆盖正常结束、exit 与 set -e 触发的退出；INT、TERM、HUP 转成 exit 1，
# Ctrl-C 或 SSH 断开时也会清掉工作目录与写了一半的临时二进制。
trap 'rm -rf "$work"; rm -f "$BIN_TMP"' EXIT
trap 'exit 1' INT TERM HUP

is_https() { case "$1" in https://*) return 0;; esac; return 1; }
dl() {
  # 下载地址为 https 时，请求和重定向都只走 https。其他协议（本地验收）不加：--proto '=https' 会拒绝它。
  if is_https "$1"; then curl --proto '=https' --proto-redir '=https' -fsSL -o "$2" "$1"
  else curl -fsSL -o "$2" "$1"; fi
}
dl "$BASE_URL/$PKG" "$work/$PKG"
dl "$BASE_URL/SHA256SUMS" "$work/SHA256SUMS"
# SHA256SUMS 含全部资产，只核对本包那一行；行格式是 64 位十六进制、空白、文件名。
(cd "$work" && awk -v p="$PKG" '$2 == p' SHA256SUMS > verify.txt)
[ -s "$work/verify.txt" ] || {
  echo "SHA256SUMS has no entry for $PKG" >&2; exit 1
}
(cd "$work" && shasum -a 256 -c verify.txt)

# 解包、检查包内文件、写临时二进制都在停服务之前做完：这些准备失败时，正在运行的旧服务不受影响。
tar -xzf "$work/$PKG" -C "$work"
for f in probe-agent "$LABEL.plist"; do
  [ -f "$work/$f" ] || { echo "package is missing $f" >&2; exit 1; }
done
mkdir -p "$(dirname "$BIN")"
install -m 0755 "$work/probe-agent" "$BIN_TMP"
stop_service
# 同目录 rename 原子替换目录项：launchd 执行 $BIN 时看到的始终是完整的旧文件或完整的新文件。
mv -f "$BIN_TMP" "$BIN"

if [ ! -f "$CFG" ]; then
  # 注册只在没有配置时发生；配置落盘后重跑不再注册，所以注册之后的步骤失败时，重跑不会多耗窗口名额。
  set -- register --hub "$HUB" --key "$KEY" --config "$CFG"
  if [ -n "$NAME" ]; then set -- "$@" --name "$NAME"; fi
  "$BIN" "$@" </dev/null
else
  if [ -n "$KEY" ]; then echo "existing config found; keeping the current registration (--key ignored)"; fi
  if [ -n "$NAME" ]; then echo "existing config found; --name ignored"; fi
fi
# 每次安装都做，不只在注册之后（手工重新注册、注册后被打断、账户重建后 uid 变了，都会留下服务用户读不到的配置）。
# 目录属 root、0750：服务用户不能增删目录项，这里的 chown 不会被链接劫持。
# 目录无需对服务用户可写：写配置只发生在以 root 执行的 register 里，run 只读配置。
chown "root:$SVC_USER" "$CFG_DIR"
chmod 0750 "$CFG_DIR"
chown "$SVC_USER:$SVC_USER" "$CFG"

# 日志目录属 root：launchd 以 root 身份按 plist 的 UserName 属主创建日志文件，服务用户不需要写目录。
mkdir -p "$LOG_DIR"
chown root:wheel "$LOG_DIR"
chmod 0755 "$LOG_DIR"

# plist 每次覆盖，改动随升级下发。它决定以什么身份运行什么程序，只能由 root 改：root:wheel、0644。
# enable 清掉可能残留的禁用覆盖（launchctl disable 跨重启有效），与 systemctl enable 同位。
# KeepAlive 隐含 RunAtLoad（launchd.plist(5)），bootstrap 即启动。
# 走到这里时没有以服务用户运行的进程：stop_service 已确认。
mkdir -p "$(dirname "$PLIST")"
install -m 0644 "$work/$LABEL.plist" "$PLIST"
chown root:wheel "$PLIST"
launchctl enable "system/$LABEL" </dev/null
launchctl bootstrap system "$PLIST" </dev/null
confirm_service_started
echo "probe-agent installed and started (launchd, $ARCH, $PKG)"
```

```sh
cd /Users/xjetry/work/vibe/probe-macos && chmod +x deploy/install-macos.sh && shellcheck -s sh deploy/install-macos.sh > /tmp/m6mac-t4-sc.log 2>&1; echo $?
```

Expected: `0`。

`Makefile` 的 `lint`：

```diff
-	shellcheck -s sh deploy/install.sh deploy/openrc/probe-agent
+	shellcheck -s sh deploy/install.sh deploy/install-macos.sh deploy/openrc/probe-agent
```

- [ ] **Step 5: 跑绿（macOS 的 /bin/sh 与 Linux 容器的 dash）**

```sh
cd /Users/xjetry/work/vibe/probe-macos && go test -count=1 -v ./deploy > /tmp/m6mac-t4-green.log 2>&1; echo $?
cd /Users/xjetry/work/vibe/probe-macos && docker run --rm -v "$PWD:/src" -v "$(go env GOMODCACHE):/go/pkg/mod" -w /src -e GOFLAGS=-buildvcs=false golang:1.27.1-bookworm go test -count=1 -v ./deploy > /tmp/m6mac-t4-linux.log 2>&1; echo $?
```

Expected: 两个都是 `0`，各有 11 个 `--- PASS`。本机约 20 秒（每个用例起若干个 shell 进程），容器里不到 1 秒。

- [ ] **Step 6: 缺陷注入**

每条先 `git add deploy` 建基准，注入后 `git diff --stat` 非空，跑 `go test -count=1 -run '<用例>' ./deploy > /tmp/m6mac-t4-injN.log 2>&1; echo $?` 期望 `1`，`git checkout -- deploy` 还原：

1. 注册那行 `"$BIN" "$@" </dev/null` 去掉 `</dev/null` → `TestFreshInstallFromStdin`：`exit 0, want the final line …`，输出停在 `registered as node 1; …`（假 agent 吞掉了脚本余下部分）。
2. `service_loaded` 改成 `service_loaded() { [ -e "$PLIST" ]; }` → `TestUnloadedJobIsNotBootedOut`：输出含 `Boot-out failed: 3: No such process` 与 `failed to stop probe-agent`。
3. 架构判定的 `if [ "$(sysctl -in hw.optional.arm64 </dev/null)" = 1 ]; then ARCH=arm64` 改成 `if false; then :` → `TestArchitectureFromHardwareNotShell`：`arm64="1" uname="x86_64": exit 0, want the arm64 package`。
4. 删掉 `create_account` 末尾的回查（`id "$SVC_USER" … failed to create system user` 一行与其后的 `actual_group` 两段）→ `TestAccountCreationIsVerified`：`exit 1`，但输出里没有 `failed to create system user`，而是已经 `registered as node 1`（下载与注册都在账户不存在时发生了）。
5. `stop_service` 里删掉 `confirm_service_stopped` → `TestLingeringProcessBlocksReplacement`：`exit 0`。
6. `confirm_service_started` 里把 `sleep 3` 与其后的 `scan_uid_pids` 两行换成 `return 0` → `TestStartIsConfirmedBySamePID`：`STUB_RESPAWN=1: exit 0, want "probe-agent did not stay running (pid 4243)"`。
7. 删掉 `delete_account` 末尾的回查 → `TestUninstallAndPurge`：`undeleted account must fail purge, exit 0`。
8. plist 的 `UserName` 改成 `_probe` → `TestPlistAgreesWithScript`：`plist lacks "<key>UserName</key>\n\t<string>_probe-agent</string>"`。
9. 删掉 `(cd "$work" && shasum -a 256 -c verify.txt)` → `TestChecksumMismatchLeavesRunningServiceAlone`：`exit 0, want shasum failure`。

- [ ] **Step 7: make ci**

```sh
cd /Users/xjetry/work/vibe/probe-macos && make ci > /tmp/m6mac-t4-ci.log 2>&1; echo $?
```

Expected: `0`。

- [ ] **Step 8: 提交**

```sh
cd /Users/xjetry/work/vibe/probe-macos && git add deploy Makefile && git commit -m "deploy: macOS 的 LaunchDaemon 与安装脚本，脚本逻辑以 PATH 替身测试"
```

---

### Task 5: `make release` 发布 darwin 产物

**Files:**
- Modify: `Makefile`（发布矩阵变量、`release`）

**Interfaces:**
- Consumes：Task 3 的 darwin 构建；Task 4 的 `deploy/launchd/xyz.probe.agent.plist`、`deploy/install-macos.sh`。
- Produces：`dist/probe-agent_darwin_amd64.tar.gz`、`dist/probe-agent_darwin_arm64.tar.gz`（各含 `probe-agent` 与 `xyz.probe.agent.plist`）、`dist/install-macos.sh`；`dist/SHA256SUMS` 由现有的 `sha256sum probe-*.tar.gz` 自然包含 darwin 两行；`release.yml` 的 `gh release create … dist/*` 自然上传它们，本计划不改 `release.yml`。Task 6 与 README 用这些名字。

- [ ] **Step 1: 跑红**

```sh
cd /Users/xjetry/work/vibe/probe-macos && make release VERSION=v0.0.0-m6mac > /tmp/m6mac-t5-red-release.log 2>&1; echo $?
cd /Users/xjetry/work/vibe/probe-macos && ls dist/probe-agent_darwin_amd64.tar.gz dist/probe-agent_darwin_arm64.tar.gz dist/install-macos.sh > /tmp/m6mac-t5-red.log 2>&1; echo $?
```

Expected: 第一条 `0`，第二条非 0，日志三行 `No such file or directory`。

- [ ] **Step 2: 改 Makefile**

```diff
-# 发布产物矩阵：agent 五个 Linux 架构，hub 两个。架构集合只在这两个变量维护，
+# 发布产物矩阵：agent 五个 Linux 架构与两个 darwin 架构，hub 两个 Linux 架构。架构集合只在这三个变量维护，
 # 静态门禁与打包清单都由它们展开，不存在第二份文件清单。
 AGENT_LINUX_ARCHES := amd64 arm64 armv7 386 riscv64
+AGENT_DARWIN_ARCHES := amd64 arm64
 HUB_LINUX_ARCHES := amd64 arm64
@@
 # 说明写在 recipe 之外：recipe 是反斜杠续行拼成的一条 shell 命令，行内的 # 会把其后的续行一并注释掉。
+# 静态门禁只收 Linux 产物：它守的是"与 libc 无关"（不带 PT_INTERP 与 DT_NEEDED），这是 Linux 发行版之间的约束。
+# darwin 产物是 Mach-O，依赖 /usr/lib/libSystem.B.dylib 与 libresolv.9.dylib（otool -L）：Go 在 darwin 上经 libSystem
+# 发起系统调用，macOS 也不提供静态链接的系统库，"不带动态依赖"在这里不成立也无须成立。
+# darwin 的 CGO_ENABLED=0 由下面的显式 env 承载。
 # VERSION 在构建前端之前检查：缺参时立即报错，不等 web 目标跑完。
 release:
 	@if [ -z "$(VERSION)" ]; then echo "VERSION is required, e.g. make release VERSION=v0.1.0" >&2; exit 1; fi
 	$(MAKE) web
-	rm -rf dist/build dist/*.tar.gz dist/SHA256SUMS dist/install.sh
+	rm -rf dist/build dist/*.tar.gz dist/SHA256SUMS dist/install.sh dist/install-macos.sh
 	mkdir -p dist/build
 	@set -e; for arch in $(AGENT_LINUX_ARCHES); do \
 	  case $$arch in armv7) gflags="GOARCH=arm GOARM=7" ;; *) gflags="GOARCH=$$arch" ;; esac; \
 	  env GOOS=linux CGO_ENABLED=0 $$gflags go build -trimpath -ldflags "-X main.version=$(VERSION)" -o "dist/build/probe-agent-linux-$$arch" ./cmd/agent; \
 	done; \
+	for arch in $(AGENT_DARWIN_ARCHES); do \
+	  env GOOS=darwin GOARCH=$$arch CGO_ENABLED=0 go build -trimpath -ldflags "-X main.version=$(VERSION)" -o "dist/build/probe-agent-darwin-$$arch" ./cmd/agent; \
+	done; \
 	for arch in $(HUB_LINUX_ARCHES); do \
@@
 	  COPYFILE_DISABLE=1 tar --no-xattrs -C "$$pkg" -czf "dist/probe-agent_linux_$$arch.tar.gz" probe-agent probe-agent.service probe-agent.openrc; \
 	  rm -rf "$$pkg"; \
 	done; \
+	for arch in $(AGENT_DARWIN_ARCHES); do \
+	  pkg="dist/pkg-darwin-$$arch"; mkdir -p "$$pkg"; \
+	  cp "dist/build/probe-agent-darwin-$$arch" "$$pkg/probe-agent"; \
+	  cp deploy/launchd/xyz.probe.agent.plist "$$pkg/xyz.probe.agent.plist"; \
+	  COPYFILE_DISABLE=1 tar --no-xattrs -C "$$pkg" -czf "dist/probe-agent_darwin_$$arch.tar.gz" probe-agent xyz.probe.agent.plist; \
+	  rm -rf "$$pkg"; \
+	done; \
 	for arch in $(HUB_LINUX_ARCHES); do \
@@
 	rm -rf dist/build
 	cp deploy/install.sh dist/install.sh
+	cp deploy/install-macos.sh dist/install-macos.sh
 	cd dist && sha256sum probe-*.tar.gz > SHA256SUMS
```

`go run ./scripts/checkstatic …` 那一行不动：它由 `AGENT_LINUX_ARCHES` 与 `HUB_LINUX_ARCHES` 展开，darwin 产物不进去。

- [ ] **Step 3: 产出与核对**

```sh
cd /Users/xjetry/work/vibe/probe-macos && make release VERSION=v0.0.0-m6mac > /tmp/m6mac-t5-release.log 2>&1; echo $?
cd /Users/xjetry/work/vibe/probe-macos && grep -c '_darwin_' dist/SHA256SUMS > /tmp/m6mac-t5-sums-count.log 2>&1; echo $?
cd /Users/xjetry/work/vibe/probe-macos/dist && shasum -a 256 -c SHA256SUMS > /tmp/m6mac-t5-sums.log 2>&1; echo $?
cd /Users/xjetry/work/vibe/probe-macos && cmp dist/install-macos.sh deploy/install-macos.sh > /tmp/m6mac-t5-cmp.log 2>&1; echo $?
cd /Users/xjetry/work/vibe/probe-macos && rm -rf /tmp/m6mac-t5-x && mkdir -p /tmp/m6mac-t5-x/amd64 /tmp/m6mac-t5-x/arm64 && tar -xzf dist/probe-agent_darwin_arm64.tar.gz -C /tmp/m6mac-t5-x/arm64 && tar -xzf dist/probe-agent_darwin_amd64.tar.gz -C /tmp/m6mac-t5-x/amd64 && ls /tmp/m6mac-t5-x/arm64 /tmp/m6mac-t5-x/amd64 > /tmp/m6mac-t5-ls.log 2>&1; echo $?
cd /Users/xjetry/work/vibe/probe-macos && /tmp/m6mac-t5-x/arm64/probe-agent version > /tmp/m6mac-t5-v-arm64.log 2>&1; echo $?
cd /Users/xjetry/work/vibe/probe-macos && arch -x86_64 /tmp/m6mac-t5-x/amd64/probe-agent version > /tmp/m6mac-t5-v-amd64.log 2>&1; echo $?
cd /Users/xjetry/work/vibe/probe-macos && codesign -dv /tmp/m6mac-t5-x/arm64/probe-agent > /tmp/m6mac-t5-codesign.log 2>&1; echo $?
cd /Users/xjetry/work/vibe/probe-macos && go version -m /tmp/m6mac-t5-x/arm64/probe-agent > /tmp/m6mac-t5-buildinfo.log 2>&1; echo $?
```

Expected: 全部 `0`。`m6mac-t5-sums-count.log` 为 `2`；`m6mac-t5-sums.log` 九行 `OK`（agent 五个 Linux 包、两个 darwin 包，hub 两个包）；两个解包目录都恰好是 `probe-agent` 与 `xyz.probe.agent.plist`；两个 `version` 日志都是 `v0.0.0-m6mac`；`codesign` 日志含 `flags=0x20002(adhoc,linker-signed)`；`buildinfo` 日志含 `build	CGO_ENABLED=0`、`build	-trimpath=true` 与 `dep	github.com/ebitengine/purego	v0.11.1`。`m6mac-t5-release.log` 里 checkstatic 那一行只列 `probe-agent-linux-*` 与 `probe-hub-linux-*`。

- [ ] **Step 4: ubuntu 上交叉编译的产物能在本机起来**

release 流水线跑在 ubuntu 上；在 Linux 容器里按 `release` 同样的参数交叉编译 darwin/arm64，拷回本机运行：

```sh
cd /Users/xjetry/work/vibe/probe-macos && docker run --rm -v "$PWD:/src" -v "$(go env GOMODCACHE):/go/pkg/mod" -w /src -e GOOS=darwin -e GOARCH=arm64 -e CGO_ENABLED=0 -e GOFLAGS=-buildvcs=false golang:1.27.1-bookworm go build -trimpath -ldflags "-X main.version=v0.0.0-xlinux" -o /src/dist/xlinux-probe-agent ./cmd/agent > /tmp/m6mac-t5-xbuild.log 2>&1; echo $?
cd /Users/xjetry/work/vibe/probe-macos && dist/xlinux-probe-agent version > /tmp/m6mac-t5-xrun.log 2>&1; echo $?
cd /Users/xjetry/work/vibe/probe-macos && codesign -dv dist/xlinux-probe-agent > /tmp/m6mac-t5-xcs.log 2>&1; echo $?; rm -f dist/xlinux-probe-agent
```

Expected: 三个 `0`；`xrun` 日志 `v0.0.0-xlinux`；`xcs` 日志含 `adhoc,linker-signed`（Apple Silicon 拒绝执行未签名的 arm64 代码，签名由 Go 链接器写入，与构建主机无关）。

- [ ] **Step 5: 缺陷注入**

先 `git add Makefile` 建基准。`AGENT_DARWIN_ARCHES := amd64 arm64` 改成 `AGENT_DARWIN_ARCHES := arm64`；`git diff --stat` 非空；重跑 Step 3 的前两条：`m6mac-t5-sums-count.log` 为 `1`（期望 `2`，红）。`git checkout -- Makefile` 还原，重跑 `make release VERSION=v0.0.0-m6mac` 回到 `2`。

另记一条证据（不是断言）：`cd /Users/xjetry/work/vibe/probe-macos && go run ./scripts/checkstatic /tmp/m6mac-t5-x/arm64/probe-agent > /tmp/m6mac-t5-checkstatic.log 2>&1; echo $?` 为 `1`，日志 `not an ELF file`——darwin 产物交给静态门禁只会被当成格式错误拒收，这是它不进门禁的直接原因。

- [ ] **Step 6: make ci**

```sh
cd /Users/xjetry/work/vibe/probe-macos && make ci > /tmp/m6mac-t5-ci.log 2>&1; echo $?
```

Expected: `0`。

- [ ] **Step 7: 提交**

```sh
cd /Users/xjetry/work/vibe/probe-macos && git add Makefile && git commit -m "build: 发布 darwin/amd64、arm64 的 agent 包与 install-macos.sh"
```

---

### Task 6: 本机端到端验收 `scripts/macos-accept.sh`

**Files:**
- Create: `scripts/macos-accept.sh`

**Interfaces:**
- Consumes：Task 5 的 `make release` 产物与包内 plist；hub 的 `window open`、`passwd`、`serve`（`cmd/hub`，不改）；`AdminService` 的 `Login`、`GetSnapshot`、`ListNodes`、`SaveProbeTask`、`QueryProbes`（JSON，字段名同 `scripts/e2e.sh`）。
- Produces：可重复运行、不 sudo 的验收脚本；成功的最后一行 `MACOS ACCEPT OK (<arch>)`。

脚本做的事：普通用户起 hub（TTL 12 秒、上报间隔 4 秒）→ 用发布包里的二进制 `register` 与 `run` → 快照里 19 个指标字段都有值、`bootId` 等于 `sysctl -n kern.bootsessionuuid`、`memTotal` 等于 `hw.memsize` → facts 与 `sw_vers`、`uname -r`、`sysctl` 一致且 `icmpAvailable` 为真 → ICMP（127.0.0.1）与 TCP 任务都有 RTT 样本、无 error（§13 第 2 项）→ agent 停着时在回环上走 64 MiB，重启后总量至少涨 64 MiB（Review Focus 3）→ 把包里的 plist 改成用户域作业交给 launchd：日志文件由 launchd 创建、hub 停 15 秒期间 pid 不变、恢复后重新在线、被杀后拉起、第二次拉起与上一次相隔约 5 秒（Review Focus 5）→ 日志里没有 `partial collection` → `bootout` 后进程不在。

- [ ] **Step 1: 写脚本**

`scripts/macos-accept.sh`（`chmod +x`）：

```sh
#!/bin/sh
# macOS agent 的本机验收：以普通用户在本机起 hub，用本次 make release 的 darwin 产物注册并运行 agent，
# 经管理 API（curl + jq）断言各指标、facts、ICMP 与流量差分；再把包里的 plist 改成用户域作业交给 launchd，
# 验证 KeepAlive 拉起、ThrottleInterval 与日志文件由 launchd 创建。
# 不 sudo，不执行安装脚本：system 域、专用账户与 root 属主由 README 的真机清单验证。
# 端口 18087/18088 与 e2e（18080/18081）、install-accept（18085/18086）错开。
set -eu
cd "$(cd "$(dirname "$0")/.." && pwd)"
[ "$(uname -s)" = Darwin ] || { echo "macos-accept.sh runs on macOS only" >&2; exit 2; }
# 以 root 跑会掩盖本验收要证明的事：非特权采集与非特权 ICMP（spec §13 第 2 项）。
[ "$(id -u)" != 0 ] || { echo "run macos-accept.sh as a normal user" >&2; exit 2; }

VERSION=v0.0.0-macos-accept
PORT=18087
BLOB_PORT=18088
base="http://127.0.0.1:$PORT"
uid=$(id -u)
label="xyz.probe.agent.accept.$$"
work=$(mktemp -d)
echo "work=$work"
admin_pw="macos accept password 2026"
hub=""; agent=""; blob=""; job=""; hub_starts=0

cleanup() {
  if [ -n "$job" ]; then launchctl bootout "gui/$uid/$label" > /dev/null 2>&1 || true; fi
  for p in $agent $blob $hub; do kill "$p" 2>/dev/null || true; wait "$p" 2>/dev/null || true; done
}
trap cleanup EXIT

if [ "$(sysctl -in hw.optional.arm64)" = 1 ]; then arch=arm64; else arch=amd64; fi

make release VERSION="$VERSION" > "$work/release.log" 2>&1 || { echo "FAIL: make release"; tail -20 "$work/release.log"; exit 1; }
for a in amd64 arm64; do
  mkdir -p "$work/pkg-$a"
  tar -xzf "dist/probe-agent_darwin_$a.tar.gz" -C "$work/pkg-$a"
  [ -x "$work/pkg-$a/probe-agent" ] && [ -f "$work/pkg-$a/xyz.probe.agent.plist" ] || { echo "FAIL: darwin/$a package contents"; ls -l "$work/pkg-$a"; exit 1; }
done
bin="$work/pkg-$arch/probe-agent"
[ "$("$bin" version)" = "$VERSION" ] || { echo "FAIL: $arch binary version"; exit 1; }
# Apple Silicon 上 amd64 产物经 Rosetta 执行：Linux 上交叉编译的 Mach-O 也必须能在 macOS 上起来（ad-hoc 签名由 Go 链接器写入）。
if [ "$arch" = arm64 ]; then
  if arch -x86_64 /usr/bin/true 2>/dev/null; then
    [ "$(arch -x86_64 "$work/pkg-amd64/probe-agent" version)" = "$VERSION" ] || { echo "FAIL: amd64 binary under Rosetta"; exit 1; }
  else
    echo "note: Rosetta is not installed; the amd64 binary was not executed"
  fi
fi
plutil -lint "$work/pkg-$arch/xyz.probe.agent.plist" > /dev/null || { echo "FAIL: packaged plist does not lint"; exit 1; }

go build -o "$work/probe-hub" ./cmd/hub
"$work/probe-hub" window open --db "$work/hub.db" --ttl 10m --max 1 > "$work/window.txt"
key=$(sed -n 's/^key: //p' "$work/window.txt")
[ -n "$key" ] || { echo "FAIL: no window key"; exit 1; }

# TTL 12s → 上报间隔 4s、agent 退避上限 12s（spec §4.4）。
start_hub() {
  PROBE_OFFLINE_AFTER=12s "$work/probe-hub" serve --db "$work/hub.db" --listen "127.0.0.1:$PORT" --timezone UTC >> "$work/hub.log" 2>&1 &
  hub=$!
  i=0
  until [ "$(curl -s -o /dev/null -w '%{http_code}' "$base/")" = 302 ]; do
    i=$((i + 1)); [ "$i" -lt 50 ] || { echo "FAIL: hub did not answer"; cat "$work/hub.log"; exit 1; }
    sleep 0.2
  done
  # 302 也可能来自占着端口的别的进程：每起一次，日志里就必须多一行 listening。
  hub_starts=$((hub_starts + 1))
  [ "$(grep -c 'hub listening' "$work/hub.log")" = "$hub_starts" ] || { echo "FAIL: hub did not bind $PORT"; cat "$work/hub.log"; exit 1; }
}
stop_hub() { kill "$hub"; wait "$hub" || true; hub=""; }
start_hub
printf '%s\n' "$admin_pw" | "$work/probe-hub" passwd --db "$work/hub.db" > "$work/passwd.log" 2>&1

: > "$work/jar"
rpc() {
  name=$1; body=$2
  curl -sS -o "$work/$name.json" -w '%{http_code}' -H 'Content-Type: application/json' \
    -b "$work/jar" -c "$work/jar" --data "$body" "$base/probe.v1.AdminService/$name"
}
login() { [ "$(rpc Login "$(jq -nc --arg p "$admin_pw" '{password: $p}')")" = 200 ] || { echo "FAIL: login"; exit 1; }; }
login
# until_node <秒> <jq 表达式>：在上限内轮询快照，直到唯一节点满足表达式。
until_node() {
  deadline=$(($(date +%s) + $1))
  until [ "$(rpc GetSnapshot '{}')" = 200 ] && jq -e ".nodes | length == 1 and (.[0] | $2)" "$work/GetSnapshot.json" > /dev/null; do
    [ "$(date +%s)" -lt "$deadline" ] || { echo "FAIL: node did not satisfy $2 within $1s"; cat "$work/GetSnapshot.json"; exit 1; }
    sleep 0.5
  done
}

"$bin" register --hub "$base" --key "$key" --config "$work/agent.json" --name macos-accept > "$work/register.log" 2>&1 || { echo "FAIL: register"; cat "$work/register.log"; exit 1; }
"$bin" run --config "$work/agent.json" > "$work/agent.log" 2>&1 &
agent=$!

# cpu_pct 与 net_*_bps 要两次采样才有；第二次上报后每个字段都必须在。
until_node 60 '.online and .metrics.cpuPct != null and .metrics.netRxBps != null'
jq -e '.nodes[0].metrics | [.bootId, .cpuPct, .load1, .load5, .load15, .memTotal, .memUsed, .swapTotal, .swapUsed,
  .diskTotal, .diskUsed, .netRxTotal, .netTxTotal, .netRxBps, .netTxBps, .tcpConns, .udpConns, .procs, .uptimeS] | all(. != null)' \
  "$work/GetSnapshot.json" > /dev/null || { echo "FAIL: missing metrics"; cat "$work/GetSnapshot.json"; exit 1; }
jq -e --arg boot "$(sysctl -n kern.bootsessionuuid)" --arg mem "$(sysctl -n hw.memsize)" \
  '.nodes[0].metrics | .bootId == $boot and .memTotal == $mem and (.memUsed | tonumber) <= (.memTotal | tonumber) and (.diskUsed | tonumber) <= (.diskTotal | tonumber)' \
  "$work/GetSnapshot.json" > /dev/null || { echo "FAIL: boot id or memory"; cat "$work/GetSnapshot.json"; exit 1; }
node=$(jq -r '.nodes[0].id' "$work/GetSnapshot.json")

[ "$(rpc ListNodes '{}')" = 200 ] || { echo "FAIL: ListNodes"; exit 1; }
jq -e --arg os "macOS $(sw_vers -productVersion)" --arg kernel "$(uname -r)" --arg arch "$arch" \
  --arg cpu "$(sysctl -n machdep.cpu.brand_string)" --argjson cores "$(sysctl -n hw.logicalcpu)" --arg v "$VERSION" \
  '.nodes[0].facts | .os == $os and .kernel == $kernel and .arch == $arch and .cpuModel == $cpu and .cpuCores == $cores and .agentVersion == $v and .icmpAvailable == true' \
  "$work/ListNodes.json" > /dev/null || { echo "FAIL: facts"; cat "$work/ListNodes.json"; exit 1; }

# 非特权数据报 ICMP 探测回环、TCP 连 hub：两个任务都要有成功的样本且没有 error。
icmp_body=$(jq -nc --arg n "$node" '{task: {kind: "PROBE_KIND_ICMP", target: "127.0.0.1", intervalS: 5, timeoutMs: 1000}, nodeIds: [$n]}')
[ "$(rpc SaveProbeTask "$icmp_body")" = 200 ] || { echo "FAIL: SaveProbeTask icmp"; cat "$work/SaveProbeTask.json"; exit 1; }
icmp_task=$(jq -r '.task.task.id' "$work/SaveProbeTask.json")
tcp_body=$(jq -nc --arg n "$node" --arg t "127.0.0.1:$PORT" '{task: {kind: "PROBE_KIND_TCP", target: $t, intervalS: 5, timeoutMs: 1000}, nodeIds: [$n]}')
[ "$(rpc SaveProbeTask "$tcp_body")" = 200 ] || { echo "FAIL: SaveProbeTask tcp"; cat "$work/SaveProbeTask.json"; exit 1; }
tcp_task=$(jq -r '.task.task.id' "$work/SaveProbeTask.json")
now=$(date +%s)
probe_body=$(jq -nc --arg n "$node" --argjson from "$((now - 600))" --argjson to "$((now + 600))" '{nodeId: $n, from: $from, to: $to, maxPoints: 100}')
# 结果在分钟桶刷出后才可查：最坏约一分钟加首次偏移与上报间隔。
deadline=$(($(date +%s) + 90))
until [ "$(rpc QueryProbes "$probe_body")" = 200 ] && jq -e --arg i "$icmp_task" --arg t "$tcp_task" \
  '(.series // []) as $s | ([$s[].taskId] | sort) == ([$i, $t] | sort) and all($s[]; any(.samples[]; .sent > 0 and .rttMeanUs != null and (.errors // 0) == 0))' \
  "$work/QueryProbes.json" > /dev/null; do
  [ "$(date +%s)" -lt "$deadline" ] || { echo "FAIL: probe results"; cat "$work/QueryProbes.json" "$work/agent.log"; exit 1; }
  sleep 2
done
echo "icmp and tcp probe results present"

# 流量差分依赖 boot_id 在 agent 重启之间不变（spec §7）：agent 停着时在回环上走一批字节，
# 重启后的首次上报必须把它们计入总量。boot_id 若随进程变化，hub 会只重置基线而丢掉这批字节。
kill "$agent"; wait "$agent" || true; agent=""
"$bin" run --config "$work/agent.json" --net-include lo0 >> "$work/agent.log" 2>&1 &
agent=$!
sleep 9
kill "$agent"; wait "$agent" || true; agent=""
[ "$(rpc GetSnapshot '{}')" = 200 ] || exit 1
before=$(jq -r '.nodes[0].traffic.totalRx // "0"' "$work/GetSnapshot.json")
mkdir -p "$work/blob"
dd if=/dev/zero of="$work/blob/64m" bs=1048576 count=64 2> /dev/null
python3 -m http.server "$BLOB_PORT" --bind 127.0.0.1 --directory "$work/blob" > "$work/blob.log" 2>&1 &
blob=$!
i=0
# 就绪前的连接失败是预期的，只在超过上限时看日志；下载本身由 -f 判定。
until curl -fs -o /dev/null "http://127.0.0.1:$BLOB_PORT/64m"; do
  i=$((i + 1)); [ "$i" -lt 50 ] || { echo "FAIL: blob server"; cat "$work/blob.log"; exit 1; }
  sleep 0.2
done
kill "$blob"; wait "$blob" 2> /dev/null || true; blob=""
"$bin" run --config "$work/agent.json" --net-include lo0 >> "$work/agent.log" 2>&1 &
agent=$!
until_node 30 "(.traffic.totalRx | tonumber) >= ($before | tonumber) + 67108864"
echo "traffic across agent restart: $before -> $(jq -r '.nodes[0].traffic.totalRx' "$work/GetSnapshot.json")"
kill "$agent"; wait "$agent" || true; agent=""

# launchd 用户域：plist 取自发布包，只改 Label、路径，并去掉只对 system 域有效的 UserName/GroupName；
# KeepAlive 与 ThrottleInterval 原样保留，验证的就是它们。日志目录事先建好、文件不建，看 launchd 是否创建。
job_plist="$work/$label.plist"
cp "$work/pkg-$arch/xyz.probe.agent.plist" "$job_plist"
plutil -replace Label -string "$label" "$job_plist"
plutil -replace ProgramArguments -json "[\"$bin\", \"run\", \"--config\", \"$work/agent.json\"]" "$job_plist"
plutil -remove UserName "$job_plist"
plutil -remove GroupName "$job_plist"
mkdir -p "$work/logs"
plutil -replace StandardOutPath -string "$work/logs/probe-agent.log" "$job_plist"
plutil -replace StandardErrorPath -string "$work/logs/probe-agent.err" "$job_plist"
plutil -lint "$job_plist" > /dev/null
t_job=$(date +%s)
launchctl bootstrap "gui/$uid" "$job_plist"
job=1
job_pid() { launchctl print "gui/$uid/$label" 2> /dev/null | awk '$1 == "pid" && $2 == "=" { print $3; exit }'; }
# wait_new_pid <旧 pid> <秒>：等到作业换成另一个 pid，打印新 pid。
wait_new_pid() {
  deadline=$(($(date +%s) + $2))
  while :; do
    p=$(job_pid)
    if [ -n "$p" ] && [ "$p" != "$1" ]; then echo "$p"; return 0; fi
    [ "$(date +%s)" -lt "$deadline" ] || return 1
    sleep 0.2
  done
}
pid1=$(wait_new_pid none 15) || { echo "FAIL: launchd did not start the job"; launchctl print "gui/$uid/$label"; exit 1; }
# 节点在上一个 agent 停下后仍在 TTL 内显示在线；以作业启动之后的上报时刻为准。
until_node 30 "(.lastSeenAt | tonumber) > $t_job"
[ -f "$work/logs/probe-agent.err" ] && [ -f "$work/logs/probe-agent.log" ] || { echo "FAIL: launchd did not create the log files"; ls -l "$work/logs"; exit 1; }
grep -q 'agent starting' "$work/logs/probe-agent.err" || { echo "FAIL: agent log not in StandardErrorPath"; cat "$work/logs/probe-agent.err"; exit 1; }

# hub 停 15 秒：agent 在进程内退避，不能退出让 launchd 拉起（pid 不变），hub 回来后在退避上限内重新在线。
stop_hub
sleep 15
[ "$(job_pid)" = "$pid1" ] || { echo "FAIL: agent process changed while the hub was down"; exit 1; }
start_hub
login
until_node 30 '.online'
[ "$(job_pid)" = "$pid1" ] || { echo "FAIL: agent process changed across the hub outage"; exit 1; }

# 进程被杀：KeepAlive 拉起。第二次在拉起后立刻再杀，下一次拉起受 ThrottleInterval 节流：
# 与上一次拉起相隔约 5 秒（默认值 10 秒会落在区间外）。
kill -9 "$pid1"
pid2=$(wait_new_pid "$pid1" 15) || { echo "FAIL: KeepAlive did not restart the agent"; exit 1; }
t2=$(date +%s)
kill -9 "$pid2"
pid3=$(wait_new_pid "$pid2" 20) || { echo "FAIL: KeepAlive gave up after a quick second exit"; exit 1; }
gap=$(($(date +%s) - t2))
[ "$gap" -ge 3 ] && [ "$gap" -le 8 ] || { echo "FAIL: respawn after a quick exit took ${gap}s, want about 5 (ThrottleInterval)"; exit 1; }
t3=$(date +%s)
until_node 30 "(.lastSeenAt | tonumber) >= $t3"
echo "launchd: pids $pid1 -> $pid2 -> $pid3, throttled respawn ${gap}s"

# 普通用户下每个来源都读得到：采集失败只让字段缺失并记日志（不以失败显形），所以直接查日志。
if grep -h 'partial collection' "$work/agent.log" "$work/logs/probe-agent.err"; then echo "FAIL: collection errors as a normal user"; exit 1; fi

launchctl bootout "gui/$uid/$label"
job=""
sleep 1
if kill -0 "$pid3" 2> /dev/null; then echo "FAIL: agent survived bootout"; exit 1; fi
echo "MACOS ACCEPT OK ($arch)"
```

```sh
cd /Users/xjetry/work/vibe/probe-macos && chmod +x scripts/macos-accept.sh && shellcheck -s sh scripts/macos-accept.sh > /tmp/m6mac-t6-sc.log 2>&1; echo $?
```

Expected: `0`。

- [ ] **Step 2: 跑验收**

```sh
cd /Users/xjetry/work/vibe/probe-macos && scripts/macos-accept.sh > /tmp/m6mac-t6-accept.log 2>&1; echo $?
cd /Users/xjetry/work/vibe/probe-macos && launchctl list > /tmp/m6mac-t6-jobs.log 2>&1; echo $?
cd /Users/xjetry/work/vibe/probe-macos && grep -c 'xyz.probe.agent.accept' /tmp/m6mac-t6-jobs.log; echo $?
```

Expected: 第一条 `0`，日志末尾依次有 `icmp and tcp probe results present`、`traffic across agent restart: <前> -> <后>`（后 − 前 ≥ 67108864）、`launchd: pids <a> -> <b> -> <c>, throttled respawn 5s`（4–6 秒都正常）、`MACOS ACCEPT OK (arm64)`。后两条：`launchctl list` 为 `0`，`grep -c` 输出 `0` 且退出码 `1`（没有留下作业）。整轮约 3–4 分钟，其中 `make release` 约 1 分钟。

- [ ] **Step 3: 缺陷注入（每条整轮重跑，期望非 0 并看 FAIL 行）**

每条先 `git add` 建基准，注入后 `git diff --stat` 非空，跑 Step 2 第一条，`git checkout -- <文件>` 还原：

1. plist 删掉 `ThrottleInterval` 的键与值 → `FAIL: respawn after a quick exit took 10s, want about 5 (ThrottleInterval)`（9–11 秒都算，launchd 默认 10 秒）。
2. `internal/agent/client/runner.go` 的 `attempt++` 之前插入 `return err`（上报一失败 agent 就退出）→ `FAIL: agent process changed while the hub was down`。
3. `darwinraw.go` 的 `bootID` 返回 `id + "-" + strconv.Itoa(os.Getpid()), nil`（boot_id 随进程变化；补上 `os`、`strconv` 的 import），**同时**把脚本里的 `.bootId == $boot and ` 删掉（否则它先红）→ `FAIL: node did not satisfy (.traffic.totalRx | tonumber) >= … within 30s`（hub 在 boot_id 变化时只换基线，停机期间的 64 MiB 没有入账）。还原脚本、只保留 `bootID` 的注入再跑一次 → `FAIL: boot id or memory`。
4. 脚本里 `plutil -replace StandardErrorPath -string "$work/logs/probe-agent.err"` 的路径改成 `"$work/nologs/probe-agent.err"`（目录不存在）→ `FAIL: launchd did not create the log files` 或 `FAIL: launchd did not start the job`；两者都说明日志文件的创建由 launchd 完成，记下实际是哪一条。

- [ ] **Step 4: make ci 与提交**

```sh
cd /Users/xjetry/work/vibe/probe-macos && make ci > /tmp/m6mac-t6-ci.log 2>&1; echo $?
cd /Users/xjetry/work/vibe/probe-macos && git add scripts/macos-accept.sh && git commit -m "scripts: macOS 本机验收覆盖指标、ICMP、跨 agent 重启的流量差分与 launchd 拉起"
```

Expected: `make ci` 为 `0`。

---

### Task 7: CI 的 macOS 任务

**Files:**
- Modify: `.github/workflows/ci.yml`

**Interfaces:**
- Consumes：Task 1–4 的测试（`./internal/agent/...`、`./cmd/agent/...`、`./deploy/...`）；现有 action 的固定 SHA。
- Produces：`agent-macos` 任务，矩阵 `macos-latest`（Apple Silicon）与 `macos-26-intel`（x64）。

- [ ] **Step 1: 加任务**

```diff
diff --git a/.github/workflows/ci.yml b/.github/workflows/ci.yml
--- a/.github/workflows/ci.yml
+++ b/.github/workflows/ci.yml
@@ -35,3 +35,20 @@ jobs:
           else
             echo "main has no .proto files yet; nothing to compare against"
           fi
+  # darwin 的采集文件带 build tag，Linux 上的 go test 照不到（spec §12）；安装脚本的逻辑测试在这里
+  # 跑的是 macOS 自带的 /bin/sh（bash 3.2 的 sh 模式），ubuntu 上跑的是 dash。
+  # 两格覆盖 Apple Silicon 与 Intel：页大小（16 KiB 与 4 KiB）与 sysctl 的差异只在对应架构的 macOS 内核上显形。
+  agent-macos:
+    strategy:
+      fail-fast: false
+      matrix:
+        runner: [macos-latest, macos-26-intel]
+    runs-on: ${{ matrix.runner }}
+    steps:
+      - uses: actions/checkout@11d5960a326750d5838078e36cf38b85af677262 # v4
+      - uses: actions/setup-go@40f1582b2485089dde7abd97c1529aa768e1baff # v5
+        with:
+          go-version-file: go.mod
+      - run: go test -count=1 ./internal/agent/... ./cmd/agent/... ./deploy/...
+        env:
+          CGO_ENABLED: "0"
```

- [ ] **Step 2: 本机按 CI 的命令跑一遍，并 lint workflow**

```sh
cd /Users/xjetry/work/vibe/probe-macos && CGO_ENABLED=0 go test -count=1 ./internal/agent/... ./cmd/agent/... ./deploy/... > /tmp/m6mac-t7-cmd.log 2>&1; echo $?
cd /Users/xjetry/work/vibe/probe-macos && go run github.com/rhysd/actionlint/cmd/actionlint@v1.7.12 .github/workflows/ci.yml .github/workflows/release.yml > /tmp/m6mac-t7-actionlint.log 2>&1; echo $?
```

Expected: 两个 `0`。actionlint 用 v1.7.12：更早的 v1.7.7 不认识 `macos-26-intel`（报 `label "macos-26-intel" is unknown`）。

- [ ] **Step 3: 缺陷注入**

先 `git add .github/workflows/ci.yml` 建基准。`macos-26-intel` 改成 `macos-26-intl`；`git diff --stat` 非空；重跑 actionlint 期望非 0，日志含 `label "macos-26-intl" is unknown`；`git checkout -- .github/workflows/ci.yml` 还原，复跑为 `0`。

- [ ] **Step 4: make ci 与提交**

```sh
cd /Users/xjetry/work/vibe/probe-macos && make ci > /tmp/m6mac-t7-ci.log 2>&1; echo $?
cd /Users/xjetry/work/vibe/probe-macos && git add .github/workflows/ci.yml && git commit -m "ci: macOS runner 在 Apple Silicon 与 Intel 上跑 agent 与安装脚本的测试"
```

合并并推送后由控制端在 Actions 页确认两格都实际跑完：Intel 格长时间 queued 说明该标签对仓库不可用，改标签，不删格。

---

### Task 8: README（hub 运行、Linux 与 macOS 安装、macOS 真机核对）

**Files:**
- Create: `README.md`

**Interfaces:**
- Consumes：Task 4 的脚本参数与路径、Task 5 的资产名、`probe-hub` 的子命令与 `serve` 参数（`cmd/hub/main.go`、`cmd/hub/serve.go`）、面板注册窗口页生成的 Linux 命令形态（`web/src/pages/RegisterWindow.tsx`）。
- Produces：仓库根的 `README.md`。面板的安装命令只给 Linux，macOS 的命令只在这里（§14）。

- [ ] **Step 1: 写 README**

````markdown
# probe

自托管服务器监控探针：agent 采集主机指标并上报，hub 存储、展示并对外提供查询。设计见 [架构设计](docs/superpowers/specs/2026-09-17-probe-architecture-design.md)。

## 运行 hub

hub 是一个静态链接的二进制，数据在一个 SQLite 文件里。从 [Releases](https://github.com/xjetry/probe/releases/latest) 下载 `probe-hub_linux_<arch>.tar.gz`（amd64、arm64）：

```sh
tar -xzf probe-hub_linux_amd64.tar.gz
./probe-hub passwd --db /var/lib/probe/probe.db   # 设置管理员密码
./probe-hub serve --db /var/lib/probe/probe.db    # 默认监听 127.0.0.1:8080
```

- hub 只监听明文 HTTP，TLS 由反代（Caddy、nginx、CDN）终止；反代地址用 `--trusted-proxies` 声明，否则不信任转发头。
- 管理面板在 `/admin/`。离线判定的时限由环境变量 `PROBE_OFFLINE_AFTER` 设定（默认 30s，10s–180s）。
- 其余参数见 `probe-hub serve -h`；节点、注册窗口与 API token 也可在 hub 主机上用 `probe-hub node|window|token` 管理。

## 安装 agent

先在面板的「注册窗口」开一个窗口拿到 key（或在 hub 主机上 `probe-hub window open`）。重跑安装命令即升级：已有配置时沿用现有注册，不会在 hub 上多出节点。

### Linux

用面板注册窗口页给出的命令，形如：

```sh
curl -fsSL https://github.com/xjetry/probe/releases/latest/download/install.sh | sh -s -- --hub https://probe.example.com --key <key>
```

以 root 运行，支持 systemd 与 OpenRC。卸载：同一条命令把参数换成 `--uninstall`，加 `--purge` 一并删除配置、日志与 `probe-agent` 用户。

### macOS

面板不给 macOS 的命令，用这一条（Apple Silicon 与 Intel 通用，脚本按硬件选包）：

```sh
curl -fsSL https://github.com/xjetry/probe/releases/latest/download/install-macos.sh | sudo sh -s -- --hub https://probe.example.com --key <key>
```

- agent 装成 LaunchDaemon `xyz.probe.agent`，以隐藏的系统用户 `_probe-agent` 运行；二进制在 `/usr/local/bin/probe-agent`，配置在 `/etc/probe-agent/`，日志在 `/Library/Logs/probe-agent/`。
- 参数与 Linux 脚本相同：`--name`、`--version vX.Y.Z`（钉住版本）、`--base-url`（下载目录，用于镜像或本地构建）。
- 卸载：`… | sudo sh -s -- --uninstall`；加 `--purge` 一并删除配置、日志、用户与同名组。
- 默认不计入流量的网卡（回环、隧道与 VPN、桥与虚拟机网卡等）见 `probe-agent run -h`。

#### 真机核对

没有 macOS 虚拟机可做自动验收，改动 `deploy/install-macos.sh` 或 `deploy/launchd/xyz.probe.agent.plist` 后、发版前在一台 Mac 上逐条执行。未发布的构建用 `make release VERSION=v0.0.0-check` 产出 `dist/`，`python3 -m http.server 18086 --directory dist` 提供下载，下面的 `<base>` 即该地址，安装时加 `--base-url <base>`。

1. `curl -fsSL <base>/install-macos.sh | sudo sh -s -- --hub <hub> --key <key> --base-url <base>`：最后一行是 `probe-agent installed and started (launchd, arm64, probe-agent_darwin_arm64.tar.gz)`（Intel 为 amd64）。
2. `dscl . -read /Users/_probe-agent UniqueID PrimaryGroupID UserShell NFSHomeDirectory`：UniqueID 在 300–499，PrimaryGroupID 等于 `dscl . -read /Groups/_probe-agent PrimaryGroupID` 的值，UserShell 为 `/usr/bin/false`；`id -gn _probe-agent` 输出 `_probe-agent`；登录窗口里看不到这个用户。
3. `ps -axo user,pid,command | grep '[p]robe-agent run'`：USER 为 `_probe-agent`。`sudo launchctl print system/xyz.probe.agent | grep -E 'state =|pid ='`：`state = running`。
4. `sudo ls -ld /etc/probe-agent /etc/probe-agent/config.json /Library/LaunchDaemons/xyz.probe.agent.plist /Library/Logs/probe-agent /Library/Logs/probe-agent/*`：目录 `drwxr-x--- root _probe-agent`；配置 `-rw------- _probe-agent _probe-agent`；plist `-rw-r--r-- root wheel`；日志目录 `drwxr-xr-x root wheel`；两个日志文件属 `_probe-agent`（由 launchd 创建）。
5. 面板：节点在线；详情页系统为 `macOS <sw_vers -productVersion>`、架构为 `arm64`（Intel 为 `amd64`）、CPU 型号等于 `sysctl -n machdep.cpu.brand_string`、ICMP 可用；实时视图每项指标都有值；内存已用与活动监视器的"已使用内存"接近。
6. 给节点建一个 ICMP 任务，目标取网关（`route -n get default | awk '/gateway/ {print $2}'`）或 1.1.1.1：一分钟后有 RTT，与 `ping -c 10 <目标>` 的平均值同一量级。
7. hub 在局域网地址（如 192.168.x.x）上时节点同样上线，ICMP 到局域网主机有结果（macOS 15 起的本地网络隐私不应拦截这个 LaunchDaemon）。
8. 带同一个 `--key` 重跑第 1 条：输出含 `existing config found; keeping the current registration (--key ignored)`，面板节点数不变；换一个版本重跑：面板上的 agent 版本随之改变。
9. `sudo launchctl disable system/xyz.probe.agent` 后重跑第 1 条：成功，服务在跑。
10. `sudo kill -9 $(pgrep -u _probe-agent probe-agent)`：约 5 秒内出现新的 pid，节点保持或恢复在线。
11. `sudo rm /Library/Logs/probe-agent/probe-agent.log /Library/Logs/probe-agent/probe-agent.err && sudo launchctl kickstart -k system/xyz.probe.agent`：两个文件被重新创建且属 `_probe-agent`，进程在跑。
12. 记下 `sysctl -n kern.bootsessionuuid`，睡眠至少 2 分钟（`pmset sleepnow` 或合盖）后唤醒：值不变；节点在离线时限内回到在线；流量图在唤醒处没有尖峰。
13. 重启这台 Mac：服务开机自启、节点在线；`kern.bootsessionuuid` 换了新值；总流量没有一次性跳涨。
14. `curl -fsSL <base>/install-macos.sh | sudo sh -s -- --uninstall`：输出 `probe-agent uninstalled`；`sudo launchctl print system/xyz.probe.agent` 退出码 113；`/usr/local/bin/probe-agent` 与 plist 不在，配置与用户仍在。再带 `--uninstall --purge` 执行：`/etc/probe-agent`、`/Library/Logs/probe-agent` 不在，`id _probe-agent` 报 no such user，`dscl . -read /Groups/_probe-agent` 报 `eDSRecordNotFound`。
15. 有 Intel Mac 时在其上重复 1–5，装的是 amd64 包。
````

- [ ] **Step 2: 与代码逐项对照**

```sh
cd /Users/xjetry/work/vibe/probe-macos && go run ./cmd/hub serve -h > /tmp/m6mac-t8-hub-serve.log 2>&1; echo $?
cd /Users/xjetry/work/vibe/probe-macos && go run ./cmd/hub > /tmp/m6mac-t8-hub-usage.log 2>&1; echo $?
cd /Users/xjetry/work/vibe/probe-macos && grep -n -e 'usage: install-macos.sh' -e 'install-macos.sh --uninstall' deploy/install-macos.sh > /tmp/m6mac-t8-usage.log 2>&1; echo $?
```

Expected: 前两条 `1`（`serve -h` 走 `flag.ErrHelp`；无参数时打印用法并以 2 退出，`go run` 报 `exit status 2`）；第一份日志含 `-db`、`-listen`、`-trusted-proxies`，第二份含 `passwd`、`token list|revoke`、`node create|list|delete|rotate-token`、`window open|close|show`；第三条 `0`，两行用法里的参数与 README 一致（这里只读脚本，不执行它）。README 里的 `/usr/local/bin/probe-agent`、`/etc/probe-agent/`、`/Library/Logs/probe-agent/`、`xyz.probe.agent`、`_probe-agent`、`probe-agent_darwin_arm64.tar.gz` 逐个在 `deploy/install-macos.sh`、plist 与 `Makefile` 里找到同一字面量（用编辑器搜索，记进报告）。

- [ ] **Step 3: make ci 与提交**

```sh
cd /Users/xjetry/work/vibe/probe-macos && make ci > /tmp/m6mac-t8-ci.log 2>&1; echo $?
cd /Users/xjetry/work/vibe/probe-macos && git add README.md && git commit -m "docs: README 写明 hub 运行、Linux 与 macOS 安装及 macOS 真机核对"
```

真机核对（README 的清单）由用户在自己的 Mac 上 sudo 执行，不属于实现者的步骤；控制端在合并前把清单交给用户，结果记进进度文件。

---

## 对 spec 的回写（控制端合并时提交；实现者不改 docs/）

- §13 第 1 项：改为"已于 2026-09-26 实验确认，结论记在 `docs/superpowers/plans/2026-09-26-m5m6-macos.md` 的'实验结论'节"，并摘要：网卡计数器经 `NET_RT_IFLIST2` 与 `getifaddrs` 对普通进程只给 32 位、按 KiB 取整的值，`net.link.generic.ifdata` 给完整 64 位值；聚合的 `HOST_CPU_LOAD_INFO` 在 Apple Silicon 上按秒取差不稳，用逐 CPU 的 `host_processor_info`；Rosetta 下 `hw.pagesize` 为 4096 而页计数单位是 16384。
- §14 macOS agent 一条：把"候选"换成定案——sysctl（x/sys/unix）取启动标识、内存总量、负载、swap、连接数、网卡计数器与 facts；`clock_gettime(CLOCK_MONOTONIC)` 取运行时长；purego 调 libSystem 取逐 CPU tick、VM 统计、页大小与进程数；`launchctl enable` 在 `bootstrap` 之前；是否 `bootout` 看作业是否已载入。
- §7 末段：补 darwin 的默认排除列表 `lo* gif* stf* utun* ipsec* bridge* vmenet* awdl* llw* anpi* ap*`。
- §3.1 仓库布局：`collect/` 一行改为 `host.go（Host 接口）/ procfs.go / darwinraw.go / platform_{linux,darwin,other}.go`；`deploy/` 一行加 `install-macos.sh、launchd plist`。

## 自查记录

- spec 覆盖：§2 依赖隔离（Task 3 deps 测试）；§4.2 optional 语义与 `boot_id`（Task 1–3 的缺失用例、Task 6 的 19 个字段）；§4.5（运行时长不经墙钟）；§4.7（Task 6 hub 停机期间 pid 不变）；§7（Task 6 跨重启流量、默认排除列表）；§12（darwin 测试进 macOS CI、Linux 上 `GOOS=darwin go vet` 与 darwin 两架构 `go build`）；§13 第 1 项（实验结论）、第 2 项（Task 6 ICMP）；§14 macOS 一条的每个步骤（Task 4 脚本与测试、Task 5 产物、README 清单）、面板只给 Linux 命令（未改面板）、CI 含 macOS runner（Task 7）。
- 占位扫描：无 TBD、无"类似 Task N"；每个代码步骤给出整份文件或精确 diff，全部在 main（c381a27）的副本上编译、测试、注入过。
- 类型一致：`Host` 的 12 个方法、`darwinSource` 的 10 个方法、`usage`/`ifaceCounters`/`hostFacts`/`loadAvg`、`ProcFS{FS, DiskUsage}`、`DefaultNetExclude()` 在 Task 1–3 与 `cmd/agent/main.go`、`internal/agent/client/client_test.go` 中同名同签名。
- Review Focus：五条各有落在具体任务里的测试与注入；Rosetta 相关的注入只在 `GOARCH=amd64` 下红，已在 Task 3 Step 5 写明。

## 执行顺序与并行

- 本计划内：Task 1 → 2 → 3 依次（接口逐级依赖）；Task 4 只依赖 agent 的命令行，可与 1–3 并行（另开工作树时注意 `Makefile` 的 `lint` 与 Task 3 的 `build` 改在同一文件的不同段）；Task 5 依赖 3、4；Task 6 依赖 5；Task 7 依赖 3、4；Task 8 最后。单工作树顺序执行最简单，推荐如此。
- 与 hub Docker 镜像计划：两者都改 `Makefile`（本计划：`lint`、`build`、发布矩阵变量、`release`；Docker 计划：`docker` 目标与发布相关部分）与 `.github/workflows/ci.yml`（各加一个任务）；Docker 计划还改 `release.yml`（本计划不改）；若 Docker 计划也新建 `README.md`，会与本计划的 Task 8 冲突。**后合并者变基**：合回前固定三步——`git log --oneline $(git merge-base main <分支>)..main` 读标题找同类改动；`git merge-tree --write-tree --name-only main <分支>` 无副作用预演冲突；冲突按"两边都保留"解（Makefile 的两处新增并存、ci.yml 两个任务并存、README 把 Docker 的运行方式并进"运行 hub"一节），解完在变基后的分支上重跑 `make ci` 与 `make release VERSION=v0.0.0-rebase`，并核对 `dist/SHA256SUMS` 同时含 darwin 两行与 Docker 计划的产物（若有）。
- 两个计划的 `make release` 都往 `dist/` 写并先清理：在同一台机器上并行验收时各用自己的工作树，不共用 `dist/`。`scripts/macos-accept.sh` 的端口 18087/18088 与其他验收脚本不冲突，可与 e2e、install-accept 同时跑。

## 执行修正（执行与整分支审阅后记录；代码以分支为准）

**计划文字与实测不符（没改行为）**
- Task 2 注入 2（`internal` 读错偏移）红在 `checkUsage` 的 used exceeds total，不是计划期望的 `want total 2^37` 断言：vmBytes 的哨兵让任何错位都使已用量超过 hw.memsize。后由专属的偏移断言（`TestVMStatisticsReadsHeaderOffsets`，按十六进制页数报出错的字段）取代。
- Task 2 注入 4 按计划写法（`if err != nil {`）会让 `io/fs` import 未使用而编译失败，改为语义相同的写法。
- 规划决定里"CLOCK_MONOTONIC 不经墙钟"未经验证：darwin 的 CLOCK_MONOTONIC 与 gettimeofday − kern.boottime 相符、微秒粒度，在睡眠期间也计数（clock_gettime(3)）；`CLOCK_MONOTONIC_RAW` 同样含睡眠，`CLOCK_UPTIME_RAW` 才不计。仍用 CLOCK_MONOTONIC（spec §14），注释只写已验证事实。
- 规划决定的取号策略"300–499 最小空闲号"会撞上 Apple 逐版向上追加的系统账户（本机 26.3.1 已占到 308），改为从 499 向下（spec §14）。
- 本机 `/bin/sh` 收到 SIGINT 时会执行 EXIT trap（与审阅推测相反），`trap 'exit 1' INT TERM HUP` 仍加（dash 不会）。
- Task 4 D.4"launchd 以 root 身份创建日志文件"未经实证；用户域实验：作业以自己的身份打开日志。最终口径见下。

**源自计划原文、审阅改掉的缺陷**
- Linux"上报值不变"没有能红的测试：补逐字段黄金值测试（docker-debian 快照 + 各字段取不同非零值的合成快照）。进程数按 spec §7 改为数 `/proc` 进程目录（不再取 loadavg 第 4 字段），`hidepid=2` 下同一次目录列表里没有 `1` 这个进程目录即缺失。
- 合计型读数缺成员即整体缺失：sockstat 表不存在不计、存在读不出则 conns 整体缺失并记日志；网卡在列出与读取之间消失（ENOENT/ENODEV，以目录是否还在判定）只略过该网卡，其它读错整体缺失。网卡过滤只由 Collector 承载（Host 返回全部网卡）。
- darwin 夹具的区分度：swap 夹具 total/used/avail 互不相等；CPU 夹具四个状态增量两两不同；hw.logicalcpu 夹具值不等于本机核数；ifmib 长度守卫改为等于 180；网卡枚举上界与换基线各有用例。真机对照：内存按 vm_stat 四行逐项比对，夹逼测试先断言网卡集合相等（netstat 名字去掉结尾 `*`）。
- `hostVMInfo64Count` 的 38 是 HOST_VM_INFO64_REV1_COUNT（当前 SDK 的 HOST_VM_INFO64_COUNT = 62）；内核按 revision 回填并写回 count，count 偏小只让内存读数整体缺失。libSystem 逐函数绑定：缺一个符号只让对应读数缺失。
- 进程身份按 uid + 可执行路径（launchd 以该 uid 派生 cfprefsd、trustd 等辅助进程），启动与停止确认都用它。
- 日志目录口径经四轮收敛：目录 root:wheel 0755，`chown` 后 `chmod -N` 去 ACL（服务用户曾为属主时可加允许项，数字 chmod 与 chown 都不去掉），再检查两个日志文件为"不存在或链接数 1 的普通文件"（硬链接可穿过 `[ -f ]`，让 root 的 chown/chmod 改到目录外），每次安装预建文件交给服务用户 0640；预建、属主、权限等依赖外部条件的操作全部在停服务之前完成，停服务后只剩换二进制、写 plist、bootstrap（Linux install.sh 同一顺序）。文件被删后若 launchd 以服务用户打开会反复 EX_CONFIG，重跑安装恢复；launchd 以什么身份打开由 README 真机清单判别（文件不存在时按 UserName 属主创建，看属主分不出）。
- `--purge` 与建号的回查：删号前置判定读本地节点记录（`id` 会解析非本地来源的账户），Linux install.sh 同形改齐并加 `PROBE_INSTALL_ROOT` 测试缝（生产路径不变）。
- 验收脚本（macos-accept.sh、install-accept.sh）的端口可用环境变量覆盖，默认值不变；macos-accept.sh 每条判定都能红，"空输出 = 通过"的 grep 先冒烟。

**留给后续的同形清单**
- `scripts/macos-accept.sh` 的三处墙钟截止（节点条件等待、探测结果 90s、带 return 1 的等待函数）应改为按累计 sleep 计（e2e.sh 已改）；271–272 行测的是 launchd 的 ThrottleInterval 本身，不能照搬，改法待定。
- 公开页合入后 `/` 是公开页，macos-accept.sh 以"`/` 返回 302"判就绪要改为匿名 GetSite 200（与 e2e.sh、install-accept.sh 同写法）。
- 仓库里没有 LXC、OpenVZ 的 /proc 快照可核对 sockstat 的严格解析。
- 被排除的网卡长期读不出时整条流量读数缺失（接受的残余）。
