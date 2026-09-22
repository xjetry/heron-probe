package main

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"net"
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

func TestSecondInterruptTerminatesWhileDraining(t *testing.T) {
	checkRepeatedSignalDuringDrain(t, syscall.SIGINT)
}

func TestRepeatedTerminationCompletesDrain(t *testing.T) {
	checkRepeatedSignalDuringDrain(t, syscall.SIGTERM)
}

func checkRepeatedSignalDuringDrain(t *testing.T, sig syscall.Signal) {
	t.Helper()
	t.Setenv("PROBE_OFFLINE_AFTER", "30s")
	cmd := hubCommand(t, "serve", "--db", filepath.Join(t.TempDir(), "hub.db"), "--listen", "127.0.0.1:0")
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
		if addr == "" {
			time.Sleep(10 * time.Millisecond)
		}
	}
	if addr == "" {
		t.Fatalf("serve never listened: %s", output.String())
	}
	conn, err := net.DialTimeout("tcp", addr, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if err := conn.SetDeadline(time.Now().Add(5 * time.Second)); err != nil {
		t.Fatal(err)
	}
	// 100 Continue 证明处理器已开始读请求体；故意不发完整正文，让首次信号进入排空等待。
	if _, err := fmt.Fprintf(conn, "POST /probe.v1.AdminService/Login HTTP/1.1\r\nHost: %s\r\nContent-Type: application/json\r\nContent-Length: 100\r\nExpect: 100-continue\r\n\r\n", addr); err != nil {
		t.Fatal(err)
	}
	reader := bufio.NewReader(conn)
	response, err := http.ReadResponse(reader, nil)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusContinue {
		t.Fatalf("handler did not begin reading: %s", response.Status)
	}
	if err := cmd.Process.Signal(sig); err != nil {
		t.Fatal(err)
	}
	deadline = time.Now().Add(2 * time.Second)
	for !strings.Contains(output.String(), "shutting down") && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if !strings.Contains(output.String(), "shutting down") {
		t.Fatalf("first interrupt did not start draining: %s", output.String())
	}
	select {
	case <-finished:
		t.Fatalf("first interrupt did not wait for active request: %v", result)
	default:
	}
	if err := cmd.Process.Signal(sig); err != nil {
		t.Fatal(err)
	}
	if sig == syscall.SIGTERM {
		select {
		case <-finished:
			t.Fatalf("second SIGTERM bypassed draining: %v", result)
		case <-time.After(150 * time.Millisecond):
		}
		// 只有发送剩余正文后请求才可完成；正常退出必须等待它，而不是被重复 SIGTERM 杀死。
		if _, err := fmt.Fprint(conn, "{}"+strings.Repeat(" ", 98)); err != nil {
			t.Fatal(err)
		}
		response, err = http.ReadResponse(reader, nil)
		if err != nil {
			t.Fatalf("drained request lost response: %v", err)
		}
		response.Body.Close()
		if response.StatusCode != http.StatusUnauthorized {
			t.Fatalf("drained request status=%d, want 401", response.StatusCode)
		}
		select {
		case <-finished:
			if result != nil {
				t.Fatalf("SIGTERM drain did not exit cleanly: %v", result)
			}
		case <-time.After(3 * time.Second):
			t.Fatal("SIGTERM drain did not finish after request completed")
		}
		return
	}
	select {
	case <-finished:
		status, ok := cmd.ProcessState.Sys().(syscall.WaitStatus)
		if !ok || !status.Signaled() || status.Signal() != syscall.SIGINT {
			t.Fatalf("second interrupt was not default termination: %v (%v)", status, result)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("second interrupt ignored while draining")
	}
}
