package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"strconv"

	"github.com/xjetry/heron-probe/internal/hub/store"
)

func runStats(args []string) error { return runStatsWith(args, os.Stdout) }

// runStatsWith 打印库的逻辑大小、每张表的行数与存储健康读数；数据来自 store.StorageStats，与
// AdminService.GetStorageStats 同源。健康读数在表行数之后按 Series 的固定顺序给出，键都带点号
// （表名.oldest、表名.watermark、prune.finished_at、rollup.finished_at），不会与"表名: 行数"混淆；缺失的值写 none。
// 离线命令不知道运行中 hub 的保留期配置，所以只给原值，不给标红结论（标红见 GetStorageStats）。
func runStatsWith(args []string, out io.Writer) error {
	fs := flag.NewFlagSet("stats", flag.ContinueOnError)
	db := fs.String("db", "heron.db", "SQLite database path")
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
	for _, h := range stats.Series {
		fmt.Fprintf(out, "%s.oldest: %s\n", h.Table, orNone(h.Oldest))
		if h.Watermark != nil {
			fmt.Fprintf(out, "%s.watermark: %d\n", h.Table, *h.Watermark)
		}
	}
	fmt.Fprintf(out, "%s.finished_at: %s\n", store.MaintenancePrune, orNone(stats.LastPrune))
	fmt.Fprintf(out, "%s.finished_at: %s\n", store.MaintenanceRollup, orNone(stats.LastRollup))
	return nil
}

func orNone(v *int64) string {
	if v == nil {
		return "none"
	}
	return strconv.FormatInt(*v, 10)
}
