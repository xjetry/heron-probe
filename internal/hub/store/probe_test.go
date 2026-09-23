package store

import (
	"database/sql"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"

	probev1 "github.com/xjetry/probe/gen/probe/v1"
	"github.com/xjetry/probe/internal/hub/metric"
	"github.com/xjetry/probe/internal/probelimit"
	"google.golang.org/protobuf/proto"
)

func probeRow(nodeID int64, ts int64, task uint64, rtts []uint32, lost, errs uint32) metric.ProbeRow {
	b := &metric.ProbeBucket{}
	for _, us := range rtts {
		b.Add(&probev1.ProbeResult{TaskId: task, Outcome: &probev1.ProbeResult_RttUs{RttUs: us}})
	}
	for range lost {
		b.Add(&probev1.ProbeResult{TaskId: task, Outcome: &probev1.ProbeResult_Timeout{Timeout: &probev1.Timeout{}}})
	}
	for range errs {
		b.Add(&probev1.ProbeResult{TaskId: task, Outcome: &probev1.ProbeResult_Error{Error: &probev1.ProbeError{Message: "x"}}})
	}
	return metric.ProbeRow{NodeID: nodeID, TS: ts, TaskID: task, Bucket: b}
}

func TestProbeRowsMergeAdditivelyAndKeepNullRtt(t *testing.T) {
	s, _ := open(t)
	ctx := t.Context()
	id, _ := s.CreateNode(ctx, "n", hash(1))
	// 先写一个只有丢包的桶：rtt_min/max 必须是 NULL 而不是 0。
	if _, err := s.WriteMinuteBatch(ctx, metric.Batch{Probes: []metric.ProbeRow{probeRow(id, 600, 7, nil, 2, 0)}}); err != nil {
		t.Fatal(err)
	}
	var mn sql.NullInt64
	if err := s.r.QueryRow("SELECT rtt_min_us FROM probe_1m WHERE node_id = ? AND ts = 600 AND task_id = 7", id).Scan(&mn); err != nil || mn.Valid {
		t.Fatalf("rtt_min_us should be NULL without samples; got valid=%v err=%v", mn.Valid, err)
	}
	// 再并入有 rtt 的半桶：min 不能被 NULL 或 0 污染。
	if _, err := s.WriteMinuteBatch(ctx, metric.Batch{Probes: []metric.ProbeRow{probeRow(id, 600, 7, []uint32{300, 100}, 0, 1)}}); err != nil {
		t.Fatal(err)
	}
	rows, err := s.QueryProbes(ctx, id, 600, 660, levels[0], 60)
	if err != nil || len(rows) != 1 {
		t.Fatalf("rows=%s err=%v", formatProbeRows(rows), err)
	}
	b := rows[0].Bucket
	if b.Sent != 5 || b.Lost != 2 || b.Errors != 1 || b.RttN != 2 || b.RttMinUs != 100 || b.RttMaxUs != 300 || b.RttSumUs != 400 {
		t.Fatalf("merged bucket %+v", *b)
	}

	for _, tc := range []struct {
		name string
		rtts []uint32
		lost uint32
		want metric.ProbeBucket
	}{
		{"inside", []uint32{200}, 0, metric.ProbeBucket{Sent: 6, Lost: 2, Errors: 1, RttN: 3, RttSumUs: 600, RttMinUs: 100, RttMaxUs: 300}},
		{"outside", []uint32{50, 400}, 0, metric.ProbeBucket{Sent: 8, Lost: 2, Errors: 1, RttN: 5, RttSumUs: 1050, RttMinUs: 50, RttMaxUs: 400}},
		{"loss_after_samples", nil, 1, metric.ProbeBucket{Sent: 9, Lost: 3, Errors: 1, RttN: 5, RttSumUs: 1050, RttMinUs: 50, RttMaxUs: 400}},
	} {
		if _, err := s.WriteMinuteBatch(ctx, metric.Batch{Probes: []metric.ProbeRow{probeRow(id, 600, 7, tc.rtts, tc.lost, 0)}}); err != nil {
			t.Fatal(err)
		}
		t.Run(tc.name, func(t *testing.T) {
			rows, err := s.QueryProbes(ctx, id, 600, 660, levels[0], 60)
			if err != nil || len(rows) != 1 || *rows[0].Bucket != tc.want {
				t.Fatalf("merged %s: rows=%s err=%v want=%+v", tc.name, formatProbeRows(rows), err, tc.want)
			}
		})
	}
	// 双方都无样本的冲突合并仍然必须保持 SQL NULL。
	for range 2 {
		if _, err := s.WriteMinuteBatch(ctx, metric.Batch{Probes: []metric.ProbeRow{probeRow(id, 600, 8, nil, 1, 0)}}); err != nil {
			t.Fatal(err)
		}
	}
	var mx sql.NullInt64
	if err := s.r.QueryRow("SELECT rtt_min_us, rtt_max_us FROM probe_1m WHERE node_id=? AND ts=600 AND task_id=8", id).Scan(&mn, &mx); err != nil || mn.Valid || mx.Valid {
		t.Fatalf("NULL/NULL merge min=%v max=%v err=%v", mn, mx, err)
	}
}

func TestProbeWriterRejectsRowsBeforeProbeWatermarkOnly(t *testing.T) {
	for _, frozen := range []string{"probe_5m", "5m"} {
		t.Run(frozen, func(t *testing.T) {
			s, _ := open(t)
			ctx := t.Context()
			id, _ := s.CreateNode(ctx, "n", hash(1))
			if err := s.setRollupWatermark(t.Context(), frozen, 1200); err != nil {
				t.Fatal(err)
			}
			n, err := s.WriteMinuteBatch(ctx, metric.Batch{Rows: minuteRows(id, 600, 660), Probes: []metric.ProbeRow{probeRow(id, 600, 7, []uint32{5}, 0, 0)}})
			if err != nil || n != 1 {
				t.Fatalf("rejected=%d err=%v, want 1", n, err)
			}
			counts, err := s.Counts(ctx)
			if err != nil {
				t.Fatal(err)
			}
			wantMetric, wantProbe := int64(1), int64(0)
			if frozen == "5m" {
				wantMetric, wantProbe = 0, 1
			}
			if counts["metric_1m"] != wantMetric || counts["probe_1m"] != wantProbe {
				t.Fatalf("independent freeze: metric=%d probe=%d, want %d/%d", counts["metric_1m"], counts["probe_1m"], wantMetric, wantProbe)
			}
		})
	}
}

func TestQueryProbesRebucketsPerTask(t *testing.T) {
	s, _ := open(t)
	id, _ := s.CreateNode(t.Context(), "n", hash(1))
	var input []metric.ProbeRow
	for ts := int64(600); ts < 1200; ts += 60 {
		for _, task := range []uint64{9, 3} {
			input = append(input, probeRow(id, ts, task, []uint32{100}, 1, 1))
		}
	}
	input = append(input, probeRow(id, 1200, 3, []uint32{200}, 0, 0))
	if _, err := s.WriteMinuteBatch(t.Context(), metric.Batch{Probes: input}); err != nil {
		t.Fatal(err)
	}
	rows, err := s.QueryProbes(t.Context(), id, 630, 1201, levels[0], 300)
	want := []metric.ProbeRow{
		{NodeID: id, TS: 600, TaskID: 3, Bucket: &metric.ProbeBucket{Sent: 15, Lost: 5, Errors: 5, RttN: 5, RttSumUs: 500, RttMinUs: 100, RttMaxUs: 100}},
		{NodeID: id, TS: 900, TaskID: 3, Bucket: &metric.ProbeBucket{Sent: 15, Lost: 5, Errors: 5, RttN: 5, RttSumUs: 500, RttMinUs: 100, RttMaxUs: 100}},
		{NodeID: id, TS: 1200, TaskID: 3, Bucket: &metric.ProbeBucket{Sent: 1, RttN: 1, RttSumUs: 200, RttMinUs: 200, RttMaxUs: 200}},
		{NodeID: id, TS: 600, TaskID: 9, Bucket: &metric.ProbeBucket{Sent: 15, Lost: 5, Errors: 5, RttN: 5, RttSumUs: 500, RttMinUs: 100, RttMaxUs: 100}},
		{NodeID: id, TS: 900, TaskID: 9, Bucket: &metric.ProbeBucket{Sent: 15, Lost: 5, Errors: 5, RttN: 5, RttSumUs: 500, RttMinUs: 100, RttMaxUs: 100}},
	}
	if err != nil || !reflect.DeepEqual(rows, want) {
		t.Fatalf("rebucketed rows=%s err=%v, want %s", formatProbeRows(rows), err, formatProbeRows(want))
	}
	for _, step := range []int64{0, 30, 90} {
		if _, err := s.QueryProbes(t.Context(), id, 600, 1200, levels[0], step); err == nil {
			t.Fatalf("invalid step %d accepted", step)
		}
	}
	for _, lv := range []Level{{Name: "bad", Bucket: 60}, {Name: "1m", Bucket: 0}} {
		if _, err := s.QueryProbes(t.Context(), id, 600, 1200, lv, 60); err == nil {
			t.Fatalf("invalid level %+v accepted", lv)
		}
	}
}

func taskForTest() *probev1.ProbeTask {
	return &probev1.ProbeTask{Kind: probev1.ProbeKind_PROBE_KIND_ICMP, Target: "127.0.0.1", IntervalS: 5, TimeoutMs: 100}
}

func TestSaveProbeTaskAssignsAndBumpsVersion(t *testing.T) {
	s, _ := open(t)
	ctx := t.Context()
	a, _ := s.CreateNode(ctx, "a", hash(1))
	b, _ := s.CreateNode(ctx, "b", hash(2))
	saved, version, err := s.SaveProbeTask(ctx, taskForTest(), []int64{b, a})
	if err != nil || saved == nil || saved.Id != 1 || version != 1 {
		t.Fatalf("save=%v version=%d err=%v", saved, version, err)
	}
	assertTasks(t, s, 1, []ProbeTaskRecord{{Task: saved, NodeIDs: []int64{a, b}}})
	saved.Target = "example.com"
	saved.IntervalS, saved.TimeoutMs = 10, 500
	saved, version, err = s.SaveProbeTask(ctx, saved, []int64{b})
	if err != nil || version != 2 {
		t.Fatalf("replace version=%d err=%v", version, err)
	}
	assertTasks(t, s, 2, []ProbeTaskRecord{{Task: saved, NodeIDs: []int64{b}}})
	absent := taskForTest()
	absent.Id = 99
	if _, _, err := s.SaveProbeTask(ctx, absent, nil); !errors.Is(err, ErrNotFound) || !strings.Contains(err.Error(), "probe task 99") {
		t.Fatalf("missing task error=%v", err)
	}
	if _, _, err := s.SaveProbeTask(ctx, taskForTest(), []int64{a, 42}); !errors.Is(err, ErrNotFound) || !strings.Contains(err.Error(), "node 42") {
		t.Fatalf("missing node error=%v", err)
	}
	assertTasks(t, s, 2, []ProbeTaskRecord{{Task: saved, NodeIDs: []int64{b}}})
	if version, err := s.DeleteProbeTask(ctx, 1); err != nil || version != 3 {
		t.Fatalf("delete version=%d err=%v", version, err)
	}
	assertTasks(t, s, 3, nil)
	if _, err := s.DeleteProbeTask(ctx, 1); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing delete error=%v", err)
	}
	assertTasks(t, s, 3, nil)
}

func assertTasks(t *testing.T, s *Store, wantVersion uint64, want []ProbeTaskRecord) {
	t.Helper()
	version, got, err := s.LoadProbeTasks(t.Context())
	if err != nil || version != wantVersion || len(got) != len(want) {
		t.Fatalf("tasks=%v version=%d err=%v, want %v/%d", got, version, err, want, wantVersion)
	}
	for i := range want {
		if !proto.Equal(got[i].Task, want[i].Task) || !reflect.DeepEqual(got[i].NodeIDs, want[i].NodeIDs) {
			t.Fatalf("task %d=%v/%v, want %v/%v", i, got[i].Task, got[i].NodeIDs, want[i].Task, want[i].NodeIDs)
		}
	}
	var assignments int
	if err := s.r.QueryRow("SELECT COUNT(*) FROM probe_task_node").Scan(&assignments); err != nil {
		t.Fatal(err)
	}
	wantAssignments := 0
	for _, rec := range want {
		wantAssignments += len(rec.NodeIDs)
	}
	if assignments != wantAssignments {
		t.Fatalf("assignment count=%d, want %d", assignments, wantAssignments)
	}
}

func TestSaveProbeTaskEnforcesPerNodeLimit(t *testing.T) {
	s, _ := open(t)
	ctx := t.Context()
	id, _ := s.CreateNode(ctx, "n", hash(1))
	for range probelimit.MaxTasksPerNode {
		if _, _, err := s.SaveProbeTask(ctx, taskForTest(), []int64{id}); err != nil {
			t.Fatal(err)
		}
	}
	if _, _, err := s.SaveProbeTask(ctx, taskForTest(), []int64{id}); !errors.Is(err, ErrNodeLimit) || !strings.Contains(err.Error(), fmt.Sprintf("node %d", id)) {
		t.Fatalf("65th task error=%v, want ErrNodeLimit with node", err)
	}
	version, tasks, err := s.LoadProbeTasks(ctx)
	if err != nil || version != 64 || len(tasks) != 64 {
		t.Fatalf("limit rollback: version=%d tasks=%d err=%v", version, len(tasks), err)
	}

	changed := proto.Clone(tasks[0].Task).(*probev1.ProbeTask)
	changed.Target = "example.com"
	saved, version, err := s.SaveProbeTask(ctx, changed, []int64{id})
	if err != nil || version != 65 || !proto.Equal(saved, changed) {
		t.Fatalf("editing full node: task=%v version=%d err=%v, want %v/65", saved, version, err, changed)
	}
	tasks[0].Task = changed
	assertTasks(t, s, 65, tasks)
}

func TestDuplicateProbeAssignmentRollsBackReplacement(t *testing.T) {
	s, _ := open(t)
	ctx := t.Context()
	id, _ := s.CreateNode(ctx, "n", hash(1))
	saved, _, err := s.SaveProbeTask(ctx, taskForTest(), []int64{id})
	if err != nil {
		t.Fatal(err)
	}
	changed := proto.Clone(saved).(*probev1.ProbeTask)
	changed.Target = "changed"
	if _, _, err := s.SaveProbeTask(ctx, changed, []int64{id, id}); err == nil || !strings.Contains(err.Error(), "UNIQUE constraint failed") {
		t.Fatalf("duplicate assignment error=%v", err)
	}
	assertTasks(t, s, 1, []ProbeTaskRecord{{Task: saved, NodeIDs: []int64{id}}})
}

func TestDeleteNodeRemovesProbeRowsAndAssignments(t *testing.T) {
	s, _ := open(t)
	ctx := t.Context()
	a, _ := s.CreateNode(ctx, "a", hash(1))
	b, _ := s.CreateNode(ctx, "b", hash(2))
	saved, _, err := s.SaveProbeTask(ctx, taskForTest(), []int64{a, b})
	if err != nil {
		t.Fatal(err)
	}
	seedProbeLevels(t, s, []int64{a, b})
	if err := s.DeleteNode(ctx, a); err != nil {
		t.Fatal(err)
	}
	assertTasks(t, s, 1, []ProbeTaskRecord{{Task: saved, NodeIDs: []int64{b}}})
	counts, err := s.Counts(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, table := range append(append([]string{}, probeTables...), "probe_task", "probe_task_node") {
		if counts[table] != 1 {
			t.Fatalf("%s count=%d, want 1", table, counts[table])
		}
	}
	for _, table := range probeTables {
		var node int64
		if err := s.r.QueryRow("SELECT node_id FROM " + table).Scan(&node); err != nil || node != b {
			t.Fatalf("%s remaining node=%d err=%v, want %d", table, node, err, b)
		}
	}
	n, err := s.WriteMinuteBatch(ctx, metric.Batch{Probes: []metric.ProbeRow{probeRow(a, 600, 1, []uint32{5}, 0, 0)}})
	if err != nil || n != 1 {
		t.Fatalf("deleted node probe accepted: rejected=%d err=%v", n, err)
	}
	var count int
	if err := s.r.QueryRow("SELECT COUNT(*) FROM probe_1m WHERE node_id = ?", a).Scan(&count); err != nil || count != 0 {
		t.Fatalf("deleted history revived: count=%d err=%v", count, err)
	}
}

func seedProbeLevels(t *testing.T, s *Store, ids []int64) {
	t.Helper()
	if err := s.write(t.Context(), func(tx *sql.Tx) error {
		for _, table := range probeTables {
			for _, id := range ids {
				if _, err := tx.Exec("INSERT INTO "+table+" (node_id,ts,task_id) VALUES (?,600,1)", id); err != nil {
					return err
				}
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestDeleteProbeTaskKeepsHistoryAndNeverReusesID(t *testing.T) {
	s, _ := open(t)
	ctx := t.Context()
	id, _ := s.CreateNode(ctx, "n", hash(1))
	saved, _, err := s.SaveProbeTask(ctx, taskForTest(), []int64{id})
	if err != nil {
		t.Fatal(err)
	}
	seedProbeLevels(t, s, []int64{id})
	if _, err := s.DeleteProbeTask(ctx, saved.Id); err != nil {
		t.Fatal(err)
	}
	next, _, err := s.SaveProbeTask(ctx, taskForTest(), nil)
	if err != nil || next.Id <= saved.Id {
		t.Fatalf("task id reused: next=%v old=%v err=%v", next, saved, err)
	}
	counts, err := s.Counts(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, table := range probeTables {
		if counts[table] != 1 {
			t.Fatalf("task deletion erased %s history", table)
		}
	}
}

func TestMinuteBatchRollsBackBothFamilies(t *testing.T) {
	s, _ := open(t)
	ctx := t.Context()
	id, _ := s.CreateNode(ctx, "n", hash(1))
	if err := s.write(ctx, func(tx *sql.Tx) error {
		_, err := tx.Exec("CREATE TRIGGER reject_probe BEFORE INSERT ON probe_1m BEGIN SELECT RAISE(ABORT, 'probe rejected'); END")
		return err
	}); err != nil {
		t.Fatal(err)
	}
	_, err := s.WriteMinuteBatch(ctx, metric.Batch{Rows: minuteRows(id, 600, 660), Probes: []metric.ProbeRow{probeRow(id, 600, 1, []uint32{1}, 0, 0)}})
	if err == nil || !strings.Contains(err.Error(), "probe rejected") {
		t.Fatalf("batch error=%v", err)
	}
	counts, err := s.Counts(ctx)
	if err != nil || counts["metric_1m"] != 0 || counts["probe_1m"] != 0 {
		t.Fatalf("partial batch committed: counts=%v err=%v", counts, err)
	}
}

func TestProbeBucketMergeMeanAndBatchEmpty(t *testing.T) {
	b := &metric.ProbeBucket{}
	if mean, ok := b.RttMean(); mean != 0 || ok {
		t.Fatalf("empty mean=%d/%v", mean, ok)
	}
	b.Merge(probeRow(1, 0, 1, nil, 2, 1).Bucket)
	b.Merge(probeRow(1, 0, 1, []uint32{300, 100}, 0, 0).Bucket)
	b.Merge(probeRow(1, 0, 1, nil, 1, 0).Bucket)
	b.Merge(probeRow(1, 0, 1, []uint32{50, 500}, 0, 0).Bucket)
	want := metric.ProbeBucket{Sent: 8, Lost: 3, Errors: 1, RttSumUs: 950, RttN: 4, RttMinUs: 50, RttMaxUs: 500}
	if *b != want {
		t.Fatalf("merged=%+v want=%+v", *b, want)
	}
	if mean, ok := b.RttMean(); mean != 237 || !ok {
		t.Fatalf("mean=%d/%v, want 237/true", mean, ok)
	}
	for _, tc := range []struct {
		batch metric.Batch
		empty bool
	}{
		{metric.Batch{}, true},
		{metric.Batch{Rows: []metric.Row{{}}}, false},
		{metric.Batch{Probes: []metric.ProbeRow{{}}}, false},
		{metric.Batch{Rows: []metric.Row{{}}, Probes: []metric.ProbeRow{{}}}, false},
	} {
		if got := tc.batch.Empty(); got != tc.empty {
			t.Fatalf("batch %+v empty=%v, want %v", tc.batch, got, tc.empty)
		}
	}
}

func formatProbeRows(rows []metric.ProbeRow) string {
	var out strings.Builder
	out.WriteByte('[')
	for i, row := range rows {
		if i > 0 {
			out.WriteString(", ")
		}
		fmt.Fprintf(&out, "{NodeID:%d TS:%d TaskID:%d Bucket:", row.NodeID, row.TS, row.TaskID)
		if row.Bucket == nil {
			out.WriteString("<nil>")
		} else {
			fmt.Fprintf(&out, "%+v", *row.Bucket)
		}
		out.WriteByte('}')
	}
	out.WriteByte(']')
	return out.String()
}

func TestProbeBucketIgnoresMissingOutcome(t *testing.T) {
	for _, input := range []*probev1.ProbeResult{nil, {TaskId: 7}} {
		b := probeRow(1, 0, 7, []uint32{100}, 1, 1).Bucket
		want := *b
		b.Add(input)
		if *b != want || b.RttN != b.Sent-b.Lost-b.Errors {
			t.Fatalf("missing outcome changed bucket: got=%+v want=%+v", *b, want)
		}
	}
}
