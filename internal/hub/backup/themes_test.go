package backup

import (
	"context"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/xjetry/probe/internal/hub/s3"
	"github.com/xjetry/probe/internal/hub/store"
)

type themeObjects struct {
	mu       sync.Mutex
	base     *fakeObjects
	onUpload func()
	changed  chan string
	budgets  map[string]time.Duration
}

func (o *themeObjects) PutObject(ctx context.Context, key string, r io.ReadSeeker) error {
	o.mu.Lock()
	defer o.mu.Unlock()
	if !strings.Contains(key, "/theme/") {
		return o.base.PutObject(ctx, key, r)
	}
	if err := o.base.call("upload", key); err != nil {
		return err
	}
	b, err := io.ReadAll(r)
	if err != nil {
		return err
	}
	if d, ok := ctx.Deadline(); ok {
		o.budgets["upload"] = time.Until(d)
	}
	if o.onUpload != nil {
		o.onUpload()
	}
	o.base.objects[key] = b
	if o.changed != nil {
		o.changed <- "put " + key
	}
	return nil
}

func (o *themeObjects) ListObjectsV2(ctx context.Context, prefix string, n int) ([]s3.Object, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if strings.HasSuffix(prefix, "/theme/") {
		if d, ok := ctx.Deadline(); ok {
			o.budgets["list"] = time.Until(d)
		}
	}
	return o.base.ListObjectsV2(ctx, prefix, n)
}

func (o *themeObjects) DeleteObject(ctx context.Context, key string) error {
	o.mu.Lock()
	defer o.mu.Unlock()
	if err := o.base.DeleteObject(ctx, key); err != nil {
		return err
	}
	if strings.Contains(key, "/theme/") && o.changed != nil {
		o.changed <- "delete " + key
	}
	return nil
}

func themeStorage(m *Manager, base *fakeObjects) *themeObjects {
	o := &themeObjects{base: base, budgets: map[string]time.Duration{}}
	m.newClient = func(s3.Config) (objectStore, error) { return o, nil }
	return o
}

func installTheme(t *testing.T, m *Manager, content string) {
	t.Helper()
	_, err := m.st.PutTheme(t.Context(), store.Theme{ID: "a", Name: "A", UploadedAt: time.Now()},
		[]store.ThemeFile{{Path: "index.html", Content: []byte(content)}}, []byte(content), false, 20)
	if err != nil {
		t.Fatal(err)
	}
}

func themePackage(t *testing.T, m *Manager) store.ThemeBackup {
	t.Helper()
	p, err := m.st.ThemeBackupPackage(t.Context(), "a")
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func TestThemeSyncReconcilesAndReplaces(t *testing.T) {
	m, clk, base, _ := setup(t)
	o := themeStorage(m, base)
	installTheme(t, m, "original zip")
	base.objects["tenant/theme/deleted.zip"] = []byte("old")
	base.objects["foreign/theme/keep.zip"] = []byte("foreign")
	tick(t, m)
	if got := string(base.objects["tenant/theme/a.zip"]); got != "original zip" {
		t.Fatalf("uploaded package=%q", got)
	}
	if _, ok := base.objects["tenant/theme/deleted.zip"]; ok {
		t.Fatal("orphan theme object survived synchronization")
	}
	if got := string(base.objects["foreign/theme/keep.zip"]); got != "foreign" {
		t.Fatalf("foreign object changed: %q", got)
	}
	if !themePackage(t, m).Uploaded {
		t.Fatal("successful upload was not recorded")
	}
	if d := o.budgets["upload"]; d <= 5*time.Minute || d > 5*time.Minute+time.Second {
		t.Fatalf("theme upload deadline=%v", d)
	}
	if d := o.budgets["list"]; d <= 4*time.Minute || d > 5*time.Minute {
		t.Fatalf("theme list deadline=%v", d)
	}
	base.calls = nil
	clk.Advance(5 * time.Minute)
	tick(t, m)
	for _, c := range base.calls {
		if c == "upload tenant/theme/a.zip" {
			t.Fatal("unchanged package uploaded again")
		}
	}
	installTheme(t, m, "replacement zip")
	if themePackage(t, m).Uploaded {
		t.Fatal("replacement did not reset uploaded")
	}
	tick(t, m)
	if got := string(base.objects["tenant/theme/a.zip"]); got != "replacement zip" {
		t.Fatalf("replacement not uploaded: %q", got)
	}
	delete(base.objects, "tenant/theme/a.zip")
	clk.Advance(5 * time.Minute)
	tick(t, m)
	if got := string(base.objects["tenant/theme/a.zip"]); got != "replacement zip" {
		t.Fatalf("missing object not repaired: %q", got)
	}
	if _, err := m.st.SaveSettings(t.Context(), store.SettingsUpdate{Backup: &store.BackupSettingsUpdate{Endpoint: "https://example.test", Bucket: "other", Region: "auto", AccessKey: "key", Prefix: "other"}}); err != nil {
		t.Fatal(err)
	}
	clk.Advance(5 * time.Minute)
	tick(t, m)
	if got := string(base.objects["other/theme/a.zip"]); got != "replacement zip" {
		t.Fatalf("new target not populated: %q", got)
	}
}

func TestThemeSyncConcurrentWrite(t *testing.T) {
	for _, recreate := range []bool{false, true} {
		name := "overwrite"
		if recreate {
			name = "delete-reinstall"
		}
		t.Run(name, func(t *testing.T) {
			m, _, base, _ := setup(t)
			o := themeStorage(m, base)
			installTheme(t, m, "old zip")
			old := themePackage(t, m)
			o.onUpload = func() {
				o.onUpload = nil
				if recreate {
					if err := m.st.DeleteTheme(t.Context(), "a"); err != nil {
						t.Fatal(err)
					}
				}
				installTheme(t, m, "new zip")
			}
			tick(t, m)
			p := themePackage(t, m)
			if p.Uploaded || p.Revision == old.Revision || string(p.Content) != "new zip" {
				t.Fatalf("in-flight write marked new package uploaded: %+v old=%d", p, old.Revision)
			}
			tick(t, m)
			if got := string(base.objects["tenant/theme/a.zip"]); got != "new zip" || !themePackage(t, m).Uploaded {
				t.Fatalf("next synchronization did not upload new package: %q", got)
			}
		})
	}
}

func TestThemeSyncFailureAndLostWake(t *testing.T) {
	for _, stage := range []string{"upload", "list", "delete"} {
		t.Run(stage, func(t *testing.T) {
			m, clk, base, sink := setup(t)
			themeStorage(m, base)
			tick(t, m)
			last := status(t, m).Config.LastSuccess
			installTheme(t, m, "zip")
			<-m.st.ThemeChanges()
			base.objects["tenant/theme/orphan.zip"] = nil
			base.failLayer, base.failStage = "theme", stage
			clk.Advance(5 * time.Minute)
			tick(t, m)
			s := status(t, m).Config
			if s.Failure != "theme_"+stage+"/http_status" || s.Since.IsZero() || !s.LastSuccess.Equal(last) || len(sink.events) != 1 {
				t.Fatalf("theme failure not in config state: %+v events=%d", s, len(sink.events))
			}
			if since, err := m.st.BackupFailingSince(t.Context()); err != nil || !since.Equal(s.Since) {
				t.Fatalf("theme failure marker not persisted: %v %v", since, err)
			}
			clk.Advance(5 * time.Minute)
			tick(t, m)
			if len(sink.events) != 1 {
				t.Fatalf("repeated theme failure notified: %d", len(sink.events))
			}
			base.failStage = ""
			clk.Advance(5 * time.Minute)
			tick(t, m)
			if got := string(base.objects["tenant/theme/a.zip"]); got != "zip" {
				t.Fatalf("lost wake not repaired by period: %q", got)
			}
			if s := status(t, m).Config; s.Failure != "" || len(sink.events) != 2 || sink.events[1].Transition != store.TransitionRecovered {
				t.Fatalf("theme recovery not in config state: %+v events=%v", s, sink.events)
			}
			if since, err := m.st.BackupFailingSince(t.Context()); err != nil || !since.IsZero() {
				t.Fatalf("theme failure marker not recovered: %v %v", since, err)
			}
		})
	}
}

func TestThemeSyncDisabledThenConfigured(t *testing.T) {
	m, _, base, _ := setup(t)
	themeStorage(m, base)
	if _, err := m.st.SaveSettings(t.Context(), store.SettingsUpdate{Backup: &store.BackupSettingsUpdate{}}); err != nil {
		t.Fatal(err)
	}
	installTheme(t, m, "zip")
	base.objects["tenant/theme/orphan.zip"] = nil
	tick(t, m)
	if len(base.calls) != 0 {
		t.Fatalf("disabled theme sync touched objects: %v", base.calls)
	}
	if _, err := m.st.SaveSettings(t.Context(), store.SettingsUpdate{Backup: &store.BackupSettingsUpdate{Endpoint: "https://example.test", Bucket: "backups", Region: "auto", AccessKey: "key", Prefix: "tenant"}}); err != nil {
		t.Fatal(err)
	}
	tick(t, m)
	if got := string(base.objects["tenant/theme/a.zip"]); got != "zip" {
		t.Fatalf("later configuration did not upload package: %q", got)
	}
}

func TestThemeSyncRunWakesAfterCommit(t *testing.T) {
	m, _, base, _ := setup(t)
	o := themeStorage(m, base)
	tick(t, m)
	o.changed = make(chan string, 4)
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	go func() { defer close(done); m.Run(ctx) }()
	defer func() { cancel(); <-done }()
	wait := func(want string) {
		t.Helper()
		select {
		case got := <-o.changed:
			if got != want {
				t.Fatalf("wake operation=%q want=%q", got, want)
			}
		case <-time.After(3 * time.Second):
			t.Fatalf("committed theme did not wake manager: %s", want)
		}
	}
	installTheme(t, m, "zip")
	wait("put tenant/theme/a.zip")
	if err := m.st.DeleteTheme(t.Context(), "a"); err != nil {
		t.Fatal(err)
	}
	wait("delete tenant/theme/a.zip")
}

func TestThemeSyncMissingPackageAndRecordFailure(t *testing.T) {
	for _, stage := range []string{"package", "record"} {
		t.Run(stage, func(t *testing.T) {
			m, _, base, sink := setup(t)
			themeStorage(m, base)
			installTheme(t, m, "zip")
			if stage == "package" {
				execFixtureSQL(t, base.databasePath, "DELETE FROM theme_package")
			} else {
				execFixtureSQL(t, base.databasePath, "CREATE TRIGGER reject_mark BEFORE UPDATE OF uploaded ON theme_package BEGIN SELECT RAISE(ABORT, 'mark rejected'); END")
			}
			tick(t, m)
			if s := status(t, m).Config; s.Failure != "theme_"+stage || !s.LastSuccess.IsZero() || len(sink.events) != 1 {
				t.Fatalf("theme %s failure hidden: %+v events=%d", stage, s, len(sink.events))
			}
		})
	}
}

func TestThemeFailureSharesMarkerAndDisabledLifecycle(t *testing.T) {
	m, _, base, sink := setup(t)
	themeStorage(m, base)
	installTheme(t, m, "zip")
	execFixtureSQL(t, base.databasePath, `INSERT INTO setting VALUES ('backup.config_failing_since','x')`)
	tick(t, m)
	if s, err := m.Status(t.Context()); err != nil || s.Config.Failure != "marker" || len(sink.events) != 1 {
		t.Fatalf("theme wake bypassed marker preparation: %+v err=%v events=%v", s, err, sink.events)
	}
	base.failLayer, base.failStage = "theme", "upload"
	tick(t, m)
	if s := status(t, m).Config; s.Failure != "theme_upload/http_status" || len(sink.events) != 1 {
		t.Fatalf("theme failure did not continue marker lifecycle: %+v events=%v", s, sink.events)
	}
	if _, err := m.st.SaveSettings(t.Context(), store.SettingsUpdate{Backup: &store.BackupSettingsUpdate{}}); err != nil {
		t.Fatal(err)
	}
	tick(t, m)
	if s := status(t, m).Config; s.Failure != "" || len(sink.events) != 2 || sink.events[1].Transition != store.TransitionDisabled {
		t.Fatalf("theme failure did not close on disable: %+v events=%v", s, sink.events)
	}
}
