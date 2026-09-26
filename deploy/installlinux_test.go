package deploy

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// install.sh 的替身。本地账户记录在 $PROBE_INSTALL_ROOT/etc/passwd 与 etc/group 里；
// state/remote-passwd 里的账户由 NSS 的非本地源解析得到：id 查得到，本地文件里没有。
// userdel、groupdel 只改本地文件，找不到记录时照 shadow 4.13 实测的报错与退出码（6）失败。
// systemctl 只记下参数：服务的真实起停由 scripts/install-accept.sh 在容器里验证。
var linuxStubs = map[string]string{
	"id": `#!/bin/sh
S=$STUB_STATE
if [ "$#" = 1 ] && [ "$1" = -u ]; then echo "${STUB_UID:-0}"; exit 0; fi
flag=""; [ "$#" = 2 ] && { flag=$1; shift; }
line=$(grep "^$1:" "$PROBE_INSTALL_ROOT/etc/passwd") || line=$(grep "^$1:" "$S/remote-passwd" 2>/dev/null) || {
  echo "id: '$1': no such user" >&2; exit 1; }
uid=$(echo "$line" | cut -d: -f3); gid=$(echo "$line" | cut -d: -f4)
case "$flag" in
  "") echo "uid=$uid($1) gid=$gid";;
  -u) echo "$uid";;
  -g) echo "$gid";;
esac
`,
	"systemctl": `#!/bin/sh
cat > /dev/null
echo "systemctl $*" >> "$STUB_STATE/calls"
`,
	"userdel": `#!/bin/sh
cat > /dev/null
echo "userdel $*" >> "$STUB_STATE/calls"
f=$PROBE_INSTALL_ROOT/etc/passwd
grep -q "^$1:" "$f" || { echo "userdel: user '$1' does not exist" >&2; exit 6; }
grep -v "^$1:" "$f" > "$f.new"; mv "$f.new" "$f"
`,
	"groupdel": `#!/bin/sh
cat > /dev/null
echo "groupdel $*" >> "$STUB_STATE/calls"
f=$PROBE_INSTALL_ROOT/etc/group
grep -q "^$1:" "$f" || { echo "groupdel: group '$1' does not exist" >&2; exit 6; }
grep -v "^$1:" "$f" > "$f.new"; mv "$f.new" "$f"
`,
	"sleep": `#!/bin/sh
echo "sleep $*" >> "$STUB_STATE/calls"
`,
}

// newLinuxEnv 是一台已装好服务的 systemd 主机：服务用户不在本地账户文件里，由各用例按需加上；
// /proc 下只有 self，没有以任何 uid 运行的进程目录。
func newLinuxEnv(t *testing.T) *env {
	t.Helper()
	e := newStubEnv(t, "install.sh", linuxStubs)
	for rel, body := range map[string]string{
		"run/systemd/system/.keep":               "",
		"proc/self/status":                       "Uid:\t1000\t1000\t1000\t1000\n",
		"etc/passwd":                             "root:x:0:0:root:/root:/bin/sh\n",
		"etc/group":                              "root:x:0:\n",
		"etc/systemd/system/probe-agent.service": "[Service]\n",
		"etc/probe-agent/config.json":            "{}\n",
		"var/log/probe-agent/.keep":              "",
		"usr/local/bin/probe-agent":              "#!/bin/sh\n",
	} {
		e.put(rel, body)
	}
	return e
}

// put 在假根目录下写文件，缺的父目录一并建出。
func (e *env) put(rel, body string) {
	e.t.Helper()
	p := filepath.Join(e.root, rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		e.t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		e.t.Fatal(err)
	}
}

func (e *env) appendTo(rel, line string) {
	e.t.Helper()
	e.put(rel, e.file(rel)+line)
}

func TestLinuxPurgeDeletesTheLocalAccount(t *testing.T) {
	t.Parallel()
	e := newLinuxEnv(t)
	e.appendTo("etc/passwd", "probe-agent:x:480:480::/nonexistent:/usr/sbin/nologin\n")
	e.appendTo("etc/group", "probe-agent:x:480:\n")
	out, code := e.run("--uninstall", "--purge")
	if code != 0 || !strings.Contains(out, "probe-agent uninstalled") {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	for _, rel := range []string{"usr/local/bin/probe-agent", "etc/systemd/system/probe-agent.service", "etc/probe-agent", "var/log/probe-agent"} {
		if e.exists(rel) {
			t.Errorf("%s left behind", rel)
		}
	}
	if strings.Contains(e.file("etc/passwd"), "probe-agent:") || strings.Contains(e.file("etc/group"), "probe-agent:") {
		t.Fatalf("account left in the local files:\n%s%s", e.file("etc/passwd"), e.file("etc/group"))
	}
}

// 解析得到的同名用户不在本地 /etc/passwd 里：不发 userdel（它会以 does not exist 失败、脚本以它的
// 退出码结束），回查仍能看到这个用户，由脚本自己报出删不掉。
func TestLinuxPurgeDecidesDeletionFromTheLocalRecord(t *testing.T) {
	t.Parallel()
	e := newLinuxEnv(t)
	e.write("remote-passwd", "probe-agent:x:480:480::/nonexistent:/usr/sbin/nologin\n")
	out, code := e.run("--uninstall", "--purge")
	if code != 1 || !strings.Contains(out, "failed to delete user or group probe-agent") {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	if index(e.calls(), "userdel") >= 0 {
		t.Fatalf("no local record, no userdel: %q", e.calls())
	}
}
