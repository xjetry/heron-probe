package update

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func TestSystemCredentialDrop(t *testing.T) {
	if os.Getenv("HERON_UPDATE_ACCEPT") != "isolated-systemd" {
		t.Skip("only run in the dedicated disposable systemd machine")
	}
	if _, err := os.Stat("/var/lib/heron-update-accept"); err != nil {
		t.Fatal(err)
	}
	status, err := os.ReadFile("/proc/self/status")
	if err != nil || !strings.Contains(string(status), "NoNewPrivs:\t1\n") {
		t.Fatalf("test requires active NoNewPrivileges: %s %v", status, err)
	}
	u, err := user.Lookup("heron-" + os.Getenv("HERON_UPDATE_ROLE"))
	if err != nil {
		t.Fatal(err)
	}
	uid, _ := strconv.Atoi(u.Uid)
	gid, _ := strconv.Atoi(u.Gid)
	if uid <= 0 || gid <= 0 {
		t.Fatal("service identity must be non-root")
	}
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.CommandContext(t.Context(), exe, "-test.run=^TestSystemCredentialChild$", "-test.count=1", "-test.v")
	cmd.Env = []string{"HERON_CREDENTIAL_CHILD=1", "HERON_EXPECT_UID=" + u.Uid, "HERON_EXPECT_GID=" + u.Gid}
	cmd.SysProcAttr = credentials(uid, gid)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("service-user version execution failed: %v\n%s", err, out)
	}
	if !strings.Contains(string(out), "HERON_CREDENTIAL_CHILD_VERIFIED\n") {
		t.Fatalf("credential child did not execute its assertions: %s", out)
	}
	t.Log(string(out))
	fmt.Println("HERON_CREDENTIAL_PARENT_VERIFIED")
}

func TestSystemCredentialChild(t *testing.T) {
	if os.Getenv("HERON_CREDENTIAL_CHILD") != "1" {
		t.Skip("only run as the credential probe child")
	}
	if strconv.Itoa(os.Getuid()) != os.Getenv("HERON_EXPECT_UID") || strconv.Itoa(os.Geteuid()) != os.Getenv("HERON_EXPECT_UID") || strconv.Itoa(os.Getgid()) != os.Getenv("HERON_EXPECT_GID") || strconv.Itoa(os.Getegid()) != os.Getenv("HERON_EXPECT_GID") {
		t.Fatalf("version subprocess did not drop to service identity: uid=%d euid=%d gid=%d egid=%d", os.Getuid(), os.Geteuid(), os.Getgid(), os.Getegid())
	}
	groups, err := os.Getgroups()
	if err != nil || len(groups) != 0 {
		t.Fatalf("supplementary groups retained: %v %v", groups, err)
	}
	b, err := os.ReadFile("/proc/self/status")
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"CapPrm", "CapEff", "CapAmb"} {
		if !strings.Contains(string(b), name+":\t0000000000000000\n") {
			t.Fatalf("version subprocess retained usable capabilities: %s", b)
		}
	}
	if !strings.Contains(string(b), "NoNewPrivs:\t1\n") {
		t.Fatal("version subprocess lost NoNewPrivileges")
	}
	if err := unix.Setuid(0); !errors.Is(err, unix.EPERM) {
		t.Fatalf("version subprocess can regain root: %v", err)
	}
	fmt.Println("HERON_CREDENTIAL_CHILD_VERIFIED")
}

// 受控产物只链接进测试程序，发行更新器没有读取测试目录或环境变量的路径。
type acceptanceSource struct{}

func (acceptanceSource) Download(_ context.Context, role, _, version string) ([]byte, error) {
	if !ValidVersion(version) || (role != "hub" && role != "agent") {
		return nil, errors.New("invalid fixture")
	}
	return os.ReadFile(filepath.Join("/var/lib/heron-update-fixtures", version, role))
}

func TestSystemDaemon(t *testing.T) {
	if os.Getenv("HERON_UPDATE_ACCEPT") != "isolated-systemd" {
		t.Skip("only run in the dedicated disposable systemd machine")
	}
	if _, err := os.Stat("/var/lib/heron-update-accept"); err != nil {
		t.Fatal("missing disposable machine marker")
	}
	if err := serve(context.Background(), os.Getenv("HERON_UPDATE_ROLE"), acceptanceSource{}); err != nil {
		t.Fatal(err)
	}
}

func TestSystemVerificationBinding(t *testing.T) {
	if os.Getenv("HERON_UPDATE_ACCEPT") != "isolated-systemd" {
		t.Skip("only run in the dedicated disposable systemd machine")
	}
	if _, err := os.Stat("/var/lib/heron-update-accept"); err != nil {
		t.Fatal(err)
	}
	m := &systemMachine{unit: "heron-hub.service"}
	value, err := m.command(t.Context(), "show", m.unit, "--property=MainPID", "--value")
	if err != nil {
		t.Fatal(err)
	}
	pid, err := strconv.Atoi(value)
	if err != nil || pid <= 0 {
		t.Fatalf("no running hub: %q %v", value, err)
	}
	b, err := os.ReadFile("/usr/local/bin/heron-hub")
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(b)
	digest := hex.EncodeToString(sum[:])
	if err := m.Verify(t.Context(), pid, digest); err != nil {
		t.Fatal(err)
	}
	if err := m.Verify(t.Context(), os.Getpid(), digest); err == nil {
		t.Fatal("accepted non-main process")
	}
	if err := m.Verify(t.Context(), pid, "wrong-digest"); err == nil {
		t.Fatal("accepted different executable digest")
	}
}

func TestOfficialNetworkAcceptance(t *testing.T) {
	if os.Getenv("HERON_UPDATE_ACCEPT") != "isolated-systemd" {
		t.Skip("only run in the dedicated disposable systemd machine")
	}
	if _, err := os.Stat("/var/lib/heron-update-accept"); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Minute)
	defer cancel()
	s := NewOfficialSource()
	latest, err := s.Latest(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("official latest: %s", latest)
	for _, role := range []string{"hub", "agent"} {
		b, err := s.Download(ctx, role, "arm64", "v0.2.0")
		if err != nil {
			t.Fatal(err)
		}
		t.Logf("official v0.2.0 %s arm64: %d bytes, sha256=%x", role, len(b), sha256.Sum256(b))
	}
}
