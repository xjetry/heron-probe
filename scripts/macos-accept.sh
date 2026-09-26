#!/bin/sh
# macOS agent 的本机验收：以普通用户在本机起 hub，用本次 make release 的 darwin 产物注册并运行 agent，
# 经管理 API（curl + jq）断言各指标、facts、ICMP 与流量差分；再把包里的 plist 改成用户域作业交给 launchd，
# 验证 KeepAlive 拉起、ThrottleInterval 与日志文件由 launchd 创建。
# 不 sudo，不执行安装脚本：system 域、专用账户与 root 属主由 README 的真机清单验证。
# 端口 18087/18088 与 e2e（18080/18081）、install-accept（18085/18086）错开。
set -eu
cd "$(cd "$(dirname "$0")/.." && pwd)"
[ "$(uname -s)" = Darwin ] || { echo "macos-accept.sh runs on macOS only" >&2; exit 2; }
# 以 root 跑会掩盖本验收要证明的事：非特权采集与非特权 ICMP（spec §13 第 2 项）。
[ "$(id -u)" != 0 ] || { echo "run macos-accept.sh as a normal user" >&2; exit 2; }

VERSION=v0.0.0-macos-accept
PORT=18087
BLOB_PORT=18088
base="http://127.0.0.1:$PORT"
uid=$(id -u)
label="xyz.probe.agent.accept.$$"
work=$(mktemp -d)
echo "work=$work"
admin_pw="macos accept password 2026"
hub=""; agent=""; blob=""; job=""; hub_starts=0

cleanup() {
  if [ -n "$job" ]; then launchctl bootout "gui/$uid/$label" > /dev/null 2>&1 || true; fi
  for p in $agent $blob $hub; do kill "$p" 2>/dev/null || true; wait "$p" 2>/dev/null || true; done
}
trap cleanup EXIT

if [ "$(sysctl -in hw.optional.arm64)" = 1 ]; then arch=arm64; else arch=amd64; fi

make release VERSION="$VERSION" > "$work/release.log" 2>&1 || { echo "FAIL: make release"; tail -20 "$work/release.log"; exit 1; }
for a in amd64 arm64; do
  mkdir -p "$work/pkg-$a"
  tar -xzf "dist/probe-agent_darwin_$a.tar.gz" -C "$work/pkg-$a"
  [ -x "$work/pkg-$a/probe-agent" ] && [ -f "$work/pkg-$a/xyz.probe.agent.plist" ] || { echo "FAIL: darwin/$a package contents"; ls -l "$work/pkg-$a"; exit 1; }
done
bin="$work/pkg-$arch/probe-agent"
[ "$("$bin" version)" = "$VERSION" ] || { echo "FAIL: $arch binary version"; exit 1; }
# Apple Silicon 上 amd64 产物经 Rosetta 执行：Linux 上交叉编译的 Mach-O 也必须能在 macOS 上起来（ad-hoc 签名由 Go 链接器写入）。
if [ "$arch" = arm64 ]; then
  if arch -x86_64 /usr/bin/true 2>/dev/null; then
    [ "$(arch -x86_64 "$work/pkg-amd64/probe-agent" version)" = "$VERSION" ] || { echo "FAIL: amd64 binary under Rosetta"; exit 1; }
  else
    echo "note: Rosetta is not installed; the amd64 binary was not executed"
  fi
fi
plutil -lint "$work/pkg-$arch/xyz.probe.agent.plist" > /dev/null || { echo "FAIL: packaged plist does not lint"; exit 1; }

go build -o "$work/probe-hub" ./cmd/hub
"$work/probe-hub" window open --db "$work/hub.db" --ttl 10m --max 1 > "$work/window.txt"
key=$(sed -n 's/^key: //p' "$work/window.txt")
[ -n "$key" ] || { echo "FAIL: no window key"; exit 1; }

# TTL 12s → 上报间隔 4s、agent 退避上限 12s（spec §4.4）。
start_hub() {
  PROBE_OFFLINE_AFTER=12s "$work/probe-hub" serve --db "$work/hub.db" --listen "127.0.0.1:$PORT" --timezone UTC >> "$work/hub.log" 2>&1 &
  hub=$!
  i=0
  until [ "$(curl -s -o /dev/null -w '%{http_code}' "$base/")" = 302 ]; do
    i=$((i + 1)); [ "$i" -lt 50 ] || { echo "FAIL: hub did not answer"; cat "$work/hub.log"; exit 1; }
    sleep 0.2
  done
  # 302 也可能来自占着端口的别的进程：每起一次，日志里就必须多一行 listening。
  hub_starts=$((hub_starts + 1))
  [ "$(grep -c 'hub listening' "$work/hub.log")" = "$hub_starts" ] || { echo "FAIL: hub did not bind $PORT"; cat "$work/hub.log"; exit 1; }
}
stop_hub() { kill "$hub"; wait "$hub" || true; hub=""; }
start_hub
printf '%s\n' "$admin_pw" | "$work/probe-hub" passwd --db "$work/hub.db" > "$work/passwd.log" 2>&1

: > "$work/jar"
rpc() {
  name=$1; body=$2
  curl -sS -o "$work/$name.json" -w '%{http_code}' -H 'Content-Type: application/json' \
    -b "$work/jar" -c "$work/jar" --data "$body" "$base/probe.v1.AdminService/$name"
}
login() { [ "$(rpc Login "$(jq -nc --arg p "$admin_pw" '{password: $p}')")" = 200 ] || { echo "FAIL: login"; exit 1; }; }
login
# until_node <秒> <jq 表达式>：在上限内轮询快照，直到唯一节点满足表达式。
until_node() {
  deadline=$(($(date +%s) + $1))
  until [ "$(rpc GetSnapshot '{}')" = 200 ] && jq -e ".nodes | length == 1 and (.[0] | $2)" "$work/GetSnapshot.json" > /dev/null; do
    [ "$(date +%s)" -lt "$deadline" ] || { echo "FAIL: node did not satisfy $2 within $1s"; cat "$work/GetSnapshot.json"; exit 1; }
    sleep 0.5
  done
}

"$bin" register --hub "$base" --key "$key" --config "$work/agent.json" --name macos-accept > "$work/register.log" 2>&1 || { echo "FAIL: register"; cat "$work/register.log"; exit 1; }
"$bin" run --config "$work/agent.json" > "$work/agent.log" 2>&1 &
agent=$!

# cpu_pct 与 net_*_bps 要两次采样才有；第二次上报后每个字段都必须在。
until_node 60 '.online and .metrics.cpuPct != null and .metrics.netRxBps != null'
jq -e '.nodes[0].metrics | [.bootId, .cpuPct, .load1, .load5, .load15, .memTotal, .memUsed, .swapTotal, .swapUsed,
  .diskTotal, .diskUsed, .netRxTotal, .netTxTotal, .netRxBps, .netTxBps, .tcpConns, .udpConns, .procs, .uptimeS] | all(. != null)' \
  "$work/GetSnapshot.json" > /dev/null || { echo "FAIL: missing metrics"; cat "$work/GetSnapshot.json"; exit 1; }
jq -e --arg boot "$(sysctl -n kern.bootsessionuuid)" --arg mem "$(sysctl -n hw.memsize)" \
  '.nodes[0].metrics | .bootId == $boot and .memTotal == $mem and (.memUsed | tonumber) <= (.memTotal | tonumber) and (.diskUsed | tonumber) <= (.diskTotal | tonumber)' \
  "$work/GetSnapshot.json" > /dev/null || { echo "FAIL: boot id or memory"; cat "$work/GetSnapshot.json"; exit 1; }
node=$(jq -r '.nodes[0].id' "$work/GetSnapshot.json")

[ "$(rpc ListNodes '{}')" = 200 ] || { echo "FAIL: ListNodes"; exit 1; }
jq -e --arg os "macOS $(sw_vers -productVersion)" --arg kernel "$(uname -r)" --arg arch "$arch" \
  --arg cpu "$(sysctl -n machdep.cpu.brand_string)" --argjson cores "$(sysctl -n hw.logicalcpu)" --arg v "$VERSION" \
  '.nodes[0].facts | .os == $os and .kernel == $kernel and .arch == $arch and .cpuModel == $cpu and .cpuCores == $cores and .agentVersion == $v and .icmpAvailable == true' \
  "$work/ListNodes.json" > /dev/null || { echo "FAIL: facts"; cat "$work/ListNodes.json"; exit 1; }

# 非特权数据报 ICMP 探测回环、TCP 连 hub：两个任务都要有成功的样本且没有 error。
icmp_body=$(jq -nc --arg n "$node" '{task: {kind: "PROBE_KIND_ICMP", target: "127.0.0.1", intervalS: 5, timeoutMs: 1000}, nodeIds: [$n]}')
[ "$(rpc SaveProbeTask "$icmp_body")" = 200 ] || { echo "FAIL: SaveProbeTask icmp"; cat "$work/SaveProbeTask.json"; exit 1; }
icmp_task=$(jq -r '.task.task.id' "$work/SaveProbeTask.json")
tcp_body=$(jq -nc --arg n "$node" --arg t "127.0.0.1:$PORT" '{task: {kind: "PROBE_KIND_TCP", target: $t, intervalS: 5, timeoutMs: 1000}, nodeIds: [$n]}')
[ "$(rpc SaveProbeTask "$tcp_body")" = 200 ] || { echo "FAIL: SaveProbeTask tcp"; cat "$work/SaveProbeTask.json"; exit 1; }
tcp_task=$(jq -r '.task.task.id' "$work/SaveProbeTask.json")
now=$(date +%s)
probe_body=$(jq -nc --arg n "$node" --argjson from "$((now - 600))" --argjson to "$((now + 600))" '{nodeId: $n, from: $from, to: $to, maxPoints: 100}')
# 结果在分钟桶刷出后才可查：最坏约一分钟加首次偏移与上报间隔。
deadline=$(($(date +%s) + 90))
until [ "$(rpc QueryProbes "$probe_body")" = 200 ] && jq -e --arg i "$icmp_task" --arg t "$tcp_task" \
  '(.series // []) as $s | ([$s[].taskId] | sort) == ([$i, $t] | sort) and all($s[]; any(.samples[]; .sent > 0 and .rttMeanUs != null and (.errors // 0) == 0))' \
  "$work/QueryProbes.json" > /dev/null; do
  [ "$(date +%s)" -lt "$deadline" ] || { echo "FAIL: probe results"; cat "$work/QueryProbes.json" "$work/agent.log"; exit 1; }
  sleep 2
done
echo "icmp and tcp probe results present"

# 流量差分依赖 boot_id 在 agent 重启之间不变（spec §7）：agent 停着时在回环上走一批字节，
# 重启后的首次上报必须把它们计入总量。boot_id 若随进程变化，hub 会只重置基线而丢掉这批字节。
kill "$agent"; wait "$agent" || true; agent=""
"$bin" run --config "$work/agent.json" --net-include lo0 >> "$work/agent.log" 2>&1 &
agent=$!
sleep 9
kill "$agent"; wait "$agent" || true; agent=""
[ "$(rpc GetSnapshot '{}')" = 200 ] || exit 1
before=$(jq -r '.nodes[0].traffic.totalRx // "0"' "$work/GetSnapshot.json")
mkdir -p "$work/blob"
dd if=/dev/zero of="$work/blob/64m" bs=1048576 count=64 2> /dev/null
python3 -m http.server "$BLOB_PORT" --bind 127.0.0.1 --directory "$work/blob" > "$work/blob.log" 2>&1 &
blob=$!
i=0
# 就绪前的连接失败是预期的，只在超过上限时看日志；下载本身由 -f 判定。
until curl -fs -o /dev/null "http://127.0.0.1:$BLOB_PORT/64m"; do
  i=$((i + 1)); [ "$i" -lt 50 ] || { echo "FAIL: blob server"; cat "$work/blob.log"; exit 1; }
  sleep 0.2
done
kill "$blob"; wait "$blob" 2> /dev/null || true; blob=""
"$bin" run --config "$work/agent.json" --net-include lo0 >> "$work/agent.log" 2>&1 &
agent=$!
until_node 30 "(.traffic.totalRx | tonumber) >= ($before | tonumber) + 67108864"
echo "traffic across agent restart: $before -> $(jq -r '.nodes[0].traffic.totalRx' "$work/GetSnapshot.json")"
kill "$agent"; wait "$agent" || true; agent=""

# launchd 用户域：plist 取自发布包，只改 Label、路径，并去掉只对 system 域有效的 UserName/GroupName；
# KeepAlive 与 ThrottleInterval 原样保留，验证的就是它们。日志目录事先建好、文件不建，看 launchd 是否创建。
job_plist="$work/$label.plist"
cp "$work/pkg-$arch/xyz.probe.agent.plist" "$job_plist"
plutil -replace Label -string "$label" "$job_plist"
plutil -replace ProgramArguments -json "[\"$bin\", \"run\", \"--config\", \"$work/agent.json\"]" "$job_plist"
plutil -remove UserName "$job_plist"
plutil -remove GroupName "$job_plist"
mkdir -p "$work/logs"
plutil -replace StandardOutPath -string "$work/logs/probe-agent.log" "$job_plist"
plutil -replace StandardErrorPath -string "$work/logs/probe-agent.err" "$job_plist"
plutil -lint "$job_plist" > /dev/null
t_job=$(date +%s)
launchctl bootstrap "gui/$uid" "$job_plist"
job=1
job_pid() { launchctl print "gui/$uid/$label" 2> /dev/null | awk '$1 == "pid" && $2 == "=" { print $3; exit }'; }
# wait_new_pid <旧 pid> <秒>：等到作业换成另一个 pid，打印新 pid。
wait_new_pid() {
  deadline=$(($(date +%s) + $2))
  while :; do
    p=$(job_pid)
    if [ -n "$p" ] && [ "$p" != "$1" ]; then echo "$p"; return 0; fi
    [ "$(date +%s)" -lt "$deadline" ] || return 1
    sleep 0.2
  done
}
pid1=$(wait_new_pid none 15) || { echo "FAIL: launchd did not start the job"; launchctl print "gui/$uid/$label"; exit 1; }
# 节点在上一个 agent 停下后仍在 TTL 内显示在线；以作业启动之后的上报时刻为准。
until_node 30 "(.lastSeenAt | tonumber) > $t_job"
[ -f "$work/logs/probe-agent.err" ] && [ -f "$work/logs/probe-agent.log" ] || { echo "FAIL: launchd did not create the log files"; ls -l "$work/logs"; exit 1; }
grep -q 'agent starting' "$work/logs/probe-agent.err" || { echo "FAIL: agent log not in StandardErrorPath"; cat "$work/logs/probe-agent.err"; exit 1; }

# hub 停 15 秒：agent 在进程内退避，不能退出让 launchd 拉起（pid 不变），hub 回来后在退避上限内重新在线。
stop_hub
sleep 15
[ "$(job_pid)" = "$pid1" ] || { echo "FAIL: agent process changed while the hub was down"; exit 1; }
start_hub
login
until_node 30 '.online'
[ "$(job_pid)" = "$pid1" ] || { echo "FAIL: agent process changed across the hub outage"; exit 1; }

# 进程被杀：KeepAlive 拉起。第二次在拉起后立刻再杀，下一次拉起受 ThrottleInterval 节流：
# 与上一次拉起相隔约 5 秒（默认值 10 秒会落在区间外）。
kill -9 "$pid1"
pid2=$(wait_new_pid "$pid1" 15) || { echo "FAIL: KeepAlive did not restart the agent"; exit 1; }
t2=$(date +%s)
kill -9 "$pid2"
pid3=$(wait_new_pid "$pid2" 20) || { echo "FAIL: KeepAlive gave up after a quick second exit"; exit 1; }
gap=$(($(date +%s) - t2))
[ "$gap" -ge 3 ] && [ "$gap" -le 8 ] || { echo "FAIL: respawn after a quick exit took ${gap}s, want about 5 (ThrottleInterval)"; exit 1; }
t3=$(date +%s)
until_node 30 "(.lastSeenAt | tonumber) >= $t3"
echo "launchd: pids $pid1 -> $pid2 -> $pid3, throttled respawn ${gap}s"

# 普通用户下每个来源都读得到：采集失败只让字段缺失并记日志（不以失败显形），所以直接查日志。
if grep -h 'partial collection' "$work/agent.log" "$work/logs/probe-agent.err"; then echo "FAIL: collection errors as a normal user"; exit 1; fi

launchctl bootout "gui/$uid/$label"
job=""
sleep 1
if kill -0 "$pid3" 2> /dev/null; then echo "FAIL: agent survived bootout"; exit 1; fi
echo "MACOS ACCEPT OK ($arch)"
