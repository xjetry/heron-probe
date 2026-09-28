package backup

import (
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/xjetry/probe/internal/hub/s3"
	"github.com/xjetry/probe/internal/hub/store"
)

// saveBackup 只改备份组，其余设置不动。
func saveBackup(t *testing.T, st *store.Store, u store.BackupSettingsUpdate) {
	t.Helper()
	if _, _, err := st.SaveSettings(t.Context(), store.SiteSettingsUpdate{}, &u); err != nil {
		t.Fatal(err)
	}
}

func enabledTarget() store.BackupSettingsUpdate {
	return store.BackupSettingsUpdate{Endpoint: "https://example.test", Bucket: "backups", Region: "auto", AccessKey: "key", Prefix: "tenant"}
}

// 份数 2：两份旧快照之外还有一个排在所有时间戳之后的手工 .db。手工文件若计入份数，会顶掉较新的那份旧快照。
func TestRetentionDoesNotCountManualDB(t *testing.T) {
	m, clk, objects, _ := setup(t)
	two := uint32(2)
	u := enabledTarget()
	u.ConfigKeep = &two
	saveBackup(t, m.st, u)
	manual := "tenant/config/zzzz-manual.db"
	older := "tenant/config/" + clk.Now().Add(-10*time.Minute).Format("20060102T150405.000000000Z") + ".db"
	newer := "tenant/config/" + clk.Now().Add(-5*time.Minute).Format("20060102T150405.000000000Z") + ".db"
	current := "tenant/config/" + clk.Now().Format("20060102T150405.000000000Z") + ".db"
	objects.objects[manual], objects.objects[older], objects.objects[newer] = nil, nil, nil
	tick(t, m)
	var backups []string
	for k := range objects.objects {
		if strings.HasPrefix(k, "tenant/config/") && k != manual {
			backups = append(backups, k)
		}
	}
	_, keptManual := objects.objects[manual]
	_, keptNewer := objects.objects[newer]
	_, keptCurrent := objects.objects[current]
	if len(backups) != 2 || !keptCurrent || !keptNewer || !keptManual {
		t.Errorf("manual .db counted toward keep or deleted: backups=%v keptNewer=%v keptManual=%v calls=%v", backups, keptNewer, keptManual, objects.calls)
	}
}

type sizedObjects struct {
	*fakeObjects
	size   int64
	budget time.Duration
}

func (o *sizedObjects) PutObject(ctx context.Context, key string, body io.ReadSeeker) error {
	deadline, _ := ctx.Deadline()
	o.budget = time.Until(deadline)
	size, err := body.Seek(0, io.SeekEnd)
	if err != nil {
		return err
	}
	if _, err := body.Seek(0, io.SeekStart); err != nil {
		return err
	}
	o.size = size
	return o.fakeObjects.PutObject(ctx, key, body)
}

// 配置层期限按对象体量给：5 分钟起，每开始一个 MiB 加 1 秒。在 PutObject 处读 ctx 截止时间，
// 退化成指标层那样按周期给（约一天）或不计体量都会红。
func TestConfigUploadBudgetFollowsObjectSize(t *testing.T) {
	m, _, objects, _ := setup(t)
	// 把配置层快照撑到数 MiB，按体量加的那部分期限才看得出来。
	execFixtureSQL(t, objects.databasePath, `INSERT INTO node (name, note, token_hash, created_at) VALUES ('padding', hex(randomblob(2097152)), randomblob(32), 0)`)
	sized := &sizedObjects{fakeObjects: objects}
	m.newClient = func(s3.Config) (objectStore, error) { return sized, nil }
	cfg, err := m.st.BackupSettings(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	category, _, _ := m.perform(t.Context(), cfg, "config", 1)
	mib := (sized.size + 1<<20 - 1) / (1 << 20)
	want := 5*time.Minute + time.Duration(mib)*time.Second
	if category != "" || mib < 3 || sized.budget > want || sized.budget < want-time.Second {
		t.Errorf("config upload budget=%s want=%s size=%d category=%s", sized.budget, want, sized.size, category)
	}
}

// 重启后首轮之前，状态直接读库里的未恢复标记，不等配置层读回。
func TestStatusShowsPersistedMarkerBeforeFirstTick(t *testing.T) {
	m, clk, objects, sink := setup(t)
	start := clk.Now()
	if _, err := m.st.RecordBackupEvent(t.Context(), store.TransitionFiring, "config 层备份失败（upload）", start); err != nil {
		t.Fatal(err)
	}
	clk.Advance(time.Hour)
	fresh := restart(m, objects, sink)
	if got := status(t, fresh).Config; got.Failure != "unrecovered" || !got.Since.Equal(start) {
		t.Errorf("status before first tick ignored persisted marker: %+v", got)
	}
}

type heldObjects struct {
	*fakeObjects
	entered, release chan struct{}
	dir              string
}

func (h *heldObjects) PutObject(ctx context.Context, key string, r io.ReadSeeker) error {
	if strings.Contains(key, "/config/") {
		if f, ok := r.(*os.File); ok {
			h.dir = filepath.Dir(f.Name())
		}
		close(h.entered)
		<-h.release
	}
	return h.fakeObjects.PutObject(ctx, key, r)
}

// 同一目录下的另一个库 hub.db-2 在途时，hub.db 的启动清理不能删它的暂存目录。
// hub.db-2 的前缀以 hub.db 的前缀开头，只按前缀匹配会误删；hub.db 自己的残留仍要删掉。
func TestStartupCleanupSparesOtherDatabases(t *testing.T) {
	m, clk, objects, _ := setup(t)
	dir, prefix := m.st.BackupScratch()
	own, err := os.MkdirTemp(dir, prefix+"*")
	if err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(filepath.Join(dir, "hub.db-2"), clk, slog.Default(), store.MigrateSchema)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	secret := "secret"
	u := enabledTarget()
	u.Secret = &secret
	saveBackup(t, st, u)
	held := &heldObjects{fakeObjects: &fakeObjects{objects: map[string][]byte{}}, entered: make(chan struct{}), release: make(chan struct{})}
	other := New(st, &eventSink{}, clk, slog.Default())
	other.newClient = func(s3.Config) (objectStore, error) { return held, nil }
	done := make(chan error, 1)
	go func() { done <- other.tickLayer(t.Context(), 0) }()
	<-held.entered
	tick(t, m)
	_, inFlightErr := os.Stat(held.dir)
	close(held.release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(own); !os.IsNotExist(err) {
		t.Errorf("own stale scratch remains: %v", err)
	}
	if got := status(t, other).Config; inFlightErr != nil || got.Failure != "" || got.LastSuccess.IsZero() {
		t.Errorf("startup cleanup removed another database's in-flight scratch %s: stat=%v status=%+v", held.dir, inFlightErr, got)
	}
	if len(objects.paths) != 2 {
		t.Errorf("own backups did not run: %v", objects.paths)
	}
}
