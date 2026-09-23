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

PROBE_OFFLINE_AFTER=12s bin/probe-hub serve --db "$db" --listen "127.0.0.1:$port" > "$work/hub.log" 2>&1 &
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
sleep 1

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

agent_status=0
wait "$amd64" || agent_status=1
wait "$arm64" || agent_status=1

# 跨过分钟边界后查最近一小时；首次冷采样所在桶可能没有 CPU 读数，后续样本不能因此被忽略。
now=$(date +%s)
node1=$(jq -r '.nodes[0].id' "$work/ListNodes.json")
query_body=$(jq -nc --arg nodeId "$node1" --argjson from "$((now - 3600))" --argjson to "$((now + 60))" \
  '{nodeId: $nodeId, from: $from, to: $to, maxPoints: 100}')
[ "$(rpc QueryMetrics "$query_body")" = 200 ] || { echo "FAIL: QueryMetrics"; cat "$work/QueryMetrics.json"; exit 1; }
jq -e '.level == "1m" and (.ts | length) >= 1 and any(.series[] | select(.name == "cpu") | .samples[]; .n > 0)' "$work/QueryMetrics.json" > /dev/null || { echo "FAIL: QueryMetrics shape"; cat "$work/QueryMetrics.json"; exit 1; }
[ "$(rpc Logout '{}')" = 200 ] || { echo "FAIL: logout"; exit 1; }
[ "$(rpc GetSnapshot '{}')" = 401 ] || { echo "FAIL: session survived logout"; exit 1; }

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
[ "$(get node_facts)" = 2 ] || { echo "FAIL: facts count"; exit 1; }
[ "$(get metric_1m)" -ge 2 ] || { echo "FAIL: no minute rows"; exit 1; }
[ "$(get admin)" = 1 ] || { echo "FAIL: admin row"; exit 1; }
[ "$(get admin_session)" = 0 ] || { echo "FAIL: session not removed by logout"; exit 1; }
[ "$(grep -c 'node registered' "$work/hub.log")" = 2 ] || { echo "FAIL: registration log count"; exit 1; }
grep -Eq '^[0-9]+[[:space:]]+e2e-amd64[[:space:]]' "$work/nodes.txt" || { echo "FAIL: amd64 node missing"; exit 1; }
grep -Eq '^[0-9]+[[:space:]]+e2e-arm64[[:space:]]' "$work/nodes.txt" || { echo "FAIL: arm64 node missing"; exit 1; }
echo "E2E OK"
