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
	heronv1 "github.com/xjetry/heron-probe/gen/heron/v1"
	"github.com/xjetry/heron-probe/gen/heron/v1/heronv1connect"
	"github.com/xjetry/heron-probe/internal/testwait"
)

// 子进程执行真实 main，使 os.Exit 与信号处理不会终止测试宿主。
//
// 不并行：子进程里它改进程级的 os.Args 再调用 main。
func TestHubCommandChild(t *testing.T) {
	if os.Getenv("HERON_HUB_COMMAND_CHILD") != "1" {
		return
	}
	for i, arg := range os.Args {
		if arg == "--" {
			os.Args = append([]string{"heron-hub"}, os.Args[i+1:]...)
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
	cmd.Env = append(os.Environ(), "HERON_HUB_COMMAND_CHILD=1")
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
	t.Parallel()
	db := filepath.Join(t.TempDir(), "hub.db")
	password := "command password with enough characters"
	set := hubCommand(t, "passwd", "--db", db)
	set.Stdin = strings.NewReader(password + "\nignored second line\n")
	if out, err := set.CombinedOutput(); err != nil {
		t.Fatalf("passwd command: %v %s", err, out)
	}
	cmd := hubCommand(t, "serve", "--db", db, "--listen", "127.0.0.1:0", "--offline-after", "45s")
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
	testwait.Until(t, 10*time.Millisecond, func() bool {
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
		return addr != ""
	}, "serve command never listened: %s", output)
	client := heronv1connect.NewAdminServiceClient(&http.Client{Timeout: testwait.Bound}, "http://"+addr)
	if _, err := client.Login(context.Background(), connect.NewRequest(&heronv1.LoginRequest{Password: password})); err != nil {
		t.Fatalf("command password cannot authenticate real serve: %v", err)
	}
	// 排空超时的 ctx 在进程收到信号、进入 shutdownHTTP 之后才开始计。
	// 若关停总是等满 drainTimeout，从发信号到退出不会早于它。
	signaled := time.Now()
	if err := cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	select {
	case <-finished:
		if result != nil || !strings.Contains(output.String(), "shutting down") {
			t.Fatalf("serve signal exit: %v %s", result, output.String())
		}
		if elapsed := time.Since(signaled); elapsed >= drainTimeout {
			t.Fatalf("SIGTERM shutdown took %v since signal, want < %v", elapsed, drainTimeout)
		}
	case <-time.After(testwait.Bound):
		t.Fatal("serve command ignored SIGTERM")
	}
}

func TestUsageListsPasswd(t *testing.T) {
	t.Parallel()
	output, err := hubCommand(t).CombinedOutput()
	if err == nil || !strings.Contains(string(output), "passwd") {
		t.Fatalf("usage omitted passwd: err=%v output=%s", err, output)
	}
	if exit, ok := err.(*exec.ExitError); !ok || exit.ExitCode() != 2 {
		t.Fatalf("usage exit: %v", err)
	}
}

func TestSecondInterruptTerminatesWhileDraining(t *testing.T) {
	t.Parallel()
	checkRepeatedSignalDuringDrain(t, syscall.SIGINT)
}

func TestRepeatedTerminationCompletesDrain(t *testing.T) {
	t.Parallel()
	checkRepeatedSignalDuringDrain(t, syscall.SIGTERM)
}

func checkRepeatedSignalDuringDrain(t *testing.T, sig syscall.Signal) {
	t.Helper()
	cmd := hubCommand(t, "serve", "--db", filepath.Join(t.TempDir(), "hub.db"), "--listen", "127.0.0.1:0", "--offline-after", "30s")
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
	testwait.Until(t, 10*time.Millisecond, func() bool {
		for _, field := range strings.Fields(output.String()) {
			if strings.HasPrefix(field, "listen=") {
				addr = strings.Trim(strings.TrimPrefix(field, "listen="), `"`)
			}
		}
		return addr != ""
	}, "serve never listened: %s", output)
	conn, err := net.DialTimeout("tcp", addr, testwait.Bound)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if err := conn.SetDeadline(time.Now().Add(testwait.Bound)); err != nil {
		t.Fatal(err)
	}
	// 100 Continue 证明处理器已开始读请求体；故意不发完整正文，让首次信号进入排空等待。
	if _, err := fmt.Fprintf(conn, "POST /heron.v1.AdminService/Login HTTP/1.1\r\nHost: %s\r\nContent-Type: application/json\r\nContent-Length: 100\r\nExpect: 100-continue\r\n\r\n", addr); err != nil {
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
	// 排空超时的 ctx 在第一次信号被处理、进入 shutdownHTTP 之后才开始计。
	signaled := time.Now()
	if err := cmd.Process.Signal(sig); err != nil {
		t.Fatal(err)
	}
	testwait.Until(t, time.Millisecond, func() bool {
		return strings.Contains(output.String(), "shutting down")
	}, "first interrupt did not start draining: %s", output)
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
		// 负向窗口：第二次 SIGTERM 不应在这段时间内结束排空。窗口短只会漏掉稍晚才被杀掉的缺陷，不会把仍在排空的进程判失败。
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
			// 从第一次发信号起算。缺陷路径等满 drainTimeout 才返回，不会早于它。
			if elapsed := time.Since(signaled); elapsed >= drainTimeout {
				t.Fatalf("SIGTERM drain took %v since first signal, want < %v", elapsed, drainTimeout)
			}
		case <-time.After(testwait.Bound):
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
	case <-time.After(testwait.Bound):
		t.Fatal("second interrupt ignored while draining")
	}
}
