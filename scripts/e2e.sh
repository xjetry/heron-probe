#!/bin/sh
# 端到端：hub 跑在宿主机，agent 跑在 Linux 容器里经 host.docker.internal 上报。
# 验收凭据是库里出现了节点、facts 与至少一条分钟行，不是"进程没报错"。
set -eu
cd "$(cd "$(dirname "$0")/.." && pwd)"
work=$(mktemp -d)
db="$work/e2e.db"
port=18080
case "$(uname -m)" in
  arm64|aarch64) agent=bin/probe-agent-linux-arm64 ;;
  *) agent=bin/probe-agent-linux-amd64 ;;
esac

key=$(bin/probe-hub window open --db "$db" --ttl 10m --max 1 | sed -n 's/^key: //p')
[ -n "$key" ] || { echo "no key"; exit 1; }

PROBE_OFFLINE_AFTER=12s bin/probe-hub serve --db "$db" --listen "127.0.0.1:$port" > "$work/hub.log" 2>&1 &
hub=$!
cleanup() { kill "$hub" 2>/dev/null || true; wait "$hub" 2>/dev/null || true; }
trap cleanup EXIT
sleep 1

docker run --rm --add-host=host.docker.internal:host-gateway \
  -v "$PWD/bin:/probe:ro" debian:bookworm-slim sh -c "
    set -e
    /probe/$(basename "$agent") register --hub http://host.docker.internal:$port --key $key --config /tmp/agent.json --name e2e
    timeout 75 /probe/$(basename "$agent") run --config /tmp/agent.json || true
  " > "$work/agent.log" 2>&1

kill "$hub"; wait "$hub" || true
trap - EXIT

echo "--- hub.log ---"; cat "$work/hub.log"
echo "--- agent.log (tail) ---"; tail -5 "$work/agent.log"
echo "--- stats ---"
bin/probe-hub stats --db "$db" | tee "$work/stats.txt"
bin/probe-hub node list --db "$db"

get() { sed -n "s/^$1: //p" "$work/stats.txt"; }
[ "$(get node)" = 1 ] || { echo "FAIL: node count"; exit 1; }
[ "$(get node_facts)" = 1 ] || { echo "FAIL: facts count"; exit 1; }
[ "$(get metric_1m)" -ge 1 ] || { echo "FAIL: no minute rows"; exit 1; }
grep -q "node registered" "$work/hub.log" || { echo "FAIL: hub did not log registration"; exit 1; }
echo "E2E OK"
