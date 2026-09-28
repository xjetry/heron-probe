package store

import (
	"bytes"
	"database/sql"
	"errors"
	"fmt"
	"maps"
	"reflect"
	"slices"
	"strings"
	"sync"
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
	th, err := s.PutTheme(t.Context(), Theme{ID: id, Name: "Theme " + id, Version: "1", UploadedAt: time.Unix(100, 0)}, files, []byte("original zip"), false, 20)
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
		[]ThemeFile{{Path: "index.html", Content: []byte("new")}, {Path: "p.png", Content: []byte("png")}, {Path: "empty.css", Content: []byte{}}}, []byte("original zip"), true, 20)
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
		[]ThemeFile{{Path: "index.html", Content: []byte("new")}, {Path: "dup", Content: []byte("1")}, {Path: "dup", Content: []byte("2")}}, []byte("original zip"), true, 20)
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
		[]ThemeFile{{Path: "dup", Content: []byte("1")}, {Path: "dup", Content: []byte("2")}}, []byte("original zip"), false, 20); err == nil {
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
	_, err := s.PutTheme(t.Context(), Theme{ID: "d", Name: "D", Version: "1"}, []ThemeFile{{Path: "index.html", Content: []byte("d")}}, []byte("original zip"), false, 3)
	if !errors.Is(err, ErrThemeLimit) {
		t.Fatalf("fourth theme with limit 3: %v, want ErrThemeLimit", err)
	}
	if files := themeFiles(t, s, "d"); len(files) != 0 {
		t.Fatalf("rejected theme left files: %v", files)
	}
	if _, err := s.PutTheme(t.Context(), Theme{ID: "b", Name: "B2", Version: "2"}, []ThemeFile{{Path: "index.html", Content: []byte("b2")}}, []byte("original zip"), true, 3); err != nil {
		t.Fatalf("replacing an installed theme at the limit: %v", err)
	}
}

// mustExist：声明为更新而目标不在，什么都不写。
func TestPutThemeMustExist(t *testing.T) {
	s, _ := open(t)
	_, err := s.PutTheme(t.Context(), Theme{ID: "a", Name: "A", Version: "1"}, []ThemeFile{{Path: "index.html", Content: []byte("a")}}, []byte("original zip"), true, 20)
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

// 托管读的是启用中主题的整包：别的主题里同名的文件不会被读到；没有启用中的主题与"启用了但包里没有某个路径"是两个不同的
// 答案，前者让主题 origin 回落内置公开页，后者按包内的回落规则处理。
func TestEnabledThemePackageReadsOnlyTheEnabledPackage(t *testing.T) {
	s, _ := open(t)
	read := func() (map[string]string, bool) {
		t.Helper()
		gen, files, enabled, err := s.EnabledThemePackage(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		if now := s.ThemeGeneration(); gen != now {
			t.Fatalf("package read at generation %d with no concurrent writes, want the current generation %d", gen, now)
		}
		out := map[string]string{}
		for p, c := range files {
			out[p] = string(c)
		}
		return out, enabled
	}
	if files, enabled := read(); enabled || len(files) != 0 {
		t.Fatalf("no theme installed: %v %v, want nothing enabled", files, enabled)
	}
	putTheme(t, s, "a", "index.html", "assets/app.js")
	putTheme(t, s, "b", "index.html", "assets/app.js", "only-b.css")
	if files, enabled := read(); enabled || len(files) != 0 {
		t.Fatalf("themes installed but none enabled: %v %v, want nothing enabled", files, enabled)
	}
	if err := s.EnableTheme(t.Context(), "a"); err != nil {
		t.Fatal(err)
	}
	if files, enabled := read(); !enabled || !reflect.DeepEqual(files, map[string]string{"assets/app.js": "a:assets/app.js", "index.html": "a:index.html"}) {
		t.Fatalf("theme a enabled: %v %v, want exactly a's package", files, enabled)
	}
	if err := s.DeleteTheme(t.Context(), "a"); err != nil {
		t.Fatal(err)
	}
	if files, enabled := read(); enabled || len(files) != 0 {
		t.Fatalf("enabled theme deleted: %v %v, want nothing enabled", files, enabled)
	}
}

// 代数随 PutTheme、EnableTheme、DeleteTheme 的每次提交前进，被拒绝、未落库的写不动它：托管只在代数变了时重读，
// 漏掉一个提交就会一直服务旧包。
func TestThemeGenerationAdvancesOnEveryCommittedThemeWrite(t *testing.T) {
	s, _ := open(t)
	ctx := t.Context()
	step := func(what string, want uint64, op func() error, wantErr bool) {
		t.Helper()
		before := s.ThemeGeneration()
		err := op()
		if (err != nil) != wantErr {
			t.Fatalf("%s: err = %v, want error %v", what, err, wantErr)
		}
		if got := s.ThemeGeneration() - before; got != want {
			t.Fatalf("%s: generation advanced by %d, want %d", what, got, want)
		}
	}
	put := func(id string, mustExist bool, limit int, files ...ThemeFile) func() error {
		return func() error {
			_, err := s.PutTheme(ctx, Theme{ID: id, Name: id, Version: "1", UploadedAt: time.Unix(100, 0)}, files, []byte("original zip"), mustExist, limit)
			return err
		}
	}
	index := ThemeFile{Path: "index.html", Content: []byte("x")}
	step("install a", 1, put("a", false, 20, index), false)
	step("replace a", 1, put("a", true, 20, index), false)
	step("install b", 1, put("b", false, 20, index), false)
	step("replace with a duplicate path", 0, put("a", true, 20, index, ThemeFile{Path: "dup"}, ThemeFile{Path: "dup"}), true)
	step("update a missing theme", 0, put("c", true, 20, index), true)
	step("install past the limit", 0, put("c", false, 2, index), true)
	step("enable a", 1, func() error { return s.EnableTheme(ctx, "a") }, false)
	step("enable a missing theme", 0, func() error { return s.EnableTheme(ctx, "missing") }, true)
	step("enable none", 1, func() error { return s.EnableTheme(ctx, "") }, false)
	step("delete b", 1, func() error { return s.DeleteTheme(ctx, "b") }, false)
	step("delete a missing theme", 0, func() error { return s.DeleteTheme(ctx, "b") }, true)
}

// 整包是一个快照：写者不停地整包替换、删除、重装、启用时，每次读出的要么是没有启用中的主题，要么是某一次提交的完整的包
// （文件一个不少、全部出自同一次写入），且内容不旧于标注的代数。任何一次读拆成两条语句，两条之间落下的提交就会让
// 读出的包缺文件或混着两次写入；代数若在读库之后才取，读的过程中落下的提交会让旧内容标上新代数。
func TestEnabledThemePackageIsOneSnapshotNotOlderThanItsGeneration(t *testing.T) {
	s, _ := open(t)
	ctx := t.Context()
	const nFiles = 100
	pad := strings.Repeat("-", 1000)
	// 每次写入的文件内容都以写入时的序号开头：同一个包里的文件序号必须相同。
	install := func(seq uint64) error {
		files := make([]ThemeFile, 0, nFiles)
		for i := range nFiles {
			p := fmt.Sprintf("assets/f%03d.js", i)
			if i == 0 {
				p = "index.html"
			}
			files = append(files, ThemeFile{Path: p, Content: fmt.Appendf(nil, "%d|%s", seq, pad)})
		}
		_, err := s.PutTheme(ctx, Theme{ID: "a", Name: "a", Version: "1", UploadedAt: time.Unix(100, 0)}, files, []byte("original zip"), false, 20)
		return err
	}
	if err := install(0); err != nil {
		t.Fatal(err)
	}
	if err := s.EnableTheme(ctx, "a"); err != nil {
		t.Fatal(err)
	}
	// state 是某一代提交之后启用中主题的样子：enabled 为假时没有启用中的主题，否则 seq 是它的包是哪一次写入的。
	type state struct {
		enabled bool
		seq     uint64
	}
	base := s.ThemeGeneration()
	var mu sync.Mutex
	states := map[uint64]state{base: {true, 0}} // 代数 → 该代提交之后的状态，由写者在每次提交返回后登记。
	done := make(chan struct{})
	writeErr := make(chan error, 1)
	go func() {
		defer close(done)
		cur := state{true, 0}
		for seq := uint64(1); seq <= 400; seq++ {
			var err error
			switch seq % 6 {
			case 0, 1, 2: // 整包替换，启用状态沿用。
				err = install(seq)
				cur.seq = seq
			case 3:
				err = s.DeleteTheme(ctx, "a")
				cur = state{false, 0}
			case 4: // 重装：新装的主题未启用。
				err = install(seq)
				cur = state{false, 0}
			case 5:
				err = s.EnableTheme(ctx, "a")
				cur = state{true, seq - 1}
			}
			if err != nil {
				writeErr <- err
				return
			}
			mu.Lock()
			states[s.ThemeGeneration()] = cur
			mu.Unlock()
		}
	}()
	type observation struct {
		gen   uint64
		state state
	}
	var seen []observation
	for reading := true; reading; {
		select {
		case <-done:
			reading = false
		default:
		}
		gen, files, enabled, err := s.EnabledThemePackage(ctx)
		if err != nil {
			t.Fatal(err)
		}
		obs := observation{gen: gen, state: state{enabled: enabled}}
		if enabled {
			seqs := map[string]bool{}
			for _, c := range files {
				seqs[string(c[:bytes.IndexByte(c, '|')])] = true
			}
			if len(files) != nFiles || files["index.html"] == nil || len(seqs) != 1 {
				t.Fatalf("read at generation %d: %d files (index.html present: %v) from writes %v, want all %d files of one write", gen, len(files), files["index.html"] != nil, slices.Sorted(maps.Keys(seqs)), nFiles)
			}
			for seq := range seqs {
				fmt.Sscan(seq, &obs.state.seq)
			}
		} else if len(files) != 0 {
			t.Fatalf("read at generation %d: no theme enabled but %d files", gen, len(files))
		}
		seen = append(seen, obs)
	}
	select {
	case err := <-writeErr:
		t.Fatal(err)
	default:
	}
	// 标注为代数 g 的内容必须是 g 或更晚某一代的状态：写者先提交后递增，读者先取代数后读库。
	final := s.ThemeGeneration()
	for _, o := range seen {
		ok := false
		for g := o.gen; g <= final && !ok; g++ {
			st, known := states[g]
			ok = known && st == o.state
		}
		if !ok {
			t.Fatalf("read labelled generation %d returned %+v, which is no state at or after that generation", o.gen, o.state)
		}
	}
	t.Logf("%d package reads across %d writes", len(seen), final-base)
}
