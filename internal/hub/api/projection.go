package api

import (
	"fmt"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
)

// projection 把一种消息按字段名投影成它的公开形态。字段集合由目标（公开）消息决定：源消息以后新增的字段
// 不会出现在公开消息里，除非公开消息也声明它——"默认私有"由此落在类型上，而不是一处可能漏改的逐字段拷贝。
// newProjection 逐字段核对名字、类型、基数与 presence，任一不符即 panic：描述符来自生成代码，
// 只有改了 proto 却没对齐时才会发生，那时 hub 在构造 Public 时就起不来。
type projection struct {
	dst    protoreflect.MessageType
	fields [][2]protoreflect.FieldDescriptor // {目标字段, 源字段}
}

func newProjection(dst protoreflect.MessageType, src protoreflect.MessageDescriptor) projection {
	p := projection{dst: dst}
	fields := dst.Descriptor().Fields()
	for i := 0; i < fields.Len(); i++ {
		d := fields.Get(i)
		s := src.Fields().ByName(d.Name())
		switch {
		case s == nil:
			panic(fmt.Sprintf("%s has no counterpart named %s in %s", d.FullName(), d.Name(), src.FullName()))
		case !projectable(d) || !projectable(s):
			panic(fmt.Sprintf("%s: only singular scalar fields can be projected", d.FullName()))
		case d.Kind() != s.Kind() || d.HasPresence() != s.HasPresence():
			panic(fmt.Sprintf("%s (%v, presence %v) does not match %s (%v, presence %v)",
				d.FullName(), d.Kind(), d.HasPresence(), s.FullName(), s.Kind(), s.HasPresence()))
		}
		p.fields = append(p.fields, [2]protoreflect.FieldDescriptor{d, s})
	}
	return p
}

// projectable 限于单值标量：消息、枚举、列表与 map 的逐项语义各不相同，公开消息目前只需要标量。
func projectable(f protoreflect.FieldDescriptor) bool {
	if f.Cardinality() == protoreflect.Repeated {
		return false
	}
	switch f.Kind() {
	case protoreflect.MessageKind, protoreflect.GroupKind, protoreflect.EnumKind:
		return false
	}
	return true
}

// apply 只复制源里存在的字段：optional 缺失仍是缺失，显式的 0 仍是 0。
func (p projection) apply(src proto.Message) proto.Message {
	sm := src.ProtoReflect()
	out := p.dst.New()
	for _, f := range p.fields {
		if sm.Has(f[1]) {
			out.Set(f[0], sm.Get(f[1]))
		}
	}
	return out.Interface()
}
