# 服务器监控探针：架构设计

日期：2026-09-17

## 1. 目标与非目标

**目标**：一个单一职责的自托管服务器监控探针。agent 采集主机指标并上报，hub 存储并展示。

范围内：

- 主机指标采集（CPU、内存、交换、磁盘、网络、负载、连接数、进程数）与实时 / 历史图表
- 延迟探测：hub 下发目标，agent 执行并回报
- 告警通知：节点离线 / 恢复、探测异常、节点到期
- 公开状态页（匿名可访问）
- 流量统计：总量与按重置日滚动的周期用量
- 节点费用与到期：价格、币种、周期、到期日与自动续期的展示值，以及到期提醒（§9.4）
- 公开页主题：经 `AdminService` 上传、启用、删除主题包；主题是只调 `PublicService` 的静态前端工程，由 hub 在独立 origin 下托管，并随功能提供开发指南
- 节点的国家 / 地区徽章：由运维显式开启的查询服务按节点来源地址得出，或由管理员手动指定；默认不出网

已确认要做、但尚未在本文成形的功能点记在仓库根的 `FEATURES.md`；其中某条进入里程碑时，设计并入本文并从那里删除。

**非目标**（明确排除，新增功能前先对照此表）：

| 排除项 | 原因 |
|---|---|
| 远程终端、命令执行、文件管理 | 会让 hub 成为对全部节点的远程代码执行入口；hub 失守即全部节点失守 |
| agent 自动更新、hub 托管 agent 二进制 | 本质是"hub 可向全部节点推送代码"，与上一条同类 |
| 插件系统 | 第三方代码在 hub 进程内执行，与远程执行同类。主题的代码跑在访客浏览器里，不是同一件事 |
| 主题市场：hub 出网拉取远程目录并自动安装 | 让 hub 携带信任去访问代码分发点。主题包由管理员上传，不由 hub 去取。国家查询（`FEATURES.md`）也是 hub 出网，但取回的是两个字母的数据、默认关闭、由运维显式开启，不是同一件事 |
| OAuth、多用户 | 单管理员足够；支持 TOTP 与无密码 Passkey |
| 多数据库方言、外接时序库 | 目标规模内单文件 SQLite 足够 |
| 流量用量告警 | 未列入需求；内存和磁盘持续紧张告警已纳入 |
| mTLS | 见 §5.5 |
| hub 自行终止 TLS | 见 §5.4 |

**目标规模**：上百到数百节点，历史保留数月。

## 2. 技术选型

| 项 | 选择 | 理由 |
|---|---|---|
| 语言 | hub 与 agent 都用 Go，单 module | 协议类型编译期共享；`CGO_ENABLED=0` 即可交叉编译到冷门架构 |
| agent 平台 | Linux、macOS | Linux 侧直读 `/proc` 与 `/sys`，采集层零第三方依赖；darwin 侧以 build tag 隔离，其依赖不链入 Linux 二进制 |
| 通信 | ConnectRPC，agent 侧仅 unary | 见 §4.1 |
| 契约 | protobuf，`proto/` 为单一事实源 | 同时生成 Go 与 TS；字段号使改名安全；`optional` 区分"无读数"与"读数为 0" |
| 存储 | 单文件 SQLite，纯 Go 驱动 `modernc.org/sqlite`，WAL | 保持无 CGO、单二进制单文件部署 |
| 前端 | React + Vite + TS，uPlot 画图，产物 `go:embed` 进 hub | Connect 官方查询层现成；uPlot 专做时序、体积小 |

## 3. 架构

### 3.1 仓库布局

```
proto/probe/v1/       agent.proto / admin.proto / public.proto / query.proto（管理与公开共用的历史查询类型）/ types.proto / access.proto / cache.proto（cache_max_age_s 方法选项）
proto/SKILL.md        agent 入口卡片；与 proto 源文件一同由 proto/embed.go 嵌入 hub（§5.6）
gen/                  buf 生成的 Go 代码，入库
cmd/hub/  cmd/agent/
internal/hub/
  metric/    指标描述表与分钟桶；纯数据的叶子包，live 的折叠与 store 的 SQL 都自它生成
  live/      内存中的节点实时状态与未落盘的分钟桶
  ingest/    AgentService 实现：校验 → live、traffic、探测桶；分钟刷出与有界的待重试列表
  traffic/   流量差分与周期累计
  store/     SQLite：schema、迁移、写协程、上卷、prune、按窗口选级查询
  probe/     探测任务、分配与版本号
  alert/     巡检、状态机、通知渠道与投递队列
  auth/      管理员会话、API token、节点 token、注册窗口、可信代理
  ratelimit/ 按来源的令牌桶与挂载点中间件（来源键经 auth.ClientIP 与 auth.SourceKey，IPv6 按 /64）；Register 与 PublicService 共用
  api/       AdminService / PublicService 实现
  web/       嵌入的面板与公开页两份产物 + 静态目录替换，三者共用只服务普通文件的核心
internal/agent/
  collect/   host.go（Host 接口）/ procfs.go / darwinraw.go / platform_{linux,darwin,other}.go
  prober/    icmp / tcp 探测执行与调度
  client/    上报循环、退避、配置文件
internal/clock/       可注入的单调钟与墙钟
web/                  React 工程（admin 与 public 两个入口）
deploy/               install.sh、systemd 单元、OpenRC 服务脚本（§14）
```

依赖方向：`live → metric`；`store → metric`；`auth → store`；`ingest → live, store, auth, traffic, probe`；`alert → live, store`；`api → live, store, probe, alert, auth`。`metric` 与 `clock` 不依赖任何 hub 包：描述表必须同时被 live（折叠）与 store（SQL）看到，而 live 不能依赖 store，所以它只能是二者之下的叶子。

### 3.2 三个服务，三种鉴权

| 服务 | 调用方 | 鉴权 |
|---|---|---|
| `probe.v1.AgentService` | agent | 节点 bearer token（`Register` 用注册窗口 key） |
| `probe.v1.AdminService` | 管理面板；agent 与脚本 | 会话 cookie；标为只读的方法另接受 API token（§5.6） |
| `probe.v1.PublicService` | 公开页、第三方主题 | 无，按来源键限流（§5.3） |
| 主题 origin（§10.1） | 访客浏览器 | 只挂 `PublicService` 与主题静态文件；`AdminService`、`AgentService` 不在此 origin 上 |

鉴权由"服务挂载时绑定的拦截器"承载，不在方法内逐个检查：新增方法无法漏掉鉴权，因为不存在未绑定拦截器的挂载点。

`AdminService` 的每个方法用 `probe.v1.access` 选项声明准入口径：`ACCESS_LOGIN`（仅 `Login`，凭据是请求体里的密码）、`ACCESS_READ`（会话或 API token：无副作用，不列出或管理凭据，也不回显可能含密钥的内容——配置本身，或外部接收方对它的回显）、`ACCESS_SESSION`（仅会话：有副作用的方法；凭据管理——包括只读的 `ListApiTokens`，自动化进程没有理由知道还有哪些 token 存在；回显可能含密钥的配置的方法——`ListNotifyChannels` 会回显 webhook 的请求体模板，模板里可能放着密钥，而 agent 不需要通知渠道的配置；`GetAlertDeliveryError` 返回投递失败的原文，接收方可能在错误响应里回显收到的请求体）。拦截器在构造时从生成的描述符读出整张表，任一方法未声明即 panic：未声明的方法无法随 hub 启动，因而不存在"漏标时默认放行还是默认拒绝"的取舍。准入口径与方法定义写在同一处，proto 仍是单一事实源。它与 `idempotency_level` 是两件事：后者决定是否接受 GET，`AdminService` 一律不标（§3.3）。

公开数据使用独立的消息类型（`PublicNode`、`PublicSnapshot`），不对 `Node` 做字段过滤。由此默认方向是"私有"：给 `Node` 加字段不会出现在公开页，必须显式加入 `Public*` 消息才公开。

### 3.3 方法清单

- `AgentService`：`Register`、`Report`。
- GET 准入由装配期的显式检查承载：`probe.v1` 的每个方法接受 GET 当且仅当声明了正的 `cache_max_age_s`，遍历包内全部服务，任一方向不符 hub 起不来。
- `AdminService`：`Login`、`Logout`、`ListSessions`、`RevokeSession`；节点 `ListNodes`、`CreateNode`、`UpdateNode`、`DeleteNode`、`RotateNodeToken`、`ReorderNodes`；注册窗口 `OpenRegisterWindow`、`CloseRegisterWindow`、`GetRegisterWindow`；数据 `GetSnapshot`、`QueryMetrics`、`QueryProbes`、`GetTraffic`、`AdjustTraffic`；探测 `ListProbeTasks`、`SaveProbeTask`、`DeleteProbeTask`；告警 `ListAlertRules`、`SaveAlertRule`、`DeleteAlertRule`、`ListAlertEvents`、`GetAlertDeliveryError`、`ListNotifyChannels`、`SaveNotifyChannel`、`DeleteNotifyChannel`、`TestNotifyChannel`；设置 `GetSettings`、`UpdateSettings`、`GetStorageStats`；标签 `ListTags`（`ACCESS_READ`）、`DeleteTag`；主题 `UploadTheme`、`ListThemes`、`EnableTheme`、`DeleteTheme`、`GetThemePreview`（§10.1，全部仅会话）；API token `ListApiTokens`、`CreateApiToken`、`DeleteApiToken`；自描述 `GetApiReference`。
- `PublicService`：`GetSite`、`GetSnapshot`、`QueryMetrics`、`QueryProbes`。后两者只对 `public = true` 的节点应答，对其余节点与不存在的节点返回同一个 `NotFound`。

无副作用标注（`idempotency_level = NO_SIDE_EFFECTS`，决定方法是否接受 GET）按服务的信任模型决定，不按读写决定：`PublicService` 的四个方法全部标注，因而可用 GET 调用并带 `Cache-Control`——它无鉴权（§3.2），不存在会被浏览器环境性携带的凭据，§5.3 的 CSRF 论证在这里不成立，而公开页恰是需要被缓存的那一面。缓存上界分别定：实时快照不超过一个上报间隔（更短无意义，更长会展示过期的在线状态），历史查询可更长，站点配置最长。`AdminService` 与 `AgentService` 一律不标：前者是 §5.3 的 CSRF 防线之一，后者的上报本就有副作用。

## 4. agent 协议

### 4.1 为什么是 unary 而不是长连接

hub → agent 的下行只有探测任务列表与上报间隔，都是低频变更的配置，可以在每次上报的响应里按版本对账。由此 agent 与 hub 之间不需要应用层连接状态，下列问题不存在：连接替换时迟到的拆除误删新会话、半开连接探测、出站队列满导致推送丢失、握手与首报之间的在线语义、hub 重启后的重连风暴（每个 agent 本来就是每周期一个请求，恢复后的负载就是稳态负载）。

unary 是普通 HTTP POST，HTTP/1.1 即可，过反代与 CDN 无需特殊配置。不使用 bidi streaming：它要求 HTTP/2 端到端，反代到 hub 这一跳需要显式配置。

连接断开不等于节点宕机——绝大多数断开是网络抖动、反代空闲超时或 hub 重启。长连接方案要避免由此误报，仍须叠一层宽限期，叠完就回到了 TTL 语义，只是额外背上连接状态。更根本的是 §4.4 的不变式会碎：一旦存在连接，"连接在但没有数据"与"数据刚到但连接已断"是两个无法归并的状态，而哪个算在线没有正确答案。

代价：每次上报多一份 HTTP 头部；离线发现由"连接断开即知"变为"TTL 到期才知"。该代价有明确上界，且是被优先满足的约束而非残值——见 §4.4。

### 4.2 消息

```proto
service AgentService {
  rpc Register(RegisterRequest) returns (RegisterResponse);
  rpc Report(ReportRequest)     returns (ReportResponse);
}

message ReportRequest {
  Metrics metrics = 1;
  repeated ProbeResult probe_results = 2;   // 一次至多 1024 条（probelimit.MaxResultsPerReport），多余留待下一次上报
  uint64  tasks_version = 3;   // agent 当前持有的探测任务版本
  fixed64 facts_hash = 4;      // agent 静态信息的摘要
  Facts   facts = 5;           // 进程启动后的首次上报携带；此后仅在 hub 要求时携带
}

message ReportResponse {
  uint32     report_interval_ms = 1;
  ProbeTasks tasks = 2;        // 仅当 tasks_version 与 hub 不一致时携带
  bool       want_facts = 3;   // hub 持有的 facts_hash 与请求不一致
}

message Metrics {
  string boot_id = 1;
  optional double cpu_pct = 2;
  optional double load1 = 3;  optional double load5 = 4;  optional double load15 = 5;
  optional uint64 mem_total = 6;   optional uint64 mem_used = 7;
  optional uint64 swap_total = 8;  optional uint64 swap_used = 9;
  optional uint64 disk_total = 10; optional uint64 disk_used = 11;
  optional uint64 net_rx_total = 12; optional uint64 net_tx_total = 13; // 内核累计计数器
  optional uint64 net_rx_bps = 14;   optional uint64 net_tx_bps = 15;   // agent 本地采样速率，用于实时与历史峰值
  optional uint32 tcp_conns = 16;  optional uint32 udp_conns = 17;
  optional uint32 procs = 18;      optional uint64 uptime_s = 19;
}

message Facts {
  string hostname = 1; string os = 2; string kernel = 3; string arch = 4;
  string virtualization = 5; string cpu_model = 6; uint32 cpu_cores = 7;
  string agent_version = 8;
  bool icmp_available = 9;     // 两种 ICMP socket 是否至少一种可用，见 §8.2
}

message ProbeResult {
  uint64 task_id = 1;
  uint32 age_ms = 2;           // 测量完成至发送的时长，agent 单调钟
  oneof outcome {
    uint32 rtt_us = 3;
    Timeout timeout = 4;       // 计入丢包
    ProbeError error = 5;      // 无权限、解析失败等；不计入丢包；message 至多 128 字节，agent 按 rune 截断、hub 超长拒收
  }
}
```

`boot_id` 放在 `Metrics` 而不是 `Facts`：流量差分必须在同一条消息里同时拿到计数器与它所属的启动周期。若 `boot_id` 随 `Facts` 走，重启后的首次上报只带新计数器，当新计数器已超过旧基线时（长时间断连且流量大），hub 会把它当成同一启动周期内的增量而错误入账。

### 4.3 对账

探测任务、主机静态信息、上报间隔三样状态都是电平触发：agent 每次上报带上自己持有的版本 / 摘要，hub 在响应里补齐差异。hub 数据库被恢复、重启或丢失某节点的静态信息后，无需 agent 重启即可重新收敛，收敛时间不超过一个上报周期。

### 4.4 在线判定

`live` 中每节点一个条目，同时持有最新指标与 `last_seen`（单调钟）。在线 ⇔ `now − last_seen < TTL`。"在线"与"有最新数据"是同一个事实，只有这一个来源；不存在第二张在线表。节点从首次成功上报起即在线。

TTL 是**离线发现延迟的上界**，也是这条链上唯一被直接配置的量：环境变量 `PROBE_OFFLINE_AFTER`，默认 30s，下限 10s，上限 180s。其余三个量由它反推，不各自取值：

| 量 | 取值 | 依据 |
|---|---|---|
| TTL | `PROBE_OFFLINE_AFTER`，默认 30s | 掉线多久应当被看见是产品指标，由部署者定 |
| 下发的上报间隔 | `TTL / 3` | 一个 TTL 内有三次上报机会，容得下两次连续失败而不误判离线 |
| agent 退避上限（§4.7） | `TTL` | 恢复后重新可见的时长与掉线被发现的时长同一预算 |
| 离线告警宽限期（§9.1） | 下限为 TTL | 宽限期短于 TTL 会在面板仍显示在线时告警；两个口径必须同向，由保存规则时的显式校验承载 |

方向不可倒置：不能先按带宽预算选上报间隔、再让 TTL 从中掉出来。间隔是实现细节，TTL 是使用者唯一能感知的那个数。

上限来自结果排空：上报间隔是 TTL/3，一次上报至多携带 `MaxResultsPerReport`（1024）条探测结果，必须能排空 `MaxTasksPerNode / MinIntervalS`（64 任务 / 5 s）的满速产出并留余量——超过 hub 读上限（256 KiB，由这些常量推导）的请求会被整条拒绝并回队，形成永久失败。这条关系由编译期断言钉住，放宽任何一个常量都必须重新审视。

### 4.5 时钟

不信任 agent 墙钟。指标由 hub 在收到时打点；探测结果用 `age_ms` 反推测量时刻。hub 内部凡是时长一律用单调钟——墙钟被 NTP 向后拨时差值为负，用它做除数会得到离谱的速率。

### 4.6 版本偏斜

agent 与 hub 不同时升级。hub 必须接受旧 agent 的上报（缺失的 `optional` 字段 = 无读数）；agent 忽略响应里不认识的字段（protobuf 默认行为）。`buf breaking` 以 `WIRE_JSON` 级别在 CI 中守线上兼容——第三方主题以 JSON 调 `PublicService`，字段名同样是契约。

### 4.7 失败与退避

上报失败时 agent 做带抖动的指数退避，上限取 TTL 而非独立取值（§4.4）：上限若长于 TTL，hub 短暂不可用后早已恢复，面板上却仍显示掉线。抖动负责把恢复后的重试摊开，成功后回到下发间隔。指标不缓存（过期的实时数据没有意义）；探测结果缓存至多 `MAX_AGE`（120s，其取值约束见 §6.4），超龄丢弃；hub 以 `InvalidArgument` 拒绝的请求不回队——确定性拒绝重发无益。回队是至少一次语义：hub 已入账但响应丢失时同一批结果会重复计入，协议未做去重，精确一次留待后续里程碑。流量不因断连丢失：计数器是累计值，恢复后的首次差分覆盖整个断连区间（前提见 §7）。

### 4.8 注册

`probe-agent register --hub <url> --key <key> [--name <name>] [--insecure-http]` 调 `Register`，把节点 token 写入配置文件（权限 0600）。hub 地址在发请求之前按 §5.7 的传输规则校验（被拒时 key 不出线、窗口名额不消耗），`--insecure-http` 把放行明文 http 的决定写进配置。已有配置时沿用其中的 `probe_allow`、`probe_deny`：重新注册换的是 hub 身份，探测策略属于宿主机；已有配置读不出来时报错，不静默丢掉它。安装脚本只负责下载、校验、调用这条命令与安装服务单元，脚本里不解析 JSON。

配置里除 hub 地址、token 与名字外的字段都是宿主机本地策略（`insecure_http`、`probe_allow`、`probe_deny`，§5.7、§8.4），由 `probe-agent configure --config <path> [--insecure-http=true|false] [--probe-allow CIDR,...] [--probe-deny CIDR,...]` 修改：只改命令行上显式给出的项，列表给空串即清空；读取时不校验（才能修正一份升级后被 `run` 拒绝的配置），写入前按 `run` 加载时的同一套规则校验整份配置，打印修改后的本地策略；目标文件已存在时保留它的属主与权限（root 执行 configure 不能把服务用户读不到的文件留给 `run`）。配置里的未知字段、以及第一个 JSON 对象之后的任何内容都是错误：策略字段拼错时若静默忽略，宿主机以为拒绝了的地址实际放行。agent 只在启动时读配置，改完要重启服务。hub 无法修改这些字段：agent 运行期不写配置，下行消息里也没有对应字段。

### 4.9 节点来源地址与国家 / 地区

来源地址：hub 记录每个节点最近一次上报的来源地址——只用 hub 在 `Report` 上看到的对端地址，经 `auth.ClientIP` 按 `--trusted-proxies` 解析；不读 `CF-Connecting-IP` 之类的旁路头（§5.4 的"不从请求头推断"），agent 也不自报地址：那是 agent 的自述，与"hub 看到什么"是两个事实，混在一列里无法区分。hub 在反代之后而未把反代列进 `--trusted-proxies` 时，记下的是反代地址（与 §5.3 登录通知的来源地址同一口径，文案不遮掩）。存 `node.last_source`（规范化文本；空即 hub 没有记录到来源——从未上报，或最近一次上报早于 hub 开始记录来源的版本，后者 `last_seen_at` 有值而这里为空），只存最后一个——v4 与 v6 交替上报时留最后一次，面板看到的就是最近一次的事实；与 `last_seen_at` 同一路径落盘（随分钟行刷出与退出时写入），`Report` 路径仍只碰内存，`live` 的条目携带它。可见范围仅 `AdminService` 的 `Node`（会话与 API token 均可读）；`PublicNode` 里连字段号都不分配。不做手动覆盖：地址是观测事实，手动值写在备注里。hub 与节点同在内网、或 agent 经出口代理时看到的是内网或代理地址，照实记录。

国家 / 地区：每个节点一个 ISO 3166-1 alpha-2 国家码，面板与公开页显示为徽章（旗帜用 Unicode 区域指示符，不引入图片资源）。来源两条：运维在面板开启查询服务后由 hub 按来源地址自动查得，查得的国家与它所属的地址成对存放（`node.country`、`node.country_ip`）；或由管理员逐节点手动指定（`node.country_pin`）。手动值优先且互不覆盖：查询不改手动列，手动列清空即回落查得值；手动列只有 `UpdateNode` 一个写者，查得两列有刷出（换地址即清空）与查询器（条件写入）两个写者，但没有哪个写者同时写两边，冲突不需要裁决。查询默认关闭；开启是 `setting` 里的显式开关（键 `geo.enabled`），面板写明开启即把节点地址发给哪个服务——hub 出网到第三方是信任边界上的事实，由运维决定而不是由默认值决定。服务地址来自设置（键 `geo.url`，默认 `https://ipinfo.io/{ip}/country`，可回显、不算凭据），代码不得在配置之外另有出站目标，让"hub 会连到哪里"能从配置读出。开关与服务地址在 `UpdateSettings` 里都是 `optional`、缺席即不变（与 §10 总闸同一口径）：这两项决定 hub 是否、向谁发送节点地址，只改外观的老客户端若把它们整体替换成内置值，会顺手把运维选定的服务换回默认并开始向它发地址。服务地址必须含 `{ip}`，且 `{ip}` 不得出现在主机或端口位置（含 `[{ip}]` 的 IPv6 字面量写法——否则 hub 会直连节点自己的任意端口，而 IPv4 节点又会因 `[…]` 里不是 IPv6 而每次失败），必须出现在路径或查询串这些随请求发出的部分（只写在 `#` 后的片段里时请求不带地址，每个节点的请求都一样，服务若按请求方地址作答，所有节点都会被记成 hub 所在的国家且不再重查；判定是两族样例填入后请求行必须不同），填入样例地址后是带 host 的绝对 http(s) URL、不含用户信息（net/http 会把 URL 里的用户信息转成 Basic 认证头，等于给"不带任何凭据"开了后门）、不超过 2048 字节（解码预算的推导前提）。应答只认 200。查询只发地址、不带任何凭据；响应去掉首尾空白后只接受两个大写字母，其余按失败处理，响应体不进入任何解释路径；出站客户端复用 §9.3 通知那一个（不跟随重定向、限读响应体）。每节点每地址至多查一次——查询器在内存里记住（节点, 地址）→ 国家的答案，每节点只保留最近 4 个地址（超过 4 个地址轮换的节点会再次外呼，对外文字要写出这个上界，不能许诺无条件的"至多一次"），节点在 v4 与 v6 之间交替时不重复外呼，命中即直接写回；失败按小时级退避重试（退避的键含服务地址，运维换了服务即重查），hub 重启后对尚无答案的地址重查一次——无退避会让一个坏链路的节点每次上报都触发一次外呼；每次外呼前都重新读开关与服务地址，关闭之后至多还有一个在途请求（上界是客户端总超时），不让一轮开头读到的设置授权整轮几十分钟的外呼；地址变了国家即清空并重查，迟到的应答只在地址仍一致时写入：国家是"对某个地址"的答案，不是节点属性，换了出口的节点不得沿用旧答案。非公网地址（RFC 1918、CGNAT 100.64/10、回环、链路本地、ULA、未指定、组播与文档保留段）不发出查询：这类地址没有国家，发出去只是把内网拓扑交给第三方。公开：`PublicNode.country` 只放行最终显示的那个国家码，不放行地址与来源——公开页表达"在哪个区域"，不定位机器；§12 的允许列表随之改。面板显示国家、来源（手动 / 查得）与查得于哪个地址。开关与服务地址属备份的配置层（备份落地时登记）。本地 mmdb 后端：`serve` 加 `--geo-mmdb <路径>` 指向一份 MaxMind 格式的国家库（GeoLite2-Country 或同格式），用纯 Go 读取器（`github.com/oschwald/maxminddb-golang/v2`）在本机查询，完全不出网；配了它就不再向 HTTP 服务发任何请求（mmdb 优先，`geo.url` 保留但不生效，面板写明当前后端是"本地文件 <路径>"还是"HTTP 服务 <地址>"）。查询语义与上面完全一致，只换后端：成对存放、手动优先、非公网不查、每地址至多一次、失败按小时退避（本地查不到也算失败，退避表的作用是免得每轮都查同一个没有答案的地址）、公开只放行国家码；`geo.enabled` 仍是唯一开关——它表达的是"要不要给节点标国家"，后端不同只改变"地址会不会离开本机"，面板在 mmdb 后端下写明不出网。路径是部署配置而不是运行配置，所以是启动参数不是设置：文件读不到或不是 mmdb 格式在启动时报错退出，不静默退回 HTTP——静默退回会让运维以为没出网而实际在出网。启动时把库文件整读进内存并做结构校验（读取器的 Verify 只查搜索树与数据段的结构损坏；库文件没有校验和，把合法值换成另一个合法值——写错的国家码——它查不出，由答案校验与人工核对承担；非法 UTF-8 这类结构性损坏才会被拒），运行期不再访问该文件：原地覆盖或截断都不影响运行中的答案，替换文件后重启才生效，这一条由构造保证而不是靠运维遵守"先写临时文件再改名"；文件大小上限 256 MiB，超出按配置错误启动失败。启动日志记录选定的后端，mmdb 下另记路径与库元数据（数据库类型、构建时间），运维据此确认不出网与加载的库版本。本地查不到（无记录或缺国家码）以明确的"无记录"错误进退避，日志不沿用 HTTP 口径的文案；退避键里的"服务"一项由后端给出（HTTP 为服务地址，mmdb 为库路径），所以 mmdb 下改动不生效的 `geo.url` 不会清掉退避。后端拿到的开关与服务地址是 Resolver 判定准入时读到的同一份快照，后端不得另读设置；面板回显的后端描述取自 hub 实际选定的后端对象，不另由启动参数推导。文件更新靠重启，不做热加载。

## 5. 鉴权与信任边界

### 5.1 节点 token

- 32 字节随机数，只经 `Authorization: Bearer` 传递，不进 URL、不进请求体。
- hub 只存 SHA-256。token 是高熵随机数，不需要抗字典攻击的慢哈希，而慢哈希撑不住每秒数百次校验。
- 内存中维护 `hash → node_id` 映射，`Report` 的鉴权只查它、不读库。修改顺序固定为：持 `auth` 的锁 → 写库并等待成功 → 改映射 → 放锁。写库失败则映射不动；进程在两步之间崩溃则映射在下次启动时自库重建。任何绕开这把锁直接改表或改映射的写入都会让两者分叉。
- token 明文只在创建与轮换时返回一次。轮换后旧 hash 立即从映射移除。
- 按 `node_id` 键控的其余内存状态（live 的桶、限速桶）随节点删除一起清理：进程内删除（`AdminService.DeleteNode`）在映射更新的同一步清理；`probe-hub node delete` 在另一进程改表，这些状态在 hub 重启重建映射时随之消失。
- 每节点令牌桶限速：上报速率超过下发间隔所对应速率的 2 倍即返回 `ResourceExhausted`。
- `AgentService` 请求体上限 64 KiB。

同一 token 被两台机器同时使用（克隆虚拟机）会表现为 `boot_id` 交替出现，使流量基线反复重置。hub 在窗口内统计 `boot_id` 切换次数，超过阈值即在管理面板对该节点标记警告。

### 5.2 注册窗口

管理员在面板开启注册窗口：生成一次性 key，带截止时间与可注册节点数上限。窗口关闭与 key 错误返回同一响应。失败计数按来源键（§5.3）独立于登录失败计数：批量安装时用了过期 key 是配置失误而不是对面板的攻击，共用计数会把运维者自己锁在登录页外。只有窗口开启且 key 错误才计数；窗口关闭时没有可猜的秘密，计数只会误伤与他人共用出口地址的运维者。

`Register` 是 `AgentService` 唯一的匿名方法，因此按来源键令牌桶限速（桶容量 30、每秒补充 1，超限返回 `ResourceExhausted`），与 `PublicService` 的限流同一原则。限速在 connect 解码之前（解码失败的请求也计数；挂载点上按路径的 HTTP 中间件，窗口失败计数用它算出的同一个来源地址）生效：窗口关闭时匿名请求也到不了写协程，否则任何人都能用几十字节的请求体让分钟刷出与 facts 落盘排在自己的事务之后。批量安装脚本遇到 `ResourceExhausted` 按退避重试即可。

### 5.3 管理员

- 单管理员。密码用 argon2id 存储，通过 `probe-hub passwd` 在 hub 主机上交互设置；没有经网络的首次设置页，也就没有"谁先访问谁占有"的窗口。
- 管理员表为空时登录一律失败。空表的语义是"无人可登录"而不是"无需认证"，由登录路径上的显式检查承载。
- 会话 token 为 32 字节随机数，库中只存 SHA-256，带绝对过期与空闲过期。cookie：`HttpOnly`、`SameSite=Strict`、不设 `Domain`（host-only，§10.1 的隔离依赖它；同名 cookie 有多个值时逐个校验、任一有效即通过，见 §10.1），`Secure` 由可信代理转发的协议决定。修改密码即清空全部会话；API token 不随之吊销（§5.6）。
- 会话可列可撤：`ListSessions` 返回当前有效的会话（创建时刻、最近使用时刻、是否本次请求所用的会话），`RevokeSession` 按 `admin_session.token_hash` 撤销一个；两者都是 `ACCESS_SESSION`——API token 不能列也不能撤会话，与 token 不能管理 token 同一原则（§5.6）。以 hash 作标识：知道 hash 不能冒用会话，校验需要 cookie 里的明文。撤销当前会话等于登出（响应清 cookie）；撤销不存在或已过期的 hash 不报错，重复撤销幂等。换设备或怀疑某处忘了登出时按会话撤销，不必改密码清空全部。
- 密码校验同一时刻只跑一个：门在锁定判定之后、慢哈希之前，容量固定为 1，并发到达的其余尝试立即以 `ResourceExhausted` 拒绝、文案让其稍后重试，且不计入失败次数（它不是一次猜测，计入会让攻击者用并发把运维者锁在外面）；会话 cookie 校验是哈希查表，不经过门。排队只是把同一波洪水推迟、让合法登录排在队尾，拒绝才让它落空；容量按核数推导会在小机器上重新打开口子。
- 登录通知：`Auth.Login` 签发会话成功、以及登录失败达到锁定阈值时，经通知渠道各推一条（时间、来源地址、凭据种类；时间写进摘要正文本身、RFC 3339 并带 hub 时区偏移——投递会重试、重启后续投，接收方看到的送达时刻不是登录时刻；来源地址是 `auth.ClientIP` 解析后的值，反代未配 `--trusted-proxies` 时会是代理地址，文案不遮掩这一点）。投递到哪些渠道由一个全局设置（键 `notify.login_channels`）选择，不走规则×节点；没有选渠道即不发。在 `Settings` 里它是一个带 presence 的 message 字段（`optional`，内含 `repeated int64 channel_ids`），`UpdateSettings` 缺席即不变、显式给出空集合即关闭（与 §10 总闸同一口径）：proto3 的裸 repeated 字段缺席与空列表在线上不可区分，直接用它会让只改外观的老客户端把已选的渠道清空；保存时每个 id 必须存在（否则 `InvalidArgument` 点名），列表按请求里的原始条数（含重复）至多 16 个，超出 `InvalidArgument` 点名字段与上限——没有上限就算不出解码预算的最坏请求；渠道被删除时从这个列表里摘除（与 `alert_rule_channel` 删渠道同一事务）。API token 的使用不通知——它是自动化，会刷屏。事件进 `alert_event` 并带投递记录，`rule_id` 与 `node_id` 为 0，`ListAlertEvents` 能看到；现有查询、索引与 `Forget` 清理要按 0 值核对。单管理员面板没有第二双眼睛看审计日志，登录通知是密码泄漏当下唯一的信号。
- 登录失败按来源键锁定。来源键由 `auth.SourceKey` 统一归一化（IPv4 按单个地址，IPv4 映射地址先还原；IPv6 按 /64——一台主机通常拥有整个 /64，逐地址计等于不计），登录锁定、注册窗口失败计数与两处限流（§5.2、§10）共用同一个键。
- 跨站请求伪造由以下几条各自独立的事实约束，不指定其中哪一条是"主要防线"：会话 cookie 为 `SameSite=Strict`；hub 不下发任何 CORS 允许头；`AdminService` 不把任何方法标为无副作用（因而不接受 GET）；Connect 处理器对 `application/json` 与 `application/proto` 之外的 `Content-Type` 拒绝服务，而浏览器的跨站"简单请求"发不出这两种类型。最后一条是对 connect-go 行为的断言，列入 §13 并由 §12 的测试钉住。

管理员第二因素状态位于配置层 `admin_security`：TOTP 密钥与已消费时间步、恢复码摘要、WebAuthn 凭据和版本号。密码登录启用 TOTP 后必须同时验证 OTP 或一次性恢复码；Passkey 独立支持无密码登录，要求可发现凭据与用户验证。`--admin-origin` 固定可信 HTTPS 来源，不使用请求的 Host 自报值；主题必须使用不同主机名。认证消费、凭据版本 CAS 和会话签发同事务，修改认证方式同事务撤销旧会话。设置挑战及重新认证证明绑定会话、版本和有效期且只能消费一次。普通失败、成功、锁定及认证变更都写审计；普通失败不投递。设备丢失时本机 `security-reset --yes` 清除因素并撤销会话，不清密码和 API token。

### 5.4 TLS 与可信代理

hub 只监听明文 HTTP，TLS 由反代（Caddy / nginx / CDN）终止，hub 内没有证书代码。

- `--listen` 默认 `127.0.0.1:8080`。监听非 loopback 地址时启动日志告警：此时任何人都能绕过反代直连并自带转发头。
- `--timezone` 是 IANA 时区名，默认取 hub 进程的本地时区；只用于 §7 流量周期的重置日判定、§9.4 到期日的天边界、面板文案与通知文案里的时刻（§5.3 登录通知的摘要按它写 RFC 3339 时间），不参与任何时长计算。本地时区的名字按 `TZ`、再按 `/etc/localtime` 符号链接的目标路径里 `zoneinfo/` 之后的部分解析（在所测的 Alpine 3.21、Debian 12、Ubuntu 24.04、Rocky Linux 9 上按各自的标准方式设置时区后都是符号链接，Alpine 指向 `/etc/zoneinfo/`）；不读 `/etc/timezone`——RHEL 系没有它，Debian 与 Ubuntu 用 `timedatectl` 改时区后它仍是旧值。`/etc/localtime` 是复制出来的普通文件时（常见于 Dockerfile）取不到名字，退回 UTC 并告警。
- `--trusted-proxies` 显式给出 CIDR 列表。只有 TCP 对端地址落在列表内的请求，其 `X-Forwarded-For` / `X-Forwarded-Proto` 才被采信。`X-Forwarded-For` 可能有多行（HAProxy 的 `option forwardfor` 把真实地址另起一行追加），读取时把全部字段行按出现顺序合并后再取值，只读第一行会让键取自客户端伪造的那一行。`X-Forwarded-Proto` 同样按全部字段行合并后取第一个值，即最外层那一跳写的协议；它不带逐跳地址，没法像 `X-Forwarded-For` 那样从右向左跳过可信代理，代理追加而不覆盖时客户端自带的值排在最前。这一点有意不处理：它只决定签发或清除请求者自己的会话 cookie（Login、Logout、撤销当前会话）时带不带 `Secure`，客户端只能改到自己，影响不到别的来源。空列表 = 不信任任何转发头、一律用 TCP 对端地址，是收紧方向。hub 不从请求头推断自己是否在反代之后。
- hub 主动出网的目标只有两类，都由配置显式给出、默认没有：通知渠道（§9.3）、国家查询（§4.9）与分层备份（§6.7）；代码内不得另有出站目标。
- `--theme-origin` 给出主题托管的 origin（§10.1）；未给出时主题功能整体关闭。
- hub 不生成自己的对外地址：面板里安装命令的 hub 地址取浏览器当前的 origin（§10），所以没有 `--site-url`，也不存在从 `Host` 头推断对外地址的问题。

### 5.5 为什么不做 mTLS

mTLS 相对 bearer token 的增量是"凭据不过线"与"在 HTTP 层之前拒绝未授权连接"。代价：它要求 hub 自己终止 TLS，与 §5.4 冲突——若由反代验证客户端证书再以请求头转发身份，hub 又回到信任请求头；还需要 CA、签发、轮换、吊销整套生命周期，而注册阶段仍需一个一次性秘密换取证书。

在本项目的威胁模型下，节点 token 泄漏的后果是有人能伪造该节点的指标；hub 失守的后果受 §5.7 的 agent 侧边界约束。两者都不足以支撑上述代价。agent 强制校验服务端证书，不提供跳过校验的开关；§5.7 的 `insecure_http` 放行的是明文 http，不是跳过 https 的证书校验。

### 5.6 API token

与会话平行的第二条凭据口径，给 agent 与脚本读数据用：自动化进程不必持有管理员密码，出事时的吊销范围从"全部会话加改密码"缩到一个 token。

- 明文为固定前缀 `probe_at_` 加 32 字节随机数的 hex，库中只存整串的 SHA-256，明文只在 `CreateApiToken` 的响应里出现一次。前缀让泄漏到日志、配置或代码仓库里的 token 能被审查与 secret scanning 认出。
- 两条路径互不回退：`Authorization` 头的 scheme 为 `Bearer` 即走 bearer 路径，cookie 一律不看；否则走 §5.3 的会话路径。任一路径的失败都不转交另一条——有回退就等于实际生效的是两套鉴权里较弱的那条，且弱在哪条随请求头变化，事后无法从代码读出。scheme 为 `Bearer` 而 token 为空、格式不对或不存在，都返回 `Unauthenticated`。其他 scheme 不是 hub 的凭据，按不存在处理：反代做 Basic 认证时，浏览器会对每个请求自动附带 `Authorization: Basic`，nginx 与 Caddy 默认原样转给 hub，若见头即走 bearer 路径，面板的每个请求都会被拒。
- token 只能调 `ACCESS_READ` 方法（§3.2），其余返回 `PermissionDenied`，错误信息写明方法名：`ACCESS_SESSION` 方法说明 API token 只读、需要面板会话，`Login` 说明 token 不能用来登录、应只带密码。写操作对 token 开放要逐个显式决定，目前一个都不开；token 的建、列、删都是 `ACCESS_SESSION`，token 不能签发 token。
- §5.3 的四条 CSRF 事实属于会话路径，一条都不因 bearer 路径而放松。bearer 路径不需要它们：浏览器会自动附带的 HTTP 认证只有 Basic、Digest 这类缓存凭据，`Bearer` 只能由脚本显式设置，而跨源请求带 `Authorization` 头必须先过 CORS 预检，hub 不下发允许头。会话路径仍然需要。
- 每次校验都查库，不缓存：吊销（删行）在下一个请求即生效，hub 运行中由 `probe-hub` 直接改库也一样。管理请求的频率远低于上报，查库的代价可以接受；引入缓存必须同时给出吊销的传播路径。
- 最后使用时间只供展示，与会话同一口径：从未使用或距已落库值满一分钟才异步刷新，不让每次读请求都排进写协程；刷新只 UPDATE 已存在的行，吊销之后才落库的刷新不会把 token 写回来。它不参与任何裁决。
- 名称 1–64 字符，不要求唯一，身份是 id。token 总数上限 100，超出返回 `ResourceExhausted`。不设过期时间，靠面板上的创建时间、最后使用时间与手动吊销管理。
- 改密码不连带吊销 token：连带吊销会让每次轮换密码都静默打断自动化。代价是密码泄漏期间被创建的 token 在改密码后仍然有效，所以 `probe-hub passwd` 改完后列出现存 token（名称、创建时间、最后使用时间）并询问是否全部吊销，默认不吊销。`probe-hub token list`、`probe-hub token revoke --id N`、`probe-hub token revoke --all` 供面板不可用或密码已泄漏时应急。离线子命令直接改库：只有建立状态的（`passwd`、`window open`、`node create`）在 `--db` 指向的文件不存在时建库，供第一次 `serve` 之前准备；其余读或改已有状态的子命令（`token`、`stats`、`node list|delete|rotate-token`、`window close|show`）在库不存在时报错——否则写错 `--db` 会静默建一个空库，`token revoke --all` 报告吊销了 0 个，而真正的库原封不动。
- 自描述：`GetApiReference`（`ACCESS_READ`）返回入口卡片（`proto/SKILL.md`）与全部 proto 源文件，均在构建时嵌入——不在仓库里的 agent 由此取得与 hub 同版本的 schema，注释即文档。不用 gRPC reflection：它是双向流，不能以纯 HTTP+JSON POST 调用，与 §4.1 的 unary 约束冲突；也不另开端点，对外仍是三个 Connect 服务。
- 入口卡片约定 `PROBE_HUB` 与 `PROBE_TOKEN` 两个环境变量，写明进门方式、schema 的取法、JSON 约定（int64 编码为字符串、时间为 Unix 秒、`_ms` 后缀为毫秒、缺读数与读数为 0 的区别、Connect 错误体）与可直接运行的例子；例子由 e2e 执行（§12）。

### 5.7 hub 失守时 agent 宿主机的边界

威胁模型：攻击者完全控制 hub（进程、数据库、面板页面），或处在 agent 与 hub 之间的明文链路上。要守住的性质：攻击者不能在 agent 宿主机上执行代码、不能读写宿主机文件、不能让 agent 耗尽宿主机资源（内存与日志）、不能让 agent 探测宿主机本机（回环与本机接口上的地址）与链路本地地址。攻击者仍能做的：伪造展示、停掉监控，以及按 §8.4 的速率上限探测本地策略允许的地址（私网默认允许）。下面各条分别承载其中一部分，互不替代，不指定哪一条是主要防线：

- 下行面：agent 从 hub 收到的只有 `RegisterResponse` 与 `ReportResponse` 的三个字段；agent 不监听端口、不执行外部命令、不自我升级、运行期不写配置。新增下行字段时必须在本节写明它交给了 hub 什么能力。
- 上报间隔：agent 把 `report_interval_ms` 限定在 hub 能合法配置的范围内，即 TTL 取 `MinTTL` 与 `MaxTTL` 时的间隔（间隔 = TTL / `ReportsPerTTL`）。越界（含 0）取最近的边界并告警，值变化时告警一次。TTL 边界、`ReportsPerTTL` 与间隔的换算只在 `internal/agentwire` 定义一次，hub 的 TTL 准入、hub 的间隔下发、agent 的限定与 agent 的退避上限（§4.7）都读它。没有这一条，hub 下发 1 ms 就能让 agent 不停地采集与上报。
- 响应体：agent 在 HTTP 层限读响应正文 `agentwire.MaxResponseBytes`（64 KiB），成功与错误响应都经过这一层；超出即报错，不截断（截断的正文可能恰好解码成一条更短的合法消息），按普通失败退避。agent 不接受压缩：connect 不声明 gzip，HTTP Transport 也不自行声明与解压，hub 不顾声明回 gzip 时按不认识的编码报错；读到的字节因此就是解码前的全部大小，响应本来不超过上限，压缩没有收益。connect-go（v1.21.0 实测）的 `ReadMaxBytes` 只管成功响应的消息：256 MiB 的错误正文让客户端分配了 1282 MiB，47 KiB 的 gzip 错误正文分配了 128 MiB，所以限读不能交给它。hub 侧有测试钉住满载 `ReportResponse` 的编码不超过上限（当前 18199 字节）。响应头另设 32 KiB 上限（Go 默认 10 MiB）。
- 重定向：agent 的 HTTP 客户端不跟随任何重定向，`Register` 与 `Report` 都没有需要重定向的场景。Go 的 http.Client 跟随同主机重定向时会保留 `Authorization`，而协议不参与判断，https 到同主机 http 的重定向会把节点 token 明文发出。
- 传输：hub 地址必须是 https。http 只在两种情形下接受：主机是 loopback IP 字面量（`127.0.0.0/8`、`::1`；`localhost` 这类名字要经解析，不在豁免内），或配置里有 `insecure_http: true`（由 `register --insecure-http` 或 `configure --insecure-http=true` 写入）。`register` 在发请求之前、`run` 在加载配置时调用同一个校验函数。不满足时 `run` 拒绝启动，报错里给出两种放行方式。已有的非 loopback http 部署升级后会停在这一步，安装脚本的启动确认随之失败并指向日志；这是有意的，明文链路上的中间人与 hub 失守等价，不能静默延续。
- 探测目标：agent 解析出地址之后、发包之前，按宿主机本地策略检查实际要连的地址（§8.4）。策略只来自本地配置，hub 改不了。
- 日志：不少日志行由 hub 的应答触发（上报失败、被拒的任务、越界的间隔），错误文本里带着 hub 给的字符串；OpenRC 与 launchd 把 stderr 写进不轮转的普通文件。边界放在出口（`internal/agent/agentlog`），每一行都经过它：令牌桶每个令牌恰好一行，突发 20 行、此后每 30 秒一行，被压掉的行数以 `suppressed_before` 带在下一次放行的那一行上；消息与每个字符串值（error 等先格式化）连同截断标记至多 256 字节。标准库 `log` 经 `slog.SetDefault` 接到同一个出口，在创建任何网络客户端之前完成：`net/http` 在 idle 连接收到多余字节时用它写出对端给的内容，不接过来就绕开了边界。`register` 与 `configure` 是一次性命令，最终错误行至多 4 KiB（`register` 的错误可能带着 hub 的文本）。调度器每次应用清单对被拒任务只出一行汇总（每个被拒任务仍留一条 error 结果，结果队列有容量上限）；没有这两条，一个塞满空任务的 64 KiB 响应能刷出 4 MiB 日志。这给的是速率上界，日志文件的保留仍归宿主机的日志管理：systemd 下是 journald 的保留策略，OpenRC 与 launchd 的日志文件产品不轮转。
- 资源上限：systemd 单元设 `MemoryMax=128M`。2026-09-29 在 Debian 12.15 arm64 / systemd 252.39-1~deb12u2（OrbStack LXC，cgroup v2）连接一个 hub、64 个 ICMP 任务以 5 秒间隔探测回环，全部有结果后持续 3 分钟，服务 cgroup 峰值 21389312 字节（约 20.40 MiB）；取六倍后向上取整为 128 MiB，给 Go GC、并发探测和平台差异留余量，不视作所有负载的最坏峰值。生效要求内核提供 memory 控制器。OpenRC 不设内存上限：`rc_cgroup_settings` 依赖宿主机向服务 cgroup 下放 memory 控制器；2026-09-29 在 Alpine 3.21 / OpenRC 0.55.1（OrbStack LXC，cgroup2，根 `subtree_control` 为空）实测静默不生效，安装脚本不修改宿主机的全局 cgroup 配置。launchd 同样没有会被强制执行的内存上限；OpenRC 与 launchd 的内存防护只靠 agent 的响应体上限，不额外承诺服务级强制上限。systemd 的这一层是纵深防御，agent 内存有界仍由响应体上限与任务数上限承载。
- 安装链路：面板由 hub 提供，失守的 hub 可以把面板上的安装命令整条换掉，这一点产品内防不住。安装命令的可信来源是 README 与 GitHub Release，面板上的命令只是为了方便。能防住的是一种更隐蔽的变体：命令仍取 github.com 上的脚本，只在后面追加一个指向恶意镜像的 `--base-url`。为此每个 release 的安装脚本（agent 的两个与 hub 的一个）内嵌本版全部 tar 包的 SHA-256（由 `make release` 写入），只按内嵌值校验，不拿下载来的 `SHA256SUMS` 作校验依据。`--base-url` 只改变从哪里取字节，不改变接受哪些字节。脚本只安装自己所属的版本，没有 `--version`，要装哪个版本就取哪个版本的脚本；没有写入哈希的源码脚本拒绝安装（卸载不下载，照常可用）。

## 6. 存储

### 6.1 读写模型

所有写由单一写协程串行执行，读走独立的只读连接池。SQLite 同一时刻只允许一个写者，应用内串行化从根上避免写者之间的 `SQLITE_BUSY`。

`Report` 的处理路径只碰内存、不等待数据库：鉴权查 `auth` 的内存映射，任务列表取自 `probe` 的内存副本，写入落在 `live`、流量累加器与探测桶；`Facts` 的保存投递给写协程后即返回。落盘由定时器驱动：分钟边界刷指标与探测行，每 10 秒刷流量，退出时全刷。管理端的写操作（建节点、轮换 token、改任务）则等待写协程的结果后才应答。

### 6.2 指标表

三级结构相同：`metric_1m`、`metric_5m`、`metric_1h`。

```sql
CREATE TABLE metric_1m (
  node_id INTEGER NOT NULL,
  ts      INTEGER NOT NULL,          -- 桶起始，Unix 秒
  cpu_sum REAL NOT NULL,       cpu_n INTEGER NOT NULL,       cpu_max REAL NOT NULL,
  mem_used_sum INTEGER NOT NULL,  mem_used_n INTEGER NOT NULL,  mem_used_max INTEGER NOT NULL,
  swap_used_sum INTEGER NOT NULL, swap_used_n INTEGER NOT NULL,
  disk_used_sum INTEGER NOT NULL, disk_used_n INTEGER NOT NULL,
  load1_sum REAL NOT NULL,     load1_n INTEGER NOT NULL,
  tcp_sum INTEGER NOT NULL,    tcp_n INTEGER NOT NULL,
  udp_sum INTEGER NOT NULL,    udp_n INTEGER NOT NULL,
  procs_sum INTEGER NOT NULL,  procs_n INTEGER NOT NULL,
  rx_bytes_sum INTEGER NOT NULL, rx_bytes_n INTEGER NOT NULL,
  tx_bytes_sum INTEGER NOT NULL, tx_bytes_n INTEGER NOT NULL,
  PRIMARY KEY (node_id, ts)
) WITHOUT ROWID;
```

**只存可加量**：和、样本数、最大值、字节增量。均值在查询时由 `x_sum / x_n` 得到，速率由 `bytes / 桶长` 得到。由此：

- 上卷是精确的求和与取最大，不是均值的均值。
- 每个取均值的指标有自己的样本数。上报里缺失的 `optional` 字段既不加进 `x_sum` 也不加进 `x_n`；`x_n = 0` 的桶在查询结果里是"无数据"而不是 0。"无读数 ≠ 读数为 0"由此从协议一直保持到图表。共用一个行级样本数做不到这一点：某个字段缺失的样本会把该列的均值拉低。
- hub 在一分钟中途重启时，退出前刷出的半桶与重启后的半桶落在同一主键上，用加法合并（`ON CONFLICT DO UPDATE SET x_sum = x_sum + excluded.x_sum, x_n = x_n + excluded.x_n, x_max = max(x_max, excluded.x_max)`）。这条合并正确的前提是每个内存桶至多成功写入一次：刷出时在 `live` 的锁内"取走并清零"，再交给写协程；写事务失败则该桶留在有界的待重试列表，事务原子性保证失败即未应用，重试不会重复计入。
- 最大值一路保留到 1h 级，短时尖峰不会被抹平。
- 流量列是字节增量的**和**（描述表里的 `Sum` 种类）而不是均值：`rx_bytes_n` 记录该分钟有多少次上报入了账——计数器缺失、以及按 §7 只进总量不进桶的增量都不计数，因此 `rx_bytes_n = 0` 与其他列一样是空洞而不是 0。查询对 `Sum` 列下发 `sum`（`MetricSample.sum`）而不下发均值与最大值，速率 = `sum / 桶长`；上卷仍是求和。
- `net_rx_bps` / `net_tx_bps` 单独使用 `MeanMax` 聚合，记录 agent 本地采样速率（bytes/s）的 sum、n、max；上卷保留最大采样值，不从请求到达间隔推算。网络图均值仍采用上述流量口径，峰值取速率 max。schema 21 给三个指标层追加列，旧行 n=0，不伪造旧历史峰值；配置层和指标层快照恢复同步迁移。

主键顺序即唯一查询路径（某节点 + 时间窗），`WITHOUT ROWID` 使主键索引就是表本身。

指标列由 `metric` 包内的一张描述表驱动（列名、整型或浮点、是均值还是和、是否带最大值、对应的 `Metrics` 字段或入账回调）；建表语句、内存桶的折叠、加法合并、上卷与查询的 SQL 都自它生成。新增一个指标 = 描述表加一项 + 一次迁移，不存在需要手工保持一致的多份字段清单。

### 6.3 探测表

```sql
CREATE TABLE probe_1m (
  node_id INTEGER NOT NULL, ts INTEGER NOT NULL, task_id INTEGER NOT NULL,
  sent INTEGER NOT NULL, lost INTEGER NOT NULL, errors INTEGER NOT NULL,
  rtt_sum_us INTEGER NOT NULL, rtt_min_us INTEGER, rtt_max_us INTEGER,
  PRIMARY KEY (node_id, ts, task_id)
) WITHOUT ROWID;
```

`probe_5m`、`probe_1h` 同构。`rtt_min_us`/`rtt_max_us` 可空：桶内没有任何 rtt 样本（全部丢包或错误）时为 NULL，`min()`/`max()` 聚合自动忽略；若落成 0，上卷会把"没有样本"当成 0 µs。主键里 `ts` 在 `task_id` 之前：唯一的查询是"某节点、某时间窗、全部任务"，`task_id` 在前会让 SQLite 只能定位到节点，然后扫描该节点的全部历史。

### 6.4 上卷

`rollup_state(level PRIMARY KEY, upto_ts)` 记录每级水位：水位之前的下级桶已被上卷。指标与探测两族表各有自己的水位（`metric_5m`、`metric_1h`、`probe_5m`、`probe_1h`），互不牵制。上卷任务对每一级：取水位之后的已闭合桶，`INSERT OR REPLACE … SELECT … GROUP BY node_id, 桶` 自下一级重算整桶，并在同一事务内推进水位。重算整桶使它幂等；同事务使它崩溃安全。

这里的替换语义与 §6.2 的加法合并不冲突：加法合并只发生在"内存桶 → 1m 行"，上卷只发生在"下级行 → 上级行"且总是整桶重算。

**冻结不变式**：水位之前的 1m 桶不再被写入，否则上级行不再反映下级行。它由三处共同维持，各有断言：

1. 写协程是 1m 行的唯一写入口，拒绝 `ts` 早于 5m 水位的加法合并（丢弃并记日志）。这是承载不变式的那道检查——它比较的是已持久化的水位而不是时钟，所以墙钟被向后拨、待重试列表里的旧桶迟到，都越不过它。
2. 上卷只把水位推进到 `桶结束 < now − ROLLUP_LAG` 的桶。
3. ingest 拒收 `age_ms > MAX_AGE` 的探测结果。

第 2、3 条的作用是让第 1 条在正常运行时不触发。从常量推：一条探测结果最迟在其所属 1m 桶结束后 `MAX_AGE`（120s）到达 hub；到达后先折叠进内存桶，要等下一次分钟刷出才成为 1m 行的写入，再晚至多一个刷出周期（60s）；写协程排队另留余量。因此 `ROLLUP_LAG` 取 300s，并以启动期断言 `ROLLUP_LAG ≥ MAX_AGE + 刷出周期 + 60s` 钉住三个常量之间的关系——调大 `MAX_AGE` 或刷出周期、或调小 `ROLLUP_LAG` 而不同步其余两个，会让合法数据落在水位之前被第 1 条丢弃。上下级一致性本身只依赖第 1 条，不依赖这些常量取值。

迟到的探测结果所属的分钟桶可能已经刷出过一次；它会进入一个同主键的新内存桶，在下次刷出时经加法合并并入已有的行。

### 6.5 保留与查询

保留期默认 1m：7 天、5m：30 天、1h：365 天，可配。prune 按时间片分块删除，每块一个短事务，不长时间占住写协程。

查询按窗口跨度选级：≤ 6h 用 1m，≤ 7d 用 5m，更长用 1h；再按目标点数上限在查询时二次分桶。

存储健康信号：`GetStorageStats` 与 `probe-hub stats` 在库大小与行数之外，给出每级指标与探测表的最老桶时刻（按表分别给，不合并——各级保留期不同，合并后无法与各自的保留期对比）、每级上卷水位（直接取 `rollup_state.upto_ts`，不另算，与上卷读的是同一个值）、上次 prune 与上次上卷的完成时刻（记在与 `rollup_state` 同类的簿记表里，只在成功时写：字段缺失即从未跑过，失败与从未跑过因此可区分）。全部是聚合值，API token 可读（`ACCESS_READ`）。prune 停了只表现为库慢慢变大，上卷停了只表现为长窗口的图变空，最老桶对保留期、水位对当前时刻是一眼能读出故障的两组对照：面板的存储页显示这些数，并在最老桶早于"保留期 + 一个该级周期 + 一个维护间隔"、或水位落后当前时刻超过三个该级周期时标红。多出的一个维护间隔是因为 prune 的截止点按桶长向下对齐、维护每分钟跑一轮：截止点跨过桶边界之后、下一轮 prune 删掉那一桶之前，健康的表也会比保留期早一个桶长多一点，不加这一段每个桶长都会误报一次。新库在第一轮上卷之前水位为 0，会标红约一分钟，不加特判。"上次 prune"只指时序表的 prune；告警事件的清理另有保留期、不依赖上卷，不混进同一行。不给可回收空间（`freelist_count × page_size`）：hub 没有 VACUUM 入口，这个数没有动作可对应。

规模估算（500 节点，从常量算起）：1m 级 500 × 1440 × 7 = 504 万行；5m 级 500 × 288 × 30 = 432 万行；1h 级 500 × 24 × 365 = 438 万行；合计约 1370 万行。按每行约 120 字节估，指标三级表在 2 GB 上下，探测表另计；均未实测（见 §13）。

告警事件与投递记录（`alert_event`、`alert_delivery`）随维护任务按保留期清理，默认 90 天，由 `--retention-alert-events` 配置，下限 24 小时；抖动的节点会持续产生事件，不设保留期表会无限增长。下限必须大于满队列的最坏排空时长，否则维护会删掉仍在重试的事件，通知随之丢失。推导以 `retention_test.go` 从常量重算为准（队列容量 256 批、每批 3 次尝试各 10 s 超时、1 s 与 4 s 退避、Retry-After 上限 5 分钟、渠道节奏上限）：等待（退避、Retry-After、节奏空位）不占用 worker 时，当前常量下为 2 × 301 + 768 × 10 + 769 × 60 = 54422 s，约 15.1 小时（256 批各 3 次尝试共 768 次、每次 10 s 超时；节奏 1 条/分钟时每次请求各占一个 60 s 窗口，排空开始前一分钟内的请求使窗口多一个；Retry-After 上限 300 s 计两次，每次因 not_before 按秒向上取整多等不到 1 s、按 301 s 计）；模型不计数据库耗时、排空期间新到达的事件与存储故障的退避，这三者出现时排空会更久，但都不改变"下限必须大于上界"这条的算法；若等待占用 worker，同样常量下约 44.8 小时，超过 24 小时下限——"等待不占用 worker"是这条下限成立的前提之一。事件清理不依赖上卷水位，上卷失败不阻止它；删除走 `alert_event(at)` 索引，不全表扫描。规则、渠道与状态不清理。

### 6.6 其余表

`node`（名称、排序、是否公开、备注、离线宽限期、流量重置日、token_hash，§9.4 的计费五列：价格、币种、周期、到期日、自动续期，以及 §4.9 的 `last_source`、`country`、`country_ip`、`country_pin`）、`node_facts`（facts_hash 与各静态字段）、`traffic`、`probe_task`、`probe_task_node`、`probe_meta`（任务版本号）、`alert_rule`（到期规则另有 `days_before`，其余种类为 NULL）、`alert_rule_node`（显式作用域；`alert_rule.all_nodes` 为真时不存行且覆盖全部节点，为假时无行表示不覆盖任何节点——删除作用域里最后一个节点不会放宽到全部）、`alert_rule_channel`、`alert_state`（到期规则另带进入 `firing` 时的到期日 `fired_expires_on`，恢复文案据它判断日期是否改过，§9.2）、`alert_event`、`alert_delivery`（每事件每渠道一行投递记录，`batch_id` 非空、同批各行共享尝试计数与结果，`not_before` 为下一次尝试的最早时刻；失败类别、HTTP 状态码与错误原文分列存放，同一次发送覆盖的多行共享 `batch_id`，见 §9.3）、`notify_channel`、`setting`、`admin`、`admin_session`、`api_token`（名称、token_hash、创建时间、最后使用时间）、`register_window`、`rollup_state`、`maintenance_state`（prune 与上卷的完成时刻，§6.5）、`tag`（名称）与 `node_tag`（节点与标签多对多，§10 的标签一条）、`theme`（id、name、version、清单里的 preview 路径——`GetThemePreview` 据它取图而不重新解析包、uploaded_at、enabled；"至多一个启用"由 `enabled = 1` 上的部分唯一索引承载）与 `theme_file`（§10.1）、`theme_package`（上传时的原包字节、本次写入的随机标识 revision 与是否已上传 uploaded：主题同步按 (theme_id, revision) 置已上传，覆盖或删除重装后标识必变，同步期间被覆盖的包不会被误标；不进任何快照层）、`restore_record`（§6.7）。

schema 版本记在 `PRAGMA user_version`，迁移为按版本号顺序执行的函数；空库直接建到当前版本，不重放历史。打开库时的 schema 策略由调用方显式给出：只有 `serve` 迁移旧库，每迁一步记一行日志（from、to），空库建成时也记一行；离线子命令（`passwd`、`token`、`stats`、`node`、`window`）打开比自己旧的库时拒绝并提示先用新版本 `serve` 升级（升级前备份）——否则运维用新二进制看一眼 `stats` 就把库单向迁走，旧 hub 下次重启起不来；两种策略下建空库都允许（没有旧数据可丢）——空库指没有任何对象的文件；有表却没有版本号、或版本号为负的文件不是本项目的库，拒绝打开而不是当作空库建表或当作旧库去迁（否则 `stats --db` 指错文件会往别人的库里建出全部表）；比二进制新的库都拒绝。

### 6.7 分层自动备份到 S3 兼容对象存储

hub 自行把数据推送到 S3 兼容对象存储（R2 为首选 endpoint），分两层、两个周期：配置与凭据分钟级（默认 5 分钟），指标与探测历史按天。运维配置一次，此后无需人工动作；恢复不自动化，是显式的运维操作。整套数据只在一个 SQLite 文件里，宿主盘损坏即丢掉全部历史与全部节点凭据，而手动导出的可靠性取决于人是否记得做。分层而不整库周期快照：500 节点的指标三级表约 2 GB，配置与凭据合计几百 KB，整库快照只能退到按天，全库 RPO 被最不值钱的那部分拖到 24 小时；分层后最痛的部分拿到分钟级 RPO。

分层判据两条各管一件事：进哪一层看"丢了能不能自愈"（指标丢了节点继续上报、过去是空洞，可按天；配置、凭据、累计流量、告警历史、标签、主题清单都不自愈，分钟级）；描述与被描述必须同层（`rollup_state` 与 `maintenance_state` 描述指标内容，跟指标层走：水位若比它描述的数据新，晚到的数据会按 §6.4 被丢弃）。配置层：`node`、`node_facts`、`traffic`、`probe_task`、`probe_task_node`、`probe_task_tag`、`probe_meta`、`alert_rule`、`alert_rule_node`、`alert_rule_tag`、`alert_rule_channel`、`alert_state`、`alert_event`、`alert_delivery`、`notify_channel`、`setting`、`admin`、`admin_security`、`api_token`、`tag`、`node_tag`、`theme`、`restore_record`；指标层：`metric_*`、`probe_*`、`rollup_state`、`maintenance_state`；主题产物（`theme_file` 与原包 `theme_package`）体量比配置层大三个数量级，不进快照层，按变更时备份：库里保存上传时的原包（`theme_package`，带本次写入的随机标识与是否已上传），每次上传或删除主题后唤醒配置层立即执行一轮完整备份。配置快照的同一读事务生成 `snapshot_theme(theme_id, sha256)` 引用清单，并将该视图中的原包逐个写入私有暂存目录；先按摘要上传缺失原包，再发布配置快照，任何原包上传失败都不发布该配置快照。摘要为空明确表示取快照时没有原包。主题对象不可变，替换或删除当前主题不会删除历史原包；当前不自动回收主题对象，避免删掉历史快照仍引用的内容。配置层每个周期也执行同样流程兜底；唤醒的完整一轮会多占用配置层的一份保留名额（换取最新的配置快照与主题对象一致），调试主题期间反复上传会缩短配置层可回溯的时间窗；升级前安装、没有原包的主题不算故障，同步跳过它并在备份状态里列出、提示重新上传；不备份 `admin_session`（重新登录即可，恢复它等于复活可能已登出的会话）与 `register_window`（限时限量，恢复旧窗口会复活已消耗的名额）。

每层快照都是单事务内的一致读：`ATTACH` 一个临时库，在一个读事务里逐表 `CREATE TABLE … AS SELECT`（每层都另带一份 `sqlite_sequence` 与 `snapshot_meta` 作簿记，`snapshot_meta.format_version=2` 独立于数据库 schema 版本——序列是 `node.id` 不复用那条不变式的载体，漏搬即失效，恢复时按表名取各来源的最大值），产物是可校验的 SQLite 文件，上传后删除临时文件；跨表分多次读会得到互相矛盾的配置。对象键 `<前缀>/config/<UTC 时刻>.db`、`<前缀>/metrics/<UTC 时刻>.db`、`<前缀>/theme/sha256/<SHA256>.zip`。保留按份数、hub 自删：配置层默认 48 份、指标层 14 份（可配），每次上传成功后列出该层对象、删除超出份数的最旧者；bucket 不得公开可读（内含口令哈希、token 哈希与全部拓扑，足以离线爆破弱口令）。不加密：保密由私有 bucket 与 TLS 承载。配置：endpoint、bucket、区域、access key、secret（只写不读，与渠道凭据同一做法）、前缀、两层周期、两层份数，存于 `setting`，经面板设置；未配置即整体关闭。周期与份数的取值范围：配置层周期 60–86400 秒、指标层周期 3600–604800 秒、两层份数各 1–1000；`UpdateSettings` 里这四个字段是 `optional`，缺席即不变（与 §10 总闸同一口径），给出的值出范围（含显式的 0）返回 `InvalidArgument` 并点名字段与范围——0 不表示"取默认"，份数为 0 会在上传成功后把这一层全删光，周期为 0 会空转，两者都不是任何人想要的配置；库里没有这个键时取默认值，回到默认就显式写默认值。上限只是防止把一层实际关掉而面板上看不出来：周期超过一天的配置层已经不是分钟级 RPO，要关就清掉 endpoint。S3 客户端只实现 SigV4 的 PutObject、ListObjectsV2、DeleteObject、GetObject，纯 Go；出站复用 §9.3 的出站边界——同一个不跟随重定向的传输、不含 URL 的错误文本与状态码判定——而不是它的总时限：期限经 ctx 逐次给出，配置层的上传按对象大小推导，指标层的上传按它自己的周期推导（周期减去保留预算，一次上传必须在下一次快照之前结束），列举与删除共用一个固定期限，客户端本身只限建连与首字节（同一个客户端既传几百 KB 的配置层也传 GB 级的指标层，固定总时限取长了配置层卡住要等满才失败、取短了大对象必然超时）；§9.3 的 64 KiB 应答上限不适用于下载与列举，两处各有自己的上限。这是架构里第一个 hub 主动出站且携带长期凭据的路径，与 §5.4 的出站目标清单同列：由运维显式配置、默认关闭、目标来自配置。

配置层上传失败必须告警，不得静默重试到下一周期——静默失效会让实际 RPO 无声退回 24 小时，失败与从未跑过在事后看长得一样：失败作为事件进 `alert_event`（规则与节点为 0，同 §5.3 登录通知的形态）投递到设置里选定的渠道（键 `notify.backup_channels`，`Settings` 里的形态与更新语义同 §5.3 的登录通知渠道：带 presence 的 message、缺席不变、显式空集合关闭、至多 16 个），同一故障只在首次失败与恢复时各发一条，首次失败的时刻随标记落库（与事件同一事务），重启后不重复发；故障类别除快照、上传、保留与设置读不出之外，还有标记读不出（坏标记先触发一次故障事件、再由覆盖写修复，上传正常即恢复——否则配置层永久卡住且没有接口能修）与启动准备失败（读回成功时刻或清理残留失败），都按配置层类别处理；坏标记不挡住指标层，启动准备失败期间指标层暂停；停用备份（endpoint 清空）即结束两层的故障跟踪并清除标记，已通知过的配置层故障以一条停用事件（transition `backup_disabled`）收尾，未通知过的不发；面板显示两层各自的上次成功时刻与当前故障。`GetBackupStatus`（只读口径）返回同一份数据。

恢复是离线子命令 `probe-hub restore --db <库> --config <配置层文件> [--metrics <指标层文件>] [--themes <目录>]`（新格式目录内使用从 S3 取回的 `<SHA256>.zip`，须匹配配置快照引用的 SHA256 与包内主题 ID；指定 `--themes` 时任何引用缺包或摘要不符都整体拒绝，不能用同 ID 的较新原包替代。历史格式没有摘要清单，仍按 `<id>.zip` 校验并保留旧格式的缺包停用行为。不带 `--themes` 是显式降级：全部主题保留清单但停用、清空预览与内容，缺包及忽略文件写入恢复摘要。所有包复用上传校验器，任一包非法时目标不动）；须在 hub 停止时进行（整表覆盖一个正在被写协程写入的库没有一致性可言，hub 的设置内存副本与内存索引也只在 Open 时加载，不会因库被改写而重载），需要确认，无终端时须 `--yes`：两层不对齐是常态，以配置层为准、不把配置回退去迁就指标层——否则分层的全部收益都被抹掉；差异只有一种可见形态：节点存在但在 `[指标层时刻, 恢复时刻]` 区间没有历史，与 §7 的断连空洞是同一个状态。恢复前校验来源：支持 schema 17 至当前版本，拒绝更旧、不支持及未来版本；来源以只读连接在同一事务中复制到私有临时目录，显式保留 `sqlite_sequence`，再按已审定的分层迁移升到当前 schema，不改来源文件。不能直接重放完整 live 数据库迁移，因为每层只有部分表；增加 schema 版本时必须同步补齐两层投影迁移。新旧快照均校验表齐全、页大小及格式版本，任一不符即拒绝。恢复目标内容在单事务内替换，失败不改变目标；已有管理员会话全部撤销，不从目标沿用授权。恢复后显式清理孤儿行，不依赖外键（`ON DELETE CASCADE` 只在 DELETE 时触发，恢复是整表覆盖；SQLite 的外键默认还不开启）：指向不存在节点的指标、探测、流量、标签关联行删除并记数，方向恒为"以 `node` 表为准"，与哪层更新无关；告警事件是审计历史，与删节点时一样保留（系统事件以 node_id=0 写入，按 `node` 表清理会把备份失败事件一起删掉）；"随节点一起消失的表"只有一份清单，删节点与恢复都遍历它，并有用例把它与库里所有带 node_id 列的表对齐、显式登记例外。恢复同时写入不投递的 `backup_restored` 系统事件，并落一条记录到 `restore_record`（两层各自的时刻、恢复时刻、清理了什么、主题摘要：恢复了 / 缺包 / 忽略了哪些）：空洞若无处解释，半年后没人能判断它是断连、回滚还是缺陷；这张表属于配置层（不自愈），恢复时与目标库已有的记录取并集而不是整表替换——否则从快照恢复本身会抹掉之前的恢复记录。恢复的目标库准入与建库和 §6.6 的离线命令共用同一份判定与同一行建库日志：目标库比二进制新、版本为负、有对象无版本号各报各的原因，恢复到新文件时放出 database schema created，写错 `--db` 才看得见。`node.id` 永不复用（`INTEGER PRIMARY KEY AUTOINCREMENT`，M1 起如此）是两层可以各自漂移的前提：若复用，指标层里 id=7 还是已删的旧节点，配置层里 id=7 已是新建的另一台，恢复后新节点的图表挂着别人的历史，不报错、不可由肉眼分辨。所以恢复不能让序列回退：`sqlite_sequence` 不属于任何一层的数据表，而是分配状态，每层快照都带一份（与 `snapshot_meta` 同为快照簿记）；恢复后每张 AUTOINCREMENT 表的序列取目标库现有值、配置层、指标层三者的最大值——配置层整表替换带回的是它取快照时刻的序列，节点在那之后还可能分配过，指标层与目标库各自见过更晚的分配；只有单调不降的序列能保证恢复后新建的节点不会拿到任何一层见过的 id。

不用 Litestream：它把 WAL 帧连续复制到对象存储、RPO 秒级，但要求没有别的东西 checkpoint WAL，嵌进同一进程意味着 hub 自己不得 `VACUUM`、必须关掉 `wal_autocheckpoint`，是一条靠"没人写那行代码"维持的不变式；其支持面是 CLI 而非库，S3 后端会把 AWS SDK 拖进 hub。条件若反转（库里出现丢一分钟都不可接受的账目类数据）再重新考虑，且走 sidecar 而非嵌入。

## 7. 流量累计

每节点一个内存累加器：`boot_id`、`last_rx`、`last_tx`、`total_rx`、`total_tx`、`period_rx`、`period_tx`、`period_start`、脏标记。

- 每次上报：若 `boot_id` 相同且计数器不小于基线，增量 = 计数器 − 基线，计入总量与周期量；随后基线 = 计数器。
- `boot_id` 变化、或计数器小于基线（网卡重置、32 位回绕）：只把基线重置为当前计数器，不入账。最多丢失开机到首次上报之间的流量；换来的是 token 被挪到另一台机器时，不会把那台机器开机以来的全部流量一次性记入。
- 计数器缺失（`optional` 未设置）：不入账也**不动基线**。"无读数"不是"读数为 0"，把基线改成 0 会让下一次上报的增量等于计数器全值。
- 总量用饱和加法，不回绕。
- 每 10 秒与退出时，把所有脏条目在一个事务里落盘，基线与累计值同写。

**崩溃不变式**：基线与累计值总是同事务持久化，所以崩溃后的首次上报相对"已落盘的基线"做差分，恰好覆盖崩溃丢失的内存增量，不重不漏。前提是其间 `boot_id` 未变；若节点恰在此窗口内重启，窗口内的流量不可知，按上一条规则重置基线。

**图表与总量的关系**：断连恢复后的首个增量覆盖整个断连区间，若计入单个分钟桶会在速率图上形成假尖峰。因此当距该节点上次上报的间隔超过 TTL 时，该增量只计入 `traffic` 的总量与周期量，不计入 `metric_1m`；hub 重启后各节点的首次上报同样处理（上次上报的时刻已不可知）。由此，`metric_*` 的 `rx_bytes` 对时间求和等于同区间总量的增量，这条等式只在"节点连续在线、hub 未重启、且区间内的分钟行都成功落库"的区间上成立；断连区间在图表上本来就是空洞。

周期用量按节点的重置日（1–28）滚动，时区由 hub 的 `--timezone` 决定；滚动在入账与落盘路径上判定：`now` 不早于 `period_start` 之后的首个重置日零点时，周期量清零、`period_start` 推进到该零点。重置日经 `UpdateNode` 修改。

**读写接口**：`GetTraffic` 返回全部节点的总量、周期量、`period_start` 与下次重置时刻；`GetSnapshot` 的每个节点状态也带周期量与总量，实时视图不需要第二条轮询。`AdjustTraffic(node_id, period_rx, period_tx)` 把当前周期的两个用量覆盖为给定值（把面板对齐到云商计费口径的那一次操作），总量按同一差值同步调整且不低于 0；基线不动，之后的增量照常叠加；内存条目与库在同一把锁下同步写入，不经过 10 秒刷出。

agent 默认汇总除回环与虚拟网卡外的全部网卡（Linux：`lo`、`docker*`、`veth*`、`br-*`、`virbr*`；darwin：`lo*`、`gif*`、`stf*`、`utun*`、`ipsec*`、`bridge*`、`vmenet*`、`awdl*`、`llw*`、`anpi*`、`ap*`——回环，以及字节同时计在物理网口上或不出本机的接口），可用 `--net-include` / `--net-exclude` 覆盖。进程数两个平台都按进程计：Linux 数 `/proc` 下的进程目录（不用 `/proc/loadavg` 第 4 字段——那是含线程的调度实体数），darwin 用 `proc_listallpids`。`/proc` 以 `hidepid=2` 挂载时非 root 的 agent 只看得到自己的进程：同一次目录列表里必须有名为 `1` 的进程目录，没有就让进程数缺失并记日志，不上报一个错的小数。合计型读数（连接数、流量、进程数）的口径都是要么正确要么缺失：成员读不出就整个缺失，不给缺一半的合计；网卡在列出与读取之间消失（ENOENT）只略过该网卡。

## 8. 探测

### 8.1 任务与版本

`ProbeTask{id, kind(icmp|tcp), target, interval_s, timeout_ms}`，通过 `probe_task_node` 分配到节点。经管理接口对任务或分配的任何修改都经由 `probe` 包内唯一的写入口，在同一事务内把 `probe_meta.version` 改为 `max(version + 1, 修改时刻的 Unix 秒)`：严格递增，且库从备份恢复后重做编辑得到的值大于 agent 从旧库拿到的值——收敛条件是重做时的 Unix 秒大于 agent 持有的旧版本值（同一秒内多次编辑会把版本推到秒数之上）；同秒重做或时钟回拨到该值以下仍可能碰撞，需重启 agent。agent 只比较相等与否。删除节点顺带删除它的分配行不改版本——版本号的用途是让清单变化的 agent 重取，被删节点的 token 已撤销、其余节点的清单未变。版本全局唯一而非每节点一份：修改是管理员的低频动作，全体 agent 各多取一次列表的代价可以忽略，换来的是不需要维护"哪些节点受这次修改影响"的推导。

探测任务可声明作用于全部节点（`ProbeTaskDetail.all_nodes`，管理端字段；agent 只拿展开后的清单）。语义与 `alert_rule.all_nodes` 完全一致：为真时不存分配行、覆盖全部节点，之后新建的节点自动纳入；为假时空分配集不覆盖任何节点，删掉最后一个分配不放宽——同一形状的开关只有一种语义。每节点任务数上限（§8.4 的 64）在保存任务与建节点两处都校验：新建节点会继承全部 `all_nodes` 任务，超限时建节点失败并说明。建节点（面板的 `CreateNode` 与 agent 的自助注册共用同一入口）必须推进任务版本：版本的不变式是"任何一个节点的清单变了，版本就变"，新节点的清单从空变为全部 `all_nodes` 任务；agent 只比较相等，不能指望它恰好持有别的值（刚启动的 agent 报 0，编辑过任务的 hub 版本不小于 Unix 秒，两者不等只是巧合，不是机制）。删除节点仍不推。`ListProbeTasks` 对 `all_nodes` 任务回显当前展开的节点列表并带开关，面板与 agent 都不必自己展开；公开端的任务标签规则不变：`all_nodes` 任务对每个公开节点都算"当前分配"。

展示顺序单独保存在 `probe_task.sort_order`。`ReorderProbeTasks` 只接受全部现存任务 ID 的完整排列，在写事务中拒绝重复、缺漏和未知 ID；新任务追加到末尾，编辑保留顺序。管理清单、管理端和公开端历史使用同一顺序，已删除任务的历史排在现存任务之后并按 ID 排列。重排不改变任务内容或分配，因此不推进 `probe_meta.version`；agent 清单仍按 ID 下发，展示偏好不改变执行配置。

### 8.2 执行

- ICMP：优先用非特权数据报 ICMP socket；不可用且进程持有 `CAP_NET_RAW` 时退到 raw socket；都不可用则每次回报 `error`，面板显示原因，而不是静默呈现为 100% 丢包。
- TCP：连接建立耗时即 rtt，解析在计时之前完成。
- 丢包与 `error` 的分界两种探测共用一句口径：这一次没有联通是可达性事实，计入丢包（超时、连接被拒或重置、网络或主机不可达）；本地无法发起才是 `error`（无 socket、解析失败、地址非法、fd 耗尽、权限）。
- 名字的地址族按本机可建的 socket 选、v4 优先、不看路由；仅 IPv6 的主机上双栈名字会选到 v4，是当前的已知限制。
- 各任务的首次触发时刻加随机偏移，避免同一时刻齐发。
- ICMP 实现用 `golang.org/x/net/icmp`（纯 Go，与 hub 已依赖的 `x/crypto` 同源；§2 的"零第三方依赖"说的是 /proc / /sys 采集）。启动时探测两种 socket 的可用性并写入 `Facts.icmp_available`。每个地址族一个共享 socket，单读协程按 payload（进程 nonce + task_id + seq）把回包分发给等待中的探测；不按 ICMP ID 匹配——Linux 数据报 socket 的回包 ID 被内核改成本地端口，macOS 的公网回包 ID 也会被改写；读侧只接受 Echo Reply，raw socket 与 macOS 的 udp6 会先读到自己发出的 Echo Request，macOS 同进程的数据报 socket 之间会互相收到对方的回包（§13 第 2 项的实验结论）。
- 结果进有界队列，上报时整体取走并按单调钟折算 `age_ms`；队列满时丢最旧的并计数，不阻塞探测协程。任务集更新时停掉消失的任务、启动新增的任务，未变化的任务不重启计时。

### 8.3 对账与入库

版本在 hub 进程内缓存，由 `probe` 包的写入口在事务提交后更新；`Report` 只比较两个整数，不一致时响应携带该节点的 `ProbeTasks`（版本 + 分配给它的任务）。

结果逐条校验后才折叠进内存桶：`task_id` 必须分配给本节点（否则丢弃并记日志——token 被挪用或分配已撤销的旧结果不得写进别的任务的历史）；`age_ms ≤ MAX_AGE`（§6.4 第 3 条）；测量时刻 = 收到时刻 − `age_ms`。桶键是 `(node_id, 分钟, task_id)`：`sent` 每条加一，`timeout` 计入 `lost`，`error` 计入 `errors`，`rtt_us` 累加到 `rtt_sum` 并更新 `rtt_min` / `rtt_max`。刷出、加法合并、冻结检查与 §6.2 的指标桶共用同一条路径。删除任务不删已有历史，到期由 prune 清理；`QueryProbes` 对这些行只带 `task_id`，不再有类型与目标。

`QueryProbes(node_id, from, to, max_points)` 与 `QueryMetrics` 同一套选级与对齐规则，按任务返回序列，每个点是 `ProbeSample{ts, sent, lost, errors, optional rtt_mean_us, optional rtt_min_us, optional rtt_max_us}`；`sent = 0` 的桶不出样本；rtt 三项只在 `sent − lost − errors > 0` 时存在，缺失由 `optional` 表达而不是零值（平行数组无法表达"这一点没有 rtt"）。丢包率 = `lost / sent`，`errors` 不计入丢包。任务管理经 `ListProbeTasks`（含分配节点）、`SaveProbeTask`（id 为 0 即创建，提交整份分配列表）、`DeleteProbeTask`。

### 8.4 agent 侧硬限制

agent 强制执行、hub 侧同步校验（两侧各有断言）：探测间隔 ≥ 5s、任务数 ≤ 64、单次探测 1 个包、超时 ≤ 5s。超限的任务 agent 直接丢弃并回报 `error`。目的是 hub 失守时，攻击者无法把全部节点变成扫描器或流量反射器。

速率上限约束的是量，目标地址另由 agent 的本地策略约束（§5.7）。agent 在解析之后、发包之前检查实际要连的地址，默认拒绝本机、链路本地（含云厂商的 metadata 地址）、组播与广播：`0.0.0.0/8`、`127.0.0.0/8`、`169.254.0.0/16`、`224.0.0.0/4`、`255.255.255.255/32`、`::/128`、`::1/128`、`fe80::/10`、`ff00::/8`，以及落在私网段里的两个 metadata 地址 `100.100.100.200/32`（阿里云）与 `fd00:ec2::254/128`（AWS Nitro 的 IPv6 端点）。宿主机自己接口上的地址每次探测前重新枚举（地址随 DHCP、网卡增删而变），固定集之外的每一个按满长前缀并入默认拒绝：连本机的非回环地址走本地路由，常能绕过只挡外部入站的防火墙，与回环同理；已落在固定集里的（lo 上的 `127.0.0.1`、链路本地地址）仍由固定前缀管辖，否则 `probe_allow` 写 `127.0.0.0/8` 放不行回环。枚举失败时拒绝这次探测。私网默认允许，因为内网互测是常见用法；节点探测自己的地址（例如分配给全部节点、目标是某个节点地址的任务）在该节点上回报 `error`。宿主机可以用配置里的 `probe_deny` 追加拒绝、用 `probe_allow` 放行，两者都是规范形式的 CIDR 列表（`netip.Prefix` 已按掩码归一，写成 `10.1.2.3/8` 这类非规范形式是配置错误）；IPv4 映射段（`::ffff:0:0/96` 之内）的前缀也是配置错误，报错给出等价的 IPv4 写法：目标先还原成 IPv4 再比较，这样的前缀永远匹配不到，写在 `probe_deny` 里就是静默放行。固定默认集之外的宿主机接口地址只有写出同一个满长前缀才放行，更短的放行前缀（如整个私网段）不覆盖它；落在固定集里的本机地址（回环、链路本地）随固定前缀，写出与之等长或更长的放行前缀即可。裁决取最长前缀匹配；前缀等长时本地配置优先于默认值；同一前缀同时出现在两个列表里是配置错误，`run` 与 `configure` 都拒绝。IPv4 映射的 IPv6 地址先还原成 IPv4 再判断；其余内嵌 IPv4 的 IPv6 形态（IPv4 兼容 `::a.b.c.d`、NAT64 `64:ff9b::/96`、6to4 `2002::/16`）按 IPv6 地址判断，它们到达的是转换器或隧道，Linux 上是否会被送往本机未实测。被拒的任务照常调度，每次回报 `error`，写明地址与命中的前缀，hub 面板据此显示原因。名字解析出的首选地址被拒时不换用其他地址，否则攻击者可以借 DNS 挑选。检查放在 TCP 与 ICMP 共用的解析入口里，今后新增的探测种类经过同一个入口。

## 9. 告警

### 9.1 规则

- 离线：节点超过宽限期未上报。宽限期按节点可配，下限为 TTL（§4.4），由保存规则时的显式校验承载：宽限期短于 TTL 会在面板仍显示该节点在线时发出离线告警，两处读的是同一个 `last_seen`，口径必须同向。
- 探测：某任务在某节点上的丢包率或平均 rtt 连续 N 分钟超过阈值。数据源为 `probe_1m`。
- 到期：节点的到期日距今不超过 `days_before` 天（1–365，含已过期的负数）。数据源为 `node` 的到期日（§9.4）；没有到期日的节点不参与。规则的探测专用字段（任务、指标、阈值、持续分钟）对离线与到期规则都必须为零，`days_before` 对离线与探测规则必须为零，由 `CheckRule` 显式拒绝（`InvalidArgument`）；保存入口与 Load 路径共用它。此前离线规则带探测字段会被存储层静默清零，这条检查随到期规则一起补上——静默清零是放宽方向，调用方发了什么、存下的却是零值，无从察觉。

### 9.2 状态机

资源规则使用 `memory_used_pct`、`disk_used_pct` 的分钟均值。比例在同一次原始采样中计算后再聚合，不以已用量均值除以另一时刻容量。触发阈值在 (0,100]，恢复阈值在 [0,触发阈值)，连续 1–60 个完整分钟达到触发阈值才触发；连续相同长度窗口不高于恢复阈值才恢复。滞回区间和缺失读数均不能令 firing 恢复。分钟落库之后统一评估探测与资源规则。

每（规则 × 节点）一个状态：`ok → pending → firing → ok`，进入 `firing` 发告警通知，回到 `ok` 发恢复通知。状态持久化在 `alert_state`，hub 重启不会重复触发，也不会忘记尚未恢复的告警。

离线规则每 10 秒巡检；探测规则在分钟桶刷出后评估。离线的恢复条件是收到一次上报（上报本身即证明）；探测的恢复条件是连续 1 分钟低于阈值。离线的 `pending` 是"未上报已超过 TTL 但未到宽限期"（面板已显示离线、告警尚未发出）；探测的 `pending` 是最近一分钟超阈但尚未连续 N 分钟。探测规则在某分钟没有数据时保持当前状态：缺数据既不是超阈也不是恢复。

离线告警的抖动抑制：一条规则×节点从超过宽限期的离线恢复后，在一小时的窗口内再次离线，这次离线的宽限取 `max(节点宽限, 30 分钟)` 才进入 `firing`。两个数是常量，不按节点或规则配：抖动宽限的作用是压噪声，与节点的正常宽限是两个量。窗口按这次离线开始的时刻判定——离线开始即最后一次上报的时刻，跨重启取落库的最后上报时刻——同一次离线在整个 `pending` 期间用同一个宽限，hub 重启也不换。窗口严格大于抖动宽限是取值约束：状态机不比较两者（宽限按离线开始时刻一次定下，之后不再看窗口），判定不读两者的大小关系，改动其中一个数带来的变化只来自被改的那个数本身；改反只会让"窗口内再掉"与"抖动宽限"两个量失去各自的含义；只有节点没有任何上报落过库、离线开始退回启动时刻的那条路径上，"恢复后立刻再掉的抖动宽限不被重启打断"才靠这条关系保证。抖动判定只看这条规则×节点自己的历史：上次从 `firing` 恢复的时刻记在 `alert_state.recovered_at`（一段历史而非当前状态的属性：恢复转换写当下时刻，其余转换沿用现值，否则再次离线的 `pending` 一写就把窗口抹掉；非离线种类恒为 NULL），hub 重启不丢窗口。恢复通知照发——它是 `firing` 的配对事件，省略会让面板"已通知"与实际不对；`pending` 与面板显示不受影响，面板仍按 TTL 显示离线，抑制只推迟 `firing`；告警规则页的状态列把正因抖动宽限而推迟的 `pending` 标为"抖动中"（不落库，hub 重启后第一轮巡检之前不标）。探测规则不做同类抑制：`for_minutes` 已承担同样作用。

到期规则没有 `pending`：它是日历事件，不存在"持续多久才算"。剩余天数 ≤ `days_before` 直接 `firing`，续期把日期推出窗口或清空到期日即 `ok` 并发恢复通知；节点没有到期日按恢复处理。评估时机四处：hub 启动一次；每个 hub 时区的零点一次（按时区算出下一个日界再定时；零点不存在的日子——夏令时从零点开始的时区——下一个日界是新的一天的第一个时刻（01:00，新偏移）；`time.Date` 对不存在的零点的归一方向随 UTC 偏移的正负而异：偏移为负（America/Santiago、America/Havana）往回到前一天 23:00、本地日期没变，拿它定时会在旧的一天里空转，所以按本地日期是否已变判断，没变就取该时刻所在时段的结束处；偏移为正（Africa/Cairo、Asia/Beirut）往前到新一天 01:00，本身就是新一天的第一个时刻；hub 停机跨过多个零点由启动那次补上）；`UpdateNode` 改了任一计费字段后立刻一次——否则续费后要等到零点才恢复；保存到期规则（新建、启用、改 `days_before` 或作用域）后立刻一次——否则新规则要等到零点才有状态。日界循环里的扫描（含启动那次）出错（续期写回或状态写失败）时不等到下一个日界：按 1 分钟起、每次翻倍、上限 1 小时的退避重扫，成功即回到日界节奏；退避未到而日界先到就按日界扫。否则零点一次写库失败会让开着自动续期的节点整天显示"已过期"，而离线巡检与投递（§9.3）都有各自的重试节奏。同一次扫描先做自动续期的推后再评估规则（§9.4），推后与恢复在一次扫描里完成。一条规则对一个节点只提醒一次：进入窗口时按当时的状态写 `节点 X 将于 2026-10-01 到期（剩 4 天，规则 R）` 或 `节点 X 已于 2026-09-20 到期（已过期 7 天，规则 R）`，从"将于"走到"已于"不再发第二条（`days_before` 最小为 1，没有"恰在当天"的写法；到期当天写"剩 0 天"）。恢复文案按离开窗口的原因：到期日改了写 `节点 X 到期日已更新为 2026-11-01（规则 R）`，清空写 `节点 X 已清除到期日（规则 R）`，日期没变写 `节点 X 已不在提醒窗口内（规则 R）`（通常是规则的提前天数调小了；换时区或墙钟回拨也会）。"日期没变"与进入 `firing` 时记在 `alert_state` 里的到期日比：告警事件按保留期删除，而已过期的节点可以一直 `firing`，只有状态表能可靠地带着这个日期。事件的 `value` 是剩余天数（清空到期日而恢复时为 0）。`days_before` 与阈值、持续分钟同类，不是规则身份：改它保留状态，下一次扫描按新值判断。库里读不懂的到期日跳过评估、保留状态、不推后、`days_left` 缺失并记一行 Warn。扫描读快照之后才提交的 `UpdateNode` 不在本次快照里，本次可能按旧值多发一对触发与恢复，那次 `UpdateNode` 自己再扫描即收敛；续期写回是条件更新（只在库里的值仍等于计算所依据的值时写），不会盖掉快照之后的修改。

**重启不变式**：hub 重启后 `live` 为空，所有节点看起来都未上报。对本次启动以来尚未上报过的节点，离线时长从 hub 启动时刻起算（单调钟）；已上报过的节点从 `live` 的 `last_seen` 起算。由此重启后每个节点都获得完整的宽限期，重启本身不会触发离线告警。重启前已处于 `firing` 的告警保持 `firing`，直到该节点再次上报才恢复；重启前处于 `pending` 的从启动时刻重新计时。

落盘的 `last_seen`（墙钟，随分钟行刷出与退出时写入）不参与离线时长的计算——它必然早于启动时刻，拿它与启动时刻取较大值没有意义；它供面板显示"最后在线于"与告警文案，并在重启后作为这次离线的开始时刻参与 §9.2 抖动窗口的判定。

### 9.3 通知

渠道：Telegram、通用 Webhook（可配方法、头、请求体模板）。投递走有界队列（单 worker），按批次投递：每个批次至多 3 次尝试、退避 1 s 与 4 s，同批各行共享尝试计数与结果；等待（退避、Retry-After、渠道节奏空位）不占用 worker，窗口（就绪、等待与在途的批次合计不超过容量）满时新批次留在库里等补货，入队时先挤出最旧的就绪批次，其次挤节奏等待的批次，再次挤已把等待时刻落库的重试批次，结果尚未落库的等待不挤，被挤出的批次由补货装回；每次可重试的失败都把下一次尝试的最早时刻（`not_before`，按秒向上取整）随结果落库，补货与重启后读批次时遵守它；每次发送前先落盘一次尝试计数，且这次尝试只覆盖拼消息时读到的那组行（行集合在开始尝试的事务里变了就拒绝、重读重拼），落盘失败就不发送，因此即使发送后的结果未能落盘（崩溃、写失败或关停时取消），同一批的发送次数也不超过上限，也不会有行被记成已送达却不在发出的消息里；HTTP 4xx（除 408、429）是永久失败不重试；出站客户端不跟随重定向（3xx 当失败，凭据不随跳转外泄），响应体只读前 64 KiB。每次尝试的结果写入 `alert_delivery`；未成功且未耗尽次数的投递在 hub 重启后重新入队；运行中被挤出有界队列、或因存储故障未能记下结果的投递，由投递协程从库中补回，存储故障期间按 1 s 起、上限 60 s 的退避重试。面板显示的"已通知"只来自成功的投递记录。登录通知（§5.3）复用渠道与投递记录，事件的规则与节点为 0。系统事件（登录、备份）各有专用的 transition（`login_success`、`login_locked`、`backup_failed`、`backup_recovered`、`backup_disabled`），事件的种类由 transition 决定，读侧（事件列表的标签与标红、投递摘要、面板）不从 `rule_id`/`node_id` 为 0 反推：0/0 只是"不属于任何规则×节点"，分不出是登录还是备份，也分不出同一系统的失败与恢复；规则事件继续用 `firing`/`recovered`，系统事件不复用它们。

合并与节流：同一渠道在同一评估周期内产生的多个事件合成一条消息发送，合并只发生在投递层，事件层不变——`alert_event` 仍每个规则×节点一行，事件是状态机的事实记录，合并是发送策略。合并的键是（渠道，评估周期，规则，转换方向）：同一规则的多个节点同时 firing 合一条，firing 与 recovered 不混（一条消息里既有掉线又有恢复读不出结论）。合并窗口有上界（一个离线巡检周期或一次探测评估），不为了等更多事件推迟发送。一条消息列出的节点数有上限（20），超出部分写"另外 N 台"：Discord 限 2000 字符、企业微信限 2048 字节，全部节点同时掉线时消息必须仍能发出。`alert_delivery` 仍每事件每渠道一行，同一次发送覆盖的多行共享一个发送批次标识（`batch_id`），结果一起写；重试按批次进行，不把一个批次拆成多条重发；重启后未完成的批次按原批次重投，不重新合并——合并结果已经落盘，重启不改变"发了什么"。按渠道种类决定是否合并：Telegram 合并，Webhook 不合并（接收方多是机器，一次请求一个事件的请求体模板不变；429 的问题主要在 IM 渠道）。每渠道有出站节奏上限（每分钟消息数，渠道配置的一部分，Telegram 默认 20——群聊的文档值），超出的批次排队而不是丢弃，队列仍有界；上限属于接收方而不是 hub，不同渠道不同。429 的 `Retry-After` 纳入退避（只看 429，上限 5 分钟，按批次持久化"不早于"时刻，结果写库失败与重启都不丢它）：固定的 1 s 与 4 s 短于 Telegram 常见的几十秒 `retry_after`，多数投递会以失败告终。动因：hub 侧网络抖动或反代重启会让几十个节点在同一个巡检周期里一起过宽限期，四十个节点就是四十条 Telegram 消息，超过群 20 条/分钟即 429。渠道凭据存库不回显：Telegram bot token、Webhook 的 URL（入站 webhook 的 URL 本身常是密钥）与全部头值都只写不读，列表只回显主机名与头名；保存时省略即保留旧值。hub 自己生成的出站错误文本（连接、DNS、TLS、超时、请求构造的错误）同样不含 URL；HTTP 失败的原文是接收方的响应体，见下一段。Webhook 的目标地址不做限制（含回环与内网地址），管理员因此能让 hub 向其网络可达的任意地址发请求；这是单管理员模型接受的边界，需要隔离时在网络层限制 hub 的出站。

投递失败按类别记录，类别在产生失败的地方确定，不从错误文本反推：`http_status`（接收方以非 2xx 应答，另记状态码）、`transport`（没有收到合法应答：连接、DNS、TLS、超时，以及状态码不在 100–999 的应答）、`request`（请求没能构造：模板执行、编码、URL）、`channel_invalid`（渠道配置无法解析）、`channel_deleted`（渠道已删除，投递终止）、`result_unrecorded`（次数耗尽而最后一次结果未落盘）、`unclassified`（失败没有携带类别；迁移时无法从旧记录确定类别的也归入此类）。只读口径的 `ListAlertEvents` 只返回类别与状态码；错误原文——HTTP 失败时是响应体的前 200 个字符，其余是出站错误文本——只经仅会话的 `GetAlertDeliveryError` 读出：接收方可能在错误响应里回显收到的请求体，而请求体模板里可能放着密钥（§3.2）。hub 不把 URL 写进原文，但响应体的内容由接收方决定：接收方若回显请求路径或头值，会话用户也能从原文里看到这些本应只写不读的配置，这在会话的权限之内。`channel_deleted` 与 `result_unrecorded` 没有原文；`result_unrecorded` 不沿用更早一次尝试的失败——最后一次尝试已发出而结果未知，接收方可能已经收到。投递成功时类别、状态码与原文一并清空。

### 9.4 节点计费与到期

节点可记录费用与到期日，供运维知道每台机器花多少钱、什么时候续费；到期前由 §9.1 的到期规则提醒。价格与币种是提醒用的展示值，不是账目：不汇总、不换算、不参与任何计算；到期日与周期只驱动 `days_left`、自动续期与到期规则。`FEATURES.md` 里"引入计费"那条 Litestream 的重新考虑条件不因它成立。

| 字段 | 存法 | 校验 | 空值含义 |
|---|---|---|---|
| 价格 | TEXT，十进制文本如 `12.50` | `^\d{1,9}(\.\d{1,2})?$` | 空 = 未填。展示值不是计算值，所以既不用浮点也不用最小货币单位 |
| 币种 | TEXT | `^[A-Z]{3}$`（ISO 4217） | 空 = 未填；价格非空时币种必填，币种非空价格可空 |
| 周期 | TEXT 枚举：月、季、半年、年、两年、三年、五年（`BillingCycle`，在 `types.proto`，公开与管理端共用） | 枚举值之一，对应 1/3/6/12/24/36/60 月 | 空（`UNSPECIFIED`）= 无周期（一次性或未填）；自动续期要求非空 |
| 到期日 | TEXT `YYYY-MM-DD` | 合法日期 | 空 = 无到期；到期规则与自动续期都要求非空 |
| 自动续期 | INTEGER 0/1 | — | 默认关。关是缺省不是放宽：到期后显示已过期、告警保持，直到管理员改日期 |

五项收在 `Billing` 子消息里（`types.proto`），`Node` 与 `UpdateNodeRequest` 都以它承载；`days_left` 也在其中，保存请求里的值忽略。校验全部在 `UpdateNode` 一处裁决，与 `traffic_reset_day`、`offline_grace_s` 同一个函数、同一格式的错误文案；另加"自动续期开着时周期与到期日都必须非空"。`UpdateNode` 是整体替换语义（缺省值即清除）：`billing` 缺失等于五项全清。

日期按天计，天的边界用 `--timezone`（§5.4，与流量周期同一个时区）。剩余天数 = 到期日 − 今天，由 hub 算好作为 `days_left` 下发（`Node` 与 `PublicNode` 都带；无到期日时缺失；负数即已过期天数），面板与公开页只显示它，不在浏览器里用本地时区再算一次——跨时区的访客会看到差一天的数字。

自动续期：扫描时对开着自动续期且到期日早于今天的节点，`while 到期日 < 今天: 到期日 += 周期月数`（1/3/6/12/24/36），落库并记一行日志。日号超过目标月天数时钳到月末（1 月 31 日 + 1 月 = 2 月 28/29 日），钳过之后日号停在钳后的值不再回到 31：接受这个漂移，它是提醒日期不是账单日期。周期为空的节点不推后（保存入口已拒绝这种组合，扫描里再守一次，不依赖入口）。

公开：`PublicNode` 带 `PublicBilling`，由 `Billing` 按 §10 的投影规则生成（投影为此扩展到枚举字段：两侧必须引用同一个枚举类型），价格、币种、周期、到期日与 `days_left` 同号放行，自动续期是运维开关，号与名都 reserved——对不齐在构造期就 panic，与 `PublicFacts`、`PublicMetrics` 同一机制。Agent 侧协议不涉及这些字段。

## 10. 前端与公开页

- `web/` 内两个 Vite 入口：`/admin/*` 管理面板，`/` 公开页，各自打包。测试扫描公开入口的 import、构建产物按描述符前缀核对，禁止公开页引用 `AdminService` 的生成客户端；这是卫生措施，安全边界在 §3.2 的服务端挂载。
- 实时数据用轮询（默认 2 秒）。`PublicService.GetSnapshot` 一次返回全部公开节点的实时状态，hub 对序列化结果缓存 1 秒：匿名访客数量不影响 hub 的序列化开销。
- `--public-dir <dir>` 用指定静态目录替代内置公开页，未命中文件时回落到该目录的 `index.html`。`/admin` 与 RPC 路径的路由优先级更高，替换目录无法遮蔽它们。文件访问经 `os.Root`，不可越出目录、不跟随指向目录外的符号链接。目录与面板同源：里面的脚本能读面板、也能带着来访管理员的会话调管理接口，所以只放与 hub 二进制同等可信的内容（flag 帮助写明）。
- 外观设置（明暗、主色、logo、标题、自定义 CSS）存于 `setting`，经 `PublicService.GetSite` 下发并以 CSS 变量应用。只接受 CSS，不接受 JS 或 HTML；需要改结构的人使用 `--public-dir`。
- 公开页总闸：`Settings.public_enabled`（`optional bool`，键 `site.public_enabled`，从未保存过时为开）。`UpdateSettings` 对外观字段是整体替换、缺席即内置值，对这一项（以及 §4.9 的国家查询开关与服务地址、§5.3 的登录通知渠道、§6.7 的备份通知渠道）缺席表示不变：把公开页关掉是一次对外可见的中断，不知道这个字段的老客户端与脚本改个标题不得顺手把它关掉，所以缺席不能等于 false。由此 `UpdateSettings` 的请求按组判定，各组彼此独立：外观五项是一组，任一项非空即视为给出，整体替换并按整体校验（theme 必填，其余为空即清空）——proto3 的 string 没有 presence，分不开"没给"与"给了空串"，按任一项非空判定能让只带 title 不带 theme 的请求得到点名 theme 的错误，而不是被静默丢弃；总闸、国家查询两项、登录通知渠道、备份通知渠道各自是一个 presence 组，给出即改、缺席即不变；一次请求至少给出一组，否则 InvalidArgument 点名各组——只改总闸或只改国家查询的脚本不必重发外观，这正是 presence 语义存在的理由。关闭时 `PublicService` 全部方法返回 `NotFound`——包括 `GetSite`，否则站点标题与 logo 仍会泄漏；`/` 与公开页的前端路由（按公开页静态服务同一条回落规则：`assets/` 之外的路径）都返回"公开页已关闭"的说明页——分享出去的节点页链接要能看出是站点关了而不是链接失效；`assets/` 下 404；说明页带内置页同一套安全头。内置公开页与 `--public-dir` 一样受总闸约束，`/admin` 与 RPC 路径不受影响；主题 origin 上 RPC 之外的整个静态面（主题文件与回落的内置页）同样受约束，关闸时不读库、不服务任何主题文件——主题文件与 `--public-dir` 同属替换公开页结构的一档，关闸的人期望整个公开面消失，主题自己的报错页不是"公开页已关闭"。总闸与节点的 `public` 是与的关系：关闭时逐节点设置原样保留，重新打开即恢复；只想把 hub 当内部工具的人不必逐个取消节点公开。拦截器读设置的内存副本，不查库；关闭后 1 秒内快照缓存仍可能命中，与节点改私有的语义相同；限流仍生效，关闭后的匿名请求照样计入令牌桶。
- 节点标签：一个节点可挂多个运维自定义的标签（`tag`、`node_tag` 两张表，节点与标签多对多；`Node.tags` 随 `ListNodes` 回显，`UpdateNode` 整体替换标签集合），面板的节点列表可按标签过滤。多选过滤取交集：只保留同时拥有所选全部标签的节点（`GROUP BY node_id HAVING COUNT(DISTINCT tag_id) = 所选标签数`）；空选择不过滤、返回全部——空条件匹配一切，与"空即拒绝"方向相反，这一分支显式写出；过滤条件按折叠去重后超过 16 个（超过每节点上限的交集必然为空，同时也是 SQL `IN (…)` 参数个数的上界）或含不合法的名字，返回 `InvalidArgument` 而不是空结果——空结果会把写错的条件伪装成"没有这样的节点"。`ListTags` 列出全部标签与各自的节点数（`ACCESS_READ`），`DeleteTag` 按名字（折叠后）定位：协议里标签的身份就是名字，`Node.tags`、`UpdateNode` 与过滤条件都只用名字，不再暴露一套 id。删除标签只解除关联，不影响节点。命名：去掉首尾空白后 1–64 个字符、不含控制字符、允许空格与大小写混写；同一名字按 Unicode 简单折叠大小写不敏感去重（`db` 与 `DB` 是同一个标签，沿用先建的写法），每节点至多 16 个。标签随公开节点公开：`PublicNode.tags` 是该节点的全部标签名（先建的写法，按折叠排序），快照只含 `public = 1` 的节点，私有节点的标签因此不出现；没有单独的“标签是否公开”开关，标签常写用途与归属（`db`、`客户A`），挂在公开节点上即对外可见，这是运维的显式取舍。公开页按标签过滤纯前端、不动协议与存储：标签栏照 `PublicSnapshot.tags` 渲染：它是快照里各公开节点标签的并集，由 hub 按折叠键（`name_fold`）排序、与 `ListTags` 同序，页面不自己汇总与排序——折叠规则只在 hub 一处实现，页面复刻它的顺序只能近似（按小写比较会把 `_x` 排到 `Ab` 前面，折叠键里 `A` < `_` < `a`）；选择集为空不过滤、显示全部（空条件匹配一切，这一分支显式写出）；单击标签变为只选这一个（当前恰好只选它时清空、回到全部），Shift+单击在其余标签状态不变的前提下翻转它；多选取交集，与面板一致；实际生效的选择集是所选与当前快照里仍存在的标签的交集，标签在轮询后消失不会留下看不见的过滤条件；有过滤时不带标签的节点不显示；顶部在线计数按过滤后的节点算。公开页另有两个独立的纯前端开关，默认都关（关着时既不过滤也不重排，即显示全部、面板的手动顺序），与标签过滤叠加，先过滤后排序：“仅离线”只留 `online` 为假的节点；“按到期时间排序”从早到晚，已过期的最前，没有到期日或到期日无法解析（`days_left` 缺失）的最后，到期相同的保持面板顺序——排序键取 hub 下发的 `days_left` 而不是到期日字符串：同一份快照里各节点的 `days_left` 出自同一个 today，按它排就是按到期日排，且浏览器不自己按本地日期重算。过滤把节点滤空时说明“没有符合筛选条件的节点”。探测与告警支持动态标签交集，保存为独立关联表，与全部节点和显式节点互斥。节点标签改变时，在同一事务校验配额并取得新的任务和规则作用域，成功后发布内存快照；被选择器引用的标签禁止删除。面板还可按标签一次性批量选择显式节点，此后不随标签变化。标签把"机器叫什么"与"机器属于哪几类"拆开：只有名称子串搜索时，分类信息只能编进名称，一台机器只能属于一个维度、改归属要改名。
- 面板的节点页与总览页有搜索框：按名称、备注与主机名做子串过滤，大小写不敏感（Unicode 简单折叠），纯前端、不动协议与存储；空输入不过滤、显示全部（空条件匹配一切，这一分支显式写出，不让它从循环里自然掉出来）；搜索中禁用拖动排序——结果是子集，拖动无法表达全序。备注与主机名本来就在 `ListNodes` 里回显给会话与 API token，不构成新的暴露。标签落地后过滤器与搜索框并排取交集。
- 管理面板的 API token 页：列表显示名称、创建时间、最后使用时间；创建后明文只显示一次；删除需确认；可下载入口卡片（§5.6）。
- 注册窗口开启后，面板在 key 旁给出一行安装命令（curl 与 wget 各一条）。hub 地址取浏览器当前的 origin，并注明 agent 若经另一地址访问 hub 需替换；hub 为正式版本（`hub_version` 是带 `v` 前缀的合法 semver，与节点落后判定用同一个解析）时，脚本取自该版本的 release（`hub_version` 经 `GetSnapshotResponse.hub_version` 下发），脚本只装自己所属的版本（§5.7），装上的 agent 因而与 hub 同版本；开发构建取最新 release 的脚本，并提示将安装最新 release。origin 为 http 且主机不是 loopback IP 字面量时，命令带 `--insecure-http` 并注明它意味着 token 与指标明文传输（判定与 agent 的传输规则同一口径，§5.7）。面板旁注明安装命令的可信来源是 README 与 GitHub Release：面板由 hub 提供，hub 失守时这里的命令不可信。命令区域在 `hub_version` 到达之前不渲染。
- 未构建前端时 hub 照常编译与启动，页面路径返回"前端未构建"的说明；`go build` 与 `go test` 不依赖 Node。

公开页与 `PublicService` 的细节：

- 内容：总览是节点卡片（名称、国家 / 地区徽章（§4.9）、在线、系统与架构、CPU、内存、磁盘、网速、运行时长、本周期流量，以及填了才显示的费用与到期——费用行在价格或周期任一填了时显示，只填周期时显示"每月"这类周期字样；到期行带剩余天数，已过期标红），节点页是历史图表（静态信息卡同样带费用与到期两行）（指标与探测，时间范围选择与面板同一组件）。图表组件与面板共用；公开入口不得引用 `AdminService` 的生成代码，由测试扫描公开入口的 import 钉住，构建后再按描述符前缀对产物做一次性 grep 核对（不是常驻检查）；依赖方向只禁止公开到管理，面板可以引用公开页的常量。配色用 `light-dark()` 加 `color-scheme`，站点设置的明暗经 `html[data-theme]` 压过系统设置，图表颜色由浏览器解析成 rgb 再交给 uPlot（canvas 不认 `light-dark()`）；浏览器下限 Chrome 123、Firefox 120、Safari 17.5。公开入口的产物在 `internal/hub/web/dist-public`。
- 消息：`PublicSnapshot`（`now`、`report_interval_ms`、`nodes`、公开节点标签的并集 `tags`）；`PublicNode`（id、名称、在线、最近上报、排序、`PublicFacts`、`PublicMetrics`、`Traffic`、§4.9 的 `country`、公开节点的 `tags`（§10 标签一条），以及 §9.4 的 `PublicBilling`：价格、币种、周期、到期日与 `days_left`，自动续期 reserved）。`PublicFacts` 只有系统、架构、CPU 型号、核数、虚拟化——不给主机名、内核版本、agent 版本、ICMP 可用性；`PublicMetrics` 与 `Metrics` 同字段但没有 `boot_id`。两者沿用源消息的字段号，不公开的号连名带号 `reserved`——要公开 `hostname` 这类字段必须先删掉 `reserved` 行，是一个显式动作；值由投影按字段名从源消息复制，字段集合由公开消息自己声明（`Metrics` 以后加字段不会自动公开），构造时逐字段核对名字、类型、基数与 presence，任一不符即 panic。`QueryMetrics` 与 `QueryProbes` 复用管理端的请求与响应类型（定义在 `query.proto`：`public.proto` 若 import `admin.proto`，protoc-gen-es 会让公开包带上 `AdminService` 的描述符）；`GetSite` 直接返回 `PublicSite`、`GetSnapshot` 直接返回 `PublicSnapshot`，第三方主题拿到的 JSON 顶层就是快照本身。把节点标为公开即公开它正在探测的目标：`ProbeSeries` 带任务的种类与目标，公开端只给当前分配给该节点的任务打标签（历史里出现、现已撤下的任务留空——它改成内网目标后从未被该节点探测过，不在公开范围内），管理端按任务当前配置标注、已删除的任务留空；面板图例也用序列自带的标签。缓存上界以 `cache_max_age_s` 方法选项写在 proto 里，与 `probe.v1.access` 同一口径：proto 是单一事实源，第三方主题在 proto 注释里就能看到。
- 限流：按来源键令牌桶，桶容量 60、每秒补充 10，超限 `ResourceExhausted`，与 `Register` 的限速同一实现（§5.2，`internal/hub/ratelimit`）。两处都是挂载点上的 HTTP 中间件而不是拦截器（`Register` 按路径恰为 `/probe.v1.AgentService/Register` 匹配，`Report` 不进桶；公开服务是整个挂载点都经过它）：解码先于拦截器，拦截器看不到解码失败的请求；公开服务还要包在快照缓存外面，缓存命中在 connect 处理器之前应答。只有这样每个请求（含解码失败的）都计数。来源键：IPv4 按单个地址，IPv6 按 /64（一台主机通常拥有整个 /64，逐地址计键等于不限流）；超限的 429 同样带 `no-store`。hub 在反向代理之后而没有配 `--trusted-proxies` 时，全部访客共用代理地址的一个桶（每个打开的总览页每 2 秒轮询一次即 0.5 次/秒，节点页另有每分钟两次历史查询与加载时的请求；补充 10 次/秒：总览页超过 20 个、节点页约 19 个起消耗持续多于补充，30 个页面时净流出约 5 次/秒、60 的桶约 10–12 秒耗尽后出现 429）——这是部署配置问题，写在 flag 帮助与 README 的反代一节，不改限流。
- 缓存：`GetSnapshot` 的序列化结果按编码缓存 1 秒，缓存的是响应字节而不是消息：只缓存规范形态的请求（GET 不带正文——connect 对带正文的 GET 回 415，缓存不得替它应答；POST 为规范的 connect unary），键是 {GET 或 POST, codec, 协商出的压缩}，其余形态直通 connect；协商压缩只读 `Accept-Encoding` 的第一行，与 connect 一致；节点改为私有后公开快照里最多还能看到它约 2 秒（hub 缓存 1 秒加下游 `max-age=1`）。公开页只在加载时取 `GetSite`，已打开的页面刷新后才看到外观改动，刷新时浏览器还可能再用最多 5 分钟的缓存。GET 响应的 `Cache-Control`：快照 `max-age=1`、历史查询 `max-age=60`、站点配置 `max-age=300`；失败响应带 `no-store`（节点改回公开后浏览器不会继续用缓存的 NotFound）；POST 响应不带缓存头。
- 设置：`setting` 表是键值表；`GetSettings` 为只读口径、`UpdateSettings` 仅会话。`Settings` 的字段号在此登记，各分支按表取号、不各自挑：1–5 外观，6 `public_enabled`（§10 总闸），7 `geo_enabled`、8 `geo_url`、9 `geo_backend`、10 `geo_mmdb_path`（§4.9），11 `backup`（§6.7），12 `login_notify`（§5.3）。字段与上限：标题不超过 64 个字符；明暗为 `auto`、`light`、`dark` 之一；主色为 `#rrggbb`；logo 为 `data:` URL，图片类型限 png、jpeg、webp、svg，不超过 128 KiB；自定义 CSS 不超过 64 KiB，含 `</` 即拒绝（它能跳出注入点的 `<style>`）。校验错误写明字段、违反的约束与期望取值；任一项不合约束整次更新不写入。logo 只接受 `data:<type>;base64,<data>` 这一种写法（type 全小写、不带参数；data 逐字节核对标准 base64 字母表后 Strict 解码——宽松解析与浏览器解析一旦不一致，白名单就能被绕过）。CSS 不清洗、按字节原样存，只查字面 `</`（它本身不含字母，一条就覆盖全部大小写变体；CSS 转义与 HTML 实体在 `<style>` 的 RAWTEXT 里都不解码，不拒绝）。表结构 `setting(key TEXT PRIMARY KEY, value TEXT NOT NULL)`，不用 `WITHOUT ROWID`（值可达 128 KiB，超出 SQLite 对无 rowid 表的建议行大小）；键 `site.title`、`site.theme`、`site.accent_color`、`site.logo`、`site.custom_css`、`site.public_enabled` 是持久标识；从未保存过时明暗为 `auto`、总闸为开、其余为空串（空标题即内置标题）。标题有两道限：清洗前不超过 1024 字节，去掉控制字符与首尾空白后不超过 64 个字符。管理请求的解码预算分两类。设置一类由按字段登记的表推出（`internal/hub/api/settings_budget.go` 的 `settingsBudget`）：`Settings` 的每个叶字段登记它的最坏编码类型与上限参数，字节数与用例的边界样本由同一段类型规则推出，表里不写算好的数；按字节上限、取值集合或类型上限取界，边界样本不必通过业务校验。字符串按字节上限的 6 倍加两个引号（encoding/json 默认写法下任何字符编码后不超过其 UTF-8 字节数的 6 倍：控制字符与 HTML 转义的 `<`、`>`、`&` 单字节写成 6 字节，U+2028/2029 由 3 字节写成 6 字节，由逐码点用例钉住）；logo 的上界从 logo 校验的接受集（data URL 前缀集 × 标准 base64 字母表）推出——前缀取 JSON 编码最长者、余下字节按字母表里膨胀最大的字符计，当前等于上限加引号；放宽字母表或前缀集时上界随之变大，用例另钉住接受集不越出前缀集 × 字母表；明暗、主色与 `geo_backend` 按取值集合里最长的一个加引号，并逐值断言没有取值超过登记值；布尔按 `false` 计；uint32 按类型上限 10 位计；渠道列表（§5.3、§6.7）按至多 16 条、每条 22 字节计（正 int64 至多 19 位、两个引号、条间逗号），加方括号、减末项逗号；§4.9 本地库路径按 6 × 4096 计（hub 只回显自己启动参数里的路径，客户端可能把 `GetSettings` 的回显整份送回，合法回送不能被拒；任何能打开的路径不超过 Linux 的 PATH_MAX 4096）。其余字节由 descriptor 逐字段汇总：字段名（取 protojson 接受的原名与 JSON 名中编码后较长的那个）、冒号、对象内字段之间的逗号、各消息的大括号，以及 `{"settings":{}}` 骨架，不留语法余量。预算模型只支持标量、标量 repeated 与本包的单值 message，遇到其他形状（map、repeated message、oneof、外部 message）或未登记的叶字段，在包初始化时即失败；多余的 JSON 空白、对无需转义的字符的转义、冗余的数字写法都不在预算内。用例三向核对：descriptor 枚举出的叶字段集合与登记表双向相等，新字段未登记即红；每项等于其边界样本编码后的长度；按 descriptor 组装的完整边界请求长度恰好等于预算。另有一条用例把可保存的满额设置用 JSON 空白补到恰好预算字节数，必须被接受并保存，预算少一字节时同一请求得到 resource_exhausted。当前预算为 609955 字节，这是设置一类；主题包一类（§10.1）是 8 MiB 的 base64（4·⌈8388608/3⌉）加 4 KiB 语法 ＝ 11188908 字节；预算按过程分两类：`UploadTheme` 用主题包一类（约 10.7 MiB），其余过程用设置一类；主题包一类的用例从常量重算最坏请求并断言落在预算内——解码先于鉴权拦截器，所以匿名请求的读取上限在 `UploadTheme` 上是主题包一类、在其余过程上仍是设置一类，都有界（`Register` 在 `AgentService` 上，用 ingest 自己的 256 KiB 上限，不受影响）。`GetSite` 下发这五项，公开页以 CSS 变量应用，自定义 CSS 放在其后。
- 静态服务：内置公开页与面板用同一套 CSP；`--public-dir` 只加 `X-Content-Type-Options: nosniff` 与 `frame-ancestors 'none'`，不限制脚本与外部资源——目录由运维放置，严格 CSP 会让第三方主题的字体与图片失效。面板、内置公开页与 `--public-dir` 共用一个只服务普通文件的核心：目录、FIFO、设备一律当作不存在，路径任一段以 `.` 开头的名字也当作不存在（`.git/config`、`.env` 是运维放目录时最常见的泄漏；`.well-known/` 因此也不服务，ACME http-01 之类由反代完成），因此任何来源都不列目录、也不会在特殊文件上阻塞（打开带 `O_NONBLOCK`）；`assets/` 下未命中返回 404（`/admin/assets` 因此是 404 而不是重定向），其余回落 `index.html`；自定义目录一律 `no-cache`，每个请求重新 `os.OpenRoot`，目录被原子替换后下一个请求就读到新内容；启动时核对其 `index.html` 是普通文件，否则 `serve` 在打开数据库之前报错。`/` 就是公开页，不再重定向到 `/admin/`。未构建前端时 `/` 与面板一样返回"前端未构建"的说明。
- `GetStorageStats`（只读口径）返回库大小与各表行数，与 `probe-hub stats` 同一来源：表名取自 `sqlite_master` 而不是手写清单（手写清单曾漏掉三张表）；库大小是逻辑大小 `page_count × page_size`——WAL 下主文件大小滞后于内容，逻辑大小等于检查点之后的主文件大小。CLI 先一行 `db_bytes: N`，再逐表 `name: rows` 按表名升序。行数是聚合值，API token 可读：看到 `api_token`、`admin_session` 的行数不构成列出 token（§5.6 禁的是枚举与吊销其他 token）。

第三方主题 = 调 `PublicService` 的静态站点，框架自选；Connect unary 即 HTTP POST + JSON，直接 `fetch` 可用。

### 10.1 公开页主题的上传与托管

经 `AdminService` 上传、列出、启用、删除主题包（`UploadTheme`、`ListThemes`、`EnableTheme`、`DeleteTheme`、`GetThemePreview`，均仅会话）。主题是一个只调 `PublicService` 的静态前端工程，产物由 hub 存进库（`theme`：标识、名称、版本、上传时刻、是否启用；`theme_file`：路径与内容）并在独立 origin 下托管。公开页原先只有两档可换——`setting` 里的外观项只收 CSS，`--public-dir` 能改结构但要求换页面的人能 ssh 到 hub；上传把"改公开页"从一次主机操作降为一次面板操作。文件处理是需要长期维护的攻击面，所以下面每一面各配一条显式守卫，而不是靠"没人上传恶意主题"维持；插件系统与主题市场继续排除，三者的分界是第三方代码跑在哪、包由谁递过来。

- 未配置独立 origin（`--theme-origin https://status.example.com`）时，上传与托管整体不开启：同源的主题 JS 可直接 fetch `AdminService`，浏览器自动附带会话 cookie，§5.3 那四条 CSRF 事实挡的是跨站请求，对同源脚本一条都不成立；"能传但不生效"会让人以为差一步启用，而实际差的是一个域名。hub 按请求的 `Host` 判定：等于主题 origin 的主机名走主题托管，其余走面板与内置公开页；反代把两个主机名都指向 hub。
- 会话 cookie 不设 `Domain`，只对精确主机生效：主题若放在同一注册域名的子域下，`SameSite=Strict` 判定为同站，它不构成隔离。隔离由下面几条各自承载，逐条可验：主题 origin 不挂 `AdminService`（同源请求 404，由挂载承载）；跨源的 JSON 请求要预检，hub 对任何 origin 都不下发 CORS 允许头——这是安全约束而不是"用不着跨源"的便利说明，用例必须覆盖 OPTIONS 预检；简单请求被 connect 以 415 拒绝，`AdminService` 不接受 GET；会话 cookie 是 host-only。兄弟主机能写父域同名 cookie 这一面另有约束：浏览器会把它排在管理员的 host-only 会话之前，所以会话读取对同名 cookie 的多个值逐个校验、任一有效即通过（有效的那个进 ctx，`Logout` 吊销的是它），多出来的无效值不能让有效值失效，同一行 Cookie 头里别的 cookie 的语法（带引号的 JSON、非 ASCII 值）与个数也不影响会话读取——会话值只按名字逐对切出、按 token 形状预筛后在内存里与读出的会话表按候选顺序匹配，不按候选查库（一条带上万个变量的 IN 查询约 110 ms、是同尺寸普通头的约 30 倍，匿名请求不经登录门就能反复触发；读表一次的成本只随会话行数——30 天内的成功登录次数——变化，候选侧只剩 SHA-256、与头部字节线性），不用 net/http 的整行解析器（它遇到不合语法的邻居会整行报错、超过 3000 个 cookie 时整体放弃）；也不设候选数上限——上限会让攻击者用更多 cookie 把有效值挤出去，同一把锁换个形状；否则任何能写父域 cookie 的兄弟主机都能把管理员锁在面板外。
- 主题 origin 上只挂载 `PublicService` 与主题静态文件，不挂载 `AdminService` 与 `AgentService`："只能调 `PublicService`"由挂载承载而非约定（§3.2 的表加一行）。限流与公开页同一套。
- 启用中主题的文件由所有请求共享一份不可变的内存快照：快照在一条语句里整包读出（一个读事务即一个快照，不会拼出两个包），只在 `UploadTheme`、`EnableTheme`、`DeleteTheme` 三个写者提交后失效并原子替换；每个请求的分配量与文件大小无关；内存里同时存在的包数等于 handler 持有的当前一代加"仍被在途请求引用的旧代数"（每次替换产生新一代，旧一代在引用它的慢请求结束后回收），慢连接只能钉住它开始时的那一代，不能按连接数放大——上界是代数乘以一份包（展开 ≤ 64 MiB）而不是并发数乘以文件大小，与 `--public-dir` 从磁盘流式写出同一档。文件带按内容哈希算的强 ETag，同一秒内的两次替换也能区分。
- 主题 origin 上 RPC 路径优先于静态文件；`/admin` 在主题 origin 上不存在（404）。主题静态文件的头与 `--public-dir` 同：`nosniff`、`frame-ancestors 'none'`，不限制脚本与外部资源；`assets/` 未命中 404，其余回落 `index.html`。
- 包是 zip，单个 Connect unary 请求带 `bytes`（面向 agent 设计那条"每个方法都能纯 HTTP+JSON POST 调通"，代价是 base64），不分块；包 ≤ 8 MiB，`AdminService` 的解码预算随之调整。解压总量在读取任何条目内容之前由中央目录的未压缩大小判出上界：展开后总量 ≤ 64 MiB、条目 ≤ 2000、单文件 ≤ 16 MiB，压缩比藏在上传体积上限后面，展开总量必须独立设界；实际展开时逐条累加再核对一次，超出即拒绝整包；各条目的压缩字节合计不得超过包长：条目可以互相重叠、指向同一段压缩数据，合计超过包长即存在重叠，重叠让解压的 CPU 随条目数放大而不受上传体积上限约束，展开总量上限也挡不住它（每个条目各自都在上限内）。
- 条目类型只接受普通文件与目录；符号链接、硬链接、设备节点拒绝整包——路径检查看的是条目名，看不见链接指向；类型按 Unix 类型位判定、不看创建者字段（创建者不是 Unix 时类型位仍可能被填上，只看创建者会放过它们）；跳过而非拒绝会让"装上了"与"装对了"不可分辨。条目路径规范化后必须落在包内，`..` 与绝对路径拒绝整包：产物入库后路径是库里的键，`../` 仍能构造出对其他主题键的遮蔽。
- 清单 `theme.json`：`id`（`[a-z0-9-]{1,32}`，`builtin` 保留）、`name`、`version`、可选 `preview`（包内 png/jpg/webp 路径，经 `GetThemePreview` 读出）。主题不得自报内置：`builtin` 是内置公开页的标识，包里出现即拒绝。`UploadTheme` 带可选 `expect_id`：更新已装主题时包里的 `id` 必须与之一致，否则"更新"会装出第二个主题或覆盖无关的那个而接口报告成功。主题数 ≤ 20。校验比"能解开"严：包根必须有 `index.html`（没有它启用后公开页全是错误页，与 `--public-dir` 启动时核对 index.html 同一口径）；以 `.` 开头的路径段拒绝整包而不是存下永远不会被服务的文件——macOS Finder 压出的 `__MACOSX/._*` 因此被拒，错误信息点名并提示移除；清单的未知字段拒绝（主题不声明配置项，写了也不会生效）；`name`、`version` 至多 64 个字符、不含控制字符；`expect_id` 指向的主题已被删除时返回 `NotFound` 而不是静默装成新主题；预览图按内容嗅探且须与扩展名一致；压缩方式只收 store 与 deflate；条目类型按白名单（普通文件、目录），链接、设备、重解析点整包拒绝；中央目录的上界（条目 ≤ 2000、单文件 ≤ 16 MiB、合计 ≤ 64 MiB）在读任何内容之前判完，展开时逐条核对实际字节数等于声明、CRC 一致。上传是同一个 HTTP 请求体，受 `http.Server` 的 30 秒 `ReadTimeout` 约束：8 MiB 的包要求约 3 Mbit/s 的上行，慢链路上传失败按超时报，不为它单独放宽全局超时；一次上传的峰值内存是 connect 读请求体的缓冲（至多主题包一类的预算）加解码后的包加至多 64 MiB 展开内容，只有会话能调；上传容量为 1，占用即 `ResourceExhausted`——与 §5.3 的密码校验门同一形状，理由也相同：排队只是把内存攒着。
- 整包在单事务内写入，提交前对任何读者不可见；同一 `id` 重传即整体替换（不做版本与回滚，每个标识只存当前包）。启用至多一个；启用中的主题被删除即回落内置公开页——公开页是匿名入口，不得因一次管理操作变成 404。没有启用主题时主题 origin 服务内置公开页。
- `--public-dir` 一旦给出就完全接管主 origin 的公开页；主题 origin 不受它影响，面板在两者同时存在时显式标明主 origin 被目录接管。hub 只接收产物，不执行构建：在 hub 上跑第三方构建脚本就是被排除的插件系统换了触发时机。
- 主题不声明配置项：外观（标题、logo、明暗、主色、自定义 CSS）由 `GetSite` 下发，主题自己决定用不用。
- 主题产物的备份按变更时上传（§6.7）。开发指南 `docs/theme-guide.md` 与功能同批落地：`PublicService` 契约（方法、限流、`cache_max_age_s`）、包布局、清单字段、上限与被拒绝的条目类型。

## 11. 错误处理

| 情形 | 行为 |
|---|---|
| 上报含非法值（非有限数、负数、百分比越界、`load` 形状不对） | 整条拒绝（`InvalidArgument`）；`live`、流量基线、探测桶均不变 |
| `Facts` 中的字符串 | 截断到上限并剔除控制字符；其中数个字段会出现在匿名公开页 |
| 分钟行写库失败 | 该桶进入有界的待重试列表；列表满则丢弃最旧的并记日志；重试时若已落在水位之前，按 §6.4 第 1 条丢弃。不阻塞上报 |
| 流量落盘失败 | 保留脏标记，下个周期重试；内存状态仍在，不丢 |
| hub 重启 | `live` 自首批上报重建；流量按 §7 崩溃不变式补齐；告警按 §9.2 重启不变式计时 |
| agent 连不上 hub | §4.7 |
| hub 墙钟被向后拨 | 桶的键来自墙钟，回拨后新样本会落进已经写过的分钟。尚在水位之后的桶经加法合并并入：均值仍是并入样本的均值，但该桶的 `rx_bytes` / `tx_bytes` 会装进超过一分钟的字节，图表上这一桶的速率偏高；已在水位之前的桶按 §6.4 第 1 条丢弃，图表在墙钟追上之前出现空洞。流量总量不经过分钟桶，不受影响；TTL 与告警宽限期用单调钟，不受影响 |
| 同一 token 两台机器 | §5.1，标记警告 |
| 通知投递失败 | §9.3，重试并记录 |

## 12. 测试策略

- 从用户可见入口测：`ingest`、`api` 的测试经真实 Connect 处理器 + `httptest` + 生成的客户端，不直接调内部函数。
- 横切不变式用自动枚举覆盖，新增成员自动入测：
  - 从生成的服务描述符枚举全部 RPC，逐个无凭据调用，断言 `Unauthenticated`；白名单恰好是 `PublicService` 的四个方法，白名单内的另断言不是 401（证明确实可匿名到达）。`Register` 与 `Login` 不在白名单：用空请求调用时各自的方法体本就返回 `Unauthenticated`，放进白名单只会让测试变弱。
  - 从 `PublicService` 各方法的响应出发遍历可达的全部消息（含复用的 `Traffic`、`QueryMetricsResponse` 等非 `Public*` 类型）的字段，与测试内的显式允许列表比对；往公开消息加字段必须同时改这份列表，列表里有而响应到不了的消息同样报错。
  - 从描述符枚举 `AdminService` 全部方法：每个都声明了 `probe.v1.access`；持有效 API token 逐个调用，`ACCESS_READ` 放行、其余 `PermissionDenied`；吊销后下一个请求即 `Unauthenticated`。两条凭据互不回退由交叉用例钉住：有效 cookie 加无效 bearer 被拒，有效 bearer 加无效 cookie 调只读方法放行，有效 cookie 加 `Authorization: Basic` 走会话路径放行。
- 多处同守的不变式每处各写断言：冻结不变式（写协程拒绝水位之前的 1m 写入 / 上卷不越过 `now − ROLLUP_LAG` / ingest 拒收超龄结果 / 三个常量关系的启动期断言）、探测硬限制（hub 校验 / agent 丢弃）。
- 钉住 §5.3 对 connect-go 的行为断言：带有效会话 cookie、`Content-Type: text/plain` 向 `AdminService` 的写方法 POST，断言被拒绝且无副作用；同一请求改用 GET 同样被拒绝。
- 时间经 `internal/clock` 注入，`live`、`alert`、`traffic`、上卷的测试可确定性地推进单调钟与墙钟，并可单独向后拨墙钟。
- 存储：上卷跑两遍结果相同；半桶合并；在"插入上级行"与"推进水位"之间注入失败，断言两者一同回滚；某指标在整桶内都缺失时查询返回"无数据"而不是 0，部分样本缺失时均值只由存在的样本决定。
- 采集：解析函数接受可注入的文件系统根，用来自真机（含 LXC、OpenVZ）的 `/proc` 快照做 fixture。
- darwin 采集文件带 build tag，Linux 上的验证循环照不到：CI 含 macOS runner 跑其测试；Linux 上至少执行 `GOOS=darwin go vet ./...`。
- 公开页：限流超限返回 `ResourceExhausted`；1 秒内的两次 `GetSnapshot` 得到同一份响应字节且只序列化一次；GET 的 `Cache-Control` 按方法各异、POST 没有；`--public-dir` 下 `..` 与指向目录外的符号链接都拿不到文件；设置的每条校验各有用例；公开入口引用 `AdminService` 生成代码即红；e2e 里用 GET 调 `GetSite` 与 `GetSnapshot`，对非公开节点的 `QueryMetrics` 与不存在的节点得到同一个 NotFound。
- Docker 镜像：构建后根文件系统逐条目核对；起容器，`/admin/` 返回 200 的面板页（503 是面板没构建进二进制的说明页，不算通过），`docker exec -i` 能执行 `passwd`，不带 `-i` 时报没有输入；库目录不可写时立即退出并写出库路径。
- 每条新断言做一次缺陷注入，确认它红且红在正确的原因上；声称"只有 X 会让它红"的断言，把非 X 的原因也注入一遍。
- 端到端按 agent 容器镜像参数化，并断言上报的系统名与镜像一致（证明换镜像真的生效）：一级发行版每次跑，二级发行版发版前跑（§14）。容器没有真实 init，覆盖不了 sysctl 默认值与服务管理；安装脚本与服务单元在各发行版真实启动的机器上验证。
- 入口卡片（`proto/SKILL.md`）里标为示例的 shell 代码块由 e2e 用真实 hub 与 token 逐个执行，断言退出码为 0 且输出为合法 JSON：卡片与接口漂移时 e2e 变红，而不是等 agent 调用失败才发现。
- 安装验收在真实启动的机器上跑（本地 OrbStack；验收脚本只创建与删除带自己前缀的机器）：一级发行版 Debian 12、Alpine 3.21 两个架构每次改动安装脚本或服务定义时跑，二级发行版发版前加跑。每格断言：以面板给出的管道形态（`curl … | sh -s -- …`）一条命令装好且节点上线——脚本来自 stdin 时，脚本里读 stdin 的命令会吞掉余下部分，文件形态测不出这一点；服务进程的 Uid 不为 0 且有效能力含 `CAP_NET_RAW`，facts 里 ICMP 可用且 ICMP 任务有结果（能力由 init 授予）；服务下上报的指标字段集合与内存总量与同机 root 手动运行一致，服务进程所见的根目录与 init 所见的在同一个文件系统上（按设备号判定）（加固项不得让采集缩水；根分区总量不按两次上报的数值比：OrbStack 机器的根是 btrfs，实测同一台机器相隔 5 秒的两次上报总量可相差约 2.4 GiB 而已用量相同；网卡只以合计计数器上报，逐网卡集合经接口观测不到，不作断言）；换一个版本重跑后节点数不变、版本为新版本；OpenRC 机器重启后服务自启；卸载（含 `--purge`）后服务、二进制、配置、日志目录、用户与组都不存在。下载目录经 `--base-url` 指向本地 `make release` 的产物。这类验收依赖真实 init，不进 CI；`install.sh` 的 shellcheck（POSIX 模式）进 CI，systemd 单元的 `systemd-analyze verify` 在验收机器上跑。
- CI：`buf lint`、`buf breaking`（`WIRE_JSON`，对比主干）、`go vet`、`go test -count=1 ./...`。

## 13. 实现前需以实验确认的事项

下列都是对外部组件特定版本的行为断言，以实验结果为准，结论记入对应里程碑的计划：

1. darwin 采集在 `CGO_ENABLED=0` 下的可行实现（gopsutil 或 purego）：能构建，且在真机读出 CPU、内存、网卡计数器、`boot_id` 等价物。——已于 2026-09-26 在 Apple Silicon 真机实验确认，结论记在 `docs/superpowers/plans/2026-09-26-m6-macos-agent.md` 的"实验结论"节。摘要：网卡计数器经 `NET_RT_IFLIST2` 与 `getifaddrs` 对普通进程只给 32 位、按 KiB 取整的值，`net.link.generic.ifdata` 给完整 64 位值；聚合的 `HOST_CPU_LOAD_INFO` 在 Apple Silicon 上按秒取差不稳，要用逐 CPU 的 `host_processor_info`；Rosetta 下 `hw.pagesize` 为 4096 而页计数的单位是 `host_page_size`（16384）；`kern.bootsessionuuid` 跨睡眠唤醒不变、重启即变。
2. 非特权数据报 ICMP 在目标 Linux 发行版（含容器环境）与 macOS 上的可用性，以及 `CAP_NET_RAW` 回退路径。——已于 2026-09-23 实验确认，结论记在 `docs/superpowers/plans/2026-09-23-m3-probe-backend.md` 的"实验结论"节：macOS 非 root 可用数据报 socket；Linux 只受 `net.ipv4.ping_group_range` 管（Docker 默认放开，内核默认关闭，Debian 12 的 systemd 包不放开），raw 需有效 `CAP_NET_RAW`；回包匹配不能依赖 ICMP ID。2026-09-25 在 OrbStack 提供的 Alpine 3.21、Debian 12、Ubuntu 24.04、Rocky Linux 9 机器镜像（真实启动，amd64 与 arm64）上复现并补充：开机时 `ping_group_range` 只有 Rocky 9 放开（systemd 上游的 50-default.conf），另外三个关闭——Alpine 的 00-alpine.conf 写了 `999 59999`，但该镜像的 sysctl 服务不在默认 runlevel，文件没有被应用；Alpine 官方安装介质的默认 runlevel 未验证。由此：运行用户的组不在 `ping_group_range` 内时，要靠授予 `CAP_NET_RAW`（§14）才有 ICMP；显式把该组放进范围同样能走数据报路径，二者任一即可。
3. `modernc.org/sqlite` 在约 500 万行规模下的上卷查询、窗口查询与分块 prune 耗时；带对照组（同一数据、同一查询、空闲与并发写入两种条件）。
4. 经反代（HTTP/2 到反代）时单次上报的线上字节数，用于判断 §4.4 由 TTL 反推出的上报间隔在目标规模下的成本是否可接受。结论若为不可接受，要动的是 TTL 这个产品指标或消息体积，不是把间隔调长而默许 TTL 跟着漂。
5. 含 connect 与 protobuf runtime 的 agent 二进制体积与常驻内存。
6. 500 节点规模下的库文件大小。
7. connect-go 处理器对 `application/json`、`application/proto` 之外的 `Content-Type`，以及对未标为无副作用的方法的 GET 请求的实际响应（§5.3 的前提）。

## 14. 构建、发布、安装

- 全部 `CGO_ENABLED=0`。agent 目标：linux/amd64、arm64、armv7、386、riscv64；darwin/amd64、arm64。hub 目标：linux/amd64、arm64，另出 Docker 镜像。
- Linux 支持矩阵：架构一级为 amd64 与 arm64（每次改动跑端到端），其余 agent 架构只保证能构建。发行版一级为 Debian 12（glibc、systemd）与 Alpine 3.21（musl、OpenRC、busybox），每次改动两个架构都跑端到端；二级为 Ubuntu 24.04 与 Rocky Linux 9，发版前跑。与 libc 无关由静态链接承载：产物不得带动态解释器，构建产出二进制时就检查，让产物变成动态链接的改动都在那里失败，而不是等到 Alpine 上启动报错——未显式关闭 cgo 且 C 工具链可用时，包含 net 的原生构建可能引入系统 C 库依赖。检查是三条独立的交付约束：没有 `PT_INTERP`、没有 `DT_NEEDED`、构建设置显式为 `CGO_ENABLED=0`；它们不互相等价（例如带 `netgo` 标签、开着 cgo 的构建也可能是静态链接），缺一条就拒绝。检查器是 `scripts/checkstatic`，发布流水线必须把全部 Linux 产物（agent 与 hub）交给它，而不是复制一份当前的文件清单。Linux 采集直接读 `/proc`、`/sys` 与 `/etc/os-release`，不调用发行版的命令行工具；共用的内核接口与 os-release 格式让采集不需要发行版专用分支，文件是否可读、返回值是否合理仍由测试与各发行版端到端验证。
- 构建顺序：`buf generate` → 前端构建 → `go build`。生成的 Go 代码入库，前端产物不入库。
- systemd 卸载边界（agent 与 hub 相同）：普通卸载保留本地 drop-in；`--uninstall --purge` 删除 `/etc/systemd/system/<服务名>.service.d` 和 `/run/systemd/system/<服务名>.service.d` 后执行 daemon-reload，不依赖主单元仍存在。目录为符号链接时只删除链接；不从 DropInPaths 枚举删除目标，避免波及共享 drop-in 与发行版文件。不清理系统 journal，由系统日志保留策略处理。
- 发布：`make release VERSION=vX.Y.Z` 把全部产物生成到 `dist/`；推送 `v*` tag 时 CI 调用同一目标并建 GitHub Release。构建逻辑只在 Makefile 一处，本地验收与线上发布用的是同一套产物。Linux agent 每个架构一个 `probe-agent_linux_<arch>.tar.gz`（二进制、systemd 单元、OpenRC 服务脚本），hub 每个架构一个 `probe-hub_linux_<arch>.tar.gz`，另有 `SHA256SUMS`、`install.sh` 与 `install-hub.sh`（两个 hub 的 tar 包内含 systemd 单元文件）。资产名不带版本号：`releases/latest/download/<名>` 与 `releases/download/<tag>/<名>` 都能直接拼出，脚本不必调用 GitHub API。版本号经 `-ldflags -X main.version` 注入，构建带 `-trimpath`，二进制里不含构建机的路径。三个安装脚本在打包时写入本版的版本号与本版全部 tar 包的 SHA-256（§5.7），写入只有一份实现，`make release` 与 `deploy/` 的脚本测试都调用它；`SHA256SUMS` 仍随 release 发布，供人工核对，脚本不以它为校验依据。tag 带预发布后缀（semver 的 `-` 部分）时建为 prerelease，不成为 latest：开发构建的面板命令取 latest 的脚本；预发布的判定只写在 Makefile 一处，GitHub Release 与镜像的 `latest` 都读它。当前只出 Linux 产物，darwin agent 与 hub 的 Docker 镜像随 M6 加入（§15）。服务定义在仓库 `deploy/` 下是真实文件，打包时原样放入，不在脚本里以字符串另存一份。仓库必须公开：私有仓库的 Release 资产要鉴权才能下载。
- Linux 安装脚本（`install.sh`，以 root 运行）的步骤顺序：检测 init 系统与架构（卸载不看架构）→ 创建固定的系统用户 `probe-agent` → 检查下载器（curl 或 wget）与 `sha256sum`，缺了在任何网络操作之前报错 → 确保 CA 证书 → 下载 tar 包并按脚本内嵌的哈希校验（§5.7；源码脚本没有内嵌哈希、或清单里没有本机的包时，在检测完架构之后、建用户与任何网络操作之前就拒绝安装，不留下建了一半的账户）→ 解包并确认三个文件齐全 → 写同目录的临时二进制 → 停止已安装的服务并确认（见下条）→ `mv` 替换二进制 → 没有配置时 `probe-agent register` → 让服务用户能读配置：目录保持 root 属主、组为 `probe-agent`、0750，文件交给该用户、0600（register 以 root 写入，不改属主服务就读不到；目录不交给服务用户，服务用户就不能替换其中的目录项，root 对它的后续操作不会被链接或竞态劫持；agent 运行时不写配置目录）。这一步每次安装都做，不只在注册之后：手工重新注册、注册后被打断、账户被删后重建，都会留下服务用户读不到的配置 → 安装服务定义并启动（systemd 单元或 OpenRC 服务，要求见下两条）→ 确认服务起来了：有上限地等到出现以服务用户运行的进程，3 秒后同一个进程仍在，否则失败退出并指出日志位置。init 的 start 返回 0 证明不了子进程活着（OpenRC 的情形见下；systemd 的 `Type=simple` 在 fork 之后即视为已启动），不确认就会出现脚本报成功、节点却不上线。判据与停服务之后的确认是同一个（有效 uid 为服务用户的进程）。建用户排在下载与注册之前：它若失败，注册窗口的名额尚未消耗、旧服务尚未停止。参数：`--hub`、`--key`、`--name`、`--insecure-http`、`--base-url`（覆盖下载目录，用于镜像与本地验收；默认是脚本所属版本的 release 下载目录，只改变字节从哪里取，校验依据仍是内嵌哈希）。没有 `--version`：给了就报错并说明取对应版本的脚本。`--insecure-http` 在首次安装时交给 `register`，重跑沿用现有配置时交给 `probe-agent configure --insecure-http=true`，已有 http 部署靠它一条命令完成升级（§5.7）。重跑沿用现有配置时 `--name` 不生效，与 `--key` 被忽略时一样给出提示。
- 重跑即升级：已有配置时跳过注册，`--hub` 与 `--key` 可省；同时给了 `--key` 时明确提示沿用现有注册，不重新注册——重新注册会多消耗一个窗口名额，并在 hub 上多出一个节点。替换之前先确认包完整（校验、解包、三个文件齐全），再停止已安装的服务——是否需要发 stop 看服务定义是否已安装，不看配置是否存在；发出之后无条件确认没有以服务用户运行的进程（扫描 `/proc/<pid>/status` 的有效 uid，有上限地等待），不论服务定义在不在：OpenRC 0.55.1 上 `rc-service stop` 在所测的各状态下都返回 0，supervisor 被杀而子进程留存时 `status` 也报 stopped，二者都证明不了进程已退出；单元或 init 脚本被手工删掉而进程仍在时也只有这一步抓得到。确认不通过即失败退出，不能在旧进程仍在运行时报告安装成功。二进制先写同目录临时文件再 `mv` 替换；服务定义每次覆盖，单元的改动随升级下发。`--uninstall` 停止并禁用服务（停不下来即非零退出）、删除二进制与服务定义，保留配置与用户；`--purge` 必须与 `--uninstall` 同时给出；加 `--purge` 一并删除 `/etc/probe-agent`、日志目录、用户与同名组，并回查二者都已不存在，删不掉即非零退出。以 `curl … | sh -s --` 运行时脚本来自 stdin，脚本里每个可能读 stdin 的外部命令都显式 `</dev/null`（不能 `exec </dev/null`，那会切断脚本自己的来源）。
- systemd 单元使用静态 `User=` 并加固（`NoNewPrivileges=`、`ProtectSystem=strict` 等），默认带 `AmbientCapabilities=CAP_NET_RAW` 与 `CapabilityBoundingSet=CAP_NET_RAW`：裸机 Debian 的 `ping_group_range` 默认关闭（§13 第 2 项），没有这项能力时 ICMP 探测只能回报 error。用户由安装脚本创建并拥有配置文件，OpenRC 侧以同一用户运行，单元用静态 `User=` 并逐项写出加固，不经 `DynamicUser=` 隐式引入；`DynamicUser=` 会隐含的 `RestrictSUIDSGID=` 在单元里明写。单元设 `MemoryMax=128M`（§5.7）：2026-09-29 在 Debian 12.15 arm64 / systemd 252.39-1~deb12u2（OrbStack LXC，cgroup v2）满 64 个 5 秒间隔 ICMP 任务运行至少 3 分钟，cgroup 峰值 21389312 字节，取六倍后向上取整；详细负载、进程读数与生效前提写在服务定义注释里。OpenRC 不设：`rc_cgroup_settings` 需要宿主机向服务 cgroup 下放 memory 控制器，2026-09-29 在 Alpine 3.21 / OpenRC 0.55.1（OrbStack LXC，cgroup2，根 `subtree_control` 为空）实测静默不生效，安装脚本不改宿主机全局 cgroup 配置。launchd 同样不设；OpenRC 与 launchd 的内存防护只靠 agent 响应体上限，没有服务级强制上限。monitor 提交 `85f6702` 记录过未开 nesting 的 LXC 容器里 `DynamicUser=` 的单元拒绝启动（226/NAMESPACE）；2026-09-26 在 Debian 12 bookworm-backports 的 Incus、容器内 systemd 252（252.39-1~deb12u2）、`security.nesting=false` 的容器里实测，静态 `User=` 与 `DynamicUser=` 的单元都能启动，该记录在此环境不成立；它出自哪种 LXC 配置未定位，不作为选型理由。采集读不到某个文件时只让对应字段缺失并记日志，上报照常，因而加固项若遮蔽了采集要读的 `/proc`、`/sys` 路径不会以失败显形；由 §12 的真机对照验收承载。
- init 系统支持 systemd 与 OpenRC。判定：`/run/systemd/system` 存在为 systemd；否则 `/sbin/openrc-run` 存在且可执行为 OpenRC（`/run/openrc` 表示已启动）；两者都不是时安装脚本报错并列出支持的 init，不静默降级（容器里两者通常都不存在）。OpenRC 服务用 `supervisor=supervise-daemon`、`command_user` 为固定系统用户、`capabilities="^cap_net_raw"`，与 systemd 的 `AmbientCapabilities` 等价（2026-09-25 在 Alpine 3.21 / OpenRC 0.55.1 真机上实测：ping_group_range 关闭时授予该能力 ICMP 可用，去掉即不可用）。`output_log` 与 `error_log` 的文件必须在启动前建好并交给运行用户，由服务脚本的 `start_pre` 建立，只把这两个文件交给运行用户，日志目录保持 root 属主、不递归改属主（服务用户能增删目录项时，root 按路径做的改属主之类的操作，对象可以被它换成别的文件；所以 root 不在服务用户控制的目录里操作），每次启动都成立而不只在安装时建一次：supervise-daemon 降权后才打开它们，打不开时子进程秒退、反复拉起，而 `rc-service status` 仍显示 started。agent 异常退出后两种 init 都无限次重启、每次间隔 5 秒：systemd 用 `Restart=on-failure`、`RestartSec=5` 与 `StartLimitIntervalSec=0`（默认 100 ms 间隔加 10 秒内 5 次的上限，会让一次短暂故障把单元永久留在 failed）；OpenRC 用 supervise-daemon 的对应设置，取值以实测为准。
- 安装脚本用 POSIX sh，兼容 busybox，按实际存在的工具分支，不假设任何单一工具（所测的基础容器镜像与机器镜像之间、以及各发行版之间，工具集都不相同）：先建同名组（`groupadd --system`，没有时 busybox 的 `addgroup -S` 或 Debian 的 `addgroup --system`），再以它为主组建用户：优先 `useradd --system -g`，没有时 busybox 的 `adduser -S -D -H -G`、Debian 的 `adduser --system --no-create-home --ingroup`（busybox 的 adduser 不指定组时会把用户放进 `nogroup`，而 OpenRC 的 `command_user` 与配置文件属主都写 `probe-agent`），建完一律回查主组 `id -gn`，不信退出码——实测 Debian 的 perl 版 adduser 收到 busybox 风格的参数时打印用法、不建用户，却返回 0。下载用 curl 或 wget，按实际存在的那个走（所测基础容器里 Alpine 只有 wget、Rocky 只有 curl、Debian 与 Ubuntu 两者都没有；所测机器镜像里四个都有 curl），两者都没有就报错说明依赖；下载地址为 https 时 curl 限定请求与重定向都只走 https（wget 没有对应开关）。能力授予交给 init，不依赖 setcap（所测的 Alpine 与 Debian 机器镜像、以及 Alpine、Debian、Ubuntu 基础容器里都没有）。所测的 Debian 与 Ubuntu 基础容器不带 CA 证书（机器镜像都带），CA 是否存在按文件探测而不是按发行版名判断；缺失时安装脚本先装 `ca-certificates`，判据是下载地址或 hub 地址任一为 https——默认下载地址就是 https，只看 hub 地址会让下载先失败。
- hub 安装脚本 `install-hub.sh`（`deploy/`，以 root 运行）：在 Linux 主机上一条命令装好 hub 的 systemd 服务，重跑即升级，`--uninstall` 与 `--purge` 的删除范围与 agent 脚本同语义，但两者都要确认（无终端时须 `--yes`）：agent 卸了重装即回，hub 的普通卸载停掉的是全部节点的展示与告警，purge 删的是唯一一份数据与全部节点凭据；不用 Docker 的自托管者由此有一条能直接跑的路径。与 agent 脚本同一套约束（POSIX sh、按实际存在的工具分支、curl 或 wget、按内嵌哈希校验且没有 `--version`（§5.7）、静态系统用户 `probe-hub`、先建用户再下载、停服务后确认进程退出、启动后确认进程活着），不另立口径。单元 `deploy/systemd/probe-hub.service` 用静态 `User=` 并逐项加固，不需要 `CAP_NET_RAW`；`--listen` 默认 `127.0.0.1:8080` 不变，脚本不替用户决定对外监听。数据目录固定 `/var/lib/probe`，root 属主、服务用户组可写（0770），库文件属服务用户 0600：SQLite 要在目录里建删 `-wal`/`-shm`，服务用户必须能增删目录项，agent 配置目录那套只读的 0750 不适用；目录留 root 属主是为了升级时能锁住它——脚本在停服务并确认该 uid 没有进程之后先把目录收成 0750，此时只有 root 能增删目录项，再核对库文件不是链接、只有一个硬链接才改属主，改完放回 0770；服务用户拥有目录的话这一步没有可靠的锁。管理员密码在脚本末尾提示用 `probe-hub passwd --db /var/lib/probe/probe.db` 设置，脚本自己不生成、不打印密码。无终端时不交互、取默认值，需要确认的动作要求 `--yes`。升级时 `--listen`、`--timezone`、`--trusted-proxies` 等参数沿用已装单元里的值，除非命令行显式给出——重跑即升级不能把用户改过的参数重置回默认。停旧服务之前先查端口冲突，按 pid 排除 hub 自己。只做 systemd；不做菜单，只做参数式（菜单是交互层不是功能）。release 产物加这两个文件，`scripts/install-accept.sh` 加 hub 一格并在真机验收，README 加一节。
- macOS agent：采集在 `CGO_ENABLED=0` 下实现（§13 第 1 项的实验定案）：`x/sys/unix` 的 sysctl 取启动标识（`kern.bootsessionuuid`）、内存总量、负载、swap、连接数（`net.inet.tcp.pcbcount`/`net.inet.udp.pcbcount`）、网卡计数器（`net.link.generic.ifdata`）与 facts；`statfs` 取磁盘；`clock_gettime(CLOCK_MONOTONIC)` 取运行时长；purego 调 libSystem 取逐 CPU tick、VM 统计、页大小与进程数。采集分层为平台取原始读数的 `Host` 与平台无关的差分、过滤与 `used ≤ total` 检查；darwin 的字节布局解析不带 build tag、Linux 上可测，只有系统调用层带 darwin 约束并引用 purego，其依赖不链入 Linux 二进制。darwin 二进制按平台约定动态链接 libSystem，静态门禁只查 Linux 产物。产物 `probe-agent_darwin_<arch>.tar.gz`（二进制、launchd plist）。安装脚本 `install-macos.sh` 以 root 运行：检测架构 → 用 `dscl` 建 `_probe-agent` 用户与组（uid/gid 从 499 向下取空闲号：Apple 逐版从 300 向上追加系统账户，升级会替换低号段的第三方账户）→ 下载并用 `shasum -a 256` 按内嵌哈希校验（§5.7，参数与 Linux 脚本同口径：没有 `--version`，有 `--insecure-http`）→ 停止已装的 LaunchDaemon 并确认进程退出 → 替换二进制 → 没有配置时 `probe-agent register` → 配置属主同 Linux（`/etc/probe-agent`，目录 root 属主、组 `_probe-agent`、0750，文件 0600）→ 写 `/Library/LaunchDaemons/xyz.probe.agent.plist`（`UserName` 为该用户、`KeepAlive`、`ThrottleInterval` 5 秒——两次拉起的最小间隔，进程跑满 5 秒后退出会立即拉起，`KeepAlive` 为真时 0 退出也拉起，与 Linux 的重启间隔同量级、日志在 `/Library/Logs/probe-agent/`：目录属 root:wheel 0755，服务用户不能在其中放条目，launchd 无论以什么身份打开日志都不会被链接引到别处；两个日志文件每次安装预建并交给服务用户 0640；安装脚本先把目录交给 root、`chmod -N` 去掉 ACL（服务用户曾为属主时可能加过允许项，数字 chmod 与 chown 都不去掉它），再检查文件是不存在或链接数为 1 的普通文件；预建、属主、权限这些依赖外部条件的操作全部在停服务之前完成，停服务之后只剩换二进制、写 plist、bootstrap。文件被删后若 launchd 以服务用户身份打开会反复 EX_CONFIG，重跑安装恢复；launchd 以什么身份打开由真机清单判别，不在代码里假设）→ `launchctl enable` 后 `bootstrap system`（enable 清掉可能残留的禁用覆盖）→ 确认进程活着（`ps -axo uid=,pid=,comm=` 按有效 uid 与可执行路径，macOS 没有 /proc；只按 uid 不够：launchd 会以该 uid 派生 cfprefsd、trustd 之类的辅助进程，停止确认与启动确认都要把它们排除）。是否发 `bootout` 看作业是否已载入 system 域（`launchctl print`），不看 plist 文件在不在：launchd 按已载入的作业管进程。架构按 `hw.optional.arm64` 判定再看 `uname -m`（Rosetta 终端里 `uname -m` 报 x86_64）。不装 CA：macOS 自带 curl 与系统信任库。重跑即升级，`--uninstall`、`--purge` 语义同 Linux。没有 macOS 虚拟机可用：脚本逻辑用桩测试，真机验收由人在 Mac 上执行，脚本随附检查清单。面板的安装命令只给 Linux 的两条，macOS 的写在 README。CI 含 macOS runner 跑 agent 的测试。
- hub Docker 镜像：`ghcr.io/xjetry/probe-hub:<version>`，预发布不打 `latest`；`FROM scratch`，只含静态二进制（`/usr/local/bin/probe-hub`，让 `docker exec … probe-hub` 按名字可执行：`/` 不在容器默认 PATH 里）、CA 证书（通知出站 HTTPS 要用）、非 root 用户、属于该用户的空 `/data` 与 1777 的 `/tmp`（SQLite 的排序溢出、临时表与建索引要写临时文件，没有 `/tmp` 时报 `disk I/O error`，小查询不触发）；时区数据已嵌入二进制。根文件系统由 `scripts/checkimage` 逐条目核对，多出或缺少任一条即失败。数据卷 `/data`，默认参数 `serve --db /data/probe.db --listen 0.0.0.0:8080`；容器里监听非 loopback 是预期的，启动告警照旧，反代与 `--trusted-proxies` 由部署者配。管理员密码经 `docker exec -i … probe-hub passwd --db /data/probe.db` 设置；不带 `-i` 时容器内 stdin 立即 EOF，`passwd` 单独报没有输入。镜像的构建与推送都在 Makefile（构建器是按 digest 固定的 BuildKit，本地与发布同一版本），release 流水线不另用 build-push 类 action；镜像先于 GitHub Release 推送并回读：先只推版本 tag，回读时逐平台拉回、冒烟并把导出的根文件系统交给 `scripts/checkimage` 核对（核对的是 registry 上实际存在的内容，不是构建时的中间产物），正式版本再把 `latest` 指向已回读的 digest 并回读 `latest`——`latest` 只会指向回读通过的镜像；回读里"取不到"与"读取失败"分开判定，读取失败不放行；同组 release 运行串行；任何一步失败重跑 job 即可；带构建元数据（`+`）的 tag 不能成为 Docker tag，这类 tag 的发布整体失败、什么都不发布；新建的 ghcr 包首次发布须手工设为公开。`make docker` 在本地构建、核对并冒烟（起容器、`/admin/` 返回 200 的面板页而不是"未构建"说明、`passwd` 可执行）。
- 升级 = 重跑安装脚本（见上）。hub 版本经 `GetSnapshotResponse.hub_version` 下发：面板据此生成与 hub 同版本的安装命令（§10），并显示各节点 agent 版本、标出落后于 hub 的节点：按 semver 2.0 优先级比较（预发布低于对应的正式版，构建元数据不参与），任一方不是带 `v` 前缀的合法 semver 时不标。

## 15. 里程碑

每个里程碑有独立的实现计划，结束时都是可端到端运行的状态。

| 里程碑 | 内容 |
|---|---|
| M1 垂直切片 | proto 与 buf 流水线；hub 的 `ingest` / `live` / `store`（仅 1m 级）；节点 token、注册窗口、`probe-hub` 的节点管理子命令；Linux agent 采集与上报循环 |
| M2 管理面板 | `AdminService`、管理员登录、节点管理与 token 轮换、实时视图、历史图表；5m / 1h 上卷、prune、按窗口选级 |
| M3 流量与探测 | 流量累计；探测任务与版本对账、agent `prober`、探测存储与上卷、图表 |
| M4 告警 | 规则、状态机、Telegram / Webhook、投递记录 |
| M4 之后：接入与 Linux 交付 | API token、入口卡片与 `GetApiReference`（§5.6）及其面板页；Linux 发布流水线、安装脚本、systemd 与 OpenRC 服务、面板安装命令（§14） |
| M5 公开页 | `PublicService`、外观定制、`--public-dir` |
| M6 交付 | macOS agent、Docker 镜像 |
| M6 之后：节点计费 | 费用与到期（§9.4）、到期告警规则（§9.1、§9.2）、面板与公开页的费用与到期 |

## 16. 参考项目

`reference-projects/` 下有两个只读参考（不入库）。

沿用自 monitor（`src/agent_ws.rs`、`src/db.rs`）：在线与最新数据同一事实；hub 侧用 `boot_id` + 内核计数器做流量差分且"无读数 ≠ 0"；历史行是整桶聚合而非边界瞬时采样；时长用单调钟；限时限量且失败计数独立的注册窗口；主键顺序按查询路径排；非法上报不改动已有状态。

有意不同于 monitor：token 存 hash 而非明文；单仓库共享协议类型而非两仓库靠运行时契约检查；不从请求头推断部署形态；主题托管在与面板不同的 origin 且产物入库，而非同源落盘；不托管 agent 二进制（计费字段与 GeoIP 外呼一度也在此列：计费字段自 §9.4 起作为提醒用的展示值纳入；国家查询记在 `FEATURES.md`，与 monitor 的差别是默认关闭、服务地址来自配置、地址只取 hub 看到的来源）。

规避自 komari：token 经 URL 传递且有三个读取位置；WebSocket 与 HTTP 两套在线状态并存；远程执行 / 终端 / 文件管理；内嵌 JS 引擎的插件系统；三方言自研时序层。
