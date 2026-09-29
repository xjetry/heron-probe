# probe

自托管服务器监控探针：agent 采集主机指标并上报，hub 存储、展示并对外提供查询。设计见 [架构设计](docs/superpowers/specs/2026-09-17-probe-architecture-design.md)。

## 运行 hub

升级前备份库；新版本 `serve` 会迁移库，迁移后旧版本无法再打开。离线子命令遇到旧库会拒绝操作，并提示先用新版本 `serve` 升级。可使用内置 S3 分层快照在线备份，或停 hub 后复制整个数据卷，详见下文“数据卷与备份”。

hub 是一个静态链接的二进制，数据在一个 SQLite 文件里。从 [Releases](https://github.com/xjetry/probe/releases/latest) 下载 `probe-hub_linux_<arch>.tar.gz`（amd64、arm64）：

```sh
tar -xzf probe-hub_linux_amd64.tar.gz
./probe-hub serve --db /var/lib/probe/probe.db    # 默认监听 127.0.0.1:8080
# 等 serve 启动完成后，在另一个终端设置管理员密码
./probe-hub passwd --db /var/lib/probe/probe.db
```

- hub 只监听明文 HTTP，TLS 由反代（Caddy、nginx、CDN）终止；反代地址用 `--trusted-proxies` 声明，否则不信任转发头。
- 管理面板在 `/admin/`。离线判定的时限由环境变量 `PROBE_OFFLINE_AFTER` 设定（默认 30s，10s–180s）。
- 其余参数见 `probe-hub serve -h`；节点、注册窗口与 API token 也可在 hub 主机上用 `probe-hub node|window|token` 管理。
- 节点可在面板里记录价格、币种、计费周期与到期日：只用于展示与提醒，hub 不汇总、不换算。公开节点填了的这几项也显示在公开页，自动续期开关除外。建「到期」类型的告警规则可在到期前若干天提醒；开着自动续期的节点过了到期日，hub 按周期把到期日推后。到期日按天计，天的边界与流量周期一样取 `--timezone`。
- 节点可挂多个标签，面板按标签过滤。公开节点的标签也显示在公开页，访客可以按标签筛选；标签常写用途与归属，挂到公开节点前先确认可以对外公开，没有单独隐藏标签的开关。

## 安装 hub（Linux，systemd）

支持 amd64、arm64，以 root 执行：

```sh
curl -fsSL https://github.com/xjetry/probe/releases/latest/download/install-hub.sh | sh
probe-hub passwd --db /var/lib/probe/probe.db
```

安装器只按脚本里内嵌的本版 SHA-256 校验下载包（发布时写进脚本；release 里的 `SHA256SUMS` 供人工核对，不是脚本的校验依据），以静态系统用户 `probe-hub` 启动服务，确认进程持续存活后才提示设置密码。主机没有 CA 证书包时安装器会装上 `ca-certificates`：不论从哪里下载，hub 发往 Telegram 的告警都走 HTTPS。升级时先让新版本的 `serve` 启动并完成数据库迁移，再使用 `passwd` 等离线子命令。管理员密码由你设置，脚本不生成、不打印密码。

默认只监听 `127.0.0.1:8080`，TLS 交给反向代理。可用 `--listen`、`--timezone`、`--trusted-proxies`、`--public-dir`、`--theme-origin`、`--admin-origin`、`--geo-mmdb` 和 `--retention-*` 设置 serve 参数；例如：

```sh
curl -fsSL https://github.com/xjetry/probe/releases/latest/download/install-hub.sh | sh -s -- \
  --timezone Asia/Taipei --trusted-proxies 127.0.0.1/32
```

重跑即升级，沿用 `/etc/systemd/system/probe-hub.service` 里 `ExecStart` 的参数，命令行显式给出的值按参数名替换旧值；写回时每个参数只留一份，统一写成 `--flag=value`。脚本只装自己所属的版本，没有 `--version`：`releases/latest/download/install-hub.sh` 装最新正式版，要装或升级到指定版本就取该版本的脚本，`https://github.com/xjetry/probe/releases/download/vX.Y.Z/install-hub.sh`。`--base-url URL` 只改从哪个目录下载，接受哪些字节仍由内嵌的 SHA-256 决定。安装器只接受静态参数：用了 systemd 的 `$` / `%` 动态展开，或有 drop-in 设了 `ExecStart` 时，须先把参数合并为主单元里的静态值；无法解析时升级在停服前报错，不会重置配置。首装时已有设了 `ExecStart` 的 drop-in（例如预先写入 `probe-hub.service.d/` 的），安装器写好主单元后报错，不 enable、不 start。数据库固定为 `/var/lib/probe/probe.db`。

每次安装都用发行包里的单元覆盖主单元，只保留其中 `ExecStart` 的参数：主单元里别的手工改动（例如 `Environment=PROBE_OFFLINE_AFTER=60s`）会在升级时丢失。这类定制放进 drop-in（`systemctl edit probe-hub`，写在 `/etc/systemd/system/probe-hub.service.d/`）；不设 `ExecStart` 的 drop-in 升级时保留。普通卸载保留 drop-in，`--purge` 删除 `/etc/systemd/system/probe-hub.service.d/` 与 `/run/systemd/system/probe-hub.service.d/`，包括手工定制；目录为符号链接时只删除链接，不删除目标内容。共享 drop-in、发行版提供的配置和系统 journal 不清理，journal 由系统日志保留策略处理。

数据目录为 `root:probe-hub 0770`，库文件为 `probe-hub:probe-hub 0600`；目录必须允许服务组创建和删除 SQLite 的 WAL/SHM 文件。数据目录或库文件是符号链接、库文件另有硬链接时，安装器在停服前拒绝，不改动链接指向的文件。单元逐项加固，将数据目录列入 `ReadWritePaths`，提供私有临时目录，不授予 `CAP_NET_RAW`。查看状态与日志：`systemctl status probe-hub`、`journalctl -u probe-hub`。

普通卸载停掉的是全部节点的展示与告警，`--purge` 删除唯一一份数据与全部节点凭据，所以两者都需确认；无终端时必须显式 `--yes`，不会读取管道里的脚本内容：

```sh
curl -fsSL https://github.com/xjetry/probe/releases/latest/download/install-hub.sh | sh -s -- --uninstall --yes
# 同时删除 /var/lib/probe、服务用户与组，不可恢复
curl -fsSL https://github.com/xjetry/probe/releases/latest/download/install-hub.sh | sh -s -- --uninstall --purge --yes
```

普通卸载保留数据和账户。安装、升级都会先核对目标端口的监听进程，排除现有 hub 自身；冲突时不停止旧服务。停止失败、旧进程未退出或新进程未持续存活均返回失败。停服之后、`systemctl start` 成功返回之前某一步失败时，安装器会提示 hub 已停，可重跑安装器或手动启动；启动之后没能持续存活时不这样提示，那时 systemd 仍按 `Restart=always` 继续拉起，按 `journalctl -u probe-hub` 排查。例外是写好主单元之后的 drop-in 检查没过：查出设了 `ExecStart` 的 drop-in 时，手动启动会按它的参数起来；`systemctl daemon-reload` 等本身出错时，drop-in 还没查过。这两种情形安装器都只说明单元是否仍 enabled，并按失败点要求先处理报出的 drop-in 或 systemctl 问题再重跑。是否 enabled 看的是 `/etc/systemd/system/multi-user.target.wants/probe-hub.service` 这条 enable 链接，不向 systemctl 查询，systemctl 出错时也答得出；升级时只要没被 disable 过，上次安装建的链接就还在，下次开机也会这样起来。另用 `systemctl add-wants` 等挂到别的 target 下的链接不在判断之内。

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

### 账户安全

在面板「安全」进入认证器管理，可启用 TOTP、生成一次性恢复码、注册或删除 Passkey。启用 TOTP 后，密码登录必须同时提供六位动态验证码或一个恢复码；恢复码只显示一次，使用后失效，重新生成会作废旧码。修改认证方式需要重新证明身份，并撤销全部旧会话。

Passkey 支持无密码登录，需启动参数 `--admin-origin https://panel.example.com` 固定可信管理来源。反代须提供 HTTPS，浏览器须支持 WebAuthn；本地开发仅允许 localhost/回环地址使用 HTTP。该主机名必须与 `--theme-origin` 不同。未配置 `--admin-origin` 时 TOTP 和密码仍可使用，Passkey 关闭。注册的是可发现凭据，登录时要求认证器执行用户验证。

认证器丢失时先用恢复码登录；密码或全部认证器丢失时，可登录 hub 服务器，使用对数据库有读写权限的账号执行以下命令，不需要提供旧密码或认证器证明：

```bash
# 重设密码，终端交互输入且不回显；不会清除 TOTP 或 Passkey
probe-hub passwd --db /var/lib/probe/probe.db
# 清除 TOTP、恢复码和全部 Passkey；不改变密码或 API token
probe-hub security-reset --db /var/lib/probe/probe.db --yes
```

密码和认证器全部丢失时执行两条；两者都撤销全部登录会话，无需重启 hub。`passwd` 会列出现有 API token，并在交互终端询问是否一并撤销；非交互时默认保留。必须指定实际使用的数据库；`security-reset` 拒绝不存在的库，`passwd` 也可用于首次建库。旧库须先由新版本 `serve` 完成迁移。

Docker 部署对应为：

```bash
docker exec -it probe probe-hub passwd --db /data/probe.db
docker exec probe probe-hub security-reset --db /data/probe.db --yes
```

备份包含认证密钥，须按凭据管理，不应公开 bucket 或分享快照。

### 标签与告警

探测任务和告警规则可按标签一次性批量选中节点，保存后是固定节点列表；也可选择动态标签交集，自动覆盖同时拥有全部所选标签的节点。动态选择器与显式节点、全部节点互斥，标签变更会更新探测分配和告警作用域。被动态选择器引用的标签不能删除，须先修改引用它的任务或规则；匹配空集不表示全部节点。

资源规则支持内存与磁盘已用百分比，按同一次采样的已用量／总量计算，连续指定数量的已闭合分钟达到阈值才触发，连续相同数量的完整分钟不高于独立恢复阈值才恢复。缺失读数不算恢复。登录成功、失败、锁定、认证方式变更，以及备份成功、故障、故障恢复、停用和手动恢复都会保留事件；普通登录失败、周期备份成功及手动恢复只记审计，不默认投递通知。

### 公开页主题

公开页除了在面板的「外观」页改标题、配色、logo 与 CSS，还可以换成第三方主题：一个只调 `PublicService` 的静态前端，打成 zip 在面板的「主题」页上传、启用。主题托管在单独的主机名上，hub 以 `--theme-origin https://status.example.com` 启动，并让反代把这个主机名也转给 hub、原样转发 `Host`（nginx 要写 `proxy_set_header Host $host;`）。这个主机名必须与面板的不同，只差端口不算。不给 `--theme-origin` 时主题功能关闭。

主题的开发、包布局、清单字段、上限与本地调试见 [主题开发指南](docs/theme-guide.md)。

### 时区

镜像里没有 `/etc/localtime`（时区数据已嵌入二进制）。不指定时区时，hub 按 UTC 判定流量周期的重置日与节点到期日的天边界，并在启动日志里告警 `host time zone could not be resolved; using UTC`。用 `-e TZ=Asia/Shanghai` 指定（不替换默认参数），或在写全的参数里加 `--timezone Asia/Shanghai`；两者都给时以 `--timezone` 为准。

### 环境变量

| 变量 | 作用 |
|---|---|
| `TZ` | IANA 时区名；没有给 `--timezone` 时使用 |
| `PROBE_OFFLINE_AFTER` | 节点离线判定时长（Go duration，`10s` 到 `3m`，默认 `30s`）；上报间隔、退避上限与告警宽限期的下限都由它推出 |

### 数据卷与备份

面板中配置 S3 兼容 endpoint、bucket、区域、access key、secret 和前缀后，hub 自动生成一致性分层快照：配置层默认每 5 分钟一次、保留 48 份；指标层默认每天一次、保留 14 份。周期与份数可配置，两层状态可从面板读取。周期成功只记事件，配置层故障与恢复按设置的渠道通知。bucket 必须私有，并使用 HTTPS；快照不额外加密。

配置快照带主题摘要清单，先上传 `theme/sha256/<SHA-256>.zip` 不可变主题包，再上传引用它的快照。更新或删除当前主题不会覆盖、删除旧快照需要的包；主题对象不按当前安装清单自动清理，需另行评估保留空间。

可用 `sqlite3 config.db 'SELECT theme_id,sha256 FROM snapshot_theme ORDER BY theme_id;'` 查看所需主题版本，只下载清单引用的包。空摘要表示该主题没有原包，需重新上传后再备份；恢复该快照时可省略 `--themes` 并停用主题。

恢复前停止 hub，将配置快照、可选的指标快照和所引用的主题包下载到本地：

```sh
probe-hub restore --db probe.db --config config.db --metrics metrics.db --themes ./themes --yes
```

`themes` 目录放摘要命名的 `<SHA-256>.zip`。新格式快照指定 `--themes` 时严格校验主题引用，缺包或摘要不符拒绝恢复；省略该参数则恢复配置但停用全部主题。恢复接受明确支持的 schema 17–20，旧快照在私有副本中迁移，不改写来源；未来版本、未知格式或结构不符拒绝。旧格式按原有 `<主题 id>.zip` 导入，缺包主题保留但停用。恢复不复活会话或注册窗口，并写入恢复记录和手动恢复事件。两层时刻可不同，以配置层节点为准清理孤儿历史。

若不使用在线快照，可停机备份整个卷：

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

这是 `backup` 的数值片段，不是完整请求：请求在 `settings.backup` 中仍须带齐要保留的备份目标（`endpoint`、`bucket`、`region`、`accessKey`、`prefix`），因为这些字段在备份组内整体替换。若选择关闭备份，可显式把目标字段清空、区域设为 `auto`，但四个数值仍需合法。外观五项全部省略时不改外观，不需要猜测读取失败前的外观值；若给出任一外观项，则必须带合法 `theme`，并整体替换五项。`secret`、`notify`、`publicEnabled` 与国家查询项缺席表示不变，不要把未知旧值猜成空值或开关值。

## 安装 agent

先在面板的「注册窗口」开一个窗口拿到 key（或在 hub 主机上 `probe-hub window open`）。重跑安装命令即升级：已有配置时沿用现有注册，不会在 hub 上多出节点。

安装命令以本 README 与 GitHub Release 为准，不以 hub 面板为准：面板由 hub 提供，hub 失守时面板上的命令可以被整条换掉，这一点产品内防不住。面板的命令只是为了方便，复制前核对脚本地址是 `https://github.com/xjetry/probe/releases/…`。

两个 agent 脚本与 hub 脚本共同的规则：

- 脚本只装自己所属的版本，没有 `--version`。`releases/latest/download/<脚本>` 装最新正式版；要装或升级到指定版本就取该版本的脚本，`https://github.com/xjetry/probe/releases/download/vX.Y.Z/<脚本>`。
- 脚本里内嵌本版全部 tar 包的 SHA-256，只按它校验下载的包。`--base-url URL` 只改从哪个目录下载（镜像、本地构建），接受哪些字节不变：指向的目录里即使放了与篡改包相符的 `SHA256SUMS` 也装不上。仓库里的源码脚本没有内嵌哈希，只能卸载，安装请用 release 里的脚本。
- `--insecure-http`（只在两个 agent 脚本上，hub 脚本没有 hub 地址）：hub 地址是 `http://` 且主机不是 loopback IP（`127.0.0.0/8`、`[::1]`；`localhost` 不算）时必须给出，agent 否则拒绝注册与启动。它把"接受明文 http"写进 agent 的本地配置，意味着节点 token 与指标明文传输，链路上的中间人与 hub 失守等价；能用 https 就用 https。首次安装时交给 `probe-agent register`，重跑时交给 `probe-agent configure`：已有的 http 部署升级时在命令里加上它，一次重跑即可恢复。面板在这种地址上给出的命令会自动带上它。

### Linux

面板注册窗口页给出的命令形如下面这条（hub 为正式版本时脚本地址是 hub 同版本的 `releases/download/vX.Y.Z/install.sh`，装上的 agent 与 hub 同版本）：

```sh
curl -fsSL https://github.com/xjetry/probe/releases/latest/download/install.sh | sh -s -- --hub https://probe.example.com --key <key>
```

参数：`--hub`、`--key`（首次安装必需）、`--name`、`--insecure-http`、`--base-url`，含义见上。

以 root 运行，支持 systemd 与 OpenRC。卸载：同一条命令把参数换成 `--uninstall`，加 `--purge` 一并删除配置、专用日志目录、`probe-agent` 用户与同名组。systemd 普通卸载保留手工 drop-in；`--purge` 还删除 `/etc/systemd/system/probe-agent.service.d/` 与 `/run/systemd/system/probe-agent.service.d/`。目录为符号链接时只删除链接，不删除目标内容；共享 drop-in、发行版提供的配置和系统 journal 不清理。

### macOS

面板不给 macOS 的命令，用这一条（Apple Silicon 与 Intel 通用，脚本按硬件选包）：

```sh
curl -fsSL https://github.com/xjetry/probe/releases/latest/download/install-macos.sh | sudo sh -s -- --hub https://probe.example.com --key <key>
```

- agent 装成 LaunchDaemon `xyz.probe.agent`，以隐藏的系统用户 `_probe-agent` 运行；二进制在 `/usr/local/bin/probe-agent`，配置在 `/etc/probe-agent/`，日志在 `/Library/Logs/probe-agent/`。
- 参数与 Linux 脚本相同：`--name`、`--insecure-http`、`--base-url`（下载目录，用于镜像或本地构建；校验依据仍是脚本内嵌的 SHA-256）。没有 `--version`，指定版本取 `releases/download/vX.Y.Z/install-macos.sh`。
- 卸载：`… | sudo sh -s -- --uninstall`；加 `--purge` 一并删除配置、日志、用户与同名组。
- 默认不计入流量的网卡（回环、隧道与 VPN、桥与虚拟机网卡等）见 `probe-agent run -h`。

#### 真机核对

没有 macOS 虚拟机可做自动验收，改动 `deploy/install-macos.sh` 或 `deploy/launchd/xyz.probe.agent.plist` 后、发版前在一台 Mac 上逐条执行。未发布的构建用 `make release VERSION=v0.0.0-check` 产出 `dist/`（其中的脚本已写入这次构建的哈希；仓库里的 `deploy/install-macos.sh` 不能安装），`python3 -m http.server 18089 --directory dist` 提供下载，下面的 `<base>` 即该地址，安装时加 `--base-url <base>`。端口避开仓库里各验收脚本占用的号：e2e 用 18080/18081，Linux 安装验收用 18085/18086（下载服务就在 18086），macOS 本机验收用 18087/18088；同一台机器上同时跑时，后起的一方会绑不上端口。

1. `curl -fsSL <base>/install-macos.sh | sudo sh -s -- --hub <hub> --key <key> --base-url <base>`：最后一行是 `probe-agent installed and started (launchd, arm64, probe-agent_darwin_arm64.tar.gz)`（Intel 为 amd64）。
2. `dscl . -read /Users/_probe-agent UniqueID PrimaryGroupID UserShell NFSHomeDirectory`：UniqueID 是 300–499 中从 499 往下第一个两个命名空间都空闲的号（通常就是 499），PrimaryGroupID 等于 `dscl . -read /Groups/_probe-agent PrimaryGroupID` 的值、与 UniqueID 相同，UserShell 为 `/usr/bin/false`，NFSHomeDirectory 为 `/var/empty`；`id -gn _probe-agent` 输出 `_probe-agent`；登录窗口里看不到这个用户。
3. `ps -axo user,pid,command | grep '[p]robe-agent run'`：USER 为 `_probe-agent`。`sudo launchctl print system/xyz.probe.agent | grep -E 'state =|pid ='`：`state = running`。
4. `sudo ls -ld /etc/probe-agent /etc/probe-agent/config.json /Library/LaunchDaemons/xyz.probe.agent.plist /Library/Logs/probe-agent /Library/Logs/probe-agent/*`：目录 `drwxr-x--- root _probe-agent`；配置 `-rw------- _probe-agent _probe-agent`；plist `-rw-r--r-- root wheel`；日志目录 `drwxr-xr-x root wheel`；两个日志文件 `-rw-r----- _probe-agent _probe-agent`（安装脚本每次安装都建好）。
5. 面板：节点在线；详情页系统为 `macOS <sw_vers -productVersion>`、架构为 `arm64`（Intel 为 `amd64`）、CPU 型号等于 `sysctl -n machdep.cpu.brand_string`、ICMP 可用；实时视图每项指标都有值；内存已用与活动监视器的"已使用内存"接近。
6. 给节点建一个 ICMP 任务，目标取网关（`route -n get default | awk '/gateway/ {print $2}'`）或 1.1.1.1：一分钟后有 RTT，与 `ping -c 10 <目标>` 的平均值同一量级。
7. hub 在局域网地址（如 192.168.x.x）上时节点同样上线，ICMP 到局域网主机有结果（macOS 15 起的本地网络隐私不应拦截这个 LaunchDaemon）。
8. 带同一个 `--key` 重跑第 1 条：输出含 `existing config found; keeping the current registration (--key ignored)`，面板节点数不变；取另一个版本构建的 `install-macos.sh` 与对应的 `<base>` 重跑：面板上的 agent 版本随之改变。
9. hub 用 https 地址安装，节点上线后列出服务 uid 的全部进程：`ps -axo uid=,pid=,comm= | awk -v u=$(id -u _probe-agent) '$1 == u'`。agent 那一行的 comm 是 `/usr/local/bin/probe-agent`（安装脚本按 uid 加这个路径认进程）；若还有 cfprefsd、trustd 之类的辅助进程，把输出记下来，再重跑第 1 条与第 15 条的卸载各一次，都应成功而不报 `still running`。
10. `sudo launchctl disable system/xyz.probe.agent` 后重跑第 1 条：成功，服务在跑。
11. `sudo kill -9 $(pgrep -u _probe-agent probe-agent)`：约 5 秒内出现新的 pid，节点保持或恢复在线。
12. 日志文件被删后的行为：`sudo rm /Library/Logs/probe-agent/probe-agent.log && sudo launchctl kickstart -k system/xyz.probe.agent`，等 10 秒后 `sudo launchctl print system/xyz.probe.agent | grep -E 'state =|last exit code'` 并 `ls -l /Library/Logs/probe-agent`。会看到两种结果之一：文件重建、`state = running`（root 属主的目录里建得出文件，说明 launchd 以 root 打开；按 launchd.plist(5) 新建文件的属主取 UserName，所以不看属主；预建文件多余但无害），或作业以 `last exit code = 78: EX_CONFIG` 反复退出（以服务用户打开；重跑安装脚本把文件建回来即恢复）。记录看到的是哪一种；两种都不是缺陷。
13. 记下 `sysctl -n kern.bootsessionuuid`，睡眠至少 2 分钟（`pmset sleepnow` 或合盖）后唤醒：值不变；节点在离线时限内回到在线；流量图在唤醒处没有尖峰。
14. 重启这台 Mac：服务开机自启、节点在线；`kern.bootsessionuuid` 换了新值；总流量没有一次性跳涨。
15. `curl -fsSL <base>/install-macos.sh | sudo sh -s -- --uninstall`：输出 `probe-agent uninstalled`；`sudo launchctl print system/xyz.probe.agent` 退出码 113；`/usr/local/bin/probe-agent` 与 plist 不在，配置与用户仍在。再带 `--uninstall --purge` 执行：`/etc/probe-agent`、`/Library/Logs/probe-agent` 不在，`id _probe-agent` 报 no such user，`dscl . -read /Groups/_probe-agent` 报 `eDSRecordNotFound`。
16. 有 Intel Mac 时在其上重复 1–5，装的是 amd64 包。

### 宿主机本地策略

agent 不照单执行 hub 的指令：hub 被攻陷时，它能改变的只有 agent 上报的节奏（限在合法范围内）和探测任务（限在下面的本地策略内）。hub 不能让 agent 执行命令、读写文件或升级自己。本地策略写在配置文件 `/etc/probe-agent/config.json` 里，hub 改不了它，用 `probe-agent configure` 修改，改完重启服务生效：

```sh
# 放行对本机回环与某个内网段的探测（默认拒绝本机回环）
sudo probe-agent configure --probe-allow 127.0.0.0/8,10.20.0.0/16
# 另外拒绝整个私网（私网默认允许）
sudo probe-agent configure --probe-deny 10.0.0.0/8,172.16.0.0/12,192.168.0.0/16
# 清空某个列表
sudo probe-agent configure --probe-deny ""
sudo systemctl restart probe-agent   # OpenRC：rc-service probe-agent restart；macOS：launchctl kickstart -k system/xyz.probe.agent
```

- 默认拒绝的目标：本机（`127.0.0.0/8`、`::1`、`0.0.0.0/8`、`::`）、链路本地（`169.254.0.0/16`，含云厂商的 metadata 地址 `169.254.169.254`；`fe80::/10`）、组播与广播。检查的是解析之后实际要连的地址，用域名绕不过去。被拒的任务在面板上显示为错误，写明地址和命中的前缀。
- 规则按最长前缀匹配；前缀一样长时，本地规则优先于默认规则。前缀必须写成规范形式（`10.0.0.0/8`，不能写 `10.1.2.3/8`）；同一前缀不能同时出现在两个列表里。
- 连 hub 必须用 https。只有 hub 地址是回环 IP（`127.0.0.1`、`[::1]`）时才允许 http；其他情况用 http 要显式放行，放行后节点 token 和指标会以明文传输。首次安装时加 `--insecure-http`；已经用 http 部署的节点升级之后会拒绝启动，这时执行 `sudo probe-agent configure --insecure-http=true` 并重启服务（或重跑安装脚本并加 `--insecure-http`）。

## 许可

MIT，见 [LICENSE](LICENSE)。
