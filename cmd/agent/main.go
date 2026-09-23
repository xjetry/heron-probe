// probe-agent：register 用注册窗口 key 换 token 并写入配置；run 进入上报循环。
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"connectrpc.com/connect"

	probev1 "github.com/xjetry/probe/gen/probe/v1"
	"github.com/xjetry/probe/gen/probe/v1/probev1connect"
	"github.com/xjetry/probe/internal/agent/client"
	"github.com/xjetry/probe/internal/agent/collect"
	"github.com/xjetry/probe/internal/agent/prober"
	"github.com/xjetry/probe/internal/clock"
)

var version = "dev"

const defaultConfig = "/etc/probe-agent/config.json"

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: probe-agent register|run|version [flags]")
		os.Exit(2)
	}
	var err error
	switch os.Args[1] {
	case "register":
		err = runRegister(os.Args[2:])
	case "run":
		err = runRun(os.Args[2:])
	case "version":
		fmt.Println(version)
	default:
		fmt.Fprintln(os.Stderr, "usage: probe-agent register|run|version [flags]")
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
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *hub == "" || *key == "" {
		return errors.New("--hub and --key are required")
	}
	if *name == "" {
		*name, _ = os.Hostname()
	}
	c := probev1connect.NewAgentServiceClient(&http.Client{Timeout: 15 * time.Second}, strings.TrimRight(*hub, "/"))
	resp, err := c.Register(context.Background(), connect.NewRequest(&probev1.RegisterRequest{Key: *key, Name: *name}))
	if err != nil {
		return fmt.Errorf("register: %w", err)
	}
	if err := client.SaveConfig(*cfgPath, client.Config{Hub: strings.TrimRight(*hub, "/"), Token: resp.Msg.Token, Name: *name}); err != nil {
		return err
	}
	fmt.Printf("registered as node %d; config written to %s\n", resp.Msg.NodeId, *cfgPath)
	return nil
}

func runRun(args []string) error {
	fs := flag.NewFlagSet("run", flag.ContinueOnError)
	cfgPath := fs.String("config", defaultConfig, "agent config path")
	include := fs.String("net-include", "", "comma-separated interface globs to count (exclusive)")
	exclude := fs.String("net-exclude", "", "comma-separated interface globs to skip (default: lo, docker*, veth*, br-*, virbr*)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	cfg, err := client.LoadConfig(*cfgPath)
	if err != nil {
		return fmt.Errorf("load config: %w (run `probe-agent register` first)", err)
	}
	clk := clock.Real()
	col, err := collect.NewPlatform(version, clk, splitList(*include), splitList(*exclude))
	if err != nil {
		return err
	}
	log := slog.New(slog.NewTextHandler(os.Stderr, nil))
	ic := prober.NewICMP(clk, log)
	// defer 逆序执行，先停止调度再关闭 socket，避免仍在运行的任务入队 icmp closed。
	defer ic.Close()
	col.IcmpAvailable = ic.Available()
	if !ic.Available() {
		log.Warn("icmp probing unavailable; icmp tasks will report errors", "reasons", ic.InitErrors())
	}
	queue := prober.NewQueue(prober.QueueCap)
	sched := prober.NewScheduler(prober.Multi{ICMP: ic, TCP: prober.TCP{Clock: clk}}, queue, clk, log)
	defer sched.Stop()
	r := &client.Runner{
		Collector: col,
		Client:    probev1connect.NewAgentServiceClient(&http.Client{Timeout: 15 * time.Second}, cfg.Hub),
		Token:     cfg.Token,
		Clock:     clk,
		Log:       log,
		Interval:  10 * time.Second,
		Prober:    sched,
		Results:   queue,
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	log.Info("agent starting", "hub", cfg.Hub, "version", version)
	if err := r.Run(ctx); err != nil && !errors.Is(err, context.Canceled) {
		return err
	}
	return nil
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
