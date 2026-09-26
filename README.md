# probe

自托管服务器监控探针：agent 采集主机指标并上报，hub 存储、展示并对外提供查询。

## 用 Docker 运行 hub

镜像 `ghcr.io/xjetry/probe-hub:<版本>`，含 linux/amd64 与 linux/arm64。正式版本在发布回读通过后同时成为 `latest`；预发布版本（tag 含 `-`，如 `v0.2.0-rc.1`）不动 `latest`。推 `v*` tag 触发发布，同组的发布运行串行：同时推多个 tag 时，排队中被后来者替换而取消的那个需要手工重跑。镜像基于 `scratch`，只有静态链接的 `probe-hub`、CA 证书与 uid 65532 的非 root 用户，没有 shell。

```sh
docker volume create probe-data
docker run -d --name probe --restart unless-stopped --stop-timeout 30 \
  -p 127.0.0.1:8080:8080 \
  -v probe-data:/data \
  -e TZ=Asia/Shanghai \
  ghcr.io/xjetry/probe-hub:v0.1.0
```

`--stop-timeout 30` 给关停留出余量：`docker stop` 先发 SIGTERM，hub 排空在途请求（至多 10 秒）、停下后台循环、关库后退出；宽限期一过 Docker 就发 SIGKILL。默认宽限期也是 10 秒，与排空上限相等，排空用满时最后一批写可能被截断。

默认参数是 `serve --db /data/probe.db --listen 0.0.0.0:8080`。镜像名之后写的任何参数都会替换这一整组默认参数，要加参数时连同默认的一起写全：

```sh
docker run -d --name probe --restart unless-stopped --stop-timeout 30 -p 127.0.0.1:8080:8080 -v probe-data:/data \
  ghcr.io/xjetry/probe-hub:v0.1.0 \
  serve --db /data/probe.db --listen 0.0.0.0:8080 --timezone Asia/Shanghai
```

### 设置管理员密码

hub 没有经网络的首次设置页，密码在容器里设置；hub 运行中设置即生效，不需要重启：

```sh
docker exec -it probe probe-hub passwd --db /data/probe.db
```

从脚本设置时经 stdin 传一行，必须带 `-i`。不带 `-i` 时容器里的 stdin 是空的，`passwd` 报 `no password on stdin` 并且不做任何修改：

```sh
printf '%s\n' "$ADMIN_PASSWORD" | docker exec -i probe probe-hub passwd --db /data/probe.db
```

### 反代与 `--trusted-proxies`

hub 只提供明文 HTTP，TLS 由反代（Caddy、nginx、CDN）终止。容器里监听 `0.0.0.0` 是预期的，启动日志里的 `listening on a non-loopback address` 告警照旧出现：能直连这个端口的人可以绕过反代并自带转发头。因此：

- 端口只发布到本机回环（`-p 127.0.0.1:8080:8080`，反代在宿主上），或者不发布端口，让反代容器与 hub 在同一个 Docker 网络里转发到 `http://probe:8080`。
- `--trusted-proxies` 写 hub 看到的反代地址（TCP 对端；CIDR 列表，逗号分隔）。只有来自这些地址的 `X-Forwarded-For`、`X-Forwarded-Proto` 才被采信。不设置时一律不信：公开页与注册的限流按来源地址计，反代后不配它，所有访客共用代理地址的一个桶；登录失败锁定同样按代理地址计，会话 cookie 也不带 `Secure`。量级：公开服务每个来源的桶容量 60、每秒补充 10；每个打开的总览页每 2 秒轮询一次快照（每秒 0.5 次），节点页另有每分钟两次历史查询，页面加载时还有几次请求。同时打开的总览页超过 20 个、或节点页约 19 个起，消耗就持续多于补充，60 次的余量用完后访客开始收到 429（30 个页面时约 10–12 秒后）。

反代与 hub 在同一个网络时，给网络固定网段、只让这两个容器加入，并信任这个网段：

```sh
docker network create --subnet 172.30.0.0/24 probe-net
docker run -d --name probe --restart unless-stopped --stop-timeout 30 --network probe-net -v probe-data:/data -e TZ=Asia/Shanghai \
  ghcr.io/xjetry/probe-hub:v0.1.0 \
  serve --db /data/probe.db --listen 0.0.0.0:8080 --trusted-proxies 172.30.0.0/24
# 反代容器以 --network probe-net 加入，把请求转给 http://probe:8080
```

### 时区

镜像里没有 `/etc/localtime`（时区数据已嵌入二进制）。不指定时区时，hub 按 UTC 判定流量周期的重置日，并在启动日志里告警 `host time zone could not be resolved; using UTC`。用 `-e TZ=Asia/Shanghai` 指定（不替换默认参数），或在写全的参数里加 `--timezone Asia/Shanghai`；两者都给时以 `--timezone` 为准。

### 环境变量

| 变量 | 作用 |
|---|---|
| `TZ` | IANA 时区名；没有给 `--timezone` 时使用 |
| `PROBE_OFFLINE_AFTER` | 节点离线判定时长（Go duration，`10s` 到 `3m`，默认 `30s`）；上报间隔、退避上限与告警宽限期的下限都由它推出 |

### 数据卷与备份

- `/data` 由 uid 65532 写入。新建的命名卷或匿名卷挂上时，Docker 把镜像里 `/data` 的属主带过去，无需处理。绑定宿主目录（`-v /srv/probe:/data`）时，目录须可被 uid 65532 写入（Linux 宿主上 `chown 65532:65532 /srv/probe`），否则 hub 启动即退出，报错 `open database /data/probe.db: …`。
- 库由 `probe.db` 与同名的 `probe.db-wal`、`probe.db-shm` 三个文件组成，已提交但尚未写回 `probe.db` 的数据只在 `-wal` 里：运行中只复制 `probe.db` 会丢数据。备份时先停容器，再把三个文件（或整个卷）一起复制：

```sh
docker stop probe
docker run --rm -v probe-data:/data -v "$PWD":/backup alpine:3.21 tar -C /data -czf /backup/probe-data.tgz .
docker start probe
```

### 排查

镜像里没有 shell，`docker exec probe sh` 不可用。可以：

- 看日志：`docker logs probe`。
- 查版本与各表行数：`docker exec probe probe-hub version`、`docker exec probe probe-hub stats --db /data/probe.db`。
- 看卷里的文件：挂同一个卷起一个带 shell 的临时容器，`docker run --rm -v probe-data:/data alpine:3.21 ls -ln /data`。
- 从 hub 自己的网络里发请求：`docker run --rm --network container:probe alpine:3.21 wget -qO /dev/null http://127.0.0.1:8080/admin/ && echo ok`。
