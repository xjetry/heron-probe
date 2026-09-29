package update

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"
)

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
