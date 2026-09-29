package api

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	"connectrpc.com/connect"

	heronv1 "github.com/xjetry/heron-probe/gen/heron/v1"
	"github.com/xjetry/heron-probe/internal/hub/sanitize"
	"github.com/xjetry/heron-probe/internal/hub/store"
)

const (
	maxTagRunes = 64
	// maxTagsPerNode 是每个节点的标签数上限，也是过滤条件里不同标签数的上限：交集只可能匹配同时挂着全部所选标签的
	// 节点，所选多于任何节点能挂的个数时注定为空。
	maxTagsPerNode = 16
)

// cleanTag 是标签名的唯一校验（§10），UpdateNode、ListNodes 的过滤与 DeleteTag 共用：去首尾空白后 1–64 个字符、不含
// 控制字符。控制字符拒绝而不是剔除：标签名由运维亲手输入，剔除会让存下的名字与输入不同而无人察觉；拒绝的字符集与
// 节点名清洗时剔除的同一个口径（sanitize.IsControl）。
func cleanTag(field, raw string) (string, error) {
	name := strings.TrimSpace(raw)
	if strings.ContainsFunc(name, sanitize.IsControl) {
		return "", invalid("%s: must not contain control characters; got %q", field, raw)
	}
	if n := utf8.RuneCountInString(name); n == 0 || n > maxTagRunes {
		return "", invalid("%s: must be 1–%d characters after trimming whitespace; got %d", field, maxTagRunes, n)
	}
	return name, nil
}

// cleanTags 逐个校验并按 store.TagFold 去重，保留每组里第一个出现的写法；去重后多于 maxTagsPerNode 个即拒绝。
// 上限按去重后计：[db, DB] 是同一个标签，不该占两个名额。
func cleanTags(field string, raw []string) ([]string, error) {
	var out []string
	seen := map[string]bool{}
	for i, r := range raw {
		name, err := cleanTag(fmt.Sprintf("%s[%d]", field, i), r)
		if err != nil {
			return nil, err
		}
		if fold := store.TagFold(name); !seen[fold] {
			seen[fold] = true
			out = append(out, name)
		}
	}
	if len(out) > maxTagsPerNode {
		return nil, invalid("%s: at most %d distinct tags (case-insensitive); got %d", field, maxTagsPerNode, len(out))
	}
	return out, nil
}

func (s *Service) ListTags(ctx context.Context, _ *connect.Request[heronv1.ListTagsRequest]) (*connect.Response[heronv1.ListTagsResponse], error) {
	tags, err := s.store.ListTags(ctx)
	if err != nil {
		s.log.Error("listing tags failed", "err", err)
		return nil, internalError("listing tags failed")
	}
	out := make([]*heronv1.Tag, 0, len(tags))
	for _, t := range tags {
		out = append(out, &heronv1.Tag{Name: t.Name, NodeCount: uint32(t.Nodes)})
	}
	return connect.NewResponse(&heronv1.ListTagsResponse{Tags: out}), nil
}

func (s *Service) DeleteTag(ctx context.Context, req *connect.Request[heronv1.DeleteTagRequest]) (*connect.Response[heronv1.DeleteTagResponse], error) {
	name, err := cleanTag("name", req.Msg.GetName())
	if err != nil {
		return nil, err
	}
	err = s.store.DeleteTag(ctx, name)
	if errors.Is(err, store.ErrInUse) {
		return nil, connect.NewError(connect.CodeFailedPrecondition, err)
	}
	if errors.Is(err, store.ErrNotFound) {
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("tag %q does not exist", name))
	}
	if err != nil {
		s.log.Error("deleting tag failed", "err", err)
		return nil, internalError("deleting tag failed")
	}
	s.log.Info("tag deleted", "tag", name)
	return connect.NewResponse(&heronv1.DeleteTagResponse{}), nil
}
