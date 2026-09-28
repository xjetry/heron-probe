---
name: probe-hub
description: 查询自托管探针 hub 的节点、实时状态、历史指标、流量、探测、费用与到期，以及告警事件。用户问起服务器在不在线、负载、流量、延迟、费用、何时到期或告警时使用。
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
- 时间是 Unix 秒；字段名以 `_ms` 结尾的是毫秒，以 `_s` 结尾的是秒，以 `_us` 结尾的是微秒。JSON 字段名是 proto 字段名的小驼峰（`last_seen_at` → `lastSeenAt`）。
- 列表为空时字段不出现，jq 里取列表写 `(.字段 // [])`。
- 非 `optional` 的字段取默认值（0、空串、false、空列表）时在 JSON 里省略，读不到就按默认值理解（样本 `{}` 的 `n` 是 0）。
- `optional` 字段只要有值就出现，哪怕是 0 或 false；缺席含义见字段注释。读数（如 `lastSeenAt` 与指标样本的 `mean`、`max`、`sum`）缺席表示没有读数，不是 0；设置的 `publicEnabled` 在更新时缺席表示不变。
- 出错时 HTTP 状态非 200，响应体是 `{"code": "...", "message": "..."}`；message 写明哪个字段、违反了什么约束、期望什么取值。

## 例子

全部节点与最近一次上报时刻：

```sh example
curl -fsS -H "Authorization: Bearer $PROBE_TOKEN" -H 'Content-Type: application/json' \
  --data '{}' "$PROBE_HUB/probe.v1.AdminService/ListNodes" | jq '[(.nodes // [])[] | {id, name, lastSeenAt}]'
```

第一个节点最近一小时的 CPU（百分比；每点有样本数、均值与最大值，`ts` 与 `samples` 一一对应）：

```sh example
nodes=$(curl -fsS -H "Authorization: Bearer $PROBE_TOKEN" -H 'Content-Type: application/json' \
  --data '{}' "$PROBE_HUB/probe.v1.AdminService/ListNodes")
node=$(printf '%s' "$nodes" | jq -r '(.nodes // [])[0].id // empty')
if [ -z "$node" ]; then
  echo '{}'
else
  now=$(date +%s)
  curl -fsS -H "Authorization: Bearer $PROBE_TOKEN" -H 'Content-Type: application/json' \
    --data "$(jq -nc --arg node "$node" --argjson now "$now" '{nodeId: $node, from: ($now - 3600), to: $now}')" \
    "$PROBE_HUB/probe.v1.AdminService/QueryMetrics" | jq '{stepS, ts: (.ts // []), cpu: [(.series // [])[] | select(.name == "cpu") | (.samples // [])[]]}'
fi
```

节点的费用与到期。`daysLeft` 是到期日减去今天的天数，今天按 hub 的时区（`--timezone`）取日历日，负数是已过期的天数，由 hub 算好下发；没有到期日（或库里的到期日无法解析）时它缺失，这样的节点不列出。价格与币种是记录用的展示值，hub 不汇总、不换算：

```sh example
curl -fsS -H "Authorization: Bearer $PROBE_TOKEN" -H 'Content-Type: application/json' \
  --data '{}' "$PROBE_HUB/probe.v1.AdminService/ListNodes" | jq '[(.nodes // [])[] | select(.billing.daysLeft != null) | {name, price: .billing.price, currency: .billing.currency, expiresOn: .billing.expiresOn, daysLeft: .billing.daysLeft}]'
```

最近 20 条告警事件：

```sh example
curl -fsS -H "Authorization: Bearer $PROBE_TOKEN" -H 'Content-Type: application/json' \
  --data '{"limit": 20}' "$PROBE_HUB/probe.v1.AdminService/ListAlertEvents" | jq '.events // []'
```

## 公开数据

总闸 `Settings.public_enabled` 是 `optional bool`：`GetSettings` 总是带值，未保存时为 true；`UpdateSettings` 中缺席表示不变，显式 true/false 才修改。其它外观字段仍整体替换，旧客户端修改标题等外观不会顺带改变总闸。

标为公开的节点在 `Settings.public_enabled` 开启时另经 `PublicService` 对外提供，不需要 token：只能查到公开节点，未公开与不存在的节点得到同一个 `not_found`。总闸从未保存过时为开；关闭后全部公开方法（含 `GetSite`）返回 `not_found`，快照仍可能在 1 秒内命中字节缓存，节点的公开标记不变。按来源限流（IPv4 一个地址、IPv6 一个 /64 算一个来源），每个来源瞬时 60 次、此后每秒 10 次，超出返回 `resource_exhausted`，关闭后仍计数。方法与字段见 `probe/v1/public.proto`，都可以用 GET 调用，请求消息放在查询串里。

公开节点的实时状态：

```sh example
curl -fsS -G --data-urlencode 'connect=v1' --data-urlencode 'encoding=json' --data-urlencode 'message={}' \
  "$PROBE_HUB/probe.v1.PublicService/GetSnapshot" | jq '[(.nodes // [])[] | {id, name, online}]'
```
