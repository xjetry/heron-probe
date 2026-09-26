---
name: probe-hub
description: 查询自托管探针 hub 的节点、实时状态、历史指标、流量、探测与告警事件。用户问起服务器在不在线、负载、流量、延迟或告警时使用。
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
- `optional` 字段（proto 里标了 `optional` 的，如 `lastSeenAt` 与指标样本的 `mean`、`max`、`sum`）只要有值就出现，哪怕是 0；缺失才表示没有读数。不要把缺失当成 0，也不要把出现的 0 当成缺失。
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

最近 20 条告警事件：

```sh example
curl -fsS -H "Authorization: Bearer $PROBE_TOKEN" -H 'Content-Type: application/json' \
  --data '{"limit": 20}' "$PROBE_HUB/probe.v1.AdminService/ListAlertEvents" | jq '.events // []'
```

## 公开数据

标为公开的节点另经 `PublicService` 对外提供，不需要 token：只能查到公开节点，未公开与不存在的节点得到同一个 `not_found`。按来源限流（IPv4 一个地址、IPv6 一个 /64 算一个来源），每个来源瞬时 60 次、此后每秒 10 次，超出返回 `resource_exhausted`。方法与字段见 `probe/v1/public.proto`，都可以用 GET 调用，请求消息放在查询串里。

公开节点的实时状态：

```sh example
curl -fsS -G --data-urlencode 'connect=v1' --data-urlencode 'encoding=json' --data-urlencode 'message={}' \
  "$PROBE_HUB/probe.v1.PublicService/GetSnapshot" | jq '[(.nodes // [])[] | {id, name, online}]'
```
