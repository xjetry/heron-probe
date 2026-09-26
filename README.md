# probe

自托管服务器监控探针：agent 采集主机指标并上报，hub 存储、展示并对外提供查询。设计见 [架构设计](docs/superpowers/specs/2026-09-17-probe-architecture-design.md)。

## 运行 hub

hub 是一个静态链接的二进制，数据在一个 SQLite 文件里。从 [Releases](https://github.com/xjetry/probe/releases/latest) 下载 `probe-hub_linux_<arch>.tar.gz`（amd64、arm64）：

```sh
tar -xzf probe-hub_linux_amd64.tar.gz
./probe-hub passwd --db /var/lib/probe/probe.db   # 设置管理员密码
./probe-hub serve --db /var/lib/probe/probe.db    # 默认监听 127.0.0.1:8080
```

- hub 只监听明文 HTTP，TLS 由反代（Caddy、nginx、CDN）终止；反代地址用 `--trusted-proxies` 声明，否则不信任转发头。
- 管理面板在 `/admin/`。离线判定的时限由环境变量 `PROBE_OFFLINE_AFTER` 设定（默认 30s，10s–180s）。
- 其余参数见 `probe-hub serve -h`；节点、注册窗口与 API token 也可在 hub 主机上用 `probe-hub node|window|token` 管理。

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

没有 macOS 虚拟机可做自动验收，改动 `deploy/install-macos.sh` 或 `deploy/launchd/xyz.probe.agent.plist` 后、发版前在一台 Mac 上逐条执行。未发布的构建用 `make release VERSION=v0.0.0-check` 产出 `dist/`，`python3 -m http.server 18086 --directory dist` 提供下载，下面的 `<base>` 即该地址，安装时加 `--base-url <base>`。

1. `curl -fsSL <base>/install-macos.sh | sudo sh -s -- --hub <hub> --key <key> --base-url <base>`：最后一行是 `probe-agent installed and started (launchd, arm64, probe-agent_darwin_arm64.tar.gz)`（Intel 为 amd64）。
2. `dscl . -read /Users/_probe-agent UniqueID PrimaryGroupID UserShell NFSHomeDirectory`：UniqueID 在 300–499，PrimaryGroupID 等于 `dscl . -read /Groups/_probe-agent PrimaryGroupID` 的值，UserShell 为 `/usr/bin/false`；`id -gn _probe-agent` 输出 `_probe-agent`；登录窗口里看不到这个用户。
3. `ps -axo user,pid,command | grep '[p]robe-agent run'`：USER 为 `_probe-agent`。`sudo launchctl print system/xyz.probe.agent | grep -E 'state =|pid ='`：`state = running`。
4. `sudo ls -ld /etc/probe-agent /etc/probe-agent/config.json /Library/LaunchDaemons/xyz.probe.agent.plist /Library/Logs/probe-agent /Library/Logs/probe-agent/*`：目录 `drwxr-x--- root _probe-agent`；配置 `-rw------- _probe-agent _probe-agent`；plist `-rw-r--r-- root wheel`；日志目录 `drwxr-xr-x root wheel`；两个日志文件属 `_probe-agent`（由 launchd 创建）。
5. 面板：节点在线；详情页系统为 `macOS <sw_vers -productVersion>`、架构为 `arm64`（Intel 为 `amd64`）、CPU 型号等于 `sysctl -n machdep.cpu.brand_string`、ICMP 可用；实时视图每项指标都有值；内存已用与活动监视器的"已使用内存"接近。
6. 给节点建一个 ICMP 任务，目标取网关（`route -n get default | awk '/gateway/ {print $2}'`）或 1.1.1.1：一分钟后有 RTT，与 `ping -c 10 <目标>` 的平均值同一量级。
7. hub 在局域网地址（如 192.168.x.x）上时节点同样上线，ICMP 到局域网主机有结果（macOS 15 起的本地网络隐私不应拦截这个 LaunchDaemon）。
8. 带同一个 `--key` 重跑第 1 条：输出含 `existing config found; keeping the current registration (--key ignored)`，面板节点数不变；换一个版本重跑：面板上的 agent 版本随之改变。
9. `sudo launchctl disable system/xyz.probe.agent` 后重跑第 1 条：成功，服务在跑。
10. `sudo kill -9 $(pgrep -u _probe-agent probe-agent)`：约 5 秒内出现新的 pid，节点保持或恢复在线。
11. `sudo rm /Library/Logs/probe-agent/probe-agent.log /Library/Logs/probe-agent/probe-agent.err && sudo launchctl kickstart -k system/xyz.probe.agent`：两个文件被重新创建且属 `_probe-agent`，进程在跑。
12. 记下 `sysctl -n kern.bootsessionuuid`，睡眠至少 2 分钟（`pmset sleepnow` 或合盖）后唤醒：值不变；节点在离线时限内回到在线；流量图在唤醒处没有尖峰。
13. 重启这台 Mac：服务开机自启、节点在线；`kern.bootsessionuuid` 换了新值；总流量没有一次性跳涨。
14. `curl -fsSL <base>/install-macos.sh | sudo sh -s -- --uninstall`：输出 `probe-agent uninstalled`；`sudo launchctl print system/xyz.probe.agent` 退出码 113；`/usr/local/bin/probe-agent` 与 plist 不在，配置与用户仍在。再带 `--uninstall --purge` 执行：`/etc/probe-agent`、`/Library/Logs/probe-agent` 不在，`id _probe-agent` 报 no such user，`dscl . -read /Groups/_probe-agent` 报 `eDSRecordNotFound`。
15. 有 Intel Mac 时在其上重复 1–5，装的是 amd64 包。
