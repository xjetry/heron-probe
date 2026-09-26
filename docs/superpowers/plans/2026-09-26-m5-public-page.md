# M5 公开页 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 匿名访客在 `/` 看到公开节点的实时卡片与历史图表。为此 hub 新增 `PublicService`：无鉴权，按来源限流，GET 可缓存。管理面板能设置公开页外观，运维能用 `--public-dir` 换掉内置公开页。

**Architecture:**
- **共用类型：** 历史查询的消息拆到 `query.proto`，管理与公开两个服务共用这组请求/响应类型。
- **服务层：** `PublicService` 与 `AdminService` 同在 `internal/hub/api`，历史查询与在线判定只有一份实现。
- **公开消息：** 由描述符投影生成。字段集合由公开消息自己声明，没声明的就不公开（默认私有）。
- **挂载链：** HTTP 中间件 `cacheControl(ratelimit.BySource(snapshotCache(connect)))`。`Register` 的限速改用同一个中间件，同样在 connect 解码之前计数；快照缓存按编码缓存响应字节。
- **静态服务：** 面板、内置公开页、`--public-dir` 三者共用一个"只服务普通文件"的核心；自定义目录经 `os.Root` 按请求打开。
- **前端：** 新增第二个 Vite 入口，以 `web/src/public` 为根；与面板共用图表、历史、配色组件。由 import 扫描测试钉住公开入口不触达 `admin_pb`。

**Tech Stack:**
- 后端：Go 1.27（`os.Root`）、connect-go v1.21.0、modernc SQLite 1.59.0
- 生成：buf 1.50.0（protoc-gen-go、protoc-gen-connect-go、protoc-gen-es）
- 前端：React 19、connect-query、connect-web（`useHttpGet`）、Vite 8、vitest（jsdom 30.1）、uPlot
- e2e：POSIX sh + curl + jq

**Spec:**
- `docs/superpowers/specs/2026-09-17-probe-architecture-design.md`，以与本计划同时提交的版本为准（63d0800 之后的同步提交）；涉及 §3.1、§3.2、§3.3、§5.2、§5.3、§6.6、§10、§12、§14。
- 方针：`docs/guidelines/agent-first.md`。

工作树 `/Users/xjetry/work/vibe/probe-public`，分支 `m5m6-public`（hub Docker 镜像计划引用的就是这个名字），基点 main。

## Global Constraints

**PublicService 的方法与消息**
- 方法：`GetSite`、`GetSnapshot`、`QueryMetrics`、`QueryProbes`。
- 后两者只对 `public = true` 的节点应答；其余节点与不存在的节点，返回同一个 `NotFound`。
- 四个方法全部标 `idempotency_level = NO_SIDE_EFFECTS`，因而可用 GET 调用并带 `Cache-Control`。`AdminService` 与 `AgentService` 一律不标。
- 公开数据用独立的消息类型，不对 `Node` 做字段过滤。默认方向是私有：必须显式加入 `Public*` 消息才公开。
- `PublicSnapshot`：`now`、`report_interval_ms`、`nodes`。
- `PublicNode`：id、名称、在线、最近上报、排序、`PublicFacts`、`PublicMetrics`、`Traffic`。
- `PublicFacts` 只有系统、架构、CPU 型号、核数、虚拟化。不给主机名、内核版本、agent 版本、ICMP 可用性。
- `PublicMetrics` 与 `Metrics` 同字段，但没有 `boot_id`。
- `QueryMetrics` 与 `QueryProbes` 复用管理端的请求与响应类型。
- 把节点标为公开，就是公开它正在探测的目标：公开端 `QueryProbes` 只对当前分配给该节点的任务返回种类与目标；历史里有、但已从该节点撤下的任务两项留空。管理端按任务当前的配置标注，不受此限。

**限流**
- 按来源令牌桶：桶容量 60、每秒补充 10，超限返回 `ResourceExhausted`。
- 来源键：IPv4 按单个地址，IPv6 按所在 /64（一台主机通常独占整个 /64，逐地址计键等于在 /64 里换个地址就换一份计数）。限流、`Register` 的窗口失败计数与登录失败锁定三处用同一个 `auth.SourceKey`。
- 反代后不配 `--trusted-proxies` 时全体访客共用代理地址的一个桶：这是部署配置问题，不改限流，写进 `--trusted-proxies` 的 flag 帮助与 README 的反代一节。
- 与 `Register` 的限速是同一实现（`Register`：桶容量 30、每秒补充 1）：`internal/hub/ratelimit` 的 `BySource` 中间件，包在 connect 处理器外面，解码失败的请求也计数。

**缓存**
- `GetSnapshot` 的序列化结果按编码（codec、压缩）缓存 1 秒。缓存的是响应字节，不是消息。
- GET 成功响应的 `Cache-Control`：快照 `max-age=1`、历史查询 `max-age=60`、站点配置 `max-age=300`。失败的 GET 响应（含 429 与 NotFound）带 `no-store`。POST 响应不带缓存头。

**设置**
- 存储：`setting` 表，键值结构。
- 准入：`GetSettings` 为只读口径（`ACCESS_READ`），`UpdateSettings` 仅会话（`ACCESS_SESSION`）。
- 字段与上限：
  - 标题：清洗前不超过 1024 字节，去掉控制字符与首尾空白后不超过 64 个字符。
  - 明暗：`auto`、`light`、`dark` 之一。
  - 主色：`#rrggbb`。
  - logo：`data:` URL，图片类型限 png、jpeg、webp、svg，不超过 128 KiB。
  - 自定义 CSS：不超过 64 KiB；含 `</` 即拒绝（它能跳出注入点的 `<style>`）。
- 只接受 CSS，不接受 JS 或 HTML。
- 校验错误写明字段、违反的约束与期望取值。
- `GetSite` 下发这五项。公开页以 CSS 变量应用，自定义 CSS 放在内置样式之后。

**静态服务**
- 内置公开页与面板用同一套 CSP。
- `--public-dir` 只加 `X-Content-Type-Options: nosniff` 与 `frame-ancestors 'none'`，不限制脚本与外部资源。后果写进 flag 帮助与 `DirHandler` 注释：目录与面板同源，其中的脚本能带着来访管理员的会话调管理接口，只放与 hub 二进制同等可信的内容。
- 两者都不列目录。`assets/` 下未命中返回 404，其余回落 `index.html`。自定义目录一律 `no-cache`。
- 路径任一段以 `.` 开头的名字一律当作不存在（`assets/` 下 404，其余回落 `index.html`），三个来源同一条规则，在共用核心里判定。
- 未构建前端时，`/` 与面板一样返回"前端未构建"的说明。
- `--public-dir` 的文件访问经 `os.Root`：不可越出目录，不跟随指向目录外的符号链接。
- `/admin` 与 RPC 路径的路由优先级更高，替换目录无法遮蔽它们。

**存储统计**
- `GetStorageStats`（只读口径）返回库大小（逻辑大小 `page_count × page_size`）与各表行数，与 `probe-hub stats` 同一来源。

**前端**
- `web/` 内两个 Vite 入口：`/admin/*` 管理面板、`/` 公开页，各自打包。
- 公开入口不得引用 `AdminService` 的生成代码，由测试扫描公开入口的 import 钉住。
- 图表组件与面板共用。
- 实时数据轮询默认 2 秒。
- 总览是节点卡片：名称、在线、系统与架构、CPU、内存、磁盘、网速、运行时长、本周期流量。
- 节点页是历史图表（指标与探测），时间范围选择与面板用同一组件。

**测试口径（§12）**
- 从生成的服务描述符枚举全部 RPC，逐个无凭据调用，断言 `Unauthenticated`；仅 `PublicService` 的方法在白名单内。
- 遍历全部 `Public*` 消息的字段，与测试内的显式允许列表比对。往公开消息加字段，必须同时改这份列表。

**构建**
- 顺序：`buf generate` → 前端构建 → `go build`。
- 生成的 Go 与 TS 代码入库，前端产物不入库。
- `go build` 与 `go test` 不依赖 Node。

**代码与提交规范**
- 代码注释与提交信息不写过程信息：任务、步骤、轮次编号，方案代号，审阅引用，"按上一轮"。
- 注释写 WHY 与不变式，并指明前提由谁保证。
- 不打补丁：根因在哪层就在哪层修，不在调用方加特判；同形状的缺陷全库一次改齐；不加兼容层。
- 不变式靠显式检查承载，不靠隐式机制兜底。

**验证规范**
- 每条新断言做一次缺陷注入，并确认它红在正确原因上。
- 注入在该任务提交之后进行，这样 `git checkout -- <文件>` 还原到的是已提交的实现。
- 注入前用 `git diff --stat` 非空确认注入确实落地。
- 判成败的命令写成 `cmd > /tmp/<名>.log 2>&1; echo $?`，不接管道。
- 缺陷注入表格里的 `\|` 是 Markdown 表格对竖线的转义，照抄命令时写成 `|`。`-run` 的正则里它表示"或"，留着反斜杠会变成匹配字面竖线，一个测试都不跑，却退出 0。
- Go 测试一律 `go test -count=1`。
- 测试里的正向等待只用 `internal/testwait.Bound` / `Until`；页面测试的异步查找有全局上界，不逐个加 timeout。
- 生成物（`gen/`、`web/src/gen/`）只由 `make gen` 产生，不手改。`make ci` 要求生成后工作树干净。

**执行约束**
- 实现者不改 `docs/`；不派子代理。
- 每条命令以 `cd /Users/xjetry/work/vibe/probe-public && ` 开头。

## Review Focus

1. **伪造的 logo 前缀。** 以下输入一律返回 `InvalidArgument`，错误写明期望形态，库里的外观不变：
   - 大写的 `DATA:`
   - 带参数的 `data:image/png;charset=utf-8;base64,…`
   - 不带 base64 的 `data:image/png,<svg…>`
   - 非图片类型 `data:text/html;base64,…`
   - 数据带换行或非字母表字符
   - 缺填充
   - 前导空格

   测试在 Task 3 Step 1（`TestUpdateSettingsLogoAcceptsOnlyOneShape`）。

2. **CSS 里 `</` 的大小写与编码变体。**
   - 拒绝并报出字节位置：`</style>`、`</STYLE>`、`</ style>`，以及任意位置的 `a</b`。
   - 照常保存（到不了 HTML 标记化器的写法）：`\3c/style>`、`&lt;/style>`、`<\/style>`、全角 `＜/`、`< /style>`。
   - 公开页用 textContent 写入 `<style>`：值为 `</style><img src=x onerror=…>` 时不产生任何元素。

   测试在 Task 3 Step 1（`TestUpdateSettingsCustomCSSRejectsOnlyLiteralEndTagOpen`）与 Task 10 Step 1（`web/src/public/site.test.ts`）。

3. **public-dir 里的符号链接。**
   - 指向目录内文件的链接照常服务。
   - 指向目录外文件、目录外目录、绝对路径的链接拿不到目标内容：一般路径回落 `index.html`，`assets/` 下的返回 404。
   - 目录里的 FIFO 不让请求挂住。
   - `index.html` 本身是越界链接时，hub 拒绝启动。
   - 目录被原子替换后，下一个请求读到新内容。
   - 点文件与点目录（`.env`、`.git/config`、`sub/.hidden.txt`）回落 `index.html`，`assets/` 下的点文件返回 404；嵌入产物同一规则。

   测试在 Task 8 Step 1（`internal/hub/web/dir_test.go`、`internal/hub/web/web_test.go`）。

4. **GET 查询串里的 base64。** 下列每种请求，快照缓存的应答都与直连 connect 相同（状态码、Content-Type、Content-Encoding、Vary、正文；同一请求带两行 `Accept-Encoding` 时也相同）：
   - `base64=1` 包着 `{}`，带填充与不带填充
   - `base64=1` 却给字面 `{}`
   - proto 编码的空消息，带与不带 `base64=1`
   - `base64=0`
   - 重复的 `encoding` 参数

   缓存不替 connect 接受它会拒绝的请求。测试在 Task 7 Step 1（`TestSnapshotCacheIsTransparent`）。

5. **节点从公开改为私有之后的缓存窗口。**
   - hub 已缓存的快照最多再下发 1 秒（`snapshotTTL`），1 秒后不再出现；浏览器与中间缓存按 `max-age=1` 还可能再用 1 秒。
   - `QueryMetrics` 立即返回 `NotFound`。
   - 失败响应带 `Cache-Control: no-store`，节点改回公开后浏览器不会继续用缓存的 NotFound。

   测试在 Task 7 Step 1（`TestSnapshotCacheWindowAfterNodeTurnsPrivate`）与 Task 6 Step 1（`TestPublicCacheControlPerMethod`）。

## 实验与读码结论

凡由实验得出的结论，都注明了版本；换版本要重跑，不能直接改数字。

1. **buf 1.50.0，破坏性检查（`buf breaking` WIRE_JSON）。**
   - 做法：把 `QueryMetricsRequest`…`ProbeSample` 八个消息从 `admin.proto` 移到新文件 `query.proto`（同包 `probe.v1`），`Traffic` 移到 `types.proto`，并给 `ProbeSeries` 加字段 3、4。
   - 结果：`buf lint` 与 `buf breaking --against <移动前>` 均退出 0。
   - 冒烟：把 `Traffic.total_rx` 改成 `string`，`buf breaking` 退出 100，并指出该字段。说明这条检查真的在比对。

2. **buf 1.50.0，STANDARD lint。**
   - 公开服务复用 `QueryMetricsRequest/Response` 后，`RPC_REQUEST_RESPONSE_UNIQUE` 同时在 `admin.proto` 与 `public.proto` 报错。
   - `GetSite` 直接返回 `PublicSite`、`GetSnapshot` 直接返回 `PublicSnapshot`，触发 `RPC_RESPONSE_STANDARD_NAME`。
   - 在 `buf.yaml` 里用 `lint.ignore_only` 按文件豁免这两条规则后，lint 退出 0。
   - 去掉 `RPC_REQUEST_RESPONSE_UNIQUE` 的豁免，lint 退出 100，四处复用都被列出。

3. **protoc-gen-es：只为选项而 import 的文件，同样成为运行时依赖。**
   - 证据：现有 `web/src/gen/probe/v1/admin_pb.ts` 第 9 行 `import { file_probe_v1_access } from "./access_pb"`；`fileDesc(…, [file_probe_v1_types, file_probe_v1_access])`。
   - 推论：若 `public.proto` import `admin.proto`，`public_pb.ts` 就会 import `admin_pb.ts`，公开包里会带上 `AdminService` 的描述符，import 扫描永远过不了。这是拆出 `query.proto` 的原因。

4. **生成的描述符串在 JS 包里有稳定前缀。**
   - 以 `\n\x14probe/v1/admin.proto` 开头的 base64 前缀是 `ChRwcm9iZS92MS9hZG1pbi5wcm90`，出现在现有面板包里（`grep -c` 为 1）。
   - `public.proto` 的对应前缀是 `ChVwcm9iZS92MS9wdWJsaWMucHJv`。
   - Task 10 用这两个前缀检查构建产物。

5. **Chrome 153（Playwright）。**
   - `getComputedStyle(root).getPropertyValue('--muted')` 返回原串 `light-dark(#6b7280,#9ca3af)`；canvas 不认这种写法。
   - 元素的计算后 `color` 会解析成 rgb，并随 `html[data-theme]` 切换：light 为 `rgb(107, 114, 128)`，dark 为 `rgb(156, 163, 175)`。

6. **jsdom 30.1.0。**
   - 计算后 `color` 不解析 `var()`，原样返回 `var(--line)`。
   - 内联自定义属性的 `getPropertyValue` 可用。
   - 所以 Chart 的单测要替换 `getComputedStyle`。真实解析由 Task 10 的浏览器验收覆盖。

7. **go1.27.1 darwin/arm64 的 `os.Root`。**
   - 以下三种都失败并报 `path escapes from parent`：指向目录外的文件或目录的符号链接、绝对路径的符号链接、`../`。
   - 指向目录内的符号链接照常跟随。
   - 不带 `O_NONBLOCK` 打开目录里的 FIFO，会一直阻塞到有写端。带 `O_NONBLOCK` 立即返回，mode 为 `p`，不是普通文件。
   - 目录也不是普通文件。

8. **SQLite WAL 下的库大小（modernc SQLite 1.59.0，本仓库 `store.Open`）。**
   - 写入 200 个节点后：`PRAGMA page_count × page_size` 为 196608，主文件大小只有 4096；其余页还在 WAL 里。
   - 执行 `PRAGMA wal_checkpoint(TRUNCATE)` 后，两者都是 196608。
   - 所以 `db_bytes` 取逻辑大小 `page_count × page_size`，它等于检查点之后主文件的大小。

9. **读码：connect-go v1.21.0。**
   - 解码先于拦截器。
   - GET 形态的参数是 `connect`、`encoding`、`message`、`base64`、`compression`。
   - 请求带压缩时响应沿用同一种；否则取 `Accept-Encoding` 里第一个支持的名字，只按 `,` 与空格切分。只读第一行 `Accept-Encoding`（`protocol_connect.go` 用 `getHeaderCanonical` 取值）。默认只注册 gzip。
   - GET 响应带 `Vary: Accept-Encoding`。
   - `connect.NewErrorWriter` 能在 HTTP 中间件里按请求的协议写出错误。

   以上先由读码得出，随后实测：把 Task 7 的实现与测试原样放进仓库副本（go1.27.1 darwin/arm64，connect-go v1.21.0）运行。结果：
   - 29 种请求形态经缓存与直连 connect 的应答一致（状态码、Content-Type、Content-Encoding、Vary、解压后的正文），其中包括两行 `Accept-Encoding`（第一行 identity、第二行 gzip）与带未知字段的 POST 正文。
   - 规范形态在键已有缓存时不进 connect。
   - `Accept-Encoding: gzip;q=0` 协商为 identity，`br, gzip` 协商为 gzip，两行 `identity` 与 `gzip` 协商为 identity（只看第一行），与 `negotiatedCompression` 的复刻一致。

   即使如此，快照缓存也不依赖这些结论：只在"connect 实际的 Content-Encoding 与键一致"时才入缓存。

10. **读码：现有管理请求的解码预算装不下合法的 `UpdateSettings`。**
    - 现有 `maxBody = 64 << 10`，单是一个满额 logo（128 KiB）就超过了。
    - Task 3 给每个字段的合法取值都定出字节上限（标题另加清洗前 1024 字节），按上限与最坏转义重新推导这个预算，并用"满额设置在最坏转义下被接受"的测试钉住。

## 设计决定（spec 未定，由本计划定）

1. **历史查询类型独立成 `proto/probe/v1/query.proto`；`Traffic` 移到 `types.proto`。**
   - 实际做法：公开服务复用这些类型，但类型不再定义在 `admin.proto` 里。这改了用户所说"复用 admin.proto 的类型"的字面，保留了复用本身。
   - 理由：见实验 3。
   - 这些消息在同包内换文件，Go 标识符与 JSON 名不变，实验 1 已确认线上兼容。

2. **`ProbeSeries` 加 `ProbeKind kind = 3; string target = 4;`。两端的标注口径不同，由调用方决定。**
   - 取值来自查询时的任务清单。历史行只带 `task_id`（`AUTOINCREMENT` 保证编号不复用），标注取当前配置而不是历史配置。
   - **管理端**（`AdminService.QueryProbes`）：`probe.Registry.Target(id)`，按任务当前的配置标注。任务改过目标时整段历史按新目标标注，这与现有面板图例的行为相同；已删除的任务两项留空。
   - **公开端**（`PublicService.QueryProbes`）：`probe.Registry.TargetFor(nodeID, id)`，只标注当前分配给被查节点的任务，其余两项留空。
     - 理由：节点公开即公开它正在探测的目标。一个已从公开节点撤下、之后改成内网目标并只分配给私有节点的任务，新目标从未被该节点探测过，不在公开范围内。
     - 分配与目标在同一个读锁下读出，不会拼出两次发布之间的状态。
   - `history.probeSeries` 接收一个 `taskLabel` 函数，两端各传自己的口径；`history` 本身不再持有任务清单。
   - 管理面板的探测图例因此改为用序列自带的标签，`NodeDetail` 不再为图例另查 `ListProbeTasks`。公开页与面板用同一个 `seriesLabels`，留空的序列显示为"任务 #N"。

3. **缓存上界写在 proto 里。**
   - 新增 `proto/probe/v1/cache.proto`：在 `MethodOptions` 上扩展 `uint32 cache_max_age_s = 50002`。
   - `PublicService` 每个方法都声明这个值。
   - 不变式由装配期的显式检查承载：`cachePolicy` 遍历 `probe.v1` 包的全部服务（`protoregistry.GlobalFiles`），每个方法"接受 GET（`idempotency_level = NO_SIDE_EFFECTS`）"当且仅当"声明了正的 `cache_max_age_s`"，任一方向不符即 panic，hub 起不来。只检查 `PublicService` 不够：以后给别的服务的方法标 `NO_SIDE_EFFECTS`，它就接受 GET，却没有缓存上界。
   - 与 `probe.v1.access` 同一口径：proto 仍是单一事实源，第三方主题的开发者在 proto 注释里就能看到上界。

4. **`public.proto` 的命名。**
   - `GetSite(GetSiteRequest) returns (PublicSite)`；`GetSnapshot(PublicServiceGetSnapshotRequest) returns (PublicSnapshot)`。
   - 请求类型叫 `PublicServiceGetSnapshotRequest`，因为 `GetSnapshotRequest` 已是管理端的类型；buf 接受 `<Service><Method>Request` 这种名字。
   - 响应直接是 `PublicSite`/`PublicSnapshot`：spec 以这两个名字定义消息，第三方主题拿到的 JSON 顶层就是快照本身。代价是 `buf.yaml` 要按文件豁免两条规则（实验 2），豁免范围只到这两个文件。

5. **`PublicFacts` 与 `PublicMetrics` 沿用 `Facts`、`Metrics` 的字段号；缺的号用 `reserved` 保留（号与名都保留）。**
   - 要公开 `hostname` 这类字段，必须先删掉 `reserved` 行——这是一个显式动作。
   - 两者由 `projection` 按字段名从源消息复制。字段集合由公开消息决定：`Metrics` 以后加字段，不会自动出现在公开页。
   - 构造时逐字段核对名字、类型、基数、presence，任一不符即 panic。

6. **`Public` 与 `Service` 同在 `internal/hub/api`，共用实现。**
   - 共用四样：`history`（两族历史查询的唯一实现）、`checkWindow`（纯请求校验）、`liveState`（在线判定）、`trafficProto`。
   - 两个服务只在节点准入上不同：管理端 `requireNode` 返回 `node %d does not exist`；公开端 `requirePublic` 对非公开与不存在的节点返回同一个 `node_id: no public node has this id`。
   - 公开端的文案不带 id，因此两种情形的响应体逐字节相同。

7. **两个匿名入口的限流都是挂载点上的 HTTP 中间件（`ratelimit.BySource`），不是拦截器。**
   - 理由一：connect 先读取并解码请求，再进拦截器（实验 9）。放在拦截器里的限流数不到解码失败的请求：匿名来源持续发送坏 proto 或坏 JSON（Register 每个最多 256 KiB），一个都不计数。包在 connect 外面，到达挂载点的每个请求都计数，超限的请求也不再消耗解码。
   - 理由二（公开服务）：快照缓存在 connect 处理器之前应答，拦截器看不到缓存命中。限流包在缓存外面，才能做到"每个公开请求都计数"。
   - `Register` 原来的限速在拦截器里，改为同一个中间件：`ingest.Service.Handler` 只让路径恰为 `/probe.v1.AgentService/Register` 的请求经过它，路径判定与 connect 分派用同一个 `r.URL.Path` 全等比较。`Report` 不进这个桶：同一出口地址后面可以有很多 agent，上报按节点限速。
   - 中间件把算出的来源键放进请求的 context（`ratelimit.SourceOf`），`Register` 的窗口失败计数用同一个键，不再各算一遍。
   - 来源键由 `auth.SourceKey` 归一化：IPv4 按单个地址，IPv6 截到所在 /64 的网络地址。一台主机通常独占整个 /64（SLAAC 与隐私扩展地址随时可换），逐地址计键等于在 /64 里换个地址就换一份计数。
     - 按来源计数的三处都调它：限流（`BySource`，`SourceOf` 返回的就是它的结果）、`Register` 的窗口失败计数、登录失败锁定。后两处在 `failureTracker` 的三个入口归一化，调用方照旧传 `ClientIP` 的结果；`Register` 传入的已是键，再归一化一次不变（掩码两次与一次无异）。
     - 放在 `auth` 而不是 `ratelimit`：登录锁定在 `auth` 里，`auth` 依赖 `ratelimit` 会成环；依赖方向保持 `ingest`、`api` → `ratelimit` → `auth`。
     - IPv4 映射地址先还原成 IPv4：它们的前 64 位全是 0，不还原就全部落进 `::/64`。`ClientIP` 已经还原过，`SourceKey` 仍自己做一遍，不依赖调用方。
     - 超限的错误写 `2001:db8:1:2::/64` 这样的前缀（`auth.DescribeSource`），不把网络地址写成像一个具体地址。登录锁定的报错从 "from this address" 改为 "from this source (one IPv4 address, or one IPv6 /64)"。
     - 后果：`Register` 的日志里 `from` 是这个键，IPv6 时不再是具体地址；登录日志的 `from` 仍是具体地址（归一化在计数器里）。
   - 反代后不配 `--trusted-proxies` 时，来源都是代理地址，全体访客共用一个桶。这是部署配置问题，不改限流：写进 `--trusted-proxies` 的 flag 帮助（Task 8）与 README 的反代一节（Task 12），后者带量级推导。
   - 桶都是 `internal/hub/ratelimit` 的 `Buckets`。补充周期从"每次调用传入"改为"构造时固定"：回收空闲桶的推论依赖补充周期不变，原来只靠约定保证，现在由结构承载。

8. **挂载链是 `cacheControl(ratelimit.BySource(snapshotCache(connect)))`。**
   - `cacheControl` 只作用于 GET：200 按方法写 `max-age=N`；非 200（含限流的 429 与 NotFound）写 `no-store`。POST 不写缓存头。
   - 失败响应写 `no-store` 的原因：节点改为公开后，浏览器不能继续用缓存里的 NotFound 挡住访客。

9. **快照缓存只回答规范形态的请求；键是 `{GET 或 POST, codec, 协商出的压缩}`。**
   - 缓存键不含请求内容，所以缓存只能回答"connect 必然以同样方式成功处理"的请求。规范形态：
     - GET：`connect` 缺省或为 `v1`；`encoding` 为 `json` 且 `message` 为 `{}`、不带 `base64`，或 `encoding` 为 `proto` 且 `message` 为空、`base64` 缺省或为 `1`；`compression` 缺省或为 `identity`；每个参数至多出现一次。
     - POST：`Content-Type` 恰为 `application/json` 或 `application/proto`；`Connect-Protocol-Version` 缺省或为 `1`；无 `Content-Encoding`；正文恰为 `{}` 或空。
     - 两种方法都不带 `Connect-Timeout-Ms`。
   - 其余形态全部交给 connect，不经缓存（宁可少命中）。
   - 每个键有自己的互斥锁：同键请求排队，窗口内只序列化一次。
   - 只在"状态 200 且 connect 实际的 Content-Encoding 与键一致"时入缓存。
   - 命中时写回克隆的头，并补上 `Content-Length`。窗口 `snapshotTTL = time.Second`，按 `clk.Mono()` 计时。

10. **设置校验（`internal/hub/api/settings.go`）。**
    - **标题：** 清洗前不超过 1024 字节（`maxTitleBytes`）：只限清洗后的字符数时原始标题没有上限，设计决定 11 的预算推导就不成立。清洗与节点名同一口径——`sanitize.String` 去控制字符、`TrimSpace` 去首尾空白后计字符数，不超过 64。空串表示公开页用内置标题 `服务器状态`。
    - **主色：** 接受大小写，存小写；空串表示内置配色。
    - **logo：** 只接受 `data:<type>;base64,<data>` 这一种写法：
      - type 为白名单里的全小写值，不带参数。
      - data 非空，先逐字节核对标准 base64 字母表——Go 的解码器会跳过 `\r\n`，所以这一步不能省；再用 `StdEncoding.Strict()` 解码。
      - 整串不超过 131072 字节。
      - 收窄到一种写法的理由："是不是白名单里的图片"只有一个答案，宽松解析与浏览器解析一旦不一致，白名单就能被绕过。
    - **CSS：**
      - 不清洗，按字节原样存。
      - 只查字面序列 `</`，这一条就覆盖了全部大小写变体（`</` 本身不含字母）。
      - CSS 转义与 HTML 实体在 `<style>` 的 RAWTEXT 里都不被解码，不拒绝。
    - 任一项不合约束，整次更新不写入任何东西。

11. **管理请求的解码预算由设置上限推出。**
    - 公式：`maxBody = maxLogoBytes + 6*maxCSSBytes + 6*maxTitleBytes + 4<<10`，即 534528 字节。
    - 推导：logo 是 base64 文本，满额时在 JSON 里无需转义。自定义 CSS 与清洗前的标题满额，且每个字节都转义成 6 字节的 `\u00XX`（控制字符就是这样）。另留 4 KiB 给明暗、主色、字段名与 JSON 语法。前提是每个字段的合法取值都有字节上限：明暗与主色由取值集合与格式限定，标题由 `maxTitleBytes` 限定。
    - 覆盖范围写明：满额设置在最坏转义下的 JSON 装得下。多余的 JSON 空白、对无需转义的字符的转义不在预算内，这样的请求超出预算时得到 `resource_exhausted`。
    - 未鉴权请求的解码上限因此从 64 KiB 升到约 522 KiB。它仍然有界，这是为"满额的 `UpdateSettings` 不在校验之前被拒"付出的代价。
    - 另一种做法是按方法分预算：要在 connect 之前再加一层按路径的 `MaxBytesReader`，并与 connect 的解压后上限配合。用两套上限换一个方法的余量，不划算。

12. **`setting` 表用 `CREATE TABLE setting (key TEXT PRIMARY KEY, value TEXT NOT NULL)`，不用 `WITHOUT ROWID`。**
    - 理由：值可达 128 KiB。`WITHOUT ROWID` 表把整行放进主键 B 树，SQLite 文档建议这种表的行不超过页大小的约 1/20。
    - 键名 `site.title`、`site.theme`、`site.accent_color`、`site.logo`、`site.custom_css` 是持久标识。
    - 从未保存过时，明暗为 `auto`，其余为空串。

13. **存储统计由 `store.StorageStats` 在一个只读事务里算出，替换并删除 `Store.Counts`。**
    - 表名取自 `sqlite_master`，不再手写清单。现有 `Counts` 的手写清单漏了 `api_token`、`probe_meta`、`rollup_state`——正是"手写枚举"这种缺陷形状。
    - `db_bytes` 是逻辑大小（实验 8）。
    - 内部表按字面前缀 `substr(name, 1, 7) <> 'sqlite_'` 排除；`LIKE 'sqlite_%'` 的 `_` 是通配符，且对 ASCII 不分大小写。
    - CLI 输出先一行 `db_bytes: N`，再逐表 `name: rows`，按表名升序。e2e 现有的 `get` 解析照常可用。
    - `GetStorageStats` 保持只读口径（`ACCESS_READ`）：各表行数是聚合值，token 持有者看到 `api_token` 与 `admin_session` 的行数，不构成列出 token（§5.6 禁的是枚举与吊销其他 token）。

14. **静态服务共用 `serveFiles` 核心。**
    - 只服务普通文件；目录、FIFO、设备一律当作不存在，因此任何来源都不列目录，也不会在特殊文件上阻塞。
    - `rel` 为 `assets` 或以 `assets/` 开头时未命中返回 404，否则回落 `index.html`。`/admin/assets` 因此由 301 变为 404。
    - `DirHandler` 每个请求都 `os.OpenRoot(dir)`，原子替换目录（rename）后下一个请求就读到新内容。打开时带 `O_NONBLOCK`。
    - 启动时核对 `index.html` 是普通文件，否则 `serve` 在打开数据库之前就报错。
    - 路径任一段以 `.` 开头的名字一律当作不存在（`hidden`），在 `serveFiles` 里判定，三个来源同一条规则。
      - 理由：`.git/config`、`.env` 是运维把检出或构建目录当 `--public-dir` 时最常见的泄漏。
      - 判定只看请求路径里各段的名字，符号链接按链接自己的名字算：名字不带点、指向点文件的链接照常服务，那是运维的显式动作。
      - 后果：`.well-known/` 也不服务，需要它的（ACME 校验、security.txt）由反向代理提供。嵌入产物里的 `.gitkeep` 同样不再被服务。
    - `RootRedirect` 删除。

15. **前端结构。**
    - **配色：** 调色板改用 `light-dark()` 加 `color-scheme`。`:root[data-theme="light"|"dark"]` 覆盖 `color-scheme`，删除原来的 `@media (prefers-color-scheme: dark)` 块，这样站点设置的明暗能压过系统设置。`light-dark()` 需要 2024 年以后的浏览器（Chrome 123、Firefox 120、Safari 17.5）。
    - **图表：** 经 `lib/colorScheme.ts` 跟随 `data-theme` 与系统明暗，颜色由浏览器解析成 rgb 再交给 uPlot（实验 5）。
    - **共用件：** `components/History.tsx`（时间范围、两族查询、图表）、`components/Bar.tsx`、`lib/poll.ts` 从面板页面里抽出，面板与公开页共用。
    - **公开入口：** 在 `web/src/public/`，Vite 以 `--mode public` 构建，根是 `src/public`，base 是 `/`，产物落到 `internal/hub/web/dist-public`。
    - `DEFAULT_TITLE` 定义在 `web/src/public/site.ts`，面板的外观页从那里引用。依赖方向只禁止公开到管理，不禁止管理到公开。
    - 公开页只在加载时取 `GetSite`，之后不再重取（`staleTime: Infinity`；窗口聚焦重取在公开页的 `QueryClient` 里关掉，重连与重新挂载只重取已过期的查询）：已打开的页面刷新后才看到外观改动，刷新时浏览器还可能再用最多 5 分钟的缓存（`max-age=300`）。面板外观页的说明照此写。

16. **§12 的匿名白名单改为恰好四个 `PublicService` 过程。**
    - `Register` 与 `Login` 用 `{}` 调用时，各自的方法体本来就返回 `Unauthenticated`（没有注册窗口 / 没有管理员），留在白名单里只会让测试变弱。
    - 白名单内的过程另断言"不是 401"，证明它们确实可以匿名到达。

17. **`GetStorageStats` 本计划不做面板界面。** 范围只列了外观页；数据经 API 与 CLI 可得。

18. **入口卡片 `proto/SKILL.md` 增加"公开数据"一节与一个 GET 例子。** 卡片写的是"这个 API 在哪、怎么进门"，公开服务是第二个门。例子由 e2e 执行。

## 文件结构

**proto 与生成**
- `proto/probe/v1/query.proto`（新）：历史查询类型。
- `proto/probe/v1/cache.proto`（新）：`cache_max_age_s`。
- `proto/probe/v1/public.proto`（新）：`PublicService` 与 `Public*` 消息。
- `proto/probe/v1/admin.proto`：移出查询类型；加设置与存储统计三个方法。
- `proto/probe/v1/types.proto`：收下 `Traffic`。
- `buf.yaml`：按文件豁免两条 lint 规则。
- 生成物：`gen/probe/v1/*`、`web/src/gen/probe/v1/*`。

**store**（`internal/hub/store/`）
- `setting.go`（新）：外观的读写。
- `stats.go`（新）：库大小与行数。
- `schema.go`、`migrations.go`、`store.go`：v8。
- `node.go`：公开节点查询；删除 `Counts`。

**ratelimit 与来源键**
- `internal/hub/ratelimit/ratelimit.go`（新）：令牌桶，替换 `internal/hub/ingest/limiter.go`。
- `internal/hub/ratelimit/source.go`（新）：按来源限流的 HTTP 中间件 `BySource`，`Register` 与公开服务共用。
- `internal/hub/auth/proxy.go`：`SourceKey`、`DescribeSource`，紧挨 `ClientIP`。按来源计数的三处（限流、`Register` 窗口失败计数、登录失败锁定）都调它。
- `internal/hub/auth/failures.go`：失败计数与锁定按来源键计；`admin.go`、`auth.go` 的注释与 `ErrLocked` 文案随之改。依赖方向 `ingest`、`api` → `ratelimit` → `auth`。

**api**（`internal/hub/api/`）
- `history.go`（新）：共用的历史、窗口与在线判定。
- `projection.go`（新）：公开消息投影。
- `settings.go`（新）：设置与存储统计方法及校验。
- `public.go`（新）：`PublicService` 与挂载中间件。
- `snapshot_cache.go`（新）：快照响应字节缓存。
- `data.go`、`probes.go`、`service.go`：改为调用共用实现；`service.go` 里登录锁定的报错随来源键改文案。

**ingest**
- `internal/hub/ingest/service.go`：`Register` 的来源限速从拦截器移到 `Handler` 的中间件。

**web 静态服务**（`internal/hub/web/`）
- `web.go`：嵌入两份产物，共用 `serveFiles`；点文件在其中一律当作不存在。
- `dir.go`（新）：`--public-dir`。
- `dist-public/.gitkeep`（新）。

**cmd/hub**
- `serve.go`：挂载 `PublicService`、公开页与 `--public-dir`；`--trusted-proxies` 的帮助写明反代后不配它的后果。
- `stats.go`、`main.go`：新的统计输出。

**前端**
- 公开页：`web/src/public/*`（新）。
- 共用件：`web/src/components/History.tsx`、`Bar.tsx`（新），`web/src/lib/colorScheme.ts`、`poll.ts`（新）。
- 外观设置：`web/src/lib/appearance.ts`、`web/src/pages/Appearance.tsx`（新）。
- 构建配置：`web/vite.config.ts`、`web/package.json`。

**其他**
- `.gitignore`、`Makefile`（仅 `web` 目标的注释）
- `proto/SKILL.md`
- `scripts/e2e.sh`、`scripts/install-accept.sh`（就绪判据）
- `README.md`（反代一节：不配 `--trusted-proxies` 时限流共用一个桶的量级）

## 开工

- [ ] 确认工作树并跑基线

工作树 `/Users/xjetry/work/vibe/probe-public` 由控制端建好，本计划不新建工作树。先确认它在 `m5m6-public` 分支上、提交不早于 main 的 63d0800，再跑基线。基点要含 63d0800：它之前的 main 合入了 hub Docker 镜像的改动（`README.md` 由此而来，Task 12 改其中反代一节；`store.go`、`store_test.go`、`.gitignore`、`Makefile` 的原文以它为准），63d0800 本身是按本计划回写的 spec。

```bash
cd /Users/xjetry/work/vibe/probe-public && git rev-parse --abbrev-ref HEAD > /tmp/m5-setup-branch.log 2>&1; echo $?
cd /Users/xjetry/work/vibe/probe-public && git merge-base --is-ancestor 63d0800 HEAD > /tmp/m5-setup-ancestor.log 2>&1; echo $?
cd /Users/xjetry/work/vibe/probe-public && git log --oneline -1 > /tmp/m5-setup-head.log 2>&1; echo $?
cd /Users/xjetry/work/vibe/probe-public && make web-install > /tmp/m5-setup-install.log 2>&1; echo $?
cd /Users/xjetry/work/vibe/probe-public && go test -count=1 ./... > /tmp/m5-setup-go.log 2>&1; echo $?
cd /Users/xjetry/work/vibe/probe-public && pnpm --dir web exec vitest run > /tmp/m5-setup-web.log 2>&1; echo $?
```

Expected：
- 六条都是 0。
- `m5-setup-branch.log` 为 `m5m6-public`。第二条退出 0 表示 63d0800 是当前提交的祖先，即工作树不早于 main 的 63d0800；退出 1 说明基点落后，先找控制端。

这两份基线日志是之后判定"既有 flake"的对照：同一条命令在基线上不红，才谈得上既有。

---

### Task 1: 历史查询类型独立成 query.proto，探测序列带任务的种类与目标

**Files:**
- Create: `proto/probe/v1/query.proto`（由 Step 1 的脚本从 `admin.proto` 逐字搬出）
- Modify: `proto/probe/v1/admin.proto`（删去 9 个消息，import `query.proto`）
- Modify: `proto/probe/v1/types.proto`（末尾收下 `Traffic`）
- Generated（`make gen`）：
  - Go：`gen/probe/v1/query.pb.go`、`admin.pb.go`、`types.pb.go`
  - TS：`web/src/gen/probe/v1/query_pb.ts`、`admin_pb.ts`、`types_pb.ts`
- Modify: `internal/hub/probe/registry.go`（`Target`）
- Modify: `internal/hub/api/probes.go`（`QueryProbes` 填 kind/target）
- Test: `internal/hub/api/probes_test.go`
- Modify: `web/src/lib/probes.ts`、`web/src/lib/series.ts`、`web/src/pages/NodeDetail.tsx`
- Test: `web/src/lib/probes.test.ts`、`web/src/lib/series.test.ts`、`web/src/pages/NodeDetail.test.tsx`

**Interfaces:**
- Produces（proto）：
  - 新文件 `probe/v1/query.proto`，含 `QueryMetricsRequest`、`QueryMetricsResponse`、`MetricSeries`、`MetricSample`、`QueryProbesRequest`、`QueryProbesResponse`、`ProbeSeries`、`ProbeSample`。
  - `ProbeSeries` 新增 `ProbeKind kind = 3`、`string target = 4`。
  - `Traffic` 移到 `probe/v1/types.proto`。
  - Go 标识符不变（同包）。TS 类型改从 `web/src/gen/probe/v1/query_pb.ts` 与 `types_pb.ts` 导出。
- Produces（Go）：`func (r *probe.Registry) Target(id uint64) (kind probev1.ProbeKind, target string, ok bool)`。
- Produces（TS，`web/src/lib/probes.ts`）：
  - `seriesLabel(s: ProbeSeries): string`
  - `seriesLabels(series: readonly ProbeSeries[]): string[]`
  - `taskLabel(id: bigint, tasks: readonly { task?: ProbeTask }[] | undefined): string`
  - `taskLabels(ids: bigint[], tasks: readonly { task?: ProbeTask }[] | undefined): string[]`

- [ ] **Step 1: 搬移消息并加字段**

```bash
cd /Users/xjetry/work/vibe/probe-public && python3 - > /tmp/m5-t1-move.log 2>&1 <<'PY'; echo $?
import re
p = 'proto/probe/v1/admin.proto'
s = open(p).read()
moved = {}
for n in ['QueryMetricsRequest', 'QueryMetricsResponse', 'MetricSeries', 'MetricSample', 'Traffic',
          'QueryProbesRequest', 'QueryProbesResponse', 'ProbeSeries', 'ProbeSample']:
    m = re.search(r'^message ' + n + r' \{\n.*?^\}\n', s, re.S | re.M)
    assert m, n
    moved[n] = m.group(0)
    s = s[:m.start()] + s[m.end():]
s = re.sub(r'\n{3,}', '\n\n', s)
imp = 'import "probe/v1/access.proto";\n'
assert imp in s
s = s.replace(imp, imp + 'import "probe/v1/query.proto";\n', 1)
open(p, 'w').write(s)

old = ('message ProbeSeries {\n  uint64 task_id = 1;\n'
       '  // 按 ts 升序；只包含 sent > 0 的点，缺失的 ts 表示该段没有结果。\n'
       '  repeated ProbeSample samples = 2;\n}\n')
assert moved['ProbeSeries'] == old, moved['ProbeSeries']
moved['ProbeSeries'] = old[:-2] + (
    '  // 任务的种类与目标，查询时从任务清单读取；不标注时 kind 为 PROBE_KIND_UNSPECIFIED、target 为空串，\n'
    '  // 客户端退回用 task_id 称呼。AdminService.QueryProbes 按任务当前的配置标注：改过目标的任务整段历史按新目标标注，\n'
    '  // 已删除的任务不标注。PublicService.QueryProbes 只标注当前分配给被查节点的任务：节点公开即公开它正在探测的目标，\n'
    '  // 已从该节点撤下的任务，当前目标不一定被该节点探测过，不标注。\n'
    '  ProbeKind kind = 3;\n  string target = 4;\n}\n')

header = '''syntax = "proto3";

package probe.v1;

option go_package = "github.com/xjetry/probe/gen/probe/v1;probev1";

import "probe/v1/types.proto";

// 历史查询的请求与响应。AdminService 与 PublicService 的 QueryMetrics、QueryProbes 共用这些类型；
// 它们独立成文件，公开页的生成代码因此只依赖本文件与 types.proto，不连带管理服务的描述符。
'''
order = ['QueryMetricsRequest', 'QueryMetricsResponse', 'MetricSeries', 'MetricSample',
         'QueryProbesRequest', 'QueryProbesResponse', 'ProbeSeries', 'ProbeSample']
open('proto/probe/v1/query.proto', 'w').write(header + ''.join('\n' + moved[n] for n in order))
t = open('proto/probe/v1/types.proto').read()
open('proto/probe/v1/types.proto', 'w').write(t.rstrip('\n') + '\n\n' + moved['Traffic'])
PY
cd /Users/xjetry/work/vibe/probe-public && make gen > /tmp/m5-t1-gen.log 2>&1; echo $?
cd /Users/xjetry/work/vibe/probe-public && buf lint > /tmp/m5-t1-lint.log 2>&1; echo $?
cd /Users/xjetry/work/vibe/probe-public && buf breaking --against '/Users/xjetry/work/vibe/probe/.git#branch=main' > /tmp/m5-t1-breaking.log 2>&1; echo $?
cd /Users/xjetry/work/vibe/probe-public && go build ./... > /tmp/m5-t1-build.log 2>&1; echo $?
```

Expected：
- 五条都是 0。
- `git status --short` 列出 `proto/probe/v1/{admin,query,types}.proto`、`gen/probe/v1/query.pb.go` 与 `web/src/gen/probe/v1/query_pb.ts`。
- `grep -c '^message' proto/probe/v1/query.proto` 为 8。

- [ ] **Step 2: 写失败测试**

`internal/hub/api/probes_test.go` 末尾追加：

```go
// 图例要的种类与目标随序列下发，取自查询时的任务清单：改过目标的任务按新目标标注，
// 清单里已没有的任务（删除后仍有历史）两者都空，由客户端退回编号。
func TestQueryProbesLabelsSeriesWithCurrentTaskConfig(t *testing.T) {
	h := newHarness(t, "")
	h.login(t)
	id, _ := h.createNode(t, "n")
	saved, err := h.admin.SaveProbeTask(t.Context(), connect.NewRequest(&probev1.SaveProbeTaskRequest{
		Task:    &probev1.ProbeTask{Kind: probev1.ProbeKind_PROBE_KIND_TCP, Target: "example.com:443", IntervalS: 30, TimeoutMs: 1000},
		NodeIds: []int64{id},
	}))
	if err != nil {
		t.Fatal(err)
	}
	task := saved.Msg.GetTask().GetTask().GetId()
	gone := task + 1000
	base := h.clk.Now().Truncate(time.Hour).Unix()
	rows := []metric.ProbeRow{
		{NodeID: id, TS: base, TaskID: task, Bucket: &metric.ProbeBucket{Sent: 1, Lost: 1}},
		{NodeID: id, TS: base, TaskID: gone, Bucket: &metric.ProbeBucket{Sent: 1, Lost: 1}},
	}
	if _, err := h.store.WriteMinuteBatch(t.Context(), metric.Batch{Probes: rows}); err != nil {
		t.Fatal(err)
	}
	if _, err := h.admin.SaveProbeTask(t.Context(), connect.NewRequest(&probev1.SaveProbeTaskRequest{
		Task:    &probev1.ProbeTask{Id: task, Kind: probev1.ProbeKind_PROBE_KIND_ICMP, Target: "192.0.2.1", IntervalS: 30, TimeoutMs: 1000},
		NodeIds: []int64{id},
	})); err != nil {
		t.Fatal(err)
	}
	resp, err := h.admin.QueryProbes(t.Context(), connect.NewRequest(&probev1.QueryProbesRequest{NodeId: id, From: base, To: base + 3600}))
	if err != nil {
		t.Fatal(err)
	}
	type label struct {
		kind   probev1.ProbeKind
		target string
	}
	want := map[uint64]label{task: {probev1.ProbeKind_PROBE_KIND_ICMP, "192.0.2.1"}, gone: {}}
	got := map[uint64]label{}
	for _, s := range resp.Msg.GetSeries() {
		got[s.GetTaskId()] = label{s.GetKind(), s.GetTarget()}
	}
	if len(got) != len(want) {
		t.Fatalf("labels = %v, want %v", got, want)
	}
	for id, w := range want {
		if got[id] != w {
			t.Fatalf("labels = %v, want %v", got, want)
		}
	}
}
```

- [ ] **Step 3: 跑红**

```bash
cd /Users/xjetry/work/vibe/probe-public && go test -count=1 -run TestQueryProbesLabelsSeriesWithCurrentTaskConfig ./internal/hub/api/ > /tmp/m5-t1-red.log 2>&1; echo $?
```

Expected：1。日志里有 `labels = map[...:{0 } ...]`，当前任务那一项的 kind 为 0、target 为空。

- [ ] **Step 4: 实现**

`internal/hub/probe/registry.go`，放在 `List` 之后：

```go
// Target 返回任务当前的种类与目标，是管理端标注历史序列的口径。任务不在清单里时 ok 为 false：
// 历史行只带 task_id，而任务表的 AUTOINCREMENT 保证编号不复用，查不到就是删了，不会标成别的任务。
func (r *Registry) Target(id uint64) (kind probev1.ProbeKind, target string, ok bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	t, ok := r.tasks[id]
	if !ok {
		return probev1.ProbeKind_PROBE_KIND_UNSPECIFIED, "", false
	}
	return t.Kind, t.Target, true
}
```

`internal/hub/api/probes.go` 的 `QueryProbes` 里，新建序列处改为：

```go
		if cur == nil || cur.TaskId != r.TaskID {
			cur = &probev1.ProbeSeries{TaskId: r.TaskID}
			// 标签取查询时的任务清单；已删除的任务两项留空，客户端退回编号。
			cur.Kind, cur.Target, _ = s.probes.Target(r.TaskID)
			resp.Series = append(resp.Series, cur)
		}
```

- [ ] **Step 5: 跑绿**

```bash
cd /Users/xjetry/work/vibe/probe-public && go test -count=1 ./internal/hub/... > /tmp/m5-t1-green.log 2>&1; echo $?
```

Expected：0。现有的 `TestQueryProbesGroupsPerTaskAndOmitsEmptyPoints` 不变也通过：它的任务 7、9 不在清单里，两项本来就空。

- [ ] **Step 6: 面板的失败测试**

`web/src/lib/series.test.ts` 与 `web/src/lib/probes.test.ts` 的 import 改为：

```ts
// series.test.ts
import { QueryMetricsResponseSchema } from "../gen/probe/v1/query_pb";
// probes.test.ts
import { ProbeTaskDetailSchema } from "../gen/probe/v1/admin_pb";
import { ProbeSeriesSchema, QueryProbesResponseSchema } from "../gen/probe/v1/query_pb";
import { PROBE_KINDS, kindLabel, lossPercent, rttMeanMs, seriesLabels, taskIdsOf, taskLabel, taskLabels, toProbeAligned } from "./probes";
```

`probes.test.ts` 在第一个 `it.each` 之后加：

```ts
it.each([
  {
    series: [{ taskId: 7n, kind: ProbeKind.ICMP, target: "host" }, { taskId: 3n, kind: ProbeKind.ICMP, target: "host" }, { taskId: 9n }],
    labels: ["ICMP host #7", "ICMP host #3", "任务 #9"],
  },
  { series: [{ taskId: 3n, kind: ProbeKind.TCP, target: "1.1.1.1:443" }], labels: ["TCP 1.1.1.1:443"] },
])("序列标签取序列自带的种类与目标，已删除任务用编号 $labels", ({ series, labels }) => {
  expect(seriesLabels(series.map((s) => create(ProbeSeriesSchema, s)))).toEqual(labels);
});
```

`web/src/pages/NodeDetail.test.tsx` 改动：

- 第 2 行改为 `import { QueryProbesResponseSchema } from "../gen/probe/v1/query_pb";`（不再用 `ListProbeTasksResponseSchema`）。
- `defaultImpl` 删去 `listProbeTasks` 一行。
- "五个查询同文刷新失败只显示一条"：改名"四个查询同文刷新失败只显示一条"，删去 `listProbeTasks: failing(...)` 一行。
- 删除两个用例："任务列表查询失败显示错误，探测图例仍以编号可辨认"、"任务列表迟到时先用编号，标签到达后更新且切窗挂起时保留探测图"。
- 下列两个用例删去其中的 `listProbeTasks` 一行，其余不变："窗口内没有探测结果时给出去向"、"主机信息显示 ICMP 是否可用"。
- "同窗口同名任务在两张探测图中带编号区分"整体替换为：

```ts
it("同窗口同名任务在两张探测图中带编号区分", async () => {
  renderWithAdmin({
    ...defaultImpl,
    queryProbes: async () => create(QueryProbesResponseSchema, { stepS: 60, series: [
      { taskId: 7n, kind: ProbeKind.ICMP, target: "1.1.1.1" },
      { taskId: 3n, kind: ProbeKind.ICMP, target: "1.1.1.1" },
    ] }),
  }, [{ path: "/nodes/:id", Component: NodeDetail }], "/nodes/7");
  await screen.findAllByText("ICMP 1.1.1.1 #7");
  const charts = screen.getAllByTestId("chart").filter((chart) => chart.dataset.labels?.includes("ICMP"));
  expect(charts.map((chart) => chart.dataset.labels)).toEqual([
    "ICMP 1.1.1.1 #7,ICMP 1.1.1.1 #3",
    "ICMP 1.1.1.1 #7,ICMP 1.1.1.1 #3",
  ]);
});
```

- "探测图每个任务一条线，已删除任务用编号，且与指标查询共用同一窗口"：
  - 删去 `listProbeTasks` 一段。
  - `queryProbes` 返回的第一条序列改为 `{ taskId: 3n, kind: ProbeKind.ICMP, target: "1.1.1.1", samples: [...] }`，第二条 `{ taskId: 9n, samples: [...] }` 不变。
  - 断言不变（`ICMP 1.1.1.1` 两处、`任务 #9` 两处）。
- 文件末尾新增：

```ts
it("切窗请求挂起时保留探测图与图例", async () => {
  let releaseProbes!: () => void;
  let started!: () => void;
  const probeGate = new Promise<void>((resolve) => { releaseProbes = resolve; });
  const pending = new Promise<void>((resolve) => { started = resolve; });
  const queryProbes = vi.fn<NonNullable<AdminImpl["queryProbes"]>>(async (req) => {
    if (queryProbes.mock.calls.length > 1) { started(); await probeGate; }
    return create(QueryProbesResponseSchema, { stepS: 60, series: [
      { taskId: 3n, kind: ProbeKind.ICMP, target: "1.1.1.1", samples: [{ ts: req.from, sent: 1, rttMeanUs: 1000 }] },
    ] });
  });
  renderWithAdmin({ ...defaultImpl, queryProbes }, [{ path: "/nodes/:id", Component: NodeDetail }], "/nodes/7");
  try {
    expect(await screen.findAllByText("ICMP 1.1.1.1")).toHaveLength(2);
    await act(async () => { fireEvent.click(screen.getByRole("button", { name: "7d" })); await pending; });
    expect(screen.getAllByText("ICMP 1.1.1.1")).toHaveLength(2);
  } finally {
    await act(async () => { releaseProbes(); });
  }
});
```

- [ ] **Step 7: 跑红**

```bash
cd /Users/xjetry/work/vibe/probe-public && pnpm --dir web exec vitest run src/lib/probes.test.ts src/lib/series.test.ts src/pages/NodeDetail.test.tsx > /tmp/m5-t1-web-red.log 2>&1; echo $?
```

Expected：1。原因有两个：
- `seriesLabels` 不存在。
- `NodeDetail` 仍查 `listProbeTasks`：`defaultImpl` 不再实现它，路由传输返回 unimplemented，横幅多出一条错误，图例是 `任务 #3`。

- [ ] **Step 8: 实现面板改动**

`web/src/lib/series.ts` 第 2 行改为 `import type { QueryMetricsResponse } from "../gen/probe/v1/query_pb";`。

`web/src/lib/probes.ts`：
- import 改为：

```ts
import type { AlignedData } from "uplot";
import type { ProbeSample, ProbeSeries, QueryProbesResponse } from "../gen/probe/v1/query_pb";
import { ProbeKind, type ProbeTask } from "../gen/probe/v1/types_pb";
import { gridOf } from "./series";
```

- 把 `taskLabel`、`taskLabels` 两个函数替换为：

```ts
// 已删除任务的历史仍会返回；没有任务可查时用编号，让线仍有名字。
// 只要求每项带可选的 task：任务页与告警规则页传 ProbeTaskDetail，本文件因此不依赖管理端的生成代码，公开页也能用。
export function taskLabel(id: bigint, tasks: readonly { task?: ProbeTask }[] | undefined): string {
  const t = tasks?.find((d) => d.task?.id === id)?.task;
  if (!t) return `任务 #${id}`;
  return `${kindLabel(t.kind)} ${t.target}`;
}

export function taskLabels(ids: bigint[], tasks: readonly { task?: ProbeTask }[] | undefined): string[] {
  return disambiguate(ids.map((id) => taskLabel(id, tasks)), ids);
}

// 序列自带任务当前的种类与目标（hub 查询时从任务清单读出）；已删除的任务种类为 UNSPECIFIED，退回编号。
// hub 的任务准入只放行 ICMP 与 TCP（probelimit.CheckTask），所以其余种类只会是 UNSPECIFIED。
export function seriesLabel(s: ProbeSeries): string {
  return s.kind === ProbeKind.UNSPECIFIED ? `任务 #${s.taskId}` : `${kindLabel(s.kind)} ${s.target}`;
}

export function seriesLabels(series: readonly ProbeSeries[]): string[] {
  return disambiguate(series.map(seriesLabel), series.map((s) => s.taskId));
}

// 同一窗口可有配置不同却同名的任务；只给碰撞的标签追加编号，保留常见图例的简短形式。
function disambiguate(labels: string[], ids: readonly bigint[]): string[] {
  const counts = new Map<string, number>();
  for (const label of labels) counts.set(label, (counts.get(label) ?? 0) + 1);
  return labels.map((label, i) => (counts.get(label)! > 1 ? `${label} #${ids[i]}` : label));
}
```

`web/src/pages/NodeDetail.tsx`：
- probes 的 import 改为 `import { lossPercent, rttMeanMs, seriesLabels, taskIdsOf, toProbeAligned, type ProbeValue } from "../lib/probes";`。
- 删去 `tasks` 查询及其上方注释。
- `probeCharts` 改为：

```ts
  // 标签随序列下发（任务当前的种类与目标），与数据同一次响应到达，不另查任务列表。
  const probeCharts = useMemo(() => {
    if (!probes.data) return [];
    const ids = taskIdsOf(probes.data);
    const labels = seriesLabels(probes.data.series);
    return PROBE_PANELS.map((p) => ({ ...p, labels, data: toProbeAligned(probes.data!, ids, from, to, p.value) }));
  }, [probes.data, from, to]);
```

- 横幅改为 `{errorBanner(nodes.error, history.error, probes.error, traffic.error)}`。

- [ ] **Step 9: 跑绿**

```bash
cd /Users/xjetry/work/vibe/probe-public && pnpm --dir web exec tsc -b > /tmp/m5-t1-tsc.log 2>&1; echo $?
cd /Users/xjetry/work/vibe/probe-public && pnpm --dir web exec vitest run > /tmp/m5-t1-web-green.log 2>&1; echo $?
```

Expected：两条都是 0。全量 vitest 通过，其中 `AlertRules`、`ProbeTasks`、`alerts` 的用例仍以 `ProbeTaskDetail` 调 `taskLabel(s)`。

- [ ] **Step 10: 提交**

```bash
cd /Users/xjetry/work/vibe/probe-public && git add proto gen web/src/gen internal/hub/probe/registry.go internal/hub/api/probes.go internal/hub/api/probes_test.go web/src/lib web/src/pages/NodeDetail.tsx web/src/pages/NodeDetail.test.tsx && git commit -m "proto: 历史查询类型独立成 query.proto，探测序列带任务当前的种类与目标" -m "公开服务要复用 QueryMetrics 与 QueryProbes 的请求与响应；protoc-gen-es 把被 import 的文件生成为运行时依赖，类型留在 admin.proto 会让公开页的包带上 AdminService 的描述符。同包换文件，Go 标识符与 JSON 名不变，buf breaking（WIRE_JSON）通过。ProbeSeries 的种类与目标取自查询时的任务清单，已删除的任务两项留空；面板图例改用序列自带的标签，详情页不再为图例另查任务列表。" > /tmp/m5-t1-commit.log 2>&1; echo $?
```

Expected：0。

- [ ] **Step 11: 缺陷注入**

逐项进行：改动 → `git diff --stat` 非空 → 跑命令 → 核对红的原因 → `git checkout -- <文件>`。

| | 改动 | 命令 | 期望 |
|---|---|---|---|
| a | 删掉 `probes.go` 里 `cur.Kind, cur.Target, _ = s.probes.Target(r.TaskID)` 一行 | 同 Step 3 | 1，`labels =` 里当前任务为 `{0 }` |
| b | `Registry.Target` 查不到时返回 `probev1.ProbeKind_PROBE_KIND_ICMP, "?", true` | 同 Step 3 | 1，`gone` 那项为 `{1 ?}` |
| c | `seriesLabel` 的返回式改成下面"c 的替换代码"（种类照旧、丢掉目标；UNSPECIFIED 仍退回编号，不让 `kindLabel` 在未知种类上抛异常） | 同 Step 7 | 1，`probes.test.ts` 红在 `expected [ 'ICMP #7', 'ICMP #3', '任务 #9' ]`；`NodeDetail.test.tsx` 红在找不到 `ICMP 1.1.1.1 #7` |
| d | `seriesLabels` 直接返回 `series.map(seriesLabel)`（不消歧） | 同 Step 7 | 1，"同窗口同名任务"用例红在 `#7` 缺失 |
| e | `NodeDetail.tsx` 的 `probes` 查询去掉 `placeholderData: keepPreviousData` | 同 Step 7 | 1，"切窗请求挂起时保留探测图与图例"红在切窗后找不到 `ICMP 1.1.1.1`：挂起期间探测图被卸掉 |

c 的替换代码：

```ts
  return s.kind === ProbeKind.UNSPECIFIED ? `任务 #${s.taskId}` : kindLabel(s.kind);
```

---

### Task 2: store：setting 表（schema v8）与存储统计

**Files:**
- Modify: `internal/hub/store/schema.go`（`ddlSetting`，`schemaStatements` 末尾追加；`metricTables` 的注释）
- Modify: `internal/hub/store/migrations.go`（迁移 8 与冻结的 `ddlSettingV8`）
- Modify: `internal/hub/store/store.go:178`（`schemaVersion = 8`）
- Create: `internal/hub/store/setting.go`、`internal/hub/store/setting_test.go`
- Create: `internal/hub/store/stats.go`、`internal/hub/store/stats_test.go`
- Modify: `internal/hub/store/node.go`（删除 `Counts`）
- Modify（`Counts` 的调用点改用 `rowCounts`）：
  - `internal/hub/store/store_test.go:322`
  - `internal/hub/store/probe_test.go:140,332,389,414`
  - `internal/hub/api/api_test.go:247,280,592`
- Delete：`internal/hub/store/alert_test.go` 里的 `TestCountsIncludesAlertTables`。它的性质由 `TestStorageStatsCoversEveryTableAndCountsRows` 以更强的形式覆盖。
- Modify: `cmd/hub/stats.go`、`cmd/hub/main.go:57`、`cmd/hub/offline_test.go:38`
- Create: `cmd/hub/stats_test.go`

**Interfaces:**
- Produces（`internal/hub/store`）：
  - `type SiteSettings struct { Title, Theme, AccentColor, Logo, CustomCSS string }`
  - `const DefaultTheme = "auto"`
  - `func (s *Store) SiteSettings(ctx context.Context) (SiteSettings, error)`
  - `func (s *Store) SaveSiteSettings(ctx context.Context, st SiteSettings) error`
  - `type TableRows struct { Name string; Rows int64 }`
  - `type StorageStats struct { DBBytes int64; Tables []TableRows }`（`Tables` 按名升序）
  - `func (s *Store) StorageStats(ctx context.Context) (StorageStats, error)`
- Produces（测试辅助）：store 包内 `rowCounts(t testing.TB, s *Store) map[string]int64`；api 包内 `rowCounts(t *testing.T, st *store.Store) map[string]int64`。
- Produces（cmd/hub）：`func runStatsWith(args []string, out io.Writer) error`。
- Removes：`func (s *Store) Counts(ctx) (map[string]int64, error)`。

- [ ] **Step 1: 写失败测试**

`internal/hub/store/stats_test.go`：

```go
package store

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"testing"
	"time"

	"github.com/xjetry/probe/internal/clock"
)

// rowCounts 是测试里按表名取行数的写法；来源与 GetStorageStats、probe-hub stats 相同。
func rowCounts(t testing.TB, s *Store) map[string]int64 {
	t.Helper()
	stats, err := s.StorageStats(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]int64{}
	for _, tr := range stats.Tables {
		out[tr.Name] = tr.Rows
	}
	return out
}

func openAt(t *testing.T) (*Store, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "t.db")
	s, err := Open(path, clock.NewFake(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)), slog.Default())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s, path
}

// 统计恰好覆盖 schemaStatements 建出的每一张表，不多不少，行数与表一一对应。新库的 sqlite_master 就是执行这些
// 语句建出来的，所以按它列表的实现自然满足；统计若改成手写的表名清单，以后新增表而漏改清单时这个用例会红。
func TestStorageStatsCoversEveryTableAndCountsRows(t *testing.T) {
	s, _ := openAt(t)
	for i := range 3 {
		if _, err := s.CreateNode(t.Context(), fmt.Sprint("n", i), hash(byte(i))); err != nil {
			t.Fatal(err)
		}
	}
	stats, err := s.StorageStats(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, tr := range stats.Tables {
		names = append(names, tr.Name)
	}
	var want []string
	create := regexp.MustCompile(`^CREATE TABLE (\w+)`)
	for _, stmt := range schemaStatements() {
		if m := create.FindStringSubmatch(stmt); m != nil {
			want = append(want, m[1])
		}
	}
	slices.Sort(want)
	if !slices.Equal(names, want) {
		t.Fatalf("tables = %v, want %v", names, want)
	}
	rows := rowCounts(t, s)
	if rows["node"] != 3 || rows["setting"] != 0 || rows["probe_meta"] != 1 || rows["api_token"] != 0 {
		t.Fatalf("rows = %v", rows)
	}
}

// db_bytes 是逻辑大小 page_count × page_size：WAL 检查点之前主文件可能远小于它，之后二者相等。
func TestStorageStatsReportsLogicalDatabaseSize(t *testing.T) {
	s, path := openAt(t)
	for i := range 200 {
		if _, err := s.CreateNode(t.Context(), "n", hash(byte(i))); err != nil {
			t.Fatal(err)
		}
	}
	stats, err := s.StorageStats(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.w.Exec("PRAGMA wal_checkpoint(TRUNCATE)"); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if stats.DBBytes <= 4096 || stats.DBBytes != info.Size() {
		t.Fatalf("db_bytes = %d, main file after checkpoint = %d", stats.DBBytes, info.Size())
	}
}
```

`internal/hub/store/setting_test.go`：

```go
package store

import (
	"reflect"
	"slices"
	"testing"
)

func TestSiteSettingsDefaultAndWholeReplacement(t *testing.T) {
	s, _ := open(t)
	got, err := s.SiteSettings(t.Context())
	if err != nil || got != (SiteSettings{Theme: DefaultTheme}) {
		t.Fatalf("never saved: %+v %v", got, err)
	}
	full := SiteSettings{Title: "状态", Theme: "dark", AccentColor: "#112233", Logo: "data:image/png;base64,AAAA", CustomCSS: "body{}"}
	if err := s.SaveSiteSettings(t.Context(), full); err != nil {
		t.Fatal(err)
	}
	if got, err := s.SiteSettings(t.Context()); err != nil || got != full {
		t.Fatalf("round trip: %+v %v", got, err)
	}
	// 整体替换：空串写入，表示该项回到默认，不是"不改"。
	if err := s.SaveSiteSettings(t.Context(), SiteSettings{Theme: "auto"}); err != nil {
		t.Fatal(err)
	}
	if got, err := s.SiteSettings(t.Context()); err != nil || got != (SiteSettings{Theme: "auto"}) {
		t.Fatalf("replacement kept old values: %+v %v", got, err)
	}
	if n := rowCounts(t, s)["setting"]; n != 5 {
		t.Fatalf("setting rows = %d, want 5", n)
	}
}

// v7 的完整 DDL：v6 加上 migrateDeliveryFailure 的两条 ALTER。
var schemaV7 = append(slices.Clone(schemaV6),
	"ALTER TABLE alert_delivery ADD COLUMN failure TEXT NOT NULL DEFAULT ''",
	"ALTER TABLE alert_delivery ADD COLUMN http_status INTEGER")

func TestMigrationFromV7MatchesFreshSchemaAndKeepsRows(t *testing.T) {
	migrated, fresh := migrateFrom(t, schemaV7, 7, seedMinuteRow)
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
	if st, err := migrated.SiteSettings(t.Context()); err != nil || st != (SiteSettings{Theme: DefaultTheme}) {
		t.Fatalf("settings after migration: %+v %v", st, err)
	}
}
```

`cmd/hub/stats_test.go`：

```go
package main

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
)

// 统计的表清单来自库本身：原先手写清单漏掉的 api_token、probe_meta、setting 都在；
// 关库后主文件已检查点，db_bytes 等于它的大小。
func TestStatsPrintsSizeAndEveryTable(t *testing.T) {
	db := filepath.Join(t.TempDir(), "hub.db")
	st, _, err := openOffline(db, true)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.CreateNode(context.Background(), "n", make([]byte, 32)); err != nil {
		t.Fatal(err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	var out strings.Builder
	if err := runStatsWith([]string{"--db", db}, &out); err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSuffix(out.String(), "\n"), "\n")
	size, ok := strings.CutPrefix(lines[0], "db_bytes: ")
	info, statErr := os.Stat(db)
	if !ok || statErr != nil || size != strconv.FormatInt(info.Size(), 10) {
		t.Fatalf("first line %q, file size %v (%v)", lines[0], info, statErr)
	}
	tables := lines[1:]
	if !slices.IsSorted(tables) {
		t.Fatalf("tables not sorted: %v", tables)
	}
	for _, want := range []string{"api_token: 0", "node: 1", "probe_meta: 1", "setting: 0"} {
		if !slices.Contains(tables, want) {
			t.Errorf("stats lacks %q: %v", want, tables)
		}
	}
}
```

- [ ] **Step 2: 跑红**

```bash
cd /Users/xjetry/work/vibe/probe-public && go test -count=1 ./internal/hub/store/ ./cmd/hub/ > /tmp/m5-t2-red.log 2>&1; echo $?
```

Expected：1。编译失败：`s.StorageStats`、`SiteSettings`、`runStatsWith` 未定义。

- [ ] **Step 3: 实现**

`internal/hub/store/schema.go`，放在 `ddlAPIToken` 之后：

```go
// setting 是全站设置的键值表（公开页外观等）。值可达 128 KiB（logo 的 data: URL），不用 WITHOUT ROWID：
// 那种表把整行放进主键 B 树，SQLite 文档建议其行不超过页大小的约 1/20。
const ddlSetting = `CREATE TABLE setting (
  key TEXT PRIMARY KEY,
  value TEXT NOT NULL
)`
```

`schemaStatements` 的最后一行改为：

```go
	return append(append(out, alertStatements()...), ddlAPIToken, ddlSetting)
```

`metricTables` 上方的注释说它与 `Counts` 共用，`Counts` 删除之后这句就错了。改为：

```go
// metricTables 按级别从细到粗；建库与 DeleteNode 共用此清单，避免新增级别后遗漏删除；
// 已有库仍需对应的增量迁移。存储统计按 sqlite_master 列表，不读它。
```

`internal/hub/store/migrations.go`：
- `migrations` 表加 `8: execAll([]string{ddlSettingV8}),`。
- 文件末尾（`migrateDeliveryFailure` 之后）加：

```go
// v8：全站设置的键值表。
const ddlSettingV8 = `CREATE TABLE setting (
  key TEXT PRIMARY KEY,
  value TEXT NOT NULL
)`
```

`internal/hub/store/store.go:178` 改为 `const schemaVersion = 8`。

`internal/hub/store/setting.go`：

```go
package store

import (
	"context"
	"database/sql"
)

// SiteSettings 是公开页的外观，五项整体读写。存储不校验取值：约束由 api 的 UpdateSettings 裁决，
// 这里只维持"整体替换"——空串照样写入，表示该项回到默认，不存在"不改"。
type SiteSettings struct {
	Title       string
	Theme       string
	AccentColor string
	Logo        string
	CustomCSS   string
}

// DefaultTheme 是从未保存过外观时的明暗：跟随访客系统。
const DefaultTheme = "auto"

type settingField struct {
	key   string
	value *string
}

// 键名是库里的持久标识，改名要迁移。setting 表还会放别的设置，外观只占 site.* 这五个键。
func (st *SiteSettings) fields() []settingField {
	return []settingField{
		{"site.title", &st.Title},
		{"site.theme", &st.Theme},
		{"site.accent_color", &st.AccentColor},
		{"site.logo", &st.Logo},
		{"site.custom_css", &st.CustomCSS},
	}
}

func (s *Store) SiteSettings(ctx context.Context) (SiteSettings, error) {
	out := SiteSettings{Theme: DefaultTheme}
	byKey := map[string]*string{}
	for _, f := range out.fields() {
		byKey[f.key] = f.value
	}
	rows, err := s.r.QueryContext(ctx, "SELECT key, value FROM setting WHERE key GLOB 'site.*'")
	if err != nil {
		return SiteSettings{}, err
	}
	defer rows.Close()
	for rows.Next() {
		var k, v string
		if err := rows.Scan(&k, &v); err != nil {
			return SiteSettings{}, err
		}
		if p := byKey[k]; p != nil {
			*p = v
		}
	}
	return out, rows.Err()
}

// SaveSiteSettings 在一个事务里写五个键：读侧不会看到新旧混合的外观。
func (s *Store) SaveSiteSettings(ctx context.Context, st SiteSettings) error {
	return s.write(ctx, func(tx *sql.Tx) error {
		for _, f := range st.fields() {
			if _, err := tx.Exec("INSERT INTO setting (key, value) VALUES (?, ?) ON CONFLICT (key) DO UPDATE SET value = excluded.value", f.key, *f.value); err != nil {
				return err
			}
		}
		return nil
	})
}
```

`internal/hub/store/stats.go`：

```go
package store

import (
	"context"
	"database/sql"
	"errors"
	"strings"
)

type TableRows struct {
	Name string
	Rows int64
}

// StorageStats 是库的规模：DBBytes 为 page_count × page_size，即数据库的逻辑大小，等于 WAL 检查点之后
// 主文件的大小（检查点之前主文件可能远小于它）；不含 -wal 与 -shm 文件。Tables 按表名升序。
type StorageStats struct {
	DBBytes int64
	Tables  []TableRows
}

// StorageStats 在一个只读事务里读出，行数与大小属于同一快照。表名取自 sqlite_master 而不是手写清单：
// 新增的表自动计入。名字以 sqlite_ 开头的是 SQLite 内部表（如 AUTOINCREMENT 的 sqlite_sequence），不计；
// 前缀按字面比较，不用 LIKE（它的 _ 是通配符，且对 ASCII 不分大小写）。
func (s *Store) StorageStats(ctx context.Context) (StorageStats, error) {
	tx, err := s.r.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return StorageStats{}, err
	}
	defer tx.Rollback()
	var names []string
	rows, err := tx.QueryContext(ctx, "SELECT name FROM sqlite_master WHERE type = 'table' AND substr(name, 1, 7) <> 'sqlite_' ORDER BY name")
	if err != nil {
		return StorageStats{}, err
	}
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			rows.Close()
			return StorageStats{}, err
		}
		names = append(names, n)
	}
	if err := errors.Join(rows.Err(), rows.Close()); err != nil {
		return StorageStats{}, err
	}
	var out StorageStats
	for _, n := range names {
		var c int64
		// 表名来自 sqlite_master，按 SQL 标识符规则加双引号并转义内部的双引号。
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM "`+strings.ReplaceAll(n, `"`, `""`)+`"`).Scan(&c); err != nil {
			return StorageStats{}, err
		}
		out.Tables = append(out.Tables, TableRows{Name: n, Rows: c})
	}
	var pages, pageSize int64
	if err := tx.QueryRowContext(ctx, "PRAGMA page_count").Scan(&pages); err != nil {
		return StorageStats{}, err
	}
	if err := tx.QueryRowContext(ctx, "PRAGMA page_size").Scan(&pageSize); err != nil {
		return StorageStats{}, err
	}
	out.DBBytes = pages * pageSize
	return out, nil
}
```

`internal/hub/store/node.go`：删除 `Counts` 整个函数。

调用点：
- `probe_test.go:140`、`:332`、`:389`：把 `counts, err := s.Counts(ctx)` 连同紧随的 `if err != nil { t.Fatal(err) }` 换成 `counts := rowCounts(t, s)`。
- `probe_test.go:414`：换成：

```go
	counts := rowCounts(t, s)
	if counts["metric_1m"] != 0 || counts["probe_1m"] != 0 {
		t.Fatalf("partial batch committed: counts=%v", counts)
	}
```

- `store_test.go:322`：换成：

```go
	if n := rowCounts(t, s)["traffic"]; n != 1 {
		t.Fatalf("traffic count before delete = %d, want 1", n)
	}
```

- `alert_test.go`：删除 `TestCountsIncludesAlertTables`。
- `internal/hub/api/api_test.go`：
  - 在 `codeOf` 之后加：

```go
// rowCounts 按表名取行数，来源与 GetStorageStats、probe-hub stats 相同。
func rowCounts(t *testing.T, st *store.Store) map[string]int64 {
	t.Helper()
	stats, err := st.StorageStats(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]int64{}
	for _, tr := range stats.Tables {
		out[tr.Name] = tr.Rows
	}
	return out
}
```

  - 第 247、280 行的 `before, err := h.store.Counts(t.Context())` 与 `after, err := ...` 各自连同 err 判断，换成 `before := rowCounts(t, h.store)`、`after := rowCounts(t, h.store)`。
  - 第 592 行换成 `counts := rowCounts(t, h.store)`，删去其后的 err 判断。

`cmd/hub/stats.go` 整份替换：

```go
package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
)

func runStats(args []string) error { return runStatsWith(args, os.Stdout) }

// runStatsWith 打印库的逻辑大小与每张表的行数；数据来自 store.StorageStats，与 AdminService.GetStorageStats 同源。
func runStatsWith(args []string, out io.Writer) error {
	fs := flag.NewFlagSet("stats", flag.ContinueOnError)
	db := fs.String("db", "probe.db", "SQLite database path")
	if err := fs.Parse(args); err != nil {
		return err
	}
	st, _, err := openOffline(*db, false)
	if err != nil {
		return err
	}
	defer st.Close()
	stats, err := st.StorageStats(context.Background())
	if err != nil {
		return err
	}
	fmt.Fprintf(out, "db_bytes: %d\n", stats.DBBytes)
	for _, t := range stats.Tables {
		fmt.Fprintf(out, "%s: %d\n", t.Name, t.Rows)
	}
	return nil
}
```

`cmd/hub/main.go:57` 改为 `  stats                     database size (page_count × page_size) and row counts per table`，与上下行的列对齐。

`cmd/hub/offline_test.go:38` 改为 `{"stats", func(db string) error { return runStatsWith([]string{"--db", db}, io.Discard) }},`。

- [ ] **Step 4: 跑绿**

```bash
cd /Users/xjetry/work/vibe/probe-public && go test -count=1 ./internal/hub/store/ ./internal/hub/api/ ./cmd/hub/ > /tmp/m5-t2-green.log 2>&1; echo $?
cd /Users/xjetry/work/vibe/probe-public && go vet ./... > /tmp/m5-t2-vet.log 2>&1; echo $?
```

Expected：两条都是 0。以下测试照常通过：
- `TestMigrationsReferenceOnlyFrozenDDL`：`migrations.go` 只引用 `ddlSettingV8`，没有引用 `ddlSetting`。
- 各版本的迁移测试：从 v1–v7 迁到 v8 都与新建库一致。

- [ ] **Step 5: 提交**

```bash
cd /Users/xjetry/work/vibe/probe-public && git add internal/hub/store internal/hub/api/api_test.go cmd/hub && git commit -m "store: setting 键值表（schema v8）与按 sqlite_master 计算的存储统计" -m "外观设置五项整体读写，空串表示回到默认。存储统计替换手写表名的 Counts：原清单漏了 api_token、probe_meta、rollup_state，现在表名取自 sqlite_master，行数与大小在同一个只读事务里读出。db_bytes 取 page_count × page_size：WAL 模式下主文件在检查点之前可能远小于数据库（实测 200 个节点时 4096 对 196608），检查点之后二者相等。probe-hub stats 改用同一来源，首行输出 db_bytes。" > /tmp/m5-t2-commit.log 2>&1; echo $?
```

Expected：0。

- [ ] **Step 6: 缺陷注入**

| | 改动 | 命令 | 期望 |
|---|---|---|---|
| a | `migrations.go` 的迁移 8 改成 `execAll([]string{ddlSetting})` | `go test -count=1 -run TestMigrationsReferenceOnlyFrozenDDL ./internal/hub/store/ > /tmp/m5-t2-inj-a.log 2>&1; echo $?` | 1，`migrations reference current DDL ddlSetting` |
| b | 删掉迁移 8 那一行 | `go test -count=1 -run TestMigrationFromV7 ./internal/hub/store/ > /tmp/m5-t2-inj-b.log 2>&1; echo $?` | 1，`Open` 失败于 `no migration to schema version 8` |
| c | `StorageStats` 的 SQL 加 `AND name <> 'setting'` | `go test -count=1 -run TestStorageStatsCovers ./internal/hub/store/ > /tmp/m5-t2-inj-c.log 2>&1; echo $?` | 1，`tables = … want …` 缺 `setting` |
| d | `out.DBBytes = pages`（漏乘页大小） | `go test -count=1 -run 'TestStorageStatsReportsLogical\|TestStatsPrints' ./internal/hub/store/ ./cmd/hub/ > /tmp/m5-t2-inj-d.log 2>&1; echo $?` | 1，store 红在 `db_bytes = 50, main file after checkpoint = 204800` 之类；CLI 红在 `first line "db_bytes: 45", file size …`（首行不等于检查点之后的文件大小） |
| e | `SaveSiteSettings` 循环里对 `*f.value == ""` 的项 `continue` | `go test -count=1 -run TestSiteSettingsDefault ./internal/hub/store/ > /tmp/m5-t2-inj-e.log 2>&1; echo $?` | 1，`replacement kept old values` |
| f | `runStatsWith` 跳过行数为 0 的表 | `go test -count=1 -run TestStatsPrints ./cmd/hub/ > /tmp/m5-t2-inj-f.log 2>&1; echo $?` | 1，`stats lacks "api_token: 0"` |
| g | `SiteSettings` 里 `out := SiteSettings{Theme: DefaultTheme}` 改成 `out := SiteSettings{}` | `go test -count=1 -run 'TestSiteSettings\|TestMigrationFromV7' ./internal/hub/store/ > /tmp/m5-t2-inj-g.log 2>&1; echo $?` | 1，`never saved: {Title: Theme: …}` 与 `settings after migration: {… Theme: …}` |
| h | `fields()` 里 `{"site.logo", &st.Logo}` 改成 `{"site.logo", &st.CustomCSS}` | `go test -count=1 -run TestSiteSettingsDefault ./internal/hub/store/ > /tmp/m5-t2-inj-h.log 2>&1; echo $?` | 1，`round trip: {… Logo: CustomCSS:body{}}` |
| i | `SaveSiteSettings` 先 `DELETE FROM setting WHERE key GLOB 'site.*'`，循环里只写非空的值 | `go test -count=1 -run TestSiteSettingsDefault ./internal/hub/store/ > /tmp/m5-t2-inj-i.log 2>&1; echo $?` | 1，`setting rows = 1, want 5`：读侧看起来一样，但"空串照样写入"的约定破了 |
| j | 计数的 SQL 里 `strings.ReplaceAll(n, …)` 改成 `strings.ReplaceAll("node", …)`（每张表都数 node） | `go test -count=1 -run TestStorageStatsCovers ./internal/hub/store/ > /tmp/m5-t2-inj-j.log 2>&1; echo $?` | 1，`rows = map[admin:3 admin_session:3 …]` |
| k | 表名查询的 `ORDER BY name` 改成 `ORDER BY rowid`（建表顺序） | `go test -count=1 -run 'TestStorageStatsCovers\|TestStatsPrints' ./internal/hub/store/ ./cmd/hub/ > /tmp/m5-t2-inj-k.log 2>&1; echo $?` | 1，store 红在 `tables = [node node_facts …]`；CLI 红在 `tables not sorted` |
| l | 表名查询去掉 `AND substr(name, 1, 7) <> 'sqlite_'` | `go test -count=1 -run TestStorageStatsCovers ./internal/hub/store/ > /tmp/m5-t2-inj-l.log 2>&1; echo $?` | 1，`tables = […]` 多出 `sqlite_sequence` |

每项：改动 → `git diff --stat` 非空 → 跑命令 → 核对原因 → `git checkout -- <文件>`。

---

### Task 3: AdminService 的外观设置与存储统计

**Files:**
- Modify: `proto/probe/v1/admin.proto`（三个 rpc，放在 `TestNotifyChannel` 与 `ListApiTokens` 之间；消息接在 `message TestNotifyChannelResponse {}` 之后；`Node.public` 加注释）
- Generated（`make gen`）：`gen/probe/v1/admin.pb.go`、`gen/probe/v1/probev1connect/admin.connect.go`、`web/src/gen/probe/v1/admin_pb.ts`
- Create: `internal/hub/api/settings.go`、`internal/hub/api/settings_test.go`
- Modify: `internal/hub/api/service.go:32-36`（`maxBody`）
- Modify: `internal/hub/api/access_test.go:16-20`（`readMethods`）
- Modify: `internal/hub/api/api_test.go:637`（超限请求用 `maxBody+1`）

**Interfaces:**
- Consumes：Task 2 的 `store.SiteSettings`、`(*store.Store).SiteSettings`、`SaveSiteSettings`、`StorageStats`，以及 api 包测试辅助 `rowCounts`。
- Produces（proto）：
  - `AdminService.GetSettings`（READ）、`UpdateSettings`（SESSION）、`GetStorageStats`（READ）
  - 消息 `Settings{title, theme, accent_color, logo, custom_css}`、`GetSettingsResponse{settings}`、`UpdateSettingsRequest{settings}`、`UpdateSettingsResponse{settings}`、`GetStorageStatsResponse{db_bytes, tables}`、`TableRows{name, rows}`
- Produces（Go，`internal/hub/api/settings.go`）：
  - 常量 `maxTitleRunes = 64`、`maxTitleBytes = 1 << 10`、`maxLogoBytes = 128 << 10`、`maxCSSBytes = 64 << 10`
  - 变量 `themes = []string{"auto", "light", "dark"}`、`logoTypes = []string{"image/png", "image/jpeg", "image/webp", "image/svg+xml"}`
  - `func cleanSettings(in *probev1.Settings) (store.SiteSettings, error)`
  - `func settingsProto(st store.SiteSettings) *probev1.Settings`

  Task 11 的 `appearanceLimits.test.ts` 按上面的名字与写法解析本文件。
- Produces（Go）：`maxBody = maxLogoBytes + 6*maxCSSBytes + 6*maxTitleBytes + 4<<10`。

- [ ] **Step 1: proto 与失败测试**

`admin.proto` 中，在 `TestNotifyChannel` 的 rpc 之后插入：

```proto
  // 公开页外观：标题、明暗、主色、logo 与自定义 CSS，经 PublicService.GetSite 对外下发。
  rpc GetSettings(GetSettingsRequest) returns (GetSettingsResponse) {
    option (probe.v1.access) = ACCESS_READ;
  }
  // 整体替换外观的五项并回显 hub 实际保存的值。任一项不合约束即 InvalidArgument，错误写明字段、
  // 约束与期望取值，什么都不写入。
  rpc UpdateSettings(UpdateSettingsRequest) returns (UpdateSettingsResponse) {
    option (probe.v1.access) = ACCESS_SESSION;
  }
  // 库的逻辑大小与每张表的行数，与 probe-hub stats 同一来源。
  rpc GetStorageStats(GetStorageStatsRequest) returns (GetStorageStatsResponse) {
    option (probe.v1.access) = ACCESS_READ;
  }
```

`message TestNotifyChannelResponse {}` 之后插入：

```proto

// 公开页外观。整体替换：UpdateSettings 写入全部五项，没有"不改"的取值。
message Settings {
  // 页面标题：清洗前最多 1024 字节，去掉控制字符与首尾空白之后最多 64 个字符，hub 保存去掉之后的值；
  // 空串表示公开页用内置标题。
  string title = 1;
  // auto（跟随访客的系统设置）、light 或 dark。
  string theme = 2;
  // 主色 #rrggbb，hub 保存为小写；空串表示内置配色。
  string accent_color = 3;
  // data:<type>;base64,<data>：type 为 image/png、image/jpeg、image/webp、image/svg+xml 之一（全小写、不带参数），
  // data 为带填充的标准 base64，整串不超过 131072 字节；空串表示没有 logo。
  string logo = 4;
  // 追加在公开页内置样式之后的 CSS，不超过 65536 字节，不得含 "</"。只接受 CSS；要改页面结构用 --public-dir。
  string custom_css = 5;
}
message GetSettingsRequest {}
message GetSettingsResponse { Settings settings = 1; }
message UpdateSettingsRequest { Settings settings = 1; }
// hub 实际保存的值：标题已清洗，主色已转小写。
message UpdateSettingsResponse { Settings settings = 1; }

message GetStorageStatsRequest {}
message GetStorageStatsResponse {
  // page_count × page_size：数据库的逻辑大小，等于 WAL 检查点之后主文件的大小；不含 -wal 与 -shm 文件。
  uint64 db_bytes = 1;
  // 每张表一项，按表名升序，含 hub 内部的簿记表。
  repeated TableRows tables = 2;
}
message TableRows {
  string name = 1;
  uint64 rows = 2;
}
```

`message Node` 的 `bool public = 3;` 上方加一行注释：`// 公开节点出现在 PublicService：实时状态、历史指标与探测（含任务的种类与目标）对匿名访客可见。`

然后执行：

```bash
cd /Users/xjetry/work/vibe/probe-public && make gen > /tmp/m5-t3-gen.log 2>&1; echo $?
cd /Users/xjetry/work/vibe/probe-public && buf lint > /tmp/m5-t3-lint.log 2>&1; echo $?
```

Expected：两条都是 0。

`internal/hub/api/access_test.go` 的 `readMethods` 加上 `"GetSettings", "GetStorageStats"`：

```go
var readMethods = []string{
	"ListNodes", "GetRegisterWindow", "GetSnapshot", "QueryMetrics", "GetTraffic",
	"ListProbeTasks", "QueryProbes", "ListAlertRules", "ListAlertEvents",
	"GetSettings", "GetStorageStats", "GetApiReference",
}
```

`internal/hub/api/api_test.go:637` 改为 `strings.Repeat("x", maxBody+1)`。

`internal/hub/api/settings_test.go`：

```go
package api

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strings"
	"testing"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/proto"

	probev1 "github.com/xjetry/probe/gen/probe/v1"
)

func validSettings() *probev1.Settings {
	return &probev1.Settings{Title: "状态", Theme: "dark", AccentColor: "#112233", Logo: "data:image/png;base64,iVBORw0KGgo=", CustomCss: "body { color: red }"}
}

func withSettings(change func(*probev1.Settings)) *probev1.Settings {
	s := validSettings()
	change(s)
	return s
}

func saveSettings(t *testing.T, h *harness, in *probev1.Settings) *probev1.Settings {
	t.Helper()
	resp, err := h.admin.UpdateSettings(t.Context(), connect.NewRequest(&probev1.UpdateSettingsRequest{Settings: in}))
	if err != nil {
		t.Fatalf("UpdateSettings(%v): %v", in, err)
	}
	return resp.Msg.GetSettings()
}

func currentSettings(t *testing.T, h *harness) *probev1.Settings {
	t.Helper()
	resp, err := h.admin.GetSettings(t.Context(), connect.NewRequest(&probev1.GetSettingsRequest{}))
	if err != nil {
		t.Fatal(err)
	}
	return resp.Msg.GetSettings()
}

// rejected 断言更新被拒、错误含 want，且库里的外观仍是 before：一项不合约束，整次更新什么都不写。
func rejected(t *testing.T, h *harness, in *probev1.Settings, want string, before *probev1.Settings) {
	t.Helper()
	_, err := h.admin.UpdateSettings(t.Context(), connect.NewRequest(&probev1.UpdateSettingsRequest{Settings: in}))
	if codeOf(err) != connect.CodeInvalidArgument || !strings.Contains(err.Error(), want) {
		t.Fatalf("err = %v, want InvalidArgument containing %q", err, want)
	}
	if got := currentSettings(t, h); !proto.Equal(got, before) {
		t.Fatalf("rejected update changed settings to %v", got)
	}
}

func TestUpdateSettingsValidatesTitleThemeAndAccent(t *testing.T) {
	h := newHarness(t, "")
	h.login(t)
	before := saveSettings(t, h, validSettings())
	for _, c := range []struct {
		name string
		in   *probev1.Settings
		want string
	}{
		{"title", withSettings(func(s *probev1.Settings) { s.Title = strings.Repeat("字", 65) }), "settings.title must be at most 64 characters after removing control characters and surrounding whitespace; got 65"},
		{"title raw bytes", withSettings(func(s *probev1.Settings) { s.Title = strings.Repeat(" ", maxTitleBytes) + "a" }), "settings.title must be at most 1024 bytes before cleaning; got 1025"},
		{"theme empty", withSettings(func(s *probev1.Settings) { s.Theme = "" }), `settings.theme must be one of auto, light, dark; got ""`},
		{"theme case", withSettings(func(s *probev1.Settings) { s.Theme = "Dark" }), `settings.theme must be one of auto, light, dark; got "Dark"`},
		{"settings missing", nil, `settings.theme must be one of auto, light, dark; got ""`},
		{"accent short", withSettings(func(s *probev1.Settings) { s.AccentColor = "#12345" }), `settings.accent_color must be empty (the default color) or #rrggbb with six hex digits; got "#12345"`},
		{"accent long", withSettings(func(s *probev1.Settings) { s.AccentColor = "#1234567" }), `got "#1234567"`},
		{"accent without hash", withSettings(func(s *probev1.Settings) { s.AccentColor = "123456" }), `got "123456"`},
		{"accent not hex", withSettings(func(s *probev1.Settings) { s.AccentColor = "#gggggg" }), `got "#gggggg"`},
	} {
		t.Run(c.name, func(t *testing.T) { rejected(t, h, c.in, c.want, before) })
	}
}

func TestUpdateSettingsCleansTitleAndAccentAndEchoes(t *testing.T) {
	h := newHarness(t, "")
	h.login(t)
	want := &probev1.Settings{Title: "运行状态", Theme: "light", AccentColor: "#abcdef"}
	if got := saveSettings(t, h, &probev1.Settings{Title: " ‮\x07运行状态 \t", Theme: "light", AccentColor: "#AbCdEf"}); !proto.Equal(got, want) {
		t.Fatalf("echo = %v, want %v", got, want)
	}
	if got := currentSettings(t, h); !proto.Equal(got, want) {
		t.Fatalf("stored = %v, want %v", got, want)
	}
	// 控制字符不计入 64 个字符；清洗前的字节上限恰好用满也照常保存。
	saveSettings(t, h, &probev1.Settings{Title: strings.Repeat("字", 64) + "\x00\x01", Theme: "auto"})
	if got := saveSettings(t, h, &probev1.Settings{Title: strings.Repeat(" ", maxTitleBytes-1) + "a", Theme: "auto"}); got.GetTitle() != "a" {
		t.Fatalf("title at the raw byte limit: echo %q, want \"a\"", got.GetTitle())
	}
}

func TestUpdateSettingsLogoAcceptsOnlyOneShape(t *testing.T) {
	h := newHarness(t, "")
	h.login(t)
	before := saveSettings(t, h, validSettings())
	shape := "settings.logo must be empty or data:<type>;base64,<data> with <type> one of image/png, image/jpeg, image/webp, image/svg+xml"
	data := "settings.logo: the data after ;base64, must be non-empty standard base64"
	for _, c := range []struct{ name, logo, want string }{
		{"scheme case", "DATA:image/png;base64,aGk=", shape},
		{"type case", "data:IMAGE/PNG;base64,aGk=", shape},
		{"parameter", "data:image/png;charset=utf-8;base64,aGk=", shape},
		{"not base64", "data:image/svg+xml,<svg/>", shape},
		{"not an image", "data:text/html;base64,PGgxPg==", shape},
		{"type prefix", "data:image/pngx;base64,aGk=", shape},
		{"javascript", "javascript:alert(1)", shape},
		{"leading space", " data:image/png;base64,aGk=", shape},
		{"empty data", "data:image/png;base64,", data},
		{"trailing newline", "data:image/png;base64,aGk=\n", data},
		{"inner newline", "data:image/png;base64,aG\nk=", data},
		{"url-safe alphabet", "data:image/png;base64,-_8=", data},
		{"unpadded", "data:image/png;base64,aGk", data},
		{"fragment", "data:image/png;base64,aGk=#x", data},
		{"too large", "data:image/png;base64," + strings.Repeat("A", 131052), "settings.logo must be at most 131072 bytes as a data: URL; got 131074"},
		// 大小检查排在形态检查之前，报哪一条就说明边界落在哪：恰好 131072 字节的值过了大小检查、错在 base64 长度，
		// 多一个字节就是大小错误。
		{"at the limit", "data:image/png;base64," + strings.Repeat("A", 131050), data},
		{"one byte over", "data:image/png;base64," + strings.Repeat("A", 131051), "settings.logo must be at most 131072 bytes as a data: URL; got 131073"},
	} {
		t.Run(c.name, func(t *testing.T) {
			rejected(t, h, withSettings(func(s *probev1.Settings) { s.Logo = c.logo }), c.want, before)
		})
	}
	for _, logo := range []string{
		"data:image/png;base64," + strings.Repeat("A", 131048), // 131070 字节：base64 长度须为 4 的倍数，这是不超过上限的最大值
		"data:image/jpeg;base64,/9j/4A==",
		"data:image/webp;base64,UklGRg==",
		"data:image/svg+xml;base64,PHN2Zy8+",
		"",
	} {
		if got := saveSettings(t, h, withSettings(func(s *probev1.Settings) { s.Logo = logo })); got.GetLogo() != logo {
			t.Fatalf("logo not stored verbatim: %.40q", got.GetLogo())
		}
	}
}

func TestUpdateSettingsCustomCSSRejectsOnlyLiteralEndTagOpen(t *testing.T) {
	h := newHarness(t, "")
	h.login(t)
	before := saveSettings(t, h, validSettings())
	for _, c := range []struct {
		css string
		at  int
	}{{"</style>", 0}, {"a{}</STYLE>", 3}, {"</ style>", 0}, {"a</b", 1}, {"</", 0}} {
		t.Run(c.css, func(t *testing.T) {
			want := fmt.Sprintf(`settings.custom_css must not contain "</" (it could end the page's <style> element); found at byte %d`, c.at)
			rejected(t, h, withSettings(func(s *probev1.Settings) { s.CustomCss = c.css }), want, before)
		})
	}
	rejected(t, h, withSettings(func(s *probev1.Settings) { s.CustomCss = strings.Repeat("a", 65537) }), "settings.custom_css must be at most 65536 bytes; got 65537", before)
	// 到不了 HTML 标记化器的写法：CSS 转义与 HTML 实体在 <style> 的 RAWTEXT 里都不被解码。
	for _, css := range []string{`a::before { content: "\3c/style>" }`, "/* &lt;/style> */", `a::after { content: "<\/style>" }`, "/* ＜/style> */", "a < /style {}", strings.Repeat("a", 65536)} {
		if got := saveSettings(t, h, withSettings(func(s *probev1.Settings) { s.CustomCss = css })); got.GetCustomCss() != css {
			t.Fatalf("css not stored verbatim: %.40q", got.GetCustomCss())
		}
	}
}

// 解码预算不够时，connect 在方法体之前就以 ResourceExhausted 拒绝，校验根本到不了。
// 解码预算装得下满额设置在最坏转义下的 JSON（service.go 的 maxBody 写了推导）：标题与 CSS 用控制字符填满，
// json.Marshal 把每个控制字符写成 6 字节的 \u00XX；标题的控制字符清洗后不计入 64 个字符，所以这仍是合法的设置。
func TestUpdateSettingsBudgetFitsFullSettingsWithWorstCaseEscaping(t *testing.T) {
	h := newHarness(t, "")
	h.login(t)
	body, err := json.Marshal(map[string]any{"settings": map[string]string{
		"title": strings.Repeat("\x01", maxTitleBytes), "theme": "auto", "accentColor": "#112233",
		"logo":      "data:image/png;base64," + strings.Repeat("A", 131048),
		"customCss": strings.Repeat("\x01", maxCSSBytes),
	}})
	if err != nil {
		t.Fatal(err)
	}
	if len(body) <= 131048+6*maxCSSBytes+6*maxTitleBytes {
		t.Fatalf("request is %d bytes; the worst case was not constructed", len(body))
	}
	req, err := http.NewRequest(http.MethodPost, h.srv.URL+"/probe.v1.AdminService/UpdateSettings", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := h.http.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("full settings escaped worst case (%d bytes): %d %s", len(body), resp.StatusCode, b)
	}
}

func TestGetStorageStatsMatchesTheStore(t *testing.T) {
	h := newHarness(t, "")
	h.login(t)
	h.createNode(t, "n")
	resp, err := h.admin.GetStorageStats(t.Context(), connect.NewRequest(&probev1.GetStorageStatsRequest{}))
	if err != nil {
		t.Fatal(err)
	}
	counts := rowCounts(t, h.store)
	var names []string
	for _, tr := range resp.Msg.GetTables() {
		names = append(names, tr.GetName())
		if int64(tr.GetRows()) != counts[tr.GetName()] {
			t.Errorf("%s: %d rows, store says %d", tr.GetName(), tr.GetRows(), counts[tr.GetName()])
		}
	}
	if resp.Msg.GetDbBytes() == 0 || len(names) != len(counts) || !slices.IsSorted(names) || counts["node"] != 1 {
		t.Fatalf("stats = %v, store = %v", resp.Msg, counts)
	}
}
```

- [ ] **Step 2: 跑红**

```bash
cd /Users/xjetry/work/vibe/probe-public && go test -count=1 ./internal/hub/api/ > /tmp/m5-t3-red.log 2>&1; echo $?
```

Expected：1。编译失败：`*Service` 未实现 `GetSettings`、`UpdateSettings`、`GetStorageStats`；`maxCSSBytes` 未定义。

- [ ] **Step 3: 实现**

`internal/hub/api/settings.go`：

```go
package api

import (
	"context"
	"encoding/base64"
	"regexp"
	"slices"
	"strings"
	"unicode/utf8"

	"connectrpc.com/connect"

	probev1 "github.com/xjetry/probe/gen/probe/v1"
	"github.com/xjetry/probe/internal/hub/sanitize"
	"github.com/xjetry/probe/internal/hub/store"
)

// 外观的上限（§10）。面板的 web/src/lib/appearance.ts 用同值做提交前提示，由 appearanceLimits.test.ts 对照本文件。
const (
	maxTitleRunes = 64
	// maxTitleBytes 限制清洗前的标题：清洗会去掉控制字符与首尾空白，只限清洗后的字符数，原始标题就没有上限，
	// 装不进解码预算的合法请求也就存在（maxBody 的推导要求每个字段都有字节上限）。
	maxTitleBytes = 1 << 10
	maxLogoBytes  = 128 << 10
	maxCSSBytes   = 64 << 10
)

var (
	themes    = []string{"auto", "light", "dark"}
	logoTypes = []string{"image/png", "image/jpeg", "image/webp", "image/svg+xml"}
	accentRE  = regexp.MustCompile(`^#[0-9a-fA-F]{6}$`)
)

// cleanSettings 校验并清洗外观，返回可以原样存储与下发的值；任一项不合约束即返回错误，调用方什么都不写。
// 标题会显示在页面与标签页上，按节点名同一口径清洗；logo 与 CSS 是数据与代码，改写任何字节都可能改变含义，只校验不清洗。
func cleanSettings(in *probev1.Settings) (store.SiteSettings, error) {
	if n := len(in.GetTitle()); n > maxTitleBytes {
		return store.SiteSettings{}, invalid("settings.title must be at most %d bytes before cleaning; got %d", maxTitleBytes, n)
	}
	title := strings.TrimSpace(sanitize.String(in.GetTitle(), len(in.GetTitle())))
	if n := utf8.RuneCountInString(title); n > maxTitleRunes {
		return store.SiteSettings{}, invalid("settings.title must be at most %d characters after removing control characters and surrounding whitespace; got %d", maxTitleRunes, n)
	}
	if !slices.Contains(themes, in.GetTheme()) {
		return store.SiteSettings{}, invalid("settings.theme must be one of %s; got %q", strings.Join(themes, ", "), in.GetTheme())
	}
	if c := in.GetAccentColor(); c != "" && !accentRE.MatchString(c) {
		return store.SiteSettings{}, invalid("settings.accent_color must be empty (the default color) or #rrggbb with six hex digits; got %q", c)
	}
	if err := checkLogo(in.GetLogo()); err != nil {
		return store.SiteSettings{}, err
	}
	if err := checkCSS(in.GetCustomCss()); err != nil {
		return store.SiteSettings{}, err
	}
	return store.SiteSettings{
		Title: title, Theme: in.GetTheme(), AccentColor: strings.ToLower(in.GetAccentColor()),
		Logo: in.GetLogo(), CustomCSS: in.GetCustomCss(),
	}, nil
}

// checkLogo 只接受 data:<type>;base64,<data> 这一种写法：type 在白名单内、全小写、不带参数，data 是带填充的
// 标准 base64。写法收窄到一种，"是不是白名单里的图片"就只有一个答案——宽松解析与浏览器的解析一旦不一致
// （参数、大小写、非 base64 形态），白名单就能被绕过。公开页只把它放进 <img src>，SVG 在 <img> 里不执行脚本。
func checkLogo(logo string) error {
	if logo == "" {
		return nil
	}
	if len(logo) > maxLogoBytes {
		return invalid("settings.logo must be at most %d bytes as a data: URL; got %d", maxLogoBytes, len(logo))
	}
	rest, ok := strings.CutPrefix(logo, "data:")
	mediaType, data, found := strings.Cut(rest, ";base64,")
	if !ok || !found || !slices.Contains(logoTypes, mediaType) {
		return invalid("settings.logo must be empty or data:<type>;base64,<data> with <type> one of %s; got a value starting with %q", strings.Join(logoTypes, ", "), head(logo))
	}
	const want = "settings.logo: the data after ;base64, must be non-empty standard base64 (A–Z, a–z, 0–9, + and /, padded with =)"
	// 标准库的解码器会跳过 \r 与 \n，先逐字节核对字母表，换行与空白都不算合法数据。
	if data == "" || strings.IndexFunc(data, func(r rune) bool { return !isBase64Char(r) }) >= 0 {
		return invalid("%s", want)
	}
	if _, err := base64.StdEncoding.Strict().DecodeString(data); err != nil {
		return invalid("%s: %v", want, err)
	}
	return nil
}

func isBase64Char(r rune) bool {
	return r >= 'A' && r <= 'Z' || r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '+' || r == '/' || r == '='
}

// head 是错误信息里回显的开头：到第一个逗号为止、最多 64 字节，不把整张图片写进错误。
func head(s string) string {
	if i := strings.IndexByte(s, ','); i >= 0 {
		s = s[:i+1]
	}
	if len(s) > 64 {
		s = s[:64]
	}
	return s
}

// checkCSS 拒绝字面的 "</"。公开页用 textContent 写进 <style>，不经 HTML 解析；但第三方主题与任何把它内联进
// HTML 的消费者都会让 "</style" 结束元素，所以约束放在唯一的写入口。只查字面序列就覆盖了全部大小写变体
// （"</" 本身不含字母）；CSS 转义（\3c/）与 HTML 实体（&lt;/）在 <style> 的 RAWTEXT 里都不被解码，结束不了元素。
func checkCSS(css string) error {
	if len(css) > maxCSSBytes {
		return invalid("settings.custom_css must be at most %d bytes; got %d", maxCSSBytes, len(css))
	}
	if i := strings.Index(css, "</"); i >= 0 {
		return invalid(`settings.custom_css must not contain "</" (it could end the page's <style> element); found at byte %d`, i)
	}
	return nil
}

func settingsProto(st store.SiteSettings) *probev1.Settings {
	return &probev1.Settings{Title: st.Title, Theme: st.Theme, AccentColor: st.AccentColor, Logo: st.Logo, CustomCss: st.CustomCSS}
}

func (s *Service) GetSettings(ctx context.Context, _ *connect.Request[probev1.GetSettingsRequest]) (*connect.Response[probev1.GetSettingsResponse], error) {
	st, err := s.store.SiteSettings(ctx)
	if err != nil {
		s.log.Error("reading settings failed", "err", err)
		return nil, internalError("reading settings failed")
	}
	return connect.NewResponse(&probev1.GetSettingsResponse{Settings: settingsProto(st)}), nil
}

func (s *Service) UpdateSettings(ctx context.Context, req *connect.Request[probev1.UpdateSettingsRequest]) (*connect.Response[probev1.UpdateSettingsResponse], error) {
	st, err := cleanSettings(req.Msg.GetSettings())
	if err != nil {
		return nil, err
	}
	if err := s.store.SaveSiteSettings(ctx, st); err != nil {
		s.log.Error("saving settings failed", "err", err)
		return nil, internalError("saving settings failed")
	}
	return connect.NewResponse(&probev1.UpdateSettingsResponse{Settings: settingsProto(st)}), nil
}

func (s *Service) GetStorageStats(ctx context.Context, _ *connect.Request[probev1.GetStorageStatsRequest]) (*connect.Response[probev1.GetStorageStatsResponse], error) {
	stats, err := s.store.StorageStats(ctx)
	if err != nil {
		s.log.Error("reading storage stats failed", "err", err)
		return nil, internalError("reading storage stats failed")
	}
	out := &probev1.GetStorageStatsResponse{DbBytes: uint64(stats.DBBytes)}
	for _, t := range stats.Tables {
		out.Tables = append(out.Tables, &probev1.TableRows{Name: t.Name, Rows: uint64(t.Rows)})
	}
	return connect.NewResponse(out), nil
}
```

`internal/hub/api/service.go` 的常量块改为：

```go
const (
	SessionCookie = "probe_session"
	// maxBody 是管理请求的解码预算。connect 先解码再进拦截器，未鉴权的请求也会被读到这个上限，所以它必须有界。
	// 它要装下 UpdateSettings 的满额设置在最坏转义下的 JSON：logo 满额（base64 字符在 JSON 里无需转义）；自定义 CSS
	// 与清洗前的标题满额，且每个字节都转义成 6 字节的 \u00XX（控制字符就是这样）；另留 4 KiB 给明暗、主色、字段名与
	// JSON 语法。多余的 JSON 空白、对无需转义的字符的转义不在预算内：这样的请求超出预算时得到 resource_exhausted。
	// 各项的上限在 settings.go；每个字段的合法取值都有字节上限（明暗与主色由取值集合与格式限定）是这条推导成立的前提。
	maxBody = maxLogoBytes + 6*maxCSSBytes + 6*maxTitleBytes + 4<<10
)
```

- [ ] **Step 4: 跑绿**

```bash
cd /Users/xjetry/work/vibe/probe-public && go test -count=1 ./internal/hub/api/ ./cmd/hub/ > /tmp/m5-t3-green.log 2>&1; echo $?
```

Expected：0。`TestAdminAccessTableMatchesDeclaredPolicy` 与 token 路径的枚举测试也通过：只读的两个方法 token 可调，`UpdateSettings` 返回 `PermissionDenied`。

- [ ] **Step 5: 提交**

```bash
cd /Users/xjetry/work/vibe/probe-public && git add proto gen web/src/gen internal/hub/api && git commit -m "api: 公开页外观的读写与存储统计" -m "UpdateSettings 整体替换五项并回显保存值，任一项不合约束即 InvalidArgument 且不写入，错误写明字段、约束与期望取值。logo 只接受 data:<type>;base64,<data> 一种写法：宽松解析与浏览器解析一旦不一致，白名单就能被绕过；标准库解码会跳过换行，所以先逐字节核对字母表。CSS 只拒绝字面的 </，它覆盖全部大小写变体，而 CSS 转义与 HTML 实体在 <style> 的 RAWTEXT 里不被解码。标题另限清洗前 1024 字节，每个字段的合法取值因此都有字节上限；管理请求的解码预算由这些上限推出（满额 logo，加满额 CSS 与标题的最坏 JSON 转义），原来的 64 KiB 装不下一张满额 logo。" > /tmp/m5-t3-commit.log 2>&1; echo $?
```

Expected：0。

- [ ] **Step 6: 缺陷注入**

| | 改动 | 命令 | 期望 |
|---|---|---|---|
| a | `checkLogo` 在查白名单之前，先用 `strings.Cut(mediaType, ";")` 把参数去掉 | `go test -count=1 -run TestUpdateSettingsLogo ./internal/hub/api/ > /tmp/m5-t3-inj-a.log 2>&1; echo $?` | 1，子用例 `parameter` 的 err 为 nil |
| b | 删掉字母表核对那个 `if` | 同 a | 1，`trailing newline`、`inner newline` 被接受 |
| c | `checkCSS` 改查 `strings.Index(css, "</style")` | `go test -count=1 -run TestUpdateSettingsCustomCSS ./internal/hub/api/ > /tmp/m5-t3-inj-c.log 2>&1; echo $?` | 1，`</STYLE>`、`</ style>`、`a</b`、`</` 各自被接受 |
| d | `maxBody` 改回 `64 << 10` | `go test -count=1 -run 'TestUpdateSettingsBudget\|TestUpdateSettingsLogo' ./internal/hub/api/ > /tmp/m5-t3-inj-d.log 2>&1; echo $?` | 1，满额请求得到 429（`resource_exhausted`，connect 的读取上限）；131070 字节的 png 同样被拒 |
| e | `UpdateSettings` 在 `cleanSettings` 之前先 `SaveSiteSettings` 原值 | `go test -count=1 -run TestUpdateSettingsValidates ./internal/hub/api/ > /tmp/m5-t3-inj-e.log 2>&1; echo $?` | 1，`rejected update changed settings` |
| f | `readMethods` 删去 `"GetStorageStats"` | `go test -count=1 -run TestAdminAccessTable ./internal/hub/api/ > /tmp/m5-t3-inj-f.log 2>&1; echo $?` | 1，`GetStorageStats: access ACCESS_READ, want ACCESS_SESSION` |
| g | `cleanSettings` 开头的清洗前字节检查改成 `if n := len(in.GetTitle()); false && n > maxTitleBytes {` | `go test -count=1 -run TestUpdateSettings ./internal/hub/api/ > /tmp/m5-t3-inj-g.log 2>&1; echo $?` | 1，子用例 `title raw bytes` 的 err 为 nil，库里的标题变成 `a` |
| h | `maxBody` 去掉 `6*maxTitleBytes` 一项 | 同 g | 1，`full settings escaped worst case (530519 bytes): 429 … message size 530519 is larger than configured max 528384` |
| i | `checkLogo` 的 `len(logo) > maxLogoBytes` 改成 `>=` | `go test -count=1 -run TestUpdateSettingsLogo ./internal/hub/api/ > /tmp/m5-t3-inj-i.log 2>&1; echo $?` | 1，子用例 `at the limit` 报的是大小错误 `got 131072`，不是 base64 错误 |
| j | 同一处改成 `len(logo) > maxLogoBytes+1` | 同 i | 1，子用例 `one byte over` 报的是 base64 错误，不是 `got 131073` |
| k | 回显的主色去掉 `strings.ToLower` | 同 g | 1，`echo = … accent_color:"#AbCdEf", want … "#abcdef"` |
| l | 标题计数 `utf8.RuneCountInString(title)` 改成 `len(title)`，并删去不再使用的 `unicode/utf8` import | 同 g | 1，子用例 `title` 报 `got 195`；64 个"字"加控制字符的标题被拒 |
| m | `accentRE` 去掉 `^` 与 `$` | 同 g | 1，子用例 `accent long` 的 err 为 nil，库里的主色变成 `#1234567` |
| n | 明暗改为 `slices.ContainsFunc(themes, func(t string) bool { return strings.EqualFold(t, in.GetTheme()) })` | 同 g | 1，子用例 `theme case` 的 err 为 nil，库里的明暗变成 `Dark` |
| o | `checkCSS` 的 `len(css) > maxCSSBytes` 改成 `>=` | 同 g | 1，满额 CSS 被拒：`settings.custom_css must be at most 65536 bytes; got 65536` |
| p | 标题不再经 `sanitize.String`（`title := strings.TrimSpace(in.GetTitle())`），并删去 `sanitize` import | 同 g | 1，`echo = title:"‮\x07运行状态" …`；满额用例的标题 1024 个控制字符按 1024 个字符计而被拒 |
| q | `GetStorageStats` 的循环跳过 `t.Rows == 0` 的表 | `go test -count=1 -run TestGetStorageStats ./internal/hub/api/ > /tmp/m5-t3-inj-q.log 2>&1; echo $?` | 1，`stats = … store = map[…]`：API 的表比 store 少 |

每项：改动 → `git diff --stat` 非空 → 跑命令 → 核对原因 → `git checkout -- <文件>`。

---

### Task 4: 令牌桶抽成 internal/hub/ratelimit，匿名入口的限流移到 connect 之外

**Files:**
- Create: `internal/hub/ratelimit/ratelimit.go`、`internal/hub/ratelimit/ratelimit_test.go`
- Create: `internal/hub/ratelimit/source.go`、`internal/hub/ratelimit/source_test.go`
- Delete: `internal/hub/ingest/limiter.go`
- Modify: `internal/hub/auth/proxy.go`（`SourceKey`、`DescribeSource`，紧挨 `ClientIP`）、`internal/hub/auth/failures.go`（锁定按来源键计）、`internal/hub/auth/admin.go`（`ErrLocked` 文案与 `Login` 注释）、`internal/hub/auth/auth.go`（`Register` 注释）
- Test: `internal/hub/auth/proxy_test.go`、`internal/hub/auth/admin_test.go`
- Modify: `internal/hub/api/service.go`（锁定的报错文案）
- Modify: `internal/hub/ingest/service.go`（字段类型、`New`、两处调用；`Handler` 挂上 `BySource`；拦截器与 `Register` 不再自算来源）
- Modify: `internal/hub/ingest/ingest_test.go`：
  - 测试辅助 `newHubWith`，配置在挂载前给定。
  - 用新的两个用例替换 `TestInterceptorRateLimitsAnonymousRegisterBeforeDispatch`。
  - `TestBucketsSweepIdleKeys` 移到 ratelimit。
  - `TestForgetClearsNodeState` 与在途上报用例里直接读 `limit.mu`、`limit.m` 的两处改为经导出的 `Allow` 观察。

**Interfaces:**
- Produces：
  - `func ratelimit.New[K comparable](capacity int, refillPer time.Duration) *ratelimit.Buckets[K]`
  - `func (b *Buckets[K]) Allow(key K, now time.Duration) bool`，其中 `now` 是 `clock.Clock.Mono()`
  - `func (b *Buckets[K]) Forget(key K)`
  - `func ratelimit.BySource(b *Buckets[netip.Addr], trusted []netip.Prefix, clk clock.Clock, next http.Handler) http.Handler`：按来源键取令牌（IPv4 一个地址、IPv6 一个 /64），超限时按请求的协议写出 `ResourceExhausted`；放行的请求带着来源键进入 `next`。
  - `func ratelimit.SourceOf(ctx context.Context) (netip.Addr, bool)`：返回归一化后的来源键（IPv4 是地址本身，IPv6 是所在 /64 的网络地址）
- Produces（`internal/hub/auth`）：
  - `func auth.SourceKey(a netip.Addr) netip.Addr`：把 `ClientIP` 的结果归一化成按来源计数的键。IPv4 按单个地址，IPv6 截到所在 /64 的网络地址，IPv4 映射地址先还原。按来源计数的三处都调它：`ratelimit.BySource`（`SourceOf` 返回的就是它的结果）、`Register` 的窗口失败计数、登录失败锁定。
  - `func auth.DescribeSource(key netip.Addr) string`：IPv6 的键写成 `…::/64`。
  - 放在 `auth` 而不是 `ratelimit`：登录锁定在 `auth` 里，`auth` 不能依赖 `ratelimit`；依赖方向保持 `ingest`、`api` → `ratelimit` → `auth`。
- Task 6 用它做公开服务的限流：`ratelimit.New[netip.Addr](60, time.Second/10)` 交给 `BySource`。

`Register` 的限速原来在拦截器里。connect 先读取并解码请求，再进拦截器（实验 9），所以解码失败的 `Register` 一个都不计数（设计决定 7）。本任务把它移到 `ingest.Service.Handler` 的 `BySource` 中间件里，公开服务在 Task 6 用同一个中间件。

按来源计数的键在这里一并定下：IPv4 按单个地址，IPv6 按 /64（一台主机通常独占整个 /64，逐地址计键等于在 /64 里换个地址就换一份计数）。限流、`Register` 的窗口失败计数与登录失败锁定三处用同一个 `auth.SourceKey`。锁定的归一化放在 `failureTracker` 的三个入口，调用方照旧传 `ClientIP` 的结果，日志里的登录来源仍是具体地址。

- [ ] **Step 1: 写失败测试**

`internal/hub/ratelimit/ratelimit_test.go`：

```go
package ratelimit

import (
	"strings"
	"testing"
	"time"
)

func TestAllowSpendsCapacityThenRefillsPerKey(t *testing.T) {
	b := New[string](60, time.Second/10)
	for i := range 60 {
		if !b.Allow("a", 0) {
			t.Fatalf("request %d within capacity denied", i+1)
		}
	}
	if b.Allow("a", 0) {
		t.Fatal("request 61 at the same instant allowed")
	}
	if !b.Allow("b", 0) {
		t.Fatal("another key shares the bucket")
	}
	// 半个补充周期只补回半个令牌；两个半周期的和在浮点里恰为 1，边界不靠舍入。
	if b.Allow("a", 50*time.Millisecond) {
		t.Fatal("token refilled before refillPer elapsed")
	}
	if !b.Allow("a", 100*time.Millisecond) || b.Allow("a", 100*time.Millisecond) {
		t.Fatal("refillPer should restore exactly one token")
	}
	b.Forget("a")
	if !b.Allow("a", 100*time.Millisecond) {
		t.Fatal("forgotten key should start from a full bucket")
	}
}

func TestBucketsSweepIdleKeys(t *testing.T) {
	per := time.Second
	b := New[string](3, per)
	b.Allow("a", 0)
	b.Allow("b", 0)
	b.Allow("c", 3*per)
	if len(b.m) != 1 || b.m["c"] == nil {
		t.Fatalf("idle keys not swept: %+v", b.m)
	}
	b.Allow("active", 3*per)
	active := b.m["active"]
	b.Allow("active", 6*per-time.Millisecond)
	if b.lastSweep != 3*per || len(b.m) != 2 || b.m["active"] != active {
		t.Fatal("swept before period or replaced active bucket")
	}
	b.Allow("d", 6*per)
	if len(b.m) != 2 || b.m["active"] != active || b.m["d"] == nil {
		t.Fatalf("sweep removed active key or retained idle key: %+v", b.m)
	}
}

func TestNewRejectsDegenerateParameters(t *testing.T) {
	for _, c := range []struct {
		capacity int
		per      time.Duration
	}{{0, time.Second}, {1, 0}, {1, -time.Second}} {
		func() {
			defer func() {
				if r := recover(); r == nil || !strings.Contains(r.(string), "capacity must be at least 1 and refillPer positive") {
					t.Errorf("New(%d, %v): recover() = %v", c.capacity, c.per, r)
				}
			}()
			New[string](c.capacity, c.per)
		}()
	}
}
```

`internal/hub/ratelimit/source_test.go`：

```go
package ratelimit

import (
	"io"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"testing"
	"time"

	"github.com/xjetry/probe/internal/clock"
)

func TestBySourceLimitsBeforeNextAndPassesTheSource(t *testing.T) {
	clk := clock.NewFake(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	var seen []string
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		from, ok := SourceOf(r.Context())
		if !ok {
			t.Error("next ran without a source address")
		}
		seen = append(seen, from.String())
	})
	h := BySource(New[netip.Addr](2, time.Second), []netip.Prefix{netip.MustParsePrefix("192.0.2.0/24")}, clk, next)
	call := func(peer, xff string) (int, string) {
		req := httptest.NewRequest(http.MethodPost, "/probe.v1.S/M", strings.NewReader("{}"))
		req.Header.Set("Content-Type", "application/json")
		req.RemoteAddr = peer
		if xff != "" {
			req.Header.Set("X-Forwarded-For", xff)
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		body, _ := io.ReadAll(rec.Body)
		return rec.Code, string(body)
	}
	// 可信代理转发的请求按 X-Forwarded-For 计；不可信对端的这个头被忽略，按对端地址计。
	for range 2 {
		if code, body := call("192.0.2.1:5000", "203.0.113.9"); code != http.StatusOK {
			t.Fatalf("within capacity: %d %s", code, body)
		}
	}
	code, body := call("192.0.2.1:5000", "203.0.113.9")
	if code != http.StatusTooManyRequests || !strings.Contains(body, `"code":"resource_exhausted"`) ||
		!strings.Contains(body, "rate limit exceeded for 203.0.113.9: each source (one IPv4 address, or one IPv6 /64) may make 2 requests at once and then one every 1s") {
		t.Fatalf("past capacity: %d %s", code, body)
	}
	if code, _ := call("198.51.100.1:5000", "203.0.113.9"); code != http.StatusOK {
		t.Fatalf("untrusted peer was charged to the forwarded address: %d", code)
	}
	if want := []string{"203.0.113.9", "203.0.113.9", "198.51.100.1"}; strings.Join(seen, ",") != strings.Join(want, ",") {
		t.Fatalf("next saw sources %v, want %v (the limited request must not reach next)", seen, want)
	}
}

// 一台主机通常独占整个 IPv6 /64，逐地址计键等于不限流：IPv6 同一 /64 的地址共用一桶，不同 /64 各自一桶；
// IPv4 仍是一个地址一桶。放行的请求带进 next 的是同一个键。
func TestBySourceKeysIPv4ByAddressAndIPv6ByPrefix64(t *testing.T) {
	clk := clock.NewFake(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	var seen []string
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		from, _ := SourceOf(r.Context())
		seen = append(seen, from.String())
	})
	h := BySource(New[netip.Addr](1, time.Second), nil, clk, next)
	for _, c := range []struct {
		peer   string
		status int
		body   string
	}{
		{"[2001:db8:1:2::1]:5000", http.StatusOK, ""},
		{"[2001:db8:1:2:ffff:ffff:ffff:ffff]:5000", http.StatusTooManyRequests, "rate limit exceeded for 2001:db8:1:2::/64:"},
		{"[2001:db8:1:3::1]:5000", http.StatusOK, ""},
		{"192.0.2.1:5000", http.StatusOK, ""},
		{"192.0.2.2:5000", http.StatusOK, ""},
		{"192.0.2.1:5001", http.StatusTooManyRequests, "rate limit exceeded for 192.0.2.1:"},
	} {
		req := httptest.NewRequest(http.MethodPost, "/probe.v1.S/M", strings.NewReader("{}"))
		req.Header.Set("Content-Type", "application/json")
		req.RemoteAddr = c.peer
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != c.status || !strings.Contains(rec.Body.String(), c.body) {
			t.Errorf("%s: %d %s, want %d %q", c.peer, rec.Code, rec.Body.String(), c.status, c.body)
		}
	}
	if want := []string{"2001:db8:1:2::", "2001:db8:1:3::", "192.0.2.1", "192.0.2.2"}; strings.Join(seen, ",") != strings.Join(want, ",") {
		t.Errorf("next saw sources %v, want %v", seen, want)
	}
}
```

`internal/hub/auth/proxy_test.go` 末尾追加：

```go
// 按来源计数的键：IPv4 一个地址一个键；IPv6 同一 /64 的地址合成一个键、不同 /64 各自一个；IPv4 映射地址按 IPv4 算，
// 不还原的话它们的前 64 位全是 0，会全部合进 ::/64。
func TestSourceKey(t *testing.T) {
	for _, c := range []struct{ in, want string }{
		{"192.0.2.1", "192.0.2.1"},
		{"192.0.2.2", "192.0.2.2"},
		{"::ffff:192.0.2.1", "192.0.2.1"},
		{"2001:db8:1:2::1", "2001:db8:1:2::"},
		{"2001:db8:1:2:ffff:ffff:ffff:ffff", "2001:db8:1:2::"},
		{"2001:db8:1:3::1", "2001:db8:1:3::"},
		{"fe80::1%eth0", "fe80::"},
	} {
		if got := SourceKey(netip.MustParseAddr(c.in)).String(); got != c.want {
			t.Errorf("SourceKey(%s) = %s, want %s", c.in, got, c.want)
		}
	}
	if got := SourceKey(netip.Addr{}); got.IsValid() {
		t.Errorf("SourceKey(invalid) = %s, want the invalid address back", got)
	}
}

// IPv6 的键写成前缀，免得在报错与日志里被读成一个具体地址。
func TestDescribeSource(t *testing.T) {
	for in, want := range map[string]string{"192.0.2.1": "192.0.2.1", "2001:db8:1:2::": "2001:db8:1:2::/64"} {
		if got := DescribeSource(netip.MustParseAddr(in)); got != want {
			t.Errorf("DescribeSource(%s) = %s, want %s", in, got, want)
		}
	}
}
```

`internal/hub/auth/admin_test.go` 在 `TestSessionExpiresIdleAndAbsolute` 之前加：

```go
// 登录失败按来源键计：一台主机通常独占整个 IPv6 /64，每次换一个 /64 里的地址也在累加同一份计数，
// 锁定后同一 /64 的任何地址都进不来；另一个 /64 不受影响。IPv4 逐地址计由上一个用例钉住。
func TestLoginLockoutCountsAnIPv6Prefix64AsOneSource(t *testing.T) {
	a, _, _ := setup(t)
	ctx := context.Background()
	if err := a.SetPassword(ctx, goodPassword); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < failLimit; i++ {
		from := netip.MustParseAddr(fmt.Sprintf("2001:db8:1:2::%x", i+1))
		if _, err := a.Login(ctx, "wrong password here", from); !errors.Is(err, ErrBadPassword) {
			t.Fatalf("attempt %d from %s: %v", i+1, from, err)
		}
	}
	if _, err := a.Login(ctx, goodPassword, netip.MustParseAddr("2001:db8:1:2:ffff:ffff:ffff:ffff")); !errors.Is(err, ErrLocked) {
		t.Fatalf("another address in the locked /64 got past the lockout: %v", err)
	}
	if _, err := a.Login(ctx, goodPassword, netip.MustParseAddr("2001:db8:1:3::1")); err != nil {
		t.Fatalf("another /64 must not be affected: %v", err)
	}
}
```

`internal/hub/ingest/ingest_test.go`：

- `newHubAt` 拆成两层，配置在挂载之前给定。`Handler` 在挂载时把可信代理交给限流中间件，测试不能挂载后再改 `svc.cfg`。从 `func newHub` 那一行到 `newHubAt` 函数体里的 `t.Helper()` 为止，替换为：

```go
func newHub(t *testing.T) *hub { return newHubAt(t, filepath.Join(t.TempDir(), "t.db")) }

// newHubAt 在给定库文件上起一套 hub；同一路径起两次即模拟 hub 重启。
func newHubAt(t *testing.T, path string) *hub {
	t.Helper()
	return newHubWith(t, path, Config{TTL: 30 * time.Second})
}

// newHubWith 用给定配置起 hub。配置在挂载时读入（Handler 把可信代理交给限流中间件），测试不能挂载后再改。
func newHubWith(t *testing.T, path string, cfg Config) *hub {
	t.Helper()
```

  它原来构造服务的那一行改为 `svc, err := New(cfg, l, st, a, book, reg, clk, slog.Default())`。
- `TestRegisterIsRateLimitedPerSourceAddress` 的前两行替换为：

```go
func TestRegisterIsRateLimitedPerSourceAddress(t *testing.T) {
	h := newHubWith(t, filepath.Join(t.TempDir(), "t.db"), Config{TTL: 30 * time.Second, TrustedProxies: []netip.Prefix{netip.MustParsePrefix("127.0.0.1/32")}})
```

- 删去 `TestInterceptorRateLimitsAnonymousRegisterBeforeDispatch` 及其上方两行注释。它直接给拦截器装处理器，限速离开拦截器之后测的就不是挂载点了。在原处加：

```go
// 限速包在 connect 外面：解码失败的 Register 同样消耗来源地址的令牌，之后连格式正确的请求也在窗口裁决之前被拒。
// 放在拦截器里时这些请求在解码处就被拒绝，一个都不计数。
func TestRegisterRateLimitCountsUndecodableRequests(t *testing.T) {
	h := newHub(t)
	for i := 1; i <= 30; i++ {
		resp, err := http.Post(h.srv.URL+probev1connect.AgentServiceRegisterProcedure, "application/json", strings.NewReader("{"))
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusBadRequest {
			t.Fatalf("malformed request %d: %d, want 400 from the decoder", i, resp.StatusCode)
		}
	}
	_, err := h.client.Register(context.Background(), connect.NewRequest(&probev1.RegisterRequest{Key: "wrong", Name: "n"}))
	if code := connect.CodeOf(err); code != connect.CodeResourceExhausted {
		t.Fatalf("well-formed request after 30 malformed ones: %v, want ResourceExhausted", err)
	}
}

// 只有 Register 进来源地址的桶：同一出口地址后面可以有很多 agent，上报按节点限速，不能被注册耗尽。
func TestRegisterRateLimitDoesNotChargeReports(t *testing.T) {
	h := newHub(t)
	_, tok := h.node(t)
	for i := 1; i <= 31; i++ {
		_, err := h.client.Register(context.Background(), connect.NewRequest(&probev1.RegisterRequest{Key: "wrong", Name: "n"}))
		if want := map[bool]connect.Code{false: connect.CodeUnauthenticated, true: connect.CodeResourceExhausted}[i == 31]; connect.CodeOf(err) != want {
			t.Fatalf("register %d: %v, want %v", i, err, want)
		}
	}
	if _, err := h.client.Report(context.Background(), report(tok, &probev1.Metrics{CpuPct: proto.Float64(1)})); err != nil {
		t.Fatalf("report from an address whose register bucket is empty: %v", err)
	}
}
```

- [ ] **Step 2: 跑红**

```bash
cd /Users/xjetry/work/vibe/probe-public && go test -count=1 ./internal/hub/auth/ ./internal/hub/ratelimit/ ./internal/hub/ingest/ > /tmp/m5-t4-red.log 2>&1; echo $?
```

Expected：1。
- auth：测试编译失败，`SourceKey`、`DescribeSource` 未定义。
- ratelimit：`New`、`BySource`、`SourceOf` 未定义（包里只有测试文件）。
- ingest：只有 `TestRegisterRateLimitCountsUndecodableRequests` 红，报 `well-formed request after 30 malformed ones: unauthenticated: unauthenticated, want ResourceExhausted`：限速还在拦截器里，30 个解码失败的请求一个都没计数。`TestRegisterRateLimitDoesNotChargeReports` 与改过的 `TestRegisterIsRateLimitedPerSourceAddress` 在现状下就是绿的，它们是本次改动的回归网。

- [ ] **Step 3: 实现**

`internal/hub/auth/proxy.go`，在 `func peerIP` 之前加：

```go
// SourceKey 把 ClientIP 得到的来源地址归一化成按来源计数的键：IPv4 按单个地址，IPv6 截到所在 /64 的网络地址。
// 一台主机通常独占整个 /64（SLAAC 与隐私扩展地址随时可换），逐地址计键等于在 /64 里换个地址就换一份计数。
// 按来源计数的三处都经它：匿名入口的限流（ratelimit.BySource）、Register 的窗口失败计数与登录失败锁定（failureTracker）。
// IPv4 映射地址先还原成 IPv4：它们的前 64 位全是 0，不还原就全部落进 ::/64。netip.PrefixFrom 丢掉区域标识（%eth0），
// 键里没有它。无效地址（取不到对端）原样返回，这类请求共用一个键。
func SourceKey(a netip.Addr) netip.Addr {
	a = a.Unmap()
	if !a.Is6() {
		return a
	}
	return netip.PrefixFrom(a, 64).Masked().Addr()
}

// DescribeSource 把 SourceKey 的结果写成给人看的形式：IPv6 的键带上 /64，免得被读成一个具体地址。
func DescribeSource(key netip.Addr) string {
	if key.Is6() {
		return netip.PrefixFrom(key, 64).String()
	}
	return key.String()
}
```

`internal/hub/auth/failures.go` 整份替换：

```go
package auth

import (
	"net/netip"
	"time"
)

// failureTracker 保存滑动窗口中的失败时刻，达到上限后从该次失败起锁满 window。
// 调用方持 Auth.mu，临界区无 I/O；过期的键在访问时回收，每个键最多保存 limit 次失败。
// 键是 SourceKey 归一化后的来源（IPv4 一个地址，IPv6 一个 /64）：在自己的 /64 里换地址不重置计数。归一化在这里的
// 三个入口做，调用方传具体地址即可；传入已归一化的键（Register 从 ratelimit.SourceOf 拿到的就是）结果相同，掩码两次与一次无异。
type failureTracker struct {
	limit  int
	window time.Duration
	m      map[netip.Addr]*failure
}

type failure struct {
	attempts []time.Duration
	until    time.Duration
}

func newFailureTracker(limit int, window time.Duration) *failureTracker {
	return &failureTracker{limit: limit, window: window, m: map[netip.Addr]*failure{}}
}

func (t *failureTracker) sweep(now time.Duration) {
	for ip, f := range t.m {
		if now < f.until {
			continue
		}
		for len(f.attempts) > 0 && now-f.attempts[0] >= t.window {
			f.attempts = f.attempts[1:]
		}
		if len(f.attempts) == 0 {
			delete(t.m, ip)
		}
	}
}

func (t *failureTracker) locked(from netip.Addr, now time.Duration) bool {
	t.sweep(now)
	from = SourceKey(from)
	f := t.m[from]
	return f != nil && now < f.until
}

func (t *failureTracker) record(from netip.Addr, now time.Duration) int {
	t.sweep(now)
	from = SourceKey(from)
	f := t.m[from]
	if f == nil {
		f = &failure{}
		t.m[from] = f
	}
	if now < f.until {
		return len(f.attempts)
	}
	f.attempts = append(f.attempts, now)
	if len(f.attempts) >= t.limit {
		f.until = now + t.window
	}
	return len(f.attempts)
}

func (t *failureTracker) clear(from netip.Addr) { delete(t.m, SourceKey(from)) }
```

`internal/hub/auth/admin.go`：
- `ErrLocked` 改为 `errors.New("too many failed logins from this source (one IPv4 address, or one IPv6 /64)")`。
- `Login` 注释里的"失败按来源 IP 计数，"改为"失败按来源键计数（SourceKey：IPv4 按地址、IPv6 按 /64），"。

`internal/hub/auth/auth.go`：`Register` 注释里的"计数按来源 IP、独立于任何登录失败计数："改为"计数按来源键（SourceKey：IPv4 按地址、IPv6 按 /64）、独立于任何登录失败计数："。

`internal/hub/api/service.go`：`Login` 里锁定的报错改为 `unauthenticated("too many failed logins from this source (one IPv4 address, or one IPv6 /64); retry in 15 minutes")`：锁定按来源键计之后，"from this address" 对 IPv6 不再准确。

`internal/hub/ratelimit/ratelimit.go`：

```go
// Package ratelimit 是 hub 的按键令牌桶（按单调钟补充）与匿名入口按来源的限流中间件（BySource；IPv4 按地址、IPv6 按 /64）。
// AgentService 的 Report（按节点）、Register 与 PublicService（按来源，经 BySource）都用它：限速只有这一份实现。
package ratelimit

import (
	"fmt"
	"sync"
	"time"
)

// Buckets 的容量与补充周期在构造时固定。空闲满 capacity × refillPer 的桶必已补满，与新桶不可区分，
// 所以可以回收；这个推论要求同一实例的补充周期不变，构造时固定就是它的保证。
// 按补满周期最多扫描一次，避免每次请求遍历全部键。mu 保护桶状态与回收进度。
type Buckets[K comparable] struct {
	mu        sync.Mutex
	m         map[K]*bucket
	capacity  float64
	refillPer time.Duration
	lastSweep time.Duration
}

type bucket struct {
	tokens float64
	last   time.Duration
}

// New 要求 capacity 至少为 1、refillPer 为正，否则 panic：零容量拒绝一切、零周期除零，都只可能是装配错误。
func New[K comparable](capacity int, refillPer time.Duration) *Buckets[K] {
	if capacity < 1 || refillPer <= 0 {
		panic(fmt.Sprintf("ratelimit.New(%d, %v): capacity must be at least 1 and refillPer positive", capacity, refillPer))
	}
	return &Buckets[K]{m: map[K]*bucket{}, capacity: float64(capacity), refillPer: refillPer}
}

// Allow 从 key 的桶里取一个令牌，取不到返回 false。now 是单调钟读数（clock.Clock.Mono）。
func (b *Buckets[K]) Allow(key K, now time.Duration) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	fullAfter := time.Duration(b.capacity * float64(b.refillPer))
	if now-b.lastSweep >= fullAfter {
		for k, bk := range b.m {
			if now-bk.last >= fullAfter {
				delete(b.m, k)
			}
		}
		b.lastSweep = now
	}
	bk := b.m[key]
	if bk == nil {
		bk = &bucket{tokens: b.capacity, last: now}
		b.m[key] = bk
	}
	bk.tokens = min(b.capacity, bk.tokens+float64(now-bk.last)/float64(b.refillPer))
	bk.last = now
	if bk.tokens < 1 {
		return false
	}
	bk.tokens--
	return true
}

// Forget 丢掉 key 的桶，下次请求从满桶开始。
func (b *Buckets[K]) Forget(key K) {
	b.mu.Lock()
	delete(b.m, key)
	b.mu.Unlock()
}
```

`internal/hub/ratelimit/source.go`：

```go
package ratelimit

import (
	"context"
	"fmt"
	"net/http"
	"net/netip"

	"connectrpc.com/connect"

	"github.com/xjetry/probe/internal/clock"
	"github.com/xjetry/probe/internal/hub/auth"
)

type sourceCtxKey struct{}

// BySource 是匿名入口的限流中间件：按来源从 b 取令牌，取不到时按请求的协议写出 ResourceExhausted，不进 next。
//
// 它包在 connect 处理器外面：connect 先读取并解码请求，再进拦截器，放在拦截器里的限流数不到解码失败的请求。
// 包在外面，到达挂载点的每个请求都计数，超限的请求也不再消耗解码。
// 来源地址只在对端属于 trusted 时才取 X-Forwarded-For（auth.ClientIP），否则客户端改一个头就能换桶。
// 桶按 auth.SourceKey 归一化后的键计：IPv4 一个地址一桶，IPv6 一个 /64 一桶。
// 放行的请求带着这个键进入 next（SourceOf 取出），下游按同一个键做后续裁决，不再各算一遍。
func BySource(b *Buckets[netip.Addr], trusted []netip.Prefix, clk clock.Clock, next http.Handler) http.Handler {
	errs := connect.NewErrorWriter()
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		key := auth.SourceKey(auth.ClientIP(r.RemoteAddr, r.Header.Get("X-Forwarded-For"), trusted))
		if !b.Allow(key, clk.Mono()) {
			errs.Write(w, r, connect.NewError(connect.CodeResourceExhausted, fmt.Errorf(
				"rate limit exceeded for %s: each source (one IPv4 address, or one IPv6 /64) may make %d requests at once and then one every %v",
				auth.DescribeSource(key), int(b.capacity), b.refillPer)))
			return
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), sourceCtxKey{}, key)))
	})
}

// SourceOf 取出 BySource 放行时记下的来源键（auth.SourceKey 归一化之后：IPv4 是地址本身，IPv6 是所在 /64 的网络地址）；
// 请求没有经过 BySource 时 ok 为 false。
func SourceOf(ctx context.Context) (netip.Addr, bool) {
	a, ok := ctx.Value(sourceCtxKey{}).(netip.Addr)
	return a, ok
}
```

`internal/hub/ingest`：
- `git rm internal/hub/ingest/limiter.go`。
- `ingest_test.go` 删除 `TestBucketsSweepIdleKeys`（已移到 ratelimit）。
- `ingest_test.go` 里两处直接读桶内部的断言（约 550 行 `TestForgetClearsNodeState`、约 684 行在途上报用例）改为经导出的 `Allow` 观察。
  - 前一处替换为：

```go
	// Forget 之后该节点从满桶开始：钟不走，连续 burst 次都放行，说明桶被丢掉而不是留着上次的余量。
	for i := range burst {
		if !h.svc.limit.Allow(id, h.clk.Mono()) {
			t.Fatalf("rate limit bucket survived Forget: request %d of %d denied", i+1, burst)
		}
	}
```

  - 后一处替换为：

```go
	// 在途上报若在 Forget 之后重建了桶，桶里就少了它取走的那个令牌，连续 burst 次里最后一次被拒。
	for i := range burst {
		if !h.svc.limit.Allow(id, h.clk.Mono()) {
			t.Errorf("in-flight report rebuilt rate limit: request %d of %d denied", i+1, burst)
			break
		}
	}
```

- `service.go` 的 import 加 `"github.com/xjetry/probe/internal/hub/ratelimit"`。
- 在原 `burst` 常量的位置（它随 `limiter.go` 一起删了）加：

```go
const (
	// burst 是上报的令牌桶容量：允许上报间隔的抖动与一次立即重试，再多就是异常。
	burst = 3
	// registerBurst 与每秒补充 1 个是 Register 按来源的限速（§5.2；来源的口径见 ratelimit.BySource）。
	registerBurst = 30
)
```

- 字段改为 `limit *ratelimit.Buckets[int64]` 与 `registerLimit *ratelimit.Buckets[netip.Addr]`。
- `New` 的返回语句改为：

```go
	// 上报的补充周期是下发间隔的一半：允许正常间隔内的一次重试。间隔由 TTL 决定，服务存续期间不变。
	return &Service{cfg: cfg, live: l, traffic: book, tasks: tasks, store: st, writer: st, auth: a, clk: clk, log: log,
		limit: ratelimit.New[int64](burst, cfg.TTL/reportsPerTTL/2), registerLimit: ratelimit.New[netip.Addr](registerBurst, time.Second),
		factsHash: map[int64]uint64{}}, nil
```

- `Report` 与 `Forget` 里的两处调用改为 `s.limit.Allow(id, s.clk.Mono())`（并删去其上方关于补充速率的注释，已移到 `New`）与 `s.limit.Forget(nodeID)`。
- `Handler` 整体替换为：

```go
// Handler 挂载 AgentService。Register 是唯一的匿名方法，按来源地址限速（§5.2），限流中间件包在 connect 外面，
// 解码失败的请求同样计数（ratelimit.BySource 的注释写了理由）。Report 不进这个桶：同一出口地址后面可以有很多
// agent，上报按节点限速（Report 方法体里的 s.limit）。路径判定与 connect 分派用同一个 r.URL.Path 全等比较，
// 所以到达 Register 方法体的请求都先经过了限流。
func (s *Service) Handler() (string, http.Handler) {
	path, h := probev1connect.NewAgentServiceHandler(s,
		connect.WithInterceptors(s.authInterceptor()),
		connect.WithReadMaxBytes(maxBody))
	register := ratelimit.BySource(s.registerLimit, s.cfg.TrustedProxies, s.clk, h)
	return path, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == probev1connect.AgentServiceRegisterProcedure {
			register.ServeHTTP(w, r)
			return
		}
		h.ServeHTTP(w, r)
	})
}
```

- 删去 `type registerFromKey struct{}`。
- `authInterceptor` 上方的注释替换为：

```go
// authInterceptor 在挂载点上裁决每个方法的凭据来源。没有在这里显式列出的
// 方法一律拒绝：新增方法不可能因为忘了加检查而被放行。
// Register 的来源限速不在这里，在更外层的 Handler：超限请求连解码都不进，更到不了方法体里会触碰写协程的语句。
```

- `WrapUnary` 里 `AgentServiceRegisterProcedure` 分支替换为：

```go
		case probev1connect.AgentServiceRegisterProcedure:
			// 凭据是请求体里的窗口 key，由 Register 裁决。
			return next(ctx, req)
```

- `Register` 开头取来源的两行替换为：

```go
	// 窗口裁决的失败计数与限速按同一个来源键（IPv4 按地址、IPv6 按 /64）：由 Handler 里的 ratelimit.BySource 算出并放进 ctx。
	// 取不到只可能是挂载绕过了 Handler，属于装配错误。
	from, ok := ratelimit.SourceOf(ctx)
	if !ok {
		panic("ingest: Register reached without the source-address rate limit; mount Service.Handler")
	}
```

- [ ] **Step 4: 跑绿**

```bash
cd /Users/xjetry/work/vibe/probe-public && go test -count=1 ./internal/hub/auth/ ./internal/hub/ratelimit/ ./internal/hub/ingest/ ./internal/hub/api/ ./cmd/hub/ > /tmp/m5-t4-green.log 2>&1; echo $?
cd /Users/xjetry/work/vibe/probe-public && go vet ./... > /tmp/m5-t4-vet.log 2>&1; echo $?
```

Expected：两条都是 0。行为回归网：`TestRateLimitIsTwiceTheReportRate`、`TestRegisterIsRateLimitedPerSourceAddress`、`TestRegisterRateLimitDoesNotChargeReports`，以及 cmd/hub 经真实挂载的注册用例。

- [ ] **Step 5: 提交**

```bash
cd /Users/xjetry/work/vibe/probe-public && git add -A internal/hub/ratelimit internal/hub/ingest internal/hub/auth internal/hub/api/service.go && git commit -m "ratelimit: 令牌桶独立成包，Register 的来源限速移到 connect 解码之前；按来源计数统一按 IPv6 /64" -m "Register 与公开服务按来源地址限速用同一份实现。补充周期原先每次调用传入，回收空闲桶的推论（空闲满一个补满周期即与新桶不可区分）只靠调用方约定同一实例用同一周期；改为构造时固定，由结构承载。connect 先解码再进拦截器，放在拦截器里的限速数不到解码失败的请求；BySource 包在 connect 外面，每个到达挂载点的 Register 都计数，算出的来源键经 context 交给窗口裁决。来源键由 auth.SourceKey 归一化：IPv4 按单个地址、IPv6 按所在 /64，一台主机通常独占整个 /64，逐地址计键等于在 /64 里换个地址就换一份计数；IPv4 映射地址先还原，否则全部落进 ::/64。限流、Register 的窗口失败计数与登录失败锁定三处都经它；它放在 auth 里，登录锁定不必反向依赖 ratelimit。Report 不进这个桶：同一出口地址后面可以有很多 agent。" > /tmp/m5-t4-commit.log 2>&1; echo $?
```

Expected：0。

- [ ] **Step 6: 缺陷注入**

| | 改动 | 命令 | 期望 |
|---|---|---|---|
| a | `Allow` 里 `bk.tokens < 1` 改成 `bk.tokens < 0` | `go test -count=1 ./internal/hub/ratelimit/ > /tmp/m5-t4-inj-a.log 2>&1; echo $?` | 1，`request 61 at the same instant allowed` |
| b | 回收条件 `now-bk.last >= fullAfter` 改成 `>= 0` | 同 a | 1，`sweep removed active key` |
| c | `New` 去掉参数检查 | 同 a | 1，`New(0, 1s): recover() = <nil>` |
| d | ingest 的 `registerBurst` 改成 31 | `go test -count=1 -run TestRegisterIsRateLimited ./internal/hub/ingest/ > /tmp/m5-t4-inj-d.log 2>&1; echo $?` | 1，`attempt 31: unauthenticated, want ResourceExhausted before window decision` |
| e | ingest 的 `Forget` 删去 `s.limit.Forget(nodeID)` | `go test -count=1 -run TestForgetClearsNodeState ./internal/hub/ingest/ > /tmp/m5-t4-inj-e.log 2>&1; echo $?` | 1，`rate limit bucket survived Forget: request 3 of 3 denied` |
| f | `Allow` 开头加 `var zero K; key = zero`（所有键共用一个桶） | 同 a | 1，`another key shares the bucket`；`source_test.go` 红在 `untrusted peer was charged to the forwarded address: 429` |
| g | `Forget` 只解锁、不删桶 | 同 a | 1，`forgotten key should start from a full bucket` |
| h | 把限速换回拦截器：`Handler` 直接 `return path, h`；拦截器的 Register 分支先 `auth.ClientIP(req.Peer().Addr, req.Header().Get("X-Forwarded-For"), s.cfg.TrustedProxies)` 再 `s.registerLimit.Allow`，超限返回 `ResourceExhausted`；`Register` 开头改用同一个 `auth.ClientIP` 自算来源 | `go test -count=1 -run TestRegister ./internal/hub/ingest/ > /tmp/m5-t4-inj-h.log 2>&1; echo $?` | 1，只有 `TestRegisterRateLimitCountsUndecodableRequests` 红：`well-formed request after 30 malformed ones: unauthenticated: unauthenticated, want ResourceExhausted` |
| i | `Handler` 把整个服务包进限流：`return path, ratelimit.BySource(s.registerLimit, s.cfg.TrustedProxies, s.clk, h)` | 同 h | 1，`report from an address whose register bucket is empty: resource_exhausted: rate limit exceeded for 127.0.0.1 …` |
| j | `BySource` 放行时不带来源：`next.ServeHTTP(w, r)` | `go test -count=1 ./internal/hub/ratelimit/ > /tmp/m5-t4-inj-j.log 2>&1; echo $?` | 1，`next ran without a source address` |
| k | `BySource` 取来源时不看可信代理：`auth.ClientIP(…, nil)` | 同 j | 1，`past capacity: 429 … rate limit exceeded for 192.0.2.1 …`：可信代理转发的请求被算到代理自己头上 |
| l | `auth.SourceKey` 的掩码 `64` 改成 `128`（IPv6 逐地址计键） | `go test -count=1 ./internal/hub/auth/ ./internal/hub/ratelimit/ > /tmp/m5-t4-inj-l.log 2>&1; echo $?` | 1，`SourceKey(2001:db8:1:2::1) = 2001:db8:1:2::1, want 2001:db8:1:2::`；登录用例红在 `another address in the locked /64 got past the lockout: <nil>`；`source_test.go` 红在 `[2001:db8:1:2:ffff:ffff:ffff:ffff]:5000: 200 , want 429` |
| m | 同一处改成 `48` | 同 l | 1，`SourceKey(2001:db8:1:2::1) = 2001:db8:1::`；登录用例红在 `another /64 must not be affected: too many failed logins …`：不同 /64 合进了一份计数 |
| n | `SourceKey` 删去 `a = a.Unmap()` | 同 l | 1，`SourceKey(::ffff:192.0.2.1) = ::, want 192.0.2.1`（经 `BySource` 的用例照常绿：`ClientIP` 已经还原过，这条断言钉的是 `SourceKey` 自己不依赖调用方） |
| o | IPv4 分支改成 `return netip.PrefixFrom(a, 24).Masked().Addr()` | 同 l | 1，`SourceKey(192.0.2.2) = 192.0.2.0`；原有的 `another address must not be affected`（10.0.0.1 与 10.0.0.2）与 `lockout is per IP` 也红 |
| p | `BySource` 里去掉 `auth.SourceKey(…)`，直接用 `auth.ClientIP(…)` 作键 | 同 l | 1，只有 `source_test.go` 红：`[2001:db8:1:2:ffff:ffff:ffff:ffff]:5000: 200 , want 429 …` 与 `next saw sources [2001:db8:1:2::1 …]`：限流没有经过归一化 |
| q | `failureTracker` 的 `locked`、`record` 删去 `from = SourceKey(from)`，`clear` 改回 `delete(t.m, from)`（锁定的键改回原地址） | `go test -count=1 ./internal/hub/auth/ > /tmp/m5-t4-inj-q.log 2>&1; echo $?` | 1，`another address in the locked /64 got past the lockout: <nil>`：每次换地址都另起一份计数，锁不住 |
| r | 只删 `locked` 里那一行 `from = SourceKey(from)` | 同 q | 1，同 q：计数按键累加、查锁却按原地址，同样锁不住 |
| s | `DescribeSource` 的 IPv6 分支改成 `return key.String()` | 同 l | 1，`DescribeSource(2001:db8:1:2::) = 2001:db8:1:2::, want 2001:db8:1:2::/64`；`source_test.go` 红在报错写成 `rate limit exceeded for 2001:db8:1:2:::` |

每项：改动 → `git diff --stat` 非空 → 跑命令 → 核对原因 → `git checkout -- <文件>`。

---

### Task 5: PublicService 的四个方法

**Files:**
- Create: `proto/probe/v1/cache.proto`、`proto/probe/v1/public.proto`
- Modify: `buf.yaml`
- Generated（`make gen`）：
  - Go：`gen/probe/v1/cache.pb.go`、`public.pb.go`、`gen/probe/v1/probev1connect/public.connect.go`
  - TS：`web/src/gen/probe/v1/cache_pb.ts`、`public_pb.ts`
- Modify: `internal/hub/store/node.go`（`nodeOrder`、`ListPublicNodes`、`NodeIsPublic`）
- Test: `internal/hub/store/store_test.go`
- Create: `internal/hub/api/history.go`
- Modify: `internal/hub/probe/registry.go`（`TargetFor`）
- Modify: `internal/hub/api/data.go`、`probes.go`、`service.go`（改用共用实现；`Service.history` 字段）
- Create: `internal/hub/api/projection.go`、`projection_test.go`
- Create: `internal/hub/api/public.go`、`public_test.go`
- Modify: `internal/hub/api/api_test.go`（harness 挂上 `Public`；`publicClient`）
- Modify: `cmd/hub/serve.go`（挂载 `PublicService`）、`cmd/hub/mux_test.go`（匿名白名单、GET 枚举）、`cmd/hub/serve_test.go`

**Interfaces:**
- Consumes：
  - Task 1 的 `query.proto` 类型与 `Registry.Target`
  - Task 2 的 `(*store.Store).SiteSettings`
  - Task 3 测试里的 `saveSettings`、`validSettings`
- Produces（proto）：
  - 扩展 `probev1.E_CacheMaxAgeS`（`uint32`，字段号 50002）
  - 服务 `probe.v1.PublicService`：Go 过程常量 `probev1connect.PublicServiceGetSiteProcedure` 等四个；TS `PublicService`（`web/src/gen/probe/v1/public_pb.ts`）
  - 消息 `GetSiteRequest`、`PublicSite`、`PublicServiceGetSnapshotRequest`、`PublicSnapshot`、`PublicNode`、`PublicFacts`、`PublicMetrics`
- Produces（store）：
  - `func (s *Store) ListPublicNodes(ctx context.Context) ([]Node, error)`
  - `func (s *Store) NodeIsPublic(ctx context.Context, id int64) (bool, error)`
- Produces（api）：
  - `type PublicConfig struct { ReportInterval time.Duration; TrustedProxies []netip.Prefix }`
  - `func NewPublic(cfg PublicConfig, st *store.Store, l *live.Live, book *traffic.Book, probes *probe.Registry, clk clock.Clock, log *slog.Logger) *Public`
  - `func (p *Public) connectHandler() (string, http.Handler)`：未经中间件的处理器，Task 7 的等价测试要用。
  - `func (p *Public) Handler() (string, http.Handler)`：Task 6 在它里面套上中间件。
  - `func noPublicNode() error`
  - `type history struct{…}`，方法 `metrics`、`probeSeries(ctx, m, maxPoints, label taskLabel)`
  - `type taskLabel func(taskID uint64) (kind probev1.ProbeKind, target string, ok bool)`
- Produces（probe）：`func (r *Registry) TargetFor(nodeID int64, id uint64) (kind probev1.ProbeKind, target string, ok bool)`
  - `func checkWindow(from, to int64, requested uint32) (int, error)`
  - `func liveState(l *live.Live, n store.Node) (bool, *int64, *probev1.Metrics)`
  - `type projection`：`newProjection`、`apply`
- Produces（测试辅助）：
  - `harness.pub *Public`
  - `func (h *harness) publicClient(opts ...connect.ClientOption) probev1connect.PublicServiceClient`
  - `func (h *harness) setPublic(t *testing.T, id int64, name string, public bool)`
  - `pubGet`、`pubPost`、`jsonQuery`、`type pubResult`

- [ ] **Step 1: proto**

`proto/probe/v1/cache.proto`：

```proto
syntax = "proto3";

package probe.v1;

option go_package = "github.com/xjetry/probe/gen/probe/v1;probev1";

import "google/protobuf/descriptor.proto";

extend google.protobuf.MethodOptions {
  // GET 成功响应的 Cache-Control: max-age（秒）。probe.v1 的每个方法接受 GET（idempotency_level = NO_SIDE_EFFECTS）
  // 当且仅当声明了这个值且大于 0；hub 装配时逐个方法核对，不符即拒绝启动。失败响应一律 no-store，POST 响应不带缓存头。
  // 50000–99999 是留给组织内部扩展的字段号区间。
  uint32 cache_max_age_s = 50002;
}
```

`proto/probe/v1/public.proto`：

```proto
syntax = "proto3";

package probe.v1;

option go_package = "github.com/xjetry/probe/gen/probe/v1;probev1";

import "probe/v1/cache.proto";
import "probe/v1/query.proto";
import "probe/v1/types.proto";

// 公开页与第三方主题 → hub。没有鉴权，不需要也不看任何凭据；按来源限流（IPv4 一个地址、IPv6 一个 /64 算一个来源）：
// 每个来源桶容量 60、每秒补充 10，超出返回 ResourceExhausted。只对标为公开的节点应答，未公开的节点与不存在的节点得到同一个 NotFound。
// 方法都无副作用，可用 GET 调用：/probe.v1.PublicService/<方法>?connect=v1&encoding=json&message=<URL 编码的 JSON 请求>。
// GET 的成功响应带 Cache-Control: max-age=<cache_max_age_s>，失败响应带 no-store；POST 响应不带缓存头。
service PublicService {
  // 站点外观：标题、明暗、主色、logo 与自定义 CSS。
  rpc GetSite(GetSiteRequest) returns (PublicSite) {
    option idempotency_level = NO_SIDE_EFFECTS;
    option (probe.v1.cache_max_age_s) = 300;
  }
  // 全部公开节点的实时状态。请求是规范形态时（GET 的 message 为 JSON {} 或 proto 空消息，POST 的正文为 {} 或空，
  // 不带压缩与 Connect-Timeout-Ms；connect-web 与 curl 的写法都是），hub 按编码缓存响应字节 1 秒，窗口内不再序列化；
  // 其余形态每次序列化。节点改为非公开后，hub 最多再下发它 1 秒；GET 响应另带 max-age=1，浏览器与中间缓存还可再用 1 秒。
  rpc GetSnapshot(PublicServiceGetSnapshotRequest) returns (PublicSnapshot) {
    option idempotency_level = NO_SIDE_EFFECTS;
    option (probe.v1.cache_max_age_s) = 1;
  }
  // 公开节点一段时间的指标历史；请求与响应和 AdminService.QueryMetrics 相同。
  rpc QueryMetrics(QueryMetricsRequest) returns (QueryMetricsResponse) {
    option idempotency_level = NO_SIDE_EFFECTS;
    option (probe.v1.cache_max_age_s) = 60;
  }
  // 公开节点一段时间的探测历史。当前分配给该节点的任务，序列带种类与目标：把节点标为公开即公开它正在探测的目标。
  // 历史里有、但已从该节点撤下的任务不带这两项，客户端退回用 task_id 称呼。
  rpc QueryProbes(QueryProbesRequest) returns (QueryProbesResponse) {
    option idempotency_level = NO_SIDE_EFFECTS;
    option (probe.v1.cache_max_age_s) = 60;
  }
}

message GetSiteRequest {}

// 公开页外观，取值约束见 AdminService.UpdateSettings。空串表示该项用公开页的默认值。
message PublicSite {
  string title = 1;
  // auto、light 或 dark。
  string theme = 2;
  // #rrggbb 或空串。
  string accent_color = 3;
  // data:image/…;base64,… 或空串。
  string logo = 4;
  // 放在公开页内置样式之后的 CSS；不含 "</"。
  string custom_css = 5;
}

message PublicServiceGetSnapshotRequest {}

message PublicSnapshot {
  // hub 墙钟，Unix 秒；客户端据此显示"多久之前"而不依赖自己的时钟。
  int64 now = 1;
  // hub 下发给 agent 的上报间隔；实时状态不会比它更新得更快。
  uint32 report_interval_ms = 2;
  // 按 sort_order、id 升序。
  repeated PublicNode nodes = 3;
}

message PublicNode {
  int64 id = 1;
  string name = 2;
  // 在线 ⇔ 距最近一次上报不足 TTL，hub 是唯一裁决者。
  bool online = 3;
  // 最近一次上报的墙钟 Unix 秒；从未上报则缺失。
  optional int64 last_seen_at = 4;
  // 管理员在面板里定的顺序；nodes 已按它排好。
  int32 sort_order = 5;
  // 主机静态信息的公开部分；从未上报则缺失。
  PublicFacts facts = 6;
  // 最近一次上报的读数；从未上报则缺失，离线节点仍带最后一次读数。
  PublicMetrics metrics = 7;
  // hub 侧累计的流量；每个节点都有，从未上报的节点为零用量。
  Traffic traffic = 8;
}

// Facts 的公开部分，字段号与 Facts 相同。主机名、内核版本、agent 版本与 ICMP 可用性不公开，
// 它们的号与名保留：要公开必须先删掉 reserved，而不是随手加一个字段。
message PublicFacts {
  reserved 1, 3, 8, 9;
  reserved "hostname", "kernel", "agent_version", "icmp_available";
  string os = 2;
  string arch = 4;
  string virtualization = 5;
  string cpu_model = 6;
  uint32 cpu_cores = 7;
}

// 与 Metrics 同字段号、同语义，只是没有 boot_id（流量差分用的内部标识）。每个读数都是 optional：
// 缺失表示无读数，与读数为 0 是两个不同的事实。
message PublicMetrics {
  reserved 1;
  reserved "boot_id";
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
  // 内核累计计数器，字节。
  optional uint64 net_rx_total = 12;
  optional uint64 net_tx_total = 13;
  // agent 自测的瞬时速率，字节/秒。
  optional uint64 net_rx_bps = 14;
  optional uint64 net_tx_bps = 15;
  optional uint32 tcp_conns = 16;
  optional uint32 udp_conns = 17;
  optional uint32 procs = 18;
  optional uint64 uptime_s = 19;
}
```

`buf.yaml` 整份替换：

```yaml
version: v2
modules:
  - path: proto
lint:
  use:
    - STANDARD
  # 豁免只到文件：历史查询的请求与响应由管理与公开两个服务共用（§10），公开服务的两个方法直接返回
  # §10 定义的 PublicSite 与 PublicSnapshot。其余文件照常受这两条规则约束。
  ignore_only:
    RPC_REQUEST_RESPONSE_UNIQUE:
      - proto/probe/v1/admin.proto
      - proto/probe/v1/public.proto
    RPC_RESPONSE_STANDARD_NAME:
      - proto/probe/v1/public.proto
breaking:
  use:
    - WIRE_JSON
```

```bash
cd /Users/xjetry/work/vibe/probe-public && make gen > /tmp/m5-t5-gen.log 2>&1; echo $?
cd /Users/xjetry/work/vibe/probe-public && buf lint > /tmp/m5-t5-lint.log 2>&1; echo $?
cd /Users/xjetry/work/vibe/probe-public && buf breaking --against '/Users/xjetry/work/vibe/probe/.git#branch=main' > /tmp/m5-t5-breaking.log 2>&1; echo $?
cd /Users/xjetry/work/vibe/probe-public && grep -c 'IdempotencyNoSideEffects' gen/probe/v1/probev1connect/public.connect.go > /tmp/m5-t5-idem.log 2>&1; echo $?
```

Expected：
- 前三条都是 0。
- 第四条 0，且 `/tmp/m5-t5-idem.log` 是 8：生成的客户端与处理器各四处，证明四个方法都标了无副作用。
- 冒烟：临时删掉 `buf.yaml` 里 `RPC_REQUEST_RESPONSE_UNIQUE` 那三行，`buf lint` 应退出 100 并列出两个文件；看完 `git checkout -- buf.yaml`。

- [ ] **Step 2: 写失败测试**

`internal/hub/store/store_test.go` 末尾追加：

```go
func TestPublicNodeQueriesSeeOnlyPublicNodes(t *testing.T) {
	s, _ := open(t)
	ctx := t.Context()
	var ids []int64
	for i, name := range []string{"a", "b", "c"} {
		id, err := s.CreateNode(ctx, name, hash(byte(i)))
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, id)
	}
	for _, i := range []int{0, 2} {
		if err := s.UpdateNode(ctx, ids[i], []string{"a", "b", "c"}[i], true, "", 1, 0); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.ReorderNodes(ctx, []int64{ids[2], ids[1], ids[0]}); err != nil {
		t.Fatal(err)
	}
	nodes, err := s.ListPublicNodes(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, n := range nodes {
		names = append(names, n.Name)
	}
	if strings.Join(names, ",") != "c,a" {
		t.Fatalf("public nodes = %v, want c,a in panel order", names)
	}
	for id, want := range map[int64]bool{ids[0]: true, ids[1]: false, ids[2]: true, 999999: false} {
		if got, err := s.NodeIsPublic(ctx, id); err != nil || got != want {
			t.Errorf("NodeIsPublic(%d) = %v %v, want %v", id, got, err, want)
		}
	}
}
```

（`strings` 已在 `store_test.go` 的 import 里。）

`internal/hub/api/api_test.go`：
- `harness` 结构体加字段 `pub *Public`。
- `newHarness` 在 `svc := New(...)` 之后加 `pub := NewPublic(PublicConfig{ReportInterval: 10 * time.Second, TrustedProxies: prefixes}, st, l, book, reg, clk, slog.Default())`，在 `mux.Handle(svc.Handler())` 之后加 `mux.Handle(pub.Handler())`，返回值加 `pub: pub`。
- 在 `report` 之后加：

```go
// publicClient 是不带任何凭据的公开服务客户端：harness.http 带着会话 cookie jar，这里用裸客户端。
func (h *harness) publicClient(opts ...connect.ClientOption) probev1connect.PublicServiceClient {
	return probev1connect.NewPublicServiceClient(h.srv.Client(), h.srv.URL, opts...)
}

// setPublic 只改公开与否；UpdateNode 整体替换可编辑字段，其余取建节点时的默认值（重置日 1、宽限期取 TTL）。
func (h *harness) setPublic(t *testing.T, id int64, name string, public bool) {
	t.Helper()
	req := &probev1.UpdateNodeRequest{Id: id, Name: name, Public: public, TrafficResetDay: 1, OfflineGraceS: proto.Uint32(0)}
	if _, err := h.admin.UpdateNode(t.Context(), connect.NewRequest(req)); err != nil {
		t.Fatal(err)
	}
}
```

`internal/hub/api/projection_test.go`：

```go
package api

import (
	"testing"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protodesc"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/descriptorpb"
	"google.golang.org/protobuf/types/dynamicpb"

	probev1 "github.com/xjetry/probe/gen/probe/v1"
)

// projectionFixtures 造几种单字段消息：Src 是 int32 a；其余各在一处与它不对齐。
func projectionFixtures(t *testing.T) protoreflect.FileDescriptor {
	t.Helper()
	i32 := descriptorpb.FieldDescriptorProto_TYPE_INT32.Enum()
	i64 := descriptorpb.FieldDescriptorProto_TYPE_INT64.Enum()
	single := descriptorpb.FieldDescriptorProto_LABEL_OPTIONAL.Enum()
	many := descriptorpb.FieldDescriptorProto_LABEL_REPEATED.Enum()
	field := func(name string, typ *descriptorpb.FieldDescriptorProto_Type, label *descriptorpb.FieldDescriptorProto_Label) *descriptorpb.FieldDescriptorProto {
		return &descriptorpb.FieldDescriptorProto{Name: proto.String(name), Number: proto.Int32(1), Type: typ, Label: label, JsonName: proto.String(name)}
	}
	msg := func(name string, f *descriptorpb.FieldDescriptorProto) *descriptorpb.DescriptorProto {
		return &descriptorpb.DescriptorProto{Name: proto.String(name), Field: []*descriptorpb.FieldDescriptorProto{f}}
	}
	// proto3 optional 由一个合成 oneof 承载 presence。
	optional := msg("WithPresence", field("a", i32, single))
	optional.Field[0].Proto3Optional, optional.Field[0].OneofIndex = proto.Bool(true), proto.Int32(0)
	optional.OneofDecl = []*descriptorpb.OneofDescriptorProto{{Name: proto.String("_a")}}
	fd, err := protodesc.NewFile(&descriptorpb.FileDescriptorProto{
		Name: proto.String("projection_fixtures.proto"), Package: proto.String("projfix"), Syntax: proto.String("proto3"),
		MessageType: []*descriptorpb.DescriptorProto{
			msg("Src", field("a", i32, single)),
			msg("Renamed", field("z", i32, single)),
			msg("Wider", field("a", i64, single)),
			msg("Repeated", field("a", i32, many)),
			optional,
		},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	return fd
}

func TestNewProjectionRejectsMisalignedFields(t *testing.T) {
	fd := projectionFixtures(t)
	desc := func(name string) protoreflect.MessageDescriptor { return fd.Messages().ByName(protoreflect.Name(name)) }
	for _, c := range []struct{ dst, want string }{
		{"Renamed", "has no counterpart named z"},
		{"Wider", "does not match"},
		{"WithPresence", "does not match"},
		{"Repeated", "only singular scalar fields"},
	} {
		t.Run(c.dst, func(t *testing.T) {
			expectPanic(t, c.want, func() { newProjection(dynamicpb.NewMessageType(desc(c.dst)), desc("Src")) })
		})
	}
	newProjection(dynamicpb.NewMessageType(desc("Src")), desc("Src"))
}

// 只复制源里存在的字段：optional 缺失仍是缺失，显式的 0 仍是 0；目标没有的字段（boot_id）不出现。
func TestProjectionKeepsPresenceAndDropsUndeclaredFields(t *testing.T) {
	p := newProjection((&probev1.PublicMetrics{}).ProtoReflect().Type(), (&probev1.Metrics{}).ProtoReflect().Descriptor())
	got := p.apply(&probev1.Metrics{BootId: "b", MemUsed: proto.Uint64(0), Load1: proto.Float64(0.5)}).(*probev1.PublicMetrics)
	if want := (&probev1.PublicMetrics{MemUsed: proto.Uint64(0), Load1: proto.Float64(0.5)}); !proto.Equal(got, want) || got.CpuPct != nil {
		t.Fatalf("projected = %v, want %v", got, want)
	}
}
```

`internal/hub/api/public_test.go`：

```go
package api

import (
	"bytes"
	"fmt"
	"io"
	"maps"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"

	probev1 "github.com/xjetry/probe/gen/probe/v1"
	"github.com/xjetry/probe/internal/hub/metric"
)

// pubResult 是一次原样 HTTP 调用的结果：公开服务的断言常要比较整段响应字节与响应头。
type pubResult struct {
	status int
	header http.Header
	body   []byte
}

// rawClient 不带 cookie，也不替调用方协商压缩：默认 Transport 会自动加 Accept-Encoding: gzip 并解压、
// 删掉 Content-Encoding，测试就看不到服务端实际发了什么。
var rawClient = &http.Client{Transport: &http.Transport{DisableCompression: true}}

func jsonQuery(msg string) string { return "connect=v1&encoding=json&message=" + url.QueryEscape(msg) }

func pubDo(t *testing.T, req *http.Request, header map[string]string) pubResult {
	t.Helper()
	for k, v := range header {
		req.Header.Set(k, v)
	}
	resp, err := rawClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return pubResult{status: resp.StatusCode, header: resp.Header, body: body}
}

// pubGet 用 curl 同款的 Connect GET 形态调公开服务，不带任何凭据；query 是已编码的查询串。
func pubGet(t *testing.T, h *harness, method, query string, header map[string]string) pubResult {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, h.srv.URL+"/probe.v1.PublicService/"+method+"?"+query, nil)
	if err != nil {
		t.Fatal(err)
	}
	return pubDo(t, req, header)
}

// pubPost 以 JSON POST 调公开服务；header 里的 Content-Type 覆盖默认值。
func pubPost(t *testing.T, h *harness, method, body string, header map[string]string) pubResult {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, h.srv.URL+"/probe.v1.PublicService/"+method, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	return pubDo(t, req, header)
}

// 未公开与不存在的节点得到同一个响应：状态码与正文逐字节相同，错误里没有 id。
func TestPublicHistoryTreatsPrivateAndMissingNodesAlike(t *testing.T) {
	h := newHarness(t, "")
	h.login(t)
	pub, _ := h.createNode(t, "pub")
	priv, _ := h.createNode(t, "priv")
	h.setPublic(t, pub, "pub", true)
	window := func(id int64) string { return fmt.Sprintf(`{"nodeId":"%d","from":"0","to":"3600"}`, id) }
	for _, method := range []string{"QueryMetrics", "QueryProbes"} {
		if got := pubGet(t, h, method, jsonQuery(window(pub)), nil); got.status != http.StatusOK {
			t.Fatalf("%s on a public node: %d %s", method, got.status, got.body)
		}
		private := pubGet(t, h, method, jsonQuery(window(priv)), nil)
		missing := pubGet(t, h, method, jsonQuery(window(999999)), nil)
		if private.status != http.StatusNotFound || missing.status != private.status || !bytes.Equal(private.body, missing.body) {
			t.Fatalf("%s: private %d %s, missing %d %s", method, private.status, private.body, missing.status, missing.body)
		}
		if !bytes.Contains(private.body, []byte(`"node_id: no public node has this id"`)) {
			t.Fatalf("%s: body %s", method, private.body)
		}
	}
}

func TestPublicHistorySharesWindowValidation(t *testing.T) {
	h := newHarness(t, "")
	h.login(t)
	pub, _ := h.createNode(t, "pub")
	priv, _ := h.createNode(t, "priv")
	h.setPublic(t, pub, "pub", true)
	client := h.publicClient()
	for _, tc := range []struct {
		name           string
		node, from, to int64
		max            uint32
		code           connect.Code
		text           string
	}{
		{"negative", pub, -1, 60, 0, connect.CodeInvalidArgument, "from must be a nonnegative Unix timestamp; got -1"},
		{"order", pub, 60, 60, 0, connect.CodeInvalidArgument, "from (60) must be earlier than to (60)"},
		{"span", pub, 0, 401 * 86400, 0, connect.CodeInvalidArgument, "window spans 34646400 seconds; the maximum is 34560000 (400 days)"},
		{"points", pub, 0, 3600, 2001, connect.CodeInvalidArgument, "max_points must be at most 2000; got 2001"},
		{"private", priv, 0, 3600, 0, connect.CodeNotFound, "node_id: no public node has this id"},
		{"missing", 999, 0, 3600, 0, connect.CodeNotFound, "node_id: no public node has this id"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, pe := client.QueryProbes(t.Context(), connect.NewRequest(&probev1.QueryProbesRequest{NodeId: tc.node, From: tc.from, To: tc.to, MaxPoints: tc.max}))
			_, me := client.QueryMetrics(t.Context(), connect.NewRequest(&probev1.QueryMetricsRequest{NodeId: tc.node, From: tc.from, To: tc.to, MaxPoints: tc.max}))
			for _, err := range []error{pe, me} {
				if codeOf(err) != tc.code || !strings.Contains(err.Error(), tc.text) {
					t.Errorf("error=%v want=%s %q", err, tc.code, tc.text)
				}
			}
		})
	}
}

func TestPublicSnapshotListsOnlyPublicNodesWithPublicFields(t *testing.T) {
	h := newHarness(t, "")
	h.login(t)
	ctx := t.Context()
	a, tokA := h.createNode(t, "a")
	b, _ := h.createNode(t, "b")
	c, _ := h.createNode(t, "c")
	h.setPublic(t, a, "a", true)
	h.setPublic(t, c, "c", true)
	if _, err := h.admin.ReorderNodes(ctx, connect.NewRequest(&probev1.ReorderNodesRequest{Ids: []int64{c, b, a}})); err != nil {
		t.Fatal(err)
	}
	facts := &probev1.Facts{Hostname: "secret-host", Os: "Debian 12", Kernel: "6.1.0-secret", Arch: "amd64", Virtualization: "kvm",
		CpuModel: "EPYC", CpuCores: 4, AgentVersion: "v9.9.9-secret", IcmpAvailable: true}
	if err := h.store.UpsertFacts(ctx, a, 1, facts); err != nil {
		t.Fatal(err)
	}
	if err := h.report(t, tokA, &probev1.Metrics{BootId: "boot-secret", CpuPct: proto.Float64(12.5), MemUsed: proto.Uint64(0), MemTotal: proto.Uint64(1 << 30)}); err != nil {
		t.Fatal(err)
	}
	resp, err := h.publicClient().GetSnapshot(ctx, connect.NewRequest(&probev1.PublicServiceGetSnapshotRequest{}))
	if err != nil {
		t.Fatal(err)
	}
	snap := resp.Msg
	if snap.GetNow() != h.clk.Now().Unix() || snap.GetReportIntervalMs() != 10000 || len(snap.GetNodes()) != 2 {
		t.Fatalf("snapshot = %v", snap)
	}
	first, second := snap.GetNodes()[0], snap.GetNodes()[1]
	if first.GetId() != c || second.GetId() != a || first.GetSortOrder() >= second.GetSortOrder() {
		t.Fatalf("nodes not in panel order: %v", snap.GetNodes())
	}
	if first.GetOnline() || first.LastSeenAt != nil || first.Facts != nil || first.Metrics != nil || first.Traffic == nil {
		t.Fatalf("never-reported node = %v", first)
	}
	wantFacts := &probev1.PublicFacts{Os: "Debian 12", Arch: "amd64", Virtualization: "kvm", CpuModel: "EPYC", CpuCores: 4}
	wantMetrics := &probev1.PublicMetrics{CpuPct: proto.Float64(12.5), MemUsed: proto.Uint64(0), MemTotal: proto.Uint64(1 << 30)}
	if !second.GetOnline() || second.GetLastSeenAt() != h.clk.Now().Unix() || !proto.Equal(second.GetFacts(), wantFacts) ||
		!proto.Equal(second.GetMetrics(), wantMetrics) || second.Traffic == nil {
		t.Fatalf("reported node = %v", second)
	}
	// 正文层面再核一次：不公开的字段与私有节点的名字都不在 JSON 里。
	raw := pubGet(t, h, "GetSnapshot", jsonQuery("{}"), nil)
	for _, leak := range []string{"secret", "hostname", "kernel", "agentVersion", "icmpAvailable", "bootId", `"b"`} {
		if bytes.Contains(raw.body, []byte(leak)) {
			t.Errorf("snapshot JSON contains %s: %s", leak, raw.body)
		}
	}
}

func TestPublicSiteServesSavedSettings(t *testing.T) {
	h := newHarness(t, "")
	h.login(t)
	client := h.publicClient()
	got, err := client.GetSite(t.Context(), connect.NewRequest(&probev1.GetSiteRequest{}))
	if err != nil || !proto.Equal(got.Msg, &probev1.PublicSite{Theme: "auto"}) {
		t.Fatalf("never saved: %v %v", got, err)
	}
	saveSettings(t, h, validSettings())
	got, err = client.GetSite(t.Context(), connect.NewRequest(&probev1.GetSiteRequest{}))
	want := &probev1.PublicSite{Title: "状态", Theme: "dark", AccentColor: "#112233", Logo: "data:image/png;base64,iVBORw0KGgo=", CustomCss: "body { color: red }"}
	if err != nil || !proto.Equal(got.Msg, want) {
		t.Fatalf("site = %v %v, want %v", got, err, want)
	}
}

// publicFields 是 PublicService 的响应能到达的每个消息的字段全集。往这些消息加字段就是公开给匿名访客，
// 必须同时改这份清单——与 access_test 的 readMethods 同一口径。共用的 Traffic 与历史查询类型同样在列：
// 给它们加字段也会出现在公开页。
var publicFields = map[protoreflect.FullName][]protoreflect.Name{
	"probe.v1.PublicSite":     {"title", "theme", "accent_color", "logo", "custom_css"},
	"probe.v1.PublicSnapshot": {"now", "report_interval_ms", "nodes"},
	"probe.v1.PublicNode":     {"id", "name", "online", "last_seen_at", "sort_order", "facts", "metrics", "traffic"},
	"probe.v1.PublicFacts":    {"os", "arch", "virtualization", "cpu_model", "cpu_cores"},
	"probe.v1.PublicMetrics": {"cpu_pct", "load1", "load5", "load15", "mem_total", "mem_used", "swap_total", "swap_used",
		"disk_total", "disk_used", "net_rx_total", "net_tx_total", "net_rx_bps", "net_tx_bps", "tcp_conns", "udp_conns", "procs", "uptime_s"},
	"probe.v1.Traffic":              {"total_rx", "total_tx", "period_rx", "period_tx", "period_start", "next_reset_at", "reset_day"},
	"probe.v1.QueryMetricsResponse": {"level", "step_s", "ts", "series"},
	"probe.v1.MetricSeries":         {"name", "unit", "samples"},
	"probe.v1.MetricSample":         {"n", "mean", "max", "sum"},
	"probe.v1.QueryProbesResponse":  {"level", "step_s", "series"},
	"probe.v1.ProbeSeries":          {"task_id", "samples", "kind", "target"},
	"probe.v1.ProbeSample":          {"ts", "sent", "lost", "errors", "rtt_mean_us", "rtt_min_us", "rtt_max_us"},
}

func TestPublicResponsesExposeOnlyAllowlistedFields(t *testing.T) {
	svc := probev1.File_probe_v1_public_proto.Services().ByName("PublicService")
	seen := map[protoreflect.FullName]bool{}
	var walk func(md protoreflect.MessageDescriptor)
	walk = func(md protoreflect.MessageDescriptor) {
		if seen[md.FullName()] {
			return
		}
		seen[md.FullName()] = true
		var got []protoreflect.Name
		for i := 0; i < md.Fields().Len(); i++ {
			f := md.Fields().Get(i)
			got = append(got, f.Name())
			if f.Message() != nil {
				walk(f.Message())
			}
		}
		want, ok := publicFields[md.FullName()]
		if !ok {
			t.Errorf("%s is reachable from PublicService but not in publicFields; its fields are %v", md.FullName(), got)
			return
		}
		slices.Sort(got)
		if want = slices.Sorted(slices.Values(want)); !slices.Equal(got, want) {
			t.Errorf("fields of %s = %v, allowlist has %v", md.FullName(), got, want)
		}
	}
	if svc.Methods().Len() == 0 {
		t.Fatal("PublicService has no methods")
	}
	for i := 0; i < svc.Methods().Len(); i++ {
		walk(svc.Methods().Get(i).Output())
	}
	for name := range publicFields {
		if !seen[name] {
			t.Errorf("publicFields lists %s, which no PublicService response reaches", name)
		}
	}
}

// 公开节点的历史与管理端同一来源：对同一个请求两端的应答逐字段相同，公开端只多了"节点必须公开"这一道门。
// 任务仍分配给该节点时探测序列的标签两端也相同；撤下之后的分歧由 TestPublicProbeLabelsOnlyTasksAssignedToTheNode 钉住。
func TestPublicHistoryMatchesAdminForAPublicNode(t *testing.T) {
	h := newHarness(t, "")
	h.login(t)
	id, _ := h.createNode(t, "pub")
	h.setPublic(t, id, "pub", true)
	saved, err := h.admin.SaveProbeTask(t.Context(), connect.NewRequest(&probev1.SaveProbeTaskRequest{
		Task:    &probev1.ProbeTask{Kind: probev1.ProbeKind_PROBE_KIND_ICMP, Target: "192.0.2.1", IntervalS: 30, TimeoutMs: 1000},
		NodeIds: []int64{id},
	}))
	if err != nil {
		t.Fatal(err)
	}
	base := h.clk.Now().Truncate(time.Hour).Unix()
	b := metric.NewBucket()
	b.Add(&probev1.Metrics{CpuPct: proto.Float64(7)})
	batch := metric.Batch{
		Rows:   []metric.Row{{NodeID: id, TS: base, Bucket: b}},
		Probes: []metric.ProbeRow{{NodeID: id, TS: base, TaskID: saved.Msg.GetTask().GetTask().GetId(), Bucket: &metric.ProbeBucket{Sent: 2, Lost: 1}}},
	}
	if _, err := h.store.WriteMinuteBatch(t.Context(), batch); err != nil {
		t.Fatal(err)
	}
	pub := h.publicClient()
	metrics := &probev1.QueryMetricsRequest{NodeId: id, From: base, To: base + 3600}
	pm, err := pub.QueryMetrics(t.Context(), connect.NewRequest(metrics))
	if err != nil {
		t.Fatal(err)
	}
	am, err := h.admin.QueryMetrics(t.Context(), connect.NewRequest(metrics))
	if err != nil {
		t.Fatal(err)
	}
	if len(am.Msg.GetTs()) != 1 || !proto.Equal(pm.Msg, am.Msg) {
		t.Errorf("public QueryMetrics = %v\nadmin QueryMetrics = %v", pm.Msg, am.Msg)
	}
	probes := &probev1.QueryProbesRequest{NodeId: id, From: base, To: base + 3600}
	pp, err := pub.QueryProbes(t.Context(), connect.NewRequest(probes))
	if err != nil {
		t.Fatal(err)
	}
	ap, err := h.admin.QueryProbes(t.Context(), connect.NewRequest(probes))
	if err != nil {
		t.Fatal(err)
	}
	if len(ap.Msg.GetSeries()) != 1 || !proto.Equal(pp.Msg, ap.Msg) {
		t.Errorf("public QueryProbes = %v\nadmin QueryProbes = %v", pp.Msg, ap.Msg)
	}
}

// 公开端只标注当前分配给被查节点的任务：任务从公开节点撤下、目标改成只分配给私有节点的内网地址之后，
// 公开节点历史里的这条序列不带种类与目标；管理端照旧按任务当前的配置标注。
func TestPublicProbeLabelsOnlyTasksAssignedToTheNode(t *testing.T) {
	h := newHarness(t, "")
	h.login(t)
	pub, _ := h.createNode(t, "pub")
	priv, _ := h.createNode(t, "priv")
	h.setPublic(t, pub, "pub", true)
	save := func(id uint64, target string, node int64) uint64 {
		t.Helper()
		resp, err := h.admin.SaveProbeTask(t.Context(), connect.NewRequest(&probev1.SaveProbeTaskRequest{
			Task:    &probev1.ProbeTask{Id: id, Kind: probev1.ProbeKind_PROBE_KIND_ICMP, Target: target, IntervalS: 30, TimeoutMs: 1000},
			NodeIds: []int64{node},
		}))
		if err != nil {
			t.Fatal(err)
		}
		return resp.Msg.GetTask().GetTask().GetId()
	}
	kept := save(0, "192.0.2.1", pub)
	moved := save(0, "192.0.2.2", pub)
	base := h.clk.Now().Truncate(time.Hour).Unix()
	rows := []metric.ProbeRow{
		{NodeID: pub, TS: base, TaskID: kept, Bucket: &metric.ProbeBucket{Sent: 1, Lost: 1}},
		{NodeID: pub, TS: base, TaskID: moved, Bucket: &metric.ProbeBucket{Sent: 1, Lost: 1}},
	}
	if _, err := h.store.WriteMinuteBatch(t.Context(), metric.Batch{Probes: rows}); err != nil {
		t.Fatal(err)
	}
	save(moved, "10.0.0.5", priv)
	req := &probev1.QueryProbesRequest{NodeId: pub, From: base, To: base + 3600}
	labels := func(series []*probev1.ProbeSeries) map[uint64]string {
		out := map[uint64]string{}
		for _, s := range series {
			out[s.GetTaskId()] = fmt.Sprintf("%v %q", s.GetKind(), s.GetTarget())
		}
		return out
	}
	public, err := h.publicClient().QueryProbes(t.Context(), connect.NewRequest(req))
	if err != nil {
		t.Fatal(err)
	}
	admin, err := h.admin.QueryProbes(t.Context(), connect.NewRequest(req))
	if err != nil {
		t.Fatal(err)
	}
	wantPublic := map[uint64]string{kept: `PROBE_KIND_ICMP "192.0.2.1"`, moved: `PROBE_KIND_UNSPECIFIED ""`}
	wantAdmin := map[uint64]string{kept: `PROBE_KIND_ICMP "192.0.2.1"`, moved: `PROBE_KIND_ICMP "10.0.0.5"`}
	if got := labels(public.Msg.GetSeries()); !maps.Equal(got, wantPublic) {
		t.Errorf("public labels = %v, want %v", got, wantPublic)
	}
	if got := labels(admin.Msg.GetSeries()); !maps.Equal(got, wantAdmin) {
		t.Errorf("admin labels = %v, want %v", got, wantAdmin)
	}
}
```

- [ ] **Step 3: 跑红**

```bash
cd /Users/xjetry/work/vibe/probe-public && go test -count=1 ./internal/hub/store/ ./internal/hub/api/ > /tmp/m5-t5-red.log 2>&1; echo $?
```

Expected：1。编译失败：`ListPublicNodes`、`NodeIsPublic`、`NewPublic`、`PublicConfig`、`newProjection` 未定义。

- [ ] **Step 4: 实现 store**

`internal/hub/store/node.go`：
- 在 `selectNodes` 之后加：

```go
// nodeOrder 是节点列表唯一的排序：面板与公开页看到同一个顺序。
const nodeOrder = " ORDER BY n.sort_order, n.id"
```

- `ListNodes` 的查询改为 `selectNodes+nodeOrder`。
- 在 `ListNodes` 之后加：

```go
// ListPublicNodes 只返回 public = 1 的节点。公开服务只经它与 NodeIsPublic 读节点：可见范围由这两处的 WHERE 承载。
func (s *Store) ListPublicNodes(ctx context.Context) ([]Node, error) {
	rows, err := s.r.QueryContext(ctx, selectNodes+" WHERE n.public = 1"+nodeOrder)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanNodes(rows)
}

// NodeIsPublic 对未公开的节点与不存在的节点同样返回 false：调用方无从、也不需要区分二者。
func (s *Store) NodeIsPublic(ctx context.Context, id int64) (bool, error) {
	var public bool
	err := s.r.QueryRowContext(ctx, "SELECT public FROM node WHERE id = ?", id).Scan(&public)
	if err == sql.ErrNoRows {
		return false, nil
	}
	return public, err
}
```

- [ ] **Step 5: 实现共用的历史与在线判定**

`internal/hub/probe/registry.go`，放在 `Target` 之后：

```go
// TargetFor 是公开端标注历史序列的口径：只对当前分配给 nodeID 的任务给出种类与目标，其余 ok 为 false。
// 节点公开即公开它正在探测的目标；历史里出现、但现在不分配给该节点的任务，当前目标可能从未被该节点探测过
// （撤下后改成了内网地址、只分配给私有节点），不在公开范围内。分配与目标在同一个读锁下读出：分开两次加锁，
// 中间的 Save 可能让"已分配"与"新目标"拼在一起。
func (r *Registry) TargetFor(nodeID int64, id uint64) (kind probev1.ProbeKind, target string, ok bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	t, known := r.tasks[id]
	if _, assigned := r.byNode[nodeID][id]; !known || !assigned {
		return probev1.ProbeKind_PROBE_KIND_UNSPECIFIED, "", false
	}
	return t.Kind, t.Target, true
}
```

`internal/hub/api/history.go`：

```go
package api

import (
	"context"
	"log/slog"
	"time"

	"google.golang.org/protobuf/proto"

	probev1 "github.com/xjetry/probe/gen/probe/v1"
	"github.com/xjetry/probe/internal/hub/live"
	"github.com/xjetry/probe/internal/hub/metric"
	"github.com/xjetry/probe/internal/hub/store"
)

const (
	defaultMaxPoints = 720
	maxMaxPoints     = 2000
	maxQuerySpan     = 400 * 24 * time.Hour
)

// history 是两族历史查询的唯一实现，管理端与公开端共用。两端在两处不同，都由调用方决定：
// 哪些节点可查（Service.requireNode / Public.requirePublic 先行裁决，这里不再看节点），
// 以及探测序列怎样标注（taskLabel）。
type history struct {
	store *store.Store
	log   *slog.Logger
}

// taskLabel 给出任务的种类与目标；ok 为 false 时两项留空，客户端退回编号。管理端传 probe.Registry.Target
// （任务当前的配置），公开端传 probe.Registry.TargetFor（只标当前分配给被查节点的任务）。
type taskLabel func(taskID uint64) (kind probev1.ProbeKind, target string, ok bool)

// checkWindow 为两族查询、两个服务维持同一套窗口与点数约束；只看请求本身，不查库。
func checkWindow(from, to int64, requested uint32) (int, error) {
	if from < 0 {
		return 0, invalid("from must be a nonnegative Unix timestamp; got %d", from)
	}
	if from >= to {
		return 0, invalid("from (%d) must be earlier than to (%d)", from, to)
	}
	// 两族水位从 Unix epoch 开始；非负秒差直接比较，避免转换纳秒时溢出。
	if span := to - from; span > int64(maxQuerySpan/time.Second) {
		return 0, invalid("window spans %d seconds; the maximum is %d (400 days)", to-from, int64(maxQuerySpan/time.Second))
	}
	maxPoints := int(requested)
	if maxPoints == 0 {
		maxPoints = defaultMaxPoints
	}
	if maxPoints > maxMaxPoints {
		return 0, invalid("max_points must be at most %d; got %d", maxMaxPoints, maxPoints)
	}
	return maxPoints, nil
}

func (h history) metrics(ctx context.Context, m *probev1.QueryMetricsRequest, maxPoints int) (*probev1.QueryMetricsResponse, error) {
	lv, step := store.ChooseLevel(m.GetFrom(), m.GetTo(), maxPoints)
	rows, err := h.store.QueryMetrics(ctx, m.GetNodeId(), m.GetFrom(), m.GetTo(), lv, step)
	if err != nil {
		h.log.Error("metric query failed", "err", err)
		return nil, internalError("metric query failed")
	}
	resp := &probev1.QueryMetricsResponse{Level: lv.Name, StepS: uint32(step)}
	series := make([]*probev1.MetricSeries, len(metric.Columns))
	for i, c := range metric.Columns {
		series[i] = &probev1.MetricSeries{Name: c.Name, Unit: c.Unit, Samples: make([]*probev1.MetricSample, 0, len(rows))}
	}
	for _, r := range rows {
		resp.Ts = append(resp.Ts, r.TS)
		for i, c := range metric.Columns {
			sample := &probev1.MetricSample{N: r.Bucket.N[i]}
			switch {
			case c.Kind == metric.Sum:
				// 可加量下发和，不下发均值：一分钟内的字节数除以入账次数没有意义。
				if r.Bucket.N[i] > 0 {
					sample.Sum = proto.Float64(r.Bucket.Sum[i])
				}
			default:
				if mean, ok := r.Bucket.Mean(i); ok {
					sample.Mean = proto.Float64(mean)
					if c.Kind == metric.MeanMax {
						sample.Max = proto.Float64(r.Bucket.Max[i])
					}
				}
			}
			series[i].Samples = append(series[i].Samples, sample)
		}
	}
	resp.Series = series
	return resp, nil
}

func (h history) probeSeries(ctx context.Context, m *probev1.QueryProbesRequest, maxPoints int, label taskLabel) (*probev1.QueryProbesResponse, error) {
	lv, step := store.ChooseLevel(m.GetFrom(), m.GetTo(), maxPoints)
	rows, err := h.store.QueryProbes(ctx, m.GetNodeId(), m.GetFrom(), m.GetTo(), lv, step)
	if err != nil {
		h.log.Error("probe query failed", "err", err)
		return nil, internalError("probe query failed")
	}
	resp := &probev1.QueryProbesResponse{Level: lv.Name, StepS: uint32(step)}
	var cur *probev1.ProbeSeries
	for _, r := range rows { // store 已按 TaskID、TS 排序
		if r.Bucket.Sent == 0 {
			continue
		}
		if cur == nil || cur.TaskId != r.TaskID {
			cur = &probev1.ProbeSeries{TaskId: r.TaskID}
			cur.Kind, cur.Target, _ = label(r.TaskID)
			resp.Series = append(resp.Series, cur)
		}
		sample := &probev1.ProbeSample{Ts: r.TS, Sent: r.Bucket.Sent, Lost: r.Bucket.Lost, Errors: r.Bucket.Errors}
		if mean, ok := r.Bucket.RttMean(); ok {
			sample.RttMeanUs, sample.RttMinUs, sample.RttMaxUs = proto.Uint32(mean), proto.Uint32(r.Bucket.RttMinUs), proto.Uint32(r.Bucket.RttMaxUs)
		}
		cur.Samples = append(cur.Samples, sample)
	}
	return resp, nil
}

// liveState 是两端快照共用的在线判定：在线只来自 live；库里的 last_seen_at 只在 live 没有该节点
// （hub 重启后尚未再上报）时用来展示"上次见到"，不参与在线判定。
func liveState(l *live.Live, n store.Node) (online bool, lastSeen *int64, m *probev1.Metrics) {
	if e, ok := l.Get(n.ID); ok {
		return e.Online, proto.Int64(e.LastSeenWall.Unix()), e.Metrics
	}
	if !n.LastSeenAt.IsZero() {
		return false, proto.Int64(n.LastSeenAt.Unix()), nil
	}
	return false, nil, nil
}
```

`internal/hub/api/data.go` 整份替换：

```go
package api

import (
	"context"
	"time"

	"connectrpc.com/connect"

	probev1 "github.com/xjetry/probe/gen/probe/v1"
)

// GetSnapshot 的在线判定见 liveState。
func (s *Service) GetSnapshot(ctx context.Context, _ *connect.Request[probev1.GetSnapshotRequest]) (*connect.Response[probev1.GetSnapshotResponse], error) {
	nodes, err := s.store.ListNodes(ctx)
	if err != nil {
		s.log.Error("listing nodes failed", "err", err)
		return nil, internalError("listing nodes failed")
	}
	out := &probev1.GetSnapshotResponse{Now: s.clk.Now().Unix(), ReportIntervalMs: uint32(s.cfg.ReportInterval / time.Millisecond), HubVersion: s.cfg.HubVersion}
	for _, n := range nodes {
		st := &probev1.NodeStatus{Id: n.ID, Name: n.Name, Traffic: trafficProto(s.traffic.View(n.ID))}
		st.Online, st.LastSeenAt, st.Metrics = liveState(s.live, n)
		out.Nodes = append(out.Nodes, st)
	}
	return connect.NewResponse(out), nil
}

func (s *Service) QueryMetrics(ctx context.Context, req *connect.Request[probev1.QueryMetricsRequest]) (*connect.Response[probev1.QueryMetricsResponse], error) {
	m := req.Msg
	maxPoints, err := checkWindow(m.GetFrom(), m.GetTo(), m.GetMaxPoints())
	if err != nil {
		return nil, err
	}
	if err := s.requireNode(ctx, m.GetNodeId()); err != nil {
		return nil, err
	}
	resp, err := s.history.metrics(ctx, m, maxPoints)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(resp), nil
}

// requireNode 是管理端历史查询的节点准入：不存在即 NotFound，错误写出 id。
func (s *Service) requireNode(ctx context.Context, id int64) error {
	exists, err := s.store.NodeExists(ctx, id)
	if err != nil {
		s.log.Error("looking up node failed", "err", err)
		return internalError("looking up node failed")
	}
	if !exists {
		return notFound(id)
	}
	return nil
}
```

`internal/hub/api/probes.go`：
- `QueryProbes` 整个函数替换为下面这段。
- 删去不再使用的 `google.golang.org/protobuf/proto` 与 `internal/hub/store` 两个 import。

```go
func (s *Service) QueryProbes(ctx context.Context, req *connect.Request[probev1.QueryProbesRequest]) (*connect.Response[probev1.QueryProbesResponse], error) {
	m := req.Msg
	maxPoints, err := checkWindow(m.GetFrom(), m.GetTo(), m.GetMaxPoints())
	if err != nil {
		return nil, err
	}
	if err := s.requireNode(ctx, m.GetNodeId()); err != nil {
		return nil, err
	}
	resp, err := s.history.probeSeries(ctx, m, maxPoints, s.probes.Target)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(resp), nil
}
```

`internal/hub/api/service.go`：
- `Service` 结构体在 `log` 之后加 `history history`。
- `New` 的字面量加 `history: history{store: st, log: log},`。管理端的探测标签口径（`s.probes.Target`）在 `QueryProbes` 里传给 `probeSeries`。

- [ ] **Step 6: 实现投影与公开服务**

`internal/hub/api/projection.go`：

```go
package api

import (
	"fmt"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
)

// projection 把一种消息按字段名投影成它的公开形态。字段集合由目标（公开）消息决定：源消息以后新增的字段
// 不会出现在公开消息里，除非公开消息也声明它——"默认私有"由此落在类型上，而不是一处可能漏改的逐字段拷贝。
// newProjection 逐字段核对名字、类型、基数与 presence，任一不符即 panic：描述符来自生成代码，
// 只有改了 proto 却没对齐时才会发生，那时 hub 在构造 Public 时就起不来。
type projection struct {
	dst    protoreflect.MessageType
	fields [][2]protoreflect.FieldDescriptor // {目标字段, 源字段}
}

func newProjection(dst protoreflect.MessageType, src protoreflect.MessageDescriptor) projection {
	p := projection{dst: dst}
	fields := dst.Descriptor().Fields()
	for i := 0; i < fields.Len(); i++ {
		d := fields.Get(i)
		s := src.Fields().ByName(d.Name())
		switch {
		case s == nil:
			panic(fmt.Sprintf("%s has no counterpart named %s in %s", d.FullName(), d.Name(), src.FullName()))
		case !projectable(d) || !projectable(s):
			panic(fmt.Sprintf("%s: only singular scalar fields can be projected", d.FullName()))
		case d.Kind() != s.Kind() || d.HasPresence() != s.HasPresence():
			panic(fmt.Sprintf("%s (%v, presence %v) does not match %s (%v, presence %v)",
				d.FullName(), d.Kind(), d.HasPresence(), s.FullName(), s.Kind(), s.HasPresence()))
		}
		p.fields = append(p.fields, [2]protoreflect.FieldDescriptor{d, s})
	}
	return p
}

// projectable 限于单值标量：消息、枚举、列表与 map 的逐项语义各不相同，公开消息目前只需要标量。
func projectable(f protoreflect.FieldDescriptor) bool {
	if f.Cardinality() == protoreflect.Repeated {
		return false
	}
	switch f.Kind() {
	case protoreflect.MessageKind, protoreflect.GroupKind, protoreflect.EnumKind:
		return false
	}
	return true
}

// apply 只复制源里存在的字段：optional 缺失仍是缺失，显式的 0 仍是 0。
func (p projection) apply(src proto.Message) proto.Message {
	sm := src.ProtoReflect()
	out := p.dst.New()
	for _, f := range p.fields {
		if sm.Has(f[1]) {
			out.Set(f[0], sm.Get(f[1]))
		}
	}
	return out.Interface()
}
```

`internal/hub/api/public.go`：

```go
package api

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/netip"
	"time"

	"connectrpc.com/connect"

	probev1 "github.com/xjetry/probe/gen/probe/v1"
	"github.com/xjetry/probe/gen/probe/v1/probev1connect"
	"github.com/xjetry/probe/internal/clock"
	"github.com/xjetry/probe/internal/hub/live"
	"github.com/xjetry/probe/internal/hub/probe"
	"github.com/xjetry/probe/internal/hub/store"
	"github.com/xjetry/probe/internal/hub/traffic"
)

// publicMaxBody 是公开请求的解码预算。公开请求的字段只有历史查询的几个数值与节点 id，正常客户端发出的
// JSON 远小于 4 KiB；connect 的 JSON 解码接受任意空白与未知字段，所以没有"最大合法请求"这样的上界，
// 这个数只是对匿名请求体的有界约束。
const publicMaxBody = 4 << 10

type PublicConfig struct {
	// ReportInterval 原样经 PublicSnapshot.report_interval_ms 下发。
	ReportInterval time.Duration
	// TrustedProxies 决定限流按哪个来源地址计：只有来自这些对端的 X-Forwarded-For 才被采信；空表示一个都不信。
	TrustedProxies []netip.Prefix
}

// Public 实现 PublicService。它不经会话或 token：挂载点只绑定按来源地址的限流与缓存头（Handler）。
// 可见范围由 store 的 ListPublicNodes 与 NodeIsPublic 承载，节点是否公开在每个请求里现读库；
// 进程里只有 GetSnapshot 的响应字节有 snapshotTTL 的缓存窗口。
type Public struct {
	cfg     PublicConfig
	store   *store.Store
	live    *live.Live
	traffic *traffic.Book
	probes  *probe.Registry
	history history
	clk     clock.Clock
	log     *slog.Logger

	facts   projection
	metrics projection
}

func NewPublic(cfg PublicConfig, st *store.Store, l *live.Live, book *traffic.Book, probes *probe.Registry, clk clock.Clock, log *slog.Logger) *Public {
	return &Public{
		cfg: cfg, store: st, live: l, traffic: book, probes: probes, clk: clk, log: log,
		history: history{store: st, log: log},
		facts:   newProjection((&probev1.PublicFacts{}).ProtoReflect().Type(), (&probev1.Facts{}).ProtoReflect().Descriptor()),
		metrics: newProjection((&probev1.PublicMetrics{}).ProtoReflect().Type(), (&probev1.Metrics{}).ProtoReflect().Descriptor()),
	}
}

// connectHandler 是不带挂载点中间件的处理器。
func (p *Public) connectHandler() (string, http.Handler) {
	return probev1connect.NewPublicServiceHandler(p, connect.WithReadMaxBytes(publicMaxBody))
}

// Handler 是公开服务唯一的挂载点。
func (p *Public) Handler() (string, http.Handler) { return p.connectHandler() }

// noPublicNode 对未公开与不存在的节点是同一个错误：文案不带 id，两种情形的响应逐字节相同，
// 匿名调用方无从由错误区分"存在但未公开"与"不存在"。
func noPublicNode() error {
	return connect.NewError(connect.CodeNotFound, errors.New("node_id: no public node has this id"))
}

// requirePublic 是公开端历史查询的节点准入。
func (p *Public) requirePublic(ctx context.Context, id int64) error {
	public, err := p.store.NodeIsPublic(ctx, id)
	if err != nil {
		p.log.Error("looking up node failed", "err", err)
		return internalError("looking up node failed")
	}
	if !public {
		return noPublicNode()
	}
	return nil
}

func (p *Public) GetSite(ctx context.Context, _ *connect.Request[probev1.GetSiteRequest]) (*connect.Response[probev1.PublicSite], error) {
	st, err := p.store.SiteSettings(ctx)
	if err != nil {
		p.log.Error("reading settings failed", "err", err)
		return nil, internalError("reading settings failed")
	}
	return connect.NewResponse(&probev1.PublicSite{Title: st.Title, Theme: st.Theme, AccentColor: st.AccentColor, Logo: st.Logo, CustomCss: st.CustomCSS}), nil
}

func (p *Public) GetSnapshot(ctx context.Context, _ *connect.Request[probev1.PublicServiceGetSnapshotRequest]) (*connect.Response[probev1.PublicSnapshot], error) {
	nodes, err := p.store.ListPublicNodes(ctx)
	if err != nil {
		p.log.Error("listing public nodes failed", "err", err)
		return nil, internalError("listing nodes failed")
	}
	out := &probev1.PublicSnapshot{Now: p.clk.Now().Unix(), ReportIntervalMs: uint32(p.cfg.ReportInterval / time.Millisecond)}
	for _, n := range nodes {
		online, seen, m := liveState(p.live, n)
		pn := &probev1.PublicNode{Id: n.ID, Name: n.Name, Online: online, LastSeenAt: seen, SortOrder: n.SortOrder, Traffic: trafficProto(p.traffic.View(n.ID))}
		if n.Facts != nil {
			pn.Facts = p.facts.apply(n.Facts).(*probev1.PublicFacts)
		}
		if m != nil {
			pn.Metrics = p.metrics.apply(m).(*probev1.PublicMetrics)
		}
		out.Nodes = append(out.Nodes, pn)
	}
	return connect.NewResponse(out), nil
}

func (p *Public) QueryMetrics(ctx context.Context, req *connect.Request[probev1.QueryMetricsRequest]) (*connect.Response[probev1.QueryMetricsResponse], error) {
	m := req.Msg
	maxPoints, err := checkWindow(m.GetFrom(), m.GetTo(), m.GetMaxPoints())
	if err != nil {
		return nil, err
	}
	if err := p.requirePublic(ctx, m.GetNodeId()); err != nil {
		return nil, err
	}
	resp, err := p.history.metrics(ctx, m, maxPoints)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(resp), nil
}

func (p *Public) QueryProbes(ctx context.Context, req *connect.Request[probev1.QueryProbesRequest]) (*connect.Response[probev1.QueryProbesResponse], error) {
	m := req.Msg
	maxPoints, err := checkWindow(m.GetFrom(), m.GetTo(), m.GetMaxPoints())
	if err != nil {
		return nil, err
	}
	if err := p.requirePublic(ctx, m.GetNodeId()); err != nil {
		return nil, err
	}
	node := m.GetNodeId()
	resp, err := p.history.probeSeries(ctx, m, maxPoints, func(id uint64) (probev1.ProbeKind, string, bool) {
		return p.probes.TargetFor(node, id)
	})
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(resp), nil
}
```

- [ ] **Step 7: 挂到 hub 的 mux，匿名白名单改为 PublicService**

`PublicService` 一进注册表，`cmd/hub` 的匿名枚举测试就会调到它。挂载与白名单必须在同一个提交里。

`cmd/hub/mux_test.go`：
- `anonymousProcedures` 整个替换为：

```go
// publicProcedures 是匿名可达的全部过程：只有 PublicService（§12）。Register 与 Login 的凭据在请求体里，
// 用 {} 调用时由方法体返回 Unauthenticated（没有注册窗口、没有管理员），与其他过程一样断言 401。
var publicProcedures = map[string]bool{
	"/probe.v1.PublicService/GetSite":      true,
	"/probe.v1.PublicService/GetSnapshot":  true,
	"/probe.v1.PublicService/QueryMetrics": true,
	"/probe.v1.PublicService/QueryProbes":  true,
}
```

- `newTestMux` 在 `admin := api.New(...)` 之后加 `pub := api.NewPublic(api.PublicConfig{ReportInterval: 10 * time.Second}, st, l, book, reg, clk, slog.Default())`，返回语句改为：

```go
	return newMux(mountOf(svc.Handler()), mountOf(admin.Handler()), mountOf(pub.Handler()), mountOf(web.Prefix, web.Handler()), mountOf("/", web.RootRedirect()))
```

- `TestMuxRejectsAnonymousProcedures`：
  - 子用例里的 `if anonymousProcedures[path] { return }` 改为：

```go
					if publicProcedures[path] {
						// 404 说明没挂载（落到了根路径），401 说明被鉴权挡住：两者都不是匿名可达。
						if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusNotFound {
							t.Fatalf("%s: status %d, want the public service to answer anonymously", path, resp.StatusCode)
						}
						return
					}
```

  - 末尾的循环改为遍历 `publicProcedures`。
- 文件末尾追加：

```go
// 无副作用标注决定一个过程是否接受 GET（§3.3），只有 PublicService 标了：GET 到其余过程一律 405，
// 这是 §5.3 的 CSRF 事实之一；公开过程接受 GET，浏览器与中间缓存才能按 Cache-Control 复用响应。
func TestMuxAcceptsGETOnlyOnPublicService(t *testing.T) {
	srv := httptest.NewServer(newTestMux(t))
	t.Cleanup(srv.Close)
	count := 0
	protoregistry.GlobalFiles.RangeFiles(func(file protoreflect.FileDescriptor) bool {
		if file.Package() != "probe.v1" {
			return true
		}
		for i := 0; i < file.Services().Len(); i++ {
			svc := file.Services().Get(i)
			for j := 0; j < svc.Methods().Len(); j++ {
				path := "/" + string(svc.FullName()) + "/" + string(svc.Methods().Get(j).Name())
				count++
				resp, err := srv.Client().Get(srv.URL + path + "?connect=v1&encoding=json&message=%7B%7D")
				if err != nil {
					t.Fatal(err)
				}
				resp.Body.Close()
				switch public := svc.FullName() == "probe.v1.PublicService"; {
				case public && resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusBadRequest:
					t.Errorf("%s: GET status %d, want the public service to answer (200, or 400 for an empty window)", path, resp.StatusCode)
				case !public && resp.StatusCode != http.StatusMethodNotAllowed:
					t.Errorf("%s: GET status %d, want 405", path, resp.StatusCode)
				}
			}
		}
		return true
	})
	if count == 0 {
		t.Fatal("enumerated no procedures")
	}
}
```

`cmd/hub/serve_test.go` 末尾追加：

```go
// serve 的装配与 newTestMux 各写一份：这里经真实 serve 调一次公开服务，挂载遗漏不会只在 mux 测试里被掩盖。
func TestServeMountsPublicService(t *testing.T) {
	db := filepath.Join(t.TempDir(), "hub.db")
	url, _, _ := startTestHub(t, db, clock.NewFake(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)))
	resp, err := http.Get(url + "/probe.v1.PublicService/GetSite?connect=v1&encoding=json&message=%7B%7D")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var site struct {
		Theme string `json:"theme"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&site); err != nil || resp.StatusCode != http.StatusOK || site.Theme != "auto" {
		t.Fatalf("GetSite via serve: %d %+v %v", resp.StatusCode, site, err)
	}
}
```

（所需的 `encoding/json`、`net/http`、`path/filepath`、`time`、`clock` 都已在 `serve_test.go` 的 import 里。）

`cmd/hub/serve.go`：`admin := api.New(...)` 之后加一行，`newMux` 一行改为：

```go
	pub := api.NewPublic(api.PublicConfig{ReportInterval: svc.Interval(), TrustedProxies: trusted}, st, l, book, reg, clk, log)

	mux := newMux(mountOf(svc.Handler()), mountOf(admin.Handler()), mountOf(pub.Handler()), mountOf(web.Prefix, web.Handler()), mountOf("/", web.RootRedirect()))
```

- [ ] **Step 8: 跑绿**

```bash
cd /Users/xjetry/work/vibe/probe-public && go test -count=1 ./... > /tmp/m5-t5-green.log 2>&1; echo $?
cd /Users/xjetry/work/vibe/probe-public && go vet ./... > /tmp/m5-t5-vet.log 2>&1; echo $?
```

Expected：两条都是 0。以下管理端测试照常通过，说明重构没有改变管理端行为：
- `TestProbeAndMetricQueriesShareWindowValidation`（错误仍是 `node 999 does not exist`）
- `TestQueryProbesGroupsPerTaskAndOmitsEmptyPoints`
- 快照相关测试

- [ ] **Step 9: 提交**

```bash
cd /Users/xjetry/work/vibe/probe-public && git add proto buf.yaml gen web/src/gen internal/hub/store internal/hub/api cmd/hub && git commit -m "api: PublicService 的四个方法，公开消息由描述符投影生成" -m "公开端与管理端共用历史查询、窗口校验与在线判定，只在节点准入与探测标签上不同：未公开与不存在的节点得到同一个错误，文案不带 id，响应逐字节相同；公开端只给当前分配给该节点的任务标种类与目标，撤下后改了目标的任务不会把新目标带到公开节点上。PublicFacts 与 PublicMetrics 沿用源消息的字段号，缺的号与名保留；投影按公开消息声明的字段复制，源消息新增的字段不会自动公开，构造时核对名字、类型与 presence。响应可达的每个消息的字段集合由测试里的允许列表钉住，共用的 Traffic 与历史查询类型同样在列。缓存上界以 cache_max_age_s 方法选项写在 proto 里。挂载点的匿名白名单只剩 PublicService 的四个过程，且断言它们确实匿名可达；只有它们接受 GET。" > /tmp/m5-t5-commit.log 2>&1; echo $?
```

Expected：0。

- [ ] **Step 10: 缺陷注入**

| | 改动 | 命令 | 期望 |
|---|---|---|---|
| a | `ListPublicNodes` 去掉 `WHERE n.public = 1` | `go test -count=1 -run 'TestPublicNodeQueries\|TestPublicSnapshot' ./internal/hub/store/ ./internal/hub/api/ > /tmp/m5-t5-inj-a.log 2>&1; echo $?` | 1，`public nodes = [c b a]`；快照有 3 个节点 |
| b | `requirePublic` 改为先 `NodeExists`：不存在时返回 `notFound(id)`，存在而未公开时返回 `noPublicNode()` | `go test -count=1 -run TestPublicHistoryTreats ./internal/hub/api/ > /tmp/m5-t5-inj-b.log 2>&1; echo $?` | 1，private 与 missing 的正文不同（`node 999999 does not exist`） |
| c | `public.proto` 的 `PublicFacts` 删去两行 `reserved`，加 `string hostname = 1;`，再 `make gen` | `go test -count=1 -run 'TestPublicResponsesExpose\|TestPublicSnapshot' ./internal/hub/api/ > /tmp/m5-t5-inj-c.log 2>&1; echo $?` | 1，`fields of probe.v1.PublicFacts = [arch cpu_cores cpu_model hostname os virtualization]`；快照用例红在 `reported node = … facts:{hostname:"secret-host" …}`（投影按名字自动复制了新字段）。还原：`git checkout -- proto gen web/src/gen` |
| d | `projection.apply` 去掉 `if sm.Has(f[1])` | `go test -count=1 -run TestProjectionKeeps ./internal/hub/api/ > /tmp/m5-t5-inj-d.log 2>&1; echo $?` | 1，projected 里出现 `cpu_pct:0` |
| e | `newProjection` 的比较去掉 `\|\| d.HasPresence() != s.HasPresence()` | `go test -count=1 -run TestNewProjectionRejects ./internal/hub/api/ > /tmp/m5-t5-inj-e.log 2>&1; echo $?` | 1，子用例 `WithPresence` 报 `no panic` |
| f | `publicFields` 删去 `"probe.v1.Traffic"` 一项 | `go test -count=1 -run TestPublicResponsesExpose ./internal/hub/api/ > /tmp/m5-t5-inj-f.log 2>&1; echo $?` | 1，`probe.v1.Traffic is reachable from PublicService but not in publicFields` |
| g | `serve.go` 的 `newMux` 去掉 `mountOf(pub.Handler())`，并在 `mux :=` 之前加一行 `_ = pub`（`pub` 只在这里用到，不加编译不过） | `go test -count=1 -run TestServeMountsPublicService ./cmd/hub/ > /tmp/m5-t5-inj-g.log 2>&1; echo $?` | 1，`GetSite via serve: 404`：请求落到根路径的重定向处理器，非 `/` 的路径一律 404 |
| h | `newTestMux` 去掉 `mountOf(pub.Handler())`，并在 `return` 之前加一行 `_ = pub`（`pub` 只在这里用到，不加编译不过） | `go test -count=1 -run 'TestMuxRejectsAnonymous\|TestMuxAcceptsGET' ./cmd/hub/ > /tmp/m5-t5-inj-h.log 2>&1; echo $?` | 1，四个 `PublicService` 过程报 `status 404, want the public service to answer` |
| i | 在 `admin.proto` 的 `GetSnapshot` 里加 `option idempotency_level = NO_SIDE_EFFECTS;`，再 `make gen` | `go test -count=1 -run TestMuxAcceptsGET ./cmd/hub/ > /tmp/m5-t5-inj-i.log 2>&1; echo $?` | 1，`/probe.v1.AdminService/GetSnapshot: GET status 401, want 405`。Task 6 之后同一改动更早失败：装配时 panic `probe.v1.AdminService.GetSnapshot accepts GET (idempotency_level = NO_SIDE_EFFECTS) but does not declare a positive probe.v1.cache_max_age_s`。还原：`git checkout -- proto gen web/src/gen` |
| j | `Public.QueryProbes` 删去 `node := m.GetNodeId()`，标签函数改传 `p.probes.Target`（管理端口径） | `go test -count=1 -run TestPublicProbeLabels ./internal/hub/api/ > /tmp/m5-t5-inj-j.log 2>&1; echo $?` | 1，`public labels = map[1:PROBE_KIND_ICMP "192.0.2.1" 2:PROBE_KIND_ICMP "10.0.0.5"]`：撤下后改成内网地址的目标出现在公开端 |
| k | `TargetFor` 的判定改成 `if !known {`（不看分配） | 同 j | 1，同 j |
| l | `Public.QueryProbes` 的 `checkWindow(m.GetFrom(), m.GetTo(), m.GetMaxPoints())` 改成 `checkWindow(m.GetFrom(), m.GetTo(), 0)` | `go test -count=1 -run TestPublicHistorySharesWindow ./internal/hub/api/ > /tmp/m5-t5-inj-l.log 2>&1; echo $?` | 1，子用例 `points` 报 `error=<nil> want=invalid_argument "max_points must be at most 2000; got 2001"` |
| m | `ListPublicNodes` 的排序改成 `selectNodes+" WHERE n.public = 1 ORDER BY n.id"` | `go test -count=1 -run 'TestPublicNodeQueries\|TestPublicSnapshot' ./internal/hub/store/ ./internal/hub/api/ > /tmp/m5-t5-inj-m.log 2>&1; echo $?` | 1，store 红在 `public nodes = [a c], want c,a in panel order`；api 红在 `nodes not in panel order` |
| n | `GetSnapshot` 里 `online, seen, m := liveState(p.live, n)` 改成 `online, _, m := liveState(p.live, n)` 并加一行 `seen := proto.Int64(n.LastSeenAt.Unix())` | `go test -count=1 -run TestPublicSnapshot ./internal/hub/api/ > /tmp/m5-t5-inj-n.log 2>&1; echo $?` | 1，`never-reported node = … last_seen_at:-62135596800 …` |
| o | `GetSnapshot` 里 `Traffic: trafficProto(p.traffic.View(n.ID))` 改成 `Traffic: nil` | 同 n | 1，`never-reported node = id:3 name:"c"`（没有 traffic） |
| p | `GetSite` 的返回去掉 `CustomCss: st.CustomCSS` | `go test -count=1 -run TestPublicSite ./internal/hub/api/ > /tmp/m5-t5-inj-p.log 2>&1; echo $?` | 1，`site = … want …`：保存的 CSS 没有下发 |
| q | `newProjection` 按字段号找源字段：`src.Fields().ByName(d.Name())` 改成 `src.Fields().ByNumber(d.Number())` | `go test -count=1 -run TestNewProjectionRejects ./internal/hub/api/ > /tmp/m5-t5-inj-q.log 2>&1; echo $?` | 1，子用例 `Renamed` 报 `no panic; want one mentioning "has no counterpart named z"` |
| r | `newProjection` 的比较去掉 `d.Kind() != s.Kind() \|\|` | 同 q | 1，子用例 `Wider` 报 `no panic` |
| s | `projectable` 去掉 `Repeated` 的判断 | 同 q | 1，子用例 `Repeated` 报 `no panic` |
| t | `connectHandler` 给公开处理器加一个一律返回 `Unauthenticated` 的拦截器（`connect.WithInterceptors(connect.UnaryInterceptorFunc(…))`） | `go test -count=1 -run TestMuxRejectsAnonymous ./cmd/hub/ > /tmp/m5-t5-inj-t.log 2>&1; echo $?` | 1，四个 `PublicService` 过程报 `status 401, want the public service to answer anonymously` |
| u | `probes.go` 的标签函数改成公开端的口径：`s.history.probeSeries(ctx, m, maxPoints, func(id uint64) (probev1.ProbeKind, string, bool) { return s.probes.TargetFor(m.GetNodeId(), id) })` | `go test -count=1 -run TestPublicProbeLabels ./internal/hub/api/ > /tmp/m5-t5-inj-u.log 2>&1; echo $?` | 1，只有 `admin labels = map[… 2:PROBE_KIND_UNSPECIFIED ""]` 一行：管理端对撤下的任务仍要标注当前目标 |
| v | `Public.QueryMetrics` 在 `requirePublic` 之后直接 `return connect.NewResponse(&probev1.QueryMetricsResponse{}), nil`（并加 `_ = maxPoints`） | `go test -count=1 -run TestPublicHistoryMatchesAdmin ./internal/hub/api/ > /tmp/m5-t5-inj-v.log 2>&1; echo $?` | 1，`public QueryMetrics = ` 为空，管理端有一行 |
| w | `Public.QueryProbes` 在 `probeSeries` 之后加 `if resp != nil { for _, s := range resp.Series { s.Samples = nil } }`（序列在、样本丢了） | 同 v | 1，`public QueryProbes = level:"1m" step_s:60 series:{task_id:1 kind:PROBE_KIND_ICMP target:"192.0.2.1"}`，没有 `samples` |
| x | `Public.QueryProbes` 的标签函数改成 `_, _ = node, id; return probev1.ProbeKind_PROBE_KIND_UNSPECIFIED, "", false`（一律不标注） | `go test -count=1 -run 'TestPublicHistoryMatchesAdmin\|TestPublicProbeLabels' ./internal/hub/api/ > /tmp/m5-t5-inj-x.log 2>&1; echo $?` | 1，`public QueryProbes = …series:{task_id:1 samples:…}` 没有种类与目标；`public labels = map[1:PROBE_KIND_UNSPECIFIED "" …]`：仍分配着的任务也丢了标签 |
| y | `public.proto` 的 `PublicMetrics` 删去 `reserved 1;` 与 `reserved "boot_id";` 两行，加 `string boot_id = 1;`，再 `make gen` | `go test -count=1 -run 'TestPublicResponsesExpose\|TestPublicSnapshot' ./internal/hub/api/ > /tmp/m5-t5-inj-y.log 2>&1; echo $?` | 1，`fields of probe.v1.PublicMetrics = [boot_id cpu_pct …]`；快照用例红在 `reported node = … metrics:{boot_id:"boot-secret" …}`：投影按字段名复制，公开消息多一个同名字段就把它带了出去。还原：`git checkout -- proto gen web/src/gen` |

每项：改动 → `git diff --stat` 非空 → 跑命令 → 核对原因 → `git checkout -- <文件>`。

---

### Task 6: 公开服务的挂载中间件：限流与缓存头

**Files:**
- Modify: `internal/hub/api/public.go`（`Public` 加 `limit`、`maxAge`；`Handler`；`cacheControl`；`cachePolicy`；`probeServices`）
- Test: `internal/hub/api/public_test.go`
- Modify: `proto/SKILL.md`（"公开数据"一节）
- Modify: `scripts/e2e.sh`（node1 标为公开）

**Interfaces:**
- Consumes：
  - Task 4 的 `ratelimit.New`、`ratelimit.BySource`
  - Task 5 的 `Public`、`connectHandler`、`probev1.E_CacheMaxAgeS`、测试辅助 `pubGet`/`pubPost`/`jsonQuery`
- Produces：
  - 常量 `publicBurst = 60`、`publicRefill = time.Second / 10`
  - `func cachePolicy(services []protoreflect.ServiceDescriptor) map[string]uint32`
  - `func probeServices() []protoreflect.ServiceDescriptor`
  - `func (p *Public) cacheControl(next http.Handler) http.Handler`
  - `Handler()` 返回 `cacheControl(ratelimit.BySource(connect))`，Task 7 在 `BySource` 与 connect 之间插入快照缓存。

- [ ] **Step 1: 写失败测试**

`internal/hub/api/public_test.go` 的 import 加 `"google.golang.org/protobuf/types/descriptorpb"`，文件末尾追加：

```go
func TestPublicRateLimitPerSourceAddress(t *testing.T) {
	h := newHarness(t, "127.0.0.0/8")
	site := jsonQuery("{}")
	from := func(ip string) map[string]string { return map[string]string{"X-Forwarded-For": ip} }
	// 60 与 100ms 是 spec §10 的字面值（桶容量 60、每秒补充 10），不引用常量：常量改了，这里要红。
	for i := range 60 {
		if got := pubGet(t, h, "GetSite", site, from("198.51.100.7")); got.status != http.StatusOK {
			t.Fatalf("request %d within the burst: %d %s", i+1, got.status, got.body)
		}
	}
	over := pubGet(t, h, "GetSite", site, from("198.51.100.7"))
	if over.status != http.StatusTooManyRequests || !bytes.Contains(over.body, []byte(`"code":"resource_exhausted"`)) || over.header.Get("Cache-Control") != "no-store" {
		t.Fatalf("request past the burst: %d %v %s", over.status, over.header, over.body)
	}
	if got := pubGet(t, h, "GetSite", site, from("198.51.100.8")); got.status != http.StatusOK {
		t.Fatalf("another source shares the bucket: %d", got.status)
	}
	h.clk.Advance(100 * time.Millisecond)
	if got := pubGet(t, h, "GetSite", site, from("198.51.100.7")); got.status != http.StatusOK {
		t.Fatalf("after one refill period: %d", got.status)
	}
	if got := pubGet(t, h, "GetSite", site, from("198.51.100.7")); got.status != http.StatusTooManyRequests {
		t.Fatalf("second request after one refill period: %d", got.status)
	}
	// 解码失败的请求同样计数：限流在 connect 之外裁决。
	for range 60 {
		pubGet(t, h, "GetSite", "connect=v1&encoding=json&message=%7B", from("198.51.100.9"))
	}
	if got := pubGet(t, h, "GetSite", site, from("198.51.100.9")); got.status != http.StatusTooManyRequests {
		t.Fatalf("malformed requests were not counted: %d", got.status)
	}
}

// 对端不是可信代理时 X-Forwarded-For 不被采信：改这个头换不了桶。
func TestPublicRateLimitIgnoresForwardedForFromUntrustedPeers(t *testing.T) {
	h := newHarness(t, "")
	for i := range 60 {
		pubGet(t, h, "GetSite", jsonQuery("{}"), map[string]string{"X-Forwarded-For": fmt.Sprintf("198.51.100.%d", i+1)})
	}
	if got := pubGet(t, h, "GetSite", jsonQuery("{}"), map[string]string{"X-Forwarded-For": "203.0.113.1"}); got.status != http.StatusTooManyRequests {
		t.Fatalf("spoofed X-Forwarded-For got a fresh bucket: %d", got.status)
	}
}

func TestPublicCacheControlPerMethod(t *testing.T) {
	h := newHarness(t, "")
	h.login(t)
	pub, _ := h.createNode(t, "pub")
	h.setPublic(t, pub, "pub", true)
	window := fmt.Sprintf(`{"nodeId":"%d","from":"0","to":"3600"}`, pub)
	for _, c := range []struct{ method, body, want string }{
		{"GetSite", "{}", "max-age=300"},
		{"GetSnapshot", "{}", "max-age=1"},
		{"QueryMetrics", window, "max-age=60"},
		{"QueryProbes", window, "max-age=60"},
	} {
		if got := pubGet(t, h, c.method, jsonQuery(c.body), nil); got.status != http.StatusOK || got.header.Get("Cache-Control") != c.want {
			t.Errorf("GET %s: %d Cache-Control %q, want %q", c.method, got.status, got.header.Get("Cache-Control"), c.want)
		}
		if got := pubPost(t, h, c.method, c.body, nil); got.status != http.StatusOK || got.header.Values("Cache-Control") != nil {
			t.Errorf("POST %s: %d Cache-Control %q, want none", c.method, got.status, got.header.Values("Cache-Control"))
		}
	}
	// 失败的 GET 一律 no-store：节点改回公开之后，浏览器不能继续用缓存里的 NotFound 挡住访客。
	h.setPublic(t, pub, "pub", false)
	if got := pubGet(t, h, "QueryMetrics", jsonQuery(window), nil); got.status != http.StatusNotFound || got.header.Get("Cache-Control") != "no-store" {
		t.Errorf("NotFound: %d Cache-Control %q", got.status, got.header.Get("Cache-Control"))
	}
	if got := pubGet(t, h, "QueryMetrics", jsonQuery(`{"from":"-1"}`), nil); got.status != http.StatusBadRequest || got.header.Get("Cache-Control") != "no-store" {
		t.Errorf("InvalidArgument: %d Cache-Control %q", got.status, got.header.Get("Cache-Control"))
	}
}

func TestCachePolicyRequiresGETAndMaxAgeTogether(t *testing.T) {
	opts := func(get bool, maxAge uint32, set bool) *descriptorpb.MethodOptions {
		o := &descriptorpb.MethodOptions{}
		if get {
			o.IdempotencyLevel = descriptorpb.MethodOptions_NO_SIDE_EFFECTS.Enum()
		}
		if set {
			proto.SetExtension(o, probev1.E_CacheMaxAgeS, maxAge)
		}
		return o
	}
	one := func(o *descriptorpb.MethodOptions) []protoreflect.ServiceDescriptor {
		return []protoreflect.ServiceDescriptor{syntheticService(t, o)}
	}
	expectPanic(t, "synthetic.S.Bare accepts GET", func() { cachePolicy(one(opts(true, 0, false))) })
	expectPanic(t, "synthetic.S.Bare accepts GET", func() { cachePolicy(one(opts(true, 0, true))) })
	expectPanic(t, "synthetic.S.Bare declares probe.v1.cache_max_age_s but does not accept GET", func() { cachePolicy(one(opts(false, 30, true))) })
	if got := cachePolicy(one(opts(false, 0, false))); len(got) != 0 {
		t.Fatalf("a POST-only method without a max-age: table = %v, want empty", got)
	}
	// 装配时核对的是 probe.v1 的全部服务，不只是 PublicService：枚举本身不能是空的。
	services := probeServices()
	var names []string
	for _, s := range services {
		names = append(names, string(s.FullName()))
	}
	slices.Sort(names)
	if want := []string{"probe.v1.AdminService", "probe.v1.AgentService", "probe.v1.PublicService"}; !slices.Equal(names, want) {
		t.Fatalf("probe.v1 services = %v, want %v", names, want)
	}
	got := cachePolicy(services)
	want := map[string]uint32{
		"/probe.v1.PublicService/GetSite": 300, "/probe.v1.PublicService/GetSnapshot": 1,
		"/probe.v1.PublicService/QueryMetrics": 60, "/probe.v1.PublicService/QueryProbes": 60,
	}
	if !maps.Equal(got, want) {
		t.Fatalf("table = %v, want %v", got, want)
	}
}
```

- [ ] **Step 2: 跑红**

```bash
cd /Users/xjetry/work/vibe/probe-public && go test -count=1 -run 'TestPublicRateLimit|TestPublicCacheControl|TestCachePolicy' ./internal/hub/api/ > /tmp/m5-t6-red.log 2>&1; echo $?
```

Expected：1。编译失败：`cachePolicy`、`probeServices` 未定义。

- [ ] **Step 3: 实现**

`internal/hub/api/public.go`：
- import 加 `"fmt"`、`"strconv"`、`"google.golang.org/protobuf/proto"`、`"google.golang.org/protobuf/reflect/protoreflect"`、`"google.golang.org/protobuf/reflect/protoregistry"`、`"google.golang.org/protobuf/types/descriptorpb"`、`"github.com/xjetry/probe/internal/hub/ratelimit"`。
- `publicMaxBody` 旁边加：

```go
const (
	// 每个来源的桶（IPv4 一个地址、IPv6 一个 /64，见 ratelimit.BySource）：容量 60、每秒补充 10（§10）。公开页每 2 秒轮询一次快照，一个访客开几个标签页仍远在其下。
	publicBurst  = 60
	publicRefill = time.Second / 10
)
```

- `Public` 结构体末尾加：

```go
	// limit 按来源计数，覆盖挂载点收到的每个请求。
	limit *ratelimit.Buckets[netip.Addr]
	// maxAge 是每个接受 GET 的过程的成功响应 max-age，构造时从描述符读出（cachePolicy），之后只读。
	maxAge map[string]uint32
```

- `NewPublic` 的字面量加：

```go
		limit:   ratelimit.New[netip.Addr](publicBurst, publicRefill),
		maxAge:  cachePolicy(probeServices()),
```

- `Handler` 替换为：

```go
// Handler 是公开服务唯一的挂载点：cacheControl(BySource(connect))。限流包在 connect 之外，解码失败的请求同样计数
// （ratelimit.BySource 的注释写了理由）；缓存头包在最外面，限流的 429 也带 no-store。
func (p *Public) Handler() (string, http.Handler) {
	path, h := p.connectHandler()
	return path, p.cacheControl(ratelimit.BySource(p.limit, p.cfg.TrustedProxies, p.clk, h))
}

// cacheControl 只作用于 GET：GET 的 URL 就是缓存键，浏览器与中间缓存可以复用；POST 响应不带缓存头。
// 成功响应按方法声明的 cache_max_age_s；失败响应（含限流的 429 与 NotFound）一律 no-store——节点改为公开后，
// 之前缓存的 NotFound 不能继续挡住访客。
func (p *Public) cacheControl(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			next.ServeHTTP(w, r)
			return
		}
		next.ServeHTTP(&cacheHeaderWriter{ResponseWriter: w, maxAge: p.maxAge[r.URL.Path]}, r)
	})
}

// cacheHeaderWriter 在状态码确定的那一刻写 Cache-Control：之后头已发出，改不了。maxAge 为 0 只出现在
// 注册表之外的路径上，connect 以 404 应答，落到 no-store。
type cacheHeaderWriter struct {
	http.ResponseWriter
	maxAge uint32
	wrote  bool
}

func (c *cacheHeaderWriter) WriteHeader(code int) {
	if !c.wrote {
		c.wrote = true
		v := "no-store"
		if code == http.StatusOK && c.maxAge > 0 {
			v = "max-age=" + strconv.FormatUint(uint64(c.maxAge), 10)
		}
		c.Header().Set("Cache-Control", v)
	}
	c.ResponseWriter.WriteHeader(code)
}

func (c *cacheHeaderWriter) Write(b []byte) (int, error) {
	if !c.wrote {
		c.WriteHeader(http.StatusOK)
	}
	return c.ResponseWriter.Write(b)
}

// Unwrap 让 http.ResponseController 找到底层 ResponseWriter 的能力。
func (c *cacheHeaderWriter) Unwrap() http.ResponseWriter { return c.ResponseWriter }

// cachePolicy 读出接受 GET 的方法的缓存上界，键为 Connect 过程路径，并在装配时核对不变式：一个方法接受 GET
// （idempotency_level = NO_SIDE_EFFECTS，protoc-gen-connect-go 据它生成 GET 支持）当且仅当它声明了正的
// cache_max_age_s。前者缺后者，GET 响应没有上界可写；后者缺前者，声明的上界永远用不上，多半是标错了方法。
// 任一方向不符即 panic，与准入表同一口径：不合的 proto 无法随 hub 启动（NewPublic 在 serve 装配时调用它）。
func cachePolicy(services []protoreflect.ServiceDescriptor) map[string]uint32 {
	table := map[string]uint32{}
	for _, svc := range services {
		methods := svc.Methods()
		for i := 0; i < methods.Len(); i++ {
			m := methods.Get(i)
			opts, _ := m.Options().(*descriptorpb.MethodOptions)
			get := opts.GetIdempotencyLevel() == descriptorpb.MethodOptions_NO_SIDE_EFFECTS
			v, _ := proto.GetExtension(m.Options(), probev1.E_CacheMaxAgeS).(uint32)
			switch {
			case get && v == 0:
				panic(fmt.Sprintf("%s accepts GET (idempotency_level = NO_SIDE_EFFECTS) but does not declare a positive probe.v1.cache_max_age_s", m.FullName()))
			case !get && v != 0:
				panic(fmt.Sprintf("%s declares probe.v1.cache_max_age_s but does not accept GET (idempotency_level is not NO_SIDE_EFFECTS)", m.FullName()))
			case get:
				table["/"+string(svc.FullName())+"/"+string(m.Name())] = v
			}
		}
	}
	return table
}

// probeServices 是 probe.v1 包里的全部服务。包里每个 proto 文件的生成代码都在 gen/probe/v1 这一个 Go 包里，
// 本包 import 了它，所以它们都已登记进 protoregistry.GlobalFiles。
func probeServices() []protoreflect.ServiceDescriptor {
	var out []protoreflect.ServiceDescriptor
	protoregistry.GlobalFiles.RangeFilesByPackage("probe.v1", func(fd protoreflect.FileDescriptor) bool {
		for i := 0; i < fd.Services().Len(); i++ {
			out = append(out, fd.Services().Get(i))
		}
		return true
	})
	return out
}
```

`proto/SKILL.md` 末尾追加：

````markdown

## 公开数据

标为公开的节点另经 `PublicService` 对外提供，不需要 token：只能查到公开节点，未公开与不存在的节点得到同一个 `not_found`。按来源限流（IPv4 一个地址、IPv6 一个 /64 算一个来源），每个来源瞬时 60 次、此后每秒 10 次，超出返回 `resource_exhausted`。方法与字段见 `probe/v1/public.proto`，都可以用 GET 调用，请求消息放在查询串里。

公开节点的实时状态：

```sh example
curl -fsS -G --data-urlencode 'connect=v1' --data-urlencode 'encoding=json' --data-urlencode 'message={}' \
  "$PROBE_HUB/probe.v1.PublicService/GetSnapshot" | jq '[(.nodes // [])[] | {id, name, online}]'
```
````

这个例子的消费方要同一个提交里跟上：e2e 的 `run_card_examples` 逐个执行卡片里的例子，重启后那一轮要求每个例子输出非空；现有 e2e 的节点全是私有的，这个例子会输出 `[]`。`scripts/e2e.sh` 改一处：

**改动 1.** `update_body` 把 node1 标为公开（原来是 `public: false`），并说明原因。

原文：

```sh
update_body=$(jq -nc --arg id "$node1" --arg name "e2e-amd64" '{id: $id, name: $name, public: false, note: "", trafficResetDay: 15, offlineGraceS: 0}')
```

改为：

```sh
# node1 标为公开：入口卡片"公开数据"的例子只列公开节点，重启后那一轮 run_card_examples 要求它输出非空。
update_body=$(jq -nc --arg id "$node1" --arg name "e2e-amd64" '{id: $id, name: $name, public: true, note: "", trafficResetDay: 15, offlineGraceS: 0}')
```

- [ ] **Step 4: 跑绿**

```bash
cd /Users/xjetry/work/vibe/probe-public && go test -count=1 ./internal/hub/api/ ./cmd/hub/ > /tmp/m5-t6-green.log 2>&1; echo $?
cd /Users/xjetry/work/vibe/probe-public && sh -n scripts/e2e.sh > /tmp/m5-t6-e2e-syntax.log 2>&1; echo $?
```

Expected：
- 两条都是 0。`TestApiReferenceServesTheRepositoryProtoAndGuide` 通过：卡片与三个新 proto 文件随 hub 下发。
- e2e 本任务不跑（一遍要几分钟）；这一处改动由 Task 12 的 `make e2e` 实跑验证：重启后那一轮 `card examples ok` 覆盖到这个例子。

- [ ] **Step 5: 提交**

```bash
cd /Users/xjetry/work/vibe/probe-public && git add internal/hub/api/public.go internal/hub/api/public_test.go proto/SKILL.md scripts/e2e.sh && git commit -m "api: 公开服务按来源地址限流，GET 响应按方法带缓存头" -m "限流用 Register 的同一个中间件 ratelimit.BySource（桶容量 60、每秒补充 10），在 connect 之外裁决，解码失败的请求同样计数；X-Forwarded-For 只在对端是可信代理时采信。测试用 spec 的字面值 60 与 100ms，常量改了会红。缓存上界在装配时核对：probe.v1 的每个方法接受 GET 当且仅当声明了正的 cache_max_age_s。GET 的成功响应带方法声明的 max-age，失败响应带 no-store，节点改回公开后浏览器不会继续用缓存里的 NotFound；POST 不带缓存头。入口卡片加公开数据一节与一个 GET 例子；e2e 把 node1 标为公开，这个例子在重启后那一轮才有非空输出。" > /tmp/m5-t6-commit.log 2>&1; echo $?
```

Expected：0。

- [ ] **Step 6: 缺陷注入**

| | 改动 | 命令 | 期望 |
|---|---|---|---|
| a | `Handler` 返回 `p.cacheControl(h)`（去掉限流） | `go test -count=1 -run TestPublicRateLimitPerSource ./internal/hub/api/ > /tmp/m5-t6-inj-a.log 2>&1; echo $?` | 1，`request past the burst: 200` |
| b | `ratelimit.BySource` 里 `auth.ClientIP` 的第三个参数写死为 `[]netip.Prefix{netip.MustParsePrefix("127.0.0.0/8")}`（无视 `--trusted-proxies`，信任本机对端的转发头） | `go test -count=1 -run TestPublicRateLimitIgnores ./internal/hub/api/ > /tmp/m5-t6-inj-b.log 2>&1; echo $?` | 1，`spoofed X-Forwarded-For got a fresh bucket: 200`。写成 `0.0.0.0/0` 不会红：`ClientIP` 从右往左跳过所有可信地址，最后落回对端 `127.0.0.1`，桶没换 |
| c | `cacheHeaderWriter.WriteHeader` 去掉 `code == http.StatusOK &&` | `go test -count=1 -run TestPublicCacheControl ./internal/hub/api/ > /tmp/m5-t6-inj-c.log 2>&1; echo $?` | 1，`NotFound: 404 Cache-Control "max-age=60"` |
| d | `cacheControl` 去掉 `r.Method != http.MethodGet` 的分支 | 同 c | 1，`POST GetSite: 200 Cache-Control ["max-age=300"], want none` |
| e | `cachePolicy` 去掉 `get && v == 0` 那个 `case` | `go test -count=1 -run TestCachePolicy ./internal/hub/api/ > /tmp/m5-t6-inj-e.log 2>&1; echo $?` | 1，`no panic; want one mentioning "synthetic.S.Bare accepts GET"` |
| f | `public.proto` 删去 `GetSnapshot` 的 `cache_max_age_s` 选项，再 `make gen` | `go test -count=1 ./cmd/hub/ > /tmp/m5-t6-inj-f.log 2>&1; echo $?` | 1，构造 `Public` 时 panic：`probe.v1.PublicService.GetSnapshot accepts GET (idempotency_level = NO_SIDE_EFFECTS) but does not declare a positive probe.v1.cache_max_age_s`。还原：`git checkout -- proto gen web/src/gen` |
| g | `cachePolicy` 去掉 `!get && v != 0` 那个 `case` | 同 e | 1，`no panic; want one mentioning "synthetic.S.Bare declares probe.v1.cache_max_age_s but does not accept GET"` |
| h | `probeServices` 的回调开头加 `if fd.Path() != "probe/v1/public.proto" { return true }`（只查公开服务） | 同 e | 1，`probe.v1 services = [probe.v1.PublicService], want [probe.v1.AdminService probe.v1.AgentService probe.v1.PublicService]` |
| i | `public.proto` 里 `GetSite` 的 `cache_max_age_s` 改成 30，再 `make gen` | `go test -count=1 -run 'TestCachePolicy\|TestPublicCacheControl' ./internal/hub/api/ > /tmp/m5-t6-inj-i.log 2>&1; echo $?` | 1，`GET GetSite: 200 Cache-Control "max-age=30", want "max-age=300"` 与 `table = map[…GetSite:30 …]`。还原同 f |
| j | `publicBurst = 59` | `go test -count=1 -run TestPublicRateLimit ./internal/hub/api/ > /tmp/m5-t6-inj-j.log 2>&1; echo $?` | 1，`request 60 within the burst: 429` |
| k | `publicRefill = time.Second` | 同 j | 1，`after one refill period: 429` |
| l | 把限流换成 connect 拦截器：`Handler` 返回 `p.cacheControl(h)`，`connectHandler` 加一个拦截器，按 `auth.ClientIP(req.Peer().Addr, req.Header().Get("X-Forwarded-For"), p.cfg.TrustedProxies)` 取 `p.limit.Allow`，超限返回 `ResourceExhausted` | 同 j | 1，`malformed requests were not counted: 200`（Task 7 之后还有 `cache hit past the burst: 200`） |
| m | `BySource` 取来源改成 `auth.ClientIP(r.RemoteAddr, "", nil)`（所有请求都算到对端地址上） | `go test -count=1 -run TestPublicRateLimitPerSource ./internal/hub/api/ > /tmp/m5-t6-inj-m.log 2>&1; echo $?` | 1，`another source shares the bucket: 429` |

每项：改动 → `git diff --stat` 非空 → 跑命令 → 核对原因 → `git checkout -- <文件>`。

---

### Task 7: GetSnapshot 的响应字节缓存

**Files:**
- Create: `internal/hub/api/snapshot_cache.go`、`internal/hub/api/snapshot_cache_test.go`
- Modify: `internal/hub/api/public.go`（`Handler` 在限流与 connect 之间插入缓存）

**Interfaces:**
- Consumes：
  - Task 5 的 `(*Public).connectHandler`、测试辅助 `pubGet`/`pubPost`/`jsonQuery`/`setPublic`
  - Task 6 的 `cacheControl`；Task 4 的 `ratelimit.BySource`
- Produces：
  - `const snapshotTTL = time.Second`
  - `func newSnapshotCache(next http.Handler, clk clock.Clock) http.Handler`
  - `func negotiatedCompression(accept string) string`：参数是 `Header.Get("Accept-Encoding")`，只看第一行，与 connect 相同
  - `Handler()` 返回 `cacheControl(ratelimit.BySource(newSnapshotCache(connect)))`

- [ ] **Step 1: 写失败测试**

`internal/hub/api/snapshot_cache_test.go`：

```go
package api

import (
	"bytes"
	"compress/gzip"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/xjetry/probe/gen/probe/v1/probev1connect"
	"github.com/xjetry/probe/internal/clock"
	"github.com/xjetry/probe/internal/testwait"
)

// observed 是一次响应里缓存必须与直连 connect 一致的部分；gzip 正文先解开再比，压缩字节本身不是契约。
type observed struct {
	status                             int
	contentType, contentEncoding, vary string
	body                               string
}

func observe(t *testing.T, h http.Handler, newReq func() *http.Request) observed {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, newReq())
	resp := rec.Result()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	if resp.Header.Get("Content-Encoding") == "gzip" {
		zr, err := gzip.NewReader(bytes.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		if body, err = io.ReadAll(zr); err != nil {
			t.Fatal(err)
		}
	}
	return observed{resp.StatusCode, resp.Header.Get("Content-Type"), resp.Header.Get("Content-Encoding"), resp.Header.Get("Vary"), string(body)}
}

func snapshotRequest(method, query, body string, header map[string]string) func() *http.Request {
	return func() *http.Request {
		target := probev1connect.PublicServiceGetSnapshotProcedure
		if query != "" {
			target += "?" + query
		}
		req := httptest.NewRequest(method, target, strings.NewReader(body))
		// 值里的换行拆成同名头的多行：有的形态要测多行同名头。
		for k, v := range header {
			for _, line := range strings.Split(v, "\n") {
				req.Header.Add(k, line)
			}
		}
		return req
	}
}

// 缓存对每一种请求形态的应答都与直连 connect 相同：规范形态由缓存回答（第二次是命中），
// 其余形态原样交给 connect；缓存不替 connect 接受它会拒绝的请求，也不给请求方它没要的压缩。
func TestSnapshotCacheIsTransparent(t *testing.T) {
	h := newHarness(t, "")
	h.login(t)
	id, _ := h.createNode(t, "pub")
	h.setPublic(t, id, "pub", true)
	_, direct := h.pub.connectHandler()
	const json = "connect=v1&encoding=json&message=%7B%7D"
	jsonCT := map[string]string{"Content-Type": "application/json"}
	acceptGzip := map[string]string{"Accept-Encoding": "gzip"}
	primes := []func() *http.Request{
		snapshotRequest(http.MethodGet, json, "", nil),
		snapshotRequest(http.MethodGet, json, "", acceptGzip),
		snapshotRequest(http.MethodGet, "connect=v1&encoding=proto&base64=1&message=", "", nil),
		snapshotRequest(http.MethodPost, "", "{}", jsonCT),
		snapshotRequest(http.MethodPost, "", "{}", map[string]string{"Content-Type": "application/json", "Accept-Encoding": "gzip"}),
		snapshotRequest(http.MethodPost, "", "", map[string]string{"Content-Type": "application/proto"}),
	}
	// hit 标出规范形态：键已有缓存时两次都不进 connect。其余形态每次都交给 connect。
	for _, c := range []struct {
		name, method, query, body string
		header                    map[string]string
		hit                       bool
	}{
		{"json", http.MethodGet, json, "", nil, true},
		{"json without connect", http.MethodGet, "encoding=json&message=%7B%7D", "", nil, true},
		{"json gzip", http.MethodGet, json, "", map[string]string{"Accept-Encoding": "gzip"}, true},
		{"json br then gzip", http.MethodGet, json, "", map[string]string{"Accept-Encoding": "br, gzip"}, true},
		{"json gzip q=0", http.MethodGet, json, "", map[string]string{"Accept-Encoding": "gzip;q=0"}, true},
		{"json identity line then gzip line", http.MethodGet, json, "", map[string]string{"Accept-Encoding": "identity\ngzip"}, true},
		{"json base64 unpadded", http.MethodGet, "connect=v1&encoding=json&base64=1&message=e30", "", nil, false},
		{"json base64 padded", http.MethodGet, "connect=v1&encoding=json&base64=1&message=e30%3D", "", nil, false},
		{"json base64 flag with literal", http.MethodGet, "connect=v1&encoding=json&base64=1&message=%7B%7D", "", nil, false},
		{"json base64=0", http.MethodGet, "connect=v1&encoding=json&base64=0&message=%7B%7D", "", nil, false},
		{"json whitespace", http.MethodGet, "connect=v1&encoding=json&message=%7B%20%7D", "", nil, false},
		{"json unknown field", http.MethodGet, "connect=v1&encoding=json&message=%7B%22x%22%3A1%7D", "", nil, false},
		{"json without message", http.MethodGet, "connect=v1&encoding=json", "", nil, false},
		{"proto base64", http.MethodGet, "connect=v1&encoding=proto&base64=1&message=", "", nil, true},
		{"proto raw", http.MethodGet, "connect=v1&encoding=proto&message=", "", nil, true},
		{"proto unknown field", http.MethodGet, "connect=v1&encoding=proto&base64=1&message=CAE", "", nil, false},
		{"proto invalid", http.MethodGet, "connect=v1&encoding=proto&base64=1&message=AA", "", nil, false},
		{"duplicate encoding", http.MethodGet, "connect=v1&encoding=json&encoding=proto&message=%7B%7D", "", nil, false},
		{"connect v2", http.MethodGet, "connect=v2&encoding=json&message=%7B%7D", "", nil, false},
		{"compressed request", http.MethodGet, "connect=v1&encoding=json&compression=gzip&message=%7B%7D", "", nil, false},
		{"timeout header", http.MethodGet, json, "", map[string]string{"Connect-Timeout-Ms": "abc"}, false},
		{"post json", http.MethodPost, "", "{}", jsonCT, true},
		{"post json gzip", http.MethodPost, "", "{}", map[string]string{"Content-Type": "application/json", "Accept-Encoding": "gzip"}, true},
		{"post proto", http.MethodPost, "", "", map[string]string{"Content-Type": "application/proto"}, true},
		{"post json whitespace", http.MethodPost, "", "{ }", jsonCT, false},
		{"post json unknown field", http.MethodPost, "", `{"x":1}`, jsonCT, false},
		{"post json charset", http.MethodPost, "", "{}", map[string]string{"Content-Type": "application/json; charset=utf-8"}, false},
		{"post protocol version 2", http.MethodPost, "", "{}", map[string]string{"Content-Type": "application/json", "Connect-Protocol-Version": "2"}, false},
		{"post text", http.MethodPost, "", "{}", map[string]string{"Content-Type": "text/plain"}, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			calls := 0
			cached := newSnapshotCache(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				direct.ServeHTTP(w, r)
			}), h.clk)
			// 先让每个键都有缓存：错把某种形态当成规范形态的缺陷，只在它的键已有缓存时才会下发错的字节。
			for _, prime := range primes {
				observe(t, cached, prime)
			}
			calls = 0
			req := snapshotRequest(c.method, c.query, c.body, c.header)
			want := observe(t, direct, req)
			for i := range 2 {
				if got := observe(t, cached, req); got != want {
					t.Fatalf("call %d through the cache:\n got %+v\nwant %+v", i+1, got, want)
				}
			}
			if wantCalls := map[bool]int{true: 0, false: 2}[c.hit]; calls != wantCalls {
				t.Fatalf("connect ran %d times for two requests, want %d", calls, wantCalls)
			}
		})
	}
}

// 同一个键在窗口内只进一次处理器：并发的同键请求排队，拿到同一份字节。
func TestSnapshotCacheSerializesOnceForConcurrentRequests(t *testing.T) {
	clk := clock.NewFake(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	var calls atomic.Int64
	allArrived := make(chan struct{})
	cache := newSnapshotCache(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := calls.Add(1)
		<-allArrived
		fmt.Fprintf(w, "serialization %d", n)
	}), clk)
	const n = 16
	var arrived atomic.Int64
	bodies := make(chan string, n)
	for range n {
		go func() {
			arrived.Add(1)
			rec := httptest.NewRecorder()
			cache.ServeHTTP(rec, snapshotRequest(http.MethodGet, "connect=v1&encoding=json&message=%7B%7D", "", nil)())
			bodies <- rec.Body.String()
		}()
	}
	testwait.Until(t, time.Millisecond, func() bool { return arrived.Load() == n }, "only %v of %d requests arrived",
		testwait.When(func() string { return fmt.Sprint(arrived.Load()) }), n)
	close(allArrived)
	for range n {
		select {
		case b := <-bodies:
			if b != "serialization 1" {
				t.Errorf("body %q, want the single serialization", b)
			}
		case <-time.After(testwait.Bound):
			t.Fatal("a request did not finish")
		}
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("handler ran %d times for one key within one window", got)
	}
}

func TestSnapshotCacheKeysWindowAndWhatItStores(t *testing.T) {
	clk := clock.NewFake(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	var calls int
	gzipHonest, fail := true, false
	cache := newSnapshotCache(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if gzipHonest && negotiatedCompression(r.Header.Get("Accept-Encoding")) == "gzip" {
			w.Header().Set("Content-Encoding", "gzip")
		}
		if fail {
			w.WriteHeader(http.StatusServiceUnavailable)
		}
		fmt.Fprintf(w, "serialization %d", calls)
	}), clk)
	serve := func(method, query, body string, header map[string]string) string {
		rec := httptest.NewRecorder()
		cache.ServeHTTP(rec, snapshotRequest(method, query, body, header)())
		return rec.Body.String()
	}
	const json = "connect=v1&encoding=json&message=%7B%7D"
	acceptGzip := map[string]string{"Accept-Encoding": "gzip"}
	for _, s := range []struct {
		name, method, query, body string
		header                    map[string]string
		want                      string
	}{
		{"json GET", http.MethodGet, json, "", nil, "serialization 1"},
		{"json GET again", http.MethodGet, json, "", nil, "serialization 1"},
		{"proto GET", http.MethodGet, "connect=v1&encoding=proto&base64=1&message=", "", nil, "serialization 2"},
		{"proto GET without base64 flag", http.MethodGet, "encoding=proto&message=", "", nil, "serialization 2"},
		{"gzip GET", http.MethodGet, json, "", acceptGzip, "serialization 3"},
		{"json POST", http.MethodPost, "", "{}", map[string]string{"Content-Type": "application/json"}, "serialization 4"},
		{"proto POST", http.MethodPost, "", "", map[string]string{"Content-Type": "application/proto"}, "serialization 5"},
	} {
		if got := serve(s.method, s.query, s.body, s.header); got != s.want {
			t.Fatalf("%s: %q, want %q", s.name, got, s.want)
		}
	}
	// 窗口是 spec §10 的字面值 1 秒，不引用 snapshotTTL：常量改了，这里要红。
	clk.Advance(time.Second - time.Nanosecond)
	if got := serve(http.MethodGet, json, "", nil); got != "serialization 1" {
		t.Fatalf("expired before one second: %q", got)
	}
	clk.Advance(time.Nanosecond)
	if got := serve(http.MethodGet, json, "", nil); got != "serialization 6" {
		t.Fatalf("served after one second: %q", got)
	}
	// connect 实际的压缩与键不符时不入缓存：下一次仍进处理器。
	gzipHonest = false
	clk.Advance(time.Second)
	for _, want := range []string{"serialization 7", "serialization 8"} {
		if got := serve(http.MethodGet, json, "", acceptGzip); got != want {
			t.Fatalf("mismatched compression was cached: %q, want %q", got, want)
		}
	}
	// 失败的响应不入缓存。
	fail = true
	for _, want := range []string{"serialization 9", "serialization 10"} {
		if got := serve(http.MethodGet, "connect=v1&encoding=proto&message=", "", nil); got != want {
			t.Fatalf("failed response was cached: %q, want %q", got, want)
		}
	}
}

// 节点改为非公开之后：已缓存的快照最多再下发 1 秒（spec §10 的字面值），之后不再出现；
// 历史查询不经这层缓存，立即 NotFound；此前没有缓存的键（POST）立即反映改动。
func TestSnapshotCacheWindowAfterNodeTurnsPrivate(t *testing.T) {
	h := newHarness(t, "")
	h.login(t)
	id, _ := h.createNode(t, "flip")
	h.setPublic(t, id, "flip", true)
	snapshot := jsonQuery("{}")
	before := pubGet(t, h, "GetSnapshot", snapshot, nil)
	if before.status != http.StatusOK || !bytes.Contains(before.body, []byte(`"flip"`)) {
		t.Fatalf("public node missing: %d %s", before.status, before.body)
	}
	h.setPublic(t, id, "flip", false)
	if within := pubGet(t, h, "GetSnapshot", snapshot, nil); !bytes.Equal(within.body, before.body) {
		t.Fatalf("snapshot re-serialized within the window: %s", within.body)
	}
	window := fmt.Sprintf(`{"nodeId":"%d","from":"0","to":"3600"}`, id)
	if got := pubGet(t, h, "QueryMetrics", jsonQuery(window), nil); got.status != http.StatusNotFound {
		t.Fatalf("history of a private node: %d %s", got.status, got.body)
	}
	if got := pubPost(t, h, "GetSnapshot", "{}", nil); bytes.Contains(got.body, []byte(`"flip"`)) {
		t.Fatalf("uncached key still lists the private node: %s", got.body)
	}
	h.clk.Advance(time.Second)
	if after := pubGet(t, h, "GetSnapshot", snapshot, nil); after.status != http.StatusOK || bytes.Contains(after.body, []byte(`"flip"`)) {
		t.Fatalf("private node still served after the window: %d %s", after.status, after.body)
	}
}

// 快照缓存的命中同样计数：限流包在缓存外面。
func TestPublicRateLimitCountsSnapshotCacheHits(t *testing.T) {
	h := newHarness(t, "")
	for i := range 60 {
		if got := pubGet(t, h, "GetSnapshot", jsonQuery("{}"), nil); got.status != http.StatusOK {
			t.Fatalf("request %d: %d %s", i+1, got.status, got.body)
		}
	}
	if got := pubGet(t, h, "GetSnapshot", jsonQuery("{}"), nil); got.status != http.StatusTooManyRequests {
		t.Fatalf("cache hit past the burst: %d", got.status)
	}
}
```

- [ ] **Step 2: 跑红**

```bash
cd /Users/xjetry/work/vibe/probe-public && go test -count=1 -run 'TestSnapshotCache|TestPublicRateLimitCounts' ./internal/hub/api/ > /tmp/m5-t7-red.log 2>&1; echo $?
```

Expected：1。编译失败：`newSnapshotCache`、`negotiatedCompression`、`snapshotTTL` 未定义。

- [ ] **Step 3: 实现**

`internal/hub/api/snapshot_cache.go`：

```go
package api

import (
	"bytes"
	"io"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/xjetry/probe/gen/probe/v1/probev1connect"
	"github.com/xjetry/probe/internal/clock"
)

// snapshotTTL 是 GetSnapshot 响应字节的缓存窗口（§10：1 秒）。访客再多，hub 每个键每个窗口最多序列化一次；
// 代价是节点改为非公开后，已缓存的快照最多再下发 snapshotTTL。历史查询不经这层缓存。
const snapshotTTL = time.Second

// snapshotKey 区分响应字节会不同的请求：GET 与 POST 的响应头不同（connect 给 GET 加 Vary），codec 决定正文编码，
// 压缩由 Accept-Encoding 协商。规范形态的请求消息为空，不进键。
type snapshotKey struct {
	get         bool
	codec       string // "json" 或 "proto"
	compression string // "identity" 或 "gzip"
}

type snapshotEntry struct {
	// mu 让同键的并发请求排队：窗口内只有第一个进到 connect 处理器，其余拿它存下的字节。
	mu      sync.Mutex
	filled  bool
	expires time.Duration
	status  int
	header  http.Header
	body    []byte
}

type snapshotCache struct {
	next http.Handler
	clk  clock.Clock

	mu      sync.Mutex // 只保护 entries 这张表；条目内容由各自的 mu 保护
	entries map[snapshotKey]*snapshotEntry
}

func newSnapshotCache(next http.Handler, clk clock.Clock) http.Handler {
	return &snapshotCache{next: next, clk: clk, entries: map[snapshotKey]*snapshotEntry{}}
}

func (c *snapshotCache) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != probev1connect.PublicServiceGetSnapshotProcedure {
		c.next.ServeHTTP(w, r)
		return
	}
	key, ok := canonicalSnapshotRequest(r)
	if !ok {
		c.next.ServeHTTP(w, r)
		return
	}
	status, header, body := c.lookup(key, r)
	// 存下的头在条目里共享，只读；写回时逐项复制到这个响应自己的头里。
	dst := w.Header()
	for k, v := range header {
		dst[k] = slices.Clone(v)
	}
	dst.Set("Content-Length", strconv.Itoa(len(body)))
	w.WriteHeader(status)
	w.Write(body)
}

// lookup 返回窗口内的缓存，或者调 connect 处理器取一份新的。只有"状态 200 且 connect 实际用的压缩与键一致"才入缓存：
// 键里的压缩是 negotiatedCompression 对 connect 协商的复刻，复刻与实际不符时只损失命中率，不会把与键不符的字节
// 发给后来的请求。写回客户端在条目锁之外进行，慢客户端不挡同键的其他请求。
func (c *snapshotCache) lookup(key snapshotKey, r *http.Request) (int, http.Header, []byte) {
	c.mu.Lock()
	e := c.entries[key]
	if e == nil {
		e = &snapshotEntry{}
		c.entries[key] = e
	}
	c.mu.Unlock()
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.filled && c.clk.Mono() < e.expires {
		return e.status, e.header, e.body
	}
	rec := &responseRecorder{header: http.Header{}, status: http.StatusOK}
	c.next.ServeHTTP(rec, r)
	body := rec.body.Bytes()
	if rec.status == http.StatusOK && rec.header.Get("Content-Encoding") == contentEncoding(key.compression) {
		e.filled, e.status, e.header, e.body, e.expires = true, rec.status, rec.header, body, c.clk.Mono()+snapshotTTL
	}
	return rec.status, rec.header, body
}

// canonicalSnapshotRequest 只认 connect-web 与 curl 常用的规范形态。其余形态（base64 包着的 JSON、带压缩的请求、
// 多余的空白或字段、别的协议版本、超时头……）交给 connect 自己解码与报错，不走缓存：缓存键不含请求内容，
// 缓存只能回答 connect 必然以同样方式成功处理的请求——宁可少命中，不可替 connect 接受它会拒绝的请求。
func canonicalSnapshotRequest(r *http.Request) (snapshotKey, bool) {
	if len(r.Header.Values("Connect-Timeout-Ms")) != 0 {
		return snapshotKey{}, false
	}
	key := snapshotKey{compression: negotiatedCompression(r.Header.Get("Accept-Encoding"))}
	switch r.Method {
	case http.MethodGet:
		q := r.URL.Query()
		for _, name := range []string{"connect", "encoding", "message", "base64", "compression"} {
			if len(q[name]) > 1 {
				return snapshotKey{}, false
			}
		}
		if v, ok := q["connect"]; ok && v[0] != "v1" {
			return snapshotKey{}, false
		}
		if v, ok := q["compression"]; ok && v[0] != "identity" {
			return snapshotKey{}, false
		}
		msg, hasMsg := q["message"]
		b64, hasB64 := q["base64"]
		switch q.Get("encoding") {
		case "json":
			if !hasMsg || msg[0] != "{}" || hasB64 {
				return snapshotKey{}, false
			}
		case "proto":
			if !hasMsg || msg[0] != "" || hasB64 && b64[0] != "1" {
				return snapshotKey{}, false
			}
		default:
			return snapshotKey{}, false
		}
		key.get, key.codec = true, q.Get("encoding")
		return key, true
	case http.MethodPost:
		ct := r.Header.Values("Content-Type")
		if len(ct) != 1 || len(r.Header.Values("Content-Encoding")) != 0 {
			return snapshotKey{}, false
		}
		if v := r.Header.Values("Connect-Protocol-Version"); len(v) > 1 || len(v) == 1 && v[0] != "1" {
			return snapshotKey{}, false
		}
		var want string
		switch ct[0] {
		case "application/json":
			key.codec, want = "json", "{}"
		case "application/proto":
			key.codec, want = "proto", ""
		default:
			return snapshotKey{}, false
		}
		// 多读一个字节就能判定正文是否恰为 want；读出的字节放回请求体，connect 照常读到完整正文。
		head, err := io.ReadAll(io.LimitReader(r.Body, int64(len(want)+1)))
		r.Body = readCloser{Reader: io.MultiReader(bytes.NewReader(head), r.Body), Closer: r.Body}
		if err != nil || string(head) != want {
			return snapshotKey{}, false
		}
		return key, true
	}
	return snapshotKey{}, false
}

type readCloser struct {
	io.Reader
	io.Closer
}

// negotiatedCompression 复刻 connect（v1.21.0）为不带压缩的 unary 请求选择响应压缩的规则：只看第一行
// Accept-Encoding（调用方传 Header.Get 的结果，与 connect 取这个头的方式相同），按逗号与空格切分，
// 取第一个服务端注册了的名字；本服务只有 connect 默认的 gzip。gzip 只在请求方列出了它时才会被选中，
// 所以按键命中的字节总是请求方能解开的。
func negotiatedCompression(accept string) string {
	for _, name := range strings.FieldsFunc(accept, func(r rune) bool { return r == ',' || r == ' ' }) {
		if name == "gzip" {
			return "gzip"
		}
	}
	return "identity"
}

// contentEncoding 是某种压缩在响应头里的写法：identity 不写 Content-Encoding。
func contentEncoding(compression string) string {
	if compression == "identity" {
		return ""
	}
	return compression
}

// responseRecorder 收下 connect 处理器的完整响应；unary 处理器返回时头与正文都已写完。
type responseRecorder struct {
	header http.Header
	status int
	wrote  bool
	body   bytes.Buffer
}

func (r *responseRecorder) Header() http.Header { return r.header }

func (r *responseRecorder) WriteHeader(code int) {
	if !r.wrote {
		r.status, r.wrote = code, true
	}
}

func (r *responseRecorder) Write(b []byte) (int, error) {
	r.wrote = true
	return r.body.Write(b)
}
```

`internal/hub/api/public.go` 的 `Handler` 及其注释替换为：

```go
// Handler 是公开服务唯一的挂载点：cacheControl(BySource(snapshotCache(connect)))。限流包在缓存与 connect 之外，
// 缓存命中与解码失败的请求同样计数；缓存头包在最外面，限流的 429 也带 no-store。
func (p *Public) Handler() (string, http.Handler) {
	path, h := p.connectHandler()
	return path, p.cacheControl(ratelimit.BySource(p.limit, p.cfg.TrustedProxies, p.clk, newSnapshotCache(h, p.clk)))
}
```

- [ ] **Step 4: 跑绿**

```bash
cd /Users/xjetry/work/vibe/probe-public && go test -count=1 ./internal/hub/api/ ./cmd/hub/ > /tmp/m5-t7-green.log 2>&1; echo $?
cd /Users/xjetry/work/vibe/probe-public && go test -count=1 -race ./internal/hub/api/ > /tmp/m5-t7-race.log 2>&1; echo $?
```

Expected：两条都是 0。`TestSnapshotCacheIsTransparent` 的 29 个子用例全绿，说明两点：
- 规范形态在键已有缓存时不进 connect（`hit`）。
- connect v1.21.0 实际选的压缩与 `negotiatedCompression` 一致：`gzip;q=0` 得到 identity，`br, gzip` 得到 gzip，两行 `Accept-Encoding` 只看第一行。若不一致，要么 `hit` 用例的调用次数是 2（复刻的键与实际压缩不符，不入缓存），要么经缓存的 Content-Encoding 与直连不同。

- [ ] **Step 5: 提交**

```bash
cd /Users/xjetry/work/vibe/probe-public && git add internal/hub/api/snapshot_cache.go internal/hub/api/snapshot_cache_test.go internal/hub/api/public.go && git commit -m "api: 公开快照按编码缓存响应字节 1 秒" -m "键是 GET 或 POST、codec 与协商出的压缩；同键的并发请求排队，窗口内只序列化一次。缓存键不含请求内容，所以只回答规范形态的请求，其余形态（base64 包着的 JSON、带压缩或超时头的请求、多余的空白或字段）交给 connect 自己解码与报错；等价测试对每种形态比对缓存与直连 connect 的应答。只在状态 200 且 connect 实际的压缩与键一致时入缓存，协商复刻出错只损失命中率。节点改为非公开后，已缓存的快照最多再下发 1 秒；历史查询不经这层缓存。限流包在缓存之外，命中同样计数。协商的复刻与 connect 同样只看第一行 Accept-Encoding，两行同名头的请求经缓存与直连也一致。" > /tmp/m5-t7-commit.log 2>&1; echo $?
```

Expected：0。

- [ ] **Step 6: 缺陷注入**

| | 改动 | 命令 | 期望 |
|---|---|---|---|
| a | `canonicalSnapshotRequest` 的 json 分支去掉 `\|\| hasB64` | `go test -count=1 -run TestSnapshotCacheIsTransparent ./internal/hub/api/ > /tmp/m5-t7-inj-a.log 2>&1; echo $?` | 1。子用例 `json base64 flag with literal`：经缓存得到 200，直连得到 400。`json base64=0` 报 `connect ran 0 times for two requests, want 2` |
| b | `lookup` 删去 `e.mu.Lock()` 与 `defer e.mu.Unlock()` | `go test -count=5 -run TestSnapshotCacheSerializesOnce ./internal/hub/api/ > /tmp/m5-t7-inj-b.log 2>&1; echo $?` | 1，`handler ran N times for one key within one window`，N > 1。`arrived` 在进入 `ServeHTTP` 之前计数，`allArrived` 关闭时可能有请求还没走到 `lookup`，所以 N 在 2 到 16 之间，不一定是 16 |
| c | `lookup` 的入缓存条件去掉 `&& rec.header.Get("Content-Encoding") == contentEncoding(key.compression)` | `go test -count=1 -run TestSnapshotCacheKeysWindow ./internal/hub/api/ > /tmp/m5-t7-inj-c.log 2>&1; echo $?` | 1，`mismatched compression was cached: "serialization 7", want "serialization 8"` |
| d | `snapshotTTL` 改为 `2 * time.Second` | `go test -count=1 -run 'TestSnapshotCacheWindowAfter\|TestSnapshotCacheKeysWindow' ./internal/hub/api/ > /tmp/m5-t7-inj-d.log 2>&1; echo $?` | 1，`private node still served after the window` 与 `served after one second: "serialization 1"`：测试用 spec 的字面值 1 秒，不引用常量 |
| e | `Handler` 改成 `p.cacheControl(newSnapshotCache(ratelimit.BySource(p.limit, p.cfg.TrustedProxies, p.clk, h), p.clk))`（缓存在限流之外） | `go test -count=1 -run TestPublicRateLimitCountsSnapshotCacheHits ./internal/hub/api/ > /tmp/m5-t7-inj-e.log 2>&1; echo $?` | 1，`cache hit past the burst: 200` |
| f | 键的压缩改成看全部行：`negotiatedCompression(strings.Join(r.Header.Values("Accept-Encoding"), ","))` | `go test -count=1 -run TestSnapshotCacheIsTransparent ./internal/hub/api/ > /tmp/m5-t7-inj-f.log 2>&1; echo $?` | 1，子用例 `json identity line then gzip line`：经缓存 `contentEncoding:gzip`，直连为空（connect 只看第一行） |
| g | 删去 `r.Body = readCloser{…}` 那一行（读出的字节不放回） | 同 f | 1，`post json`、`post json gzip`、`post json whitespace`、`post json unknown field` 经缓存得到 400 `zero-length payload is not a valid JSON object` 或 JSON 解码错误，直连为 200 |
| h | `lookup` 的入缓存条件去掉 `rec.status == http.StatusOK &&` | `go test -count=1 -run TestSnapshotCacheKeysWindow ./internal/hub/api/ > /tmp/m5-t7-inj-h.log 2>&1; echo $?` | 1，`failed response was cached: "serialization 9", want "serialization 10"` |
| i | GET 分支的 `key.get, key.codec = true, …` 改成 `false`（GET 与 POST 共用条目） | `go test -count=1 -run TestSnapshotCache ./internal/hub/api/ > /tmp/m5-t7-inj-i.log 2>&1; echo $?` | 1，`post json` 等子用例经缓存带 `vary:Accept-Encoding`，直连为空：POST 拿到了 GET 存下的响应头 |

每项：改动 → `git diff --stat` 非空 → 跑命令 → 核对原因 → `git checkout -- <文件>`。

---

### Task 8: 静态服务：内置公开页与 --public-dir

**Files:**
- Modify: `internal/hub/web/web.go`（整份替换：两份嵌入产物、共用的 `serveFiles`；删除 `RootRedirect`）
- Create: `internal/hub/web/dir.go`
- Create: `internal/hub/web/dist-public/.gitkeep`（空文件，入库）
- Modify: `.gitignore`
- Test: `internal/hub/web/web_test.go`（整份替换）、`internal/hub/web/dir_test.go`（新）
- Modify: `cmd/hub/serve.go`（`--public-dir`；根路径挂公开页；`--trusted-proxies` 的帮助）
- Modify: `cmd/hub/mux_test.go`（`newTestMux`、`TestMuxRoutesPanelAndRootAroundRPC`，以及两个枚举测试里公开过程的判据）
- Test: `cmd/hub/serve_test.go`
- Modify: `scripts/e2e.sh`、`scripts/install-accept.sh`（就绪判据不再依赖 `/` 的 302）

**Interfaces:**
- Produces（`internal/hub/web`）：
  - `func Handler() http.Handler`（面板，签名不变）
  - `func PublicHandler() http.Handler`
  - `func DirHandler(dir string) (http.Handler, error)`
  - 包内 `embedded(root fs.FS, dir, prefix, notBuilt string) http.Handler`、`serveFiles`、`hidden`、`relPath`、`serveRegular`
- Removes：`web.RootRedirect`。
- Produces（cmd/hub）：`serve --public-dir <dir>`。
- Task 10 把公开页构建进 `internal/hub/web/dist-public`。

- [ ] **Step 1: 写失败测试**

`internal/hub/web/web_test.go` 整份替换：

```go
package web

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"
)

func builtFS(dir string) fstest.MapFS {
	return fstest.MapFS{
		dir + "/index.html":         {Data: []byte("<!doctype html><div id=root></div>")},
		dir + "/assets/app-abc.js":  {Data: []byte("console.log(1)")},
		dir + "/assets/app-abc.css": {Data: []byte("body{}")},
		dir + "/robots.txt":         {Data: []byte("User-agent: *")},
		dir + "/sub/page.txt":       {Data: []byte("sub page")},
		dir + "/.gitkeep":           {},
		dir + "/.env":               {Data: []byte("SECRET=embedded")},
		dir + "/.git/config":        {Data: []byte("[core] embedded")},
		dir + "/sub/.hidden.txt":    {Data: []byte("hidden page")},
		dir + "/assets/.hidden.js":  {Data: []byte("hidden asset")},
	}
}

func get(t *testing.T, h http.Handler, path string) *http.Response {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
	resp := rec.Result()
	t.Cleanup(func() { resp.Body.Close() })
	return resp
}

func responseBody(t *testing.T, resp *http.Response) string {
	t.Helper()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func checkSecurityHeaders(t *testing.T, resp *http.Response) {
	t.Helper()
	wantCSP := "default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline'; img-src 'self' data:; connect-src 'self'; frame-ancestors 'none'; base-uri 'none'; form-action 'none'"
	if resp.Header.Get("Content-Security-Policy") != wantCSP || resp.Header.Get("X-Content-Type-Options") != "nosniff" || resp.Header.Get("Referrer-Policy") != "no-referrer" {
		t.Fatalf("security headers missing or incorrect: %v", resp.Header)
	}
}

// 面板与内置公开页是同一个核心、两个挂载点：同一组路径在两处得到同样的应答与同一套 CSP。
func TestEmbeddedServesFilesAndFallsBackToIndex(t *testing.T) {
	for _, mount := range []struct {
		name, dir, prefix string
	}{{"panel", "dist", Prefix}, {"public", "dist-public", "/"}} {
		h := embedded(builtFS(mount.dir), mount.dir, mount.prefix, notBuiltAdmin)
		for _, c := range []struct {
			path, wantBody, wantCache string
			wantStatus                int
		}{
			{"", "<div id=root>", "no-cache", 200},
			{"index.html", "<div id=root>", "no-cache", 200},
			{"nodes/7", "<div id=root>", "no-cache", 200},
			{"assets/app-abc.js", "console.log", "public, max-age=31536000, immutable", 200},
			{"assets/app-abc.css", "body{}", "public, max-age=31536000, immutable", 200},
			{"robots.txt", "User-agent: *", "no-cache", 200},
			{"sub/", "<div id=root>", "no-cache", 200},
			{"sub", "<div id=root>", "no-cache", 200},
			{"assets/../robots.txt", "User-agent: *", "no-cache", 200},
			{"sub/../robots.txt", "User-agent: *", "no-cache", 200},
			{"assets/missing.js", "404 page not found", "", 404},
			{"assets/", "404 page not found", "", 404},
			{"assets", "404 page not found", "", 404},
			// 点文件当作不存在：assets/ 下 404，其余回落 index.html；产物里的 .gitkeep 也不例外。
			{".gitkeep", "<div id=root>", "no-cache", 200},
			{".env", "<div id=root>", "no-cache", 200},
			{".git/config", "<div id=root>", "no-cache", 200},
			{"sub/.hidden.txt", "<div id=root>", "no-cache", 200},
			{"assets/.hidden.js", "404 page not found", "", 404},
		} {
			path := mount.prefix + c.path
			t.Run(mount.name+" "+path, func(t *testing.T) {
				resp := get(t, h, path)
				body := responseBody(t, resp)
				if resp.StatusCode != c.wantStatus || !strings.Contains(body, c.wantBody) {
					t.Fatalf("%s: status %d body %q", path, resp.StatusCode, body)
				}
				if c.wantCache != "" && resp.Header.Get("Cache-Control") != c.wantCache {
					t.Fatalf("%s: Cache-Control %q, want %q", path, resp.Header.Get("Cache-Control"), c.wantCache)
				}
				checkSecurityHeaders(t, resp)
			})
		}
	}
}

func TestUnbuiltEmbeddedPagesExplainThemselves(t *testing.T) {
	for _, c := range []struct {
		dir, prefix, notBuilt, want string
	}{
		{"dist", Prefix, notBuiltAdmin, "The admin panel has not been built"},
		{"dist-public", "/", notBuiltPublic, "The public page has not been built"},
	} {
		h := embedded(fstest.MapFS{c.dir + "/.gitkeep": {}}, c.dir, c.prefix, c.notBuilt)
		resp := get(t, h, c.prefix+"nodes/7")
		body := responseBody(t, resp)
		if resp.StatusCode != http.StatusServiceUnavailable || !strings.Contains(body, c.want) {
			t.Fatalf("%s: status %d body %q", c.dir, resp.StatusCode, body)
		}
		if resp.Header.Get("Cache-Control") != "no-cache" || resp.Header.Get("Content-Type") != "text/html; charset=utf-8" {
			t.Fatalf("%s: unbuilt response headers: %v", c.dir, resp.Header)
		}
		checkSecurityHeaders(t, resp)
	}
}

// all:dist 与 all:dist-public 必须能匹配文件；源码检出靠入库的 .gitkeep 满足，构建后还会包含产物。
func TestEmbeddedDistExists(t *testing.T) {
	if _, err := adminDist.ReadDir("dist"); err != nil {
		t.Fatal(err)
	}
	if _, err := publicDist.ReadDir("dist-public"); err != nil {
		t.Fatal(err)
	}
}
```

`internal/hub/web/dir_test.go`：

```go
package web

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/xjetry/probe/internal/testwait"
)

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func symlink(t *testing.T, target, link string) {
	t.Helper()
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
}

// publicDirFixture 造一个替换目录 site 与它旁边的 outside；outside 里的内容一个字节都不能经 site 被读到。
func publicDirFixture(t *testing.T) string {
	t.Helper()
	base := t.TempDir()
	site, outside := filepath.Join(base, "site"), filepath.Join(base, "outside")
	writeFile(t, filepath.Join(site, "index.html"), "site index")
	writeFile(t, filepath.Join(site, "assets", "app.js"), "console.log(1)")
	writeFile(t, filepath.Join(site, "inner.txt"), "inner file")
	writeFile(t, filepath.Join(site, "sub", "page.txt"), "sub page")
	writeFile(t, filepath.Join(outside, "secret.txt"), "outside secret")
	symlink(t, "inner.txt", filepath.Join(site, "in-link.txt"))
	symlink(t, "../outside/secret.txt", filepath.Join(site, "out-link.txt"))
	symlink(t, filepath.Join(outside, "secret.txt"), filepath.Join(site, "abs-link.txt"))
	symlink(t, "../outside", filepath.Join(site, "out-dir"))
	symlink(t, "../../outside/secret.txt", filepath.Join(site, "assets", "leak.js"))
	if err := syscall.Mkfifo(filepath.Join(site, "pipe"), 0o644); err != nil {
		t.Fatal(err)
	}
	return site
}

// serveWithin 在 testwait.Bound 内拿到响应：读在 FIFO 上挂住的缺陷以失败结束，而不是拖到测试超时。
func serveWithin(t *testing.T, h http.Handler, target string) (int, http.Header, string) {
	t.Helper()
	done := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, target, nil))
		done <- rec
	}()
	select {
	case rec := <-done:
		return rec.Code, rec.Header(), rec.Body.String()
	case <-time.After(testwait.Bound):
		t.Fatalf("GET %s did not return within %v", target, testwait.Bound)
		return 0, nil, ""
	}
}

func TestDirHandlerServesOnlyRegularFilesInsideTheDirectory(t *testing.T) {
	h, err := DirHandler(publicDirFixture(t))
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		path, want string
		status     int
	}{
		{"/", "site index", 200},
		{"/index.html", "site index", 200},
		{"/nodes/7", "site index", 200},
		{"/assets/app.js", "console.log(1)", 200},
		{"/inner.txt", "inner file", 200},
		{"/in-link.txt", "inner file", 200},
		{"/sub/page.txt", "sub page", 200},
		{"/sub/", "site index", 200},
		{"/sub", "site index", 200},
		{"/out-link.txt", "site index", 200},
		{"/abs-link.txt", "site index", 200},
		{"/out-dir/secret.txt", "site index", 200},
		{"/../outside/secret.txt", "site index", 200},
		{"/sub/../../outside/secret.txt", "site index", 200},
		{"/%2e%2e/outside/secret.txt", "site index", 200},
		{"/pipe", "site index", 200},
		{"/assets/leak.js", "404 page not found\n", 404},
		{"/assets/missing.js", "404 page not found\n", 404},
		{"/assets/", "404 page not found\n", 404},
	} {
		t.Run(c.path, func(t *testing.T) {
			status, header, body := serveWithin(t, h, c.path)
			if status != c.status || body != c.want {
				t.Fatalf("status %d body %q, want %d %q", status, body, c.status, c.want)
			}
			if header.Get("Content-Security-Policy") != "frame-ancestors 'none'" || header.Get("X-Content-Type-Options") != "nosniff" ||
				header.Get("Cache-Control") != "no-cache" || header.Get("Referrer-Policy") != "" {
				t.Fatalf("headers: %v", header)
			}
		})
	}
}

// 运维常把整个检出或构建目录当 --public-dir，里面的 .git/config、.env 不能被读到：路径任一段以 . 开头
// 就当作不存在，assets/ 下 404，其余回落 index.html。
func TestDirHandlerTreatsDotfilesAsMissing(t *testing.T) {
	site := t.TempDir()
	writeFile(t, filepath.Join(site, "index.html"), "site index")
	writeFile(t, filepath.Join(site, ".env"), "SECRET=dir")
	writeFile(t, filepath.Join(site, ".git", "config"), "[core] dir")
	writeFile(t, filepath.Join(site, ".well-known", "security.txt"), "Contact: dir")
	writeFile(t, filepath.Join(site, "sub", ".hidden.txt"), "hidden page")
	writeFile(t, filepath.Join(site, "assets", ".hidden.js"), "hidden asset")
	h, err := DirHandler(site)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		path, want string
		status     int
	}{
		{"/.env", "site index", 200},
		{"/.git/config", "site index", 200},
		{"/.well-known/security.txt", "site index", 200},
		{"/sub/.hidden.txt", "site index", 200},
		{"/assets/.hidden.js", "404 page not found\n", 404},
	} {
		t.Run(c.path, func(t *testing.T) {
			if status, _, body := serveWithin(t, h, c.path); status != c.status || body != c.want {
				t.Fatalf("status %d body %q, want %d %q", status, body, c.status, c.want)
			}
		})
	}
}

func TestDirHandlerRefusesADirectoryWithoutAnIndexInside(t *testing.T) {
	base := t.TempDir()
	noIndex := filepath.Join(base, "no-index")
	writeFile(t, filepath.Join(noIndex, "other.html"), "x")
	escaping := filepath.Join(base, "escaping")
	writeFile(t, filepath.Join(base, "real-index.html"), "outside index")
	if err := os.MkdirAll(escaping, 0o755); err != nil {
		t.Fatal(err)
	}
	symlink(t, "../real-index.html", filepath.Join(escaping, "index.html"))
	dirIndex := filepath.Join(base, "dir-index")
	if err := os.MkdirAll(filepath.Join(dirIndex, "index.html"), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, dir := range []string{filepath.Join(base, "missing"), noIndex, escaping, dirIndex} {
		if _, err := DirHandler(dir); err == nil || !strings.Contains(err.Error(), "--public-dir "+dir) {
			t.Errorf("DirHandler(%s) error = %v, want one naming --public-dir and the directory", dir, err)
		}
	}
}

// 每个请求重新打开目录：运维原子替换（rename）之后，下一个请求就读到新内容。
func TestDirHandlerFollowsAnAtomicallyReplacedDirectory(t *testing.T) {
	base := t.TempDir()
	site := filepath.Join(base, "site")
	writeFile(t, filepath.Join(site, "index.html"), "old index")
	h, err := DirHandler(site)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, body := serveWithin(t, h, "/"); body != "old index" {
		t.Fatalf("before replacement: %q", body)
	}
	next := filepath.Join(base, "next")
	writeFile(t, filepath.Join(next, "index.html"), "new index")
	if err := os.Rename(site, filepath.Join(base, "old")); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(next, site); err != nil {
		t.Fatal(err)
	}
	if _, _, body := serveWithin(t, h, "/"); body != "new index" {
		t.Fatalf("after replacement: %q", body)
	}
}
```

`cmd/hub/serve_test.go` 的 import 补上 `"os"` 与 `"github.com/xjetry/probe/internal/hub/web"`，文件末尾追加：

```go
// 不带 --public-dir 时根路径是内置公开页。serve 的装配与 newTestMux 各写一份，这里经真实 serve 核对：应答与直接调用
// web.PublicHandler 逐字节相同。面板与公开页的应答总是不同（构建过是各自的 index.html，没构建是各自的说明页），
// 所以无论是否构建过，把 / 挂成面板或别的处理器都会在这里现形。
func TestServeMountsBuiltinPublicPageAtRoot(t *testing.T) {
	url, _, _ := startTestHub(t, filepath.Join(t.TempDir(), "hub.db"), clock.NewFake(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)))
	resp, err := http.Get(url + "/nodes/3")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	want := httptest.NewRecorder()
	web.PublicHandler().ServeHTTP(want, httptest.NewRequest(http.MethodGet, "/nodes/3", nil))
	if resp.StatusCode != want.Code || string(body) != want.Body.String() {
		t.Fatalf("/nodes/3 via serve: %d %q, want the built-in public page: %d %q", resp.StatusCode, body, want.Code, want.Body.String())
	}
}

// 替换目录在打开数据库之前核对：配置有误时 hub 不留下任何副作用。
func TestServeRejectsPublicDirWithoutIndexBeforeOpeningTheDatabase(t *testing.T) {
	db := filepath.Join(t.TempDir(), "hub.db")
	dir := t.TempDir()
	err := runServeWith(context.Background(), []string{"--db", db, "--listen", "127.0.0.1:0", "--public-dir", dir},
		clock.NewFake(time.Now()), slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err == nil || !strings.Contains(err.Error(), "--public-dir "+dir) {
		t.Fatalf("err = %v, want a --public-dir error naming the directory", err)
	}
	if _, statErr := os.Stat(db); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("database touched before --public-dir was checked: %v", statErr)
	}
}

// 替换目录只接管 /：面板与 RPC 路径的路由优先级更高，目录里同名的文件遮蔽不了它们。
func TestServePublicDirReplacesRootButNotPanelOrRPC(t *testing.T) {
	dir := t.TempDir()
	for name, content := range map[string]string{
		"index.html":                     "custom site",
		"admin/index.html":               "shadow panel",
		"probe.v1.PublicService/GetSite": "shadow rpc",
	} {
		path := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	url, _, _ := startTestHub(t, filepath.Join(t.TempDir(), "hub.db"), clock.NewFake(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)), "--public-dir", dir)
	fetch := func(path string) (*http.Response, string) {
		t.Helper()
		resp, err := http.Get(url + path)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		b, err := io.ReadAll(resp.Body)
		if err != nil {
			t.Fatal(err)
		}
		return resp, string(b)
	}
	if resp, body := fetch("/nodes/3"); resp.StatusCode != http.StatusOK || body != "custom site" || resp.Header.Get("Content-Security-Policy") != "frame-ancestors 'none'" {
		t.Fatalf("/nodes/3: %d %q %v", resp.StatusCode, body, resp.Header)
	}
	if resp, body := fetch("/admin/"); strings.Contains(body, "shadow") || !strings.Contains(resp.Header.Get("Content-Security-Policy"), "default-src 'self'") {
		t.Fatalf("/admin/ was shadowed: %d %q", resp.StatusCode, body)
	}
	if resp, body := fetch("/probe.v1.PublicService/GetSite?connect=v1&encoding=json&message=%7B%7D"); resp.StatusCode != http.StatusOK || strings.Contains(body, "shadow") {
		t.Fatalf("RPC path was shadowed: %d %q", resp.StatusCode, body)
	}
}
```

`cmd/hub/mux_test.go`：
- `newTestMux` 的返回语句里 `mountOf("/", web.RootRedirect())` 改为 `mountOf("/", web.PublicHandler())`。
- `TestMuxRoutesPanelAndRootAroundRPC` 上方注释改为 `// RPC 路径与 /admin/ 的优先级高于根路径的公开页；ServeMux 按最长前缀匹配，三者同时挂载时，RPC 仍必须经过服务自身的鉴权。`
- 该测试开头对 `/` 的 302 断言替换为：

```go
	for _, path := range []string{"/", "/nodes/3"} {
		resp, err := client.Get(srv.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if (resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusServiceUnavailable) || resp.Header.Get("Content-Security-Policy") == "" {
			t.Fatalf("%s: %d, want the public page handler (200 when built, 503 when not) with CSP", path, resp.StatusCode)
		}
	}
	resp, err := client.Get(srv.URL + "/admin/nodes/1")
```

  （原来紧随其后的 `resp, err = client.Get(srv.URL + "/admin/nodes/1")` 由上面最后一行取代，其后的断言不变。）
- `TestMuxRejectsAnonymousProcedures` 里公开过程那一支：根路径换成公开页之后，没挂载的公开过程不再得到 404，而是落到公开页拿到 HTML，只看状态码就放过了。改为同时要求 connect 的 JSON 应答：

```go
						// 401 说明被鉴权挡住。没挂载的过程落到根路径：根路径挂着公开页，得到的是 HTML（构建过 200，没构建 503），
						// 只看状态码分不出来，所以还要求应答是 connect 的 JSON（成功与错误都是 application/json）。
						if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusNotFound || resp.Header.Get("Content-Type") != "application/json" {
							t.Fatalf("%s: status %d %q, want the public service to answer anonymously", path, resp.StatusCode, resp.Header.Get("Content-Type"))
						}
```

  替换原来的注释行与 `if` 块（`// 404 说明没挂载（落到了根路径）…` 起，到 `t.Fatalf` 所在 `if` 的右括号止）。
- `TestMuxAcceptsGETOnlyOnPublicService` 的 `case public && …` 两行同理替换为：

```go
				// 公开过程的应答必须来自 connect：没挂载的过程落到根路径的公开页，也可能是 200，只是不是 JSON。
				case public && (resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusBadRequest || resp.Header.Get("Content-Type") != "application/json"):
					t.Errorf("%s: GET status %d %q, want the public service to answer (200, or 400 for an empty window)", path, resp.StatusCode, resp.Header.Get("Content-Type"))
```

- [ ] **Step 2: 跑红**

```bash
cd /Users/xjetry/work/vibe/probe-public && go test -count=1 ./internal/hub/web/ ./cmd/hub/ > /tmp/m5-t8-red.log 2>&1; echo $?
```

Expected：1。编译失败：`embedded`、`notBuiltAdmin`、`notBuiltPublic`、`adminDist`、`publicDist`、`DirHandler`、`web.PublicHandler` 未定义。

- [ ] **Step 3: 实现**

```bash
cd /Users/xjetry/work/vibe/probe-public && mkdir -p internal/hub/web/dist-public && : > internal/hub/web/dist-public/.gitkeep && echo ok > /tmp/m5-t8-gitkeep.log 2>&1; echo $?
```

`.gitignore` 的"构建产物"一节，在 `!/internal/hub/web/dist/.gitkeep` 之后加两行：

```
/internal/hub/web/dist-public/*
!/internal/hub/web/dist-public/.gitkeep
```

`internal/hub/web/web.go` 整份替换：

```go
// Package web 服务 hub 的静态页面：嵌入的管理面板（/admin/）、嵌入的内置公开页（/），以及运维用
// --public-dir 指定的替换目录。三者共用 serveFiles。
//
// 两份嵌入产物由 Vite 构建到本包的 dist 与 dist-public 目录，不入库；目录里只保证有一个占位文件，
// 所以 embed 永远成立，而"有没有真的构建过"由 index.html 是否存在判定。
package web

import (
	"embed"
	"io"
	"io/fs"
	"net/http"
	"path"
	"strings"
)

//go:embed all:dist
var adminDist embed.FS

//go:embed all:dist-public
var publicDist embed.FS

// Prefix 是面板的挂载路径；Vite 的 base 与之一致，产物里的资源引用都以它开头。
const Prefix = "/admin/"

// csp 是面板与内置公开页共用的策略（§10）。style-src 带 'unsafe-inline'，只因为公开页把站点设置的自定义 CSS
// 写进一个 <style> 元素；元素的 style 经 CSSOM 写入（React 的 style 属性、uPlot），不受 style-src 约束。
// img-src 带 data:：logo 是 data: URL。
const csp = "default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline'; img-src 'self' data:; connect-src 'self'; frame-ancestors 'none'; base-uri 'none'; form-action 'none'"

const notBuiltAdmin = `<!doctype html><meta charset="utf-8"><title>probe</title>
<p>The admin panel has not been built into this binary. Run <code>make web</code> before <code>go build</code>, or use a release build.</p>`

const notBuiltPublic = `<!doctype html><meta charset="utf-8"><title>probe</title>
<p>The public page has not been built into this binary. Run <code>make web</code> before <code>go build</code>, or use a release build.</p>`

// Handler 服务 /admin/ 下的管理面板。
func Handler() http.Handler { return embedded(adminDist, "dist", Prefix, notBuiltAdmin) }

// PublicHandler 服务挂在 / 的内置公开页。
func PublicHandler() http.Handler { return embedded(publicDist, "dist-public", "/", notBuiltPublic) }

func builtinHeaders(h http.Header) {
	h.Set("Content-Security-Policy", csp)
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Referrer-Policy", "no-referrer")
}

// embedded 服务一份嵌入产物：assets/ 下的文件名带内容哈希，可以永久缓存，其余 no-cache。
// 没构建过（没有 index.html）时每个路径都回答一页说明。
func embedded(root fs.FS, dir, prefix, notBuilt string) http.Handler {
	sub, err := fs.Sub(root, dir)
	if err != nil {
		panic(err)
	}
	if info, err := fs.Stat(sub, "index.html"); err != nil || !info.Mode().IsRegular() {
		return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			h := w.Header()
			builtinHeaders(h)
			h.Set("Cache-Control", "no-cache")
			h.Set("Content-Type", "text/html; charset=utf-8")
			w.WriteHeader(http.StatusServiceUnavailable)
			io.WriteString(w, notBuilt)
		})
	}
	cacheFor := func(rel string) string {
		if strings.HasPrefix(rel, "assets/") {
			return "public, max-age=31536000, immutable"
		}
		return "no-cache"
	}
	return serveFiles(prefix, builtinHeaders, cacheFor, sub.Open)
}

// opener 打开 rel：rel 已按 URL 路径语义清理，相对挂载根，不以 / 开头，不含 ..。
type opener func(rel string) (fs.File, error)

// serveFiles 是三处静态服务共用的核心。命中普通文件就返回它；rel 为 assets 或在 assets/ 之下而未命中时返回 404——
// 用 HTML 回应 script 标签会被浏览器按 MIME 拒绝，404 才能让缺失可见；其余路径回落到 index.html，交给客户端路由。
// 只服务普通文件：目录、FIFO、设备一律当作不存在，所以任何来源都不列目录，也不会读在特殊文件上。
// 点文件同样当作不存在：路径任一段以 . 开头就不打开（hidden），.git/config、.env 这类运维放目录时顺手带进来的
// 文件因此不经任何来源服务。判定只看请求路径里各段的名字，符号链接按链接自己的名字算。.well-known/ 也在其列，
// 需要它的（ACME 校验、security.txt）由反向代理提供。
func serveFiles(prefix string, headers func(http.Header), cacheFor func(rel string) string, open opener) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		headers(w.Header())
		rel := relPath(r.URL.Path, prefix)
		if rel == "" {
			rel = "index.html"
		}
		if !hidden(rel) && serveRegular(w, r, open, rel, cacheFor(rel)) {
			return
		}
		if rel == "assets" || strings.HasPrefix(rel, "assets/") {
			http.NotFound(w, r)
			return
		}
		if !serveRegular(w, r, open, "index.html", "no-cache") {
			http.Error(w, "index.html unreadable", http.StatusInternalServerError)
		}
	})
}

// hidden 报告 rel 是否有一段以 . 开头。rel 来自 relPath：经 path.Clean 清理，没有 . 与 .. 段，也不以 / 开头，
// 所以以 . 开头的段只能是点文件或点目录的名字。
func hidden(rel string) bool {
	return strings.HasPrefix(rel, ".") || strings.Contains(rel, "/.")
}

// relPath 先按 URL 路径语义清理再去掉挂载前缀。三种来源对 . 与 .. 的处理不同（embed 经 fs.ValidPath 一律拒绝，
// os.Root 接受不越界的 ..），先清理，同一个 URL 在三处才落到同一个文件名，assets/ 的 404 判定也看清理后的路径。
// 越界由来源自己拒绝（fs.ValidPath、os.Root），不靠这里。清理后不在前缀之下的（如 /admin 本身）按挂载根处理。
func relPath(urlPath, prefix string) string {
	if rest, ok := strings.CutPrefix(path.Clean("/"+urlPath), prefix); ok {
		return rest
	}
	return ""
}

// serveRegular 在 rel 是普通文件时写出它并返回 true；打不开或不是普通文件时返回 false，什么都不写。
func serveRegular(w http.ResponseWriter, r *http.Request, open opener, rel, cacheControl string) bool {
	f, err := open(rel)
	if err != nil {
		return false
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() {
		return false
	}
	content, ok := f.(io.ReadSeeker)
	if !ok {
		return false
	}
	w.Header().Set("Cache-Control", cacheControl)
	http.ServeContent(w, r, info.Name(), info.ModTime(), content)
	return true
}
```

`internal/hub/web/dir.go`：

```go
package web

import (
	"fmt"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"syscall"
)

// DirHandler 服务运维用 --public-dir 放置的替换公开页。
//
// 文件经 os.Root 打开：路径与符号链接都越不出 dir，越界即打开失败，按不存在处理。每个请求重新打开 dir：
// 运维原子替换目录（rename）之后，下一个请求就读到新内容。打开带 O_NONBLOCK：目录里的 FIFO 在没有写端时
// 不会让请求挂住，随后的普通文件检查把它当作不存在；对普通文件的读取它不起作用。
// index.html 在构造时核对，必须是目录内的普通文件：它回答每一个不是文件的路径，缺了它 hub 不启动。
//
// 这个目录与面板同源，是信任边界之内的内容：目录里的脚本能读 /admin/，也能带着来访管理员的会话 cookie 调
// AdminService 的任意方法——同源请求照常带 cookie，SameSite=Strict 不拦同源。只放与 hub 二进制同等可信的内容；
// 不受信任的主题要放在另一个 origin，经 PublicService 取数。
func DirHandler(dir string) (http.Handler, error) {
	open := func(rel string) (fs.File, error) {
		root, err := os.OpenRoot(dir)
		if err != nil {
			return nil, err
		}
		defer root.Close()
		return root.OpenFile(filepath.FromSlash(rel), os.O_RDONLY|syscall.O_NONBLOCK, 0)
	}
	f, err := open("index.html")
	if err != nil {
		return nil, fmt.Errorf("--public-dir %s: %w", dir, err)
	}
	defer f.Close()
	if info, err := f.Stat(); err != nil || !info.Mode().IsRegular() {
		return nil, fmt.Errorf("--public-dir %s: index.html must be a regular file inside the directory; it answers every path that is not a file", dir)
	}
	return serveFiles("/", dirHeaders, func(string) string { return "no-cache" }, open), nil
}

// dirHeaders 只加 nosniff 与 frame-ancestors 'none'（§10）：目录由运维放置，严格 CSP 会让第三方主题的
// 字体与图片失效。不限制脚本的后果是目录里的脚本以面板的 origin 运行（DirHandler 的注释写了能做到什么）。
// 文件名不保证带内容哈希，所以一律 no-cache，404 也一样。
func dirHeaders(h http.Header) {
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Content-Security-Policy", "frame-ancestors 'none'")
	h.Set("Cache-Control", "no-cache")
}
```

`cmd/hub/serve.go`：
- `proxies := fs.String(...)` 整行替换为下面这行。反代后不配它时，公开页与注册的限流、登录失败锁定都按代理地址计，全体访客共用一个桶与一个锁定计数；这是部署配置问题，不改限流，只在帮助里写明：

```go
	proxies := fs.String("trusted-proxies", "", "comma-separated CIDRs whose X-Forwarded-For / X-Forwarded-Proto are trusted; empty trusts none. Behind a reverse proxy, list the proxy here: the public page and agent registration are rate-limited per source address, and failed logins are locked out per source address, so without it every visitor shares the proxy address's single bucket and lockout")
```

- `proxies := fs.String(...)` 之后加：

```go
	publicDir := fs.String("public-dir", "", "serve this directory at / instead of the built-in public page; files are opened through os.Root, so paths and symbolic links cannot leave the directory; a path that is not a file, or that has a segment starting with a dot (.git, .env, .well-known), gets the directory's index.html (404 under assets/); every response is no-cache. The directory shares the admin panel's origin: its scripts can read the panel and call the admin API with the session of any signed-in administrator who opens the page, so put only content you trust as much as the hub binary there")
```

- `trusted, err := auth.ParsePrefixes(*proxies)` 及其错误判断之后加：

```go
	// 替换目录在打开数据库之前核对：配置有误时 hub 不留下任何副作用就退出。
	public := web.PublicHandler()
	if *publicDir != "" {
		if public, err = web.DirHandler(*publicDir); err != nil {
			return err
		}
	}
```

- `newMux(...)` 一行的 `mountOf("/", web.RootRedirect())` 改为 `mountOf("/", public)`。
- "hub listening" 日志在 `"timezone", loc.String(),` 之后加 `"public_dir", *publicDir,`。

`/` 不再重定向，"`/` 返回 302"的全部读者要在同一个提交里改掉。在基线上，`git grep -n -e '= 302' -e 'RootRedirect' -e 'StatusFound' -- cmd internal scripts` 命中六个文件：`cmd/hub/serve.go`、`cmd/hub/mux_test.go`、`internal/hub/web/web.go`、`internal/hub/web/web_test.go`（上面已改），以及 `scripts/e2e.sh` 与 `scripts/install-accept.sh` 两个脚本的就绪判据。（`internal/hub/alert` 测试里的 302 是 webhook 应答码，写法不是 `= 302`，不在命中之列，也与此无关。）新判据是匿名的 `GetSite` 返回 200：本提交里 `/` 在没有构建公开页时是 503 说明页，构建后是 200，换了 `--public-dir` 又是另一份内容；`GetSite` 三种情形都是 200，且是 hub 独有的应答。

`scripts/e2e.sh` 两处：

**改动 1.** `wait_hub` 的就绪判据由"`/` 返回 302"改为匿名的 `GetSite` 返回 200。

原文：

```sh
wait_hub() {
  attempt=0
  while [ "$attempt" -lt 30 ]; do
    if status=$(curl -s -o /dev/null -w '%{http_code}' "$base/"); then
      [ "$status" = 302 ] && return 0
    fi
    attempt=$((attempt + 1))
    sleep 0.2
  done
  echo "FAIL: hub did not answer on $base within 6s (expected / to return 302)"
```

改为：

```sh
# 就绪判据是匿名的 GetSite 返回 200：根路径的应答取决于公开页是否构建进二进制、是否换了 --public-dir，
# GetSite 两者都不取决。
wait_hub() {
  attempt=0
  while [ "$attempt" -lt 30 ]; do
    if status=$(curl -s -o /dev/null -w '%{http_code}' "$base/probe.v1.PublicService/GetSite?connect=v1&encoding=json&message=%7B%7D"); then
      [ "$status" = 200 ] && return 0
    fi
    attempt=$((attempt + 1))
    sleep 0.2
  done
  echo "FAIL: hub did not answer on $base within 6s (expected an anonymous GetSite to return 200)"
```

**改动 2.** 删去根路径重定向的断言（`/` 的新行为由 Task 12 在公开页构建之后断言）：

原文：

```sh
[ "$(curl -sS -o /dev/null -w '%{http_code}' "$base/")" = 302 ] || { echo "FAIL: / must redirect to the panel"; exit 1; }
```

改为：删去这一行。

`scripts/install-accept.sh` 一处：

**改动 1.** 就绪循环改用同一个判据，注释随之改。

原文：

```sh
while [ "$attempt" -lt 50 ]; do
  [ "$(curl -s -o /dev/null -w '%{http_code}' "http://127.0.0.1:$HUB_PORT/")" = 302 ] && break
  attempt=$((attempt + 1)); sleep 0.2
done
# 302 也可能是别人占着 18085。本进程没打出 listening 就不是这次的 hub。
```

改为：

```sh
# 就绪判据是匿名的 GetSite 返回 200，不取决于根路径服务什么（公开页是否构建、是否换了 --public-dir）。
while [ "$attempt" -lt 50 ]; do
  [ "$(curl -s -o /dev/null -w '%{http_code}' "http://127.0.0.1:$HUB_PORT/probe.v1.PublicService/GetSite?connect=v1&encoding=json&message=%7B%7D")" = 200 ] && break
  attempt=$((attempt + 1)); sleep 0.2
done
# 200 也可能是别人占着 18085。本进程没打出 listening 就不是这次的 hub。
```

就绪判据的冒烟脚本 `/tmp/m5-t8-ready/check.sh`（用本任务 `go build` 出的 hub，公开页此时还没有构建）：

```sh
#!/bin/sh
# 就绪判据的冒烟：没有构建公开页的 hub 上，匿名 GetSite 是 200，根路径是 503 说明页。
set -u
dir=/tmp/m5-t8-ready
rm -rf "$dir/db"; mkdir -p "$dir/db"
"$dir/probe-hub" serve --db "$dir/db/hub.db" --listen 127.0.0.1:18079 > "$dir/hub.log" 2>&1 &
hub=$!
trap 'kill "$hub" 2> /dev/null; wait "$hub" 2> /dev/null' EXIT
attempt=0
until [ "$(curl -s -o /dev/null -w '%{http_code}' "http://127.0.0.1:18079/probe.v1.PublicService/GetSite?connect=v1&encoding=json&message=%7B%7D")" = 200 ]; do
  attempt=$((attempt + 1)); [ "$attempt" -lt 50 ] || { echo "FAIL: GetSite never returned 200"; cat "$dir/hub.log"; exit 1; }
  sleep 0.2
done
grep -q 'hub listening' "$dir/hub.log" || { echo "FAIL: hub.log has no listening line"; exit 1; }
echo "ready after $attempt retries; / returned $(curl -s -o /dev/null -w '%{http_code}' http://127.0.0.1:18079/)"
```

- [ ] **Step 4: 跑绿**

```bash
cd /Users/xjetry/work/vibe/probe-public && go test -count=1 ./... > /tmp/m5-t8-green.log 2>&1; echo $?
cd /Users/xjetry/work/vibe/probe-public && go vet ./... > /tmp/m5-t8-vet.log 2>&1; echo $?
cd /Users/xjetry/work/vibe/probe-public && GOOS=linux go vet ./internal/hub/web/ ./cmd/hub/ > /tmp/m5-t8-vet-linux.log 2>&1; echo $?
cd /Users/xjetry/work/vibe/probe-public && git status --short internal/hub/web > /tmp/m5-t8-status.log 2>&1; echo $?
cd /Users/xjetry/work/vibe/probe-public && sh -n scripts/e2e.sh > /tmp/m5-t8-e2e-syntax.log 2>&1; echo $?
cd /Users/xjetry/work/vibe/probe-public && sh -n scripts/install-accept.sh > /tmp/m5-t8-ia-syntax.log 2>&1; echo $?
cd /Users/xjetry/work/vibe/probe-public && mkdir -p /tmp/m5-t8-ready && go build -o /tmp/m5-t8-ready/probe-hub ./cmd/hub > /tmp/m5-t8-ready/build.log 2>&1; echo $?
cd /Users/xjetry/work/vibe/probe-public && sh /tmp/m5-t8-ready/check.sh > /tmp/m5-t8-ready/check.log 2>&1; echo $?
cd /Users/xjetry/work/vibe/probe-public && git grep -n -e '= 302' -e 'RootRedirect' -e 'StatusFound' main -- cmd internal scripts > /tmp/m5-t8-302-base.log 2>&1; echo $?
cd /Users/xjetry/work/vibe/probe-public && git grep -n -e '= 302' -e 'RootRedirect' -e 'StatusFound' -- cmd internal scripts > /tmp/m5-t8-302.log 2>&1; echo $?
```

Expected：
- 除最后一条外全部是 0。第三条确认 `syscall.O_NONBLOCK` 与 `syscall.Mkfifo` 在 Linux 上同样可用。
- 倒数第二条是阳性对照：同一组模式在 main 的树上命中，`/tmp/m5-t8-302-base.log` 的行以 `main:` 开头，去掉前缀后恰好是上面列的六个文件。它证明检查本身能命中。
- 最后一条是 1、日志为空：302 与 `RootRedirect` 的读者全部改完。退出码 2 或日志里有报错，说明检查没跑成。
- `/tmp/m5-t8-status.log` 列出 `web.go`、`dir.go`、两份测试与 `dist-public/.gitkeep`，不列 `dist-public` 下的其他文件。
- `/tmp/m5-t8-ready/check.log` 为 `ready after N retries; / returned 503`：同一个 hub 上旧判据永远等不到 302，新判据成立。
- 两个脚本本任务不实跑：e2e 由 Task 12 的 `make e2e` 实跑；`install-accept.sh` 要 OrbStack 真机，不在本计划的验证范围内，它与 e2e 用同一行判据，由冒烟脚本证明判据本身。

- [ ] **Step 5: 提交**

```bash
cd /Users/xjetry/work/vibe/probe-public && git add .gitignore internal/hub/web cmd/hub scripts/e2e.sh scripts/install-accept.sh && git commit -m "web: 根路径服务内置公开页，--public-dir 经 os.Root 服务替换目录" -m "面板、内置公开页与替换目录共用一个只服务普通文件的核心：目录、FIFO 与设备按不存在处理，所以不列目录、不在特殊文件上读挂住；路径任一段以 . 开头的名字同样按不存在处理，.git/config、.env 这类随目录带进来的文件不经任何来源服务（.well-known 也在其列，需要的由反代提供）；assets/ 下未命中返回 404，其余回落 index.html。替换目录每个请求经 os.Root 重新打开，路径与符号链接越不出目录，原子替换目录后下一个请求即读到新内容；打开带 O_NONBLOCK，目录里的 FIFO 不会让请求挂住。它只加 nosniff 与 frame-ancestors 'none'，一律 no-cache；目录与面板同源，其中的脚本能带着来访管理员的会话调管理接口，这一后果写进了 flag 帮助与 DirHandler 注释。--trusted-proxies 的帮助写明反代后不配它时全体访客共用代理地址的一个限流桶与登录锁定计数。CSP 的 'unsafe-inline' 只为公开页的自定义 CSS 那个 <style>：元素的 style 经 CSSOM 写入，不受 style-src 约束。index.html 在打开数据库之前核对。/admin 与 RPC 路径的路由优先级更高，替换目录遮蔽不了它们。e2e 与 install-accept 的就绪判据改为匿名 GetSite 返回 200：根路径的应答随公开页是否构建、是否换了替换目录而变。" > /tmp/m5-t8-commit.log 2>&1; echo $?
```

Expected：0。

- [ ] **Step 6: 缺陷注入**

| | 改动 | 命令 | 期望 |
|---|---|---|---|
| a | `DirHandler` 的 `open` 改为 `return os.OpenFile(filepath.Join(dir, filepath.FromSlash(rel)), os.O_RDONLY\|syscall.O_NONBLOCK, 0)`（不经 os.Root） | `go test -count=1 -run TestDirHandler ./internal/hub/web/ > /tmp/m5-t8-inj-a.log 2>&1; echo $?` | 1，`/out-link.txt`、`/abs-link.txt`、`/out-dir/secret.txt` 的正文是 `outside secret`，`/assets/leak.js` 得到 200；`escaping` 目录被接受 |
| b | `DirHandler` 的打开标志去掉 `\|syscall.O_NONBLOCK`（并删去不再使用的 `syscall` import） | `go test -count=1 -run TestDirHandlerServesOnly ./internal/hub/web/ > /tmp/m5-t8-inj-b.log 2>&1; echo $?` | 1，约 30 秒后报 `GET /pipe did not return within 30s` |
| c | `serveRegular` 去掉 `\|\| !info.Mode().IsRegular()` | `go test -count=1 ./internal/hub/web/ > /tmp/m5-t8-inj-c.log 2>&1; echo $?` | 1，`/sub/` 与 `/sub` 得到空正文的 200，`/pipe` 得到 500 `seeker can't seek`，`/assets/` 得到空正文的 200 而不是 404 |
| d | `relPath` 改为 `return strings.TrimPrefix(urlPath, prefix)`（不先清理），并删去不再使用的 `path` import | `go test -count=1 ./internal/hub/web/ > /tmp/m5-t8-inj-d.log 2>&1; echo $?` | 1，嵌入产物的 `assets/../robots.txt` 得到 404、`sub/../robots.txt` 得到 index.html（`..` 这一段以 `.` 开头，先被点文件判定当作不存在；embed 本身也拒绝含 `..` 的名字）。替换目录的 `..` 用例照常绿：同一判定先生效，越界另有 os.Root 拒绝，不靠清理 |
| e | `serve.go` 把 `public` 的核对挪到 `store.Open` 之后 | `go test -count=1 -run TestServeRejectsPublicDir ./cmd/hub/ > /tmp/m5-t8-inj-e.log 2>&1; echo $?` | 1，`database touched before --public-dir was checked` |
| f | `dirHeaders` 多加 `h.Set("Referrer-Policy", "no-referrer")` | `go test -count=1 -run TestDirHandlerServesOnly ./internal/hub/web/ > /tmp/m5-t8-inj-f.log 2>&1; echo $?` | 1，`headers: map[…Referrer-Policy…]`（替换目录只允许 §10 的两项） |
| g | `/tmp/m5-t8-ready/check.sh` 的 `until` 判据换回旧的：URL 改成 `http://127.0.0.1:18079/`、期望码改成 `302`（脚本不在仓库里，改完核对文件确实变了，不用 `git diff`） | `sh /tmp/m5-t8-ready/check.sh > /tmp/m5-t8-inj-g.log 2>&1; echo $?` | 1，约 10 秒后 `FAIL: GetSite never returned 200`：旧判据在本提交上等不到。还原脚本 |
| h | `DirHandler` 在构造时 `os.OpenRoot(dir)` 一次，`open` 复用这个 root（不再每个请求重新打开） | `go test -count=1 -run TestDirHandlerFollows ./internal/hub/web/ > /tmp/m5-t8-inj-h.log 2>&1; echo $?` | 1，`after replacement: "old index"`：rename 之后旧的 root 仍指向旧目录 |
| i | `DirHandler` 的普通文件检查改成 `if info, err := f.Stat(); err != nil \|\| info == nil {` | `go test -count=1 -run TestDirHandlerRefuses ./internal/hub/web/ > /tmp/m5-t8-inj-i.log 2>&1; echo $?` | 1，`DirHandler(…/dir-index) error = <nil>`：`index.html` 是目录也被接受 |
| j | `DirHandler` 打开 `index.html` 失败且 `os.IsNotExist(err)` 时直接返回处理器 | 同 i | 1，`…/missing` 与 `…/no-index` 两个目录的 `error = <nil>` |
| k | `serve.go` 的 `newMux` 把 `mountOf(web.Prefix, web.Handler())` 改成 `mountOf(web.Prefix, public)` | `go test -count=1 -run TestServePublicDirReplaces ./cmd/hub/ > /tmp/m5-t8-inj-k.log 2>&1; echo $?` | 1，`/admin/ was shadowed: 200 "custom site"` |
| l | `notBuiltPublic` 的文案改成面板的那句（`The admin panel has not been built …`） | `go test -count=1 -run TestUnbuilt ./internal/hub/web/ > /tmp/m5-t8-inj-l.log 2>&1; echo $?` | 1，`dist-public: status 503 body "…The admin panel has not been built…"` |
| m | `builtinHeaders` 删去 `h.Set("Content-Security-Policy", csp)` | `go test -count=1 -run TestEmbedded ./internal/hub/web/ > /tmp/m5-t8-inj-m.log 2>&1; echo $?` | 1，`security headers missing or incorrect`，`panel` 与 `public` 两个挂载点的子测试都红 |
| n | `serve.go` 的 `public := web.PublicHandler()` 改成 `public := web.Handler()`（根路径挂成面板） | `go test -count=1 -run TestServeMountsBuiltinPublicPageAtRoot ./cmd/hub/ > /tmp/m5-t8-inj-n.log 2>&1; echo $?` | 1，`/nodes/3 via serve: … want the built-in public page`：构建过时得到的是引用 `/admin/assets/` 的面板 index.html，没构建时是面板的说明页 |
| o | `serve.go` 的 `newMux` 去掉 `mountOf(pub.Handler())`，并在 `mux :=` 之前加一行 `_ = pub` | `go test -count=1 -run TestServePublicDirReplaces ./cmd/hub/ > /tmp/m5-t8-inj-o.log 2>&1; echo $?` | 1，`RPC path was shadowed: 200 "shadow rpc"`：RPC 路径没挂载时落到替换目录。挂载了的 RPC 路径不被遮蔽由 ServeMux 的最长前缀匹配保证，与挂载顺序无关 |
| p | `dirHeaders` 删去 `h.Set("X-Content-Type-Options", "nosniff")` | `go test -count=1 -run TestDirHandlerServesOnly ./internal/hub/web/ > /tmp/m5-t8-inj-p.log 2>&1; echo $?` | 1，`headers: map[…]` 里没有 `X-Content-Type-Options` |
| q | `DirHandler` 的缓存函数改成 `func(rel string) string { if len(rel) > 7 && rel[:7] == "assets/" { return "public, max-age=31536000, immutable" }; return "no-cache" }`（照搬嵌入产物的规则） | 同 p | 1，`headers: map[…Cache-Control:[public, max-age=31536000, immutable]…]`：替换目录的文件名不保证带内容哈希 |
| r | `serveFiles` 删去 `rel == "assets" \|\| strings.HasPrefix(rel, "assets/")` 那个 404 分支 | `go test -count=1 ./internal/hub/web/ > /tmp/m5-t8-inj-r.log 2>&1; echo $?` | 1，替换目录红在 `status 200 body "site index", want 404`，嵌入产物的 `panel` 与 `public` 两个挂载点的 `assets/missing.js`、`assets/`、`assets` 子用例同样得到 index.html |
| s | `newTestMux` 去掉 `mountOf(pub.Handler())`，并在 `return` 之前加一行 `_ = pub`（与 Task 5 注入 h 同一改动） | `go test -count=1 -run 'TestMuxRejectsAnonymous\|TestMuxAcceptsGET' ./cmd/hub/ > /tmp/m5-t8-inj-s.log 2>&1; echo $?` | 1，四个公开过程在两个测试里各报一次 `status 200 "text/html; charset=utf-8"`（没构建公开页时是 503）：不加 Content-Type 这一条，本任务之后这项改动两个测试都照样绿 |
| t | `serveFiles` 的 `if !hidden(rel) && serveRegular(…)` 去掉 `!hidden(rel) &&` | `go test -count=1 -run 'TestDirHandlerTreatsDotfiles\|TestEmbedded' ./internal/hub/web/ > /tmp/m5-t8-inj-t.log 2>&1; echo $?` | 1，替换目录红在 `status 200 body "SECRET=dir", want 200 "site index"` 等五项；嵌入产物两个挂载点的 `.gitkeep`、`.env`、`.git/config`、`sub/.hidden.txt`、`assets/.hidden.js` 各自红 |
| u | `hidden` 改成只看第一段：`return strings.HasPrefix(rel, ".")` | 同 t | 1，只有 `sub/.hidden.txt` 与 `assets/.hidden.js` 两项红（三个来源各一次）：`status 200 body "hidden page"`、`"hidden asset", want 404` |
| v | `hidden` 改成只看最后一段：`return strings.HasPrefix(path.Base(rel), ".")` | 同 t | 1，`.git/config` 红（三个来源各一次）、替换目录的 `.well-known/security.txt` 红：点目录下名字不带点的文件漏了出来 |

每项：改动 → `git diff --stat` 非空 → 跑命令 → 核对原因 → `git checkout -- <文件>`。

---

### Task 9: 前端共用件：明暗跟随 data-theme，历史图表与读数条从面板页面抽出

**Files:**
- Create: `web/src/lib/colorScheme.ts`、`web/src/lib/colorScheme.test.ts`
- Create: `web/src/palette.test.ts`
- Create: `web/src/components/History.tsx`、`web/src/components/Bar.tsx`、`web/src/lib/poll.ts`
- Modify: `web/src/styles.css`（调色板改用 `light-dark()`）
- Modify: `web/src/components/Chart.tsx`、`web/src/components/Chart.test.tsx`
- Modify: `web/src/pages/NodeDetail.tsx`、`web/src/pages/Overview.tsx`、`web/src/pages/Overview.test.tsx`
- Modify: `web/src/test/harness.tsx`（`renderWithService`）

**Interfaces:**
- Consumes：Task 1 的 `seriesLabels`、`web/src/gen/probe/v1/query_pb.ts`。
- Produces（TS）：
  - `lib/colorScheme.ts`：`type Scheme = "light" | "dark"`；`currentScheme(): Scheme`；`useColorScheme(): Scheme`；`resolveColor(host: HTMLElement, value: string): string`。
  - `lib/poll.ts`：`POLL_MS = 2000`（从 `pages/Overview.tsx` 移来）。
  - `components/Bar.tsx`：`Bar`、`Missing`、`ratio`（从 `pages/Overview.tsx` 移来）。
  - `components/History.tsx`：
    - `type HistoryMethods = { queryMetrics; queryProbes }`（两个 `DescMethodUnary`，请求与响应类型取自 `query_pb`）
    - `useHistory(methods: HistoryMethods, nodeId: bigint, enabled: boolean)`，返回 `{ range, setRange, metrics, probes, charts, probeCharts }`
    - `type HistoryState = ReturnType<typeof useHistory>`
    - `RangePicker({ history })`、`HistoryCharts({ history, noProbes })`
  - `test/harness.tsx`：`renderWithService<S extends DescService>(service, impl, routes, initialPath)`；`renderWithAdmin` 改为委托它。

`History.tsx` 只 import `query_pb`，不 import 任何服务的生成代码：公开页（Task 10）用同一份组件，Task 10 的 import 扫描会钉住这一点。

- [ ] **Step 1: 写失败测试**

`web/src/lib/colorScheme.test.ts`：

```ts
import { act, renderHook, waitFor } from "@testing-library/react";
import { afterEach, expect, it, vi } from "vitest";
import { currentScheme, useColorScheme } from "./colorScheme";

let systemDark = false;
let changed = () => {};

function stubSystem() {
  vi.stubGlobal("matchMedia", () => ({
    get matches() { return systemDark; },
    addEventListener: (_: string, listener: () => void) => { changed = listener; },
    removeEventListener: vi.fn(),
  }));
}

afterEach(() => {
  vi.unstubAllGlobals();
  delete document.documentElement.dataset.theme;
  systemDark = false;
});

it("data-theme 压过系统设置，没有或不认识时跟随系统", () => {
  stubSystem();
  expect(currentScheme()).toBe("light");
  systemDark = true;
  expect(currentScheme()).toBe("dark");
  document.documentElement.dataset.theme = "light";
  expect(currentScheme()).toBe("light");
  systemDark = false;
  document.documentElement.dataset.theme = "dark";
  expect(currentScheme()).toBe("dark");
  document.documentElement.dataset.theme = "auto";
  expect(currentScheme()).toBe("light");
});

it("useColorScheme 随系统与 data-theme 的变化更新", async () => {
  stubSystem();
  const { result, unmount } = renderHook(() => useColorScheme());
  expect(result.current).toBe("light");
  act(() => { systemDark = true; changed(); });
  expect(result.current).toBe("dark");
  await act(async () => { document.documentElement.dataset.theme = "light"; });
  await waitFor(() => expect(result.current).toBe("light"));
  // 先卸载再由 afterEach 撤掉 matchMedia 的替身：挂着的订阅会在删 data-theme 时再读一次系统设置。
  unmount();
});
```

`web/src/palette.test.ts`：

```ts
// @ts-nocheck -- 这个测试读仓库文件，app tsconfig 只带 vite/client，没有 node 类型。
// @vitest-environment node
import { readFileSync } from "node:fs";
import { dirname, resolve } from "node:path";
import { fileURLToPath } from "node:url";
import { expect, it } from "vitest";

const css = readFileSync(resolve(dirname(fileURLToPath(import.meta.url)), "styles.css"), "utf8");

// 明暗只经 color-scheme 切换：公开页把站点设置写在 html 的 data-theme 上，它要能压过系统设置。
// 按系统明暗的媒体查询改写变量会绕过 data-theme，所以一处都不能有。浏览器里的实际效果由公开页的浏览器验收核对。
it("调色板随 color-scheme 取值，data-theme 能强制明暗", () => {
  expect(css).not.toMatch(/prefers-color-scheme/);
  const root = css.match(/:root\s*\{([^}]*)\}/)[1];
  expect(root).toMatch(/color-scheme:\s*light dark/);
  for (const name of ["--bg", "--card", "--fg", "--muted", "--line", "--accent"]) {
    expect(root, name).toMatch(new RegExp(`${name}:\\s*light-dark\\(`));
  }
  expect(css).toMatch(/:root\[data-theme="light"\]\s*\{\s*color-scheme:\s*light;?\s*\}/);
  expect(css).toMatch(/:root\[data-theme="dark"\]\s*\{\s*color-scheme:\s*dark;?\s*\}/);
});
```

`web/src/components/Chart.test.tsx` 整体替换为：

```tsx
import { act, render, waitFor } from "@testing-library/react";
import { afterEach, expect, it, vi } from "vitest";
import type { AlignedData, Options } from "uplot";
import { currentScheme } from "../lib/colorScheme";
import { Chart } from "./Chart";

const plots = vi.hoisted(() => [] as { options: Options; data: AlignedData; setData: ReturnType<typeof vi.fn>; destroy: ReturnType<typeof vi.fn> }[]);
vi.mock("uplot", () => ({ default: class {
  setData = vi.fn();
  setSize = vi.fn();
  destroy = vi.fn();
  constructor(public options: Options, public data: AlignedData) { plots.push(this); }
} }));

afterEach(() => {
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
  delete document.documentElement.dataset.theme;
  plots.length = 0;
});

// jsdom 不解析 var() 与 light-dark()：按探针元素上写的值，给出浏览器在当前明暗下会解析出的颜色。
function stubResolvedColors() {
  const real = window.getComputedStyle.bind(window);
  vi.spyOn(window, "getComputedStyle").mockImplementation((el, pseudo) => {
    const dark = currentScheme() === "dark";
    const resolved: Record<string, string> = {
      "var(--muted)": dark ? "rgb(156, 163, 175)" : "rgb(107, 114, 128)",
      "var(--line)": dark ? "rgb(42, 47, 58)" : "rgb(229, 231, 235)",
    };
    const value = (el as HTMLElement).style?.color;
    return value in resolved ? ({ color: resolved[value] } as CSSStyleDeclaration) : real(el, pseudo);
  });
}

it("数据原地更新；系统或 data-theme 改变明暗时，用最新数据和解析后的配色重建", async () => {
  let dark = false;
  let changed = () => {};
  vi.stubGlobal("matchMedia", () => ({ get matches() { return dark; }, addEventListener: (_: string, listener: () => void) => { changed = listener; }, removeEventListener: vi.fn() }));
  vi.stubGlobal("ResizeObserver", class { observe() {} disconnect() {} });
  stubResolvedColors();
  const light = ["rgb(107, 114, 128)", "rgb(229, 231, 235)", "rgb(107, 114, 128)"];
  const darkColors = ["rgb(156, 163, 175)", "rgb(42, 47, 58)", "rgb(156, 163, 175)"];
  const { rerender, unmount } = render(<Chart data={[[0], [1]]} labels={["cpu"]} unit="count" />);
  const colors = () => plots.at(-1)!.options.axes!.map((axis) => [axis.stroke, axis.grid?.stroke, axis.ticks?.stroke]);
  expect(colors()).toEqual([light, light]);
  const next: AlignedData = [[0], [2]];
  rerender(<Chart data={next} labels={["cpu"]} unit="count" />);
  expect(plots).toHaveLength(1);
  expect(plots[0].setData).toHaveBeenLastCalledWith(next);
  act(() => { dark = true; changed(); });
  expect(colors()).toEqual([darkColors, darkColors]);
  expect(plots.at(-1)!.data).toEqual(next);
  // 站点设置强制浅色：html 的 data-theme 压过系统的深色。
  await act(async () => { document.documentElement.dataset.theme = "light"; });
  await waitFor(() => expect(colors()).toEqual([light, light]));
  expect(plots.at(-1)!.data).toEqual(next);
  unmount();
});
```

- [ ] **Step 2: 跑红**

```bash
cd /Users/xjetry/work/vibe/probe-public && pnpm --dir web exec vitest run src/lib/colorScheme.test.ts src/palette.test.ts src/components/Chart.test.tsx > /tmp/m5-t9-red.log 2>&1; echo $?
```

Expected：1。
- `colorScheme.test.ts` 与 `Chart.test.tsx`：`Failed to resolve import "./colorScheme"`（或 `"../lib/colorScheme"`）。
- `palette.test.ts`：`not to match /prefers-color-scheme/`。

- [ ] **Step 3: 实现明暗**

`web/src/lib/colorScheme.ts`：

```ts
import { useSyncExternalStore } from "react";

export type Scheme = "light" | "dark";

const systemDark = () => window.matchMedia("(prefers-color-scheme: dark)");

// 页面的明暗由两处决定：html 的 data-theme（公开页按站点设置写 light 或 dark）优先，没有时跟随系统。
// styles.css 的 color-scheme 规则按同一优先级写，图表读到的与 CSS 是同一个结果。
export function currentScheme(): Scheme {
  const forced = document.documentElement.dataset.theme;
  if (forced === "light" || forced === "dark") return forced;
  return systemDark().matches ? "dark" : "light";
}

function subscribe(onChange: () => void): () => void {
  const media = systemDark();
  media.addEventListener("change", onChange);
  const observer = new MutationObserver(onChange);
  observer.observe(document.documentElement, { attributes: true, attributeFilter: ["data-theme"] });
  return () => {
    media.removeEventListener("change", onChange);
    observer.disconnect();
  };
}

export function useColorScheme(): Scheme {
  return useSyncExternalStore(subscribe, currentScheme);
}

// canvas 不认 light-dark() 与 var()：放一个临时元素，让浏览器把 CSS 值解析成具体颜色，再交给 uPlot。
// host 须已挂在文档里，解析才会带上它继承的 color-scheme。
export function resolveColor(host: HTMLElement, value: string): string {
  const probe = document.createElement("span");
  probe.style.color = value;
  host.append(probe);
  const color = getComputedStyle(probe).color;
  probe.remove();
  return color;
}
```

`web/src/styles.css` 开头的 `:root { … }` 与 `@media (prefers-color-scheme: dark) { … }` 两块替换为：

```css
/* 明暗只经 color-scheme 切换：light-dark() 按元素的 color-scheme 取值，默认跟随系统；公开页按站点设置在 html 上
   写 data-theme，下面两条规则让它压过系统设置。不要再按系统明暗的媒体查询改写变量，那会绕过 data-theme。 */
:root {
  color-scheme: light dark;
  --bg: light-dark(#f6f7f9, #0f1115); --card: light-dark(#ffffff, #171a21); --fg: light-dark(#1f2328, #e5e7eb);
  --muted: light-dark(#6b7280, #9ca3af); --line: light-dark(#e5e7eb, #2a2f3a);
  --accent: light-dark(#2563eb, #60a5fa); --ok: #16a34a; --bad: #dc2626; --warn: #d97706;
  font-family: system-ui, -apple-system, "Segoe UI", sans-serif; font-size: 14px; color: var(--fg); background: var(--bg);
}
:root[data-theme="light"] { color-scheme: light; }
:root[data-theme="dark"] { color-scheme: dark; }
```

`web/src/components/Chart.tsx`：
- 第 1 行改为 `import { useEffect, useRef } from "react";`，在 `import { axisValues } from "../lib/axis";` 之后加 `import { resolveColor, useColorScheme } from "../lib/colorScheme";`。
- 删去 `dark` 状态与订阅 `matchMedia` 的那个 `useEffect`，换成一行 `const scheme = useColorScheme();`。
- 建图 effect 开头读颜色的三行改为：

```ts
    const axisColor = resolveColor(host, "var(--muted)");
    const gridColor = resolveColor(host, "var(--line)");
```

- 建图 effect 的依赖与注释改为：

```ts
    // 标签、单位、尺寸或明暗改变才重建；下面的数据 effect 维护最近提交的数据快照并应用当前数据。
  }, [key, unit, height, scheme]);
```

改完的 `Chart.tsx` 全文：

```tsx
import { useEffect, useRef } from "react";
import uPlot, { type AlignedData, type Options } from "uplot";
import "uplot/dist/uPlot.min.css";
import { formatUnit } from "../lib/format";
import { axisValues } from "../lib/axis";
import { resolveColor, useColorScheme } from "../lib/colorScheme";

// 一个节点常有多条探测线，八色减少颜色重复；超过八条时循环使用。
const palette = ["#3b82f6", "#f59e0b", "#10b981", "#ef4444", "#8b5cf6", "#06b6d4", "#84cc16", "#ec4899"];

// spanGaps 关闭：null 是无读数，线在这里必须断开而不是把两侧连起来。
export function Chart({ data, labels, unit, height = 180 }: { data: AlignedData; labels: string[]; unit: string; height?: number }) {
  const el = useRef<HTMLDivElement>(null);
  const plot = useRef<uPlot | null>(null);
  const initialData = useRef(data);
  const key = labels.join("|");
  const scheme = useColorScheme();
  useEffect(() => {
    const host = el.current;
    if (!host) return;
    const axisColor = resolveColor(host, "var(--muted)");
    const gridColor = resolveColor(host, "var(--line)");
    const axisStyle = { stroke: axisColor, grid: { stroke: gridColor }, ticks: { stroke: axisColor } };
    const opts: Options = {
      width: host.clientWidth || 600,
      height,
      scales: { x: { time: true }, y: unit === "percent" ? { range: [0, 100] } : {} },
      axes: [{ ...axisStyle }, { ...axisStyle, size: 80, values: (_u, vals) => axisValues(vals, unit) }],
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
    plot.current = new uPlot(opts, initialData.current, host);
    const ro = new ResizeObserver(() => plot.current?.setSize({ width: host.clientWidth, height }));
    ro.observe(host);
    return () => {
      ro.disconnect();
      plot.current?.destroy();
      plot.current = null;
    };
    // 标签、单位、尺寸或明暗改变才重建；下面的数据 effect 维护最近提交的数据快照并应用当前数据。
  }, [key, unit, height, scheme]);
  useEffect(() => {
    initialData.current = data;
    plot.current?.setData(data);
  }, [data]);
  return <div ref={el} className="chart" />;
}
```

- [ ] **Step 4: 跑绿**

```bash
cd /Users/xjetry/work/vibe/probe-public && pnpm --dir web exec vitest run src/lib/colorScheme.test.ts src/palette.test.ts src/components/Chart.test.tsx > /tmp/m5-t9-green1.log 2>&1; echo $?
```

Expected：0。

- [ ] **Step 5: 抽出共用件**

面板的 `NodeDetail` 与 `Overview` 改用抽出的组件，行为不变；它们现有的测试就是这一步的回归网。

`web/src/lib/poll.ts`：

```ts
// 实时视图靠轮询（§10 默认 2 秒）；hub 的上报间隔不会更短，2 秒是让"刚上报"尽快可见的取值。
export const POLL_MS = 2000;
```

`web/src/components/Bar.tsx`（内容从 `pages/Overview.tsx` 原样移来，`Missing` 与 `ratio` 改为导出）：

```tsx
// 无读数与 0 是两个事实：缺失的字段显示为破折号，不画成 0。
export function Missing() {
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

export function ratio(used: bigint, total: bigint): number {
  return (Number(used) / Number(total)) * 100;
}
```

`web/src/components/History.tsx`（时间范围、两族查询与图表从 `pages/NodeDetail.tsx` 移来，查询的方法由调用方传入）：

```tsx
import type { DescMethodUnary } from "@bufbuild/protobuf";
import { useQuery } from "@connectrpc/connect-query";
import { keepPreviousData } from "@tanstack/react-query";
import { type ReactNode, useEffect, useMemo, useState } from "react";
import type { QueryMetricsRequestSchema, QueryMetricsResponseSchema, QueryProbesRequestSchema, QueryProbesResponseSchema } from "../gen/probe/v1/query_pb";
import { lossPercent, rttMeanMs, seriesLabels, taskIdsOf, toProbeAligned, type ProbeValue } from "../lib/probes";
import { toAligned, unitOf } from "../lib/series";
import { Chart } from "./Chart";

const RANGES = [
  { label: "1h", seconds: 3600 },
  { label: "6h", seconds: 6 * 3600 },
  { label: "24h", seconds: 86400 },
  { label: "7d", seconds: 7 * 86400 },
  { label: "30d", seconds: 30 * 86400 },
];

// 每个面板画哪些指标；名字与 hub 的描述表一致，单位随数据来。可加量（字节增量）以速率
// 作图，单位由面板指定：数据里的 bytes 是一个点内的总和，图上要的是 bytes/s。
const PANELS: { title: string; names: string[]; unit?: string }[] = [
  { title: "CPU", names: ["cpu"] },
  { title: "内存 / 交换", names: ["mem_used", "swap_used"] },
  { title: "磁盘", names: ["disk_used"] },
  { title: "负载（1 分钟）", names: ["load1"] },
  { title: "连接数", names: ["tcp", "udp"] },
  { title: "进程数", names: ["procs"] },
  { title: "网络", names: ["rx_bytes", "tx_bytes"], unit: "bytes/s" },
];

// 探测图两张：丢包率与 RTT 均值，每个任务一条线。单位不随数据来——探测样本没有 unit 字段，
// 两种量各自固定。
const PROBE_PANELS: { title: string; unit: string; value: ProbeValue }[] = [
  { title: "探测 · 丢包率", unit: "percent", value: lossPercent },
  { title: "探测 · RTT 均值", unit: "ms", value: rttMeanMs },
];

// 窗口右端每分钟前进一次：历史行本来就按分钟产生，更频繁的刷新看不到新东西。
const REFRESH_MS = 60_000;

// 两族历史查询在管理与公开两个服务上各有一份，请求与响应类型相同（query.proto）。图表按此共用，
// 调用方只决定查哪个服务；本文件不引用任何服务的生成代码，公开页因此能用它。
export type HistoryMethods = {
  queryMetrics: DescMethodUnary<typeof QueryMetricsRequestSchema, typeof QueryMetricsResponseSchema>;
  queryProbes: DescMethodUnary<typeof QueryProbesRequestSchema, typeof QueryProbesResponseSchema>;
};

export function useHistory(methods: HistoryMethods, nodeId: bigint, enabled: boolean) {
  const [range, setRange] = useState(RANGES[2]);
  const [to, setTo] = useState(() => Math.floor(Date.now() / 1000) + 60);
  useEffect(() => {
    const t = setInterval(() => setTo(Math.floor(Date.now() / 1000) + 60), REFRESH_MS);
    return () => clearInterval(t);
  }, []);
  const from = to - range.seconds;
  const request = { nodeId, from: BigInt(from), to: BigInt(to), maxPoints: 1000 };
  const metrics = useQuery(methods.queryMetrics, request, { enabled, placeholderData: keepPreviousData });
  const probes = useQuery(methods.queryProbes, request, { enabled, placeholderData: keepPreviousData });
  const charts = useMemo(
    () => metrics.data ? PANELS.map((p) => ({ ...p, data: toAligned(metrics.data, p.names, from, to), unit: p.unit ?? unitOf(metrics.data, p.names[0]) })) : [],
    [metrics.data, from, to],
  );
  // 标签随序列下发（任务当前的种类与目标），与数据同一次响应到达，不另查任务列表。
  const probeCharts = useMemo(() => {
    if (!probes.data) return [];
    const ids = taskIdsOf(probes.data);
    const labels = seriesLabels(probes.data.series);
    return PROBE_PANELS.map((p) => ({ ...p, labels, data: toProbeAligned(probes.data!, ids, from, to, p.value) }));
  }, [probes.data, from, to]);
  return { range, setRange, metrics, probes, charts, probeCharts };
}

export type HistoryState = ReturnType<typeof useHistory>;

export function RangePicker({ history }: { history: HistoryState }) {
  const { range, setRange, metrics } = history;
  return (
    <>
      <nav aria-label="时间窗口">
        {RANGES.map((r) => (
          <button key={r.label} type="button" className={r.label === range.label ? "active" : "link"} onClick={() => setRange(r)} aria-pressed={r.label === range.label}>
            {r.label}
          </button>
        ))}
      </nav>
      {metrics.data && <span className="muted">级别 {metrics.data.level}，每点 {metrics.data.stepS}s</span>}
    </>
  );
}

// noProbes 是窗口内没有探测结果时的说明：面板给出去任务页的链接，公开页只说明没有。
export function HistoryCharts({ history, noProbes }: { history: HistoryState; noProbes: ReactNode }) {
  const { charts, probeCharts, probes } = history;
  return (
    <>
      <div className="grid">
        {charts.map((c) => (
          <div className="card" key={c.title}>
            <h2>{c.title}</h2>
            <Chart data={c.data} labels={c.names} unit={c.unit} />
          </div>
        ))}
      </div>
      {probes.data && probes.data.series.length === 0 && noProbes}
      {probes.data && probes.data.series.length > 0 && (
        <div className="grid">
          {probeCharts.map((c) => (
            <div className="card" key={c.title}>
              <h2>{c.title}</h2>
              <Chart data={c.data} labels={c.labels} unit={c.unit} />
            </div>
          ))}
        </div>
      )}
    </>
  );
}
```

`web/src/pages/Overview.tsx`：
- 删去 `POLL_MS` 的定义及其注释，删去文件末尾的 `ratio`、`Missing`、`Bar` 三个函数。
- import 区加：

```ts
import { POLL_MS } from "../lib/poll";
import { Bar, Missing, ratio } from "../components/Bar";
```

`web/src/pages/Overview.test.tsx` 第 5 行 `import { Overview, POLL_MS } from "./Overview";` 改为两行：

```ts
import { POLL_MS } from "../lib/poll";
import { Overview } from "./Overview";
```

`web/src/pages/NodeDetail.tsx`：从文件开头到 `NodeDetail` 函数结束（`const GIB = 2 ** 30;` 之前）整体替换为下面这段；`TrafficCard` 及其以下不变。

```tsx
import { createConnectQueryKey, useMutation, useQuery } from "@connectrpc/connect-query";
import { useQueryClient } from "@tanstack/react-query";
import { type FormEvent, useState } from "react";
import { Link, useParams } from "react-router";
import { errorBanner, queryGate } from "../api/queryGate";
import { HistoryCharts, RangePicker, useHistory, type HistoryMethods } from "../components/History";
import { AdminService, type GetTrafficResponse } from "../gen/probe/v1/admin_pb";
import { bytes } from "../lib/format";
import { errorText } from "../api/auth";

const ADMIN_HISTORY: HistoryMethods = { queryMetrics: AdminService.method.queryMetrics, queryProbes: AdminService.method.queryProbes };

// 周期量随每次上报更新；流量卡以 10 秒节奏展示内存视图的变化，不依赖落盘刷出。
export const TRAFFIC_MS = 10_000;

export function NodeDetail() {
  const { id } = useParams();
  const validId = /^\d+$/.test(id ?? "");
  const nodeId = validId ? BigInt(id!) : 0n;
  const nodes = useQuery(AdminService.method.listNodes, {}, { enabled: validId });
  const history = useHistory(ADMIN_HISTORY, nodeId, validId);
  // 流量与图表面向不同查询，各自降级；校正操作在卡片内保留自己的错误槽位。
  const traffic = useQuery(AdminService.method.getTraffic, {}, { enabled: validId, refetchInterval: TRAFFIC_MS });

  if (!validId) return <p role="alert" className="error">节点 {id} 不存在。<Link to="/">返回总览</Link></p>;
  const gate = queryGate(nodes);
  if (!gate.ready) return gate.loading ?? errorBanner(...gate.errors);
  const node = gate.data.nodes.find((n) => n.id === nodeId);
  if (!node) return <p role="alert" className="error">节点 {id} 不存在。<Link to="/">返回总览</Link></p>;
  return (
    <section>
      {errorBanner(nodes.error, history.metrics.error, history.probes.error, traffic.error)}
      <header className="row detail-header">
        <h1>{node.name}</h1>
        <Link to={`/events?node=${id}`}>告警事件</Link>
        <RangePicker history={history} />
      </header>
      <TrafficCard nodeId={nodeId} data={traffic.data} />
      <HistoryCharts history={history} noProbes={<p className="muted">窗口内没有探测结果。<Link to="/probes">管理探测任务</Link></p>} />
      {node.facts && (
        <dl className="card facts">
          <dt>主机名</dt><dd>{node.facts.hostname}</dd>
          <dt>系统</dt><dd>{node.facts.os}</dd>
          <dt>内核</dt><dd>{node.facts.kernel}</dd>
          <dt>架构</dt><dd>{node.facts.arch}</dd>
          <dt>CPU</dt><dd>{node.facts.cpuModel} × {node.facts.cpuCores}</dd>
          <dt>虚拟化</dt><dd>{node.facts.virtualization || "无 / 未知"}</dd>
          <dt>agent</dt><dd>{node.facts.agentVersion}</dd>
          <dt>ICMP 探测</dt><dd>{node.facts.icmpAvailable ? "可用" : "不可用"}</dd>
        </dl>
      )}
    </section>
  );
}
```

`web/src/test/harness.tsx` 整体替换为：

```tsx
import type { DescService } from "@bufbuild/protobuf";
import { createRouterTransport, type ServiceImpl } from "@connectrpc/connect";
import { TransportProvider } from "@connectrpc/connect-query";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render } from "@testing-library/react";
import { createMemoryRouter, RouterProvider, type RouteObject } from "react-router";
import { AdminService } from "../gen/probe/v1/admin_pb";

export type AdminImpl = Partial<ServiceImpl<typeof AdminService>>;

// 页面测试使用内存服务和路由并关闭重试，不安装全局认证跳转；生产 client 的跳转由入口测试验证。
export function renderWithService<S extends DescService>(service: S, impl: Partial<ServiceImpl<S>>, routes: RouteObject[], initialPath: string) {
  const transport = createRouterTransport(({ service: register }) => {
    register(service, impl);
  });
  const router = createMemoryRouter(routes, { initialEntries: [initialPath] });
  const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  render(
    <TransportProvider transport={transport}>
      <QueryClientProvider client={queryClient}>
        <RouterProvider router={router} />
      </QueryClientProvider>
    </TransportProvider>,
  );
  return { router, queryClient };
}

export function renderWithAdmin(impl: AdminImpl, routes: RouteObject[], initialPath: string) {
  return renderWithService(AdminService, impl, routes, initialPath);
}
```

- [ ] **Step 6: 跑绿（全量）**

```bash
cd /Users/xjetry/work/vibe/probe-public && pnpm --dir web exec tsc -b > /tmp/m5-t9-tsc.log 2>&1; echo $?
cd /Users/xjetry/work/vibe/probe-public && pnpm --dir web exec vitest run > /tmp/m5-t9-green.log 2>&1; echo $?
```

Expected：两条都是 0。`NodeDetail.test.tsx`、`Overview.test.tsx` 的全部用例不改断言照常通过。

- [ ] **Step 7: 提交**

```bash
cd /Users/xjetry/work/vibe/probe-public && git add web/src && git commit -m "web: 明暗改由 color-scheme 切换并跟随 data-theme，历史图表与读数条抽成共用组件" -m "调色板改用 light-dark()，:root[data-theme] 覆盖 color-scheme，删除按系统明暗改写变量的媒体查询：公开页按站点设置写在 html 上的 data-theme 因此能压过系统设置。图表经 useColorScheme 跟随同一优先级，颜色由浏览器在图表所在元素上解析成 rgb 再交给 uPlot（canvas 不认 light-dark() 与 var()）。时间范围、两族历史查询与图表抽成 components/History，查询的方法由调用方传入，组件本身不引用任何服务的生成代码；读数条与轮询间隔同样抽出，供公开页共用。" > /tmp/m5-t9-commit.log 2>&1; echo $?
```

Expected：0。

- [ ] **Step 8: 缺陷注入**

逐项进行：改动 → `git diff --stat` 非空 → 跑命令 → 核对红的原因 → `git checkout -- <文件>`。

| | 改动 | 命令 | 期望 |
|---|---|---|---|
| a | `colorScheme.ts` 删去 `if (forced === "light" \|\| forced === "dark") return forced;` | 同 Step 2 | 1，`colorScheme.test.ts` 红在 `expected 'dark' to be 'light'`；`Chart.test.tsx` 红在切到 `data-theme="light"` 之后的配色 |
| b | `subscribe` 去掉 `MutationObserver`（只订阅系统明暗） | 同 Step 2 | 1，"useColorScheme 随系统与 data-theme 的变化更新"红在 `expected 'dark' to be 'light'`；`Chart.test.tsx` 同 a |
| c | `styles.css` 在 `:root[data-theme="light"]` 一行后加回 `@media (prefers-color-scheme: dark) { :root { --bg: #0f1115; } }` | 同 Step 2 | 1，`palette.test.ts` 红在 `not to match /prefers-color-scheme/` |
| d | `styles.css` 把 `--muted: light-dark(#6b7280, #9ca3af);` 改成 `--muted: #6b7280;` | 同 Step 2 | 1，`palette.test.ts` 红在 `--muted: expected … to match /--muted:\s*light-dark\(/` |
| e | `Chart.tsx` 的 `resolveColor` 两行换回 `getComputedStyle(document.documentElement).getPropertyValue("--muted").trim()` 与 `--line` 的同形写法 | 同 Step 2 | 1，`Chart.test.tsx` 红在 `expected [ [ '', '', '' ], [ '', '', '' ] ] to deeply equal …` |
| f | `Chart.tsx` 建图 effect 的依赖去掉 `scheme` | 同 Step 2 | 1，`Chart.test.tsx` 红在系统切到深色之后的配色（仍是浅色） |

e 的红同时说明：jsdom 里读变量得到空串，真实浏览器里读到的是 `light-dark(…)` 原串（实验 5），两者都不是 canvas 能用的颜色。解析的真实效果由 Task 10 的浏览器验收核对。

---

### Task 10: 公开页：第二个 Vite 入口

**Files:**
- Create: `web/src/public/index.html`、`main.tsx`、`transport.ts`、`router.tsx`、`site.ts`、`Layout.tsx`、`Overview.tsx`、`NodePage.tsx`、`public.css`
- Test: `web/src/public/importScan.test.ts`、`site.test.ts`、`Overview.test.tsx`、`NodePage.test.tsx`、`main.test.tsx`
- Modify: `web/vite.config.ts`、`web/package.json`（`build` 脚本）、`Makefile`（仅 `web` 目标的注释）

**Interfaces:**
- Consumes：
  - Task 5 生成的 `web/src/gen/probe/v1/public_pb.ts`：`PublicService`、`PublicSite`、`PublicNode`。
  - Task 8：`internal/hub/web/dist-public` 是 embed 目录，hub 在 `/` 服务它（`.gitkeep` 入库，其余忽略）。
  - Task 9：`History.tsx`、`Bar.tsx`、`poll.ts`、`renderWithService`、`styles.css` 的 `data-theme` 规则。
- Produces：
  - `web/src/public/site.ts`：`DEFAULT_TITLE = "服务器状态"`；`applySite(site: PublicSite): () => void`。
  - 构建：`pnpm --dir web run build` 先后产出 `internal/hub/web/dist`（面板，base `/admin/`）与 `internal/hub/web/dist-public`（公开页，base `/`）。`make web` 不变，仍调用这条脚本。

公开页的请求用 GET、`credentials: "omit"`：GET 让 hub 的 `Cache-Control` 生效；不带 cookie，公开请求与登录状态无关。入口的 import 链不触达 `admin_pb.ts`，由 `importScan.test.ts` 钉住，Step 6 再从构建产物里核对一次。

- [ ] **Step 1: 写失败测试**

`web/src/public/importScan.test.ts`：

```ts
// @ts-nocheck -- 这个测试读仓库文件，app tsconfig 只带 vite/client，没有 node 类型。
// @vitest-environment node
import { readFileSync, statSync } from "node:fs";
import { dirname, relative, resolve } from "node:path";
import { fileURLToPath } from "node:url";
import { expect, it } from "vitest";

const src = resolve(dirname(fileURLToPath(import.meta.url)), "..");
const adminGen = resolve(src, "gen/probe/v1/admin_pb.ts");
// import / export … from "…"、副作用 import "…" 与动态 import("…")；类型 import 同样计入：
// 卫生规则针对源码依赖，不只针对打包结果。
const SPECIFIER = /\b(?:import|export)\b[^"'`;]*?\bfrom\s*["']([^"']+)["']|\bimport\s*\(?\s*["']([^"']+)["']/g;

function resolveImport(dir, spec) {
  const base = resolve(dir, spec);
  for (const candidate of [base, `${base}.ts`, `${base}.tsx`, resolve(base, "index.ts"), resolve(base, "index.tsx")]) {
    if (statSync(candidate, { throwIfNoEntry: false })?.isFile()) return candidate;
  }
  throw new Error(`cannot resolve ${spec} from ${relative(src, dir)}`);
}

// reachable 从入口沿相对 import 走到底，返回触达的每个文件与引入它的文件。包名 import（react、@connectrpc/…）
// 不展开：它们不会反向引用本仓库的生成代码。
function reachable(entry) {
  const parent = new Map([[entry, null]]);
  const stack = [entry];
  while (stack.length > 0) {
    const file = stack.pop();
    if (!/\.(ts|tsx)$/.test(file)) continue;
    for (const m of readFileSync(file, "utf8").matchAll(SPECIFIER)) {
      const spec = m[1] ?? m[2];
      if (!spec.startsWith(".")) continue;
      const next = resolveImport(dirname(file), spec);
      if (!parent.has(next)) {
        parent.set(next, file);
        stack.push(next);
      }
    }
  }
  return parent;
}

function chain(parent, file) {
  const out = [];
  for (let f = file; f; f = parent.get(f)) out.unshift(relative(src, f));
  return out.join(" → ");
}

it("公开入口不触达管理服务的生成代码", () => {
  const parent = reachable(resolve(src, "public/main.tsx"));
  // 冒烟：确实走进了公开服务的生成代码与共用组件，空集不能冒充通过。
  for (const f of ["gen/probe/v1/public_pb.ts", "gen/probe/v1/query_pb.ts", "components/History.tsx", "components/Chart.tsx", "styles.css"]) {
    expect(parent.has(resolve(src, f)), f).toBe(true);
  }
  expect(parent.has(adminGen), parent.has(adminGen) ? chain(parent, adminGen) : "").toBe(false);
});

it("同一套扫描从面板入口看得到 admin_pb", () => {
  expect(reachable(resolve(src, "main.tsx")).has(adminGen)).toBe(true);
});
```

`web/src/public/site.test.ts`（Review Focus 2 的客户端一半在最后一个用例）：

```ts
import { create } from "@bufbuild/protobuf";
import { afterEach, expect, it } from "vitest";
import { PublicSiteSchema } from "../gen/probe/v1/public_pb";
import { applySite, DEFAULT_TITLE } from "./site";

afterEach(() => {
  document.head.innerHTML = "";
  document.documentElement.removeAttribute("style");
  delete document.documentElement.dataset.theme;
  delete document.body.dataset.pwned;
});

it("明暗、主色与标题写到文档上，撤销后恢复；auto 跟随系统", () => {
  const root = document.documentElement;
  const undo = applySite(create(PublicSiteSchema, { title: "机房", theme: "dark", accentColor: "#123abc" }));
  expect(root.dataset.theme).toBe("dark");
  expect(root.style.getPropertyValue("--accent")).toBe("#123abc");
  expect(document.title).toBe("机房");
  undo();
  expect(root.dataset.theme).toBeUndefined();
  expect(root.style.getPropertyValue("--accent")).toBe("");
  applySite(create(PublicSiteSchema, { theme: "auto" }));
  expect(root.dataset.theme).toBeUndefined();
  expect(document.title).toBe(DEFAULT_TITLE);
});

it("自定义 CSS 排在已有样式之后，撤销时移除", () => {
  const builtIn = document.createElement("link");
  builtIn.rel = "stylesheet";
  document.head.append(builtIn);
  const undo = applySite(create(PublicSiteSchema, { theme: "auto", customCss: "body { color: red }" }));
  const last = document.head.lastElementChild as HTMLStyleElement;
  expect(last.tagName).toBe("STYLE");
  expect(last.dataset.siteCss).toBe("");
  expect(last.textContent).toBe("body { color: red }");
  undo();
  expect(document.head.querySelector("style[data-site-css]")).toBeNull();
});

// hub 拒绝含 "</" 的 CSS；即便有这样的值到达，textContent 也不经 HTML 解析，产生不了任何元素。
it("CSS 按文本写入，不产生元素", () => {
  const css = `</style><img src=x onerror="document.body.dataset.pwned='1'">`;
  applySite(create(PublicSiteSchema, { theme: "auto", customCss: css }));
  expect(document.querySelectorAll("img")).toHaveLength(0);
  expect((document.head.lastElementChild as HTMLStyleElement).textContent).toBe(css);
  expect(document.body.dataset.pwned).toBeUndefined();
});
```

`web/src/public/Overview.test.tsx`：

```tsx
import { act, screen, waitFor, within } from "@testing-library/react";
import { afterEach, expect, it, vi } from "vitest";
import { PublicService } from "../gen/probe/v1/public_pb";
import { POLL_MS } from "../lib/poll";
import { renderWithService } from "../test/harness";
import { PublicOverview } from "./Overview";

const snapshot = {
  now: 1_000n,
  reportIntervalMs: 4000,
  nodes: [
    {
      id: 3n, name: "web-1", online: true, lastSeenAt: 998n, sortOrder: 0,
      facts: { os: "Debian 12", arch: "amd64" },
      metrics: { cpuPct: 42, memUsed: 512n * 1024n ** 2n, memTotal: 1024n ** 3n, diskUsed: 0n, diskTotal: 10n * 1024n ** 3n,
        netRxBps: 2048n, netTxBps: 1024n, uptimeS: 90_000n },
      traffic: { periodRx: 1024n ** 3n, periodTx: 0n },
    },
    { id: 4n, name: "db-1", online: false, sortOrder: 1 },
  ],
};

afterEach(() => vi.useRealTimers());

it("每个公开节点一张卡片：名称、在线、系统与架构、读数、运行时长与本周期流量", async () => {
  renderWithService(PublicService, { getSnapshot: async () => snapshot }, [{ path: "/", Component: PublicOverview }], "/");
  expect(await screen.findByText("1 / 2 在线")).toBeInTheDocument();
  const web = within(screen.getByRole("article", { name: "web-1" }));
  expect(web.getByRole("img", { name: "在线" })).toBeInTheDocument();
  expect(web.getByRole("link", { name: "web-1" })).toHaveAttribute("href", "/nodes/3");
  expect(web.getByText("Debian 12 · amd64")).toBeInTheDocument();
  expect(web.getByRole("meter", { name: "42%" })).toHaveAttribute("aria-valuenow", "42");
  expect(web.getByRole("meter", { name: "512 MiB / 1.0 GiB" })).toHaveAttribute("aria-valuenow", "50");
  // 读数为 0 与无读数是两个事实：0 画成空条，不是破折号。
  expect(web.getByRole("meter", { name: "0 B / 10 GiB" })).toHaveAttribute("aria-valuenow", "0");
  expect(web.getByText("↓ 2.0 KiB/s ↑ 1.0 KiB/s")).toBeInTheDocument();
  expect(web.getByText("1d 1h")).toBeInTheDocument();
  expect(web.getByText("↓ 1.0 GiB ↑ 0 B")).toBeInTheDocument();
  expect(web.getByText("最近上报 刚刚")).toBeInTheDocument();
  const db = within(screen.getByRole("article", { name: "db-1" }));
  expect(db.getByRole("img", { name: "离线" })).toBeInTheDocument();
  expect(db.getByText("系统未知")).toBeInTheDocument();
  expect(db.getAllByLabelText("无读数")).toHaveLength(6);
  expect(db.getByText("从未上报")).toBeInTheDocument();
});

it("没有公开节点时说明", async () => {
  renderWithService(PublicService, { getSnapshot: async () => ({ now: 1n, nodes: [] }) }, [{ path: "/", Component: PublicOverview }], "/");
  expect(await screen.findByText("没有公开的节点。")).toBeInTheDocument();
});

it("按 POLL_MS 轮询快照", async () => {
  vi.useFakeTimers({ shouldAdvanceTime: true });
  const getSnapshot = vi.fn(async () => snapshot);
  renderWithService(PublicService, { getSnapshot }, [{ path: "/", Component: PublicOverview }], "/");
  await screen.findByText("1 / 2 在线");
  await act(async () => vi.advanceTimersByTimeAsync(POLL_MS + 100));
  await waitFor(() => expect(getSnapshot.mock.calls.length).toBeGreaterThanOrEqual(2));
});
```

`web/src/public/NodePage.test.tsx`：

```tsx
import { create } from "@bufbuild/protobuf";
import { screen } from "@testing-library/react";
import { expect, it, vi } from "vitest";
import { PublicService } from "../gen/probe/v1/public_pb";
import { QueryProbesResponseSchema } from "../gen/probe/v1/query_pb";
import { ProbeKind } from "../gen/probe/v1/types_pb";
import { renderWithService } from "../test/harness";
import { NodePage } from "./NodePage";

vi.mock("../components/Chart", () => ({
  Chart: ({ labels }: { labels: string[] }) => <div data-testid="chart">{labels.map((l) => <span key={l}>{l}</span>)}</div>,
}));

const snapshot = async () => ({ now: 1_000n, nodes: [{ id: 7n, name: "edge-1", online: true, facts: { os: "Alpine 3.21", arch: "arm64", cpuModel: "Neoverse", cpuCores: 2 } }] });

it("公开节点的历史图表走 PublicService，与面板同一组时间范围与探测图例", async () => {
  const queryMetrics = vi.fn(async () => ({ level: "1m", stepS: 60, ts: [], series: [] }));
  const queryProbes = vi.fn(async () => create(QueryProbesResponseSchema, { level: "1m", stepS: 60, series: [{ taskId: 3n, kind: ProbeKind.TCP, target: "example.com:443" }] }));
  renderWithService(PublicService, { getSnapshot: snapshot, queryMetrics, queryProbes }, [{ path: "/nodes/:id", Component: NodePage }], "/nodes/7");
  expect(await screen.findByRole("heading", { level: 1, name: "edge-1" })).toBeInTheDocument();
  expect(await screen.findAllByText("TCP example.com:443")).toHaveLength(2);
  expect(screen.getAllByTestId("chart")).toHaveLength(9);
  for (const r of ["1h", "6h", "24h", "7d", "30d"]) expect(screen.getByRole("button", { name: r })).toBeInTheDocument();
  expect(screen.getByText("Alpine 3.21")).toBeInTheDocument();
  expect((queryMetrics.mock.calls[0] as unknown[])[0]).toMatchObject({ nodeId: 7n, maxPoints: 1000 });
});

it("快照里没有的节点说明不存在或未公开，也不去查历史", async () => {
  const queryMetrics = vi.fn(async () => ({ level: "1m", stepS: 60, ts: [], series: [] }));
  renderWithService(PublicService, { getSnapshot: snapshot, queryMetrics }, [{ path: "/nodes/:id", Component: NodePage }], "/nodes/9");
  expect(await screen.findByRole("alert")).toHaveTextContent("节点 9 不存在或未公开");
  expect(queryMetrics).not.toHaveBeenCalled();
});
```

`web/src/public/main.test.tsx`：

```tsx
import { onlineManager } from "@tanstack/react-query";
import { act, within } from "@testing-library/react";
import * as ReactDOM from "react-dom/client";
import { afterAll, beforeAll, expect, test, vi } from "vitest";

// 入口用例验证传输与站点设置的应用；图表依赖的布局和 canvas 不由 jsdom 提供。
vi.mock("../components/Chart", () => ({ Chart: () => null }));

vi.mock("react-dom/client", async (importOriginal) => {
  const actual = await importOriginal<typeof import("react-dom/client")>();
  return { ...actual, createRoot: vi.fn(actual.createRoot) };
});

const fetches: { url: string; init?: RequestInit }[] = [];
let root: HTMLDivElement;
let reactRoot: ReactDOM.Root | undefined;

function json(body: unknown) {
  return new Response(JSON.stringify(body), { status: 200, headers: { "Content-Type": "application/json" } });
}

beforeAll(async () => {
  vi.stubGlobal("fetch", vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
    const url = String(input);
    fetches.push({ url, init });
    if (url.includes("/probe.v1.PublicService/GetSite")) return json({ title: "机房状态", theme: "dark", accentColor: "#123abc" });
    return json({ now: "1000", nodes: [{ id: "3", name: "web-1", online: true }] });
  }));
  root = document.createElement("div");
  root.id = "root";
  document.body.append(root);
  window.history.pushState({}, "", "/");
  const createRoot = vi.mocked(ReactDOM.createRoot);
  await act(async () => {
    await import("./main.tsx");
  });
  reactRoot = createRoot.mock.results[0]?.value;
});

afterAll(async () => {
  await act(async () => reactRoot?.unmount());
  root.remove();
  vi.unstubAllGlobals();
});

test("入口在 / 挂载公开总览并应用站点设置", async () => {
  expect(await within(root).findByRole("article", { name: "web-1" })).toBeInTheDocument();
  expect(await within(root).findByRole("link", { name: "机房状态" })).toBeInTheDocument();
  expect(document.documentElement.dataset.theme).toBe("dark");
  expect(document.documentElement.style.getPropertyValue("--accent")).toBe("#123abc");
});

// 公开请求用 GET（curl 同款的 Connect GET 形态），不带 cookie。
test("公开请求是不带凭据的 GET", () => {
  expect(fetches.length).toBeGreaterThanOrEqual(2);
  for (const { url, init } of fetches) {
    expect(url).toMatch(/^\/probe\.v1\.PublicService\/(GetSite|GetSnapshot)\?connect=v1&encoding=json&message=/);
    expect(init?.method).toBe("GET");
    expect(init?.credentials).toBe("omit");
  }
});

// 站点设置只在页面加载时取：断网重连时 react-query 重取已过期的查询，GetSite 无论过了多久都不在其列。
// 时钟拨到一天之后再重连，任何有限的 staleTime 都已过期。加载时的次数不写死：开发模式下 StrictMode 把挂载做两遍，
// 加载时取了两次（去掉 StrictMode 即为一次）。
test("网络恢复后不重取站点设置", async () => {
  const siteFetches = () => fetches.filter(({ url }) => url.includes("/GetSite?")).length;
  expect(await within(root).findByRole("link", { name: "机房状态" })).toBeInTheDocument();
  const loaded = siteFetches();
  expect(loaded).toBeGreaterThan(0);
  vi.setSystemTime(Date.now() + 24 * 60 * 60 * 1000);
  try {
    await act(async () => onlineManager.setOnline(false));
    await act(async () => onlineManager.setOnline(true));
    await act(async () => new Promise((resolve) => setTimeout(resolve, 50)));
  } finally {
    vi.useRealTimers();
  }
  expect(siteFetches()).toBe(loaded);
});
```

- [ ] **Step 2: 跑红**

```bash
cd /Users/xjetry/work/vibe/probe-public && pnpm --dir web exec vitest run src/public > /tmp/m5-t10-red.log 2>&1; echo $?
```

Expected：1。
- `importScan.test.ts`：`ENOENT` 读 `public/main.tsx`。
- 其余四个文件：`Failed to resolve import`（`./site`、`./Overview`、`./NodePage`、`./main.tsx`）。

- [ ] **Step 3: 实现公开页**

`web/src/public/index.html`：

```html
<!doctype html>
<html lang="zh-CN">
  <head>
    <meta charset="UTF-8" />
    <link rel="icon" href="data:," />
    <meta name="viewport" content="width=device-width, initial-scale=1.0" />
    <title>probe</title>
  </head>
  <body>
    <div id="root"></div>
    <script type="module" src="./main.tsx"></script>
  </body>
</html>
```

`web/src/public/main.tsx`：

```tsx
import { StrictMode } from "react";
import { createRoot } from "react-dom/client";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { TransportProvider } from "@connectrpc/connect-query";
import { RouterProvider } from "react-router";
import { transport } from "./transport";
import { router } from "./router";
import "../styles.css";
import "./public.css";

// 公开页没有会话，不装认证跳转。失败的查询重试两次后交给页面的错误横幅；限流（ResourceExhausted）也一样。
const queryClient = new QueryClient({ defaultOptions: { queries: { retry: 2, refetchOnWindowFocus: false } } });

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

`web/src/public/transport.ts`：

```ts
import { createConnectTransport } from "@connectrpc/connect-web";

// GET：PublicService 的方法都无副作用，GET 的 URL 可被浏览器与中间缓存按 hub 下发的 Cache-Control 复用。
// credentials: "omit"：公开服务不看凭据，同源的会话 cookie 不随公开请求发出，中间缓存也不必按登录与否区分。
// JSON 编码与面板一致，开发者工具里可读。
export const transport = createConnectTransport({
  baseUrl: "/",
  useBinaryFormat: false,
  useHttpGet: true,
  fetch: (input, init) => globalThis.fetch(input, { ...init, credentials: "omit" }),
});
```

`web/src/public/router.tsx`：

```tsx
import { createBrowserRouter } from "react-router";
import { NotFound, PublicLayout } from "./Layout";
import { NodePage } from "./NodePage";
import { PublicOverview } from "./Overview";

// 公开页挂在 /：/admin 与 RPC 路径由 hub 先行匹配，其余路径都回落到本页的 index.html，由这里路由。
export const router = createBrowserRouter([
  {
    path: "/",
    Component: PublicLayout,
    children: [
      { index: true, Component: PublicOverview },
      { path: "nodes/:id", Component: NodePage },
      { path: "*", Component: NotFound },
    ],
  },
]);
```

`web/src/public/site.ts`：

```ts
import type { PublicSite } from "../gen/probe/v1/public_pb";

// 站点设置里标题为空时用的标题；面板的外观页拿它作占位提示。
export const DEFAULT_TITLE = "服务器状态";

// applySite 把站点设置应用到文档。明暗写在 html 的 data-theme 上（auto 不写，跟随系统；styles.css 的 color-scheme
// 规则按它切换），主色覆盖 --accent，自定义 CSS 放进 head 末尾的 <style>，排在全部内置样式之后。
// CSS 经 textContent 写入，不经 HTML 解析；hub 另外拒绝含 "</" 的值，那是给把它内联进 HTML 的消费者的约束。
// 返回撤销函数：设置变化时先撤掉上一份再应用新的。
export function applySite(site: PublicSite): () => void {
  const root = document.documentElement;
  if (site.theme === "light" || site.theme === "dark") root.dataset.theme = site.theme;
  else delete root.dataset.theme;
  if (site.accentColor) root.style.setProperty("--accent", site.accentColor);
  else root.style.removeProperty("--accent");
  document.title = site.title || DEFAULT_TITLE;
  const style = document.createElement("style");
  style.dataset.siteCss = "";
  style.textContent = site.customCss;
  document.head.append(style);
  return () => {
    style.remove();
    delete root.dataset.theme;
    root.style.removeProperty("--accent");
  };
}
```

`web/src/public/Layout.tsx`：

```tsx
import { useQuery } from "@connectrpc/connect-query";
import { useEffect } from "react";
import { Link, Outlet } from "react-router";
import { errorBanner } from "../api/queryGate";
import { PublicService } from "../gen/probe/v1/public_pb";
import { applySite, DEFAULT_TITLE } from "./site";

// 外观只在页面加载时取，之后不再重取：已打开的页面刷新后才看到改动，刷新时浏览器还可能再用最多 5 分钟的缓存
// （hub 对 GetSite 下发 max-age=300）。"不再重取"由 staleTime: Infinity 承载：窗口聚焦重取已在 QueryClient 关掉，
// 断网重连与重新挂载只重取已过期的查询，永不过期的这条不在其列。
export function PublicLayout() {
  const site = useQuery(PublicService.method.getSite, {}, { staleTime: Infinity });
  useEffect(() => (site.data ? applySite(site.data) : undefined), [site.data]);
  return (
    <div className="layout">
      <header className="nav">
        <Link to="/" className="brand">
          {site.data?.logo && <img src={site.data.logo} alt="" className="logo" />}
          {site.data?.title || DEFAULT_TITLE}
        </Link>
      </header>
      <main className="main">
        {errorBanner(site.error)}
        <Outlet />
      </main>
    </div>
  );
}

export function NotFound() {
  return <p className="muted">页面不存在。<Link to="/">返回总览</Link></p>;
}
```

`web/src/public/Overview.tsx`：

```tsx
import { useQuery } from "@connectrpc/connect-query";
import { Link } from "react-router";
import { errorBanner, queryGate } from "../api/queryGate";
import { Bar, Missing, ratio } from "../components/Bar";
import { PublicService, type PublicNode } from "../gen/probe/v1/public_pb";
import { ago, bytes, duration, percent } from "../lib/format";
import { POLL_MS } from "../lib/poll";

export function PublicOverview() {
  const snap = useQuery(PublicService.method.getSnapshot, {}, { refetchInterval: POLL_MS });
  const gate = queryGate(snap);
  if (!gate.ready) return gate.loading ?? errorBanner(...gate.errors);
  const now = Number(gate.data.now);
  const online = gate.data.nodes.filter((n) => n.online).length;
  return (
    <section>
      <header className="row">
        <h1>节点</h1>
        <span className="muted">{online} / {gate.data.nodes.length} 在线</span>
      </header>
      {gate.banner}
      {gate.data.nodes.length === 0 && <p className="muted">没有公开的节点。</p>}
      <div className="cards">
        {gate.data.nodes.map((n) => <NodeCard key={String(n.id)} node={n} now={now} />)}
      </div>
    </section>
  );
}

// 卡片内容按 §10：名称、在线、系统与架构、CPU、内存、磁盘、网速、运行时长、本周期流量。
function NodeCard({ node, now }: { node: PublicNode; now: number }) {
  const m = node.metrics;
  const f = node.facts;
  return (
    <article className={`card node-card ${node.online ? "online" : "offline"}`} aria-label={node.name}>
      <h2>
        <span className={`dot ${node.online ? "ok" : "bad"}`} role="img" aria-label={node.online ? "在线" : "离线"} />
        <Link to={`/nodes/${node.id}`}>{node.name}</Link>
      </h2>
      <p className="muted">{f ? [f.os, f.arch].filter(Boolean).join(" · ") : "系统未知"}</p>
      <dl className="facts">
        <dt>CPU</dt>
        <dd>{m?.cpuPct !== undefined ? <Bar value={m.cpuPct} label={percent(m.cpuPct)} /> : <Missing />}</dd>
        <dt>内存</dt>
        <dd>{m?.memUsed !== undefined && m.memTotal ? <Bar value={ratio(m.memUsed, m.memTotal)} label={`${bytes(m.memUsed)} / ${bytes(m.memTotal)}`} /> : <Missing />}</dd>
        <dt>磁盘</dt>
        <dd>{m?.diskUsed !== undefined && m.diskTotal ? <Bar value={ratio(m.diskUsed, m.diskTotal)} label={`${bytes(m.diskUsed)} / ${bytes(m.diskTotal)}`} /> : <Missing />}</dd>
        <dt>网速</dt>
        <dd>{m?.netRxBps !== undefined && m.netTxBps !== undefined ? `↓ ${bytes(m.netRxBps)}/s ↑ ${bytes(m.netTxBps)}/s` : <Missing />}</dd>
        <dt>运行</dt>
        <dd>{m?.uptimeS !== undefined ? duration(m.uptimeS) : <Missing />}</dd>
        <dt>本周期</dt>
        <dd>{node.traffic ? `↓ ${bytes(node.traffic.periodRx)} ↑ ${bytes(node.traffic.periodTx)}` : <Missing />}</dd>
      </dl>
      <p className="muted">{node.lastSeenAt !== undefined ? `最近上报 ${ago(node.lastSeenAt, now)}` : "从未上报"}</p>
    </article>
  );
}
```

`web/src/public/NodePage.tsx`：

```tsx
import { useQuery } from "@connectrpc/connect-query";
import { Link, useParams } from "react-router";
import { errorBanner, queryGate } from "../api/queryGate";
import { HistoryCharts, RangePicker, useHistory, type HistoryMethods } from "../components/History";
import { PublicService } from "../gen/probe/v1/public_pb";
import { POLL_MS } from "../lib/poll";

const PUBLIC_HISTORY: HistoryMethods = { queryMetrics: PublicService.method.queryMetrics, queryProbes: PublicService.method.queryProbes };

export function NodePage() {
  const { id } = useParams();
  const validId = /^\d+$/.test(id ?? "");
  const nodeId = validId ? BigInt(id!) : 0n;
  const snap = useQuery(PublicService.method.getSnapshot, {}, { enabled: validId, refetchInterval: POLL_MS });
  const node = snap.data?.nodes.find((n) => n.id === nodeId);
  // 只在快照里有这个节点时查历史：未公开或不存在的节点，历史查询只会得到 NotFound。
  const history = useHistory(PUBLIC_HISTORY, nodeId, node !== undefined);
  const missing = <p role="alert" className="error">节点 {id} 不存在或未公开。<Link to="/">返回总览</Link></p>;
  if (!validId) return missing;
  const gate = queryGate(snap);
  if (!gate.ready) return gate.loading ?? errorBanner(...gate.errors);
  if (!node) return missing;
  return (
    <section>
      {errorBanner(snap.error, history.metrics.error, history.probes.error)}
      <header className="row detail-header">
        <h1>{node.name}</h1>
        <RangePicker history={history} />
      </header>
      <HistoryCharts history={history} noProbes={<p className="muted">窗口内没有探测结果。</p>} />
      {node.facts && (
        <dl className="card facts">
          <dt>系统</dt><dd>{node.facts.os}</dd>
          <dt>架构</dt><dd>{node.facts.arch}</dd>
          <dt>CPU</dt><dd>{node.facts.cpuModel} × {node.facts.cpuCores}</dd>
          <dt>虚拟化</dt><dd>{node.facts.virtualization || "无 / 未知"}</dd>
        </dl>
      )}
    </section>
  );
}
```

`web/src/public/public.css`：

```css
/* 公开页在共用调色板（../styles.css）之上的布局：站点标题与节点卡片。站点设置的自定义 CSS 排在这之后。 */
.nav .brand { display: inline-flex; align-items: center; gap: 0.5rem; color: var(--fg); font-weight: 600; }
.nav .brand .logo { height: 24px; width: auto; }
.cards { display: grid; grid-template-columns: repeat(auto-fill, minmax(min(280px, 100%), 1fr)); gap: 1rem; }
.node-card h2 { display: flex; align-items: center; margin: 0 0 0.25rem; font-size: 1rem; }
.node-card.offline { opacity: 0.7; }
.node-card .facts { display: grid; grid-template-columns: auto 1fr; gap: 0.35rem 0.75rem; align-items: center; margin: 0.5rem 0; }
.node-card .facts dd { margin: 0; }
```

- [ ] **Step 4: 构建配置**

`web/vite.config.ts` 整体替换为：

```ts
import react from "@vitejs/plugin-react";
import { writeFileSync } from "node:fs";
import { fileURLToPath } from "node:url";
import { defineConfig, type Plugin } from "vitest/config";

import { asyncUtilTimeout } from "./src/test/async-timeout.ts";

// 两个入口各自打包，产物直接落在 internal/hub/web 的 embed 目录，不再拷贝一次：
// 默认模式是管理面板（web/index.html，base /admin/，落到 dist）；--mode public 是公开页
// （根为 src/public，base /，落到 dist-public）。base 必须与 hub 的挂载路径一致，产物里的资源引用才能命中。
// tsconfig.app.json 关掉了 erasableSyntaxOnly：protoc-gen-es 为 proto enum 生成 TS enum，那条限制会拒绝生成代码。
const embedDir = (name: string) => fileURLToPath(new URL(`../internal/hub/web/${name}`, import.meta.url));

// Vite 清空输出目录，也由构建自身恢复占位文件，保证任意构建入口都维持 embed 目录含文件（go:embed 的模式要能匹配）。
function keepEmbedDirectory(outDir: string): Plugin {
  return { name: "keep-embed-directory", apply: "build", closeBundle() { writeFileSync(`${outDir}/.gitkeep`, ""); } };
}

export default defineConfig(({ mode }) => {
  const isPublic = mode === "public";
  const outDir = embedDir(isPublic ? "dist-public" : "dist");
  return {
    root: isPublic ? fileURLToPath(new URL("./src/public", import.meta.url)) : undefined,
    base: isPublic ? "/" : "/admin/",
    plugins: [react(), keepEmbedDirectory(outDir)],
    build: { outDir, emptyOutDir: true },
    test: {
      environment: "jsdom",
      setupFiles: ["./src/test/setup.ts"],
      globals: false,
      // 必须长于异步查找上界，否则运行器会先杀掉用例，上界到不了。
      testTimeout: asyncUtilTimeout * 2,
    },
  };
});
```

`web/package.json` 的 `build` 脚本改为：

```json
    "build": "tsc -b && vite build && vite build --mode public",
```

`Makefile` 的 `web` 目标上方注释改为：

```make
# 两个入口的产物落在 internal/hub/web/dist（面板）与 dist-public（公开页）供 go:embed；不入库，缺产物时 hub 也能编译并给出说明页。
```

- [ ] **Step 5: 跑绿**

```bash
cd /Users/xjetry/work/vibe/probe-public && pnpm --dir web exec tsc -b > /tmp/m5-t10-tsc.log 2>&1; echo $?
cd /Users/xjetry/work/vibe/probe-public && pnpm --dir web exec vitest run > /tmp/m5-t10-green.log 2>&1; echo $?
```

Expected：
- 两条都是 0。
- `m5-t10-green.log` 里有一行 `Could not parse CSS stylesheet`：jsdom 解析 `site.test.ts` 故意写入的 `</style><img …>`，不是失败。

- [ ] **Step 6: 构建两个入口并核对产物**

```bash
cd /Users/xjetry/work/vibe/probe-public && make web > /tmp/m5-t10-build.log 2>&1; echo $?
cd /Users/xjetry/work/vibe/probe-public && grep -rl 'ChRwcm9iZS92MS9hZG1pbi5wcm90' internal/hub/web/dist/assets > /tmp/m5-t10-admin-in-panel.log 2>&1; echo $?
cd /Users/xjetry/work/vibe/probe-public && grep -rl 'ChRwcm9iZS92MS9hZG1pbi5wcm90' internal/hub/web/dist-public/assets > /tmp/m5-t10-admin-in-public.log 2>&1; echo $?
cd /Users/xjetry/work/vibe/probe-public && grep -rl 'ChVwcm9iZS92MS9wdWJsaWMucHJv' internal/hub/web/dist-public/assets > /tmp/m5-t10-public-in-public.log 2>&1; echo $?
cd /Users/xjetry/work/vibe/probe-public && grep -c 'src="/assets/' internal/hub/web/dist-public/index.html > /tmp/m5-t10-public-base.log 2>&1; echo $?
cd /Users/xjetry/work/vibe/probe-public && git status --porcelain -- internal/hub/web > /tmp/m5-t10-status.log 2>&1; echo $?
```

Expected（前缀的来历见实验 4）：
- 第 1 条 0。`m5-t10-build.log` 有两段 `vite v8… building`，分别写到 `../internal/hub/web/dist/` 与 `../internal/hub/web/dist-public/`。两段都有 `Some chunks are larger than 500 kB` 的提示，面板在本计划之前就有，不处理。
- 第 2 条 0，日志一行（面板包里有 `admin.proto` 的描述符）。这是下一条的阳性对照：同一个前缀、同一种 grep 在面板包里找得到。
- 第 3 条 1，日志为空：公开包里没有 `admin.proto` 的描述符。退出码 2 或日志里有报错，说明检查本身没跑成。
- 第 4 条 0，日志一行（公开包里有 `public.proto` 的描述符）。
- 第 5 条 0，日志为 `1`：公开页的资源引用在 `/assets/` 下。
- 第 6 条 0，日志为空：两份产物都被 `.gitignore` 忽略，`.gitkeep` 由构建恢复、内容不变。

- [ ] **Step 7: 浏览器验收**

jsdom 不解析 `var()` 与 `light-dark()`（实验 6），CSP 也只在浏览器里生效。本步在真实浏览器里核对明暗、自定义 CSS、logo、请求形态，用 Playwright MCP。

准备 hub（`make web` 已在 Step 6 跑过，嵌入的是刚构建的产物）：

```bash
cd /Users/xjetry/work/vibe/probe-public && mkdir -p /tmp/m5-t10-accept && go build -o /tmp/m5-t10-accept/probe-hub ./cmd/hub > /tmp/m5-t10-accept/build.log 2>&1; echo $?
```

后台启动（Bash 工具的 `run_in_background`）：

```bash
cd /Users/xjetry/work/vibe/probe-public && /tmp/m5-t10-accept/probe-hub serve --db /tmp/m5-t10-accept/hub.db --listen 127.0.0.1:18090 > /tmp/m5-t10-accept/hub.log 2>&1
```

`/tmp/m5-t10-accept/setup.sh`：

```sh
#!/bin/sh
# 浏览器验收用的 hub 状态：一个公开节点（没有 agent 上报），外观为深色、橙色主色、SVG logo 与一条自定义 CSS。
set -eu
dir=/tmp/m5-t10-accept
base=http://127.0.0.1:18090
printf '%s\n' 'accept admin password 2026' | "$dir/probe-hub" passwd --db "$dir/hub.db"
curl -fsS -o /dev/null -c "$dir/jar" -H 'Content-Type: application/json' --data '{"password":"accept admin password 2026"}' "$base/probe.v1.AdminService/Login"
curl -fsS -o "$dir/node.json" -b "$dir/jar" -H 'Content-Type: application/json' --data '{"name":"web-1"}' "$base/probe.v1.AdminService/CreateNode"
id=$(jq -r '.node.id' "$dir/node.json")
curl -fsS -o /dev/null -b "$dir/jar" -H 'Content-Type: application/json' \
  --data "$(jq -nc --arg id "$id" '{id: $id, name: "web-1", public: true, note: "", trafficResetDay: 1, offlineGraceS: 0}')" "$base/probe.v1.AdminService/UpdateNode"
svg='<svg xmlns="http://www.w3.org/2000/svg" width="16" height="16"><rect width="16" height="16" fill="red"/></svg>'
logo="data:image/svg+xml;base64,$(printf '%s' "$svg" | base64 | tr -d '\n')"
curl -fsS -o "$dir/settings.json" -b "$dir/jar" -H 'Content-Type: application/json' \
  --data "$(jq -nc --arg logo "$logo" '{settings: {title: "机房状态", theme: "dark", accentColor: "#FF5500", logo: $logo, customCss: ".node-card { border-width: 3px; }"}}')" \
  "$base/probe.v1.AdminService/UpdateSettings"
```

`/tmp/m5-t10-accept/theme.sh`（只改明暗，其余四项回到默认）：

```sh
#!/bin/sh
set -eu
dir=/tmp/m5-t10-accept
curl -fsS -o "$dir/settings.json" -b "$dir/jar" -H 'Content-Type: application/json' \
  --data "$(jq -nc --arg theme "$1" '{settings: {theme: $theme}}')" "http://127.0.0.1:18090/probe.v1.AdminService/UpdateSettings"
```

```bash
cd /Users/xjetry/work/vibe/probe-public && sh /tmp/m5-t10-accept/setup.sh > /tmp/m5-t10-accept/setup.log 2>&1; echo $?
```

Expected：0；`/tmp/m5-t10-accept/settings.json` 的 `accentColor` 为 `#ff5500`。

浏览器里逐项核对（Playwright MCP）：

1. `browser_emulate_media` 设 `colorScheme: "light"`，`browser_navigate` 到 `http://127.0.0.1:18090/`，`browser_evaluate`（先等 1 秒，让 `GetSite` 与 `GetSnapshot` 到达）：

   ```js
   () => new Promise((r) => setTimeout(() => { const root = document.documentElement; const last = document.head.lastElementChild; const card = document.querySelector("article.node-card"); const logo = document.querySelector("img.logo"); r({ theme: root.dataset.theme, bg: getComputedStyle(root).backgroundColor, accent: getComputedStyle(root).getPropertyValue("--accent"), lastTag: last.tagName, lastText: last.textContent, cardBorder: card && getComputedStyle(card).borderTopWidth, cardLabel: card && card.getAttribute("aria-label"), logoWidth: logo && logo.naturalWidth, title: document.title, linkColor: getComputedStyle(document.querySelector(".node-card a")).color }); }, 1000))
   ```

   期望：`theme: "dark"`、`bg: "rgb(15, 17, 21)"`（系统浅色、站点深色，站点优先）、`accent: "#ff5500"`、`lastTag: "STYLE"`、`lastText: ".node-card { border-width: 3px; }"`、`cardBorder: "3px"`（自定义 CSS 排在内置样式之后并生效）、`cardLabel: "web-1"`、`logoWidth: 16`（CSP 的 `img-src data:` 放行 logo）、`title: "机房状态"`、`linkColor: "rgb(255, 85, 0)"`。
2. `browser_network_requests`（`static: false`）：只有 `GET …/probe.v1.PublicService/GetSite?connect=v1&encoding=json&message=%7B%7D` 与同形的 `GetSnapshot`，没有 POST，没有 `AdminService`。`GetSnapshot` 的形态正是快照缓存的规范形态。
3. `browser_console_messages`（`level: "warning"`）：0 条，没有 CSP 拦截。
4. `browser_navigate` 到 `/nodes/1`，等 1 秒后 `browser_evaluate`：`h1` 为 `web-1`；`.uplot` 7 个；时间窗口按钮 `1h 6h 24h 7d 30d`；页面有"窗口内没有探测结果。"；没有 `[role=alert]`。再在第一个 `.uplot` 的父元素里放一个 `color: var(--muted)` 的 span 读计算色，得 `rgb(156, 163, 175)`：图表所在元素解析出的是深色的 `--muted`。
5. `browser_navigate` 到 `/nodes/999`：`[role=alert]` 为"节点 999 不存在或未公开。返回总览"；网络请求里没有 `QueryMetrics`。
6. `cd /Users/xjetry/work/vibe/probe-public && sh /tmp/m5-t10-accept/theme.sh light > /tmp/m5-t10-accept/theme.log 2>&1; echo $?`（期望 0），`browser_emulate_media` 设 `colorScheme: "dark"`。`GetSite` 带 `max-age=300`，浏览器会用缓存里的旧设置：先 `browser_evaluate` 执行 `async () => { await fetch("/probe.v1.PublicService/GetSite?connect=v1&encoding=json&message=%7B%7D", { cache: "reload", credentials: "omit" }); location.reload(); }`，再读：`theme: "light"`、`bg: "rgb(246, 247, 249)"`（系统深色、站点浅色）、`title: "服务器状态"`、没有 `img.logo`。
7. 同样的命令把参数换成 `auto`，刷新缓存后再读：`data-theme` 不存在，`bg: "rgb(15, 17, 21)"`（跟随系统深色）。

收尾：`browser_close`；停掉后台 hub。Playwright MCP 把页面快照写在它自己的工作目录下的 `.playwright-mcp/`，那可能是主仓库而不是本工作树；两处都删，都不提交。

```bash
cd /Users/xjetry/work/vibe/probe-public && rm -rf .playwright-mcp /Users/xjetry/work/vibe/probe/.playwright-mcp && git status --porcelain > /tmp/m5-t10-accept/status.log 2>&1; echo $?
```

Expected：0；`status.log` 只列本任务要提交的文件。

- [ ] **Step 8: 提交**

```bash
cd /Users/xjetry/work/vibe/probe-public && git add web/src/public web/vite.config.ts web/package.json Makefile && git commit -m "web: 公开页作为第二个 Vite 入口，构建到 dist-public" -m "公开页以 web/src/public 为根、base 为 /，与面板各自打包；两个入口共用历史图表、读数条与调色板。请求用 GET 且不带凭据：hub 按方法下发的 Cache-Control 因此生效，公开请求与登录状态无关。站点设置在 html 上写 data-theme 与 --accent，自定义 CSS 经 textContent 放进 head 末尾的 <style>，排在全部内置样式之后。站点设置只在页面加载时取、之后不再重取（staleTime: Infinity，断网重连也不重取），已打开的页面刷新后才看到外观改动。入口的 import 链不触达 admin_pb.ts，由扫描测试钉住，面板入口作阳性对照。" > /tmp/m5-t10-commit.log 2>&1; echo $?
```

Expected：0。

- [ ] **Step 9: 缺陷注入**

逐项进行：改动 → `git diff --stat` 非空 → 跑命令 → 核对红的原因 → `git checkout -- <文件>`。命令除注明外都是 Step 2 那条。

| | 改动 | 期望 |
|---|---|---|
| a | `site.ts` 的第一个 import 之后加一行 `import "../gen/probe/v1/admin_pb";` | 1，`importScan.test.ts` 红，消息为 `public/main.tsx → public/router.tsx → public/Layout.tsx → public/site.ts → gen/probe/v1/admin_pb.ts` |
| b | `importScan.test.ts` 的 `SPECIFIER` 里 `\bfrom\s*` 改成 `\bfromm\s*`（扫描失效） | 1，冒烟红在 `gen/probe/v1/public_pb.ts: expected false to be true`，面板入口的阳性对照也红 |
| c | `applySite` 里建 `<style>` 的四行（`createElement` 到 `append`）换成表后的"c 的替换代码"：把 CSS 拼进 HTML 插入 head | 1，"CSS 按文本写入，不产生元素"红在 `expected <img src="x" …(1)></img> to have a length of +0 but got 1` |
| d | `applySite` 的 `document.head.append(style)` 改成 `prepend` | 1，"自定义 CSS 排在已有样式之后"红在 `expected 'TITLE' to be 'STYLE'` |
| e | `transport.ts` 的 `fetch` 改成 `globalThis.fetch(input, init)` | 1，`main.test.tsx` 红在 `expected undefined to be 'omit'` |
| f | `transport.ts` 的 `useHttpGet: true` 改成 `false` | 1，`main.test.tsx` 红在 URL 不匹配 `?connect=v1&encoding=json&message=` |
| g | `Layout.tsx` 的 effect 改成 `useEffect(() => undefined, [site.data]);`（不应用站点设置） | 1，`main.test.tsx` 红在 `expected undefined to be 'dark'` |
| h | `NodePage.tsx` 的 `useHistory(PUBLIC_HISTORY, nodeId, node !== undefined)` 改成 `validId` | 1，"快照里没有的节点…也不去查历史"红在 `expected "vi.fn()" to not be called at all` |
| i | `public/Overview.tsx` 磁盘一格的条件 `m?.diskUsed !== undefined && m.diskTotal` 改成 `m?.diskUsed && m.diskTotal` | 1，红在找不到 `meter` `0 B / 10 GiB`：0 被当成无读数 |
| j | `vite.config.ts` 的 `base: isPublic ? "/" : "/admin/"` 改成 `base: "/admin/"`；命令为 Step 6 的第 1 条与第 5 条 | 第 5 条退出 1、日志为 `0`：公开页的资源引用落到 `/admin/assets/`，hub 在 `/` 下找不到 |
| k | `public/Overview.tsx` 的 `online` 改成 `gate.data.nodes.length`（不看在线） | 1，找不到 `1 / 2 在线` |
| l | 删去"没有公开的节点。"那一行 | 1，"没有公开节点时说明"找不到该文本 |
| m | `public/Overview.tsx` 的快照查询去掉 `refetchInterval: POLL_MS` | 1，"按 POLL_MS 轮询快照"红在 `expected 1 to be greater than or equal to 2` |
| n | `NodePage.tsx` 的 `useHistory(PUBLIC_HISTORY, nodeId, …)` 改成传 `0n` | 1，`expected { …(5) } to match object { nodeId: 7n, maxPoints: 1000 }` |
| o | `public/Overview.tsx` 的 `"系统未知"` 改成 `""`；另起一项把 `"从未上报"` 改成 `""` | 1，各自红在找不到 `系统未知` / `从未上报` |
| p | `applySite` 的撤销函数不再 `removeProperty("--accent")`；另起一项把 `document.title = site.title \|\| DEFAULT_TITLE` 改成 `site.title` | 1，前者红在 `expected '#123abc' to be ''`，后者红在 `expected '' to be '服务器状态'` |
| q | `Layout.tsx` 的 `useQuery(PublicService.method.getSite, {}, { staleTime: Infinity })` 去掉第三个参数；另起一项改成 `{ staleTime: 5 * 60 * 1000 }` | 1，两项都红在"网络恢复后不重取站点设置"的 `expected 3 to be 2`：时钟拨过一天后重连，过期的 `GetSite` 被重取 |

c 的替换代码：

```ts
  document.head.insertAdjacentHTML("beforeend", `<style data-site-css>${site.customCss}</style>`);
  const style = document.head.querySelector("style[data-site-css]")!;
```

j 之后再跑一次 Step 6 的第 1 条，让 `dist-public` 回到正确的产物。

---

### Task 11: 面板的外观页

**Files:**
- Create: `web/src/lib/appearance.ts`、`web/src/lib/appearanceLimits.test.ts`
- Create: `web/src/pages/Appearance.tsx`、`web/src/pages/Appearance.test.tsx`
- Modify: `web/src/App.tsx`（路由 `appearance`）、`web/src/components/Layout.tsx`（导航"外观"）、`web/src/components/Layout.test.tsx`
- Modify: `web/src/styles.css`（`.logo-preview`）

**Interfaces:**
- Consumes：
  - Task 3：`AdminService.GetSettings` / `UpdateSettings`、`Settings`（`web/src/gen/probe/v1/admin_pb.ts`）；`internal/hub/api/settings.go` 的 `maxTitleRunes`、`maxTitleBytes`、`maxLogoBytes`、`maxCSSBytes`、`themes`、`logoTypes`（Task 3 在那里的注释已指向本任务的两个文件）。
  - Task 10：`DEFAULT_TITLE`（`web/src/public/site.ts`）。
- Produces（`web/src/lib/appearance.ts`）：`MAX_TITLE_CHARS`、`MAX_TITLE_BYTES`、`MAX_LOGO_BYTES`、`MAX_CSS_BYTES`、`THEMES`、`LOGO_TYPES`、`type Theme`、`sizeProblems(title: string, logo: string, customCss: string): string[]`。

外观约束的裁决只在 hub：页面显示 hub 的错误原文，不另做一份校验。页面在提交前只查标题、logo 与 CSS 的字节数，原因是超过管理请求解码预算的请求在校验之前就被拒绝（`resource_exhausted`，见实验与读码结论 10、设计决定 11），那时错误说不出是哪个字段。这三个字节上限、标题字符上限、明暗取值与 logo 类型是页面与 hub 的两份同值常量，由 `appearanceLimits.test.ts` 直接读 `settings.go` 对照。

- [ ] **Step 1: 写失败测试**

`web/src/lib/appearanceLimits.test.ts`：

```ts
// @ts-nocheck -- 这个测试读仓库文件，app tsconfig 只带 vite/client，没有 node 类型。
// @vitest-environment node
import { readFileSync } from "node:fs";
import { dirname, resolve } from "node:path";
import { fileURLToPath } from "node:url";
import { expect, it } from "vitest";
import { LOGO_TYPES, MAX_CSS_BYTES, MAX_LOGO_BYTES, MAX_TITLE_BYTES, MAX_TITLE_CHARS, THEMES, sizeProblems } from "./appearance";

const settingsGo = readFileSync(resolve(dirname(fileURLToPath(import.meta.url)), "../../../internal/hub/api/settings.go"), "utf8");

// 常量写成 N 或 N << S；找不到就让用例红，而不是比对 undefined。
function goConst(name) {
  const m = settingsGo.match(new RegExp(`\\b${name}\\s*=\\s*(\\d+)(?:\\s*<<\\s*(\\d+))?`));
  if (!m) throw new Error(`${name} not found in settings.go`);
  return Number(m[1]) * 2 ** Number(m[2] ?? 0);
}

function goStrings(name) {
  const m = settingsGo.match(new RegExp(`\\b${name}\\s*=\\s*\\[\\]string\\{([^}]*)\\}`));
  if (!m) throw new Error(`${name} not found in settings.go`);
  return [...m[1].matchAll(/"([^"]*)"/g)].map((s) => s[1]);
}

it("上限与取值和 hub 的 settings.go 一致", () => {
  expect(MAX_TITLE_CHARS).toBe(goConst("maxTitleRunes"));
  expect(MAX_TITLE_BYTES).toBe(goConst("maxTitleBytes"));
  expect(MAX_LOGO_BYTES).toBe(goConst("maxLogoBytes"));
  expect(MAX_CSS_BYTES).toBe(goConst("maxCSSBytes"));
  expect([...THEMES]).toEqual(goStrings("themes"));
  expect([...LOGO_TYPES]).toEqual(goStrings("logoTypes"));
});

it("大小按 UTF-8 字节计，恰在上限时不报", () => {
  expect(sizeProblems("a".repeat(MAX_TITLE_BYTES), "a".repeat(MAX_LOGO_BYTES), "a".repeat(MAX_CSS_BYTES))).toEqual([]);
  expect(sizeProblems("", "a".repeat(MAX_LOGO_BYTES + 1), "")).toHaveLength(1);
  // 342 个"中"是 1026 字节，UTF-16 长度只有 342；21846 个是 65538 字节。
  expect(sizeProblems("中".repeat(342), "", "")).toEqual([`标题 1026 字节，上限 ${MAX_TITLE_BYTES} 字节（去掉控制字符与首尾空白之前计）。`]);
  const css = "中".repeat(21846);
  expect(sizeProblems("", "", css)).toEqual([`自定义 CSS 65538 字节，上限 ${MAX_CSS_BYTES} 字节（64 KiB）。`]);
});
```

`web/src/pages/Appearance.test.tsx`：

```tsx
import { Code, ConnectError } from "@connectrpc/connect";
import { fireEvent, screen, waitFor, within } from "@testing-library/react";
import { expect, it } from "vitest";
import type { UpdateSettingsRequest } from "../gen/probe/v1/admin_pb";
import { MAX_LOGO_BYTES } from "../lib/appearance";
import { renderWithAdmin, type AdminImpl } from "../test/harness";
import { Appearance } from "./Appearance";

const current = { title: "机房", theme: "dark", accentColor: "#123abc", logo: "", customCss: "body { margin: 0 }" };
const routes = [{ path: "/appearance", Component: Appearance }];
const render = (impl: AdminImpl) => renderWithAdmin({ getSettings: async () => ({ settings: current }), ...impl }, routes, "/appearance");

async function form() {
  return within(await screen.findByRole("form", { name: "公开页外观" }));
}

it("表单显示当前设置，标题留空时提示内置标题", async () => {
  render({ getSettings: async () => ({ settings: { ...current, title: "" } }) });
  const f = await form();
  expect(f.getByLabelText("标题")).toHaveValue("");
  expect(f.getByLabelText("标题")).toHaveAttribute("placeholder", "服务器状态");
  expect(f.getByLabelText("明暗")).toHaveValue("dark");
  expect(f.getByLabelText("主色")).toHaveValue("#123abc");
  expect(f.getByLabelText("自定义 CSS")).toHaveValue("body { margin: 0 }");
});

it("保存提交全部五项，表单改显 hub 实际保存的值", async () => {
  const sent: UpdateSettingsRequest[] = [];
  render({ updateSettings: async (req) => { sent.push(req); return { settings: { ...req.settings!, title: "新标题", accentColor: "#abcdef" } }; } });
  const f = await form();
  fireEvent.change(f.getByLabelText("标题"), { target: { value: " 新标题 " } });
  fireEvent.change(f.getByLabelText("明暗"), { target: { value: "auto" } });
  fireEvent.change(f.getByLabelText("主色"), { target: { value: "#ABCDEF" } });
  fireEvent.click(f.getByRole("button", { name: "保存" }));
  expect(await f.findByRole("status")).toHaveTextContent("已保存");
  expect(sent).toHaveLength(1);
  expect(sent[0].settings).toMatchObject({ title: " 新标题 ", theme: "auto", accentColor: "#ABCDEF", logo: "", customCss: "body { margin: 0 }" });
  expect(f.getByLabelText("标题")).toHaveValue("新标题");
  expect(f.getByLabelText("主色")).toHaveValue("#abcdef");
});

it("hub 拒绝时显示错误原文并保留草稿", async () => {
  render({ updateSettings: async () => { throw new ConnectError('settings.custom_css must not contain "</" (it could end the page\'s <style> element); found at byte 0', Code.InvalidArgument); } });
  const f = await form();
  fireEvent.change(f.getByLabelText("自定义 CSS"), { target: { value: "</style>" } });
  fireEvent.click(f.getByRole("button", { name: "保存" }));
  expect(await f.findByRole("alert")).toHaveTextContent('settings.custom_css must not contain "</"');
  expect(f.getByLabelText("自定义 CSS")).toHaveValue("</style>");
});

it("选中的 logo 以 data: URL 提交，可移除", async () => {
  const sent: UpdateSettingsRequest[] = [];
  render({ updateSettings: async (req) => { sent.push(req); return { settings: req.settings }; } });
  const f = await form();
  expect(f.getByLabelText("logo")).toHaveAttribute("accept", "image/png,image/jpeg,image/webp,image/svg+xml");
  fireEvent.change(f.getByLabelText("logo"), { target: { files: [new File([new Uint8Array([137, 80, 78, 71])], "logo.png", { type: "image/png" })] } });
  expect(await f.findByRole("img", { name: "logo 预览" })).toHaveAttribute("src", "data:image/png;base64,iVBORw==");
  fireEvent.click(f.getByRole("button", { name: "保存" }));
  await waitFor(() => expect(sent.map((r) => r.settings?.logo)).toEqual(["data:image/png;base64,iVBORw=="]));
  fireEvent.click(f.getByRole("button", { name: "移除 logo" }));
  expect(f.queryByRole("img", { name: "logo 预览" })).toBeNull();
});

it("超出大小上限的 logo 在提交前报出，不发请求", async () => {
  const sent: UpdateSettingsRequest[] = [];
  render({ updateSettings: async (req) => { sent.push(req); return { settings: req.settings }; } });
  const f = await form();
  // 98304 字节编码成 131072 个 base64 字符，恰等于上限；加上 data:image/png;base64, 前缀就超出。
  const size = (MAX_LOGO_BYTES / 4) * 3;
  fireEvent.change(f.getByLabelText("logo"), { target: { files: [new File([new Uint8Array(size)], "big.png", { type: "image/png" })] } });
  expect(await f.findByRole("alert")).toHaveTextContent(`上限 ${MAX_LOGO_BYTES} 字节`);
  expect(f.getByRole("button", { name: "保存" })).toBeDisabled();
  // 按钮禁用只是提示，不发请求由 submit 里的检查承载：直接触发表单的 submit 事件验证它。
  // 随后换成合规的 logo 再保存；超限的那次若也发出了，会排在前面。
  fireEvent.submit(f.getByRole("button", { name: "保存" }).closest("form")!);
  fireEvent.change(f.getByLabelText("logo"), { target: { files: [new File([new Uint8Array([137, 80, 78, 71])], "logo.png", { type: "image/png" })] } });
  await f.findByRole("img", { name: "logo 预览" });
  await waitFor(() => expect(f.getByRole("button", { name: "保存" })).toBeEnabled());
  fireEvent.click(f.getByRole("button", { name: "保存" }));
  await waitFor(() => expect(sent.length).toBeGreaterThan(0));
  expect(sent[0].settings?.logo).toBe("data:image/png;base64,iVBORw==");
});
```

`web/src/components/Layout.test.tsx` 在"告警规则导航进入应用的规则页路由"之前加：

```tsx
it("外观导航进入应用的外观页路由", async () => {
  renderWithAdmin({ listNodes: async () => ({ nodes: [] }), getSettings: async () => ({ settings: { theme: "auto" } }) }, appRouter.routes, "/nodes");
  const link = screen.getByRole("link", { name: "外观" });
  expect(link).toHaveAttribute("href", "/appearance");
  fireEvent.click(link);
  expect(await screen.findByRole("heading", { name: "外观" })).toBeInTheDocument();
});
```

- [ ] **Step 2: 跑红**

```bash
cd /Users/xjetry/work/vibe/probe-public && pnpm --dir web exec vitest run src/lib/appearanceLimits.test.ts src/pages/Appearance.test.tsx src/components/Layout.test.tsx > /tmp/m5-t11-red.log 2>&1; echo $?
```

Expected：1。
- 前两个文件：`Failed to resolve import "./appearance"` / `"./Appearance"`。
- `Layout.test.tsx`："外观导航进入应用的外观页路由"红在 `Unable to find an accessible element with the role "link" and name "外观"`。

- [ ] **Step 3: 实现**

`web/src/lib/appearance.ts`：

```ts
// 外观的取值与上限，与 hub 的 internal/hub/api/settings.go 同值，由 appearanceLimits.test.ts 逐项对照。
// 约束的裁决在 hub：页面显示 hub 的错误原文，不另做一份校验。页面在提交前只查三个字段的字节数——
// 超过 hub 管理请求解码预算（internal/hub/api/service.go 的 maxBody）的请求在校验之前就被拒绝，
// 那时的错误说不出是哪个字段超了。
export const MAX_TITLE_CHARS = 64;
export const MAX_TITLE_BYTES = 1024;
export const MAX_LOGO_BYTES = 128 * 1024;
export const MAX_CSS_BYTES = 64 * 1024;
export const THEMES = ["auto", "light", "dark"] as const;
export const LOGO_TYPES = ["image/png", "image/jpeg", "image/webp", "image/svg+xml"] as const;

export type Theme = (typeof THEMES)[number];

// hub 按 UTF-8 字节计大小；JS 字符串的 length 是 UTF-16 码元数，非 ASCII 时两者不同。
const utf8Bytes = (s: string) => new TextEncoder().encode(s).length;

export function sizeProblems(title: string, logo: string, customCss: string): string[] {
  const out: string[] = [];
  const titleBytes = utf8Bytes(title);
  if (titleBytes > MAX_TITLE_BYTES) out.push(`标题 ${titleBytes} 字节，上限 ${MAX_TITLE_BYTES} 字节（去掉控制字符与首尾空白之前计）。`);
  const logoBytes = utf8Bytes(logo);
  if (logoBytes > MAX_LOGO_BYTES) out.push(`logo 编码为 data: URL 后 ${logoBytes} 字节，上限 ${MAX_LOGO_BYTES} 字节（128 KiB）。换一张更小的图片。`);
  const cssBytes = utf8Bytes(customCss);
  if (cssBytes > MAX_CSS_BYTES) out.push(`自定义 CSS ${cssBytes} 字节，上限 ${MAX_CSS_BYTES} 字节（64 KiB）。`);
  return out;
}
```

`web/src/pages/Appearance.tsx`：

```tsx
import { createConnectQueryKey, useMutation, useQuery } from "@connectrpc/connect-query";
import { useQueryClient } from "@tanstack/react-query";
import { type ChangeEvent, type FormEvent, useState } from "react";
import { errorText } from "../api/auth";
import { errorBanner, queryGate } from "../api/queryGate";
import { AdminService, type Settings } from "../gen/probe/v1/admin_pb";
import { LOGO_TYPES, MAX_TITLE_CHARS, THEMES, sizeProblems, type Theme } from "../lib/appearance";
import { DEFAULT_TITLE } from "../public/site";

const THEME_LABELS: Record<Theme, string> = { auto: "跟随访客的系统设置", light: "浅色", dark: "深色" };
// 主色留空时公开页用内置配色；取色器必须有值，空时显示内置浅色主题的主色。
const BUILT_IN_ACCENT = "#2563eb";

type Draft = { title: string; theme: string; accentColor: string; logo: string; customCss: string };

const toDraft = (s: Settings | undefined): Draft => ({
  title: s?.title ?? "", theme: s?.theme || "auto", accentColor: s?.accentColor ?? "", logo: s?.logo ?? "", customCss: s?.customCss ?? "",
});

// 公开页的外观：UpdateSettings 整体替换五项，表单因此总是提交全部字段。
export function Appearance() {
  const qc = useQueryClient();
  const settings = useQuery(AdminService.method.getSettings, {});
  // draft 为空时表单显示 hub 的当前值；保存成功后改为 hub 回显的实际保存值（标题已清洗、主色已转小写）。
  const [draft, setDraft] = useState<Draft | null>(null);
  const [saved, setSaved] = useState(false);
  const [fileError, setFileError] = useState<string | null>(null);
  const update = useMutation(AdminService.method.updateSettings, {
    onSuccess: (r) => {
      setDraft(toDraft(r.settings));
      setSaved(true);
      return qc.invalidateQueries({ queryKey: createConnectQueryKey({ schema: AdminService.method.getSettings, cardinality: "finite" }) });
    },
  });
  const gate = queryGate(settings);
  if (!gate.ready) return gate.loading ?? errorBanner(...gate.errors);
  const form = draft ?? toDraft(gate.data.settings);
  const edit = (patch: Partial<Draft>) => {
    setDraft({ ...form, ...patch });
    setSaved(false);
    update.reset();
  };
  const problems = sizeProblems(form.title, form.logo, form.customCss);
  const pickLogo = (e: ChangeEvent<HTMLInputElement>) => {
    const file = e.target.files?.[0];
    e.target.value = "";
    if (!file) return;
    const reader = new FileReader();
    reader.onload = () => { setFileError(null); edit({ logo: String(reader.result) }); };
    reader.onerror = () => setFileError(`读取 ${file.name} 失败：${String(reader.error)}`);
    reader.readAsDataURL(file);
  };
  const submit = (e: FormEvent) => {
    e.preventDefault();
    if (problems.length === 0) update.mutate({ settings: form });
  };
  return (
    <section>
      {gate.banner}
      <h1>外观</h1>
      <p className="muted">
        公开页（站点根路径 /）的标题、明暗、主色、logo 与自定义 CSS。保存后，访客刷新公开页才看到新外观；浏览器还可能再用最多 5 分钟的缓存。
        要改页面结构，用 hub 的 --public-dir 换掉整个公开页。
      </p>
      <form className="card edit-form" aria-label="公开页外观" onSubmit={submit}>
        <label>
          标题
          <input value={form.title} placeholder={DEFAULT_TITLE} onChange={(e) => edit({ title: e.target.value })} />
        </label>
        <p className="muted">最多 {MAX_TITLE_CHARS} 个字符；留空用「{DEFAULT_TITLE}」。</p>
        <label>
          明暗
          <select value={form.theme} onChange={(e) => edit({ theme: e.target.value })}>
            {THEMES.map((t) => <option key={t} value={t}>{THEME_LABELS[t]}</option>)}
          </select>
        </label>
        <div className="row">
          <label>
            主色
            <input value={form.accentColor} placeholder="#rrggbb，留空用内置配色" onChange={(e) => edit({ accentColor: e.target.value })} />
          </label>
          <label>
            取色
            <input type="color" value={form.accentColor || BUILT_IN_ACCENT} onChange={(e) => edit({ accentColor: e.target.value })} />
          </label>
          <button type="button" className="link" onClick={() => edit({ accentColor: "" })} disabled={form.accentColor === ""}>用内置配色</button>
        </div>
        <div className="row">
          <label>
            logo
            <input type="file" accept={LOGO_TYPES.join(",")} onChange={pickLogo} />
          </label>
          {form.logo && <img src={form.logo} alt="logo 预览" className="logo-preview" />}
          <button type="button" className="link" onClick={() => edit({ logo: "" })} disabled={form.logo === ""}>移除 logo</button>
        </div>
        <label>
          自定义 CSS
          <textarea value={form.customCss} onChange={(e) => edit({ customCss: e.target.value })} spellCheck={false} />
        </label>
        <p className="muted">排在公开页内置样式之后。只接受 CSS，不能含 &lt;/。</p>
        {fileError && <p role="alert" className="error">{fileError}</p>}
        {problems.map((p) => <p key={p} role="alert" className="error">{p}</p>)}
        {update.error != null && <p role="alert" className="error">{errorText(update.error)}</p>}
        {saved && <p role="status">已保存。</p>}
        <button type="submit" disabled={update.isPending || problems.length > 0}>保存</button>
      </form>
    </section>
  );
}
```

`web/src/App.tsx`：在 `import { ApiTokens } from "./pages/ApiTokens";` 之后加 `import { Appearance } from "./pages/Appearance";`；在 `{ path: "tokens", Component: ApiTokens },` 之后加 `{ path: "appearance", Component: Appearance },`。

`web/src/components/Layout.tsx`：在 `<NavLink to="/tokens">API token</NavLink>` 之后加 `<NavLink to="/appearance">外观</NavLink>`。

`web/src/styles.css`：在 `.edit-form textarea { … }` 一行之后加：

```css
.logo-preview { height: 32px; width: auto; }
```

- [ ] **Step 4: 跑绿**

```bash
cd /Users/xjetry/work/vibe/probe-public && pnpm --dir web exec tsc -b > /tmp/m5-t11-tsc.log 2>&1; echo $?
cd /Users/xjetry/work/vibe/probe-public && pnpm --dir web exec vitest run > /tmp/m5-t11-green.log 2>&1; echo $?
```

Expected：两条都是 0。

- [ ] **Step 5: 提交**

```bash
cd /Users/xjetry/work/vibe/probe-public && git add web/src && git commit -m "web: 面板的外观页，编辑公开页的标题、明暗、主色、logo 与自定义 CSS" -m "UpdateSettings 整体替换，表单总是提交全部五项；保存后表单改显 hub 回显的实际保存值。约束由 hub 裁决，页面显示它的错误原文；页面只在提交前查标题、logo 与 CSS 的字节数：超过管理请求解码预算的请求在校验之前就被拒绝，那时的错误说不出是哪个字段。页面上的上限与取值和 settings.go 是两份同值常量，由测试直接读 settings.go 对照。" > /tmp/m5-t11-commit.log 2>&1; echo $?
```

Expected：0。

- [ ] **Step 6: 缺陷注入**

逐项进行：改动 → `git diff --stat` 非空 → 跑命令 → 核对红的原因 → `git checkout -- <文件>`。命令都是 Step 2 那条。

| | 改动 | 期望 |
|---|---|---|
| a | `appearance.ts` 的 `MAX_LOGO_BYTES` 改成 `256 * 1024` | 1，`appearanceLimits.test.ts` 红在 `expected 262144 to be 131072` |
| b | `appearance.ts` 的 `THEMES` 末尾加 `"sepia"` | 1，红在 `expected [ 'auto', 'light', 'dark', 'sepia' ] to deeply equal [ 'auto', 'light', 'dark' ]` |
| c | `internal/hub/api/settings.go` 的 `logoTypes` 去掉 `"image/svg+xml"`（改的是 hub 一侧） | 1，红在 `LOGO_TYPES` 与 `logoTypes` 不等：测试确实读的是 `settings.go` |
| d | `sizeProblems` 里 `utf8Bytes(customCss)` 改成 `customCss.length` | 1，"大小按 UTF-8 字节计"红在 `expected [] to deeply equal [ Array(1) ]` |
| e | `Appearance.tsx` 的 `update.mutate({ settings: form })` 改成 `update.mutate({ settings: { title: form.title, theme: form.theme } })` | 1，"保存提交全部五项"红在 `to match object`；logo 用例红在 `expected [ '' ] to deeply equal [ 'data:image/png;base64,iVBORw==' ]` |
| f | `onSuccess` 里 `setDraft(toDraft(r.settings))` 改成 `setDraft(null)` | 1，"保存提交全部五项"红在标题仍为旧值，不是 hub 回显的 `新标题` |
| g | logo 输入框去掉 `accept={LOGO_TYPES.join(",")}` | 1，logo 用例红在 `accept` 属性 |
| h | `submit` 里去掉 `problems.length === 0` 的判断（按钮的 `disabled` 保留） | 1，"超出大小上限的 logo"红在 `expected 'data:image/png;base64,AAAA…' to be 'data:image/png;base64,iVBORw=='`：直接触发的 submit 事件把超限的请求发了出去 |
| i | 保存按钮的 `disabled` 去掉 `problems.length > 0` | 1，同一用例红在 `toBeDisabled` |
| j | `Layout.tsx` 删去"外观"导航 | 1，`Layout.test.tsx` 红在找不到 `link` `外观` |
| k | `sizeProblems` 里标题那一条的条件改成 `false && titleBytes > MAX_TITLE_BYTES` | 1，"大小按 UTF-8 字节计"红在 `expected [] to deeply equal [ Array(1) ]` |
| l | `internal/hub/api/settings.go` 的 `maxTitleBytes` 改成 `2 << 10`（改的是 hub 一侧） | 1，`expected 1024 to be 2048` |
| m | 标题输入框去掉 `placeholder={DEFAULT_TITLE}` | 1，"标题留空时提示内置标题"红在 `toHaveAttribute("placeholder", "服务器状态")` |
| n | `useMutation` 的选项加 `onError: () => setDraft(null)` | 1，"hub 拒绝时显示错误原文并保留草稿"红在 `toHaveValue(</style>)` |
| o | "移除 logo"按钮的 `onClick` 改成 `() => setSaved(false)` | 1，"选中的 logo 以 data: URL 提交，可移除"红在 `expected <img alt="logo 预览" …> to be null` |

---

### Task 12: e2e：公开服务、外观、存储统计与 --public-dir

**Files:**
- Modify: `scripts/e2e.sh`
- Modify: `README.md`（"反代与 `--trusted-proxies`"一节的一句）

**Interfaces:**
- Consumes：Task 2–11 的全部对外行为。e2e 用 curl 走纯 HTTP：公开服务用 Connect GET（查询串里的 `connect=v1&encoding=json&message=…`），与入口卡片"公开数据"一节的例子同一形态；那个例子本身也由 `run_card_examples` 执行，重启后的第二轮要求它输出非空，靠的是 Task 6 把 node1 标为公开。

前面的任务已在各自的提交里改了 e2e 的两处：Task 6 把 node1 标为公开（卡片的公开数据例子要求非空输出），Task 8 把就绪判据换成匿名 `GetSite` 并删去根路径重定向的断言。本任务补上公开页、公开服务、外观、存储统计与替换目录的断言。

- [ ] **Step 1: 改 e2e.sh**

以下七处按顺序替换，原文在文件里都只出现一次（原文以 Task 8 提交后的文件为准）。

**改动 1.** 在 `# 卡片示例取自 hub 刚下发的那份。` 这行注释之前加两个辅助函数：

原文：

```sh
# 卡片示例取自 hub 刚下发的那份。
```

改为：

```sh
# pubget 名字 方法 请求消息：以 GET 匿名调用 PublicService，不带 cookie 与 token；打印状态码，
# 响应体落 $work/pub-<名字>.json，响应头落 $work/pub-<名字>.headers。
pubget() {
  name=$1; method=$2; msg=$3
  curl -sS -G -o "$work/pub-$name.json" -D "$work/pub-$name.headers" -w '%{http_code}' \
    --data-urlencode connect=v1 --data-urlencode encoding=json --data-urlencode "message=$msg" "$base/probe.v1.PublicService/$method"
}

# hdr 名字 头名：打印 $work/pub-<名字>.headers 里该头的值（头名不分大小写，去掉行尾 CR）；没有这个头时不打印。
hdr() {
  awk -v want="$2" 'BEGIN { want = tolower(want) } { sub(/\r$/, "") } tolower(substr($0, 1, length(want) + 2)) == want ": " { print substr($0, length(want) + 3) }' "$work/pub-$1.headers"
}

# 卡片示例取自 hub 刚下发的那份。
```

**改动 2.** 根路径是内置公开页：在面板检查之前加公开页的检查，面板检查补一条资源路径。

原文：

```sh
[ "$(curl -sS -o "$work/admin.html" -w '%{http_code}' "$base/admin/")" = 200 ] || { echo "FAIL: /admin/ not served"; exit 1; }
grep -q 'id="root"' "$work/admin.html" || { echo "FAIL: panel index missing root element"; exit 1; }
```

改为：

```sh
# 根路径是内置公开页，面板在 /admin/；两者各有一份构建产物，资源分别引用 /assets/ 与 /admin/assets/。
[ "$(curl -sS -o "$work/pub-index.html" -D "$work/pub-index.headers" -w '%{http_code}' "$base/")" = 200 ] || { echo "FAIL: / did not serve the public page"; exit 1; }
grep -q 'src="/assets/' "$work/pub-index.html" || { echo "FAIL: public page does not load its own bundle"; cat "$work/pub-index.html"; exit 1; }
[ -n "$(hdr index Content-Security-Policy)" ] || { echo "FAIL: CSP header missing on the public page"; cat "$work/pub-index.headers"; exit 1; }
[ "$(curl -sS -o "$work/admin.html" -w '%{http_code}' "$base/admin/")" = 200 ] || { echo "FAIL: /admin/ not served"; exit 1; }
grep -q 'id="root"' "$work/admin.html" || { echo "FAIL: panel index missing root element"; exit 1; }
grep -q 'src="/admin/assets/' "$work/admin.html" || { echo "FAIL: panel does not load its own bundle"; cat "$work/admin.html"; exit 1; }
```

**改动 3.** 匿名的 `GetSite` 与外观设置：在第一次 `Login` 前后插入。

原文：

```sh
[ "$(rpc GetSnapshot '{}')" = 401 ] || { echo "FAIL: anonymous GetSnapshot was not 401"; exit 1; }
login_body=$(jq -nc --arg password "$admin_pw" '{password: $password}')
[ "$(rpc Login "$login_body")" = 200 ] || { echo "FAIL: login"; cat "$work/Login.json"; exit 1; }
```

改为：

```sh
[ "$(rpc GetSnapshot '{}')" = 401 ] || { echo "FAIL: anonymous GetSnapshot was not 401"; exit 1; }
# 公开服务匿名可达；从未保存过外观时明暗为 auto，其余为空（JSON 里省略）。
[ "$(pubget site-default GetSite '{}')" = 200 ] || { echo "FAIL: anonymous GetSite"; cat "$work/pub-site-default.json"; exit 1; }
jq -e '. == {theme: "auto"}' "$work/pub-site-default.json" > /dev/null || { echo "FAIL: default site settings"; cat "$work/pub-site-default.json"; exit 1; }
[ "$(hdr site-default Cache-Control)" = "max-age=300" ] || { echo "FAIL: GetSite Cache-Control"; cat "$work/pub-site-default.headers"; exit 1; }
login_body=$(jq -nc --arg password "$admin_pw" '{password: $password}')
[ "$(rpc Login "$login_body")" = 200 ] || { echo "FAIL: login"; cat "$work/Login.json"; exit 1; }
# 外观整体替换并回显 hub 实际保存的值（主色转小写），公开页随即拿到；被拒的更新什么都不写。
settings_body='{"settings": {"title": "e2e 状态", "theme": "dark", "accentColor": "#FF5500", "customCss": ".card { border-width: 2px; }"}}'
[ "$(rpc UpdateSettings "$settings_body")" = 200 ] || { echo "FAIL: UpdateSettings"; cat "$work/UpdateSettings.json"; exit 1; }
jq -e '.settings.accentColor == "#ff5500"' "$work/UpdateSettings.json" > /dev/null || { echo "FAIL: UpdateSettings echo"; cat "$work/UpdateSettings.json"; exit 1; }
[ "$(pubget site GetSite '{}')" = 200 ] || { echo "FAIL: GetSite after update"; exit 1; }
jq -e '. == {title: "e2e 状态", theme: "dark", accentColor: "#ff5500", customCss: ".card { border-width: 2px; }"}' "$work/pub-site.json" > /dev/null || { echo "FAIL: GetSite does not serve the saved settings"; cat "$work/pub-site.json"; exit 1; }
[ "$(rpc UpdateSettings '{"settings": {"theme": "auto", "customCss": "a</style>"}}')" = 400 ] || { echo "FAIL: CSS containing </ was accepted"; cat "$work/UpdateSettings.json"; exit 1; }
grep -q 'settings.custom_css must not contain' "$work/UpdateSettings.json" || { echo "FAIL: error must name the field"; cat "$work/UpdateSettings.json"; exit 1; }
[ "$(pubget site-after-reject GetSite '{}')" = 200 ] && cmp -s "$work/pub-site.json" "$work/pub-site-after-reject.json" || { echo "FAIL: a rejected update changed the site"; cat "$work/pub-site-after-reject.json"; exit 1; }
```

**改动 4.** `UpdateNode` 的回显补查公开标志，随后核对公开服务。此时两族历史都已有 node1 的分钟行（上面的探测结果循环已等到刷出）。

原文：

```sh
jq -e '.node.trafficResetDay == 15' "$work/UpdateNode.json" > /dev/null || { echo "FAIL: reset day not echoed"; cat "$work/UpdateNode.json"; exit 1; }
```

改为：

```sh
jq -e '.node.trafficResetDay == 15 and .node.public == true' "$work/UpdateNode.json" > /dev/null || { echo "FAIL: reset day or public flag not echoed"; cat "$work/UpdateNode.json"; exit 1; }

# 公开服务只给 node1（公开）；node2 保持私有。快照响应缓存 1 秒，公开之后最迟 1 秒出现在公开快照里。
i=0
until [ "$(pubget snapshot GetSnapshot '{}')" = 200 ] && jq -e --arg id "$node1" '[(.nodes // [])[].id] == [$id]' "$work/pub-snapshot.json" > /dev/null; do
  i=$((i + 1)); [ "$i" -lt 10 ] || { echo "FAIL: public snapshot does not list exactly node1"; cat "$work/pub-snapshot.json"; exit 1; }; sleep 0.5
done
# 公开的主机信息只有这五项：主机名、内核、agent 版本、ICMP 可用性不出现在线上。
jq -e --arg os "$EXPECT_OS" '.reportIntervalMs == 4000 and (.nodes[0].facts | (.os | contains($os)) and .arch == "amd64" and (keys - ["os", "arch", "virtualization", "cpuModel", "cpuCores"]) == []) and ((.nodes[0].metrics // {}) | has("bootId") | not)' "$work/pub-snapshot.json" > /dev/null || { echo "FAIL: public snapshot shape"; cat "$work/pub-snapshot.json"; exit 1; }
[ "$(hdr snapshot Cache-Control)" = "max-age=1" ] || { echo "FAIL: GetSnapshot Cache-Control"; cat "$work/pub-snapshot.headers"; exit 1; }
# 缓存头只给 GET：POST 的响应不进浏览器缓存，不带这个头。
[ "$(curl -sS -o /dev/null -D "$work/pub-post.headers" -w '%{http_code}' -H 'Content-Type: application/json' --data '{}' "$base/probe.v1.PublicService/GetSnapshot")" = 200 ] || { echo "FAIL: POST GetSnapshot"; exit 1; }
[ -z "$(hdr post Cache-Control)" ] || { echo "FAIL: POST response carries Cache-Control"; cat "$work/pub-post.headers"; exit 1; }
[ "$(pubget metrics QueryMetrics "$query_body")" = 200 ] || { echo "FAIL: public QueryMetrics"; cat "$work/pub-metrics.json"; exit 1; }
jq -e '.level == "1m" and any(.series[] | select(.name == "cpu") | .samples[]; .n > 0)' "$work/pub-metrics.json" > /dev/null || { echo "FAIL: public QueryMetrics shape"; cat "$work/pub-metrics.json"; exit 1; }
[ "$(hdr metrics Cache-Control)" = "max-age=60" ] || { echo "FAIL: QueryMetrics Cache-Control"; cat "$work/pub-metrics.headers"; exit 1; }
# 公开节点即公开它的探测目标：序列带任务当前的种类与目标。
[ "$(pubget probes QueryProbes "$probe_body")" = 200 ] || { echo "FAIL: public QueryProbes"; cat "$work/pub-probes.json"; exit 1; }
jq -e --arg icmp "$icmp_task" --arg tcp "$tcp_task" --arg target "host.docker.internal:$port" '[.series[] | {taskId, kind, target}] | sort_by(.taskId) == ([{taskId: $icmp, kind: "PROBE_KIND_ICMP", target: "127.0.0.1"}, {taskId: $tcp, kind: "PROBE_KIND_TCP", target: $target}] | sort_by(.taskId))' "$work/pub-probes.json" > /dev/null || { echo "FAIL: public probe series lack kind and target"; cat "$work/pub-probes.json"; exit 1; }
# 私有节点与不存在的节点逐字节同一个 NotFound，且不进缓存：节点改为公开后浏览器不会继续用它。
for method in QueryMetrics QueryProbes; do
  private_body=$(jq -nc --arg nodeId "$node2" --argjson from "$((now - 3600))" --argjson to "$((now + 60))" '{nodeId: $nodeId, from: $from, to: $to, maxPoints: 100}')
  missing_body=$(jq -nc --argjson from "$((now - 3600))" --argjson to "$((now + 60))" '{nodeId: "999999", from: $from, to: $to, maxPoints: 100}')
  [ "$(pubget "private-$method" "$method" "$private_body")" = 404 ] || { echo "FAIL: $method on a private node was not 404"; cat "$work/pub-private-$method.json"; exit 1; }
  [ "$(pubget "missing-$method" "$method" "$missing_body")" = 404 ] || { echo "FAIL: $method on a missing node was not 404"; cat "$work/pub-missing-$method.json"; exit 1; }
  cmp -s "$work/pub-private-$method.json" "$work/pub-missing-$method.json" || { echo "FAIL: $method tells private and missing nodes apart"; cat "$work/pub-private-$method.json" "$work/pub-missing-$method.json"; exit 1; }
  [ "$(hdr "private-$method" Cache-Control)" = no-store ] || { echo "FAIL: $method NotFound is cacheable"; cat "$work/pub-private-$method.headers"; exit 1; }
done
```

**改动 5.** 重启时带上 `--public-dir`：先准备替换目录，重启后核对它的服务范围与越界防护。

原文：

```sh
kill "$hub"; wait "$hub"
hub=""

# 重启：流量状态、重置日与被 Drain 出的分钟行都必须还在。
PROBE_OFFLINE_AFTER=12s bin/probe-hub serve --db "$db" --listen "127.0.0.1:$port" --timezone UTC >> "$work/hub.log" 2>&1 &
hub=$!
wait_hub
```

改为：

```sh
kill "$hub"; wait "$hub"
hub=""

# 重启时换上替换目录：它接管 / 下除 /admin 与 RPC 之外的路径；指向目录外的符号链接拿不到目标内容。
mkdir -p "$work/site/assets"
printf '%s\n' '<!doctype html><title>e2e custom public page</title>' > "$work/site/index.html"
printf '%s\n' 'body { color: red }' > "$work/site/assets/app.css"
printf '%s\n' 'outside secret' > "$work/outside.txt"
ln -s ../outside.txt "$work/site/leak.txt"

# 重启：流量状态、重置日与被 Drain 出的分钟行都必须还在。
PROBE_OFFLINE_AFTER=12s bin/probe-hub serve --db "$db" --listen "127.0.0.1:$port" --timezone UTC --public-dir "$work/site" >> "$work/hub.log" 2>&1 &
hub=$!
wait_hub
[ "$(curl -sS -o "$work/pub-dir-index.html" -D "$work/pub-dir-index.headers" -w '%{http_code}' "$base/")" = 200 ] || { echo "FAIL: --public-dir index not served"; exit 1; }
grep -q 'e2e custom public page' "$work/pub-dir-index.html" || { echo "FAIL: / is not the --public-dir index"; cat "$work/pub-dir-index.html"; exit 1; }
[ "$(hdr dir-index Content-Security-Policy)" = "frame-ancestors 'none'" ] || { echo "FAIL: --public-dir CSP"; cat "$work/pub-dir-index.headers"; exit 1; }
[ "$(hdr dir-index X-Content-Type-Options)" = nosniff ] || { echo "FAIL: --public-dir nosniff"; cat "$work/pub-dir-index.headers"; exit 1; }
[ "$(hdr dir-index Cache-Control)" = no-cache ] || { echo "FAIL: --public-dir Cache-Control"; cat "$work/pub-dir-index.headers"; exit 1; }
[ "$(curl -sS -o "$work/pub-dir-css" -w '%{http_code}' "$base/assets/app.css")" = 200 ] && grep -q 'color: red' "$work/pub-dir-css" || { echo "FAIL: --public-dir asset"; exit 1; }
[ "$(curl -sS -o /dev/null -w '%{http_code}' "$base/assets/missing.js")" = 404 ] || { echo "FAIL: a missing asset under --public-dir must be 404"; exit 1; }
for path in /leak.txt /../outside.txt; do
  curl -sS --path-as-is -L -o "$work/pub-dir-escape" "$base$path"
  if grep -q 'outside secret' "$work/pub-dir-escape"; then echo "FAIL: $path read a file outside --public-dir"; exit 1; fi
  grep -q 'e2e custom public page' "$work/pub-dir-escape" || { echo "FAIL: $path did not fall back to index.html"; cat "$work/pub-dir-escape"; exit 1; }
done
[ "$(curl -sS -o "$work/admin-after-dir.html" -w '%{http_code}' "$base/admin/")" = 200 ] && grep -q 'src="/admin/assets/' "$work/admin-after-dir.html" || { echo "FAIL: --public-dir shadowed the panel"; exit 1; }
[ "$(pubget site-after-dir GetSite '{}')" = 200 ] || { echo "FAIL: --public-dir shadowed PublicService"; exit 1; }
```

**改动 6.** 最后一次 `Logout` 之前取 `GetStorageStats`：

原文：

```sh
[ "$(rpc Logout '{}')" = 200 ] || { echo "FAIL: logout after restart"; exit 1; }
kill "$hub"; wait "$hub"
hub=""
```

改为：

```sh
[ "$(rpc GetStorageStats '{}')" = 200 ] || { echo "FAIL: GetStorageStats"; cat "$work/GetStorageStats.json"; exit 1; }
jq -e '(.dbBytes | tonumber) > 0 and (.tables | length) > 0' "$work/GetStorageStats.json" > /dev/null || { echo "FAIL: GetStorageStats shape"; cat "$work/GetStorageStats.json"; exit 1; }
[ "$(rpc Logout '{}')" = 200 ] || { echo "FAIL: logout after restart"; exit 1; }
kill "$hub"; wait "$hub"
hub=""
```

**改动 7.** `get()` 定义之后加 API 与 CLI 的对照、`db_bytes` 与 `setting` 行数：

原文：

```sh
get() { sed -n "s/^$1: //p" "$work/stats.txt"; }
```

改为：

```sh
get() { sed -n "s/^$1: //p" "$work/stats.txt"; }
# API 与 CLI 同一来源：两边列出同一组表（行数在两次读取之间会变，只比表名）。
jq -r '.tables[].name' "$work/GetStorageStats.json" > "$work/stats-api-tables.txt"
sed -n '/^db_bytes: /d; s/^\([a-z0-9_]*\): [0-9][0-9]*$/\1/p' "$work/stats.txt" > "$work/stats-cli-tables.txt"
[ -s "$work/stats-cli-tables.txt" ] && cmp -s "$work/stats-api-tables.txt" "$work/stats-cli-tables.txt" || { echo "FAIL: GetStorageStats and probe-hub stats list different tables"; cat "$work/stats-api-tables.txt" "$work/stats-cli-tables.txt"; exit 1; }
[ "$(get db_bytes)" -gt 0 ] || { echo "FAIL: db_bytes"; exit 1; }
[ "$(get setting)" = 5 ] || { echo "FAIL: setting rows"; exit 1; }
```

- [ ] **Step 2: 语法检查**

```bash
cd /Users/xjetry/work/vibe/probe-public && sh -n scripts/e2e.sh > /tmp/m5-t12-syntax.log 2>&1; echo $?
```

Expected：0，日志为空。

- [ ] **Step 3: 跑 e2e**

```bash
cd /Users/xjetry/work/vibe/probe-public && make e2e > /tmp/m5-t12-e2e.log 2>&1; echo $?
```

Expected：
- 0。`E2E_TIER1` 的两个镜像（Debian、Alpine）各跑一遍，日志里两次 `E2E OK`。
- 每一遍的 `--- stats ---` 段有 `setting: 5`，`db_bytes:` 为正数。
- 镜像冷拉取报 `toomanyrequests` 是 Docker Hub 的匿名限速，属于环境；换个时间重跑同一条命令，不改脚本。

各轮的工件目录由日志里的 `E2E artifacts:` 行给出，失败时先看其中的 `pub-*.json` 与 `pub-*.headers`。

- [ ] **Step 4: 提交**

```bash
cd /Users/xjetry/work/vibe/probe-public && git add scripts/e2e.sh && git commit -m "e2e: 公开服务、外观设置、存储统计与 --public-dir" -m "根路径改为内置公开页，面板与公开页各自引用自己的资源路径。匿名 GET 调公开服务：默认外观、保存后下发与被拒更新不落库；公开快照只列公开的 node1、主机信息只有五项、历史与探测序列（带种类与目标）可查、各方法的 Cache-Control 与 POST 不带缓存头；私有节点与不存在的节点逐字节同一个 NotFound 且 no-store。重启时换上 --public-dir：替换页、它的响应头、assets 未命中 404、越界符号链接与 .. 拿不到目录外内容、/admin 与 RPC 不被遮蔽。GetStorageStats 与 probe-hub stats 列出同一组表。" > /tmp/m5-t12-commit.log 2>&1; echo $?
```

Expected：0。

- [ ] **Step 5: 缺陷注入**

e2e 一遍要几分钟，注入只跑一个镜像。每项：改动 → `git diff --stat` 非空 → 重建 hub → 跑命令 → 核对首个 `FAIL` 行 → `git checkout -- <文件>` → 再重建 hub。

重建与命令：

```bash
cd /Users/xjetry/work/vibe/probe-public && go build -o bin/probe-hub ./cmd/hub > /tmp/m5-t12-inj-build.log 2>&1; echo $?
cd /Users/xjetry/work/vibe/probe-public && AGENT_IMAGE=alpine:3.21 EXPECT_OS=Alpine scripts/e2e.sh > /tmp/m5-t12-inj-<项>.log 2>&1; echo $?
```

| | 改动 | 期望 |
|---|---|---|
| a | `internal/hub/api/public.go` 的 `requirePublic` 里 `if !public {` 的分支改为先查 `p.store.NodeExists(ctx, id)`，存在时返回 `connect.NewError(connect.CodeNotFound, errors.New("node_id: this node is not public"))`，不存在时仍返回 `noPublicNode()` | 1，`FAIL: QueryMetrics tells private and missing nodes apart` |
| b | `internal/hub/web/dir.go` 的 `open` 里 `root.OpenFile(filepath.FromSlash(rel), …)` 改成 `os.OpenFile(filepath.Join(dir, filepath.FromSlash(rel)), os.O_RDONLY\|syscall.O_NONBLOCK, 0)`（绕过 `os.Root`） | 1，`FAIL: /leak.txt read a file outside --public-dir` |
| c | `internal/hub/api/public.go` 的 `GetSnapshot` 里 `p.store.ListPublicNodes(ctx)` 改成 `p.store.ListNodes(ctx)`（私有节点也进公开快照） | 1，约 5 秒后 `FAIL: public snapshot does not list exactly node1` |
| d | `cacheControl` 去掉 `r.Method != http.MethodGet` 的分支（POST 也写缓存头） | 1，`FAIL: POST response carries Cache-Control` |
| e | `dirHeaders` 的 CSP 改成 `default-src 'self'; frame-ancestors 'none'` | 1，`FAIL: --public-dir CSP` |
| f | `cmd/hub/stats.go` 的循环跳过 `setting` 表 | 1，`FAIL: GetStorageStats and probe-hub stats list different tables` |
| g | `cleanSettings` 回显的主色去掉 `strings.ToLower` | 1，`FAIL: UpdateSettings echo` |
| h | `UpdateSettings` 在 `cleanSettings` 之前先用原值 `SaveSiteSettings` | 1，`FAIL: a rejected update changed the site` |

e2e 的其余断言不逐条在 e2e 上注入，各自的缺陷由单测的注入覆盖（一遍 e2e 要几分钟，逐条注入不现实）：
- 根路径是内置公开页：Task 8 注入 n。资源路径是 `/assets/`：Task 10 注入 j（`base`）。CSP 头：Task 8 注入 m。
- 默认外观：Task 2 注入 g。保存后下发：Task 5 注入 p。`</` 被拒且错误写明字段（单测逐字比对错误原文）：Task 3 注入 c。
- 公开快照的主机信息只有五项：Task 5 注入 c。没有 `bootId`：Task 5 注入 y。
- `GetSite` 的 `max-age=300`、`GetSnapshot` 的 `max-age=1`、历史的 `max-age=60`：`TestPublicCacheControlPerMethod` 与 `TestCachePolicy` 逐方法写字面值，Task 6 注入 i。
- 公开历史有数据：Task 5 注入 v、w。探测序列带种类与目标：Task 5 注入 x（公开端）、Task 1 注入 a 与 Task 5 注入 u（管理端）。
- 私有与不存在的节点响应 `no-store`：Task 6 注入 c。
- `--public-dir` 的 nosniff 与 `no-cache`：Task 8 注入 p、q。`assets/` 下 404：Task 8 注入 r。`..` 与符号链接不越界、回落 index.html：Task 8 注入 a。`/admin/` 不被遮蔽：Task 8 注入 k。RPC 不被遮蔽：Task 8 注入 o。
- `GetStorageStats` 的 `db_bytes` 与 `setting` 行数：Task 2 注入 d、i。

- [ ] **Step 6: README 的反代一节写明限流的量级**

反代后不配 `--trusted-proxies` 时，公开页与注册的限流、登录失败锁定都按代理地址计，全体访客共用一个桶。这是部署配置问题，不改限流，写在运维会看的两处：flag 帮助（Task 8）与这里。量级从常量推出：公开服务每个来源的桶容量 60、每秒补充 10；公开页（总览与节点页）每 2 秒轮询一次快照，一个打开的页面每秒 0.5 次；超过 20 个页面时轮询持续多于补充，余量 60 以每秒 `0.5N − 10` 次耗尽（30 个页面时 60 / 5 = 12 秒）。

`README.md` 的"反代与 `--trusted-proxies`"一节，原文（只出现一次）：

```
不设置时一律不信：登录失败锁定与按来源地址的限流都按反代的地址计，所有访客共用一个计数，会话 cookie 也不带 `Secure`。
```

替换为：

```
不设置时一律不信：公开页与注册的限流按来源地址计，反代后不配它，所有访客共用代理地址的一个桶；登录失败锁定同样按代理地址计，会话 cookie 也不带 `Secure`。量级：公开服务每个来源的桶容量 60、每秒补充 10，每个打开的公开页每 2 秒轮询一次快照（每秒 0.5 次），同时打开的公开页超过 20 个，轮询就持续多于补充，60 次的余量用完后访客开始收到 429（30 个页面时约 12 秒后）。
```

```bash
cd /Users/xjetry/work/vibe/probe-public && grep -c '共用代理地址的一个桶' README.md > /tmp/m5-t12-readme.log 2>&1; echo $?
cd /Users/xjetry/work/vibe/probe-public && git add README.md && git commit -m "docs: README 写明反代后不配 --trusted-proxies 时全体访客共用一个限流桶及其量级" -m "公开页与注册的限流、登录失败锁定都按来源地址计；反代之后不配 --trusted-proxies，来源都是代理地址。公开服务的桶容量 60、每秒补充 10，每个打开的公开页每 2 秒轮询一次快照，超过 20 个页面就持续多于补充。" > /tmp/m5-t12-readme-commit.log 2>&1; echo $?
```

Expected：两条都是 0，第一条的日志为 `1`（替换落地）。

---

## 执行顺序与并行

**本计划内**
- 依赖：
  - Task 1 最先：后面的 proto、Go 与 TS 都建立在拆出的 `query.proto` 上。
  - Task 2（store）与 Task 4（ratelimit）只依赖 Task 1。
  - Task 3 依赖 Task 2（`SiteSettings`、`StorageStats`）。
  - Task 5 依赖 Task 2、3（`GetSite` 读 `SiteSettings`；与 Task 3 都改 proto 并 `make gen`）。
  - Task 6 依赖 Task 4（`ratelimit.BySource`）与 Task 5；Task 7 依赖 Task 6；Task 8 依赖 Task 5（就绪冒烟用匿名 `GetSite`）。
  - Task 9 只依赖 Task 1（`seriesLabels`），只改 `web/src` 下的非生成文件。
  - Task 10 依赖 Task 5（`public_pb.ts`）、Task 8（`dist-public` 的 embed 目录与 `.gitignore`）与 Task 9。
  - Task 11 依赖 Task 3（`Settings`）与 Task 10（`DEFAULT_TITLE`）。
  - Task 12 最后。
- 推荐单工作树按 1→12 顺序做。要并行时，Task 9 可以在 Task 1 提交之后另开工作树与 Task 2–8 同时进行，Task 10 开始前合回：Task 2–8 只经 `make gen` 改 `web/src/gen`，Task 9 不碰生成物，预计不冲突；合回后在合并结果上重跑 `pnpm --dir web exec tsc -b` 与 `pnpm --dir web exec vitest run`。
- e2e：Task 6（卡片例子需要公开节点）与 Task 8（就绪判据）在各自的提交里改了 e2e 的消费方，按推理每个任务边界上 e2e 都应是绿的；但 e2e 一遍要几分钟，只在 Task 12 实跑，中间边界的"应是绿的"没有实跑验证。
- 端口：Task 8 的就绪冒烟用 18079，Task 10 的浏览器验收用 18090；e2e 用 18080/18081，install-accept 用 18085/18086，macOS 计划的 macos-accept 用 18087/18088，彼此不冲突。

**与另外两份计划**

- macOS agent 计划（`/Users/xjetry/work/vibe/probe-macos`，分支 `m5m6-macos`）：
  - `Makefile`：本计划只改 `web` 目标上方的注释；macOS 计划改 `lint`、`build`、发布矩阵变量与 `release`。不同段，两边都保留。
  - **`scripts/macos-accept.sh`（macOS 计划新建）的 `start_hub` 以"`/` 返回 302"判定就绪。** 它用 `go build` 出的 hub，不构建前端；本计划合入后 `/` 是公开页，没构建时是 503 说明页，这个判据永远等不到。这与 Task 8 改掉的两处是同一个判据。后合并者把它改成匿名 `GetSite` 返回 200（与 `scripts/e2e.sh`、`scripts/install-accept.sh` 同一行写法），"302 也可能来自…"的注释同步改成 200。合并前在主干上重新枚举一遍读者：`git grep -n -e '= 302' -e 'RootRedirect' -- scripts cmd internal`。
  - `.github/workflows/ci.yml`：本计划不改。`README.md`：macOS 分支自己新建过 README，与 main 上 Docker 加的 README 是两份各自新建的文件，合并时本就要合成一份；本计划只动 main 上那份的反代一节，与 macOS 的安装段落不重叠。
- hub Docker 镜像（分支 `m5m6-docker`）已在 63d0800 之前合入 main，本计划的基点包含它：
  - 计划里 `store.go`、`store_test.go`、`.gitignore`、`Makefile` 的原文与行号都以含它的基点为准（`store_test.go` 已有 `strings` 与 `os` 的 import，末尾已有 `TestOpenErrorNamesTheDatabasePath`）。
  - `README.md` 由它加入，Task 12 只改其中"反代与 `--trusted-proxies`"一节的一句。
  - `make docker` 先 `$(MAKE) web`：本计划之后 `make web` 同时构建两个入口，镜像自然带上公开页。镜像冒烟（`scripts/docker-smoke.sh`）只查 `/admin/`，面板的 503 说明页文案本计划没有改，它照常成立。
  - 镜像默认参数是否加 `--public-dir`，由控制端决定。
- 合回主干前固定三步：
  1. `git log --oneline $(git merge-base main m5m6-public)..main` 读标题，找同类实现。
  2. `git merge-tree --write-tree --name-only main m5m6-public` 无副作用预演冲突。
  3. 生成物（`gen/`、`web/src/gen/`）冲突不手解，改源文件后重跑 `make gen`。

**控制端核对（与本计划同时提交的 spec）**
- §3.1 ratelimit 一行、§5.3 登录锁定、§3.2 表与 §5.2、§10 里"来源 IP"的写法都已改为来源键（`auth.SourceKey`：IPv4 按地址、映射地址还原、IPv6 按 /64）。
- §10 已补：`.well-known/` 随点文件规则一起不服务（ACME http-01 由反代完成）；反代未配可信代理时 429 的量级从常量推出（每页每 2 秒轮询一次即 0.5 次/秒，补充 10 次/秒，超过 20 个打开的页面后消耗持续多于补充，30 个页面时净流出 5 次/秒、60 的桶约 12 秒耗尽）；构建产物按描述符前缀的核对是构建后的一次性 grep，不是常驻检查。

## 自查记录

**spec 覆盖**

| spec | 落点 |
|---|---|
| §3.2 三种鉴权；公开数据独立消息、默认私有 | Task 5（`Public*` 消息与投影、字段允许列表测试、匿名白名单） |
| §3.3 方法清单；`PublicService` 四方法 `NO_SIDE_EFFECTS`；`AdminService`、`AgentService` 不标；缓存上界分别定 | Task 3（设置与统计三个方法）、Task 5（四方法与 GET）、Task 6（`cache_max_age_s` 与 `Cache-Control`） |
| §5.2 `Register` 限速与公开服务同一实现 | Task 4（`ratelimit.BySource`；`Register` 的限速挪到挂载点，在解码之前；来源键 IPv4 按地址、IPv6 按 /64）、Task 6（公开服务挂同一个中间件） |
| §5.3 CSRF：`AdminService` 不接受 GET | Task 5 的 `TestMuxAcceptsGETOnlyOnPublicService`、e2e 原有的 405 断言 |
| §6.6 `setting` 表 | Task 2（v8，冻结 DDL） |
| §10 两个入口、轮询 2 秒、快照缓存 1 秒、`--public-dir` 与 `os.Root`、外观、卡片与历史页、import 扫描、限流、缓存头、设置上限、静态服务头、`GetStorageStats` | Task 7、8、9、10、11，以及 Task 2、3、6 |
| §12 匿名白名单只含 `PublicService`；公开消息字段允许列表 | Task 5 |
| §14 构建顺序 `buf generate` → 前端 → `go build`；`go build` 与 `go test` 不依赖 Node | Task 10（`make web` 构建两个入口；未构建时 hub 仍编译并给出说明页，Task 8 的测试覆盖） |

用户列的七项：(1) proto → Task 1、3、5；(2) store → Task 2；(3) hub → Task 4–7；(4) 静态服务 → Task 8；(5) web → Task 9–11；(6) e2e → Task 12（另有 Task 6、8 的消费方改动）；(7) 本节。

**Review Focus 与测试的对应**：五条各落在 Review Focus 一节写明的任务与测试里；e2e 另从线上复核了第 3 条（越界符号链接与 `..`）与第 5 条的一半（私有节点的 NotFound 带 `no-store`、与不存在的节点逐字节相同）。

**占位扫描**：没有 TBD、没有"类似 Task N"。每个代码步骤给出整份文件或精确的原文与替换。

**类型与名字一致**：`HistoryMethods`、`useHistory`、`RangePicker`、`HistoryCharts`、`renderWithService`、`applySite`、`DEFAULT_TITLE`、`sizeProblems`、`pubget`、`hdr` 在定义处与全部使用处同名同签名；前端常量 `MAX_*`、`THEMES`、`LOGO_TYPES` 与 `settings.go` 的对应常量由测试对照。

**写计划时实跑过的验证**（仓库之外的两份副本：先在 main c381a27 上做完 Task 1–12 的全部改动，之后把这份改动整体移到 main 63d0800 上——只有 `store_test.go` 末尾与 Docker 加的测试相邻，两边都保留——再加上点文件、IPv6 /64 来源键（`auth.SourceKey`，限流、`Register` 窗口失败计数与登录锁定共用）与两处运维说明；go1.27.1 darwin/arm64，Node 26，pnpm 12，vitest 5.0.1，Vite 8.3.0，Docker 29.4.0 / OrbStack）：
- 63d0800 的副本上重跑了：`go vet ./...`（含 `GOOS=linux`）、全部包 `go test -count=1`、`tsc -b`、`vitest run`（35 个文件 356 个用例）、`make binaries`、alpine 镜像的 e2e（`E2E OK`，`setting: 5`）；Task 4 注入 f、k、l–s 与 Task 8 注入 t–v。下面几条里的其余注入是在 c381a27 的副本上跑的：它们改的文件在两个基点之间没有变化（两个基点之间非文档的改动只有：`store.go` 的 `Open`、`store_test.go` 的 import 与末尾、`.gitignore`、`Makefile`、`cmd/hub/passwd.go` 及其测试、`README.md`、`Dockerfile`、`.dockerignore`、CI 与发布的 workflow、`scripts/` 下的镜像与发布脚本）。
- Go：全部包 `go test -count=1` 与 `go vet ./...` 通过；Task 7 的 29 种请求形态与 connect 直连一致。Task 1–8 注入表的 Go 各项都在这份副本上实跑过，红在写明的原因上，并按实跑输出修正了期望。副本是全部任务做完的状态，期望依赖中间状态的几项做了模拟：Task 5 的 g、h 把根路径换成一律 404 的处理器（Task 8 之前根路径是重定向，非 `/` 一律 404）；Task 1 的 a 删的是 `history.go` 里同一行标注（Task 5 之后它从 `probes.go` 挪到那里）。Task 5 的 i 只在副本上跑出了 Task 6 之后的结果（装配时 panic），它在 Task 5 提交上的期望 `GET status 401, want 405` 没有实跑。
- web：`tsc -b` 通过；`vitest run` 35 个文件 356 个用例通过；Task 9、10、11 表里的每项注入都逐项实跑过，红在写明的原因上（Task 10 的 j 以构建产物核对）；Task 1 的 web 三项（c–e）同样实跑过，e 删的是 `components/History.tsx` 里同一个选项（Task 9 之后探测查询挪到那里）。
- 构建：两个入口都构建成功；`admin.proto` 的描述符前缀只在面板包里、`public.proto` 的只在公开包里。
- 浏览器（Playwright，Chrome）：Task 10 Step 7 的七项逐一得到表中的值；公开请求全是 `GET …?connect=v1&encoding=json&message=%7B%7D`，控制台 0 条告警；`GetSite` 的 `max-age=300` 在浏览器里确实生效（改设置后需 `cache: "reload"` 才看到新值）。
- e2e：`AGENT_IMAGE=alpine:3.21 EXPECT_OS=Alpine scripts/e2e.sh` 以 Task 12 之后的脚本在两份副本上都跑通（`E2E OK`，`setting: 5`）；Task 12 的八项注入在 c381a27 的副本上各自红在表里的 `FAIL` 行。Debian 镜像那一遍没有在写计划时跑，由 Task 12 Step 3 的 `make e2e` 覆盖。
- 就绪判据：Task 8 的冒烟脚本在没有构建公开页的 hub 上输出 `/ returned 503`，注入 g 红在 `GetSite never returned 200`。302 读者的枚举在 c381a27 与 63d0800 上都命中同样六个文件（Docker 的冒烟只查 `/admin/`，不在其列），在两份改完的副本上都为空。
- 没有实跑的：`scripts/install-accept.sh`（要 OrbStack 真机）；它只改了就绪判据一行，与 e2e 同一写法。

**修订说明**：公开端 `QueryProbes` 只标注当前分配给该节点的任务（`Registry.TargetFor`；`probeSeries` 改为接收标注函数，管理端口径不变）；`Register` 的限速从拦截器挪到挂载点中间件 `ratelimit.BySource`，与公开服务共用，解码失败的请求也计数、`Report` 不计；缓存上界改为装配时对 `probe.v1` 全部方法双向核对（`cachePolicy`）；标题加清洗前 1024 字节的上限，`maxBody` 按最坏转义重新推导为 534528 字节；快照缓存键的压缩只读 `Accept-Encoding` 的第一行；存储统计按字面前缀排除内部表；`schema.go`、`stats_test.go`、`cache.proto`、CSP、`--public-dir`（flag 帮助与 `DirHandler` 注释写明与面板同源的后果）、`GetSnapshot` 的注释与公开页外观的时效（`staleTime: Infinity`，刷新后可见）按实际行为改写；限流与快照窗口的测试改用 spec 的字面值；每条新断言补了缺陷注入并在副本上实跑（例外见上），由此补出三处原来照绿的缺口，各加了测试：`serve` 在根路径挂的是哪个处理器、公开历史与管理端同一来源、根路径换成公开页之后两个枚举测试对没挂载的公开过程的判据；Task 12 的 e2e 注入由三项增至八项，其余 e2e 断言逐条写明由哪项单测注入覆盖；开工改为核对控制端建好的工作树；设计决定补 `GetStorageStats` 保持只读口径的理由；末尾加"对 spec 的回写"一节。

**按控制端裁决补充**：`serveFiles` 把路径任一段以 `.` 开头的名字当作不存在（`hidden`；替换目录与嵌入产物各一组用例，Task 8 注入 t–v）；限流的来源键改为 IPv4 按地址、IPv6 按 /64（`SourceOf` 返回归一化后的键，`Register` 的窗口失败计数随之同口径；归一化的位置见下一段），`public.proto`、入口卡片与相关注释同步改口径；`--trusted-proxies` 的 flag 帮助与 README 反代一节写明反代后不配它时全体访客共用一个桶（README 带量级推导）；`--public-dir` 的帮助补上点文件；开工的基点改为 main 63d0800（README 与 Docker 的改动由此进入基点），`store.go`、`store_test.go` 的行号与 import 说明、"与另外两份计划"的 Docker 一节按新基点改写，整份改动在 63d0800 的副本上重跑；"对 spec 的回写"改为对照 63d0800 的 spec。

**来源键移到 auth**：来源键的归一化从 `ratelimit` 挪到 `auth`（`SourceKey`、`DescribeSource`，紧挨 `ClientIP`），`ratelimit.BySource`、`Register` 的窗口失败计数与登录失败锁定三处都调它，依赖方向保持 `ingest`、`api` → `ratelimit` → `auth`；锁定在 `failureTracker` 的三个入口归一化，`ErrLocked` 与登录接口的报错改为按来源的写法；Task 4 加 `SourceKey`、`DescribeSource` 的用例与"同一 /64 的两个地址共用登录失败计数"，注入 l–s 改写（锁定的键改回原地址红在 q、r）；文件结构、设计决定 7、Global Constraints 与回写一节同步。

## 对 spec 的回写

以 main 63d0800 的 spec 逐条核对过计划改变或细化 spec 字面的地方。

**已一致**
- §3.1：`query.proto`、`cache.proto`、`ratelimit/`（令牌桶与挂载点中间件，IPv6 按 /64）。
- §3.3：GET 准入由装配期对 `probe.v1` 全部方法的双向核对承载。
- §5.2：`Register` 的限速在 connect 解码之前、挂载点上按路径匹配，`Report` 不进桶。
- §10：
  - 第一条的 import 扫描。
  - 公开端探测标签的口径。
  - 限流：中间件的位置；来源键 IPv4 按地址、IPv6 按 /64；429 带 `no-store`；反代后不配 `--trusted-proxies` 时全体访客共用一个桶，写在 flag 帮助与 README，不改限流。
  - 快照缓存只缓存规范形态、压缩只读第一行、节点转私有后约 2 秒可见。
  - `GetSite` 只在加载时取、刷新后可见。
  - 标题两道限；解码预算 534528 字节及其影响面（`AdminService` 全部匿名请求，不含 `Register`）。
  - `--public-dir` 与面板同源的后果。
  - 点文件一律当作不存在。
  - `GetStorageStats` 的逻辑大小、只读口径及理由、表名取自 `sqlite_master`。
- §12：匿名白名单恰为四个方法；字段允许列表从响应可达的全部消息出发。

**计划比 spec 细、或 spec 字面仍有出入的**
- §5.2 写"窗口失败计数用它算出的同一个来源地址"，§5.3 写"登录失败按来源 IP 锁定"。计划里这两处与限流用同一个来源键 `auth.SourceKey`（IPv4 按地址、IPv6 按 /64；§5.3 由控制端改写）：
  - 归一化放在 `auth`，紧挨 `ClientIP`，依赖方向 `ingest`、`api` → `ratelimit` → `auth`（§3.1 的 ratelimit 一行写的是"来源键经 auth.ClientIP"，实际还经 `auth.SourceKey`）。
  - 锁定的报错改为 "too many failed logins from this source (one IPv4 address, or one IPv6 /64)"。
  - `Register` 日志里的 `from` 是来源键，IPv6 时不再是具体地址；登录日志的 `from` 仍是具体地址。
- §10 的点文件规则，计划另定了两点：
  - 判定只看请求路径里各段的名字，符号链接按链接自己的名字算。名字不带点、指向点文件的链接照常服务，那是运维的显式动作。
  - `.well-known/` 也不服务，需要它的（ACME 校验、security.txt）由反向代理提供。嵌入产物里的 `.gitkeep` 同样不再被服务。
- §10 限流写"约 20 个并发访客即触发 429"。按常量算，每个打开的公开页（总览或节点页，一个标签页算一个）每 2 秒轮询一次，超过 20 个页面才持续多于每秒 10 次的补充，之后 60 的余量耗尽才开始 429。README 按这个写法（30 个页面时约 12 秒）。
- §10 第一条的"构建产物按描述符前缀核对"，在计划里是 Task 10 Step 6 构建之后的一次 grep 核对，不是常驻测试。
