# 公开视图标签筛选 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 公开快照带出公开节点的标签，公开总览页按标签筛选：默认显示全部，单击只选这一个，Shift+单击翻转被点的标签而不动其余。

**Architecture:** `PublicNode` 增加 `repeated string tags = 11`，`GetSnapshot` 直接填 `Node.Tags`（`ListPublicNodes` 只返回 `public = 1` 的节点，私有节点的标签因此不出现）。筛选纯前端：选择逻辑是 `web/src/lib/tags.ts` 里的纯函数，标签栏是独立组件，总览页持有选择状态。

**Tech Stack:** Go（ConnectRPC、buf 生成入库）、React 19、vitest + Testing Library、connect-query。

**Spec:** `docs/superpowers/specs/2026-09-17-probe-architecture-design.md` §10「节点标签」一条与 `PublicNode` 字段清单（本计划开始前已改好、未提交，随 Task 1 一起提交）。

## Global Constraints

- 标签名的比较口径：去首尾空白（Unicode White_Space）后按简单折叠大小写不敏感，前端一律经 `web/src/lib/tags.ts` 的 `sameTag`，不自造比较。
- 空选择 = 不过滤、显示全部；这一分支必须写成显式分支，不从 `every` 对空数组恒真里自然掉出来（spec §10）。
- 多选取交集：节点必须同时带有所选全部标签。
- 生效的选择集 = 所选与当前快照里仍存在的标签的交集。
- 顶部在线计数按过滤后的节点算。
- `ci` 要求 `gen`、`web/src/gen` 在 `make gen` 之后没有改动或未跟踪文件：生成物用 `make gen` 重生成，不手改。
- 代码注释与 commit message 不写过程信息（任务编号、方案代号、轮次），只写 WHY 与不变式；因果论断要能被推演证伪。
- 每条新断言做缺陷注入，确认红在正确的原因上；判成败的命令不接管道，用 `cmd > log 2>&1; echo $?`。
- Bash 每条命令用 `cd /Users/xjetry/work/vibe/probe && …`；Go 测试加 `-count=1`。

## Review Focus

- 私有节点带标签时，其标签绝不出现在快照里（连同公开节点的标签同时存在于库中，两个方向都断言）——Task 1。
- 公开节点没有标签时快照里 `tags` 为空，前端不显示标签栏、不因空数组崩——Task 2。
- 轮询后某个被选中的标签从快照里消失（该标签的节点被摘标签或转私有）：页面回到显示全部，而不是显示空列表且看不到筛选条件——Task 2。
- 标签名只有大小写或首尾空白差异（`DB` 与 `db` 在不同节点上出现，如经旧数据或 API 直写）：标签栏只出一个按钮，筛选两边的节点都命中——Task 2。
- 有过滤时无标签节点不显示；过滤后无节点时给出说明而不是空白页——Task 2。

---

### Task 1: 协议、hub 与 spec

**Files:**
- Modify: `proto/probe/v1/public.proto:88`（把「标签不在这里」的注释换成字段）
- Regenerate: `gen/probe/v1/public.pb.go`、`web/src/gen/probe/v1/public_pb.ts`（`make gen`）
- Modify: `internal/hub/api/public.go`（`GetSnapshot`，约 249 行）
- Modify: `internal/hub/api/public_test.go`（`publicFields` 允许列表，约 232 行）
- Modify: `internal/hub/api/tags_test.go:247-259`（`TestTagsStayOffThePublicSnapshot` 改为正反两向）
- Modify: `internal/hub/store/node.go:190`（`ListPublicNodes` 注释里「标签不公开由 PublicNode 没有这个字段承载」已失效）
- Already modified, uncommitted: `docs/superpowers/specs/2026-09-17-probe-architecture-design.md`

**Interfaces:**
- Produces: `probev1.PublicNode.Tags []string`（Go），`PublicNode.tags: string[]`（TS，protobuf-es 的 repeated 字段总是数组，缺省为 `[]`）。

- [ ] **Step 1: 确认字段号 11 空闲**

Run: `cd /Users/xjetry/work/vibe/probe && sed -n 60,90p proto/probe/v1/public.proto`
Expected: `PublicNode` 的字段号用到 10（`country`），没有 `reserved 11`。若有 reserved 或已占用，停下来另选空闲号并同步本计划。

- [ ] **Step 2: 先改测试（红）**

把 `internal/hub/api/tags_test.go` 的 `TestTagsStayOffThePublicSnapshot` 整个替换为：

```go
// 公开节点的标签随快照公开，私有节点的标签不出现：快照只含 public = 1 的节点（store.ListPublicNodes），
// 标签跟着节点行读出，节点不在快照里它的标签就无处可出。两个方向同库断言，只测一个方向分不出
// "标签被过滤掉"与"标签根本没被读出"。
func TestPublicSnapshotCarriesTagsOfPublicNodesOnly(t *testing.T) {
	h := newHarness(t, "")
	h.login(t)
	pub, _ := h.createNode(t, "pub")
	priv, _ := h.createNode(t, "priv")
	if _, err := updateTags(t, h, pub, "pub", true, "web", "客户A"); err != nil {
		t.Fatal(err)
	}
	if _, err := updateTags(t, h, priv, "priv", false, "internal-billing-db"); err != nil {
		t.Fatal(err)
	}
	resp, err := h.publicClient().GetSnapshot(t.Context(), connect.NewRequest(&probev1.PublicServiceGetSnapshotRequest{}))
	if err != nil {
		t.Fatal(err)
	}
	nodes := resp.Msg.GetNodes()
	if len(nodes) != 1 || nodes[0].GetId() != pub {
		t.Fatalf("snapshot nodes = %v, want only the public node", nodes)
	}
	// 与管理端 Node.tags 同一口径：先建的写法，按折叠排序。不用 mustUpdateTags 取值：它会把节点改回私有。
	adminNodes, err := h.admin.ListNodes(t.Context(), connect.NewRequest(&probev1.ListNodesRequest{}))
	if err != nil {
		t.Fatal(err)
	}
	var want []string
	for _, n := range adminNodes.Msg.GetNodes() {
		if n.GetId() == pub {
			want = n.GetTags()
		}
	}
	if !slices.Equal(nodes[0].GetTags(), want) || len(want) != 2 {
		t.Fatalf("public tags = %v, admin tags = %v", nodes[0].GetTags(), want)
	}
	raw := pubGet(t, h, "GetSnapshot", jsonQuery("{}"), nil)
	if bytes.Contains(raw.body, []byte("internal-billing-db")) {
		t.Fatalf("private node's tag leaked into the public snapshot: %s", raw.body)
	}
}
```

若 `slices` 未 import，加上 `"slices"`。

另在 `internal/hub/api/public_test.go` 改 `publicFields`（约 232 行）：`PublicNode` 的清单末尾加 `"tags"`：

```go
	"probe.v1.PublicNode":     {"id", "name", "online", "last_seen_at", "sort_order", "facts", "metrics", "traffic", "billing", "country", "tags"},
```

- [ ] **Step 3: 跑测试确认红在正确的原因上**

Run: `cd /Users/xjetry/work/vibe/probe && go test -count=1 ./internal/hub/api -run 'TestPublicSnapshotCarriesTagsOfPublicNodesOnly|TestPublicFields' > /tmp/t1-red.log 2>&1; echo $?; tail -20 /tmp/t1-red.log`
Expected: 退出码非 0，原因是编译失败 `nodes[0].GetTags undefined`（字段还不存在）。这是"没有字段"的红，不是别的。

- [ ] **Step 4: 改协议并重生成**

`proto/probe/v1/public.proto`：把第 88 行注释

```
  // 节点标签（Node.tags）不在这里，也不保留字段号：标签常写用途与归属（db、客户A），公开即暴露内部构成。
```

替换为：

```
  // 节点标签，与 Node.tags 同一口径（先建的写法、按折叠排序）。只有公开节点进快照，私有节点的标签不出现；标签常写用途与
  // 归属（db、客户A），挂在公开节点上即对外可见，没有单独的公开开关。
  repeated string tags = 11;
```

Run: `cd /Users/xjetry/work/vibe/probe && make gen > /tmp/t1-gen.log 2>&1; echo $?; tail -5 /tmp/t1-gen.log; git status --short`
Expected: 退出码 0；`git status` 里 `gen/probe/v1/public.pb.go` 与 `web/src/gen/probe/v1/public_pb.ts` 被修改，`proto/probe/v1/public.proto` 被修改，没有别的生成物之外的意外文件。

- [ ] **Step 5: hub 填字段**

`internal/hub/api/public.go` 在 `pn.Country, _ = n.DisplayCountry()` 之后加：

```go
		// 标签随公开节点公开：n 来自 ListPublicNodes，只含 public = 1 的节点，私有节点的标签不会走到这里。
		pn.Tags = n.Tags
```

并把 `internal/hub/store/node.go:190` 的注释「返回的 Node 带着标签，标签不公开由 PublicNode 没有这个字段承载（§10）。」改为：

```go
// 返回的 Node 带着标签：公开快照据此公开公开节点的标签（§10），私有节点不在结果里，它的标签无从带出。
```

- [ ] **Step 6: 跑测试确认绿，再做缺陷注入**

Run: `cd /Users/xjetry/work/vibe/probe && go test -count=1 ./internal/hub/api ./internal/hub/store > /tmp/t1-green.log 2>&1; echo $?; tail -8 /tmp/t1-green.log`
Expected: 退出码 0。

缺陷注入（两个方向各一次；先确认注入落地）：
1. 把 `pn.Tags = n.Tags` 注释掉 → 重跑 `TestPublicSnapshotCarriesTagsOfPublicNodesOnly`，应红在 `public tags = [] ...`。
2. 恢复后，临时改 `store.ListPublicNodes` 的 SQL 去掉 `public = 1` 条件（`git diff` 确认改动在）→ 重跑，应红在 `snapshot nodes = ... want only the public node`。
每次注入后用 `git diff --stat` 确认落地，跑完 `git checkout -- internal/hub/store/node.go` 与还原 `public.go` 并再跑一次全绿。

- [ ] **Step 7: 提交**

```bash
cd /Users/xjetry/work/vibe/probe && go vet ./... > /tmp/t1-vet.log 2>&1; echo $?
git add proto/probe/v1/public.proto gen web/src/gen internal/hub/api/public.go internal/hub/api/public_test.go internal/hub/api/tags_test.go internal/hub/store/node.go docs/superpowers/specs/2026-09-17-probe-architecture-design.md
git commit -m "api: 公开快照带出公开节点的标签"
```

---

### Task 2: 前端选择逻辑与标签栏

**Files:**
- Modify: `web/src/lib/tags.ts`（加 `nextSelection`、`matchesTags`、`presentTags`）
- Modify: `web/src/lib/tags.test.ts`
- Create: `web/src/public/TagBar.tsx`
- Modify: `web/src/public/Overview.tsx`
- Modify: `web/src/public/Overview.test.tsx`
- Modify: `web/src/public/public.css`

**Interfaces:**
- Consumes: `PublicNode.tags: string[]`（Task 1 生成）、`sameTag`、`withTag`、`withoutTag`。
- Produces（`web/src/lib/tags.ts`）:
  - `nextSelection(selected: readonly string[], tag: string, shift: boolean): string[]`
  - `matchesTags(nodeTags: readonly string[], selected: readonly string[]): boolean`
  - `presentTags(nodes: readonly { tags: readonly string[] }[]): string[]`
- Produces（`TagBar.tsx`）: `TagBar({ tags, selected, onSelect, onClear })`，`tags: readonly string[]`、`selected: readonly string[]`、`onSelect: (tag: string, shift: boolean) => void`、`onClear: () => void`。

- [ ] **Step 1: 先写纯函数的测试（红）**

在 `web/src/lib/tags.test.ts` 顶部 import 加 `matchesTags, nextSelection, presentTags`，在 `describe` 内追加：

```ts
  describe("nextSelection", () => {
    it("单击：未选时变成只选这一个，覆盖其余", () => {
      expect(nextSelection([], "db", false)).toEqual(["db"]);
      expect(nextSelection(["web"], "db", false)).toEqual(["db"]);
      expect(nextSelection(["web", "db"], "cache", false)).toEqual(["cache"]);
    });
    it("单击：多选之一时收成只选它；恰好只选它时清空回到全部", () => {
      expect(nextSelection(["web", "db"], "db", false)).toEqual(["db"]);
      expect(nextSelection(["db"], "db", false)).toEqual([]);
      expect(nextSelection(["db"], "DB", false)).toEqual([]);
    });
    it("Shift+单击：其余不动，只翻转被点的一个", () => {
      expect(nextSelection([], "db", true)).toEqual(["db"]);
      expect(nextSelection(["web"], "db", true)).toEqual(["web", "db"]);
      expect(nextSelection(["web", "db"], "web", true)).toEqual(["db"]);
      expect(nextSelection(["db"], "DB", true)).toEqual([]);
    });
  });

  describe("matchesTags", () => {
    it("空选择匹配一切，含没有标签的节点", () => {
      expect(matchesTags([], [])).toBe(true);
      expect(matchesTags(["db"], [])).toBe(true);
    });
    it("多选取交集，按折叠比较", () => {
      expect(matchesTags(["db", "web"], ["web", "db"])).toBe(true);
      expect(matchesTags(["db"], ["web", "db"])).toBe(false);
      expect(matchesTags(["DB"], ["db"])).toBe(true);
      expect(matchesTags([], ["db"])).toBe(false);
    });
  });

  describe("presentTags", () => {
    it("跨节点按折叠去重，保留先出现的写法，按码元序排序", () => {
      expect(presentTags([{ tags: ["web", "db"] }, { tags: ["DB", "cache"] }, { tags: [] }])).toEqual(["cache", "db", "web"]);
      expect(presentTags([{ tags: ["DB"] }, { tags: ["db"] }])).toEqual(["DB"]);
      expect(presentTags([])).toEqual([]);
    });
  });
```

Run: `cd /Users/xjetry/work/vibe/probe/web && npx vitest run src/lib/tags.test.ts > /tmp/t2-red.log 2>&1; echo $?; tail -15 /tmp/t2-red.log`
Expected: 非 0，原因是 `nextSelection is not a function`（未导出）。

- [ ] **Step 2: 实现纯函数（绿）**

在 `web/src/lib/tags.ts` 末尾追加：

```ts
// 公开页标签栏的点击语义。selected 为空表示不过滤；普通单击把选择收成"只选这一个"，当前恰好只选它时再点一次清空
// （回到全部）；shift 单击只翻转被点的一个，其余标签的状态不动。比较全部经 sameTag，与 hub 的折叠口径一致。
export function nextSelection(selected: readonly string[], tag: string, shift: boolean): string[] {
  const on = selected.some((s) => sameTag(s, tag));
  if (shift) return on ? withoutTag(selected, tag) : withTag(selected, tag);
  return on && selected.length === 1 ? [] : [tag];
}

// 多选取交集：节点必须带有所选的每一个标签。空选择不过滤——空条件匹配一切，这一分支显式写出而不靠 every 对空数组恒真。
export function matchesTags(nodeTags: readonly string[], selected: readonly string[]): boolean {
  if (selected.length === 0) return true;
  return selected.every((s) => nodeTags.some((t) => sameTag(t, s)));
}

// 快照里出现过的标签：跨节点按折叠去重、保留先出现的写法，按码元序排序（不用 localeCompare：顺序不随浏览器语言变）。
export function presentTags(nodes: readonly { tags: readonly string[] }[]): string[] {
  const out: string[] = [];
  for (const n of nodes) for (const t of n.tags) if (!out.some((o) => sameTag(o, t))) out.push(t);
  return out.sort();
}
```

Run: 同 Step 1 命令。Expected: 退出码 0。

- [ ] **Step 3: 纯函数缺陷注入**

逐个注入并确认落地（`git diff --stat`）、红在对应用例、随后 `git checkout -- web/src/lib/tags.ts` 前先把 Step 2 的实现暂存（`git stash` 不便，直接改完手工还原并重跑绿）：
1. `nextSelection` 里把 `on && selected.length === 1 ? [] : [tag]` 改成 `on ? [] : [tag]` → 应红在「多选之一时收成只选它」。
2. shift 分支改成 `return [tag]` → 应红在 Shift 的「其余不动」。
3. `matchesTags` 的 `every` 改成 `some` → 应红在「多选取交集」。
4. 删掉 `matchesTags` 的 `selected.length === 0` 显式分支且把 `every` 换成 `selected.length > 0 && every` → 应红在「空选择匹配一切」（证明显式分支不是摆设：这条注入模拟"空即拒绝"的方向错误）。

- [ ] **Step 4: 写组件与页面测试（红）**

在 `web/src/public/Overview.test.tsx` 顶部 import 加 `fireEvent`（从 `@testing-library/react`），文件末尾追加：

```tsx
const tagged = {
  now: 1_000n,
  reportIntervalMs: 4000,
  nodes: [
    { id: 1n, name: "web-1", online: true, sortOrder: 0, tags: ["web", "prod"] },
    { id: 2n, name: "db-1", online: false, sortOrder: 1, tags: ["db", "prod"] },
    { id: 3n, name: "lab-1", online: true, sortOrder: 2, tags: ["DB"] },
    { id: 4n, name: "bare-1", online: true, sortOrder: 3, tags: [] },
  ],
};

const shown = () => screen.queryAllByRole("article").map((a) => a.getAttribute("aria-label"));
const chip = (name: string) => screen.getByRole("button", { name });

function renderTagged(getSnapshot: () => Promise<typeof tagged> = async () => tagged) {
  renderWithService(PublicService, { getSnapshot }, [{ path: "/", Component: PublicOverview }], "/");
}

it("标签栏：默认显示全部，标签按折叠去重，没有节点带标签时不出现标签栏", async () => {
  renderTagged();
  await screen.findByText("3 / 4 在线");
  expect(shown()).toEqual(["web-1", "db-1", "lab-1", "bare-1"]);
  expect(chip("全部")).toHaveAttribute("aria-pressed", "true");
  // db 与 DB 是同一个标签：只有一个按钮，保留先出现的写法。
  expect(screen.getAllByRole("button").map((b) => b.textContent)).toEqual(["全部", "db", "prod", "web"]);
});

it("没有任何节点带标签时不画标签栏", async () => {
  renderWithService(PublicService, { getSnapshot: async () => snapshot }, [{ path: "/", Component: PublicOverview }], "/");
  await screen.findByText("1 / 2 在线");
  expect(screen.queryByRole("button", { name: "全部" })).toBeNull();
});

it("单击只选这一个；再点同一个回到全部；点「全部」清空", async () => {
  renderTagged();
  await screen.findByText("3 / 4 在线");
  fireEvent.click(chip("prod"));
  expect(shown()).toEqual(["web-1", "db-1"]);
  expect(chip("prod")).toHaveAttribute("aria-pressed", "true");
  expect(chip("全部")).toHaveAttribute("aria-pressed", "false");
  // 顶部计数按过滤后的节点算。
  expect(screen.getByText("1 / 2 在线")).toBeInTheDocument();
  fireEvent.click(chip("web"));
  expect(shown()).toEqual(["web-1"]);
  expect(chip("prod")).toHaveAttribute("aria-pressed", "false");
  fireEvent.click(chip("web"));
  expect(shown()).toEqual(["web-1", "db-1", "lab-1", "bare-1"]);
  fireEvent.click(chip("db"));
  fireEvent.click(chip("全部"));
  expect(shown()).toEqual(["web-1", "db-1", "lab-1", "bare-1"]);
});

it("Shift+单击在其余标签状态不变的前提下翻转被点的一个，多选取交集", async () => {
  renderTagged();
  await screen.findByText("3 / 4 在线");
  fireEvent.click(chip("prod"));
  fireEvent.click(chip("db"), { shiftKey: true });
  expect(shown()).toEqual(["db-1"]);
  expect(chip("prod")).toHaveAttribute("aria-pressed", "true");
  expect(chip("db")).toHaveAttribute("aria-pressed", "true");
  fireEvent.click(chip("prod"), { shiftKey: true });
  // 只剩 db：DB 与 db 折叠后是同一个标签，两个节点都命中。
  expect(shown()).toEqual(["db-1", "lab-1"]);
  fireEvent.click(chip("db"), { shiftKey: true });
  expect(shown()).toEqual(["web-1", "db-1", "lab-1", "bare-1"]);
});

it("过滤后没有节点时给出说明，标签栏仍在", async () => {
  renderTagged();
  await screen.findByText("3 / 4 在线");
  fireEvent.click(chip("web"));
  fireEvent.click(chip("db"), { shiftKey: true });
  expect(shown()).toEqual([]);
  expect(screen.getByText("没有符合所选标签的节点。")).toBeInTheDocument();
  expect(chip("web")).toBeInTheDocument();
});

it("被选中的标签从快照里消失后回到显示全部，而不是留下看不见的过滤", async () => {
  vi.useFakeTimers({ shouldAdvanceTime: true });
  let current: typeof tagged = tagged;
  renderTagged(async () => current);
  await screen.findByText("3 / 4 在线");
  fireEvent.click(chip("web"));
  expect(shown()).toEqual(["web-1"]);
  current = { ...tagged, nodes: tagged.nodes.map((n) => ({ ...n, tags: n.tags.filter((t) => t !== "web") })) };
  await act(async () => vi.advanceTimersByTimeAsync(POLL_MS + 100));
  await waitFor(() => expect(screen.queryByRole("button", { name: "web" })).toBeNull());
  expect(shown()).toEqual(["web-1", "db-1", "lab-1", "bare-1"]);
  expect(chip("全部")).toHaveAttribute("aria-pressed", "true");
});
```

Run: `cd /Users/xjetry/work/vibe/probe/web && npx vitest run src/public/Overview.test.tsx > /tmp/t2-red2.log 2>&1; echo $?; tail -30 /tmp/t2-red2.log`
Expected: 非 0；新用例红在找不到「全部」按钮之类（组件未实现），原有三条仍绿。

- [ ] **Step 5: 实现 TagBar 与页面接入（绿）**

新建 `web/src/public/TagBar.tsx`：

```tsx
// 公开页标签栏。选择集与点击语义在 lib/tags.ts 的 nextSelection，这里只负责呈现与转发点击；selected 为空表示"全部"。
export function TagBar({ tags, selected, onSelect, onClear }: {
  tags: readonly string[];
  selected: readonly string[];
  onSelect: (tag: string, shift: boolean) => void;
  onClear: () => void;
}) {
  return (
    <div className="tag-bar" role="group" aria-label="按标签筛选">
      <button type="button" className="tag-chip" aria-pressed={selected.length === 0} onClick={onClear}>全部</button>
      {tags.map((t) => (
        <button key={t} type="button" className="tag-chip" aria-pressed={selected.includes(t)} onClick={(e) => onSelect(t, e.shiftKey)}>{t}</button>
      ))}
    </div>
  );
}
```

`selected.includes(t)` 成立的前提：`selected` 里的写法来自 `tags`（`presentTags` 的输出）——见下面 Overview 里 `effective` 的构造，它把所选映射回 `tags` 里的写法。

`web/src/public/Overview.tsx`：import 追加

```tsx
import { useState } from "react";
import { matchesTags, nextSelection, presentTags, sameTag } from "../lib/tags";
import { TagBar } from "./TagBar";
```

把 `PublicOverview` 改为：

```tsx
export function PublicOverview() {
  const snap = useQuery(PublicService.method.getSnapshot, {}, { refetchInterval: POLL_MS });
  const [selected, setSelected] = useState<string[]>([]);
  const gate = queryGate(snap);
  if (!gate.ready) return gate.loading ?? errorBanner(...gate.errors);
  const now = Number(gate.data.now);
  const all = gate.data.nodes;
  const tags = presentTags(all);
  // 生效的选择集只取当前快照里仍存在的标签，并换成标签栏上的写法：被选的标签在轮询后消失时，页面回到显示全部，
  // 而不是留下一个看不见的过滤条件。状态里存的写法可能与快照当前的写法只差折叠，经这一步统一。
  const effective = tags.filter((t) => selected.some((s) => sameTag(s, t)));
  const nodes = all.filter((n) => matchesTags(n.tags, effective));
  const online = nodes.filter((n) => n.online).length;
  return (
    <section>
      <header className="row">
        <h1>节点</h1>
        <span className="muted">{online} / {nodes.length} 在线</span>
      </header>
      {gate.banner}
      {tags.length > 0 && <TagBar tags={tags} selected={effective} onSelect={(t, shift) => setSelected(nextSelection(effective, t, shift))} onClear={() => setSelected([])} />}
      {all.length === 0 && <p className="muted">没有公开的节点。</p>}
      {all.length > 0 && nodes.length === 0 && <p className="muted">没有符合所选标签的节点。</p>}
      <div className="cards">
        {nodes.map((n) => <NodeCard key={String(n.id)} node={n} now={now} />)}
      </div>
    </section>
  );
}
```

`web/src/public/public.css` 末尾追加：

```css
.tag-bar { display: flex; flex-wrap: wrap; gap: .4rem; margin: 0 0 1rem; }
/* Shift+单击会在按钮文字上触发浏览器的范围选中，选中高亮与点击语义无关，关掉。 */
.tag-chip { user-select: none; padding: 0 .6rem; border: 1px solid var(--line); border-radius: 999px; background: none; color: var(--fg); font-size: 12px; cursor: pointer; }
.tag-chip[aria-pressed="true"] { border-color: var(--accent); background: var(--accent); color: var(--bg); }
```

先确认 `--bg`、`--fg`、`--line`、`--accent` 在 `web/src/styles.css` 里存在（`grep -n -- '--bg\|--fg\|--line\|--accent' web/src/styles.css | head`）；缺哪个就换成共用调色板里实际存在的对应变量，不新增变量。

Run: `cd /Users/xjetry/work/vibe/probe/web && npx vitest run src/public src/lib > /tmp/t2-green.log 2>&1; echo $?; tail -15 /tmp/t2-green.log`
Expected: 退出码 0。

- [ ] **Step 6: 页面缺陷注入**

逐个注入、确认落地、确认红的原因、还原：
1. `effective` 改成直接用 `selected`（不与快照交集）→ 应红在「被选中的标签从快照里消失…」。
2. `onSelect` 里忽略 shift（传 `false`）→ 应红在 Shift 用例。
3. `matchesTags` 调用换成只按第一个所选标签过滤 → 应红在 Shift 多选取交集的 `["db-1"]` 断言。
4. `presentTags` 换成不折叠去重（`out.push(t)` 直接推）→ 应红在标签栏去重用例（按钮多出一个 `DB`）。
5. 删除 `nodes.length === 0` 的说明行 → 应红在「过滤后没有节点时给出说明」。

- [ ] **Step 7: 类型检查、全量前端测试并提交**

Run: `cd /Users/xjetry/work/vibe/probe && make web-test > /tmp/t2-web.log 2>&1; echo $?; tail -15 /tmp/t2-web.log`
Expected: 退出码 0（含 `styles.test.tsx`；若它对 `public.css` 有规则性断言而失败，按其报错修 CSS，不放宽断言）。

```bash
cd /Users/xjetry/work/vibe/probe && git add web/src/lib/tags.ts web/src/lib/tags.test.ts web/src/public
git commit -m "web: 公开总览按标签筛选，单击独选、Shift+单击翻转"
```

---

### Task 3: 全量验收

**Files:** 无改动（发现问题回到对应 Task 修）。

- [ ] **Step 1: 与 CI 同一条命令**

Run: `cd /Users/xjetry/work/vibe/probe && make ci > /tmp/t3-ci.log 2>&1; echo $?; tail -25 /tmp/t3-ci.log`
Expected: 退出码 0；`ci` 末尾的生成目录洁净检查通过（`git status --porcelain -- gen web/src/gen` 为空）。失败时先按「失败归因看证据」：`git diff --stat main..HEAD -- <失败包>` 判断是否本分支引入。

- [ ] **Step 2: race 与 e2e 门禁**

Run: `cd /Users/xjetry/work/vibe/probe && go test -race -count=1 ./... > /tmp/t3-race.log 2>&1; echo $?; tail -15 /tmp/t3-race.log`
Expected: 退出码 0。再跑 `make e2e > /tmp/t3-e2e.log 2>&1; echo $?`（依赖本机 Docker / 环境不可控时，说明未跑及原因，不声称通过）。

- [ ] **Step 3: 从用户可见入口实测**

起一个 hub（`make web` 后按 README 的方式启动，建 4 个节点：2 个公开带标签 `web`/`prod`、`db`/`prod`，1 个公开无标签，1 个私有带标签 `secret`），用浏览器（chrome-devtools 或 playwright MCP）打开公开首页，依次：单击 `prod`、Shift+单击 `db`、再单击 `prod`、点「全部」，每步读页面上的卡片名单并截图；页面源码与 `GetSnapshot` 响应里检索 `secret` 应为空。记录看到了什么，不把"测试绿"当作这一步的凭据。

- [ ] **Step 4: 收尾**

Run: `cd /Users/xjetry/work/vibe/probe && git status --short && git log --oneline main..HEAD`
Expected: 工作树洁净，两条提交（Task 1、Task 2）。汇报改了什么、跑了什么、各自结果；未跑的项明说。
