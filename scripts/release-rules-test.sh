#!/bin/sh
# 版本号守卫与预发布判定的回归检查（make ci 调用）：Makefile 的 make 层守卫、check_version、
# RELEASE_CHANNEL 与推送命令里 latest 的取舍。只跑 make 的检查与 -n 展开，不构建、不碰 docker。
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
# echo 在 sh 里是内建，command -v 给不出路径，按系统目录找可执行文件。
for tool in tr grep sed echo; do
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
MAKE() {
  env PATH="$work/bin" "$make_path" "$@"
}

bad() {
  echo "FAIL: $*" >&2
  if [ -s "$work/out" ]; then sed 's/^/    /' "$work/out" >&2; fi
  failures=$((failures + 1))
}

# 每个消费 VERSION 的目标都必须以 check_version 开头：配方其余部分直接展开 $(VERSION)。
targets='release docker docker-smoke docker-push docker-readback docker-promote release-channel'
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
# 时第一行是 make 回显的下一条配方（docker-push 递归的 make docker 之后会再打印同样的守卫文字，但已不在
# 第一行），所以只凭"输出含 WANT"钉不住守卫。带换行的值会把守卫的报错拆成两行，WANT 因此不限定在第一行。
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
