#!/bin/sh
# install.sh 与服务单元的真机验收：只在 OrbStack 真实启动的机器上跑，不进 CI。
# 机器名 pia- 前缀是隔离边界；只删除本 run 创建的机器（逐台登记）。
# 端口 18085/18086 与 e2e 的 18080/18081 错开，两者可同时跑。
set -eu
cd "$(cd "$(dirname "$0")/.." && pwd)"

TIER2=0
ONLY=""
usage() { echo "usage: install-accept.sh [--tier2] [--only DISTRO-ARCH]..." >&2; exit 2; }
while [ $# -gt 0 ]; do
  case "$1" in
    --tier2) TIER2=1; shift;;
    --only)
      [ "$#" -ge 2 ] || usage
      case "$2" in
        debian-amd64|debian-arm64|alpine-amd64|alpine-arm64|ubuntu-amd64|ubuntu-arm64|rocky-amd64|rocky-arm64) ;;
        *) echo "unknown cell: $2" >&2; usage;;
      esac
      ONLY="$ONLY $2"
      shift 2;;
    *) usage;;
  esac
done

# OrbStack 镜像写法。运行时可用同名环境变量覆盖。二级镜像只在 --tier2 时使用。
IMG_DEBIAN=${IMG_DEBIAN:-debian:12}
IMG_ALPINE=${IMG_ALPINE:-alpine:3.21}
IMG_UBUNTU=${IMG_UBUNTU:-ubuntu:24.04}
IMG_ROCKY=${IMG_ROCKY:-rocky:9}

VERSION_A=${VERSION_A:-v0.0.0-accept-a}
VERSION_B=${VERSION_B:-v0.0.0-accept-b}
HUB_PORT=18085
DIST_PORT=18086
HOST=host.orb.internal
work=$(mktemp -d)
echo "work=$work"
: > "$work/machines"
hub=""; httpd=""; rootrun=""
admin_pw="accept admin password 2026"

cleanup() {
  if [ -n "$httpd" ]; then kill "$httpd" 2>/dev/null || true; wait "$httpd" 2>/dev/null || true; fi
  if [ -n "$hub" ]; then kill "$hub" 2>/dev/null || true; wait "$hub" 2>/dev/null || true; fi
  if [ -n "$rootrun" ]; then kill "$rootrun" 2>/dev/null || true; wait "$rootrun" 2>/dev/null || true; fi
  while read -r m; do orb delete -f "$m" > /dev/null 2>&1 || true; done < "$work/machines"
}
trap cleanup EXIT

# 两个版本各打一包再复制走：第二次 make release 会清空 dist/，重跑必须能证出版本从 A 变成 B。
make release VERSION="$VERSION_A" > "$work/release-a.log" 2>&1 || { echo "FAIL: make release A"; tail -20 "$work/release-a.log"; exit 1; }
mkdir -p "$work/dist/a"
cp dist/probe-*.tar.gz dist/SHA256SUMS dist/install.sh "$work/dist/a/"
make release VERSION="$VERSION_B" > "$work/release-b.log" 2>&1 || { echo "FAIL: make release B"; tail -20 "$work/release-b.log"; exit 1; }
mkdir -p "$work/dist/b"
cp dist/probe-*.tar.gz dist/SHA256SUMS dist/install.sh "$work/dist/b/"
make binaries > "$work/binaries.log" 2>&1 || { echo "FAIL: make binaries"; tail -20 "$work/binaries.log"; exit 1; }
# 本 run 的标记，HTTP 起来后核对它。install.sh 与包内文件在 deploy/ 未改动时各轮逐字相同，
# 比对被测文件区分不了本轮的服务与上一轮遗留的服务；$work 由本轮的 mktemp 产生，各轮必然不同。
printf '%s\n' "$work" > "$work/dist/run-id"

# 每格 2 个节点（服务 + root 对照）。窗口名额必须盖住本 run 会注册的节点，否则后一格 register 被拒。
# --only 再留 1 个名额：单格注入若把重跑改成再次注册，窗口要接得住，节点数断言才看得到；
# 名额刚好用完时这次 register 会被拒成 unauthenticated，断言到不了。
if [ -n "$ONLY" ]; then
  max_nodes=0
  cells=0
  for id in $ONLY; do cells=$((cells + 1)); max_nodes=$((max_nodes + 2)); done
  max_nodes=$((max_nodes + cells))
else
  max_nodes=8
  [ "$TIER2" = 1 ] && max_nodes=16
fi
bin/probe-hub window open --db "$work/accept.db" --ttl 90m --max "$max_nodes" > "$work/window.txt" 2>&1
key=$(sed -n 's/^key: //p' "$work/window.txt")
[ -n "$key" ] || { echo "FAIL: no window key"; exit 1; }

# 用 hub 默认 30s TTL（上报间隔 10s）。3 分钟 TTL 会把间隔抬到 1 分钟：
# 35 秒的 root 对照凑不齐两次 CPU 采样，90 秒也等不到任务下发后再上报的探测结果。
bin/probe-hub serve --db "$work/accept.db" --listen "127.0.0.1:$HUB_PORT" --timezone UTC > "$work/hub.log" 2>&1 &
hub=$!
attempt=0
while [ "$attempt" -lt 50 ]; do
  [ "$(curl -s -o /dev/null -w '%{http_code}' "http://127.0.0.1:$HUB_PORT/")" = 302 ] && break
  attempt=$((attempt + 1)); sleep 0.2
done
# 302 也可能是别人占着 18085。本进程没打出 listening 就不是这次的 hub。
grep -q 'hub listening' "$work/hub.log" || { echo "FAIL: hub did not bind $HUB_PORT"; cat "$work/hub.log"; exit 1; }
printf '%s\n' "$admin_pw" | bin/probe-hub passwd --db "$work/accept.db" > "$work/passwd.log" 2>&1

# HTTP 以 $work/dist 为根，/a 与 /b 是两个版本目录。
# exec 让 $httpd 就是 python：子 shell 被 kill 后 python 会被 init 收养并继续占着端口，
# 下一轮会装到上一轮的包。起来之后核对应答的是本 run 的标记，端口被占时立即失败。
(cd "$work/dist" && exec python3 -m http.server "$DIST_PORT" --bind 127.0.0.1) > "$work/httpd.log" 2>&1 &
httpd=$!
attempt=0
while [ "$attempt" -lt 50 ]; do
  curl -fsS -o "$work/served-run-id" "http://127.0.0.1:$DIST_PORT/run-id" 2>/dev/null && break
  attempt=$((attempt + 1)); sleep 0.2
done
cmp -s "$work/dist/run-id" "$work/served-run-id" || { echo "FAIL: dist HTTP is not serving this run"; cat "$work/httpd.log"; exit 1; }

: > "$work/jar"
base="http://127.0.0.1:$HUB_PORT"
rpc() {
  name=$1; body=$2
  curl -sS -o "$work/$name.json" -w '%{http_code}' -H 'Content-Type: application/json' \
    -b "$work/jar" -c "$work/jar" --data "$body" "$base/probe.v1.AdminService/$name"
}
[ "$(rpc Login "$(jq -nc --arg password "$admin_pw" '{password: $password}')")" = 200 ] || { echo "FAIL: login"; exit 1; }

node_field() { # node_field <node-name> <jq-expr> — 轮询直到 expr 为真（60 次 × 1s）
  i=0
  until [ "$(rpc GetSnapshot '{}')" = 200 ] && jq -e --arg n "$1" ".nodes[] | select(.name == \$n) | $2" "$work/GetSnapshot.json" > /dev/null 2>&1; do
    i=$((i + 1)); [ "$i" -lt 60 ] || return 1
    sleep 1
  done
}
list_nodes() { [ "$(rpc ListNodes '{}')" = 200 ]; }

# 与 install.sh 的 confirm_service_stopped 同一判据：有效 uid 等于 probe-agent 的进程就是本服务的进程。
# 前提由 create_account（专供 agent 的 nologin 账户）与服务定义（systemd User=、OpenRC command_user）保证；
# 服务定义若改以其他身份运行，两边要一起改。
# 不按名字。Alpine 3.21 / BusyBox 1.37.0 / OpenRC 0.55.1 实测：pidof probe-agent 还会列出 pidof 自己
# （comm=pidof，cmdline 含参数 probe-agent，有效 uid 0，exe 是 /bin/busybox）。同机 supervise-daemon
# 的 /proc/comm 是 supervise-daemo（15 字节截断）、有效 uid 0，不在那份 pidof 输出里。
# 按有效 uid 扫描时 root 对照进程不在结果里。
# 有效 uid（Uid 行第三列，与扫描用的同一列）不为 0，CapEff 含 bit 13（CAP_NET_RAW = 0x2000）。
# 服务进程所见的根目录与 init 所见的在同一个文件系统上（设备号相同）：加固项不得让 agent 统计到另一个根分区。
# 不拿两次上报的根分区总量比数值：OrbStack 机器的根是 btrfs，实测同一格里服务与 root 对照相隔 5 秒的
# 两次上报总量相差约 2.4 GiB 而已用量相同；同一时刻在带同样加固的 systemd-run 与普通环境里 statfs 结果相同。
# ping_group_range 写进日志：所测机器上组范围关闭时，ICMP 可用应来自能力而不是组范围。
assert_service_identity() {
  cell=$1
  orb -m "$cell" -u root sh -c '
    svc_uid=$(id -u probe-agent) || { echo "no probe-agent user"; exit 1; }
    pids=""
    for s in /proc/[0-9]*/status; do
      if euid=$(awk "\$1 == \"Uid:\" { print \$3; exit }" "$s" 2>/dev/null); then
        if [ "$euid" = "$svc_uid" ]; then p=${s#/proc/}; pids="$pids ${p%/status}"; fi
      elif [ -e "$s" ]; then
        echo "cannot read $s"; exit 1
      fi
    done
    set -- $pids
    [ "$#" -eq 1 ] || { echo "want exactly one process with euid $svc_uid (probe-agent), got $#:$pids"; exit 1; }
    pid=$1
    uid=$(awk "/^Uid:/ {print \$3; exit}" "/proc/$pid/status")
    cap=$(awk "/^CapEff:/ {print \$2; exit}" "/proc/$pid/status")
    echo "pid=$pid uid=$uid CapEff=$cap ping_group_range=$(cat /proc/sys/net/ipv4/ping_group_range)"
    [ "$uid" != 0 ] || exit 1
    rootdev=$(stat -c %d "/proc/$pid/root/") || { echo "cannot stat /proc/$pid/root"; exit 1; }
    initdev=$(stat -c %d /) || exit 1
    echo "rootdev=$rootdev initdev=$initdev"
    [ "$rootdev" = "$initdev" ] || { echo "service root is on another filesystem"; exit 1; }
    [ $((0x$cap & 0x2000)) -ne 0 ] || exit 1
  ' > "$work/ident-$cell.log" 2>&1 || { echo "FAIL($cell): service identity"; cat "$work/ident-$cell.log"; exit 1; }
  cat "$work/ident-$cell.log"
}

# 配置目录必须是 root:probe-agent 0750：服务用户能读配置，但不能增删目录项，
# root 对配置文件的后续操作才不会被链接劫持。配置文件属主必须是 probe-agent、0600：
# register 以 root 写入，不改属主服务就读不到。这两条由 install.sh 在 register 之后保证。
# OpenRC 日志目录必须保持 root:root 0755：服务用户能增删目录项时，root 按路径做的改属主可以被换成别的文件。
# 两个日志文件属主 probe-agent、0640，由 start_pre 的 checkpath 每次启动建立，不靠安装时建一次。
assert_layout() {
  cell=$1
  logs=$2
  tag=$3
  orb -m "$cell" -u root sh -c '
    fail=0
    want() {
      path=$1; expect=$2
      got=$(stat -c "%U:%G %a" "$path") || { echo "stat failed: $path"; fail=1; return; }
      echo "$path $got"
      [ "$got" = "$expect" ] || { echo "want $path $expect"; fail=1; }
    }
    want_owner() {
      path=$1; user=$2; mode=$3
      full=$(stat -c "%U:%G %a" "$path") || { echo "stat failed: $path"; fail=1; return; }
      got=$(stat -c "%U %a" "$path") || { echo "stat failed: $path"; fail=1; return; }
      echo "$path $full"
      [ "$got" = "$user $mode" ] || { echo "want $path owner $user mode $mode"; fail=1; }
    }
    want /etc/probe-agent "root:probe-agent 750"
    want_owner /etc/probe-agent/config.json probe-agent 600
    if [ "'"$logs"'" = 1 ]; then
      want /var/log/probe-agent "root:root 755"
      want_owner /var/log/probe-agent/probe-agent.log probe-agent 640
      want_owner /var/log/probe-agent/probe-agent.err probe-agent 640
    fi
    exit "$fail"
  ' > "$work/$tag-$cell.log" 2>&1 || { echo "FAIL($cell): ownership"; cat "$work/$tag-$cell.log"; exit 1; }
  cat "$work/$tag-$cell.log"
}

run_cell() {
  img=$1; distro=$2; arch=$3
  name="pia-$distro-$arch"
  echo "== cell $name =="
  orb create -a "$arch" "$img" "$name" > "$work/create-$name.log" 2>&1 || { echo "FAIL($name): orb create"; exit 1; }
  echo "$name" >> "$work/machines"

  # 首次安装用面板命令的管道形态。无 curl 时用 wget（运行时再探一次，不把探测结果写死）；
  # 重跑下载版本 B 的 install.sh 用同一次探测的结果。
  if orb -m "$name" -u root command -v curl >/dev/null 2>&1; then
    fetch="curl -fsSL http://$HOST:$DIST_PORT/a/install.sh"
    fetch_b="curl -fsSL -o /root/install.sh http://$HOST:$DIST_PORT/b/install.sh"
  else
    fetch="wget -qO- http://$HOST:$DIST_PORT/a/install.sh"
    fetch_b="wget -q -O /root/install.sh http://$HOST:$DIST_PORT/b/install.sh"
  fi
  orb -m "$name" -u root sh -c "$fetch | sh -s -- --hub http://$HOST:$HUB_PORT --key $key --base-url http://$HOST:$DIST_PORT/a" \
    > "$work/install-$name.log" 2>&1 || { echo "FAIL($name): install"; tail -20 "$work/install-$name.log"; exit 1; }

  node_field "$name" '.online == true and .metrics.cpuPct != null' || { echo "FAIL($name): node did not come online"; exit 1; }
  assert_service_identity "$name"
  case "$distro" in
    alpine) assert_layout "$name" 1 layout;;
    *) assert_layout "$name" 0 layout;;
  esac

  list_nodes || { echo "FAIL($name): ListNodes"; exit 1; }
  jq -e --arg n "$name" --arg v "$VERSION_A" '.nodes[] | select(.name == $n) | .facts.icmpAvailable == true and .facts.agentVersion == $v' "$work/ListNodes.json" > /dev/null \
    || { echo "FAIL($name): icmpAvailable or agentVersion A"; jq -r --arg n "$name" '.nodes[] | select(.name == $n) | .facts' "$work/ListNodes.json"; exit 1; }

  # ICMP 任务下发并等到有结果：能力由 init 授予，非 root 服务必须有可用 ICMP。
  node_id=$(jq -r --arg n "$name" '.nodes[] | select(.name == $n) | .id' "$work/ListNodes.json")
  [ "$(rpc SaveProbeTask "$(jq -nc --arg id "$node_id" '{task: {kind: "PROBE_KIND_ICMP", target: "127.0.0.1", intervalS: 5, timeoutMs: 1000}, nodeIds: [$id]}')")" = 200 ] \
    || { echo "FAIL($name): SaveProbeTask"; cat "$work/SaveProbeTask.json"; exit 1; }
  task_id=$(jq -r '.task.task.id' "$work/SaveProbeTask.json")
  now=$(date +%s)
  qbody=$(jq -nc --arg id "$node_id" --argjson from "$((now - 600))" --argjson to "$((now + 120))" '{nodeId: $id, from: $from, to: $to, maxPoints: 20}')
  i=0
  # 还没有结果时 series 不出现；any(.series[]) 会报错而不是假，空窗必须当成还没到。
  until [ "$(rpc QueryProbes "$qbody")" = 200 ] && jq -e --arg t "$task_id" 'any(.series[]?; .taskId == $t and any(.samples[]?; .sent > 0 and (.errors // 0) == 0))' "$work/QueryProbes.json" > /dev/null; do
    i=$((i + 1)); [ "$i" -lt 45 ] || { echo "FAIL($name): no ICMP results"; cat "$work/QueryProbes.json"; exit 1; }
    sleep 2
  done

  # 加固不得让采集缩水：字段集合、内存总量与 bootId 与 root 对照一致；根分区是不是同一个由
  # assert_service_identity 按设备号判定。网卡只以合计计数器上报，逐网卡集合经接口观测不到，不作断言（§12）。
  orb -m "$name" -u root /usr/local/bin/probe-agent register --hub "http://$HOST:$HUB_PORT" --key "$key" \
    --config /root/root-agent.json --name "$name-root" > "$work/regroot-$name.log" 2>&1 || { echo "FAIL($name): root register"; exit 1; }
  orb -m "$name" -u root timeout 35 /usr/local/bin/probe-agent run --config /root/root-agent.json > "$work/runroot-$name.log" 2>&1 &
  rootrun=$!
  node_field "$name-root" '.online == true and .metrics.cpuPct != null' || { echo "FAIL($name): root node did not come online"; exit 1; }
  jq -e --arg a "$name" --arg b "$name-root" '
    ([.nodes[] | select(.name == $a) | .metrics][0]) as $ma |
    ([.nodes[] | select(.name == $b) | .metrics][0]) as $mb |
    ($ma | keys | sort) == ($mb | keys | sort) and
    $ma.memTotal == $mb.memTotal and $ma.bootId == $mb.bootId' \
    "$work/GetSnapshot.json" > /dev/null || { echo "FAIL($name): service/root metrics mismatch"; cat "$work/GetSnapshot.json"; exit 1; }
  # timeout 35 到点结束 root 对照，退出码非零是预期的；等完即清空，cleanup 不再去 kill 一个可能已被复用的 pid。
  wait "$rootrun" || true
  rootrun=""

  # 重跑即升级：文件形态，base-url 指向版本 B。沿用注册；版本必须变成 B，节点数不变。
  list_nodes || { echo "FAIL($name): ListNodes before rerun"; exit 1; }
  before=$(jq '[.nodes[] | select(.name | startswith("'"$name"'"))] | length' "$work/ListNodes.json")
  orb -m "$name" -u root sh -c "$fetch_b" \
    > "$work/fetchb-$name.log" 2>&1 || { echo "FAIL($name): fetch rerun install.sh"; exit 1; }
  orb -m "$name" -u root sh /root/install.sh --hub "http://$HOST:$HUB_PORT" --key "$key" --base-url "http://$HOST:$DIST_PORT/b" \
    > "$work/rerun-$name.log" 2>&1 || { echo "FAIL($name): rerun"; tail -20 "$work/rerun-$name.log"; exit 1; }
  grep -q 'keeping the current registration' "$work/rerun-$name.log" || { echo "FAIL($name): rerun did not keep registration"; exit 1; }
  list_nodes || { echo "FAIL($name): ListNodes after rerun"; exit 1; }
  after=$(jq '[.nodes[] | select(.name | startswith("'"$name"'"))] | length' "$work/ListNodes.json")
  [ "$before" = "$after" ] || { echo "FAIL($name): node count changed on rerun ($before -> $after)"; exit 1; }
  # 新进程的第一次上报才带上新版本；安装脚本返回时这次上报通常还没到。
  i=0
  until list_nodes && jq -e --arg n "$name" --arg v "$VERSION_B" '.nodes[] | select(.name == $n) | .facts.agentVersion == $v' "$work/ListNodes.json" > /dev/null; do
    i=$((i + 1)); [ "$i" -lt 60 ] || { echo "FAIL($name): agentVersion after rerun is not B"; exit 1; }
    sleep 1
  done

  case "$distro" in
    debian|ubuntu|rocky)
      orb -m "$name" -u root systemd-analyze verify /etc/systemd/system/probe-agent.service > "$work/verify-$name.log" 2>&1 \
        || { echo "FAIL($name): systemd-analyze verify"; cat "$work/verify-$name.log"; exit 1; };;
    alpine)
      # 删日志目录后重启仍须健康：start_pre 每次启动都建，不靠安装时建一次。
      orb -m "$name" -u root rm -rf /var/log/probe-agent
      orb -m "$name" -u root rc-service probe-agent restart > "$work/logrestart-$name.log" 2>&1 \
        || { echo "FAIL($name): restart after log dir removed"; cat "$work/logrestart-$name.log"; exit 1; }
      node_field "$name" '.online == true and .metrics.cpuPct != null' || { echo "FAIL($name): not healthy after log restart"; exit 1; }
      assert_layout "$name" 1 layout-restart
      # 重启机器后自启。last_seen_at 在 JSON 里是 lastSeenAt，int64 编码为字符串（§5.6）。
      # 同时验证 depend() 的 after net 在真机开机顺序下能自启。
      seen=$(jq -r --arg n "$name" '.nodes[] | select(.name == $n) | .lastSeenAt' "$work/GetSnapshot.json")
      orb stop "$name"
      orb start "$name"
      i=0
      until [ "$(rpc GetSnapshot '{}')" = 200 ] && jq -e --arg n "$name" --arg s "$seen" '.nodes[] | select(.name == $n) | .online == true and (.lastSeenAt | tonumber) > ($s | tonumber)' "$work/GetSnapshot.json" > /dev/null 2>&1; do
        i=$((i + 1)); [ "$i" -lt 120 ] || { echo "FAIL($name): did not come back after reboot"; exit 1; }
        sleep 1
      done
      assert_service_identity "$name";;
  esac

  orb -m "$name" -u root sh /root/install.sh --uninstall --purge > "$work/uninstall-$name.log" 2>&1 || { echo "FAIL($name): uninstall"; tail -20 "$work/uninstall-$name.log"; exit 1; }
  case "$distro" in
    debian|ubuntu|rocky)
      orb -m "$name" -u root sh -c 'test ! -f /etc/systemd/system/probe-agent.service && ! systemctl is-enabled probe-agent >/dev/null 2>&1' \
        || { echo "FAIL($name): systemd service still present after uninstall"; exit 1; };;
    alpine)
      orb -m "$name" -u root sh -c 'test ! -f /etc/init.d/probe-agent && ! rc-update show default 2>/dev/null | grep -q probe-agent' \
        || { echo "FAIL($name): openrc service still present after uninstall"; exit 1; };;
  esac
  # BusyBox 1.37.0（Alpine 3.21，同机 OpenRC 0.55.1）的 pidof 会把参数 probe-agent 匹配到 pidof 自己。
  # 卸载后用户已删除，不能再按有效 uid 扫；comm 精确等于 probe-agent 才是残留的 agent 进程。
  orb -m "$name" -u root sh -c '
    left=""
    for s in /proc/[0-9]*/comm; do
      c=$(tr -d "\n" < "$s" 2>/dev/null || true)
      if [ "$c" = probe-agent ]; then
        p=${s#/proc/}
        left="$left ${p%/comm}"
      fi
    done
    if [ -n "$left" ]; then echo "leftover probe-agent:$left"; exit 1; fi
    test ! -e /usr/local/bin/probe-agent && ! id probe-agent >/dev/null 2>&1 && ! grep -q "^probe-agent:" /etc/group && test ! -e /etc/probe-agent
  ' || { echo "FAIL($name): purge left process, binary, user, group, or config"; exit 1; }

  orb delete -f "$name" > /dev/null 2>&1
  # grep 把名单滤成空时退出码为 1；set -eu 会把"只剩这一台"当成失败。
  # 名单里只剩本格时 grep 无保留行、退出码为 1，属正常；只放过 1，读不到文件等错误（2）照常失败。
  grep -v "^$name\$" "$work/machines" > "$work/machines.tmp" || [ "$?" = 1 ]
  mv "$work/machines.tmp" "$work/machines"
  echo "== cell $name OK =="
}

run_if() {
  id=$1; tier=$2; img=$3; distro=$4; arch=$5
  if [ -n "$ONLY" ]; then
    case " $ONLY " in
      *" $id "*) ;;
      *) return 0;;
    esac
  elif [ "$tier" != 1 ] && [ "$TIER2" != 1 ]; then
    return 0
  fi
  run_cell "$img" "$distro" "$arch"
}

run_if debian-amd64 1 "$IMG_DEBIAN" debian amd64
run_if debian-arm64 1 "$IMG_DEBIAN" debian arm64
run_if alpine-amd64 1 "$IMG_ALPINE" alpine amd64
run_if alpine-arm64 1 "$IMG_ALPINE" alpine arm64
run_if ubuntu-amd64 2 "$IMG_UBUNTU" ubuntu amd64
run_if ubuntu-arm64 2 "$IMG_UBUNTU" ubuntu arm64
run_if rocky-amd64 2 "$IMG_ROCKY" rocky amd64
run_if rocky-arm64 2 "$IMG_ROCKY" rocky arm64
echo "INSTALL ACCEPT OK"
