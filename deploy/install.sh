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

stop_service() {
  service_installed || return 0
  case "$INIT" in
    systemd)
      systemctl stop probe-agent </dev/null || {
        echo "failed to stop probe-agent" >&2; return 1;
      }
      if systemctl is-active --quiet probe-agent </dev/null; then
        echo "failed to stop probe-agent: service is still active" >&2; return 1
      fi
      ;;
    openrc)
      # OpenRC 0.55.1（Alpine 3.21）实测：supervise-daemon 被杀、子进程留存时 status 为 unsupervised（64），
      # 此时 stop 打印 "Unable to shut down the supervisor" 却返回 0，之后 status 为 stopped（3），
      # 子进程仍在运行。stop 的退出码与 status 都证明不了进程已退出，所以直接查进程：
      # $SVC_USER 是专供 agent 的 nologin 账户（create_account 建立），以它为 EUID 的进程都属于本服务。
      rc-service probe-agent stop </dev/null || {
        echo "failed to stop probe-agent" >&2; return 1;
      }
      command -v pgrep >/dev/null 2>&1 || {
        echo "pgrep is required to verify that probe-agent stopped" >&2; return 1;
      }
      found=0
      pids=$(pgrep -u "$SVC_USER" </dev/null) || found=$?
      case "$found" in
        0) echo "failed to stop probe-agent: processes still running as $SVC_USER:" >&2
           echo "$pids" >&2; return 1;;
        1) ;;
        *) echo "pgrep exited $found while checking processes of $SVC_USER" >&2; return 1;;
      esac
      ;;
  esac
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
      if [ -e /etc/systemd/system/probe-agent.service ]; then
        systemctl disable probe-agent </dev/null
      fi
      rm -f /etc/systemd/system/probe-agent.service
      systemctl daemon-reload </dev/null;;
    openrc)
      if [ -e /etc/runlevels/default/probe-agent ]; then
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
  actual_group=$(id -gn "$SVC_USER" 2>/dev/null) || {
    echo "failed to create system user $SVC_USER" >&2; exit 1; }
  [ "$actual_group" = "$SVC_USER" ] || {
    echo "user $SVC_USER exists with primary group $actual_group; expected $SVC_USER" >&2; exit 1; }
}
create_account

[ -n "$BASE_URL" ] || {
  if [ -n "$VERSION" ]; then BASE_URL="$REPO/releases/download/$VERSION"
  else BASE_URL="$REPO/releases/latest/download"; fi
}

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

PKG="probe-agent_linux_$ARCH.tar.gz"
if command -v curl >/dev/null 2>&1; then FETCH=curl
elif command -v wget >/dev/null 2>&1; then FETCH=wget
else echo "neither curl nor wget is available to download $PKG" >&2; exit 1; fi
command -v sha256sum >/dev/null 2>&1 || { echo "sha256sum is required to verify downloads" >&2; exit 1; }

work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT

dl() {
  if [ "$FETCH" = curl ]; then curl -fsSL -o "$2" "$1"; else wget -q -O "$2" "$1"; fi
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

# 先解包并确认服务所需文件齐全，避免包损坏时先停掉正在运行的旧服务。
tar -xzf "$work/$PKG" -C "$work"
for f in probe-agent probe-agent.service probe-agent.openrc; do
  [ -f "$work/$f" ] || { echo "package is missing $f" >&2; exit 1; }
done
stop_service
# 同目录 rename 原子替换目录项：进程看到的始终是完整的旧文件或完整的新文件，中断不会留下截断二进制。
install -m 0755 "$work/probe-agent" "$BIN.tmp.$$"
mv -f "$BIN.tmp.$$" "$BIN"

if [ ! -f "$CFG" ]; then
  # 注册只在没有配置时发生；配置落盘后重跑不再注册，所以注册之后的步骤失败时，重跑不会多耗窗口名额。
  set -- register --hub "$HUB" --key "$KEY" --config "$CFG"
  if [ -n "$NAME" ]; then set -- "$@" --name "$NAME"; fi
  "$BIN" "$@" </dev/null
  # 目录属 root、组 probe-agent、0750：服务用户能读到配置，但不能增删或替换目录项，
  # root 在其中的操作（register 写配置、下面对 $CFG 的 chown）不会被链接或竞态劫持。
  # 目录无需对服务用户可写：写配置只发生在以 root 执行的 register 里，cmd/agent 的 run 只调用 LoadConfig、不调用 SaveConfig。
  chown root:"$SVC_USER" "$CFG_DIR"
  chmod 0750 "$CFG_DIR"
  chown "$SVC_USER:$SVC_USER" "$CFG"
else
  if [ -n "$KEY" ]; then echo "existing config found; keeping the current registration (--key ignored)"; fi
fi

# 服务定义每次覆盖，单元的改动随升级下发。
# 走到这里时服务一定没在运行：服务定义已安装则 stop_service 已确认停下，否则停不下来已退出；
# 未安装则没有由它管理的进程。所以用 start，不依赖 restart 对已停服务等价于 start。
case "$INIT" in
  systemd)
    install -m 0644 "$work/probe-agent.service" /etc/systemd/system/probe-agent.service
    systemctl daemon-reload </dev/null
    systemctl enable probe-agent </dev/null
    systemctl start probe-agent </dev/null;;
  openrc)
    install -m 0755 "$work/probe-agent.openrc" /etc/init.d/probe-agent
    # 重跑时它已在 default runlevel 里；只在不在时才加，不依赖 rc-update 对重复 add 的退出码。
    [ -e /etc/runlevels/default/probe-agent ] || rc-update add probe-agent default </dev/null
    rc-service probe-agent start </dev/null;;
esac
echo "probe-agent installed and started ($INIT, $ARCH, $PKG)"
