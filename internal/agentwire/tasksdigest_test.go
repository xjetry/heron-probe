package agentwire

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"testing"

	heronv1 "github.com/xjetry/heron-probe/gen/heron/v1"
	"google.golang.org/protobuf/proto"
)

func digestTask(id uint64, target string) *heronv1.ProbeTask {
	return &heronv1.ProbeTask{Id: id, Kind: heronv1.ProbeKind_PROBE_KIND_ICMP, Target: target, IntervalS: 5, TimeoutMs: 1000}
}

// 对照实现：严格按契约（升序、确定性编码、8 字节大端长度前缀、SHA-256）逐步手写。
func wantDigest(t *testing.T, tasks ...*heronv1.ProbeTask) []byte {
	t.Helper()
	h := sha256.New()
	var size [8]byte
	for _, task := range tasks {
		b, err := proto.MarshalOptions{Deterministic: true}.Marshal(task)
		if err != nil {
			t.Fatal(err)
		}
		binary.BigEndian.PutUint64(size[:], uint64(len(b)))
		h.Write(size[:])
		h.Write(b)
	}
	return h.Sum(nil)
}

func TestTasksDigestEmptyIsSHA256OfEmptyString(t *testing.T) {
	empty := sha256.Sum256(nil)
	if got := TasksDigest(nil); !bytes.Equal(got, empty[:]) {
		t.Fatalf("TasksDigest(nil) = %x, want SHA-256 of empty string %x", got, empty)
	}
	if got := TasksDigest([]*heronv1.ProbeTask{}); !bytes.Equal(got, empty[:]) {
		t.Fatalf("TasksDigest(empty) = %x, want %x", got, empty)
	}
}

func TestTasksDigestContract(t *testing.T) {
	tasks := []*heronv1.ProbeTask{digestTask(1, "a.example"), digestTask(3, "c.example"), digestTask(2, "b.example")}
	want := wantDigest(t, digestTask(1, "a.example"), digestTask(2, "b.example"), digestTask(3, "c.example"))
	if got := TasksDigest(tasks); !bytes.Equal(got, want) {
		t.Fatalf("TasksDigest = %x, want contract encoding %x", got, want)
	}
	// 收到顺序不影响摘要。
	reversed := []*heronv1.ProbeTask{digestTask(3, "c.example"), digestTask(2, "b.example"), digestTask(1, "a.example")}
	if got := TasksDigest(reversed); !bytes.Equal(got, want) {
		t.Fatalf("reordered digest = %x, want %x", got, want)
	}
	// 内容变化（含 config_id 与 pin）改变摘要。
	changed := digestTask(2, "b.example")
	changed.ConfigId = make([]byte, 16)
	if got := TasksDigest([]*heronv1.ProbeTask{digestTask(1, "a.example"), changed, digestTask(3, "c.example")}); bytes.Equal(got, want) {
		t.Fatal("content change did not change digest")
	}
}

// 摘要是内容摘要：清单版本计数不参与，全局版本变了而清单不变时摘要相等。
func TestTasksDigestExcludesVersion(t *testing.T) {
	tasks := []*heronv1.ProbeTask{digestTask(1, "a.example"), digestTask(2, "b.example")}
	v1 := &heronv1.ProbeTasks{Version: 100, Tasks: tasks}
	v2 := &heronv1.ProbeTasks{Version: 200, Tasks: tasks}
	if got, want := TasksDigest(v1.GetTasks()), TasksDigest(v2.GetTasks()); !bytes.Equal(got, want) {
		t.Fatalf("version-only change altered digest: %x vs %x", got, want)
	}
}

// 摘要必须覆盖会被 agent 拒收的任务：被拒任务仍在持有的清单里，两侧摘要才可比。
func TestTasksDigestCoversRejectedTasks(t *testing.T) {
	bad := digestTask(9, "not a valid target at all:::")
	withBad := TasksDigest([]*heronv1.ProbeTask{digestTask(1, "a.example"), bad})
	withoutBad := TasksDigest([]*heronv1.ProbeTask{digestTask(1, "a.example")})
	if bytes.Equal(withBad, withoutBad) {
		t.Fatal("rejected task not covered by digest")
	}
}
