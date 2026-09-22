# M1 垂直切片 实现计划

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 一条能端到端跑通的最小链路：Linux agent 采集 `/proc` 指标并按 hub 下发的间隔上报；hub 鉴权、校验、折叠成分钟桶写入 SQLite；运维用 `probe-hub` 子命令管节点与注册窗口。

**Architecture:** hub 与 agent 同一 Go module，协议由 `proto/` 生成。hub 内 `Report` 路径只碰内存（`auth` 的 token 映射、`live` 的实时状态与分钟桶），落盘由分钟定时器把闭合的桶交给 `store` 的单一写协程做加法合并；指标列由 `metric` 包的描述表驱动，建表、折叠、合并 SQL 都从它生成。agent 的采集层是对 `fs.FS` 的纯解析（在 macOS 上用 fixture 测试），只有取 `os.DirFS("/")` 与 `statfs` 的几行带 `linux` build tag。

**Tech Stack:** Go 1.27（`CGO_ENABLED=0`）、buf 1.50 + `protoc-gen-go` v1.36.12 + `protoc-gen-connect-go` v1.21.0（经 go.mod `tool` 指令固定）、`connectrpc.com/connect` v1.21.0、`google.golang.org/protobuf` v1.36.12、`modernc.org/sqlite` v1.59.0、`golang.org/x/sys` v0.48.0（仅 linux 的 `Statfs`）。

**Spec:** `docs/superpowers/specs/2026-09-17-probe-architecture-design.md`。本计划覆盖 §15 的 M1 行；每个任务开头列出必读章节。`FEATURES.md` 第 1 条对 M1 有一条硬约束（`node.id` 用 `AUTOINCREMENT`），已纳入 Task 5。

## Global Constraints

以下每条对全部任务生效，执行者不必在任务内重复核对来源。

**来自 spec 的硬值**

- Go module 路径：`github.com/xjetry/probe`（仓库尚无远端，取 git 用户名；改动它要改全部 import，所以从第一个提交起就是这个）。
- 全部构建 `CGO_ENABLED=0`（§14）。hub 与 agent 都要在 macOS 开发机上 `go vet` 通过，且 `GOOS=linux go vet ./...` 与 `GOOS=linux GOARCH=amd64 go build ./...` 通过（§12：darwin 上的验证循环照不到 linux build tag 文件，必须显式跑）。
- 生成的 Go 代码入库；前端产物不入库（§14）。M1 没有前端。
- `proto/` 是单一事实源；`buf lint` 用 `STANDARD`，`buf breaking` 用 `WIRE_JSON`（§4.6）。
- 协议里的读数用 `optional`：缺失 = 无读数 ≠ 读数为 0（§2、§6.2）。
- TTL 来自环境变量 `PROBE_OFFLINE_AFTER`，默认 `30s`，下限 `10s`；下发的上报间隔 = `TTL / 3`；agent 退避上限 = TTL（agent 不知道 TTL，用 `3 × 最近一次下发的间隔`）（§4.4、§4.7）。
- 节点 token：32 字节随机数，hex 编码传输与保存于配置文件；只经 `Authorization: Bearer` 传递；hub 只存 SHA-256；内存映射 `hash → node_id`，`Report` 的鉴权只查它不读库；修改顺序：持锁 → 写库并等待成功 → 改映射 → 放锁（§5.1）。
- `AgentService` 请求体上限 64 KiB；每节点令牌桶限速，超过下发间隔对应速率的 2 倍返回 `ResourceExhausted`（§5.1）。
- 注册窗口：一次性 key（与 token 同形），带截止时间与可注册节点数上限；窗口关闭与 key 错误返回同一响应；失败计数按来源 IP，只有窗口开启且 key 错误才计数（§5.2）。
- `--listen` 默认 `127.0.0.1:8080`，非 loopback 时启动日志告警；`--trusted-proxies` 是显式 CIDR 列表，空 = 不信任任何转发头（§5.4）。
- 所有写由单一写协程串行执行，读走独立只读连接池；`Report` 路径不等待数据库（§6.1）。
- `metric_1m` 只存可加量（sum / n / max），`ON CONFLICT` 加法合并；每个指标各自的样本数；`x_n = 0` 在查询结果里是"无数据"（§6.2）。写协程拒绝 `ts` 早于 5m 水位的 1m 写入（§6.4 第 1 条）——M1 没有上卷，水位恒为 0，但检查与它的测试从 M1 起就在。
- `node.id` 为 `INTEGER PRIMARY KEY AUTOINCREMENT`（`FEATURES.md` 第 1 条：建表时定，事后补不上）。
- 上报含非法值（非有限数、负数、百分比越界、`load` 形状不对）整条拒绝 `InvalidArgument`，`live` 不变；`Facts` 字符串截断并剔除控制字符（§11）。
- hub 内凡是时长一律用单调钟，经 `internal/clock` 注入（§4.5、§12）。
- 时间与随机都可注入，测试确定性推进。

**来自 spec 的 M1 范围裁剪（写进代码注释里要用"为什么"，不要写"M1"、"以后"）**

- `metric_1m` 在 M1 不含 `rx_bytes` / `tx_bytes`：它们的来源是 §7 的流量差分，与流量累计同批加入（描述表加一项 + 一次迁移，正是 §6.2 设计的扩展方式）。
- `ReportRequest.probe_results` 与 `ReportResponse.tasks` 在协议里齐全，hub 在 M1 忽略前者、不填后者。
- `ReportRequest.facts` 的 `icmp_available` 由 agent 固定回报 `false`（探测未实现）。

**代码与提交规范（来自用户全局规则，对子代理同样生效）**

- 注释、KDoc、commit message 里**禁止**出现过程信息：任务 / 步骤编号（"Task 3"、"Step 2"）、里程碑代号（"M1"）、方案代号、审阅轮次、"按计划"之类的引用。写 **WHY** 与 **不变式**：先陈述这段代码依赖或维持的不变式，再推出为什么必须这样写，前提要指明由谁保证（哪个函数、哪把锁、哪张表）。
- 禁止补丁式修改：不为过某个测试加特判分支，不复制第二份实现，不写 `TODO: 以后重构`，不加"临时"开关。根因在哪一层就在哪一层修。
- 每条新断言做一次缺陷注入：构造它本该抓住的缺陷，确认测试红、且红在正确的原因上，再改回来。任务内已写明注入点。
- 判成败的命令不接管道：`go test -count=1 ./... > /tmp/<task>.log 2>&1; echo $?`，再看 log。
- 提交信息用中文，`<type>: <一句话>` 开头，正文写 WHY；以仓库既有的两个提交为样本。
- 每个任务结束时：`go vet ./...`、`GOOS=linux go vet ./...`、`go test -count=1 ./...` 全绿，再提交。

---

## 文件结构

```
go.mod / go.sum                       module github.com/xjetry/probe；tool 指令固定两个 protoc 插件
buf.yaml / buf.gen.yaml               lint STANDARD、breaking WIRE_JSON；插件走 `go tool`
Makefile                              gen / lint / test / build / e2e
.github/workflows/ci.yml              buf lint、buf breaking（PR 时）、vet、test、交叉编译
proto/probe/v1/types.proto            Metrics、Facts、ProbeResult、ProbeTask(s)
proto/probe/v1/agent.proto            AgentService：Register、Report
gen/probe/v1/*.pb.go                  生成物，入库
gen/probe/v1/probev1connect/*.go      生成物，入库
internal/clock/clock.go               Clock 接口：Now（墙钟）/ Mono（单调钟）；Real 与 Fake
internal/hub/metric/metric.go         指标描述表 Columns；Bucket 折叠；Row（分钟行）
internal/hub/live/live.go             每节点实时状态与分钟桶；Online；Flush / Drain
internal/hub/store/store.go           Open / Close；写协程；只读池；迁移
internal/hub/store/schema.go          DDL 常量 + 从描述表生成 metric_1m 的建表与 upsert SQL
internal/hub/store/node.go            node 表：Create / List / Delete / SetTokenHash / TokenHashes
internal/hub/store/window.go          register_window 表 + RegisterNode（同事务消耗名额）
internal/hub/store/facts.go           node_facts 表
internal/hub/store/metric.go          WriteMinuteRows（含水位检查）/ ReadMinuteRows / SetRollupWatermark
internal/hub/auth/token.go            NewToken / HashToken
internal/hub/auth/auth.go             映射与锁序；CreateNode / RotateToken / DeleteNode / Register / 窗口
internal/hub/auth/proxy.go            ClientIP：可信代理与 X-Forwarded-For
internal/hub/ingest/service.go        AgentService 实现 + 鉴权拦截器
internal/hub/ingest/validate.go       Metrics 校验、Facts 清洗
internal/hub/ingest/limiter.go        每节点令牌桶
internal/hub/ingest/flush.go          分钟刷出循环 + 有界待重试列表
cmd/hub/main.go                       子命令分发
cmd/hub/serve.go                      serve：装配、监听、优雅退出
cmd/hub/node.go                       node create / list / delete / rotate-token
cmd/hub/window.go                     window open / close / show
internal/agent/collect/parse.go       /proc 与 /sys 纯解析（无 build tag）
internal/agent/collect/collect.go     Collector：Metrics() / Facts()（无 build tag）
internal/agent/collect/platform_linux.go   os.DirFS("/") + unix.Statfs
internal/agent/collect/platform_other.go   !linux：返回"平台未支持"
internal/agent/collect/testdata/docker-debian/…   从容器抓的真实 /proc 快照
internal/agent/client/config.go       配置文件（0600）
internal/agent/client/hash.go         FactsHash
internal/agent/client/backoff.go      带抖动的指数退避
internal/agent/client/runner.go       上报循环
cmd/agent/main.go                     register / run
scripts/capture-proc.sh               从 Docker 容器抓 /proc fixture
scripts/e2e.sh                        端到端：hub 在宿主机、agent 在 Linux 容器
```

依赖方向（对 §3.1 的补充，已回写 spec）：`metric` 与 `clock` 是叶子；`live → metric`；`store → metric`；`auth → store`；`ingest → live, store, auth, metric`；`cmd/hub → 全部`。

---

### Task 0: 仓库脚手架与生成流水线

**必读**：spec §2、§3.1、§12 末条（CI）、§14。

**Files:**
- Create: `go.mod`、`buf.yaml`、`buf.gen.yaml`、`Makefile`、`.github/workflows/ci.yml`
- Modify: `.gitignore`（追加 `/gen/**/*.tmp` 不需要；确认已有 `/bin/`、`*.db`）

**Interfaces:**
- Produces: `make gen` / `make lint` / `make test` / `make build` 四个目标；CI 在 push 与 PR 上跑。

- [ ] **Step 1: 初始化 module 并固定插件版本**

```bash
cd /Users/xjetry/work/vibe/probe
go mod init github.com/xjetry/probe
go get -tool google.golang.org/protobuf/cmd/protoc-gen-go@v1.36.12
go get -tool connectrpc.com/connect/cmd/protoc-gen-connect-go@v1.21.0
go get connectrpc.com/connect@v1.21.0 google.golang.org/protobuf@v1.36.12 modernc.org/sqlite@v1.59.0 golang.org/x/sys@v0.48.0
```

预期 `go.mod` 含 `go 1.27.1`、`tool (...)` 块与四个 require。

- [ ] **Step 2: buf 配置**

`buf.yaml`：

```yaml
version: v2
modules:
  - path: proto
lint:
  use:
    - STANDARD
breaking:
  use:
    - WIRE_JSON
```

`buf.gen.yaml`：

```yaml
version: v2
managed:
  enabled: false
plugins:
  - local: ["go", "tool", "protoc-gen-go"]
    out: gen
    opt: paths=source_relative
  - local: ["go", "tool", "protoc-gen-connect-go"]
    out: gen
    opt: paths=source_relative
```

- [ ] **Step 3: Makefile**

```make
export CGO_ENABLED=0

.PHONY: gen lint test build binaries ci e2e fixtures

gen:
	buf generate

lint:
	buf lint
	go vet ./...
	GOOS=linux go vet ./...

test:
	go test -count=1 ./...

# build 只验证全部已有的包在三个目标平台都能编译，所以从第一个 Go 包起每个提交上都有意义；
# 二进制产物由 binaries 生成，只有 e2e 需要它。
build:
	go build ./...
	GOOS=linux GOARCH=amd64 go build ./...
	GOOS=linux GOARCH=arm64 go build ./...

binaries:
	go build -o bin/probe-hub ./cmd/hub
	GOOS=linux GOARCH=amd64 go build -o bin/probe-agent-linux-amd64 ./cmd/agent
	GOOS=linux GOARCH=arm64 go build -o bin/probe-agent-linux-arm64 ./cmd/agent

ci: gen lint test build
	git diff --exit-code -- gen

fixtures:
	scripts/capture-proc.sh docker-debian

e2e: binaries
	scripts/e2e.sh
```

`ci` 目标末尾的 `git diff --exit-code -- gen` 钉住"生成物与 proto 一致"：有人改了 proto 没重新生成，CI 就红。

- [ ] **Step 4: CI**

`.github/workflows/ci.yml`：

```yaml
name: ci
on:
  push:
    branches: [main]
  pull_request:
jobs:
  ci:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
        with:
          fetch-depth: 0
      - uses: actions/setup-go@v5
        with:
          go-version-file: go.mod
      - uses: bufbuild/buf-action@v1
        with:
          setup_only: true
      - run: make ci
      - name: buf breaking
        if: github.event_name == 'pull_request'
        run: buf breaking --against '.git#branch=main'
```

- [ ] **Step 5: 验证空 module 的状态**

```bash
go build ./... ; echo "build=$?"
go vet ./... ; echo "vet=$?"
```

预期 `build=0`；`vet=1` 且 stderr 是 `matched no packages` / `no packages to vet`——这是"没有包可查"，不是某个包没过；同理此时 `go test ./...` 也是 1。Global Constraints 的三条门禁在这个提交上没有对象，从 Task 1 加入第一个 Go 包起才生效。`buf lint` 此时报 proto 模块没有 .proto 文件，属预期，Task 1 补。

- [ ] **Step 6: 提交**

```bash
git add go.mod go.sum buf.yaml buf.gen.yaml Makefile .github
git commit -m "build: 建立 Go module 与 buf 生成流水线

protoc 插件经 go.mod 的 tool 指令固定版本，buf 用 go tool 调用它们：
生成物是否一致只取决于 go.mod，与开发机 PATH 上装了什么无关。
ci 目标末尾比对 gen/ 无 diff，改了 proto 不重新生成就会在 CI 上红。"
```

---

### Task 1: 协议定义与生成代码

**必读**：spec §3.2、§3.3（只做 `AgentService`）、§4.2 全部、§4.6。

**Files:**
- Create: `proto/probe/v1/types.proto`、`proto/probe/v1/agent.proto`
- Create（生成）: `gen/probe/v1/types.pb.go`、`gen/probe/v1/agent.pb.go`、`gen/probe/v1/probev1connect/agent.connect.go`

**Interfaces:**
- Produces: Go 包 `github.com/xjetry/probe/gen/probe/v1`（别名 `probev1`）：`Metrics`、`Facts`、`ProbeResult`、`ProbeTask`、`ProbeTasks`、`ProbeKind`、`RegisterRequest/Response`、`ReportRequest/Response`；包 `.../probev1connect`：`AgentServiceHandler` 接口、`NewAgentServiceHandler`、`NewAgentServiceClient`、常量 `AgentServiceRegisterProcedure = "/probe.v1.AgentService/Register"`、`AgentServiceReportProcedure`。

- [ ] **Step 1: types.proto**

```proto
syntax = "proto3";

package probe.v1;

option go_package = "github.com/xjetry/probe/gen/probe/v1;probev1";

// 一次上报里的主机读数。每个读数都是 optional：缺失表示"无读数"，
// 与读数为 0 是两个不同的事实，从协议一直保持到图表。
message Metrics {
  // 启动周期标识，随计数器同一条消息到达：流量差分必须在同一条消息里
  // 同时拿到计数器与它所属的启动周期，否则重启后的首次上报会被误当增量。
  string boot_id = 1;
  optional double cpu_pct = 2;
  optional double load1 = 3;
  optional double load5 = 4;
  optional double load15 = 5;
  optional uint64 mem_total = 6;
  optional uint64 mem_used = 7;
  optional uint64 swap_total = 8;
  optional uint64 swap_used = 9;
  optional uint64 disk_total = 10;
  optional uint64 disk_used = 11;
  // 内核累计计数器，hub 侧做差分。
  optional uint64 net_rx_total = 12;
  optional uint64 net_tx_total = 13;
  // agent 自测的瞬时速率，仅供实时视图。
  optional uint64 net_rx_bps = 14;
  optional uint64 net_tx_bps = 15;
  optional uint32 tcp_conns = 16;
  optional uint32 udp_conns = 17;
  optional uint32 procs = 18;
  optional uint64 uptime_s = 19;
}

// 主机静态信息。进程启动后的首次上报携带；此后仅在 hub 要求时携带。
message Facts {
  string hostname = 1;
  string os = 2;
  string kernel = 3;
  string arch = 4;
  string virtualization = 5;
  string cpu_model = 6;
  uint32 cpu_cores = 7;
  string agent_version = 8;
  // 两种 ICMP socket 是否至少一种可用。
  bool icmp_available = 9;
}

message ProbeResult {
  uint64 task_id = 1;
  // 测量完成至发送的时长，agent 单调钟；hub 用它反推测量时刻。
  uint32 age_ms = 2;
  oneof outcome {
    uint32 rtt_us = 3;
    // 计入丢包。
    Timeout timeout = 4;
    // 无权限、解析失败等；不计入丢包。
    ProbeError error = 5;
  }
}

message Timeout {}

message ProbeError {
  string message = 1;
}

enum ProbeKind {
  PROBE_KIND_UNSPECIFIED = 0;
  PROBE_KIND_ICMP = 1;
  PROBE_KIND_TCP = 2;
}

message ProbeTask {
  uint64 id = 1;
  ProbeKind kind = 2;
  string target = 3;
  uint32 interval_s = 4;
  uint32 timeout_ms = 5;
}

message ProbeTasks {
  uint64 version = 1;
  repeated ProbeTask tasks = 2;
}
```

- [ ] **Step 2: agent.proto**

```proto
syntax = "proto3";

package probe.v1;

option go_package = "github.com/xjetry/probe/gen/probe/v1;probev1";

import "probe/v1/types.proto";

// agent → hub 的唯一服务。两个方法都是 unary：hub 向 agent 的下行只有低频
// 配置，在每次上报的响应里按版本对账即可，不需要应用层连接状态。
service AgentService {
  // 用注册窗口的一次性 key 换取节点 token。
  rpc Register(RegisterRequest) returns (RegisterResponse);
  // 周期上报。鉴权用节点 token（Authorization: Bearer）。
  rpc Report(ReportRequest) returns (ReportResponse);
}

message RegisterRequest {
  string key = 1;
  string name = 2;
}

message RegisterResponse {
  int64 node_id = 1;
  // 明文 token 只在此处返回一次。
  string token = 2;
}

message ReportRequest {
  Metrics metrics = 1;
  repeated ProbeResult probe_results = 2;
  // agent 当前持有的探测任务版本。
  uint64 tasks_version = 3;
  // agent 静态信息的摘要，每次都带。
  fixed64 facts_hash = 4;
  // 进程启动后的首次上报携带；此后仅在 hub 要求时携带。
  Facts facts = 5;
}

message ReportResponse {
  uint32 report_interval_ms = 1;
  // 仅当 tasks_version 与 hub 不一致时携带。
  ProbeTasks tasks = 2;
  // hub 持有的 facts_hash 与请求不一致。
  bool want_facts = 3;
}
```

- [ ] **Step 3: lint 与生成**

```bash
buf lint; echo "lint=$?"
buf generate; echo "gen=$?"
find gen -type f
go build ./... ; echo "build=$?"
```

预期：lint 0；gen 0；三个文件 `gen/probe/v1/types.pb.go`、`gen/probe/v1/agent.pb.go`、`gen/probe/v1/probev1connect/agent.connect.go`；build 0。

- [ ] **Step 4: 钉住生成物一致性**

```bash
buf generate && git status --short gen
```

预期：第二次生成后 `gen/` 无变化（幂等）。

- [ ] **Step 5: 提交**

```bash
git add proto gen
git commit -m "proto: 定义 AgentService 与共享消息

读数一律 optional：缺失是无读数，不是 0。boot_id 放在 Metrics 而非
Facts，流量差分要在同一条消息里同时看到计数器与启动周期。"
```

---

### Task 2: 可注入时钟

**必读**：spec §4.5、§12（时间注入那条）。

**Files:**
- Create: `internal/clock/clock.go`、`internal/clock/clock_test.go`

**Interfaces:**
- Produces:
  ```go
  type Clock interface { Now() time.Time; Mono() time.Duration }
  func Real() Clock
  type Fake struct{ … }
  func NewFake(wall time.Time) *Fake
  func (f *Fake) Now() time.Time
  func (f *Fake) Mono() time.Duration
  func (f *Fake) Advance(d time.Duration)   // 墙钟与单调钟同推
  func (f *Fake) SetWall(t time.Time)       // 只拨墙钟
  ```

- [ ] **Step 1: 失败的测试**

```go
package clock

import (
	"testing"
	"time"
)

func TestFakeAdvanceMovesBothClocks(t *testing.T) {
	f := NewFake(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	m0, w0 := f.Mono(), f.Now()
	f.Advance(3 * time.Second)
	if f.Mono()-m0 != 3*time.Second {
		t.Fatalf("mono advanced %v, want 3s", f.Mono()-m0)
	}
	if f.Now().Sub(w0) != 3*time.Second {
		t.Fatalf("wall advanced %v, want 3s", f.Now().Sub(w0))
	}
}

func TestFakeSetWallLeavesMonoAlone(t *testing.T) {
	f := NewFake(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	m0 := f.Mono()
	f.SetWall(f.Now().Add(-time.Hour))
	if f.Mono() != m0 {
		t.Fatalf("mono moved to %v after wall was set back", f.Mono())
	}
}

func TestRealMonoNeverDecreases(t *testing.T) {
	c := Real()
	a := c.Mono()
	b := c.Mono()
	if b < a {
		t.Fatalf("mono went backwards: %v then %v", a, b)
	}
}
```

- [ ] **Step 2: 跑，确认红**

`go test -count=1 ./internal/clock/ > /tmp/t2.log 2>&1; echo $?` → 非 0，log 里是 undefined。

- [ ] **Step 3: 实现**

```go
// Package clock 把墙钟与单调钟分开注入。
//
// hub 内凡是"时长"（在线判定、限速、退避）一律用 Mono：墙钟被 NTP 向后拨
// 时差值为负，拿它做除数或比较都会得到荒谬的结果。墙钟只用于给样本打点
// 与展示。
package clock

import (
	"sync"
	"time"
)

type Clock interface {
	// Now 是墙钟。
	Now() time.Time
	// Mono 是单调钟，自任意固定起点起算，只能用于相减。
	Mono() time.Duration
}

type real struct{ start time.Time }

// Real 返回进程时钟。Mono 基于 time.Since，它读取 time.Time 内嵌的
// 单调读数，不受墙钟调整影响。
func Real() Clock { return &real{start: time.Now()} }

func (r *real) Now() time.Time        { return time.Now() }
func (r *real) Mono() time.Duration   { return time.Since(r.start) }

// Fake 供测试确定性推进，两只钟可分别拨动。
type Fake struct {
	mu   sync.Mutex
	wall time.Time
	mono time.Duration
}

func NewFake(wall time.Time) *Fake { return &Fake{wall: wall, mono: time.Hour} }

func (f *Fake) Now() time.Time {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.wall
}

func (f *Fake) Mono() time.Duration {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.mono
}

// Advance 同时推进两只钟，模拟正常流逝。
func (f *Fake) Advance(d time.Duration) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.wall = f.wall.Add(d)
	f.mono += d
}

// SetWall 只拨墙钟，模拟 NTP 调整；单调钟不动。
func (f *Fake) SetWall(t time.Time) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.wall = t
}
```

- [ ] **Step 4: 跑，确认绿**

`go test -count=1 ./internal/clock/ > /tmp/t2.log 2>&1; echo $?` → 0。

- [ ] **Step 5: 缺陷注入**

把 `SetWall` 改成同时 `f.mono = 0`，跑测试，预期 `TestFakeSetWallLeavesMonoAlone` 红且报错含 "mono moved"。改回。

- [ ] **Step 6: 提交**

```bash
git add internal/clock
git commit -m "hub: 可注入的墙钟与单调钟

时长一律走单调钟，墙钟只打点与展示；Fake 能单独回拨墙钟，
供在线判定与桶折叠的测试复现 NTP 调整。"
```

---

### Task 3: 指标描述表与分钟桶

**必读**：spec §6.2 全部（尤其"只存可加量"与"描述表驱动"两段）。

**Files:**
- Create: `internal/hub/metric/metric.go`、`internal/hub/metric/metric_test.go`

**Interfaces:**
- Consumes: `probev1.Metrics`。
- Produces:
  ```go
  type Kind uint8;  const ( Mean Kind = iota; MeanMax )
  type Type uint8;  const ( Float Type = iota; Int )
  type Column struct { Name string; Kind Kind; Type Type; Get func(*probev1.Metrics) (float64, bool) }
  var Columns []Column                       // 顺序即 Bucket 各切片的下标
  func (c Column) SQLType() string           // "REAL" / "INTEGER"
  type Bucket struct { Sum []float64; N []uint32; Max []float64 }   // len == len(Columns)
  func NewBucket() *Bucket
  func (b *Bucket) Add(m *probev1.Metrics)
  func (b *Bucket) Merge(o *Bucket)
  func (b *Bucket) Mean(i int) (float64, bool)
  type Row struct { NodeID int64; TS int64; Bucket *Bucket; LastSeen time.Time }
  ```

- [ ] **Step 1: 失败的测试**

```go
package metric

import (
	"testing"

	probev1 "github.com/xjetry/probe/gen/probe/v1"
	"google.golang.org/protobuf/proto"
)

func idx(t *testing.T, name string) int {
	t.Helper()
	for i, c := range Columns {
		if c.Name == name {
			return i
		}
	}
	t.Fatalf("no column %q", name)
	return -1
}

func TestAddCountsOnlyPresentReadings(t *testing.T) {
	b := NewBucket()
	b.Add(&probev1.Metrics{CpuPct: proto.Float64(10), MemUsed: proto.Uint64(100)})
	b.Add(&probev1.Metrics{CpuPct: proto.Float64(30)}) // 无 mem_used 读数
	cpu, mem := idx(t, "cpu"), idx(t, "mem_used")
	if got, ok := b.Mean(cpu); !ok || got != 20 {
		t.Fatalf("cpu mean = %v,%v want 20,true", got, ok)
	}
	if b.N[mem] != 1 {
		t.Fatalf("mem_used n = %d, want 1: a missing reading must not enter the count", b.N[mem])
	}
	if got, ok := b.Mean(mem); !ok || got != 100 {
		t.Fatalf("mem_used mean = %v,%v want 100,true", got, ok)
	}
	if b.Max[cpu] != 30 {
		t.Fatalf("cpu max = %v, want 30", b.Max[cpu])
	}
}

func TestMeanOfEmptyColumnIsNoData(t *testing.T) {
	b := NewBucket()
	b.Add(&probev1.Metrics{CpuPct: proto.Float64(1)})
	if _, ok := b.Mean(idx(t, "swap_used")); ok {
		t.Fatal("swap_used had no readings; mean must report no-data, not 0")
	}
}

func TestMergeIsAdditive(t *testing.T) {
	a, b := NewBucket(), NewBucket()
	a.Add(&probev1.Metrics{CpuPct: proto.Float64(10)})
	b.Add(&probev1.Metrics{CpuPct: proto.Float64(20)})
	b.Add(&probev1.Metrics{CpuPct: proto.Float64(60)})
	a.Merge(b)
	cpu := idx(t, "cpu")
	if a.Sum[cpu] != 90 || a.N[cpu] != 3 || a.Max[cpu] != 60 {
		t.Fatalf("merged sum/n/max = %v/%d/%v, want 90/3/60", a.Sum[cpu], a.N[cpu], a.Max[cpu])
	}
}

func TestColumnsCoverSpecifiedMetrics(t *testing.T) {
	want := map[string]struct {
		kind Kind
		typ  Type
	}{
		"cpu": {MeanMax, Float}, "mem_used": {MeanMax, Int}, "swap_used": {Mean, Int},
		"disk_used": {Mean, Int}, "load1": {Mean, Float}, "tcp": {Mean, Int},
		"udp": {Mean, Int}, "procs": {Mean, Int},
	}
	if len(Columns) != len(want) {
		t.Fatalf("%d columns, want %d", len(Columns), len(want))
	}
	for _, c := range Columns {
		w, ok := want[c.Name]
		if !ok {
			t.Fatalf("unexpected column %q", c.Name)
		}
		if c.Kind != w.kind || c.Type != w.typ {
			t.Fatalf("column %q kind/type = %v/%v, want %v/%v", c.Name, c.Kind, c.Type, w.kind, w.typ)
		}
	}
}
```

- [ ] **Step 2: 跑，确认红**

`go test -count=1 ./internal/hub/metric/ > /tmp/t3.log 2>&1; echo $?` → 非 0。

- [ ] **Step 3: 实现**

```go
// Package metric 是指标列的唯一描述处。
//
// 不变式：建表语句、内存桶的折叠、写库时的加法合并、查询与上卷的 SQL 都
// 从 Columns 生成；不存在需要手工保持一致的第二份字段清单。新增一个指标
// 是描述表加一项加一次迁移，而不是在四个地方各改一行。
package metric

import (
	"time"

	probev1 "github.com/xjetry/probe/gen/probe/v1"
)

type Kind uint8

const (
	// Mean 存 sum 与 n，均值在查询时由 sum / n 得到。
	Mean Kind = iota
	// MeanMax 另存 max：短时尖峰一路保留到最粗一级，不被均值抹平。
	MeanMax
)

type Type uint8

const (
	Float Type = iota
	Int
)

type Column struct {
	// Name 是 SQL 列名的词干：cpu → cpu_sum、cpu_n、cpu_max。
	Name string
	Kind Kind
	Type Type
	// Get 从一次上报里取读数；false 表示无读数，此时既不进 sum 也不进 n。
	Get func(*probev1.Metrics) (float64, bool)
}

func (c Column) SQLType() string {
	if c.Type == Int {
		return "INTEGER"
	}
	return "REAL"
}

func f64(v uint64) float64 { return float64(v) }

// Columns 的顺序就是 Bucket 各切片的下标，也是 SQL 里列的顺序。
// 只能在末尾追加：中间插入会让已存在的桶与行错位。
var Columns = []Column{
	{"cpu", MeanMax, Float, func(m *probev1.Metrics) (float64, bool) { return m.GetCpuPct(), m.CpuPct != nil }},
	{"mem_used", MeanMax, Int, func(m *probev1.Metrics) (float64, bool) { return f64(m.GetMemUsed()), m.MemUsed != nil }},
	{"swap_used", Mean, Int, func(m *probev1.Metrics) (float64, bool) { return f64(m.GetSwapUsed()), m.SwapUsed != nil }},
	{"disk_used", Mean, Int, func(m *probev1.Metrics) (float64, bool) { return f64(m.GetDiskUsed()), m.DiskUsed != nil }},
	{"load1", Mean, Float, func(m *probev1.Metrics) (float64, bool) { return m.GetLoad1(), m.Load1 != nil }},
	{"tcp", Mean, Int, func(m *probev1.Metrics) (float64, bool) { return f64(uint64(m.GetTcpConns())), m.TcpConns != nil }},
	{"udp", Mean, Int, func(m *probev1.Metrics) (float64, bool) { return f64(uint64(m.GetUdpConns())), m.UdpConns != nil }},
	{"procs", Mean, Int, func(m *probev1.Metrics) (float64, bool) { return f64(uint64(m.GetProcs())), m.Procs != nil }},
}

// Bucket 是一分钟内样本的可加折叠。
//
// 全部以 float64 累加，写库时按列类型转回整数。sum 在 2^53 以内是精确的：
// 一分钟最多几十个样本、单个读数不超过 TB 量级，远在这个界内。
type Bucket struct {
	Sum []float64
	N   []uint32
	Max []float64
}

func NewBucket() *Bucket {
	n := len(Columns)
	return &Bucket{Sum: make([]float64, n), N: make([]uint32, n), Max: make([]float64, n)}
}

func (b *Bucket) Add(m *probev1.Metrics) {
	for i, c := range Columns {
		v, ok := c.Get(m)
		if !ok {
			continue
		}
		b.Sum[i] += v
		if b.N[i] == 0 || v > b.Max[i] {
			b.Max[i] = v
		}
		b.N[i]++
	}
}

// Merge 把 o 加进 b。与写库时的 ON CONFLICT 合并是同一种运算。
func (b *Bucket) Merge(o *Bucket) {
	for i := range Columns {
		if o.N[i] == 0 {
			continue
		}
		if b.N[i] == 0 || o.Max[i] > b.Max[i] {
			b.Max[i] = o.Max[i]
		}
		b.Sum[i] += o.Sum[i]
		b.N[i] += o.N[i]
	}
}

// Mean 的第二个返回值为 false 表示该列在桶内没有任何读数。
func (b *Bucket) Mean(i int) (float64, bool) {
	if b.N[i] == 0 {
		return 0, false
	}
	return b.Sum[i] / float64(b.N[i]), true
}

// Row 是一条分钟行：live 刷出的单位，也是 store 写入与读回的单位。
type Row struct {
	NodeID int64
	// TS 是桶起始，Unix 秒，60 对齐。
	TS       int64
	Bucket   *Bucket
	// LastSeen 是该节点最近一次上报的墙钟，只供展示与告警文案。
	LastSeen time.Time
}
```

- [ ] **Step 4: 跑，确认绿**

`go test -count=1 ./internal/hub/metric/ > /tmp/t3.log 2>&1; echo $?` → 0。

- [ ] **Step 5: 缺陷注入**

把 `Add` 里的 `if !ok { continue }` 删掉（缺失读数也计数），跑测试：预期 `TestAddCountsOnlyPresentReadings` 红，报错含 "mem_used n = 2"。改回。

- [ ] **Step 6: 提交**

```bash
git add internal/hub/metric
git commit -m "hub: 指标描述表与分钟桶

指标列只在这一处描述，建表、折叠、合并 SQL 都从它生成。每列各自
计数：缺失的 optional 读数既不进 sum 也不进 n，无读数与 0 从协议
一路区分到查询。"
```

---

### Task 4: 实时状态与在线判定

**必读**：spec §4.4、§6.1（`Report` 只碰内存那段）、§6.2 第三条（取走并清零）、§11（墙钟回拨行）。

**Files:**
- Create: `internal/hub/live/live.go`、`internal/hub/live/live_test.go`

**Interfaces:**
- Consumes: `clock.Clock`、`metric.Bucket`、`metric.Row`。
- Produces:
  ```go
  func New(clk clock.Clock, ttl time.Duration) *Live
  func (l *Live) Observe(nodeID int64, m *probev1.Metrics)
  func (l *Live) Online(nodeID int64) bool
  func (l *Live) Get(nodeID int64) (Entry, bool)      // Entry{Metrics, LastSeen time.Duration, Online bool}
  func (l *Live) Flush() []metric.Row                 // 已闭合的桶（起始 < 当前分钟）
  func (l *Live) Drain() []metric.Row                 // 全部桶，退出时用
  func (l *Live) Forget(nodeID int64)
  ```

- [ ] **Step 1: 失败的测试**

```go
package live

import (
	"testing"
	"time"

	probev1 "github.com/xjetry/probe/gen/probe/v1"
	"github.com/xjetry/probe/internal/clock"
	"google.golang.org/protobuf/proto"
)

func at(sec int) time.Time { return time.Unix(int64(sec), 0).UTC() }

func TestOnlineIsLastSeenWithinTTL(t *testing.T) {
	clk := clock.NewFake(at(600))
	l := New(clk, 30*time.Second)
	if l.Online(1) {
		t.Fatal("never-reported node must be offline")
	}
	l.Observe(1, &probev1.Metrics{})
	if !l.Online(1) {
		t.Fatal("node must be online from its first report")
	}
	clk.Advance(29 * time.Second)
	if !l.Online(1) {
		t.Fatal("still inside TTL")
	}
	clk.Advance(time.Second)
	if l.Online(1) {
		t.Fatal("TTL elapsed, must be offline")
	}
}

func TestOnlineUsesMonotonicClock(t *testing.T) {
	clk := clock.NewFake(at(600))
	l := New(clk, 30*time.Second)
	l.Observe(1, &probev1.Metrics{})
	clk.SetWall(at(600 + 3600)) // 墙钟前跳一小时，单调钟不动
	if !l.Online(1) {
		t.Fatal("wall clock jump must not affect online state")
	}
}

func TestFlushTakesOnlyClosedBuckets(t *testing.T) {
	clk := clock.NewFake(at(600)) // 分钟 600 的起点
	l := New(clk, 30*time.Second)
	l.Observe(1, &probev1.Metrics{CpuPct: proto.Float64(10)})
	if rows := l.Flush(); len(rows) != 0 {
		t.Fatalf("bucket for the current minute must stay open, got %d rows", len(rows))
	}
	clk.Advance(60 * time.Second)
	l.Observe(1, &probev1.Metrics{CpuPct: proto.Float64(50)})
	rows := l.Flush()
	if len(rows) != 1 || rows[0].TS != 600 || rows[0].NodeID != 1 {
		t.Fatalf("rows = %+v, want one row for ts 600", rows)
	}
	if mean, _ := rows[0].Bucket.Mean(0); mean != 10 {
		t.Fatalf("flushed bucket mean = %v, want 10 (the sample at 660 belongs to the open bucket)", mean)
	}
	if again := l.Flush(); len(again) != 0 {
		t.Fatalf("flush must take the bucket away; second flush returned %d rows", len(again))
	}
}

func TestDrainTakesEverything(t *testing.T) {
	clk := clock.NewFake(at(600))
	l := New(clk, 30*time.Second)
	l.Observe(1, &probev1.Metrics{CpuPct: proto.Float64(1)})
	l.Observe(2, &probev1.Metrics{CpuPct: proto.Float64(2)})
	if rows := l.Drain(); len(rows) != 2 {
		t.Fatalf("drain returned %d rows, want 2", len(rows))
	}
}

func TestWallClockSetBackLandsInEarlierMinute(t *testing.T) {
	clk := clock.NewFake(at(660))
	l := New(clk, 30*time.Second)
	l.Observe(1, &probev1.Metrics{CpuPct: proto.Float64(1)})
	clk.SetWall(at(610)) // 回拨到上一分钟
	l.Observe(1, &probev1.Metrics{CpuPct: proto.Float64(3)})
	clk.SetWall(at(720))
	rows := l.Flush()
	if len(rows) != 2 {
		t.Fatalf("got %d rows, want separate buckets for 600 and 660", len(rows))
	}
	seen := map[int64]float64{}
	for _, r := range rows {
		seen[r.TS] = r.Bucket.Sum[0]
	}
	if seen[600] != 3 || seen[660] != 1 {
		t.Fatalf("sums by ts = %v, want 600:3 660:1", seen)
	}
}

func TestGetReflectsLatestReport(t *testing.T) {
	clk := clock.NewFake(at(600))
	l := New(clk, 30*time.Second)
	l.Observe(7, &probev1.Metrics{CpuPct: proto.Float64(42)})
	e, ok := l.Get(7)
	if !ok || e.Metrics.GetCpuPct() != 42 || !e.Online {
		t.Fatalf("entry = %+v ok=%v", e, ok)
	}
	l.Forget(7)
	if _, ok := l.Get(7); ok {
		t.Fatal("forgotten node must be gone")
	}
}
```

- [ ] **Step 2: 跑，确认红**

`go test -count=1 ./internal/hub/live/ > /tmp/t4.log 2>&1; echo $?` → 非 0。

- [ ] **Step 3: 实现**

```go
// Package live 持有每个节点的实时状态：最新指标、last_seen 与未落盘的分钟桶。
//
// 不变式：在线 ⇔ now − last_seen < TTL，且这是在线的唯一来源——不存在第二张
// 在线表，也没有连接状态可以与它分叉。last_seen 用单调钟，墙钟回拨不影响。
package live

import (
	"sync"
	"time"

	probev1 "github.com/xjetry/probe/gen/probe/v1"
	"github.com/xjetry/probe/internal/clock"
	"github.com/xjetry/probe/internal/hub/metric"
)

type Live struct {
	mu    sync.Mutex
	clk   clock.Clock
	ttl   time.Duration
	nodes map[int64]*entry
}

type entry struct {
	metrics      *probev1.Metrics
	lastSeen     time.Duration
	lastSeenWall time.Time
	// buckets 按桶起始（墙钟 Unix 秒，60 对齐）索引。同一节点可以同时有多个
	// 未刷出的桶：墙钟回拨时新样本会落进更早的分钟，它们各自独立、刷出后
	// 由写库时的加法合并并入已有的行。
	buckets map[int64]*metric.Bucket
}

type Entry struct {
	Metrics  *probev1.Metrics
	LastSeen time.Duration
	Online   bool
}

func New(clk clock.Clock, ttl time.Duration) *Live {
	return &Live{clk: clk, ttl: ttl, nodes: map[int64]*entry{}}
}

func minuteOf(t time.Time) int64 {
	s := t.Unix()
	return s - s%60
}

// Observe 记录一次已通过校验的上报。调用方保证 m 不再被修改。
func (l *Live) Observe(nodeID int64, m *probev1.Metrics) {
	now, wall := l.clk.Mono(), l.clk.Now()
	ts := minuteOf(wall)
	l.mu.Lock()
	defer l.mu.Unlock()
	e := l.nodes[nodeID]
	if e == nil {
		e = &entry{buckets: map[int64]*metric.Bucket{}}
		l.nodes[nodeID] = e
	}
	e.metrics, e.lastSeen, e.lastSeenWall = m, now, wall
	b := e.buckets[ts]
	if b == nil {
		b = metric.NewBucket()
		e.buckets[ts] = b
	}
	b.Add(m)
}

func (l *Live) online(e *entry, now time.Duration) bool {
	return now-e.lastSeen < l.ttl
}

func (l *Live) Online(nodeID int64) bool {
	now := l.clk.Mono()
	l.mu.Lock()
	defer l.mu.Unlock()
	e, ok := l.nodes[nodeID]
	return ok && l.online(e, now)
}

func (l *Live) Get(nodeID int64) (Entry, bool) {
	now := l.clk.Mono()
	l.mu.Lock()
	defer l.mu.Unlock()
	e, ok := l.nodes[nodeID]
	if !ok {
		return Entry{}, false
	}
	return Entry{Metrics: e.metrics, LastSeen: e.lastSeen, Online: l.online(e, now)}, true
}

// Flush 取走所有已闭合的桶：起始早于当前分钟的。取走即从 live 删除——每个
// 桶至多被交给写协程一次，写库的加法合并才不会重复计入。
func (l *Live) Flush() []metric.Row {
	return l.take(minuteOf(l.clk.Now()))
}

// Drain 取走全部桶，包括当前分钟仍开着的；退出时用。
func (l *Live) Drain() []metric.Row {
	return l.take(1<<62 - 1)
}

func (l *Live) take(before int64) []metric.Row {
	l.mu.Lock()
	defer l.mu.Unlock()
	var rows []metric.Row
	for id, e := range l.nodes {
		for ts, b := range e.buckets {
			if ts >= before {
				continue
			}
			rows = append(rows, metric.Row{NodeID: id, TS: ts, Bucket: b, LastSeen: e.lastSeenWall})
			delete(e.buckets, ts)
		}
	}
	return rows
}

// Forget 删除节点时调用；未刷出的桶随之丢弃，被删节点的历史无处可挂。
func (l *Live) Forget(nodeID int64) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.nodes, nodeID)
}
```

- [ ] **Step 4: 跑，确认绿**

`go test -count=1 ./internal/hub/live/ > /tmp/t4.log 2>&1; echo $?` → 0。

- [ ] **Step 5: 缺陷注入（两处）**

1. `online` 改用墙钟：`return l.clk.Now().Sub(e.lastSeenWall) < l.ttl`，预期 `TestOnlineUsesMonotonicClock` 红。改回。
2. `take` 里去掉 `delete(e.buckets, ts)`，预期 `TestFlushTakesOnlyClosedBuckets` 红且报错含 "second flush returned 1 rows"。改回。

- [ ] **Step 6: 提交**

```bash
git add internal/hub/live
git commit -m "hub: 节点实时状态、在线判定与分钟桶

在线只有一个来源：单调钟下 now − last_seen < TTL。分钟桶按墙钟分钟
索引，回拨时落进更早的桶各自独立；刷出即取走，每个桶至多交给写协程
一次，写库的加法合并才不会重复计入。"
```

---

### Task 5: 存储——schema、写协程、节点、注册窗口、facts、分钟行

**必读**：spec §6.1、§6.2、§6.4 冻结不变式第 1 条、§6.6；`FEATURES.md` 第 1 条中"这一条对 M1 有约束"段。

**Files:**
- Create: `internal/hub/store/store.go`、`schema.go`、`node.go`、`window.go`、`facts.go`、`metric.go`、`store_test.go`

**Interfaces:**
- Consumes: `clock.Clock`、`metric.Columns`、`metric.Row`、`probev1.Facts`。
- Produces:
  ```go
  func Open(path string, clk clock.Clock, log *slog.Logger) (*Store, error)
  func (s *Store) Close() error
  // node
  type Node struct { ID int64; Name string; Public bool; Note string; CreatedAt, LastSeenAt time.Time }
  func (s *Store) CreateNode(ctx, name string, tokenHash []byte) (int64, error)
  func (s *Store) ListNodes(ctx) ([]Node, error)
  func (s *Store) DeleteNode(ctx, id int64) error            // 不存在 → ErrNotFound
  func (s *Store) SetTokenHash(ctx, id int64, hash []byte) error   // 不存在 → ErrNotFound
  func (s *Store) TokenHashes(ctx) (map[[32]byte]int64, error)
  // register window
  type Window struct { ExpiresAt time.Time; Remaining int }
  func (s *Store) SetRegisterWindow(ctx, keyHash []byte, expiresAt time.Time, max int) error
  func (s *Store) ClearRegisterWindow(ctx) error
  func (s *Store) RegisterWindow(ctx) (Window, bool, error)
  func (s *Store) RegisterNode(ctx, keyHash []byte, name string, tokenHash []byte) (int64, error)  // ErrNoWindow / ErrBadKey
  // facts
  func (s *Store) UpsertFacts(ctx, nodeID int64, hash uint64, f *probev1.Facts) error
  func (s *Store) UpsertFactsAsync(nodeID int64, hash uint64, f *probev1.Facts, done func(error))
  func (s *Store) FactsHashes(ctx) (map[int64]uint64, error)
  // metric
  func (s *Store) WriteMinuteRows(ctx, rows []metric.Row) (rejected int, err error)
  func (s *Store) ReadMinuteRows(ctx, nodeID int64, from, to int64) ([]metric.Row, error)
  func (s *Store) SetRollupWatermark(ctx, level string, uptoTS int64) error
  var ErrNotFound, ErrNoWindow, ErrBadKey error
  ```

- [ ] **Step 1: 失败的测试**

```go
package store

import (
	"context"
	"database/sql"
	"errors"
	"log/slog"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	probev1 "github.com/xjetry/probe/gen/probe/v1"
	"github.com/xjetry/probe/internal/clock"
	"github.com/xjetry/probe/internal/hub/metric"
	"google.golang.org/protobuf/proto"
)

func open(t *testing.T) (*Store, *clock.Fake) {
	t.Helper()
	clk := clock.NewFake(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	s, err := Open(filepath.Join(t.TempDir(), "t.db"), clk, slog.Default())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s, clk
}

func hash(b byte) []byte { h := make([]byte, 32); h[0] = b; return h }

func TestOpenCreatesSchemaAtCurrentVersion(t *testing.T) {
	s, _ := open(t)
	var v int
	if err := s.r.QueryRow("PRAGMA user_version").Scan(&v); err != nil {
		t.Fatal(err)
	}
	if v != schemaVersion {
		t.Fatalf("user_version = %d, want %d", v, schemaVersion)
	}
	var seq int
	if err := s.r.QueryRow("SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = 'sqlite_sequence'").Scan(&seq); err != nil {
		t.Fatal(err)
	}
	if seq != 1 {
		t.Fatal("node.id must be AUTOINCREMENT: SQLite creates sqlite_sequence only when some table uses it")
	}
}

func TestReopenKeepsData(t *testing.T) {
	dir := t.TempDir()
	clk := clock.NewFake(time.Unix(0, 0))
	s, err := Open(filepath.Join(dir, "t.db"), clk, slog.Default())
	if err != nil {
		t.Fatal(err)
	}
	id, err := s.CreateNode(context.Background(), "a", hash(1))
	if err != nil {
		t.Fatal(err)
	}
	s.Close()
	s, err = Open(filepath.Join(dir, "t.db"), clk, slog.Default())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	nodes, err := s.ListNodes(context.Background())
	if err != nil || len(nodes) != 1 || nodes[0].ID != id {
		t.Fatalf("nodes = %+v err = %v", nodes, err)
	}
}

func TestDeletedNodeIDIsNotReused(t *testing.T) {
	s, _ := open(t)
	ctx := context.Background()
	a, _ := s.CreateNode(ctx, "a", hash(1))
	if err := s.DeleteNode(ctx, a); err != nil {
		t.Fatal(err)
	}
	b, _ := s.CreateNode(ctx, "b", hash(2))
	if b == a {
		t.Fatalf("id %d was reused after delete", a)
	}
}

func TestTokenHashesAndRotate(t *testing.T) {
	s, _ := open(t)
	ctx := context.Background()
	id, _ := s.CreateNode(ctx, "a", hash(1))
	if err := s.SetTokenHash(ctx, id, hash(9)); err != nil {
		t.Fatal(err)
	}
	m, err := s.TokenHashes(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var k [32]byte
	copy(k[:], hash(9))
	if m[k] != id || len(m) != 1 {
		t.Fatalf("hashes = %v", m)
	}
	if err := s.SetTokenHash(ctx, id+100, hash(3)); !errors.Is(err, ErrNotFound) {
		t.Fatalf("rotate on missing node: err = %v, want ErrNotFound", err)
	}
}

func TestRegisterNodeConsumesWindow(t *testing.T) {
	s, clk := open(t)
	ctx := context.Background()
	if _, err := s.RegisterNode(ctx, hash(5), "x", hash(1)); !errors.Is(err, ErrNoWindow) {
		t.Fatalf("no window: err = %v", err)
	}
	if err := s.SetRegisterWindow(ctx, hash(5), clk.Now().Add(time.Hour), 2); err != nil {
		t.Fatal(err)
	}
	if _, err := s.RegisterNode(ctx, hash(6), "x", hash(1)); !errors.Is(err, ErrBadKey) {
		t.Fatalf("wrong key: err = %v", err)
	}
	if _, err := s.RegisterNode(ctx, hash(5), "x", hash(1)); err != nil {
		t.Fatal(err)
	}
	if _, err := s.RegisterNode(ctx, hash(5), "y", hash(2)); err != nil {
		t.Fatal(err)
	}
	if _, err := s.RegisterNode(ctx, hash(5), "z", hash(3)); !errors.Is(err, ErrNoWindow) {
		t.Fatalf("exhausted window must read as closed, err = %v", err)
	}
	w, ok, _ := s.RegisterWindow(ctx)
	if !ok || w.Remaining != 0 {
		t.Fatalf("window = %+v ok=%v", w, ok)
	}
}

func TestExpiredWindowIsClosed(t *testing.T) {
	s, clk := open(t)
	ctx := context.Background()
	_ = s.SetRegisterWindow(ctx, hash(5), clk.Now().Add(time.Minute), 5)
	clk.Advance(2 * time.Minute)
	if _, err := s.RegisterNode(ctx, hash(5), "x", hash(1)); !errors.Is(err, ErrNoWindow) {
		t.Fatalf("expired: err = %v", err)
	}
}

func TestFacts(t *testing.T) {
	s, _ := open(t)
	ctx := context.Background()
	id, _ := s.CreateNode(ctx, "a", hash(1))
	f := &probev1.Facts{Hostname: "h", Os: "o", CpuCores: 4}
	if err := s.UpsertFacts(ctx, id, 77, f); err != nil {
		t.Fatal(err)
	}
	f.Hostname = "h2"
	done := make(chan error, 1)
	s.UpsertFactsAsync(id, 78, f, func(err error) { done <- err })
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	m, _ := s.FactsHashes(ctx)
	if m[id] != 78 {
		t.Fatalf("facts hash = %d, want 78", m[id])
	}
}

func bucket(cpu float64) *metric.Bucket {
	b := metric.NewBucket()
	b.Add(&probev1.Metrics{CpuPct: proto.Float64(cpu)})
	return b
}

func TestHalfBucketsMergeAdditively(t *testing.T) {
	s, _ := open(t)
	ctx := context.Background()
	id, _ := s.CreateNode(ctx, "a", hash(1))
	if _, err := s.WriteMinuteRows(ctx, []metric.Row{{NodeID: id, TS: 600, Bucket: bucket(10)}}); err != nil {
		t.Fatal(err)
	}
	b := bucket(30)
	b.Add(&probev1.Metrics{CpuPct: proto.Float64(50)})
	if _, err := s.WriteMinuteRows(ctx, []metric.Row{{NodeID: id, TS: 600, Bucket: b}}); err != nil {
		t.Fatal(err)
	}
	rows, err := s.ReadMinuteRows(ctx, id, 0, 1000)
	if err != nil || len(rows) != 1 {
		t.Fatalf("rows = %+v err = %v", rows, err)
	}
	if rows[0].Bucket.Sum[0] != 90 || rows[0].Bucket.N[0] != 3 || rows[0].Bucket.Max[0] != 50 {
		t.Fatalf("merged = %v/%d/%v, want 90/3/50", rows[0].Bucket.Sum[0], rows[0].Bucket.N[0], rows[0].Bucket.Max[0])
	}
}

func TestMissingMetricReadsBackAsNoData(t *testing.T) {
	s, _ := open(t)
	ctx := context.Background()
	id, _ := s.CreateNode(ctx, "a", hash(1))
	_, _ = s.WriteMinuteRows(ctx, []metric.Row{{NodeID: id, TS: 600, Bucket: bucket(10)}})
	rows, _ := s.ReadMinuteRows(ctx, id, 0, 1000)
	for i, c := range metric.Columns {
		_, ok := rows[0].Bucket.Mean(i)
		if c.Name == "cpu" && !ok {
			t.Fatal("cpu had a reading")
		}
		if c.Name != "cpu" && ok {
			t.Fatalf("%s had no readings but reads back as data", c.Name)
		}
	}
}

func TestWriterRejectsRowsBeforeRollupWatermark(t *testing.T) {
	s, _ := open(t)
	ctx := context.Background()
	id, _ := s.CreateNode(ctx, "a", hash(1))
	if err := s.SetRollupWatermark(ctx, "5m", 900); err != nil {
		t.Fatal(err)
	}
	rejected, err := s.WriteMinuteRows(ctx, []metric.Row{
		{NodeID: id, TS: 600, Bucket: bucket(1)},
		{NodeID: id, TS: 900, Bucket: bucket(2)},
	})
	if err != nil || rejected != 1 {
		t.Fatalf("rejected = %d err = %v, want 1 nil", rejected, err)
	}
	rows, _ := s.ReadMinuteRows(ctx, id, 0, 2000)
	if len(rows) != 1 || rows[0].TS != 900 {
		t.Fatalf("rows = %+v, want only ts 900", rows)
	}
}

func TestWriteMinuteRowsUpdatesLastSeen(t *testing.T) {
	s, _ := open(t)
	ctx := context.Background()
	id, _ := s.CreateNode(ctx, "a", hash(1))
	seen := time.Unix(1234, 0).UTC()
	_, _ = s.WriteMinuteRows(ctx, []metric.Row{{NodeID: id, TS: 1200, Bucket: bucket(1), LastSeen: seen}})
	nodes, _ := s.ListNodes(ctx)
	if !nodes[0].LastSeenAt.Equal(seen) {
		t.Fatalf("last_seen_at = %v, want %v", nodes[0].LastSeenAt, seen)
	}
}

func TestDeleteNodeRemovesDependentRows(t *testing.T) {
	s, _ := open(t)
	ctx := context.Background()
	id, _ := s.CreateNode(ctx, "a", hash(1))
	_ = s.UpsertFacts(ctx, id, 1, &probev1.Facts{})
	_, _ = s.WriteMinuteRows(ctx, []metric.Row{{NodeID: id, TS: 600, Bucket: bucket(1)}})
	if err := s.DeleteNode(ctx, id); err != nil {
		t.Fatal(err)
	}
	if m, _ := s.FactsHashes(ctx); len(m) != 0 {
		t.Fatalf("facts survived delete: %v", m)
	}
	if rows, _ := s.ReadMinuteRows(ctx, id, 0, 1000); len(rows) != 0 {
		t.Fatalf("metric rows survived delete: %v", rows)
	}
}

// 回调必须在事务提交之后触发：调用方据它更新的内存状态不能先于持久化。
func TestAsyncCallbackObservesCommittedWrite(t *testing.T) {
	s, _ := open(t)
	ctx := context.Background()
	id, _ := s.CreateNode(ctx, "a", hash(1))
	if err := s.UpsertFacts(ctx, id, 77, &probev1.Facts{}); err != nil {
		t.Fatal(err)
	}
	seen := make(chan uint64, 1)
	s.UpsertFactsAsync(id, 78, &probev1.Facts{}, func(err error) {
		if err != nil {
			t.Error(err)
		}
		m, _ := s.FactsHashes(ctx)
		seen <- m[id]
	})
	if got := <-seen; got != 78 {
		t.Fatalf("callback observed facts hash %d, want 78: it must run only after the transaction committed", got)
	}
}

// 已入队但尚未开始的事务在 ctx 取消后不执行：调用方得到 ctx.Err() 且库无变化。
func TestCancelBeforeStartSkipsTransaction(t *testing.T) {
	s, _ := open(t)
	gate := make(chan struct{})
	started := make(chan struct{})
	go s.write(context.Background(), func(*sql.Tx) error { close(started); <-gate; return nil })
	<-started // 写协程正被第一个事务占住，后面的请求只能排队
	ctx, cancel := context.WithCancel(context.Background())
	res := make(chan error, 1)
	go func() { _, err := s.CreateNode(ctx, "queued", hash(1)); res <- err }()
	for len(s.writes) == 0 {
		runtime.Gosched() // 等它真的入队，否则走的是入队前取消那条路径
	}
	cancel()
	close(gate)
	if err := <-res; !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	if nodes, _ := s.ListNodes(context.Background()); len(nodes) != 0 {
		t.Fatalf("node was created despite cancellation before start: %+v", nodes)
	}
}

// 已开始的事务不受取消影响，调用方必须拿到真实结果：nil 且库里有行。
func TestCancelDuringTransactionStillReportsCommit(t *testing.T) {
	s, _ := open(t)
	ctx, cancel := context.WithCancel(context.Background())
	inside := make(chan struct{})
	res := make(chan error, 1)
	go func() {
		res <- s.write(ctx, func(tx *sql.Tx) error {
			close(inside)
			<-ctx.Done() // 事务进行中 ctx 被取消
			_, err := tx.Exec("INSERT INTO node (name, token_hash, created_at) VALUES ('mid', ?, 0)", hash(2))
			return err
		})
	}()
	<-inside
	cancel()
	if err := <-res; err != nil {
		t.Fatalf("err = %v, want nil: the transaction committed", err)
	}
	if nodes, _ := s.ListNodes(context.Background()); len(nodes) != 1 {
		t.Fatalf("committed row missing: %+v", nodes)
	}
}
```

- [ ] **Step 2: 跑，确认红**

`go test -count=1 ./internal/hub/store/ > /tmp/t5.log 2>&1; echo $?` → 非 0。

- [ ] **Step 3: store.go——打开、写协程、只读池、迁移**

```go
// Package store 是 hub 唯一的持久化层：单文件 SQLite，纯 Go 驱动。
//
// 不变式：所有写都经 runWriter 串行执行，w 只在那个协程里被使用。SQLite 同一
// 时刻只允许一个写者，应用内串行化从根上避免写者之间的 SQLITE_BUSY；读走
// 独立的只读连接池（query_only），WAL 下读不阻塞写。
package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"

	_ "modernc.org/sqlite"

	"github.com/xjetry/probe/internal/clock"
)

var (
	ErrNotFound = errors.New("not found")
	ErrNoWindow = errors.New("register window closed")
	ErrBadKey   = errors.New("register key mismatch")
)

type Store struct {
	w      *sql.DB
	r      *sql.DB
	clk    clock.Clock
	log    *slog.Logger
	writes chan writeReq
	done   chan struct{}
}

type writeReq struct {
	// ctx 只在写协程开始事务前被检查：已取消就不开事务。异步请求为 nil，永不取消。
	ctx context.Context
	fn  func(*sql.Tx) error
	// res 与 done 都由 runWriter 在 inTx 返回（事务已提交或已回滚）之后触发：
	// 等待方与回调方看到的都是已持久化的状态。res 非 nil 表示调用方在等结果；
	// done 非 nil 表示投递即返回、结果经回调送达。
	res  chan error
	done func(error)
}

func dsn(path string, extra string) string {
	return "file:" + path + "?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)&_pragma=synchronous(NORMAL)" + extra
}

func Open(path string, clk clock.Clock, log *slog.Logger) (*Store, error) {
	w, err := sql.Open("sqlite", dsn(path, ""))
	if err != nil {
		return nil, err
	}
	w.SetMaxOpenConns(1)
	if err := migrate(w); err != nil {
		w.Close()
		return nil, err
	}
	r, err := sql.Open("sqlite", dsn(path, "&_pragma=query_only(1)"))
	if err != nil {
		w.Close()
		return nil, err
	}
	s := &Store{w: w, r: r, clk: clk, log: log, writes: make(chan writeReq, 1024), done: make(chan struct{})}
	go s.runWriter()
	return s, nil
}

// Close 等待队列里的写全部执行完再关闭连接，退出时投递的最后一批刷出不丢。
func (s *Store) Close() error {
	close(s.writes)
	<-s.done
	return errors.Join(s.r.Close(), s.w.Close())
}

func (s *Store) runWriter() {
	defer close(s.done)
	for req := range s.writes {
		var err error
		if req.ctx != nil && req.ctx.Err() != nil {
			// 尚未开始的事务尊重取消：不开事务，以 ctx.Err() 作为结果分发。
			err = req.ctx.Err()
		} else {
			err = s.inTx(req.fn)
		}
		switch {
		case req.res != nil:
			req.res <- err
		case req.done != nil:
			req.done(err)
		case err != nil:
			s.log.Error("async write failed", "err", err)
		}
	}
}

func (s *Store) inTx(fn func(*sql.Tx) error) error {
	tx, err := s.w.Begin()
	if err != nil {
		return err
	}
	if err := fn(tx); err != nil {
		tx.Rollback()
		return err
	}
	return tx.Commit()
}

// write 投递一个事务并等待其结果。
//
// 结果语义是 auth 只在 nil 后改映射的前提：返回错误意味着事务未应用，返回 nil
// 意味着已提交。所以一旦入队就无条件等到写协程的结果——中途随 ctx 放弃等待
// 会让调用方在事务照常提交时误以为失败，映射与库由此分叉。取消只在两处生效：
// 入队之前，以及写协程开始事务之前（runWriter 的预检）。
func (s *Store) write(ctx context.Context, fn func(*sql.Tx) error) error {
	req := writeReq{ctx: ctx, fn: fn, res: make(chan error, 1)}
	select {
	case s.writes <- req:
	case <-ctx.Done():
		return ctx.Err()
	}
	return <-req.res
}

// writeAsync 投递后立即返回；done 在写协程里、事务提交之后被调用。队列满时
// 丢弃并报告，调用方据此保持自己的状态不变，让下一次上报重新触发。
func (s *Store) writeAsync(fn func(*sql.Tx) error, done func(error)) {
	req := writeReq{fn: fn, done: done}
	select {
	case s.writes <- req:
	default:
		s.log.Warn("write queue full, dropping async write")
		if done != nil {
			done(errors.New("write queue full"))
		}
	}
}

const schemaVersion = 1

// migrations[v] 把 user_version = v−1 的库升到 v。空库不重放历史，直接建
// 到当前版本；所以 schema 常量必须始终是"当前版本的完整 DDL"。
var migrations = map[int]func(*sql.Tx) error{}

func migrate(db *sql.DB) error {
	var v int
	if err := db.QueryRow("PRAGMA user_version").Scan(&v); err != nil {
		return err
	}
	switch {
	case v == schemaVersion:
		return nil
	case v > schemaVersion:
		return fmt.Errorf("database schema version %d is newer than this binary (%d)", v, schemaVersion)
	case v == 0:
		return inTxDB(db, func(tx *sql.Tx) error {
			for _, stmt := range schemaStatements() {
				if _, err := tx.Exec(stmt); err != nil {
					return fmt.Errorf("%w in %q", err, stmt)
				}
			}
			_, err := tx.Exec(fmt.Sprintf("PRAGMA user_version = %d", schemaVersion))
			return err
		})
	}
	for next := v + 1; next <= schemaVersion; next++ {
		step, ok := migrations[next]
		if !ok {
			return fmt.Errorf("no migration to schema version %d", next)
		}
		if err := inTxDB(db, func(tx *sql.Tx) error {
			if err := step(tx); err != nil {
				return err
			}
			_, err := tx.Exec(fmt.Sprintf("PRAGMA user_version = %d", next))
			return err
		}); err != nil {
			return err
		}
	}
	return nil
}

func inTxDB(db *sql.DB, fn func(*sql.Tx) error) error {
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	if err := fn(tx); err != nil {
		tx.Rollback()
		return err
	}
	return tx.Commit()
}
```

- [ ] **Step 4: schema.go——DDL 与从描述表生成的 SQL**

```go
package store

import (
	"strings"

	"github.com/xjetry/probe/internal/hub/metric"
)

// 与描述表无关的表写成常量；metric_1m 由 metricDDL 生成。
const schemaFixed = `
CREATE TABLE node (
  -- AUTOINCREMENT 使 id 永不复用：分层备份恢复后两层可能各自漂移，
  -- id 若复用，指标层里已删节点的历史会挂到同 id 的新节点上且无法肉眼分辨。
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  name TEXT NOT NULL,
  sort_order INTEGER NOT NULL DEFAULT 0,
  public INTEGER NOT NULL DEFAULT 0,
  note TEXT NOT NULL DEFAULT '',
  -- NULL 表示"用默认值（TTL）"，是缺省不是放宽；读侧遇 NULL 必须取 TTL。
  offline_grace_s INTEGER,
  traffic_reset_day INTEGER NOT NULL DEFAULT 1,
  token_hash BLOB NOT NULL UNIQUE,
  created_at INTEGER NOT NULL,
  -- 墙钟，只供展示与告警文案，不参与离线时长计算。
  last_seen_at INTEGER
);
CREATE TABLE node_facts (
  node_id INTEGER PRIMARY KEY,
  facts_hash INTEGER NOT NULL,
  hostname TEXT NOT NULL,
  os TEXT NOT NULL,
  kernel TEXT NOT NULL,
  arch TEXT NOT NULL,
  virtualization TEXT NOT NULL,
  cpu_model TEXT NOT NULL,
  cpu_cores INTEGER NOT NULL,
  agent_version TEXT NOT NULL,
  icmp_available INTEGER NOT NULL,
  updated_at INTEGER NOT NULL
);
CREATE TABLE register_window (
  id INTEGER PRIMARY KEY CHECK (id = 1),
  key_hash BLOB NOT NULL,
  expires_at INTEGER NOT NULL,
  remaining INTEGER NOT NULL
);
CREATE TABLE rollup_state (
  level TEXT PRIMARY KEY,
  upto_ts INTEGER NOT NULL
);
INSERT INTO rollup_state (level, upto_ts) VALUES ('5m', 0), ('1h', 0);
`

func schemaStatements() []string {
	var out []string
	for _, stmt := range strings.Split(schemaFixed, ";") {
		if strings.TrimSpace(stmt) != "" {
			out = append(out, stmt)
		}
	}
	return append(out, metricDDL("metric_1m"))
}

// metricDDL 从描述表生成分钟表。主键顺序 (node_id, ts) 即唯一查询路径，
// WITHOUT ROWID 使主键索引就是表本身。
func metricDDL(table string) string {
	cols := []string{"node_id INTEGER NOT NULL", "ts INTEGER NOT NULL"}
	for _, c := range metric.Columns {
		cols = append(cols, c.Name+"_sum "+c.SQLType()+" NOT NULL", c.Name+"_n INTEGER NOT NULL")
		if c.Kind == metric.MeanMax {
			cols = append(cols, c.Name+"_max "+c.SQLType()+" NOT NULL")
		}
	}
	return "CREATE TABLE " + table + " (" + strings.Join(cols, ", ") + ", PRIMARY KEY (node_id, ts)) WITHOUT ROWID"
}

// metricColumnNames 是 SQL 里列的顺序：与 Bucket 切片按描述表下标对应。
func metricColumnNames() []string {
	var names []string
	for _, c := range metric.Columns {
		names = append(names, c.Name+"_sum", c.Name+"_n")
		if c.Kind == metric.MeanMax {
			names = append(names, c.Name+"_max")
		}
	}
	return names
}

// metricUpsert 生成加法合并语句。合并正确的前提由 live 保证：每个内存桶至多
// 成功写入一次（刷出即取走）；事务原子性保证失败即未应用，重试不会重复计入。
func metricUpsert(table string) string {
	names := metricColumnNames()
	var sets []string
	for _, c := range metric.Columns {
		sets = append(sets,
			c.Name+"_sum = "+c.Name+"_sum + excluded."+c.Name+"_sum",
			c.Name+"_n = "+c.Name+"_n + excluded."+c.Name+"_n")
		if c.Kind == metric.MeanMax {
			sets = append(sets, c.Name+"_max = max("+c.Name+"_max, excluded."+c.Name+"_max)")
		}
	}
	all := append([]string{"node_id", "ts"}, names...)
	return "INSERT INTO " + table + " (" + strings.Join(all, ", ") + ") VALUES (" +
		strings.TrimSuffix(strings.Repeat("?, ", len(all)), ", ") + ") ON CONFLICT (node_id, ts) DO UPDATE SET " +
		strings.Join(sets, ", ")
}

func metricSelect(table string) string {
	return "SELECT ts, " + strings.Join(metricColumnNames(), ", ") + " FROM " + table +
		" WHERE node_id = ? AND ts >= ? AND ts < ? ORDER BY ts"
}

// bucketArgs 把桶按描述表顺序展开成绑定参数；整数列在此处转回整型。
func bucketArgs(b *metric.Bucket) []any {
	var args []any
	for i, c := range metric.Columns {
		if c.Type == metric.Int {
			args = append(args, int64(b.Sum[i]), int64(b.N[i]))
			if c.Kind == metric.MeanMax {
				args = append(args, int64(b.Max[i]))
			}
		} else {
			args = append(args, b.Sum[i], int64(b.N[i]))
			if c.Kind == metric.MeanMax {
				args = append(args, b.Max[i])
			}
		}
	}
	return args
}
```

- [ ] **Step 5: node.go**

```go
package store

import (
	"context"
	"database/sql"
	"time"
)

type Node struct {
	ID         int64
	Name       string
	Public     bool
	Note       string
	CreatedAt  time.Time
	LastSeenAt time.Time // 零值表示从未上报
}

func (s *Store) CreateNode(ctx context.Context, name string, tokenHash []byte) (int64, error) {
	var id int64
	err := s.write(ctx, func(tx *sql.Tx) error {
		res, err := tx.Exec("INSERT INTO node (name, token_hash, created_at) VALUES (?, ?, ?)",
			name, tokenHash, s.clk.Now().Unix())
		if err != nil {
			return err
		}
		id, err = res.LastInsertId()
		return err
	})
	return id, err
}

func (s *Store) ListNodes(ctx context.Context) ([]Node, error) {
	rows, err := s.r.QueryContext(ctx,
		"SELECT id, name, public, note, created_at, last_seen_at FROM node ORDER BY sort_order, id")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Node
	for rows.Next() {
		var n Node
		var created int64
		var seen sql.NullInt64
		if err := rows.Scan(&n.ID, &n.Name, &n.Public, &n.Note, &created, &seen); err != nil {
			return nil, err
		}
		n.CreatedAt = time.Unix(created, 0).UTC()
		if seen.Valid {
			n.LastSeenAt = time.Unix(seen.Int64, 0).UTC()
		}
		out = append(out, n)
	}
	return out, rows.Err()
}

// DeleteNode 显式删除从属行。不依赖外键：SQLite 默认不开启外键约束，
// 而备份恢复是整表覆盖、不触发级联；靠显式删除才在两条路径上都成立。
func (s *Store) DeleteNode(ctx context.Context, id int64) error {
	return s.write(ctx, func(tx *sql.Tx) error {
		res, err := tx.Exec("DELETE FROM node WHERE id = ?", id)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return ErrNotFound
		}
		for _, q := range []string{
			"DELETE FROM node_facts WHERE node_id = ?",
			"DELETE FROM metric_1m WHERE node_id = ?",
		} {
			if _, err := tx.Exec(q, id); err != nil {
				return err
			}
		}
		return nil
	})
}

func (s *Store) SetTokenHash(ctx context.Context, id int64, hash []byte) error {
	return s.write(ctx, func(tx *sql.Tx) error {
		res, err := tx.Exec("UPDATE node SET token_hash = ? WHERE id = ?", hash, id)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return ErrNotFound
		}
		return nil
	})
}

func (s *Store) TokenHashes(ctx context.Context) (map[[32]byte]int64, error) {
	rows, err := s.r.QueryContext(ctx, "SELECT id, token_hash FROM node")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[[32]byte]int64{}
	for rows.Next() {
		var id int64
		var h []byte
		if err := rows.Scan(&id, &h); err != nil {
			return nil, err
		}
		var k [32]byte
		copy(k[:], h)
		out[k] = id
	}
	return out, rows.Err()
}
```

- [ ] **Step 6: window.go**

```go
package store

import (
	"bytes"
	"context"
	"database/sql"
	"time"
)

type Window struct {
	ExpiresAt time.Time
	Remaining int
}

// SetRegisterWindow 替换当前窗口：同一时刻只有一个窗口，新开即作废旧 key。
func (s *Store) SetRegisterWindow(ctx context.Context, keyHash []byte, expiresAt time.Time, max int) error {
	return s.write(ctx, func(tx *sql.Tx) error {
		_, err := tx.Exec(`INSERT INTO register_window (id, key_hash, expires_at, remaining) VALUES (1, ?, ?, ?)
			ON CONFLICT (id) DO UPDATE SET key_hash = excluded.key_hash, expires_at = excluded.expires_at, remaining = excluded.remaining`,
			keyHash, expiresAt.Unix(), max)
		return err
	})
}

func (s *Store) ClearRegisterWindow(ctx context.Context) error {
	return s.write(ctx, func(tx *sql.Tx) error {
		_, err := tx.Exec("DELETE FROM register_window")
		return err
	})
}

// RegisterWindow 返回窗口原样，不判过期：判定属于 RegisterNode 的事务，
// 展示侧只需要把截止时间给人看。
func (s *Store) RegisterWindow(ctx context.Context) (Window, bool, error) {
	var w Window
	var exp int64
	err := s.r.QueryRowContext(ctx, "SELECT expires_at, remaining FROM register_window WHERE id = 1").Scan(&exp, &w.Remaining)
	if err == sql.ErrNoRows {
		return Window{}, false, nil
	}
	if err != nil {
		return Window{}, false, err
	}
	w.ExpiresAt = time.Unix(exp, 0).UTC()
	return w, true, nil
}

// RegisterNode 在一个事务里判定窗口、比对 key、建节点、消耗名额。
//
// ErrNoWindow 覆盖"没有窗口 / 已过期 / 名额用尽"，ErrBadKey 只表示窗口开着
// 但 key 不对：调用方对外把两者映射成同一响应，但只对后者计失败次数——
// 窗口关闭时没有可猜的秘密，计数只会误伤与他人共用出口地址的运维者。
func (s *Store) RegisterNode(ctx context.Context, keyHash []byte, name string, tokenHash []byte) (int64, error) {
	var id int64
	err := s.write(ctx, func(tx *sql.Tx) error {
		var stored []byte
		var exp int64
		var remaining int
		err := tx.QueryRow("SELECT key_hash, expires_at, remaining FROM register_window WHERE id = 1").Scan(&stored, &exp, &remaining)
		if err == sql.ErrNoRows {
			return ErrNoWindow
		}
		if err != nil {
			return err
		}
		if s.clk.Now().Unix() >= exp || remaining <= 0 {
			return ErrNoWindow
		}
		if !bytes.Equal(stored, keyHash) {
			return ErrBadKey
		}
		res, err := tx.Exec("INSERT INTO node (name, token_hash, created_at) VALUES (?, ?, ?)", name, tokenHash, s.clk.Now().Unix())
		if err != nil {
			return err
		}
		if id, err = res.LastInsertId(); err != nil {
			return err
		}
		_, err = tx.Exec("UPDATE register_window SET remaining = remaining - 1 WHERE id = 1")
		return err
	})
	return id, err
}
```

- [ ] **Step 7: facts.go**

```go
package store

import (
	"context"
	"database/sql"

	probev1 "github.com/xjetry/probe/gen/probe/v1"
)

func (s *Store) factsTx(nodeID int64, hash uint64, f *probev1.Facts) func(*sql.Tx) error {
	return func(tx *sql.Tx) error {
		_, err := tx.Exec(`INSERT INTO node_facts (node_id, facts_hash, hostname, os, kernel, arch, virtualization, cpu_model, cpu_cores, agent_version, icmp_available, updated_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
			ON CONFLICT (node_id) DO UPDATE SET facts_hash = excluded.facts_hash, hostname = excluded.hostname, os = excluded.os,
			kernel = excluded.kernel, arch = excluded.arch, virtualization = excluded.virtualization, cpu_model = excluded.cpu_model,
			cpu_cores = excluded.cpu_cores, agent_version = excluded.agent_version, icmp_available = excluded.icmp_available, updated_at = excluded.updated_at`,
			nodeID, int64(hash), f.GetHostname(), f.GetOs(), f.GetKernel(), f.GetArch(), f.GetVirtualization(),
			f.GetCpuModel(), f.GetCpuCores(), f.GetAgentVersion(), f.GetIcmpAvailable(), s.clk.Now().Unix())
		return err
	}
}

func (s *Store) UpsertFacts(ctx context.Context, nodeID int64, hash uint64, f *probev1.Facts) error {
	return s.write(ctx, s.factsTx(nodeID, hash, f))
}

// UpsertFactsAsync 供上报路径使用：投递即返回，上报不等待数据库。
func (s *Store) UpsertFactsAsync(nodeID int64, hash uint64, f *probev1.Facts, done func(error)) {
	s.writeAsync(s.factsTx(nodeID, hash, f), done)
}

// FactsHashes 存的是 int64，读回按位转回 uint64；fixed64 的全部取值都能往返。
func (s *Store) FactsHashes(ctx context.Context) (map[int64]uint64, error) {
	rows, err := s.r.QueryContext(ctx, "SELECT node_id, facts_hash FROM node_facts")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[int64]uint64{}
	for rows.Next() {
		var id, h int64
		if err := rows.Scan(&id, &h); err != nil {
			return nil, err
		}
		out[id] = uint64(h)
	}
	return out, rows.Err()
}
```

- [ ] **Step 8: metric.go**

```go
package store

import (
	"context"
	"database/sql"

	"github.com/xjetry/probe/internal/hub/metric"
)

var (
	upsertMinute = metricUpsert("metric_1m")
	selectMinute = metricSelect("metric_1m")
)

// WriteMinuteRows 是 1m 行的唯一写入口。
//
// 冻结不变式：5m 水位之前的 1m 桶不再被写入，否则上级行不再反映下级行。
// 这里比较的是已持久化的水位而不是时钟，所以墙钟被向后拨、待重试列表里的
// 旧桶迟到，都越不过它。被拒绝的行计数返回并记日志，其余行照常写入。
func (s *Store) WriteMinuteRows(ctx context.Context, rows []metric.Row) (int, error) {
	rejected := 0
	err := s.write(ctx, func(tx *sql.Tx) error {
		var upto int64
		if err := tx.QueryRow("SELECT upto_ts FROM rollup_state WHERE level = '5m'").Scan(&upto); err != nil {
			return err
		}
		for _, r := range rows {
			if r.TS < upto {
				rejected++
				s.log.Warn("minute row before rollup watermark dropped", "node", r.NodeID, "ts", r.TS, "watermark", upto)
				continue
			}
			args := append([]any{r.NodeID, r.TS}, bucketArgs(r.Bucket)...)
			if _, err := tx.Exec(upsertMinute, args...); err != nil {
				return err
			}
			if !r.LastSeen.IsZero() {
				if _, err := tx.Exec("UPDATE node SET last_seen_at = ? WHERE id = ?", r.LastSeen.Unix(), r.NodeID); err != nil {
					return err
				}
			}
		}
		return nil
	})
	return rejected, err
}

func (s *Store) ReadMinuteRows(ctx context.Context, nodeID int64, from, to int64) ([]metric.Row, error) {
	rows, err := s.r.QueryContext(ctx, selectMinute, nodeID, from, to)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []metric.Row
	for rows.Next() {
		b := metric.NewBucket()
		var ts int64
		dest := []any{&ts}
		// 扫描目标与 metricColumnNames 同序；整数列先落到 int64 再转回 float64。
		ints := make([]int64, 0, 3*len(metric.Columns))
		for _, c := range metric.Columns {
			if c.Type == metric.Int {
				ints = append(ints, 0, 0)
				dest = append(dest, &ints[len(ints)-2], &ints[len(ints)-1])
				if c.Kind == metric.MeanMax {
					ints = append(ints, 0)
					dest = append(dest, &ints[len(ints)-1])
				}
			} else {
				ints = append(ints, 0)
				dest = append(dest, new(float64), &ints[len(ints)-1])
				if c.Kind == metric.MeanMax {
					dest = append(dest, new(float64))
				}
			}
		}
		if err := rows.Scan(dest...); err != nil {
			return nil, err
		}
		di := 1
		for i, c := range metric.Columns {
			if c.Type == metric.Int {
				b.Sum[i] = float64(*dest[di].(*int64))
				b.N[i] = uint32(*dest[di+1].(*int64))
				di += 2
				if c.Kind == metric.MeanMax {
					b.Max[i] = float64(*dest[di].(*int64))
					di++
				}
			} else {
				b.Sum[i] = *dest[di].(*float64)
				b.N[i] = uint32(*dest[di+1].(*int64))
				di += 2
				if c.Kind == metric.MeanMax {
					b.Max[i] = *dest[di].(*float64)
					di++
				}
			}
		}
		out = append(out, metric.Row{NodeID: nodeID, TS: ts, Bucket: b})
	}
	return out, rows.Err()
}

func (s *Store) SetRollupWatermark(ctx context.Context, level string, uptoTS int64) error {
	return s.write(ctx, func(tx *sql.Tx) error {
		res, err := tx.Exec("UPDATE rollup_state SET upto_ts = ? WHERE level = ?", uptoTS, level)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return ErrNotFound
		}
		return nil
	})
}
```

注意 `ReadMinuteRows` 里 `ints` 切片预分配了足够容量，`append` 不会重新分配，指向其元素的指针在 `Scan` 期间有效；若改动列数计算，这个容量表达式要一起改。

- [ ] **Step 9: 跑，确认绿**

`go test -count=1 ./internal/hub/store/ > /tmp/t5.log 2>&1; echo $?` → 0。若 `query_only` 在只读池上导致 `PRAGMA user_version` 读取失败，改为在写连接上读（测试里的 `s.r` 换 `s.w`）并在 log 里记下原因。

- [ ] **Step 10: 缺陷注入（三处）**

1. `metricUpsert` 里把 `_n = _n + excluded._n` 改成 `_n = excluded._n`，预期 `TestHalfBucketsMergeAdditively` 红（n = 2 而非 3）。改回。
2. `WriteMinuteRows` 里把 `r.TS < upto` 改成 `r.TS <= upto`，预期 `TestWriterRejectsRowsBeforeRollupWatermark` 红（ts 900 也被拒）。改回。
3. `RegisterNode` 里去掉 `remaining <= 0` 判断，预期 `TestRegisterNodeConsumesWindow` 红（第三次注册成功）。改回。
4. `writeAsync` 改回在事务函数内 `defer done(err)`（提交前触发），预期 `TestAsyncCallbackObservesCommittedWrite` 红且报错含 "observed facts hash 77"。改回。
5. `write` 入队后改回 `select { case err := <-req.res: … case <-ctx.Done(): return ctx.Err() }`，预期 `TestCancelDuringTransactionStillReportsCommit` 红（err = context canceled, want nil）。改回。
6. `runWriter` 去掉 ctx 预检，预期 `TestCancelBeforeStartSkipsTransaction` 红（节点被创建）。改回。

- [ ] **Step 11: 提交**

```bash
git add internal/hub/store
git commit -m "hub: SQLite 存储层——单写协程、只读池、schema 与分钟行合并

写只经一个协程，SQLite 的单写者约束在应用内串行化；读走 query_only
连接池。metric_1m 的建表与 ON CONFLICT 加法合并从指标描述表生成。
写协程拒绝 5m 水位之前的 1m 行：比较的是已持久化的水位而非时钟，
墙钟回拨与迟到的重试都越不过它。node.id 用 AUTOINCREMENT，分层备份
恢复后 id 若复用会把别人的历史挂到新节点上且无法分辨。"
```

---

### Task 6: 鉴权——token、映射与锁序、注册窗口、可信代理

**必读**：spec §5.1、§5.2、§5.4（`--trusted-proxies` 那条）。

**Files:**
- Create: `internal/hub/auth/token.go`、`auth.go`、`proxy.go`、`auth_test.go`、`proxy_test.go`

**Interfaces:**
- Consumes: `store.Store`（`CreateNode`、`SetTokenHash`、`DeleteNode`、`TokenHashes`、`SetRegisterWindow`、`ClearRegisterWindow`、`RegisterWindow`、`RegisterNode`、`ErrNotFound`、`ErrNoWindow`、`ErrBadKey`）、`clock.Clock`。
- Produces:
  ```go
  func NewToken() (plain string, hash [32]byte)
  func HashToken(plain string) [32]byte
  var ErrDenied error
  func New(st *store.Store, clk clock.Clock, log *slog.Logger) *Auth
  func (a *Auth) Load(ctx) error
  func (a *Auth) Authenticate(token string) (int64, bool)
  func (a *Auth) CreateNode(ctx, name string) (id int64, plain string, err error)
  func (a *Auth) RotateToken(ctx, id int64) (plain string, err error)
  func (a *Auth) DeleteNode(ctx, id int64) error
  func (a *Auth) OpenWindow(ctx, ttl time.Duration, max int) (plainKey string, err error)
  func (a *Auth) CloseWindow(ctx) error
  func (a *Auth) Window(ctx) (store.Window, bool, error)
  func (a *Auth) Register(ctx, key, name string, from netip.Addr) (id int64, plain string, err error)
  func ClientIP(peerAddr, xff string, trusted []netip.Prefix) netip.Addr
  func ParsePrefixes(list string) ([]netip.Prefix, error)
  ```

- [ ] **Step 1: 失败的测试**

`auth_test.go`：

```go
package auth

import (
	"context"
	"errors"
	"log/slog"
	"net/netip"
	"path/filepath"
	"testing"
	"time"

	"github.com/xjetry/probe/internal/clock"
	"github.com/xjetry/probe/internal/hub/store"
)

func setup(t *testing.T) (*Auth, *store.Store, *clock.Fake) {
	t.Helper()
	clk := clock.NewFake(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	st, err := store.Open(filepath.Join(t.TempDir(), "t.db"), clk, slog.Default())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	a := New(st, clk, slog.Default())
	if err := a.Load(context.Background()); err != nil {
		t.Fatal(err)
	}
	return a, st, clk
}

func TestTokenIs32RandomBytesHex(t *testing.T) {
	p1, h1 := NewToken()
	p2, _ := NewToken()
	if len(p1) != 64 || p1 == p2 {
		t.Fatalf("token %q / %q", p1, p2)
	}
	if HashToken(p1) != h1 {
		t.Fatal("hash must be derived from the plain token")
	}
}

func TestCreateNodeMakesTokenAuthenticate(t *testing.T) {
	a, _, _ := setup(t)
	id, plain, err := a.CreateNode(context.Background(), "a")
	if err != nil {
		t.Fatal(err)
	}
	if got, ok := a.Authenticate(plain); !ok || got != id {
		t.Fatalf("authenticate = %d,%v want %d,true", got, ok, id)
	}
	if _, ok := a.Authenticate("nope"); ok {
		t.Fatal("unknown token authenticated")
	}
}

func TestRotateRemovesOldTokenImmediately(t *testing.T) {
	a, _, _ := setup(t)
	ctx := context.Background()
	id, old, _ := a.CreateNode(ctx, "a")
	fresh, err := a.RotateToken(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := a.Authenticate(old); ok {
		t.Fatal("old token still authenticates after rotation")
	}
	if got, ok := a.Authenticate(fresh); !ok || got != id {
		t.Fatal("new token does not authenticate")
	}
}

func TestFailedStoreWriteLeavesMapUntouched(t *testing.T) {
	a, _, _ := setup(t)
	ctx := context.Background()
	_, plain, _ := a.CreateNode(ctx, "a")
	if _, err := a.RotateToken(ctx, 9999); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("err = %v", err)
	}
	if _, ok := a.Authenticate(plain); !ok {
		t.Fatal("a failed rotation must not disturb existing tokens")
	}
	a.mu.RLock()
	n := len(a.byHash)
	a.mu.RUnlock()
	if n != 1 {
		t.Fatalf("map has %d entries, want 1", n)
	}
}

func TestLoadRebuildsMapFromStore(t *testing.T) {
	a, st, clk := setup(t)
	ctx := context.Background()
	id, plain, _ := a.CreateNode(ctx, "a")
	b := New(st, clk, slog.Default())
	if _, ok := b.Authenticate(plain); ok {
		t.Fatal("fresh Auth must not know tokens before Load")
	}
	if err := b.Load(ctx); err != nil {
		t.Fatal(err)
	}
	if got, ok := b.Authenticate(plain); !ok || got != id {
		t.Fatal("Load did not rebuild the map")
	}
}

func TestRegisterDeniedWithoutWindowAndDoesNotCount(t *testing.T) {
	a, _, _ := setup(t)
	ctx := context.Background()
	from := netip.MustParseAddr("203.0.113.5")
	for i := 0; i < 10; i++ {
		if _, _, err := a.Register(ctx, "anything", "n", from); !errors.Is(err, ErrDenied) {
			t.Fatalf("err = %v", err)
		}
	}
	key, _ := a.OpenWindow(ctx, time.Hour, 1)
	if _, _, err := a.Register(ctx, key, "n", from); err != nil {
		t.Fatalf("closed-window attempts must not have locked the IP: %v", err)
	}
}

func TestRegisterWrongKeyOnOpenWindowLocksIP(t *testing.T) {
	a, _, clk := setup(t)
	ctx := context.Background()
	from := netip.MustParseAddr("203.0.113.5")
	key, _ := a.OpenWindow(ctx, time.Hour, 5)
	for i := 0; i < failLimit; i++ {
		if _, _, err := a.Register(ctx, "wrong", "n", from); !errors.Is(err, ErrDenied) {
			t.Fatalf("err = %v", err)
		}
	}
	if _, _, err := a.Register(ctx, key, "n", from); !errors.Is(err, ErrDenied) {
		t.Fatal("locked IP must be denied even with the right key")
	}
	other := netip.MustParseAddr("203.0.113.6")
	if _, _, err := a.Register(ctx, key, "n", other); err != nil {
		t.Fatalf("lockout is per IP: %v", err)
	}
	clk.Advance(failWindow)
	if _, _, err := a.Register(ctx, key, "n", from); err != nil {
		t.Fatalf("lockout must expire: %v", err)
	}
}

func TestRegisterIssuesWorkingToken(t *testing.T) {
	a, _, _ := setup(t)
	ctx := context.Background()
	key, _ := a.OpenWindow(ctx, time.Hour, 1)
	id, plain, err := a.Register(ctx, key, "n", netip.MustParseAddr("10.0.0.1"))
	if err != nil {
		t.Fatal(err)
	}
	if got, ok := a.Authenticate(plain); !ok || got != id {
		t.Fatal("registered token does not authenticate")
	}
	if _, _, err := a.Register(ctx, key, "m", netip.MustParseAddr("10.0.0.2")); !errors.Is(err, ErrDenied) {
		t.Fatal("window with max 1 must be exhausted")
	}
}

func TestDeleteNodeRevokesToken(t *testing.T) {
	a, _, _ := setup(t)
	ctx := context.Background()
	id, plain, _ := a.CreateNode(ctx, "a")
	if err := a.DeleteNode(ctx, id); err != nil {
		t.Fatal(err)
	}
	if _, ok := a.Authenticate(plain); ok {
		t.Fatal("deleted node's token still authenticates")
	}
}
```

`proxy_test.go`：

```go
package auth

import (
	"net/netip"
	"testing"
)

func TestClientIPIgnoresForwardedHeaderFromUntrustedPeer(t *testing.T) {
	got := ClientIP("198.51.100.7:4321", "203.0.113.9", nil)
	if got != netip.MustParseAddr("198.51.100.7") {
		t.Fatalf("got %v", got)
	}
}

func TestClientIPUsesRightmostUntrustedForwardedAddress(t *testing.T) {
	trusted, _ := ParsePrefixes("10.0.0.0/8, 127.0.0.1")
	got := ClientIP("10.1.2.3:80", "203.0.113.9, 10.9.9.9", trusted)
	if got != netip.MustParseAddr("203.0.113.9") {
		t.Fatalf("got %v", got)
	}
}

func TestClientIPMalformedHeaderFallsBackToPeer(t *testing.T) {
	trusted, _ := ParsePrefixes("10.0.0.0/8")
	got := ClientIP("10.1.2.3:80", "not-an-ip", trusted)
	if got != netip.MustParseAddr("10.1.2.3") {
		t.Fatalf("got %v", got)
	}
}

func TestClientIPUnmapsIPv4InIPv6(t *testing.T) {
	got := ClientIP("[::ffff:198.51.100.7]:1", "", nil)
	if got != netip.MustParseAddr("198.51.100.7") {
		t.Fatalf("got %v", got)
	}
}

func TestParsePrefixesRejectsGarbage(t *testing.T) {
	if _, err := ParsePrefixes("10.0.0.0/8, banana"); err == nil {
		t.Fatal("expected error")
	}
}
```

- [ ] **Step 2: 跑，确认红**

`go test -count=1 ./internal/hub/auth/ > /tmp/t6.log 2>&1; echo $?` → 非 0。

- [ ] **Step 3: token.go**

```go
package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
)

// NewToken 生成节点 token 或注册窗口 key：32 字节随机数，hex 编码。
// token 是高熵随机数，SHA-256 足够——不需要抗字典攻击的慢哈希，而慢哈希撑不住
// 每秒数百次的上报校验。
func NewToken() (string, [32]byte) {
	var b [32]byte
	rand.Read(b[:])
	plain := hex.EncodeToString(b[:])
	return plain, HashToken(plain)
}

func HashToken(plain string) [32]byte { return sha256.Sum256([]byte(plain)) }
```

- [ ] **Step 4: auth.go**

```go
// Package auth 持有节点 token 的内存映射与注册窗口的裁决。
//
// 不变式：byHash 与 node.token_hash 列始终一致。修改顺序固定为：持 mu → 写库并
// 等待成功 → 改映射 → 放锁。写库失败则映射不动；进程在两步之间崩溃则映射在
// 下次启动时经 Load 自库重建。任何绕开 mu 直接改表或改映射的写入都会让两者
// 分叉——probe-hub 的离线子命令直接改表，所以它们要求 hub 重启。
package auth

import (
	"context"
	"errors"
	"log/slog"
	"net/netip"
	"sync"
	"time"

	"github.com/xjetry/probe/internal/clock"
	"github.com/xjetry/probe/internal/hub/store"
)

var ErrDenied = errors.New("registration denied")

const (
	// 同一来源 IP 在窗口开启期间连错 failLimit 次 key，failWindow 内拒绝其注册。
	failLimit  = 5
	failWindow = 15 * time.Minute
)

type Auth struct {
	mu       sync.RWMutex
	store    *store.Store
	clk      clock.Clock
	log      *slog.Logger
	byHash   map[[32]byte]int64
	failures map[netip.Addr]*failure
}

type failure struct {
	count int
	since time.Duration
}

func New(st *store.Store, clk clock.Clock, log *slog.Logger) *Auth {
	return &Auth{store: st, clk: clk, log: log, byHash: map[[32]byte]int64{}, failures: map[netip.Addr]*failure{}}
}

func (a *Auth) Load(ctx context.Context) error {
	m, err := a.store.TokenHashes(ctx)
	if err != nil {
		return err
	}
	a.mu.Lock()
	a.byHash = m
	a.mu.Unlock()
	return nil
}

// Authenticate 只查内存映射，不读库：这是上报路径上唯一的鉴权动作。
func (a *Auth) Authenticate(token string) (int64, bool) {
	h := HashToken(token)
	a.mu.RLock()
	defer a.mu.RUnlock()
	id, ok := a.byHash[h]
	return id, ok
}

func (a *Auth) CreateNode(ctx context.Context, name string) (int64, string, error) {
	plain, h := NewToken()
	a.mu.Lock()
	defer a.mu.Unlock()
	id, err := a.store.CreateNode(ctx, name, h[:])
	if err != nil {
		return 0, "", err
	}
	a.byHash[h] = id
	return id, plain, nil
}

// RotateToken 让旧 hash 立即失效：库写成功后先删旧再加新，中间没有两者都有效的窗口。
func (a *Auth) RotateToken(ctx context.Context, id int64) (string, error) {
	plain, h := NewToken()
	a.mu.Lock()
	defer a.mu.Unlock()
	if err := a.store.SetTokenHash(ctx, id, h[:]); err != nil {
		return "", err
	}
	a.dropLocked(id)
	a.byHash[h] = id
	return plain, nil
}

func (a *Auth) DeleteNode(ctx context.Context, id int64) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if err := a.store.DeleteNode(ctx, id); err != nil {
		return err
	}
	a.dropLocked(id)
	return nil
}

func (a *Auth) dropLocked(id int64) {
	for k, v := range a.byHash {
		if v == id {
			delete(a.byHash, k)
		}
	}
}

func (a *Auth) OpenWindow(ctx context.Context, ttl time.Duration, max int) (string, error) {
	plain, h := NewToken()
	if err := a.store.SetRegisterWindow(ctx, h[:], a.clk.Now().Add(ttl), max); err != nil {
		return "", err
	}
	return plain, nil
}

func (a *Auth) CloseWindow(ctx context.Context) error { return a.store.ClearRegisterWindow(ctx) }

func (a *Auth) Window(ctx context.Context) (store.Window, bool, error) { return a.store.RegisterWindow(ctx) }

// Register 用窗口 key 换取一个新节点的 token。
//
// 窗口关闭与 key 错误对外都是 ErrDenied；失败计数只在窗口开启且 key 错误时累加：
// 窗口关闭时没有可猜的秘密，计数只会误伤与他人共用出口地址的运维者。
// 计数按来源 IP、独立于任何登录失败计数：批量安装时用了过期 key 是配置失误，
// 不是对面板的攻击。
func (a *Auth) Register(ctx context.Context, key, name string, from netip.Addr) (int64, string, error) {
	now := a.clk.Mono()
	a.mu.Lock()
	defer a.mu.Unlock()
	if f := a.failures[from]; f != nil {
		if now-f.since >= failWindow {
			delete(a.failures, from)
		} else if f.count >= failLimit {
			return 0, "", ErrDenied
		}
	}
	keyHash := HashToken(key)
	plain, tokHash := NewToken()
	id, err := a.store.RegisterNode(ctx, keyHash[:], name, tokHash[:])
	switch {
	case errors.Is(err, store.ErrBadKey):
		f := a.failures[from]
		if f == nil {
			f = &failure{since: now}
			a.failures[from] = f
		}
		f.count++
		a.log.Warn("register key mismatch", "from", from, "failures", f.count)
		return 0, "", ErrDenied
	case errors.Is(err, store.ErrNoWindow):
		return 0, "", ErrDenied
	case err != nil:
		return 0, "", err
	}
	a.byHash[tokHash] = id
	delete(a.failures, from)
	return id, plain, nil
}
```

- [ ] **Step 5: proxy.go**

```go
package auth

import (
	"fmt"
	"net/netip"
	"strings"
)

// ClientIP 决定一个请求的来源地址。
//
// 只有 TCP 对端落在可信列表内时，X-Forwarded-For 才被采信；此时从右向左跳过
// 可信地址，第一个不可信的就是客户端。空列表 = 不信任任何转发头、一律用对端
// 地址，是收紧方向。畸形头同样回落到对端地址。hub 不从请求头推断自己是否
// 在反代之后。
func ClientIP(peerAddr, xff string, trusted []netip.Prefix) netip.Addr {
	peer := peerIP(peerAddr)
	if !peer.IsValid() || !inAny(peer, trusted) {
		return peer
	}
	parts := strings.Split(xff, ",")
	for i := len(parts) - 1; i >= 0; i-- {
		s := strings.TrimSpace(parts[i])
		if s == "" {
			continue
		}
		ip, err := netip.ParseAddr(s)
		if err != nil {
			return peer
		}
		ip = ip.Unmap()
		if !inAny(ip, trusted) {
			return ip
		}
	}
	return peer
}

func peerIP(addr string) netip.Addr {
	if ap, err := netip.ParseAddrPort(addr); err == nil {
		return ap.Addr().Unmap()
	}
	if ip, err := netip.ParseAddr(addr); err == nil {
		return ip.Unmap()
	}
	return netip.Addr{}
}

func inAny(ip netip.Addr, prefixes []netip.Prefix) bool {
	for _, p := range prefixes {
		if p.Contains(ip) {
			return true
		}
	}
	return false
}

// ParsePrefixes 解析逗号分隔的 CIDR 列表；裸地址按单主机前缀处理。
func ParsePrefixes(list string) ([]netip.Prefix, error) {
	var out []netip.Prefix
	for _, s := range strings.Split(list, ",") {
		s = strings.TrimSpace(s)
		if s == "" {
			continue
		}
		if p, err := netip.ParsePrefix(s); err == nil {
			out = append(out, p)
			continue
		}
		ip, err := netip.ParseAddr(s)
		if err != nil {
			return nil, fmt.Errorf("trusted proxy %q: not a CIDR or address", s)
		}
		out = append(out, netip.PrefixFrom(ip, ip.BitLen()))
	}
	return out, nil
}
```

- [ ] **Step 6: 跑，确认绿**

`go test -count=1 ./internal/hub/auth/ > /tmp/t6.log 2>&1; echo $?` → 0。

- [ ] **Step 7: 缺陷注入（三处）**

1. `RotateToken` 里把 `a.byHash[h] = id` 移到 `SetTokenHash` 之前（先改映射再写库），预期 `TestFailedStoreWriteLeavesMapUntouched` 红（map 变成 2 项）。改回。
2. `Register` 里对 `ErrNoWindow` 也 `f.count++`，预期 `TestRegisterDeniedWithoutWindowAndDoesNotCount` 红。改回。
3. `ClientIP` 开头去掉 `!inAny(peer, trusted)` 判断，预期 `TestClientIPIgnoresForwardedHeaderFromUntrustedPeer` 红。改回。

- [ ] **Step 8: 提交**

```bash
git add internal/hub/auth
git commit -m "hub: 节点 token 映射、注册窗口裁决与可信代理

token 映射与 node.token_hash 的一致性由固定的修改顺序承载：持锁、写库
等成功、再改映射。上报路径只查映射不读库。注册失败只在窗口开启且 key
错误时计数——窗口关闭时没有可猜的秘密。X-Forwarded-For 只在 TCP 对端
落在显式可信列表内时采信，空列表不信任何转发头。"
```

---

### Task 7: AgentService 实现——校验、鉴权拦截器、限速、分钟刷出

**必读**：spec §3.2、§4.2、§4.3、§4.4（间隔 = TTL/3）、§5.1（限速与 64 KiB）、§6.1、§11（前三行）、§12 前四条。

**Files:**
- Create: `internal/hub/ingest/service.go`、`validate.go`、`limiter.go`、`flush.go`、`ingest_test.go`

**Interfaces:**
- Consumes: `live.Live`、`store.Store`、`auth.Auth`、`auth.ClientIP`、`metric.Row`、`probev1connect.*`。
- Produces:
  ```go
  type Config struct { TTL time.Duration; TrustedProxies []netip.Prefix }
  func New(cfg Config, l *live.Live, st *store.Store, a *auth.Auth, clk clock.Clock, log *slog.Logger) *Service
  func (s *Service) Load(ctx) error
  func (s *Service) Interval() time.Duration
  func (s *Service) Handler() (path string, h http.Handler)
  func (s *Service) Flush(ctx, all bool)
  func (s *Service) RunFlusher(ctx)   // 阻塞到 ctx 结束，结束前 Drain 一次
  ```

- [ ] **Step 1: 失败的测试**

```go
package ingest

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"math"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"
	probev1 "github.com/xjetry/probe/gen/probe/v1"
	"github.com/xjetry/probe/gen/probe/v1/probev1connect"
	"github.com/xjetry/probe/internal/clock"
	"github.com/xjetry/probe/internal/hub/auth"
	"github.com/xjetry/probe/internal/hub/live"
	"github.com/xjetry/probe/internal/hub/metric"
	"github.com/xjetry/probe/internal/hub/store"
	"google.golang.org/protobuf/proto"
)

type hub struct {
	svc    *Service
	srv    *httptest.Server
	client probev1connect.AgentServiceClient
	clk    *clock.Fake
	store  *store.Store
	auth   *auth.Auth
	live   *live.Live
}

func newHub(t *testing.T) *hub {
	t.Helper()
	clk := clock.NewFake(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	st, err := store.Open(filepath.Join(t.TempDir(), "t.db"), clk, slog.Default())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	a := auth.New(st, clk, slog.Default())
	l := live.New(clk, 30*time.Second)
	svc := New(Config{TTL: 30 * time.Second}, l, st, a, clk, slog.Default())
	if err := svc.Load(context.Background()); err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	mux.Handle(svc.Handler())
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return &hub{svc: svc, srv: srv, client: probev1connect.NewAgentServiceClient(srv.Client(), srv.URL), clk: clk, store: st, auth: a, live: l}
}

func (h *hub) node(t *testing.T) (int64, string) {
	t.Helper()
	id, tok, err := h.auth.CreateNode(context.Background(), "n")
	if err != nil {
		t.Fatal(err)
	}
	return id, tok
}

func report(tok string, m *probev1.Metrics) *connect.Request[probev1.ReportRequest] {
	req := connect.NewRequest(&probev1.ReportRequest{Metrics: m})
	if tok != "" {
		req.Header().Set("Authorization", "Bearer "+tok)
	}
	return req
}

func TestReportWithoutTokenIsUnauthenticated(t *testing.T) {
	h := newHub(t)
	_, err := h.client.Report(context.Background(), report("", &probev1.Metrics{}))
	if connect.CodeOf(err) != connect.CodeUnauthenticated {
		t.Fatalf("err = %v", err)
	}
	_, err = h.client.Report(context.Background(), report("bogus", &probev1.Metrics{}))
	if connect.CodeOf(err) != connect.CodeUnauthenticated {
		t.Fatalf("err = %v", err)
	}
}

// 从服务描述符枚举全部 RPC，逐个无凭据调用：新增方法自动入测，不可能漏掉鉴权。
func TestEveryProcedureRejectsAnonymousCalls(t *testing.T) {
	h := newHub(t)
	services := probev1.File_probe_v1_agent_proto.Services()
	count := 0
	for i := 0; i < services.Len(); i++ {
		svc := services.Get(i)
		methods := svc.Methods()
		for j := 0; j < methods.Len(); j++ {
			count++
			path := "/" + string(svc.FullName()) + "/" + string(methods.Get(j).Name())
			resp, err := http.Post(h.srv.URL+path, "application/json", strings.NewReader("{}"))
			if err != nil {
				t.Fatal(err)
			}
			var body struct {
				Code string `json:"code"`
			}
			json.NewDecoder(resp.Body).Decode(&body)
			resp.Body.Close()
			if resp.StatusCode != http.StatusUnauthorized || body.Code != "unauthenticated" {
				t.Fatalf("%s: status %d code %q, want 401 unauthenticated", path, resp.StatusCode, body.Code)
			}
		}
	}
	if count == 0 {
		t.Fatal("enumerated no procedures; the descriptor lookup is wrong")
	}
}

func TestReportUpdatesLiveAndReturnsInterval(t *testing.T) {
	h := newHub(t)
	id, tok := h.node(t)
	resp, err := h.client.Report(context.Background(), report(tok, &probev1.Metrics{CpuPct: proto.Float64(12)}))
	if err != nil {
		t.Fatal(err)
	}
	if resp.Msg.ReportIntervalMs != 10_000 {
		t.Fatalf("interval = %d ms, want TTL/3 = 10000", resp.Msg.ReportIntervalMs)
	}
	e, ok := h.live.Get(id)
	if !ok || !e.Online || e.Metrics.GetCpuPct() != 12 {
		t.Fatalf("live = %+v ok=%v", e, ok)
	}
}

func TestInvalidMetricsRejectedWholeWithoutSideEffect(t *testing.T) {
	h := newHub(t)
	id, tok := h.node(t)
	cases := map[string]*probev1.Metrics{
		"nan":          {CpuPct: proto.Float64(math.NaN())},
		"inf":          {Load1: proto.Float64(math.Inf(1)), Load5: proto.Float64(0), Load15: proto.Float64(0)},
		"negative":     {CpuPct: proto.Float64(-1)},
		"pct over 100": {CpuPct: proto.Float64(100.5)},
		"partial load": {Load1: proto.Float64(1)},
		"nil metrics":  nil,
	}
	for name, m := range cases {
		_, err := h.client.Report(context.Background(), report(tok, m))
		if connect.CodeOf(err) != connect.CodeInvalidArgument {
			t.Fatalf("%s: err = %v, want InvalidArgument", name, err)
		}
	}
	if _, ok := h.live.Get(id); ok {
		t.Fatal("a rejected report must leave live untouched")
	}
}

func TestFactsAreStoredAndReconciledByHash(t *testing.T) {
	h := newHub(t)
	id, tok := h.node(t)
	ctx := context.Background()
	req := report(tok, &probev1.Metrics{})
	req.Msg.Facts = &probev1.Facts{Hostname: "box", CpuCores: 2}
	req.Msg.FactsHash = 41
	resp, err := h.client.Report(ctx, req)
	if err != nil || resp.Msg.WantFacts {
		t.Fatalf("resp = %+v err = %v", resp, err)
	}
	waitFor(t, func() bool { m, _ := h.store.FactsHashes(ctx); return m[id] == 41 })

	h.clk.Advance(10 * time.Second)
	req = report(tok, &probev1.Metrics{})
	req.Msg.FactsHash = 41
	resp, _ = h.client.Report(ctx, req)
	if resp.Msg.WantFacts {
		t.Fatal("same hash must not ask for facts")
	}
	h.clk.Advance(10 * time.Second)
	req = report(tok, &probev1.Metrics{})
	req.Msg.FactsHash = 42
	resp, _ = h.client.Report(ctx, req)
	if !resp.Msg.WantFacts {
		t.Fatal("changed hash must ask for facts")
	}
}

func TestFactsStringsAreSanitized(t *testing.T) {
	h := newHub(t)
	id, tok := h.node(t)
	ctx := context.Background()
	req := report(tok, &probev1.Metrics{})
	req.Msg.Facts = &probev1.Facts{Hostname: "a\x00b\x1fc\x7fd", Os: strings.Repeat("x", 1000)}
	req.Msg.FactsHash = 1
	if _, err := h.client.Report(ctx, req); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { m, _ := h.store.FactsHashes(ctx); return m[id] == 1 })
	var hostname, os string
	if err := h.store.QueryFacts(ctx, id, &hostname, &os); err != nil {
		t.Fatal(err)
	}
	if hostname != "abcd" || len(os) != maxFactString {
		t.Fatalf("hostname %q os len %d", hostname, len(os))
	}
}

func TestRateLimitIsTwiceTheReportRate(t *testing.T) {
	h := newHub(t)
	_, tok := h.node(t)
	ctx := context.Background()
	var last error
	for i := 0; i < burst+1; i++ {
		_, last = h.client.Report(ctx, report(tok, &probev1.Metrics{}))
	}
	if connect.CodeOf(last) != connect.CodeResourceExhausted {
		t.Fatalf("burst+1 immediate reports: err = %v, want ResourceExhausted", last)
	}
	h.clk.Advance(h.svc.Interval() / 2) // 2× 速率 = 每半个间隔补一个令牌
	if _, err := h.client.Report(ctx, report(tok, &probev1.Metrics{})); err != nil {
		t.Fatalf("after refill: %v", err)
	}
}

func TestOversizedBodyIsRejected(t *testing.T) {
	h := newHub(t)
	id, tok := h.node(t)
	req := report(tok, &probev1.Metrics{})
	req.Msg.Facts = &probev1.Facts{Os: strings.Repeat("x", 70*1024)}
	_, err := h.client.Report(context.Background(), req)
	if err == nil {
		t.Fatal("70 KiB body must be rejected")
	}
	if _, ok := h.live.Get(id); ok {
		t.Fatal("rejected body must leave live untouched")
	}
}

func TestRegisterThenReport(t *testing.T) {
	h := newHub(t)
	ctx := context.Background()
	_, err := h.client.Register(ctx, connect.NewRequest(&probev1.RegisterRequest{Key: "nope", Name: "x"}))
	if connect.CodeOf(err) != connect.CodeUnauthenticated {
		t.Fatalf("closed window: err = %v", err)
	}
	key, _ := h.auth.OpenWindow(ctx, time.Hour, 1)
	resp, err := h.client.Register(ctx, connect.NewRequest(&probev1.RegisterRequest{Key: key, Name: "x"}))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.client.Report(ctx, report(resp.Msg.Token, &probev1.Metrics{})); err != nil {
		t.Fatalf("token from Register must work: %v", err)
	}
	if !h.live.Online(resp.Msg.NodeId) {
		t.Fatal("node not online after report")
	}
}

func TestFlushWritesClosedBucketsOnly(t *testing.T) {
	h := newHub(t)
	id, tok := h.node(t)
	ctx := context.Background()
	h.client.Report(ctx, report(tok, &probev1.Metrics{CpuPct: proto.Float64(10)}))
	h.svc.Flush(ctx, false)
	if rows, _ := h.store.ReadMinuteRows(ctx, id, 0, math.MaxInt64); len(rows) != 0 {
		t.Fatalf("open bucket was written: %+v", rows)
	}
	h.clk.Advance(61 * time.Second)
	h.svc.Flush(ctx, false)
	rows, _ := h.store.ReadMinuteRows(ctx, id, 0, math.MaxInt64)
	if len(rows) != 1 {
		t.Fatalf("rows = %+v, want 1", rows)
	}
	if mean, _ := rows[0].Bucket.Mean(0); mean != 10 {
		t.Fatalf("mean = %v", mean)
	}
}

type failingWriter struct {
	*store.Store
	fail bool
}

func (f *failingWriter) WriteMinuteRows(ctx context.Context, rows []metric.Row) (int, error) {
	if f.fail {
		return 0, errors.New("disk on fire")
	}
	return f.Store.WriteMinuteRows(ctx, rows)
}

func TestFlushRetriesFailedBatchesLater(t *testing.T) {
	h := newHub(t)
	id, tok := h.node(t)
	ctx := context.Background()
	fw := &failingWriter{Store: h.store, fail: true}
	h.svc.writer = fw
	h.client.Report(ctx, report(tok, &probev1.Metrics{CpuPct: proto.Float64(10)}))
	h.clk.Advance(61 * time.Second)
	h.svc.Flush(ctx, false)
	if rows, _ := h.store.ReadMinuteRows(ctx, id, 0, math.MaxInt64); len(rows) != 0 {
		t.Fatal("write should have failed")
	}
	fw.fail = false
	h.svc.Flush(ctx, false)
	if rows, _ := h.store.ReadMinuteRows(ctx, id, 0, math.MaxInt64); len(rows) != 1 {
		t.Fatalf("retried batch not written: %+v", rows)
	}
}

func TestDrainOnShutdownWritesOpenBucket(t *testing.T) {
	h := newHub(t)
	id, tok := h.node(t)
	ctx := context.Background()
	h.client.Report(ctx, report(tok, &probev1.Metrics{CpuPct: proto.Float64(10)}))
	h.svc.Flush(ctx, true)
	if rows, _ := h.store.ReadMinuteRows(ctx, id, 0, math.MaxInt64); len(rows) != 1 {
		t.Fatalf("rows = %+v", rows)
	}
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("condition not met in time")
}
```

这份测试用到 `store.QueryFacts(ctx, id, &hostname, &os)`——在 `internal/hub/store/facts.go` 里补一个只读查询：

```go
func (s *Store) QueryFacts(ctx context.Context, nodeID int64, hostname, os *string) error {
	return s.r.QueryRowContext(ctx, "SELECT hostname, os FROM node_facts WHERE node_id = ?", nodeID).Scan(hostname, os)
}
```

- [ ] **Step 2: 跑，确认红**

`go test -count=1 ./internal/hub/ingest/ > /tmp/t7.log 2>&1; echo $?` → 非 0。

- [ ] **Step 3: validate.go**

```go
package ingest

import (
	"errors"
	"fmt"
	"math"
	"strings"
	"unicode/utf8"

	probev1 "github.com/xjetry/probe/gen/probe/v1"
)

// validateMetrics 对整条上报做准入：任何一个字段非法就整条拒绝，live 不变。
// 无符号整数字段没有非法取值；需要判定的只有浮点与 load 三元组的形状。
func validateMetrics(m *probev1.Metrics) error {
	if m == nil {
		return errors.New("metrics: required")
	}
	floats := []struct {
		name string
		v    *float64
	}{{"cpu_pct", m.CpuPct}, {"load1", m.Load1}, {"load5", m.Load5}, {"load15", m.Load15}}
	for _, f := range floats {
		if f.v != nil && (math.IsNaN(*f.v) || math.IsInf(*f.v, 0) || *f.v < 0) {
			return fmt.Errorf("%s: must be a finite non-negative number", f.name)
		}
	}
	if m.CpuPct != nil && *m.CpuPct > 100 {
		return errors.New("cpu_pct: must not exceed 100")
	}
	set := 0
	for _, v := range []*float64{m.Load1, m.Load5, m.Load15} {
		if v != nil {
			set++
		}
	}
	if set != 0 && set != 3 {
		return errors.New("load: load1, load5 and load15 must be given together")
	}
	return nil
}

// maxFactString 是 Facts 里每个字符串的字节上限；其中数个字段会出现在匿名公开页。
const maxFactString = 256

func sanitizeString(s string) string {
	var b strings.Builder
	for _, r := range s {
		if r < 0x20 || r == 0x7f || r == utf8.RuneError {
			continue
		}
		if b.Len()+utf8.RuneLen(r) > maxFactString {
			break
		}
		b.WriteRune(r)
	}
	return b.String()
}

func sanitizeFacts(f *probev1.Facts) {
	f.Hostname = sanitizeString(f.Hostname)
	f.Os = sanitizeString(f.Os)
	f.Kernel = sanitizeString(f.Kernel)
	f.Arch = sanitizeString(f.Arch)
	f.Virtualization = sanitizeString(f.Virtualization)
	f.CpuModel = sanitizeString(f.CpuModel)
	f.AgentVersion = sanitizeString(f.AgentVersion)
}
```

- [ ] **Step 4: limiter.go**

```go
package ingest

import (
	"sync"
	"time"
)

// burst 是令牌桶容量：允许上报间隔的抖动与一次立即重试，再多就是异常。
const burst = 3

// limiter 按节点限速：补充速率是下发间隔对应速率的 2 倍。用单调钟计时。
type limiter struct {
	mu    sync.Mutex
	nodes map[int64]*tokenBucket
}

type tokenBucket struct {
	tokens float64
	last   time.Duration
}

func newLimiter() *limiter { return &limiter{nodes: map[int64]*tokenBucket{}} }

func (l *limiter) allow(id int64, now, interval time.Duration) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	b := l.nodes[id]
	if b == nil {
		b = &tokenBucket{tokens: burst, last: now}
		l.nodes[id] = b
	}
	b.tokens = min(burst, b.tokens+float64(now-b.last)/float64(interval)*2)
	b.last = now
	if b.tokens < 1 {
		return false
	}
	b.tokens--
	return true
}
```

- [ ] **Step 5: service.go**

```go
// Package ingest 实现 AgentService：校验 → live、facts 落盘、分钟刷出。
//
// 上报路径只碰内存：鉴权查 auth 的映射，状态写进 live，facts 投递给写协程
// 即返回。落盘由 RunFlusher 的分钟定时器驱动。
package ingest

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/netip"
	"strings"
	"sync"
	"time"

	"connectrpc.com/connect"

	probev1 "github.com/xjetry/probe/gen/probe/v1"
	"github.com/xjetry/probe/gen/probe/v1/probev1connect"
	"github.com/xjetry/probe/internal/clock"
	"github.com/xjetry/probe/internal/hub/auth"
	"github.com/xjetry/probe/internal/hub/live"
	"github.com/xjetry/probe/internal/hub/metric"
	"github.com/xjetry/probe/internal/hub/store"
)

// maxBody 限制 AgentService 的请求体：一条上报远小于此，超出的只可能是滥用。
const maxBody = 64 << 10

type Config struct {
	TTL            time.Duration
	TrustedProxies []netip.Prefix
}

type minuteWriter interface {
	WriteMinuteRows(ctx context.Context, rows []metric.Row) (int, error)
}

type Service struct {
	cfg    Config
	live   *live.Live
	store  *store.Store
	writer minuteWriter
	auth   *auth.Auth
	clk    clock.Clock
	log    *slog.Logger
	limit  *limiter

	mu sync.Mutex
	// factsHash 是 hub 已持久化的各节点 facts 摘要；只在写库成功后更新，
	// 写失败则保持旧值，下一次上报会因不一致再次要求 facts。
	factsHash map[int64]uint64

	pendingMu sync.Mutex
	pending   [][]metric.Row
}

func New(cfg Config, l *live.Live, st *store.Store, a *auth.Auth, clk clock.Clock, log *slog.Logger) *Service {
	return &Service{cfg: cfg, live: l, store: st, writer: st, auth: a, clk: clk, log: log, limit: newLimiter(), factsHash: map[int64]uint64{}}
}

func (s *Service) Load(ctx context.Context) error {
	m, err := s.store.FactsHashes(ctx)
	if err != nil {
		return err
	}
	s.mu.Lock()
	s.factsHash = m
	s.mu.Unlock()
	return nil
}

// Interval 是下发给 agent 的上报间隔：TTL 内三次上报机会，容得下两次连续失败。
func (s *Service) Interval() time.Duration { return s.cfg.TTL / 3 }

func (s *Service) Handler() (string, http.Handler) {
	return probev1connect.NewAgentServiceHandler(s,
		connect.WithInterceptors(s.authInterceptor()),
		connect.WithReadMaxBytes(maxBody))
}

type nodeKey struct{}

func unauthenticated() error {
	return connect.NewError(connect.CodeUnauthenticated, errors.New("unauthenticated"))
}

// authInterceptor 在挂载点上裁决每个方法的凭据来源。没有在这里显式列出的
// 方法一律拒绝：新增方法不可能因为忘了加检查而被放行。
func (s *Service) authInterceptor() connect.Interceptor {
	return connect.UnaryInterceptorFunc(func(next connect.UnaryFunc) connect.UnaryFunc {
		return func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
			switch req.Spec().Procedure {
			case probev1connect.AgentServiceRegisterProcedure:
				// 凭据是请求体里的窗口 key，由 Register 裁决。
				return next(ctx, req)
			case probev1connect.AgentServiceReportProcedure:
				tok, ok := strings.CutPrefix(req.Header().Get("Authorization"), "Bearer ")
				if !ok {
					return nil, unauthenticated()
				}
				id, ok := s.auth.Authenticate(tok)
				if !ok {
					return nil, unauthenticated()
				}
				return next(context.WithValue(ctx, nodeKey{}, id), req)
			}
			return nil, unauthenticated()
		}
	})
}

func (s *Service) Register(ctx context.Context, req *connect.Request[probev1.RegisterRequest]) (*connect.Response[probev1.RegisterResponse], error) {
	from := auth.ClientIP(req.Peer().Addr, req.Header().Get("X-Forwarded-For"), s.cfg.TrustedProxies)
	name := sanitizeString(strings.TrimSpace(req.Msg.GetName()))
	if name == "" {
		name = "node"
	}
	id, tok, err := s.auth.Register(ctx, req.Msg.GetKey(), name, from)
	if errors.Is(err, auth.ErrDenied) {
		return nil, unauthenticated()
	}
	if err != nil {
		s.log.Error("register failed", "err", err)
		return nil, connect.NewError(connect.CodeInternal, errors.New("registration failed"))
	}
	s.log.Info("node registered", "node", id, "name", name, "from", from)
	return connect.NewResponse(&probev1.RegisterResponse{NodeId: id, Token: tok}), nil
}

func (s *Service) Report(ctx context.Context, req *connect.Request[probev1.ReportRequest]) (*connect.Response[probev1.ReportResponse], error) {
	id := ctx.Value(nodeKey{}).(int64)
	if !s.limit.allow(id, s.clk.Mono(), s.Interval()) {
		return nil, connect.NewError(connect.CodeResourceExhausted, errors.New("reporting faster than twice the assigned interval"))
	}
	m := req.Msg.GetMetrics()
	if err := validateMetrics(m); err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}
	s.live.Observe(id, m)
	want := s.reconcileFacts(id, req.Msg.GetFactsHash(), req.Msg.GetFacts())
	return connect.NewResponse(&probev1.ReportResponse{
		ReportIntervalMs: uint32(s.Interval() / time.Millisecond),
		WantFacts:        want,
	}), nil
}

// reconcileFacts 是电平触发的对账：agent 每次带摘要，hub 只在不一致时索要。
func (s *Service) reconcileFacts(id int64, hash uint64, f *probev1.Facts) bool {
	if f != nil {
		sanitizeFacts(f)
		s.store.UpsertFactsAsync(id, hash, f, func(err error) {
			if err != nil {
				s.log.Error("facts write failed", "node", id, "err", err)
				return
			}
			s.mu.Lock()
			s.factsHash[id] = hash
			s.mu.Unlock()
		})
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	known, ok := s.factsHash[id]
	return !ok || known != hash
}
```

- [ ] **Step 6: flush.go**

```go
package ingest

import (
	"context"
	"time"

	"github.com/xjetry/probe/internal/hub/metric"
)

// maxPendingBatches 限制待重试列表：数据库长时间不可用时内存不无限增长，
// 满了丢最旧的一批并记日志。
const maxPendingBatches = 64

// Flush 把已闭合的分钟桶（all 为 true 时包括仍开着的）交给写协程。
// 写失败的批次留在待重试列表，下次先重试它们；被写协程按水位拒绝的行
// 不算失败——它们已经不可能再被正确并入。
func (s *Service) Flush(ctx context.Context, all bool) {
	var rows []metric.Row
	if all {
		rows = s.live.Drain()
	} else {
		rows = s.live.Flush()
	}
	s.pendingMu.Lock()
	defer s.pendingMu.Unlock()
	if len(rows) > 0 {
		s.pending = append(s.pending, rows)
	}
	for len(s.pending) > maxPendingBatches {
		s.log.Error("dropping oldest unflushed minute batch", "rows", len(s.pending[0]))
		s.pending = s.pending[1:]
	}
	for len(s.pending) > 0 {
		batch := s.pending[0]
		rejected, err := s.writer.WriteMinuteRows(ctx, batch)
		if err != nil {
			s.log.Error("minute flush failed, keeping batch for retry", "err", err, "batches", len(s.pending))
			return
		}
		if rejected > 0 {
			s.log.Warn("minute rows dropped at rollup watermark", "rejected", rejected)
		}
		s.pending = s.pending[1:]
	}
}

// RunFlusher 在每个分钟边界后半秒刷出一次，ctx 结束时把全部桶刷出后返回。
// 半秒的偏移保证按墙钟分钟闭合的桶在刷出时确实已经闭合。
func (s *Service) RunFlusher(ctx context.Context) {
	for {
		wall := s.clk.Now()
		next := wall.Truncate(time.Minute).Add(time.Minute + 500*time.Millisecond)
		timer := time.NewTimer(next.Sub(wall))
		select {
		case <-ctx.Done():
			timer.Stop()
			s.Flush(context.Background(), true)
			return
		case <-timer.C:
			s.Flush(ctx, false)
		}
	}
}
```

- [ ] **Step 7: 跑，确认绿**

`go test -count=1 ./internal/hub/ingest/ > /tmp/t7.log 2>&1; echo $?` → 0。

若 `TestOversizedBodyIsRejected` 里客户端拿到的 code 不是预期，把实际 code 记进该测试的注释——这是对 connect-go 该版本行为的实验结论（§13 第 7 项同类）。

- [ ] **Step 8: 缺陷注入（三处）**

1. `authInterceptor` 末尾的 `return nil, unauthenticated()` 改成 `return next(ctx, req)`（默认放行），预期 `TestEveryProcedureRejectsAnonymousCalls` **仍然绿**——因为两个方法都被显式列出了。这说明这条测试钉的是"每个方法都有鉴权"，不是"默认分支拒绝"。再把 `case ...ReportProcedure:` 整段删掉，预期该测试红且报错指向 `/probe.v1.AgentService/Report`。两处都改回。
2. `Report` 里把 `validateMetrics` 调用移到 `s.live.Observe` 之后，预期 `TestInvalidMetricsRejectedWholeWithoutSideEffect` 红（live 被改）。改回。
3. `reconcileFacts` 里在 `UpsertFactsAsync` 之前就 `s.factsHash[id] = hash`，然后在测试里让写失败——最省事的注入是把 `factsTx` 的 SQL 改坏（列名拼错）；预期 `TestFactsAreStoredAndReconciledByHash` 在 `waitFor` 处超时。两处都改回。

- [ ] **Step 9: 提交**

```bash
git add internal/hub/ingest internal/hub/store/facts.go
git commit -m "hub: AgentService——鉴权拦截器、准入校验、限速与分钟刷出

鉴权在挂载点裁决，未显式列出凭据来源的方法一律拒绝。非法上报整条
拒绝且 live 不变；facts 摘要只在写库成功后更新，写失败则下一次上报
自然再索要。分钟桶写失败留在有界的待重试列表，被水位拒绝的行不重试。"
```

---

### Task 8: `probe-hub` 命令——serve、node、window、stats

**必读**：spec §4.4（`PROBE_OFFLINE_AFTER`）、§5.4、§6.1（退出时全刷）。

**Files:**
- Create: `cmd/hub/main.go`、`config.go`、`config_test.go`、`serve.go`、`node.go`、`window.go`、`stats.go`
- Modify: `internal/hub/store/node.go`（追加 `Counts`）

**Interfaces:**
- Consumes: 前面全部包。
- Produces: 二进制 `probe-hub`，子命令：
  - `serve --db probe.db --listen 127.0.0.1:8080 --trusted-proxies "" --site-url ""`，环境变量 `PROBE_OFFLINE_AFTER`
  - `node create --name X`、`node list`、`node delete --id N`、`node rotate-token --id N`
  - `window open --ttl 1h --max 10`、`window close`、`window show`
  - `stats`（各表行数，供 e2e 与运维核对）
  - 全部子命令共用 `--db`。

- [ ] **Step 1: 失败的测试（config_test.go）**

```go
package main

import (
	"testing"
	"time"
)

func TestParseTTL(t *testing.T) {
	cases := []struct {
		in   string
		want time.Duration
		err  bool
	}{
		{"", 30 * time.Second, false},
		{"45s", 45 * time.Second, false},
		{"10s", 10 * time.Second, false},
		{"9s", 0, true},
		{"banana", 0, true},
	}
	for _, c := range cases {
		got, err := parseTTL(c.in)
		if (err != nil) != c.err || got != c.want {
			t.Fatalf("parseTTL(%q) = %v, %v; want %v, err=%v", c.in, got, err, c.want, c.err)
		}
	}
}

func TestIsLoopback(t *testing.T) {
	if !isLoopback("127.0.0.1:8080") || !isLoopback("[::1]:8080") || !isLoopback("localhost:8080") {
		t.Fatal("loopback addresses misclassified")
	}
	if isLoopback("0.0.0.0:8080") || isLoopback(":8080") || isLoopback("10.0.0.1:8080") {
		t.Fatal("non-loopback addresses misclassified")
	}
}
```

- [ ] **Step 2: 跑，确认红** — `go test -count=1 ./cmd/hub/ > /tmp/t8.log 2>&1; echo $?` → 非 0。

- [ ] **Step 3: config.go**

```go
package main

import (
	"fmt"
	"net"
	"time"
)

const (
	defaultTTL = 30 * time.Second
	minTTL     = 10 * time.Second
)

// parseTTL 解析 PROBE_OFFLINE_AFTER。TTL 是离线发现延迟的上界，也是这条链上
// 唯一被直接配置的量：上报间隔、退避上限、告警宽限期下限都由它反推。
func parseTTL(s string) (time.Duration, error) {
	if s == "" {
		return defaultTTL, nil
	}
	d, err := time.ParseDuration(s)
	if err != nil {
		return 0, fmt.Errorf("PROBE_OFFLINE_AFTER: %w", err)
	}
	if d < minTTL {
		return 0, fmt.Errorf("PROBE_OFFLINE_AFTER: %v is below the minimum %v", d, minTTL)
	}
	return d, nil
}

func isLoopback(listen string) bool {
	host, _, err := net.SplitHostPort(listen)
	if err != nil || host == "" {
		return false
	}
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}
```

- [ ] **Step 4: main.go**

```go
// probe-hub：serve 起服务；其余子命令直接操作数据库，供 serve 之外的运维动作。
//
// 直接改表的子命令绕过了 auth 的内存映射：token 映射在 hub 启动时自库重建，
// 所以 node delete / rotate-token 在 hub 运行期间需要重启才生效。
package main

import (
	"fmt"
	"os"
)

var version = "dev"

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	var err error
	switch os.Args[1] {
	case "serve":
		err = runServe(os.Args[2:])
	case "node":
		err = runNode(os.Args[2:])
	case "window":
		err = runWindow(os.Args[2:])
	case "stats":
		err = runStats(os.Args[2:])
	case "version":
		fmt.Println(version)
	default:
		usage()
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, `usage: probe-hub <command> [flags]

commands:
  serve                     start the hub
  node create|list|delete|rotate-token
  window open|close|show    manage the registration window
  stats                     row counts per table
  version`)
}
```

- [ ] **Step 5: serve.go**

```go
package main

import (
	"context"
	"errors"
	"flag"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/xjetry/probe/internal/clock"
	"github.com/xjetry/probe/internal/hub/auth"
	"github.com/xjetry/probe/internal/hub/ingest"
	"github.com/xjetry/probe/internal/hub/live"
	"github.com/xjetry/probe/internal/hub/store"
)

func runServe(args []string) error {
	fs := flag.NewFlagSet("serve", flag.ContinueOnError)
	db := fs.String("db", "probe.db", "SQLite database path")
	listen := fs.String("listen", "127.0.0.1:8080", "listen address")
	proxies := fs.String("trusted-proxies", "", "comma-separated CIDRs whose X-Forwarded-For is trusted; empty trusts none")
	fs.String("site-url", "", "public URL of this hub, used in generated install commands")
	if err := fs.Parse(args); err != nil {
		return err
	}
	ttl, err := parseTTL(os.Getenv("PROBE_OFFLINE_AFTER"))
	if err != nil {
		return err
	}
	trusted, err := auth.ParsePrefixes(*proxies)
	if err != nil {
		return err
	}
	log := slog.New(slog.NewTextHandler(os.Stderr, nil))
	if !isLoopback(*listen) {
		log.Warn("listening on a non-loopback address: anyone reaching it directly can forge forwarded headers", "listen", *listen)
	}

	clk := clock.Real()
	st, err := store.Open(*db, clk, log)
	if err != nil {
		return err
	}
	a := auth.New(st, clk, log)
	l := live.New(clk, ttl)
	svc := ingest.New(ingest.Config{TTL: ttl, TrustedProxies: trusted}, l, st, a, clk, log)
	ctx := context.Background()
	if err := errors.Join(a.Load(ctx), svc.Load(ctx)); err != nil {
		st.Close()
		return err
	}

	mux := http.NewServeMux()
	mux.Handle(svc.Handler())
	srv := &http.Server{Addr: *listen, Handler: mux, ReadHeaderTimeout: 10 * time.Second}

	flushCtx, stopFlusher := context.WithCancel(ctx)
	flusherDone := make(chan struct{})
	go func() {
		defer close(flusherDone)
		svc.RunFlusher(flushCtx)
	}()

	errCh := make(chan error, 1)
	go func() { errCh <- srv.ListenAndServe() }()
	log.Info("hub listening", "listen", *listen, "ttl", ttl, "interval", svc.Interval(), "version", version)

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
	select {
	case s := <-sig:
		log.Info("shutting down", "signal", s)
	case err := <-errCh:
		stopFlusher()
		<-flusherDone
		st.Close()
		return err
	}
	// 关闭顺序：先停接收新上报，再把内存里的桶全部刷出，最后关库。
	// 反过来会把退出前最后一分钟的数据丢在内存里。
	shutdownCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	srv.Shutdown(shutdownCtx)
	stopFlusher()
	<-flusherDone
	return st.Close()
}
```

- [ ] **Step 6: node.go**

```go
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"text/tabwriter"

	"github.com/xjetry/probe/internal/clock"
	"github.com/xjetry/probe/internal/hub/auth"
	"github.com/xjetry/probe/internal/hub/store"
)

func openOffline(db string) (*store.Store, *auth.Auth, error) {
	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	st, err := store.Open(db, clock.Real(), log)
	if err != nil {
		return nil, nil, err
	}
	a := auth.New(st, clock.Real(), log)
	if err := a.Load(context.Background()); err != nil {
		st.Close()
		return nil, nil, err
	}
	return st, a, nil
}

const restartNotice = "note: if the hub is running, restart it for this change to take effect (the token map is rebuilt at startup)"

func runNode(args []string) error {
	if len(args) < 1 {
		return errors.New("usage: probe-hub node create|list|delete|rotate-token [flags]")
	}
	fs := flag.NewFlagSet("node "+args[0], flag.ContinueOnError)
	db := fs.String("db", "probe.db", "SQLite database path")
	name := fs.String("name", "", "node name (create)")
	id := fs.Int64("id", 0, "node id (delete, rotate-token)")
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}
	st, a, err := openOffline(*db)
	if err != nil {
		return err
	}
	defer st.Close()
	ctx := context.Background()
	switch args[0] {
	case "create":
		if *name == "" {
			return errors.New("--name is required")
		}
		nid, tok, err := a.CreateNode(ctx, *name)
		if err != nil {
			return err
		}
		fmt.Printf("id: %d\ntoken: %s\n", nid, tok)
		fmt.Fprintln(os.Stderr, "the token is shown once; the hub stores only its hash")
	case "list":
		nodes, err := st.ListNodes(ctx)
		if err != nil {
			return err
		}
		w := tabwriter.NewWriter(os.Stdout, 0, 8, 2, ' ', 0)
		fmt.Fprintln(w, "ID\tNAME\tPUBLIC\tLAST SEEN")
		for _, n := range nodes {
			seen := "never"
			if !n.LastSeenAt.IsZero() {
				seen = n.LastSeenAt.Format("2006-01-02 15:04:05Z")
			}
			fmt.Fprintf(w, "%d\t%s\t%v\t%s\n", n.ID, n.Name, n.Public, seen)
		}
		return w.Flush()
	case "delete":
		if *id == 0 {
			return errors.New("--id is required")
		}
		if err := a.DeleteNode(ctx, *id); err != nil {
			return err
		}
		fmt.Fprintln(os.Stderr, restartNotice)
	case "rotate-token":
		if *id == 0 {
			return errors.New("--id is required")
		}
		tok, err := a.RotateToken(ctx, *id)
		if err != nil {
			return err
		}
		fmt.Printf("token: %s\n", tok)
		fmt.Fprintln(os.Stderr, restartNotice)
	default:
		return fmt.Errorf("unknown node command %q", args[0])
	}
	return nil
}
```

- [ ] **Step 7: window.go**

```go
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"time"
)

func runWindow(args []string) error {
	if len(args) < 1 {
		return errors.New("usage: probe-hub window open|close|show [flags]")
	}
	fs := flag.NewFlagSet("window "+args[0], flag.ContinueOnError)
	db := fs.String("db", "probe.db", "SQLite database path")
	ttl := fs.Duration("ttl", time.Hour, "how long the window stays open (open)")
	max := fs.Int("max", 10, "how many nodes may register (open)")
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}
	st, a, err := openOffline(*db)
	if err != nil {
		return err
	}
	defer st.Close()
	ctx := context.Background()
	switch args[0] {
	case "open":
		key, err := a.OpenWindow(ctx, *ttl, *max)
		if err != nil {
			return err
		}
		fmt.Printf("key: %s\nexpires: %s\nmax: %d\n", key, time.Now().Add(*ttl).UTC().Format(time.RFC3339), *max)
	case "close":
		return a.CloseWindow(ctx)
	case "show":
		w, ok, err := a.Window(ctx)
		if err != nil {
			return err
		}
		if !ok {
			fmt.Println("no window")
			return nil
		}
		fmt.Printf("expires: %s\nremaining: %d\n", w.ExpiresAt.Format(time.RFC3339), w.Remaining)
	default:
		return fmt.Errorf("unknown window command %q", args[0])
	}
	return nil
}
```

- [ ] **Step 8: stats.go 与 store.Counts**

在 `internal/hub/store/node.go` 末尾追加：

```go
// Counts 返回各表行数，供运维核对与端到端验收。
func (s *Store) Counts(ctx context.Context) (map[string]int64, error) {
	out := map[string]int64{}
	for _, table := range []string{"node", "node_facts", "metric_1m", "register_window"} {
		var n int64
		if err := s.r.QueryRowContext(ctx, "SELECT COUNT(*) FROM "+table).Scan(&n); err != nil {
			return nil, err
		}
		out[table] = n
	}
	return out, nil
}
```

`cmd/hub/stats.go`：

```go
package main

import (
	"context"
	"flag"
	"fmt"
	"sort"
)

func runStats(args []string) error {
	fs := flag.NewFlagSet("stats", flag.ContinueOnError)
	db := fs.String("db", "probe.db", "SQLite database path")
	if err := fs.Parse(args); err != nil {
		return err
	}
	st, _, err := openOffline(*db)
	if err != nil {
		return err
	}
	defer st.Close()
	counts, err := st.Counts(context.Background())
	if err != nil {
		return err
	}
	tables := make([]string, 0, len(counts))
	for t := range counts {
		tables = append(tables, t)
	}
	sort.Strings(tables)
	for _, t := range tables {
		fmt.Printf("%s: %d\n", t, counts[t])
	}
	return nil
}
```

- [ ] **Step 9: 跑测试与冒烟**

```bash
go test -count=1 ./cmd/hub/ > /tmp/t8.log 2>&1; echo $?
go build -o /tmp/probe-hub ./cmd/hub && echo built
d=$(mktemp -d)
/tmp/probe-hub node create --db $d/x.db --name smoke
/tmp/probe-hub window open --db $d/x.db --ttl 5m --max 2
/tmp/probe-hub window show --db $d/x.db
/tmp/probe-hub node list --db $d/x.db
/tmp/probe-hub stats --db $d/x.db
PROBE_OFFLINE_AFTER=5s /tmp/probe-hub serve --db $d/x.db --listen 127.0.0.1:0; echo "exit=$?"
```

预期：测试 0；create 打印 id 与 64 字符 token；window show 显示 remaining 2；list 一行；stats 显示 node: 1、register_window: 1；最后一条因 TTL 低于下限退出码 1 且 stderr 含 "below the minimum"。再用 `PROBE_OFFLINE_AFTER=12s` 起一次，另一个终端 `curl -s -o /dev/null -w '%{http_code}\n' -X POST -H 'Content-Type: application/json' -d '{}' http://127.0.0.1:8080/probe.v1.AgentService/Report` 应得 401；Ctrl-C 后日志有 "shutting down" 且进程退出码 0。

- [ ] **Step 10: 提交**

```bash
git add cmd/hub internal/hub/store/node.go
git commit -m "hub: probe-hub 命令——serve 与节点、注册窗口、统计子命令

TTL 是唯一被直接配置的时间量，经 PROBE_OFFLINE_AFTER 读入并校验下限。
退出顺序固定为停收上报、刷出全部内存桶、关库，反过来会丢最后一分钟。
离线子命令直接改表，token 映射在启动时自库重建，因此提示重启生效。"
```

---

### Task 9: Linux 采集——`/proc` 解析与 fixture

**必读**：spec §4.2（`Metrics`、`Facts` 字段）、§7 末段（默认网卡过滤）、§12（采集与 darwin build tag 两条）。

**Files:**
- Create: `internal/agent/collect/parse.go`、`collect.go`、`platform_linux.go`、`platform_other.go`、`parse_test.go`、`collect_test.go`、`scripts/capture-proc.sh`、`internal/agent/collect/testdata/docker-debian/…`

**Interfaces:**
- Consumes: `probev1.Metrics`、`probev1.Facts`、`clock.Clock`。
- Produces:
  ```go
  type Collector struct { FS fs.FS; DiskUsage func(path string) (total, used uint64, err error); Clock clock.Clock; NetInclude, NetExclude []string; Version string; … }
  func (c *Collector) Metrics() (*probev1.Metrics, error)   // 总是返回可上报的 m；err 汇总读取失败供日志
  func (c *Collector) Facts() *probev1.Facts
  func NewPlatform(version string, clk clock.Clock, netInclude, netExclude []string) (*Collector, error)   // linux 实现；其余平台返回错误
  ```

- [ ] **Step 1: 抓 fixture**

`scripts/capture-proc.sh`：

```sh
#!/bin/sh
# 从一个 Linux 容器抓取采集层要读的文件，作为解析测试的 fixture。
# /proc 下的文件 stat 出来大小为 0，tar 直接打包会得到空文件，所以先 cat 成普通文件。
set -eu
name=${1:?usage: capture-proc.sh <fixture-name> [image]}
image=${2:-debian:bookworm-slim}
dest="$(cd "$(dirname "$0")/.." && pwd)/internal/agent/collect/testdata/$name"
mkdir -p "$dest"
docker run --rm "$image" sh -c '
  set -e
  out=/tmp/fixture
  for f in proc/stat proc/meminfo proc/loadavg proc/uptime proc/net/dev proc/net/sockstat proc/net/sockstat6 \
           proc/sys/kernel/random/boot_id proc/sys/kernel/osrelease proc/sys/kernel/hostname proc/cpuinfo \
           proc/1/environ etc/os-release; do
    mkdir -p "$out/$(dirname "$f")"
    cat "/$f" > "$out/$f" 2>/dev/null || true
  done
  for i in /sys/class/net/*; do
    n=$(basename "$i")
    mkdir -p "$out/sys/class/net/$n/statistics"
    cat "$i/statistics/rx_bytes" > "$out/sys/class/net/$n/statistics/rx_bytes"
    cat "$i/statistics/tx_bytes" > "$out/sys/class/net/$n/statistics/tx_bytes"
  done
  tar -c -C "$out" .
' | tar -x -C "$dest"
echo "captured into $dest"
find "$dest" -type f | sort
```

```bash
chmod +x scripts/capture-proc.sh && scripts/capture-proc.sh docker-debian
```

预期列出约 18 个文件，`proc/stat` 非空。

- [ ] **Step 2: 失败的测试**

`parse_test.go`：

```go
package collect

import (
	"strings"
	"testing"
)

func TestParseStatCPUTimes(t *testing.T) {
	in := "cpu  100 0 50 800 20 0 10 0 0 0\ncpu0 1 2 3 4 5 6 7 8 9 10\n"
	c, err := parseStat(strings.NewReader(in))
	if err != nil {
		t.Fatal(err)
	}
	// idle = idle + iowait = 820；total = 100+0+50+800+20+0+10+0 = 980
	if c.idle != 820 || c.total != 980 {
		t.Fatalf("idle/total = %d/%d, want 820/980", c.idle, c.total)
	}
}

func TestCPUPercentFromTwoSamples(t *testing.T) {
	a := cpuTimes{idle: 800, total: 1000}
	b := cpuTimes{idle: 850, total: 1100} // 100 个 tick 里 50 个空闲 → 50%
	got, ok := cpuPercent(a, b)
	if !ok || got != 50 {
		t.Fatalf("cpu%% = %v,%v", got, ok)
	}
	if _, ok := cpuPercent(b, b); ok {
		t.Fatal("no elapsed ticks must yield no reading, not 0%")
	}
}

func TestParseMeminfoKBToBytes(t *testing.T) {
	in := "MemTotal:       2048 kB\nMemFree:        100 kB\nMemAvailable:   1024 kB\nSwapTotal:      512 kB\nSwapFree:       256 kB\n"
	m, err := parseMeminfo(strings.NewReader(in))
	if err != nil {
		t.Fatal(err)
	}
	if m.total != 2048*1024 || m.available != 1024*1024 || m.swapTotal != 512*1024 || m.swapFree != 256*1024 {
		t.Fatalf("%+v", m)
	}
}

func TestParseLoadavg(t *testing.T) {
	l, err := parseLoadavg(strings.NewReader("0.52 0.31 0.20 3/721 12345\n"))
	if err != nil {
		t.Fatal(err)
	}
	if l.l1 != 0.52 || l.l5 != 0.31 || l.l15 != 0.20 || l.procs != 721 {
		t.Fatalf("%+v", l)
	}
}

func TestParseNetDev(t *testing.T) {
	in := `Inter-|   Receive                                                |  Transmit
 face |bytes    packets errs drop fifo frame compressed multicast|bytes    packets errs drop fifo colls carrier compressed
    lo:     100     1    0    0    0     0          0         0      100     1    0    0    0     0       0          0
  eth0:    5000    10    0    0    0     0          0         0     7000    12    0    0    0     0       0          0
`
	m, err := parseNetDev(strings.NewReader(in))
	if err != nil {
		t.Fatal(err)
	}
	if m["eth0"].rx != 5000 || m["eth0"].tx != 7000 || m["lo"].rx != 100 {
		t.Fatalf("%+v", m)
	}
}

func TestParseSockstatBothFamilies(t *testing.T) {
	tcp, udp, err := parseSockstat(strings.NewReader("sockets: used 5\nTCP: inuse 3 orphan 0 tw 0 alloc 1 mem 0\nUDP: inuse 2 mem 1\n"))
	if err != nil || tcp != 3 || udp != 2 {
		t.Fatalf("v4: %d %d %v", tcp, udp, err)
	}
	tcp, udp, err = parseSockstat(strings.NewReader("TCP6: inuse 4\nUDP6: inuse 1\nUDPLITE6: inuse 0\n"))
	if err != nil || tcp != 4 || udp != 1 {
		t.Fatalf("v6: %d %d %v", tcp, udp, err)
	}
}

func TestParseCPUInfo(t *testing.T) {
	in := "processor\t: 0\nmodel name\t: Fancy CPU @ 3.0GHz\n\nprocessor\t: 1\nmodel name\t: Fancy CPU @ 3.0GHz\n"
	c := parseCPUInfo(strings.NewReader(in))
	if c.model != "Fancy CPU @ 3.0GHz" || c.cores != 2 {
		t.Fatalf("%+v", c)
	}
}

func TestParseOSReleasePrettyName(t *testing.T) {
	in := "NAME=\"Debian GNU/Linux\"\nPRETTY_NAME=\"Debian GNU/Linux 12 (bookworm)\"\nID=debian\n"
	if got := parseOSRelease(strings.NewReader(in)); got != "Debian GNU/Linux 12 (bookworm)" {
		t.Fatalf("%q", got)
	}
}

func TestInterfaceFilterDefaults(t *testing.T) {
	c := &Collector{}
	for _, n := range []string{"lo", "docker0", "veth1234", "br-abc", "virbr0"} {
		if c.includeIface(n) {
			t.Fatalf("%s must be excluded by default", n)
		}
	}
	for _, n := range []string{"eth0", "ens3", "wlan0"} {
		if !c.includeIface(n) {
			t.Fatalf("%s must be included by default", n)
		}
	}
	c = &Collector{NetInclude: []string{"eth*"}}
	if c.includeIface("ens3") || !c.includeIface("eth1") {
		t.Fatal("explicit include list must be exclusive")
	}
}
```

`collect_test.go`：

```go
package collect

import (
	"os"
	"testing"
	"testing/fstest"
	"time"

	"github.com/xjetry/probe/internal/clock"
)

func fixture(t *testing.T) *Collector {
	t.Helper()
	return &Collector{
		FS:        os.DirFS("testdata/docker-debian"),
		DiskUsage: func(string) (uint64, uint64, error) { return 1000, 400, nil },
		Clock:     clock.NewFake(time.Unix(0, 0)),
		Version:   "test",
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
	c := &Collector{FS: fsys, DiskUsage: func(string) (uint64, uint64, error) { return 0, 0, nil }, Clock: clock.NewFake(time.Unix(0, 0))}
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
	c := &Collector{FS: fsys, DiskUsage: func(string) (uint64, uint64, error) { return 0, 0, nil }, Clock: clk}
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
	c := &Collector{FS: fstest.MapFS{}, DiskUsage: func(string) (uint64, uint64, error) { return 0, 0, os.ErrNotExist }, Clock: clock.NewFake(time.Unix(0, 0))}
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
```

- [ ] **Step 3: 跑，确认红** — `go test -count=1 ./internal/agent/collect/ > /tmp/t9.log 2>&1; echo $?` → 非 0。

- [ ] **Step 4: parse.go**

```go
// Package collect 读取 Linux 的 /proc 与 /sys 生成一次上报。
//
// 解析全部是对 fs.FS 的纯函数，不带 build tag：它们在任何平台上都能用真机
// 抓来的快照测试。只有取根文件系统与 statfs 的几行在 platform_linux.go 里。
package collect

import (
	"bufio"
	"errors"
	"io"
	"io/fs"
	"strconv"
	"strings"
)

type cpuTimes struct{ idle, total uint64 }

// parseStat 取 /proc/stat 首行的聚合 CPU 时间。guest 与 guest_nice 已计入
// user 与 nice，不再相加。
func parseStat(r io.Reader) (cpuTimes, error) {
	sc := bufio.NewScanner(r)
	for sc.Scan() {
		f := strings.Fields(sc.Text())
		if len(f) < 8 || f[0] != "cpu" {
			continue
		}
		var vals [8]uint64
		for i := range vals {
			v, err := strconv.ParseUint(f[i+1], 10, 64)
			if err != nil {
				return cpuTimes{}, err
			}
			vals[i] = v
		}
		var total uint64
		for _, v := range vals {
			total += v
		}
		return cpuTimes{idle: vals[3] + vals[4], total: total}, nil
	}
	return cpuTimes{}, errors.New("/proc/stat: no cpu line")
}

// cpuPercent 由两次采样的差算出忙碌比例；没有流逝的 tick 就没有读数。
func cpuPercent(prev, cur cpuTimes) (float64, bool) {
	if cur.total <= prev.total || cur.idle < prev.idle {
		return 0, false
	}
	dt := float64(cur.total - prev.total)
	di := float64(cur.idle - prev.idle)
	pct := 100 * (1 - di/dt)
	if pct < 0 {
		pct = 0
	}
	if pct > 100 {
		pct = 100
	}
	return pct, true
}

type memInfo struct{ total, available, swapTotal, swapFree uint64 }

func parseMeminfo(r io.Reader) (memInfo, error) {
	var m memInfo
	found := 0
	sc := bufio.NewScanner(r)
	for sc.Scan() {
		f := strings.Fields(sc.Text())
		if len(f) < 2 {
			continue
		}
		v, err := strconv.ParseUint(f[1], 10, 64)
		if err != nil {
			continue
		}
		v *= 1024
		switch f[0] {
		case "MemTotal:":
			m.total, found = v, found+1
		case "MemAvailable:":
			m.available, found = v, found+1
		case "SwapTotal:":
			m.swapTotal, found = v, found+1
		case "SwapFree:":
			m.swapFree, found = v, found+1
		}
	}
	if found < 4 {
		return m, errors.New("/proc/meminfo: missing fields")
	}
	return m, nil
}

type loadAvg struct {
	l1, l5, l15 float64
	procs       uint32
}

func parseLoadavg(r io.Reader) (loadAvg, error) {
	b, err := io.ReadAll(r)
	if err != nil {
		return loadAvg{}, err
	}
	f := strings.Fields(string(b))
	if len(f) < 4 {
		return loadAvg{}, errors.New("/proc/loadavg: short")
	}
	var l loadAvg
	if l.l1, err = strconv.ParseFloat(f[0], 64); err != nil {
		return l, err
	}
	if l.l5, err = strconv.ParseFloat(f[1], 64); err != nil {
		return l, err
	}
	if l.l15, err = strconv.ParseFloat(f[2], 64); err != nil {
		return l, err
	}
	_, total, ok := strings.Cut(f[3], "/")
	if !ok {
		return l, errors.New("/proc/loadavg: no running/total field")
	}
	n, err := strconv.ParseUint(total, 10, 32)
	if err != nil {
		return l, err
	}
	l.procs = uint32(n)
	return l, nil
}

func parseUptime(r io.Reader) (uint64, error) {
	b, err := io.ReadAll(r)
	if err != nil {
		return 0, err
	}
	f := strings.Fields(string(b))
	if len(f) < 1 {
		return 0, errors.New("/proc/uptime: empty")
	}
	sec, err := strconv.ParseFloat(f[0], 64)
	if err != nil {
		return 0, err
	}
	return uint64(sec), nil
}

type netCounters struct{ rx, tx uint64 }

// parseNetDev 读 /proc/net/dev：前两行是表头；每行 "iface: rx_bytes … tx_bytes …"，
// 接收段 8 列后是发送段。
func parseNetDev(r io.Reader) (map[string]netCounters, error) {
	out := map[string]netCounters{}
	sc := bufio.NewScanner(r)
	for sc.Scan() {
		name, rest, ok := strings.Cut(sc.Text(), ":")
		if !ok {
			continue
		}
		f := strings.Fields(rest)
		if len(f) < 9 {
			continue
		}
		rx, err1 := strconv.ParseUint(f[0], 10, 64)
		tx, err2 := strconv.ParseUint(f[8], 10, 64)
		if err1 != nil || err2 != nil {
			continue
		}
		out[strings.TrimSpace(name)] = netCounters{rx: rx, tx: tx}
	}
	if len(out) == 0 {
		return nil, errors.New("/proc/net/dev: no interfaces")
	}
	return out, sc.Err()
}

// parseSockstat 同时认 sockstat（TCP:/UDP:）与 sockstat6（TCP6:/UDP6:）。
func parseSockstat(r io.Reader) (tcp, udp uint32, err error) {
	sc := bufio.NewScanner(r)
	for sc.Scan() {
		f := strings.Fields(sc.Text())
		if len(f) < 3 || f[1] != "inuse" {
			continue
		}
		n, perr := strconv.ParseUint(f[2], 10, 32)
		if perr != nil {
			continue
		}
		switch f[0] {
		case "TCP:", "TCP6:":
			tcp += uint32(n)
		case "UDP:", "UDP6:":
			udp += uint32(n)
		}
	}
	return tcp, udp, sc.Err()
}

type cpuInfo struct {
	model string
	cores uint32
}

func parseCPUInfo(r io.Reader) cpuInfo {
	var c cpuInfo
	sc := bufio.NewScanner(r)
	for sc.Scan() {
		k, v, ok := strings.Cut(sc.Text(), ":")
		if !ok {
			continue
		}
		switch strings.TrimSpace(k) {
		case "processor":
			c.cores++
		case "model name":
			if c.model == "" {
				c.model = strings.TrimSpace(v)
			}
		}
	}
	return c
}

func parseOSRelease(r io.Reader) string {
	sc := bufio.NewScanner(r)
	for sc.Scan() {
		if v, ok := strings.CutPrefix(sc.Text(), "PRETTY_NAME="); ok {
			return strings.Trim(v, `"`)
		}
	}
	return ""
}

func readTrim(fsys fs.FS, name string) (string, error) {
	b, err := fs.ReadFile(fsys, name)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(b)), nil
}

// detectVirtualization 只做几条廉价而明确的判定，认不出就返回空串。
func detectVirtualization(fsys fs.FS) string {
	if b, err := fs.ReadFile(fsys, "proc/1/environ"); err == nil {
		for _, kv := range strings.Split(string(b), "\x00") {
			if v, ok := strings.CutPrefix(kv, "container="); ok && v != "" {
				return v
			}
		}
	}
	if _, err := fs.Stat(fsys, "proc/vz"); err == nil {
		return "openvz"
	}
	if _, err := fs.Stat(fsys, "proc/xen"); err == nil {
		return "xen"
	}
	if p, err := readTrim(fsys, "sys/class/dmi/id/product_name"); err == nil {
		switch l := strings.ToLower(p); {
		case strings.Contains(l, "kvm"), strings.Contains(l, "qemu"):
			return "kvm"
		case strings.Contains(l, "vmware"):
			return "vmware"
		case strings.Contains(l, "virtualbox"):
			return "virtualbox"
		case strings.Contains(l, "hyper-v"), strings.Contains(l, "virtual machine"):
			return "hyperv"
		}
	}
	if b, err := fs.ReadFile(fsys, "proc/cpuinfo"); err == nil && strings.Contains(string(b), " hypervisor") {
		return "vm"
	}
	return ""
}
```

- [ ] **Step 5: collect.go**

```go
package collect

import (
	"errors"
	"fmt"
	"io/fs"
	"path"
	"runtime"
	"time"

	probev1 "github.com/xjetry/probe/gen/probe/v1"
	"github.com/xjetry/probe/internal/clock"
	"google.golang.org/protobuf/proto"
)

// defaultNetExclude 是不计入流量的网卡：回环与常见的虚拟桥接口。
var defaultNetExclude = []string{"lo", "docker*", "veth*", "br-*", "virbr*"}

type Collector struct {
	// FS 是主机根文件系统；路径相对根，如 "proc/stat"。
	FS fs.FS
	// DiskUsage 取根分区用量；statfs 是系统调用，由平台文件注入。
	DiskUsage func(path string) (total, used uint64, err error)
	Clock     clock.Clock
	// NetInclude 非空时只统计匹配的网卡；否则统计除 NetExclude（默认列表）外的全部。
	NetInclude []string
	NetExclude []string
	Version    string

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
		ex = defaultNetExclude
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

func (c *Collector) open(name string) (fs.File, error) { return c.FS.Open(name) }

// Metrics 生成一次上报。任何一个来源读不到只让对应读数缺失，其余照常；
// 返回的 error 汇总了这些失败，供调用方记日志，不阻止上报。
func (c *Collector) Metrics() (*probev1.Metrics, error) {
	m := &probev1.Metrics{}
	var errs []error
	fail := func(what string, err error) { errs = append(errs, fmt.Errorf("%s: %w", what, err)) }

	if id, err := readTrim(c.FS, "proc/sys/kernel/random/boot_id"); err == nil {
		m.BootId = id
	} else {
		fail("boot_id", err)
	}

	if f, err := c.open("proc/stat"); err == nil {
		cur, perr := parseStat(f)
		f.Close()
		if perr != nil {
			fail("stat", perr)
		} else {
			if c.prevCPU != nil {
				if pct, ok := cpuPercent(*c.prevCPU, cur); ok {
					m.CpuPct = proto.Float64(pct)
				}
			}
			c.prevCPU = &cur
		}
	} else {
		fail("stat", err)
	}

	if f, err := c.open("proc/meminfo"); err == nil {
		mi, perr := parseMeminfo(f)
		f.Close()
		if perr != nil {
			fail("meminfo", perr)
		} else {
			m.MemTotal, m.MemUsed = proto.Uint64(mi.total), proto.Uint64(mi.total-mi.available)
			m.SwapTotal, m.SwapUsed = proto.Uint64(mi.swapTotal), proto.Uint64(mi.swapTotal-mi.swapFree)
		}
	} else {
		fail("meminfo", err)
	}

	if f, err := c.open("proc/loadavg"); err == nil {
		l, perr := parseLoadavg(f)
		f.Close()
		if perr != nil {
			fail("loadavg", perr)
		} else {
			m.Load1, m.Load5, m.Load15 = proto.Float64(l.l1), proto.Float64(l.l5), proto.Float64(l.l15)
			m.Procs = proto.Uint32(l.procs)
		}
	} else {
		fail("loadavg", err)
	}

	if f, err := c.open("proc/uptime"); err == nil {
		up, perr := parseUptime(f)
		f.Close()
		if perr != nil {
			fail("uptime", perr)
		} else {
			m.UptimeS = proto.Uint64(up)
		}
	} else {
		fail("uptime", err)
	}

	if total, used, err := c.DiskUsage("/"); err == nil {
		m.DiskTotal, m.DiskUsed = proto.Uint64(total), proto.Uint64(used)
	} else {
		fail("disk", err)
	}

	var tcp, udp uint32
	gotConns := false
	for _, name := range []string{"proc/net/sockstat", "proc/net/sockstat6"} {
		f, err := c.open(name)
		if err != nil {
			continue
		}
		t, u, perr := parseSockstat(f)
		f.Close()
		if perr != nil {
			fail(name, perr)
			continue
		}
		tcp, udp, gotConns = tcp+t, udp+u, true
	}
	if gotConns {
		m.TcpConns, m.UdpConns = proto.Uint32(tcp), proto.Uint32(udp)
	} else {
		fail("sockstat", fs.ErrNotExist)
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

// netTotals 从 /sys/class/net/<if>/statistics 汇总计数器；每个网卡一对文件，
// 比 /proc/net/dev 少一次整表解析，且缺某块网卡时不影响其余。
func (c *Collector) netTotals() (netCounters, error) {
	entries, err := fs.ReadDir(c.FS, "sys/class/net")
	if err != nil {
		return netCounters{}, err
	}
	var sum netCounters
	found := false
	for _, e := range entries {
		if !c.includeIface(e.Name()) {
			continue
		}
		base := "sys/class/net/" + e.Name() + "/statistics/"
		rx, err1 := readUint(c.FS, base+"rx_bytes")
		tx, err2 := readUint(c.FS, base+"tx_bytes")
		if err1 != nil || err2 != nil {
			continue
		}
		sum.rx, sum.tx, found = sum.rx+rx, sum.tx+tx, true
	}
	if !found {
		return netCounters{}, errors.New("no interface with readable counters")
	}
	return sum, nil
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

// Facts 收集静态信息；读不到的字段留空，由 hub 侧展示为未知。
func (c *Collector) Facts() *probev1.Facts {
	f := &probev1.Facts{Arch: runtime.GOARCH, AgentVersion: c.Version, IcmpAvailable: false}
	f.Hostname, _ = readTrim(c.FS, "proc/sys/kernel/hostname")
	f.Kernel, _ = readTrim(c.FS, "proc/sys/kernel/osrelease")
	if r, err := c.open("etc/os-release"); err == nil {
		f.Os = parseOSRelease(r)
		r.Close()
	}
	if r, err := c.open("proc/cpuinfo"); err == nil {
		ci := parseCPUInfo(r)
		r.Close()
		f.CpuModel, f.CpuCores = ci.model, ci.cores
	}
	if f.CpuCores == 0 {
		f.CpuCores = uint32(runtime.NumCPU())
	}
	f.Virtualization = detectVirtualization(c.FS)
	return f
}
```

- [ ] **Step 6: 平台文件**

`platform_linux.go`：

```go
//go:build linux

package collect

import (
	"os"

	"golang.org/x/sys/unix"

	"github.com/xjetry/probe/internal/clock"
)

func NewPlatform(version string, clk clock.Clock, netInclude, netExclude []string) (*Collector, error) {
	return &Collector{FS: os.DirFS("/"), DiskUsage: statfs, Clock: clk, NetInclude: netInclude, NetExclude: netExclude, Version: version}, nil
}

// statfs 按 df 的口径：total = 全部块，used = 全部块 − 空闲块（含 root 保留）。
func statfs(path string) (uint64, uint64, error) {
	var st unix.Statfs_t
	if err := unix.Statfs(path, &st); err != nil {
		return 0, 0, err
	}
	bs := uint64(st.Bsize)
	return st.Blocks * bs, (st.Blocks - st.Bfree) * bs, nil
}
```

`platform_other.go`：

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
```

- [ ] **Step 7: 跑，确认绿；交叉 vet**

```bash
go test -count=1 ./internal/agent/collect/ > /tmp/t9.log 2>&1; echo $?
GOOS=linux go vet ./internal/agent/... ; echo "vet=$?"
GOOS=linux GOARCH=amd64 go build ./internal/agent/... ; echo "build=$?"
```

预期三个 0。

- [ ] **Step 8: 缺陷注入（两处）**

1. `cpuPercent` 在 `cur.total <= prev.total` 时返回 `0, true`，预期 `TestCPUPercentFromTwoSamples` 红（"no elapsed ticks must yield no reading"）。改回。
2. `Metrics` 里 meminfo 读失败时改为 `m.MemTotal = proto.Uint64(0)`，预期 `TestMissingFilesYieldMissingReadingsNotZero` 红。改回。

- [ ] **Step 9: 提交**

```bash
git add internal/agent/collect scripts/capture-proc.sh
git commit -m "agent: Linux 采集层——/proc 解析与容器抓取的 fixture

解析是对 fs.FS 的纯函数，不带 build tag，在任何平台都能用真机快照测；
只有 os.DirFS 与 statfs 在 linux 文件里。任一来源读不到只让对应读数缺失，
不填 0：无读数与 0 从采集处就分开。"
```

---

### Task 10: agent 上报循环与 `probe-agent` 命令

**必读**：spec §4.2、§4.3、§4.7、§4.8。

**Files:**
- Create: `internal/agent/client/config.go`、`hash.go`、`backoff.go`、`runner.go`、`client_test.go`、`cmd/agent/main.go`

**Interfaces:**
- Consumes: `collect.Collector`、`probev1connect.AgentServiceClient`、`clock.Clock`。
- Produces:
  ```go
  type Config struct { Hub, Token, Name string }
  func LoadConfig(path string) (Config, error)
  func SaveConfig(path string, c Config) error        // 0600，先写临时文件再 rename
  func FactsHash(f *probev1.Facts) uint64
  func Backoff(attempt int, interval time.Duration, rnd func() float64) time.Duration
  type Runner struct { Collector *collect.Collector; Client probev1connect.AgentServiceClient; Token string; Clock clock.Clock; Sleep func(context.Context, time.Duration) error; Rand func() float64; Log *slog.Logger; Interval time.Duration }
  func (r *Runner) Run(ctx) error   // 阻塞到 ctx 结束
  ```

- [ ] **Step 1: 失败的测试**

```go
package client

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"testing/fstest"
	"time"

	"connectrpc.com/connect"
	probev1 "github.com/xjetry/probe/gen/probe/v1"
	"github.com/xjetry/probe/gen/probe/v1/probev1connect"
	"github.com/xjetry/probe/internal/agent/collect"
	"github.com/xjetry/probe/internal/clock"
)

func TestConfigRoundTripAndPermissions(t *testing.T) {
	p := filepath.Join(t.TempDir(), "sub", "config.json")
	if err := SaveConfig(p, Config{Hub: "http://h", Token: "t", Name: "n"}); err != nil {
		t.Fatal(err)
	}
	st, _ := os.Stat(p)
	if st.Mode().Perm() != 0o600 {
		t.Fatalf("perm = %o, want 600", st.Mode().Perm())
	}
	c, err := LoadConfig(p)
	if err != nil || c.Hub != "http://h" || c.Token != "t" || c.Name != "n" {
		t.Fatalf("%+v %v", c, err)
	}
}

func TestFactsHashIsStableAndSensitive(t *testing.T) {
	a := &probev1.Facts{Hostname: "h", CpuCores: 2}
	b := &probev1.Facts{Hostname: "h", CpuCores: 2}
	if FactsHash(a) != FactsHash(b) {
		t.Fatal("equal facts must hash equal")
	}
	b.CpuCores = 3
	if FactsHash(a) == FactsHash(b) {
		t.Fatal("different facts must hash differently")
	}
}

func TestBackoffGrowsAndCapsAtThreeIntervals(t *testing.T) {
	interval := 10 * time.Second
	one := func() float64 { return 1 }
	zero := func() float64 { return 0 }
	if d := Backoff(1, interval, one); d != interval {
		t.Fatalf("attempt 1 max = %v, want %v", d, interval)
	}
	if d := Backoff(2, interval, one); d != 2*interval {
		t.Fatalf("attempt 2 max = %v, want %v", d, 2*interval)
	}
	for attempt := 3; attempt < 40; attempt++ {
		if d := Backoff(attempt, interval, one); d != 3*interval {
			t.Fatalf("attempt %d max = %v, want cap %v", attempt, d, 3*interval)
		}
		if d := Backoff(attempt, interval, zero); d < 3*interval/2 {
			t.Fatalf("attempt %d min = %v, jitter must keep at least half the base", attempt, d)
		}
	}
}

// fakeHub 记录收到的上报并按脚本应答。
type fakeHub struct {
	mu       sync.Mutex
	reports  []*probev1.ReportRequest
	wantNext bool
	fail     bool
	interval uint32
}

func (f *fakeHub) Register(context.Context, *connect.Request[probev1.RegisterRequest]) (*connect.Response[probev1.RegisterResponse], error) {
	return nil, connect.NewError(connect.CodeUnimplemented, nil)
}

func (f *fakeHub) Report(_ context.Context, req *connect.Request[probev1.ReportRequest]) (*connect.Response[probev1.ReportResponse], error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if req.Header().Get("Authorization") != "Bearer tok" {
		return nil, connect.NewError(connect.CodeUnauthenticated, errors.New("no"))
	}
	if f.fail {
		return nil, connect.NewError(connect.CodeUnavailable, errors.New("down"))
	}
	f.reports = append(f.reports, req.Msg)
	want := f.wantNext
	f.wantNext = false
	return connect.NewResponse(&probev1.ReportResponse{ReportIntervalMs: f.interval, WantFacts: want}), nil
}

func (f *fakeHub) count() int { f.mu.Lock(); defer f.mu.Unlock(); return len(f.reports) }

func newRunner(t *testing.T, hub *fakeHub) (*Runner, chan time.Duration) {
	t.Helper()
	mux := http.NewServeMux()
	mux.Handle(probev1connect.NewAgentServiceHandler(hub))
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	sleeps := make(chan time.Duration, 100)
	r := &Runner{
		Collector: &collect.Collector{FS: fstest.MapFS{"proc/loadavg": {Data: []byte("0 0 0 1/2 3\n")}}, DiskUsage: func(string) (uint64, uint64, error) { return 1, 1, nil }, Clock: clock.NewFake(time.Unix(0, 0)), Version: "t"},
		Client:    probev1connect.NewAgentServiceClient(srv.Client(), srv.URL),
		Token:     "tok",
		Clock:     clock.NewFake(time.Unix(0, 0)),
		Sleep: func(ctx context.Context, d time.Duration) error {
			sleeps <- d
			select {
			case <-ctx.Done():
				return ctx.Err()
			default:
				return nil
			}
		},
		Rand:     func() float64 { return 1 },
		Log:      slog.Default(),
		Interval: 10 * time.Second,
	}
	return r, sleeps
}

func runFor(t *testing.T, r *Runner, hub *fakeHub, reports int) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { r.Run(ctx); close(done) }()
	deadline := time.Now().Add(2 * time.Second)
	for hub.count() < reports && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	cancel()
	<-done
	if hub.count() < reports {
		t.Fatalf("got %d reports, want at least %d", hub.count(), reports)
	}
}

func TestFirstReportCarriesFactsThenOnlyOnRequest(t *testing.T) {
	hub := &fakeHub{interval: 5000}
	r, _ := newRunner(t, hub)
	hub.wantNext = false
	runFor(t, r, hub, 2)
	if hub.reports[0].Facts == nil || hub.reports[0].FactsHash == 0 {
		t.Fatal("first report must carry facts and their hash")
	}
	if hub.reports[1].Facts != nil || hub.reports[1].FactsHash != hub.reports[0].FactsHash {
		t.Fatal("second report must carry only the hash")
	}
}

func TestWantFactsTriggersResend(t *testing.T) {
	hub := &fakeHub{interval: 5000}
	r, _ := newRunner(t, hub)
	hub.mu.Lock()
	hub.wantNext = true // 首次响应就要求 facts → 第二次上报再次携带
	hub.mu.Unlock()
	runFor(t, r, hub, 2)
	if hub.reports[1].Facts == nil {
		t.Fatal("want_facts must make the next report carry facts")
	}
}

func TestAdoptsIntervalFromResponse(t *testing.T) {
	hub := &fakeHub{interval: 7000}
	r, sleeps := newRunner(t, hub)
	runFor(t, r, hub, 1)
	if d := <-sleeps; d != 7*time.Second {
		t.Fatalf("slept %v after first response, want the assigned 7s", d)
	}
}

func TestFailureBacksOffWithinThreeIntervals(t *testing.T) {
	hub := &fakeHub{interval: 5000, fail: true}
	r, sleeps := newRunner(t, hub)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { r.Run(ctx); close(done) }()
	var seen []time.Duration
	for len(seen) < 6 {
		seen = append(seen, <-sleeps)
	}
	cancel()
	<-done
	if seen[0] != 10*time.Second || seen[1] != 20*time.Second {
		t.Fatalf("first two backoffs = %v, want 10s then 20s", seen[:2])
	}
	for _, d := range seen[2:] {
		if d != 30*time.Second {
			t.Fatalf("backoff %v exceeds cap 3×interval", d)
		}
	}
}
```

- [ ] **Step 2: 跑，确认红** — `go test -count=1 ./internal/agent/client/ > /tmp/t10.log 2>&1; echo $?` → 非 0。

- [ ] **Step 3: config.go、hash.go、backoff.go**

```go
package client

import (
	"encoding/json"
	"os"
	"path/filepath"
)

type Config struct {
	Hub   string `json:"hub"`
	Token string `json:"token"`
	Name  string `json:"name"`
}

func LoadConfig(path string) (Config, error) {
	var c Config
	b, err := os.ReadFile(path)
	if err != nil {
		return c, err
	}
	return c, json.Unmarshal(b, &c)
}

// SaveConfig 以 0600 写入，先写临时文件再 rename：token 不会以部分写入的状态落盘。
func SaveConfig(path string, c Config) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	b, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
```

```go
package client

import (
	"hash/fnv"

	probev1 "github.com/xjetry/probe/gen/probe/v1"
	"google.golang.org/protobuf/proto"
)

// FactsHash 是 agent 侧的静态信息摘要：hub 只比较、不重算，所以只需在
// 同一 agent 内稳定。确定性序列化保证同样的 Facts 得到同样的字节。
func FactsHash(f *probev1.Facts) uint64 {
	b, _ := proto.MarshalOptions{Deterministic: true}.Marshal(f)
	h := fnv.New64a()
	h.Write(b)
	return h.Sum64()
}
```

```go
package client

import "time"

// Backoff 给出第 attempt 次失败后的等待：指数增长，上限 3 × interval。
//
// 上限取 3 × interval 而非独立取值：interval = TTL / 3，所以上限就是 TTL——
// 恢复后重新可见的时长与掉线被发现的时长共用同一预算。抖动取 [base/2, base]，
// 把 hub 恢复后的重试摊开又不至于让等待坍缩到零。
func Backoff(attempt int, interval time.Duration, rnd func() float64) time.Duration {
	cap := 3 * interval
	base := interval
	for i := 1; i < attempt && base < cap; i++ {
		base *= 2
	}
	if base > cap {
		base = cap
	}
	half := base / 2
	return half + time.Duration(rnd()*float64(half))
}
```

- [ ] **Step 4: runner.go**

```go
package client

import (
	"context"
	"log/slog"
	"math/rand/v2"
	"time"

	"connectrpc.com/connect"

	probev1 "github.com/xjetry/probe/gen/probe/v1"
	"github.com/xjetry/probe/gen/probe/v1/probev1connect"
	"github.com/xjetry/probe/internal/agent/collect"
	"github.com/xjetry/probe/internal/clock"
)

type Runner struct {
	Collector *collect.Collector
	Client    probev1connect.AgentServiceClient
	Token     string
	Clock     clock.Clock
	Sleep     func(context.Context, time.Duration) error
	Rand      func() float64
	Log       *slog.Logger
	// Interval 是收到第一个响应之前使用的间隔；之后一律用 hub 下发的。
	Interval time.Duration
}

func sleepReal(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// Run 是上报循环。三样状态都是电平触发：facts 的摘要每次都带，hub 不一致时
// 索要；间隔以响应为准。失败退避、成功即回到下发间隔。指标不缓存——过期的
// 实时数据没有意义。
func (r *Runner) Run(ctx context.Context) error {
	if r.Sleep == nil {
		r.Sleep = sleepReal
	}
	if r.Rand == nil {
		r.Rand = rand.Float64
	}
	interval := r.Interval
	sendFacts := true
	var factsHash uint64
	attempt := 0
	for {
		m, err := r.Collector.Metrics()
		if err != nil {
			r.Log.Warn("partial collection", "err", err)
		}
		req := connect.NewRequest(&probev1.ReportRequest{Metrics: m})
		req.Header().Set("Authorization", "Bearer "+r.Token)
		if sendFacts {
			f := r.Collector.Facts()
			factsHash = FactsHash(f)
			req.Msg.Facts = f
		}
		req.Msg.FactsHash = factsHash

		resp, err := r.Client.Report(ctx, req)
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			attempt++
			d := Backoff(attempt, interval, r.Rand)
			r.Log.Warn("report failed", "err", err, "attempt", attempt, "retry_in", d)
			if err := r.Sleep(ctx, d); err != nil {
				return err
			}
			continue
		}
		attempt = 0
		sendFacts = resp.Msg.WantFacts
		if ms := resp.Msg.ReportIntervalMs; ms > 0 {
			interval = time.Duration(ms) * time.Millisecond
		}
		if err := r.Sleep(ctx, interval); err != nil {
			return err
		}
	}
}
```

- [ ] **Step 5: cmd/agent/main.go**

```go
// probe-agent：register 用注册窗口 key 换 token 并写入配置；run 进入上报循环。
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"connectrpc.com/connect"

	probev1 "github.com/xjetry/probe/gen/probe/v1"
	"github.com/xjetry/probe/gen/probe/v1/probev1connect"
	"github.com/xjetry/probe/internal/agent/client"
	"github.com/xjetry/probe/internal/agent/collect"
	"github.com/xjetry/probe/internal/clock"
)

var version = "dev"

const defaultConfig = "/etc/probe-agent/config.json"

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: probe-agent register|run|version [flags]")
		os.Exit(2)
	}
	var err error
	switch os.Args[1] {
	case "register":
		err = runRegister(os.Args[2:])
	case "run":
		err = runRun(os.Args[2:])
	case "version":
		fmt.Println(version)
	default:
		fmt.Fprintln(os.Stderr, "usage: probe-agent register|run|version [flags]")
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func runRegister(args []string) error {
	fs := flag.NewFlagSet("register", flag.ContinueOnError)
	hub := fs.String("hub", "", "hub base URL, e.g. https://probe.example.com")
	key := fs.String("key", "", "registration key from `probe-hub window open`")
	name := fs.String("name", "", "node name (default: hostname)")
	cfgPath := fs.String("config", defaultConfig, "where to write the agent config")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *hub == "" || *key == "" {
		return errors.New("--hub and --key are required")
	}
	if *name == "" {
		*name, _ = os.Hostname()
	}
	c := probev1connect.NewAgentServiceClient(&http.Client{Timeout: 15 * time.Second}, strings.TrimRight(*hub, "/"))
	resp, err := c.Register(context.Background(), connect.NewRequest(&probev1.RegisterRequest{Key: *key, Name: *name}))
	if err != nil {
		return fmt.Errorf("register: %w", err)
	}
	if err := client.SaveConfig(*cfgPath, client.Config{Hub: strings.TrimRight(*hub, "/"), Token: resp.Msg.Token, Name: *name}); err != nil {
		return err
	}
	fmt.Printf("registered as node %d; config written to %s\n", resp.Msg.NodeId, *cfgPath)
	return nil
}

func runRun(args []string) error {
	fs := flag.NewFlagSet("run", flag.ContinueOnError)
	cfgPath := fs.String("config", defaultConfig, "agent config path")
	include := fs.String("net-include", "", "comma-separated interface globs to count (exclusive)")
	exclude := fs.String("net-exclude", "", "comma-separated interface globs to skip (default: lo, docker*, veth*, br-*, virbr*)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	cfg, err := client.LoadConfig(*cfgPath)
	if err != nil {
		return fmt.Errorf("load config: %w (run `probe-agent register` first)", err)
	}
	clk := clock.Real()
	col, err := collect.NewPlatform(version, clk, splitList(*include), splitList(*exclude))
	if err != nil {
		return err
	}
	log := slog.New(slog.NewTextHandler(os.Stderr, nil))
	r := &client.Runner{
		Collector: col,
		Client:    probev1connect.NewAgentServiceClient(&http.Client{Timeout: 15 * time.Second}, cfg.Hub),
		Token:     cfg.Token,
		Clock:     clk,
		Log:       log,
		Interval:  10 * time.Second,
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	log.Info("agent starting", "hub", cfg.Hub, "version", version)
	if err := r.Run(ctx); err != nil && !errors.Is(err, context.Canceled) {
		return err
	}
	return nil
}

func splitList(s string) []string {
	if s == "" {
		return nil
	}
	var out []string
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}
```

- [ ] **Step 6: 跑，确认绿；交叉编译**

```bash
go test -count=1 ./internal/agent/client/ > /tmp/t10.log 2>&1; echo $?
GOOS=linux GOARCH=amd64 go build -o /dev/null ./cmd/agent; echo "amd64=$?"
GOOS=linux GOARCH=arm64 go build -o /dev/null ./cmd/agent; echo "arm64=$?"
go build -o /dev/null ./cmd/agent; echo "darwin=$?"
```

预期四个 0（darwin 能编译，`run` 时 `NewPlatform` 返回未支持）。

- [ ] **Step 7: 缺陷注入（两处）**

1. `Backoff` 里把 `cap := 3 * interval` 改成 `4 * interval`，预期 `TestBackoffGrowsAndCapsAtThreeIntervals` 与 `TestFailureBacksOffWithinThreeIntervals` 都红。改回。
2. `Run` 里 `sendFacts = resp.Msg.WantFacts` 改成 `sendFacts = false`，预期 `TestWantFactsTriggersResend` 红。改回。

- [ ] **Step 8: 提交**

```bash
git add internal/agent/client cmd/agent
git commit -m "agent: 上报循环、退避与 probe-agent 命令

facts 摘要每次都带、内容只在首次与 hub 索要时带，hub 丢失静态信息后
一个周期内自愈。退避上限取 3 × 下发间隔即 TTL：恢复后重新可见与掉线
被发现共用同一预算。配置以 0600 写入并经 rename 落盘。"
```

---

### Task 11: 端到端验收

**必读**：spec §15 M1 行（"结束时都是可端到端运行的状态"）、§12 末条。

**Files:**
- Create: `scripts/e2e.sh`
- Modify: `Makefile`（`e2e` 目标已在 Task 0 写好）

- [ ] **Step 1: e2e 脚本**

```sh
#!/bin/sh
# 端到端：hub 跑在宿主机，agent 跑在 Linux 容器里经 host.docker.internal 上报。
# 验收凭据是库里出现了节点、facts 与至少一条分钟行，不是"进程没报错"。
set -eu
cd "$(cd "$(dirname "$0")/.." && pwd)"
work=$(mktemp -d)
db="$work/e2e.db"
port=18080
case "$(uname -m)" in
  arm64|aarch64) agent=bin/probe-agent-linux-arm64 ;;
  *) agent=bin/probe-agent-linux-amd64 ;;
esac

key=$(bin/probe-hub window open --db "$db" --ttl 10m --max 1 | sed -n 's/^key: //p')
[ -n "$key" ] || { echo "no key"; exit 1; }

PROBE_OFFLINE_AFTER=12s bin/probe-hub serve --db "$db" --listen "127.0.0.1:$port" > "$work/hub.log" 2>&1 &
hub=$!
cleanup() { kill "$hub" 2>/dev/null || true; wait "$hub" 2>/dev/null || true; }
trap cleanup EXIT
sleep 1

docker run --rm --add-host=host.docker.internal:host-gateway \
  -v "$PWD/bin:/probe:ro" debian:bookworm-slim sh -c "
    set -e
    /probe/$(basename "$agent") register --hub http://host.docker.internal:$port --key $key --config /tmp/agent.json --name e2e
    timeout 75 /probe/$(basename "$agent") run --config /tmp/agent.json || true
  " > "$work/agent.log" 2>&1

kill "$hub"; wait "$hub" || true
trap - EXIT

echo "--- hub.log ---"; cat "$work/hub.log"
echo "--- agent.log (tail) ---"; tail -5 "$work/agent.log"
echo "--- stats ---"
bin/probe-hub stats --db "$db" | tee "$work/stats.txt"
bin/probe-hub node list --db "$db"

get() { sed -n "s/^$1: //p" "$work/stats.txt"; }
[ "$(get node)" = 1 ] || { echo "FAIL: node count"; exit 1; }
[ "$(get node_facts)" = 1 ] || { echo "FAIL: facts count"; exit 1; }
[ "$(get metric_1m)" -ge 1 ] || { echo "FAIL: no minute rows"; exit 1; }
grep -q "node registered" "$work/hub.log" || { echo "FAIL: hub did not log registration"; exit 1; }
echo "E2E OK"
```

- [ ] **Step 2: 跑全量验证与 e2e**

```bash
chmod +x scripts/e2e.sh
make gen lint test build > /tmp/t11-ci.log 2>&1; echo "ci=$?"
git diff --exit-code -- gen; echo "gen-clean=$?"
make e2e > /tmp/t11-e2e.log 2>&1; echo "e2e=$?"
tail -20 /tmp/t11-e2e.log
```

预期：`ci=0`、`gen-clean=0`、`e2e=0`，log 末尾 `E2E OK`，stats 显示 `metric_1m` ≥ 1（agent 跑 75s，跨越至少一个分钟边界；hub 收到 SIGTERM 后 Drain 把最后一个开着的桶也写入）。

- [ ] **Step 3: 缺陷注入（验收脚本自身）**

把 `scripts/e2e.sh` 里 `[ "$(get metric_1m)" -ge 1 ]` 临时改成 `-ge 1000`，重跑，预期 `FAIL: no minute rows`、退出码 1。改回。这一步证明脚本的断言真的在判定，而不是永远打印 OK。

- [ ] **Step 4: 提交**

```bash
git add scripts/e2e.sh
git commit -m "build: 端到端验收脚本——宿主机 hub 与容器内 Linux agent

验收凭据是库里出现节点、facts 与分钟行，不是进程没报错；断言方向经
反向注入确认会红。"
```

---

## 自查

- **spec 覆盖**：§3.1 布局（含 `metric` 补充）→ Task 0/3；§3.2 三种鉴权中的 agent 一种 → Task 6/7；§4.2 消息 → Task 1；§4.3 对账（facts）→ Task 7/10；§4.4 在线与 TTL 反推 → Task 4/7/8/10；§4.5 时钟 → Task 2；§4.7 退避 → Task 10；§4.8 注册 → Task 6/7/10；§5.1 token/限速/64 KiB → Task 6/7；§5.2 窗口 → Task 5/6；§5.4 listen/trusted-proxies → Task 6/8；§6.1 写协程/只读池/上报不等库 → Task 5/7；§6.2 可加量/描述表/半桶合并 → Task 3/5；§6.4 第 1 条 → Task 5；§6.6 M1 需要的表 → Task 5；§7 网卡过滤 → Task 9；§11 前三行 → Task 7；§12 自动枚举 RPC、时间注入、半桶合并、无数据 vs 0、采集 fixture、`GOOS=linux go vet`、CI → Task 0/5/7/9；§14 CGO_ENABLED=0 与交叉编译 → Task 0/9/10；§15 M1 全部条目 → Task 1–11。
- **不在 M1 的**：§4.2 `tasks` 下发与 `probe_results` 处理、§6.3–6.5、§7 流量累计、§8、§9、§10、§13 除 buf 与 SQLite 已在计划前实测者之外的实验、§14 安装脚本与 systemd。
- **类型一致性**：`metric.Row` 在 live（产出）、store（消费与产出）、ingest（转交）三处同名同形；`store.ErrNoWindow`/`ErrBadKey` 在 auth 里按名引用；`auth.ErrDenied` 在 ingest 里按名引用；`Service.writer` 字段与 `minuteWriter` 接口在 Task 7 测试里直接赋值；`probev1connect.AgentServiceRegisterProcedure` / `AgentServiceReportProcedure` 是 connect 生成器的固定命名。
