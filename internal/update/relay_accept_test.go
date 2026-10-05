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
