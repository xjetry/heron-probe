package store

import (
	"database/sql"
	"errors"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"
)

// v15 的完整 DDL：v14 加上节点标签的两张表与索引。
var schemaV15 = append(slices.Clone(schemaV14),
	"CREATE TABLE tag (id INTEGER PRIMARY KEY AUTOINCREMENT, name TEXT NOT NULL, name_fold TEXT NOT NULL UNIQUE)",
	"CREATE TABLE node_tag (node_id INTEGER NOT NULL, tag_id INTEGER NOT NULL, PRIMARY KEY (node_id, tag_id)) WITHOUT ROWID",
	"CREATE INDEX node_tag_by_tag ON node_tag (tag_id)")

// 旧库升级后与新建库结构相同，节点原样保留且没有主题；升级后的库能装主题。"至多一行启用"由两条约束共同承载，
// 两库各自对照：部分唯一索引 theme_enabled 拒绝第二行 1，CHECK 拒绝 0 与 1 以外的值（否则 2 这样的值绕过只看
// enabled = 1 的索引）。describe 不读 CHECK，所以 CHECK 只能由这里的写入来钉。
func TestMigrationFromV15AddsThemeTables(t *testing.T) {
	migrated, fresh := migrateFrom(t, schemaV15, 15, func(t *testing.T, db *sql.DB) {
		if _, err := db.Exec("INSERT INTO node (id, name, token_hash, created_at) VALUES (7, 'kept', x'00', 1)"); err != nil {
			t.Fatal(err)
		}
	})
	if got, want := describe(t, migrated.r), describe(t, fresh.r); !reflect.DeepEqual(got, want) {
		t.Fatalf("migrated schema differs from fresh schema:\n got: %+v\nwant: %+v", got, want)
	}
	if v := userVersion(t, migrated.r); v != schemaVersion {
		t.Fatalf("user_version = %d, want %d", v, schemaVersion)
	}
	if n, err := migrated.GetNode(t.Context(), 7); err != nil || n.Name != "kept" {
		t.Fatalf("node after migration: %+v %v", n, err)
	}
	for _, s := range []*Store{migrated, fresh} {
		kind := map[bool]string{true: "migrated", false: "fresh"}[s == migrated]
		putTheme(t, s, "a", "index.html")
		putTheme(t, s, "b", "index.html")
		if err := s.EnableTheme(t.Context(), "a"); err != nil {
			t.Fatal(err)
		}
		if err := forceEnabled(t, s, "b", 1); err == nil || !strings.Contains(err.Error(), "UNIQUE constraint failed") {
			t.Errorf("second enabled row on a %s database: %v, want the theme_enabled index to refuse it", kind, err)
		}
		if err := forceEnabled(t, s, "b", 2); err == nil || !strings.Contains(err.Error(), "CHECK constraint failed") {
			t.Errorf("enabled = 2 on a %s database: %v, want the CHECK on theme.enabled to refuse it", kind, err)
		}
		if got := enabledColumn(t, s); !reflect.DeepEqual(got, map[string]int{"a": 1, "b": 0}) {
			t.Errorf("theme.enabled on a %s database after the refused writes = %v, want a=1 b=0", kind, got)
		}
	}
}

// enabledColumn 直接读 theme.enabled 的原值：约束失守时列里可能是 2，经 ListThemes 读会在扫描成 bool 时出错。
func enabledColumn(t *testing.T, s *Store) map[string]int {
	t.Helper()
	rows, err := s.r.Query("SELECT id, enabled FROM theme")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	out := map[string]int{}
	for rows.Next() {
		var id string
		var v int
		if err := rows.Scan(&id, &v); err != nil {
			t.Fatal(err)
		}
		out[id] = v
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

// forceEnabled 绕开 EnableTheme 直接改表：约束由 schema 承载，不依赖写者自觉先清再置、只写 0 与 1。
func forceEnabled(t *testing.T, s *Store, id string, v int) error {
	t.Helper()
	return s.write(t.Context(), func(tx *sql.Tx) error {
		_, err := tx.Exec("UPDATE theme SET enabled = ? WHERE id = ?", v, id)
		return err
	})
}

func putTheme(t *testing.T, s *Store, id string, paths ...string) Theme {
	t.Helper()
	var files []ThemeFile
	for _, p := range paths {
		files = append(files, ThemeFile{Path: p, Content: []byte(id + ":" + p)})
	}
	th, err := s.PutTheme(t.Context(), Theme{ID: id, Name: "Theme " + id, Version: "1", UploadedAt: time.Unix(100, 0)}, files, false, 20)
	if err != nil {
		t.Fatal(err)
	}
	return th
}

// themeFiles 是 theme_file 里某主题的 path→content；直接读表，不经任何 Store 方法。
func themeFiles(t *testing.T, s *Store, id string) map[string]string {
	t.Helper()
	rows, err := s.r.Query("SELECT path, content FROM theme_file WHERE theme_id = ?", id)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var p string
		var c []byte
		if err := rows.Scan(&p, &c); err != nil {
			t.Fatal(err)
		}
		out[p] = string(c)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

func enabledIDs(t *testing.T, s *Store) []string {
	t.Helper()
	list, err := s.ListThemes(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	out := []string{}
	for _, th := range list {
		if th.Enabled {
			out = append(out, th.ID)
		}
	}
	return out
}

// 同一 id 重传整体替换：旧包独有的文件不再存在，元数据换成新的，启用状态沿用。
func TestPutThemeReplacesTheWholePackageAndKeepsEnabled(t *testing.T) {
	s, _ := open(t)
	putTheme(t, s, "a", "index.html", "old.js", "assets/x.css")
	if err := s.EnableTheme(t.Context(), "a"); err != nil {
		t.Fatal(err)
	}
	got, err := s.PutTheme(t.Context(), Theme{ID: "a", Name: "Renamed", Version: "2", Preview: "p.png", UploadedAt: time.Unix(200, 0)},
		[]ThemeFile{{Path: "index.html", Content: []byte("new")}, {Path: "p.png", Content: []byte("png")}, {Path: "empty.css", Content: []byte{}}}, true, 20)
	if err != nil {
		t.Fatal(err)
	}
	if want := (Theme{ID: "a", Name: "Renamed", Version: "2", Preview: "p.png", UploadedAt: time.Unix(200, 0).UTC(), Enabled: true}); got != want {
		t.Fatalf("PutTheme = %+v, want %+v", got, want)
	}
	if files := themeFiles(t, s, "a"); !reflect.DeepEqual(files, map[string]string{"index.html": "new", "p.png": "png", "empty.css": ""}) {
		t.Fatalf("files after replace = %v, want only the new package", files)
	}
	th, content, err := s.ThemePreview(t.Context(), "a")
	if err != nil || th != got || string(content) != "png" {
		t.Fatalf("ThemePreview = %+v %q %v", th, content, err)
	}
}

// 替换在一个事务里：写到一半失败（这里用重复路径撞 theme_file 主键）时旧包原样保留，元数据也不变。
func TestPutThemeFailureLeavesThePreviousPackageIntact(t *testing.T) {
	s, _ := open(t)
	putTheme(t, s, "a", "index.html", "old.js")
	before, err := s.ListThemes(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.PutTheme(t.Context(), Theme{ID: "a", Name: "Broken", Version: "2", UploadedAt: time.Unix(300, 0)},
		[]ThemeFile{{Path: "index.html", Content: []byte("new")}, {Path: "dup", Content: []byte("1")}, {Path: "dup", Content: []byte("2")}}, true, 20)
	if err == nil {
		t.Fatal("PutTheme with a duplicate path succeeded")
	}
	if files := themeFiles(t, s, "a"); !reflect.DeepEqual(files, map[string]string{"index.html": "a:index.html", "old.js": "a:old.js"}) {
		t.Fatalf("files after failed replace = %v, want the previous package", files)
	}
	if after, err := s.ListThemes(t.Context()); err != nil || !reflect.DeepEqual(after, before) {
		t.Fatalf("themes after failed replace = %+v %v, want %+v", after, err, before)
	}
	// 新建时失败同样不留下元数据行。
	if _, err := s.PutTheme(t.Context(), Theme{ID: "b", Name: "B", Version: "1", UploadedAt: time.Unix(300, 0)},
		[]ThemeFile{{Path: "dup", Content: []byte("1")}, {Path: "dup", Content: []byte("2")}}, false, 20); err == nil {
		t.Fatal("PutTheme with a duplicate path succeeded")
	}
	if list, _ := s.ListThemes(t.Context()); len(list) != 1 || len(themeFiles(t, s, "b")) != 0 {
		t.Fatalf("failed insert left rows: %+v %v", list, themeFiles(t, s, "b"))
	}
}

// 上限只数不同的 id：满额时新 id 被拒且不留任何行，已装的 id 仍可替换。
func TestPutThemeLimitCountsOnlyNewIDs(t *testing.T) {
	s, _ := open(t)
	for i := range 3 {
		putTheme(t, s, string(rune('a'+i)), "index.html")
	}
	_, err := s.PutTheme(t.Context(), Theme{ID: "d", Name: "D", Version: "1"}, []ThemeFile{{Path: "index.html", Content: []byte("d")}}, false, 3)
	if !errors.Is(err, ErrThemeLimit) {
		t.Fatalf("fourth theme with limit 3: %v, want ErrThemeLimit", err)
	}
	if files := themeFiles(t, s, "d"); len(files) != 0 {
		t.Fatalf("rejected theme left files: %v", files)
	}
	if _, err := s.PutTheme(t.Context(), Theme{ID: "b", Name: "B2", Version: "2"}, []ThemeFile{{Path: "index.html", Content: []byte("b2")}}, true, 3); err != nil {
		t.Fatalf("replacing an installed theme at the limit: %v", err)
	}
}

// mustExist：声明为更新而目标不在，什么都不写。
func TestPutThemeMustExist(t *testing.T) {
	s, _ := open(t)
	_, err := s.PutTheme(t.Context(), Theme{ID: "a", Name: "A", Version: "1"}, []ThemeFile{{Path: "index.html", Content: []byte("a")}}, true, 20)
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("update of a missing theme: %v, want ErrNotFound", err)
	}
	if list, _ := s.ListThemes(t.Context()); len(list) != 0 || len(themeFiles(t, s, "a")) != 0 {
		t.Fatalf("update of a missing theme left rows: %+v", list)
	}
}

func TestEnableThemeKeepsAtMostOneAndDeleteFallsBack(t *testing.T) {
	s, _ := open(t)
	putTheme(t, s, "a", "index.html")
	putTheme(t, s, "b", "index.html")
	ctx := t.Context()
	if err := s.EnableTheme(ctx, "a"); err != nil {
		t.Fatal(err)
	}
	if err := s.EnableTheme(ctx, "b"); err != nil {
		t.Fatal(err)
	}
	if got := enabledIDs(t, s); !slices.Equal(got, []string{"b"}) {
		t.Fatalf("enabled after enabling b = %v, want [b]", got)
	}
	if err := s.EnableTheme(ctx, "b"); err != nil {
		t.Fatalf("re-enabling the enabled theme: %v", err)
	}
	if err := s.EnableTheme(ctx, "missing"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("enabling a missing theme: %v, want ErrNotFound", err)
	}
	if got := enabledIDs(t, s); !slices.Equal(got, []string{"b"}) {
		t.Fatalf("enabled after a failed enable = %v, want [b] unchanged", got)
	}
	if err := s.EnableTheme(ctx, ""); err != nil {
		t.Fatal(err)
	}
	if got := enabledIDs(t, s); len(got) != 0 {
		t.Fatalf("enabled after enabling none = %v, want none", got)
	}
	if err := s.EnableTheme(ctx, "a"); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteTheme(ctx, "a"); err != nil {
		t.Fatal(err)
	}
	if got := enabledIDs(t, s); len(got) != 0 {
		t.Fatalf("enabled after deleting the enabled theme = %v, want none", got)
	}
	if files := themeFiles(t, s, "a"); len(files) != 0 {
		t.Fatalf("deleted theme left files: %v", files)
	}
	if err := s.DeleteTheme(ctx, "a"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("deleting a missing theme: %v, want ErrNotFound", err)
	}
	if _, _, err := s.ThemePreview(ctx, "a"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("preview of a deleted theme: %v, want ErrNotFound", err)
	}
	if th, content, err := s.ThemePreview(ctx, "b"); err != nil || th.ID != "b" || content != nil {
		t.Fatalf("preview of a theme without one = %+v %q %v, want the theme and no content", th, content, err)
	}
}
