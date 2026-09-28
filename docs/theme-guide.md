# 公开页主题开发指南

主题是一个只调 `PublicService` 的静态前端：框架自选，构建产物打成 zip，在管理面板的「主题」页上传、启用。hub 把产物存进库，在一个单独的主机名（主题 origin）的根路径上托管；hub 只接收产物，不执行任何构建。

设计依据见[架构设计](superpowers/specs/2026-09-17-probe-architecture-design.md) §10.1。

## 部署：主题 origin

主题功能要求 hub 以 `--theme-origin` 启动，并把第二个主机名也指向 hub：

```sh
probe-hub serve --db /var/lib/probe/probe.db --theme-origin https://status.example.com
```

- **主机名必须与面板不同。** 主题是第三方代码；与面板同源的主题脚本能直接调 `AdminService`，浏览器会自动带上来访管理员的会话 cookie。换了主机名之后，主题与面板之间靠哪些事实隔开，见下面的「主题与面板的隔离」。
- **只差端口不算另一个主机名。** cookie 不按端口隔离；hub 按主机名分流时也忽略端口，于是面板那个主机名的请求会全部被分到主题 origin，而那里没有面板。hub 不知道面板用的是哪个主机名，这种配置启动时查不出来，只能靠部署的人避开。
- **反代必须原样转发 `Host`。** hub 按请求的 `Host` 判定：去掉端口、不分大小写、去掉一个结尾的点、IP 地址按规范写法（`[0:0::1]` 即 `[::1]`）比较，等于 `--theme-origin` 的主机名走主题 origin，其余走面板与内置公开页。`--theme-origin` 里的国际化域名按浏览器发送的 punycode 写法比较（`https://状态.example` 即 `xn--t7t692b.example`），浏览器不会原样发出的写法（带 zone 的 IPv6 地址、`127.1` 这类非四段十进制的 IPv4 写法）启动时就被拒绝。Caddy 默认转发原始 `Host`；nginx 要写 `proxy_set_header Host $host;`，否则 hub 看到的是 upstream 的地址，两个主机名都会落到面板那一边。
- 没有配 `--theme-origin` 时主题功能整体关闭：面板的「主题」页给出说明，上传、列出、启用、删除、预览五个方法一律 `FailedPrecondition`。
- **公开页总闸同样管主题 origin。** 在面板的「外观」页关掉公开页之后，主题 origin 上的 `PublicService` 全部 `NotFound`，页面路径（包括主题包里的文件路径）返回"公开页已关闭"的说明页，`assets/` 下 404，主题文件一个都不服务。重新打开即恢复，启用中的主题不变。

主题 origin 上只有两样东西：

| 路径 | 内容 |
|---|---|
| `/probe.v1.PublicService/<方法>` | 公开接口，与面板所在 origin 上的同一个挂载点（同一套限流与缓存） |
| `/probe.v1.AdminService/…`、`/probe.v1.AgentService/…`、`/admin`、`/admin/…` | 404 |
| 其余路径 | 启用中主题的文件；没有启用中的主题时是内置公开页 |

RPC 路径优先于主题文件：包里即使有 `admin/index.html` 或 `probe.v1.PublicService/GetSite` 这样的文件，也遮蔽不了上表的前两行。`--public-dir` 只接管面板所在 origin 的公开页，与主题 origin 无关；两者同时存在时面板会标明这一点。主题调 `PublicService` 用相对路径，请求发往主题 origin 自己。

### 主题与面板的隔离

主题 origin 与面板是两个 origin。下面几条各自成立、各自可验，不是其中某一条单独承担隔离：

- **主题 origin 不挂 `AdminService`。** 主题脚本对自己 origin 的 `/probe.v1.AdminService/…` 发同源请求得到 404，带着有效的会话 cookie 也一样。这由挂载承载，不靠约定。
- **跨源的 JSON 请求要先过预检，hub 对任何 origin 都不下发 CORS 允许头。** 这是安全约束，不是"主题用不着跨源"的便利说明：预检的应答不许可主题 origin，浏览器就不发出实际请求；hub 一旦许可，主题脚本就能带着来访管理员的 cookie 把写请求发到面板，副作用在服务端已经发生，读不读得到响应无关紧要。
- **不需要预检的简单请求被拒绝。** 跨源的简单请求只能用 `text/plain`、`application/x-www-form-urlencoded`、`multipart/form-data`，connect 对这三种类型回 415；`AdminService` 不接受 GET（405）。
- **会话 cookie 是 host-only。** 它不设 `Domain`，浏览器只把它发给签发它的那个主机名，主题 origin 上的请求不带面板的会话。
- **同名的会话 cookie 有多个值时，任一有效即通过。** 兄弟主机能写 `Domain` 为父域的同名 `probe_session`。它与管理员的 host-only 会话是两个 cookie，路径匹配时浏览器把两者一起发给面板。排在前面的未必是管理员那个：RFC 6265 §5.4 建议（SHOULD，并注明不是所有浏览器都如此）把 `Path` 更长的排在前面、`Path` 同长时先建的在前；Chromium 实测带更长 `Path` 的伪造值排在最前，`Path` 同为 `/` 时，管理员重新登录后伪造值也排到前面。hub 不看顺序，对同名 cookie 的每个值逐个校验，任一有效即通过，多出来的无效值不让有效值失效，所以兄弟主机写入的同名 cookie 锁不住面板。
- **`SameSite=Strict` 对兄弟子域不起隔离作用。** `panel.example.com` 与 `status.example.com` 同属一个注册域名，浏览器判定为同站，`SameSite` 不拦它们之间的请求；两者之间的隔离靠的是上面几条。

## `PublicService` 契约

权威定义是 `proto/probe/v1/public.proto`（请求与响应类型另见同目录的 `query.proto`、`types.proto`），注释写明了每个字段的含义。

| 方法 | 请求 | 返回 | `cache_max_age_s` |
|---|---|---|---|
| `GetSite` | `{}` | `PublicSite`：标题、明暗（`auto`/`light`/`dark`）、主色、logo（`data:` URL）、自定义 CSS | 300 |
| `GetSnapshot` | `{}` | `PublicSnapshot`：`now`、`reportIntervalMs`、全部公开节点的实时状态 | 1 |
| `QueryMetrics` | `{"nodeId": "3", "from": "…", "to": "…", "maxPoints": 720}` | 指标历史 | 60 |
| `QueryProbes` | 同上 | 探测历史 | 60 |

- **调用方式。** Connect unary 就是 HTTP POST + JSON：

  ```js
  const res = await fetch("/probe.v1.PublicService/GetSnapshot", {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: "{}",
  });
  const snapshot = await res.json(); // 顶层就是 PublicSnapshot
  ```

  四个方法都无副作用，也可以用 GET：`/probe.v1.PublicService/GetSnapshot?connect=v1&encoding=json&message=%7B%7D`（`message` 是 URL 编码的 JSON 请求）。GET 带请求体会被拒绝（415）。
- **JSON 形态。** 字段名是 lowerCamelCase；`int64` 字段（节点 id、`now`、`lastSeenAt`、`from`、`to` 等）在 JSON 里是字符串；没有显式存在性的字段取默认值（`false`、`0`、空串、空列表）时不出现在响应里，读取时按默认值补齐；proto 里标了 `optional` 的字段不出现表示没有这个值（例如从未上报过的节点没有 `lastSeenAt`）。
- **缓存。** GET 的成功响应带 `Cache-Control: max-age=<cache_max_age_s>`，失败响应带 `no-store`；POST 响应不带缓存头。`GetSnapshot` 的结果 hub 另缓存 1 秒，轮询间隔短于 1 秒没有意义；按 `reportIntervalMs` 或 2 秒轮询即可。主题若只在加载时取一次 `GetSite`，已打开的页面刷新后才看到外观改动；用 GET 取时，刷新后浏览器还可能再用最多 5 分钟的缓存。
- **限流。** 按来源计：IPv4 一个地址、IPv6 一个 /64 算一个来源，桶容量 60、每秒补充 10，超出得到 HTTP 429（Connect 错误码 `resource_exhausted`，带 `no-store`）。hub 在反代之后而没有配 `--trusted-proxies` 时，所有访客共用反代地址的一个桶；量级估算见 README 的反代一节。
- **可见范围。** 只有标为公开的节点出现在快照里；对未公开的节点与不存在的节点，`QueryMetrics`、`QueryProbes` 返回同一个 `not_found`。
- **错误。** 失败时响应体是 `{"code": "...", "message": "..."}`，`message` 供人阅读。

主题不声明配置项：外观由 `GetSite` 下发，主题自己决定用不用。

## 包布局

包是一个 zip，**包根**（不是某个子目录）必须有 `index.html` 与 `theme.json`：

```
theme.zip
├── theme.json
├── index.html
├── preview.png          可选，theme.json 的 preview 指向它
└── assets/
    ├── index-3f9a1c.js
    └── index-b27e4d.css
```

托管规则与 `--public-dir` 相同：

- 请求路径命中包里的文件就返回它；`/` 是 `index.html`。
- `assets/` 下未命中返回 404——用 HTML 回应 `<script>` 会被浏览器按 MIME 拒绝，404 让缺失可见。
- 其余未命中的路径回落到 `index.html`，交给前端路由；因此节点页这类深链接可以直接分享。
- 主题在主题 origin 的根路径上托管：Vite 的 `base` 保持默认的 `/`。
- 响应头：`X-Content-Type-Options: nosniff`、`Content-Security-Policy: frame-ancestors 'none'`（主题页不能被嵌进 iframe）、`Cache-Control: no-cache`（每次都向 hub 重新验证，文件名带不带哈希都一样）；不限制脚本与外部资源，字体与图片可以从别的站点加载。内容类型按扩展名决定。

打包时进入产物目录再压缩，让文件落在包根：

```sh
cd dist && zip -r -X ../theme.zip . && cd ..
```

macOS 访达的「压缩」会加入 `__MACOSX/._*` 条目，它们以 `.` 开头，整包会被拒绝；用上面的命令行打包。

## 清单 `theme.json`

一个 JSON 对象，只允许这些字段（出现别的字段整包拒绝）：

| 字段 | 必填 | 约束 |
|---|---|---|
| `id` | 是 | `[a-z0-9-]{1,32}`；不得是 `builtin`（内置公开页的标识，主题不得自报内置） |
| `name` | 是 | 去掉首尾空白后非空，至多 64 个字符，不含控制字符 |
| `version` | 是 | 同 `name` |
| `preview` | 否 | 包内 `.png`、`.jpg`、`.jpeg` 或 `.webp` 文件的路径，内容必须真是该类型的图片；面板的主题列表显示它 |

```json
{ "id": "dark-cards", "name": "Dark cards", "version": "1.0.0", "preview": "preview.png" }
```

`id` 是主题的身份：再次上传同一 `id` 即整体替换（不留旧版本，也不能回滚），启用状态沿用。更新已装的主题时在面板上选「更新 <主题>」（接口上是 `UploadTheme` 的 `expect_id`）：包的 `id` 必须与之一致、且该主题必须已安装，否则拒绝——包装错了主题时不会静默装出第二个主题或覆盖无关的那个。

## 上限

| 项 | 上限 |
|---|---|
| zip 包本身 | 8 MiB |
| 条目数 | 2000 |
| 单个文件展开后 | 16 MiB |
| 全部文件展开后合计 | 64 MiB |
| 已安装主题数 | 20（替换同一 `id` 不计入） |
| 上传耗时 | 30 秒 |

- 展开大小在读取任何条目之前按 zip 中央目录声明的大小判一次，实际展开时逐条再核对一次；任一超出整包拒绝。压缩方式只接受 store 与 deflate。
- 30 秒是 hub 读取一个请求的时限（`http.Server` 的 `ReadTimeout`），整个上传请求必须在这之内传完。面板用 JSON 上传，包以 base64 编码，8 MiB 的包约 11 MB，需要约 3 Mbit/s 的上行；上行更慢时把包做小，或在离 hub 更近的网络里上传。

## 被拒绝的内容

以下任一出现即拒绝整包，什么都不写入；错误信息写明是哪个条目或哪个字段、违反了什么：

- **条目类型**：只接受普通文件与目录。符号链接、硬链接、设备节点、FIFO 一律拒绝——路径检查看的是条目名，看不见链接指向哪里；跳过而不拒绝会让"装上了"与"装对了"分不开。
- **条目路径**：必须相对包根、以 `/` 分隔、规范化后仍在包内。含 `..`、绝对路径、反斜杠、空段、控制字符，或任一段以 `.` 开头（`.env`、`.git/`、`.well-known/`、`__MACOSX/._x`）都拒绝；同一路径不得出现两次。
- **清单**：缺 `index.html` 或 `theme.json`，清单不是合法 JSON、有未知字段、字段不合上表的约束，`preview` 指向的文件不存在或内容不是声明的图片类型。

## 启用、停用与删除

- 至多一个主题处于启用状态；启用一个即停用其余。面板上的「停用」让主题 origin 回到内置公开页（接口上是 `EnableTheme` 传空 `id`）。
- 删除启用中的主题后，主题 origin 回落内置公开页，不会变成 404。
- 整包在一个事务里写入，提交前访客看不到新包；访客的每个请求读到的都是某一个完整的包。
- 主题的管理方法只接受管理员会话，API token 调不了。

## 本地开发

在本机起一个 hub，让主题 origin 用 `localhost`、面板用 `127.0.0.1`——两个不同的主机名指向同一个 hub：

```sh
probe-hub passwd --db dev.db
probe-hub serve --db dev.db --listen 127.0.0.1:18180 --theme-origin http://localhost:18180
```

- 面板：`http://127.0.0.1:18180/admin/`；主题 origin：`http://localhost:18180/`（没有启用主题时是内置公开页）。
- 开发时用框架自己的开发服务器，把 `/probe.v1.PublicService/` 代理到 hub。以 Vite 为例：

  ```js
  // vite.config.js
  export default { server: { proxy: { "/probe.v1.PublicService": "http://localhost:18180" } } };
  ```

  `PublicService` 在两个主机名上都有，代理到哪一个都行。
- 要有数据可看，先在面板上建节点并把它标为公开，再在另一台机器（或本机）上装 agent 指向这个 hub。
- 成品验证：`vite build`，按上文打包，在面板上传并启用，打开 `http://localhost:18180/` 与一个深链接（如 `/nodes/1`），确认资源路径与前端路由在根路径下都对。
