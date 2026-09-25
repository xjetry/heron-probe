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
- 时间是 Unix 秒；字段名以 `_ms` 结尾的是毫秒，以 `_s` 结尾的是秒。JSON 字段名是 proto 字段名的小驼峰（`last_seen_at` → `lastSeenAt`）。
- 缺读数与读数为 0 不同：`optional` 字段缺失表示没有读数，不要当成 0。
- 字段取默认值（0、空串、false、空列表）时在 JSON 里省略。
- 出错时 HTTP 状态非 200，响应体是 `{"code": "...", "message": "..."}`；message 写明哪个字段、违反了什么约束、期望什么取值。

## 例子

全部节点与最近一次上报时刻：

```sh example
curl -fsS -H "Authorization: Bearer $PROBE_TOKEN" -H 'Content-Type: application/json' \
  --data '{}' "$PROBE_HUB/probe.v1.AdminService/ListNodes" | jq '[.nodes[] | {id, name, lastSeenAt}]'
```

第一个节点最近一小时的 CPU（百分比；每点有样本数、均值与最大值，`ts` 与 `samples` 一一对应）：

```sh example
node=$(curl -fsS -H "Authorization: Bearer $PROBE_TOKEN" -H 'Content-Type: application/json' \
  --data '{}' "$PROBE_HUB/probe.v1.AdminService/ListNodes" | jq -r '.nodes[0].id')
now=$(date +%s)
curl -fsS -H "Authorization: Bearer $PROBE_TOKEN" -H 'Content-Type: application/json' \
  --data "$(jq -nc --arg node "$node" --argjson now "$now" '{nodeId: $node, from: ($now - 3600), to: $now}')" \
  "$PROBE_HUB/probe.v1.AdminService/QueryMetrics" | jq '{stepS, ts, cpu: [.series[] | select(.name == "cpu") | .samples[]]}'
```

最近 20 条告警事件：

```sh example
curl -fsS -H "Authorization: Bearer $PROBE_TOKEN" -H 'Content-Type: application/json' \
  --data '{"limit": 20}' "$PROBE_HUB/probe.v1.AdminService/ListAlertEvents" | jq '.events // []'
```
