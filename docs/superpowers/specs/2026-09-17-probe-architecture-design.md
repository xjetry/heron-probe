# 服务器监控探针：架构设计

日期：2026-09-17

## 1. 目标与非目标

**目标**：一个单一职责的自托管服务器监控探针。agent 采集主机指标并上报，hub 存储并展示。

范围内：

- 主机指标采集（CPU、内存、交换、磁盘、网络、负载、连接数、进程数）与实时 / 历史图表
- 延迟探测：hub 下发目标，agent 执行并回报
- 告警通知：节点离线 / 恢复、探测异常
- 公开状态页（匿名可访问）
- 流量统计：总量与按重置日滚动的周期用量
- 公开页主题：经 `AdminService` 上传、启用、删除主题包；主题是只调 `PublicService` 的静态前端工程，由 hub 在独立 origin 下托管，并随功能提供开发指南

已确认要做、但尚未在本文成形的功能点记在仓库根的 `FEATURES.md`；其中某条进入里程碑时，设计并入本文并从那里删除。

**非目标**（明确排除，新增功能前先对照此表）：

| 排除项 | 原因 |
|---|---|
| 远程终端、命令执行、文件管理 | 会让 hub 成为对全部节点的远程代码执行入口；hub 失守即全部节点失守 |
| agent 自动更新、hub 托管 agent 二进制 | 本质是"hub 可向全部节点推送代码"，与上一条同类 |
| 插件系统 | 第三方代码在 hub 进程内执行，与远程执行同类。主题的代码跑在访客浏览器里，不是同一件事 |
| 主题市场：hub 出网拉取远程目录并自动安装 | 让 hub 携带信任去访问代码分发点，与 GeoIP 外呼被排除的理由同型。主题包由管理员上传，不由 hub 去取 |
| OAuth、2FA、多用户 | 单管理员足够 |
| GeoIP 外呼、计费与到期字段 | 与监控无关 |
| 多数据库方言、外接时序库 | 目标规模内单文件 SQLite 足够 |
| 资源阈值告警、流量用量告警 | 未列入需求 |
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
proto/probe/v1/       agent.proto / admin.proto / public.proto / types.proto / access.proto
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
  api/       AdminService / PublicService 实现
  web/       嵌入的前端产物 + 静态目录替换
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
| `probe.v1.PublicService` | 公开页、第三方主题 | 无，按来源 IP 限流 |

鉴权由"服务挂载时绑定的拦截器"承载，不在方法内逐个检查：新增方法无法漏掉鉴权，因为不存在未绑定拦截器的挂载点。

`AdminService` 的每个方法用 `probe.v1.access` 选项声明准入口径：`ACCESS_LOGIN`（仅 `Login`，凭据是请求体里的密码）、`ACCESS_READ`（会话或 API token：无副作用，不列出或管理凭据，也不回显可能含密钥的内容——配置本身，或外部接收方对它的回显）、`ACCESS_SESSION`（仅会话：有副作用的方法；凭据管理——包括只读的 `ListApiTokens`，自动化进程没有理由知道还有哪些 token 存在；回显可能含密钥的配置的方法——`ListNotifyChannels` 会回显 webhook 的请求体模板，模板里可能放着密钥，而 agent 不需要通知渠道的配置；`GetAlertDeliveryError` 返回投递失败的原文，接收方可能在错误响应里回显收到的请求体）。拦截器在构造时从生成的描述符读出整张表，任一方法未声明即 panic：未声明的方法无法随 hub 启动，因而不存在"漏标时默认放行还是默认拒绝"的取舍。准入口径与方法定义写在同一处，proto 仍是单一事实源。它与 `idempotency_level` 是两件事：后者决定是否接受 GET，`AdminService` 一律不标（§3.3）。

公开数据使用独立的消息类型（`PublicNode`、`PublicSnapshot`），不对 `Node` 做字段过滤。由此默认方向是"私有"：给 `Node` 加字段不会出现在公开页，必须显式加入 `Public*` 消息才公开。

### 3.3 方法清单

- `AgentService`：`Register`、`Report`。
- `AdminService`：`Login`、`Logout`；节点 `ListNodes`、`CreateNode`、`UpdateNode`、`DeleteNode`、`RotateNodeToken`、`ReorderNodes`；注册窗口 `OpenRegisterWindow`、`CloseRegisterWindow`、`GetRegisterWindow`；数据 `GetSnapshot`、`QueryMetrics`、`QueryProbes`、`GetTraffic`、`AdjustTraffic`；探测 `ListProbeTasks`、`SaveProbeTask`、`DeleteProbeTask`；告警 `ListAlertRules`、`SaveAlertRule`、`DeleteAlertRule`、`ListAlertEvents`、`GetAlertDeliveryError`、`ListNotifyChannels`、`SaveNotifyChannel`、`DeleteNotifyChannel`、`TestNotifyChannel`；设置 `GetSettings`、`UpdateSettings`、`GetStorageStats`；API token `ListApiTokens`、`CreateApiToken`、`DeleteApiToken`；自描述 `GetApiReference`。
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
  optional uint64 net_rx_bps = 14;   optional uint64 net_tx_bps = 15;   // agent 自测瞬时速率，仅供实时视图
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

`probe-agent register --hub <url> --key <key> [--name <name>]` 调 `Register`，把节点 token 写入配置文件（权限 0600）。安装脚本只负责下载、校验、调用这条命令与安装服务单元，脚本里不解析 JSON。

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

管理员在面板开启注册窗口：生成一次性 key，带截止时间与可注册节点数上限。窗口关闭与 key 错误返回同一响应。失败计数按来源 IP 独立于登录失败计数：批量安装时用了过期 key 是配置失误而不是对面板的攻击，共用计数会把运维者自己锁在登录页外。只有窗口开启且 key 错误才计数；窗口关闭时没有可猜的秘密，计数只会误伤与他人共用出口地址的运维者。

`Register` 是 `AgentService` 唯一的匿名方法，因此按来源 IP 令牌桶限速（桶容量 30、每秒补充 1，超限返回 `ResourceExhausted`），与 `PublicService` 的按 IP 限流同一原则。限速在窗口裁决之前生效：窗口关闭时匿名请求也到不了写协程，否则任何人都能用几十字节的请求体让分钟刷出与 facts 落盘排在自己的事务之后。批量安装脚本遇到 `ResourceExhausted` 按退避重试即可。

### 5.3 管理员

- 单管理员。密码用 argon2id 存储，通过 `probe-hub passwd` 在 hub 主机上交互设置；没有经网络的首次设置页，也就没有"谁先访问谁占有"的窗口。
- 管理员表为空时登录一律失败。空表的语义是"无人可登录"而不是"无需认证"，由登录路径上的显式检查承载。
- 会话 token 为 32 字节随机数，库中只存 SHA-256，带绝对过期与空闲过期。cookie：`HttpOnly`、`SameSite=Strict`，`Secure` 由可信代理转发的协议决定。修改密码即清空全部会话；API token 不随之吊销（§5.6）。
- 登录失败按来源 IP 锁定。
- 跨站请求伪造由以下几条各自独立的事实约束，不指定其中哪一条是"主要防线"：会话 cookie 为 `SameSite=Strict`；hub 不下发任何 CORS 允许头；`AdminService` 不把任何方法标为无副作用（因而不接受 GET）；Connect 处理器对 `application/json` 与 `application/proto` 之外的 `Content-Type` 拒绝服务，而浏览器的跨站"简单请求"发不出这两种类型。最后一条是对 connect-go 行为的断言，列入 §13 并由 §12 的测试钉住。

### 5.4 TLS 与可信代理

hub 只监听明文 HTTP，TLS 由反代（Caddy / nginx / CDN）终止，hub 内没有证书代码。

- `--listen` 默认 `127.0.0.1:8080`。监听非 loopback 地址时启动日志告警：此时任何人都能绕过反代直连并自带转发头。
- `--timezone` 是 IANA 时区名，默认取 hub 进程的本地时区；只用于 §7 流量周期的重置日判定与面板文案，不参与任何时长计算。本地时区的名字按 `TZ`、再按 `/etc/localtime` 符号链接的目标路径里 `zoneinfo/` 之后的部分解析（在所测的 Alpine 3.21、Debian 12、Ubuntu 24.04、Rocky Linux 9 上按各自的标准方式设置时区后都是符号链接，Alpine 指向 `/etc/zoneinfo/`）；不读 `/etc/timezone`——RHEL 系没有它，Debian 与 Ubuntu 用 `timedatectl` 改时区后它仍是旧值。`/etc/localtime` 是复制出来的普通文件时（常见于 Dockerfile）取不到名字，退回 UTC 并告警。
- `--trusted-proxies` 显式给出 CIDR 列表。只有 TCP 对端地址落在列表内的请求，其 `X-Forwarded-For` / `X-Forwarded-Proto` 才被采信。空列表 = 不信任任何转发头、一律用 TCP 对端地址，是收紧方向。hub 不从请求头推断自己是否在反代之后。
- hub 不生成自己的对外地址：面板里安装命令的 hub 地址取浏览器当前的 origin（§10），所以没有 `--site-url`，也不存在从 `Host` 头推断对外地址的问题。

### 5.5 为什么不做 mTLS

mTLS 相对 bearer token 的增量是"凭据不过线"与"在 HTTP 层之前拒绝未授权连接"。代价：它要求 hub 自己终止 TLS，与 §5.4 冲突——若由反代验证客户端证书再以请求头转发身份，hub 又回到信任请求头；还需要 CA、签发、轮换、吊销整套生命周期，而注册阶段仍需一个一次性秘密换取证书。

在本项目的威胁模型下，节点 token 泄漏的后果是有人能伪造该节点的指标；hub 失守的后果受 §8.3 的 agent 侧限制约束。两者都不足以支撑上述代价。agent 强制校验服务端证书，不提供跳过校验的开关。

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

规模估算（500 节点，从常量算起）：1m 级 500 × 1440 × 7 = 504 万行；5m 级 500 × 288 × 30 = 432 万行；1h 级 500 × 24 × 365 = 438 万行；合计约 1370 万行。按每行约 120 字节估，指标三级表在 2 GB 上下，探测表另计；均未实测（见 §13）。

告警事件与投递记录（`alert_event`、`alert_delivery`）随维护任务按保留期清理，默认 90 天，由 `--retention-alert-events` 配置，下限 24 小时；抖动的节点会持续产生事件，不设保留期表会无限增长。下限必须大于满队列的最坏投递时长（队列容量 256 × 每项 3 次尝试各 10 s 超时，加 1 s 与 4 s 退避，约 2.5 小时），否则维护会删掉仍在重试的事件，通知随之丢失。事件清理不依赖上卷水位，上卷失败不阻止它；删除走 `alert_event(at)` 索引，不全表扫描。规则、渠道与状态不清理。

### 6.6 其余表

`node`（名称、排序、是否公开、备注、离线宽限期、流量重置日、token_hash）、`node_facts`（facts_hash 与各静态字段）、`traffic`、`probe_task`、`probe_task_node`、`probe_meta`（任务版本号）、`alert_rule`、`alert_rule_node`（显式作用域；`alert_rule.all_nodes` 为真时不存行且覆盖全部节点，为假时无行表示不覆盖任何节点——删除作用域里最后一个节点不会放宽到全部）、`alert_rule_channel`、`alert_state`、`alert_event`、`alert_delivery`（每事件每渠道一行投递记录；失败类别、HTTP 状态码与错误原文分列存放，见 §9.3）、`notify_channel`、`setting`、`admin`、`admin_session`、`api_token`（名称、token_hash、创建时间、最后使用时间）、`register_window`、`rollup_state`。

schema 版本记在 `PRAGMA user_version`，迁移为按版本号顺序执行的函数；空库直接建到当前版本，不重放历史。

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

agent 默认汇总除回环与虚拟网卡外的全部网卡（Linux：`lo`、`docker*`、`veth*`、`br-*`、`virbr*`；darwin：`lo*`、`gif*`、`stf*`、`utun*`、`ipsec*`、`bridge*`、`vmenet*`、`awdl*`、`llw*`、`anpi*`、`ap*`——回环，以及字节同时计在物理网口上或不出本机的接口），可用 `--net-include` / `--net-exclude` 覆盖。进程数两个平台都按进程计：Linux 数 `/proc` 下的进程目录（不用 `/proc/loadavg` 第 4 字段——那是含线程的调度实体数），darwin 用 `proc_listallpids`。

## 8. 探测

### 8.1 任务与版本

`ProbeTask{id, kind(icmp|tcp), target, interval_s, timeout_ms}`，通过 `probe_task_node` 分配到节点。经管理接口对任务或分配的任何修改都经由 `probe` 包内唯一的写入口，在同一事务内把 `probe_meta.version` 改为 `max(version + 1, 修改时刻的 Unix 秒)`：严格递增，且库从备份恢复后重做编辑得到的值大于 agent 从旧库拿到的值——收敛条件是重做时的 Unix 秒大于 agent 持有的旧版本值（同一秒内多次编辑会把版本推到秒数之上）；同秒重做或时钟回拨到该值以下仍可能碰撞，需重启 agent。agent 只比较相等与否。删除节点顺带删除它的分配行不改版本——版本号的用途是让清单变化的 agent 重取，被删节点的 token 已撤销、其余节点的清单未变。版本全局唯一而非每节点一份：修改是管理员的低频动作，全体 agent 各多取一次列表的代价可以忽略，换来的是不需要维护"哪些节点受这次修改影响"的推导。

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

## 9. 告警

### 9.1 规则

- 离线：节点超过宽限期未上报。宽限期按节点可配，下限为 TTL（§4.4），由保存规则时的显式校验承载：宽限期短于 TTL 会在面板仍显示该节点在线时发出离线告警，两处读的是同一个 `last_seen`，口径必须同向。
- 探测：某任务在某节点上的丢包率或平均 rtt 连续 N 分钟超过阈值。数据源为 `probe_1m`。

### 9.2 状态机

每（规则 × 节点）一个状态：`ok → pending → firing → ok`，进入 `firing` 发告警通知，回到 `ok` 发恢复通知。状态持久化在 `alert_state`，hub 重启不会重复触发，也不会忘记尚未恢复的告警。

离线规则每 10 秒巡检；探测规则在分钟桶刷出后评估。离线的恢复条件是收到一次上报（上报本身即证明）；探测的恢复条件是连续 1 分钟低于阈值。离线的 `pending` 是"未上报已超过 TTL 但未到宽限期"（面板已显示离线、告警尚未发出）；探测的 `pending` 是最近一分钟超阈但尚未连续 N 分钟。探测规则在某分钟没有数据时保持当前状态：缺数据既不是超阈也不是恢复。

**重启不变式**：hub 重启后 `live` 为空，所有节点看起来都未上报。对本次启动以来尚未上报过的节点，离线时长从 hub 启动时刻起算（单调钟）；已上报过的节点从 `live` 的 `last_seen` 起算。由此重启后每个节点都获得完整的宽限期，重启本身不会触发离线告警。重启前已处于 `firing` 的告警保持 `firing`，直到该节点再次上报才恢复；重启前处于 `pending` 的从启动时刻重新计时。

落盘的 `last_seen`（墙钟，随分钟行刷出与退出时写入）只用于面板显示"最后在线于"与告警文案，不参与离线时长的计算——它必然早于启动时刻，拿它与启动时刻取较大值没有意义。

### 9.3 通知

渠道：Telegram、通用 Webhook（可配方法、头、请求体模板）。投递走有界队列（单 worker），每条投递至多 3 次尝试、退避 1 s 与 4 s；每次发送前先落盘一次尝试计数，落盘失败就不发送，因此即使发送后的结果未能落盘（崩溃、写失败或关停时取消），同一条投递的发送次数也不超过上限；HTTP 4xx（除 408、429）是永久失败不重试；出站客户端不跟随重定向（3xx 当失败，凭据不随跳转外泄），响应体只读前 64 KiB。每次尝试的结果写入 `alert_delivery`；未成功且未耗尽次数的投递在 hub 重启后重新入队；运行中被挤出有界队列、或因存储故障未能记下结果的投递，由投递协程从库中补回，存储故障期间按 1 s 起、上限 60 s 的退避重试。面板显示的"已通知"只来自成功的投递记录。渠道凭据存库不回显：Telegram bot token、Webhook 的 URL（入站 webhook 的 URL 本身常是密钥）与全部头值都只写不读，列表只回显主机名与头名；保存时省略即保留旧值。hub 自己生成的出站错误文本（连接、DNS、TLS、超时、请求构造的错误）同样不含 URL；HTTP 失败的原文是接收方的响应体，见下一段。Webhook 的目标地址不做限制（含回环与内网地址），管理员因此能让 hub 向其网络可达的任意地址发请求；这是单管理员模型接受的边界，需要隔离时在网络层限制 hub 的出站。

投递失败按类别记录，类别在产生失败的地方确定，不从错误文本反推：`http_status`（接收方以非 2xx 应答，另记状态码）、`transport`（没有收到合法应答：连接、DNS、TLS、超时，以及状态码不在 100–999 的应答）、`request`（请求没能构造：模板执行、编码、URL）、`channel_invalid`（渠道配置无法解析）、`channel_deleted`（渠道已删除，投递终止）、`result_unrecorded`（次数耗尽而最后一次结果未落盘）、`unclassified`（失败没有携带类别；迁移时无法从旧记录确定类别的也归入此类）。只读口径的 `ListAlertEvents` 只返回类别与状态码；错误原文——HTTP 失败时是响应体的前 200 个字符，其余是出站错误文本——只经仅会话的 `GetAlertDeliveryError` 读出：接收方可能在错误响应里回显收到的请求体，而请求体模板里可能放着密钥（§3.2）。hub 不把 URL 写进原文，但响应体的内容由接收方决定：接收方若回显请求路径或头值，会话用户也能从原文里看到这些本应只写不读的配置，这在会话的权限之内。`channel_deleted` 与 `result_unrecorded` 没有原文；`result_unrecorded` 不沿用更早一次尝试的失败——最后一次尝试已发出而结果未知，接收方可能已经收到。投递成功时类别、状态码与原文一并清空。

## 10. 前端与公开页

- `web/` 内两个 Vite 入口：`/admin/*` 管理面板，`/` 公开页，各自打包。lint 规则禁止公开页入口引用 `AdminService` 的生成客户端；这是卫生措施，安全边界在 §3.2 的服务端挂载。
- 实时数据用轮询（默认 2 秒）。`PublicService.GetSnapshot` 一次返回全部公开节点的实时状态，hub 对序列化结果缓存 1 秒：匿名访客数量不影响 hub 的序列化开销。
- `--public-dir <dir>` 用指定静态目录替代内置公开页，未命中文件时回落到该目录的 `index.html`。`/admin` 与 RPC 路径的路由优先级更高，替换目录无法遮蔽它们。文件访问经 `os.Root`，不可越出目录、不跟随指向目录外的符号链接。
- 外观设置（明暗、主色、logo、标题、自定义 CSS）存于 `setting`，经 `PublicService.GetSite` 下发并以 CSS 变量应用。只接受 CSS，不接受 JS 或 HTML；需要改结构的人使用 `--public-dir`。
- 管理面板的 API token 页：列表显示名称、创建时间、最后使用时间；创建后明文只显示一次；删除需确认；可下载入口卡片（§5.6）。
- 注册窗口开启后，面板在 key 旁给出一行安装命令（curl 与 wget 各一条）。hub 地址取浏览器当前的 origin，并注明 agent 若经另一地址访问 hub 需替换；hub 为正式版本（`hub_version` 是带 `v` 前缀的合法 semver，与节点落后判定用同一个解析）时，脚本取自该版本的 release、命令带 `--version <hub_version>`（经 `GetSnapshotResponse.hub_version` 下发），装上的 agent 与 hub 同版本；开发构建取最新 release 的脚本、不带 `--version`，并提示将安装最新 release。命令区域在 `hub_version` 到达之前不渲染。
- 未构建前端时 hub 照常编译与启动，页面路径返回"前端未构建"的说明；`go build` 与 `go test` 不依赖 Node。

公开页与 `PublicService` 的细节：

- 内容：总览是节点卡片（名称、在线、系统与架构、CPU、内存、磁盘、网速、运行时长、本周期流量），节点页是历史图表（指标与探测，时间范围选择与面板同一组件）。图表组件与面板共用；公开入口不得引用 `AdminService` 的生成代码，由测试扫描公开入口的 import 钉住。
- 消息：`PublicSnapshot`（`now`、`report_interval_ms`、`nodes`）；`PublicNode`（id、名称、在线、最近上报、排序、`PublicFacts`、`PublicMetrics`、`Traffic`）。`PublicFacts` 只有系统、架构、CPU 型号、核数、虚拟化——不给主机名、内核版本、agent 版本、ICMP 可用性；`PublicMetrics` 与 `Metrics` 同字段但没有 `boot_id`。`QueryMetrics` 与 `QueryProbes` 复用管理端的请求与响应类型。把节点标为公开即公开它的探测目标：`QueryProbes` 返回任务的种类与目标，否则图表无从辨认。
- 限流：按来源 IP 令牌桶，桶容量 60、每秒补充 10，超限 `ResourceExhausted`，与 `Register` 的限速同一实现（§5.2）。
- 缓存：`GetSnapshot` 的序列化结果按编码（codec、压缩）缓存 1 秒，缓存的是响应字节而不是消息。GET 响应的 `Cache-Control`：快照 `max-age=1`、历史查询 `max-age=60`、站点配置 `max-age=300`；POST 响应不带缓存头。
- 设置：`setting` 表是键值表；`GetSettings` 为只读口径、`UpdateSettings` 仅会话。字段与上限：标题不超过 64 个字符；明暗为 `auto`、`light`、`dark` 之一；主色为 `#rrggbb`；logo 为 `data:` URL，图片类型限 png、jpeg、webp、svg，不超过 128 KiB；自定义 CSS 不超过 64 KiB，含 `</` 即拒绝（它能跳出注入点的 `<style>`）。校验错误写明字段、违反的约束与期望取值。`GetSite` 下发这五项，公开页以 CSS 变量应用，自定义 CSS 放在其后。
- 静态服务：内置公开页与面板用同一套 CSP；`--public-dir` 只加 `X-Content-Type-Options: nosniff` 与 `frame-ancestors 'none'`，不限制脚本与外部资源——目录由运维放置，严格 CSP 会让第三方主题的字体与图片失效。两者都不列目录；`assets/` 下未命中返回 404，其余回落 `index.html`；自定义目录一律 `no-cache`。未构建前端时 `/` 与面板一样返回"前端未构建"的说明。
- `GetStorageStats`（只读口径）返回库文件大小与各表行数，与 `probe-hub stats` 同一来源。

第三方主题 = 调 `PublicService` 的静态站点，框架自选；Connect unary 即 HTTP POST + JSON，直接 `fetch` 可用。

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
  - 从生成的服务描述符枚举全部 RPC，逐个无凭据调用，断言 `Unauthenticated`；仅 `PublicService` 的方法在白名单内。
  - 遍历全部 `Public*` 消息的字段，与测试内的显式允许列表比对；往公开消息加字段必须同时改这份列表。
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
- 发布：`make release VERSION=vX.Y.Z` 把全部产物生成到 `dist/`；推送 `v*` tag 时 CI 调用同一目标并建 GitHub Release。构建逻辑只在 Makefile 一处，本地验收与线上发布用的是同一套产物。Linux agent 每个架构一个 `probe-agent_linux_<arch>.tar.gz`（二进制、systemd 单元、OpenRC 服务脚本），hub 每个架构一个 `probe-hub_linux_<arch>.tar.gz`，另有 `SHA256SUMS` 与 `install.sh`。资产名不带版本号：`releases/latest/download/<名>` 与 `releases/download/<tag>/<名>` 都能直接拼出，脚本不必调用 GitHub API。版本号经 `-ldflags -X main.version` 注入，构建带 `-trimpath`，二进制里不含构建机的路径。tag 带预发布后缀（semver 的 `-` 部分）时建为 prerelease，不成为 latest：开发构建的面板命令与不带 `--version` 的安装都取 latest；预发布的判定只写在 Makefile 一处，GitHub Release 与镜像的 `latest` 都读它。当前只出 Linux 产物，darwin agent 与 hub 的 Docker 镜像随 M6 加入（§15）。服务定义在仓库 `deploy/` 下是真实文件，打包时原样放入，不在脚本里以字符串另存一份。仓库必须公开：私有仓库的 Release 资产要鉴权才能下载。
- Linux 安装脚本（`install.sh`，以 root 运行）的步骤顺序：检测 init 系统与架构（卸载不看架构）→ 创建固定的系统用户 `probe-agent` → 检查下载器（curl 或 wget）与 `sha256sum`，缺了在任何网络操作之前报错 → 确保 CA 证书 → 下载 tar 包与 `SHA256SUMS` 并校验 → 解包并确认三个文件齐全 → 写同目录的临时二进制 → 停止已安装的服务并确认（见下条）→ `mv` 替换二进制 → 没有配置时 `probe-agent register` → 让服务用户能读配置：目录保持 root 属主、组为 `probe-agent`、0750，文件交给该用户、0600（register 以 root 写入，不改属主服务就读不到；目录不交给服务用户，服务用户就不能替换其中的目录项，root 对它的后续操作不会被链接或竞态劫持；agent 运行时不写配置目录）。这一步每次安装都做，不只在注册之后：手工重新注册、注册后被打断、账户被删后重建，都会留下服务用户读不到的配置 → 安装服务定义并启动（systemd 单元或 OpenRC 服务，要求见下两条）→ 确认服务起来了：有上限地等到出现以服务用户运行的进程，3 秒后同一个进程仍在，否则失败退出并指出日志位置。init 的 start 返回 0 证明不了子进程活着（OpenRC 的情形见下；systemd 的 `Type=simple` 在 fork 之后即视为已启动），不确认就会出现脚本报成功、节点却不上线。判据与停服务之后的确认是同一个（有效 uid 为服务用户的进程）。建用户排在下载与注册之前：它若失败，注册窗口的名额尚未消耗、旧服务尚未停止。参数：`--hub`、`--key`、`--name`、`--version`（默认最新 release）、`--base-url`（覆盖下载目录，用于镜像与本地验收）。给了 `--base-url` 时它就是下载目录，`--version` 不再参与下载地址；重跑沿用现有配置时 `--name` 不生效。两种情形与 `--key` 被忽略时一样给出提示。
- 重跑即升级：已有配置时跳过注册，`--hub` 与 `--key` 可省；同时给了 `--key` 时明确提示沿用现有注册，不重新注册——重新注册会多消耗一个窗口名额，并在 hub 上多出一个节点。替换之前先确认包完整（校验、解包、三个文件齐全），再停止已安装的服务——是否需要发 stop 看服务定义是否已安装，不看配置是否存在；发出之后无条件确认没有以服务用户运行的进程（扫描 `/proc/<pid>/status` 的有效 uid，有上限地等待），不论服务定义在不在：OpenRC 0.55.1 上 `rc-service stop` 在所测的各状态下都返回 0，supervisor 被杀而子进程留存时 `status` 也报 stopped，二者都证明不了进程已退出；单元或 init 脚本被手工删掉而进程仍在时也只有这一步抓得到。确认不通过即失败退出，不能在旧进程仍在运行时报告安装成功。二进制先写同目录临时文件再 `mv` 替换；服务定义每次覆盖，单元的改动随升级下发。`--uninstall` 停止并禁用服务（停不下来即非零退出）、删除二进制与服务定义，保留配置与用户；`--purge` 必须与 `--uninstall` 同时给出；加 `--purge` 一并删除 `/etc/probe-agent`、日志目录、用户与同名组，并回查二者都已不存在，删不掉即非零退出。以 `curl … | sh -s --` 运行时脚本来自 stdin，脚本里每个可能读 stdin 的外部命令都显式 `</dev/null`（不能 `exec </dev/null`，那会切断脚本自己的来源）。
- systemd 单元使用静态 `User=` 并加固（`NoNewPrivileges=`、`ProtectSystem=strict` 等），默认带 `AmbientCapabilities=CAP_NET_RAW` 与 `CapabilityBoundingSet=CAP_NET_RAW`：裸机 Debian 的 `ping_group_range` 默认关闭（§13 第 2 项），没有这项能力时 ICMP 探测只能回报 error。用户由安装脚本创建并拥有配置文件，OpenRC 侧以同一用户运行，单元用静态 `User=` 并逐项写出加固，不经 `DynamicUser=` 隐式引入；`DynamicUser=` 会隐含的 `RestrictSUIDSGID=` 在单元里明写。monitor 提交 `85f6702` 记录过未开 nesting 的 LXC 容器里 `DynamicUser=` 的单元拒绝启动（226/NAMESPACE）；2026-09-26 在 Debian 12 bookworm-backports 的 Incus、容器内 systemd 252（252.39-1~deb12u2）、`security.nesting=false` 的容器里实测，静态 `User=` 与 `DynamicUser=` 的单元都能启动，该记录在此环境不成立；它出自哪种 LXC 配置未定位，不作为选型理由。采集读不到某个文件时只让对应字段缺失并记日志，上报照常，因而加固项若遮蔽了采集要读的 `/proc`、`/sys` 路径不会以失败显形；由 §12 的真机对照验收承载。
- init 系统支持 systemd 与 OpenRC。判定：`/run/systemd/system` 存在为 systemd；否则 `/sbin/openrc-run` 存在且可执行为 OpenRC（`/run/openrc` 表示已启动）；两者都不是时安装脚本报错并列出支持的 init，不静默降级（容器里两者通常都不存在）。OpenRC 服务用 `supervisor=supervise-daemon`、`command_user` 为固定系统用户、`capabilities="^cap_net_raw"`，与 systemd 的 `AmbientCapabilities` 等价（2026-09-25 在 Alpine 3.21 / OpenRC 0.55.1 真机上实测：ping_group_range 关闭时授予该能力 ICMP 可用，去掉即不可用）。`output_log` 与 `error_log` 的文件必须在启动前建好并交给运行用户，由服务脚本的 `start_pre` 建立，只把这两个文件交给运行用户，日志目录保持 root 属主、不递归改属主（服务用户能增删目录项时，root 按路径做的改属主之类的操作，对象可以被它换成别的文件；所以 root 不在服务用户控制的目录里操作），每次启动都成立而不只在安装时建一次：supervise-daemon 降权后才打开它们，打不开时子进程秒退、反复拉起，而 `rc-service status` 仍显示 started。agent 异常退出后两种 init 都无限次重启、每次间隔 5 秒：systemd 用 `Restart=on-failure`、`RestartSec=5` 与 `StartLimitIntervalSec=0`（默认 100 ms 间隔加 10 秒内 5 次的上限，会让一次短暂故障把单元永久留在 failed）；OpenRC 用 supervise-daemon 的对应设置，取值以实测为准。
- 安装脚本用 POSIX sh，兼容 busybox，按实际存在的工具分支，不假设任何单一工具（所测的基础容器镜像与机器镜像之间、以及各发行版之间，工具集都不相同）：先建同名组（`groupadd --system`，没有时 busybox 的 `addgroup -S` 或 Debian 的 `addgroup --system`），再以它为主组建用户：优先 `useradd --system -g`，没有时 busybox 的 `adduser -S -D -H -G`、Debian 的 `adduser --system --no-create-home --ingroup`（busybox 的 adduser 不指定组时会把用户放进 `nogroup`，而 OpenRC 的 `command_user` 与配置文件属主都写 `probe-agent`），建完一律回查主组 `id -gn`，不信退出码——实测 Debian 的 perl 版 adduser 收到 busybox 风格的参数时打印用法、不建用户，却返回 0。下载用 curl 或 wget，按实际存在的那个走（所测基础容器里 Alpine 只有 wget、Rocky 只有 curl、Debian 与 Ubuntu 两者都没有；所测机器镜像里四个都有 curl），两者都没有就报错说明依赖；下载地址为 https 时 curl 限定请求与重定向都只走 https（wget 没有对应开关）。能力授予交给 init，不依赖 setcap（所测的 Alpine 与 Debian 机器镜像、以及 Alpine、Debian、Ubuntu 基础容器里都没有）。所测的 Debian 与 Ubuntu 基础容器不带 CA 证书（机器镜像都带），CA 是否存在按文件探测而不是按发行版名判断；缺失时安装脚本先装 `ca-certificates`，判据是下载地址或 hub 地址任一为 https——默认下载地址就是 https，只看 hub 地址会让下载先失败。
- macOS agent：采集在 `CGO_ENABLED=0` 下实现（§13 第 1 项的实验定案）：`x/sys/unix` 的 sysctl 取启动标识（`kern.bootsessionuuid`）、内存总量、负载、swap、连接数（`net.inet.tcp.pcbcount`/`net.inet.udp.pcbcount`）、网卡计数器（`net.link.generic.ifdata`）与 facts；`statfs` 取磁盘；`clock_gettime(CLOCK_MONOTONIC)` 取运行时长；purego 调 libSystem 取逐 CPU tick、VM 统计、页大小与进程数。采集分层为平台取原始读数的 `Host` 与平台无关的差分、过滤与 `used ≤ total` 检查；darwin 的字节布局解析不带 build tag、Linux 上可测，只有系统调用层带 darwin 约束并引用 purego，其依赖不链入 Linux 二进制。darwin 二进制按平台约定动态链接 libSystem，静态门禁只查 Linux 产物。产物 `probe-agent_darwin_<arch>.tar.gz`（二进制、launchd plist）。安装脚本 `install-macos.sh` 以 root 运行：检测架构 → 用 `dscl` 建 `_probe-agent` 用户与组（uid/gid 从 499 向下取空闲号：Apple 逐版从 300 向上追加系统账户，升级会替换低号段的第三方账户）→ 下载并用 `shasum -a 256` 校验 → 停止已装的 LaunchDaemon 并确认进程退出 → 替换二进制 → 没有配置时 `probe-agent register` → 配置属主同 Linux（`/etc/probe-agent`，目录 root 属主、组 `_probe-agent`、0750，文件 0600）→ 写 `/Library/LaunchDaemons/xyz.probe.agent.plist`（`UserName` 为该用户、`KeepAlive`、`ThrottleInterval` 5 秒——两次拉起的最小间隔，进程跑满 5 秒后退出会立即拉起，`KeepAlive` 为真时 0 退出也拉起，与 Linux 的重启间隔同量级、日志在 `/Library/Logs/probe-agent/`）→ `launchctl enable` 后 `bootstrap system`（enable 清掉可能残留的禁用覆盖）→ 确认进程活着（`ps -axo uid=,pid=,comm=` 按有效 uid 与可执行路径，macOS 没有 /proc；只按 uid 不够：launchd 会以该 uid 派生 cfprefsd、trustd 之类的辅助进程，停止确认与启动确认都要把它们排除）。是否发 `bootout` 看作业是否已载入 system 域（`launchctl print`），不看 plist 文件在不在：launchd 按已载入的作业管进程。架构按 `hw.optional.arm64` 判定再看 `uname -m`（Rosetta 终端里 `uname -m` 报 x86_64）。不装 CA：macOS 自带 curl 与系统信任库。重跑即升级，`--uninstall`、`--purge` 语义同 Linux。没有 macOS 虚拟机可用：脚本逻辑用桩测试，真机验收由人在 Mac 上执行，脚本随附检查清单。面板的安装命令只给 Linux 的两条，macOS 的写在 README。CI 含 macOS runner 跑 agent 的测试。
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

## 16. 参考项目

`reference-projects/` 下有两个只读参考（不入库）。

沿用自 monitor（`src/agent_ws.rs`、`src/db.rs`）：在线与最新数据同一事实；hub 侧用 `boot_id` + 内核计数器做流量差分且"无读数 ≠ 0"；历史行是整桶聚合而非边界瞬时采样；时长用单调钟；限时限量且失败计数独立的注册窗口；主键顺序按查询路径排；非法上报不改动已有状态。

有意不同于 monitor：token 存 hash 而非明文；单仓库共享协议类型而非两仓库靠运行时契约检查；不从请求头推断部署形态；主题托管在与面板不同的 origin 且产物入库，而非同源落盘；无计费字段、无 GeoIP 外呼、不托管 agent 二进制。

规避自 komari：token 经 URL 传递且有三个读取位置；WebSocket 与 HTTP 两套在线状态并存；远程执行 / 终端 / 文件管理；内嵌 JS 引擎的插件系统；三方言自研时序层。
