package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
)

func runStats(args []string) error { return runStatsWith(args, os.Stdout) }

// runStatsWith 打印库的逻辑大小与每张表的行数；数据来自 store.StorageStats，与 AdminService.GetStorageStats 同源。
func runStatsWith(args []string, out io.Writer) error {
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
	stats, err := st.StorageStats(context.Background())
	if err != nil {
		return err
	}
	fmt.Fprintf(out, "db_bytes: %d\n", stats.DBBytes)
	for _, t := range stats.Tables {
		fmt.Fprintf(out, "%s: %d\n", t.Name, t.Rows)
	}
	return nil
}
