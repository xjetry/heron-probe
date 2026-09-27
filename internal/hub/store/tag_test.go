package store

import (
	"database/sql"
	"errors"
	"reflect"
	"slices"
	"strings"
	"testing"
)

// v14 的完整 DDL：v13 加上节点的国家三列。
var schemaV14 = append(slices.Clone(schemaV13),
	"ALTER TABLE node ADD COLUMN country TEXT NOT NULL DEFAULT ''",
	"ALTER TABLE node ADD COLUMN country_ip TEXT NOT NULL DEFAULT ''",
	"ALTER TABLE node ADD COLUMN country_pin TEXT NOT NULL DEFAULT ''")

// 旧库升级后与新建库结构相同，节点原样保留且没有标签；升级后的库能挂标签。
func TestMigrationFromV14AddsTagTables(t *testing.T) {
	migrated, fresh := migrateFrom(t, schemaV14, 14, func(t *testing.T, db *sql.DB) {
		if _, err := db.Exec("INSERT INTO node (id, name, token_hash, created_at, country_pin) VALUES (7, 'kept', x'00', 1, 'JP')"); err != nil {
			t.Fatal(err)
		}
	})
	if got, want := describe(t, migrated.r), describe(t, fresh.r); !reflect.DeepEqual(got, want) {
		t.Fatalf("migrated schema differs from fresh schema:\n got: %+v\nwant: %+v", got, want)
	}
	if v := userVersion(t, migrated.r); v != schemaVersion {
		t.Fatalf("user_version = %d, want %d", v, schemaVersion)
	}
	n, err := migrated.GetNode(t.Context(), 7)
	if err != nil || n.Name != "kept" || n.CountryPin != "JP" || n.Tags != nil {
		t.Fatalf("node after migration: %+v %v", n, err)
	}
	setTags(t, migrated, 7, "db")
	if n, err := migrated.GetNode(t.Context(), 7); err != nil || !slices.Equal(n.Tags, []string{"db"}) {
		t.Fatalf("tags on a migrated database: %+v %v", n, err)
	}
}

func setTags(t *testing.T, s *Store, id int64, tags ...string) {
	t.Helper()
	n, err := s.GetNode(t.Context(), id)
	if err != nil {
		t.Fatal(err)
	}
	edit := NodeEdit{Name: n.Name, Public: n.Public, Note: n.Note, TrafficResetDay: n.TrafficResetDay, OfflineGraceS: n.OfflineGraceS,
		Billing: n.Billing, CountryPin: n.CountryPin, Tags: tags}
	if _, err := s.UpdateNode(t.Context(), id, edit); err != nil {
		t.Fatal(err)
	}
}

func nodeNames(nodes []Node) []string {
	out := []string{}
	for _, n := range nodes {
		out = append(out, n.Name)
	}
	return out
}

// 三个节点分别挂 {a}、{a,b}、{b}：多选取交集，空选择返回全部，不存在的标签让结果为空。
func TestListNodesByTagsIsAnIntersection(t *testing.T) {
	s, _ := open(t)
	ctx := t.Context()
	for i, tags := range [][]string{{"a"}, {"a", "b"}, {"b"}} {
		id, _, err := s.CreateNode(ctx, strings.Join(tags, "+"), hash(byte(i+1)))
		if err != nil {
			t.Fatal(err)
		}
		setTags(t, s, id, tags...)
	}
	for _, c := range []struct {
		filter []string
		want   []string
	}{
		{[]string{"a", "b"}, []string{"a+b"}},
		{[]string{"b", "a"}, []string{"a+b"}},
		{[]string{"a"}, []string{"a", "a+b"}},
		{[]string{"b"}, []string{"a+b", "b"}},
		{nil, []string{"a", "a+b", "b"}},
		{[]string{}, []string{"a", "a+b", "b"}},
		{[]string{"nope"}, []string{}},
		{[]string{"a", "nope"}, []string{}},
		// 折叠后重复的名字只算一个所选标签。
		{[]string{"a", "A"}, []string{"a", "a+b"}},
	} {
		nodes, err := s.ListNodesByTags(ctx, c.filter)
		if err != nil {
			t.Fatal(err)
		}
		if got := nodeNames(nodes); !slices.Equal(got, c.want) {
			t.Errorf("filter %q: nodes %q, want %q", c.filter, got, c.want)
		}
	}
}

// db 与 DB 是同一个标签，沿用先建的写法；节点的标签按折叠后的名字排序。
func TestTagsFoldCaseAndKeepTheFirstSpelling(t *testing.T) {
	s, _ := open(t)
	ctx := t.Context()
	first, _, _ := s.CreateNode(ctx, "first", hash(1))
	second, _, _ := s.CreateNode(ctx, "second", hash(2))
	setTags(t, s, first, "db", "Web", "api")
	setTags(t, s, second, "DB", "web")
	for _, c := range []struct {
		id   int64
		want []string
	}{{first, []string{"api", "db", "Web"}}, {second, []string{"db", "Web"}}} {
		if n, err := s.GetNode(ctx, c.id); err != nil || !slices.Equal(n.Tags, c.want) {
			t.Errorf("node %d tags = %q %v, want %q", c.id, n.Tags, err, c.want)
		}
	}
	tags, err := s.ListTags(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if want := []Tag{{"api", 1}, {"db", 2}, {"Web", 2}}; !slices.Equal(tags, want) {
		t.Fatalf("ListTags = %v, want %v", tags, want)
	}
	nodes, err := s.ListNodesByTags(ctx, []string{"dB", "WEB"})
	if err != nil || !slices.Equal(nodeNames(nodes), []string{"first", "second"}) {
		t.Fatalf("filter by other spellings: %q %v", nodeNames(nodes), err)
	}
}

// 整体替换：新集合替掉旧集合，空集合清空；标签行不随最后一个关联消失。
func TestUpdateNodeReplacesTheTagSet(t *testing.T) {
	s, _ := open(t)
	ctx := t.Context()
	id, _, _ := s.CreateNode(ctx, "n", hash(1))
	setTags(t, s, id, "a", "b")
	setTags(t, s, id, "b", "c")
	if n, _ := s.GetNode(ctx, id); !slices.Equal(n.Tags, []string{"b", "c"}) {
		t.Fatalf("after replacement: %q", n.Tags)
	}
	setTags(t, s, id)
	if n, _ := s.GetNode(ctx, id); n.Tags != nil {
		t.Fatalf("after clearing: %q", n.Tags)
	}
	tags, err := s.ListTags(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if want := []Tag{{"a", 0}, {"b", 0}, {"c", 0}}; !slices.Equal(tags, want) {
		t.Fatalf("ListTags = %v, want %v", tags, want)
	}
}

// 删除标签只解除关联：节点仍在、别的标签不动；按任一写法都能删，删不存在的标签得 ErrNotFound。
func TestDeleteTagOnlyDetachesIt(t *testing.T) {
	s, _ := open(t)
	ctx := t.Context()
	id, _, _ := s.CreateNode(ctx, "n", hash(1))
	setTags(t, s, id, "db", "web")
	if err := s.DeleteTag(ctx, "DB"); err != nil {
		t.Fatal(err)
	}
	n, err := s.GetNode(ctx, id)
	if err != nil || !slices.Equal(n.Tags, []string{"web"}) {
		t.Fatalf("node after deleting its tag: %+v %v", n, err)
	}
	if tags, _ := s.ListTags(ctx); !slices.Equal(tags, []Tag{{"web", 1}}) {
		t.Fatalf("ListTags = %v", tags)
	}
	if err := s.DeleteTag(ctx, "db"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("deleting a missing tag: %v", err)
	}
	var rows int
	if err := s.r.QueryRow("SELECT COUNT(*) FROM node_tag").Scan(&rows); err != nil || rows != 1 {
		t.Fatalf("node_tag rows = %d %v, want 1", rows, err)
	}
}

// 删节点后它的关联行一行不剩，别的节点的关联与标签本身都在。
func TestDeleteNodeRemovesItsTagRows(t *testing.T) {
	s, _ := open(t)
	ctx := t.Context()
	gone, _, _ := s.CreateNode(ctx, "gone", hash(1))
	kept, _, _ := s.CreateNode(ctx, "kept", hash(2))
	setTags(t, s, gone, "a", "b")
	setTags(t, s, kept, "a")
	if err := s.DeleteNode(ctx, gone); err != nil {
		t.Fatal(err)
	}
	var rows int
	if err := s.r.QueryRow("SELECT COUNT(*) FROM node_tag WHERE node_id = ?", gone).Scan(&rows); err != nil || rows != 0 {
		t.Fatalf("node_tag rows of the deleted node = %d %v", rows, err)
	}
	if tags, _ := s.ListTags(ctx); !slices.Equal(tags, []Tag{{"a", 1}, {"b", 0}}) {
		t.Fatalf("ListTags = %v", tags)
	}
}

// TagFold 的等价关系与 strings.EqualFold（简单折叠）相同。
func TestTagFoldMatchesEqualFold(t *testing.T) {
	words := []string{"db", "DB", "Db", "σ", "ς", "Σ", "k", "K", "K", "ß", "ss", "SS", "ẞ", "客户A", "客户a", "İ", "i", "I", "ı"}
	for _, a := range words {
		for _, b := range words {
			if same, want := TagFold(a) == TagFold(b), strings.EqualFold(a, b); same != want {
				t.Errorf("%q vs %q: same key %v, EqualFold %v", a, b, same, want)
			}
		}
	}
}
