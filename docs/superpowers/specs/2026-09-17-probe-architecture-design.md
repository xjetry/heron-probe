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
- 周期流量配额与按用量比例提醒，管理面板与公开页展示相同计费口径
- 节点费用与到期：价格、币种、周期、到期日与自动续期的展示值，以及到期提醒（§9.4）
- 公开页主题：在管理后台上传或从公开 GitHub Release 安装构建产物，预览、按不可变版本启用和回滚；同域名公开页通过可信容器中的不透明来源沙箱运行主题，并提供主题 SDK 与开发指南。
- 节点的国家 / 地区徽章：由默认开启的查询服务按节点来源地址得出，或由管理员手动指定；HTTP 后端会把节点公网地址发给配置的服务，运维可在面板关闭查询，或改用不出网的本地库
- 管理员显式触发官方正式 Release 在线更新：Linux systemd 的 hub 与 agent 使用独立本机更新器，不开放任意代码或命令下发。更新器只接受带官方签名的产物；出站受限、只能连到 hub 的节点在安装时选择经 hub 中转取产物（§4.10）。

已确认要做、但尚未在本文成形的功能点记在仓库根的 `FEATURES.md`；其中某条进入里程碑时，设计并入本文并从那里删除。

**非目标**（明确排除，新增功能前先对照此表）：

| 排除项 | 原因 |
|---|---|
| 远程终端、命令执行、文件管理 | 会让 hub 成为对全部节点的远程代码执行入口；hub 失守即全部节点失守 |
| 无人值守自动更新、由 hub 自供或自选 agent 二进制 | 仅支持管理员触发、由本机更新器按内嵌公钥验签的官方正式 Release 更新。hub 不提供程序、URL、摘要或命令；对安装时选了 hub 来源的节点，hub 只转发官方签名产物，接受与否由节点上的验签决定（§4.10） |
| 插件系统 | 第三方代码在 hub 进程内执行，与远程执行同类。主题的代码跑在访客浏览器里，不是同一件事 |
| 自动主题市场与任意 URL 安装 | 不维护远程市场目录，不自动发现或更新主题。管理员可显式选择公开 GitHub Release 的已构建 ZIP 资产，hub 按限定目标、重定向和地址策略下载并校验后本地托管，不执行源码构建 |
| OAuth、多用户 | 单管理员足够；支持 TOTP 与无密码 Passkey |
| 多数据库方言、外接时序库 | 目标规模内单文件 SQLite 足够 |
| mTLS | 见 §5.5 |
| hub 自行终止 TLS | 见 §5.4 |

**目标规模**：上百到数百节点，历史保留数月。

## 2. 技术选型

| 项 | 选择 | 理由 |
|---|---|---|
| 语言 | hub 与 agent 都用 Go，单 module | 协议类型编译期共享；`CGO_ENABLED=0` 即可交叉编译到冷门架构 |
| agent 平台 | Linux、macOS | Linux 侧直读 `/proc` 与 `/sys`（磁盘 I/O 速率来自 `/proc/diskstats`，整盘判定用 `/sys/block`），采集层零第三方依赖；darwin 侧以 build tag 隔离，其依赖不链入 Linux 二进制 |
| 通信 | ConnectRPC，agent 侧仅 unary | 见 §4.1 |
| 契约 | protobuf，`proto/` 为单一事实源 | 同时生成 Go 与 TS；字段号使改名安全；`optional` 区分"无读数"与"读数为 0" |
| 存储 | 单文件 SQLite，纯 Go 驱动 `modernc.org/sqlite`，WAL | 保持无 CGO、单二进制单文件部署 |
| 前端 | React + Vite + TS，uPlot 画图，产物 `go:embed` 进 hub | Connect 官方查询层现成；uPlot 专做时序、体积小 |

## 3. 架构

### 3.1 仓库布局

```
proto/heron/v1/       agent.proto / admin.proto / public.proto / query.proto（管理与公开共用的历史查询类型）/ types.proto / access.proto / cache.proto（cache_max_age_s 方法选项）
proto/SKILL.md        agent 技能文件；与 proto 源文件一同由 proto/embed.go 嵌入 hub（§5.6）
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
| `heron.v1.AgentService` | agent；`GetRelease` 的调用方是节点上的更新器（§4.10） | Report 与 GetRelease 用运行 bearer token；Register 用注册窗口 key 或一次性安装凭据 |
| `heron.v1.AdminService` | 管理面板；agent 与脚本 | 会话 cookie；标为只读的方法另接受 API token（§5.6） |
| `heron.v1.PublicService` | 公开页、第三方主题 | 无，按来源键限流（§5.3） |
| 主题沙箱（§10.1） | 访客浏览器 | 与面板共用 URL 域名，iframe 不授予同源权限；仅通过 SDK 消息通道读取固定的公开接口 |

鉴权由"服务挂载时绑定的拦截器"承载，不在方法内逐个检查：新增方法无法漏掉鉴权，因为不存在未绑定拦截器的挂载点。

`AdminService` 的每个方法用 `heron.v1.access` 选项声明准入口径：`ACCESS_LOGIN`（仅 `Login`，凭据是请求体里的密码）、`ACCESS_READ`（会话或 API token：无副作用，不列出或管理凭据，也不回显可能含密钥的内容——配置本身，或外部接收方对它的回显）、`ACCESS_SESSION`（仅会话：有副作用的方法；凭据管理——包括只读的 `ListApiTokens`，自动化进程没有理由知道还有哪些 token 存在；回显可能含密钥的配置的方法——`ListNotifyChannels` 会回显 webhook 的请求体模板，模板里可能放着密钥，而 agent 不需要通知渠道的配置；`GetAlertDeliveryError` 返回投递失败的原文，接收方可能在错误响应里回显收到的请求体）。拦截器在构造时从生成的描述符读出整张表，任一方法未声明即 panic：未声明的方法无法随 hub 启动，因而不存在"漏标时默认放行还是默认拒绝"的取舍。准入口径与方法定义写在同一处，proto 仍是单一事实源。它与 `idempotency_level` 是两件事：后者决定是否接受 GET，`AdminService` 一律不标（§3.3）。

公开数据使用独立的消息类型（`PublicNode`、`PublicSnapshot`），不对 `Node` 做字段过滤。由此默认方向是"私有"：给 `Node` 加字段不会出现在公开页，必须显式加入 `Public*` 消息才公开。

### 3.3 方法清单

- `AgentService`：`Register`、`Report`、`GetRelease`（§4.10）。
- GET 准入由装配期的显式检查承载：`heron.v1` 的每个方法接受 GET 当且仅当声明了正的 `cache_max_age_s`，遍历包内全部服务，任一方向不符 hub 起不来。
- `AdminService`：`Login`、`Logout`、`ListSessions`、`RevokeSession`；节点 `ListNodes`、`CreateNode`、`UpdateNode`、`DeleteNode`、`RotateNodeToken`、`ReorderNodes`、`MoveNodes`（§10 节点顺序）；注册窗口 `OpenRegisterWindow`、`CloseRegisterWindow`、`GetRegisterWindow`；数据 `GetSnapshot`、`QueryMetrics`、`QueryProbes`、`GetTraffic`、`AdjustTraffic`；探测 `ListProbeTasks`、`SaveProbeTask`、`DeleteProbeTask`；告警 `ListAlertRules`、`SaveAlertRule`、`DeleteAlertRule`、`ListAlertEvents`、`GetAlertDeliveryError`、`ListNotifyChannels`、`SaveNotifyChannel`、`DeleteNotifyChannel`、`TestNotifyChannel`；维护静默 `ListSilences`（`ACCESS_READ`）、`SaveSilence`、`DeleteSilence`（仅会话，§9.5）；设置 `GetSettings`、`UpdateSettings`、`GetStorageStats`、`GetHeartbeatStatus`（§9.6，`ACCESS_READ`）；标签 `ListTags`（`ACCESS_READ`）、`DeleteTag`；主题 `UploadTheme`、`ListThemes`、`EnableTheme`、`DeleteTheme`、`DeleteThemeVersion`、`GetThemePreview`、`GetThemePackage`、`PreviewTheme`、`ListThemeReleases`、`InstallThemeRelease`（§10.1，全部仅会话）；API token `ListApiTokens`、`CreateApiToken`、`DeleteApiToken`；自描述 `GetApiReference`。
- `PublicService`：`GetSite`、`GetSnapshot`、`QueryMetrics`、`QueryProbes`、`ListProbeComparisonNodes`、`QueryProbeComparison`。后四者只对 `public = true` 的节点应答，对其余节点与不存在的节点返回同一个 `NotFound`（对比查询里不可见节点改列进 `unavailable_node_ids`，不给 404）；对比候选查询同样只看公开节点，候选为空与任务不存在回同一个 `NotFound`。公开节点谓词 `n.public = 1` 是 ListPublicNodes、NodeIsPublic 与对比候选三处共用的同一片段。读量额度（§6.5）在管理与公开两端同一口径：公开端超额同样返回 `FailedPrecondition`。
- 节点标签批量写入：`AdminService.BatchUpdateNodeTags`（`ACCESS_SESSION`），只接受明确的非空节点集合及标签增删项，语义见 §10。

无副作用标注（`idempotency_level = NO_SIDE_EFFECTS`，决定方法是否接受 GET）按服务的信任模型决定，不按读写决定：`PublicService` 的六个方法全部标注，因而可用 GET 调用并带 `Cache-Control`——它无鉴权（§3.2），不存在会被浏览器环境性携带的凭据，§5.3 的 CSRF 论证在这里不成立，而公开页恰是需要被缓存的那一面。缓存上界分别定：实时快照不超过一个上报间隔（更短无意义，更长会展示过期的在线状态），历史查询（含对比）可更长，站点配置最长。`AdminService` 与 `AgentService` 一律不标：前者是 §5.3 的 CSRF 防线之一，后者的上报本就有副作用。

## 4. agent 协议

### 4.1 为什么是 unary 而不是长连接

hub → agent 的下行包含探测任务列表、上报间隔、facts 请求及受限更新授权，可以在每次上报响应里按版本或任务 ID 对账。更新授权先持久化再下发，同一 ID 的重复交付由本机更新器幂等处理。由此 agent 与 hub 之间不需要应用层连接状态，下列问题不存在：连接替换时迟到的拆除误删新会话、半开连接探测、出站队列满导致推送丢失、握手与首报之间的在线语义、hub 重启后的重连风暴（每个 agent 本来就是每周期一个请求，恢复后的负载就是稳态负载）。

unary 是普通 HTTP POST，HTTP/1.1 即可，过反代与 CDN 无需特殊配置。不使用 bidi streaming：它要求 HTTP/2 端到端，反代到 hub 这一跳需要显式配置。

连接断开不等于节点宕机——绝大多数断开是网络抖动、反代空闲超时或 hub 重启。长连接方案要避免由此误报，仍须叠一层宽限期，叠完就回到了 TTL 语义，只是额外背上连接状态。更根本的是 §4.4 的不变式会碎：一旦存在连接，"连接在但没有数据"与"数据刚到但连接已断"是两个无法归并的状态，而哪个算在线没有正确答案。

代价：每次上报多一份 HTTP 头部；离线发现由"连接断开即知"变为"TTL 到期才知"。该代价有明确上界，且是被优先满足的约束而非残值——见 §4.4。

### 4.2 消息

```proto
service AgentService {
  rpc Register(RegisterRequest) returns (RegisterResponse);
  rpc Report(ReportRequest)     returns (ReportResponse);
  rpc GetRelease(GetReleaseRequest) returns (GetReleaseResponse);  // 调用方是 hub 来源的更新器（§4.10）
}

message GetReleaseRequest {
  string task_id = 1;   // 节点当前更新任务的 ID；版本由 hub 从该任务取得，请求里没有版本
  string arch = 2;      // agent 产物矩阵里的架构
}

message GetReleaseResponse {
  bytes sums = 1;       // 官方 SHA256SUMS 原文
  bytes signature = 2;  // 官方 SHA256SUMS.sig 原文
  bytes archive = 3;    // 官方归档原文
}

message ReportRequest {
  Metrics metrics = 1;
  repeated ProbeResult probe_results = 2;   // 一次至多 1024 条（probelimit.MaxResultsPerReport），多余留待下一次上报
  uint64  tasks_version = 3;   // agent 当前持有的探测任务版本
  fixed64 facts_hash = 4;      // agent 静态信息的摘要
  Facts   facts = 5;           // 进程启动后的首次上报携带；此后仅在 hub 要求时携带
  UpdateStatus update = 6;     // 本机更新能力、当前版本和最新任务状态
  repeated AgentCapability capabilities = 7;  // 本二进制支持的协议能力，每次上报都带；钉住证书指纹的任务只下发给声明 PROBE_CERT_PIN 的 agent
  bytes    tasks_digest = 8;   // 持有任务清单的内容摘要（agentwire.TasksDigest，算法见 §4.3）；尚未收到任何清单时缺席，与空清单的空串 SHA-256 严格区分
}

enum AgentCapability {
  AGENT_CAPABILITY_UNSPECIFIED = 0;
  AGENT_CAPABILITY_PROBE_CERT_PIN = 1;  // 支持 cert_spki_sha256 钉住的 HTTPS 探测
}

message ReportResponse {
  uint32     report_interval_ms = 1;
  ProbeTasks tasks = 2;        // 带 tasks_digest 时摘要与应下发清单不等才携带；缺席时才退回比较 tasks_version
  bool       want_facts = 3;   // hub 持有的 facts_hash 与请求不一致
  UpdateTask update = 4;       // 已持久化的受限官方更新授权
}

message Metrics {
  string boot_id = 1;   // 空串或 UUID 文本（Linux 的 /proc/sys/kernel/random/boot_id、macOS 的 kern.bootsessionuuid）；hub 拒收其他写法
  optional double cpu_pct = 2;   // 忙时占比；范围见 Facts.execution：环境走 cpu.stat 差分（steal/iowait 不设置），真根/v1 走 /proc/stat
  optional double load1 = 3;  optional double load5 = 4;  optional double load15 = 5;  // host 或 legacy（procfs）或 unknown：来源见 Facts.execution
  optional uint64 mem_total = 6;   optional uint64 mem_used = 7;
  optional uint64 swap_total = 8;  optional uint64 swap_used = 9;
  optional uint64 disk_total = 10; optional uint64 disk_used = 11;
  optional uint64 net_rx_total = 12; optional uint64 net_tx_total = 13; // 内核累计计数器
  optional uint64 net_rx_bps = 14;   optional uint64 net_tx_bps = 15;   // agent 本地采样速率，用于实时与历史峰值
  optional uint32 tcp_conns = 16;  optional uint32 udp_conns = 17;
  optional uint32 procs = 18;      optional uint64 uptime_s = 19;
  string net_counter_epoch = 20;   // 计入网络合计的网卡集合摘要，与计数同次采样
  optional uint64 disk_read_bps = 21;  optional uint64 disk_write_bps = 22;  // 整盘设备采样速率，与 net_*_bps 同一差分规则
  optional double cpu_steal_pct = 23;  optional double cpu_iowait_pct = 24;  // 与 cpu_pct 同一次 /proc/stat 差分
  optional double load1_per_core = 25; // load1 ÷ Facts.execution.load_cores；分母缺失即不设置
}

message Facts {
  string hostname = 1; string os = 2; string kernel = 3; string arch = 4;
  string virtualization = 5; string cpu_model = 6; uint32 cpu_cores = 7;  // cpu_cores 是 agent 所在执行环境的有效核数
  string agent_version = 8;
  bool icmp_available = 9;     // 两种 ICMP socket 是否至少一种可用，见 §8.2
  NetworkInfo network = 10;    // agent 自报的双栈出口；地址只给管理端，公开端只有每个地址族的探测状态（§10）
  AgentDiagnostics diagnostics = 11;  // 最近一次采集的白名单诊断，仅管理端可读
  ExecutionScope execution = 12;  // 执行环境识别快照（下文）；darwin 恒为主机范围
}

message ExecutionScope {
  ScopeKind kind = 1;            // SCOPE_KIND_HOST / _CGROUP_NAMESPACE（挂载根是环境 cgroup）/ _CGROUP_V1_LEGACY / _IDENTIFY_FAILED
  ResourceScope cpu = 2;         // _ENVIRONMENT=本环境可见口径 / _HOST=主机口径 / _LEGACY=cgroup v1 沿用旧读法 / _UNKNOWN=本周期缺读数
  ResourceScope memory = 3;      // 同上
  ResourceScope swap = 4;        // 同上
  ResourceScope load = 5;        // _HOST（/proc/loadavg 为 procfs 时）/ _LEGACY（cgroup v1）/ _UNKNOWN
  optional double cpu_effective_cores = 6;  // 本次识别的有效核数精确值；CPU 范围未知时缺失
  optional uint64 memory_limit_bytes = 7;   // 本次识别的内存可见上限；出现时必为正；对应资源未知或总量读不出时缺失
  optional uint64 swap_limit_bytes = 8;     // 同上；已知的 0（宿主或容器禁 swap）照报，与读数 0/0 一致
  optional uint32 load_cores = 9;           // Metrics.load1_per_core 的分母；缺失即本次不上报按核负载
  repeated ScopeNote notes = 10;            // 固定类别的识别说明，按枚举值升序、去重、至多 8 个；任一资源 unknown 而 kind 不是 identify_failed 时必非空
}

message ProbeResult {
  uint64 task_id = 1;
  uint32 age_ms = 2;           // 测量完成至发送的时长，agent 单调钟
  oneof outcome {
    uint32 rtt_us = 3;
    Timeout timeout = 4;       // 计入丢包
    ProbeError error = 5;      // 无权限、解析失败等；不计入丢包；message 至多 128 字节，agent 按 rune 截断、hub 超长拒收
  }
  optional int64 cert_not_after_s = 6;  // HTTPS 探测握手成功时顺带带回的证书到期时刻（链首枚证书的 NotAfter，Unix 秒）；
                                        // 只在 rtt_us 成功结果上携带，按 (task_id, config_id) 每小时至多一次；optional 区分"没带"与 0
  PresentedCertificate presented = 7;   // 证书相关丢包带回的对方叶证书候选；只随 timeout 结果，按 (task_id, config_id) 每小时至多一次
  bytes task_config_id = 8;             // 产生本结果时任务的配置身份（ProbeTask.config_id 原样回显，空即缺席）；被拒任务留的 error 结果同样带
}

// 证书相关的丢包（默认校验失败、指纹不符、不在有效期）带回握手失败时对方出示的叶证书，
// 作为面板上的信任候选；取自失败链上的 UnverifiedCertificates[0]，不另建连接。
message PresentedCertificate {
  bytes spki_sha256 = 1;      // 叶证书 SubjectPublicKeyInfo 的 SHA-256，恰 32 字节；比公钥而不是整份证书哈希，同钥续签不改变它
  int64 not_after_s = 2;
  PresentedReason reason = 3;
}

enum PresentedReason {
  PRESENTED_REASON_UNSPECIFIED = 0;
  PRESENTED_REASON_CA_VERIFY_FAILED = 1;  // 未钉住，默认校验失败
  PRESENTED_REASON_PIN_MISMATCH = 2;      // 钉住了，出示的公钥指纹与钉住值不符
  PRESENTED_REASON_OUTSIDE_VALIDITY = 3;  // 指纹相符，但证书未生效或已过期
}
```

`boot_id` 放在 `Metrics` 而不是 `Facts`：流量差分必须在同一条消息里同时拿到计数器与它所属的启动周期。若 `boot_id` 随 `Facts` 走，重启后的首次上报只带新计数器，当新计数器已超过旧基线时（长时间断连且流量大），hub 会把它当成同一启动周期内的增量而错误入账。

`disk_read_bps` / `disk_write_bps` 是 agent 两次本地采样之间整盘设备读 / 写字节速率合计（bytes/s）。数据源 `/proc/diskstats`，其扇区字段内核一律按 512 字节计（与设备报告的逻辑扇区无关，不另查扇区大小）；只计整盘设备：`/sys/block/<name>` 存在（分区没有这一项）、且名字不以 `loop`、`ram`、`zram` 开头，`dm-*` 与 `md*` 也排除——映射 / 聚合设备的 I/O 与组成它的底层盘重复，计了会把同一次读算两遍。差分规则与 `net_rx_bps` / `net_tx_bps` 相同：首样本、任一设备计数回退、设备集合变化时本次不设置（`optional` 缺失）而不是报 0；darwin 上两项不设置。

`cpu_steal_pct` / `cpu_iowait_pct` 与 `cpu_pct` 取自同一次 `/proc/stat` 两次采样差分：`steal_pct = Δsteal / Δtotal × 100`、`iowait_pct = Δiowait / Δtotal × 100`。`cpu_pct` 的口径不变，忙时不含 iowait（`idle` 计为 idle + iowait）。三者互相独立，都不是对方的子集，可以同时显示。`Δtotal = 0` 或任一计数回退时三项一起不设置，不单独保留某一项；darwin 没有 steal 概念、iowait 也不可得，两项不设置；cgroup 有限额时两项同样不设置（见下段）。

执行环境识别（spec 本节，实现 `internal/agent/collect/execscope.go`）：agent 每个上报周期先做一次识别得到快照，`Metrics` 与 `Facts` 只读快照——识别文件（mountinfo、`cgroup.type`、`cpu.max`……）每周期只读一遍，同一周期两者的范围与容量一致。识别的输入只取进程自己能看到的挂载与文件，判据按层递进，任何一步的结论都不是猜的：

1. `/sys/fs/cgroup` 的 statfs 类型不是 cgroup2 → `cgroup_v1_legacy`：读法全部沿用旧口径（CPU 走 `/proc/stat` 差分、内存走 meminfo），四种资源都标 legacy；进入该形态或从它切走时各记一行日志，不按周期记。v1 与"没有 cgroup2"不区分（对读数没有影响）。
2. 是 cgroup2，再 stat 挂载根上的 `cgroup.type`：文件存在 → 挂载根是一个环境 cgroup（容器 / LXC guest / OrbStack 机器自身的 cgroup namespace 根），`kind = cgroup_namespace`；`ENOENT` → 挂载根是真根（内核只在非根 cgroup 上提供 `cgroup.type`），`kind = host`；其他错误 → `identify_failed`，依赖识别的资源全部缺读数，说明里记失败类别。识别失败时报数缺位，不硬编一个口径。
3. 被消费的四个 `/proc` 文件（`stat`、`meminfo`、`loadavg`、`cpuinfo`）逐个判来源：stat 该文件拿 `st_dev`，在 `/proc/self/mountinfo` 里按设备号找到所在挂载，fstype 是 `proc` → procfs、`fuse.lxcfs` → lxcfs、其余 → other；stat 或 mountinfo 读不了 → 来源 unknown。判据落在每个文件自己身上：lxcfs 是逐文件 bind，叠加挂载与父目录 overmount 下，同一 `/proc` 里不同文件可以来自不同文件系统（实验确认，见 §13-12）。来源 unknown 的文件按"读不出"处理，绝不退回 procfs 假定——宁可缺读数也不报错值。

`kind` 的语义是信任边界：挂载根说真根、容器标识（`/.dockerenv`、`/run/.containerenv`、`/proc/1/environ` 的 `container=`、`/run/systemd/container`）却存在时，读数仍是整机，只记说明（`container_signal_on_host_root`），不从标识推断隔离状态。CI 托管 runner 是这个边界的实证：agent 自己跑在 `system.slice/xxx.service` 里（`/proc/self/cgroup` 指向它），但 cgroup 挂载共享自宿主、挂载根是真根——按 `/proc/self/cgroup` 定口径会把宿主整机错标成某个 slice 的环境，按挂载根则正确得到主机口径（实验确认，§13-13）。

识别之后的读数口径（决策表；`有效核数 = min(cpu.max 折算的配额核数, cpuset 核数)`，cpu.max 缺失或 `max` 视为不限；cpuset 文件缺失时回退 `/proc/cpuinfo` 处理器数，仅当其来源是 procfs（主机核数）或 lxcfs（该环境的 cpuset 视图），否则 CPU 未知）：

| 资源 | 真根 | 环境 cgroup | v1 / 无 cgroup2 |
|---|---|---|---|
| CPU | `/proc/stat` 为 procfs：忙时占比 + steal/iowait；否则未知 | `cpu.stat` 的 `usage_usec` 差分 ÷ (有效核数 × Δt)，>100 钳 100；steal、iowait 不设置；cpu.stat 或有效核数不可知 → 未知 | 沿用 `/proc/stat` 差分（含 steal/iowait） |
| 内存 | meminfo 为 procfs：`MemTotal − MemAvailable`；否则未知 | used `= memory.current − (file − shmem) − slab_reclaimable`（memory.stat；防两次读竞争的回绕检查）；控制器文件缺失或读不出 → 未知并记说明 | 沿用 meminfo |
| swap | 同上文件要求：`SwapTotal − SwapFree` | used `= memory.swap.current`；记账文件缺失 → 未知并记说明 | 沿用 meminfo |
| 负载 | loadavg 为 procfs：范围主机；否则未知并记说明 | procfs → 范围主机；lxcfs / 其他 → 未知并记说明 | 沿用 loadavg |

可见上限与环境内存 / swap 的 total：`min(controller 上限, meminfo 总量)`，meminfo 只在其来源是 procfs 或 lxcfs 时参与（lxcfs 的 MemTotal 是 guest 视图，与挂载根 memory.max 同为"本环境可见"这一层，实验确认 §13-10/12）；meminfo 来源不可用时内存 total 就是 `memory.max` 数值（swap 同理取 `memory.swap.max`），controller 上限为 `max` 且 meminfo 不可用 → 该资源未知。swap 的上限折算为 0 是已知的合法值（宿主没有 swap、或 Incus 把 `memory.swap.max` 写 0），照报 0 且读数为 0/0；内存上限出现时必为正。lxcfs 的 meminfo 按 guest 而不是读者所在的 cgroup 给值：guest 里 MemoryMax=128M 的服务单元内读 `/proc/meminfo`，MemTotal 仍是 guest 限额 512MiB（实验确认，§13-12）。

差分基线键：CPU 基线键是（来源身份, 有效核数）——环境 cgroup 路径以挂载根 cgroup 目录的 `dev:inode` 标识（容器重建即变），真根 / legacy 以 `/proc/stat` 固定标识加当次核数。键变化即丢弃旧基线、本周期按首样本处理：不共享分母的差分没有意义。`ResetRates` 与差分规则（首样本、回退、Δt ≤ 0 不设置）不变。

`cpu_cores`（Facts）只来自本周期识别的有效核数，取上取整（1.5 核报 2）；识别不出是 0，不再退回 `runtime.NumCPU()`——那会把识别失败伪装成一个像样的值。旧 agent 一律报物理核数，这种版本漂移按 §4.6 接受：该值只做展示，不参与 hub 侧计算，也不再当作按核负载的分母。`load1_per_core = load1 ÷ execution.load_cores`，分母来自同一快照（真根或 v1：loadavg 与 cpuinfo 都为 procfs 时的主机核数；环境 cgroup：负载范围为主机、且 cpuinfo 为 procfs时），缺失即不设置；出现时 `load1` 必然同时出现。hub 原样保存这个商，不再用 Facts 的核数去除。`Facts.execution` 由 agent 构造后先过 `agentwire.ValidateExecutionScope` 自检（枚举完备、kind 与四种范围的组合、容量的正性与范围配套、说明去重至多 8 个）：非法块即本方构造 bug，不发出。hub 在上报准入与快照恢复读 `node_facts` 时调用同一个函数；`cpu_cores` 另有上界，与执行环境容量共用 `MaxScopeCores`（65536），超过整条拒绝。`execution` 缺失（旧 agent）存 JSON null，与已上报的对象不同，管理端显示「未上报」，不进公开投影。

darwin 恒为主机：识别快照固定为 host 范围、核数取 `hw.logicalcpu`，不经过上述判据。

### 4.3 对账

探测任务、主机信息与上报间隔通过版本 / 摘要对账，hub 在响应里补齐差异。Facts 包含管理面白名单诊断：生效网卡包含/排除规则、实际计入名称与总数、固定采集失败类别、实际生效间隔。采集失败不上传原始错误文本；规则不含任意配置，凭据与命令行没有协议字段。诊断随内容变化推进 Facts 摘要，不加入每次采样时间；内容未变不会触发重复写库。旧 Agent 未提供时为 nil，存储为 JSON null，不等同于已提供但无失败。公开 Facts 显式保留该字段号和名称，不转交主题。管理详情按 10 秒轮询，显示最近保存时间并说明不是实时健康保证。

`node_facts.facts_rev` 记录写入时的持久化字段集合版本（常量 `factsPersistRev`）。启动加载摘要时只采用版本相等的行：升级前的行与旧快照恢复出来的行缺省是 0，摘要没有覆盖现在要存的字段（含 `execution`），即使 agent 不重启、Facts 内容也没变，下一次上报仍会重新索取并按当前版本落库。旧 agent 重新索取后仍是未上报，不是某种默认范围。以后持久化的字段集合变了，只把这个常量加一。

网卡规则每组最多 64 项、每项 128 字节，由本机启动和 Hub 上报使用共享校验；包含非空时上报的排除为空。接口最多展示排序后的前 128 项（每项 64 字节），总数明确表示是否截断；集合摘要和累计计数仍使用全集。上报、落库、读库和快照恢复共用诊断校验。schema 26 为 node_facts 增加 diagnostics、为 traffic 增加 net_counter_epoch，旧库和配置快照迁移时分别默认为 null 和空标识。

任务清单除版本计数外另以内容摘要对账：`ReportRequest.tasks_digest` 是 agent 收到并持有的整份清单（含被 `probelimit.CheckTask` 拒绝的任务）的 SHA-256——任务按 `task_id` 升序、各自确定性编码、前缀 8 字节大端长度后拼接取哈希，算法唯一实现于 `agentwire.TasksDigest`，agent 上报与 hub 决定是否重发清单调用同一函数。摘要不含版本计数：版本在 hub 备份恢复后可能与 agent 持有的内容错位，按内容摘要不会。空清单是空串的 SHA-256，与"尚未收到任何清单"（字段缺席）严格区分，两者是两个不同的对账状态。每条结果原样回显产生它时的任务配置身份（`ProbeResult.task_config_id`）：证书观测以 (task_id, config_id) 归属一份配置，身份不符的观测 hub 丢弃而探测结果本身保留。

### 4.4 在线判定

`live` 中每节点一个条目，同时持有最新指标与 `last_seen`（单调钟）。在线 ⇔ `now − last_seen < TTL`。"在线"与"有最新数据"是同一个事实，只有这一个来源；不存在第二张在线表。节点从首次成功上报起即在线。

TTL 是**离线发现延迟的上界**，也是这条链上唯一被直接配置的量：环境变量 `HERON_OFFLINE_AFTER`，默认 30s，下限 10s，上限 180s。其余三个量由它反推，不各自取值：

| 量 | 取值 | 依据 |
|---|---|---|
| TTL | `HERON_OFFLINE_AFTER`，默认 30s | 掉线多久应当被看见是产品指标，由部署者定 |
| 下发的上报间隔 | `TTL / 3` | 一个 TTL 内有三次上报机会，容得下两次连续失败而不误判离线 |
| agent 退避上限（§4.7） | `TTL` | 恢复后重新可见的时长与掉线被发现的时长同一预算 |
| 离线告警宽限期（§9.1） | 下限为 TTL | 宽限期短于 TTL 会在面板仍显示在线时告警；两个口径必须同向，由保存规则时的显式校验承载 |

方向不可倒置：不能先按带宽预算选上报间隔、再让 TTL 从中掉出来。间隔是实现细节，TTL 是使用者唯一能感知的那个数。

上限来自结果排空：上报间隔是 TTL/3，一次上报至多携带 `MaxResultsPerReport`（1024）条探测结果，必须能排空 `MaxTasksPerNode / MinIntervalS`（64 任务 / 5 s）的满速产出并留余量——超过 hub 读上限（256 KiB，由这些常量推导）的请求会被整条拒绝并回队，形成永久失败。这条关系由编译期断言钉住，放宽任何一个常量都必须重新审视。

### 4.5 时钟

上报覆盖率与在线判定独立：一分钟被 hub 观测，要求 HTTP 接收开关在分钟起点之前打开、在终点之后才关闭；live 每 2.5 秒按单调钟调度取墙钟/单调钟读数，相邻单调间隔不超过 5 秒，且两钟增量之差的绝对值不超过 2 秒。5 秒以内的停顿可能检测不到；这不是逐请求可服务证明，也不是在线率或 SLA。Flush/Drain 持 live 锁取封口读数后才定案，分钟内与分钟结束至封口之间的跳变参与检查；证据不足写 observed=0，不补纯观测行。定案不可撤销，回拨重入已定案分钟不重新判断。

不信任 agent 墙钟。指标由 hub 在收到时打点；探测结果用 `age_ms` 反推测量时刻。hub 内部凡是时长一律用单调钟——墙钟被 NTP 向后拨时差值为负，用它做除数会得到离谱的速率。

单调钟不含休眠时间（Linux 的 `CLOCK_MONOTONIC`、darwin 的 `CLOCK_UPTIME_RAW` 都如此），休眠会同时毒化 agent 的两条时间线：休眠前入队、唤醒后上报的探测结果，`age_ms` 比真实年龄少了整个休眠时长，hub 反推的测量时刻落到唤醒之后；速率差分跨越休眠时分子含休眠前后的计数变化、分母不含休眠时长，算出的速率偏大数十倍，而"计数回退不置速率"的既有防线照不到这种情形。队列里没有任何字段能区分这类结果，只能整体作废：agent 每轮上报循环开头取一对 `(wall, mono)`，与上一轮相比 `|Δwall − Δmono| > 20s` 即休眠信号（取绝对值，墙钟被向后拨同样触发）；触发时丢弃探测结果队列中此前入队的全部结果（计入丢弃统计）、重置采集器的全部速率与差分基线、记一行日志（两个增量的数值）。判定先于本轮采样，本轮即新基线的首样本——下一周期没有速率、探测结果从头积累，这是设计效果。NTP 步进同样触发，接受：效果等同一次基线重置，代价是一个周期无速率与至多 `MaxResultAge` 的积压结果被弃，不为区分两者再加判据。20s 的推导：一轮的长度上限由上报间隔的上限保证（hub 下发间隔经 `ClampReportInterval` 限定为 `MaxTTL/3`=60s，失败退避封顶 3 倍即 180s），NTP slew 上限 500 ppm 在一轮内产生的两钟偏差不超过 90ms，是毫秒量级；真实休眠至少数十秒；20s 远大于前者、小于后者，且小于 `MaxResultAge`（120s）——跨越休眠 S 的结果真龄比单调钟年龄大 S，S ≥ `MaxResultAge` 时它们必然超龄应弃，阈值更小保证这类休眠必然被判出，不会先被当作合法结果上报。

### 4.6 版本偏斜

agent 与 hub 不同时升级，版本号也分开推进：每个 hub 版本绑定一个 agent 版本，只改 hub 的版本不要求节点升级（§14.1）。hub 必须接受旧 agent 的上报（缺失的 `optional` 字段 = 无读数）；agent 忽略响应里不认识的字段（protobuf 默认行为）。`buf breaking` 以 `WIRE_JSON` 级别在 CI 中守线上兼容——第三方主题以 JSON 调 `PublicService`，字段名同样是契约。

### 4.7 失败与退避

上报失败时 agent 做带抖动的指数退避，上限取 TTL 而非独立取值（§4.4）：上限若长于 TTL，hub 短暂不可用后早已恢复，面板上却仍显示掉线。抖动负责把恢复后的重试摊开，成功后回到下发间隔。指标不缓存（过期的实时数据没有意义）；探测结果缓存至多 `MAX_AGE`（120s，其取值约束见 §6.4），超龄丢弃；hub 以 `InvalidArgument` 拒绝的请求不回队——确定性拒绝重发无益。回队是至少一次语义：hub 已入账但响应丢失时同一批结果会重复计入，协议未做去重，精确一次留待后续里程碑。流量不因断连丢失：计数器是累计值，恢复后的首次差分覆盖整个断连区间（前提见 §7）。

### 4.8 注册

`heron-agent register --hub <url> --key <key> [--name <name>] [--insecure-http]` 调 `Register`，把节点 token 写入配置文件（权限 0600）。hub 地址在发请求之前按 §5.7 的传输规则校验（被拒时 key 不出线、窗口名额不消耗），`--insecure-http` 把放行明文 http 的决定写进配置。已有配置时沿用其中的 `probe_allow`、`probe_deny`：重新注册换的是 hub 身份，探测策略属于宿主机；已有配置读不出来时报错，不静默丢掉它。安装脚本只负责下载、校验、调用这条命令与安装服务单元，脚本里不解析 JSON。

配置里除 hub 地址、token 与名字外的字段都是宿主机本地策略（`insecure_http`、`probe_allow`、`probe_deny`，§5.7、§8.4），由 `heron-agent configure --config <path> [--insecure-http=true|false] [--probe-allow CIDR,...] [--probe-deny CIDR,...]` 修改：只改命令行上显式给出的项，列表给空串即清空；读取时不校验（才能修正一份升级后被 `run` 拒绝的配置），写入前按 `run` 加载时的同一套规则校验整份配置，打印修改后的本地策略；目标文件已存在时保留它的属主与权限（root 执行 configure 不能把服务用户读不到的文件留给 `run`）。配置里的未知字段、以及第一个 JSON 对象之后的任何内容都是错误：策略字段拼错时若静默忽略，宿主机以为拒绝了的地址实际放行。agent 只在启动时读配置，改完要重启服务。hub 无法修改这些字段：agent 运行期不写配置，下行消息里也没有对应字段。

### 4.9 节点来源地址与国家 / 地区

来源地址：hub 记录每个节点最近一次上报的来源地址——只用 hub 在 `Report` 上看到的对端地址，经 `auth.ClientIP` 按 `--trusted-proxies` 解析；不读 `CF-Connecting-IP` 之类的旁路头（§5.4 的"不从请求头推断"），agent 也不自报地址：那是 agent 的自述，与"hub 看到什么"是两个事实，混在一列里无法区分。hub 在反代之后而未把反代列进 `--trusted-proxies` 时，记下的是反代地址（与 §5.3 登录通知的来源地址同一口径，文案不遮掩）。存 `node.last_source`（规范化文本；空即 hub 没有记录到来源——从未上报，或最近一次上报早于 hub 开始记录来源的版本，后者 `last_seen_at` 有值而这里为空），只存最后一个——v4 与 v6 交替上报时留最后一次，面板看到的就是最近一次的事实；与 `last_seen_at` 同一路径落盘（随分钟行刷出与退出时写入），`Report` 路径仍只碰内存，`live` 的条目携带它。可见范围仅 `AdminService` 的 `Node`（会话与 API token 均可读）；`PublicNode` 里连字段号都不分配。不做手动覆盖：地址是观测事实，手动值写在备注里。hub 与节点同在内网、或 agent 经出口代理时看到的是内网或代理地址，照实记录。

国家 / 地区：每个节点一个 ISO 3166-1 alpha-2 国家码，面板与公开页显示为徽章（旗帜用 Unicode 区域指示符，不引入图片资源）。来源两条：查询服务开启时（默认开启）由 hub 按来源地址自动查得，查得的国家与它所属的地址成对存放（`node.country`、`node.country_ip`）；或由管理员逐节点手动指定（`node.country_pin`）。手动值优先且互不覆盖：查询不改手动列，手动列清空即回落查得值；手动列只有 `UpdateNode` 一个写者，查得两列有刷出（换地址即清空）与查询器（条件写入）两个写者，但没有哪个写者同时写两边，冲突不需要裁决。查询默认开启：开关是 `setting` 里的键 `geo.enabled`，从未保存过时为开，保存过的开或关照旧生效。默认开启，是因为国家徽章是面板与公开页的常用信息，默认关闭时新装的 hub 在运维开启之前没有查得的徽章；代价是 HTTP 后端从安装起就把节点的公网来源地址发给 `geo.url`。hub 出网到第三方仍是信任边界上的事实：面板写明当前后端与发往的服务，运维可随时关闭查询，或用 `--geo-mmdb` 改为本机查询、不出网。服务地址来自设置（键 `geo.url`，默认 `https://ipinfo.io/{ip}/country`，可回显、不算凭据），代码不得在配置之外另有出站目标，让"hub 会连到哪里"能从配置读出。开关与服务地址在 `UpdateSettings` 里都是 `optional`、缺席即不变（与 §10 总闸同一口径）：这两项决定 hub 是否、向谁发送节点地址，只改外观的老客户端若把它们整体替换成内置值，会顺手把运维选定的服务换回默认并开始向它发地址。服务地址必须含 `{ip}`，且 `{ip}` 不得出现在主机或端口位置（含 `[{ip}]` 的 IPv6 字面量写法——否则 hub 会直连节点自己的任意端口，而 IPv4 节点又会因 `[…]` 里不是 IPv6 而每次失败），必须出现在路径或查询串这些随请求发出的部分（只写在 `#` 后的片段里时请求不带地址，每个节点的请求都一样，服务若按请求方地址作答，所有节点都会被记成 hub 所在的国家且不再重查；判定是两族样例填入后请求行必须不同），填入样例地址后是带 host 的绝对 http(s) URL、不含用户信息（net/http 会把 URL 里的用户信息转成 Basic 认证头，等于给"不带任何凭据"开了后门）、不超过 2048 字节（解码预算的推导前提）。应答只认 200。查询只发地址、不带任何凭据；响应去掉首尾空白后只接受两个大写字母，其余按失败处理，响应体不进入任何解释路径；出站客户端复用 §9.3 通知那一个（不跟随重定向、限读响应体）。每节点每地址至多查一次——查询器在内存里记住（节点, 地址）→ 国家的答案，每节点只保留最近 4 个地址（超过 4 个地址轮换的节点会再次外呼，对外文字要写出这个上界，不能许诺无条件的"至多一次"），节点在 v4 与 v6 之间交替时不重复外呼，命中即直接写回；失败按小时级退避重试（退避的键含服务地址，运维换了服务即重查），hub 重启后对尚无答案的地址重查一次——无退避会让一个坏链路的节点每次上报都触发一次外呼；每次外呼前都重新读开关与服务地址，关闭之后至多还有一个在途请求（上界是客户端总超时），不让一轮开头读到的设置授权整轮几十分钟的外呼；地址变了国家即清空并重查，迟到的应答只在地址仍一致时写入：国家是"对某个地址"的答案，不是节点属性，换了出口的节点不得沿用旧答案。非公网地址（RFC 1918、CGNAT 100.64/10、回环、链路本地、ULA、未指定、组播与文档保留段）不发出查询：这类地址没有国家，发出去只是把内网拓扑交给第三方。公开：`PublicNode.country` 只放行最终显示的那个国家码，不放行地址与来源——公开页表达"在哪个区域"，不定位机器；§12 的允许列表随之改。面板显示国家、来源（手动 / 查得）与查得于哪个地址。开关与服务地址属备份的配置层（备份落地时登记）。本地 mmdb 后端：`serve` 加 `--geo-mmdb <路径>` 指向一份 MaxMind 格式的国家库（GeoLite2-Country 或同格式），用纯 Go 读取器（`github.com/oschwald/maxminddb-golang/v2`）在本机查询，完全不出网；配了它就不再向 HTTP 服务发任何请求（mmdb 优先，`geo.url` 保留但不生效，面板写明当前后端是"本地文件 <路径>"还是"HTTP 服务 <地址>"）。查询语义与上面完全一致，只换后端：成对存放、手动优先、非公网不查、每地址至多一次、失败按小时退避（本地查不到也算失败，退避表的作用是免得每轮都查同一个没有答案的地址）、公开只放行国家码；`geo.enabled` 仍是唯一开关——它表达的是"要不要给节点标国家"，后端不同只改变"地址会不会离开本机"，面板在 mmdb 后端下写明不出网。路径是部署配置而不是运行配置，所以是启动参数不是设置：文件读不到或不是 mmdb 格式在启动时报错退出，不静默退回 HTTP——静默退回会让运维以为没出网而实际在出网。启动时把库文件整读进内存并做结构校验（读取器的 Verify 只查搜索树与数据段的结构损坏；库文件没有校验和，把合法值换成另一个合法值——写错的国家码——它查不出，由答案校验与人工核对承担；非法 UTF-8 这类结构性损坏才会被拒），运行期不再访问该文件：原地覆盖或截断都不影响运行中的答案，替换文件后重启才生效，这一条由构造保证而不是靠运维遵守"先写临时文件再改名"；文件大小上限 256 MiB，超出按配置错误启动失败。启动日志记录选定的后端，mmdb 下另记路径与库元数据（数据库类型、构建时间），运维据此确认不出网与加载的库版本。本地查不到（无记录或缺国家码）以明确的"无记录"错误进退避，日志不沿用 HTTP 口径的文案；退避键里的"服务"一项由后端给出（HTTP 为服务地址，mmdb 为库路径），所以 mmdb 下改动不生效的 `geo.url` 不会清掉退避。后端拿到的开关与服务地址是 Resolver 判定准入时读到的同一份快照，后端不得另读设置；面板回显的后端描述取自 hub 实际选定的后端对象，不另由启动参数推导。文件更新靠重启，不做热加载。

### 4.10 更新产物的签名与 hub 中转

更新器接受哪些字节只由发行签名决定，从哪里取字节由本机安装参数决定，两者互不推导——与安装脚本的 `--base-url` 同一个拆分（§5.7 安装链路）。此前接受的依据是"经 HTTPS 从 GitHub 取到"，传输与信任绑在一起：出站只通到 hub 的节点没有这条传输，而换一条传输就失去了区分官方程序与失守 hub 所塞程序的依据。

- 签名：release 流水线对 `"heron-release-v1\n" + <tag> + "\n" + <SHA256SUMS 原文>` 做 Ed25519 签名（Go 标准库 `crypto/ed25519`），签名的 base64 一行作为 `SHA256SUMS.sig` 随 release 发布。私钥是 GitHub Actions secret，只有 release 流水线读取；持有仓库发版权限就能触发签名，信任根与此前的"官方发行权限"同一范围（§5.7）。被签消息的构造、签名文件格式与公钥都在 `internal/releasesig`，它只依赖标准库（签名工具在持有私钥的 job 里编译运行它，见 §14）。公钥以常量写在源码里，不经 ldflags 或 build tag 注入：换公钥必然是仓库里一次可审阅的改动，正式二进制里不存在第二把。公钥是列表，留出轮换的位置；更新器只能由 root 安装器升级（§5.7），所以轮换或私钥泄漏后，信任集合的变化要每台主机重跑安装器才生效。
- 唯一接受规则：两种来源、hub 与 agent 两种角色相同，由更新器的事务引擎在下载之后调用一次，来源只负责取回三份原始字节（`SHA256SUMS`、签名、归档），自己不做任何接受判定——判定只在一处，就不会有哪条来源漏掉其中一步。规则依次为：任务版本通过 `ValidVersion` 且新于本机当前版本；用更新器手里的任务版本号拼出被签消息，以任一受信公钥验签——版本号不取来源给出的值，拿 vA 的签名冒充 vB 验不过（否则失守的 hub 能把旧版字节标成高版本装上，此后真正的新版因"不比当前新"被拒，节点永远停在旧版）；归档的 SHA-256 在已验签的 `SHA256SUMS` 里且条目唯一；归档结构检查保持现有规则（只含主程序与已知服务文件、都是普通文件、无扩展头、gzip 读到流尾、无尾随数据）。预发布 tag 过不了 `ValidVersion`，草稿 release 没有公开下载地址，所以 GitHub 来源不再调 releases API 查草稿、预发布与资产清单：下载地址只由固定仓库与版本拼出，三份文件各有大小上限。hub 的最新版本查询仍走 API（§5.4），与接受规则无关。
- 来源：hub 角色固定为 GitHub。agent 角色由安装参数 `--update-source github|hub` 决定（§14），写进 root 属主的 `/etc/heron-update-agent/config.json`（形如 `{"source":"hub"}`），不放进 agent 配置：agent 配置属服务用户（§14），"从哪里取字节"是 root 的决定。文件不存在即 github；这不是放宽，两种来源的接受规则相同。文件存在但读不出、有未知字段、第一个 JSON 对象之后还有内容或取值不认识时，更新器照常运行，状态报 `supported=false`，reason 写明文件路径，不崩溃重启：崩溃循环在面板上只表现为"没有上报更新能力"，看不出原因。
- hub 来源的连接：更新器在每个任务开始下载时读 agent 配置（`/etc/heron-agent/config.json`，按服务用户属主、单链接、普通文件、不可被组与其他用户写的检查打开后限量读取），取 hub 地址、token 与 `insecure_http`；解析与 agent 是同一个实现，地址用 §5.7 传输规则的同一个校验函数——更新器没有自己的一套 URL 规则。每个任务现读，节点 token 轮换后不需要同步。HTTP 客户端与 agent 共用同一个构造：不跟随重定向（§5.7）、响应头 32 KiB 上限、不声明压缩、正文在 HTTP 层限读，上限是参数（agent 为 `agentwire.MaxResponseBytes`，更新器为三份文件各自上限之和加编码余量）；其余传输设置沿用 agent 现状（克隆 `http.DefaultTransport`，代理取自进程环境，更新器的 systemd 单元不设代理变量）。取回的期限只归更新器：每个任务 5 分钟，经 ctx 交给来源，GitHub 与 hub 两种来源同一个期限；两种来源的连接都不另设总时限（GitHub 来源只限建连、握手与等响应头，hub 来源只限建连与握手——缓存未命中时 hub 要先从 GitHub 取完、验过才应答），没有期限的取回在发请求前即被拒绝。连接上的总时限覆盖读完正文，而 GitHub 来源一次取回是三个顺序请求，它只要比期限短，归档就只能用到它，大归档在慢链路上必然先撞它。
- `AgentService.GetRelease`：节点 token 鉴权，在挂载点拦截器里登记（§3.2）。请求里没有版本：hub 取该节点当前的更新任务，要求 ID 与 `task_id` 相同、状态为 `dispatched` 或 `downloading`、未过期，版本即该任务的版本；`arch` 只接受 agent 产物矩阵，与更新器拼资产名用同一张表。泄漏的节点 token 由此只能取到该节点正在执行的那一个版本，hub 不成为通用下载点。同一节点至多一个在途请求；每个任务 ID 至多 3 次（内存计数，hub 重启清零），更新器每个任务只发一次，这个上限把持 token 者能消耗的 hub 出口带宽绑定到管理员创建的任务数上；两者超出都返回 `ResourceExhausted`。不标无副作用（§3.3），只接受 POST。token 校验与 Report 同一个函数，但不持有 Report 用来与节点删除互斥的锁：一次下载可能持续数分钟，持读锁会让删除等待，而排队的写锁又会挡住此后全部节点的 Report；GetRelease 不写 Report 维护的内存状态，节点删除后它的任务随之消失，任务检查即拒绝。
- hub 侧取回与缓存：某个（版本，资产）第一次被请求时，hub 经 §5.4 的 GitHub 受限传输从固定官方下载地址取回三份文件，同键的并发请求合并为一次取回；取回后用同一个验签函数验过才入缓存。这一步让坏产物在 hub 处就报错，不必每个节点各下一遍才失败；它不是防线——失守的 hub 可以跳过它，防线是节点上的更新器。缓存在内存里，按引用释放：后台周期检查，某版本已没有未结束的节点任务引用即删除；不落盘，没有崩溃残留要清理。缓存总字节上限 256 MiB（单个归档上限 128 MiB 的两倍），超出返回 `ResourceExhausted`，不淘汰在用条目。取回或验签失败不进缓存，错误原样回给更新器，进入任务的 `error`。
- 状态：`UpdateStatus.source`（`github` 或 `hub`）由更新器报出、agent 原样转发，面板更新页显示。节点更新失败时先要知道它走的哪条路径。
- 旧更新器（不认识 `SHA256SUMS.sig` 的版本）照旧经 GitHub 取 `SHA256SUMS` 与归档：release 只新增文件，既有资产的名字与内容规则不变，所以不需要兼容代码。新更新器要靠重跑安装器换上（§5.7），在此之前节点沿用旧行为；只能连到 hub 的节点在旧更新器下无法在线更新，换上新更新器的那一次安装仍要借安装时可用的出网手段。

## 5. 鉴权与信任边界

### 5.1 节点 token

- 运行 token 是 32 字节随机数的十六进制，只经 `Authorization: Bearer` 上报，不进 URL、不进请求体，不授予注册或轮换权限。
- 管理端创建与换发返回独立用途的安装凭据：`heron_install_` 加 32 字节随机数的十六进制，完整前缀参与 SHA-256。只能放入 Register.key，不能用于 Report；增删前缀会改变哈希，不能把运行授权伪造成安装授权。库与内存映射仍只保存完整凭据哈希，无需结构迁移；旧无前缀 token 仅保留运行权限，旧版待安装凭据须管理员重新换发。
- hub 只存 SHA-256。token 是高熵随机数，不需要抗字典攻击的慢哈希，而慢哈希撑不住每秒数百次校验。
- 内存中维护 `hash → node_id` 映射，`Report` 的鉴权只查它、不读库。修改顺序固定为：持 `auth` 的锁 → 写库并等待成功 → 改映射 → 放锁。写库失败则映射不动；进程在两步之间崩溃则映射在下次启动时自库重建。任何绕开这把锁直接改表或改映射的写入都会让两者分叉。
- 安装凭据明文只在管理端创建与换发时返回，运行 token 明文由 Register 返回。换发或认领成功返回前，库与内存映射中的旧哈希均已替换。
- 按 `node_id` 键控的其余内存状态（live 的桶、限速桶）随节点删除一起清理：进程内删除（`AdminService.DeleteNode`）在映射更新的同一步清理；`heron-hub node delete` 在另一进程改表，这些状态在 hub 重启重建映射时随之消失。
- 每节点令牌桶限速：上报速率超过下发间隔所对应速率的 2 倍即返回 `ResourceExhausted`。
- `AgentService` 请求体上限 256 KiB（`maxBody`），由探测结果排空推导（§4.4），编译期断言钉住。

同一 token 被两台机器同时使用（克隆虚拟机）会表现为 `boot_id` 交替出现，使流量基线反复重置。hub 在窗口内统计 `boot_id` 切换次数，超过阈值即在管理面板对该节点标记警告。

### 5.2 注册窗口

`Register` 的 `key` 也可以是管理端创建或换发的一次性安装凭据：hub 限定安装用途并核对完整哈希后认领既有节点，把哈希替换为新运行 token 的哈希并返回明文。安装凭据随即失效，不新建节点、不改名称、不消耗窗口名额；运行 token 永远不能认领。认领与管理员换发共用 auth.mutMu，库成功提交后才更新映射。

已安装主机更换凭据时，安装脚本必须显式传 `--re-register --hub URL --key KEY`；这条路径用现有 agent register 保留本地探测策略、替换身份后再重启。普通升级不重新注册，已有配置时仍忽略 key，不把普通重跑变成身份替换。

管理员在面板开启注册窗口：生成一次性 key，带截止时间与可注册节点数上限。窗口关闭与 key 错误返回同一响应。失败计数按来源键（§5.3）独立于登录失败计数：批量安装时用了过期 key 是配置失误而不是对面板的攻击，共用计数会把运维者自己锁在登录页外。只有窗口开启且 key 错误才计数；窗口关闭时没有可猜的秘密，计数只会误伤与他人共用出口地址的运维者。

`Register` 是 `AgentService` 唯一的匿名方法，因此按来源键令牌桶限速（桶容量 30、每秒补充 1，超限返回 `ResourceExhausted`），与 `PublicService` 的限流同一原则。限速在 connect 解码之前（解码失败的请求也计数；挂载点上按路径的 HTTP 中间件，窗口失败计数用它算出的同一个来源地址）生效：窗口关闭时匿名请求也到不了写协程，否则任何人都能用几十字节的请求体让分钟刷出与 facts 落盘排在自己的事务之后。批量安装脚本遇到 `ResourceExhausted` 按退避重试即可。

### 5.3 管理员

- 单管理员。密码用 argon2id 存储，通过 `heron-hub passwd` 在 hub 主机上交互设置；没有经网络的首次设置页，也就没有"谁先访问谁占有"的窗口。
- 管理员表为空时登录一律失败。空表的语义是"无人可登录"而不是"无需认证"，由登录路径上的显式检查承载。
- 会话 token 为 32 字节随机数，库中只存 SHA-256，带绝对过期与空闲过期。cookie：`HttpOnly`、`SameSite=Strict`、不设 `Domain`（host-only，§10.1 的隔离依赖它；同名 cookie 有多个值时逐个校验、任一有效即通过，见 §10.1），`Secure` 由可信代理转发的协议决定。修改密码即清空全部会话；API token 不随之吊销（§5.6）。
- 会话可列可撤：`ListSessions` 返回当前有效的会话（创建时刻、最近使用时刻、是否本次请求所用的会话），`RevokeSession` 按 `admin_session.token_hash` 撤销一个；两者都是 `ACCESS_SESSION`——API token 不能列也不能撤会话，与 token 不能管理 token 同一原则（§5.6）。以 hash 作标识：知道 hash 不能冒用会话，校验需要 cookie 里的明文。撤销当前会话等于登出（响应清 cookie）；撤销不存在或已过期的 hash 不报错，重复撤销幂等。换设备或怀疑某处忘了登出时按会话撤销，不必改密码清空全部。
- 密码校验同一时刻只跑一个：门在锁定判定之后、慢哈希之前，容量固定为 1，并发到达的其余尝试立即以 `ResourceExhausted` 拒绝、文案让其稍后重试，且不计入失败次数（它不是一次猜测，计入会让攻击者用并发把运维者锁在外面）；会话 cookie 校验是哈希查表，不经过门。排队只是把同一波洪水推迟、让合法登录排在队尾，拒绝才让它落空；容量按核数推导会在小机器上重新打开口子。
- 登录通知：`Auth.Login` 签发会话成功、以及登录失败达到锁定阈值时，经通知渠道各推一条（时间、来源地址、凭据种类；时间写进摘要正文本身、RFC 3339 并带 hub 时区偏移——投递会重试、重启后续投，接收方看到的送达时刻不是登录时刻；来源地址是 `auth.ClientIP` 解析后的值，反代未配 `--trusted-proxies` 时会是代理地址，文案不遮掩这一点）。投递到哪些渠道由一个全局设置（键 `notify.login_channels`）选择，不走规则×节点；没有选渠道即不发。在 `Settings` 里它是一个带 presence 的 message 字段（`optional`，内含 `repeated int64 channel_ids`），`UpdateSettings` 缺席即不变、显式给出空集合即关闭（与 §10 总闸同一口径）：proto3 的裸 repeated 字段缺席与空列表在线上不可区分，直接用它会让只改外观的老客户端把已选的渠道清空；保存时每个 id 必须存在（否则 `InvalidArgument` 点名），列表按请求里的原始条数（含重复）至多 16 个，超出 `InvalidArgument` 点名字段与上限——没有上限就算不出解码预算的最坏请求；渠道被删除时从这个列表里摘除（与 `alert_rule_channel` 删渠道同一事务）。API token 的使用不通知——它是自动化，会刷屏。事件进 `alert_event` 并带投递记录，`rule_id` 与 `node_id` 为 0，`ListAlertEvents` 能看到；现有查询、索引与 `Forget` 清理要按 0 值核对。单管理员面板没有第二双眼睛看审计日志，登录通知是密码泄漏当下唯一的信号。
- 登录失败按来源键锁定。来源键由 `auth.SourceKey` 统一归一化（IPv4 按单个地址，IPv4 映射地址先还原；IPv6 按 /64——一台主机通常拥有整个 /64，逐地址计等于不计），登录锁定、注册窗口失败计数与两处限流（§5.2、§10）共用同一个键。
- 跨站请求伪造由以下几条各自独立的事实约束，不指定其中哪一条是"主要防线"：会话 cookie 为 `SameSite=Strict`；hub 不下发任何 CORS 允许头；`AdminService` 不把任何方法标为无副作用（因而不接受 GET）；Connect 处理器对 `application/json` 与 `application/proto` 之外的 `Content-Type` 拒绝服务，而浏览器的跨站"简单请求"发不出这两种类型。最后一条是对 connect-go 行为的断言，列入 §13 并由 §12 的测试钉住。

管理员第二因素状态位于配置层 `admin_security`：TOTP 密钥与已消费时间步、恢复码摘要、WebAuthn 凭据、持久化 RP ID/Origin 绑定和版本号。密码登录启用 TOTP 后必须同时验证 OTP 或一次性恢复码；Passkey 独立支持无密码登录，要求可发现凭据与用户验证。首次注册使用当前可信 HTTPS 访问来源生成挑战，成功消费挑战和保存凭据时原子提交域名绑定；后续请求不能根据 Host 改写 RP 配置。浏览器检测安全上下文和 WebAuthn，服务端仅采信直连 TLS 或可信代理的 HTTPS 信息。换域名需要密码及现有第二因素重新认证并显式改绑，同时撤销旧 Passkey 与会话。旧 `--admin-origin` 仅用于已有凭据且未持久绑定时的一次性可信配置导入；缺原配置保留凭据但禁用其使用，不猜测 RP ID。持久绑定存在后数据库是唯一来源，旧参数不覆盖绑定。认证消费、凭据版本 CAS 和会话签发同事务，修改认证方式同事务撤销旧会话。挑战及重新认证证明绑定会话、版本、用途、来源和有效期且只能消费一次。普通失败、成功、锁定及认证变更都写审计；普通失败不投递。设备丢失时本机 `security-reset --yes` 清除因素与绑定并撤销会话，不清密码和 API token。

### 5.4 TLS 与可信代理

hub 只监听明文 HTTP，TLS 由反代（Caddy / nginx / CDN）终止，hub 内没有证书代码。

- `--listen` 默认 `127.0.0.1:8080`。监听非 loopback 地址时启动日志告警：此时任何人都能绕过反代直连并自带转发头。
- `--timezone` 是 IANA 时区名，默认取 hub 进程的本地时区；只用于 §7 流量周期的重置日判定、§9.4 到期日的天边界、面板文案与通知文案里的时刻（§5.3 登录通知的摘要按它写 RFC 3339 时间），不参与任何时长计算。本地时区的名字按 `TZ`、再按 `/etc/localtime` 符号链接的目标路径里 `zoneinfo/` 之后的部分解析（在所测的 Alpine 3.21、Debian 12、Ubuntu 24.04、Rocky Linux 9 上按各自的标准方式设置时区后都是符号链接，Alpine 指向 `/etc/zoneinfo/`）；不读 `/etc/timezone`——RHEL 系没有它，Debian 与 Ubuntu 用 `timedatectl` 改时区后它仍是旧值。`/etc/localtime` 是复制出来的普通文件时（常见于 Dockerfile）取不到名字，退回 UTC 并告警。
- `--trusted-proxies` 显式给出 CIDR 列表。只有 TCP 对端地址落在列表内的请求，其 `X-Forwarded-For` / `X-Forwarded-Proto` 才被采信。`X-Forwarded-For` 可能有多行（HAProxy 的 `option forwardfor` 把真实地址另起一行追加），读取时把全部字段行按出现顺序合并后再取值，只读第一行会让键取自客户端伪造的那一行。`X-Forwarded-Proto` 同样按全部字段行合并后取第一个值，即最外层那一跳写的协议；它不带逐跳地址，没法像 `X-Forwarded-For` 那样从右向左跳过可信代理，代理追加而不覆盖时客户端自带的值排在最前。这一点有意不处理：它只决定签发或清除请求者自己的会话 cookie（Login、Logout、撤销当前会话）时带不带 `Secure`，客户端只能改到自己，影响不到别的来源。空列表 = 不信任任何转发头、一律用 TCP 对端地址，是收紧方向。hub 不从请求头推断自己是否在反代之后。
- hub 按配置主动访问通知渠道（§9.3）、国家查询（§4.9，默认开启，HTTP 后端发往 `geo.url`）、分层备份（§6.7）与心跳外推目标（§9.6）；管理员操作还可触发 GitHub 主题查询/安装，以及固定官方仓库的正式版本查询；hub 来源的节点调 `GetRelease` 时（§4.10），hub 从固定官方下载地址取回该任务版本的产物，与主题、版本查询走同一个 GitHub 受限传输。hub 自身与 GitHub 来源节点的程序下载由各自的本机更新器完成。
- 主题不需要独立主机名或启动参数。旧 `--theme-origin` 已移除，升级前必须从启动配置中删除；后台直接选择同域名公开页的主题版本。
- hub 不生成自己的对外地址：面板里安装命令的 hub 地址取浏览器当前的 origin（§10），所以没有 `--site-url`，也不存在从 `Host` 头推断对外地址的问题。

**管理响应的压缩边界**：经 `AdminService` 挂载点的响应除 `GetSnapshot` 外都不压缩，全部带 `Cache-Control: no-store, no-transform`；成功、错误、来源检查直接返回的 403 与 `UploadTheme` 都在此范围。唯一挂载点 `Service.Handler()` 统一写缓存头，按路径分给三个 Connect 处理器：`UploadTheme` 用大解码预算，`GetSnapshot` 按 connect 默认协商压缩，其余共用 `WithCompressMinBytes(math.MaxInt)`；三者共用鉴权，不删除 gzip 解码能力，已有压缩请求仍可接受。停机时，全局 `drainingHandler` 在进入此挂载点之前直接返回 503，不带这项缓存头；其正文固定为 `server shutting down`，不含私有数据或 agent 可控内容，因此不构成上述混合内容的压缩旁路。connect-go v1.21.0 的真实 HTTP 测试覆盖 Connect、gRPC、gRPC-Web：检查 HTTP 内容编码，以及后两者消息帧和 gRPC-Web trailer 帧的压缩位，而不只看客户端解码后的对象。

一台失守节点可以任意改写自己上报的 `Facts` 字符串（hostname、os、kernel、cpu_model 等）；`ListNodes` 又把它们与其他节点的私有备注、来源地址及国家查询记录放进同一个响应。若攻击者还能观测管理员 HTTPS 流量长度，就能反复改变自报字符串，以压缩长度随字符串与秘密重合而变化的现象探测私有内容；TLS 加密正文不等于隐藏长度。因此管理服务默认禁用响应压缩，并要求反代/CDN 不重新压缩。唯一的例外是 `GetSnapshot`：总览每 2 秒轮询它，不压缩的代价最大；它的字符串字段逐个审过——节点名（含非公开节点，属于要保护的内容）、hub 构建时注入的两个版本号、agent 写入的 `boot_id` 与 `net_counter_epoch`；其余是数值，以及 hub 判定的 `online` 与管理员设定的 `quota_mode`。`boot_id` 的准入只收空串或 UUID（§4.2 `Metrics`），`net_counter_epoch` 只收小写十六进制摘要，所以 agent 能放进这条响应的只有十六进制字符、连字符与数值写法里的字符：节点名里由这些字符组成的片段仍可被试探，其余字符不能。`TestSnapshotStringFieldsAreAudited` 枚举响应可达的全部字符串字段并与审过的清单比对，新增字段不过这条判断就不能进快照。`no-store` 禁止存储响应，`no-transform` 要求中间层不改写；部署仍须按管理路径显式禁用压缩并回读核对最终响应。`PublicService` 的响应仅投影已获准公开的数据，不把私有字段与节点自报内容混在一起，继续协商压缩，保留各方法的 GET `max-age` 和按压缩方式分键的快照缓存。

代价是管理端响应正文增大（`GetSnapshot` 自压缩起不再承担）。固定合成样本经真实管理 HTTP 入口测量：150 个节点，其中 46 个带完整 Metrics 与 Facts，其余未上报；节点有不同名称和私有备注，已上报节点包含网络检测、采集诊断与执行范围。Go 1.27.1 / connect-go v1.21.0 下，对同一份 JSON 用标准库 gzip 默认级别压缩：

| 管理方法 | identity 正文（字节） | gzip 正文（字节） | 禁压缩增加（字节/次） |
|---|---:|---:|---:|
| `GetSnapshot` | 48,970 | 4,250 | 0（压缩） |
| `ListNodes` | 85,217 | 4,058 | 81,159 |

这里只复用了生产的节点数量与上报构成，不是生产内容实测，也不是最坏上限；字符串长度、指标数值与重复度会改变体积。数字不含 HTTP 头和 TLS 开销；公开页的轮询不承担这项增量。

### 5.5 为什么不做 mTLS

mTLS 相对 bearer token 的增量是"凭据不过线"与"在 HTTP 层之前拒绝未授权连接"。代价：它要求 hub 自己终止 TLS，与 §5.4 冲突——若由反代验证客户端证书再以请求头转发身份，hub 又回到信任请求头；还需要 CA、签发、轮换、吊销整套生命周期，而注册阶段仍需一个一次性秘密换取证书。

在本项目的威胁模型下，节点 token 泄漏的后果是有人能伪造该节点的指标；hub 失守的后果受 §5.7 的 agent 侧边界约束。两者都不足以支撑上述代价。agent 强制校验服务端证书，不提供跳过校验的开关；§5.7 的 `insecure_http` 放行的是明文 http，不是跳过 https 的证书校验。

### 5.6 API token

与会话平行的第二条凭据口径，给 agent 与脚本读数据用：自动化进程不必持有管理员密码，出事时的吊销范围从"全部会话加改密码"缩到一个 token。

- 明文为固定前缀 `heron_at_` 加 32 字节随机数的 hex，库中只存整串的 SHA-256，明文只在 `CreateApiToken` 的响应里出现一次。前缀让泄漏到日志、配置或代码仓库里的 token 能被审查与 secret scanning 认出。
- 两条路径互不回退：`Authorization` 头的 scheme 为 `Bearer` 即走 bearer 路径，cookie 一律不看；否则走 §5.3 的会话路径。任一路径的失败都不转交另一条——有回退就等于实际生效的是两套鉴权里较弱的那条，且弱在哪条随请求头变化，事后无法从代码读出。scheme 为 `Bearer` 而 token 为空、格式不对或不存在，都返回 `Unauthenticated`。其他 scheme 不是 hub 的凭据，按不存在处理：反代做 Basic 认证时，浏览器会对每个请求自动附带 `Authorization: Basic`，nginx 与 Caddy 默认原样转给 hub，若见头即走 bearer 路径，面板的每个请求都会被拒。
- token 只能调 `ACCESS_READ` 方法（§3.2），其余返回 `PermissionDenied`，错误信息写明方法名：`ACCESS_SESSION` 方法说明 API token 只读、需要面板会话，`Login` 说明 token 不能用来登录、应只带密码。写操作对 token 开放要逐个显式决定，目前一个都不开；token 的建、列、删都是 `ACCESS_SESSION`，token 不能签发 token。
- §5.3 的四条 CSRF 事实属于会话路径，一条都不因 bearer 路径而放松。bearer 路径不需要它们：浏览器会自动附带的 HTTP 认证只有 Basic、Digest 这类缓存凭据，`Bearer` 只能由脚本显式设置，而跨源请求带 `Authorization` 头必须先过 CORS 预检，hub 不下发允许头。会话路径仍然需要。
- 每次校验都查库，不缓存：吊销（删行）在下一个请求即生效，hub 运行中由 `heron-hub` 直接改库也一样。管理请求的频率远低于上报，查库的代价可以接受；引入缓存必须同时给出吊销的传播路径。
- 最后使用时间只供展示，与会话同一口径：从未使用或距已落库值满一分钟才异步刷新，不让每次读请求都排进写协程；刷新只 UPDATE 已存在的行，吊销之后才落库的刷新不会把 token 写回来。它不参与任何裁决。
- 名称 1–64 字符，不要求唯一，身份是 id。token 总数上限 100，超出返回 `ResourceExhausted`。不设过期时间，靠面板上的创建时间、最后使用时间与手动吊销管理。
- 改密码不连带吊销 token：连带吊销会让每次轮换密码都静默打断自动化。代价是密码泄漏期间被创建的 token 在改密码后仍然有效，所以 `heron-hub passwd` 改完后列出现存 token（名称、创建时间、最后使用时间）并询问是否全部吊销，默认不吊销。`heron-hub token list`、`heron-hub token revoke --id N`、`heron-hub token revoke --all` 供面板不可用或密码已泄漏时应急。离线子命令直接改库：只有建立状态的（`passwd`、`window open`、`node create`）在 `--db` 指向的文件不存在时建库，供第一次 `serve` 之前准备；其余读或改已有状态的子命令（`token`、`stats`、`node list|delete|rotate-token`、`window close|show`）在库不存在时报错——否则写错 `--db` 会静默建一个空库，`token revoke --all` 报告吊销了 0 个，而真正的库原封不动。
- 自描述：`GetApiReference`（`ACCESS_READ`）返回技能文件（`proto/SKILL.md`）与全部 proto 源文件，均在构建时嵌入——不在仓库里的 agent 由此取得与 hub 同版本的 schema，注释即文档。不用 gRPC reflection：它是双向流，不能以纯 HTTP+JSON POST 调用，与 §4.1 的 unary 约束冲突；也不另开端点，对外仍是三个 Connect 服务。
- 技能文件约定 `HERON_HUB` 与 `HERON_TOKEN` 两个环境变量，写明进门方式、schema 的取法、JSON 约定（int64 编码为字符串、时间为 Unix 秒、`_ms` 后缀为毫秒、缺读数与读数为 0 的区别、Connect 错误体）与可直接运行的例子；例子由 e2e 执行（§12）。

### 5.7 hub 失守时 agent 宿主机的边界

威胁模型：攻击者完全控制 hub（进程、数据库、面板页面），或处在 agent 与 hub 之间的明文链路上。要守住的性质：攻击者不能在 agent 宿主机上执行任意代码、不能任意读写宿主机文件、不能让 agent 耗尽宿主机资源（内存与日志）、不能让 agent 探测宿主机本机（回环与本机接口上的地址）与链路本地地址。攻击者仍能做的：伪造展示、停掉监控、请求安装比当前版本新的官方正式 Release（包括曾签名发布、后来从 GitHub 撤回的版本：更新器只认签名，不知道撤回；需要时可在更新器里加最低版本表，目前不做），以及按 §8.4 的速率上限探测本地策略允许的地址（私网默认允许）。官方发行权限属于更新信任根：发行签名私钥是 GitHub Actions secret，持有发版权限就能触发签名（§4.10）。下面各条分别承载其中一部分，互不替代，不指定哪一条是主要防线：

- 下行的钉指纹：`ProbeTask.cert_spki_sha256` 让 hub 指定 agent 只认某一把叶证书公钥（仍受握手签名约束，见 §8.2），`config_id` 让 hub 把这份配置标成一个身份，agent 原样回显。这与 hub 本来就能改探测目标同级：失守的 hub 可以让 agent 去探测它指定的、本地策略允许的地址，并只接受它指定的公钥。它不增加在宿主机上执行代码或读文件的能力。
- 下行面：agent 从 hub 收到的只有 `RegisterResponse` 与 `ReportResponse`；后者包含上报间隔、探测任务、facts 请求和受限更新任务。agent 不监听入站端口、不执行外部命令、运行期不写配置。更新任务只将版本号、任务 ID 与到期时间提交给对应服务用户可访问的本机 Unix socket；独立 root 更新器验证正式版本递增、发行签名（签名绑定任务版本）、摘要、归档及固定 systemd 目标后替换主程序，不接受 URL、仓库、路径、摘要或命令。下载来源由本机安装参数决定（§4.10）：hub 来源时更新器向 hub 发 `GetRelease`，失守的 hub 给出的字节同样要过验签，它能做的只是不给、慢给或给错，让任务失败；该请求的响应头与正文有上限、不跟随重定向，与 agent 用同一个客户端构造。新增下行字段必须在本节写明授予 hub 的能力。
- 在线更新事务：管理员会话可创建和取消任务，只读 API token 只能读取。每节点最新任务与能力持久化，后台先将 dispatched 写入数据库，Report 才下发；Report 本身不等待数据库或本机 socket。排队任务最多 24 小时，仅 queued 可取消。最新任务只描述"这台机器的更新进行到哪了"：上报的运行版本已达到（或超过）任务目标，而该任务既不是成功记录、也不在本机更新器的执行中（失败、回滚、过期、取消、结果未确认，或本机没有记录的进行中任务）时，hub 清除这条任务——经重装或手动升级到达目标的节点不再挂着旧错误，也不冒称任务成功；hub 自身的更新状态在投影时按同一判定（`updates.Superseded`）隐藏本机更新器的这类记录。更新器独立保存任务 ID 防重放记录、备份和事务阶段，崩溃后按持久化阶段恢复。Hub 候选绑定监听但在确认前不开始 Serve，避免回滚覆盖已接受业务数据；Agent 需新进程成功上报才确认。就绪还需匹配 systemd MainPID、Unix 对端 PID 和实际 exe 摘要。更新器本身及服务定义只能由 root 安装器升级，安装器通过 root-only 维护握手与更新事务互斥。节点任务不进入分层快照，恢复时清空，避免重放旧升级命令。
- 上报间隔：agent 把 `report_interval_ms` 限定在 hub 能合法配置的范围内，即 TTL 取 `MinTTL` 与 `MaxTTL` 时的间隔（间隔 = TTL / `ReportsPerTTL`）。越界（含 0）取最近的边界并告警，值变化时告警一次。TTL 边界、`ReportsPerTTL` 与间隔的换算只在 `internal/agentwire` 定义一次，hub 的 TTL 准入、hub 的间隔下发、agent 的限定与 agent 的退避上限（§4.7）都读它。没有这一条，hub 下发 1 ms 就能让 agent 不停地采集与上报。
- 响应体：agent 在 HTTP 层限读响应正文 `agentwire.MaxResponseBytes`（64 KiB），成功与错误响应都经过这一层；超出即报错，不截断（截断的正文可能恰好解码成一条更短的合法消息），按普通失败退避。agent 不接受压缩：connect 不声明 gzip，HTTP Transport 也不自行声明与解压，hub 不顾声明回 gzip 时按不认识的编码报错；读到的字节因此就是解码前的全部大小，响应本来不超过上限，压缩没有收益。connect-go（v1.21.0 实测）的 `ReadMaxBytes` 只管成功响应的消息：256 MiB 的错误正文让客户端分配了 1282 MiB，47 KiB 的 gzip 错误正文分配了 128 MiB，所以限读不能交给它。hub 侧有测试钉住满载 `ReportResponse` 的编码不超过上限（含受限更新任务）。响应头另设 32 KiB 上限（Go 默认 10 MiB）。
- 重定向：agent 与更新器连 hub 的 HTTP 客户端不跟随任何重定向，`Register`、`Report` 与 `GetRelease` 都没有需要重定向的场景。Go 的 http.Client 跟随同主机重定向时会保留 `Authorization`，而协议不参与判断，https 到同主机 http 的重定向会把节点 token 明文发出。
- 传输：hub 地址必须是 https。http 只在两种情形下接受：主机是 loopback IP 字面量（`127.0.0.0/8`、`::1`；`localhost` 这类名字要经解析，不在豁免内），或配置里有 `insecure_http: true`（由 `register --insecure-http` 或 `configure --insecure-http=true` 写入）。`register` 在发请求之前、`run` 在加载配置时、hub 来源的更新器在发 `GetRelease` 之前（§4.10）调用同一个校验函数。不满足时 `run` 拒绝启动，报错里给出两种放行方式；更新器则让该任务失败。已有的非 loopback http 部署升级后会停在这一步，安装脚本的启动确认随之失败并指向日志；这是有意的，明文链路上的中间人与 hub 失守等价，不能静默延续。
- 探测目标：agent 解析出地址之后、发包之前，按宿主机本地策略检查实际要连的地址（§8.4）。策略只来自本地配置，hub 改不了。
- 日志：不少日志行由 hub 的应答触发（上报失败、被拒的任务、越界的间隔），错误文本里带着 hub 给的字符串；OpenRC 与 launchd 把 stderr 写进不轮转的普通文件。边界放在出口（`internal/agent/agentlog`），每一行都经过它：令牌桶每个令牌恰好一行，突发 20 行、此后每 30 秒一行，被压掉的行数以 `suppressed_before` 带在下一次放行的那一行上；消息与每个字符串值（error 等先格式化）连同截断标记至多 256 字节。标准库 `log` 经 `slog.SetDefault` 接到同一个出口，在创建任何网络客户端之前完成：`net/http` 在 idle 连接收到多余字节时用它写出对端给的内容，不接过来就绕开了边界。`register` 与 `configure` 是一次性命令，最终错误行至多 4 KiB（`register` 的错误可能带着 hub 的文本）。调度器每次应用清单对被拒任务只出一行汇总（每个被拒任务仍留一条 error 结果，结果队列有容量上限）；没有这两条，一个塞满空任务的 64 KiB 响应能刷出 4 MiB 日志。这给的是速率上界，日志文件的保留仍归宿主机的日志管理：systemd 下是 journald 的保留策略，OpenRC 与 launchd 的日志文件产品不轮转。
- 资源上限：systemd 单元设 `MemoryMax=128M`。2026-09-29 在 Debian 12.15 arm64 / systemd 252.39-1~deb12u2（OrbStack LXC，cgroup v2）连接一个 hub、64 个 ICMP 任务以 5 秒间隔探测回环，全部有结果后持续 3 分钟，服务 cgroup 峰值 21389312 字节（约 20.40 MiB）；取六倍后向上取整为 128 MiB，给 Go GC、并发探测和平台差异留余量，不视作所有负载的最坏峰值。生效要求内核提供 memory 控制器。OpenRC 不设内存上限：`rc_cgroup_settings` 依赖宿主机向服务 cgroup 下放 memory 控制器；2026-09-29 在 Alpine 3.21 / OpenRC 0.55.1（OrbStack LXC，cgroup2，根 `subtree_control` 为空）实测静默不生效，安装脚本不修改宿主机的全局 cgroup 配置。launchd 同样没有会被强制执行的内存上限；OpenRC 与 launchd 的内存防护只靠 agent 的响应体上限，不额外承诺服务级强制上限。systemd 的这一层是纵深防御，agent 内存有界仍由响应体上限与任务数上限承载。
- 安装链路：面板由 hub 提供，失守的 hub 可以把面板上的安装命令整条换掉，这一点产品内防不住。安装命令的可信来源是 README 与 GitHub Release，面板上的命令只是为了方便。能防住的是一种更隐蔽的变体：命令仍取 github.com 上的脚本，只在后面追加一个指向恶意镜像的 `--base-url`。为此每个安装脚本（agent 的两个与 hub 的一个）内嵌它所属版本全部 tar 包的 SHA-256（由发布目标写入，§14；只发 hub 的 release 里的两个 agent 脚本原样取自绑定的 agent 版本，属于那个版本，§14.1），只按内嵌值校验，不拿下载来的 `SHA256SUMS` 作校验依据。`--base-url` 只改变从哪里取字节，不改变接受哪些字节。脚本只安装自己所属的版本，没有 `--version`，要装哪个版本就取哪个版本的脚本；没有写入哈希的源码脚本拒绝安装（卸载不下载，照常可用）。

## 6. 存储

### 6.1 读写模型

所有写由单一写协程串行执行，读走独立的只读连接池。SQLite 同一时刻只允许一个写者，应用内串行化从根上避免写者之间的 `SQLITE_BUSY`。

`Report` 的处理路径只碰内存、不等待数据库：鉴权查 `auth` 的内存映射，任务列表取自 `probe` 的内存副本，写入落在 `live`、流量累加器与探测桶；`Facts` 的保存投递给写协程后即返回。落盘由定时器驱动：分钟边界刷指标与探测行，每 10 秒刷流量，退出时全刷。管理端的写操作（建节点、轮换 token、改任务）则等待写协程的结果后才应答。

### 6.2 指标表

三级指标列相同：`metric_1m`、`metric_5m`、`metric_1h`；覆盖事实列按级别区分。

`metric_1m` 另存 reported（已准入上报，0/1）与 observed（hub 观测，0/1；旧行 NULL）。指标全无读数的有效上报仍是 reported=1；纯观测行 reported=0、所有指标 n=0，不带 LastSeen，不更新 last_seen_at/last_source，不令资源告警恢复。两事实各自按三值或合并：任一为 1 则为 1，否则任一未知则未知。交集从合并后的事实按三值与计算，不单独合并交集。5m/1h 存 minutes、observed、both 三个计数，各列任一源行未知则该列整体未知。旧 1m 的 reported 回填 1；旧 coarse 行三列未知，minutes 从不改成观测分钟数。

指标层 `node_coverage(node_id PRIMARY KEY, start_ts)` 保存覆盖起点：接收首报的分钟。live 启动加载它，新节点首报即记入内存，批构造时随行显式携带，pending 重试保持不变。任一指标行获准入时在同一事务 INSERT ON CONFLICT DO NOTHING；observed=1 或 reported=0 的行必须有不晚于行 ts 的起点。回拨到起点之前的真实上报照原水位/节点准入写入，只不补观测。迁移回填各级最早留存指标 ts，无指标时用 last_seen_at 所在分钟，都没有则无记录；这是最早留存证据，不保证精确首报，只影响旧区间未知分钟数量。

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
- `disk_read_bps` / `disk_write_bps` / `cpu_steal_pct` / `cpu_iowait_pct` 同为 `MeanMax`：磁盘速率记整盘设备的采样速率（bytes/s），两个 CPU 占比记 `/proc/stat` 差分占比（%）。schema 28 给三个指标层各追加这四个指标的 sum、n、max（共 12 列，`NOT NULL DEFAULT 0`），旧行 n=0 即空洞，不把缺失伪装成已测的零速率或零占用；配置层无变化，指标层快照恢复同步迁移。
- `load1_per_core` 是 `Mean`：agent 在同一次采样里算好的按核负载，无单位。schema 34 把它追加在描述表末尾，三个指标层各有 sum 与 n。覆盖列已经排在指标列之后，所以迁移按冻结 DDL 重建表，而不是 `ADD COLUMN` 加到表尾。旧行 n=0，升级前的窗口这一列是空洞，不是 0。

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

`probe_5m`、`probe_1h` 同构。`rtt_min_us`/`rtt_max_us` 可空：桶内没有任何 rtt 样本（全部丢包或错误）时为 NULL，`min()`/`max()` 聚合自动忽略；若落成 0，上卷会把"没有样本"当成 0 µs。主键里 `ts` 在 `task_id` 之前：单节点查询（"某节点、某时间窗、全部任务"）由主键直接定位，`task_id` 在前会让 SQLite 只能定位到节点，然后扫描该节点的全部历史。

迁移 33 给三张表各加 `(task_id, node_id, ts)` 索引（`<表>_by_task`）：跨节点对比查询按"某任务、一组节点、某时间窗"读取，等值键在前、`ts` 范围殿后；对比源查询用 `INDEXED BY` 钉住该索引——store 从不跑 `ANALYZE`，无统计时规划器可能选中主键按节点扫窗口内全部任务的行，读量与对比分块无关，额度计数也会与聚合走不同的计划。索引列序为 `(task_id, node_id, ts)`：`node_id` 在 `ts` 之前使 IN 清单里的每个节点都成为等值探测；`(task_id, ts, node_id)` 会让 `node_id` 落到范围之后、逐行过滤。纯索引构建不搬数据：验收机上 5627 万探测行约 51 秒（modernc 纯 Go 引擎，行速约 1.1M 行/秒；同一机器 C 版 SQLite 约 15 秒，SQLite 单写者也排除了并行建索引的收益），迁移在写路径上一次性占住写协程。

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

WAL 文件观测：`GetStorageStats.wal` 与 `heron-hub stats` 均取 `store.StorageStats` 的一次观测，路径是库旁的 `<Store.path>-wal`，生产使用 `os.Stat`，不读取 `-shm`。文件存在时返回实际字节数（允许零）；ENOENT 用显式 absent 标记“无 WAL 文件”；其他错误为未知并带至多 512 字节的有效 UTF-8 错误文本，不以零代替。API 子消息缺失表示旧 hub 没有该项，与三态分开。观测时刻为 stat 完成时的 hub 墙钟 Unix 秒；SQL 行数、逻辑大小与维护健康来自 Store 独立只读连接上的同一事务；并发请求共用单份计算，成功完成后 60 秒内复用，文件仍逐请求观测，二者不是同一快照。`sql_observed_at` 为该事务开始时的 Store 墙钟 Unix 秒，新 hub 总是给出，缺席表示旧 hub；复用期间该值不变，CLI 在表行数之后输出 `sql.observed_at`（Unix 秒），同样不能把它当作本次请求时刻。CLI 的 `wal.observed_at`、`wal.state`（present / absent / unknown）、仅存在时的 `wal.bytes`、仅失败时的 `wal.error` 展示同一结果，不再 stat。离线命令自己打开库，观测包含这次打开的影响：hub 未运行、库上次正常关闭时，看到的是这次打开建立的 0 字节 `-wal`，不是 absent。WAL 实际长度不是未检查点数据量；本项不定义 checkpoint 策略或告警阈值。

QueryMetrics 两服务均返回与 ts 对齐的 coverage 三计数，以及独立的 coverage_summary。eligible 是完整落入请求窗口、起点不早于 coverage_start 且终点不晚于查询时当前分钟起点的整分钟集合；缺覆盖起点时为空。覆盖起点、水位、源桶与汇总在同一读事务读取；按水位拼接的源桶只在完整落入 eligible 时计入 observed/observed_reported，跨边界与 NULL 留在未知里，不按输出 step 重算。守恒为 observed_reported ≤ observed ≤ eligible，未知 = eligible − observed；保留期外缺行也属于未知，汇总不随 max_points 变化。

保留期默认 1m：7 天、5m：30 天、1h：365 天，可配。prune 按时间片分块删除，每块一个短事务，不长时间占住写协程。

查询按窗口跨度选定基础聚合级别：≤ 6h 用 1m，≤ 7d 用 5m，更长用 1h；再按目标点数上限在查询时二次分桶。指标与探测共用按各自族水位拼接的查询层：5m 查询在 5m 水位之前读 5m、之后读 1m；1h 查询在 1h 水位之前读 1h、两级水位之间读 5m、5m 水位之后读 1m。源区间互斥且首尾相接，水位与各级行在同一个只读事务内读取，避免上卷推进与清理期间混用不同快照。水位可能落在最终 step 内部，所有源行须按同一 step、用上卷的聚合表达式统一重聚合，不能直接拼接两侧已聚合的点；每条序列的 ts 唯一，均值按样本数加权，点数预算不变。响应的 `level` 表示基础级别，`step_s` 决定输出步长；末桶可由已刷出的分钟组成、不含 live 内存里尚未刷出的当前分钟。管理与公开服务共享此口径，探测任务标注与节点可见性规则不变。

历史查询按实际要读的源行数计读量额度，额度加在查询层本身、两端同一：聚合之前、同一读事务里按与聚合完全相同的条件对各级源行带上限计数（每级 `SELECT count(*) FROM (SELECT 1 … LIMIT 剩余额度+1)`，命中上限即已超额），累加超过 R 即返回 `FailedPrecondition`，错误带额度、参与查询的各级水位时刻与建议（缩窗口、加大 max_points、或等数据整理追上）；计数命中提前终止，未超额时各级计数恰为实际行数。R = 12000 × 序列上限：指标与覆盖率为 1（每节点每时刻一行），单节点探测查询为每节点任务上限 64，对比分块为 `max_nodes_per_query`。空库、纯未来窗口、水位跨界部不为拒：只按对齐后实际要读的行计，不从跨度或水位落后时长推算；已删任务的残留行也计在丙内——同节点反复换任务会拉开长窗口单节点查询的行数，超出即如实被拒。告警在 for_minutes 校验上限（60 分钟）下按满配 64 槽 + 残留满打满算也距额度很远。

历史查询按来源限制在飞并发（hub 可用 CPU 数的四分之一，至少 1 个；超出的最多等 5 秒再执行，仍无空位返回 `ResourceExhausted`，文案与限流区分；等待不占读连接、不开读事务，来源键与限流器同一口径：公开端是客户端来源、管理端是调用方凭据）。限流只限准入速率，封不住无界在飞积压：重查询零点几秒一个、来源满速开环发送时，在飞数随"准入速率 × 单请求时长"无限增长，读连接数随之无界。结构保证：入口与 history 层为一个历史请求做的库读（节点准入与查询本身）都发生在它持有本来源空位期间，同一来源至多这么多个历史请求同时在读库，每个来源占用的读连接随之有界，来源数本身不设上限；凭据鉴权在所有接口共用的拦截器里查库，早于入口，不在此列。无在飞也无等待的来源即时回收，状态不随来源数增长。取 CPU 四分之一的依据是受控环境的实测：历史查询是 CPU 密集的扫描，一个来源占用的 CPU 比例决定其他读者被拖慢多少。机器是 GitHub 托管 runner（AMD EPYC 7763，4 核，GOMAXPROCS=4），用开环饱和来源测受保护读者的 p99（压着时比空闲时，门槛 2 倍）：每来源上限 4 时 2.4–7.4 倍，2 时 1.5–2.5 倍，1 时 1.1–1.6 倍；同机闭环 365d 16 节点分块，k=1 时读者约 1.3 倍，k=2 时指标读者已到 2.2 倍。CPU 数取 GOMAXPROCS，Linux 上 Go 1.25 起默认按 cgroup 的 CPU 配额取值。面板一页同时在飞的历史查询至多 2 个（对比页的分块并发 2，节点详情两族各一个），上限为 1 的小机器上它们依次执行，后一个至多等 5 秒。

跨节点同目标对比：`ListProbeComparisonNodes` 在同一个只读事务里读出任务的候选节点（覆盖展开 ∩ 调用方节点可见性，按节点全序）与标注材料；候选为空与任务不存在回同一个 `NotFound`，不区分。`QueryProbeComparison` 按分块查询，一次至多 `max_nodes_per_query` 个节点（服务端唯一常量，见下）：逐节点按与单节点查询相同的准入判定，不可见或不存在进 `unavailable_node_ids`（顺序同请求），可见节点各一条序列、与请求同序，窗口内没有样本的也给空序列；样本与单节点查询同一形状与稀疏规则，聚合复用同一条查询层（第二分组键从 `task_id` 换成 `node_id`），同一任务的对比结果与逐节点单查一致。服务端常量为 16。成本验收要求两件事：一是对比分块每个窗口的 p99 不超过同窗口的单节点查询；二是饱和组按期内配对测量，压着类的 p99 不超过期内空闲类的 2 倍。期内配对测量是在同一轮里交替"开环 / 闲"周期，读者每 200ms 采样，按样本发出时刻是否落在饱和来源的请求区间内分类。受控环境（GitHub 托管 runner，AMD EPYC 7763，4 核）上，16 节点的分块在 6h、7d、30d、365d 四个窗口都满足第一条，四轮全过；饱和组在每来源上限取 CPU 数的四分之一时满足第二条。在 Apple M4 Max 上测，32 节点的分块在四个窗口的 p99 都超过同窗口的单节点查询。

存储健康信号：`GetStorageStats` 与 `heron-hub stats` 在库大小与行数之外，给出每级指标与探测表的最老桶时刻（按表分别给，不合并——各级保留期不同，合并后无法与各自的保留期对比）、每级上卷水位（直接取 `rollup_state.upto_ts`，不另算，与上卷读的是同一个值）、上次 prune 与上次上卷的完成时刻（记在与 `rollup_state` 同类的簿记表里，只在成功时写：字段缺失即从未跑过，失败与从未跑过因此可区分）。全部是聚合值，API token 可读（`ACCESS_READ`）。精确行数与最老桶都需扫描时序数据；六张时序表各用一条 `SELECT COUNT(*), min(ts)` 得出，不再重复扫描。计算使用 Store 自有的独立 `query_only` 连接（至多一条），不占共享读池；同一时刻至多一份计算，等待者共用结果，失败不缓存。请求并发与频率原本不受限，而整库扫描持有的长读事务会挡 WAL 重置（§13 第 9 项），因此成功完成后留出 60 秒复用窗口：存储页与 API 的分钟级新鲜度足够，单次耗时 S 时，成功扫描占据长读快照的时间比例不超过 S/(S+60秒)。等待者取消不取消计算；Store 关闭时取消并等它退出，再关独立连接。存储页显示“统计于”及复用说明，以 `sql_observed_at` 标明 SQL 原值的统计时刻。prune 停了只表现为库慢慢变大，上卷停了只表现为长窗口的图变空，最老桶对保留期、水位对当前时刻是一眼能读出故障的两组对照：面板的存储页显示这些数，并在最老桶早于"保留期 + 一个该级周期 + 一个维护间隔"、或水位落后当前时刻超过三个该级周期时标红。多出的一个维护间隔是因为 prune 的截止点按桶长向下对齐、维护每分钟跑一轮：截止点跨过桶边界之后、下一轮 prune 删掉那一桶之前，健康的表也会比保留期早一个桶长多一点，不加这一段每个桶长都会误报一次。新库在第一轮上卷之前水位为 0，会标红约一分钟，不加特判。"上次 prune"只指时序表的 prune；告警事件的清理另有保留期、不依赖上卷，不混进同一行。不给可回收空间（`freelist_count × page_size`）：hub 没有 VACUUM 入口，这个数没有动作可对应。

规模估算（500 节点，从常量算起）：1m 级 500 × 1440 × 7 = 504 万行；5m 级 500 × 288 × 30 = 432 万行；1h 级 500 × 24 × 365 = 438 万行；合计约 1370 万行。按每行约 120 字节估，指标三级表在 2 GB 上下，探测表另计；均未实测（见 §13）。

告警事件与投递记录（`alert_event`、`alert_delivery`）随维护任务按保留期清理，默认 90 天，由 `--retention-alert-events` 配置，下限 24 小时；抖动的节点会持续产生事件，不设保留期表会无限增长。下限必须大于满队列的最坏排空时长，否则维护会删掉仍在重试的事件，通知随之丢失。推导以 `retention_test.go` 从常量重算为准（队列容量 256 批、每批 3 次尝试各 10 s 超时、1 s 与 4 s 退避、Retry-After 上限 5 分钟、渠道节奏上限）：等待（退避、Retry-After、节奏空位）不占用 worker 时，当前常量下为 2 × 301 + 768 × 10 + 769 × 60 = 54422 s，约 15.1 小时（256 批各 3 次尝试共 768 次、每次 10 s 超时；节奏 1 条/分钟时每次请求各占一个 60 s 窗口，排空开始前一分钟内的请求使窗口多一个；Retry-After 上限 300 s 计两次，每次因 not_before 按秒向上取整多等不到 1 s、按 301 s 计）；模型不计数据库耗时、排空期间新到达的事件与存储故障的退避，这三者出现时排空会更久，但都不改变"下限必须大于上界"这条的算法；若等待占用 worker，同样常量下约 44.8 小时，超过 24 小时下限——"等待不占用 worker"是这条下限成立的前提之一。事件清理不依赖上卷水位，上卷失败不阻止它；删除走 `alert_event(at)` 索引，不全表扫描。规则、渠道与状态不清理。已到期的一次性维护静默（§9.5）按同一截止点随事件一起清理（`until_at` 早于截止点才删，到期后保留供审计），每日重复的永不清理。

### 6.6 其余表

schema 36 在 `node` 追加 `traffic_quota_bytes INTEGER NOT NULL DEFAULT 0` 与 `traffic_quota_mode TEXT NOT NULL DEFAULT 'sum'`，不改变指标表；配额与口径随节点配置整体替换，见 §7。

`probe_task` 另有 `cert_spki_sha256`（NULL 即不钉）与 `config_id`（16 字节，非空；内容变化时在保存事务里重生成，同时删掉该任务的证书行与候选行）。`probe_cert` 另有 `config_id`（NULL 表示旧 agent 写入、未绑定身份）。`probe_cert_presented`（`node_id, task_id, config_id, spki_sha256, not_after, reason, observed_at`，主键是节点与任务）只存当前身份的信任候选，写入条件与 `probe_cert` 相同且不接受空身份；两张表都随任务删除与节点删除消失。schema 35 给已有任务生成 `config_id` 并推进清单版本。`node`（名称、排序、是否公开、备注、公开备注 `public_remark`（站长写给访客的一行说明，空串即没有；准入在 api 的共享准入层：单行、至多 100 个 Unicode 码点、拒绝控制字符，超长拒绝不截断；随 `UpdateNode` 整体替换，创建时不可设置）、离线宽限期、流量重置日、维护开关 `maintenance`（§9.5）、token_hash，§9.4 的计费五列：价格、币种、周期、到期日、自动续期，以及 §4.9 的 `last_source`、`country`、`country_ip`、`country_pin`）、`node_facts`（facts_hash 与各静态字段，另有 `execution`（执行环境的 protojson，`'null'` 表示未上报）与 `facts_rev`（持久化字段集合版本，旧行是 0）；schema 34 同时给三张指标表加 `load1_per_core`）、`traffic`、`probe_task`、`probe_task_node`、`probe_meta`（任务版本号）、`probe_cert`（(节点, 任务) 的最新一份证书到期观测，§8.3；随节点一起消失的表之一）、`alert_rule`（到期规则另有 `days_before`，其余种类为 NULL）、`alert_rule_node`（显式作用域；`alert_rule.all_nodes` 为真时不存行且覆盖全部节点，为假时无行表示不覆盖任何节点——删除作用域里最后一个节点不会放宽到全部）、`alert_rule_channel`、`alert_state`（到期规则另带进入 `firing` 时的到期日 `fired_expires_on`，恢复文案据它判断日期是否改过，§9.2；另带 `fired_silenced`，§9.5）、`alert_event`（带 `silenced`，§9.5）、`alert_delivery`（每事件每渠道一行投递记录，`batch_id` 非空、同批各行共享尝试计数与结果，`not_before` 为下一次尝试的最早时刻；失败类别、HTTP 状态码与错误原文分列存放，同一次发送覆盖的多行共享 `batch_id`，见 §9.3）、`notify_channel`、`silence`（§9.5 的维护静默：名称、启用、作用域形状与 `alert_rule` 相同、种类、窗口、原因、创建时刻）、`silence_node`（显式作用域，语义同 `alert_rule_node`）与 `silence_tag`（动态标签选择器，§10 标签一条）、`setting`、`admin`、`admin_session`、`api_token`（名称、token_hash、创建时间、最后使用时间）、`register_window`、`rollup_state`、`maintenance_state`（prune 与上卷的完成时刻，§6.5）、`tag`（名称）与 `node_tag`（节点与标签多对多，§10 的标签一条）、`theme`（主题身份）、`theme_version`（以 ID+SHA256 标识的不可变版本元数据、SDK、来源和公开发布状态）、`theme_selection`（全站当前与回滚引用）、`theme_file`（按 ID+摘要+路径存储展开文件）与 `theme_package`（原始 ZIP、本次落库随机 revision、uploaded 标记；上传确认同时匹配 ID、digest、revision；文件和原包不进快照层）、`restore_record`（§6.7）。

schema 版本记在 `PRAGMA user_version`，迁移为按版本号顺序执行的函数；空库直接建到当前版本，不重放历史。打开库时的 schema 策略由调用方显式给出：只有 `serve` 迁移旧库，每迁一步记一行日志（from、to），空库建成时也记一行；离线子命令（`passwd`、`token`、`stats`、`node`、`window`）打开比自己旧的库时拒绝并提示先用新版本 `serve` 升级（升级前备份）——否则运维用新二进制看一眼 `stats` 就把库单向迁走，旧 hub 下次重启起不来；两种策略下建空库都允许（没有旧数据可丢）——空库指没有任何对象的文件；有表却没有版本号、或版本号为负的文件不是本项目的库，拒绝打开而不是当作空库建表或当作旧库去迁（否则 `stats --db` 指错文件会往别人的库里建出全部表）；比二进制新的库都拒绝。

### 6.7 分层自动备份到 S3 兼容对象存储

投影迁移清单：schema 36 只给配置层 `node` 加配额和口径两列，指标层显式无变化；新建库、live 升级与两层快照恢复采用同一默认值，旧快照恢复后配额为 0。

node_coverage 与 metric_* 同属指标层、同快照恢复；只恢复配置不覆盖它。DeleteNode 与恢复孤儿清理共用节点从属表清单，起点与指标行一起消失。删除后经旧配置复活的节点没有覆盖记录，不拿旧 last_seen_at 重新生成起点；下一次上报重新确立。旧指标快照投影迁移仅依据自身留存指标回填，不从另一时点的配置层推断缺失历史。

hub 自行把数据推送到 S3 兼容对象存储（R2 为首选 endpoint），分两层、两个周期：配置与凭据分钟级（默认 5 分钟），指标与探测历史按天。运维配置一次，此后无需人工动作；恢复不自动化，是显式的运维操作。整套数据只在一个 SQLite 文件里，宿主盘损坏即丢掉全部历史与全部节点凭据，而手动导出的可靠性取决于人是否记得做。分层而不整库周期快照：500 节点的指标三级表约 2 GB，配置与凭据合计几百 KB，整库快照只能退到按天，全库 RPO 被最不值钱的那部分拖到 24 小时；分层后最痛的部分拿到分钟级 RPO。

分层判据两条各管一件事：进哪一层看"丢了能不能自愈"（指标丢了节点继续上报、过去是空洞，可按天；配置、凭据、累计流量、告警历史、标签、主题清单都不自愈，分钟级）；描述与被描述必须同层（`rollup_state` 与 `maintenance_state` 描述指标内容，跟指标层走：水位若比它描述的数据新，晚到的数据会按 §6.4 被丢弃）。配置层：`node`、`node_facts`、`traffic`、`probe_task`、`probe_task_node`、`probe_task_tag`、`probe_meta`、`probe_cert`、`probe_cert_presented`、`alert_rule`、`alert_rule_node`、`alert_rule_tag`、`alert_rule_channel`、`alert_state`、`alert_event`、`alert_delivery`、`notify_channel`、`silence`、`silence_node`、`silence_tag`、`setting`、`admin`、`admin_security`、`api_token`、`tag`、`node_tag`、`theme`、`theme_version`、`theme_selection`、`restore_record`；指标层：`metric_*`、`probe_*`、`rollup_state`、`maintenance_state`；主题产物（`theme_file` 与原包 `theme_package`）体量比配置层大三个数量级，不进快照层，按变更时备份：库里保存上传时的原包（`theme_package`，带本次写入的随机标识与是否已上传），每次上传或删除主题后唤醒配置层立即执行一轮完整备份。配置快照的同一读事务生成 `snapshot_theme(theme_id, digest, sha256)` 引用清单，并将该视图中的原包逐个写入私有暂存目录；先按摘要上传缺失原包，再发布配置快照，任何原包上传失败都不发布该配置快照。sha256 为空明确表示该版本取快照时没有原包；digest 保留已知版本身份。清单覆盖全部保留版本，不仅是当前主题，当前与回滚引用同快照保存。主题对象不可变，替换或删除当前主题不会删除历史原包；当前不自动回收主题对象，避免删掉历史快照仍引用的内容。配置层每个周期也执行同样流程兜底；唤醒的完整一轮会多占用配置层的一份保留名额（换取最新的配置快照与主题对象一致），调试主题期间反复上传会缩短配置层可回溯的时间窗；升级前安装、没有原包的主题不算故障，同步跳过它并在备份状态里列出、提示重新上传；不备份 `admin_session`（重新登录即可，恢复它等于复活可能已登出的会话）与 `register_window`（限时限量，恢复旧窗口会复活已消耗的名额）。

每层快照都是单事务内的一致读：`ATTACH` 一个临时库，在一个读事务里逐表 `CREATE TABLE … AS SELECT`（每层都另带一份 `sqlite_sequence` 与 `snapshot_meta` 作簿记，`snapshot_meta.format_version=2` 独立于数据库 schema 版本——序列是 `node.id` 不复用那条不变式的载体，漏搬即失效，恢复时按表名取各来源的最大值），产物是可校验的 SQLite 文件，上传后删除临时文件；跨表分多次读会得到互相矛盾的配置。对象键 `<前缀>/config/<UTC 时刻>.db`、`<前缀>/metrics/<UTC 时刻>.db`、`<前缀>/theme/sha256/<SHA256>.zip`。保留按份数、hub 自删：配置层默认 48 份、指标层 14 份（可配），每次上传成功后列出该层对象、删除超出份数的最旧者；bucket 不得公开可读（内含口令哈希、token 哈希与全部拓扑，足以离线爆破弱口令）。不加密：保密由私有 bucket 与 TLS 承载。配置：endpoint、bucket、区域、access key、secret（只写不读，与渠道凭据同一做法）、前缀、两层周期、两层份数，存于 `setting`，经面板设置；未配置即整体关闭。周期与份数的取值范围：配置层周期 60–86400 秒、指标层周期 3600–604800 秒、两层份数各 1–1000；`UpdateSettings` 里这四个字段是 `optional`，缺席即不变（与 §10 总闸同一口径），给出的值出范围（含显式的 0）返回 `InvalidArgument` 并点名字段与范围——0 不表示"取默认"，份数为 0 会在上传成功后把这一层全删光，周期为 0 会空转，两者都不是任何人想要的配置；库里没有这个键时取默认值，回到默认就显式写默认值。上限只是防止把一层实际关掉而面板上看不出来：周期超过一天的配置层已经不是分钟级 RPO，要关就清掉 endpoint。S3 客户端只实现 SigV4 的 PutObject、ListObjectsV2、DeleteObject、GetObject，纯 Go；出站复用 §9.3 的出站边界——同一个不跟随重定向的传输、不含 URL 的错误文本与状态码判定——而不是它的总时限：期限经 ctx 逐次给出，配置层的上传按对象大小推导，指标层的上传按它自己的周期推导（周期减去保留预算，一次上传必须在下一次快照之前结束），列举与删除共用一个固定期限，客户端本身只限建连与首字节（同一个客户端既传几百 KB 的配置层也传 GB 级的指标层，固定总时限取长了配置层卡住要等满才失败、取短了大对象必然超时）；§9.3 的 64 KiB 应答上限不适用于下载与列举，两处各有自己的上限。这是架构里第一个 hub 主动出站且携带长期凭据的路径，与 §5.4 的出站目标清单同列：由运维显式配置、默认关闭、目标来自配置。

配置层上传失败必须告警，不得静默重试到下一周期——静默失效会让实际 RPO 无声退回 24 小时，失败与从未跑过在事后看长得一样：失败作为事件进 `alert_event`（规则与节点为 0，同 §5.3 登录通知的形态）投递到设置里选定的渠道（键 `notify.backup_channels`，`Settings` 里的形态与更新语义同 §5.3 的登录通知渠道：带 presence 的 message、缺席不变、显式空集合关闭、至多 16 个），同一故障只在首次失败与恢复时各发一条，首次失败的时刻随标记落库（与事件同一事务），重启后不重复发；故障类别除快照、上传、保留与设置读不出之外，还有标记读不出（坏标记先触发一次故障事件、再由覆盖写修复，上传正常即恢复——否则配置层永久卡住且没有接口能修）与启动准备失败（读回成功时刻或清理残留失败），都按配置层类别处理；坏标记不挡住指标层，启动准备失败期间指标层暂停；停用备份（endpoint 清空）即结束两层的故障跟踪并清除标记，已通知过的配置层故障以一条停用事件（transition `backup_disabled`）收尾，未通知过的不发；面板显示两层各自的上次成功时刻与当前故障。`GetBackupStatus`（只读口径）返回同一份数据。

恢复是离线子命令 `heron-hub restore --db <库> --config <配置层文件> [--metrics <指标层文件>] [--themes <目录>]`（新格式目录内使用从 S3 取回的 `<SHA256>.zip`，须匹配配置快照引用的 SHA256 与包内主题 ID；指定 `--themes` 时任何引用缺包或摘要不符都整体拒绝，不能用同 ID 的较新原包替代。历史格式没有摘要清单，仍按 `<id>.zip` 校验并保留旧格式的缺包停用行为。不带 `--themes` 是显式降级：全部主题保留清单但停用、清空预览与内容，缺包及忽略文件写入恢复摘要。所有包复用上传校验器，任一包非法时目标不动）；须在 hub 停止时进行（整表覆盖一个正在被写协程写入的库没有一致性可言，hub 的设置内存副本与内存索引也只在 Open 时加载，不会因库被改写而重载），需要确认，无终端时须 `--yes`：两层不对齐是常态，以配置层为准、不把配置回退去迁就指标层——否则分层的全部收益都被抹掉；差异只有一种可见形态：节点存在但在 `[指标层时刻, 恢复时刻]` 区间没有历史，与 §7 的断连空洞是同一个状态。恢复前校验来源：支持 schema 17 至当前版本，拒绝更旧、不支持及未来版本；来源以只读连接在同一事务中复制到私有临时目录，显式保留 `sqlite_sequence`，再按已审定的分层迁移升到当前 schema，不改来源文件。不能直接重放完整 live 数据库迁移，因为每层只有部分表；增加 schema 版本时必须同步补齐两层投影迁移。新旧快照均校验表齐全、页大小及格式版本，任一不符即拒绝。恢复目标内容在单事务内替换，失败不改变目标；已有管理员会话全部撤销，不从目标沿用授权。恢复后显式清理孤儿行，不依赖外键（`ON DELETE CASCADE` 只在 DELETE 时触发，恢复是整表覆盖；SQLite 的外键默认还不开启）：指向不存在节点的指标、探测、流量、标签关联行删除并记数，方向恒为"以 `node` 表为准"，与哪层更新无关；告警事件是审计历史，与删节点时一样保留（系统事件以 node_id=0 写入，按 `node` 表清理会把备份失败事件一起删掉）；"随节点一起消失的表"只有一份清单，删节点与恢复都遍历它，并有用例把它与库里所有带 node_id 列的表对齐、显式登记例外。恢复同时写入不投递的 `backup_restored` 系统事件，并落一条记录到 `restore_record`（两层各自的时刻、恢复时刻、清理了什么、主题摘要：恢复了 / 缺包 / 忽略了哪些）：空洞若无处解释，半年后没人能判断它是断连、回滚还是缺陷；这张表属于配置层（不自愈），恢复时与目标库已有的记录取并集而不是整表替换——否则从快照恢复本身会抹掉之前的恢复记录。恢复的目标库准入与建库和 §6.6 的离线命令共用同一份判定与同一行建库日志：目标库比二进制新、版本为负、有对象无版本号各报各的原因，恢复到新文件时放出 database schema created，写错 `--db` 才看得见。`node.id` 永不复用（`INTEGER PRIMARY KEY AUTOINCREMENT`，M1 起如此）是两层可以各自漂移的前提：若复用，指标层里 id=7 还是已删的旧节点，配置层里 id=7 已是新建的另一台，恢复后新节点的图表挂着别人的历史，不报错、不可由肉眼分辨。所以恢复不能让序列回退：`sqlite_sequence` 不属于任何一层的数据表，而是分配状态，每层快照都带一份（与 `snapshot_meta` 同为快照簿记）；恢复后每张 AUTOINCREMENT 表的序列取目标库现有值、配置层、指标层三者的最大值——配置层整表替换带回的是它取快照时刻的序列，节点在那之后还可能分配过，指标层与目标库各自见过更晚的分配；只有单调不降的序列能保证恢复后新建的节点不会拿到任何一层见过的 id。

不用 Litestream：它把 WAL 帧连续复制到对象存储、RPO 秒级，但要求没有别的东西 checkpoint WAL，嵌进同一进程意味着 hub 自己不得 `VACUUM`、必须关掉 `wal_autocheckpoint`，是一条靠"没人写那行代码"维持的不变式；其支持面是 CLI 而非库，S3 后端会把 AWS SDK 拖进 hub。条件若反转（库里出现丢一分钟都不可接受的账目类数据）再重新考虑，且走 sidecar 而非嵌入。

## 7. 流量累计

**配额与口径**：节点的 `traffic_quota_bytes` 范围为 [0, 2^62) 字节，0 表示未设配额并停用该节点的配额告警；`traffic_quota_mode` 为 `sum`（收+发）、`rx`、`tx`、`max`（取大者）。配额非零必须显式给合法口径，配额为 0 忽略口径并存 `sum`。`UpdateNode` 整体替换，缺省配额即停用；`ExecuteChange.updateNode` 按 mask 合并后经同一校验，未指定路径不清配额；`CreateNode` 不接受配额。

共享计算入口 `traffic.Quota` 先把两个非负 int64 转为 uint64 再求和，两个 MaxInt64 的和仍可表示；百分比按 float64 算。`Traffic.quota_used_bytes` 是该口径的分子，配额为 0 时仍有已用量，`quota_used_pct` 则缺失。管理和公开接口共用这份计算，四个配额字段随公开节点下发；面板与公开卡片只展示 hub 给出的已用 / 配额（百分比），不另求收发之和。编辑器接受 GiB/TiB/GB/TB，十进制字符串用 BigInt 换算，最终不足一字节的部分四舍五入；正输入若舍入为 0 则拒绝，未触碰时提交库内原始字节值，不把显示精度当存储精度。

每节点一个内存累加器：`boot_id`、`net_counter_epoch`、`last_rx`、`last_tx`、`total_rx`、`total_tx`、`period_rx`、`period_tx`、`period_start`、脏标记。`net_counter_epoch` 是实际计入合计的网卡名称按字节顺序排序、JSON 编码后的 SHA-256 小写十六进制；与计数同次采样，独立于机器启动标识。名字集合不识别同名设备在两次采样之间被替换；计数倒退仍由原有守卫处理。

- 每次上报：若 `boot_id` 和 `net_counter_epoch` 都相同且计数器不小于基线，增量 = 计数器 − 基线，计入总量与周期量；随后基线 = 计数器。
- 启动周期或网卡集合变化、或计数器小于基线（网卡重置、32 位回绕）：只把基线重置为当前计数器，不入账。聚合计数无法拆出其中仍有效的增量，因此舍弃相对旧基线的整个区间，避免把另一台机器或新增网卡已有的计数一次性记入；这不只丢失集合切换之后的流量。频繁增删计入网卡会持续少计，必要时用包含规则选定稳定接口，此口径不保证无损计费。旧 Agent 的空集合标识仍可使用，空与非空互换也重建一次；Agent 本地速率同样只在集合未变时计算，切换样本无速率，不伪造零值。
- 计数器缺失（`optional` 未设置）：不入账也**不动基线**。"无读数"不是"读数为 0"，把基线改成 0 会让下一次上报的增量等于计数器全值。
- 总量用饱和加法，不回绕。
- 每 10 秒与退出时，把所有脏条目在一个事务里落盘，基线与累计值同写。

**崩溃不变式**：基线（含网卡集合标识）与累计值总是同事务持久化，所以崩溃后的首次上报相对"已落盘的基线"做差分，覆盖崩溃丢失的内存增量。前提是其间启动周期、集合不变且计数不倒退；否则窗口内的流量不可知，按上一条规则重置基线。

**图表与总量的关系**：断连恢复后的首个增量覆盖整个断连区间，若计入单个分钟桶会在速率图上形成假尖峰。因此当距该节点上次上报的间隔超过 TTL 时，该增量只计入 `traffic` 的总量与周期量，不计入 `metric_1m`；hub 重启后各节点的首次上报同样处理（上次上报的时刻已不可知）。由此，`metric_*` 的 `rx_bytes` 对时间求和等于同区间总量的增量，这条等式只在"节点连续在线、hub 未重启、且区间内的分钟行都成功落库"的区间上成立；断连区间在图表上本来就是空洞。

周期用量按节点的重置日（1–28）滚动，时区由 hub 的 `--timezone` 决定；滚动在入账与落盘路径上判定：`now` 不早于 `period_start` 之后的首个重置日零点时，周期量清零、`period_start` 推进到该零点。重置日经 `UpdateNode` 修改。

**读写接口**：`GetTraffic` 返回全部节点的总量、周期量、`period_start` 与下次重置时刻；`GetSnapshot` 的每个节点状态也带周期量与总量，实时视图不需要第二条轮询。`AdjustTraffic(node_id, period_rx, period_tx)` 把当前周期的两个用量覆盖为给定值（把面板对齐到云商计费口径的那一次操作），总量按同一差值同步调整且不低于 0；基线不动，之后的增量照常叠加；内存条目与库在同一把锁下同步写入，不经过 10 秒刷出。

agent 默认汇总除回环与虚拟网卡外的全部网卡（Linux：`lo`、`docker*`、`veth*`、`br-*`、`virbr*`；darwin：`lo*`、`gif*`、`stf*`、`utun*`、`ipsec*`、`bridge*`、`vmenet*`、`awdl*`、`llw*`、`anpi*`、`ap*`——回环，以及字节同时计在物理网口上或不出本机的接口），可用 `--net-include` / `--net-exclude` 覆盖。进程数两个平台都按进程计：Linux 数 `/proc` 下的进程目录（不用 `/proc/loadavg` 第 4 字段——那是含线程的调度实体数），darwin 用 `proc_listallpids`。`/proc` 以 `hidepid=2` 挂载时非 root 的 agent 只看得到自己的进程：同一次目录列表里必须有名为 `1` 的进程目录，没有就让进程数缺失并记日志，不上报一个错的小数。合计型读数（连接数、流量、进程数）的口径都是要么正确要么缺失：成员读不出就整个缺失，不给缺一半的合计；网卡在列出与读取之间消失（ENOENT）只略过该网卡。

## 8. 探测

### 8.1 任务与版本

`ProbeTask{id, kind(icmp|tcp|http|dns), target, interval_s, timeout_ms, dns_server, cert_spki_sha256, config_id}`，通过 `probe_task_node` 分配到节点。`cert_spki_sha256` 是钉住的叶证书公钥指纹（恰 32 字节，只允许 https 目标的 HTTP 任务），`config_id` 是任务配置身份（16 字节随机数，任务内容变化时 hub 重新生成，内容不变的保存保留原值，只比较相等；agent 在结果里原样回显）。`target` 的含义按种类：ICMP 是 IP 或主机名，TCP 是 `host:port`，HTTP 是绝对 `http(s)` URL（有主机、不含用户信息、不含片段），DNS 是要解析的 DNS 名（不是 IP 字面量——探测 IP 字面量量的是本机组包，不是解析路径）。`dns_server` 是 DNS 任务要查询的解析器，`ip:port` 规范形式且只允许 IP 字面量：解析器自身若是名字就要先用别的解析器解析它，§5.7 的地址策略也就查不到实际要发包的地址；该字段只对 DNS 任务有意义，其他种类携带即 `InvalidArgument`——静默忽略会让调用方以为它生效了。字段约束的裁决只有 `probelimit.CheckTask` 一处，hub 保存与 agent 执行前各过一遍，错误文案点名字段、约束与期望取值。经管理接口对任务或分配的任何修改都经由 `probe` 包内唯一的写入口，在同一事务内把 `probe_meta.version` 改为 `max(version + 1, 修改时刻的 Unix 秒)`：严格递增，且库从备份恢复后重做编辑得到的值大于 agent 从旧库拿到的值——收敛条件是重做时的 Unix 秒大于 agent 持有的旧版本值（同一秒内多次编辑会把版本推到秒数之上）；同秒重做或时钟回拨到该值以下仍可能碰撞，需重启 agent。agent 只比较相等与否。删除节点顺带删除它的分配行不改版本——版本号的用途是让清单变化的 agent 重取，被删节点的 token 已撤销、其余节点的清单未变。版本全局唯一而非每节点一份：修改是管理员的低频动作，全体 agent 各多取一次列表的代价可以忽略，换来的是不需要维护"哪些节点受这次修改影响"的推导。

探测任务可声明作用于全部节点（`ProbeTaskDetail.all_nodes`，管理端字段；agent 只拿展开后的清单）。语义与 `alert_rule.all_nodes` 完全一致：为真时不存分配行、覆盖全部节点，之后新建的节点自动纳入；为假时空分配集不覆盖任何节点，删掉最后一个分配不放宽——同一形状的开关只有一种语义。每节点任务数上限（§8.4 的 64）在保存任务与建节点两处都校验：新建节点会继承全部 `all_nodes` 任务，超限时建节点失败并说明。建节点（面板的 `CreateNode` 与 agent 的自助注册共用同一入口）必须推进任务版本：版本的不变式是"任何一个节点的清单变了，版本就变"，新节点的清单从空变为全部 `all_nodes` 任务；agent 只比较相等，不能指望它恰好持有别的值（刚启动的 agent 报 0，编辑过任务的 hub 版本不小于 Unix 秒，两者不等只是巧合，不是机制）。删除节点仍不推。`ListProbeTasks` 对 `all_nodes` 任务回显当前展开的节点列表并带开关，面板与 agent 都不必自己展开；公开端的任务标签规则不变：`all_nodes` 任务对每个公开节点都算"当前分配"。

展示顺序单独保存在 `probe_task.sort_order`。`ReorderProbeTasks` 只接受全部现存任务 ID 的完整排列，在写事务中拒绝重复、缺漏和未知 ID；新任务追加到末尾，编辑保留顺序。管理清单、管理端和公开端历史使用同一顺序，已删除任务的历史排在现存任务之后并按 ID 排列。重排不改变任务内容或分配，因此不推进 `probe_meta.version`；agent 清单仍按 ID 下发，展示偏好不改变执行配置。

### 8.2 执行

- ICMP：优先用非特权数据报 ICMP socket；不可用且进程持有 `CAP_NET_RAW` 时退到 raw socket；都不可用则每次回报 `error`，面板显示原因，而不是静默呈现为 100% 丢包。
- TCP：连接建立耗时即 rtt，解析在计时之前完成。
- HTTP：一次 GET，rtt 从拨号开始到收到响应头为止，解析与整个请求（连接、TLS、等响应头）共用 `timeout_ms` 预算。先经共用的解析入口把 URL 主机解析成一个地址，自定义 `DialContext` 只连那个地址（忽略传入地址，端口取 URL 的或默认 80/443），SNI 与 `Host` 仍是 URL 里的名字；不跟随重定向（3xx 本身就是答案）、不复用连接（每次探测新建 Transport）、不读正文（拿到响应头即关闭）；`User-Agent` 是 `heron-agent/<version>`。收到响应头且状态 < 400 是成功；状态 ≥400 与 TLS 握手失败（含证书错误：过期、名字不符、自签）计入丢包；URL 非法、解析失败、地址策略拒绝与 socket/fd 等本机原因是 `error`。target 为 `https://` 的任务握手成功（成功结果）时顺带在 `cert_not_after_s` 带回链首枚证书的到期时刻，按 (task_id, config_id) 每小时至多携带一次——证书到期日按天变化，每次都带是无意义的重复字节，一小时内能看到更换后的新到期日已经足够；不上报签发者与证书链。携带 `cert_spki_sha256` 的 HTTPS 任务关掉默认的链与主机名校验（`InsecureSkipVerify`），改在 `VerifyConnection` 里只比对叶证书公钥指纹与有效期：钉住即把信任锚定在这份公钥上，同钥续签不受影响；指纹不符或不在有效期中止握手、归为丢包。钉住的安全性前提是握手完整性不被 `InsecureSkipVerify` 跳过：证明私钥持有的握手签名（TLS 1.3 的 CertificateVerify、TLS 1.2 ECDHE 套件的 ServerKeyExchange 签名）仍用对方出示的叶证书公钥验证，拿不到私钥的一方即使出示的正是钉住的那份证书也过不去（Go 1.27.1 两个版本实测，错误均为 `tls: invalid signature by the server certificate`；同一条链配正确私钥的对照组成功；agent 侧有契约测试钉住这一行为）。证书相关的丢包（默认校验失败、指纹不符、不在有效期）在结果里带 `presented`：握手失败链上对方出示的叶证书指纹、到期时刻与失败类别，作为面板上的信任候选；agent 不替管理员做信任决定。成功证书与候选是两个独立时钟，各自按 (task_id, config_id) 每小时至多一次：任务内容一变即是另一份配置，限频从新身份的首次观测重新开始；Apply 换身份或任务消失时删除旧身份的限频状态，取消后才返回的旧探测写状态前核对身份仍是当前的，不重建已删的键。上报的 `capabilities` 声明 `PROBE_CERT_PIN`，钉住的任务只下发给声明它的 agent；旧 agent 收到钉住任务会按未知字段忽略，所以 hub 必须按能力过滤而不是假设所有 agent 都支持。
- DNS：手组一条 A 查询发给 `dns_server` 指定的解析器，单个 UDP 包，不重试、不走 TCP——重试会把一次失败伪装成一次慢成功，探的是"这个解析器此刻能不能答出 A 记录"。只有对得上号（ID 匹配且是应答）、NOERROR 且带着至少一条 A 记录的应答才是成功；NXDOMAIN/SERVFAIL/REFUSED、无 A 记录、解不开的应答与张冠李戴的应答都计入丢包；`dns_server` 非法、地址策略拒绝（解析器地址同样过 §5.7 的本地策略）与 socket/fd 等本机原因是 `error`。rtt 从发出查询到收到应答。
- 丢包与 `error` 的分界四种探测共用一句口径：这一次没有联通是可达性事实，计入丢包（超时、连接被拒或重置、网络或主机不可达、HTTP 状态 ≥400、TLS 握手失败、DNS 拒绝或答非所问）；本地无法发起才是 `error`（无 socket、URL 或 `dns_server` 非法、解析失败、地址策略拒绝、fd 耗尽、权限）。连接与发送失败的归类集中在 `classify` 一处，四种探测共用。
- hub 不按 agent 版本过滤任务：旧 agent 在 `Scheduler.Apply` 里先用自己版本的 `probelimit.CheckTask` 校验清单（自 v0.1.0 起各版本都如此），不认识的种类在调度前被拒，按被拒任务留一条 error 结果（原因形如 `kind must be PROBE_KIND_ICMP or PROBE_KIND_TCP; got PROBE_KIND_HTTP`），面板显示为 `error`；版本偏斜由这条 error 暴露，而不是由 hub 侧的版本协商掩盖。`Multi` 的默认分支（`unsupported probe kind`）只在绕过 `Apply` 直接调用引擎时可达。
- 名字的地址族按本机可建的 socket 选、v4 优先、不看路由；仅 IPv6 的主机上双栈名字会选到 v4，是当前的已知限制。
- 各任务的首次触发时刻加随机偏移，避免同一时刻齐发。
- ICMP 实现用 `golang.org/x/net/icmp`（纯 Go，与 hub 已依赖的 `x/crypto` 同源；§2 的"零第三方依赖"说的是 /proc / /sys 采集）。启动时探测两种 socket 的可用性并写入 `Facts.icmp_available`。每个地址族一个共享 socket，单读协程按 payload（进程 nonce + task_id + seq）把回包分发给等待中的探测；不按 ICMP ID 匹配——Linux 数据报 socket 的回包 ID 被内核改成本地端口，macOS 的公网回包 ID 也会被改写；读侧只接受 Echo Reply，raw socket 与 macOS 的 udp6 会先读到自己发出的 Echo Request，macOS 同进程的数据报 socket 之间会互相收到对方的回包（§13 第 2 项的实验结论）。
- 结果进有界队列，上报时整体取走并按单调钟折算 `age_ms`；队列满时丢最旧的并计数，不阻塞探测协程。休眠信号（§4.5）触发时队列中此前入队的全部结果作废并计入同一丢弃统计。任务集更新时停掉消失的任务、启动新增的任务，未变化的任务不重启计时。

### 8.3 对账与入库

版本在 hub 进程内缓存，由 `probe` 包的写入口在事务提交后更新。`Report` 对清单的对账摘要优先：请求带了 `tasks_digest`（恰 32 字节，否则整批 `InvalidArgument`）时，hub 用 `agentwire.TasksDigest` 对**本次能力过滤后**的清单算同一摘要，不等就下发，与 `tasks_version` 无关；摘要算不出来按下发处理并记日志，不拒绝上报。字段缺席是旧 agent，仍只比较两个版本整数。每个节点只缓存当前一份摘要，键是注册表版本与实际授予的能力集合。快照恢复在写回 `probe_meta` 之后按 `bumpProbeVersion` 再推进一次，时钟正常时也让旧 agent 的计数对不上恢复前的清单。

`TasksFor` 用这次上报的能力过滤：不声明 `PROBE_CERT_PIN` 的节点收不到已钉指纹的任务。能力条数上限是 `agentwire.MaxCapabilities`，重复项与不认识的值都计入，超出整批拒绝；之后去重并丢掉未知值。hub 记住本次启动以来每个节点最近一次上报的能力，供 `ListProbeCertificates` 显示支持、未下发或尚未上报。

结果逐条校验后才折叠进内存桶。结构问题整批 `InvalidArgument`：`cert_not_after_s` 必须为正且只随 `rtt_us`；`presented` 的指纹恰 32 字节、到期为正、reason 合法且只随 timeout；`task_config_id` 为空或恰 16 字节。归属、时效、能力与配置相关的检查只丢弃整条结果或只丢弃附带观测，不整批拒绝：未分配或超龄丢整条；任务已钉住而本次上报不声明能力也丢整条；`cert_not_after_s` 只在任务当前是 https 的 HTTP 任务、且身份等于当前 `config_id`（或身份为空且任务未钉住）时留下，否则只丢这份观测；`presented` 只在任务当前是 https、身份非空且等于当前 `config_id` 时留下。任务被删或从 HTTPS 改成别的种类时，在途观测因此不会挡住下一次清单下发。

证书与候选的写在写协程的同一个事务里再核对三件事：任务还在、身份相符（成功证书还允许“身份为空且任务未钉住”）、节点仍分配到该任务。不符就不写。任务保存使 `config_id` 变化时，同一事务删掉该任务的 `probe_cert` 与 `probe_cert_presented` 行，旧身份的观测不能重建或覆盖新行。到期告警只读 `probe_cert`，不读候选。

`SaveProbeTask` 与 `ExecuteChange` 的保存分支调用同一个写函数。`task.config_id` 与 `task.cert_spki_sha256` 是只输出字段，输入忽略。指纹只由 `cert_pin` 改变：缺席保持原值，`set` 的空字节与未选 oneof 是 `InvalidArgument`，清除必须用 `clear`。`expected_config_id` 在写事务里比对，不符是 `FailedPrecondition`；既有任务做 `set` 必须带它，新建任务不得带。只做指纹动作、不改其他字段是合法保存。信任操作就是对看过的那份配置执行 `cert_pin.set`：会话可以，或者在 `ExecuteChange` 上被预授权 `TOKEN_PERMISSION_CONFIGURE`、并在写事务里通过对任务节点选择器范围裁决的 API token。不泛称“有写权限”。`ListProbeCertificates`（`ACCESS_READ`）在一个读快照里返回当前身份、pin，以及可见节点上的能力、当前证书、未绑定的旧 agent 观测和当前身份的候选；受限 token 的任务必须是它可标注的任务，否则与任务不存在同一 `NotFound`。公开端不提供这个方法。

桶键是 `(node_id, 分钟, task_id)`：`sent` 每条加一，`timeout` 计入 `lost`，`error` 计入 `errors`，`rtt_us` 累加到 `rtt_sum` 并更新 `rtt_min` / `rtt_max`。刷出、加法合并、冻结检查与 §6.2 的指标桶共用同一条路径。删除任务不删已有历史，到期由 prune 清理；`QueryProbes` 对这些行只带 `task_id`，不再有类型与目标。

`QueryProbes(node_id, from, to, max_points)` 与 `QueryMetrics` 同一套选级与对齐规则，按任务返回序列，每个点是 `ProbeSample{ts, sent, lost, errors, optional rtt_mean_us, optional rtt_min_us, optional rtt_max_us}`；`sent = 0` 的桶不出样本；rtt 三项只在 `sent − lost − errors > 0` 时存在，缺失由 `optional` 表达而不是零值（平行数组无法表达"这一点没有 rtt"）。丢包率 = `lost / sent`，`errors` 不计入丢包。任务管理经 `ListProbeTasks`（含分配节点）、`SaveProbeTask`（id 为 0 即创建，提交整份分配列表）、`DeleteProbeTask`。

### 8.4 agent 侧硬限制

agent 强制执行、hub 侧同步校验（两侧各有断言）：探测间隔 ≥ 5s、任务数 ≤ 64、单次探测 1 个包（ICMP 一个 Echo、TCP 一次连接、HTTP 一次请求一个连接不重试不跟随、DNS 一个查询包不重试）、超时 ≤ 5s；`target` 按种类有上限（ICMP/TCP/DNS 253 字节，HTTP 512 字节），`dns_server` ≤ 47 字节（最长规范形式：39 字节 IPv6 + 方括号 2 + 冒号 1 + 端口 5 位，常量与校验绑在同一处）。HTTP 目标取 512 而不是更大：hub 下发的任何合法清单都必须装进 §5.9 的 64 KiB 响应上限——64 个任务每个 `target` 至多这么长、加上其余字段的满载值仍小于它，由 hub 侧的满载不变式测试机械地守着，放宽任一上限都要先看还剩多少余量。超限的任务 agent 直接丢弃并回报 `error`。目的是 hub 失守时，攻击者无法把全部节点变成扫描器或流量反射器。

速率上限约束的是量，目标地址另由 agent 的本地策略约束（§5.7）。agent 在解析之后、发包之前检查实际要连的地址，默认拒绝本机、链路本地（含云厂商的 metadata 地址）、组播与广播：`0.0.0.0/8`、`127.0.0.0/8`、`169.254.0.0/16`、`224.0.0.0/4`、`255.255.255.255/32`、`::/128`、`::1/128`、`fe80::/10`、`ff00::/8`，以及落在私网段里的两个 metadata 地址 `100.100.100.200/32`（阿里云）与 `fd00:ec2::254/128`（AWS Nitro 的 IPv6 端点）。宿主机自己接口上的地址每次探测前重新枚举（地址随 DHCP、网卡增删而变），固定集之外的每一个按满长前缀并入默认拒绝：连本机的非回环地址走本地路由，常能绕过只挡外部入站的防火墙，与回环同理；已落在固定集里的（lo 上的 `127.0.0.1`、链路本地地址）仍由固定前缀管辖，否则 `probe_allow` 写 `127.0.0.0/8` 放不行回环。枚举失败时拒绝这次探测。私网默认允许，因为内网互测是常见用法；节点探测自己的地址（例如分配给全部节点、目标是某个节点地址的任务）在该节点上回报 `error`。宿主机可以用配置里的 `probe_deny` 追加拒绝、用 `probe_allow` 放行，两者都是规范形式的 CIDR 列表（`netip.Prefix` 已按掩码归一，写成 `10.1.2.3/8` 这类非规范形式是配置错误）；IPv4 映射段（`::ffff:0:0/96` 之内）的前缀也是配置错误，报错给出等价的 IPv4 写法：目标先还原成 IPv4 再比较，这样的前缀永远匹配不到，写在 `probe_deny` 里就是静默放行。固定默认集之外的宿主机接口地址只有写出同一个满长前缀才放行，更短的放行前缀（如整个私网段）不覆盖它；落在固定集里的本机地址（回环、链路本地）随固定前缀，写出与之等长或更长的放行前缀即可。裁决取最长前缀匹配；前缀等长时本地配置优先于默认值；同一前缀同时出现在两个列表里是配置错误，`run` 与 `configure` 都拒绝。IPv4 映射的 IPv6 地址先还原成 IPv4 再判断；其余内嵌 IPv4 的 IPv6 形态（IPv4 兼容 `::a.b.c.d`、NAT64 `64:ff9b::/96`、6to4 `2002::/16`）按 IPv6 地址判断，它们到达的是转换器或隧道，Linux 上是否会被送往本机未实测。被拒的任务照常调度，每次回报 `error`，写明地址与命中的前缀，hub 面板据此显示原因。名字解析出的首选地址被拒时不换用其他地址，否则攻击者可以借 DNS 挑选。检查放在 TCP 与 ICMP 共用的解析入口里，今后新增的探测种类经过同一个入口。

## 9. 告警

### 9.1 规则

- 流量：按节点周期配额和口径计算已提交用量，百分比达到 `threshold` 即触发，阈值范围 (0, 100]。只允许携带阈值，不接受任务、指标、持续分钟、提前天数或资源专用字段；多级提醒由多条规则表达。

- 离线：节点超过宽限期未上报。宽限期按节点可配，下限为 TTL（§4.4），由保存规则时的显式校验承载：宽限期短于 TTL 会在面板仍显示该节点在线时发出离线告警，两处读的是同一个 `last_seen`，口径必须同向。
- 探测：某任务在某节点上的丢包率或平均 rtt 连续 N 分钟超过阈值。数据源为 `probe_1m`。
- 资源：节点的资源指标连续 N 分钟达到触发阈值。数据源为 `metric_1m` 的分钟均值：内存使用率、磁盘使用率、CPU 占比（`cpu_sum / cpu_n`）、按核负载（`load1_per_core_sum / load1_per_core_n`）、网卡收发速率（`net_rx_bps_sum / net_rx_bps_n`、`net_tx_bps_sum / net_tx_bps_n`）。触发阈值范围按指标：百分比 (0,100]，按核负载 (0,64]，速率 (0,2^40] bytes/s（面板以 Mbps 输入，保存前换算成 bytes/s）。按核负载用 agent 采样时算好的商，不再用 `node_facts.cpu_cores` 去除：64 核与 2 核机器上同一个原始 load1 含义不同，归一发生在采样当时，分母后来变化不会改写已经入库的分钟。旧 agent 没有这一列时该分钟无读数。
- 到期：节点的到期日距今不超过 `days_before` 天（1–365，含已过期的负数）。数据源为 `node` 的到期日（§9.4）；没有到期日的节点不参与。
- 证书到期：某任务在某节点上观测到的服务端证书到期日距今不超过 `days_before` 天（1–365，含已过期）。数据源为 `probe_cert`（§8.3）的最新一份观测；没有观测行的（节点, 任务）即无读数，不参与评估、也不恢复（已有状态原样保留，行随任务或节点删除时按候选集撤销规则清除）。规则必须携带 `task_id`，且任务必须是 target 为 `https://` 的 HTTP 任务——只有它能带回证书观测；保存规则在事务内核验，把被引用的任务改成非 `https://` 或删除它同样被事务内守卫拒绝。评估与到期规则同一次扫描（§9.2 的五处时机），另在 ingest 写入的观测使 `not_after` 发生变化时立即评估一次——续期（证书更换）后不必等到日界才恢复。
- 规则的探测专用字段里，任务只允许探测与证书到期规则携带，指标只允许探测规则携带，阈值允许探测、资源与流量规则携带，持续分钟只允许探测与资源规则携带；`days_before` 只允许到期与证书到期规则携带；资源专用字段（资源指标、恢复阈值）只允许资源规则携带。越界组合由 `CheckRule` 显式拒绝（`InvalidArgument`）；保存入口与 Load 路径共用它。此前离线规则带探测字段会被存储层静默清零，这条检查随到期规则一起补上——静默清零是放宽方向，调用方发了什么、存下的却是零值，无从察觉。

### 9.2 状态机

流量规则没有 pending：当前周期百分比 ≥ 阈值直接 firing，低于阈值或配额清零即恢复，没有账本条目按有效零用量处理。事件 value 是百分比；触发文案包含已用 / 配额、口径和规则名；恢复只陈述“已清除流量配额”或实际百分比“已低于阈值”，不推断“周期已重置”。维护与静默经共用 apply 处理，触发和恢复配对。

流量告警只读账本 committed 状态：Load 装入值、Flush 成功写出的快照、Adjust 成功与 Commit 成功是四个发布点，写失败不更新。Committed 原样读，不滚动；引擎仅在 `period_start ≤ now < NextResetAfter(period_start, 当前重置日)` 时转换状态，过期或墙钟回拨则保持现状。实时 API 仍读 View，告警事件与面板可能暂时不同。入账到提交的 10 秒刷出周期，加提交到评估的 10 秒巡检周期，健康时约 20 秒；数据库错误或耗时期间没有上界。

流量评估时机：离线巡检同轮（共用一次 ListMonitoringNodes 快照，不逐节点读库）；AdjustTraffic 写库成功后；UpdateNode / ExecuteChange 改配额、口径或重置日后先 Commit，成功才评估；保存流量规则后全量；hub 启动在 Book.Load 后全量。Commit 与 Flush/Adjust 共用写锁，锁内不回调引擎。即时评估也用 ListMonitoringNodes，不按调用者凭据过滤；单节点评估只更新该节点，完整扫描和 UpdateScope 才裁剪离开作用域的状态。触发所据用量和周期清零均先提交后生成事件，重启恢复相同观测，不靠丢失的内存增量制造触发/恢复配对。

资源规则使用 `metric_1m` 的分钟均值（各指标的列与范围见 §9.1）。比例在同一次原始采样中计算后再聚合，不以已用量均值除以另一时刻容量。按核负载在该分钟没有 `load1_per_core` 采样（旧 agent，或采样时分母缺失）时视为缺失读数——既不触发也不恢复，不退回原始 load1，也不再查 Facts 的核数。恢复阈值在 [0,触发阈值)，连续 1–60 个完整分钟达到触发阈值才触发；连续相同长度窗口不高于恢复阈值才恢复。滞回区间和缺失读数均不能令 firing 恢复。分钟落库之后统一评估探测与资源规则。

每（规则 × 节点）一个状态：`ok → pending → firing → ok`，进入 `firing` 发告警通知，回到 `ok` 发恢复通知。状态持久化在 `alert_state`，hub 重启不会重复触发，也不会忘记尚未恢复的告警。

离线规则每 10 秒巡检；探测规则在分钟桶刷出后评估。离线的恢复条件是收到一次上报（上报本身即证明）；探测的恢复条件是连续 1 分钟低于阈值。离线的 `pending` 是"未上报已超过 TTL 但未到宽限期"（面板已显示离线、告警尚未发出）；探测的 `pending` 是最近一分钟超阈但尚未连续 N 分钟。探测规则在某分钟没有数据时保持当前状态：缺数据既不是超阈也不是恢复。

离线告警的抖动抑制：一条规则×节点从超过宽限期的离线恢复后，在一小时的窗口内再次离线，这次离线的宽限取 `max(节点宽限, 30 分钟)` 才进入 `firing`。两个数是常量，不按节点或规则配：抖动宽限的作用是压噪声，与节点的正常宽限是两个量。窗口按这次离线开始的时刻判定——离线开始即最后一次上报的时刻，跨重启取落库的最后上报时刻——同一次离线在整个 `pending` 期间用同一个宽限，hub 重启也不换。窗口严格大于抖动宽限是取值约束：状态机不比较两者（宽限按离线开始时刻一次定下，之后不再看窗口），判定不读两者的大小关系，改动其中一个数带来的变化只来自被改的那个数本身；改反只会让"窗口内再掉"与"抖动宽限"两个量失去各自的含义；只有节点没有任何上报落过库、离线开始退回启动时刻的那条路径上，"恢复后立刻再掉的抖动宽限不被重启打断"才靠这条关系保证。抖动判定只看这条规则×节点自己的历史：上次从 `firing` 恢复的时刻记在 `alert_state.recovered_at`（一段历史而非当前状态的属性：恢复转换写当下时刻，其余转换沿用现值，否则再次离线的 `pending` 一写就把窗口抹掉；非离线种类恒为 NULL），hub 重启不丢窗口。恢复通知照发——它是 `firing` 的配对事件，省略会让面板"已通知"与实际不对；`pending` 与面板显示不受影响，面板仍按 TTL 显示离线，抑制只推迟 `firing`；告警规则页的状态列把正因抖动宽限而推迟的 `pending` 标为"抖动中"（不落库，hub 重启后第一轮巡检之前不标）。探测规则不做同类抑制：`for_minutes` 已承担同样作用。

到期规则没有 `pending`：它是日历事件，不存在"持续多久才算"。剩余天数 ≤ `days_before` 直接 `firing`，续期把日期推出窗口或清空到期日即 `ok` 并发恢复通知；节点没有到期日按恢复处理。评估时机五处：hub 启动一次；每个 hub 时区的零点一次（按时区算出下一个日界再定时；零点不存在的日子——夏令时从零点开始的时区——下一个日界是新的一天的第一个时刻（01:00，新偏移）；`time.Date` 对不存在的零点的归一方向随 UTC 偏移的正负而异：偏移为负（America/Santiago、America/Havana）往回到前一天 23:00、本地日期没变，拿它定时会在旧的一天里空转，所以按本地日期是否已变判断，没变就取该时刻所在时段的结束处；偏移为正（Africa/Cairo、Asia/Beirut）往前到新一天 01:00，本身就是新一天的第一个时刻；hub 停机跨过多个零点由启动那次补上）；`CreateNode` 带计费字段建节点与 `UpdateNode` 改任一计费字段都立刻一次——新建即过期的节点不等到零点才触发，续费后不等到零点才恢复；保存到期规则（新建、启用、改 `days_before` 或作用域）后立刻一次——否则新规则要等到零点才有状态。日界循环里的扫描（含启动那次）出错（续期写回或状态写失败）时不等到下一个日界：按 1 分钟起、每次翻倍、上限 1 小时的退避重扫，成功即回到日界节奏；退避未到而日界先到就按日界扫。否则零点一次写库失败会让开着自动续期的节点整天显示"已过期"，而离线巡检与投递（§9.3）都有各自的重试节奏。同一次扫描先做自动续期的推后再评估规则（§9.4），推后与恢复在一次扫描里完成。一条规则对一个节点只提醒一次：进入窗口时按当时的状态写 `节点 X 将于 2026-10-01 到期（剩 4 天，规则 R）` 或 `节点 X 已于 2026-09-20 到期（已过期 7 天，规则 R）`，从"将于"走到"已于"不再发第二条（`days_before` 最小为 1，没有"恰在当天"的写法；到期当天写"剩 0 天"）。恢复文案按离开窗口的原因：到期日改了写 `节点 X 到期日已更新为 2026-11-01（规则 R）`，清空写 `节点 X 已清除到期日（规则 R）`，日期没变写 `节点 X 已不在提醒窗口内（规则 R）`（通常是规则的提前天数调小了；换时区或墙钟回拨也会）。"日期没变"与进入 `firing` 时记在 `alert_state` 里的到期日比：告警事件按保留期删除，而已过期的节点可以一直 `firing`，只有状态表能可靠地带着这个日期。事件的 `value` 是剩余天数（清空到期日而恢复时为 0）。`days_before` 与阈值、持续分钟同类，不是规则身份：改它保留状态，下一次扫描按新值判断。库里读不懂的到期日跳过评估、保留状态、不推后、`days_left` 缺失并记一行 Warn。扫描读快照之后才提交的 `UpdateNode` 不在本次快照里，本次可能按旧值多发一对触发与恢复，那次 `UpdateNode` 自己再扫描即收敛；续期写回是条件更新（只在库里的值仍等于计算所依据的值时写），不会盖掉快照之后的修改。

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

五项收在 `Billing` 子消息里（`types.proto`），`Node`、`CreateNodeRequest` 与 `UpdateNodeRequest` 都以它承载；`days_left` 也在其中，保存请求里的值忽略。校验由 `CreateNode` 与 `UpdateNode` 共用一处裁决（`billingOf`），与 `traffic_reset_day`、`offline_grace_s` 同一个函数、同一格式的错误文案；另加"自动续期开着时周期与到期日都必须非空"。`CreateNode` 的 `billing` 缺失等于不填，`UpdateNode` 是整体替换语义（缺省值即清除）：`billing` 缺失等于五项全清。

日期按天计，天的边界用 `--timezone`（§5.4，与流量周期同一个时区）。剩余天数 = 到期日 − 今天，由 hub 算好作为 `days_left` 下发（`Node` 与 `PublicNode` 都带；无到期日时缺失；负数即已过期天数），面板与公开页只显示它，不在浏览器里用本地时区再算一次——跨时区的访客会看到差一天的数字。

自动续期：扫描时对开着自动续期且到期日早于今天的节点，`while 到期日 < 今天: 到期日 += 周期月数`（1/3/6/12/24/36），落库并记一行日志。日号超过目标月天数时钳到月末（1 月 31 日 + 1 月 = 2 月 28/29 日），钳过之后日号停在钳后的值不再回到 31：接受这个漂移，它是提醒日期不是账单日期。周期为空的节点不推后（保存入口已拒绝这种组合，扫描里再守一次，不依赖入口）。

公开：`PublicNode` 带 `PublicBilling`，由 `Billing` 按 §10 的投影规则生成（投影为此扩展到枚举字段：两侧必须引用同一个枚举类型），价格、币种、周期、到期日与 `days_left` 同号放行，自动续期是运维开关，号与名都 reserved——对不齐在构造期就 panic，与 `PublicFacts`、`PublicMetrics` 同一机制。Agent 侧协议不涉及这些字段。

### 9.5 维护静默

计划内的停机（重启、升级、搬迁）不该惊动通知渠道：维护期间的告警事件照常落库（事实是审计），但不产生任何投递。两个机制，同一个抑制点：

- 节点维护开关：`node.maintenance`，随 `UpdateNode` 整体替换（§9.4 同一语义：缺席即清除），逐台机器的长期状态；面板节点编辑里有开关，节点列表与公开页节点卡片标"维护中"——维护不是离线，`online` 照实显示，让访客分得清"机器活着但在维护"与"真的掉了"。`CreateNode` 不接受维护字段：新建的机器没有"维护中"的合法初始状态，要开维护先建再改。
- 维护静默（`silence`）：带窗口的作用域集合，名称、启用开关、原因（选填，至多 256 字符）与创建时刻随主行落库。作用域与 `alert_rule` 同一形状、同一不变式：`all_nodes` / 显式 `silence_node` 行 / 动态标签选择器 `silence_tag` 三者互斥，`all_nodes` 为真不落显式行，选择器在读时展开为当前交集、不落展开行；标签变更在与单节点编辑相同的事务路径里重算静默覆盖并发布内存快照，被静默引用的标签与任务、规则的引用并列成为 `DeleteTag` 拒绝的第三种来源。窗口两种，互斥由保存入口校验（携带另一种类的字段即 `InvalidArgument` 点名字段）：每日重复（`daily`，`start_hhmm`–`end_hhmm`，按 hub 的 `--timezone` 取墙钟，允许跨午夜如 22:00–06:00，开始含、结束不含；开始等于结束拒绝——它既不表示"永不"也不表示"全天"，是两个合法意图中无法区分的那一个）与一次性（`once`，Unix 秒 `[from_at, until_at)` 闭开区间，`from_at < until_at`）。

抑制只发生在事件生成处，状态机本身不变（§9.2 的转换、`pending`、抖动抑制、`recovered_at` 全部照常）：转换进入 `firing` 时若节点处于维护开关打开或任一启用静默的生效窗口内，事件记 `alert_event.silenced=1`、状态行记 `alert_state.fired_silenced=1`，不产生 `alert_delivery` 行、不入投递队列；恢复事件是否投递只看配对的触发是否投递过——`fired_silenced=1` 的恢复同样记 `silenced=1` 不投递，`fired_silenced=0` 的恢复即使此刻静默正在生效也照常投递，恢复事件的 `silenced` 抄配对触发的值。由此静默在 `firing` 中途结束不补发触发通知（状态没有转换就没有事件），`recovered_at` 照常写、面板"已通知"与实际一致。日历类规则（§9.4 的到期与 §9.1 的证书到期：日历事件，提醒窗口本身就是它的意义，静默窗口压不住每天都在走近的日历）与系统事件（登录、备份，§9.3）永远不被静默；节点删除时其静默显式作用域行随之消失（`silence_node` 在"随节点一起消失的表"清单里）。

到期的一次性静默保留供审计，随告警事件的保留期（§6.5，`--retention-alert-events` 默认 90 天）按同一截止点清理；每日重复的永不清理。RPC：`ListSilences`（`ACCESS_READ`，每条带 hub 算好的 `active`——面板不按时区自己重算窗口，与 `days_left` 同一口径）、`SaveSilence`、`DeleteSilence`（仅会话）。界面另有两处只读标注：告警规则页的状态列把 `fired_silenced` 的 firing 标"已静默"（随 `AlertStateEntry.silenced` 下发，与"抖动中"同为 hub 算好、面板只标出），告警事件列表把 `silenced=1` 的事件显示为"已静默（未投递）"而不是"未配置渠道"——两者都没有投递行，原因是不同的。

### 9.6 hub 心跳外推

监控监控者：hub 按周期向一个外部监控服务（Healthchecks、Uptime Kuma、BetterStack 一类的 ping 地址）发一次请求，hub 自己死掉时对方收不到心跳即告警。默认关闭，目标地址只来自设置，出站复用 §9.3 的边界——同一个 `outbound` 客户端、不跟随重定向（3xx 当失败）、应答体只读前 64 KiB——与告警投递共用一套"请求与应答都有小上界"的约定。发到外部的只有聚合计数与版本号，不含任何节点名、地址或别的内容：POST 的 JSON 恰好是 `nodes_total`、`online`、`offline`、`maintenance`、`firing`（`maintenance` 取 §9.5 的节点维护开关，`firing` 取 §9.2 状态表里 `StateFiring` 的行数）与 `hub_version`；GET 与 HEAD 不带正文。计数与在线判定走 §4.4 的 `live` 口径，不在心跳一侧重算。

设置：`Settings` 字段 13 `optional Heartbeat heartbeat`，presence 组语义与 §10 总闸、§5.3 登录通知渠道相同——`UpdateSettings` 缺席即不变，给出即整体替换这一组，一次请求至少给出一组（各组清单加上这一组）。`url` 只写不读：`GetSettings` 回显时**恒为空**（ping 地址本身即密钥，与 webhook URL 同一做法），写入时必须是绝对 http(s)、主机非空、不超过 2048 字节；空串表示清空并停用；地址清空后循环不再发送，改动不需要重启。`interval_s` 为 60–3600 秒，越界 `InvalidArgument` 点名字段与范围，0 不是"取默认"，库里没有键时默认 60。`method` 必须是 GET、POST 或 HEAD，`UNSPECIFIED` 拒绝、不取默认，库里没有键时默认 POST。`has_url`（按库中 url 非空计算）与 `url_host`（`url.Parse` 所得主机，不含路径与查询串）只在响应里有意义。存储键 `heartbeat.url`、`heartbeat.interval_s`、`heartbeat.method`。

循环在 `cmd/hub/serve.go` 装配（与其余后台循环同一处），每轮开头重读设置：改间隔下一轮生效，清地址立即停发，填上地址一分钟内生效（停用态按 60 秒轮询设置，不按外呼间隔；停用态 `next_at` 置零）。每轮先读设置与计数，再发一次请求并记结果；读库失败跳过这一轮，不算成功也不算失败（内部错误不在三个类别里）。失败在产生处归类：地址非法/非绝对或方法非法走 `request`，`client.Do` 失败走 `transport`，非 2xx（含 3xx）走 `http_status` 并记状态码。状态只在内存里：上次成功时刻、上次失败时刻与类别/状态码、下次外呼时刻；`GetHeartbeatStatus`（`ACCESS_READ`，API token 与 §5.6 的只读方法同样放行）读这份状态，`enabled` 由库中 url 非空决定。连续同一失败只记一行日志，成功不清除"上次失败"；重启后状态归零，面板显示"从未跑过"。面板在设置页给出这一组的表单与状态（写侧不回显地址，已配置时留空保存会清掉地址，因此表单在地址留空时禁用保存、另给停用按钮），回显只给主机名。

## 10. 前端与公开页

管理端历史显示上报覆盖率，公开页不显示。coverage_start 缺席显示“尚无覆盖记录”；有起点但 observed_minutes=0 显示“无可观测区间”；否则显示 observed_reported_minutes/observed_minutes 和未知分钟数，并说明这不是在线率。三个状态直接使用响应字段，不从三个零猜原因。两个 API 均返回覆盖字段，公开节点的 observed 暴露 hub 在保留期内的观测分钟。

- `web/` 内两个 Vite 入口：`/admin/*` 管理面板，`/` 公开页，各自打包。测试扫描公开入口的 import、构建产物按描述符前缀核对，禁止公开页引用 `AdminService` 的生成客户端；这是卫生措施，安全边界在 §3.2 的服务端挂载。
- 公开页的登录入口：主站 `PublicSite.admin_path` 是 `/admin/`，内置公开页据此显示管理入口。主题 SDK 的可信容器在返回 `GetSite` 数据前移除该字段；主题可读取公开外观，但不获得管理接口或管理员凭据。
- 实时数据用轮询（默认 2 秒）。`PublicService.GetSnapshot` 一次返回全部公开节点的实时状态，hub 对序列化结果缓存 1 秒：匿名访客数量不影响 hub 的序列化开销。
- 历史与探测对比窗口只由轮询响应的 hub `now` 推出，右端取 hub 当前分钟的结束时刻，按 hub 分钟前进；不读浏览器墙钟，同一分钟的公开 GET 窗口相同。只有轮询查询在页面回前台时立即补取，历史与站点设置不因聚焦重复请求。
- 无副作用调用有 30 秒客户端等待预算：共用传输拦截器读取 proto 方法描述符的 `NO_SIDE_EFFECTS` 或管理端 `ACCESS_READ` 声明，不按名称猜测；有副作用调用不加截止时间。预算通过 `Connect-Timeout-Ms` 传给 hub，客户端到点取消并以 `DeadlineExceeded` 失败，超过预算的合法慢读也会被取消，不承诺服务端执行有总耗时上界。查询最多重试两次、间隔 1 秒与 2 秒，连续挂住时在 `3 × 30 + 1 + 2 = 93` 秒后最终报错并进入现有横幅路径，随后轮询可恢复；此前提是页面在前台、在线且事件循环未冻结，后台/离线暂停不受此墙钟上界覆盖。对比图的直接分块调用不经查询重试，每次调用仍受 30 秒预算约束。
- `--public-dir <dir>` 用指定静态目录替代内置公开页，未命中文件时回落到该目录的 `index.html`。`/admin` 与 RPC 路径的路由优先级更高，替换目录无法遮蔽它们。文件访问经 `os.Root`，不可越出目录、不跟随指向目录外的符号链接。目录与面板同源：里面的脚本能读面板、也能带着来访管理员的会话调管理接口，所以只放与 hub 二进制同等可信的内容（flag 帮助写明）。
- 外观设置（明暗、主色、logo、标题、自定义 CSS）存于 `setting`，经 `PublicService.GetSite` 下发并以 CSS 变量应用。只接受 CSS，不接受 JS 或 HTML；需要改结构的人使用 `--public-dir`。
- 公开页总闸：`Settings.public_enabled`（`optional bool`，键 `site.public_enabled`，从未保存过时为开）。`UpdateSettings` 对外观字段是整体替换、缺席即内置值，对这一项（以及 §4.9 的国家查询开关与服务地址、§5.3 的登录通知渠道、§6.7 的备份通知渠道、§9.6 的心跳外推组）缺席表示不变：把公开页关掉是一次对外可见的中断，不知道这个字段的老客户端与脚本改个标题不得顺手把它关掉，所以缺席不能等于 false。由此 `UpdateSettings` 的请求按组判定，各组彼此独立：外观五项是一组，任一项非空即视为给出，整体替换并按整体校验（theme 必填，其余为空即清空）——proto3 的 string 没有 presence，分不开"没给"与"给了空串"，按任一项非空判定能让只带 title 不带 theme 的请求得到点名 theme 的错误，而不是被静默丢弃；总闸、国家查询两项、登录通知渠道、备份通知渠道、心跳外推组各自是一个 presence 组，给出即改、缺席即不变；一次请求至少给出一组，否则 InvalidArgument 点名各组——只改总闸或只改国家查询的脚本不必重发外观，这正是 presence 语义存在的理由。关闭时 `PublicService` 全部方法返回 `NotFound`——包括 `GetSite`，否则站点标题与 logo 仍会泄漏；`/` 与公开页的前端路由（按公开页静态服务同一条回落规则：`assets/` 之外的路径）都返回"公开页已关闭"的说明页——分享出去的节点页链接要能看出是站点关了而不是链接失效；`assets/` 下 404；说明页带内置页同一套安全头。内置公开页与 `--public-dir` 一样受总闸约束，`/admin` 与 RPC 路径不受影响；主题沙箱中 RPC 之外的整个静态面（主题文件与回落的内置页）同样受约束，关闸时不读库、不服务任何主题文件——主题文件与 `--public-dir` 同属替换公开页结构的一档，关闸的人期望整个公开面消失，主题自己的报错页不是"公开页已关闭"。总闸与节点的 `public` 是与的关系：关闭时逐节点设置原样保留，重新打开即恢复；只想把 hub 当内部工具的人不必逐个取消节点公开。拦截器读设置的内存副本，不查库；关闭后 1 秒内快照缓存仍可能命中，与节点改私有的语义相同；限流仍生效，关闭后的匿名请求照样计入令牌桶。
- 节点标签：一个节点可挂多个运维自定义的标签（`tag`、`node_tag` 两张表，节点与标签多对多；`Node.tags` 随 `ListNodes` 回显，`UpdateNode` 整体替换标签集合），面板的节点列表可按标签过滤。多选过滤取交集：只保留同时拥有所选全部标签的节点（`GROUP BY node_id HAVING COUNT(DISTINCT tag_id) = 所选标签数`）；空选择不过滤、返回全部（`untagged` 为真时改由它决定，见下）——空条件匹配一切，与"空即拒绝"方向相反，这一分支显式写出；过滤条件按折叠去重后超过 16 个（超过每节点上限的交集必然为空，同时也是 SQL `IN (…)` 参数个数的上界）或含不合法的名字，返回 `InvalidArgument` 而不是空结果——空结果会把写错的条件伪装成"没有这样的节点"。另可只列无标签节点（`ListNodesRequest.untagged`）：为真时只返回没有任何标签的节点，仍限于调用方可见的节点范围；与 `tags` 同时给出返回 `InvalidArgument`（"带有所选全部标签"与"没有标签"的交集必然为空，同上，不让空结果把写错的条件伪装成"没有这样的节点"）。`ListTags` 列出全部标签与各自的节点数（`ACCESS_READ`），`DeleteTag` 按名字（折叠后）定位：协议里标签的身份就是名字，`Node.tags`、`UpdateNode` 与过滤条件都只用名字，不再暴露一套 id。删除标签只解除关联，不影响节点。命名：去掉首尾空白后 1–64 个字符、不含控制字符、允许空格与大小写混写；同一名字按 Unicode 简单折叠大小写不敏感去重（`db` 与 `DB` 是同一个标签，沿用先建的写法），每节点至多 16 个。标签随公开节点公开：`PublicNode.tags` 是该节点的全部标签名（先建的写法，按折叠排序），快照只含 `public = 1` 的节点，私有节点的标签因此不出现；没有单独的“标签是否公开”开关，标签常写用途与归属（`db`、`客户A`），挂在公开节点上即对外可见，这是运维的显式取舍。公开页按标签过滤纯前端、不动协议与存储：标签选项照 `PublicSnapshot.tags` 渲染：它是快照里各公开节点标签的并集，由 hub 按折叠键（`name_fold`）排序、与 `ListTags` 同序，页面不自己汇总与排序——折叠规则只在 hub 一处实现，页面复刻它的顺序只能近似（按小写比较会把 `_x` 排到 `Ab` 前面，折叠键里 `A` < `_` < `a`）；选择集为空不过滤、显示全部（空条件匹配一切，这一分支显式写出）；默认单选，点一个标签只看它（再点它回到全部）；切到多选后默认取交集（同时满足，与面板一致），访客可改为并集（满足任一），交互见 Web 改版设计 §3.1；实际生效的选择集是所选与当前快照里仍存在的标签的交集，标签在轮询后消失不会留下看不见的过滤条件；有过滤时不带标签的节点不显示；顶部在线计数按过滤后的节点算。公开页另有纯前端的“只看在线”（只留四态为在线的节点，维护中不算）与卡片 / 列表视图共用的排序，与标签、地区过滤叠加，先过滤后排序；排序默认为面板的手动顺序，“到期”从早到晚，已过期的最前，没有到期日或到期日无法解析（`days_left` 缺失）的最后，到期相同的保持面板顺序——排序键取 hub 下发的 `days_left` 而不是到期日字符串：同一份快照里各节点的 `days_left` 出自同一个 today，按它排就是按到期日排，且浏览器不自己按本地日期重算。过滤把节点滤空时说明“没有符合筛选条件的节点”。探测、告警与维护静默支持动态标签交集，保存为独立关联表，与全部节点和显式节点互斥。节点标签改变时，在同一事务校验配额并取得新的任务和规则作用域，成功后发布内存快照；被选择器引用的标签禁止删除。面板还可按标签一次性批量选择显式节点，此后不随标签变化。标签把"机器叫什么"与"机器属于哪几类"拆开：只有名称子串搜索时，分类信息只能编进名称，一台机器只能属于一个维度、改归属要改名。
- 面板的节点页与总览页有搜索框：按名称、备注与主机名做子串过滤，大小写不敏感（Unicode 简单折叠），纯前端、不动协议与存储；空输入不过滤、显示全部（空条件匹配一切，这一分支显式写出，不让它从循环里自然掉出来）；搜索中禁用拖动排序——结果是子集，拖动无法表达全序。备注与主机名本来就在 `ListNodes` 里回显给会话与 API token，不构成新的暴露。标签落地后过滤器与搜索框并排取交集。
- 节点顺序：全序按 `(sort_order, id)` 升序，管理清单、总览与公开页同一顺序。`Node.position` 是节点在全部节点里按这一全序的名次（从 1 起），读时计算，不随标签过滤、搜索或调用方可见的节点范围变化；对只能看到部分节点的 API token，它透露了排在前面的节点数——`ReorderNodes` 之后 `sort_order` 本来就等于名次减一，这一信息此前已经可见。改顺序有两个写入口，都只接受会话（`ACCESS_SESSION`），都在一个写事务里读出当时的全序、把全部节点的 `sort_order` 重写为 0..N−1：`ReorderNodes` 只接受全部节点 id 的完整排列，承载拖动、方向键与置顶置底，只在未过滤时可用（过滤结果是子集，表达不了全序）；`MoveNodes` 把给定的一组节点按它们现有的先后整体移到第 `position` 位起的连续位置，其余节点相对顺序不变，请求里 id 的先后不影响结果——选中第 57、88、103 位的节点移到 10，三者占第 10–12 位，原第 10 位起的其余节点依次后移；被选节点原本排在目标位置之前的同样适用。`position` 取 1..N−k+1（N 为节点总数，k 为去重后的节点数），越界返回 `InvalidArgument` 并写明区间而不截断：截断会让写错的位置静默落到首尾；重复 id 去重；任一 id 不存在返回 `NotFound` 并给出该 id，整批不改。位置按事务里读到的全序计算，并发的增删与重排不会被一份过期的完整排列覆盖。面板序号列显示 `position`，过滤时也是全序名次，与 `MoveNodes` 同一编号；多选（含过滤后的结果）与单行的移动菜单都可"移动到…"。
- 管理面板的 API token 页：列表显示名称、创建时间、最后使用时间；创建后明文只显示一次；删除需确认；可下载技能文件（§5.6，面板上叫「Agent 技能文件（SKILL.md）」）；可复制油猴脚本（`web/src/assets/heron-quick-node.user.js` 随面板打包，复制时占位符换成本站 origin，创建带"创建节点"权限的 token 后另有一份预填明文的一键复制）——粘贴进脚本管理器后任意站点出现悬浮按钮，在 IDC 页面看着价格与到期经 `ExecuteChange` 一步建节点并给出安装命令。
- 注册窗口开启后，面板在 key 旁给出一行安装命令（curl 与 wget 各一条）。hub 地址取浏览器当前的 origin，并注明 agent 若经另一地址访问 hub 需替换；hub 为正式版本（`hub_version` 是带 `v` 前缀的合法 semver，与节点落后判定用同一个解析）时，脚本取自该版本的 release（`hub_version` 经 `GetSnapshotResponse.hub_version` 下发），该 release 里的 agent 安装脚本装的是这个 hub 版本绑定的 agent（§14.1），命令旁写明绑定的版本号（油猴脚本的结果面板同样写明，绑定版本取自同一个 `GetSnapshotResponse`）；开发构建取最新 release 的脚本，并提示将安装最新 release。origin 为 http 且主机不是 loopback IP 字面量时，命令带 `--insecure-http` 并注明它意味着 token 与指标明文传输（判定与 agent 的传输规则同一口径，§5.7）。面板旁注明安装命令的可信来源是 README 与 GitHub Release：面板由 hub 提供，hub 失守时这里的命令不可信。命令区域在 `hub_version` 到达之前不渲染。
- 安装命令区（注册窗口、添加节点与换发凭据的弹窗共用一个组件，油猴脚本的结果面板另有一份同样的拼法，测试逐字对照）有"国内主机"开关，每次打开默认关闭。打开后命令加 `--update-source hub`（§4.10），并给出首次安装用的 SSH 反代参数 `-t -R 127.0.0.1:<端口>:127.0.0.1:<端口> '<设 http_proxy、https_proxy、all_proxy 指向该端口>; exec $SHELL -l'`：运维在开着代理的电脑上把它接在 `ssh root@<主机>` 之后登录，在这个 shell 里执行安装命令。端口是运维本机代理的端口，默认 7897，按浏览器（油猴脚本按脚本管理器的存储）记住；它拼进可复制的 shell 命令，只接受不带前导零的十进制 1–65535，否则不出参数。开关只影响生成的命令，hub 不记录：节点实际的取产物来源由更新器上报（`UpdateStatus.source`）。OpenRC 主机不支持在线更新，安装脚本拒绝 `--update-source`，面板旁注明需删掉。
- 未构建前端时 hub 照常编译与启动，页面路径返回"前端未构建"的说明；`go build` 与 `go test` 不依赖 Node。

公开页与 `PublicService` 的细节：

- 内容：总览是节点卡片（名称、国家 / 地区徽章（§4.9）、IPv4 / IPv6 标记（只为探测确认有公网出口的地址族画，探测失败、不支持与还没探测都不画）、在线、系统与架构、CPU、内存、磁盘、网速、运行时长、本周期流量，以及填了才显示的费用与到期——费用行在价格或周期任一填了时显示，只填周期时显示“每月”这类周期字样；到期行带剩余天数，已过期标红），节点页是历史图表（静态信息卡同样带费用与到期两行）（指标与探测，时间范围选择与面板同一组件）。公开备注 `public_remark` 与私有备注 `note` 并列：站长写给访客的一行说明（如线路类型），面板节点编辑里填写，公开卡片与节点页按纯文本渲染（不加链接、不渲染换行，准入已拒换行），空串不显示；只随公开节点下发，私有节点的备注不经公开端出现。图表组件与面板共用;公开入口不得引用 `AdminService` 的生成代码,由测试扫描公开入口的 import 钉住,构建后再按描述符前缀对产物做一次性 grep 核对(不是常驻检查);依赖方向只禁止公开到管理,面板可以引用公开页的常量。配色用 `light-dark()` 加 `color-scheme`,站点设置的明暗经 `html[data-theme]` 压过系统设置,图表颜色由浏览器解析成 rgb 再交给 uPlot(canvas 不认 `light-dark()`);浏览器下限 Chrome 123、Firefox 120、Safari 17.5。公开入口的产物在 `internal/hub/web/dist-public`。
- 消息：`PublicSnapshot`（`now`、`report_interval_ms`、`nodes`、公开节点标签的并集 `tags`）；`PublicNode`（id、名称、在线、最近上报、排序、`PublicFacts`、`PublicMetrics`、`Traffic`、§4.9 的 `country`、公开节点的 `tags`（§10 标签一条）、§9.5 的 `maintenance`、公开备注 `public_remark`（只在节点公开时随白名单投影下发，字段登记见 §12 的允许列表），以及 §9.4 的 `PublicBilling`：价格、币种、周期、到期日与 `days_left`，自动续期 reserved）。`PublicFacts` 只有系统、架构、CPU 型号、核数、虚拟化，以及双栈出口里每个地址族的探测状态（`PublicNetworkInfo` → `PublicAddressDetection.state`）——不给主机名、内核版本、agent 版本、ICMP 可用性，也不给出口地址与探测时间；`PublicMetrics` 与 `Metrics` 同字段但没有 `boot_id`。两者沿用源消息的字段号，不公开的号连名带号 `reserved`——要公开 `hostname` 这类字段必须先删掉 `reserved` 行，是一个显式动作（buf 的兼容检查因此对 `public.proto` 豁免 `RESERVED_MESSAGE_NO_DELETE`：这个文件里的 `reserved` 只表示源字段尚未公开，带 `reserved` 的消息都必须是投影目标，由测试核对）；值由投影按字段名从源消息复制，字段集合由公开消息自己声明（`Metrics` 以后加字段不会自动公开），构造时逐字段核对名字、类型、基数与 presence，任一不符即 panic。消息字段不整条复制：公开侧声明自己的子消息（`PublicNetworkInfo`、`PublicAddressDetection`），投影按同一套规则逐层核对与复制，源子消息里不公开的字段（地址、探测时间）同样要在公开子消息里 `reserved`。`QueryMetrics` 与 `QueryProbes` 复用管理端的请求与响应类型（定义在 `query.proto`：`public.proto` 若 import `admin.proto`，protoc-gen-es 会让公开包带上 `AdminService` 的描述符）；`GetSite` 直接返回 `PublicSite`、`GetSnapshot` 直接返回 `PublicSnapshot`，第三方主题拿到的 JSON 顶层就是快照本身。把节点标为公开即公开它正在探测的目标：`ProbeSeries` 带任务的种类与目标，公开端只给当前分配给该节点的任务打标签（历史里出现、现已撤下的任务留空——它改成内网目标后从未被该节点探测过，不在公开范围内），管理端按任务当前配置标注、已删除的任务留空；面板图例也用序列自带的标签。缓存上界以 `cache_max_age_s` 方法选项写在 proto 里，与 `heron.v1.access` 同一口径：proto 是单一事实源，第三方主题在 proto 注释里就能看到。
- 限流：按来源键令牌桶，桶容量 60、每秒补充 10，超限 `ResourceExhausted`，与 `Register` 的限速同一实现（§5.2，`internal/hub/ratelimit`）。两处都是挂载点上的 HTTP 中间件而不是拦截器（`Register` 按路径恰为 `/heron.v1.AgentService/Register` 匹配，`Report` 不进桶；公开服务是整个挂载点都经过它）：解码先于拦截器，拦截器看不到解码失败的请求；公开服务还要包在快照缓存外面，缓存命中在 connect 处理器之前应答。只有这样每个请求（含解码失败的）都计数。来源键：IPv4 按单个地址，IPv6 按 /64（一台主机通常拥有整个 /64，逐地址计键等于不限流）；超限的 429 同样带 `no-store`。hub 在反向代理之后而没有配 `--trusted-proxies` 时，全部访客共用代理地址的一个桶（每个打开的总览页每 2 秒轮询一次即 0.5 次/秒，节点页另有每分钟两次历史查询与加载时的请求；补充 10 次/秒：总览页超过 20 个、节点页约 19 个起消耗持续多于补充，30 个页面时净流出约 5 次/秒、60 的桶约 10–12 秒耗尽后出现 429）——这是部署配置问题，写在 flag 帮助与 README 的反代一节，不改限流。
- 缓存：`GetSnapshot` 的序列化结果按编码缓存 1 秒，缓存的是响应字节而不是消息：只缓存规范形态的请求（GET 不带正文——connect 对带正文的 GET 回 415，缓存不得替它应答；POST 为规范的 connect unary），键是 {GET 或 POST, codec, 协商出的压缩}，其余形态直通 connect；协商压缩只读 `Accept-Encoding` 的第一行，与 connect 一致；节点改为私有后公开快照里最多还能看到它约 2 秒（hub 缓存 1 秒加下游 `max-age=1`）。公开页只在加载时取 `GetSite`，已打开的页面刷新后才看到外观改动，刷新时浏览器还可能再用最多 5 分钟的缓存。GET 响应的 `Cache-Control`：快照 `max-age=1`、历史查询 `max-age=60`、站点配置 `max-age=300`；失败响应带 `no-store`（节点改回公开后浏览器不会继续用缓存的 NotFound）；POST 响应不带缓存头。
- 设置：`setting` 表是键值表；`GetSettings` 为只读口径、`UpdateSettings` 仅会话。`Settings` 的字段号在此登记，各分支按表取号、不各自挑：1–5 外观，6 `public_enabled`（§10 总闸），7 `geo_enabled`、8 `geo_url`、9 `geo_backend`、10 `geo_mmdb_path`（§4.9），11 `backup`（§6.7），12 `login_notify`（§5.3），13 `heartbeat`（§9.6）。字段与上限：标题不超过 64 个字符；明暗为 `auto`、`light`、`dark` 之一；主色为 `#rrggbb`；logo 为 `data:` URL，图片类型限 png、jpeg、webp、svg，不超过 128 KiB；自定义 CSS 不超过 64 KiB，含 `</` 即拒绝（它能跳出注入点的 `<style>`）。校验错误写明字段、违反的约束与期望取值；任一项不合约束整次更新不写入。logo 只接受 `data:<type>;base64,<data>` 这一种写法（type 全小写、不带参数；data 逐字节核对标准 base64 字母表后 Strict 解码——宽松解析与浏览器解析一旦不一致，白名单就能被绕过）。CSS 不清洗、按字节原样存，只查字面 `</`（它本身不含字母，一条就覆盖全部大小写变体；CSS 转义与 HTML 实体在 `<style>` 的 RAWTEXT 里都不解码，不拒绝）。表结构 `setting(key TEXT PRIMARY KEY, value TEXT NOT NULL)`，不用 `WITHOUT ROWID`（值可达 128 KiB，超出 SQLite 对无 rowid 表的建议行大小）；键 `site.title`、`site.theme`、`site.accent_color`、`site.logo`、`site.custom_css`、`site.public_enabled` 是持久标识；从未保存过时明暗为 `auto`、总闸为开、其余为空串（空标题即内置标题）。标题有两道限：清洗前不超过 1024 字节，去掉控制字符与首尾空白后不超过 64 个字符。管理请求的解码预算分两类。设置一类由按字段登记的表推出（`internal/hub/api/settings_budget.go` 的 `settingsBudget`）：`Settings` 的每个叶字段登记它的最坏编码类型与上限参数，字节数与用例的边界样本由同一段类型规则推出，表里不写算好的数；按字节上限、取值集合或类型上限取界，边界样本不必通过业务校验。字符串按字节上限的 6 倍加两个引号（encoding/json 默认写法下任何字符编码后不超过其 UTF-8 字节数的 6 倍：控制字符与 HTML 转义的 `<`、`>`、`&` 单字节写成 6 字节，U+2028/2029 由 3 字节写成 6 字节，由逐码点用例钉住）；logo 的上界从 logo 校验的接受集（data URL 前缀集 × 标准 base64 字母表）推出——前缀取 JSON 编码最长者、余下字节按字母表里膨胀最大的字符计，当前等于上限加引号；放宽字母表或前缀集时上界随之变大，用例另钉住接受集不越出前缀集 × 字母表；明暗、主色与 `geo_backend` 按取值集合里最长的一个加引号，并逐值断言没有取值超过登记值；布尔按 `false` 计；uint32 按类型上限 10 位计；渠道列表（§5.3、§6.7）按至多 16 条、每条 22 字节计（正 int64 至多 19 位、两个引号、条间逗号），加方括号、减末项逗号；§4.9 本地库路径按 6 × 4096 计（hub 只回显自己启动参数里的路径，客户端可能把 `GetSettings` 的回显整份送回，合法回送不能被拒；任何能打开的路径不超过 Linux 的 PATH_MAX 4096）。其余字节由 descriptor 逐字段汇总：字段名（取 protojson 接受的原名与 JSON 名中编码后较长的那个）、冒号、对象内字段之间的逗号、各消息的大括号，以及 `{"settings":{}}` 骨架，不留语法余量。预算模型只支持标量、标量 repeated 与本包的单值 message，遇到其他形状（map、repeated message、oneof、外部 message）或未登记的叶字段，在包初始化时即失败；多余的 JSON 空白、对无需转义的字符的转义、冗余的数字写法都不在预算内。用例三向核对：descriptor 枚举出的叶字段集合与登记表双向相等，新字段未登记即红；每项等于其边界样本编码后的长度；按 descriptor 组装的完整边界请求长度恰好等于预算。另有一条用例把可保存的满额设置用 JSON 空白补到恰好预算字节数，必须被接受并保存，预算少一字节时同一请求得到 resource_exhausted。当前预算为 634648 字节，这是设置一类；主题包一类（§10.1）是 8 MiB 的 base64（4·⌈8388608/3⌉）加 4 KiB 语法 ＝ 11188908 字节；预算按过程分两类：`UploadTheme` 用主题包一类（约 10.7 MiB），其余过程用设置一类；主题包一类的用例从常量重算最坏请求并断言落在预算内——解码先于鉴权拦截器，所以匿名请求的读取上限在 `UploadTheme` 上是主题包一类、在其余过程上仍是设置一类，都有界（`Register` 在 `AgentService` 上，用 ingest 自己的 256 KiB 上限，不受影响）。`GetSite` 下发这五项，公开页以 CSS 变量应用，自定义 CSS 放在其后。
- 静态服务：内置公开页与面板用同一套 CSP；`--public-dir` 只加 `X-Content-Type-Options: nosniff` 与 `frame-ancestors 'none'`，不限制脚本与外部资源——目录由运维放置，严格 CSP 会让第三方主题的字体与图片失效。面板、内置公开页与 `--public-dir` 共用一个只服务普通文件的核心：目录、FIFO、设备一律当作不存在，路径任一段以 `.` 开头的名字也当作不存在（`.git/config`、`.env` 是运维放目录时最常见的泄漏；`.well-known/` 因此也不服务，ACME http-01 之类由反代完成），因此任何来源都不列目录、也不会在特殊文件上阻塞（打开带 `O_NONBLOCK`）；`assets/` 下未命中返回 404（`/admin/assets` 因此是 404 而不是重定向），其余回落 `index.html`；自定义目录一律 `no-cache`，每个请求重新 `os.OpenRoot`，目录被原子替换后下一个请求就读到新内容；启动时核对其 `index.html` 是普通文件，否则 `serve` 在打开数据库之前报错。`/` 就是公开页，不再重定向到 `/admin/`。未构建前端时 `/` 与面板一样返回"前端未构建"的说明。
- `GetStorageStats`（只读口径）返回库大小与各表行数，与 `heron-hub stats` 同一来源：表名取自 `sqlite_master` 而不是手写清单（手写清单曾漏掉三张表）；库大小是逻辑大小 `page_count × page_size`——WAL 下主文件大小滞后于内容，逻辑大小等于检查点之后的主文件大小。SQL 读数在独立只读连接的同一事务中计算，并发共用单份计算，成功完成后 60 秒内复用；`sql_observed_at` 是该事务开始时的 hub 墙钟 Unix 秒，缺席表示旧 hub，不能当作请求时刻（详见 §6）。CLI 先一行 `db_bytes: N`，再逐表 `name: rows` 按表名升序，之后是键带点号的读数（第一条为 `sql.observed_at: N`，其后为健康读数与 `wal.*`）：表行以外的读数键都带点号，按"键不带点号且值为整数"认表行的解析方（`scripts/e2e.sh`）才不会把它们当成表；WAL 文件仍逐调用观测。行数是聚合值，API token 可读：看到 `api_token`、`admin_session` 的行数不构成列出 token（§5.6 禁的是枚举与吊销其他 token）。

- 批量编辑节点标签：节点清单支持逐台选择和选择当前搜索、标签过滤结果；改变筛选条件清空选择，避免隐藏节点被误改。弹窗以打开时的所选节点快照显示每个标签的关联数量，全有为选中、全无为未选中、部分有为半选。半选标签可依次切换为全部添加、全部移除、保持原样；可输入新标签并向全部所选节点添加。保存仅提交显式增删项，不回传各节点原有标签全集，未修改的标签和其它节点字段保持不变。`BatchUpdateNodeTags` 的节点 id 必须为正且非空，重复 id 去重；增删列表按标签名规则校验、折叠去重且不可交叠。移除列表可超过 16 项，最终每节点仍至多 16 个标签。标签写入、探测配额校验、告警作用域裁剪、覆盖回读与版本推进在同一事务中完成；任一失败整批回滚，成功后按与单节点编辑相同的锁顺序发布内存覆盖。移除只解除所选节点的关联，不删除标签本身。

第三方主题是通过主题 SDK 读取公开数据的静态站点，框架自选；沙箱内不直接 `fetch PublicService`，SDK 由可信容器桥接固定的公开操作。

### 10.1 公开页主题的安装与同域托管

- 公开页在 `/`，后台仍在 `/admin/`。第三方主题不直接注入主页面：项目维护的可信容器通过 `sandbox="allow-scripts"` iframe 加载包内 HTML，不授予 `allow-same-origin`。包内 HTML、脚本、错误和缓存验证响应都带沙箱 CSP；直接打开资源不能恢复管理同源权限。主题不能读取父文档、cookie、管理存储，不能注册 Service Worker 或使用 WebAuthn。
- 数据通道只允许 `GetSite`、`GetSnapshot`、`QueryMetrics`、`QueryProbes`，父容器以不带凭据的请求读取公开接口；不提供任意 URL 代理。父容器核对 iframe 窗口与不透明来源，用 MessageChannel 绑定本次加载，限制消息大小、并发和频率，换页时关闭旧通道。主题 SDK 位于 `/_heron/theme-sdk.js`，导出 `getSite`、`getSnapshot`、`queryMetrics`、`queryProbes`、`navigate`、`onRoute`；路由只允许公开根页和节点页。预览复用同一隔离机制，以绑定管理员会话的短期能力访问未发布包。
- 安装入口为本地 ZIP 上传，或显式选择公开 GitHub 仓库/Release 的已发布 ZIP 资产。下载器只连接白名单 HTTPS 主机，逐跳验证重定向和解析地址，拒绝回环、私网、链路本地等目标，并限制时长和压缩体积。来源保存仓库、Release 和资产名称。GitHub 只用于安装，访问主题不依赖 GitHub；不接受源码压缩包、不执行构建、不自动跟随上游更新。
- 包根必须有 `index.html` 与 `theme.json`。清单包含 `id`、`name`、`version`、`sdk` 和可选 `preview`。`id` 匹配 `[a-z0-9-]{1,32}` 且不为 `builtin`，名称与版本最多 64 字符、不含控制字符；新安装要求 SDK 1。预览图只接受内容与扩展名匹配的 PNG/JPEG/WebP。未知字段、隐藏路径段、绝对路径、`..`、重复路径及链接、设备文件拒绝整包。只接受普通文件、目录和 store/deflate 压缩。
- 原包上限 8 MiB、条目 2000、单文件展开 16 MiB、总展开 64 MiB；读取前校验中央目录声明，读取后核对实际大小和 CRC。条目压缩字节合计不得超过包长，避免同一压缩片段被重复展开放大成本。上传仍受 30 秒 HTTP 读请求超时和单个安装容量约束；失败不留下部分版本。
- `theme(id)` 表示身份，`theme_version(theme_id,digest)` 表示不可变产物，摘要是原始 ZIP 的 SHA256；展示版本号不参与唯一性。最多 20 个主题，每主题 3 个版本，计数与插入在同一事务显式守卫。安装不激活、不覆盖旧版本，相同摘要重复安装返回原记录；缺包占位在补回原包时清理，不永久占用名额。`expect_id` 必须指向已安装主题且与包内 ID 一致。
- 全站 `theme_selection` 单行保存 current/previous 引用。启用时将原 current 记为 previous，重复启用不覆盖 previous；可回滚或切回内置页。单版本删除保护 current/previous，整主题卸载清除该主题的引用并可回落内置页。启用过的版本记 published，已打开页面的资源仍绑定原摘要；显式删除后不再服务。包与文件按 ID+摘要键读取，同一文档不混用版本。
- `--public-dir` 是运维自定义目录，给出时接管公开页并阻止后台启用第三方主题。旧 `--theme-origin` 删除，不再按 Host 分流。旧 SDK 包可解析归档、备份和恢复，但预览、启用、文件执行入口共同拒绝；原启用旧包回落内置页，不因迁移而取得新权限。
- 公开页总闸覆盖容器、全部主题资源与公开接口，不影响管理入口。包资源使用相对地址，Vite 的 `base` 为 `./`；主题通过 SDK 路由，不直接控制顶层历史。主题管理全部仅管理员会话。原包备份、恢复与启用引用见 §6.7，开发指南（SDK、数据字段、包格式与示例）见 `web/src/assets/heron-theme-skill.md`（面板「主题」页可下载），安装与迁移说明见 `docs/theme-guide.md`。

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
  - 从描述符枚举 `AdminService` 全部方法：每个都声明了 `heron.v1.access`；持有效 API token 逐个调用，`ACCESS_READ` 放行、其余 `PermissionDenied`；吊销后下一个请求即 `Unauthenticated`。两条凭据互不回退由交叉用例钉住：有效 cookie 加无效 bearer 被拒，有效 bearer 加无效 cookie 调只读方法放行，有效 cookie 加 `Authorization: Basic` 走会话路径放行。
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
- 技能文件（`proto/SKILL.md`）里标为示例的 shell 代码块由 e2e 用真实 hub 与 token 逐个执行，断言退出码为 0 且输出为合法 JSON：技能文件与接口漂移时 e2e 变红，而不是等 agent 调用失败才发现。
- 安装验收在真实启动的机器上跑（本地 OrbStack；验收脚本只创建与删除带自己前缀的机器）：一级发行版 Debian 12、Alpine 3.21 两个架构每次改动安装脚本或服务定义时跑，二级发行版发版前加跑。每格断言：以面板给出的管道形态（`curl … | sh -s -- …`）一条命令装好且节点上线——脚本来自 stdin 时，脚本里读 stdin 的命令会吞掉余下部分，文件形态测不出这一点；服务进程的 Uid 不为 0 且有效能力含 `CAP_NET_RAW`，facts 里 ICMP 可用且 ICMP 任务有结果（能力由 init 授予）；服务下上报的指标字段集合与内存总量与同机 root 手动运行一致，服务进程所见的根目录与 init 所见的在同一个文件系统上（按设备号判定）（加固项不得让采集缩水；根分区总量不按两次上报的数值比：OrbStack 机器的根是 btrfs，实测同一台机器相隔 5 秒的两次上报总量可相差约 2.4 GiB 而已用量相同；网卡只以合计计数器上报，逐网卡集合经接口观测不到，不作断言）；换一个版本重跑后节点数不变、版本为新版本；OpenRC 机器重启后服务自启；卸载（含 `--purge`）后服务、二进制、配置、日志目录、用户与组都不存在。下载目录经 `--base-url` 指向本地 `make release-full` 的产物。这类验收依赖真实 init，不进 CI；`install.sh` 的 shellcheck（POSIX 模式）进 CI，systemd 单元的 `systemd-analyze verify` 在验收机器上跑。
- hub 中转（§4.10）在真实启动的机器上验收（本地 OrbStack，`pia-relay-` 前缀）：hub 机器前用 Caddy 在非标准端口终止 TLS，agent 机器出站只放行到该端口、拒绝 80/443/8080/8443。开始前先在 agent 机器上确认访问 GitHub 失败——封锁没生效时，后面的正向结果证明不了产物是经 hub 取得的。测试二进制以构造参数注入测试公钥与假官方源，正式二进制只有常量公钥与固定地址。正向：`StartUpdate` 下发后节点经 hub 升级，运行文件与候选逐字节一致；反向：假官方源给出篡改产物时 hub 预验签失败、任务失败且面板可见原因；伪造的 hub 跳过验签直接给坏字节时，节点更新器拒绝安装、原程序不变。
- CI：`buf lint`、`buf breaking`（`WIRE_JSON`，对比主干）、`go vet`、`go test -count=1 ./...`。

## 13. 实现前需以实验确认的事项

下列都是对外部组件特定版本的行为断言，以实验结果为准，结论记入对应里程碑的计划：

1. darwin 采集在 `CGO_ENABLED=0` 下的可行实现（gopsutil 或 purego）：能构建，且在真机读出 CPU、内存、网卡计数器、`boot_id` 等价物。——已于 2026-09-26 在 Apple Silicon 真机实验确认，结论记在 `docs/superpowers/plans/2026-09-26-m6-macos-agent.md` 的"实验结论"节。摘要：网卡计数器经 `NET_RT_IFLIST2` 与 `getifaddrs` 对普通进程只给 32 位、按 KiB 取整的值，`net.link.generic.ifdata` 给完整 64 位值；聚合的 `HOST_CPU_LOAD_INFO` 在 Apple Silicon 上按秒取差不稳，要用逐 CPU 的 `host_processor_info`；Rosetta 下 `hw.pagesize` 为 4096 而页计数的单位是 `host_page_size`（16384）；`kern.bootsessionuuid` 跨睡眠唤醒不变、重启即变；`host_statistics64(HOST_VM_INFO64)` 给第三方调用者返回按 flavor 全局缓存、约每秒刷新一次的快照而 Apple 平台二进制每次调用拿新值，内存页计数不能与 `vm_stat` 做同一时刻数值对照（2026-10-04 补测，macOS 26.3.1 / kernel 25.3.0）。
2. 非特权数据报 ICMP 在目标 Linux 发行版（含容器环境）与 macOS 上的可用性，以及 `CAP_NET_RAW` 回退路径。——已于 2026-09-23 实验确认，结论记在 `docs/superpowers/plans/2026-09-23-m3-probe-backend.md` 的"实验结论"节：macOS 非 root 可用数据报 socket；Linux 只受 `net.ipv4.ping_group_range` 管（Docker 默认放开，内核默认关闭，Debian 12 的 systemd 包不放开），raw 需有效 `CAP_NET_RAW`；回包匹配不能依赖 ICMP ID。2026-09-25 在 OrbStack 提供的 Alpine 3.21、Debian 12、Ubuntu 24.04、Rocky Linux 9 机器镜像（真实启动，amd64 与 arm64）上复现并补充：开机时 `ping_group_range` 只有 Rocky 9 放开（systemd 上游的 50-default.conf），另外三个关闭——Alpine 的 00-alpine.conf 写了 `999 59999`，但该镜像的 sysctl 服务不在默认 runlevel，文件没有被应用；Alpine 官方安装介质的默认 runlevel 未验证。由此：运行用户的组不在 `ping_group_range` 内时，要靠授予 `CAP_NET_RAW`（§14）才有 ICMP；显式把该组放进范围同样能走数据报路径，二者任一即可。
3. `modernc.org/sqlite` 在约 500 万行规模下的上卷查询、窗口查询与分块 prune 耗时；带对照组（同一数据、同一查询、空闲与并发写入两种条件）。方法与量级参照：monitor v1.3.2（其 #84）把小时汇总的清理从按天改为按节点一条语句后，29 个节点从 27.7 s 降到 1.2 s，每条语句持锁约 24 ms。
4. 经反代（HTTP/2 到反代）时单次上报的线上字节数，用于判断 §4.4 由 TTL 反推出的上报间隔在目标规模下的成本是否可接受。结论若为不可接受，要动的是 TTL 这个产品指标或消息体积，不是把间隔调长而默许 TTL 跟着漂。
5. 含 connect 与 protobuf runtime 的 agent 二进制体积与常驻内存。
6. 500 节点规模下的库文件大小。
7. connect-go 处理器对 `application/json`、`application/proto` 之外的 `Content-Type`，以及对未标为无副作用的方法的 GET 请求的实际响应（§5.3 的前提）。
8. 容器 / LXC 与 OrbStack 机器里 cgroup CPU 限额文件的位置与形状。——已于 2026-10-04 在本机实验确认（kernel 7.0.14-orbstack，Docker 容器与 OrbStack 机器均为 cgroup v2 unified：`/proc/1/cgroup` 是 `0::/…`、`/sys/fs/cgroup` 是 cgroup2fs；本机没有 v1 环境，v1 只有夹具覆盖）：限额落在 `cpu.max`（`--cpus=2` → `200000 100000`）与 `cpuset.cpus.effective`（`--cpuset-cpus=0-1` → `0-1`），两者同给各写各的文件，有效核数取较小者；`nproc` 反映 cpuset 不反映 quota；`/proc/stat` 与 `/proc/cpuinfo` 是宿主全机同一份计数（同一时刻 OrbStack 机器与容器里读数一致）；`cpu.stat` 的 `usage_usec` 是本 cgroup 的累计用量，两次采样差分除以（有效核数 × 区间）算得出占比——限 2 核跑满一核 3 秒，`/proc/stat` 口径约 7%，cgroup 口径 50%。v1 侧未实验（本机没有 v1 环境），按内核源码（`cpu_legacy_files`）判断：`cpu/cpu.cfs_quota_us` 不像 v2 的 `cpu.max` 有"只在非 root cgroup 上提供"的属性，v1 宿主 root 的 cpu 控制器上同样存在（值 -1），所以裸机 v1 宿主也会落入 v1 回退路径、启动时记一行日志。
9. SQLite WAL 在持续读者下的行为，以及 checkpoint 能否不阻塞写入地完成（modernc.org/sqlite v1.59.0，即 SQLite 3.53.4）。——已于 2026-10-06 在本机实验确认（Apple M4 Max、macOS 26.3.1；100 个节点、3 天历史的库，每秒一批分钟行写入，三种条件各 15.5 分钟）：只有写入时，自动 checkpoint 反复重置 `-wal`，文件停在 9,616,112 字节的平台；有 8 个不停顿循环的短查询读者，或一个循环持 60 秒只读事务的长读者时，`-wal` 以同一斜率线性增长、从不重置（该写入负载下约 87 MiB/分钟，15.5 分钟累积到约 1.31 GiB：两种条件分别为 1,411,606,792 与 1,411,623,272 字节）——自动 checkpoint 仍在回填，被挡住的是重置；手动 PASSIVE 同样只回填不重置；TRUNCATE 在短查询读者下成功（约 0.86 s），在长读者持着快照时等满 `busy_timeout`（5 s）后返回 busy；大规模 prune 之后 `-wal` 停在高水位，不会自行缩小；正常 Close 时驱动做 checkpoint 并删除 `-wal` 与 `-shm`。生产 hub 在 v0.6.1 启动后约 9 分钟与 19 分钟各采一次：`-wal` 两次都是 4,408,432 字节，主库从 8,425,472 字节涨到 8,482,816 字节。有节点在线时 hub 每分钟都写分钟行，十分钟的写入没有让 `-wal` 超出原有长度，新帧落在重置后从头复用的空间里：现有读负载下自动 checkpoint 能完成重置。hub 的所有连接都设 `journal_size_limit` 为 16 MiB（平时高水位约 4 MiB 的 4 倍，见 `store.walSizeLimit`）：迁移、大规模 prune 这类突发写入把 `-wal` 撑大之后，下一次重置时截回 16 MiB 以内，不再停在高水位；截断只发生在重置时，读者挡住重置的情形不受它约束，单次读数仍只是曾经到过的长度。是否需要 checkpoint 策略由这一项决定：经 `GetStorageStats` 的 WAL 文件观测跨时刻比较，长度持续增长、不停在平台时再定；策略须经写连接、在事务之外执行，并给出读快照推进不了时阻塞的上界。
10. LXC guest 里 lxcfs 接管的文件与它们的口径（Incus 6.0.4 + lxcfs 6.0.4，guest 为 Debian 12，内核 7.0.14-orbstack）。——已于 2026-10-06 在本机实验确认：lxcfs 默认接管 `/proc` 下 cpuinfo、diskstats、loadavg、meminfo、stat、swaps、uptime 七个文件与 `/sys/devices/system/cpu`（逐文件 bind，`fuse.lxcfs`），不接管 `/sys/fs/cgroup`，`boot_id` 每个容器独立；`/proc/stat` 是宿主的逐核计数按 guest 的 cpuset 过滤，不是 guest 自身的用量——guest 空闲、宿主跑一个单核忙循环时，绑在单核上的 guest 读到 47.7% 忙，而 guest 自身的 cgroup 用量只增加约 2 ms；`/proc/cpuinfo` 与 `/proc/stat` 的核数都是 cpuset 视图，"cpuset 少于 cpuinfo 核数"这条限额判据在 guest 里不再成立；Incus 的 `limits.cpu` 实现为 cpuset 绑核而不是 `cpu.max` 配额；`/proc/meminfo` 只在设了内存限额时按限额给出（MemTotal 等于限额），无限额时是宿主全机；`limits.memory` 同时把 `memory.swap.max` 设为 0；guest 里 `/proc/1/environ` 的 `container=lxc` 与 `/run/systemd/container` 都在，agent 报的 `virtualization` 是 lxc；cgroup `cpu.stat` 的用量只含 guest 自身。
11. Docker 容器在 cgroup v2 下的限额组合与内存口径（Docker 29.4.0，cgroupns 默认 private，内核 7.0.14-orbstack）。——已于 2026-10-06 在本机实验确认：cgroupns private 时挂载根就是容器自身的 cgroup，CPU 与内存的限额、用量文件全部存在，root 与非 root 都可读；`--cpus` 只写 `cpu.max`，`--cpuset-cpus` 只改 `cpuset.cpus.effective`，`--memory` 写 `memory.max` 并把 `memory.swap.max` 设为同值，三者独立、可叠加；cpuset 不过滤 `/proc/stat` 与 `/proc/cpuinfo`；`--memory` 的限额对 `/proc/meminfo` 不可见，agent 报的是宿主全机内存；容器里没有 `container=` 环境变量，也没有 `/run/systemd/container`，现有的执行环境标识全为空；`--cgroupns=host` 时挂载根是宿主的 cgroup 根，容器的限额从挂载根不可达，根上的 `cpu.stat`、`memory.stat` 读出 0 字节；cgroup 的 `memory.current`、`memory.stat` 只含容器自身的用量，宿主 `MemAvailable` 则受全机其他活动影响，同一次 200 MiB 分配实验里反向涨过 153 MiB。
12. 执行环境识别的判据（kernel 7.0.14-orbstack，Debian 12 容器与 OrbStack 机器，lxcfs 6.0.4 / Incus 6.0.4，2026-10-06 实验）。——已确认：① `cgroup.type` 只在非根 cgroup 上存在，真根（GitHub runner 与手工构造的共享挂载）上 stat 得 ENOENT，是挂载根层次的可靠判据；`cpu.stat` 在真根和控制器未下放的子 cgroup 上都存在，不能当环境判据。② 控制器未下放（特权容器建子 cgroup、不在 `cgroup.subtree_control` 打开 cpu 与 memory）时，子 cgroup 上 `cgroup.type` 与 `cpu.stat` 存在、`cpu.max` 与 `memory.current` 不存在——CPU 用量可读、配额按不限、内存按未知并记说明。③ `/proc` 文件来源按 `st_dev` 对 mountinfo 判：叠加挂载（新 proc 盖住 /proc）整体换设备号，单文件 bind（lxcfs 的做法）只换那一个文件，判据必须逐文件；来源不在 mountinfo 里或 fstype 不是 proc/fuse.lxcfs 一律按未知。④ 部分 lxcfs（只 bind cpuinfo）下 cpuinfo=fuse.lxcfs、stat/meminfo/loadavg=proc，识别结果与决策表一致。⑤ lxcfs 的 meminfo 按 guest 而不是读者所在的 cgroup 给值：Incus guest（Debian 12 / systemd 252，limits.cpu=4、limits.memory=512MiB）里按 deploy/systemd 的单元原样运行识别探针（drop-in 只换 ExecStart），服务进程位于 `system.slice/heron-agent.service`，该 cgroup 的 memory.max 为 128MiB，服务内读到的 MemTotal 仍是 512MiB；单元的 ProtectSystem=strict、PrivateTmp、ProtectHome 只给服务添加只读重挂与私有 /tmp，`/proc` 与 `/sys/fs/cgroup` 仍是原来的文件系统实例；服务内识别为 cgroup_namespace、有效核数 4、内存可见上限 512MiB、swap 上限 0，与同配置 guest 中由 shell 运行的结果逐字段相同。⑥ OrbStack 机器与 Docker 容器的 cgroup 文件对非 root 用户全部可读（0644），非 root 识别结果与 root 逐字段一致。⑦ 缺 cpuset 控制器（OrbStack `.lxc` 的直接子 cgroup、system.slice 的 scope 均如此）时 `cpuset.cpus.effective` 不存在，有效核数回退 `/proc/cpuinfo` 处理器数（procfs，主机核数）；四核满载 + agent taskset 单核下识别仍报主机核数，不取进程亲和性。⑧ `memory.swap.max` 可为 0（Incus `limits.memory` 顺带禁 swap），可见上限 0 是合法已知值。
13. CI 托管 runner（GitHub Actions ubuntu-24.04，kernel 6.17.0-1022-azure，x86_64，2026-10-06 实验）。——已确认：`/sys/fs/cgroup` 是真正的根 cgroup（无 `cgroup.type`、无 `cpu.max` / `memory.current`，而 `cpu.stat` 存在），`/proc/self/cgroup` 是 `0::/system.slice/…service`——挂载根与自身 cgroup 分离，按挂载根识别得到主机口径是正确语义。systemd-run scope（MemoryMax=512M）内的四步负载与内存算式一致：注入匿名 200M / 页缓存 0 / tmpfs 100M / 可回收 slab 0（30 万空文件的 dentry，slab_reclaimable 高达 385M）对应 used 增量 +204 / −0.4 / +99.5 / +1.3 MiB，env 口径的增量与注入量一一对应；宿主侧 `MemTotal − MemAvailable` 的同期增量（−42 / +8 / +104 / +24 MiB）被 runner 的后台负载淹没，只作参考不作证据。对照组 `memory.current − file` 口径会把 tmpfs 漏掉（−0.2MiB）、把 dentry 记成 386MiB 占用。

## 14. 构建、发布、安装

- 全部 `CGO_ENABLED=0`。agent 目标：linux/amd64、arm64、armv7、386、riscv64；darwin/amd64、arm64。hub 目标：linux/amd64、arm64，另出 Docker 镜像。
- Linux 支持矩阵：架构一级为 amd64 与 arm64（每次改动跑端到端），其余 agent 架构只保证能构建。发行版一级为 Debian 12（glibc、systemd）与 Alpine 3.21（musl、OpenRC、busybox），每次改动两个架构都跑端到端；二级为 Ubuntu 24.04 与 Rocky Linux 9，发版前跑。与 libc 无关由静态链接承载：产物不得带动态解释器，构建产出二进制时就检查，让产物变成动态链接的改动都在那里失败，而不是等到 Alpine 上启动报错——未显式关闭 cgo 且 C 工具链可用时，包含 net 的原生构建可能引入系统 C 库依赖。检查是三条独立的交付约束：没有 `PT_INTERP`、没有 `DT_NEEDED`、构建设置显式为 `CGO_ENABLED=0`；它们不互相等价（例如带 `netgo` 标签、开着 cgo 的构建也可能是静态链接），缺一条就拒绝。检查器是 `scripts/checkstatic`，发布流水线必须把全部 Linux 产物（agent 与 hub）交给它，而不是复制一份当前的文件清单。Linux 采集直接读 `/proc`、`/sys` 与 `/etc/os-release`，不调用发行版的命令行工具；共用的内核接口与 os-release 格式让采集不需要发行版专用分支，文件是否可读、返回值是否合理仍由测试与各发行版端到端验证。
- 构建顺序：`buf generate` → 前端构建 → `go build`。生成的 Go 代码入库，前端产物不入库。
- systemd 卸载边界（agent 与 hub 相同）：普通卸载保留本地 drop-in；`--uninstall --purge` 删除 `/etc/systemd/system/<服务名>.service.d` 和 `/run/systemd/system/<服务名>.service.d` 后执行 daemon-reload，不依赖主单元仍存在。目录为符号链接时只删除链接；不从 DropInPaths 枚举删除目标，避免波及共享 drop-in 与发行版文件。不清理系统 journal，由系统日志保留策略处理。
- 发布：`make release-full VERSION=vX.Y.Z` 或 `make release-hub-only VERSION=vX.Y.Z` 把这次 release 的产物生成到 `dist/`（选哪个由 `make release-kind` 按 `AGENT_VERSION` 判定，§14.1；下文列的是完整 release 的产物）；推送 `v*` tag 时 CI 先取判定、再调用同一目标并建 GitHub Release。构建逻辑只在 Makefile 一处，本地验收与线上发布用的是同一套产物。Linux agent 每个架构一个 `heron-agent_linux_<arch>.tar.gz`（二进制、systemd 单元、OpenRC 服务脚本），hub 每个架构一个 `heron-hub_linux_<arch>.tar.gz`，另有 `SHA256SUMS`、`install.sh` 与 `install-hub.sh`（两个 hub 的 tar 包内含 systemd 单元文件）。资产名不带版本号：`releases/latest/download/<名>` 与 `releases/download/<tag>/<名>` 都能直接拼出，脚本不必调用 GitHub API。版本号经 `-ldflags -X main.version` 注入，构建带 `-trimpath`，二进制里不含构建机的路径。三个安装脚本在打包时写入本版的版本号与本版全部 tar 包的 SHA-256（§5.7；只发 hub 的 release 只写入 `install-hub.sh`，另两个脚本取自绑定的 agent 版本，§14.1），写入只有一份实现，发布目标与 `deploy/` 的脚本测试都调用它；`SHA256SUMS` 仍随 release 发布，安装脚本不以它为校验依据；在线更新以它加 `SHA256SUMS.sig` 为依据（§4.10）。签名不在发布目标里：本地构建、验收与镜像构建都不需要私钥；release 流水线在发布目标之后单独签名，secret 缺失即失败——发出一个没有签名的正式版，新更新器都装不上它。签名在独立 job 里做：构建 job 的 `make ci` 与发布目标会运行 pnpm 依赖、Go 模块等第三方代码，私钥若与它们同 job，被污染的依赖就能带走私钥并在此后给任意产物签名，比篡改一次发布的后果大；签名 job 只编译标准库与 `internal/releasesig`、不恢复构建 job 写过的 Go 缓存，私钥只经环境变量交给签名那一步。签名私钥与仓库里的公钥不匹配时签名步骤即失败。签名后先用仓库里提交的公钥验 `dist/` 里的文件再发布，`gh release create` 之后从 Release 页面取回 `SHA256SUMS` 与签名再验一遍，不过即流水线失败。tag 带预发布后缀（semver 的 `-` 部分）时建为 prerelease，不成为 latest：开发构建的面板命令取 latest 的脚本；预发布的判定只写在 Makefile 一处，GitHub Release 与镜像的 `latest` 都读它。当前只出 Linux 产物，darwin agent 与 hub 的 Docker 镜像随 M6 加入（§15）。服务定义在仓库 `deploy/` 下是真实文件，打包时原样放入，不在脚本里以字符串另存一份。仓库必须公开：私有仓库的 Release 资产要鉴权才能下载。
- Linux 安装脚本（`install.sh`，以 root 运行）的步骤顺序：检测 init 系统与架构（卸载不看架构）→ 创建固定的系统用户 `heron-agent` → 检查下载器（curl 或 wget）与 `sha256sum`，缺了在任何网络操作之前报错 → 确保 CA 证书 → 下载 tar 包并按脚本内嵌的哈希校验（§5.7；源码脚本没有内嵌哈希、或清单里没有本机的包时，在检测完架构之后、建用户与任何网络操作之前就拒绝安装，不留下建了一半的账户）→ 解包并确认三个文件齐全 → 写同目录的临时二进制 → 没有配置或显式给出 `--re-register` 时，用临时二进制执行 `register`，保留已有本地探测策略 → 让服务用户能读配置：目录保持 root 属主、组为 `heron-agent`、0750，文件交给该用户、0600（register 以 root 写入，不改属主服务就读不到；目录不交给服务用户，服务用户就不能替换其中的目录项，root 对它的后续操作不会被链接或竞态劫持；agent 运行时不写配置目录）。这一步每次安装都做，不只在注册之后：手工重新注册、注册后被打断、账户被删后重建，都会留下服务用户读不到的配置 → 停止已安装的服务并确认（见下条）→ `mv` 替换二进制 → 安装服务定义并启动（systemd 单元或 OpenRC 服务，要求见下两条）→ 确认服务起来了：有上限地等到出现以服务用户运行的进程，3 秒后同一个进程仍在，否则失败退出并指出日志位置。init 的 start 返回 0 证明不了子进程活着（OpenRC 的情形见下；systemd 的 `Type=simple` 在 fork 之后即视为已启动），不确认就会出现脚本报成功、节点却不上线。判据与停服务之后的确认是同一个（有效 uid 为服务用户的进程）。建用户排在下载与注册之前：它若失败，注册窗口的名额尚未消耗、旧服务尚未停止。参数：`--hub`、`--key`、`--re-register`、`--name`、`--insecure-http`、`--base-url`（覆盖下载目录，用于镜像与本地验收；默认是脚本所属版本的 release 下载目录，只改变字节从哪里取，校验依据仍是内嵌哈希）、`--update-source github|hub`（在线更新的取产物来源，写 root 属主的 `/etc/heron-update-agent/config.json`，§4.10；只用于 systemd，OpenRC 下给出即在任何系统变更之前报错；首次安装不给即不写，等于 github；重跑不给即保留原值，显式给出才改——例行重跑不能把 hub 来源的节点悄悄切回 github，那样它下一次在线更新必然失败）。没有 `--version`：给了就报错并说明取对应版本的脚本。`--insecure-http` 在首次安装或显式重新注册时交给 `register`，普通升级沿用现有配置时交给 `heron-agent configure --insecure-http=true`，已有 http 部署靠它一条命令完成升级（§5.7）。重跑沿用现有配置时 `--name` 不生效，与 `--key` 被忽略时一样给出提示。
- 普通重跑即升级：已有配置且未给 `--re-register` 时跳过注册，`--hub` 与 `--key` 可省；同时给了 `--key` 时明确提示沿用现有注册。显式 `--re-register` 必须提供非空 `--hub` 与 `--key`，缺失时在锁文件、下载与系统变更前拒绝；它即使已有配置也注册，不先删除配置。使用窗口 key 重新注册会消耗窗口名额并新建节点，安装凭据则只认领指定的既有节点。替换之前先确认包完整（校验、解包、三个文件齐全），再停止已安装的服务——是否需要发 stop 看服务定义是否已安装，不看配置是否存在；发出之后无条件确认没有以服务用户运行的进程（扫描 `/proc/<pid>/status` 的有效 uid，有上限地等待），不论服务定义在不在：OpenRC 0.55.1 上 `rc-service stop` 在所测的各状态下都返回 0，supervisor 被杀而子进程留存时 `status` 也报 stopped，二者都证明不了进程已退出；单元或 init 脚本被手工删掉而进程仍在时也只有这一步抓得到。确认不通过即失败退出，不能在旧进程仍在运行时报告安装成功。二进制先写同目录临时文件再 `mv` 替换；替换前把现有二进制留成同目录 `.bak`。停服务之后、启动确认成功之前的任何失败退出——某一步失败（写更新来源、装服务定义、`daemon-reload`、`enable`）、新版本起不来或没留在运行、被信号打断（Ctrl-C、SSH 断开）——都由 EXIT trap 把 `.bak` 换回来、按原 init 重启旧版本并以非零退出，stderr 写明已回滚与日志位置；确认成功后才删 `.bak`，回滚换不回去时 `.bak` 留给人手工恢复——一次失败的升级若留下一个起不来的二进制、或干脆停着服务，节点就永久离线；首次安装没有旧二进制，不备份也不回滚。更新器在 agent 确认运行之后才启动：它起不来时安装失败，但不回滚已经在跑的新 agent。服务定义每次覆盖，单元的改动随升级下发。`--uninstall` 停止并禁用服务（停不下来即非零退出）、删除二进制与服务定义，保留配置与用户；`--purge` 必须与 `--uninstall` 同时给出；加 `--purge` 一并删除 `/etc/heron-agent`、`/etc/heron-update-agent`、日志目录、用户与同名组（普通卸载保留 `/etc/heron-update-agent`，与 agent 配置同一口径），并回查二者都已不存在，删不掉即非零退出。以 `curl … | sh -s --` 运行时脚本来自 stdin，脚本里每个可能读 stdin 的外部命令都显式 `</dev/null`（不能 `exec </dev/null`，那会切断脚本自己的来源）。
- systemd 单元使用静态 `User=` 并加固（`NoNewPrivileges=`、`ProtectSystem=strict` 等），默认带 `AmbientCapabilities=CAP_NET_RAW` 与 `CapabilityBoundingSet=CAP_NET_RAW`：裸机 Debian 的 `ping_group_range` 默认关闭（§13 第 2 项），没有这项能力时 ICMP 探测只能回报 error。用户由安装脚本创建并拥有配置文件，OpenRC 侧以同一用户运行，单元用静态 `User=` 并逐项写出加固，不经 `DynamicUser=` 隐式引入；`DynamicUser=` 会隐含的 `RestrictSUIDSGID=` 在单元里明写。单元设 `MemoryMax=128M`（§5.7）：2026-09-29 在 Debian 12.15 arm64 / systemd 252.39-1~deb12u2（OrbStack LXC，cgroup v2）满 64 个 5 秒间隔 ICMP 任务运行至少 3 分钟，cgroup 峰值 21389312 字节，取六倍后向上取整；详细负载、进程读数与生效前提写在服务定义注释里。OpenRC 不设：`rc_cgroup_settings` 需要宿主机向服务 cgroup 下放 memory 控制器，2026-09-29 在 Alpine 3.21 / OpenRC 0.55.1（OrbStack LXC，cgroup2，根 `subtree_control` 为空）实测静默不生效，安装脚本不改宿主机全局 cgroup 配置。launchd 同样不设；OpenRC 与 launchd 的内存防护只靠 agent 响应体上限，没有服务级强制上限。monitor 提交 `85f6702` 记录过未开 nesting 的 LXC 容器里 `DynamicUser=` 的单元拒绝启动（226/NAMESPACE）；2026-09-26 在 Debian 12 bookworm-backports 的 Incus、容器内 systemd 252（252.39-1~deb12u2）、`security.nesting=false` 的容器里实测，静态 `User=` 与 `DynamicUser=` 的单元都能启动，该记录在此环境不成立；它出自哪种 LXC 配置未定位，不作为选型理由。采集读不到某个文件时只让对应字段缺失并记日志，上报照常，因而加固项若遮蔽了采集要读的 `/proc`、`/sys` 路径不会以失败显形；由 §12 的真机对照验收承载。
- init 系统支持 systemd 与 OpenRC。判定：`/run/systemd/system` 存在为 systemd；否则 `/sbin/openrc-run` 存在且可执行为 OpenRC（`/run/openrc` 表示已启动）；两者都不是时安装脚本报错并列出支持的 init，不静默降级（容器里两者通常都不存在）。OpenRC 服务用 `supervisor=supervise-daemon`、`command_user` 为固定系统用户、`capabilities="^cap_net_raw"`，与 systemd 的 `AmbientCapabilities` 等价（2026-09-25 在 Alpine 3.21 / OpenRC 0.55.1 真机上实测：ping_group_range 关闭时授予该能力 ICMP 可用，去掉即不可用）。`output_log` 与 `error_log` 的文件必须在启动前建好并交给运行用户，由服务脚本的 `start_pre` 建立，只把这两个文件交给运行用户，日志目录保持 root 属主、不递归改属主（服务用户能增删目录项时，root 按路径做的改属主之类的操作，对象可以被它换成别的文件；所以 root 不在服务用户控制的目录里操作），每次启动都成立而不只在安装时建一次：supervise-daemon 降权后才打开它们，打不开时子进程秒退、反复拉起，而 `rc-service status` 仍显示 started。agent 异常退出后两种 init 都无限次重启、每次间隔 5 秒：systemd 用 `Restart=on-failure`、`RestartSec=5` 与 `StartLimitIntervalSec=0`（默认 100 ms 间隔加 10 秒内 5 次的上限，会让一次短暂故障把单元永久留在 failed）；OpenRC 用 supervise-daemon 的对应设置，取值以实测为准。
- 安装脚本用 POSIX sh，兼容 busybox，按实际存在的工具分支，不假设任何单一工具（所测的基础容器镜像与机器镜像之间、以及各发行版之间，工具集都不相同）：先建同名组（`groupadd --system`，没有时 busybox 的 `addgroup -S` 或 Debian 的 `addgroup --system`），再以它为主组建用户：优先 `useradd --system -g`，没有时 busybox 的 `adduser -S -D -H -G`、Debian 的 `adduser --system --no-create-home --ingroup`（busybox 的 adduser 不指定组时会把用户放进 `nogroup`，而 OpenRC 的 `command_user` 与配置文件属主都写 `heron-agent`），建完一律回查主组 `id -gn`，不信退出码——实测 Debian 的 perl 版 adduser 收到 busybox 风格的参数时打印用法、不建用户，却返回 0。下载用 curl 或 wget，按实际存在的那个走（所测基础容器里 Alpine 只有 wget、Rocky 只有 curl、Debian 与 Ubuntu 两者都没有；所测机器镜像里四个都有 curl），两者都没有就报错说明依赖；下载地址为 https 时 curl 限定请求与重定向都只走 https（wget 没有对应开关）。能力授予交给 init，不依赖 setcap（所测的 Alpine 与 Debian 机器镜像、以及 Alpine、Debian、Ubuntu 基础容器里都没有）。所测的 Debian 与 Ubuntu 基础容器不带 CA 证书（机器镜像都带），CA 是否存在按文件探测而不是按发行版名判断；缺失时安装脚本先装 `ca-certificates`，判据是下载地址或 hub 地址任一为 https——默认下载地址就是 https，只看 hub 地址会让下载先失败。
- hub 安装脚本 `install-hub.sh`（`deploy/`，以 root 运行）：在 Linux 主机上一条命令装好 hub 的 systemd 服务，重跑即升级，`--uninstall` 与 `--purge` 的删除范围与 agent 脚本同语义，但两者都要确认（无终端时须 `--yes`）：agent 卸了重装即回，hub 的普通卸载停掉的是全部节点的展示与告警，purge 删的是唯一一份数据与全部节点凭据；不用 Docker 的自托管者由此有一条能直接跑的路径。与 agent 脚本同一套约束（POSIX sh、按实际存在的工具分支、curl 或 wget、按内嵌哈希校验且没有 `--version`（§5.7）、静态系统用户 `heron-hub`、先建用户再下载、停服务后确认进程退出、启动后确认进程活着），不另立口径。单元 `deploy/systemd/heron-hub.service` 用静态 `User=` 并逐项加固，不需要 `CAP_NET_RAW`；`--listen` 默认 `127.0.0.1:8080` 不变，脚本不替用户决定对外监听。数据目录固定 `/var/lib/heron`，root 属主、服务用户组可写（0770），库文件属服务用户 0600：SQLite 要在目录里建删 `-wal`/`-shm`，服务用户必须能增删目录项，agent 配置目录那套只读的 0750 不适用；目录留 root 属主是为了升级时能锁住它——脚本在停服务并确认该 uid 没有进程之后先把目录收成 0750，此时只有 root 能增删目录项，再核对库文件不是链接、只有一个硬链接才改属主，改完放回 0770；服务用户拥有目录的话这一步没有可靠的锁。管理员密码在脚本末尾提示用 `heron-hub passwd --db /var/lib/heron/heron.db` 设置，脚本自己不生成、不打印密码。无终端时不交互、取默认值，需要确认的动作要求 `--yes`。升级时 `--listen`、`--timezone`、`--trusted-proxies` 等参数沿用已装单元里的值，除非命令行显式给出——重跑即升级不能把用户改过的参数重置回默认。停旧服务之前先查端口冲突，按 pid 排除 hub 自己。只做 systemd；不做菜单，只做参数式（菜单是交互层不是功能）。替换二进制前把现有二进制留成同目录 `.bak`，并在目录锁成 0750、服务用户没有进程的窗口内把三个库文件各自留一份同目录 `.bak`；停服务之后、启动确认成功之前的任何失败退出（某一步失败、启动或启动确认失败、被信号打断）都由 EXIT trap 把二进制换回，三个库文件的备份完整时把库一起换回，然后按 systemd 重启旧版本并以非零退出，stderr 写明已回滚与日志位置——候选 hub 在确认之前就以 `MigrateSchema` 打开过库，只换二进制会把 hub 留在旧程序打不开新库的状态；候选新建的 `-wal`/`-shm` 没有备份，回滚时删掉，确认成功后才删这一批 `.bak`。库备份没做完（例如复制到一半磁盘写满）时候选 hub 还没启动过、库原样未动：不拿只复制了一半的 `.bak` 覆盖它，也不删原库的 `-wal`/`-shm`；数据目录若停在 0750 先放回 0770 再重启，0750 下旧 hub 建不了 WAL/SHM。锁内复检与 drop-in 检查失败是要人查看的情形：只换回文件、不启动。首次安装没有旧二进制，不备份也不回滚。release 产物加这两个文件，`scripts/install-accept.sh` 加 hub 一格并在真机验收，README 加一节。
- macOS agent：采集在 `CGO_ENABLED=0` 下实现（§13 第 1 项的实验定案）：`x/sys/unix` 的 sysctl 取启动标识（`kern.bootsessionuuid`）、内存总量、负载、swap、连接数（`net.inet.tcp.pcbcount`/`net.inet.udp.pcbcount`）、网卡计数器（`net.link.generic.ifdata`）与 facts；`statfs` 取磁盘；`clock_gettime(CLOCK_MONOTONIC)` 取运行时长；purego 调 libSystem 取逐 CPU tick、VM 统计、页大小与进程数。采集分层为平台取原始读数的 `Host` 与平台无关的差分、过滤与 `used ≤ total` 检查；darwin 的字节布局解析不带 build tag、Linux 上可测，只有系统调用层带 darwin 约束并引用 purego，其依赖不链入 Linux 二进制。darwin 二进制按平台约定动态链接 libSystem，静态门禁只查 Linux 产物。产物 `heron-agent_darwin_<arch>.tar.gz`（二进制、launchd plist）。安装脚本 `install-macos.sh` 以 root 运行：检测架构 → 用 `dscl` 建 `_heron-agent` 用户与组（uid/gid 从 499 向下取空闲号：Apple 逐版从 300 向上追加系统账户，升级会替换低号段的第三方账户）→ 下载并用 `shasum -a 256` 按内嵌哈希校验（§5.7，参数与 Linux 脚本同口径：没有 `--version`，有 `--insecure-http` 与 `--re-register`）→ 写临时二进制 → 没有配置或显式给出 `--re-register` 时，用临时二进制执行 `register`，保留已有本地探测策略 → 配置属主同 Linux（`/etc/heron-agent`，目录 root 属主、组 `_heron-agent`、0750，文件 0600）→ 停止已装的 LaunchDaemon 并确认进程退出 → 替换二进制 → 写 `/Library/LaunchDaemons/xyz.heron.agent.plist`（`UserName` 为该用户、`KeepAlive`、`ThrottleInterval` 5 秒——两次拉起的最小间隔，进程跑满 5 秒后退出会立即拉起，`KeepAlive` 为真时 0 退出也拉起，与 Linux 的重启间隔同量级、日志在 `/Library/Logs/heron-agent/`：目录属 root:wheel 0755，服务用户不能在其中放条目，launchd 无论以什么身份打开日志都不会被链接引到别处；两个日志文件每次安装预建并交给服务用户 0640；安装脚本先把目录交给 root、`chmod -N` 去掉 ACL（服务用户曾为属主时可能加过允许项，数字 chmod 与 chown 都不去掉它），再检查文件是不存在或链接数为 1 的普通文件；预建、属主、权限这些依赖外部条件的操作全部在停服务之前完成，停服务之后只剩换二进制、写 plist、bootstrap。文件被删后若 launchd 以服务用户身份打开会反复 EX_CONFIG，重跑安装恢复；launchd 以什么身份打开由真机清单判别，不在代码里假设）→ `launchctl enable` 后 `bootstrap system`（enable 清掉可能残留的禁用覆盖）→ 确认进程活着（`ps -axo uid=,pid=,comm=` 按有效 uid 与可执行路径，macOS 没有 /proc；只按 uid 不够：launchd 会以该 uid 派生 cfprefsd、trustd 之类的辅助进程，停止确认与启动确认都要把它们排除）。替换前同样把旧二进制留成 `.bak`，停服务之后到启动确认成功之前的任何失败退出都卸下新作业、换回旧二进制、重新 `bootstrap` 并确认旧版本在跑，与 Linux 同一口径；plist 不回滚。是否发 `bootout` 看作业是否已载入 system 域（`launchctl print`），不看 plist 文件在不在：launchd 按已载入的作业管进程。架构按 `hw.optional.arm64` 判定再看 `uname -m`（Rosetta 终端里 `uname -m` 报 x86_64）。不装 CA：macOS 自带 curl 与系统信任库。重跑即升级，`--uninstall`、`--purge` 语义同 Linux。没有 macOS 虚拟机可用：脚本逻辑用桩测试，真机验收由人在 Mac 上执行，脚本随附检查清单。面板的安装命令只给 Linux 的两条，macOS 的写在 README。CI 含 macOS runner 跑 agent 的测试。
- hub Docker 镜像：`ghcr.io/xjetry/heron-hub:<version>`，预发布不打 `latest`；`FROM scratch`，只含静态二进制（`/usr/local/bin/heron-hub`，让 `docker exec … heron-hub` 按名字可执行：`/` 不在容器默认 PATH 里）、CA 证书（通知出站 HTTPS 要用）、非 root 用户、属于该用户的空 `/data` 与 1777 的 `/tmp`（SQLite 的排序溢出、临时表与建索引要写临时文件，没有 `/tmp` 时报 `disk I/O error`，小查询不触发）；时区数据已嵌入二进制。根文件系统由 `scripts/checkimage` 逐条目核对，多出或缺少任一条即失败。数据卷 `/data`，默认参数 `serve --db /data/heron.db --listen 0.0.0.0:8080`；容器里监听非 loopback 是预期的，启动告警照旧，反代与 `--trusted-proxies` 由部署者配。管理员密码经 `docker exec -i … heron-hub passwd --db /data/heron.db` 设置；不带 `-i` 时容器内 stdin 立即 EOF，`passwd` 单独报没有输入。镜像的构建与推送都在 Makefile（构建器是按 digest 固定的 BuildKit，本地与发布同一版本），release 流水线不另用 build-push 类 action；镜像先于 GitHub Release 推送并回读：先只推版本 tag，回读时逐平台拉回、冒烟并把导出的根文件系统交给 `scripts/checkimage` 核对（核对的是 registry 上实际存在的内容，不是构建时的中间产物），正式版本再把 `latest` 指向已回读的 digest 并回读 `latest`——`latest` 只会指向回读通过的镜像；回读里"取不到"与"读取失败"分开判定，读取失败不放行；同组 release 运行串行；任何一步失败重跑 job 即可；带构建元数据（`+`）的 tag 不能成为 Docker tag，这类 tag 的发布整体失败、什么都不发布；新建的 ghcr 包首次发布须手工设为公开。`make docker` 在本地构建、核对并冒烟（起容器、`/admin/` 返回 200 的面板页而不是"未构建"说明、`passwd` 可执行）。镜像的 `HEALTHCHECK` 用 exec 形式调镜像内二进制的 `health` 子命令（scratch 没有 curl 也没有 shell，探针必须随二进制进镜像）：`heron-hub health [--url http://127.0.0.1:8080]` 对 `<url>/healthz` 发 GET，3 秒总时限覆盖连接、请求与读体，2xx 打印 `ok` 退出 0，否则按状态码或传输错误非零退出，不跟随重定向——反代把探测路径 302 到别处时，探针要看的是候选 hub 自己的状态码。`/healthz` 无鉴权、只回 `ok\n`、`Content-Type: text/plain; charset=utf-8`、`Cache-Control: no-store`，不带版本、节点数或配置；挂载时机与 Serve 同生命周期：`newHandler` 在打开库、加载内存索引并挂载全部服务之后才把它放进 mux，mux 只在 `srv.Serve` 里被调用，所以候选 hub"绑定监听但在确认前不开始 Serve"（§5.7）期间探测不到就绪——绑定即应答会让更新器把一个尚未接受业务的候选当成就绪。它不是 `PublicService` 的挂载点，不进公开服务的限流桶（§10 只覆盖 `PublicService` 挂载点）。间隔 30s、超时 5s（大于探针自身的 3s 总时限，留给进程调度）、重试 3 次、起始宽限 5s（覆盖打开库与加载索引）的原因写在 Dockerfile 注释里。
- Linux systemd 部署安装新版安装器后可在后台更新 Hub、批量下发节点 Agent 更新；只支持版本递增的官方正式版，本机更新器与服务定义仍由 root 重跑安装器更新。出站受限、只能连到 hub 的节点在安装时加 `--update-source hub`，由 hub 中转官方签名产物（§4.10）。Docker、OpenRC、macOS 沿用原安装方式。hub 版本经 `GetSnapshotResponse.hub_version` 下发，面板据此生成该版本 release 的安装命令（§10）；hub 绑定的 agent 版本经同一响应的 `bound_agent_version` 下发（§14.1），面板据此显示各节点 agent 版本、标出低于绑定版本的节点：按 semver 2.0 优先级比较（预发布低于对应的正式版，构建元数据不参与），任一方不是带 `v` 前缀的合法 semver 时不标。

### 14.1 hub 与 agent 的版本

- 一套 tag，两种 release。每个 `vX.Y.Z` tag 都是一个 hub 版本；agent 有改动的那次 release 同时带 agent 组产物，agent 版本即该 tag（完整 release），只改 hub 的 release 不带 agent 组产物（只发 hub）。agent 的版本号因而是它最后一次变动时的 release 版本，会跳号：hub v1.2.4、v1.2.5 绑定 agent v1.2.3，agent 有改动的 v1.2.6 绑定 v1.2.6。不另开一套 agent 的 tag：线上每一代更新器都按"版本即 tag"取产物——旧更新器查 `releases/tags/<版本>`，现更新器拼 `releases/download/<版本>/…`，被签消息里也是这个 tag（§4.10）——独立编号会让同一个版本号同时指 hub 与 agent，已装的更新器都得先重跑安装器才能继续在线更新；一套 tag 之下，agent 版本总是一个真实存在、带 agent 产物的 tag，这些假设原样成立。GitHub 的 latest 仍是最新的 hub 版本。
- 绑定：仓库根的 `AGENT_VERSION`（一行，带 `v` 的 semver）是"这一提交的 hub 绑定哪个 agent 版本"的唯一事实源，构建 hub 时与 `main.version` 一样经 ldflags 注入（`main.agentVersion`）。没有注入的 hub（直接 `go build`）没有绑定：节点在线更新一律拒绝、面板不标落后——空值在这里是收紧，不是"任何版本都行"。`make ci` 按原始字节校验文件：恰好一行、以换行结尾、内容是 release tag（Makefile 读文件时只取第一行并去掉空白，只校验读出来的值会放过多余的行）。两次发版之间文件写的是最近一次已发布的 agent 版本；agent 有改动时，由发版提交把它改成本次的版本号。
- 发布判定只有一条规则，实现在 `scripts/releasekind`，`make release-kind` 打印它（与 `release-channel` 同处；接线由发布规则的桩测试核对，规则本身由单元测试核对）：tag 为 vX、`AGENT_VERSION` 为 vY 时，vY = vX 为完整 release；vY 按 semver 优先级低于 vX 且是正式版，为只发 hub；其余情形（vY 高于 vX、vY 是预发布而不等于 vX、格式不合法）发布失败。两种 release 是两个目标 `make release-full`、`make release-hub-only`，调用方（release 流水线、本地验收）先取判定再调用对应目标；两个目标开头各自再判定一次，不符即失败，单独调用也绕不过规则。不设一个在 make 里再分派的 `release`：make 对含 `$(MAKE)` 的配方行在 `-n` 下也整行执行，判定会在只想看配方的 dry-run 里真的跑起来。`AGENT_VERSION` 默认取仓库文件；本地验收构建（`install-accept`、`macos-accept`、README 的 `v0.0.0-check`）调用 `release-full` 并显式给出与 `VERSION` 相同的值，产出完整的一套——它们验的是本次构建的 agent，不是某个已发布的版本。
- 产物按角色分两组。hub 组：`heron-hub_linux_<arch>.tar.gz`、hub 架构的 `heron-updater_linux_<arch>.tar.gz`（hub 主机的更新器由 `install-hub.sh` 一起装）、`install-hub.sh`、Docker 镜像。agent 组：五个 Linux 架构的 agent 包与更新器包、两个 darwin 包、`install.sh`、`install-macos.sh`。完整 release 两组都发。只发 hub 的 release 发 hub 组，另把 vY 的 `install.sh` 与 `install-macos.sh` 原样复制进来：这两个脚本内嵌的是 vY 的版本号、下载目录与哈希（§5.7），于是 `releases/latest/download/install.sh` 与面板给出的 `releases/download/<hub 版本>/install.sh` 装的都是 hub 绑定的 agent——每个 release 里的 agent 安装脚本装的是这个 hub 版本绑定的 agent。复制前取 vY 的 `SHA256SUMS` 与签名，用仓库内的受信公钥验签（§4.10 的同一函数，被签消息里的版本是 vY），核对两个脚本的哈希，并核对 vY 的清单含完整的 agent 组；任一不过即失败。所以被绑定的 vY 必须是带签名的 release：v0.5.3 及更早的版本没有签名，不能被只发 hub 的 release 绑定。复制来的脚本照常进 vX 的 `SHA256SUMS`、受 vX 的签名覆盖；发布后的回读另核对它们与 vY 的逐字节相同。
- 只发 hub 的门禁：agent 组的构建输入自 tag vY 以来没有变化。输入有三类：（1）本模块源码：`./cmd/agent`（全部 Linux 与 darwin agent 平台）与 `./cmd/updater`（全部 Linux agent 平台）的 `go list -deps` 闭包里属于本模块的包，取参与该平台构建的全部文件（Go 源码、汇编及其 `#include` 的头文件、`//go:embed` 的文件等 `go list` 列出的各类文件）；以本地目录 replace 的模块没有不可变的版本，同样按文件计入（目录须在仓库内，否则门禁报错）。生成的 proto 包按 proto 文件算：只算 agent 代码实际引用的生成标识符所在的文件，加上这些文件的 proto 源的 import 闭包生成的文件——生成包把 `AdminService` 与 `AgentService` 放在同一个 Go 包里，按包算会把每个只动 `admin.proto` 的 hub 功能都判成 agent 改动，拆分就失去意义。（2）这些包用到的第三方模块与版本（本地目录 replace 的模块另计入它自己的 `go.mod`：其中的 `go` 指令决定那个模块的语言版本），以及根 `go.mod` 的 `go`、`toolchain` 与 `godebug` 指令（`godebug` 改变运行时默认行为）。（3）agent 组的打包配方：构建参数、架构表、打包步骤与 make 层导出给构建的环境变量都放在单独的 makefile 片段里，片段文件本身就是输入，打包与门禁读的是同一份；主 Makefile 不 export 影响构建的变量（`AGENT_VERSION` 只进 hub 的 ldflags），发布规则测试核对这一点——否则主 Makefile 的一行 export 就能改变 agent 产物而不被门禁看见。vY 与 HEAD 两侧各自展开输入，比较两侧的并集，任一文件内容或模块版本不同即失败，列出变了的输入并提示把 `AGENT_VERSION` 改成 vX。没有跳过开关：门禁放过一次真实的 agent 改动，结果是那次改动静默地没有发布出去。门禁只保证 agent 改动不会漏发，不证明 hub 与 agent 的组合可用；组合由下一条的端到端承载。
- 端到端：完整 release 照旧用当前源码构建的 agent 跑 `make e2e`。只发 hub 的 release 实际发出去的组合是 hub vX 加 agent vY，另用 vY 已发布的 agent 包跑同一套端到端，不以当前源码构建替代；下载与校验复用 `compat-e2e` 的实现，依据是已验签的 vY `SHA256SUMS`。`compat-e2e` 固定的旧版基线不变。
- hub 侧：`GetSnapshotResponse` 与 `GetUpdatesResponse` 下发 `bound_agent_version`。节点在线更新的目标只能是绑定版本，检查放在 `updates.Manager.Start`（`StartUpdate` 与 `ExecuteChange` 的 `start_update` 都经过它）；hub 自身的更新仍以 GitHub 最新正式版为目标。面板的节点落后标记与更新页的节点目标都按绑定版本判定，更新节点不必先检查官方新版本；安装命令的地址不变，说明改为装的是 hub 绑定的 agent 版本。hub 启动日志带上绑定的 agent 版本。
- 过渡：拆分后的第一个 release 必然是完整 release——v0.5.3 及更早的版本没有签名，不能被绑定；hub 中转同时改了 agent 与更新器，门禁也不会放行只发 hub。仍停在拆分之前的 hub 把 GitHub 最新正式版当作节点的更新目标；如果最新版是只发 hub 的 release，节点任务会在下载阶段失败（旧 agent 不受影响）。发版说明写明先升级 hub。

## 15. 里程碑

每个里程碑有独立的实现计划，结束时都是可端到端运行的状态。

| 里程碑 | 内容 |
|---|---|
| M1 垂直切片 | proto 与 buf 流水线；hub 的 `ingest` / `live` / `store`（仅 1m 级）；节点 token、注册窗口、`heron-hub` 的节点管理子命令；Linux agent 采集与上报循环 |
| M2 管理面板 | `AdminService`、管理员登录、节点管理与 token 轮换、实时视图、历史图表；5m / 1h 上卷、prune、按窗口选级 |
| M3 流量与探测 | 流量累计；探测任务与版本对账、agent `prober`、探测存储与上卷、图表 |
| M4 告警 | 规则、状态机、Telegram / Webhook、投递记录 |
| M4 之后：接入与 Linux 交付 | API token、技能文件与 `GetApiReference`（§5.6）及其面板页；Linux 发布流水线、安装脚本、systemd 与 OpenRC 服务、面板安装命令（§14） |
| M5 公开页 | `PublicService`、外观定制、`--public-dir` |
| M6 交付 | macOS agent、Docker 镜像 |
| M6 之后：节点计费 | 费用与到期（§9.4）、到期告警规则（§9.1、§9.2）、面板与公开页的费用与到期 |

## 16. 参考项目

`reference-projects/` 下有三个只读参考（不入库）。

沿用自 monitor（`src/agent_ws.rs`、`src/db.rs`）：在线与最新数据同一事实；hub 侧用 `boot_id` + 内核计数器做流量差分且"无读数 ≠ 0"；历史行是整桶聚合而非边界瞬时采样；时长用单调钟；限时限量且失败计数独立的注册窗口；主键顺序按查询路径排；非法上报不改动已有状态。v1.3.2 一轮另沿用：节点公开备注（其 #89：单行、至多 100 个码点、超长拒绝不截断，只随公开节点下发，§6.6 与 §10）；价格的货币符号取 `Intl.NumberFormat` 的 zh-CN 形式（#88），数额保留存储的小数位，不按币种默认精度舍入；非回环监听的启动告警分别说明端口发布与 host 网络两种部署路径的暴露面（#82），只改文字、不检测容器或网络模式；历史查询的上卷水位与各级数据行在同一个读事务里读出（#85），并据此把水位之后较细一级的数据拼进窗口右缘（§6.5）。v1.4.0 一轮另沿用：管理端响应不压缩并带 `no-store, no-transform`（#101、#104，见 §5）；历史窗口以 hub `now` 为准（#100、#102、#103 同类，见 §10）；无副作用调用的客户端等待预算与轮询查询切回前台即刷新（#98 同类，见 §10）；存储统计单份计算、限时复用（#99 同类，见 §6）。

有意不同于 monitor：token 存 hash 而非明文；单仓库共享协议类型而非两仓库靠运行时契约检查；转发信息只从显式可信代理采用；主题产物按摘要入库，并在同域名入口的不透明来源沙箱中运行，不获得面板同源权限；hub 不自供也不自选 agent 二进制（§4.10 的中转只转发官方签名产物，接受与否由节点验签决定；计费字段与 GeoIP 外呼一度也在此列：计费字段自 §9.4 起作为提醒用的展示值纳入；国家查询见 §4.9，默认开启；与 monitor 的差别是服务地址来自配置、地址只取 hub 看到的来源）。v1.3.2 一轮：每点覆盖度不照搬 #81 的"该桶有数据的分钟数"，覆盖率的分母只计 hub 可观测的分钟，hub 停机或未在接收的分钟记为未知、不算漏报，覆盖计数与在线判定分开（观测判据见 §4.5，查询口径见 §6.5）；不在取读连接前对超过阈值的 WAL 做 TRUNCATE checkpoint（#85），是否需要 checkpoint 策略由 `GetStorageStats` 的 WAL 文件观测与 §13 第 9 项决定；SQLite 的临时文件（#78）由镜像自带的 1777 `/tmp` 承接，hub 也没有 VACUUM 入口；删除节点按显式清单清理从属行、恢复时清理孤儿行，node id 用 AUTOINCREMENT 永不复用、恢复时显式保留序列，#90 那类复用 id 挂上旧历史的问题不出现。v1.4.0 一轮未采纳：指针事件拖拽排序（#92），Heron 已有“移动到…”菜单与键盘排序可替代，前端改版时再议；站点图标上传（#94），暂不做；公开列表复用压缩帧（perf 分支），Heron 的快照缓存已按压缩方式分键缓存。

规避自 komari：token 经 URL 传递且有三个读取位置；WebSocket 与 HTTP 两套在线状态并存；远程执行 / 终端 / 文件管理；内嵌 JS 引擎的插件系统；三方言自研时序层。

沿用自 beszel（`internal/alerts/`、`agent/network_monitor_*.go`、`internal/hub/heartbeat/`、`agent/cpu_linux.go`）：节点维护状态与静默时段让计划内中断不产生告警，静默只抑制投递、不停状态机；hub 自身向外部监控推心跳，解决"谁来监控监控者"；资源规则覆盖 CPU、负载与网卡速率；探测种类含 HTTP 与 DNS，结果带总数、成功数与和以便再聚合；cgroup 限额下 CPU 取容器视角；休眠唤醒后作废跨越休眠的探测样本；二进制自带 `health` 子命令供 scratch 镜像做 HEALTHCHECK。

有意不同于 beszel：指标表只存可加量并按桶对齐，不存 JSON blob 也不做滑动窗口上卷（它的 10m 级不查重、上卷时刻离线的节点被跳过且 1m 数据一小时后删除，形成永久空洞）；离线告警的 pending 持久化而非内存定时器，投递有记录与重试，触发与恢复有滞回；节点 token 只存 hash、经注册窗口或安装凭据换发、每节点独立，而不是明文存储、面板可读回、通用 token 直接沿用为节点凭据；来源地址只从显式可信代理采用，不无条件信任 `CF-Connecting-IP`；agent 与 hub 之间只有 unary 加 TLS，不做 SSH 反向拨号与 WebSocket 双传输回退，也不用静态 token 的确定性签名认 hub；图表缺数据按时间网格出 null，不按"间隔乘 1.5"启发式插点；版本号经 ldflags 注入而非源码常量。ec96c134..4fe1a0a7 一轮：HTTPS 探测不加"跳过证书校验"的开关（其 #2500），自签目标的做法见 §8.2（钉公钥指纹）；LXC 与容器里 CPU、内存的口径不做"检测到 LXC 就一律改用 cgroup"的开关，按资源给出来源与有效容量的做法见 §4.2 的执行环境识别（实验事实见 §13 第 10–13 项）；启动时不预采差分基线（其 initializeCpu）：CPU 占用与网卡、磁盘速率都由两次采样差分得到（§4.2），首个样本缺读数而不是报 0，代价是一个上报间隔。4fe1a0a7..bc2278e7 一轮：仅其 #2522（xbps 包管理器的更新检测），属上面已规避的包更新检测一类，无新增采纳。

规避自 beszel：Docker 容器、systemd 单元、SMART、ZFS、GPU 采集依赖 socket 或外部命令，超出单一职责与零依赖；包更新检测调用 apt-get、dnf 等，与 §14 不调用发行版工具冲突；shoutrrr 通知库绕开 §9.3 的出站边界；核心指标采集失败时报 0 而非缺失；网卡集合启动时缓存一次、运行中新增的网卡发现不了；速率上限兜底计数器回绕；YAML 对账以名称加地址为键，改名即丢历史；凭据明文写进 0644 的 unit 文件或安装命令参数。
