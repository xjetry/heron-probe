// Package deploy 的测试以普通用户运行安装脚本：脚本读写的系统路径经 PROBE_INSTALL_ROOT 挂到临时目录，
// 系统管理命令由 PATH 上的替身接管。本文件测 install-macos.sh：dscl、launchctl、ps、id、sysctl、uname、
// chown、sleep 是替身，curl、shasum、tar 用真的；真实 launchd、目录服务与 root 属主只在真机上验证
// （spec §14：没有 macOS 虚拟机可用）。install.sh 的替身在 installlinux_test.go。
package deploy

import (
	"archive/tar"
	"compress/gzip"
	"crypto/sha256"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

const svcUser = "_probe-agent"

// 替身共用的状态目录：users/<名> 存 "uid gid"，groups/<名> 存 "gid"，procs 每行 "uid pid comm"，
// loaded 表示作业已载入，calls 按调用顺序记下每个替身收到的参数。
// launchd 起的 agent 的 comm 是 plist 的 ProgramArguments[0]；STUB_HELPER 另起一个同 uid 的 cfprefsd，
// 它不属于作业，bootout 不带走它（launchd 按 uid 派生的辅助进程就是这样）。
// dscl、launchctl 与假 agent 读尽 stdin：脚本以 sh -s 从 stdin 运行，
// 漏掉 </dev/null 的调用会吞掉脚本余下部分，安装在中途无声结束，测试看不到最后一行。
var stubs = map[string]string{
	"id": `#!/bin/sh
S=$STUB_STATE
if [ "$#" = 1 ] && [ "$1" = -u ]; then echo "${STUB_UID:-0}"; exit 0; fi
flag=""; [ "$#" = 2 ] && { flag=$1; shift; }
f="$S/users/$1"
# remote-users 是解析得到、但不在本地节点里的账户（别的目录节点的同名账户，或陈旧的缓存）。
[ -f "$f" ] || f="$S/remote-users/$1"
[ -f "$f" ] || { echo "id: $1: no such user" >&2; exit 1; }
read -r uid gid < "$f"
case "$flag" in
  "") echo "uid=$uid($1) gid=$gid";;
  -u) echo "$uid";;
  -g) echo "$gid";;
  -gn)
    for g in "$S"/groups/*; do
      [ -f "$g" ] || continue
      if [ "$(cat "$g")" = "$gid" ]; then basename "$g"; exit 0; fi
    done
    echo "id: group $gid not found" >&2; exit 1;;
esac
`,
	"dscl": `#!/bin/sh
cat > /dev/null
S=$STUB_STATE
echo "dscl $*" >> "$S/calls"
[ "$1" = . ] || exit 64
op=$2; path=$3; key=${4-}; val=${5-}
kind=${path#/}; kind=${kind%%/*}; name=${path##*/}
case "$op" in
  -list)
    [ -z "${STUB_DSCL_LIST_FAILS-}" ] || { echo "list failed" >&2; exit 71; }
    cat "$S/sys-$kind" 2>/dev/null
    for f in "$S/$(echo "$kind" | tr 'UG' 'ug')"/*; do
      [ -f "$f" ] || continue
      read -r a b < "$f"
      echo "$(basename "$f") $a"
    done;;
  -read)
    f="$S/$(echo "$kind" | tr 'UG' 'ug')/$name"
    [ -f "$f" ] || { echo "<dscl_cmd> DS Error: -14136 (eDSRecordNotFound)" >&2; exit 56; }
    read -r a b < "$f"; echo "$key: $a";;
  -create)
    case "$kind/$key" in
      Groups/PrimaryGroupID) echo "$val" > "$S/groups/$name";;
      Users/UniqueID) [ -n "${STUB_DSCL_DROP_USER-}" ] || echo "$val" > "$S/users/$name";;
      Users/PrimaryGroupID) [ -f "$S/users/$name" ] && { read -r u g < "$S/users/$name"; echo "$u $val" > "$S/users/$name"; };;
    esac;;
  -delete)
    f="$S/$(echo "$kind" | tr 'UG' 'ug')/$name"
    [ -f "$f" ] || { echo "<main> delete status: eDSRecordNotFound" >&2; exit 56; }
    [ -n "${STUB_DSCL_KEEP-}" ] || rm -f "$f";;
esac
exit 0
`,
	"launchctl": `#!/bin/sh
cat > /dev/null
S=$STUB_STATE
echo "launchctl $*" >> "$S/calls"
case "$1" in
  print) [ -f "$S/loaded" ] && exit 0; echo "Could not find service \"${2#*/}\" in domain for system" >&2; exit 113;;
  bootout)
    [ -f "$S/loaded" ] || { echo "Boot-out failed: 3: No such process" >&2; exit 3; }
    rm -f "$S/loaded"
    if [ -z "${STUB_BOOTOUT_LEAVES_PROCESS-}" ]; then
      grep -v ' /usr/local/bin/probe-agent$' "$S/procs" > "$S/procs.new"; mv "$S/procs.new" "$S/procs"
    fi
    exit 0;;
  enable) exit 0;;
  bootstrap)
    [ -f "$3" ] || { echo "Bootstrap failed: 2: No such file or directory" >&2; exit 5; }
    cp "$3" "$S/bootstrapped.plist"
    : > "$S/loaded"
    read -r uid gid < "$S/users/_probe-agent"
    [ -z "${STUB_HELPER-}" ] || grep -q ' 999 ' "$S/procs" || echo "$uid 999 /usr/libexec/cfprefsd" >> "$S/procs"
    [ -n "${STUB_START_FAILS-}" ] && exit 0
    echo "$uid 4242 /usr/local/bin/probe-agent" >> "$S/procs"
    exit 0;;
esac
exit 64
`,
	"ps": `#!/bin/sh
S=$STUB_STATE
[ "$*" = "-axo uid=,pid=,comm=" ] || { echo "unexpected ps $*" >&2; exit 64; }
echo "    0     1 /sbin/launchd"
echo "  501   777 /Applications/Some App.app/Contents/MacOS/Some App"
if [ -n "${STUB_RESPAWN-}" ] && [ -s "$S/procs" ]; then
  awk '$3 == "/usr/local/bin/probe-agent" { $2 = $2 + 1 } { print }' "$S/procs" > "$S/procs.new" && mv "$S/procs.new" "$S/procs"
fi
cat "$S/procs" 2>/dev/null
exit 0
`,
	"sysctl": `#!/bin/sh
[ "$*" = "-in hw.optional.arm64" ] || { echo "unexpected sysctl $*" >&2; exit 64; }
printf '%s' "${STUB_ARM64-}"
[ -z "${STUB_ARM64-}" ] || echo
`,
	"uname": `#!/bin/sh
[ "$*" = -m ] || { echo "unexpected uname $*" >&2; exit 64; }
echo "${STUB_UNAME_M:-arm64}"
`,
	"chown": `#!/bin/sh
echo "chown $*" >> "$STUB_STATE/calls"
`,
	"sleep": `#!/bin/sh
echo "sleep $*" >> "$STUB_STATE/calls"
`,
}

// fakeAgent 是包里的 probe-agent：register 写出配置并记下参数，与真 agent 的 SaveConfig 同为 0600。
const fakeAgent = `#!/bin/sh
cat > /dev/null
echo "probe-agent $*" >> "$STUB_STATE/calls"
[ "$1" = register ] || exit 0
while [ $# -gt 0 ]; do [ "$1" = --config ] && cfg=$2; shift; done
mkdir -p "$(dirname "$cfg")"
echo '{"hub":"h","token":"t"}' > "$cfg"
chmod 0600 "$cfg"
echo "registered as node 1; config written to $cfg"
`

type env struct {
	t                *testing.T
	script           string
	root, state, bin string
	dist             string
	vars             []string
}

// newStubEnv 建出临时的根目录、替身状态目录与放替身的 PATH 目录，脚本与替身集由调用方给。
func newStubEnv(t *testing.T, script string, stubs map[string]string) *env {
	t.Helper()
	d := t.TempDir()
	e := &env{t: t, script: script, root: filepath.Join(d, "root"), state: filepath.Join(d, "state"), bin: filepath.Join(d, "bin"), dist: filepath.Join(d, "dist")}
	for _, dir := range []string{e.root, e.state, e.bin, e.dist} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	for name, body := range stubs {
		if err := os.WriteFile(filepath.Join(e.bin, name), []byte(body), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	e.write("calls", "")
	return e
}

func newEnv(t *testing.T) *env {
	t.Helper()
	e := newStubEnv(t, "install-macos.sh", stubs)
	for _, dir := range []string{"users", "groups"} {
		if err := os.MkdirAll(filepath.Join(e.state, dir), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	// Apple 的系统账户从 300 往上连号占用（macOS 26.3.1 本机到 308），两个命名空间合起来占满 300–308；
	// 取号从 499 往下，不取紧挨着的 309。系统账户不在替身状态里，只出现在 -list 的输出中。
	e.write("sys-Users", "_taken300 300\n_taken302 302\n_taken304 304\n_taken306 306\n_taken308 308\n")
	e.write("sys-Groups", "_taken301 301\n_taken303 303\n_taken305 305\n_taken307 307\n")
	e.write("procs", "")
	e.release("arm64", "v1")
	e.release("amd64", "v1")
	return e
}

func (e *env) write(name, body string) {
	e.t.Helper()
	if err := os.WriteFile(filepath.Join(e.state, name), []byte(body), 0o644); err != nil {
		e.t.Fatal(err)
	}
}

// release 按 make release 的形状打包：包内是 probe-agent 与仓库里的 plist 原件。
func (e *env) release(arch, version string) {
	e.t.Helper()
	plist, err := os.ReadFile("launchd/xyz.probe.agent.plist")
	if err != nil {
		e.t.Fatal(err)
	}
	pkg := "probe-agent_darwin_" + arch + ".tar.gz"
	f, err := os.Create(filepath.Join(e.dist, pkg))
	if err != nil {
		e.t.Fatal(err)
	}
	gz := gzip.NewWriter(f)
	tw := tar.NewWriter(gz)
	for _, m := range []struct {
		name string
		body string
		mode int64
	}{{"probe-agent", fakeAgent + "# " + version + " " + arch + "\n", 0o755}, {"xyz.probe.agent.plist", string(plist), 0o644}} {
		if err := tw.WriteHeader(&tar.Header{Name: m.name, Mode: m.mode, Size: int64(len(m.body))}); err != nil {
			e.t.Fatal(err)
		}
		tw.Write([]byte(m.body))
	}
	tw.Close()
	gz.Close()
	f.Close()
	e.sums()
}

func (e *env) sums() {
	e.t.Helper()
	var b strings.Builder
	for _, arch := range []string{"amd64", "arm64"} {
		pkg := "probe-agent_darwin_" + arch + ".tar.gz"
		data, err := os.ReadFile(filepath.Join(e.dist, pkg))
		if err != nil {
			continue
		}
		fmt.Fprintf(&b, "%x  %s\n", sha256.Sum256(data), pkg)
	}
	if err := os.WriteFile(filepath.Join(e.dist, "SHA256SUMS"), []byte(b.String()), 0o644); err != nil {
		e.t.Fatal(err)
	}
}

// run 以面板命令的形态执行：脚本来自 stdin（sh -s --）。
func (e *env) run(args ...string) (string, int) {
	e.t.Helper()
	script, err := os.Open(e.script)
	if err != nil {
		e.t.Fatal(err)
	}
	defer script.Close()
	cmd := exec.Command("sh", append([]string{"-s", "--"}, args...)...)
	cmd.Stdin = script
	cmd.Env = append(os.Environ(), "PATH="+e.bin+string(os.PathListSeparator)+os.Getenv("PATH"),
		"PROBE_INSTALL_ROOT="+e.root, "STUB_STATE="+e.state)
	cmd.Env = append(cmd.Env, e.vars...)
	out, err := cmd.CombinedOutput()
	code := 0
	if ee, ok := err.(*exec.ExitError); ok {
		code = ee.ExitCode()
	} else if err != nil {
		e.t.Fatal(err)
	}
	return string(out), code
}

func (e *env) install(extra ...string) (string, int) {
	return e.run(append([]string{"--hub", "http://hub.test", "--key", "k", "--base-url", "file://" + e.dist}, extra...)...)
}

func (e *env) calls() []string {
	e.t.Helper()
	b, err := os.ReadFile(filepath.Join(e.state, "calls"))
	if err != nil {
		e.t.Fatal(err)
	}
	return strings.Split(strings.TrimSpace(string(b)), "\n")
}

func (e *env) resetCalls() { e.write("calls", "") }

// index 返回第一条以 prefix 开头的调用的位置，没有时为 -1。
func index(calls []string, prefix string) int {
	return slices.IndexFunc(calls, func(c string) bool { return strings.HasPrefix(c, prefix) })
}

func (e *env) file(rel string) string {
	e.t.Helper()
	b, err := os.ReadFile(filepath.Join(e.root, rel))
	if err != nil {
		e.t.Fatalf("%s: %v", rel, err)
	}
	return string(b)
}

func (e *env) exists(rel string) bool {
	_, err := os.Stat(filepath.Join(e.root, rel))
	return err == nil
}

const done = "probe-agent installed and started (launchd, arm64, probe-agent_darwin_arm64.tar.gz)"

func TestFreshInstallFromStdin(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	out, code := e.install("--name", "mac-1")
	if code != 0 || !strings.Contains(out, done) {
		t.Fatalf("exit %d, want the final line %q:\n%s", code, done, out)
	}
	c := e.calls()
	// 从 499 往下的第一个空闲号同时给组与用户，不取 Apple 追加前沿上的 309；组先于用户建。
	if g, u := index(c, "dscl . -create /Groups/_probe-agent PrimaryGroupID 499"), index(c, "dscl . -create /Users/_probe-agent UniqueID 499"); g < 0 || u < g {
		t.Fatalf("group then user with id 499, got %q", c)
	}
	reg := index(c, "probe-agent register --hub http://hub.test --key k --config "+e.root+"/etc/probe-agent/config.json --name mac-1")
	boot := index(c, "launchctl bootstrap system "+e.root+"/Library/LaunchDaemons/xyz.probe.agent.plist")
	if reg < 0 || boot < reg || index(c, "launchctl bootout") >= 0 {
		t.Fatalf("register before bootstrap and no bootout on a fresh host, got %q", c)
	}
	for _, want := range []string{
		"chown root:_probe-agent " + e.root + "/etc/probe-agent",
		"chown _probe-agent:_probe-agent " + e.root + "/etc/probe-agent/config.json",
		"chown root:wheel " + e.root + "/Library/LaunchDaemons/xyz.probe.agent.plist",
		"chown root:wheel " + e.root + "/Library/Logs/probe-agent",
		"chown _probe-agent:_probe-agent " + e.root + "/Library/Logs/probe-agent/probe-agent.log",
		"chown _probe-agent:_probe-agent " + e.root + "/Library/Logs/probe-agent/probe-agent.err",
		"launchctl enable system/xyz.probe.agent",
	} {
		if i := index(c, want); i < 0 || i > boot {
			t.Errorf("missing %q before bootstrap in %q", want, c)
		}
	}
	if !strings.Contains(e.file("usr/local/bin/probe-agent"), "# v1 arm64") {
		t.Fatal("installed binary is not the arm64 package's")
	}
	e.mode("etc/probe-agent", 0o750)
	e.mode("etc/probe-agent/config.json", 0o600)
	// 目录属 root，两个日志文件由脚本建好交给服务用户：launchd 不论以哪个身份打开都写得进去。
	e.mode("Library/Logs/probe-agent", 0o755)
	e.mode("Library/Logs/probe-agent/probe-agent.log", 0o640)
	e.mode("Library/Logs/probe-agent/probe-agent.err", 0o640)
}

func (e *env) mode(rel string, want os.FileMode) {
	e.t.Helper()
	st, err := os.Lstat(filepath.Join(e.root, rel))
	if err != nil {
		e.t.Fatalf("%s: %v", rel, err)
	}
	if st.Mode().Perm() != want || !st.Mode().IsRegular() && !st.IsDir() {
		e.t.Fatalf("%s: mode %v; want %v", rel, st.Mode(), want)
	}
}

// 499 只被用作 GID 也不能取：组与用户同号，要两个命名空间都空闲。
func TestFreeIDMustBeFreeInBothNamespaces(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	e.write("sys-Groups", "_taken301 301\n_taken303 303\n_taken305 305\n_taken307 307\n_taken499 499\n")
	if out, code := e.install(); code != 0 {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	if c := e.calls(); index(c, "dscl . -create /Users/_probe-agent UniqueID 498") < 0 {
		t.Fatalf("want id 498, got %q", c)
	}
}

// 上次安装在建组之后、建用户之前中断：沿用已有的组，用户另取一个空闲号，不再建组。
func TestExistingGroupWithoutUserIsReused(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	e.write("groups/_probe-agent", "310\n")
	if out, code := e.install(); code != 0 {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	c := e.calls()
	if index(c, "dscl . -create /Groups/") >= 0 {
		t.Fatalf("the existing group must be reused, got %q", c)
	}
	if index(c, "dscl . -create /Users/_probe-agent PrimaryGroupID 310") < 0 || index(c, "dscl . -create /Users/_probe-agent UniqueID 499") < 0 {
		t.Fatalf("user must join group 310 with a free id, got %q", c)
	}
}

// 同名用户已在但主组不是同名组：plist 的 GroupName 与配置目录的组对不上，退出且不建组。
func TestExistingUserWithWrongPrimaryGroupIsRefused(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	e.write("users/_probe-agent", "450 20\n")
	e.write("groups/staff", "20\n")
	out, code := e.install()
	if code != 1 || !strings.Contains(out, "exists with primary group staff; expected _probe-agent") {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	if index(e.calls(), "dscl . -create") >= 0 || e.exists("usr/local/bin/probe-agent") {
		t.Fatal("nothing may be created or downloaded")
	}
}

// 配置与日志文件的属主、权限每次安装都设，不只在首次注册之后：手工重新注册以 root 重写配置、
// 注册之后被打断、人工编辑改了权限，重跑都要修回来。
func TestOwnershipIsRestoredOnEveryInstall(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	if out, code := e.install(); code != 0 {
		t.Fatalf("first install exit %d:\n%s", code, out)
	}
	os.Chmod(filepath.Join(e.root, "etc/probe-agent"), 0o755)
	os.Chmod(filepath.Join(e.root, "etc/probe-agent/config.json"), 0o644)
	os.Chmod(filepath.Join(e.root, "Library/Logs/probe-agent"), 0o700)
	os.Chmod(filepath.Join(e.root, "Library/Logs/probe-agent/probe-agent.err"), 0o644)
	// 日志文件被删后，以服务用户身份打开它的 launchd 建不回来；重跑安装脚本把它建回来。
	os.Remove(filepath.Join(e.root, "Library/Logs/probe-agent/probe-agent.log"))
	e.resetCalls()
	if out, code := e.install(); code != 0 {
		t.Fatalf("rerun exit %d:\n%s", code, out)
	}
	c := e.calls()
	if index(c, "probe-agent register") >= 0 {
		t.Fatalf("rerun must not register: %q", c)
	}
	out, boot := index(c, "launchctl bootout"), index(c, "launchctl bootstrap")
	for _, want := range []string{
		"chown root:_probe-agent " + e.root + "/etc/probe-agent",
		"chown _probe-agent:_probe-agent " + e.root + "/etc/probe-agent/config.json",
		"chown root:wheel " + e.root + "/Library/Logs/probe-agent",
		"chown _probe-agent:_probe-agent " + e.root + "/Library/Logs/probe-agent/probe-agent.log",
		"chown _probe-agent:_probe-agent " + e.root + "/Library/Logs/probe-agent/probe-agent.err",
	} {
		if i := index(c, want); i < out || i > boot {
			t.Errorf("rerun: %q must run between bootout and bootstrap, calls %q", want, c)
		}
	}
	e.mode("etc/probe-agent", 0o750)
	e.mode("etc/probe-agent/config.json", 0o600)
	e.mode("Library/Logs/probe-agent", 0o755)
	e.mode("Library/Logs/probe-agent/probe-agent.log", 0o640)
	e.mode("Library/Logs/probe-agent/probe-agent.err", 0o640)
}

// launchd 以服务 uid 派生的辅助进程（cfprefsd、trustd……）不是本服务：按 uid 加可执行路径认进程，
// 升级与卸载不会因为它们等满 10 秒后失败，也不会把它们当成已启动的 agent。
func TestHelperProcessesOfTheServiceUserAreNotTheService(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	e.vars = []string{"STUB_HELPER=1"}
	if out, code := e.install(); code != 0 || !strings.Contains(out, done) {
		t.Fatalf("install exit %d:\n%s", code, out)
	}
	e.release("arm64", "v2")
	if out, code := e.install(); code != 0 || !strings.Contains(out, done) {
		t.Fatalf("upgrade with a helper process still running: exit %d:\n%s", code, out)
	}
	if out, code := e.run("--uninstall"); code != 0 {
		t.Fatalf("uninstall with a helper process still running: exit %d:\n%s", code, out)
	}
}

func TestHelperProcessDoesNotCountAsStarted(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	e.vars = []string{"STUB_HELPER=1", "STUB_START_FAILS=1"}
	if out, code := e.install(); code != 1 || !strings.Contains(out, "probe-agent did not start") {
		t.Fatalf("only a helper process running must not count as started: exit %d:\n%s", code, out)
	}
}

// 包里的 plist 与脚本的常量是同一件事的两处写法：脚本按 Label 管作业、按 UserName 的 uid 认进程、
// 把二进制放到 ProgramArguments[0]、建 StandardErrorPath 所在的目录。
func TestPlistAgreesWithScript(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	if out, code := e.install(); code != 0 {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	p := e.file("Library/LaunchDaemons/xyz.probe.agent.plist")
	for _, want := range []string{
		"<key>Label</key>\n\t<string>xyz.probe.agent</string>",
		"<string>/usr/local/bin/probe-agent</string>",
		"<string>/etc/probe-agent/config.json</string>",
		"<key>UserName</key>\n\t<string>" + svcUser + "</string>",
		"<key>GroupName</key>\n\t<string>" + svcUser + "</string>",
		"<string>/Library/Logs/probe-agent/probe-agent.err</string>",
	} {
		if !strings.Contains(p, want) {
			t.Errorf("plist lacks %q", want)
		}
	}
	for _, rel := range []string{"usr/local/bin/probe-agent", "etc/probe-agent/config.json", "Library/Logs/probe-agent"} {
		if !e.exists(rel) {
			t.Errorf("script did not create %s, which the plist refers to", rel)
		}
	}
}

func TestRerunUpgradesWithoutRegistering(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	if out, code := e.install(); code != 0 {
		t.Fatalf("first install exit %d:\n%s", code, out)
	}
	e.release("arm64", "v2")
	e.resetCalls()
	out, code := e.install("--name", "ignored")
	if code != 0 || !strings.Contains(out, "keeping the current registration (--key ignored)") || !strings.Contains(out, "--name ignored") {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	c := e.calls()
	if index(c, "probe-agent register") >= 0 || index(c, "dscl . -create") >= 0 {
		t.Fatalf("rerun must neither register nor recreate the account: %q", c)
	}
	if out := index(c, "launchctl bootout system/xyz.probe.agent"); out < 0 || out > index(c, "launchctl bootstrap") {
		t.Fatalf("a loaded job must be booted out before the new one is bootstrapped: %q", c)
	}
	if !strings.Contains(e.file("usr/local/bin/probe-agent"), "# v2 arm64") {
		t.Fatal("binary was not replaced")
	}
}

func TestArchitectureFromHardwareNotShell(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		arm64, uname, pkg string
	}{
		{"1", "x86_64", "arm64"}, // Rosetta 转译的终端：uname -m 报 x86_64，硬件是 Apple Silicon
		{"", "x86_64", "amd64"},  // Intel：hw.optional.arm64 不存在
	} {
		e := newEnv(t)
		e.vars = []string{"STUB_ARM64=" + tc.arm64, "STUB_UNAME_M=" + tc.uname}
		out, code := e.install()
		if code != 0 || !strings.Contains(out, "probe-agent_darwin_"+tc.pkg+".tar.gz)") {
			t.Fatalf("arm64=%q uname=%q: exit %d, want the %s package:\n%s", tc.arm64, tc.uname, code, tc.pkg, out)
		}
	}
}

// 作业被手工 bootout 后 plist 还在：没有可停的作业，bootout 会以 3 失败，重跑不能因此失败。
func TestUnloadedJobIsNotBootedOut(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	if out, code := e.install(); code != 0 {
		t.Fatalf("first install exit %d:\n%s", code, out)
	}
	os.Remove(filepath.Join(e.state, "loaded"))
	e.write("procs", "")
	e.resetCalls()
	out, code := e.install()
	if code != 0 || !strings.Contains(out, done) {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	if index(e.calls(), "launchctl bootout") >= 0 {
		t.Fatalf("an unloaded job must not be booted out: %q", e.calls())
	}
}

func TestChecksumMismatchLeavesRunningServiceAlone(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	if out, code := e.install(); code != 0 {
		t.Fatalf("first install exit %d:\n%s", code, out)
	}
	sums := filepath.Join(e.dist, "SHA256SUMS")
	os.WriteFile(sums, []byte(strings.Repeat("0", 64)+"  probe-agent_darwin_arm64.tar.gz\n"), 0o644)
	e.resetCalls()
	out, code := e.install()
	if code == 0 || !strings.Contains(out, "FAILED") {
		t.Fatalf("exit %d, want shasum failure:\n%s", code, out)
	}
	if index(e.calls(), "launchctl bootout") >= 0 {
		t.Fatalf("a bad download must not stop the running service: %q", e.calls())
	}
	os.WriteFile(sums, []byte(strings.Repeat("0", 64)+"  probe-agent_darwin_amd64.tar.gz\n"), 0o644)
	if out, code := e.install(); code == 0 || !strings.Contains(out, "SHA256SUMS has no entry for probe-agent_darwin_arm64.tar.gz") {
		t.Fatalf("exit %d:\n%s", code, out)
	}
}

// dscl 退出码为 0 却没建出用户：回查挡住，下载与注册都还没发生。
func TestAccountCreationIsVerified(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	e.vars = []string{"STUB_DSCL_DROP_USER=1"}
	out, code := e.install()
	if code != 1 || !strings.Contains(out, "failed to create system user _probe-agent") {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	if index(e.calls(), "probe-agent register") >= 0 || e.exists("usr/local/bin/probe-agent") {
		t.Fatal("nothing may be downloaded or registered after the account check fails")
	}
}

func TestLingeringProcessBlocksReplacement(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	if out, code := e.install(); code != 0 {
		t.Fatalf("first install exit %d:\n%s", code, out)
	}
	e.release("arm64", "v2")
	e.vars = []string{"STUB_BOOTOUT_LEAVES_PROCESS=1"}
	out, code := e.install()
	if code != 1 || !strings.Contains(out, "probe-agent is still running: processes with uid 499 (_probe-agent): 4242") {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	if !strings.Contains(e.file("usr/local/bin/probe-agent"), "# v1 arm64") {
		t.Fatal("binary must not be replaced while the old process runs")
	}
}

func TestStartIsConfirmedBySamePID(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct{ knob, want string }{
		{"STUB_START_FAILS=1", "probe-agent did not start"},
		{"STUB_RESPAWN=1", "probe-agent did not stay running (pid 4243)"},
	} {
		e := newEnv(t)
		e.vars = []string{tc.knob}
		out, code := e.install()
		if code != 1 || !strings.Contains(out, tc.want) || !strings.Contains(out, "/Library/Logs/probe-agent/probe-agent.err") {
			t.Fatalf("%s: exit %d, want %q and the log path:\n%s", tc.knob, code, tc.want, out)
		}
	}
}

func TestUninstallAndPurge(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	if out, code := e.install(); code != 0 {
		t.Fatalf("install exit %d:\n%s", code, out)
	}
	if out, code := e.run("--purge"); code != 2 {
		t.Fatalf("--purge alone must be a usage error, exit %d:\n%s", code, out)
	}
	out, code := e.run("--uninstall")
	if code != 0 || e.exists("usr/local/bin/probe-agent") || e.exists("Library/LaunchDaemons/xyz.probe.agent.plist") {
		t.Fatalf("uninstall exit %d:\n%s", code, out)
	}
	if !e.exists("etc/probe-agent/config.json") || !e.exists("../state/users/_probe-agent") {
		t.Fatal("plain uninstall keeps config and account")
	}
	e.vars = []string{"STUB_DSCL_KEEP=1"}
	if out, code := e.run("--uninstall", "--purge"); code != 1 || !strings.Contains(out, "failed to delete user or group _probe-agent") {
		t.Fatalf("undeleted account must fail purge, exit %d:\n%s", code, out)
	}
	e.vars = nil
	if out, code := e.run("--uninstall", "--purge"); code != 0 || e.exists("etc/probe-agent") || e.exists("Library/Logs/probe-agent") || e.exists("../state/users/_probe-agent") || e.exists("../state/groups/_probe-agent") {
		t.Fatalf("purge exit %d:\n%s", code, out)
	}
}

func TestRefusals(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	e.vars = []string{"STUB_UID=501"}
	if out, code := e.install(); code != 1 || !strings.Contains(out, "must run as root") {
		t.Fatalf("non-root: exit %d:\n%s", code, out)
	}
	e.vars = nil
	if out, code := e.run("--base-url", "file://"+e.dist); code != 2 || !strings.Contains(out, "--hub and --key are required for the first install") {
		t.Fatalf("missing credentials: exit %d:\n%s", code, out)
	}
	if out, code := e.run("--hub"); code != 2 {
		t.Fatalf("flag without value: exit %d:\n%s", code, out)
	}
}

// 解析得到的同名用户不在本地节点里：不对本地节点发 -delete（它会以 eDSRecordNotFound 失败、
// 脚本以 dscl 的退出码结束），回查仍能看到这个用户，由脚本自己报出删不掉。
func TestPurgeDecidesDeletionFromTheLocalRecord(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	if err := os.MkdirAll(filepath.Join(e.state, "remote-users"), 0o755); err != nil {
		t.Fatal(err)
	}
	e.write("remote-users/_probe-agent", "480 480\n")
	out, code := e.run("--uninstall", "--purge")
	if code != 1 || !strings.Contains(out, "failed to delete user or group _probe-agent") {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	if index(e.calls(), "dscl . -delete /Users/_probe-agent") >= 0 {
		t.Fatalf("no local record, no -delete: %q", e.calls())
	}
}

// 目录服务读不出账户列表与号段耗尽是两回事，报错各说各的。
func TestAccountFailuresNameTheirCause(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	e.vars = []string{"STUB_DSCL_LIST_FAILS=1"}
	if out, code := e.install(); code != 1 || !strings.Contains(out, "cannot list user ids") || strings.Contains(out, "no free id") {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	f := newEnv(t)
	var b strings.Builder
	for i := 300; i < 500; i++ {
		fmt.Fprintf(&b, "_taken%d %d\n", i, i)
	}
	f.write("sys-Users", b.String())
	if out, code := f.install(); code != 1 || !strings.Contains(out, "no free id in 300-499 for _probe-agent") {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	g := newEnv(t)
	g.write("groups/_probe-agent", "\n")
	if out, code := g.install(); code != 1 || !strings.Contains(out, "group _probe-agent has no PrimaryGroupID") {
		t.Fatalf("exit %d:\n%s", code, out)
	}
}

// 会让脚本中止的前置检查都在停服务之前：日志目录不是真目录、日志文件不是普通文件时拒绝，
// 旧服务照常运行、二进制不被替换，也不对链接指向的对象 chown。
func TestLogPathsThatAreNotPlainAreRefusedBeforeStopping(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, rel, want string
		plant           func(path, root string)
	}{
		{"directory symlink", "Library/Logs/probe-agent", "exists but is not a directory", func(p, root string) {
			os.RemoveAll(p)
			os.Symlink(filepath.Join(root, "etc"), p)
		}},
		{"file symlink", "Library/Logs/probe-agent/probe-agent.err", "exists but is not a regular file", func(p, root string) {
			os.Remove(p)
			os.Symlink(filepath.Join(root, "etc/probe-agent/config.json"), p)
		}},
		{"file is a directory", "Library/Logs/probe-agent/probe-agent.log", "exists but is not a regular file", func(p, root string) {
			os.Remove(p)
			os.Mkdir(p, 0o755)
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			e := newEnv(t)
			if out, code := e.install(); code != 0 {
				t.Fatalf("first install exit %d:\n%s", code, out)
			}
			tc.plant(filepath.Join(e.root, tc.rel), e.root)
			e.release("arm64", "v2")
			e.resetCalls()
			out, code := e.install()
			if code != 1 || !strings.Contains(out, tc.want) {
				t.Fatalf("exit %d:\n%s", code, out)
			}
			c := e.calls()
			if index(c, "launchctl bootout") >= 0 || index(c, "chown") >= 0 || !strings.Contains(e.file("usr/local/bin/probe-agent"), "# v1 arm64") {
				t.Fatalf("the running service must be left alone: calls %q", c)
			}
		})
	}
}
