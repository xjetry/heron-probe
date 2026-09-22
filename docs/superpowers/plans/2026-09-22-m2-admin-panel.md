# M2 管理面板（二）：前端与嵌入 实现计划

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 一个嵌进 hub 二进制、挂在 `/admin/` 下的管理面板：登录、节点实时总览（2 秒轮询）、节点历史图表（uPlot，按窗口选级）、节点管理（建 / 改 / 删 / 换 token / 排序）、注册窗口。全部数据经生成的 Connect-ES 客户端调 `AdminService`；前端产物不入库，未构建时 hub 照常编译启动。

**Architecture:** `web/` 是 pnpm + Vite + React + TypeScript 工程，`base` 为 `/admin/`，产物输出到 `internal/hub/web/dist`（git 忽略，只保留占位文件让 `go:embed` 永远有目录可嵌）。`internal/hub/web` 提供静态处理器：命中文件直接返回，`assets/` 未命中 404，其余回落到 `index.html`（客户端路由），带 CSP 与缓存头；`cmd/hub` 把它挂在 `/admin/`，`/` 在公开页出现之前重定向到 `/admin/`。TS 客户端由根 `buf.gen.yaml` 的第二个插件生成到 `web/src/gen` 并入库，与 Go 生成物同一口径（`make ci` 用 `git diff --exit-code` 钉住）。数据层用 `@connectrpc/connect-query`（TanStack Query 之上），任何 `Unauthenticated` 统一跳登录页；组件测试用 `createRouterTransport` 在内存里实现服务，不起网络。

**Tech Stack:** Node 26、pnpm 12；react ^19.2、react-router ^8.4、vite ^8.3、typescript ~6.0（`erasableSyntaxOnly` 必须关掉——生成代码含 TS `enum`）、@vitejs/plugin-react ^6.1；@bufbuild/protobuf ^2.15、@bufbuild/protoc-gen-es ^2.15、@connectrpc/connect ^2.2、@connectrpc/connect-web ^2.2、@connectrpc/connect-query ^2.3、@tanstack/react-query ^5.103；uplot ^1.6.32；测试 vitest ^5、jsdom、@testing-library/react ^16。以上版本来自 2026-09-22 的 `npm view` 与一次 `/tmp` 内的脚手架实验（`tsc` 与 `vite build` 通过，产物基线 222 KB / gzip 69 KB）。

**Spec:** `docs/superpowers/specs/2026-09-17-probe-architecture-design.md` §2（前端选型）、§10（前端与公开页）、§12、§14；`docs/superpowers/plans/2026-09-22-m2-admin-api.md` 是它的前置——本计划假定该计划已合入。

## Global Constraints

**来自 spec 的硬值**

- `web/` 内 `/admin/*` 入口；公开页入口（`/`）属公开页里程碑，本计划**不建**（§10）。
- 实时数据用轮询，默认 2 秒（§10）。
- 前端产物不入库；`go build` 与 `go test` 不依赖 Node；未构建前端时 hub 照常编译与启动，页面路径返回"前端未构建"的说明（§10、§14）。
- 构建顺序：`buf generate` → 前端构建 → `go build`（§14）。
- `/admin` 与 RPC 路径的路由优先级高于任何静态服务（§10）。
- 会话是 cookie，浏览器自动携带；前端不持有任何 token（§5.3）。`Unauthenticated` 一律回到登录页。
- 无读数 ≠ 0：`MetricSample.n == 0` 的点在图上是空洞（null），不是 0（§6.2）。
- 面向 agent 设计不因面板而放宽：面板只是 `AdminService` 的一个消费者，不引入面板专用端点（`docs/guidelines/agent-first.md`）。

**本计划裁定的取值**

- 包管理器 pnpm；锁文件 `web/pnpm-lock.yaml` 入库；CI 用 `pnpm install --frozen-lockfile`。
- 生成的 TS 入库到 `web/src/gen`，与 `gen/` 同一口径；`make ci` 的幂等检查同时覆盖两处。
- Connect 传输用 JSON 编码（`useBinaryFormat: false`）：浏览器开发者工具里能直接读，体积差异对面板无意义。
- 样式用一份 `styles.css` + CSS 变量（明暗跟随 `prefers-color-scheme`），不引入 UI 框架：公开页的外观定制走 CSS 变量，面板与之同一机制。
- 静态响应头：`assets/*`（带内容哈希）`Cache-Control: public, max-age=31536000, immutable`；`index.html` `Cache-Control: no-cache`；全部页面 `Content-Security-Policy: default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline'; img-src 'self' data:; connect-src 'self'; frame-ancestors 'none'; base-uri 'none'; form-action 'none'`（`style-src` 的 `'unsafe-inline'` 给 React 与 uPlot 经 CSSOM 之外偶发写入的内联样式留余地，脚本一律不允许内联）、`X-Content-Type-Options: nosniff`、`Referrer-Policy: no-referrer`。
- `/` 在公开页出现之前 302 到 `/admin/`。
- 历史窗口预设：1h、6h、24h、7d、30d；`max_points` 固定 720。
- token 与注册 key 只展示一次：展示组件带"复制"按钮，离开页面即不可再见。

**代码与提交规范（来自用户全局规则，对子代理同样生效）**

- 注释、commit message 里禁止过程信息（任务 / 步骤编号、里程碑代号、审阅轮次、"按计划"）。写 WHY 与不变式。
- 不打补丁；不复制第二份实现；不写 TODO；同形缺陷一次改全。
- 每条新断言做缺陷注入并确认红在正确原因上。
- 判成败的命令不接管道；`go test -count=1`；`pnpm exec vitest run`（非 watch）。
- 提交信息中文，`<type>: <一句话>` + WHY。
- 每个任务结束 `make ci` 全绿（本计划 Task 1 起 `ci` 包含 `web-test` 与 `web`）。

---

## 文件结构

```
web/package.json、pnpm-lock.yaml、tsconfig.json、tsconfig.app.json、tsconfig.node.json、vite.config.ts、index.html
web/src/gen/probe/v1/*_pb.ts            生成物，入库
web/src/main.tsx                        Providers：QueryClient、TransportProvider、Router
web/src/App.tsx                         路由表与受保护布局
web/src/api/transport.ts                createConnectTransport（JSON）
web/src/api/auth.ts                     isUnauthenticated；QueryCache 全局错误 → 跳登录
web/src/lib/format.ts(+.test.ts)        bytes / percent / duration / relative time
web/src/lib/series.ts(+.test.ts)        QueryMetricsResponse → uPlot 数据（n=0 → null；缺桶补 null）
web/src/components/Layout.tsx           导航、登出
web/src/components/Chart.tsx            uPlot 封装
web/src/components/Secret.tsx           一次性展示 token / key + 复制
web/src/pages/Login.tsx(+.test.tsx)
web/src/pages/Overview.tsx(+.test.tsx)  实时总览，2s 轮询
web/src/pages/NodeDetail.tsx            历史图表 + facts
web/src/pages/Nodes.tsx(+.test.tsx)     节点管理
web/src/pages/RegisterWindow.tsx        注册窗口
web/src/styles.css
web/src/test/setup.ts                   vitest + jsdom
internal/hub/web/web.go                 go:embed dist；Handler；缺产物说明页
internal/hub/web/web_test.go
internal/hub/web/dist/.gitkeep           占位，让 embed 永远有目录
cmd/hub/serve.go                        挂 /admin/ 与 / 重定向
cmd/hub/mux_test.go                     路由优先级
buf.gen.yaml                            第二个插件：protoc-gen-es → web/src/gen
Makefile                                web-install / web-test / web / gen 的 TS 部分；ci 扩展
.github/workflows/ci.yml                pnpm 安装步骤
.gitignore                              internal/hub/web/dist/*（保留 .gitkeep）
scripts/e2e.sh                          /admin/ 返回 200 且带 CSP；/ 302
```

---

### Task 1: `web/` 工程脚手架、TS 生成与构建流水线

**必读**：spec §2、§10、§14。

**Files:**
- Create: `web/`（脚手架）、`web/src/gen/`（生成物）
- Modify: `buf.gen.yaml`、`Makefile`、`.github/workflows/ci.yml`、`.gitignore`
- Create: `internal/hub/web/dist/.gitkeep`

**Interfaces:**
- Produces: `web/src/gen/probe/v1/{types,agent,admin}_pb.ts`（`AdminService` 描述符等）；`make web`、`make web-test`、`make web-install`。

- [ ] **Step 1: 脚手架**

```bash
cd /Users/xjetry/work/vibe/probe
pnpm dlx create-vite@latest web --template react-ts > /tmp/m2b-t1-scaffold.log 2>&1; echo $?
cd web
pnpm add react@^19.2 react-dom@^19.2 react-router@^8.4 @connectrpc/connect@^2.2 @connectrpc/connect-web@^2.2 @connectrpc/connect-query@^2.3 @bufbuild/protobuf@^2.15 @tanstack/react-query@^5.103 uplot@^1.6.32 > /tmp/m2b-t1-add.log 2>&1; echo $?
pnpm add -D @bufbuild/protoc-gen-es@^2.15 vitest@^5 jsdom @testing-library/react@^16 @testing-library/jest-dom > /tmp/m2b-t1-addd.log 2>&1; echo $?
pnpm remove oxlint > /dev/null 2>&1 || true
```

删掉模板的示例文件：`src/App.css`、`src/assets/`、`public/vite.svg`、`src/App.tsx` 与 `src/main.tsx` 的示例内容（后续任务重写）。`README.md` 删除。

`web/tsconfig.app.json`：把 `"erasableSyntaxOnly": true` 改为 `false`，并在该行上方加注释（JSON 不能有注释——改为在 `web/README` 里？不，写进 `vite.config.ts` 顶部的注释里，见下）。其余保持模板默认；`include` 加 `"src"` 已有。

`web/vite.config.ts`：

```ts
import react from "@vitejs/plugin-react";
import { defineConfig } from "vite";

// base 与 hub 的挂载路径一致：产物里的资源引用都是 /admin/assets/…，
// 由 internal/hub/web 服务。outDir 直接落在 embed 目录，不再拷贝一次。
// tsconfig.app.json 关掉了 erasableSyntaxOnly：protoc-gen-es 为 proto enum
// 生成 TS enum，那条限制会拒绝生成代码。
export default defineConfig({
  base: "/admin/",
  plugins: [react()],
  build: { outDir: "../internal/hub/web/dist", emptyOutDir: true },
  test: {
    environment: "jsdom",
    setupFiles: ["./src/test/setup.ts"],
    globals: false,
  },
});
```

`vite.config.ts` 的 `test` 字段需要 vitest 的类型：文件顶部改为 `import { defineConfig } from "vitest/config";`（vitest 重导出 vite 的 `defineConfig` 并带 `test` 类型）。

`web/src/test/setup.ts`：

```ts
import "@testing-library/jest-dom/vitest";
```

`web/package.json` 的 scripts：

```json
{
  "dev": "vite",
  "build": "tsc -b && vite build",
  "test": "vitest run",
  "typecheck": "tsc -b"
}
```

`web/index.html`：`<title>probe</title>`，`<div id="root"></div>`，`<script type="module" src="/src/main.tsx"></script>`（模板已有；删 favicon 引用）。

- [ ] **Step 2: TS 生成进 `web/src/gen`**

根 `buf.gen.yaml` 追加插件：

```yaml
  - local: ["web/node_modules/.bin/protoc-gen-es"]
    out: web/src/gen
    opt: target=ts
```

（现有两个 Go 插件不动。）`make gen` 现在需要 `web/node_modules` 存在——Makefile 的 `gen` 目标依赖 `web-install`。

```bash
cd /Users/xjetry/work/vibe/probe && make gen > /tmp/m2b-t1-gen.log 2>&1; echo $?
ls web/src/gen/probe/v1/
```

预期 `types_pb.ts`、`agent_pb.ts`、`admin_pb.ts`。

- [ ] **Step 3: Makefile 与 CI**

`Makefile`：

```make
export CGO_ENABLED=0

.PHONY: gen lint test build binaries ci e2e fixtures web-install web-test web

web-install:
	pnpm --dir web install --frozen-lockfile

# TS 客户端与 Go 代码同一口径：都由 buf 生成、都入库、都由 ci 的 diff 检查钉住。
gen: web-install
	buf generate

lint:
	go mod tidy -diff
	buf lint
	go vet ./...
	GOOS=linux go vet ./...
	GOOS=darwin go vet ./...

test:
	go test -count=1 ./...

web-test: web-install
	pnpm --dir web exec vitest run

# 产物落在 internal/hub/web/dist 供 go:embed；不入库，缺产物时 hub 也能编译并给出说明页。
web: web-install
	pnpm --dir web run build

# build 验证全部已有的包在本机以及 Linux amd64、arm64 上都能编译；
# 二进制产物由 binaries 生成，只有 e2e 需要它。
build:
	go build ./...
	GOOS=linux GOARCH=amd64 go build ./...
	GOOS=linux GOARCH=arm64 go build ./...

binaries: web
	go build -o bin/probe-hub ./cmd/hub
	GOOS=linux GOARCH=amd64 go build -o bin/probe-agent-linux-amd64 ./cmd/agent
	GOOS=linux GOARCH=arm64 go build -o bin/probe-agent-linux-arm64 ./cmd/agent

ci: gen lint test web-test web build
	git diff --exit-code -- gen web/src/gen

fixtures:
	scripts/capture-proc.sh docker-debian

e2e: binaries
	scripts/e2e.sh
```

`.github/workflows/ci.yml` 在 `setup-go` 之后加：

```yaml
      - uses: pnpm/action-setup@v4
        with:
          version: 12
      - uses: actions/setup-node@v4
        with:
          node-version: 26
          cache: pnpm
          cache-dependency-path: web/pnpm-lock.yaml
```

`.gitignore`：把 `/web/dist/` 换成

```
/internal/hub/web/dist/*
!/internal/hub/web/dist/.gitkeep
```

`internal/hub/web/dist/.gitkeep` 空文件入库。

- [ ] **Step 4: 验证流水线**

```bash
cd /Users/xjetry/work/vibe/probe
make web > /tmp/m2b-t1-web.log 2>&1; echo $?
ls internal/hub/web/dist/
git status --short internal/hub/web/dist   # 应只显示无变化（产物被忽略，.gitkeep 仍在）
make ci > /tmp/m2b-t1-ci.log 2>&1; echo $?
```

`make web` 后 `dist/` 里有 `index.html` 与 `assets/`；`git status` 不显示它们。注意 `emptyOutDir: true` 会删掉 `.gitkeep`——Vite 只清空自己认得的产物？不：它清空整个目录。所以 `web` 目标末尾补一行 `touch internal/hub/web/dist/.gitkeep`，并在 Makefile 注释里写明原因（embed 目录必须始终存在且入库有文件）。

注入：临时删掉 `.gitignore` 里那两行，`git status --short` 必须列出 `dist/index.html`——证明忽略规则在起作用；改回。

- [ ] **Step 5: 提交**

```bash
git add web buf.gen.yaml Makefile .github .gitignore internal/hub/web/dist/.gitkeep
git commit -m "build: 管理面板工程脚手架与 TS 客户端生成

TS 客户端与 Go 代码同一口径：由 buf 生成、入库、由 ci 的 diff 检查钉住。
产物直接落在 embed 目录且不入库，只保留占位文件让 go:embed 永远有目录可嵌，
go build 与 go test 不依赖 Node。tsconfig 关掉 erasableSyntaxOnly：生成代码
里的 enum 会被它拒绝。"
```

---

### Task 2: `internal/hub/web`——嵌入、静态处理器、挂载与重定向

**必读**：spec §10（回落规则、assets 未命中 404 的理由、路由优先级）、§5.3（CSP 与 CORS 的关系）。

**Files:**
- Create: `internal/hub/web/web.go`、`internal/hub/web/web_test.go`
- Modify: `cmd/hub/serve.go`（挂 `/admin/`、`/` 重定向）、`cmd/hub/mux_test.go`
- Modify: `scripts/e2e.sh`

**Interfaces:**
- Produces: `web.Handler() http.Handler`（服务 `/admin/` 下的产物）；`web.RootRedirect() http.Handler`。

- [ ] **Step 1: 处理器**

`internal/hub/web/web.go`：

```go
// Package web 嵌入并服务管理面板的静态产物。
//
// 产物由 Vite 构建到本包的 dist 目录，不入库；目录里只保证有一个占位文件，
// 所以 embed 永远成立，而"有没有真的构建过"由 index.html 是否存在判定。
package web

import (
	"embed"
	"io/fs"
	"net/http"
	"path"
	"strings"
)

//go:embed all:dist
var dist embed.FS

// Prefix 是面板的挂载路径；Vite 的 base 与之一致，产物里的资源引用都以它开头。
const Prefix = "/admin/"

const csp = "default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline'; img-src 'self' data:; connect-src 'self'; frame-ancestors 'none'; base-uri 'none'; form-action 'none'"

const notBuilt = `<!doctype html><meta charset="utf-8"><title>probe</title>
<p>The admin panel has not been built into this binary. Run <code>make web</code> before <code>go build</code>, or use a release build.</p>`

// Handler 服务 /admin/ 下的文件：命中直接返回；assets/ 下未命中返回 404 而不是
// index.html——用 HTML 回应 script 标签会被浏览器按 MIME 拒绝，404 才能让缺失可见；
// 其余路径回落到 index.html 交给客户端路由。
func Handler() http.Handler { return handlerFor(dist) }

func handlerFor(root fs.FS) http.Handler {
	sub, err := fs.Sub(root, "dist")
	if err != nil {
		panic(err)
	}
	files := http.FS(sub)
	server := http.FileServer(files)
	built := true
	if _, err := fs.Stat(sub, "index.html"); err != nil {
		built = false
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Content-Security-Policy", csp)
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Referrer-Policy", "no-referrer")
		if !built {
			h.Set("Cache-Control", "no-cache")
			h.Set("Content-Type", "text/html; charset=utf-8")
			w.WriteHeader(http.StatusServiceUnavailable)
			w.Write([]byte(notBuilt))
			return
		}
		rel := strings.TrimPrefix(r.URL.Path, Prefix)
		if rel == "" {
			rel = "index.html"
		}
		if _, err := fs.Stat(sub, rel); err == nil && rel != "index.html" {
			if strings.HasPrefix(rel, "assets/") {
				// 文件名带内容哈希，可以永久缓存。
				h.Set("Cache-Control", "public, max-age=31536000, immutable")
			} else {
				h.Set("Cache-Control", "no-cache")
			}
			r2 := r.Clone(r.Context())
			r2.URL.Path = "/" + rel
			server.ServeHTTP(w, r2)
			return
		}
		if strings.HasPrefix(rel, "assets/") {
			http.NotFound(w, r)
			return
		}
		h.Set("Cache-Control", "no-cache")
		r2 := r.Clone(r.Context())
		r2.URL.Path = "/index.html"
		server.ServeHTTP(w, r2)
	})
}

// RootRedirect 把 / 送到面板：公开页尚不存在，根路径没有别的内容可以给。
func RootRedirect() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		http.Redirect(w, r, Prefix, http.StatusFound)
	})
}
```

`path` 未使用则删掉 import。`http.FileServer` 对 `/index.html` 会 301 到 `./`——这是标准库的行为：请求路径以 `/index.html` 结尾时重定向到目录。为避免它，回落分支不经 `FileServer`，直接读文件写出：

```go
func serveIndex(w http.ResponseWriter, sub fs.FS) {
	b, err := fs.ReadFile(sub, "index.html")
	if err != nil {
		http.Error(w, "index.html unreadable", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	w.Write(b)
}
```

`rel == "index.html"` 与回落两处都调用 `serveIndex`；`FileServer` 只用于命中的非 index 文件。

- [ ] **Step 2: 测试**

`internal/hub/web/web_test.go` 用 `fstest.MapFS` 造两种产物状态：

```go
package web

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"
)

func builtFS() fstest.MapFS {
	return fstest.MapFS{
		"dist/index.html":         {Data: []byte("<!doctype html><div id=root></div>")},
		"dist/assets/app-abc.js":  {Data: []byte("console.log(1)")},
		"dist/assets/app-abc.css": {Data: []byte("body{}")},
	}
}

func get(t *testing.T, h http.Handler, path string) *http.Response {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
	return rec.Result()
}

func TestServesFilesAndFallsBackToIndex(t *testing.T) {
	h := handlerFor(builtFS())
	for _, c := range []struct {
		path, wantBody, wantCache string
		wantStatus                int
	}{
		{"/admin/", "<div id=root>", "no-cache", 200},
		{"/admin/index.html", "<div id=root>", "no-cache", 200},
		{"/admin/nodes/7", "<div id=root>", "no-cache", 200},
		{"/admin/assets/app-abc.js", "console.log", "public, max-age=31536000, immutable", 200},
		{"/admin/assets/missing.js", "", "", 404},
	} {
		resp := get(t, h, c.path)
		body := new(strings.Builder)
		buf := make([]byte, 4096)
		n, _ := resp.Body.Read(buf)
		body.Write(buf[:n])
		if resp.StatusCode != c.wantStatus || !strings.Contains(body.String(), c.wantBody) {
			t.Fatalf("%s: status %d body %q", c.path, resp.StatusCode, body.String())
		}
		if c.wantCache != "" && resp.Header.Get("Cache-Control") != c.wantCache {
			t.Fatalf("%s: Cache-Control %q, want %q", c.path, resp.Header.Get("Cache-Control"), c.wantCache)
		}
		if resp.Header.Get("Content-Security-Policy") == "" || resp.Header.Get("X-Content-Type-Options") != "nosniff" {
			t.Fatalf("%s: security headers missing: %v", c.path, resp.Header)
		}
	}
}

func TestUnbuiltPanelExplainsItself(t *testing.T) {
	h := handlerFor(fstest.MapFS{"dist/.gitkeep": {}})
	resp := get(t, h, "/admin/")
	buf := make([]byte, 4096)
	n, _ := resp.Body.Read(buf)
	if resp.StatusCode != http.StatusServiceUnavailable || !strings.Contains(string(buf[:n]), "has not been built") {
		t.Fatalf("status %d body %q", resp.StatusCode, buf[:n])
	}
}

func TestRootRedirectsOnlyExactRoot(t *testing.T) {
	h := RootRedirect()
	if resp := get(t, h, "/"); resp.StatusCode != http.StatusFound || resp.Header.Get("Location") != "/admin/" {
		t.Fatalf("/: %d %q", resp.StatusCode, resp.Header.Get("Location"))
	}
	if resp := get(t, h, "/nothing"); resp.StatusCode != http.StatusNotFound {
		t.Fatalf("/nothing: %d", resp.StatusCode)
	}
}

// 嵌入的真实目录至少含占位文件：embed 指令在没有 dist 目录时编译失败，这条测试
// 钉住"目录入库"这个前提。
func TestEmbeddedDistExists(t *testing.T) {
	if _, err := dist.ReadDir("dist"); err != nil {
		t.Fatal(err)
	}
}
```

```bash
go test -count=1 ./internal/hub/web > /tmp/m2b-t2-web.log 2>&1; echo $?
```

注入：`assets/` 未命中分支改成回落 `index.html` → 红在 `/admin/assets/missing.js: status 200`。改回。去掉 CSP 头 → 红在 "security headers missing"。改回。

- [ ] **Step 3: 挂载与路由优先级**

`cmd/hub/serve.go`：

```go
	mux := newMux(mountOf(svc.Handler()), mountOf(admin.Handler()), mountOf(web.Prefix, web.Handler()), mountOf("/", web.RootRedirect()))
```

`cmd/hub/mux_test.go` 的 `newTestMux` 同步；追加：

```go
// RPC 路径与 /admin/ 的优先级高于根路径的重定向；ServeMux 按最长前缀匹配，
// 这条测试钉住三者同时挂上后仍各归各。
func TestMuxRoutesPanelAndRootAroundRPC(t *testing.T) {
	srv := httptest.NewServer(newTestMux(t))
	t.Cleanup(srv.Close)
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := client.Get(srv.URL + "/")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusFound || resp.Header.Get("Location") != "/admin/" {
		t.Fatalf("/: %d %q", resp.StatusCode, resp.Header.Get("Location"))
	}
	resp, err = client.Get(srv.URL + "/admin/nodes/1")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode == http.StatusFound || resp.Header.Get("Content-Security-Policy") == "" {
		t.Fatalf("/admin/nodes/1: %d, want the panel handler (200 when built, 503 when not) with CSP", resp.StatusCode)
	}
	resp, err = client.Post(srv.URL+"/probe.v1.AdminService/ListNodes", "application/json", strings.NewReader("{}"))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("RPC path: %d, want 401 from the service, not the panel", resp.StatusCode)
	}
}
```

`go test` 时 `dist/` 里只有 `.gitkeep`（产物不入库），所以 `/admin/nodes/1` 得到 503 + CSP；构建后是 200 + CSP。测试接受两者、拒绝重定向。

```bash
go test -count=1 ./cmd/hub > /tmp/m2b-t2-mux.log 2>&1; echo $?
```

注入：把 `mountOf("/", web.RootRedirect())` 换成 `mountOf("/", web.Handler())` → `/` 不再 302，红。改回。

- [ ] **Step 4: e2e 断言页面**

`scripts/e2e.sh` 在登录相关断言附近加：

```sh
[ "$(curl -sS -o /dev/null -w '%{http_code}' "$base/")" = 302 ] || { echo "FAIL: / must redirect to the panel"; exit 1; }
[ "$(curl -sS -o "$work/admin.html" -w '%{http_code}' "$base/admin/")" = 200 ] || { echo "FAIL: /admin/ not served"; exit 1; }
grep -q 'id="root"' "$work/admin.html" || { echo "FAIL: panel index missing root element"; exit 1; }
curl -sS -D "$work/admin.headers" -o /dev/null "$base/admin/" && grep -qi '^content-security-policy:' "$work/admin.headers" || { echo "FAIL: CSP header missing"; exit 1; }
```

`make e2e` 依赖 `binaries`，`binaries` 依赖 `web`，所以 e2e 总是带真产物运行。

```bash
make e2e > /tmp/m2b-t2-e2e.log 2>&1; echo $?
```

- [ ] **Step 5: 提交**

```bash
make ci > /tmp/m2b-t2-ci.log 2>&1; echo $?
git add internal/hub/web cmd/hub scripts/e2e.sh
git commit -m "hub: 嵌入并服务管理面板；根路径重定向到面板

命中文件直接返回，assets 下未命中 404 而不是 index.html：用 HTML 回应
script 标签会被浏览器按 MIME 拒绝，404 才让缺失可见；其余路径回落给客户端
路由。带内容哈希的资源永久缓存，index 不缓存。页面带 CSP、nosniff 与
no-referrer；脚本不允许内联。未构建产物时返回说明页而不是空白。"
```

---

### Task 3: 前端基础——传输层、登录跳转、布局、登录页、格式化函数

**必读**：spec §5.3（前端不持有凭据；`Unauthenticated` 的含义）、§10；`docs/guidelines/agent-first.md`（hub 的错误信息本身给人读）。

**Files:**
- Create: `web/src/api/transport.ts`、`web/src/api/auth.ts`
- Create: `web/src/main.tsx`、`web/src/App.tsx`（重写模板）
- Create: `web/src/components/Layout.tsx`
- Create: `web/src/pages/Login.tsx`、`web/src/pages/Login.test.tsx`
- Create: `web/src/lib/format.ts`、`web/src/lib/format.test.ts`
- Create: `web/src/styles.css`

**Interfaces:**
- Produces: `transport`；`isUnauthenticated(err)`；`router`（`createBrowserRouter`，basename `/admin`）；`Layout`（导航 + 登出 + `<Outlet/>`）；`errorText(err)`；`bytes / percent / duration / ago / formatUnit`。
- 后续任务往 `App.tsx` 的 `children` 加路由、往 `Layout` 加导航项。

- [ ] **Step 1: 传输与鉴权识别**

`web/src/api/transport.ts`：

```ts
import { createConnectTransport } from "@connectrpc/connect-web";

// JSON 编码：开发者工具里能直接读请求与响应，体积差异对面板无意义。
// baseUrl 是同源根路径：会话 cookie 由浏览器自动附带，前端不持有任何凭据。
export const transport = createConnectTransport({ baseUrl: "/", useBinaryFormat: false });
```

`web/src/api/auth.ts`：

```ts
import { Code, ConnectError } from "@connectrpc/connect";

// 会话过期、被改密码登出、从未登录，对前端是同一件事：回到登录页。
export function isUnauthenticated(err: unknown): boolean {
  return err instanceof ConnectError && err.code === Code.Unauthenticated;
}
```

- [ ] **Step 2: 入口与路由**

`web/src/main.tsx`：

```tsx
import { StrictMode } from "react";
import { createRoot } from "react-dom/client";
import { MutationCache, QueryCache, QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { TransportProvider } from "@connectrpc/connect-query";
import { RouterProvider } from "react-router";
import { transport } from "./api/transport";
import { isUnauthenticated } from "./api/auth";
import { router } from "./App";
import "./styles.css";

// 任何查询或变更得到 Unauthenticated 都跳登录页：由数据层统一识别，页面不各自判断。
// Unauthenticated 不重试——重试不会让会话复活。
const onError = (err: unknown) => {
  if (isUnauthenticated(err)) void router.navigate("/login", { replace: true });
};
const queryClient = new QueryClient({
  queryCache: new QueryCache({ onError }),
  mutationCache: new MutationCache({ onError }),
  defaultOptions: {
    queries: { retry: (count, err) => !isUnauthenticated(err) && count < 2, refetchOnWindowFocus: false },
  },
});

createRoot(document.getElementById("root")!).render(
  <StrictMode>
    <TransportProvider transport={transport}>
      <QueryClientProvider client={queryClient}>
        <RouterProvider router={router} />
      </QueryClientProvider>
    </TransportProvider>
  </StrictMode>,
);
```

`web/src/App.tsx`：

```tsx
import { createBrowserRouter } from "react-router";
import { Layout } from "./components/Layout";
import { Login } from "./pages/Login";

// basename 与 hub 的挂载路径一致。路由不做鉴权判断：谁都能打开任何页面，
// 页面里的第一次请求得到 Unauthenticated 就会被数据层送去登录。
export const router = createBrowserRouter(
  [
    { path: "/login", Component: Login },
    {
      path: "/",
      Component: Layout,
      children: [{ index: true, element: <p className="muted">从导航选择一页。</p> }],
    },
  ],
  { basename: "/admin" },
);
```

（index 路由的内容在下一任务被总览页替换。）

`web/src/components/Layout.tsx`：

```tsx
import { useMutation } from "@connectrpc/connect-query";
import { NavLink, Outlet, useNavigate } from "react-router";
import { AdminService } from "../gen/probe/v1/admin_pb";

export function Layout() {
  const navigate = useNavigate();
  const logout = useMutation(AdminService.method.logout, {
    onSuccess: () => void navigate("/login", { replace: true }),
  });
  return (
    <div className="layout">
      <nav className="nav" aria-label="主导航">
        <span className="brand">probe</span>
        <NavLink to="/" end>总览</NavLink>
        <button type="button" className="link" onClick={() => logout.mutate({})} disabled={logout.isPending}>
          登出
        </button>
      </nav>
      <main className="main">
        <Outlet />
      </main>
    </div>
  );
}
```

- [ ] **Step 3: 登录页与测试**

`web/src/pages/Login.tsx`：

```tsx
import { ConnectError } from "@connectrpc/connect";
import { useMutation } from "@connectrpc/connect-query";
import { type FormEvent, useState } from "react";
import { useNavigate } from "react-router";
import { AdminService } from "../gen/probe/v1/admin_pb";

export function Login() {
  const navigate = useNavigate();
  const [password, setPassword] = useState("");
  const login = useMutation(AdminService.method.login, {
    onSuccess: () => void navigate("/", { replace: true }),
  });
  const onSubmit = (e: FormEvent) => {
    e.preventDefault();
    login.mutate({ password });
  };
  return (
    <main className="login">
      <form onSubmit={onSubmit} className="card">
        <h1>probe</h1>
        <label>
          管理员密码
          <input
            type="password"
            autoComplete="current-password"
            value={password}
            onChange={(e) => setPassword(e.target.value)}
            autoFocus
          />
        </label>
        <button type="submit" disabled={login.isPending || password === ""}>
          登录
        </button>
        {login.error && (
          <p role="alert" className="error">
            {errorText(login.error)}
          </p>
        )}
      </form>
    </main>
  );
}

// hub 的错误信息本身就是给人读的；这里只去掉 Connect 的错误码前缀。
export function errorText(err: unknown): string {
  return err instanceof ConnectError ? err.rawMessage : String(err);
}
```

`web/src/pages/Login.test.tsx`（组件测试用 `createRouterTransport` 在内存里实现服务，不起网络；这是 Connect 官方的测试方式，后续页面测试同一夹具）：

```tsx
import { Code, ConnectError, createRouterTransport } from "@connectrpc/connect";
import { TransportProvider } from "@connectrpc/connect-query";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { createMemoryRouter, RouterProvider } from "react-router";
import { describe, expect, it, vi } from "vitest";
import { AdminService } from "../gen/probe/v1/admin_pb";
import { Login } from "./Login";

function renderLogin(login: (req: { password: string }) => Promise<Record<string, never>>) {
  const transport = createRouterTransport(({ service }) => {
    service(AdminService, { login });
  });
  const router = createMemoryRouter(
    [
      { path: "/login", Component: Login },
      { path: "/", element: <h1>home</h1> },
    ],
    { initialEntries: ["/login"] },
  );
  render(
    <TransportProvider transport={transport}>
      <QueryClientProvider client={new QueryClient()}>
        <RouterProvider router={router} />
      </QueryClientProvider>
    </TransportProvider>,
  );
  return router;
}

describe("Login", () => {
  it("提交密码，成功后回到首页", async () => {
    const login = vi.fn(async () => ({}));
    const router = renderLogin(login);
    fireEvent.change(screen.getByLabelText("管理员密码"), { target: { value: "correct horse battery" } });
    fireEvent.click(screen.getByRole("button", { name: "登录" }));
    await waitFor(() => expect(router.state.location.pathname).toBe("/"));
    expect(login).toHaveBeenCalledWith(expect.objectContaining({ password: "correct horse battery" }), expect.anything());
  });

  it("失败时原样显示 hub 的信息并留在登录页", async () => {
    const router = renderLogin(async () => {
      throw new ConnectError("wrong password", Code.Unauthenticated);
    });
    fireEvent.change(screen.getByLabelText("管理员密码"), { target: { value: "nope nope nope" } });
    fireEvent.click(screen.getByRole("button", { name: "登录" }));
    expect(await screen.findByRole("alert")).toHaveTextContent("wrong password");
    expect(router.state.location.pathname).toBe("/login");
  });

  it("空密码不能提交", () => {
    renderLogin(vi.fn(async () => ({})));
    expect(screen.getByRole("button", { name: "登录" })).toBeDisabled();
  });
});
```

若 `service(AdminService, { login })` 的类型要求完整实现，用 `Partial<ServiceImpl<typeof AdminService>>` 断言；connect v2 的 router 对未实现的方法返回 `Unimplemented`，测试只调 `login`。

- [ ] **Step 4: 格式化函数与测试**

`web/src/lib/format.ts`：

```ts
const units = ["B", "KiB", "MiB", "GiB", "TiB", "PiB"];

// 二进制单位：内存与磁盘的读数都是字节，运维习惯 GiB 而非 GB。
export function bytes(n: number | bigint): string {
  let x = Number(n);
  let i = 0;
  while (x >= 1024 && i < units.length - 1) {
    x /= 1024;
    i++;
  }
  const digits = i > 0 && x < 10 ? 1 : 0;
  return `${x.toFixed(digits)} ${units[i]}`;
}

export function percent(v: number): string {
  return `${v.toFixed(v < 10 ? 1 : 0)}%`;
}

export function duration(seconds: number | bigint): string {
  let s = Number(seconds);
  const d = Math.floor(s / 86400);
  s -= d * 86400;
  const h = Math.floor(s / 3600);
  s -= h * 3600;
  const m = Math.floor(s / 60);
  if (d > 0) return `${d}d ${h}h`;
  if (h > 0) return `${h}h ${m}m`;
  return `${m}m`;
}

// 用 hub 给的 now 而不是浏览器时钟：客户机时钟偏了也不会显示"负几秒前"。
export function ago(unixSeconds: number | bigint, now: number | bigint): string {
  const diff = Math.max(0, Number(now) - Number(unixSeconds));
  if (diff < 5) return "刚刚";
  if (diff < 60) return `${diff} 秒前`;
  if (diff < 3600) return `${Math.floor(diff / 60)} 分钟前`;
  if (diff < 86400) return `${Math.floor(diff / 3600)} 小时前`;
  return `${Math.floor(diff / 86400)} 天前`;
}

// 单位随数据来（MetricSeries.unit），显示层不查表。
export function formatUnit(v: number, unit: string): string {
  switch (unit) {
    case "percent":
      return percent(v);
    case "bytes":
      return bytes(v);
    case "count":
      return String(Math.round(v));
    default:
      return v.toFixed(2);
  }
}
```

`web/src/lib/format.test.ts`：

```ts
import { describe, expect, it } from "vitest";
import { ago, bytes, duration, formatUnit, percent } from "./format";

describe("format", () => {
  it("bytes 用二进制单位，小数只在个位数时出现", () => {
    expect(bytes(0)).toBe("0 B");
    expect(bytes(1023)).toBe("1023 B");
    expect(bytes(1024)).toBe("1.0 KiB");
    expect(bytes(15n * 1024n * 1024n)).toBe("15 MiB");
    expect(bytes(3.5 * 1024 ** 3)).toBe("3.5 GiB");
  });
  it("percent 小于 10 保留一位小数", () => {
    expect(percent(3.14159)).toBe("3.1%");
    expect(percent(42.6)).toBe("43%");
  });
  it("duration 取最大的两个单位", () => {
    expect(duration(59)).toBe("0m");
    expect(duration(3661)).toBe("1h 1m");
    expect(duration(90000n)).toBe("1d 1h");
  });
  it("ago 以 hub 的 now 为基准且不出现负数", () => {
    expect(ago(1000, 1002)).toBe("刚刚");
    expect(ago(1000, 1030)).toBe("30 秒前");
    expect(ago(1000, 1000 + 7200)).toBe("2 小时前");
    expect(ago(2000, 1000)).toBe("刚刚");
  });
  it("formatUnit 按单位分派，未知单位保留两位小数", () => {
    expect(formatUnit(50, "percent")).toBe("50%");
    expect(formatUnit(2048, "bytes")).toBe("2.0 KiB");
    expect(formatUnit(2.4, "count")).toBe("2");
    expect(formatUnit(1.23456, "")).toBe("1.23");
  });
});
```

- [ ] **Step 5: 样式**

`web/src/styles.css`（CSS 变量，明暗跟随系统；只列关键规则，其余按需补充但不引入框架）：

```css
:root {
  --bg: #f6f7f9; --card: #ffffff; --fg: #1f2328; --muted: #6b7280; --line: #e5e7eb;
  --accent: #2563eb; --ok: #16a34a; --bad: #dc2626; --warn: #d97706;
  font-family: system-ui, -apple-system, "Segoe UI", sans-serif; font-size: 14px; color: var(--fg); background: var(--bg);
}
@media (prefers-color-scheme: dark) {
  :root { --bg: #0f1115; --card: #171a21; --fg: #e5e7eb; --muted: #9ca3af; --line: #2a2f3a; --accent: #60a5fa; }
}
* { box-sizing: border-box; }
body { margin: 0; }
a { color: var(--accent); text-decoration: none; }
.layout { min-height: 100vh; display: flex; flex-direction: column; }
.nav { display: flex; gap: 1rem; align-items: center; padding: 0.6rem 1rem; border-bottom: 1px solid var(--line); background: var(--card); }
.nav .brand { font-weight: 600; margin-right: 1rem; }
.nav a.active { font-weight: 600; text-decoration: underline; }
.nav .link { margin-left: auto; }
.main { padding: 1rem; max-width: 1200px; width: 100%; margin: 0 auto; }
.card { background: var(--card); border: 1px solid var(--line); border-radius: 8px; padding: 1rem; }
.login { min-height: 100vh; display: grid; place-items: center; }
.login .card { width: 320px; display: grid; gap: 0.75rem; }
label { display: grid; gap: 0.25rem; }
input, select, button { font: inherit; padding: 0.4rem 0.6rem; border: 1px solid var(--line); border-radius: 6px; background: var(--card); color: var(--fg); }
button { cursor: pointer; }
button[disabled] { opacity: 0.5; cursor: default; }
button.link { border: none; background: none; color: var(--accent); padding: 0; }
button.danger { color: var(--bad); border-color: var(--bad); }
.error { color: var(--bad); }
.muted { color: var(--muted); }
.row { display: flex; align-items: baseline; gap: 1rem; }
table.nodes { width: 100%; border-collapse: collapse; background: var(--card); border: 1px solid var(--line); border-radius: 8px; }
table.nodes th, table.nodes td { text-align: left; padding: 0.5rem 0.75rem; border-bottom: 1px solid var(--line); white-space: nowrap; }
tr.offline td { color: var(--muted); }
.dot { display: inline-block; width: 8px; height: 8px; border-radius: 50%; margin-right: 0.5rem; }
.dot.ok { background: var(--ok); } .dot.bad { background: var(--bad); }
.bar { position: relative; min-width: 140px; height: 18px; background: var(--bg); border: 1px solid var(--line); border-radius: 4px; overflow: hidden; }
.bar .fill { height: 100%; background: var(--accent); opacity: 0.35; }
.bar span { position: absolute; inset: 0; padding: 0 0.4rem; font-size: 12px; line-height: 18px; }
.chart { width: 100%; }
.grid { display: grid; grid-template-columns: repeat(auto-fit, minmax(360px, 1fr)); gap: 1rem; }
.secret { font-family: ui-monospace, monospace; word-break: break-all; background: var(--bg); padding: 0.5rem; border-radius: 6px; }
```

- [ ] **Step 6: 跑测试、类型检查、构建**

```bash
cd /Users/xjetry/work/vibe/probe && make web-test > /tmp/m2b-t3-test.log 2>&1; echo $?
make web > /tmp/m2b-t3-build.log 2>&1; echo $?
```

注入：`Login.tsx` 的 `onSuccess` 去掉 `navigate` → 第一条测试红在 pathname；`errorText` 改成返回固定字串 → 第二条红在 alert 文本；`disabled` 去掉 `password === ""` → 第三条红。各自改回。`format.ts` 的 `ago` 去掉 `Math.max(0, …)` → `ago(2000, 1000)` 一组红。改回。

- [ ] **Step 7: 提交**

```bash
make ci > /tmp/m2b-t3-ci.log 2>&1; echo $?
git add web
git commit -m "web: 传输层、登录页与登录跳转、布局与格式化函数

会话是 cookie，前端不持有凭据；任何 Unauthenticated 由数据层统一送回登录页，
页面不各自判断，也不重试——重试不会让会话复活。hub 的错误信息本身给人读，
登录页原样显示。相对时间以 hub 下发的 now 为基准，客户机时钟偏了也不出现
负数。组件测试用内存里的路由传输实现服务，不起网络。"
```

---

### Task 4: 实时总览

**必读**：spec §10（轮询 2 秒）、§4.4（在线由 hub 裁决）、§6.2（无读数 ≠ 0）。

**Files:**
- Create: `web/src/pages/Overview.tsx`、`web/src/pages/Overview.test.tsx`
- Create: `web/src/test/harness.tsx`（把 Login.test 里的夹具抽出来共用）
- Modify: `web/src/App.tsx`（index 路由 → `Overview`）
- Modify: `web/src/pages/Login.test.tsx`（改用共用夹具）

- [ ] **Step 1: 共用夹具**

`web/src/test/harness.tsx`：

```tsx
import { createRouterTransport, type ServiceImpl } from "@connectrpc/connect";
import { TransportProvider } from "@connectrpc/connect-query";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render } from "@testing-library/react";
import type { ReactNode } from "react";
import { createMemoryRouter, RouterProvider, type RouteObject } from "react-router";
import { AdminService } from "../gen/probe/v1/admin_pb";

export type AdminImpl = Partial<ServiceImpl<typeof AdminService>>;

// 与生产入口同样的 Provider 栈，只是传输换成内存里的服务实现、路由换成内存历史。
export function renderWithAdmin(impl: AdminImpl, routes: RouteObject[], initialPath: string, extra?: ReactNode) {
  const transport = createRouterTransport(({ service }) => {
    service(AdminService, impl);
  });
  const router = createMemoryRouter(routes, { initialEntries: [initialPath] });
  const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  render(
    <TransportProvider transport={transport}>
      <QueryClientProvider client={queryClient}>
        <RouterProvider router={router} />
        {extra}
      </QueryClientProvider>
    </TransportProvider>,
  );
  return { router, queryClient };
}
```

`Login.test.tsx` 的 `renderLogin` 改为调用 `renderWithAdmin({ login }, [...], "/login")`。

- [ ] **Step 2: 总览页**

`web/src/pages/Overview.tsx`：

```tsx
import { useQuery } from "@connectrpc/connect-query";
import { Link } from "react-router";
import { AdminService, type NodeStatus } from "../gen/probe/v1/admin_pb";
import { ago, bytes, percent } from "../lib/format";

// 实时视图靠轮询；hub 的上报间隔不会更短，2 秒是让"刚上报"尽快可见的取值。
export const POLL_MS = 2000;

export function Overview() {
  const snap = useQuery(AdminService.method.getSnapshot, {}, { refetchInterval: POLL_MS });
  if (snap.isPending) return <p className="muted">加载中…</p>;
  if (snap.error) return <p role="alert" className="error">{snap.error.rawMessage}</p>;
  const now = Number(snap.data.now);
  const online = snap.data.nodes.filter((n) => n.online).length;
  return (
    <section>
      <header className="row">
        <h1>总览</h1>
        <span className="muted">{online} / {snap.data.nodes.length} 在线</span>
      </header>
      {snap.data.nodes.length === 0 && (
        <p className="muted">
          还没有节点。去 <Link to="/nodes">节点</Link> 页创建，或开一个 <Link to="/register">注册窗口</Link>。
        </p>
      )}
      <table className="nodes">
        <thead>
          <tr><th>节点</th><th>CPU</th><th>内存</th><th>磁盘</th><th>负载</th><th>网络</th><th>最近上报</th></tr>
        </thead>
        <tbody>
          {snap.data.nodes.map((n) => <NodeRow key={String(n.id)} node={n} now={now} />)}
        </tbody>
      </table>
    </section>
  );
}

function NodeRow({ node, now }: { node: NodeStatus; now: number }) {
  const m = node.metrics;
  return (
    <tr className={node.online ? "online" : "offline"}>
      <td>
        <span className={`dot ${node.online ? "ok" : "bad"}`} role="img" aria-label={node.online ? "在线" : "离线"} />
        <Link to={`/nodes/${node.id}`}>{node.name}</Link>
      </td>
      <td>{m?.cpuPct !== undefined ? <Bar value={m.cpuPct} label={percent(m.cpuPct)} /> : <Missing />}</td>
      <td>{m?.memUsed !== undefined && m.memTotal ? <Bar value={ratio(m.memUsed, m.memTotal)} label={`${bytes(m.memUsed)} / ${bytes(m.memTotal)}`} /> : <Missing />}</td>
      <td>{m?.diskUsed !== undefined && m.diskTotal ? <Bar value={ratio(m.diskUsed, m.diskTotal)} label={`${bytes(m.diskUsed)} / ${bytes(m.diskTotal)}`} /> : <Missing />}</td>
      <td>{m?.load1 !== undefined ? `${m.load1.toFixed(2)} / ${m.load5?.toFixed(2) ?? "–"} / ${m.load15?.toFixed(2) ?? "–"}` : <Missing />}</td>
      <td>{m?.netRxBps !== undefined && m.netTxBps !== undefined ? `↓ ${bytes(m.netRxBps)}/s ↑ ${bytes(m.netTxBps)}/s` : <Missing />}</td>
      <td className="muted">{node.lastSeenAt !== undefined ? ago(node.lastSeenAt, now) : "从未"}</td>
    </tr>
  );
}

function ratio(used: bigint, total: bigint): number {
  return (Number(used) / Number(total)) * 100;
}

// 无读数与 0 是两个事实：缺失的字段显示为破折号，不画成 0。
function Missing() {
  return <span className="muted" aria-label="无读数">–</span>;
}

export function Bar({ value, label }: { value: number; label: string }) {
  const v = Math.max(0, Math.min(100, value));
  return (
    <div className="bar" role="meter" aria-valuenow={Math.round(v)} aria-valuemin={0} aria-valuemax={100} aria-label={label}>
      <div className="fill" style={{ width: `${v}%` }} />
      <span>{label}</span>
    </div>
  );
}
```

`App.tsx` 的 index 路由改为 `{ index: true, Component: Overview }`。

- [ ] **Step 3: 测试**

`web/src/pages/Overview.test.tsx`：

```tsx
import { screen, waitFor, within } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { renderWithAdmin } from "../test/harness";
import { Overview, POLL_MS } from "./Overview";

const snapshot = {
  now: 1_000_000n,
  reportIntervalMs: 10_000,
  nodes: [
    {
      id: 1n, name: "web-01", online: true, lastSeenAt: 999_990n,
      metrics: { cpuPct: 42, memUsed: 512n * 1024n * 1024n, memTotal: 1024n * 1024n * 1024n, load1: 0.5, load5: 0.4, load15: 0.3, netRxBps: 1024n, netTxBps: 2048n },
    },
    { id: 2n, name: "never", online: false },
  ],
};

afterEach(() => vi.useRealTimers());

describe("Overview", () => {
  it("在线计数、读数条与无读数的破折号", async () => {
    renderWithAdmin({ getSnapshot: async () => snapshot }, [{ path: "/", Component: Overview }], "/");
    expect(await screen.findByText("1 / 2 在线")).toBeInTheDocument();
    const rows = screen.getAllByRole("row").slice(1);
    const web = within(rows[0]);
    expect(web.getByRole("img", { name: "在线" })).toBeInTheDocument();
    expect(web.getByRole("meter", { name: "42%" })).toHaveAttribute("aria-valuenow", "42");
    expect(web.getByRole("meter", { name: "512 MiB / 1.0 GiB" })).toHaveAttribute("aria-valuenow", "50");
    expect(web.getByText("10 秒前")).toBeInTheDocument();
    const never = within(rows[1]);
    expect(never.getByRole("img", { name: "离线" })).toBeInTheDocument();
    expect(never.getAllByLabelText("无读数")).toHaveLength(5);
    expect(never.getByText("从未")).toBeInTheDocument();
    expect(web.getByRole("link", { name: "web-01" })).toHaveAttribute("href", "/nodes/1");
  });

  it("按 POLL_MS 轮询", async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true });
    const getSnapshot = vi.fn(async () => snapshot);
    renderWithAdmin({ getSnapshot }, [{ path: "/", Component: Overview }], "/");
    await waitFor(() => expect(getSnapshot).toHaveBeenCalledTimes(1));
    await vi.advanceTimersByTimeAsync(POLL_MS + 100);
    await waitFor(() => expect(getSnapshot.mock.calls.length).toBeGreaterThanOrEqual(2));
  });

  it("没有节点时给出去处", async () => {
    renderWithAdmin({ getSnapshot: async () => ({ now: 1n, reportIntervalMs: 10_000, nodes: [] }) }, [{ path: "/", Component: Overview }], "/");
    expect(await screen.findByText(/还没有节点/)).toBeInTheDocument();
    expect(screen.getByRole("link", { name: "注册窗口" })).toHaveAttribute("href", "/register");
  });
});
```

```bash
cd /Users/xjetry/work/vibe/probe && make web-test > /tmp/m2b-t4-test.log 2>&1; echo $?
```

注入：`Missing` 改为显示 `0` → 第一条红在 `getAllByLabelText("无读数")`；`refetchInterval` 去掉 → 第二条红在调用次数；`online` 计数改成 `nodes.length` → 第一条红在 "1 / 2 在线"。各自改回。

- [ ] **Step 4: 提交**

```bash
make ci > /tmp/m2b-t4-ci.log 2>&1; echo $?
git add web
git commit -m "web: 实时总览——两秒轮询快照，无读数显示为破折号

在线由 hub 裁决，页面只展示；缺失的读数是一个事实而不是零，显示为破折号，
不画成空条。相对时间以快照里的 now 为基准。"
```

---

### Task 5: 节点详情——历史图表与主机信息

**必读**：spec §6.2（`x_n = 0` 是无数据）、§6.5（选级由 hub 决定，随数据返回）、§10。

**Files:**
- Create: `web/src/lib/series.ts`、`web/src/lib/series.test.ts`
- Create: `web/src/components/Chart.tsx`
- Create: `web/src/pages/NodeDetail.tsx`、`web/src/pages/NodeDetail.test.tsx`
- Modify: `web/src/App.tsx`（`nodes/:id`）

- [ ] **Step 1: 响应 → uPlot 数据**

`web/src/lib/series.ts`：

```ts
import type { AlignedData } from "uplot";
import type { QueryMetricsResponse } from "../gen/probe/v1/admin_pb";

// 把 hub 的响应铺成 uPlot 的对齐数组。x 轴按 step 补全 [from, to) 的网格：
// 缺桶与 n = 0 都是 null——无读数在图上是空洞，不是 0。均值直接用 hub 给的 mean。
export function toAligned(resp: QueryMetricsResponse, names: string[], from: number, to: number): AlignedData {
  const step = resp.stepS;
  const start = from - (from % step);
  const xs: number[] = [];
  for (let t = start; t < to; t += step) xs.push(t);
  const at = new Map<number, number>();
  resp.ts.forEach((t, i) => at.set(Number(t), i));
  const columns = names.map((name) => {
    const s = resp.series.find((x) => x.name === name);
    return xs.map((t) => {
      const i = at.get(t);
      const sample = i === undefined ? undefined : s?.samples[i];
      return sample !== undefined && sample.n > 0 && sample.mean !== undefined ? sample.mean : null;
    });
  });
  return [xs, ...columns] as AlignedData;
}

export function unitOf(resp: QueryMetricsResponse, name: string): string {
  return resp.series.find((x) => x.name === name)?.unit ?? "";
}
```

`web/src/lib/series.test.ts`：

```ts
import { describe, expect, it } from "vitest";
import { toAligned, unitOf } from "./series";

const resp = {
  level: "1m",
  stepS: 180,
  ts: [0n, 180n, 540n],
  series: [
    { name: "cpu", unit: "percent", samples: [{ n: 3, mean: 1, max: 2 }, { n: 3, mean: 2, max: 3 }, { n: 1, mean: 9, max: 9 }] },
    { name: "swap_used", unit: "bytes", samples: [{ n: 0 }, { n: 0 }, { n: 0 }] },
  ],
};

describe("toAligned", () => {
  it("按 step 补全网格；缺桶与 n=0 都是 null", () => {
    const [xs, cpu, swap] = toAligned(resp, ["cpu", "swap_used"], 30, 720);
    expect(xs).toEqual([0, 180, 360, 540]);
    expect(cpu).toEqual([1, 2, null, 9]);
    expect(swap).toEqual([null, null, null, null]);
  });
  it("未知指标整列 null，单位为空串", () => {
    const [, ghost] = toAligned(resp, ["ghost"], 0, 360);
    expect(ghost).toEqual([null, null]);
    expect(unitOf(resp, "ghost")).toBe("");
    expect(unitOf(resp, "cpu")).toBe("percent");
  });
});
```

- [ ] **Step 2: uPlot 封装**

`web/src/components/Chart.tsx`：

```tsx
import { useEffect, useRef } from "react";
import uPlot, { type AlignedData, type Options } from "uplot";
import "uplot/dist/uPlot.min.css";
import { formatUnit } from "../lib/format";

const palette = ["#3b82f6", "#f59e0b", "#10b981", "#ef4444"];

// spanGaps 关闭：null 是无读数，线在这里必须断开而不是把两侧连起来。
export function Chart({ data, labels, unit, height = 180 }: { data: AlignedData; labels: string[]; unit: string; height?: number }) {
  const el = useRef<HTMLDivElement>(null);
  const plot = useRef<uPlot | null>(null);
  const key = labels.join("|");
  useEffect(() => {
    const host = el.current;
    if (!host) return;
    const opts: Options = {
      width: host.clientWidth || 600,
      height,
      scales: { x: { time: true }, y: unit === "percent" ? { range: [0, 100] } : {} },
      axes: [{}, { values: (_u, vals) => vals.map((v) => formatUnit(v, unit)) }],
      series: [
        {},
        ...labels.map((label, i) => ({
          label,
          stroke: palette[i % palette.length],
          width: 1.5,
          spanGaps: false,
          value: (_u: uPlot, v: number | null) => (v === null ? "–" : formatUnit(v, unit)),
        })),
      ],
    };
    plot.current = new uPlot(opts, data, host);
    const ro = new ResizeObserver(() => plot.current?.setSize({ width: host.clientWidth, height }));
    ro.observe(host);
    return () => {
      ro.disconnect();
      plot.current?.destroy();
      plot.current = null;
    };
    // data 的更新走 setData，不重建图表。
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [key, unit, height]);
  useEffect(() => {
    plot.current?.setData(data);
  }, [data]);
  return <div ref={el} className="chart" />;
}
```

（没有装 eslint，`eslint-disable` 注释无效则删掉，改为把 `data` 的首次值经 `useRef` 传入。）

- [ ] **Step 3: 详情页**

`web/src/pages/NodeDetail.tsx`：

```tsx
import { useQuery } from "@connectrpc/connect-query";
import { useEffect, useMemo, useState } from "react";
import { Link, useParams } from "react-router";
import { Chart } from "../components/Chart";
import { AdminService } from "../gen/probe/v1/admin_pb";
import { toAligned, unitOf } from "../lib/series";

export const RANGES = [
  { label: "1h", seconds: 3600 },
  { label: "6h", seconds: 6 * 3600 },
  { label: "24h", seconds: 86400 },
  { label: "7d", seconds: 7 * 86400 },
  { label: "30d", seconds: 30 * 86400 },
];

// 每个面板画哪些指标；名字与 hub 的描述表一致，单位随数据来。
const PANELS: { title: string; names: string[] }[] = [
  { title: "CPU", names: ["cpu"] },
  { title: "内存 / 交换", names: ["mem_used", "swap_used"] },
  { title: "磁盘", names: ["disk_used"] },
  { title: "负载（1 分钟）", names: ["load1"] },
  { title: "连接数", names: ["tcp", "udp"] },
  { title: "进程数", names: ["procs"] },
];

const REFRESH_MS = 60_000;

export function NodeDetail() {
  const { id } = useParams();
  const nodeId = BigInt(id ?? "0");
  const [range, setRange] = useState(RANGES[2]);
  // 窗口右端每分钟前进一次：历史行本来就按分钟产生，更频繁的刷新看不到新东西。
  const [to, setTo] = useState(() => Math.floor(Date.now() / 1000) + 60);
  useEffect(() => {
    const t = setInterval(() => setTo(Math.floor(Date.now() / 1000) + 60), REFRESH_MS);
    return () => clearInterval(t);
  }, []);
  const from = to - range.seconds;

  const nodes = useQuery(AdminService.method.listNodes, {});
  const node = nodes.data?.nodes.find((n) => n.id === nodeId);
  const history = useQuery(AdminService.method.queryMetrics, { nodeId, from: BigInt(from), to: BigInt(to), maxPoints: 720 });
  const charts = useMemo(
    () => history.data ? PANELS.map((p) => ({ ...p, data: toAligned(history.data, p.names, from, to), unit: unitOf(history.data, p.names[0]) })) : [],
    [history.data, from, to],
  );

  if (nodes.error) return <p role="alert" className="error">{nodes.error.rawMessage}</p>;
  if (nodes.data && !node) return <p role="alert" className="error">节点 {id} 不存在。<Link to="/">返回总览</Link></p>;
  return (
    <section>
      <header className="row">
        <h1>{node?.name ?? "…"}</h1>
        <nav aria-label="时间窗口">
          {RANGES.map((r) => (
            <button key={r.label} type="button" className={r.label === range.label ? "active" : "link"} onClick={() => setRange(r)} aria-pressed={r.label === range.label}>
              {r.label}
            </button>
          ))}
        </nav>
        {history.data && <span className="muted">级别 {history.data.level}，每点 {history.data.stepS}s</span>}
      </header>
      {history.error && <p role="alert" className="error">{history.error.rawMessage}</p>}
      <div className="grid">
        {charts.map((c) => (
          <div className="card" key={c.title}>
            <h2>{c.title}</h2>
            <Chart data={c.data} labels={c.names} unit={c.unit} />
          </div>
        ))}
      </div>
      {node?.facts && (
        <dl className="card facts">
          <dt>主机名</dt><dd>{node.facts.hostname}</dd>
          <dt>系统</dt><dd>{node.facts.os}</dd>
          <dt>内核</dt><dd>{node.facts.kernel}</dd>
          <dt>架构</dt><dd>{node.facts.arch}</dd>
          <dt>CPU</dt><dd>{node.facts.cpuModel} × {node.facts.cpuCores}</dd>
          <dt>虚拟化</dt><dd>{node.facts.virtualization || "无 / 未知"}</dd>
          <dt>agent</dt><dd>{node.facts.agentVersion}</dd>
        </dl>
      )}
    </section>
  );
}
```

`App.tsx` 的 `children` 加 `{ path: "nodes/:id", Component: NodeDetail }`。

- [ ] **Step 4: 测试**

jsdom 没有 canvas，`Chart` 在测试里替换成只记录入参的桩：

`web/src/pages/NodeDetail.test.tsx`：

```tsx
import { screen, fireEvent } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";
import { renderWithAdmin } from "../test/harness";
import { NodeDetail } from "./NodeDetail";

vi.mock("../components/Chart", () => ({
  Chart: ({ labels, unit, data }: { labels: string[]; unit: string; data: unknown[] }) => (
    <div data-testid="chart" data-labels={labels.join(",")} data-unit={unit} data-points={String((data[0] as unknown[]).length)} />
  ),
}));

const listNodes = async () => ({
  nodes: [{ id: 7n, name: "db-01", public: false, note: "", sortOrder: 0, createdAt: 0n,
    facts: { hostname: "db-01.internal", os: "Debian 12", kernel: "6.1", arch: "amd64", virtualization: "kvm", cpuModel: "EPYC", cpuCores: 4, agentVersion: "dev", icmpAvailable: false } }],
});

describe("NodeDetail", () => {
  it("按面板画图，单位随数据，显示 hub 选定的级别", async () => {
    const queryMetrics = vi.fn(async () => ({
      level: "5m", stepS: 300, ts: [],
      series: [{ name: "cpu", unit: "percent", samples: [] }, { name: "mem_used", unit: "bytes", samples: [] }],
    }));
    renderWithAdmin({ listNodes, queryMetrics }, [{ path: "/nodes/:id", Component: NodeDetail }], "/nodes/7");
    expect(await screen.findByRole("heading", { level: 1, name: "db-01" })).toBeInTheDocument();
    expect(await screen.findByText(/级别 5m，每点 300s/)).toBeInTheDocument();
    const charts = screen.getAllByTestId("chart");
    expect(charts).toHaveLength(6);
    expect(charts[0]).toHaveAttribute("data-unit", "percent");
    expect(charts[1]).toHaveAttribute("data-labels", "mem_used,swap_used");
    expect(charts[1]).toHaveAttribute("data-unit", "bytes");
    expect(screen.getByText("db-01.internal")).toBeInTheDocument();
    const req = queryMetrics.mock.calls[0][0] as { nodeId: bigint; from: bigint; to: bigint; maxPoints: number };
    expect(req.nodeId).toBe(7n);
    expect(Number(req.to - req.from)).toBe(86400);
    expect(req.maxPoints).toBe(720);
  });

  it("切换窗口重新查询", async () => {
    const queryMetrics = vi.fn(async () => ({ level: "1m", stepS: 60, ts: [], series: [] }));
    renderWithAdmin({ listNodes, queryMetrics }, [{ path: "/nodes/:id", Component: NodeDetail }], "/nodes/7");
    await screen.findByRole("heading", { level: 1, name: "db-01" });
    fireEvent.click(screen.getByRole("button", { name: "7d" }));
    await vi.waitFor(() => expect(queryMetrics.mock.calls.length).toBeGreaterThanOrEqual(2));
    const last = queryMetrics.mock.calls.at(-1)![0] as { from: bigint; to: bigint };
    expect(Number(last.to - last.from)).toBe(7 * 86400);
  });

  it("不存在的节点给出返回链接", async () => {
    renderWithAdmin({ listNodes, queryMetrics: async () => ({ level: "1m", stepS: 60, ts: [], series: [] }) }, [{ path: "/nodes/:id", Component: NodeDetail }], "/nodes/99");
    expect(await screen.findByRole("alert")).toHaveTextContent("节点 99 不存在");
  });
});
```

```bash
cd /Users/xjetry/work/vibe/probe && make web-test > /tmp/m2b-t5-test.log 2>&1; echo $?
make web > /tmp/m2b-t5-build.log 2>&1; echo $?
```

注入：`toAligned` 里 `sample.n > 0 &&` 去掉 → `series.test` 的 swap 一组红（`n=0` 但 `mean` 缺失仍是 null……这条注入不红；改为让 `n=0` 的样本带 `mean: 0` 再注入，即测试数据 swap 的 samples 写成 `{ n: 0, mean: 0 }`——这才是 hub 不会发但客户端必须防的形状？hub 保证 n=0 时 mean 缺失，客户端以 n 为准是双重保险）。**裁决**：测试数据里 swap 的样本改为 `{ n: 0, mean: 0 }`，断言仍是全 null；注入去掉 `sample.n > 0` 后红在 swap 列出现 0。`NodeDetail` 的 `maxPoints: 720` 改 100 → 第一条红。各自改回。

- [ ] **Step 5: 提交**

```bash
make ci > /tmp/m2b-t5-ci.log 2>&1; echo $?
git add web
git commit -m "web: 节点详情——按窗口查询历史并用 uPlot 绘图

级别与步长由 hub 选定并随数据返回，页面只按 step 补全网格；缺桶与样本数
为零的点都是 null，折线在那里断开而不是连成 0。单位随每条序列下发，
显示层不查表。窗口右端每分钟前进一次，与分钟行的产生节奏一致。"
```

---

### Task 6: 节点管理与注册窗口

**必读**：spec §5.1（token 明文只返回一次；轮换后旧 token 立即失效）、§5.2（窗口）、§4.8。

**Files:**
- Create: `web/src/components/Secret.tsx`
- Create: `web/src/pages/Nodes.tsx`、`web/src/pages/Nodes.test.tsx`
- Create: `web/src/pages/RegisterWindow.tsx`、`web/src/pages/RegisterWindow.test.tsx`
- Modify: `web/src/App.tsx`（`nodes`、`register`）、`web/src/components/Layout.tsx`（导航项）

- [ ] **Step 1: 一次性展示**

`web/src/components/Secret.tsx`：

```tsx
import { useState } from "react";

// token 与注册 key 只在创建那一次返回；hub 只存哈希，离开这个页面就再也看不到。
export function Secret({ label, value }: { label: string; value: string }) {
  const [copied, setCopied] = useState(false);
  const copy = async () => {
    await navigator.clipboard.writeText(value);
    setCopied(true);
  };
  return (
    <div className="card">
      <p><strong>{label}</strong> —— 只显示这一次，hub 不保存明文。</p>
      <code className="secret" aria-label={label}>{value}</code>
      <p>
        <button type="button" onClick={copy}>{copied ? "已复制" : "复制"}</button>
      </p>
    </div>
  );
}
```

- [ ] **Step 2: 节点管理页**

`web/src/pages/Nodes.tsx`：

```tsx
import { useMutation, useQuery } from "@connectrpc/connect-query";
import { useQueryClient } from "@tanstack/react-query";
import { type FormEvent, useState } from "react";
import { Link } from "react-router";
import { Secret } from "../components/Secret";
import { AdminService, type Node } from "../gen/probe/v1/admin_pb";
import { errorText } from "./Login";

export function Nodes() {
  const qc = useQueryClient();
  const nodes = useQuery(AdminService.method.listNodes, {});
  const refresh = () => qc.invalidateQueries();
  const [secret, setSecret] = useState<{ label: string; value: string } | null>(null);
  const [name, setName] = useState("");

  const create = useMutation(AdminService.method.createNode, {
    onSuccess: (r) => { setSecret({ label: `节点 ${r.node?.name} 的 token`, value: r.token }); setName(""); void refresh(); },
  });
  const update = useMutation(AdminService.method.updateNode, { onSuccess: () => void refresh() });
  const remove = useMutation(AdminService.method.deleteNode, { onSuccess: () => void refresh() });
  const rotate = useMutation(AdminService.method.rotateNodeToken, {
    onSuccess: (r, req) => { setSecret({ label: `节点 ${req.id} 的新 token`, value: r.token }); void refresh(); },
  });
  const reorder = useMutation(AdminService.method.reorderNodes, { onSuccess: () => void refresh() });

  const onCreate = (e: FormEvent) => { e.preventDefault(); create.mutate({ name }); };
  // 排序接口要求给出全部 id 的完整排列：交换相邻两项后整表提交。
  const move = (list: Node[], i: number, dir: -1 | 1) => {
    const ids = list.map((n) => n.id);
    const j = i + dir;
    if (j < 0 || j >= ids.length) return;
    [ids[i], ids[j]] = [ids[j], ids[i]];
    reorder.mutate({ ids });
  };
  const anyError = create.error ?? update.error ?? remove.error ?? rotate.error ?? reorder.error;

  if (nodes.isPending) return <p className="muted">加载中…</p>;
  if (nodes.error) return <p role="alert" className="error">{nodes.error.rawMessage}</p>;
  const list = nodes.data.nodes;
  return (
    <section>
      <h1>节点</h1>
      {secret && <Secret label={secret.label} value={secret.value} />}
      <form onSubmit={onCreate} className="row">
        <label>新节点名称<input value={name} onChange={(e) => setName(e.target.value)} /></label>
        <button type="submit" disabled={create.isPending || name.trim() === ""}>创建</button>
      </form>
      {anyError && <p role="alert" className="error">{errorText(anyError)}</p>}
      <table className="nodes">
        <thead><tr><th>排序</th><th>名称</th><th>公开</th><th>备注</th><th>创建于</th><th>操作</th></tr></thead>
        <tbody>
          {list.map((n, i) => (
            <NodeEditor key={String(n.id)} node={n}
              onMoveUp={() => move(list, i, -1)} onMoveDown={() => move(list, i, 1)}
              onSave={(patch) => update.mutate({ id: n.id, ...patch })}
              onDelete={() => remove.mutate({ id: n.id })}
              onRotate={() => rotate.mutate({ id: n.id })} />
          ))}
        </tbody>
      </table>
    </section>
  );
}

function NodeEditor({ node, onMoveUp, onMoveDown, onSave, onDelete, onRotate }: {
  node: Node;
  onMoveUp: () => void; onMoveDown: () => void;
  onSave: (patch: { name: string; public: boolean; note: string }) => void;
  onDelete: () => void; onRotate: () => void;
}) {
  const [editing, setEditing] = useState(false);
  const [confirming, setConfirming] = useState(false);
  const [draft, setDraft] = useState({ name: node.name, public: node.public, note: node.note });
  if (editing) {
    return (
      <tr>
        <td />
        <td><input aria-label="名称" value={draft.name} onChange={(e) => setDraft({ ...draft, name: e.target.value })} /></td>
        <td><input type="checkbox" aria-label="公开" checked={draft.public} onChange={(e) => setDraft({ ...draft, public: e.target.checked })} /></td>
        <td><input aria-label="备注" value={draft.note} onChange={(e) => setDraft({ ...draft, note: e.target.value })} /></td>
        <td />
        <td>
          <button type="button" onClick={() => { onSave(draft); setEditing(false); }}>保存</button>{" "}
          <button type="button" className="link" onClick={() => setEditing(false)}>取消</button>
        </td>
      </tr>
    );
  }
  return (
    <tr>
      <td>
        <button type="button" className="link" aria-label={`上移 ${node.name}`} onClick={onMoveUp}>↑</button>
        <button type="button" className="link" aria-label={`下移 ${node.name}`} onClick={onMoveDown}>↓</button>
      </td>
      <td><Link to={`/nodes/${node.id}`}>{node.name}</Link></td>
      <td>{node.public ? "是" : "否"}</td>
      <td className="muted">{node.note}</td>
      <td className="muted">{new Date(Number(node.createdAt) * 1000).toLocaleDateString()}</td>
      <td>
        <button type="button" className="link" onClick={() => setEditing(true)}>编辑</button>{" "}
        <button type="button" className="link" onClick={onRotate}>换 token</button>{" "}
        {confirming ? (
          <>
            <button type="button" className="danger" onClick={onDelete}>确认删除 {node.name}</button>{" "}
            <button type="button" className="link" onClick={() => setConfirming(false)}>取消</button>
          </>
        ) : (
          <button type="button" className="link danger" onClick={() => setConfirming(true)}>删除</button>
        )}
      </td>
    </tr>
  );
}
```

删除要二次确认（删的是节点及其全部历史），确认态在行内展开而不是弹窗：`window.confirm` 在测试里要打桩，且与 CSP 无关但风格不一。

- [ ] **Step 3: 注册窗口页**

`web/src/pages/RegisterWindow.tsx`：

```tsx
import { useMutation, useQuery } from "@connectrpc/connect-query";
import { useQueryClient } from "@tanstack/react-query";
import { type FormEvent, useState } from "react";
import { Secret } from "../components/Secret";
import { AdminService } from "../gen/probe/v1/admin_pb";
import { errorText } from "./Login";

const TTLS = [
  { label: "10 分钟", seconds: 600 },
  { label: "1 小时", seconds: 3600 },
  { label: "24 小时", seconds: 86400 },
  { label: "7 天", seconds: 7 * 86400 },
];

export function RegisterWindow() {
  const qc = useQueryClient();
  const status = useQuery(AdminService.method.getRegisterWindow, {}, { refetchInterval: 10_000 });
  const [ttl, setTtl] = useState(TTLS[1].seconds);
  const [maxNodes, setMaxNodes] = useState(5);
  const [key, setKey] = useState<string | null>(null);
  const open = useMutation(AdminService.method.openRegisterWindow, {
    onSuccess: (r) => { setKey(r.key); void qc.invalidateQueries(); },
  });
  const close = useMutation(AdminService.method.closeRegisterWindow, {
    onSuccess: () => { setKey(null); void qc.invalidateQueries(); },
  });
  const onOpen = (e: FormEvent) => { e.preventDefault(); open.mutate({ ttlS: ttl, maxNodes }); };
  const err = open.error ?? close.error ?? status.error;
  return (
    <section>
      <h1>注册窗口</h1>
      {status.data?.open ? (
        <p>窗口开启中：剩余 {status.data.remaining} 个名额，截止 {new Date(Number(status.data.expiresAt) * 1000).toLocaleString()}。{" "}
          <button type="button" className="danger" onClick={() => close.mutate({})} disabled={close.isPending}>关闭窗口</button>
        </p>
      ) : (
        <p className="muted">当前没有开启的窗口。</p>
      )}
      {key && (
        <>
          <Secret label="注册 key" value={key} />
          <p>在被监控的机器上执行：</p>
          <pre className="secret">{`probe-agent register --hub ${window.location.origin} --key ${key}`}</pre>
        </>
      )}
      <form onSubmit={onOpen} className="row">
        <label>有效期
          <select value={ttl} onChange={(e) => setTtl(Number(e.target.value))}>
            {TTLS.map((t) => <option key={t.seconds} value={t.seconds}>{t.label}</option>)}
          </select>
        </label>
        <label>可注册节点数<input type="number" min={1} max={1000} value={maxNodes} onChange={(e) => setMaxNodes(Number(e.target.value))} /></label>
        <button type="submit" disabled={open.isPending}>开启新窗口</button>
      </form>
      {err && <p role="alert" className="error">{errorText(err)}</p>}
    </section>
  );
}
```

`App.tsx` 加 `{ path: "nodes", Component: Nodes }`、`{ path: "register", Component: RegisterWindow }`；`Layout` 的导航加 `<NavLink to="/nodes">节点</NavLink>`、`<NavLink to="/register">注册窗口</NavLink>`（放在"总览"之后、登出之前）。

- [ ] **Step 4: 测试**

`web/src/pages/Nodes.test.tsx`：

```tsx
import { fireEvent, screen, waitFor } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";
import { renderWithAdmin } from "../test/harness";
import { Nodes } from "./Nodes";

const two = [
  { id: 1n, name: "a", public: false, note: "", sortOrder: 0, createdAt: 0n },
  { id: 2n, name: "b", public: true, note: "db", sortOrder: 1, createdAt: 0n },
];

describe("Nodes", () => {
  it("创建后一次性展示 token", async () => {
    const createNode = vi.fn(async () => ({ node: { ...two[0], id: 3n, name: "c" }, token: "deadbeef" }));
    renderWithAdmin({ listNodes: async () => ({ nodes: two }), createNode }, [{ path: "/nodes", Component: Nodes }], "/nodes");
    await screen.findByRole("link", { name: "a" });
    fireEvent.change(screen.getByLabelText("新节点名称"), { target: { value: "c" } });
    fireEvent.click(screen.getByRole("button", { name: "创建" }));
    expect(await screen.findByLabelText("节点 c 的 token")).toHaveTextContent("deadbeef");
    expect(createNode).toHaveBeenCalledWith(expect.objectContaining({ name: "c" }), expect.anything());
  });

  it("删除需要二次确认", async () => {
    const deleteNode = vi.fn(async () => ({}));
    renderWithAdmin({ listNodes: async () => ({ nodes: two }), deleteNode }, [{ path: "/nodes", Component: Nodes }], "/nodes");
    await screen.findByRole("link", { name: "a" });
    fireEvent.click(screen.getAllByRole("button", { name: "删除" })[0]);
    expect(deleteNode).not.toHaveBeenCalled();
    fireEvent.click(screen.getByRole("button", { name: "确认删除 a" }));
    await waitFor(() => expect(deleteNode).toHaveBeenCalledWith(expect.objectContaining({ id: 1n }), expect.anything()));
  });

  it("下移提交完整排列", async () => {
    const reorderNodes = vi.fn(async () => ({}));
    renderWithAdmin({ listNodes: async () => ({ nodes: two }), reorderNodes }, [{ path: "/nodes", Component: Nodes }], "/nodes");
    await screen.findByRole("link", { name: "a" });
    fireEvent.click(screen.getByRole("button", { name: "下移 a" }));
    await waitFor(() => expect(reorderNodes).toHaveBeenCalledWith(expect.objectContaining({ ids: [2n, 1n] }), expect.anything()));
  });

  it("编辑整体提交三个字段", async () => {
    const updateNode = vi.fn(async () => ({ node: two[0] }));
    renderWithAdmin({ listNodes: async () => ({ nodes: two }), updateNode }, [{ path: "/nodes", Component: Nodes }], "/nodes");
    await screen.findByRole("link", { name: "a" });
    fireEvent.click(screen.getAllByRole("button", { name: "编辑" })[0]);
    fireEvent.change(screen.getByLabelText("名称"), { target: { value: "a2" } });
    fireEvent.click(screen.getByLabelText("公开"));
    fireEvent.click(screen.getByRole("button", { name: "保存" }));
    await waitFor(() => expect(updateNode).toHaveBeenCalledWith(expect.objectContaining({ id: 1n, name: "a2", public: true, note: "" }), expect.anything()));
  });
});
```

`web/src/pages/RegisterWindow.test.tsx`：

```tsx
import { fireEvent, screen } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";
import { renderWithAdmin } from "../test/harness";
import { RegisterWindow } from "./RegisterWindow";

describe("RegisterWindow", () => {
  it("开窗后展示 key 与安装命令", async () => {
    const openRegisterWindow = vi.fn(async () => ({ key: "cafe", expiresAt: 100n, maxNodes: 5 }));
    renderWithAdmin(
      { getRegisterWindow: async () => ({ open: false, expiresAt: 0n, remaining: 0 }), openRegisterWindow },
      [{ path: "/register", Component: RegisterWindow }], "/register",
    );
    await screen.findByText("当前没有开启的窗口。");
    fireEvent.click(screen.getByRole("button", { name: "开启新窗口" }));
    expect(await screen.findByLabelText("注册 key")).toHaveTextContent("cafe");
    expect(screen.getByText(/probe-agent register --hub .* --key cafe/)).toBeInTheDocument();
    expect(openRegisterWindow).toHaveBeenCalledWith(expect.objectContaining({ ttlS: 3600, maxNodes: 5 }), expect.anything());
  });

  it("开启中的窗口显示剩余名额并可关闭", async () => {
    const closeRegisterWindow = vi.fn(async () => ({}));
    renderWithAdmin(
      { getRegisterWindow: async () => ({ open: true, expiresAt: 4_000_000_000n, remaining: 3 }), closeRegisterWindow },
      [{ path: "/register", Component: RegisterWindow }], "/register",
    );
    expect(await screen.findByText(/剩余 3 个名额/)).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "关闭窗口" }));
    await vi.waitFor(() => expect(closeRegisterWindow).toHaveBeenCalled());
  });
});
```

```bash
cd /Users/xjetry/work/vibe/probe && make web-test > /tmp/m2b-t6-test.log 2>&1; echo $?
make web > /tmp/m2b-t6-build.log 2>&1; echo $?
```

注入：删除按钮直接调 `onDelete` 不经确认态 → "删除需要二次确认" 红在 `not.toHaveBeenCalled`；`move` 只提交被交换的两个 id → "下移提交完整排列" 红；`Secret` 不渲染 `value` → 第一条红。各自改回。

- [ ] **Step 5: 端到端与提交**

```bash
make ci > /tmp/m2b-t6-ci.log 2>&1; echo $?
make e2e > /tmp/m2b-t6-e2e.log 2>&1; echo $?
git add web
git commit -m "web: 节点管理与注册窗口

token 与注册 key 只展示一次并可复制，页面不保存它们。删除在行内二次确认，
排序按接口要求提交完整排列。注册窗口页直接给出被监控机上要执行的命令。"
```

---

## 收尾：控制端的浏览器验收

代码任务结束后，由控制端（不是实现者）用真实浏览器对着 `make e2e` 那样起的 hub 走一遍：登录 → 总览看到两个容器节点在线、读数条有值 → 点进一个节点看到六张图有线、切 7d 级别变化 → 节点页创建一个节点看到 token → 注册窗口页开窗看到 key 与命令 → 登出后回到登录页。任何一步不符即回到对应任务修复。

## 自检

**Spec 覆盖**：§10 的 `/admin/*` 入口、2 秒轮询、未构建说明页、路由优先级；§14 的构建顺序与产物不入库；§12 的"从用户可见入口测"（组件测试经生成客户端与内存服务实现；Go 侧经真实处理器）；§6.2 的无读数在图表上的呈现。公开页入口、外观设置、`--public-dir` 留给公开页里程碑。

**类型一致性**：`renderWithAdmin` 在 Task 4 定义、Task 5/6 使用；`errorText` 在 Task 3 定义、Task 6 使用；`web.Prefix` / `web.Handler` / `web.RootRedirect` 在 Task 2 定义并挂载；`toAligned` / `unitOf` 在 Task 5 定义与使用。

**占位扫描**：无 TBD；index 路由的初始内容在 Task 4 被替换，Task 3 内它本身是完整可运行的页面。
