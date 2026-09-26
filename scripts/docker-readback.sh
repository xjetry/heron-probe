#!/bin/sh
# 发布后回读 hub 镜像（spec §14）：release.yml 在 make docker-push 之后、gh release create 之前调用。
#   docker-readback.sh latest          打印 registry 上 latest 当前的 digest；取不到时打印 absent
#   docker-readback.sh verify BEFORE   回读刚推送的版本：匿名可取、各架构冒烟、latest 的指向；
#                                      BEFORE 是推送前 latest 子命令的输出
# 参数经 make docker-latest / make docker-readback 传入：镜像名、架构集合与预发布判定只在 Makefile 定义。
set -eu
: "${IMAGE_REPO:?IMAGE_REPO is required}"

fail() {
  echo "FAIL: $*" >&2
  exit 1
}

# 不存在与读取失败都记为 absent：对预发布，推送前取不到而推送后取到了，就是 latest 被移动，按失败处理；
# 两次都读取失败时无从比较，这是这项检查照不到的情形。
latest_digest() {
  docker buildx imagetools inspect --format '{{.Manifest.Digest}}' "$IMAGE_REPO:latest" || echo absent
}

case "${1:-}" in
latest)
  latest_digest
  ;;
verify)
  before=${2:-}
  [ -n "$before" ] || fail "verify needs the latest digest recorded before the push (or absent)"
  : "${VERSION:?VERSION is required}" "${CHANNEL:?CHANNEL is required}" "${ARCHES:?ARCHES is required}"
  image=$IMAGE_REPO:$VERSION
  # 匿名可取：新建的 ghcr 包默认私有时匿名 docker pull 会失败，与 Release 资产要求仓库公开（§14）同一理由。
  # 首次发布在这里失败时，把包的可见性改为公开后重跑 release job。
  anon=$(mktemp -d)
  DOCKER_CONFIG=$anon docker manifest inspect "$image" > /dev/null || fail "$image is not anonymously pullable; make the ghcr package public, then re-run the release job"
  for arch in $ARCHES; do
    docker pull --platform "linux/$arch" "$image"
    IMAGE=$image VERSION=$VERSION SMOKE_PLATFORM=linux/$arch "$(dirname "$0")/docker-smoke.sh"
  done
  tag_digest=$(docker buildx imagetools inspect --format '{{.Manifest.Digest}}' "$image")
  now=$(latest_digest)
  case $CHANNEL in
  stable) [ "$now" = "$tag_digest" ] || fail "latest points at $now, want $tag_digest" ;;
  prerelease) [ "$now" = "$before" ] || fail "a prerelease moved latest from $before to $now" ;;
  *) fail "CHANNEL must be stable or prerelease, got '$CHANNEL'" ;;
  esac
  echo "readback ok: $image ($CHANNEL, latest $now)"
  ;;
*)
  echo "usage: docker-readback.sh latest | verify BEFORE" >&2
  exit 2
  ;;
esac
