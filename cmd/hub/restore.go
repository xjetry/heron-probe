package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/xjetry/probe/internal/hub/store"
)

func runRestore(args []string) error { return runRestoreWith(args, os.Stdout) }

func runRestoreWith(args []string, out io.Writer) error {
	fs := flag.NewFlagSet("restore", flag.ContinueOnError)
	db := fs.String("db", "probe.db", "SQLite database path; stop hub before restoring")
	config := fs.String("config", "", "configuration snapshot (required)")
	metrics := fs.String("metrics", "", "metrics snapshot (omit to keep existing metrics)")
	yes := fs.Bool("yes", false, "confirm hub is stopped and replace database contents")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return errors.New("restore does not accept positional arguments")
	}
	if !*yes {
		return errors.New("restore requires --yes: stop hub before restoring; this command replaces database contents")
	}
	if *config == "" {
		return errors.New("restore requires --config <snapshot>")
	}
	result, err := store.Restore(context.Background(), *db, *config, *metrics, time.Now())
	if err != nil {
		return err
	}
	summary, err := json.Marshal(result)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(out, "restored: %s\n", summary)
	return err
}
