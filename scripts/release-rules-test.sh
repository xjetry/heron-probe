#!/bin/sh
# 版本号守卫与发布规则的回归检查（make ci 调用）：Makefile 的 make 层守卫、check_version、发布目标的
# 组成与打包输入登记（spec §14.1）、RELEASE_CHANNEL 与推送命令里 latest 的取舍。只跑 make 的检查与 -n 展开，
# 不构建、不碰 docker；两个发布目标的资产集合由 scripts/release-assets-test.sh 用替身执行核对。
set -eu
make_path=$(command -v "${MAKE:-make}") || { echo "FAIL: make not found" >&2; exit 1; }
# 用例自己给出 VERSION；从外层 make 继承的 MAKEFLAGS（含命令行变量）与环境变量会顶替用例的取值。
unset MAKEFLAGS MFLAGS MAKELEVEL VERSION SMOKE_PLATFORM
work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT
failures=0

# make 只在这个 PATH 下运行：里面只有只读的文本工具（守卫可能用到的 tr、grep、sed，以及 echo——make 对
# 不含 shell 元字符的配方行直接 exec，不经 shell 的内建），加上 docker、go、pnpm、gh 的绊线。被拒的用例
# 跑的是真目标，守卫一旦失效，配方会接着构建乃至推送；在这个 PATH 下其余命令要么找不到，要么只在
# 绊线文件里留下记录，什么都不会真的发生。
mkdir "$work/bin"
# echo 与 printf 在 sh 里是内建，但 make 对不含 shell 元字符的配方行（如片段里的 @printf）不经 shell 直接
# exec，command -v 给不出路径，按系统目录找可执行文件。printf 只写标准输出，与 tr、grep、sed 同为只读文本工具。
for tool in tr grep sed echo printf; do
  for dir in /bin /usr/bin; do
    if [ -x "$dir/$tool" ]; then
      ln -s "$dir/$tool" "$work/bin/$tool"
      break
    fi
  done
  [ -e "$work/bin/$tool" ] || { echo "FAIL: no $tool in /bin or /usr/bin" >&2; exit 1; }
done
for tool in docker go pnpm gh; do
  printf '#!/bin/sh\necho "%s $*" >> "%s/tripwire"\nexit 1\n' "$tool" "$work" > "$work/bin/$tool"
  chmod +x "$work/bin/$tool"
done
# 内层 make 的 $(MAKE) 必须是绝对路径：配方里的递归行（$(MAKE) web 等）即使在 -n 下也会真的执行，而它们
# 跑在只有文本工具的 PATH 里。make 默认把 $(MAKE) 取自自己的 argv[0]，但同名环境变量的优先级高于这个默认值
# （origin 为 environment 与 default 的关系），外层 Makefile 传进来的 MAKE 在按 PATH 启动 make 的系统上就是
# 裸的 make；macOS 的 make 垫片重新 exec 时会把 argv[0] 换成绝对路径，掩盖了这一点。这里显式传绝对路径。
MAKE() {
  env PATH="$work/bin" MAKE="$make_path" "$make_path" "$@"
}

bad() {
  echo "FAIL: $*" >&2
  if [ -s "$work/out" ]; then sed 's/^/    /' "$work/out" >&2; fi
  failures=$((failures + 1))
}

# 每个消费 VERSION 的目标都必须以 check_version 开头：配方其余部分直接展开 $(VERSION)。release 换成了
# release-full / release-hub-only / release-kind（spec §14.1），rejects 的各条经 $targets 对三者照旧成立。
targets='release-full release-hub-only release-kind docker docker-smoke docker-push docker-readback docker-promote release-channel'
for t in $targets; do
  MAKE -n "$t" VERSION=v1.0.0 > "$work/out" 2>&1 || { bad "make -n $t exited non-zero"; continue; }
  # shellcheck disable=SC2016 # 比对的是 make -n 打印的配方原文，$VERSION 按字面出现
  case $(sed -n 1p "$work/out") in
    'if [ -z "$VERSION" ]; then echo "VERSION is required'*) ;;
    *) bad "$t does not start with check_version" ;;
  esac
done

# rejects TARGET VALUE WANT：退出码非 0，输出含 WANT，且第一行就是守卫的报错（shell 层以 "VERSION " 开头，
# make 层是 "*** VERSION"）。要看第一行：守卫是配方第一行、打印后立即退出，它之前不会有任何输出；守卫被删
# 时第一行是下一条配方的回显（docker-push 递归的 make docker 之后会再打印同样的守卫文字，但已不在第一行），
# 所以只凭"输出含 WANT"钉不住守卫；release-channel 的下一条是静默的 echo，删守卫后它以 0 退出，由 accepted
# 分支拦下。带换行的值会把守卫的报错拆成两行，WANT 因此不限定在第一行。
# 绊线文件为空证明 docker、go、pnpm、gh 一次都没被调用。
rejects() {
  rc=0
  MAKE "$1" "VERSION=$2" > "$work/out" 2>&1 || rc=$?
  if [ "$rc" = 0 ]; then
    bad "make $1 accepted VERSION '$2'"
  elif ! grep -qF -- "$3" "$work/out"; then
    bad "make $1 with VERSION '$2' did not fail on the version check (want '$3')"
  elif ! sed -n 1p "$work/out" | grep -q '^VERSION \|\*\*\* VERSION'; then
    bad "make $1 with VERSION '$2' ran past the version check before failing"
  fi
}

shape="cannot be an image tag"
long=$(printf 'a%.0s' $(seq 129))
nl='v1.0
x'
for t in $targets; do
  rejects "$t" '' 'VERSION is required, e.g. VERSION=v0.1.0'
  rejects "$t" "v1'x'" "$shape"
  rejects "$t" "$nl" "$shape"
  rejects "$t" 'v1.0+meta' "$shape"
  rejects "$t" '-v1' "$shape"
  rejects "$t" "$long" "$shape"
  # make 层守卫：原文里的 $ 在解析 Makefile 时就被拒绝，函数一次也没有执行。
  rejects "$t" "v1\$(shell echo expanded > $work/expanded)" "contains '\$'"
  # shellcheck disable=SC2016 # 原样交给 make 的字面值，由 make 层守卫拒绝
  rejects "$t" 'v1$$(id -un)' "contains '\$'"
  # shellcheck disable=SC2016
  rejects "$t" 'v1.0$(e)' "contains '\$'"
done
[ ! -e "$work/expanded" ] || bad "a \$(shell …) in VERSION was executed"
if [ -s "$work/tripwire" ]; then
  cp "$work/tripwire" "$work/out"
  bad "recipes ran past the version check"
fi

# AGENT_VERSION 的 make 层守卫（§14.1）：与 VERSION 同理，命令行值里的 $ 在解析 Makefile 时就被拒绝，
# $(shell …) 一次也没有执行。
for t in release-full release-hub-only release-kind; do
  # shellcheck disable=SC2016 # 交给 make 的字面值，由 make 层守卫拒绝
  MAKE "$t" VERSION=v1.0.0 'AGENT_VERSION=v1$(shell touch '"$work"'/expanded-agent)' > "$work/out" 2>&1 && bad "make $t accepted a \$ in AGENT_VERSION"
  grep -qF "contains '\$'" "$work/out" || bad "make $t did not reject a \$ in AGENT_VERSION at parse time"
  [ ! -e "$work/expanded-agent" ] || bad "a \$(shell …) in AGENT_VERSION was executed"
done

# 发布目标的组成（§14.1）。-n 只核对不经 shell 循环的行：循环体里的 $$arch 在 -n 下原样打印，按具体架构的
# 文件名去找必然找不到。完整 release 不碰 agentinputs/boundagent；只发 hub 的门禁先于任何构建（第一条 pnpm）。
MAKE -n release-hub-only VERSION=v1.2.4 AGENT_VERSION=v1.2.3 > "$work/hub-only-n" 2>&1 || bad "make -n release-hub-only failed"
MAKE -n release-full VERSION=v1.2.3 AGENT_VERSION=v1.2.3 > "$work/full-n" 2>&1 || bad "make -n release-full failed"
# shellcheck disable=SC2016 # 比对的是 make -n 打印的配方原文，$AGENT_VERSION 按字面出现
grep -qF 'go run ./scripts/agentinputs -base "$AGENT_VERSION"' "$work/hub-only-n" || bad "release-hub-only does not gate on scripts/agentinputs"
grep -qF 'go run ./scripts/boundagent fetch' "$work/hub-only-n" || bad "release-hub-only does not fetch the bound agent's installers"
gate_line=$(awk '/agentinputs/ { print NR; exit }' "$work/hub-only-n")
first_pnpm=$(awk '/pnpm/ { print NR; exit }' "$work/hub-only-n")
case $gate_line in '' | *[!0-9]*) bad "no agentinputs line in release-hub-only's -n output" ;; esac
case $first_pnpm in '' | *[!0-9]*) bad "no pnpm line in release-hub-only's -n output" ;; esac
[ "$gate_line" -lt "$first_pnpm" ] || bad "release-hub-only runs its gate after the web build"
if grep -q 'agentinputs\|boundagent' "$work/full-n"; then
  cp "$work/full-n" "$work/out"
  bad "release-full runs a hub-only step"
fi
MAKE -n release-kind VERSION=v1.2.3 > "$work/out" 2>&1 || bad "make -n release-kind failed"
# shellcheck disable=SC2016 # 比对的是 make -n 打印的配方原文，$VERSION 按字面出现
grep -qF 'go run ./scripts/releasekind -version "$VERSION" -agent "$AGENT_VERSION"' "$work/out" || bad "release-kind does not print the releasekind call"
# 以上 -n 命令里会被执行的只有 $(MAKE) web 一行的递归 make，它也只打印：到这里绊线文件仍为空。
if [ -s "$work/tripwire" ]; then
  cp "$work/tripwire" "$work/out"
  bad "a -n run executed a tripwire command"
fi

# 打包输入登记完整：deploy/agent.mk 里出现的每个 deploy/ 路径都登记在 AGENT_BUNDLE_FILES。只发 hub 的门禁
# （scripts/agentinputs）把这个片段本身当作 agent 组的输入，写死在配方里的路径它看不见。
MAKE -s agent-bundle-inputs > "$work/bundle" 2>&1 || { cp "$work/bundle" "$work/out"; bad "make agent-bundle-inputs failed"; }
# shellcheck disable=SC2013 # 路径不含空白，逐个比对
for p in $(grep -o 'deploy/[A-Za-z0-9_./-][A-Za-z0-9_./-]*' deploy/agent.mk | sort -u); do
  grep -qxF "$p" "$work/bundle" || bad "deploy/agent.mk references unregistered $p"
done

# 两个发布目标只碰登记过的文件：-n 输出里出现的每个 deploy/ 路径（make 已把变量展开成字面路径）都属于
# agent 组的登记清单，或 hub 组的 deploy/systemd/heron-hub.service 与 deploy/install-hub.sh。
for out in "$work/full-n" "$work/hub-only-n"; do
  # shellcheck disable=SC2013 # 路径不含空白，逐个比对
  for p in $(grep -o 'deploy/[A-Za-z0-9_./-][A-Za-z0-9_./-]*' "$out" | sort -u); do
    case $p in
      deploy/systemd/heron-hub.service | deploy/install-hub.sh) continue ;;
    esac
    grep -qxF "$p" "$work/bundle" || bad "release recipes reference unregistered $p"
  done
done

# 主 Makefile 不 export 影响构建的变量：export 会传进每一条配方的环境，绕开配方的 PATH 约束；片段的
# CGO_ENABLED=0 只属于它自己，其余构建参数都经显式 env 或 gflags 进入命令。主 Makefile 唯一的 export 是
# AGENT_VERSION（§14.1，配方的运行时输入，不是构建参数）。
exports=$(grep -E '^[[:space:]]*export([[:space:]]|$)|\.EXPORT_ALL_VARIABLES' Makefile || true)
if [ "$(printf '%s\n' "$exports" | grep -c .)" != 1 ] || [ "$(printf '%s\n' "$exports" | sed -n 1p)" != 'export AGENT_VERSION' ]; then
  printf '%s\n' "$exports" > "$work/out"
  bad "main Makefile exports more than AGENT_VERSION"
fi

# 默认目标不变：不带目标的 make 一直做的是执行第一个普通目标（基点 Makefile 的是 web-install），片段的
# include 位置不能改变它。
default_out=$(MAKE -n 2>&1) || bad "make -n without a target failed"
webinstall_out=$(MAKE -n web-install 2>&1) || bad "make -n web-install failed"
[ "$default_out" = "$webinstall_out" ] || bad "the default make target changed (agent.mk must include after the first regular target)"

# accepts VALUE CHANNEL：经 check_version 放行，release-channel 打印判定。
accepts() {
  rc=0
  MAKE -s release-channel "VERSION=$1" > "$work/out" 2>&1 || rc=$?
  if [ "$rc" != 0 ] || [ "$(cat "$work/out")" != "$2" ]; then
    bad "make release-channel VERSION=$1: exit $rc, want '$2'"
  fi
}
accepts v1.0.0 stable
accepts v1.0.0-rc.1 prerelease

# RELEASE_CHANNEL 本身的规则（§14）：去掉构建元数据后仍含 - 才是预发布。带 + 的值过不了 check_version，
# 这里绕开目标直接求值。
for pair in v1.2.3=stable v1.2.3-rc.1=prerelease v1.2.3+build-5=stable v1.2.3-rc.1+b.2=prerelease; do
  # shellcheck disable=SC2016 # 写进 makefile 的 make 引用
  printf 'print-release-channel:\n\t@echo $(RELEASE_CHANNEL)\n' > "$work/eval.mk"
  got=$(MAKE -s -f Makefile -f "$work/eval.mk" print-release-channel "VERSION=${pair%%=*}" 2>&1) || got="exit $?: $got"
  [ "$got" = "${pair#*=}" ] || bad "RELEASE_CHANNEL for ${pair%%=*}: got '$got', want '${pair#*=}'"
done

# latest 只由 docker-promote 按回读过的 digest 移动：推送命令在两种渠道下都不带 latest，
# docker-promote 拿到的 CHANNEL 与预发布判定一致。
for v in v1.2.3 v1.2.3-rc.1; do
  MAKE -n docker-push "VERSION=$v" > "$work/out" 2>&1 || bad "make -n docker-push VERSION=$v"
  [ "$(awk '/--push/ { n++ } END { print n + 0 }' "$work/out")" = 1 ] || bad "make -n docker-push VERSION=$v has no single push line"
  [ "$(awk '/--push/ && /latest/ { n++ } END { print n + 0 }' "$work/out")" = 0 ] || bad "docker-push for $v pushes latest"
done
for pair in v1.2.3=stable v1.2.3-rc.1=prerelease; do
  MAKE -n docker-promote "VERSION=${pair%%=*}" > "$work/out" 2>&1 || bad "make -n docker-promote VERSION=${pair%%=*}"
  grep -q "CHANNEL=${pair#*=} " "$work/out" || bad "docker-promote for ${pair%%=*} does not pass CHANNEL=${pair#*=}"
done

if [ "$failures" != 0 ]; then
  echo "release rules: $failures failure(s)" >&2
  exit 1
fi
echo "release rules ok"
