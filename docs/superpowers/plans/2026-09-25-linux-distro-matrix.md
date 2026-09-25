# Linux 发行版矩阵：端到端覆盖 Alpine 并守住静态链接 实现计划

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 端到端测试按 agent 容器镜像参数化，`make e2e` 每次在 amd64 与 arm64 上同时跑 Debian 12 与 Alpine 3.21，`make e2e-matrix` 再加 Ubuntu 24.04 与 Rocky Linux 9；构建产出 Linux 二进制时检查它们是静态链接，让"与 libc 无关"这条承载 Alpine 兼容的不变式有一道门禁。

**Architecture:** 不改 hub 与 agent 的运行时代码（实测 agent 已在四个发行版、两个架构上跑通完整端到端）。`scripts/e2e.sh` 从环境变量读 agent 镜像与期望的系统名，并断言节点上报的 `facts.os` 包含期望值，证明换镜像真的生效；Makefile 用一个循环按镜像清单逐个运行。静态链接检查是一个很小的 Go 程序，用标准库 `debug/elf` 与 `debug/buildinfo` 读二进制，`make binaries` 在产出 Linux 二进制后立即运行它。安装脚本与 OpenRC 服务不在本计划内，属于交付里程碑（spec §14 已写明做法）。

**Tech Stack:** POSIX sh、GNU make、Go 1.27 标准库（`debug/elf`、`debug/buildinfo`）、Docker 多架构。

**Spec:** `docs/superpowers/specs/2026-09-17-probe-architecture-design.md` §12（端到端按镜像参数化并断言系统名）、§13 第 2 项（多发行版 ICMP 实测）、§14（Linux 支持矩阵、静态链接、init 与安装脚本）。实验依据：Alpine、Ubuntu、Rocky 镜像替换后的完整端到端在两个架构上都通过；四个发行版真实机器上的 ping_group_range、OpenRC、安装工具、时区、CA 证书实测。

## Global Constraints

- 一级发行版（每次 `make e2e`）：`debian:bookworm-slim` 期望系统名含 `Debian`；`alpine:3.21` 期望含 `Alpine`。二级（`make e2e-matrix` 额外跑）：`ubuntu:24.04` 期望含 `Ubuntu`；`rockylinux:9` 期望含 `Rocky`。每个镜像都在 amd64 与 arm64 两个容器里跑（现有脚本已是两个架构）。
- 镜像与期望系统名成对出现，任何一个缺省都不能退化成"不检查"：`scripts/e2e.sh` 缺少期望系统名时直接失败并说明，不允许空串匹配一切。
- 静态链接的判定：ELF 没有 `PT_INTERP` 程序头、动态段里没有 `DT_NEEDED`，且构建信息里 `CGO_ENABLED=0`。三者任一不满足都失败，并在输出里说明是哪一条。
- 检查对象是 `make binaries` 产出的全部 Linux 二进制（目前是 `bin/probe-agent-linux-amd64`、`bin/probe-agent-linux-arm64`），由 Makefile 显式列出；hub 的 Linux 二进制在交付里程碑加入发布流水线时同样接入这道检查。
- 端到端端口仍是 18080 与 18081，同一时刻只跑一份；Makefile 按镜像顺序执行，前一个失败就停。
- 不改 `internal/`、`cmd/`、`proto/`、`gen/`、`web/`；不改 `docs/`（spec 与计划已由控制端提交）。
- 注释与提交信息只写 WHY 与不变式（前提指明由谁保证），禁止过程信息（任务或步骤编号、里程碑代号、审阅轮次、"按计划/简报"）。不打补丁、不复制第二份实现、不写 TODO。
- 每条新断言先红后绿：注入前 `git add` 建立基准，`git diff --quiet; echo $?` 为 1 确认落地，看红的原因行，再从索引还原。判成败的命令不接管道：`cmd > log 2>&1; echo $?`。

## 文件结构

```
scripts/e2e.sh                      从环境读 AGENT_IMAGE 与 EXPECT_OS，断言 facts.os
scripts/checkstatic/main.go         静态链接检查（package main，go run 调用）
scripts/checkstatic/main_test.go    检查器的正反用例
Makefile                            e2e 与 e2e-matrix 的镜像清单循环；binaries 之后运行静态检查
```

---

### Task 1: 端到端按镜像参数化，一级矩阵含 Alpine

**Files:**
- Modify: `scripts/e2e.sh`、`Makefile`

**Interfaces:**
- Produces：`scripts/e2e.sh` 读取环境变量 `AGENT_IMAGE`（镜像引用）与 `EXPECT_OS`（`facts.os` 应包含的子串）；两者都必填。`make e2e` 依次以 `debian:bookworm-slim/Debian`、`alpine:3.21/Alpine` 运行；`make e2e-matrix` 依次以四对运行。

- [ ] **Step 1: 脚本读取镜像与期望系统名**

在 `scripts/e2e.sh` 开头（`set -eu` 之后、`cd` 之前）加入，缺失即失败：

```sh
# 镜像与期望系统名成对给出：缺一个就无法证明容器真的换成了目标发行版，不能退化成不检查。
: "${AGENT_IMAGE:?AGENT_IMAGE is required, e.g. alpine:3.21}"
: "${EXPECT_OS:?EXPECT_OS is required, e.g. Alpine}"
echo "agent image: $AGENT_IMAGE (expect os containing \"$EXPECT_OS\")"
```

`register_agent` 里的 `debian:bookworm-slim` 改为 `"$AGENT_IMAGE"`。

- [ ] **Step 2: 断言两个节点上报的系统名**

在现有 `ListNodes` 形状断言之后（节点已上报 facts 的位置；以脚本里首次断言 `facts.icmpAvailable` 的 `jq` 为参照），加一条：

```sh
jq -e --arg os "$EXPECT_OS" '(.nodes | length) == 2 and all(.nodes[]; (.facts.os // "") | contains($os))' "$work/ListNodes.json" > /dev/null || { echo "FAIL: nodes did not report an OS containing \"$EXPECT_OS\""; cat "$work/ListNodes.json"; exit 1; }
```

- [ ] **Step 3: Makefile 的镜像清单循环**

```make
# 一级发行版每次都跑；二级发版前跑。镜像与期望系统名成对，前一个失败就停。
E2E_TIER1 := debian:bookworm-slim=Debian alpine:3.21=Alpine
E2E_TIER2 := ubuntu:24.04=Ubuntu rockylinux:9=Rocky

e2e: binaries
	@for pair in $(E2E_TIER1); do \
	  AGENT_IMAGE="$${pair%%=*}" EXPECT_OS="$${pair#*=}" scripts/e2e.sh || exit $$?; \
	done

e2e-matrix: binaries
	@for pair in $(E2E_TIER1) $(E2E_TIER2); do \
	  AGENT_IMAGE="$${pair%%=*}" EXPECT_OS="$${pair#*=}" scripts/e2e.sh || exit $$?; \
	done
```

若镜像引用本身含 `=`（目前没有），换分隔符；不要为此加兼容分支。`.PHONY` 若存在，补上 `e2e-matrix`。

- [ ] **Step 4: 验证缺参失败**

Run: `AGENT_IMAGE= EXPECT_OS= sh scripts/e2e.sh > /tmp/distro-t1-missing.log 2>&1; echo $?`
Expected: 非 0，日志含 `AGENT_IMAGE is required`（证明空值不会退化成不检查；此命令在启动 hub 之前就失败，不占用端口）。

- [ ] **Step 5: 语法检查**

Run: `sh -n scripts/e2e.sh > /tmp/distro-t1-syntax.log 2>&1; echo $?` → 0。`make -n e2e > /tmp/distro-t1-dryrun.log 2>&1; echo $?` → 0，且日志里能看到两个镜像各一次调用。

- [ ] **Step 6: 端到端实跑**

Run: `make e2e > /tmp/distro-t1-e2e.log 2>&1; echo $?` → 0；日志里出现两次 `E2E OK`，分别跟在 `agent image: debian:bookworm-slim` 与 `agent image: alpine:3.21` 之后。

- [ ] **Step 7: 注入验红**

- 以 `AGENT_IMAGE=alpine:3.21 EXPECT_OS=Debian sh scripts/e2e.sh > /tmp/distro-t1-mismatch.log 2>&1; echo $?` 运行（期望系统名与镜像不符）：非 0，红在 `FAIL: nodes did not report an OS containing "Debian"`。这条断言证明换镜像真的生效。

- [ ] **Step 8: 提交**

```bash
git add scripts/e2e.sh Makefile
git commit -m "e2e: 按 agent 镜像参数化并断言上报的系统名，一级矩阵每次覆盖 Debian 与 Alpine"
```

---

### Task 2: 构建产物的静态链接门禁

**Files:**
- Create: `scripts/checkstatic/main.go`、`scripts/checkstatic/main_test.go`
- Modify: `Makefile`

**Interfaces:**
- Produces：`go run ./scripts/checkstatic <file>...`：全部满足静态链接判定时退出 0；否则对每个不满足的文件输出一行 `<file>: <原因>` 并退出 1。原因取值固定为 `has PT_INTERP (dynamic loader <路径>)`、`has DT_NEEDED <库名>`、`built with CGO_ENABLED=<值>`、`missing CGO_ENABLED build setting`、`not an ELF file: <错误>`。

- [ ] **Step 1: 写检查器的失败测试**

`scripts/checkstatic/main_test.go`：
- 正例：在测试里用 `go build`（`exec.Command("go", "build", "-o", out, "./testdata/hello")`，环境设 `GOOS=linux GOARCH=amd64 CGO_ENABLED=0`）构建一个最小程序，`check(out)` 返回空原因列表。
- 反例一（PT_INTERP 与 DT_NEEDED）：构造一个合成的 ELF 文件——用 `encoding/binary` 写一个 ELF64 小端头、一个 `PT_INTERP` 程序头与其指向的 `/lib/ld-musl-x86_64.so.1\x00` 字符串；`check` 返回的原因含 `has PT_INTERP`。在宿主 macOS 上无法交叉产出真正动态链接的 Linux 二进制（没有交叉 C 工具链），所以动态特征用合成 ELF 表达，构建信息特征用下一条表达。
- 反例二（CGO 构建设置）：把判定构建信息的部分写成接受 `*debug/buildinfo.BuildInfo` 的纯函数，喂一个 `Settings` 含 `CGO_ENABLED=1` 的值，原因含 `built with CGO_ENABLED=1`；再喂一个不含该键的值，原因含 `missing CGO_ENABLED build setting`（缺失不等于 0，不能放行）。
- 反例三：非 ELF 文件（例如写入几个字节的文本）→ 原因含 `not an ELF file`。
- `testdata/hello/main.go` 是只有 `package main; func main() {}` 的最小程序。

Run: `go test -count=1 ./scripts/checkstatic > /tmp/distro-t2-red.log 2>&1; echo $?` → 1（未实现）。

- [ ] **Step 2: 实现检查器**

`scripts/checkstatic/main.go`：
- `check(path string) []string`：`elf.Open`；遍历 `Progs` 找 `PT_INTERP`，读出解释器路径写进原因；`ImportedLibraries()` 非空时每个库一条 `has DT_NEEDED <名>`；`buildinfo.ReadFile(path)` 取 `Settings` 交给 `cgoReason(bi)`。
- `main`：对每个参数调用 `check`，汇总输出，任一文件有原因即退出 1。
- 注释写 WHY：Alpine 用 musl，动态链接 glibc 的二进制在那里直接无法启动；原生 Linux 上构建时 `CGO_ENABLED` 默认为 1，net 包会链接系统解析器，所以只靠构建命令的约定不够，要在产物上检查。

Run: `go test -count=1 ./scripts/checkstatic > /tmp/distro-t2-green.log 2>&1; echo $?` → 0。

- [ ] **Step 3: 接入 Makefile**

`binaries` 目标在产出两个 Linux agent 二进制之后追加：

```make
	go run ./scripts/checkstatic bin/probe-agent-linux-amd64 bin/probe-agent-linux-arm64
```

Run: `make binaries > /tmp/distro-t2-binaries.log 2>&1; echo $?` → 0。

- [ ] **Step 4: 注入验红**

- 把 `cgoReason` 对缺失键的判定改成放行 → 反例二的"缺失"子用例红。
- 把 `PT_INTERP` 的判定去掉 → 反例一红。
- `make ci` 与 `go vet ./...` 为 0（`scripts/checkstatic` 在模块内，会被 `go vet ./...` 与 `go test ./...` 覆盖）。

- [ ] **Step 5: 提交**

```bash
git add scripts/checkstatic Makefile
git commit -m "build: 产出 Linux 二进制后检查静态链接，musl 发行版不能依赖系统动态链接器"
```

---

## 收尾

控制端在最终 HEAD 上跑 `make ci`、`make e2e`（Debian 与 Alpine，两个架构）与一次 `make e2e-matrix`（四个发行版），并确认 `go vet ./...` 覆盖到 `scripts/checkstatic`。OrbStack 上连续多架构端到端曾出现 Docker 守护进程整体卡住（实验记录），跑矩阵前先 `docker info` 确认守护进程可用，卡住时区分"守护进程死"与"客户端挂"后再重试，不把基础设施失败记成产品缺陷。

## 自检

- spec §14 的支持矩阵与静态链接要求、§12 的"断言系统名"各有任务对应；安装脚本、OpenRC 服务与各发行版真机安装测试明确留给交付里程碑。
- 镜像与期望系统名缺省即失败，没有"空值匹配一切"的放宽路径。
- 静态检查的三条判定各有反例；缺失的 CGO 设置不被当作 0。
