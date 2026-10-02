---
name: heron-hub
description: 查询自托管探针 hub 的节点、指标、流量、探测、费用与告警，并在预授权范围内修改监控配置、创建和注册节点、轮换凭据、删除节点或更新官方 agent。用户要求诊断或管理服务器监控时使用。
---

# Heron hub

hub 的管理接口是 Connect unary：每个方法都是 `POST $HERON_HUB/heron.v1.AdminService/<方法>`，请求体与响应体都是 JSON。不需要生成客户端，curl 加 jq 即可。

## 进门

- `HERON_HUB`：hub 的对外地址，如 `https://heron.example.com`，不带末尾斜杠。
- `HERON_TOKEN`：在面板"API token"页创建的 token，形如 `heron_at_` 加 64 位十六进制。默认全站只读；管理员可预授权配置、创建、注册、轮换、删除、官方节点更新，并选择全站或指定节点。写入经 `ExecuteChange`，不能直接调用仅限会话的方法。
- 每个请求带两个头：`Authorization: Bearer $HERON_TOKEN` 与 `Content-Type: application/json`。
- `ListSessions` 与 `RevokeSession` 也仅限会话 cookie，API token 不可用；即使获得写权限，也不能枚举、创建或撤销 API 凭据及会话。
- `GetUpdates` 查询范围内节点的在线更新能力和任务；全站 token 还可读 Hub 状态。`checkLatest: true` 显式查询官方最新正式版。具备更新权限时，经 `ExecuteChange.startUpdate` / `cancelUpdate` 操作正数 nodeId 的官方更新。Hub 更新仍仅限会话。

## 预授权写入

1. 用 `ListNodes`、`ListProbeTasks`、`ListAlertRules` 读取授权范围；用 `ListNotifyChannelRefs` 取得渠道 id、名称、类型，不读地址、模板或密钥。指定节点凭据不能调用全站设置、备份与存储统计。
2. 调 `ExecuteChange`，给出一种 change 和 `preview: true`。修改已有配置需 `updateMask`，节点路径如 `note,tags`，探测路径如 `task.target`，告警路径如 `rule.enabled`。JSON FieldMask 是逗号分隔的 lowerCamelCase 字符串，例如 `"updateMask":"note,trafficResetDay"`，不是 paths 对象。
3. 查看 `operation.beforeJson` / `afterJson`。执行相同变更，设 `preview: false`，附上预览返回的 `expectedVersion` 和唯一 `requestId`。版本过时返回 `aborted`，重新读取和预览，不盲目覆盖。
4. 响应丢失时原样重试同一 requestId；同键不同请求返回 `already_exists`。已提交则 `replayed: true`，不会重复执行。`ListOperations` 按 requestId 查询回执，`operation.id` 是跨重试和恢复稳定的回执标识；已提交节点更新不等于已安装，实际状态用 `GetUpdates` 查询。

首次执行的 `result` 是 protobuf Any，HTTP JSON 带 `@type` 和原业务响应字段。创建节点、轮换凭据、打开注册窗口的秘密仅在首次响应出现，不落审计，不在重试中重放。`createNode` 与 `updateNode.billing` 一样接受 `billing`（价格、币种、周期、到期日、自动续期，同一处校验）：看到价格与到期信息时一步建档，不必建完再改。丢失秘密后查询回执，再用新 requestId 显式轮换节点凭据或重开自己的注册窗口；不要重复创建节点。

`ExecuteChange.createNode` 与 `rotateNodeToken` 的 `result.token` 是带 `heron_install_` 前缀的一次性安装凭据，不是运行 token，也不是管理接口的 `HERON_TOKEN`。在目标主机用 `heron-agent register --hub URL --key KEY --config PATH` 注册；它向 `POST /heron.v1.AgentService/Register` 发送 `{"key":"安装凭据"}`，用返回的运行 `token` 写入配置。直接调用 Register 时由请求体 key 授权，不需要管理 bearer。认领保留节点 ID、名称和公开范围，不消费窗口名额；安装凭据不能 Report，运行 token 只能作为 Report 的 bearer，不能再次 Register。

换发会撤销旧凭据。已安装主机用同版本安装脚本的 `--re-register --hub URL --key KEY` 显式替换注册，保留本地探测策略；普通升级不消费 key。注册成功且配置已写入、但后续安装失败时，去掉 `--re-register` 按普通升级重跑；Register 响应丢失或配置未能写入时，重新换发安装凭据，不重建节点，也不重复使用已消费的凭据。

指定节点授权同时约束读写；普通标签不扩权。显式包含未授权节点、全站和动态标签规则不能由指定节点凭据编辑。创建和经自己注册窗口接入的节点自动纳入范围，不增加操作权限。注册窗口互不覆盖；吊销凭据会关闭其窗口，并阻止尚未下发的更新。已下发更新不能据此撤回。

详细差异在后续写入时清理超过 90 天的记录，幂等回执永久保留。配置备份保存授权与回执，恢复时合并回执历史，并清除所有注册窗口和节点更新任务。回执描述原事务，不保证当前配置仍未变化。

例如只修改节点备注：

```json
{"preview":true,"updateMask":"note","updateNode":{"id":"42","note":"维护窗口：周六"}}
```

执行时保留同样的 `updateMask` 与 `updateNode`，加上 `requestId`、`expectedVersion`，去掉 `preview` 或设为 false。不开放远程命令、云主机管理、Hub 升级、管理员管理或通知密钥修改。

## 取 schema

方法、字段与语义都写在 proto 注释里。取与 hub 同版本的全部 proto：

```sh example
curl -fsS -H "Authorization: Bearer $HERON_TOKEN" -H 'Content-Type: application/json' \
  --data '{}' "$HERON_HUB/heron.v1.AdminService/GetApiReference" | jq '[.files[].path]'
```

看某个文件：把上面的 jq 换成 `jq -r '.files[] | select(.path == "heron/v1/admin.proto") | .content'`。每个 rpc 上的 `option (heron.v1.access)` 标明准入策略：`ACCESS_READ` 允许读取（仍受节点范围限制），`ACCESS_CHANGE` 经预授权写入；`ACCESS_SESSION` 不接受 API token。

## 约定

- int64 与 uint64 在 JSON 里是字符串（`"id": "3"`）；请求里写数字或字符串都可以。
- 时间是 Unix 秒；字段名以 `_ms` 结尾的是毫秒，以 `_s` 结尾的是秒，以 `_us` 结尾的是微秒。JSON 字段名是 proto 字段名的小驼峰（`last_seen_at` → `lastSeenAt`）。
- 列表为空时字段不出现，jq 里取列表写 `(.字段 // [])`。
- 非 `optional` 的字段取默认值（0、空串、false、空列表）时在 JSON 里省略，读不到就按默认值理解（样本 `{}` 的 `n` 是 0）。
- `optional` 字段只要有值就出现，哪怕是 0 或 false；缺席含义见字段注释。读数（如 `lastSeenAt` 与指标样本的 `mean`、`max`、`sum`）缺席表示没有读数，不是 0；设置的 `publicEnabled` 在更新时缺席表示不变。
- 出错时 HTTP 状态非 200，响应体是 `{"code": "...", "message": "..."}`；message 写明哪个字段、违反了什么约束、期望什么取值。

## 例子

历史网络速率有两种口径：`rx_bytes` / `tx_bytes` 的 `sum / stepS` 是桶均值；`net_rx_bps` / `net_tx_bps` 的 `max` 是 agent 本地采样速率峰值（bytes/s）。`n=0` 或缺少速率序列表示没有读数，不能补零或拿均值代替峰值。CPU 与内存的 `max` 同样表示采样峰值。

`ListProbeTasks` 与 `QueryProbes` 返回展示顺序，已删除任务的历史排在最后，按编号升序。`ReorderProbeTasks` 只接受完整任务 ID 排列且仅允许会话调用，不改变 agent 的执行配置版本。付款周期枚举为月、季、半年、年、两年、三年、五年；五年对应 `BILLING_CYCLE_QUINQUENNIAL`。

全部节点与最近一次上报时刻：

```sh example
curl -fsS -H "Authorization: Bearer $HERON_TOKEN" -H 'Content-Type: application/json' \
  --data '{}' "$HERON_HUB/heron.v1.AdminService/ListNodes" | jq '[(.nodes // [])[] | {id, name, lastSeenAt}]'
```

第一个节点最近一小时的 CPU（百分比；每点有样本数、均值与最大值，`ts` 与 `samples` 一一对应）：

```sh example
nodes=$(curl -fsS -H "Authorization: Bearer $HERON_TOKEN" -H 'Content-Type: application/json' \
  --data '{}' "$HERON_HUB/heron.v1.AdminService/ListNodes")
node=$(printf '%s' "$nodes" | jq -r '(.nodes // [])[0].id // empty')
if [ -z "$node" ]; then
  echo '{}'
else
  now=$(date +%s)
  curl -fsS -H "Authorization: Bearer $HERON_TOKEN" -H 'Content-Type: application/json' \
    --data "$(jq -nc --arg node "$node" --argjson now "$now" '{nodeId: $node, from: ($now - 3600), to: $now}')" \
    "$HERON_HUB/heron.v1.AdminService/QueryMetrics" | jq '{stepS, ts: (.ts // []), cpu: [(.series // [])[] | select(.name == "cpu") | (.samples // [])[]]}'
fi
```

节点的费用与到期。`daysLeft` 是到期日减去今天的天数，今天按 hub 的时区（`--timezone`）取日历日，负数是已过期的天数，由 hub 算好下发；没有到期日（或库里的到期日无法解析）时它缺失，这样的节点不列出。价格与币种是记录用的展示值，hub 不汇总、不换算：

```sh example
curl -fsS -H "Authorization: Bearer $HERON_TOKEN" -H 'Content-Type: application/json' \
  --data '{}' "$HERON_HUB/heron.v1.AdminService/ListNodes" | jq '[(.nodes // [])[] | select(.billing.daysLeft != null) | {name, price: .billing.price, currency: .billing.currency, expiresOn: .billing.expiresOn, daysLeft: .billing.daysLeft}]'
```

最近 20 条告警事件（含 `rule_id`、`node_id` 为 0 的系统事件：`transition` 为 `login_success`、`login_locked` 的登录通知与 `backup_failed`、`backup_recovered`、`backup_disabled` 的配置层备份；规则事件为 `firing`、`recovered`）：

```sh example
curl -fsS -H "Authorization: Bearer $HERON_TOKEN" -H 'Content-Type: application/json' \
  --data '{"limit": 20}' "$HERON_HUB/heron.v1.AdminService/ListAlertEvents" | jq '.events // []'
```

## 公开数据

总闸 `Settings.public_enabled` 是 `optional bool`：`GetSettings` 总是带值，未保存时为 true。`UpdateSettings` 按组判定、各组彼此独立：外观五项（`title`、`theme`、`accent_color`、`logo`、`custom_css`）是一组，任一项非空即视为给出，给出就整体替换（`theme` 必填，其余为空即清空）；总闸 `public_enabled`、国家查询、备份 `backup` 与登录通知 `login_notify` 各自是一组，显式给出才修改，缺席表示不变：国家查询两项（`geo_enabled`、`geo_url`）合为一组，逐项按 presence 判定，给出任一项即算这一组给出、只改给出的那项；`backup` 内各项的 presence 见 `BackupSettings`；`login_notify` 给出空的 `channel_ids` 即关闭登录通知，响应里总带它。两个渠道列表（`backup.notify.channel_ids`、`login_notify.channel_ids`）各至多 16 条（按原始条数计，含重复），每个 ID 必须是存在的渠道。`geo_backend`、`geo_mmdb_path` 是只读回显，请求里给出也被忽略，不算给出任何一组。一组都没给出返回 `invalid_argument` 并点名各组。所以只开关公开页的请求只带 `public_enabled` 即可，旧客户端修改标题等外观也不会顺带改变总闸。

标为公开的节点在 `Settings.public_enabled` 开启时另经 `PublicService` 对外提供，不需要 token：只能查到公开节点，未公开与不存在的节点得到同一个 `not_found`。总闸从未保存过时为开；关闭后全部公开方法（含 `GetSite`）返回 `not_found`，快照仍可能在 1 秒内命中字节缓存，节点的公开标记不变。按来源限流（IPv4 一个地址、IPv6 一个 /64 算一个来源），每个来源瞬时 60 次、此后每秒 10 次，超出返回 `resource_exhausted`，关闭后仍计数。方法与字段见 `heron/v1/public.proto`，都可以用 GET 调用，请求消息放在查询串里。

公开节点的实时状态：

```sh example
curl -fsS -G --data-urlencode 'connect=v1' --data-urlencode 'encoding=json' --data-urlencode 'message={}' \
  "$HERON_HUB/heron.v1.PublicService/GetSnapshot" | jq '[(.nodes // [])[] | {id, name, online}]'
```
