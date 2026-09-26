#!/bin/sh
# 端到端：hub 跑在宿主机，agent 跑在 Linux 容器里经 host.docker.internal 上报；
# 管理 API 用 curl + jq 走纯 HTTP + JSON——这是面向 agent 设计的验收方式，不用生成客户端。
# 两个平台都必须实际运行；依赖 Docker 多架构模拟，OrbStack / Docker Desktop 自带。
# 验收凭据是库里出现两个节点、facts 与分钟行，且管理 API 看到它们在线并查到历史。
set -eu
# 镜像与期望系统名成对给出：缺一个就无法证明容器真的换成了目标发行版，不能退化成不检查。
: "${AGENT_IMAGE:?AGENT_IMAGE is required, e.g. alpine:3.21}"
: "${EXPECT_OS:?EXPECT_OS is required, e.g. Alpine}"
echo "agent image: $AGENT_IMAGE (expect os containing \"$EXPECT_OS\")"
cd "$(cd "$(dirname "$0")/.." && pwd)"
work=$(mktemp -d)
echo "E2E artifacts: $work"
db="$work/e2e.db"
port=18080
base="http://127.0.0.1:$port"
admin_pw="e2e admin password 2026"
: > "$work/jar"
hookrecv=""
hub=""
# 每次运行只回收自己的容器；失败也必须停止上报，不能把流量带进下一次验收。
# 清理先于一切会留下副作用的步骤安装：容器准备中途失败时，已启动的容器也要回收。
cleanup() {
  if [ -n "$hookrecv" ]; then
    kill "$hookrecv" || true
    wait "$hookrecv" || true
    hookrecv=""
  fi
  for arch in amd64 arm64; do
    if [ -s "$work/cid-$arch" ]; then
      docker rm -f "$(cat "$work/cid-$arch")" || true
    fi
  done
  if [ -n "$hub" ]; then
    kill "$hub" 2>/dev/null || true
    wait "$hub" 2>/dev/null || true
  fi
}
trap cleanup EXIT

# 镜像冷拉取可能远超注册窗口与上线等待的预算，它属于准备阶段，不能消耗这些预算；
# 任一架构准备失败就直接退出，不进入注册阶段。注册与上报复用这两个容器。
for arch in amd64 arm64; do
  docker run -d --cidfile "$work/cid-$arch" --platform "linux/$arch" --add-host=host.docker.internal:host-gateway \
    -v "$PWD/bin:/probe:ro" "$AGENT_IMAGE" sleep infinity > /dev/null
done
echo "agent containers ready: $AGENT_IMAGE"

bin/probe-hub window open --db "$db" --ttl 10m --max 2 > "$work/window.txt"
key=$(sed -n 's/^key: //p' "$work/window.txt")
[ -n "$key" ] || { echo "no key"; exit 1; }
echo "registration window: $(sed -n 's/^expires: //p' "$work/window.txt")"

PROBE_OFFLINE_AFTER=12s bin/probe-hub serve --db "$db" --listen "127.0.0.1:$port" --timezone UTC > "$work/hub.log" 2>&1 &
hub=$!

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
  exit 1
}
wait_hub
# 告警等待上限由 hub 与 agent 实际生效的参数推出（见 wait_alert 的调用处），参数读自两者的启动行，
# 脚本里不另抄一份。只认整秒写法：读不出来就停下，不能退回一个与实际参数无关的固定上限。
# startup_seconds 日志文件 启动行消息 字段名。internal/testlog.WholeSeconds 与这里同形（msg 过滤、字段前后
# 空格、纯整秒、取第一条能读出的），make ci 的启动行测试靠它钉住本脚本读得出的写法，改一处须同改另一处。
startup_seconds() {
  sed -n "s/.*msg=\"$2\".* $3=\([0-9][0-9]*\)s .*/\1/p" "$1" | sed -n 1p
}
ttl_s=$(startup_seconds "$work/hub.log" "hub listening" ttl)
sweep_s=$(startup_seconds "$work/hub.log" "hub listening" offline_sweep)
retry_wait_s=$(startup_seconds "$work/hub.log" "hub listening" delivery_retry_wait)
[ -n "$ttl_s" ] && [ -n "$sweep_s" ] && [ -n "$retry_wait_s" ] || { echo "FAIL: hub startup line does not state ttl, offline_sweep and delivery_retry_wait in whole seconds"; cat "$work/hub.log"; exit 1; }

# 运行中设密码：另一进程经 WAL 写库，登录路径每次读库，不需要重启 hub。
printf '%s\n' "$admin_pw" | bin/probe-hub passwd --db "$db" > "$work/passwd.log" 2>&1

# rpc 名字 请求体 [额外 curl 参数]：向 AdminService 发 JSON，打印 HTTP 状态码，响应体落 $work/<名字>.json。
rpc() {
  name=$1; body=$2; shift 2
  curl -sS -o "$work/$name.json" -w '%{http_code}' -H 'Content-Type: application/json' \
    -b "$work/jar" -c "$work/jar" "$@" --data "$body" "$base/probe.v1.AdminService/$name"
}

# bearer 名字 请求体：用 API token 调 AdminService，不带 cookie；打印状态码，响应体落 $work/bearer-<名字>.json。
bearer() {
  name=$1; body=$2
  curl -sS -o "$work/bearer-$name.json" -w '%{http_code}' -H 'Content-Type: application/json' \
    -H "Authorization: Bearer $api_token" --data "$body" "$base/probe.v1.AdminService/$name"
}

# 卡片示例取自 hub 刚下发的那份。空列表由例子自己处理，空 hub 与有数据时用同一段。
# 第二个参数非空时，每个例子的顶层 JSON 必须非空：有数据时例 2 输出 {} 说明取 id 走错了分支。
run_card_examples() {
  label=$1
  nonempty=$2
  jq -r '.guide' "$work/bearer-GetApiReference.json" > "$work/SKILL.md"
  rm -f "$work"/card-example-*.sh "$work"/card-example-*.sh.out "$work"/card-example-*.sh.err
  awk -v dir="$work" '
    /^```sh example$/ { n++; file = sprintf("%s/card-example-%d.sh", dir, n); inblock = 1; next }
    inblock && /^```$/ { inblock = 0; close(file); next }
    inblock { print > file }
  ' "$work/SKILL.md"
  examples=$(ls "$work"/card-example-*.sh 2> /dev/null | wc -l | tr -d ' ')
  # 标记块数独立于抽取逻辑另数一次：抽取漏块或多切时两数不等；卡片被删到只剩寥寥几例时下限挡住。
  marked=$(grep -c '^```sh example$' "$work/SKILL.md" || [ "$?" = 1 ])
  [ "$examples" = "$marked" ] || { echo "FAIL: extracted $examples card examples but the card marks $marked"; exit 1; }
  [ "$examples" -ge 3 ] || { echo "FAIL: expected at least 3 card examples, found $examples"; exit 1; }
  for ex in "$work"/card-example-*.sh; do
    status=0
    PROBE_HUB=$base PROBE_TOKEN=$api_token sh -eu "$ex" > "$ex.out" 2> "$ex.err" || status=$?
    [ "$status" = 0 ] || { echo "FAIL: card example $ex exited $status"; cat "$ex" "$ex.err"; exit 1; }
    [ -s "$ex.out" ] && jq -e . "$ex.out" > /dev/null || { echo "FAIL: card example $ex did not print JSON"; cat "$ex" "$ex.out" "$ex.err"; exit 1; }
    # null 的 length 是 0，不能靠 length 单独把 null 当成有内容；空对象与空数组的 length 也是 0。
    if [ -n "$nonempty" ]; then
      jq -e 'if . == null then false else length > 0 end' "$ex.out" > /dev/null || { echo "FAIL: card example $ex printed empty JSON"; cat "$ex" "$ex.out"; exit 1; }
    fi
  done
  if [ -n "$label" ]; then
    echo "card examples ok ($label): $examples"
  else
    echo "card examples ok: $examples"
  fi
}

[ "$(curl -sS -o /dev/null -w '%{http_code}' "$base/")" = 302 ] || { echo "FAIL: / must redirect to the panel"; exit 1; }
[ "$(curl -sS -o "$work/admin.html" -w '%{http_code}' "$base/admin/")" = 200 ] || { echo "FAIL: /admin/ not served"; exit 1; }
grep -q 'id="root"' "$work/admin.html" || { echo "FAIL: panel index missing root element"; exit 1; }
curl -sS -D "$work/admin.headers" -o /dev/null "$base/admin/" && grep -qi '^content-security-policy:' "$work/admin.headers" || { echo "FAIL: CSP header missing"; exit 1; }

[ "$(rpc GetSnapshot '{}')" = 401 ] || { echo "FAIL: anonymous GetSnapshot was not 401"; exit 1; }
login_body=$(jq -nc --arg password "$admin_pw" '{password: $password}')
[ "$(rpc Login "$login_body")" = 200 ] || { echo "FAIL: login"; cat "$work/Login.json"; exit 1; }
# hub 上还没有节点：卡片例子必须在空库上也能跑完。
[ "$(rpc CreateApiToken '{"name":"e2e-empty"}')" = 200 ] || { echo "FAIL: CreateApiToken (empty hub)"; cat "$work/CreateApiToken.json"; exit 1; }
api_token=$(jq -r '.token' "$work/CreateApiToken.json")
api_token_id=$(jq -r '.apiToken.id' "$work/CreateApiToken.json")
[ "$(bearer GetApiReference '{}')" = 200 ] || { echo "FAIL: GetApiReference on empty hub"; cat "$work/bearer-GetApiReference.json"; exit 1; }
run_card_examples "empty hub" ""
[ "$(rpc DeleteApiToken "$(jq -nc --arg id "$api_token_id" '{id: $id}')")" = 200 ] || { echo "FAIL: DeleteApiToken (empty hub)"; exit 1; }
[ "$(curl -sS -o /dev/null -w '%{http_code}' -b "$work/jar" -H 'Content-Type: text/plain' --data '{}' "$base/probe.v1.AdminService/CreateNode")" = 415 ] || { echo "FAIL: text/plain POST was not 415"; exit 1; }
[ "$(curl -sS -o /dev/null -w '%{http_code}' -b "$work/jar" "$base/probe.v1.AdminService/CreateNode?connect=v1&encoding=json&message=%7B%7D")" = 405 ] || { echo "FAIL: GET was not 405"; exit 1; }

register_agent() {
  arch=$1
  docker exec "$(cat "$work/cid-$arch")" "/probe/probe-agent-linux-$arch" register \
    --hub "http://host.docker.internal:$port" --key "$key" --config /tmp/agent.json --name "e2e-$arch"
}
run_agent() {
  arch=$1
  if [ "$#" -gt 1 ]; then
    docker exec "$(cat "$work/cid-$arch")" timeout "$2" "/probe/probe-agent-linux-$arch" run --config /tmp/agent.json || [ "$?" = 124 ]
  else
    docker exec "$(cat "$work/cid-$arch")" "/probe/probe-agent-linux-$arch" run --config /tmp/agent.json
  fi
}
first_agent() {
  register_agent "$1"
  run_agent "$1" 75
}
first_agent amd64 > "$work/agent-amd64.log" 2>&1 &
amd64=$!
first_agent arm64 > "$work/agent-arm64.log" 2>&1 &
arm64=$!

# CPU 百分比需要两次采样差分；在线并不保证第一份上报已有 CPU 读数。
i=0
until [ "$(rpc GetSnapshot '{}')" = 200 ] && jq -e '[.nodes[]? | select(.online == true and .metrics.cpuPct != null)] | length == 2' "$work/GetSnapshot.json" > /dev/null; do
  i=$((i + 1)); [ "$i" -lt 60 ] || { echo "FAIL: nodes did not come online"; cat "$work/GetSnapshot.json"; exit 1; }
  sleep 1
done
jq -e '.reportIntervalMs == 4000 and all(.nodes[]; .metrics.cpuPct != null)' "$work/GetSnapshot.json" > /dev/null || { echo "FAIL: snapshot shape"; cat "$work/GetSnapshot.json"; exit 1; }
[ "$(rpc ListNodes '{}')" = 200 ] || { echo "FAIL: ListNodes"; exit 1; }
jq -e '[.nodes[] | select(.facts.arch == "amd64" or .facts.arch == "arm64")] | length == 2' "$work/ListNodes.json" > /dev/null || { echo "FAIL: facts not reported"; cat "$work/ListNodes.json"; exit 1; }
jq -e --arg os "$EXPECT_OS" '(.nodes | length) == 2 and all(.nodes[]; (.facts.os // "") | contains($os))' "$work/ListNodes.json" > /dev/null || { echo "FAIL: nodes did not report an OS containing \"$EXPECT_OS\""; cat "$work/ListNodes.json"; exit 1; }
node1=$(jq -r '.nodes[] | select(.facts.arch == "amd64") | .id' "$work/ListNodes.json")
node2=$(jq -r '.nodes[] | select(.facts.arch == "arm64") | .id' "$work/ListNodes.json")
icmp_body=$(jq -nc --arg a "$node1" --arg b "$node2" '{task: {kind: "PROBE_KIND_ICMP", target: "127.0.0.1", intervalS: 5, timeoutMs: 1000}, nodeIds: [$a, $b]}')
[ "$(rpc SaveProbeTask "$icmp_body")" = 200 ] || { echo "FAIL: SaveProbeTask icmp"; cat "$work/SaveProbeTask.json"; exit 1; }
icmp_task=$(jq -r '.task.task.id' "$work/SaveProbeTask.json")
tcp_body=$(jq -nc --arg a "$node1" --arg b "$node2" --arg target "host.docker.internal:$port" '{task: {kind: "PROBE_KIND_TCP", target: $target, intervalS: 5, timeoutMs: 2000}, nodeIds: [$a, $b]}')
[ "$(rpc SaveProbeTask "$tcp_body")" = 200 ] || { echo "FAIL: SaveProbeTask tcp"; cat "$work/SaveProbeTask.json"; exit 1; }
tcp_task=$(jq -r '.task.task.id' "$work/SaveProbeTask.json")
[ "$(rpc SaveProbeTask '{"task": {"kind": "PROBE_KIND_ICMP", "target": "127.0.0.1", "intervalS": 1, "timeoutMs": 1000}}')" = 400 ] || { echo "FAIL: interval below the minimum must be rejected"; exit 1; }
grep -q 'interval_s must be between 5 and 3600' "$work/SaveProbeTask.json" || { echo "FAIL: error must name the field"; cat "$work/SaveProbeTask.json"; exit 1; }
[ "$(rpc ListProbeTasks '{}')" = 200 ] || { echo "FAIL: ListProbeTasks"; exit 1; }
jq -e '(.version | tonumber) > 0 and (.tasks | length) == 2 and all(.tasks[]; (.nodeIds | length) == 2)' "$work/ListProbeTasks.json" > /dev/null || { echo "FAIL: task list shape"; cat "$work/ListProbeTasks.json"; exit 1; }

agent_status=0
wait "$amd64" || agent_status=1
wait "$arm64" || agent_status=1

task_version=$(jq -r '.version' "$work/ListProbeTasks.json")
echo "probe task version before restart: $task_version"

# 跨过分钟边界后查最近一小时；首次冷采样所在桶可能没有 CPU 读数，后续样本不能因此被忽略。
now=$(date +%s)
query_body=$(jq -nc --arg nodeId "$node1" --argjson from "$((now - 3600))" --argjson to "$((now + 60))" \
  '{nodeId: $nodeId, from: $from, to: $to, maxPoints: 100}')
[ "$(rpc QueryMetrics "$query_body")" = 200 ] || { echo "FAIL: QueryMetrics"; cat "$work/QueryMetrics.json"; exit 1; }
jq -e '.level == "1m" and (.ts | length) >= 1 and any(.series[] | select(.name == "cpu") | .samples[]; .n > 0)' "$work/QueryMetrics.json" > /dev/null || { echo "FAIL: QueryMetrics shape"; cat "$work/QueryMetrics.json"; exit 1; }
probe_body=$(jq -nc --arg nodeId "$node1" --argjson from "$((now - 3600))" --argjson to "$((now + 60))" '{nodeId: $nodeId, from: $from, to: $to, maxPoints: 100}')
# 两个任务都有成功的探测：容器里的 ICMP 探测回环，TCP 连接宿主上的 hub。
# hub 在分钟边界后 0.5s 刷出分钟桶；4s 上报间隔与最多 5s 首次偏移下，首条结果最晚在任务创建后约 13s 到达。
# agent 退出后立即查库，距首条结果最坏不足 60.5s，一次性查询的最坏余量为负，须等待常规刷出。
# 截止按轮询间累计的 sleep 秒数计（见 wait_alert 的说明）。分钟行按墙钟分钟边界刷出，但宿主休眠时墙钟照走，
# hub 的刷出定时器无论是否把休眠时长计入，醒来后还需的醒着时间都不超过不休眠时；醒来后的第一次刷出由
# live.Flush 取走全部已闭合的桶（起始早于当前墙钟分钟的，不只上一分钟），墙钟跳过多个分钟也一次交出。
# 所以这里也不用墙钟截止。
probe_started=$(date +%s)
probe_budget_s=75
probe_slept_s=0
while :; do
  [ "$(rpc QueryProbes "$probe_body")" = 200 ] || { echo "FAIL: QueryProbes"; cat "$work/QueryProbes.json"; exit 1; }
  if jq -e --arg icmp "$icmp_task" --arg tcp "$tcp_task" '.level == "1m" and ([.series[].taskId] | sort) == ([$icmp, $tcp] | sort) and all(.series[]; any(.samples[]; .sent > 0 and .rttMeanUs != null and (.errors // 0) == 0))' "$work/QueryProbes.json" > /dev/null; then
    echo "probe results ready after $(($(date +%s) - probe_started))s (${probe_slept_s}s of polling)"
    break
  fi
  if [ "$probe_slept_s" -ge "$probe_budget_s" ]; then
    echo "FAIL: probe results not ready after ${probe_slept_s}s of polling"; cat "$work/QueryProbes.json"; exit 1
  fi
  sleep 2
  probe_slept_s=$((probe_slept_s + 2))
done
jq -e 'all(.nodes[]; .facts.icmpAvailable == true)' "$work/ListNodes.json" > /dev/null || { echo "FAIL: icmp_available not reported"; cat "$work/ListNodes.json"; exit 1; }
# 流量：两个 agent 每 4 秒上报一次，上报本身就产生字节；首次上报只取基线，之后的差分进总量。
[ "$(rpc GetTraffic '{}')" = 200 ] || { echo "FAIL: GetTraffic"; cat "$work/GetTraffic.json"; exit 1; }
jq -e '.timezone == "UTC" and (.nodes | length) == 2 and all(.nodes[]; (.traffic.totalRx | tonumber) > 0 and (.traffic.totalTx | tonumber) > 0 and .traffic.resetDay == 1 and (.traffic.nextResetAt | tonumber) > (.traffic.periodStart | tonumber))' "$work/GetTraffic.json" > /dev/null || { echo "FAIL: traffic shape"; cat "$work/GetTraffic.json"; exit 1; }
tx_before=$(jq -r --arg id "$node1" '.nodes[] | select(.nodeId == $id) | .traffic.totalTx' "$work/GetTraffic.json")
adjust_body=$(jq -nc --arg nodeId "$node1" '{nodeId: $nodeId, periodRx: "1073741824", periodTx: "0"}')
[ "$(rpc AdjustTraffic "$adjust_body")" = 200 ] || { echo "FAIL: AdjustTraffic"; cat "$work/AdjustTraffic.json"; exit 1; }
# 首个周期里总量等于周期量，校正后两者同为 1 GiB；上行改成 0 后总量也随差值归零。
jq -e '.traffic.periodRx == "1073741824" and .traffic.totalRx == "1073741824" and (.traffic.periodTx // "0") == "0" and (.traffic.totalTx // "0") == "0"' "$work/AdjustTraffic.json" > /dev/null || { echo "FAIL: AdjustTraffic result"; cat "$work/AdjustTraffic.json"; exit 1; }
[ "$(rpc AdjustTraffic '{"nodeId": "999999", "periodRx": "1"}')" = 404 ] || { echo "FAIL: AdjustTraffic on an unknown node must be 404"; exit 1; }
update_body=$(jq -nc --arg id "$node1" --arg name "e2e-amd64" '{id: $id, name: $name, public: false, note: "", trafficResetDay: 15, offlineGraceS: 0}')
[ "$(rpc UpdateNode "$update_body")" = 200 ] || { echo "FAIL: UpdateNode reset day"; cat "$work/UpdateNode.json"; exit 1; }
jq -e '.node.trafficResetDay == 15' "$work/UpdateNode.json" > /dev/null || { echo "FAIL: reset day not echoed"; cat "$work/UpdateNode.json"; exit 1; }

# 规则只覆盖 arm64；amd64 保持退出，node1 的流量精确复核不受后续上报影响。
run_agent arm64 >> "$work/agent-arm64.log" 2>&1 &
arm64=$!
i=0
until [ "$(rpc GetSnapshot '{}')" = 200 ] && jq -e --arg id "$node2" '[.nodes[] | select(.id == $id and .online == true)] | length == 1' "$work/GetSnapshot.json" > /dev/null; do
  i=$((i + 1)); [ "$i" -lt 40 ] || { echo "FAIL: arm64 did not resume"; cat "$work/GetSnapshot.json"; exit 1; }; sleep 1
done

# 接收器与 hub 同在宿主回环；每次请求体落一行，退出时一并回收。
python3 - "$work/hooks.txt" <<'PY' > "$work/hookrecv.log" 2>&1 &
import sys
from http.server import BaseHTTPRequestHandler, HTTPServer
out = sys.argv[1]
class H(BaseHTTPRequestHandler):
    def do_POST(self):
        body = self.rfile.read(int(self.headers.get("Content-Length", "0")))
        with open(out, "ab") as f:
            f.write(body + b"\n")
        self.send_response(200)
        self.end_headers()
    def do_GET(self):
        self.send_response(200)
        self.end_headers()
    def log_message(self, *a):
        pass
HTTPServer(("127.0.0.1", 18081), H).serve_forever()
PY
hookrecv=$!
i=0
until curl -sf -o /dev/null http://127.0.0.1:18081/; do
  i=$((i + 1)); [ "$i" -lt 30 ] || { echo "FAIL: webhook receiver did not listen"; cat "$work/hookrecv.log"; exit 1; }; sleep 0.2
done
[ "$(rpc SaveNotifyChannel "$(jq -nc '{channel: {name: "e2e hook", kind: "CHANNEL_KIND_WEBHOOK", webhook: {url: "http://127.0.0.1:18081/hook"}}}')")" = 200 ] || { echo "FAIL: SaveNotifyChannel"; cat "$work/SaveNotifyChannel.json"; exit 1; }
channel=$(jq -r '.channel.id' "$work/SaveNotifyChannel.json")
[ "$(rpc TestNotifyChannel "$(jq -nc --arg id "$channel" '{id: $id}')")" = 200 ] || { echo "FAIL: TestNotifyChannel"; cat "$work/TestNotifyChannel.json"; exit 1; }
[ "$(rpc SaveAlertRule "$(jq -nc --arg c "$channel" --arg n "$node2" '{rule: {name: "e2e offline", kind: "ALERT_KIND_OFFLINE", enabled: true, allNodes: false, nodeIds: [$n], channelIds: [$c]}}')")" = 200 ] || { echo "FAIL: SaveAlertRule"; cat "$work/SaveAlertRule.json"; exit 1; }

# 截止按轮询间累计的 sleep 秒数计，不按墙钟。宿主休眠时 hub、docker VM 与本脚本一起停摆，一次休眠
# 至多落在一次 sleep 里，累计值至多多算这一次，其余每一秒都是被等的系统醒着运行的时间。墙钟截止会把
# 整段休眠算进预算，醒来后第一次检查就失败，而那段时间里被等的系统根本没有运行。
wait_alert() {
  transition=$1
  alert_budget_s=$2
  alert_started=$(date +%s)
  alert_slept_s=0
  while :; do
    [ "$(rpc ListAlertEvents '{}')" = 200 ] || { echo "FAIL: ListAlertEvents"; cat "$work/ListAlertEvents.json"; exit 1; }
    if jq -e --arg n "$node2" --arg tr "$transition" '[.events[]? | select(.transition == $tr and .nodeId == $n and any(.deliveries[]; (.ok // false) == true))] | length == 1' "$work/ListAlertEvents.json" > /dev/null; then
      echo "alert $transition delivered after $(($(date +%s) - alert_started))s (${alert_slept_s}s of polling, budget ${alert_budget_s}s)"
      return
    fi
    [ "$alert_slept_s" -lt "$alert_budget_s" ] || { echo "FAIL: $transition alert not delivered after ${alert_slept_s}s of polling"; cat "$work/ListAlertEvents.json"; exit 1; }
    sleep 1
    alert_slept_s=$((alert_slept_s + 1))
  done
}
# agent 的参数取自它首次运行的启动行（cmd/agent/main.go 的 requestTimeout、initialInterval）。
request_timeout_s=$(startup_seconds "$work/agent-arm64.log" "agent starting" request_timeout)
initial_interval_s=$(startup_seconds "$work/agent-arm64.log" "agent starting" initial_interval)
[ -n "$request_timeout_s" ] && [ -n "$initial_interval_s" ] || { echo "FAIL: agent startup line does not state request_timeout and initial_interval in whole seconds"; cat "$work/agent-arm64.log"; exit 1; }
# 两个上限都是"推出的最坏时长 + 投递 + 余量"。SweepOffline 只由 RunOfflineSweep 按 offline_sweep
# 定时调用，触发与恢复都只在巡检时判定。
# firing：最后一次上报早于 docker kill，kill 后至多 ttl 即满足离线（node2 的宽限未设置，取 TTL，
#   NextOffline 也以 TTL 为下限），之后至多再等一个 offline_sweep。agent 侧没有量进入这条链：
#   kill 前 agent 若正在退避，最后一次上报只会更早，离线只会更早成立。
# recovered：agent 一启动就上报（Runner.Run 在首次 Sleep 之前发出 Report）。最坏情形取首次上报挂满
#   request_timeout（main.go 的 http.Client 超时），按 client.Backoff(1, initial_interval) 退避——
#   attempt 为 1 时等待落在 [interval/2, interval)——第二次上报成功。此后按 hub 下发的 ttl/3 间隔
#   上报，任何一轮巡检看到的未上报时长都小于 TTL，首个成功上报之后的第一轮巡检就判恢复，至多一个
#   offline_sweep，与 ttl 无关。连续失败没有上限可推：hub 一直不应答时节点本来就没有恢复上报。
# 投递 delivery_retry_wait：渠道失败可重试且存储正常时，一条投递依次等过 internal/hub/alert/queue.go
#   的 backoff 各项，总和即 DeliveryRetryWait。存储失败走 worker 级退避（1s 起翻倍、上限 1 分钟），
#   没有总量上界，不在预算内；e2e 的库在本机磁盘上，视为正常。渠道客户端 10s 超时（notify.go 的
#   NewHTTPClient）只在接收器挂住时才会用满；接收器在本机回环、已由 TestNotifyChannel 验证能应答，
#   上限不为此留量。
# 余量一个 offline_sweep：容纳 agent 进程启动与巡检本身的耗时推迟下一轮。docker kill/start 在计数
#   开始之前完成，不占预算。
wait_alert_firing_s=$((ttl_s + sweep_s + retry_wait_s + sweep_s))
wait_alert_recovered_s=$((request_timeout_s + initial_interval_s + sweep_s + retry_wait_s + sweep_s))
docker kill "$(cat "$work/cid-arm64")" > /dev/null
wait "$arm64" || true
wait_alert firing "$wait_alert_firing_s"
grep -q '"transition":"firing"' "$work/hooks.txt" || { echo "FAIL: webhook body"; cat "$work/hooks.txt"; exit 1; }
docker start "$(cat "$work/cid-arm64")" > /dev/null
run_agent arm64 >> "$work/agent-arm64.log" 2>&1 &
arm64=$!
wait_alert recovered "$wait_alert_recovered_s"
# node1 的 agent 已退出；先推进到新重置日对应的周期，再保存停机前状态。
[ "$(rpc GetTraffic '{}')" = 200 ] || { echo "FAIL: GetTraffic before restart"; exit 1; }
traffic_before=$(jq -c --arg id "$node1" '.nodes[] | select(.nodeId == $id) | .traffic' "$work/GetTraffic.json")

[ "$(rpc CreateApiToken '{"name":"e2e"}')" = 200 ] || { echo "FAIL: CreateApiToken"; cat "$work/CreateApiToken.json"; exit 1; }
api_token=$(jq -r '.token' "$work/CreateApiToken.json")
api_token_id=$(jq -r '.apiToken.id' "$work/CreateApiToken.json")
case "$api_token" in probe_at_*) ;; *) echo "FAIL: API token lacks the probe_at_ prefix"; exit 1 ;; esac

[ "$(rpc Logout '{}')" = 200 ] || { echo "FAIL: logout"; exit 1; }
[ "$(rpc GetSnapshot '{}')" = 401 ] || { echo "FAIL: session survived logout"; exit 1; }
# token 与会话是两条独立口径：登出不影响 token。
[ "$(bearer GetSnapshot '{}')" = 200 ] || { echo "FAIL: API token stopped working after logout"; cat "$work/bearer-GetSnapshot.json"; exit 1; }

kill "$hub"; wait "$hub"
hub=""

# 重启：流量状态、重置日与被 Drain 出的分钟行都必须还在。
PROBE_OFFLINE_AFTER=12s bin/probe-hub serve --db "$db" --listen "127.0.0.1:$port" --timezone UTC >> "$work/hub.log" 2>&1 &
hub=$!
wait_hub
[ "$(rpc Login "$login_body")" = 200 ] || { echo "FAIL: login after restart"; exit 1; }
# token 跨重启存活，只读、不能写；卡片取自 hub 实际下发的那份，其中的例子逐个在真实数据上跑。
[ "$(bearer ListNodes '{}')" = 200 ] || { echo "FAIL: API token lost across restart"; cat "$work/bearer-ListNodes.json"; exit 1; }
jq -e '(.nodes | length) == 2' "$work/bearer-ListNodes.json" > /dev/null || { echo "FAIL: ListNodes via token"; cat "$work/bearer-ListNodes.json"; exit 1; }
[ "$(bearer CreateNode '{"name":"via-token"}')" = 403 ] || { echo "FAIL: API token was allowed to write"; cat "$work/bearer-CreateNode.json"; exit 1; }
jq -e '.code == "permission_denied"' "$work/bearer-CreateNode.json" > /dev/null || { echo "FAIL: write via token not permission_denied"; exit 1; }
[ "$(bearer GetApiReference '{}')" = 200 ] || { echo "FAIL: GetApiReference via token"; exit 1; }
jq -e 'any(.files[]; .path == "probe/v1/admin.proto") and (.guide | contains("PROBE_TOKEN"))' "$work/bearer-GetApiReference.json" > /dev/null || { echo "FAIL: GetApiReference content"; exit 1; }
run_card_examples "" nonempty
[ "$(rpc DeleteApiToken "$(jq -nc --arg id "$api_token_id" '{id: $id}')")" = 200 ] || { echo "FAIL: DeleteApiToken"; exit 1; }
[ "$(bearer ListNodes '{}')" = 401 ] || { echo "FAIL: revoked API token still accepted"; exit 1; }
# 改密只清会话、不动 token；运行中由另一进程吊销，下一个请求即 401。
[ "$(rpc CreateApiToken '{"name":"e2e-cli"}')" = 200 ] || { echo "FAIL: CreateApiToken (cli)"; exit 1; }
api_token=$(jq -r '.token' "$work/CreateApiToken.json")
api_token_id=$(jq -r '.apiToken.id' "$work/CreateApiToken.json")
printf '%s\n' "$admin_pw" | bin/probe-hub passwd --db "$db" > "$work/passwd2.log" 2>&1
grep -q 'API tokens are not revoked' "$work/passwd2.log" || { echo "FAIL: passwd did not list API tokens"; cat "$work/passwd2.log"; exit 1; }
[ "$(rpc GetSnapshot '{}')" = 401 ] || { echo "FAIL: session survived password change"; exit 1; }
[ "$(bearer GetSnapshot '{}')" = 200 ] || { echo "FAIL: password change revoked the API token"; exit 1; }
bin/probe-hub token revoke --db "$db" --id "$api_token_id" > "$work/token-revoke.log" 2>&1 || { echo "FAIL: probe-hub token revoke"; cat "$work/token-revoke.log"; exit 1; }
[ "$(bearer GetSnapshot '{}')" = 401 ] || { echo "FAIL: CLI revocation not effective on a running hub"; exit 1; }
[ "$(rpc Login "$login_body")" = 200 ] || { echo "FAIL: login after password change"; exit 1; }
[ "$(rpc ListAlertRules '{}')" = 200 ] || { echo "FAIL: ListAlertRules after restart"; exit 1; }
jq -e '(.rules | length) == 1' "$work/ListAlertRules.json" > /dev/null || { echo "FAIL: alert rule lost"; cat "$work/ListAlertRules.json"; exit 1; }
[ "$(rpc ListAlertEvents '{}')" = 200 ] || { echo "FAIL: ListAlertEvents after restart"; exit 1; }
jq -e '(.events | length) == 2 and all(.events[]; any(.deliveries[]; (.ok // false) == true))' "$work/ListAlertEvents.json" > /dev/null || { echo "FAIL: alert events lost"; cat "$work/ListAlertEvents.json"; exit 1; }
[ "$(rpc ListProbeTasks '{}')" = 200 ] || { echo "FAIL: ListProbeTasks after restart"; exit 1; }
jq -e --arg version "$task_version" --arg icmp "$icmp_task" --arg tcp "$tcp_task" '.version == $version and (.tasks | length) == 2 and all(.tasks[]; (.nodeIds | length) == 2) and ([.tasks[].task.id] | sort) == ([$icmp, $tcp] | sort)' "$work/ListProbeTasks.json" > /dev/null || { echo "FAIL: tasks lost across restart"; cat "$work/ListProbeTasks.json"; exit 1; }
echo "probe task version after restart: $(jq -r '.version' "$work/ListProbeTasks.json")"
[ "$(rpc DeleteProbeTask "$(jq -nc --arg id "$tcp_task" '{id: $id}')")" = 200 ] || { echo "FAIL: DeleteProbeTask"; exit 1; }
[ "$(rpc ListProbeTasks '{}')" = 200 ] || { echo "FAIL: ListProbeTasks after deletion"; exit 1; }
jq -e --arg version "$task_version" --arg icmp "$icmp_task" '(.version | tonumber) > ($version | tonumber) and (.tasks | length) == 1 and .tasks[0].task.id == $icmp' "$work/ListProbeTasks.json" > /dev/null || { echo "FAIL: task deletion not reflected"; cat "$work/ListProbeTasks.json"; exit 1; }
[ "$(rpc QueryProbes "$probe_body")" = 200 ] || { echo "FAIL: QueryProbes after deletion"; exit 1; }
echo "probe task version after deletion: $(jq -r '.version' "$work/ListProbeTasks.json")"
# 删除清单中的任务不删除历史，重启前采集的两个任务仍须可查询。
jq -e --arg icmp "$icmp_task" --arg tcp "$tcp_task" '([.series[].taskId] | sort) == ([$icmp, $tcp] | sort)' "$work/QueryProbes.json" > /dev/null || { echo "FAIL: probe history lost"; cat "$work/QueryProbes.json"; exit 1; }
[ "$(rpc GetTraffic '{}')" = 200 ] || { echo "FAIL: GetTraffic after restart"; exit 1; }
# 总量不因周期滚动清零；agent 已退出，同周期的用量不再变化，跨周期则为零。
jq -e --arg id "$node1" --arg tx "$tx_before" --argjson before "$traffic_before" '.nodes[] | select(.nodeId == $id) |
  .traffic.resetDay == 15 and (.traffic.totalRx | tonumber) >= 1073741824 and (.traffic.totalTx // "0" | tonumber) <= ($tx | tonumber) and
  (if .traffic.periodStart == $before.periodStart then
    (.traffic.periodRx // "0") == ($before.periodRx // "0")
  else
    (.traffic.periodStart | tonumber) > ($before.periodStart | tonumber) and (.traffic.periodRx // "0") == "0"
  end)' "$work/GetTraffic.json" > /dev/null || { echo "FAIL: traffic state lost across restart"; cat "$work/GetTraffic.json"; exit 1; }
[ "$(rpc QueryMetrics "$query_body")" = 200 ] || { echo "FAIL: QueryMetrics after restart"; exit 1; }
jq -e 'any(.series[] | select(.name == "tx_bytes") | .samples[]; .n > 0 and .sum != null and .mean == null)' "$work/QueryMetrics.json" > /dev/null || { echo "FAIL: tx_bytes minute sums missing"; cat "$work/QueryMetrics.json"; exit 1; }
[ "$(rpc Logout '{}')" = 200 ] || { echo "FAIL: logout after restart"; exit 1; }
kill "$hub"; wait "$hub"
hub=""

cleanup
trap - EXIT

echo "--- hub.log ---"; cat "$work/hub.log"
echo "--- agent-amd64.log (tail) ---"; tail -5 "$work/agent-amd64.log"
echo "--- agent-arm64.log (tail) ---"; tail -5 "$work/agent-arm64.log"
[ "$agent_status" = 0 ] || { echo "FAIL: agent container failed"; exit 1; }
echo "--- stats ---"
bin/probe-hub stats --db "$db" > "$work/stats.txt"
cat "$work/stats.txt"
bin/probe-hub node list --db "$db" > "$work/nodes.txt"
cat "$work/nodes.txt"

get() { sed -n "s/^$1: //p" "$work/stats.txt"; }
[ "$(get node)" = 2 ] || { echo "FAIL: node count"; exit 1; }
[ "$(get alert_rule)" = 1 ] || { echo "FAIL: alert rule count"; exit 1; }
[ "$(get alert_rule_node)" = 1 ] || { echo "FAIL: alert scope count"; exit 1; }
[ "$(get alert_event)" = 2 ] || { echo "FAIL: alert event count"; exit 1; }
[ "$(get alert_delivery)" = 2 ] || { echo "FAIL: alert delivery count"; exit 1; }
[ "$(get notify_channel)" = 1 ] || { echo "FAIL: channel count"; exit 1; }
[ "$(get probe_task)" = 1 ] || { echo "FAIL: task count"; exit 1; }
[ "$(get probe_task_node)" = 2 ] || { echo "FAIL: assignment count"; exit 1; }
[ "$(get probe_1m)" -ge 2 ] || { echo "FAIL: no probe minute rows"; exit 1; }
[ "$(get traffic)" = 2 ] || { echo "FAIL: traffic rows"; exit 1; }
[ "$(get node_facts)" = 2 ] || { echo "FAIL: facts count"; exit 1; }
[ "$(get metric_1m)" -ge 2 ] || { echo "FAIL: no minute rows"; exit 1; }
[ "$(get admin)" = 1 ] || { echo "FAIL: admin row"; exit 1; }
[ "$(get admin_session)" = 0 ] || { echo "FAIL: session not removed by logout"; exit 1; }
[ "$(grep -c 'node registered' "$work/hub.log")" = 2 ] || { echo "FAIL: registration log count"; exit 1; }
grep -Eq '^[0-9]+[[:space:]]+e2e-amd64[[:space:]]' "$work/nodes.txt" || { echo "FAIL: amd64 node missing"; exit 1; }
grep -Eq '^[0-9]+[[:space:]]+e2e-arm64[[:space:]]' "$work/nodes.txt" || { echo "FAIL: arm64 node missing"; exit 1; }
echo "E2E OK"
