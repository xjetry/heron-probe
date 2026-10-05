# 更新产物签名与 hub 中转验收记录

## 范围与版本

- 分支 `integ/r908b6d3`；门禁在 `2399b8a`，隔离验收的测试程序编自 `2399b8a` 加出网检查修正 `b19b094`（只改验收用例本身）。
- 设计：spec §4.10、§5.7、§12；计划 `docs/superpowers/plans/2026-10-05-update-relay.md`。
- 受信公钥列表在本记录时为空：隔离验收用测试公钥（`internal/releasesig/sigtest`），经构造参数注入测试程序，正式二进制不链接它（`TestProductionBinariesDoNotLinkSigtest`）。

## 自动检查

- `make ci`（含 `make lint`、全量 `go test`、deploy 替身测试、脚本测试、前端 71 文件 803 用例、构建）在 `2399b8a` 退出 0，用时 885 秒，之后工作树干净。
- `go test -race -count=1 -timeout 30m ./internal/... ./cmd/...`、`GOOS=linux go vet ./...`、`GOOS=darwin go vet ./...`、`make e2e` 在 `aa15713` 退出 0；`2399b8a` 相对它只有 gofmt 换行。
- 第一次 `make ci` 停在 lint：Go 1.27 的 gofmt 拆开过长的单行函数，四处来自计划给出的代码，修正于 `2399b8a`。
- 高负载（四个 worktree 并行测试，1 分钟负载 13–17）下 deploy 包整包用时 1537 秒，超过 Makefile 给它的 20 分钟；负载回落后 617–670 秒。

## 隔离验收（spec §12）

环境：本地 OrbStack 2.2.3，两台新建机器 `pia-relay-hub`（10.88.1.204）与 `pia-relay-agent`（10.88.1.171），Debian 12.15 arm64，内核 7.0.14-orbstack，iptables 1.8.9（nf_tables），Caddy 2.6.2。

- hub 机：Caddy 以 `https://10.88.1.204:28080 { tls internal; reverse_proxy 127.0.0.1:8080 }` 终止 TLS；`hub.test -test.run TestRelayAcceptServe` 监听 127.0.0.1:8080（真实 ingest、Manager、Relay，假官方源与测试公钥）。Caddy 本地 CA 根证书装进 agent 机系统信任库。
- agent 机出站：只放行回环、已建立连接、DNS 与 10.88.1.204 的 tcp/28080，其余 REJECT；IPv6 只放行回环。
- 环境事实：两台机器的 DNS 经宿主机代理返回 fake-ip（github.com → 198.18.0.103）。`githubtransport` 拒绝非公网地址，所以验收用例原来的"连不上 GitHub"检查在这里不论封锁与否都会失败、不构成证据；已改为对 `github.com:443` 的普通 TCP 拨号（`b19b094`）。

封锁的证据（同一台 agent 机）：

| 检查 | 加封锁前 | 加封锁后 |
|---|---|---|
| `curl https://github.com/` | HTTP 200 | 连接失败（curl 7） |
| `curl https://release-assets.githubusercontent.com/`、`http://example.com/`、`https://1.1.1.1/` | — | 连接失败（curl 7） |
| `curl http://10.88.1.204:8080/`（绕过反代直连 hub） | — | 连接失败（curl 7） |
| `curl https://10.88.1.204:28080/` | — | HTTP 404（TLS 与证书校验通过，测试 hub 只挂 AgentService） |
| 验收用例的出网检查（修正后） | 失败：`reached GitHub directly (198.18.0.103:443)` | 通过：`dial tcp 198.18.0.103:443: connect: connection refused` |

三轮结果（agent 端 `update.test -test.run TestRelayAcceptFetch`，三轮都在封锁生效时运行）：

| 轮次 | hub 端 | 期望 | 结果 |
|---|---|---|---|
| 正常 | 官方产物合法 | 经 hub 取回并以测试公钥接受，得到约定二进制 | PASS |
| 篡改官方产物 | `HERON_RELAY_TAMPER=1` | hub 预验签拒绝 | PASS：`fetch release from hub: unavailable: fetch official release: release asset SHA-256 mismatch` |
| 失守的 hub | `HERON_RELAY_TAMPER=1 HERON_RELAY_SKIP_VERIFY=1` | hub 原样转发，节点 Accept 拒绝 | PASS：取回成功，`release asset SHA-256 mismatch` 来自节点侧 Accept |

## 未覆盖

- 真实 systemd 更新器在 hub 来源下完成一次替换并确认：本次只验证了取回与接受（`HubSource` + `Accept` 经真实网络与反代），替换与确认路径沿用 2026-09-29 在线更新验收的结论（`docs/validation/2026-09-29-online-update.md`），本次未改动。
- 生产签名链路（Actions secret 签名、Release 回读验签）要到第一个带签名的正式版发布时才实际运行。
- 生产特殊主机上"后台点更新 → 经 hub 中转升级"的完整回读，要到第二个带签名的正式版发布后才能做。

原始日志在 `.herdr-runs/r908b6d3/accept/`（git 忽略）：`fw-r1.log`、`fw-final.log`、`agent-control.log`、`agent-r1.log`、`agent-r2.log`、`agent-r3.log`，hub 端日志 `relay-hub-r1.log`、`relay-hub-r2.log`、`relay-hub-r3.log`（三轮都以 SIGTERM 正常结束，`TestRelayAcceptServe` PASS）。
