#!/bin/sh
# 用本地产物替身验收下载失败、无基线与摘要不匹配都不能变成兼容通过；不访问网络或执行发布二进制。
set -eu
root=$(cd "$(dirname "$0")/.." && pwd)
work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT
mkdir "$work/scripts" "$work/bin" "$work/package"
cp "$root/scripts/compat-download.sh" "$work/scripts/"
printf '#!/bin/sh\nprintf "published fixture\\n"\n' > "$work/package/heron-agent"
chmod +x "$work/package/heron-agent"
COPYFILE_DISABLE=1 tar --no-xattrs -czf "$work/fixture.tar.gz" -C "$work/package" heron-agent
digest=$(sha256sum "$work/fixture.tar.gz")
digest=${digest%% *}
# 基线样本在这里构造完整，不从仓库里的 compat-agent.json 派生：那份文件可以处在"尚无基线"的状态。
jq -n --arg digest "$digest" '{repository: "xjetry/heron-probe", tag: "v9.8.7-rc.1", releaseKind: "prerelease", assets: [{arch: "amd64", sha256: $digest}, {arch: "arm64", sha256: $digest}]}' > "$work/pin.json"
cat > "$work/bin/curl" <<'SH'
#!/bin/sh
set -eu
echo called >> "$FIXTURE_ROOT/curl-calls"
if [ "${FAIL_DOWNLOAD:-0}" = 1 ]; then exit 22; fi
while [ "$#" -gt 0 ]; do
  case "$1" in
    https://*) url=$1 ;;
    -o) shift; out=$1 ;;
  esac
  shift
done
case "$url" in
  https://github.com/xjetry/heron-probe/releases/download/v9.8.7-rc.1/heron-agent_linux_amd64.tar.gz|https://github.com/xjetry/heron-probe/releases/download/v9.8.7-rc.1/heron-agent_linux_arm64.tar.gz) ;;
  *) echo "unexpected release URL: $url" >&2; exit 1 ;;
esac
cp "$FIXTURE_ROOT/fixture.tar.gz" "$out"
SH
chmod +x "$work/bin/curl"
export FIXTURE_ROOT="$work"
export PATH="$work/bin:$PATH"

rejects() {
  label=$1; want=$2
  rc=0
  "$work/scripts/compat-download.sh" "$work/output" > "$work/result" 2>&1 || rc=$?
  [ "$rc" != 0 ] || { echo "FAIL: $label unexpectedly succeeded" >&2; exit 1; }
  grep -F "$want" "$work/result" > /dev/null || { echo "FAIL: $label failed for the wrong reason" >&2; cat "$work/result" >&2; exit 1; }
  [ ! -e "$work/output" ] || { echo "FAIL: $label retained unverified artifacts" >&2; exit 1; }
  echo "rejected $label: exit $rc ($want)"
}

# 缺失 pin 与缺少一个架构都在网络请求之前拒绝；先前成功场景会独立确认 curl 替身可用。
cp "$work/pin.json" "$work/scripts/compat-agent.json"
"$work/scripts/compat-download.sh" "$work/output" > "$work/result" 2>&1
for arch in amd64 arm64; do
  [ "$("$work/output/heron-agent-linux-$arch")" = "published fixture" ] || { echo "FAIL: missing verified $arch agent" >&2; exit 1; }
done
[ "$(awk 'END { print NR }' "$work/curl-calls")" = 2 ] || { echo "FAIL: did not download both release assets" >&2; exit 1; }
rm -rf "$work/output"
rm "$work/curl-calls" "$work/scripts/compat-agent.json"
rejects "missing pin" "invalid or absent published-agent compatibility baseline"
[ ! -e "$work/curl-calls" ] || { echo "FAIL: missing pin attempted a download" >&2; exit 1; }
# 仓库里当前的基线状态：尚无 Heron 发布时 tag 为 null，下载必须明确失败，不能变成兼容通过或跳过。
cp "$root/scripts/compat-agent.json" "$work/scripts/compat-agent.json"
if jq -e '.tag == null' "$work/scripts/compat-agent.json" > /dev/null; then
  rejects "no Heron baseline" "no published Heron release is pinned"
  [ ! -e "$work/curl-calls" ] || { echo "FAIL: the absent baseline attempted a download" >&2; exit 1; }
fi
jq '.assets = [.assets[0]]' "$work/pin.json" > "$work/scripts/compat-agent.json"
rejects "missing architecture" "invalid or absent published-agent compatibility baseline"
[ ! -e "$work/curl-calls" ] || { echo "FAIL: incomplete pin attempted a download" >&2; exit 1; }
jq '.releaseKind = "stable"' "$work/pin.json" > "$work/scripts/compat-agent.json"
rejects "incorrect release channel" "invalid or absent published-agent compatibility baseline"
jq '.tag = "latest"' "$work/pin.json" > "$work/scripts/compat-agent.json"
rejects "mutable release tag" "invalid or absent published-agent compatibility baseline"
[ ! -e "$work/curl-calls" ] || { echo "FAIL: invalid release identity attempted a download" >&2; exit 1; }

# 故意换掉摘要，先确认写入值与真实摘要不同，再跑同一下载入口。
jq '.assets[0].sha256 = "0000000000000000000000000000000000000000000000000000000000000000"' "$work/pin.json" > "$work/scripts/compat-agent.json"
[ "$(jq -r '.assets[0].sha256' "$work/scripts/compat-agent.json")" != "$digest" ] || { echo "FAIL: checksum fault was not injected" >&2; exit 1; }
rejects "checksum mismatch" "SHA256 mismatch"
[ "$(awk 'END { print NR }' "$work/curl-calls")" = 1 ] || { echo "FAIL: checksum failure did not stop after the first asset" >&2; exit 1; }
rm "$work/curl-calls"

cp "$work/pin.json" "$work/scripts/compat-agent.json"
export FAIL_DOWNLOAD=1
rc=0
"$work/scripts/compat-download.sh" "$work/output" > "$work/result" 2>&1 || rc=$?
[ "$rc" = 22 ] && [ ! -e "$work/output" ] || { echo "FAIL: failed release download was skipped or left artifacts (exit $rc)" >&2; cat "$work/result" >&2; exit 1; }
unset FAIL_DOWNLOAD
echo "rejected unavailable release: exit $rc"

# 恢复摘要与下载后仍须通过，避免负例破坏替身而产生虚假的验红。
"$work/scripts/compat-download.sh" "$work/output" > "$work/result" 2>&1
for arch in amd64 arm64; do
  [ "$("$work/output/heron-agent-linux-$arch")" = "published fixture" ] || { echo "FAIL: restored $arch baseline failed" >&2; exit 1; }
done
echo "compatibility download checks OK"
