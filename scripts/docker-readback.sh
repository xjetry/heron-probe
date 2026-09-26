#!/bin/sh
# 发布后回读 hub 镜像（spec §14）。release.yml 依次调用 make docker-push（只推版本 tag）、
# make docker-readback（本脚本 verify）、make docker-promote（本脚本 promote），最后才 gh release create。
#   docker-readback.sh verify    回读 registry 上 <IMAGE_REPO>:<VERSION> 指向的索引，通过后写 RECORD
#   docker-readback.sh promote   按 RECORD 里回读过的 digest 处理 latest
# 不变式：latest 只会指向回读通过的镜像；回读核对的是 registry 上实际存在的内容，不是构建时的中间产物。
# 参数经 make 传入：镜像名、架构集合、构建器与预发布判定只在 Makefile 定义。
set -eu
: "${IMAGE_REPO:?IMAGE_REPO is required}" "${VERSION:?VERSION is required}" "${RECORD:?RECORD is required}"

fail() {
  echo "FAIL: $*" >&2
  exit 1
}

work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT

# index_digest REF：REF 当前指向的索引 digest；读不到时失败。
index_digest() {
  docker buildx imagetools inspect --format '{{.Manifest.Digest}}' "$1"
}

# latest_state：latest 当前指向的 digest；registry 确认不存在时打印 absent。其余读取失败一律中止，不当作
# 不存在——当作不存在会让"预发布没有碰 latest"在读不到时照样通过。buildx 对不存在的 tag 与仓库报
# "<引用>: not found"（buildx v0.33.0 对 ghcr.io 与 registry:2 实测；连接失败、DNS 失败是别的文字）。
# 文字若随 buildx 版本变了，这里按读取失败中止，失败方向是封闭的。
latest_state() {
  if index_digest "$IMAGE_REPO:latest" 2> "$work/latest.err"; then
    return 0
  fi
  case $(cat "$work/latest.err") in
    *"$IMAGE_REPO:latest: not found"*) echo absent ;;
    *)
      cat "$work/latest.err" >&2
      fail "cannot read $IMAGE_REPO:latest (a failed read is not taken as absent)"
      ;;
  esac
}

case "${1:-}" in
verify)
  : "${ARCHES:?ARCHES is required}" "${BUILDER:?BUILDER is required}" "${SMOKE:?SMOKE is required}" \
    "${CHECKIMAGE:?CHECKIMAGE is required}" "${TOOL_IMAGE:?TOOL_IMAGE is required}"
  rm -f "$RECORD"
  image=$IMAGE_REPO:$VERSION
  digest=$(index_digest "$image") || fail "cannot read $image"
  # 之后的每一步都按这个 digest 引用：同一个 tag 在回读期间被重推，也改变不了这里核对的对象。
  ref=$IMAGE_REPO@$digest

  # 匿名可取：新建的 ghcr 包默认私有时匿名 docker pull 会失败，与 Release 资产要求仓库公开（§14）同一理由。
  # 首次发布在这里失败时，把包的可见性改为公开后重跑 release job。
  mkdir "$work/anonymous"
  DOCKER_CONFIG=$work/anonymous docker manifest inspect "$ref" > /dev/null ||
    fail "$ref is not anonymously pullable; make the ghcr package public, then re-run the release job"

  # 逐平台冒烟：按平台清单的 digest 拉取并运行（原因见 image-platform-ref.sh）。在 amd64 运行器上 arm64 经
  # QEMU 运行。
  for arch in $ARCHES; do
    platform_ref=$("$(dirname "$0")/image-platform-ref.sh" "$ref" "linux/$arch")
    docker pull --platform "linux/$arch" "$platform_ref" || fail "pull $platform_ref"
    IMAGE=$platform_ref VERSION=$VERSION SMOKE_PLATFORM=linux/$arch TOOL_IMAGE=$TOOL_IMAGE "$SMOKE" ||
      fail "smoke of $platform_ref (linux/$arch) failed"
  done

  # 逐平台核对根文件系统：构建器按 digest 从 registry 取回各平台，以与 make docker 相同的 tar 布局
  # （每个平台一个 linux_<arch>/）导出，交给同一个 checkimage。不用 docker export：它导出的是容器的文件
  # 系统，带着运行时注入的 /.dockerenv、/dev、/proc、/sys、/etc/hosts 等条目。冒烟照不到架构错配（amd64
  # 运行器会原生跑通装错了 amd64 二进制的 arm64 条目），这里读 ELF 头能照到。
  # shellcheck disable=SC2086 # 架构清单按词拆成参数
  platforms=$(printf 'linux/%s,' $ARCHES)
  printf 'FROM %s\n' "$ref" |
    docker buildx build --builder "$BUILDER" --platform "${platforms%,}" --output "type=tar,dest=$work/rootfs.tar" - ||
    fail "export the root filesystems of $ref"
  # shellcheck disable=SC2086 # 架构清单按词拆成参数
  "$CHECKIMAGE" "$work/rootfs.tar" $ARCHES || fail "the root filesystems of $ref failed checkimage"

  printf '%s %s\n' "$VERSION" "$digest" > "$RECORD"
  echo "readback ok: $ref"
  ;;
promote)
  : "${CHANNEL:?CHANNEL is required}"
  [ -s "$RECORD" ] || fail "no readback record at $RECORD; run make docker-readback first"
  read -r recorded_version digest < "$RECORD"
  [ "$recorded_version" = "$VERSION" ] || fail "the readback record is for $recorded_version, not $VERSION"
  # 回读之后版本 tag 没有被重推：要移动或核对的正是回读过的那一个索引。
  now=$(index_digest "$IMAGE_REPO:$VERSION") || fail "cannot read $IMAGE_REPO:$VERSION"
  [ "$now" = "$digest" ] || fail "$IMAGE_REPO:$VERSION moved from $digest to $now after the readback"
  case $CHANNEL in
  stable)
    # 来源是索引时，imagetools create 原样复制，latest 与版本 tag 是同一个 digest（registry:2 上实测）；
    # 这里仍回读确认，不依赖这一点。
    docker buildx imagetools create -t "$IMAGE_REPO:latest" "$IMAGE_REPO@$digest"
    latest=$(latest_state)
    [ "$latest" = "$digest" ] || fail "latest points at $latest after the move, want $digest"
    ;;
  prerelease)
    latest=$(latest_state)
    [ "$latest" != "$digest" ] || fail "latest points at this prerelease ($digest)"
    ;;
  *) fail "CHANNEL must be stable or prerelease, got '$CHANNEL'" ;;
  esac
  echo "latest ok: $IMAGE_REPO:$VERSION ($CHANNEL, latest $latest)"
  ;;
*)
  echo "usage: docker-readback.sh verify | promote" >&2
  exit 2
  ;;
esac
