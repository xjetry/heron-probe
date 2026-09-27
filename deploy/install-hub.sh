#!/bin/sh
# hub 安装与升级共用此入口；服务参数保存在 systemd 单元，不另建一份配置。
# 管道安装时 stdin 是脚本源码，外部命令不能读取它；确认只从 /dev/tty 读取。
set -eu

ROOT=${PROBE_INSTALL_ROOT-}
BIN=$ROOT/usr/local/bin/probe-hub
DATA=$ROOT/var/lib/probe
UNIT=$ROOT/etc/systemd/system/probe-hub.service
WANTS=$ROOT/etc/systemd/system/multi-user.target.wants/probe-hub.service
PROC=$ROOT/proc
SVC_USER=probe-hub
REPO=https://github.com/xjetry/probe
VERSION=""; BASE_URL=""; UNINSTALL=0; PURGE=0; YES=0; OVERRIDES=""
usage() {
  echo 'usage: install-hub.sh [--version VERSION] [--base-url URL] [--listen ADDR] [--timezone ZONE] [--trusted-proxies CIDRS] [--public-dir DIR] [--retention-1m DURATION] [--retention-5m DURATION] [--retention-1h DURATION] [--retention-alert-events DURATION] [--yes]' >&2
  echo '       install-hub.sh --uninstall [--purge] [--yes]' >&2
  exit 2
}
fail() { echo "$*" >&2; exit 1; }
# 每个参数单独引用；systemd 会展开 $ 与 %，两者必须双写才能保持命令行给出的字面值。
quote_arg() {
  case "$1" in *'
'*|*''*) fail 'arguments must not contain line breaks';; esac
  quoted=$(printf '%s' "$1" | sed 's/\\/\\\\/g; s/"/\\"/g; s/\$/$$/g; s/%/%%/g')
  quoted="\"$quoted\""
}
while [ "$#" -gt 0 ]; do
  case "$1" in
    --version|--base-url)
      [ "$#" -ge 2 ] || usage
      case "$1" in --version) VERSION=$2;; --base-url) BASE_URL=$2;; esac
      shift 2;;
    --listen|--timezone|--trusted-proxies|--public-dir|--retention-1m|--retention-5m|--retention-1h|--retention-alert-events)
      [ "$#" -ge 2 ] || usage
      quote_arg "$1=$2"
      OVERRIDES="$OVERRIDES $quoted"
      shift 2;;
    --uninstall) UNINSTALL=1; shift;;
    --purge) PURGE=1; shift;;
    --yes) YES=1; shift;;
    *) usage;;
  esac
done
[ "$PURGE" = 0 ] || [ "$UNINSTALL" = 1 ] || usage
[ "$(id -u)" = 0 ] || fail 'install-hub.sh must run as root'
[ -d "$ROOT/run/systemd/system" ] || fail 'unsupported init: expected systemd (/run/systemd/system)'

confirm_removal() {
  [ "$YES" = 0 ] || return 0
  if [ ! -t 1 ] || [ ! -r /dev/tty ]; then
    fail 'uninstall requires --yes without a terminal'
  fi
  printf 'Stop and remove probe-hub (purge data: %s)? [y/N] ' "$PURGE" > /dev/tty
  IFS= read -r answer < /dev/tty || fail 'confirmation canceled'
  case "$answer" in y|Y|yes|YES) ;; *) fail 'confirmation canceled';; esac
}
local_user_exists() { grep -q "^$SVC_USER:" "$ROOT/etc/passwd"; }
local_group_exists() { grep -q "^$SVC_USER:" "$ROOT/etc/group"; }
create_account() {
  nologin=/sbin/nologin
  [ ! -x "$ROOT/usr/sbin/nologin" ] || nologin=/usr/sbin/nologin
  if id "$SVC_USER" >/dev/null 2>&1; then
    actual_group=$(id -gn "$SVC_USER") || fail "primary group missing for $SVC_USER"
    [ "$actual_group" = "$SVC_USER" ] || fail "user $SVC_USER has primary group $actual_group; expected $SVC_USER"
    [ "$(id -u "$SVC_USER")" != 0 ] || fail 'probe-hub must not have uid 0'
    return
  fi
  if ! local_group_exists; then
    if command -v groupadd >/dev/null 2>&1; then groupadd --system "$SVC_USER" </dev/null
    elif command -v addgroup >/dev/null 2>&1; then
      if addgroup --help </dev/null 2>&1 | grep -qi busybox; then addgroup -S "$SVC_USER" </dev/null
      else addgroup --system "$SVC_USER" </dev/null; fi
    else fail 'no groupadd or addgroup available to create the system group'; fi
  fi
  if command -v useradd >/dev/null 2>&1; then
    useradd --system -M -d /nonexistent -s "$nologin" -g "$SVC_USER" "$SVC_USER" </dev/null
  elif command -v adduser >/dev/null 2>&1; then
    if adduser --help </dev/null 2>&1 | grep -qi busybox; then
      adduser -S -D -H -s "$nologin" -G "$SVC_USER" "$SVC_USER" </dev/null
    else adduser --system --no-create-home --shell "$nologin" --ingroup "$SVC_USER" "$SVC_USER" </dev/null; fi
  else fail 'no useradd or adduser available to create the system user'; fi
  id "$SVC_USER" >/dev/null 2>&1 || fail "failed to create system user $SVC_USER"
  [ "$(id -gn "$SVC_USER")" = "$SVC_USER" ] || fail "primary group must be $SVC_USER"
}
delete_account() {
  if local_user_exists; then
    if command -v userdel >/dev/null 2>&1; then userdel "$SVC_USER" </dev/null
    elif command -v deluser >/dev/null 2>&1; then deluser "$SVC_USER" </dev/null; fi
  fi
  if local_group_exists; then
    if command -v groupdel >/dev/null 2>&1; then groupdel "$SVC_USER" </dev/null
    elif command -v delgroup >/dev/null 2>&1; then delgroup "$SVC_USER" </dev/null; fi
  fi
  if id "$SVC_USER" >/dev/null 2>&1 || local_group_exists; then fail "failed to delete user or group $SVC_USER"; fi
}
# 专用账户由 create_account 建立，单元的 User= 保证服务进程使用它；两者改变时须同步修改扫描判据。
scan_uid_pids() {
  svc_pids=""
  [ -r "$PROC/self/status" ] || fail 'cannot inspect processes: /proc is not mounted'
  for s in "$PROC"/[0-9]*/status; do
    if euid=$(awk '$1 == "Uid:" { print $3; exit }' "$s" 2>/dev/null); then
      if [ "$euid" = "$1" ]; then p=${s#"$PROC"/}; svc_pids="$svc_pids ${p%/status}"; fi
    elif [ -e "$s" ]; then fail "cannot read $s"; fi
  done
}
stop_service() {
  if [ -f "$UNIT" ]; then systemctl stop probe-hub </dev/null || fail 'failed to stop probe-hub'; fi
  id "$SVC_USER" >/dev/null 2>&1 || return 0
  svc_uid=$(id -u "$SVC_USER")
  polls=0
  while :; do
    scan_uid_pids "$svc_uid"
    [ -n "$svc_pids" ] || return 0
    [ "$polls" -lt 10 ] || fail "probe-hub is still running: uid $svc_uid processes:$svc_pids"
    sleep 1
    polls=$((polls + 1))
  done
}
confirm_service_started() {
  svc_uid=$(id -u "$SVC_USER") || fail 'no service user probe-hub; see journalctl -u probe-hub'
  polls=0; pid=""
  while :; do
    scan_uid_pids "$svc_uid"
    if [ -n "$svc_pids" ]; then pid=${svc_pids# }; pid=${pid%% *}; break; fi
    [ "$polls" -lt 10 ] || fail 'probe-hub did not start; see journalctl -u probe-hub'
    sleep 1
    polls=$((polls + 1))
  done
  sleep 3
  scan_uid_pids "$svc_uid"
  case " $svc_pids " in *" $pid "*) return 0;; esac
  fail "probe-hub did not stay running (pid $pid); see journalctl -u probe-hub"
}
# 数据目录与三个库文件的形态核对，停服前的预检与停服后的锁内复检共用。
# 目录在 root 属主的 /var/lib 下，服务用户换不掉这个目录项，所以对目录本身的核对不依赖锁；下文的 chown
# 不带 -h，目录是链接就会改到链接指向的目录。库文件要么不存在，要么是链接数为 1 的普通文件：符号链接与
# 硬链接都会让 root 的 chown、chmod 改到另一个名字所指的文件。硬链接 [ -L ] 为假、[ -f ] 为真，只有链接数
# 看得出；链接数用 find -links 取（BSD 与 GNU 的 find 都支持，替身测试也在 macOS 上跑），find 失败时输出
# 同样为空，所以先看它的退出码。
data_dir_ok() {
  if [ -L "$DATA" ] || { [ -e "$DATA" ] && [ ! -d "$DATA" ]; }; then
    echo "$DATA exists but is not a directory" >&2; return 1
  fi
}
db_files_ok() {
  for file in "$DATA/probe.db" "$DATA/probe.db-wal" "$DATA/probe.db-shm"; do
    [ -L "$file" ] || [ -e "$file" ] || continue
    extra=""
    if [ ! -L "$file" ] && [ -f "$file" ]; then
      extra=$(find "$file" -prune -links +1) || { echo "cannot read the link count of $file" >&2; return 1; }
    fi
    if [ -L "$file" ] || [ ! -f "$file" ] || [ -n "$extra" ]; then
      echo "$file exists but is not a regular file with a single link" >&2; return 1
    fi
  done
}
if [ "$UNINSTALL" = 1 ]; then
  confirm_removal
  stop_service
  if [ -f "$UNIT" ]; then systemctl disable probe-hub </dev/null; fi
  if [ -L "$WANTS" ]; then rm -f "$WANTS"; fi
  rm -f "$UNIT" "$BIN"
  systemctl daemon-reload </dev/null
  if [ "$PURGE" = 1 ]; then rm -rf "$DATA"; delete_account; fi
  echo 'probe-hub uninstalled'
  exit 0
fi
case "$(uname -m)" in
  x86_64) ARCH=amd64;; aarch64) ARCH=arm64;;
  *) fail "unsupported architecture: $(uname -m)";;
esac
create_account
PKG=probe-hub_linux_$ARCH.tar.gz
if command -v curl >/dev/null 2>&1; then FETCH=curl
elif command -v wget >/dev/null 2>&1; then FETCH=wget
else fail "neither curl nor wget is available to download $PKG"; fi
command -v sha256sum >/dev/null 2>&1 || fail 'sha256sum is required to verify downloads'
if [ -n "$BASE_URL" ] && [ -n "$VERSION" ]; then echo '--base-url is the download directory (--version ignored)'; fi
if [ -z "$BASE_URL" ]; then
  if [ -n "$VERSION" ]; then BASE_URL=$REPO/releases/download/$VERSION
  else BASE_URL=$REPO/releases/latest/download; fi
fi
is_https() { case "$1" in https://*) return 0;; esac; return 1; }
ca_bundle_present() {
  for f in /etc/ssl/certs/ca-certificates.crt /etc/pki/tls/certs/ca-bundle.crt /etc/ssl/cert.pem /etc/pki/ca-trust/extracted/pem/tls-ca-bundle.pem; do
    [ ! -f "$ROOT$f" ] || return 0
  done
  return 1
}
# 不论下载地址是不是 https，hub 进程自己都要做 TLS 出站：serve 以空基址构造告警队列（cmd/hub/serve.go），
# Telegram 于是固定走 https://api.telegram.org（internal/hub/alert/notify.go），webhook 也可以是 https。
# Go 的 crypto/x509 在 Linux 上只从系统的证书文件与目录（或 SSL_CERT_FILE、SSL_CERT_DIR 指定的位置）加载根证书，
# 本仓库没有引入内置根证书。缺证书包时安装照样成功、面板正常，告警投递却一直因证书校验失败被记成 transport 失败。
# 探测按文件做，不按发行版判断。
if ! ca_bundle_present; then
  if command -v apt-get >/dev/null 2>&1; then
    apt-get update </dev/null && DEBIAN_FRONTEND=noninteractive apt-get install -y ca-certificates </dev/null
  elif command -v apk >/dev/null 2>&1; then apk add --no-cache ca-certificates </dev/null
  elif command -v dnf >/dev/null 2>&1; then dnf install -y ca-certificates </dev/null
  elif command -v yum >/dev/null 2>&1; then yum install -y ca-certificates </dev/null
  else fail 'no CA bundle found and no supported package manager to install ca-certificates'; fi
  ca_bundle_present || fail 'ca-certificates installation did not produce a CA bundle'
fi

work=$(mktemp -d)
BIN_TMP=$BIN.tmp.$$
# 停服务之后、start 返回之前的任何失败都让 hub 停着，各步的报错只说自己的原因：退出时补一句服务已停，
# 免得人以为旧服务还在跑。
SERVICE_STOPPED=0
on_exit() {
  rc=$?
  rm -rf "$work"; rm -f "$BIN_TMP"
  if [ "$rc" != 0 ] && [ "$SERVICE_STOPPED" = 1 ]; then
    echo 'probe-hub is stopped; rerun the installer or start it manually' >&2
  fi
}
trap on_exit EXIT
trap 'exit 1' INT TERM HUP
dl() {
  if [ "$FETCH" = curl ]; then
    if is_https "$1"; then curl --proto '=https' --proto-redir '=https' -fsSL -o "$2" "$1" </dev/null
    else curl -fsSL -o "$2" "$1" </dev/null; fi
  else wget -q -O "$2" "$1" </dev/null; fi
}
dl "$BASE_URL/$PKG" "$work/$PKG"
dl "$BASE_URL/SHA256SUMS" "$work/SHA256SUMS"
(cd "$work" && awk -v p="$PKG" '$2 == p' SHA256SUMS > verify.txt)
[ -s "$work/verify.txt" ] || fail "SHA256SUMS has no entry for $PKG"
(cd "$work" && sha256sum -c verify.txt)
tar -xzf "$work/$PKG" -C "$work"
for f in probe-hub probe-hub.service; do
  [ -f "$work/$f" ] && [ ! -L "$work/$f" ] || fail "package is missing regular file $f"
done
install -m 0755 "$work/probe-hub" "$BIN_TMP"

# 不执行单元内容，也不按空格粗拆：带引号的目录必须作为一个参数保留。
# 未支持的 systemd 动态展开与转义在停服前拒绝，而不是静默变成另一组参数。
source_unit=$work/probe-hub.service
if [ -f "$UNIT" ]; then
  systemctl cat probe-hub </dev/null > "$work/loaded-unit"
  # 保留下来的 drop-in 若改 ExecStart，会盖掉写进主单元的显式覆盖参数；不涉及命令的全局加固项不受影响。
  awk '
    /^# \// { source = substr($0, 3) }
    /^[[:space:]]*ExecStart[[:space:]]*=/ && source != "/etc/systemd/system/probe-hub.service" { exit 1 }
  ' "$work/loaded-unit" || fail 'merge ExecStart drop-ins into probe-hub.service before upgrading'
  source_unit=$UNIT
fi
awk '
  /^[[:space:]]*[#;]/ { next }
  {
    line = pending $0; pending = ""
    if (sub(/\\$/, " ", line)) { pending = line; next }
    sub(/^[[:space:]]+/, "", line)
    if (line ~ /^\[/) { service = (line == "[Service]") }
    if (service && line ~ /^ExecStart=/) { command = substr(line, 11) }
  }
  END { if (pending != "" || command == "") exit 1; print command }
' "$source_unit" > "$work/command" || fail 'cannot read ExecStart from installed unit'
printf '%s\n' "$(cat "$work/command")$OVERRIDES" > "$work/effective"
awk '
  function bad() { failed = 1; exit 1 }
  {
    token = ""; quote = ""; active = 0
    for (i = 1; i <= length($0); i++) {
      c = substr($0, i, 1)
      if (c == "\\") {
        c = substr($0, ++i, 1)
        if (c == "s") c = " "
        else if (c != "\\" && c != "\"" && c != "\047") bad()
        token = token c; active = 1
      } else if (quote != "") {
        if (c == quote) quote = ""; else token = token c
      } else if (c == "\"" || c == "\047") { quote = c; active = 1 }
      else if (c ~ /[ \t]/) {
        if (active) { print token; token = ""; active = 0 }
      } else { token = token c; active = 1 }
    }
    if (quote != "") bad()
    if (active) print token
  }
  END { if (failed) exit 1 }
' "$work/effective" > "$work/raw-args" || fail 'unsupported quoting or escape in ExecStart'
# 双写的 $、% 是字面字符；单写需要 systemd 的运行时上下文，安装器不能据此判定监听端口。
awk '{
  out = ""
  for (i = 1; i <= length($0); i++) {
    c = substr($0, i, 1)
    if (c == "$" || c == "%") { if (substr($0, ++i, 1) != c) exit 1 }
    out = out c
  }
  print out
}' "$work/raw-args" > "$work/args" || fail 'dynamic $ or % expansion in ExecStart is not supported'
EXEC=""; position=0; pending=""; listen=127.0.0.1:8080; db=""
while IFS= read -r arg; do
  position=$((position + 1))
  case "$position:$arg" in
    1:/usr/local/bin/probe-hub|2:serve) ;;
    1:*|2:*) fail 'ExecStart must invoke /usr/local/bin/probe-hub serve';;
  esac
  if [ -n "$pending" ]; then
    case "$pending" in listen) listen=$arg;; db) db=$arg;; esac
    pending=""
  else
    if [ "$position" -gt 2 ]; then
      flag=${arg#-}; flag=${flag#-}; flag=${flag%%=*}
      case "$arg" in -*) ;; *) fail "unexpected positional argument in ExecStart: $arg";; esac
      case "$flag" in
        listen|db|timezone|trusted-proxies|public-dir|retention-1m|retention-5m|retention-1h|retention-alert-events) ;;
        *) fail "unsupported serve flag in ExecStart: $arg";;
      esac
      case "$arg" in
        *=*) case "$flag" in listen) listen=${arg#*=};; db) db=${arg#*=};; esac;;
        *) pending=$flag;;
      esac
    fi
  fi
  quote_arg "$arg"
  EXEC="$EXEC $quoted"
done < "$work/args"
[ "$position" -ge 2 ] && [ -z "$pending" ] || fail 'incomplete ExecStart arguments'
[ "$db" = /var/lib/probe/probe.db ] || fail 'installed unit must use --db /var/lib/probe/probe.db'
port=${listen##*:}
case "$port" in ''|*[!0-9]*) fail "listen address must end in a numeric TCP port: $listen";; esac
[ "$port" -ge 1 ] && [ "$port" -le 65535 ] || fail "invalid TCP port: $port"
PROBE_HUB_EXEC=${EXEC# }
export PROBE_HUB_EXEC
awk '/^ExecStart=/ { print "ExecStart=" ENVIRON["PROBE_HUB_EXEC"]; next } { print }' "$work/probe-hub.service" > "$work/unit"

check_port() {
  # /proc 提供监听 inode，再经 fd 定位持有者；不能只按进程名排除，另一个同名进程也会占端口。
  own=0
  if [ -f "$UNIT" ]; then
    own=$(systemctl show probe-hub -p MainPID --value </dev/null)
    case "$own" in ''|*[!0-9]*) fail 'cannot determine existing hub MainPID';; esac
    if [ "$own" != 0 ]; then
      exe=$(readlink "$PROC/$own/exe") || fail 'cannot inspect existing hub executable'
      [ "$exe" = /usr/local/bin/probe-hub ] || fail "unexpected executable for existing hub: $exe"
    fi
  fi
  hex=$(printf '%04X' "$port")
  [ -r "$PROC/net/tcp" ] || fail 'cannot inspect TCP listeners'
  set -- "$PROC/net/tcp"
  [ ! -f "$PROC/net/tcp6" ] || set -- "$@" "$PROC/net/tcp6"
  inodes=$(awk -v p="$hex" '$4 == "0A" { split($2,a,":"); if (toupper(a[2]) == p) print $10 }' "$@")
  for inode in $inodes; do
    found=0
    for fd in "$PROC"/[0-9]*/fd/*; do
      target=$(readlink "$fd" 2>/dev/null) || continue
      [ "$target" = "socket:[$inode]" ] || continue
      holder=${fd#"$PROC"/}; holder=${holder%%/*}
      [ "$holder" = "$own" ] || fail "port $port is already in use by pid $holder; old service was not stopped"
      found=1
    done
    [ "$found" = 1 ] || fail "port $port is in use; cannot identify listener pid; old service was not stopped"
  done
}
# 停服前先核对一遍，这里失败时旧服务照常运行。这一遍只为早失败：目录此时是 0770，服务组还能增删其中的条目，
# 改属主的依据是下面停服加锁之后的复检。
if ! data_dir_ok || ! db_files_ok; then fail 'refusing to hand the database to probe-hub; old service was not stopped'; fi
check_port
stop_service
SERVICE_STOPPED=1
mv -f "$BIN_TMP" "$BIN"

# SQLite 要创建和删除 WAL/SHM，目录必须可写，不能照搬只读配置目录的 0750。
# 停服务并确认该 uid 无进程之后，先把目录交给 root 并收成 0750：此后只有 root 能增删其中的条目，复检看到的
# 就是接下来要改属主的条目。预检之后、加锁之前，服务组仍能替换目录里的条目，所以复检不能省。复检失败时
# 服务已停、目录留在 0750：服务用户建不了 WAL/SHM，没有现成 WAL/SHM 时 hub 被拉起也打不开库（0750 下
# 实测报 attempt to write a readonly database），直到有人查看后重跑。不跟随链接、不递归 chown。
data_dir_ok || fail "$DATA changed after the pre-stop check"
mkdir -p "$DATA"
chown root:"$SVC_USER" "$DATA"
chmod 0750 "$DATA"
db_files_ok || fail "database files changed after the pre-stop check; $DATA stays locked at 0750 until you inspect it and rerun the installer"
# 新建的空库与已有的库走同一个交还步骤；umask 077 让它在交还之前也只有 root 可读。
[ -e "$DATA/probe.db" ] || (umask 077 && : > "$DATA/probe.db")
for file in "$DATA/probe.db" "$DATA/probe.db-wal" "$DATA/probe.db-shm"; do
  [ -e "$file" ] || continue
  chown "$SVC_USER:$SVC_USER" "$file"
  chmod 0600 "$file"
done
chmod 0770 "$DATA"
install -m 0644 "$work/unit" "$UNIT"
systemctl daemon-reload </dev/null
systemctl enable probe-hub </dev/null
systemctl start probe-hub </dev/null
SERVICE_STOPPED=0
confirm_service_started
echo "probe-hub installed and started (systemd, $ARCH, $PKG)"
echo 'Set the administrator password: probe-hub passwd --db /var/lib/probe/probe.db'
