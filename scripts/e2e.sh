#!/bin/sh
# 端到端：hub 跑在宿主机，agent 跑在 Linux 容器里经 host.docker.internal 上报。
# 两个平台都必须实际运行；依赖 Docker 多架构模拟，OrbStack / Docker Desktop 自带。
# 验收凭据是库里出现两个节点、facts 与分钟行，不是"进程没报错"。
set -eu
cd "$(cd "$(dirname "$0")/.." && pwd)"
work=$(mktemp -d)
db="$work/e2e.db"
port=18080
bin/probe-hub window open --db "$db" --ttl 10m --max 2 > "$work/window.txt"
key=$(sed -n 's/^key: //p' "$work/window.txt")
[ -n "$key" ] || { echo "no key"; exit 1; }

PROBE_OFFLINE_AFTER=12s bin/probe-hub serve --db "$db" --listen "127.0.0.1:$port" > "$work/hub.log" 2>&1 &
hub=$!
cleanup() { kill "$hub" 2>/dev/null || true; wait "$hub" 2>/dev/null || true; }
trap cleanup EXIT
sleep 1

run_agent() {
  arch=$1
  docker run --rm --platform "linux/$arch" --add-host=host.docker.internal:host-gateway \
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
agent_status=0
wait "$amd64" || agent_status=1
wait "$arm64" || agent_status=1

kill "$hub"; wait "$hub" || true
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
[ "$(grep -c 'node registered' "$work/hub.log")" = 2 ] || { echo "FAIL: registration log count"; exit 1; }
grep -Eq '^[0-9]+[[:space:]]+e2e-amd64[[:space:]]' "$work/nodes.txt" || { echo "FAIL: amd64 node missing"; exit 1; }
grep -Eq '^[0-9]+[[:space:]]+e2e-arm64[[:space:]]' "$work/nodes.txt" || { echo "FAIL: arm64 node missing"; exit 1; }
echo "E2E OK"
