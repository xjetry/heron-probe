// probe-agent：register 用注册窗口 key 换 token 并写入配置；configure 修改宿主机本地策略；run 进入上报循环。
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	iofs "io/fs"
	"log/slog"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"connectrpc.com/connect"

	probev1 "github.com/xjetry/probe/gen/probe/v1"
	"github.com/xjetry/probe/internal/agent/agentlog"
	"github.com/xjetry/probe/internal/agent/client"
	"github.com/xjetry/probe/internal/agent/collect"
	"github.com/xjetry/probe/internal/agent/prober"
	"github.com/xjetry/probe/internal/clock"
)

var version = "dev"

const defaultConfig = "/etc/probe-agent/config.json"

const usage = "usage: probe-agent register|configure|run|version [flags]"

// requestTimeout 是单次 RPC 的上限：hub 不应答时一次上报至多挂这么久才进入退避。
// initialInterval 是收到 hub 第一个响应之前的上报间隔，也是这段时间里 client.Backoff 的基数。
// 两者都写进启动行，scripts/e2e.sh 据此推出告警恢复的等待上限。
const (
	requestTimeout  = 15 * time.Second
	initialInterval = 10 * time.Second
)

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, usage)
		os.Exit(2)
	}
	var err error
	switch os.Args[1] {
	case "register":
		err = runRegister(os.Args[2:])
	case "configure":
		err = runConfigure(os.Args[2:], os.Stdout)
	case "run":
		err = runRun(os.Args[2:])
	case "version":
		fmt.Println(version)
	default:
		fmt.Fprintln(os.Stderr, usage)
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func runRegister(args []string) error {
	fs := flag.NewFlagSet("register", flag.ContinueOnError)
	hub := fs.String("hub", "", "hub base URL, e.g. https://probe.example.com")
	key := fs.String("key", "", "registration key from `probe-hub window open`")
	name := fs.String("name", "", "node name (default: hostname)")
	cfgPath := fs.String("config", defaultConfig, "where to write the agent config")
	insecure := fs.Bool("insecure-http", false, "accept a plain http hub address (the node token and metrics travel unencrypted)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *hub == "" || *key == "" {
		return errors.New("--hub and --key are required")
	}
	if *name == "" {
		*name, _ = os.Hostname()
	}
	base := strings.TrimRight(*hub, "/")
	// 校验在发请求之前：明文地址被拒时 key 不能已经出过线，窗口名额也不能已经消耗。
	if err := client.CheckHub(base, *insecure); err != nil {
		return err
	}
	// 重新注册换的是 hub 身份，本地探测策略属于宿主机，沿用已有配置里的；已有配置读不出来时报错，不静默丢掉它。
	cfg := client.Config{Hub: base, Name: *name, InsecureHTTP: *insecure}
	if old, err := client.ReadConfig(*cfgPath); err == nil {
		cfg.ProbeAllow, cfg.ProbeDeny = old.ProbeAllow, old.ProbeDeny
	} else if !errors.Is(err, iofs.ErrNotExist) {
		return fmt.Errorf("existing config: %w", err)
	}
	if _, err := cfg.Policy(); err != nil {
		return fmt.Errorf("existing config: %w", err)
	}
	c := client.NewServiceClient(base, requestTimeout)
	resp, err := c.Register(context.Background(), connect.NewRequest(&probev1.RegisterRequest{Key: *key, Name: *name}))
	if err != nil {
		return fmt.Errorf("register: %w", err)
	}
	cfg.Token = resp.Msg.Token
	if err := client.SaveConfig(*cfgPath, cfg); err != nil {
		return err
	}
	fmt.Printf("registered as node %d; config written to %s\n", resp.Msg.NodeId, *cfgPath)
	return nil
}

func runRun(args []string) error {
	fs := flag.NewFlagSet("run", flag.ContinueOnError)
	cfgPath := fs.String("config", defaultConfig, "agent config path")
	include := fs.String("net-include", "", "comma-separated interface globs to count (exclusive)")
	exclude := fs.String("net-exclude", "", "comma-separated interface globs to skip (default: "+strings.Join(collect.DefaultNetExclude(), ", ")+")")
	if err := fs.Parse(args); err != nil {
		return err
	}
	cfg, err := client.LoadConfig(*cfgPath)
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}
	policy, err := cfg.Policy()
	if err != nil {
		return err
	}
	targets := prober.Targets{Policy: policy}
	clk := clock.Real()
	col, err := collect.NewPlatform(version, clk, splitList(*include), splitList(*exclude))
	if err != nil {
		return err
	}
	log := newLogger(os.Stderr)
	log.Info("probe target policy", "policy", policy.String())
	ic := prober.NewICMP(clk, log)
	ic.Targets = targets
	// defer 逆序执行，先停止调度再关闭 socket，避免仍在运行的任务入队 icmp closed。
	defer ic.Close()
	col.IcmpAvailable = ic.Available()
	if !ic.Available() {
		log.Warn("icmp probing unavailable; icmp tasks will report errors", "reasons", ic.InitErrors())
	}
	queue := prober.NewQueue(prober.QueueCap)
	sched := prober.NewScheduler(prober.Multi{ICMP: ic, TCP: prober.TCP{Clock: clk, Targets: targets}}, queue, clk, log)
	defer sched.Stop()
	r := &client.Runner{
		Collector: col,
		Client:    client.NewServiceClient(cfg.Hub, requestTimeout),
		Token:     cfg.Token,
		Clock:     clk,
		Log:       log,
		Interval:  initialInterval,
		Prober:    sched,
		Results:   queue,
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	logStarting(log, cfg.Hub)
	if err := r.Run(ctx); err != nil && !errors.Is(err, context.Canceled) {
		return err
	}
	return nil
}

// newLogger 是 run 的日志装配。启动行的文本格式有外部读者（scripts/e2e.sh 按整秒读字段），测试经同一个
// 函数装配日志，才钉得住读者实际看到的格式。agentlog 给全部输出设速率与长度上界：不少行由 hub 的应答触发（§5.7）。
func newLogger(w io.Writer) *slog.Logger {
	return slog.New(agentlog.New(slog.NewTextHandler(w, nil), nil))
}

func logStarting(log *slog.Logger, hub string) {
	log.Info("agent starting", "hub", hub, "request_timeout", requestTimeout, "initial_interval", initialInterval, "version", version)
}

func splitList(s string) []string {
	if s == "" {
		return nil
	}
	var out []string
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// runConfigure 修改配置里的宿主机本地策略（§4.8）。只改命令行上显式给出的项，列表给空串即清空；
// 读取时不校验，才能修正一份当前被 run 拒绝的配置；写入前按 run 的同一套规则校验整份配置。
func runConfigure(args []string, out io.Writer) error {
	fs := flag.NewFlagSet("configure", flag.ContinueOnError)
	cfgPath := fs.String("config", defaultConfig, "agent config path")
	insecure := fs.Bool("insecure-http", false, "accept a plain http hub address (the node token and metrics travel unencrypted)")
	allow := fs.String("probe-allow", "", "comma-separated CIDR prefixes the agent may probe despite the defaults; empty clears")
	deny := fs.String("probe-deny", "", "comma-separated CIDR prefixes the agent refuses to probe in addition to the defaults; empty clears")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() > 0 {
		return fmt.Errorf("configure: unexpected argument %q", fs.Arg(0))
	}
	cfg, err := client.ReadConfig(*cfgPath)
	if err != nil {
		return err
	}
	fs.Visit(func(f *flag.Flag) {
		switch f.Name {
		case "insecure-http":
			cfg.InsecureHTTP = *insecure
		case "probe-allow":
			cfg.ProbeAllow = splitList(*allow)
		case "probe-deny":
			cfg.ProbeDeny = splitList(*deny)
		}
	})
	if err := cfg.Validate(); err != nil {
		return err
	}
	if err := client.SaveConfig(*cfgPath, cfg); err != nil {
		return err
	}
	policy, _ := cfg.Policy()
	fmt.Fprintf(out, "insecure_http=%v %s\nrestart the probe-agent service to apply\n", cfg.InsecureHTTP, policy)
	return nil
}
