#!/bin/sh
# hub 镜像冒烟（spec §12、§14）：以默认参数起容器，/admin/ 返回嵌入的面板，docker exec 经 stdin
# 设密码后能登录，docker stop 后以 0 退出。另外钉住部署时最容易踩到的几条：运行用户与卷属主、
# passwd 没收到输入时的报错、库目录不可写时的退出、非 loopback 监听与时区回退的告警、README 里
# 不依赖镜像内 shell 的排查办法。只创建与删除带本次运行前缀的容器和卷；工件目录保留，路径在开头打印。
set -eu
: "${IMAGE:?IMAGE is required, e.g. ghcr.io/xjetry/probe-hub:v0.1.0}"
: "${VERSION:?VERSION is required: the image must report exactly this version}"
# 空表示 docker 的默认平台；发布后回读时逐个架构给出。
platform=${SMOKE_PLATFORM:-}
# 带 shell 的工具镜像：读卷里的属主、预置不可写的卷、在 hub 的网络命名空间里发请求。不进入产物。
# 按 digest 固定，由 Makefile 的 ALPINE_IMAGE 经环境变量传入，与 Dockerfile 的基础镜像同一处定义。
tool=${TOOL_IMAGE:?TOOL_IMAGE is required: the pinned alpine image, see ALPINE_IMAGE in the Makefile}
# 工具容器在 docker 守护进程的本机平台上运行，按该平台清单的 digest 引用（原因见 image-platform-ref.sh）。
tool_platform=$(docker version --format '{{.Server.Os}}/{{.Server.Arch}}')
tool=$("$(dirname "$0")/image-platform-ref.sh" "$tool" "$tool_platform")
work=$(mktemp -d)
echo "docker smoke artifacts: $work (image $IMAGE${platform:+, platform $platform})"
run_id=probe-smoke-$(basename "$work")
hub=$run_id-hub
denied=$run_id-denied
data=$run_id-data
denied_data=$run_id-denied-data

cleanup() {
  docker rm -f "$hub" "$denied" > /dev/null 2>&1 || true
  docker volume rm "$data" "$denied_data" > /dev/null 2>&1 || true
}
trap cleanup EXIT
# dash 与 busybox ash 被信号终止时不执行 EXIT trap；转成 exit，清理照常发生。
trap 'exit 1' INT TERM HUP

fail() {
  echo "FAIL: $*" >&2
  exit 1
}

# docker run 带上指定的平台；未指定时不加参数，由 docker 取默认平台。
drun() {
  if [ -n "$platform" ]; then
    docker run --platform "$platform" "$@"
  else
    docker run "$@"
  fi
}

got=$(drun --rm "$IMAGE" version) || fail "probe-hub version exited $?"
[ "$got" = "$VERSION" ] || fail "image reports version '$got', want '$VERSION'"
echo "version ok: $got"

# 三种失败分开报：容器已退出（没起来）、到上限仍无 HTTP 应答（起了但没在监听）、有应答但不是面板。
hub_running() {
  state=$(docker inspect -f '{{.State.Status}} {{.State.ExitCode}}' "$hub")
  case $state in
    running*) ;;
    *)
      docker logs "$hub" >&2
      fail "hub container is not running ($state)"
      ;;
  esac
}

docker volume create "$data" > /dev/null
drun -d --name "$hub" -p 127.0.0.1::8080 -v "$data:/data" "$IMAGE" > /dev/null
# 容器已经退出时 docker port 同样失败；先按没起来报并附上 hub 的日志，端口确实没发布才报端口。
hostport=$(docker port "$hub" 8080/tcp) || {
  hub_running
  fail "port 8080 of $hub is not published"
}
base="http://127.0.0.1:${hostport##*:}"

# 上限 30 秒：发布后回读在 QEMU 模拟的 arm64 上跑同一段，启动比本机慢。
deadline=$(($(date +%s) + 30))
while :; do
  hub_running
  rc=0
  status=$(curl -sS --max-time 2 -o "$work/admin.html" -w '%{http_code}' "$base/admin/" 2> "$work/admin.curl") || rc=$?
  [ "$rc" = 0 ] && break
  if [ "$(date +%s)" -ge "$deadline" ]; then
    docker logs "$hub" >&2
    cat "$work/admin.curl" >&2
    fail "no HTTP answer on $base/admin/ within 30s (curl exit $rc)"
  fi
  sleep 0.5
done
# 503 是 internal/hub/web 的"面板没有构建进二进制"说明页：镜像里的 hub 缺了 make web 的产物。
[ "$status" = 200 ] || {
  cat "$work/admin.html" >&2
  fail "/admin/ returned $status, want 200"
}
grep -q 'id="root"' "$work/admin.html" || fail "/admin/ is not the panel index"
echo "admin ok: $base/admin/"

docker logs "$hub" > "$work/hub.log" 2>&1
# 容器里监听 0.0.0.0 是预期的，告警照旧（§5.4、§14）：能直连这个端口的人都能绕过反代并自带转发头。
grep -q 'listening on a non-loopback address' "$work/hub.log" || {
  cat "$work/hub.log" >&2
  fail "startup log lacks the non-loopback listen warning"
}
# 镜像里没有 /etc/localtime、默认不设 TZ：退回 UTC 并告警（§5.4），README 据此要求显式给时区。
grep -q 'host time zone could not be resolved; using UTC' "$work/hub.log" || {
  cat "$work/hub.log" >&2
  fail "startup log lacks the UTC fallback warning"
}
echo "startup warnings ok"

# README 与 §14 的命令按名字执行 probe-hub：二进制必须在容器的默认 PATH 里。
# 两个流都落到同一个文件：exec 本身失败时（如 executable file not found in $PATH），docker CLI 把
# OCI 运行时的报错写在 stdout 上，只收 stderr 会让诊断丢失。
docker exec "$hub" probe-hub version > "$work/exec.log" 2>&1 || {
  cat "$work/exec.log" >&2
  fail "docker exec $hub probe-hub version failed: probe-hub is not runnable by name"
}
got=$(cat "$work/exec.log")
[ "$got" = "$VERSION" ] || fail "docker exec probe-hub version printed '$got', want '$VERSION'"

# 不带 -i 时容器里的 stdin 是 /dev/null：passwd 必须报没有收到密码，且不改管理员表。
rc=0
docker exec "$hub" probe-hub passwd --db /data/probe.db > "$work/passwd-noinput.log" 2>&1 || rc=$?
[ "$rc" != 0 ] || fail "passwd without -i succeeded"
grep -q 'no password on stdin' "$work/passwd-noinput.log" || {
  cat "$work/passwd-noinput.log" >&2
  fail "passwd without -i did not report the missing input"
}
docker exec "$hub" probe-hub stats --db /data/probe.db > "$work/stats.log" 2>&1 || {
  cat "$work/stats.log" >&2
  fail "probe-hub stats"
}
grep -qx 'admin: 0' "$work/stats.log" || {
  cat "$work/stats.log" >&2
  fail "passwd without input changed the admin table"
}
echo "passwd without -i ok"

pw='smoke admin password 2026'
printf '%s\n' "$pw" | docker exec -i "$hub" probe-hub passwd --db /data/probe.db > "$work/passwd.log" 2>&1 || {
  cat "$work/passwd.log" >&2
  fail "passwd via docker exec -i"
}
login() {
  curl -sS -o "$work/login.json" -D "$work/login.headers" -w '%{http_code}' -H 'Content-Type: application/json' \
    --data "{\"password\":\"$1\"}" "$base/probe.v1.AdminService/Login"
}
[ "$(login 'not the admin password')" = 401 ] || {
  cat "$work/login.json" >&2
  fail "a wrong password was not rejected with 401"
}
[ "$(login "$pw")" = 200 ] || {
  cat "$work/login.json" >&2
  fail "login with the password set through docker exec"
}
grep -qi '^set-cookie: probe_session=' "$work/login.headers" || fail "login did not set the session cookie"
echo "passwd and login ok"

# hub 以 uid 65532 运行：它在新建的命名卷里写出的库文件属于 65532（Docker 把镜像里 /data 的属主带到空卷上）。
docker run --rm --platform "$tool_platform" -v "$data:/data" "$tool" stat -c '%u:%g %n' /data /data/probe.db > "$work/owner.log" 2> "$work/owner.err" || {
  cat "$work/owner.log" "$work/owner.err" >&2
  fail "stat the data volume"
}
awk '$1 != "65532:65532" { bad = 1 } END { exit bad || NR != 2 }' "$work/owner.log" || {
  cat "$work/owner.log" >&2
  fail "data volume entries are not owned by 65532:65532"
}
# README 的排查办法：镜像里没有 shell，用工具镜像进入 hub 的网络命名空间发请求。
docker run --rm --platform "$tool_platform" --network "container:$hub" "$tool" wget -q -O /dev/null http://127.0.0.1:8080/admin/ > "$work/netns.log" 2>&1 || {
  cat "$work/netns.log" >&2
  fail "request from the hub's network namespace"
}
echo "volume owner and debugging recipes ok"

# docker stop 发 SIGTERM：hub 的关停路径排空请求、停下后台循环并关库后以 0 退出。非 0 说明
# SIGTERM 没有走到这条路径，退出前的落盘无从保证。
docker stop "$hub" > /dev/null
code=$(docker inspect -f '{{.State.ExitCode}}' "$hub")
[ "$code" = 0 ] || {
  docker logs "$hub" >&2
  fail "hub exited $code on docker stop, want 0"
}
echo "stop ok"

# 库目录不可写（绑定了属主为 root 的宿主目录）：卷里已有内容时 Docker 不改它的属主，
# 用预置了 root 文件的卷模拟。hub 必须退出，报错里写出库路径。
docker volume create "$denied_data" > /dev/null
docker run --rm --platform "$tool_platform" -v "$denied_data:/data" "$tool" sh -c 'touch /data/placeholder && chown 0:0 /data /data/placeholder && chmod 755 /data' || fail "prepare the root-owned volume"
drun -d --name "$denied" -v "$denied_data:/data" "$IMAGE" > /dev/null
deadline=$(($(date +%s) + 30))
until [ "$(docker inspect -f '{{.State.Status}}' "$denied")" = exited ]; do
  [ "$(date +%s)" -lt "$deadline" ] || {
    docker logs "$denied" >&2
    fail "hub kept running on an unwritable /data"
  }
  sleep 0.5
done
code=$(docker inspect -f '{{.State.ExitCode}}' "$denied")
docker logs "$denied" > "$work/denied.log" 2>&1
[ "$code" = 1 ] || {
  cat "$work/denied.log" >&2
  fail "hub on an unwritable /data exited $code, want 1"
}
grep -q 'open database /data/probe.db' "$work/denied.log" || {
  cat "$work/denied.log" >&2
  fail "error on an unwritable /data does not name the database path"
}
echo "unwritable data dir ok"
echo "docker smoke passed: $IMAGE${platform:+ ($platform)}"
