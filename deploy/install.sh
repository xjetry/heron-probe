#!/bin/sh
# probe-agent 安装脚本：下载、校验、注册并安装服务；重跑即升级。
# 与 libc 无关由静态链接产物承载（发布流水线的静态门禁保证），本脚本不处理。
# 以 curl … | sh -s -- 运行时，脚本本身来自 stdin。脚本里任何读 stdin 的命令都会吞掉
# 脚本余下部分，安装在中途无声结束。所以每个可能读 stdin 的外部命令都显式 </dev/null。
# 不能用 exec </dev/null：那会切断脚本自己的来源。
set -eu

BIN=/usr/local/bin/probe-agent
CFG_DIR=/etc/probe-agent
CFG=$CFG_DIR/config.json
LOG_DIR=/var/log/probe-agent
SVC_USER=probe-agent
SYSTEMD_WANTS=/etc/systemd/system/multi-user.target.wants/probe-agent.service
REPO=https://github.com/xjetry/probe

usage() {
  echo "usage: install.sh --hub URL --key KEY [--name N] [--version vX.Y.Z] [--base-url URL]" >&2
  echo "       install.sh --uninstall [--purge]" >&2
  exit 2
}

HUB=""; KEY=""; NAME=""; VERSION=""; BASE_URL=""; UNINSTALL=0; PURGE=0
need_value() { [ "$#" -ge 2 ] || usage; }
while [ $# -gt 0 ]; do
  case "$1" in
    --hub) need_value "$@"; HUB=$2; shift 2;;
    --key) need_value "$@"; KEY=$2; shift 2;;
    --name) need_value "$@"; NAME=$2; shift 2;;
    --version) need_value "$@"; VERSION=$2; shift 2;;
    --base-url) need_value "$@"; BASE_URL=$2; shift 2;;
    --uninstall) UNINSTALL=1; shift;;
    --purge) PURGE=1; shift;;
    *) usage;;
  esac
done
[ "$PURGE" = 0 ] || [ "$UNINSTALL" = 1 ] || usage

[ "$(id -u)" = 0 ] || { echo "install.sh must run as root" >&2; exit 1; }

# init 判定只认两个真实标记：都没有时是容器等无 init 环境，报错而不静默降级。
if [ -d /run/systemd/system ]; then INIT=systemd
elif [ -x /sbin/openrc-run ]; then INIT=openrc
else echo "unsupported init: expected systemd (/run/systemd/system) or OpenRC (/sbin/openrc-run)" >&2; exit 1; fi

service_installed() {
  case "$INIT" in
    systemd) [ -e /etc/systemd/system/probe-agent.service ];;
    openrc) [ -e /etc/init.d/probe-agent ];;
  esac
}

# 停服务后确认进程退出的轮询间隔（秒）与次数上限：sleep 合计最多 10 秒，外加每轮一次 /proc 扫描。
# 间隔用整数秒：POSIX 的 sleep 只保证整数，小数在只有整数 sleep 的系统上会让确认本身失败。
STOP_POLL_INTERVAL=1
STOP_POLL_MAX=10
# 启动后等到进程出现的上限，同一口径。出现之后再固定等 3 秒，确认还是同一个 pid。
START_POLL_INTERVAL=1
START_POLL_MAX=10

# 列出有效 uid 为 $1 的进程 pid（空格分隔，写入 svc_pids）。有效 uid 是 /proc/<pid>/status 里 Uid: 行的第三列。
# 枚举与读取之间进程可能已退出，其 status 文件随之消失：读失败且文件已不在的条目按已退出跳过；
# 文件还在却读不到则无法判定，返回失败。
scan_uid_pids() {
  svc_pids=""
  [ -r /proc/self/status ] || { echo "cannot inspect processes: /proc is not mounted" >&2; return 1; }
  for s in /proc/[0-9]*/status; do
    if euid=$(awk '$1 == "Uid:" { print $3; exit }' "$s" 2>/dev/null); then
      if [ "$euid" = "$1" ]; then p=${s#/proc/}; svc_pids="$svc_pids ${p%/status}"; fi
    elif [ -e "$s" ]; then
      echo "cannot read $s" >&2; return 1
    fi
  done
}

# 确认没有以服务用户身份运行的进程。它不依赖服务定义是否安装，也不看 init 报告的状态：
# OpenRC 0.55.1（Alpine 3.21）实测，supervise-daemon 被杀、子进程留存时 stop 打印
# "Unable to shut down the supervisor" 却返回 0，之后 status 为 stopped（3），子进程仍在运行。
# 判据"有效 uid 等于服务用户的 uid"与"是本服务的进程"等价，两个方向各有担保：
# - 以服务用户运行的进程都属于本服务：$SVC_USER 是专供 agent 的 nologin 账户，由 create_account 建立。
# - 本服务的每个进程都以服务用户运行：由 systemd 单元的 User=probe-agent（deploy/systemd/probe-agent.service）
#   与 OpenRC 脚本的 command_user（deploy/openrc/probe-agent）保证。给服务定义加以其他身份运行的进程
#   （例如以 root 执行的 ExecStartPre=+）或改用 DynamicUser= 时，这里会漏查，必须同步改判据。
# 用户不存在时没有可比对的 uid，直接通过。这让以下情形查不到仍在运行的旧进程：
# - 卸载路径不建账户：账户被带外删除后，这里直接通过。
# - 安装路径先由 create_account 重建账户：新账户的 uid 可能与旧进程的不同，按新 uid 查不到它。
# 不假定 stop 返回时进程已被回收：有上限地轮询，超过上限仍在才报错。
confirm_service_stopped() {
  id "$SVC_USER" >/dev/null 2>&1 || return 0
  svc_uid=$(id -u "$SVC_USER")
  polls=0
  while :; do
    scan_uid_pids "$svc_uid" || return 1
    [ -n "$svc_pids" ] || return 0
    [ "$polls" -lt "$STOP_POLL_MAX" ] || break
    sleep "$STOP_POLL_INTERVAL"
    polls=$((polls + 1))
  done
  echo "probe-agent is still running: processes with uid $svc_uid ($SVC_USER):$svc_pids" >&2
  return 1
}

# 启动之后确认服务进程活着。init 的 start 返回 0 证明不了子进程仍在：
# systemd 的 Type=simple 在 fork 之后即视为已启动；OpenRC 上子进程秒退、反复拉起时 status 仍是 started。
# 判据与 confirm_service_stopped 同一处，只调用 scan_uid_pids，不另写一套扫描。
# 先等到出现有效 uid 为服务用户的进程，记下 pid，3 秒后同一个 pid 仍在：
# 秒退再拉起会换成新 pid，不能算起来了。前提与停服务确认相同，由 create_account 与服务定义保证。
start_log_hint() {
  case "$INIT" in
    systemd) echo "see journalctl -u probe-agent" >&2;;
    openrc) echo "see /var/log/probe-agent/probe-agent.err" >&2;;
  esac
}
confirm_service_started() {
  svc_uid=$(id -u "$SVC_USER") || { echo "no service user $SVC_USER" >&2; start_log_hint; return 1; }
  polls=0
  pid=""
  while :; do
    scan_uid_pids "$svc_uid" || return 1
    if [ -n "$svc_pids" ]; then
      pid=${svc_pids# }
      pid=${pid%% *}
      break
    fi
    [ "$polls" -lt "$START_POLL_MAX" ] || break
    sleep "$START_POLL_INTERVAL"
    polls=$((polls + 1))
  done
  if [ -z "$pid" ]; then
    echo "probe-agent did not start" >&2
    start_log_hint
    return 1
  fi
  sleep 3
  scan_uid_pids "$svc_uid" || return 1
  case " $svc_pids " in
    *" $pid "*) return 0;;
  esac
  echo "probe-agent did not stay running (pid $pid)" >&2
  start_log_hint
  return 1
}

# 是否发 stop 以服务定义是否安装为准，stop 命令失败即失败；确认一步不依赖它，总是执行：
# 定义不在而进程仍在时，不替人收拾，由确认失败退出。
stop_service() {
  if service_installed; then
    case "$INIT" in
      systemd) systemctl stop probe-agent </dev/null;;
      openrc) rc-service probe-agent stop </dev/null;;
    esac || { echo "failed to stop probe-agent" >&2; return 1; }
  fi
  confirm_service_stopped
}

# 用户与同名组都删并回查：各发行版删除工具对组的处理不一致，不能信退出码，也不能半成功还报成功。
delete_account() {
  if id "$SVC_USER" >/dev/null 2>&1; then
    if command -v userdel >/dev/null 2>&1; then userdel "$SVC_USER" </dev/null
    elif command -v deluser >/dev/null 2>&1; then deluser "$SVC_USER" </dev/null
    fi
  fi
  if grep -q "^$SVC_USER:" /etc/group; then
    if command -v groupdel >/dev/null 2>&1; then groupdel "$SVC_USER" </dev/null
    elif command -v delgroup >/dev/null 2>&1; then delgroup "$SVC_USER" </dev/null
    fi
  fi
  if id "$SVC_USER" >/dev/null 2>&1 || grep -q "^$SVC_USER:" /etc/group; then
    echo "failed to delete user or group $SVC_USER" >&2; exit 1
  fi
}

if [ "$UNINSTALL" = 1 ]; then
  stop_service
  case "$INIT" in
    systemd)
      if service_installed; then
        systemctl disable probe-agent </dev/null
      fi
      # enable 按单元 [Install] 段的 WantedBy=multi-user.target 建这个链接，两处改一处必须同步。
      # 单元文件被手工删掉时上面不发 disable，链接悬空留下；判链接本身（-L），与 OpenRC 侧同口径。
      if [ -L "$SYSTEMD_WANTS" ]; then
        rm -f "$SYSTEMD_WANTS"
      fi
      rm -f /etc/systemd/system/probe-agent.service
      systemctl daemon-reload </dev/null;;
    openrc)
      # 判链接本身（-L）而不跟随它：init 脚本被删后链接悬空，-e 会判为不存在而留下它。
      # OpenRC 0.55.1 实测：init 脚本不存在时 rc-update del 仍删掉悬空链接并返回 0。
      if [ -L /etc/runlevels/default/probe-agent ]; then
        rc-update del probe-agent default </dev/null
      fi
      rm -f /etc/init.d/probe-agent;;
  esac
  rm -f "$BIN"
  if [ "$PURGE" = 1 ]; then
    rm -rf "$CFG_DIR" "$LOG_DIR"
    delete_account
  fi
  echo "probe-agent uninstalled"
  exit 0
fi

# 卸载不需要架构：不支持的架构不应让人连卸载都做不了。
case "$(uname -m)" in
  x86_64) ARCH=amd64;; aarch64) ARCH=arm64;; armv7l) ARCH=armv7;;
  i386|i686) ARCH=386;; riscv64) ARCH=riscv64;;
  *) echo "unsupported architecture: $(uname -m)" >&2; exit 1;;
esac

# 首次安装必须有注册凭据；升级沿用现有配置，不需要也不接受重新注册。
if [ ! -f "$CFG" ] && { [ -z "$HUB" ] || [ -z "$KEY" ]; }; then
  echo "--hub and --key are required for the first install" >&2
  exit 2
fi

# 建用户排在下载与注册之前：它若失败，注册窗口的名额尚未消耗、旧服务尚未停止。
# 主组必须是同名组：OpenRC 的 command_user 与配置文件属主都写 probe-agent:probe-agent，
# 而 busybox 的 adduser 不指定组时会把用户放进 nogroup。先建组、再以它为主组建用户，建完回查。
create_account() {
  nologin=/sbin/nologin
  [ -x /usr/sbin/nologin ] && nologin=/usr/sbin/nologin
  # 用户已在时先核对主组，再决定要不要建组。顺序反了会在主组不符退出时留下本次新建的同名组。
  if id "$SVC_USER" >/dev/null 2>&1; then
    actual_group=$(id -gn "$SVC_USER" 2>/dev/null) || {
      echo "user $SVC_USER exists but its primary group is missing (gid $(id -g "$SVC_USER"))" >&2; exit 1; }
    [ "$actual_group" = "$SVC_USER" ] || {
      echo "user $SVC_USER exists with primary group $actual_group; expected $SVC_USER" >&2; exit 1; }
    return 0
  fi
  if ! grep -q "^$SVC_USER:" /etc/group; then
    if command -v groupadd >/dev/null 2>&1; then
      groupadd --system "$SVC_USER" </dev/null
    elif command -v addgroup >/dev/null 2>&1; then
      if addgroup --help </dev/null 2>&1 | grep -qi busybox; then addgroup -S "$SVC_USER" </dev/null
      else addgroup --system "$SVC_USER" </dev/null; fi
    else
      echo "no groupadd or addgroup available to create the system group" >&2; exit 1
    fi
  fi
  if ! id "$SVC_USER" >/dev/null 2>&1; then
    if command -v useradd >/dev/null 2>&1; then
      useradd --system -M -d /nonexistent -s "$nologin" -g "$SVC_USER" "$SVC_USER" </dev/null
    elif command -v adduser >/dev/null 2>&1; then
      # adduser 有两种不兼容的实现（busybox 与 Debian perl 版），按实现分支；
      # 建完必须回查——perl 版收到 busybox 风格参数会打印用法、不建用户，却返回 0。
      if adduser --help </dev/null 2>&1 | grep -qi busybox; then
        adduser -S -D -H -s "$nologin" -G "$SVC_USER" "$SVC_USER" </dev/null
      else
        adduser --system --no-create-home --shell "$nologin" --ingroup "$SVC_USER" "$SVC_USER" </dev/null
      fi
    else
      echo "no useradd or adduser available to create the system user" >&2; exit 1
    fi
  fi
  id "$SVC_USER" >/dev/null 2>&1 || { echo "failed to create system user $SVC_USER" >&2; exit 1; }
  actual_group=$(id -gn "$SVC_USER" 2>/dev/null) || {
    echo "user $SVC_USER exists but its primary group is missing (gid $(id -g "$SVC_USER"))" >&2; exit 1; }
  [ "$actual_group" = "$SVC_USER" ] || {
    echo "user $SVC_USER exists with primary group $actual_group; expected $SVC_USER" >&2; exit 1; }
}
create_account

# 给了 --base-url 时它就是下载目录，--version 不参与地址。与 --key 被忽略时一样说出来，
# 避免人以为钉住了版本。
if [ -n "$BASE_URL" ] && [ -n "$VERSION" ]; then
  echo "--base-url is the download directory (--version ignored)"
fi
[ -n "$BASE_URL" ] || {
  if [ -n "$VERSION" ]; then BASE_URL="$REPO/releases/download/$VERSION"
  else BASE_URL="$REPO/releases/latest/download"; fi
}

# 缺下载器或 sha256sum 时在任何网络操作之前退出：否则会先 apt-get update 装 CA，再报没有 curl。
PKG="probe-agent_linux_$ARCH.tar.gz"
if command -v curl >/dev/null 2>&1; then FETCH=curl
elif command -v wget >/dev/null 2>&1; then FETCH=wget
else echo "neither curl nor wget is available to download $PKG" >&2; exit 1; fi
command -v sha256sum >/dev/null 2>&1 || { echo "sha256sum is required to verify downloads" >&2; exit 1; }

ca_bundle_present() {
  for f in /etc/ssl/certs/ca-certificates.crt /etc/pki/tls/certs/ca-bundle.crt /etc/ssl/cert.pem /etc/pki/ca-trust/extracted/pem/tls-ca-bundle.pem; do
    [ -f "$f" ] && return 0
  done
  return 1
}
is_https() { case "$1" in https://*) return 0;; esac; return 1; }
# 下载地址与 hub 地址分别判断：默认下载地址就是 https；hub 用 https 时 register 与上报也要 CA。
if is_https "$BASE_URL" || is_https "$HUB"; then
  if ! ca_bundle_present; then
    if command -v apt-get >/dev/null 2>&1; then
      apt-get update </dev/null && DEBIAN_FRONTEND=noninteractive apt-get install -y ca-certificates </dev/null
    elif command -v apk >/dev/null 2>&1; then
      apk add --no-cache ca-certificates </dev/null
    elif command -v dnf >/dev/null 2>&1; then
      dnf install -y ca-certificates </dev/null
    elif command -v yum >/dev/null 2>&1; then
      yum install -y ca-certificates </dev/null
    else
      echo "no CA bundle found and no supported package manager to install ca-certificates" >&2; exit 1
    fi
    ca_bundle_present || { echo "ca-certificates installation did not produce a CA bundle" >&2; exit 1; }
  fi
fi

work=$(mktemp -d)
BIN_TMP="$BIN.tmp.$$"
# EXIT trap 覆盖正常结束、exit 与 set -e 触发的退出。dash 与 busybox ash 被信号终止时不执行 EXIT trap，
# 所以把 INT、TERM、HUP 转成 exit 1，Ctrl-C 或 SSH 断开时也会清掉工作目录与写了一半的临时二进制。
trap 'rm -rf "$work"; rm -f "$BIN_TMP"' EXIT
trap 'exit 1' INT TERM HUP

dl() {
  # 下载地址为 https 时，请求和重定向都只走 https。http（本地验收）不加：--proto '=https' 会拒绝它。
  # wget 没有对应开关。
  if [ "$FETCH" = curl ]; then
    if is_https "$1"; then curl --proto '=https' --proto-redir '=https' -fsSL -o "$2" "$1"
    else curl -fsSL -o "$2" "$1"; fi
  else wget -q -O "$2" "$1"; fi
}
dl "$BASE_URL/$PKG" "$work/$PKG"
dl "$BASE_URL/SHA256SUMS" "$work/SHA256SUMS"
# SHA256SUMS 含全部资产，只核对本包那一行；busybox 的 sha256sum 没有 --ignore-missing。
# 行格式是 64 位十六进制、空白、文件名；二进制模式带 * 前缀的行按第二字段取不到。
(cd "$work" && awk -v p="$PKG" '$2 == p' SHA256SUMS > verify.txt)
[ -s "$work/verify.txt" ] || {
  echo "SHA256SUMS has no entry for $PKG" >&2; exit 1
}
(cd "$work" && sha256sum -c verify.txt)

# 解包、检查包内文件、写临时二进制都在停服务之前做完：这些准备失败时，正在运行的旧服务不受影响。
tar -xzf "$work/$PKG" -C "$work"
for f in probe-agent probe-agent.service probe-agent.openrc; do
  [ -f "$work/$f" ] || { echo "package is missing $f" >&2; exit 1; }
done
install -m 0755 "$work/probe-agent" "$BIN_TMP"
stop_service
# 同目录 rename 原子替换目录项：exec $BIN 看到的始终是完整的旧文件或完整的新文件；
# 写了一半的临时文件由上面的 trap 删除。
mv -f "$BIN_TMP" "$BIN"

if [ ! -f "$CFG" ]; then
  # 注册只在没有配置时发生；配置落盘后重跑不再注册，所以注册之后的步骤失败时，重跑不会多耗窗口名额。
  set -- register --hub "$HUB" --key "$KEY" --config "$CFG"
  if [ -n "$NAME" ]; then set -- "$@" --name "$NAME"; fi
  "$BIN" "$@" </dev/null
else
  if [ -n "$KEY" ]; then echo "existing config found; keeping the current registration (--key ignored)"; fi
  if [ -n "$NAME" ]; then echo "existing config found; --name ignored"; fi
fi
# 每次安装都做，不只在注册之后。只做一次会留下服务用户读不到的配置：
# - 手工重新注册以 root 重写配置（SaveConfig 新建 0600，属主是调用者）
# - 注册之后、改属主之前被信号打断，重跑走已有配置分支
# - 账户被删后以新 uid 重建，配置仍属旧 uid
# 目录属 root、0750：服务用户不能增删目录项，这里的 chown 不会被链接劫持。
# 目录无需对服务用户可写：写配置只发生在以 root 执行的 register 里，cmd/agent 的 run 只调用 LoadConfig、不调用 SaveConfig。
chown root:"$SVC_USER" "$CFG_DIR"
chmod 0750 "$CFG_DIR"
chown "$SVC_USER:$SVC_USER" "$CFG"

# 服务定义每次覆盖，单元的改动随升级下发。
# 走到这里时没有以服务用户运行的进程：stop_service 不论服务定义在不在都已确认，查到就已退出。
# 所以用 start，不依赖 restart 对已停服务等价于 start。
case "$INIT" in
  systemd)
    install -m 0644 "$work/probe-agent.service" /etc/systemd/system/probe-agent.service
    systemctl daemon-reload </dev/null
    systemctl enable probe-agent </dev/null
    systemctl start probe-agent </dev/null;;
  openrc)
    install -m 0755 "$work/probe-agent.openrc" /etc/init.d/probe-agent
    # 重跑时它已在 default runlevel 里；只在不在时才加，不依赖 rc-update 对重复 add 的退出码。
    [ -L /etc/runlevels/default/probe-agent ] || rc-update add probe-agent default </dev/null
    rc-service probe-agent start </dev/null;;
esac
confirm_service_started
echo "probe-agent installed and started ($INIT, $ARCH, $PKG)"
