package api

import (
	"bytes"
	"fmt"
	"slices"
	"strings"
	"testing"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/proto"

	probev1 "github.com/xjetry/probe/gen/probe/v1"
)

func updateTags(t *testing.T, h *harness, id int64, name string, public bool, tags ...string) (*probev1.Node, error) {
	t.Helper()
	req := &probev1.UpdateNodeRequest{Id: id, Name: name, Public: public, TrafficResetDay: 1, OfflineGraceS: proto.Uint32(0), Tags: tags}
	resp, err := h.admin.UpdateNode(t.Context(), connect.NewRequest(req))
	if err != nil {
		return nil, err
	}
	return resp.Msg.GetNode(), nil
}

func mustUpdateTags(t *testing.T, h *harness, id int64, name string, tags ...string) *probev1.Node {
	t.Helper()
	n, err := updateTags(t, h, id, name, false, tags...)
	if err != nil {
		t.Fatal(err)
	}
	return n
}

func listByTags(t *testing.T, h *harness, tags ...string) ([]string, error) {
	t.Helper()
	resp, err := h.admin.ListNodes(t.Context(), connect.NewRequest(&probev1.ListNodesRequest{Tags: tags}))
	if err != nil {
		return nil, err
	}
	names := []string{}
	for _, n := range resp.Msg.GetNodes() {
		names = append(names, n.GetName())
	}
	return names, nil
}

func listTags(t *testing.T, h *harness) []string {
	t.Helper()
	resp, err := h.admin.ListTags(t.Context(), connect.NewRequest(&probev1.ListTagsRequest{}))
	if err != nil {
		t.Fatal(err)
	}
	out := []string{}
	for _, tag := range resp.Msg.GetTags() {
		out = append(out, fmt.Sprintf("%s:%d", tag.GetName(), tag.GetNodeCount()))
	}
	return out
}

// 三个节点分别挂 {a}、{a,b}、{b}：多选取交集，空选择返回全部，不存在的标签让结果为空。
func TestListNodesFiltersByTagIntersection(t *testing.T) {
	h := newHarness(t, "")
	h.login(t)
	for _, tags := range [][]string{{"a"}, {"a", "b"}, {"b"}} {
		name := strings.Join(tags, "+")
		id, _ := h.createNode(t, name)
		mustUpdateTags(t, h, id, name, tags...)
	}
	for _, c := range []struct {
		filter []string
		want   []string
	}{
		{[]string{"a", "b"}, []string{"a+b"}},
		{[]string{"a"}, []string{"a", "a+b"}},
		{nil, []string{"a", "a+b", "b"}},
		{[]string{"nope"}, []string{}},
		{[]string{"A", " b "}, []string{"a+b"}},
	} {
		got, err := listByTags(t, h, c.filter...)
		if err != nil {
			t.Fatalf("filter %q: %v", c.filter, err)
		}
		if !slices.Equal(got, c.want) {
			t.Errorf("filter %q: nodes %q, want %q", c.filter, got, c.want)
		}
	}
}

// 过滤条件按标签名的规则校验；不同的标签多于 16 个时注定为空，按参数错误拒绝（折叠后重复的只算一个）。
func TestListNodesTagFilterValidation(t *testing.T) {
	h := newHarness(t, "")
	h.login(t)
	var seventeen, sixteenTwice []string
	for i := range 17 {
		seventeen = append(seventeen, fmt.Sprintf("t%d", i))
	}
	for i := range 16 {
		sixteenTwice = append(sixteenTwice, fmt.Sprintf("t%d", i), fmt.Sprintf("T%d", i))
	}
	for _, c := range []struct {
		filter []string
		code   connect.Code
		detail string
	}{
		{seventeen, connect.CodeInvalidArgument, "tags: at most 16 distinct tags"},
		{[]string{"a\x07b"}, connect.CodeInvalidArgument, "tags[0]: must not contain control characters"},
		{[]string{"ok", "  "}, connect.CodeInvalidArgument, "tags[1]: must be 1–64 characters"},
	} {
		_, err := listByTags(t, h, c.filter...)
		if codeOf(err) != c.code || !strings.Contains(err.Error(), c.detail) {
			t.Errorf("filter %q: %v, want %v mentioning %q", c.filter, err, c.code, c.detail)
		}
	}
	if got, err := listByTags(t, h, sixteenTwice...); err != nil || len(got) != 0 {
		t.Errorf("16 tags each spelled twice: %q %v, want an empty result", got, err)
	}
}

// db 与 DB 是同一个标签，沿用先建的写法；同一请求里折叠后重复的只留第一个；回显按折叠后的名字排序。
func TestTagsAreCaseInsensitiveAndKeepTheFirstSpelling(t *testing.T) {
	h := newHarness(t, "")
	h.login(t)
	first, _ := h.createNode(t, "first")
	second, _ := h.createNode(t, "second")
	if n := mustUpdateTags(t, h, first, "first", "db", "Web", "  api  "); !slices.Equal(n.GetTags(), []string{"api", "db", "Web"}) {
		t.Fatalf("first node tags = %q", n.GetTags())
	}
	if n := mustUpdateTags(t, h, second, "second", "DB", "客户A", "客户a", "WEB"); !slices.Equal(n.GetTags(), []string{"db", "Web", "客户A"}) {
		t.Fatalf("second node tags = %q, want the first spellings", n.GetTags())
	}
	if got, want := listTags(t, h), []string{"api:1", "db:2", "Web:2", "客户A:1"}; !slices.Equal(got, want) {
		t.Fatalf("ListTags = %q, want %q", got, want)
	}
}

// 每节点至多 16 个（按去重后计），名字 1–64 个字符、不含控制字符；被拒的更新什么都不写。
func TestUpdateNodeTagValidation(t *testing.T) {
	h := newHarness(t, "")
	h.login(t)
	id, _ := h.createNode(t, "n")
	mustUpdateTags(t, h, id, "n", "kept")
	var sixteen []string
	for i := range 16 {
		sixteen = append(sixteen, fmt.Sprintf("t%02d", i))
	}
	long := strings.Repeat("é", 64)
	for _, c := range []struct {
		tags   []string
		detail string
	}{
		{append(slices.Clone(sixteen), "t16"), "tags: at most 16 distinct tags (case-insensitive); got 17"},
		{[]string{long + "x"}, "tags[0]: must be 1–64 characters after trimming whitespace; got 65"},
		{[]string{"ok", ""}, "tags[1]: must be 1–64 characters after trimming whitespace; got 0"},
		{[]string{"a\x00b"}, "tags[0]: must not contain control characters"},
		{[]string{"a\tb"}, "tags[0]: must not contain control characters"},
		{[]string{"a‮b"}, "tags[0]: must not contain control characters"},
		{[]string{"a\u0085b"}, "tags[0]: must not contain control characters"},
	} {
		_, err := updateTags(t, h, id, "renamed", false, c.tags...)
		if codeOf(err) != connect.CodeInvalidArgument || !strings.Contains(err.Error(), c.detail) {
			t.Errorf("tags %q: %v, want InvalidArgument mentioning %q", c.tags, err, c.detail)
		}
	}
	if n := listedNode(t, h); n.GetName() != "n" || !slices.Equal(n.GetTags(), []string{"kept"}) {
		t.Fatalf("a rejected update wrote something: %v", n)
	}
	// 边界：恰好 16 个、16 个再加一个只差大小写的、64 个字符（首尾空白不计）都接受。
	for _, tags := range [][]string{sixteen, append(slices.Clone(sixteen), "T00"), {"  " + long + "\t"}} {
		if _, err := updateTags(t, h, id, "n", false, tags...); err != nil {
			t.Errorf("tags %q rejected: %v", tags, err)
		}
	}
	if n := listedNode(t, h); !slices.Equal(n.GetTags(), []string{long}) {
		t.Fatalf("tags after the last accepted update = %q", n.GetTags())
	}
}

// 删除标签只解除关联：节点仍在、Node.tags 不再含它；大小写不敏感；不存在的标签 NotFound。
func TestDeleteTagDetachesItFromNodes(t *testing.T) {
	h := newHarness(t, "")
	h.login(t)
	id, _ := h.createNode(t, "n")
	mustUpdateTags(t, h, id, "n", "db", "web")
	if _, err := h.admin.DeleteTag(t.Context(), connect.NewRequest(&probev1.DeleteTagRequest{Name: " DB "})); err != nil {
		t.Fatal(err)
	}
	if n := listedNode(t, h); n.GetId() != id || !slices.Equal(n.GetTags(), []string{"web"}) {
		t.Fatalf("node after deleting a tag: %v", n)
	}
	if got := listTags(t, h); !slices.Equal(got, []string{"web:1"}) {
		t.Fatalf("ListTags = %q", got)
	}
	_, err := h.admin.DeleteTag(t.Context(), connect.NewRequest(&probev1.DeleteTagRequest{Name: "db"}))
	if codeOf(err) != connect.CodeNotFound || !strings.Contains(err.Error(), `tag "db" does not exist`) {
		t.Fatalf("deleting a missing tag: %v", err)
	}
	_, err = h.admin.DeleteTag(t.Context(), connect.NewRequest(&probev1.DeleteTagRequest{Name: ""}))
	if codeOf(err) != connect.CodeInvalidArgument || !strings.Contains(err.Error(), "name: must be 1–64 characters") {
		t.Fatalf("deleting an empty name: %v", err)
	}
}

// 删节点后 node_tag 没有它的行，标签本身留着。
func TestDeleteNodeLeavesNoTagRows(t *testing.T) {
	h := newHarness(t, "")
	h.login(t)
	gone, _ := h.createNode(t, "gone")
	kept, _ := h.createNode(t, "kept")
	mustUpdateTags(t, h, gone, "gone", "a", "b")
	mustUpdateTags(t, h, kept, "kept", "a")
	if _, err := h.admin.DeleteNode(t.Context(), connect.NewRequest(&probev1.DeleteNodeRequest{Id: gone})); err != nil {
		t.Fatal(err)
	}
	if rows := rowCounts(t, h.store); rows["node_tag"] != 1 || rows["tag"] != 2 {
		t.Fatalf("node_tag rows = %d, tag rows = %d; want 1 and 2", rows["node_tag"], rows["tag"])
	}
	if got := listTags(t, h); !slices.Equal(got, []string{"a:1", "b:0"}) {
		t.Fatalf("ListTags = %q", got)
	}
}

// 公开节点的标签不出现在公开快照里（PublicNode 没有这个字段，字段允许列表见 public_test）。
func TestTagsStayOffThePublicSnapshot(t *testing.T) {
	h := newHarness(t, "")
	h.login(t)
	id, _ := h.createNode(t, "n")
	if _, err := updateTags(t, h, id, "n", true, "internal-billing-db"); err != nil {
		t.Fatal(err)
	}
	snap := pubGet(t, h, "GetSnapshot", jsonQuery("{}"), nil)
	if !bytes.Contains(snap.body, []byte(`"name":"n"`)) || bytes.Contains(snap.body, []byte("internal-billing-db")) || bytes.Contains(snap.body, []byte("tags")) {
		t.Fatalf("public snapshot must list the node without its tags: %s", snap.body)
	}
}
