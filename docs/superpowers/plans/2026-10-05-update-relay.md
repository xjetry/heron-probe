# 更新产物签名与 hub 中转 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 在线更新改为只按 Ed25519 发行签名接受产物，出站只能连到 hub 的节点在安装时选 `--update-source hub`，经 hub 的 `AgentService.GetRelease` 中转取官方产物。

**Architecture:** 新增只依赖标准库的 `internal/releasesig`（被签消息、签名文件格式、受信公钥），release 流水线在独立 job 里签名。更新器的来源只取回三份原始字节（`SHA256SUMS`、`SHA256SUMS.sig`、归档），接受判定只在 `update.Accept` 一处。hub 侧 `updates.Relay` 按节点当前任务授权、取回合并、预验签、内存缓存按引用释放；更新器侧 `update.HubSource` 读 agent 配置、用与 agent 共用的 `hubclient` 调 `GetRelease`。

**Tech Stack:** Go（`crypto/ed25519`、ConnectRPC unary、protobuf/buf）、POSIX sh（install.sh）、React + Vitest（面板）、GitHub Actions。

**Spec:** `docs/superpowers/specs/2026-09-17-probe-architecture-design.md` 的 §4.10（主体），以及 §1、§3.2、§3.3、§4.2、§5.4、§5.7、§12、§14、§16 的相关段落。实现前完整读 §4.10 与 §5.7。

## Global Constraints

- 执行规则：每个 worker 先读本次运行目录下的 `exec-rules.md`（用户全局规则摘要：不打补丁、注释与提交信息禁止过程信息、缺陷注入、命令不接管道、只在自己的 worktree 工作）。
- 被签消息：`"heron-release-v1\n" + <tag> + "\n" + <SHA256SUMS 原文>`；签名文件 `SHA256SUMS.sig` = 64 字节 Ed25519 签名的标准 base64 一行加 `\n`。
- 私钥：GitHub Actions secret `HERON_RELEASE_SIGNING_KEY`，内容是 `openssl genpkey -algorithm ed25519` 输出的 PKCS#8 PEM。
- 受信公钥只在 `internal/releasesig` 源码常量里；不经 ldflags、build tag、环境变量注入。测试经构造参数传测试公钥（`internal/releasesig/sigtest`），正式二进制不链接 `sigtest`。
- 大小上限：`SHA256SUMS` 1 MiB（`releasesig.MaxSums`），签名文件 1 KiB（`releasesig.MaxFile`），归档 128 MiB（`update` 包的 `maxArchive`），整次下载 5 分钟（`update` 包的 `downloadTimeout`）。
- 来源配置：`/etc/heron-update-agent/config.json`，内容 `{"source":"hub"}` 或 `{"source":"github"}`，严格解析；文件不存在 = github。只对 agent 角色生效。
- agent 配置：`/etc/heron-agent/config.json`（`update` 包常量 `agentConfigPath`）。
- `GetRelease`：节点 token 鉴权、不持 `stateMu`；任务须 ID 相同、状态 `dispatched` 或 `downloading`、未过期；同节点至多 1 个在途；每任务 ID 至多 3 次；缓存总量 256 MiB；超限 `ResourceExhausted`；不标 `NO_SIDE_EFFECTS`。
- `UpdateStatus.source` 取值 `github`、`hub`，旧更新器为空串。
- 注释用中文，写 WHY 与不变式；提交信息 `<type>(<scope>): <中文一句话>`。
- 不运行 `make e2e`、`make web-e2e`、playwright；`gen/` 与 `web/src/gen/` 只经 `make gen` 生成。
- 命令里的 `<worktree>` 是该任务 worktree 的绝对路径，`<task-dir>` 是运行目录下该任务的目录（日志与 result.md 放这里，不放共享 `/tmp` 根）。

## Review Focus

1. 节点 A 的 token 带着节点 B 的任务 ID 调 `GetRelease`：必须 `FailedPrecondition`，且不触发任何取回（Task 5 的 `TestRelayRejectsForeignTask`）。
2. hub 连不上 GitHub 或取回的签名不对：错误回给更新器、不进缓存，下一次请求重新取回而不是永远卡在旧错误（Task 5 的 `TestRelayDoesNotCacheFailures`）。
3. 一个慢下载在途时删除节点与其他节点的 Report 不被挡住（Task 5 的 `TestGetReleaseDoesNotBlockForgetOrReport`）。
4. 在 hub 来源的主机上不带 `--update-source` 重跑安装器：来源保持 hub；OpenRC 上给出该参数在任何下载与系统变更之前退出（Task 7 的 `TestUpdateSourceKeptOnRerun`、`TestUpdateSourceRefusedOnOpenRC`）。
5. 来源配置文件损坏：更新器照常运行，状态 `supported=false` 且 reason 含文件路径，新任务被拒（Task 6 的 `TestEngineSourceConfigErrorDisablesUpdates`）。

## 任务依赖与并行

| 任务 | 依赖 | 说明 |
|---|---|---|
| Task 1 发行签名包与签名流水线 | — | |
| Task 3 agent 配置与 hub 客户端共用包 | — | |
| Task 4 proto | — | 只改 proto 与生成物 |
| Task 7 安装脚本 `--update-source` | — | |
| Task 2 更新器唯一接受规则 | Task 1 | |
| Task 8 面板显示来源 | Task 4 | |
| Task 5 hub 中转 | Task 2、Task 4 | |
| Task 6 更新器 hub 来源 | Task 2、Task 3、Task 4 | |
| Task 9 进程内集成与验收用例 | Task 5、Task 6 | |
| Task 10 生产公钥、隔离验收与合入（控制端） | 全部 | 不派 worker |

---

### Task 1: 发行签名包与签名流水线

**Files:**
- Create: `internal/releasesig/releasesig.go`
- Create: `internal/releasesig/keys.go`
- Create: `internal/releasesig/releasesig_test.go`
- Create: `internal/releasesig/sigtest/sigtest.go`
- Create: `scripts/releasesign/main.go`
- Create: `scripts/releasesign/main_test.go`
- Modify: `.github/workflows/release.yml`

**Interfaces:**
- Produces（`package releasesig`）：
  - `const MaxSums = 1 << 20`、`const MaxFile = 1 << 10`
  - `func Message(version string, sums []byte) []byte`
  - `func Sign(key ed25519.PrivateKey, version string, sums []byte) ([]byte, error)`（返回签名文件完整内容）
  - `func Verify(keys []ed25519.PublicKey, version string, sums, sigFile []byte) error`
  - `func ParsePrivateKey(pemData []byte) (ed25519.PrivateKey, error)`
  - `func Trusted() []ed25519.PublicKey`
- Produces（`package sigtest`，只供测试）：
  - `func Key() (ed25519.PublicKey, ed25519.PrivateKey)`
  - `func Archive(role string, binary []byte) []byte`（只含 `heron-<role>` 一个普通文件的 tar.gz）
  - `func Sums(asset string, archive []byte) []byte`（一行 `<hex>  <asset>\n`）
  - `func Sign(version string, sums []byte) []byte`

- [ ] **Step 1: 写 releasesig 的失败测试**

`internal/releasesig/releasesig_test.go`：

```go
package releasesig

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/pem"
	"strings"
	"testing"
)

func key(seed byte) (ed25519.PublicKey, ed25519.PrivateKey) {
	priv := ed25519.NewKeyFromSeed(bytes.Repeat([]byte{seed}, ed25519.SeedSize))
	return priv.Public().(ed25519.PublicKey), priv
}

func TestMessageLayout(t *testing.T) {
	got := string(Message("v1.2.3", []byte("abc  x.tar.gz\n")))
	if want := "heron-release-v1\nv1.2.3\nabc  x.tar.gz\n"; got != want {
		t.Fatalf("Message = %q, want %q", got, want)
	}
}

func TestVerify(t *testing.T) {
	pub, priv := key(1)
	other, _ := key(2)
	sums := []byte("00  heron-agent_linux_amd64.tar.gz\n")
	good, err := Sign(priv, "v1.2.3", sums)
	if err != nil {
		t.Fatal(err)
	}
	clip := func(b []byte) []byte { return b[:len(b):len(b)] }
	for _, tc := range []struct {
		name    string
		keys    []ed25519.PublicKey
		version string
		sums    []byte
		sig     []byte
		want    string
	}{
		{"valid", []ed25519.PublicKey{pub}, "v1.2.3", sums, good, ""},
		{"second_trusted_key", []ed25519.PublicKey{other, pub}, "v1.2.3", sums, good, ""},
		{"untrusted_key", []ed25519.PublicKey{other}, "v1.2.3", sums, good, "does not verify"},
		{"no_trusted_keys", nil, "v1.2.3", sums, good, "does not verify"},
		{"other_version", []ed25519.PublicKey{pub}, "v1.2.4", sums, good, "does not verify"},
		{"tampered_sums", []ed25519.PublicKey{pub}, "v1.2.3", append(clip(sums), 'x'), good, "does not verify"},
		{"short_public_key", []ed25519.PublicKey{pub[:31]}, "v1.2.3", sums, good, "does not verify"},
		{"missing_newline", []ed25519.PublicKey{pub}, "v1.2.3", sums, good[:len(good)-1], "one base64 line"},
		{"crlf", []ed25519.PublicKey{pub}, "v1.2.3", sums, append(clip(good[:len(good)-1]), '\r', '\n'), "one base64 line"},
		{"truncated", []ed25519.PublicKey{pub}, "v1.2.3", sums, append(clip(good[:20]), '\n'), "not a base64 Ed25519 signature"},
		{"oversized", []ed25519.PublicKey{pub}, "v1.2.3", sums, bytes.Repeat([]byte("A"), MaxFile+1), "exceeds size limit"},
		{"newline_in_version", []ed25519.PublicKey{pub}, "v1\nx", sums, good, "version"},
	} {
		err := Verify(tc.keys, tc.version, tc.sums, tc.sig)
		if tc.want == "" && err != nil {
			t.Errorf("%s rejected: %v", tc.name, err)
		}
		if tc.want != "" && (err == nil || !strings.Contains(err.Error(), tc.want)) {
			t.Errorf("%s: err=%v, want %q", tc.name, err, tc.want)
		}
	}
}

func TestSignRejectsNewlineInVersion(t *testing.T) {
	_, priv := key(1)
	if _, err := Sign(priv, "v1\nx", nil); err == nil {
		t.Fatal("version with a newline makes the signed message ambiguous and must be refused")
	}
}

func TestParsePrivateKey(t *testing.T) {
	_, priv := key(3)
	der, err := x509.MarshalPKCS8PrivateKey(priv)
	if err != nil {
		t.Fatal(err)
	}
	block := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})
	got, err := ParsePrivateKey(block)
	if err != nil || !got.Equal(priv) {
		t.Fatalf("round trip: %v", err)
	}
	ec, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	ecDER, err := x509.MarshalPKCS8PrivateKey(ec)
	if err != nil {
		t.Fatal(err)
	}
	for name, data := range map[string][]byte{
		"ecdsa":      pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: ecDER}),
		"two_blocks": append(append([]byte{}, block...), block...),
		"wrong_type": pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: der}),
		"not_pem":    []byte("seed"),
	} {
		if _, err := ParsePrivateKey(data); err == nil {
			t.Errorf("%s accepted", name)
		}
	}
}

func TestTrustedKeysAreWellFormed(t *testing.T) {
	for i, k := range Trusted() {
		if len(k) != ed25519.PublicKeySize {
			t.Fatalf("trusted key %d has %d bytes", i, len(k))
		}
	}
	keys := Trusted()
	if len(keys) > 0 {
		keys[0] = nil
		if Trusted()[0] == nil {
			t.Fatal("Trusted must return a copy")
		}
	}
}
```

- [ ] **Step 2: 运行确认失败**

Run: `cd <worktree> && go test -count=1 ./internal/releasesig/ > <task-dir>/t1.log 2>&1; echo $?`
Expected: 非 0，log 里是 `undefined: Message` 一类编译错误。

- [ ] **Step 3: 实现 releasesig**

`internal/releasesig/releasesig.go`：

```go
// Package releasesig 定义发行签名：被签的字节、签名文件的格式与受信公钥。
// 只依赖标准库：release 流水线在持有私钥的 job 里编译并运行它（scripts/releasesign），那个 job 不编译也不执行
// 任何第三方代码，私钥才不会落到依赖手里（spec §14）。scripts/releasesign 的依赖检查守着这一条。
package releasesig

import (
	"bytes"
	"crypto/ed25519"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"errors"
	"fmt"
	"strings"
)

// prefix 把发行签名与同一把私钥可能签的其他内容区分开；改它等于作废全部已发布的签名。
const prefix = "heron-release-v1\n"

const (
	// MaxSums 是 SHA256SUMS 的大小上限，更新器、hub 中转与签名工具读它时共用。
	MaxSums = 1 << 20
	// MaxFile 是签名文件的大小上限。合法文件是 64 字节签名的 base64（88 字节）加换行。
	MaxFile = 1 << 10
)

// Message 返回签名覆盖的字节。版本号在被签内容里，签名才绑定到版本：验签方必须传入自己持有的任务版本，
// 而不是来源声称的版本（spec §4.10）。版本号里不能有换行，否则 (v, sums) 的不同切分会拼出同一串字节——
// Sign 与 Verify 都先拒绝它。
func Message(version string, sums []byte) []byte {
	msg := make([]byte, 0, len(prefix)+len(version)+1+len(sums))
	msg = append(msg, prefix...)
	msg = append(msg, version...)
	msg = append(msg, '\n')
	return append(msg, sums...)
}

func checkVersion(version string) error {
	if version == "" || strings.ContainsAny(version, "\r\n") {
		return errors.New("release version must be a non-empty single line")
	}
	return nil
}

// Sign 返回 SHA256SUMS.sig 的完整内容。
func Sign(key ed25519.PrivateKey, version string, sums []byte) ([]byte, error) {
	if err := checkVersion(version); err != nil {
		return nil, err
	}
	sig := ed25519.Sign(key, Message(version, sums))
	return []byte(base64.StdEncoding.EncodeToString(sig) + "\n"), nil
}

// Verify 在 keys 中任一公钥验过 sigFile 时返回 nil。keys 为空时一律失败：没有受信公钥意味着不接受任何产物，
// 而不是不验。
func Verify(keys []ed25519.PublicKey, version string, sums, sigFile []byte) error {
	if err := checkVersion(version); err != nil {
		return err
	}
	sig, err := decode(sigFile)
	if err != nil {
		return err
	}
	msg := Message(version, sums)
	for _, k := range keys {
		// ed25519.Verify 遇到长度不对的公钥会 panic；受信列表由 mustParse 构造，这里仍按不可信输入处理。
		if len(k) == ed25519.PublicKeySize && ed25519.Verify(k, msg, sig) {
			return nil
		}
	}
	return fmt.Errorf("release signature does not verify for %s with any trusted key", version)
}

func decode(file []byte) ([]byte, error) {
	if len(file) > MaxFile {
		return nil, errors.New("release signature file exceeds size limit")
	}
	line, ok := bytes.CutSuffix(file, []byte("\n"))
	if !ok || bytes.ContainsAny(line, "\r\n") {
		return nil, errors.New("release signature file must be one base64 line ending in a newline")
	}
	sig, err := base64.StdEncoding.Strict().DecodeString(string(line))
	if err != nil || len(sig) != ed25519.SignatureSize {
		return nil, errors.New("release signature is not a base64 Ed25519 signature")
	}
	return sig, nil
}

// ParsePrivateKey 解析 `openssl genpkey -algorithm ed25519` 输出的 PKCS#8 PEM。
func ParsePrivateKey(pemData []byte) (ed25519.PrivateKey, error) {
	block, rest := pem.Decode(pemData)
	if block == nil || block.Type != "PRIVATE KEY" || len(bytes.TrimSpace(rest)) != 0 {
		return nil, errors.New("signing key must be a single PKCS#8 PEM block")
	}
	key, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("parse signing key: %w", err)
	}
	ed, ok := key.(ed25519.PrivateKey)
	if !ok {
		return nil, errors.New("signing key is not Ed25519")
	}
	return ed, nil
}
```

`internal/releasesig/keys.go`：

```go
package releasesig

import (
	"crypto/ed25519"
	"encoding/base64"
	"slices"
)

// trusted 是正式产物的受信公钥：标准 base64 的 32 字节 Ed25519 公钥；私钥只在 release 流水线的
// Actions secret 里。公钥写在源码里而不经 ldflags 或 build tag 注入：换钥必然是仓库里一次可审阅的改动，
// 正式二进制里没有第二把。更新器只能由 root 安装器升级（spec §5.7），这里的增删要每台主机重跑安装器才生效。
var trusted = mustParse()

// Trusted 返回受信公钥的副本，调用方改动切片不影响后续验签。
func Trusted() []ed25519.PublicKey { return slices.Clone(trusted) }

func mustParse(encoded ...string) []ed25519.PublicKey {
	keys := make([]ed25519.PublicKey, 0, len(encoded))
	for _, s := range encoded {
		raw, err := base64.StdEncoding.Strict().DecodeString(s)
		if err != nil || len(raw) != ed25519.PublicKeySize {
			panic("releasesig: trusted key is not a base64 Ed25519 public key: " + s)
		}
		keys = append(keys, ed25519.PublicKey(raw))
	}
	return keys
}
```

生产公钥由 Task 10 写入 `mustParse(...)` 的参数；本任务保持空列表（空 = 不接受任何产物，方向是收紧）。

- [ ] **Step 4: 写 sigtest**

`internal/releasesig/sigtest/sigtest.go`：

```go
// Package sigtest 为测试提供固定的发行签名密钥与产物。只供 _test.go 引用；正式二进制不链接它，
// cmd 的依赖检查守着这一条（update 包的 TestProductionBinariesDoNotLinkSigtest）。
package sigtest

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"

	"github.com/xjetry/heron-probe/internal/releasesig"
)

// Key 返回固定种子派生的测试密钥对，跨包、跨进程（隔离验收的 hub 与 agent 两端）都相同。
func Key() (ed25519.PublicKey, ed25519.PrivateKey) {
	priv := ed25519.NewKeyFromSeed(bytes.Repeat([]byte{7}, ed25519.SeedSize))
	return priv.Public().(ed25519.PublicKey), priv
}

// Archive 返回只含 heron-<role> 一个普通文件的 tar.gz，满足更新器的归档结构检查。
func Archive(role string, binary []byte) []byte {
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	if err := tw.WriteHeader(&tar.Header{Name: "heron-" + role, Mode: 0o755, Size: int64(len(binary)), Typeflag: tar.TypeReg, Format: tar.FormatUSTAR}); err != nil {
		panic(err)
	}
	if _, err := tw.Write(binary); err != nil {
		panic(err)
	}
	if err := tw.Close(); err != nil {
		panic(err)
	}
	if err := gz.Close(); err != nil {
		panic(err)
	}
	return buf.Bytes()
}

// Sums 返回只含 asset 一行的 SHA256SUMS。
func Sums(asset string, archive []byte) []byte {
	sum := sha256.Sum256(archive)
	return []byte(hex.EncodeToString(sum[:]) + "  " + asset + "\n")
}

// Sign 用测试私钥为 (version, sums) 签名。
func Sign(version string, sums []byte) []byte {
	_, priv := Key()
	sig, err := releasesig.Sign(priv, version, sums)
	if err != nil {
		panic(err)
	}
	return sig
}
```

`tar.FormatUSTAR` 让归档不带 PAX 扩展头——更新器的 `extractBinary` 拒绝 `PAXRecords` 非空的条目。

- [ ] **Step 5: 运行 releasesig 测试通过**

Run: `cd <worktree> && go test -count=1 ./internal/releasesig/... > <task-dir>/t1.log 2>&1; echo $?`
Expected: `0`。

- [ ] **Step 6: 写签名工具的失败测试**

`scripts/releasesign/main_test.go`：

```go
package main

import (
	"crypto/ed25519"
	"crypto/x509"
	"encoding/pem"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/xjetry/heron-probe/internal/releasesig/sigtest"
)

func pemKey(t *testing.T, priv ed25519.PrivateKey) string {
	t.Helper()
	der, err := x509.MarshalPKCS8PrivateKey(priv)
	if err != nil {
		t.Fatal(err)
	}
	return string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}))
}

func distDir(t *testing.T) string {
	t.Helper()
	d := t.TempDir()
	if err := os.WriteFile(filepath.Join(d, "SHA256SUMS"), []byte("00  heron-agent_linux_amd64.tar.gz\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return d
}

func env(m map[string]string) func(string) string { return func(k string) string { return m[k] } }

func TestSignThenVerify(t *testing.T) {
	pub, priv := sigtest.Key()
	d := distDir(t)
	if err := run([]string{"sign", "-version", "v1.2.3", "-dir", d}, env(map[string]string{"HERON_RELEASE_SIGNING_KEY": pemKey(t, priv)}), []ed25519.PublicKey{pub}); err != nil {
		t.Fatal(err)
	}
	if err := run([]string{"verify", "-version", "v1.2.3", "-dir", d}, env(nil), []ed25519.PublicKey{pub}); err != nil {
		t.Fatal(err)
	}
	if err := run([]string{"verify", "-version", "v1.2.4", "-dir", d}, env(nil), []ed25519.PublicKey{pub}); err == nil {
		t.Fatal("signature verified for a different version")
	}
}

func TestSignRefusesMissingKey(t *testing.T) {
	pub, _ := sigtest.Key()
	d := distDir(t)
	err := run([]string{"sign", "-version", "v1.2.3", "-dir", d}, env(nil), []ed25519.PublicKey{pub})
	if err == nil || !strings.Contains(err.Error(), "HERON_RELEASE_SIGNING_KEY") {
		t.Fatalf("err = %v", err)
	}
	if _, statErr := os.Stat(filepath.Join(d, "SHA256SUMS.sig")); !os.IsNotExist(statErr) {
		t.Fatal("a signature file was written without a key")
	}
}

func TestSignRefusesKeyOutsideTrustedSet(t *testing.T) {
	_, priv := sigtest.Key()
	other := ed25519.NewKeyFromSeed(make([]byte, ed25519.SeedSize)).Public().(ed25519.PublicKey)
	d := distDir(t)
	err := run([]string{"sign", "-version", "v1.2.3", "-dir", d}, env(map[string]string{"HERON_RELEASE_SIGNING_KEY": pemKey(t, priv)}), []ed25519.PublicKey{other})
	if err == nil || !strings.Contains(err.Error(), "does not match a trusted public key") {
		t.Fatalf("err = %v", err)
	}
	if _, statErr := os.Stat(filepath.Join(d, "SHA256SUMS.sig")); !os.IsNotExist(statErr) {
		t.Fatal("a signature no updater accepts was written")
	}
}

// 持有私钥的 job 只能编译标准库与 internal/releasesig（spec §14）。列表必须含本包自己，
// 否则 go list 出错或输出为空时这条检查会空过。
func TestSigningToolLinksOnlyStdlibAndReleasesig(t *testing.T) {
	out, err := exec.Command("go", "list", "-deps", "-f", "{{if not .Standard}}{{.ImportPath}}{{end}}", ".").CombinedOutput()
	if err != nil {
		t.Fatalf("go list: %v\n%s", err, out)
	}
	got := strings.Fields(string(out))
	slices.Sort(got)
	want := []string{"github.com/xjetry/heron-probe/internal/releasesig", "github.com/xjetry/heron-probe/scripts/releasesign"}
	if !slices.Equal(got, want) {
		t.Fatalf("non-standard dependencies = %q, want %q", got, want)
	}
}
```

- [ ] **Step 7: 实现签名工具**

`scripts/releasesign/main.go`：

```go
// Command releasesign 为 release 产物签名与验签。release 流水线在单独的 job 里运行它：那个 job 只编译本命令与
// internal/releasesig（只依赖标准库），私钥经环境变量只交给签名那一步（spec §14）。
//
//	releasesign sign   -version vX.Y.Z -dir dist   读 HERON_RELEASE_SIGNING_KEY，写 dist/SHA256SUMS.sig
//	releasesign verify -version vX.Y.Z -dir DIR    用 internal/releasesig 的受信公钥验 DIR 下的签名
package main

import (
	"crypto/ed25519"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/xjetry/heron-probe/internal/releasesig"
)

func main() {
	if err := run(os.Args[1:], os.Getenv, releasesig.Trusted()); err != nil {
		fmt.Fprintln(os.Stderr, "releasesign:", err)
		os.Exit(1)
	}
}

const usage = "usage: releasesign sign|verify -version vX.Y.Z -dir DIR"

func run(args []string, getenv func(string) string, trusted []ed25519.PublicKey) error {
	if len(args) == 0 {
		return errors.New(usage)
	}
	fs := flag.NewFlagSet(args[0], flag.ContinueOnError)
	version := fs.String("version", "", "release tag")
	dir := fs.String("dir", "", "directory holding SHA256SUMS")
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}
	if *version == "" || *dir == "" || fs.NArg() != 0 {
		return errors.New(usage)
	}
	sums, err := readLimited(filepath.Join(*dir, "SHA256SUMS"), releasesig.MaxSums)
	if err != nil {
		return err
	}
	switch args[0] {
	case "sign":
		pemKey := getenv("HERON_RELEASE_SIGNING_KEY")
		if pemKey == "" {
			return errors.New("HERON_RELEASE_SIGNING_KEY is empty; refusing to publish an unsigned release")
		}
		key, err := releasesig.ParsePrivateKey([]byte(pemKey))
		if err != nil {
			return err
		}
		sig, err := releasesig.Sign(key, *version, sums)
		if err != nil {
			return err
		}
		// 私钥与仓库里的公钥不匹配时签出的文件没有任何更新器会接受：在写文件与发布之前失败。
		if err := releasesig.Verify(trusted, *version, sums, sig); err != nil {
			return fmt.Errorf("signing key does not match a trusted public key: %w", err)
		}
		return os.WriteFile(filepath.Join(*dir, "SHA256SUMS.sig"), sig, 0o644)
	case "verify":
		sig, err := readLimited(filepath.Join(*dir, "SHA256SUMS.sig"), releasesig.MaxFile)
		if err != nil {
			return err
		}
		return releasesig.Verify(trusted, *version, sums, sig)
	}
	return errors.New(usage)
}

func readLimited(path string, limit int64) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(b)) > limit {
		return nil, fmt.Errorf("%s exceeds %d bytes", path, limit)
	}
	return b, nil
}
```

注意 `sigtest` 只出现在 `main_test.go`；`TestSigningToolLinksOnlyStdlibAndReleasesig` 用 `go list -deps .`（不含 `-test`），不会把测试依赖算进去。

- [ ] **Step 8: 运行测试通过，并做缺陷注入**

Run: `cd <worktree> && go test -count=1 ./internal/releasesig/... ./scripts/releasesign/ > <task-dir>/t1.log 2>&1; echo $?`
Expected: `0`。

缺陷注入（每条注入后跑同一命令，确认红在指定用例与原因，再恢复并 `git diff` 确认干净），记入 result.md：
1. `Message` 去掉 `version` 那两行 append → `TestMessageLayout`、`TestVerify/other_version` 红。
2. `Verify` 删掉 `len(k) == ed25519.PublicKeySize &&` → `TestVerify` 以 panic 红（short_public_key）。
3. `run` 的 `sign` 分支删掉 `releasesig.Verify(trusted, …)` 检查 → `TestSignRefusesKeyOutsideTrustedSet` 红。
4. 在 `main.go` 临时 `import _ "google.golang.org/protobuf/proto"` → `TestSigningToolLinksOnlyStdlibAndReleasesig` 红。

- [ ] **Step 9: 拆分 release 流水线**

`.github/workflows/release.yml` 改为三个 job，权限按 job 最小化：

1. 顶层 `permissions:` 改为 `{}`；原 `jobs.release` 改名 `build`，加 `permissions: { contents: read, packages: write }`，其余步骤（checkout 到 `make docker-promote`）原样保留。
2. 删除 `build` 里原来的 `gh release create` 那一步，换成：

```yaml
      # dist 交给签名 job；签名 job 不重新构建，它签的就是这里通过 e2e 与镜像回读的那一份产物。
      - uses: actions/upload-artifact@043fb46d1a93c77aae656e7c1c64a875d1fc6a0a # v7.0.1
        with:
          name: dist
          path: dist/
          if-no-files-found: error
          retention-days: 7
```

3. 追加两个 job：

```yaml
  # 私钥只进这个 job：它不运行 make ci / make release 里的第三方代码，只编译标准库与 internal/releasesig
  # （scripts/releasesign 的依赖检查守着），也不恢复构建 job 写过的 Go 缓存——被污染的依赖可能在缓存里留下
  # 编译产物。私钥只经 env 交给签名那一步。
  sign:
    needs: build
    runs-on: ubuntu-latest
    timeout-minutes: 10
    permissions:
      contents: read
    steps:
      - uses: actions/checkout@11d5960a326750d5838078e36cf38b85af677262 # v4
        with:
          persist-credentials: false
      - uses: actions/setup-go@40f1582b2485089dde7abd97c1529aa768e1baff # v5
        with:
          go-version-file: go.mod
          cache: false
      - uses: actions/download-artifact@3e5f45b2cfb9172054b4087a40e8e0b5a5461e7c # v8.0.1
        with:
          name: dist
          path: dist
      - name: sign
        env:
          HERON_RELEASE_SIGNING_KEY: ${{ secrets.HERON_RELEASE_SIGNING_KEY }}
        run: go run ./scripts/releasesign sign -version "$GITHUB_REF_NAME" -dir dist
      - run: go run ./scripts/releasesign verify -version "$GITHUB_REF_NAME" -dir dist
      - uses: actions/upload-artifact@043fb46d1a93c77aae656e7c1c64a875d1fc6a0a # v7.0.1
        with:
          name: signature
          path: dist/SHA256SUMS.sig
          if-no-files-found: error
          retention-days: 7
  publish:
    needs: sign
    runs-on: ubuntu-latest
    timeout-minutes: 10
    permissions:
      contents: write
    steps:
      - uses: actions/checkout@11d5960a326750d5838078e36cf38b85af677262 # v4
        with:
          persist-credentials: false
      - uses: actions/setup-go@40f1582b2485089dde7abd97c1529aa768e1baff # v5
        with:
          go-version-file: go.mod
          cache: false
      - uses: actions/download-artifact@3e5f45b2cfb9172054b4087a40e8e0b5a5461e7c # v8.0.1
        with:
          name: dist
          path: dist
      - uses: actions/download-artifact@3e5f45b2cfb9172054b4087a40e8e0b5a5461e7c # v8.0.1
        with:
          name: signature
          path: dist
      # prerelease 与镜像是否移动 latest 读同一个判定（make release-channel）。
      - run: |
          channel=$(make -s release-channel VERSION="$GITHUB_REF_NAME")
          case "$channel" in
            prerelease) extra=--prerelease ;;
            stable) extra= ;;
            *) echo "make release-channel printed '$channel'" >&2; exit 1 ;;
          esac
          gh release create "$GITHUB_REF_NAME" dist/* --verify-tag --title "$GITHUB_REF_NAME" ${extra:+"$extra"}
        env:
          GH_TOKEN: ${{ github.token }}
      # 回读：验的是 Release 页面上实际可下载的字节，不是本 job 手里的文件。
      - run: |
          gh release download "$GITHUB_REF_NAME" -p SHA256SUMS -p SHA256SUMS.sig -D readback
          go run ./scripts/releasesign verify -version "$GITHUB_REF_NAME" -dir readback
        env:
          GH_TOKEN: ${{ github.token }}
```

同时把 `build` 里原先"镜像先于 GitHub Release 发布，gh release create 是最后一步"那段注释改成按 job 描述：镜像在 `build` 里推送并回读，Release 在 `publish` 里最后创建，任一 job 失败都可以在 Actions 页面重跑失败的 job。

本地校验：`cd <worktree> && ruby -ryaml -e 'YAML.load_file(".github/workflows/release.yml")' > <task-dir>/yaml.log 2>&1; echo $?` 为 0（没有 ruby 时用 `python3 -c 'import yaml,sys; yaml.safe_load(open(sys.argv[1]))' .github/workflows/release.yml`）。若本机有 `actionlint` 再跑一次 `actionlint .github/workflows/release.yml`。

- [ ] **Step 10: 提交**

```bash
cd <worktree> && git add internal/releasesig scripts/releasesign .github/workflows/release.yml
git commit -m "feat(release): 发行签名包与独立签名 job，产物附 SHA256SUMS.sig"
```

---

### Task 2: 更新器唯一接受规则

**Files:**
- Create: `internal/update/accept.go`
- Create: `internal/update/accept_test.go`
- Create: `internal/update/deps_test.go`
- Modify: `internal/update/release.go`（删 `Download`，加 `Fetch`，常量改用 `releasesig.MaxSums`）
- Modify: `internal/update/engine.go`（`source` 接口、`sourceChoice`、`keys`、`downloadTimeout`、`status`、`submit`、`execute`）
- Modify: `internal/update/protocol.go`（`Status.Source`；`trailingJSON` 从 `system_linux.go` 移来）
- Modify: `internal/update/system_linux.go`（`Serve`/`serve` 传公钥与 `sourceChoice`；删 `trailingJSON`）
- Modify: `internal/update/engine_test.go`、`internal/update/release_test.go`、`internal/update/accept_linux_test.go`

**Interfaces:**
- Consumes: `releasesig.Verify`、`releasesig.MaxSums`、`releasesig.MaxFile`、`releasesig.Trusted`；`sigtest.Key/Archive/Sums/Sign`。
- Produces（`package update`）：
  - `type Artifacts struct { Sums, Signature, Archive []byte }`
  - `func Accept(keys []ed25519.PublicKey, role, arch, version string, a Artifacts) ([]byte, error)`
  - `func AgentArch(arch string) bool`
  - `func (s *OfficialSource) Fetch(ctx context.Context, task Request, role, arch string) (Artifacts, error)`
  - `type source interface { Fetch(context.Context, Request, string, string) (Artifacts, error) }`
  - `type sourceChoice struct { name string; src source; err string }`
  - `func newEngine(ctx context.Context, path, role, arch string, choice sourceChoice, keys []ed25519.PublicKey, m machine) (*Engine, error)`
  - `Status.Source string` (`json:"source,omitempty"`)
  - `func serve(ctx context.Context, role string, official source, keys []ed25519.PublicKey) error`
  - `const downloadTimeout = 5 * time.Minute`

- [ ] **Step 1: 写 Accept 的失败测试**

`internal/update/accept_test.go`：

```go
package update

import (
	"archive/tar"
	"crypto/ed25519"
	"strings"
	"testing"

	"github.com/xjetry/heron-probe/internal/releasesig/sigtest"
)

func signedArtifacts(role, arch, version string, archive []byte) Artifacts {
	asset, err := archiveName(role, arch)
	if err != nil {
		panic(err)
	}
	sums := sigtest.Sums(asset, archive)
	return Artifacts{Sums: sums, Signature: sigtest.Sign(version, sums), Archive: archive}
}

func testKeys() []ed25519.PublicKey { pub, _ := sigtest.Key(); return []ed25519.PublicKey{pub} }

func TestAccept(t *testing.T) {
	archive := sigtest.Archive("agent", []byte("binary"))
	good := signedArtifacts("agent", "amd64", "v1.2.3", archive)
	otherVersion := signedArtifacts("agent", "amd64", "v1.2.2", archive)
	otherAsset := signedArtifacts("agent", "arm64", "v1.2.3", archive)
	tamperedArchive := good
	tamperedArchive.Archive = sigtest.Archive("agent", []byte("evil"))
	extraEntry := makeArchive(t, []*tar.Header{
		{Name: "heron-agent", Mode: 0o755, Size: 6, Typeflag: tar.TypeReg},
		{Name: "extra", Mode: 0o644, Size: 1, Typeflag: tar.TypeReg},
	}, []string{"binary", "x"})
	badStructure := signedArtifacts("agent", "amd64", "v1.2.3", extraEntry)
	other := ed25519.NewKeyFromSeed(make([]byte, ed25519.SeedSize)).Public().(ed25519.PublicKey)
	for _, tc := range []struct {
		name    string
		keys    []ed25519.PublicKey
		role    string
		arch    string
		version string
		a       Artifacts
		want    string
	}{
		{"valid", testKeys(), "agent", "amd64", "v1.2.3", good, ""},
		{"untrusted_key", []ed25519.PublicKey{other}, "agent", "amd64", "v1.2.3", good, "does not verify"},
		{"no_trusted_keys", nil, "agent", "amd64", "v1.2.3", good, "does not verify"},
		{"signed_for_other_version", testKeys(), "agent", "amd64", "v1.2.3", otherVersion, "does not verify"},
		{"tampered_archive", testKeys(), "agent", "amd64", "v1.2.3", tamperedArchive, "SHA-256 mismatch"},
		{"sums_lack_asset", testKeys(), "agent", "amd64", "v1.2.3", otherAsset, "missing target asset"},
		{"archive_structure", testKeys(), "agent", "amd64", "v1.2.3", badStructure, "unexpected"},
		{"prerelease_version", testKeys(), "agent", "amd64", "v1.2.3-rc.1", good, "invalid stable version"},
		{"unknown_arch", testKeys(), "agent", "mips", "v1.2.3", good, "unsupported agent architecture"},
		{"empty_archive", testKeys(), "agent", "amd64", "v1.2.3", Artifacts{Sums: good.Sums, Signature: good.Signature}, "size limits"},
	} {
		bin, err := Accept(tc.keys, tc.role, tc.arch, tc.version, tc.a)
		if tc.want == "" {
			if err != nil || string(bin) != "binary" {
				t.Errorf("%s: bin=%q err=%v", tc.name, bin, err)
			}
			continue
		}
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: err=%v, want %q", tc.name, err, tc.want)
		}
	}
}

func TestAgentArchMatchesArchiveMatrix(t *testing.T) {
	for _, arch := range []string{"amd64", "arm64", "armv7", "386", "riscv64"} {
		if !AgentArch(arch) {
			t.Errorf("%s rejected", arch)
		}
	}
	for _, arch := range []string{"", "arm", "mips", "amd64/../x"} {
		if AgentArch(arch) {
			t.Errorf("%q accepted", arch)
		}
	}
}

```

- [ ] **Step 2: 运行确认失败**

Run: `cd <worktree> && go test -count=1 -run 'TestAccept|TestAgentArch' ./internal/update/ > <task-dir>/t2.log 2>&1; echo $?`
Expected: 非 0，`undefined: Artifacts` / `undefined: Accept`。

- [ ] **Step 3: 实现 Accept 与 AgentArch**

`internal/update/accept.go`：

```go
package update

import (
	"crypto/ed25519"
	"crypto/sha256"
	"errors"

	"github.com/xjetry/heron-probe/internal/releasesig"
)

// Artifacts 是来源取回的三份原始字节。来源只负责取回，接受与否只由 Accept 判定（spec §4.10）。
type Artifacts struct {
	Sums      []byte
	Signature []byte
	Archive   []byte
}

// Accept 是在线更新唯一的接受规则：hub 与 agent 两种角色、GitHub 与 hub 两种来源都只经过它，
// 判定只在一处，就没有哪条来源会漏掉其中一步。顺序：版本合法 → 以调用方持有的版本验签 → 归档摘要在已验签
// 清单里 → 归档结构检查。version 必须是调用方自己的任务版本：签名绑定版本号，来源声称的版本不参与判定。
// "新于本机当前版本"不在这里：Accept 不知道本机版本，它由 Engine.submit 在接收任务时裁决。
func Accept(keys []ed25519.PublicKey, role, arch, version string, a Artifacts) ([]byte, error) {
	asset, err := archiveName(role, arch)
	if err != nil {
		return nil, err
	}
	if !ValidVersion(version) {
		return nil, errors.New("invalid stable version")
	}
	if len(a.Sums) == 0 || len(a.Sums) > releasesig.MaxSums || len(a.Archive) == 0 || len(a.Archive) > maxArchive {
		return nil, errors.New("release artifacts are empty or exceed size limits")
	}
	if err := releasesig.Verify(keys, version, a.Sums, a.Signature); err != nil {
		return nil, err
	}
	want, err := checksum(a.Sums, asset)
	if err != nil {
		return nil, err
	}
	if sha256.Sum256(a.Archive) != want {
		return nil, errors.New("release asset SHA-256 mismatch")
	}
	return extractBinary(a.Archive, role)
}

// AgentArch 判定 arch 是否在 agent 产物矩阵里；与拼资产名共用 archiveName 那一张表。
func AgentArch(arch string) bool {
	_, err := archiveName("agent", arch)
	return err == nil
}
```

- [ ] **Step 4: 改 OfficialSource：删 Download，加 Fetch**

`internal/update/release.go`：
- 常量块删 `maxSums`，引用处改 `releasesig.MaxSums`（`checksum` 不受影响；`extractBinary` 里的 `maxSums` 是服务文件的单条上限，改名为 `maxServiceFile = 1 << 20` 并在原处使用，避免与清单上限同名而语义不同）。
- 删除 `Download` 方法整段。`release()` 与 `Latest()` 保留（hub 的最新版本查询仍走 API）。
- 新增：

```go
// Fetch 从固定官方下载目录取回某版本的清单、签名与归档，不做任何接受判定（见 Accept）。
// 地址只由固定仓库、版本与本地产物矩阵拼出，不调 releases API：草稿 release 没有公开下载地址，
// 预发布 tag 过不了 ValidVersion，资产是否存在由下载结果本身回答。task 只用到版本。
func (s *OfficialSource) Fetch(ctx context.Context, task Request, role, arch string) (Artifacts, error) {
	asset, err := archiveName(role, arch)
	if err != nil {
		return Artifacts{}, err
	}
	if !ValidVersion(task.Version) {
		return Artifacts{}, errors.New("invalid stable version")
	}
	base := officialDownloads + task.Version + "/"
	var a Artifacts
	if a.Sums, err = s.get(ctx, base+"SHA256SUMS", "application/octet-stream", releasesig.MaxSums); err != nil {
		return Artifacts{}, err
	}
	if a.Signature, err = s.get(ctx, base+"SHA256SUMS.sig", "application/octet-stream", releasesig.MaxFile); err != nil {
		return Artifacts{}, err
	}
	if a.Archive, err = s.get(ctx, base+asset, "application/octet-stream", maxArchive); err != nil {
		return Artifacts{}, err
	}
	return a, nil
}
```

- 更新 `OfficialSource` 的类型注释：只接受官方仓库的产物地址；接受判定在 Accept。

- [ ] **Step 5: 改 Engine**

`internal/update/engine.go`：

```go
// downloadTimeout 是一次取回的总时限，GitHub 与 hub 两种来源相同。
const downloadTimeout = 5 * time.Minute

type source interface {
	Fetch(context.Context, Request, string, string) (Artifacts, error)
}

// sourceChoice 是更新器启动时按本机安装参数选定的取产物来源（spec §4.10）。
type sourceChoice struct {
	// name 是 "github" 或 "hub"，随状态上报（UpdateStatus.source）。
	name string
	src  source
	// err 非空表示来源配置读不出：更新器照常运行、回答状态，但不支持更新，原因写进状态。
	// 不让进程退出——崩溃循环在面板上只表现为"没有上报更新能力"，看不出原因。
	err string
}
```

- `Engine` 字段：`source source` 改为 `choice sourceChoice`，新增 `keys []ed25519.PublicKey`。
- `newEngine(ctx, path, role, arch string, choice sourceChoice, keys []ed25519.PublicKey, m machine)`，赋值到字段。
- `status()`：构造 `s` 后设 `s.Source = e.choice.name`；在 `recoveryError` 判断之后加：

```go
	if e.choice.err != "" && e.recoveryError == "" {
		s.Supported, s.Reason = false, e.choice.err
	}
```

- `submit()`：在 `recoveryError` 检查之后加 `if e.choice.err != "" { return Job{}, errors.New(e.choice.err) }`。
- `execute()` 的下载段改为：

```go
	downloadCtx, cancelDownload := context.WithTimeout(e.ctx, downloadTimeout)
	a, err := e.choice.src.Fetch(downloadCtx, j.Request, e.role, e.arch)
	cancelDownload()
	if err != nil {
		return err
	}
	data, err := Accept(e.keys, e.role, e.arch, j.Version, a)
	if err != nil {
		return err
	}
```

`internal/update/protocol.go`：`Status` 增加 `Source string \`json:"source,omitempty"\``（注释：本机取产物来源，github 或 hub）；把 `trailingJSON` 从 `system_linux.go` 原样移到本文件末尾（`system_linux.go` 删除该函数），它将被平台无关的来源配置解析共用。

`internal/update/system_linux.go`：

```go
func Serve(ctx context.Context, role string) error {
	return serve(ctx, role, NewOfficialSource(), releasesig.Trusted())
}

// serve 的来源与公钥由调用方给出：正式入口只传固定官方源与 releasesig 的常量公钥，
// 隔离验收的测试程序传受控来源与测试公钥（accept_linux_test.go）。
func serve(ctx context.Context, role string, official source, keys []ed25519.PublicKey) error {
```

在 `newEngine` 调用处传 `sourceChoice{name: "github", src: official}` 与 `keys`。

- [ ] **Step 6: 改测试夹具**

`internal/update/engine_test.go`：

```go
type fakeSource struct {
	err error
	a   Artifacts
}

func (s fakeSource) Fetch(context.Context, Request, string, string) (Artifacts, error) { return s.a, s.err }

// goodSource 给出 request() 版本的 hub amd64 合法签名产物。
func goodSource() fakeSource {
	return fakeSource{a: signedArtifacts("hub", "amd64", "v0.3.0", sigtest.Archive("hub", []byte("binary")))}
}
```

`testEngine` 改为 `newEngine(context.Background(), …, "hub", "amd64", sourceChoice{name: "github", src: s}, testKeys(), m)`；所有 `newEngine(...)` 调用同样补两个参数。凡是会走到下载的用例（任务真正执行的）传 `goodSource()`，原来传 `fakeSource{err: …}` 的保持。新增：

```go
func TestEngineRejectsArtifactsSignedForAnotherVersion(t *testing.T) {
	m := &fakeMachine{version: "v0.2.0"}
	s := fakeSource{a: signedArtifacts("hub", "amd64", "v0.2.9", sigtest.Archive("hub", []byte("binary")))}
	e := testEngine(t, m, s)
	if _, err := e.submit(request()); err != nil {
		t.Fatal(err)
	}
	j := waitEngine(t, e)
	if j.State != "failed" || !strings.Contains(j.Error, "does not verify") {
		t.Fatalf("job = %+v", j)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if slices.Contains(m.calls, "stage") || slices.Contains(m.calls, "stop") {
		t.Fatalf("rejected artifacts reached the machine: %v", m.calls)
	}
}

func TestEngineReportsSource(t *testing.T) {
	e := testEngine(t, &fakeMachine{version: "v0.2.0"}, goodSource())
	if s := e.status(); s.Source != "github" || !s.Supported {
		t.Fatalf("status = %+v", s)
	}
}
```

（`waitEngine` 返回失败任务时的确切 State 值以现有 `run()` 的失败路径为准：先读 `TestEngineFailureRecovery` 确认失败终态字符串，若是 `failed` 以外的值，按实际改断言并在 result.md 记录。）

`internal/update/release_test.go`：
- `releaseRoutes` 改为只登记三条下载地址（`…/download/<v>/SHA256SUMS`、`…/SHA256SUMS.sig`、`…/<asset>`），用 `signedArtifacts` 生成内容；`testSource` 对未登记地址仍 `t.Fatalf`，因此任何 API 请求都会让用例失败。
- `TestOfficialDownload` 改为 `TestOfficialFetchUsesOnlyDownloadURLs`：断言 `Fetch` 返回的三份字节与登记的一致。
- `TestOfficialRejectsReleaseMetadata` 中针对 `Download` 的草稿 / 预发布 / 资产清单断言删除（这些判定已不在接受路径上）；若其中有针对 `Latest` 的断言则保留。
- `TestOfficialChecksum`、`TestOfficialArchiveStructure`、`TestOfficialArchiveSizeLimits` 改为直接调 `Accept`（签名用 `sigtest`），断言的错误文本按新实现调整；`TestOfficialHTTPBoundaries` 改调 `Fetch`。
- `TestOfficialRejectsInputBeforeNetwork` 改调 `Fetch`。

`internal/update/accept_linux_test.go`：
- `acceptanceSource` 改为：

```go
// 受控产物只链接进测试程序，发行更新器没有读取测试目录或环境变量的路径。夹具目录里仍是裸二进制，
// 这里现场打成归档并用测试私钥签名；serve 收到的是测试公钥，正式入口 Serve 只传 releasesig 的常量公钥。
func (acceptanceSource) Fetch(_ context.Context, task Request, role, arch string) (Artifacts, error) {
	if !ValidVersion(task.Version) || (role != "hub" && role != "agent") {
		return Artifacts{}, errors.New("invalid fixture")
	}
	b, err := os.ReadFile(filepath.Join("/var/lib/heron-update-fixtures", task.Version, role))
	if err != nil {
		return Artifacts{}, err
	}
	return signedArtifacts(role, arch, task.Version, sigtest.Archive(role, b)), nil
}
```

- `TestSystemDaemon` 调 `serve(context.Background(), os.Getenv("HERON_UPDATE_ROLE"), acceptanceSource{}, testKeys())`。
- `TestOfficialNetworkAcceptance`：改为取 `Latest` 后对 hub 与 agent 各调 `Fetch` 再 `Accept(releasesig.Trusted(), …)`，注释写明"要求最新正式版已带签名"。

`signedArtifacts` 与 `testKeys` 定义在 accept_test.go（无 build tag），linux 专属测试可直接用。

- [ ] **Step 7: 写依赖检查**

`internal/update/deps_test.go`：

```go
package update

import (
	"os"
	"os/exec"
	"slices"
	"strings"
	"testing"
)

const sigtestPkg = "github.com/xjetry/heron-probe/internal/releasesig/sigtest"

func goList(t *testing.T, args ...string) []string {
	t.Helper()
	cmd := exec.Command("go", append([]string{"list"}, args...)...)
	cmd.Env = append(os.Environ(), "GOOS=linux", "GOARCH=amd64", "CGO_ENABLED=0")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("go list %v: %v\n%s", args, err, out)
	}
	return strings.Fields(string(out))
}

// 测试公钥只经构造参数进入测试程序（spec §4.10）；正式二进制链接 sigtest 就等于多了一把受信钥匙的来源。
// 测试依赖里必须看得见它，否则这条检查对"列表为空"与"确实没有"分不出来。
func TestProductionBinariesDoNotLinkSigtest(t *testing.T) {
	if !slices.Contains(goList(t, "-deps", "-test", "github.com/xjetry/heron-probe/internal/update"), sigtestPkg) {
		t.Fatal("update tests do not depend on sigtest; the production check below would pass vacuously")
	}
	for _, pkg := range []string{"github.com/xjetry/heron-probe/cmd/updater", "github.com/xjetry/heron-probe/cmd/agent", "github.com/xjetry/heron-probe/cmd/hub"} {
		if slices.Contains(goList(t, "-deps", pkg), sigtestPkg) {
			t.Fatalf("%s links %s", pkg, sigtestPkg)
		}
	}
}
```

- [ ] **Step 8: 全包测试、linux vet 与缺陷注入**

Run: `cd <worktree> && go test -count=1 ./internal/update/ ./internal/releasesig/... > <task-dir>/t2.log 2>&1; echo $?` → `0`
Run: `cd <worktree> && GOOS=linux go vet ./internal/update/ ./cmd/updater/ > <task-dir>/t2vet.log 2>&1; echo $?` → `0`（`system_linux.go`、`accept_linux_test.go` 只有 linux 构建看得见）
Run: `cd <worktree> && go build ./... > <task-dir>/t2build.log 2>&1; echo $?` → `0`

缺陷注入（各自记入 result.md）：
1. `Accept` 里 `releasesig.Verify(keys, version, …)` 的 `version` 换成常量 `"v0.0.0"` → `TestAccept/valid` 红。
2. `execute` 跳过 `Accept`、直接 `extractBinary(a.Archive, e.role)` → `TestEngineRejectsArtifactsSignedForAnotherVersion` 红。
3. `status()` 删掉 `s.Source = e.choice.name` → `TestEngineReportsSource` 红。
4. 在 `cmd/updater/main.go` 临时 `import _ "github.com/xjetry/heron-probe/internal/releasesig/sigtest"` → `TestProductionBinariesDoNotLinkSigtest` 红。

- [ ] **Step 9: 提交**

```bash
cd <worktree> && git add internal/update
git commit -m "feat(update): 更新器只按发行签名接受产物，GitHub 来源不再调 releases API"
```

---

### Task 3: agent 配置与 hub 客户端抽为共用包

**Files:**
- Create: `internal/agentconfig/config.go`、`internal/agentconfig/config_test.go`
- Create: `internal/hubclient/client.go`、`internal/hubclient/client_test.go`
- Modify: `internal/agent/client/config.go`（只留 `LoadConfig`、`Validate`、`Policy`）、`internal/agent/client/config_test.go`
- Delete: `internal/agent/client/transport.go`、`internal/agent/client/transport_test.go`（内容迁到 hubclient）
- Modify: `cmd/agent/main.go`、`internal/hub/ingest/ingest_test.go` 及 `git grep -n 'NewServiceClient\|client\.Config\b\|ReadConfig\|SaveConfig\|CheckHub\|\.Policy()\|\.Validate()'` 找到的全部调用点

**Interfaces:**
- Produces（`package agentconfig`）：
  - `type Config struct { Hub, Token, Name string; InsecureHTTP bool; ProbeAllow, ProbeDeny []string }`（JSON 标签与现状一致）
  - `func Decode(r io.Reader, name string) (Config, error)`
  - `func Read(path string) (Config, error)`
  - `func CheckHub(raw string, insecure bool) error`
  - `func Save(path string, c Config) error`
- Produces（`package hubclient`）：
  - `func New(hub string, timeout time.Duration, maxBody int64) heronv1connect.AgentServiceClient`
- Produces（`package client`，保留）：`func LoadConfig(path string) (agentconfig.Config, error)`、`func Validate(c agentconfig.Config) error`、`func Policy(c agentconfig.Config) (prober.Policy, error)`

背景：`internal/agent/client` 已 import `internal/update`，更新器不能反过来 import `client`；共用部分必须在独立的包里，且 `agentconfig` 不能 import `prober`（它会把 x/net 与探测代码带进 root 更新器）。

- [ ] **Step 1: 迁移 agentconfig**

把 `internal/agent/client/config.go` 里的 `Config`、`ReadConfig` 的解析部分、`CheckHub`、`SaveConfig` 移到 `internal/agentconfig/config.go`，包注释：

```go
// Package agentconfig 是 agent 配置文件的格式与 hub 地址规则。agent（register、run、configure）与节点上的
// root 更新器（hub 来源，spec §4.10）读同一份文件，解析与地址规则只在这里定义一次——更新器没有自己的一套。
// 本包不依赖探测策略的解析（prober）：那会把探测代码带进 root 更新器；策略校验留在 agent 的 client 包。
package agentconfig
```

`Decode` 是原 `ReadConfig` 的解码逻辑，输入换成 `io.Reader`：

```go
// Decode 严格解析一份配置：未知字段与第一个对象之后的任何内容都是错误（spec §4.8）——策略字段拼错时若静默忽略，
// 宿主机以为拒绝了的地址实际放行。name 只用于报错。
func Decode(r io.Reader, name string) (Config, error) {
	var c Config
	dec := json.NewDecoder(r)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&c); err != nil {
		return c, fmt.Errorf("%s: %w", name, err)
	}
	if err := dec.Decode(&json.RawMessage{}); !errors.Is(err, io.EOF) {
		return c, fmt.Errorf("%s: unexpected content after the configuration object", name)
	}
	return c, nil
}

// Read 读文件并 Decode，不校验，供 configure 修正一份当前不合规的配置。
func Read(path string) (Config, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return Config{}, err
	}
	return Decode(bytes.NewReader(b), path)
}
```

`CheckHub`、`Save`（原 `SaveConfig`）函数体原样迁移，注释里"register 在发请求之前、run 在加载配置时调用"补上"hub 来源的更新器在发 GetRelease 之前调用"。

`internal/agent/client/config.go` 只剩：

```go
// LoadConfig 读取并校验配置，run 只经过它。
func LoadConfig(path string) (agentconfig.Config, error) {
	c, err := agentconfig.Read(path)
	if err != nil {
		return c, err
	}
	return c, Validate(c)
}

// Validate 是 run 加载配置时的完整校验：hub 地址规则加本地探测策略。
func Validate(c agentconfig.Config) error {
	if err := agentconfig.CheckHub(c.Hub, c.InsecureHTTP); err != nil {
		return err
	}
	_, err := Policy(c)
	return err
}

func Policy(c agentconfig.Config) (prober.Policy, error) { return prober.ParsePolicy(c.ProbeAllow, c.ProbeDeny) }
```

调用点：`cmd/agent/main.go` 的 `client.CheckHub` → `agentconfig.CheckHub`，`client.Config` → `agentconfig.Config`，`client.ReadConfig` → `agentconfig.Read`，`client.SaveConfig` → `agentconfig.Save`，`cfg.Policy()` → `client.Policy(cfg)`，`cfg.Validate()` → `client.Validate(cfg)`。

测试：`TestCheckHub`、`TestSaveConfigKeepsOwner` 以及 `TestLoadConfigValidates` 中只涉及解析的用例（`typo_field`、`second_object` 等）迁到 `internal/agentconfig/config_test.go` 改调 `Read`；涉及探测策略的用例留在 client 包继续调 `LoadConfig`。新增 `Decode` 直接读 reader 的用例：

```go
func TestDecodeReportsName(t *testing.T) {
	_, err := Decode(strings.NewReader(`{"hub":"https://h","token":"t","bogus":1}`), "/etc/heron-agent/config.json")
	if err == nil || !strings.Contains(err.Error(), "/etc/heron-agent/config.json") || !strings.Contains(err.Error(), `unknown field "bogus"`) {
		t.Fatalf("err = %v", err)
	}
}
```

- [ ] **Step 2: 迁移 hubclient**

`internal/hubclient/client.go` 承接 `transport.go` 的全部内容，构造函数改为带正文上限参数：

```go
// Package hubclient 构造连 hub 的 AgentService 客户端。agent（register、run）与节点上的 root 更新器
// （hub 来源，spec §4.10）共用这一个构造，下面几条约束因此对两者同时成立（spec §5.7）：
//   - 响应正文在 HTTP 层限读 maxBody 字节，成功与错误响应都经过这一层。connect-go 的 ReadMaxBytes 只管成功
//     响应的消息，错误正文与解压后的大小都不受它约束。
//   - 不声明压缩：hub 没法用一个小的压缩正文换出大块内存；hub 不顾声明仍回 gzip 时 connect 按不认识的编码报错。
//   - 不跟随重定向。http.Client 跟随同主机重定向时保留 Authorization 而不看协议，https 到同主机 http 的重定向
//     会把节点 token 明文发出；AgentService 没有需要重定向的场景。
//   - 响应头上限 32 KiB。
// 其余传输设置克隆 http.DefaultTransport（代理取自进程环境）。
package hubclient

func New(hub string, timeout time.Duration, maxBody int64) heronv1connect.AgentServiceClient {
	tr := http.DefaultTransport.(*http.Transport).Clone()
	tr.DisableCompression = true
	tr.MaxResponseHeaderBytes = maxResponseHeaderBytes
	hc := &http.Client{
		Timeout:       timeout,
		Transport:     limitedTransport{base: tr, max: maxBody},
		CheckRedirect: refuseRedirect,
	}
	return heronv1connect.NewAgentServiceClient(hc, hub, connect.WithAcceptCompression("gzip", nil, nil))
}
```

`errResponseTooLarge` 原是包级变量（消息里带固定上限）；改为 `limitedBody` 持有 `max` 并在超限时返回 `fmt.Errorf("hub response body exceeds %d bytes", max)`。`limitedTransport` 把 `t.max` 传给 `limitedBody`。

`transport_test.go` 迁为 `internal/hubclient/client_test.go`，`NewServiceClient(srv.URL, d)` 改为 `New(srv.URL, d, 64<<10)`，比较 `errResponseTooLarge.Error()` 的地方改为比较 `"hub response body exceeds 65536 bytes"`。新增一条确认上限确实是参数：

```go
func TestClientBodyLimitIsAParameter(t *testing.T) {
	srv := httptest.NewServer(protoResponder(t, oversizedResponse(), false))
	defer srv.Close()
	if err := report(t, New(srv.URL, 5*time.Second, 1<<30)); err != nil {
		t.Fatalf("a client with a larger limit rejected the response: %v", err)
	}
}
```

（`oversizedResponse()` 的大小以原文件为准，需确认它在 1 GiB 以内且超过 64 KiB。）

调用点：`cmd/agent/main.go` 两处 `client.NewServiceClient(x, requestTimeout)` → `hubclient.New(x, requestTimeout, agentwire.MaxResponseBytes)`；`internal/hub/ingest/ingest_test.go` 的 `agentclient.NewServiceClient(srv.URL, 5*time.Second)` → `hubclient.New(srv.URL, 5*time.Second, agentwire.MaxResponseBytes)`；其余 `git grep -n NewServiceClient` 命中逐个改。

- [ ] **Step 3: 全量测试与缺陷注入**

Run: `cd <worktree> && go build ./... > <task-dir>/t3.log 2>&1 && go test -count=1 ./internal/agentconfig/ ./internal/hubclient/ ./internal/agent/... ./cmd/agent/ ./internal/hub/ingest/ >> <task-dir>/t3.log 2>&1; echo $?`
Expected: `0`。

缺陷注入：
1. `limitedTransport.RoundTrip` 里改用常量 `64<<10` 而不是 `t.max` → `TestClientBodyLimitIsAParameter` 红。
2. `Decode` 删 `DisallowUnknownFields` → `TestDecodeReportsName` 红。

- [ ] **Step 4: 提交**

```bash
cd <worktree> && git add -A internal/agentconfig internal/hubclient internal/agent cmd/agent internal/hub/ingest
git commit -m "refactor(agent): 配置解析与连 hub 客户端抽为共用包，正文上限改为参数"
```

---

### Task 4: proto 增加 GetRelease 与 UpdateStatus.source

**Files:**
- Modify: `proto/heron/v1/agent.proto`、`proto/heron/v1/update.proto`
- Regenerate: `gen/`、`web/src/gen/`（`make gen`）
- Modify: `internal/agent/client/client_test.go`（`fakeHub` 补方法以通过编译）
- Modify: `internal/hub/ingest/service.go`（临时让 `Service` 满足接口：见 Step 3）

**Interfaces:**
- Produces: `heronv1.GetReleaseRequest{TaskId, Arch}`、`heronv1.GetReleaseResponse{Sums, Signature, Archive}`、`heronv1connect.AgentServiceGetReleaseProcedure`、`AgentServiceClient.GetRelease`、`heronv1.UpdateStatus.Source`、TS 侧 `UpdateStatus.source`。

- [ ] **Step 1: 改 proto**

`proto/heron/v1/agent.proto`：服务注释"两个方法都是 unary"改为"全部方法都是 unary"；在 `Report` 之后加：

```proto
  // 节点上 hub 来源的 root 更新器取产物（spec §4.10）。鉴权用节点 token（Authorization: Bearer）。
  // 请求里没有版本：hub 取该节点当前更新任务的版本，泄漏的 token 只能取到该节点正在执行的那一个版本。
  // 响应是官方原始字节；hub 已预先验签，但接受与否只由更新器验签决定。不标无副作用：只接受 POST。
  rpc GetRelease(GetReleaseRequest) returns (GetReleaseResponse);
```

文件末尾加：

```proto
message GetReleaseRequest {
  // 节点当前更新任务的 ID。
  string task_id = 1;
  // agent 产物矩阵里的架构：amd64、arm64、armv7、386、riscv64。
  string arch = 2;
}

message GetReleaseResponse {
  // 官方 SHA256SUMS 原文。
  bytes sums = 1;
  // 官方 SHA256SUMS.sig 原文。
  bytes signature = 2;
  // 官方归档原文。
  bytes archive = 3;
}
```

`proto/heron/v1/update.proto` 的 `UpdateStatus` 加：

```proto
  // 本机更新器取产物的来源：github 或 hub（spec §4.10）。不认识来源字段的旧更新器为空。
  string source = 5;
```

- [ ] **Step 2: 生成**

Run: `cd <worktree> && make gen > <task-dir>/t4gen.log 2>&1; echo $?` → `0`；`git status --short gen web/src/gen` 应列出 agent 与 update 的生成文件。

- [ ] **Step 3: 让实现方编译通过**

`internal/agent/client/client_test.go` 的 `fakeHub` 加：

```go
func (f *fakeHub) GetRelease(context.Context, *connect.Request[heronv1.GetReleaseRequest]) (*connect.Response[heronv1.GetReleaseResponse], error) {
	return nil, connect.NewError(connect.CodeUnimplemented, nil)
}
```

（接收者类型以 `fakeHub` 现有方法为准。）`internal/hub/ingest/service.go` 的 `Service` 加同签名方法，返回 `connect.NewError(connect.CodeUnavailable, errors.New("release relay is not configured"))`——Task 5 会把它替换为真实实现；它在拦截器里尚未登记，匿名与带 token 的调用都先被拦截器以 `Unauthenticated` 拒绝，`TestEveryProcedureRejectsAnonymousCalls` 与 `cmd/hub` 的 `TestMuxRejectsAnonymousProcedures` 自动覆盖新方法。

- [ ] **Step 4: 验证并提交**

Run: `cd <worktree> && go build ./... > <task-dir>/t4.log 2>&1 && go test -count=1 ./internal/hub/ingest/ ./cmd/hub/ ./internal/agent/... >> <task-dir>/t4.log 2>&1 && make lint >> <task-dir>/t4.log 2>&1; echo $?` → `0`（`make lint` 含 `buf lint`）。

```bash
cd <worktree> && git add proto gen web/src/gen internal/agent/client/client_test.go internal/hub/ingest/service.go
git commit -m "feat(proto): AgentService 增加 GetRelease，UpdateStatus 增加 source"
```

本提交只含 proto、生成物与编译所需的最小实现，之后不改写历史：Task 5、6、8 的分支从它开出。

---

### Task 5: hub 中转

**Files:**
- Create: `internal/hub/updates/relay.go`、`internal/hub/updates/relay_test.go`
- Modify: `internal/hub/updates/manager.go`（`ActiveTasks`）、`internal/hub/updates/manager_test.go`
- Modify: `internal/hub/ingest/service.go`（`Config.Releases`、拦截器、`GetRelease`）、`internal/hub/ingest/release_test.go`（新建）
- Modify: `cmd/hub/serve.go`（装配）

**Interfaces:**
- Consumes: `update.Artifacts`、`update.Accept`、`update.AgentArch`、`update.ActiveState`、`(*update.OfficialSource).Fetch`、`releasesig.Trusted`、`sigtest`（测试）。
- Produces（`package updates`）：
  - `func (m *Manager) ActiveTasks() map[string]string`（任务 ID → 版本）
  - `type Relay struct{...}`、`func NewRelay(tasks RelayTasks, fetch FetchFunc, verify VerifyFunc, clk clock.Clock) *Relay`
  - `type RelayTasks interface { Snapshot(int64) *heronv1.UpdateStatus; ActiveTasks() map[string]string }`
  - `type FetchFunc func(ctx context.Context, version, arch string) (update.Artifacts, error)`
  - `type VerifyFunc func(version, arch string, a update.Artifacts) error`
  - `func (r *Relay) Get(ctx context.Context, node int64, taskID, arch string) (update.Artifacts, error)`
  - `func (r *Relay) Sweep()`、`func (r *Relay) Run(ctx context.Context)`
  - 错误：`ErrArch`、`ErrNoTask`、`ErrBusy`、`ErrAttempts`、`ErrCacheFull`
  - 常量：`MaxAttemptsPerTask = 3`、`MaxCacheBytes = 256 << 20`、`fetchTimeout = 5 * time.Minute`
- Produces（`package ingest`）：`Config.Releases interface{ Get(ctx context.Context, node int64, taskID, arch string) (update.Artifacts, error) }`

- [ ] **Step 1: Manager.ActiveTasks 的失败测试与实现**

`manager_test.go` 追加（夹具按该文件现有的建 Manager 与节点方式写；需要一个 `dispatched`、一个 `succeeded` 的任务）：

```go
func TestActiveTasksListsOnlyActiveStates(t *testing.T) {
	m := New(nil, clock.NewFake(time.Unix(1000, 0)), slog.Default())
	m.states = map[int64]*heronv1.UpdateStatus{
		1: {Task: &heronv1.UpdateTask{Id: "aaaaaaaaaaaaaaaa", Version: "v1.0.0", State: "dispatched"}},
		2: {Task: &heronv1.UpdateTask{Id: "bbbbbbbbbbbbbbbb", Version: "v1.0.0", State: "succeeded"}},
		3: {Task: &heronv1.UpdateTask{Id: "cccccccccccccccc", Version: "v1.1.0", State: "downloading"}},
		4: {},
	}
	got := m.ActiveTasks()
	want := map[string]string{"aaaaaaaaaaaaaaaa": "v1.0.0", "cccccccccccccccc": "v1.1.0"}
	if !maps.Equal(got, want) {
		t.Fatalf("ActiveTasks = %v, want %v", got, want)
	}
}
```

实现（`manager.go`）：

```go
// ActiveTasks 返回仍在进行中的节点任务（ID → 版本）。中转缓存据此按引用释放：版本不再被任何进行中的任务
// 引用即可丢弃；任务的取用计数也只在任务进行中才有意义。
func (m *Manager) ActiveTasks() map[string]string {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make(map[string]string)
	for _, s := range m.states {
		if t := s.GetTask(); t != nil && update.ActiveState(t.State) {
			out[t.Id] = t.Version
		}
	}
	return out
}
```

- [ ] **Step 2: Relay 的失败测试**

`internal/hub/updates/relay_test.go`：

```go
package updates

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	heronv1 "github.com/xjetry/heron-probe/gen/heron/v1"
	"github.com/xjetry/heron-probe/internal/clock"
	"github.com/xjetry/heron-probe/internal/update"
)

type fakeTasks struct {
	mu     sync.Mutex
	status map[int64]*heronv1.UpdateStatus
}

func (f *fakeTasks) Snapshot(id int64) *heronv1.UpdateStatus {
	f.mu.Lock()
	defer f.mu.Unlock()
	if s := f.status[id]; s != nil {
		return s
	}
	return &heronv1.UpdateStatus{}
}

func (f *fakeTasks) ActiveTasks() map[string]string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := map[string]string{}
	for _, s := range f.status {
		if t := s.GetTask(); t != nil && update.ActiveState(t.State) {
			out[t.Id] = t.Version
		}
	}
	return out
}

func (f *fakeTasks) set(node int64, id, version, state string, expires int64) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.status[node] = &heronv1.UpdateStatus{Task: &heronv1.UpdateTask{Id: id, Version: version, State: state, ExpiresAt: expires}}
}

const (
	taskA = "aaaaaaaaaaaaaaaa"
	taskB = "bbbbbbbbbbbbbbbb"
)

var now = time.Unix(10_000, 0)

func artifacts(n int) update.Artifacts {
	return update.Artifacts{Sums: []byte("s"), Signature: []byte("g"), Archive: make([]byte, n)}
}

type harness struct {
	tasks   *fakeTasks
	fetches atomic.Int32
	relay   *Relay
}

func newHarness(t *testing.T, fetch FetchFunc, verify VerifyFunc) *harness {
	t.Helper()
	h := &harness{tasks: &fakeTasks{status: map[int64]*heronv1.UpdateStatus{}}}
	if fetch == nil {
		fetch = func(context.Context, string, string) (update.Artifacts, error) { return artifacts(10), nil }
	}
	if verify == nil {
		verify = func(string, string, update.Artifacts) error { return nil }
	}
	counted := func(ctx context.Context, v, a string) (update.Artifacts, error) {
		h.fetches.Add(1)
		return fetch(ctx, v, a)
	}
	h.relay = NewRelay(h.tasks, counted, verify, clock.NewFake(now))
	return h
}

func TestRelayServesDispatchedAndDownloadingTasks(t *testing.T) {
	for _, state := range []string{"dispatched", "downloading"} {
		h := newHarness(t, nil, nil)
		h.tasks.set(1, taskA, "v1.0.0", state, now.Unix()+60)
		a, err := h.relay.Get(context.Background(), 1, taskA, "amd64")
		if err != nil || len(a.Archive) != 10 {
			t.Fatalf("%s: a=%v err=%v", state, a, err)
		}
	}
}

func TestRelayRejectsWithoutMatchingLiveTask(t *testing.T) {
	for name, set := range map[string]func(*fakeTasks){
		"no_task":   func(*fakeTasks) {},
		"queued":    func(f *fakeTasks) { f.set(1, taskA, "v1.0.0", "queued", now.Unix()+60) },
		"succeeded": func(f *fakeTasks) { f.set(1, taskA, "v1.0.0", "succeeded", now.Unix()+60) },
		"unconfirmed": func(f *fakeTasks) { f.set(1, taskA, "v1.0.0", "unconfirmed", now.Unix()+60) },
		"expired":   func(f *fakeTasks) { f.set(1, taskA, "v1.0.0", "dispatched", now.Unix()) },
		"other_id":  func(f *fakeTasks) { f.set(1, taskB, "v1.0.0", "dispatched", now.Unix()+60) },
	} {
		h := newHarness(t, nil, nil)
		set(h.tasks)
		if _, err := h.relay.Get(context.Background(), 1, taskA, "amd64"); !errors.Is(err, ErrNoTask) {
			t.Errorf("%s: err = %v, want ErrNoTask", name, err)
		}
		if h.fetches.Load() != 0 {
			t.Errorf("%s: fetched without a matching task", name)
		}
	}
}

func TestRelayRejectsForeignTask(t *testing.T) {
	h := newHarness(t, nil, nil)
	h.tasks.set(2, taskB, "v1.0.0", "dispatched", now.Unix()+60)
	if _, err := h.relay.Get(context.Background(), 1, taskB, "amd64"); !errors.Is(err, ErrNoTask) {
		t.Fatalf("node 1 fetched node 2's task: %v", err)
	}
	if h.fetches.Load() != 0 {
		t.Fatal("a foreign task triggered a fetch")
	}
}

func TestRelayRejectsUnknownArch(t *testing.T) {
	h := newHarness(t, nil, nil)
	h.tasks.set(1, taskA, "v1.0.0", "dispatched", now.Unix()+60)
	if _, err := h.relay.Get(context.Background(), 1, taskA, "mips"); !errors.Is(err, ErrArch) {
		t.Fatalf("err = %v", err)
	}
}

func TestRelayMergesConcurrentFetches(t *testing.T) {
	release := make(chan struct{})
	h := newHarness(t, func(context.Context, string, string) (update.Artifacts, error) {
		<-release
		return artifacts(10), nil
	}, nil)
	h.tasks.set(1, taskA, "v1.0.0", "dispatched", now.Unix()+60)
	h.tasks.set(2, taskB, "v1.0.0", "dispatched", now.Unix()+60)
	var wg sync.WaitGroup
	errs := make([]error, 2)
	for i, c := range []struct {
		node int64
		task string
	}{{1, taskA}, {2, taskB}} {
		wg.Add(1)
		go func() { defer wg.Done(); _, errs[i] = h.relay.Get(context.Background(), c.node, c.task, "amd64") }()
	}
	deadline := time.Now().Add(2 * time.Second)
	for h.fetches.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	time.Sleep(20 * time.Millisecond) // 给第二个请求进入等待的机会；合并与否由下面的计数判定，不靠这段时长
	close(release)
	wg.Wait()
	if errs[0] != nil || errs[1] != nil || h.fetches.Load() != 1 {
		t.Fatalf("errs=%v fetches=%d, want one shared fetch", errs, h.fetches.Load())
	}
}

func TestRelayOneInFlightPerNode(t *testing.T) {
	release := make(chan struct{})
	h := newHarness(t, func(context.Context, string, string) (update.Artifacts, error) {
		<-release
		return artifacts(10), nil
	}, nil)
	h.tasks.set(1, taskA, "v1.0.0", "dispatched", now.Unix()+60)
	done := make(chan error, 1)
	go func() { _, err := h.relay.Get(context.Background(), 1, taskA, "amd64"); done <- err }()
	for h.fetches.Load() == 0 {
		time.Sleep(time.Millisecond)
	}
	if _, err := h.relay.Get(context.Background(), 1, taskA, "amd64"); !errors.Is(err, ErrBusy) {
		t.Fatalf("second concurrent request: %v", err)
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestRelayLimitsAttemptsPerTask(t *testing.T) {
	h := newHarness(t, nil, nil)
	h.tasks.set(1, taskA, "v1.0.0", "dispatched", now.Unix()+60)
	for i := 0; i < MaxAttemptsPerTask; i++ {
		if _, err := h.relay.Get(context.Background(), 1, taskA, "amd64"); err != nil {
			t.Fatalf("attempt %d: %v", i+1, err)
		}
	}
	if _, err := h.relay.Get(context.Background(), 1, taskA, "amd64"); !errors.Is(err, ErrAttempts) {
		t.Fatalf("attempt %d: %v", MaxAttemptsPerTask+1, err)
	}
}

func TestRelayDoesNotCacheFailures(t *testing.T) {
	for name, h := range map[string]*harness{
		"fetch":  newHarness(t, func(context.Context, string, string) (update.Artifacts, error) { return update.Artifacts{}, errors.New("github unreachable") }, nil),
		"verify": newHarness(t, nil, func(string, string, update.Artifacts) error { return errors.New("bad signature") }),
	} {
		h.tasks.set(1, taskA, "v1.0.0", "dispatched", now.Unix()+60)
		for i := 0; i < 2; i++ {
			if _, err := h.relay.Get(context.Background(), 1, taskA, "amd64"); err == nil {
				t.Fatalf("%s: failure was served", name)
			}
		}
		if h.fetches.Load() != 2 {
			t.Errorf("%s: fetches = %d, want a fresh fetch per request", name, h.fetches.Load())
		}
	}
}

func TestRelayCacheCap(t *testing.T) {
	h := newHarness(t, func(_ context.Context, _ string, arch string) (update.Artifacts, error) { return artifacts(60), nil }, nil)
	h.relay.maxBytes = 100
	h.tasks.set(1, taskA, "v1.0.0", "dispatched", now.Unix()+60)
	h.tasks.set(2, taskB, "v1.0.0", "dispatched", now.Unix()+60)
	if _, err := h.relay.Get(context.Background(), 1, taskA, "amd64"); err != nil {
		t.Fatal(err)
	}
	if _, err := h.relay.Get(context.Background(), 2, taskB, "arm64"); !errors.Is(err, ErrCacheFull) {
		t.Fatalf("err = %v, want ErrCacheFull", err)
	}
	// 在用条目不被挤掉：同版本同架构的再次请求仍命中缓存。
	if _, err := h.relay.Get(context.Background(), 1, taskA, "amd64"); err != nil || h.fetches.Load() != 2 {
		t.Fatalf("err=%v fetches=%d", err, h.fetches.Load())
	}
}

func TestRelaySweepReleasesUnreferenced(t *testing.T) {
	h := newHarness(t, nil, nil)
	h.tasks.set(1, taskA, "v1.0.0", "dispatched", now.Unix()+60)
	if _, err := h.relay.Get(context.Background(), 1, taskA, "amd64"); err != nil {
		t.Fatal(err)
	}
	h.relay.Sweep()
	if h.relay.cachedBytes() == 0 {
		t.Fatal("swept a version still referenced by an active task")
	}
	h.tasks.set(1, taskA, "v1.0.0", "succeeded", now.Unix()+60)
	h.relay.Sweep()
	if h.relay.cachedBytes() != 0 || h.relay.attemptCount(taskA) != 0 {
		t.Fatalf("bytes=%d attempts=%d after the task ended", h.relay.cachedBytes(), h.relay.attemptCount(taskA))
	}
}
```

（`cachedBytes()`、`attemptCount()` 是只读的小方法，Step 3 一并实现，生产代码也不需要别的读法时就放在 `relay.go` 里、首字母小写。）

- [ ] **Step 3: 实现 Relay**

`internal/hub/updates/relay.go`：

```go
package updates

import (
	"context"
	"errors"
	"sync"
	"time"

	heronv1 "github.com/xjetry/heron-probe/gen/heron/v1"
	"github.com/xjetry/heron-probe/internal/clock"
	"github.com/xjetry/heron-probe/internal/update"
)

const (
	// MaxAttemptsPerTask 大于更新器每个任务的 1 次请求；它把持 token 者能消耗的 hub 出口带宽绑定到
	// 管理员创建的任务数上（任务只有管理员会话能建）。
	MaxAttemptsPerTask = 3
	// MaxCacheBytes 是缓存总字节上限，为单个归档上限（128 MiB）的两倍。
	MaxCacheBytes = 256 << 20
	fetchTimeout  = 5 * time.Minute
)

var (
	ErrArch      = errors.New("architecture is not in the agent artifact matrix")
	ErrNoTask    = errors.New("no matching update task in progress for this node")
	ErrBusy      = errors.New("this node already has a release download in progress")
	ErrAttempts  = errors.New("release download attempts for this task are exhausted")
	ErrCacheFull = errors.New("release cache is full")
)

type RelayTasks interface {
	Snapshot(int64) *heronv1.UpdateStatus
	ActiveTasks() map[string]string
}

type FetchFunc func(ctx context.Context, version, arch string) (update.Artifacts, error)
type VerifyFunc func(version, arch string, a update.Artifacts) error

// Relay 为 hub 来源的节点中转官方产物（spec §4.10）。它只转发：取回后预验签，让坏产物在 hub 处就报错、
// 不必每个节点各下一遍才失败；接受与否仍由节点上的更新器验签决定——失守的 hub 可以跳过这里，所以这里不是防线。
// 缓存只在内存，按引用释放（Sweep）；不落盘，没有崩溃残留要清理。
type Relay struct {
	tasks    RelayTasks
	fetch    FetchFunc
	verify   VerifyFunc
	clk      clock.Clock
	maxBytes int64

	mu       sync.Mutex
	inflight map[int64]bool
	attempts map[string]int
	cache    map[relayKey]*relayEntry
	bytes    int64
}

type relayKey struct{ version, arch string }

// relayEntry 在取回完成前 done 未关闭；完成后 a 与 err 只读。失败的条目在关闭 done 之前已从 cache 删除。
type relayEntry struct {
	done chan struct{}
	a    update.Artifacts
	err  error
	size int64
}

func NewRelay(tasks RelayTasks, fetch FetchFunc, verify VerifyFunc, clk clock.Clock) *Relay {
	return &Relay{tasks: tasks, fetch: fetch, verify: verify, clk: clk, maxBytes: MaxCacheBytes,
		inflight: map[int64]bool{}, attempts: map[string]int{}, cache: map[relayKey]*relayEntry{}}
}

// Get 只服务该节点当前进行中的那个任务：ID 相同、状态 dispatched 或 downloading、未过期，版本取自任务本身。
// hub 侧任务在更新器开始下载前是 dispatched，更新器报出 downloading 后推进（Manager.flush）；更新器本地的
// queued 不推进 hub 状态，所以这两个状态覆盖了下载期间 hub 能看到的全部取值。
func (r *Relay) Get(ctx context.Context, node int64, taskID, arch string) (update.Artifacts, error) {
	if !update.AgentArch(arch) {
		return update.Artifacts{}, ErrArch
	}
	t := r.tasks.Snapshot(node).GetTask()
	if t == nil || t.Id != taskID || (t.State != "dispatched" && t.State != "downloading") || t.ExpiresAt <= r.clk.Now().Unix() {
		return update.Artifacts{}, ErrNoTask
	}
	r.mu.Lock()
	if r.inflight[node] {
		r.mu.Unlock()
		return update.Artifacts{}, ErrBusy
	}
	if r.attempts[taskID] >= MaxAttemptsPerTask {
		r.mu.Unlock()
		return update.Artifacts{}, ErrAttempts
	}
	r.inflight[node] = true
	r.attempts[taskID]++
	k := relayKey{t.Version, arch}
	e := r.cache[k]
	if e == nil {
		e = &relayEntry{done: make(chan struct{})}
		r.cache[k] = e
		go r.load(k, e)
	}
	r.mu.Unlock()
	defer func() { r.mu.Lock(); delete(r.inflight, node); r.mu.Unlock() }()
	select {
	case <-e.done:
		return e.a, e.err
	case <-ctx.Done():
		return update.Artifacts{}, ctx.Err()
	}
}

// load 用独立的上下文取回：发起请求的节点断开不能让同键上等待的其他节点一起失败。
func (r *Relay) load(k relayKey, e *relayEntry) {
	ctx, cancel := context.WithTimeout(context.Background(), fetchTimeout)
	defer cancel()
	a, err := r.fetch(ctx, k.version, k.arch)
	if err == nil {
		err = r.verify(k.version, k.arch, a)
	}
	size := int64(len(a.Sums) + len(a.Signature) + len(a.Archive))
	r.mu.Lock()
	if err == nil && r.bytes+size > r.maxBytes {
		err = ErrCacheFull
	}
	if err != nil {
		delete(r.cache, k)
		e.err = err
	} else {
		e.a, e.size = a, size
		r.bytes += size
	}
	r.mu.Unlock()
	close(e.done)
}

// Sweep 释放不再被进行中任务引用的版本与已结束任务的取用计数。
func (r *Relay) Sweep() {
	active := r.tasks.ActiveTasks()
	versions := make(map[string]bool, len(active))
	for _, v := range active {
		versions[v] = true
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	for id := range r.attempts {
		if _, ok := active[id]; !ok {
			delete(r.attempts, id)
		}
	}
	for k, e := range r.cache {
		select {
		case <-e.done:
		default:
			continue
		}
		if !versions[k.version] {
			delete(r.cache, k)
			r.bytes -= e.size
		}
	}
}

func (r *Relay) Run(ctx context.Context) {
	t := time.NewTicker(time.Minute)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			r.Sweep()
		}
	}
}

func (r *Relay) cachedBytes() int64 { r.mu.Lock(); defer r.mu.Unlock(); return r.bytes }

func (r *Relay) attemptCount(id string) int { r.mu.Lock(); defer r.mu.Unlock(); return r.attempts[id] }
```

注意 `TestRelayCacheCap`：第二个架构的取回在 `load` 里因超限失败、条目被删，返回 `ErrCacheFull`；第一个条目仍在。

- [ ] **Step 4: Relay 测试通过 + race**

Run: `cd <worktree> && go test -count=1 -race ./internal/hub/updates/ > <task-dir>/t5a.log 2>&1; echo $?` → `0`。

- [ ] **Step 5: ingest 接入的失败测试**

`internal/hub/ingest/release_test.go`：

```go
package ingest

import (
	"context"
	"errors"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/proto"

	heronv1 "github.com/xjetry/heron-probe/gen/heron/v1"
	"github.com/xjetry/heron-probe/internal/hub/updates"
	"github.com/xjetry/heron-probe/internal/update"
)

type fakeReleases struct {
	node  atomic.Int64
	block chan struct{}
	err   error
}

func (f *fakeReleases) Get(ctx context.Context, node int64, taskID, arch string) (update.Artifacts, error) {
	f.node.Store(node)
	if f.block != nil {
		<-f.block
	}
	if f.err != nil {
		return update.Artifacts{}, f.err
	}
	return update.Artifacts{Sums: []byte(taskID), Signature: []byte(arch), Archive: []byte("archive")}, nil
}

func getRelease(tok string) *connect.Request[heronv1.GetReleaseRequest] {
	req := connect.NewRequest(&heronv1.GetReleaseRequest{TaskId: "aaaaaaaaaaaaaaaa", Arch: "amd64"})
	req.Header().Set("Authorization", "Bearer "+tok)
	return req
}

func TestGetReleaseUsesTheAuthenticatedNode(t *testing.T) {
	f := &fakeReleases{}
	h := newHubWith(t, filepath.Join(t.TempDir(), "t.db"), Config{TTL: 30 * time.Second, Releases: f})
	id, tok := h.node(t)
	resp, err := h.client.GetRelease(context.Background(), getRelease(tok))
	if err != nil {
		t.Fatal(err)
	}
	if f.node.Load() != id || string(resp.Msg.Sums) != "aaaaaaaaaaaaaaaa" || string(resp.Msg.Signature) != "amd64" || string(resp.Msg.Archive) != "archive" {
		t.Fatalf("node=%d resp=%v", f.node.Load(), resp.Msg)
	}
	if _, err := h.client.GetRelease(context.Background(), getRelease("bogus")); connect.CodeOf(err) != connect.CodeUnauthenticated {
		t.Fatalf("bad token: %v", err)
	}
}

func TestGetReleaseErrorCodes(t *testing.T) {
	for _, tc := range []struct {
		err  error
		code connect.Code
	}{
		{updates.ErrArch, connect.CodeInvalidArgument},
		{updates.ErrNoTask, connect.CodeFailedPrecondition},
		{updates.ErrBusy, connect.CodeResourceExhausted},
		{updates.ErrAttempts, connect.CodeResourceExhausted},
		{updates.ErrCacheFull, connect.CodeResourceExhausted},
		{errors.New("github unreachable"), connect.CodeUnavailable},
	} {
		h := newHubWith(t, filepath.Join(t.TempDir(), "t.db"), Config{TTL: 30 * time.Second, Releases: &fakeReleases{err: tc.err}})
		_, tok := h.node(t)
		_, err := h.client.GetRelease(context.Background(), getRelease(tok))
		if connect.CodeOf(err) != tc.code {
			t.Errorf("%v: code %v, want %v", tc.err, connect.CodeOf(err), tc.code)
		}
	}
}

func TestGetReleaseUnconfiguredIsUnavailable(t *testing.T) {
	h := newHub(t)
	_, tok := h.node(t)
	if _, err := h.client.GetRelease(context.Background(), getRelease(tok)); connect.CodeOf(err) != connect.CodeUnavailable {
		t.Fatalf("err = %v", err)
	}
}

// 一次下载可能持续数分钟；它若持有 stateMu 的读锁，Forget 会等它，而排队的写锁又会挡住此后全部 Report。
func TestGetReleaseDoesNotBlockForgetOrReport(t *testing.T) {
	f := &fakeReleases{block: make(chan struct{})}
	h := newHubWith(t, filepath.Join(t.TempDir(), "t.db"), Config{TTL: 30 * time.Second, Releases: f})
	slowID, slowTok := h.node(t)
	_, otherTok := h.node(t)
	done := make(chan error, 1)
	go func() { _, err := h.client.GetRelease(context.Background(), getRelease(slowTok)); done <- err }()
	deadline := time.Now().Add(2 * time.Second)
	for f.node.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	forgot := make(chan struct{})
	go func() { h.svc.Forget(slowID); close(forgot) }()
	select {
	case <-forgot:
	case <-time.After(2 * time.Second):
		t.Fatal("Forget waited for an in-flight GetRelease")
	}
	if _, err := h.client.Report(context.Background(), report(otherTok, &heronv1.Metrics{CpuPct: proto.Float64(1)})); err != nil {
		t.Fatalf("Report blocked or failed while a download was in flight: %v", err)
	}
	close(f.block)
	<-done
}
```

（`h.svc.Forget` 的签名以 service.go 为准。）

- [ ] **Step 6: 实现 ingest 接入**

`internal/hub/ingest/service.go`：

`Config` 增加：

```go
	// Releases 为 hub 来源的节点中转官方产物（spec §4.10）。nil 时 GetRelease 返回 Unavailable：
	// 缺省是不提供中转，不是放宽。
	Releases interface {
		Get(ctx context.Context, node int64, taskID, arch string) (update.Artifacts, error)
	}
```

拦截器：把取 token 与 `Authenticate` 抽成 `func (s *Service) bearer(req connect.AnyRequest) (int64, string, bool)`，Report 分支改用它（行为不变），新增分支：

```go
		case heronv1connect.AgentServiceGetReleaseProcedure:
			// 不取 stateMu：一次下载可能持续数分钟，持读锁会让 Forget 等待，而排队的写锁又会挡住此后全部
			// Report 的读锁（sync.RWMutex 有写者排队时新读者阻塞）。GetRelease 不写 Report 维护的内存状态；
			// 节点删除后它的任务随 Manager.Forget 消失，Relay 的任务检查即拒绝。
			id, _, ok := s.bearer(req)
			if !ok {
				return nil, unauthenticated()
			}
			return next(context.WithValue(ctx, nodeKey{}, id), req)
```

方法（替换 Task 4 的占位实现）：

```go
func (s *Service) GetRelease(ctx context.Context, req *connect.Request[heronv1.GetReleaseRequest]) (*connect.Response[heronv1.GetReleaseResponse], error) {
	if s.cfg.Releases == nil {
		return nil, connect.NewError(connect.CodeUnavailable, errors.New("release relay is not configured"))
	}
	id := ctx.Value(nodeKey{}).(int64)
	a, err := s.cfg.Releases.Get(ctx, id, req.Msg.GetTaskId(), req.Msg.GetArch())
	if err != nil {
		return nil, releaseError(err)
	}
	return connect.NewResponse(&heronv1.GetReleaseResponse{Sums: a.Sums, Signature: a.Signature, Archive: a.Archive}), nil
}

// releaseError 把中转的失败映射为 Connect 错误码。取回失败的原文回给更新器、进入任务的 error，
// 管理员据此判断是 hub 连不上 GitHub 还是产物验签不过；原文里只有地址与原因，没有凭据。
func releaseError(err error) error {
	switch {
	case errors.Is(err, updates.ErrArch):
		return connect.NewError(connect.CodeInvalidArgument, err)
	case errors.Is(err, updates.ErrNoTask):
		return connect.NewError(connect.CodeFailedPrecondition, err)
	case errors.Is(err, updates.ErrBusy), errors.Is(err, updates.ErrAttempts), errors.Is(err, updates.ErrCacheFull):
		return connect.NewError(connect.CodeResourceExhausted, err)
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		return connect.NewError(connect.CodeCanceled, err)
	}
	return connect.NewError(connect.CodeUnavailable, fmt.Errorf("fetch official release: %w", err))
}
```

`cmd/hub/serve.go`（`updateManager := updates.New(...)` 之后）：

```go
	official := update.NewOfficialSource()
	relay := updates.NewRelay(updateManager,
		func(ctx context.Context, version, arch string) (update.Artifacts, error) {
			return official.Fetch(ctx, update.Request{Version: version}, "agent", arch)
		},
		// 与节点更新器同一个接受函数：hub 处的预验签只为尽早报错，判定标准不能与节点不同。
		func(version, arch string, a update.Artifacts) error {
			_, err := update.Accept(releasesig.Trusted(), "agent", arch, version, a)
			return err
		}, clk)
```

`ingest.Config{…}` 加 `Releases: relay`；在 `defer startLoop(updateManager.Run)()` 旁加 `defer startLoop(relay.Run)()`。

- [ ] **Step 7: 测试、race 与缺陷注入**

Run: `cd <worktree> && go build ./... > <task-dir>/t5.log 2>&1 && go test -count=1 -race ./internal/hub/updates/ ./internal/hub/ingest/ ./cmd/hub/ >> <task-dir>/t5.log 2>&1; echo $?` → `0`。

缺陷注入：
1. `Get` 删掉 `t.Id != taskID ||` → `TestRelayRejectsForeignTask`、`TestRelayRejectsWithoutMatchingLiveTask/other_id` 红。
2. `load` 失败分支不删 `r.cache[k]` → `TestRelayDoesNotCacheFailures` 红。
3. `Get` 每次都新建条目（不查 `r.cache[k]`）→ `TestRelayMergesConcurrentFetches` 红。
4. 拦截器 GetRelease 分支加 `s.stateMu.RLock(); defer s.stateMu.RUnlock()` → `TestGetReleaseDoesNotBlockForgetOrReport` 红。
5. `Sweep` 不删 attempts → `TestRelaySweepReleasesUnreferenced` 红。

- [ ] **Step 8: 提交**

```bash
cd <worktree> && git add internal/hub/updates internal/hub/ingest cmd/hub/serve.go
git commit -m "feat(hub): GetRelease 按节点当前任务中转官方产物，预验签后内存缓存"
```

---

### Task 6: 更新器的 hub 来源与来源配置

**Files:**
- Create: `internal/update/hubsource.go`、`internal/update/hubsource_test.go`
- Create: `internal/update/sourceconfig.go`、`internal/update/sourceconfig_test.go`
- Modify: `internal/update/system_linux.go`（选来源、`readSafe`、`agentConfigPath` 常量替换第 68 行字面量）
- Modify: `internal/update/wire.go`（`StatusProto` 带 source、`ValidateStatus` 校验 source）、对应测试
- Modify: `internal/update/engine_test.go`

**Interfaces:**
- Consumes: Task 2 的 `sourceChoice`、`Artifacts`、`trailingJSON`、`downloadTimeout`；Task 3 的 `agentconfig.Decode/CheckHub`、`hubclient.New`；Task 4 的 `GetReleaseRequest`、`UpdateStatus.Source`。
- Produces：
  - `func NewHubSource(readConfig func() ([]byte, error)) *HubSource`、`func (s *HubSource) Fetch(ctx context.Context, task Request, role, arch string) (Artifacts, error)`
  - `const agentConfigPath = "/etc/heron-agent/config.json"`、`const sourceConfigPath = "/etc/heron-update-agent/config.json"`
  - `func parseSourceConfig(b []byte) (string, error)`
  - `func chooseSource(role string, read func() ([]byte, error), github, hub source) sourceChoice`

- [ ] **Step 1: 来源配置的失败测试**

`internal/update/sourceconfig_test.go`：

```go
package update

import (
	"errors"
	"io/fs"
	"strings"
	"testing"
)

func TestParseSourceConfig(t *testing.T) {
	for _, tc := range []struct{ body, want, err string }{
		{`{"source":"github"}`, "github", ""},
		{`{"source":"hub"}` + "\n", "hub", ""},
		{`{"source":"ftp"}`, "", "github or hub"},
		{`{}`, "", "github or hub"},
		{`{"source":"hub","extra":1}`, "", "unknown field"},
		{`{"source":"hub"} {"source":"github"}`, "", "single JSON object"},
		{``, "", "EOF"},
	} {
		got, err := parseSourceConfig([]byte(tc.body))
		if tc.err == "" && (err != nil || got != tc.want) {
			t.Errorf("%q: got %q err %v", tc.body, got, err)
		}
		if tc.err != "" && (err == nil || !strings.Contains(err.Error(), tc.err)) {
			t.Errorf("%q: err %v, want %q", tc.body, err, tc.err)
		}
	}
}

type namedSource string

func (namedSource) Fetch(context.Context, Request, string, string) (Artifacts, error) { return Artifacts{}, nil }

func TestChooseSource(t *testing.T) {
	gh, hub := namedSource("github"), namedSource("hub")
	read := func(b string, err error) func() ([]byte, error) { return func() ([]byte, error) { return []byte(b), err } }
	if c := chooseSource("hub", func() ([]byte, error) { t.Fatal("hub role read the agent source config"); return nil, nil }, gh, hub); c.name != "github" || c.src != gh {
		t.Errorf("hub role: %+v", c)
	}
	if c := chooseSource("agent", read("", fs.ErrNotExist), gh, hub); c.name != "github" || c.src != gh || c.err != "" {
		t.Errorf("missing file: %+v", c)
	}
	if c := chooseSource("agent", read(`{"source":"hub"}`, nil), gh, hub); c.name != "hub" || c.src != hub {
		t.Errorf("hub file: %+v", c)
	}
	if c := chooseSource("agent", read(`{"source":"github"}`, nil), gh, hub); c.name != "github" || c.src != gh {
		t.Errorf("github file: %+v", c)
	}
	for name, r := range map[string]func() ([]byte, error){
		"corrupt":    read(`{"source":`, nil),
		"permission": read("", fs.ErrPermission),
		"other":      read("", errors.New("managed file must be regular")),
	} {
		c := chooseSource("agent", r, gh, hub)
		if c.src != nil || !strings.Contains(c.err, sourceConfigPath) {
			t.Errorf("%s: %+v", name, c)
		}
	}
}
```

（补 `context` import。）

- [ ] **Step 2: 实现来源配置**

`internal/update/sourceconfig.go`：

```go
package update

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
)

// sourceConfigPath 由 root 安装器按 --update-source 写入（spec §4.10、§14）。它不在 agent 配置里：
// agent 配置属服务用户，而从哪里取字节是 root 的决定。
const sourceConfigPath = "/etc/heron-update-agent/config.json"

// agentConfigPath 是安装器固定的 agent 配置路径；hub 来源从这里读 hub 地址与 token，Current 也按它核对 agent 的启动参数。
const agentConfigPath = "/etc/heron-agent/config.json"

// parseSourceConfig 严格解析来源配置：未知字段、第一个对象之后的内容、不认识或缺失的取值都是错误。
func parseSourceConfig(b []byte) (string, error) {
	var c struct {
		Source string `json:"source"`
	}
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&c); err != nil {
		return "", err
	}
	if err := trailingJSON(dec); err != nil {
		return "", err
	}
	switch c.Source {
	case "github", "hub":
		return c.Source, nil
	}
	return "", errors.New("source must be github or hub")
}

// chooseSource 按本机安装参数选来源，只对 agent 角色读文件（hub 角色固定 GitHub）。文件不存在等于 github：
// 这不是放宽，两种来源的接受规则相同（Accept）。其他读取或解析错误不回退到 github——那会让一台只能连 hub 的
// 主机悄悄改走必然失败的路径——而是让更新器不支持更新，原因带上文件路径。
func chooseSource(role string, read func() ([]byte, error), github, hub source) sourceChoice {
	if role != "agent" {
		return sourceChoice{name: "github", src: github}
	}
	b, err := read()
	if errors.Is(err, fs.ErrNotExist) {
		return sourceChoice{name: "github", src: github}
	}
	if err == nil {
		var name string
		if name, err = parseSourceConfig(b); err == nil {
			if name == "hub" {
				return sourceChoice{name: "hub", src: hub}
			}
			return sourceChoice{name: "github", src: github}
		}
	}
	return sourceChoice{err: fmt.Sprintf("update source config %s is unusable: %v; rerun the installer with --update-source", sourceConfigPath, err)}
}
```

`system_linux.go` 第 68 行的 `"/etc/heron-agent/config.json"` 字面量改用 `agentConfigPath`。

- [ ] **Step 3: HubSource 的失败测试**

`internal/update/hubsource_test.go`：

```go
package update

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"connectrpc.com/connect"

	heronv1 "github.com/xjetry/heron-probe/gen/heron/v1"
	"github.com/xjetry/heron-probe/gen/heron/v1/heronv1connect"
)

type relayHub struct {
	heronv1connect.UnimplementedAgentServiceHandler
	calls atomic.Int32
	auth  string
	req   *heronv1.GetReleaseRequest
	resp  *heronv1.GetReleaseResponse
	err   error
}

func (h *relayHub) GetRelease(_ context.Context, req *connect.Request[heronv1.GetReleaseRequest]) (*connect.Response[heronv1.GetReleaseResponse], error) {
	h.calls.Add(1)
	h.auth, h.req = req.Header().Get("Authorization"), req.Msg
	if h.err != nil {
		return nil, h.err
	}
	return connect.NewResponse(h.resp), nil
}

func serveRelay(t *testing.T, h *relayHub) string {
	t.Helper()
	mux := http.NewServeMux()
	mux.Handle(heronv1connect.NewAgentServiceHandler(h))
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv.URL
}

func configFor(url string) func() ([]byte, error) {
	return func() ([]byte, error) {
		return []byte(`{"hub":"` + url + `","token":"node-secret-token","name":"n"}`), nil
	}
}

func TestHubSourceFetch(t *testing.T) {
	want := signedArtifacts("agent", "amd64", "v1.2.3", []byte("archive-bytes"))
	h := &relayHub{resp: &heronv1.GetReleaseResponse{Sums: want.Sums, Signature: want.Signature, Archive: want.Archive}}
	s := NewHubSource(configFor(serveRelay(t, h)))
	got, err := s.Fetch(context.Background(), Request{ID: "aaaaaaaaaaaaaaaa", Version: "v1.2.3"}, "agent", "amd64")
	if err != nil {
		t.Fatal(err)
	}
	if string(got.Sums) != string(want.Sums) || string(got.Signature) != string(want.Signature) || string(got.Archive) != "archive-bytes" {
		t.Fatalf("got %+v", got)
	}
	if h.auth != "Bearer node-secret-token" || h.req.GetTaskId() != "aaaaaaaaaaaaaaaa" || h.req.GetArch() != "amd64" {
		t.Fatalf("auth=%q req=%v", h.auth, h.req)
	}
}

func TestHubSourceRefusesBeforeNetwork(t *testing.T) {
	h := &relayHub{}
	url := serveRelay(t, h)
	for name, tc := range map[string]struct {
		role string
		read func() ([]byte, error)
		want string
	}{
		"hub_role":          {"hub", configFor(url), "only agent"},
		"plain_http_remote": {"agent", func() ([]byte, error) { return []byte(`{"hub":"http://10.0.0.1:8080","token":"t"}`), nil }, "plain http"},
		"read_error":        {"agent", func() ([]byte, error) { return nil, errors.New("managed file must be regular") }, "read agent config"},
		"unknown_field":     {"agent", func() ([]byte, error) { return []byte(`{"hub":"` + url + `","token":"t","x":1}`), nil }, "unknown field"},
	} {
		_, err := NewHubSource(tc.read).Fetch(context.Background(), Request{ID: "aaaaaaaaaaaaaaaa", Version: "v1.2.3"}, tc.role, "amd64")
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: err %v, want %q", name, err, tc.want)
		}
	}
	if h.calls.Load() != 0 {
		t.Fatalf("refused fetches still reached the hub %d times", h.calls.Load())
	}
}

func TestHubSourceErrorDoesNotLeakToken(t *testing.T) {
	h := &relayHub{err: connect.NewError(connect.CodeFailedPrecondition, errors.New("no matching update task"))}
	_, err := NewHubSource(configFor(serveRelay(t, h))).Fetch(context.Background(), Request{ID: "aaaaaaaaaaaaaaaa", Version: "v1.2.3"}, "agent", "amd64")
	if err == nil || !strings.Contains(err.Error(), "no matching update task") || strings.Contains(err.Error(), "node-secret-token") {
		t.Fatalf("err = %v", err)
	}
}
```

- [ ] **Step 4: 实现 HubSource**

`internal/update/hubsource.go`：

```go
package update

import (
	"bytes"
	"context"
	"errors"
	"fmt"

	"connectrpc.com/connect"

	heronv1 "github.com/xjetry/heron-probe/gen/heron/v1"
	"github.com/xjetry/heron-probe/internal/agentconfig"
	"github.com/xjetry/heron-probe/internal/hubclient"
	"github.com/xjetry/heron-probe/internal/releasesig"
)

// maxRelayResponse 是 GetRelease 响应正文的上限：三份文件各自上限之和，加 protobuf 与 Connect 的编码余量。
const maxRelayResponse = maxArchive + releasesig.MaxSums + releasesig.MaxFile + 64<<10

// HubSource 经 hub 的 GetRelease 取产物（spec §4.10），不做接受判定（见 Accept）。hub 地址、token 与明文许可在
// 每个任务现读 agent 配置：节点 token 轮换后无须同步。解析与地址规则和 agent 同一实现（agentconfig），连接与 agent
// 同一构造（hubclient），所以 https 要求、不跟随重定向、响应上限对两者同时成立（spec §5.7）。
type HubSource struct {
	// readConfig 返回 agent 配置原文；正式实现按服务用户属主等检查打开并限量读取（serve 里构造）。
	readConfig func() ([]byte, error)
}

func NewHubSource(readConfig func() ([]byte, error)) *HubSource { return &HubSource{readConfig: readConfig} }

func (s *HubSource) Fetch(ctx context.Context, task Request, role, arch string) (Artifacts, error) {
	if role != "agent" {
		return Artifacts{}, errors.New("hub source serves only agent updates")
	}
	raw, err := s.readConfig()
	if err != nil {
		return Artifacts{}, fmt.Errorf("read agent config: %w", err)
	}
	cfg, err := agentconfig.Decode(bytes.NewReader(raw), agentConfigPath)
	if err != nil {
		return Artifacts{}, err
	}
	if err := agentconfig.CheckHub(cfg.Hub, cfg.InsecureHTTP); err != nil {
		return Artifacts{}, err
	}
	req := connect.NewRequest(&heronv1.GetReleaseRequest{TaskId: task.ID, Arch: arch})
	req.Header().Set("Authorization", "Bearer "+cfg.Token)
	resp, err := hubclient.New(cfg.Hub, downloadTimeout, maxRelayResponse).GetRelease(ctx, req)
	if err != nil {
		return Artifacts{}, fmt.Errorf("fetch release from hub: %w", err)
	}
	return Artifacts{Sums: resp.Msg.GetSums(), Signature: resp.Msg.GetSignature(), Archive: resp.Msg.GetArchive()}, nil
}
```

- [ ] **Step 5: serve 选来源**

`internal/update/system_linux.go`：

```go
const (
	// agent 配置属服务用户、由 root 更新器解析：限量读取，文件被换成巨大文件也不会让 root 进程耗尽内存。
	maxAgentConfig  = 1 << 20
	maxSourceConfig = 4 << 10
)

// readSafe 经 safeFile（目录与文件都不跟随链接、普通文件、单链接、属主为 uid、不可被组与其他用户写）打开后限量读取。
func readSafe(path string, uid int, limit int64) ([]byte, error) {
	f, err := safeFile(path, uid)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(b)) > limit {
		return nil, fmt.Errorf("%s exceeds %d bytes", path, limit)
	}
	return b, nil
}
```

在 `serve` 里得到 `uid` 之后、`newEngine` 之前：

```go
	hub := NewHubSource(func() ([]byte, error) { return readSafe(agentConfigPath, uid, maxAgentConfig) })
	choice := chooseSource(role, func() ([]byte, error) { return readSafe(sourceConfigPath, 0, maxSourceConfig) }, official, hub)
```

`newEngine` 传 `choice`。`safeFile` 打开不存在的目录或文件时返回的 `unix.ENOENT` 满足 `errors.Is(err, fs.ErrNotExist)`（`syscall.Errno.Is`），`chooseSource` 依赖这一点把"没写"当 github——在 Step 7 的 linux vet 之外，用下面这条用例在 linux 构建里钉住（放 `system_linux_test.go`，它只在 linux 上运行，控制端的隔离验收或 CI 会跑到）：

```go
func TestReadSafeMissingIsNotExist(t *testing.T) {
	_, err := readSafe(filepath.Join(t.TempDir(), "absent", "config.json"), os.Getuid(), 16)
	if !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("err = %v, want fs.ErrNotExist", err)
	}
}
```

- [ ] **Step 6: wire 与 engine 测试**

`wire.go`：`StatusProto` 增加 `Source: s.Source`；`ValidateStatus` 在字符串上限检查之后加：

```go
	switch s.Source {
	case "", "github", "hub":
	default:
		return errors.New("invalid update source")
	}
```

（空串放行：旧更新器不报这个字段，它不是放宽——来源只用于展示，不参与任何判定。）

`wire_test.go`（若不存在则新建）：

```go
func TestStatusProtoCarriesSource(t *testing.T) {
	if got := StatusProto(Status{Supported: true, Source: "hub"}, "v1.0.0").GetSource(); got != "hub" {
		t.Fatalf("source = %q", got)
	}
}

func TestValidateStatusSource(t *testing.T) {
	for src, ok := range map[string]bool{"": true, "github": true, "hub": true, "ftp": false} {
		err := ValidateStatus(&heronv1.UpdateStatus{Source: src})
		if (err == nil) != ok {
			t.Errorf("%q: err %v", src, err)
		}
	}
}
```

`engine_test.go` 追加：

```go
func TestEngineSourceConfigErrorDisablesUpdates(t *testing.T) {
	m := &fakeMachine{version: "v0.2.0"}
	e, err := newEngine(context.Background(), filepath.Join(t.TempDir(), "state.json"), "agent", "amd64",
		sourceChoice{err: "update source config " + sourceConfigPath + " is unusable: bad"}, testKeys(), m)
	if err != nil {
		t.Fatal(err)
	}
	s := e.status()
	if s.Supported || !strings.Contains(s.Reason, sourceConfigPath) {
		t.Fatalf("status = %+v", s)
	}
	if _, err := e.submit(request()); err == nil || !strings.Contains(err.Error(), sourceConfigPath) {
		t.Fatalf("submit accepted with an unusable source config: %v", err)
	}
}
```

- [ ] **Step 7: 测试、linux vet 与缺陷注入**

Run: `cd <worktree> && go test -count=1 ./internal/update/ > <task-dir>/t6.log 2>&1; echo $?` → `0`
Run: `cd <worktree> && GOOS=linux go vet ./internal/update/ ./cmd/updater/ > <task-dir>/t6vet.log 2>&1; echo $?` → `0`
Run: `cd <worktree> && GOOS=linux go test -c -o /dev/null ./internal/update/ > <task-dir>/t6c.log 2>&1; echo $?` → `0`（确认 linux 专属测试能编译）

缺陷注入：
1. `chooseSource` 解析失败时返回 github → `TestChooseSource` 的 corrupt 用例红。
2. `HubSource.Fetch` 删掉 `CheckHub` → `TestHubSourceRefusesBeforeNetwork/plain_http_remote` 红（且 `calls` 断言红）。
3. `submit` 删掉 `choice.err` 检查 → `TestEngineSourceConfigErrorDisablesUpdates` 红。
4. `ValidateStatus` 删掉 source 检查 → `TestValidateStatusSource` 红。

- [ ] **Step 8: 提交**

```bash
cd <worktree> && git add internal/update
git commit -m "feat(update): agent 更新器按安装参数选 hub 来源，经 GetRelease 取产物并上报来源"
```

---

### Task 7: 安装脚本 `--update-source`

**Files:**
- Modify: `deploy/install.sh`
- Create: `deploy/installsource_test.go`

**Interfaces:**
- Produces: `--update-source github|hub`；写 `$ROOT/etc/heron-update-agent/config.json`，内容 `{"source":"<值>"}\n`，目录 0755、文件 0644。

- [ ] **Step 1: 写失败测试**

`deploy/installsource_test.go`（复用 `installlinux_test.go`、`installmacos_test.go` 里的 `newLinuxHost`、`newLinuxInstalled`、`e.run`、`e.file`、`e.exists`、`e.mode`、`e.calls`、`e.resetCalls`、`e.linuxRelease`；`assertNothingDownloaded` 在 `installintegrity_test.go`）：

```go
package main

import (
	"strings"
	"testing"
)

const sourceFile = "etc/heron-update-agent/config.json"

func (e *env) installWithSource(source string) (string, int) {
	return e.run("--hub", "http://hub.test", "--key", "k", "--base-url", "file://"+e.dist, "--update-source", source)
}

func TestUpdateSourceWrittenOnInstall(t *testing.T) {
	e := newLinuxHost(t)
	e.appendTo("etc/passwd", "heron-agent:x:480:480::/nonexistent:/usr/sbin/nologin\n")
	e.appendTo("etc/group", "heron-agent:x:480:\n")
	e.linuxRelease("amd64", "v1")
	out, code := e.installWithSource("hub")
	if code != 0 {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	if got := e.file(sourceFile); got != "{\"source\":\"hub\"}\n" {
		t.Fatalf("source config = %q", got)
	}
	e.mode(sourceFile, 0o644)
	e.mode("etc/heron-update-agent", 0o755)
}

func TestUpdateSourceAbsentByDefault(t *testing.T) {
	e := newLinuxInstalled(t)
	if e.exists(sourceFile) {
		t.Fatal("an install without --update-source wrote a source config")
	}
}

func TestUpdateSourceKeptOnRerun(t *testing.T) {
	e := newLinuxInstalled(t)
	if out, code := e.installWithSource("hub"); code != 0 {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	if out, code := e.linuxInstall(); code != 0 {
		t.Fatalf("rerun exit %d:\n%s", code, out)
	}
	if got := e.file(sourceFile); got != "{\"source\":\"hub\"}\n" {
		t.Fatalf("a rerun without --update-source changed the source: %q", got)
	}
	if out, code := e.installWithSource("github"); code != 0 {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	if got := e.file(sourceFile); got != "{\"source\":\"github\"}\n" {
		t.Fatalf("explicit switch not applied: %q", got)
	}
}

func TestUpdateSourceRejectsUnknownValue(t *testing.T) {
	e := newLinuxInstalled(t)
	e.resetCalls()
	out, code := e.installWithSource("ftp")
	if code != 2 || !strings.Contains(out, "--update-source must be github or hub") {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	assertNothingDownloaded(t, e)
}

func TestUpdateSourceRefusedOnOpenRC(t *testing.T) {
	e := newStubEnv(t, "install.sh", linuxStubs)
	e.put("sbin/openrc-run", "#!/bin/sh\n")
	e.chmodExec("sbin/openrc-run")
	e.put("proc/self/status", "Uid:\t1000\t1000\t1000\t1000\n")
	e.linuxRelease("amd64", "v1")
	out, code := e.installWithSource("hub")
	if code != 2 || !strings.Contains(out, "--update-source applies only to systemd") {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	assertNothingDownloaded(t, e)
}

func TestUpdateSourceWithUninstallIsUsageError(t *testing.T) {
	e := newLinuxInstalled(t)
	if out, code := e.run("--uninstall", "--update-source", "hub"); code != 2 {
		t.Fatalf("exit %d:\n%s", code, out)
	}
}

func TestUpdateSourcePurgeScope(t *testing.T) {
	e := newLinuxInstalled(t)
	if out, code := e.installWithSource("hub"); code != 0 {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	if out, code := e.run("--uninstall"); code != 0 || !e.exists(sourceFile) {
		t.Fatalf("plain uninstall must keep the source config: exit %d\n%s", code, out)
	}
	if out, code := e.run("--uninstall", "--purge"); code != 0 || e.exists("etc/heron-update-agent") {
		t.Fatalf("purge must remove /etc/heron-update-agent: exit %d\n%s", code, out)
	}
}
```

（`e.chmodExec` 若不存在，用 `os.Chmod(filepath.Join(e.root, "sbin/openrc-run"), 0o755)` 写一个小 helper；`assertNothingDownloaded` 的签名以 `installintegrity_test.go` 为准；OpenRC 宿主若还需要别的文件才能走到参数检查之后，以脚本实际报错为准补齐——但参数检查必须在 INIT 检测之后、任何下载与系统变更之前，测试据此断言。第二次 `--uninstall` 前若服务已卸，以现有 purge 测试的写法为准。）

- [ ] **Step 2: 运行确认失败**

Run: `cd <worktree> && go test -count=1 -run UpdateSource ./deploy/ > <task-dir>/t7.log 2>&1; echo $?` → 非 0。

- [ ] **Step 3: 实现**

`deploy/install.sh`：

1. 路径变量区（`UPDATER_STATE` 旁）：

```sh
# 在线更新取产物的来源（spec §4.10）：root 的决定，不放进服务用户拥有的 agent 配置。
UPDATER_CFG_DIR=$ROOT/etc/heron-update-agent
UPDATER_CFG=$UPDATER_CFG_DIR/config.json
```

2. `usage` 第一行加 `[--update-source github|hub]`；变量初始化加 `UPDATE_SOURCE=""`；参数循环加 `--update-source) need_value "$@"; UPDATE_SOURCE=$2; shift 2;;`。
3. 循环之后、`[ "$PURGE" = 0 ] || …` 旁：

```sh
case "$UPDATE_SOURCE" in
  ""|github|hub) ;;
  *) echo "--update-source must be github or hub" >&2; exit 2;;
esac
[ -z "$UPDATE_SOURCE" ] || [ "$UNINSTALL" = 0 ] || usage
```

4. INIT 检测（`elif … INIT=openrc … fi`）之后：

```sh
# 在线更新只支持 systemd；OpenRC 主机靠重跑安装器升级，来源参数在那里没有读者，静默接受会让人以为生效了。
if [ -n "$UPDATE_SOURCE" ] && [ "$INIT" != systemd ]; then
  echo "--update-source applies only to systemd online updates; $INIT hosts update by rerunning the installer" >&2
  exit 2
fi
```

5. 函数区：

```sh
# 不给 --update-source 时不动已有文件：例行重跑不能把 hub 来源的节点悄悄切回 github，那样它下一次在线更新必然失败。
# 先写同目录临时文件再 mv，更新器启动时读不到半份文件；目录只有 root 能写，没有可被替换的目录项。
write_update_source() {
  [ -n "$UPDATE_SOURCE" ] || return 0
  mkdir -p "$UPDATER_CFG_DIR"
  chmod 0755 "$UPDATER_CFG_DIR"
  printf '{"source":"%s"}\n' "$UPDATE_SOURCE" > "$UPDATER_CFG.tmp.$$"
  chmod 0644 "$UPDATER_CFG.tmp.$$"
  mv -f "$UPDATER_CFG.tmp.$$" "$UPDATER_CFG"
}
```

6. systemd 安装分支里 `mv -f "$UPDATER_TMP" "$UPDATER_BIN"` 之前调用 `write_update_source`（更新器在本分支末尾 `systemctl start heron-updater-agent` 才启动，启动时读来源配置）。
7. 卸载段 `if [ "$PURGE" = 1 ]; then rm -rf "$CFG_DIR" "$LOG_DIR"` 改为 `rm -rf "$CFG_DIR" "$LOG_DIR" "$UPDATER_CFG_DIR"`。

- [ ] **Step 4: 测试、lint 与缺陷注入**

Run: `cd <worktree> && go test -count=1 -timeout 20m ./deploy/ > <task-dir>/t7.log 2>&1; echo $?` → `0`
Run: `cd <worktree> && shellcheck -s sh deploy/install.sh > <task-dir>/t7sc.log 2>&1; echo $?` → `0`
Run: `cd <worktree> && cd web && pnpm exec vitest run src/pages/installSurface.test.ts > <task-dir>/t7web.log 2>&1; echo $?` → `0`

缺陷注入：
1. `write_update_source` 去掉开头的 `[ -n … ] || return 0` 并在空值时写 github → `TestUpdateSourceKeptOnRerun` 与 `TestUpdateSourceAbsentByDefault` 红。
2. 删掉 OpenRC 拒绝段 → `TestUpdateSourceRefusedOnOpenRC` 红。
3. purge 行去掉 `"$UPDATER_CFG_DIR"` → `TestUpdateSourcePurgeScope` 红。

- [ ] **Step 5: 提交**

```bash
cd <worktree> && git add deploy/install.sh deploy/installsource_test.go
git commit -m "feat(install): --update-source 写入在线更新的取产物来源，重跑不给时保留原值"
```

---

### Task 8: 面板显示取产物来源

**Files:**
- Modify: `web/src/pages/Updates.tsx`、`web/src/pages/Updates.test.tsx`

**Interfaces:**
- Consumes: Task 4 生成的 TS `UpdateStatus.source`。

- [ ] **Step 1: 写失败测试**

`Updates.test.tsx`：把 `targets` 夹具里节点 1 的 status 加 `source: "hub"`，节点 3 加 `source: "github"`，节点 2 不加；新增：

```tsx
it("显示每个节点取产物的来源", async () => {
  render();
  expect(await screen.findByText("经 hub 中转")).toBeInTheDocument();
  expect(screen.getByText("GitHub 直连")).toBeInTheDocument();
  expect(screen.getAllByText(/经 hub 中转|GitHub 直连/)).toHaveLength(2);
});
```

（若 Hub 卡片的 status 也带 `source` 会多出一个匹配；夹具里节点 0 不加 source，Hub 卡片不显示来源。断言的数量以夹具为准。）

- [ ] **Step 2: 运行确认失败**

Run: `cd <worktree>/web && pnpm exec vitest run src/pages/Updates.test.tsx > <task-dir>/t8.log 2>&1; echo $?` → 非 0。

- [ ] **Step 3: 实现**

`Updates.tsx` 的 `Progress` 里，在 `!status.supported` 那行之后加：

```tsx
    {status.source && <span className="muted">{sourceLabels[status.source] ?? status.source}</span>}
```

文件顶部 `labels` 旁：

```tsx
// 取产物的来源由节点安装时的 --update-source 决定（spec §4.10）；更新失败时先看它走的哪条路径。
const sourceLabels: Record<string, string> = { github: "GitHub 直连", hub: "经 hub 中转" };
```

说明文字 `仅从 xjetry/heron-probe 的正式 Release 下载并校验产物，不执行远程命令。` 改为 `只安装 xjetry/heron-probe 正式 Release 中带官方签名的产物，不执行远程命令；节点按安装时的选择直接从 GitHub 或经 hub 中转取得。`（搜索 `Updates.test.tsx` 与 `web/e2e/admin-ui.spec.ts` 里是否断言了旧文案，一并改。）

- [ ] **Step 4: 测试并提交**

Run: `cd <worktree> && make web-test > <task-dir>/t8.log 2>&1; echo $?` → `0`（目标名以 Makefile 为准：`ci` 依赖里的 `web-test`）。

缺陷注入：`sourceLabels` 的 hub 改成别的文字 → 新用例红。

```bash
cd <worktree> && git add web/src/pages/Updates.tsx web/src/pages/Updates.test.tsx web/e2e/admin-ui.spec.ts
git commit -m "feat(web): 更新页显示节点取产物的来源"
```

---

### Task 9: 进程内集成与隔离验收用例

**Files:**
- Create: `internal/hub/ingest/relay_integration_test.go`
- Create: `internal/hub/ingest/relay_accept_test.go`
- Create: `internal/update/relay_accept_test.go`

**Interfaces:**
- Consumes: 全部前序任务。

- [ ] **Step 1: 进程内全链路用例**

`internal/hub/ingest/relay_integration_test.go`——真实 ingest 处理器 + 真实 `updates.Manager` 与 `updates.Relay`（假官方源、测试公钥）+ 真实 `update.HubSource` + `update.Accept`：

```go
package ingest

import (
	"context"
	"crypto/ed25519"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/proto"

	heronv1 "github.com/xjetry/heron-probe/gen/heron/v1"
	"github.com/xjetry/heron-probe/internal/agentwire"
	"github.com/xjetry/heron-probe/internal/clock"
	"github.com/xjetry/heron-probe/internal/hub/auth"
	"github.com/xjetry/heron-probe/internal/hub/live"
	"github.com/xjetry/heron-probe/internal/hub/probe"
	"github.com/xjetry/heron-probe/internal/hub/store"
	"github.com/xjetry/heron-probe/internal/hub/traffic"
	"github.com/xjetry/heron-probe/internal/hub/updates"
	"github.com/xjetry/heron-probe/internal/hubclient"
	"github.com/xjetry/heron-probe/internal/releasesig/sigtest"
	"github.com/xjetry/heron-probe/internal/update"
)

// relayVersion 与 relayBinary 也被 internal/update/relay_accept_test.go 按值引用（两个包不能互相 import 测试代码）。
const (
	relayVersion = "v9.0.0"
	relayBinary  = "relay-accept-agent"
)

func signedAgent(arch string, archive []byte) update.Artifacts {
	sums := sigtest.Sums("heron-agent_linux_"+arch+".tar.gz", archive)
	return update.Artifacts{Sums: sums, Signature: sigtest.Sign(relayVersion, sums), Archive: archive}
}

func testTrusted() []ed25519.PublicKey { pub, _ := sigtest.Key(); return []ed25519.PublicKey{pub} }

func acceptVerify(version, arch string, a update.Artifacts) error {
	_, err := update.Accept(testTrusted(), "agent", arch, version, a)
	return err
}

// relayHub 起一套带中转的 hub（构造顺序同 newHubWith），并让一个节点处于 dispatched 的更新任务上。
// official 按请求的架构给出假官方产物；listen 为空时用 httptest 的随机端口，否则监听该地址（隔离验收）。
func relayHub(t *testing.T, official func(arch string) update.Artifacts, verify updates.VerifyFunc, listen string) (*hub, string, *heronv1.UpdateTask) {
	t.Helper()
	clk := clock.NewFake(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	st, err := store.Open(filepath.Join(t.TempDir(), "t.db"), clk, slog.Default(), store.MigrateSchema)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	reg := probe.New(st, slog.Default())
	a := auth.New(st, reg, nil, clk, time.UTC, slog.Default())
	l := live.New(clk, 30*time.Second)
	book := traffic.New(st, clk, time.UTC, slog.Default())
	manager := updates.New(st, clk, slog.Default())
	relay := updates.NewRelay(manager, func(_ context.Context, _ string, arch string) (update.Artifacts, error) {
		return official(arch), nil
	}, verify, clk)
	svc, err := New(Config{TTL: 30 * time.Second, Updates: manager, Releases: relay}, l, st, a, book, reg, clk, slog.Default())
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	if err := errors.Join(a.Load(ctx), svc.Load(ctx), book.Load(ctx), reg.Load(ctx), manager.Load(ctx)); err != nil {
		t.Fatal(err)
	}
	go manager.Run(ctx)
	mux := http.NewServeMux()
	mux.Handle(svc.Handler())
	srv := httptest.NewUnstartedServer(mux)
	if listen != "" {
		srv.Listener.Close()
		if srv.Listener, err = net.Listen("tcp", listen); err != nil {
			t.Fatal(err)
		}
	}
	srv.Start()
	t.Cleanup(srv.Close)
	h := &hub{svc: svc, srv: srv, client: hubclient.New(srv.URL, 5*time.Second, agentwire.MaxResponseBytes), clk: clk, store: st, auth: a, live: l, book: book, reg: reg}
	id, tok := h.node(t)
	reportUpdate := func() {
		req := report(tok, &heronv1.Metrics{CpuPct: proto.Float64(1)})
		req.Msg.Update = &heronv1.UpdateStatus{Supported: true, Version: "v1.0.0"}
		if _, err := h.client.Report(ctx, req); err != nil {
			t.Fatal(err)
		}
	}
	waitFor := func(ok func(*heronv1.UpdateStatus) bool) *heronv1.UpdateStatus {
		deadline := time.Now().Add(3 * time.Second)
		for time.Now().Before(deadline) {
			if s := manager.Snapshot(id); ok(s) {
				return s
			}
			time.Sleep(5 * time.Millisecond)
		}
		t.Fatalf("update state did not converge: %v", manager.Snapshot(id))
		return nil
	}
	// Start 要求节点已报过支持在线更新，dispatched 要求任务创建后节点再上报一次（Manager.flush）。
	reportUpdate()
	waitFor(func(s *heronv1.UpdateStatus) bool { return s.GetSupported() })
	if _, err := manager.Start(ctx, id, relayVersion); err != nil {
		t.Fatal(err)
	}
	reportUpdate()
	s := waitFor(func(s *heronv1.UpdateStatus) bool { return s.GetTask().GetState() == "dispatched" })
	return h, tok, s.GetTask()
}

func hubSourceFor(h *hub, tok string) *update.HubSource {
	return update.NewHubSource(func() ([]byte, error) {
		return []byte(`{"hub":"` + h.srv.URL + `","token":"` + tok + `","name":"n"}`), nil
	})
}

func TestRelayEndToEnd(t *testing.T) {
	archive := sigtest.Archive("agent", []byte(relayBinary))
	h, tok, task := relayHub(t, func(arch string) update.Artifacts { return signedAgent(arch, archive) }, acceptVerify, "")
	a, err := hubSourceFor(h, tok).Fetch(context.Background(), update.Request{ID: task.Id, Version: task.Version}, "agent", "amd64")
	if err != nil {
		t.Fatal(err)
	}
	bin, err := update.Accept(testTrusted(), "agent", "amd64", task.Version, a)
	if err != nil || string(bin) != relayBinary {
		t.Fatalf("bin=%q err=%v", bin, err)
	}
}

func tampered(arch string) update.Artifacts {
	a := signedAgent(arch, sigtest.Archive("agent", []byte(relayBinary)))
	a.Archive = sigtest.Archive("agent", []byte("evil"))
	return a
}

func TestRelayHubPreverifyRejectsTamperedOfficial(t *testing.T) {
	h, tok, task := relayHub(t, tampered, acceptVerify, "")
	_, err := hubSourceFor(h, tok).Fetch(context.Background(), update.Request{ID: task.Id, Version: task.Version}, "agent", "amd64")
	if connect.CodeOf(err) != connect.CodeUnavailable {
		t.Fatalf("err = %v, want Unavailable from the hub's preverify", err)
	}
}

// 失守的 hub 跳过预验签、把篡改的归档原样转发：节点上的 Accept 仍拒绝。
func TestUpdaterRejectsForgedHub(t *testing.T) {
	h, tok, task := relayHub(t, tampered, func(string, string, update.Artifacts) error { return nil }, "")
	a, err := hubSourceFor(h, tok).Fetch(context.Background(), update.Request{ID: task.Id, Version: task.Version}, "agent", "amd64")
	if err != nil {
		t.Fatalf("the forged hub should serve its bytes: %v", err)
	}
	if _, err := update.Accept(testTrusted(), "agent", "amd64", task.Version, a); err == nil {
		t.Fatal("bytes from a hub that skipped verification were accepted")
	}
}
```

（`HubSource.Fetch` 用 `%w` 包装 Connect 错误，`connect.CodeOf` 经 `errors.As` 穿过包装取到错误码。）

Run: `cd <worktree> && go test -count=1 -race -run 'Relay|ForgedHub' ./internal/hub/ingest/ > <task-dir>/t9.log 2>&1; echo $?` → `0`。

缺陷注入：
1. `HubSource.Fetch` 里 `TaskId: task.ID` 改成空串 → `TestRelayEndToEnd` 红（FailedPrecondition）。
2. `relayHub` 里 hub 侧 verify 换成恒返回 nil（即令 `TestRelayHubPreverifyRejectsTamperedOfficial` 走转发）→ 该用例红。

- [ ] **Step 2: 隔离验收的 hub 端用例**

`internal/hub/ingest/relay_accept_test.go`：

```go
package ingest

import (
	"encoding/json"
	"os"
	"os/signal"
	"runtime"
	"syscall"
	"testing"

	"github.com/xjetry/heron-probe/internal/releasesig/sigtest"
	"github.com/xjetry/heron-probe/internal/update"
)

// TestRelayAcceptServe 只在隔离验收机上运行（spec §12）：用测试公钥与假官方源起一套带 GetRelease 的 hub，
// 监听 HERON_RELAY_LISTEN（例如 127.0.0.1:8080，前面由 Caddy 终止 TLS），把节点 token 与任务写进
// HERON_RELAY_OUT（JSON：{"token","task_id","version"}），然后阻塞到进程收到 SIGTERM 或 SIGINT。
// HERON_RELAY_TAMPER=1：假官方源给出与清单不符的归档，hub 预验签应失败。
// HERON_RELAY_SKIP_VERIFY=1：hub 不做预验签、原样转发（模拟失守的 hub），节点更新器应拒绝。
func TestRelayAcceptServe(t *testing.T) {
	if os.Getenv("HERON_RELAY_ACCEPT") != "hub" {
		t.Skip("only run on the disposable relay acceptance machines")
	}
	official := func(arch string) update.Artifacts { return signedAgent(arch, sigtest.Archive("agent", []byte(relayBinary))) }
	if os.Getenv("HERON_RELAY_TAMPER") == "1" {
		official = tampered
	}
	verify := acceptVerify
	if os.Getenv("HERON_RELAY_SKIP_VERIFY") == "1" {
		verify = func(string, string, update.Artifacts) error { return nil }
	}
	_, tok, task := relayHub(t, official, verify, os.Getenv("HERON_RELAY_LISTEN"))
	out, err := json.Marshal(map[string]string{"token": tok, "task_id": task.Id, "version": task.Version})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(os.Getenv("HERON_RELAY_OUT"), out, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Logf("serving relay on %s for %s/%s", os.Getenv("HERON_RELAY_LISTEN"), runtime.GOOS, runtime.GOARCH)
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGTERM, os.Interrupt)
	<-stop
}
```

- [ ] **Step 3: 隔离验收的 agent 端用例**

`internal/update/relay_accept_test.go`：

```go
package update

import (
	"context"
	"encoding/json"
	"os"
	"runtime"
	"testing"
	"time"

	"github.com/xjetry/heron-probe/internal/githubtransport"
)

// relayAcceptBinary 与 internal/hub/ingest/relay_integration_test.go 的 relayBinary 同值：hub 端把它打包签名后中转。
const relayAcceptBinary = "relay-accept-agent"

// TestRelayAcceptFetch 只在隔离验收机上运行（spec §12）。先断言本机连不上 GitHub——封锁没生效时，后面的正向结果
// 证明不了产物是经 hub 取得的。判据是传输层失败：拿到任何 HTTP 状态码（含 404）都说明连上了，不能用 Fetch 的
// 报错代替（旧版本没有签名文件，连得上也会报 404）。然后按 HERON_RELAY_IN（hub 端写出的 JSON：token、task_id、
// version）与 HERON_RELAY_HUB（https://<hub 机>:28080）拼出 agent 配置，经真实网络调 GetRelease，用测试公钥 Accept。
// HERON_RELAY_EXPECT=accept 时要求得到约定的二进制；=reject 时要求 Fetch 或 Accept 失败。
func TestRelayAcceptFetch(t *testing.T) {
	if os.Getenv("HERON_RELAY_ACCEPT") != "agent" {
		t.Skip("only run on the disposable relay acceptance machines")
	}
	if resp, err := githubtransport.NewClient(10 * time.Second).Get("https://github.com/"); err == nil {
		resp.Body.Close()
		t.Fatalf("this machine reached GitHub directly (HTTP %d); the egress block is not in effect", resp.StatusCode)
	} else {
		t.Logf("direct GitHub connection failed as required: %v", err)
	}
	raw, err := os.ReadFile(os.Getenv("HERON_RELAY_IN"))
	if err != nil {
		t.Fatal(err)
	}
	var in struct {
		Token   string `json:"token"`
		TaskID  string `json:"task_id"`
		Version string `json:"version"`
	}
	if err := json.Unmarshal(raw, &in); err != nil {
		t.Fatal(err)
	}
	cfg, err := json.Marshal(map[string]string{"hub": os.Getenv("HERON_RELAY_HUB"), "token": in.Token, "name": "relay-accept"})
	if err != nil {
		t.Fatal(err)
	}
	arch := runtime.GOARCH
	if arch == "arm" {
		arch = "armv7"
	}
	ctx, cancel := context.WithTimeout(context.Background(), downloadTimeout)
	defer cancel()
	a, err := NewHubSource(func() ([]byte, error) { return cfg, nil }).Fetch(ctx, Request{ID: in.TaskID, Version: in.Version}, "agent", arch)
	var bin []byte
	if err == nil {
		bin, err = Accept(testKeys(), "agent", arch, in.Version, a)
	}
	switch os.Getenv("HERON_RELAY_EXPECT") {
	case "accept":
		if err != nil || string(bin) != relayAcceptBinary {
			t.Fatalf("bin=%q err=%v", bin, err)
		}
	case "reject":
		if err == nil {
			t.Fatal("tampered artifacts were accepted")
		}
		t.Logf("rejected as required: %v", err)
	default:
		t.Fatal("HERON_RELAY_EXPECT must be accept or reject")
	}
}
```

- [ ] **Step 4: 编译检查并提交**

Run: `cd <worktree> && GOOS=linux GOARCH=arm64 go test -c -o <task-dir>/hub.test ./internal/hub/ingest/ > <task-dir>/t9c.log 2>&1 && GOOS=linux GOARCH=arm64 go test -c -o <task-dir>/update.test ./internal/update/ >> <task-dir>/t9c.log 2>&1; echo $?` → `0`
Run: `cd <worktree> && go test -count=1 ./internal/hub/ingest/ ./internal/update/ > <task-dir>/t9.log 2>&1; echo $?` → `0`（验收用例在无环境变量时 Skip）

```bash
cd <worktree> && git add internal/hub/ingest/relay_integration_test.go internal/hub/ingest/relay_accept_test.go internal/update/relay_accept_test.go internal/hub/ingest/*_test.go
git commit -m "test(update): 中转全链路进程内用例与隔离验收用例"
```

---

### Task 10: 生产公钥、隔离验收与合入（控制端执行，不派 worker）

- [ ] **Step 1: 生产公钥**

请用户在本机终端执行（私钥不经过对话与 worker）：

```sh
umask 077
openssl genpkey -algorithm ed25519 -out ~/heron-release-signing.pem
openssl pkey -in ~/heron-release-signing.pem -pubout -outform DER | tail -c 32 | base64
gh secret set HERON_RELEASE_SIGNING_KEY -R xjetry/heron-probe < ~/heron-release-signing.pem
```

用户把第三行输出的公钥（44 个字符）贴回。控制端把它写进 `internal/releasesig/keys.go` 的 `mustParse("<公钥>")`，并在 `releasesig_test.go` 加：

```go
// 正式构建必须带至少一把受信公钥：空列表下更新器拒绝一切产物，release 流水线的签名步骤也会失败。
func TestTrustedKeysConfigured(t *testing.T) {
	if len(Trusted()) == 0 {
		t.Fatal("no trusted release key is configured")
	}
}
```

验证：用私钥对一份临时 SHA256SUMS 签名并用常量公钥验过（`HERON_RELEASE_SIGNING_KEY="$(cat ~/heron-release-signing.pem)" go run ./scripts/releasesign sign -version v0.0.0 -dir <tmp>` 后 `verify`，由用户执行或用户授权后控制端执行）。提交 `feat(release): 写入发行签名公钥`。

- [ ] **Step 2: 集成与门禁**

按任务完成顺序在 integ 分支上栈式 rebase；每次合入前在 integ 树跑 `make ci`、`go test -race -count=1 -timeout 30m ./internal/... ./cmd/...`、`GOOS=linux go vet ./...`、`make e2e`（端口 18080/18081 同时只跑一份）。

- [ ] **Step 3: 隔离验收（spec §12）**

1. `orb create debian:bookworm pia-relay-hub`、`orb create debian:bookworm pia-relay-agent`。
2. 本机 `GOOS=linux GOARCH=arm64 go test -c` 出 `hub.test`（`./internal/hub/ingest`）与 `update.test`（`./internal/update`），拷进两台机器。
3. hub 机：装 Caddy，`Caddyfile` 为 `:28080 { tls internal; reverse_proxy 127.0.0.1:8080 }`；取 Caddy 的本地 CA 根证书装进 agent 机的系统信任库（`/usr/local/share/ca-certificates/` + `update-ca-certificates`）。
4. agent 机：iptables `OUTPUT` 只放行 loopback、已建立连接、DNS 与 hub 机 IP 的 tcp/28080，其余 REJECT；**先确认** `curl -sS -m 10 https://github.com` 失败、`curl -sS -m 10 -o /dev/null -w '%{http_code}' https://<hub>:28080/` 能得到 HTTP 状态码。
5. 三轮：正常（`HERON_RELAY_EXPECT=accept`）、`HERON_RELAY_TAMPER=1`（reject，hub 返回 Unavailable）、`HERON_RELAY_SKIP_VERIFY=1 HERON_RELAY_TAMPER=1`（reject，节点 Accept 拒绝）。每轮 hub 端 `HERON_RELAY_ACCEPT=hub HERON_RELAY_LISTEN=127.0.0.1:8080 HERON_RELAY_OUT=/root/relay.json ./hub.test -test.run '^TestRelayAcceptServe$' -test.v -test.timeout 0`（后台运行，日志落文件），把 `/root/relay.json` 拷到 agent 机；agent 端 `HERON_RELAY_ACCEPT=agent HERON_RELAY_IN=/root/relay.json HERON_RELAY_HUB=https://<hub 机名>:28080 HERON_RELAY_EXPECT=<accept|reject> ./update.test -test.run '^TestRelayAcceptFetch$' -test.v > log 2>&1; echo $?`，再看 log 里的直连失败行与结果行；每轮结束给 hub 端发 SIGTERM。
6. 记录到 `docs/validation/2026-10-05-update-relay.md`（环境版本、封锁规则、三轮结果、日志位置），删除两台 `pia-relay-*` 机器。

- [ ] **Step 4: 合入**

按用户选择合入 main；推送、打 tag、发版、生产升级与特殊主机重跑安装器各自单独征得用户确认。上线顺序见 spec 讨论：发版 → hub 在线升级 → 特殊主机经 SSH 反代重跑安装器加 `--update-source hub` → 下一个正式版发布后在生产特殊主机上完整走一次后台更新并回读。
