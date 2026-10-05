# hub 与 agent 版本拆分 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** hub 与 agent 的版本号分开推进：每个 hub 版本绑定一个 agent 版本（仓库根 `AGENT_VERSION`），只改 hub 的 release 不带 agent 产物、不要求节点升级。

**Architecture:** 一套 `vX.Y.Z` tag，两种 release：`AGENT_VERSION` 等于 tag 为完整 release，低于 tag 且是正式版为只发 hub（判定只在 `scripts/releasekind` 一处）。只发 hub 时：门禁 `scripts/agentinputs` 核对 agent 组构建输入自绑定版本以来未变；`scripts/boundagent` 取绑定版本已验签的 `SHA256SUMS`，把它的两个 agent 安装脚本原样放进本次 release；端到端另用绑定版本已发布的 agent 包跑。hub 侧绑定只由 `updates.Manager` 持有：节点在线更新只接受绑定版本，`GetSnapshot`/`GetUpdates` 下发 `bound_agent_version`，面板按它标落后、选目标。

**Tech Stack:** Go 1.27（`go/parser`、`go/ast`、`crypto/ed25519` 经 `internal/releasesig`）、GNU make（本机 3.81 与 CI 4.x 都要能跑）、POSIX sh、React + Vitest、GitHub Actions。

**Spec:** `docs/superpowers/specs/2026-09-17-probe-architecture-design.md` 的 §14.1（主体，提交 aee08fa），以及 §4.6、§10（安装命令一条）、§14（「发布」一条与最后一条）。动手前完整读 §14.1。

## Global Constraints

- 执行规则：每个 worker 先读运行目录下的 `exec-rules.md`（不打补丁、注释与提交信息禁止过程信息、缺陷注入、判成败的命令不接管道、只在自己的 worktree 工作、日志写 `<task-dir>/logs/`）。
- 基点：Task 1、3、4 从提交 `7933dea` 开分支（它在 main `bb46a81`——已含 hub 中转与生产公钥——之上加了 spec 与地基提交）。地基提交已经做了这些，各任务不要重做：仓库根 `AGENT_VERSION`（内容 `v0.5.3` 一行）；Makefile 的 `AGENT_VERSION := $(strip $(shell read -r v < AGENT_VERSION; printf '%s' "$$v"))`、按原文拒绝 `$` 的 make 层守卫与 `export AGENT_VERSION`；`RELEASE_LDFLAGS`、`RELEASE_GOFLAGS`、`HUB_GOFLAGS`（hub 另注入 `-X main.agentVersion=$$AGENT_VERSION`）；`hub_build` 用 `HUB_GOFLAGS`；`hub-binary` 注入同一变量；`cmd/hub/main.go` 的 `var agentVersion string`。
- 配方里一律经环境变量 `$$AGENT_VERSION` 引用绑定版本，不写 `$(AGENT_VERSION)`：make 展开后的值会被拼进 shell 源码，命令行给出的值里的引号或分号会改写命令（`export AGENT_VERSION` 让配方环境里有它）。`$(VERSION)` 维持现状（它有 `check_version` 的逐字节检查）。
- 发布判定（spec §14.1）：tag vX、`AGENT_VERSION` vY；vY = vX（逐字相同）为 `full`；vY 是正式版且按 semver 优先级低于 vX 为 `hub-only`；其余都是错误。判定只实现在 `scripts/releasekind`，Makefile 与 release 流水线都调用它。
- 产物分组（spec §14.1）：hub 组 = `heron-hub_linux_{amd64,arm64}.tar.gz`、`heron-updater_linux_{amd64,arm64}.tar.gz`、`install-hub.sh`；agent 组 = `heron-agent_linux_{amd64,arm64,armv7,386,riscv64}.tar.gz`、`heron-updater_linux_{同五个}.tar.gz`、`heron-agent_darwin_{amd64,arm64}.tar.gz`、`install.sh`、`install-macos.sh`。完整 release 的 `dist/` 文件集合与改动前逐一相同。
- 官方下载目录：`https://github.com/xjetry/heron-probe/releases/download/<tag>/`。被签消息与验签只经 `internal/releasesig.Verify(keys, version, sums, sig)`；受信公钥只来自 `releasesig.Trusted()`，测试经参数注入 `internal/releasesig/sigtest` 的测试密钥，命令行不提供换公钥或换下载地址的开关。
- 生产公钥已在 `internal/releasesig/keys.go`（提交 24f8fb8），但还没有任何带签名的 release：v0.5.3 及更早没有签名，可绑定的版本要等拆分后第一个完整 release 发布之后才存在。在那之前任何真实的只发 hub 构建都在取 `SHA256SUMS.sig` 或验签处失败，这是预期；本计划的只发 hub 路径全部用 `sigtest` 测试密钥与本地 HTTP 服务验证。
- proto 字段号：`GetSnapshotResponse.bound_agent_version = 5`，`GetUpdatesResponse.bound_agent_version = 4`。
- 注释用中文，写 WHY 与不变式；提交信息 `<type>(<scope>): <中文一句话>`；`gen/` 与 `web/src/gen/` 只经 `make gen` 生成。
- 只有 task.md 的 Reservations 给了端口的任务才能跑 `make e2e` / `compat-e2e` / `web-e2e`，并且只用分到的端口（`E2E_HUB_PORT`、`E2E_HOOK_PORT`）。

## Review Focus

1. 只改 `admin.proto` 的 hub 功能：门禁必须放行（否则拆分形同虚设）——Task 4 的 `TestAdminOnlyGeneratedChangeIsNotAnAgentInput`。
2. agent 实际依赖的生成文件（`types.pb.go`，经 `agent.proto` 的 import 才进闭包）或只在 darwin 编译的 agent 文件变了：门禁必须拦下——Task 4 的 `TestImportedProtoChangeIsAnAgentInput`、`TestDarwinOnlyFileChangeIsAnAgentInput`。
3. 用户的典型场景：hub v0.5.6 绑定 agent v0.5.4，节点跑 v0.5.4——节点页不标落后、更新页不可选；节点跑 v0.5.3 才标落后、才可更新且目标是 v0.5.4——Task 2 的 `marks nodes against the bound agent version, not the hub version` 与 `targets the bound agent version without checking latest`。
4. 只发 hub 的 release 发布后，`releases/latest/download/install.sh` 装的是绑定版本：复制来的两个脚本与 vY 的逐字节相同、受 vY 的签名覆盖——Task 3 的 `TestFetchWritesVerifiedInstallers`、Task 5 的 `TestReadbackRejectsDifferentInstaller`。
5. 本地验收构建 `make release VERSION=v0.0.0-check` 忘了给 `AGENT_VERSION`：明确报错并说出该怎么给，而不是静默产出只发 hub 或绑错版本的包——Task 3 的 `TestKindRejectsLocalBuildWithoutAgentVersion`。

## 任务依赖与并行

| 任务 | 依赖 | 基点 | 说明 |
|---|---|---|---|
| Task 1 hub 侧绑定 | — | `7933dea` | 第一个提交只含 proto 与生成物，Task 2 从它开分支 |
| Task 2 面板 | Task 1 的 proto 提交 | 该提交 | |
| Task 3 发布配方、判定与绑定版本的安装脚本 | — | `7933dea` | 只发 hub 的配方调用 Task 4 的工具，集成前该路径只用 `make -n` 验 |
| Task 4 agent 输入门禁 | — | `7933dea` | 读 Task 3 定义的两个 make 目标（接口见 Task 4） |
| Task 5 流水线、绑定版本端到端与回读 | Task 3、Task 4 | 两者的集成分支 | |
| Task 6 集成与验收（控制端） | 全部 | — | 不派 worker |

---

### Task 1: hub 侧绑定（proto、Manager、API、启动日志、端到端断言）

**Files:**
- Modify: `proto/heron/v1/admin.proto`（`GetSnapshotResponse`、`GetUpdatesResponse`、`StartUpdate` 注释）；`gen/`、`web/src/gen/`（`make gen`）
- Modify: `internal/hub/updates/manager.go`、`internal/hub/updates/manager_test.go`
- Modify: `internal/hub/api/data.go`、`internal/hub/api/updates.go`、`internal/hub/api/service.go`（若需要辅助函数）及同包测试（`updates_test.go`、`changes_test.go`、新增用例所在文件）
- Modify: `internal/hub/ingest/relay_integration_test.go`（`updates.New` 调用点）
- Modify: `cmd/hub/serve.go`
- Modify: `scripts/e2e.sh`
- Modify: `proto/SKILL.md`

**Interfaces:**
- Consumes: `cmd/hub/main.go` 的 `var agentVersion string`（地基提交，构建时注入）。
- Produces:
  - `func New(st *store.Store, clk clock.Clock, log *slog.Logger, boundAgent string) *Manager`（`internal/hub/updates`）
  - `func (m *Manager) BoundAgent() string`
  - proto：`GetSnapshotResponse.bound_agent_version`（5）、`GetUpdatesResponse.bound_agent_version`（4）；TS 里是 `boundAgentVersion`。

绑定版本只由 `updates.Manager` 持有：API 下发的值读 `Manager.BoundAgent()`，不在 `api.Config` 另存一份（两份会漂）。`s.cfg.Updates` 为 nil（部分测试的装配）时下发空串——没有 Manager 就没有节点在线更新，与"没有绑定"一致。

- [ ] **Step 1: proto 与生成物（单独一个提交，Task 2 从它开分支）**

`GetSnapshotResponse` 改为：

```proto
message GetSnapshotResponse {
  // hub 墙钟，Unix 秒；客户端据此显示"多久之前"而不依赖自己的时钟。
  int64 now = 1;
  // hub 下发给 agent 的上报间隔；实时状态不会比它更新得更快。
  uint32 report_interval_ms = 2;
  repeated NodeStatus nodes = 3;
  // hub 构建版本（release 经 ldflags 注入，未注入为 dev）：面板据此生成该版本 release 的安装命令。
  string hub_version = 4;
  // hub 绑定的 agent 版本（spec §14.1）：构建时取自仓库根 AGENT_VERSION，未注入为空串，表示没有绑定。节点在线更新
  // 只能以它为目标，面板据此标出 agent 版本低于它的节点；该版本 release 的 install.sh 装的也是它。与
  // GetUpdatesResponse.bound_agent_version 同值。
  string bound_agent_version = 5;
}
```

`GetUpdatesResponse` 改为：

```proto
message GetUpdatesResponse {
  repeated UpdateTarget targets = 1;
  // 官方最新正式版，只在 check_latest 时查询：hub 自身更新的目标。
  string latest_version = 2;
  string check_error = 3;
  // hub 绑定的 agent 版本（spec §14.1）：节点 StartUpdate 唯一接受的 version，不需要 check_latest。空串表示这个
  // hub 没有绑定（没有注入的构建），节点在线更新不可用；是预发布时同样不可用（更新器只接受正式版）。
  string bound_agent_version = 4;
}
```

`StartUpdate` 的注释改为：

```proto
  // 创建单目标更新。node_id=0 为 hub，目标须是比 hub 当前版本新的官方正式版；节点的目标须等于
  // GetUpdatesResponse.bound_agent_version 且比节点当前版本新（spec §14.1）。
  // 节点离线时排队，24 小时过期；同一目标不能同时有多个活动任务。
```

Run: `cd <worktree> && make gen > <task-dir>/logs/gen.log 2>&1; echo $?` → `0`；`git status --short` 只列 `proto/heron/v1/admin.proto` 与生成物。

```bash
git add proto/heron/v1/admin.proto gen/heron/v1/admin.pb.go web/src/gen/heron/v1/admin_pb.ts
git commit -m "feat(proto): GetSnapshot 与 GetUpdates 下发 hub 绑定的 agent 版本"
```

（`git status` 列出的生成物若不止这两个，逐个加上；不用 `git add -A`。把这个提交的哈希写进 result.md 的 `## Summary` 第一行：`PROTO_COMMIT: <hash>`。）

- [ ] **Step 2: Manager 的失败用例**

`internal/hub/updates/manager_test.go` 的 `fixture(t)`（第 18–37 行：建库、建一个节点、`New(st, clk, log)`、`Observe` 成支持更新的 v0.2.0）改为 `fixtureBound(t, bound string)`，`New` 传入 `bound`；原 `fixture(t)` 改为调用 `fixtureBound(t, "v0.3.0")`——现有用例启动的版本是 v0.3.0。现有用例里启动别的版本、本来在测"拒绝"的（例如版本不比当前新），改用 `fixtureBound` 把绑定设成它启动的那个版本，确认它仍红在原来的原因上，而不是被绑定判断提前拒绝。加用例：

```go
func TestStartRejectsVersionOtherThanBound(t *testing.T) {
	m, id, _ := fixtureBound(t, "v0.3.0")
	_, err := m.Start(t.Context(), id, "v0.4.0")
	if err == nil || !strings.Contains(err.Error(), "bound agent version v0.3.0") {
		t.Fatalf("Start(v0.4.0) with bound v0.3.0 = %v, want bound-version error", err)
	}
	if task := m.Snapshot(id).Task; task != nil {
		t.Fatalf("rejected start left task %v", task)
	}
	if _, err := m.Start(t.Context(), id, "v0.3.0"); err != nil {
		t.Fatalf("Start(bound) = %v", err)
	}
}

func TestStartRejectsWithoutStableBound(t *testing.T) {
	for _, bound := range []string{"", "dev", "v0.3.0-rc.1"} {
		m, id, _ := fixtureBound(t, bound)
		_, err := m.Start(t.Context(), id, "v0.3.0")
		if err == nil || !strings.Contains(err.Error(), "no stable bound agent version") {
			t.Fatalf("bound %q: Start = %v, want no-stable-bound error", bound, err)
		}
		if task := m.Snapshot(id).Task; task != nil {
			t.Fatalf("bound %q: rejected start left task %v", bound, task)
		}
	}
}
```

Run: `cd <worktree> && go test -count=1 -run 'TestStartRejects' ./internal/hub/updates/ > <task-dir>/logs/t-red.log 2>&1; echo $?` → 非 0，红在编译错误（`New` 参数个数）或断言。

- [ ] **Step 3: Manager 实现**

`internal/hub/updates/manager.go`：

```go
type Manager struct {
	// ……现有字段……

	// boundAgent 是 hub 绑定的 agent 版本（spec §14.1），节点在线更新唯一可用的目标；New 之后不变。
	// API 下发的 bound_agent_version 也读它（BoundAgent），绑定只有这一个持有者。
	boundAgent string
}

func New(st *store.Store, clk clock.Clock, log *slog.Logger, boundAgent string) *Manager {
	// ……现有构造，加上 boundAgent: boundAgent……
}

// BoundAgent 返回 hub 绑定的 agent 版本，空串表示没有绑定。
func (m *Manager) BoundAgent() string { return m.boundAgent }
```

`Start` 开头（取 `m.op` 之前）：

```go
	// 节点只能更新到 hub 绑定的 agent 版本（spec §14.1）：只发 hub 的 release 不带 agent 产物，别的版本号在官方
	// release 里不一定有 agent 包，也没有与这个 hub 一起跑过端到端。绑定为空（没有注入的构建）或不是正式版时，
	// 节点在线更新一律不可用——更新器只接受正式版（update.ValidVersion），空值在这里是收紧。
	if !update.ValidVersion(m.boundAgent) {
		return nil, errors.New("this hub has no stable bound agent version; node online updates are unavailable")
	}
	if version != m.boundAgent {
		return nil, fmt.Errorf("node updates must target this hub's bound agent version %s, got %s", m.boundAgent, version)
	}
```

更新全部调用点：`cmd/hub/serve.go` 的 `updates.New(st, clk, log)` → `updates.New(st, clk, log, agentVersion)`；测试里的 `New(...)` / `updates.New(...)`（`manager_test.go` 第 30、60、262 行附近，`internal/hub/api/changes_test.go` 两处，`internal/hub/ingest/relay_integration_test.go` 一处）按各自用例实际启动的版本传绑定值——用例里 `Start` 的版本是什么，绑定就给什么；用例本来就在测"拒绝"的，确认它仍红在原来的原因上。

Run: `cd <worktree> && go test -count=1 ./internal/hub/updates/ ./internal/hub/ingest/ > <task-dir>/logs/t-mgr.log 2>&1; echo $?` → `0`。

- [ ] **Step 4: API 下发绑定版本的失败用例**

在 `internal/hub/api` 现有的 GetSnapshot / GetUpdates / StartUpdate / ExecuteChange 测试旁加用例（沿用该包的 harness：看 `updates_test.go` 怎样装配带 `Updates` 的 Service，`changes_test.go` 第 806 行附近怎样装配 Manager）：

1. `TestSnapshotAndUpdatesCarryBoundAgentVersion`：Manager 绑定 `v1.2.3` → `GetSnapshot` 的 `BoundAgentVersion == "v1.2.3"`；`GetUpdates`（`check_latest` 为假，也为真时各一次——为真时 harness 的 `updateSource` 用假源）的 `BoundAgentVersion == "v1.2.3"`。
2. `TestSnapshotWithoutUpdatesHasNoBoundAgentVersion`：`cfg.Updates` 为 nil 的 Service → 两个响应的 `BoundAgentVersion` 都是空串。
3. `TestStartUpdateRejectsNonBoundVersion`：节点支持更新、当前 v1.2.0、绑定 v1.2.3；`StartUpdate{node_id: 7, version: "v1.2.4"}` → `connect.CodeFailedPrecondition`，消息含 `bound agent version v1.2.3`。
4. `TestExecuteChangeStartUpdateRejectsNonBoundVersion`：同样的条件经 `ExecuteChange` 的 `start_update`，`preview` 为真与为假各一次，都被拒且没有提交回执（`ListOperations` 里没有这条）。

Run: 跑这四个用例 → 红（字段不存在或值为空）。

- [ ] **Step 5: API 实现**

`internal/hub/api/service.go` 加：

```go
// boundAgent 是下发给面板与 API 的 hub 绑定 agent 版本（spec §14.1），只读 updates.Manager：绑定只有它一个持有者。
// 没有 Manager 的装配下没有节点在线更新，下发空串，与"没有绑定"同义。
func (s *Service) boundAgent() string {
	if s.cfg.Updates == nil {
		return ""
	}
	return s.cfg.Updates.BoundAgent()
}
```

`data.go` 的 `GetSnapshot` 构造响应时加 `BoundAgentVersion: s.boundAgent()`；`updates.go` 的 `GetUpdates` 改为 `out := &heronv1.GetUpdatesResponse{BoundAgentVersion: s.boundAgent()}`。

Run: Step 4 的四个用例与 `go test -count=1 ./internal/hub/api/` → `0`。

- [ ] **Step 6: 启动日志**

`cmd/hub/serve.go` 第 281 行附近 `log.Info("hub listening", …)` 的字段里，在 `"version", version` 之后加 `"agent_version", agentVersion`。

- [ ] **Step 7: 端到端断言注入确实生效**

`scripts/e2e.sh` 第 313 行的快照形状断言改为同时核对绑定版本（e2e 的 hub 由 `make binaries` → `hub-binary` 构建，带 `-X main.agentVersion`；`compat-e2e` 复用同一脚本，hub 同样经 `hub-binary` 构建）：

```sh
# hub 绑定的 agent 版本经 hub-binary 的 ldflags 注入（spec §14.1）。变量改名或注入断开时链接器静默忽略 -X，
# hub 就没有绑定、节点在线更新全部被拒，编译与单元测试都照不出来，只有从真实二进制回读才看得见。
bound=$(sed -n 1p "$root/AGENT_VERSION")
jq -e --arg bound "$bound" '.reportIntervalMs == 4000 and all(.nodes[]; .metrics.cpuPct != null) and .boundAgentVersion == $bound' "$work/GetSnapshot.json" > /dev/null || { echo "FAIL: snapshot shape or bound agent version (want $bound)"; cat "$work/GetSnapshot.json"; exit 1; }
```

先确认脚本里仓库根的变量名（`root` 或别的），按实际名字写。

Run（端口取 task.md 的 Reservations）：`cd <worktree> && E2E_HUB_PORT=<p1> E2E_HOOK_PORT=<p2> make e2e > <task-dir>/logs/e2e.log 2>&1; echo $?` → `0`。

缺陷注入：把 Makefile `hub-binary` 里的 `main.agentVersion` 临时改成 `main.agentVersionX`，重跑 `make e2e` → 红在 `FAIL: snapshot shape or bound agent version`；恢复后确认 `git diff Makefile` 为空。

- [ ] **Step 8: SKILL.md**

`proto/SKILL.md` 第 16 行（`GetUpdates` 一条）在"`checkLatest: true` 显式查询官方最新正式版"之后补一句：`boundAgentVersion` 是 hub 绑定的 agent 版本，节点更新（`ExecuteChange.startUpdate`）的 `version` 只能是它，查询它不需要 `checkLatest`。

- [ ] **Step 9: 全量与提交**

Run: `cd <worktree> && make lint > <task-dir>/logs/lint.log 2>&1; echo $?` → `0`；`go test -race -count=1 ./internal/hub/updates/ ./internal/hub/api/ ./internal/hub/ingest/ > <task-dir>/logs/race.log 2>&1; echo $?` → `0`；最后 `make ci > <task-dir>/logs/ci.log 2>&1; echo $?` → `0`。

```bash
git add internal/hub/updates/manager.go internal/hub/updates/manager_test.go
git commit -m "feat(updates): 节点在线更新只接受 hub 绑定的 agent 版本"
git add internal/hub/api/ cmd/hub/serve.go internal/hub/ingest/relay_integration_test.go
git commit -m "feat(api): GetSnapshot 与 GetUpdates 下发 hub 绑定的 agent 版本"
git add scripts/e2e.sh proto/SKILL.md
git commit -m "test(e2e): 从真实 hub 回读绑定的 agent 版本"
```

（`git add internal/hub/api/` 前用 `git status --short internal/hub/api/` 确认只有本任务的文件。）

缺陷注入（记入 result.md 的 `## Fault injection`）：(a) `Start` 去掉 `version != m.boundAgent` 判断 → `TestStartRejectsVersionOtherThanBound` 与 `TestStartUpdateRejectsNonBoundVersion` 红；(b) `ValidVersion(m.boundAgent)` 判断改成 `m.boundAgent != ""` → `TestStartRejectsWithoutStableBound` 的 `dev`、`v1.3.0-rc.1` 两项红；(c) `GetUpdates` 不填 `BoundAgentVersion` → `TestSnapshotAndUpdatesCarryBoundAgentVersion` 红；(d) Step 7 的注入。

---
### Task 2: 面板按绑定版本标落后、选目标，安装命令写明绑定版本

**Files:**
- Modify: `web/src/lib/version.ts`、`web/src/lib/version.test.ts`
- Modify: `web/src/pages/Nodes.tsx`、`web/src/pages/Nodes.test.tsx`
- Modify: `web/src/pages/Updates.tsx`、`web/src/pages/Updates.test.tsx`
- Modify: `web/src/components/InstallCommands.tsx`、`web/src/components/NodeInstallModal.tsx`、`web/src/pages/RegisterWindow.tsx`、`web/src/pages/RegisterWindow.test.tsx`
- Modify: `README.md`（第 246–262 行附近"安装命令"一节）

**Interfaces:**
- Consumes: Task 1 的 proto 提交（`PROTO_COMMIT`）：`GetSnapshotResponse.boundAgentVersion`、`GetUpdatesResponse.boundAgentVersion`（TS 字段名）。
- Produces: `olderThan(current: string | undefined, target: string): boolean`、`isStableRelease(v: string): boolean`（`web/src/lib/version.ts`）。

本任务不改 Go 与 proto；分支从 `PROTO_COMMIT` 开（orchestrator 在 task.md 里给出哈希）。

- [ ] **Step 1: version.ts 的失败用例**

`lagsHub` 比较的从来不是"hub"，而是"当前版本是否低于目标版本"；目标从 hub 版本换成绑定版本后，名字按语义改为 `olderThan(current, target)`，比较规则不变（semver 2.0 优先级）。另加 `isStableRelease`：节点在线更新只接受正式版（hub 的 `update.ValidVersion`：`vMAJOR.MINOR.PATCH`，无预发布、无构建元数据）。

`web/src/lib/version.test.ts`：把 `lagsHub` 的三组用例原样改成 `olderThan`（参数顺序不变），再加：

```ts
describe("isStableRelease 与 hub 的 ValidVersion 同一口径", () => {
  it.each(["v0.5.4", "v1.0.0", "v10.20.30"])("%s 是正式版", (v) => {
    expect(isStableRelease(v)).toBe(true);
  });
  it.each(["", "dev", "v0.5.4-rc.1", "v0.5.4+b.1", "0.5.4", "v1.0", "v01.0.0"])("%s 不是正式版", (v) => {
    expect(isStableRelease(v)).toBe(false);
  });
});
```

Run: `cd <worktree> && pnpm --dir web exec vitest run src/lib/version.test.ts > <task-dir>/logs/v-red.log 2>&1; echo $?` → 非 0。

- [ ] **Step 2: version.ts 实现**

```ts
// 当前版本按 semver 2.0 优先级低于目标版本才算落后；更高或相同不标。任一方解析不了（dev、空、格式不对）
// 就没有可比的次序，不标。节点与 hub 绑定的 agent 版本比（spec §14.1），hub 与官方最新版比，都用它。
export function olderThan(current: string | undefined, target: string): boolean {
  const a = current ? parse(current) : null;
  const t = parse(target);
  return a !== null && t !== null && compare(a, t) < 0;
}

// 正式版：vMAJOR.MINOR.PATCH，无预发布、无构建元数据——与 hub 的 update.ValidVersion 同一口径。节点在线更新
// 只接受正式版，绑定版本不是正式版时面板不提供节点更新。
export function isStableRelease(v: string): boolean {
  const p = parse(v);
  return p !== null && p.pre.length === 0 && !v.includes("+");
}
```

删除 `lagsHub`；`isRelease` 的注释把"与 lagsHub 用同一个 parse"改成"与 olderThan 用同一个 parse"。全部调用点（`Nodes.tsx`、`Updates.tsx`）改用 `olderThan`。

Run: 同 Step 1 → `0`。

- [ ] **Step 3: 节点页的失败用例**

`web/src/pages/Nodes.test.tsx` 第 23 行的夹具改为 `const snapshotOf = (hubVersion: string, boundAgentVersion = "") => async () => ({ now: 1n, reportIntervalMs: 4000, nodes: [], hubVersion, boundAgentVersion });`。第 133 行附近现有的落后标记用例改为按绑定版本给值（标记出现与否由 `boundAgentVersion` 决定，`hubVersion` 不再参与）。加用例（`renderNodes`、`agentAt` 是文件里现有的辅助；两个节点用 `agentAt` 的写法造出不同的 `id`）：

```ts
it("marks nodes against the bound agent version, not the hub version", async () => {
  // hub v0.5.6 只改了 hub，绑定 agent v0.5.4：跑 v0.5.4 的节点不落后，跑 v0.5.3 的才落后。
  const nodes = [{ ...agentAt("v0.5.4")[0], id: 1n, name: "current" }, { ...agentAt("v0.5.3")[0], id: 2n, name: "behind" }];
  renderNodes({ listNodes: async () => ({ nodes }), getSnapshot: snapshotOf("v0.5.6", "v0.5.4") });
  expect(await screen.findByText("agent 低于 v0.5.4")).toBeInTheDocument();
  expect(screen.getAllByText("agent 低于 v0.5.4")).toHaveLength(1);
});
it.each(["", "dev"])("no lagging marker when the hub has no stable binding ('%s')", async (bound) => {
  renderNodes({ listNodes: async () => ({ nodes: agentAt("v0.5.3") }), getSnapshot: snapshotOf("v0.5.6", bound) });
  await screen.findByText(/节点清单/);
  expect(screen.queryByText(/agent 低于/)).toBeNull();
});
```

`renderNodes` 需要的其余方法（`listTags` 等）照文件里现有用例补齐。

- [ ] **Step 4: 节点页实现**

`Nodes.tsx`：`const bound = snapshot.data?.boundAgentVersion;`，`NodeRow` 的 `hubVersion` 属性改名 `boundAgentVersion`，标记改为：

```tsx
{boundAgentVersion !== undefined && olderThan(node.facts?.agentVersion, boundAgentVersion) &&
  <span className="node-subtext warn" title={`低于 hub 绑定的 agent 版本 ${boundAgentVersion}，可在在线更新页更新`}>agent 低于 {boundAgentVersion}</span>}
```

第 161 行快照错误提示里的"落后标记"文案改为按绑定版本说：取不到时"无法取得 hub 绑定的 agent 版本，落后标记不可用"，刷新失败时"刷新失败，落后标记按上次取得的绑定版本 ${bound || "空"} 判断"。安装弹窗（`NodeInstallModal`）照旧拿 `hubVersion`，另传 `boundAgentVersion`（Step 7）。

Run: `pnpm --dir web exec vitest run src/pages/Nodes.test.tsx` → `0`。

- [ ] **Step 5: 更新页的失败用例**

`web/src/pages/Updates.test.tsx`：现有夹具 `getUpdates` 返回 `{ targets, latestVersion }`，加 `boundAgentVersion`。加用例：

1. `targets the bound agent version without checking latest`：`boundAgentVersion: "v0.5.4"`，节点 A 支持更新、版本 v0.5.3，节点 B v0.5.4；不点"检查官方新版本"，A 的勾选框可用、B 不可用；勾 A 点"更新选中节点"，确认框写"更新到 v0.5.4"，确认后 `startUpdate` 收到 `{ nodeId: A, version: "v0.5.4" }`。
2. `does not offer node updates without a stable binding`：`boundAgentVersion` 为 `""` 与 `"v0.5.4-rc.1"` 各一次 → 节点勾选框全部禁用，页面有说明"这个 hub 没有绑定正式的 agent 版本"。
3. `hub update still targets the latest official version`：点"检查官方新版本"得到 `latestVersion: "v0.5.6"`、hub 当前 v0.5.5 → "更新 Hub"可用，确认框写 v0.5.6；节点目标仍是 v0.5.4。

- [ ] **Step 6: 更新页实现**

`Updates.tsx`：

```tsx
const bound = updates.data!.boundAgentVersion;
// 节点只能更新到 hub 绑定的 agent 版本（spec §14.1），hub 的 StartUpdate 按同一条件拒绝别的版本；绑定不是正式版
// （空串、开发构建、预发布）时不提供节点更新。hub 自身仍以官方最新正式版为目标，那个才需要"检查官方新版本"。
const nodeTarget = isStableRelease(bound) ? bound : "";
```

节点的 `eligible(status, nodeTarget)`、全选、勾选框、"更新选中节点"与确认框都用 `nodeTarget`；hub 的卡片与按钮仍用 `latest`。`eligible` 内部的比较改用 `olderThan`。节点区标题旁写"目标版本 {nodeTarget}（hub 绑定的 agent 版本）"；`nodeTarget` 为空时写"这个 hub 没有绑定正式的 agent 版本（开发构建或预发布），不能在线更新节点"。更新页顶部第 74 行附近"只安装 xjetry/heron-probe 正式 Release……"的说明后补一句"节点更新到 hub 绑定的 agent 版本；只改 hub 的版本不要求节点升级"。

Run: `pnpm --dir web exec vitest run src/pages/Updates.test.tsx` → `0`。

- [ ] **Step 7: 安装命令写明绑定版本**

`InstallCommands` 增加必填属性 `boundAgentVersion: string`。脚本地址规则不变（hub 为带 `v` 的合法 semver 时取该版本 release 的 `install.sh`，否则取 latest）——只发 hub 的 release 里的 `install.sh` 是绑定版本的脚本（spec §14.1），地址本身不用改。说明文字：

```tsx
{isRelease(hubVersion)
  ? <p className="muted">脚本取自 hub {hubVersion} 的 release，安装 hub 绑定的 agent {boundAgentVersion || "（未知）"}。</p>
  : <p className="muted">hub 不是正式版本（{hubVersion || "未知"}），脚本取自最新 release，将安装最新 release 绑定的 agent。</p>}
```

文件顶部的注释改为陈述新口径：每个 release 的 `install.sh` 装的是该 hub 版本绑定的 agent（只发 hub 的 release 复制了绑定版本的脚本，spec §14.1），所以按 hub 版本取脚本即装上绑定的 agent。`NodeInstallModal`、`RegisterWindow` 把 `snapshot.data.boundAgentVersion` 传进去（两者已在 `hubVersion` 到达前不渲染命令区，绑定版本同一个响应带来）。

`RegisterWindow.test.tsx`：夹具 `snapshotOf` 加 `boundAgentVersion`，加用例：hub `v0.5.6`、绑定 `v0.5.4` → 命令地址含 `releases/download/v0.5.6/install.sh`，说明含"安装 hub 绑定的 agent v0.5.4"。

- [ ] **Step 8: README**

`README.md` 安装命令一节（第 246–262 行附近）：
- 第 250 行"脚本只装自己所属的版本……"改为：`install-hub.sh` 只装自己所属的 hub 版本；`install.sh` 与 `install-macos.sh` 装的是该 release 的 hub 绑定的 agent 版本（只发 hub 的 release 原样带着绑定版本的这两个脚本），`releases/latest/download/install.sh` 因而装最新 hub 绑定的 agent；要装指定的 agent 版本，取那个 agent 版本自己的 release 里的脚本。
- 第 256 行括号里"装上的 agent 与 hub 同版本"改为"装上的是该 hub 版本绑定的 agent"。
- 在这一节末尾加一段"hub 与 agent 的版本"：每个 hub 版本绑定一个 agent 版本；agent 版本号是它最后一次变动时的 release 版本，只改 hub 的版本不要求节点升级；面板按绑定版本标落后、在线更新节点只到绑定版本。

- [ ] **Step 9: 全量与提交**

Run: `cd <worktree> && pnpm --dir web exec vitest run > <task-dir>/logs/vitest.log 2>&1; echo $?` → `0`；`make lint > <task-dir>/logs/lint.log 2>&1; echo $?` → `0`；`make ci > <task-dir>/logs/ci.log 2>&1; echo $?` → `0`。

```bash
git add web/src/lib/version.ts web/src/lib/version.test.ts
git commit -m "refactor(web): 版本落后判定按目标版本比较，另给出正式版判定"
git add web/src/pages/Nodes.tsx web/src/pages/Nodes.test.tsx web/src/pages/Updates.tsx web/src/pages/Updates.test.tsx
git commit -m "feat(web): 节点落后标记与在线更新目标按 hub 绑定的 agent 版本"
git add web/src/components/InstallCommands.tsx web/src/components/NodeInstallModal.tsx web/src/pages/RegisterWindow.tsx web/src/pages/RegisterWindow.test.tsx README.md
git commit -m "feat(web): 安装命令写明装的是 hub 绑定的 agent 版本"
```

缺陷注入（记入 `## Fault injection`）：(a) 节点页仍按 `hubVersion` 比较 → `marks nodes against the bound agent version` 红；(b) 更新页节点目标仍用 `latest` → `targets the bound agent version without checking latest` 红；(c) `isStableRelease` 放过预发布 → version 用例与更新页 `v0.5.4-rc.1` 一项红。

---
### Task 3: 发布配方按种类分两路，判定只在 releasekind，只发 hub 时取绑定版本的安装脚本

**Files:**
- Create: `deploy/agent.mk`
- Modify: `Makefile`（`include`、移走 agent 组变量、`release` 拆成 `release` / `release-full` / `release-hub-only`、新增 `release-kind`、`lint` 核对 `AGENT_VERSION` 格式）
- Modify: `internal/update/version.go`、`internal/update/version_test.go`（`ReleaseTag`）
- Create: `scripts/releasekind/main.go`、`scripts/releasekind/main_test.go`
- Create: `scripts/boundagent/main.go`、`scripts/boundagent/fetch.go`、`scripts/boundagent/fetch_test.go`
- Modify: `scripts/release-rules-test.sh`
- Modify: `scripts/install-accept.sh`（第 86、89 行）、`scripts/macos-accept.sh`（第 48 行）、`README.md`（第 281 行附近 `v0.0.0-check`）

**Interfaces:**
- Consumes: 地基提交的 `AGENT_VERSION` 变量（已导出到配方环境）、`HUB_GOFLAGS`、`hub_build`。
- Produces:
  - `func ReleaseTag(tag string) (core string, prerelease bool, ok bool)`（`internal/update`）
  - `go run ./scripts/releasekind -version vX -agent vY` → stdout 一行 `full` 或 `hub-only`，退出 0；不合规则退出 1、stderr 说明；参数错误退出 2。`-agent vY -check` 只核对格式。
  - `make -s release-kind VERSION=vX` → 同上一行输出（`AGENT_VERSION` 默认取仓库文件）。
  - `make -s agent-bundle-inputs` → agent 组打包输入的文件路径（相对仓库根），一行一个，含 `deploy/agent.mk` 自己。
  - `make -s agent-go-targets` → agent 组 Go 构建的（包、平台），一行一个，字段以空白分隔：`<包路径> <GOOS> <GOARCH> [<GOARM>]`，如 `./cmd/agent linux arm 7`、`./cmd/agent darwin arm64`。Task 4 只按这个格式读。
  - `go run ./scripts/boundagent fetch -version vY -dir DIR -linux-arches "<空格分隔>" -darwin-arches "<空格分隔>"`；包内函数 `fetchRelease`、`parseSums`、`agentBundle`（签名见 Step 6），Task 5 复用。
  - `release-hub-only` 配方调用 `go run ./scripts/agentinputs -base "$$AGENT_VERSION"`（Task 4 提供；本任务内这条路径只用 `make -n` 验）。

- [ ] **Step 1: `ReleaseTag` 的失败用例**

`internal/update/version_test.go` 加：

```go
func TestReleaseTag(t *testing.T) {
	for _, c := range []struct {
		tag, core string
		pre, ok   bool
	}{
		{"v1.2.3", "v1.2.3", false, true},
		{"v1.2.3-rc.1", "v1.2.3", true, true},
		{"v1.2.3-0", "v1.2.3", true, true},
		{"v1.2.3-alpha-1.x.7", "v1.2.3", true, true},
		{"", "", false, false},
		{"1.2.3", "", false, false},
		{"v1.2", "", false, false},
		{"v01.2.3", "", false, false},
		{"v1.2.3-", "", false, false},
		{"v1.2.3-rc..1", "", false, false},
		{"v1.2.3-01", "", false, false},
		{"v1.2.3-rc_1", "", false, false},
		{"v1.2.3+b.1", "", false, false},
		{"v1.2.3-rc.1+b.1", "", false, false},
		{"dev", "", false, false},
	} {
		core, pre, ok := ReleaseTag(c.tag)
		if core != c.core || pre != c.pre || ok != c.ok {
			t.Errorf("ReleaseTag(%q) = %q, %v, %v; want %q, %v, %v", c.tag, core, pre, ok, c.core, c.pre, c.ok)
		}
	}
}
```

Run: `cd <worktree> && go test -count=1 -run TestReleaseTag ./internal/update/ > <task-dir>/logs/tag-red.log 2>&1; echo $?` → 非 0（未定义）。

- [ ] **Step 2: `ReleaseTag` 实现**

`internal/update/version.go` 加：

```go
// ReleaseTag 解析 release 的 tag：vMAJOR.MINOR.PATCH，或其后带 semver 预发布后缀 -<点分标识符>。返回去掉预发布
// 后缀的正式版本与是否为预发布。构建元数据（+）不接受：check_version 拒绝它，release 的 tag 里不会出现。标识符
// 只含 [0-9A-Za-z-] 且不为空，纯数字的不带前导 0（semver 2.0 第 9 条）。正式版部分与 ValidVersion 同一个解析。
func ReleaseTag(tag string) (core string, prerelease bool, ok bool) {
	core, pre, hasPre := strings.Cut(tag, "-")
	if !ValidVersion(core) {
		return "", false, false
	}
	if !hasPre {
		return core, false, true
	}
	for _, id := range strings.Split(pre, ".") {
		if id == "" {
			return "", false, false
		}
		numeric := true
		for _, ch := range id {
			switch {
			case ch >= '0' && ch <= '9':
			case ch >= 'a' && ch <= 'z', ch >= 'A' && ch <= 'Z', ch == '-':
				numeric = false
			default:
				return "", false, false
			}
		}
		if numeric && len(id) > 1 && id[0] == '0' {
			return "", false, false
		}
	}
	return core, true, true
}
```

Run: Step 1 的命令 → `0`。

- [ ] **Step 3: `releasekind` 的失败用例**

`scripts/releasekind/main_test.go`：

```go
func TestKind(t *testing.T) {
	for _, c := range []struct{ version, agent, want, errHas string }{
		{"v1.2.3", "v1.2.3", "full", ""},
		{"v1.3.0-rc.1", "v1.3.0-rc.1", "full", ""},
		{"v1.2.4", "v1.2.3", "hub-only", ""},
		{"v1.3.0-rc.1", "v1.2.3", "hub-only", ""},
		{"v2.0.0", "v1.9.9", "hub-only", ""},
		{"v1.2.3", "v1.2.4", "", "not lower than VERSION"},
		{"v1.3.0-rc.1", "v1.3.0", "", "not lower than VERSION"},
		{"v1.2.4", "v1.2.3-rc.1", "", "prerelease"},
		{"v1.2.4", "", "", "AGENT_VERSION"},
		{"v1.2.4", "dev", "", "AGENT_VERSION"},
		{"dev", "v1.2.3", "", "VERSION"},
	} {
		got, err := kind(c.version, c.agent)
		if c.errHas == "" && (err != nil || got != c.want) {
			t.Errorf("kind(%q, %q) = %q, %v; want %q", c.version, c.agent, got, err, c.want)
		}
		if c.errHas != "" && (err == nil || !strings.Contains(err.Error(), c.errHas)) {
			t.Errorf("kind(%q, %q) = %q, %v; want error containing %q", c.version, c.agent, got, err, c.errHas)
		}
	}
}

// 本地验收构建只给 VERSION 时，判定必须失败并说出该怎么给，而不是静默产出只发 hub 的包或绑错版本。
func TestKindRejectsLocalBuildWithoutAgentVersion(t *testing.T) {
	_, err := kind("v0.0.0-check", "v0.5.3")
	if err == nil || !strings.Contains(err.Error(), "AGENT_VERSION=$VERSION") {
		t.Fatalf("kind(v0.0.0-check, v0.5.3) = %v, want a hint to pass AGENT_VERSION=$VERSION", err)
	}
}

func TestRunCheckValidatesAgentFormatOnly(t *testing.T) {
	var out, errb bytes.Buffer
	if code := run([]string{"-agent", "v0.5.3", "-check"}, &out, &errb); code != 0 || out.Len() != 0 {
		t.Fatalf("check v0.5.3: code %d out %q err %q", code, out.String(), errb.String())
	}
	if code := run([]string{"-agent", "v0.5", "-check"}, &out, &errb); code != 1 {
		t.Fatalf("check v0.5: code %d, want 1", code)
	}
	if code := run([]string{"-version", "v1.2.4", "-agent", "v1.2.3"}, &out, &errb); code != 0 || out.String() != "hub-only\n" {
		t.Fatalf("kind run: code %d out %q", code, out.String())
	}
	if code := run([]string{"-bogus"}, &out, &errb); code != 2 {
		t.Fatalf("bad flag: code %d, want 2", code)
	}
}
```

Run: `go test -count=1 ./scripts/releasekind/` → 红（未定义）。

- [ ] **Step 4: `releasekind` 实现**

`scripts/releasekind/main.go`：

```go
// Command releasekind 按 spec §14.1 的唯一规则判定一次 release 的种类；Makefile 的 release、release-kind 与
// release 流水线都调用它，规则不在别处另写。
//
//	releasekind -version vX -agent vY   打印 full 或 hub-only；不合规则退出 1 并说明
//	releasekind -agent vY -check        只核对 AGENT_VERSION 的格式（make lint 调用）
package main

import (
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/xjetry/heron-probe/internal/update"
)

func main() { os.Exit(run(os.Args[1:], os.Stdout, os.Stderr)) }

func run(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("releasekind", flag.ContinueOnError)
	fs.SetOutput(stderr)
	version := fs.String("version", "", "release tag (VERSION)")
	agent := fs.String("agent", "", "bound agent version (AGENT_VERSION)")
	check := fs.Bool("check", false, "only validate -agent")
	if err := fs.Parse(args); err != nil || fs.NArg() != 0 {
		fmt.Fprintln(stderr, "usage: releasekind -version vX -agent vY | releasekind -agent vY -check")
		return 2
	}
	if *check {
		if _, _, ok := update.ReleaseTag(*agent); !ok {
			fmt.Fprintf(stderr, "releasekind: AGENT_VERSION %q is not a release tag vMAJOR.MINOR.PATCH[-PRERELEASE]\n", *agent)
			return 1
		}
		return 0
	}
	k, err := kind(*version, *agent)
	if err != nil {
		fmt.Fprintln(stderr, "releasekind:", err)
		return 1
	}
	fmt.Fprintln(stdout, k)
	return 0
}

// kind：vY 与 vX 逐字相同为完整 release；vY 是正式版且按 semver 优先级低于 vX 为只发 hub（vX 是预发布时
// 比较它的正式版部分：vC 高于 vC-rc，低于 vC 的正式版都低于 vC-rc）；其余都不合规则。只发 hub 只能绑定正式版：
// 被绑定的版本要有签名与完整的 agent 组，预发布不被更新器接受（update.ValidVersion）。
func kind(version, agent string) (string, error) {
	versionCore, _, ok := update.ReleaseTag(version)
	if !ok {
		return "", fmt.Errorf("VERSION %q is not a release tag vMAJOR.MINOR.PATCH[-PRERELEASE]", version)
	}
	agentCore, agentPre, ok := update.ReleaseTag(agent)
	if !ok {
		return "", fmt.Errorf("AGENT_VERSION %q is not a release tag vMAJOR.MINOR.PATCH[-PRERELEASE]", agent)
	}
	if agent == version {
		return "full", nil
	}
	if agentPre {
		return "", fmt.Errorf("AGENT_VERSION %s is a prerelease and differs from VERSION %s: a hub-only release binds only a stable agent release; for a full release set AGENT_VERSION=%s", agent, version, version)
	}
	if update.Newer(versionCore, agentCore) {
		return "hub-only", nil
	}
	return "", fmt.Errorf("AGENT_VERSION %s is not lower than VERSION %s: a hub-only release binds an earlier agent release; for a full release set AGENT_VERSION=%s (local acceptance builds pass AGENT_VERSION=$VERSION)", agent, version, version)
}
```

Run: `go test -count=1 ./scripts/releasekind/ ./internal/update/` → `0`。

- [ ] **Step 5: agent 组的 makefile 片段**

新建 `deploy/agent.mk`，把 Makefile 里的 `AGENT_LINUX_ARCHES`、`agent_goarch`、`AGENT_DARWIN_ARCHES`、`RELEASE_LDFLAGS`、`RELEASE_GOFLAGS` 连同注释移进来（Makefile 里删掉，不留第二份），并加上打包输入与配方片段：

```make
# agent 组（spec §14.1）：架构表、构建参数、打包配方与打包输入。只发 hub 的门禁（scripts/agentinputs）把这个
# 文件本身与 AGENT_BUNDLE_FILES 当作 agent 组的打包输入，把 agent-go-targets 列出的（包、平台）展开成 Go 源码
# 输入。配方只经这里的变量引用 deploy/ 下的文件：写死在配方里的路径门禁看不见，scripts/release-rules-test.sh
# 核对这个文件里出现的每个 deploy/ 路径都登记在 AGENT_BUNDLE_FILES 里。

# 发布产物矩阵：agent 与更新器五个 Linux 架构，agent 两个 darwin 架构（hub 的两个在 Makefile 的 HUB_LINUX_ARCHES）。
# 架构集合只在这几个变量维护，静态门禁、打包清单与门禁的平台清单都由它们展开，不存在第二份清单。
AGENT_LINUX_ARCHES := amd64 arm64 armv7 386 riscv64
AGENT_DARWIN_ARCHES := amd64 arm64
# agent_goarch 把 shell 变量 $$arch（AGENT_LINUX_ARCHES 的一项）映射成 $$goarch、$$goarm 与环境变量串 $$gflags。
# 架构名与 GOARCH 不同名的只有 armv7；打包、build 的编译检查与 agent-go-targets 共用这一处映射。
agent_goarch = case $$arch in armv7) goarch=arm goarm=7 ;; *) goarch=$$arch goarm= ;; esac; gflags="GOARCH=$$goarch$${goarm:+ GOARM=$$goarm}"

# 发布产物的构建参数（spec §14）：版本经 ldflags 注入，-trimpath 去掉构建机路径。hub 的 HUB_GOFLAGS 也由
# RELEASE_LDFLAGS 组成（Makefile），任何一种产物的版本注入都不会单独漂移。
RELEASE_LDFLAGS = -X main.version=$(VERSION)
RELEASE_GOFLAGS = -trimpath -ldflags "$(RELEASE_LDFLAGS)"

AGENT_SYSTEMD_UNIT := deploy/systemd/heron-agent.service
AGENT_OPENRC_SCRIPT := deploy/openrc/heron-agent
AGENT_LAUNCHD_PLIST := deploy/launchd/xyz.heron.agent.plist
UPDATER_UNITS := deploy/systemd/heron-updater-agent.service deploy/systemd/heron-updater-hub.service
AGENT_INSTALLERS := deploy/install.sh deploy/install-macos.sh
AGENT_BUNDLE_FILES := deploy/agent.mk $(AGENT_SYSTEMD_UNIT) $(AGENT_OPENRC_SCRIPT) $(AGENT_LAUNCHD_PLIST) $(UPDATER_UNITS) $(AGENT_INSTALLERS)

# agent 组的二进制（完整 release）：Linux agent 五个架构、darwin agent 两个架构。
agent_build = for arch in $(AGENT_LINUX_ARCHES); do \
	  $(agent_goarch); \
	  env GOOS=linux CGO_ENABLED=0 $$gflags go build $(RELEASE_GOFLAGS) -o "dist/build/heron-agent-linux-$$arch" ./cmd/agent; \
	done; \
	for arch in $(AGENT_DARWIN_ARCHES); do \
	  env GOOS=darwin GOARCH=$$arch CGO_ENABLED=0 go build $(RELEASE_GOFLAGS) -o "dist/build/heron-agent-darwin-$$arch" ./cmd/agent; \
	done
AGENT_STATIC = $(addprefix dist/build/heron-agent-linux-,$(AGENT_LINUX_ARCHES))
agent_pack = for arch in $(AGENT_LINUX_ARCHES); do \
	  pkg="dist/pkg-$$arch"; mkdir -p "$$pkg"; \
	  cp "dist/build/heron-agent-linux-$$arch" "$$pkg/heron-agent"; \
	  cp $(AGENT_SYSTEMD_UNIT) "$$pkg/heron-agent.service"; \
	  cp $(AGENT_OPENRC_SCRIPT) "$$pkg/heron-agent.openrc"; \
	  COPYFILE_DISABLE=1 tar --no-xattrs -C "$$pkg" -czf "dist/heron-agent_linux_$$arch.tar.gz" heron-agent heron-agent.service heron-agent.openrc; \
	  rm -rf "$$pkg"; \
	done; \
	for arch in $(AGENT_DARWIN_ARCHES); do \
	  pkg="dist/pkg-darwin-$$arch"; mkdir -p "$$pkg"; \
	  cp "dist/build/heron-agent-darwin-$$arch" "$$pkg/heron-agent"; \
	  cp $(AGENT_LAUNCHD_PLIST) "$$pkg/xyz.heron.agent.plist"; \
	  COPYFILE_DISABLE=1 tar --no-xattrs -C "$$pkg" -czf "dist/heron-agent_darwin_$$arch.tar.gz" heron-agent xyz.heron.agent.plist; \
	  rm -rf "$$pkg"; \
	done

# 更新器：$(1) 是架构名列表，AGENT_LINUX_ARCHES 或它的子集 HUB_LINUX_ARCHES。完整 release 为全部架构构建与打包
# （agent 组）；只发 hub 的 release 只为 hub 架构打包，供 install-hub.sh 装 hub 主机的更新器。两路同一份配方，
# 更新器的源码与服务文件因而都是 agent 组的输入——只发 hub 时它们不得有变化，hub 主机与节点上的更新器行为一致。
updater_build = for arch in $(1); do \
	  $(agent_goarch); \
	  env GOOS=linux CGO_ENABLED=0 $$gflags go build $(RELEASE_GOFLAGS) -o "dist/build/heron-updater-linux-$$arch" ./cmd/updater; \
	done
updater_static = $(addprefix dist/build/heron-updater-linux-,$(1))
updater_pack = for arch in $(1); do \
	  pkg="dist/pkg-updater-$$arch"; mkdir -p "$$pkg"; \
	  cp "dist/build/heron-updater-linux-$$arch" "$$pkg/heron-updater"; \
	  cp $(UPDATER_UNITS) "$$pkg/"; \
	  COPYFILE_DISABLE=1 tar --no-xattrs -C "$$pkg" -czf "dist/heron-updater_linux_$$arch.tar.gz" heron-updater $(notdir $(UPDATER_UNITS)); \
	  rm -rf "$$pkg"; \
	done

# 门禁读的两份清单（scripts/agentinputs）。agent-go-targets 每行：包路径 GOOS GOARCH [GOARM]。scripts/stampinstall
# 在构建机上把哈希写进 agent 的安装脚本，它的源码同样决定 agent 组的产物，按 CI 的构建机平台列入。
.PHONY: agent-bundle-inputs agent-go-targets
agent-bundle-inputs:
	@printf '%s\n' $(AGENT_BUNDLE_FILES)
agent-go-targets:
	@for arch in $(AGENT_LINUX_ARCHES); do \
	  $(agent_goarch); \
	  echo "./cmd/agent linux $$goarch $$goarm"; \
	  echo "./cmd/updater linux $$goarch $$goarm"; \
	done; \
	for arch in $(AGENT_DARWIN_ARCHES); do echo "./cmd/agent darwin $$arch"; done; \
	echo "./scripts/stampinstall linux amd64"
```

旧配方里 agent 包与更新器包共用一个 `dist/pkg-$$arch` 目录、在同一轮循环里打包；拆成两个片段后各用各的临时目录，tar 包的成员名与内容不变（Step 9 用清单逐一核对）。

- [ ] **Step 6: Makefile 的发布入口**

Makefile 在地基提交的 `export AGENT_VERSION` 之后加 `include deploy/agent.mk`。`build` 目标里的 `$(agent_goarch)` / `$$gflags` 用法不变。把原 `release` 目标整个替换为：

```make
# 发布入口（spec §14.1）：本地验收与 release 流水线都只调用 release。种类只由 scripts/releasekind 判定；两个
# 分支目标开头各自再判定一次，单独调用分支目标也绕不过规则。
release:
	@$(check_version)
	@kind=$$(go run ./scripts/releasekind -version "$$VERSION" -agent "$$AGENT_VERSION") || exit 1; \
	echo "release kind: $$kind"; \
	$(MAKE) "release-$$kind"

assert_release_kind = kind=$$(go run ./scripts/releasekind -version "$$VERSION" -agent "$$AGENT_VERSION") || exit 1; \
	[ "$$kind" = $(1) ] || { echo "release-$(1) does not match this release's kind: $$kind" >&2; exit 1; }
release_prepare = $(MAKE) web && rm -rf dist/build dist/*.tar.gz dist/SHA256SUMS dist/install.sh dist/install-hub.sh dist/install-macos.sh && mkdir -p dist/build
hub_binaries = for arch in $(HUB_LINUX_ARCHES); do $(call hub_build,$$arch,dist/build/heron-hub-linux-$$arch); done
HUB_STATIC = $(addprefix dist/build/heron-hub-linux-,$(HUB_LINUX_ARCHES))
hub_pack = for arch in $(HUB_LINUX_ARCHES); do \
	  pkg="dist/pkg-hub-$$arch"; mkdir -p "$$pkg"; \
	  cp "dist/build/heron-hub-linux-$$arch" "$$pkg/heron-hub"; \
	  cp deploy/systemd/heron-hub.service "$$pkg/heron-hub.service"; \
	  COPYFILE_DISABLE=1 tar --no-xattrs -C "$$pkg" -czf "dist/heron-hub_linux_$$arch.tar.gz" heron-hub heron-hub.service; \
	  rm -rf "$$pkg"; \
	done

release-full:
	@$(check_version)
	@$(call assert_release_kind,full)
	$(release_prepare)
	@set -e; $(agent_build); $(call updater_build,$(AGENT_LINUX_ARCHES)); $(hub_binaries)
	go run ./scripts/checkstatic $(AGENT_STATIC) $(call updater_static,$(AGENT_LINUX_ARCHES)) $(HUB_STATIC)
	@set -e; $(agent_pack); $(call updater_pack,$(AGENT_LINUX_ARCHES)); $(hub_pack)
	rm -rf dist/build
	go run ./scripts/stampinstall -version $(VERSION) -dir dist $(AGENT_INSTALLERS) deploy/install-hub.sh
	shellcheck -s sh dist/install.sh dist/install-hub.sh dist/install-macos.sh
	cd dist && sha256sum *.tar.gz install.sh install-hub.sh install-macos.sh > SHA256SUMS

# 只发 hub（spec §14.1）：门禁排在任何构建之前，agent 组的输入自绑定版本以来变过就不产出任何东西。hub 组之外，
# agent 的两个安装脚本取自绑定版本的 release，验签与核对都在 scripts/boundagent 里。
release-hub-only:
	@$(check_version)
	@$(call assert_release_kind,hub-only)
	go run ./scripts/agentinputs -base "$$AGENT_VERSION"
	$(release_prepare)
	@set -e; $(call updater_build,$(HUB_LINUX_ARCHES)); $(hub_binaries)
	go run ./scripts/checkstatic $(call updater_static,$(HUB_LINUX_ARCHES)) $(HUB_STATIC)
	@set -e; $(call updater_pack,$(HUB_LINUX_ARCHES)); $(hub_pack)
	rm -rf dist/build
	go run ./scripts/stampinstall -version $(VERSION) -dir dist deploy/install-hub.sh
	shellcheck -s sh dist/install-hub.sh
	go run ./scripts/boundagent fetch -version "$$AGENT_VERSION" -dir dist -linux-arches "$(AGENT_LINUX_ARCHES)" -darwin-arches "$(AGENT_DARWIN_ARCHES)"
	cd dist && sha256sum *.tar.gz install.sh install-hub.sh install-macos.sh > SHA256SUMS
```

原 `release` 配方上方那几段注释（COPYFILE_DISABLE、--no-xattrs、静态门禁只收 Linux 产物、darwin 的 CGO、stampinstall 排在打包之后、SHA256SUMS 最后生成）保留，移到对应的新位置（打包相关的随片段进 `deploy/agent.mk` 或留在 `release-full` 上方），内容不变。`release-kind` 目标放在 `release-channel` 旁边（spec §14.1：同处、同一套桩测试），两者都是"打印一个判定"：

```make
# 发布种类（spec §14.1）：full 或 hub-only，判定只在 scripts/releasekind。release 与 release 流水线都读它。
release-kind:
	@$(check_version)
	@go run ./scripts/releasekind -version "$$VERSION" -agent "$$AGENT_VERSION"
```

`.PHONY` 加上 `release-full release-hub-only release-kind`。

`lint` 目标末尾加一行，让 `make ci` 拦下格式不对的 `AGENT_VERSION` 文件：

```make
	go run ./scripts/releasekind -agent "$$AGENT_VERSION" -check
```

- [ ] **Step 7: `boundagent fetch` 的失败用例**

`scripts/boundagent/fetch_test.go`。夹具：`httptest.NewServer` 按路径（`/<version>/<名字>`）返回一张表里的字节；`sigtest.Key()` 的公钥作为受信列表、`sigtest.Sign(version, sums)` 签名（看 `internal/releasesig/sigtest/sigtest.go` 的签名）。辅助 `bundleSums(t, installers map[string][]byte, drop string) []byte` 生成 sha256sum 格式的清单：agent 组每个 tar 包名配一个任意合法摘要，两个安装脚本配真实摘要，`drop` 指定的名字不写。

用例（每个都断言出错时 `dir` 里没有写出任何文件）：
1. `TestFetchWritesVerifiedInstallers`：完整清单、正确签名 → `dir/install.sh`、`dir/install-macos.sh` 与服务端字节逐字节相同。
2. `TestFetchRejectsSignatureForAnotherVersion`：签名用 `sigtest.Sign("v1.2.2", sums)`、取 `v1.2.3` → 错误。
3. `TestFetchRejectsIncompleteAgentBundle`：清单缺 `heron-agent_linux_riscv64.tar.gz` → 错误消息含该名字。
4. `TestFetchRejectsInstallerHashMismatch`：服务端的 `install.sh` 与清单里的摘要不符 → 错误。
5. `TestFetchRejectsDuplicateSumsEntry`：清单里 `install.sh` 出现两行 → 错误。
6. `TestFetchRejectsNonStableVersion`：`v1.2.3-rc.1` → 错误，且服务端没有收到任何请求（夹具计数）。
7. `TestFetchRejectsOversizedSums`：清单超过 `releasesig.MaxSums` → 错误。

- [ ] **Step 8: `boundagent fetch` 实现**

`scripts/boundagent/fetch.go`：

```go
// 取绑定版本 vY 的 release 元数据与 agent 安装脚本（spec §14.1）。接受与否只由发行签名决定：传输不参与信任，
// 所以用普通 HTTP 客户端（跟随 GitHub 下载地址的重定向），不用 githubtransport 的地址限制。
package main

const (
	officialDownloads = "https://github.com/xjetry/heron-probe/releases/download/"
	// maxInstaller 是单个安装脚本的大小上限；现有脚本在 30 KiB 量级。
	maxInstaller = 1 << 20
)

// release 是已验签的一份 SHA256SUMS：digests 是资产名到小写十六进制 SHA-256。
type release struct {
	version string
	sums    []byte
	digests map[string]string
}

// fetchRelease 取回 version 的 SHA256SUMS 与签名，用 keys 验签（正式命令传 releasesig.Trusted()，测试传 sigtest
// 的公钥），解析清单。version 必须是正式版：只发 hub 只绑定正式版，预发布的签名不该被当作绑定依据。
func fetchRelease(ctx context.Context, client *http.Client, base string, keys []ed25519.PublicKey, version string) (release, error) {
	if !update.ValidVersion(version) {
		return release{}, fmt.Errorf("bound agent version %q is not a stable release", version)
	}
	sums, err := get(ctx, client, base+version+"/SHA256SUMS", releasesig.MaxSums)
	if err != nil {
		return release{}, err
	}
	sig, err := get(ctx, client, base+version+"/SHA256SUMS.sig", releasesig.MaxFile)
	if err != nil {
		return release{}, err
	}
	if err := releasesig.Verify(keys, version, sums, sig); err != nil {
		return release{}, fmt.Errorf("verify %s SHA256SUMS: %w", version, err)
	}
	digests, err := parseSums(sums)
	if err != nil {
		return release{}, fmt.Errorf("%s SHA256SUMS: %w", version, err)
	}
	return release{version: version, sums: sums, digests: digests}, nil
}

// get 取 url 的正文，至多 limit 字节；非 200 或超限即错误。
func get(ctx context.Context, client *http.Client, url string, limit int64) ([]byte, error)

// parseSums 解析 sha256sum 的输出：每行 "<64 位小写十六进制>  <名字>"（两个空格），末尾一个换行；格式不对或名字重复即错误。
func parseSums(sums []byte) (map[string]string, error)

// agentBundle 返回 agent 组在 SHA256SUMS 里必须出现的资产名（spec §14.1 的产物分组）。
func agentBundle(linuxArches, darwinArches []string) []string {
	var names []string
	for _, a := range linuxArches {
		names = append(names, "heron-agent_linux_"+a+".tar.gz", "heron-updater_linux_"+a+".tar.gz")
	}
	for _, a := range darwinArches {
		names = append(names, "heron-agent_darwin_"+a+".tar.gz")
	}
	return append(names, "install.sh", "install-macos.sh")
}

// fetchInstallers 核对 vY 的清单含完整的 agent 组，取回两个安装脚本并按清单核对摘要，全部通过后才写进 dir
// （先写临时文件再改名）；任何一步失败都不留下文件。
func fetchInstallers(ctx context.Context, client *http.Client, base string, keys []ed25519.PublicKey, version, dir string, linuxArches, darwinArches []string) error
```

`scripts/boundagent/main.go`：子命令分发（本任务只有 `fetch`），正式命令用 `officialDownloads`、`releasesig.Trusted()`、`&http.Client{}` 与 5 分钟的 `context.WithTimeout`；`-linux-arches` / `-darwin-arches` 用 `strings.Fields` 拆。参数缺失退出 2，失败退出 1，错误写 stderr。命令行不提供换下载地址或换公钥的参数。

Run: `go test -count=1 ./scripts/boundagent/` → `0`。

- [ ] **Step 9: 完整 release 与改动前逐一相同**

先在基点上产出对照（另开一个临时 worktree，不动自己的工作树）：

```bash
git -C <worktree> worktree add --detach <task-dir>/base-tree 7933dea
cd <task-dir>/base-tree && pnpm --dir web install --frozen-lockfile > <task-dir>/logs/base-pnpm.log 2>&1 && make release VERSION=v0.0.0-split > <task-dir>/logs/base-release.log 2>&1; echo $?
cd <task-dir>/base-tree/dist && ls > <task-dir>/logs/base-files.txt && for f in *.tar.gz; do echo "== $f"; tar -tzf "$f"; done > <task-dir>/logs/base-members.txt
```

再在自己的工作树：

```bash
cd <worktree> && make release VERSION=v0.0.0-split AGENT_VERSION=v0.0.0-split > <task-dir>/logs/full-release.log 2>&1; echo $?
cd <worktree>/dist && ls > <task-dir>/logs/files.txt && for f in *.tar.gz; do echo "== $f"; tar -tzf "$f"; done > <task-dir>/logs/members.txt
diff <task-dir>/logs/base-files.txt <task-dir>/logs/files.txt; echo $?
diff <task-dir>/logs/base-members.txt <task-dir>/logs/members.txt; echo $?
```

两个 `diff` 都输出 `0`。`full-release.log` 里有 `release kind: full`。最后 `git -C <worktree> worktree remove --force <task-dir>/base-tree`。

再确认本地构建忘给 `AGENT_VERSION` 时失败并给出提示：`cd <worktree> && make release VERSION=v0.0.0-split > <task-dir>/logs/no-agent.log 2>&1; echo $?` → 非 0，日志含 `AGENT_VERSION=$VERSION`，且 `dist/` 没有被清空重建（门禁在 `release` 第一行之后、`$(MAKE) "release-$$kind"` 之前就失败）。

- [ ] **Step 10: 发布规则测试**

`scripts/release-rules-test.sh` 加三组（沿用文件里的 `bad`、`MAKE` 与受限 PATH）：

1. `AGENT_VERSION` 的 make 层守卫：`MAKE -s release-kind VERSION=v1.0.0 'AGENT_VERSION=v1$(shell touch '"$work"'/expanded-agent)'` 退出非 0、输出含 `contains '$'`，且 `$work/expanded-agent` 不存在。
2. 两个分支目标的 `-n` 展开（受限 PATH 下 `go` 是绊线，`-n` 只打印不执行；`$(MAKE) web` 一行在 `-n` 下会递归执行 `make -n web`，只打印 pnpm 命令）：
   - `MAKE -n release-full VERSION=v1.2.3 AGENT_VERSION=v1.2.3` 的输出含 `heron-agent_linux_riscv64.tar.gz`、`heron-agent_darwin_arm64.tar.gz`、`heron-updater_linux_armv7.tar.gz`、`install.sh`，不含 `agentinputs`、`boundagent`。
   - `MAKE -n release-hub-only VERSION=v1.2.4 AGENT_VERSION=v1.2.3` 的输出含 `scripts/agentinputs -base "$AGENT_VERSION"`、`scripts/boundagent fetch`、`heron-updater_linux_amd64.tar.gz`、`heron-hub_linux_arm64.tar.gz`，不含 `heron-agent_linux_`、`heron-agent_darwin_`、`heron-updater_linux_armv7`、`-o "dist/build/heron-agent-`。
   - `MAKE -n release-kind VERSION=v1.2.3` 的输出含 `go run ./scripts/releasekind -version "$VERSION" -agent "$AGENT_VERSION"`（`-n` 也打印 `@` 开头的行）：判定的接线与 `release-channel` 同在这套桩测试里。
   - 这些命令跑完绊线文件为空（`-n` 没有执行任何 go）。
3. `deploy/agent.mk` 里出现的每个 `deploy/` 路径都在 `MAKE -s agent-bundle-inputs` 的输出里：`grep -o 'deploy/[A-Za-z0-9_./-]*' deploy/agent.mk | sort -u`，逐个在清单里找。

Run: `cd <worktree> && MAKE=make scripts/release-rules-test.sh > <task-dir>/logs/rules.log 2>&1; echo $?` → `0`。

- [ ] **Step 11: 本地验收脚本与 README**

- `scripts/install-accept.sh` 第 86、89 行：`make release VERSION="$VERSION_A"` → `make release VERSION="$VERSION_A" AGENT_VERSION="$VERSION_A"`，B 同理；上方注释加一句：验收的是本次构建的 agent，给出与 VERSION 相同的 AGENT_VERSION 产出完整的一套（spec §14.1）。
- `scripts/macos-accept.sh` 第 48 行同理。
- `README.md` 第 281 行附近：`make release VERSION=v0.0.0-check` → `make release VERSION=v0.0.0-check AGENT_VERSION=v0.0.0-check`，并说明原因（同上）。
- `git grep -n 'make release VERSION'` 列出其余出现处：`docs/validation*.md` 是历史验收记录，不改；其余逐个裁决并在 result.md 里列出。

- [ ] **Step 12: 全量与提交**

Run: `cd <worktree> && make lint > <task-dir>/logs/lint.log 2>&1; echo $?` → `0`；`make ci > <task-dir>/logs/ci.log 2>&1; echo $?` → `0`。

```bash
git add internal/update/version.go internal/update/version_test.go scripts/releasekind/
git commit -m "feat(release): 完整 release 与只发 hub 的判定只在 releasekind 一处"
git add scripts/boundagent/
git commit -m "feat(release): 只发 hub 时取绑定版本已验签的 agent 安装脚本"
git add deploy/agent.mk Makefile scripts/release-rules-test.sh
git commit -m "build(release): 发布按种类分两路，agent 组的配方与打包输入集中在 deploy/agent.mk"
git add scripts/install-accept.sh scripts/macos-accept.sh README.md
git commit -m "build(release): 本地验收构建显式给出与 VERSION 相同的 AGENT_VERSION"
```

缺陷注入（记入 `## Fault injection`）：(a) `kind` 里把 `agent == version` 判断去掉 → `TestKind` 的两条 `full` 红；(b) 允许预发布的绑定版本（去掉 `agentPre` 分支）→ 对应用例红；(c) `fetchInstallers` 不核对 agent 组完整性 → `TestFetchRejectsIncompleteAgentBundle` 红；(d) `release-hub-only` 配方里直接写 `cp deploy/systemd/heron-agent.service …`（未登记的路径进片段）→ 发布规则测试第 3 组红（这条注入改 `deploy/agent.mk`，确认红后恢复）。

---
### Task 4: agent 输入门禁 `scripts/agentinputs`

**Files:**
- Create: `scripts/agentinputs/main.go`（命令行与输出）
- Create: `scripts/agentinputs/collect.go`（一棵树的输入展开：make 清单、`go list`、生成文件与 proto 闭包）
- Create: `scripts/agentinputs/compare.go`（基点与工作树的比较）
- Create: `scripts/agentinputs/main_test.go`（合成仓库夹具与用例）

**Interfaces:**
- Consumes（Task 3 定义，本任务只按格式读，测试里用合成仓库自带的 Makefile 提供）：
  - `make -s -C <树> agent-bundle-inputs`：一行一个相对树根的文件路径。
  - `make -s -C <树> agent-go-targets`：一行一个 `<包路径> <GOOS> <GOARCH> [<GOARM>]`。
- Produces：`go run ./scripts/agentinputs -base <git 引用>`，在仓库内任意目录运行。输入未变退出 0，stdout 一行 `agent inputs unchanged since <base> (<n> files, <m> modules)`；有变化退出 1，stdout 列出变化，最后一行提示把 `AGENT_VERSION` 改成本次 release 的版本；出错退出 2，stderr 说明。`release-hub-only` 配方调用它。
- 包内（测试用）：`func run(args []string, dir string, stdout, stderr io.Writer) int`。

**输入的定义**（spec §14.1，逐条实现，注释里写明理由）：

1. 打包输入：`agent-bundle-inputs` 列出的文件。
2. Go 源码：对 `agent-go-targets` 的每一行，在树根以 `GOOS`、`GOARCH`、`GOARM`（有才设）、`CGO_ENABLED=0`、`GOWORK=off` 运行 `go list -deps -json=ImportPath,Name,Dir,Standard,Module,GoFiles,CgoFiles,SFiles,SysoFiles,EmbedFiles <包>`。`Module.Main` 为真的包（本模块）取 `GoFiles`、`CgoFiles`、`SFiles`、`SysoFiles`、`EmbedFiles`，换算成相对树根的路径；全部行取并集（只在 darwin 编译的文件因此也在内）。`Standard` 的包跳过——标准库由 Go 版本承载（第 4 条）。
3. 第三方模块：同一批 `go list` 结果里 `Module.Main` 为假的包所属模块，记为 `path@version`，有替换时加 `=>replPath@replVersion`；取并集。
4. `go.mod` 里的 `go` 与 `toolchain` 两条指令行（逐行读，去掉首尾空白；没有 `toolchain` 行就只有 `go`）。CI 用 `go-version-file: go.mod` 取工具链，`go` 指令即编译器版本。
5. 生成的 proto 文件按 proto 文件算，不按 Go 包算：
   - 生成文件的判定看文件本身：开头含 `// Code generated by protoc-gen-go. DO NOT EDIT.` 或 `// Code generated by protoc-gen-connect-go. DO NOT EDIT.`，且有一行 `// source: <x.proto>`（protoc-gen-go）或 `// Source: <x.proto>`（connect-go）。只读文件开头 4 KiB。
   - 根：解析第 2 条里全部**非生成**的 Go 文件（`go/parser`，`parser.SkipObjectResolution`），对每个 import 了"含生成文件的包"的文件，本地名取 import 的别名，没有别名取该包的 `Name`；收集 `X.Sel` 形式的选择子里 `X` 是这个本地名的 `Sel`。别名为 `_` 的跳过（只跑 init）；别名为 `.` 的报错退出 2（无法从选择子知道引用了什么，宁可停下）。
   - 每个生成文件的顶层声明名（无接收者的函数、`type`、`var`、`const` 的名字）建表：（包路径，名字）→ 文件。被引用的名字落在哪些生成文件，这些文件的 proto 源就是根 proto。名字声明在同包的非生成文件里的，那个文件已是第 2 条的输入，不进根。
   - proto 闭包：proto 源按 `buf.yaml` 里 `modules` 的每个 `- path: <目录>`（逐行匹配，不引入 YAML 依赖）查找 `<目录>/<x.proto>`；读每个文件里的 `import [public|weak] "<y.proto>";` 递归；在这些目录里找不到的 import（`google/protobuf/...`）不展开——它们由第 3 条的 `google.golang.org/protobuf` 模块版本承载。
   - 最终只把 proto 源在闭包里的生成文件计入输入，其余生成文件不计。理由写进注释：生成包把 `AdminService` 与 `AgentService` 放在同一个 Go 包里，按包算会把每个只动 `admin.proto` 的 hub 功能都判成 agent 改动；而按"agent 代码引用了哪些生成标识符"取根、再沿 proto import 展开，覆盖了 agent 经消息嵌套间接依赖的文件（`agent.proto` 引用 `types.proto` 里的消息）。

**比较**（`compare.go`）：

- 工作树一侧：在仓库根（`git rev-parse --show-toplevel`）上按上面展开。
- 基点一侧：`git worktree add --detach <临时目录>/base <base>` 后在其中展开，结束时 `git worktree remove --force`（`defer`，失败也清理）。临时目录用 `os.MkdirTemp`。基点上 `agent-bundle-inputs` 或 `agent-go-targets` 不存在（make 报 `No rule to make target`）→ 退出 2：`<base> predates the agent bundle definition (deploy/agent.mk); a hub-only release cannot bind it`。
- 文件：对两侧文件集合的并集逐个比较——基点内容取 `git cat-file -e <base>:<路径>` 判存在、`git show <base>:<路径>` 取内容；工作树内容直接读文件。一侧有一侧没有、或内容不同，都算变化。这样未跟踪的新文件、工作树里删掉的文件都被比到（`git diff` 看不见未跟踪文件）。
- 模块：两侧集合的对称差。
- `go.mod` 指令：两侧逐行比较。

**输出**（有变化时，按类分组，组内排序）：

```
agent inputs changed since v1.2.3:
  file  gen/heron/v1/types.pb.go
  file  internal/agentwire/wire.go
  module  golang.org/x/net@v0.57.0 (only in v1.2.3)
  module  golang.org/x/net@v0.58.0 (only in working tree)
  go.mod  "go 1.27.1" -> "go 1.27.2"
set AGENT_VERSION to this release's version: the agent bundle must be released together with this hub (spec §14.1)
```

- [ ] **Step 1: 合成仓库夹具**

`main_test.go` 里的 `newFixture(t *testing.T, mutate func(dir string)) string`：在 `t.TempDir()` 里写出下面的文件，`git init`、提交（`git -c user.name=t -c user.email=t@example.com commit`）、打 tag `v1.0.0`，返回目录。`mutate` 非 nil 时在提交之前调用（用来造出"基点就带某种写法"的变体）。所有测试 `t.Setenv("GOFLAGS", "-mod=mod")`、`t.Setenv("GOTOOLCHAIN", "local")`、`t.Setenv("GOPROXY", "off")`、`t.Setenv("GOWORK", "off")`，不碰网络。

```
go.mod
  module example.com/fix
  go 1.22
  require ( example.com/dep v0.0.0  example.com/hubdep v0.0.0 )
  replace example.com/dep => ./dep
  replace example.com/hubdep => ./hubdep
dep/go.mod            module example.com/dep / go 1.22
dep/dep.go            package dep; func X() int { return 1 }
dep2/go.mod           module example.com/dep / go 1.22
dep2/dep.go           package dep; func X() int { return 2 }
hubdep/go.mod         module example.com/hubdep / go 1.22
hubdep/hubdep.go      package hubdep; func Y() int { return 1 }
buf.yaml              version: v2 / modules: / "  - path: proto"
proto/heron/v1/agent.proto    syntax = "proto3"; package heron.v1; import "heron/v1/types.proto";
proto/heron/v1/types.proto    syntax = "proto3"; package heron.v1;
proto/heron/v1/admin.proto    syntax = "proto3"; package heron.v1; import "heron/v1/types.proto";
proto/heron/v1/public.proto   syntax = "proto3"; package heron.v1;
gen/v1/agent.pb.go    头两行 "// Code generated by protoc-gen-go. DO NOT EDIT." 与 "// source: heron/v1/agent.proto"；package v1; type ReportRequest struct{ M *Metrics }
gen/v1/types.pb.go    source heron/v1/types.proto；type Metrics struct{}
gen/v1/admin.pb.go    source heron/v1/admin.proto；type AdminRequest struct{ M *Metrics }
gen/v1/public.pb.go   source heron/v1/public.proto；type PublicThing struct{}
gen/v1/v1connect/agent.connect.go   头 "// Code generated by protoc-gen-connect-go. DO NOT EDIT." / "//" / "// Source: heron/v1/agent.proto"；package v1connect; import v1 "example.com/fix/gen/v1"; func NewAgentServiceClient() *v1.ReportRequest { return nil }
gen/v1/v1connect/admin.connect.go   Source heron/v1/admin.proto；func NewAdminServiceClient() *v1.AdminRequest { return nil }
internal/agentlib/lib.go         package agentlib; import "example.com/dep"; func Run() int { return dep.X() }
internal/agentlib/lib_darwin.go  //go:build darwin / package agentlib; func darwinOnly() int { return 0 }
cmd/agent/main.go     package main; import ( v1 "example.com/fix/gen/v1"; "example.com/fix/gen/v1/v1connect"; "example.com/fix/internal/agentlib" ); func main() { _ = v1.ReportRequest{}; _ = v1connect.NewAgentServiceClient(); _ = agentlib.Run() }
cmd/updater/main.go   package main; import "example.com/fix/internal/agentlib"; func main() { _ = agentlib.Run() }
cmd/hub/main.go       package main; import ( v1 "example.com/fix/gen/v1"; "example.com/fix/gen/v1/v1connect"; "example.com/hubdep" ); func main() { _ = v1.AdminRequest{}; _ = v1connect.NewAdminServiceClient(); _ = hubdep.Y() }
deploy/agent.mk       AGENT_BUNDLE := 1
deploy/agent.service  [Service]
deploy/hub.service    [Service]
Makefile
  agent-bundle-inputs:
  	@printf '%s\n' deploy/agent.mk deploy/agent.service
  agent-go-targets:
  	@printf '%s\n' './cmd/agent linux amd64' './cmd/agent darwin arm64' './cmd/updater linux arm 7'
```

（上面是内容提要，写成真实的多行 Go/proto/make 文件；Makefile 的配方行以制表符开头。）

辅助 `runTool(t, dir string, args ...string) (int, string)` 调 `run(args, dir, &out, &out)`，返回退出码与合并输出。

- [ ] **Step 2: 用例表（先写，全部红在 `run` 未定义）**

| 用例 | 提交后的改动（在工作树上做，不提交，除非注明） | 退出码 | 输出必须含 |
|---|---|---|---|
| `TestUnchanged` | 无 | 0 | `unchanged since v1.0.0` |
| `TestHubOnlyChangeIsNotAnAgentInput` | 改 `cmd/hub/main.go`、`deploy/hub.service` | 0 | |
| `TestAdminOnlyGeneratedChangeIsNotAnAgentInput` | 改 `gen/v1/admin.pb.go`、`gen/v1/v1connect/admin.connect.go`、`proto/heron/v1/admin.proto` | 0 | |
| `TestImportedProtoChangeIsAnAgentInput` | 改 `gen/v1/types.pb.go`（只经 `agent.proto` 的 import 进闭包） | 1 | `gen/v1/types.pb.go` |
| `TestAgentGeneratedChangeIsAnAgentInput` | 改 `gen/v1/agent.pb.go` | 1 | `gen/v1/agent.pb.go` |
| `TestDarwinOnlyFileChangeIsAnAgentInput` | 改 `internal/agentlib/lib_darwin.go` | 1 | `internal/agentlib/lib_darwin.go` |
| `TestUpdaterOnlyPlatformIsCovered` | 改 `cmd/updater/main.go` | 1 | `cmd/updater/main.go` |
| `TestBundleFileChangeIsAnAgentInput` | 改 `deploy/agent.service` | 1 | `deploy/agent.service` |
| `TestFragmentChangeIsAnAgentInput` | 改 `deploy/agent.mk` | 1 | `deploy/agent.mk` |
| `TestUntrackedAgentFileIsAnAgentInput` | 新建未跟踪的 `internal/agentlib/extra.go`（`package agentlib`） | 1 | `internal/agentlib/extra.go` |
| `TestDeletedAgentFileIsAnAgentInput` | 删除 `internal/agentlib/lib_darwin.go` | 1 | `internal/agentlib/lib_darwin.go` |
| `TestAgentDependencyChangeIsAnAgentInput` | `go.mod` 里 `replace example.com/dep => ./dep2` | 1 | `example.com/dep` |
| `TestHubDependencyChangeIsNotAnAgentInput` | `hubdep/hubdep.go` 改返回值（只被 hub 用；同一替换目录，模块串不变） | 0 | |
| `TestGoDirectiveChangeIsAnAgentInput` | `go.mod` 的 `go 1.22` → `go 1.22.1` | 1 | `go 1.22.1` |
| `TestReferencedGeneratedFileOutsideAgentProtoIsAnInput` | `newFixture` 的 `mutate` 让 `cmd/agent/main.go` 另引用 `v1.PublicThing{}`；提交后改 `gen/v1/public.pb.go` | 1 | `gen/v1/public.pb.go` |
| `TestBasePredatingBundleDefinitionIsAnError` | `mutate` 让基点的 Makefile 没有这两个目标；提交后在工作树补上目标 | 2 | `predates the agent bundle definition` |
| `TestDotImportOfGeneratedPackageIsAnError` | 在工作树把 `cmd/agent/main.go` 的生成包 import 改成 `.` 导入 | 2 | `dot import` |
| `TestBaseWorktreeIsRemoved` | 无；跑完后 `git worktree list --porcelain` 只有一棵 | 0 | |

每个"改"都是在文件末尾加一行注释（Go 文件）或一行文本，足以改变内容。

Run: `cd <worktree> && go test -count=1 ./scripts/agentinputs/ > <task-dir>/logs/red.log 2>&1; echo $?` → 非 0（未定义）。

- [ ] **Step 3: 实现 `collect.go`**

```go
// inputs 是一棵树上 agent 组的构建输入（spec §14.1）。
type inputs struct {
	files   map[string]bool // 相对树根的路径
	modules map[string]bool // path@version，或 path@version=>replPath@replVersion
	goLines []string        // go.mod 的 go 与 toolchain 指令行
}

// target 是 agent-go-targets 的一行。
type target struct{ pkg, goos, goarch, goarm string }

// errNoBundleDefinition：这棵树的 Makefile 没有 agent-bundle-inputs / agent-go-targets。
var errNoBundleDefinition = errors.New("no agent bundle definition")

func collect(tree string) (inputs, error)
func makeLines(tree, goal string) ([]string, error)          // make -s -C tree goal；No rule to make target → errNoBundleDefinition
func parseTargets(lines []string) ([]target, error)          // strings.Fields，3 或 4 段，其余报错
func goList(tree string, t target) ([]listedPackage, error) // 解码 go list -json 的对象流（json.Decoder 循环 Decode）
func generatedSource(path string) (proto string, ok bool, err error)
func referencedNames(files []string, generatedPkgs map[string]string) (map[pkgName]bool, error) // generatedPkgs：导入路径 → 包名
func declaredNames(file string) ([]string, error)
func protoClosure(tree string, roots []string) (map[string]bool, error)
```

`listedPackage` 只解码 `go list` 给出的那几个字段（`Module` 是指针，`Replace` 同）。

- [ ] **Step 4: 实现 `compare.go` 与 `main.go`**

```go
type diff struct {
	files   []string // 排序
	onlyOld []string // 只在基点的模块，排序
	onlyNew []string // 只在工作树的模块，排序
	goLines [2][]string
}

func (d diff) empty() bool

// compareTrees 展开工作树与基点两侧的输入并比较；基点在临时 worktree 里展开，返回前移除。
func compareTrees(root, base string) (diff, inputs, error)
```

`main.go`：`run` 解析 `-base`（缺失退出 2），`git -C dir rev-parse --show-toplevel` 得到仓库根，调 `compareTrees`，按上面的格式输出。`errNoBundleDefinition` 出现在基点一侧时输出 `predates` 那句；出现在工作树一侧时输出 `working tree has no agent bundle definition (deploy/agent.mk)`。两者都退出 2。

Run: `go test -count=1 ./scripts/agentinputs/ > <task-dir>/logs/green.log 2>&1; echo $?` → `0`。

- [ ] **Step 5: 真实仓库上的冒烟**

本任务的基点还没有 `deploy/agent.mk`（Task 3 才加），所以真实仓库上只能验"出错路径"：

Run: `cd <worktree> && go run ./scripts/agentinputs -base v0.5.3 > <task-dir>/logs/real.log 2>&1; echo $?` → `2`，日志含 `working tree has no agent bundle definition`（工作树一侧先展开）。集成后由 Task 6 在真实仓库上跑正向用例。

- [ ] **Step 6: 全量与提交**

Run: `cd <worktree> && go vet ./scripts/agentinputs/ > <task-dir>/logs/vet.log 2>&1; echo $?` → `0`；`make lint > <task-dir>/logs/lint.log 2>&1; echo $?` → `0`；`make ci > <task-dir>/logs/ci.log 2>&1; echo $?` → `0`。

```bash
git add scripts/agentinputs/
git commit -m "feat(release): 只发 hub 的门禁核对 agent 组构建输入自绑定版本以来未变"
```

缺陷注入（记入 `## Fault injection`）：(a) 生成文件一律计入（不做 proto 粒度）→ `TestAdminOnlyGeneratedChangeIsNotAnAgentInput` 红；(b) proto 闭包不递归 import → `TestImportedProtoChangeIsAnAgentInput` 红；(c) 只展开第一个目标平台 → `TestDarwinOnlyFileChangeIsAnAgentInput` 与 `TestUpdaterOnlyPlatformIsCovered` 红；(d) 文件比较改用 `git diff --quiet <base> -- <路径>` → `TestUntrackedAgentFileIsAnAgentInput` 红；(e) 不比较模块 → `TestAgentDependencyChangeIsAnAgentInput` 红；(f) 根只取 `agent.proto` 而不按引用的标识符取 → `TestReferencedGeneratedFileOutsideAgentProtoIsAnInput` 红。

---
### Task 5: release 流水线接上两种 release、绑定版本的端到端与发布回读

**Files:**
- Modify: `scripts/boundagent/main.go`、`scripts/boundagent/fetch.go`（加 `pin`、`readback` 子命令）、新建 `scripts/boundagent/pin_test.go`
- Modify: `scripts/compat-download.sh`、`scripts/compat-e2e.sh`、`scripts/compat-download-test.sh`
- Modify: `Makefile`（`bound-agent-e2e`、`agent-version` 两个目标）
- Modify: `.github/workflows/release.yml`
- Modify: `.gitignore`（若 `build/` 未被忽略）
- Modify: `README.md`（发布一节：维护者发版时如何定 `AGENT_VERSION`）

**Interfaces:**
- Consumes: Task 3 的 `fetchRelease`、`parseSums`、`agentBundle`、`make -s release-kind`；Task 4 的 `go run ./scripts/agentinputs -base <ref>`。
- Produces:
  - `go run ./scripts/boundagent pin -version vY -out FILE`：取 vY 已验签的清单，写出与 `scripts/compat-agent.json` 同格式的文件（`repository`、`tag`、`releaseKind: "stable"`、`assets` 为 amd64 与 arm64 的 `heron-agent_linux_<arch>.tar.gz` 摘要）。
  - `go run ./scripts/boundagent readback -version vX -bound vY -dir DIR`：`DIR` 里有从 vX 的 Release 页面取回的 `SHA256SUMS`、`install.sh`、`install-macos.sh`；核对两个脚本的 SHA-256 等于 vY 已验签清单里的摘要，且 vX 的 `SHA256SUMS` 里这两行的摘要与之相同。
  - `make bound-agent-e2e`、`make -s agent-version`（打印 `$$AGENT_VERSION`）。
  - `scripts/compat-download.sh` 与 `scripts/compat-e2e.sh` 读环境变量 `COMPAT_PIN`（清单路径），缺省仍是 `scripts/compat-agent.json`。

- [ ] **Step 1: `pin` 与 `readback` 的失败用例**

`scripts/boundagent/pin_test.go`，沿用 Task 3 的 httptest 夹具与 `sigtest`：
1. `TestPinWritesBoundAgentDigests`：输出 JSON 的 `tag == "v1.2.3"`、`releaseKind == "stable"`、`assets` 恰为 amd64、arm64 两项，摘要等于清单里 `heron-agent_linux_amd64.tar.gz`、`heron-agent_linux_arm64.tar.gz` 的值；再用 `jq` 等价的 Go 断言核对它满足 `compat-download.sh` 里那段 `jq -e` 的全部条件（repository、tag 形状、两个架构、64 位十六进制）。
2. `TestPinRejectsUnverifiedSums`：签名对不上 → 错误、不写文件。
3. `TestReadbackAcceptsCopiedInstallers`：vX 的 `SHA256SUMS` 与 `DIR` 里两个脚本都与 vY 的一致 → 通过。
4. `TestReadbackRejectsDifferentInstaller`：`DIR/install.sh` 多一个字节 → 错误，消息含 `install.sh`。
5. `TestReadbackRejectsSumsLineMismatch`：脚本字节正确，但 vX 的 `SHA256SUMS` 里 `install-macos.sh` 那一行的摘要不同 → 错误。

- [ ] **Step 2: 实现 `pin`、`readback`**

`pin` 调 `fetchRelease`，按 `agentBundle` 核对完整性后取两个架构的摘要写 JSON（先写临时文件再改名）。`readback` 调 `fetchRelease(vY)`，读 `DIR` 下三个文件，按上面的两条核对。两者都只用 `officialDownloads` 与 `releasesig.Trusted()`（测试经包内函数注入）。

Run: `cd <worktree> && go test -count=1 ./scripts/boundagent/ > <task-dir>/logs/bound.log 2>&1; echo $?` → `0`。

- [ ] **Step 3: 兼容下载脚本接受清单路径**

`scripts/compat-download.sh` 第 6 行 `pin="$(cd "$(dirname "$0")" && pwd)/compat-agent.json"` 改为：

```sh
# 清单缺省是仓库内审查过的兼容基线；只发 hub 的 release 用绑定版本的清单（make bound-agent-e2e 经 COMPAT_PIN 给出），
# 其摘要取自用受信公钥验过签的 SHA256SUMS（scripts/boundagent pin），信任根同样不是下载时临时取得的校验和。
pin=${COMPAT_PIN:-"$(cd "$(dirname "$0")" && pwd)/compat-agent.json"}
```

文件开头两行注释改为陈述两种清单的来源。`scripts/compat-e2e.sh` 里读 tag 的那行改为读同一个 `${COMPAT_PIN:-…}`。`scripts/compat-download-test.sh` 加一个用例：`COMPAT_PIN` 指向测试里写出的另一份清单时，脚本按它的 tag 与摘要校验（沿用该测试现有的桩方式，不访问网络）。

Run: `cd <worktree> && scripts/compat-download-test.sh > <task-dir>/logs/compat-test.log 2>&1; echo $?` → `0`。

- [ ] **Step 4: Makefile 的两个目标**

```make
# 只发 hub 的 release 实际发出去的组合是 hub 加绑定版本的 agent（spec §14.1）：用绑定版本已发布的 agent 包跑同一套
# 端到端，不以当前源码构建替代。摘要取自已验签的 SHA256SUMS，写成与兼容基线同格式的清单交给 compat-e2e。
bound-agent-e2e: hub-binary
	@mkdir -p build/bound-agent
	go run ./scripts/boundagent pin -version "$$AGENT_VERSION" -out build/bound-agent/pin.json
	COMPAT_PIN=build/bound-agent/pin.json scripts/compat-e2e.sh $(E2E_TIER1)

# release 流水线的回读读它，不在 workflow 里另读一遍仓库文件。
agent-version:
	@printf '%s\n' "$$AGENT_VERSION"
```

`.PHONY` 加上两者。确认 `build/` 已被 `.gitignore` 覆盖（`git check-ignore -v build/bound-agent/pin.json`），没有就加。

- [ ] **Step 5: release.yml**

`build` job：

1. `actions/checkout` 加 `fetch-depth: 0`，并在注释里写原因：只发 hub 的门禁要在本地检出绑定版本的 tag（`git worktree add`），浅克隆没有它。
2. `make release VERSION="$GITHUB_REF_NAME"` 之后加一步，把种类写进 step 输出；赋值与写输出分开，判定失败时这一步失败，而不是写出空值：

```yaml
      # 种类只由 scripts/releasekind 判定（spec §14.1）；make release 已按它分路，后续步骤读同一个判定。
      - id: kind
        run: |
          kind=$(make -s release-kind VERSION="$GITHUB_REF_NAME")
          echo "kind=$kind" >> "$GITHUB_OUTPUT"
```

3. `make compat-e2e` 之后加：

```yaml
      # 只发 hub 时发出去的组合是 hub 加绑定版本已发布的 agent，用那份 agent 再跑一遍端到端。
      - if: steps.kind.outputs.kind == 'hub-only'
        run: make bound-agent-e2e
        env:
          E2E_LISTEN_HOST: 0.0.0.0
```

`publish` job 的回读那一步之后加：

```yaml
      # 只发 hub 的 release 原样带着绑定版本的两个 agent 安装脚本（spec §14.1）：从 Release 页面取回实际可下载的
      # 字节，核对它们等于绑定版本已验签清单里的摘要。
      - run: |
          kind=$(make -s release-kind VERSION="$GITHUB_REF_NAME")
          if [ "$kind" = hub-only ]; then
            bound=$(make -s agent-version)
            gh release download "$GITHUB_REF_NAME" -p install.sh -p install-macos.sh -D readback
            go run ./scripts/boundagent readback -version "$GITHUB_REF_NAME" -bound "$bound" -dir readback
          fi
        env:
          GH_TOKEN: ${{ github.token }}
```

`sign` job 不变：它只签 `dist/` 里的 `SHA256SUMS`，两种 release 都一样。

若本机有 `actionlint`（`command -v actionlint`），跑一遍并把结果写进 Evidence；没有就在 result.md 写明未跑。

- [ ] **Step 6: README 的发版说明**

在 README 发布一节（第 72 行附近讲镜像与 tag 的段落之后）加"发版时的 AGENT_VERSION"：
- `AGENT_VERSION` 在两次发版之间写最近一次已发布的 agent 版本。发版前跑 `go run ./scripts/agentinputs -base "$(make -s agent-version)"`：有变化（退出 1）就在发版提交里把 `AGENT_VERSION` 改成这次的版本号，这是完整 release；没有变化（退出 0）就保持不变，这是只发 hub 的 release。
- 拆分后的第一个 release 必须是完整 release：此前的版本没有发行签名，门禁对它们报 `predates`。
- 只发 hub 的 release 说明里写明：先升级 hub，再更新节点；旧 hub 的面板会把最新版当作节点目标，节点任务会在下载阶段失败（旧 agent 不受影响）。

- [ ] **Step 7: 全量与提交**

Run: `cd <worktree> && make lint > <task-dir>/logs/lint.log 2>&1; echo $?` → `0`；`make script-test > <task-dir>/logs/script-test.log 2>&1; echo $?` → `0`；`make -n bound-agent-e2e > <task-dir>/logs/n-bound.log 2>&1; echo $?` → `0` 且输出含 `boundagent pin` 与 `COMPAT_PIN=build/bound-agent/pin.json`；用 task.md 分到的端口跑一次缺省清单的 `make compat-e2e`（确认 `COMPAT_PIN` 缺省时行为不变）→ `0`；`make ci > <task-dir>/logs/ci.log 2>&1; echo $?` → `0`。

```bash
git add scripts/boundagent/
git commit -m "feat(release): 绑定版本的 agent 清单与安装脚本回读"
git add scripts/compat-download.sh scripts/compat-e2e.sh scripts/compat-download-test.sh Makefile .gitignore
git commit -m "test(e2e): 只发 hub 时用绑定版本已发布的 agent 跑端到端"
git add .github/workflows/release.yml README.md
git commit -m "ci(release): 流水线按 release 种类跑绑定版本的端到端与安装脚本回读"
```

（`.gitignore` 没改就不加。）

缺陷注入（记入 `## Fault injection`）：(a) `readback` 只比 vX 的 `SHA256SUMS` 行、不读脚本字节 → `TestReadbackRejectsDifferentInstaller` 红；(b) `compat-download.sh` 忽略 `COMPAT_PIN` → `compat-download-test.sh` 新用例红。

---

### Task 6: 集成与验收（控制端执行，不派 worker）

- [ ] **Step 1: 集成分支**

从 `7933dea` 开 `integ/<run>-vsplit`，按 Task 1 → Task 2 → Task 3 → Task 4 → Task 5 的顺序叠上各任务的提交（`git cherry-pick <base>..<branch>`）；冲突只在 `gen/`、`web/src/gen/` 时 `make gen` 重新生成后继续，其余冲突手解并记下。叠完 `git log --oneline 7933dea..HEAD` 核对每个任务的提交都在。

- [ ] **Step 2: 真实仓库上的门禁正反用例**

在集成分支的干净工作树上（`git status --porcelain` 为空）：

| 改动（各自在干净树上做、做完 `git checkout -- .`） | `go run ./scripts/agentinputs -base HEAD` |
|---|---|
| 无 | 0 |
| `gen/heron/v1/admin.pb.go` 末尾加一行注释 | 0 |
| `internal/hub/api/nodes.go` 末尾加一行注释 | 0 |
| `gen/heron/v1/types.pb.go` 末尾加一行注释 | 1 |
| `gen/heron/v1/update.pb.go` 末尾加一行注释 | 1 |
| `cmd/updater/main.go` 末尾加一行注释 | 1 |
| `deploy/install.sh` 末尾加一行注释 | 1 |
| `deploy/agent.mk` 末尾加一行注释 | 1 |
| `go.mod` 的 `go` 指令改一个补丁号 | 1 |

另跑 `go run ./scripts/agentinputs -base v0.5.3` → 2，含 `predates`。

- [ ] **Step 3: 门禁**

`make ci`、`go test -race -count=1 ./internal/hub/updates/ ./internal/hub/api/ ./internal/hub/ingest/ ./internal/update/ ./scripts/...`、`make e2e`（含绑定版本断言）、`make compat-e2e`、`make release VERSION=v0.0.0-<run> AGENT_VERSION=v0.0.0-<run>`（完整 release，与 Task 3 Step 9 的清单对照）、`make release VERSION=v0.0.0-<run>`（失败并提示 `AGENT_VERSION=$VERSION`）。每条 `cmd > log 2>&1; echo $?`。

- [ ] **Step 4: 面板的真实浏览器验收**

`make hub-binary` 构建的 hub（绑定 `v0.5.3`）起在本机，登录后核对：在线更新页节点区写"目标版本 v0.5.3（hub 绑定的 agent 版本）"、不点"检查官方新版本"就能勾选低于它的节点；注册窗口页安装命令旁写明绑定版本。截图存运行目录。

- [ ] **Step 5: 汇报与合入**

向用户汇报改动范围、门禁结果、没覆盖到的部分（真实的只发 hub 构建要等第一个签名 release 发布后才能端到端跑通），按用户的选择合入 main。发版不在本计划内：拆分后的第一个 release 是完整 release，发版提交把 `AGENT_VERSION` 改成该版本号。
