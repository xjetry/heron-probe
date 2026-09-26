#!/bin/sh
# 回读判定的回归检查（make ci 调用）：docker-readback.sh 在 docker 桩、冒烟桩与 checkimage 桩上逐例运行，
# 不访问 registry、不起容器。真实 registry 上的路径由 make docker-push/docker-readback/docker-promote 覆盖。
set -eu
here=$(cd "$(dirname "$0")" && pwd)
work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT
failures=0
repo=reg.example/probe-hub
tag_digest=sha256:$(printf 'a%.0s' $(seq 64))
other_digest=sha256:$(printf 'b%.0s' $(seq 64))

mkdir "$work/bin"
# docker 桩。状态在 $STATE 目录：tag（版本 tag 指向的 digest）、latest（digest、absent 或 unreadable）、
# 可选的 create（imagetools create 之后 latest 实际得到的 digest）、anon（匿名读取的退出码）。
# 每次调用追加到 $STATE/calls。
cat > "$work/bin/docker" << 'STUB'
#!/bin/sh
echo "$*" >> "$STATE/calls"
last=
for a; do last=$a; done
case "$1 $2 $3" in
  "buildx imagetools inspect")
    case "$*" in
      *Manifest.Manifests*)
        case "$*" in
          *'"amd64"'*) echo "sha256:$(printf '1%.0s' $(seq 64))" ;;
          *'"arm64"'*) echo "sha256:$(printf '2%.0s' $(seq 64))" ;;
        esac
        exit 0
        ;;
    esac
    case $last in
      *:latest)
        state=$(cat "$STATE/latest")
        case $state in
          absent) echo "ERROR: $last: not found" >&2; exit 1 ;;
          unreadable) echo "ERROR: failed to do request: Head \"https://reg.example/v2/\": EOF" >&2; exit 1 ;;
        esac
        printf '%s' "$state"
        ;;
      *) printf '%s' "$(cat "$STATE/tag")" ;;
    esac
    ;;
  "buildx imagetools create")
    if [ -s "$STATE/create" ]; then cp "$STATE/create" "$STATE/latest"; else echo "${last#*@}" > "$STATE/latest"; fi
    ;;
  "buildx build --builder")
    cat > /dev/null
    for a; do case $a in type=tar,dest=*) : > "${a#type=tar,dest=}" ;; esac; done
    ;;
  "manifest inspect $3") exit "$(cat "$STATE/anon")" ;;
  "pull --platform $3") ;;
  *) echo "docker stub: unexpected: $*" >&2; exit 99 ;;
esac
STUB
# 冒烟桩：FAIL_SMOKE 给出的平台失败。checkimage 桩：FAIL_CHECK 非空时失败。
cat > "$work/bin/smoke" << 'STUB'
#!/bin/sh
echo "smoke $IMAGE $SMOKE_PLATFORM" >> "$STATE/calls"
[ "$SMOKE_PLATFORM" != "${FAIL_SMOKE:-}" ]
STUB
cat > "$work/bin/checkimage" << 'STUB'
#!/bin/sh
echo "checkimage $*" >> "$STATE/calls"
[ -z "${FAIL_CHECK:-}" ]
STUB
chmod +x "$work/bin/docker" "$work/bin/smoke" "$work/bin/checkimage"

# reset LATEST：新的一组状态。
reset() {
  STATE=$(mktemp -d "$work/state.XXXXXX")
  export STATE
  echo "$tag_digest" > "$STATE/tag"
  echo "$1" > "$STATE/latest"
  echo 0 > "$STATE/anon"
}

# run NAME WANT_EXIT WANT_TEXT SUBCOMMAND [VAR=VALUE…]：在桩上运行一次，核对退出码与输出里的文字。
run() {
  name=$1 want_exit=$2 want_text=$3 sub=$4
  shift 4
  rc=0
  env PATH="$work/bin:$PATH" IMAGE_REPO=$repo VERSION=v1.2.3 RECORD="$STATE/record" ARCHES='amd64 arm64' \
    BUILDER=probe-hub-buildkit-test SMOKE="$work/bin/smoke" CHECKIMAGE="$work/bin/checkimage" TOOL_IMAGE=tool \
    "$@" "$here/docker-readback.sh" "$sub" > "$STATE/out" 2>&1 || rc=$?
  if [ "$rc" != "$want_exit" ] || ! grep -qF -- "$want_text" "$STATE/out"; then
    echo "FAIL: $name: exit $rc (want $want_exit), want output containing '$want_text'" >&2
    sed 's/^/    /' "$STATE/out" >&2
    failures=$((failures + 1))
  fi
}

# 正式版本全程通过：两个平台各按平台清单拉取并冒烟一次，核对根文件系统，latest 移到回读过的 digest。
reset "$other_digest"
run "stable verify" 0 "readback ok: $repo@$tag_digest" verify
run "stable promote" 0 "latest ok" promote CHANNEL=stable
[ "$(cat "$STATE/latest")" = "$tag_digest" ] || { echo "FAIL: stable promote left latest at $(cat "$STATE/latest")" >&2; failures=$((failures + 1)); }
[ "$(grep -c '^smoke ' "$STATE/calls")" = 2 ] || { echo "FAIL: stable verify did not smoke each platform once" >&2; failures=$((failures + 1)); }
grep -q "^pull --platform linux/arm64 $repo@sha256:2" "$STATE/calls" || { echo "FAIL: arm64 was not pulled by its platform manifest" >&2; failures=$((failures + 1)); }
grep -q "^checkimage .* amd64 arm64$" "$STATE/calls" || { echo "FAIL: the root filesystems were not checked for both platforms" >&2; failures=$((failures + 1)); }

# 预发布全程通过：latest 不存在，或指向别的 digest；promote 不移动 latest。
reset absent
run "prerelease verify" 0 "readback ok" verify
run "prerelease promote, latest absent" 0 "latest absent" promote CHANNEL=prerelease
reset "$other_digest"
run "prerelease verify" 0 "readback ok" verify
run "prerelease promote, latest elsewhere" 0 "latest $other_digest" promote CHANNEL=prerelease
! grep -q 'imagetools create' "$STATE/calls" || { echo "FAIL: a prerelease moved latest" >&2; failures=$((failures + 1)); }

# latest 读取失败：一律失败，不当作不存在。
reset unreadable
run "prerelease verify" 0 "readback ok" verify
run "prerelease promote, latest unreadable" 1 "cannot read $repo:latest" promote CHANNEL=prerelease

# latest 指向别处：预发布指向了自己，或正式版本移动之后回读到的不是回读过的 digest。
reset "$tag_digest"
run "prerelease verify" 0 "readback ok" verify
run "prerelease promote, latest points at it" 1 "latest points at this prerelease" promote CHANNEL=prerelease
reset absent
echo "$other_digest" > "$STATE/create"
run "stable verify" 0 "readback ok" verify
run "stable promote, latest lands elsewhere" 1 "latest points at $other_digest after the move" promote CHANNEL=stable

# 某平台 checkimage 或冒烟失败：不写记录，promote 拒绝。
reset absent
run "verify, checkimage fails" 1 "failed checkimage" verify FAIL_CHECK=1
run "promote without a record" 1 "no readback record" promote CHANNEL=stable
reset absent
run "verify, arm64 smoke fails" 1 "smoke of $repo@sha256:2" verify FAIL_SMOKE=linux/arm64
[ ! -e "$STATE/record" ] || { echo "FAIL: a failed smoke still wrote the readback record" >&2; failures=$((failures + 1)); }

# 匿名不可取；回读之后版本 tag 被重推。
reset absent
echo 1 > "$STATE/anon"
run "verify, not anonymously pullable" 1 "is not anonymously pullable" verify
reset absent
run "verify" 0 "readback ok" verify
echo "$other_digest" > "$STATE/tag"
run "promote after the tag moved" 1 "moved from $tag_digest to $other_digest" promote CHANNEL=stable

if [ "$failures" != 0 ]; then
  echo "readback checks: $failures failure(s)" >&2
  exit 1
fi
echo "readback checks ok"
