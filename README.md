# probe

自托管服务器监控探针：agent 采集主机指标并上报，hub 存储、展示并对外提供查询。设计见 [架构设计](docs/superpowers/specs/2026-09-17-probe-architecture-design.md)。

## 运行 hub

升级前备份库；新版本 `serve` 会迁移库，迁移后旧版本无法再打开。离线子命令遇到旧库会拒绝操作，并提示先用新版本 `serve` 升级。备份方法见下文"数据卷与备份"：运行中直接复制可能得到损坏的备份，需要先停 hub，再连同 `-wal`、`-shm` 一起复制。

hub 是一个静态链接的二进制，数据在一个 SQLite 文件里。从 [Releases](https://github.com/xjetry/probe/releases/latest) 下载 `probe-hub_linux_<arch>.tar.gz`（amd64、arm64）：

```sh
tar -xzf probe-hub_linux_amd64.tar.gz
./probe-hub passwd --db /var/lib/probe/probe.db   # 设置管理员密码
./probe-hub serve --db /var/lib/probe/probe.db    # 默认监听 127.0.0.1:8080
```

- hub 只监听明文 HTTP，TLS 由反代（Caddy、nginx、CDN）终止；反代地址用 `--trusted-proxies` 声明，否则不信任转发头。
- 管理面板在 `/admin/`。离线判定的时限由环境变量 `PROBE_OFFLINE_AFTER` 设定（默认 30s，10s–180s）。
- 其余参数见 `probe-hub serve -h`；节点、注册窗口与 API token 也可在 hub 主机上用 `probe-hub node|window|token` 管理。
- 节点可在面板里记录价格、币种、计费周期与到期日：只用于展示与提醒，hub 不汇总、不换算。公开节点填了的这几项也显示在公开页，自动续期开关除外。建「到期」类型的告警规则可在到期前若干天提醒；开着自动续期的节点过了到期日，hub 按周期把到期日推后。到期日按天计，天的边界与流量周期一样取 `--timezone`。

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
- `--trusted-proxies` 写 hub 看到的反代地址（TCP 对端；CIDR 列表，逗号分隔）。只有来自这些地址的 `X-Forwarded-For`、`X-Forwarded-Proto` 才被采信。不设置时一律不信：公开页与注册的限流按来源地址计，反代后不配它，所有访客共用代理地址的一个桶；登录失败锁定同样按代理地址计，会话 cookie 也不带 `Secure`；节点的来源地址（面板节点详情的主机名一格）也记成反代地址。量级：公开服务每个来源的桶容量 60、每秒补充 10；每个打开的总览页每 2 秒轮询一次快照（每秒 0.5 次），节点页另有每分钟两次历史查询，页面加载时还有几次请求。同时打开的总览页超过 20 个、或节点页约 19 个起，消耗就持续多于补充，60 次的余量用完后访客开始收到 429（30 个页面时约 10–12 秒后）。

反代与 hub 在同一个网络时，给网络固定网段、只让这两个容器加入，并信任这个网段：

```sh
docker network create --subnet 172.30.0.0/24 probe-net
docker run -d --name probe --restart unless-stopped --stop-timeout 30 --network probe-net -v probe-data:/data -e TZ=Asia/Shanghai \
  ghcr.io/xjetry/probe-hub:v0.1.0 \
  serve --db /data/probe.db --listen 0.0.0.0:8080 --trusted-proxies 172.30.0.0/24
# 反代容器以 --network probe-net 加入，把请求转给 http://probe:8080
```

### 时区

镜像里没有 `/etc/localtime`（时区数据已嵌入二进制）。不指定时区时，hub 按 UTC 判定流量周期的重置日与节点到期日的天边界，并在启动日志里告警 `host time zone could not be resolved; using UTC`。用 `-e TZ=Asia/Shanghai` 指定（不替换默认参数），或在写全的参数里加 `--timezone Asia/Shanghai`；两者都给时以 `--timezone` 为准。

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
- 查版本、各表行数与存储健康（各级最老桶、上卷水位、上次清理与上卷的完成时刻；标红判定见面板的存储页或 `GetStorageStats`）：`docker exec probe probe-hub version`、`docker exec probe probe-hub stats --db /data/probe.db`。
- 看卷里的文件：挂同一个卷起一个带 shell 的临时容器，`docker run --rm -v probe-data:/data alpine:3.21 ls -ln /data`。
- 从 hub 自己的网络里发请求：`docker run --rm --network container:probe alpine:3.21 wget -qO /dev/null http://127.0.0.1:8080/admin/ && echo ok`。

### 备份设置损坏的恢复

`setting` 中已有的备份周期或保留份数越界时，`GetSettings` 与未修复全部坏值的 `UpdateSettings` 返回 `Internal`，不会把坏值当作默认值，也不会部分保存外观或公开页总闸。hub 日志中的 `invalid stored backup.<字段>` 指出损坏的键。

使用管理员登录会话调用 `POST /probe.v1.AdminService/UpdateSettings`（`Content-Type: application/json`），在 `settings.backup` 中**同时提供四个合法数值**即可覆盖坏值并恢复读取；不需要直接改库。默认值为：

```json
{"configIntervalS":300,"metricsIntervalS":86400,"configKeep":48,"metricsKeep":14}
```

这是 `backup` 的数值片段，不是完整请求：请求仍须带齐要保留的五项外观（`title`、`theme`、`accentColor`、`logo`、`customCss`，其中 `theme` 为 `auto`、`light` 或 `dark`）与备份目标（`endpoint`、`bucket`、`region`、`accessKey`、`prefix`），因为这些字段是整体替换语义。若选择关闭备份，可显式把目标字段清空、区域设为 `auto`，但四个数值仍需合法。`secret`、`notify` 和 `publicEnabled` 缺席表示不变，不要把未知旧值猜成空值或开关值。外观读取失败时应从运维留存配置取值，或明确选择恢复成内置外观。

## 安装 agent

先在面板的「注册窗口」开一个窗口拿到 key（或在 hub 主机上 `probe-hub window open`）。重跑安装命令即升级：已有配置时沿用现有注册，不会在 hub 上多出节点。

### Linux

用面板注册窗口页给出的命令，形如：

```sh
curl -fsSL https://github.com/xjetry/probe/releases/latest/download/install.sh | sh -s -- --hub https://probe.example.com --key <key>
```

以 root 运行，支持 systemd 与 OpenRC。卸载：同一条命令把参数换成 `--uninstall`，加 `--purge` 一并删除配置、日志与 `probe-agent` 用户。

### macOS

面板不给 macOS 的命令，用这一条（Apple Silicon 与 Intel 通用，脚本按硬件选包）：

```sh
curl -fsSL https://github.com/xjetry/probe/releases/latest/download/install-macos.sh | sudo sh -s -- --hub https://probe.example.com --key <key>
```

- agent 装成 LaunchDaemon `xyz.probe.agent`，以隐藏的系统用户 `_probe-agent` 运行；二进制在 `/usr/local/bin/probe-agent`，配置在 `/etc/probe-agent/`，日志在 `/Library/Logs/probe-agent/`。
- 参数与 Linux 脚本相同：`--name`、`--version vX.Y.Z`（钉住版本）、`--base-url`（下载目录，用于镜像或本地构建）。
- 卸载：`… | sudo sh -s -- --uninstall`；加 `--purge` 一并删除配置、日志、用户与同名组。
- 默认不计入流量的网卡（回环、隧道与 VPN、桥与虚拟机网卡等）见 `probe-agent run -h`。

#### 真机核对

没有 macOS 虚拟机可做自动验收，改动 `deploy/install-macos.sh` 或 `deploy/launchd/xyz.probe.agent.plist` 后、发版前在一台 Mac 上逐条执行。未发布的构建用 `make release VERSION=v0.0.0-check` 产出 `dist/`，`python3 -m http.server 18089 --directory dist` 提供下载，下面的 `<base>` 即该地址，安装时加 `--base-url <base>`。端口避开仓库里各验收脚本占用的号：e2e 用 18080/18081，Linux 安装验收用 18085/18086（下载服务就在 18086），macOS 本机验收用 18087/18088；同一台机器上同时跑时，后起的一方会绑不上端口。

1. `curl -fsSL <base>/install-macos.sh | sudo sh -s -- --hub <hub> --key <key> --base-url <base>`：最后一行是 `probe-agent installed and started (launchd, arm64, probe-agent_darwin_arm64.tar.gz)`（Intel 为 amd64）。
2. `dscl . -read /Users/_probe-agent UniqueID PrimaryGroupID UserShell NFSHomeDirectory`：UniqueID 是 300–499 中从 499 往下第一个两个命名空间都空闲的号（通常就是 499），PrimaryGroupID 等于 `dscl . -read /Groups/_probe-agent PrimaryGroupID` 的值、与 UniqueID 相同，UserShell 为 `/usr/bin/false`，NFSHomeDirectory 为 `/var/empty`；`id -gn _probe-agent` 输出 `_probe-agent`；登录窗口里看不到这个用户。
3. `ps -axo user,pid,command | grep '[p]robe-agent run'`：USER 为 `_probe-agent`。`sudo launchctl print system/xyz.probe.agent | grep -E 'state =|pid ='`：`state = running`。
4. `sudo ls -ld /etc/probe-agent /etc/probe-agent/config.json /Library/LaunchDaemons/xyz.probe.agent.plist /Library/Logs/probe-agent /Library/Logs/probe-agent/*`：目录 `drwxr-x--- root _probe-agent`；配置 `-rw------- _probe-agent _probe-agent`；plist `-rw-r--r-- root wheel`；日志目录 `drwxr-xr-x root wheel`；两个日志文件 `-rw-r----- _probe-agent _probe-agent`（安装脚本每次安装都建好）。
5. 面板：节点在线；详情页系统为 `macOS <sw_vers -productVersion>`、架构为 `arm64`（Intel 为 `amd64`）、CPU 型号等于 `sysctl -n machdep.cpu.brand_string`、ICMP 可用；实时视图每项指标都有值；内存已用与活动监视器的"已使用内存"接近。
6. 给节点建一个 ICMP 任务，目标取网关（`route -n get default | awk '/gateway/ {print $2}'`）或 1.1.1.1：一分钟后有 RTT，与 `ping -c 10 <目标>` 的平均值同一量级。
7. hub 在局域网地址（如 192.168.x.x）上时节点同样上线，ICMP 到局域网主机有结果（macOS 15 起的本地网络隐私不应拦截这个 LaunchDaemon）。
8. 带同一个 `--key` 重跑第 1 条：输出含 `existing config found; keeping the current registration (--key ignored)`，面板节点数不变；换一个版本重跑：面板上的 agent 版本随之改变。
9. hub 用 https 地址安装，节点上线后列出服务 uid 的全部进程：`ps -axo uid=,pid=,comm= | awk -v u=$(id -u _probe-agent) '$1 == u'`。agent 那一行的 comm 是 `/usr/local/bin/probe-agent`（安装脚本按 uid 加这个路径认进程）；若还有 cfprefsd、trustd 之类的辅助进程，把输出记下来，再重跑第 1 条与第 15 条的卸载各一次，都应成功而不报 `still running`。
10. `sudo launchctl disable system/xyz.probe.agent` 后重跑第 1 条：成功，服务在跑。
11. `sudo kill -9 $(pgrep -u _probe-agent probe-agent)`：约 5 秒内出现新的 pid，节点保持或恢复在线。
12. 日志文件被删后的行为：`sudo rm /Library/Logs/probe-agent/probe-agent.log && sudo launchctl kickstart -k system/xyz.probe.agent`，等 10 秒后 `sudo launchctl print system/xyz.probe.agent | grep -E 'state =|last exit code'` 并 `ls -l /Library/Logs/probe-agent`。会看到两种结果之一：文件重建、`state = running`（root 属主的目录里建得出文件，说明 launchd 以 root 打开；按 launchd.plist(5) 新建文件的属主取 UserName，所以不看属主；预建文件多余但无害），或作业以 `last exit code = 78: EX_CONFIG` 反复退出（以服务用户打开；重跑安装脚本把文件建回来即恢复）。记录看到的是哪一种；两种都不是缺陷。
13. 记下 `sysctl -n kern.bootsessionuuid`，睡眠至少 2 分钟（`pmset sleepnow` 或合盖）后唤醒：值不变；节点在离线时限内回到在线；流量图在唤醒处没有尖峰。
14. 重启这台 Mac：服务开机自启、节点在线；`kern.bootsessionuuid` 换了新值；总流量没有一次性跳涨。
15. `curl -fsSL <base>/install-macos.sh | sudo sh -s -- --uninstall`：输出 `probe-agent uninstalled`；`sudo launchctl print system/xyz.probe.agent` 退出码 113；`/usr/local/bin/probe-agent` 与 plist 不在，配置与用户仍在。再带 `--uninstall --purge` 执行：`/etc/probe-agent`、`/Library/Logs/probe-agent` 不在，`id _probe-agent` 报 no such user，`dscl . -read /Groups/_probe-agent` 报 `eDSRecordNotFound`。
16. 有 Intel Mac 时在其上重复 1–5，装的是 amd64 包。

## 许可

MIT，见 [LICENSE](LICENSE)。
