#!/bin/sh
# probe-agent 的 macOS 安装脚本：下载、校验、注册并装成 LaunchDaemon；重跑即升级。
# 参数、步骤顺序与提示与 Linux 的 install.sh 同形，差异只在平台工具：dscl 建账户、shasum 校验、
# launchctl 管服务、ps 扫进程（macOS 没有 /proc）。
# 以 curl … | sh -s -- 运行时，脚本本身来自 stdin。脚本里任何读 stdin 的命令都会吞掉
# 脚本余下部分，安装在中途无声结束。所以每个可能读 stdin 的外部命令都显式 </dev/null。
# 不能用 exec </dev/null：那会切断脚本自己的来源。
set -eu

# 落盘路径都挂在 PROBE_INSTALL_ROOT 下。生产运行时它为空，即真实根目录；脚本的逻辑测试以普通用户
# 把它指向临时目录，连同 PATH 上的 dscl、launchctl、ps 等替身一起运行，不触碰真实系统路径。
# 它只改变本脚本写文件的位置：plist 里的 ProgramArguments 与日志路径是固定的真实路径。
ROOT=${PROBE_INSTALL_ROOT-}
# 服务进程的可执行路径，即 plist 的 ProgramArguments[0]；按它认进程，所以不带 ROOT 前缀。
SVC_BIN=/usr/local/bin/probe-agent
BIN=$ROOT$SVC_BIN
CFG_DIR=$ROOT/etc/probe-agent
CFG=$CFG_DIR/config.json
LOG_DIR=$ROOT/Library/Logs/probe-agent
LABEL=xyz.probe.agent
PLIST=$ROOT/Library/LaunchDaemons/$LABEL.plist
SVC_USER=_probe-agent
REPO=https://github.com/xjetry/probe

usage() {
  echo "usage: install-macos.sh --hub URL --key KEY [--name N] [--version vX.Y.Z] [--base-url URL]" >&2
  echo "       install-macos.sh --uninstall [--purge]" >&2
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

[ "$(id -u)" = 0 ] || { echo "install-macos.sh must run as root" >&2; exit 1; }

# 停服务后确认进程退出的轮询间隔（秒）与次数上限：sleep 合计最多 10 秒，外加每轮一次 ps。
STOP_POLL_INTERVAL=1
STOP_POLL_MAX=10
# 启动后等到进程出现的上限，同一口径。出现之后再固定等 3 秒，确认还是同一个 pid。
START_POLL_INTERVAL=1
START_POLL_MAX=10

# 列出本服务的进程 pid（空格分隔，写入 svc_pids）：有效 uid 为 $1 且可执行路径为 $SVC_BIN。
# BSD ps 的 uid 列是有效 uid（ps(1)）；comm 列是进程的 argv[0]（实测：不截断，文件被替换后不变），
# launchd 作业的 argv[0] 是 plist 的 ProgramArguments[0]（用户域作业实测）。comm 里可能有空格，取前两列之后的整段比较。
scan_svc_pids() {
  procs=$(ps -axo uid=,pid=,comm=) || { echo "cannot list processes" >&2; return 1; }
  svc_pids=$(printf '%s\n' "$procs" | awk -v u="$1" -v p="$SVC_BIN" '
    { c = $0; sub(/^[ \t]*[0-9]+[ \t]+[0-9]+[ \t]+/, "", c) }
    $1 == u && c == p { printf " %s", $2 }')
}

# 确认没有本服务的进程。它不看 launchd 报告的状态，也不依赖 plist 是否还在：
# plist 被手工删掉而进程仍在时也只有这一步抓得到。
# 判据"有效 uid 为服务用户且可执行路径为 $SVC_BIN"与"是本服务的进程"等价，两个方向各有担保：
# - 选中的都是本服务的进程：$SVC_USER 是专供 agent 的账户（登录 shell /usr/bin/false），由 create_account 建立；
#   launchd 会以该 uid 按需派生 cfprefsd、trustd 之类的辅助进程，只看 uid 会把它们算进来（升级、卸载误报仍在运行，
#   秒退的 agent 被辅助进程顶替成"已启动"），可执行路径把它们排除。
# - 本服务的每个进程都被选中：plist 的 UserName 与 ProgramArguments[0]（deploy/launchd/xyz.probe.agent.plist）
#   保证，TestPlistAgreesWithScript 把两处钉在一起。plist 若改以别的身份或别的程序运行任何进程，这里会漏查，必须同步改判据。
# 用户不存在时没有可比对的 uid，直接通过；此时查不到仍在运行的旧进程，与 Linux 脚本相同。
confirm_service_stopped() {
  id "$SVC_USER" >/dev/null 2>&1 || return 0
  svc_uid=$(id -u "$SVC_USER")
  polls=0
  while :; do
    scan_svc_pids "$svc_uid" || return 1
    [ -n "$svc_pids" ] || return 0
    [ "$polls" -lt "$STOP_POLL_MAX" ] || break
    sleep "$STOP_POLL_INTERVAL"
    polls=$((polls + 1))
  done
  echo "probe-agent is still running: processes with uid $svc_uid ($SVC_USER):$svc_pids" >&2
  return 1
}

# 启动之后确认服务进程活着：bootstrap 返回 0 只说明作业已载入，证明不了子进程没有秒退。
# 先等到出现本服务的进程，记下 pid，3 秒后同一个 pid 仍在；
# 秒退再被 KeepAlive 拉起会换成新 pid，不能算起来了。判据与 confirm_service_stopped 同一处。
start_log_hint() { echo "see $LOG_DIR/probe-agent.err" >&2; }
confirm_service_started() {
  svc_uid=$(id -u "$SVC_USER") || { echo "no service user $SVC_USER" >&2; start_log_hint; return 1; }
  polls=0
  pid=""
  while :; do
    scan_svc_pids "$svc_uid" || return 1
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
  scan_svc_pids "$svc_uid" || return 1
  case " $svc_pids " in
    *" $pid "*) return 0;;
  esac
  echo "probe-agent did not stay running (pid $pid)" >&2
  start_log_hint
  return 1
}

# 是否发 bootout 看作业是否已载入 system 域，而不是 plist 文件在不在：launchd 按已载入的作业管进程，
# 文件在而作业未载入时没有可停的东西（bootout 会以 3 失败），作业已载入而文件被删时进程照样在跑。
# bootout 失败即失败；确认一步不依赖它，总是执行。
service_loaded() { launchctl print "system/$LABEL" >/dev/null 2>&1 </dev/null; }
stop_service() {
  if service_loaded; then
    launchctl bootout "system/$LABEL" </dev/null || { echo "failed to stop probe-agent" >&2; return 1; }
  fi
  confirm_service_stopped
}

group_exists() { dscl . -read "/Groups/$SVC_USER" PrimaryGroupID >/dev/null 2>&1 </dev/null; }
user_record_exists() { dscl . -read "/Users/$SVC_USER" UniqueID >/dev/null 2>&1 </dev/null; }

# 用户与同名组都删并回查：不信 dscl 的退出码，也不能半成功还报成功。
# 要不要发 dscl . -delete 看本地节点的记录，与这条命令操作的对象同一口径：id 的解析结果可能来自别的目录节点
# 或陈旧的缓存，那时 -delete 以 eDSRecordNotFound 失败，脚本以 dscl 的退出码结束、说不出原因。
# 回查仍用 id：chown 与 launchd 按名字取身份走的就是这条解析路径。
delete_account() {
  if user_record_exists; then dscl . -delete "/Users/$SVC_USER" </dev/null; fi
  if group_exists; then dscl . -delete "/Groups/$SVC_USER" </dev/null; fi
  if id "$SVC_USER" >/dev/null 2>&1 || group_exists; then
    echo "failed to delete user or group $SVC_USER" >&2; exit 1
  fi
}

if [ "$UNINSTALL" = 1 ]; then
  stop_service
  # 作业已由 stop_service 卸下；plist 删掉后开机也不再载入。
  rm -f "$PLIST" "$BIN"
  if [ "$PURGE" = 1 ]; then
    rm -rf "$CFG_DIR" "$LOG_DIR"
    delete_account
  fi
  echo "probe-agent uninstalled"
  exit 0
fi

# 卸载不需要架构：不支持的架构不应让人连卸载都做不了。
# 在 Rosetta 转译的终端里 uname -m 报 x86_64，而 hw.optional.arm64 仍为 1：先看它，只看 uname 会在 Apple Silicon 上装 amd64 包。
# 它不存在或不为 1 时按 uname -m 判定；-i 让未知 OID 输出为空而不报错。
if [ "$(sysctl -in hw.optional.arm64 </dev/null)" = 1 ]; then ARCH=arm64
else
  case "$(uname -m)" in
    x86_64) ARCH=amd64;; arm64) ARCH=arm64;;
    *) echo "unsupported architecture: $(uname -m)" >&2; exit 1;;
  esac
fi

# 首次安装必须有注册凭据；升级沿用现有配置，不需要也不接受重新注册。
if [ ! -f "$CFG" ] && { [ -z "$HUB" ] || [ -z "$KEY" ]; }; then
  echo "--hub and --key are required for the first install" >&2
  exit 2
fi

# 300–499 中同时未被用作 UniqueID 与 PrimaryGroupID 的最大值：低于 500 的账户不出现在登录窗口，
# 两个命名空间都空闲的号可以让新建的组与用户同号。从上端往下取：Apple 的系统账户从 300 往上逐版追加
# （macOS 26.3.1 已连号占到 308），升级会替换占着这些号的第三方账户（macOS 15 替换过占 301–304 的 _nixbld1–4）；
# 从 499 取离这条前沿最远。低于 500 的号段没有留给第三方的保证，这只是把风险推到最远。
free_id() {
  uids=$(dscl . -list /Users UniqueID </dev/null) || { echo "cannot list user ids" >&2; return 1; }
  gids=$(dscl . -list /Groups PrimaryGroupID </dev/null) || { echo "cannot list group ids" >&2; return 1; }
  printf '%s\n%s\n' "$uids" "$gids" | awk '{ used[$2] = 1 } END { for (i = 499; i >= 300; i--) if (!(i in used)) { print i; exit 0 } exit 1 }' ||
    { echo "no free id in 300-499 for $SVC_USER" >&2; return 1; }
}

# 建用户排在下载与注册之前：它若失败，注册窗口的名额尚未消耗、旧服务尚未停止。
# 主组必须是同名组：plist 的 GroupName 与配置目录的组都写 $SVC_USER。先建组、再以它为主组建用户，建完回查，
# 不信 dscl 的退出码。
create_account() {
  # 用户已在时先核对主组，再决定要不要建组。顺序反了会在主组不符退出时留下本次新建的同名组。
  if id "$SVC_USER" >/dev/null 2>&1; then
    actual_group=$(id -gn "$SVC_USER" 2>/dev/null) || {
      echo "user $SVC_USER exists but its primary group is missing (gid $(id -g "$SVC_USER"))" >&2; exit 1; }
    [ "$actual_group" = "$SVC_USER" ] || {
      echo "user $SVC_USER exists with primary group $actual_group; expected $SVC_USER" >&2; exit 1; }
    return 0
  fi
  if group_exists; then
    rec=$(dscl . -read "/Groups/$SVC_USER" PrimaryGroupID </dev/null) || { echo "cannot read group $SVC_USER" >&2; exit 1; }
    gid=$(printf '%s\n' "$rec" | awk '$1 == "PrimaryGroupID:" { print $2 }')
    [ -n "$gid" ] || { echo "group $SVC_USER has no PrimaryGroupID" >&2; exit 1; }
    uid=$(free_id) || exit 1
  else
    gid=$(free_id) || exit 1
    uid=$gid
    dscl . -create "/Groups/$SVC_USER" PrimaryGroupID "$gid" </dev/null
    dscl . -create "/Groups/$SVC_USER" RealName "probe agent" </dev/null
    dscl . -create "/Groups/$SVC_USER" Password '*' </dev/null
  fi
  dscl . -create "/Users/$SVC_USER" UniqueID "$uid" </dev/null
  dscl . -create "/Users/$SVC_USER" PrimaryGroupID "$gid" </dev/null
  dscl . -create "/Users/$SVC_USER" UserShell /usr/bin/false </dev/null
  dscl . -create "/Users/$SVC_USER" NFSHomeDirectory /var/empty </dev/null
  dscl . -create "/Users/$SVC_USER" RealName "probe agent" </dev/null
  dscl . -create "/Users/$SVC_USER" Password '*' </dev/null
  dscl . -create "/Users/$SVC_USER" IsHidden 1 </dev/null
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

# 缺下载器或 shasum 时在任何网络操作之前退出。macOS 自带二者与系统 CA，不需要装 CA 的分支。
PKG="probe-agent_darwin_$ARCH.tar.gz"
command -v curl >/dev/null 2>&1 || { echo "curl is required to download $PKG" >&2; exit 1; }
command -v shasum >/dev/null 2>&1 || { echo "shasum is required to verify downloads" >&2; exit 1; }

work=$(mktemp -d)
BIN_TMP="$BIN.tmp.$$"
# EXIT trap 覆盖正常结束、exit 与 set -e 触发的退出；INT、TERM、HUP 转成 exit 1，
# Ctrl-C 或 SSH 断开时也会清掉工作目录与写了一半的临时二进制。
trap 'rm -rf "$work"; rm -f "$BIN_TMP"' EXIT
trap 'exit 1' INT TERM HUP

is_https() { case "$1" in https://*) return 0;; esac; return 1; }
dl() {
  # 下载地址为 https 时，请求和重定向都只走 https。其他协议（本地验收）不加：--proto '=https' 会拒绝它。
  if is_https "$1"; then curl --proto '=https' --proto-redir '=https' -fsSL -o "$2" "$1"
  else curl -fsSL -o "$2" "$1"; fi
}
dl "$BASE_URL/$PKG" "$work/$PKG"
dl "$BASE_URL/SHA256SUMS" "$work/SHA256SUMS"
# SHA256SUMS 含全部资产，只核对本包那一行；行格式是 64 位十六进制、空白、文件名。
(cd "$work" && awk -v p="$PKG" '$2 == p' SHA256SUMS > verify.txt)
[ -s "$work/verify.txt" ] || {
  echo "SHA256SUMS has no entry for $PKG" >&2; exit 1
}
(cd "$work" && shasum -a 256 -c verify.txt)

# 解包、检查包内文件、写临时二进制都在停服务之前做完：这些准备失败时，正在运行的旧服务不受影响。
tar -xzf "$work/$PKG" -C "$work"
for f in probe-agent "$LABEL.plist"; do
  [ -f "$work/$f" ] || { echo "package is missing $f" >&2; exit 1; }
done
mkdir -p "$(dirname "$BIN")"
install -m 0755 "$work/probe-agent" "$BIN_TMP"
stop_service
# 同目录 rename 原子替换目录项：launchd 执行 $BIN 时看到的始终是完整的旧文件或完整的新文件。
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
# 每次安装都做，不只在注册之后（手工重新注册、注册后被打断、账户重建后 uid 变了、人工编辑改了权限，
# 都会留下服务用户读不到或别人读得到的配置）。
# 目录属 root、0750：服务用户不能增删目录项，这里的 chown 不会被链接劫持。
# 目录无需对服务用户可写：写配置只发生在以 root 执行的 register 里，run 只读配置。
chown "root:$SVC_USER" "$CFG_DIR"
chmod 0750 "$CFG_DIR"
chown "$SVC_USER:$SVC_USER" "$CFG"
chmod 0600 "$CFG"

# 日志目录属 root、0755，两个日志文件事先建好并交给服务用户，与 Linux 的 OpenRC 服务同一做法（spec §14）。
# 用户域作业实测：StandardErrorPath 所在目录对作业用户不可写时，launchd 建不出日志文件，作业以 78（EX_CONFIG）
# 退出——文件以作业的身份打开。system 域加 UserName 时是否同样如此本机无法实测（要 root），由 README 真机
# 核对清单第 12 条验证；事先建好属于服务用户的文件，两种情形下服务都能打开日志。
# 服务用户不能增删 root 目录里的条目，文件换不成链接；已是链接（只有 root 放得进来）时拒绝，不跟着改别的文件。
mkdir -p "$LOG_DIR"
chown root:wheel "$LOG_DIR"
chmod 0755 "$LOG_DIR"
for f in "$LOG_DIR/probe-agent.log" "$LOG_DIR/probe-agent.err"; do
  [ ! -L "$f" ] || { echo "$f is a symbolic link; refusing to hand it to $SVC_USER" >&2; exit 1; }
  [ -e "$f" ] || : > "$f"
  chown "$SVC_USER:$SVC_USER" "$f"
  chmod 0640 "$f"
done

# plist 每次覆盖，改动随升级下发。它决定以什么身份运行什么程序，只能由 root 改：root:wheel、0644。
# enable 清掉可能残留的禁用覆盖（launchctl disable 跨重启有效），与 systemctl enable 同位。
# KeepAlive 隐含 RunAtLoad（launchd.plist(5)），bootstrap 即启动。
# 走到这里时没有以服务用户运行的进程：stop_service 已确认。
mkdir -p "$(dirname "$PLIST")"
install -m 0644 "$work/$LABEL.plist" "$PLIST"
chown root:wheel "$PLIST"
launchctl enable "system/$LABEL" </dev/null
launchctl bootstrap system "$PLIST" </dev/null
confirm_service_started
echo "probe-agent installed and started (launchd, $ARCH, $PKG)"
