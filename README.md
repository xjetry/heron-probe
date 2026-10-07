<img src="web/src/assets/heron.svg" alt="" width="64" height="64" />

# Heron

轻量自托管主机监控。agent 采集主机指标并上报，hub 存储、展示并提供预授权自动化管理。关注状态，不接管系统。设计见 [架构设计](docs/superpowers/specs/2026-09-17-probe-architecture-design.md)。

## Agentic 自动化

在「API token」页按操作预授权监控配置与节点生命周期，并选择全站或指定节点。AI 和脚本经 HTTP+JSON 自主执行，支持真实事务预览、字段级修改、并发版本检查、安全重试及操作审计。旧 token 保持全站只读。注册入口按凭据隔离，自己创建或注册的节点自动纳入范围，普通标签不能扩权。

不开放远程命令、云主机操作、Hub 升级、管理员或 API 凭据管理、通知密钥修改。调用约定和示例见 [API 入口卡片](proto/SKILL.md)；也可从面板下载与 Hub 同版本的卡片。同页还能复制油猴脚本：粘贴进脚本管理器后任意站点出现悬浮按钮，在 IDC 页面看着价格与到期一步建节点并拿到安装命令。

名称、图形与命名边界见 [品牌约定](docs/brand.md)。Heron 使用独立的命令、服务路径与 `heron.v1` API，不兼容旧 probe 部署，安装器不自动迁移旧数据。

## 运行 hub

升级前备份库；新版本 `serve` 会迁移库，迁移后旧版本无法再打开。离线子命令遇到旧库会拒绝操作，并提示先用新版本 `serve` 升级。可使用内置 S3 分层快照在线备份，或停 hub 后复制整个数据卷，详见下文“数据卷与备份”。

hub 是一个静态链接的二进制，数据在一个 SQLite 文件里。从 [Releases](https://github.com/xjetry/heron-probe/releases/latest) 下载 `heron-hub_linux_<arch>.tar.gz`（amd64、arm64）：

```sh
tar -xzf heron-hub_linux_amd64.tar.gz
./heron-hub serve --db /var/lib/heron/heron.db    # 默认监听 127.0.0.1:8080
# 等 serve 启动完成后，在另一个终端设置管理员密码
./heron-hub passwd --db /var/lib/heron/heron.db
```

- hub 只监听明文 HTTP，TLS 由反代（Caddy、nginx、CDN）终止；反代地址用 `--trusted-proxies` 声明，否则不信任转发头。
- 管理面板在 `/admin/`，内置公开页页头的「登录」链到它；第三方主题与面板共用域名，但在沙箱中运行。离线判定的时限由环境变量 `HERON_OFFLINE_AFTER` 设定（默认 30s，10s–180s）。
- 其余参数见 `heron-hub serve -h`；节点、注册窗口与 API token 也可在 hub 主机上用 `heron-hub node|window|token` 管理。
- 节点可在面板里记录价格、币种、计费周期与到期日，周期支持 1 月、3 月、半年、1 年、2 年、3 年、5 年：只用于展示与提醒，hub 不汇总、不换算。公开节点填了的这几项也显示在公开页，自动续期开关除外。建「到期」类型的告警规则可在到期前若干天提醒；开着自动续期的节点过了到期日，hub 按周期把到期日推后。到期日按天计，天的边界与流量周期一样取 `--timezone`。
- 历史图展示 CPU、内存和网络采样峰值。网络均值仍是桶内入账字节数除以桶长；峰值是 agent 采样速率的最大值，不能代表未采到的瞬时尖峰。升级前历史和没有速率读数的旧 agent 保持空洞，不补零。
- 同一探测任务可跨节点并排比较：管理与公开接口各提供候选节点清单与分块取数（每块至多 16 个节点），每个节点的序列与单独查询该节点同一口径，不可见节点显式列为不可用而不是画成零。历史查询按实际读取的源行数计额度（每序列 12000 行），超额返回 `failed_precondition`，提示缩小窗口、加大 max_points 或等数据整理追上；数据整理水位回拨（迁移、维护落后）时窗口越长越容易触到额度。同一来源同时在飞的历史查询至多为 hub 可用 CPU 数的四分之一（至少 1 个，4 核机器上是 1 个）：限流只限准入速率，封不住重查询开环发送造成的在飞积压——结构保证是同一来源至多这么多个历史请求同时在读库（含节点准入），每个来源占用的读连接随之有界（来源数本身不设上限）；超出的请求最多等 5 秒，仍无空位按"来源并发过多"拒绝，与限流文案区分。首次升级到带对比索引的版本时，启动会为三张探测表建 (task_id, node_id, ts) 索引：在 Apple M4 Max 上实测约 110 万行/秒，单条序列满保留期共 27,480 行（1m 10080 + 5m 8640 + 1h 8760），存量约 1,800 条序列（约 4,930 万行）内的库预计在更新器的 90 秒宽限内完成；实际耗时随磁盘与 CPU 差别很大，升级前可用存储页的表行数自行估算，更新器判定新进程就绪的超时时间在宽限期过半时仍未达成就会回滚，不会把旧版本留在“已升级”状态。
- 探测任务可调整展示顺序，管理清单和两端历史图同步采用；重排不会改变 agent 执行清单或任务版本。节点管理支持连续排序、失败回读恢复和窄屏卡片布局。
- 在面板新建的节点默认公开：创建时不再询问公开范围，需要隐藏时在编辑里关闭「公开显示」。节点可挂多个标签，面板按标签过滤。公开节点的标签也显示在公开页，访客可以按标签筛选；标签常写用途与归属，挂到公开节点前先确认可以对外公开，没有单独隐藏标签的开关。

## 安装 hub（Linux，systemd）

支持 amd64、arm64，以 root 执行：

```sh
curl -fsSL https://github.com/xjetry/heron-probe/releases/latest/download/install-hub.sh | sh
heron-hub passwd --db /var/lib/heron/heron.db
```

安装器只按脚本里内嵌的本版 SHA-256 校验下载包（发布时写进脚本；release 里的 `SHA256SUMS` 供人工核对，不是脚本的校验依据），以静态系统用户 `heron-hub` 启动服务，确认进程持续存活后才提示设置密码。主机没有 CA 证书包时安装器会装上 `ca-certificates`：不论从哪里下载，hub 发往 Telegram 的告警都走 HTTPS。升级时先让新版本的 `serve` 启动并完成数据库迁移，再使用 `passwd` 等离线子命令。管理员密码由你设置，脚本不生成、不打印密码。

默认只监听 `127.0.0.1:8080`，TLS 交给反向代理。可用 `--listen`、`--timezone`、`--trusted-proxies`、`--public-dir`、`--geo-mmdb` 和 `--retention-*` 设置 serve 参数；`--admin-origin` 仅保留给旧 Passkey 凭据迁移，新安装不需要。例如：

```sh
curl -fsSL https://github.com/xjetry/heron-probe/releases/latest/download/install-hub.sh | sh -s -- \
  --timezone Asia/Taipei --trusted-proxies 127.0.0.1/32
```

重跑即升级，沿用 `/etc/systemd/system/heron-hub.service` 里 `ExecStart` 的参数，命令行显式给出的值按参数名替换旧值；写回时每个参数只留一份，统一写成 `--flag=value`。脚本只装自己所属的版本，没有 `--version`：`releases/latest/download/install-hub.sh` 装最新正式版，要装或升级到指定版本就取该版本的脚本，`https://github.com/xjetry/heron-probe/releases/download/vX.Y.Z/install-hub.sh`。`--base-url URL` 只改从哪个目录下载，接受哪些字节仍由内嵌的 SHA-256 决定。安装器只接受静态参数：用了 systemd 的 `$` / `%` 动态展开，或有 drop-in 设了 `ExecStart` 时，须先把参数合并为主单元里的静态值；无法解析时升级在停服前报错，不会重置配置。首装时已有设了 `ExecStart` 的 drop-in（例如预先写入 `heron-hub.service.d/` 的），安装器写好主单元后报错，不 enable、不 start。数据库固定为 `/var/lib/heron/heron.db`。

每次安装都用发行包里的单元覆盖主单元，只保留其中 `ExecStart` 的参数：主单元里别的手工改动（例如 `Environment=HERON_OFFLINE_AFTER=60s`）会在升级时丢失。这类定制放进 drop-in（`systemctl edit heron-hub`，写在 `/etc/systemd/system/heron-hub.service.d/`）；不设 `ExecStart` 的 drop-in 升级时保留。普通卸载保留 drop-in，`--purge` 删除 `/etc/systemd/system/heron-hub.service.d/` 与 `/run/systemd/system/heron-hub.service.d/`，包括手工定制；目录为符号链接时只删除链接，不删除目标内容。共享 drop-in、发行版提供的配置和系统 journal 不清理，journal 由系统日志保留策略处理。

数据目录为 `root:heron-hub 0770`，库文件为 `heron-hub:heron-hub 0600`；目录必须允许服务组创建和删除 SQLite 的 WAL/SHM 文件。数据目录或库文件是符号链接、库文件另有硬链接时，安装器在停服前拒绝，不改动链接指向的文件。单元逐项加固，将数据目录列入 `ReadWritePaths`，提供私有临时目录，不授予 `CAP_NET_RAW`。查看状态与日志：`systemctl status heron-hub`、`journalctl -u heron-hub`。

普通卸载停掉的是全部节点的展示与告警，`--purge` 删除唯一一份数据与全部节点凭据，所以两者都需确认；无终端时必须显式 `--yes`，不会读取管道里的脚本内容：

```sh
curl -fsSL https://github.com/xjetry/heron-probe/releases/latest/download/install-hub.sh | sh -s -- --uninstall --yes
# 同时删除 /var/lib/heron、服务用户与组，不可恢复
curl -fsSL https://github.com/xjetry/heron-probe/releases/latest/download/install-hub.sh | sh -s -- --uninstall --purge --yes
```

普通卸载保留数据和账户。安装、升级都会先核对目标端口的监听进程，排除现有 hub 自身；冲突时不停止旧服务。停止失败、旧进程未退出或新进程未持续存活均返回失败。停服之后、`systemctl start` 成功返回之前某一步失败时，安装器会提示 hub 已停，可重跑安装器或手动启动；启动之后没能持续存活时不这样提示，那时 systemd 仍按 `Restart=always` 继续拉起，按 `journalctl -u heron-hub` 排查。例外是写好主单元之后的 drop-in 检查没过：查出设了 `ExecStart` 的 drop-in 时，手动启动会按它的参数起来；`systemctl daemon-reload` 等本身出错时，drop-in 还没查过。这两种情形安装器都只说明单元是否仍 enabled，并按失败点要求先处理报出的 drop-in 或 systemctl 问题再重跑。是否 enabled 看的是 `/etc/systemd/system/multi-user.target.wants/heron-hub.service` 这条 enable 链接，不向 systemctl 查询，systemctl 出错时也答得出；升级时只要没被 disable 过，上次安装建的链接就还在，下次开机也会这样起来。另用 `systemctl add-wants` 等挂到别的 target 下的链接不在判断之内。

## 用 Docker 运行 hub

镜像 `ghcr.io/xjetry/heron-hub:<版本>`，含 linux/amd64 与 linux/arm64。正式版本在发布回读通过后同时成为 `latest`；预发布版本（tag 含 `-`，如 `v0.2.0-rc.1`）不动 `latest`。推 `v*` tag 触发发布，同组的发布运行串行：同时推多个 tag 时，排队中被后来者替换而取消的那个需要手工重跑。镜像基于 `scratch`，只有静态链接的 `heron-hub`、CA 证书与 uid 65532 的非 root 用户，没有 shell。

发版时的 `AGENT_VERSION`（spec §14.1）写最近一次已发布的 agent 版本，两次发版之间不动。发版提交前跑 `go run ./scripts/agentinputs -base "$(make -s agent-version)"`：输出 `agent inputs unchanged since …`（退出 0）就保持不变，这是只发 hub 的 release；输出 `agent inputs changed since …` 就在发版提交里把 `AGENT_VERSION` 改成这次的版本号，这是完整 release；其它输出是出错，先解决（经 `go run` 时“有变化”与“出错”的退出码都是 1，按输出区分）。拆分后的第一个 release 必须是完整 release：更早的版本既早于 agent 组的定义（门禁对它们报 `predates`），也没有发行签名。只发 hub 的 release 的说明里写明：先升级 hub，再更新节点；旧 hub 的面板会把最新版当作节点目标，节点任务会在下载阶段失败（旧 agent 不受影响）。

```sh
docker volume create heron-data
docker run -d --name heron --restart unless-stopped --stop-timeout 30 \
  -p 127.0.0.1:8080:8080 \
  -v heron-data:/data \
  -e TZ=Asia/Shanghai \
  ghcr.io/xjetry/heron-hub:v0.1.0
```

`--stop-timeout 30` 给关停留出余量：`docker stop` 先发 SIGTERM，hub 排空在途请求（至多 10 秒）、停下后台循环、关库后退出；宽限期一过 Docker 就发 SIGKILL。默认宽限期也是 10 秒，与排空上限相等，排空用满时最后一批写可能被截断。

镜像自带 `HEALTHCHECK`：容器内每隔 30 秒用 `heron-hub health` 请求 `http://127.0.0.1:8080/healthz`，探针只在库打开、内存索引加载并挂载全部服务之后才可能拿到应答（超时 5 秒、重试 3 次、起始宽限 5 秒），所以 `healthy` 表示 hub 已能接受请求，而不只是进程还在；探针不跟随重定向。`docker ps` 的 STATUS 列、编排器的健康判定与反代的上游摘除都读它。

默认参数是 `serve --db /data/heron.db --listen 0.0.0.0:8080`。镜像名之后写的任何参数都会替换这一整组默认参数，要加参数时连同默认的一起写全：

```sh
docker run -d --name heron --restart unless-stopped --stop-timeout 30 -p 127.0.0.1:8080:8080 -v heron-data:/data \
  ghcr.io/xjetry/heron-hub:v0.1.0 \
  serve --db /data/heron.db --listen 0.0.0.0:8080 --timezone Asia/Shanghai
```

### 设置管理员密码

hub 没有经网络的首次设置页，密码在容器里设置；hub 运行中设置即生效，不需要重启：

```sh
docker exec -it heron heron-hub passwd --db /data/heron.db
```

从脚本设置时经 stdin 传一行，必须带 `-i`。不带 `-i` 时容器里的 stdin 是空的，`passwd` 报 `no password on stdin` 并且不做任何修改：

```sh
printf '%s\n' "$ADMIN_PASSWORD" | docker exec -i heron heron-hub passwd --db /data/heron.db
```

### 反代与 `--trusted-proxies`

hub 只提供明文 HTTP，TLS 由反代（Caddy、nginx、CDN）终止。容器里监听 `0.0.0.0` 是预期的，启动日志里的 `listening on a non-loopback address` 告警照旧出现：能直连这个端口的人可以绕过反代并自带转发头。因此：

- 端口只发布到本机回环（`-p 127.0.0.1:8080:8080`，反代在宿主上），或者不发布端口，让反代容器与 hub 在同一个 Docker 网络里转发到 `http://heron:8080`。
- `--trusted-proxies` 写 hub 看到的反代地址（TCP 对端；CIDR 列表，逗号分隔）。只有来自这些地址的 `X-Forwarded-For`、`X-Forwarded-Proto` 才被采信。HTTPS 反代必须保留浏览器访问的 Host（含非默认端口），设置 `X-Forwarded-Proto: https`，并配置可信代理；否则管理接口的同源检查会拒绝浏览器请求，包括密码登录，返回 403。升级前先核对这些配置；不要通过改写或删除浏览器 Origin 头绕过检查。
- 反代与 CDN 必须对 `/heron.v1.AdminService/` 路径禁用响应压缩，并保留 hub 的 `Cache-Control: no-store, no-transform`：前者禁止缓存，后者要求中间层不要改写响应（包括重新压缩）。管理响应含私有数据和节点自报字符串，压缩后的长度可能泄漏二者的重合；TLS 不隐藏流量长度。不要只依赖源站已不压缩，部署后用带 `Accept-Encoding: gzip, deflate, br, zstd` 的管理请求检查最终响应没有 `Content-Encoding`、缓存头仍完整。公开 API 与静态资源可继续压缩。
- 不信任转发头时，公开页与注册的限流按 TCP 对端地址计，反代后的访客共用代理地址的一个桶；节点来源 IP 也记成代理地址。配置可信代理和正确的 `X-Forwarded-For` 后才能记录实际节点来源。量级：公开服务每个来源的桶容量 60、每秒补充 10；每个打开的总览页每 2 秒轮询一次快照（每秒 0.5 次），节点页另有每分钟两次历史查询，页面加载时还有几次请求。同时打开的总览页超过 20 个、或节点页约 19 个起，消耗就持续多于补充，60 次的余量用完后访客开始收到 429（30 个页面时约 10–12 秒后）。

反代与 hub 在同一个网络时，给网络固定网段、只让这两个容器加入，并信任这个网段：

```sh
docker network create --subnet 172.30.0.0/24 heron-net
docker run -d --name heron --restart unless-stopped --stop-timeout 30 --network heron-net -v heron-data:/data -e TZ=Asia/Shanghai \
  ghcr.io/xjetry/heron-hub:v0.1.0 \
  serve --db /data/heron.db --listen 0.0.0.0:8080 --trusted-proxies 172.30.0.0/24
# 反代容器以 --network heron-net 加入，把请求转给 http://heron:8080
```

经过 CDN 时还要核对上游一跳。若节点来源显示 Docker 内网地址，先检查 hub 信任的网段是否与反代实际 TCP 地址一致；若显示 CDN 地址，则需要在反代验证 CDN 来源后解析真实客户端地址，再转发给 hub。Caddy 可用 `trusted_proxies` 限定 Cloudflare 官方网段、`trusted_proxies_strict` 和 `client_ip_headers CF-Connecting-IP` 解析来源，并在 hub 的 `reverse_proxy` 中用 `header_up X-Forwarded-For {client_ip}` 传递已核实的地址。不要无条件信任客户端自带的 `CF-Connecting-IP` 或 `X-Forwarded-For`。国家查询只处理公网来源；地址要等下一次分钟写入后更新，查询完成后刷新管理节点列表。

节点双栈出口由新版 agent 在启动后立即、此后每 5 分钟分别经 IPv4/IPv6 请求 `https://api64.ipify.org`，不走环境代理、不跟随重定向，单次超时 10 秒，不阻塞指标采集。每族独立显示可用地址、不支持、探测失败或等待上报；只有没有可用该族接口地址，或系统明确报告无路由/地址族不支持，才标记不支持。DNS、TLS、超时和回显错误属于探测失败，失败清空旧地址。私网 IPv4 与 IPv6 ULA 仍会尝试探测，支持 NAT 出口。旧 agent 没有双栈结果，需要升级后才会出现。双栈结果只向管理员展示，是 agent 自报信息，不替代上报来源，不用于身份校验或国家查询。

### 账户安全

在面板「安全」进入认证器管理，可启用 TOTP、生成一次性恢复码、注册或删除 Passkey。启用 TOTP 后，密码登录必须同时提供六位动态验证码或一个恢复码；恢复码只显示一次，使用后失效，重新生成会作废旧码。修改认证方式需要重新证明身份，并撤销全部旧会话。

Passkey 支持无密码登录，无需手填域名或 `--admin-origin`。通过当前 HTTPS 域名进入「安全」页，浏览器支持 WebAuthn 时即可添加；首次注册成功才把完整 Origin 与主机名 RP ID 持久绑定。后续访问别的域名不会自动改绑。管理界面要求 HTTPS，本地开发也应通过 TLS 反代访问；注册和登录均要求认证器执行用户验证。

反代必须保留访问 Host、正确声明 HTTPS，并用 `--trusted-proxies` 只信任实际代理地址；不可信来源的转发头不会让 HTTP 请求冒充 HTTPS。换域名后用密码及现有第二因素登录，在「安全」页重新绑定并注册新 Passkey；改绑会撤销旧 Passkey 和旧会话。备份恢复保留原绑定，不根据恢复时的访问域名自动更改。

旧部署已有 Passkey、却没有数据库绑定时，升级首次启动仍保留原 `--admin-origin https://panel.example.com` 用于一次性导入；导入后以数据库为准，可以移除参数。缺少可信原配置时旧凭据保留但不可使用，需恢复原配置或通过密码及第二因素重新绑定，不会猜测 RP ID。

认证器丢失时先用恢复码登录；密码或全部认证器丢失时，可登录 hub 服务器，使用对数据库有读写权限的账号执行以下命令，不需要提供旧密码或认证器证明：

```bash
# 重设密码，终端交互输入且不回显；不会清除 TOTP 或 Passkey
heron-hub passwd --db /var/lib/heron/heron.db
# 清除 TOTP、恢复码和全部 Passkey；不改变密码或 API token
heron-hub security-reset --db /var/lib/heron/heron.db --yes
```

密码和认证器全部丢失时执行两条；两者都撤销全部登录会话，无需重启 hub。`passwd` 会列出现有 API token，并在交互终端询问是否一并撤销；非交互时默认保留。必须指定实际使用的数据库；`security-reset` 拒绝不存在的库，`passwd` 也可用于首次建库。旧库须先由新版本 `serve` 完成迁移。

Docker 部署对应为：

```bash
docker exec -it heron heron-hub passwd --db /data/heron.db
docker exec heron heron-hub security-reset --db /data/heron.db --yes
```

备份包含认证密钥，须按凭据管理，不应公开 bucket 或分享快照。

### 标签与告警

探测任务和告警规则可按标签一次性批量选中节点，保存后是固定节点列表；也可选择动态标签交集，自动覆盖同时拥有全部所选标签的节点。动态选择器与显式节点、全部节点互斥，标签变更会更新探测分配和告警作用域。被动态选择器引用的标签不能删除，须先修改引用它的任务或规则；匹配空集不表示全部节点。

资源规则支持内存与磁盘已用百分比，按同一次采样的已用量／总量计算，连续指定数量的已闭合分钟达到阈值才触发，连续相同数量的完整分钟不高于独立恢复阈值才恢复。缺失读数不算恢复。登录成功、失败、锁定、认证方式变更，以及备份成功、故障、故障恢复、停用和手动恢复都会保留事件；普通登录失败、周期备份成功及手动恢复只记审计，不默认投递通知。

### 公开页主题

在管理面板「主题」页选择公开 GitHub 仓库的 Release ZIP 资产，或上传已构建 ZIP，再预览和启用。公开页 `/` 与后台 `/admin/` 共用域名，切换无需改启动参数或重启。GitHub 仅用于安装，运行时由 hub 托管本地副本；主题在沙箱中通过 SDK 读取公开数据，不能获得管理员会话权限。

安装新版本不覆盖当前版本，也不自动启用。每主题最多保留 3 个版本、共 20 个主题，可切回上个版本或内置公开页；单版本清理保护当前和回滚版本，整主题卸载可回落内置页。`--public-dir` 接管公开页时不能启用第三方主题。

升级前删除旧启动配置的 `--theme-origin`，安装器会在停服前拒绝残留参数并说明迁移方式。未声明 SDK 1 的旧主题只保留归档和备份恢复能力，不在同域运行；需要主题作者适配 SDK 后重新发布。

旧主题域名不再按 Host 分流：仍指向 hub 时，也会提供 `/admin/` 及管理 API 的认证入口。若只保留一个域名，应撤掉旧域名的反代配置或将其重定向到新域名；不能继续依赖旧的“主题域名不承载管理入口”行为。

主题的开发、包布局、清单字段、上限与本地调试见 [主题开发指南](docs/theme-guide.md)。

### 时区

镜像里没有 `/etc/localtime`（时区数据已嵌入二进制）。不指定时区时，hub 按 UTC 判定流量周期的重置日与节点到期日的天边界，并在启动日志里告警 `host time zone could not be resolved; using UTC`。用 `-e TZ=Asia/Shanghai` 指定（不替换默认参数），或在写全的参数里加 `--timezone Asia/Shanghai`；两者都给时以 `--timezone` 为准。

### 环境变量

| 变量 | 作用 |
|---|---|
| `TZ` | IANA 时区名；没有给 `--timezone` 时使用 |
| `HERON_OFFLINE_AFTER` | 节点离线判定时长（Go duration，`10s` 到 `3m`，默认 `30s`）；上报间隔、退避上限与告警宽限期的下限都由它推出 |

### 数据卷与备份

面板中配置 S3 兼容 endpoint、bucket、区域、access key、secret 和前缀后，hub 自动生成一致性分层快照：配置层默认每 5 分钟一次、保留 48 份；指标层默认每天一次、保留 14 份。周期与份数可配置，两层状态可从面板读取。周期成功只记事件，配置层故障与恢复按设置的渠道通知。bucket 必须私有，并使用 HTTPS；快照不额外加密。

配置快照带主题摘要清单，先上传 `theme/sha256/<SHA-256>.zip` 不可变主题包，再上传引用它的快照。更新或删除当前主题不会覆盖、删除旧快照需要的包；主题对象不按当前安装清单自动清理，需另行评估保留空间。

可用 `sqlite3 config.db 'SELECT theme_id,digest,sha256 FROM snapshot_theme ORDER BY theme_id,digest;'` 查看所需主题版本，只下载清单引用的包。快照包含全部保留版本和当前、回滚选择。空 `sha256` 表示该版本没有原包，需重新上传后再备份；恢复该快照时可省略 `--themes` 并停用主题。

恢复前停止 hub，将配置快照、可选的指标快照和所引用的主题包下载到本地：

```sh
heron-hub restore --db heron.db --config config.db --metrics metrics.db --themes ./themes --yes
```

`themes` 目录放摘要命名的 `<SHA-256>.zip`。新格式快照指定 `--themes` 时严格校验主题引用，要求的原包缺失或摘要不符拒绝恢复；省略该参数则恢复配置但停用全部主题。恢复接受明确支持的 schema 17–24，旧快照在私有副本中迁移，不改写来源；未来版本、未知格式或结构不符拒绝。旧格式按原有 `<主题 id>.zip` 导入，缺包主题保留但停用，不兼容 SDK 的旧包恢复后仍不可执行。恢复不复活会话、注册窗口或节点更新授权，并写入恢复记录和手动恢复事件。两层时刻可不同，以配置层节点为准清理孤儿历史。

若不使用在线快照，可停机备份整个卷：

- `/data` 由 uid 65532 写入。新建的命名卷或匿名卷挂上时，Docker 把镜像里 `/data` 的属主带过去，无需处理。绑定宿主目录（`-v /srv/heron:/data`）时，目录须可被 uid 65532 写入（Linux 宿主上 `chown 65532:65532 /srv/heron`），否则 hub 启动即退出，报错 `open database /data/heron.db: …`。
- 库由 `heron.db` 与同名的 `heron.db-wal`、`heron.db-shm` 三个文件组成，已提交但尚未写回 `heron.db` 的数据只在 `-wal` 里：运行中只复制 `heron.db` 会丢数据。备份时先停容器，再把三个文件（或整个卷）一起复制：

```sh
docker stop heron
docker run --rm -v heron-data:/data -v "$PWD":/backup alpine:3.21 tar -C /data -czf /backup/heron-data.tgz .
docker start heron
```

### 排查

镜像里没有 shell，`docker exec heron sh` 不可用。可以：

- 看日志：`docker logs heron`。
- 查版本、SQL 统计时刻（`sql_observed_at`，Unix 秒）、各表行数与存储健康（各级最老桶、上卷水位、上次清理与上卷的完成时刻；SQL 统计在算出后 60 秒内复用，WAL 为逐调用观测；标红判定见面板的存储页或 `GetStorageStats`）：`docker exec heron heron-hub version`、`docker exec heron heron-hub stats --db /data/heron.db`。
- 看卷里的文件：挂同一个卷起一个带 shell 的临时容器，`docker run --rm -v heron-data:/data alpine:3.21 ls -ln /data`。
- 从 hub 自己的网络里发请求：`docker run --rm --network container:heron alpine:3.21 wget -qO /dev/null http://127.0.0.1:8080/admin/ && echo ok`。

### 备份设置损坏的恢复

`setting` 中已有的备份周期或保留份数越界时，`GetSettings` 与未修复全部坏值的 `UpdateSettings` 返回 `Internal`，不会把坏值当作默认值，也不会部分保存外观或公开页总闸。hub 日志中的 `invalid stored backup.<字段>` 指出损坏的键。

使用管理员登录会话调用 `POST /heron.v1.AdminService/UpdateSettings`（`Content-Type: application/json`），在 `settings.backup` 中**同时提供四个合法数值**即可覆盖坏值并恢复读取；不需要直接改库。默认值为：

```json
{"configIntervalS":300,"metricsIntervalS":86400,"configKeep":48,"metricsKeep":14}
```

这是 `backup` 的数值片段，不是完整请求：请求在 `settings.backup` 中仍须带齐要保留的备份目标（`endpoint`、`bucket`、`region`、`accessKey`、`prefix`），因为这些字段在备份组内整体替换。若选择关闭备份，可显式把目标字段清空、区域设为 `auto`，但四个数值仍需合法。外观五项全部省略时不改外观，不需要猜测读取失败前的外观值；若给出任一外观项，则必须带合法 `theme`，并整体替换五项。`secret`、`notify`、`publicEnabled` 与国家查询项缺席表示不变，不要把未知旧值猜成空值或开关值。

## 安装 agent

先在面板的「注册窗口」开一个窗口拿到 key（或在 hub 主机上 `heron-hub window open`）；也可以在「节点」页直接添加节点，用创建时返回的 `heron_install_` 安装凭据当 `--key`。hub 认领该节点后使安装凭据失效，返回只用于上报的运行 token；安装凭据不能上报，运行 token 不能再次注册。CLI `node create`、`node rotate-token` 与管理 API 的 `token` 字段也返回安装凭据，不能直接填入 agent 配置。旧版运行 token 继续上报，旧版尚未使用的无前缀安装 token 须在面板重新换发；库中的完整凭据哈希格式不变，无需数据库迁移。

普通重跑安装命令即升级：已有配置时沿用现有注册，不消费 `--key`。在面板「换 token」后，必须使用弹窗中带 **`--re-register`** 的命令（Linux、macOS 安装脚本均支持），并给出 `--hub`、`--key`：它显式重新注册并重启服务，沿用本地探测策略，不删除配置。换发会立即撤销旧凭据，直到原机完成重新注册才能恢复上报；不要把凭据替换当作普通升级。

安装凭据只能消费一次。若脚本已显示注册成功并写好配置，但后续安装失败，去掉 `--re-register` 按普通升级重跑，沿用已写入的运行 token。若注册响应丢失或配置写入失败，在面板为原节点重新换发凭据，不重复创建节点，也不重用已消费的凭据。

安装命令以本 README 与 GitHub Release 为准，不以 hub 面板为准：面板由 hub 提供，hub 失守时面板上的命令可以被整条换掉，这一点产品内防不住。面板的命令只是为了方便，复制前核对脚本地址是 `https://github.com/xjetry/heron-probe/releases/…`。

两个 agent 脚本与 hub 脚本共同的规则：

- `install-hub.sh` 只装自己所属的 hub 版本，没有 `--version`；`install.sh` 与 `install-macos.sh` 装的是该 release 的 hub 绑定的 agent 版本（只发 hub 的 release 原样带着绑定版本的这两个脚本），`releases/latest/download/install.sh` 因而装最新 hub 绑定的 agent。要装或升级到指定的 hub 版本就取 `https://github.com/xjetry/heron-probe/releases/download/vX.Y.Z/install-hub.sh`；要装指定的 agent 版本，取那个 agent 版本自己的 release 里的脚本。
- 脚本里内嵌本版全部 tar 包的 SHA-256，只按它校验下载的包。`--base-url URL` 只改从哪个目录下载（镜像、本地构建），接受哪些字节不变：指向的目录里即使放了与篡改包相符的 `SHA256SUMS` 也装不上。仓库里的源码脚本没有内嵌哈希，只能卸载，安装请用 release 里的脚本。
- `--insecure-http`（只在两个 agent 脚本上，hub 脚本没有 hub 地址）：hub 地址是 `http://` 且主机不是 loopback IP（`127.0.0.0/8`、`[::1]`；`localhost` 不算）时必须给出，agent 否则拒绝注册与启动。它把"接受明文 http"写进 agent 的本地配置，意味着节点 token 与指标明文传输，链路上的中间人与 hub 失守等价；能用 https 就用 https。首次安装或显式重新注册时交给 `heron-agent register`，普通升级时交给 `heron-agent configure`：已有的 http 部署升级时在命令里加上它，一次重跑即可恢复。面板在这种地址上给出的命令会自动带上它。

hub 与 agent 的版本：每个 hub 版本绑定一个 agent 版本（一套 tag，两种 release）；agent 版本号是它最后一次变动时的 release 版本，会跳号，只改 hub 的版本不要求节点升级。面板按绑定版本标“agent 低于 vX”，在线更新节点也只到绑定版本。

### Linux

面板注册窗口页给出的命令形如下面这条（hub 为正式版本时脚本地址是 hub 同版本的 `releases/download/vX.Y.Z/install.sh`，装上的是该 hub 版本绑定的 agent）：

```sh
curl -fsSL https://github.com/xjetry/heron-probe/releases/latest/download/install.sh | sh -s -- --hub https://heron.example.com --key <key>
```

参数：`--hub`、`--key`（首次安装必需）、`--name`、`--insecure-http`、`--base-url`，含义见上；`--update-source github|hub` 选在线更新从哪里取产物，见「在线更新」。

以 root 运行，支持 systemd 与 OpenRC。卸载：同一条命令把参数换成 `--uninstall`，加 `--purge` 一并删除配置、专用日志目录、`heron-agent` 用户与同名组。systemd 普通卸载保留手工 drop-in；`--purge` 还删除 `/etc/systemd/system/heron-agent.service.d/` 与 `/run/systemd/system/heron-agent.service.d/`。目录为符号链接时只删除链接，不删除目标内容；共享 drop-in、发行版提供的配置和系统 journal 不清理。

### macOS

面板不给 macOS 的命令，用这一条（Apple Silicon 与 Intel 通用，脚本按硬件选包）：

```sh
curl -fsSL https://github.com/xjetry/heron-probe/releases/latest/download/install-macos.sh | sudo sh -s -- --hub https://heron.example.com --key <key>
```

- agent 装成 LaunchDaemon `xyz.heron.agent`，以隐藏的系统用户 `_heron-agent` 运行；二进制在 `/usr/local/bin/heron-agent`，配置在 `/etc/heron-agent/`，日志在 `/Library/Logs/heron-agent/`。
- 参数与 Linux 脚本相同：`--name`、`--insecure-http`、`--base-url`（下载目录，用于镜像或本地构建；校验依据仍是脚本内嵌的 SHA-256）。没有 `--version`，指定版本取 `releases/download/vX.Y.Z/install-macos.sh`。
- 卸载：`… | sudo sh -s -- --uninstall`；加 `--purge` 一并删除配置、日志、用户与同名组。
- 默认不计入流量的网卡（回环、隧道与 VPN、桥与虚拟机网卡等）见 `heron-agent run -h`。

#### 真机核对

没有 macOS 虚拟机可做自动验收，改动 `deploy/install-macos.sh` 或 `deploy/launchd/xyz.heron.agent.plist` 后、发版前在一台 Mac 上逐条执行。未发布的构建用 `make release-full VERSION=v0.0.0-check AGENT_VERSION=v0.0.0-check` 产出 `dist/`（验收的是本次构建的 agent，AGENT_VERSION 与 VERSION 相同才产出完整的一套，spec §14.1；其中的脚本已写入这次构建的哈希；仓库里的 `deploy/install-macos.sh` 不能安装），`python3 -m http.server 18089 --directory dist` 提供下载，下面的 `<base>` 即该地址，安装时加 `--base-url <base>`。端口避开仓库里各验收脚本占用的号：e2e 用 18080/18081，Linux 安装验收用 18085/18086（下载服务就在 18086），macOS 本机验收用 18087/18088；同一台机器上同时跑时，后起的一方会绑不上端口。

1. `curl -fsSL <base>/install-macos.sh | sudo sh -s -- --hub <hub> --key <key> --base-url <base>`：最后一行是 `heron-agent installed and started (launchd, arm64, heron-agent_darwin_arm64.tar.gz)`（Intel 为 amd64）。
2. `dscl . -read /Users/_heron-agent UniqueID PrimaryGroupID UserShell NFSHomeDirectory`：UniqueID 是 300–499 中从 499 往下第一个两个命名空间都空闲的号（通常就是 499），PrimaryGroupID 等于 `dscl . -read /Groups/_heron-agent PrimaryGroupID` 的值、与 UniqueID 相同，UserShell 为 `/usr/bin/false`，NFSHomeDirectory 为 `/var/empty`；`id -gn _heron-agent` 输出 `_heron-agent`；登录窗口里看不到这个用户。
3. `ps -axo user,pid,command | grep '[h]eron-agent run'`：USER 为 `_heron-agent`。`sudo launchctl print system/xyz.heron.agent | grep -E 'state =|pid ='`：`state = running`。
4. `sudo ls -ld /etc/heron-agent /etc/heron-agent/config.json /Library/LaunchDaemons/xyz.heron.agent.plist /Library/Logs/heron-agent /Library/Logs/heron-agent/*`：目录 `drwxr-x--- root _heron-agent`；配置 `-rw------- _heron-agent _heron-agent`；plist `-rw-r--r-- root wheel`；日志目录 `drwxr-xr-x root wheel`；两个日志文件 `-rw-r----- _heron-agent _heron-agent`（安装脚本每次安装都建好）。
5. 面板：节点在线；详情页系统为 `macOS <sw_vers -productVersion>`、架构为 `arm64`（Intel 为 `amd64`）、CPU 型号等于 `sysctl -n machdep.cpu.brand_string`、ICMP 可用；实时视图每项指标都有值；内存已用与活动监视器的"已使用内存"接近。
6. 给节点建一个 ICMP 任务，目标取网关（`route -n get default | awk '/gateway/ {print $2}'`）或 1.1.1.1：一分钟后有 RTT，与 `ping -c 10 <目标>` 的平均值同一量级。
7. hub 在局域网地址（如 192.168.x.x）上时节点同样上线，ICMP 到局域网主机有结果（macOS 15 起的本地网络隐私不应拦截这个 LaunchDaemon）。
8. 带同一个 `--key` 重跑第 1 条：输出含 `existing config found; keeping the current registration (--key ignored)`，面板节点数不变；取另一个版本构建的 `install-macos.sh` 与对应的 `<base>` 重跑：面板上的 agent 版本随之改变。
9. hub 用 https 地址安装，节点上线后列出服务 uid 的全部进程：`ps -axo uid=,pid=,comm= | awk -v u=$(id -u _heron-agent) '$1 == u'`。agent 那一行的 comm 是 `/usr/local/bin/heron-agent`（安装脚本按 uid 加这个路径认进程）；若还有 cfprefsd、trustd 之类的辅助进程，把输出记下来，再重跑第 1 条与第 15 条的卸载各一次，都应成功而不报 `still running`。
10. `sudo launchctl disable system/xyz.heron.agent` 后重跑第 1 条：成功，服务在跑。
11. `sudo kill -9 $(pgrep -u _heron-agent heron-agent)`：约 5 秒内出现新的 pid，节点保持或恢复在线。
12. 日志文件被删后的行为：`sudo rm /Library/Logs/heron-agent/heron-agent.log && sudo launchctl kickstart -k system/xyz.heron.agent`，等 10 秒后 `sudo launchctl print system/xyz.heron.agent | grep -E 'state =|last exit code'` 并 `ls -l /Library/Logs/heron-agent`。会看到两种结果之一：文件重建、`state = running`（root 属主的目录里建得出文件，说明 launchd 以 root 打开；按 launchd.plist(5) 新建文件的属主取 UserName，所以不看属主；预建文件多余但无害），或作业以 `last exit code = 78: EX_CONFIG` 反复退出（以服务用户打开；重跑安装脚本把文件建回来即恢复）。记录看到的是哪一种；两种都不是缺陷。
13. 记下 `sysctl -n kern.bootsessionuuid`，睡眠至少 2 分钟（`pmset sleepnow` 或合盖）后唤醒：值不变；节点在离线时限内回到在线；流量图在唤醒处没有尖峰。
14. 重启这台 Mac：服务开机自启、节点在线；`kern.bootsessionuuid` 换了新值；总流量没有一次性跳涨。
15. `curl -fsSL <base>/install-macos.sh | sudo sh -s -- --uninstall`：输出 `heron-agent uninstalled`；`sudo launchctl print system/xyz.heron.agent` 退出码 113；`/usr/local/bin/heron-agent` 与 plist 不在，配置与用户仍在。再带 `--uninstall --purge` 执行：`/etc/heron-agent`、`/Library/Logs/heron-agent` 不在，`id _heron-agent` 报 no such user，`dscl . -read /Groups/_heron-agent` 报 `eDSRecordNotFound`。
16. 有 Intel Mac 时在其上重复 1–5，装的是 amd64 包。

### 宿主机本地策略

agent 不照单执行 hub 的指令：hub 可改变上报节奏（限在合法范围内）和探测任务（限在下面的本地策略内）。装有本机更新器的 Linux systemd 节点还接受受限的官方版本升级请求；更新器独立核实正式 Release、版本递增与 SHA-256，不接受 hub 提供的下载地址或命令。hub 不能下发任意程序或修改本地配置。本地探测策略写在 `/etc/heron-agent/config.json`，用 `heron-agent configure` 修改，改完重启服务生效：

```sh
# 放行对本机回环与某个内网段的探测（默认拒绝本机回环）
sudo heron-agent configure --probe-allow 127.0.0.0/8,10.20.0.0/16
# 另外拒绝整个私网（私网默认允许）
sudo heron-agent configure --probe-deny 10.0.0.0/8,172.16.0.0/12,192.168.0.0/16
# 清空某个列表
sudo heron-agent configure --probe-deny ""
sudo systemctl restart heron-agent   # OpenRC：rc-service heron-agent restart；macOS：launchctl kickstart -k system/xyz.heron.agent
```

- 默认拒绝的目标：本机回环（`127.0.0.0/8`、`::1`、`0.0.0.0/8`、`::`）、本机网卡上的全部地址（每次探测前重新枚举）、链路本地（`169.254.0.0/16`，含云厂商的 metadata 地址 `169.254.169.254`；`fe80::/10`）、另外两个 metadata 地址（阿里云 `100.100.100.200`、AWS 的 `fd00:ec2::254`）、组播与广播。检查的是解析之后实际要连的地址，用域名绕不过去。被拒的任务在面板上显示为错误，写明地址和命中的前缀。节点探测自己网卡上的地址会报错，要放行就写出该地址的 `/32`（IPv6 为 `/128`）；本机回环与链路本地地址则按上面的整段放行（如 `127.0.0.0/8`）。
- 规则按最长前缀匹配；前缀一样长时，本地规则优先于默认规则。前缀必须写成规范形式（`10.0.0.0/8`，不能写 `10.1.2.3/8`），IPv4 要写成 IPv4 形式（不能写 `::ffff:10.0.0.0/104`）；同一前缀不能同时出现在两个列表里。配置文件里有不认识的字段或多余内容时 agent 拒绝启动，免得拼错的规则被静默忽略。
- agent 的日志有总量上限（突发 20 行，此后每 30 秒至多一行，被压掉的行数记在下一行的 `suppressed_before` 上；每个值至多 256 字节）。systemd 下日志进 journald；OpenRC 与 launchd 的日志文件不自动轮转，由宿主机的日志管理处理。
- 连 hub 必须用 https，例外与 `--insecure-http` 的含义见上面「安装 agent」。不重跑安装脚本、只修正已部署节点时：`sudo heron-agent configure --insecure-http=true`，再重启服务。

## 在线更新

管理后台的「在线更新」提供官方版本检查、Hub 更新与选定节点批量更新。首次启用须在对应机器重跑新版官方安装器，以安装独立的 `heron-updater-hub` 或 `heron-updater-agent` root 服务；旧版没有这个服务，不能直接从后台自举。主服务仍以原来的非 root 用户运行。Docker、OpenRC、macOS 和不符合固定程序/配置路径的自定义服务不支持在线更新。

更新仅接受 `xjetry/heron-probe` 已发布的 `vMAJOR.MINOR.PATCH` 正式版本，拒绝预发布、同版和降级。每台机器串行更新。节点任务离线等待最多 24 小时，仅尚未下发的任务可以取消。下发后超时，或人工安装已达到目标但没有匹配任务记录时，显示「结果未确认」，不冒充成功或失败；迟到的同任务终态仍可校正结果，重试仍受本机事务互斥约束。安装器与在线更新互斥，正在更新时重跑安装器会拒绝，不会抢占事务。

下载与校验期间旧服务继续运行。Hub 停服后备份程序与数据库，候选完成初始化、实际进程摘要核验通过后才接受业务请求；失败时恢复程序及数据库。Agent 必须以新版本成功上报后才算完成。面板断连不表示成功，须等待回读状态。恢复失败时保留备份并报告错误，需要人工处理，不继续覆盖备份。

更新器只替换主程序，不更新自身、systemd 单元或启动参数。更新器和服务定义需要通过新版官方安装器升级。事务及回滚备份位于 `/var/lib/heron-update-{hub,agent}`，正常卸载会移除更新器及其已终结的事务历史/备份，业务数据仍按原有 `--purge` 规则处理。节点更新任务不进入分层快照，恢复快照不会重放旧升级命令。

更新器只接受带官方发行签名的产物：release 流水线用 Ed25519 对版本号与 `SHA256SUMS` 原文一起签名，签名作为 `SHA256SUMS.sig` 随 release 发布；更新器用内嵌的公钥、按任务的版本号验签（拿别的版本的签名冒充验不过），再按已验签的 `SHA256SUMS` 核对归档摘要与结构。签名私钥只在 release 流水线里使用，信任根仍是官方仓库的发行权限，发行权限被攻陷仍会影响更新安全。更新器只认签名，不知道版本是否已从 GitHub 撤回：失守的 hub 仍能让经它中转的节点装上曾签名发布、后来撤回的更高版本。

从哪里取产物与接受哪些字节互不相关。hub 主机总从 GitHub 取；节点默认也从 GitHub 取，出站受限、只能连到 hub 的节点在安装时给 `install.sh` 加 `--update-source hub`，改由 hub 中转：hub 从 GitHub 取回、验签后转发，节点上的更新器照样验签，失守的 hub 塞不进别的程序。这个参数只对 Linux systemd 有效（OpenRC 主机的安装器会拒绝它，macOS 脚本没有它），设置写在 root 属主的 `/etc/heron-update-agent/config.json`；重跑安装器不带这个参数时沿用原设置，给 `--update-source github` 才改回直连。面板「在线更新」页显示每个节点走的是「GitHub 直连」还是「经 hub 中转」；面板生成安装命令时勾选「国内主机」，命令会带上 `--update-source hub`，并给出安装时经本机代理出网用的 SSH 反代参数。

签名校验与 hub 中转从 v0.6.0 的更新器开始生效：更新器不随在线更新替换，要在对应机器重跑 v0.6.0 或更新的官方安装器换上；经 hub 中转还要求 hub 已是 v0.6.0 或更新（中转接口随该版本加入）。在此之前，旧更新器照旧直接从 GitHub 取 `SHA256SUMS` 与归档，只核对摘要；只能连到 hub 的节点在旧更新器下无法在线更新，换上新更新器的那次安装仍要借安装时可用的出网手段（如上面的 SSH 反代）。

## 开发验收

`make test`、`make lint`、`make build` 分别运行后端测试、静态检查和跨平台编译；前端在 `web/` 执行 `pnpm test` 与 `pnpm run build`。

`make e2e compat-e2e` 使用真实 hub，在 Debian、Alpine 的 amd64、arm64 容器中分别运行当前源码 agent 和固定已发布 agent。兼容基线在 `scripts/compat-agent.json` 固定 tag、发布渠道和两架构 SHA256，不使用浮动 latest；当前固定正式版 `v0.3.5`，CI 与发布流程都执行兼容矩阵。更新基线时必须核对发布资产和摘要，再运行完整兼容矩阵。更名前的 `probe.v1` 发布不能作 `heron.v1` 的兼容基线；缺少基线时明确失败，不跳过检查。Linux runner 需设置 `E2E_LISTEN_HOST=0.0.0.0` 供 bridge 容器连接，开发机默认只监听回环。

管理端节点详情的「Agent 运行诊断」显示最近保存的生效网卡规则、实际计入网卡、采集失败类别与上报间隔。它是只读的最近上报信息，不保证离线节点当前健康；旧 Agent 未提供时显示未知。诊断不含凭据、Hub URL、完整命令行或原始错误，也不进入公开页和主题数据。网卡清单最多显示前 128 个，超过时保留真实总数，计数仍覆盖全部匹配网卡。

Agent 的 `run --net-include` / `--net-exclude` 为逗号分隔的 glob，每组最多 64 项、每项最多 128 字节；包含规则非空时只按包含规则统计，否则使用指定排除规则或平台默认规则。实际计入的网卡集合变化时，首个样本仅重建速率与流量差分基线，不把新网卡已有计数当新流量。该标识随累计基线原子保存；旧 Agent 仍能上报，但无法识别集合变化。首次升级到支持该标识的 Agent 会重建一次基线。

重建基线会舍弃跨集合的整个采样区间，频繁增删网卡时会持续少计，并非无损计费口径；这类主机可用包含规则只统计稳定的上行接口。

上述持久化使用 schema 26。升级前保留一致性数据库备份；升级后不能只换回旧 Hub 二进制降级，必须同时恢复升级前数据库。

管理端节点详情新增「执行环境」：整体范围、CPU / 内存 / swap / 负载各自的范围与可见上限，以及固定类别的说明。容量是可见上限，不是机器的实际容量。采样来源取自最近保存的 Facts，可能滞后于实时指标，旁边给出保存时间。旧 agent 未上报时显示「未上报」，不会被当成整台主机。公开页与主题数据不包含执行环境。

按核负载由 agent 在采样时算好后上报。管理端与公开端的实时快照和历史都直接使用这个值，hub 不再用 Facts 里的核数去除。升级前没有该列的分钟在历史图上是空的，不是 0。按核负载告警读同一列的分钟均值：旧 agent 不上报时该分钟无读数，既不触发也不恢复；分母后来变化不会改写已经入库的分钟。升级后每个节点的 Facts 会被重新索取一次（旧库里的摘要还没有覆盖执行环境），agent 不用重启。

读数口径随 agent 识别出的执行环境变化，更新 agent 后生效：无限额的容器与 LXC guest 里，CPU 与内存从整台主机的读数改为该环境自己的用量与可见上限；默认挂载 lxcfs 的 LXC guest 里，`/proc/loadavg` 由 lxcfs 提供而不是 procfs，负载的范围无法确定，负载与按核负载不再上报；有 CPU 限额的 Docker 容器里，按核负载的分母从容器核数改为主机核数——负载本来就是整台主机的。主机与虚拟机的读数不变。先升级 Hub 再升级 Agent：新 Agent 连旧 Hub 时，旧 Hub 仍用 Facts 的核数去除，默认 lxcfs 的 LXC guest 在旧 Hub 上没有按核负载读数。

执行环境与按核负载列使用 schema 34。升级前保留一致性数据库备份；升级后不能只换回旧 Hub 二进制降级，必须同时恢复升级前数据库。

HTTPS 探测任务可以钉住叶证书公钥指纹。指纹写在任务上，面板与请求里的显示形如 `sha256//` 加 base64。不声明钉指纹能力的旧 agent 收不到已钉住的任务，它的结果也不会被采信；未钉住的任务仍按原来的方式下发。升级到 schema 35 时，已有任务各自得到一个配置身份，清单版本随之推进，agent 会重新领取带身份的任务。信任某个候选指纹是一次显式的任务写入（会话，或被预授权修改探测任务并通过节点范围裁决的 API token），不是看到候选就生效。

## 许可

MIT，见 [LICENSE](LICENSE)。
