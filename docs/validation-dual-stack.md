# 双栈出口实现与验证

## 实现范围

2026-09-29，`/Users/xjetry/work/vibe/probe` 未提交工作树。

- `Facts.network` 包含 `NetworkInfo.ipv4/ipv6`，每族 `AddressDetection.state/address/checked_at`。枚举依次为 UNSPECIFIED=0、AVAILABLE=1、UNSUPPORTED=2、FAILED=3。
- agent 独立 `netinfo.Detector.Run`，启动后立即检测，此后每 5 分钟检测；`Runner.Network.Snapshot` 只读取副本，变化经既有 FactsHash 对账。
- 固定 `https://api64.ipify.org`，强制 tcp4/tcp6、无环境代理、不跟随重定向、64 字节响应体上界、10 秒请求超时。接口判定允许私网 IPv4 与 ULA；DNS、TLS、超时、格式错误是失败，明确连接 ENETUNREACH/EAFNOSUPPORT 或没有该族接口才是不支持。DNS 错误即使内层是 ENETUNREACH 仍是失败。
- schema 23 增加 `node_facts.network` JSON 列，旧库及旧快照缺省 `{}`，新快照往返保留状态。protojson 读取错误显式返回，不伪装无数据。
- 管理 API 返回网络信息；PublicFacts 显式保留 network 的号与名，不进入公开响应。可信上报来源和国家查询口径不变。
- 既有 geo 公网判定下沉到 `internal/netaddr`；出口探测和准入共用 `ParsePublicFamily`，不复制地址族与公网判定。

## 已读测试与变更

阅读了 agent/client 的摘要对账和 Runner 测试，ingest 的 Report、主机载荷预算测试，store 的冻结 schema、逐版本迁移、主题快照恢复、节点往返测试，API 的显式公开字段及正文排除测试，以及 cmd/hub 的历史快照和离线命令夹具。

新增 `internal/agent/netinfo/detector_test.go`、`internal/agent/client/network_test.go`、`internal/hub/ingest/network_test.go`、`internal/hub/store/network_test.go`；扩充公开 API 的管理可见/匿名排除断言和载荷预算。旧版本夹具移除新列后才宣称旧版本，冻结 schema 同步登记 23。

## 验证证据

以下命令均在上述绝对目录执行，不经过输出管道；本机 macOS arm64，Go 1.27.1。

- `buf generate` 退出 0，同步 Go/TS 生成物；`buf lint` 退出 0。
- `go test -count=1 ./internal/agent/... ./internal/agentwire ./internal/netaddr ./internal/hub/geo ./internal/hub/store ./internal/hub/ingest ./internal/hub/api ./cmd/hub ./cmd/agent` 退出 0。日志：`build/validation/dual-stack-local-final.log`。观察 store 10.379 秒、API 18.999 秒、cmd/hub 28.875 秒，全部包通过。
- `go test -race -count=1 ./internal/agent/netinfo ./internal/agent/client` 退出 0。日志：`build/validation/dual-stack-race.log`，两包 1.804 秒/6.097 秒。
- `git diff --check` 退出 0。
- 所有探测测试使用受控本地 TLS 服务、注入拨号和接口地址，不访问公网，不依赖本机公网 IPv6 可达性。拨号参数明确断言 tcp4/tcp6，TLS、取消与 HTTP 行为由实际 net/http 执行。

缺陷注入前均用 rg 确认修改存在；所有注入测试退出 1，观察到的失败原因如下，随后恢复原实现并运行上面的整组命令，退出 0。

| 日志 | 注入与实际观察 |
| --- | --- |
| `dual-stack-red-data.log` | 跳过网络准入后非法输入被接受；Runner 丢掉网络后首次报告缺失地址；存储丢掉 JSON 后往返为空；把无路由判成失败后 unsupported 断言失败。 |
| `dual-stack-red-boundaries.log` | 放宽共享地址判定后错误族、映射、私网、文档地址被接受；公开 CPU 字段夹带 IP 被发现；快照漏迁移导致读取新列失败。迁移默认值的结构差异仅证明 schema 一致性，未当成未知状态验红凭据。 |
| `dual-stack-red-invariants.log` | 迁移把旧行写成 unsupported，旧库与旧快照均在未知状态断言失败；公开名称夹带 IP，在原始匿名 JSON 排除断言失败；IPv6 强制 tcp4 被两族拨号断言发现；丢失探测时间在时间断言失败。 |
| `dual-stack-red-stale.log` | 失败继续带上旧地址，明确报 stale address after failure。 |
| `dual-stack-red-transport.log` | 放大响应上界后接受超长体；允许一次重定向后收到 2 个请求；共享快照指针被别名断言发现；启用环境代理被传输策略断言发现。 |
| `dual-stack-red-failed.log` | 默认失败状态误改为 unsupported，接口枚举错误、DNS（含 DNS 无路由）、超时、TLS 失败均在错误分类断言失败。 |

日志统一位于 `build/validation/`，未提交。

最终关键文件 SHA-256：

```text
7c3a1faf007fcde0dbfcb3131bb99f650457ffd95ee2c1fa25e5e9211e3b606f  internal/agent/netinfo/detector.go
5805c709379990761edefd8dda36998544c0b7d00cf8cc69b5e0f93c04ab214b  internal/agentwire/network.go
65ea416cfd985905520de5b4853dd4c34ae24921cb8854a9036a34b8d8b5876f  internal/hub/store/facts.go
6da33ab96f89264b573c97e0ce6eebde70f8b8e5af9eed24bf23395c0eb5a3f1  internal/hub/store/node.go
f52cb10417fe2105837d51151ee4730e805bf82958bfba5323576359e50a1e69  proto/heron/v1/types.proto
```

## 未做项

此单元没有修改页面/CSS、没有提交、推送、SSH 或部署，没有用公网探测结果充当受控测试证据。全仓库构建与全量测试、后台双栈展示和发布验收由主任务继续完成；这里的通过记录不代表它们已通过。
