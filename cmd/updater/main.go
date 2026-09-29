package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/xjetry/heron-probe/internal/update"
)

var version = "dev"

func main() {
	if len(os.Args) == 2 && os.Args[1] == "version" {
		fmt.Println(version)
		return
	}
	role := flag.String("role", "", "managed service: hub or agent")
	maintenance := flag.Bool("maintenance", false, "reserve the local updater for a root installer")
	flag.Parse()
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()
	var err error
	if *role != "hub" && *role != "agent" {
		err = fmt.Errorf("role must be hub or agent")
	} else if *maintenance {
		err = update.NewClient(*role).Maintenance(ctx)
	} else {
		err = update.Serve(ctx, *role)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
