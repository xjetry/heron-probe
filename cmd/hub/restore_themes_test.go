package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/xjetry/probe/internal/hub/theme"
	. "github.com/xjetry/probe/internal/hub/theme/themetest"
)

func themeRestoreFixture(t *testing.T) (string, string) {
	t.Helper()
	config, _ := restoreSnapshots(t)
	restoreExec(t, restoreDB(t, config), "INSERT INTO theme VALUES ('a','A','1','',100,1),('b','B','1','',100,0)")
	path := restoreTarget(t)
	db := restoreDB(t, path)
	restoreExec(t, db, `INSERT INTO theme_file VALUES ('a','stale.js',x'00'),('gone','index.html',x'01');
		INSERT INTO theme_package VALUES ('a',x'00',1,1),('gone',x'01',2,1)`)
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	return config, path
}

type themeRestoreSummary struct {
	Restored []string `json:"restored"`
	Missing  []string `json:"missing"`
	Ignored  []string `json:"ignored"`
}

func checkThemeRestoreSummary(t *testing.T, path string, output []byte, want themeRestoreSummary) {
	t.Helper()
	var result struct {
		Themes themeRestoreSummary `json:"themes"`
	}
	if err := json.Unmarshal(bytes.TrimPrefix(bytes.TrimSpace(output), []byte("restored: ")), &result); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(result.Themes, want) {
		t.Errorf("theme restore summary=%+v want=%+v", result.Themes, want)
	}
	var record string
	if err := restoreDB(t, path).QueryRow("SELECT themes FROM restore_record ORDER BY rowid DESC LIMIT 1").Scan(&record); err != nil {
		t.Fatal(err)
	}
	var got themeRestoreSummary
	if err := json.Unmarshal([]byte(record), &got); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("theme restore audit=%+v want=%+v", got, want)
	}
}

func TestRestoreThemesOmittedClearsAndDisables(t *testing.T) {
	config, path := themeRestoreFixture(t)
	var out bytes.Buffer
	restoreExec(t, restoreDB(t, config), "UPDATE theme SET preview='old.png'")
	if err := runRestoreWith([]string{"--db", path, "--config", config, "--yes"}, &out); err != nil {
		t.Fatal(err)
	}
	db := restoreDB(t, path)
	restoreWant(t, db, "SELECT group_concat(id || ':' || enabled) FROM (SELECT * FROM theme ORDER BY id)", "a:0,b:0")
	restoreWant(t, db, "SELECT count(*) FROM theme_file", "0")
	restoreWant(t, db, "SELECT count(*) FROM theme_package", "0")
	restoreWant(t, db, "SELECT count(*) FROM theme WHERE preview <> ''", "0")
	checkThemeRestoreSummary(t, path, out.Bytes(), themeRestoreSummary{Missing: []string{"a", "b"}})
}

func TestRestoreThemesCommand(t *testing.T) {
	config, path := themeRestoreFixture(t)
	dir := t.TempDir()
	pkg := Minimal(t, "a", File("assets/new.js", "new"))
	for name, content := range map[string][]byte{"a.zip": pkg, "extra.zip": Minimal(t, "extra")} {
		if err := os.WriteFile(filepath.Join(dir, name), content, 0600); err != nil {
			t.Fatal(err)
		}
	}
	cmd := hubCommand(t, "restore", "--db", path, "--config", config, "--themes", dir, "--yes")
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("restore themes command: %v", err)
	}
	db := restoreDB(t, path)
	restoreWant(t, db, "SELECT group_concat(id || ':' || enabled) FROM (SELECT * FROM theme ORDER BY id)", "a:1,b:0")
	restoreWant(t, db, "SELECT group_concat(path) FROM (SELECT path FROM theme_file ORDER BY path)", "assets/new.js,index.html,theme.json")
	restoreWant(t, db, "SELECT count(*) FROM theme_package", "1")
	var raw []byte
	var revision int64
	var uploaded bool
	if err := db.QueryRow("SELECT content,revision,uploaded FROM theme_package WHERE theme_id='a'").Scan(&raw, &revision, &uploaded); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(raw, pkg) || revision <= 0 || uploaded {
		t.Fatalf("restored original package differs or is marked uploaded: revision=%d uploaded=%t", revision, uploaded)
	}
	checkThemeRestoreSummary(t, path, out, themeRestoreSummary{Restored: []string{"a"}, Missing: []string{"b"}, Ignored: []string{"extra.zip"}})
}

func TestRestoreThemesInvalidLeavesTargetUntouched(t *testing.T) {
	for _, defect := range []string{"missing-directory", "bad-package", "bad-extra", "wrong-id", "oversized", "invalid-filename"} {
		for _, existing := range []bool{false, true} {
			t.Run(defect+map[bool]string{false: "/new", true: "/existing"}[existing], func(t *testing.T) {
				config, path := themeRestoreFixture(t)
				if !existing {
					path = filepath.Join(t.TempDir(), "new.db")
				}
				var before []byte
				if existing {
					var err error
					before, err = os.ReadFile(path)
					if err != nil {
						t.Fatal(err)
					}
				}
				dir := t.TempDir()
				if err := os.WriteFile(filepath.Join(dir, "a.zip"), Minimal(t, "a"), 0600); err != nil {
					t.Fatal(err)
				}
				name, content, wantErr := "b.zip", []byte("invalid zip"), "theme package"
				switch defect {
				case "invalid-filename":
					name, content, wantErr = "A (1).zip", Minimal(t, "a"), "invalid theme id"
				case "missing-directory":
					dir = filepath.Join(dir, "missing")
					wantErr = "themes directory"
				case "bad-extra":
					name = "extra.zip"
				case "wrong-id":
					name = "a.zip"
					content = Minimal(t, "other")
					wantErr = "does not match"
				case "oversized":
					content = make([]byte, theme.MaxPackageBytes+1)
				}
				if defect != "missing-directory" {
					if err := os.WriteFile(filepath.Join(dir, name), content, 0600); err != nil {
						t.Fatal(err)
					}
				}
				err := runRestoreWith([]string{"--db", path, "--config", config, "--themes", dir, "--yes"}, &bytes.Buffer{})
				if err == nil || !strings.Contains(err.Error(), wantErr) {
					t.Errorf("invalid themes accepted or wrong error: %v want %q", err, wantErr)
				}
				if existing {
					after, err := os.ReadFile(path)
					if err != nil || !bytes.Equal(before, after) {
						t.Fatalf("invalid themes changed target bytes: %v", err)
					}
				} else if _, err := os.Stat(path); !os.IsNotExist(err) {
					t.Fatalf("invalid themes created target: %v", err)
				}
			})
		}
	}
}

func TestRestoreThemesWriteFailureRollsBack(t *testing.T) {
	config, path := themeRestoreFixture(t)
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "a.zip"), Minimal(t, "a"), 0600); err != nil {
		t.Fatal(err)
	}
	restoreExec(t, restoreDB(t, path), "CREATE TRIGGER reject_theme BEFORE INSERT ON theme_file BEGIN SELECT RAISE(ABORT,'theme write rejected'); END")
	before := restoreDump(t, path)
	err := runRestoreWith([]string{"--db", path, "--config", config, "--themes", dir, "--yes"}, &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "theme write rejected") {
		t.Fatalf("theme write failure hidden: %v", err)
	}
	if after := restoreDump(t, path); after != before {
		t.Fatal("theme write failure changed target tables or audit")
	}
}

func TestRestoreThemesPreflightPrecedesTargetOpen(t *testing.T) {
	config, _ := themeRestoreFixture(t)
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "a.zip"), []byte("bad zip"), 0600); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "missing-parent", "target.db")
	err := runRestoreWith([]string{"--db", path, "--config", config, "--themes", dir, "--yes"}, &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "theme package") {
		t.Fatalf("theme preflight did not precede target open: %v", err)
	}
}

func TestRestoreThemesMissingEnabledWithDirectory(t *testing.T) {
	config, path := themeRestoreFixture(t)
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "b.zip"), Minimal(t, "b"), 0600); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := runRestoreWith([]string{"--db", path, "--config", config, "--themes", dir, "--yes"}, &out); err != nil {
		t.Fatal(err)
	}
	restoreWant(t, restoreDB(t, path), "SELECT group_concat(id || ':' || enabled) FROM (SELECT * FROM theme ORDER BY id)", "a:0,b:0")
	checkThemeRestoreSummary(t, path, out.Bytes(), themeRestoreSummary{Restored: []string{"b"}, Missing: []string{"a"}})
}

func TestRestoreThemesManifestMetadata(t *testing.T) {
	config, path := themeRestoreFixture(t)
	restoreExec(t, restoreDB(t, config), "UPDATE theme SET name='old',version='old',preview='old.png' WHERE id='a'")
	dir := t.TempDir()
	raw := Minimal(t, "a")
	pkg, err := theme.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "a.zip"), raw, 0600); err != nil {
		t.Fatal(err)
	}
	if err := runRestoreWith([]string{"--db", path, "--config", config, "--themes", dir, "--yes"}, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	db := restoreDB(t, path)
	restoreWant(t, db, "SELECT name || ':' || version || ':' || preview || ':' || uploaded_at || ':' || enabled FROM theme WHERE id='a'", pkg.Manifest.Name+":"+pkg.Manifest.Version+"::100:1")
}

func TestRestoreThemesSourceAdmissionFirst(t *testing.T) {
	_, metrics := restoreSnapshots(t)
	err := runRestoreWith([]string{"--db", filepath.Join(t.TempDir(), "target.db"), "--config", metrics, "--themes", t.TempDir(), "--yes"}, &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "config snapshot missing table") {
		t.Fatalf("source admission did not precede theme preflight: %v", err)
	}
}
