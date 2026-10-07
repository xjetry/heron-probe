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
// wal.* 原样展示 store 的单次文件观测；present（含零字节）、absent、unknown 分开，不另读文件系统。
// 本命令自己打开库：hub 未运行时观测到的是这次打开建立的 -wal（正常关闭过的库上为 0 字节）。
func runStatsWith(args []string, out io.Writer) error {
	return runStatsWithSource(args, out, func(path string) (storageStatsSource, error) {
		st, _, err := openOffline(path, false)
		return st, err
	})
}

type storageStatsSource interface {
	StorageStats(context.Context) (store.StorageStats, error)
	Close() error
}

func runStatsWithSource(args []string, out io.Writer, open func(string) (storageStatsSource, error)) error {
	fs := flag.NewFlagSet("stats", flag.ContinueOnError)
	db := fs.String("db", "heron.db", "SQLite database path")
	if err := fs.Parse(args); err != nil {
		return err
	}
	st, err := open(*db)
	if err != nil {
		return err
	}
	defer st.Close()
	stats, err := st.StorageStats(context.Background())
	if err != nil {
		return err
	}
	fmt.Fprintf(out, "db_bytes: %d\n", stats.DBBytes)
	fmt.Fprintf(out, "sql_observed_at: %d\n", stats.SQLObservedAt)
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
	fmt.Fprintf(out, "wal.observed_at: %d\n", stats.WAL.ObservedAt)
	switch {
	case stats.WAL.Bytes != nil:
		fmt.Fprintln(out, "wal.state: present")
		fmt.Fprintf(out, "wal.bytes: %d\n", *stats.WAL.Bytes)
	case stats.WAL.Absent:
		fmt.Fprintln(out, "wal.state: absent (no WAL file)")
	default:
		fmt.Fprintln(out, "wal.state: unknown")
		fmt.Fprintf(out, "wal.error: %q\n", stats.WAL.Error)
	}
	return nil
}

func orNone(v *int64) string {
	if v == nil {
		return "none"
	}
	return strconv.FormatInt(*v, 10)
}
