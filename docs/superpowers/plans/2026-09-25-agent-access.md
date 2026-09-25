# API token 与 agent 入口 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 让不在仓库里的 agent 不持有管理员密码也能读 hub 的数据：只读 API token、按方法声明的准入口径、随 hub 下发的 schema 与入口卡片，以及面板上的 token 管理页。

**Architecture:** proto 里新增 `probe.v1.access` 方法选项，`AdminService` 每个方法声明 LOGIN / READ / SESSION 之一；拦截器在构造时从生成的描述符读出整张表，按 `Authorization` 的 Bearer scheme 选择 token 路径或会话路径，两路互不回退。token 存 `api_token` 表（schema v6），每次请求查库、不缓存。`GetApiReference` 返回 `proto/` 下嵌入的入口卡片与 proto 源文件，卡片里的例子由 e2e 执行。

**Tech Stack:** Go 1.27、connect-go、protobuf（buf 生成 Go 与 TS）、modernc SQLite、React + connect-query + vitest、POSIX sh + curl + jq（e2e）。

**Spec:** `docs/superpowers/specs/2026-09-17-probe-architecture-design.md`（§3.2、§3.3、§5.3、§5.6、§6.6、§10、§12）；方针 `docs/guidelines/agent-first.md`。

## Global Constraints

- `AdminService` 的每个方法用 `probe.v1.access` 选项声明准入口径：`ACCESS_LOGIN`（仅 `Login`，凭据是请求体里的密码）、`ACCESS_READ`（会话或 API token）、`ACCESS_SESSION`（仅会话：有副作用的方法，以及凭据管理——包括只读的 `ListApiTokens`）。
- 拦截器在构造时从生成的描述符读出整张表，任一方法未声明即 panic。
- 明文为固定前缀 `probe_at_` 加 32 字节随机数的 hex，库中只存整串的 SHA-256，明文只在 `CreateApiToken` 的响应里出现一次。
- 两条路径互不回退：`Authorization` 头的 scheme 为 `Bearer` 即走 bearer 路径，cookie 一律不看；否则走 §5.3 的会话路径。scheme 为 `Bearer` 而 token 为空、格式不对或不存在，都返回 `Unauthenticated`。其他 scheme 不是 hub 的凭据，按不存在处理。
- token 只能调 `ACCESS_READ` 方法，其余返回 `PermissionDenied`，错误信息写明方法名与"API token 只读"。
- §5.3 的四条 CSRF 事实属于会话路径，一条都不因 bearer 路径而放松。
- 每次校验都查库，不缓存：吊销（删行）在下一个请求即生效，hub 运行中由 `probe-hub` 直接改库也一样。
- 最后使用时间只供展示，距已落库值满一分钟才异步刷新（与会话同一口径），刷新只做 UPDATE，不复活已删除的 token。
- 名称 1–64 字符，不要求唯一，身份是 id。token 总数上限 100，超出返回 `ResourceExhausted`。不设过期时间。
- 改密码不连带吊销 token；`probe-hub passwd` 改完后列出现存 token，终端下询问是否全部吊销（默认不吊销）。`probe-hub token list`、`probe-hub token revoke --id N`、`probe-hub token revoke --all`。
- `GetApiReference`（`ACCESS_READ`）返回入口卡片（`proto/SKILL.md`）与全部 proto 源文件，均在构建时嵌入。
- 入口卡片约定 `PROBE_HUB` 与 `PROBE_TOKEN` 两个环境变量；卡片里标为示例的 shell 代码块由 e2e 用真实 hub 与 token 逐个执行，断言退出码为 0 且输出为合法 JSON。
- 代码注释与提交信息不写过程信息（任务 / 步骤 / 轮次编号、方案代号、审阅引用、"按上一轮"）；注释写 WHY 与不变式，并指明前提由谁保证。
- 不打补丁：根因在哪层就在哪层修，不在调用方加特判；同形状的缺陷全库一次改齐。
- 每条新断言做一次缺陷注入：构造它本该抓住的缺陷，确认它红且红在正确原因；注入前用 `git diff --quiet` 的反面（`git diff --stat` 非空）确认注入落地，注入后 `git checkout -- <file>` 还原。判成败的命令写成 `cmd > log 2>&1; echo $?`，不接管道。Go 测试一律 `go test -count=1`。
- 生成物（`gen/`、`web/src/gen/`）只由 `make gen` 产生，不手改；`make ci` 要求生成后工作树干净。
- 实现者不改 `docs/`；不派子代理。

## Review Focus

1. hub 在做 Basic 认证的反代之后：浏览器对每个请求自动带 `Authorization: Basic …`，面板必须照常可用（会话路径放行，写方法照常）。测试落在 Task 3。
2. 畸形 bearer：`Authorization: Bearer `（空 token）、同一请求两个 Bearer 头、节点 token 当 API token 用、API token 拿去调 `AgentService.Report`——一律 `Unauthenticated`，且不回退到 cookie。测试落在 Task 3。
3. hub 运行中由另一进程（`probe-hub token revoke`）吊销：下一个请求就是 401，不需要重启。测试落在 Task 3（直接删行）与 Task 5（e2e 里跑 CLI）。
4. 改密码：会话全部失效但 token 继续可用；`passwd` 从管道读密码时（e2e、容器初始化）不能停下来等 y/N。测试落在 Task 5。
5. 卡片漂移：hub 实际下发的卡片里的例子在真实数据上跑不通、或面板下载到的文件不是 hub 下发的那份。测试落在 Task 4（e2e 用 hub 下发的卡片跑例子）与 Task 6（下载内容等于 `guide`）。

---

### Task 1: 准入口径选项与拦截器读表

**Files:**
- Create: `proto/probe/v1/access.proto`
- Modify: `proto/probe/v1/admin.proto`（import、服务注释、27 个现有 rpc 各加一行 option）
- Generated（`make gen`）：`gen/probe/v1/access.pb.go`、`gen/probe/v1/admin.pb.go`、`web/src/gen/probe/v1/access_pb.ts`、`web/src/gen/probe/v1/admin_pb.ts`
- Create: `internal/hub/api/access.go`
- Modify: `internal/hub/api/service.go`（`Service.access` 字段、`New` 建表、拦截器改名并按表裁决）
- Create: `internal/hub/api/access_test.go`
- Modify: `internal/hub/api/api_test.go`（`TestEveryAdminProcedureRejectsAnonymousCalls` 按表跳过 LOGIN）

**Interfaces:**
- Produces: proto 枚举 `probe.v1.Access`（Go：`probev1.Access`，值 `probev1.Access_ACCESS_UNSPECIFIED`、`Access_ACCESS_LOGIN`、`Access_ACCESS_READ`、`Access_ACCESS_SESSION`）与扩展 `probev1.E_Access`；`func accessTable(svc protoreflect.ServiceDescriptor) map[string]probev1.Access`（键为 Connect 过程路径 `/probe.v1.AdminService/<Method>`）；`Service.access map[string]probev1.Access`；拦截器类型 `accessInterceptor`。

- [ ] **Step 1: 写 access.proto**

```proto
syntax = "proto3";

package probe.v1;

option go_package = "github.com/xjetry/probe/gen/probe/v1;probev1";

import "google/protobuf/descriptor.proto";

// Access 声明 AdminService 的方法接受哪种凭据，由挂载时绑定的拦截器在构造时读出并裁决。
// 每个方法都必须显式声明：ACCESS_UNSPECIFIED 让 hub 在构造处理器时拒绝启动，
// 因而不存在"漏标时默认放行还是默认拒绝"的取舍。
enum Access {
  ACCESS_UNSPECIFIED = 0;
  // 仅 Login：凭据是请求体里的密码，不需要会话或 API token。
  ACCESS_LOGIN = 1;
  // 无副作用，且不列出或管理任何凭据：会话与 API token 都可调用。
  // 把方法改成 ACCESS_READ 就是扩大 API token 的权限。
  ACCESS_READ = 2;
  // 仅会话：有副作用的方法，以及凭据管理（包括只读的 ListApiTokens——自动化进程
  // 没有理由知道还有哪些 token 存在）。
  ACCESS_SESSION = 3;
}

extend google.protobuf.MethodOptions {
  // 50000–99999 是留给组织内部扩展的字段号区间。
  Access access = 50001;
}
```

- [ ] **Step 2: 给 admin.proto 的每个 rpc 声明口径**

在 `import "probe/v1/types.proto";` 下加 `import "probe/v1/access.proto";`。服务注释的第一句改为：

```proto
// 管理面板与 agent → hub。每个方法以 probe.v1.access 声明准入口径（见 access.proto），
// 由挂载时绑定的拦截器裁决。全部方法都是 unary，且没有一个标为无副作用：本服务不接受 GET，
// 这是抵御跨站请求伪造的几条各自独立的事实之一。
```

每个 rpc 从 `rpc X(A) returns (B);` 改成带 option 的块，保留原有注释。口径表（27 个，必须逐个核对）：

| 方法 | 口径 |
|---|---|
| Login | ACCESS_LOGIN |
| ListNodes、GetRegisterWindow、GetSnapshot、QueryMetrics、GetTraffic、ListProbeTasks、QueryProbes、ListAlertRules、ListAlertEvents、ListNotifyChannels | ACCESS_READ |
| Logout、CreateNode、UpdateNode、DeleteNode、RotateNodeToken、ReorderNodes、OpenRegisterWindow、CloseRegisterWindow、AdjustTraffic、SaveProbeTask、DeleteProbeTask、SaveAlertRule、DeleteAlertRule、SaveNotifyChannel、DeleteNotifyChannel、TestNotifyChannel | ACCESS_SESSION |

写法示例：

```proto
  // 用管理员密码换取会话 cookie（Set-Cookie 在响应头里）。
  rpc Login(LoginRequest) returns (LoginResponse) {
    option (probe.v1.access) = ACCESS_LOGIN;
  }
  // 作废当前会话并清除 cookie。
  rpc Logout(LogoutRequest) returns (LogoutResponse) {
    option (probe.v1.access) = ACCESS_SESSION;
  }

  rpc ListNodes(ListNodesRequest) returns (ListNodesResponse) {
    option (probe.v1.access) = ACCESS_READ;
  }
```

`GetRegisterWindow` 可以是 READ：它的响应只有 open、expires_at、remaining，不含 key。`ListNotifyChannels` 可以是 READ：渠道的 token、URL、头值只写不读，响应里没有凭据。

- [ ] **Step 3: 生成并确认生成物**

Run: `cd /Users/xjetry/work/vibe/probe-access && make gen > /tmp/agent-access-gen.log 2>&1; echo $?`
Expected: `0`；`git status --short gen web/src/gen` 列出 `access.pb.go`、`admin.pb.go`、`access_pb.ts`、`admin_pb.ts`。`buf lint` 在 `make lint` 里跑，此时先 `cd /Users/xjetry/work/vibe/probe-access && buf lint > /tmp/agent-access-buflint.log 2>&1; echo $?` 期望 `0`。

- [ ] **Step 4: 写会失败的测试**

`internal/hub/api/access_test.go`：

```go
package api

import (
	"strings"
	"testing"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protodesc"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/descriptorpb"

	probev1 "github.com/xjetry/probe/gen/probe/v1"
)

// readMethods 是对 API token 开放的全部方法。把一个方法改成 ACCESS_READ 是在扩大
// token 的权限，必须同时改这份清单——与 Public* 字段允许列表同一口径。
var readMethods = []string{
	"ListNodes", "GetRegisterWindow", "GetSnapshot", "QueryMetrics", "GetTraffic",
	"ListProbeTasks", "QueryProbes", "ListAlertRules", "ListAlertEvents", "ListNotifyChannels",
}

func adminService() protoreflect.ServiceDescriptor {
	return probev1.File_probe_v1_admin_proto.Services().ByName("AdminService")
}

func TestAdminAccessTableMatchesDeclaredPolicy(t *testing.T) {
	svc := adminService()
	table := accessTable(svc)
	if len(table) != svc.Methods().Len() {
		t.Fatalf("table has %d entries for %d methods", len(table), svc.Methods().Len())
	}
	read := map[string]bool{}
	for _, m := range readMethods {
		read[m] = true
	}
	for i := 0; i < svc.Methods().Len(); i++ {
		name := string(svc.Methods().Get(i).Name())
		want := probev1.Access_ACCESS_SESSION
		switch {
		case name == "Login":
			want = probev1.Access_ACCESS_LOGIN
		case read[name]:
			want = probev1.Access_ACCESS_READ
		}
		if got := table["/probe.v1.AdminService/"+name]; got != want {
			t.Errorf("%s: access %v, want %v", name, got, want)
		}
		delete(read, name)
	}
	if len(read) != 0 {
		t.Errorf("readMethods names methods that do not exist: %v", read)
	}
}

// syntheticService 造一个只有一个方法的服务；opts 为 nil 表示该方法没有任何选项。
func syntheticService(t *testing.T, opts *descriptorpb.MethodOptions) protoreflect.ServiceDescriptor {
	t.Helper()
	fd, err := protodesc.NewFile(&descriptorpb.FileDescriptorProto{
		Name:        proto.String("synthetic.proto"),
		Package:     proto.String("synthetic"),
		Syntax:      proto.String("proto3"),
		MessageType: []*descriptorpb.DescriptorProto{{Name: proto.String("M")}},
		Service: []*descriptorpb.ServiceDescriptorProto{{
			Name: proto.String("S"),
			Method: []*descriptorpb.MethodDescriptorProto{{
				Name: proto.String("Bare"), InputType: proto.String(".synthetic.M"), OutputType: proto.String(".synthetic.M"), Options: opts,
			}},
		}},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	return fd.Services().Get(0)
}

func expectPanic(t *testing.T, want string, fn func()) {
	t.Helper()
	defer func() {
		r := recover()
		if r == nil {
			t.Fatalf("no panic; want one mentioning %q", want)
		}
		if !strings.Contains(r.(string), want) {
			t.Fatalf("panic %q does not mention %q", r, want)
		}
	}()
	fn()
}

func TestAccessTableRefusesUndeclaredOrUnknownAccess(t *testing.T) {
	expectPanic(t, "synthetic.S.Bare", func() { accessTable(syntheticService(t, nil)) })
	unknown := &descriptorpb.MethodOptions{}
	proto.SetExtension(unknown, probev1.E_Access, probev1.Access(99))
	expectPanic(t, "synthetic.S.Bare", func() { accessTable(syntheticService(t, unknown)) })
	declared := &descriptorpb.MethodOptions{}
	proto.SetExtension(declared, probev1.E_Access, probev1.Access_ACCESS_READ)
	if got := accessTable(syntheticService(t, declared))["/synthetic.S/Bare"]; got != probev1.Access_ACCESS_READ {
		t.Fatalf("declared method: %v", got)
	}
}
```

Run: `cd /Users/xjetry/work/vibe/probe-access && go test -count=1 ./internal/hub/api/ -run 'AccessTable' > /tmp/agent-access-t1-red.log 2>&1; echo $?`
Expected: 非 0，编译错误 `undefined: accessTable`。

- [ ] **Step 5: 实现 accessTable**

`internal/hub/api/access.go`：

```go
package api

import (
	"fmt"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"

	probev1 "github.com/xjetry/probe/gen/probe/v1"
)

// accessTable 从服务描述符读出每个方法声明的准入口径，键为 Connect 过程路径。
// 未声明或取值不认识即 panic：描述符来自生成代码，只有改了 proto 却漏标时才会发生，
// 而那时 hub 在构造处理器时就起不来——未声明的方法不可能随发布出去被默认放行或默认拒绝。
func accessTable(svc protoreflect.ServiceDescriptor) map[string]probev1.Access {
	methods := svc.Methods()
	table := make(map[string]probev1.Access, methods.Len())
	for i := 0; i < methods.Len(); i++ {
		m := methods.Get(i)
		level, _ := proto.GetExtension(m.Options(), probev1.E_Access).(probev1.Access)
		switch level {
		case probev1.Access_ACCESS_LOGIN, probev1.Access_ACCESS_READ, probev1.Access_ACCESS_SESSION:
		default:
			panic(fmt.Sprintf("%s does not declare a known probe.v1.access (got %v)", m.FullName(), level))
		}
		table["/"+string(svc.FullName())+"/"+string(m.Name())] = level
	}
	return table
}
```

- [ ] **Step 6: 拦截器按表裁决**

`internal/hub/api/service.go`：`Service` 加字段 `access map[string]probev1.Access`（注释：`// access 是 AdminService 每个过程的准入口径，New 时从描述符读出，之后只读。`）；`New` 里在返回前赋值 `access: accessTable(probev1.File_probe_v1_admin_proto.Services().ByName("AdminService"))`。把 `sessionInterceptor` 改名为 `accessInterceptor`（类型、构造函数 `accessInterceptor()`、`Handler` 里的调用、两个流式方法的接收者一并改），`WrapUnary` 的开头改为按表裁决：

```go
func (i accessInterceptor) WrapUnary(next connect.UnaryFunc) connect.UnaryFunc {
	s := i.s
	return func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
		level, ok := s.access[req.Spec().Procedure]
		if !ok {
			return nil, unauthenticated("unauthenticated")
		}
		peer := peerInfo{
			from:   auth.ClientIP(req.Peer().Addr, req.Header().Get("X-Forwarded-For"), s.cfg.TrustedProxies),
			scheme: auth.RequestScheme(req.Peer().Addr, req.Header().Get("X-Forwarded-Proto"), s.cfg.TrustedProxies),
		}
		ctx = context.WithValue(ctx, peerKey{}, peer)
		if level == probev1.Access_ACCESS_LOGIN {
			// 凭据是请求体里的密码，由 Login 裁决；按来源的锁定也在那里。
			return next(ctx, req)
		}
		// 以下与原会话校验相同：取 cookie、AuthenticateSession、放入 sessionKey。
		...
	}
}
```

（`...` 处保留原有从 `tok, ok := sessionToken(req.Header())` 到 `return next(context.WithValue(ctx, sessionKey{}, tok), req)` 的代码，不改。）

`api_test.go` 的 `TestEveryAdminProcedureRejectsAnonymousCalls` 里把按名字跳过 Login 改为按表跳过：

```go
			method := svc.Methods().Get(j)
			path := "/" + string(svc.FullName()) + "/" + string(method.Name())
			// LOGIN 方法的凭据在请求体里，匿名白名单由 cmd/hub/mux_test 守；无管理员的 Login 失败不能证明拦截器存在。
			if h.svc.access[path] == probev1.Access_ACCESS_LOGIN {
				continue
			}
```

- [ ] **Step 7: 跑测试**

Run: `cd /Users/xjetry/work/vibe/probe-access && go test -count=1 ./internal/hub/api/ ./cmd/hub/ > /tmp/agent-access-t1-green.log 2>&1; echo $?`
Expected: `0`。

- [ ] **Step 8: 缺陷注入**

1. 把 admin.proto 里 `CreateNode` 的口径改成 `ACCESS_READ`，`make gen`，`git diff --stat proto` 非空确认落地；跑 `go test -count=1 ./internal/hub/api/ -run AdminAccessTable > /tmp/agent-access-inj1.log 2>&1; echo $?`，期望非 0 且日志含 `CreateNode: access ACCESS_READ, want ACCESS_SESSION`。`git checkout -- proto gen web/src/gen` 还原。
2. 删掉 `ListNodes` 的 option 块，`make gen`，确认落地；跑同一命令，期望非 0 且 panic 信息含 `probe.v1.AdminService.ListNodes does not declare`。还原。
3. 在 `accessTable` 的 `switch` 里把 `default: panic(...)` 改成 `default:`（吞掉未知值），确认落地；跑 `-run AccessTableRefuses`，期望非 0 且报 `no panic`。还原。

- [ ] **Step 9: 全量门禁并提交**

Run: `cd /Users/xjetry/work/vibe/probe-access && make ci > /tmp/agent-access-t1-ci.log 2>&1; echo $?`，期望 `0`。

```bash
cd /Users/xjetry/work/vibe/probe-access && git add proto gen web/src/gen internal/hub/api && git commit -m "api: AdminService 每个方法以 probe.v1.access 声明准入口径，拦截器按表裁决"
```

---

### Task 2: api_token 存储与 token 鉴权

**Files:**
- Modify: `internal/hub/store/schema.go`（`ddlAPIToken`，并入 `schemaStatements`）
- Modify: `internal/hub/store/store.go`（`schemaVersion = 6`，`migrations[6]`）
- Create: `internal/hub/store/apitoken.go`
- Create: `internal/hub/store/apitoken_test.go`
- Modify: `internal/hub/store/migrate_test.go`（冻结的 `schemaV5` 与从 v5 升级的测试）
- Create: `internal/hub/auth/apitoken.go`
- Create: `internal/hub/auth/apitoken_test.go`

**Interfaces:**
- Consumes: `store.Store.write`、`store.Store.writeAsync`、`auth.HashToken`、`auth.touchEvery`。
- Produces:
  - `type store.APIToken struct { ID int64; Name string; CreatedAt time.Time; LastUsedAt time.Time }`（`LastUsedAt.IsZero()` 表示从未使用）
  - `var store.ErrAPITokenLimit = errors.New("API token limit reached")`
  - `func (s *Store) CreateAPIToken(ctx context.Context, name string, hash [32]byte, now time.Time, limit int) (APIToken, error)`
  - `func (s *Store) ListAPITokens(ctx context.Context) ([]APIToken, error)`（按 id 升序）
  - `func (s *Store) APITokenByHash(ctx context.Context, hash [32]byte) (APIToken, bool, error)`
  - `func (s *Store) DeleteAPIToken(ctx context.Context, id int64) (bool, error)`
  - `func (s *Store) DeleteAllAPITokens(ctx context.Context) (int64, error)`
  - `func (s *Store) TouchAPITokenAsync(id int64, now time.Time, done func(error))`
  - `const auth.APITokenPrefix = "probe_at_"`、`const auth.MaxAPITokens = 100`
  - `func auth.NewAPIToken() (string, [32]byte)`
  - `func (a *Auth) CreateAPIToken(ctx context.Context, name string) (store.APIToken, string, error)`
  - `func (a *Auth) AuthenticateAPIToken(ctx context.Context, plain string) (bool, error)`

- [ ] **Step 1: 写 store 的会失败测试**

`internal/hub/store/apitoken_test.go`（用本包 `store_test.go` 里的 `open(t) (*Store, *clock.Fake)` 打开库）：

```go
func TestAPITokenLifecycle(t *testing.T) {
	s, clk := open(t)
	ctx := t.Context()
	h := sha256.Sum256([]byte("probe_at_x"))
	tok, err := s.CreateAPIToken(ctx, "ci", h, clk.Now(), 100)
	if err != nil || tok.ID == 0 || tok.Name != "ci" || !tok.CreatedAt.Equal(clk.Now().Truncate(time.Second)) || !tok.LastUsedAt.IsZero() {
		t.Fatalf("create: %+v %v", tok, err)
	}
	got, ok, err := s.APITokenByHash(ctx, h)
	if err != nil || !ok || got.ID != tok.ID {
		t.Fatalf("by hash: %+v %v %v", got, ok, err)
	}
	if list, err := s.ListAPITokens(ctx); err != nil || len(list) != 1 || list[0].ID != tok.ID {
		t.Fatalf("list: %+v %v", list, err)
	}
	if found, err := s.DeleteAPIToken(ctx, tok.ID); err != nil || !found {
		t.Fatalf("delete: %v %v", found, err)
	}
	if _, ok, err := s.APITokenByHash(ctx, h); err != nil || ok {
		t.Fatalf("deleted token still resolves: %v %v", ok, err)
	}
	if found, err := s.DeleteAPIToken(ctx, tok.ID); err != nil || found {
		t.Fatalf("second delete reported found=%v err=%v", found, err)
	}
}

// 吊销按 id 进行：id 若被复用，针对旧 token 写下的吊销命令会落到新 token 上。
func TestAPITokenIDsAreNotReused(t *testing.T) {
	s, clk := open(t)
	ctx := t.Context()
	first, _ := s.CreateAPIToken(ctx, "a", sha256.Sum256([]byte("a")), clk.Now(), 100)
	if _, err := s.DeleteAPIToken(ctx, first.ID); err != nil {
		t.Fatal(err)
	}
	second, err := s.CreateAPIToken(ctx, "b", sha256.Sum256([]byte("b")), clk.Now(), 100)
	if err != nil || second.ID <= first.ID {
		t.Fatalf("id reused or went backwards: first %d second %d err %v", first.ID, second.ID, err)
	}
}

func TestAPITokenLimitIsDecidedInsideTheWriteTransaction(t *testing.T) {
	s, clk := open(t)
	ctx := t.Context()
	const limit = 5
	var wg sync.WaitGroup
	var created, refused atomic.Int32
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := s.CreateAPIToken(ctx, "n", sha256.Sum256([]byte(fmt.Sprint(i))), clk.Now(), limit)
			switch {
			case err == nil:
				created.Add(1)
			case errors.Is(err, ErrAPITokenLimit):
				refused.Add(1)
			default:
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if created.Load() != limit || refused.Load() != 12-limit {
		t.Fatalf("created %d refused %d, want %d and %d", created.Load(), refused.Load(), limit, 12-limit)
	}
}

func TestTouchRecordsUseAndNeverResurrects(t *testing.T) {
	s, clk := open(t)
	ctx := t.Context()
	h := sha256.Sum256([]byte("t"))
	tok, _ := s.CreateAPIToken(ctx, "t", h, clk.Now(), 100)
	clk.Advance(time.Minute)
	done := make(chan error, 1)
	s.TouchAPITokenAsync(tok.ID, clk.Now(), func(err error) { done <- err })
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	got, _, _ := s.APITokenByHash(ctx, h)
	if !got.LastUsedAt.Equal(clk.Now().Truncate(time.Second)) {
		t.Fatalf("last used %v, want %v", got.LastUsedAt, clk.Now())
	}
	if _, err := s.DeleteAPIToken(ctx, tok.ID); err != nil {
		t.Fatal(err)
	}
	s.TouchAPITokenAsync(tok.ID, clk.Now(), func(err error) { done <- err })
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if list, err := s.ListAPITokens(ctx); err != nil || len(list) != 0 {
		t.Fatalf("touch after delete resurrected a token: %+v %v", list, err)
	}
}

func TestDeleteAllAPITokens(t *testing.T) {
	s, clk := open(t)
	ctx := t.Context()
	for i := 0; i < 3; i++ {
		s.CreateAPIToken(ctx, "x", sha256.Sum256([]byte(fmt.Sprint(i))), clk.Now(), 100)
	}
	if n, err := s.DeleteAllAPITokens(ctx); err != nil || n != 3 {
		t.Fatalf("deleted %d err %v", n, err)
	}
}
```

Run: `cd /Users/xjetry/work/vibe/probe-access && go test -count=1 ./internal/hub/store/ -run 'APIToken|Touch' > /tmp/agent-access-t2-red.log 2>&1; echo $?`
Expected: 非 0，`undefined: ...CreateAPIToken`。

- [ ] **Step 2: 表与迁移**

`schema.go` 加：

```go
// api_token 是 AdminService 的程序化凭据（§5.6）。
const ddlAPIToken = `CREATE TABLE api_token (
  -- AUTOINCREMENT：id 永不复用。吊销按 id 进行，复用会让针对旧 token 的吊销落到新 token 上。
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  name TEXT NOT NULL,
  -- 整串明文（含前缀）的 SHA-256；明文不落库。
  token_hash BLOB NOT NULL UNIQUE,
  created_at INTEGER NOT NULL,
  -- NULL 表示从未使用。只供展示：距已落库值满一分钟才刷新。
  last_used_at INTEGER
)`
```

`schemaStatements()` 的返回改为 `return append(append(out, alertStatements()...), ddlAPIToken)`。`store.go`：`const schemaVersion = 6`，`migrations` 加：

```go
	6: func(tx *sql.Tx) error {
		if _, err := tx.Exec(ddlAPIToken); err != nil {
			return fmt.Errorf("%w in %q", err, ddlAPIToken)
		}
		return nil
	},
```

- [ ] **Step 3: store 方法**

`internal/hub/store/apitoken.go`：

```go
package store

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

type APIToken struct {
	ID         int64
	Name       string
	CreatedAt  time.Time
	LastUsedAt time.Time // 零值表示从未使用
}

var ErrAPITokenLimit = errors.New("API token limit reached")

// CreateAPIToken 在同一写事务里计数并插入：写协程串行执行事务，并发创建不会都看到
// limit−1 而一起越过上限。
func (s *Store) CreateAPIToken(ctx context.Context, name string, hash [32]byte, now time.Time, limit int) (APIToken, error) {
	var out APIToken
	err := s.write(ctx, func(tx *sql.Tx) error {
		var n int
		if err := tx.QueryRow("SELECT COUNT(*) FROM api_token").Scan(&n); err != nil {
			return err
		}
		if n >= limit {
			return ErrAPITokenLimit
		}
		res, err := tx.Exec("INSERT INTO api_token (name, token_hash, created_at) VALUES (?, ?, ?)", name, hash[:], now.Unix())
		if err != nil {
			return err
		}
		id, err := res.LastInsertId()
		if err != nil {
			return err
		}
		out = APIToken{ID: id, Name: name, CreatedAt: time.Unix(now.Unix(), 0).UTC()}
		return nil
	})
	return out, err
}

func scanAPIToken(scan func(...any) error) (APIToken, error) {
	var t APIToken
	var created int64
	var used sql.NullInt64
	if err := scan(&t.ID, &t.Name, &created, &used); err != nil {
		return APIToken{}, err
	}
	t.CreatedAt = time.Unix(created, 0).UTC()
	if used.Valid {
		t.LastUsedAt = time.Unix(used.Int64, 0).UTC()
	}
	return t, nil
}

func (s *Store) ListAPITokens(ctx context.Context) ([]APIToken, error) {
	rows, err := s.r.QueryContext(ctx, "SELECT id, name, created_at, last_used_at FROM api_token ORDER BY id")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []APIToken
	for rows.Next() {
		t, err := scanAPIToken(rows.Scan)
		if err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// APITokenByHash 每次鉴权都直接读库，不经缓存：删行即吊销，且对另一进程的删除同样立即生效。
func (s *Store) APITokenByHash(ctx context.Context, hash [32]byte) (APIToken, bool, error) {
	row := s.r.QueryRowContext(ctx, "SELECT id, name, created_at, last_used_at FROM api_token WHERE token_hash = ?", hash[:])
	t, err := scanAPIToken(row.Scan)
	if errors.Is(err, sql.ErrNoRows) {
		return APIToken{}, false, nil
	}
	if err != nil {
		return APIToken{}, false, err
	}
	return t, true, nil
}

func (s *Store) DeleteAPIToken(ctx context.Context, id int64) (bool, error) {
	var found bool
	err := s.write(ctx, func(tx *sql.Tx) error {
		res, err := tx.Exec("DELETE FROM api_token WHERE id = ?", id)
		if err != nil {
			return err
		}
		n, err := res.RowsAffected()
		found = n == 1
		return err
	})
	return found, err
}

func (s *Store) DeleteAllAPITokens(ctx context.Context) (int64, error) {
	var n int64
	err := s.write(ctx, func(tx *sql.Tx) error {
		res, err := tx.Exec("DELETE FROM api_token")
		if err != nil {
			return err
		}
		n, err = res.RowsAffected()
		return err
	})
	return n, err
}

// TouchAPITokenAsync 只 UPDATE 已存在的行：吊销之后才落库的刷新不会把 token 写回来。
func (s *Store) TouchAPITokenAsync(id int64, now time.Time, done func(error)) {
	s.writeAsync(func(tx *sql.Tx) error {
		_, err := tx.Exec("UPDATE api_token SET last_used_at = ? WHERE id = ?", now.Unix(), id)
		return err
	}, done)
}
```

- [ ] **Step 4: 迁移测试**

`migrate_test.go`：加 `schemaV5`——**逐字冻结**当前（v5）完整 DDL 的字面量字符串切片，不引用 `schemaStatements()` 或任何 `ddl*` 常量（它们以后会变，冻结的是历史）。取法：在 Task 2 开始前的提交上临时打印 `schemaStatements()` 的每一项，把输出原样粘成 Go 字符串字面量；另把 `PRAGMA user_version` 为 5 这件事交给 `migrateFrom` 的第三个参数。然后：

```go
func TestMigrationFromV5MatchesFreshSchemaAndKeepsRows(t *testing.T) {
	migrated, fresh := migrateFrom(t, schemaV5, 5, seedMinuteRow)
	if got, want := describe(t, migrated.r), describe(t, fresh.r); !reflect.DeepEqual(got, want) {
		t.Fatalf("migrated schema differs from fresh schema:\n got: %+v\nwant: %+v", got, want)
	}
	if v := userVersion(t, migrated.r); v != schemaVersion {
		t.Fatalf("user_version = %d, want %d", v, schemaVersion)
	}
	rows, err := migrated.ReadMinuteRows(t.Context(), 7, 0, 120)
	if err != nil || len(rows) != 1 {
		t.Fatalf("minute row lost across migration: %v %v", rows, err)
	}
}
```

Run: `cd /Users/xjetry/work/vibe/probe-access && go test -count=1 ./internal/hub/store/ > /tmp/agent-access-t2-store.log 2>&1; echo $?`
Expected: `0`。

- [ ] **Step 5: 写 auth 的会失败测试**

`internal/hub/auth/apitoken_test.go`（用本包 `auth_test.go` 里的 `setup(t) (*Auth, *store.Store, *clock.Fake)`）：

```go
func TestNewAPITokenFormat(t *testing.T) {
	plain, hash := NewAPIToken()
	if !strings.HasPrefix(plain, APITokenPrefix) || len(plain) != len(APITokenPrefix)+64 {
		t.Fatalf("token %q: want %q + 64 hex digits", plain, APITokenPrefix)
	}
	if _, err := hex.DecodeString(strings.TrimPrefix(plain, APITokenPrefix)); err != nil {
		t.Fatalf("suffix is not hex: %v", err)
	}
	if hash != HashToken(plain) {
		t.Fatal("hash must cover the whole string including the prefix")
	}
}

func TestAuthenticateAPIToken(t *testing.T) {
	a, st, _ := setup(t)
	ctx := t.Context()
	tok, plain, err := a.CreateAPIToken(ctx, "ci")
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		name  string
		plain string
		want  bool
	}{
		{"valid", plain, true},
		{"unknown", APITokenPrefix + strings.Repeat("0", 64), false},
		{"no prefix", strings.TrimPrefix(plain, APITokenPrefix), false},
		{"empty", "", false},
	} {
		if ok, err := a.AuthenticateAPIToken(ctx, c.plain); err != nil || ok != c.want {
			t.Errorf("%s: ok=%v err=%v, want %v", c.name, ok, err, c.want)
		}
	}
	if _, err := st.DeleteAPIToken(ctx, tok.ID); err != nil {
		t.Fatal(err)
	}
	if ok, err := a.AuthenticateAPIToken(ctx, plain); err != nil || ok {
		t.Fatalf("revoked token accepted: %v %v", ok, err)
	}
}

func TestCreateAPITokenEnforcesLimit(t *testing.T) {
	a, _, _ := setup(t)
	for i := 0; i < MaxAPITokens; i++ {
		if _, _, err := a.CreateAPIToken(t.Context(), "n"); err != nil {
			t.Fatal(err)
		}
	}
	if _, _, err := a.CreateAPIToken(t.Context(), "n"); !errors.Is(err, store.ErrAPITokenLimit) {
		t.Fatalf("token %d: %v, want ErrAPITokenLimit", MaxAPITokens+1, err)
	}
}

// 与会话同一口径：从未使用或距已落库值满一分钟才刷新。
func TestAPITokenUseIsRecordedAtMostOncePerMinute(t *testing.T) {
	a, st, clk := setup(t)
	ctx := t.Context()
	_, plain, _ := a.CreateAPIToken(ctx, "ci")
	lastUsed := func() time.Time {
		t.Helper()
		got, _, err := st.APITokenByHash(ctx, HashToken(plain))
		if err != nil {
			t.Fatal(err)
		}
		return got.LastUsedAt
	}
	waitLastUsed := func(want time.Time) {
		t.Helper()
		for deadline := time.Now().Add(2 * time.Second); time.Now().Before(deadline); time.Sleep(5 * time.Millisecond) {
			if lastUsed().Equal(want) {
				return
			}
		}
		t.Fatalf("last used %v, want %v", lastUsed(), want)
	}
	first := clk.Now().Truncate(time.Second)
	a.AuthenticateAPIToken(ctx, plain)
	waitLastUsed(first)
	clk.Advance(59 * time.Second)
	a.AuthenticateAPIToken(ctx, plain)
	// 未满一分钟不投递刷新；用一次同步写把可能的异步写排到它之后，再断言值未变。
	if _, err := st.DeleteAPIToken(ctx, -1); err != nil {
		t.Fatal(err)
	}
	if got := lastUsed(); !got.Equal(first) {
		t.Fatalf("touched within a minute: %v", got)
	}
	clk.Advance(time.Second)
	a.AuthenticateAPIToken(ctx, plain)
	waitLastUsed(clk.Now().Truncate(time.Second))
}
```

（`DeleteAPIToken(ctx, -1)` 删不到任何行，只是借写协程的先进先出把此前可能投递的异步刷新排空；实现者核对 `writeAsync` 与 `write` 共用同一通道后保留这句注释。）

Run: `cd /Users/xjetry/work/vibe/probe-access && go test -count=1 ./internal/hub/auth/ -run 'APIToken' > /tmp/agent-access-t2-auth-red.log 2>&1; echo $?`
Expected: 非 0，`undefined: NewAPIToken`。

- [ ] **Step 6: 实现 auth**

`internal/hub/auth/apitoken.go`：

```go
package auth

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"strings"

	"github.com/xjetry/probe/internal/hub/store"
)

const (
	// APITokenPrefix 让泄漏到日志、配置或代码仓库里的 token 能被审查与 secret scanning 认出。
	APITokenPrefix = "probe_at_"
	MaxAPITokens   = 100
)

// NewAPIToken 生成明文与整串（含前缀）的哈希。高熵随机数用 SHA-256 足够，理由同节点 token。
func NewAPIToken() (string, [32]byte) {
	var b [32]byte
	rand.Read(b[:])
	plain := APITokenPrefix + hex.EncodeToString(b[:])
	return plain, HashToken(plain)
}

// CreateAPIToken 的 name 由调用方清洗与校验；这里只负责生成、限额与落库。
func (a *Auth) CreateAPIToken(ctx context.Context, name string) (store.APIToken, string, error) {
	plain, hash := NewAPIToken()
	tok, err := a.store.CreateAPIToken(ctx, name, hash, a.clk.Now(), MaxAPITokens)
	if err != nil {
		return store.APIToken{}, "", err
	}
	return tok, plain, nil
}

// AuthenticateAPIToken 每次都查库、不缓存：吊销在下一个请求即生效，包括 probe-hub 在另一进程里的删除。
// 最近使用时刻与会话同一口径——从未使用或距已落库值满 touchEvery 才异步刷新，刷新只 UPDATE。
func (a *Auth) AuthenticateAPIToken(ctx context.Context, plain string) (bool, error) {
	if !strings.HasPrefix(plain, APITokenPrefix) {
		return false, nil
	}
	tok, ok, err := a.store.APITokenByHash(ctx, HashToken(plain))
	if err != nil || !ok {
		return false, err
	}
	now := a.clk.Now()
	if tok.LastUsedAt.IsZero() || now.Sub(tok.LastUsedAt) >= touchEvery {
		a.store.TouchAPITokenAsync(tok.ID, now, func(err error) {
			if err != nil {
				a.log.Warn("recording API token use failed", "err", err)
			}
		})
	}
	return true, nil
}
```

- [ ] **Step 7: 跑测试**

Run: `cd /Users/xjetry/work/vibe/probe-access && go test -count=1 ./internal/hub/store/ ./internal/hub/auth/ > /tmp/agent-access-t2-green.log 2>&1; echo $?`
Expected: `0`。

- [ ] **Step 8: 缺陷注入**

1. `TouchAPITokenAsync` 的 SQL 改成 `INSERT OR REPLACE INTO api_token (id, name, token_hash, created_at, last_used_at) VALUES (?, '', x'00', 0, ?)`（参数顺序相应调整），确认落地；跑 `-run TouchRecordsUseAndNeverResurrects`，期望红在 `resurrected`。还原。
2. `CreateAPIToken` 把计数挪到事务外（先 `s.r.QueryRowContext` 计数再 `s.write` 插入），确认落地；跑 `-run LimitIsDecided -count=20`，期望出现 `created > 5` 的失败。还原。
3. `ddlAPIToken` 去掉 `AUTOINCREMENT`，确认落地；跑 `-run IDsAreNotReused`，期望红在 `id reused`（迁移测试也会红，属同一注入的预期连带）。还原。
4. `AuthenticateAPIToken` 的节流条件改成 `true`（每次都刷新），确认落地；跑 `-run AtMostOncePerMinute`，期望红在 `touched within a minute`。还原。
5. `AuthenticateAPIToken` 加一个包级 `map[[32]byte]bool` 缓存命中结果，确认落地；跑 `-run TestAuthenticateAPIToken`，期望红在 `revoked token accepted`。还原。

- [ ] **Step 9: 门禁并提交**

Run: `cd /Users/xjetry/work/vibe/probe-access && make ci > /tmp/agent-access-t2-ci.log 2>&1; echo $?`，期望 `0`。

```bash
cd /Users/xjetry/work/vibe/probe-access && git add internal/hub/store internal/hub/auth && git commit -m "store, auth: api_token 表与只读 token 的签发、查库鉴权"
```

---

### Task 3: token 管理 RPC 与 bearer 路径

**Files:**
- Modify: `proto/probe/v1/admin.proto`（`ListApiTokens`、`CreateApiToken`、`DeleteApiToken` 与消息）
- Generated（`make gen`）
- Create: `internal/hub/api/tokens.go`
- Modify: `internal/hub/api/service.go`（包注释、bearer 分支、`bearerCredential`、`permissionDenied`）
- Create: `internal/hub/api/tokens_test.go`

**Interfaces:**
- Consumes: Task 1 的 `Service.access`、`accessInterceptor`；Task 2 的 `auth.(*Auth).CreateAPIToken`、`AuthenticateAPIToken`、`store.(*Store).ListAPITokens`、`DeleteAPIToken`、`store.ErrAPITokenLimit`、`auth.MaxAPITokens`；`api.cleanName`。
- Produces: proto `ApiToken`、`ListApiTokensRequest/Response`、`CreateApiTokenRequest/Response`、`DeleteApiTokenRequest/Response`；`func bearerCredential(h http.Header) (token string, bearer bool, err error)`。

- [ ] **Step 1: proto**

在 `AdminService` 末尾（`TestNotifyChannel` 之后）加：

```proto
  // API token 的元数据；明文只在 CreateApiToken 的响应里出现一次，hub 只存哈希。
  rpc ListApiTokens(ListApiTokensRequest) returns (ListApiTokensResponse) {
    option (probe.v1.access) = ACCESS_SESSION;
  }
  // 建一个只读 API token，以 Authorization: Bearer <token> 调用 ACCESS_READ 方法。
  rpc CreateApiToken(CreateApiTokenRequest) returns (CreateApiTokenResponse) {
    option (probe.v1.access) = ACCESS_SESSION;
  }
  // 吊销：下一个用它的请求即返回 Unauthenticated，不需要重启 hub。
  rpc DeleteApiToken(DeleteApiTokenRequest) returns (DeleteApiTokenResponse) {
    option (probe.v1.access) = ACCESS_SESSION;
  }
```

文件末尾加消息：

```proto
message ApiToken {
  int64 id = 1;
  string name = 2;
  // Unix 秒。
  int64 created_at = 3;
  // 最近一次用于成功鉴权的墙钟 Unix 秒，精度一分钟；从未使用则缺失。
  optional int64 last_used_at = 4;
}

message ListApiTokensRequest {}
message ListApiTokensResponse {
  // 按 id 升序。
  repeated ApiToken tokens = 1;
}

message CreateApiTokenRequest {
  // 1–64 字符（去掉控制字符与首尾空白之后），不要求唯一。
  string name = 1;
}
message CreateApiTokenResponse {
  ApiToken api_token = 1;
  // 明文，形如 probe_at_ 加 64 位十六进制；只在此处出现一次。
  string token = 2;
}

message DeleteApiTokenRequest {
  int64 id = 1;
}
message DeleteApiTokenResponse {}
```

Run: `cd /Users/xjetry/work/vibe/probe-access && make gen > /tmp/agent-access-t3-gen.log 2>&1; echo $?`，期望 `0`（此时 `go build` 会因 `Service` 未实现新方法而失败，属预期）。

- [ ] **Step 2: 写会失败的测试**

`internal/hub/api/tokens_test.go`：

```go
package api

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"

	probev1 "github.com/xjetry/probe/gen/probe/v1"
	"github.com/xjetry/probe/internal/hub/auth"
)

type rawResult struct {
	status  int
	code    string
	message string
}

// rawCall 用纯 HTTP+JSON 调一个 AdminService 方法，headers 原样加到请求上；不带 cookie jar。
func rawCall(t *testing.T, h *harness, method string, body string, headers map[string][]string) rawResult {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, h.srv.URL+"/probe.v1.AdminService/"+method, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	for k, vs := range headers {
		for _, v := range vs {
			req.Header.Add(k, v)
		}
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var e struct{ Code, Message string }
	json.NewDecoder(resp.Body).Decode(&e)
	return rawResult{resp.StatusCode, e.Code, e.Message}
}

func bearer(tok string) map[string][]string { return map[string][]string{"Authorization": {"Bearer " + tok}} }

// sessionCookie 取 harness 登录后 jar 里的会话 cookie，供手工拼请求头。
func sessionCookie(t *testing.T, h *harness) string {
	t.Helper()
	u, _ := http.NewRequest(http.MethodGet, h.srv.URL, nil)
	for _, c := range h.http.Jar.Cookies(u.URL) {
		if c.Name == SessionCookie {
			return SessionCookie + "=" + c.Value
		}
	}
	t.Fatal("no session cookie after login")
	return ""
}

func createToken(t *testing.T, h *harness, name string) (int64, string) {
	t.Helper()
	resp, err := h.admin.CreateApiToken(context.Background(), connect.NewRequest(&probev1.CreateApiTokenRequest{Name: name}))
	if err != nil {
		t.Fatal(err)
	}
	return resp.Msg.GetApiToken().GetId(), resp.Msg.GetToken()
}

func TestAPITokenReachesExactlyTheReadMethods(t *testing.T) {
	h := newHarness(t, "")
	h.login(t)
	_, tok := createToken(t, h, "matrix")
	svc := adminService()
	for i := 0; i < svc.Methods().Len(); i++ {
		name := string(svc.Methods().Get(i).Name())
		got := rawCall(t, h, name, "{}", bearer(tok))
		if h.svc.access["/probe.v1.AdminService/"+name] == probev1.Access_ACCESS_READ {
			if got.code == "unauthenticated" || got.code == "permission_denied" {
				t.Errorf("%s: read method refused a valid token: %+v", name, got)
			}
			continue
		}
		if got.status != http.StatusForbidden || got.code != "permission_denied" ||
			!strings.Contains(got.message, name) || !strings.Contains(got.message, "read-only") {
			t.Errorf("%s: %+v, want 403 permission_denied naming the method and read-only", name, got)
		}
	}
}

func TestBearerAndCookiePathsNeverFallBack(t *testing.T) {
	h := newHarness(t, "")
	h.login(t)
	_, tok := createToken(t, h, "cross")
	cookie := sessionCookie(t, h)
	cases := []struct {
		name    string
		method  string
		headers map[string][]string
		status  int
	}{
		{"valid cookie + invalid bearer", "ListNodes", map[string][]string{"Cookie": {cookie}, "Authorization": {"Bearer " + auth.APITokenPrefix + strings.Repeat("0", 64)}}, 401},
		{"valid bearer + invalid cookie", "ListNodes", map[string][]string{"Cookie": {SessionCookie + "=garbage"}, "Authorization": {"Bearer " + tok}}, 200},
		{"valid cookie + Basic (reverse proxy auth) on read", "ListNodes", map[string][]string{"Cookie": {cookie}, "Authorization": {"Basic dXNlcjpwYXNz"}}, 200},
		{"valid cookie + Basic (reverse proxy auth) on write", "CreateNode", map[string][]string{"Cookie": {cookie}, "Authorization": {"Basic dXNlcjpwYXNz"}}, 200},
		{"lowercase scheme is still bearer", "ListNodes", map[string][]string{"Authorization": {"bearer " + tok}}, 200},
		{"empty bearer + valid cookie", "ListNodes", map[string][]string{"Cookie": {cookie}, "Authorization": {"Bearer "}}, 401},
		{"two bearer headers", "ListNodes", map[string][]string{"Authorization": {"Bearer " + tok, "Bearer " + tok}}, 401},
		{"valid bearer cannot mint tokens", "CreateApiToken", map[string][]string{"Cookie": {cookie}, "Authorization": {"Bearer " + tok}}, 403},
	}
	for _, c := range cases {
		body := "{}"
		if c.method == "CreateNode" {
			body = `{"name":"via-basic"}`
		}
		if got := rawCall(t, h, c.method, body, c.headers); got.status != c.status {
			t.Errorf("%s: status %d (%s %q), want %d", c.name, got.status, got.code, got.message, c.status)
		}
	}
}

func TestCredentialsDoNotCrossServices(t *testing.T) {
	h := newHarness(t, "")
	h.login(t)
	_, apiTok := createToken(t, h, "x")
	_, nodeTok := h.createNode(t, "n1")
	if err := h.report(t, apiTok, &probev1.Metrics{}); connect.CodeOf(err) != connect.CodeUnauthenticated {
		t.Errorf("API token accepted by AgentService.Report: %v", err)
	}
	if got := rawCall(t, h, "ListNodes", "{}", bearer(nodeTok)); got.status != 401 {
		t.Errorf("node token accepted by AdminService: %+v", got)
	}
}

func TestRevocationTakesEffectOnTheNextRequest(t *testing.T) {
	h := newHarness(t, "")
	h.login(t)
	id, tok := createToken(t, h, "gone")
	if got := rawCall(t, h, "ListNodes", "{}", bearer(tok)); got.status != 200 {
		t.Fatalf("before revocation: %+v", got)
	}
	if _, err := h.admin.DeleteApiToken(context.Background(), connect.NewRequest(&probev1.DeleteApiTokenRequest{Id: id})); err != nil {
		t.Fatal(err)
	}
	if got := rawCall(t, h, "ListNodes", "{}", bearer(tok)); got.status != 401 {
		t.Fatalf("after DeleteApiToken: %+v", got)
	}
	// probe-hub token revoke 在另一进程里直接删行：不经 api 层，同样必须立即生效。
	id2, tok2 := createToken(t, h, "gone-offline")
	if _, err := h.store.DeleteAPIToken(context.Background(), id2); err != nil {
		t.Fatal(err)
	}
	if got := rawCall(t, h, "ListNodes", "{}", bearer(tok2)); got.status != 401 {
		t.Fatalf("after direct row delete: %+v", got)
	}
}

func TestAPITokenManagementValidation(t *testing.T) {
	h := newHarness(t, "")
	h.login(t)
	ctx := context.Background()
	for _, name := range []string{"", "  \x01  ", strings.Repeat("字", 65)} {
		_, err := h.admin.CreateApiToken(ctx, connect.NewRequest(&probev1.CreateApiTokenRequest{Name: name}))
		if connect.CodeOf(err) != connect.CodeInvalidArgument {
			t.Errorf("name %q: %v, want InvalidArgument", name, err)
		}
	}
	resp, err := h.admin.CreateApiToken(ctx, connect.NewRequest(&probev1.CreateApiTokenRequest{Name: "  ci\x07  "}))
	if err != nil || resp.Msg.GetApiToken().GetName() != "ci" || !strings.HasPrefix(resp.Msg.GetToken(), auth.APITokenPrefix) {
		t.Fatalf("cleaned create: %v %v", resp, err)
	}
	for i := 1; i < auth.MaxAPITokens; i++ {
		createToken(t, h, "n")
	}
	_, err = h.admin.CreateApiToken(ctx, connect.NewRequest(&probev1.CreateApiTokenRequest{Name: "one too many"}))
	if connect.CodeOf(err) != connect.CodeResourceExhausted || !strings.Contains(err.Error(), "100") {
		t.Fatalf("token %d: %v, want ResourceExhausted naming the limit", auth.MaxAPITokens+1, err)
	}
	_, err = h.admin.DeleteApiToken(ctx, connect.NewRequest(&probev1.DeleteApiTokenRequest{Id: 999999}))
	if connect.CodeOf(err) != connect.CodeNotFound {
		t.Fatalf("delete unknown: %v, want NotFound", err)
	}
}

func TestListApiTokensShowsLastUse(t *testing.T) {
	h := newHarness(t, "")
	h.login(t)
	_, tok := createToken(t, h, "seen")
	createToken(t, h, "unseen")
	rawCall(t, h, "ListNodes", "{}", bearer(tok))
	deadline := time.Now().Add(2 * time.Second)
	for {
		resp, err := h.admin.ListApiTokens(context.Background(), connect.NewRequest(&probev1.ListApiTokensRequest{}))
		if err != nil {
			t.Fatal(err)
		}
		list := resp.Msg.GetTokens()
		if len(list) != 2 || list[0].GetName() != "seen" || list[1].GetName() != "unseen" {
			t.Fatalf("list: %v", list)
		}
		if list[1].LastUsedAt != nil {
			t.Fatalf("unused token reports a last use: %v", list[1])
		}
		if list[0].LastUsedAt != nil {
			if list[0].GetLastUsedAt() != h.clk.Now().Unix() {
				t.Fatalf("last used %d, want %d", list[0].GetLastUsedAt(), h.clk.Now().Unix())
			}
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("last use never recorded")
		}
		time.Sleep(5 * time.Millisecond)
	}
}
```

Run: `cd /Users/xjetry/work/vibe/probe-access && go test -count=1 ./internal/hub/api/ > /tmp/agent-access-t3-red.log 2>&1; echo $?`
Expected: 非 0，`*Service does not implement` / `missing method ListApiTokens`。

- [ ] **Step 3: 实现处理器**

`internal/hub/api/tokens.go`：

```go
package api

import (
	"context"
	"errors"
	"fmt"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/proto"

	probev1 "github.com/xjetry/probe/gen/probe/v1"
	"github.com/xjetry/probe/internal/hub/auth"
	"github.com/xjetry/probe/internal/hub/store"
)

func apiTokenProto(t store.APIToken) *probev1.ApiToken {
	out := &probev1.ApiToken{Id: t.ID, Name: t.Name, CreatedAt: t.CreatedAt.Unix()}
	if !t.LastUsedAt.IsZero() {
		out.LastUsedAt = proto.Int64(t.LastUsedAt.Unix())
	}
	return out
}

func (s *Service) ListApiTokens(ctx context.Context, _ *connect.Request[probev1.ListApiTokensRequest]) (*connect.Response[probev1.ListApiTokensResponse], error) {
	list, err := s.store.ListAPITokens(ctx)
	if err != nil {
		s.log.Error("listing API tokens failed", "err", err)
		return nil, internalError("listing API tokens failed")
	}
	out := &probev1.ListApiTokensResponse{}
	for _, t := range list {
		out.Tokens = append(out.Tokens, apiTokenProto(t))
	}
	return connect.NewResponse(out), nil
}

func (s *Service) CreateApiToken(ctx context.Context, req *connect.Request[probev1.CreateApiTokenRequest]) (*connect.Response[probev1.CreateApiTokenResponse], error) {
	name, err := cleanName(req.Msg.GetName())
	if err != nil {
		return nil, err
	}
	tok, plain, err := s.auth.CreateAPIToken(ctx, name)
	if errors.Is(err, store.ErrAPITokenLimit) {
		return nil, connect.NewError(connect.CodeResourceExhausted,
			fmt.Errorf("at most %d API tokens may exist; delete an unused one first", auth.MaxAPITokens))
	}
	if err != nil {
		s.log.Error("creating API token failed", "err", err)
		return nil, internalError("creating API token failed")
	}
	return connect.NewResponse(&probev1.CreateApiTokenResponse{ApiToken: apiTokenProto(tok), Token: plain}), nil
}

func (s *Service) DeleteApiToken(ctx context.Context, req *connect.Request[probev1.DeleteApiTokenRequest]) (*connect.Response[probev1.DeleteApiTokenResponse], error) {
	found, err := s.store.DeleteAPIToken(ctx, req.Msg.GetId())
	if err != nil {
		s.log.Error("deleting API token failed", "err", err)
		return nil, internalError("deleting API token failed")
	}
	if !found {
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("API token %d does not exist", req.Msg.GetId()))
	}
	return connect.NewResponse(&probev1.DeleteApiTokenResponse{}), nil
}
```

- [ ] **Step 4: 拦截器的 bearer 分支**

`service.go` 包注释改为：

```go
// Package api 实现 AdminService。
//
// 鉴权在挂载点的拦截器里裁决：每个方法以 probe.v1.access 声明准入口径，流式调用一律拒绝，
// 方法体只在准入通过后执行。凭据有两条路径：Authorization 的 scheme 为 Bearer 时走 API token，
// 此后不看 cookie；否则走会话 cookie。两条路径互不回退——若互相回退，实际生效的是较弱的那条。
package api
```

加辅助：

```go
func permissionDenied(format string, args ...any) error {
	return connect.NewError(connect.CodePermissionDenied, fmt.Errorf(format, args...))
}

// bearerCredential 按 scheme 选凭据路径。scheme 为 Bearer（大小写不敏感）即走 token 路径，
// 哪怕 token 为空，此后不再看 cookie。其他 scheme 不是 hub 的凭据，按不存在处理：反代做 Basic
// 认证时浏览器对每个请求自动附带 Authorization: Basic，若因此选了 token 路径，面板的每个请求都会被拒。
// 同一请求带多个 Bearer 无从判定该用哪个，按无效凭据处理。
func bearerCredential(h http.Header) (string, bool, error) {
	var found []string
	for _, v := range h.Values("Authorization") {
		scheme, rest, _ := strings.Cut(strings.TrimSpace(v), " ")
		if strings.EqualFold(scheme, "Bearer") {
			found = append(found, strings.TrimSpace(rest))
		}
	}
	switch {
	case len(found) == 0:
		return "", false, nil
	case len(found) > 1:
		return "", true, errors.New("multiple bearer credentials")
	case found[0] == "":
		return "", true, errors.New("empty bearer token")
	}
	return found[0], true, nil
}
```

`WrapUnary` 里，在 `ctx = context.WithValue(ctx, peerKey{}, peer)` 之后、`if level == probev1.Access_ACCESS_LOGIN` 之前插入：

```go
		if tok, isBearer, err := bearerCredential(req.Header()); isBearer {
			if err != nil {
				return nil, unauthenticated(err.Error() + "; send exactly one Authorization: Bearer <API token>")
			}
			ok, err := s.auth.AuthenticateAPIToken(ctx, tok)
			if err != nil {
				s.log.Error("API token lookup failed", "err", err)
				return nil, internalError("API token lookup failed")
			}
			if !ok {
				return nil, unauthenticated("API token unknown or revoked")
			}
			if level != probev1.Access_ACCESS_READ {
				return nil, permissionDenied("%s: API tokens are read-only; this method requires a panel session", req.Spec().Procedure)
			}
			return next(ctx, req)
		}
```

（先鉴别身份再裁决权限：无效 token 调任何方法都是 401，有效 token 调非 READ 方法是 403。`Login` 带 Bearer 也落在 403：token 路径只放行 READ。）

- [ ] **Step 5: 跑测试**

Run: `cd /Users/xjetry/work/vibe/probe-access && go test -count=1 ./internal/hub/api/ ./cmd/hub/ > /tmp/agent-access-t3-green.log 2>&1; echo $?`
Expected: `0`。

- [ ] **Step 6: 缺陷注入**

1. `bearerCredential` 的 `strings.EqualFold(scheme, "Bearer")` 改成 `scheme != ""`（见头即走 bearer），确认落地；跑 `-run NeverFallBack`，期望红在 `Basic (reverse proxy auth)` 两条。还原。
2. bearer 分支里 `!ok` 时改成落回会话路径（删掉 `return nil, unauthenticated(...)`，让控制流继续往下走到 cookie 校验），确认落地；跑 `-run NeverFallBack`，期望红在 `valid cookie + invalid bearer`。还原。
3. `level != probev1.Access_ACCESS_READ` 改成 `level == probev1.Access_ACCESS_LOGIN`，确认落地；跑 `-run ReachesExactlyTheReadMethods`，期望红在每个 SESSION 方法（含 `CreateApiToken`）。还原。
4. `case len(found) > 1:` 分支删除，确认落地；跑 `-run NeverFallBack`，期望红在 `two bearer headers`（若两个相同 token 恰好放行则红；若不红，把用例改成一个有效一个无效再注入一次，确认用例能区分）。还原。
5. `ListApiTokens` 的 option 改为 `ACCESS_READ` 并 `make gen`，确认落地；跑 `-run 'AdminAccessTable|ReachesExactly'`，期望 `TestAdminAccessTableMatchesDeclaredPolicy` 红在 `ListApiTokens`。还原（`git checkout -- proto gen web/src/gen`）。

- [ ] **Step 7: 门禁并提交**

Run: `cd /Users/xjetry/work/vibe/probe-access && make ci > /tmp/agent-access-t3-ci.log 2>&1; echo $?`，期望 `0`。

```bash
cd /Users/xjetry/work/vibe/probe-access && git add proto gen web/src/gen internal/hub/api && git commit -m "api: 只读 API token 的管理方法与 bearer 路径，两条凭据路径互不回退"
```

---

### Task 4: GetApiReference、入口卡片与 e2e

**Files:**
- Create: `proto/embed.go`
- Create: `proto/SKILL.md`
- Modify: `proto/probe/v1/admin.proto`（`GetApiReference` 与消息）
- Generated（`make gen`）
- Create: `internal/hub/api/reference.go`
- Create: `internal/hub/api/reference_test.go`
- Modify: `internal/hub/api/access_test.go`（`readMethods` 加 `GetApiReference`）
- Modify: `scripts/e2e.sh`

**Interfaces:**
- Consumes: Task 3 的 bearer 路径与 `rawCall`、`bearer`、`createToken` 测试辅助。
- Produces: 包 `github.com/xjetry/probe/proto`（包名 `protosrc`）：`var Files embed.FS`（`probe/v1/*.proto`）、`var Guide string`（`SKILL.md`）；proto `GetApiReferenceRequest/Response`、`ProtoFile`；卡片示例块的标记约定：信息串为 `sh example` 的围栏代码块。

- [ ] **Step 1: 嵌入包**

`proto/embed.go`：

```go
// Package protosrc 把 proto 源文件与 agent 入口卡片嵌入 hub，由 GetApiReference 下发：
// 不在仓库里的 agent 由此取得与 hub 同版本的 schema，注释即接口文档。
// go:embed 只能取包目录之下的文件，所以这个包放在 proto/ 根；buf 只看 .proto，不受影响。
package protosrc

import "embed"

// Files 用通配而不是逐个列出：新增的 proto 文件自动随 hub 下发，不会漏。
//
//go:embed probe/v1/*.proto
var Files embed.FS

//go:embed SKILL.md
var Guide string
```

- [ ] **Step 2: 入口卡片**

`proto/SKILL.md`（完整内容；示例块的信息串必须是 `sh example`，e2e 按它抽取）：

````markdown
---
name: probe-hub
description: 查询自托管探针 hub 的节点、实时状态、历史指标、流量、探测与告警事件。用户问起服务器在不在线、负载、流量、延迟或告警时使用。
---

# probe hub

hub 的管理接口是 Connect unary：每个方法都是 `POST $PROBE_HUB/probe.v1.AdminService/<方法>`，请求体与响应体都是 JSON。不需要生成客户端，curl 加 jq 即可。

## 进门

- `PROBE_HUB`：hub 的对外地址，如 `https://probe.example.com`，不带末尾斜杠。
- `PROBE_TOKEN`：在面板"API token"页创建的只读 token，形如 `probe_at_` 加 64 位十六进制。它只能调只读方法；写方法返回 `permission_denied`，需要在面板上操作。
- 每个请求带两个头：`Authorization: Bearer $PROBE_TOKEN` 与 `Content-Type: application/json`。

## 取 schema

方法、字段与语义都写在 proto 注释里。取与 hub 同版本的全部 proto：

```sh example
curl -fsS -H "Authorization: Bearer $PROBE_TOKEN" -H 'Content-Type: application/json' \
  --data '{}' "$PROBE_HUB/probe.v1.AdminService/GetApiReference" | jq '[.files[].path]'
```

看某个文件：把上面的 jq 换成 `jq -r '.files[] | select(.path == "probe/v1/admin.proto") | .content'`。每个 rpc 上的 `option (probe.v1.access)` 标明它是否对 token 开放（`ACCESS_READ` 才开放）。

## 约定

- int64 与 uint64 在 JSON 里是字符串（`"id": "3"`）；请求里写数字或字符串都可以。
- 时间是 Unix 秒；字段名以 `_ms` 结尾的是毫秒，以 `_s` 结尾的是秒。JSON 字段名是 proto 字段名的小驼峰（`last_seen_at` → `lastSeenAt`）。
- 缺读数与读数为 0 不同：`optional` 字段缺失表示没有读数，不要当成 0。
- 字段取默认值（0、空串、false、空列表）时在 JSON 里省略。
- 出错时 HTTP 状态非 200，响应体是 `{"code": "...", "message": "..."}`；message 写明哪个字段、违反了什么约束、期望什么取值。

## 例子

全部节点与最近一次上报时刻：

```sh example
curl -fsS -H "Authorization: Bearer $PROBE_TOKEN" -H 'Content-Type: application/json' \
  --data '{}' "$PROBE_HUB/probe.v1.AdminService/ListNodes" | jq '[.nodes[] | {id, name, lastSeenAt}]'
```

第一个节点最近一小时的 CPU（百分比；每点有样本数、均值与最大值，`ts` 与 `samples` 一一对应）：

```sh example
node=$(curl -fsS -H "Authorization: Bearer $PROBE_TOKEN" -H 'Content-Type: application/json' \
  --data '{}' "$PROBE_HUB/probe.v1.AdminService/ListNodes" | jq -r '.nodes[0].id')
now=$(date +%s)
curl -fsS -H "Authorization: Bearer $PROBE_TOKEN" -H 'Content-Type: application/json' \
  --data "$(jq -nc --arg node "$node" --argjson now "$now" '{nodeId: $node, from: ($now - 3600), to: $now}')" \
  "$PROBE_HUB/probe.v1.AdminService/QueryMetrics" | jq '{stepS, ts, cpu: [.series[] | select(.name == "cpu") | .samples[]]}'
```

最近 20 条告警事件：

```sh example
curl -fsS -H "Authorization: Bearer $PROBE_TOKEN" -H 'Content-Type: application/json' \
  --data '{"limit": 20}' "$PROBE_HUB/probe.v1.AdminService/ListAlertEvents" | jq '.events // []'
```
````

- [ ] **Step 3: proto**

`AdminService` 末尾加：

```proto
  // 入口卡片与 hub 构建时嵌入的全部 proto 源文件：不在仓库里的调用方由此取得与 hub 同版本的 schema。
  rpc GetApiReference(GetApiReferenceRequest) returns (GetApiReferenceResponse) {
    option (probe.v1.access) = ACCESS_READ;
  }
```

消息：

```proto
message GetApiReferenceRequest {}
message GetApiReferenceResponse {
  // 入口卡片（markdown，Claude Code skill 格式）：进门方式、约定与可直接运行的例子。
  string guide = 1;
  // 按路径升序。
  repeated ProtoFile files = 2;
}

message ProtoFile {
  // 相对 proto 根的路径，如 probe/v1/admin.proto。
  string path = 1;
  string content = 2;
}
```

`access_test.go` 的 `readMethods` 末尾加 `"GetApiReference"`。`make gen`。

- [ ] **Step 4: 写会失败的测试**

`internal/hub/api/reference_test.go`：

```go
package api

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"connectrpc.com/connect"

	probev1 "github.com/xjetry/probe/gen/probe/v1"
)

// 与仓库里的文件逐个比对：嵌入的通配若漏了文件或内容过期，这里红。
func TestApiReferenceServesTheRepositoryProtoAndGuide(t *testing.T) {
	h := newHarness(t, "")
	h.login(t)
	resp, err := h.admin.GetApiReference(context.Background(), connect.NewRequest(&probev1.GetApiReferenceRequest{}))
	if err != nil {
		t.Fatal(err)
	}
	root := filepath.Join("..", "..", "..", "proto")
	want := map[string]string{}
	entries, err := os.ReadDir(filepath.Join(root, "probe", "v1"))
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".proto") {
			b, err := os.ReadFile(filepath.Join(root, "probe", "v1", e.Name()))
			if err != nil {
				t.Fatal(err)
			}
			want["probe/v1/"+e.Name()] = string(b)
		}
	}
	got := map[string]string{}
	var order []string
	for _, f := range resp.Msg.GetFiles() {
		got[f.GetPath()] = f.GetContent()
		order = append(order, f.GetPath())
	}
	if len(got) != len(want) {
		t.Fatalf("served %v, repository has %d proto files", order, len(want))
	}
	for path, content := range want {
		if got[path] != content {
			t.Errorf("%s: served content differs from the repository", path)
		}
	}
	for i := 1; i < len(order); i++ {
		if order[i-1] >= order[i] {
			t.Errorf("files not in ascending path order: %v", order)
		}
	}
	guide, err := os.ReadFile(filepath.Join(root, "SKILL.md"))
	if err != nil {
		t.Fatal(err)
	}
	if resp.Msg.GetGuide() != string(guide) {
		t.Error("served guide differs from proto/SKILL.md")
	}
	for _, s := range []string{"PROBE_HUB", "PROBE_TOKEN", "```sh example"} {
		if !strings.Contains(resp.Msg.GetGuide(), s) {
			t.Errorf("guide lacks %q", s)
		}
	}
}

func TestApiReferenceIsReachableWithAToken(t *testing.T) {
	h := newHarness(t, "")
	h.login(t)
	_, tok := createToken(t, h, "agent")
	if got := rawCall(t, h, "GetApiReference", "{}", bearer(tok)); got.status != 200 {
		t.Fatalf("GetApiReference with token: %+v", got)
	}
}
```

Run: `cd /Users/xjetry/work/vibe/probe-access && go test -count=1 ./internal/hub/api/ -run ApiReference > /tmp/agent-access-t4-red.log 2>&1; echo $?`
Expected: 非 0，`missing method GetApiReference`。

- [ ] **Step 5: 实现**

`internal/hub/api/reference.go`：

```go
package api

import (
	"context"
	"io/fs"

	"connectrpc.com/connect"

	probev1 "github.com/xjetry/probe/gen/probe/v1"
	protosrc "github.com/xjetry/probe/proto"
)

// GetApiReference 下发构建时嵌入的卡片与 proto 源文件。WalkDir 按字典序遍历，
// 响应顺序因而稳定；嵌入的文件系统只读且随二进制固定，读失败只可能是构建缺陷。
func (s *Service) GetApiReference(ctx context.Context, _ *connect.Request[probev1.GetApiReferenceRequest]) (*connect.Response[probev1.GetApiReferenceResponse], error) {
	out := &probev1.GetApiReferenceResponse{Guide: protosrc.Guide}
	err := fs.WalkDir(protosrc.Files, ".", func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		b, err := fs.ReadFile(protosrc.Files, path)
		if err != nil {
			return err
		}
		out.Files = append(out.Files, &probev1.ProtoFile{Path: path, Content: string(b)})
		return nil
	})
	if err != nil {
		s.log.Error("reading embedded proto sources failed", "err", err)
		return nil, internalError("reading embedded proto sources failed")
	}
	return connect.NewResponse(out), nil
}
```

Run: `cd /Users/xjetry/work/vibe/probe-access && go test -count=1 ./internal/hub/api/ > /tmp/agent-access-t4-green.log 2>&1; echo $?`，期望 `0`。

- [ ] **Step 6: e2e——token 跨重启与卡片例子**

`scripts/e2e.sh`：在 `rpc()` 定义之后加：

```sh
# bearer 名字 请求体：用 API token 调 AdminService，不带 cookie；打印状态码，响应体落 $work/bearer-<名字>.json。
bearer() {
  name=$1; body=$2
  curl -sS -o "$work/bearer-$name.json" -w '%{http_code}' -H 'Content-Type: application/json' \
    -H "Authorization: Bearer $api_token" --data "$body" "$base/probe.v1.AdminService/$name"
}
```

在第一次 `[ "$(rpc Logout '{}')" = 200 ] || { echo "FAIL: logout"; exit 1; }` 之前插入：

```sh
[ "$(rpc CreateApiToken '{"name":"e2e"}')" = 200 ] || { echo "FAIL: CreateApiToken"; cat "$work/CreateApiToken.json"; exit 1; }
api_token=$(jq -r '.token' "$work/CreateApiToken.json")
api_token_id=$(jq -r '.apiToken.id' "$work/CreateApiToken.json")
case "$api_token" in probe_at_*) ;; *) echo "FAIL: API token lacks the probe_at_ prefix"; exit 1 ;; esac
```

在紧随其后的 `[ "$(rpc GetSnapshot '{}')" = 401 ] || { echo "FAIL: session survived logout"; exit 1; }` 之后插入：

```sh
# token 与会话是两条独立口径：登出不影响 token。
[ "$(bearer GetSnapshot '{}')" = 200 ] || { echo "FAIL: API token stopped working after logout"; cat "$work/bearer-GetSnapshot.json"; exit 1; }
```

在重启后的 `[ "$(rpc Login "$login_body")" = 200 ] || { echo "FAIL: login after restart"; exit 1; }` 之后插入：

```sh
# token 跨重启存活，只读、不能写；卡片取自 hub 实际下发的那份，其中的例子逐个在真实数据上跑。
[ "$(bearer ListNodes '{}')" = 200 ] || { echo "FAIL: API token lost across restart"; cat "$work/bearer-ListNodes.json"; exit 1; }
jq -e '(.nodes | length) == 2' "$work/bearer-ListNodes.json" > /dev/null || { echo "FAIL: ListNodes via token"; cat "$work/bearer-ListNodes.json"; exit 1; }
[ "$(bearer CreateNode '{"name":"via-token"}')" = 403 ] || { echo "FAIL: API token was allowed to write"; cat "$work/bearer-CreateNode.json"; exit 1; }
jq -e '.code == "permission_denied"' "$work/bearer-CreateNode.json" > /dev/null || { echo "FAIL: write via token not permission_denied"; exit 1; }
[ "$(bearer GetApiReference '{}')" = 200 ] || { echo "FAIL: GetApiReference via token"; exit 1; }
jq -e 'any(.files[]; .path == "probe/v1/admin.proto") and (.guide | contains("PROBE_TOKEN"))' "$work/bearer-GetApiReference.json" > /dev/null || { echo "FAIL: GetApiReference content"; exit 1; }
jq -r '.guide' "$work/bearer-GetApiReference.json" > "$work/SKILL.md"
awk -v dir="$work" '
  /^```sh example$/ { n++; file = sprintf("%s/card-example-%d.sh", dir, n); inblock = 1; next }
  inblock && /^```$/ { inblock = 0; close(file); next }
  inblock { print > file }
' "$work/SKILL.md"
examples=$(ls "$work"/card-example-*.sh 2> /dev/null | wc -l | tr -d ' ')
[ "$examples" -ge 3 ] || { echo "FAIL: expected at least 3 card examples, found $examples"; exit 1; }
for ex in "$work"/card-example-*.sh; do
  status=0
  PROBE_HUB=$base PROBE_TOKEN=$api_token sh -eu "$ex" > "$ex.out" 2> "$ex.err" || status=$?
  [ "$status" = 0 ] || { echo "FAIL: card example $ex exited $status"; cat "$ex" "$ex.err"; exit 1; }
  [ -s "$ex.out" ] && jq -e . "$ex.out" > /dev/null || { echo "FAIL: card example $ex did not print JSON"; cat "$ex" "$ex.out" "$ex.err"; exit 1; }
done
echo "card examples ok: $examples"
[ "$(rpc DeleteApiToken "$(jq -nc --arg id "$api_token_id" '{id: $id}')")" = 200 ] || { echo "FAIL: DeleteApiToken"; exit 1; }
[ "$(bearer ListNodes '{}')" = 401 ] || { echo "FAIL: revoked API token still accepted"; exit 1; }
```

（卡片例子输出空时 `[ -s ]` 失败：例子里 curl 的失败被管道末端的 jq 掩盖，所以用"非空且是 JSON"兜住，而不是只看退出码。）

- [ ] **Step 7: 跑 e2e**

Run: `cd /Users/xjetry/work/vibe/probe-access && make e2e > /tmp/agent-access-t4-e2e.log 2>&1; echo $?`
Expected: `0`，日志两轮各有 `card examples ok: 4` 与 `E2E OK`。（e2e 占 18080/18081 端口，同一时刻只能跑一份。）

- [ ] **Step 8: 缺陷注入**

1. `embed.go` 的通配改成 `probe/v1/admin.proto`，确认落地；跑 `go test -count=1 ./internal/hub/api/ -run ApiReferenceServes > /tmp/agent-access-inj-embed.log 2>&1; echo $?`，期望红在 `repository has`。还原。
2. `SKILL.md` 第二个例子里 `QueryMetrics` 改成 `QueryMetric`，确认落地；`make e2e`，期望红在 `card example …card-example-3.sh`（404 → curl -f 失败 → 输出空）。还原。
3. `reference.go` 里 `Guide: protosrc.Guide` 改成 `Guide: ""`，确认落地；跑 `-run ApiReferenceServes`，期望红在 `served guide differs`。还原。

- [ ] **Step 9: 门禁并提交**

Run: `cd /Users/xjetry/work/vibe/probe-access && make ci > /tmp/agent-access-t4-ci.log 2>&1; echo $?`，期望 `0`。

```bash
cd /Users/xjetry/work/vibe/probe-access && git add proto gen web/src/gen internal/hub/api scripts/e2e.sh && git commit -m "api: GetApiReference 下发嵌入的入口卡片与 proto 源文件，e2e 执行卡片里的例子"
```

---

### Task 5: probe-hub token 子命令与 passwd 的 token 清单

**Files:**
- Create: `cmd/hub/token.go`
- Create: `cmd/hub/token_test.go`
- Modify: `cmd/hub/passwd.go`
- Modify: `cmd/hub/passwd_test.go`
- Modify: `cmd/hub/main.go`（包注释、`case "token"`、usage）
- Modify: `scripts/e2e.sh`

**Interfaces:**
- Consumes: Task 2 的 `store.(*Store).ListAPITokens`、`DeleteAPIToken`、`DeleteAllAPITokens`、`auth.(*Auth).CreateAPIToken`；`openOffline`。
- Produces: `func runToken(args []string) error`、`func runTokenWith(args []string, out, errOut io.Writer) error`、`func reviewAPITokens(ctx context.Context, st *store.Store, w io.Writer, ask func() (bool, error), db string) error`。

- [ ] **Step 1: 写会失败的测试**

`cmd/hub/token_test.go`（库路径用 `filepath.Join(t.TempDir(), "hub.db")`，先经 `openOffline` 建两个 token 再关闭）：

```go
func seedTokens(t *testing.T, db string, names ...string) []store.APIToken {
	t.Helper()
	st, a, err := openOffline(db)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	var out []store.APIToken
	for _, n := range names {
		tok, _, err := a.CreateAPIToken(context.Background(), n)
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, tok)
	}
	return out
}

func TestTokenListAndRevoke(t *testing.T) {
	db := filepath.Join(t.TempDir(), "hub.db")
	toks := seedTokens(t, db, "ci", "laptop")
	var out, errOut bytes.Buffer
	if err := runTokenWith([]string{"list", "--db", db}, &out, &errOut); err != nil {
		t.Fatal(err)
	}
	for _, s := range []string{"ID", "NAME", "LAST USED", "ci", "laptop", "never"} {
		if !strings.Contains(out.String(), s) {
			t.Errorf("list output lacks %q:\n%s", s, out.String())
		}
	}
	out.Reset()
	if err := runTokenWith([]string{"revoke", "--db", db, "--id", fmt.Sprint(toks[0].ID)}, &out, &errOut); err != nil {
		t.Fatal(err)
	}
	if err := runTokenWith([]string{"revoke", "--db", db, "--id", fmt.Sprint(toks[0].ID)}, &out, &errOut); err == nil || !strings.Contains(err.Error(), "does not exist") {
		t.Fatalf("revoking twice: %v", err)
	}
	if err := runTokenWith([]string{"revoke", "--db", db, "--all"}, &out, &errOut); err != nil || !strings.Contains(out.String(), "revoked 1") {
		t.Fatalf("revoke --all: %v %q", err, out.String())
	}
	for _, bad := range [][]string{{"revoke", "--db", db}, {"revoke", "--db", db, "--all", "--id", "1"}, {"frobnicate", "--db", db}, {}} {
		if err := runTokenWith(bad, &out, &errOut); err == nil {
			t.Errorf("%v: no error", bad)
		}
	}
}

func TestReviewAPITokensAfterPasswordChange(t *testing.T) {
	db := filepath.Join(t.TempDir(), "hub.db")
	seedTokens(t, db, "ci")
	st, _, err := openOffline(db)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx := context.Background()
	var w bytes.Buffer
	// 非终端（管道、容器初始化）：只列出与提示，不提问、不吊销。
	if err := reviewAPITokens(ctx, st, &w, nil, db); err != nil {
		t.Fatal(err)
	}
	if list, _ := st.ListAPITokens(ctx); len(list) != 1 || !strings.Contains(w.String(), "ci") || !strings.Contains(w.String(), "probe-hub token revoke --all") {
		t.Fatalf("non-interactive review: %d tokens left, output %q", len(list), w.String())
	}
	w.Reset()
	if err := reviewAPITokens(ctx, st, &w, func() (bool, error) { return false, nil }, db); err != nil {
		t.Fatal(err)
	}
	if list, _ := st.ListAPITokens(ctx); len(list) != 1 {
		t.Fatal("answering no revoked tokens")
	}
	if err := reviewAPITokens(ctx, st, &w, func() (bool, error) { return true, nil }, db); err != nil {
		t.Fatal(err)
	}
	if list, _ := st.ListAPITokens(ctx); len(list) != 0 {
		t.Fatal("answering yes kept tokens")
	}
	w.Reset()
	asked := false
	if err := reviewAPITokens(ctx, st, &w, func() (bool, error) { asked = true; return true, nil }, db); err != nil || asked || w.Len() != 0 {
		t.Fatalf("no tokens: asked=%v output %q err %v", asked, w.String(), err)
	}
}
```

`passwd_test.go` 加一条：用管道作 stdin 只写一行密码，调用 `runPasswdWith([]string{"--db", db}, pipeReader, &prompt)`，断言返回 nil（没有阻塞等待第二行）、token 仍在、输出含 `API tokens are not revoked` 且不含 `[y/N]`（非终端不提问）。沿用该文件里已有的建管道写法。

Run: `cd /Users/xjetry/work/vibe/probe-access && go test -count=1 ./cmd/hub/ -run 'Token|Review|Passwd' > /tmp/agent-access-t5-red.log 2>&1; echo $?`
Expected: 非 0，`undefined: runTokenWith`。

- [ ] **Step 2: 实现 token 子命令**

`cmd/hub/token.go`：

```go
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"text/tabwriter"

	"github.com/xjetry/probe/internal/hub/store"
)

func runToken(args []string) error { return runTokenWith(args, os.Stdout, os.Stderr) }

// runTokenWith 直接改库。hub 每次请求都查 api_token、不缓存，所以运行中的 hub 不需要重启，
// 吊销在下一个请求即生效——这是面板不可用或管理员密码已泄漏时的应急路径。
func runTokenWith(args []string, out, errOut io.Writer) error {
	if len(args) < 1 {
		return errors.New("usage: probe-hub token list|revoke [flags]")
	}
	fs := flag.NewFlagSet("token "+args[0], flag.ContinueOnError)
	fs.SetOutput(errOut)
	db := fs.String("db", "probe.db", "SQLite database path")
	id := fs.Int64("id", 0, "API token id (revoke)")
	all := fs.Bool("all", false, "revoke every API token (revoke)")
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}
	st, _, err := openOffline(*db)
	if err != nil {
		return err
	}
	defer st.Close()
	ctx := context.Background()
	switch args[0] {
	case "list":
		list, err := st.ListAPITokens(ctx)
		if err != nil {
			return err
		}
		return printAPITokens(out, list)
	case "revoke":
		switch {
		case *all && *id != 0:
			return errors.New("--id and --all are mutually exclusive")
		case *all:
			n, err := st.DeleteAllAPITokens(ctx)
			if err != nil {
				return err
			}
			fmt.Fprintf(out, "revoked %d API tokens\n", n)
		case *id != 0:
			found, err := st.DeleteAPIToken(ctx, *id)
			if err != nil {
				return err
			}
			if !found {
				return fmt.Errorf("API token %d does not exist", *id)
			}
			fmt.Fprintf(out, "revoked API token %d\n", *id)
		default:
			return errors.New("--id or --all is required")
		}
		fmt.Fprintln(errOut, "takes effect on the next request; the hub looks up API tokens on every call")
		return nil
	default:
		return fmt.Errorf("unknown token command %q; want list or revoke", args[0])
	}
}

func printAPITokens(w io.Writer, list []store.APIToken) error {
	tw := tabwriter.NewWriter(w, 0, 8, 2, ' ', 0)
	fmt.Fprintln(tw, "ID\tNAME\tCREATED\tLAST USED")
	for _, t := range list {
		used := "never"
		if !t.LastUsedAt.IsZero() {
			used = t.LastUsedAt.Format("2006-01-02 15:04Z")
		}
		fmt.Fprintf(tw, "%d\t%s\t%s\t%s\n", t.ID, t.Name, t.CreatedAt.Format("2006-01-02 15:04Z"), used)
	}
	return tw.Flush()
}
```

`main.go`：`case "token": err = runToken(os.Args[2:])`；usage 加一行 `  token list|revoke           list or revoke API tokens (effective immediately)`；包注释的最后一句改为：`passwd 与 token 不需要重启：登录与 API token 每次读库，改密事务同时撤销旧会话。`

- [ ] **Step 3: passwd 列出 token**

`passwd.go`：`runPasswdWith` 在打印 `admin password set; …` 之后：

```go
	var ask func() (bool, error)
	if term.IsTerminal(int(in.Fd())) {
		ask = func() (bool, error) {
			fmt.Fprint(prompt, "Revoke all API tokens as well? [y/N] ")
			line, err := bufio.NewReader(in).ReadString('\n')
			if err != nil && !errors.Is(err, io.EOF) {
				return false, err
			}
			answer := strings.ToLower(strings.TrimSpace(line))
			return answer == "y" || answer == "yes", nil
		}
	}
	return reviewAPITokens(context.Background(), st, prompt, ask, *db)
```

并把原来的 `return nil` 删除。加：

```go
// reviewAPITokens 在改密后列出现存 API token。改密不连带吊销：连带吊销会让每次轮换密码都
// 静默打断自动化；代价是密码泄漏期间被创建的 token 仍然有效，所以把清单摆到改密的人面前。
// ask 为 nil 表示没有终端可问（管道、容器初始化），此时只列出并给出吊销命令，默认不吊销。
func reviewAPITokens(ctx context.Context, st *store.Store, w io.Writer, ask func() (bool, error), db string) error {
	list, err := st.ListAPITokens(ctx)
	if err != nil || len(list) == 0 {
		return err
	}
	fmt.Fprintln(w, "API tokens are not revoked by a password change. Existing tokens:")
	if err := printAPITokens(w, list); err != nil {
		return err
	}
	if ask == nil {
		fmt.Fprintf(w, "to revoke them: probe-hub token revoke --all --db %s\n", db)
		return nil
	}
	yes, err := ask()
	if err != nil || !yes {
		return err
	}
	n, err := st.DeleteAllAPITokens(ctx)
	if err != nil {
		return err
	}
	fmt.Fprintf(w, "revoked %d API tokens\n", n)
	return nil
}
```

（`readPassword` 在终端下用 `term.ReadPassword` 读，之后用新的 `bufio.Reader` 读 y/N 不会丢数据：终端按行交付，密码那行已被 `ReadPassword` 读完。非终端路径不再读任何东西，所以管道里只有一行密码时不会阻塞。）

- [ ] **Step 4: 跑测试**

Run: `cd /Users/xjetry/work/vibe/probe-access && go test -count=1 ./cmd/hub/ > /tmp/agent-access-t5-green.log 2>&1; echo $?`
Expected: `0`。

- [ ] **Step 5: e2e——运行中 CLI 吊销与改密保留 token**

`scripts/e2e.sh` 里，在 Task 4 加入的 `[ "$(bearer ListNodes '{}')" = 401 ] || { echo "FAIL: revoked API token still accepted"; exit 1; }` 之后追加：

```sh
# 改密只清会话、不动 token；运行中由另一进程吊销，下一个请求即 401。
[ "$(rpc CreateApiToken '{"name":"e2e-cli"}')" = 200 ] || { echo "FAIL: CreateApiToken (cli)"; exit 1; }
api_token=$(jq -r '.token' "$work/CreateApiToken.json")
api_token_id=$(jq -r '.apiToken.id' "$work/CreateApiToken.json")
printf '%s\n' "$admin_pw" | bin/probe-hub passwd --db "$db" > "$work/passwd2.log" 2>&1
grep -q 'API tokens are not revoked' "$work/passwd2.log" || { echo "FAIL: passwd did not list API tokens"; cat "$work/passwd2.log"; exit 1; }
[ "$(rpc GetSnapshot '{}')" = 401 ] || { echo "FAIL: session survived password change"; exit 1; }
[ "$(bearer GetSnapshot '{}')" = 200 ] || { echo "FAIL: password change revoked the API token"; exit 1; }
bin/probe-hub token revoke --db "$db" --id "$api_token_id" > "$work/token-revoke.log" 2>&1 || { echo "FAIL: probe-hub token revoke"; cat "$work/token-revoke.log"; exit 1; }
[ "$(bearer GetSnapshot '{}')" = 401 ] || { echo "FAIL: CLI revocation not effective on a running hub"; exit 1; }
[ "$(rpc Login "$login_body")" = 200 ] || { echo "FAIL: login after password change"; exit 1; }
```

（最后重新登录：脚本后续还要用会话调 `ListAlertRules` 等方法。）

Run: `cd /Users/xjetry/work/vibe/probe-access && make e2e > /tmp/agent-access-t5-e2e.log 2>&1; echo $?`，期望 `0`，两轮各一次 `E2E OK`。

- [ ] **Step 6: 缺陷注入**

1. `runPasswdWith` 里去掉 `if term.IsTerminal(...)` 的判断、始终构造 `ask`，确认落地；跑 `go test -count=1 ./cmd/hub/ -run Passwd > /tmp/agent-access-inj-passwd.log 2>&1; echo $?`，期望新用例红在输出含 `[y/N]`（管道已读到 EOF，`ask` 返回 false，但提问已经打印）。还原。
2. `reviewAPITokens` 在 `ask == nil` 时也调用 `DeleteAllAPITokens`，确认落地；跑 `-run ReviewAPITokens`，期望红在 `non-interactive review`。还原。
3. `SetAdminPassword` 的事务里加 `DELETE FROM api_token`，确认落地；`make e2e`，期望红在 `password change revoked the API token`。还原。

- [ ] **Step 7: 门禁并提交**

Run: `cd /Users/xjetry/work/vibe/probe-access && make ci > /tmp/agent-access-t5-ci.log 2>&1; echo $?`，期望 `0`。

```bash
cd /Users/xjetry/work/vibe/probe-access && git add cmd/hub scripts/e2e.sh && git commit -m "cmd/hub: token 子命令即时吊销，passwd 改密后列出现存 API token"
```

---

### Task 6: 面板 API token 页

**Files:**
- Create: `web/src/pages/ApiTokens.tsx`
- Create: `web/src/pages/ApiTokens.test.tsx`
- Modify: `web/src/App.tsx`（路由 `tokens`）
- Modify: `web/src/components/Layout.tsx`（导航 `API token`，放在"通知渠道"之后、"注册窗口"之前）
- Modify: `web/src/components/Layout.test.tsx`（若它断言导航项清单，同步加上）

**Interfaces:**
- Consumes: 生成的 `AdminService.method.listApiTokens`、`createApiToken`、`deleteApiToken`、`getApiReference`；`queryGate`、`errorBanner`、`useLatestError`、`ConfirmDelete`、`Secret`、`renderWithAdmin`。
- Produces: 路由 `/admin/tokens`，页面组件 `ApiTokens`。

- [ ] **Step 1: 写会失败的测试**

`web/src/pages/ApiTokens.test.tsx`：

```tsx
import { afterEach, expect, it, vi } from "vitest";
import { fireEvent, screen, waitFor, within } from "@testing-library/react";
import { create } from "@bufbuild/protobuf";
import { Code, ConnectError } from "@connectrpc/connect";
import { ListApiTokensResponseSchema } from "../gen/probe/v1/admin_pb";
import { renderWithAdmin, type AdminImpl } from "../test/harness";
import { ApiTokens } from "./ApiTokens";

const tokens = create(ListApiTokensResponseSchema, { tokens: [
  { id: 1n, name: "ci", createdAt: 1_700_000_000n, lastUsedAt: 1_700_000_600n },
  { id: 2n, name: "laptop", createdAt: 1_700_000_000n },
] });
const routes = [{ path: "/tokens", Component: ApiTokens }];
const render = (impl: AdminImpl) => renderWithAdmin({ listApiTokens: async () => tokens, ...impl }, routes, "/tokens");

afterEach(() => { vi.restoreAllMocks(); });

it("列表区分用过与从未使用", async () => {
  render({});
  const laptop = (await screen.findByRole("cell", { name: "laptop" })).closest("tr")!;
  expect(within(laptop).getByText("从未使用")).toBeInTheDocument();
  const ci = screen.getByRole("cell", { name: "ci" }).closest("tr")!;
  expect(within(ci).queryByText("从未使用")).toBeNull();
});

it("创建后只显示一次明文并刷新列表", async () => {
  let lists = 0;
  const created: string[] = [];
  render({
    listApiTokens: async () => { lists++; return tokens; },
    createApiToken: async (req) => { created.push(req.name); return { apiToken: { id: 3n, name: req.name, createdAt: 1n }, token: "probe_at_abc" }; },
  });
  const form = await screen.findByRole("form", { name: "新建 API token" });
  fireEvent.change(within(form).getByLabelText("名称"), { target: { value: "agent" } });
  fireEvent.click(within(form).getByRole("button", { name: "创建" }));
  expect(await screen.findByLabelText("API token agent")).toHaveTextContent("probe_at_abc");
  expect(created).toEqual(["agent"]);
  await waitFor(() => expect(lists).toBe(2));
  expect(within(form).getByLabelText("名称")).toHaveValue("");
});

it("创建失败显示 hub 的错误原文", async () => {
  render({ createApiToken: async () => { throw new ConnectError("at most 100 API tokens may exist; delete an unused one first", Code.ResourceExhausted); } });
  const form = await screen.findByRole("form", { name: "新建 API token" });
  fireEvent.change(within(form).getByLabelText("名称"), { target: { value: "x" } });
  fireEvent.click(within(form).getByRole("button", { name: "创建" }));
  expect(await screen.findByRole("alert")).toHaveTextContent("at most 100 API tokens");
});

it("吊销需要确认，确认后按 id 删除", async () => {
  const deleted: bigint[] = [];
  render({ deleteApiToken: async (req) => { deleted.push(req.id); return {}; } });
  fireEvent.click(await screen.findByRole("button", { name: "吊销 laptop" }));
  expect(deleted).toEqual([]);
  fireEvent.click(screen.getByRole("button", { name: "确认吊销 laptop" }));
  await waitFor(() => expect(deleted).toEqual([2n]));
});

it("下载的入口卡片就是 hub 下发的 guide", async () => {
  const blobs: Blob[] = [];
  vi.spyOn(URL, "createObjectURL").mockImplementation((b) => { blobs.push(b as Blob); return "blob:card"; });
  vi.spyOn(URL, "revokeObjectURL").mockImplementation(() => {});
  const clicked: HTMLAnchorElement[] = [];
  vi.spyOn(HTMLAnchorElement.prototype, "click").mockImplementation(function (this: HTMLAnchorElement) { clicked.push(this); });
  render({ getApiReference: async () => ({ guide: "---\nname: probe-hub\n---\n卡片", files: [] }) });
  fireEvent.click(await screen.findByRole("button", { name: "下载入口卡片" }));
  await waitFor(() => expect(clicked).toHaveLength(1));
  expect(clicked[0].download).toBe("SKILL.md");
  // 用 Response 读 Blob：jsdom 不一定实现 Blob.text。
  expect(await new Response(blobs[0]).text()).toBe("---\nname: probe-hub\n---\n卡片");
});

it("列表挂起时显示加载中", async () => {
  render({ listApiTokens: () => new Promise(() => {}) });
  expect(await screen.findByText("加载中…")).toBeInTheDocument();
});
```

（`ConfirmDelete` 的按钮文字以其 props 为准：`label` 用于首击按钮的可访问名，`confirm` 用于确认按钮；实现时 `label={\`吊销 ${t.name}\`}`、`confirm={\`确认吊销 ${t.name}\`}`。若 `ConfirmDelete` 的可访问名规则不同，按组件现状改测试里的名字，不改组件。）

Run: `cd /Users/xjetry/work/vibe/probe-access && pnpm --dir web exec vitest run src/pages/ApiTokens.test.tsx > /tmp/agent-access-t6-red.log 2>&1; echo $?`
Expected: 非 0，`Failed to resolve import "./ApiTokens"`。

- [ ] **Step 2: 实现页面**

`web/src/pages/ApiTokens.tsx`：

```tsx
import { createConnectQueryKey, useMutation, useQuery } from "@connectrpc/connect-query";
import { useQueryClient } from "@tanstack/react-query";
import { type FormEvent, useState } from "react";
import { errorText } from "../api/auth";
import { errorBanner, queryGate } from "../api/queryGate";
import { useLatestError } from "../api/useLatestError";
import { ConfirmDelete } from "../components/ConfirmDelete";
import { Secret } from "../components/Secret";
import { AdminService } from "../gen/probe/v1/admin_pb";

// 卡片内容以 hub 下发的为准：与 hub 同版本，面板不另存一份。
function download(filename: string, text: string) {
  const url = URL.createObjectURL(new Blob([text], { type: "text/markdown" }));
  const a = document.createElement("a");
  a.href = url;
  a.download = filename;
  a.click();
  URL.revokeObjectURL(url);
}

export function ApiTokens() {
  const qc = useQueryClient();
  const [name, setName] = useState("");
  const [secret, setSecret] = useState<{ label: string; value: string } | null>(null);
  const { error, mutationOptions } = useLatestError();
  const list = useQuery(AdminService.method.listApiTokens, {});
  const refresh = () => qc.invalidateQueries({ queryKey: createConnectQueryKey({ schema: AdminService.method.listApiTokens, cardinality: "finite" }) });
  const create = useMutation(AdminService.method.createApiToken, {
    ...mutationOptions,
    onSuccess: (r) => { setSecret({ label: `API token ${r.apiToken?.name}`, value: r.token }); setName(""); return refresh(); },
  });
  const remove = useMutation(AdminService.method.deleteApiToken, { ...mutationOptions, onSuccess: refresh });
  const reference = useMutation(AdminService.method.getApiReference, { ...mutationOptions, onSuccess: (r) => download("SKILL.md", r.guide) });
  const gate = queryGate(list);
  if (!gate.ready) return gate.loading ?? errorBanner(...gate.errors);
  const submit = (e: FormEvent<HTMLFormElement>) => {
    e.preventDefault();
    if (!e.currentTarget.checkValidity()) return;
    create.mutate({ name: name.trim() });
  };
  return (
    <section>
      {gate.banner}
      <h1>API token</h1>
      <p className="muted">
        只读凭据，供 agent 与脚本调用：以 <code>Authorization: Bearer &lt;token&gt;</code> 调用只读方法，写操作仍需在面板上完成。
        把入口卡片放进 agent 的 skills 目录，并设置 <code>PROBE_HUB={window.location.origin}</code> 与 <code>PROBE_TOKEN</code>。
      </p>
      <p>
        <button type="button" onClick={() => reference.mutate({})} disabled={reference.isPending}>下载入口卡片</button>
      </p>
      <form className="card edit-form" aria-label="新建 API token" onSubmit={submit}>
        <div className="row">
          <label>名称<input required maxLength={64} value={name} onChange={(e) => setName(e.target.value)} /></label>
          <button type="submit" disabled={create.isPending}>创建</button>
        </div>
      </form>
      {secret && <Secret label={secret.label} value={secret.value} />}
      {error != null && <p role="alert" className="error">{errorText(error)}</p>}
      <div className="table-scroll" role="region" aria-label="API token 管理" tabIndex={0}>
        <table className="nodes">
          <thead><tr><th>名称</th><th>创建于</th><th>最后使用</th><th>操作</th></tr></thead>
          <tbody>
            {gate.data.tokens.map((t) => (
              <tr key={String(t.id)}>
                <td>{t.name}</td>
                <td className="muted">{new Date(Number(t.createdAt) * 1000).toLocaleDateString()}</td>
                <td className="muted">{t.lastUsedAt == null ? "从未使用" : new Date(Number(t.lastUsedAt) * 1000).toLocaleString()}</td>
                <td>
                  <ConfirmDelete label={`吊销 ${t.name}`} confirm={`确认吊销 ${t.name}`} note="用它的请求立即失效" pending={remove.isPending}
                    onDelete={() => remove.mutate({ id: t.id })} />
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>
      {gate.data.tokens.length === 0 && <p className="muted">还没有 API token。</p>}
    </section>
  );
}
```

`App.tsx` 加 `import { ApiTokens } from "./pages/ApiTokens";` 与子路由 `{ path: "tokens", Component: ApiTokens }`（放在 `channels` 之后）。`Layout.tsx` 在 `通知渠道` 之后加 `<NavLink to="/tokens">API token</NavLink>`。

- [ ] **Step 3: 跑测试**

Run: `cd /Users/xjetry/work/vibe/probe-access && pnpm --dir web exec vitest run > /tmp/agent-access-t6-green.log 2>&1; echo $?`
Expected: `0`。

- [ ] **Step 4: 缺陷注入**

1. `download` 里 `a.download = filename` 改成 `a.download = "card.md"`，确认落地；只跑 `src/pages/ApiTokens.test.tsx`，期望红在 `SKILL.md`。还原。
2. 表格单元格的条件改成 `t.lastUsedAt === 0n ? "从未使用" : …`，确认落地；期望红在 `列表区分用过与从未使用`。还原。
3. `create` 的 `onSuccess` 去掉 `return refresh()`，确认落地；期望红在 `lists` 为 1。还原。

- [ ] **Step 5: 门禁、浏览器目测并提交**

Run: `cd /Users/xjetry/work/vibe/probe-access && make ci > /tmp/agent-access-t6-ci.log 2>&1; echo $?`，期望 `0`。

```bash
cd /Users/xjetry/work/vibe/probe-access && git add web/src && git commit -m "web: API token 页：创建、吊销与下载入口卡片"
```
