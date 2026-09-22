package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"text/tabwriter"

	"github.com/xjetry/probe/internal/clock"
	"github.com/xjetry/probe/internal/hub/auth"
	"github.com/xjetry/probe/internal/hub/store"
)

func openOffline(db string) (*store.Store, *auth.Auth, error) {
	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	st, err := store.Open(db, clock.Real(), log)
	if err != nil {
		return nil, nil, err
	}
	a := auth.New(st, clock.Real(), log)
	if err := a.Load(context.Background()); err != nil {
		st.Close()
		return nil, nil, err
	}
	return st, a, nil
}

const restartNotice = "note: if the hub is running, restart it for this change to take effect (the token map is rebuilt at startup)"

func runNode(args []string) error {
	if len(args) < 1 {
		return errors.New("usage: probe-hub node create|list|delete|rotate-token [flags]")
	}
	fs := flag.NewFlagSet("node "+args[0], flag.ContinueOnError)
	db := fs.String("db", "probe.db", "SQLite database path")
	name := fs.String("name", "", "node name (create)")
	id := fs.Int64("id", 0, "node id (delete, rotate-token)")
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}
	st, a, err := openOffline(*db)
	if err != nil {
		return err
	}
	defer st.Close()
	ctx := context.Background()
	switch args[0] {
	case "create":
		if *name == "" {
			return errors.New("--name is required")
		}
		nid, tok, err := a.CreateNode(ctx, *name)
		if err != nil {
			return err
		}
		fmt.Printf("id: %d\ntoken: %s\n", nid, tok)
		fmt.Fprintln(os.Stderr, "the token is shown once; the hub stores only its hash")
	case "list":
		nodes, err := st.ListNodes(ctx)
		if err != nil {
			return err
		}
		w := tabwriter.NewWriter(os.Stdout, 0, 8, 2, ' ', 0)
		fmt.Fprintln(w, "ID\tNAME\tPUBLIC\tLAST SEEN")
		for _, n := range nodes {
			seen := "never"
			if !n.LastSeenAt.IsZero() {
				seen = n.LastSeenAt.Format("2006-01-02 15:04:05Z")
			}
			fmt.Fprintf(w, "%d\t%s\t%v\t%s\n", n.ID, n.Name, n.Public, seen)
		}
		return w.Flush()
	case "delete":
		if *id == 0 {
			return errors.New("--id is required")
		}
		if err := a.DeleteNode(ctx, *id); err != nil {
			return err
		}
		fmt.Fprintln(os.Stderr, restartNotice)
	case "rotate-token":
		if *id == 0 {
			return errors.New("--id is required")
		}
		tok, err := a.RotateToken(ctx, *id)
		if err != nil {
			return err
		}
		fmt.Printf("token: %s\n", tok)
		fmt.Fprintln(os.Stderr, restartNotice)
	default:
		return fmt.Errorf("unknown node command %q", args[0])
	}
	return nil
}
