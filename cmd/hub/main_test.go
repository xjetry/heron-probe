package main

import (
	"bytes"
	"context"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"connectrpc.com/connect"
	probev1 "github.com/xjetry/probe/gen/probe/v1"
	"github.com/xjetry/probe/gen/probe/v1/probev1connect"
)

// 子进程执行真实 main，使 os.Exit 与信号处理不会终止测试宿主。
func TestHubCommandChild(t *testing.T) {
	if os.Getenv("PROBE_HUB_COMMAND_CHILD") != "1" {
		return
	}
	for i, arg := range os.Args {
		if arg == "--" {
			os.Args = append([]string{"probe-hub"}, os.Args[i+1:]...)
			main()
			os.Exit(0)
		}
	}
	t.Fatal("missing child argument delimiter")
}

func hubCommand(t *testing.T, args ...string) *exec.Cmd {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(exe, append([]string{"-test.run=^TestHubCommandChild$", "--"}, args...)...)
	cmd.Env = append(os.Environ(), "PROBE_HUB_COMMAND_CHILD=1")
	return cmd
}

type commandOutput struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *commandOutput) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}
func (b *commandOutput) String() string { b.mu.Lock(); defer b.mu.Unlock(); return b.buf.String() }

func TestMainPasswdServeAndSignal(t *testing.T) {
	t.Setenv("PROBE_OFFLINE_AFTER", "45s")
	db := filepath.Join(t.TempDir(), "hub.db")
	password := "command password with enough characters"
	set := hubCommand(t, "passwd", "--db", db)
	set.Stdin = strings.NewReader(password + "\nignored second line\n")
	if out, err := set.CombinedOutput(); err != nil {
		t.Fatalf("passwd command: %v %s", err, out)
	}
	cmd := hubCommand(t, "serve", "--db", db, "--listen", "127.0.0.1:0")
	output := &commandOutput{}
	cmd.Stdout, cmd.Stderr = output, output
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	finished := make(chan struct{})
	var result error
	go func() { result = cmd.Wait(); close(finished) }()
	defer func() { _ = cmd.Process.Kill(); <-finished }()
	var addr string
	deadline := time.Now().Add(5 * time.Second)
	for addr == "" && time.Now().Before(deadline) {
		for _, field := range strings.Fields(output.String()) {
			if strings.HasPrefix(field, "listen=") {
				addr = strings.Trim(strings.TrimPrefix(field, "listen="), `"`)
			}
		}
		select {
		case <-finished:
			t.Fatalf("serve command exited before ready: %v %s", result, output.String())
		default:
		}
		if addr == "" {
			time.Sleep(10 * time.Millisecond)
		}
	}
	if addr == "" {
		t.Fatalf("serve command never listened: %s", output.String())
	}
	client := probev1connect.NewAdminServiceClient(&http.Client{Timeout: 2 * time.Second}, "http://"+addr)
	if _, err := client.Login(context.Background(), connect.NewRequest(&probev1.LoginRequest{Password: password})); err != nil {
		t.Fatalf("command password cannot authenticate real serve: %v", err)
	}
	if err := cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	select {
	case <-finished:
		if result != nil || !strings.Contains(output.String(), "shutting down") {
			t.Fatalf("serve signal exit: %v %s", result, output.String())
		}
	case <-time.After(5 * time.Second):
		t.Fatal("serve command ignored SIGTERM")
	}
}

func TestUsageListsPasswd(t *testing.T) {
	output, err := hubCommand(t).CombinedOutput()
	if err == nil || !strings.Contains(string(output), "passwd") {
		t.Fatalf("usage omitted passwd: err=%v output=%s", err, output)
	}
	if exit, ok := err.(*exec.ExitError); !ok || exit.ExitCode() != 2 {
		t.Fatalf("usage exit: %v", err)
	}
}
