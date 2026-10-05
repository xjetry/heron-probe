#!/bin/sh
# 替身执行测试（make ci 调用）：在最小副本里用 go/pnpm/shellcheck 替身真正执行两个发布目标，核对产出的
# 资产集合——这是两种 release 的产物分组（spec §14.1）唯一被执行验证的地方；发布规则测试只看 make -n。
# 真正的构建产物由 make release-full 与基点树的对照覆盖，这里不构建，替身产物是占位字节。
set -eu

repo=$(CDPATH='' cd -- "$(dirname -- "$0")/.." && pwd)
work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT
failures=0

bad() {
  echo "FAIL: $*" >&2
  if [ -s "$work/out" ]; then sed 's/^/    /' "$work/out" >&2; fi
  failures=$((failures + 1))
}

# 最小副本：Makefile、AGENT_VERSION 与 deploy/ 整个目录。配方只把 ./cmd/... 交给 go，替身不需要源码。
mkdir "$work/stub" "$work/repo"
cp "$repo/Makefile" "$repo/AGENT_VERSION" "$work/repo/"
cp -R "$repo/deploy" "$work/repo/deploy"

# go 替身：构建写占位文件，脚本子命令按各自语义回应；每次调用追加到调用日志。pnpm、shellcheck 退出 0。
# tar、cp、mkdir、rm、sha256sum 用真的：替身 go 只替 go，打包与清单仍走真实路径。
cat > "$work/stub/go" <<'STUB'
#!/bin/sh
# shellcheck disable=SC2016 # 记录的是 make 展开前的字面值
printf '%s\n' "go $*" >> "$GO_CALL_LOG"
case $1 in
  build)
    out=
    prev=
    for a in "$@"; do
      if [ "$prev" = "-o" ]; then out=$a; fi
      prev=$a
    done
    if [ -z "$out" ]; then echo "stub go: no -o in: $*" >&2; exit 1; fi
    printf 'stub binary\n' > "$out"
    ;;
  run)
    shift
    cmd=$1
    shift
    case $cmd in
      ./scripts/releasekind)
        printf '%s\n' "$STUB_KIND"
        ;;
      ./scripts/stampinstall)
        dir=
        while [ $# -gt 0 ]; do
          case $1 in
            -version) shift 2 ;;
            -dir) dir=$2; shift 2 ;;
            -*) echo "stub stampinstall: unexpected $1" >&2; exit 1 ;;
            *) cp "$1" "$dir/$(basename "$1")"; shift ;;
          esac
        done
        ;;
      ./scripts/boundagent)
        [ "$1" = fetch ] && shift
        version=
        dir=
        while [ $# -gt 0 ]; do
          case $1 in
            -version) version=$2; shift 2 ;;
            -dir) dir=$2; shift 2 ;;
            -linux-arches | -darwin-arches) shift 2 ;;
            *) echo "stub boundagent: unexpected $1" >&2; exit 1 ;;
          esac
        done
        printf 'bound %s\n' "$version" > "$dir/install.sh"
        printf 'bound %s\n' "$version" > "$dir/install-macos.sh"
        ;;
      ./scripts/agentinputs | ./scripts/checkstatic) ;;
      *) echo "stub go: unexpected go run $cmd" >&2; exit 1 ;;
    esac
    ;;
  *) echo "stub go: unexpected args: $*" >&2; exit 1 ;;
esac
STUB
for tool in pnpm shellcheck; do
  printf '#!/bin/sh\nexit 0\n' > "$work/stub/$tool"
done
chmod +x "$work/stub"/*

# 用例 1：完整 release。dist 恰为 14 个 tar 包 + 三个安装脚本 + SHA256SUMS；SHA256SUMS 恰好列出其余全部
# 文件；tar 成员与旧配方一致；调用日志里没有 agentinputs、boundagent。
: > "$work/go-calls"
cd "$work/repo"
env PATH="$work/stub:$PATH" MAKE="$(command -v make)" GO_CALL_LOG="$work/go-calls" STUB_KIND=full \
  make release-full VERSION=v1.2.3 AGENT_VERSION=v1.2.3 > "$work/out" 2>&1 || bad "make release-full failed (stub)"
want=$(printf '%s\n' \
  heron-agent_darwin_amd64.tar.gz heron-agent_darwin_arm64.tar.gz \
  heron-agent_linux_386.tar.gz heron-agent_linux_amd64.tar.gz heron-agent_linux_arm64.tar.gz \
  heron-agent_linux_armv7.tar.gz heron-agent_linux_riscv64.tar.gz \
  heron-hub_linux_amd64.tar.gz heron-hub_linux_arm64.tar.gz \
  heron-updater_linux_386.tar.gz heron-updater_linux_amd64.tar.gz heron-updater_linux_arm64.tar.gz \
  heron-updater_linux_armv7.tar.gz heron-updater_linux_riscv64.tar.gz \
  install-hub.sh install-macos.sh install.sh SHA256SUMS | sort)
# shellcheck disable=SC2012 # 与期望清单逐字比对的是目录里的非隐藏文件，正是 ls 的输出
got=$(cd dist && ls | sort)
if [ "$got" != "$want" ]; then
  bad "release-full produced a wrong asset set:
$(printf '%s\n' "$got" | sed 's/^/    got /')
$(printf '%s\n' "$want" | sed 's/^/    want /')"
fi
listed=$(awk '{ print $2 }' dist/SHA256SUMS | sort)
# shellcheck disable=SC2010 # 同上，比对的是目录里的非隐藏文件
others=$(cd dist && ls | grep -vx SHA256SUMS | sort)
[ "$listed" = "$others" ] || bad "SHA256SUMS does not list exactly the other files"
for arch in amd64 arm64 armv7 386 riscv64; do
  members=$(tar -tzf "dist/heron-agent_linux_$arch.tar.gz" | sort)
  [ "$members" = "$(printf 'heron-agent\nheron-agent.openrc\nheron-agent.service\n' | sort)" ] ||
    bad "heron-agent_linux_$arch.tar.gz members: $members"
  members=$(tar -tzf "dist/heron-updater_linux_$arch.tar.gz" | sort)
  [ "$members" = "$(printf 'heron-updater\nheron-updater-agent.service\nheron-updater-hub.service\n' | sort)" ] ||
    bad "heron-updater_linux_$arch.tar.gz members: $members"
done
for arch in amd64 arm64; do
  members=$(tar -tzf "dist/heron-agent_darwin_$arch.tar.gz" | sort)
  [ "$members" = "$(printf 'heron-agent\nxyz.heron.agent.plist\n' | sort)" ] ||
    bad "heron-agent_darwin_$arch.tar.gz members: $members"
  members=$(tar -tzf "dist/heron-hub_linux_$arch.tar.gz" | sort)
  [ "$members" = "$(printf 'heron-hub\nheron-hub.service\n' | sort)" ] ||
    bad "heron-hub_linux_$arch.tar.gz members: $members"
done
if grep -q 'agentinputs\|boundagent' "$work/go-calls"; then
  cp "$work/go-calls" "$work/out"
  bad "release-full ran a hub-only step"
fi

# 用例 2：只发 hub。dist 恰为 hub 与 updater 各两个架构的 tar 包、复制来的 install-hub.sh、取自绑定版本的
# 两个 agent 安装脚本与 SHA256SUMS；agentinputs 的门禁在第一次 go build 之前。
: > "$work/go-calls"
rm -rf dist
env PATH="$work/stub:$PATH" MAKE="$(command -v make)" GO_CALL_LOG="$work/go-calls" STUB_KIND=hub-only \
  make release-hub-only VERSION=v1.2.4 AGENT_VERSION=v1.2.3 > "$work/out" 2>&1 || bad "make release-hub-only failed (stub)"
want=$(printf '%s\n' \
  heron-hub_linux_amd64.tar.gz heron-hub_linux_arm64.tar.gz \
  heron-updater_linux_amd64.tar.gz heron-updater_linux_arm64.tar.gz \
  install-hub.sh install-macos.sh install.sh SHA256SUMS | sort)
# shellcheck disable=SC2012 # 与期望清单逐字比对的是目录里的非隐藏文件，正是 ls 的输出
got=$(cd dist && ls | sort)
if [ "$got" != "$want" ]; then
  bad "release-hub-only produced a wrong asset set:
$(printf '%s\n' "$got" | sed 's/^/    got /')
$(printf '%s\n' "$want" | sed 's/^/    want /')"
fi
[ "$(cat dist/install.sh)" = "bound v1.2.3" ] || bad "install.sh is not the bound version's copy"
[ "$(cat dist/install-macos.sh)" = "bound v1.2.3" ] || bad "install-macos.sh is not the bound version's copy"
gate=$(awk '/agentinputs/ { print NR; exit }' "$work/go-calls")
first_build=$(awk '$2 == "build" { print NR; exit }' "$work/go-calls")
case $gate in '' | *[!0-9]*) bad "release-hub-only never ran scripts/agentinputs" ;; esac
case $first_build in '' | *[!0-9]*) bad "release-hub-only never built" ;; esac
[ "$gate" -lt "$first_build" ] || bad "release-hub-only built before its agent-inputs gate"
grep -q '^go run ./scripts/boundagent fetch' "$work/go-calls" || bad "release-hub-only did not fetch the bound agent's installers"

# 用例 3：种类不符即失败：release-full 撞上 hub-only 的判定，任何 go build 之前退出，dist 原样。
: > "$work/go-calls"
rm -rf dist
mkdir dist
: > dist/.marker
rc=0
env PATH="$work/stub:$PATH" MAKE="$(command -v make)" GO_CALL_LOG="$work/go-calls" STUB_KIND=hub-only \
  make release-full VERSION=v1.2.3 AGENT_VERSION=v1.2.3 > "$work/out" 2>&1 || rc=$?
if [ "$rc" = 0 ]; then
  bad "release-full accepted a hub-only release"
else
  grep -q "does not match this release's kind" "$work/out" || bad "release-full failed for the wrong reason"
fi
if grep -q '^go build' "$work/go-calls"; then
  cp "$work/go-calls" "$work/out"
  bad "release-full built before failing the kind check"
fi
[ -e dist/.marker ] || bad "release-full touched dist before failing the kind check"

if [ "$failures" != 0 ]; then
  echo "release assets: $failures failure(s)" >&2
  exit 1
fi
echo "release assets ok"
