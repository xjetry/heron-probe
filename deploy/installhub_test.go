package deploy

import (
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
)

// install-hub.sh 的替身。服务用户 probe-hub（uid 481）在 $PROBE_INSTALL_ROOT/etc/passwd 与 etc/group 里，
// 不走建账户分支；id、uname、sleep 与 install.sh 的替身相同。
// systemctl 记下参数；start 在假 /proc 里放一个以服务用户运行、exe 指向 /usr/local/bin/probe-hub 的进程 4242，
// stop 把它拿走；show 回答 MainPID 与 DropInPaths（state/dropins 里空格分隔的路径，是目标系统里的真实路径）。
// chown 只记参数：测试以普通用户运行，改不了属主，属主由真机验收回读。chmod 记下参数后转调真的，权限断言看的
// 是真实的文件模式。curl 只认 file:// 地址并复制文件；apt-get 记下参数，install 时建出 CA 证书包。
// systemctl、curl、apt-get 读尽 stdin：脚本以 sh -s 从 stdin 运行，漏掉 </dev/null 的调用会吞掉脚本余下部分，
// 安装在中途无声结束，测试看不到最后一行。真实的 systemd 解析、起停与属主由 scripts/install-accept.sh 在真机上验证。
var hubStubs = map[string]string{
	"id":    linuxStubs["id"],
	"uname": linuxStubs["uname"],
	"sleep": linuxStubs["sleep"],
	"systemctl": `#!/bin/sh
cat > /dev/null
echo "systemctl $*" >> "$STUB_STATE/calls"
P=$PROBE_INSTALL_ROOT/proc
case "$*" in
  "start probe-hub")
    [ -z "${STUB_START_FAILS-}" ] || { echo "Job for probe-hub.service failed." >&2; exit 1; }
    uid=$(grep '^probe-hub:' "$PROBE_INSTALL_ROOT/etc/passwd" | cut -d: -f3)
    mkdir -p "$P/4242"
    printf 'Uid:\t%s\t%s\t%s\t%s\n' "$uid" "$uid" "$uid" "$uid" > "$P/4242/status"
    rm -f "$P/4242/exe"; ln -s /usr/local/bin/probe-hub "$P/4242/exe";;
  "stop probe-hub") rm -rf "$P/4242";;
  "show probe-hub -p MainPID --value") if [ -d "$P/4242" ]; then echo 4242; else echo 0; fi;;
  "show probe-hub -p DropInPaths --value") cat "$STUB_STATE/dropins" 2>/dev/null || echo;;
esac
`,
	// STUB_SWAP_DB 是目录外的一个文件：数据目录交给 root 的那一刻，把 probe.db 换成指向它的硬链接，
	// 模拟停服前预检之后、加锁之前服务组替换了库文件。
	"chown": `#!/bin/sh
echo "chown $*" >> "$STUB_STATE/calls"
d=$PROBE_INSTALL_ROOT/var/lib/probe
if [ -n "${STUB_SWAP_DB-}" ] && [ "$1" = root:probe-hub ] && [ "$2" = "$d" ]; then
  rm -f "$d/probe.db"; ln "$STUB_SWAP_DB" "$d/probe.db"
fi
`,
	"chmod": `#!/bin/sh
echo "chmod $*" >> "$STUB_STATE/calls"
exec /bin/chmod "$@"
`,
	"curl": `#!/bin/sh
cat > /dev/null
echo "curl $*" >> "$STUB_STATE/calls"
out=""; url=""
while [ "$#" -gt 0 ]; do
  case "$1" in
    -o) out=$2; shift 2;;
    --proto|--proto-redir) shift 2;;
    -*) shift;;
    *) url=$1; shift;;
  esac
done
case "$url" in
  file://*) cp "${url#file://}" "$out";;
  *) echo "curl: (6) unexpected URL $url" >&2; exit 6;;
esac
`,
	"apt-get": `#!/bin/sh
cat > /dev/null
echo "apt-get $*" >> "$STUB_STATE/calls"
if [ "$1" = install ]; then
  mkdir -p "$PROBE_INSTALL_ROOT/etc/ssl/certs"
  : > "$PROBE_INSTALL_ROOT/etc/ssl/certs/ca-certificates.crt"
fi
`,
}

const (
	hubUnit = "etc/systemd/system/probe-hub.service"
	hubData = "var/lib/probe"
	hubDone = "probe-hub installed and started (systemd, amd64, probe-hub_linux_amd64.tar.gz)"
	hubLast = "Set the administrator password: probe-hub passwd --db /var/lib/probe/probe.db"
	// /proc/net/tcp 的表头；监听行由用例按需追加。
	tcpHeader = "  sl  local_address rem_address   st tx_queue rx_queue tr tm->when retrnsmt   uid  timeout inode\n"
)

// newHubHost 是一台 systemd 主机：服务用户已在本地账户文件里，有 CA 证书包，没有任何 TCP 监听，
// /proc 下只有 self；dist 里放着 v1 的 hub 包。
func newHubHost(t *testing.T) *env {
	t.Helper()
	e := newStubEnv(t, "install-hub.sh", hubStubs)
	for rel, body := range map[string]string{
		"run/systemd/system/.keep":          "",
		"usr/local/bin/.keep":               "",
		"etc/systemd/system/.keep":          "",
		"etc/ssl/certs/ca-certificates.crt": "",
		"proc/self/status":                  "Uid:\t1000\t1000\t1000\t1000\n",
		"proc/net/tcp":                      tcpHeader,
		"etc/passwd":                        "root:x:0:0:root:/root:/bin/sh\nprobe-hub:x:481:481::/nonexistent:/usr/sbin/nologin\n",
		"etc/group":                         "root:x:0:\nprobe-hub:x:481:\n",
	} {
		e.put(rel, body)
	}
	e.hubRelease("v1")
	return e
}

// hubRelease 按 make release 的形状打 hub 包：probe-hub 与仓库里的 systemd 单元原件。
func (e *env) hubRelease(version string) {
	e.t.Helper()
	unit, err := os.ReadFile("systemd/probe-hub.service")
	if err != nil {
		e.t.Fatal(err)
	}
	e.pack("probe-hub_linux_amd64.tar.gz", []packFile{
		{"probe-hub", "#!/bin/sh\n# " + version + " amd64\n", 0o755},
		{"probe-hub.service", string(unit), 0o644},
	})
}

func (e *env) hubInstall(extra ...string) (string, int) {
	return e.run(append([]string{"--base-url", "file://" + e.dist}, extra...)...)
}

// newHubInstalled 在 newHubHost 上以 v1 装好并启动服务，然后换上 v2 的包、清空调用记录，等用例重跑。
func newHubInstalled(t *testing.T, args ...string) *env {
	t.Helper()
	e := newHubHost(t)
	out, code := e.hubInstall(args...)
	if code != 0 || !strings.Contains(out, hubDone) {
		t.Fatalf("first install exit %d:\n%s", code, out)
	}
	e.hubRelease("v2")
	e.resetCalls()
	return e
}

func (e *env) execStart() string {
	e.t.Helper()
	for _, line := range strings.Split(e.file(hubUnit), "\n") {
		if v, ok := strings.CutPrefix(line, "ExecStart="); ok {
			return v
		}
	}
	e.t.Fatalf("no ExecStart in:\n%s", e.file(hubUnit))
	return ""
}

// setExecStart 把已装单元里的 ExecStart 行换成 lines（可以跨行），其余行保持安装器写出的样子。
func (e *env) setExecStart(lines string) {
	e.t.Helper()
	var out []string
	for _, line := range strings.Split(e.file(hubUnit), "\n") {
		if strings.HasPrefix(line, "ExecStart=") {
			line = lines
		}
		out = append(out, line)
	}
	e.put(hubUnit, strings.Join(out, "\n"))
}

// assertUntouched 断言重跑没有碰正在运行的旧服务：没发 stop、二进制还是 v1、进程 4242 还在。
func (e *env) assertUntouched(out string) {
	e.t.Helper()
	if index(e.calls(), "systemctl stop") >= 0 || !strings.Contains(e.file("usr/local/bin/probe-hub"), "# v1 amd64") || !e.exists("proc/4242/status") {
		e.t.Fatalf("the running hub must be left alone: calls %q\n%s", e.calls(), out)
	}
	if strings.Contains(out, "probe-hub is stopped") {
		e.t.Fatalf("nothing was stopped, but the installer says so:\n%s", out)
	}
}

const hubCmd = `"/usr/local/bin/probe-hub" "serve"`

func TestHubFreshInstallFromStdin(t *testing.T) {
	t.Parallel()
	e := newHubHost(t)
	out, code := e.hubInstall("--listen", "127.0.0.1:9000", "--timezone", "Asia/Taipei")
	// 最后一行在：脚本没有被哪个读 stdin 的调用截断。
	if code != 0 || !strings.Contains(out, hubDone) || !strings.HasSuffix(strings.TrimSpace(out), hubLast) {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	if got, want := e.execStart(), hubCmd+` "--db=/var/lib/probe/probe.db" "--listen=127.0.0.1:9000" "--timezone=Asia/Taipei"`; got != want {
		t.Fatalf("ExecStart=%s\nwant      %s", got, want)
	}
	e.mode(hubData, 0o770)
	e.mode(hubData+"/probe.db", 0o600)
	c := e.calls()
	for _, want := range []string{"chown root:probe-hub " + e.root + "/" + hubData, "chown probe-hub:probe-hub " + e.root + "/" + hubData + "/probe.db"} {
		if !slices.Contains(c, want) {
			t.Errorf("missing %q in calls %q", want, c)
		}
	}
	if index(c, "apt-get") >= 0 {
		t.Errorf("a CA bundle is present; no package manager call expected: %q", c)
	}
	if !strings.Contains(e.file("usr/local/bin/probe-hub"), "# v1 amd64") {
		t.Fatal("v1 was not installed")
	}
}

// 已装单元里 ExecStart 的各种写法：能逐字还原的沿用并按 flag 名与命令行覆盖合并，写回统一成 --flag=value；
// 还原不了或不认识的，在停服前拒绝，旧服务照常运行。
func TestHubExecStartForms(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name     string
		exec     string            // 已装单元里代替 ExecStart 行的内容
		crlf     bool              // 已装单元改成 CRLF 行尾
		dropins  map[string]string // drop-in 的真实路径到内容；空内容表示列出来却不存在
		args     []string
		want     string // 重跑后的 ExecStart；为空时 wantErr 非空
		wantErr  string
		preamble string // 加在已装单元开头的行
	}{
		{name: "inherited", exec: `ExecStart=/usr/local/bin/probe-hub serve --db /var/lib/probe/probe.db --listen 127.0.0.1:9000 --timezone Asia/Taipei --trusted-proxies 10.0.0.0/8`,
			want: hubCmd + ` "--db=/var/lib/probe/probe.db" "--listen=127.0.0.1:9000" "--timezone=Asia/Taipei" "--trusted-proxies=10.0.0.0/8"`},
		{name: "go flag spellings", exec: `ExecStart=/usr/local/bin/probe-hub serve -db /var/lib/probe/probe.db --listen=127.0.0.1:9000 -timezone=UTC --retention-1m 72h`,
			want: hubCmd + ` "--db=/var/lib/probe/probe.db" "--listen=127.0.0.1:9000" "--timezone=UTC" "--retention-1m=72h"`},
		{name: "override replaces by name and appends", exec: `ExecStart=/usr/local/bin/probe-hub serve --db /var/lib/probe/probe.db --timezone Asia/Taipei --listen 127.0.0.1:9000`,
			args: []string{"--listen", "127.0.0.1:9100", "--public-dir", "/srv/site"},
			want: hubCmd + ` "--db=/var/lib/probe/probe.db" "--timezone=Asia/Taipei" "--listen=127.0.0.1:9100" "--public-dir=/srv/site"`},
		{name: "repeated flag keeps its first place and last value", exec: `ExecStart=/usr/local/bin/probe-hub serve --timezone Asia/Taipei --db /var/lib/probe/probe.db --timezone=UTC`,
			want: hubCmd + ` "--timezone=UTC" "--db=/var/lib/probe/probe.db"`},
		{name: "quotes and escapes round-trip", exec: `ExecStart=/usr/local/bin/probe-hub serve --db /var/lib/probe/probe.db "--public-dir=/srv/probe site \"x\" $$literal%% \\end" '--trusted-proxies=10.0.0.0/8'`,
			want: hubCmd + ` "--db=/var/lib/probe/probe.db" "--public-dir=/srv/probe site \"x\" $$literal%% \\end" "--trusted-proxies=10.0.0.0/8"`},
		{name: "override value is kept literally", args: []string{"--public-dir", `/srv/x "y" $z %w \v`},
			want: hubCmd + ` "--db=/var/lib/probe/probe.db" "--listen=127.0.0.1:8080" "--public-dir=/srv/x \"y\" $$z %%w \\v"`},
		{name: "continuation line", exec: "ExecStart=/usr/local/bin/probe-hub serve \\\n  --db /var/lib/probe/probe.db --listen 127.0.0.1:9000",
			want: hubCmd + ` "--db=/var/lib/probe/probe.db" "--listen=127.0.0.1:9000"`},
		{name: "spaces around the equals sign", exec: `ExecStart = /usr/local/bin/probe-hub serve --db /var/lib/probe/probe.db`,
			want: hubCmd + ` "--db=/var/lib/probe/probe.db"`},
		{name: "CRLF line endings", crlf: true,
			want: hubCmd + ` "--db=/var/lib/probe/probe.db" "--listen=127.0.0.1:8080"`},
		{name: "comment naming a path", preamble: "# /var/lib/probe holds the database",
			want: hubCmd + ` "--db=/var/lib/probe/probe.db" "--listen=127.0.0.1:8080"`},
		{name: "drop-in without ExecStart", dropins: map[string]string{"/run/systemd/system/service.d/zzz-lxc-service.conf": "[Service]\nProtectProc=default\n"},
			want: hubCmd + ` "--db=/var/lib/probe/probe.db" "--listen=127.0.0.1:8080"`},

		{name: "single dollar", exec: `ExecStart=/usr/local/bin/probe-hub serve --db /var/lib/probe/probe.db "--public-dir=/srv/$HOME"`,
			wantErr: "dynamic $ or % expansion in ExecStart is not supported"},
		{name: "single percent", exec: `ExecStart=/usr/local/bin/probe-hub serve --db /var/lib/probe/probe.db --public-dir=/srv/%h`,
			wantErr: "dynamic $ or % expansion in ExecStart is not supported"},
		{name: "hex escape", exec: `ExecStart=/usr/local/bin/probe-hub serve --db /var/lib/probe/probe.db "--public-dir=/srv\x20a"`,
			wantErr: "unsupported quoting or escape in ExecStart"},
		{name: "unclosed quote", exec: `ExecStart=/usr/local/bin/probe-hub serve --db /var/lib/probe/probe.db "--public-dir=/srv/a`,
			wantErr: "unsupported quoting or escape in ExecStart"},
		{name: "unknown flag", exec: `ExecStart=/usr/local/bin/probe-hub serve --db /var/lib/probe/probe.db --foo=bar`,
			wantErr: "unsupported serve flag in ExecStart: --foo=bar"},
		{name: "positional argument", exec: `ExecStart=/usr/local/bin/probe-hub serve --db /var/lib/probe/probe.db extra`,
			wantErr: "unexpected positional argument in ExecStart: extra"},
		{name: "another database", exec: `ExecStart=/usr/local/bin/probe-hub serve --db /srv/other.db`,
			wantErr: "installed unit must use --db /var/lib/probe/probe.db"},
		{name: "no database", exec: `ExecStart=/usr/local/bin/probe-hub serve --listen 127.0.0.1:9000`,
			wantErr: "installed unit must use --db /var/lib/probe/probe.db"},
		{name: "another binary", exec: `ExecStart=/usr/bin/probe-hub serve --db /var/lib/probe/probe.db`,
			wantErr: "ExecStart must invoke /usr/local/bin/probe-hub serve"},
		{name: "flag without value", exec: `ExecStart=/usr/local/bin/probe-hub serve --db /var/lib/probe/probe.db --listen`,
			wantErr: "incomplete ExecStart arguments"},
		{name: "ExecStart set twice", exec: "ExecStart=\nExecStart=/usr/local/bin/probe-hub serve --db /var/lib/probe/probe.db",
			wantErr: "installed unit must set ExecStart exactly once"},
		{name: "drop-in sets ExecStart", dropins: map[string]string{"/etc/systemd/system/probe-hub.service.d/override.conf": "[Service]\nExecStart=\nExecStart=/usr/local/bin/probe-hub serve --db /var/lib/probe/probe.db\n"},
			wantErr: "drop-in /etc/systemd/system/probe-hub.service.d/override.conf sets ExecStart"},
		{name: "drop-in sets ExecStart with spaces and CRLF", dropins: map[string]string{"/etc/systemd/system/probe-hub.service.d/override.conf": "[Service] \r\nExecStart = /usr/local/bin/probe-hub serve\r\n"},
			wantErr: "sets ExecStart"},
		{name: "drop-in listed but missing", dropins: map[string]string{"/etc/systemd/system/probe-hub.service.d/gone.conf": ""},
			wantErr: "cannot read probe-hub drop-in /etc/systemd/system/probe-hub.service.d/gone.conf"},
		{name: "line feed in an override", args: []string{"--timezone", "UTC\n--listen=0.0.0.0:80"},
			wantErr: "arguments must not contain line breaks"},
		{name: "carriage return in an override", args: []string{"--timezone", "UTC\r"},
			wantErr: "arguments must not contain line breaks"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			e := newHubInstalled(t)
			if tc.exec != "" {
				e.setExecStart(tc.exec)
			}
			if tc.preamble != "" {
				e.put(hubUnit, tc.preamble+"\n"+e.file(hubUnit))
			}
			if tc.crlf {
				e.put(hubUnit, strings.ReplaceAll(e.file(hubUnit), "\n", "\r\n"))
			}
			var paths []string
			for path, body := range tc.dropins {
				paths = append(paths, path)
				if body != "" {
					e.put(strings.TrimPrefix(path, "/"), body)
				}
			}
			e.write("dropins", strings.Join(paths, " ")+"\n")
			before := e.file(hubUnit)
			out, code := e.hubInstall(tc.args...)
			if tc.wantErr != "" {
				if code != 1 || !strings.Contains(out, tc.wantErr) {
					t.Fatalf("exit %d, want 1 with %q:\n%s", code, tc.wantErr, out)
				}
				e.assertUntouched(out)
				if e.file(hubUnit) != before {
					t.Fatalf("a refused upgrade rewrote the unit:\n%s", e.file(hubUnit))
				}
				return
			}
			if code != 0 || !strings.Contains(out, hubDone) {
				t.Fatalf("exit %d:\n%s", code, out)
			}
			if got := e.execStart(); got != tc.want {
				t.Fatalf("ExecStart=%s\nwant      %s", got, tc.want)
			}
		})
	}
}

// 带同一组参数重跑任意次、再不带参数重跑，单元逐字不变：覆盖按 flag 名替换，不累加。
func TestHubRerunsDoNotAccumulateArguments(t *testing.T) {
	t.Parallel()
	args := []string{"--listen", "127.0.0.1:9000", "--public-dir", `/srv/probe site "x" $literal% \end`, "--timezone", "UTC"}
	e := newHubInstalled(t, args...)
	want := hubCmd + ` "--db=/var/lib/probe/probe.db" "--listen=127.0.0.1:9000" "--public-dir=/srv/probe site \"x\" $$literal%% \\end" "--timezone=UTC"`
	if got := e.execStart(); got != want {
		t.Fatalf("first install ExecStart=%s\nwant                    %s", got, want)
	}
	first := e.file(hubUnit)
	for i, rerun := range [][]string{args, args, nil} {
		out, code := e.hubInstall(rerun...)
		if code != 0 {
			t.Fatalf("rerun %d exit %d:\n%s", i, code, out)
		}
		if got := e.file(hubUnit); got != first {
			t.Fatalf("rerun %d changed the unit:\n%s\nwant\n%s", i, got, first)
		}
	}
}

// 停旧服务之前先查端口：别的进程占着、或查不出持有者时拒绝，旧服务照常运行；持有者是 hub 自己时照常升级。
func TestHubPortConflictLeavesTheServiceRunning(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, holder, wantErr string
	}{
		{"another process", "777", "port 9000 is already in use by pid 777; old service was not stopped"},
		{"listener without a holder", "", "port 9000 is in use; cannot identify listener pid; old service was not stopped"},
		{"the hub itself", "4242", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			e := newHubInstalled(t, "--listen", "127.0.0.1:9000")
			// 9000 = 0x2328；状态 0A 是 LISTEN。
			e.appendTo("proc/net/tcp", "   0: 0100007F:2328 00000000:0000 0A 00000000:00000000 00:00000000 00000000   481        0 5555 1 0000000000000000 100 0 0 10 0\n")
			if tc.holder != "" {
				fd := filepath.Join(e.root, "proc", tc.holder, "fd", "3")
				if err := os.MkdirAll(filepath.Dir(fd), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink("socket:[5555]", fd); err != nil {
					t.Fatal(err)
				}
			}
			out, code := e.hubInstall()
			if tc.wantErr == "" {
				if code != 0 || !strings.Contains(out, hubDone) {
					t.Fatalf("exit %d:\n%s", code, out)
				}
				return
			}
			if code != 1 || !strings.Contains(out, tc.wantErr) {
				t.Fatalf("exit %d:\n%s", code, out)
			}
			e.assertUntouched(out)
		})
	}
}

// 数据目录不是真目录、库文件不是链接数为 1 的普通文件时，在停服前拒绝：旧服务照常运行，不对任何东西
// chown、chmod——链接指向的文件不被改动。
func TestHubDatabaseThatIsNotPlainIsRefusedBeforeStopping(t *testing.T) {
	t.Parallel()
	notPlain := "exists but is not a regular file with a single link"
	for _, tc := range []struct {
		name, rel, want string
		plant           func(path, outside string) error
	}{
		{"database symlink", "probe.db", notPlain, func(p, outside string) error { os.Remove(p); return os.Symlink(outside, p) }},
		{"dangling database symlink", "probe.db", notPlain, func(p, outside string) error { os.Remove(p); return os.Symlink(outside+".missing", p) }},
		// 硬链接：[ -L ] 为假、[ -f ] 为真，只有链接数看得出它另有名字。
		{"database hard link", "probe.db", notPlain, func(p, outside string) error { os.Remove(p); return os.Link(outside, p) }},
		{"WAL symlink", "probe.db-wal", notPlain, func(p, outside string) error { return os.Symlink(outside, p) }},
		{"WAL hard link", "probe.db-wal", notPlain, func(p, outside string) error { return os.Link(outside, p) }},
		{"SHM is a directory", "probe.db-shm", notPlain, func(p, _ string) error { return os.Mkdir(p, 0o755) }},
		{"data directory symlink", "", "exists but is not a directory", func(p, outside string) error {
			if err := os.RemoveAll(p); err != nil {
				return err
			}
			return os.Symlink(filepath.Dir(outside), p)
		}},
		{"data directory is a file", "", "exists but is not a directory", func(p, _ string) error {
			if err := os.RemoveAll(p); err != nil {
				return err
			}
			return os.WriteFile(p, nil, 0o644)
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			e := newHubInstalled(t)
			e.put("etc/outside/probe.db", "root's\n")
			outside := filepath.Join(e.root, "etc/outside/probe.db")
			if err := tc.plant(filepath.Join(e.root, hubData, tc.rel), outside); err != nil {
				t.Fatal(err)
			}
			out, code := e.hubInstall()
			if code != 1 || !strings.Contains(out, tc.want) || !strings.Contains(out, "old service was not stopped") {
				t.Fatalf("exit %d:\n%s", code, out)
			}
			e.assertUntouched(out)
			for _, call := range e.calls() {
				if strings.HasPrefix(call, "chown ") || strings.HasPrefix(call, "chmod ") {
					t.Errorf("%q runs although the upgrade was refused", call)
				}
			}
			e.mode("etc/outside/probe.db", 0o644)
		})
	}
}

// 停服前的预检通过之后、目录交给 root 之前，服务组还能替换其中的条目。替身在目录 chown 的那一刻把 probe.db
// 换成指向目录外文件的硬链接：锁内复检要看到它并拒绝，目录外的文件不被改动，目录留在 0750，报错说明 hub 已停。
func TestHubDatabaseSwappedAfterThePreStopCheckIsRefused(t *testing.T) {
	t.Parallel()
	e := newHubInstalled(t)
	e.put("etc/outside", "root's\n")
	e.vars = []string{"STUB_SWAP_DB=" + filepath.Join(e.root, "etc/outside")}
	out, code := e.hubInstall()
	for _, want := range []string{
		"probe.db exists but is not a regular file with a single link",
		"database files changed after the pre-stop check; " + e.root + "/" + hubData + " stays locked at 0750",
		"probe-hub is stopped; rerun the installer or start it manually",
	} {
		if code != 1 || !strings.Contains(out, want) {
			t.Fatalf("exit %d, want %q:\n%s", code, want, out)
		}
	}
	e.mode("etc/outside", 0o644)
	e.mode(hubData, 0o750)
	c := e.calls()
	if index(c, "chown probe-hub:probe-hub") >= 0 || index(c, "systemctl start") >= 0 {
		t.Fatalf("nothing may be handed to probe-hub or started: calls %q", c)
	}
}

// 库文件的属主与权限每次安装都设：root 手工操作留下的 0644、崩溃留下的 WAL 都在重跑后回到服务用户 0600。
// 顺序承载安全性：停服之后先把目录交给 root 并收成 0750，才对其中的文件 chown、chmod，最后放回 0770。
func TestHubDatabaseOwnershipIsRestoredOnEveryInstall(t *testing.T) {
	t.Parallel()
	e := newHubInstalled(t)
	dir := filepath.Join(e.root, hubData)
	if err := os.Chmod(filepath.Join(dir, "probe.db"), 0o644); err != nil {
		t.Fatal(err)
	}
	e.put(hubData+"/probe.db-wal", "")
	if err := os.Chmod(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	out, code := e.hubInstall()
	if code != 0 || !strings.Contains(out, hubDone) {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	e.mode(hubData, 0o770)
	e.mode(hubData+"/probe.db", 0o600)
	e.mode(hubData+"/probe.db-wal", 0o600)
	c := e.calls()
	at := func(call string) int {
		i := slices.Index(c, call)
		if i < 0 {
			t.Fatalf("missing %q in calls %q", call, c)
		}
		return i
	}
	stop := at("systemctl stop probe-hub")
	take := at("chown root:probe-hub " + dir)
	lock := at("chmod 0750 " + dir)
	release := at("chmod 0770 " + dir)
	start := at("systemctl start probe-hub")
	if !(stop < take && take < lock && lock < release && release < start) {
		t.Fatalf("want stop < chown root:probe-hub < chmod 0750 < chmod 0770 < start, calls %q", c)
	}
	for _, f := range []string{"probe.db", "probe.db-wal"} {
		for _, call := range []string{"chown probe-hub:probe-hub " + dir + "/" + f, "chmod 0600 " + dir + "/" + f} {
			if i := at(call); i < lock || i > release {
				t.Errorf("%q must run while the directory is locked, calls %q", call, c)
			}
		}
	}
}

// hub 自己的 Telegram 出站走 https：下载地址不是 https 时也要有 CA 证书包，没有就装。
func TestHubCABundleIsInstalledForPlainDownloads(t *testing.T) {
	t.Parallel()
	e := newHubHost(t)
	if err := os.Remove(filepath.Join(e.root, "etc/ssl/certs/ca-certificates.crt")); err != nil {
		t.Fatal(err)
	}
	out, code := e.hubInstall()
	if code != 0 || !strings.Contains(out, hubDone) {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	if c := e.calls(); !slices.Contains(c, "apt-get install -y ca-certificates") {
		t.Fatalf("file:// download on a host without a CA bundle must still install ca-certificates, calls %q", c)
	}
}

// 停服务之后的失败补一句 hub 已停；停服务之前的失败与成功都不说。
func TestHubSaysItIsStoppedOnlyAfterStopping(t *testing.T) {
	t.Parallel()
	e := newHubInstalled(t)
	e.vars = []string{"STUB_START_FAILS=1"}
	out, code := e.hubInstall()
	if code == 0 || !strings.Contains(out, "Job for probe-hub.service failed.") || !strings.Contains(out, "probe-hub is stopped; rerun the installer or start it manually") {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	e.vars = nil
	out, code = e.hubInstall()
	if code != 0 || strings.Contains(out, "probe-hub is stopped") {
		t.Fatalf("a successful rerun must not say the hub is stopped: exit %d:\n%s", code, out)
	}
	e.resetCalls()
	out, code = e.hubInstall("--listen", "127.0.0.1:http")
	if code != 1 || !strings.Contains(out, "listen address must end in a numeric TCP port") {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	if index(e.calls(), "systemctl stop") >= 0 || !e.exists("proc/4242/status") || strings.Contains(out, "probe-hub is stopped") {
		t.Fatalf("a failure before stopping must leave the hub running and not say it is stopped: calls %q\n%s", e.calls(), out)
	}
}

// 没有终端时卸载必须带 --yes：不带时什么都不停；带上时停服务、删单元与二进制，保留数据与账户。
func TestHubUninstallWithoutATerminalNeedsYes(t *testing.T) {
	t.Parallel()
	e := newHubInstalled(t)
	out, code := e.run("--uninstall")
	if code != 1 || !strings.Contains(out, "uninstall requires --yes without a terminal") || index(e.calls(), "systemctl") >= 0 {
		t.Fatalf("exit %d, calls %q:\n%s", code, e.calls(), out)
	}
	out, code = e.run("--uninstall", "--yes")
	if code != 0 || !strings.Contains(out, "probe-hub uninstalled") {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	if e.exists(hubUnit) || e.exists("usr/local/bin/probe-hub") || !e.exists(hubData+"/probe.db") || !strings.Contains(e.file("etc/passwd"), "probe-hub:") {
		t.Fatal("uninstall must remove the unit and binary and keep the data and account")
	}
}

// 安装器认得的 serve 参数（SERVE_FLAGS 加上固定的 --db）与 cmd/hub/serve.go 定义的 flag 是同一张表的两处写法。
// serve 加了参数而安装器没加时，装过这个参数的单元升级会在停服前被拒绝；反过来安装器会写出 serve 不认的参数，
// hub 起不来。usage 也要列出每个可覆盖的参数。
func TestHubFlagTableAgreesWithServe(t *testing.T) {
	t.Parallel()
	src, err := os.ReadFile("../cmd/hub/serve.go")
	if err != nil {
		t.Fatal(err)
	}
	var serve []string
	for _, m := range regexp.MustCompile(`\bfs\.[A-Za-z]+\((?:&[^,]+,\s*)?"([^"]+)"`).FindAllStringSubmatch(string(src), -1) {
		serve = append(serve, m[1])
	}
	script, err := os.ReadFile("install-hub.sh")
	if err != nil {
		t.Fatal(err)
	}
	m := regexp.MustCompile(`(?m)^SERVE_FLAGS='([^']*)'$`).FindStringSubmatch(string(script))
	if m == nil {
		t.Fatal("install-hub.sh has no SERVE_FLAGS line")
	}
	overridable := strings.Fields(m[1])
	installer := append([]string{"db"}, overridable...)
	slices.Sort(serve)
	slices.Sort(installer)
	if !slices.Equal(serve, installer) {
		t.Fatalf("serve defines %q; the installer knows %q", serve, installer)
	}
	for _, f := range overridable {
		if !strings.Contains(string(script), "[--"+f+" ") {
			t.Errorf("usage does not list --%s", f)
		}
	}
}

// 命令行只接受 SERVE_FLAGS 里的参数：--db 固定、表外的参数与 --flag=value 写法都以用法错误退出，什么都不做。
// 带空格的名字按子串会命中参数表（" listen timezone " 在表里），写进单元就是 serve 不认的参数。
func TestHubCommandLineRefusesFlagsOutsideTheTable(t *testing.T) {
	t.Parallel()
	for _, args := range [][]string{
		{"--db", "/srv/other.db"},
		{"--foo", "bar"},
		{"--listen=127.0.0.1:9000"},
		{"--listen timezone", "x"},
		{"--*", "x"},
	} {
		e := newHubHost(t)
		out, code := e.hubInstall(args...)
		// calls 为空文件时 e.calls() 是一个空串。
		if c := e.calls(); code != 2 || !strings.Contains(out, "usage: install-hub.sh") || len(c) != 1 || c[0] != "" {
			t.Fatalf("%q: exit %d, calls %q:\n%s", args, code, c, out)
		}
	}
}
