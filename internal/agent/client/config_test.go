package client

import (
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

func TestCheckHub(t *testing.T) {
	for _, tc := range []struct {
		hub      string
		insecure bool
		want     string // 空即接受
	}{
		{"https://probe.example.com", false, ""},
		{"https://10.0.0.1:8443/sub", false, ""},
		{"http://127.0.0.1:8080", false, ""},
		{"http://127.9.9.9", false, ""},
		{"http://[::1]:8080", false, ""},
		{"http://[::ffff:127.0.0.1]:8080", false, ""},
		{"http://localhost:8080", false, "uses plain http"},
		{"http://10.0.0.1:8080", false, "uses plain http"},
		{"http://hub.example", false, "uses plain http"},
		{"http://host.docker.internal:8080", false, "uses plain http"},
		{"http://10.0.0.1:8080", true, ""},
		{"http://localhost:8080", true, ""},
		{"ftp://hub.example", true, "must be an https:// URL"},
		{"hub.example", false, "absolute https:// URL"},
		{"", false, "absolute https:// URL"},
		{"http:hub.example", true, "absolute https:// URL"},
	} {
		err := CheckHub(tc.hub, tc.insecure)
		if tc.want == "" && err != nil {
			t.Errorf("%q insecure=%v rejected: %v", tc.hub, tc.insecure, err)
		}
		if tc.want != "" && (err == nil || !strings.Contains(err.Error(), tc.want)) {
			t.Errorf("%q insecure=%v: err=%v, want %q", tc.hub, tc.insecure, err, tc.want)
		}
	}
}

func writeConfig(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestLoadConfigValidates(t *testing.T) {
	for _, tc := range []struct {
		name, body, want string
	}{
		{"https", `{"hub":"https://h","token":"t","name":"n"}`, ""},
		{"loopback_http", `{"hub":"http://127.0.0.1:8080","token":"t"}`, ""},
		{"plain_http", `{"hub":"http://10.0.0.1:8080","token":"t"}`, "uses plain http"},
		{"insecure_http", `{"hub":"http://10.0.0.1:8080","token":"t","insecure_http":true}`, ""},
		{"policy", `{"hub":"https://h","token":"t","probe_allow":["127.0.0.0/8"],"probe_deny":["10.0.0.0/8"]}`, ""},
		{"bad_policy", `{"hub":"https://h","token":"t","probe_deny":["10.1.2.3/8"]}`, "host bits set"},
		{"typo_field", `{"hub":"https://h","token":"t","probe_dney":["10.0.0.0/8"]}`, `unknown field "probe_dney"`},
	} {
		_, err := LoadConfig(writeConfig(t, tc.body))
		if tc.want == "" && err != nil {
			t.Errorf("%s rejected: %v", tc.name, err)
		}
		if tc.want != "" && (err == nil || !strings.Contains(err.Error(), tc.want)) {
			t.Errorf("%s: err=%v, want %q", tc.name, err, tc.want)
		}
	}
}

// ReadConfig 不校验传输规则：configure 要能读出一份被 run 拒绝的配置去修正它。
func TestReadConfigSkipsValidation(t *testing.T) {
	c, err := ReadConfig(writeConfig(t, `{"hub":"http://10.0.0.1:8080","token":"t"}`))
	if err != nil || c.Hub != "http://10.0.0.1:8080" {
		t.Fatalf("%+v %v", c, err)
	}
}

// root 改写服务用户拥有的配置后，属主仍是服务用户。只有 root 能把文件交给别人，所以这条只在 root 下跑；
// 非 root 下跳过会让它恒绿，验收在容器里以 root 跑一次（见提交说明）。
func TestSaveConfigKeepsOwner(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("needs root to hand the file to another user")
	}
	p := filepath.Join(t.TempDir(), "config.json")
	if err := SaveConfig(p, Config{Hub: "https://h", Token: "t"}); err != nil {
		t.Fatal(err)
	}
	if err := os.Chown(p, 65534, 65534); err != nil {
		t.Fatal(err)
	}
	if err := SaveConfig(p, Config{Hub: "https://h", Token: "t2"}); err != nil {
		t.Fatal(err)
	}
	st, err := os.Stat(p)
	if err != nil {
		t.Fatal(err)
	}
	sys := st.Sys().(*syscall.Stat_t)
	if sys.Uid != 65534 || sys.Gid != 65534 || st.Mode().Perm() != 0o600 {
		t.Fatalf("owner=%d:%d perm=%o, want 65534:65534 600", sys.Uid, sys.Gid, st.Mode().Perm())
	}
}
