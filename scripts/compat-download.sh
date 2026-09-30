#!/bin/sh
# 兼容基线是仓库内审查过的 tag 与摘要，而不是可变的 latest 或下载时取得的校验和。
# 明确输出渠道，不把预发布版当作稳定版本的兼容凭据。
set -eu
[ "$#" = 1 ] || { echo "usage: $0 NEW_OUTPUT_DIRECTORY" >&2; exit 1; }
pin="$(cd "$(dirname "$0")" && pwd)/compat-agent.json"
# 缺少发布基线必须失败，不能把未运行的兼容验收当作通过。
if [ -f "$pin" ] && jq -e '.tag == null' "$pin" > /dev/null; then
  echo "FAIL: no published Heron release is pinned as the compatibility baseline yet (scripts/compat-agent.json has tag null)" >&2
  exit 1
fi
jq -e '
  .repository == "xjetry/heron-probe" and
  (.tag | type == "string" and test("^v[0-9]+\\.[0-9]+\\.[0-9]+(-[A-Za-z0-9.-]+)?$")) and
  (.releaseKind == (if .tag | contains("-") then "prerelease" else "stable" end)) and
  ([.assets[].arch] | sort) == ["amd64", "arm64"] and
  all(.assets[]; .sha256 | type == "string" and test("^[a-f0-9]{64}$"))
' "$pin" > /dev/null || { echo "FAIL: invalid or absent published-agent compatibility baseline" >&2; exit 1; }
tag=$(jq -r .tag "$pin")
kind=$(jq -r .releaseKind "$pin")
repository=$(jq -r .repository "$pin")
[ ! -e "$1" ] || { echo "FAIL: output directory already exists: $1" >&2; exit 1; }
mkdir -p "$1"
out=$(cd "$1" && pwd)
complete=false
cleanup() {
  if [ "$complete" = false ]; then rm -rf "$out"; fi
}
trap cleanup EXIT
trap 'exit 1' INT TERM HUP
echo "compatibility baseline: $repository $tag ($kind)"
for arch in amd64 arm64; do
  asset="heron-agent_linux_$arch.tar.gz"
  expected=$(jq -r --arg arch "$arch" '.assets[] | select(.arch == $arch) | .sha256' "$pin")
  curl --fail --show-error --silent --location --retry 3 --connect-timeout 15 --max-time 180 \
    "https://github.com/$repository/releases/download/$tag/$asset" -o "$out/$asset"
  actual=$(sha256sum "$out/$asset")
  actual=${actual%% *}
  [ "$actual" = "$expected" ] || { echo "FAIL: SHA256 mismatch for $tag/$asset: $actual, expected $expected" >&2; exit 1; }
  # 校验成功之后才解包，且只取固定名称；其它包内路径不能写入输出目录。
  tar -xzf "$out/$asset" -C "$out" heron-agent
  if ! { [ -f "$out/heron-agent" ] && [ ! -L "$out/heron-agent" ]; }; then echo "FAIL: $asset has no regular heron-agent" >&2; exit 1; fi
  mv "$out/heron-agent" "$out/heron-agent-linux-$arch"
  chmod +x "$out/heron-agent-linux-$arch"
  rm "$out/$asset"
  echo "verified $tag/$asset sha256:$expected"
done
complete=true
