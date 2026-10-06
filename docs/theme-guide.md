# 公开页主题开发指南

主题是运行在浏览器中的静态前端。管理员在 `/admin/` 的「主题」页安装、预览和启用，访客在同一域名的 `/` 查看；切换不改域名、不重启 hub。hub 只接收构建产物，不执行主题仓库里的构建脚本。

## 安装与升级

在后台填写 `owner/repo`、GitHub 仓库 HTTPS 地址或指定 Release 地址，选择已发布 Release 的 ZIP 资产安装。也可以上传本地 ZIP。只支持公开仓库，不接受 GitHub 自动生成的源码压缩包；主题作者必须发布已经构建的产物。

GitHub 是安装来源，不是运行时依赖。下载完成并校验后，原包与文件存入 hub 数据库；GitHub 不可用不会影响已启用版本。安装新版本不会自动启用，也不会覆盖当前版本。主题列表保存仓库、Release、资产名称和 SHA256，更新指定主题时包内 `id` 必须与目标一致。

- `id` 标识主题，原始 ZIP 的 SHA256 标识不可变安装产物；清单 `version` 只是展示文字。
- 全站保存当前版本与上一个版本。启用后可以切回上一个版本，也可以切回内置公开页。
- 最多安装 20 个主题，每个主题最多保留 3 个版本。超限时须显式清理，不自动挤掉当前版本或回滚版本。
- 单版本清理不能删除当前或回滚版本；删除整个主题会清除它的选择引用，若它正在启用则回到内置公开页。
- 预览不改变全站选择。未发布版本只通过绑定管理员会话的短期预览地址读取，不能把预览地址当作公开分享链接。
- 页面和资源 URL 固定同一摘要。切换后，已经打开的页面仍读取原版本的资源；显式清理该版本后，它的资源不再提供。
- 主题管理需要管理员会话，API token 不可调用。

`--public-dir` 是运维手动提供的公开页替代目录，不属于上传主题。使用它时公开页由该目录接管，后台会阻止启用第三方主题，须先移除该参数并重启。

## 同域名与隔离

主题页面由 hub 自己的容器加载到 `sandbox="allow-scripts"` iframe 中，不授予 `allow-same-origin`。地址栏仍是公开站点域名，但主题脚本的来源是不透明来源，不具有管理面板的同源权限。

主题 HTML、JS、错误响应和缓存验证响应都带沙箱 CSP；直接打开包内 HTML 也不能恢复同源权限。主题不能读取父文档、会话 cookie 或管理存储，不能注册 Service Worker，也不能直接使用 WebAuthn。页面不开放任意网络代理：公开数据经 SDK 的消息通道请求，由可信容器只调用六个固定的只读公开方法，请求不带管理员凭据。

主题可以加载脚本、样式、字体和图片，但直接 `fetch`、XHR、WebSocket 受 CSP 限制。第三方脚本仍是主题作者供应链的一部分；沙箱隔离权限，不替作者担保页面内容。

公开页总闸同时约束内置页、主题容器、主题资源和公开数据接口。关闭公开页不影响 `/admin/`；重新打开后保留原主题选择。

### 旧部署迁移

新版本移除 `--theme-origin`。升级前从 systemd 主单元、Docker 参数或其他启动配置删除它；安装器发现旧参数会在停服前明确报错，不静默更改路由。公开页和后台现在共用同一域名，无须另配主题域名。

旧主题域名若继续指向 hub，也会提供管理入口，不再隔离成主题专用主机。升级时同步撤销旧域名的反代路由或重定向到保留的域名。HTTPS 反代必须保留访问 Host、发送正确的 `X-Forwarded-Proto`，并在 hub 配置实际代理的 `--trusted-proxies`，否则包括登录在内的管理请求会被同源检查拒绝。

未声明 SDK 或 SDK 不兼容的旧包仍保留原包与元数据，并可备份、恢复，但不能预览或启用。原启用包不兼容时回到内置页。旧包必须适配 SDK、重新构建并安装，不能只修改清单数字而保留直接调用 API 的代码。

## 主题 SDK

清单声明 `"sdk": 1`，通过 hub 提供的模块获取公开数据和路由：

```html
<!doctype html>
<html lang="zh">
<meta charset="utf-8">
<meta name="viewport" content="width=device-width,initial-scale=1">
<title>主题示例</title>
<h1 id="title"></h1>
<pre id="nodes"></pre>
<script type="module">
import { getSite, getSnapshot, queryMetrics, queryProbes, navigate, onRoute } from '/_heron/theme-sdk.js';

const site = await getSite();
document.querySelector('#title').textContent = site.title || 'Heron';
document.querySelector('#nodes').textContent = JSON.stringify(await getSnapshot(), null, 2);
await onRoute(path => {
  // 路由由可信容器同步，主题不直接修改父页面地址。
  console.log(path);
});
</script>
</html>
```

| 方法 | 请求 | 返回 |
|---|---|---|
| `getSite()` | 无 | `PublicSite`：标题、明暗、主色、logo、自定义 CSS；不含管理入口 |
| `getSnapshot()` | 无 | `PublicSnapshot`：时间、建议上报间隔、公开节点实时状态 |
| `queryMetrics(args)` | `{ nodeId, from, to, maxPoints }` | 节点指标历史 |
| `queryProbes(args)` | `{ nodeId, from, to, maxPoints }` | 节点探测历史 |
| `listProbeComparisonNodes(args)` | `{ taskId }` | 跨节点对比的候选与分块上限，见下文 |
| `queryProbeComparison(args)` | `{ taskId, nodeIds, from, to, maxPoints }` | 一个任务在一组节点上的探测历史 |
| `navigate(path, replace?)` | `/`、`/nodes/<正整数>` 或 `/probes/<正整数>`，可带尾部斜杠；可选替换历史记录 | 更新公开页地址并通知路由订阅者 |
| `onRoute(listener)` | 接收路径的函数 | Promise，解析为取消订阅函数；订阅后立即收到当前路径 |

数据字段定义见 `proto/heron/v1/public.proto`、`query.proto` 和 `types.proto`。JSON 使用 lowerCamelCase；`int64` 字段是字符串；普通字段缺席时使用 protobuf 默认值，`optional` 字段缺席表示没有读数。SDK 返回的是响应数据本身，不是 `Response` 对象，失败会拒绝 Promise。

只公开标为公开的节点。查询私有节点和不存在的节点都得到 `not_found`。公开接口沿用来源限流；可信容器另限制消息大小、请求并发与频率，不支持任意 URL 或方法名。快照按约 2 秒轮询即可，不要高频重试。

历史网络均值从 `rx_bytes` / `tx_bytes` 的 `sum / stepS` 计算，采样峰值取 `net_rx_bps` / `net_tx_bps` 的 `max`，单位 bytes/s。缺少系列、`n=0` 或缺少值时保留空洞，有效零值正常显示。探测系列沿用服务端顺序，不在主题内重新按 ID 排序。

历史响应的 `level` 是窗口选定的基础聚合级别，较新的区间可由更细数据按同一 `stepS` 聚合补齐；画点间隔以 `stepS` 为准。末桶可能未完成，只含已刷出的分钟，不含 live 内存里尚未刷出的当前分钟。

### 跨节点探测对比

一次对比是两步，都经 SDK 走 POST。主题桥接不发 GET，所以公开方法上的 `cache_max_age_s = 60` 不会作用在这条路径上；浏览器的 GET 缓存只覆盖直接 GET 调用。

1. `listProbeComparisonNodes({ taskId })` 取当前分配了该任务的公开节点。`nodeIds` 已按节点全序排好，顺序只决定显示。`maxNodesPerQuery` 是下一步一次能带的节点数，与 hub 校验用的是同一个值，恒为正。收到 `0`（proto3 数值缺席也是 0）是协议错误：不能当成不限，也不能改用主题自己写死的块大小。
2. 按 `maxNodesPerQuery` 把 `nodeIds` 切成块，再调用 `queryProbeComparison({ taskId, nodeIds, from, to, maxPoints })`。同时在飞的块不要超过两块。某一块网络失败或服务端报错时显示错误，让用户重试，不要自动循环。窗口或任务变了就放弃尚未返回的块。

`series` 与这一块请求的 `nodeIds` 同序；窗口内没有样本的节点也在，`samples` 为空。`unavailableNodeIds` 是不可见或不存在的节点，二者不加区分，不要为它们画线。分块不返回任务标注，种类与目标用 List 响应的 `kind`、`target`；List 之后目标、间隔、超时、DNS 或分配变了，下一次 List 才体现，没有“整次重来”。

List 的错误码 `not_found`：任务不存在，或没有任何分配了该任务的公开节点。两种情况是同一条错误，不要设法区分。`failed_precondition`：这次要读的历史超过额度，`message` 里带有额度和建议（缩小窗口、增大 `maxPoints`，或等待数据整理）。空的 `samples` 表示窗口内没有结果，不是数值 0。丢包率看样本里的 `lost / sent`：全部超时是 100%，全部本地错误（`errors`，探测没有发出）是 0%，不要因为没有 RTT 就画成 100%。RTT 只在样本带了 `rttMeanUs` 时才有。

`int64` 在 JSON 里是字符串，包括 `taskId` 和 `nodeIds`。

QueryMetrics 的 coverage 与 ts 一一对应：minutes 是留存的上报分钟，observed 是 hub 观测分钟，observedReported 是交集，各自可缺席。纯观测点所有指标 n=0，按空值绘图。coverageSummary 是请求窗口汇总，不受 maxPoints 影响；未知分钟 = eligibleMinutes−observedMinutes，覆盖率 = observedReportedMinutes/observedMinutes，不是在线率。coverageStart 缺席表示尚无覆盖记录，有起点但 observedMinutes=0 表示无可观测区间。内置公开页不展示覆盖率，但公开节点的 observed 暴露 hub 在保留期内的观测分钟。

## 包布局与清单

ZIP 包根必须有 `index.html` 和 `theme.json`，不能再套一层仓库或 `dist` 目录。其余资源例如 `assets/app.js`、`assets/app.css`、`preview.png` 使用包内相对路径。

```json
{
  "id": "dark-cards",
  "name": "Dark cards",
  "version": "1.0.0",
  "sdk": 1,
  "preview": "preview.png"
}
```

| 字段 | 约束 |
|---|---|
| `id` | 必填，`[a-z0-9-]{1,32}`，不得是 `builtin` |
| `name`、`version` | 必填，去除首尾空白后非空，最多 64 字符，无控制字符 |
| `sdk` | 新安装必须为 `1`；缺席或不支持的值只可用于旧包归档恢复 |
| `preview` | 可选，包内 PNG/JPEG/WebP 路径；内容必须与扩展名匹配 |

未知清单字段会拒绝。外观数据由 `getSite()` 提供，不支持自定义清单配置项。

包内资源用 `./assets/app.js` 等相对地址；Vite 构建应配置 `base: './'`。不要将包资源写成 `/assets/...`，也不要通过 `<base>` 改写基址。主题文档实际位于带摘要的资源路径，公开深链接由可信容器通过 `onRoute` 传入，不能把 iframe 的 `location.pathname` 当作站点路由。

```sh
cd dist && zip -r -X ../theme.zip .
```

macOS Finder 的“压缩”可能加入 `__MACOSX/._*`，这些隐藏条目会被拒绝。

## 包限制

ZIP 最多 8 MiB，最多 2000 条目；单文件展开后最多 16 MiB，总展开大小最多 64 MiB。只接受 store、deflate 压缩以及普通文件和目录。解析器在读文件前检查声明上限，再核对实际展开大小和 CRC。

绝对路径、`..`、反斜杠、空路径段、控制字符、以 `.` 开头的路径段、重复路径、链接和设备文件都会拒绝整包。安装失败不影响当前主题，也不留下部分版本。

上传经过一个 JSON 请求，原包以 base64 编码；8 MiB ZIP 的请求约 11 MB，受服务端 30 秒读请求超时约束。GitHub 下载也有独立超时与压缩包大小限制。

## 备份与恢复

配置快照保存全部保留版本、当前与回滚引用，以及 `snapshot_theme(theme_id,digest,sha256)` 原包清单。原包先上传到 `<前缀>/theme/sha256/<SHA256>.zip`，成功后才发布引用它的配置快照。更新、清理或卸载本地主题不删除历史快照引用的远端原包；远端主题对象目前不自动回收。

```sh
sqlite3 config.db 'SELECT theme_id,digest,sha256 FROM snapshot_theme ORDER BY theme_id,digest;'
heron-hub restore --db hub.db --config config.db --themes ./theme-packages --yes
```

恢复前停止 hub。目录内使用 `<SHA256>.zip`，必须匹配快照摘要和包内 ID，不从 GitHub 重新下载。指定目录时，快照要求的原包缺失或校验失败会拒绝恢复；空 `sha256` 表示快照本来就没有该原包，保留缺包元数据。省略 `--themes` 会清空主题文件、原包与选择，保留元数据，管理员重新上传原包后再启用。

旧 schema 快照在私有副本中迁移，不改原文件。没有摘要清单的历史格式仍可读取 `<主题 id>.zip`；旧 SDK 包恢复后仍不可执行。有原包时恢复清单元数据和文件，无原包时不沿用目标库旧内容。恢复输出记录已恢复、缺包与忽略的主题。

主题内容写入或删除会唤醒配置备份，周期同步补偿丢失的唤醒。备份状态列出缺原包的主题，但不把它当作对象存储故障。密集安装会多占用配置快照保留名额；原包和快照包含站点配置，应放在私有存储中。

## 本地开发

```sh
heron-hub serve --db dev.db --listen 127.0.0.1:18180
heron-hub passwd --db dev.db
```

访问 `http://127.0.0.1:18180/admin/`，创建公开节点并接入 agent。将产物上传后先预览，再启用并访问 `http://127.0.0.1:18180/`。SDK 依赖可信容器的消息通道，直接用框架开发服务器打开主题不具备这条通道；成品验证应通过 hub 预览，检查相对资源、`/nodes/1` 与 `/probes/1` 深链接、前进后退、切换版本和公开页关闭行为。
