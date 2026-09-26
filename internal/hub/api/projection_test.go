package api

import (
	"testing"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protodesc"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/descriptorpb"
	"google.golang.org/protobuf/types/dynamicpb"

	probev1 "github.com/xjetry/probe/gen/probe/v1"
)

// projectionFixtures 造几种单字段消息：Src 是 int32 a；其余各在一处与它不对齐。
func projectionFixtures(t *testing.T) protoreflect.FileDescriptor {
	t.Helper()
	i32 := descriptorpb.FieldDescriptorProto_TYPE_INT32.Enum()
	i64 := descriptorpb.FieldDescriptorProto_TYPE_INT64.Enum()
	single := descriptorpb.FieldDescriptorProto_LABEL_OPTIONAL.Enum()
	many := descriptorpb.FieldDescriptorProto_LABEL_REPEATED.Enum()
	field := func(name string, typ *descriptorpb.FieldDescriptorProto_Type, label *descriptorpb.FieldDescriptorProto_Label) *descriptorpb.FieldDescriptorProto {
		return &descriptorpb.FieldDescriptorProto{Name: proto.String(name), Number: proto.Int32(1), Type: typ, Label: label, JsonName: proto.String(name)}
	}
	msg := func(name string, f *descriptorpb.FieldDescriptorProto) *descriptorpb.DescriptorProto {
		return &descriptorpb.DescriptorProto{Name: proto.String(name), Field: []*descriptorpb.FieldDescriptorProto{f}}
	}
	// proto3 optional 由一个合成 oneof 承载 presence。
	optional := msg("WithPresence", field("a", i32, single))
	optional.Field[0].Proto3Optional, optional.Field[0].OneofIndex = proto.Bool(true), proto.Int32(0)
	optional.OneofDecl = []*descriptorpb.OneofDescriptorProto{{Name: proto.String("_a")}}
	fd, err := protodesc.NewFile(&descriptorpb.FileDescriptorProto{
		Name: proto.String("projection_fixtures.proto"), Package: proto.String("projfix"), Syntax: proto.String("proto3"),
		MessageType: []*descriptorpb.DescriptorProto{
			msg("Src", field("a", i32, single)),
			msg("Renamed", field("z", i32, single)),
			msg("Wider", field("a", i64, single)),
			msg("Repeated", field("a", i32, many)),
			optional,
		},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	return fd
}

func TestNewProjectionRejectsMisalignedFields(t *testing.T) {
	fd := projectionFixtures(t)
	desc := func(name string) protoreflect.MessageDescriptor { return fd.Messages().ByName(protoreflect.Name(name)) }
	for _, c := range []struct{ dst, want string }{
		{"Renamed", "has no counterpart named z"},
		{"Wider", "does not match"},
		{"WithPresence", "does not match"},
		{"Repeated", "only singular scalar fields"},
	} {
		t.Run(c.dst, func(t *testing.T) {
			expectPanic(t, c.want, func() { newProjection(dynamicpb.NewMessageType(desc(c.dst)), desc("Src")) })
		})
	}
	newProjection(dynamicpb.NewMessageType(desc("Src")), desc("Src"))
}

// 只复制源里存在的字段：optional 缺失仍是缺失，显式的 0 仍是 0；目标没有的字段（boot_id）不出现。
func TestProjectionKeepsPresenceAndDropsUndeclaredFields(t *testing.T) {
	p := newProjection((&probev1.PublicMetrics{}).ProtoReflect().Type(), (&probev1.Metrics{}).ProtoReflect().Descriptor())
	got := p.apply(&probev1.Metrics{BootId: "b", MemUsed: proto.Uint64(0), Load1: proto.Float64(0.5)}).(*probev1.PublicMetrics)
	if want := (&probev1.PublicMetrics{MemUsed: proto.Uint64(0), Load1: proto.Float64(0.5)}); !proto.Equal(got, want) || got.CpuPct != nil {
		t.Fatalf("projected = %v, want %v", got, want)
	}
}
