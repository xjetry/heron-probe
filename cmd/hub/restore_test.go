package main

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"fmt"
	"log/slog"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/xjetry/probe/internal/clock"
	"github.com/xjetry/probe/internal/hub/store"
)

func restoreDB(t *testing.T, path string) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

func restoreExec(t *testing.T, db *sql.DB, query string) {
	t.Helper()
	if _, err := db.Exec(query); err != nil {
		t.Fatal(err)
	}
}

func restoreWant(t *testing.T, db *sql.DB, query, want string) {
	t.Helper()
	var got sql.NullString
	if err := db.QueryRow(query).Scan(&got); err != nil {
		t.Errorf("%s: %v", query, err)
		return
	}
	if !got.Valid || got.String != want {
		t.Errorf("%s = %q (valid=%t), want %q", query, got.String, got.Valid, want)
	}
}

func restoreDump(t *testing.T, path string) string {
	t.Helper()
	out, err := exec.Command("sqlite3", path, ".dump").CombinedOutput()
	if err != nil {
		t.Fatalf("dump: %v %s", err, out)
	}
	return string(out)
}

func restoreSnapshots(t *testing.T) (config, metrics string) {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "source.db")
	clk := clock.NewFake(time.Unix(1000, 0))
	s, err := store.Open(path, clk, slog.Default(), store.MigrateSchema)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	db := restoreDB(t, path)
	restoreExec(t, db, `INSERT INTO node (id,name,token_hash,created_at) VALUES (1,'one',x'01',0),(2,'two',x'02',0);
		INSERT INTO alert_rule (id,name,kind,created_at) VALUES (1,'rule','offline',0);
		INSERT INTO alert_event (rule_id,node_id,transition,at,summary,value) VALUES (0,0,'firing',0,'system',0),(1,99,'firing',0,'deleted',0);
		INSERT INTO setting VALUES ('site.title','snapshot');
		INSERT INTO sqlite_sequence VALUES ('retired_table',17);
		INSERT INTO probe_task (id,kind,target,interval_s,timeout_ms,created_at) VALUES (7,1,'localhost',60,1000,0);
		DELETE FROM probe_task;`)
	for _, id := range []int{2, 3} {
		restoreExec(t, db, fmt.Sprintf(`INSERT INTO node_facts VALUES (%[1]d,0,'','','','','','',0,'',0,0);
			INSERT INTO traffic VALUES (%[1]d,'',0,0,0,0,0,0,0,0);
			INSERT INTO probe_task_node VALUES (1,%[1]d);
			INSERT INTO alert_rule_node VALUES (1,%[1]d);
			INSERT INTO alert_state (rule_id,node_id,state,since_at) VALUES (1,%[1]d,'firing',0);`, id))
	}
	config, metrics = filepath.Join(dir, "config.db"), filepath.Join(dir, "metrics.db")
	if err := s.SnapshotConfig(t.Context(), config); err != nil {
		t.Fatal(err)
	}
	restoreExec(t, db, `DELETE FROM node WHERE id=2;
		INSERT INTO node (name,token_hash,created_at) VALUES ('three',x'03',0);
		DELETE FROM sqlite_sequence WHERE name='retired_table';
		INSERT INTO sqlite_sequence VALUES ('metrics_only',23);
		UPDATE rollup_state SET upto_ts=600;
		INSERT INTO maintenance_state VALUES ('prune',2000);`)
	for _, table := range []string{"metric_1m", "metric_5m", "metric_1h"} {
		restoreExec(t, db, "INSERT INTO "+table+" (node_id,ts) VALUES (2,600),(3,600)")
	}
	for _, table := range []string{"probe_1m", "probe_5m", "probe_1h"} {
		restoreExec(t, db, "INSERT INTO "+table+" (node_id,ts,task_id) VALUES (2,600,7),(3,600,7)")
	}
	clk.Advance(1000 * time.Second)
	if err := s.SnapshotMetrics(t.Context(), metrics); err != nil {
		t.Fatal(err)
	}
	return config, metrics
}

func restoreTarget(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "target.db")
	s, err := store.Open(path, clock.Real(), slog.Default(), store.MigrateSchema)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	db := restoreDB(t, path)
	restoreExec(t, db, `INSERT INTO node (id,name,token_hash,created_at) VALUES (5,'old',x'05',0);
		DELETE FROM node;
		INSERT INTO node (id,name,token_hash,created_at) VALUES (1,'target',x'01',0);
		INSERT INTO metric_1m (node_id,ts) VALUES (2,300),(5,300);
		INSERT INTO setting VALUES ('site.title','target');
		INSERT INTO sqlite_sequence VALUES ('target_only',19);`)
	return path
}

func TestRestoreCommandTimeline(t *testing.T) {
	for _, configAt := range []int64{1000, 3000} {
		t.Run(fmt.Sprint(configAt), func(t *testing.T) { testRestoreTimeline(t, configAt) })
	}
}

func testRestoreTimeline(t *testing.T, configAt int64) {
	t.Helper()
	config, metrics := restoreSnapshots(t)
	restoreExec(t, restoreDB(t, config), fmt.Sprintf("UPDATE snapshot_meta SET taken_at=%d", configAt))
	path := filepath.Join(t.TempDir(), "restored.db")
	start := time.Now().Unix()
	out, err := hubCommand(t, "restore", "--db", path, "--config", config, "--metrics", metrics, "--yes").CombinedOutput()
	if err != nil {
		t.Fatalf("restore command: %v %s", err, out)
	}
	var summary store.RestoreResult
	if err := json.Unmarshal(bytes.TrimPrefix(bytes.TrimSpace(out), []byte("restored: ")), &summary); err != nil {
		t.Fatalf("restore summary: %s: %v", out, err)
	}
	if summary.ConfigTakenAt != configAt || summary.MetricsTakenAt == nil || *summary.MetricsTakenAt != 2000 || summary.RestoredAt < start || summary.RestoredAt > time.Now().Unix() {
		t.Errorf("restore summary timestamps: %+v", summary)
	}
	db := restoreDB(t, path)
	restoreWant(t, db, "SELECT seq FROM sqlite_sequence WHERE name='metrics_only'", "23")
	for query, want := range map[string]string{
		"SELECT group_concat(id) FROM node":                                     "1,2",
		"SELECT name FROM alert_rule WHERE id=1":                                "rule",
		"SELECT value FROM setting WHERE key='site.title'":                      "snapshot",
		"SELECT seq FROM sqlite_sequence WHERE name='node'":                     "3",
		"SELECT seq FROM sqlite_sequence WHERE name='probe_task'":               "7",
		"SELECT seq FROM sqlite_sequence WHERE name='retired_table'":            "17",
		"SELECT count(*) FROM restore_record":                                   "1",
		"SELECT config_taken_at || ',' || metrics_taken_at FROM restore_record": fmt.Sprintf("%d,2000", configAt),
		"SELECT count(*) FROM rollup_state WHERE upto_ts=600":                   "4",
		"SELECT finished_at FROM maintenance_state WHERE name='prune'":          "2000",
		"SELECT count(*) FROM alert_event":                                      "2",
	} {
		restoreWant(t, db, query, want)
	}
	wantOrphans := make(map[string]int64)
	for _, table := range []string{"node_facts", "traffic", "probe_task_node", "alert_rule_node", "alert_state", "metric_1m", "metric_5m", "metric_1h", "probe_1m", "probe_5m", "probe_1h"} {
		restoreWant(t, db, "SELECT group_concat(node_id) FROM "+table, "2")
		wantOrphans[table] = 1
	}
	if !reflect.DeepEqual(summary.Orphans, wantOrphans) {
		t.Errorf("summary orphan counts = %v, want %v", summary.Orphans, wantOrphans)
	}
	var record store.RestoreResult
	var orphanJSON string
	if err := db.QueryRow("SELECT restored_at,config_taken_at,metrics_taken_at,orphans FROM restore_record").Scan(&record.RestoredAt, &record.ConfigTakenAt, &record.MetricsTakenAt, &orphanJSON); err != nil {
		t.Error(err)
	} else {
		if err := json.Unmarshal([]byte(orphanJSON), &record.Orphans); err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(record, summary) {
			t.Errorf("stored restore record = %+v, want summary %+v", record, summary)
		}
	}
	restoreExec(t, db, "INSERT INTO node (name,token_hash,created_at) VALUES ('next',x'04',0)")
	restoreWant(t, db, "SELECT id FROM node WHERE name='next'", "4")
}

func TestRestoreExistingHighWaterAndOptionalMetrics(t *testing.T) {
	for _, withMetrics := range []bool{true, false} {
		t.Run(fmt.Sprint(withMetrics), func(t *testing.T) {
			config, metrics := restoreSnapshots(t)
			restoreExec(t, restoreDB(t, config), "UPDATE sqlite_sequence SET seq=3 WHERE name='node'")
			path := restoreTarget(t)
			args := []string{"--db", path, "--config", config, "--yes"}
			wantTS, wantTaken := "300", "none"
			if withMetrics {
				args = append(args, "--metrics", metrics)
				wantTS, wantTaken = "600", "2000"
			}
			if err := runRestoreWith(args, &bytes.Buffer{}); err != nil {
				t.Fatal(err)
			}
			db := restoreDB(t, path)
			restoreWant(t, db, "SELECT seq FROM sqlite_sequence WHERE name='node'", "5")
			restoreWant(t, db, "SELECT seq FROM sqlite_sequence WHERE name='target_only'", "19")
			restoreWant(t, db, "SELECT seq FROM sqlite_sequence WHERE name='probe_task'", "7")
			restoreWant(t, db, "SELECT group_concat(node_id || ':' || ts) FROM metric_1m", "2:"+wantTS)
			restoreWant(t, db, "SELECT coalesce(metrics_taken_at,'none') FROM restore_record", wantTaken)
			restoreExec(t, db, "INSERT INTO node (name,token_hash,created_at) VALUES ('next',x'06',0)")
			restoreWant(t, db, "SELECT id FROM node WHERE name='next'", "6")
			if err := runRestoreWith(args, &bytes.Buffer{}); err != nil {
				t.Fatal(err)
			}
			restoreWant(t, db, "SELECT count(*) FROM restore_record", "2")
		})
	}
}

func TestRestoreTargetSchemaPolicy(t *testing.T) {
	for _, version := range []int{13, 15, -1, 0} {
		t.Run(fmt.Sprint(version), func(t *testing.T) {
			config, metrics := restoreSnapshots(t)
			path := restoreTarget(t)
			db := restoreDB(t, path)
			restoreExec(t, db, fmt.Sprintf("PRAGMA user_version=%d", version))
			before := restoreDump(t, path)
			err := runRestoreWith([]string{"--db", path, "--config", config, "--metrics", metrics, "--yes"}, &bytes.Buffer{})
			if err == nil || !strings.Contains(err.Error(), "schema version") {
				t.Errorf("target version %d must be rejected without migration: %v", version, err)
			}
			if after := restoreDump(t, path); after != before {
				t.Error("rejected target schema changed database")
			}
			restoreWant(t, db, "PRAGMA user_version", fmt.Sprint(version))
		})
	}
}

func TestRestoreRejectsSnapshotsWithoutChangingTarget(t *testing.T) {
	for _, layer := range []string{"config", "metrics"} {
		for _, defect := range []string{"schema", "page", "table", "layer", "metadata"} {
			t.Run(layer+"/"+defect, func(t *testing.T) {
				config, metrics := restoreSnapshots(t)
				path := restoreTarget(t)
				source := config
				if layer == "metrics" {
					source = metrics
				}
				db := restoreDB(t, source)
				query, wantErr := "", ""
				switch defect {
				case "schema":
					query, wantErr = "UPDATE snapshot_meta SET schema_version=-1", "schema_version=-1"
				case "page":
					// VACUUM 会移除没有 AUTOINCREMENT 表的内部序列表；保留簿记，避免先撞缺表守卫。
					query, wantErr = `CREATE TABLE saved_sequence AS SELECT * FROM sqlite_sequence;
						PRAGMA page_size=8192; VACUUM;
						CREATE TABLE sequence_seed (id INTEGER PRIMARY KEY AUTOINCREMENT);
						DROP TABLE sequence_seed;
						INSERT INTO sqlite_sequence SELECT * FROM saved_sequence;
						DROP TABLE saved_sequence`, "page_size=8192"
				case "table":
					if layer == "config" {
						query, wantErr = "DROP TABLE alert_rule", "missing table alert_rule"
					} else {
						query, wantErr = "DROP TABLE metric_1h", "missing table metric_1h"
					}
				case "layer":
					query, wantErr = "UPDATE snapshot_meta SET layer='wrong'", `layer="wrong"`
				case "metadata":
					query, wantErr = "INSERT INTO snapshot_meta SELECT * FROM snapshot_meta", "rows=2"
				}
				restoreExec(t, db, query)
				before := restoreDump(t, path)
				err := runRestoreWith([]string{"--db", path, "--config", config, "--metrics", metrics, "--yes"}, &bytes.Buffer{})
				if err == nil || !strings.Contains(err.Error(), wantErr) {
					t.Errorf("reject %s %s: err=%v, want %q", layer, defect, err, wantErr)
				}
				if after := restoreDump(t, path); after != before {
					t.Error("invalid snapshot changed target database")
				}
			})
		}
	}
}

func TestRestoreFailureRollsBackAllTables(t *testing.T) {
	config, metrics := restoreSnapshots(t)
	path := restoreTarget(t)
	db := restoreDB(t, path)
	restoreExec(t, db, `CREATE TRIGGER reject_restore_record BEFORE INSERT ON restore_record BEGIN SELECT RAISE(ABORT,'record write rejected'); END`)
	before := restoreDump(t, path)
	err := runRestoreWith([]string{"--db", path, "--config", config, "--metrics", metrics, "--yes"}, &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "record write rejected") {
		t.Errorf("expected late record failure, got %v", err)
	}
	if after := restoreDump(t, path); after != before {
		t.Error("late restore failure changed target database: config, metrics, sequences and record must roll back together")
	}
}

func TestRestoreRequiresExplicitConfirmation(t *testing.T) {
	config, metrics := restoreSnapshots(t)
	path := restoreTarget(t)
	before := restoreDump(t, path)
	out, err := hubCommand(t, "restore", "--db", path, "--config", config, "--metrics", metrics).CombinedOutput()
	if err == nil || !strings.Contains(string(out), "requires --yes") || !strings.Contains(string(out), "stop hub") {
		t.Errorf("unconfirmed restore: err=%v output=%s", err, out)
	}
	if after := restoreDump(t, path); after != before {
		t.Error("unconfirmed restore changed target database")
	}
}
