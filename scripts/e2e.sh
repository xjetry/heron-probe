#!/bin/sh
# 端到端：hub 跑在宿主机，agent 跑在 Linux 容器里经 host.docker.internal 上报；
# 管理 API 用 curl + jq 走纯 HTTP + JSON——这是面向 agent 设计的验收方式，不用生成客户端。
# 两个平台都必须实际运行；依赖 Docker 多架构模拟，OrbStack / Docker Desktop 自带。
# 验收凭据是库里出现两个节点、facts 与分钟行，且管理 API 看到它们在线并查到历史。
set -eu
cd "$(cd "$(dirname "$0")/.." && pwd)"
work=$(mktemp -d)
echo "E2E artifacts: $work"
db="$work/e2e.db"
port=18080
base="http://127.0.0.1:$port"
admin_pw="e2e admin password 2026"
: > "$work/jar"
bin/probe-hub window open --db "$db" --ttl 10m --max 2 > "$work/window.txt"
key=$(sed -n 's/^key: //p' "$work/window.txt")
[ -n "$key" ] || { echo "no key"; exit 1; }

PROBE_OFFLINE_AFTER=12s bin/probe-hub serve --db "$db" --listen "127.0.0.1:$port" --timezone UTC > "$work/hub.log" 2>&1 &
hub=$!
# 每次运行只回收自己的容器；失败也必须停止上报，不能把流量带进下一次验收。
cleanup() {
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

# 运行中设密码：另一进程经 WAL 写库，登录路径每次读库，不需要重启 hub。
printf '%s\n' "$admin_pw" | bin/probe-hub passwd --db "$db" > "$work/passwd.log" 2>&1

# rpc 名字 请求体 [额外 curl 参数]：向 AdminService 发 JSON，打印 HTTP 状态码，响应体落 $work/<名字>.json。
rpc() {
  name=$1; body=$2; shift 2
  curl -sS -o "$work/$name.json" -w '%{http_code}' -H 'Content-Type: application/json' \
    -b "$work/jar" -c "$work/jar" "$@" --data "$body" "$base/probe.v1.AdminService/$name"
}

[ "$(curl -sS -o /dev/null -w '%{http_code}' "$base/")" = 302 ] || { echo "FAIL: / must redirect to the panel"; exit 1; }
[ "$(curl -sS -o "$work/admin.html" -w '%{http_code}' "$base/admin/")" = 200 ] || { echo "FAIL: /admin/ not served"; exit 1; }
grep -q 'id="root"' "$work/admin.html" || { echo "FAIL: panel index missing root element"; exit 1; }
curl -sS -D "$work/admin.headers" -o /dev/null "$base/admin/" && grep -qi '^content-security-policy:' "$work/admin.headers" || { echo "FAIL: CSP header missing"; exit 1; }

[ "$(rpc GetSnapshot '{}')" = 401 ] || { echo "FAIL: anonymous GetSnapshot was not 401"; exit 1; }
login_body=$(jq -nc --arg password "$admin_pw" '{password: $password}')
[ "$(rpc Login "$login_body")" = 200 ] || { echo "FAIL: login"; cat "$work/Login.json"; exit 1; }
[ "$(curl -sS -o /dev/null -w '%{http_code}' -b "$work/jar" -H 'Content-Type: text/plain' --data '{}' "$base/probe.v1.AdminService/CreateNode")" = 415 ] || { echo "FAIL: text/plain POST was not 415"; exit 1; }
[ "$(curl -sS -o /dev/null -w '%{http_code}' -b "$work/jar" "$base/probe.v1.AdminService/CreateNode?connect=v1&encoding=json&message=%7B%7D")" = 405 ] || { echo "FAIL: GET was not 405"; exit 1; }

run_agent() {
  arch=$1
  docker run --cidfile "$work/cid-$arch" --platform "linux/$arch" --add-host=host.docker.internal:host-gateway \
    -v "$PWD/bin:/probe:ro" debian:bookworm-slim sh -c "
      set -e
      /probe/probe-agent-linux-$arch register --hub http://host.docker.internal:$port --key $key --config /tmp/agent.json --name e2e-$arch
      timeout 75 /probe/probe-agent-linux-$arch run --config /tmp/agent.json || true
    "
}
run_agent amd64 > "$work/agent-amd64.log" 2>&1 &
amd64=$!
run_agent arm64 > "$work/agent-arm64.log" 2>&1 &
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
node1=$(jq -r '.nodes[0].id' "$work/ListNodes.json")
node2=$(jq -r '.nodes[1].id' "$work/ListNodes.json")
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
probe_started=$(date +%s)
probe_deadline=$((probe_started + 75))
while :; do
  [ "$(rpc QueryProbes "$probe_body")" = 200 ] || { echo "FAIL: QueryProbes"; cat "$work/QueryProbes.json"; exit 1; }
  if jq -e --arg icmp "$icmp_task" --arg tcp "$tcp_task" '.level == "1m" and ([.series[].taskId] | sort) == ([$icmp, $tcp] | sort) and all(.series[]; any(.samples[]; .sent > 0 and .rttMeanUs != null and (.errors // 0) == 0))' "$work/QueryProbes.json" > /dev/null; then
    echo "probe results ready after $(($(date +%s) - probe_started))s"
    break
  fi
  if [ "$(date +%s)" -ge "$probe_deadline" ]; then
    echo "FAIL: probe results after 75s"; cat "$work/QueryProbes.json"; exit 1
  fi
  sleep 2
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
update_body=$(jq -nc --arg id "$node1" --arg name "$(jq -r '.nodes[0].name' "$work/ListNodes.json")" '{id: $id, name: $name, public: false, note: "", trafficResetDay: 15}')
[ "$(rpc UpdateNode "$update_body")" = 200 ] || { echo "FAIL: UpdateNode reset day"; cat "$work/UpdateNode.json"; exit 1; }
jq -e '.node.trafficResetDay == 15' "$work/UpdateNode.json" > /dev/null || { echo "FAIL: reset day not echoed"; cat "$work/UpdateNode.json"; exit 1; }

# 两个 agent 已退出；先推进到新重置日对应的周期，再保存停机前状态。
[ "$(rpc GetTraffic '{}')" = 200 ] || { echo "FAIL: GetTraffic before restart"; exit 1; }
traffic_before=$(jq -c --arg id "$node1" '.nodes[] | select(.nodeId == $id) | .traffic' "$work/GetTraffic.json")

[ "$(rpc Logout '{}')" = 200 ] || { echo "FAIL: logout"; exit 1; }
[ "$(rpc GetSnapshot '{}')" = 401 ] || { echo "FAIL: session survived logout"; exit 1; }

kill "$hub"; wait "$hub"
hub=""

# 重启：流量状态、重置日与被 Drain 出的分钟行都必须还在。
PROBE_OFFLINE_AFTER=12s bin/probe-hub serve --db "$db" --listen "127.0.0.1:$port" --timezone UTC >> "$work/hub.log" 2>&1 &
hub=$!
wait_hub
[ "$(rpc Login "$login_body")" = 200 ] || { echo "FAIL: login after restart"; exit 1; }
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
