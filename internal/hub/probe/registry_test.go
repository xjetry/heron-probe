package probe

import (
	"errors"
	"io"
	"log/slog"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	probev1 "github.com/xjetry/probe/gen/probe/v1"
	"github.com/xjetry/probe/internal/clock"
	"github.com/xjetry/probe/internal/hub/store"
	"github.com/xjetry/probe/internal/probelimit"
	"google.golang.org/protobuf/proto"
)

func registryStore(t *testing.T) (*Registry, *store.Store, []int64) {
	t.Helper()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	st, err := store.Open(filepath.Join(t.TempDir(), "hub.db"), clock.NewFake(time.Unix(0, 0)), log)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := st.Close(); err != nil {
			t.Error(err)
		}
	})
	var ids []int64
	for _, name := range []string{"one", "two"} {
		id, err := st.CreateNode(t.Context(), name, []byte(name))
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, id)
	}
	r := New(st, log)
	if err := r.Load(t.Context()); err != nil {
		t.Fatal(err)
	}
	return r, st, ids
}

func task(target string) *probev1.ProbeTask {
	return &probev1.ProbeTask{Kind: probev1.ProbeKind_PROBE_KIND_ICMP, Target: target, IntervalS: 5, TimeoutMs: 1000}
}

func save(t *testing.T, r *Registry, target string, ids []int64) Detail {
	t.Helper()
	d, _, err := r.Save(t.Context(), task(target), ids)
	if err != nil {
		t.Fatal(err)
	}
	return d
}

func assertDetails(t *testing.T, got, want []Detail) {
	t.Helper()
	if len(got) != len(want) {
		t.Errorf("details count=%d want=%d: %+v", len(got), len(want), got)
		return
	}
	for i := range want {
		if !proto.Equal(got[i].Task, want[i].Task) || !slices.Equal(got[i].NodeIDs, want[i].NodeIDs) {
			t.Errorf("detail[%d]=%v nodes=%v want=%v nodes=%v", i, got[i].Task, got[i].NodeIDs, want[i].Task, want[i].NodeIDs)
		}
	}
}

func assertState(t *testing.T, r *Registry, version uint64, want []Detail, nodes ...int64) {
	t.Helper()
	if v := r.Version(); v != version {
		t.Errorf("Version=%d want=%d", v, version)
	}
	v, got := r.List()
	if v != version {
		t.Errorf("List version=%d want=%d", v, version)
	}
	assertDetails(t, got, want)
	for _, node := range nodes {
		wt := &probev1.ProbeTasks{Version: version}
		for _, d := range want {
			assigned := slices.Contains(d.NodeIDs, node)
			if r.Assigned(node, d.Task.Id) != assigned {
				t.Errorf("Assigned(%d,%d) want=%v", node, d.Task.Id, assigned)
			}
			if assigned {
				wt.Tasks = append(wt.Tasks, d.Task)
			}
		}
		if tasks := r.TasksFor(node); !proto.Equal(tasks, wt) {
			t.Errorf("TasksFor(%d)=%v want=%v", node, tasks, wt)
		}
	}
}

func TestRegistrySaveDeleteVersionAndAssignments(t *testing.T) {
	r, _, ids := registryStore(t)
	a, v, err := r.Save(t.Context(), task("a.example"), []int64{ids[1], ids[0], ids[0]})
	if err != nil {
		t.Fatal(err)
	}
	if v != 1 || !slices.Equal(a.NodeIDs, ids) || a.Task.Id == 0 {
		t.Errorf("saved=%+v version=%d", a, v)
	}
	assertState(t, r, 1, []Detail{a}, ids...)
	b, v, err := r.Save(t.Context(), task("b.example"), nil)
	if err != nil {
		t.Fatal(err)
	}
	if v != 2 {
		t.Errorf("save B version=%d want=2", v)
	}
	assertState(t, r, 2, []Detail{a, b}, ids...)
	v, err = r.Delete(t.Context(), a.Task.Id)
	if err != nil || v != 3 {
		t.Errorf("delete version=%d err=%v", v, err)
	}
	if r.Assigned(ids[0], a.Task.Id) || r.Assigned(ids[1], a.Task.Id) {
		t.Error("deleted task remains assigned")
	}
	assertState(t, r, 3, []Detail{b}, ids...)
}

func TestRegistryRejectsInvalidTaskWithoutTouchingStore(t *testing.T) {
	r, st, ids := registryStore(t)
	bad := task("example.com")
	bad.IntervalS = 1
	_, _, err := r.Save(t.Context(), bad, ids)
	if !errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), "interval_s must be between") {
		t.Errorf("invalid error=%v", err)
	}
	assertState(t, r, 0, nil, ids...)
	v, recs, err := st.LoadProbeTasks(t.Context())
	if err != nil || v != 0 || len(recs) != 0 {
		t.Errorf("invalid task reached store: version=%d recs=%v err=%v", v, recs, err)
	}
}

func TestRegistryPassesThroughNotFoundAndLimit(t *testing.T) {
	r, _, ids := registryStore(t)
	_, _, err := r.Save(t.Context(), task("example.com"), []int64{42})
	if !errors.Is(err, store.ErrNotFound) || !strings.Contains(err.Error(), "node 42 does not exist") {
		t.Errorf("missing node error=%v", err)
	}
	assertState(t, r, 0, nil, ids...)
	var want []Detail
	for range probelimit.MaxTasksPerNode {
		want = append(want, save(t, r, "example.com", ids[:1]))
	}
	_, _, err = r.Save(t.Context(), task("example.com"), ids[:1])
	if !errors.Is(err, store.ErrNodeLimit) || !strings.Contains(err.Error(), "node 1 already has 64 probe tasks (maximum 64)") {
		t.Errorf("limit error=%v", err)
	}
	assertState(t, r, 64, want, ids...)
	if _, err := r.Delete(t.Context(), 999); !errors.Is(err, store.ErrNotFound) || !strings.Contains(err.Error(), "probe task 999 does not exist") {
		t.Errorf("missing task delete error=%v", err)
	}
	assertState(t, r, 64, want, ids...)
}

func TestRegistryReloadMatchesMemory(t *testing.T) {
	r, st, ids := registryStore(t)
	a := save(t, r, "a.example", ids)
	b := save(t, r, "b.example", nil)
	c := save(t, r, "c.example", ids)
	d := save(t, r, "d.example", nil)
	edit := proto.Clone(a.Task).(*probev1.ProbeTask)
	edit.Target = "changed.example"
	a, _, err := r.Save(t.Context(), edit, ids[1:])
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.Delete(t.Context(), b.Task.Id); err != nil {
		t.Fatal(err)
	}
	assertState(t, r, 6, []Detail{a, c, d}, ids...)
	next := New(st, r.log)
	if err := next.Load(t.Context()); err != nil {
		t.Fatal(err)
	}
	assertState(t, next, 6, []Detail{a, c, d}, ids...)
}

func TestRegistryLoadWaitsForPublication(t *testing.T) {
	r, st, ids := registryStore(t)
	// writeMu 覆盖从存储访问到发布的整个区间；重载不能在持锁写者结束前读取旧版本。
	r.writeMu.Lock()
	loaded := make(chan error, 1)
	go func() { loaded <- r.Load(t.Context()) }()
	var loadErr error
	early := false
	select {
	case loadErr = <-loaded:
		early = true
	case <-time.After(100 * time.Millisecond):
	}
	saved, version, err := st.SaveProbeTask(t.Context(), task("new.example"), ids)
	r.writeMu.Unlock()
	if !early {
		loadErr = <-loaded
	}
	if err != nil || loadErr != nil {
		t.Fatalf("save error=%v load error=%v", err, loadErr)
	}
	if early {
		t.Error("Load returned before the writer released writeMu")
	}
	assertState(t, r, version, []Detail{{Task: saved, NodeIDs: ids}}, ids...)
}

func TestRegistrySnapshotsDoNotAliasCache(t *testing.T) {
	for _, source := range []string{"save", "list", "tasks"} {
		t.Run(source, func(t *testing.T) {
			r, _, ids := registryStore(t)
			d := save(t, r, "a.example", ids)
			want := Detail{Task: proto.Clone(d.Task).(*probev1.ProbeTask), NodeIDs: slices.Clone(ids)}
			switch source {
			case "save":
				d.Task.Target = "corrupted"
				d.NodeIDs[0] = 999
			case "list":
				_, list := r.List()
				list[0].Task.Target = "corrupted"
				list[0].NodeIDs[0] = 999
			case "tasks":
				r.TasksFor(ids[0]).Tasks[0].Target = "corrupted"
			}
			assertState(t, r, 1, []Detail{want}, ids...)
		})
	}
}

func TestRegistryForgetPreservesOtherAssignmentsAndVersion(t *testing.T) {
	r, st, ids := registryStore(t)
	a := save(t, r, "a.example", ids)
	if err := st.DeleteNode(t.Context(), ids[0]); err != nil {
		t.Fatal(err)
	}
	r.Forget(ids[0])
	a.NodeIDs = ids[1:]
	assertState(t, r, 1, []Detail{a}, ids...)
	next := New(st, r.log)
	if err := next.Load(t.Context()); err != nil {
		t.Fatal(err)
	}
	assertState(t, next, 1, []Detail{a}, ids...)
}
