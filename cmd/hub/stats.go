package main

import (
	"context"
	"flag"
	"fmt"
	"sort"
)

func runStats(args []string) error {
	fs := flag.NewFlagSet("stats", flag.ContinueOnError)
	db := fs.String("db", "probe.db", "SQLite database path")
	if err := fs.Parse(args); err != nil {
		return err
	}
	st, _, err := openOffline(*db, false)
	if err != nil {
		return err
	}
	defer st.Close()
	counts, err := st.Counts(context.Background())
	if err != nil {
		return err
	}
	tables := make([]string, 0, len(counts))
	for t := range counts {
		tables = append(tables, t)
	}
	sort.Strings(tables)
	for _, t := range tables {
		fmt.Printf("%s: %d\n", t, counts[t])
	}
	return nil
}
