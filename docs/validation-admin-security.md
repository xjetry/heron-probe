# 管理员认证验证记录

## 范围与版本

记录时间：2026-09-29。工作目录 `/Users/xjetry/work/vibe/probe`，基点 `bffd1485fe2a0ea34da0b2900ff2b014b17de2d3`，验证对象为其上的未提交认证修改，不是该提交本身。

环境：本机 macOS、Go 1.27.1、离线依赖缓存，WebAuthn 验证库 `github.com/go-webauthn/webauthn v0.17.4`。Go 命令显式使用 `GOCACHE=/private/tmp/probe-go-cache GOPROXY=off GOSUMDB=off`。

以下最终局部测试完成于 10:45 左右，早于后续将认证动作改为域内强类型及用结果字段统一 cookie 撤销的重构；该重构需由总验收重新运行，不能直接沿用本记录作为最终树凭据。

当时关键文件 SHA-256：

```text
df699f45a7941b17ec31843b702d9d3a0dda9063a07afc7d3def49998d07360f  internal/hub/auth/security.go
8cf71667f85506eb8d9f76cfa368d5ea5dc6f3930e63ab53079a051001dc3b12  internal/hub/auth/security_test.go
3ccdeb0e3901a0818e65e7f4eb272f4b116e870d7abffdbd3c531b13beaf0017  internal/hub/store/security.go
25ebe6bacdb29712b767138f93fb7654240c5c39d29429b99fe009e19f9f5920  internal/hub/store/security_test.go
c8c88df6ca0da8c365bec74971baa72db48f7baaeab07ad6ab244577f04547f4  internal/hub/api/security.go
8d636dfdefa729756acf7c9f5fe828da9b22d3d1a251f5f48ca88978c9a7b483  web/src/pages/Login.tsx
27dcaa1cbf9d4f38abbc6b987a98e6c4cd2a8c79b767f3b371c60b6cda9b5b43  web/src/pages/Security.tsx
```

## 执行与观察

- `go test -count=1 ./internal/hub/auth`：退出码 0。完整 auth 测试包含旧密码并发撤销、登录门与来源锁定，以及新增 TOTP、恢复码、Passkey 测试。完整输出 `/private/tmp/probe-security-auth-final.log`。
- `go test -count=1 ./internal/hub/auth ./internal/hub/store -run 'TestTOTP|TestSecurity|TestWebAuthn|TestPasskey'`：退出码 0。输出 `/private/tmp/probe-security-targeted-final.log`。
- 在 `web` 目录执行 `npm test -- src/pages/Login.test.tsx src/pages/Security.test.tsx`：退出码 0，两个文件、六个测试通过。
- 在 `web` 目录执行 `npm run typecheck`：退出码 0。
- 尝试 `go test -count=1 ./internal/hub/auth ./internal/hub/api` 时，API 测试的 `httptest` 监听被当前沙箱拒绝（`bind: operation not permitted`）；这不是 API 产品失败，也不是 API 验收通过。需要由可监听环境补跑。

新增认证测试从 `Auth.Login`、`Auth.LoginFactors`、`Auth.SecurityAction` 等入口观察以下结果：启用 TOTP 后密码不能单独登录；已消费时间步和恢复码不能重用；恢复码明文不进入持久化 JSON；变更认证器撤销会话；已撤销会话不能继续完成绑定；旧 generation 不能签发会话；挑战受用途、会话、代际、期限和单次消费约束。

Passkey 测试在测试中构造 ES256 软件认证器，产生 CBOR/COSE 注册材料及真实签名，再交给正式 WebAuthn 库校验；实际观察注册成功、无密码登录成功、错误 origin 和缺失 UV 被拒绝、断言重放被拒绝、Passkey 重新认证证明可删除凭据并撤销会话。来源测试观察同一 IPv6 /64 不能占用超过三个待完成匿名挑战，另一 /64 仍可开始；Passkey 验证与密码验证共享并发门，Passkey 失败触发的锁定也约束密码登录。

## 缺陷注入

每次修改后先读取源文件确认注入生效，再执行对应测试；结束后恢复生产实现。

| 注入 | 观察 | 恢复后 |
| --- | --- | --- |
| 从密码登录条件移除 `consumeFactor` | `TestTOTPEnrollmentLoginRecoveryAndRevocation` 退出码 1，失败原因 `password bypassed factor: <nil>` | 完整 auth 测试通过 |
| 将 SQL 中 `generation=?` 改为不约束版本的 `?>=0` | `TestSecurityCASAndChallengeBindings` 退出码 1，失败原因 `stale security state issued session` | 局部及完整 auth 测试通过 |
| 将 WebAuthn 正式验证错误错误地视为成功 | `TestPasskeyRegistrationLoginOriginProofAndDeletion` 退出码 1，失败原因 `foreign origin assertion accepted` | 局部及完整 auth 测试通过 |
| 恢复码成功页继续启用 `getSecurity` 查询 | `Security.test.tsx` 退出码 1；失效缓存时查询次数从预期 1 次增加到 2 次 | 两个认证 UI 测试文件六测通过 |

SQLite 事务失败验证使用持久化测试中的真实触发器，拒绝写入 `alert_event`。密码修改、安全重置、认证器变更三种操作均返回失败，读回确认密码、认证状态、generation 与原会话保持不变；不是只检查返回错误。

注入日志分别为 `/private/tmp/probe-security-mutant-totp.log`、`/private/tmp/probe-security-mutant-cas.log`、`/private/tmp/probe-security-mutant-webauthn.log`、`/private/tmp/probe-security-mutant-ui.log`。临时日志不是长期交付载体，关键实际观察已记在本文件。

## 未覆盖

未使用真实浏览器或硬件/系统 Passkey 认证器执行注册和登录；软件认证器证明正式库可校验这些材料，不证明 Safari、Chrome、iCloud Keychain 或实体安全密钥的交互兼容性。UI 测试使用 jsdom 和模拟浏览器凭据函数，不能替代浏览器验收。尚未在本子任务执行完整仓库、跨 origin 浏览器或部署启动验收。
